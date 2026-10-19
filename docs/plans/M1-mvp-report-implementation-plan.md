# M1 端到端最小闭环 + 首份 report.html 实施计划

> **Goal**: 打通「导入一个双人会话 → 落库 → 词典法分析 → 生成可双击打开、含热力图/词云/情感基线/免责面板的单文件 `report.html`」的端到端最小闭环。
> **Tech Stack**: Go 1.26（`go.mod` 声明 `go 1.26.0`——该行由 `modernc.org/sqlite` 拉高；实测连 v1.58.0 自身都要求 ≥1.25，故原写的「Go 1.24+」是错的）/ Python 3.11+ / SQLite（`modernc.org/sqlite`，免 CGO）/ Go `html/template` + `go:embed`。
> **Spec Reference**: [docs/specs/2026-09-08-wechat-relationship-analysis-design.md](../specs/2026-09-08-wechat-relationship-analysis-design.md) §数据模型 / §指标体系 M1 / §NLP 流水线 / §交付物；[ADR-0001](../adr/ADR-0001-compliance-sidecar-adapter.md)（合规边界与 M0 实测契约）/ [ADR-0002](../adr/ADR-0002-job-state-machine-chunked-batch.md)（块级批处理）/ [ADR-0003](../adr/ADR-0003-self-contained-html-report.md)（HTML 报告为分享交付物）/ [ADR-0004](../adr/ADR-0004-sentiment-honesty-cloud-llm-deny.md)（情感诚实化）
> **Global Constraints**:
> - **合规铁律**：本仓库不内置、不分发、不协助开发任何解密或密钥提取代码；数据只经 `internal/source/chatlogkeeper` 消费用户自行导出的 JSON。
> - **隐私**：`content` / `talker` / `nickname` / `wxid` **永不入日志**（只记 id/count/耗时）；真实聊天库、导出 JSON、`report.html` 一律不入库（`.gitignore` 已覆盖 `*.db` / `workmem/` / `*.report.html`）；SQLite 路径必须避开 OneDrive/坚果云等同步盘（README 红字警告）。
> - **数据正确性**：时间戳**固定东八区分桶**（`+8 hours`）；唯一键用 M0 冻结的 `server_id` + `source_offset` 兜底；增量走 **60s 回扫 + 自然键去重 upsert**，不得用水位整段跳过；**严禁零值 `http.Client{}`**。
> - **错误处理零静默吞**：任何失败路径必须显式返回错误或落地为可见状态；ingest **绝不允许以"成功 0 行"收场**。
> - **编码**：Python 入口 UTF-8 stdout（Windows GBK 免疫）；Go 卫语句提前返回、嵌套 ≤2 层。
> - **依赖**：Go 仅新增 `modernc.org/sqlite`（**钉 v1.58.0**——DSN 形态是在这个版本上实测的；`go mod tidy` 会解析到更高的 v1.60.1，实测 DSN 行为一致，但既然已在该版本验证就以它为准）；Python 运行期仅 `fastapi` / `uvicorn` / `jieba`，**测试期另需 `pytest` 与 `httpx`**（`fastapi.testclient` 依赖它，而它不在 fastapi 基础安装内）。**任何任务的 RED 步骤之前，必须先执行一次显式安装**（见 Task 4 Step 0）。
> **Branch**: **trunk 例外（显式声明）**——本仓库为纯本地仓库、**无远端**（`git remote -v` 为空，已实测），全部既有提交直接落在 `main`，PR 流程在本环境下不成立。故本计划所有任务在 `main` 上以原子提交推进，**不新建 feature 分支、不开 PR**。经用户 2026-10-20 明确确认。

---

## [*]️ 修订记录（2026-10-20，对抗性拷打后）

本计划初稿经 4 个镜头（依赖完整性 / API 正确性 / spec 覆盖 / Windows 坑）对抗性拷打，合并 1 条 blocker + 14 条 major + 7 条 minor。以下是**改变接口形状**的修订，逐条对应原缺陷：

| 原缺陷 | 修订 |
| --- | --- |
| `requirements.txt` 缺 `httpx`，`fastapi.testclient` 在 pytest 收集阶段即 ImportError | Global Constraints 补依赖清单；Task 4 新增 **Step 0「显式安装」**；Task 7 测试保留 `TestClient` |
| `word_freq` 表**无任何生产者**，词云必然为空且不报错 | Task 7 的 `/score` 响应改为**逐条返回 `tokens`**，Task 10 在每块短事务里累加写入 `word_freq`（`ON CONFLICT DO UPDATE cnt = cnt + excluded.cnt`），Task 11 取其 Top 200 |
| `/score` 请求体不带身份，`top_words.{self,partner}` 无法产出 | **从契约里删掉 `top_words`**：分自我/对方是 Go 的职责（Go 才知道 `is_self`），Python 回归纯文本函数 |
| Task 1 的 DSN 用 `net/url` 拼，Windows 上直接 `invalid uri authority: C:` | 见下方 **[实测] DSN 形态**，Task 1 的 Step 4 与测试双双改写 |
| 端到端路径里没有任何一步调用 `RebuildBehaviorMetrics` | Task 14 在 ingest 成功后**无条件**调用它（不依赖 sentiment），并写进 Consumes |
| Task 10 的 Consumes 把写入器 `UpsertMessages` 当读侧，且全计划无人提供"读待办/写 sentiment" | Task 10 改为 Consumes `Store.PendingMessages`；Produces 补 `PendingMessages` / `SaveSentimentScores` / `AddTokenCounts` |
| `cnsenti` 提供不了 DUTIR 的 1–9 强度 | **移除 `cnsenti` 依赖**；`intensity` 改为 HowNet 程度因子归一化，词表自建（spec 同步加勘误） |
| `daily_stats` 行为列与情感列被 `model_version` 拆成两行，Task 11 读任一行都缺一半 | Task 11 的 `Collect` **显式跨 `model_version` 各取所需**（行为行取计数、情感行取极性），并加断言钉住 |
| Task 2 的双分支去重（`source_msg_id` 为空走自然键）在 `CanonicalMessage` 上构造不出来 | Task 2 改为**单路径**（`source_msg_id := UniqueKey`，恒非空），自然键索引保留为 schema 预留并显式说明 |
| `--talker` 是必填入参，但 M1 无从得知该填什么（联系人库解不开，拿不到备注名） | `--talker` 改为**可选**：缺省走唯一 talker 自动推断；新增 `--list-talkers` |
| `.gitignore` 的 `data/` 吞掉 `nlp-service/data/` 全部词典文件 | `.gitignore` 加 `!nlp-service/data/` 例外（已实测生效）；Task 5/6 证据门新增 `git check-ignore` 断言 |
| Task 12「从官方渠道取得」无兜底，截断的 `.min.js` 能过大小的断言 | Task 12 改为**有序候选源 + 独立 SHA256 + 尾部完整性**，并修掉许可证记录错误 |
| 「零外部请求」的两处判据不一致，且 `grep -cE 'https?://'` 在 echarts 上必然 ≥6 | Task 13 改用**语义化断言**（剥离已内联的 vendor 脚本后再断言无 http），Task 15 复用同一条断言而非 grep |
| Task 10 待办集无 `msg_type` 过滤，系统/红包/撤回/媒体全进词典 | `PendingMessages` 按类型白名单过滤，并把**被排除类型的分布**显式写进报告，不静默丢弃 |
| spec §指标体系 M1 明列但全计划无产出的指标（emoji 频率 / 发起方占比 / 每日首末条 / 话轮平衡 / DUTIR 7 类占比 + 雷达） | **全部补齐**（Task 9 + Task 11），无一项降级为 Out of Scope |
| `.gitignore` 例外、`uvicorn app.main:app` 无对应模块级 `app`、`RejectSyncedPath` 死代码、未定义的 `Result`/`Health`/`Progress`、`echarts-wordcloud` 许可证记成 MIT | 逐条修正（Task 4 / 11 / 12 / 14） |

### 执行期偏离（2026-10-20，Task 1–15 实施后回填）

计划是写在上游任务之前的，实施过程中有若干接口形状被证实更好或不得不改。以下逐条回填，**以代码为准**——凡与本表单不符的计划正文，一律按本表读：

| 计划原文 | 实际实现 | 原因 |
| --- | --- | --- |
| `RebuildBehaviorMetrics(ctx, talker, modelVersion)` | `RebuildBehaviorMetrics(ctx, talker) error`，**返回 error 而非 nil** | 行为指标没有 `model_version` 维度可用（只有 `behavior-v1` 一个值），形参是死参数 |
| Task 11 从 `daily_stats` 两行各取所需 | `Store.BehaviorMetrics(ctx, talker)` **重算后返回结构体**，`daily_stats` 只服务 Dashboard | 同一份日聚合由两条派生路径产出会静默分歧，而这正是本计划反复强调的失效形态；报告只认一条 |
| `SaveSentimentScores` + `AddTokenCounts` 两个接口 | 合并为 `SaveScoredBlock(ctx, talker, modelVersion, period, rows, selfTokens, partnerTokens)`，**一个事务** | 分数与词频必须同一事务落地：词频按 `cnt = cnt + excluded.cnt` 累加，靠 anti-join 保证每块只写一次，分开写就可能在崩溃后只写一半 |
| Task 10 传 `[]nlp.Result` 给 store | `[]store.SentimentRow` | 分层：store 不该依赖 nlp 客户端的类型 |
| `BehaviorMetrics` 无区间字段 | 增加 `RangeStartMs` / `RangeEndMs` | 报告要写明覆盖区间，否则读者不知道这份报告对应哪段时间 |
| 计划未列 `SentimentTotals` / `WordFrequencies` / `EmotionDistribution` | store 新增这三个读接口 | Task 11 需要它们；`SentimentTotals` 从逐条分数算极性，不依赖 `RebuildSentimentMetrics` 是否已跑 |
| Task 9 的 `DailyFirstLastCoverage` | **取消**，改为 `DailyRow.FirstIsSelf` / `LastIsSelf` + 报告侧汇总 | 覆盖率概念多余；报告直接给「我开场/收尾各多少天」 |
| `ExternalRefs(html []byte) ([]string, error)` | `ExternalRefs(html []byte) []string` | 纯函数，无可失败之处；返回 error 只会让调用点写 `if err != nil` 的死分支 |
| `/health` 只报 `{status, model_loaded}` | 增加 `model_version`（加载中为 `null`） | 每条分数都以某个 model_version 入库并被报告印给读者，调用方必须从**持有词典的那个进程**取版本号；在 Go 侧再写一份常量就是第二个事实源 |
| `/score` 响应里的 `model_version` 只解码不校验 | `checkAlignment` 校验回显版本，不一致则整块失败 | 否则会把这批分数记在一个并没有产出它们的版本号下 |
| `tokenize()` 同时充当打分与计数视图 | 新增 `countable_tokens()` 供计数，**打分视图不变** | 打分的程度/否定窗口按 token 位置计距，从那条流里删 token 会让每个命中悄然靠近修饰词、改变极性 |
| File-Occupancy 表称 Task 10 修改 `metrics.go` | Task 10 新增 `sentiment_rows.go`，**未触碰 `metrics.go`** | 原表预判失准；`metrics.go` 只被 Task 9 写过 |

### [实测] DSN 形态（本机跑 `modernc.org/sqlite` v1.58.0 得出，非推测）

对 5 种路径形态（普通 / 含空格 / 含中文 / 含中文+空格 / 含 `#`）逐一实测，判据是**打开后 `PRAGMA database_list` 解析出的文件路径必须等于预期路径**：

| DSN 构造 | 结果 |
| --- | --- |
| `url.URL{Scheme:"file", Path: abs}` | ❌ **全部失败**：`invalid uri authority: C:` —— 盘符被当成 URI authority |
| `"file:" + abs + "?" + q`（裸路径拼接） | ⚠️ 前 4 种通过，**含 `#` 时静默解析到 `...\a`** —— 打开了另一个文件 |
| `url.URL{Scheme:"file", Opaque: abs}` | ⚠️ 同上，含 `#` 时同样静默跑偏 |
| `"file:/" + abs` / `"file:///" + abs` | ⚠️ 同上，含 `#` 时同样静默跑偏 |
| **`"file:" + (&url.URL{Path: filepath.ToSlash(abs)}).EscapedPath() + "?" + q`** | ✅ **5 种形态全部通过且解析到正确文件** |

**结论（Task 1 的硬性要求）**：DSN 必须用最后一行那种**转义路径**形式。前 4 种在含 `#` 的路径上会**静默打开另一个文件**——这是比"打开失败"危险得多的失效形态，因此 Task 1 的测试必须断言 `PRAGMA database_list` 的解析结果，而不只是断言"打开没报错"。

---

## [*]️ Decisions So Far

- [ADR-0001](../adr/ADR-0001-compliance-sidecar-adapter.md)：数据接入走外部 sidecar 适配器，仓库不含解密代码。**M0 实测已冻结契约**：数据源为 `chatlog-keeper` 导出 JSON；字段名 `server_id` / `source_offset` / `ts`(float 秒) / `conversation_id` / `sender_wxid` / `msg_type` / `is_self` / `content`；唯一键 `server_id` → `source_offset` 兜底（`uniqueKeyExtraFields` 为显式有序列表，**不得再加任何未实测字段**）。
- [ADR-0002](../adr/ADR-0002-job-state-machine-chunked-batch.md)：长任务须有状态机与块级批处理。**M1 只落块级批处理四要素**（批大小/块级超时/短事务检查点/块级重试），`analysis_jobs` 状态机表随迁移建好但编排留到 M2。
- [ADR-0003](../adr/ADR-0003-self-contained-html-report.md)：分享交付物是自包含单文件 HTML，非绑定本机的 Dashboard 链接。
- [ADR-0004](../adr/ADR-0004-sentiment-honesty-cloud-llm-deny.md)：情感结论诚实化命名 + 报告内置免责面板；M1 用词典法，**云端 LLM deny-by-default**。
- **D1（M1 定为 CLI，不做 REST）**：spec 的 `POST /api/analyze` 等 REST 面属于 Dashboard 时代（M3）。M1 交付物是报告文件，交互入口用 CLI `cmd/analyze` 即可，避免为无人调用的接口先建 HTTP 层（YAGNI）。REST/前端留 M3。
- **D2（词云用 ECharts 插件）**：内联 `echarts-wordcloud` 而非服务端渲染 SVG，理由是与报告内其余图表同库、配色与交互一致；代价是产物再增约 50KB。若实测单文件超出可接受体积，回落方案为服务端 SVG（记录于本计划的 Fog of War）。**许可证更正：`echarts` 为 Apache-2.0，`echarts-wordcloud` 为 ISC**（初稿两处均写成 MIT，错误）。
- **D3（不改动既有适配器契约）**：M1 直接复用 `internal/source/chatlogkeeper` 与 `internal/model`，**不改字段映射，也不给 `CanonicalMessage` 加字段**。由此推出一条 M1 的既有事实：`ParseRaw` 保证 `UniqueKey` **恒非空**（M0 实测 `source_offset` 100% 非空且唯一），故存储层只有**单一路径**——`source_msg_id := UniqueKey`。spec §数据模型的 `uq_msg_natural` 自然键索引仍随迁移建好，但它是**为将来无原生 id 的数据源预留的 schema 条款**，本数据源不会触发它；这一点由 Task 2 的一条断言显式钉住，不留悬空分支。
- **D4（指标口径以 spec §指标体系 M1 为准，逐条对齐）**：M1 交付 spec §指标体系 M1 的**全部**条目——描述（消息数/句长/类型分布/24×7 热力图/深夜占比/**emoji 表情频率**）、词频（**双方词云** Top 200）、会话（**发起方占比 / 每日首末条归属 / 话轮平衡**）、回复时延（中位数/P90/秒回率）、情感（**DUTIR 7 类占比 + 情绪雷达** + 模型眼中的正负消息比）。SKEP/RoBERTa（M2）、话题词云随时间（M4）、LSM/情绪惯性/滞后互相关/变点检测（已砍）均不在 M1。**唯一明确不符的是 spec 原文「双方词云……正/负各一朵」——两句自相矛盾，M1 按"双方"实现自我/对方两朵，极性分桶词云列入 Out of Scope。**
- **D5（`intensity` 的语义更正）**：spec 原文称 cnsenti 提供 DUTIR「1–9 强度」，**实测不成立**（cnsenti 0.0.7 只返回 7 类计数与 pos/neg 分值，无强度维度，DUTIR 本体以 pkl 内嵌且不含强度）。故 M1 弃用 cnsenti，`sentiment.intensity` 存 **HowNet 程度因子归一化值（0..1）**，列名保留但语义已在 Task 6 docstring 与 spec 勘误中写明。

---

## [*] 文件占用分区（File-Occupancy）

本计划的任务**全部按序执行**，不并行派发 —— 依赖链是 store → ingest → nlp → analyze → report → cli。因此不需要独占式文件表；但仍声明跨任务触碰同一文件的边界，防止 `/dfs-exec` 误判可并行：

| 文件 | 首个创建者 | 后续修改者 | 约束 |
| --- | --- | --- | --- |
| `internal/store/metrics.go` | Task 9 | 无（**实测 Task 10 未触碰，改为新增 `sentiment_rows.go`**） | Task 9 提交后其他任务不改本文件 |
| `internal/store/messages.go` | Task 2 | Task 3（加 `ingest_state.go`）、Task 10（加 `sentiment_rows.go`） | 后两者为**新文件**，不改 Task 2 的实现 |
| `backend-go/go.mod` | Task 1（加 `modernc.org/sqlite`） | 无 | 其余任务禁止再加依赖 |
| `internal/report/` | Task 11（model/collect） | Task 12（assets）、Task 13（render/templates） | 三者文件不重叠，但 Task 13 消费前两者的产物 |
| `nlp-service/app/` | Task 4（main/health） | Task 5（tokenize）、Task 6（sentiment_lexicon）、Task 7（score） | 各为独立新文件，不互相改写 |
| `fixtures/`、`internal/model/`、`internal/source/` | 已存在 | **无** | M1 **不修改**（D3） |

---

## [*] Active Frontier (Unblocked Tasks)

### Task 1: SQLite store 打开 + PRAGMA + schema 迁移 [Tier: 3-Critical] [Mode: AFK] [Role: TDD Builder]

*Tier 3 理由：schema 迁移是与后续所有任务耦合的不可逆结构决策，一旦落库后改列代价最高；须走 Maker-Checker 双审。*

**Files:**
- Create: `backend-go/internal/store/store.go`
- Create: `backend-go/internal/store/migrations.go`
- Create: `backend-go/internal/store/store_test.go`
- Modify: `backend-go/go.mod`（新增 `modernc.org/sqlite`）

**Interfaces:**
- Consumes: 无（首层）
- Produces:
  - `func Open(dsn string) (*Store, error)` —— 内部 `SetMaxOpenConns(1)`
  - `func (s *Store) Migrate(ctx context.Context) error` —— 幂等建表（`IF NOT EXISTS`）
  - `func (s *Store) Close() error`
  - `type Store struct{ db *sql.DB }` + `func (s *Store) DB() *sql.DB`（仅测试与同包使用）
  - `func DSN(path string) (string, error)` —— **转义路径**形式，见上方 [实测] 表最后一行；路径非绝对时先 `filepath.Abs`
  - `func RejectSyncedPath(path string) error` —— 命中同步盘时返回可执行的 error

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Zero-Lock DDL]`：迁移必须幂等（`IF NOT EXISTS`），重复 `Migrate` 不报错、不重建。
- [ ] `[Instinct: Single-Writer]`：`SetMaxOpenConns(1)` 必须在 `Open` 内完成，禁止由调用方决定。
- [ ] `[Instinct: Escaped-DSN]`：DSN 必须百分号转义路径。**实测**：未转义的形式在含 `#` 的路径上会静默打开另一个文件（`PRAGMA database_list` 可验），这是比报错更危险的失效。
- [ ] `[Instinct: Privacy-Path]`：`RejectSyncedPath` 的匹配集合必须命中**真实目录名**（`OneDrive`、`OneDrive - `、`Nutstore Files`、`BaiduNetdiskDownload`、`百度网盘`）以及 `%OneDrive%` / `%OneDriveCommercial%` 环境变量指向的前缀，不能只匹配中文产品名。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 1: SQLite store 打开 + PRAGMA + schema 迁移。
> Mode: AFK | Role: TDD Builder
> Goal: 用 `modernc.org/sqlite` 打开库、强制 WAL/busy_timeout/synchronous/foreign_keys 与 `SetMaxOpenConns(1)`，并幂等建立 spec §数据模型 的全部表。
> Target Files: Create `backend-go/internal/store/store.go`、`backend-go/internal/store/migrations.go`、Test `backend-go/internal/store/store_test.go`；Modify `backend-go/go.mod`（`go get modernc.org/sqlite@v1.58.0`，GOPROXY 为 `goproxy.cn`）。
> 表清单照 spec：messages / contacts / sentiment / daily_stats / activity_heatmap / conversations / word_freq / analysis_jobs / ingest_state，含 `uq_msg_source`、`uq_msg_natural`、`uq_jobs_active`、`idx_msg_talker_ts`。
> **相对 spec 的唯一 schema 增补**：`daily_stats` 增加两列 `first_is_self INTEGER` / `last_is_self INTEGER`（spec §指标体系 M1 要求"每日首/末条归属"而 DDL 无落点；属新建库，无迁移风险）。
> TDD Steps:
> 1. 写失败测试 `store_test.go`（RED）：`Open` 后 `PRAGMA journal_mode` 返回 `wal`；`PRAGMA busy_timeout` ≥10000；`PRAGMA foreign_keys` =1；`db.Stats().MaxOpenConnections == 1`；`Migrate` 连跑两次不报错；九张表 `sqlite_master` 均可查到。
> 2. **DSN 测试（关键，不得省）**：对 5 种路径形态（普通 / 含空格 / 含中文 / 含中文+空格 / 含 `#`）建库，断言 `PRAGMA database_list` 解析出的路径**等于**预期绝对路径。断言必须落在解析结果上，只断言"Open 没报错"会漏掉静默打开错文件的形态。
> 3. `RejectSyncedPath` 测试：含 `OneDrive` 的路径 → error；`Nutstore Files` → error；`BaiduNetdiskDownload` → error；`D:\Code\lover-instance\data.db` → nil。
> 4. `cd backend-go && go test ./internal/store/...` 验证失败。
> 5. 实现使绿（GREEN）。
> 6. 卫语句扁平化（≤2 层）。
> 7. `go test ./...` 全绿；`go mod tidy`。
> Return: 摘要 + MANDATORY Physical Execution Receipt（退出码 + 测试通过数 + `git diff --stat`）。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails with expected message**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten with guard clauses (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**（Command + Exit code: 0 + pass count + `git diff --stat`）
- [ ] **Step 7: Git atomic commit** `feat(store): add sqlite store with pragma and schema migration`

---

### Task 2: messages 幂等 upsert（单一路径 + 正文变更更新） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/store/messages.go`
- Create: `backend-go/internal/store/messages_test.go`

**Interfaces:**
- Consumes: `store.Store`（Task 1）、`model.CanonicalMessage`
- Produces:
  - `func ContentSHA1(content string) string` —— 归一化后（去首尾空白）算 SHA-1 十六进制
  - `type InsertResult struct{ Inserted, Skipped, Updated int }`
  - `func (s *Store) UpsertMessages(ctx context.Context, source string, msgs []model.CanonicalMessage) (InsertResult, error)` —— 单事务批量写入；`source_msg_id` **恒取 `msg.UniqueKey`**（`ParseRaw` 保证非空）；冲突时若正文有变化则更新正文并计入 `Updated`，否则计入 `Skipped`
  - `func (s *Store) CountMessages(ctx context.Context, talker string) (int, error)`
  - `func (s *Store) ListTalkers(ctx context.Context) ([]string, error)`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Silent-Loss]`：`Inserted + Skipped + Updated` 必须等于输入条数，否则返回 error。静默丢数据是最高级别缺陷。
- [ ] `[Instinct: Batch-Transaction]`：整批一个短事务；禁止逐条 `Exec` 不包事务。
- [ ] `[Instinct: Key-Always-Present]`：**不得**为 `source_msg_id` 为空的行写分支——`model.ParseRaw` 的契约是 `UniqueKey` 恒非空（M0 实测 `source_offset` 100% 非空唯一）。测试须断言"canonical 消息的 UniqueKey 恒非空"，让这条契约在任何适配器改动下都显式失效。
- [ ] `[Instinct: Content-Refresh]`：正文变更（撤回等）必须能写回，故冲突时用 `ON CONFLICT DO UPDATE SET content=excluded.content, content_sha1=excluded.content_sha1 WHERE messages.content IS NOT excluded.content`；正文未变时不得改动行（重复导入必须产生完全相同的库状态）。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 2: messages 幂等 upsert。
> Mode: AFK | Role: TDD Builder
> Goal: 把 canonical 消息批量写入 messages 表，重复导入产生完全相同的库状态，同时让正文变更（撤回）可写回。
> Target Files: Create `backend-go/internal/store/messages.go`、Test `backend-go/internal/store/messages_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：同一批消息 upsert 两次 → 第二次 `Inserted==0 && Updated==0` 且 `messages` 行数不变；`Inserted+Skipped+Updated==len(input)` 恒成立；同一 `UniqueKey` 但正文改过的行 → `Updated==1` 且库内正文为新值；两次跑完的库内容逐行一致（幂等）；`ContentSHA1("a") == ContentSHA1(" a ")`；**断言输入的每条 canonical 消息 `UniqueKey != ""`**。
> 2. `cd backend-go && go test ./internal/store/...` 验证失败。
> 3. 实现使绿（GREEN）；SQL 用参数绑定，禁止字符串拼接。
> 4. 卫语句扁平化。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(store): add idempotent message upsert with content refresh`

---

### Task 3: ingest 编排 —— 导出 → 落库 + 60s 回扫水位 + 静默空洞守卫 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/ingest/ingest.go`
- Create: `backend-go/internal/ingest/ingest_test.go`
- Create: `backend-go/internal/store/ingest_state.go`
- Create: `backend-go/internal/store/ingest_state_test.go`

**Interfaces:**
- Consumes: `chatlogkeeper.ParseMessages(io.Reader) ([]model.CanonicalMessage, error)`、`store.UpsertMessages`、`store.CountMessages`、`store.Store`
- Produces:
  - `type Options struct{ Source, Talker string; Reader io.Reader }` —— **`Talker` 为空是合法输入**：为空表示"导入导出里出现的全部 talker"，水位按 talker 分别维护（导出可能含多个会话；M1 的 CLI 一次只分析一个，但 ingest 不做这个裁剪）
  - `type Report struct{ Parsed, Inserted, Skipped, Updated int; Talkers []string; WindowStartMs int64 }`
  - `func Run(ctx context.Context, s *store.Store, opts Options) (Report, error)`
  - `func (s *Store) IngestState(ctx, source, talker string) (lastTs int64, ok bool, err error)` / `func (s *Store) SaveIngestState(ctx, source, talker string, lastTs int64, lastMsgID string) error`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Empty-Success-A]`：导出解析出 `Parsed == 0` → **必须返回 error**（空导出不是成功）。这条与下面那条互为独立断言，不得合并成一个前置条件。
- [ ] `[Instinct: No-Empty-Success-B]`：`Parsed > 0` 但该次调用**一条也没落库**（`Inserted+Updated == 0` 且 `Skipped == Parsed`）而**库内该 talker 此前总行数为 0** → 返回 error。这是"唯一键塌缩"这类静默丢数据的兜底告警。
- [ ] `[Instinct: Timezone-Bucket]`：窗口与水位一律 epoch ms，**不在此层做时区换算**（分桶只在指标层做）。
- [ ] `[Instinct: Rescan-Window]`：水位起点 = `last_ts - 60_000`，且**首次导入不受水位限制**。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 3: ingest 编排。
> Mode: AFK | Role: TDD Builder
> Goal: 读 chatlog-keeper 导出 → 规范化 → 按 60s 回扫窗口落库 → 更新 ingest_state 水位；空结果必须报错。
> Target Files: Create `backend-go/internal/ingest/{ingest.go,ingest_test.go}`、`backend-go/internal/store/{ingest_state.go,ingest_state_test.go}`。
> TDD Steps:
> 1. 失败测试（RED）：用 `fixtures/sample_chat.json` 全量 ingest → `Parsed==Inserted`（首次无水位）；再跑一次 → `Inserted==0, Updated==0, Skipped==Parsed`；显式传入 `last_ts` 后，早于 `last_ts-60s` 的消息被排除、恰好等于 `last_ts-60s` 的**被保留**（钉住边界不整段跳过）；**空导出（`[]`）→ 返回 error**；导出非空但全部是已存在行且该 talker 库内 0 行 → 返回 error；`Options.Talker == ""` 时能正确从导出推断出 talker 集合并且逐 talker 维护水位。
> 2. `cd backend-go && go test ./internal/ingest/... ./internal/store/...` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化；窗口计算用 `int64` 算术，禁止浮点。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(ingest): orchestrate export to store with rescan watermark`

---

### Task 4: nlp-service 脚手架 + GET /health [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `nlp-service/requirements.txt`
- Create: `nlp-service/requirements-dev.txt`
- Create: `nlp-service/app/__init__.py`
- Create: `nlp-service/app/main.py`
- Create: `nlp-service/app/health.py`
- Create: `nlp-service/tests/test_health.py`

**Interfaces:**
- Consumes: 无（首层）
- Produces:
  - `GET /health` → `{"status": "ready"|"loading", "model_loaded": bool}`
  - `create_app() -> FastAPI`（工厂函数，便于测试注入）
  - **模块级 `app = create_app()`**，使 `uvicorn app.main:app` 可用（Task 15 的启动命令依赖它）
  - 词典加载状态 `_LEXICON_STATE: dict`（M1 零模型，`model_loaded` 恒 false、`status` 由词典是否就绪决定）
  - `requirements.txt`：`fastapi` / `uvicorn` / `jieba`（**无 cnsenti**，见 D5）
  - `requirements-dev.txt`：`pytest` / `httpx`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: UTF8-Stdout]`：`main.py` **模块顶层**配置 `sys.stdout.reconfigure(encoding="utf-8")`，免疫 Windows GBK 崩溃。必须写在模块顶层而非 `if __name__ == "__main__":` 内，否则 `uvicorn app.main:app` 不会执行它。
- [ ] `[Instinct: No-File-Handle]`：本服务**绝不打开任何 SQLite 文件**（spec 硬约束），只收发 HTTP 批次。
- [ ] `[Instinct: Install-Before-Red]`：**任何 RED 步骤之前必须显式安装依赖**（Step 0）。此前没有任何任务执行过 `pip install`，而本机 `jieba` 未安装，RED 会以 ImportError 收场而非断言失败，TDD 循环失去意义。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 4: nlp-service 脚手架 + GET /health。
> Mode: AFK | Role: TDD Builder
> Goal: 建 FastAPI 微服务骨架与健康检查，区分 loading/ready。
> Target Files: Create `nlp-service/{requirements.txt, requirements-dev.txt, app/__init__.py, app/main.py, app/health.py}`、Test `nlp-service/tests/test_health.py`。
> TDD Steps:
> 0. **安装（必须先做）**：`python -m pip install -r nlp-service/requirements.txt -r nlp-service/requirements-dev.txt`，然后 `python -c "import jieba, httpx, pytest"` 必须成功。**`httpx` 不可省**——`fastapi.testclient` 依赖它，而它不在 fastapi 的基础依赖里；缺它会让整个 nlp-service 测试套件在 pytest 收集阶段就 ImportError。
> 1. 失败测试（RED）：用 `fastapi.testclient.TestClient(create_app())` 断言 `GET /health` 返回 200、含 `status` 与 `model_loaded` 两个键、`status ∈ {"loading","ready"}`；**另加一条 `from app.main import app; assert app is not None`** 钉住模块级入口；断言服务进程不 import sqlite3（AST 扫描 `app/`，禁止出现 `sqlite3` import）。
> 2. `cd nlp-service && PYTHONIOENCODING=utf-8 python -m pytest tests/ -q` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化。
> 5. 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt（退出码 + passed 数）。"

**Step Breakdown:**
- [ ] **Step 0: 安装依赖（显式 `pip install`，`import jieba, httpx, pytest` 自检）**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**（含 `git check-ignore nlp-service/` 的否定断言，见 Task 5）
- [ ] **Step 7: Git atomic commit** `feat(nlp): scaffold fastapi service with health endpoint`

---

### Task 5: 分词与词频（jieba + 停用词 + 自定义词典 + 微信表情映射） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `nlp-service/app/tokenize.py`
- Create: `nlp-service/data/stopwords.txt`
- Create: `nlp-service/data/custom_dict.txt`
- Create: `nlp-service/data/emoji_map.json`
- Create: `nlp-service/tests/test_tokenize.py`

**Interfaces:**
- Consumes: `jieba`
- Produces:
  - `def tokenize(text: str) -> list[str]` —— 去停用词、载入自定义词典后的词序列；**微信自定义表情 `[捂脸]` / `[旺柴]` 保留为一个 token**（下游 Go 靠"是否被方括号包裹"区分词云 token 与表情 token）
  - `def emoji_polarity(mark: str) -> float | None` —— 微信自定义表情的极性；**未知表情返回 `None`**（= 中性并标记，不得默认 0 与"已知中性"混淆）
  - `WORD_RE = re.compile(r"^\[.+\]$")` —— 判定"表情 token"的常量（Task 7 复用，保证两端同一口径）
  - ~~`word_counts`~~：**不提供**。词频聚合由 Go 侧按 `(talker, is_self, word)` 累加完成（Go 才知道 `is_self`），Python 保持纯文本函数。

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-False-Neutral]`：未知表情必须返回 `None` 而不是 `0.0`，否则下游无法区分"未知"与"确知中性"。
- [ ] `[Instinct: Emoji-Is-A-Token]`：`tokenize` 必须把 `[捂脸]` 原样保留在输出里，否则表情频率无从统计。
- [ ] `[Instinct: Tracked-Data-Dir]`：`nlp-service/data/` 必须是**入库交付物**。`.gitignore` 第 15 行的 `data/` 会连它一起忽略，`git add` 会静默成功但文件没进去。证据门必须显式验证。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 5: 分词与词频。
> Mode: AFK | Role: TDD Builder
> Goal: jieba 分词 + 停用词过滤 + 自定义词典（昵称/爱称/网络梗）+ 微信自定义表情极性映射。
> Target Files: Create `nlp-service/app/tokenize.py`、`nlp-service/data/{stopwords.txt, custom_dict.txt, emoji_map.json}`、Test `nlp-service/tests/test_tokenize.py`。
> TDD Steps:
> 1. 失败测试（RED）：停用词（的/了/是）被滤除；自定义词典词（如"宝子"）作为**单个 token** 出现而非被切开；`[捂脸]` 在 token 序列里**原样出现**且返回非 `None` 极性；`[不存在的表情]` 返回 `None`；`WORD_RE` 能匹配 `[捂脸]` 且不匹配普通词。
> 2. **入库证据门（关键）**：`git check-ignore -q nlp-service/data/stopwords.txt` 必须以退出码 1 结束（= 未被忽略）；并断言 `git status --short` 里能看到这三个 data 文件待提交。这条直接对应"提交静默漏掉全部数据文件、新 clone 上测试全崩"这一缺陷。
> 3. `cd nlp-service && PYTHONIOENCODING=utf-8 python -m pytest tests/ -q` 验证失败。
> 4. 实现使绿（GREEN）。
> 5. 卫语句扁平化；词典文件用 UTF-8 显式 `encoding=` 打开。
> 6. 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt + `git status --short` 原始输出。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**（含 `git check-ignore` 否定断言 + `git status --short`）
- [ ] **Step 7: Git atomic commit** `feat(nlp): add tokenizer with stopwords and wechat emoji map`

---

### Task 6: 词典情感基线（自建 7 类情绪表 + HowNet 程度/否定/转折） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `nlp-service/app/sentiment_lexicon.py`
- Create: `nlp-service/data/emotion_lexicon.txt`（自建，7 类情绪词表）
- Create: `nlp-service/data/hownet_degree.txt`
- Create: `nlp-service/data/hownet_negation.txt`
- Create: `nlp-service/tests/test_sentiment_lexicon.py`

**Interfaces:**
- Consumes: `tokenize()`（Task 5）
- Produces:
  - `class Score(BaseModel)`：`polarity: float`（−1..1）、`emotion: str`（7 类之一：`好/乐/哀/怒/惧/恶/惊`，或 `"neutral"`）、`intensity: float`（**HowNet 程度因子归一化到 0..1**，非 DUTIR 1–9 本体强度，见 D5）
  - `def score(text: str) -> Score`
  - `MODEL_VERSION = "lexicon-hownet-v1"`
  - `EMOTIONS = ("好", "乐", "哀", "怒", "惧", "恶", "惊")` —— 7 类常量，Task 11 的情绪雷达按它对齐

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Honest-Naming]`：模块名与 docstring **禁止**出现"关系诊断""Gottman"等表述，只称"词典基线极性"（ADR-0004）。
- [ ] `[Instinct: Window-Bounded]`：否定词窗口固定前 3–5 词，系数 ×−0.5（非 ×−1）；程度副词 ×0.5–2.0。
- [ ] `[Instinct: No-Cnsenti-Claim]`：**不得**在 docstring 或注释里声称 `intensity` 是 DUTIR 的 1–9 强度。实测 cnsenti 0.0.7 无强度维度，故本任务自建 7 类词表，`intensity` 只表示 HowNet 程度因子。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 6: 词典情感基线。
> Mode: AFK | Role: TDD Builder
> Goal: 自建 7 类情绪词表 + HowNet 程度副词/否定窗口/转折规则，输出 polarity/emotion/intensity。
> Target Files: Create `nlp-service/app/sentiment_lexicon.py`、`nlp-service/data/{emotion_lexicon.txt, hownet_degree.txt, hownet_negation.txt}`、Test `nlp-service/tests/test_sentiment_lexicon.py`。
> 词表规模：M1 允许小规模精选（每类 ≥20 词），许可证自持（自建，不引第三方词库）。
> TDD Steps:
> 1. 失败测试（RED）：纯正向句 polarity>0；纯负向句 polarity<0；中性句 polarity==0 且 emotion=="neutral"；否定句（"不开心"）极性翻转但**幅度小于**肯定句（验证 ×−0.5 而非 ×−1）；程度副词放大（"非常开心" > "开心"）；`intensity` 落在 0..1；`EMOTIONS` 含且仅含 7 类；`MODEL_VERSION == "lexicon-hownet-v1"`。
> 2. `cd nlp-service && PYTHONIOENCODING=utf-8 python -m pytest tests/ -q` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化。
> 5. 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**（含 `git check-ignore` 否定断言）
- [ ] **Step 7: Git atomic commit** `feat(nlp): add self-built emotion lexicon sentiment baseline`

---

### Task 7: 批处理契约 POST /score [Tier: 3-Critical] [Mode: AFK] [Role: TDD Builder]

*Tier 3 理由：批处理契约是 spec 明示"事后返工成本最高"的部分（§NLP 流水线 5），接口形状一旦被 Go 侧依赖即难以更改。*

**Files:**
- Create: `nlp-service/app/score.py`
- Create: `nlp-service/tests/test_score.py`

**Interfaces:**
- Consumes: `score()`（Task 6）、`tokenize()`（Task 5）、`WORD_RE`（Task 5）
- Produces:
  - `POST /score` 请求体 `{"model_version": str, "items": [{"msg_id": int, "content": str}]}`，**items ≤ 256**
  - 响应体 `{"model_version": str, "results": [{"msg_id": int, "polarity": float, "emotion": str, "intensity": float, "tokens": [str]}]}`
  - `def score_batch(items: list[dict]) -> list[dict]` —— **纯函数、无副作用、不写任何存储**
  - 超限（>256 条或 body >5MB）返回 413 并附明确 `code`
  - **契约说明**：请求体**不带** `is_self` / `sender` / `talker`。自我/对方的切分由 Go 完成（Go 从 `messages.is_self` 读，Python 无从也不应知道"谁是谁"）。Python 只回逐条的 `tokens` 与分数，词频/表情频率的聚合与落库全在 Go。

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Pure-Function]`：`score_batch` 不得写文件/库/全局可变状态；测试须断言连续两次调用结果完全一致。
- [ ] `[Instinct: No-Identity-In-Request]`：请求体不得包含任何身份字段。发现需要身份才能完成的功能时，改的是 Go 侧聚合，不是往契约里塞字段。
- [ ] `[Instinct: No-Query-Log-Leak]`：日志不得输出 `content`；若需记录只记 `msg_id` 与条数。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 7: 批处理契约 POST /score。
> Mode: AFK | Role: TDD Builder
> Goal: 定义 Go↔Python 的批处理接口，256 条上限、纯函数、无副作用、错误必须显式。
> Target Files: Create `nlp-service/app/score.py`、Test `nlp-service/tests/test_score.py`。
> TDD Steps:
> 1. 失败测试（RED）：正常批次返回与输入等长的 results 且 `msg_id` 一一对应；**每条 result 都带 `tokens` 且为 list[str]**；`[捂脸]` 在 tokens 里原样出现；257 条 → 413 且 body 含 `code`；同一批次连调两次结果字节级一致（纯函数性）；空 items → 200 且 results 为空数组（不是 500）；**响应与请求均不含 `is_self`/`top_words`**（钉住契约里没有身份与分桶）。
> 2. `cd nlp-service && PYTHONIOENCODING=utf-8 python -m pytest tests/ -q` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化；Pydantic 模型做入参校验，超限在进入业务逻辑前就拒绝。
> 5. 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(nlp): add batched score contract with size guard`

---

### Task 8: Go NLP 客户端（块级超时 / 硬兜底 / 块级重试） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/nlp/client.go`
- Create: `backend-go/internal/nlp/client_test.go`

**Interfaces:**
- Consumes: `net/http`
- Produces:
  - `type Result struct{ MsgID int64; Polarity float64; Emotion string; Intensity float64; Tokens []string }` —— **显式定义**（Task 10 需要 `Tokens` 与 `Emotion` 才能落库与聚合，不得留悬空类型）
  - `type Health struct{ Status string; ModelLoaded bool }` —— **显式定义**
  - `type Client struct{ baseURL string; http *http.Client }`
  - `func NewClient(baseURL string) (*Client, error)` —— 内部构造**非零值** `http.Client`（connect 3s / 总 10min 硬兜底）
  - `type Item struct{ MsgID int64; Content string }`
  - `func (c *Client) Score(ctx context.Context, modelVersion string, items []Item) ([]Result, error)` —— 单批调用，块级 ctx 由调用方给 5min；返回结果长度必须与入参等长且 `MsgID` 一一对应，否则报错
  - `func (c *Client) Health(ctx context.Context) (Health, error)`
  - 块级重试内置：仅对 connect reset / 5xx / 超时重试，指数退避 + full jitter，**上限 2 次**

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Zero-Client]`：`NewClient` 必须显式设置 `Timeout`，测试断言 `client.http.Timeout > 0`。
- [ ] `[Instinct: Block-Retry-Only]`：重试只发生在单块内；**任务级不得重试**（否则 60min 任务再叠 60min）。
- [ ] `[Instinct: Privacy-Log]`：错误信息中不得回显请求体内容。
- [ ] `[Instinct: Length-And-Id-Match]`：结果长度或 `MsgID` 对不上必须报错——静默错位会把 A 的分数记到 B 头上。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 8: Go NLP 客户端。
> Mode: AFK | Role: TDD Builder
> Goal: 按 ADR-0002 的四要素实现块级调用：批大小由调用方定、块级超时 5min、硬兜底 10min、块级重试指数退避+jitter 上限 2。
> Target Files: Create `backend-go/internal/nlp/client.go`、Test `backend-go/internal/nlp/client_test.go`（用 `httptest.Server` 做假服务）。
> TDD Steps:
> 1. 失败测试（RED）：`NewClient` 返回的非零 `Timeout > 0`；假服务返回 200 时解析出等长结果且 `Tokens` 非 nil；假服务返回**条数不等**或 **MsgID 错位**的响应 → 返回 error；假服务连续返回 500 三次 → 返回 error 且**只尝试了 3 次**（1 次 + 2 重试）；假服务首次 500 后成功 → 返回成功；内容包含敏感串时错误信息**不含**该串；`Health` 能把 `loading` 与 `ready` 区分开。
> 2. `cd backend-go && go test ./internal/nlp/...` 验证失败。
> 3. 实现使绿（GREEN）；重试用 `select` 同时监听 `ctx.Done()`，退避等待可被打断。
> 4. 卫语句扁平化。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(nlp): add batched client with block timeout and retry`

---

### Task 9: 行为指标聚合（东八区分桶 / 热力图 / 会话 / 回复时延 / 会话类指标） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/store/metrics.go`
- Create: `backend-go/internal/store/metrics_test.go`

**Interfaces:**
- Consumes: `Store`、messages 表
- Produces:
  - `type DailyRow struct{ Day string; SelfMsgs, PartnerMsgs, SelfChars, PartnerChars int; FirstIsSelf, LastIsSelf bool; AvgPolarity float64; HasSentiment bool }`
  - `type HeatCell struct{ Weekday, Hour, SelfCount, PartnerCount int }`
  - `type ConversationRow struct{ StartMs, EndMs int64; Initiator string; MsgCount int }`
  - `type ReplyStats struct{ MedianMs, P90Ms int64; FastRatio float64; SampleCount int }`（**同秒计 0**）
  - `type TypeCount struct{ MsgType, Count int }`
  - `type BehaviorMetrics struct{ Daily []DailyRow; Heatmap []HeatCell; Conversations []ConversationRow; Reply ReplyStats; Types []TypeCount; LateNightRatio, InitiatorRatio, TurnBalanceCharRatio float64; AvgSelfLen, AvgPartnerLen float64; DailyFirstLastCoverage int }`
  - `func (s *Store) RebuildBehaviorMetrics(ctx context.Context, talker, modelVersion string) error` —— 单事务重建该 talker 的 `daily_stats`（行为列 + `first_is_self`/`last_is_self`）/ `activity_heatmap` / `conversations`。**`modelVersion` 由调用方传，但行为列固定写 `"behavior-v1"`**（见 Task 11 的跨版本读法）
  - `func (s *Store) BehaviorMetrics(ctx context.Context, talker, behaviorModelVersion string) (BehaviorMetrics, error)`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: CST-Bucket]`：所有日/时桶 SQL 必须写 `'unixepoch','+8 hours'`；测试须构造一条 UTC 23:30（= 东八区次日 07:30）的消息来钉住日界。
- [ ] `[Instinct: Rebuild-Not-Append]`：重建是**先删该 talker 分区再插**，避免重复导入造成指标翻倍。
- [ ] `[Instinct: Behavior-Version-Constant]`：行为行**只写** `model_version='behavior-v1'`。测试须断言库里 `daily_stats` 的 `behavior-v1` 行非空——这条钉住 Task 11 跨版本读法的前提。
- [ ] `[Instinct: Spec-Metric-Coverage]`：本任务必须产出 spec §指标体系 M1 会话栏点名的**发起方占比 / 每日首末条归属 / 话轮平衡**（初稿全缺，报告里会静默少掉这些指标）。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 9: 行为指标聚合。
> Mode: AFK | Role: TDD Builder
> Goal: 从 messages 聚合出日汇总（行为列 + 每日首末条归属）、168 格热力、会话切分、回复时延、发起方占比与话轮平衡，全部按东八区分桶。
> Target Files: Create `backend-go/internal/store/metrics.go`、Test `backend-go/internal/store/metrics_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：**日界钉死**——插入 UTC `2026-08-15T23:30Z` 的消息，断言其日桶为东八区 `2026-08-16`、小时桶为 `7`；插入 UTC `2026-08-15T16:00Z` → 日桶 `2026-08-16`、小时 `0`；`RebuildBehaviorMetrics` 连跑两次后行数不翻倍；热力图恒 168 格且 weekday∈0..6、hour∈0..23；同秒两条消息的回复时延记 0；5min 间隔切成两个会话；**每日首/末条归属**：构造一天内 self 先发、partner 后发 → `FirstIsSelf=true, LastIsSelf=false`；**发起方占比**：3 个会话里 2 个由 self 发起 → `InitiatorRatio≈0.667`；**话轮平衡**：self 字数:partner 字数 = 2:1 → `TurnBalanceCharRatio≈2.0`；`daily_stats` 的 `behavior-v1` 行存在且 `self_msgs` 非空。
> 2. `cd backend-go && go test ./internal/store/...` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化；分位数在 Go 侧算，SQL 只取有序时延序列。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(store): aggregate behavior metrics in CST buckets`

---

### Task 10: 情感打分驱动 + 词频/表情落库 + 半成品门控 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Modify: `backend-go/internal/store/metrics.go`（追加情感列回填）
- Create: `backend-go/internal/store/sentiment_rows.go`
- Create: `backend-go/internal/store/sentiment_rows_test.go`
- Create: `backend-go/internal/analyze/pipeline.go`
- Create: `backend-go/internal/analyze/pipeline_test.go`

**Interfaces:**
- Consumes: `nlp.Client.Score`、`nlp.Result`、`Store.RebuildBehaviorMetrics`（Task 9）、`Store` 的 messages/sentiment 表
- Produces（**注意：Consumes 里不再有 `UpsertMessages`**——它是写入器，且本任务根本不需要写 messages）:
  - `type PendingMsg struct{ ID int64; Content string; IsSelf bool; MsgType int }`
  - `func (s *Store) PendingMessages(ctx context.Context, talker, modelVersion string, limit int) ([]PendingMsg, error)` —— 待办集 = `messages WHERE talker=? AND id NOT IN (SELECT msg_id FROM sentiment WHERE model_version=?)`，**并按可分析类型白名单过滤**（M1 只送 `msg_type=1` 文本；系统/红包/撤回/纯媒体一律不进模型）。**取 pending 与随后释放连接之间不得持有事务。**
  - `func (s *Store) SaveSentimentScores(ctx context.Context, modelVersion string, rows []nlp.Result) error` —— 单短事务批写 `sentiment`
  - `func (s *Store) AddTokenCounts(ctx context.Context, talker, period string, self, partner map[string]int) error` —— 单短事务累加进 `word_freq`（`ON CONFLICT DO UPDATE SET cnt = cnt + excluded.cnt`）。**这是 `word_freq` 唯一的写入者**；表情 token（`[捂脸]` 形态）同样进这张表，报告层按方括号区分词云与表情频率
  - `func (s *Store) ExcludedTypeCounts(ctx context.Context, talker string) ([]TypeCount, error)` —— 被排除出情感分析的类型分布，供报告显式标注
  - `type Progress struct{ Scored, Total int }` —— **显式定义**
  - `func RunSentiment(ctx context.Context, s *store.Store, c *nlp.Client, talker, modelVersion string, batchSize int) (Progress, error)`
  - `type Coverage struct{ Scored, Total int }` + `func (s *Store) SentimentCoverage(ctx, talker, modelVersion string) (Coverage, error)`
  - `func (s *Store) RebuildSentimentMetrics(ctx, talker, modelVersion string) error` —— 回填 `daily_stats` **该 model_version 行**的情感列（`avg_polarity` / `pos_count` / `neg_count`）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Conn-During-HTTP]`：快照与 HTTP 之间必须释放连接，否则唯一连接被长任务占满、所有读请求堵死（spec §SQLite 落地约定硬约束）。
- [ ] `[Instinct: Half-Baked-Gate]`：`Coverage.Scored < Coverage.Total` 时，报告层必须标注覆盖范围，**绝不用半满 sentiment 画看似完整的曲线**。
- [ ] `[Instinct: Word-Freq-Producer]`：`word_freq` 的写入必须发生在**每块回写 sentiment 的同一个短事务**里，靠 anti-join 保证"续跑时每块只贡献一次"。测试须断言：跑完一遍后 `word_freq` 非空；**再跑一遍计数不翻倍**（anti-join 使第二遍零块）。
- [ ] `[Instinct: Type-Filter-Visible]`：类型过滤不得静默——被排除的类型分布必须可查、并进报告。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 10: 情感打分驱动 + 词频/表情落库 + 半成品门控。
> Mode: AFK | Role: TDD Builder
> Goal: 反连接待办集驱动块级打分，每块一个短事务同时写 sentiment 与 word_freq，断点续跑免费；回填情感列；暴露覆盖率供门控。
> Target Files: Modify `backend-go/internal/store/metrics.go`；Create `backend-go/internal/store/{sentiment_rows.go,sentiment_rows_test.go}`、`backend-go/internal/analyze/{pipeline.go,pipeline_test.go}`。
> TDD Steps:
> 1. 失败测试（RED）：用假 NLP 服务（`httptest`）跑一轮 → 全部**可分析**消息被打分；**`word_freq` 非空且含自我/对方两侧**；类型非 1 的消息（system/红包/媒体）**未出现在 `PendingMessages`**、也未被打分；**再跑一轮 → 零次 HTTP 调用** 且 `word_freq` 计数**不翻倍**（钉住 anti-join 的检查点语义）；中途让假服务对第 2 块返回 500 → 函数返回 error，但第 1 块的 sentiment **与** word_freq 结果都已落库；`RebuildSentimentMetrics` 后 `daily_stats` 该 model_version 行 `avg_polarity` 非空；`SentimentCoverage` 能正确报出 `Scored < Total`；`ExcludedTypeCounts` 能报出被排除的类型分布。
> 2. `cd backend-go && go test ./internal/store/... ./internal/analyze/...` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(analyze): drive chunked sentiment scoring with resumable todo set`

---

### Task 11: 报告数据契约（单一聚合视图，只出聚合不出原始） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/report/model.go`
- Create: `backend-go/internal/report/collect.go`
- Create: `backend-go/internal/report/collect_test.go`

**Interfaces:**
- Consumes: `Store.BehaviorMetrics`、`Store.SentimentCoverage`、`Store.ExcludedTypeCounts`、**`Store.EmotionDistribution`（新增，读 `sentiment` 表）**、`daily_stats` / `activity_heatmap` / `conversations` / `word_freq` 表
- Produces:
  - `type MetaView struct{ TalkerHash string; RangeStartMs, RangeEndMs int64; BehaviorModelVersion, SentimentModelVersion string }`
  - `type SummaryView struct{ SelfMsgs, PartnerMsgs, SelfChars, PartnerChars int; AvgSelfLen, AvgPartnerLen float64; LateNightRatio, InitiatorRatio, TurnBalanceCharRatio float64; DailyFirstIsSelfCount, DailyLastIsSelfCount int; Reply ReplyView; Types []TypeCount }`
  - `type HeatCellView struct{ Weekday, Hour, SelfCount, PartnerCount int }`
  - `type WordsView struct{ Self, Partner []WordCount }` + `type WordCount struct{ Word string; Count int }` —— **Top 200**，由 `Collect` 从 `word_freq` 排序截取；**必须排除方括号表情 token**
  - `type EmojiView struct{ Self, Partner []WordCount }` —— 表情频率（同一张 `word_freq` 里的方括号 token）
  - `type EmotionView struct{ Counts map[string]int; Shares map[string]float64; PositiveRatio float64; Radar []float64 }` —— **DUTIR 7 类占比 + 情绪雷达**；`Radar` 按 `EMOTIONS` 固定顺序对齐
  - `type CoverageView struct{ Scored, Total int; Partial bool; ExcludedTypes []TypeCount }`
  - `type View struct{ Meta MetaView; Summary SummaryView; Heatmap []HeatCellView; Words WordsView; Emoji EmojiView; Emotion EmotionView; Coverage CoverageView; Disclaimer string }`
  - `func (s *Store) EmotionDistribution(ctx context.Context, talker, modelVersion string) (map[string]int, error)`
  - `func Collect(ctx context.Context, s *store.Store, talker, behaviorModelVersion, sentimentModelVersion string) (View, error)`
  - **契约**：`View` 的可序列化结果中**不得出现任何逐条消息**（只有计数/比率/聚合桶），测试用反射断言

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Aggregate-Only]`：报告数据层禁止携带 `Content` 字段；这是隐私防线而非风格问题。
- [ ] `[Instinct: Honest-Labels]`：正负比字段名必须表达"模型眼中的正负消息比"语义（不叫 Gottman），并在 View 中携带免责文案常量。
- [ ] `[Instinct: Cross-Version-Read]`：`daily_stats` 的**行为列**在 `model_version='behavior-v1'` 行、**情感列**在 sentiment 版本行，二者是两行。`Collect` 必须**显式各取所需**，不能只读其中一行——只读一行会让报告缺一半数据且不报错。测试须构造同一天同时存在两行的场景，断言 View 里**计数与极性同时非空**。
- [ ] `[Instinct: Spec-Metric-Coverage]`：本任务必须让 emoji 频率、会话类指标、7 类情绪占比 + 雷达**都进得去 View**（初稿这些数据算了也进不了报告）。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 11: 报告数据契约。
> Mode: AFK | Role: TDD Builder
> Goal: 把库里的聚合结果收敛成一个只含聚合值的 View 结构，供模板直接渲染。
> Target Files: Create `backend-go/internal/report/{model.go, collect.go}`、Test `backend-go/internal/report/collect_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：`Collect` 在真实 fixture 数据上返回非零汇总；**`Words.Self` 与 `Words.Partner` 都非空且长度 ≤200**；**`Emoji.Self`/`Emoji.Partner` 只含方括号 token**；**`Emotion.Counts` 的键落在 7 类 + neutral 内，`Radar` 长度 == 7，`Shares` 求和 ≈ 1.0**；**跨版本读**：同一天既有 behavior 行又有 sentiment 行时，`Summary.SelfMsgs > 0` **且** `Emotion.PositiveRatio` 有定义；反射遍历 `View` 的全部字段，断言不存在名为 `Content`/`Messages`/`Raw` 的字段；`Disclaimer` 非空且包含"非关系诊断"字样；`CoverageView` 能表达 `Scored < Total` 并携带 `ExcludedTypes`。
> 2. `cd backend-go && go test ./internal/report/...` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(report): define aggregate-only report view`

---

### Task 12: 内联图表资产 vendor [Tier: 1-Light] [Mode: AFK] [Role: Builder]

**Files:**
- Create: `backend-go/internal/report/assets/echarts.min.js`
- Create: `backend-go/internal/report/assets/echarts-wordcloud.min.js`
- Create: `backend-go/internal/report/assets/README.md`（记录来源 URL、版本、许可证、SHA256）

**Interfaces:**
- Consumes: 无
- Produces: `//go:embed assets/*.js` 可用的嵌入文件系统

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Supply-Chain]`：资产必须记录**确切版本号与 SHA256**。来源写成**有序候选**（1. `cdn.jsdelivr.net` → 2. `registry.npmjs.org` → 3. 官方 GitHub release），任一来源校验不过就换下一个。
- [ ] `[Instinct: Integrity-Not-Size]`：只断言"大小 >0"会**放行被截断的文件**（体积非 0、语法残缺），随后 `go:embed` 进报告、浏览器静默白屏。必须**独立校验 SHA256**（先查清官方发布物摘要，再比对下载结果），并检查文件**尾部完整**（min 产物以 `));` 或注释收尾）。
- [ ] `[Instinct: Correct-License]`：`echarts` 记 **Apache-2.0**，`echarts-wordcloud` 记 **ISC**（初稿两处都写成 MIT，是错的）。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 12: 内联图表资产 vendor。
> Mode: AFK | Role: Builder（Tier 1 快通道）
> Goal: 把 echarts 与 echarts-wordcloud 的发行版落到仓库内，供 go:embed 内联。
> Target Files: Create `backend-go/internal/report/assets/` 下两个 `.min.js` + `README.md`。
> Steps:
> 1. 按有序候选取得资产：`echarts@5` 与 `echarts-wordcloud@2`。首选 `https://cdn.jsdelivr.net/npm/<pkg>@<ver>/dist/<file>.min.js`（本机实测 200）；失败再试 `registry.npmjs.org` tarball；再失败试 GitHub release。
> 2. 校验：记录官方发布的 `dist.integrity`（npm registry 的 sha512）或已知 SHA256，与下载结果**比对**；另断言文件尾部完整（末 64 字符内出现 `));` 或 `*/`）。
> 3. 记录来源 URL、版本号、**许可证（echarts=Apache-2.0 / echarts-wordcloud=ISC）**、`sha256sum` 到 `assets/README.md`。
> 4. 写一个临时 Go 程序断言 `go:embed` 能读到两个文件且大小 >0；跑完删除该临时文件。
> Return: 摘要 + 物理凭据（两个文件的 `sha256sum` 原始输出 + 字节数 + 尾部片段）。"

**Step Breakdown:**
- [ ] **Step 1: 按有序候选取得两个资产文件并校验摘要**
- [ ] **Step 2: 记录来源/版本/许可证/SHA256 到 assets/README.md**
- [ ] **Step 3: Physical Evidence Gate**（`sha256sum` 原始输出 + 字节数 + 尾部片段）
- [ ] **Step 4: Git atomic commit** `chore(report): vendor echarts and wordcloud assets`

---

### Task 13: 自包含 report.html 模板与渲染 [Tier: 3-Critical] [Mode: AFK] [Role: TDD Builder]

*Tier 3 理由：这是 M1 的对外交付物本体，且"自包含"是一条一旦破坏就失去全部分享价值的硬属性。*

**Files:**
- Create: `backend-go/internal/report/templates/report.html.tmpl`
- Create: `backend-go/internal/report/render.go`
- Create: `backend-go/internal/report/render_test.go`

**Interfaces:**
- Consumes: `View`（Task 11）、`assets/*.js`（Task 12）
- Produces:
  - `func Render(w io.Writer, view View) error` —— `html/template` 渲染单文件；JS 与数据 JSON 内联
  - `func ExternalRefs(html []byte) ([]string, error)` —— **语义化外链检测器**，供本包测试与 Task 15 共用：剥离两个已知 vendor `<script>` 块后，收集 `<script src>` / `<link href>` / `<img src>` / 内联 `style` 与 `script` 中的 `url(...)` / `@import` / `fetch(` / `XMLHttpRequest(` / `new Image` 的目标，只要命中 `http://` / `https://` 就返回。测试断言其返回空。
  - 产物必须含：核心统计卡片、24×7 热力图、**双方词云 + 表情频率**、**会话类指标（发起方占比 / 每日首末条 / 话轮平衡）**、**情感基线（含 7 类占比 + 情绪雷达 + "模型眼中的正负消息比"）**、被排除类型分布、**固定免责面板**、覆盖范围标注（当 `Scored < Total`）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Zero-External-Ref]`：判据必须是**语义化**的（见 `ExternalRefs`），**不是** `grep -cE 'https?://'`——内联的 `echarts.min.js` 本身含 6 处 http(s) 串（w3.org 命名空间、Apache/LICENSE 注释），任何裸 grep 的期望值都必然落空。同时，仅检查 `src=`/`href=` 属性会漏掉 CSS `url()`、`@import`、内联 `fetch()`。
- [ ] `[Instinct: XSS-Escape]`：数据经 `template.JS` 注入时必须走 `json.Marshal` + 转义 `<`/`>`，不得直接拼接字符串。
- [ ] `[Instinct: Honest-Panel]`：免责面板是**常量**，不得由数据决定是否渲染。
- [ ] `[Instinct: Charts-Have-Data]`：测试必须断言词云/表情/情绪三块的数据**非空**——只断言"容器 id 存在"会让空图通过（初稿正是如此，`word_freq` 无生产者时报告会静默出空图）。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 13: 自包含 report.html 模板与渲染。
> Mode: AFK | Role: TDD Builder
> Goal: 渲染出双击/离线可开、零外部请求的单文件报告。
> Target Files: Create `backend-go/internal/report/templates/report.html.tmpl`、`backend-go/internal/report/render.go`、Test `backend-go/internal/report/render_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：渲染 fixture 数据 → 产物含 `<html`、含内联 `<script>` 数据块、含免责面板文案、含热力图/词云/情绪雷达/会话指标的容器 id；**`ExternalRefs(html)` 返回空切片**；产物中检索 `echarts` 字样出现；**对 `View` 注入非空的 Words/Emoji/Emotion 后，产物里对应数据块非空**（防空图假绿）；构造含 `<script>alert(1)</script>` 的边界数据 → 产物中该串被转义、不形成可执行标签。
> 2. **反向验证（关键）**：临时往模板里插一个 `<img src="https://example.com/x.png">`，确认 `ExternalRefs` **能报出来**（否则这条断言是假绿）；验证完删除。
> 3. `cd backend-go && go test ./internal/report/...` 验证失败。
> 4. 实现使绿（GREEN）。
> 5. 卫语句扁平化。
> 6. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Reverse-verify `ExternalRefs` catches an injected external ref**
- [ ] **Step 4: Implement minimal production code (GREEN)**
- [ ] **Step 5: Run tests and verify all green**
- [ ] **Step 6: Refactor & Flatten (REFACTOR)**
- [ ] **Step 7: Physical Evidence Gate**
- [ ] **Step 8: Git atomic commit** `feat(report): render self-contained single-file html report`

---

### Task 14: CLI 端到端编排 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/cmd/analyze/main.go`
- Create: `backend-go/cmd/analyze/main_test.go`

**Interfaces:**
- Consumes: `store.RejectSyncedPath`、`store.Open/DSN/Migrate`、`store.RebuildBehaviorMetrics`、`ingest.Run`、`nlp.NewClient`、`analyze.RunSentiment`、`report.Collect/Render`
- Produces: CLI 接口
  - `analyze --export <path> [--talker <id>] [--db <path>] [--out <report.html>] [--nlp-url <url>] [--skip-sentiment] [--list-talkers]`
  - **`--talker` 可选**：缺省时若导出/库中只有一个 talker 就自动采用；多于一个则退出码 2 并提示运行 `--list-talkers`
  - `--list-talkers`：从导出打印可选 talker 值后**退出 0**（这是用户显式请求的输出，不是日志）
  - `--db` 缺省：`%LOCALAPPDATA%\lover-instance\data.db`
  - 退出码：0 成功 / 1 可预期失败（附 hint）/ 2 用法错误
  - 单行 JSON 摘要输出到 stdout（`{parsed, inserted, skipped, updated, scored, coverage, out_path, elapsed_ms}`）；**日志只含 id/count/耗时**
  - 主流程：`RejectSyncedPath(db)` → `Open` → `Migrate` → `ingest.Run` → **无条件 `RebuildBehaviorMetrics`** → （未 skip 时）`RunSentiment` + `RebuildSentimentMetrics` → `Collect` → `Render`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Secret-Log]`：stdout 摘要与 stderr 日志都不得出现 `content`/`talker`/`nickname`/`wxid` 字面值（talker 只以计数或哈希出现）。**例外**：`--list-talkers` 的输出是 CLI 的显式功能产物，不受此限。
- [ ] `[Instinct: Fail-Fast]`：`--nlp-url` 不可达且未指定 `--skip-sentiment` → 启动即失败并给可执行 hint，不要跑到一半才炸。
- [ ] `[Instinct: Behavior-Always-Rebuilt]`：行为指标重建必须在 ingest 之后**无条件**执行，**不能**藏在 `RunSentiment` 内部——否则 `--skip-sentiment` 路径下热力图与统计卡片全为 0。
- [ ] `[Instinct: Privacy-Path-Enforced]`：`RejectSyncedPath` 必须在 `store.Open` **之前**被调用且失败即退出。提供却不调用等于死代码，同步盘红线形同虚设。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 14: CLI 端到端编排。
> Mode: AFK | Role: TDD Builder
> Goal: 一条命令跑通「导出 → 落库 → 出报告」，情感服务可选。
> Target Files: Create `backend-go/cmd/analyze/main.go`、Test `backend-go/cmd/analyze/main_test.go`（把编排抽成可测的 `run(args, stdout, stderr) int`）。
> TDD Steps:
> 1. 失败测试（RED）：缺 `--export` → 退出码 2；`--skip-sentiment` 时跳过 NLP、**`daily_stats` 行为行仍非空**、报告仍能渲染（覆盖率为 0/Total）；不可达 `--nlp-url` 且未 skip → 退出码 1 且 stderr 含 hint；`--db` 指向含 `OneDrive` 的路径 → 退出码非 0 且 stderr 含 hint；**不传 `--talker` 且导出只有一个 talker → 自动采用并成功**；`--list-talkers` → 退出 0 且 stdout 列出 talker；成功路径 stdout 是**单行合法 JSON** 且含 `out_path`；断言 stdout/stderr **不含** fixture 里的任意消息正文片段。
> 2. `cd backend-go && go test ./cmd/analyze/...` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化；用 `flag` 标准库，不引第三方 CLI 框架。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(cmd): add end-to-end analyze cli`

---

### Task 15: M1 端到端验收（真实数据，HITL） [Tier: 3-Critical] [Mode: HITL] [Role: Verifier]

**Files:**
- Create: `workmem/evidence/M1-ACCEPTANCE.md`（gitignored，验收记录）
- 产物：`workmem/out/report.report.html`（gitignored）

**Interfaces:**
- Consumes: Task 1–14 的全部产出
- Produces: 验收结论 + 报告产物路径

**验收标准（MUST ALL，逐条对齐 spec §指标体系 M1）:**
- [x] `go test ./...` 全绿；`pytest fixtures/ -q` 与 `nlp-service` 测试全绿。（11 + 38 passed）
- [x] 用 `workmem/evidence/full/wechat_messages.json`（32624 条真实消息）跑通 CLI（**不手动传 `--talker`**，验证自动推断路径）。
- [~] 产出 `report.html`：**零外部请求** ✅（判据是 `report.ExternalRefs(html)` 返回空，**不是** `grep -cE 'https?://'`——后者在内联 echarts 上必然 ≥6，实测 6 处命中）；**双击可在浏览器打开 + 断网渲染** ⚠️ **本机未安装任何浏览器，未能验证**，需用户手动确认。
- [x] 报告含且**非空**：核心统计卡片、24×7 热力图、双方词云、表情频率、会话类指标（发起方占比 / 每日首末条 / 话轮平衡）、情感基线（7 类占比 + 情绪雷达 + 模型眼中的正负消息比）、被排除类型分布、**固定免责面板**。
- [x] 隐私复查：`git status` 中无 `*.db` / `workmem/` / `*.report.html`；`git check-ignore` 对 `nlp-service/data/` 下的文件返回**非 0**（证明词典确实入库）；日志抽查无 content/talker 字面值。
- [x] 记录报告文件体积与各阶段耗时到 `M1-ACCEPTANCE.md`。
- [x] 逐条比对 spec §指标体系 M1，**未达成项如实记录、不假绿**。

> **验收结果（2026-10-20）**：见 [workmem/evidence/M1-ACCEPTANCE.md](../../workmem/evidence/M1-ACCEPTANCE.md)（本地文件，`workmem/` 已 ignore）。
> 真实数据跑通并**暴露 4 个种子数据构造不出的缺陷**，全部先补失败测试再修：情绪中性桶的两种口径打架、话轮平衡进度条渲染出 126.8%/−26.8%、续跑把「已完成」报成「部分覆盖」、词云里 `，` 排第三。
> 全量运行与续跑运行产出的报告**逐字节相同**；报告 1,088,612 字节；单文件内联后无需网络。
> **未验证**：浏览器渲染（无浏览器）与断网渲染，留给用户；词云插件在 Node SSR 下无法验证注册。**未达成**：spec 的「日均月趋势」（计划 D4 亦未列，属既有缺口）。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Execute Task 15: M1 端到端验收。
> Mode: HITL | Role: Verifier
> **这是人机配合任务：涉及真实聊天数据与对外交付物，执行前必须由用户确认。**
> Goal: 用真实导出跑通端到端并产出可分享报告。
> Steps:
> 1. 用户确认后，安装依赖并启动 nlp-service（`uvicorn app.main:app --port 8000`），确认 `GET /health` 为 ready。
> 2. 运行 CLI（不传 `--talker`），指定真实导出与输出路径到 `workmem/out/`。
> 3. 抓物理凭据：CLI 的 stdout JSON 摘要、报告文件字节数、`git status --short`、`git check-ignore nlp-service/data/*` 的退出码、`report.ExternalRefs` 的返回值（跑对应 Go 测试）。
> 4. 用户手动双击打开报告确认渲染 + 断网确认。
> Return: 摘要 + 全部原始凭据 + 未达成项（如有）的如实记录，**不假绿**。"

**Step Breakdown:**
- [ ] **Step 1: 用户确认执行窗口**
- [ ] **Step 2: 启动 nlp-service 并确认 ready**
- [ ] **Step 3: 跑 CLI 产出报告（不传 --talker）**
- [ ] **Step 4: 用户双击验证渲染 + 断网验证**
- [ ] **Step 5: Physical Evidence Gate**（全部原始输出）
- [ ] **Step 6: 隐私复查 + 逐条比对 spec §指标体系 M1 + 结果写入 `M1-ACCEPTANCE.md`**
- [ ] **Step 7: Git atomic commit**（仅提交代码与文档；产物不入库）

---

## [*]️ Fog of War (Not Yet Specified)

- **[词云体积回落]**：Task 12 内联 `echarts-wordcloud` 后，若单文件超过可接受体积（阈值待实测），需改为 Go 侧渲染 SVG 词云。**阻塞于 Task 13 的实测产物体积**，届时才决定。
- **[情感词表的取得]**：`emotion_lexicon.txt` 的具体词条规模尚未定。M1 允许每类 ≥20 词的精选小表跑通闭环，**完整词库的覆盖率提升留到 M1 之后**；自建词表不涉及第三方许可证。`hownet_degree.txt` / `hownet_negation.txt` 同样自建小表。
- **[contacts 缺失的影响]**：M0 实测 `contact.db` 未能解密，`contacts` 表在本机将为空，报告只能以 **talker 哈希**呈现对象。**阻塞于联系人库能否解开**；不阻塞交付。
- **[多会话支持]**：M1 的 ingest 已能导入多 talker，但 CLI 一次只分析一个（多于一个时要求显式 `--talker`）。批量/多会话的报告切换机制未定，属 M3 范围。
- **[真实数据的分析结论合理性]**：报告出图后，各项指标是否符合"描述性统计结论很明显"的预期，需要用户实际看过真实数据才能判断；若某指标在真实数据上无区分度，需回头调整口径。

---

## [x] Out of Scope

- **解密/密钥提取**：任何形式，永不进入本仓库（ADR-0001）。
- **正/负极性分桶词云**：spec §指标体系 M1 原文「双方词云……正/负各一朵」两句自相矛盾（"双方"与"正/负"是两种切分）。M1 按**自我/对方**实现，极性分桶词云留待 spec 口径澄清后的后续版本。
- **Vue3 + ECharts 交互 Dashboard**：属 M3，M1 只出自包含静态报告。
- **SKEP / RoBERTa 模型情感**：属 M2；M1 只用词典基线。
- **`analysis_jobs` 状态机编排与 REST API / 前端轮询**：属 M2/M3；M1 迁移建表但只用块级批处理。
- **本地 Ollama 叙事、话题词云随时间变化（ThemeRiver）**：属 M4。
- **Docker / docker-compose 编排**：M1 用本地进程（`go run` + `uvicorn`）；compose 属 M3。
- **FTS5 全文检索、LSM、情绪惯性、情感滞后互相关、变点检测**：已在设计阶段砍掉，不复活。
- **`echarts.min.js` 之外的任何前端构建链**（Vite/npm）：M1 报告是纯 Go 模板渲染，不引入 node 依赖。