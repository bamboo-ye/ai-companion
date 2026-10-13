<p align="center">
  <img src="web/public/icons/logo.png" width="112" alt="伴AI 品牌形象">
</p>

<h1 align="center">伴AI · AI Companion</h1>
<p align="center"><strong>陪你聊，帮你记，和你一起把事情做好。</strong></p>
<p align="center">可自部署的个人 AI 工作台 · 情感陪伴 · 生活管理 · 工作提效</p>

<p align="center">
  <a href="README.zh-CN.md">简体中文</a> · <a href="README.md">English</a><br>
  <a href="#核心能力">核心能力</a> · <a href="#快速开始">快速开始</a> · <a href="#系统实现效果">查看效果</a> · <a href="#技术亮点">技术亮点</a> · <a href="#文档导航">文档</a> · <a href="#参与改进">参与贡献</a>
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26"></a>
  <a href="web/package.json"><img src="https://img.shields.io/badge/Next.js-16-111111?logo=nextdotjs" alt="Next.js 16"></a>
  <a href="workers/python/pyproject.toml"><img src="https://img.shields.io/badge/Agent-LangGraph-64748B" alt="LangGraph Agent"></a>
  <a href="#快速开始"><img src="https://img.shields.io/badge/Self--hosted-Docker-2496ED?logo=docker&logoColor=white" alt="Docker 自部署"></a>
</p>

**把持续对话、日常记录和办公成果放在同一个工作台。** 伴AI 通过角色、可纠正记忆、个人知识与受控工具，让你从“说出需求”走到“查看结果”：接着上次的话题聊，核对刚记下的账目，或继续修改上一版演示文稿。

![伴AI 情感陪伴主页：角色会话与长期记忆入口](docs/images/readme/companion-dashboard.jpg)

## 核心能力

| 场景 | 你可以做什么 | 结果在哪里 |
| --- | --- | --- |
| **情感陪伴** | 自定义角色、延续多轮对话，查看与纠正长期记忆 | 角色会话、记忆管理 |
| **生活助手** | 用自然语言记账、创建提醒、整理今日计划，导出 Excel | 账本、提醒事项、今日计划 |
| **工作伙伴** | 研读多份附件，按需审校，制作与续改 PPT、翻译 PDF、编辑 DOCX 副本、分析 CSV/XLSX | 工作聊天、PPT 制作工作台、任务文件 |
| **知识中心** | 统一搜索资料、Wiki、个人记忆和产品指南，阅读原文、追踪来源、提交反馈 | 统一搜索、资料与记忆管理 |
| **管理后台** | 配置模型、Agent、权限和用户额度，查看运行与知识加工状态 | 独立 Passkey 管理控制台 |

- **对话能够延续**：近期消息、带来源摘要和可纠正记忆共同组织上下文。
- **成果能够精修**：PPT 保留可编辑文字、图表和表格，可在同一对话继续调整风格。
- **资料能够追溯**：引用绑定来源与版本，分歧保留待核对；检索命中不自动等于事实认证。
- **执行能够管理**：长资料全量分片、独立任务有界并行，工具写入、审批与成本由服务端控制。

## 快速开始

准备 **Git、Docker Desktop / Docker Compose v2、Make**。完整 Docker 路径无需在宿主机安装 Go、Python 或 Node.js。

```bash
git clone https://github.com/bamboo-ye/ai-companion.git
cd ai-companion
test -f .env || cp .env.example .env
make docker-up
make docker-ps
```

打开 **<http://localhost:3000>**，注册并登录，添加角色后开始会话。首次构建会下载镜像和依赖；Compose 自动应用业务迁移并初始化 Agent 检查点。

> 默认 `MODEL_PROVIDER=development` 无需密钥，只返回固定模拟回复，用于检查页面与任务链路。体验真实问答、工具选择和内容生成前，请按[模型配置指南](docs/OPENROUTER_MODELS.md)设置服务端提供方、密钥和模型名称，再重新构建启动。自部署应用不等于离线模型推理。

接入模型后，可以从这些请求开始；它们是输入示例，不是已经执行的操作：

| 想试什么 | 试着这样说 | 如何验证 |
| --- | --- | --- |
| 延续对话 | “明天要做汇报，我有点紧张。先陪我理清最担心的事。” | 补充原因，观察回复是否承接语境 |
| 记录消费 | “今天午餐花了 18 元，帮我记一笔餐饮支出。” | 核对并确认，再到「账本」查看 |
| 创建提醒 | “明天下午三点提醒我准备项目演示材料。” | 核对日期、时区，再到「提醒事项」查看 |
| 制作演示 | 提供资料后：“做成 8 页培训演示，使用图文和表格，保留资料要点。” | 检查页序、来源和导出文件 |
| 比较资料 | 上传 2–3 份资料：“比较成本、风险与实施条件，标明来源，仔细核对数字。” | 核对引用、比较口径与未解决分歧 |

也可直接进入「侧栏 → 知识中心」或「工作伙伴 → 工作台 → PPT 制作工作台」。登录页和页面顶部提供可搜索的「新手指引」；直接阅读内置说明不需要模型密钥。

<details>
<summary>启动排查、管理入口与安全配置</summary>

- `make docker-logs` 查看日志；`make docker-down` 停止服务并保留数据卷。
- API 检查：<http://localhost:8080/healthz>、<http://localhost:8080/readyz>。
- 三个聊天模块默认依赖 Agent Worker；仅启动 API 不足以完成全部体验。
- 管理入口：<http://localhost:3000/admin>，需[管理员邀请与 Passkey](docs/ADMIN_PASSKEY_LOGIN.md)，不是普通注册账号。
- 对外开放前替换所有 `change-*` 开发密钥，配置域名与凭据；真实密钥不要提交到 Git。
- 安装排查见[上手指南](docs/GETTING_STARTED.md)，本机开发见[工程指南](docs/ENGINEERING.md#quick-start)。

</details>

## 系统实现效果

以下为脱敏演示账号的真实本地界面。工作助手截图采集于 **2026-09-29 至 2026-10-01**；历史验收不代表当前账户已执行任务，也不证明远程服务器的部署版本。

### 工作伙伴：从资料到可继续修改的演示成果

**对话生成 → 整稿规划 → 页面编辑 → 试播与导出 → 同一对话续改。**

| 能力 | 实际可用的交互 |
| --- | --- |
| 从模板或资料开始 | 4 套主题、23 种版式、3 套七页成品模板、18 种常用页面；也可提供整稿资料、听众与用途 |
| 调整整份演示 | 查看每页的表达目的、版式与动画；支持混合图解、流程、对比、图表和表格 |
| 继续编辑与交付 | 页面编辑、排序、锁定、30 步撤销/重做；导出可编辑 PPTX、PDF 和可恢复的 `.deck.json` |

**示例：八页方案，改风格不改内容。** 先生成「社区阅读活动方案」，再在同一对话发送：

> 保持刚才8页PPT的全部内容、标题和页序不变，改成适合会前阅读的黑白简约风格，关闭所有动画，活动构成仍保留为其中一页可编辑图解。直接给我修改后的PPTX和PDF。

![工作助手对话续改：保留八页内容与顺序，改为黑白阅读版并返回三种文件](docs/images/readme/work-studio-chat-revision.png)

该样例核对结果为 **8 页 → 8 页，标题、内容与页序保留，全部动画移除**；原稿 PPTX 与续稿 PDF 的浏览器下载哈希与服务端一致。[查看验收记录](docs/PRESENTATION_STUDIO.md)。

<details>
<summary>查看整稿规划：八页培训演示如何安排结构与节奏</summary>

另一份「社区阅读活动 · 志愿者培训」样例使用统一配色，仅第三页采用分层图解，其余页面使用适合各自内容的版式。下图是八页中的前六页规划，不是整份 PPT 的逐页贴图。

![PPT 工作台整稿结构与节奏：八页培训稿中的前六页规划与混合版式](docs/images/readme/work-studio-storyboard.png)

</details>

<details>
<summary>查看制作过程：选模板 → 编辑页面 → 检查与导出</summary>

以下三张图来自不同的本地验收样例，分别展示对应操作。

**选择模板。** 按用途预览季度复盘、项目提案或课程讲解；使用后把占位内容替换为自己的资料。

![PPT 成品模板库：按用途选择、逐页预览，再使用模板](docs/images/readme/work-studio-templates.png)

**逐页精修。** 切换主题与动画，编辑标题、正文、图表和表格，保存版本并试播。

![PPT 工作台编辑界面：主题、动画、页面预览与编辑面板](docs/images/readme/work-studio-editor.png)

**检查后交付。** 「版式检查通过」对应最终 PPTX 的 LibreOffice 渲染检查；下载 PPTX、PDF 与设计稿。动画兼容性仍需在目标播放软件核对。

![PPT 工作台导出界面：版式检查通过及 PPTX、PDF、设计稿下载入口](docs/images/readme/work-studio-export.png)

</details>

<details>
<summary>查看产品介绍示例：图文与功能表格</summary>

实际发送的提示词：

> 帮我生成一个图文并茂的ppt介绍伴A I，使用表格介绍主要功能

工作伙伴调用 PPT 生成 Skill，在原对话返回 `banyai-intro.pptx` 文件入口；相关内置产品知识为介绍提供依据。

<p align="center">
  <img src="docs/images/readme/work-ppt-conversation.jpg" width="600" alt="工作伙伴实际对话：图文与表格生成请求、成功状态及 PPT 文件入口">
</p>

</details>

<details>
<summary>查看更多办公工具：文档、翻译与表格分析</summary>

工作台提供文字翻译预览、Markdown 文档、DOCX 副本编辑、PPT 制作、CSV/XLSX 分析表单与 Skill 启停。PDF 翻译、附件研读和邮件草稿从工作聊天发起；草稿生成不等于邮件已发送。

![工作伙伴工作台：Skill、任务路由与 MCP 安全控制](docs/images/readme/workbench.jpg)

</details>

草稿按账户保存在当前浏览器，恢复仍需联网及可用的后台服务；清理浏览器前请另存设计稿。AI 风格规划消耗模型用量，手工保存与导出不再次规划风格。PDF 与导出实拍需要服务端渲染器和中文字体。[制作与恢复步骤](docs/GETTING_STARTED.md#制作续改与导出演示文稿)。

### 生活助手：说完之后，记录找得到

记账和提醒都可以从对话开始，再到对应页面核对。样例包含 **¥18.00 的支出**和 **“准备 AI Companion 项目演示材料”** 提醒。

<details>
<summary>查看记账：生活工作区 → 对话确认 → 账本记录</summary>

今日计划、提醒和账本集中在同一工作区：

![生活助手主页：今日计划、提醒事项与账本](docs/images/readme/life-dashboard.jpg)

描述金额与用途，核对并确认写入：

![生活助手对话：自然语言记账与成功写入](docs/images/readme/life-conversation.jpg)

在账本查看记录与月度汇总：

![生活助手账本：18 元消费记录与月度汇总](docs/images/readme/life-ledger-result.jpg)

</details>

<details>
<summary>查看提醒：创建对话 → 提醒事项结果</summary>

先通过对话提出需求，收到创建结果：

![生活助手对话：成功创建演示材料提醒](docs/images/readme/life-reminder-conversation.jpg)

再核对标题、触发时间与通知渠道：

![生活助手提醒：演示材料提醒与触发时间](docs/images/readme/life-reminder-result.jpg)

</details>

### 情感陪伴：接着上次的话题聊

用户表达汇报前的紧张，再补充“内容太多、担心超时”。角色承接语境，给出删减内容、分段计时与彩排建议；长期偏好可在记忆管理中查看和纠正。

<details>
<summary>查看多轮对话：情绪承接与汇报准备</summary>

![情感陪伴对话：承接情绪并给出时间管理建议](docs/images/readme/companion-conversation.jpg)

</details>

## 技术亮点

面向 **上下文工程、Agentic RAG、多 Agent 协作与可编辑内容生成**，重点解决长资料覆盖、并行恢复、来源可信和成果续改。实现基线：`42694a4`，核对日期：**2026-10-14**。

| 关键设计 | 解决什么难点 | 深入了解 |
| --- | --- | --- |
| **大文件全量分片** | 按轮次读完来源，以稳定 Source IR ID、Token/字符双上限、覆盖门禁和确定性合并，处理超出单次窗口的材料；缺失分片可重试 | [分片设计](docs/blog/ppt-generation-vibe-coding/03-large-files-and-intermediate-representation.md) |
| **有界多 Agent 并行** | 附件独立读取，研究按依赖调度，来源/表达专家按需并行审校；预分预算、保留成功分支、检查点恢复，避免重复执行与费用漏计 | [并行与资源治理](docs/PARALLEL_AGENTS.md) |
| **上下文与可纠正记忆** | Go/Python 共享版本化快照、来源摘要与记忆纠正链；检查完整请求窗口，避免压缩后丢失关键约束 | [上下文管理](docs/CONTEXT_MANAGEMENT.md) |
| **可追溯的统一知识** | 文档、Wiki、记忆、产品指南共用版本引用；后台持久索引、就绪键过滤与权限复验，防止旧索引或派生摘要冒充当前原文 | [知识工程实现](docs/KNOWLEDGE_ENGINEERING_IMPLEMENTATION.md) |
| **证据仲裁与可选辩论** | 核对候选归属、来源版本与原文引句；隔离首轮判断，只读补证，证据不足保留分歧，未解决研究争议停止文件生成 | [仲裁协议](docs/ARBITRATION.md) |
| **可编辑演示设计稿** | `presentation-design-v2` 分开管理事实与视觉设计；稳定页面/内容 ID 保持整稿续改，预览与导出共用几何，并检查最终 PPTX 渲染 | [PPT 制作工作台](docs/PRESENTATION_STUDIO.md) |
| **可恢复、可授权的执行** | Go Tool Gateway 控制业务写入；Run、幂等、租约与版本隔离处理取消和进程中断；管理员 Passkey 与分层额度管理控制权限和成本 | [工程设计](docs/ENGINEERING.md) · [额度管理](docs/ADMIN_QUOTAS.md) |
| **文档即产品知识** | 白名单文档、SHA-256、`go:embed` 与本地 BM25，让说明随应用发布；来源一致性和帮助召回测试防止文档更新而对话仍引用旧用法 | [知识包维护](docs/PRODUCT_KNOWLEDGE.md) |

<details>
<summary>查看关键参数与验证依据</summary>

| 链路 | 默认设置与保证 |
| --- | --- |
| 附件与内容编排 | 聊天附件默认 20 MiB；Composer 每片 2,000 估算 Token / 12,000 源字符，并发 3、硬上限 4；全轮读取后再生成 |
| Agent 协作 | 2–3 个附件独立读取、2–6 个研究子任务；按请求审校，至多一次修订与复核 |
| Wiki 编译 | 每批 24,000 UTF-8 字节，单文档并发 3、最多 64 批；成功候选缓存 24 小时，超预算仍保留全部原文页并标记语义覆盖不足 |
| 供应商并发 | Go/Python 共享许可默认 8，依赖同主机可靠共享存储，不是跨主机分布式限流 |
| 可选辩论 | 默认 `off`；启用后每 Run 最多 2 轮、6 次新增模型调用、2 次只读补证、60 秒绝对截止时间，仍受总预算约束 |

“全量”指读取与来源/实体覆盖，不等于摘要复述全部细节；内容审校也不是 PPT 视觉验收。统一检索未启用语义模型时保留词法路径。辩论的真实模型对照评测与生产灰度尚未完成，不以模拟测试宣称准确率或加速倍数。

验证入口：[分片与覆盖](workers/python/tests/test_agent_runtime.py)、[并行与恢复](workers/python/tests/test_parallel_tasks.py)、[知识权限与版本](internal/knowledge/service_test.go)、[整稿续改](workers/python/tests/test_presentation_whole_deck.py)、[产品知识一致性](internal/productknowledge/catalog_test.go)。更多难点见[工程难点矩阵](docs/TECHNICAL_GUIDE.zh-CN.md#工程难点与解决方案)，历史验收见[项目验证记录](docs/PROJECT_COMPLETION.md)。

</details>

## 架构与技术栈

**Web 交互 + Go 业务服务 + 独立 Worker + Python Agent 运行时。** 模型提出行动与生成内容，Go 负责身份、授权和业务写入；PostgreSQL 保存权威状态，Kafka 为可选分发组件。

| 层级 | 技术 |
| --- | --- |
| Web | Next.js 16 · React 19 · TypeScript |
| API 与业务 | Go 1.26 · OpenAPI · PostgreSQL 18 |
| Agent 与处理 | Python · LangGraph · PostgreSQL Checkpointer · 版本化 Skill |
| 检索与文件 | Qdrant · Redis · MinIO · Source IR · Wiki · 产品知识 BM25 · python-pptx / LibreOffice |
| 部署与观测 | Docker Compose · Caddy · OpenTelemetry · Prometheus · Loki / Alloy · 可选 Langfuse |

[系统架构与处理流水线](docs/TECHNICAL_GUIDE.zh-CN.md#系统架构) · [目录结构](docs/TECHNICAL_GUIDE.zh-CN.md#目录结构) · [并行与分片配置](docs/TECHNICAL_GUIDE.zh-CN.md#关键并行与分片参数)

## 文档导航

| 我要做什么 | 推荐文档 |
| --- | --- |
| 安装、首次使用与排查 | [上手指南](docs/GETTING_STARTED.md) · [模型配置](docs/OPENROUTER_MODELS.md) |
| 制作演示、恢复设计稿 | [PPT 制作工作台](docs/PRESENTATION_STUDIO.md) |
| 搜索资料、阅读原文与反馈 | [知识中心](docs/KNOWLEDGE_CENTER.md) · [上下文与 Wiki](docs/CONTEXT_MANAGEMENT.md) |
| 了解分片、并行与证据核验 | [中文技术指南](docs/TECHNICAL_GUIDE.zh-CN.md) · [English engineering guide](docs/ENGINEERING.md) · [证据仲裁](docs/ARBITRATION.md) |
| 管理账户、额度与知识质量 | [Passkey](docs/ADMIN_PASSKEY_LOGIN.md) · [额度](docs/ADMIN_QUOTAS.md) · [质量复核](docs/KNOWLEDGE_CENTER.md#后台知识质量) |
| 部署、升级与检查质量 | [运行手册](docs/runbooks/) · [发布门禁](docs/TECHNICAL_GUIDE.zh-CN.md#质量与发布门禁) · [项目验证](docs/PROJECT_COMPLETION.md) |
| 扩展或研究实现 | [OpenAPI](api/openapi/openapi.yaml) · [详细设计](docs/DETAILED_DESIGN.md) · [ADR](docs/adr/) |
| 报告漏洞与检查公开内容 | [安全政策](SECURITY.md) · [历史安全检查](docs/SECURITY_REVIEW.md) |

<details>
<summary>已有部署如何同步更新</summary>

从旧功能版本升级统一知识，需应用 PostgreSQL `000046` / MySQL `000035` 及此前待执行迁移；切换 Agent 图版本前处理未完成运行。仅同步文档和知识包不增加迁移，但需重建 API、Worker、Agent Worker 与 Web 才会在运行中的应用生效。PPT 的 PDF 导出另需 LibreOffice 与中文字体。详见[知识包维护](docs/PRODUCT_KNOWLEDGE.md)与专题部署说明。

</details>

## 最近更新

- **2026-10-13** · 补齐知识中心、PPT 续改、资料核验与后台质量的操作指南，完善页面引导及产品帮助检索。基于已有能力改进使用路径。[提交](https://github.com/bamboo-ye/ai-companion/commit/42694a4bc551c6e6cf35d1c570121c33d8808530)
- **2026-10-01** · 集成 PPT 制作工作台、统一知识工程与默认关闭的有界证据核验。[实现记录](docs/PROJECT_COMPLETION.md)

## 参与改进

欢迎通过 [Issues](https://github.com/bamboo-ye/ai-companion/issues) 分享使用场景和问题，通过 [Pull Requests](https://github.com/bamboo-ye/ai-companion/pulls) 改进代码、测试、文档与演示。报告问题时提供版本、脱敏的复现步骤与日志；安全漏洞请按[安全政策](SECURITY.md)私下报告，不要公开密钥或私人资料。

修改中文 README 或其他产品知识来源后，同步知识包并检查：

```bash
node scripts/build-product-knowledge.mjs
node scripts/build-product-knowledge.mjs --check
go test ./internal/productknowledge
```

完整开发与验证命令见[工程指南](docs/ENGINEERING.md#documentation-maintenance)。如果伴AI对你有帮助，欢迎点亮 Star，让更多人发现这个项目。
