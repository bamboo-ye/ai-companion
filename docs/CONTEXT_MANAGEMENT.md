# 上下文管理（P0–P3）

已实现统一会话快照、结构化摘要、可配置语义检索、Wiki 编译与阅读、按需补查、来源失效、缓存、反馈和索引灰度。Go 管理权威数据与权限，Python Agent 使用同一快照契约。默认无需新增模型服务即可运行；语义能力通过环境变量启用。

## 分层与预算

| 层 | 实现与边界 |
|---|---|
| 当前请求 | 独立保留，不在历史中重复，不截断当前指令 |
| 近期会话 | 完整消息组；Go 默认 6000 估算 Token |
| 滚动摘要 | 目标、约束、事实、已完成、待办、背景；原消息 ID、发言角色；默认 1200 Token |
| 长期记忆 | 有效期、用户隔离、固定项；语义相似度、关键词、时效、重要度融合；完整事实装入 1200 Token |
| Wiki | Markdown 页、来源版本、原文片段、页码、链接、冲突、版本历史；按需查询 |
| 原始资料 | Source IR 和附件分轮全量提取；完整性任务继续经过覆盖率门禁 |

快照为 `conversation-context-v1`，普通聊天与 Agent 共用。摘要和记忆是独立的参考资料消息，不具备修改工具权限的权力。执行观察也以数据消息呈现，引用正文不能升级为系统指令。

Python 按节点选择完整历史消息组：Router/Assessor 2000、Repairer 3000、Planner 4000、Composer/Responder 6000 Token。最新组与摘要保留，省略数量写入 manifest。当前请求、工具 schema 和全文附件轮次不受该选择器裁切。

每次调用检查完整请求的 UTF-8 字节 Token 上界、输出预算和 1024 安全余量，再检查 Agent Run 调用次数、累计 Token 与成本预算。普通聊天窗口由 `MODEL_CONTEXT_WINDOW` 配置；Agent 从模型目录取本角色及回退模型的最小窗口。字节上界是保守保护，可能提前拒绝请求，不代表真实 Token 用量。

## P1：摘要、记忆与语义检索

- 摘要模型返回带来源 ID 的结构化事实，未知来源或无效结构触发抽取式回退。回退按完整句子分类并优先保留约束，避免过去只取消息前 240 字、按尾部截取摘要的问题。容量不足记录 `omitted`；摘要不是无损存储。
- `/embeddings` 适配器支持可配置多语模型、维度、批处理和向量格式校验。中文能力须通过部署方的回放评估。
- 文档继续使用 Qdrant 稀疏+稠密 RRF；语义稠密向量使用独立集合，不与旧哈希向量混写。可选 `/rerank` 使用 `relevance_score`，不可用时回退并标记降级。
- 记忆向量缓存键包含用户、记忆 ID、内容哈希和模型版本；Wiki 向量缓存包含页面版本。所有结果仍经过当前权威数据的可见性和有效期过滤。
- `POST /v1/memories/{id}/corrections` 原子关闭旧记忆有效期，并创建带 `supersedes_id` 的新记忆。Web 编辑和 `memory_save_explicit` 的可选 `supersedes_id` 接入此机制，服务端校验归属。

## P2：有来源的 Wiki

迁移：PostgreSQL `000041_context_wiki`、MySQL `000030_context_wiki`。新增 `wiki_pages`、`wiki_versions`、`wiki_jobs`、`wiki_owners`、`wiki_feedback`。页面及来源结构以 JSON 保存，Markdown 是正文和导出格式，数据库是权威存储。

文档解析完成、删除与 Wiki 入队处于同一事务；迁移为已有 ready 文档入队。任务有 10 分钟租约、8 分钟编译截止时间、最多 5 次自动重试和退避，租约令牌阻止旧任务发布。同一所有者的任务串行，不同所有者可由多个 Worker 并行处理。用户重建或运营回填可重置失败任务。

每个文档片段都有页面，来源摘要包含完整链接索引；启用模型后补充主题、实体、决策页并校验引用 ID。相同类型/标题的多文档页面汇总为聚合页，同名字段值不同时保留两侧记录，标记需要核对时间和范围，不猜测哪一侧正确。抽取式冲突检测限于显式键值差异，复杂语义冲突仍需模型或人工核对。

聚合页依赖全部源文档。个人页只在本人范围读取；团队 API 先校验成员资格，再要求每个来源均已共享至同一工作空间。读取、搜索、历史访问均检查来源存在、ready 状态和版本。删除或未共享来源的页面立即不可见；来源版本变化的页面退出模型检索，等待重新编译。引用证明来源存在，不保证来源或模型解释本身正确。

Web「我的 Wiki」支持分类、搜索、阅读、原文引用、链接、Markdown 编辑与导出、版本历史和质量反馈。更新须提交当前 `version`，冲突返回 409。编译器保留人工修改；人工页来源变化后可显式选择“按新来源重建”，释放编辑保护。数据库保留旧修订，但过期来源的历史正文不会作为有效参考返回。

## P3：按需补查、缓存、反馈与灰度

工作模块新增 `work_wiki_search`、`work_wiki_read`、`work_wiki_follow_links`：搜索最多 5 个摘要；读取按 offset 连续分页并返回 `has_more`；链接最多 4 页。单次工具返回有 6000 Token 硬上限。内置 Router 在 8 次成功 Wiki 调用后停止提供这些工具；全部节点另受 Run 预算约束。Wiki 不替代真实账本、任务、计划工具或完整文档提取。

当前 Wiki 检索扫描已授权页面的词法候选，语义候选最多 200 页、重排最多 24 页；大型知识库应以实际数据集评估召回与延迟后逐步放量。

检索缓存最多 256 项、TTL 2 分钟，键含用户、工作空间、问题、预算及当前可见页面/来源版本摘要。新增、编辑、删除和权限变化会失效正命中与空结果。向量缓存最多 2048 项、TTL 15 分钟；缓存不授予权限。

质量反馈绑定页面版本并持久化；负反馈安排重新核对来源。`GET /v1/ops/context/metrics` 返回当前进程的检索、缓存、编译和模型 Token 计数，以及数据库任务状态/反馈聚合。多副本进程指标应在监控层汇总。灰度指标包含影子查询差异与候选失败数。新适配器的 Token 计数不是费用账单，实际费用以服务商计费为准。

| `CONTEXT_INDEX_MODE` | 行为 |
|---|---|
| `legacy` | 仅原哈希索引（默认） |
| `shadow` | 新摄取双写；查询同时比较但返回旧索引；候选失败不阻塞原检索 |
| `semantic` | 优先语义集合；候选不可用或尚无结果时回退旧索引 |

集合名附加模型名称/维度的哈希后缀。开启或更换模型后执行回填脚本，按游标每批入队 200 文档，候选重建失败会重试；不改变原始文档版本。Wiki 开关与索引回填独立。回滚切回 `legacy` 并重启对应进程，保留新集合供排查，不自动删除索引。

## 配置与运行

```dotenv
CONTEXT_WIKI_ENABLED=true
CONTEXT_MODEL_BASE_URL=https://your-compatible-model-service/v1
CONTEXT_MODEL_API_KEY=...
CONTEXT_MODEL_TIMEOUT=30s
CONTEXT_SUMMARY_MODEL=your-summary-model
CONTEXT_EMBEDDING_MODEL=your-multilingual-embedding-model
CONTEXT_EMBEDDING_DIMENSIONS=1024
CONTEXT_RERANK_MODEL=your-reranker
CONTEXT_INDEX_MODE=shadow
```

模型名留空会禁用对应远端能力。服务需要支持相应端点；只提供聊天接口的地址不能直接用作 reranker。API/Worker 使用相同配置。先运行数据库迁移，再启动 API/Worker；生产配置要求 HTTPS 模型地址。

```sh
# 使用环境变量 CONTEXT_OPERATOR_TOKEN；脚本不输出凭证。
python3 scripts/context_backfill.py --base-url http://localhost:8080
# 中断后可传 --cursor 恢复。

# 离线基线，不代表真实模型质量。
go run ./cmd/context-eval --offline
# 配置真实模型后运行回放并设置发布门槛。
go run ./cmd/context-eval --min-recall 0.8
```

`evals/context/retrieval.v1.json` 为合成样例，包含中文、跨语言、改写、删除后不召回。上线门槛应补充经授权的实际会话、硬负例和人工相关性标签。程序输出逐例结果、排名、延迟和实际模型用量；未配置真实模型时不会伪装为 live 测试。

## API

- `GET /v1/wiki/pages`；`POST /v1/wiki/search`
- `GET/PATCH /v1/wiki/pages/{id}`；`GET .../links`、`.../versions`、`.../export`
- `POST .../feedback`、`.../rebuild`（页面显式释放人工编辑保护）
- `POST /v1/documents/{id}/wiki/rebuild`（保留人工编辑）；`POST .../reindex`
- 团队只读：`/v1/workspaces/{workspace_id}/wiki/pages`、`.../search`、`.../pages/{id}`、`.../pages/{id}/links`
- 运维：`POST /v1/ops/context/backfill`；`GET /v1/ops/context/metrics`

快照仍是单个 Run 的历史输入。删除记忆或文档阻止新的召回和 Wiki 读取，但不抹除已送入模型、历史聊天、执行观察和检查点中的文本；这些历史副本需要单独的数据保留/删除策略。有限摘要也不能保证无限对话中的所有早期事实均被保留，`omitted` 和来源 ID 用于明确该边界。

## 验证

```sh
go test ./...
PYTHONPATH=workers/python/src workers/python/.venv/bin/python -m unittest discover -s workers/python/tests
pnpm -C web check
pnpm -C web build
# 专用测试实例，请勿指向生产库。
POSTGRES_TEST_DSN=... go test ./internal/persistence/postgresstore
QDRANT_TEST_URL=... go test ./internal/document -run TestSemanticQdrantIntegration
```

测试覆盖摘要保留、引用伪造拒绝、语义召回、记忆替代/删除、租户隔离、编译重放、CAS、人工编辑保护、多来源权限交集、冲突、缓存失效、租约/重试、HTTP 鉴权、节点预算和 PostgreSQL/Qdrant 协议。真实模型检索质量与成本须单独通过 live 回放评估。
