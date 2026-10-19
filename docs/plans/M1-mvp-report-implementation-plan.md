# M1 端到端最小闭环 + 首份 report.html 实施计划

> **Goal**: 打通「导入一个双人会话 → 落库 → 词典法分析 → 生成可双击打开、含热力图/词云/情感基线/免责面板的单文件 `report.html`」的端到端最小闭环。
> **Tech Stack**: Go 1.24+ / Python 3.11+ / SQLite（`modernc.org/sqlite`，免 CGO）/ Go `html/template` + `go:embed`。
> **Spec Reference**: [docs/specs/2026-09-08-wechat-relationship-analysis-design.md](../specs/2026-09-08-wechat-relationship-analysis-design.md) §数据模型 / §指标体系 M1 / §NLP 流水线 / §交付物；[ADR-0001](../adr/ADR-0001-compliance-sidecar-adapter.md)（合规边界与 M0 实测契约）/ [ADR-0002](../adr/ADR-0002-job-state-machine-chunked-batch.md)（块级批处理）/ [ADR-0003](../adr/ADR-0003-self-contained-html-report.md)（HTML 报告为分享交付物）/ [ADR-0004](../adr/ADR-0004-sentiment-honesty-cloud-llm-deny.md)（情感诚实化）
> **Global Constraints**:
> - **合规铁律**：本仓库不内置、不分发、不协助开发任何解密或密钥提取代码；数据只经 `internal/source/chatlogkeeper` 消费用户自行导出的 JSON。
> - **隐私**：`content` / `talker` / `nickname` / `wxid` **永不入日志**（只记 id/count/耗时）；真实聊天库、导出 JSON、`report.html` 一律不入库（`.gitignore` 已覆盖 `*.db` / `workmem/` / `*.report.html`）；SQLite 路径必须避开 OneDrive/坚果云等同步盘（README 红字警告）。
> - **数据正确性**：时间戳**固定东八区分桶**（`+8 hours`）；唯一键用 M0 冻结的 `server_id` + `source_offset` 兜底；增量走 **60s 回扫 + 自然键去重 upsert**，不得用水位整段跳过；**严禁零值 `http.Client{}`**。
> - **错误处理零静默吞**：任何失败路径必须显式返回错误或落地为可见状态；ingest **绝不允许以"成功 0 行"收场**。
> - **编码**：Python 入口 UTF-8 stdout（Windows GBK 免疫）；Go 卫语句提前返回、嵌套 ≤2 层。
> - **依赖**：Go 现有零第三方依赖，M1 仅新增 `modernc.org/sqlite`；Python 仅 `fastapi` / `uvicorn` / `jieba` / `cnsenti`。
> **Branch**: **trunk 例外（显式声明）**——本仓库为纯本地仓库、**无远端**，`git remote -v` 为空，14 个既有提交全部直接落在 `main`，PR 流程在本环境下不成立。故本计划所有任务在 `main` 上以原子提交推进，**不新建 feature 分支、不开 PR**。经用户 2026-10-20 明确确认。

---

## [*]️ Decisions So Far

- [ADR-0001](../adr/ADR-0001-compliance-sidecar-adapter.md)：数据接入走外部 sidecar 适配器，仓库不含解密代码。**M0 实测已冻结契约**：数据源为 `chatlog-keeper` 导出 JSON；字段名 `server_id` / `source_offset` / `ts`(float 秒) / `conversation_id` / `sender_wxid` / `msg_type` / `is_self` / `content`；唯一键 `server_id` → `source_offset` 兜底。
- [ADR-0002](../adr/ADR-0002-job-state-machine-chunked-batch.md)：长任务须有状态机与块级批处理。**M1 只落块级批处理四要素**（批大小/块级超时/短事务检查点/块级重试），`analysis_jobs` 状态机表随迁移建好但编排留到 M2。
- [ADR-0003](../adr/ADR-0003-self-contained-html-report.md)：分享交付物是自包含单文件 HTML，非绑定本机的 Dashboard 链接。
- [ADR-0004](../adr/ADR-0004-sentiment-honesty-cloud-llm-deny.md)：情感结论诚实化命名 + 报告内置免责面板；M1 用词典法，**云端 LLM deny-by-default**。
- **D1（M1 定为 CLI，不做 REST）**：spec 的 `POST /api/analyze` 等 REST 面属于 Dashboard 时代（M3）。M1 交付物是报告文件，交互入口用 CLI `cmd/analyze` 即可，避免为无人调用的接口先建 HTTP 层（YAGNI）。REST/前端留 M3。
- **D2（词云用 ECharts 插件）**：内联 `echarts-wordcloud`（MIT）而非服务端渲染 SVG，理由是与报告内其余图表同库、配色与交互一致；代价是产物再增约 50KB。若实测单文件超出可接受体积，回落方案为服务端 SVG（记录于本计划的 Fog of War）。
- **D3（不改动既有适配器契约）**：M1 直接复用 `internal/source/chatlogkeeper` 与 `internal/model`，不改字段映射；新增的只有存储、分析、渲染三层。
- **D4（指标口径以 spec §指标体系 M1 为准）**：只做确定性描述统计 + 分词词频 + 词典情感基线；SKEP/RoBERTa（M2）、话题词云随时间（M4）、LSM/情绪惯性/滞后互相关/变点检测（已砍）均不在 M1。

---

## [*] 文件占用分区（File-Occupancy）

本计划的任务**全部按序执行**，不并行派发 —— 依赖链是 store → ingest → nlp → analyze → report → cli。因此不需要独占式文件表；但仍声明跨任务触碰同一文件的边界，防止 `/dfs-exec` 误判可并行：

| 文件 | 首个创建者 | 后续修改者 | 约束 |
| --- | --- | --- | --- |
| `internal/store/metrics.go` | Task 9 | Task 10 | Task 10 必须等 Task 9 提交后再改 |
| `backend-go/go.mod` | Task 1（加 `modernc.org/sqlite`） | 无 | 其余任务禁止再加依赖 |
| `internal/report/` | Task 11（model/collect） | Task 12（assets）、Task 13（render/templates） | 三者文件不重叠，但 Task 13 消费前两者的产物 |
| `fixtures/` | 已存在 | 无 | M1 **不修改** fixture 与 `internal/model`、`internal/source` |

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
  - `func Open(dsn string) (*Store, error)` —— DSN 走函数式 pragma，内部 `SetMaxOpenConns(1)`
  - `func (s *Store) Migrate(ctx context.Context) error` —— 幂等建表（`IF NOT EXISTS`）
  - `func (s *Store) Close() error`
  - `type Store struct{ db *sql.DB }` + `func (s *Store) DB() *sql.DB`（仅测试与同包使用）
  - DSN 构造器 `func DSN(path string) string` —— 产出 `file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Zero-Lock DDL]`：迁移必须幂等（`IF NOT EXISTS`），重复 `Migrate` 不报错、不重建。
- [ ] `[Instinct: Single-Writer]`：`SetMaxOpenConns(1)` 必须在 `Open` 内完成，禁止由调用方决定。
- [ ] `[Instinct: Privacy-Path]`：提供 `func RejectSyncedPath(path string) error`，命中 OneDrive/坚果云/百度网盘关键字时显式拒绝启动并给出可执行提示。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 1: SQLite store 打开 + PRAGMA + schema 迁移。
> Mode: AFK | Role: TDD Builder
> Goal: 用 `modernc.org/sqlite` 打开库、强制 WAL/busy_timeout/synchronous/foreign_keys 与 `SetMaxOpenConns(1)`，并幂等建立 spec §数据模型 的全部表。
> Target Files: Create `backend-go/internal/store/store.go`、`backend-go/internal/store/migrations.go`、Test `backend-go/internal/store/store_test.go`；Modify `backend-go/go.mod`。
> 表清单严格照 spec：messages / contacts / sentiment / daily_stats / activity_heatmap / conversations / word_freq / analysis_jobs / ingest_state，含全部 UNIQUE 索引（`uq_msg_source`、`uq_msg_natural`、`uq_jobs_active`）与 `idx_msg_talker_ts`。
> Injected Instincts: Zero-Lock DDL（幂等迁移）、Single-Writer（Open 内设 MaxOpenConns=1）、Privacy-Path（同步盘路径拒绝）。
> TDD Steps:
> 1. 写失败测试 `store_test.go`（RED）：`Open` 后 `PRAGMA journal_mode` 返回 `wal`；`PRAGMA busy_timeout` ≥10000；`db.Stats().MaxOpenConnections == 1`；`Migrate` 连跑两次不报错；九张表 `sqlite_master` 均可查到；`RejectSyncedPath` 对含 `OneDrive` 的路径返回 error、对普通路径返回 nil。
> 2. `cd backend-go && go test ./internal/store/...` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化（≤2 层）；DSN 用 `net/url` 拼 query 而非字符串相加。
> 5. `go test ./...` 全绿。
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

### Task 2: messages 幂等 upsert + content_sha1 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/store/messages.go`
- Create: `backend-go/internal/store/messages_test.go`

**Interfaces:**
- Consumes: `store.Store`（Task 1）、`model.CanonicalMessage`
- Produces:
  - `func ContentSHA1(content string) string` —— 归一化后（去首尾空白）算 SHA-1 十六进制
  - `type InsertResult struct{ Inserted, Skipped int }`
  - `func (s *Store) UpsertMessages(ctx context.Context, source string, msgs []model.CanonicalMessage) (InsertResult, error)` —— 单事务批量 upsert；source_msg_id 非空走 `uq_msg_source`，为空走 `uq_msg_natural`；**冲突即跳过并计数，不更新已有行**

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Silent-Loss]`：`Inserted + Skipped` 必须等于输入条数，否则返回 error。静默丢数据是最高级别缺陷。
- [ ] `[Instinct: Batch-Transaction]`：整批一个短事务；禁止逐条 `Exec` 不包事务。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 2: messages 幂等 upsert + content_sha1。
> Mode: AFK | Role: TDD Builder
> Goal: 把 canonical 消息批量写入 messages 表，靠两个 UNIQUE 索引去重，重复导入产生完全相同的库状态。
> Target Files: Create `backend-go/internal/store/messages.go`、Test `backend-go/internal/store/messages_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：同一批消息 upsert 两次 → 第二次 `Inserted==0` 且 `messages` 行数不变；`source_msg_id` 为空的两条**内容相同**消息 → 第二次被 `uq_msg_natural` 去重；`source_msg_id` 为空但内容不同 → 两条都入库；`Inserted+Skipped==len(input)` 恒成立；`ContentSHA1` 对 `"a"` 与 `" a "` 返回同值。
> 2. `cd backend-go && go test ./internal/store/...` 验证失败。
> 3. 实现使绿（GREEN）；SQL 用参数绑定，禁止字符串拼接。
> 4. 卫语句扁平化；`ON CONFLICT DO NOTHING` 配合 `RowsAffected` 统计。
> 5. `go test ./...` 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(store): add idempotent message upsert with content hash dedup`

---

### Task 3: ingest 编排 —— 导出 → 落库 + 60s 回扫水位 + 静默空洞守卫 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/ingest/ingest.go`
- Create: `backend-go/internal/ingest/ingest_test.go`

**Interfaces:**
- Consumes: `chatlogkeeper.ParseMessages(io.Reader) ([]model.CanonicalMessage, error)`、`store.UpsertMessages`、`store.Store`
- Produces:
  - `type Options struct{ Source, Talker string; Reader io.Reader }`
  - `type Report struct{ Parsed, Inserted, Skipped int; WindowStartMs int64 }`
  - `func Run(ctx context.Context, s *store.Store, opts Options) (Report, error)`
  - 水位读写：`func (s *Store) IngestState(ctx, source, talker) (lastTs int64, ok bool, err error)` / `func (s *Store) SaveIngestState(ctx, source, talker string, lastTs int64, lastMsgID string) error`（落在 `internal/store/ingest_state.go`）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Empty-Success]`：导出解析出 N>0 条但落库 `Inserted==0` 且库里该 talker 总行数为 0 → **必须返回 error**，不得以"成功 0 行"收场。
- [ ] `[Instinct: Timezone-Bucket]`：窗口与水位一律 epoch ms，**不在此层做时区换算**（分桶只在指标层做）。
- [ ] `[Instinct: Rescan-Window]`：水位起点 = `last_ts - 60_000`，且**首次导入不受水位限制**。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 3: ingest 编排。
> Mode: AFK | Role: TDD Builder
> Goal: 读 chatlog-keeper 导出 → 规范化 → 按 60s 回扫窗口落库 → 更新 ingest_state 水位；空结果必须报错。
> Target Files: Create `backend-go/internal/ingest/ingest.go`、`internal/store/ingest_state.go`、Tests `internal/ingest/ingest_test.go` + `internal/store/ingest_state_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：用 `fixtures/sample_chat.json` 全量 ingest → `Parsed==Inserted`（首次无水位）；再跑一次 → `Inserted==0, Skipped==Parsed`；显式传入 `last_ts` 后，早于 `last_ts-60s` 的消息被排除；**空导出（`[]`）→ 返回 error**；导出非空但全部是已存在行且该 talker 库内 0 行 → 返回 error。
> 2. `cd backend-go && go test ./internal/ingest/...` 验证失败。
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
- Create: `nlp-service/app/__init__.py`
- Create: `nlp-service/app/main.py`
- Create: `nlp-service/app/health.py`
- Create: `nlp-service/tests/test_health.py`

**Interfaces:**
- Consumes: 无（首层）
- Produces:
  - `GET /health` → `{"status": "ready"|"loading", "model_loaded": bool}`
  - `create_app() -> FastAPI`（工厂函数，便于测试注入）
  - 词典加载状态 `_LEXICON_STATE: dict`（M1 零模型，`model_loaded` 恒 false、`status` 由词典是否就绪决定）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: UTF8-Stdout]`：`main.py` 入口首行配置 `sys.stdout.reconfigure(encoding="utf-8")`，免疫 Windows GBK 崩溃。
- [ ] `[Instinct: No-File-Handle]`：本服务**绝不打开任何 SQLite 文件**（spec 硬约束），只收发 HTTP 批次。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 4: nlp-service 脚手架 + GET /health。
> Mode: AFK | Role: TDD Builder
> Goal: 建 FastAPI 微服务骨架与健康检查，区分 loading/ready。
> Target Files: Create `nlp-service/{requirements.txt, app/__init__.py, app/main.py, app/health.py}`、Test `nlp-service/tests/test_health.py`。
> 依赖仅 fastapi / uvicorn / jieba / cnsenti（requirements.txt 固定版本下限）。
> TDD Steps:
> 1. 失败测试（RED）：用 `fastapi.testclient.TestClient(create_app())` 断言 `GET /health` 返回 200、含 `status` 与 `model_loaded` 两个键、`status ∈ {"loading","ready"}`；断言服务进程不 import sqlite3（AST 扫描 `app/`，禁止出现 `sqlite3` import）。
> 2. `cd nlp-service && PYTHONIOENCODING=utf-8 python -m pytest tests/ -q` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化。
> 5. 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt（退出码 + passed 数）。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
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
  - `def tokenize(text: str) -> list[str]` —— 去停用词、载入自定义词典后的词序列
  - `def word_counts(texts: list[str]) -> dict[str, int]`
  - `def emoji_polarity(mark: str) -> float | None` —— 微信自定义表情 `[捂脸]`/`[旺柴]` 的极性；**未知表情返回 `None`**（= 中性并标记，不得默认 0 与"已知中性"混淆）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-False-Neutral]`：未知表情必须返回 `None` 而不是 `0.0`，否则下游无法区分"未知"与"确知中性"。
- [ ] `[Instinct: Deterministic-Order]`：`word_counts` 的返回顺序不得依赖 dict 插入序；Top-N 排序必须显式指定 tie-break（词串字典序）。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 5: 分词与词频。
> Mode: AFK | Role: TDD Builder
> Goal: jieba 分词 + 停用词过滤 + 自定义词典（昵称/爱称/网络梗）+ 微信自定义表情极性映射。
> Target Files: Create `nlp-service/app/tokenize.py`、`nlp-service/data/{stopwords.txt, custom_dict.txt, emoji_map.json}`、Test `nlp-service/tests/test_tokenize.py`。
> TDD Steps:
> 1. 失败测试（RED）：停用词（的/了/是）被滤除；自定义词典词（如"宝子"）作为**单个 token** 出现而非被切开；`[捂脸]` 返回非 `None` 极性；`[不存在的表情]` 返回 `None`；`word_counts` 对并列词频按词串字典序稳定排序。
> 2. `cd nlp-service && PYTHONIOENCODING=utf-8 python -m pytest tests/ -q` 验证失败。
> 3. 实现使绿（GREEN）。
> 4. 卫语句扁平化；词典文件用 UTF-8 显式 `encoding=` 打开。
> 5. 全绿。
> Return: 摘要 + MANDATORY Physical Execution Receipt。"

**Step Breakdown:**
- [ ] **Step 1: Write the failing test (RED)**
- [ ] **Step 2: Run test and verify it fails**
- [ ] **Step 3: Implement minimal production code (GREEN)**
- [ ] **Step 4: Run tests and verify all green**
- [ ] **Step 5: Refactor & Flatten (REFACTOR)**
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(nlp): add tokenizer with stopwords and wechat emoji map`

---

### Task 6: 词典情感基线（DUTIR 7 情绪 + HowNet 程度/否定/转折） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `nlp-service/app/sentiment_lexicon.py`
- Create: `nlp-service/data/dutir_lexicon.txt`
- Create: `nlp-service/data/hownet_degree.txt`
- Create: `nlp-service/data/hownet_negation.txt`
- Create: `nlp-service/tests/test_sentiment_lexicon.py`

**Interfaces:**
- Consumes: `tokenize()`
- Produces:
  - `class Score(BaseModel)`：`polarity: float`（−1..1）、`emotion: str`（DUTIR 7 类之一或 `"neutral"`）、`intensity: float`（1..9 归一到 0..1）
  - `def score(text: str) -> Score`
  - `MODEL_VERSION = "dutir-hownet-v1"`（写进 `sentiment.model_version`，与 M2 的模型版共存可对比）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Honest-Naming]`：模块名与 docstring **禁止**出现"关系诊断""Gottman"等表述，只称"词典基线极性"（ADR-0004）。
- [ ] `[Instinct: Window-Bounded]`：否定词窗口固定前 3–5 词，系数 ×−0.5（非 ×−1）；程度副词 ×0.5–2.0。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 6: 词典情感基线。
> Mode: AFK | Role: TDD Builder
> Goal: DUTIR 7 情绪 + HowNet 程度副词/否定窗口/转折规则，输出 polarity/emotion/intensity。
> Target Files: Create `nlp-service/app/sentiment_lexicon.py`、`data/{dutir_lexicon.txt, hownet_degree.txt, hownet_negation.txt}`、Test `nlp-service/tests/test_sentiment_lexicon.py`。
> TDD Steps:
> 1. 失败测试（RED）：纯正向句 polarity>0；纯负向句 polarity<0；中性句 polarity==0 且 emotion=="neutral"；否定句（"不开心"）极性翻转但**幅度小于**肯定句（验证 ×−0.5 而非 ×−1）；程度副词放大（"非常开心" > "开心"）；`intensity` 落在 0..1；`MODEL_VERSION` 常量存在。
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
- [ ] **Step 6: Physical Evidence Gate**
- [ ] **Step 7: Git atomic commit** `feat(nlp): add DUTIR/HowNet lexicon sentiment baseline`

---

### Task 7: 批处理契约 POST /score [Tier: 3-Critical] [Mode: AFK] [Role: TDD Builder]

*Tier 3 理由：批处理契约是 spec 明示"事后返工成本最高"的部分（§NLP 流水线 5），接口形状一旦被 Go 侧依赖即难以更改。*

**Files:**
- Create: `nlp-service/app/score.py`
- Create: `nlp-service/tests/test_score.py`

**Interfaces:**
- Consumes: `score()`（Task 6）、`tokenize()`（Task 5）
- Produces:
  - `POST /score` 请求体 `{"model_version": str, "items": [{"msg_id": int, "content": str}]}`，**items ≤ 256**
  - 响应体 `{"model_version": str, "results": [{"msg_id": int, "polarity": float, "emotion": str, "intensity": float}], "top_words": {"self": [...], "partner": [...]}}`
  - `def score_batch(items: list[dict]) -> list[dict]` —— **纯函数、无副作用、不写任何存储**
  - 超限（>256 条或 body >5MB）返回 413 并附明确 `code`

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Pure-Function]`：`score_batch` 不得写文件/库/全局可变状态；测试须断言连续两次调用结果完全一致。
- [ ] `[Instinct: No-Query-Log-Leak]`：日志不得输出 `content`；若需记录只记 `msg_id` 与条数。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 7: 批处理契约 POST /score。
> Mode: AFK | Role: TDD Builder
> Goal: 定义 Go↔Python 的批处理接口，256 条上限、纯函数、无副作用、错误必须显式。
> Target Files: Create `nlp-service/app/score.py`、Test `nlp-service/tests/test_score.py`。
> TDD Steps:
> 1. 失败测试（RED）：正常批次返回与输入等长的 results 且 `msg_id` 一一对应；257 条 → 413 且 body 含 `code`；同一批次连调两次结果字节级一致（纯函数性）；空 items → 200 且 results 为空数组（不是 500）；`top_words` 同时含 `self` 与 `partner` 两个键。
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
  - `type Client struct{ baseURL string; http *http.Client }`
  - `func NewClient(baseURL string) (*Client, error)` —— 内部构造**非零值** `http.Client`（connect 3s / 总 10min 硬兜底）
  - `type Item struct{ MsgID int64; Content string }`
  - `func (c *Client) Score(ctx context.Context, modelVersion string, items []Item) ([]Result, error)` —— 单批调用，块级 ctx 由调用方给 5min
  - `func (c *Client) Health(ctx context.Context) (Health, error)`
  - 块级重试内置：仅对 connect reset / 5xx / 超时重试，指数退避 + full jitter，**上限 2 次**

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Zero-Client]`：`NewClient` 必须显式设置 `Timeout`，测试断言 `client.http.Timeout > 0`。
- [ ] `[Instinct: Block-Retry-Only]`：重试只发生在单块内；**任务级不得重试**（否则 60min 任务再叠 60min）。
- [ ] `[Instinct: Privacy-Log]`：错误信息中不得回显请求体内容。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 8: Go NLP 客户端。
> Mode: AFK | Role: TDD Builder
> Goal: 按 ADR-0002 的四要素实现块级调用：批大小由调用方定、块级超时 5min、硬兜底 10min、块级重试指数退避+jitter 上限 2。
> Target Files: Create `backend-go/internal/nlp/client.go`、Test `backend-go/internal/nlp/client_test.go`（用 `httptest.Server` 做假服务）。
> TDD Steps:
> 1. 失败测试（RED）：`NewClient` 返回的非零 `Timeout > 0`；假服务返回 200 时解析出等长结果；假服务连续返回 500 三次 → 返回 error 且**只尝试了 3 次**（1 次 + 2 重试）；假服务首次 500 后成功 → 返回成功；内容包含敏感串时错误信息**不含**该串；`Health` 能把 `loading` 与 `ready` 区分开。
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

### Task 9: 行为指标聚合（东八区分桶 / 热力图 / 会话 / 回复时延） [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/internal/store/metrics.go`
- Create: `backend-go/internal/store/metrics_test.go`

**Interfaces:**
- Consumes: `Store`、messages 表
- Produces:
  - `type BehaviorMetrics struct{ Daily []DailyRow; Heatmap [168]HeatCell; Conversations []ConversationRow; Reply ReplyStats; Types map[int]int; LateNightRatio float64; AvgLen AvgLength }`
  - `func (s *Store) RebuildBehaviorMetrics(ctx context.Context, talker, modelVersion string) error` —— 单事务重建该 talker 的 `daily_stats`（行为列）/ `activity_heatmap` / `conversations`
  - `func (s *Store) BehaviorMetrics(ctx context.Context, talker string) (BehaviorMetrics, error)`
  - 回复时延：`ReplyStats{ MedianMs, P90Ms int64; FastRatio float64 }`（**同秒计 0**）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: CST-Bucket]`：所有日/时桶 SQL 必须写 `'unixepoch','+8 hours'`；测试须构造一条 UTC 23:30（= 东八区次日 07:30）的消息来钉住日界。
- [ ] `[Instinct: Rebuild-Not-Append]`：重建是**先删该 talker 分区再插**，避免重复导入造成指标翻倍。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 9: 行为指标聚合。
> Mode: AFK | Role: TDD Builder
> Goal: 从 messages 聚合出日汇总（行为列）、168 格热力、会话切分与回复时延，全部按东八区分桶。
> Target Files: Create `backend-go/internal/store/metrics.go`、Test `backend-go/internal/store/metrics_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：**日界钉死**——插入 UTC `2026-08-15T23:30Z` 的消息，断言其日桶为东八区 `2026-08-16`、小时桶为 `7`；插入 UTC `2026-08-15T16:00Z` → 日桶 `2026-08-16`、小时 `0`；`RebuildBehaviorMetrics` 连跑两次后行数不翻倍；热力图恒 168 格且 weekday∈0..6、hour∈0..23；同秒两条消息的回复时延记 0；5min 间隔切成两个会话。
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

### Task 10: 情感指标聚合 + 半成品门控 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Modify: `backend-go/internal/store/metrics.go`
- Create: `backend-go/internal/analyze/pipeline.go`
- Create: `backend-go/internal/analyze/pipeline_test.go`

**Interfaces:**
- Consumes: `nlp.Client.Score`、`Store.UpsertMessages`（读侧）、`Store.RebuildBehaviorMetrics`
- Produces:
  - `func RunSentiment(ctx context.Context, s *store.Store, c *nlp.Client, talker, modelVersion string, batchSize int) (Progress, error)` —— 待办集 = `messages WHERE talker=? AND id NOT IN (SELECT msg_id FROM sentiment WHERE model_version=?)`；**先在单事务内快照 msg_id 列表并 Close，再进入 HTTP 循环**（HTTP 期间不持连接）
  - `type Coverage struct{ Scored, Total int }` + `func (s *Store) SentimentCoverage(ctx, talker, modelVersion string) (Coverage, error)` —— 供报告做半成品门控
  - `func (s *Store) RebuildSentimentMetrics(ctx, talker, modelVersion string) error` —— 回填 `daily_stats` 情感列

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Conn-During-HTTP]`：快照与 HTTP 之间必须释放连接，否则唯一连接被长任务占满、所有读请求堵死（spec §SQLite 落地约定硬约束）。
- [ ] `[Instinct: Half-Baked-Gate]`：`Coverage.Scored < Coverage.Total` 时，报告层必须标注覆盖范围，**绝不用半满 sentiment 画看似完整的曲线**。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 10: 情感指标聚合 + 半成品门控。
> Mode: AFK | Role: TDD Builder
> Goal: 反连接待办集驱动块级打分，断点续跑免费；回填情感列；暴露覆盖率供门控。
> Target Files: Modify `backend-go/internal/store/metrics.go`；Create `backend-go/internal/analyze/pipeline.go`、Test `backend-go/internal/analyze/pipeline_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：用假 NLP 服务（`httptest`）跑一轮 → 全部消息被打分；**再跑一轮 → 零次 HTTP 调用**（续跑跳过已打分）；中途让假服务对第 2 块返回 500 → 函数返回 error，但第 1 块的结果已落库（检查点语义）；`RebuildSentimentMetrics` 后 `daily_stats.avg_polarity` 非空；`SentimentCoverage` 能正确报出 `Scored < Total`。
> 2. `cd backend-go && go test ./internal/analyze/...` 验证失败。
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
- Consumes: `Store.BehaviorMetrics`、`Store.SentimentCoverage`、`daily_stats` / `activity_heatmap` / `conversations` / `word_freq`
- Produces:
  - `type View struct{ Meta MetaView; Summary SummaryView; Heatmap []HeatCell; Words WordsView; Emotion EmotionView; Coverage CoverageView; Reply ReplyView; Disclaimer string }`
  - `func Collect(ctx context.Context, s *store.Store, talker, modelVersion string) (View, error)`
  - **契约**：`View` 的可序列化结果中**不得出现任何逐条消息**（只有计数/比率/聚合桶），测试用 AST + 反射双重断言

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Aggregate-Only]`：报告数据层禁止携带 `Content` 字段；这是隐私防线而非风格问题。
- [ ] `[Instinct: Honest-Labels]`：`EmotionView` 的正负比字段名必须是"模型眼中的正负消息比"语义（不叫 Gottman），并在 View 中携带免责文案常量。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 11: 报告数据契约。
> Mode: AFK | Role: TDD Builder
> Goal: 把库里的聚合结果收敛成一个只含聚合值的 View 结构，供模板直接渲染。
> Target Files: Create `backend-go/internal/report/{model.go, collect.go}`、Test `backend-go/internal/report/collect_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：`Collect` 在真实 fixture 数据上返回非零汇总；**反射遍历 `View` 的全部字段，断言不存在名为 `Content`/`Messages`/`Raw` 的字段**；`Disclaimer` 非空且包含"非关系诊断"字样；`CoverageView` 能表达 `Scored < Total`。
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
- [ ] `[Instinct: Supply-Chain]`：资产必须记录**确切版本号与 SHA256**，来源为官方发布渠道；不得使用 CDN 热链。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 12: 内联图表资产 vendor。
> Mode: AFK | Role: Builder（Tier 1 快通道）
> Goal: 把 echarts 与 echarts-wordcloud 的发行版落到仓库内，供 go:embed 内联。
> Target Files: Create `backend-go/internal/report/assets/` 下两个 `.min.js` + `README.md`。
> Steps:
> 1. 从官方发布渠道取得 ECharts 5.x 与 echarts-wordcloud 2.x 的 min 版。
> 2. 记录来源 URL、版本号、许可证（均 MIT/Apache-2.0）、`sha256sum` 到 assets/README.md。
> 3. 写一个临时 Go 程序断言 `go:embed` 能读到两个文件且大小 >0；跑完删除该临时文件。
> Return: 摘要 + 物理凭据（两个文件的 `sha256sum` 输出 + 字节数）。"

**Step Breakdown:**
- [ ] **Step 1: 取得两个资产文件**
- [ ] **Step 2: 记录来源/版本/许可证/SHA256 到 assets/README.md**
- [ ] **Step 3: Physical Evidence Gate**（`sha256sum` 原始输出 + 字节数）
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
  - 产物必须含：核心统计卡片、24×7 热力图、双方词云、情感基线（含"模型眼中的正负消息比"）、**固定免责面板**、覆盖范围标注（当 `Scored < Total`）

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: Zero-External-Ref]`：产物**不得含任何 `http://` / `https://` 外链资源**（`src=`/`href=`），测试用正则断言。
- [ ] `[Instinct: XSS-Escape]`：数据经 `template.JS` 注入时必须走 `json.Marshal` + 转义 `<`/`>`，不得直接拼接字符串。
- [ ] `[Instinct: Honest-Panel]`：免责面板是**常量**，不得由数据决定是否渲染。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 13: 自包含 report.html 模板与渲染。
> Mode: AFK | Role: TDD Builder
> Goal: 渲染出双击/离线可开、零外部请求的单文件报告。
> Target Files: Create `backend-go/internal/report/templates/report.html.tmpl`、`backend-go/internal/report/render.go`、Test `backend-go/internal/report/render_test.go`。
> TDD Steps:
> 1. 失败测试（RED）：渲染 fixture 数据 → 产物含 `<html`、含内联 `<script>` 数据块、含免责面板文案、含热力图容器 id；**正则断言产物中所有 `src="..."` / `href="..."` 均不以 `http` 开头**；产物中检索 `echarts` 字样出现；构造含 `<script>alert(1)</script>` 的边界数据 → 产物中该串被转义、不形成可执行标签。
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
- [ ] **Step 7: Git atomic commit** `feat(report): render self-contained single-file html report`

---

### Task 14: CLI 端到端编排 [Tier: 2-Standard] [Mode: AFK] [Role: TDD Builder]

**Files:**
- Create: `backend-go/cmd/analyze/main.go`
- Create: `backend-go/cmd/analyze/main_test.go`

**Interfaces:**
- Consumes: `store.Open/Migrate`、`ingest.Run`、`nlp.NewClient`、`analyze.RunSentiment`、`report.Collect/Render`
- Produces: CLI 接口
  - `analyze --export <path> --talker <id> --db <path> --out <report.html> --nlp-url <url> [--skip-sentiment]`
  - 退出码：0 成功 / 1 可预期失败（附 hint）/ 2 用法错误
  - 单行 JSON 摘要输出到 stdout（`{parsed, inserted, skipped, scored, coverage, out_path, elapsed_ms}`）；**日志只含 id/count/耗时**

**Injected Instincts (Compile-Time Rule Injection):**
- [ ] `[Instinct: No-Secret-Log]`：stdout 摘要与 stderr 日志都不得出现 `content`/`talker`/`wxid` 字面值（talker 只以哈希或计数出现）。
- [ ] `[Instinct: Fail-Fast]`：`--nlp-url` 不可达且未指定 `--skip-sentiment` → 启动即失败并给可执行 hint，不要跑到一半才炸。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Implement Task 14: CLI 端到端编排。
> Mode: AFK | Role: TDD Builder
> Goal: 一条命令跑通「导出 → 落库 → 打分 → 出报告」。
> Target Files: Create `backend-go/cmd/analyze/main.go`、Test `backend-go/cmd/analyze/main_test.go`（把编排抽成可测的 `run(args, stdout, stderr) int`）。
> TDD Steps:
> 1. 失败测试（RED）：缺 `--export` → 退出码 2；`--skip-sentiment` 时跳过 NLP 且报告仍能渲染（覆盖率为 0/Total）；不可达 `--nlp-url` 且未 skip → 退出码 1 且 stderr 含 hint；成功路径 stdout 是**单行合法 JSON** 且含 `out_path`；断言 stdout/stderr **不含** fixture 里的任意消息正文片段。
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

**验收标准（MUST ALL）:**
- [ ] `go test ./...` 全绿；`pytest fixtures/ -q` 与 `nlp-service` 测试全绿。
- [ ] 用 `workmem/evidence/full/wechat_messages.json`（32624 条真实消息）跑通 CLI。
- [ ] 产出 `report.html`：**双击可在浏览器打开**、**断网仍完整渲染**、**文件内零外部请求**。
- [ ] 报告含：核心统计卡片、24×7 热力图、双方词云、情感基线、**固定免责面板**。
- [ ] 隐私复查：`git status` 中无 `*.db` / `workmem/` / `*.report.html`；日志抽查无 content/talker 字面值。
- [ ] 记录报告文件体积与各阶段耗时到 `M1-ACCEPTANCE.md`。

**Subagent Prompt Scaffold (for /dfs-exec):**
> "Execute Task 15: M1 端到端验收。
> Mode: HITL | Role: Verifier
> **这是人机配合任务：涉及真实聊天数据与对外交付物，执行前必须由用户确认。**
> Goal: 用真实导出跑通端到端并产出可分享报告。
> Steps:
> 1. 用户确认后，启动 nlp-service（`uvicorn app.main:app --port 8000`），确认 `GET /health` 为 ready。
> 2. 运行 CLI，指定真实导出与输出路径到 `workmem/out/`。
> 3. 抓物理凭据：CLI 的 stdout JSON 摘要、报告文件字节数、`git status --short` 输出、`grep -cE 'https?://' report.html`（期望仅注释/无外链）的原始输出。
> 4. 用户手动双击打开报告确认渲染。
> Return: 摘要 + 全部原始凭据 + 未达成项（如有）的如实记录，**不假绿**。"

**Step Breakdown:**
- [ ] **Step 1: 用户确认执行窗口**
- [ ] **Step 2: 启动 nlp-service 并确认 ready**
- [ ] **Step 3: 跑 CLI 产出报告**
- [ ] **Step 4: 用户双击验证渲染 + 断网验证**
- [ ] **Step 5: Physical Evidence Gate**（全部原始输出）
- [ ] **Step 6: 隐私复查 + 结果写入 `M1-ACCEPTANCE.md`**
- [ ] **Step 7: Git atomic commit**（仅提交代码与文档；产物不入库）

---

## [*]️ Fog of War (Not Yet Specified)

- **[词云体积回落]**：Task 12 内联 `echarts-wordcloud` 后，若单文件超过可接受体积（阈值待实测），需改为 Go 侧渲染 SVG 词云。**阻塞于 Task 13 的实测产物体积**，届时才决定。
- **[停用词与情感词典的取得]**：`stopwords.txt` / `dutir_lexicon.txt` / `hownet_degree.txt` / `hownet_negation.txt` 的具体来源与规模尚未定。M1 允许先用小规模精选词表跑通闭环，**完整词库的覆盖率提升留到 M1 之后**；但词库来源的许可证必须在使用前核实（Task 5/6 执行时确认）。
- **[contacts 缺失的影响]**：M0 实测 `contact.db` 未能解密，`contacts` 表在本机将为空。M1 的报告是否需要显示对方昵称（而非 wxid）取决于此，**阻塞于联系人库能否解开**；M1 先以 wxid/占位呈现，不阻塞交付。
- **[多会话支持]**：M1 只跑单个 talker（CLI 一次一个）。批量/多会话的报告切换机制未定，属 M3 范围。
- **[真实数据的分析结论合理性]**：报告出图后，各项指标是否符合"描述性统计结论很明显"的预期，需要用户实际看过真实数据才能判断；若某指标在真实数据上无区分度，需回头调整口径。

---

## [x] Out of Scope

- **解密/密钥提取**：任何形式，永不进入本仓库（ADR-0001）。
- **Vue3 + ECharts 交互 Dashboard**：属 M3，M1 只出自包含静态报告。
- **SKEP / RoBERTa 模型情感**：属 M2；M1 只用词典基线。
- **`analysis_jobs` 状态机编排与 REST API / 前端轮询**：属 M2/M3；M1 迁移建表但只用块级批处理。
- **本地 Ollama 叙事、话题词云随时间变化（ThemeRiver）**：属 M4。
- **Docker / docker-compose 编排**：M1 用本地进程（`go run` + `uvicorn`）；compose 属 M3。
- **FTS5 全文检索、LSM、情绪惯性、情感滞后互相关、变点检测**：已在设计阶段砍掉，不复活。
- **`echarts.min.js` 之外的任何前端构建链**（Vite/npm）：M1 报告是纯 Go 模板渲染，不引入 node 依赖。