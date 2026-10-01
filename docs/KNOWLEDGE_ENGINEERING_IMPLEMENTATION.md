# 知识工程集成实现与验证

本说明记录 2026 年 9 月 30 日完成的统一知识主链路、配置和验证结果。设计依据见 [知识工程评审](KNOWLEDGE_ENGINEERING_REVIEW.md)。本次交付将资料、Wiki、个人记忆、产品指南接入共同的检索、阅读、版本校验、来源追踪和反馈服务，并让 Wiki 与记忆的内容向量在后台持久化生成。

Qdrant 检索和持久任务已纳入主链路。真实语料盲测、大规模容量验收、时态图查询与视觉检索仍需独立开展；本次功能测试不代表这些质量目标已经达标。

## 已实现的能力

| 能力 | 实现与约束 |
|---|---|
| 统一入口 | `internal/knowledge` 定义来源契约；`internal/knowledgebridge` 适配四类现有来源。用户侧“知识中心”提供统一搜索、原文分页阅读、依赖来源和分类反馈。 |
| 范围隔离 | 用户身份来自认证上下文；团队范围重新验证成员身份。个人记忆不能进入团队检索；工作文档和 Wiki 工具限定工作模块。读取与引用校验重新检查权威存储。 |
| 版本引用 | `knowledge-evidence-v1` 引用包含类型、来源 ID、版本、片段 ID、完整正文摘要和位置。返回预览不会改变全文摘要；继续阅读遇到旧版本会失败。 |
| 完整范围 | 文档和记忆通过稳定 ID 游标读取权限范围，移除前 200 份文档、前 200 页 Wiki 和前 500 条记忆的检索上限。查询结果数量和模型上下文仍受预算限制。 |
| 检索说明 | 返回策略版本、候选数、索引规格、遗漏数、降级原因和耗时。检索命中标记为 `unassessed`；存在分歧时标记为 `disputed`，不会把命中自动标为事实核验成功。 |
| 原文校验 | 文档向量命中在进入重排前与当前数据库片段逐字比对。统一入口再次检查来源版本；引文校验要求来源仍可访问、版本及全文摘要一致、引文确实存在。 |
| 后台向量 | Wiki 和记忆按完整文本分片，生成版本绑定的持久投影，并写入独立 Qdrant 集合。查询先从 SQL 获取当前可读且已就绪的投影键，再执行过滤后的分组 ANN 检索，避免长来源占满候选。查询只生成问题向量；缺少索引或模型失败时保留词法召回并报告降级。 |
| 文档索引 | 后台 Worker 先提交解析结果和原文，再由持久任务写入文档索引。任务按来源版本和索引规格幂等生成，大文档自动续租；文档、语义投影、对账三个循环独立运行。界面区分原文就绪与检索加工状态。 |
| 任务恢复 | PostgreSQL 与 MySQL 均提供任务表、租约、原子领取、发布令牌校验、失败退避和重试。工作进程在调用模型前与发布前两次核对当前来源。 |
| 更新失效 | 更正、失效和删除后，旧版本投影不再匹配当前来源。后台每分钟从权威记录发现工作，完整对账成功后清理超过 24 小时未被当前来源引用的投影，先删除对应 Qdrant 向量再删除 SQL 行。远端删除失败时保留 SQL 任务以便重试。文档删除覆盖当前配置命名空间内的旧语义集合，迟到索引任务在发布后发现删除会补偿清理。 |
| 对话接入 | 新增 `work_knowledge_search` 和 `work_knowledge_read`；沿用现有调用预算、工作模块限制和只读参考结果。旧工具继续保留。上下文中的记忆增加来源版本。 |
| 检查点兼容 | Python 证据适配器识别统一契约中的原始资料与产品指南。Wiki 摘要和个人断言不会直接升级为原始文档证据，需要读取依赖原文。 |
| 反馈复核 | 反馈绑定认证用户、来源版本及检索策略，持久保存；后台“知识质量”显示加工状态、失败重试、管理员重建索引和待复核队列。复核记录处理人和结论，不自动改写事实。普通查看角色不能复核或重试。 |

## 接口与错误语义

用户接口均要求登录，响应禁止共享缓存。

| 接口 | 用途 |
|---|---|
| `POST /v1/knowledge/search` | 查询四类来源；可选 `source_kinds`、`document_ids`、`workspace_id`、`limit`、`budget`。 |
| `POST /v1/knowledge/read` | 按 `ref`、`offset` 阅读；单次最多 2400 字符，返回 `next_offset` 和 `has_more`。 |
| `POST /v1/knowledge/validate` | 校验带版本和内容摘要的引文；最多 48 条。只证明来源一致和引文存在，不证明结论蕴含关系。 |
| `POST /v1/knowledge/trace` | 重新校验直接来源与依赖原文；一次最多返回 49 个引用。 |
| `POST /v1/knowledge/feedback` | 提交有帮助、错误、过期、缺失、引用不支持或权限反馈。 |
| `GET /v1/ops/knowledge/metrics` | 运维查看任务状态、索引规格及原有上下文指标。 |
| `POST /v1/ops/knowledge/retry` | 支持人员或管理员重试失败的投影任务。 |
| `POST /v1/ops/knowledge/rebuild` | 管理员重建当前规格的知识索引，用于索引丢失或修复；会产生重新加工成本。 |
| `GET /v1/ops/knowledge/feedback` | 支持人员或管理员读取最早的 100 条待复核反馈。 |
| `POST /v1/ops/knowledge/feedback/{feedback_id}/resolve` | 保存复核结论；重复处理返回冲突。 |

参数不合法或当前未支持的历史时点、全局推理模式返回 422；范围权限错误返回 403；来源删除或不可用返回 404；版本、引文或处理状态冲突返回 409。基础设施不可用返回 503。检索部分降级通过成功响应的 `manifest.degraded` 明示。

指定 `document_ids` 时默认只搜索文档及全部依赖均位于该范围内的 Wiki。显式混入记忆或指南类型会被拒绝，避免附件任务越出指定资料范围。

## 存储与上线顺序

新增 PostgreSQL 迁移 `000046_knowledge_projections` 和 MySQL 迁移 `000035_knowledge_projections`，包含 `knowledge_projections` 与 `knowledge_feedback`。迁移已在独立空库中连同全部历史迁移执行通过。9 月 30 日开发验证阶段没有变更正在运行的应用数据库；10 月 1 日按用户要求完成了下述本地部署。

1. 备份并按现有发布流程应用对应数据库迁移，再启动新版 API 和后台 Worker。持久化 Store 的编译期接口要求包含投影和反馈实现，避免生产环境静默落入内存存储。
2. 若启用语义检索，配置已有模型服务地址、模型与维度，以及新增的 `CONTEXT_EMBEDDING_REVISION`、`CONTEXT_QUERY_INSTRUCTION`、`CONTEXT_PREPROCESSING_VERSION`。最后一个参数默认 `source-title-section-v2`。
3. 文档、Wiki 和记忆由 Worker 自动扫描回填。模型修订、维度、服务地址、查询指令或预处理规则改变会生成不同索引规格。服务商不提供固定修订时，运营者须维护可区分的部署修订；留空会显示 `unversioned`，不能据此证明底层模型没有变动。
4. 文档保留已有 `legacy / shadow / semantic` 路由，后台投影负责新规格的自动回填，影子模式下的候选写入失败也会重试。`POST /v1/documents/{document_id}/reindex` 可立即重建单份资料；后台“重建当前检索索引”可恢复丢失的集合。现有 `scripts/context_backfill.py` 只负责 Wiki 重编译。
5. 先检查失败任务、来源版本与反馈，再依据真实语料质量数据决定是否切入语义路由。回退文档检索可使用原有 `CONTEXT_INDEX_MODE=legacy`；停用可选模型后，统一入口保留词法检索。增量数据表可以保留，回滚应用不需要立即删表。

原始资料没有被此次开发发送到新外部服务。协议测试使用本机固定模型响应；语义模型仍按项目现有配置显式启用。

## 自动验证

已执行全项目 Go 测试与静态检查；知识相关核心包增加竞争检测。新增边界测试覆盖 513 份文档、超过 257 页 Wiki、770 条记忆、长文本尾部、版本变化、来源删除、跨用户与团队范围、引文伪造、分页拼接、租约恢复、幂等入队和反馈复核。

PostgreSQL 18.3、MySQL 8.4.10 和 Qdrant 1.18.0 使用本次专用临时容器验证。数据库测试实际执行 SQL；Qdrant 测试实际创建、查询、过滤和删除集合，嵌入模型由固定夹具提供。CI 新增 `knowledge-integration`，在独立服务中执行迁移、核心回归及两种持久化实现测试。

前端已通过静态检查、42 项测试和生产构建。Python 最终全量回归为 836 项通过、14 项跳过、142 个子测试通过，包含新增的统一知识适配用例。跳过项未被计为通过。测试结果对应本地工作区，不代表尚未运行的远程 CI。

复验命令如下，数据库连接变量必须指向专用测试实例：

```sh
go test ./...
go vet ./...
go test -race ./internal/knowledge... ./internal/semantic ./internal/document ./internal/memory ./internal/chattool ./internal/httpserver ./internal/persistence/postgresstore ./internal/persistence/mysqlstore
go test ./internal/persistence/postgresstore ./internal/persistence/mysqlstore -run TestKnowledgeDurable -count=1 -v
QDRANT_TEST_URL=http://127.0.0.1:16336 go test ./internal/semantic ./internal/document -run Qdrant -count=1 -v
PYTHONPATH=workers/python/src workers/python/.venv/bin/python -m pytest workers/python/tests -q
pnpm --dir web check
pnpm --dir web test
pnpm --dir web build
```

## 2026 年 10 月 1 日本地部署

已使用现有本地 Compose 配置构建并更新 API、Worker、Agent Worker 和网页服务。网页地址为 `http://localhost:3000`，运维入口为 `http://localhost:3000/ops`，API 地址为 `http://localhost:8080`。原有基础服务、数据卷和账号继续保留。

部署前已保存 PostgreSQL 自定义格式备份并验证其归档目录，同时为四个应用的旧镜像保留回退标签。备份位于本机 `.data/backups/knowledge-local-20261001-162242/`；该目录不纳入版本控制。

PostgreSQL 已迁移至 `000046`，Agent 检查点表核对通过。网页和运维入口、API 健康与就绪接口、Agent 就绪接口均通过检查。使用两个临时账号执行的 14 项接口验收全部通过，覆盖统一检索、版本化阅读、来源追踪、引文校验、跨用户及团队范围限制、反馈保存和删除失效；临时账号及测试记录已清理。

现有 32 份就绪文档的 `document_index` 任务全部就绪，原先 2 份失败文档的状态未改变。部署后检查未发现 API、Worker 或 Agent Worker 的警告和错误日志。

当前本地未配置语义嵌入模型，沿用 `legacy` 词法检索。统一来源、权限、引用、反馈和持久任务已经启用；语义 ANN 召回尚未在此本地配置中启用。

## 尚未验收的部分

生产配置使用 Qdrant ANN，SQL 保存投影就绪状态和恢复数据；未配置 Qdrant 的测试环境保留 SQL 向量计算。权限范围和就绪键仍需按来源枚举，大规模过滤成本需要压测，不能据此宣称十万片段规模的延迟目标达标。

历史文档集合清理按当前配置根名称及严格的历史语义集合命名规则发现，只删除指定用户及来源的向量，不删除整个集合。配置迁移到其他 Qdrant 地址或集合根名称时，旧命名空间需按迁移清单单独处理。向量清理不等同于删除历史会话、检查点、导出文件或备份。

检索融合当前采用各来源倒数排名的统一排序；没有实现实体去重图、多跳图推理、可查询的历史时点、视觉区域检索、自动蕴含判定或真正 BM25 的替换。接口保留相关位置字段，但不会虚构缺失的表格区域或历史证据。

仍需授权脱敏语料与人工标注开展真实模型盲测、Recall 和 nDCG 对比、引用支持率、无答案率、成本及 1 万和 10 万片段压测。本次没有达到或声称达到评审中的这些发布目标；这些验收通过前不宜把本地工程测试作为生产语义检索全面放量依据。

## 实现参考

Qdrant 分组检索使用官方 [Query point groups 接口](https://api.qdrant.tech/master/api-reference/search/query-points-groups?explorer=true)，按来源分组限制片段数量，并在检索前提供当前允许使用的投影键过滤条件。具体请求已在本地 Qdrant 1.18.0 实例上执行验证。
