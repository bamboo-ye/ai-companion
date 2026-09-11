# Langfuse LLM / Agent 专项观测

## 职责边界

Langfuse 负责模型与 Agent 专项能力：Agent Trace、模型 Generation、节点 Observation、Token/成本、Prompt/模型/Agent 版本关联，以及运行质量 Score。

伴AI 自身仍是业务与治理事实源：PostgreSQL 保存 Agent Run、不可变 Agent/Prompt/模型配置版本、工具审批、恢复状态、评测与发布门禁；Prometheus/Grafana、系统日志、告警和事故中心负责基础设施与跨服务运维。Langfuse 不参与路由、工具执行、审批或恢复，服务不可用时不会使 Agent Run 失败。

当前映射如下：

| 伴AI 数据 | Langfuse 数据 |
| --- | --- |
| 一个 Agent Run | 加入请求 OTel Trace 的根 `agent` observation |
| 每次 OpenRouter 请求或回退尝试 | 一个 `generation` observation |
| `node_trace` 节点 | `span`、`tool` 或 `guardrail` observation |
| `response_validation.passed` | `response_contract_passed` boolean score |
| 最终状态 | `agent_completed` boolean score |
| 步数、模型成本 | `agent_steps`、`model_cost_usd` numeric score |
| Agent/模型配置版本与 fingerprint | 根 observation metadata/version |

正常请求的 Trace ID 由 HTTP 入口的 W3C `traceparent` 提取或生成，经 Outbox、Kafka、Go Agent Worker
和 Python Runtime 传播到 Langfuse；Langfuse 根 `agent` observation 使用同一 32 位十六进制 Trace ID，
并把 Go Worker 的 Span ID 作为父级。没有入站上下文的数据库对账任务才使用 Run ID 的确定性
Trace ID 作为回退，因此同一个持久化 Run 仍能稳定关联。管理端可复制 Trace ID 到日志中心和 Langfuse 检索。

## 配置

在服务端 Secret Manager 或本地 `.env` 配置：

```dotenv
LANGFUSE_ENABLED=true
LANGFUSE_PUBLIC_KEY=pk-lf-...
LANGFUSE_SECRET_KEY=sk-lf-...
LANGFUSE_BASE_URL=https://cloud.langfuse.com
LANGFUSE_SAMPLE_RATE=1
LANGFUSE_CAPTURE_CONTENT=false
LANGFUSE_TRACING_ENVIRONMENT=staging
LANGFUSE_RELEASE=2026.09.0
LANGFUSE_FLUSH_AT=20
LANGFUSE_FLUSH_INTERVAL=5
LANGFUSE_TIMEOUT_SECONDS=3
```

`LANGFUSE_PUBLIC_KEY` 和 `LANGFUSE_SECRET_KEY` 必须成对存在；启用但缺少任一密钥时，API/Worker 配置校验和 Python Agent Runtime 都会拒绝启动。正式环境的 `LANGFUSE_BASE_URL` 必须使用 HTTPS。

`LANGFUSE_SAMPLE_RATE` 范围为 `0..1`。先在预发布以 `1` 验证，再按流量和成本调低；未采样不影响本地 Agent Run 证据。

## 隐私策略

`LANGFUSE_CAPTURE_CONTENT=false` 是默认值。此模式仅发送：

- 消息长度、上下文键名，不发送用户消息正文或上下文值；
- 模型、角色、超时、Token、成本、错误分类和响应长度；
- 哈希后的用户、会话与角色引用；
- Agent、模型配置和图版本标识；
- 节点名、状态、顺序和本地记录的耗时。

只有完成单独的隐私、区域、留存和访问权限评审后，才能设置 `LANGFUSE_CAPTURE_CONTENT=true`。开启后仍会递归屏蔽 Secret/Password/Token/Credential 字段，并对 Bearer Token、常见密钥格式和电子邮箱做替换，对长内容做截断。该防护不能替代上游数据最小化。

项目没有启用 LangGraph/LangChain 自动 Callback，因为完整 Graph State 可能包含附件正文、对话历史和工具结果；当前使用显式、安全字段的手动 instrumentation。

## 部署

Langfuse Cloud 和独立自托管实例使用同一组环境变量。本仓库不内嵌 Langfuse 自托管 Compose：Langfuse v4 自托管需要 Web、Worker、PostgreSQL、ClickHouse、Valkey/Redis 和对象存储，并且默认 Web 端口与伴AI Web 的 `3000` 冲突。应按 Langfuse 官方部署文档单独部署、备份和升级，然后把 HTTPS 地址填入 `LANGFUSE_BASE_URL`。

更新配置后重建并启动：

```bash
docker compose --env-file .env -f deploy/compose/compose.yml --profile app --profile agent-runtime up -d --build api agent-worker web
```

SDK 只安装在 Agent 镜像内。API 仅暴露经过脱敏的集成状态与 Trace 引用，不持有或返回 Langfuse Secret。

## 验收

1. 打开 `/admin`，在“运行观测”确认 Langfuse 显示“已接入”，采样率和正文策略正确。
2. 从普通用户端执行一条会触发 Agent 的请求。
3. 从请求响应的 `traceparent` 或 `X-Trace-ID` 取出 32 位 Trace ID；在 Agent Run 详情确认 `Langfuse Trace` 使用同一个 ID。
4. 在日志中心和 Langfuse 中检索该 Trace ID，确认 HTTP、Kafka/Worker 日志及根 `agent`、`llm.<role>` generation、`node.<name>` observations 和 scores 能关联。
5. 比较 Langfuse generation 的 Token/成本与管理端本地模型调用记录；Langfuse 不是账单事实源，差异以本地不可变 usage 记录为准并调查。
6. 临时把 `LANGFUSE_BASE_URL` 指向不可达地址运行一次，验证 Agent 仍完成；恢复正确地址后重启 Worker。不要在正式环境用真实用户正文执行故障演练。

本地自动验证：

```bash
workers/python/.venv/bin/python -m pytest -q workers/python/tests/test_langfuse_observability.py
go test ./internal/platform/config ./internal/httpserver
make release-check
```

## 故障语义

- 创建、更新或结束 Langfuse observation 的异常均被隔离，不替换模型或 Agent 的原始结果/异常。
- 常驻 Agent Runtime 使用 Langfuse SDK 的异步缓冲；进程正常退出时执行 `shutdown()`。
- `initialized=true` 表示本地 observation 已建立，不代表该 Trace 被采样或已在远端持久化；采样与远端交付状态以 Langfuse 为准。
- Langfuse 关闭或不可达时，管理端本地节点轨迹、模型调用、成本、日志、告警、事故和评测仍然可用。

## 官方参考

- [Python SDK v4 与 OpenTelemetry](https://langfuse.com/docs/observability/sdk/overview)
- [Observation 类型](https://langfuse.com/docs/observability/features/observation-types)
- [确定性 Trace ID 与分布式追踪](https://langfuse.com/docs/observability/features/trace-ids-and-distributed-tracing)
- [SDK Scores](https://langfuse.com/docs/evaluation/evaluation-methods/scores-via-sdk)
- [数据屏蔽](https://langfuse.com/docs/observability/features/masking)
- [自托管 Docker Compose](https://langfuse.com/self-hosting/deployment/docker-compose)
