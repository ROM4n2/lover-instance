# M4 本地叙事 + 话题词云时间线 实施计划

> **Goal**：报告从「一堆图表」变成「有话说的一页」——用**本机 Ollama** 写一段关于这段时间的叙述，并把词云从"全期一张"扩展成"按月看变化"。
> **Spec Reference**：[spec §里程碑 M4](../specs/2026-09-08-wechat-relationship-analysis-design.md)、[ADR-0004](../adr/ADR-0004-sentiment-honesty-cloud-llm-deny.md)
> **前置**：M1 报告、M2 模型情感 + jobs、M3 Dashboard 均已验收。
> **Branch**：沿用 trunk 例外——纯本地仓库、无远端，原子提交直接落 `main`。

## 全局约束（不可协商）

- **叙事只看聚合，不看对话**。喂给 LLM 的只有：统计卡片、Top 词、情绪占比、月度趋势点、类型分布。**任何一条消息正文都不进 prompt**——这不是"尽量"，是设计前提：一旦 LLM 能看到正文，报告、日志、模型三层都可能把它带出去。
- **云 LLM deny-by-default**（ADR-0004）：只允许 **loopback** 上的 OpenAI 兼容端点（`127.0.0.1` / `localhost` / `::1`）。非回环地址**直接拒绝**，不提供"我知道了"之类的开关。
- **可复现**：叙事按 `(talker, 输入摘要)` 缓存到本地文件；同样的输入必须产出同样的字节。LLM 不是随机数生成器，报告也不能每次打开都不一样。
- **降级不致命**：Ollama 没起、模型没拉、超时——报告照常生成，那一段写「本次未生成叙事」并说明原因。**不得**让叙事失败拖垮报告。
- **零外部资源**：报告仍然自包含（`ExternalRefs` 必须为空）。

## Decisions

- **D1（provider 形状）**：`internal/narrative` 定义 `Provider` 接口（`Generate(ctx, prompt) (string, error)`），M4 只实现 `ollamaProvider`（`POST {base}/v1/chat/completions`，OpenAI 兼容）。接口的意义是测试可以注入一个**确定性**的假 provider，而不必启动 LLM。
- **D2（prompt 由结构体渲染，不由字符串拼接）**：`narrative.Input` 是一个**只有聚合字段**的结构体（见 Task 2），模板渲染成 prompt。这样"不得含正文"是**类型级**约束，不是靠人记得别塞。
- **D3（缓存键 = 输入摘要）**：`sha256(JSON(Input) + model + promptVersion)` → `<LOCALAPPDATA>/lover-instance/narrative/<digest>.md`。命中即用，不发请求。prompt 改一个字节就必须换 `promptVersion`，否则旧缓存会被当成新结论。
- **D4（月度词频在打分时累积）**：`word_freq_v2` 已有 `period` 维度（`AllPeriod="all"`）。打分时**同一块写两次**：`all` 与 `YYYY-MM`。历史数据只有 `all`，要月度视图必须重扫一次（`--rescore-words` 之类的显式动作，默认不删数据）。
- **D5（报告顺序）**：叙事段落放在免责面板之后、图表之前——先读"这是玩具级统计"，再读"这段时间像什么"。

## 任务

### Task 1: 月度词频累积与查询 [Tier: 2]
**Files**：Modify `backend-go/internal/store/{sentiment_rows.go,metrics.go}`、`analyze/pipeline.go`；Create `backend-go/internal/store/monthly_words.go` + 测试
- `addTokenCounts` 在写 `all` 之后，按每条消息的**东八区月份**再累加一次 `YYYY-MM`。
  - 问题：一个块里可能跨月，`counts` 是**块级聚合**的 map，混了月份。**必须**在块内按消息分月重新聚合 token，不能把整块的 counts 同时记到两个月。
- `MonthlyWordFrequencies(ctx, talker, modelVersion, isSelf, limit)` → `[]MonthWords{Month, Words []WordCount}`（Top N/月，N 默认 12）。
- 历史数据无月度行 → 返回空切片并在响应/报告里说明「需要重扫才有月度视图」，**不是**假装没有数据。
**TDD**：跨月块（一条 8-31、一条 9-01）→ 两个月各只拿到自己那月的词；同月块重复写不翻倍（沿用 M2 的守卫）；空库返回空切片。

### Task 2: `narrative` 包（聚合输入 + 渲染 prompt）[Tier: 2]
**Files**：Create `backend-go/internal/narrative/{narrative.go,input.go,prompt.go}` + 测试
```go
type Input struct {            // 只有聚合字段，没有 content/talker/sender
    Days, Msgs, SelfMsgs, PartnerMsgs int
    TopWordsSelf, TopWordsPartner []string   // 只放词，不放句子
    EmotionShares map[string]float64
    TrendPoints []struct{ Day string; Mean, StdDev float64; Count int }
    MonthlyTop map[string][]string
    RangeStart, RangeEnd string              // 日期，不含时间戳精度
}
func (i Input) Digest(model, promptVersion string) string
func RenderPrompt(i Input) string
```
- `Input` **不得**含 `Talker`/`Content`/`Sender`/`Wxid` 字段；用一条测试反射断言结构体字段集合（新增字段必须显式加白名单），把"不小心加了正文"变成编译后立刻失败。
**TDD**：字段白名单测试；`Digest` 对同一输入稳定、对任一字段变化敏感；`RenderPrompt` 输出里不含任何聊天文本（用带标记的假数据验证）。

### Task 3: Ollama provider + 缓存 [Tier: 3-Critical]
**Files**：Create `backend-go/internal/narrative/ollama.go`、`cache.go` + 测试
- `NewOllama(baseURL, model string, timeout time.Duration) (Provider, error)`：**非回环 baseURL 直接 error**（`net.ParseIP` / `localhost` 判断，含 `127.0.0.1`、`::1`）。**禁止零值超时**。
- 请求体 OpenAI 兼容；`stream:false`；失败/超时返回错误（由调用方降级）。
- 缓存：命中不发请求（注入一个「一调就炸」的 Provider 钉住）。
**TDD**：`https://api.openai.com` → 构造失败；`http://127.0.0.1:11434` → 成功；缓存命中零请求；超时非零且生效。

### Task 4: 报告接入（叙事段 + 月度话题图）[Tier: 2]
**Files**：Modify `internal/report/{model,collect,render}.go`、`templates/report.html.tmpl`、`cmd/analyze/main.go`
- 新 section：叙事（Markdown → 纯文本段落，**不做 HTML 注入**，`html/template` 转义即可）+ 月度 Top 词小图（每行一个月的横向条形或分面条形）。
- 叙事失败/未配置 → 段落在位但写明原因（「未生成叙事：Ollama 未运行」），**不留空洞**。
- `--narrative`（默认开？）与 `--ollama-url` / `--ollama-model` 参数；默认地址 `http://127.0.0.1:11434`，默认模型名由参数给出（不猜）。
**TDD**：无 Ollama 时报告仍生成且含降级说明；假 provider 时段落包含其输出；月度无数据时图区显示说明；`ExternalRefs` 仍为空。

### Task 5: M4 验收 [Tier: 3-Critical] [Mode: HITL]
- `go test ./...` / `pytest` 全绿；真实数据上跑一次带叙事的报告（本机 Ollama，模型名记录在验收文件里）。
- 同一输入跑两次 → 叙事段落**逐字节相同**（缓存生效）。
- 隐私：抓取发给 Ollama 的请求体（在 provider 里可注入记录器），断言其中**不含**任何消息原文与 talker。
- 非回环地址被拒；Ollama 关闭时报告仍生成。
- 记录在 `workmem/evidence/M4-ACCEPTANCE.md`。

## Out of Scope

- 云端 LLM（ADR-0004 deny-by-default）。
- 微调 / 提示词自动优化。
- 逐条消息的摘要（那需要把正文喂给模型，与全局约束冲突）。
- Dashboard 的叙事视图（M3 已交付图表探索；叙事先在报告里验证价值）。

## 依赖与风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| Ollama 未安装/未拉模型 | 叙事不可用 | 降级文案 + 验收时记录实际模型名；provider 用假实现单测 |
| LLM 输出不稳定 | 报告每次不同 | 按 digest 缓存；prompt 变更必须升 `promptVersion` |
| 月度词频需重扫 | 22,093 条 × 每模型约 5 分钟 | 显式 `--rescore-words`，默认不动历史数据 |
| prompt 意外带入正文 | 隐私事故 | `Input` 结构体字段白名单测试（类型级约束） |
| 生成慢 | 报告迟迟不出 | 超时（默认 120s）+ 失败降级；叙事在渲染前完成，不进图表路径 |
