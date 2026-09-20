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
| 把想法和资料变成可交付成果 | **工作伙伴** | 知识 Wiki、PPTX 生成、PDF 翻译、DOCX 副本编辑、CSV/XLSX 分析 |

三类体验共用上下文、知识与受控工具执行。管理员可在独立控制台管理模型、Agent、权限、额度和运行记录。

## 为什么是伴AI

- **对话有延续**：近期消息、带来源的摘要和可纠正记忆共同组织上下文，让关键偏好与约束能够跨会话保留。
- **资料有出处**：上传资料后，可检索、阅读和编辑 Wiki；回答与知识页可以回到原文证据，来源权限变化会同步影响访问。
- **任务有结果**：账目写入账本、提醒进入事项列表、办公任务返回文件，完成状态可以在对应页面核对。
- **长任务有章法**：超过单次模型窗口的资料通过全量分片、有界并行与确定性合并处理，失败分片可单独恢复。
- **运行有控制**：自部署应用与数据服务，配置模型、工具白名单和额度；需要确认的动作由服务端执行审批与鉴权。

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

以下保留本地运行环境的真实界面与对话截图，使用脱敏演示账号和演示数据。示例展示“输入 → 执行 → 结果验证”，不代表当前账户已经执行这些操作。

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

### 工作伙伴：把一句需求变成办公成果

实际发送的提示词：

> 帮我生成一个图文并茂的ppt介绍伴A I，使用表格介绍主要功能

工作伙伴识别 PPT 生成意图，调用 `PPTX 多模态生成` Skill，完成后在原对话返回 `banyai-intro.pptx` 文件入口。相关产品知识可进入上下文，为介绍内容提供功能和技术依据。

<p align="center">
  <img src="docs/images/readme/work-ppt-conversation.jpg" width="600" alt="工作伙伴实际对话：图文与表格生成请求、成功状态及 PPT 文件入口">
</p>

<details>
<summary>查看工作台：版本化 Skill、任务路由与 MCP 控制</summary>

工作台提供附件提取、DOCX 副本编辑、PDF 翻译、PPTX 生成、CSV/XLSX 分析等能力。后台执行关联任务状态、确认步骤和生成文件权限。

![工作伙伴工作台：Skill、任务路由与 MCP 安全控制](docs/images/readme/workbench.jpg)

</details>

## 技术亮点

围绕上下文工程、Agentic RAG 和并行 Agent，伴AI 将技术主题落实为可以在源码与测试中核对的工程机制。

| 技术方向 | 具体实现 | 为什么重要 |
| --- | --- | --- |
| **大文件全量分片** | 有序读取轮次、Source IR 稳定实体 ID、Token/字符双上限、覆盖率门禁、确定性合并、局部重试 | 让超出模型单次窗口的资料仍有完整的来源处理链 |
| **Agent 三层并行** | Go 多 Run 调度、持久 Python 进程池、LangGraph `Send` 分支；先分预算、后并行、按序归并 | 同时处理多个任务，也缩短单个长任务中的串行等待 |
| **统一上下文与长期记忆** | Go/Python 共享版本化快照、来源摘要、记忆替代链、节点预算和完整请求窗口校验 | 让聊天与工具执行使用一致、可纠正的上下文 |
| **可追溯知识 Wiki** | 来源/主题/实体/决策页，按需搜索与分页阅读，证据引用、编辑版本和权限失效 | 将检索片段组织为可阅读、可维护、可回到原文的知识 |
| **可恢复的工具执行** | PostgreSQL Run 与检查点、人工确认、幂等、租约和 revision fencing；Go Tool Gateway 掌握写权限 | 面对重试、取消和进程中断，仍能追踪状态与副作用 |
| **文档即产品知识** | 白名单文档、SHA-256、`go:embed`、本地 BM25 与指南引用 | 产品问题和介绍任务可直接取用说明，无需用户另行上传 |
| **身份与成本治理** | 管理员 Passkey、近期验证、逐资源额度继承、版本校验与事务审计 | 将管理权限、模型成本和配置变更纳入可追踪的控制链 |

### 大文件与并行，如何同时兼顾完整性

默认附件上限为 **20 MiB**。系统先完整读取来源，再按 **2,000 估算 Token / 12,000 源字符**组织 Composer 分片；Go 默认同时调度 **4 个 Run**，单 Run 的 Composer 默认并发 **3**、硬上限 **4**。输出按来源顺序合并，通过来源/实体覆盖检查后再渲染文件；失败时只重试相关分片。

这里的完整性指全量读取及可核验的来源/实体覆盖，不承诺任意格式都能无误解析。并行效果已有固定延迟模拟供应商的受控测试，真实耗时取决于文件与模型服务，不以模拟测试宣称生产加速倍数。

[阅读大文件处理设计](docs/blog/ppt-generation-vibe-coding/03-large-files-and-intermediate-representation.md) · [查看并行设计与实验](docs/blog/ppt-generation-vibe-coding/09-parallelism-design-and-results.md) · [完整技术指南](docs/TECHNICAL_GUIDE.zh-CN.md)

### 工程难点与解决方案

| 难点 | 解决方式 | 验证入口 |
| --- | --- | --- |
| 跨片记录遗漏、重复，或合并顺序不稳定 | Source IR、覆盖门禁、固定顺序归并和缺失分片重试 | [分片与覆盖测试](workers/python/tests/test_agent_runtime.py) |
| 并发提高后，预算超支或失败分支漏计费 | 预分配互不重叠预算，结算已启动分支，缓存成功结果 | [并行设计与验证](docs/blog/ppt-generation-vibe-coding/09-parallelism-design-and-results.md) |
| 长对话压缩后丢失关键约束或事实归属 | 完整消息组、带来源摘要、记忆纠正链与最终窗口检查 | [共享上下文测试](internal/conversation/context_snapshot_test.go) |
| 原文取消共享后，知识与缓存仍返回旧内容 | 重新检查全部来源权限/版本，按依赖失效缓存 | [Wiki 权限与失效测试](internal/document/wiki_test.go) |
| Worker 崩溃、重复调度或取消后仍返回结果 | 持久化状态、幂等、租约接管和修订版本隔离 | [Agent Worker 测试](internal/agent/worker_test.go) |

更多关于 Wiki 人工编辑冲突、管理员并发修改与知识包一致性的设计，见[完整工程难点](docs/TECHNICAL_GUIDE.zh-CN.md#工程难点与解决方案)。

## 架构与技术栈

采用 **Go 模块化单体 + 独立异步 Worker + Python Agent 运行时**。模型负责提议与内容生成，Go 服务负责身份、业务写入和授权；PostgreSQL 保存权威状态，Kafka 是可选的扩展分发组件。

| 层级 | 技术 |
| --- | --- |
| Web | Next.js 16 · React 19 · TypeScript |
| API 与业务 | Go 1.26 · OpenAPI · PostgreSQL 18 |
| Agent 与算法 | Python · LangGraph · PostgreSQL Checkpointer · 版本化 Skill |
| 检索与文件 | Qdrant · Redis · MinIO · Source IR · Wiki · 本地 BM25 |
| 部署与观测 | Docker Compose · Caddy · OpenTelemetry · Prometheus · Loki / Alloy · 可选 Langfuse |

[系统架构与处理流水线](docs/TECHNICAL_GUIDE.zh-CN.md#系统架构) · [目录结构](docs/TECHNICAL_GUIDE.zh-CN.md#目录结构) · [并行与分片配置](docs/TECHNICAL_GUIDE.zh-CN.md#关键并行与分片参数)

## 文档导航

| 想了解什么 | 从这里开始 |
| --- | --- |
| 首次安装、注册、操作与排查 | [新手上手指南](docs/GETTING_STARTED.md) |
| 模型选择、服务端密钥、角色与预算 | [模型配置](docs/OPENROUTER_MODELS.md) |
| 大文件、并行、架构与设计取舍 | [中文技术指南](docs/TECHNICAL_GUIDE.zh-CN.md) · [English engineering guide](docs/ENGINEERING.md) |
| 记忆、Wiki、检索与上下文组织 | [上下文管理](docs/CONTEXT_MANAGEMENT.md) |
| 更新产品介绍与内置检索资料 | [产品知识维护](docs/PRODUCT_KNOWLEDGE.md) |
| 管理员登录与用户额度 | [Passkey 管理](docs/ADMIN_PASSKEY_LOGIN.md) · [额度管理](docs/ADMIN_QUOTAS.md) |
| 部署运维、质量门禁与验收记录 | [运行手册](docs/runbooks/) · [质量验证](docs/TECHNICAL_GUIDE.zh-CN.md#质量与发布门禁) · [项目完成情况](docs/PROJECT_COMPLETION.md) |
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
