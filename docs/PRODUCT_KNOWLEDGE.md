# 内置项目知识库

产品介绍、使用步骤和配置说明随 API/Worker 二进制发布。新注册用户无需上传项目文档、等待 Wiki 编译或配置向量模型，即可在情感陪伴、生活助手、工作伙伴里询问产品用法。

## 使用方式

- 侧栏「产品知识」可搜索、分类浏览、阅读完整章节；「工作伙伴 → Wiki」也包含相同入口。
- 可以直接问「伴AI有哪些功能」「如何纠正长期记忆」「怎么上传资料到知识库」「如何生成PPT」「管理员怎么用PIN登录」。
- 对话自动召回相关章节；「具体有什么限制」「然后怎么操作」等追问可沿用紧邻的用户话题。聊天中的「产品指南」引用会打开对应章节，普通登录过期时仍需登录。
- 搜索结果只是预览。打开章节可看全文；长章节带有同章节其他部分的链接。
- 内置产品资料与个人上传资料分开显示。内置资料只读，随应用更新；个人 Wiki 继续支持上传、编辑、版本与删除。

## 来源与更新

白名单来源由 `scripts/build-product-knowledge.mjs` 维护：中文 README 的项目介绍、架构和部署内容，`web/app/onboarding-content.ts` 的全部用户/后台指南，以及上下文管理、管理员登录和本知识库专题说明。当前不收录 `.env`、真实运行数据、用户文档、日志、发布证据或演示账号凭据。README 的截图、案例和历史成果只是演示，不能作为当前账户或生产状态。

更新来源后执行：

```sh
node scripts/build-product-knowledge.mjs
node scripts/build-product-knowledge.mjs --check
go test ./internal/productknowledge
```

生成器使用 Node 24+，不调用模型、不访问网络。Go `go:embed` 将 `internal/productknowledge/content/catalog.json` 编入应用，运行时不读取仓库文件、不向每个用户复制资料，也不消耗用户文档额度。部署正常重新构建 API/Worker 即生效，无需新数据库迁移。Go 测试检查每个来源的 SHA-256，Web CI 重建比对，防止指南更新而知识包遗漏更新。

## 对话与执行边界

Go 常规聊天和 Agent 入口使用同一个 `conversation-context-v1`，新增可选的 `knowledge` 字段。知识以参考资料的 user 消息传入，不升级为系统指令。router、planner、composer、assessor、repairer、responder 都保留该资料，以支持说明、规划和内容生成。

本地检索采用中文双字词、英文词及功能别名的加权 BM25，不依赖远程向量服务；新表达仍可能漏召回，模型可使用所有模块都具备的 `product_knowledge_search`、`product_knowledge_read` 补查。自动召回最多 8 个候选，按 3,600 估算 Token 保留完整知识段，省略数量写入 manifest。工具搜索最多 5 个候选，结果预算 5,500 Token；阅读每次最多 2,400 个 Unicode 字符，明确返回 `has_more`、`next_offset` 和总长度。Wiki 与产品知识合计最多 8 次成功补查，并继续服从运行的总步骤、模型窗口和成本预算。因此不能承诺一次回答覆盖整个知识库；回答应覆盖当前问题相关内容，缺证据时说明缺口。

用法咨询不应创建账单、提醒、任务或更改管理员配置。明确要求执行时，仍由现有业务工具检查身份、模块、参数与人工确认；没有对应工具时提供手动操作步骤。后台文档是说明，普通用户不会因阅读它而获得后台权限。实际模型仍负责回答与工具选择；`MODEL_PROVIDER=development` 是固定模拟回复，不代表真实模型问答质量。

## API

以下接口要求普通用户 Bearer 身份，读取的是固定内置资料，不接受上传或修改：

- `GET /v1/knowledge/builtin/pages`：目录和知识包 revision，不含全文。
- `POST /v1/knowledge/builtin/search`：`query` 为 2–1000 字，`limit` 为 1–12（默认 8），返回可打开的预览。
- `GET /v1/knowledge/builtin/pages/{page_id}`：完整章节、来源路径、章节名、内容版本和续篇链接。

链接 `/#knowledge=builtin-…` 打开 Web 阅读页。历史对话的引用保留旧来源版本标识，阅读页显示当前应用的文档版本；不提供内置文档历史版本回放。删除个人 Wiki 不影响内置项目说明，且内置搜索不会读取私人文档或实时业务状态。
