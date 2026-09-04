# PostgreSQL + LangGraph 迁移

## 目标状态

- Go API 继续承担鉴权、HTTP/SSE、配额、安全策略和控制面。
- PostgreSQL 成为唯一业务关系库，使用 `app`、`eventing`、`agent`、`langgraph` 四个 schema 隔离职责。
- PostgreSQL 任务表和租约承担默认命令调度；Kafka 仅在水平扩展模式下提供低延迟分发提示和领域事件扇出。LangGraph 检查点不充当消息队列。
- Python LangGraph Worker 使用 `agent_run_id` 作为 `thread_id`，每次运行可独立恢复、审批和重试。
- 工具调用必须经过 Go Tool Gateway；账本、提醒、计划和文件写操作继续在落库前中断并等待用户确认。
- Qdrant 继续只保存文档向量与检索负载，文件本体继续保存在对象存储。

## 迁移原则

1. PostgreSQL 已是 API/Worker 的默认业务主库；MySQL 仅保留为有期限的迁移快照。
2. 所有新业务写入只进入 PostgreSQL，不启用无事务保障的应用层双写。
3. 切流前通过一次全量复制和关键聚合校验完成数据迁移；切流后以 PostgreSQL 为唯一写入源。
4. 切流开关只能在 PostgreSQL 表数量、行数、关键聚合和抽样内容校验全部通过后启用。
5. LangGraph checkpointer 只保存图执行状态；可查询的业务事实仍写入 `app` schema。
6. PostgreSQL 开始接受新写入后，MySQL 不再是可直接启用的无损回退源；回退前必须先完成反向补偿或恢复 PostgreSQL。

## Schema 职责

| Schema | 职责 |
| --- | --- |
| `app` | 用户、角色、会话、账本、提醒、计划、文档、团队等业务事实 |
| `eventing` | Outbox、Inbox、Kafka poison message、补偿记录 |
| `agent` | Agent run、interrupt、tool call、run event 等控制面 |
| `langgraph` | `langgraph-checkpoint-postgres` 管理的 checkpoint 数据 |

## 切片状态

### P0：并行运行基线（已完成）

- PostgreSQL 18 Compose 服务、持久卷和健康检查。
- `pgx` 驱动和 PostgreSQL Store 连接根。
- 同一迁移器支持 MySQL 与 PostgreSQL，迁移目录和必需表可分别配置。
- `agent.runs`、`agent.interrupts`、`agent.tool_calls`、`agent.run_events`。
- Docker 首次迁移、重复迁移和必需表校验。

### P1：身份、角色、会话（已完成）

- 已完成 PostgreSQL 原生 schema。
- 已完成身份、角色、会话、生成任务、用量和会话摘要 Store。
- 已完成事务性 Outbox、Inbox 幂等和任务租约的真实 PostgreSQL 集成测试。
- 已加入 MySQL 到 PostgreSQL 的核心数据复制与主键集合校验命令。
- 已完成最终全量复制、关键聚合校验和运行态切流。

### P2：事件和其余领域（已完成）

- 已将 Outbox/Inbox、Kafka poison message、审计与补偿记录迁到 `eventing`。
- 已完成 ledger、planner、reminder、notification delivery 的 PostgreSQL schema 与 Store。
- 已完成账本确认、导出租约、提醒确认/改期、通知 Outbox 入队和投递的真实 PostgreSQL 事务验收。
- 已复制并校验现有生活域数据；除主键集合外，同时校验有效账本金额聚合、计划状态、提醒状态和通知状态。
- 已完成 document、document ingest/cleanup、skill runtime、tool execution、generated file 的 PostgreSQL schema 与 Store。
- 已完成文档去重、摄取租约、页/切片落库、清理重试、Skill revision/租约、工具审计和成功 Outbox 的真实 PostgreSQL 事务验收。
- 已复制并校验现有文档与工具域数据；除主键集合外，同时校验文档页/切片计数、任务尝试次数、Skill 状态、工具执行和生成文件大小聚合。
- 已完成 team、email、billing、safety、operator account 与运营审计/补偿的 PostgreSQL schema 和 Store。
- 邮件创建同时写 Audit + Outbox，失败重放和 Worker 租约接管保持单事务；运营账号继续保护最后一个有效管理员和最后一个启用 MFA 的管理员。
- 已迁移并校验 12 张治理/运营表；当前真实数据包含 3 个计费方案、205 条 Inbox 和 34 条审计记录，主键集合和关键状态聚合均一致。
- 三张工作区分享表的延迟外键已在工作区复制后完成 `VALIDATE CONSTRAINT`，后续新增分享记录也必须引用真实工作区。
- 四组 PostgreSQL Store 集成测试已全部通过：核心、生活、文档/技能、治理/运营。
- 长期记忆已纳入 PostgreSQL schema、Store、复制与聚合校验。
- API/Worker 已统一通过 `ApplicationStore` 使用 PostgreSQL；Compose 的 `app` profile 会先校验迁移再启动服务。

### P3：LangGraph 运行时（进行中）

已完成运行时基础切片：

- 固定 `langgraph 1.2.9`、`langgraph-checkpoint-postgres 3.1.0` 和
  `psycopg 3.3.4`。
- LangGraph 检查点表由官方 `PostgresSaver.setup()` 创建在独立
  `langgraph` schema，不手工复制第三方表结构。
- 强制 `LANGGRAPH_STRICT_MSGPACK=true`，限制数据库检查点反序列化。
- 实现 Supervisor 和 companion/life/work 三个模块子图。
- 实现模块工具前缀白名单，跨模块工具在调用 Go Gateway 前拒绝。
- 工具采用 prepare/commit 两阶段接口；查询工具可直接完成，写工具先返回
  规范化预览和确认 token。
- 写操作使用 `interrupt()` 持久暂停；确认结果使用
  `Command(resume=...)` 恢复，拒绝时不会调用 commit。
- `agent_run_id` 原样作为 LangGraph `thread_id`，不会用
  `conversation_id` 复用检查点。
- Go 已实现 `Agent Run Store`，`agent.runs` 使用租约、修订号围栏和事件流
  管理 accepted/running/queued/completed/failed 状态；数据库约束保证
  `thread_id = run_id`。
- 内部 Tool Gateway 已复用现有 Go 工具定义与领域服务。Gateway 从持久化
  Agent Run 推导 `user_id`、`conversation_id`、`character_id`、模块和原始
  消息，Python 传入的身份字段不会参与授权。
- 模块白名单在 Go 侧再次按精确工具定义校验；跨模块调用在任何候选或业务写入
  发生前返回 `tool_denied`。
- 写工具的确认令牌使用 HMAC-SHA256 签名，同时绑定 run、用户、工具、候选、
  规范化载荷和有效期；账本、提醒、今日计划、时间调整和需确认 Skill 只在
  commit 阶段落入业务事实。
- 内部接口
  `POST /internal/v1/agent/runs/{run_id}/tools/{prepare|commit}` 使用独立
  Bearer 服务令牌，不复用用户 Access Token 或运维 Token。
- Python `HTTPToolGateway` 只发送 run id、工具调用和签名确认，不向 Go 侧
  提交可被误信的用户或模块身份；网络错误和非 2xx 响应会转为明确的运行失败。
- Tool Gateway 提供当前 Agent Run 对应的精确工具定义列表；Python 不维护
  另一份工具白名单，也不会把模型返回的未知工具名转发给领域服务。
- 创建 Agent Run 与 `agent.run.requested.v1` Outbox 事件在同一个 PostgreSQL
  事务内提交；重复幂等键不会生成第二条运行或第二条事件。
- 独立 Go `agent-worker` 默认按一秒间隔扫描持久化 Agent Run，使用
  `AGENT_WORKER_CONCURRENCY` 个执行槽并行认领。启用可选 Kafka scale 模式后，
  `ai-companion-agent-v1` consumer group 消费 `agent.run.requested.v1` 和
  `agent.run.resume.requested.v1`，有界内存队列由 `AGENT_DISPATCH_QUEUE_SIZE`
  提供背压，数据库扫描降为低频恢复。两种模式下，数据库认领和 revision 围栏
  都是执行权来源。
- Agent Worker 在 `AGENT_METRICS_ADDR`（默认 `:9467`）提供 `/healthz`、`/readyz`
  和 `/metrics`。指标记录首次入队、排队合并、运行中一次尾随重放、当前有界队列状态，
  以及 Skill 终态事件到持久化唤醒/分发入队的延迟；不使用 run/task/user ID 标签。
- `make agent-observability-canary` 发送不对应持久化运行的有限混合 Kafka 事件，验证真实
  consumer、调度和终态 no-match 指标，同时用独立低基数计数器断言这些 Canary ID 未启动
  Python/模型执行。普通业务流量不会污染判定；版本化基线控制周期数、总时长和一秒唤醒上限，
  JSON/JUnit 报告在成功、质量失败和指标超时路径都会落盘并以非零退出阻断发布。
- Agent Worker 使用数据库租约和 revision 围栏写回运行状态；Kafka 丢失、
  重复或 Worker 崩溃时，低频 reconciler 可通过 `FOR UPDATE SKIP LOCKED`
  接管到期运行。执行器失败只把运行延后为 `queued`，不会把 Kafka 重投当作
  唯一恢复手段。
- `waiting_tool` 恢复先由 Go Worker 读取对应 Skill Run 状态。任务仍为 queued、
  running 或 waiting_confirmation 时，只更新下一次恢复时间，不启动 Python、
  不连接 LangGraph checkpoint，也不重新读取工具目录。探测间隔默认从 500ms
  指数增长到 5s；任务进入 succeeded、failed 或 cancelled 后才启动一次 Python
  恢复图并读取最终结果。审批或工具恢复时复用 checkpoint 中的可信工具目录。
- 默认启用 `AGENT_PYTHON_POOL_ENABLED=true`。有界进程池优先复用空闲暖进程，
  并最多扩展到 Agent 执行并发数；进程复用 OpenRouter 决策端口、已编译
  LangGraph 图和 PostgreSQL checkpointer 连接。Worker 启动时默认通过
  `AGENT_PYTHON_POOL_WARM_SIZE=1` 预热一个进程。Python 只有在依赖导入、Graph
  编译和 checkpointer 初始化完成后才发送带协议与 Graph 身份的 ready 信封；Go
  校验成功后才将进程放入池中。预热失败会记录并回退到请求时惰性启动。请求级
  失败返回结构化错误但保留健康进程，超时、崩溃、EOF、身份不匹配或非法协议会
  立即销毁该进程。可将 warm size 设为 0 关闭预热；紧急回退时可将 pool 开关设为
  false，恢复为每次 Agent 执行启动一个受超时约束的 Python 子进程。
- OpenRouter 决策端口使用模型工具调用做意图识别，强制且校验响应中恰好一个
  工具调用，并校验选择必须存在于 Go 提供的工具清单。只有模型选择
  `*_no_tool` 后才进入普通回复生成；真实计划、提醒、账本或文档查询不会被
  正则匹配绕过模型。
- 写工具产生 LangGraph interrupt 后，Agent Run 写回 `waiting_approval` 并
  清除执行租约；确认 token、规范化参数和确认摘要保存在运行输出与
  PostgreSQL checkpoint 中。
- 用户确认或拒绝通过用户鉴权的 Agent Run API 写入；运行归属校验、interrupt
  状态变更、运行重新排队和 `agent.run.resume.requested.v1` Outbox 在同一个
  PostgreSQL 事务中完成。`Idempotency-Key` 保证重复点击不会产生第二次恢复。
- 公共 Agent Run 响应只暴露确认摘要，不暴露内部确认 token、原始运行输入或
  恢复载荷。Python Worker 从持久化的 `resume_resolution` 调用
  `Command(resume=...)`，拒绝不会进入 commit。
- `AGENT_CHAT_MODULES` 支持以逗号分隔的 `companion`、`life`、`work` 模块
  灰度切流，开发环境默认留空；生产必须包含全部三个模块。已启用模块在单一事务内写入用户消息、Agent Run、
  Run Event 和请求 Outbox，不再创建 legacy generation job 或
  `chat.command.v1`，未启用模块继续走原有链路。
- Agent Run 完成时，运行状态、完成事件和助手消息在同一事务内提交；唯一
  消息序号约束防止恢复重放造成重复助手消息。
- Agent Run 创建时写入固定 `deadline_at`，重试和审批恢复不会延长总时限。
  到期运行由当前 Worker 或 reconciler 原子终止为 `timed_out`，此后任何旧
  revision 都无法提交 Agent Run output 或助手消息。已经通过 Tool Gateway
  围栏的领域写仍属于 in-flight 请求，依靠稳定幂等键安全重放；若要支持严格
  原子撤销，仍需把 revision 围栏带入每个领域数据库事务。
- 用户取消对排队/审批运行立即终止，对执行中运行先写入
  `cancel_requested`；Agent Worker 轮询持久控制状态、终止 Python 子进程，
  再使用新的 revision 围栏完成 `cancelled`。重复取消返回同一个终态。
- `/metrics` 暴露各 Agent 活跃状态、最近五分钟终态数量、端到端 P95 时长，
  以及执行重试的调度、恢复、次数耗尽、总时限耗尽和恢复率；Agent 可领取积压及
  失败/超时同时纳入平台可靠性采样。重试指标由 PostgreSQL Run Event 和终态
  联合计算，不依赖易丢失的进程内计数；五分钟窗口使用重试事件与终态部分索引，
  不随全量运行历史做周期性扫描。
- Agent Worker Compose 依赖常驻 Worker。默认模式由数据库 reconciler 领取
  Agent Run；`kafka-scale` profile 启用时，Outbox Relay/consumer 可优先分发，
  reconciler 继续作为低频恢复路径。
- OpenRouter 工具决策使用固定、版本化的角色模型；默认不配置跨模型 fallback，
  仅在同一模型的兼容 provider 间按价格路由。若显式启用已评审的跨模型 fallback，
  每次尝试都会占用调用、输入/输出 token 和成本预算；401/403 直接终止。
- 真实 PostgreSQL smoke 已证明：中断前 commit 为 0，检查点存在；使用同一
  run id 恢复后 commit 恰好为 1，测试完成后删除 smoke thread。
- 真实 PostgreSQL Store 验收已覆盖 Agent Outbox 原子性、`ClaimNext`
  租约接管、revision 冲突、`waiting_approval` 暂停、幂等审批恢复和完成后
  助手消息落库；同时覆盖执行失败延期、`available_at` 前不可领取、延期后恢复、
  尝试次数耗尽、剩余总时限不足、指标聚合及重复完成不会产生第二条助手消息。
- 并发持久化门禁以 8 个执行槽处理 24 个隔离 Run：第一轮全部受控 503 并延期，
  第二轮每个 Run 重复投递两次；最终恰好 24 次恢复执行、24 次 revision 去重、
  24 条助手消息，每个 Run 固定 revision 5 和五条事件。
- `life` 模块真实 OpenRouter 灰度已通过：自然语言 CNY 50 记账在确认前
  中断，确认后恢复同一 Run，最终恰好一条助手消息、一条账本记录且没有
  legacy generation job。
- 已完成 Agent Worker 停机、Kafka broker 中断、旧 Relay topic 不兼容死信
  及正式补偿重放演练。最终 canary 的 Outbox 发布后约 1ms 被 Kafka consumer
  领取，全程没有 queued 或 reconciler 事件。

完整证据见
[`runbooks/evidence/2026-07-24-agent-life-canary.md`](runbooks/evidence/2026-07-24-agent-life-canary.md)。
取消、总时限与指标证据见
[`runbooks/evidence/2026-07-25-agent-run-control.md`](runbooks/evidence/2026-07-25-agent-run-control.md)。

下一切片：

- 完成审批等待期间重启和进程被 kill 后租约到期接管演练。
- 稳定观察窗口通过后，再依次灰度 `work`、`companion`。
- 稳定观察窗口通过后，移除 Go Worker 中对应的旧编排路径和已无流量的
  legacy generation job 代码。

### P4：切流与回退（运行态切流已完成）

- 已执行 MySQL → PostgreSQL 最终复制，13 张核心表以及生活、文档/技能、治理域均通过主键集合和关键聚合校验。
- PostgreSQL 迁移 `000001`～`000008` 已重复执行验证，16 张必需表全部存在。
- API 和 Worker 已以 `APP_DATABASE_DRIVER=postgres` 重建并启动，启动日志明确记录 `driver=postgres`。
- 已完成注册、角色、会话、长期记忆、账本确认、提醒确认、今日计划、工作区和异步聊天的真实 HTTP 整链验收。
- `chat.command.v1` 已由 PostgreSQL Outbox 发布到 Kafka，并由独立消费者写入 PostgreSQL Inbox；任务完成并生成助手消息。
- 文档上传已通过 Kafka 摄取，PostgreSQL 中状态为 `ready`，页/切片为 `1/1`，Qdrant 中存在对应 point。
- 新验收账号在 PostgreSQL 中为 1 条、MySQL 中为 0 条，证明切流后没有继续写旧库。
- MySQL 暂时保留为切流前快照；在完成反向差异补偿或确认无需业务回退前不得直接把运行态切回 MySQL。
- 尚待稳定观察窗口结束后下线 MySQL，并完成 PostgreSQL 备份恢复演练。

完整证据见
[`runbooks/evidence/2026-07-24-postgres-cutover.md`](runbooks/evidence/2026-07-24-postgres-cutover.md)。

## 本地命令

```sh
make postgres-up
make postgres-migrate
make postgres-copy-core
make postgres-validate-core
make postgres-copy-life
make postgres-validate-life
make postgres-copy-tools
make postgres-validate-tools
make postgres-copy-governance
make postgres-validate-governance
make postgres-test-stores
make agent-checkpoint-setup
make agent-checkpoint-smoke
make agent-worker-up
make agent-worker-logs
make docker-up
make docker-ps
```

直接运行 Go 迁移器时：

```sh
DATABASE_DRIVER=postgres \
POSTGRES_DSN='postgres://ai_companion:change-postgres-me@127.0.0.1:5432/ai_companion?sslmode=disable' \
go run ./cmd/migrate
```

全部 PostgreSQL Store 集成测试也可从宿主机直连运行：

```sh
POSTGRES_TEST_DSN='postgres://ai_companion:change-postgres-me@127.0.0.1:5432/ai_companion?sslmode=disable' \
go test -count=1 -v ./internal/persistence/postgresstore
```

## 运行态数据库开关

默认启动：

```sh
APP_DATABASE_DRIVER=postgres make docker-up
```

仅在 PostgreSQL 尚未接受任何新写入，或已经完成 PostgreSQL → MySQL
反向差异补偿时，才允许执行旧库回退：

```sh
APP_DATABASE_DRIVER=mysql \
docker compose --profile app --profile mysql \
  --env-file .env -f deploy/compose/compose.yml up -d --build
```

这条命令只是运行时开关，不会自动反向迁移数据。切流后的新增用户、消息、
账本、提醒、文档、Outbox/Inbox 和审计记录都只存在 PostgreSQL。
