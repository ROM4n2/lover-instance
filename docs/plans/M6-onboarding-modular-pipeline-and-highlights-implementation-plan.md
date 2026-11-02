# M6 Onboarding, Modular Pipeline, and Highlights Implementation Plan

> **Goal**: 落地零门槛新手体验（`lover demo` + Web 拖拽导入向导）、分析管道接口化抽象（`Analyzer` DAG 注册表）、以及情侣关系高光与里程碑挖掘模块（初识日、连续天数火花、峰值日、深夜畅聊夜）。  
> **Tech Stack**: Go 1.26+ (Gin / modernc.org/sqlite / go:embed), Vue 3 + ECharts 5.5 + Element Plus, Python 3.11 (NLP).  
> **Spec Reference**: `docs/specs/2026-11-02-onboarding-modular-pipeline-and-highlights-design.md`  
> **Global Constraints**: 本地优先与单机回环绑定（127.0.0.1）；严格遵守 ADR-0001 合规铁律（绝不内置解密代码）；日志永不记录聊天正文与 wxid；时间分桶统一绑定东八区；数值渲染严格两位小数防 ECharts 轴溢出；保持现有 538 项测试（415 Go + 114 Python + 9 Audit）100% 绿灯。  
> **Branch**: `feat/m6-onboarding-modular-pipeline-highlights` — opened from `main` before Task 1, landed via PR.  
> **Repo Invariants Pointer**: `CLAUDE.md` & `AGENTS.md`  

---

## [NOTE] Decisions So Far

- **D1 演示沙箱使用已验证合成语料**：复用 `fixtures/sample_chat.json`（995 条已测试语料），专属标识 `talker="wxid_sample_lover"`，零隐私风险，毫秒级出图。
- **D2 分析器接口化与 DAG 拓扑排序**：抽象 `Analyzer` 契约，既有 `behavior`/`word_freq`/`sentiment`/`narrative` 平滑封装为标准分析器，支持声明式依赖解析与检查点单飞。
- **D3 Web 端拖拽流式保护**：`POST /api/import` 设 100MB 上限，使用临时文件缓冲写入，内存零大块分配，解析完即删。
- **D4 高光里程碑独立持久化**：SQLite 新增 `highlights` 表，支持幂等增量覆盖，供 HTML 报告与 Web Dashboard 双端复用。

---

## [NOTE] Active Frontier (Unblocked Tasks)

### Task 1: 分析器抽象与 DAG 拓扑注册表 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/analyze/analyzer.go`
- Create: `backend-go/internal/analyze/registry.go`
- Test: `backend-go/internal/analyze/registry_test.go`

**Interfaces:**
- Consumes: `store.Store` (`backend-go/internal/store/db.go:41`)
- Produces:
  - `type Analyzer interface { ID() string; Name() string; Dependencies() []string; Run(ctx context.Context, s *store.Store, opts Options, onProgress func(done, total int)) error }`
  - `type Registry struct`
  - `func NewRegistry() *Registry`
  - `func (r *Registry) Register(a Analyzer) error`
  - `func (r *Registry) Plan() ([]Analyzer, error)`

**SSOT Effect:** —

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Concurrency]`: 传递 `context.Context`，响应取消信号。
- [ ] `[Instinct: Zero-Silent-Fail]`: 循环依赖或未知依赖必须返回显式格式化错误，禁止吞错。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 1: Analyzer interface and DAG Registry.
> Mode: AFK | Role: TDD Builder
> Goal: Define Analyzer interface and Registry with cycle detection and topological sorting in internal/analyze.
> Target Files: Create backend-go/internal/analyze/analyzer.go, registry.go; Test registry_test.go.
> Injected Instincts: Concurrency context propagation, zero silent failures on cyclic dependencies.
> TDD Steps:
> 1. Write failing tests in registry_test.go (linear deps, cycle detection, missing dep).
> 2. Run test and verify RED.
> 3. Implement Analyzer interface and Registry with Kahn's algorithm or DFS in registry.go.
> 4. Verify all green.
> Return: Summary with MANDATORY Physical Execution Receipt (exit code + stdout snippet)."

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)** in `registry_test.go`
- [ ] **Step 2: Run test and verify it fails with expected message**
- [ ] **Step 3: Implement minimal production code (GREEN)** in `analyzer.go` & `registry.go`
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten with guard clauses (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**: `cd backend-go && go test -v ./internal/analyze`
- [ ] **Step 7: Git atomic commit** on `feat/m6-onboarding-modular-pipeline-highlights`

---

### Task 2: 既有分析流程适配纳管为标准 Analyzer [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/analyze/builtin.go`
- Modify: `backend-go/internal/analyze/pipeline.go:28-97`
- Modify: `backend-go/internal/app/app.go:200-350`
- Test: `backend-go/internal/analyze/pipeline_test.go`
- Test: `backend-go/internal/app/app_test.go`

**Interfaces:**
- Consumes: `analyze.Registry`, `analyze.RunSentimentProgress`
- Produces:
  - `func DefaultRegistry(nlpClient *nlp.Client, modelVersion string) *Registry`
  - `type BehaviorAnalyzer struct`
  - `type SentimentAnalyzer struct`

**SSOT Effect:** —

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Backward-Compatibility]`: 原有 `app.Run` 输出的 `Summary` 字段和退出码逐字节不变。
- [ ] `[Instinct: Zero-Lock DDL]`: 不持 SQLite 锁执行网络批处理。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 2: Wrap existing pipeline steps as standard Analyzers.
> Mode: AFK | Role: TDD Builder
> Goal: Encapsulate behavior and sentiment passes as standard Analyzers in internal/analyze/builtin.go and wire with app.Run.
> Target Files: Create backend-go/internal/analyze/builtin.go; Modify pipeline.go, app.go; Test app_test.go.
> Injected Instincts: Strict backward compatibility of Summary struct and exit codes.
> TDD Steps:
> 1. Write tests verifying DefaultRegistry runs existing passes correctly.
> 2. Implement builtin analyzers and wire to app.Run without altering external contracts.
> 3. Verify all existing 415 Go tests pass.
> Return: Summary with MANDATORY Physical Execution Receipt."

**Step Breakdown:**
- [ ] **Step 1: Write failing test (RED)** for `DefaultRegistry` in `pipeline_test.go`
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement builtin analyzers in `builtin.go` and refactor `app.Run`**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & flatten guard clauses**
- [ ] **Step 6: Physical Evidence Gate**: `cd backend-go && go test ./...`
- [ ] **Step 7: Git atomic commit**

---

### Task 3: 高光里程碑数据持久化与挖掘引擎 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/store/highlights.go`
- Create: `backend-go/internal/analyze/highlights.go`
- Modify: `backend-go/internal/store/db.go:120-180` (DDL migration)
- Test: `backend-go/internal/store/highlights_test.go`
- Test: `backend-go/internal/analyze/highlights_test.go`

**Interfaces:**
- Consumes: `store.Store`, `store.MessageRow`
- Produces:
  - `type HighlightEvent struct { ID int64; Talker, Kind, EventDate, Title, Description string; MetricValue float64; ExtraJSON string }`
  - `func (s *Store) SaveHighlights(ctx context.Context, talker string, events []HighlightEvent) error`
  - `func (s *Store) GetHighlights(ctx context.Context, talker string) ([]HighlightEvent, error)`
  - `type HighlightsAnalyzer struct`

**SSOT Effect:**
- Writes: `highlights` table in SQLite
- Timezone: East-8 (Asia/Shanghai) fixed via `+8 hours`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Timezone]`: 严格绑定东八区日界，跨天/跨年/深夜（00:00-05:00）判定一律带 `+8 hours`。
- [ ] `[Instinct: Math Boundary]`: 聊天记录少于 5 条或只有 1 天时，安全回退，零除零错误。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 3: Highlights storage and mining algorithm.
> Mode: AFK | Role: TDD Builder
> Goal: Create highlights table in store and HighlightsAnalyzer to detect origin, longest streak, peak days, and late night talks in East-8 timezone.
> Target Files: Create internal/store/highlights.go, internal/analyze/highlights.go; Modify db.go; Test highlights_test.go.
> Injected Instincts: Strict UTC+8 day boundaries, safe division by zero on small datasets.
> TDD Steps:
> 1. Write tests for Store Save/Get highlights and HighlightsAnalyzer calculation (streak, peak, late-night).
> 2. Run test and verify RED.
> 3. Implement DDL in db.go, Store methods, and HighlightsAnalyzer.
> 4. Verify all green.
> Return: Summary with MANDATORY Physical Execution Receipt."

**Step Breakdown:**
- [ ] **Step 1: Write failing tests in `highlights_test.go` (RED)**
- [ ] **Step 2: Run test and verify RED**
- [ ] **Step 3: Implement DDL, Store CRUD, and HighlightsAnalyzer (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & flatten guard clauses**
- [ ] **Step 6: Physical Evidence Gate**: `cd backend-go && go test -v ./internal/analyze ./internal/store`
- [ ] **Step 7: Git atomic commit**

---

### Task 4: 高光里程碑报告视图模型与 HTML 模板渲染 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Modify: `backend-go/internal/report/model.go:170-250`
- Modify: `backend-go/internal/report/collect.go:35-120`
- Modify: `backend-go/internal/report/templates/index.html` (or `render.go`)
- Test: `backend-go/internal/report/collect_test.go`
- Test: `backend-go/internal/report/render_test.go`

**Interfaces:**
- Consumes: `store.GetHighlights`
- Produces:
  - `type MilestoneView struct`
  - `type HighlightsView struct`
  - `func CollectHighlights(ctx context.Context, s *store.Store, talker string) (HighlightsView, error)`

**SSOT Effect:** —

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Offline-First]`: HTML 报告完全内联，不得引入外部 CDN 脚本或样式。
- [ ] `[Instinct: Precision]`: 浮点指标输出格式化为 2 位小数。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 4: Highlights DTO and Report Template Rendering.
> Mode: AFK | Role: TDD Builder
> Goal: Add HighlightsView to internal/report, collect milestone data, and render self-contained highlights cards in report.html.
> Target Files: Modify internal/report/model.go, collect.go, templates; Test collect_test.go, render_test.go.
> Injected Instincts: Self-contained zero-external-CDN HTML, 2-decimal-place numbers.
> TDD Steps:
> 1. Write tests in collect_test.go and render_test.go asserting highlights section presence.
> 2. Implement CollectHighlights and template integration.
> 3. Verify ExternalRefs stays empty and report renders offline.
> Return: Summary with MANDATORY Physical Execution Receipt."

**Step Breakdown:**
- [ ] **Step 1: Write failing test in `collect_test.go` and `render_test.go` (RED)**
- [ ] **Step 2: Run test and verify RED**
- [ ] **Step 3: Implement `HighlightsView` and HTML card template (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & verify `ExternalRefs` returns empty**
- [ ] **Step 6: Physical Evidence Gate**: `cd backend-go && go test -v ./internal/report`
- [ ] **Step 7: Git atomic commit**

---

### Task 5: CLI `lover demo` 与样本语料快速沙箱 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/cmd/lover/demo.go`
- Modify: `backend-go/cmd/lover/main.go:40-120`
- Modify: `backend-go/internal/app/paths.go`
- Test: `backend-go/cmd/lover/demo_test.go`

**Interfaces:**
- Consumes: `app.Run`, `fixtures/sample_chat.json`
- Produces:
  - `func runDemo(ctx context.Context, args []string, stdout, stderr io.Writer) int`
  - Command: `lover demo [--out <path>] [--open]`

**SSOT Effect:** —

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Zero-Model-Default]`: Demo 强制使用词典模式（零网络请求、零模型下载）。
- [ ] `[Instinct: Idempotent]`: 二次运行 demo 不报错，安全覆盖/复用样本数据。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 5: CLI lover demo command.
> Mode: AFK | Role: TDD Builder
> Goal: Add lover demo command in cmd/lover to load sample_chat.json, run lexicon analysis, and output demo report in under 2 seconds.
> Target Files: Create cmd/lover/demo.go; Modify cmd/lover/main.go; Test cmd/lover/demo_test.go.
> Injected Instincts: Lexicon-only zero-download, idempotent execution.
> TDD Steps:
> 1. Write tests in demo_test.go asserting lover demo flags, exit code 0, and output file generation.
> 2. Implement demo command wiring with fixtures/sample_chat.json.
> 3. Verify execution produces valid HTML report > 500KB.
> Return: Summary with MANDATORY Physical Execution Receipt."

**Step Breakdown:**
- [ ] **Step 1: Write failing test in `demo_test.go` (RED)**
- [ ] **Step 2: Run test and verify RED**
- [ ] **Step 3: Implement `runDemo` in `demo.go` and register in `main.go` (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & cleanup temporary test files**
- [ ] **Step 6: Physical Evidence Gate**: `cd backend-go && go test -v ./cmd/lover`
- [ ] **Step 7: Git atomic commit**

---

### Task 6: Web Dashboard 拖拽上传与一键 Demo API [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/api/demo.go`
- Create: `backend-go/internal/api/import.go`
- Create: `backend-go/internal/api/highlights.go`
- Modify: `backend-go/internal/api/server.go:100-130`
- Test: `backend-go/internal/api/demo_test.go`
- Test: `backend-go/internal/api/import_test.go`
- Test: `backend-go/internal/api/highlights_test.go`

**Interfaces:**
- Consumes: `store.Store`, `api.JobManager`
- Produces:
  - `POST /api/demo` -> `200 OK`
  - `POST /api/import` -> `202 Accepted` / `400 Bad Request` / `409 Conflict`
  - `GET /api/highlights?talker=...` -> `HighlightsView`

**SSOT Effect:** —

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: DOS-Guard]`: 限制上传请求体 `100MB` (`http.MaxBytesReader`)。
- [ ] `[Instinct: Privacy]`: 严禁在日志中记录文件名、聊天内容或 talker 标识。
- [ ] `[Instinct: Concurrency]`: 导入和分析复用 `analysis_jobs` 状态机互斥锁，冲突时返回 409。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 6: POST /api/demo, POST /api/import and GET /api/highlights endpoints.
> Mode: AFK | Role: TDD Builder
> Goal: Add demo, multipart import, and highlights endpoints to Gin API server.
> Target Files: Create internal/api/{demo,import,highlights}.go; Modify server.go; Test internal/api/*_test.go.
> Injected Instincts: MaxBytesReader 100MB cap, zero privacy leaks in logs, 409 job conflict guard.
> TDD Steps:
> 1. Write tests for /api/demo, /api/import (valid JSON, >100MB rejection, 409 conflict), and /api/highlights.
> 2. Implement handlers and route bindings in server.go.
> 3. Verify all green.
> Return: Summary with MANDATORY Physical Execution Receipt."

**Step Breakdown:**
- [ ] **Step 1: Write failing tests in `demo_test.go`, `import_test.go`, `highlights_test.go` (RED)**
- [ ] **Step 2: Run tests and verify RED**
- [ ] **Step 3: Implement handlers in `internal/api/` (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & flatten guard clauses**
- [ ] **Step 6: Physical Evidence Gate**: `cd backend-go && go test -v ./internal/api`
- [ ] **Step 7: Git atomic commit**

---

### Task 7: Vue 3 前端向导空状态与高光时间线卡片 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Modify: `frontend/src/api.js`
- Modify: `frontend/src/App.vue`
- Create: `frontend/src/components/HighlightsTimeline.vue`
- Create: `frontend/src/components/EmptyOnboarding.vue`
- Test: Build verification via `npm run build`

**Interfaces:**
- Consumes: `GET /api/overview`, `GET /api/highlights`, `POST /api/demo`, `POST /api/import`
- Produces:
  - Empty state with 1-click Demo & drag-drop upload zone
  - Highlights tab with streak, peaks, and milestones cards

**SSOT Effect:** —

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-XSS]`: 纯文本与属性绑定，绝无 `v-html` 或裸 `innerHTML`。
- [ ] `[Instinct: Build-Guard]`: 保证 Vite 构建输出零错误，dist 资源合规。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 7: Frontend Onboarding Empty State and Highlights Tab.
> Mode: AFK | Role: Frontend Builder
> Goal: Add EmptyOnboarding and HighlightsTimeline components in Vue3 frontend; support drag-and-drop JSON import and 1-click demo load.
> Target Files: Create frontend/src/components/{EmptyOnboarding,HighlightsTimeline}.vue; Modify App.vue, api.js.
> Injected Instincts: Zero-innerHTML XSS defense, build verification.
> Steps:
> 1. Add api.js methods (fetchHighlights, triggerDemo, uploadExportFile).
> 2. Create EmptyOnboarding.vue with drag-drop and demo buttons.
> 3. Create HighlightsTimeline.vue rendering milestone cards.
> 4. Mount in App.vue and verify with `npm run build`.
> Return: Summary with MANDATORY Physical Execution Receipt (exit code 0 of npm run build)."

**Step Breakdown:**
- [ ] **Step 1: Update `frontend/src/api.js` with new endpoints**
- [ ] **Step 2: Create `EmptyOnboarding.vue` and `HighlightsTimeline.vue`**
- [ ] **Step 3: Wire into `App.vue`**
- [ ] **Step 4: Run `npm run build` inside `frontend/` and verify Exit 0**
- [ ] **Step 5: Verify embedded build `-tags embedded` works in Go**
- [ ] **Step 6: Physical Evidence Gate**: `cd frontend && npm run build`
- [ ] **Step 7: Git atomic commit**

---

### Task 8: M6 端到端物理终验与发布前自检 [Tier: 3-Critical] [Mode: HITL] [Role: Release Auditor]

**Files:**
- Modify: `README.md` (Update CLI help with `lover demo` and Web onboarding instructions)
- Modify: `WORKMEMORY/PROJECT_OVERVIEW.md` & `work.log`
- Test: Full end-to-end smoke tests and release gate

**Interfaces:**
- Consumes: Entire pipeline

**SSOT Effect:** —

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Anti-Hallucination]`: 必须捕捉真实 CLI 输出与退出码，严禁凭假想断言。
- [ ] `[Instinct: Leak-Gate]`: 必须运行 `python3 scripts/release-audit.py` 并保证 9/9 项全绿。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Execute Task 8: M6 End-to-End Smoke Verification and Release Audit.
> Mode: HITL | Role: Release Auditor
> Goal: Verify lover demo, Web drag-drop, full regression suite (Go + Python), and release-audit.
> Target Files: Modify README.md, WORKMEMORY/work.log.
> Steps:
> 1. Run lover demo --out workmem/out/demo.html and verify report.
> 2. Run full Go test suite: cd backend-go && go test ./...
> 3. Run full Python test suite: PYTHONPATH=nlp-service python3 -m pytest nlp-service fixtures/
> 4. Run python3 scripts/release-audit.py.
> 5. Update README.md and append WORKMEMORY/work.log.
> Return: Physical receipts of all 4 verification steps."

**Step Breakdown:**
- [ ] **Step 1: Run `./lover demo` smoke test on real machine**
- [ ] **Step 2: Run Go full test suite (`cd backend-go && go test ./...`)**
- [ ] **Step 3: Run Python test suite (`PYTHONPATH=nlp-service python3 -m pytest nlp-service fixtures/`)**
- [ ] **Step 4: Run release audit (`python3 scripts/release-audit.py`)**
- [ ] **Step 5: Update documentation (`README.md`, `PROJECT_OVERVIEW.md`)**
- [ ] **Step 6: Physical Evidence Gate**: Complete test and audit receipts
- [ ] **Step 7: Final commit & PR preparation**

---

## [NOTE] Deferred Gap Register

| Gap | Why Deferred | Trigger That Forces Revisiting | Owner |
| :--- | :--- | :--- | :--- |
| **自定义恋爱纪念日表单** | 本期优先自动化挖掘事实（初识日、连续天数、峰值日），避免过早引入复杂前端表单与存储迁移 | 用户明确提出需要自定义结婚日/相识日等输入 | Product-UX |
| **异国跨时区多时区节律** | 目前双方默认在同处于东八区环境下统计，异国时差下的晚安/早安重叠度计算逻辑待专项标定 | 真实数据出现跨时区伴侣且用户反馈作息偏差时 | NLP-Specialist |
| **多语言本地化 (i18n)** | 目前主要服务中文微信聊天语境 | 出现英文/多语言聊天记录分析需求 | Frontend-Dev |

---

## [NOTE] Fog of War (Not Yet Specified)

- **[Unknown 1: 回复延迟的中位数分布]**: 互动响应延迟计算需要更精细的话轮切分规则（目前先由 Task 3 的高光峰值与连续天数覆盖核心需求，回复延迟分析器留待下一迭代）。
- **[Unknown 2: 话题演化 ThemeRiver]**: 月度话题词云的动态河流图需要更重型的流式布局算法，待基础高光时间线稳定后再扩展。

---

## [NOTE] Out of Scope

- **微信本地解密与内存读取**：严格遵守 ADR-0001 铁律，绝不内置解密代码，数据输入仅支持第三方导出的 JSON。
- **云端存储与多租户隔离**：本地单机私有工具，严格绑定 127.0.0.1，不做远程数据库与公网鉴权。
- **语音、图片与短视频深度分析**：仅分析文本与表情包符号，不引入重型多模态模型以防宿主机显存/内存溢出。
