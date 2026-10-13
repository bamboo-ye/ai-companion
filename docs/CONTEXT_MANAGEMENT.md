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
- Wiki 和记忆按完整内容分片，后台生成绑定用户、来源版本和索引规格的持久向量投影，写入独立 Qdrant 集合。查询只生成问题向量；先从 SQL 获取当前可读、已就绪的键，再进行过滤后的分组 ANN 检索。缺少索引或模型失败时保留词法结果并报告降级。
- `POST /v1/memories/{id}/corrections` 原子关闭旧记忆有效期，并创建带 `supersedes_id` 的新记忆。Web 编辑和 `memory_save_explicit` 的可选 `supersedes_id` 接入此机制，服务端校验归属。

## P2：有来源的 Wiki

迁移：PostgreSQL `000041_context_wiki`、MySQL `000030_context_wiki`。新增 `wiki_pages`、`wiki_versions`、`wiki_jobs`、`wiki_owners`、`wiki_feedback`。页面及来源结构以 JSON 保存，Markdown 是正文和导出格式，数据库是权威存储。

文档解析完成、删除与 Wiki 入队处于同一事务；迁移为已有 ready 文档入队。任务有 10 分钟租约、8 分钟编译截止时间、最多 5 次自动重试和退避，租约令牌阻止旧任务发布。同一所有者的任务串行，不同所有者可由多个 Worker 并行处理。用户重建或运营回填可重置失败任务。

每个文档片段都有页面，来源摘要包含完整链接索引；启用模型后补充主题、实体、决策页并校验引用 ID。相同类型/标题的多文档页面汇总为聚合页，同名字段值不同时保留两侧记录，标记需要核对时间和范围，不猜测哪一侧正确。抽取式冲突检测限于显式键值差异，复杂语义冲突仍需模型或人工核对。

聚合页依赖全部源文档。个人页只在本人范围读取；团队 API 先校验成员资格，再要求每个来源均已共享至同一工作空间。读取、搜索、历史访问均检查来源存在、ready 状态和版本。删除或未共享来源的页面立即不可见；来源版本变化的页面退出模型检索，等待重新编译。引用证明来源存在，不保证来源或模型解释本身正确。

Web「我的 Wiki」支持分类、搜索、阅读、原文引用、链接、Markdown 编辑与导出、版本历史和质量反馈。更新须提交当前 `version`，冲突返回 409。编译器保留人工修改；人工页来源变化后可显式选择“按新来源重建”，释放编辑保护。数据库保留旧修订，但过期来源的历史正文不会作为有效参考返回。

## 并行综合与证据仲裁（2026-10-01）

Wiki 语义综合按最多 24,000 UTF-8 字节分批处理全部片段，默认单文档并发 3、最多 64 批。每个片段的原文页面始终保留；超出批次数预算或部分失败时，来源页 `synthesis` 元数据报告覆盖范围，不能将保留原文误称为语义综合全部成功。成功分片缓存 24 小时，绑定用户、文档、来源版本、模型与输入；重建可复用成功分片，并重新校验引用和权限。

多来源显式字段差异可由证据仲裁标注等价、范围不同、明确版本替代、矛盾或待核对。Wiki 正文追加「来源差异核对」，保留原记录及冲突提示；较晚上传不能证明版本替代。模型上下文按完全相同的来源权限与版本依赖分组，失败或预算不足时保留争议。

创建或编辑记忆会对文本重合的同一用户活动记忆进行有界核对。结果只标注关系，双方仍保留；替代旧记忆继续要求显式纠正接口。关联内容变更、删除或被替代后，旧仲裁记录不再作为有效关系返回。记忆管理页在存在有效澄清项时展示相关记录及时间/情境问题；用户通过「纠正这条记录 → 保存纠正」明确提交，取消不修改数据。页面不展示完整内部裁决轨迹。

除原有迁移外，还需 PostgreSQL `000044_wiki_shards`、`000045_memory_arbitration`（MySQL `000033`、`000034`）。具体预算、共享模型许可和验证步骤见[并行执行](PARALLEL_AGENTS.md)与[证据仲裁](ARBITRATION.md)。

## P3：按需补查、缓存、反馈与灰度

工作模块新增 `work_wiki_search`、`work_wiki_read`、`work_wiki_follow_links`：搜索最多 5 个摘要；读取按 offset 连续分页并返回 `has_more`；链接最多 4 页。单次工具返回有 6000 Token 硬上限。内置 Router 在 8 次成功 Wiki 调用后停止提供这些工具；全部节点另受 Run 预算约束。Wiki 不替代真实账本、任务、计划工具或完整文档提取。

当前 Wiki 检索读取完整授权范围的词法及持久语义候选，已移除前 200 页截断，重排仍最多 24 页。统一知识入口也移除前 200 份文档和前 500 条记忆的搜索范围截断；结果数量及上下文预算仍有限，大规模权限枚举成本与召回质量需实测。这不改变记忆仲裁自身的有界候选策略。

检索缓存最多 256 项、TTL 2 分钟，键含用户、工作空间、问题、预算及当前可见页面/来源版本摘要。新增、编辑、删除和权限变化会失效正命中与空结果。内容向量由持久投影管理；缓存和投影均不授予权限，不能替代读取时的来源复验。

质量反馈绑定页面版本并持久化；负反馈安排重新核对来源。`GET /v1/ops/context/metrics` 返回当前进程的检索、缓存、编译和模型 Token 计数，以及数据库任务状态/反馈聚合。多副本进程指标应在监控层汇总。灰度指标包含影子查询差异与候选失败数。新适配器的 Token 计数不是费用账单，实际费用以服务商计费为准。

| `CONTEXT_INDEX_MODE` | 行为 |
|---|---|
| `legacy` | 仅原哈希索引（默认） |
| `shadow` | 新摄取双写；查询同时比较但返回旧索引；候选失败不阻塞原检索 |
| `semantic` | 优先语义集合；候选不可用或尚无结果时回退旧索引 |

索引规格包含模型、修订、维度、服务地址摘要、查询指令和预处理版本。Worker 自动扫描当前来源并回填新规格，候选加工失败会退避重试，不改变原文版本。`context_backfill.py` 只负责 Wiki 重编译；单文档 `reindex` 或后台「重建当前检索索引」用于索引恢复。回滚文档路由可切回 `legacy`；后台完整对账后清理超过 24 小时不再被当前来源引用的投影，旧命名空间迁移仍需独立处理。

## 统一知识入口与持久加工

「知识中心」通过 `knowledge-evidence-v1` 将资料、Wiki、记忆、产品指南接入搜索、分页阅读、引文校验、来源追踪和反馈。身份来自认证上下文，团队范围再次检查成员资格，个人记忆不进入团队搜索。命中标为未评估或有分歧，引用校验仅证明版本、正文摘要及引文存在，不证明结论蕴含关系。

文档先保存解析后的原文，再通过持久任务写入检索索引；界面区分原文就绪与检索加工状态。Wiki 和记忆也使用带租约、发布令牌、重试及对账的后台投影。更正、失效和删除后旧投影不再匹配当前来源。后台「知识质量」提供状态、失败重试、管理员索引重建和反馈复核；复核不自动修改事实。

除前述迁移外，需 PostgreSQL `000046_knowledge_projections` 或 MySQL `000035_knowledge_projections`。工作聊天新增 `work_knowledge_search/read`，继续遵守模块权限与工具预算。完整契约及部署边界见[知识工程实现](KNOWLEDGE_ENGINEERING_IMPLEMENTATION.md)，页面操作见[知识中心指南](KNOWLEDGE_CENTER.md)。历史时点查询、多跳图推理和视觉检索未实现。

有界辩论沿用仲裁协议 v2：`AGENT_DEBATE_MODE` 与 `CONTEXT_WIKI_DEBATE_MODE` 默认关闭，按配置启用后最多两轮独立判断与交叉复核，补证限只读且复验来源。记忆仍为单次仲裁。运行界面显示核验摘要，历史摘要不保证资料当前仍有效；详见[证据仲裁](ARBITRATION.md)。

## 配置与运行

```dotenv
CONTEXT_WIKI_ENABLED=true
CONTEXT_MODEL_BASE_URL=https://your-compatible-model-service/v1
CONTEXT_MODEL_API_KEY=...
CONTEXT_MODEL_TIMEOUT=30s
CONTEXT_SUMMARY_MODEL=your-summary-model
CONTEXT_EMBEDDING_MODEL=your-multilingual-embedding-model
CONTEXT_EMBEDDING_DIMENSIONS=1024
CONTEXT_EMBEDDING_REVISION=your-pinned-model-revision
CONTEXT_QUERY_INSTRUCTION=
CONTEXT_PREPROCESSING_VERSION=source-title-section-v2
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
make test-python  # pytest，包含并行与仲裁的函数式测试
pnpm -C web check
pnpm -C web build
# 专用测试实例，请勿指向生产库。
POSTGRES_TEST_DSN=... go test ./internal/persistence/postgresstore
QDRANT_TEST_URL=... go test ./internal/document -run TestSemanticQdrantIntegration
```

测试覆盖摘要保留、引用伪造拒绝、语义召回、记忆替代/删除、租户隔离、编译重放、CAS、人工编辑保护、多来源权限交集、冲突、缓存失效、租约/重试、HTTP 鉴权、节点预算和 PostgreSQL/Qdrant 协议。真实模型检索质量与成本须单独通过 live 回放评估。
