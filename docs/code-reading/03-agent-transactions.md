# 03 Agent 入库、领取与完成事务

[阅读总入口](README.md)

行为例子：发送接口返回 202 后，即使浏览器关闭或 API 重启，任务仍应能被 Worker 找到。这个保证建立在消息、运行记录、事件和调度数据的事务上。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 先知道数据库事务解决哪个问题

假设先写用户消息，再写 Run，第二步失败会留下“看得见但永远没人处理”的消息。事务让相关数据库写入一起提交或一起撤销。它不自动覆盖 HTTP 请求、文件系统、Python 进程或外部模型。

Message 保存用户说了什么，Run 保存如何处理，Event 保存发生过什么，Outbox 保存需要通知调度的意图。它们各有用途，不是重复存四份文本。sequence 是会话内逻辑排序号，不是系统时间；reply_to 指明助手回答对应哪个输入。

### 三种并发措施不能混为一谈

行锁保护本次短事务中的竞争；租约把“执行权”借给 Worker 一段时间；revision 是状态版本，提交时要求仍与自己读取的一致。因为模型可能执行很久，项目没有在整个模型调用期间一直占用最初的数据库行锁。

CAS 在这里指“带预期状态/版本条件的更新”，不是某条固定名为 CAS 的 SQL 指令。看 UPDATE 的 WHERE 比只看 SET 更重要：前者决定谁还有权写，后者决定写什么。

## AcceptChat 创建持久化运行

源码：[internal/agent/chat.go:26](/Users/windcry1/Documents/ai_companion/internal/agent/chat.go:26)

<a id="symbols-3-1"></a>

### 函数与变量详解

函数/类型声明：[`AcceptChat`](/Users/windcry1/Documents/ai_companion/internal/agent/chat.go:26)。

**职责、输入输出与影响：** 输入 AcceptChatInput 与 ctx，返回 (conversation.Message, agent.Run, error)。构造并捕获配置快照，调用 ChatStore 原子接受；不是模型执行函数。

**调用关系：** HTTP sendMessage → AcceptChat → captureAgentDefinition/captureModelProfile → AcceptAgentMessage。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | AcceptChatInput | HTTP 层校验后组装 | UserID/ConversationID/CharacterID/Module 绑定身份；Content 是正文，Context 是模型业务上下文 map。 |
| `store / ok` | ChatStore / bool | s.store.(ChatStore) | 确认存储具有原子接受消息和 Run 的能力；ok=false 不能继续。 |
| `messageID / runID` | string / string | 两次 id.New() | 不同实体的 ID；run.ThreadID=runID，run.IdempotencyKey=messageID。 |
| `message` | conversation.Message | 用户正文和新 ID | Role=user、Status=completed 仅表示输入已完整，不是回答已完成。 |
| `payload` | map[string]any | message_id、text、context 组合 | JSON 序列化前的运行输入对象。 |
| `encoded` | []byte | json.Marshal(payload) | 随后赋给 Run.Input 的 JSON 内容，不是模型响应。 |
| `run` | agent.Run | accepted/revision=1 起步 | AvailableAt 是可领取时刻，DeadlineAt 是最晚截止，随后填不可变配置快照。 |
| `now` | time.Time | s.now().UTC() | 各时间字段的统一基准；runTimeout 是时长。 |

### 源码与逐行解释

```text
  26 │ func (s *Service) AcceptChat(ctx context.Context, input AcceptChatInput) (conversation.Message, Run, error) {
  27 │ 	input.UserID = strings.TrimSpace(input.UserID)
  28 │ 	input.ConversationID = strings.TrimSpace(input.ConversationID)
  29 │ 	input.CharacterID = strings.TrimSpace(input.CharacterID)
  30 │ 	input.Module = strings.TrimSpace(input.Module)
  31 │ 	input.Content = strings.TrimSpace(input.Content)
  32 │ 	if input.UserID == "" || input.ConversationID == "" || input.CharacterID == "" ||
  33 │ 		input.Content == "" || len([]rune(input.Content)) > 8000 {
  34 │ 		return conversation.Message{}, Run{}, fmt.Errorf("%w: invalid Agent chat input", ErrValidation)
  35 │ 	}
  36 │ 	if input.Module != "companion" && input.Module != "life" && input.Module != "work" {
  37 │ 		return conversation.Message{}, Run{}, fmt.Errorf("%w: unsupported module", ErrValidation)
  38 │ 	}
  39 │ 	store, ok := s.store.(ChatStore)
  40 │ 	if !ok {
  41 │ 		return conversation.Message{}, Run{}, ErrConflict
  42 │ 	}
```

- **L26–L31**：依次规范化用户、会话、角色、模块和内容字段。
- **L32–L35**：核心 ID 不能为空，内容不能为空且最多 8000 个 Unicode 字符。
- **L36–L38**：只接受 companion/life/work 三个模块。
- **L39–L42**：要求底层 Store 实现 ChatStore，才能原子保存消息和 Run；缺此能力返回冲突。

```text
  43 │ 	messageID, err := id.New()
  44 │ 	if err != nil {
  45 │ 		return conversation.Message{}, Run{}, err
  46 │ 	}
  47 │ 	runID, err := id.New()
  48 │ 	if err != nil {
  49 │ 		return conversation.Message{}, Run{}, err
  50 │ 	}
  51 │ 	now := s.now().UTC()
  52 │ 	message := conversation.Message{
  53 │ 		ID: messageID, ConversationID: input.ConversationID, UserID: input.UserID,
  54 │ 		Role: "user", Bubble: 1, Content: input.Content, Status: "completed",
  55 │ 		CreatedAt: now, CompletedAt: &now,
  56 │ 	}
  57 │ 	payload := map[string]any{
  58 │ 		"message_id": messageID,
  59 │ 		"text":       input.Content,
  60 │ 		"context":    input.Context,
  61 │ 	}
```

- **L43–L46**：生成 Message ID。
- **L47–L50**：生成另一个 Run ID，消息与运行是不同实体。
- **L51**：固定本次 UTC 时间。
- **L52–L56**：用户消息已经是 completed，因为接收到的输入完整；这与助手是否生成成功无关。
- **L57–L61**：运行输入封装 message_id、text 与上游 Context。

```text
  62 │ 	encoded, err := json.Marshal(payload)
  63 │ 	if err != nil {
  64 │ 		return conversation.Message{}, Run{}, ErrValidation
  65 │ 	}
  66 │ 	run := Run{
  67 │ 		ID: runID, ThreadID: runID,
  68 │ 		UserID: input.UserID, ConversationID: input.ConversationID,
  69 │ 		CharacterID: input.CharacterID, Module: input.Module,
  70 │ 		GraphName: GraphName, GraphVersion: GraphVersion,
  71 │ 		Status: "accepted", IdempotencyKey: messageID, Input: encoded,
  72 │ 		AvailableAt: now, DeadlineAt: now.Add(s.runTimeout),
  73 │ 		Revision: 1, CreatedAt: now, UpdatedAt: now,
  74 │ 	}
  75 │ 	if err = s.captureAgentDefinition(ctx, &run); err != nil {
  76 │ 		return conversation.Message{}, Run{}, err
  77 │ 	}
  78 │ 	if err = s.captureModelProfile(ctx, &run); err != nil {
  79 │ 		return conversation.Message{}, Run{}, err
  80 │ 	}
```

- **L62–L65**：JSON 序列化用于持久化；失败不产生半个运行。
- **L66–L70**：创建 Run，ThreadID 等于 RunID，是 LangGraph 检查点的运行身份；记录模块和 Graph 版本。
- **L71**：初始 accepted；幂等键使用本次新生成 Message ID。注意这不等于整个发送 HTTP 接口按客户端请求键去重。
- **L72–L74**：立即可领取，设置最终截止时间、初始 revision=1 和时间戳。
- **L75–L77**：捕获当前 Agent Definition 版本与快照；失败停止接受。
- **L78–L80**：捕获 Model Profile 快照；长任务恢复时不随新发布配置漂移。

```text
  81 │ 	return store.AcceptAgentMessage(ctx, message, run)
  82 │ }
```

- **L81–L82**：交给 Store.AcceptAgentMessage，下面进入真正事务。


## 同一事务保存输入和调度依据

源码：[internal/persistence/postgresstore/agent_chat.go:14](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent_chat.go:14)

<a id="symbols-3-2"></a>

### 函数与变量详解

函数/类型声明：[`AcceptAgentMessage`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent_chat.go:14)。

**职责、输入输出与影响：** 输入用户 Message 和新 Run，返回 (带 sequence 的 Message, Run, error)。事务保存输入、运行、事件、Outbox、会话序号；仅 Commit 成功才表示接受成功。

**调用关系：** AcceptChat 调用；BeginTx → QueryRow/INSERT → appendAgentEvent/appendAgentRunRequested → Commit。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `s` | *postgresstore.Store | 具体数据库存储对象 | s.db 是数据库连接池，不是单次事务。 |
| `tx` | *sql.Tx | BeginTx | 所有关联 SQL 使用 tx；defer Rollback 兜底，成功 Commit 后结束。 |
| `message / run` | conversation.Message / agent.Run | 服务构造后传入的值 | 再次核对归属/角色；message.Sequence 在这里赋值。 |
| `sequence` | uint64 | 锁定会话行读出的 next_sequence | 给用户消息分配，更新为 sequence+2 为助手回复留位。 |
| `characterID` | string | 同一会话查询结果 | 验证 run.CharacterID 与数据库当前会话关系。 |
| `err` | error | 每个 SQL/事件/提交步骤 | 中途失败不继续后续写入；事务中的已写步骤会回滚。 |

### 源码与逐行解释

```text
  14 │ func (s *Store) AcceptAgentMessage(ctx context.Context, message conversation.Message, run agent.Run) (conversation.Message, agent.Run, error) {
  15 │ 	tx, err := s.db.BeginTx(ctx, nil)
  16 │ 	if err != nil {
  17 │ 		return message, run, err
  18 │ 	}
  19 │ 	defer tx.Rollback()
  20 │ 	var sequence uint64
  21 │ 	var characterID string
  22 │ 	err = tx.QueryRowContext(ctx, `
  23 │ 		SELECT next_sequence,character_id::text
  24 │ 		FROM app.conversations
  25 │ 		WHERE id=$1 AND user_id=$2 AND status='active'
  26 │ 		FOR UPDATE`,
  27 │ 		message.ConversationID, message.UserID,
  28 │ 	).Scan(&sequence, &characterID)
```

- **L14–L18**：开启事务，失败立即返回。
- **L19**：注册兜底回滚，任何中途 return 都不会留下事务中的部分写入。
- **L20–L21**：准备读取会话序列及角色 ID。
- **L22–L28**：用 conversation_id、user_id、active 状态锁定会话行，同时读取 next_sequence。FOR UPDATE 使同会话的序列分配串行。

```text
  29 │ 	if errors.Is(err, sql.ErrNoRows) {
  30 │ 		return message, run, conversation.ErrNotFound
  31 │ 	}
  32 │ 	if err != nil {
  33 │ 		return message, run, err
  34 │ 	}
  35 │ 	if characterID != run.CharacterID || run.ConversationID != message.ConversationID ||
  36 │ 		run.UserID != message.UserID {
  37 │ 		return message, run, agent.ErrValidation
  38 │ 	}
  39 │ 	message.Sequence = sequence
  40 │ 	if _, err = tx.ExecContext(ctx, `
  41 │ 		INSERT INTO app.messages (
  42 │ 			id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,
  43 │ 			created_at,completed_at
  44 │ 		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
  45 │ 		message.ID, message.ConversationID, message.UserID, message.Role,
  46 │ 		message.Sequence, message.Bubble, message.Content, message.Status,
  47 │ 		message.CreatedAt, message.CompletedAt,
  48 │ 	); err != nil {
```

- **L29–L34**：没有匹配会话返回领域 NotFound，其他 SQL 错误原样返回。
- **L35–L38**：再次核对 Run 与 Message 的用户、会话、角色关系，不只信任上层构造正确。
- **L39**：给用户消息赋予刚锁定的 sequence。
- **L40–L48**：INSERT app.messages：参数按字段列表一一对应；SQL 参数绑定不拼接用户文本。

```text
  49 │ 		return message, run, err
  50 │ 	}
  51 │ 	if _, err = tx.ExecContext(ctx, `
  52 │ 		INSERT INTO agent.runs (
  53 │ 			id,thread_id,user_id,conversation_id,character_id,module_key,
  54 │ 			graph_name,graph_version,agent_definition_key,agent_definition_version_id,
  55 │ 			agent_definition_version,agent_definition_revision,agent_definition_fingerprint,
  56 │ 			agent_definition_model_profile,agent_definition_snapshot,
  57 │ 			model_profile_key,model_profile_version_id,
  58 │ 			model_profile_revision,model_profile_config_version,model_profile_fingerprint,
  59 │ 			model_profile_snapshot,status,idempotency_key,input,available_at,
  60 │ 			deadline_at,revision,created_at,updated_at
  61 │ 		) VALUES (
  62 │ 			$1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,'')::uuid,
  63 │ 			NULLIF($11,0),NULLIF($12,0),NULLIF($13,''),NULLIF($14,''),$15,
  64 │ 			NULLIF($16,''),NULLIF($17,'')::uuid,NULLIF($18,0),NULLIF($19,''),
  65 │ 			NULLIF($20,''),$21,$22,$23,$24,$25,$26,$27,$28,$29
  66 │ 		)`,
```

- **L49–L50**：消息写入失败回滚。
- **L51–L60**：INSERT agent.runs 的列包含 Graph、Agent 配置、模型配置、输入、时间和 revision，足以恢复运行。
- **L61–L66**：空可选字段用 NULLIF 转 NULL，UUID 字段显式转换；这是 SQL 值占位，不是字符串模板插值。

```text
  67 │ 		run.ID, run.ThreadID, run.UserID, run.ConversationID, run.CharacterID,
  68 │ 		run.Module, run.GraphName, run.GraphVersion, run.AgentDefinitionKey,
  69 │ 		run.AgentDefinitionVersionID, run.AgentDefinitionVersion, run.AgentDefinitionRevision,
  70 │ 		run.AgentDefinitionFingerprint, run.AgentDefinitionModelProfile,
  71 │ 		run.AgentDefinitionSnapshot, run.ModelProfileKey,
  72 │ 		run.ModelProfileVersionID, run.ModelProfileRevision, run.ModelProfileConfigVersion,
  73 │ 		run.ModelProfileFingerprint, run.ModelProfileSnapshot, run.Status,
  74 │ 		run.IdempotencyKey, run.Input, run.AvailableAt, run.DeadlineAt,
  75 │ 		run.Revision, run.CreatedAt, run.UpdatedAt,
  76 │ 	); err != nil {
  77 │ 		return message, run, err
  78 │ 	}
  79 │ 	if _, err = appendAgentEvent(ctx, tx, run.ID, "accepted", map[string]any{
  80 │ 		"graph_name": run.GraphName, "graph_version": run.GraphVersion,
  81 │ 		"agent_definition_version_id":  run.AgentDefinitionVersionID,
  82 │ 		"agent_definition_fingerprint": run.AgentDefinitionFingerprint,
  83 │ 		"model_profile_version_id":     run.ModelProfileVersionID,
  84 │ 		"model_profile_fingerprint":    run.ModelProfileFingerprint,
  85 │ 	}); err != nil {
```

- **L67–L75**：按顺序绑定 Run 的完整快照和状态。
- **L76–L78**：Run 插入失败则前面的消息 INSERT 也回滚。
- **L79–L85**：追加 accepted 事件，记录配置版本和指纹用于诊断。

```text
  86 │ 		return message, run, err
  87 │ 	}
  88 │ 	if err = appendAgentRunRequested(ctx, tx, run); err != nil {
  89 │ 		return message, run, err
  90 │ 	}
  91 │ 	if _, err = tx.ExecContext(ctx, `
  92 │ 		UPDATE app.conversations
  93 │ 		SET next_sequence=$1,last_message_at=$2,updated_at=$2
  94 │ 		WHERE id=$3`,
  95 │ 		sequence+2, message.CreatedAt, message.ConversationID,
  96 │ 	); err != nil {
  97 │ 		return message, run, err
  98 │ 	}
  99 │ 	if err = tx.Commit(); err != nil {
 100 │ 		return message, run, err
 101 │ 	}
 102 │ 	return message, run, nil
 103 │ }
```

- **L86–L87**：事件记录失败同样终止事务。
- **L88–L90**：写入 Agent 调度 Outbox；消息与派发意图同生共死。
- **L91–L98**：更新会话 next_sequence 为 sequence+2，给该轮助手回复预留后一序号，同时更新最近消息时间。
- **L99–L101**：只有全部步骤成功才 Commit。
- **L102–L103**：返回已分配 sequence 的消息和运行；这时上层才能返回 202。


这里的 FOR UPDATE 直接保护序列分配。不能仅凭这段锁就声称同一会话并发发送绝对只产生一个 Run；活跃运行限制还需看入口检查与数据库约束。

## Worker 用 SKIP LOCKED 领取下一条

源码：[internal/persistence/postgresstore/agent.go:161](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent.go:161)

<a id="symbols-3-3"></a>

### 函数与变量详解

函数/类型声明：[`ClaimNextAgentRun`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent.go:161)。

**职责、输入输出与影响：** 输入 owner、当前时刻 now、租期 lease；返回 (已领取 agent.Run, error)。通过短事务选候选、更新为 running、递增 revision 并记录事件；无任务用 ErrNotFound。

**调用关系：** 后台领取/恢复路径调用；scanAgentRun 将 SQL RETURNING 列扫描到 item；appendAgentEvent 记录 running。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `owner` | string | Worker 的身份标识 | 写 lease_owner，完成阶段必须仍匹配；不是用户 ID。 |
| `now / lease` | time.Time / time.Duration | 领取方给出的时刻/租期 | now 参与 available/deadline 条件；lease 表示可持有多久。 |
| `leaseExpiresAt` | time.Time | now.Add(lease) | 执行权的到期点，不是 Run 的最终 DeadlineAt。 |
| `item` | agent.Run | 零值后 scanAgentRun 填入 | 包含数据库更新后的 revision/owner，执行器须使用这份领取结果。 |
| `tx` | *sql.Tx | BeginTx | 锁候选和状态更新处于领取事务，执行模型前已提交。 |

### 源码与逐行解释

```text
 161 │ func (s *Store) ClaimNextAgentRun(ctx context.Context, owner string, now time.Time, lease time.Duration) (agent.Run, error) {
 162 │ 	tx, err := s.db.BeginTx(ctx, nil)
 163 │ 	if err != nil {
 164 │ 		return agent.Run{}, err
 165 │ 	}
 166 │ 	defer tx.Rollback()
 167 │ 	leaseExpiresAt := now.Add(lease)
 168 │ 	var item agent.Run
 169 │ 	err = scanAgentRun(tx.QueryRowContext(ctx, `
 170 │ 		WITH candidate AS (
 171 │ 			SELECT id
 172 │ 			FROM agent.runs
 173 │ 			WHERE available_at<=$3 AND deadline_at>$3
 174 │ 				AND (
 175 │ 					status IN ('accepted','queued','waiting_tool')
 176 │ 					OR (status='running' AND lease_expires_at<=$3)
 177 │ 				)
 178 │ 			ORDER BY available_at,created_at
```

- **L161–L166**：开启领取事务并注册回滚。
- **L167–L168**：计算租约到期点，准备接收返回的 Run。
- **L169–L178**：CTE 挑选 available_at 已到、deadline 尚未到的 accepted/queued/waiting_tool，或租约已过期的 running。

```text
 179 │ 			FOR UPDATE SKIP LOCKED
 180 │ 			LIMIT 1
 181 │ 		)
 182 │ 		UPDATE agent.runs AS r
 183 │ 		SET status='running',lease_owner=$1,lease_expires_at=$2,
 184 │ 			revision=r.revision+1,updated_at=$3,error_code=NULL,error_message=NULL
 185 │ 		WHERE r.id=(SELECT id FROM candidate)
 186 │ 		RETURNING `+agentRunColumns,
 187 │ 		owner, leaseExpiresAt, now,
 188 │ 	), &item)
 189 │ 	if errors.Is(err, sql.ErrNoRows) {
 190 │ 		return agent.Run{}, agent.ErrNotFound
 191 │ 	}
 192 │ 	if err != nil {
 193 │ 		return agent.Run{}, err
 194 │ 	}
 195 │ 	if _, err = appendAgentEvent(ctx, tx, item.ID, "running", map[string]any{
 196 │ 		"lease_owner": owner, "revision": item.Revision, "source": "reconciler",
 197 │ 	}); err != nil {
 198 │ 		return agent.Run{}, err
```

- **L179–L181**：按可执行时间排序，锁定一条候选并跳过其他 Worker 已锁的行。
- **L182–L188**：同一 SQL 更新为 running、写租约 owner/expiry、revision 加 1、清理错误，再 RETURNING 完整 Run。
- **L189–L194**：没候选是 ErrNotFound，不是系统故障；其他 SQL 错误需要上报。
- **L195–L198**：记录 running 事件、owner、revision，source 标明来自数据库 reconciler。

```text
 199 │ 	}
 200 │ 	if err = tx.Commit(); err != nil {
 201 │ 		return agent.Run{}, err
 202 │ 	}
 203 │ 	return item, nil
```

- **L199–L203**：事件成功后提交，返回带新 revision 的执行权。


## 完成时防止迟到 Worker 覆盖

源码：[internal/persistence/postgresstore/agent.go:220](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent.go:220)

<a id="symbols-3-4"></a>

### 函数与变量详解

函数/类型声明：[`CompleteAgentRun`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent.go:220)。

**职责、输入输出与影响：** 输入 runID、owner、预期 revision、output JSON、now；返回 (完成后的 Run, error)。CAS 更新、完成事件和助手回复在同一事务中，条件不符是 ErrConflict。

**调用关系：** Worker 完成链路最终进入 Store；scanAgentRun → appendAgentEvent → appendAgentAssistantMessage → Commit。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `runID / owner` | string / string | 领取身份链传入 | ID 定位任务，owner 证明当前执行者，二者不可互换。 |
| `revision` | int | 执行开始时保留的预期版本 | 用于 WHERE 匹配，不直接取“现在最新版本”绕过并发保护。 |
| `output` | json.RawMessage | 运行结果序列化 | 写 Run.output，也供助手消息构造；不是可任意执行的 SQL。 |
| `now` | time.Time | 完成操作时刻 | 同时判断租约/deadline 有效，写 updated_at/completed_at。 |
| `item` | agent.Run | UPDATE RETURNING | 这是新版本终态记录；不同于原领取快照。 |
| `tx` | *sql.Tx | 本次完成操作新开的事务 | 不沿用最初接受或领取的长事务。 |

### 源码与逐行解释

```text
 220 │ func (s *Store) CompleteAgentRun(ctx context.Context, runID, owner string, revision int, output json.RawMessage, now time.Time) (agent.Run, error) {
 221 │ 	tx, err := s.db.BeginTx(ctx, nil)
 222 │ 	if err != nil {
 223 │ 		return agent.Run{}, err
 224 │ 	}
 225 │ 	defer tx.Rollback()
 226 │ 	var item agent.Run
 227 │ 	err = scanAgentRun(tx.QueryRowContext(ctx, `
 228 │ 		UPDATE agent.runs
 229 │ 		SET status='completed',output=$4,resume_resolution=NULL,
 230 │ 			lease_owner=NULL,lease_expires_at=NULL,
 231 │ 			revision=revision+1,updated_at=$5,completed_at=$5,
 232 │ 			error_code=NULL,error_message=NULL
 233 │ 		WHERE id=$1 AND status='running' AND lease_owner=$2 AND revision=$3
 234 │ 			AND lease_expires_at>$5 AND deadline_at>$5
 235 │ 		RETURNING `+agentRunColumns,
 236 │ 		runID, owner, revision, output, now,
 237 │ 	), &item)
```

- **L220–L225**：完成操作也使用独立事务及兜底回滚。
- **L226–L232**：准备更新：status=completed，保存 output，清除恢复参数/租约，revision 加 1，并设置 completed_at。
- **L233–L237**：WHERE 必须同时匹配 running、owner、revision、有效租约和未到 deadline。即使模型返回成功，失去执行权的 Worker 也不能提交。

```text
 238 │ 	if errors.Is(err, sql.ErrNoRows) {
 239 │ 		return agent.Run{}, agent.ErrConflict
 240 │ 	}
 241 │ 	if err != nil {
 242 │ 		return agent.Run{}, err
 243 │ 	}
 244 │ 	if _, err = appendAgentEvent(ctx, tx, runID, "completed", map[string]any{
 245 │ 		"output": json.RawMessage(output), "revision": item.Revision, "at": now,
 246 │ 	}); err != nil {
 247 │ 		return agent.Run{}, err
 248 │ 	}
 249 │ 	if err = appendAgentAssistantMessage(ctx, tx, item, output, now); err != nil {
 250 │ 		return agent.Run{}, err
 251 │ 	}
 252 │ 	if err = tx.Commit(); err != nil {
 253 │ 		return agent.Run{}, err
 254 │ 	}
 255 │ 	return item, nil
```

- **L238–L243**：UPDATE 未命中返回 ErrConflict；区分业务竞争与 SQL 故障。
- **L244–L248**：追加 completed 事件，仍在事务内。
- **L249–L251**：把 Agent 输出转成助手消息，同事务写入。Run 完成和用户可见回复不会只成功一半。
- **L252–L255**：最终提交并返回终态 Run。


## 把代码连成一次真实行为

### 按数字演算一次接受与完成

假设会话 next_sequence=41。接受事务将用户消息 M1 的 sequence 设为 41，保存 Run R1，追加 accepted 事件和 Outbox，再把 next_sequence 设为 43。42 留给这轮助手回复。用户消息的 completed 表示输入已经完整，不表示 R1 已经完成。

设 R1 初始 revision=1，Worker A 领取后 revision=2、owner=A、status=running。后续具体 revision 还可能因其他状态操作增加；下表用简化数字解释接管：

| 时刻 | 数据库权威状态 | A 手里保留的快照 | 能否由 A 提交 |
|---|---|---|---|
| A 刚领取 | running/A/rev2/租约有效 | A/rev2 | 条件尚符合 |
| A 卡住，租约到期 | running/A/rev2/租约失效 | A/rev2 | 即使未被接管也不能以过期租约提交 |
| B 领取 | running/B/rev3/新租约 | A/rev2 | owner 和 revision 都不符 |
| A 迟到返回模型结果 | 仍由 B 持有或已经终结 | A/rev2 | 返回文本不恢复执行权 |

条件检查与写入要落在同一数据库更新里。若先 SELECT 判断，解除锁后再无条件 UPDATE，二者之间 B 可能已经接管，这就是检查与使用之间的竞态。

### 为什么同时有 Outbox 和数据库领取

Outbox 解决“事务已提交，但通知还未发出”的间隙；Worker 的数据库领取与恢复轮询又能找到符合状态和时间条件的工作。消息通知是调度线索，数据库中的 Run 才是执行权依据。收到重复通知不能直接当成第二次业务授权。

事务成功也不是“分布式世界中绝不重复”：模型请求可能被重试，外部副作用仍要单独做幂等。Complete 的事务保护的是当前 Run 的终态与助手消息这一具体边界。

### 自测与答案

问：为什么不用模型返回时间判断谁是最新 Worker？答：晚返回可能正是失去租约的旧 Worker；应该比较数据库 owner、revision、租约和 deadline。

问：删掉 Outbox 写入会怎样？答：消息与运行可能持久化了但派发意图没有原子保存，及时通知的可靠性下降；不能以可能存在的恢复扫描代替对这个事务边界的理解。

## 对照阅读与验证

继续追 agent.go 中 appendAgentAssistantMessage：助手消息通过原 Message ID 建 reply_to，使用为本轮预留的序号。读 agent/service.go 的 captureAgentDefinition、captureModelProfile 理解快照内容；读 agent/worker_test.go 的取消、deadline 和接管案例验证这些 WHERE 条件的意义。

[返回目录](README.md) · [下一章：工具审批、恢复、取消与重试](04-agent-approval-recovery.md)
