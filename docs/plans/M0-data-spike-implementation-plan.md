# M0 取数 Spike 实施计划

> **Goal**: 实测 Windows 微信 4.1.9.30 被动取密钥可行性与数据格式，抓 payload 三证据定 canonical schema 唯一键，产出合成 fixture 解耦后续开发。
> **Tech Stack**: Go 1.24+ / Python 3.11+ / PowerShell（版本探测）
> **Spec Reference**: [docs/specs/2026-09-08-wechat-relationship-analysis-design.md](../specs/2026-09-08-wechat-relationship-analysis-design.md) §数据接入适配器 / M0 里程碑；[ADR-0001](../adr/ADR-0001-compliance-sidecar-adapter.md)
> **Global Constraints**:
> - **合规铁律**：本项目不内置/不分发解密代码；sidecar 由用户自装自担风险。
> - 所有出站 HTTP 显式超时（`http.NewRequestWithContext`，严禁零值 `http.Client{}`）；错误零静默吞。
> - Python 入口 UTF-8 stdout；Go `strict`；卫语句提前返回。
> - 隐私：合成 fixture 无隐私可入库；真实抓包 payload 只落 `workmem/`（gitignored），严禁入库。

---

## 背景：M0 的混合性质

M0 不可全自动：装 sidecar、跑微信登录态、被动取密钥均需用户手动/授权。因此分两部分——

- **A 部分（Task 1–3）**：纯代码、可 TDD，交给 `/vault-exec`；产出合成 fixture 让 M1+ 开发**不被取数阻塞**。
- **B 部分（Task 4–7）**：人机配合操作 + 证据抓取 + 验收落盘。**时间盒：1 个工作日**——超盒即按 soft gate 降级路径如实记录结论，不假绿、不拖期。

**M0 验收闸门**（三证据，见 spec；**hard/soft 拆分**——2026-09-22 评审阻塞③修复）：
- **Hard gate（必须全中，否则 M0 不通过）**：
  - (a′) 拿到 ≥1 条真实消息 raw payload，且样本中存在**同秒 ≥2 条**（证明 Seq 不可作主键）→ **定唯一键字段名**（MsgSvrID / id / server_id…，以实测为准，禁止猜）并冻结；
  - 隐私复查：`workmem/`、真实 payload、`*.db` 0 入库。
- **Soft gate（尽力而为，缺失不阻塞 M0，达成度如实记录、不假绿）**：
  - (a) 同一秒 ≥10 条完整 payload——自然难制造，制造法：同秒 2 条即可佐证，≥10 条为增强样本；
  - (b) 撤回消息前后两条 payload → 定 update 语义；**制造法=自发自撤**，自然出现更佳；
  - (c) webhook 推送 vs HTTP 历史 API 字段 diff → **降级法=同一窗口双拉取 diff**（webhook 不可用时）。

---

### Task 1: 合成语料 fixture 生成器 [Role: TDD Builder]

**Files:**
- Create: `fixtures/generate_fixture.py`
- Create: `fixtures/sample_chat.json`（生成产物，入库，无隐私）
- Test: `fixtures/test_generate_fixture.py`

**Interfaces:**
- Consumes: 无（纯标准库 `json`/`random`，用固定 seed 保确定性）
- Produces: `generate_chat(num_days=90, seed=42, out_path) -> None` 写 canonical 消息 JSON

**功能要求（对抗场景，喂 M1 静态断言测试用）：**
- 双人轮流发言：A 永远秒回（≤60s）、B 常常拖延（>10min）→ 验证秒回率/时延翻转；
- **同秒连发 50 条**（同一 ms 时间戳）→ 验证主键去重不丢条；
- 全积极 A / 全消极 B 语料 → 验证正负比方向；
- 场景覆盖：撤回消息、系统消息、红包、图片/语音占位、微信自定义表情 `[捂脸][旺柴]`、时间戳秒/毫秒/微秒混合、深夜 23 点后消息；
- 字段对齐 chatlog payload：`seq/time/talker/sender/isSelf/type/subType/content`；
- fixture 文件名**避开 `*_messages.json`**（避免被 `.gitignore` 误吞）。

**Subagent Prompt Scaffold (for /vault-exec):**
> "Implement Task 1: 合成语料 fixture 生成器。
> Goal: 用固定 seed 确定性生成对齐 chatlog payload 字段的双人聊天 JSON fixture，覆盖 8 类对抗场景（秒回/拖延、同秒 50 条、全积极全消极、撤回、系统/红包/媒体占位、表情、混时间戳、深夜）。
> Target Files: Create `fixtures/generate_fixture.py` + `fixtures/sample_chat.json`（生成品） + Test `fixtures/test_generate_fixture.py`。
> TDD Steps:
> 1. 先写失败测试（RED）：断言 fixture 字段齐全、同秒 50 条全部存在（count==50）、seed 固定时输出字节级稳定、8 类场景标记位存在。
> 2. 运行 `python -m pytest fixtures/test_generate_fixture.py -q` 验证失败。
> 3. 实现 `generate_fixture.py` 使测试绿（GREEN）。
> 4. 卫语句扁平化（max 2 层）；时间戳用固定基准时间字符串。
> 5. 用 `PYTHONIOENCODING=utf-8 python -m pytest fixtures/test_generate_fixture.py -q` 全绿。
> Return: 摘要 + 测试执行证据（实际输出）。"

**Step Breakdown:**
- [ ] Step 1: 写失败测试（字段/同秒50条/确定性/场景标记）RED
- [ ] Step 2: 运行测试验证失败
- [ ] Step 3: 实现生成器 GREEN
- [ ] Step 4: 卫语句扁平化 REFACTOR
- [ ] Step 5: 全绿 + 生成 `sample_chat.json`
- [ ] Step 6: git 原子提交 `test: add synthetic chat fixture generator`

---

### Task 2: canonical 模型 + 时间戳归一化 + 唯一键提取 [Role: TDD Builder]

**Files:**
- Create: `backend-go/go.mod`（模块布局：`module lover-instance/backend-go`，Go 1.24+；Task 2 是 backend-go 首个任务，此处初始化）
- Create: `backend-go/internal/model/message.go`
- Create: `backend-go/internal/model/message_test.go`

**Interfaces:**
- Consumes: `type RawChatlogMessage struct { Seq int64; MsgSvrID string; Time string; Talker string; Sender string; IsSelf bool; Type int; SubType int; Content string; Extra map[string]any }`（对齐 payload；**阻塞②修复**：`MsgSvrID` 显式可选字段 + `Extra` 兜底承载 id/server_id 等候选，字段名以 M0 实测为准，禁止猜）
- Produces:
  - `func ParseRaw(raw RawChatlogMessage) (CanonicalMessage, error)` —— 归一化 ms 时间戳（秒/毫秒/微秒/纳秒自识别）、`is_self` 判定
  - `func DetectUniqueKey(raw RawChatlogMessage) (string, error)` —— 按优先级取 `MsgSvrID` → `Extra["id"]` → `Extra["server_id"]` → 其余非空 `Extra` 标量；**全空返回 error**（零静默吞；M0 实测后钉死字段名）
  - `func NormalizeTs(rawTs string) (int64, error)` —— 时间戳归一化（东八区不在此步，仅转 epoch ms）

**约束:**
- 时间戳识别启发式：长度 10/13/16/19 → 秒/毫秒/微秒/纳秒 → 统一 ms；**毫秒位全 0 不判定为主键**（见 99-Inbox 坑）。
- 无效/缺失必返回 error，零静默吞。

**Subagent Prompt Scaffold (for /vault-exec):**
> "Implement Task 2: canonical 模型 + 时间戳归一化 + 唯一键提取。
> Goal: 定义对齐 chatlog payload 的 Go struct，实现秒/毫秒/微秒/纳秒 → ms 归一化，唯一键候选提取；所有无效输入显式 error。
> Target Files: Create `backend-go/go.mod` + `backend-go/internal/model/message.go` + Test `backend-go/internal/model/message_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：`"1756225000000"(ms)`、`"1756225000"(s)`、微秒/纳秒 → 同一 epoch ms；长度 11（无效）→ error；`IsSelf=true` 判定；`MsgSvrID` 非空 → 返回候选，`MsgSvrID`+`Extra` 全空 → error。
> 2. `cd backend-go && go test ./internal/model/...` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化；命名揭示意图；`time.Unix`/`time.UnixMilli` 处理。
> 5. `go test ./...` 全绿。
> Return: 摘要 + `go test` 实际输出证据。"

**Step Breakdown:**
- [ ] Step 1: 失败测试 RED
- [ ] Step 2: `go test` 验证失败
- [ ] Step 3: 实现 GREEN
- [ ] Step 4: 卫语句扁平化 REFACTOR
- [ ] Step 5: `go test ./...` 全绿
- [ ] Step 6: git 原子提交 `feat: add canonical message model with ts normalization`

---

### Task 3: 微信/sidecar 探测 CLI [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/probe/probe.go`
- Create: `backend-go/internal/probe/probe_test.go`
- Create: `backend-go/cmd/probe/main.go`

**Interfaces:**
- Produces:
  - `func FindWeChatPaths() []string` —— 扫候选路径（`C:\Program Files\Tencent\WeChat\WeChat.exe`、`Weixin.exe`、`Program Files (x86)`、用户目录 WeChat Files 等）
  - `func ProbeHTTP(baseURL string, timeout time.Duration) (*ProbeResult, error)` —— GET `:5030/api/v1/session` 探测 sidecar 在线、返回 6 态状态机（未装/微信没跑/密钥取不到/版本不支持/schema 不兼容/正常）
  - `func CheckPortOpen(addr string) bool` —— `net.DialTimeout` 3s
  - `func WeChatVersionExe(exePath string) (string, error)` —— 调 PowerShell `(Get-Item '...').VersionInfo.FileVersion`（stub 接口，M0 实测替换为 Go 读 PE 资源可选）

**约束:**
- 探测结果输出 JSON（`{wechat_exe, wechat_version, sidecar_state, http_ok, talker_count}`）。
- `ProbeResult` 必带 `raw_body`（sidecar 原始响应体，截断 ≤4KB）——schema 不兼容/版本差异取证用。
- HTTP 探测显式超时（`NewRequestWithContext`，connect 3s / read 30s）；port 探测 `net.DialTimeout`。
- 状态机 6 态每态有可执行 hint 文案（见 spec 适配器节）。

**Subagent Prompt Scaffold (for /vault-exec):**
> "Implement Task 3: 微信/sidecar 探测 CLI。
> Goal: 输出 JSON 状态的探测工具：扫微信 exe 候选路径、探测 sidecar :5030 的 6 态状态机、check port、调用 PowerShell 读 exe 版本（stub 接口保留）。
> Target Files: Create `backend-go/cmd/probe/main.go` + Test `backend-go/internal/probe/probe_test.go`（实现落 `backend-go/internal/probe`，与既有 module 同体——2026-09-22 reviewer 判定原 `tools/wechat_probe` 路径与 E2E 步骤 2 自相矛盾，已改正）。
> TDD Steps:
> 1. 失败测试（RED）：`FindWeChatPaths` 至少包含候选路径子串；`ProbeHTTP` 对不可达地址返回 6 态中的 failed 态；`CheckPortOpen` 对已知关闭端口返回 false（用 `127.0.0.1:1` 或随机未监听端口）；状态机每态输出含 hint 文案。
> 2. `cd backend-go && go test ./internal/probe/...` 验证失败。
> 3. 实现使绿（GREEN）；PowerShell 版本读取用可注入的 cmd 接口。
> 4. 卫语句扁平化；输出 JSON 用 `encoding/json`。
> 5. 全绿后 `go run .` 输出实际探测 JSON（记录证据）。
> Return: 摘要 + 测试与实跑输出证据。"

**Step Breakdown:**
- [ ] Step 1: 失败测试 RED
- [ ] Step 2: `go test` 验证失败
- [ ] Step 3: 实现 GREEN
- [ ] Step 4: 卫语句扁平化 REFACTOR
- [ ] Step 5: 全绿 + 实跑探测 JSON
- [ ] Step 6: git 原子提交 `feat: add wechat/sidecar probe CLI (6-state)`

---

## B 部分：人机配合操作（不可自动，需你授权/手动）

### Task 4: 安装并运行 sidecar（MANUAL，你操作）
**目标**: 选定并安装 sidecar 到宿主机（chatlog 续作 `myysophia/wechat-log` 优先，Windows 预编译版）。
**步骤:**
- [ ] 从 wechat-log Releases 下载 Windows 版（或 `go install`，需 CGO + MinGW 编译器——**建议直接下预编译**）。
- [ ] 关闭微信自动更新（设置→通用→升级：手动），避免跨 4.1.10.x 断点。
- [ ] 微信**保持登录态**，运行 `chatlog` → TUI 选"解密数据"→观察被动取密钥是否成功。
- [ ] 成功则开 HTTP 服务（:5030）；失败则记录错误输出（交给 Task 5 决策树）。
- [ ] 验证：`curl http://127.0.0.1:5030/api/v1/session` 返回非空 session 列表。

### Task 5: 真实取数 + 三证据抓取（MANUAL + 半自动脚本）
**目标**: 抓 M0 三证据落盘到 gitignored 工作目录。
**步骤:**
- [ ] 建 `workmem/evidence/`（gitignored），写证据抓取脚本 `workmem/grab_evidence.py`：拉某个 talker 的最近消息，抽样保存 raw JSON；出站 HTTP **显式 `timeout=30s`**（禁零值 client），失败打印错误不重试。
- [ ] 证据 (a)：翻页拉到同秒 ≥10 条消息的完整 payload → 记录唯一键候选字段名。
- [ ] 证据 (b)：找一条撤回消息，对比撤回前后两条 payload 的字段 diff。
- [ ] 证据 (c)：对比 webhook 推送（若可用）vs HTTP 历史 API 的字段 diff。
- [ ] 若被动取密钥失败：走决策树——`wechat-log` 失败 → `labazhou2024/chatlog-keeper`（debugger 方式，明示中-高封号风险仅提取那一刻）→ 仍失败 → 记录并让 M1 用 Task 1 fixture 先行。

**Subagent Prompt Scaffold (for /vault-exec，仅半自动脚本部分):**
> "Implement Task 5（半自动部分）: 证据抓取脚本。
> Goal: 写 `workmem/grab_evidence.py`——GET sidecar `:5030` 拉指定 talker 最近消息，抽样保存 raw payload 到 `workmem/evidence/`；显式 `timeout=30s`，失败打印错误、退出码非 0、不重试。
> Target Files: Create `workmem/grab_evidence.py`（gitignored，不入库）+ Test `workmem/test_grab_evidence.py`。
> TDD Steps:
> 1. 失败测试（RED）：对 `http.server` 假 sidecar 返回固定 payload，断言抽样文件字段完整；超时路径 exit code != 0。
> 2. 运行 `PYTHONIOENCODING=utf-8 python -m pytest workmem/test_grab_evidence.py -q` 验证失败。
> 3. 实现使绿（GREEN）；卫语句扁平化。
> 4. 全绿；真实抓取由人工触发。
> Return: 摘要 + 测试执行证据。

### Task 6: 唯一键确认 + schema 决策（VERIFY，你拍板）
**目标**: 依据证据 (a)(b)(c) 钉死 canonical schema 的 `source_msg_id` 字段名与撤回 update 语义。
**步骤:**
- [ ] 汇总证据到 `workmem/evidence/FINDINGS.md`：唯一键字段名（含截图级原始 payload 摘录）、撤回语义、双路径字段 diff 表。
- [ ] 若证据显示唯一键字段与 Task 2 候选一致 → 冻结；否则更新 Task 2 struct + spec，并**回刷 Task 1 fixture 字段措辞为实测字段名**。
- [ ] 更新 [spec](../specs/2026-09-08-wechat-relationship-analysis-design.md) §数据模型与 [ADR-0001](../adr/ADR-0001-compliance-sidecar-adapter.md) 决策结果。
- [ ] 更新 `workmem` 的 PROJECT_OVERVIEW 高频坑与 work.log（M0 结论，≤4KB 事件）。

**Subagent Prompt Scaffold (for /vault-exec，仅汇总部分；冻结决策由用户拍板):**
> "Implement Task 6（汇总部分）: 证据汇总 FINDINGS。
> Goal: 读 `workmem/evidence/*.json`，产出 `workmem/evidence/FINDINGS.md`：唯一键候选字段名（附原始 payload 摘录）、撤回前后字段 diff 表、双路径字段 diff 表。
> Target Files: Create `workmem/evidence/FINDINGS.md`。
> Steps（数据汇总，无 TDD——用确定性核对代替断言）:
> 1. 核对：条数与证据文件数一致、字段名非空、表格行数可回溯到具体 payload。
> 2. 输出 FINDINGS.md 路径 + 核对清单。
> Return: 摘要 + 路径。

### Task 7: M0 验收（CHECK）
**通过标准**:
- [ ] Task 1 fixture 已入库且 8 类对抗场景齐全（`pytest` 绿）。
- [ ] Task 2 时间戳归一化/唯一键提取单测绿；Task 3 探测 CLI 实跑输出真实 JSON。
- [ ] Task 4/5 hard gate：**拿到 ≥1 条真实微信消息** + 同秒 ≥2 条佐证 Seq 不可用 + 唯一键字段名冻结；soft gate (a≥10/b/c) 达成度如实记录（降级路径注明，不假绿）。
- [ ] Task 6：唯一键字段名冻结进 spec/ADR，工作日志更新。
- [ ] `go test ./...` + `python -m pytest fixtures/ -q` 全绿；隐私数据 0 入库（`git status` 复查）。

---

## 端到端验证
1. `python -m pytest fixtures/ -q` → fixture 绿。
2. `cd backend-go && go test ./...` → model/probe 绿。
3. `go run ./cmd/probe` → 输出本机微信/sidecar 真实状态 JSON（含版本号）。
4. `workmem/grab_evidence.py` 实跑 → 三证据文件 + FINDINGS.md。
5. `git status` 复查：`workmem/`、真实 payload、`*.db` 均未入库。

## 风险
- **取数不可行（最高）**：4.1.9.30 被动扫描若失败 → 决策树走 keeper debugger（明示风险）→ 仍失败则 M1 用 fixture 开发，真实取数延后，结论如实上报（不假绿）。
- **版本探测路径差异**：微信 4.x exe 名可能为 `Weixin.exe` → Task 3 多候选路径兜底。
- **CGO 编译障碍**：wechat-log 源码安装需 MinGW → 优先预编译版。
