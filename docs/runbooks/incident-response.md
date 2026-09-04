# M6 Incident Response Runbook

Date: 2026-07-08

Use this runbook for internal release incidents involving API errors, database queue lag, optional Kafka delivery, model outages, or DLQ recovery. Always preserve user data first; do not “fix” by deleting accepted work.

## First five minutes

1. Confirm scope:
   - `GET /healthz`
   - `GET /readyz`
   - `GET /metrics`
   - Authenticated `GET /v1/reliability`
2. Capture the current degradation level, queue lag, oldest job age, model error ratio, and p95 latency.
3. Check recent deploy:
   - latest git commit
   - API/Worker start time
   - configuration changes
4. Pick the primary path below.

## Production health monitor

The production host runs `ai-companion-health-monitor.timer` once per minute. The
check is read-only: it records container state, Docker health, restart policy,
restart count, CPU/memory, the loopback readiness endpoint, and the public
readiness endpoint. It never restarts a service or modifies business data.

Use these commands during triage:

```sh
systemctl status ai-companion-health-monitor.timer
systemctl status ai-companion-health-monitor.service
cat /var/lib/ai-companion-monitor/latest.txt
journalctl -u ai-companion-health-monitor.service --since "30 minutes ago"
```

Run an immediate sample with
`systemctl start ai-companion-health-monitor.service`. A non-zero result means a
required service is missing/stopped/unhealthy, a restart policy no longer matches
`unless-stopped`, Docker is unavailable, or a readiness endpoint failed. CPU at
or above `CPU_WARN_PERCENT` is logged as a warning rather than triggering an
automatic restart.

Kafka container health uses a TCP socket open and the Web container uses Alpine
`wget` every 30 seconds. Do not replace these with `kafka-topics.sh` or `node -e`:
both start heavyweight runtimes on every Docker health-check interval.

## L3 保护模式

Symptoms:

- `ai_companion_degradation_level >= 3`
- New chat messages return `202` but no generation starts.
- Document query may return `degraded=true`.

Actions:

1. Confirm accepted requests are durable:
   - sample recent generation jobs with status `accepted`
   - confirm no broad 5xx on chat acceptance
2. Check dependencies:
   - authoritative database availability and task-claim health
   - Kafka broker and consumer group health only when `KAFKA_ENABLED=true`
   - model provider error rate and latency
   - Worker process health
3. If model-only outage:
   - keep L3/accept-only behavior
   - do not manually fail accepted jobs
   - wait for circuit cooldown or switch model provider after approval
4. Recovery:
   - verify degradation steps down L3 -> L2 -> L1 -> L0
   - confirm accepted jobs resume through Worker reconciler

## 队列积压

Symptoms:

- `ai_companion_queue_lag >= 500`
- `ai_companion_oldest_job_age_seconds >= 120`

Actions:

1. Identify the backlog class from database reliability sampler queries:
   - Skill runs
   - document ingestion
   - document cleanup
   - chat generation
   - ledger exports
   - notification deliveries
   - Outbox relay
2. Scale or restart only the affected Worker group.
3. In `kafka-scale` mode, if a Kafka consumer group is stuck, restart consumers; Inbox dedupe protects repeated events. In database mode, inspect claim locks, leases and Worker saturation instead.
4. Do not lower lease durations during an incident unless stale workers are confirmed dead.

## 模型供应商故障

Symptoms:

- `ai_companion_model_error_ratio >= 0.5`
- `ai_companion_model_latency_p95_seconds >= 10`
- generation jobs are deferred with `model_circuit_open`

Actions:

1. Verify provider status and region/network path.
2. Keep the circuit breaker enabled; it protects accepted jobs from false failures.
3. If switching provider, record:
   - previous provider/model
   - new provider/model
   - approval
   - expected cost/latency change
4. After recovery, confirm deferred jobs return to `completed` or retryable terminal states.

## Agent 执行重试异常

Symptoms:

- alert `AICompanionAgentRetryStorm`: 最近五分钟调度至少 20 次执行重试。
- alert `AICompanionAgentRetryRecoveryLow`: 至少 5 个重试运行已结算且恢复率低于
  80%。
- alert `AICompanionAgentRetryExhausted`: 尝试次数或总时限耗尽达到 3 个。

Actions:

1. 若需先排除规则、路由或通知链路故障，在有 Docker 的运维机运行
   `make observability-drill`。演练使用隔离网络和合成指标，不调用模型、不写业务库，
   并在成功或失败后自动删除容器、网络和临时存储。
2. 按 `provider_status`、`error_code` 和 `policy` 聚合结构化日志
   `Agent execution retry scheduled`，不要导出提示词或模型响应。
3. 检查最近五分钟持久化终态和重试事件：

   ```sql
   SELECT status,error_code,COUNT(*)
   FROM agent.runs
   WHERE completed_at >= CURRENT_TIMESTAMP - INTERVAL '5 minutes'
   GROUP BY status,error_code
   ORDER BY COUNT(*) DESC;

   SELECT payload->>'reason' AS reason,COUNT(*)
   FROM agent.run_events
   WHERE event_type='queued'
     AND payload->>'retry_kind'='execution'
     AND created_at >= CURRENT_TIMESTAMP - INTERVAL '5 minutes'
   GROUP BY payload->>'reason'
   ORDER BY COUNT(*) DESC;
   ```

4. 优先隔离异常供应商、回滚路由/超时配置或降低进入模型链路的并发；不要先提高
   `AGENT_WORKER_MAX_ATTEMPTS`，否则会放大供应商压力和重复计费风险。
5. 不延长已有 Run 的 `deadline_at`，不删除 queued Run，不手工复制助手消息。
   revision 围栏和 reconciler 应继续负责恢复。
6. 恢复后确认 scheduled 下降、exhausted/deadline_exhausted 归零；累计至少 5 个
   新结算样本后恢复率回到 80% 以上，再解除发布冻结。

## API 5xx

Symptoms:

- alert `AICompanionHTTP5xxHigh`
- user reports failed API actions

Actions:

1. Use `X-Trace-ID` from response or logs to find route-specific errors.
2. Check whether 5xx is isolated to:
   - document upload/query
   - Skill run creation/download
   - ledger export
   - auth/session
3. If write paths are affected, prefer rollback over hotfix unless data corruption risk is understood.
4. Confirm `POST /v1/conversations/{id}/messages` still preserves messages before declaring chat available.

## Agent 事件唤醒与调度

Symptoms:

- `AICompanionAgentWorkerUnavailable`: Agent Worker 的 9467 指标端点持续不可达。
- `AICompanionAgentToolWakeLatencyHigh`: 至少 5 个样本下，Skill 终态事件到 Agent
  持久化唤醒并进入分发队列的 p95 持续超过 1 秒。
- `AICompanionAgentDispatchReplayStorm`: 至少 20 个提示下，运行中尾随重放请求比例超过
  25%。

Actions:

1. 检查 Agent Worker `/healthz`、`/readyz`、`/metrics` 和 PostgreSQL Agent Run
   认领/等待状态；仅在 `kafka-scale` 模式下继续检查 terminal Skill topic 的分区分配与 lag。
2. 用 `ai_companion_agent_tool_wake_events_total` 区分 `awakened`、`no_match` 和 `error`。
   `no_match` 可由不关联 Agent 的普通 Skill 终态产生，不应单独作为故障；`error` 需要关联
   Agent reconciler 日志调查数据库认领失败；`kafka-scale` 模式再关联 `Agent Kafka consumer`
   和 `Agent tool task wake` 脱敏日志调查事件分发失败。
3. 若唤醒延迟升高，依次检查 Agent Worker CPU/内存、PostgreSQL
   `agent_runs_waiting_tool_task_idx` 和数据库认领延迟；`kafka-scale` 模式再检查 Kafka lag
   与分发队列深度。不要用无界轮询掩盖事件链路故障。
4. 若尾随重放比例升高，对比 `replay_requested`、`coalesced`、`replays_pending` 和执行时长。
   每个运行最多保留一个尾随重放；不要关闭持久化 claim 或 Inbox 去重。优先修复重复发布、
   状态抖动或异常长执行。
5. 告警期间保留 reconciler。数据库模式下它是默认调度器；`kafka-scale` 模式下事件提示
   丢失时它仍负责恢复 durable queued/waiting 状态，30 秒恢复扫描不作为 1 秒在线目标。
6. 恢复标准：Worker ready=1、数据库认领延迟恢复；`kafka-scale` 模式还需 Kafka lag 回落。
   连续两个五分钟窗口的唤醒 p95 <= 1 秒，
   且在不少于 20 个新提示时尾随重放比例 <= 25%。

## DLQ 重放与补偿

Prerequisites:

- Use `Authorization: Bearer $OPERATOR_TOKEN`.
- Set `X-Operator-ID` to a real operator identifier.

Inspect:

```bash
curl -H "Authorization: Bearer $OPERATOR_TOKEN" \
  http://localhost:8080/v1/ops/outbox/dead-letter
```

Replay one event:

```bash
curl -X POST \
  -H "Authorization: Bearer $OPERATOR_TOKEN" \
  -H "X-Operator-ID: sre-oncall" \
  -H "Content-Type: application/json" \
  -d '{"reason":"dependency recovered; safe to replay"}' \
  http://localhost:8080/v1/ops/outbox/$EVENT_ID/replay
```

Rules:

- Replay only after confirming the downstream operation is idempotent.
- Do not edit Kafka payloads manually.
- Record manual repairs as compensation records if the replay is not sufficient.

## 部署对账调度器

Symptoms:

- `AICompanionDeploymentSchedulerUnavailable`: 已配置的 scrape target 不可达或进程 `up=0`。
- `AICompanionDeploymentSchedulerFailures`: 连续失败达到 3 次。
- `AICompanionDeploymentSchedulerCertificationExpiring`: 认证剩余不足 15 分钟。
- `AICompanionDeploymentSchedulerBacklog`: due 达到 10 或存在 deadline overdue。

Actions:

1. 先检查 `/healthz`、`/readyz` 和 `/metrics`；liveness 正常但 readiness 为 0 表示
   最近一次调度失败，不能据此绕过认证或直接修改台账。
2. 查看私有状态文件的 `last_error_code`、`consecutive_failures` 和最后成功报告。只使用
   脱敏错误码；不要把认证密钥或 Provider 原始响应写入事件记录。
3. `scheduler_error` 优先检查认证是否过期、nonce、key ID、实现版本和持久化 Provider
   instance ID；`filesystem_error` 检查命名卷所有权以及目录/文件是否保持 0700/0600；
   `controller_error`/`sqlite_error` 检查 v4 台账和租约竞争。
4. 证书即将到期时使用新 nonce 运行容器内初始化任务，再重启 sandbox scheduler。
   同一 nonce 只用于失败重试，不得覆盖或删除原认证材料。
5. due 积压时先确认 Provider 可查询，再评估批次上限和间隔。不要重放 submit；accepted
   记录只能按原幂等键 lookup。超过次数或截止时间的记录应进入 indeterminate，并走
   双人签名人工结算。
6. 恢复标准：ready=1、连续失败归零、due 持续下降、overdue=0，且成功计数只按实际
   取得租约的执行者增加。生产 Provider 不得复用本地 sandbox 认证。

## 只读 Provider shadow 对账

Symptoms:

- `AICompanionDeploymentShadowUnavailable`: 已配置的预发布 shadow target 不可达或进程
  `up=0`。
- `AICompanionDeploymentShadowFailures`: 连续批次执行失败达到预算。
- `AICompanionDeploymentShadowDrift`: 控制账本与 Provider 状态持续不一致。
- `AICompanionDeploymentShadowLookupErrors`: 有限安全重试后仍存在只读查询错误。

Actions:

1. 检查 9466 端口的 `/healthz`、`/readyz` 和 `/metrics`。服务启动但未完成首批比较时
   readiness 为 0；不要把 liveness 当作比较结果。
2. 查看私有 shadow 状态文件中的最后报告。优先按 outcome 分流：`provider_ahead` 表示
   Provider 已完成但本地仍 accepted；`provider_behind` 相反；`provider_missing`、
   `external_id_mismatch` 和 `status_mismatch` 必须结合 Provider 审计记录复核。
3. `lookup_error` 只保存脱敏错误码。401/403 检查只读令牌和账户范围；429/5xx 检查限流
   与 Provider 状态；`provider_contract`、key mismatch、redirect 或 response-too-large
   视为契约/安全故障，不要增加重试次数绕过。
4. 确认 Provider base URL 为 HTTPS、host 与 allowlist 完全一致，URL 不含用户名、密码、
   query 或 fragment。轮换令牌不会改变 Provider identity；切换实例标识会生成独立状态。
5. shadow 账本挂载必须保持只读，状态使用独立可写卷。不要授予提交/更新权限，不要把
   shadow 报告直接转换为自动账本修复或 Provider 写入。
6. 只有取得 Provider 原始审计证据后，才可按现有双人签名流程处理 indeterminate 记录。
   恢复标准为 ready=1、连续失败=0、lookup_errors=0，并在约定稳定窗口内 drift=0。

Stable-window gate:

1. `insufficient_runs`、`insufficient_window` 或 `insufficient_selected` 表示证据量不足，继续
   观察，禁止通过降低阈值把未知状态改成 pass。
2. `failed_runs`、`drift_detected`、`lookup_errors`、`observation_gap` 或
   `observation_stale` 表示窗口不健康。先处理原因，再从新的连续稳定窗口重新评估。
3. 历史 hash-chain、嵌入报告或 Provider binding 校验失败视为证据完整性事故。保留原卷，
   不删除/改写记录，并使用独立日志和 Provider 审计导出调查。
4. `hold` 报告不能签名。签名验证失败、过期、错误 key ID 或 gate digest 不匹配时，废弃
   整个 gate bundle，不能只替换其中一个文件。

## Post-incident

Within one business day:

1. Record duration, impact, root cause, and affected job/event IDs.
2. Export relevant metrics screenshot or Prometheus range.
3. List compensations and DLQ replay IDs.
4. Decide whether to adjust alert thresholds, circuit breaker settings, or runbooks.
