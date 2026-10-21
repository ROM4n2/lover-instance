# M5 一键化实施计划

> **Goal**：把「起服务、装依赖、下模型、敲 13 个 flag、构建前端」压成**四条命令**，同时不削弱已验收的任何底线。
> **决策来源**：2026-10-20 `/dfs-grill` 两轮前沿选择（Track 1，用户拍板）。
> **前置**：M1–M4 与 Task 0 均已验收（`main`，14 个 Go 包 + `pytest` 92）。

## 已锁定的两个决策（用户拍板，不要再翻）

- **D1 保留 Dashboard，首次运行自动构建**。`dist` 仍**不入库**；`lover` 发现缺失或过期就自己跑 `npm run build`，npm 不可用则跳过并明说，报告路径不受影响。撤销条件已记录：连续两个月只点过「导出报告」就删 M3。
- **D2 开箱零模型**。默认只走词典版（0 MB 下载、110 ms 出报告）；模型是**显式升级**（`lover model add erlangshen`）。依据：174 句用户复核集上词典 acc 0.649 / macro-F1 0.607 并未输给 Erlangshen 的 0.615/0.596。

## 全局约束（不可协商）

- ** wrapper 不得复制任何默认值**（`--db` / `--nlp-url` / `--ollama-url`…）：抽取到共享包，两个入口共用一份。这是本次唯一的结构性改动，也是四类已知缺陷里「一规则两实现」的直接防线。
- **一键不许变成黑箱**：失败必须可分类——Python 未装 / 端口被占 / 服务不健康 / 模型缺失，四种各自的消息与退出码。
- **Ctrl-C 不许留孤儿进程**：wrapper 拉起的 uvicorn 必须随取消一起收掉。
- **进度必须是绝对覆盖度**，不是本次跑量（M2 踩过的坑）。
- 隐私铁律不变：日志与错误体不出现 content/talker/wxid；模型只从回环地址加载。

## Decisions

- **D3 入口是 Go 二进制，不是 `.ps1`**：脚本无法 import 内部包，只能 shell out 并复制默认值 → 必然漂移。Go 入口直接复用同一套编排。
- **D4 先抽 `internal/app`，再写 `cmd/lover`**：把 `cmd/analyze` 的 `run/execute/scoreSentiment` 与默认值函数搬进 `internal/app`，`cmd/analyze` 退化为薄壳（行为逐字节不变，现有测试全绿即是证据）。`cmd/lover` 建在同一层之上。
- **D5 `requirements.txt` 先钉版本再谈一键**：裸名依赖让「第 3 台机器的环境」不可复现，这是一键化的前置而非附属。

## 任务

### Task 1: 抽取共享编排层 `internal/app` [Tier: 2]
**Files**：Create `backend-go/internal/app/{app.go,paths.go}`；Modify `cmd/analyze/main.go`
- `paths.go`：`DefaultDBPath()` / `DefaultNLPURL()` / `DefaultOllamaURL()` / `NarrativeCacheDir()` / `ExportsDir()`——**唯一来源**。
- `app.go`：`Run(ctx, Options) (Summary, error)`，内含导入→行为→情感→渲染全流程；`Options` 用共享 paths 的默认值填充。
- `cmd/analyze` 改为解析 flag → 填 `Options` → 调 `app.Run`。**summary JSON 字段集、退出码、stderr 文案一律不变**（`main_test.go` 是守卫）。
**TDD**：抽取前后 `go test ./...` 全绿 + 真实库报告 sha256 与抽取前**逐字节相同**。

### Task 2: `requirements.txt` 钉版本 + `doctor` 的依赖体检 [Tier: 2]
- 按本机已验证的版本钉死六个依赖（`pip freeze` 取值，写进注释说明这是「已验证组合」而非「最新」）。
- `doctor` 逐项 import 探测，缺哪个说哪个，附安装命令；**不静默安装**（安装是 `lover` 首次运行的显式步骤，且要打印它在做什么）。

### Task 3: `lover model` —— 模型成为显式清单 [Tier: 2]
**Files**：Create `backend-go/cmd/lover/`（`models.go` 等）
- `list`：已装（sha 校验通过）/ 未装 / 校验失败三态，**不触发下载**（菜单 ≠ 就绪）。
- `add <version>`：调 `model_store.ensure_model`，进度与 sha 结果明示。
- `verify`：重算已装权重 sha，列出不符项。
- `rm <version>`：删目录，二次确认（或 `--yes`）。
- 版本表唯一来源 = `nlp-service/app/model_registry.py` 的 `MODELS`；**不在 Go 侧复制一份**。读取方式：`GET /api/health` 的 `available_versions` + 一条 `GET /api/models`（若需要 sha 元数据），或启动时读注册表——由实现选，但必须在注释里写明唯一来源。

### Task 4: `lover` / `lover sync` [Tier: 2]
- `lover`（无参数）= 首次运行：检查 Python 依赖（缺则装）→ 起 NLP 子进程 → 轮询 `/health` 至就绪（超时 60s）→ 跑分析 → 用系统默认程序打开报告。
- `lover sync --export <json>`：同上，但输出一行「新增 N 条 / 打分 M 条 / 覆盖率 x/y」，**叙事仅在聚合摘要变化时重新生成**（缓存 digest 已存在，直接复用）。
- `lover dash`：额外保证 dist 就绪（缺失/过期则 `npm run build`）→ 起 `cmd/server`。

### Task 5: 取消路径与失败分类硬化 [Tier: 3-Critical]
**Files**：`cmd/lover/{run.go,supervise.go}` + 测试
- 子进程用 `exec.CommandContext` + **进程组**（Windows 用 `CREATE_NEW_PROCESS_GROUP` 或 Job Object），取消时整组杀掉；测试用假 python（sleep）断言**无孤儿**。
- 失败分类：`exitUserError=2`（用法）/ `exitEnv=3`（依赖缺失、npm 缺失）/ `exitPort=4`（端口被占，附占用者 PID）/ `exitService=5`（服务起了但不健康）/ `exitRun=1`（分析本身失败）。每个码一条可执行的消息。
**TDD**：四类失败各自触发、各自的消息与退出码；取消后 `tasklist` 无残留 python。

### Task 6: M5 验收 [Tier: 3-Critical] [Mode: HITL]
- **零模型开箱**：临时移走 `%LOCALAPPDATA%\lover-instance\models` → `lover` 全程**零下载**、报告出词典版；`lover model list` 显示三个未装。
- **周更幂等**：`lover sync` 连跑两次，第二次「新增 0 条」且报告与第一次**逐字节相同**。
- **取消无孤儿**：打分中 Ctrl-C（或 SIGINT）→ 端口释放、job 行正确关闭、无残留 python。
- **回归**：`go test ./...` 14 包 ok、`pytest` 92、真实库报告与 M4 基线 sha256 一致（抽取 `internal/app` 不许改变输出）。
- 记录到 `workmem/evidence/M5-ACCEPTANCE.md`；README 快速开始改写为四条命令。

## Out of Scope

- 自动导出微信记录（ADR-0001：仓库不含取钥/解密代码，导出仍由外部工具完成）。
- 定时自动同步（cron/计划任务）：先有一键，再谈自动。
- 删掉 Python 服务改 Go 直调 ONNX：硬阻塞是 jieba 无等价 Go 实现，`countable_tokens` 需整套移植并对齐 88,049 词频基线——Ladder Level 0 判为不值。
- 云端 LLM（ADR-0004 deny-by-default 不变）。

## 依赖与风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 抽 `internal/app` 改变既有行为 | M1–M4 验收证据作废 | Task 1 的验收就是「报告 sha256 逐字节相同 + 全量测试绿」，不过就不合 |
| 一键脚本自身变黑箱 | 比多敲命令更糟 | Task 5 的五类退出码 + 每条可执行消息；`doctor` 先跑一遍再谈自动化 |
| 孤儿进程占端口 | 下次运行必失败 | 进程组 + 取消测试；`doctor` 报告占用者 PID |
| 依赖版本钉死后过期 | 安全更新进不来 | 注释写明这是「已验证组合」；`lover doctor` 提示可升级，但不自动升 |
| `npm` 不可用 | Dashboard 起不来 | 自动构建失败即跳过并明说，报告路径独立可用 |
---

## 执行记录与偏离（2026-10-21 回填，以代码为准）

**验收**：`workmem/evidence/M5-ACCEPTANCE.md`（9 条判据：8 ✅ + 判据 9「零模型开箱」在收尾阶段补做了物理演练）。`main` 至 `0353ddc`；16 个 Go 包 ok、`pytest` 100 passed。

| 计划原文 | 实际实现 | 原因 |
| --- | --- | --- |
| Task 3 模型元数据二选一：(a) `GET /api/models` / (b) 解析 `model_registry.py` | **第三个方案**：子进程执行 `python -m app.model_cli registry` 取 JSON | 菜单必须能在**服务没起**时工作（(a) 要求服务在线），而 (b) 的文本解析会在注册表写法变化时静默失效。`add` 本来就要在 Python 进程里跑 `ensure_model`，顺路取注册表最省且单一来源不变 |
| Task 5「Windows 用 `CREATE_NEW_PROCESS_GROUP` 或 Job Object」 | `CREATE_NEW_PROCESS_GROUP` + `CTRL_BREAK` + `taskkill /T /F` 兜底；把 `cmd.Cancel` 换成杀组并设 `WaitDelay` | Job Object 要给每个子进程建内核句柄 + 完成端口，成本记在注释里；`WaitDelay` 是因为默认情况下孙进程占着输出管道会让 `Wait` 永久阻塞——这条正是变异②的载荷 |
| Task 4 `dash`「`go build` 或 `go run` 你定」 | `go build` 到 `%TEMP%` 再 exec，且**起进程后注册清理**（收尾阶段补） | 少一层父进程与管道拷贝，产物可入进程组。临时 exe 现由 defer 在进程回收之后删除，正常退出与取消两条路径都覆盖 |
| 未规定 `lover` 无 `--export` 时怎么办 | 取 `ExportsDir()` 里最新的 `*.json`；没有则 `exitRun` 并说明 | 一键化的自然读法；当前该目录只有导出的 HTML，所以走"未找到导出" |
| 未规定叙事默认 | `Options.OllamaModel` **无默认值**（不猜模型名），叙事默认关闭 | 猜一个模型名会在用户机器上把 4 秒的增值项变成 120 秒的超时 |
| 未规定 | `cmd/server` 的 `defaultNLPURL`/`defaultAddr` 一度是第二份实现，收尾阶段删除并改读 `app`，另加**源码字面量守卫** | 这正是本计划的「一规则两实现」条款要防的；光测值不够，还要测"字面量不许再出现" |
| 未规定 | `lover model list` 在 0 个文件时报 "an earlier add was interrupted"，收尾阶段改为三态文案 | 真机演练时它把"从未安装过"报成"安装中断"，是事实错误不是措辞问题 |

### 执行期发现的两个既有缺陷（不是本计划引入）

1. **`lover dash` 从未真正起过服务**：`exec.Command` 建命令而监督层设 `cmd.Cancel`，Go 在 `Start()` 直接拒绝。40 条测试全绿，因为它们整体替换了启动 seam。**教训：seam 覆盖到「被验证对象本身」时，必须有一条不替换它的真机冒烟。**
2. **`lover model list` 的三态归因错误**（见上表最后一行）。

### 遗留（均不阻塞）

- `taskkill /T /F` 强杀 `lover` 会留下孙进程 `server`——`/F` 不给程序跑收尾的机会；Ctrl-C 与服务自退两条路径都正常清理。Windows 固有行为。
- ~~判据 9 的演练为一次性动作~~ → **已固化为 `lover doctor` 的只读检查**（`95705a8` / `3f6dc00`）：`python -m app.model_cli lexicon-probe` 用一句写死的合成文本走词典分支，输出 `{"ok":true,"needs_weights":false,...}`；doctor 打印 `lexicon path: works without any model weights`，模型目录为空时额外说明 `no models installed: that is the supported default, not a problem`。**不下载、不建目录、不联网**（`ensure_model` 有一条「一调就 raise」的替身测试把守）。
