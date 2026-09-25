# 并行 Agent 与执行资源

本次图版本为 `ai-companion-supervisor@3.71.0`。简单聊天与单次生活工具继续走原有短路径。

## 执行链路

- Skill Worker 使用有界执行池。数据库轮询和 Kafka `RunID` 共用许可，领取租约前等待许可；解析与渲染各自设置资源上限，等待资源许可期间持续续租。每个已领取任务独立续租，停机等待执行与续租协程退出。
- 两到三个附件分别启动读取任务。同一附件的 `next_round` 按顺序推进，附件间可以并行；所有来源读取完成后才进入后续生成。异步工具全部启动后统一持久暂停，恢复时观察已有任务，不重新创建。
- Work Planner 可提出 `research_tasks`，每个包含 `id`、`question`、`tool_name`、`arguments` 和 `depends_on`。运行时校验 2–6 个任务、无环依赖、可信只读工具及其 Schema。每个子问题分为读取和分析节点，分析只可引用分配给它的证据。主 Agent 结合原始观察与候选结论生成回答。
- Wiki 将全部片段按 24,000 UTF-8 字节分批，保留每个片段的 ID 与来源。独立批次并行提炼，再按源顺序合并同名主题、实体、决策；不同的来源陈述保留。全部原文仍有独立可导航页面。超过批次数预算时保留完整原文页面，并明确记录 `semantic_batch_budget_exceeded`，不默默只综合文档开头。
- 用户提出“审校”“审阅”“仔细核对”等要求时，来源与表达两个专家并行检查同一份候选内容。发现问题后最多修订一次并复核；仍不通过时停止交付。评审对象是回复或生成参数的内容，不是渲染后文件的视觉版式。研究分析和评审使用既有 Composer 模型配置。

有关审校意见、研究结论、Wiki 和记忆的争议处理，见 [仲裁机制](ARBITRATION.md)。

## 状态与失败恢复

`parallel_plan` 保存任务 DAG、状态、异步任务 ID 和分配的预算，`parallel_results` 由稳定任务 ID 与调度代次归并。每个读取或模型分支都是独立 LangGraph 节点；分支不能修改父级预算、观察或工具状态。汇总节点统一结算已执行模型调用（含失败调用），保留成功兄弟分支，仅重试可重试失败分支一次。

模型调用前分配互不重叠的调用、输入/输出/总 Token 与成本额度，并留出最终综合的份额。工具动作在派发前统一预留；重试复用同一个分支幂等键。审批和业务写入仍走原有 Go Tool Gateway，研究计划的白名单不包含写工具。

Wiki 成功分片在 `wiki_shards` 中缓存 24 小时，键绑定用户、文档、来源版本、模型端点/名称、编译版本与输入内容。重建可复用成功分片；失败批次在本次运行内重试一次。部分失败时保留原文和有效结果，并在来源页的 `synthesis` 元数据报告覆盖范围。再次重建时只需重新调用未缓存批次。候选内容仍需重新通过引用校验，发布和读取仍检查来源版本及权限；同一用户的 Wiki 发布串行约束保持不变。

## 配置

| 配置 | 默认值 | 含义 |
| --- | --- | --- |
| `SKILL_WORKER_CONCURRENCY` | `4` | 每个 Skill Worker 的并发上限，范围 1–32 |
| `SKILL_PARSE_CONCURRENCY` | `2` | 文档解析与表格分析的并发上限，范围 1–32 |
| `SKILL_RENDER_CONCURRENCY` | `2` | PPT、PDF 翻译与 Word 编辑的并发上限，范围 1–32 |
| `AGENT_PARALLEL_ATTACHMENTS` | `true` | 多附件独立读取；`false` 回退原串行路径 |
| `AGENT_MODEL_FANOUT_CONCURRENCY` | `3` | 单 Run 的图分支并发，范围 1–4 |
| `AGENT_SPECIALIST_REVIEW_MODE` | `requested` | `off` / `requested` / `always`，仅适用于 Work |
| `CONTEXT_MODEL_CONCURRENCY` | `6` | 单个语义客户端总请求并发，范围 1–32 |
| `CONTEXT_WIKI_CONCURRENCY` | `3` | 单文档 Wiki 分片并发，范围 1–8 |
| `CONTEXT_WIKI_MAX_BATCHES` | `64` | Wiki 批次数上限；每批最多尝试两次，范围 1–256 |
| `MODEL_PROVIDER_CONCURRENCY` | `8` | 同一共享目录内、同一供应商主机的总请求并发，范围 1–64 |
| `MODEL_CONCURRENCY_DIR` | 系统临时目录内的私有目录 | 跨 Go/Python 进程共享的许可目录 |
| `MODEL_CONCURRENCY_SHARED_GID` | 空 | 可选共享组；目录需有 setgid 位且禁止其他用户访问 |

供应商许可使用 POSIX 文件锁，进程崩溃后由系统释放，排队时间计入请求截止时间。Go 对话/语义请求、Python Agent、翻译和配图使用同一协议。Compose 的 API、Worker、Agent Worker 共用 `model-slots` 卷及组 10003，保留各自原有 UID。所有参与进程需使用相同并发值。跨主机部署需另设集中式限流；本实现的共享目录应位于支持可靠 POSIX 锁的同一主机本地文件系统上。

## 升级与测试

部署前应用 PostgreSQL `000044_wiki_shards`（MySQL 对应 `000033_wiki_shards`），重新构建 API、Worker 和 Agent 镜像。旧图版本的未完成 Run 应先完成或由用户重新发起；现有版本校验会阻止旧检查点混用新图。

自动化入口 `make test-python` 与 CI 使用 pytest，兼容现有 unittest 用例并包含新的并行测试。可分别运行：

```sh
go test ./...
go test -race ./internal/skill ./internal/document ./internal/semantic ./internal/platform/modelquota
workers/python/.venv/bin/python -m pytest workers/python/tests -q
```

设置 `POSTGRES_TEST_DSN` 可运行 `TestWikiShardCacheScopesRevisionsOwnersAndExpiry`，验证持久分片缓存的用户/文档/版本隔离及过期行为。设置 `LANGGRAPH_POSTGRES_TEST_DSN` 可运行 Python 的检查点断开重连测试；两者均应指向独立测试数据库。并发测试用同步屏障验证真实重叠、许可上限与失败恢复；其结果不代表生产模型的加速倍数。
