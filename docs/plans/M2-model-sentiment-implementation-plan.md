# M2 模型情感 + 任务状态机 实施计划

> **Goal**：把情感分析从「词典基线」升级到「本地模型」，并让两版结果**共存可比**；同时把块级批处理补成完整的 `analysis_jobs` 状态机（单飞 / 取消 / 断点续跑）。
> **Tech Stack**：Go 1.26 + SQLite(`modernc.org/sqlite` v1.58.0) ｜ Python 3.11 + FastAPI + **onnxruntime + tokenizers**（推理期**不引入 torch**）｜ hf-mirror.com 取模型
> **Spec Reference**：[spec §指标体系 M2](../specs/2026-09-08-wechat-relationship-analysis-design.md)（SKEP/RoBERTa 逐句极性 → 日/周情感曲线，滚动均值±SD，`model_version` 与词典版共存可对比）｜ [ADR-0002](../adr/ADR-0002-job-state-machine-chunked-batch.md)（任务状态机）｜ [ADR-0004](../adr/ADR-0004-sentiment-honesty-cloud-llm-deny.md)（情感诚实化）
> **前置**：M1 已验收闭环（`workmem/evidence/M1-ACCEPTANCE.md`）。本计划**只增不改** M1 的既有契约，唯一例外见 Task 4。
> **Branch**：沿用 trunk 例外——纯本地仓库、无远端，全部原子提交直接落 `main`。

## Global Constraints

- **合规铁律**：不内置、不分发、不协助开发任何解密或密钥提取代码（ADR-0001）。
- **隐私**：`content` / `talker` / `nickname` / `wxid` 永不入日志；模型权重与缓存**不入库**；评测集若取自真实聊天，必须留在 `workmem/`（已 ignore），**不得进 `fixtures/`**。
- **数据正确性**：东八区分桶不变；`model_version` 是分数的身份，**任何模型变更都必须换版本号**，不得在旧版本号下混入新分数。
- **长任务**：块级超时、短事务检查点、块级重试（最多 2 次），**永不任务级重试**；禁止零值 `http.Client`。
- **模型**：权重按 **sha256 钉版本**，下载后离线可跑；推理期依赖仅 `onnxruntime` + `tokenizers` + `numpy`。
- **诚实化**：新模型同样会误判反话/爱称；报告必须同时展示两版结果与口径说明，**不得**只展示更"好看"的那一版。

---

## [实测] 模型可得性与成色（本机 2026-10-20 测得，非推测）

### 网络与依赖

| 目标 | 结果 |
| --- | --- |
| `huggingface.co` 直连 | ❌ 超时（`000`） |
| `hf-mirror.com` | ✅ 200 |
| `modelscope.cn` | ✅ 302（可达） |
| 本机已装 | `onnxruntime` / `numpy` / `modelscope` |
| 本机未装 | `torch` / `transformers` / `sentencepiece` |
| 本轮新装 | `tokenizers` 0.23.3（纯 wheel，无需 torch） |

**结论**：推理走 **onnxruntime + tokenizers** 是可行且轻的；`transformers` 会拖来 torch（约 2.5GB），M2 不为它付这个代价。spec 点名的 **SKEP 是 Paddle 模型**，PaddlePaddle 在 Windows + Py3.11 上支持差且体积大，**判定不可行**，改用 RoBERTa 系中文模型——这与 spec「SKEP/chinese-RoBERTa」的二选一表述一致。

### 两个可用候选（均已 ONNX 化，均从 hf-mirror 下载成功）

| 候选 | 类数 | 体积(int8) | 吞吐 | 备注 |
| --- | --- | --- | --- | --- |
| `onnx-community/Erlangshen-Roberta-110M-Sentiment-ONNX` | **2**（Negative/Positive） | 103 MB | 11,196 msg/s（22,093 条 ≈ 170s） | 中文原生，IDEA-CCNL 110M 的 ONNX 转换 |
| `Xenova/distilbert-base-multilingual-cased-sentiments-student` | **3**（pos/neu/neg） | 136 MB | 18,812 msg/s（22,093 条 ≈ 101s） | 多语言蒸馏学生模型 |

### 14 句中文口语对照（跑出来的，不是印象）

| 句子 | 公认倾向 | Erlangshen | 多语言 distilbert | 词典基线 |
| --- | --- | --- | --- | --- |
| 今天特别开心 | 正 | Positive .995 ✅ | Positive .956 ✅ | +0.62 ✅ |
| 好难过 | 负 | Negative .945 ✅ | Negative .633 ✅ | −0.54 ✅ |
| 你真的很讨厌 | 负 | Negative .999 ✅ | Negative .952 ✅ | −0.55 ✅ |
| 终于见到你啦！ | 正 | Positive .995 ✅ | Positive .801 ✅ | 中性 ❌漏 |
| 不想理你 | 负 | Negative .999 ✅ | Negative .608 ✅ | 中性 ❌漏 |
| 太可爱了吧 | 正 | Positive .894 ✅ | Positive .934 ✅ | +0.62 ✅ |
| 别生气了 | 负向安抚 | Negative .981 ✅ | Negative .816 ✅ | **+0.31** ❌反向 |
| 笑死我了 | 正 | Positive .598 ✅ | **Negative .839** ❌反向 | +0.47 ✅ |
| 行吧 | 中性/敷衍 | Negative .545 ~ | **Positive .604** ❌ | 中性 ~ |
| 随便你吧 | 中性/敷衍 | Negative .999 ~ | **Positive .504** ❌ | 中性 ~ |
| 我没事 | 中性/负 | **Positive .860** ❌ | Neutral .625 ~ | 中性 ~ |
| 明天几点 | 中性 | **Positive .761** ❌ | Positive .357 ❌ | 中性 ✅ |
| 呵呵 | 负（反讽） | **Positive .858** ❌ | **Positive .672** ❌ | 中性 ~ |
| 我爱你 | 正 | **Negative .539** ❌ | Positive .935 ✅ | +0.50 ✅ |

**三条结论（M2 的设计前提）**：

1. **没有哪个模型能直接替代词典。** Erlangshen 10/14、distilbert 8/14、词典 9/14（词典大量弃权）。三者**错的不是同一类**：模型接得住隐含情绪（终于见到你啦 / 不想理你），却在反讽与爱称上**自信地反向**——恰好是 ADR-0004 点名的失效模式。
2. **二分类模型会强行给每一句定极性**（`明天几点` → Positive .76）。要么换三分类，要么用**概率中性带**（`|p−0.5| < τ` 判中性），后者必须先在评测集上调 τ。
3. **`呵呵` 两个模型都判正**，而词典判中性。这条单独说明：在情侣聊天域，「模型比词典强」不是自明的，**必须用评测集证明**。

> 因此 Task 0 是**门禁**：评测没过，后面的流水线不围绕某个模型开工。

---

## Decisions

- **D1（推理栈）**：onnxruntime + tokenizers，推理期零 torch。若某候选只有 PyTorch 权重，**转换是一次性开发动作**（可在别处转好、只把 ONNX 与 tokenizer.json 入库或放入模型缓存），不把 torch 带进运行期。
- **D2（模型获取）**：默认 endpoint `https://hf-mirror.com`，每个文件钉 sha256；首次运行下载到本地缓存，之后**离线可跑**。缓存目录进 `.gitignore`。
- **D3（版本共存）**：`model_version` 是分数身份。词典版 `lexicon-hownet-v1` 与模型版（如 `erlangshen-110m-v1`）在 `sentiment` / `daily_stats` 里各占一行，**互不覆盖**；报告可切换 / 并列。
- **D4（中性带）**：二分类候选必须配 `τ`，把 `|p−0.5| < τ` 记为中性，避免把问句判成情绪。τ 在 Task 0 的评测集上选定并写进 `model_version` 的元数据。
- **D5（`word_freq` 补维度）**：`word_freq` 目前**无 `model_version`**，而 `addTokenCounts` 无条件累加 —— 用新版本重扫会把词频再加一遍。M2 **必须先修**（Task 4），否则两版共存时词云会翻倍。
- **D6（不覆盖旧分数）**：切换模型不删除词典版分数；报告默认展示最新版，但**保留对照入口**。

---

## 任务

> 顺序即依赖链：评测 → 推理封装 → 取模型 → 契约 → 存储 → 编排 → 曲线 → 报告 → 验收。
> 每个任务的红-绿循环在提交前必须**变异验证**（把实现改坏，确认测试变红，再还原）。

### Task 0: 模型选型评测集与 bake-off（HITL 门禁） [Tier: 3-Critical] [Mode: HITL]

**Files**
- Create: `workmem/eval/chat-sentiment-eval.jsonl`（**不入库**：若含真实聊天，必须留在 ignore 内）
- Create: `workmem/eval/bakeoff.py`（**不入库**）
- 产出：`workmem/eval/BAKEOFF.md`（结论 + 混淆矩阵 + 每类失败样例）

**做了什么**
1. 组评测集：≥150 条中文口语聊天句，覆盖正/负/中性/反讽/爱称/问句/表情标记七类；**建议由用户从自己的导出中抽样标注**（HITL），或先用手写 + 公开评测集混合，明确标注来源。
2. 三个打分器跑同一评测集：词典基线、Erlangshen、多语言 distilbert；记录 accuracy / macro-F1 / **中性召回** / **反向错误率**（真值为正却判负，反之亦然）。
3. 对二分类候选扫 `τ ∈ [0.05, 0.25]`，选使 macro-F1 最高且反向错误率不升的 τ。
4. 写 `BAKEOFF.md`：**若模型版在 macro-F1 上不超过词典版 10 个百分点，M2 的情感目标降级为「并存对照」而非「升级」**，并如实写明。

**判据**：`BAKEOFF.md` 存在、含混淆矩阵与最终 τ；结论明确指向一个 `model_version` 名。

---

### Task 1: ONNX 推理封装（Python，模型无关） [Tier: 2] [Mode: AFK]

**Files**
- Create: `nlp-service/app/onnx_encoder.py`
- Create: `nlp-service/tests/test_onnx_encoder.py`

**Interfaces**
- Consumes: `onnxruntime.InferenceSession`、`tokenizers.Tokenizer`
- Produces:
  - `class Encoder: def __init__(self, model_path: str, tokenizer_path: str, max_length: int = 128, labels: list[str] = ...)`
  - `def predict(self, texts: list[str]) -> list[tuple[str, float]]` —— 返回 (label, confidence)，**不做中性带判定**
  - `def apply_neutral_band(probs: list[float], tau: float, positive_index: int) -> list[str]`

**约束**
- 构造时 `providers=["CPUExecutionProvider"]`，**不依赖 GPU**。
- 空文本返回 `("neutral", 0.0)` 且不发模型（与词典路径一致）。
- 批量输入的 padding 不得让显存/内存随 batch 线性膨胀：按 batch 内最大长度动态 padding。
- 张量名从 `session.get_inputs()` 读，**不硬编码** `token_type_ids`（不同导出可能没有）。

**TDD**
- [ ] 红：测试断言 `predict([]) == []`、空串返回中性、返回条数与输入一致、labels 顺序取自 config
- [ ] 绿：最小实现
- [ ] 变异：把 `apply_neutral_band` 的边界改成 `<=`，确认边界测试变红
- [ ] 提交 `feat(nlp): wrap onnx sentiment inference behind a model-agnostic encoder`

---

### Task 2: 模型获取、校验与离线缓存 [Tier: 2] [Mode: AFK]

**Files**
- Create: `nlp-service/app/model_store.py`
- Create: `nlp-service/tests/test_model_store.py`
- Modify: `nlp-service/requirements.txt`（`onnxruntime`、`tokenizers`；**不加 torch**）

**Interfaces**
- Produces: `def ensure_model(spec: ModelSpec) -> pathlib.Path`，`@dataclass ModelSpec(name, revision, files: dict[str, str])`（path → sha256）
- 缓存根：`%LOCALAPPDATA%\lover-instance\models\<name>\`（Windows）/ `$XDG_CACHE_HOME` 回退

**约束**
- endpoint 可配（默认 `https://hf-mirror.com`），**不读环境变量之外的东西**。
- 每个文件下载后校验 sha256，**不匹配即删除并报错**（不得留半个模型）。
- 已存在且校验通过 → **不发任何网络请求**（用测试钉住：注入一个会抛异常的 http 客户端）。
- 下载走 `urllib.request` + 超时；**禁止零值超时**。

**TDD**
- [ ] 红：用本地临时目录 + 假的 http 层，测试 ① 命中缓存零请求 ② sha 不匹配报错且清理 ③ 首次下载写全
- [ ] 绿：实现
- [ ] 变异：去掉 sha 校验，确认 ② 变红
- [ ] 提交 `feat(nlp): fetch and verify onnx models with a pinned hash`

---

### Task 3: `/score` 契约扩展到模型版 [Tier: 2] [Mode: AFK]

**Files**
- Modify: `nlp-service/app/score.py`、`nlp-service/app/health.py`
- Modify: `nlp-service/tests/test_score.py`

**契约变化**
- 请求体不变（`model_version` + `items`），但 `model_version` 决定走词典还是模型；**未知版本 400**，不得静默回落到词典。
- `model_version=""` 保持现状（用服务默认版本）。
- `/health` 的 `model_version` 报告**默认**版本；新增 `available_versions: list[str]`。
- 模型版响应里 `emotion` 仍要填：二分类模型无 DUTIR 七类 → **填 `neutral`/`positive`/`negative` 之外的既有标签集**？→ 决定：模型版把 `emotion` 留空串，Go 侧按 `polarity` 符号归类；**不得伪造 DUTIR 类别**（ADR-0004）。

**TDD**
- [ ] 红：未知版本 → 4xx；`/health` 列出可用版本；模型版响应 `emotion == ""`
- [ ] 绿：实现
- [ ] 变异：让未知版本回落词典，确认红
- [ ] 提交 `feat(nlp): score with a selected model version`

---

### Task 4: `word_freq` 增加 `model_version`（本次唯一破坏性迁移） [Tier: 3-Critical] [Mode: AFK]

**Files**
- Modify: `backend-go/internal/store/migrations.go`、`sentiment_rows.go`、`analyze/pipeline.go`
- Modify: `backend-go/internal/store/*_test.go`

**为什么必须做**：`addTokenCounts` 无条件累加，待办集却按 `model_version` 判定；M2 一旦出现第二个版本，重扫会把已计过的词频**再加一遍**。

**方案**
- 新表 `word_freq_v2(talker, period, is_self, model_version, word, cnt, PRIMARY KEY(talker, period, is_self, model_version, word))`；旧表**保留只读**，迁移时按默认版本复制一次。
- 读取接口 `WordFrequencies(ctx, talker, period, isSelf, modelVersion)`。
- 首次迁移需幂等：`INSERT ... SELECT ... WHERE NOT EXISTS`。

**TDD**
- [ ] 红：两个版本各写一次同一批消息 → 两行且各自 cnt 正确；同版本重复写 → cnt 不翻倍
- [ ] 绿：实现
- [ ] 变异：去掉 model_version 条件，确认「不翻倍」测试变红
- [ ] 提交 `feat(store): key word frequencies by model version`

---

### Task 5: `analysis_jobs` 状态机与单飞 [Tier: 3-Critical] [Mode: AFK]

**Files**
- Create: `backend-go/internal/jobs/jobs.go`、`jobs_test.go`
- Modify: `backend-go/cmd/analyze/main.go`

**Interfaces**
- `Start(ctx, s, talker, kind, modelVersion) (Job, error)` —— 同 talker+kind 已有活跃任务则**返回既有任务**（单飞）
- `Heartbeat(ctx, s, id, done, total)`、`Finish(ctx, s, id, status, errMsg)`、`Cancel(ctx, s, id)`
- `Resume(ctx, s, id)` —— 从 `done` 继续

**约束**
- 唯一索引 `uq_jobs_active` 已建；**单飞靠它兜底**，不靠进程内锁。
- 进程被 kill 后重启：遗留的 `running` 任务标记为 `interrupted`，可被 `Resume` 接管。
- 状态迁移只允许 `queued→running→done/failed/canceled`，非法迁移报错。

**TDD**
- [ ] 红：并发两次 `Start` 只得到一个 id；kill 模拟（先写 running 再新建 store）后 `Resume` 能接着跑；非法迁移报错
- [ ] 绿：实现
- [ ] 变异：把单飞查询改成无条件插入，确认并发测试变红
- [ ] 提交 `feat(jobs): persist the analysis job state machine`

---

### Task 6: 日/周情感曲线（滚动均值 ± SD） [Tier: 2] [Mode: AFK]

**Files**
- Modify: `backend-go/internal/store/sentiment_rows.go`（或新建 `trend.go`）
- Create: `backend-go/internal/store/trend_test.go`

**Interfaces**
- `Trend(ctx, s, talker, modelVersion) ([]TrendPoint, error)`；`TrendPoint{Day string, Mean float64, StdDev float64, Count int}`

**约束**
- 滚动窗口 7 天，**缺失日不插零**（会把均值拽向 0，形成假的"情绪低落"）；缺失日在图上断开。
- `Count < 3` 的点不画曲线（样本太少，均值没有意义）。

**TDD**
- [ ] 红：7 天窗口的均值/SD 手算对照；缺日不补零；样本不足的点被标出
- [ ] 绿：实现
- [ ] 变异：把缺日补零，确认测试变红
- [ ] 提交 `feat(store): compute rolling sentiment trend`

---

### Task 7: 报告并列两版情感 [Tier: 2] [Mode: AFK]

**Files**
- Modify: `backend-go/internal/report/{model,collect,render}.go`、`templates/report.html.tmpl`
- Modify: `backend-go/internal/report/*_test.go`

**约束**
- 报告新增「模型版 vs 词典版」对照区：同一批消息下两版的正负比例与不一致率。**不一致率必须显示**——它是"这个模型到底改了什么"的唯一诚实答案。
- 模型版无 DUTIR 七类时，雷达图**只画词典版**并说明原因，不得用模型极性伪造七类。
- 曲线若样本不足，显示「样本不足」而非空白图。

**TDD**
- [ ] 红：两版共存时对照区非空且不一致率 = 手算值；只有一版时对照区隐藏；宽度断言（沿用 M1 的 0–100% 守卫）
- [ ] 绿：实现
- [ ] 变异：把不一致率写死为 0，确认红
- [ ] 提交 `feat(report): show both sentiment versions side by side`

---

### Task 8: M2 验收 [Tier: 3-Critical] [Mode: HITL]

**判据**
- [ ] `go test ./...` / `pytest` 全绿；真实导出上两个 `model_version` 共存，词频**不翻倍**（Task 4 的守卫在真实数据上复跑）
- [ ] 中途 kill Go 进程后重启，`analysis_jobs` 续跑且**不重打分**
- [ ] 报告能切换两版并显示不一致率；曲线缺日处断开
- [ ] `BAKEOFF.md` 的结论与报告实际展示的一致（模型没赢就写没赢）
- [ ] 记录各阶段耗时到 `workmem/evidence/M2-ACCEPTANCE.md`

---

## Fog of War（未定，Task 0 毕业前不得开工下游）

- **最终选哪个模型**：取决于 Task 0 的评测结果。若两个候选都不如词典，M2 的交付物退化为「并存对照 + 曲线」，**这是可接受的诚实结果**。
- **评测集从哪来**：需要用户参与标注；若用户不参与，只能用手写 + 公开集，**结论强度必须相应下调**并在 `BAKEOFF.md` 里写明。
- **是否需要转换中文专用模型**（如 Erlangshen 330M）：需要 torch 做一次性转换，且转换后的 ONNX 质量未测。
- **`tokenizers` 是否要进 requirements**：取决于 Task 1 是否采用它（当前判定：要）。

## Out of Scope

- **SKEP / PaddlePaddle**：Windows + Py3.11 支持差，且本项目已判定走 RoBERTa + ONNX。
- **微调 / 训练**：M2 只用现成权重，不做领域微调。
- **云端 LLM**：ADR-0004，deny-by-default。
- **Vue3 Dashboard（M3）**、**Ollama 叙事（M4）**、**话题词云随时间（M4）**。
- **词云的符号/人名过滤**等 M1 遗留口径问题：见 `workmem/evidence/M1-ACCEPTANCE.md` §8.2，未纳入本计划。

## 依赖与风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| hf-mirror 失效 | 模型无法获取 | sha 钉版本 + 本地缓存；下载后离线可跑 |
| 模型在中文口语上不如词典 | 「升级」名不副实 | Task 0 门禁；退化方案已写明 |
| Task 4 迁移破坏已有库 | 词频翻倍或丢失 | 新表并存迁移，旧表只读保留；迁移测试钉住幂等 |
| 模型推理过慢 | 全量重扫耗时 | 实测 22,093 条 ≈ 170s；块级检查点使中断可续 |
