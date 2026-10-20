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

### 79 句起步评测集的结果（`workmem/eval/bakeoff.py`，作者手写标注）

> ⚠️ 这 79 句是**作者手写**的起步集，衡量的是「与作者判断的一致率」，不是真值。它的用途是给 Task 0 一个可复跑的脚手架与量级，**不能替代用户用自己导出标注的数据**。

| 打分器 | accuracy | macro-F1 | 反向错误（真正判负 / 真负判正） |
| --- | --- | --- | --- |
| **词典基线** | **0.734** | **0.730** | 3/79（3.8%） |
| Erlangshen τ=0.25 | 0.658 | 0.648 | 8/79（10.1%） |
| Erlangshen τ=0.40 | 0.671 | 0.676 | **1/79（1.3%）** |
| distilbert 3 类 | 0.608 | 0.568 | 8/79（10.1%） |

**分标签准确率（命中/总数）**：

| tag | 词典 | Erlangshen τ=.40 | distilbert |
| --- | --- | --- | --- |
| explicit 明确情绪 | 15/23 | **20/23** | **20/23** |
| affection 爱称/撒娇 | 10/12 | 4/12 | **12/12** |
| slang 网络口语 | 8/10 | **9/10** | **9/10** |
| logistics 事务 | **10/13** | **10/13** | 2/13 |
| question 问句 | **7/7** | 6/7 | 2/7 |
| sarcasm 反讽/敷衍 | **5/10** | 3/10 | 2/10 |
| mark 表情标记 | **3/4** | 1/4 | 1/4 |

**新增结论（比 14 句探针更硬）**：

- **结论 4**：词典在 accuracy 与 macro-F1 上仍然赢，但两个模型在 `explicit` 与 `slang` 上明显更强（20/23 vs 15/23）。
- **结论 5**：模型的短板集中在「没有情绪的中性消息」：`logistics` 2/13、`question` 2/7 —— 二/三分类模型都倾向于给事务性的话强行定极性。词典在这些类目上靠**弃权**取胜。
- **结论 6**：τ 是一笔交易，不是一个可以调对的参数：τ=0.40 把反向错误压到 1/79，代价是 `affection` 从 9/12 掉到 4/12——因为 `|p−0.5|<0.40` 意味着只有 p<0.10 或 p>0.90 才表态，多数情绪被中性带吞掉。**准确率的一部分是弃权买来的。**
- **结论 7**：反讽三者都差（词典 5/10、Erlangshen 3/10、distilbert 2/10），且词典之所以"更好"只是因为它更多弃权。这一项目前没有赢家。

**据此，M2 的默认结局判定为「并存对照」而非「升级」**，除非用户标注的更大评测集推翻它。混合策略（词典弃权处交给模型）是值得在 Task 0 里一并评估的方向，但**不在本计划承诺范围内**。

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

**当前状态（2026-10-20）**：
- ✅ 脚手架 `workmem/eval/bakeoff.py` 已可跑：一次跑三个打分器、扫 τ、输出 accuracy / macro-F1 / 反向错误 / 分标签准确率。
- ✅ 79 句起步集 `workmem/eval/eval_set.jsonl`（作者手写，pos/neg/neu 27/26/26），结果已回填到上文 [实测] 段。
- ✅ **174 句真实导出抽样集已生成**（`workmem/eval/eval_set_user.jsonl`，分层抽样 + 固定种子，覆盖 7 个 tag；slang 有 6 条不可补的缺口）。跑法 `python workmem/eval/bakeoff_user.py`，结论在 `workmem/eval/BAKEOFF.md`：**词典 acc 0.638 / macro-F1 0.624 仍胜过 Erlangshen-110M τ=0.45（0.603/0.594）与 distilbert（0.471/0.450）**。
- ⚠️ **这 174 条的标签是 AI 草拟（`label_source=agent-draft`），不是用户真值**。它把结论从"量级"推进到"同一读法下词典仍赢"，但**不足以宣称选型已定**。
- ⬜ **待用户**：抽查 ≥30 条并改标（或自标 ≥150 条）后重跑，才算 Task 0 毕业。
- ⬜ 待定：是否把混合策略（词典弃权处交给模型）纳入评估——新数据进一步支持它：distilbert 在 affection 27/30（词典 12/30）、Erlangshen 在 slang 16/24（词典 8/24），而两者都在 logistics/question 上输给词典。

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
- [x] 红：测试断言 `predict([]) == []`、空串返回中性、返回条数与输入一致、labels 顺序取自 config
- [x] 绿：最小实现
- [x] 变异：把 `apply_neutral_band` 的边界改成 `<=`，确认边界测试变红
- [x] 提交 `feat(nlp): wrap onnx sentiment inference behind a model-agnostic encoder`（`d2e3d45`，17 条测试，`pytest` 38→55）

#### Task 1 执行期偏离（2026-10-20 回填，以代码为准）

| 计划原文 | 实际实现 | 原因 |
| --- | --- | --- |
| `Encoder(model_path, tokenizer_path, max_length=128, labels=...)` | `Encoder(session, tokenizer, labels, max_length=128)`；从目录构造走类方法 `from_model_dir(directory, model_file)` | 注入式让测试用假 session/tokenizer，**不依赖 100MB 权重**也能测 padding、张量名、空文本路径。计划的签名同样可实现（monkeypatch 即可），所以这是**设计改进而非被迫偏离** |
| `apply_neutral_band(probs: list[float], tau, positive_index)` | `probs: list[list[float]]` | 计划原文自相矛盾：扁平的 `list[float]` 配 `positive_index` 无法定位正类列 |
| 未规定 padding 实现 | **不使用** `tokenizer.enable_padding`，在 numpy 里按**本批**最大长度 pad | `workmem/eval/bakeoff.py:61` 的 `enable_padding(length=...)` 是**持久状态**，照抄会跨批次泄漏宽度（已用变异钉住） |
| 未规定 y 轴 | `positive_index` 越界抛 `IndexError`（响亮），但**负下标会静默回绕**成"极性反转" | 已知弱点，无现存调用点；**Task 3 接线时必须显式校验 `0 <= positive_index < n`** |

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
- [x] 红：用本地临时目录 + 假的 http 层，测试 ① 命中缓存零请求 ② sha 不匹配报错且清理 ③ 首次下载写全
- [x] 绿：实现
- [x] 变异：去掉 sha 校验，确认 ② 变红
- [x] 提交 `feat(nlp): fetch and verify onnx models with a pinned hash`（`c1a4712`，15 条测试，`pytest` 55→70）

#### Task 2 执行期偏离（2026-10-20 回填，以代码为准）

| 计划原文 | 实际实现 | 原因 |
| --- | --- | --- |
| `requirements.txt` 只加 `onnxruntime`、`tokenizers` | 另加 `numpy` | `onnx_encoder.py` 顶层直接 `import numpy`，只靠传递依赖会和 onnxruntime 的版本策略绑死 |
| 「sha 不匹配即删除并报错（不得留半个模型）」 | 单文件场景完全满足；**多文件场景下，已校验通过的前序文件会留在目录里**（`ensure_model` 永不返回半成品，下次只重取缺失项） | 删除已校验的文件等于放弃可续传；命中判定逐文件独立，不会把半成品当就绪。**口径已按此修正** |
| 未规定 | 额外写 `SOURCES.json`（revision + 各文件 sha） | 只是出货记录，**从不参与命中判定**（命中只认 sha），有一条测试钉住「陈旧 manifest 不影响命中」 |
| 未规定 | `spec.files` 为空 → 返回空目录（fail-open） | 尚无消费方；Task 3 接线时应拒绝空 spec |
| `fetch` 注入点 | 签名外的辅助 `sha256_file()` / `read_sources()` | 测试不重复实现哈希逻辑 |

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
- [x] 红：未知版本 → 4xx；`/health` 列出可用版本；模型版响应 `emotion == ""`
- [x] 绿：实现
- [x] 变异：让未知版本回落词典，确认红
- [x] 提交 `feat(nlp): score with a selected model version`（`6bd673b` + 返工 `37dd191`，`pytest` 70→92）

#### Task 3 执行期偏离（2026-10-20 回填，以代码为准）

| 计划原文 | 实际实现 | 原因 |
| --- | --- | --- |
| Files 仅 `score.py` / `health.py` / 测试 | 新增 `app/model_registry.py`（版本白名单、sha256 钉版本、懒加载 + 模块锁）与其测试 | `main.py` 先 import health 再 import score，而 score 需要 health 的 `mark_lexicon_loaded`；把注册表放进任一方都会成环或污染职责 |
| 未规定 polarity 的取值形状 | 二分类带外取**获胜侧概率**（`+p` / `-(1-p)`），带内 `0.0`；三分类 argmax `+conf/-conf/0.0`；`intensity=|polarity|` | 让 `|polarity|` 与置信度同单调，也不让落败类贡献幅度（0.51/0.49 该显弱） |
| 未规定负类列如何定位 | 按模型自己的标签名匹配 `neg`，**不是** `1 - positive_index` | 三分类导出把 Neutral 放在中间，`1 - positive_index` 会把「敷衍」读成 −0.8 |
| 未规定 503 | 权重不可用 → 503 `model_unavailable`，与未知版本 400 严格区分 | 一个是「改版本名」，一个是「稍后重试」，混用会让用户改错东西 |
| 未规定 | `RETRYABLE_LOAD_ERRORS = (OSError, URLError, ModelIntegrityError)`；不变量违例（列数 <2、返回行数不符、三分类无 `neg` 列、空 spec）→ **500** + `logger.warning` 留痕 | 评审红牌：原实现 `except Exception` 把所有加载失败都伪装成可重试 503，而 Go 侧只读 `code` 不读 message、服务端零日志 → 配置错误永远查不到原因 |
| 未规定 | 边界判定**复用** `onnx_encoder.apply_neutral_band`，不再自己写一份 | 两份实现在 `p == 0.5 ± tau` 上本来已经分歧（Task 1 的变异测试钉的是「等值仍表态」），留两份就是给「符号翻转」事故留门 |
| 未规定 | `intensity=|polarity|` 与词典版 `intensity`（度数因子）**同名异义，跨版本不可比** | τ=0.45 让 erlangshen 的 intensity 实际只剩 ≈0.95–1.0 与 0 两档；Task 6 画曲线前必须先把这个口径写进报告说明 |

**已知局限（Task 8 验收前必须复核）**：`erlangshen-110m-v1` 的 τ=0.45 是在 **agent-draft** 标签的 174 句集上选的，不是用户真值；`distilbert-multilingual-v1` 无中性带，事务/问句会被判成有情绪（实测「明天几点」→ `+0.351`）。真实权重复测（2026-10-20，服务在 8000）：词典 `+0.618/-0.539/0/0`、erlangshen `+0.995/-0.959/0/0`、distilbert `+0.961/-0.663/+0.351/0`，未知版本 → 400。

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
- [x] 红：两个版本各写一次同一批消息 → 两行且各自 cnt 正确；同版本重复写 → cnt 不翻倍（**第②半句不可实现，见下方偏离 D4-1**）
- [x] 绿：实现
- [x] 变异：去掉 model_version 条件，确认「不翻倍」测试变红
- [x] 提交 `feat(store): key word frequencies by model version`（`e6993a8`）

#### Task 4 执行期偏离（2026-10-20 回填，以代码为准）

| 计划原文 | 实际实现 | 原因 |
| --- | --- | --- |
| 「同版本重复写 → cnt 不翻倍」作为 store 层断言 | **store 层不实现该断言**；守卫放在调用点：`TestRunSentiment_SecondRunDoesNoWork`（第二轮 0 请求、计数不变） | store 层没有「块身份」列，无法区分「同一块被重写」与「下一块正常累加」；`cnt = cnt + excluded.cnt` 是**有意保留**的累加契约（续跑靠它）。真正的去重靠 `PendingMessages` 反连接 |
| Files 只列 `migrations.go` / `sentiment_rows.go` / `analyze/pipeline.go` | 还改了 `report/collect.go` 与其测试；`pipeline.go` **未改** | `WordFrequencies` 签名变更必然波及报告层；`pipeline.go` 早已把 `modelVersion` 传进 `SaveScoredBlock` |
| 「迁移时按默认版本复制一次」 | 迁移**只在 `sentiment` 去重后恰好 1 个 `model_version` 时**复制；0 个或 ≥2 个都不复制 | 把词频归给一个没有产出它的版本，正是本任务要消灭的错误。代价是极端情况下 v2 为空 → 词云静默空白，**可见性提示留给 Task 7**（不得静默） |
| `Migrate` 每次裸 `Exec` | `Migrate` 改为**单事务**（建表 + 复制） | 升级中断不得留下「表说做完了、数据只搬了一半」的状态 |

真实数据回归（2026-10-20，真实库 32,624 条 / 22,093 条已打分）：迁移前 `word_freq` 17,869 行 / 88,049 计数 → 迁移后 `word_freq_v2` **同样 17,869 行 / 88,049 计数**、旧表原样未动；报告 `sha256 a1ae683d…` 与迁移前**逐字节相同**；二次运行后计数不变（幂等）。

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
