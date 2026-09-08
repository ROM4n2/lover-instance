# lover-instance

> 把你和 TA 的微信聊天记录，变成一份可回顾、可分享的关系分析报告。

本地优先（local-first）的双人微信聊天记录分析平台：自动获取本地微信聊天记录 → 结构化存储 → 描述性统计 / 词频 / 情感分析 → **可交互 Dashboard（自己探索）** 与 **自包含 HTML 报告（发给 TA / 存档）**。

> ⚠️ 使用前请阅读 [DISCLAIMER.md](DISCLAIMER.md)：本仓库**不含任何解密代码**，数据获取依赖你自装的第三方工具；分析结论为娱乐性统计，非关系诊断；分享报告前请先征得对方同意。

## 架构

```
浏览器(Vue3+ECharts) ──REST──▶ Go(Gin) 网关/编排 ──┬─ 适配器 ─▶ 宿主机 sidecar(用户自装, HTTP API / 导出 JSON)
                                  │                 ├─ SQLite(WAL): messages/sentiment/daily_stats/analysis_jobs...
                                  ├─ HTTP 批处理 ─▶ Python(FastAPI) NLP: jieba+DUTIR/HowNet · SKEP/RoBERTa
                                  └─ 叙事(OpenAI 兼容) ─▶ 本地 Ollama（云 API 默认 deny-by-default）
交付物：① 交互 Dashboard  ② 单文件 report.html（内联图表，可微信发送）
```

- **Go**：数据接入适配器、SQLite 存储、长任务编排（单飞/取消/检查点续跑）、REST API、HTML 报告渲染（go:embed）。
- **Python**：无状态 NLP 微服务（分词、词典情感基线、SKEP/RoBERTa 模型），不持库。
- **合规边界**：解密 sidecar 跑在宿主机；本仓库只做分析与可视化。

## 当前状态

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 取数 Spike + schema 证据（微信 4.1.9.30 被动取密钥实测、唯一键核实） | ⬜ 未开始 |
| M1 | 端到端最小闭环 + 首份自包含 report.html（词典法，零模型） | ⬜ |
| M2 | analysis_jobs 状态机 + 块级批处理 + SKEP 情感 | ⬜ |
| M3 | Vue3 Dashboard（go:embed 托管、健康徽章、进度轮询） | ⬜ |
| M4 | 本地 Ollama 叙事 + 话题词云时间线 | ⬜ |

设计与决策见 [docs/specs/](docs/specs/) 与 [docs/adr/](docs/adr/)。

## 快速开始（M0 后补充）

```bash
# 1. 宿主机安装并运行解密 sidecar（用户自行选择，仓库不提供）
# 2. 启动服务（sidecar 必须在宿主机，容器内取不到密钥）
docker-compose up            # go-api + nlp-service + ollama；SIDECAR_URL=http://host.docker.internal:5030
# 或本地分别运行：go run ./backend-go/cmd/server  ·  uvicorn nlp-service/app.main:app  ·  vite
```

> 数据库文件、导出 JSON、报告、模型权重均已在 `.gitignore`，不会入库；请勿把项目放在 OneDrive/坚果云等同步盘目录下。
