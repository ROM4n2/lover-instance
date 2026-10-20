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

- **D1（不用 Gin）**：用标准库 `net/http` + Go 1.26 的 `ServeMux` 方法与路径模式（`GET /api/overview`、`DELETE /api/jobs/{id}`）。spec 写的是 Gin，但当前 `go.mod` 只有 `modernc.org/sqlite` 一个直接依赖，为实现路由引入框架没有收益；差异记录在「执行期偏离」。
- **D2（聚合来自 `report.Collect`）**：`GET /api/overview` 直接复用 `internal/report.Collect` 的 `View`，**不新建第二条指标派生路径**。M1 的教训是同一天被两条路径各算一次就必然分歧。
- **D3（提交 `frontend/dist`）**：`go:embed` 需要构建期存在 dist，本仓库无 CI，故把 `dist` 入库（与 M1 已入库的 `echarts.min.js` 同一先例）。`npm run build` 后必须同步提交，README 写明。
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
**Files**：Create `frontend/{package.json,vite.config.ts,index.html,src/**}`、`backend-go/internal/api/static.go`（`go:embed ../../frontend/dist`）
- Vue3 + Vite + Element Plus + ECharts；`?talker=&view=` 双向绑定（刷新/书签/前进后退成立）。
- 视图：概览 / 情感 / 词云 / 情绪 / 任务。健康徽章常驻右上角。
**TDD**（Go 侧）：dist 缺失时 `go build` 的明确错误处理；`GET /` 返回 index.html 且 `Content-Type` 正确；SPA 深链回退到 index.html。

### Task 6: 轮询、半成品门控与刷新恢复 [Tier: 3-Critical]
- 任务进行中 2s 轮询；`done/total` 显示绝对值；**情感视图在未 done 时显示"分析中"占位，不画完整曲线**。
- 刷新后据 URL 的 talker 恢复：有活跃 job → 继续轮询；无 → 拉 overview。
- 每个 ECharts 实例 `onUnmounted(dispose)`；resize 监听随组件摘除。
**TDD**：半成品时 overview 的 `meta.partial=true` 且前端不渲染曲线（用 CDP 断言 canvas 未创建）。

### Task 7: M3 验收 [Tier: 3-Critical] [Mode: HITL]
- `go test ./...` / `pytest` 全绿；`npm run build` 可重复（dist 无未提交差异）。
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
| `frontend/dist` 与源码漂移 | 单二进制跑的是旧界面 | README 写明 `npm run build` 后提交；Task 7 断言 dist 无未提交差异 |
| 浏览器缓存旧 dist | 改了看不到 | 静态资源文件名带内容哈希（Vite 默认），index.html 不缓存 |
| 半成品被画成完整曲线 | 误导（M1/M2 反复强调的失效形态） | Task 6 的 `meta.partial` + CDP 断言 |
| 前后端同时跑分析 | 词频/打分重叠 | `uq_jobs_active` 单飞 + 409 |
| 长任务期间占住唯一 DB 连接 | 接口全阻塞 | 复用 M2 的「块间不持连接」，worker 与 API 共用 store 但每块短事务 |
