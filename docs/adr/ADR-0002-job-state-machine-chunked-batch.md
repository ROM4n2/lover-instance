# ADR-0002：长任务用 SQLite analysis_jobs 状态机 + 块级批处理

- 状态：Accepted
- 日期：2026-09-08

## 背景（Context）

SKEP/RoBERTa 在 CPU 上对数万条消息做情感批处理耗时 30–60 分钟。vault-grill 评审（后端/前端/SRE 三席共判 RED）指出初版方案的"任务队列"只有名词没有设计：内存 channel + goroutine 在 Go 重启/崩溃后任务丢失且状态成谜；用户刷新页面后重复点击会触发两个推理进程互抢导致 OOM；没有取消路径（只能杀进程）；浏览器 POST 几分钟即超时，30 分钟后的报错报给已关闭的 socket；半满的 `sentiment` 表被直接聚合成"看似完整"的情感曲线，造成误导。

## 决策驱动

- 任务必须可持久化、可恢复、可取消、可单飞（防重复触发）。
- localhost、单用户、几十分钟级任务——不需要消息队列中间件或 WebSocket 的复杂度。
- 重试必须限定在幂等边界内；零值 `http.Client`（永不超时）是本库记录在案的头号 Go 事故模式。

## 考虑的选项

- **A. 内存 channel + goroutine**：重启丢任务、无法取消/单飞/恢复，否决。
- **B. 持久化任务表 + 块级批处理（采用）**：`analysis_jobs` 表为唯一权威状态。
- **C. 引入 Redis/Asynq 等消息队列 + WebSocket 进度**：对本地单用户属过度设计（YAGNI），否决。

## 决策（Decision）

采用 B：

- `analysis_jobs(talker, kind, status, total, done, error, ...)`，`status ∈ queued/running/done/failed/canceled/interrupted`；部分唯一索引 `uq_jobs_active ON (talker,kind) WHERE status IN ('queued','running')` 在 **DB 层兜底单飞**，冲突即返回已有 job id（服务器是唯一权威，不靠前端 disable 按钮）。
- 批大小 **128–256 条/批**（body <5MB）；**块级超时 5min**，`http.NewRequestWithContext` 传播取消（否则取消信号静默失效）；Go client 硬兜底 10min。
- **每块一个独立短事务**写 sentiment/daily_stats ＝ 检查点；待办集用 anti-join：`messages WHERE talker=? AND id NOT IN (SELECT msg_id FROM sentiment WHERE model_version=?)`，重启后续跑只处理缺失块、跳过已打分。
- 重试**仅块级**（connect reset/5xx/超时，指数退避 + full jitter，上限 2 次），**任务级不重试**（对 60min 任务重试＝再叠一个 60min）；契约写明 Python 纯函数无副作用。
- 进度：`GET /api/jobs/:id` 2–5s 轮询（绝对值 `done/total`，不用百分比）；`DELETE /api/jobs/:id` 取消，worker 每块间检查 `ctx.Err()`；启动时把**心跳已过期**的残留 `running` 置 `interrupted` 并可自动续跑。
- **活性判定（2026-10-20 修订）**：`running` 不等于"还活着"。启动时的回收只中断 `updated_at` 早于 `StaleAfter`（90s）的行，因此进程必须**独立于块**地心跳（节拍 10s），不能在块开始时才写一次——一个卡在 5 分钟块超时里的活进程，其行会看起来像死的。原措辞"启动时把残留 running 置 interrupted"是无条件的，那会让**第二个并发进程**把正在跑的任务抢走并让它与自己同时打分；`word_freq` 是累加语义，重叠块会翻倍。
- **半成品门控**：情感图表 JOIN jobs 状态，未 done 显示"分析中 42%（12,340/29,000）"或只渲染已分析区间，绝不用半满数据画完整曲线。
- SQLite：`WAL` + `busy_timeout(10000)` + `SetMaxOpenConns(1)`；**HTTP 期间不持有连接/事务**（先快照取 id 列表并 Close，再进入"HTTP→短事务写回"循环），否则唯一连接被长任务占满、所有读请求堵死。

## 后果（Consequences）

- 正面：重启可续跑、可取消、不双跑、失败可见、进度刷新不丢；状态机即文档。
- 代价：引入一张表与状态机复杂度——但这是 M2 开工前必须定稿的契约，事后补返工成本最高，值得。
