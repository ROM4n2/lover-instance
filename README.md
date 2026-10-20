# lover-instance

> 把你和 TA 的微信聊天记录，变成一份可回顾、可分享的关系分析报告。

本地优先（local-first）的双人微信聊天记录分析平台：自动获取本地微信聊天记录 → 结构化存储 → 描述性统计 / 词频 / 情感分析 → **可交互 Dashboard（自己探索）** 与 **自包含 HTML 报告（发给 TA / 存档）**。

> ⚠️ 使用前请阅读 [DISCLAIMER.md](DISCLAIMER.md)：本仓库**不含任何解密代码**，数据获取依赖你自装的第三方工具；分析结论为娱乐性统计，非关系诊断；分享报告前请先征得对方同意。

## 架构

```
浏览器(Vue3+ECharts) ──REST──▶ Go(Gin) 网关/编排 ──┬─ 适配器 ─▶ 宿主机 sidecar(用户自装, HTTP API / 导出 JSON)
                                  │                 ├─ SQLite(WAL): messages/sentiment/daily_stats/analysis_jobs...
                                  ├─ HTTP 批处理 ─▶ Python(FastAPI) NLP: jieba+DUTIR/HowNet · SKEP/RoBERTa
                                  └─ 叙事(OpenAI 兼容) ─▶ 本地 Ollama（云 API 默认 deny-by-default）
交付物：① 交互 Dashboard  ② 单文件 report.html（内联图表，可微信发送）
```

- **Go**：数据接入适配器、SQLite 存储、长任务编排（单飞/取消/检查点续跑）、REST API、HTML 报告渲染（go:embed）。
- **Python**：无状态 NLP 微服务（分词、词典情感基线、SKEP/RoBERTa 模型），不持库。
- **合规边界**：解密 sidecar 跑在宿主机；本仓库只做分析与可视化。

## 当前状态

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 取数 Spike + schema 证据（微信 4.1.9.30 被动取密钥实测、唯一键核实） | ✅ 完成（`chatlog-keeper` 导出 JSON 契约已冻结，32,624 条真实消息） |
| M1 | 端到端最小闭环 + 首份自包含 report.html（词典法，零模型） | ✅ 完成（含断网渲染与逐字节复现验证） |
| M2 | analysis_jobs 状态机 + 块级批处理 + 模型情感（ONNX，与词典版共存可比） | ✅ 完成（真实数据三版共存、kill 后续跑、两版对照区） |
| M3 | Vue3 Dashboard（Gin + go:embed、健康徽章、进度轮询、半成品门控） | ✅ 完成（CDP 真机核验、断网出图、日志不含会话键） |
| M4 | 本地 Ollama 叙事 + 话题词云时间线 | ✅ 完成（叙事 4 秒、月度词云需显式重扫；模型需自行指定） |

设计与决策见 [docs/specs/](docs/specs/) 与 [docs/adr/](docs/adr/)；M1 的实施计划与执行期偏离见 [docs/plans/M1-mvp-report-implementation-plan.md](docs/plans/M1-mvp-report-implementation-plan.md)。

## 快速开始（M1）

```bash
# 1. 在宿主机用你自己选择的工具导出聊天记录 JSON（本仓库不含导出/解密代码）。
#    导出契约见 docs/adr/ADR-0001：字段 server_id / source_offset / ts / conversation_id /
#    sender_wxid / msg_type / is_self / content。

# 2. 启动词典基线 NLP 服务（M1 零模型，只加载自建词表）
cd nlp-service && python -m uvicorn app.main:app --port 8000
curl -s http://127.0.0.1:8000/health    # 期望 {"status":"ready",...,"model_version":"lexicon-hownet-v1"}

# 3. 一条命令跑完导入 → 行为指标 → 情感打分 → 渲染报告
cd backend-go && go build -o analyze ./cmd/analyze
./analyze --export /path/to/wechat_messages.json --out report.html
```

CLI 退出码：`0` 完成 / `1` 执行失败 / `2` 命令行有误。成功时 stdout 是一行 JSON 摘要（条数、覆盖度、各阶段耗时、报告体积），**不含任何消息正文与会话标识**——只有会话指纹。

常用参数：`--talker`（导出含多个会话时必填，单会话自动推断）、`--list-talkers`（列出已入库的会话键）、`--db`（默认 `%LOCALAPPDATA%\lover-instance\data.db`）、`--skip-sentiment`（只出行为报告，覆盖度会如实标注为不完整）、`--nlp-url`。

## M2：换一个情感模型，与词典版并列比较

同一个会话可以同时存多个 `model_version` 的分数，报告会列出两版的正负比例与**不一致率**（分母只算两版都打过分、且都给出数值的消息）。

```bash
# 看本机 NLP 服务提供哪些版本
curl -s http://127.0.0.1:8000/health    # available_versions

# 用模型版重打一次（首轮会下载并校验权重，之后离线可跑）
./analyze --export wechat_messages.json --sentiment-version erlangshen-110m-v1 --out report.html
```

- 未知版本会在发出第一个打分块之前就被拒绝（不会白跑五分钟才发现拼错）。
- **续跑不重打分**：被 `kill -9` 后，重启需等该 job 的心跳过期（默认 90 秒）才会被接管；确知对方已死可加 `--take-over`（它会在 stderr 明确警告：若那个进程还活着，两边都写、词频会翻倍）。
- 实测耗时（22,093 条，本机 CPU）：Erlangshen 约 276 秒，distilbert 约 158 秒；纯推理吞吐比这高一个数量级，差额在分词、HTTP 与逐块事务上。

## M3：本地 Dashboard

```bash
cd frontend && npm install && npm run build      # 产物落在 backend-go/internal/web/dist（不入库）
cd ../backend-go && go build -o server ./cmd/server
./server                                          # 监听 127.0.0.1:8787
```

- 打开 `http://127.0.0.1:8787/`，`?talker=<wxid>&view=overview|sentiment|jobs` 会进 URL（刷新/书签成立）。**书签里含对方的 wxid，分享浏览器配置前先想起来这件事。**
- NLP 服务没起时右上角徽章显示降级，页面其余部分照常可用。
- **半成品门控**：有分析任务在跑时，情感视图只显示「分析中 done/total」，不画曲线；跑完自动出图。
- 想让单二进制自包含：`go build -tags embedded`（需先 `npm run build`）。默认构建从磁盘找 dist，找不到会给一页说明而不是白屏。
- 接口只出聚合：没有任何返回消息正文的端点；日志只记 method / 路由模板 / 状态码 / 耗时，**不记 query**。

## M4：本机叙事 + 按月高频词

```bash
# 按月词云要先重扫（把 period='YYYY-MM' 的行写出来）；显式给，绝不默认删数据
./analyze --export wechat_messages.json --rescore-words --out report.html

# 叙事：模型名必填，没有默认值；只允许回环地址（云 API 一律拒绝）
./analyze --export wechat_messages.json --ollama-model qwen2.5:7b-instruct --out report.html
./analyze --export wechat_messages.json --no-narrative --out report.html   # 关掉
```

- **叙事只看聚合，不看对话**：喂给模型的只有条数、占比、趋势点，**不含任何词**（真实词云会带人名，报告是分享物，名字不能从这条路出去）。这条是类型级约束 + 测试钉住的。
- 模型要**关思考模式**：provider 走 Ollama 原生 `/api/chat` 并显式 `think:false`。思考型小模型会把预算全花在推理上——实测 120 秒超时、空内容；关掉后同一 prompt 3.9 秒返回。
- 同一份输入的叙事按摘要缓存，重跑不会重复请求、也不会每次都不一样；模板变更必须升 `promptVersion`，否则旧缓存会被当成新结论。
- 本机 `qwen3.5:4b` 可用但**会编形状**（「先降后升再回落」这类趋势点里没有的话），数字是对的。分享前请通读那一段。
- 月度词云只画对方侧；没有月度数据时报告会写「需要重扫」，不画空图。

### 三条容易踩的坑

1. **改了 `backend-go/internal/report/templates/*.tmpl` 必须重新 `go build`。** 模板经 `go:embed` 编进二进制，不重编译就是在跑旧模板。
2. **改了词表或分词后，已入库的词频不会自动回填。** 待办集按 anti-join 判定，已打分的块不会再进入待办集；需删掉 `word_freq` 的行或删库重跑。
3. **数据库不要放在 OneDrive / 坚果云 / 百度网盘目录下。** CLI 会在建库前拒绝这类路径（Windows 的「已知文件夹移动」会把 `Documents` 悄悄重定向进 OneDrive）。

> 数据库文件、导出 JSON、报告、模型权重均已在 `.gitignore`，不会入库。报告是自包含单页，打开时不产生任何网络请求（由 `report.ExternalRefs` 断言）。
