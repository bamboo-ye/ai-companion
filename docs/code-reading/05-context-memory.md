# 05 普通聊天、上下文与记忆

[阅读总入口](README.md)

行为例子：用户连续对话并纠正“我现在住上海”。本章把普通 Job 路径补齐，再解释普通聊天与 Agent 如何共享历史、摘要、记忆、内置知识。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### “上下文”在本仓库有三个意思

Go context.Context 管超时、取消和请求信息；conversation_context 是带版本、来源、历史和记忆的业务快照；模型请求 messages 是某个节点实际收到的输入。前两者不是同一类型，后两者也不是逐字段原样转发。

模型不会因为应用曾保存一条消息就自动知道它。代码必须读取历史、挑选内容、组织参考资料并放进请求。所谓长期记忆是数据库中的事实记录及召回逻辑，不是本项目在每次对话后重新训练模型权重。

Token 是模型处理文字的计量单位，不能普遍等同于字、字符或字节。本项目有“用于选择内容的快速估计”与“请求前保守检查”两层；默认预算也不是外部所有模型的真实窗口。数据来源与预算元数据用于解释为什么某段信息被带入或省略。

## 普通聊天先接受、后执行

源码：[internal/conversation/service.go:320](/Users/windcry1/Documents/ai_companion/internal/conversation/service.go:320)

<a id="symbols-5-1"></a>

### 函数与变量详解

函数/类型声明：[`Send`](/Users/windcry1/Documents/ai_companion/internal/conversation/service.go:320)。

**职责、输入输出与影响：** 输入 userID、conversationID、content；返回 (Message, Job, error)。普通聊天接受入口，会持久化输入和 Job，并按策略启动或排队；不是 Agent.AcceptChat。

**调用关系：** sendMessage 的普通分支调用；ValidateContent → AcceptMessage → AppendEvent → policy/start。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `content` | string | 调用参数，去空白后使用 | 校验长度/安全规则后放入用户 Message。 |
| `messageID / jobID` | string / string | 独立生成的新 ID | message 标识发言，job 标识本次普通生成。 |
| `message / job` | Message / Job | 服务构造，再由 AcceptMessage 返回 | Job.UserMessageID 关联输入，Attempt=1；Message.completed 不等于 Job.completed。 |
| `policy` | reliability.Policy | s.policy() | AcceptOnly=true 时只接受；s.asyncDispatch 另决定是否本进程启动。 |
| `now` | time.Time | s.now().UTC() | 创建时间与 job.DeadlineAt 的计算基准。 |

### 源码与逐行解释

```text
 320 │ func (s *Service) Send(ctx context.Context, userID, conversationID, content string) (Message, Job, error) {
 321 │ 	if err := s.ValidateContent(content); err != nil {
 322 │ 		return Message{}, Job{}, err
 323 │ 	}
 324 │ 	content = strings.TrimSpace(content)
 325 │ 	messageID, err := id.New()
 326 │ 	if err != nil {
 327 │ 		return Message{}, Job{}, err
 328 │ 	}
 329 │ 	jobID, err := id.New()
 330 │ 	if err != nil {
 331 │ 		return Message{}, Job{}, err
 332 │ 	}
 333 │ 	now := s.now().UTC()
 334 │ 	message := Message{ID: messageID, ConversationID: conversationID, UserID: userID, Role: "user", Bubble: 1, Content: content, Status: "completed", CreatedAt: now, CompletedAt: &now}
 335 │ 	job := Job{ID: jobID, ConversationID: conversationID, UserMessageID: messageID, Status: "accepted", Attempt: 1, DeadlineAt: now.Add(s.timeout), CreatedAt: now}
 336 │ 	message, job, err = s.store.AcceptMessage(ctx, userID, conversationID, message, job)
 337 │ 	if err != nil {
 338 │ 		return Message{}, Job{}, err
 339 │ 	}
```

- **L320–L323**：先调用统一内容校验；失败不产生 Message/Job。
- **L324**：去掉首尾空白。
- **L325–L328**：生成用户消息 ID。
- **L329–L332**：生成独立 Job ID，后续取消/重试操作指向 Job。
- **L333**：固定 UTC 时间。
- **L334**：用户输入已经完整到达，所以消息为 completed。
- **L335**：Job 初始 accepted，记录原用户消息、attempt=1 和截止时间。
- **L336–L339**：Store.AcceptMessage 原子保存消息及 Job，返回已分配序列的信息。

```text
 340 │ 	_, _ = s.store.AppendEvent(ctx, job.ID, "accepted", map[string]any{"message": message, "job": job}, now)
 341 │ 	policy := s.policy()
 342 │ 	if policy.AcceptOnly {
 343 │ 		_, _ = s.store.AppendEvent(ctx, job.ID, "degraded_accept_only", map[string]any{"policy": policy}, now)
 344 │ 		return message, job, nil
 345 │ 	}
 346 │ 	if !s.asyncDispatch {
 347 │ 		s.start(userID, job.ID)
 348 │ 	}
 349 │ 	return message, job, nil
 350 │ }
```

- **L340**：追加 accepted 事件；这里两个返回值都被忽略，因此不能说本行事件与上一步始终是同一事务，也不能说失败会撤销已接受消息。
- **L341–L345**：L3 AcceptOnly 时记录降级事件并返回，任务已保存但本调用不启动生成。
- **L346–L348**：只有 asyncDispatch=false 才在当前进程 start；否则由 Worker 领取。start 仍是后台执行，不是 HTTP 同步等完整回答。
- **L349–L350**：返回已接受 Message/Job，HTTP 层把它们封装为 202。


执行续读：runClaimed 加载角色 → ContextBuilder → 选择工具/执行 → 生成回复 → 安全检查 → 拆气泡 → 记录用量与终态。SSE 返回的是持久化事件/气泡，不应自动理解成模型原始 token 逐字透传。

## 历史按完整消息组选择

源码：[internal/conversation/context.go:157](/Users/windcry1/Documents/ai_companion/internal/conversation/context.go:157)

<a id="symbols-5-2"></a>

### 函数与变量详解

函数/类型声明：[`selectRecentMessages`](/Users/windcry1/Documents/ai_companion/internal/conversation/context.go:157)。

**职责、输入输出与影响：** 输入历史 messages、预算 budget、forceProgress；返回 (recent, tokens, dropped)。纯选择函数，不删除数据库历史、不请求模型；dropped 是待摘要的旧消息。

**调用关系：** 上下文构建器使用；groupMessages 分组 → messageGroupTokens 估算 → flattenMessageGroups 展平。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `messages` | []Message | 按顺序的历史输入 | 当前函数只选择，不在数据库中移除旧消息。 |
| `budget / forceProgress` | int / bool | 构建器预算和强制推进标志 | 后者在全部可保留且组数大于 1 时主动挪出前半组做摘要。 |
| `groups` | [][]Message | groupMessages(messages) | 按同 sequence 聚合，组内可含多个气泡，不固定等于一问一答。 |
| `keptFrom` | int | 初值 len(groups)，倒序选择时向前移 | 保留范围为 groups[keptFrom:]，不是消息的物理行号。 |
| `tokens / groupTokens` | int / int | 累计 0 / 每组估计 | 第一个最新完整组即使超 budget 仍保留；随后再判断超限。 |
| `recent / dropped` | []Message / []Message | 将保留区/之前区展平 | recent 供当前输入；dropped 可参与摘要，不表示永久丢弃。 |

### 源码与逐行解释

```text
 157 │ func selectRecentMessages(messages []Message, budget int, forceProgress bool) ([]Message, int, []Message) {
 158 │ 	if len(messages) == 0 {
 159 │ 		return nil, 0, nil
 160 │ 	}
 161 │ 	groups := groupMessages(messages)
 162 │ 	keptFrom := len(groups)
 163 │ 	tokens := 0
 164 │ 	for index := len(groups) - 1; index >= 0; index-- {
 165 │ 		groupTokens := messageGroupTokens(groups[index])
 166 │ 		if keptFrom < len(groups) && tokens+groupTokens > budget {
 167 │ 			break
 168 │ 		}
 169 │ 		tokens += groupTokens
 170 │ 		keptFrom = index
 171 │ 	}
```

- **L157–L160**：空历史直接返回三个空结果：recent、token 数、待摘要 dropped。
- **L161**：groupMessages 把相同 sequence 的气泡放在一组。
- **L162–L163**：初始化保留起点和估算用量。
- **L164–L165**：从最新组向最旧组倒着计算成本。
- **L166–L168**：至少留下一组后才执行预算截断；因此最新组会完整保留，即使其自身超过选择预算。
- **L169–L171**：累加已保留成本并移动起点。

```text
 172 │ 	if forceProgress && keptFrom == 0 && len(groups) > 1 {
 173 │ 		keptFrom = len(groups) / 2
 174 │ 		tokens = 0
 175 │ 		for _, group := range groups[keptFrom:] {
 176 │ 			tokens += messageGroupTokens(group)
 177 │ 		}
 178 │ 	}
 179 │ 	recent := flattenMessageGroups(groups[keptFrom:])
 180 │ 	dropped := flattenMessageGroups(groups[:keptFrom])
 181 │ 	return recent, tokens, dropped
 182 │ }
```

- **L172–L178**：若强制推进摘要且所有组都还保留，至少移出前半组，重新统计后半组 Token；这是摘要进度保护。
- **L179–L181**：展开两段组：recent 进入当前历史，dropped 供滚动摘要处理。
- **L182**：函数结束。


这个预算是历史选择预算，不是最终模型窗口保证。当前请求、工具定义、输出预留等还要由模型调用处做完整请求检查。

## 两个执行路径共用 BuildContext

源码：[internal/conversation/context_snapshot.go:18](/Users/windcry1/Documents/ai_companion/internal/conversation/context_snapshot.go:18)

<a id="symbols-5-3"></a>

### 函数与变量详解

函数/类型声明：[`BuildContext`](/Users/windcry1/Documents/ai_companion/internal/conversation/context_snapshot.go:18)、[`contextSnapshot`](/Users/windcry1/Documents/ai_companion/internal/conversation/context_snapshot.go:29)。

**职责、输入输出与影响：** BuildContext 输入用户、会话、query，返回 (contextengine.Snapshot, error)，先查归属再构建历史。contextSnapshot 将 ContextResult、记忆与产品知识合并成快照；历史构建可能更新摘要，召回可能调用配置的语义服务。

**调用关系：** 普通聊天/Agent 接受共用；GetConversation → contextBuilder.Build → contextSnapshot → productknowledge.Context / RecallContext / BoundMemories。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `query` | string | 当前请求文本 | 用于召回相关资料；Agent intake 当前消息尚未入库，不额外重复塞入 History。 |
| `result` | ContextResult | contextBuilder.Build | 含 Messages、Summary、RecentTokens 等，不是模型回答。 |
| `recall` | bool | s.policy().UseFullRAG | 决定是否尝试长期记忆；内置产品知识在此之前仍可注入。 |
| `snapshot` | contextengine.Snapshot | 初始化空 History/Memories/Manifest | 逐步追加历史、摘要、知识与记忆，是跨 Go/Python 的版本化数据。 |
| `summary` | *ConversationSummary | result.Summary | 可能 nil；非 nil 时转为带来源和范围的 Item。 |
| `candidates` | []contextengine.Item | RecallContext 或兼容文本 Recall | 先取得候选，再做预算筛选，不全都发给模型。 |
| `memories / ok` | sourcedMemoryContext / bool | 接口能力断言 | 有来源召回优先；不支持时用文本接口兼容。 |
| `texts` | []string | 旧 Recall 接口 | 包装为来源 kind=memory 的 Item，但未凭空补真实源消息 ID。 |
| `err` | error | 记忆召回错误 | 此处记录 memory_unavailable 降级并返回已有快照，不把整个行为都当失败。 |

### 源码与逐行解释

```text
  18 │ func (s *Service) BuildContext(ctx context.Context, userID, conversationID, query string) (contextengine.Snapshot, error) {
  19 │ 	if _, err := s.store.GetConversation(ctx, userID, conversationID); err != nil {
  20 │ 		return contextengine.Snapshot{}, err
  21 │ 	}
  22 │ 	result, err := s.contextBuilder.Build(ctx, userID, conversationID)
  23 │ 	if err != nil {
  24 │ 		return contextengine.Snapshot{}, err
  25 │ 	}
  26 │ 	return s.contextSnapshot(ctx, userID, conversationID, query, result, s.policy().UseFullRAG), nil
  27 │ }
  28 │
  29 │ func (s *Service) contextSnapshot(ctx context.Context, userID, conversationID, query string, result ContextResult, recall bool) contextengine.Snapshot {
  30 │ 	snapshot := contextengine.Snapshot{
  31 │ 		Version: contextengine.Version, History: []contextengine.Message{}, Memories: []contextengine.Item{},
  32 │ 		Manifest: contextengine.Manifest{Version: contextengine.Version, Sources: []contextengine.Source{}, RecentTokens: result.RecentTokens},
  33 │ 	}
  34 │ 	for _, item := range result.Messages {
  35 │ 		if item.Role != "user" && item.Role != "assistant" {
  36 │ 			continue
  37 │ 		}
```

- **L18–L21**：先验证当前用户有权读取会话；不能仅凭 conversationID 构建上下文。
- **L22–L25**：ContextBuilder 负责近期历史和滚动摘要，错误传递给发送入口。
- **L26–L27**：按当前可靠性策略决定是否召回记忆，然后输出统一 Snapshot。
- **L28–L33**：初始化版本、空历史/记忆和诊断 manifest，避免 null/缺失数组形成多种协议形态。
- **L34–L37**：只允许用户和助手历史进入快照。

```text
  38 │ 		snapshot.History = append(snapshot.History, contextengine.Message{ID: item.ID, Role: item.Role, Content: item.Content, Sequence: item.Sequence, Bubble: item.Bubble})
  39 │ 		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, contextengine.Source{Kind: "message", ID: item.ID, ConversationID: conversationID, StartSequence: item.Sequence, EndSequence: item.Sequence})
  40 │ 	}
  41 │ 	if summary := result.Summary; summary != nil {
  42 │ 		snapshot.Summary = &contextengine.Item{Content: summary.Content, EstimatedTokens: summary.TokenCount, Source: contextengine.Source{
  43 │ 			Kind: "summary", ID: summary.ID, ConversationID: conversationID, Version: strconv.Itoa(summary.Version),
  44 │ 			StartSequence: summary.StartSequence, EndSequence: summary.EndSequence, UpdatedAt: &summary.CreatedAt,
  45 │ 		}}
  46 │ 		snapshot.Manifest.SummaryTokens = summary.TokenCount
  47 │ 		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, snapshot.Summary.Source)
  48 │ 	}
  49 │ 	// Local product help has no remote RAG dependency and remains available in
  50 │ 	// degraded mode. It never includes a user's private uploaded documents.
  51 │ 	snapshot.Knowledge, snapshot.Manifest.KnowledgeTokens, snapshot.Manifest.KnowledgeOmitted = productknowledge.Context(query, snapshot.History)
  52 │ 	for _, item := range snapshot.Knowledge {
  53 │ 		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, item.Source)
  54 │ 	}
```

- **L38**：保留消息 ID、角色、正文、sequence、bubble，Python 后续可按完整语义组选择。
- **L39–L40**：给每条历史追加来源记录，记录会话和序号边界。
- **L41–L45**：把摘要正文、Token 数、版本、覆盖序号和更新时间一起带入快照，保留来源边界。
- **L46–L48**：更新摘要用量与 manifest 来源。
- **L49–L51**：内置产品知识是本地检索，即使外部 RAG 降级仍可用；它不包含个人上传资料。
- **L52–L54**：把知识来源也写入 manifest。

```text
  55 │ 	if !recall {
  56 │ 		snapshot.Manifest.Degraded = append(snapshot.Manifest.Degraded, "memory_disabled_by_policy")
  57 │ 		return snapshot
  58 │ 	}
  59 │ 	var candidates []contextengine.Item
  60 │ 	var err error
  61 │ 	if memories, ok := s.memories.(sourcedMemoryContext); ok {
  62 │ 		candidates, err = memories.RecallContext(ctx, userID, query, 8)
  63 │ 	} else {
  64 │ 		var texts []string
  65 │ 		texts, err = s.memories.Recall(ctx, userID, query, 8)
  66 │ 		for _, content := range texts {
  67 │ 			candidates = append(candidates, contextengine.Item{Content: content, Source: contextengine.Source{Kind: "memory"}})
  68 │ 		}
  69 │ 	}
  70 │ 	if err != nil {
  71 │ 		snapshot.Manifest.Degraded = append(snapshot.Manifest.Degraded, "memory_unavailable")
  72 │ 		return snapshot
  73 │ 	}
  74 │ 	snapshot.Memories, snapshot.Manifest.MemoryTokens, snapshot.Manifest.MemoryOmitted = contextengine.BoundMemories(candidates, contextengine.DefaultMemoryTokens)
```

- **L55–L58**：不允许 recall 时留下 memory_disabled_by_policy 原因并返回，不会连带关闭本地知识。
- **L59–L62**：优先使用带来源的 RecallContext，最多召回 8 条候选。
- **L63–L69**：兼容旧 MemoryContext：仅有字符串时包装成 memory 类型来源；信息量不及新协议。
- **L70–L73**：记忆调用失败写入 degraded 原因后返回其余上下文，聊天不一定因此失败。
- **L74**：BoundMemories 按预算选择完整事实，返回选中项、估算用量和省略数。

```text
  75 │ 	for _, item := range snapshot.Memories {
  76 │ 		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, item.Source)
  77 │ 	}
  78 │ 	return snapshot
  79 │ }
```

- **L75–L79**：记录最终选中的来源并返回 Snapshot。


## 召回记忆：相关性、时效和重要度

源码：[internal/memory/service.go:157](/Users/windcry1/Documents/ai_companion/internal/memory/service.go:157)

<a id="symbols-5-4"></a>

### 函数与变量详解

函数/类型声明：[`RecallContext`](/Users/windcry1/Documents/ai_companion/internal/memory/service.go:157)。

**职责、输入输出与影响：** 输入 userID、query、limit；返回 ([]contextengine.Item, error)。只读取当前用户最多 500 条记忆并排序筛选，保留来源；不是把查询保存成一条新记忆。

**调用关系：** contextSnapshot 调用；ListMemories → semanticScores/tokens → 加权排序 → 限量转换。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `items` | []Memory | ListMemories(ctx, userID, 500) | 每条还检查 Status、ValidFrom、ValidTo，不以存在就视为当前有效。 |
| `queryTokens / memoryTokens` | map[string]bool | tokens(query)/tokens(item.Content) | 这是词法特征集合，不是模型 Token 数或 tokenizer 输出。 |
| `overlap` | int | 每条候选从 0 计 | 统计命中的特征个数，再除以查询特征数形成词法相关度。 |
| `semanticScores` | map[string]float64 | 可选语义检索结果 | 按记忆 ID 查分；与词法得分取较大值，不简单相加。 |
| `semantic / ageDays / recency / score` | float64 | 相关度 / 更新时间差 / 指数衰减 / 加权结果 | score=.55*semantic+.25*recency+.20*Importance，Pinned 再加 .35；不是置信概率。 |
| `matches` | []scored，局部结构含 item 与 score | 空列表逐条追加 | 稳定排序后再按 limit 取前几条。 |
| `limit` | int | 调用方指定 | 非 1–20 时回退 8，约束结果条数而非输入窗口。 |
| `result` | []contextengine.Item | 选中 Memory 转换 | Content 加来源 ID、会话、消息、更新时间；供下一层预算选择。 |

### 源码与逐行解释

```text
 157 │ func (s *Service) RecallContext(ctx context.Context, userID, query string, limit int) ([]contextengine.Item, error) {
 158 │ 	items, err := s.store.ListMemories(ctx, userID, 500)
 159 │ 	if err != nil {
 160 │ 		return nil, err
 161 │ 	}
 162 │ 	semanticScores := s.semanticScores(ctx, userID, query, items)
 163 │ 	queryTokens := tokens(query)
 164 │ 	type scored struct {
 165 │ 		item  Memory
 166 │ 		score float64
 167 │ 	}
 168 │ 	matches := []scored{}
 169 │ 	now := s.now().UTC()
 170 │ 	for _, item := range items {
 171 │ 		if item.Status != "active" || item.ValidFrom.After(now) || (item.ValidTo != nil && !item.ValidTo.After(now)) {
 172 │ 			continue
 173 │ 		}
```

- **L157–L161**：只读取当前用户的最多 500 条记忆；存储失败返回错误。
- **L162**：可选 Embedding 路径生成语义分数；未配置或失败时 semanticScores 返回空分数，词法仍可运行。
- **L163**：当前问题分词。
- **L164–L169**：定义候选+分数结构，初始化结果与当前 UTC 时间。
- **L170–L173**：过滤非 active、未来才生效、已经结束有效期的事实。

```text
 174 │ 		memoryTokens := tokens(item.Content)
 175 │ 		overlap := 0
 176 │ 		for token := range memoryTokens {
 177 │ 			if queryTokens[token] {
 178 │ 				overlap++
 179 │ 			}
 180 │ 		}
 181 │ 		if overlap == 0 && semanticScores[item.ID] < .5 && !item.Pinned {
 182 │ 			continue
 183 │ 		}
 184 │ 		semantic := 0.0
 185 │ 		if len(queryTokens) > 0 {
 186 │ 			semantic = math.Min(1, float64(overlap)/float64(len(queryTokens)))
 187 │ 		}
 188 │ 		semantic = math.Max(semantic, semanticScores[item.ID])
 189 │ 		ageDays := now.Sub(item.UpdatedAt).Hours() / 24
 190 │ 		if ageDays < 0 {
 191 │ 			ageDays = 0
 192 │ 		}
 193 │ 		recency := math.Exp(-ageDays / memoryHalfLifeDays(item.Type))
```

- **L174–L180**：统计记忆词与查询词重叠数。
- **L181–L183**：既无词法命中、语义分数低于 .5 又未置顶的候选被丢弃。
- **L184–L188**：词法归一化为比例，再取它与 Embedding 分数的较大值；变量名 semantic 在这里表示融合前的相关性值。
- **L189–L193**：更新时间换算天数，未来时间钳制为 0，再计算指数衰减。函数叫 halfLife，但此公式是 exp(-age/参数)，不要直接当作严格数学半衰期公式。

```text
 194 │ 		score := .55*semantic + .25*recency + .20*item.Importance
 195 │ 		if item.Pinned {
 196 │ 			score += .35
 197 │ 		}
 198 │ 		if semantic > 0 || item.Pinned {
 199 │ 			matches = append(matches, scored{item, score})
 200 │ 		}
 201 │ 	}
 202 │ 	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
 203 │ 	if limit <= 0 || limit > 20 {
 204 │ 		limit = 8
 205 │ 	}
 206 │ 	result := []contextengine.Item{}
 207 │ 	for _, match := range matches {
 208 │ 		item := match.item
 209 │ 		result = append(result, contextengine.Item{Content: item.Content, EstimatedTokens: contextengine.EstimateTokens(item.Content), Source: contextengine.Source{
 210 │ 			Kind: "memory", ID: item.ID, ConversationID: item.SourceConversationID,
 211 │ 			MessageID: item.SourceMessageID, UpdatedAt: &item.UpdatedAt,
 212 │ 		}})
```

- **L194**：综合分数为 .55×相关性 + .25×时效 + .20×重要度。
- **L195–L197**：置顶额外加 .35。
- **L198–L201**：有相关性或被置顶才加入候选结果。
- **L202**：稳定降序排序，相同分数保留原次序。
- **L203–L205**：limit 无效时用 8，上限 20。
- **L206–L212**：输出完整事实、Token 估算、原消息和会话来源。

```text
 213 │ 		if len(result) >= limit {
 214 │ 			break
 215 │ 		}
 216 │ 	}
 217 │ 	return result, nil
 218 │ }
```

- **L213–L218**：达到条数限制停止；返回的是带证据归属的 contextengine.Item。


## 用户纠正事实时保留替代链

源码：[internal/memory/semantic.go:69](/Users/windcry1/Documents/ai_companion/internal/memory/semantic.go:69)

<a id="symbols-5-5"></a>

### 函数与变量详解

函数/类型声明：[`CorrectFromMessage`](/Users/windcry1/Documents/ai_companion/internal/memory/semantic.go:69)。

**职责、输入输出与影响：** 输入 user、memoryID、新 content 与来源 conversationID/messageID；返回 (新 Memory, error)。构造替代事实并调用事务 UpsertMemory，保留旧事实关系。

**调用关系：** 显式纠正领域路径使用；GetMemory → normalize/hash/classify/sensitivity → UpsertMemory。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `user / memoryID` | string / string | 用户身份与被纠正事实 ID | 两者共同查旧事实，不能凭 memoryID 跨用户改写。 |
| `content` | string | 纠正后的新正文 | 去空白、长度校验，再计算规范化哈希与类别。 |
| `old` | Memory 值 | GetMemory 返回 | 名字易误导：随后被改为“新记录模板”；先 old.SupersedesID=old.ID，再 old.ID=nextID。 |
| `nextID` | string | id.New() | 真正新事实 ID；并非沿用被替代记录 ID。 |
| `conversationID / messageID` | string / string | 本次纠正的证据来源 | 写 SourceConversationID/SourceMessageID；不是记忆 ID。 |
| `now` | time.Time | s.now().UTC() | 新事实 ValidFrom、CreatedAt、UpdatedAt；ValidTo 清 nil。 |
| `saved` | Memory | UpsertMemory 返回 | 以存储最终返回结果为准；第二个 bool 在这里被忽略，但 err 仍返回。 |

### 源码与逐行解释

```text
  69 │ func (s *Service) CorrectFromMessage(ctx context.Context, user, memoryID, content, conversationID, messageID string) (Memory, error) {
  70 │ 	content = strings.TrimSpace(content)
  71 │ 	old, err := s.store.GetMemory(ctx, user, memoryID)
  72 │ 	if err != nil {
  73 │ 		return Memory{}, err
  74 │ 	}
  75 │ 	if len([]rune(content)) < 2 || len([]rune(content)) > 2000 {
  76 │ 		return Memory{}, ErrValidation
  77 │ 	}
  78 │ 	nextID, err := id.New()
  79 │ 	if err != nil {
  80 │ 		return Memory{}, err
  81 │ 	}
  82 │ 	now := s.now().UTC()
  83 │ 	old.SourceConversationID = conversationID
  84 │ 	old.SourceMessageID = messageID
  85 │ 	old.SupersedesID = old.ID
```

- **L69–L74**：清理新内容并按用户查旧 Memory，无法访问时拒绝。
- **L75–L77**：新事实为 2–2000 字符。
- **L78–L81**：生成新的 Memory ID。
- **L82–L84**：记录纠正时间及这次纠正的会话/消息来源。
- **L85**：先保存原 ID 到 SupersedesID，形成“新事实替代旧事实”的指针。

```text
  86 │ 	old.ID = nextID
  87 │ 	old.Content = content
  88 │ 	old.NormalizedHash = hash(normalize(content))
  89 │ 	old.Type = classify(content)
  90 │ 	old.Sensitivity = sensitivity(content)
  91 │ 	old.ValidFrom = now
  92 │ 	old.ValidTo = nil
  93 │ 	old.CreatedAt = now
  94 │ 	old.UpdatedAt = now
  95 │ 	saved, _, err := s.store.UpsertMemory(ctx, old)
  96 │ 	return saved, err
  97 │ }
```

- **L86–L90**：改为新 ID、正文、规范化哈希、分类和敏感性；变量 old 此时已是新记录的构造模板。
- **L91–L94**：新事实现在开始生效，没有结束时间，设置新建/更新时间。
- **L95–L97**：UpsertMemory 负责原子替代处理；不要把本函数当成简单 UPDATE 旧正文。


## 纠正记忆的事务：关闭旧事实与插入新事实

源码：[internal/persistence/postgresstore/memory.go:14](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/memory.go:14)

纠正链不等于所有编辑：Correct 走“新记录替代旧记录”，而普通 UpdateMemory 是同一记录更新。要按调用入口区分。

<a id="symbols-5-6"></a>

### 函数与变量详解

函数/类型声明：[`UpsertMemory`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/memory.go:14)。

**职责、输入输出与影响：** 输入 item memory.Memory；返回 (结果 Memory, 是否新建 bool, error)。可普通去重，也可同事务关闭旧事实并插入替代事实。返回 true 仍须看 Commit 是否出错。

**调用关系：** 记忆创建/纠正领域方法 → UpsertMemory；BeginTx → 旧事实 UPDATE → 查重/INSERT → Commit。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `item` | memory.Memory | 待保存事实 | SupersedesID 非空才进入替代分支；UserID 限制归属，NormalizedHash 用于内容去重。 |
| `tx` | *sql.Tx | BeginTx | 旧事实关闭与新事实插入共用；中途 return 的 defer 负责回滚。 |
| `result / n` | sql.Result / int64 | 旧事实 UPDATE / RowsAffected | 必须恰好改变一条旧 active 事实，零行不能继续当作成功。 |
| `replaceErr` | error | 旧事实 UPDATE | 与后面查重 err 区别，指出失败发生在关闭旧事实阶段。 |
| `existing` | memory.Memory | 按 user+normalized_hash 查 active 记录 | 普通重复返回此记录且 bool=false；替代与已有活动内容冲突则报错。 |
| `err` | error | 查询、插入或提交结果 | 仅 sql.ErrNoRows 表示可以新插入；其他查询错误不能当“没有重复”。 |

### 源码与逐行解释

```text
  14 │ func (s *Store) UpsertMemory(ctx context.Context, item memory.Memory) (memory.Memory, bool, error) {
  15 │ 	tx, err := s.db.BeginTx(ctx, nil)
  16 │ 	if err != nil {
  17 │ 		return item, false, err
  18 │ 	}
  19 │ 	defer tx.Rollback()
  20 │ 	if item.SupersedesID != "" {
  21 │ 		result, replaceErr := tx.ExecContext(ctx, `UPDATE app.long_term_memories SET status='superseded',valid_to=$1,updated_at=$1 WHERE id=$2 AND user_id=$3 AND status='active'`, item.ValidFrom, item.SupersedesID, item.UserID)
  22 │ 		if replaceErr != nil {
  23 │ 			return item, false, replaceErr
  24 │ 		}
  25 │ 		if n, _ := result.RowsAffected(); n != 1 {
  26 │ 			return item, false, memory.ErrNotFound
  27 │ 		}
  28 │ 	}
```

- **L14**：返回值依次是 Memory、是否新建、错误；不能把第二个 bool 当作整个操作是否成功。
- **L15–L18**：开启事务，失败返回原 item、false 与错误。
- **L19**：任何提前返回都兜底回滚；后面成功 Commit 后不会被它撤销。
- **L20**：SupersedesID 非空，说明本次新事实要替代某条旧事实。
- **L21**：把属于同一用户且仍 active 的旧事实改为 superseded；valid_to 等于新事实生效时刻。参数 $1 在两个列位置复用。
- **L22–L24**：SQL 执行失败直接结束，尚未提交的修改会回滚。
- **L25–L27**：必须恰好改变一行；零行意味着旧事实不存在、无权访问或已被其他请求替代，不能继续插入。
- **L28**：结束可选的替代步骤。

```text
  29 │ 	existing, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+`
  30 │ 		WHERE user_id=$1 AND normalized_hash=$2 AND status='active'
  31 │ 		LIMIT 1
  32 │ 		FOR UPDATE`,
  33 │ 		item.UserID, item.NormalizedHash,
  34 │ 	))
  35 │ 	if err == nil {
  36 │ 		if item.SupersedesID != "" {
  37 │ 			return item, false, memory.ErrValidation
  38 │ 		}
  39 │ 		return existing, false, nil
  40 │ 	}
  41 │ 	if !errors.Is(err, sql.ErrNoRows) {
  42 │ 		return item, false, err
  43 │ 	}
  44 │ 	_, err = tx.ExecContext(ctx, `
  45 │ 		INSERT INTO app.long_term_memories (
  46 │ 			id,user_id,memory_type,content,normalized_hash,source_conversation_id,
  47 │ 			source_message_id,confidence,importance,sensitivity,pinned,status,
  48 │ 			valid_from,valid_to,supersedes_id,created_at,updated_at
```

- **L29–L34**：按用户和规范化内容哈希找仍 active 的同内容事实，并锁住查到的行；LIMIT 1 只取一条。查不到行不会锁住一个虚构的不存在行。
- **L35**：err 为 nil 表示查到了已有事实。
- **L36–L38**：若又要求替代又撞到已有活动事实，返回 Validation；defer 会撤销刚才关闭旧事实的 UPDATE。
- **L39–L40**：普通重复插入则复用 existing，false 表示未新建；这里没有待提交的新写入。
- **L41–L43**：只有“查不到行”是允许继续 INSERT 的情况，网络/数据库故障必须返回，不能伪装成不存在。
- **L44–L48**：构造 INSERT 的目标列；source 字段保留事实来源，valid 字段保存时间区间。

```text
  49 │ 		) VALUES (
  50 │ 			$1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,$8,$9,
  51 │ 			$10,$11,$12,$13,$14,NULLIF($15,'')::uuid,$16,$17
  52 │ 		)`,
  53 │ 		item.ID, item.UserID, item.Type, item.Content, item.NormalizedHash,
  54 │ 		item.SourceConversationID, item.SourceMessageID, item.Confidence,
  55 │ 		item.Importance, item.Sensitivity, item.Pinned, item.Status,
  56 │ 		item.ValidFrom, item.ValidTo, item.SupersedesID, item.CreatedAt,
  57 │ 		item.UpdatedAt,
  58 │ 	)
  59 │ 	if err != nil {
  60 │ 		return item, false, err
  61 │ 	}
  62 │ 	return item, true, tx.Commit()
  63 │ }
```

- **L49–L52**：列顺序与 $1 到 $17 对齐；空可选 ID 经 NULLIF 变为 NULL，再转 uuid。
- **L53–L57**：逐个绑定 item 的值；用户内容作为参数传递，不作为 SQL 语法执行。
- **L58**：结束执行调用。
- **L59–L61**：新记录插入失败时返回错误，也回滚之前旧事实的状态修改。
- **L62**：先调用 tx.Commit() 再形成返回值；调用者必须检查第三个返回值。true 只是走到了新建分支，Commit 仍可能报错。
- **L63**：事务函数结束。


## 记忆预算怎样做到不截断事实

源码：[internal/contextengine/context.go:72](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:72)

<a id="symbols-5-7"></a>

### 函数与变量详解

函数/类型声明：[`BoundMemories`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:72)、[`ReferenceText`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:93)、[`EstimateTokens`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:105)。

**职责、输入输出与影响：** BoundMemories 返回 (完整事实列表, 估算成本, 未入选数)；Snapshot.ReferenceText 返回参考文本；EstimateTokens 返回 int 估计。三者不读写数据库、不调用模型。

**调用关系：** 构建快照时筛事实，组织消息时生成参考 JSON；两个估算口径分别用于选择正文与含来源的整项成本。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `items / item` | []Item / Item | 已排序候选 / range 当前值 | item 有 Content、Source、EstimatedTokens；这里只修改副本并追加 selected。 |
| `budget / selected / tokens` | int / []Item / int | 调用预算 / 空切片 / 0 | 完整一项放得下才入选；tokens 只累加入选项 cost。 |
| `seen` | map[string]bool | 空 map | 以 Content 去重，预算判断前即标记，重复项不反复尝试。 |
| `encoded / cost` | []byte / int | 整 Item 的 JSON / EstimateTokens+4 | 包括来源开销，与 item.EstimatedTokens 的纯正文估算不同。 |
| `s` | Snapshot | ReferenceText 的值接收者 | 读取 Summary/Memories/Knowledge，均空时返回空串。 |
| `value / r` | string / rune | EstimateTokens 输入 / 逐个 Unicode 码点 | 不是逐字节遍历，也不等于提供商 tokenizer。 |
| `count / other` | int / int | 均从 0 计 | count 记中日韩文字，other 记其余码点，返回 count+(other+3)/4。 |

### 源码与逐行解释

```text
  72 │ func BoundMemories(items []Item, budget int) ([]Item, int, int) {
  73 │ 	selected := make([]Item, 0, len(items))
  74 │ 	tokens := 0
  75 │ 	seen := map[string]bool{}
  76 │ 	for _, item := range items {
  77 │ 		if item.Content == "" || seen[item.Content] {
  78 │ 			continue
  79 │ 		}
  80 │ 		seen[item.Content] = true
  81 │ 		encoded, _ := json.Marshal(item)
  82 │ 		cost := EstimateTokens(string(encoded)) + 4
  83 │ 		if tokens+cost > budget {
  84 │ 			continue
  85 │ 		}
  86 │ 		item.EstimatedTokens = EstimateTokens(item.Content)
  87 │ 		selected = append(selected, item)
  88 │ 		tokens += cost
  89 │ 	}
  90 │ 	return selected, tokens, len(items) - len(selected)
  91 │ }
```

- **L72**：输入已经按优先级排列的事实与预算；返回选中事实、累计估算成本、未入选数量。
- **L73**：make 创建长度为 0、容量最多可容纳所有输入的切片；容量不是已有元素数。
- **L74**：累计成本从 0 开始。
- **L75**：map 的键为内容，值为是否看过；不存在键读取 bool 零值 false。
- **L76**：range 每次取一个 item 值；_ 忽略索引。
- **L77–L79**：空文本或重复内容直接 continue，只跳过当前轮，不退出循环。
- **L80**：即使后面因预算落选，也先标记为已看过；相同内容不会再次尝试。
- **L81**：把整个 Item 序列化，包含来源等字段；此具体结构没有不可序列化字段，代码丢弃 error。
- **L82**：入选成本按整个 JSON 估算，再加 4；不是只计算 Content 的字数。
- **L83–L85**：放不下就跳过；这里是 continue 而非 break，后面较短事实仍可能入选。
- **L86**：写入内容自身的 EstimatedTokens，注意它与包含来源的入选 cost 不是同一个数。
- **L87**：append 返回更新后的切片，必须接回 selected。
- **L88**：只有入选的事实才占累计预算。
- **L89–L91**：循环结束并返回；未入选数包括空、重复和预算不足的项，不能全解释为预算超限。

```text
  92 │
  93 │ func (s Snapshot) ReferenceText() string {
  94 │ 	if s.Summary == nil && len(s.Memories) == 0 && len(s.Knowledge) == 0 {
  95 │ 		return ""
  96 │ 	}
  97 │ 	encoded, _ := json.Marshal(struct {
  98 │ 		Summary   *Item  `json:"summary,omitempty"`
  99 │ 		Memories  []Item `json:"memories"`
 100 │ 		Knowledge []Item `json:"knowledge,omitempty"`
 101 │ 	}{s.Summary, s.Memories, s.Knowledge})
 102 │ 	return "会话参考资料：\n" + string(encoded)
 103 │ }
 104 │
 105 │ func EstimateTokens(value string) int {
 106 │ 	count, other := 0, 0
 107 │ 	for _, r := range value {
 108 │ 		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
 109 │ 			count++
```

- **L92–L93**：接下来把 Snapshot 中的参考资料转成一段字符串。
- **L94–L96**：没有摘要、记忆、知识时返回空，不增加无意义参考消息。
- **L97–L101**：构造匿名结构体再序列化，只包含参考资料字段；omitempty 使空的可选字段不出现在 JSON 中。
- **L102–L103**：拼接明确的资料前缀；这不是将历史事实升级为用户操作授权。
- **L104–L105**：下一个函数是预算估计器，不是模型提供商的真实 tokenizer。
- **L106**：分别统计中日韩文字数量 count 和其他 Unicode 码点数量 other。
- **L107**：Go 遍历 string 时逐个解码 rune；不是逐个 UTF-8 字节。
- **L108–L109**：命中列出的 Unicode 字符分类就按 1 计数。

```text
 110 │ 		} else {
 111 │ 			other++
 112 │ 		}
 113 │ 	}
 114 │ 	return count + (other+3)/4
 115 │ }
```

- **L110–L112**：其余字符累计到 other。
- **L113**：遍历结束。
- **L114–L115**：非负整数 (other+3)/4 相当于向上取整 other/4；与 count 相加作为选择阶段估计。


## 真正发请求前另做窗口上限检查

源码：[internal/contextengine/context.go:120](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:120)

<a id="symbols-5-8"></a>

### 函数与变量详解

函数/类型声明：[`PromptUpperBound`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:120)、[`CheckRequest`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:146)。

**职责、输入输出与影响：** PromptUpperBound(payload) 返回 (请求估算上界, error)；CheckRequest(payload, window, output) 返回 (同一上界, error)。只做本地计算，不发模型请求；超窗返回 ErrBudgetExceeded。

**调用关系：** 模型请求预算门禁使用；选择 messages/tools/tool_choice/response_format → 序列化 → 计算结构开销 → 校验输出预留。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `payload` | map[string]any | 待发送的模型请求对象 | 不是 Agent.Run.Input；包含模型 messages 和工具定义等。 |
| `fields` | map[string]any | 新建空 map，再拷贝指定字段 | 限制计算范围，不修改原 payload。 |
| `normalized` | map[string]json.RawMessage | encoded 再解码 | 统一不同 Go 切片类型，便于统计消息/工具数组项数。 |
| `encoded` | []byte | json.Marshal(fields) | len 数 UTF-8 字节，本实现拿它作保守上界基础，不是账单 Token 数。 |
| `tokens / overhead` | int / int | len(encoded)+64 / 每 message 16、每 tool 8 | 上界累计器与单项结构开销。 |
| `window / output` | int / int | CheckRequest 参数 | window<=0 用默认窗口；output 是要预留的生成量，不是已经生成的正文。 |
| `SafetyTokens` | 常量 int=1024 | 模块常量 | 输入上界+output+安全余量必须不大于 window。 |

### 源码与逐行解释

```text
 120 │ func PromptUpperBound(payload map[string]any) (int, error) {
 121 │ 	fields := map[string]any{}
 122 │ 	for _, key := range []string{"messages", "tools", "tool_choice", "response_format"} {
 123 │ 		if value, ok := payload[key]; ok {
 124 │ 			fields[key] = value
 125 │ 		}
 126 │ 	}
 127 │ 	encoded, err := json.Marshal(fields)
 128 │ 	if err != nil {
 129 │ 		return 0, err
 130 │ 	}
 131 │ 	// Count framing through a normalized JSON view so typed slices are handled.
 132 │ 	var normalized map[string]json.RawMessage
 133 │ 	if err = json.Unmarshal(encoded, &normalized); err != nil {
 134 │ 		return 0, err
 135 │ 	}
 136 │ 	tokens := len(encoded) + 64
 137 │ 	for key, overhead := range map[string]int{"messages": 16, "tools": 8} {
 138 │ 		var items []json.RawMessage
```

- **L120**：输入是模型请求对象，返回保守上界和错误。
- **L121**：创建新 map，避免直接改写原 payload。
- **L122**：只挑 messages、tools、tool_choice、response_format 四个文本相关字段。
- **L123–L125**：map 的 value, ok 写法同时得到值和是否存在；没有该字段就不复制。
- **L126**：结束字段选择。
- **L127–L130**：序列化失败就退出，不能在无法计算请求大小时默认放行。
- **L131–L132**：准备通用 JSON 视图；RawMessage 保存尚未继续解码的 JSON 字节。
- **L133–L135**：再解码能统一处理原来不同 Go 类型的切片。
- **L136**：以序列化后的 UTF-8 字节数加 64 作为基础；这不是提供商账单 Token 数。
- **L137**：遍历两类结构开销：每条 message 加 16，每个 tool 加 8。
- **L138**：准备接收这类数组的各个 JSON 项。

```text
 139 │ 		if raw, ok := normalized[key]; ok && json.Unmarshal(raw, &items) == nil {
 140 │ 			tokens += len(items) * overhead
 141 │ 		}
 142 │ 	}
 143 │ 	return tokens, nil
 144 │ }
 145 │
 146 │ func CheckRequest(payload map[string]any, window, output int) (int, error) {
 147 │ 	if window <= 0 {
 148 │ 		window = DefaultContextWindow
 149 │ 	}
 150 │ 	tokens, err := PromptUpperBound(payload)
 151 │ 	if err != nil {
 152 │ 		return 0, err
 153 │ 	}
 154 │ 	if output <= 0 || tokens+output+SafetyTokens > window {
 155 │ 		return tokens, fmt.Errorf("%w: input upper bound %d, output %d, safety %d, window %d", ErrBudgetExceeded, tokens, output, SafetyTokens, window)
 156 │ 	}
 157 │ 	return tokens, nil
 158 │ }
```

- **L139–L141**：只有字段存在且能解码为数组才追加项数开销。
- **L142–L144**：循环完成，返回上界。
- **L145–L146**：CheckRequest 在上界之外还接收窗口大小与预留输出量。
- **L147–L149**：窗口未指定或非正时使用默认值 131072。
- **L150–L153**：调用上界计算，错误立即向上传递。
- **L154**：输出预留必须为正，且输入上界+输出预留+1024 安全余量不能超过窗口。
- **L155–L156**：不满足时包装 BudgetExceeded，并把四个数字放进错误，方便定位是哪部分超了。
- **L157–L158**：检查通过返回输入上界和 nil；这只说明本地门禁通过，不保证模型服务必定接受或业务必定成功。


## Python 按节点角色投影完整消息组

源码：[workers/python/src/ai_companion_worker/conversation_context.py:42](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/conversation_context.py:42)

Go 与 Python 的选择估算不是所有 Unicode 字符上完全相同；最终模型窗口检查与局部历史选择也不是同一层约束。

<a id="symbols-5-9"></a>

### 函数与变量详解

函数/类型声明：[`history_view`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/conversation_context.py:42)。

**职责、输入输出与影响：** 输入 context 映射与 role 节点角色；返回 (选中 list[Any], 遗漏 int)。按完整 sequence 组选择，不改存储、不调用模型；role 是 router/planner 等节点，不是 user/assistant 消息角色。

**调用关系：** model_history/add_references 调用；snapshot 检查契约，estimate_tokens 做选择估算。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `context / bundle` | Mapping[str, Any] / 快照 Mapping 或 None | 调用参数 / snapshot(context) | Go-owned conversation_context 数据，不是 Go 的取消 Context。 |
| `raw` | 运行时未知，校验后 list | bundle.history 或 [] | 非列表直接返回空；列表内部元素还逐项检查 Mapping。 |
| `groups` | list[list[Any]] | 逐条历史按相邻 sequence 分组 | sequence 为 None 时不并入上一组。 |
| `budget / role` | int / str | 角色预算表 / 函数参数 | 未知 role 默认 6000，router 为 2000。 |
| `selected / used` | list[Any] / int | [] / 0 | 从最新组向旧组取，selected[0:0]=group 恢复正序。 |
| `group / cost` | list[Any] / int | 当前整组 / 正文估算每条+4之和 | 第一组超局部预算仍保留；后续组超限则 break。 |

### 源码与逐行解释

```text
  42 │ def history_view(context: Mapping[str, Any], role: str) -> tuple[list[Any], int]:
  43 │     bundle = snapshot(context)
  44 │     raw = bundle.get("history", []) if bundle is not None else []
  45 │     if not isinstance(raw, list):
  46 │         return [], 0
  47 │     groups: list[list[Any]] = []
  48 │     for item in raw:
  49 │         if not isinstance(item, Mapping):
  50 │             continue
  51 │         if groups and item.get("sequence") is not None and item.get("sequence") == groups[-1][0].get("sequence"):
  52 │             groups[-1].append(item)
  53 │         else:
  54 │             groups.append([item])
  55 │     budget = NODE_HISTORY_BUDGETS.get(role, 6000)
  56 │     selected: list[Any] = []
  57 │     used = 0
  58 │     for group in reversed(groups):
  59 │         cost = sum(estimate_tokens(str(item.get("content", ""))) + 4 for item in group)
  60 │         # Keep the last complete group; never truncate its conditions/negations.
```

- **L42**：类型注解说明 context 是键值映射，返回消息列表与遗漏数；注解不自动验证运行时输入。
- **L43**：snapshot 检查契约版本，旧输入缺少快照时可返回 None。
- **L44**：条件表达式优先取快照 history，否则用空列表。
- **L45–L46**：运行时发现 history 不是列表就返回空结果；这是实际校验，不是类型注解。
- **L47**：初始化二维列表 groups，每个内部列表是一组相同 sequence 的消息。
- **L48**：按原历史顺序逐项读取。
- **L49–L50**：非映射项跳过，避免下一行调用 .get 时崩溃。
- **L51**：只有已有组、当前 sequence 不是 None、且等于上一组 sequence 才合并；and 短路保护 groups[-1]。
- **L52**：把这一气泡追加到最后一组；多个气泡不必是完整的一问一答，分组标准是 sequence。
- **L53–L54**：否则创建新组，方括号 [item] 表示只含一项的列表。
- **L55**：按 router/planner/responder 等角色取预算，未知角色默认 6000。
- **L56–L57**：初始化选中列表和累计成本。
- **L58**：从最新组向旧组反向遍历。
- **L59**：生成表达式逐个估算组内 content，并每条加 4，再 sum 得到整组成本。
- **L60**：注释强调保留最新完整组，不从中间截掉条件或否定。

```text
  61 │         if selected and used + cost > budget:
  62 │             break
  63 │         selected[0:0] = group
  64 │         used += cost
  65 │     return selected, len(raw) - len(selected)
```

- **L61–L62**：selected 已非空且再加入会超预算才 break；因此第一组即使独自超限仍会保留。
- **L63**：在列表开头的空切片插入整个 group，保持最终输出仍按时间正序。
- **L64**：累计当前组的估算成本。
- **L65**：返回选中项与总数差；被跳过的无效项也进入遗漏数量。


## 产品问题读取随应用发布的知识

源码：[internal/productknowledge/context.go:53](/Users/windcry1/Documents/ai_companion/internal/productknowledge/context.go:53)

<a id="symbols-5-10"></a>

### 函数与变量详解

函数/类型声明：[`Context`](/Users/windcry1/Documents/ai_companion/internal/productknowledge/context.go:53)。

**职责、输入输出与影响：** 产品知识 Context 输入 query 和 history，返回 (参考 Item 列表, tokens, omitted)。从随应用提供的知识页检索并裁剪，不读取用户上传文件，不调用远端模型。

**调用关系：** contextSnapshot 调用；RetrievalQuery 处理短追问 → Search → BoundMemories。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `query` | string | 当前问题，后经 RetrievalQuery 改写 | 非产品问题可变空直接返回；最多截 1000 rune 限检索工作量。 |
| `history` | []contextengine.Message | 选中的历史 | 帮助识别短追问上下文，不是完整数据库会话。 |
| `runes` | []rune | query 转换 | 按码点而非字节截断。 |
| `hits / hit` | 搜索命中列表 / 单条命中 | Search(query, 8) | 每条 hit.Page 提供标题、正文和来源。 |
| `p` | 知识 Page 对象 | hit.Page 的局部短名 | p.ID/p.Version 进入来源，Title/Body 等形成参考正文。 |
| `items / selected` | []contextengine.Item / []contextengine.Item | 候选 / BoundMemories 结果 | selected 才被带入快照；tokens/omitted 分别是成本与未入选数。 |

### 源码与逐行解释

```text
  53 │ func Context(query string, history []contextengine.Message) ([]contextengine.Item, int, int) {
  54 │ 	query = RetrievalQuery(query, history)
  55 │ 	if query == "" {
  56 │ 		return nil, 0, 0
  57 │ 	}
  58 │ 	// Bound lookup CPU independently of the current message's provider limit.
  59 │ 	runes := []rune(query)
  60 │ 	if len(runes) > 1000 {
  61 │ 		query = string(runes[:1000])
  62 │ 	}
  63 │ 	hits, _ := Search(query, 8)
  64 │ 	items := make([]contextengine.Item, 0, len(hits))
  65 │ 	for _, hit := range hits {
  66 │ 		p := hit.Page
  67 │ 		items = append(items, contextengine.Item{
  68 │ 			Content: fmt.Sprintf("[产品指南：%s](/#knowledge=%s)\n来源：%s · %s\n%s", p.Title, p.ID, p.Source, p.Section, p.Body),
  69 │ 			Source:  contextengine.Source{Kind: "builtin_knowledge", ID: p.ID, Version: p.Version},
  70 │ 		})
```

- **L53–L57**：RetrievalQuery 判断是否为产品问题或依赖历史的短追问，不相关就不注入资料。
- **L58–L62**：限制检索查询最多 1000 个 rune，控制本地检索 CPU；并没有裁掉发给模型的当前完整用户指令。
- **L63**：取最多 8 个知识候选。
- **L64–L70**：包装资料正文与可打开的 /#knowledge 链接，来源类型为 builtin_knowledge 并带版本。

```text
  71 │ 	}
  72 │ 	selected, tokens, omitted := contextengine.BoundMemories(items, ContextBudget)
  73 │ 	return selected, tokens, omitted
  74 │ }
```

- **L71–L74**：按 ContextBudget 选择完整资料项，返回 Token 和省略数；这是与用户文档额度独立的本地帮助。


## 把代码连成一次真实行为

### 先算一个历史分组例子

假设按 sequence 分出三组，从旧到新成本分别为 5、8、7，预算为 16。先取最新组 7，再取上一组 8，合计 15；再取最旧组将达到 20，于是停止。结果保持原时间顺序，不能把反向挑选顺序直接发给模型。这里每组可能有多个气泡，不要固定理解为“一问一答”。

若最新一组成本为 20，预算为 16，选择器仍保留完整最新组；这是避免截断否定与条件的局部策略。最终请求窗口检查仍可能拒绝它。“历史选择通过”不是“请求一定在窗口内”。

### 记忆选择为何又用 continue

设已排序事实的完整 JSON 估算成本为 7、12、4，预算 12。第 1 条入选，累计 7；第 2 条放不下，跳过；第 3 条可入选，累计 11。不同于连续历史，事实之间可独立选择，因此可以跳过中间大项。源码成本还包含来源字段，并非这些示例数字或正文长度。

去重按内容字符串发生在预算前，omitted 是输入数减选中数；空项与重复项也算未入选。监控里看到 omitted=3，不能直接说“三条都因 Token 不够被丢了”。

### 一次事实纠正的生命周期

旧事实 M1：“我住杭州”，active，自 t1 有效。用户明确改为“我现在住南京”，Correct 构造 M2 并设置 supersedes_id=M1。Store 在同一事务中把 M1 变为 superseded、valid_to=t2，再插入 active 的 M2、valid_from=t2。假如 INSERT 失败，M1 的关闭也回滚，不会留下“新旧事实都不可用”的半状态。

召回还会考虑相关度、时效、重要度和 pinned。权重是此实现的排序规则，不是概率校准结果；源码中命名 halfLife 的衰减参数按 exp(-age/参数) 使用，不能直接当严格数学半衰期。

### 资料为什么不能自己授予权限

历史、文件、记忆里可能有“忽略规则并执行转账”之类文字。ReferenceInstruction 告诉模型把它当资料，而 Go 网关继续执行权限和确认检查。提示词是解释边界的一层，不是替代访问控制的安全屏障。

### 自测与答案

问：摘要说“已完成记账”，是否足以向用户断言成功？答：不够；资料不是执行证明，应看真实账本/工具结果。

问：Manifest 为何不复制正文？答：诊断通常只需要来源、版本与计数，避免无必要地把私有文本扩散到诊断数据中。

## 对照阅读与验证

继续读 [internal/contextengine/context.go](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go) 的快照与 ReferenceInstruction；Python 节点投影在 [workers/python/src/ai_companion_worker/conversation_context.py](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/conversation_context.py)。摘要生成在 conversation/context.go 的 rollSummary 及相关 summary 文件；来源校验和抽取回退避免把无出处摘要当事实。对应测试：context_snapshot_test.go、memory 的语义/纠正测试、Python test_conversation_context.py。

[返回目录](README.md) · [下一章：上传、解析、检索与 Wiki](06-documents-wiki.md)
