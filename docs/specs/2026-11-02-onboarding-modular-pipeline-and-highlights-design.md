# Design Specification: Onboarding Experience, Modular Pipeline, and Highlights Milestone

**Date**: 2026-11-02  
**Status**: APPROVED  
**Author**: lover-instance team  
**Target Path**: `docs/specs/2026-11-02-onboarding-modular-pipeline-and-highlights-design.md`  

---

## 1. Problem Statement & User Value

### 1.1 Why Build This
1. **冷启动价值断层 (Cold-Start Gap)**：
   目前运行 `lover` 强依赖用户通过第三方 sidecar 导出的真实聊天记录 JSON。如果新手尚未完成解密与导出，首次启动只会遇到 `no export files found`，无法在 10 秒内体验产品的可视化与分析价值。
2. **分析管道硬编码耦合 (Monolithic Pipeline Coupling)**：
   现有的 `internal/analyze` 与 `internal/app` 将行为统计（`behavior`）、词频聚合（`word_freq`）、情感打分（`sentiment`）与叙事生成（`narrative`）硬编码在主循环中。新增任何分析维度均需侵入式修改 4-5 个核心文件，缺乏统一的插件/分析器（Analyzer）契约与拓扑注册机制。
3. **高情感价值信息被掩埋 (Hidden High-Value Milestones)**：
   情侣聊天记录中最具情感共鸣的信息（如初识启程日、最长连续热聊天数、单日消息量历史峰值、深夜畅聊夜、特殊纪念日互动活跃度）目前分散在底层基础统计中，没有被提取为醒目的「高光里程碑与纪念日时间线」。

### 1.2 Target Scenario & Persona
- **新手/非技术伴侣**：启动 Dashboard 或运行命令后，无需任何配置即可一键载入演示样本，直观感受报告与看板全貌；或直接通过浏览器拖拽聊天记录 JSON 完成导入。
- **深度记录者**：希望在年度/周期复盘中一眼看到彼此关系的标志性事件节点（峰值日、最长连续聊天打卡、深夜深聊夜）。
- **二次开发者**：通过极简的 Go `Analyzer` 接口与报告 Section 规范，低成本扩展后续模块（如作息同步率、回复延迟分布、表情包使用习惯），零侵入主干编排。

### 1.3 Cost of Doing Nothing
- 新用户在第 0 步因无就绪数据而流失，Dashboard 长期处于死板的只读空白态。
- 后续功能模块继续以硬编码形式叠加，导致 `app.go` 膨胀为千行巨石，破坏已建立的单飞状态机与检查点隔离。

---

## 2. User Journey & Core Flow

### 2.1 Happy Path 1: 零门槛演示试玩 (Zero-Friction Demo Experience)
1. **触发 (Trigger)**：用户在终端执行 `./lover demo`，或在浏览器打开未导入数据的 Dashboard (`http://127.0.0.1:8787`) 并在向导卡片点击「体验演示数据 (Load Demo)」。
2. **系统响应 (System Response)**：
   - 后端自动加载内置的合成语料（`fixtures/sample_chat.json`，995 条已验证测试语料，确定性脱敏，零隐私风险）；
   - 使用词典情感分析器在 1 秒内完成入库与分析；
   - 自动生成演示会话（`talker="wxid_sample_lover"`）。
3. **价值交付 (Outcome)**：CLI 自动弹出并生成演示报告；Web Dashboard 自动刷新并展示完整的图表、词云、情感走势与高光时间线。

### 2.2 Happy Path 2: Web 端直接拖拽导入 (Drag & Drop Onboarding)
1. **触发 (Trigger)**：用户将外部工具导出的 `wechat_messages.json` 拖入 Web Dashboard 空白引导区或顶部工具栏。
2. **系统响应 (System Response)**：
   - Web 客户端向 `POST /api/import` 发送文件；
   - 后端进行流式架构校验（限制 100MB，拒绝非法文件），写入本地存储；
   - 自动在后台申请单飞任务（`analysis_jobs`），Web 呈现进度环并在 2 秒轮询状态。
3. **价值交付 (Outcome)**：分析完成后页面自动平滑切换至正式会话视图，无需切回终端。

### 2.3 Happy Path 3: 高光里程碑与纪念日探索 (Highlights & Milestones Exploration)
1. **触发 (Trigger)**：用户进入 Dashboard 的「高光里程碑」选项卡，或打开 `report.html` 的「关系高光轴」版块。
2. **系统呈现 (Presentation)**：
   - **启程之日 (Journey Origin)**：首次对话日期、总相识天数、累计消息总数字。
   - **连击火花 (Longest Streak)**：历史最长连续互发消息天数（例如：连续 48 天每日有互动）及发生时间段。
   - **热聊之最 (Peak Conversation Days)**：单日消息量 Top 3 峰值日，附带当天双方消息占比与高频话题词。
   - **深夜畅聊夜 (Deep Night Talks)**：凌晨 00:00–05:00 消息占比极高的代表性夜晚，展现长谈记忆。
   - **特殊节点互动 (Special Occasions)**：跨年夜（12-31/01-01）、情人节/520 等特定节点的活跃指数。
3. **价值交付 (Outcome)**：以精致卡片与时间轴形式陈列，支持自包含离线导出，方便伴侣间截图分享与纪念。

---

## 3. Architecture & Data Models

### 3.1 模块化分析引擎架构 (`internal/analyze`)

定义统一的分析器接口契约，解耦编排器与具体算法：

```go
package analyze

import (
    "context"
    "lover-instance/backend-go/internal/store"
)

// Analyzer 是所有离线/批处理分析模块的统一接口契约。
type Analyzer interface {
    // ID 返回模块唯一机读标识符（如 "behavior", "word_freq", "sentiment", "highlights"）
    ID() string
    // Name 返回人类可读名称（如 "高光里程碑分析器"）
    Name() string
    // Dependencies 声明依赖的前置 Analyzer ID 列表（用于拓扑排序依序执行）
    Dependencies() []string
    // Run 执行分析计算，产物持久化写入 Store
    Run(ctx context.Context, s *store.Store, opts Options, onProgress func(done, total int)) error
}
```

#### 分析器拓扑与注册表 (`internal/analyze/registry.go`)
- 注册表通过 DAG 拓扑排序校验模块依赖，检测并拒绝循环依赖。
- 既有能力平稳封装为标配分析器（确保测试 100% 回归）：
  - `BehaviorAnalyzer`：日/月行为统计、首末发信人、时间热力。
  - `WordFreqAnalyzer`：分词词频、表情统计、月度词频。
  - `SentimentAnalyzer`：块级情感打分、情感走势、七类情绪分布。
  - `HighlightsAnalyzer`（新增）：高光事件挖掘、连续天数、峰值日。
  - `NarrativeAnalyzer`：Ollama 本地叙事摘要。

---

### 3.2 高光里程碑数据模型与持久化 (`internal/store/highlights.go`)

在 SQLite 中增加 `highlights` 表：

```sql
CREATE TABLE IF NOT EXISTS highlights (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    talker TEXT NOT NULL,
    kind TEXT NOT NULL,            -- 'origin', 'streak', 'peak_day', 'late_night', 'special_day'
    event_date TEXT NOT NULL,      -- YYYY-MM-DD 或 日期范围 YYYY-MM-DD~YYYY-MM-DD
    title TEXT NOT NULL,           -- 显示标题（如 "倾心长谈：单日 1,280 条"）
    description TEXT NOT NULL,     -- 详细说明
    metric_value REAL NOT NULL,    -- 核心数值（如条数、连续天数）
    extra_json TEXT,               -- 上下文数据（如前三热词、双方条数占比 {"self": 620, "partner": 660, "words": ["晚安","开心"]})
    created_at TEXT NOT NULL,
    UNIQUE(talker, kind, event_date)
);

CREATE INDEX IF NOT EXISTS idx_highlights_talker_kind ON highlights(talker, kind);
```

#### 报表展示 DTO (`internal/report/model.go`)

```go
type MilestoneView struct {
    Kind        string   `json:"kind"`
    Date        string   `json:"date"`
    Title       string   `json:"title"`
    Description string   `json:"description"`
    Metric      string   `json:"metric"`
    Keywords    []string `json:"keywords,omitempty"`
    SelfShare   float64  `json:"self_share,omitempty"`
}

type HighlightsView struct {
    Present       bool            `json:"present"`
    JourneyDays   int             `json:"journey_days"`
    StartDate     string          `json:"start_date"`
    MaxStreakDays int             `json:"max_streak_days"`
    StreakPeriod  string          `json:"streak_period"`
    PeakDays      []MilestoneView `json:"peak_days"`
    LateNights    []MilestoneView `json:"late_nights"`
    SpecialDays   []MilestoneView `json:"special_days"`
}
```

---

### 3.3 HTTP API 契约扩展 (`internal/api`)

1. **`POST /api/demo`**：
   - 语义：为当前数据库快速载入演示样本并执行分析。
   - 响应：`200 OK {"ok": true, "talker": "wxid_sample_lover", "messages": 995}`。
   - 互斥：已有 `analysis_jobs` 正在运行时返回 `409 Conflict`。
2. **`POST /api/import`**：
   - 语义：接收浏览器拖拽上传的 JSON 聊天记录文件（`multipart/form-data`）。
   - 安全控制：`http.MaxBytesReader` 限制请求上限为 100MB；临时文件写入 `%LOCALAPPDATA%\lover-instance\scratch\`，解析完成后立即删除。
   - 响应：`202 Accepted {"job_id": 12, "status": "running"}`。
3. **`GET /api/highlights?talker=...`**：
   - 语义：获取指定会话的高光里程碑结构化视图。
   - 响应：`HighlightsView` JSON。

---

## 4. Edge Cases & Resilience

### 4.1 边界情况与防御
1. **极端稀疏与单条数据防御**：
   - 若聊天记录仅有 1 天或少于 5 条消息：
     - `MaxStreakDays` 返回 1，不报错；
     - `PeakDays` 仅展示实际存在的 1 天，不填充虚假占位行；
     - 避免一切除以零（如计算双方占比时若分母为 0 则默认返回 0.5）。
2. **时区一致性硬性保证**：
   - 所有连续聊天天数（Streak）和深夜统计严格绑定 **东八区 (Asia/Shanghai)** 日界（`strftime(..., 'unixepoch', '+8 hours')`），严禁使用 UTC 导致跨夜判断偏移 8 小时。
3. **Demo 数据防污染隔离**：
   - 演示数据严格使用专属的 `talker="wxid_sample_lover"` 与 `source="sample-fixture"`；
   - 演示数据与用户真实导入数据在数据库行级互不干扰；用户在 Web 界面随时可通过下拉框切换会话。
4. **大文件上传与内存溢出防御**：
   - Gin 端配置流式解析，严禁将 100MB 整个读入内存字节数组；采用临时文件缓冲写入与分块校验。
5. **「Exit 0 ≠ 正确」假绿防守卫**：
   - 高光挖掘算法的计算结果必须包含单元测试钉住典型用例（跨闰年、跨月末、跨时段无消息），确保边界日期不丢失、连续天数不虚报。

---

## 5. Test Strategy

### 5.1 自动化测试覆盖
- **分析器注册与拓扑测试 (`backend-go/internal/analyze/registry_test.go`)**：
  - 测试正常线性依赖拓扑执行；
  - 测试循环依赖检测（Cycle Detection）并确认返回明确错误；
  - 测试未知依赖报错。
- **高光里程碑算法测试 (`backend-go/internal/analyze/highlights_test.go`)**：
  - 使用合成序列测试 Streak 算法：连续 10 天打卡 → 7；中间断 1 天 → 验证最大连续段与当前段；
  - 测试峰值日筛选与排序准确性；
  - 测试东八区深夜时段（00:00-05:00）判定准确性。
- **HTTP 接口测试 (`backend-go/internal/api/`)**：
  - `demo_test.go`：测试载入演示数据后的 HTTP 返回与数据库持久化。
  - `import_test.go`：测试合法 JSON 上传、超大文件（>100MB）拒绝、损坏 JSON 拒绝。
- **CLI 冒烟端到端测试 (`backend-go/cmd/lover/demo_test.go`)**：
  - 真实执行 `./lover demo`，验证产出文件大小 > 500KB，无网络请求发出，词典分析退出码为 0。

### 5.2 验证命令
```bash
# Go 后端全量测试（必须保持全绿且增加新增包覆盖）
cd backend-go && go test -v ./...

# Python NLP 服务与夹具测试（必须保持全绿）
PYTHONPATH=nlp-service python3 -m pytest nlp-service fixtures/

# 发布前隐私与机械闸门（绝无泄漏）
python3 scripts/release-audit.py
```

---

## 6. Fog of War & Scope Boundaries

### 6.1 Fog of War (未决点与后续演进)
1. **用户自定义纪念日输入**：
   本期高光算法仅依赖消息事实自动推导（初识日、峰值日、连续天数、法定/自然节日），暂不提供前端复杂的“自定义恋爱纪念日”表单输入，待核心管道稳定后再行演化。
2. **多语言与跨国时区自适应**：
   目前固定东八区，若伴侣双方处于异国跨时区，两地作息重合度定义待未来专项研究。

### 6.2 Out of Scope (明确排除的非目标)
- **微信解密代码内置**：本仓库绝不内置任何解密、Hook 或提取密钥逻辑（严格遵守 ADR-0001 铁律）。
- **云端存储与多租户用户体系**：纯本地单机部署，绑定 127.0.0.1，不做远程账户与云同步。
- **语音/图片/视频内容深度识别**：仅针对文本与表情符号进行统计与情感建模。

---

## Self-Review Checklist
- [x] 零 "TODO" 或 "TBD" 占位符。
- [x] 无含糊假设与发散式复杂度膨胀。
- [x] 严格遵循复用阶梯（Ladder of Reuse）：优先利用标准库与既有组件，最小化新抽象。
- [x] 满足 DFS-SPARK 5 节规范模板。
