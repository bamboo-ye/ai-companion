<p align="center">
  <img src="web/public/icons/logo.png" width="128" alt="伴AI 品牌形象">
</p>

# 伴AI · AI Companion

<p align="center"><strong>陪你聊，帮你记，和你一起把事情做好。</strong></p>
<p align="center">情感陪伴 · 生活管理 · 工作提效</p>

<p align="center">
  <a href="README.zh-CN.md">简体中文</a> · <a href="README.md">English</a><br>
  <a href="#快速开始">快速开始</a> · <a href="#系统实现效果">查看效果</a> · <a href="#技术亮点">技术亮点</a> · <a href="#文档导航">文档</a> · <a href="https://github.com/bamboo-ye/ai-companion/issues">反馈建议</a>
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26"></a>
  <a href="web/package.json"><img src="https://img.shields.io/badge/Next.js-16-111111?logo=nextdotjs" alt="Next.js 16"></a>
  <a href="workers/python/pyproject.toml"><img src="https://img.shields.io/badge/Agent-LangGraph-64748B" alt="LangGraph Agent"></a>
  <a href="#快速开始"><img src="https://img.shields.io/badge/Self--hosted-Docker-2496ED?logo=docker&logoColor=white" alt="Docker 自部署"></a>
</p>

伴AI 是一个可自部署的个人 AI 工作台：用持续对话承接情绪，用自然语言整理生活事务，用自己的资料生成办公成果。角色、记忆、知识与工具在同一处协作，从“说出需求”走到“查看结果”。

![伴AI 情感陪伴主页：角色会话与长期记忆入口](docs/images/readme/companion-dashboard.jpg)

## 一个工作台，三种陪伴

| 你想做什么 | 打开哪个模块 | 你会得到什么 |
| --- | --- | --- |
| 找人聊聊，延续上次的话题 | **情感陪伴** | 自定义角色、多轮会话、可查看和纠正的长期记忆 |
| 整理今天的安排和日常开销 | **生活助手** | 今日计划、提醒事项、账目记录与汇总、Excel 导出 |
| 把想法和资料变成可交付成果 | **工作伙伴** | 多附件研读、统一知识检索、按需审校、可编辑 PPT 工作台与对话续改、PDF 翻译、DOCX 副本编辑、CSV/XLSX 分析 |

三类体验共用上下文、知识与受控工具执行。管理员可在独立控制台管理模型、Agent、权限、额度和运行记录。

## 为什么是伴AI

- **对话有延续**：近期消息、带来源的摘要和可纠正记忆共同组织上下文，让关键偏好与约束能够跨会话保留。
- **资料有出处，分歧不掩盖**：知识中心统一检索文档、Wiki、个人记忆和产品指南，按版本阅读、追踪来源、提交反馈；检索命中不自动等于事实已核验。
- **任务有结果**：账目写入账本、提醒进入事项列表、办公任务返回文件，完成状态可以在对应页面核对。
- **成果能继续改**：PPT 从整稿规划进入逐页编辑、试播和导出；同一对话可继续调整风格，保留内容与页序，不必每次从头生成。
- **复杂任务能协作**：多附件独立读取，研究问题按依赖并行；明确要求“审校”时，由来源与表达两个专家分工检查。简单对话仍保留短路径。
- **长任务有章法**：超过单次模型窗口的资料通过全量分片、有界并行与确定性合并处理，失败分片可单独恢复。
- **运行有控制**：自部署应用与数据服务，配置模型、工具白名单和全局/用户额度；并行调用共享供应商许可，需要确认的动作由服务端审批与鉴权。

## 快速开始

准备好 **Git、Docker Desktop / Docker Compose v2 和 Make**。完整 Docker 路径不需要在宿主机单独安装 Go、Python 或 Node.js。

### 1. 获取项目与配置

```bash
git clone https://github.com/bamboo-ye/ai-companion.git
cd ai-companion
test -f .env || cp .env.example .env
```

> **先选择体验方式：** 默认 `MODEL_PROVIDER=development` 无需模型密钥，适合检查注册、页面和任务链路，但只返回固定模拟回复。要体验下方的自然语言问答、工具选择和内容生成，请先按[模型配置指南](docs/OPENROUTER_MODELS.md)设置服务端模型提供方、密钥和具体模型名称。模型请求会按配置发送给相应提供方；自部署不等于模型推理离线。

### 2. 启动应用

```bash
make docker-up
make docker-ps
```

打开 **<http://localhost:3000>**，注册并登录，添加角色后开始会话。Compose 会自动应用业务迁移并初始化 Agent 检查点。首次构建需要下载镜像与依赖。

### 3. 完成第一次体验

下列是接入真实模型后可尝试的输入示例，不是已经执行的操作：

| 模块 | 试着这样说 | 去哪里核对 |
| --- | --- | --- |
| 情感陪伴 | “明天要做项目汇报，我有点紧张。先陪我理清最担心的事。” | 继续补充原因，观察回复是否承接前一轮语境 |
| 生活助手 | “今天午餐花了 18 元，帮我记一笔餐饮支出。” | 核对候选并确认，在「账本」查看记录 |
| 生活助手 | “明天下午三点提醒我准备项目演示材料。” | 核对具体日期、时间与时区，在「提醒事项」查看 |
| 工作伙伴 | “帮我生成一个图文并茂的ppt介绍伴A I，使用表格介绍主要功能” | 在对话中查看执行结果与文件入口 |
| 工作伙伴 | 提供资料后：“做成 8 页培训演示，先讲目标，再讲构成与流程；只有构成页用分层图解，保留资料要点。” | 在 PPT 工作台查看整稿结构、逐页设计与导出结果；也可从成品模板开始 |
| 工作伙伴 | 上传 2–3 份资料后：“比较这些方案的成本、风险与实施条件，使用表格并标明来源；请仔细核对数字，列出未解决的分歧。” | 核对原文引用、比较口径与分歧；审校默认按请求启用，受部署配置和任务预算控制 |

<details>
<summary>启动排查、管理员入口与本机开发</summary>

- `make docker-logs` 查看应用日志；`make docker-down` 停止服务并保留数据卷。
- API 存活检查：<http://localhost:8080/healthz>；就绪检查：<http://localhost:8080/readyz>。
- 三个聊天模块默认都使用 Agent，需确保 Agent Worker 就绪。
- 管理后台：<http://localhost:3000/admin>，使用[独立管理员邀请与 Passkey](docs/ADMIN_PASSKEY_LOGIN.md)，普通注册账号不能直接登录。
- 对外开放前替换所有 `change-*` 开发密钥，并按部署文档配置域名与秘密；不要把真实密钥提交到 Git。
- 本机开发、端口冲突与常见问题见[新手上手指南](docs/GETTING_STARTED.md)；更多启动与发布命令见[技术指南](docs/TECHNICAL_GUIDE.zh-CN.md#快速开始)。

</details>

## 系统实现效果

以下保留本地运行环境的真实界面与对话截图，使用脱敏演示账号和演示数据。工作助手新增展示复用 **2026-09-29 至 2026-10-01** 的本地验收记录，并非重新生成的任务。示例展示“输入 → 执行 → 结果验证”，不代表当前账户已经执行这些操作，也不代表远程服务器已更新至相同版本。

### 情感陪伴：从倾听到具体建议

用户先表达汇报前的紧张，再补充“内容太多、担心超时”。角色承接前一轮语境，给出内容删减、分段计时与彩排建议；长期偏好可在记忆管理中查看和维护。主页保留在本文开头，下面展示实际对话。

<details>
<summary>查看多轮对话：情绪承接与汇报准备</summary>

![情感陪伴对话：承接情绪并给出时间管理建议](docs/images/readme/companion-conversation.jpg)

</details>

### 生活助手：说完之后，账本和提醒里都找得到

一句话记录消费，核对并确认后写入账本；一句话创建提醒，再去事项列表验证。演示账本中可看到 **¥18.00** 的消费记录，提醒列表中可看到 **“准备 AI Companion 项目演示材料”**、触发时间与通知渠道。

<details>
<summary>查看生活工作区与记账闭环：对话 → 账本记录</summary>

今日计划、提醒与账本集中在同一工作区：

![生活助手主页：今日计划、提醒事项与账本](docs/images/readme/life-dashboard.jpg)

在对话中描述金额与用途，确认后返回写入结果：

![生活助手对话：自然语言记账与成功写入](docs/images/readme/life-conversation.jpg)

进入账本，核对记录与月度汇总：

![生活助手账本：18 元消费记录与月度汇总](docs/images/readme/life-ledger-result.jpg)

</details>

<details>
<summary>查看提醒闭环：创建提醒的对话 → 提醒事项记录</summary>

先通过对话提出提醒需求，系统返回创建结果：

![生活助手对话：成功创建演示材料提醒](docs/images/readme/life-reminder-conversation.jpg)

再进入提醒事项页核对标题、时间与渠道：

![生活助手提醒：演示材料提醒与触发时间](docs/images/readme/life-reminder-result.jpg)

</details>

### 工作伙伴：从资料到可继续修改的演示成果

工作助手已从单次 PPT 生成升级为**对话生成 + PPT 制作工作台**。可以先提供整稿资料、听众和用途，也可以从成品模板开始；随后查看叙事结构、修改页面、试播，导出后继续编辑。

| 你正在做什么 | 已实现的交互 | 可查看的结果 |
| --- | --- | --- |
| 把资料组织成演示 | 通读整稿，统一配色、字体，为每页分配表达目的、版式与动画 | 「整稿结构与节奏」、逐页预览；图解页与列表、流程、对比页共存 |
| 快速开始与精修 | 4 套主题、23 种可选版式、3 套七页成品模板、18 种常用页面库 | 逐页编辑、插页、复制、排序、锁定，30 步撤销/重做与本地草稿恢复 |
| 在同一对话继续修改 | 读取当前账户、当前对话最近成功导出的设计稿，再调整风格与用途 | 保留内容、标题、页面 ID 和页序，重新交付修改后的文件 |
| 检查并交付成果 | 内容/版式检查、最终 PPTX 的 Office 渲染与下载 | 可编辑 PPTX、固定版式 PDF、可恢复 `.deck.json`；单页图解还可交付 PNG |

#### 对话续改：八页方案，改风格不改事实

本地验收先生成八页「社区阅读活动方案」，再在**同一对话**发送：

> 保持刚才8页PPT的全部内容、标题和页序不变，改成适合会前阅读的黑白简约风格，关闭所有动画，活动构成仍保留为其中一页可编辑图解。直接给我修改后的PPTX和PDF。

截图同时展示上一版交付、本次修改要求和新版 PPTX / PDF / 设计稿入口，不使用历史任务列表代替成果：

![工作助手对话续改：保留八页内容与顺序，改为黑白阅读版并返回三种文件](docs/images/readme/work-studio-chat-revision.png)

验收记录逐项比对原稿与续稿：**8 页 → 8 页，内容与顺序保留，全部动画移除**；原稿 PPTX 和续稿 PDF 的浏览器下载文件与服务端 SHA-256 一致。这是指定样例的验证结果，不代表任意输入均能一次通过。详细过程见 [PPT 制作工作台验收](docs/PRESENTATION_STUDIO.md)。

#### 整稿规划：看得到每一页为什么这样安排

另一个八页「社区阅读活动 · 志愿者培训」样例，在统一暖白、深蓝与赭黄配色下组织目标、构成、流程、对比和计划。只有第三页采用分层图解，其余页面保留各自适合的表达方式。下面展示整稿总览中的前六页及各页目的、版式和动画说明，而非整份 PPT 逐页贴图。

![PPT 工作台整稿结构与节奏：八页培训稿中的前六页规划与混合版式](docs/images/readme/work-studio-storyboard.png)

<details>
<summary>查看制作过程：模板选择 → 页面编辑 → 版式检查与导出</summary>

以下三张图来自不同的本地验收样例，用于展示对应操作，不是同一个任务的连续截图。

**从模板开始。** 按用途筛选季度复盘、项目提案、课程讲解，逐页预览后创建独立设计稿；「待填写」内容需替换为自己的资料。

![PPT 成品模板库：按用途选择、逐页预览，再使用模板](docs/images/readme/work-studio-templates.png)

**边看边改。** 左侧切换页面，中间预览，右侧编辑标题、正文和版式；可切换主题与动画、保存版本、试播。文字、图表和表格在导出的 PPTX 中仍是可编辑对象。

![PPT 工作台编辑界面：主题、动画、页面预览与编辑面板](docs/images/readme/work-studio-editor.png)

**检查后交付。** 「版式检查通过」对应最终 PPTX 的 LibreOffice 渲染检查，下方提供 PPTX、PDF 和设计稿下载。PDF 固定视觉结果；动画兼容性仍需在目标播放软件核对。

![PPT 工作台导出界面：版式检查通过及 PPTX、PDF、设计稿下载入口](docs/images/readme/work-studio-export.png)

</details>

<details>
<summary>保留已有演示：一句话介绍伴AI，使用图文与表格</summary>

实际发送的提示词：

> 帮我生成一个图文并茂的ppt介绍伴A I，使用表格介绍主要功能

工作伙伴识别 PPT 生成意图，调用 `PPTX 多模态生成` Skill，完成后在原对话返回 `banyai-intro.pptx` 文件入口。相关产品知识可进入上下文，为介绍内容提供功能和技术依据。

<p align="center">
  <img src="docs/images/readme/work-ppt-conversation.jpg" width="600" alt="工作伙伴实际对话：图文与表格生成请求、成功状态及 PPT 文件入口">
</p>

</details>

<details>
<summary>查看工作台：版本化 Skill、任务路由与 MCP 控制</summary>

工作台提供附件提取、DOCX 副本编辑、PDF 翻译、PPTX 生成、CSV/XLSX 分析等能力。后台执行关联任务状态、确认步骤和生成文件权限。

![工作伙伴工作台：Skill、任务路由与 MCP 安全控制](docs/images/readme/workbench.jpg)

</details>

## 技术亮点

当前项目聚焦 **可编辑内容生成、上下文工程、可控多 Agent 协作、Agentic RAG 与证据仲裁**：既要处理更多资料，也要回答“成果如何继续改、结论从哪来、分歧怎么办、成本由谁控制”。以下按 `c7f24b0` 的实现核对，日期为 **2026-10-01**，不代表某个服务器已经部署同一版本。

| 技术方向 | 具体实现 | 为什么重要 |
| --- | --- | --- |
| **可编辑演示生成** | `presentation-design-v2` 统一内容、来源与几何；整稿叙事规划、混合版式、对话续改、原生 Office 对象与最终文件渲染检查 | 把一次性文件变成可以继续精修、试播和多格式交付的设计稿 |
| **大文件全量分片** | 有序读取轮次、Source IR 稳定实体 ID、Token/字符双上限、覆盖率门禁、确定性合并、局部重试 | 让超出模型单次窗口的资料仍有完整的来源处理链 |
| **有界多 Agent 协作** | 多 Run / 进程池 / Composer 分片并行；2–3 附件独立读取、2–6 个研究子任务按依赖调度，来源与表达双专家按需审校 | 将复杂工作拆成可恢复的子任务，让无依赖步骤重叠执行 |
| **跨语言并发治理** | Skill 执行、解析、渲染分层限流；Go/Python 通过共享目录中的文件锁获取供应商许可 | 避免每层并发都合规、叠加后却压垮模型服务 |
| **统一上下文与长期记忆** | Go/Python 共享版本化快照、来源摘要、记忆替代链、节点预算和完整请求窗口校验 | 让聊天与工具执行使用一致、可纠正的上下文 |
| **并行知识编译与按需检索** | Wiki 全量来源页 + 有界语义分片，按序归并主题/实体/决策页；24 小时分片候选缓存，Agent 按需搜索、阅读与追踪引用 | 长资料既能逐片追溯，也能聚合阅读，重建可复用成功工作 |
| **统一知识工程** | 四类来源统一搜索/阅读/引用/反馈；版本绑定的持久索引任务、就绪键过滤、权限复验与词法降级 | 区分“原文可读”“检索已加工”和“结论已核验”，避免旧索引冒充当前证据 |
| **证据仲裁与可选辩论** | v2 协议区分调用状态、解决程度与停止原因；可启用两轮隔离核验、只读补证与交付摘要 | 证据不足保留分歧，不用多数票或模型间相互引用代替原文；辩论默认关闭 |
| **可恢复的工具执行** | PostgreSQL Run 与检查点、人工确认、幂等、租约和 revision fencing；Go Tool Gateway 掌握写权限 | 面对重试、取消和进程中断，仍能追踪状态与副作用 |
| **文档即产品知识** | 白名单文档、SHA-256、`go:embed`、本地 BM25 与指南引用 | 产品问题和介绍任务可直接取用说明，无需用户另行上传 |
| **身份与成本治理** | 管理员 Passkey、近期验证、逐资源额度继承、版本校验与事务审计 | 将管理权限、模型成本和配置变更纳入可追踪的控制链 |

### 1. 多 Agent 协作：先拆依赖，再分配预算

工作任务可为 **2–3 个附件**建立独立读取通道，同一附件仍按 `next_round` 顺序读完。Planner 也可提出 **2–6 个研究子问题**；运行时先验证无环依赖、只读工具白名单与参数，再并行执行可就绪的读取和分析，最终结合原始证据综合回答。写入和审批仍由 Go Tool Gateway 控制。

用户要求“审校 / 审阅 / 仔细核对”时，默认由**来源专家 + 表达专家**并行检查候选回复或文件生成参数，最多修订一次再复核；未通过则停止交付。这是内容审校，不是渲染后 PPT 的视觉验收；专家复用现有模型配置，不等于多个独立模型达成共识。

调用、Token 与成本预算在分发前分配，并为最终综合保留份额；成功与失败调用均结算已发生用量。检查点保存分支结果和异步任务 ID，恢复时继续观察已启动任务，可重试的失败分支最多重试一次。Go/Python 共享的供应商许可默认上限为 **8**，适用于同一主机上的可靠本地共享文件系统，不是跨主机分布式限流。

### 2. 大文件处理：全量读取与知识压缩各司其职

| 处理链路 | 默认边界 | 完整性与恢复策略 |
| --- | --- | --- |
| 附件读取与文件生成 | 聊天附件上传上限 20 MiB；Composer 每片 2,000 估算 Token / 12,000 源字符，并发 3、硬上限 4 | 读完全部轮次后生成；Source IR 稳定实体 ID、来源顺序归并、覆盖门禁与局部重试 |
| Wiki 语义编译 | 每批最多 24,000 UTF-8 字节，单文档默认并发 3、最多 64 批 | 每个原始 Chunk 保留独立页面；语义批次超预算时标记 `semantic_batch_budget_exceeded`，不把截取前缀伪装成完整摘要 |
| Wiki 重建与缓存 | 分片候选缓存 24 小时；失败批次最多重试一次 | 缓存绑定用户、文档、来源版本、模型及内容；发布前重新校验证据与权限，同一所有者的发布仍串行 |

“完整”指全量读取与可核验的来源/实体覆盖，不承诺任意格式都能无误解析，也不要求摘要复述每个细节。受控测试验证并发重叠、顺序和恢复；真实耗时取决于资料与模型服务，不以模拟测试宣称生产加速倍数。

### 3. 证据仲裁：保留可核对的分歧

`evidence-arbitration-v2` 把审校意见、研究事实、Wiki 来源差异和记忆关系组织成带候选与引用的问题，并保留 v1 历史读取。结构化研究事实按“主体、属性、适用范围、生效时间”比较，只有同口径的不同值才触发冲突；分析节点的引用必须最终回到原始工具证据，不能用另一段模型结论循环证明自己。

裁决中的候选必须属于当前问题，引用必须可见，证据引文必须真实出现在原文。证据不足、校验失败或预算不足时保留不确定性；研究分歧未解决时停止文件生成。Wiki 不因上传时间较晚就判定旧版本失效，记忆仲裁也不自动覆盖或删除旧记录，替代仍需显式纠正。

可选辩论将 `AGENT_DEBATE_MODE` 设为 `requested` 或 `on_conflict` 启用，默认 `off`；Wiki 有独立开关，记忆仍使用单次仲裁。首轮证据核验和反例核验互不看对方判断，只有新增有效证据才进入第二轮；每 Run 最多 **2 轮、6 次新增模型调用、2 次只读补证、60 秒绝对截止时间**，仍受原有总预算约束。聊天显示解决程度、轮次和停止原因；恢复显示的是交付时核验摘要，不证明来源当前仍有效。真实模型四组对照与生产灰度尚未执行。

### 4. PPT 制作：内容事实与视觉设计分工

内容规划负责事实、来源和页序，风格模型安排已有内容的表达方式，不能借重设计增删事实。`##` 组织内容页，页内 `###` 可组织分层模块；一页图解不会把整稿压成单页。稳定页面/内容 ID 支持续改与恢复，整稿结构说明只展示在编辑器，不写入读者可见的幻灯片。

工作台预览与导出共用几何和容量规则，再用最终 PPTX 渲染结果检查缺字、越界与空白页。模型内容审校与文件版式检查是两条独立防线。模板和手工编辑不要求风格模型；自然语言设计需配置支持结构化 JSON 的模型。缺少 Office 渲染器时仅提供未验证的 PPTX/设计稿，不提供 PDF；渲染器存在但检查失败则停止交付。

### 5. 知识中心：统一入口，保留来源差异

文档、Wiki、个人记忆和产品指南使用 `knowledge-evidence-v1` 版本引用，支持分页原文、来源追踪和分类反馈。用户身份来自登录上下文，团队范围重新验证成员身份，个人记忆不进入团队检索。引用校验验证可访问性、版本、正文哈希及引文存在，不自动证明结论为真。

文档解析与索引加工分离；Wiki 和记忆的全文分片在后台生成持久向量投影，以来源版本和索引规格隔离，查询前过滤当前可读且已就绪的投影键。模型或索引不可用时返回词法结果及降级说明。2026-10-01 的本地验收配置仍采用 `legacy` 词法检索，不能据此宣称语义 ANN 已启用或真实语料检索质量达标。

[PPT 制作工作台](docs/PRESENTATION_STUDIO.md) · [统一知识实现](docs/KNOWLEDGE_ENGINEERING_IMPLEMENTATION.md) · [并行任务](docs/PARALLEL_AGENTS.md) · [证据仲裁](docs/ARBITRATION.md) · [完整技术指南](docs/TECHNICAL_GUIDE.zh-CN.md)

### 工程难点与解决方案

| 难点 | 解决方式 | 验证入口 |
| --- | --- | --- |
| 整稿改风格后丢内容、乱页序，或图解挤占其他页面 | 冻结事实与稳定 ID、逐页完整映射、混合页校验；预览/导出共用布局并检查最终 PPTX | [整稿编排测试](workers/python/tests/test_presentation_whole_deck.py) · [工作台验收](docs/PRESENTATION_STUDIO.md) |
| 旧版本知识仍可命中，或 Wiki 摘要被当成原始证据 | 版本化来源契约、原文复验、按当前就绪键检索；派生结论继续追溯依赖原文 | [统一知识测试](internal/knowledge/service_test.go) · [索引任务测试](internal/document/index_jobs_test.go) |
| 多轮核验互相暗示，或重复资料被当成新证据 | 首轮上下文隔离、候选与证据冻结、补证去重、来源复验；轮次/预算/绝对截止时间有界 | [辩论与恢复测试](workers/python/tests/test_debate.py) · [评测门槛](docs/DEBATE_EVALUATION.md) |
| 跨片记录遗漏、重复，或合并顺序不稳定 | Source IR、覆盖门禁、固定顺序归并和缺失分片重试 | [分片与覆盖测试](workers/python/tests/test_agent_runtime.py) |
| 并行任务依赖错误、重复执行，或失败后预算漏计 | 校验任务依赖与只读工具；预分预算、保留成功分支、检查点恢复、汇合结算用量 | [并行任务测试](workers/python/tests/test_parallel_tasks.py) |
| 多进程、多语言各自限流，实际请求总量仍超限 | 供应商共享许可、等待计入超时、进程退出释放锁；解析和渲染使用独立并发上限 | [跨语言许可测试](internal/platform/modelquota/slots_test.go) · [Skill 调度测试](internal/skill/runner_test.go) |
| “多专家一致”掩盖错误来源，或审校无限返工 | 原始证据核验、逐项裁决、引文匹配；至多一次修订与复核，未解决则保留分歧 | [仲裁与有界修订测试](workers/python/tests/test_arbitration.py) |
| 长文档知识编译只覆盖前缀，缓存又混入旧来源 | UTF-8 安全的全量分批、预算超限标记、版本化分片缓存、发布前引用校验 | [Wiki 分片测试](internal/document/wiki_synthesis_test.go) |
| 新旧记忆冲突，被相似度或模型判断直接覆盖 | 相似度只触发关系核对，保留双方并绑定内容指纹；显式纠正才建立替代关系 | [记忆仲裁测试](internal/memory/arbitration_test.go) |
| 长对话压缩后丢失关键约束或事实归属 | 完整消息组、带来源摘要、记忆纠正链与最终窗口检查 | [共享上下文测试](internal/conversation/context_snapshot_test.go) |
| 原文取消共享后，知识与缓存仍返回旧内容 | 重新检查全部来源权限/版本，按依赖失效缓存 | [Wiki 权限与失效测试](internal/document/wiki_test.go) |
| Worker 崩溃、重复调度或取消后仍返回结果 | 持久化状态、幂等、租约接管和修订版本隔离 | [Agent Worker 测试](internal/agent/worker_test.go) |

更多关于 Wiki 人工编辑冲突、管理员并发修改与知识包一致性的设计，见[完整工程难点](docs/TECHNICAL_GUIDE.zh-CN.md#工程难点与解决方案)。

可在已安装开发依赖的环境运行 `make test-python` 和 `make eval-agent-arbitration`；数据库恢复用例需另配隔离测试库。测试验证的是控制与恢复契约，真实模型的误判率、延迟和成本仍需场景评测。升级统一知识需 PostgreSQL `000046` / MySQL `000035` 及此前迁移；PPT 渲染需 LibreOffice 与中文字体。重建应用镜像前先处理旧图版本未完成的运行，详见对应专题指南。

## 架构与技术栈

采用 **Go 模块化单体 + 独立异步 Worker + Python Agent 运行时**。模型负责提议与内容生成，Go 服务负责身份、业务写入和授权；PostgreSQL 保存权威状态，Kafka 是可选的扩展分发组件。

| 层级 | 技术 |
| --- | --- |
| Web | Next.js 16 · React 19 · TypeScript |
| API 与业务 | Go 1.26 · OpenAPI · PostgreSQL 18 |
| Agent 与算法 | Python · LangGraph · PostgreSQL Checkpointer · 版本化 Skill |
| 检索与文件 | Qdrant · Redis · MinIO · Source IR · 统一知识服务 / Wiki · 产品知识 BM25 · python-pptx / LibreOffice |
| 部署与观测 | Docker Compose · Caddy · OpenTelemetry · Prometheus · Loki / Alloy · 可选 Langfuse |

[系统架构与处理流水线](docs/TECHNICAL_GUIDE.zh-CN.md#系统架构) · [目录结构](docs/TECHNICAL_GUIDE.zh-CN.md#目录结构) · [并行与分片配置](docs/TECHNICAL_GUIDE.zh-CN.md#关键并行与分片参数)

## 文档导航

| 想了解什么 | 从这里开始 |
| --- | --- |
| 首次安装、注册、操作与排查 | [新手上手指南](docs/GETTING_STARTED.md) |
| 模型选择、服务端密钥、角色与预算 | [模型配置](docs/OPENROUTER_MODELS.md) |
| PPT 整稿规划、编辑、续改、导出与真实验收 | [PPT 制作工作台](docs/PRESENTATION_STUDIO.md) |
| 统一知识中心、版本引用、索引任务与反馈 | [知识工程实现与验证](docs/KNOWLEDGE_ENGINEERING_IMPLEMENTATION.md) |
| 大文件、并行、架构与设计取舍 | [中文技术指南](docs/TECHNICAL_GUIDE.zh-CN.md) · [English engineering guide](docs/ENGINEERING.md) |
| 多附件、内容审校、争议处理与升级 | [并行任务与资源](docs/PARALLEL_AGENTS.md) · [证据仲裁](docs/ARBITRATION.md) |
| 记忆、Wiki、检索与上下文组织 | [上下文管理](docs/CONTEXT_MANAGEMENT.md) |
| 更新产品介绍与内置检索资料 | [产品知识维护](docs/PRODUCT_KNOWLEDGE.md) |
| 管理员登录与用户额度 | [Passkey 管理](docs/ADMIN_PASSKEY_LOGIN.md) · [额度管理](docs/ADMIN_QUOTAS.md) |
| 部署运维、质量门禁与验收记录 | [运行手册](docs/runbooks/) · [质量验证](docs/TECHNICAL_GUIDE.zh-CN.md#质量与发布门禁) · [项目完成情况](docs/PROJECT_COMPLETION.md) |
| 安全报告与公开发布检查 | [安全政策](SECURITY.md) · [安全审查记录](docs/SECURITY_REVIEW.md) |
| API、领域设计与架构决策 | [OpenAPI](api/openapi/openapi.yaml) · [详细设计](docs/DETAILED_DESIGN.md) · [ADR](docs/adr/) |

## 参与改进

欢迎通过 [Issues](https://github.com/bamboo-ye/ai-companion/issues) 分享使用反馈与场景需求，通过 [Pull Requests](https://github.com/bamboo-ye/ai-companion/pulls) 改进代码、测试、文档与演示。问题报告请附脱敏的复现步骤、版本和日志，勿上传模型密钥、账号凭据或私人资料。

开发与提交前检查见[质量与发布门禁](docs/TECHNICAL_GUIDE.zh-CN.md#质量与发布门禁)。修改中文 README、技术指南或产品说明后，请同步内置知识：

```bash
node scripts/build-product-knowledge.mjs
node scripts/build-product-knowledge.mjs --check
go test ./internal/productknowledge
```

如果伴AI对你有帮助，欢迎点亮 Star，让更多人发现这个项目。
