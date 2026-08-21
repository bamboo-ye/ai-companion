# Agent Run 取消、超时与指标验收证据

验收日期：2026-07-25（Asia/Shanghai）\
环境：本地 Docker PostgreSQL/Kafka/Redis/Qdrant/API/Worker/Agent Worker\
灰度范围：`AGENT_CHAT_MODULES=life`

## 数据库迁移

`migrate-postgres` 输出：

```text
skip 000009_agent_resume_resolution.up.sql
applied 000010_agent_run_control.up.sql
validated 16 required tables
```

新迁移为 `agent.runs` 增加固定 `deadline_at` 和 `timed_out` 状态约束。

## 运行中取消

命令：

```bash
CANARY_TIMEOUT_SECONDS=60 sh scripts/run_agent_control_canary.sh
```

结果：

```json
{
  "run_id": "7058cd1e-78c7-428f-be95-d798384a24cd",
  "running_cancel_status": "cancel_requested",
  "final_status": "cancelled",
  "assistant_messages": 0
}
```

PostgreSQL 核验：

```text
cancelled|4|cancelled
accepted,running,cancel_requested,cancelled
0
```

证明取消先持久化为 `cancel_requested`，Worker 观察后结束执行器并用新 revision
完成 `cancelled`；迟到执行结果没有写入助手消息。

## 固定总时限

命令：

```bash
CANARY_DEADLINE_SECONDS=4 CANARY_TIMEOUT_SECONDS=90 \
  sh scripts/run_agent_timeout_canary.sh
```

结果：

```json
{
  "run_id": "22fbc1d3-56ed-4e52-8c56-f60e194dd363",
  "final_status": "timed_out",
  "event_types": ["accepted", "timed_out"],
  "assistant_messages": 0
}
```

该演练先停止 Agent Worker，在 API 原子创建消息/Agent Run/Outbox 后将测试运行
设为四秒固定截止时间，再恢复 Worker；到期运行由 reconciler 原子终止，未产生
助手消息。运行中 deadline 取消执行器的路径由 Go 并发单元测试覆盖，真实
PostgreSQL Store 测试同时覆盖超时、取消、revision 冲突和终态不可再次领取。

## 指标

演练后 `/metrics`：

```text
ai_companion_agent_runs{status="accepted"} 0
ai_companion_agent_runs{status="queued"} 0
ai_companion_agent_runs{status="running"} 0
ai_companion_agent_runs{status="waiting_approval"} 0
ai_companion_agent_runs{status="cancel_requested"} 0
ai_companion_agent_runs{status="cancelled"} 1
ai_companion_agent_runs{status="timed_out"} 1
ai_companion_agent_run_duration_p95_seconds 45.389
```

最终数据库查询未发现非终态 Agent Run，所有 `timed_out` 记录均有
`completed_at`。

## 回归

- `go test ./...`：通过。
- `go vet ./...`：通过。
- PostgreSQL Store Docker 集成测试：`PASS`。
- Python Worker：`31 passed`。
- Web ESLint、TypeScript 与 Docker Next.js production build：通过；新 Web
  容器健康检查为 `running/healthy`。
- OpenAPI YAML、Compose 配置、`git diff --check`：通过。
- API、Agent Worker、常驻 Worker、PostgreSQL、Kafka 等容器均恢复运行；API
  健康检查通过。
