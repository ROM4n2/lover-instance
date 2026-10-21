# lover-instance · 微信聊天记录关系分析平台 — 设计规格 & 实施计划（v2，经 vault-grill 拷打修订）

## Context（为什么做这个）

自用（后期可**分享报告**）的双人微信聊天记录分析：自动获取本地微信聊天记录 → 结构化存储 → 描述性统计 + 词频 + 情感分析 → 产出**可交互 Dashboard（自己探索）** 与 **自包含 HTML 报告（发给伴侣/存档）**，主打情感回顾/关系分析。

简历诉求（正当、保留）：**Go 后端编排 + Python NLP 微服务 + agentic 叙事层**。"Agent" 采用 2026 主流范式：**确定性代码跑全量统计（stats，可复现）+ LLM 只做叙事解读**，不做自主决策。

### 已锁定决策（含 grill 后修订）

- 架构：Go(Gin) 网关/编排 + Python(FastAPI) 无状态 NLP + Vue3 Dashboard + 本地 Ollama 叙事。
- 情感引擎：本地 SKEP/chinese-RoBERTa 为主力（M2），DUTIR+HowNet 词典法为基线（M1，零下载秒级）。
- 微信版本：Windows 微信 **4.1.9.30**（M0 实测通过的版本；版本适用性由 sidecar 侧维护，本项目不记录断点）。
- 叙事 LLM：本地 Ollama（OpenAI 兼容接口）；**云 API 模式 deny-by-default**。
- **里程碑价值先行**：M1 就产出可分享的自包含 HTML 报告；Go/Vue/重型模型在外围增强（见里程碑）。
- **情感结论诚实化**：关系类指标全部标注"会误判反话/爱称，非心理诊断"，报告内置免责面板。
- **YAGNI**：砍 FTS5 全文检索（无搜索入口）；v2 论文指标只保留"话题词云随时间"，砍 LSM/情绪惯性/滞后互相关/变点；任务状态用 SQLite 表 + 轮询，**不上消息队列中间件、不上 WebSocket**。

### 🔴 合规铁律（最高硬约束）

腾讯主动发律师函下架解密仓库（chatlog 2025-10 删码、PyWxDump 删库、wechat-decrypt 2026-07 DMCA）。打击对象是**分发解密代码的仓库**，非读自己本地数据的个人（本地只读取本人数据，封号风险极低）。

- **本仓库绝不内置/不分发任何解密/密钥提取代码**；解密工具是用户自装的**宿主机 sidecar**，本项目只做分析+可视化，通过适配器消费其 HTTP API 或导出的 JSON。
- 仓库根 `DISCLAIMER.md`：仅处理本人合法拥有的数据、本地优先、第三方解密工具风险自负、**报告含对方聊天记录，分享前须征得对方同意**。
- 仓库可存"已知可用 sidecar 版本/哈希清单"（非解密代码），提示用户关闭微信自动更新、在 sidecar 可用时**先导出全量 JSON 快照存档**。
- **取钥过程与两个候选 sidecar 的密钥提取能力细节，已移至本地私有文档（不随本仓库分发）**：见 `docs/private/KEY-EXTRACTION-NOTES.md`（该目录在 `.gitignore` 内）。本 spec 只保留「本仓库不含也不分发解密/密钥提取代码」这条铁律与数据接入契约。

---

## 参考开源项目（复用而非重造）

| 用途                               | 项目                                                                         | 复用点                                     |
| ---------------------------------- | ---------------------------------------------------------------------------- | ------------------------------------------ |
| 数据接入（**主**，文件导出）       | `labazhou2024/chatlog-keeper`（MIT；Windows 11）                               | **M0 实测数据契约来源**：字段名、唯一键、类型码 |
| 数据接入（备选，常驻 HTTP API）    | `myysophia/wechat-log`（chatlog 续作，:5030 API，Webhook+MCP）                 | 仅留作 :5030 契约参考（本项目的目标版本下不可用，理由见私有文档） |
| 前端可视化                         | `sinyu1012/chatlog-web`（Vue3+Element Plus+ECharts，热力图）                 | 图表布局                                   |
| Agent 叙事范式                     | `863401402/she-love-me`                                                      | stats 先行、分层采样、评分回溯 stats、免责 |
| 自包含报告                         | chatlog-keeper / she-love-me 的单文件 HTML                                   | 内联图表、可分享形态                       |

---

## 架构总览

```
浏览器 ──REST──▶ Go 网关/编排 (Gin, 单二进制, go:embed 托管前端 dist)
                   │
                   ├─ 适配器层 ──▶ 宿主机 sidecar（用户自装；仓库不含其代码）
                   │     · KeeperExportAdapter 读 chatlog-keeper 导出 JSON（主，M0 实测契约）
                   │     · ChatlogHTTPAdapter  GET http://${SIDECAR_URL}:5030/api/v1/chatlog（备选）
                   │
                   ├─ SQLite（WAL, 单写者）：messages / sentiment / daily_stats /
                   │     activity_heatmap / word_freq / conversations / analysis_jobs / ingest_state
                   │
                   ├─ HTTP 批处理 ──▶ Python NLP 微服务 (FastAPI, 无状态, 不持库)
                   │     jieba + DUTIR/HowNet 词典基线 · SKEP/RoBERTa 模型主力
                   │
                   └─ 叙事 Provider（OpenAI 兼容）──▶ 本地 Ollama（默认）；云 API deny-by-default

交付物：① Vue Dashboard（自己反复探索）  ② 自包含 report.html（内联图表+数据，双击/手机可开、微信可发）
```

**关键边界**：sidecar 必须跑在**宿主机**（容器内读不到微信进程内存、取不到密钥）；compose 只编排 Go/Python/Ollama，`SIDECAR_URL` 默认 `http://host.docker.internal:5030`。Python **绝不打开 db 文件**，只通过 HTTP 收消息批次、回分数，由 Go 落库。

---

## 数据模型（grill 修订：代理主键 + 模型版本 + 任务表 + 东八区）

> ✅ **M0 已用真实 payload 核实完毕（2026-10-20，32624 条实测）**：
>
> `chatlog-keeper` 的导出**根本没有 `seq` 字段**，原"seq 是毫秒时间戳"的警告在本数据源上不成立。
>
> 实测唯一键为 **`server_id`（微信原生消息 ID，32624 条中仅 3 条为 `null`）+ `source_offset`（导出游标，100% 非空且唯一）**：`server_id` 优先、`source_offset` 兜底，二者组合在实测数据上得 32624 个互异键、0 冲突。字段名已冻结进 `internal/model` 的 `uniqueKeyExtraFields`。
>
> ⚠️ **禁止再引入任意标量兜底**：导出每条消息都带 `account_id` 与 `thread_id` 这类账号/会话级常量，任何"扫其余标量"的启发式都会命中它们，把多条消息塌成一个键、静默丢数据。
>
> 时间戳字段为 `ts`（JSON **浮点数**、epoch 秒），非字符串；适配器须显式转为整数字符串（`fmt.Sprint` 会产出 `1.792166715e+09` 而被 `NormalizeTs` 拒绝）。

```sql
-- 原始消息（代理主键 + 自然键去重）
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source TEXT NOT NULL,                  -- M0 实测: 'chatlog-keeper'
  source_msg_id TEXT,                    -- M0 实测冻结: server_id；为 null 的行用 source_offset 兜底，故实际恒非空
  ts INTEGER NOT NULL,                   -- ms epoch（微信源秒精度，ms 位恒 000）
  talker TEXT NOT NULL, sender TEXT NOT NULL, is_self INTEGER NOT NULL,
  is_chatroom INTEGER NOT NULL DEFAULT 0,
  msg_type INTEGER NOT NULL, sub_type INTEGER NOT NULL DEFAULT 0,
  content TEXT, media_ref TEXT,
  content_sha1 TEXT,                     -- Go 算，无 source_msg_id 时兜底去重
  raw_json TEXT                          -- 默认不存（隐私开关），仅排障时开
);
CREATE UNIQUE INDEX uq_msg_source  ON messages(source, source_msg_id) WHERE source_msg_id IS NOT NULL;
CREATE UNIQUE INDEX uq_msg_natural ON messages(talker, sender, ts, msg_type, content_sha1) WHERE source_msg_id IS NULL;
CREATE INDEX idx_msg_talker_ts ON messages(talker, ts, id);   -- keyset 分页 (talker,ts,id)

CREATE TABLE contacts(wxid TEXT PRIMARY KEY, nickname TEXT, remark TEXT, is_self INTEGER NOT NULL DEFAULT 0);

-- 情感结果（模型版本进主键，多版本共存，可对比/可回溯）
CREATE TABLE sentiment (
  msg_id INTEGER NOT NULL,
  model_version TEXT NOT NULL,           -- 'dutir-v1' / 'skep-ernie-2026-09'
  polarity REAL, emotion TEXT, intensity REAL,
  scored_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY(msg_id, model_version)
);

-- 日汇总（可丢弃缓存：批后事务内对受影响 talker 重建；启动 COUNT 对账自检）
CREATE TABLE daily_stats (
  talker TEXT NOT NULL, day TEXT NOT NULL, model_version TEXT NOT NULL,
  self_msgs INTEGER, partner_msgs INTEGER, self_chars INTEGER, partner_chars INTEGER,
  first_is_self INTEGER, last_is_self INTEGER,   -- M1 补：§指标体系 M1 要求"每日首/末条归属"，原 DDL 无落点
  avg_polarity REAL, pos_count INTEGER, neg_count INTEGER, updated_at TEXT NOT NULL,
  PRIMARY KEY(talker, day, model_version)        -- day 为东八区 YYYY-MM-DD；纯行为指标 model_version='behavior-v1'
);
-- 24×7 活跃热力（固定 168 格，东八区）
CREATE TABLE activity_heatmap (
  talker TEXT NOT NULL, weekday INTEGER NOT NULL, hour INTEGER NOT NULL,
  self_count INTEGER, partner_count INTEGER,
  PRIMARY KEY(talker, weekday, hour)
);
CREATE TABLE conversations (
  id INTEGER PRIMARY KEY AUTOINCREMENT, talker TEXT NOT NULL,
  start_ts INTEGER NOT NULL, end_ts INTEGER NOT NULL,
  initiator TEXT, msg_count INTEGER, UNIQUE(talker, start_ts)
);
-- 词频/话题词云（period='all' 或 'YYYY-MM'，支撑"话题词云随时间"）
CREATE TABLE word_freq (
  talker TEXT NOT NULL, period TEXT NOT NULL, is_self INTEGER NOT NULL,
  word TEXT NOT NULL, cnt INTEGER NOT NULL,
  PRIMARY KEY(talker, period, is_self, word)
);

-- 长任务状态机（持久化，进度从表读；重启不丢、可续跑、可取消、DB 层单飞）
CREATE TABLE analysis_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  talker TEXT NOT NULL, kind TEXT NOT NULL,        -- 'ingest'/'sentiment'/'metrics'/'narrative'
  model_version TEXT,
  status TEXT NOT NULL DEFAULT 'queued',           -- queued/running/done/failed/canceled/interrupted
  total INTEGER NOT NULL DEFAULT 0, done INTEGER NOT NULL DEFAULT 0,
  error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX uq_jobs_active ON analysis_jobs(talker, kind)
  WHERE status IN ('queued','running');            -- 同 talker+kind 只允许一个活跃任务（单飞兜底）

CREATE TABLE ingest_state (
  source TEXT NOT NULL, talker TEXT NOT NULL,
  last_ts INTEGER, last_msg_id TEXT, updated_at TEXT,
  PRIMARY KEY(source, talker)
);
```

**时区**：所有日/时桶固定东八区 `strftime('%Y-%m-%d', ts/1000,'unixepoch','+8 hours')`、`strftime('%H', ...,'+8 hours')`；UTC 分桶会让日界偏 8h、深夜占比全错。
**增量同步**：时间窗**回扫 60s**（`ts >= last_ts-60000`）+ 上面 UNIQUE 自然键去重 upsert，**不用 seq 水位**（避免边界秒整段跳过）。

### SQLite 落地约定（M1 验收标准）

- 迁移首条：`PRAGMA journal_mode=WAL; synchronous=NORMAL; busy_timeout(5000); foreign_keys=ON;`
- 纯 Go 驱动 `modernc.org/sqlite`（免 CGO；FTS5 已内置但本项目不用）；DSN 用函数式 pragma：`file:data.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)`；`db.SetMaxOpenConns(1)` 写串行化根除 SQLITE_BUSY。
- **HTTP 期间不持连接**：先在单事务快照取出待处理 msg_id 列表并 Close，再进入"HTTP → 短事务写回"循环；否则唯一连接被 30 分钟批处理占满，所有读请求堵死。

---

## 数据接入适配器（6 态状态机 + 版本双向核对）

统一接口 `ProbeVersion() / ListContacts() / FetchMessages(talker, since)`，返回 canonical schema。

- **sidecar 6 态**，每态对应可执行引导文案：进程不在 / 在但微信没跑 / 微信在但密钥取不到 / 微信版本不支持 / API schema 不兼容 / 正常。
- `ProbeVersion()` 不只信 sidecar 自报，**读宿主机微信 exe 文件版本双向核对**；适配器校验响应 schema/版本字段，不认识的版本显式报"仅验证过 sidecar X.Y.Z，当前 A.B.C"。
- **静默空洞硬规则**：contacts 非空但某 talker messages=0 判为**异常**，绝不允许 ingest 以"成功 0 行"收场。
- 双路径定位（grill："双传输≠双能力"）：HTTP API 与 JSON 导出共享同一解密脆弱点，微信一改版**同时失效**；故 KeeperJSON 定位为**冻结快照/存档**（sidecar 可用时先导出全量 JSON 防失效），不是运行时并列兜底。
- **M0 失败决策树**：被动扫描成功→HTTP API 主路径；失败→keeper 一次性 debugger（明确标注中-高封号风险、仅提取那一刻）；再失败→回退/固定微信版本或备用环境。并明示：**即使永远取不到真实数据，用合成语料 + keeper JSON 快照即可开发 M1 之后全部功能**，数据获取与开发解耦。

---

## 指标体系（描述性为主，关系类全部诚实化）

> 用户判断"描述性统计结论就很明显"成立——MVP 以确定性描述统计为主干。**Gottman 5:1 已在 grill 中改判**：其比值来自冲突互动视频的人工行为编码，套用到全天文本极性是方法误用；Losada 2.9:1 早已被证伪（Brown 2013），二者都不作严肃结论。

**M1（确定性 + 词典法，零模型）**

- 描述：双方消息总数/日均月趋势、平均句长、消息类型分布、**24×7 东八区热力图**、深夜(23点后)占比、emoji/表情频率
- 词频：jieba + 停用词 + 双方词云（服务端聚合 Top 200）
  > ⚠️ **口径修正（2026-10-20）**：原文「双方词云……**正/负各一朵**」自相矛盾——"双方"（自我/对方）与"正/负"（极性）是两种互斥的切分。M1 按**自我/对方**两朵实现；极性分桶词云列入 M1 的 Out of Scope，待口径澄清后再定。
- 会话：5–10min 间隔切分；发起方占比、每日首/末条归属、话轮平衡（消息数/字数比）
- 回复时延：中位数、P90、**秒回率**（≤1min）；报告注明秒级量化（同秒时延记 0）
- 情感（词典基线）：DUTIR 7 类情绪占比 + 情绪雷达；**"模型眼中的正负消息比"**（原名 Gottman，UI 强制标注"会误判反话/爱称，非关系诊断"）

**M2（模型增强）**：SKEP/RoBERTa 逐句极性 → 日/周情感曲线（滚动均值±SD，`model_version` 与词典版共存可对比）。

**M4（唯一保留的高级指标）**：**话题词云随时间变化**（月度 word_freq，ThemeRiver/堆叠面积）。

> 已砍：LSM、情绪惯性、情感滞后互相关、变点检测（用户看到数值无法行动、且吃域偏移误差）。

**可视化规范**：发散色带（红–灰–蓝绿 RdBu）；时间维度用线/面积不用饼图；双方固定双色；图表接口**只返回聚合结果，原始消息不下发图表层**。

---

## NLP 流水线与批处理契约（Python 微服务）

1. 预处理：过滤系统消息/红包/撤回/纯媒体（不喂模型）；微信自定义表情 `[捂脸][旺柴]` 用**自建映射表**（无公开词典，Novak 2015 emoji 数据补 Unicode emoji）；未知表情记中性并标记。
2. 分词 jieba + 自定义词典（昵称/爱称/网络梗）。
3. 词典基线：DUTIR 7 类情绪 + HowNet 程度副词(×0.5–2.0)/否定词窗口(前 3–5 词，×−0.5 比 ×−1 稳健)/转折规则。
   > ⚠️ **M1 实测修正（2026-10-20）**：原写「DUTIR 7 情绪 21 小类 + 1–9 强度，`cnsenti`」**不成立**。实测 `cnsenti` 0.0.7：`Emotion.emotion_count` 只返回 7 类**计数**，`Sentiment.sentiment_calculate` 只返回 pos/neg 分值，**没有任何 1–9 强度输出**；DUTIR 本体以 pkl 内嵌，既不外发 `dutir_lexicon.txt`，也不含强度维度。故 M1 **弃用 cnsenti**，改为自建 7 类情绪词表；`sentiment.intensity` 列保留但语义改为 **HowNet 程度因子归一化（0..1）**，不是 DUTIR 本体强度。
4. 模型主力：PaddleNLP **SKEP**（免训练出极性/方面/观点）为主；HF `chinese-roberta-wwm-ext`（weibo_senti_100k+ChnSentiCorp 微调）为备；权重走 **hf-mirror.com** 或本地挂载，模型接口抽象。CPU 批处理可跑。
5. **批处理契约四要素**（M2 定稿，事后返工成本最高）：
   - 批大小 **128–256 条/批**（单请求 body <5MB），块间循环；
   - **块级超时 5min**，`http.NewRequestWithContext(ctx,...)`（否则取消信号静默失效）；Go client 硬兜底 10min；
   - **每块一个独立短事务**写 sentiment/daily_stats ＝ 检查点；待办集 = `messages WHERE talker=? AND id NOT IN (SELECT msg_id FROM sentiment WHERE model_version=?)`（续跑免费，重跑跳过已打分）；
   - 重试**仅块级**（connect reset/5xx/超时，指数退避 + full jitter，上限 2 次），**任务级不重试**（对 60min 任务重试＝再叠一个 60min）；契约写明 Python 纯函数无副作用。
6. Python `GET /health` 返回 `{status: loading|ready, model_loaded: bool}`（模型冷启动 10–60s 与 ready 区分）。

---

## 任务编排与状态机（Go）

- 触发 `POST /api/analyze`（talker, kind）→ 入队 `analysis_jobs`；**活跃任务唯一索引冲突即返回已有 job id（409/复用）**，服务器是唯一权威，不靠前端 disable 按钮。
- worker 串行（全局信号量=1，防双跑 OOM）；每块之间检查 `ctx.Err()` 支持取消；进度写 `done/total`（绝对值"12,340/29,000"，不用百分比，总数随导入变）。
- `GET /api/jobs/:id` 轮询（2–5s；localhost 单用户，SSE/WS 是 YAGNI）；`DELETE /api/jobs/:id` 取消；页面加载 `GET /api/jobs?talker=` 恢复进行中任务（刷新不失忆）。
- **启动清扫**：残留 `running` 置 `interrupted`，可自动续跑（anti-join）。
- **半成品门控**：情感类图表 JOIN jobs 状态，未 done 显示"分析中 42%（12,340/29,000）"占位或只渲染已分析区间并标注覆盖范围——**绝不用半满 sentiment 画看似完整的曲线**。

---

## Agent 叙事层（本地 Ollama + 防幻觉 + 隐私 fail-closed）

1. 确定性先行：全量 `stats.json`（可复现）；LLM 仅叙事（关系报告、关键节点、"值得聊聊的时刻"），每个量化结论**必须引用 stats 字段**。
2. **分层采样**（不喂全量）：关系起源 / 高情感波动窗口 / 近 30 天。
3. 文案诚实化：原"军师建议/危险信号"改为**"值得聊聊的时刻/话题引子"**，叙事段强制免责头；报告含固定免责面板（"玩具级统计，非关系诊断；反话/爱称会被误判；报告含对方记录，分享前请征得同意"）。
4. Provider 抽象 OpenAI 兼容 → 默认本地 Ollama（qwen2.5/deepseek）。**云 API 模式 deny-by-default**：`provider=cloud` 时代码层面只允许发送**数值型 stats 字段**（计数/比率/聚合时间戳），自由文本采样硬编码 local-only（字段白名单，非"脱敏后允许"）；配置云 key 时启动大字警告数据出境。
5. Ollama preflight：未安装/模型未 pull 在健康检查中标注，叙事区禁用并提示 `ollama pull qwen2.5:7b`（防部分版本自动 pull 数 GB 挂死）；generate 超时 120s + max_tokens 上限。

---

## 可观测性、韧性与隐私

- **健康矩阵** `GET /api/health`：`{go, nlp: up|down|loading, sidecar: up|down|not_installed|...6态, ollama: up|down, models_pulled}`；Go 启动异步探 + 30s 刷新；前端顶栏徽章。**状态依赖矩阵**：浏览已分析数据只需 Go；导入需 sidecar；情感需 Python；叙事需 Ollama——Python 挂时确定性指标页照常渲染。
- **出站超时表**：sidecar connect 3s / read 30s；Python 块级 5min（硬兜底 10min）；Ollama connect 3s / generate 120s。零值 `http.Client{}`（永不超时）禁用。
- **日志红线**：只记 id/count/耗时；**content/talker/nickname/wxid 永不入日志**（uvicorn access log 屏蔽 query string，talker 改 POST body）；三跳透传 correlation ID；Docker json-log `max-size=10m,max-file=3`。
- **隐私**：SQLite 路径**避开 OneDrive/坚果云/百度同步盘**（Win11 Known Folder Move 会静默上传明文库），README 红字警告；`raw_json` 默认不存；叙事优先本地。compose 显式声明 volume：SQLite 文件、HF/Paddle 缓存、Ollama 数据目录（否则容器重建全丢）。

---

## 前端约定（Vue3 + Vite + Element Plus + ECharts）

- Go 用 **`go:embed` 托管 `frontend/dist`**，单二进制双击即用（消灭 dev/prod 端口与 CORS 困惑；Vite proxy 仅开发期）。
- 选中联系人 + 视图写入 URL query（`/?talker=xxx&view=sentiment`），刷新/书签/前进后退免费成立，且任务恢复据此知道 talker。
- 所有图表接口只返回**聚合结果**；词云服务端 Top 200；不做全量消息浏览/检索（FTS 已砍）。
- ECharts：`onUnmounted` 必须 `chart.dispose()`，resize 监听随组件摘除（防 tab 切换内存泄漏）。
- 统一错误体 `{code, message, hint}`（如 `NLP_DOWN, hint="情感服务未启动，请运行 docker-compose up nlp-service"`），按区域空态/错误态/重试，不白屏不弹技术原文。
- 健康徽章 + 任务进度轮询 + 半成品门控（见上）。

## 交付物：自包含 HTML 报告（分享形态）

- Go `html/template` + `go:embed` 内联图表库（echarts.min.js ~1MB）与数据 JSON，产出**单文件 report.html**：双击/手机可开、微信文件可发、三年后能开。
- 含：核心统计卡片、24×7 热力、双方词云、情感曲线、"值得聊聊的时刻"叙事章节（M4）、**固定免责面板**。
- Dashboard 第一颗按钮就是"导出报告 HTML"——live dashboard 负责探索，HTML 报告负责分享/存档。

---

## 项目结构

```
lover-instance/
├── DISCLAIMER.md  README.md  docker-compose.yml      # compose 仅编排 go/python/ollama；sidecar 宿主机
├── backend-go/
│   ├── cmd/server/main.go
│   ├── internal/{source,store,model,ingest,metrics,orchestrator,narrative,report,api}
│   │   - ✅ source/chatlogkeeper/export.go(M0 实测主适配器：导出 JSON → canonical)
│   │   - ✅ model/message.go(时间戳归一化 + uniqueKeyExtraFields 唯一键冻结)
│   │   - ✅ probe/probe.go(6 态状态机；跨盘符定位微信 exe)
│   │   - store/{migrations.go(含PRAGMA/schema),sqlite.go(MaxOpenConns=1)}
│   │   - orchestrator/{jobs.go(状态机/单飞/取消/续跑),nlp_client.go(块级超时/重试)}
│   │   - report/html.go(go:embed 模板+echarts)
│   │   - narrative/{provider.go(OpenAI兼容,cloud deny-by-default),sampling.go}
│   │   - api/{handlers.go,health.go,jobs.go}
│   └── go.mod (modernc.org/sqlite, gin)
├── nlp-service/  app/{main.py,tokenize.py,sentiment_lexicon.py,sentiment_model.py,health.py}
│                 data/(DUTIR/HowNet 词典, emoji 映射)  requirements.txt
├── frontend/     Vue3+Vite+ElementPlus+ECharts（dist 被 go:embed）
└── docs/specs/2026-09-08-wechat-relationship-analysis-design.md   # 批准后落盘
    docs/adr/ADR-0001..0004（合规边界 / 任务状态机与块级批处理 / HTML报告为分享交付物 / 情感诚实化与云LLM deny-by-default）
```

---

## 里程碑（价值先行）

- ✅ **M0 取数 Spike + schema 证据（已完成 2026-10-20）**
  - 取数环节的具体过程与 sidecar 能力边界见本地私有文档（不随本仓库分发）；本仓库只保留其**数据契约**结论。
  - **3 组证据落盘**（决定 schema，实测于 32624 条真实消息）：(a) 同秒 ≥2 条出现 **511 次**（soft gate 的同秒 ≥10 条未自然出现，最大 6 条）；(b) 撤回消息 **70 条**（类型 10000，正文含"撤回"）；(c) **双次导出逐条一致、零漂移**（webhook 路径不适用，改用双拉取 diff）。
  - 产出：**唯一键冻结为 `server_id` + `source_offset` 兜底**；端到端 32624 条全部经 `internal/source/chatlogkeeper` 解析得 32624 个互异键、0 冲突；合成语料 fixture 已按实测字段名与类型码回刷，可被适配器直接解析。
- **M1 端到端最小闭环 + 首份 report.html（≈1 周，即可发伴侣）**
  - Go：适配器 + SQLite(上述 schema/PRAGMA/MaxOpenConns=1) + 回扫增量去重 + 最小编排；Python：jieba+DUTIR+HowNet 词典法（零模型）；Go 渲染自包含 report.html（内联图表）。
  - 验收：导入一个双人会话 → 生成可双击打开、含热力图/词云/情感基线/免责面板的单文件 HTML。
- **M2 任务状态机硬化 + SKEP 情感（≈1 周）**
  - analysis_jobs 表 + 块级批处理四要素 + 检查点续跑 + 单飞/取消 + health；Python 上 SKEP/RoBERTa（model_version 共存）；Go REST API。
- **M3 Vue3 Dashboard（≈1 周）**：go:embed 托管、健康徽章、进度轮询、任务恢复、聚合图表、半成品门控、URL 状态。
- **M4 叙事 LLM + 话题词云时间线（≈1 周）**：本地 Ollama 报告章节（cloud deny-by-default）、月度话题词云、HTML 报告增强。

---

## Edge Cases & 韧性

- sidecar 6 态 / 版本不符 / schema 漂移 → 引导式错误 + 版本清单；contacts 非空但 0 消息判异常。
- 微信运行中 DB 锁 → sidecar 自行 copy；我方只读解密产物/API。微信自动更新跨断点 → 提示关闭更新 + 快照存档。
- 时间戳秒/毫秒/微秒/纳秒混用 → 归一化 ms；分桶固定东八区。
- 系统/红包/撤回/无转写语音 → 类型过滤不进模型；撤回触发 content 更新与 daily_stats 重建。
- 模型冷启动/无 GPU → /health loading 态、CPU 批处理、块级超时、OMP_NUM_THREADS 限线程防拖死整机。
- Python/Ollama/sidecar 任一挂 → 健康矩阵 + 区域降级，不白屏、不静默。
- 重复导入/重复分析 → 自然键 UNIQUE + jobs 单飞 + sentiment anti-join 幂等。
- 长任务中断 → 每块检查点 + 启动 interrupted + 自动续跑。
- 隐私：同步盘警告、raw_json 默认不存、日志不记内容、云 LLM 字段白名单 fail-closed。

## 测试策略

- **M0**：真实 payload 三证据（唯一键/撤回/路径 diff）。
- **Go 单测**：适配器 JSON→canonical；时间戳归一化；回扫增量去重（同秒多条不丢）；会话切分；时延/发起/平衡指标黄金 fixture；jobs 状态机（单飞冲突返回同 id、中断后续跑只处理缺失块、取消 ctx 生效）；东八区分桶。
- **Python 单测**：词典情感在人工标注小集的表现；SKEP 极性 sanity；否定/程度/转折规则。
- **静态断言自问测试**（CODING-UNIVERSAL §9）：合成"A 永远秒回/B 从不回复""A 全积极/B 全消极""同秒连发 50 条"语料，断言秒回率/正负比/发起占比/消息总数**必然翻转且不丢条**——破坏实现必变红。
- **Go↔Python 契约测试**：批大小/超时/纯函数无副作用/health 状态。
- **集成**：compose 起 go+python(+ollama)，sidecar 用 fixture JSON 适配器喂数，跑通 M1 报告生成；断言 health 各依赖 down 时的降级行为。

## 端到端验证

1. M0：版本探测 + sidecar 取数，拿到 ≥1 条真实消息与三证据。
2. 宿主机跑 sidecar；`docker-compose up`（go+python+ollama，SIDECAR_URL=host.docker.internal:5030）或本地 `go run`/`uvicorn`/`vite`。
3. 导入双人会话 → M1 立即生成 report.html，双击打开看到热力图/词云/情感基线/免责面板。
4. M2 触发情感分析，中途 kill Go 再重启 → 任务续跑、不重复打分、前端恢复进度。
5. 手动停 Python → 确定性指标页照常、情感区显示降级提示；停 Ollama → 叙事区禁用提示 pull。
6. `go test ./...` 与 `pytest` 全绿；合成极端语料断言指标方向与消息总数正确。

## 主要风险

- **取数路径不确定（最高）**：4.1.9.30 介于文档支持上限与硬断点之间 → M0 先行 + 决策树 + 快照存档 + 合成数据解耦开发。
- **合规**：仓库严格不含解密代码，适配器隔离，只存版本清单。
- **PaddlePaddle Windows 安装重 / HF 直连不通** → hf-mirror + HF transformers RoBERTa 可替换后端 + 缓存 volume。
- **SKEP 域偏移**（反话/爱称）→ 词典基线兜底 + 报告免责 + 结论诚实化命名 + 人工抽检 κ，不输出"关系诊断"。
