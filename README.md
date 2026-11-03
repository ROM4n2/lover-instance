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
| M5 | 一键化：四条命令 + 模型清单 + 五类失败退出码 | ✅ 完成（零模型开箱已物理演练、取消无孤儿、`lover dash` 真机验证） |

设计与决策见 [docs/specs/](docs/specs/) 与 [docs/adr/](docs/adr/)；每个里程碑的实施计划与「执行期偏离」表见 [docs/plans/](docs/plans/)（M1–M5）。验收记录（不入库）在 `workmem/evidence/M*-ACCEPTANCE.md`。

## 数据与隐私边界（发布前请读这一节）

这个项目处理的是**两个人的私人聊天记录**，所以边界写在最前面：

- **仓库里没有任何聊天内容、密钥、数据库或模型权重。** 真实数据只落在你的本机
  `%LOCALAPPDATA%\lover-instance\`；测试全部使用 `fixtures/` 里的**合成语料**。
- **仓库不内置、不分发任何解密或密钥提取代码**（见 [ADR-0001](docs/adr/ADR-0001-data-access-contract.md)）。
  数据由你自装的第三方工具导出为 JSON，本项目只消费该文件。
- **仓库不含**：本机知识库联动的多 Agent 入口文件（`AGENTS.md` / `CLAUDE.md` / `.cursorrules` /
  `.agent-rules.md`）、内部工作记忆账簿（`WORKMEMORY/`）、以及取钥过程细节（`docs/private/`）。
- **`report.html` 是分享物**：它含对方的聊天内容（词云、情绪、曲线、必要时的叙述段）。
  **分享前请先征得对方同意**；情感结论是描述性统计，不是关系判断。
- 完整声明见 [DISCLAIMER.md](DISCLAIMER.md) 与 [LICENSE](LICENSE)。

**发布前自检**（把上面的边界变成可重复执行的断言，而不是靠人记得）：

```bash
python scripts/release-audit.py
```

它会检查：索引与**历史**里有没有数据文件、本机目录是否被跟踪、有没有本机绝对路径、
有没有疑似密钥、公开树里有没有取钥方法的措辞，以及 LICENSE / DISCLAIMER / README 隐私小节是否齐备。
**注意：它检查的是当前索引与历史里的文件；若要彻底抹掉历史中的敏感段落，需要另做
历史重写（见发布说明）。**

## 快速开始（四条命令）

> **前置**：Go 1.26（`backend-go/go.mod`）与 Python 3.11+（`nlp-service`；可用 `LOVER_PYTHON` 指定解释器）。
> 聊天记录 JSON 由你自装的第三方工具导出——**本仓库不含导出/解密代码**；导出契约见 `docs/adr/ADR-0001`
> （字段 `server_id / source_offset / ts / conversation_id / sender_wxid / msg_type / is_self / content`）。
> 首次运行会先检查 Python 依赖，缺什么就报什么；**不加 `--yes` 绝不自动安装**。

```bash
# 0. 构建一次（Windows 用 -o lover.exe）
cd backend-go && go build -o lover ./cmd/lover
```

在仓库的任意子目录下运行即可：`lover` 会向上找 `backend-go/go.mod` + `nlp-service/app` 定位仓库根，也可用 `LOVER_REPO_ROOT` 直接指定。

```bash
# 1. 首次运行：检查依赖 → 起 NLP 服务 → 分析最新导出 → 打开报告
./lover
./lover --yes                                    # 依赖缺失时才允许自动 pip install
./lover --export /path/to/wechat_messages.json    # 指定导出文件（默认是导出目录里最新的 *.json）

# 2. 周更：同一条流水线，只报新增
./lover sync --export /path/to/wechat_messages.json
# → 新增 128 条 / 打分 128 条 / 覆盖率 32400/32624

# 3. Dashboard：bundle 缺失或过期就先 npm run build，然后 serve
./lover dash                                     # http://127.0.0.1:8787 ，Ctrl-C 停止

# 4. 体检：报告缺什么，不改任何东西
./lover doctor

# 5. 零门槛体验（内置合成语料，秒级脱机出图）
./lover demo                                     # 自动用内置 995 条样本生成体验报告
./lover demo --open                              # 生成并自动在浏览器打开 demo_report.html
```

模型是**显式升级**，不是首次运行的一部分：

```bash
./lover model list                      # 看装了什么（只哈希本地文件，零网络）
./lover model add erlangshen-110m-v1    # 下载并按 sha256 校验每一个 pin 住的文件
./lover model verify                    # 重新哈希，列出与 pin 不符的文件
./lover model rm erlangshen-110m-v1 --yes
./lover --export wechat_messages.json --sentiment-version erlangshen-110m-v1   # 用模型版重打
```

**零模型开箱**：默认走词典版情感（`lexicon-hownet-v1`），全程 **0 MB 下载**——`lover` 和 `lover sync` 永远不会替你下载权重。想要模型版就显式 `lover model add`，`list` 会如实区分「未安装（从没装过）」「部分就位（上次 add 没跑完）」「文件在但内容与 pin 不符」三种情况，并给出对应的下一步命令。

每条子命令都有 `-h`。常用参数：`--db`（默认 `%LOCALAPPDATA%\lover-instance\data.db`）、`--talker`（导出含多个会话时必填，单会话自动推断）、`--skip-sentiment`（只出行为报告，覆盖度会如实标注为不完整）、`--out`、`--nlp-url`。

### 退出码（脚本和双击都用得到）

| 码 | 含义 | 遇到该做什么 |
|---|---|---|
| 0 | 跑完了 | 报告已写出并打开（`sync` 只打印一行） |
| 1 | 分析本身失败 | 看 stderr 指名的阶段；修好后重跑，续跑不会重打分 |
| 2 | 命令行有误 | 改命令；每条子命令都有 `-h` |
| 3 | 依赖缺失 | Python 包 → `python -m pip install -r nlp-service/requirements.txt`；`dash` 还需要 npm 与 Go 工具链 |
| 4 | 端口被占用 | 消息里给出占用者的 PID，停掉它；或 `--port` / `--nlp-url` 换一个 |
| 5 | 服务起来了但 60 秒内没 ready | 手工起一次看它卡在哪：`cd nlp-service && python -m uvicorn app.main:app --port 8000` |

Ctrl-C（SIGINT）会收掉整个服务进程组、把 job 行按取消关闭，不留孤儿进程；此时退出码为 1 并打印「已中断」，重跑同一条命令即可接着跑。

### 进阶：直接用底层 `analyze`

四条命令与 `analyze` 走的是同一个 `internal/app.Run`，共享全部默认值。`analyze` 的价值在于细粒度开关（`--sentiment-version`、`--rescore-words`、`--take-over` 等）和脚本友好的三码退出；需要自己起服务、自己看每一步时用它：

```bash
# 1. 在宿主机用你自己选择的工具导出聊天记录 JSON（本仓库不含导出/解密代码）。
#    导出契约见 docs/adr/ADR-0001。

# 2. 启动词典基线 NLP 服务（零模型，只加载自建词表）
cd nlp-service && python -m uvicorn app.main:app --port 8000
curl -s http://127.0.0.1:8000/health    # 期望 {"status":"ready",...,"model_version":"lexicon-hownet-v1"}

# 3. 一条命令跑完导入 → 行为指标 → 情感打分 → 渲染报告
cd backend-go && go build -o analyze ./cmd/analyze
./analyze --export /path/to/wechat_messages.json --out report.html
```

CLI 退出码：`0` 完成 / `1` 执行失败 / `2` 命令行有误。成功时 stdout 是一行 JSON 摘要（条数、覆盖度、各阶段耗时、报告体积），**不含任何消息正文与会话标识**——只有会话指纹。

常用参数：`--talker`（导出含多个会话时必填，单会话自动推断）、`--list-talkers`（列出已入库的会话键）、`--db`、`--skip-sentiment`、`--nlp-url`。

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

## M6：新手友好向导、管道抽象与高光里程碑

- **`lover demo` 零门槛沙箱**：内置 995 条合成语料，脱机 1 秒内生成全套图表与高光时间线；
- **Web 拖拽与空态向导**：Dashboard 在空库时自动展示向导，支持拖拽 100MB 聊天 JSON 上传入库与一键载入 Demo；
- **模块化分析管道**：抽象 `Analyzer` 契约与 DAG 拓扑排序注册表，解耦算法与编排；
- **高光里程碑与纪念日时间线**：自动挖掘相识启程日（相识天数与消息总量）、心动连击（最长连续互发消息天数）、单日长谈 Top 3 峰值日、深夜畅聊夜（00:00–05:00 CST 深度长谈）。

## 技术栈说明，与三条容易踩的坑

- **Go 1.26**（`backend-go`）：数据接入适配器、SQLite 存储（WAL）、长任务编排（单飞/取消/检查点续跑）、REST API、HTML 报告渲染（`go:embed`）。入口有三：`cmd/lover`（四条命令）、`cmd/analyze`（底层 CLI）、`cmd/server`（Dashboard 服务）。
- **Python 3.11+**（`nlp-service`，无状态、不持库）：jieba 分词、词典情感基线、SKEP/RoBERTa（ONNX）。由 `lover` 作为子进程拉起，并等到 `/health` 报 `ready` 才继续。
- **Vue3 + ECharts**（`frontend`）：`npm run build` 产物落在 `backend-go/internal/web/dist`（不入库）。`lover dash` 在 bundle 缺失或比 `frontend/src` 旧时自动构建；npm 不在 PATH 时跳过 Dashboard，报告路径不受影响。
- **本地 Ollama（可选）**：叙事走 OpenAI 兼容接口，**只接受回环地址**，云 API 一律 deny-by-default；`--ollama-model` 必填，不给就不出叙事。
- **默认值只有一个来源**：`backend-go/internal/app/paths.go`（数据库、NLP 地址、Ollama 地址、导出目录、Dashboard 地址）。任何命令都从这里取默认值，不允许自带第二份。

三条容易踩的坑（M1–M4 就存在，有了四条命令之后依然成立）：

1. **改了 `backend-go/internal/report/templates/*.tmpl` 必须重新 `go build`。** 模板经 `go:embed` 编进二进制，不重编译就是在跑旧模板——用 `lover` 也一样，它跑的是编译好的二进制。
2. **改了词表或分词后，已入库的词频不会自动回填。** 待办集按 anti-join 判定，已打分的块不会再进入待办集；需删掉 `word_freq` 的行或删库重跑。
3. **数据库不要放在 OneDrive / 坚果云 / 百度网盘目录下。** CLI 会在建库前拒绝这类路径（Windows 的「已知文件夹移动」会把 `Documents` 悄悄重定向进 OneDrive）；默认路径落在 `%LOCALAPPDATA%\lover-instance\` 正是因为这个。

> 数据库文件、导出 JSON、报告、模型权重均已在 `.gitignore`，不会入库。报告是自包含单页，打开时不产生任何网络请求（由 `report.ExternalRefs` 断言）。
