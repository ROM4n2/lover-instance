# M3 Vue3 Dashboard 实施计划

> **Goal**：把「一条命令生成的静态报告」变成「可反复探索的本地 Dashboard」，同时**不削弱**已验收的两条底线：**只出聚合**、**半成品不许画成完整曲线**。
> **Spec Reference**：[spec §前端约定 / §里程碑 M3](../specs/2026-09-08-wechat-relationship-analysis-design.md)
> **前置**：M1 报告闭环、M2 模型情感 + `analysis_jobs` 状态机均已验收（`workmem/evidence/M1-ACCEPTANCE.md` / `M2-ACCEPTANCE.md`）。
> **Branch**：沿用 trunk 例外——纯本地仓库、无远端，原子提交直接落 `main`。

## 全局约束（不可协商）

- **只出聚合**：接口只返回计数、占比、分桶、Top N 词；**永不返回消息正文、`talker` 原值、`wxid`、`nickname`**。会话在 API 里只用 `report.TalkerHash`（16 hex）标识。
- **隐私日志**：Go 侧**不打印任何 query string 与 path 参数**（`talker` 会出现在 URL 里，见 D4）；错误体只含 `code`/`message`/`hint`。
- **半成品门控**：`analysis_jobs` 未 `done` 时，情感类视图必须显示「分析中 done/total」或只画已分析区间，**不得**用半满数据画一条看起来完整的曲线。
- **单飞**：`POST /api/jobs` 撞上活跃任务返回 **409** 与既有 job id，不启动第二遍（`uq_jobs_active` 说了算）。
- **零外部资源**：Dashboard 与报告一样自包含，`go:embed` 托管 dist；**不引 CDN、不引外部字体**。图表库从 `internal/report/assets/echarts.min.js` 复用同一份。

## Decisions

- **D1（用 Gin，按用户决定）**：`github.com/gin-gonic/gin`。`go.mod` 目前只有 `modernc.org/sqlite` 一个直接依赖，这是本仓库第一次引入 web 框架（`GOPROXY=https://goproxy.cn` 已验证可拉取）。
- **D2（聚合来自 `report.Collect`）**：`GET /api/overview` 直接复用 `internal/report.Collect` 的 `View`，**不新建第二条指标派生路径**。M1 的教训是同一天被两条路径各算一次就必然分歧。
- **D3（dist 不入库，按用户决定）**：`frontend/dist` 与 `backend-go/internal/web/dist` 都进 `.gitignore`，仓库里**不含任何构建产物**。两种运行形态：
  - **默认构建**（`go build ./cmd/server`）：运行期从磁盘读 dist，路径依次尝试 `$LOVER_FRONTEND_DIST` → 仓库内 `frontend/dist` → 二进制同级 `dist/`。找不到时 **不 panic**，返回一个说明「请先 `npm run build` 或设置 `LOVER_FRONTEND_DIST`」的纯文本页。
  - **单文件构建**（`go build -tags embedded`）：用 `//go:embed all:dist` 把 `backend-go/internal/web/dist` 嵌进去。该目录由 Vite 的 `outDir` 直接产出（`vite build --outDir ../backend-go/internal/web/dist`），同样不入库。
  - 后果：默认二进制**不是**单文件自包含的。spec 的「单二进制双击即用」只在 `-tags embedded` 下成立，README 要写清。前端源码与 dist 不会漂移——因为 dist 根本不进版本库。
- **D4（URL 状态带 talker）**：`/?talker=<wxid>&view=sentiment` 按 spec 落地。这是**本地浏览器 URL**，不入日志、不出网；但要在 README 与 UI 里点明「书签会包含对方的 wxid」。
- **D5（分析入口只有一种）**：Dashboard 的「开始分析」= `POST /api/jobs`，内部走与 CLI 相同的 `jobs.Start` + `analyze.RunSentimentProgress` + `jobs.Finish`；**不写第二套编排**。CLI 与 Dashboard 同时跑由单飞挡住。

## 任务

### Task 1: API 骨架与错误体 [Tier: 2]
**Files**：Create `backend-go/internal/api/{server.go,errors.go,health.go}`、`server_test.go`；Create `backend-go/cmd/server/main.go`
- `GET /api/health` → `{status, db:"ok"|"error", nlp:{status,model_version,available_versions}, job:{id,status,done,total}|null}`；NLP 不可达时 `status:"degraded"` 且**不 500**（徽章要能显示"情感服务未启动"）。
- 统一错误体 `{code, message, hint}`；`writeError` 是唯一出口。
- 只监听 `127.0.0.1`（本机工具，不对外）。
**TDD**：NLP 挂掉时 `/api/health` 返回 200 + degraded；错误体三个字段齐全；监听地址断言。

### Task 2: 聚合接口 [Tier: 2]
**Files**：Create `backend-go/internal/api/overview.go`、`overview_test.go`
- `GET /api/overview?talker=<hash|wxid>` → `report.View` 的 JSON（字段与报告同源），外加 `meta.partial`。
- talker 缺省时：若库里只有一个会话则用它，多个则 **400 + 候选指纹列表**（不猜）。
- **不得**在响应里出现 raw talker（响应里只有 `talker_hash`）。
**TDD**：两个会话且未指定 → 400 且候选是 16 hex；指定后响应的 hash 与 `report.TalkerHash` 一致；响应体里 grep 不到原始 wxid。

### Task 3: 任务接口（开始/进度/取消）[Tier: 3-Critical]
**Files**：Create `backend-go/internal/api/jobs.go`、`jobs_test.go`；Modify `cmd/server/main.go`
- `POST /api/jobs` body `{model_version?}` → 202 `{id}`；活跃任务已存在 → **409** + 既有 id。
- `GET /api/jobs/{id}` → `{id,status,done,total,error?}`（绝对值）。
- `DELETE /api/jobs/{id}` → 置 `canceled`；worker 每块检查 `ctx.Err()`。
- worker：goroutine 内 `jobs.Start` → `analyze.RunSentimentProgress` → 心跳 → `jobs.Finish`；进程退出时 worker 被 cancel，行留给下次 `Recover`。
- **与 CLI 共用**：`jobs.Recover` + `StaleAfter` 语义不变。
**TDD**：并发两次 POST 只得一个 id（第二次 409）；未 `done` 时取消 → 行变 `canceled` 且不再发块；worker 失败 → 行 `failed` 且 `error` 非空且**不含正文**。

### Task 4: 导出报告 [Tier: 2]
**Files**：Create `backend-go/internal/api/export.go`、`export_test.go`
- `POST /api/export` body `{talker, model_version?}` → `{path, bytes}`，文件落在 `<LOCALAPPDATA>/lover-instance/exports/`，**不经 HTTP 传 1MB 正文**。
- 复用 `report.Collect` + `report.Render` + `report.ExternalRefs` 守卫（非空则拒绝写并报 500）。
**TDD**：`ExternalRefs` 命中 → 500 且**不产生文件**；正常路径文件字节数等于响应里的 `bytes`。

### Task 5: 前端骨架与 URL 状态 [Tier: 2]
**Files**：Create `frontend/{package.json,vite.config.ts,index.html,src/**}`；Create `backend-go/internal/web/{static.go,static_disk.go,static_embedded.go}`（后两者用 build tag 互斥）
- Vue3 + Vite + Element Plus + ECharts；`?talker=&view=` 双向绑定（刷新/书签/前进后退成立）。
- Vite `build.outDir` = `../backend-go/internal/web/dist`（不入库）；dev 期用 Vite proxy 指向 `127.0.0.1:8787`。
- `static_disk.go`（默认）：按 D3 的三个候选路径找 dist；找不到 → 说明性纯文本页（200，不是 panic/500）。
- `static_embedded.go`（`//go:build embedded`）：`//go:embed all:dist`。
- 视图：概览 / 情感 / 词云 / 情绪 / 任务。健康徽章常驻右上角。
**TDD**（Go 侧）：dist 缺失时 `GET /` 返回说明页且**不 panic**；有 dist 时 index.html 与 `Content-Type` 正确；SPA 深链（`/anything`）回退 index.html；`-tags embedded` 构建在 dist 存在时能编译（无 dist 时不参与默认构建）。

### Task 6: 轮询、半成品门控与刷新恢复 [Tier: 3-Critical]
- 任务进行中 2s 轮询；`done/total` 显示绝对值；**情感视图在未 done 时显示"分析中"占位，不画完整曲线**。
- 刷新后据 URL 的 talker 恢复：有活跃 job → 继续轮询；无 → 拉 overview。
- 每个 ECharts 实例 `onUnmounted(dispose)`；resize 监听随组件摘除。
**TDD**：半成品时 overview 的 `meta.partial=true` 且前端不渲染曲线（用 CDP 断言 canvas 未创建）。

### Task 7: M3 验收 [Tier: 3-Critical] [Mode: HITL]
- `go test ./...` / `pytest` 全绿；`git status` 里**没有任何 dist 产物**（`git check-ignore` 对 `frontend/dist`、`backend-go/internal/web/dist` 均命中）。
- 无 dist 时 `go build ./...` 成功、服务器启动返回说明页；`npm run build` 后可正常出界面。
- CDP 截图：概览、情感、任务进行中的门控、取消后的状态。
- 断网（`--host-resolver-rules="MAP * ~NOTFOUND"`）下全部图表仍出图。
- 隐私扫描：抓包/日志里 grep 不到 wxid、正文、昵称。
- 记录在 `workmem/evidence/M3-ACCEPTANCE.md`。

## Out of Scope

- **消息浏览/检索**（FTS 已砍）。
- **多会话对比**、多人/群聊视图。
- **Ollama 叙事**（M4）、**话题词云随时间**（M4）。
- **鉴权**：只监听 127.0.0.1，不做登录（本机单用户工具）。
- 前端单测框架（Vitest）：本计划只做 Go 侧测试 + CDP 端到端核验，避免为 UI 细节引一套测试栈。

## 依赖与风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 默认二进制不自包含（dist 不入库） | 换机器后界面缺失 | 启动时返回说明页而非白屏；README 写清 `-tags embedded` 的单文件形态 |
| 浏览器缓存旧 dist | 改了看不到 | 静态资源文件名带内容哈希（Vite 默认），index.html 不缓存 |
| 拉不到 Gin / 前端依赖 | 构建失败 | `GOPROXY=https://goproxy.cn` 已验证可拉 gin；若 npm 源不可用，前端任务阻塞但 Go 侧任务可独立验收 |
| 半成品被画成完整曲线 | 误导（M1/M2 反复强调的失效形态） | Task 6 的 `meta.partial` + CDP 断言 |
| 前后端同时跑分析 | 词频/打分重叠 | `uq_jobs_active` 单飞 + 409 |
| 长任务期间占住唯一 DB 连接 | 接口全阻塞 | 复用 M2 的「块间不持连接」，worker 与 API 共用 store 但每块短事务 |
---

## 执行期偏离（2026-10-20，Task 1–7 实施后回填，以代码为准）

| 计划原文 | 实际实现 | 原因 |
| --- | --- | --- |
| Task 2 Files 只列 `internal/api/overview.go` | 另加 `internal/store/versions.go`（M2 已存在）、`internal/api/{jobs,export}.go` 拆三个提交 | 三个 Task 共用 `api.New` 路由表，按 Task 拆成三个可独立回滚的原子提交 |
| Task 3「DELETE 调 `jobs.Cancel` 并取消 worker ctx」 | 有 worker 时**只**取消 ctx，由 worker 的 `Finish` 写 `canceled`；无 worker（CLI 起的/上个进程死了）才直接 `jobs.Cancel` | 两者都做时 job 行在 `Finish` 之前就已是 `canceled`，**收尾写失败将不可观测**——这正是 M2 那张红牌的形状 |
| Task 3「进程退出 cancel worker，行留给下次 `Recover`」 | 退出时 cancel 并 `wg.Wait()`，worker 因此记 `canceled` | clean shutdown 下 `canceled` 比留 `running` 更诚实；kill -9（defer 不跑）仍走 `Recover` |
| Task 5「磁盘查找 `frontend/dist`」 | 同时找 `frontend/dist`、`backend-go/internal/web/dist`、`internal/web/dist`、`../backend-go/internal/web/dist`、包相对 `dist`、二进制同级 `dist` | **验收时真踩到**：从 `backend-go/` 下构建/启动时前两组候选都会解析到错误路径，界面被判为"未安装" |
| Task 6「情感视图不画曲线」 | `analysis.running` 时隐藏**全部**情感图（雷达/柱/趋势/对比） | 它们都由半满打分数据派生，只藏曲线等于让别的图撒谎 |
| 未规定 | `view` 直接是 `report.View` 的 Go 字段名（`Meta`/`Summary`/`Emotion`…） | 加 json tag 会改内联报告的 JS 读键，属破坏 M1 验收产物；前端按大写键取值 |
| 未规定 | 健康探测超时 **5s**（初版 2s） | 实测打分期间 `nlp-service` 的 `/health` 要 **2.9s**（排在批次后），2s 探测会在用户最需要看徽章时报「服务未启动」 |
| 未规定 | `cmd/server` 默认 `gin.ReleaseMode`（`LOVER_GIN_DEBUG=1` 可开） | debug 模式每次启动刷一屏路由表与"切到 release"提醒，是双击工具的噪音 |

**验收**：`workmem/evidence/M3-ACCEPTANCE.md`。13 个 Go 包全绿；dist 不入库（`git check-ignore` 双命中）；CDP 真实时间核验（概览 6 canvas、情感 3 canvas、门控 0 canvas）；断网 6 canvas 全出；日志 grep `wxid|talker=` 为 0；导出 1,103,199 字节；并发 `POST /api/jobs` 得 202 + 409。

**未闭环**：Task 0 的评测标签仍是 `agent-draft`；前端无单元测试（计划 Out of Scope，靠 CDP 端到端核验）。
