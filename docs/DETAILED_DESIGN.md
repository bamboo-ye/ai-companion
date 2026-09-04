# 伴AI：需求规格与详细设计

> 文档状态：方案评审稿  
> 编制日期：2026-07-01  
> 需求来源：`/Users/windcry1/Desktop/伴A I.md`  
> 适用范围：Web、Android、iOS 与配套服务端

## 0. 已确认的实施基线

- 唯一开发目录：`/Users/windcry1/Documents/code/ai_companion`。
- 主业务后端使用 Go；允许文档、表格、金融和媒体算法使用隔离的 Python Worker。
- Android 使用 Kotlin + Jetpack Compose，iOS 使用 Swift + SwiftUI，不采用 Flutter。
- 提醒同时支持伴AI 应用内记录与系统提醒事项同步：iOS 使用 EventKit Reminders；Android 使用 Calendar Provider 的事件提醒与系统 Alarm 能力，并预留 Google Tasks OAuth 连接器。所有系统写入必须先获得用户授权和逐项确认。
- 首发地区、容量、数据保留期、模型供应商和成本上限尚未确定，必须通过配置、供应商适配和容量测试保持可调整。
- 暂无金融数据/研报授权。首版只分析用户合法上传的数据和用户提供的资料；外部公开资料必须遵守站点条款并保留来源，不接入未授权实时行情或付费研报。
- 1.0 必须包含真实邮件发送、团队协作、订阅计费、未成年人模式和管理员后台；这些能力按风险和依赖分阶段交付，不全部进入首个 MVP。

## 1. 产品定义

伴AI 是一个以自然语言对话为统一入口的个人 AI 工作台。产品由三个一级空间组成：

1. **情感陪伴**：创建不同人格的 AI 角色，以短消息、分气泡、表情包等方式进行拟人化对话。
2. **生活助手**：从对话、备忘录和提醒事项中提取账单、计划和提醒，在用户确认后执行。
3. **工作伙伴**：以可插拔 Skill 完成翻译、文档、演示文稿、邮件、数据分析、金融研究和艺术创作。

产品不是三个互不相关的应用，而是共享账户、文件、对话、记忆、任务、通知和工具权限的一套系统。首版应优先验证“角色对话是否自然、记忆是否有用、工具执行是否可信”，不追求一次覆盖所有创作能力。

## 2. 目标与非目标

### 2.1 产品目标

- 用户可在 3 分钟内创建角色并开始有明显人格差异的聊天。
- 聊天输出适合即时通信：首字延迟低、消息短、长回复自然拆分，支持重试和中断。
- 系统能记住经用户允许保存的偏好、人物关系和重要事件，并能说明记忆来源、支持查看和删除。
- 账单、计划、提醒、文件生成等操作均可预览、确认、撤销或追踪。
- 耗时任务在用户离线后继续执行，重新上线时可看到结果，不因进程重启而丢失。
- 工作技能使用统一执行协议，可新增 Skill 或连接 MCP 服务而无需修改聊天主流程。

### 2.2 首版非目标

- 不训练自有基础大模型。
- 不承诺完全自治地发送邮件、交易、付款或修改外部数据；高风险动作必须二次确认。
- 不在首版实现专业级时间线视频剪辑器；首版只支持模板化剪辑任务和结果交付。
- 不把金融分析包装成确定性投资建议，也不执行证券或期货交易。
- 不在 MVP 阶段拆成大量微服务；先以边界清晰的模块化单体交付。

## 3. 用户角色与典型场景

### 3.1 用户角色

- **普通用户**：聊天、创建角色、使用生活及工作技能、管理自己的记忆与文件。
- **运营管理员**：管理模型与技能配置、查看脱敏指标、处理失败任务和内容安全事件。
- **系统服务账户**：运行定时任务、通知、索引和异步分析，不具备普通用户交互权限。

### 3.2 核心用户旅程

1. 用户注册并创建“温柔但不说教、喜欢电影”的陪伴角色。
2. 用户聊天时说“今晚打车 36 元”，系统给出账单候选卡片；确认后入账。
3. 用户说“明早九点提醒我交报告”，系统解析时区与时间，确认后创建提醒；移动端设置本地通知，服务端保留兜底推送。
4. 用户上传 PDF 并提问，回答附文档名、页码和可定位片段。
5. 用户上传十年商品价格 Excel，系统完成清洗、指标、阶段划分、事件检索、图表与带来源的研究页面。

## 4. 范围与优先级

| 能力 | MVP | 第二阶段 | 后续阶段 |
|---|---|---|---|
| 账户、角色、会话、短消息流式回复 | 完整 | 优化 | 持续优化 |
| 表情包 | 生成、发送、收藏 | 风格模板 | 个性化素材训练 |
| 混合上下文与长期记忆 | 文本记忆、可管理 | 图像/文件记忆 | 跨设备主动回忆 |
| 智能记账 | 对话提取、确认、查询、Excel 导出 | 预算与统计 | 多账本/票据 OCR |
| 每日计划与提醒 | 创建、确认、应用内提醒 | iOS Reminders / Android 系统提醒同步、APNs/FCM | Google Tasks 与主动重排 |
| 翻译、邮件草稿、文档编辑 | 基础 Skill | 邮箱连接与确认发送 | 模板/批注/协作 |
| PPT | 大纲和文件生成 | 模板与图表增强 | 品牌设计系统 |
| 数据分析 | CSV/XLSX 基础分析 | 多表与可复现分析 | 大数据源 |
| 金融研究 | 上传数据的分析报告 | 外部数据源与定期更新 | 组合与情景监测 |
| Logo/修图 | 生成与编辑 | 素材库 | 品牌资产管理 |
| 视频 | 不进入 MVP | 模板化异步剪辑 | 专业时间线 |
| Web | 完整 | 完整 | 持续优化 |
| Android/iOS | 不进入首个 Web MVP | Kotlin/Compose + Swift/SwiftUI 核心链路 | 全技能覆盖 |
| 团队协作 | 不进入 MVP | 工作区、成员与共享文件 | 评论、协同任务 |
| 订阅计费 | 用量记录 | 套餐、配额、支付回调 | 发票与企业套餐 |
| 未成年人模式 | 安全策略基础 | 监护同意与功能限制 | 监护报告与地区化合规 |
| 管理员后台 | 最小运维页面 | 运营、审核、模型与 Skill 配置 | 成本和实验平台 |

## 5. 总体架构

```text
Web / Android Native / iOS Native
       |
 REST + WebSocket/SSE
       |
API Gateway / Go Application
  |-- Identity & Profile
  |-- Character & Conversation
  |-- Memory & RAG Orchestrator
  |-- Skill / Agent Runtime
  |-- Ledger / Plan / Reminder
  |-- File / Finance / Notification
  |
  +--> PostgreSQL 18        权威数据、任务租约、默认异步调度、Outbox、Agent 检查点
  +--> Redis                缓存、限流、在线状态、实时分发、短期回放
  +--> Kafka (可选)         水平扩展后的异步分发加速与事件扇出
  +--> Qdrant               稠密/稀疏混合向量检索
  +--> S3/MinIO             原始文件、解析产物、图片、报告
  +--> LLM/Embedding/Vision 可替换的模型供应商适配层
       |
Go Workers / Python Algorithm Workers
  |-- Chat generation
  |-- Document parsing & indexing
  |-- Finance analysis & report rendering
  |-- Media processing
  |-- Notification delivery
```

### 5.1 架构原则

- **模块化单体优先**：API 和常规 worker 使用同一 Go 仓库、不同启动命令；等调用量或组织边界明确后再拆服务。
- **存储后执行**：用户消息、任务和确认操作先持久化，再投递执行，避免离线或重启丢失。
- **控制与执行分离**：对话编排决定“做什么”，Skill 负责“怎么做”，MCP 仅作为外部能力协议。
- **模型可替换**：聊天、意图、摘要、Embedding、重排和视觉模型分别配置，不把供应商结构泄露到业务层。
- **所有副作用可审计**：发邮件、设提醒、记账、覆盖文件等操作都生成 tool execution 记录和幂等键。

### 5.2 推荐技术基线

- Go 1.26；HTTP 使用 `net/http`，数据库通过 `database/sql` + `pgx` 访问，迁移由仓库内版本化迁移器执行。
- PostgreSQL 18；Redis 使用当前受支持稳定版；初始部署不要求 Kafka，达到水平扩展门槛后使用当前稳定版 Kafka/KRaft。
- Web：Next.js + TypeScript + Tailwind CSS；客户端数据请求使用 TanStack Query。
- Android：Kotlin、Jetpack Compose、Coroutines/Flow、Room；iOS：Swift、SwiftUI、async/await、SwiftData/Core Data。双端共享 OpenAPI 契约、设计令牌和交互规范，但不共享 UI 代码。
- Python Worker：只承担依赖 Python 生态的文档、表格、金融和媒体算法，通过 Go 控制面和持久化任务契约交互；Kafka 仅作为可选调度加速层，Worker 不能直接绕过 Go 权限层对外产生副作用。
- 对象存储：开发环境 MinIO，生产环境兼容 S3 的托管对象存储。
- 检索：Qdrant 稠密向量 + 稀疏检索 + RRF，必要时增加 cross-encoder 重排。
- 可观测性：OpenTelemetry、Prometheus、Grafana、Loki/兼容日志系统、Tempo/兼容链路系统。
- 本地环境：Docker Compose；生产环境先容器化部署，达到规模门槛后再引入 Kubernetes。

版本应锁定到镜像摘要或 patch 版本，并由 Dependabot/Renovate 类流程统一升级，不在设计文档中追逐浮动 `latest`。

## 6. 服务端模块设计

### 6.1 Identity

- 注册、登录、刷新令牌、退出全部设备。
- Access Token 短时有效，Refresh Token 轮换并保存哈希。
- 设备、时区、语言、推送令牌和隐私偏好管理。
- 数据按 `user_id` 强制隔离；后台查询需独立管理员权限并记录审计。

### 6.2 Character

- 字段：名称、头像、关系设定、性格、语言风格、爱好、禁区、主动程度、回复长度、表情包风格。
- 将自由文本设定编译为结构化 persona；保存原始设定与版本化编译结果。
- 系统安全规则高于角色设定，防止角色提示词覆盖工具权限和系统策略。
- 允许导出、复制、停用、删除角色；删除前展示关联会话和记忆影响。

### 6.3 Conversation

- 一条用户消息对应一个生成任务；服务端先返回 `message_id/job_id`，再通过 WebSocket 或 SSE 推送状态与分片。
- 相同会话按 `conversation_id` 分区并串行处理，避免回复乱序。
- AI 长回复按语义边界拆为 2～5 个气泡；不按固定字数粗暴截断。每个气泡保留顺序、完整文本与发送时间。
- 支持停止生成、重新生成、编辑后重试、引用回复和失败恢复。
- 已完成回复进入 PostgreSQL；Redis 只承担在线分发和短期重放，不作为唯一消息存储。

### 6.4 Intent Router

采用“模型决策、确定性校验”的分层识别，避免模式匹配绕过 Agent：

1. **模型路由层**：基于当前角色、页面上下文和可用工具分类 `casual_chat / ledger / reminder / plan / document_qa / office / unknown`，输出结构化工具调用与置信度。
2. **领域校验层**：金额、时间、文件类型和必填槽位使用确定性解析器规范化与校验；解析器不能自行查询或修改业务库。
3. **澄清层**：低置信度、缺少必填槽位或上午/下午无法判断时，由模型生成针对性追问。
4. **策略层**：根据权限、风险、配额和可靠性级别决定直接回复、展示确认卡、调用工具或降级。

路由结果必须包含 `intent`、`confidence`、`required_slots`、`risk_level`、`suggested_skill` 和可追踪版本。

### 6.5 Agent / Skill Runtime

Agent 主运行时使用 LangGraph 显式有限状态图，不允许无限自治循环：

```text
Receive -> Classify -> BuildContext -> Plan(max steps)
       -> [Ask/Confirm | ExecuteTool | Generate]
       -> Validate -> Persist -> Deliver
       -> Retry/Degrade/DeadLetter
```

- 每次运行有最大步骤数、最大模型预算、截止时间和取消信号。
- 耗时 Skill 由 API 持久化为 `queued`，Worker 使用 `FOR UPDATE SKIP LOCKED` 原子认领并周期续租；租约过期允许接管，修订号作为围栏拒绝旧 Worker 的迟到提交。
- Tool 定义统一为 JSON Schema 输入/输出，声明风险等级、幂等性、超时、是否需确认。
- Skill 是“提示词 + 工具白名单 + 状态图 + 输出契约 + 测试集”的版本化包。
- MCP 用于接入外部工具、资源和提示词；内部核心业务仍调用类型安全接口，避免让协议成为领域模型。
- LangGraph 运行在独立 Python Agent Worker 中，使用 PostgreSQL checkpointer；Go API 继续掌管鉴权、配额和业务事实。
- Supervisor 根据角色模块选择 companion/life/work 子图。所有业务工具经 Go Tool Gateway 调用，Python Worker 不直写 `app` schema。
- Tool Gateway 只信任 `agent.runs` 中的用户、会话、角色、模块和原始消息；Python Worker 不得通过请求参数覆盖这些身份。Go 侧按当前模块的精确工具定义再次校验白名单。
- 写操作使用 Go 签发的短时 HMAC 确认令牌，令牌绑定 run、工具、候选和规范化变更；commit 使用领域幂等键，重放不得产生第二条业务记录。
- `agent_run_id` 是 LangGraph `thread_id`；数据库任务认领负责创建或恢复运行，可选 Kafka 只提供分发提示，不能用 `conversation_id` 复用检查点。
- 需要确认的账本、提醒、计划和文件写操作通过 `interrupt()` 暂停，收到 Go 控制面签发的确认结果后使用 `Command(resume=...)` 恢复。
- 所谓 harness/loop engineering 落实为：受限循环、检查点、执行日志、预算、策略验证、确定性重试和评测集，而不是开放式自我循环。
- Supervisor 从原始请求编译不可变任务契约，锁定制品类型、完整性意图、必需字段、输出语言和硬性完成条件；模型不能在后续轮次删减这些条件。
- 文件生成只形成“制品观察”，必须经过通用制品质量门才能完成。通用门检查文件存在、类型匹配、安全文件名和不覆盖源文件；PPT、邮件等领域制品在此基础上叠加专用校验器。失败时只在既定行动预算内修复，耗尽后停止交付不合格结果。

### 6.6 Context & Memory

上下文由四层组成：

1. **固定层**：系统安全规则、产品规则、当前角色 persona。
2. **近期层**：按 token 预算保留最近完整消息，不能从一条消息中间裁断。
3. **摘要层**：对被窗口淘汰的有效对话生成带时间范围的滚动摘要。
4. **长期层**：从长期记忆和 RAG 中检索与当前问题有关的条目。

长期记忆候选仅包含稳定偏好、关系、长期目标、承诺、重要经历和用户明确要求记住的信息。问候、重复确认、模型套话、低置信度推断和敏感信息默认不进入长期记忆。

推荐排序分数：

`score = 0.45 * semantic + 0.25 * recency_decay + 0.20 * importance + 0.10 * persona_relevance`

- `recency_decay = exp(-age_days / half_life)`，不同记忆类型使用不同半衰期。
- 写入前做相似去重、事实冲突检测和敏感字段分类。
- 冲突事实不直接覆盖，保存 `valid_from/valid_to/supersedes_id`。
- 用户可查看“AI 记住了什么”、来源会话、编辑、固定、删除和一键清空。
- 用户明确要求长期记住的信息，由聊天 Worker 通过模型工具选择并在服务端校验后写入；提醒、计划、账单和临时任务禁止写入长期记忆。当前不启用基于关键词的自动记忆提取，未来若增加推断式提取，必须独立评估、异步执行并提供可审计来源。

### 6.7 RAG 文档处理

#### 摄取流水线

`上传 -> 病毒/类型检查 -> 原文件入对象存储 -> 页级解析 -> OCR/视觉补充 -> 结构恢复 -> 语义切片 -> 向量化 -> 索引 -> 质量校验`

- 保存文档、页、块、表格、图片、标题层级和 bounding box；引用可定位到页与区域。
- 对扫描 PDF 使用 OCR；图表、示意图和复杂页面使用视觉模型生成结构化描述，但保留“机器生成”标记。
- 切片以标题/段落/表格为边界，目标 400～800 tokens、10%～15% 重叠；每个 chunk 前缀包含文档名、章节路径、页码和表格标题。
- 检索采用 dense + sparse 双路召回、元数据过滤、RRF 融合；高价值问答再重排。
- 回答必须携带 chunk、文档、页码和解析版本；证据不足时明确说不知道。
- 大文件按原始 chunk 顺序切成 token 有界轮次，每轮附带 chunk 范围、页码、清洗计数和续读游标。连续重复行与多余空行在轮内确定性清洗；多轮结果按 manifest 合并并用唯一 chunk 范围计算覆盖率，重复重试不能虚增完成度。
- 完整性任务在 `has_more=true` 时必须使用 `next_round` 续读；只有全部附件覆盖率达到 100% 才允许进入最终制品生成。单次调用最多合并四轮，避免把超大来源一次塞入模型上下文。
- Embedding 与分词器通过离线中文/多语言评测选择，不把某个模型名写死；升级时双写新索引并做 A/B 评测。

### 6.8 Ledger

- 从消息抽取金额、币种、收支方向、分类、商户、时间、备注和置信度。
- 明确且低风险时展示单击确认卡；缺少金额/方向/日期时追问。默认不静默记账。
- 确认后写 PostgreSQL 并生成审计记录；支持修改、撤销、按日/月/分类查询。
- Excel 为导出物，不是主数据库。导出包含原始记录、汇总和生成时间，文件存对象存储并提供短时签名链接。

### 6.9 Plan & Reminder

- 从对话、应用内备忘录和用户明确授权的系统提醒事项生成计划候选。默认只同步由伴AI 创建或用户主动导入的条目，不静默读取全部私人提醒。
- 计划项包括开始/结束时间、优先级、预计时长、地点、依赖和来源。
- 时间解析统一转换到用户时区并保存 UTC；“明早”等模糊表达在创建前回显绝对时间。
- Web 和移动端均提供应用内通知中心；移动端通过 APNs/FCM 提供离线送达兜底。
- iOS 使用 EventKit `EKReminder` 读取、创建、更新和完成系统提醒事项。iOS 17+ 访问提醒事项需要完整 Reminders 权限，因此只在用户启用同步时按需申请，并提供关闭同步和断开映射入口。
- Android 没有跨厂商统一的“提醒事项”任务库：优先使用 `CalendarContract` 创建带 reminder 的系统日历事件；本应用的到点提醒使用 `AlarmManager`/通知。仅在用户要求精确到点且符合平台政策时申请精确闹钟权限。
- Google Tasks 作为可选账户连接器，通过 OAuth 同步任务清单；它不等同于所有 Android 厂商的本地提醒应用。
- 本地记录与系统条目通过 `external_provider + external_id + revision` 映射。重复写入使用幂等键；双向变更以最新显式用户操作为准，发生冲突时保留双方版本并提示选择。
- 用户拒绝或撤回系统权限后，已有应用内提醒继续工作，但界面必须明确显示“未同步到系统”，不能声称系统提醒已创建。
- 延期、完成、跳过和重新排程均形成事件，为后续计划优化提供数据。

### 6.10 Office Skills

- **翻译**：保留格式、术语表、语言和语气，支持对照预览。
- **文档编辑**：基于副本生成，默认不覆盖源文件；提供变更摘要。
- **PPT**：明确受众、页数和风格后，可预览大纲或直接生成逐页内容和 `.pptx`，无需二次执行确认。表格型请求使用结构化列/行输入，按页均衡拆表并保留来源定位；质量报告校验字段、来源覆盖率、重复内容和投影可读性。
- **邮件**：MVP 先生成草稿；1.0 接入邮箱 OAuth 后支持真实发送。每次发送前必须二次确认收件人、抄送/密送、主题、正文和附件，保存供应商 message id 与审计记录，并提供撤销窗口（供应商支持时）。
- **数据分析**：识别表头与类型，展示清洗报告、公式/代码版本、图表和可下载结果。
- PPT、DOCX 和表格等耗时操作默认进入持久 Worker 队列；需要确认的写操作以及取消、重试继续使用用户级幂等键，Worker 重启不会丢失任务。

### 6.11 Finance Research

金融研究是独立的可复现流水线，而不是单轮提示词：

1. 上传或选择价格数据，识别日期、价格、币种、计量单位、频率和缺失值。
2. 标准化日期、排序、去重、处理异常点和单位换算；所有修改生成清洗报告，原始数据不覆盖。
3. 计算起始/最新/最高/最低价格、累计涨跌幅、年化波动、最大回撤、移动平均和分年度收益。
4. 使用变化点检测、趋势斜率、波动率和回撤规则生成候选阶段，再由分析模型给阶段命名，不让模型伪造数值。
5. 按时间窗口搜索权威事件与行业资料，保存标题、发布方、发布时间、URL、访问时间和引用片段。
6. 将“价格变化”和“事件”以时间邻近和机制证据关联，明确区分事实、来源观点和系统推断。
7. 生成标题、核心结论、趋势图、关键节点标注、阶段分析、供需/政策/产能/成本驱动、情景判断、风险和来源。
8. 输出 HTML 页面，并可导出 PDF/PPT；所有指标由确定性代码生成，文字结论引用指标 JSON。

预测只给情景、假设、区间和置信度，不给保证收益或个性化买卖指令。报告展示“仅供研究参考，不构成投资建议”、数据截止日期和已知缺口。

### 6.12 Media Skills

- 图片生成、Logo 和修图通过统一媒体任务接口调用供应商；保留原图、编辑指令、模型参数和派生关系。
- 上传内容先做安全、格式、大小和 EXIF 隐私处理。
- 模板化视频任务异步调用 FFmpeg/媒体 worker；使用断点、进度、取消和对象存储分片上传。
- 生成资产需记录许可与来源，不默认声称商标可注册或素材无侵权风险。

### 6.13 Team Workspace

- 用户默认拥有个人工作区；团队版包含工作区、成员、角色、邀请、共享文件、共享任务和配额。
- 角色至少包括 Owner、Admin、Member、Viewer；资源授权在 API 层强制校验，不能只依赖前端隐藏。
- 私人陪伴会话和个人长期记忆默认不可共享；共享必须逐项显式授权，并显示成员可见范围。
- 团队删除、成员移除和所有权转移进入审计日志，并提供合理的恢复/保留窗口。

### 6.14 Subscription & Billing

- 套餐定义可用模型、月度 token/任务/存储配额、并发数和团队席位数。
- 支付供应商通过适配层接入；Webhook 验签、幂等处理，账单状态以服务端事件为准。
- 模型用量先写 `model_usage`，再聚合计费；聊天主链路不能同步依赖支付供应商。
- 超额时先提示和限制新增高成本任务，不能删除既有数据或中断已付费且已接受的任务。

### 6.15 Minor Mode

- 注册时收集满足首发地区要求的年龄段，而不是无必要地收集完整生日。
- 未成年人模式采用更严格内容安全、默认关闭高风险外部连接/公开分享/金融建议，并限制可保存的敏感长期记忆。
- 若地区法规要求监护人同意，须实现可验证同意、撤销、数据访问和删除流程；在首发地区未确定前不得宣称已满足某一司法辖区全部要求。
- 情感陪伴不得诱导秘密关系、经济消费、脱离现实关系或持续在线。

### 6.16 Admin Console

- MVP 提供最小运维能力：任务/DLQ 检索与重放、用户封禁、模型/Skill 开关、脱敏指标和审计查询。
- 1.0 增加内容安全工单、套餐/配额、供应商路由、灰度发布和成本看板。
- 管理员使用独立身份域、MFA、最小权限和高风险操作二次确认；禁止后台直接查看完整私聊正文，除非存在合法授权和严格审计流程。

## 7. 数据库优先调度、Kafka 扩展与降级设计

### 7.1 默认数据库调度

事务内同时写业务任务状态和 Outbox。Worker 默认每秒扫描一次可运行任务，使用租约、`FOR UPDATE SKIP LOCKED`、幂等键和 revision fence 并发认领。数据库模式的 Outbox settlement 只表示执行责任已经由持久化业务状态接管，不表示任务已经完成；任务最终状态仍由各领域表记录。

通知、文档、Skill、对话、账本、邮件和 Agent Run 都必须有独立于 Kafka 的数据库认领或对账路径。这样单机和初始生产部署不需要 broker，也不会因关闭 Kafka 导致 Outbox 无限保持 `pending`。

### 7.2 可选 Kafka 水平扩展

设置 `KAFKA_ENABLED=true`、配置 `KAFKA_BROKERS` 并启用 Compose `kafka-scale` profile 后，Outbox Relay 和隔离 consumer group 成为低延迟分发加速层；数据库轮询降为 30 秒的 stranded-work 恢复扫描。Kafka 不拥有任务真相，broker 丢失、重复投递或 rebalance 都由数据库租约、Inbox 去重和对账恢复。

建议仅在以下指标持续出现时启用：数据库空轮询/认领压力超过预算、需要横向扩展大量 Worker 副本、一个事件需要多个独立消费者，或需要 broker 级保留与回放。启用前必须完成分区容量、ACL/TLS/SASL、lag 告警和故障演练。

可选 Kafka topic：

| Topic | Key | 用途 |
|---|---|---|
| `chat.command.v1` | conversation_id | 对话生成，保证会话内顺序 |
| `memory.extract.v1` | user_id | 已弃用兼容 Topic：仅排空历史事件，不再产生新的记忆提取命令 |
| `document.ingest.v1` | document_id | 文档解析和索引 |
| `document.cleanup.v1` | document_id | 删除文档后的向量索引与对象清理 |
| `skill.execute.v1` | job_id | Office/Finance/Media 异步任务 |
| `ledger.export.v1` | export_id | 月度账本 XLSX 生成与持久化 |
| `notification.deliver.v1` | user_id | 推送与站内投递 |
| `*.retry.v1` | original key | 指数退避重试 |
| `*.dlq.v1` | original key | 人工检查和补偿 |

Kafka 模式下 Relay 将 Outbox 投递到 broker；消费者用 `event_id` 写 Inbox 去重。整体按“至少一次 + 幂等”设计，不依赖跨系统的脆弱全局事务。

运维恢复面使用独立 `OPERATOR_TOKEN` 认证，不复用普通用户访问令牌。进入 `dead_letter` 的 Outbox 事件可被列出、查看详情和重放；重放只把事件恢复为 `pending`，由 Relay 重新投递。每次重放自动写入补偿记录，人工补发、回滚或外部修复也可登记为 `compensation_records`，用于发布复盘和审计。

### 7.3 动态降级

降级控制器每 10～30 秒综合消费 lag、最老消息年龄、worker 利用率、模型错误率和 p95 延迟，使用滞回和最短驻留时间防止级别抖动。

| 级别 | 触发示例 | 行为 |
|---|---|---|
| L0 正常 | 指标健康 | 完整记忆、RAG、重排和主模型 |
| L1 轻度 | lag/延迟连续升高 | 缩小 top-k、跳过非必要上下文增强、工具意图优先低延迟模型 |
| L2 重度 | 积压超过 SLO | 闲聊跳过 RAG/重排，切低延迟模型；耗时 Skill 转后台 |
| L3 保护 | 模型/算法服务不可用 | 保存请求并返回明确排队状态；只执行本地确定性能力 |

恢复按 L3→L2→L1→L0 逐级进行。任何级别都不能丢用户输入、伪造已执行结果或绕过高风险确认。

策略不仅用于展示：聊天、模型选择的显式记忆写入、长期记忆/RAG 上下文、文档问答和模型调用都会读取当前策略。L3 时聊天请求只持久化为 accepted job，不启动模型；Worker 收到命令也保持 durable job 等待恢复。模型供应商通过熔断器保护，熔断打开时已领取的生成任务会重新变为 accepted 并延后 `available_at`，而不是被错误标记为失败。

### 7.4 Redis 混合推送

- `presence:{user_id}` 保存带 TTL 的设备在线状态。
- 在线：worker 发布结果事件，网关经 Redis Pub/Sub 或 Stream 推送 WebSocket。
- 离线：事件权威副本在 PostgreSQL `app.notification_deliveries`；Redis Stream 保存有限时长的快速回放。
- 重连：客户端携带 `last_event_id`，先补拉 PostgreSQL/Stream 缺口，再切实时订阅。
- 客户端按 `event_id` 去重并发送 ack；过期未读事件可转 APNs/FCM。

## 8. 数据模型

核心表（均包含 `id`、创建/更新时间，重要表包含软删除和版本字段）：

- `users`, `user_devices`, `sessions`, `user_consents`
- `workspaces`, `workspace_members`, `workspace_invites`, `resource_grants`
- `characters`, `character_versions`
- `conversations`, `messages`, `message_bubbles`, `message_attachments`
- `memory_items`, `memory_sources`, `conversation_summaries`
- `documents`, `document_pages`, `document_chunks`, `files`
- `skills`, `skill_versions`, `tool_executions`, `agent_runs`, `agent_steps`
- `ledger_entries`, `ledger_exports`
- `plans`, `plan_items`, `reminders`, `reminder_deliveries`, `external_reminder_links`, `reminder_sync_cursors`
- `jobs`, `job_events`, `notification_deliveries`
- `finance_projects`, `price_series`, `price_points`, `research_sources`, `finance_reports`
- `outbox_events`, `inbox_events`, `compensation_records`, `audit_logs`, `model_usage`
- `plans_catalog`, `subscriptions`, `usage_quotas`, `billing_customers`, `billing_events`
- `minor_profiles`, `guardian_consents`, `admin_users`, `admin_roles`, `safety_cases`

Qdrant point payload至少包含：`tenant_id/user_id`、`source_type`、`source_id`、`document_id`、`page_no`、`section_path`、`language`、`created_at`、`embedding_version` 和权限标签。所有检索必须在服务端强制加租户与权限过滤。

## 9. API 与事件契约

### 9.1 REST 示例

- `POST /v1/auth/register|login|refresh|logout`
- `GET|POST /v1/characters`, `PATCH|DELETE /v1/characters/{id}`
- `GET|POST /v1/conversations`, `GET /v1/conversations/{id}/messages`
- `POST /v1/conversations/{id}/messages`, `POST /v1/messages/{id}/cancel|retry`
- `GET|PATCH|DELETE /v1/memories`, `POST /v1/memories/{id}/pin`
- `POST /v1/files`, `POST /v1/documents`, `GET /v1/documents/{id}/status`
- `GET|POST|PATCH /v1/ledger/entries`, `POST /v1/ledger/exports`
- `GET|POST|PATCH /v1/plans`, `POST /v1/reminders`
- `GET|POST|DELETE /v1/reminder-connections`, `POST /v1/reminders/sync`, `POST /v1/reminders/{id}/resolve-conflict`
- `GET /v1/skills`, `POST /v1/skills/{name}/runs`, `GET /v1/jobs/{id}`
- `POST /v1/finance/projects`, `POST /v1/finance/projects/{id}/analyze`
- `GET /v1/events?after={event_id}`
- `GET|POST /v1/workspaces`, `POST /v1/workspaces/{id}/invites`
- `GET /v1/billing/plans`, `POST /v1/billing/checkout`, `POST /v1/webhooks/billing/{provider}`
- `POST /v1/email/connections`, `POST /v1/email/drafts/{id}/send`
- `/v1/admin/*` 使用独立管理员认证与授权策略

所有创建接口接受 `Idempotency-Key`。错误返回稳定业务码、可读消息、`trace_id` 和是否可重试；不得向客户端泄露供应商密钥或内部提示词。

### 9.2 实时事件

统一信封：

```json
{
  "event_id": "...",
  "type": "message.bubble.created",
  "occurred_at": "...",
  "aggregate_id": "...",
  "sequence": 3,
  "data": {},
  "trace_id": "..."
}
```

主要事件：`job.accepted`、`job.progress`、`message.delta`、`message.bubble.created`、`message.completed`、`tool.confirmation.required`、`tool.completed`、`job.failed`、`notification.created`。

## 10. 前端信息架构与视觉

### 10.1 信息架构

- 首页：三个绘本式入口、最近对话、今日计划和进行中任务。
- 情感陪伴：角色列表、角色创建、聊天、记忆管理、表情包收藏。
- 生活助手：今日计划、提醒、账本、对话入口。
- 工作伙伴：技能大厅、文件库、任务中心、报告中心。
- 全局：通知、搜索、模型/隐私/权限/数据导出设置。

### 10.2 视觉规则

参考图已形成明确的模块色彩语义：

- 情感陪伴：粉色/珊瑚色，云朵客厅与爱心元素。
- 生活助手：暖黄/橙色，阳光、日历和家居元素。
- 工作伙伴：蓝紫色，夜空、书桌和效率元素。
- 公共品牌：奶白背景、蓝色云朵角色、圆角卡片、柔和描边与低对比渐变。

插画只承担氛围和导航识别，正文区域使用可控的半透明/实色容器保证 WCAG AA 对比度。动效支持“减少动态效果”，图片提供替代文本，不能用颜色作为唯一状态提示。

### 10.3 关键交互

- AI 回复以气泡逐条出现，同时允许“一次显示完整回复”的无障碍设置。
- Tool 调用显示“正在做什么、需要哪些权限、将产生什么变化”。
- 高风险动作使用确认卡，不用聊天文本中的含糊“好”作为授权。
- 长任务离开页面后继续，任务中心展示阶段、进度、失败原因、重试和结果。

## 11. 安全、隐私与合规

- TLS、数据库/对象存储静态加密、密钥管理服务、最小权限与定期轮换。
- 日志默认脱敏，不记录完整聊天、上传文件正文、令牌和供应商密钥。
- 文件使用短时签名 URL；下载、分享、删除记录审计。
- 防提示词注入：系统/用户/检索内容分层，文档内指令视为不可信数据；Tool 参数在服务端验证。
- 内容安全覆盖自伤、暴力、性内容、未成年人、违法活动和隐私泄露；情感陪伴不得宣称真人、制造排他依赖或阻止用户寻求现实帮助。
- 金融输出区分数据、引用观点和推断；展示来源、截止时间、限制和免责声明。
- 支持账户数据导出、按类型删除、整个账户删除和备份到期清理。
- 设立敏感数据保留策略；记忆默认可见、可撤销，健康/财务等敏感记忆应使用更严格的显式同意。

## 12. 非功能指标

MVP 建议目标：

- API 可用性 ≥ 99.9%；已接收用户消息持久化成功率 ≥ 99.99%。
- 普通 API p95 < 300 ms；聊天任务确认 p95 < 500 ms；健康模型下首气泡 p95 < 3 s。
- 实时事件在线投递 p95 < 1 s；重连后未读补拉 p95 < 2 s（不含大文件）。
- 异步任务状态 100% 可查询，消费重复不产生重复账单/提醒/邮件。
- RAG 引用命中率、回答忠实度、记忆正确率和工具成功率进入发布门禁，不只看模型主观效果。
- RPO ≤ 15 min、RTO ≤ 2 h；季度恢复演练。

容量规划必须在确认目标用户量后补充。压测至少覆盖同会话顺序、突发聊天、文档上传、Kafka 积压、模型超时、Redis 重连和 worker 重启。

M6 内测发布包含可导入的 Prometheus 告警规则、Grafana dashboard、SLO/error-budget 文档、事故响应、备份恢复、删除/导出、安全/故障/负载测试和发布回滚 runbook。Web 内测包提供 PWA manifest 与 service worker；service worker 只缓存应用壳层和静态资源，不缓存 `/v1/*` 用户 API 数据。

## 13. 测试与评测

- 单元测试：领域规则、时间/金额解析、拆气泡、幂等、降级状态机、指标计算。
- 集成测试：PostgreSQL/Redis/Kafka/Qdrant/对象存储 Testcontainers 或 Compose 环境。
- 契约测试：客户端 API、Kafka schema、MCP Tool schema 和模型供应商适配器。
- 端到端：注册→建角色→聊天→记忆→记账确认→提醒→离线补投。
- 提醒同步测试：权限允许/拒绝/撤回、重复同步、跨端修改、完成状态、时区/DST、系统条目被删除和冲突恢复。
- AI 评测集：人格一致性、简短度、记忆准确性、拒答、安全、RAG 引用、意图路由、工具参数。
- 金融金标集：不同日期格式、缺失/重复/异常价格、单位换算、阶段切分、图表节点和来源引用。
- 故障演练：Kafka/Redis/模型不可用、重复消息、网络分区、进程崩溃和对象存储延迟。
- 发布门禁：告警规则和 dashboard 可加载、runbook 已更新、最近一次恢复演练有证据或明确标注为 internal-only。

## 14. 仓库建议结构

```text
ai_companion/
  cmd/api/                 # HTTP/实时网关
  cmd/worker/              # 数据库优先、可选 Kafka 的后台任务 worker
  cmd/migrate/             # 数据库迁移入口
  internal/
    identity/ character/ conversation/ memory/ rag/
    agent/ skill/ ledger/ planner/ finance/ media/
    notification/ platform/
  migrations/
  api/openapi/
  events/schemas/
  web/
  mobile/
    android/                # Kotlin + Jetpack Compose
    ios/                    # Swift + SwiftUI
  workers/python/           # 文档/表格/金融/媒体算法
  deploy/compose/          # 本地依赖与应用编排
  docs/
  tests/e2e/
```

领域模块只通过公开接口或事件协作，禁止跨模块直接修改对方表。`platform` 放数据库、Kafka、Redis、对象存储、模型和可观测性适配，不放业务规则。

## 15. 尚未确定但已有处理策略的决策

1. **首发地区与数据保留**：M0 期间确定；此前使用可配置保留策略和数据区域抽象，不做不可逆的跨境架构承诺。
2. **DAU 与并发**：先按单区域中小规模部署建立基线，以压测结果决定 Kafka 分区、worker 数量和数据库规格；获得业务预测后重算容量。
3. **模型与成本**：M0 对候选供应商做质量、延迟、价格、隐私和区域五维评测；聊天、意图、Embedding、视觉分别路由，不锁定单一供应商。
4. **金融数据**：首版只支持用户上传的商品、股票和期货历史文件及用户提供的合法资料，不提供实时行情；获得授权后再按市场新增连接器。
5. **1.0 功能优先级**：真实邮件、团队、计费、未成年人模式和管理后台均为必交，但其内部优先顺序需依据首发地区、商业模式和目标用户再冻结。

## 16. 技术依据（评审日期 2026-07-01）

- Go 1.26 为 2026 年 2 月发布的当前版本：https://go.dev/doc/go1.26
- MySQL 官方将 8.4 定义为 LTS 系列：https://dev.mysql.com/doc/refman/8.4/en/which-version.html
- Redis Pub/Sub 是 at-most-once，离线补偿不能只依赖 Pub/Sub：https://redis.io/docs/latest/develop/pubsub/
- Redis Streams 提供消费组、ack、回放和保留策略：https://redis.io/docs/latest/develop/use-cases/streaming/
- MCP 是主机/客户端/服务端的上下文交换协议，并不规定应用如何管理 LLM：https://modelcontextprotocol.io/docs/learn/architecture
- Qdrant 支持 dense/sparse 混合检索、RRF 和多阶段查询：https://qdrant.tech/documentation/search/hybrid-queries/
- Apple EventKit 可创建、读取和编辑 Calendar/Reminders 数据，访问前需按平台要求取得权限：https://developer.apple.com/documentation/eventkit/accessing-the-event-store
- Android Calendar Provider 提供事件 reminder 数据；精确闹钟需遵守单独权限和平台政策：https://developer.android.com/identity/providers/calendar-provider 与 https://developer.android.com/develop/background-work/services/alarms
- Google Tasks 可作为 Android 任务清单的可选云端连接器：https://developers.google.com/tasks/reference/rest/v1/tasks
