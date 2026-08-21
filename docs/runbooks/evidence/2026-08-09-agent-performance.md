# Agent 执行链路与预热验收证据

验收日期：2026-08-09（Asia/Shanghai）\
环境：本地 Docker PostgreSQL/Kafka/API/Agent Worker，OpenRouter 真实模型\
Graph：`ai-companion-supervisor` `3.6.0`

## 执行链路

- 无工具简答固定进入 `direct`，跳过 `plan` 和 `assess_progress`；确定性评估中
  每次只使用一个模型调用。
- Agent Kafka 消费进入有界队列后并行执行；数据库认领、revision 和 deadline
  仍为持久化执行围栏。
- Python 常驻池优先复用暖进程。连续请求不再为了填满并发容量而依次冷启动。
- Worker 启动时默认预热一个 Python 进程。Python 在依赖导入、Graph 编译和
  checkpointer 初始化后发送 `agent-runtime-jsonl-v1` ready 信封；Go 校验协议、
  Graph 名称和版本后才报告 Worker ready。

## 真实测量

未启用启动预热、但已启用暖进程优先时：

```text
first  direct duration_ms=12421 cold_start=true
second direct duration_ms=4957  cold_start=false
```

启用 `AGENT_PYTHON_POOL_WARM_SIZE=1` 并重启 Worker 后：

```text
warmup requested=1 ready=1 duration_ms=955 success=true
first  direct duration_ms=10854 cold_start=false pool_wait_ms=0 queue_wait_ms=0
second direct duration_ms=2851  cold_start=false pool_wait_ms=0 queue_wait_ms=0
```

两次运行均为 `success=true`、`recycled=false`，各产生一条助手消息，没有调用写
工具。首请求已确认不再承担 Python 冷启动；真实模型供应商耗时仍存在波动，因此
版本化基线将暖简答 P95 初始上限校准为 12 秒，至少 12 个运行后才作统计判断。

## 价格上限内的延迟路由复测

2026-08-10 保持 `sort=price`、供应商回退和 prompt/completion 最高价格不变，新增
滚动五分钟 `preferred_max_latency.p90=8` 秒的供应商偏好。该值只降低慢端点的
优先级，不排除唯一可用端点；设为 `0` 可独立关闭。Worker 同时把每次 Python
执行拆分为模型耗时与本地 Graph/序列化开销。

重建 Worker 后启动预热和两次真实 OpenRouter 无工具简答结果为：

```text
warmup requested=1 ready=1 duration_ms=822 success=true
first  direct duration_ms=1458 model_calls=1 model_duration_ms=1427 runtime_overhead_ms=31
second direct duration_ms=1095 model_calls=1 model_duration_ms=1072 runtime_overhead_ms=23
```

随后在同一 Worker 启动窗口补足 12 个真实样本并运行版本化性能门禁：

```text
dispatch_samples=12 direct_python_samples=12 warm_direct_python_samples=12
queue_wait_p95_ms=0 dispatch_duration_p95_ms=1494
python_duration_p95_ms=1485 model_duration_p95_ms=1454
runtime_overhead_p95_ms=31
dispatch_error_rate=0 python_error_rate=0 cold_start_rate=0 recycle_rate=0
regression_violations=[]
```

12 次请求均为 `completed`、`direct`，各调用模型一次；持久化的
`response-quality-1.0.0` 结果全部为 `passed=true`、`rewrite_attempt=0`，没有写工具。
相较同一环境上一轮 10854/2851ms 的暖请求，这一窗口明显缩短且通过 P95 门禁；
由于没有并行随机 A/B，不能把全部差异单独归因于路由偏好，仍需持续灰度监测。

## 串行回退尾延迟控制

2026-08-10 将普通模型角色从“每个候选独立 30 秒”改为所有候选共享 30 秒，
每次 HTTP 尝试最多 15 秒，并动态为每个剩余候选保留最多 5 秒。三候选连续超时
约按 15/10/5 秒分配；快速 503、429、模型能力或契约错误不等待，立即转向下一
候选。复杂参数编排单独使用 45 秒共享截止时间和 30 秒单次上限，避免损害长结构
输出质量。已产生模型调用账本的失败在候选回退耗尽后由 Graph 安全结算，不由
Worker 重放终态模型节点；只有结算前逸出的运行/供应商错误进入三次运行级重试，
避免不可证明幂等的重复计费。401/403 仍立即停止。

缩放到 300ms 总截止时间的真实时钟故障演练让三个受控网络超时在 306ms 内结束，
没有放大成三个独立 300ms。最终 Worker 启动窗口的真实 OpenRouter 门禁结果为：

```text
dispatch_samples=13 processed_dispatch_samples=12 paired_execution_samples=12
python_samples=13 direct_python_samples=13 warm_direct_python_samples=13
queue_wait_p95_ms=0 dispatch_duration_p95_ms=8284
python_duration_p95_ms=8267 model_duration_p95_ms=8237
direct_max_model_attempt_timeout_p95_ms=15000 runtime_overhead_p95_ms=30
dispatch_error_rate=0 python_error_rate=0 cold_start_rate=0 recycle_rate=0
dispatch_outcomes={not_claimed:1, processed:12} regression_violations=[]
```

评估器只使用 `processed` 调度计算排队和执行延迟，并要求至少 12 个 processed
调度与 Python 执行按 run id 配对；`not_claimed` 的重复 Kafka 投递只进入分类统计，
不会用其 1ms 去重耗时稀释真实 P95。13 次持久化结果全部为 `completed/direct`、
质量检查通过且无需重写，每次仅一个模型调用，记录的单次超时上限均为 15000ms。

## 运行级重试治理

2026-08-10 将 Python 错误信封中的 `provider_status` 和 `retry_after` 完整透传到
Go 的单进程与常驻池两条协议。运行级调度规则固定为：401/403、其他永久 4xx 和
运行/Graph 契约错误立即失败；429/503 优先遵循有效的 `Retry-After`；408、其他
5xx 与传输错误使用有上限的指数退避；同一 run/attempt 的抖动结果确定且可复现。
退避上限默认 2 分钟，最大执行尝试默认 3 次，任何延期都不得达到或越过原始
`deadline_at`。结构化重试日志不包含提示词或模型输出。

受控故障测试覆盖了 17 秒 429 建议、HTTP-date 解析、15/30/60/60 秒退避封顶、
确定性 20% 抖动、截止时间裁剪、错误分类纠偏、三次尝试上限，以及错误元数据在
一次性 Python 进程和常驻池协议中的一致性。10 项定向故障测试全部通过。重建并
滚动替换 Worker 后，真实启动观测为：

```text
warmup requested=1 ready=1 duration_ms=895 success=true
max_execution_attempts=3 retry_base_delay=30s retry_max_delay=120s
retry_jitter_percent=20
```

Go、Python 和 `.env.example` 的默认模型配置版本也已统一为
`2026-08-bounded-fallback-v1`，避免未显式设置环境变量时出现跨进程版本漂移。

## 重试闭环发布门禁

2026-08-10 新增 `agent-retry-report-v1` 和只读的 `retry.v1` 基线。受控样本包含
429、503 和两次连续 408，共 3 个运行、4 次延期；评估器从 Worker ready、Python
执行和延期事件重建完整序列。默认门禁结果为恢复率 100%、未恢复 0、重复调度 0、
重复完成 0、策略/尝试数/deadline/配置漂移违规均为 0，最大延期 60 秒。

Worker 级受控故障回归进一步让第一次执行返回 429、第二次返回 completed，并验证
恰好发生一次 defer、两次 executor 调用、一次 complete 和零 fail。该测试进入
`eval-agent-runtime`，因此不会依赖真实供应商限流，也不会污染生产数据。

滚动替换 Worker 后执行一次真实无写工具直达 Canary：8 秒完成、一个模型调用、
一条助手消息；Python 日志明确记录 `result_status=completed`、`success=true`、
`cold_start=false`、`recycled=false`，本地运行开销 30ms。Worker ready 同时记录
30,000ms 基础退避、120,000ms 上限和 20% 抖动，满足重试评估器输入契约。

## PostgreSQL 持久化重试闭环

2026-08-10 将内存级故障回归升级为真实 PostgreSQL 状态机验收。隔离运行依次
验证：第一次认领 revision 2、延期后 revision 3、`available_at` 前认领被拒、
到点后 revision 4、完成后 revision 5；事件流固定为
`accepted → running → queued → running → completed`，延期事件带
`retry_kind=execution`、原因和精确可用时间。以旧 revision 重放完成被拒，数据库中
最终助手消息恰好一条。

同一验收窗口还落库了 `execution_retry_budget_exhausted` 和
`execution_retry_deadline_exhausted` 两种终态，并由 PostgreSQL Run Event 与 Run
终态联合计算最近五分钟的 scheduled、recovered、exhausted、deadline_exhausted
及恢复率。该测试发现并修复了一个边界缺陷：Worker 在剩余总时限不足以容纳下一次
退避时会提前结算 `timed_out`，原 Store 只允许到达 deadline 后超时，导致真实库中
产生 revision 冲突；现在该专用终态可在截止前安全结算，普通 `run_timeout` 仍只在
截止时间到达后生效。

宿主机连接当前运行 PostgreSQL 的真实结果：

```text
=== RUN   TestCoreStores
--- PASS: TestCoreStores (0.30s)
PASS
```

`postgres-test-agent-retry` 已加入 Makefile 和 CI Compose 门禁，验证迁移后数据库、
持久事件、围栏、指标与消息唯一性，而不是只检查进程内计数。

随后使用本地镜像代理完整执行该 Makefile 门禁，容器内
`TestCoreStores` 0.17 秒通过；API、通用 Worker 和 Agent Worker 已滚动替换。
新 Worker 预热 932ms 成功。部署后的两次无写工具 Canary 均走 `direct`，队列等待
和进程池等待均为 0，Python 执行分别为 1379ms、882ms，模型调用均为 1，本地
开销分别为 38ms、23ms，`cold_start=false`、`recycled=false`、`success=true`，
最终产生两条且仅两条助手消息。在线 `/metrics` 已出现四类重试 outcome 和恢复率
指标；无受控故障的真实流量窗口中各项为 0，Agent completed=2、failed=0、
timed_out=0。

指标查询随后增加五分钟终态窗口限制，并通过 PostgreSQL 迁移
`000014_agent_retry_metrics_indexes` 为 queued 重试事件时间窗和 Agent 终态时间窗
建立部分索引，避免运行历史增长后由周期采样造成全表扫描。迁移后的容器门禁再次
于 0.19 秒通过，在线数据库已确认两个索引存在；最终 API 健康且可靠性级别为 L0。

## 并发恢复与告警门禁

2026-08-10 将持久化门禁扩展为 24 个隔离 Agent Run、8 个执行槽。首轮执行器统一
返回受控 503，要求 24 个 Run 全部延期并各产生一次结构化重试观察；可用时间到达后，
每个 Run 同时提交两份恢复提示，共 48 个 Dispatcher 输入。最终结果为 24 个
`processed`、24 个 `not_claimed` 去重；每个执行器调用恰好两次、revision 固定为 5、
事件固定为五条，数据库中用户消息和助手消息各 24 条，没有重复完成。真实
PostgreSQL 容器测试耗时 0.19 秒，原单次恢复/耗尽测试同时于 0.12 秒通过。
随后复用相同镜像连续执行五轮并发门禁，单轮耗时 0.10–0.20 秒且全部通过；累计
覆盖 120 个持久化 Run、120 次失败延期、240 个恢复提示、120 次实际恢复和 120 次
revision 去重，测试清理后没有遗留验收运行。
同一并发用例连接真实 PostgreSQL 的 Go Race 检测亦通过，测试本身的时钟切换、
执行次数统计和 Dispatcher 回调没有发现数据竞争。

Prometheus 增加三条有界告警：五分钟 scheduled 达到 20、至少 5 个结算样本且
恢复率低于 80%、以及 exhausted 与 deadline_exhausted 合计达到 3。三条规则均使用
1 分钟持续窗口；由于来源本身已是滚动五分钟 Gauge，避免再使用 5 分钟 `for` 导致
一次真实突发刚好在告警生效前离开统计窗口。Grafana 增加 outcome 趋势和恢复率
面板。`validate_observability.rb` 同时验证 YAML/JSON、唯一 alert/panel ID、严重级别、
持续时间、阈值、恢复率最小样本保护、Runbook 和 Dashboard 指标引用；负向自测确认
缺失告警、移除最小样本门槛或重复面板 ID 都会被发布门禁拒绝。

最终 `release-check`、Go Vet、Ruby 语法检查、Dashboard JSON 解析和
`git diff --check` 全部通过；API 继续健康，Agent Worker 保持运行。并发验收使用的
用户、会话和 Run 在每轮结束后均清理，在线数据库复核计数为 0；真实 Worker 只看到
重复 Outbox 提示并以 `not_claimed` 安全去重，没有启动 Python 或真实模型调用。

## 真实告警通知链路演练

2026-08-10 新增完全隔离的 Prometheus 3.13.2、Alertmanager 0.33.1 和标准库合成
指标/Webhook Sink。演练直接挂载生产 `prometheus-rules.yml`，保留规则组 30 秒评估
周期和告警 1 分钟持续窗口；不缩短阈值、不调用模型、不写业务数据库。启动后先由
`promtool` 校验 Prometheus 配置与全部 10 条规则，再由 `amtool` 校验 Alertmanager
路由。健康状态要求三条 retry 告警全部 inactive 且 Alertmanager 没有活动告警；
故障状态一次构造 scheduled=24、5 个已结算样本、恢复率 20%、4 个耗尽样本，要求
三条规则全部 pending 后转 firing，Alertmanager 和 Webhook 均收到三条通知；恢复
健康指标后再要求三条规则 inactive、活动告警清空并收到三条 resolved 通知。

首次真实启动暴露了只读 Prometheus 容器的数据目录权限问题，已通过显式临时盘权限
修复。首次规则演练进一步发现恢复率表达式使用默认标签匹配：聚合后的样本无标签，
而 scrape 指标带 `job/instance`，导致风暴和耗尽进入 pending 时恢复率仍 inactive。
规则与 Grafana 面板均改为显式 `and on()`；静态验证器也增加相应的负向测试，防止
再次退化成隐式标签匹配。

修复后的真实结果为：零样本保护通过；三条告警全部 firing；Alertmanager 活动集合
与预期完全一致；Webhook 对每条告警均先收到 firing、再收到 resolved；最终状态
全部 inactive。`make observability-drill` 返回 0 并输出
`observability_drill=passed`。退出后复核该 Compose 项目的容器、网络和数据卷均为空。
同一入口已加入手动触发的 `Observability alert drill` CI 工作流，作为发布前可选的
远程真实门禁。

## 发布前后指标对比门禁

告警链路演练通过后，继续增加 `observability-release-baseline-v1`。采集器从 Prometheus
文本中只选择 14 个原始可靠性样本，并计算 Agent 终态失败、retry 耗尽和 retry 结算
三个派生值；快照最终共 17 个指标，只记录 UTC 时间和原文 SHA-256，不记录 URL、
请求头、HTTP route 标签、提示词、模型响应或凭据，输出文件权限固定为 0600。
输入上限为 2MiB，缺失、重复、负数、NaN/Inf、非整数计数、比例越界都会失败；URL
也禁止携带用户名、密码、查询参数或 fragment。

`release.v1` 同时执行绝对与相对门禁。绝对值要求 L0、queue < 500、最老任务 < 120s、
模型错误率 < 20%、模型 p95 < 10s、Agent p95 <= 480s、scheduled < 20、耗尽 <= 2；
相对发布窗口最多增加 50 个队列任务、30s 最老任务年龄、5 个百分点模型错误率、
2s 模型 p95、30s Agent p95、1 个 Agent 失败/超时、5 次 scheduled 或 1 次耗尽。
恢复率只在至少 5 个样本结算时要求 >= 80%。派生值必须与原始计数一致，恢复率必须
等于 recovered/settled，防止修改快照绕过门禁。JSON 与 JUnit 报告固定写入
`artifacts/observability-eval/`，基线只读且必须显式版本升级。

11 个定向测试覆盖通过样本、绝对/相对双重失败、零样本保护、低恢复率、缺失/重复/
非有限输入、派生值篡改、脱敏和 0600 权限、非零退出码及 JUnit。随后从已连续运行
13 小时且健康的真实 API 采集两份快照；两次均为 L0、queue/model error/Agent
failure/timeout/retry 全部为 0，前后差异全部为 0，`release.v1` violations 为空。
快照原文哈希不同，证明两次均独立读取在线端点；报告未保留原始 HTTP 指标明细。
默认样本对比已加入 `release-check`、Python CI 和手动真实告警演练工作流。

## 一键灰度发布决策

在独立快照对比基础上新增 `observability-release-gate-report-v1`，按固定顺序执行
before snapshot、Canary、stabilization、after snapshot 和 evaluation。Canary 使用
版本化 JSON 参数数组，禁止 `sh -c`/`bash -c` 字符串、重复键、超过 16KiB 的规格、
超过 32 个参数、超过 30 分钟执行或超过 5 分钟稳定等待。报告只保存 Canary 名称、
状态、退出码、耗时以及规格/命令哈希，不保存命令参数、URL 或环境变量。前快照失败
时 Canary 不会启动；Canary 非零或超时后仍采集后快照以保留故障证据；任何硬违规
均给出 rollback。工具只产生决策，不执行流量、镜像或数据库回滚。

新增只读 `api-health.v1.json` 和显式选择的 `agent-direct.v1.json` 两种规格。前者只读
取 `/healthz`、`/readyz`，作为默认安全门禁；后者复用一次性 direct Canary，明确可能
创建测试数据和调用模型。7 个编排测试覆盖 promote、Canary 非零、超时、指标绝对/
相对退化、前快照失败不执行、命令参数脱敏、私有权限、仓库规格验证及 shell 字符串/
重复键拒绝。

对已健康运行的真实 API 连续执行两次默认门禁。最终一次只读 Canary 耗时 48ms，
五个阶段全部 passed，before/after 原文哈希不同，metrics 与 gate violations 均为空，
最终 `decision=promote`、退出码 0。首次权限复核发现 metrics JUnit 为 0644，随后统一
修复为 0600 并补回归；重跑后运行目录为 0700，全部快照、JSON 和 XML 均为 0600，
Gate 报告未出现本机地址、metrics/health 路径、Authorization、token 或 secret 标记。

## 签名部署交接

2026-08-10 在一键 Gate 之后增加
`observability-release-gate-attestation-v1`。签名器要求完整 Gate 精确包含 before/after、
metrics JSON/JUnit、gate JSON/JUnit 六个产物，逐个绑定名称、字节数和 SHA-256，并将
Gate 报告哈希、运行目录 ID、Gate 时间、决策、签名时间、密钥轮换 ID 一起纳入
HMAC-SHA256。密钥只从环境读取，至少 32 字节，不接受命令参数且不会写入报告。
签名文件用排他创建，已有签名时拒绝覆盖；目录必须为 0700，所有文件必须为 0600，
符号链接、硬链接、非当前用户所有权、额外/缺失文件均失败关闭。

部署验签默认要求不超过 900 秒且决策必须为 `promote`，允许的时钟偏差为 30 秒。
验签不只比较签名，还重新读取六个产物，检查快照指标、派生值、恢复率、前后汇总、
delta 与两层报告的来源哈希，并根据 Canary/五阶段/violations 重新计算 promote 或
rollback，避免只改写 decision 或在签名前替换快照。Rollback
产物可以签名保留审计证据，但默认部署验证会拒绝，只有明确指定 rollback 或只读
审计模式才接受。工具不绑定云平台，也不执行真实镜像、流量或数据库变更。

7 个定向安全测试覆盖正确 promote、内容篡改、伪造决策、错误密钥、错误 key ID、
错误 run ID、过期/未来时间、rollback 决策约束、重复 JSON 键、短密钥、不安全权限、
符号链接、额外文件和重复签名。真实 API 复验结果为：L0/ready，只读健康 Canary
43ms，五阶段全部 passed，violations=0，运行目录
`release-gate-20260810T131719519567Z-53395`；使用进程内临时 256 位密钥签名并立即
按 promote 验签成功，签名算法为 HMAC-SHA256，六个证据产物及签名文件均为 0600、
目录为 0700，随后临时密钥从进程环境移除。

## 幂等推广授权

签名交接之后新增 `observability-deployment-authorization-v1`，将部署消费分为三步：
只读 `check`、显式 `issue` 和消费前 `verify`。Check 只验证新鲜 promote Gate 并计算
稳定幂等键，不创建目录或文件；Issue 使用 HMAC 域分离子密钥生成最长 15 分钟、默认
5 分钟的推广授权，且自动裁剪到 Gate 剩余有效期。授权绑定 deployment ID、Gate run
ID、attestation/gate report 哈希、key ID、scope、签发/过期时间和幂等键，目录/文件
权限分别固定为 0700/0600。

同一 deployment ID 与同一 Gate 的重复签发返回原记录；16 路并发测试中恰好一个
写入者，其余 15 个调用复用相同 authorization ID。相同 deployment ID 不能换绑另一
Gate。7 个定向测试还覆盖内容篡改、错误密钥、过期、rollback、非法 ID/TTL、重复
JSON、非私有状态目录和符号链接。通用层不会调用云 API，也不声称单靠本地文件实现
外部副作用 exactly-once；平台适配器必须把返回的 idempotency key 与部署请求原子
持久化。

真实在线验收重新生成
`release-gate-20260810T134752273999Z-55691`：API 为 L0/ready，只读健康 Canary
39ms，五阶段全部 passed、violations=0。使用进程内临时 256 位密钥签名后，dry-run
返回 ready 且确认没有生成授权文件；首次 issue 返回 `issued`，第二次返回 `reused`，
两次 authorization ID 均为 `auth-16b29980bb7cded3e5e070abd14ddc3a`，最终 verify
通过。状态目录为 0700、授权文件为 0600；没有执行镜像、流量或数据库变更，临时
密钥随后从进程环境移除。

## 事务型部署控制器模拟

推广授权之后新增 `observability-deployment-request-v1` 和平台无关适配器协议。请求将
deployment/authorization/run ID、target、adapter、幂等键、授权与 Gate 哈希纳入自身
SHA-256。SQLite 台账以事务原子绑定 request，保存 attempt、状态、租约 token/到期
时间、外部 operation ID 和脱敏错误码，并追加有序事件；目录与数据库权限分别为
0700/0600。内置且唯一注册的 CLI 适配器为 `dry-run`，只返回 simulated，不进行网络
访问，也不生成外部 operation ID。

16 个控制器测试覆盖：只读 plan、幂等 prepare/dry-run、24 路并发仅一次 submit、
retryable failure 使用同一幂等键恢复、过期租约通过 lookup 对账且不重复 submit、无
lookup/无幂等能力的旧租约转 indeterminate、非法适配器结果失败关闭，以及缺失、
0644、符号链接或错误 schema 台账拒绝。任何未知适配器异常也进入 indeterminate，
不自动盲重放。

真实在线模拟使用 Gate `release-gate-20260810T141016619569Z-57998` 和 deployment ID
`local-controller-20260810-141016`。Plan 返回 planned 且复核未创建 ledger；Prepare
返回 prepared、attempt=0、event=1；首次 Simulate 返回 simulated、attempt=1、
event=3、external operation ID=null；第二次 Simulate 和只读 Status 返回完全相同的
终态，没有新增 attempt 或事件。事件序列精确为
`prepared → dispatch_started → simulated`。台账目录为 0700、SQLite 为 0600；验收
只读取在线健康/指标并写本地忽略目录，没有执行任何真实部署、流量或数据库变更。

## 异步对账与适配器认证

部署台账升级为 v2，新增 reconciliation count。v1→v2 会先验证既有用户表和列集合，
再在单一 `BEGIN IMMEDIATE` 事务中增加计数列并更新 user version；受控迁移验证已有
simulated operation 完整保留且 reconciliation count=0。只读 status 不触发迁移，新增
显式 `migrate-observability-deployment-ledger` 入口。

前一阶段真实 dry-run 台账也从 version 1 显式升级到 version 2：升级前后状态均为
simulated、attempt 均为 1，升级后 reconciliation count=0，三个既有事件完整保留，
SQLite 文件权限继续为 0600；随后只读 status 正常返回。

外部 submit 返回 accepted 后，dispatch 将其视为不可重放终态；后续只能通过独立
reconciliation 租约按原幂等键 lookup。受控异步适配器依次返回 accepted、accepted、
completed，最终 attempt 保持 1、reconciliation count=2，事件追加
`reconciliation_started → reconciliation_pending → reconciliation_started →
reconciliation_completed`。24 路并发对账只发生一次 lookup。暂时性 lookup 失败保留
accepted 并可再次对账；lookup miss、external operation ID 变化和非法返回均转为
indeterminate。

适配器认证 API 仅面向隔离 provider sandbox：同一请求实际 submit 两次再 lookup，
要求三次结果引用同一 external operation ID，同时报告只保存该 ID 的 SHA-256。受控
幂等适配器通过；每次 submit 产生不同 operation ID 或缺少 lookup/幂等能力的适配器
被拒。仓库没有注册真实 provider adapter 或认证 CLI，因此本阶段没有发起外部部署。

## 对账调度与超时治理

部署台账继续从 v2 升级为 v3，新增独立 reconciliation schedule，原子保存 accepted
时间、下次到期时间、总截止时间、基础/最大退避、最大次数和最近一次延迟。新 accepted
操作默认等待 15 秒后才可 lookup；pending 或 retryable 结果按指数退避并封顶，次数或
30 分钟总截止时间耗尽时直接转 `indeterminate`，不会再访问 provider。v1→v3 与
v2→v3 都在单一显式事务内完成；既有 accepted 操作会被回填为立即到期，其他历史
状态不生成多余 schedule。

新增只读 health 输出与 schema，覆盖 accepted、due、deadline overdue、schedule
missing、indeterminate 和 oldest accepted age。批处理 API 只选择到期且无有效租约的
记录，并按 adapter registry 路由；未注册 adapter 只计数、不改变台账。新增测试验证
未到期零 lookup、2→4 秒退避、次数耗尽、截止时间耗尽前零 lookup、批量仅选到期、
24 路并发单 lookup，以及 v1/v2 到 v3 的数据保留迁移。仓库仍未注册真实 provider
adapter 或对账 CLI。

真实本地台账 `ledger-20260810-141016.sqlite3` 随后通过显式 Make 入口从 version 2
升级到 version 3。升级前后 `local-controller-20260810-141016` 均保持 simulated、
attempt=1、reconciliation count=0、event count=3，权限继续为 0600；因为该操作不是
accepted，schedule 行数为 0。只读 health 返回 accepted/due/overdue/missing/
indeterminate 全部为 0、oldest age=null。演练没有调用 provider 或改变部署状态。

## 双人签名人工结算

部署台账升级为 v4，新增每个 deployment 最多一行的 manual resolution 审计表。人工
结算必须依次绑定私有 provider evidence 摘要、请求人 HMAC、不同审批人的独立 HMAC，
最终 apply 同时验证两把不同密钥。请求绑定签名时的 `updated_at`、error code、adapter、
external operation ID 和证据摘要；任一字段变化均使状态围栏失败。审批任务只持有审批
密钥并签署完整 request 摘要，不能伪造请求人签名；最终控制器任务才持有两把验证密钥。

8 个结算测试覆盖 completed/failed、无 external ID 的失败结算、16 路并发仅一次消费、
相同 bundle 幂等、不同 resolution 防覆盖、证据和签名篡改、错误密钥、同人审批、过期、
签名后台账变化、0600/符号链接保护，以及 v3→v4 数据保持迁移。结算只改变本地台账，
不调用 provider；原始 provider evidence 不落库，只保存 SHA-256。

真实历史 dry-run 台账通过显式迁移入口从 v3 升级到 v4，原 simulated、attempt=1、
reconciliation count=0、event count=3 和 0600 权限保持不变，新增 resolution 表为空。
另在隔离临时台账对合成 indeterminate 操作执行完整五步 CLI：evidence→request→approve
→check 返回 ready→apply 返回 applied；同一 bundle 第二次 apply 返回 reused。resolution
ID 为 `resolve-da5aa1b35cad01c0f23c4c82477ce31d`，最终状态 completed，只新增一条
resolution 审计和 `manual_resolution_completed` 事件，attempt=1、reconciliation
count=2 未改变。请求人 `local-requester` 与审批人 `local-approver` 不同，台账和三份
材料均为 0600、状态目录为 0700。密钥由演练进程临时生成且未写入文件；未访问网络或
provider。

## 认证 Provider 沙箱与自动对账执行器

新增唯一可执行的 `sqlite-sandbox` Provider：只接受 `isolated-` target，把幂等键、请求
摘要、稳定 external operation ID、lookup 次数和状态持久化到独立 0600 SQLite；进程
重启后重复 submit 仍返回同一个 operation。认证命令对合成隔离请求实际执行两次 submit
和一次 lookup，并把 adapter 名称、实现版本、Provider 持久化实例 ID 摘要、能力、请求、完整
报告、签发/到期时间和 key ID 纳入域分离 HMAC。认证默认 1 小时、最长 24 小时；同一
显式 nonce 的失败重试直接复用 0600 证书，不再次访问沙箱；主动重新认证必须换 nonce。

调度器在读取 controller 待办或执行 Provider lookup 前先验证证书的严格字段集合、签名、
时钟、有效期、报告摘要、请求绑定、实现版本和 Provider 实例。缺失、过期、篡改、错误
密钥或换绑另一 sandbox 均失败关闭，controller 的 reconciliation count/event 与 Provider
lookup count 保持不变。通过后仅选择 v4 台账中已到期且无有效租约的 accepted 行，沿用
指数退避、截止时间、次数预算与 lease token fencing。16 路并发调度只发生一次 lookup、
一条 reconciliation_started 和一条 reconciliation_completed；并修正批处理语义，只有
实际取得租约的执行者计 completed，其他竞争者计 busy 或 selected=0。

新增认证证书和调度运行报告 JSON Schema、两个 Make 入口、可中断且最多 1000 次的有界
循环 API，以及 8 个定向测试，覆盖认证复用、初始化 CLI、完成路径、认证前置、并发、重启、循环边界、
错误 schema 和非私有数据库。该阶段不注册生产 Provider，不访问网络，也不执行镜像、
流量或数据库发布。

真实 CLI 隔离演练使用 nonce `isolated-drill-20260811-v2` 签发
`adapter-cert-758d0fc0d71459d2e4e1a435c8202bb2`，第二次相同 nonce 返回 reused；Provider
台账保持一行 completed operation、lookup count=1，证明认证重试没有重复访问。空 v4
controller 台账运行一次调度，输出 `observability-deployment-scheduler-run-v1`，before/
after 均为 accepted=0、due=0，batch selected/completed 均为 0。Provider/证书目录为 0700，
两份 SQLite 和证书为 0600；controller schema version=4 且 operation count=0。

## 对账调度服务化与指标告警

新增独立 `ai-companion-deployment-scheduler` 服务入口：每轮继续先验签再选择到期任务，
SIGINT/SIGTERM 通过 stop event 优雅退出；连续失败预算默认 3、最大 100，成功后归零。
运行状态按 certification ID 原子写入私有 JSON，保存成功/失败次数、连续失败、最后成功/
错误、耗时和完整严格运行报告；重启只恢复审计计数，不把 readiness 误恢复为 true。
`/healthz`、`/readyz`、`/metrics` 输出 liveness、最近一轮 readiness、成功/失败 counter、
认证到期、due/overdue/indeterminate 等 gauge。认证过期的服务重试不会增加 Provider lookup。

新增 `deployment-sandbox` 显式 Compose profile：非 root UID 10002 的 setup 容器在私有命名
卷中初始化 v4 controller、Provider v2 和按 nonce 签名的证书，成功退出后 scheduler 才启动；
容器 root filesystem 只读、启用 no-new-privileges，状态卷为唯一可写持久化路径。停机 Make
入口只 stop scheduler，不影响 app/基础设施且不删除卷。新增 4 条 Prometheus 告警和 2 个
Grafana panel，覆盖已配置 scrape target 不可达/up=0、连续失败、认证剩余不足 15 分钟和 due/deadline 积压；
观测契约自测会拒绝缺失告警或面板。

真实容器验收签发 `adapter-cert-c353e1ff0ce9720fcac670de8d81710c`，setup 首次返回
certified，重启返回 reused。服务 health/ready 均为 200；首次观测成功计数为 10，优雅 stop
退出码为 0，命名卷保留，重启后成功计数继续到 20、error=0、连续失败=0，证明状态未归零。
容器实际身份为 `uid=10002(agent)`；cert/controller/provider/state 子目录为 0700，证书、
两份 SQLite 和状态文件为 0600。整个 profile 只有本地 SQLite sandbox，没有生产 Provider
或外部部署调用。

## 预发布只读 Provider shadow 对账

新增 `ai-companion-deployment-shadow`，使用固定 HTTPS `GET` 契约按幂等键读取 Provider
状态。实现没有 submit/update 接口；base URL 必须与显式 host allowlist 完全一致，拒绝
URL 凭据、query、fragment、redirect、超大响应、额外字段、错误 key 和未来时间戳。
Bearer token 只从指定环境变量读取，配置对象 repr、状态、报告和指标均不保存 token、
URL、幂等键、external operation ID 或原始响应。Provider identity 绑定适配器、规范 URL、
allowlist、实例和实现版本，但令牌轮换不会错误地产生新实例。

控制器新增真正 SQLite `mode=ro`/`query_only` 的 bounded candidate API，只读取 accepted、
completed、indeterminate 行，不申请 lease、不追加事件、不改变 reconciliation count。shadow
报告区分 match、Provider ahead/behind、missing、external-ID mismatch、status mismatch、
ledger indeterminate 和 lookup error。只有 408/429/选定 5xx 与 transport error 可按 0.25s
指数退避，最多 3 次且 Retry-After 上限 5s；401/403 与契约错误一次即失败。定向测试以
controller SQLite 文件 SHA-256、operation 和 events 前后相等证明六种比较结果均未写账本。

常驻服务把运行计数和最后严格报告原子持久化，重启不恢复 readiness；暴露 9466
health/ready/metrics。新增独立 `deployment-shadow` Compose profile，controller 命名卷整体
只读挂载，只有 shadow 状态卷可写，进程为 UID 10002、只读 root filesystem、drop ALL
capabilities、no-new-privileges。4 条告警和 2 个 Grafana panel 覆盖不可用、连续失败、差异
和查询错误；静态校验器包含缺失 shadow 告警的负向自测。该阶段没有 Provider 写入能力，
未使用真实 Provider 凭据，也未向外部端点发请求。

## Shadow 稳定窗口门禁与签名证据

shadow 服务现在把每轮 success/error 追加到独立 0600 SQLite 历史账本。事件使用唯一时间、
连续 sequence、前序 hash 和当前 hash 形成链；成功事件绑定完整 report SHA-256，失败事件只
保存脱敏 error code。同一时间和内容可幂等复用，冲突内容、Provider digest 变化、report
篡改、序号断裂、前序 hash 或 chain hash 变化全部失败。服务状态仍只保存最后报告，但稳定
窗口决策读取并验证到当前时间为止的完整链，避免“最后一次成功”覆盖中间失败。

新增 `observability-deployment-shadow-gate-v1`：默认至少 30 轮、覆盖 15 分钟、selected
合计至少 10、最大 gap 90 秒、最后样本不超过 90 秒，且 failed/drift/lookup error 均为 0。
任何不满足项都产生确定 violation code 和 `hold`，CLI 返回 3；只有零 violation 的 `pass`
返回 0。验证器会从 policy/window 重新计算 violations 和 decision，不能通过删除 violation
并重算 gate ID 伪造通过。

通过报告可使用现有 secret-manager attestation key 创建域分离、短时 HMAC 证明，绑定
Provider identity、gate report digest 和历史 chain head。重复相同签名任务幂等复用；hold、
错误 key/key ID、过期、未来时间、陈旧报告、bundle 文件增删或签名后修改 gate 均失败。
新增 2 个 JSON Schema、3 个 Make 门禁入口和 7 个定向测试。测试覆盖 pass/hold 精确退出码、
失败/漂移/查询错误/gap/stale、历史冲突和篡改、runner success/error 记录、签名复用、错误
密钥、过期和签名后 evidence 变化。仍未连接真实 Provider，也没有写入授权。

本地容器升级验收继续使用空 controller 台账和不可解析测试域，因此 selected=0 且未发生
外部请求。容器内 gate CLI 可执行，历史账本首行 sequence=1、outcome=success、前序/当前
hash 均为 64 位，文件 0600、UID 10002，controller 文件不可写；既有服务 success counter
从 3 延续到 4。首次容器 gate 暴露了嵌套 `gates/<run>` 中间目录未创建时的错误堆栈，随后
改为逐层建立 0700 私有目录并统一错误分类。复测在空流量证据上生成
`shadow-gate-3a92fe4e3c72fa6b964474109f6892f7`，decision=hold、violations=3、退出码=3；使用
一次性本地测试密钥尝试 attest 明确返回“only a passing shadow gate can be attested”，退出码
为 1。服务最终 SIGTERM 退出码 0，命名卷和 hold 证据保留。

## 异步工具事件唤醒与重复调度合并

2026-08-12 将异步 Skill 等待从“只依赖 500ms–5s 退避探测”升级为事件优先、轮询
兜底。Skill 的 succeeded、failed、cancelled 终态现在与状态变更在同一事务写入
Outbox；Agent Worker 消费终态事件后，按 user/task 精确匹配 `waiting_tool`，立即改为
queued、刷新 `available_at`、追加 `tool_ready` Run Event 和新的持久化 resume event。
原定时 resume 不删除，因此进程崩溃、Kafka 重投或事件早于 Agent 挂起落库时仍可恢复。
Worker 在首次挂起前还会再次读取 Skill 状态；若任务已终态，fallback 当场变为可认领，
关闭“任务先完成、Agent 后挂起”的竞态窗口。

Dispatcher 新增 run-id 合并：尚未执行的重复提示只保留一份；执行中的新提示最多保留
一次尾随 replay，以便捕获刚持久化的新状态。24 路 PostgreSQL 并发门禁对每个 Run 同时
提交两份恢复提示，最终 24 个 Run 全部 completed、每个 executor 恰好两次、revision
固定为 5、助手消息各一条；重复提示由内存合并或数据库 revision 围栏共同吸收。真实测试
耗时 0.44 秒，`TestCoreStores` 的等待、幂等唤醒、两条 durable resume event 和完成闭环
耗时 0.16 秒。Go Race 覆盖 Dispatcher 与 Agent 事件处理器并通过。

PostgreSQL 迁移 `000015_agent_tool_wake_index` 为 `waiting_tool` 的 user/task 表达式查询
建立部分索引。在线开发库已应用该迁移并确认索引存在。Kafka 新建
`skill.run.failed.v1`、`skill.run.cancelled.v1`，保留既有 succeeded 主题；滚动替换后的
Agent 消费组实际分配 15 个分区，覆盖 2 个 Agent 请求主题和 3 个 Skill 终态主题。
Worker ready 前 Python 预热 967ms 成功，API health 为 ok。本次部署和验收没有发起模型
调用，也没有执行外部 Provider 写操作。

性能报告升级为 `agent-performance-report-v2`/`performance.v2`。真实灰度日志必须同时
包含 12 个 processed dispatch/Python 配对、direct 样本和至少 4 个成功工具唤醒样本；
无匹配的普通 Skill 终态事件不能冒充唤醒样本。默认门禁将 event age、数据库/队列唤醒
耗时及端到端 wake latency 分开计算，端到端 P95 上限为 1000ms；受控样本结果为
event-age P95 220ms、wake-duration P95 25ms、wake-latency P95 245ms，violations 为空。

## 失败与回退

- ready 信封协议或 Graph 身份不匹配：销毁进程，标记永久
  `runtime_contract`，不进入无意义的重试循环。
- 预热超过 30 秒：终止未就绪进程并记录 discard；Worker 继续启动，后续请求可
  惰性创建新进程。
- `AGENT_PYTHON_POOL_WARM_SIZE=0`：只关闭启动预热。
- `AGENT_PYTHON_POOL_ENABLED=false`：回退到每个运行启动独立 Python 进程。

## 回归

- Go 全量测试、`internal/agent` race、Go Vet：通过。
- Python：227 tests passed。
- Ruff、Mypy：通过。
- Agent contract/replay：7/7；模型调用 P50=1、P95=3。
- 性能 v2 同时限制简答模型 P95（12 秒）、本地运行开销 P95（500 毫秒）和
  异步工具事件唤醒 P95（1 秒）；
  Compose 配置和 `git diff --check`：通过。
