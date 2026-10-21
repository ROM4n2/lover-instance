# ADR-0001：合规边界——仓库不含解密代码，sidecar 适配器隔离

- 状态：Accepted
- 日期：2026-09-08

## 背景（Context）

微信不提供官方聊天记录导出 API，数据以加密 WCDB/SQLCipher 库存于本地。解密/取密钥类开源项目正被腾讯主动追责：`sjzar/chatlog` 作者 2025-10 收到微信官方律师函后删码、`xaoyaoo/PyWxDump` 作者主动删库、`ylytdeng/wechat-decrypt` 于 2026-07 遭 DMCA 屏蔽。打击对象是**分发解密/密钥提取代码的仓库**；而读取本人本地数据对个人账号的封号风险≈0（腾讯风控针对服务端异常行为，非本地只读）。

## 决策驱动

- 合规/法律是本赛道最大不确定性，且已反复爆发；项目需长期存活。
- 解密能力随微信版本频繁失效（断点由 sidecar 侧维护），是"移动靶"。
- 分层上，"数据获取"与"分析/可视化"本就可解耦。

## 考虑的选项

- **A. 内置解密/取密钥代码**：体验最自动化，但法律高危、仓库随时被下架，否决。
- **B. 适配器隔离外部 sidecar（采用）**：解密由用户自装、在宿主机运行的第三方工具完成；本项目只定义 `SourceAdapter` 接口，消费其 HTTP API（chatlog/wechat-log :5030）或导出的 JSON（chatlog-keeper）。仓库可存"已知可用 sidecar 版本/哈希清单"（非解密代码）。
- **C. 纯手动导出**：用户逐条/截图导出，体验不可接受，已被用户明确否决。

## 决策（Decision）

采用 B。仓库**绝不内置、不分发、不协助开发**任何解密或密钥提取代码；数据接入全部走适配器；`DISCLAIMER.md` 声明边界与第三方工具风险；提示用户关闭微信自动更新、在 sidecar 可用时先导出全量 JSON 快照存档。

## 后果（Consequences）

- 正面：法律安全位（与 she-love-me / chatlog-web 存活模式一致）；数据源可插拔，新增来源只加一个适配器；分析/可视化与解密失效解耦。
- 代价/缓解：依赖外部 sidecar（移动靶）→ 适配器 6 态状态机 + 版本双向核对 + JSON 快照存档；sidecar 必须宿主机运行（容器取不到密钥）→ compose 仅编排应用层，`SIDECAR_URL=http://host.docker.internal:5030`。

## M0 实测结论（2026-10-20）

背景中的三条合规事件经 GitHub API 复核，**两条需修正**：

- `sjzar/chatlog`：仓库**仍在**（9182★），但代码与提交历史已清空，根目录只剩一个 1332 字节 README。**未被删除**，与"删码"表述不符。
- `xaoyaoo/PyWxDump`、`LC044/WeChatMsg`：同样只剩 README，非删库。
- `ylytdeng/wechat-decrypt`：**确为硬屏蔽**，API 返回 `451 Repository access Blocked`（DMCA，2026-07-13）。
- 另有一项本 ADR 漏记：`0xlane/wechat-dump-rs` 亦已被 DMCA 屏蔽（2026-01-29）。

M0 目标机实测（版本适用性与取钥过程细节见本地私有文档，不随本仓库分发）：

- **被动取密钥可行且成功**。`chatlog-keeper` 以只读内存扫描（不注入、不 hook、不附加调试器）取得 32 字节数据库主密钥，`authority=database-master-key-proof` 表示密钥经数据库验证而非猜测，**未回落到高风险的一次性调试器方案**。这关闭了本 ADR"移动靶"风险中最高的一项。
- **sidecar 选型修正**：设计阶段选定的 `myysophia/wechat-log`（chatlog 直接续作、`:5030` + `/api/v1/session` 契约与原 probe 对齐）**不可用于本项目**——其 README 自述密钥提取上限为「Windows < 4.0.3.36」，低于目标版本一整个大版本，且停更 11 个月。改用 `labazhou2024/chatlog-keeper`（MIT、Windows 11、主动维护）。
- **数据契约以实测为准**：`chatlog-keeper` 使用**文件导出**而非常驻 HTTP 服务，因此下列设计阶段的假设需要按实测重写：
  - **不存在 `seq` 字段**。背景所述"chatlog seq 实为毫秒时间戳"的坑在本数据源上不成立。
  - 唯一键实测为 **`server_id`（微信原生消息 ID，32624 条中 3 条为 `null`）+ `source_offset`（导出游标，100% 非空且唯一）**，二者共同构成自然键，`server_id` 优先、`source_offset` 兜底。
  - 时间戳字段为 `ts`，JSON **浮点数**（epoch 秒），不是设计假设的字符串；适配器须显式转为整数字符串，否则 `fmt.Sprint` 会产出 `1.792166715e+09` 科学计数法并被 `NormalizeTs` 拒绝。
- **主动移除的启发式**：`DetectUniqueKey` 原有的"按字母序扫其余 Extra 标量"兜底被删除。实测显示导出**每条消息都带 `account_id` 与 `thread_id`**，字母序会先命中这类账号/会话级常量，使 `server_id` 为 null 的消息全部塌到同一个键上、静默丢数据。唯一键候选改为**显式冻结的有序列表**（`uniqueKeyExtraFields`），新增数据源只能靠实测追加，不得猜。

证据：32,624 条真实消息经 `internal/source/chatlogkeeper` 全量解析，得 32,624 个互不相同的唯一键、0 冲突；同秒 ≥2 条出现 511 次；双次导出逐条一致零漂移。
