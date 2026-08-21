# PostgreSQL 运行态切流证据（2026-07-24）

## 范围

- 环境：本地 Docker 内测栈
- API/Worker 数据库：PostgreSQL 18
- 异步传输：Kafka 4.3
- 向量索引：Qdrant 1.18
- 旧 MySQL：只保留切流前快照，不再接受应用写入

## 迁移与 Store 验证

- PostgreSQL 迁移 `000001`～`000008` 已完成重复执行验证。
- 迁移器校验 16 张必需表，包括 `app.long_term_memories`、
  `eventing.outbox_events` 和 `agent.runs`。
- MySQL → PostgreSQL 核心复制结果：
  - users 47
  - user_devices 64
  - refresh_sessions 83
  - characters / persona_versions 50 / 50
  - conversations / messages 52 / 223
  - long_term_memories 4
  - generation_jobs / generation_job_events 100 / 530
  - model_usage_records 92
  - outbox_events 247
- 核心表主键集合完全一致，长期记忆 3 组状态聚合一致。
- 生活域、文档/技能域、治理/运营域的主键集合和关键聚合校验全部通过。
- PostgreSQL Store 真实集成测试通过：
  - `TestCoreStores`
  - `TestLifeStores`
  - `TestDocumentAndSkillStores`
  - `TestGovernanceStores`

## 运行态证据

API 启动日志：

```text
using persistent store driver=postgres
```

Worker 启动日志：

```text
durable workers ready database_driver=postgres
```

Docker 健康状态：API、Web、PostgreSQL、MySQL、Redis、Kafka、MinIO 均为
`running/healthy`，Worker 为 `running`，Qdrant 为 `running`。

## HTTP 与 Kafka 整链

使用切流后新注册账号执行：

1. 注册
2. 创建生活助手角色
3. 创建会话
4. 创建长期记忆
5. 解析并确认 50 元账本候选
6. 解析并确认次日上午九点提醒
7. 创建今日计划
8. 创建团队工作区
9. 发送聊天消息并等待异步生成

结果：

- 所有同步写入接口成功。
- generation job 尝试次数为 1，终态为 `completed`。
- 会话包含 1 条 user 和 1 条 assistant 消息。
- 对应 `chat.command.v1` Outbox 状态为 `published`。
- Kafka consumer `ai-companion-background-v1-chat` 已写入 Inbox。
- 新账号在 PostgreSQL 中计数为 1，在 MySQL 中计数为 0。

这证明运行态写入、事务 Outbox、Kafka 分发、Worker Inbox 幂等和结果落库均
使用 PostgreSQL。

## 文档摄取与向量索引

- 上传 UTF-8 文本文档后初始状态为 `queued`。
- Kafka Worker 完成摄取，文档状态为 `ready`。
- PostgreSQL 记录 `page_count=1`、`chunk_count=1`。
- Qdrant 按 `document_id` 查询得到 1 个 point，payload 中的 chunk id 与
  PostgreSQL 摄取结果一致。

文档问答当时被可靠性策略按设计降级：最近五分钟模型 p95 为
`15488 ms`，连续样本使控制器进入 L2，`use_full_rag=false`。这不是
PostgreSQL/Qdrant 写入失败；它是 OpenRouter 免费模型延迟触发的保护行为。
上线前应根据正式模型和 SLO 完成阈值校准。

## LangGraph Tool Gateway 整链

- API 已使用包含内部 Tool Gateway 的镜像重建并恢复为
  `running/healthy`。
- 使用 PostgreSQL 中持久化的 Agent Run 调用内部 prepare 接口，返回
  `requires_confirmation`、`risk_level=medium`、非空确认摘要和签名 token。
- prepare 阶段没有产生账本事实；commit 后产生一笔 `amount_minor=1200`
  的账本记录。
- 使用相同 `call_key` 和确认 token 重放 commit，返回同一个 entry id，没有
  产生第二笔记录。
- 整链验证完成后已删除 smoke Agent Run、账本候选和账本记录。
- 单元测试另行覆盖：缺少/错误服务令牌、模块越权、篡改 token、过期 token、
  非法 Run 输入和不可信身份字段。

## 回退边界

从本次切流验收首次注册成功开始，PostgreSQL 已产生 MySQL 不具备的新业务
数据。因此：

- 不得把 `APP_DATABASE_DRIVER` 直接改回 `mysql` 并宣称无损回退。
- 需要业务回退时，优先恢复 PostgreSQL 服务。
- 只有完成 PostgreSQL → MySQL 的反向差异迁移与聚合校验后，才允许短时切回
  MySQL。
- MySQL 下线前还需完成 PostgreSQL 备份恢复演练和稳定观察窗口。
