# Lover Instance

<p align="center">
  <strong>把你和 TA 的微信聊天记录，变成一份有温度、可回顾、可分享的关系复盘报告。</strong>
</p>

<p align="center">
  <a href="https://github.com/ROM4n2/lover-instance/releases"><img src="https://img.shields.io/github/v/release/ROM4n2/lover-instance?style=flat-square&color=rose" alt="Release"></a>
  <img src="https://img.shields.io/badge/Privacy-100%25%20Local%20Only-blue?style=flat-square" alt="Privacy-First">
  <img src="https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-green?style=flat-square" alt="Platform">
  <img src="https://img.shields.io/badge/License-MIT-orange?style=flat-square" alt="License">
</p>

---

## 🌟 为什么做这个项目？

情侣或伴侣间动辄数万甚至数十万条微信聊天记录，沉淀着彼此最真实的相伴轨迹：初次相遇的怦然心动、连续打卡的默契习惯、彻夜长谈的星夜私语，以及彼此独有的高频热词。

但微信官方并不提供关系维度的深度分析，而市面上的许多在线工具往往需要把私密聊天记录上传到第三方云端服务器——这带来了不可忽视的隐私泄露风险。

**Lover Instance 坚持本地优先（Local-First）原则**：
- **100% 本地运行**：数据库（SQLite WAL）、分析引擎、图表渲染与可选的本地大模型全部在你的电脑本地运行，**绝对零云端上传、零联网外呼、零第三方存储**。
- **开箱零门槛**：提供 **10.6 MB 纯绿色免安装包**，无需安装 Node.js、npm 或 Python，解压双击即可在浏览器直接弹出精美可视化面板。
- **自包含与仪式感**：不仅提供自我探索的本地交互看板，还支持一键生成 **3:4 手机竖版朋友圈高清纪念海报（PNG）** 以及可直接微信发给伴侣的 **单文件离线报告（`report.html`）**。

> ⚠️ **合规与免责声明**：使用前请阅读 [DISCLAIMER.md](DISCLAIMER.md)。依据 [ADR-0001](docs/adr/ADR-0001-compliance-sidecar-adapter.md) 铁律，本仓库**绝不内置、绝不分发任何解密或密钥提取代码**；数据获取走宿主机外部工具导出；分析结论为娱乐与纪念性统计，非关系诊断；分享报告前请务必征得对方同意。

---

## ✨ 核心功能亮点

### 1. 📊 全景恋爱数据复盘看板 (Vue3 + ECharts)
- **作息与活跃度热力图**：统计周一至周日、0–23 点双方消息分布（严格对齐东八区日界），一眼看清彼此最常热聊的黄金时段；
- **专属双人词云对比**：提取双方各自最常说的独特话题词与表达偏好；
- **表情包与常用表情排行**：统计彼此最爱用的表情符号；
- **互动节奏与回复延迟**：统计双方发起对话的比例、当天开场/收尾人，以及以秒为分辨率的回复时延中位数与极速秒回率。

### 2. 🏆 关系高光里程碑与成就勋章系统
- **启程之日**：精确锁定首次相识并留下记录的日期，展示携手走过的总天数与互动总量；
- **心动连击 (Streak)**：自动识别历史最长连续互发消息天数区间，见证日日有回响的坚韧默契；
- **倾心长谈 Top 3 峰值日**：单日消息量历史峰值挖掘，附带双方消息占比；
- **深夜畅聊夜 Top 3**：凌晨 00:00–05:00 深夜对话峰值记录；
- **三大专属关系成就勋章**：
  - 🏆 **【心动长跑】**：连续连击突破 30/49 天，默契坚如磐石；
  - 🌙 **【夜猫长谈】**：单夜凌晨畅聊突破 200/500 句，星光作伴的专属深聊夜；
  - 💬 **【倾心狂飙】**：单日长谈突破 1000/1800 句，打字手速彻底拉满。

### 3. 📸 一键生成 3:4 手机朋友圈/小红书高清纪念海报
- **2K 视网膜级超清画质 (1080 × 1440)**：纯客户端离线绘制，高级午夜深蓝黑曜石渐变风格；
- **动态防遮挡排版**：日期胶囊与多行标题/描述自适应测量，彻底告别文字重叠与锯齿；
- **一键分享**：支持一键「保存图片 (PNG)」或「复制到剪贴板」，直接发给伴侣或小红书/朋友圈分享。

### 4. 📄 纯脱机自包含 HTML 报告 (`report.html`)
- 所有图表库、样式与数据全量内嵌（`ExternalRefs` 严格校验为 0 外链）；
- 生成单个 HTML 文件，断网即开，可以直接在微信聊天框里像文件一样发给 TA 留存纪念。

---

## 🚀 极简上手指南

### 小白极速路线（Windows 单文件免安装，首推）

1. **下载解压**：前往 [Releases 页面](https://github.com/ROM4n2/lover-instance/releases/latest) 下载 `lover-v0.7.0-windows-amd64.zip`（仅 10.6 MB），解压至任意文件夹。
2. **启动**：直接双击 `lover.exe`，浏览器会自动弹出交互面板（`http://127.0.0.1:8787`）。
3. **体验与导入**：
   - **秒级试玩**：点击网页中的「体验演示数据 (Load Demo)」，1 秒内体验内置 995 条合成语料、完整图表与高光时间轴；
   - **导入自己的记录**：使用本地离线导出工具（如开源的 `chatlog-keeper`）导出聊天记录 JSON，直接将文件拖拽进网页，或点击「自动扫描下载目录」，系统将在本地秒级完成加密解析入库！

---

### 进阶开发者路线（从源码构建与 CLI 操作）

> 前置要求：Go 1.26+（`backend-go`）、Node.js 20+（`frontend`）、Python 3.11+（`nlp-service`，可用 `LOVER_PYTHON` 显式指定解释器）。

```bash
# 1. 克隆仓库
git clone https://github.com/ROM4n2/lover-instance.git
cd lover-instance

# 2. 构建前端与单文件可执行程序
cd frontend && npm install && npm run build
cd ../backend-go && go build -tags embedded -ldflags="-s -w" -o ../lover.exe ./cmd/lover
cd ..

# 3. 运行体验
./lover demo                               # 零依赖内置语料，脱机秒级产出 demo_report.html
./lover dash                               # 启动 Web 交互面板并自动打开浏览器
./lover sync --export wechat_messages.json # 增量同步聊天记录，幂等去重入库
./lover doctor                             # 本机运行环境与依赖体检
```

---

## 🔒 数据与隐私边界（发布前请读这一节）

这个项目处理的是**两个人的私人聊天记录**，所以边界写在最前面：

- **仓库里没有任何聊天内容、密钥、数据库或模型权重。** 真实数据只落在你的本机 `%LOCALAPPDATA%\lover-instance\`；测试全部使用 `fixtures/` 里的**合成语料**。
- **仓库不内置、不分发任何解密或密钥提取代码**（见 [ADR-0001](docs/adr/ADR-0001-compliance-sidecar-adapter.md)）。数据由你自装的第三方工具导出为 JSON，本项目只消费该文件。
- **仓库不含**：本机知识库联动的多 Agent 入口文件（`AGENTS.md` / `CLAUDE.md` / `.cursorrules` / `.agent-rules.md`）、内部工作记忆账簿（`WORKMEMORY/`）、以及取钥过程细节（`docs/private/`）。
- **`report.html` 是分享物**：它含对方的聊天内容（词云、情绪、曲线、必要时的叙述段）。**分享前请先征得对方同意**；情感结论是描述性统计，不是关系判断。
- 完整声明见 [DISCLAIMER.md](DISCLAIMER.md) 与 [LICENSE](LICENSE)。

**发布前自检**（通过自动化脚本把上面的合规边界变成物理断言）：
```bash
python scripts/release-audit.py
```
该门禁会机械检查：索引与历史里是否有数据文件、本机目录是否被跟踪、是否有本机绝对路径、是否有疑似密钥、公开树里是否有取钥方法措辞，以及 LICENSE / DISCLAIMER / README 隐私小节是否齐备。

---

## 🗺️ 架构设计与里程碑记录

```
浏览器(Vue3+ECharts) ──REST──▶ Go(Gin) 网关/编排 ──┬─ 适配器 ─▶ 宿主机 sidecar(用户自装, 导出 JSON)
                                  │                 ├─ SQLite(WAL): messages/sentiment/daily_stats/highlights...
                                  ├─ 模块化分析引擎 ──┼─ Builtin: behavior / sentiment / highlights / word_freq
                                  └─ 离线渲染管道 ──▶ 单文件 report.html (go:embed 内联) + 3:4 分享海报
```

| 里程碑 | 内容说明 | 成果状态 |
|:---|:---|:---|
| **M0** | 取数 Spike + 数据契约证据 | ✅ 完成（`chatlog-keeper` 导出 JSON 契约冻结，自然唯一键 `server_id + source_offset`） |
| **M1** | 端到端最小闭环 + 首份自包含 `report.html` | ✅ 完成（词典情感基线，逐字节复现验证） |
| **M2** | `analysis_jobs` 状态机 + 块级批处理 + ONNX 模型情感 | ✅ 完成（多模型共存可比、断点接管、两版情感对照区） |
| **M3** | 本地 Web Dashboard (Vue3 + ECharts) | ✅ 完成（健康徽章、打分进度门控、单二进制 `go:embed` 托管） |
| **M4** | 本地大模型叙事 + 按月高频词演化 | ✅ 完成（本地 Ollama 回环隔离，纯指标摘要生成，拒绝原始消息泄露） |
| **M5** | 一键化命令行体系与零模型开箱 | ✅ 完成（`lover` / `sync` / `dash` / `doctor` 四条命令收敛，取消无孤儿） |
| **M6** | 新手友好沙箱 + 管道模块化 + 高光里程碑时间轴 | ✅ 完成（`lover demo` 毫秒出图、`Analyzer` DAG 拓扑排序、连续打卡与峰值长谈挖掘） |
| **M7** | 关系成就勋章 + 3:4 朋友圈高清纪念海报 | ✅ 完成（4 大成就勋章、Canvas 2K 高清视网膜海报生成、自动嗅探下载目录） |

设计决策详见 [docs/specs/](docs/specs/) 与 [docs/adr/](docs/adr/)。

---

## 📄 开源许可证

本项目基于 [MIT License](LICENSE) 开源发布。
