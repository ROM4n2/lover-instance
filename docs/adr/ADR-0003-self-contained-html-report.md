# ADR-0003：分享交付物为自包含 HTML 报告，Dashboard 用于自我探索

- 状态：Accepted
- 日期：2026-09-08

## 背景（Context）

产品目标含"自用 + 后期可分享"。但可交互 Dashboard 读的是本机 SQLite 与 localhost API，`docker-compose up` 起来的服务离开这台电脑即死——想把成果发给伴侣时，手里没有任何可发送的文件，只能截图或录屏。产品透镜评审指出：一次性情感回顾的情感高潮时刻是"生成一份年度回忆 → 发给 TA / 存档"，其交付物天然是**单个自包含 HTML 文件**（内联图表、手机可开、微信文件可发、多年后能打开），这也是参考项目 she-love-me / chatlog-keeper 已验证的形态。同时初版里程碑 M1–M3 无任何用户可见产出，第一份报告要等到 M4。

## 决策驱动

- 两种消费形态不同：Dashboard = 数据所有者反复探索（长任务进度、增量导入）；HTML 报告 = write-once/read-many 的分享与存档。
- 价值先行：让第一份可分享报告在最早里程碑落地，降低整体风险（取数失败也不阻塞）。
- stats 先行范式：`stats.json`（确定性、可复现）本就是最好的静态产物来源。

## 考虑的选项

- **A. 仅交互式 Dashboard**：无法分享给没有你微信数据库的人，否决作为唯一交付物。
- **B. Dashboard + 自包含 HTML 报告并存（采用）**：Go 用 `html/template` + `go:embed` 内联 echarts.min.js（~1MB）与数据 JSON，产出单文件 `report.html`；Dashboard 第一颗按钮即"导出报告 HTML"。
- **C. 导出 PDF**：交互与配色差、中文排版与图表分页麻烦，否决。

## 决策（Decision）

采用 B，并据"价值先行"重排里程碑：

- **M1** 即产出 report.html（数据管道 + 词典法情感，零模型下载、秒级），第一周就能生成可发伴侣的文件；Go API/任务编排、SKEP 模型、Vue Dashboard、Ollama 叙事在 M2–M4 外围增强（Go 简历线 M1 即在）。
- HTML 报告含：核心统计卡片、24×7 热力、双方词云、情感曲线、（M4）叙事章节、固定免责面板。
- Go 用 `go:embed` 同时托管 `frontend/dist`（Dashboard 单二进制双击即用）与报告模板。
- 报告产物 `*.report.html`、`reports/` 含隐私，已在 `.gitignore`。

## 后果（Consequences）

- 正面："可分享"真正落地；Time-to-First-Value 从 M4 提前到 M1；stats.json 与 HTML 同源、可复现。
- 代价：维护两套视图（Dashboard 交互组件 + 报告内联图表）——但报告只读、模板化，成本低。
