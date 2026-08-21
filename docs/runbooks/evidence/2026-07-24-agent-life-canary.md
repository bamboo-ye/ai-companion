# Life Agent 灰度与恢复演练证据

日期：2026-07-24\
环境：本地 Docker Compose、PostgreSQL 18、Kafka 4.3、OpenRouter\
灰度范围：`AGENT_CHAT_MODULES=life`

## 验收目标

- 只有 `life` 模块进入 LangGraph Agent，其他模块保持旧链路。
- 自然语言“今天午饭花了50元”由模型选择账本工具。
- 写入前返回可审阅摘要，确认后恢复同一个 Agent Run。
- Agent 链路不创建 legacy generation job。
- Worker 或 Kafka 中断时请求不丢失，恢复后可以继续。
- 重复 Kafka 投递和死信重放不生成重复助手消息或账本写入。

可重复执行的验收脚本：

```sh
CANARY_TIMEOUT_SECONDS=240 sh scripts/run_agent_life_canary.sh
```

脚本每次创建隔离账号、生活助手角色和会话，验证公开响应不包含
`confirmation_token`，自动确认写入，并检查恰好一条 CNY 50 支出和一条助手
消息。

## 基线整链

Run `d88e90f7-ab80-41f4-abab-30b48a57c0bd` 完成了真实
OpenRouter 意图识别、账本预览、审批恢复和助手消息落库：

- 确认摘要包含“支出 / ¥50.00”，未暴露签名令牌。
- 最终状态 `completed`，助手消息数为 1，CNY 50 支出数为 1。
- legacy generation job 数为 0。

该轮同时发现：常驻 Worker 尚未用包含 Agent topic 白名单的新镜像重建，
Outbox 被旧 Relay 以 `event topic is not allowlisted` 重试并进入死信；Run
之所以完成，是启动时 reconciler 先于 Kafka 接管。

修复：

- Compose 中 `agent-worker` 显式依赖常驻 `worker`，保证 Outbox Relay 同时
  启动。
- `make agent-worker-up` 显式构建并启动 API、常驻 Worker 和 Agent Worker。
- reconciler 启动后先等待一个轮询周期，每周期最多接管一个到期 Run，避免与
  正常 Kafka 分发竞争。

## Agent Worker 停机恢复

停止 Agent Worker 后提交 Run
`8bc7f847-2f09-4fa8-9a02-5e8b56c25e5b`：

- 停机期间 Run 保持 `accepted`，legacy generation job 数为 0。
- 修复后的 Outbox Relay 在 `2026-07-24 15:25:33.354231Z` 发布到
  `agent.run.requested.v1`，partition `2`、offset `0`。
- Run 在 `2026-07-24 15:25:33.923088Z` 被恢复处理，最终 `completed`。
- 审批后的 `agent.run.resume.requested.v1` 由 Kafka 恢复，账本和助手消息
  均只写入一次。

## Kafka broker 中断恢复

停止 Kafka 后提交 Run
`98f9aeb4-14f9-45aa-b32f-abd80df7b711`：

- broker 停机时 Run 为 `accepted`，Outbox 为 `publishing` 且
  `published_at` 为空，legacy generation job 数为 0。
- Kafka 恢复后，原事件在 `2026-07-24 15:27:05.636105Z` 发布到 partition
  `2`、offset `1`。
- 同一 Run 随后完成审批恢复，最终 `completed`，助手消息和 CNY 50 支出各
  1 条。

## OpenRouter 免费模型回退

演练复现了两种免费模型不兼容：

- 返回成功响应但忽略 `tool_choice=required`，没有产生工具调用。
- 对工具调用选项返回 HTTP `422`。

处理策略调整为按以下顺序逐模型验证工具调用：

1. `openai/gpt-oss-20b:free`
2. `openrouter/free`
3. `inclusionai/ling-2.6-flash`

401/403 立即失败；400/404/422、网络错误、缺少工具调用、非法参数或选择越权
工具时继续下一个模型。所有模型失败后才把 Run 延后重试。

## 最终无故障 canary

Run `c6ca968e-f6b6-4b3e-b9e6-9da980c4f9de`：

| 事件 | UTC 时间 |
| --- | --- |
| accepted | 2026-07-24 15:39:18.370417 |
| Outbox published | 2026-07-24 15:39:18.395098 |
| Kafka consumer running | 2026-07-24 15:39:18.396160 |
| waiting_approval | 2026-07-24 15:39:26.000083 |
| approval_resolved | 2026-07-24 15:39:26.865452 |
| resume running | 2026-07-24 15:39:26.903950 |
| completed | 2026-07-24 15:39:27.784955 |

最终断言：

- requested 事件：partition `2`、offset `3`、首次发布成功。
- 首次领取事件没有 `source=reconciler`，证明由 Kafka consumer 领取。
- queued 事件数为 0。
- legacy generation job 数为 0。
- 助手消息数为 1。
- CNY 50 支出数为 1。

## 死信补偿

基线演练产生的历史死信事件
`d2105801-0d98-4ee1-b518-48e1e64bd9b9` 已通过
`POST /v1/ops/outbox/{event_id}/replay` 正式重放：

- 重放后发布到 `agent.run.requested.v1` partition `2`、offset `2`。
- Inbox 幂等记录数为 1。
- 助手消息仍为 1，没有重复业务写入。
- `outbox.replay` 补偿审计记录数为 1，actor 为
  `agent-canary-recovery`。

最终环境检查还发现 12 条由旧 Relay 在早期集成测试和本轮演练期间产生的
同类历史死信。关联 Run 中 11 条已被测试清理、1 条已经完成；全部通过同一
运维重放 API 补偿。重放结束后的 Agent topic 统计为：

- `dead_letter = 0`
- `pending/publishing = 0`
- `published = 23`
- 非终态 Agent Run 数为 0

## 结论

`life` 模块已具备继续本地灰度的条件。`work` 和 `companion` 仍未启用。
扩大灰度前仍需补齐 Agent Run 取消、总时限终止、运行指标和告警。
