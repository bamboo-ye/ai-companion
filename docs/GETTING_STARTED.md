# 新手上手指南

核对日期：2026-09-20。本文按当前仓库的 Web 页面、配置和启动脚本编写。功能实现与部署验收的边界见[项目实现情况](PROJECT_COMPLETION.md)。

## 1. 先选使用方式

- **已有可访问的网站**：直接从第 3 节注册和体验，无需安装开发工具。
- **在自己的电脑启动完整应用**：使用第 2 节 Docker 路径，宿主机只需 Git 和 Docker Desktop / Compose v2。
- **修改代码并本地调试**：使用第 5 节，需要 Go 1.26+、Python 3.12+、Node.js 24+ 和 pnpm 11.7.0。Android/iOS 工具链不是 Web 开发的前提。

默认配置使用 `MODEL_PROVIDER=development`，返回固定模拟回复，可验证注册、页面和任务链路；不能据此评价真实问答、自然语言工具选择或内容生成质量。真实模型接入见[模型配置](OPENROUTER_MODELS.md)。

## 2. 用 Docker 启动

在仓库根目录执行。已有 `.env` 时保留原配置：

```sh
test -f .env || cp .env.example .env
make docker-up
make docker-ps
```

完整栈包含 Web、API、通用 Worker、Agent Worker、PostgreSQL、Redis、Qdrant、MinIO 和 Loki/Alloy。Compose 会先应用业务迁移和 LangGraph 检查点初始化；一次性初始化容器成功退出是正常情况。Kafka 默认不启动。

| 入口 | 用途 |
| --- | --- |
| <http://localhost:3000> | 普通用户 Web |
| <http://localhost:3000/admin> | 独立管理员登录 |
| <http://localhost:8080/healthz> | API 存活检查 |
| <http://localhost:8080/readyz> | API 就绪检查 |
| <http://localhost:9467/readyz> | Agent Worker 就绪检查 |

首次构建需要下载镜像和依赖。用 `make docker-logs` 查看进度；需要退出日志跟随时按 Ctrl+C。`make docker-down` 停止应用并保留数据卷，再次 `make docker-up` 可启动。不要为解决普通启动问题删除数据卷。

默认端口绑定本机地址，示例密钥仅用于本地开发。更换访问域名时同步配置 `WEB_ORIGIN`、`NEXT_PUBLIC_API_BASE_URL`，并重建 Web；管理员 Passkey 绑定域名，不能随意在 `localhost` 与 IP 地址之间切换。

## 3. 完成第一次体验

### 注册并开始对话

1. 在登录页点击「立即注册」，填写昵称、邮箱和 8–128 位密码。
2. 注册成功后用邮箱、密码登录，选择「情感陪伴」「生活助手」或「工作伙伴」。
3. 点击「添加角色」，填写设定或采用默认值，再点击「开始会话」。先发一条简短消息验证回复。
4. 登录页和页面顶部都可以打开「新手指引」，支持搜索、逐节阅读和页面跳转；阅读进度保存在当前浏览器。

侧栏「个人主页」可查看账户资料、修改密码。改密成功后保留当前设备登录，其他设备需要重新登录。

### 先查产品知识，再上传个人资料

- 侧栏「产品知识」和「工作伙伴 → Wiki」都能打开内置产品说明，无需上传项目文档或配置向量模型。
- 接入真实模型后，也可以问「如何生成 PPT」「如何修改长期记忆」，点击回复中的产品指南引用查看原文。产品知识只解释用法，不读取当前账户的实时用量或替你修改配置。
- 个人资料通过「工作伙伴 → Wiki」上传文本 PDF 或 UTF-8 TXT，等待解析完成后再提问，核对引用中的文档名、页码和片段。
- Wiki 页面支持搜索、阅读、编辑、导出及反馈；证据不足时补充来源。纯图片扫描件不保证能在默认解析配置下提取正文。

### 体验生活助手

1. 打开「生活助手 → 账本」，输入「昨晚打车36元」并点击「解析」。
2. 核对收支方向、金额、分类与时间，点击「确认入账」，再在账本查看记录和汇总。
3. 在「提醒事项」解析安排，确认日期、时间与时区后创建；只有日期的提醒会显示「时间待安排」。
4. 「今日计划」包含手动事项、今天到期和已逾期的未完成提醒。完成事项后更新状态。

应用内提醒记录不等于手机系统通知已授权或同步成功。需要导出账本时点击「导出 Excel」，等待后台生成文件。

### 生成第一份办公文件

1. 打开「工作伙伴 → 工作台」，确认相应 Skill 已启用。
2. 建议先用「Markdown 文档」填写标题和短正文，点击「生成待确认任务」。
3. 打开「历史任务」，核对参数并「确认执行」，完成后下载文件。
4. 再尝试 PPTX 大纲、DOCX 副本编辑或 CSV/XLSX 分析。复杂资料任务可在工作聊天中上传附件，并明确目的、受众、语言与输出格式。

邮件草稿只生成内容；邮件投递是独立后端能力，不能把草稿生成成功当作已发送。任务处于待确认时不会自行执行；失败或取消后可按页面提示重试。

## 4. 文件与额度边界

| 入口 | 当前约束 |
| --- | --- |
| 工作聊天附件 / 个人 Wiki | 文本 PDF、UTF-8 TXT；默认单文件 20 MiB，由服务端 `DOCUMENT_MAX_UPLOAD_BYTES` 配置 |
| 工作台 DOCX 副本编辑 | 单文件最大 700 KiB；在新副本末尾追加段落 |
| 工作台 CSV/XLSX 分析 | 单文件最大 700 KiB；UTF-8 CSV 或无宏 XLSX；最多分析 10,000 行 |
| 工作台 PPTX 表单 | 页数 3–20；生成后仍需检查内容与排版 |

页面将上述二进制大小显示为 MB/KB。聊天附件上限不适用于所有办公表单。

额度按每项资源分别取「指定用户设置 → 全体用户设置 → 套餐」的有效值。达到文档、Skill、Agent 或模型成本额度时，按错误提示联系管理员；反复重试不会恢复额度。提高上限不会清零用量，也不会自动重启失败任务。详情见[额度管理](ADMIN_QUOTAS.md)。

团队工作空间、邀请和资源共享、邮件投递、订阅与未成年人策略已有后端能力；当前普通用户 Web 导航没有为这些能力全部提供独立页面。金融研究和原生 Android/iOS 完整 Beta 不在本次完成范围。

## 5. 在宿主机调试

以下是 Docker 完整栈的另一种启动方式。先停止占用相同端口的应用进程，然后在仓库根目录执行：

```sh
test -f .env || cp .env.example .env
python3 -m venv workers/python/.venv
workers/python/.venv/bin/python -m pip install -e 'workers/python[agent,dev]'
make infra-up
make postgres-migrate
make agent-checkpoint-setup
make agent-checkpoint-smoke
```

修改本机 `.env` 中 `AGENT_GATEWAY_URL=http://127.0.0.1:8080`；模板中的 `http://api:8080` 是容器地址。保留指向本机虚拟环境的 `PYTHON_EXECUTABLE` 和 `SPREADSHEET_EXECUTABLE`。API、两个 Worker 应读取同一份数据库、文件存储及网关密钥配置；不要改用各进程互不共享的 memory 数据库。

在三个终端中，分别从仓库根目录运行：

```sh
make run-api
```

```sh
make run-worker
```

```sh
make run-agent-worker
```

默认 `AGENT_CHAT_MODULES=companion,life,work`，因此三个模块聊天都需要 Agent Worker；通用 Worker 负责文档、Skill 等后台任务。只启动 API 不能完成全部体验。

第四个终端启动 Web：

```sh
cd web
pnpm install --frozen-lockfile
pnpm dev
```

Web 本地默认访问 `http://localhost:8080`。根目录 `.env` 不会自动成为 Next.js 的环境文件；使用自定义地址时，在 `web/.env.local` 设置 `NEXT_PUBLIC_API_BASE_URL` 和服务端 `ADMIN_API_BASE_URL`。后者只用于管理员同源代理，不能填入浏览器公开的密钥。

仅在需要验证 Kafka 扩展模式时使用 `make docker-up-kafka-scale`；日常开发无需开启。

## 6. 首次进入管理后台

普通注册账户不能登录 `/admin`。完整 Docker 栈启动后，由部署管理员在可信终端签发首次邀请：

```sh
docker compose --env-file .env -f deploy/compose/compose.yml exec api \
  /admin-invite --id owner --name '管理员' --reason '初始化本地后台管理员'
```

打开输出的一次性邀请链接，点击「绑定通行密钥并登录」，按设备提示完成验证。邀请有效期 30 分钟，开始绑定即消费；中途取消后需重新签发。PIN 留在设备本地，具体验证方式由浏览器和设备决定。

登录后先读后台「新手指引」：运行观测用于定位问题；配置中心可查看模型、套餐以及用户有效额度；Agent Studio 用于编排、评测与发布。额度修改要求填写原因；发布类配置可能要求另一位管理员审批。备用凭证、恢复及宿主机初始化见[管理员登录](ADMIN_PASSKEY_LOGIN.md)。

## 7. 常见问题

| 现象 | 检查顺序 |
| --- | --- |
| 网页打不开或 API 请求失败 | `make docker-ps` → API `/readyz` → `make docker-logs`；核对端口、Web API 地址和 `WEB_ORIGIN` |
| 回复固定、不会按要求调用工具 | 查看是否仍为 `MODEL_PROVIDER=development`；按模型文档配置并重启相关服务 |
| 聊天持续等待 | 检查 Agent Worker `/readyz`、检查点初始化、网关地址及密钥；宿主机不要使用容器主机名 `api` |
| 文档或 Skill 一直排队 | 确认通用 Worker 正在运行，检查 Python 虚拟环境、任务错误及文件目录；不要重复上传同一资料来催进度 |
| 文件任务没有执行 | 先查看「历史任务」是否待确认，再查看错误、额度和对应 Skill 启停状态 |
| Wiki 查不到来源 | 等待文档解析完成，检查是否为文本 PDF、问题是否有对应证据；内置产品知识与个人 Wiki 分开检索 |
| 后台 Passkey 失败 | 用邀请绑定的域名访问，核对 `WEB_ORIGIN` 和 Web 服务端代理地址；检查邀请是否已消费及设备验证支持 |
| 修改指南后产品仍显示旧内容 | 重新生成内置知识包、重建 API/Worker 与 Web；仅改 Markdown 不会替换已发布二进制 |

## 8. 文档更新与验证

页面指南来源是 `web/app/onboarding-content.ts`。本指南、中文 README 和专题文档由白名单生成器编入内置知识包；更新后在根目录执行：

```sh
node scripts/build-product-knowledge.mjs
node scripts/build-product-knowledge.mjs --check
go test ./internal/productknowledge
cd web
pnpm check
pnpm test
```

完整后端门禁是 `make release-check`；Web 还需单独运行 `pnpm build`。`make check` 会执行格式化、Go/Python 测试、Go 构建及 Compose 配置校验，不包含 Web 检查。以上本地检查不代表已完成真实模型、邮件投递、生产迁移或外部上线验收。
