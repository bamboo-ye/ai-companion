# Agent Run 取消、超时与观测

## 运行语义

- API 创建 Agent Run 时以 `AGENT_RUN_TIMEOUT` 计算一次
  `deadline_at`。重试、Kafka 重投、审批等待和恢复均不延长截止时间。
- `POST /v1/agent-runs/{run_id}/cancel` 要求用户鉴权与
  `Idempotency-Key`。排队或等待审批的运行直接进入 `cancelled`；执行中的
  运行先进入 `cancel_requested`。
- Agent Worker 每隔 `AGENT_CONTROL_POLL_INTERVAL` 读取数据库状态。发现
  `cancel_requested` 后取消 Python 子进程，再以当前 revision 完成
  `cancelled`。
- Worker 与 reconciler 均可将到期运行终止为 `timed_out`。所有完成、暂停、
  失败和延后写入同时校验状态、租约、revision 与 `deadline_at`，因此迟到的
  执行结果不能覆盖取消或超时。
- 未产生可审计 Graph 结算的临时运行错误最多执行
  `AGENT_WORKER_MAX_ATTEMPTS` 次。基础退避、上限和确定性抖动分别由
  `AGENT_WORKER_RETRY_DELAY`、`AGENT_WORKER_RETRY_MAX_DELAY`、
  `AGENT_WORKER_RETRY_JITTER_PERCENT` 控制；429/503 优先采用有效的
  `Retry-After`。401/403、其他永久 4xx 及运行协议/Graph 契约错误不重试。
- 已有模型调用账本的供应商失败由 Graph 在候选回退耗尽后安全结算，不整体
  重跑终态节点，避免重复计费。任何运行级延期若会到达或越过 `deadline_at`，
  立即进入 `timed_out`。

## 指标

`GET /metrics` 提供：

- `ai_companion_agent_runs{status=...}`：活跃状态当前数量；终态为最近五分钟
  数量。
- `ai_companion_agent_run_duration_p95_seconds`：最近五分钟 Agent Run
  端到端 P95 时长。
- `ai_companion_agent_execution_retries_recent{outcome=...}`：最近五分钟执行重试
  的 `scheduled`、`recovered`、`exhausted` 和 `deadline_exhausted` 数量；只读取
  标记为 `retry_kind=execution` 的持久化事件，并兼容标记上线前带 `reason` 的
  延期事件。
- `ai_companion_agent_execution_retry_recovery_ratio`：最近五分钟已结算重试运行中
  恢复完成的比例，分母为恢复、次数耗尽和总时限耗尽三类终态；没有已结算样本时
  为 0。
- `ai_companion_queue_lag` 和 `ai_companion_oldest_job_age_seconds`：已包含
  Agent 可领取或租约过期运行。
- `ai_companion_model_error_ratio`：已合并 Agent `failed`、`timed_out`
  终态。

建议告警：`cancel_requested` 持续大于 0 超过两个租约周期；
`timed_out` 五分钟增量大于 0；scheduled 达到 20；exhausted 与
deadline_exhausted 合计达到 3；有至少 5 个已结算重试样本时恢复率低于 80%；
或 Agent P95 超过 `AGENT_RUN_TIMEOUT` 的 80%。对应 Prometheus 规则已经由
`make validate-observability` 做结构、阈值、最小样本保护和 Dashboard 引用门禁。
发布到生产前可进一步运行 `make observability-drill`：它用正式规则时长验证零样本
不误报、三类 retry 告警触发、Alertmanager 活动状态、Webhook firing/resolved
通知和退出清理，全程不注入真实 Run 或模型流量。
灰度发布还应在流量切换前后分别运行 `make capture-observability`，再用
`make eval-observability-release` 对比。该门禁同时检查绝对 SLO、相对增量和恢复率
最小样本条件；快照只保留白名单数值与来源哈希，派生终态计数和恢复率若与原始样本
不一致会被当作无效输入拒绝。
`make observability-release-gate` 将上述步骤编排为单次决策。默认
`api-health.v1.json` 只读取 health/ready；显式选择 `agent-direct.v1.json` 才会创建
一次性测试用户并调用简答链路。Canary 非零、超时或后置指标违规均返回 rollback，
但工具本身不执行部署回滚，避免在没有外部部署授权时改变流量或镜像。

Worker 同时输出四类 JSON 观测事件，用于拆分端到端耗时：

- `Agent Python pool warmup`：`requested`、`ready`、`duration_ms` 和
  `success`，确认进程在流量进入前完成协议与 Graph 身份握手。
- `Agent run dispatch completed`：`queue_wait_ms`、
  `execution_duration_ms`、`processed` 和 `outcome`，区分排队、认领与执行。
- `Agent Python execution`：`execution_mode`、`pool_wait_ms`、`duration_ms`、
  `model_calls`、`model_duration_ms`、`max_model_attempt_timeout_ms`、
  `runtime_overhead_ms`、`result_status`、`cold_start`、`recycled`、`success`
  和稳定错误码，
  区分池等待、模型耗时、单次模型超时上限、本地 Graph/序列化开销、冷启动及
  异常回收。
- `Agent execution retry scheduled`：`attempt`、`maximum_attempts`、
  `delay_ms`、`retry_after_ms`、`base_delay_ms`、`max_delay_ms`、
  `jitter_percent`、`policy`、`provider_status`、`error_code` 和
  `deadline_remaining_ms`，用于重建调度决策并识别限流、退避放大和重试风暴。
- `Agent tool task wake`：`event_age_ms`、`wake_duration_ms`、
  `wake_latency_ms` 和 `awakened_runs`，验证异步 Skill 终态事件能直接唤醒 Agent，
  而不是等待下一轮退避探测。

发布门禁使用版本化基线
`evals/agent/baselines/performance.v2.json`。默认样本验证评估器本身：

```bash
make eval-agent-performance
```

灰度环境应从 Worker 启动日志开始截取至少 12 个已完成运行的纯 JSON 日志后执行
真实评估；基线要求至少一次成功预热、5 个 `direct` 简答样本和 4 个暖进程简答
样本、4 个真实唤醒样本，以及 12 个 `processed` 调度与 Python 执行按 run id
配对的样本。重复投递
产生的 `not_claimed` 只进入分类统计，不参与延迟计算。门禁分别限制预热耗时、
队列 P95、Python 池等待 P95、整体执行 P95、含冷
启动的简答 P95、暖进程简答 P95、简答模型耗时 P95、简答本地运行开销 P95、
简答单次模型超时上限 P95、工具终态到 Agent 可调度的唤醒 P95、错误率、冷启动率
和进程回收率：

```bash
AGENT_PERFORMANCE_LOG=/path/to/agent-worker.jsonl make eval-agent-performance
```

可先运行无写工具的一次性直达链路验收；默认连续执行两次，以确认第二次请求复用
已启动的 Python 进程：

```bash
make agent-direct-canary
```

结果固定写入 `artifacts/agent-eval/performance.json` 和
`artifacts/agent-eval/performance.xml`。任一硬阈值越界即返回非零，不自动修改已
评审基线。

重试闭环使用独立的 `retry.v1` 基线，避免受控失败样本被普通性能门禁的零错误率
要求误判。默认样本覆盖 429、503 和连续 408；评估器按日志顺序重建失败、延期和
最终完成链路，并硬性拒绝重复 attempt、越过最大尝试数、退避区间不符、策略配置
漂移、延期越过 deadline、没有前置失败的孤立重试、未恢复运行和重复完成：

```bash
make eval-agent-retry
AGENT_RETRY_LOG=/path/to/complete-controlled-failure.jsonl make eval-agent-retry
make postgres-test-agent-retry
AGENT_RETRY_TEST_COUNT=5 make postgres-test-agent-retry
```

报告固定写入 `artifacts/agent-eval/retry.json` 和
`artifacts/agent-eval/retry.xml`。真实日志必须包含 Worker ready 事件，并从受控
失败前一直截取到该运行进入 `completed`；截断窗口会按未恢复运行失败，而不会被
当作成功。`postgres-test-agent-retry` 进一步在真实 PostgreSQL 状态机中验证
`revision`、`available_at`、执行重试事件、恢复/耗尽指标和最终助手消息唯一性；
CI 的 Compose 门禁会执行该验收。

真实演练证据见
[`evidence/2026-07-25-agent-run-control.md`](evidence/2026-07-25-agent-run-control.md)
和
[`evidence/2026-08-09-agent-performance.md`](evidence/2026-08-09-agent-performance.md)。

## 验收查询

```sql
SELECT status, COUNT(*)
FROM agent.runs
GROUP BY status
ORDER BY status;

SELECT id, status, revision, deadline_at, completed_at, error_code
FROM agent.runs
WHERE status IN ('cancel_requested', 'timed_out')
ORDER BY updated_at DESC
LIMIT 20;
```
