# 06 上传、解析、检索与 Wiki

[阅读总入口](README.md)

行为例子：上传 PDF，询问其中某页，再将整份文件生成制品。问答可检索部分相关片段；整份制品处理要继续沿分轮接口获取全部来源。Wiki 是这些片段的可追溯派生知识层。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 一份文件会有多种表示

Blob 是原始文件字节；Document 元数据记录归属、文件名和状态；解析结构保存页、标题与正文；Chunk 是检索和组装使用的小片段；索引让查询定位候选；Wiki 是从来源派生、可版本化的知识内容。它们不是同一个“文件对象”的不同名字。

Embedding 把文本表示成数值向量以便比较语义接近程度；关键词检索按文本相关性找候选；rerank 再给候选排序。是否使用外部语义服务取决于配置，读到相关代码不代表当前机器一定启用了它。

问答检索只想找“能回答问题的少数片段”；整文件制品则可能要求处理全部来源。前者相关度高不意味着后者覆盖完整，不能用 top-k 检索替代逐轮读完整文档。

## 上传：Blob 与元数据分开保存

源码：[internal/document/service.go:90](/Users/windcry1/Documents/ai_companion/internal/document/service.go:90)

<a id="symbols-6-1"></a>

### 函数与变量详解

函数/类型声明：[`Upload`](/Users/windcry1/Documents/ai_companion/internal/document/service.go:90)。

**职责、输入输出与影响：** 输入 userID、originalName、data 字节；返回 (Document, 是否新建 bool, error)。校验后先写 Blob 再建元数据/任务；queued 不是解析完，跨 Blob/SQL 不在同一事务。

**调用关系：** 上传 HTTP 入口 → cleanName/detectMediaType → blobs.Put → store.CreateDocument。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `originalName / name` | string / string | 客户端文件名 / cleanName 返回 | 只用清理后名称入元数据，原名不能作为任意本地文件路径。 |
| `data` | []byte | 上传字节 | 检查非空与 s.maxBytes；normalizePDFPrefix 可能调整其内容后再算哈希。 |
| `mediaType` | string | detectMediaType(data) | 按内容检测媒体类型，不只看文件扩展名。 |
| `digest / hash` | [32]byte / string | SHA-256 / 十六进制表示 | 用于内容寻址与完整性标识，不是文件内容加密。 |
| `fileID / documentID / jobID` | string / string / string | 三次 ID 生成 | 分别标识文件、用户文档、解析任务，不可当同一个 ID。 |
| `item` | Document | 组装的新元数据 | StorageKey 由用户与内容 hash 派生；Status 初始 queued。 |
| `saved / created` | Document / bool | CreateDocument 返回 | created=false 可能是复用；saved 才是最终返回记录。 |

### 源码与逐行解释

```text
  90 │ func (s *Service) Upload(ctx context.Context, userID, originalName string, data []byte) (Document, bool, error) {
  91 │ 	name, err := cleanName(originalName)
  92 │ 	if err != nil {
  93 │ 		return Document{}, false, err
  94 │ 	}
  95 │ 	if len(data) == 0 || int64(len(data)) > s.maxBytes {
  96 │ 		return Document{}, false, fmt.Errorf("%w: file size must be between 1 and %d bytes", ErrValidation, s.maxBytes)
  97 │ 	}
  98 │ 	data = normalizePDFPrefix(data)
  99 │ 	mediaType, err := detectMediaType(data)
 100 │ 	if err != nil {
 101 │ 		return Document{}, false, err
 102 │ 	}
 103 │ 	if bytes.Contains(data, []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")) {
 104 │ 		return Document{}, false, ErrUnsafeContent
 105 │ 	}
 106 │ 	digest := sha256.Sum256(data)
 107 │ 	hash := hex.EncodeToString(digest[:])
```

- **L90–L94**：清理文件名，非法名称立即拒绝。
- **L95–L97**：上传必须非空且不超过配置字节上限。
- **L98–L102**：处理 PDF 前缀并检测真实媒体类型，失败拒绝，而不是只信文件扩展名。
- **L103–L105**：拒绝 EICAR 测试标记；这段检查本身不是完整杀毒引擎。
- **L106–L107**：对规范化后的内容计算 SHA-256。

```text
 108 │ 	fileID, err := id.New()
 109 │ 	if err != nil {
 110 │ 		return Document{}, false, err
 111 │ 	}
 112 │ 	documentID, err := id.New()
 113 │ 	if err != nil {
 114 │ 		return Document{}, false, err
 115 │ 	}
 116 │ 	jobID, err := id.New()
 117 │ 	if err != nil {
 118 │ 		return Document{}, false, err
 119 │ 	}
 120 │ 	now := s.now().UTC()
 121 │ 	item := Document{
 122 │ 		ID: documentID, UserID: userID, FileID: fileID, JobID: jobID, Name: name,
 123 │ 		MediaType: mediaType, SizeBytes: int64(len(data)), SHA256: hash,
 124 │ 		StorageKey: storageKey(userID, hash), Status: "queued", CreatedAt: now, UpdatedAt: now,
 125 │ 	}
```

- **L108–L119**：依次生成文件、文档、摄取任务的 ID；三个实体用途不同，任一步失败不继续。
- **L120–L125**：构造 queued 文档，StorageKey 由用户和内容哈希生成，记录大小与媒体类型。

```text
 126 │ 	_, err = s.blobs.Put(ctx, item.StorageKey, data)
 127 │ 	if err != nil {
 128 │ 		return Document{}, false, fmt.Errorf("store document blob: %w", err)
 129 │ 	}
 130 │ 	saved, created, err := s.store.CreateDocument(ctx, item)
 131 │ 	if err != nil {
 132 │ 		// Content-addressed blobs are intentionally retained when metadata creation fails.
 133 │ 		// A later retry reuses the same key; periodic orphan cleanup can remove unreferenced blobs.
 134 │ 		return Document{}, false, err
 135 │ 	}
 136 │ 	return saved, created, nil
 137 │ }
```

- **L126–L129**：先将字节保存到 BlobStore；失败不能创建可解析的文档元数据。
- **L130–L135**：再创建数据库文档/任务元数据；失败时内容寻址 Blob 有意保留供重试复用，文件系统和 SQL 不是一个原子事务。
- **L136–L137**：返回持久化记录和 created 标志；created=false 可能是去重复用，不是上传失败。


## Worker 把文件变成可搜索片段

源码：[internal/document/ingest.go:120](/Users/windcry1/Documents/ai_companion/internal/document/ingest.go:120)

<a id="symbols-6-2"></a>

### 函数与变量详解

函数/类型声明：[`process`](/Users/windcry1/Documents/ai_companion/internal/document/ingest.go:120)。

**职责、输入输出与影响：** Ingestor.process 输入已领取 IngestJob，返回 (已处理 bool, error)。读取 Blob、解析、建索引、存解析结构并完成任务；true 不等于整个解析成功。

**调用关系：** RunOnce/RunJob 领取后调用；blobs.Get → parser.Parse → prepareResult → index.Ensure/Upsert → SaveParsedDocument → CompleteIngestJob。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `i` | *Ingestor | 方法接收者 | 有 store、blobs、parser、index、workerID、now；这里 i 不是循环下标。 |
| `job` | IngestJob | 领取结果 | job.Document 包含文件 StorageKey/MediaType 与归属；job.ID 定位后台任务。 |
| `fail` | func(error) (bool, error) | 函数内闭包 | 分类错误并尝试 FailIngestJob，返回 true 和原 cause；标记失败的错误被忽略。 |
| `cause / code` | error / string | fail 入参 / failureCode(cause) | 前者为原始原因，后者是可存储错误类别。 |
| `data` | []byte | blobs.Get | 原始文件内容交 Python Parser，不是已切块正文。 |
| `result` | ParseResult | parser.Parse | prepareResult 通过指针补稳定标识，Chunks 用于索引，完整结果用于存储。 |

### 源码与逐行解释

```text
 120 │ func (i *Ingestor) process(ctx context.Context, job IngestJob) (bool, error) {
 121 │ 	fail := func(cause error) (bool, error) {
 122 │ 		code := failureCode(cause)
 123 │ 		_ = i.store.FailIngestJob(ctx, job.ID, code, i.now().UTC())
 124 │ 		return true, cause
 125 │ 	}
 126 │ 	data, err := i.blobs.Get(ctx, job.Document.StorageKey)
 127 │ 	if err != nil {
 128 │ 		return fail(fmt.Errorf("read_blob: %w", err))
 129 │ 	}
 130 │ 	result, err := i.parser.Parse(ctx, job.Document.MediaType, data)
 131 │ 	if err != nil {
 132 │ 		return fail(fmt.Errorf("parse_document: %w", err))
 133 │ 	}
 134 │ 	prepareResult(job.Document.ID, &result)
 135 │ 	if err = i.index.Ensure(ctx); err != nil {
 136 │ 		return fail(fmt.Errorf("ensure_index: %w", err))
 137 │ 	}
```

- **L120–L125**：局部 fail 函数把错误分类并尝试写任务失败状态；true 表示已处理/领取过任务，不代表成功。
- **L126–L129**：读取 Blob，错误前缀 read_blob 帮助定位故障阶段。
- **L130–L133**：调用 Python Parser，解析失败记录 parse_document。
- **L134**：为页和 Chunk 补充确定性 ID、解析器版本等字段，重试时保持稳定。
- **L135–L137**：确认索引集合可用。

```text
 138 │ 	if err = i.index.Upsert(ctx, job.Document, result.Chunks); err != nil {
 139 │ 		return fail(fmt.Errorf("index_chunks: %w", err))
 140 │ 	}
 141 │ 	if err = i.store.SaveParsedDocument(ctx, job, result, i.now().UTC()); err != nil {
 142 │ 		return fail(fmt.Errorf("save_chunks: %w", err))
 143 │ 	}
 144 │ 	if err = i.store.CompleteIngestJob(ctx, job.ID, i.now().UTC()); err != nil {
 145 │ 		return true, err
 146 │ 	}
 147 │ 	return true, nil
```

- **L138–L140**：先向索引 Upsert Chunks；失败标记 index_chunks。
- **L141–L143**：把解析结果写入 SQL；这里只是外部索引和 SQL 的有序更新，不是跨系统事务。
- **L144–L147**：完成摄取 Job；返回 true,nil 才表示本次成功处理。恢复/重试依赖稳定 ID 和任务状态。


## Python 按章节和 Token 切块

源码：[workers/python/src/ai_companion_worker/document_parser.py:352](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/document_parser.py:352)

<a id="symbols-6-3"></a>

### 函数与变量详解

函数/类型声明：[`_chunk_pages`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/document_parser.py:352)、[`emit`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/document_parser.py:360)。

**职责、输入输出与影响：** _chunk_pages(pages: list[Page]) 返回 list[Chunk]。纯内存切块；内部 emit 无参数、返回 None，通过闭包更新缓冲、序号和输出列表。

**调用关系：** 文档解析流程调用；_heading 识别标题，_split_oversized_block 拆大块，count_tokens 验预算，_chunk 构造片段。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `pages / page` | list[Page] / Page | 解析后的页列表 / 当前页 | page.page_no 保留页号，page.text 是可提取文字。 |
| `chunks` | list[Chunk] | 空列表 | emit 每次追加一个非空片段，最终返回。 |
| `section_stack / buffer_section` | list[str] / str | 跨页标题栈 / 当前栈拼接 | 标题级别变化时截栈，再形成章节路径，不是文件系统目录。 |
| `ordinal` | int | 初始 0 | 每发出一个 Chunk 加一，跨页连续。 |
| `buffer` | list[str] | 每页开始 [] | 聚合文本段；emit 后保留 overlap 尾部，超预算时可能清掉。 |
| `content / candidate` | string 对应 Python str | emit 中正文 / 尝试加入下一段的文本 | content 真正进入 Chunk，candidate 先用于预算判断。 |
| `blocks / block / part` | list[str] / str / str | 按空行分段 / 当前段 / 进一步拆块 | 三个粒度不同，part 才被 append 到 buffer。 |
| `heading / level / title` | 可选二元组 / int / str | _heading(block) | 标题时调整章节栈；level 不是页号。 |
| `emit` | 内部函数 | 闭包创建 | nonlocal buffer, ordinal 允许重新绑定外层变量；chunks.append 则修改已有列表。 |

### 源码与逐行解释

```text
 352 │ def _chunk_pages(pages: list[Page]) -> list[Chunk]:
 353 │     chunks: list[Chunk] = []
 354 │     section_stack: list[str] = []
 355 │     ordinal = 0
 356 │     for page in pages:
 357 │         buffer: list[str] = []
 358 │         buffer_section = " / ".join(section_stack)
 359 │
 360 │         def emit() -> None:
 361 │             nonlocal buffer, ordinal
 362 │             content = "\n\n".join(buffer).strip()
 363 │             if not content:
 364 │                 buffer = []
 365 │                 return
 366 │             ordinal += 1
 367 │             chunks.append(
 368 │                 _chunk(ordinal, page.page_no, buffer_section, content)
 369 │             )
 370 │             buffer = _overlap_tail(buffer)
 371 │
```

- **L352–L355**：初始化 Chunk 列表、跨页章节栈和全局 ordinal。
- **L356–L359**：逐页处理，每页新建缓冲；章节路径可继承前页标题。
- **L360–L362**：emit 闭包访问外层 buffer/ordinal，先合并当前段落。
- **L363–L365**：空内容不生成 Chunk，并清空 buffer。
- **L366–L369**：ordinal 加 1，以页码、章节与正文生成 Chunk。
- **L370–L371**：保留尾部重叠，作为下一 Chunk 的上下文。

```text
 372 │         blocks = [part.strip() for part in re.split(r"\n\s*\n", page.text) if part.strip()]
 373 │         for block in blocks:
 374 │             heading = _heading(block)
 375 │             if heading is not None:
 376 │                 if buffer:
 377 │                     emit()
 378 │                     buffer = []
 379 │                 level, title = heading
 380 │                 section_stack = section_stack[: level - 1]
 381 │                 section_stack.append(title[:160])
 382 │                 buffer_section = " / ".join(section_stack)
 383 │             for part in _split_oversized_block(block, MAX_CHUNK_TOKENS):
 384 │                 candidate = "\n\n".join([*buffer, part]).strip()
 385 │                 if buffer and count_tokens(candidate) > MAX_CHUNK_TOKENS:
 386 │                     emit()
 387 │                     candidate = "\n\n".join([*buffer, part]).strip()
 388 │                     if buffer and count_tokens(candidate) > MAX_CHUNK_TOKENS:
 389 │                         buffer = []
 390 │                 buffer.append(part)
```

- **L372–L374**：按空段落分块并检测标题。
- **L375–L378**：新标题前先提交旧 buffer，然后清空，避免跨章节沿用重叠内容。
- **L379–L382**：按标题级别裁章节栈并追加当前标题，更新 section_path。
- **L383–L384**：过大段落先拆成小 part，再试着拼入 buffer。
- **L385–L387**：超出 Token 上限时提交已有块，再与重叠尾部重新计算候选。
- **L388–L390**：重叠加新段仍超限则丢弃重叠；随后放入新段。此处保留正文，新块不为重叠无限超预算。

```text
 391 │         if buffer:
 392 │             emit()
 393 │     return chunks
```

- **L391–L393**：每页最后剩余 buffer 也提交，最终返回有序 Chunk。


## 问答检索首先限制用户可见范围

源码：[internal/document/retrieval.go:50](/Users/windcry1/Documents/ai_companion/internal/document/retrieval.go:50)

<a id="symbols-6-4"></a>

### 函数与变量详解

函数/类型声明：[`Query`](/Users/windcry1/Documents/ai_companion/internal/document/retrieval.go:50)。

**职责、输入输出与影响：** 输入用户与 QueryInput，返回 (QueryResult, error)。限定可见 ready 文档后检索与重排；无允许来源可正常返回“资料不足”，不是数据库错误。

**调用关系：** 文档问答入口 → normalizeQuery/ListDocuments → index.Search → rerankedAnswer。索引/重排是否使用外部服务取决于配置。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | QueryInput | 请求参数 | Query 是问题，DocumentIDs 可限定来源，Limit 是最终命中数量约束。 |
| `query / limit` | string / int | normalizeQuery(input) | 规范化问题与条数，limit*3 为候选检索量。 |
| `items` | []Document | 当前用户最多 200 条元数据 | 用于建立来源允许范围，不是所有用户文档。 |
| `requested` | map[string]bool | DocumentIDs 建集合 | 空集表示未额外指定，不表示跨用户无限搜索。 |
| `readyIDs` | []string | 从 items 筛 Status=ready 且符合 requested | 真正交给索引的可用文档 ID 列表。 |
| `hits` | []SearchHit | index.Search | 候选片段带内容、分数、页与章节信息，未必全部出现在最终回答。 |
| `allowed` | map[string]Document | documentsByID(items, readyIDs) | 重排/引用构建时校验和补全文档信息。 |

### 源码与逐行解释

```text
  50 │ func (s *Service) Query(ctx context.Context, userID string, input QueryInput) (QueryResult, error) {
  51 │ 	query, limit, err := normalizeQuery(input)
  52 │ 	if err != nil {
  53 │ 		return QueryResult{}, err
  54 │ 	}
  55 │ 	items, err := s.store.ListDocuments(ctx, userID, 200)
  56 │ 	if err != nil {
  57 │ 		return QueryResult{}, err
  58 │ 	}
  59 │ 	requested := map[string]bool{}
  60 │ 	for _, documentID := range input.DocumentIDs {
  61 │ 		requested[documentID] = true
  62 │ 	}
  63 │ 	readyIDs := make([]string, 0)
  64 │ 	for _, item := range items {
  65 │ 		if item.Status == "ready" && (len(requested) == 0 || requested[item.ID]) {
  66 │ 			readyIDs = append(readyIDs, item.ID)
  67 │ 		}
  68 │ 	}
```

- **L50–L54**：清理 query 和 limit，非法查询返回错误。
- **L55–L58**：只列当前用户文档。
- **L59–L62**：将请求中的 DocumentIDs 转为集合便于过滤。
- **L63–L68**：只选择 ready 且属于请求范围的文档；空请求范围表示当前用户可见的 ready 文档。

```text
  69 │ 	if len(readyIDs) == 0 {
  70 │ 		return insufficientQueryResult(), nil
  71 │ 	}
  72 │ 	hits, err := s.index.Search(ctx, userID, query, readyIDs, limit*3)
  73 │ 	if err != nil {
  74 │ 		return QueryResult{}, err
  75 │ 	}
  76 │ 	allowed := documentsByID(items, readyIDs)
  77 │ 	return s.rerankedAnswer(ctx, query, limit, allowed, hits), nil
  78 │ }
```

- **L69–L71**：没有可用证据时返回 insufficient，而不是调用模型猜测。
- **L72–L75**：带用户和白名单文档 ID 检索 3×limit 候选，给后续排序留余地。
- **L76–L78**：再次建立允许文档映射，经 rerankedAnswer 过滤/重排并生成带引用结果。语义适配器与重排是否启用取决于配置。


## 整份文件以轮次继续读取

源码：[internal/document/context.go:147](/Users/windcry1/Documents/ai_companion/internal/document/context.go:147)

<a id="symbols-6-5"></a>

### 函数与变量详解

函数/类型声明：[`ReadParsedContextRoundWindow`](/Users/windcry1/Documents/ai_companion/internal/document/context.go:147)。

**职责、输入输出与影响：** 输入 userID/documentID、每轮 maxTokensPerRound、1 起始 roundStart、最多 maxRounds；返回 (DocumentContext, error)。只读解析结构并组装本轮来源，coverage 描述此次调用，不累计历史调用。

**调用关系：** 完整文件处理工具调用；GetDocument → ParsedStore.ListDocumentChunks → partitionContextRounds → cleanContextText/readSourceIR。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `item / store` | Document / ParsedStore | 按归属读取 / 接口能力断言 | 文档必须 ready，且存储必须支持读取解析片段。 |
| `chunks / roundChunks` | []Chunk / [][]Chunk | 最多 10000 片段 / 按预算分轮 | chunks 为读取全集，roundChunks 为分轮视图。 |
| `maxTokensPerRound / roundStart / maxRounds` | int / int / int | 参数，非正分别回退 4000/1/4 | 预算、开始轮号、窗口轮数，不是页数。 |
| `startIndex / endIndex / completedRounds` | int / int / int | 轮号转 0 基下标、截尾 / 差值 | 切片是左闭右开；返回 NextRound=endIndex+1。 |
| `selected / manifest / blocks` | []Chunk / []ContextRound / []string | 逐轮累计 | 分别供覆盖/来源、诊断清单、最终 Markdown 文本。 |
| `report` | CleaningReport | 含 PolicyVersion 的初始结构 | 累计去重行与空行折叠，不会改原 Blob。 |
| `marker / section / roundMarker` | string / string / string | 页范围、章节说明、轮次标记 | 帮助模型与引用保留边界，不是用户原文件新增的永久内容。 |
| `roundTokens / estimatedTokens` | int / int | 每轮/全次累计成本 | 估计包含每片额外开销，不是提供商费用。 |
| `coverage / sourceIR` | float64 / map[string]any | len(selected)/len(chunks) / readSourceIR | coverage 是 0–1 比例；sourceIR 为稳定实体/出处，不能用文本“已读完”替代。 |

### 源码与逐行解释

```text
 147 │ func (s *Service) ReadParsedContextRoundWindow(
 148 │ 	ctx context.Context,
 149 │ 	userID string,
 150 │ 	documentID string,
 151 │ 	maxTokensPerRound int,
 152 │ 	roundStart int,
 153 │ 	maxRounds int,
 154 │ ) (DocumentContext, error) {
 155 │ 	item, err := s.store.GetDocument(ctx, userID, documentID)
 156 │ 	if err != nil {
 157 │ 		return DocumentContext{}, err
 158 │ 	}
 159 │ 	if item.Status != "ready" {
 160 │ 		return DocumentContext{}, ErrParsedContentUnavailable
 161 │ 	}
 162 │ 	store, ok := s.store.(ParsedStore)
 163 │ 	if !ok {
 164 │ 		return DocumentContext{}, ErrParsedContentUnavailable
 165 │ 	}
```

- **L147–L154**：接口显式接收每轮预算、起始轮次和最多轮数，所以调用方可保存游标恢复。
- **L155–L161**：按用户取文档且必须 ready。
- **L162–L165**：要求 Store 能读取解析后的内容。

```text
 166 │ 	chunks, err := store.ListDocumentChunks(ctx, userID, documentID, 10_000)
 167 │ 	if err != nil {
 168 │ 		return DocumentContext{}, err
 169 │ 	}
 170 │ 	if len(chunks) == 0 {
 171 │ 		return DocumentContext{}, ErrParsedContentUnavailable
 172 │ 	}
 173 │ 	if maxTokensPerRound <= 0 {
 174 │ 		maxTokensPerRound = 4_000
 175 │ 	}
 176 │ 	if maxRounds <= 0 {
 177 │ 		maxRounds = 4
 178 │ 	}
 179 │ 	if roundStart <= 0 {
 180 │ 		roundStart = 1
 181 │ 	}
```

- **L166–L172**：读取最多 10000 个 Chunk；空结果返回不可用。这个上限是真实实现边界，不能笼统承诺无限文件。
- **L173–L181**：缺省每轮 4000 估算 Token、每次 4 轮、从第 1 轮开始。

```text
 182 │
 183 │ 	roundChunks := partitionContextRounds(chunks, maxTokensPerRound)
 184 │ 	startIndex := min(roundStart-1, len(roundChunks))
 185 │ 	if startIndex >= len(roundChunks) {
 186 │ 		return DocumentContext{}, fmt.Errorf("document context round %d is out of range", roundStart)
 187 │ 	}
 188 │ 	endIndex := min(len(roundChunks), startIndex+maxRounds)
 189 │ 	completedRounds := endIndex - startIndex
 190 │ 	selected := make([]Chunk, 0, len(chunks))
 191 │ 	manifest := make([]ContextRound, 0, completedRounds)
 192 │ 	blocks := make([]string, 0, completedRounds)
 193 │ 	report := CleaningReport{PolicyVersion: "document-cleaning-v1"}
 194 │ 	estimatedTokens := 0
 195 │ 	for index, items := range roundChunks[startIndex:endIndex] {
 196 │ 		roundBlocks := make([]string, 0, len(items))
 197 │ 		roundTokens := 0
 198 │ 		for _, chunk := range items {
 199 │ 			cleaned, duplicateLines, blankLines := cleanContextText(chunk.Content)
 200 │ 			report.DuplicateLinesRemoved += duplicateLines
 201 │ 			report.BlankLinesCollapsed += blankLines
```

- **L182–L187**：按预算分轮，将一基 roundStart 转为零基索引；越界返回错误。
- **L188–L194**：计算窗口末尾和本次轮数，初始化已选 Chunk、轮次清单、文本及清洗报告。
- **L195–L201**：逐轮逐 Chunk 清理正文，并累计重复/空白行处理数量。

```text
 202 │ 			marker := fmt.Sprintf("[[PAGE %d]]", chunk.PageStart)
 203 │ 			if chunk.PageEnd > chunk.PageStart {
 204 │ 				marker = fmt.Sprintf("[[PAGES %d-%d]]", chunk.PageStart, chunk.PageEnd)
 205 │ 			}
 206 │ 			section := ""
 207 │ 			if strings.TrimSpace(chunk.SectionPath) != "" {
 208 │ 				section = "<!-- section: " + strings.TrimSpace(chunk.SectionPath) + " -->\n"
 209 │ 			}
 210 │ 			roundBlocks = append(roundBlocks, strings.TrimSpace(marker+"\n"+section+cleaned))
 211 │ 			cost := contextChunkTokens(chunk) + 16
 212 │ 			roundTokens += cost
 213 │ 			estimatedTokens += cost
 214 │ 			selected = append(selected, chunk)
 215 │ 		}
```

- **L202–L205**：保留单页或跨页 PAGE 标记，后续引用不依赖模型猜页码。
- **L206–L210**：保留章节信息，与页码和清洗文本组装片段。
- **L211–L215**：计算本轮/本次 Token，记录实际选中的 Chunk。

```text
 216 │ 		roundMarker := fmt.Sprintf("[[DOCUMENT ROUND %d]]", startIndex+index+1)
 217 │ 		blocks = append(blocks, roundMarker+"\n"+strings.Join(roundBlocks, "\n\n"))
 218 │ 		manifest = append(manifest, ContextRound{
 219 │ 			RoundNo: startIndex + index + 1, ChunkStart: items[0].Ordinal,
 220 │ 			ChunkEnd: items[len(items)-1].Ordinal, ChunkCount: len(items),
 221 │ 			TokenCount: roundTokens,
 222 │ 		})
 223 │ 	}
 224 │ 	coverage := float64(len(selected)) / float64(len(chunks))
 225 │ 	sourceIR, err := s.readSourceIR(ctx, userID, documentID, selected)
 226 │ 	if err != nil {
 227 │ 		return DocumentContext{}, err
 228 │ 	}
 229 │ 	return DocumentContext{
 230 │ 		DocumentID: item.ID, SourceFilename: item.Name, MediaType: item.MediaType,
 231 │ 		Format: "markdown", ParserVersion: item.ParserVersion, PageCount: item.PageCount,
 232 │ 		SelectedChunks: len(selected), TotalChunks: len(chunks), EstimatedTokens: estimatedTokens,
 233 │ 		Text: strings.Join(blocks, "\n\n"), Truncated: len(selected) < len(chunks),
```

- **L216–L223**：给每轮正文加标记，manifest 记录原 ordinal 首尾、Chunk 数与 Token 数。
- **L224**：本次覆盖率=本次选中 Chunk 数/此次读到的总 Chunk 数；它不是整个 Agent 多次调用的累计覆盖率。
- **L225–L228**：读取与当前选中 Chunk 对应的 Source IR，供结构化制品生成。
- **L229–L233**：返回文件身份、解析器版本、文本和是否尚未覆盖全部。

```text
 234 │ 		RoundCount: len(roundChunks), CompletedRounds: completedRounds,
 235 │ 		RoundStart: roundStart, NextRound: endIndex + 1, HasMore: endIndex < len(roundChunks),
 236 │ 		CoverageRatio:  coverage,
 237 │ 		CleaningReport: report, Rounds: manifest, SourceIR: sourceIR, SourceOverwritten: false,
 238 │ 	}, nil
 239 │ }
```

- **L234–L238**：返回总轮数、本次完成数、NextRound、HasMore、覆盖和清洗证据；Agent 依靠这些决定继续读取。
- **L239**：函数结束。


Python 累计覆盖需要对 Chunk 区间求并集，重复读取同一轮不能增加完成率。Source IR/并行 Composer 的下一跳在 agent_runtime.py 的 _presentation_document_batches、_merge_presentation_arguments、_document_source_coverage。

## 解析结果异步编译为 Wiki

源码：[internal/document/wiki_compiler.go:31](/Users/windcry1/Documents/ai_companion/internal/document/wiki_compiler.go:31)

<a id="symbols-6-6"></a>

### 函数与变量详解

函数/类型声明：[`CompileNextWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki_compiler.go:31)。

**职责、输入输出与影响：** 输入 ctx，返回 (是否领取处理 bool, error)。无 WikiStore 或无任务返回 false,nil；已领取则编译并保存成功/失败，可能调用配置的编译模型。

**调用关系：** 后台调度调用；ClaimWiki → compileWiki → FinishWiki。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `store / ok` | WikiStore / bool | 接口断言 | 判断当前存储是否支持 Wiki 任务，而不是用户是否有阅读权限。 |
| `job` | WikiJob | ClaimWiki | 已领取的编译工作与执行权信息，完成时传回 FinishWiki。 |
| `compileCtx / cancel` | context.Context / CancelFunc | 8 分钟 WithTimeout | 约束编译时长，使其短于注释所述 10 分钟租期。 |
| `pages` | []WikiPage | compileWiki 返回 | 待保存的派生页面，不是原文件页 Page。 |
| `err / finishErr` | error / error | 编译错误 / 标记失败错误 | errors.Join 保留两个原因，避免只看到生成失败而漏掉持久化失败。 |
| `wikiRuntime` | Service 内部计数器集合 | 服务初始化 | failures/compiles 分别记录编译失败与成功完成，不是用户消息。 |

### 源码与逐行解释

```text
  31 │ func (s *Service) CompileNextWiki(ctx context.Context) (bool, error) {
  32 │ 	store, ok := s.store.(WikiStore)
  33 │ 	if !ok {
  34 │ 		return false, nil
  35 │ 	}
  36 │ 	job, err := store.ClaimWiki(ctx, s.now().UTC())
  37 │ 	if errors.Is(err, ErrNotFound) {
  38 │ 		return false, nil
  39 │ 	}
  40 │ 	if err != nil {
  41 │ 		return false, err
  42 │ 	}
  43 │ 	// A lease is ten minutes; bounded model requests and the deadline finish first.
  44 │ 	compileCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
  45 │ 	defer cancel()
  46 │ 	pages, err := s.compileWiki(compileCtx, job)
  47 │ 	if err != nil {
  48 │ 		s.wikiRuntime.failures.Add(1)
  49 │ 		finishErr := store.FinishWiki(ctx, job, nil, "compile_failed", s.now().UTC())
  50 │ 		return true, errors.Join(err, finishErr)
```

- **L31–L35**：底层没有 WikiStore 时表示此运行环境不支持编译，返回未处理。
- **L36–L42**：领取一个 Wiki Job；没有任务不是错误，其他存储错误返回。
- **L43–L45**：租约为 10 分钟，本次编译 Context 限制 8 分钟，给收尾留时间。
- **L46–L50**：执行编译，失败累加指标并持久化 compile_failed；errors.Join 保留算法和收尾两个错误。

```text
  51 │ 	}
  52 │ 	err = store.FinishWiki(ctx, job, pages, "", s.now().UTC())
  53 │ 	if err == nil {
  54 │ 		s.wikiRuntime.compiles.Add(1)
  55 │ 	}
  56 │ 	return true, err
  57 │ }
```

- **L51–L56**：成功页面交 FinishWiki 提交；只有提交成功才计 compiles。true 表示领取过任务。
- **L57**：函数结束。


compileWiki 会验证 ready 状态及 Chunk 数等于 Document.ChunkCount；生成每个片段的证据页、来源页，再可选调用模型组织主题页。自动编译、人工修改保护和租约校验最终由 Wiki Store 提交边界约束。

## Wiki 依赖全部可见才可返回

源码：[internal/document/wiki.go:122](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:122)

<a id="symbols-6-7"></a>

### 函数与变量详解

函数/类型声明：[`ListWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:122)、[`ReadWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:170)。

**职责、输入输出与影响：** ListWiki 返回可见页面列表并标记 Stale；它本身可能包含 Stale=true 的页面。ReadWiki 查目标页并拒绝 stale，返回 (WikiPage, error)。二者不能混称为同一过滤步骤。

**调用关系：** Wiki 列表/阅读接口及工具调用；wikiDocuments → ListWikiPages → 逐来源校验；ReadWiki 再查 pageID 与 Stale。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `user / workspace` | string / string | 调用方已校验的范围 | 空 workspace 表示个人范围；成员身份应在相应入口校验。 |
| `docs / allowed` | []Document / map[string]Document | 可访问文档 / 只纳入 ready 的 ID 映射 | allowed 决定页面每个来源是否可读。 |
| `owners / seen` | []string / map[string]bool | 从可见文档收集并去重 | 减少要拉取的页面所有者；不是最终授权结论。 |
| `pages / p` | []WikiPage / WikiPage | Store 候选 / 当前页面值 | 检查 p.Sources，每个来源都要落在 allowed。 |
| `visible` | bool | 初值 len(p.Sources)>0 | 任一依赖缺失就 false；空来源页面也不直接放行。 |
| `source / d` | Wiki 来源对象 / Document | p.Sources 中一项 / allowed 查询 | 比较 SourceVersion(d) 与 source.Version，不匹配标记 p.Stale。 |
| `result` | []WikiPage | 只追加 visible 的页面 | 列表保留 stale 标记；ReadWiki 对指定 stale 页返回 ErrWikiConflict。 |

### 源码与逐行解释

```text
 122 │ func (s *Service) ListWiki(ctx context.Context, user, workspace string) ([]WikiPage, error) {
 123 │ 	docs, err := s.wikiDocuments(ctx, user, workspace)
 124 │ 	if err != nil {
 125 │ 		return nil, err
 126 │ 	}
 127 │ 	store, ok := s.store.(WikiStore)
 128 │ 	if !ok {
 129 │ 		return []WikiPage{}, nil
 130 │ 	}
 131 │ 	owners := []string{}
 132 │ 	seen := map[string]bool{}
 133 │ 	allowed := map[string]Document{}
```

- **L122–L126**：获得当前个人或 Workspace 范围内文档；Workspace 成员身份由 HTTP/Agent 边界预先验证。
- **L127–L130**：无 WikiStore 返回空页列表。
- **L131–L133**：建立作者集合与允许文档 ID 映射。

```text
 134 │ 	for _, d := range docs {
 135 │ 		if d.Status == "ready" {
 136 │ 			allowed[d.ID] = d
 137 │ 			if !seen[d.UserID] {
 138 │ 				seen[d.UserID] = true
 139 │ 				owners = append(owners, d.UserID)
 140 │ 			}
 141 │ 		}
 142 │ 	}
 143 │ 	if workspace == "" && !seen[user] {
 144 │ 		owners = append(owners, user)
 145 │ 	}
 146 │ 	pages, err := store.ListWikiPages(ctx, owners)
 147 │ 	if err != nil {
 148 │ 		return nil, err
 149 │ 	}
```

- **L134–L142**：只纳入 ready 文档；收集这些文档的作者，用于读取候选 Wiki 页。
- **L143–L145**：个人范围即使没有 ready 文档，也可列该用户候选页再做下面过滤。
- **L146–L149**：从 Store 读取候选 Wiki，存储失败返回。

```text
 150 │ 	result := []WikiPage{}
 151 │ 	for _, p := range pages {
 152 │ 		visible := len(p.Sources) > 0
 153 │ 		p.Stale = false
 154 │ 		for _, source := range p.Sources {
 155 │ 			d, ok := allowed[source.DocumentID]
 156 │ 			if !ok {
 157 │ 				visible = false
 158 │ 				break
 159 │ 			}
 160 │ 			if SourceVersion(d) != source.Version {
 161 │ 				p.Stale = true
 162 │ 			}
 163 │ 		}
 164 │ 		if visible {
 165 │ 			result = append(result, p)
 166 │ 		}
 167 │ 	}
 168 │ 	return result, nil
```

- **L150–L154**：每页先要求至少有一个来源，重置 stale 并逐一审查依赖。
- **L155–L159**：任何来源不在 allowed 中就让整页不可见：多来源页面采用权限交集。
- **L160–L163**：来源版本不一致则标记 stale，避免旧知识作为当前事实继续使用。
- **L164–L168**：仅将可见页加入列表；列表可显示 stale 状态，但直接阅读下一函数会拒绝过期页。

```text
 169 │ }
 170 │ func (s *Service) ReadWiki(ctx context.Context, user, workspace, pageID string) (WikiPage, error) {
 171 │ 	s.wikiRuntime.reads.Add(1)
 172 │ 	pages, err := s.ListWiki(ctx, user, workspace)
 173 │ 	if err != nil {
 174 │ 		return WikiPage{}, err
 175 │ 	}
 176 │ 	for _, p := range pages {
 177 │ 		if p.ID == pageID {
 178 │ 			if p.Stale {
 179 │ 				return WikiPage{}, ErrWikiConflict
 180 │ 			}
 181 │ 			return p, nil
 182 │ 		}
 183 │ 	}
 184 │ 	return WikiPage{}, ErrNotFound
 185 │ }
```

- **L169–L174**：ReadWiki 先增加读取计数，再调用同一 ListWiki 权限路径。
- **L175–L181**：找到目标页后，stale 返回 ErrWikiConflict，否则返回正文。
- **L182–L185**：不可见或不存在统一返回 NotFound。


## 人工编辑 Wiki 使用版本条件

源码：[internal/document/wiki.go:351](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:351)

<a id="symbols-6-8"></a>

### 函数与变量详解

函数/类型声明：[`UpdateWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:351)。

**职责、输入输出与影响：** 输入 user、pageID、预期 version、title/body；返回 (保存后 WikiPage, error)。只允许当前所有者编辑个人可见页面，并通过版本条件防旧编辑覆盖。

**调用关系：** 人工编辑入口 → ListWiki → 字段/归属/版本校验 → SaveWikiPage(ctx, p, version)。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `pages / p` | []WikiPage / WikiPage | ListWiki(user, "") / 当前页面 | 同时要求 p.ID=pageID、p.OwnerID=user。 |
| `version` | int | 编辑页读取时保存的版本 | 必须等于 p.Version，且传给 Store 做条件保存；不是自动采用最新版。 |
| `title / body` | string / string | 用户编辑内容 | title 按 rune 限 1–160；body 此处按字节 len 限 24000。 |
| `p.Edited / p.UpdatedAt` | bool / time.Time | 本次赋 true / s.now().UTC() | 标记人工修改和时间，而非直接重写原始文件。 |

### 源码与逐行解释

```text
 351 │ func (s *Service) UpdateWiki(ctx context.Context, user, pageID string, version int, title, body string) (WikiPage, error) {
 352 │ 	pages, err := s.ListWiki(ctx, user, "")
 353 │ 	if err != nil {
 354 │ 		return WikiPage{}, err
 355 │ 	}
 356 │ 	for _, p := range pages {
 357 │ 		if p.ID == pageID && p.OwnerID == user {
 358 │ 			if version != p.Version {
 359 │ 				return WikiPage{}, ErrWikiConflict
 360 │ 			}
 361 │ 			if len([]rune(title)) < 1 || len([]rune(title)) > 160 || len(body) > 24000 {
 362 │ 				return WikiPage{}, ErrValidation
 363 │ 			}
 364 │ 			p.Title = title
 365 │ 			p.Body = body
 366 │ 			p.Edited = true
 367 │ 			p.UpdatedAt = s.now().UTC()
 368 │ 			return s.store.(WikiStore).SaveWikiPage(ctx, p, version)
```

- **L351–L355**：编辑仅从当前用户个人可见 Wiki 集合查找。
- **L356–L360**：同时要求 pageID 匹配、OwnerID 为当前用户、客户端 version 等于当前页版本。
- **L361–L363**：标题 1–160 字符，正文最多 24000 字节；两种长度单位不同。
- **L364–L367**：写新标题/正文、设置 Edited=true、更新时间，标记人工编辑保护。
- **L368**：SaveWikiPage 再以 version 做数据库条件写入；读取后到写入前仍可能并发变化。

```text
 369 │ 		}
 370 │ 	}
 371 │ 	return WikiPage{}, ErrNotFound
 372 │ }
```

- **L369–L372**：未找到可编辑页统一返回 NotFound。


## 把代码连成一次真实行为

### 以一份 12 页报告为例

上传成功后，浏览器拿到 Document ID；只有解析完成并进入可用状态，问答检索才应把它纳入来源范围。原始文件、索引、SQL 存在多个写入边界，项目不是把这些系统放进一个全局事务。

| 阶段 | 读什么 | 写什么 | 排查时必须确认 |
|---|---|---|---|
| 上传 | 用户文件字节、文件名 | Blob 与元数据 | 归属、大小、保存状态 |
| 解析 | Blob | 页/章节/Chunk 等解析结果 | 解析错误与实际可提取内容 |
| 建索引 | 稳定 Chunk ID 和文本 | 搜索索引 | 索引写入与数据库状态是否都成功 |
| 问答 | 当前用户可见 ready 文档列表 | 本次返回的命中与引用 | 先限可见范围，再谈相关度 |
| 完整读取 | 解析结果与轮次游标 | 本次覆盖清单、下一轮信息 | 是本轮覆盖，还是已经累计全量 |
| Wiki | 合法来源版本 | 派生页面与依赖关系 | 依赖是否仍可见、是否过期 |

例如 6 个 Chunk 分两次读：第一次 {C1,C2,C3,C4}，第二次 {C4,C5,C6}。总覆盖是集合并集 6/6，不是简单把两个比例相加，重叠的 C4 只能算一次。总 Chunk 数、页数、coverage 都要区分统计口径。ReadParsedContextRoundWindow 的结果描述该次调用，不能自动当成跨调用累计状态。

### Chunk overlap 与权限的两个反例

重叠可以让相邻片段保留衔接，但它不是无成本复制：带重叠后若超过切块预算，解析器会移除相应重叠。把每片严格固定为同样字符数不是这里的完整算法，因为章节、页面与预算同时参与。

Wiki W 来自 D1 和 D2。用户只能看 D1 时，不能因为“W 的一半来源可见”就返回 W 全文。当前过滤要求依赖全部处于允许来源集合中，并检查对应版本是否仍有效。这是对派生内容的访问控制，不只是对原文件链接的访问控制。

### 自测与答案

问：搜到最相关的 5 个片段，能声称完成全书总结吗？答：不能；相关性选择没有证明覆盖完整，需继续按完整读取协议取来源。

问：上传中检查到 EICAR 标记意味着有完整杀毒引擎吗？答：不意味着；当前这一检查不能被夸大成全面恶意文件检测。

## 对照阅读与验证

问答入口是 Query，完整制品入口是 ReadParsedContextRoundWindow，两者不要互换。Wiki 搜索与链接扩展继续读 wiki.go 的 SearchWiki、FollowWikiLinks；原文件删除还要读 document/cleanup.go。验证：document/wiki_test.go、document/context_test.go、Python 文档解析与 Agent 覆盖率测试。

[返回目录](README.md) · [下一章：生活助手：记账、计划、提醒和导出](07-life-tools.md)
