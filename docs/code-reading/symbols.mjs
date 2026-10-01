// 每个条目对应 guide.mjs 的一个源码段。类型与语义人工核对，生成器检查覆盖和符号存在。
// 变量列依次为：名称 | 类型/形状 | 来源/初值 | 用途与变化。
const D = (names, contract, calls, variables) => ({ names: names.split(', '), contract, calls, variables });

export default {
  'startup-identity': [
    D('main', '程序入口，无参数、无返回值。此段装配存储和文件依赖，可能建立数据库连接、创建本地文件存储对象；初始化失败退出进程，并非业务请求函数。', '运行程序时进入 main；NewMemoryStore 创建内存实现，persistence.Open 打开持久化实现，后续把选好的依赖交给各 Service。', `cfg|config.Config|启动阶段 config.Load 返回|DatabaseDriver 决定内存/持久化分支，DocumentStorageDir 决定 Blob 目录。
identityStore / characterStore / conversationStore|各领域 Store 接口|先包装 NewMemoryStore 返回值|变量类型是接口，随后可赋同一个 persistentStore，而不是创建相同名字的多份数据库。
localBlobs / documentBlobs|本地实现 / document.BlobStore 接口|NewLocalBlobStore|前者为具体对象，后者按接口传给文档服务，保存的是原文件字节。
persistentStore|persistence.ApplicationStore|初始 nil，Open 成功后赋值|承载多个领域接口，连接失败时不继续混用部分内存数据。
connectCtx / cancel|context.Context / context.CancelFunc|WithTimeout，期限 10 秒|只限制本次打开连接；cancel 释放计时资源，不是关闭已建立的整个数据库池。
systemLogStore / incidentStore / performanceStore|对应 Store 接口|初始 nil|可选运行诊断依赖；不是每条聊天消息的内容。
err|error|初始化或打开连接的返回值|非 nil 时日志说明具体阶段并退出。`),
    D('Register', '输入 ctx 与 RegisterInput；返回 (TokenPair, error)。创建 User 后再创建登录会话，涉及多次存储写入；校验失败返回空 TokenPair，两个阶段不在这一段共同事务里。', '注册 HTTP 入口 → Register；normalizeEmail/校验 → hashPassword → CreateUser → createSession。', `s|*identity.Service|方法接收者|携带 store、tokens、refreshTTL、now；不是当前登录用户。
input|RegisterInput 值|HTTP 解码后传入|Email 被规范化；Password 仅用于派生；DisplayName/Timezone/Locale/Device 决定用户与设备资料。
hash|string|hashPassword(input.Password)|带算法、盐、参数的密码派生记录；赋给 User.PasswordHash，不是明文密码。
now|time.Time|s.now().UTC()|统一本次创建/更新时间，测试可替换 s.now。
userID|string|id.New()|新账号身份，和随后 Session ID 不同。
user|User|由输入、ID、hash 组装|CreateUser 写入，随后传入 createSession；创建成功不等于 Session 也成功。`),
    D('hashPassword, verifyPassword', 'hashPassword(password string) 返回 (编码记录 string, error)；verifyPassword(encoded, password string) 返回 bool。前者生成随机盐，后者用记录中的参数重算并比较；二者不直接访问数据库。', 'Register/改密码等路径调用派生函数；Login 调用 verifyPassword。pbkdf2.Key 计算派生结果，hmac.Equal 比较结果。', `password|string|本次用户输入|用于派生，不应记录到日志。
salt|[]byte|生成时 make 16 字节后填随机数；验证时 Base64 解码|同一密码不同盐通常得到不同派生记录；验证必须复用原盐。
key|[]byte|pbkdf2.Key 的结果|编码后作为记录第四段；不是可解密的原密码。
encoded / parts|string / []string|存储记录 / 按 $ 拆分|parts 包含算法、迭代数、盐、期望 key；先验段数再访问下标。
iterations|int|strconv.Atoi(parts[1])|控制重新派生工作量，先检查范围，不能把坏参数直接交给计算。
want / got|[]byte / []byte|存储期望值 / 本次重算值|名字分别表示“应该得到”和“实际得到”；相同且无错误才返回 true。`),
    D('AccessClaims, TokenManager, NewTokenManager, IssueAccess, ParseAccess, NewRefreshToken, HashRefreshToken, sign', 'AccessClaims 是声明字段类型，TokenManager 保存密钥和 TTL。IssueAccess 返回 (令牌, 过期时刻, error)，ParseAccess 返回 (声明, error)；NewRefreshToken 返回 (原文, 哈希, error)，HashRefreshToken/sign 返回字符串。除随机数读取外，本段是编码与校验，不自行查 Session。', '身份 Service 调用 IssueAccess/ParseAccess；构造器保存配置；IssueAccess 与 ParseAccess 共用 sign，刷新会话用 NewRefreshToken/HashRefreshToken。', `m|TokenManager|方法接收者值|m.secret 用于 HMAC；m.accessTTL 决定 Access Token 有效时长。
claims|AccessClaims|IssueAccess 中组装；ParseAccess 中由 JSON 填充|UserID/sub、SessionID/sid、ExpiresAt/exp、IssuedAt/iat；两个时间为 Unix 秒。
expiresAt|time.Time|now.Add(m.accessTTL)|业务时间点；不同于 int64 的 claims.ExpiresAt 表示形式。
payload|[]byte 或 sign 参数 string|JSON 编码/解码结果；sign 接收编码文本|同名跨函数类型不同：先看当前签名，不要按名字推断类型。
encoded / signature|string|Base64URL(JSON) / sign(encoded)|拼为 v1.payload.signature；编码不隐藏声明，认证码防止无密钥篡改。
parts|[]string|strings.Split(token, ".")|检查三段与 v1 后才验证/解码；不是 JWT Header。
plain / hash|string / string|32 随机字节的编码 / SHA-256 文本|plain 交客户端，hash 供服务端查刷新会话；不是同一份内容的两个变量名。
sum|[32]byte|sha256.Sum256|固定数组；sum[:] 取切片交十六进制编码。
mac|hash.Hash 接口|hmac.New|Write 输入待认证文本，Sum(nil) 取认证码字节。`),
    D('createSession', '输入 ctx、已验证 User、DeviceInput、timezone；返回 (TokenPair, error)。写/复用设备，再写新 Session，最后签发访问令牌；无模型调用。', 'Register/Login 共用此函数；UpsertDevice → NewRefreshToken → CreateSession → IssueAccess。', `user|User|注册创建或登录查出的账号|user.ID 绑定设备与会话，不从 DeviceInput 决定归属。
input|DeviceInput|客户端设备描述|DeviceKey 用于设备归并；Name/Platform 是展示和平台信息，不是账户权限。
deviceID / device|string / Device|备用新 ID / UpsertDevice 返回对象|Upsert 可能复用旧设备，所以 Session 使用 device.ID 而非机械使用 deviceID。
plain / hash|string / string|NewRefreshToken|plain 只返回客户端；hash 保存到 session.TokenHash。
sessionID / session|string / Session|新会话 ID / 组装记录|保存 UserID、DeviceID、期限、最近使用时刻等；与 AccessClaims 中 sid 对应。
access / accessExpiry|string / time.Time|IssueAccess 返回值|分别放入 TokenPair.AccessToken、AccessExpiresAt。
timezone|string|上层已选择的时区|写入设备记录，用来解释本地时间；不是令牌时钟偏移。`),
    D('Refresh', '输入刷新令牌原文，返回新的 (TokenPair, error)。先查旧会话，再 RotateSession 轮换；旧会话与新会话的原子关系由 Store 实现，不是重复返回旧 Token。', '刷新 HTTP 入口调用；HashRefreshToken → FindSessionByTokenHash → GetUser → RotateSession → IssueAccess。', `refreshToken|string|客户端提交|哈希后查库；不拿明文 Token 查询存储字段。
current|Session|FindSessionByTokenHash|旧会话，检查撤销与过期；其 UserID/DeviceID 继承给新会话。
user|User|按 current.UserID 查库|用于新 Access Token 声明与响应资料。
plain / hash|string / string|新生成的刷新令牌对|覆盖的是客户端后续持有的凭证，不是把旧明文写回。
newID / next|string / Session|id.New / 新记录|next.ID 不等于 current.ID；RotateSession 连接新旧会话。
access / accessExpiry|string / time.Time|绑定 next.ID 的 IssueAccess|旧 Access Token 的会话身份与新签发的不一样。`),
    D('requireAuth', '输入 next http.Handler，返回包裹后的 http.Handler。返回值不是认证结果；新 Handler 每次收到请求才认证，并可能写错误 HTTP 响应或更新在线状态。', '路由装配时用 requireAuth 包裹业务 Handler；运行时调用 identity.Authenticate → realtime.Touch → next.ServeHTTP。', `s|*httpserver.Server|HTTP 服务器依赖对象|其中 identity 是身份服务，不是 Store。
next|http.Handler|路由装配传入的下游|只有认证成功才调用；闭包保留对它的访问。
w / r|http.ResponseWriter / *http.Request|服务器交给 Handler|w 输出响应，r 读取头、路径、请求体和 Context。
header / parts|string / []string|Authorization / 按首个空格拆分|parts[0] 为 Bearer 方案，parts[1] 为凭证原文。
user / claims|identity.User / identity.AccessClaims|Authenticate 的可信返回|封装为 authenticated 写入新请求 Context，供 currentAuth 读取。
authContextKey|私有键类型|Context.WithValue 使用|隔离上下文键，避免把普通字符串键碰撞成身份数据。`),
    D('Create, compile', 'Create 接收 userID 和角色 Input，返回 (Character, PersonaVersion, error)。创建角色及第 1 版编译快照并存储；compile 将配置转为 Persona，不向模型发请求。', '角色创建入口 → applyCreateDefaults/validate → fromInput → compile → store.Create。', `input|character.Input|客户端角色设置|默认模块等由 applyCreateDefaults 补齐，validate 后才建对象。
characterID|string|id.New()|稳定角色身份，不随 Persona 版本递增而换 ID。
character|Character|fromInput(..., 1, ...)|包含归属、模块和 PersonaVersion=1。
persona|PersonaVersion|compile(character, now)|该版可执行/可引用的角色配置快照；并非一次模型回复。
now|time.Time|s.now().UTC()|角色及本版快照的共同时间依据。`),
    D('Update', '输入 userID、characterID、完整角色 Input；返回 (新 Character, 新 PersonaVersion, error)。先按归属读旧角色，再保留身份并提升版本、保存更新。', '角色修改入口 → store.Get → validate → fromInput/compile → store.Update。', `current|Character|按 userID+characterID 查询|原 ID、归属、创建时刻与旧版本的依据。
input|Input|本次修改字段|Module 为空时继承 current.Module，仍需重新校验。
updated|Character|fromInput(current.ID, ..., current.PersonaVersion+1, ...)|新版本对象；不是把 current.ID 增一。
persona|PersonaVersion|compile(updated, updated.UpdatedAt)|与 updated 对应的配置快照。
characterID / userID|string / string|调用方参数|一个定位资源，一个限定归属，不能只保留 characterID 查询。`),
  ],
  'chat-intake': [
    D('send', 'React 表单事件函数，输入 FormEvent<HTMLFormElement>，返回 Promise<void>。会 POST 消息、修改页面状态并等待任务观察；它不直接写数据库，也不自行推理。', 'form 的 onSubmit 调用；fetch → mergeMessage → consumeAgentRun 或 consumeEvents；catch 转为页面错误提示。', `event / form|FormEvent<HTMLFormElement> / HTMLFormElement|提交事件 / event.currentTarget|preventDefault 阻止原生跳转；成功接受后才 form.reset。
data / content|FormData / string|表单内容 / content 字段去空白|content 是用户可见文字，不是完整模型 messages。
pendingDocuments|PendingDocument[]，React state|附件上传响应积累|提取 id 作为 document_ids；已上传不代表已经解析完。
conversationID / jobID / agentRunID|string，React state|创建/恢复接口返回|依次是会话、普通任务、Agent 运行 ID；已有任务时阻止新发送。
token / apiBase|string / string|组件参数 / 环境配置或默认值|token 放 Authorization；apiBase 是 API 地址，不是模型地址。
response|Response|await fetch|看 status/ok 决定 HTTP 成败；不是模型生成文本。
payload|对象：message?、job?、agent_run?|await response.json 后的类型断言|message 还可能为错误字符串，需运行时检查；409 可携带活跃运行。
userMessage / current|Message / Message[]|确认响应 / setMessages 回调旧列表|按 ID 合并，避免恢复查询与实时结果重复展示。
cause|catch 中的未知值|任意抛出的异常|instanceof Error 后读 message；不会因为进入 catch 自动重发 POST。`),
    D('sendMessage', 'HTTP 发送入口前半段，输入 w/r，无 Go 返回值；结果写进 HTTP。先限流、解码、查额度与附件，失败就停止，不进入后半段接受运行。', '经 requireAuth 的消息路由进入；realtime.Allow、currentAuth、decodeJSON、requireQuota、documents.Get 等提供前置检查。', `allowed / remaining / retryAfter|bool / int / time.Duration|限流器 Allow 返回|allowed 决定继续；remaining 写响应头；retryAfter 转秒用于重试提示。
input|匿名结构 {Content string; DocumentIDs []string}|请求 JSON 解码|是不可信输入，附件 ID 必须逐一按用户校验。
auth|authenticated|currentAuth(r)|可信 User 与 Claims；auth.User.ID 限定资源归属。
visibleContent|string|input.Content 去空白或旧附件引用修复|保留用户可见文本；仅附件输入时补“请处理这个文件”。
documents|[]document.Document|空切片逐项追加已通过 Get 的记录|保存验证后的附件元数据，不是原文件字节。
documentID / item|string / document.Document|循环输入 ID / Get 返回|不能把 documentID 本身当访问许可。
messageContent|string|visibleContent 的副本|追加可信附件标记后用于内容校验与任务输入。`),
    D('sendMessage', '同一函数后半段：按角色模块和制品要求分流。Agent 返回 {message, agent_run}，普通路径返回 {message, job}，HTTP 均为 202；产生持久化任务而非同步最终回答。', 'conversations.Get/characters.Get → ActiveForConversation → ValidateContent/BuildContext → agentRuns.AcceptChat；未命中 Agent 则 conversations.Send。', `forceArtifactAgent|bool|requiresDurableArtifactAgent(messageContent)|标识制品需求；仍与 work 模块等分支条件结合，不能单独跳过校验。
conversationItem|conversation.Conversation|按已认证用户及路径 ID 查询|其 CharacterID 确定读取哪个角色。
persona|character.Character|characters.Get 返回|此处名字叫 persona 但类型是角色对象，不是 character.PersonaVersion。
activeRun / activeErr|agent.Run / error|ActiveForConversation|找到则返回 409；只有 ErrNotFound 表示没有活跃任务。
snapshot|contextengine.Snapshot|BuildContext 的返回|History、Summary、Memories、Knowledge、Manifest；作为运行上下文输入。
message / run|conversation.Message / agent.Run|Agent AcceptChat 返回|message 是已收录的用户发言，run 是后续可恢复执行记录。
job|conversation.Job|普通 Send 返回|与 agent.Run 不同的运行协议，前端接 SSE/事件路径。
acceptErr / historyErr|error / error|接受运行 / 构建上下文|名字说明失败阶段，便于知道此时是否已经入库。`),
    D('consumeAgentRun', '输入 Run ID 和可选 conversationOverride，返回 Promise<void>。轮询、弹审批、提交决策、刷新消息并更新 UI；连接失败退出观察不自动取消服务器任务。', 'send、初次恢复、重试调用；GET Run → waiting_approval 时 POST resolve → 终态时 loadMessages/mergeMessages。', `id|string|调用方传入|固定要观察的 Run，不是 conversationID。
conversationOverride / targetConversationID|string / string|显式恢复参数 / override 或当前 state|避免刚 setConversationID 后闭包仍读旧值。
failures|number|开始 0|一次正常读取后归零，异常后递增，第 5 次退出；不是任务已执行次数。
run|AgentRun 加可选 message|GET JSON|status/revision 驱动观察；不因前端类型断言自动可信。
interrupt|AgentInterrupt 或 undefined|run.output?.interrupts?.[0]|审批展示内容，缺失时不能凭空构造确认。
resolutionKey|string|Run ID 与 revision 拼接|本次逻辑确认的键；handledAgentResolutionsRef.current 保存已处理键。
approved / resolved|boolean / Response|window.confirm / POST resolve 返回|false 也是合法拒绝；resolved.ok 说明提交响应而非业务最终完成。
latest|Message[]|终态时 loadMessages|以服务器已持久化消息刷新 UI。
handledAgentResolutionsRef|React Ref，内部 Set<string>|组件初始化|去重提示，不是服务端幂等数据库，刷新页面可丢失。`),
  ],
  'agent-transactions': [
    D('AcceptChat', '输入 AcceptChatInput 与 ctx，返回 (conversation.Message, agent.Run, error)。构造并捕获配置快照，调用 ChatStore 原子接受；不是模型执行函数。', 'HTTP sendMessage → AcceptChat → captureAgentDefinition/captureModelProfile → AcceptAgentMessage。', `input|AcceptChatInput|HTTP 层校验后组装|UserID/ConversationID/CharacterID/Module 绑定身份；Content 是正文，Context 是模型业务上下文 map。
store / ok|ChatStore / bool|s.store.(ChatStore)|确认存储具有原子接受消息和 Run 的能力；ok=false 不能继续。
messageID / runID|string / string|两次 id.New()|不同实体的 ID；run.ThreadID=runID，run.IdempotencyKey=messageID。
message|conversation.Message|用户正文和新 ID|Role=user、Status=completed 仅表示输入已完整，不是回答已完成。
payload|map[string]any|message_id、text、context 组合|JSON 序列化前的运行输入对象。
encoded|[]byte|json.Marshal(payload)|随后赋给 Run.Input 的 JSON 内容，不是模型响应。
run|agent.Run|accepted/revision=1 起步|AvailableAt 是可领取时刻，DeadlineAt 是最晚截止，随后填不可变配置快照。
now|time.Time|s.now().UTC()|各时间字段的统一基准；runTimeout 是时长。`),
    D('AcceptAgentMessage', '输入用户 Message 和新 Run，返回 (带 sequence 的 Message, Run, error)。事务保存输入、运行、事件、Outbox、会话序号；仅 Commit 成功才表示接受成功。', 'AcceptChat 调用；BeginTx → QueryRow/INSERT → appendAgentEvent/appendAgentRunRequested → Commit。', `s|*postgresstore.Store|具体数据库存储对象|s.db 是数据库连接池，不是单次事务。
tx|*sql.Tx|BeginTx|所有关联 SQL 使用 tx；defer Rollback 兜底，成功 Commit 后结束。
message / run|conversation.Message / agent.Run|服务构造后传入的值|再次核对归属/角色；message.Sequence 在这里赋值。
sequence|uint64|锁定会话行读出的 next_sequence|给用户消息分配，更新为 sequence+2 为助手回复留位。
characterID|string|同一会话查询结果|验证 run.CharacterID 与数据库当前会话关系。
err|error|每个 SQL/事件/提交步骤|中途失败不继续后续写入；事务中的已写步骤会回滚。`),
    D('ClaimNextAgentRun', '输入 owner、当前时刻 now、租期 lease；返回 (已领取 agent.Run, error)。通过短事务选候选、更新为 running、递增 revision 并记录事件；无任务用 ErrNotFound。', '后台领取/恢复路径调用；scanAgentRun 将 SQL RETURNING 列扫描到 item；appendAgentEvent 记录 running。', `owner|string|Worker 的身份标识|写 lease_owner，完成阶段必须仍匹配；不是用户 ID。
now / lease|time.Time / time.Duration|领取方给出的时刻/租期|now 参与 available/deadline 条件；lease 表示可持有多久。
leaseExpiresAt|time.Time|now.Add(lease)|执行权的到期点，不是 Run 的最终 DeadlineAt。
item|agent.Run|零值后 scanAgentRun 填入|包含数据库更新后的 revision/owner，执行器须使用这份领取结果。
tx|*sql.Tx|BeginTx|锁候选和状态更新处于领取事务，执行模型前已提交。`),
    D('CompleteAgentRun', '输入 runID、owner、预期 revision、output JSON、now；返回 (完成后的 Run, error)。CAS 更新、完成事件和助手回复在同一事务中，条件不符是 ErrConflict。', 'Worker 完成链路最终进入 Store；scanAgentRun → appendAgentEvent → appendAgentAssistantMessage → Commit。', `runID / owner|string / string|领取身份链传入|ID 定位任务，owner 证明当前执行者，二者不可互换。
revision|int|执行开始时保留的预期版本|用于 WHERE 匹配，不直接取“现在最新版本”绕过并发保护。
output|json.RawMessage|运行结果序列化|写 Run.output，也供助手消息构造；不是可任意执行的 SQL。
now|time.Time|完成操作时刻|同时判断租约/deadline 有效，写 updated_at/completed_at。
item|agent.Run|UPDATE RETURNING|这是新版本终态记录；不同于原领取快照。
tx|*sql.Tx|本次完成操作新开的事务|不沿用最初接受或领取的长事务。`),
  ],
  'agent-approval-recovery': [
    D('loadRequest', '输入 runID、expectedRevision，返回 (当前 Run, conversation.ToolRequest, error)。查数据库、校验运行状态/版本/期限并还原可信工具上下文；此函数本身不提交工具副作用。', 'Prepare、Commit、Definitions 等网关入口共用；runs.Get → Unmarshal Run.Input → 过滤历史 → 组装 ToolRequest。', `g|*ToolGateway|方法接收者|持有 runs/tools/secret/now 等依赖；g=nil 或依赖缺失则拒绝。
run / expectedRevision|Run / int|数据库当前记录 / 调用方领取版本|两者 Revision 必须一致且状态 running；不能只信任 Python 传来的用户身份。
input|agentRunInput|Run.Input 解码|MessageID/Text/Context.History 是已接受输入，不是 HTTP 审批体。
history|[]conversation.Message|过滤后的历史|只留 user/assistant 非空正文，再取最近最多 20 条。
now|time.Time|g.now().UTC()|检查 LeaseExpiresAt 与 DeadlineAt，不是模型上下文时间。
runID|string|去空白后的调用参数|用于读取权威运行记录，ToolRequest 的 UserID/Module 从 run 取。`),
    D('Prepare', '输入 ToolPrepareInput，返回 (ToolPreparation, error)。可能执行只读/低风险工具、启动任务，或形成待确认动作；Status=completed 只描述准备阶段。', '图的工具预检链进入 Go 网关；loadRequest → ModelTools 白名单 → ExecuteModelTool → prepareConfirmation/公开结果。', `input|ToolPrepareInput|工具调用提案|CallKey 是动作键，ToolName 是工具名，Arguments 是参数 map，ExpectedRevision 来自可信运行边界。
run / request|Run / conversation.ToolRequest|loadRequest|run 提供身份，request 是传给领域工具的上下文；两者不是同一类型。
request.JobID|string|先为 Run ID，随后设 input.CallKey|此处以单动作身份作为下游任务幂等边界，一个 Run 可有多个动作。
result|conversation.ToolResult|ExecuteModelTool 返回|Response 是说明，Data 是业务结果，Confirmation 非 nil 表示需确认。
skillRun / ok|skill.Run / bool|skillRunAwaitingConfirmation(result.Data)|检查已创建的 Skill 是否仍在等待确认，而非所有 Data 都是 Skill。
response|string|result.Response 去空白|空时用兜底提示；不应拿此提示替代正式工具状态。`),
    D('approval', '输入 AgentState，返回跳往 commit_tool/reject_tool 的 Command。interrupt 先持久化等待；恢复后验证 resolution.approved 确为 bool，再产生状态更新，不直接记账。', 'LangGraph 进入 approval 节点；interrupt 等外部结果；_updated_recovery/_trace_event 构造恢复诊断；Command 指定下一节点。', `state|AgentState，字典形状态|图引擎传入|含 run_id、preparation、model_manifest、steps 等，不是数据库事务。
preparation|工具准备结果字典|state["preparation"]|提供 tool_name/risk_level/summary/normalized_arguments/confirmation_token。
resolution|运行时先验为未知对象，验证后 dict|interrupt 恢复返回|必须含真正 bool 的 approved；字符串 "false" 不合格。
update|dict|本节点局部构造|保存 approval、副恢复计数、node_trace 和 steps+1，再交图合并。
steps|state 中 int 计数|state.get("steps", 0)|本节点执行后加一，不是网络重试次数。`),
    D('resolveAgentRun', 'HTTP 审批入口，无直接 Go 返回值。读取 approved 三态、当前用户、Run 路径与幂等键，调用 ResolveApproval；新决策 HTTP 202，幂等复用 HTTP 200。', '浏览器 consumeAgentRun POST resolve → ResolveApproval → writeJSON(publicAgentRun)。', `input|匿名结构，Approved *bool|JSON 请求体|nil=未提供/null，指向 false=拒绝，指向 true=批准。
item|agent.Run|ResolveApproval 返回|服务端已处理审批后的运行记录，不是待确认账目。
changed|bool|ResolveApproval 第二返回值|表示本次是否发生新决策变化；不是用户是否批准。
status|int|默认 StatusOK=200|changed=true 才变为 StatusAccepted=202。
r / w|*http.Request / http.ResponseWriter|HTTP 框架|r 提供 run_id/Idempotency-Key；w 写公开响应。
err|error|ResolveApproval 第三返回值|错误时先返回，不能用 changed 推断失败/成功。`),
    D('Commit', '输入 ToolCommitInput，返回 (ToolOutcome, error)。必须批准且确认令牌合法，按 claims.Kind 调用具体领域确认；本摘录展示 ledger 分支，它会产生正式账目。', '恢复后的 commit_tool → Go Commit → loadRequest/verifyToken → ledger.Confirm；其他 Kind 在同函数后续分支处理。', `input|ToolCommitInput|恢复路径传入|CallKey、ConfirmationToken、Approved、ExpectedRevision；不是前端只含 approved 的原始 HTTP 体。
run|Run|loadRequest|再次验证版本与期限，批准不等于永久执行权。
claims|confirmationClaims|verifyToken|含 RunID/UserID/Kind/CandidateID 等签名绑定信息，防止随意替换候选。
key|string|"agent:" + run.ID + ":" + input.CallKey|下游领域确认的幂等键，连接运行与动作身份。
item|ledger.Entry（本分支）|g.ledger.Confirm|正式业务记录，放进 ToolOutcome.Data。
confirmErr|error|领域 Confirm 返回|保存失败时不能返回“已写入”提示。`),
    D('executeClaimed', '输入已领取 Run，返回 error。启动执行 goroutine，同时观察结果、控制状态、deadline、取消；可能触发终态写入，不只是在内存里等结果。', 'RuntimeWorker 领取后进入；suspendPendingTool → executor.Execute；结果交 finishExecution，控制变化交 FinalizeCancellation/Timeout。', `w|*RuntimeWorker|后台执行者实例|包含 service、executor、owner 与轮询间隔；w 不再是 HTTP ResponseWriter。
run / latest|Run / Run|最初领取快照 / 后续重新读取|比较版本与 owner 识别接管；latest 不是自动覆盖 run 的同一变量。
execCtx / cancelExec|context.Context / CancelFunc|WithCancel(ctx)|传递给执行器，失权/超时等路径触发取消。
execution|函数内结构类型；select 内也有同名局部变量|包含 result ExecutionResult、err error|一次执行既有协议结果又可能有错误，二者一起经通道交回。
resultCh|chan execution，容量 1|make|允许主协程先退出时发送者仍发送一次结果。
controlInterval / ticker|time.Duration / *time.Ticker|配置或默认 1 秒|周期查服务器状态，不是模型重试间隔。
deadlineTimer / deadline|*time.Timer / <-chan time.Time|存在 DeadlineAt 才建立|deadline 初始 nil 时对应 select 分支不会就绪。
until|time.Duration|time.Until(run.DeadlineAt)|负值钳到 0，表示已到期应尽快处理。`),
    D('execute', 'AgentRuntime.execute 输入 run_id，以及仅关键字参数 initial/resolution；返回 dict。检查持久化检查点，选择首次 invoke、等待返回、resume 或从剩余节点继续；可能调用多个节点和外部工具。', 'execute_run → runtime.execute；runtime_config/get_state → _validate_checkpoint_identity → _graph.invoke。', `self|AgentRuntime 实例|方法接收者|持有编译后的 _graph、递归上限、预期检查点身份。
run_id|str|Go Run 的 ID|用于运行配置与检查点定位，不是用户会话总 ID。
initial / resolution|Mapping[str, Any] 或 None|初始输入 / 已持久化恢复决策|没有检查点不能只给 resolution；已消费的决策不能重复 resume。
config|运行配置 dict|runtime_config(run_id, ...)|绑定线程身份和递归上限，传 get_state/invoke。
snapshot / values|图状态快照对象 / dict|_graph.get_state / snapshot.values|values 是复制的状态视图，不是模型输入 Token 清单。
next_nodes|tuple|snapshot.next|为空且无待审批通常说明无剩余节点；非空可 invoke(None) 继续。
pending / normalized|中断载荷列表 / 校验后的决策对象|快照提取 / _validate_resolution|前者说明等待什么，后者才交 Command(resume=...)。
result|dict 形图输出|_graph.invoke|cast 只提供类型意图，不自动修复错误数据。`),
  ],
  'context-memory': [
    D('Send', '输入 userID、conversationID、content；返回 (Message, Job, error)。普通聊天接受入口，会持久化输入和 Job，并按策略启动或排队；不是 Agent.AcceptChat。', 'sendMessage 的普通分支调用；ValidateContent → AcceptMessage → AppendEvent → policy/start。', `content|string|调用参数，去空白后使用|校验长度/安全规则后放入用户 Message。
messageID / jobID|string / string|独立生成的新 ID|message 标识发言，job 标识本次普通生成。
message / job|Message / Job|服务构造，再由 AcceptMessage 返回|Job.UserMessageID 关联输入，Attempt=1；Message.completed 不等于 Job.completed。
policy|reliability.Policy|s.policy()|AcceptOnly=true 时只接受；s.asyncDispatch 另决定是否本进程启动。
now|time.Time|s.now().UTC()|创建时间与 job.DeadlineAt 的计算基准。`),
    D('selectRecentMessages', '输入历史 messages、预算 budget、forceProgress；返回 (recent, tokens, dropped)。纯选择函数，不删除数据库历史、不请求模型；dropped 是待摘要的旧消息。', '上下文构建器使用；groupMessages 分组 → messageGroupTokens 估算 → flattenMessageGroups 展平。', `messages|[]Message|按顺序的历史输入|当前函数只选择，不在数据库中移除旧消息。
budget / forceProgress|int / bool|构建器预算和强制推进标志|后者在全部可保留且组数大于 1 时主动挪出前半组做摘要。
groups|[][]Message|groupMessages(messages)|按同 sequence 聚合，组内可含多个气泡，不固定等于一问一答。
keptFrom|int|初值 len(groups)，倒序选择时向前移|保留范围为 groups[keptFrom:]，不是消息的物理行号。
tokens / groupTokens|int / int|累计 0 / 每组估计|第一个最新完整组即使超 budget 仍保留；随后再判断超限。
recent / dropped|[]Message / []Message|将保留区/之前区展平|recent 供当前输入；dropped 可参与摘要，不表示永久丢弃。`),
    D('BuildContext, contextSnapshot', 'BuildContext 输入用户、会话、query，返回 (contextengine.Snapshot, error)，先查归属再构建历史。contextSnapshot 将 ContextResult、记忆与产品知识合并成快照；历史构建可能更新摘要，召回可能调用配置的语义服务。', '普通聊天/Agent 接受共用；GetConversation → contextBuilder.Build → contextSnapshot → productknowledge.Context / RecallContext / BoundMemories。', `query|string|当前请求文本|用于召回相关资料；Agent intake 当前消息尚未入库，不额外重复塞入 History。
result|ContextResult|contextBuilder.Build|含 Messages、Summary、RecentTokens 等，不是模型回答。
recall|bool|s.policy().UseFullRAG|决定是否尝试长期记忆；内置产品知识在此之前仍可注入。
snapshot|contextengine.Snapshot|初始化空 History/Memories/Manifest|逐步追加历史、摘要、知识与记忆，是跨 Go/Python 的版本化数据。
summary|*ConversationSummary|result.Summary|可能 nil；非 nil 时转为带来源和范围的 Item。
candidates|[]contextengine.Item|RecallContext 或兼容文本 Recall|先取得候选，再做预算筛选，不全都发给模型。
memories / ok|sourcedMemoryContext / bool|接口能力断言|有来源召回优先；不支持时用文本接口兼容。
texts|[]string|旧 Recall 接口|包装为来源 kind=memory 的 Item，但未凭空补真实源消息 ID。
err|error|记忆召回错误|此处记录 memory_unavailable 降级并返回已有快照，不把整个行为都当失败。`),
    D('RecallContext', '输入 userID、query、limit；返回 ([]contextengine.Item, error)。只读取当前用户最多 500 条记忆并排序筛选，保留来源；不是把查询保存成一条新记忆。', 'contextSnapshot 调用；ListMemories → semanticScores/tokens → 加权排序 → 限量转换。', `items|[]Memory|ListMemories(ctx, userID, 500)|每条还检查 Status、ValidFrom、ValidTo，不以存在就视为当前有效。
queryTokens / memoryTokens|map[string]bool|tokens(query)/tokens(item.Content)|这是词法特征集合，不是模型 Token 数或 tokenizer 输出。
overlap|int|每条候选从 0 计|统计命中的特征个数，再除以查询特征数形成词法相关度。
semanticScores|map[string]float64|可选语义检索结果|按记忆 ID 查分；与词法得分取较大值，不简单相加。
semantic / ageDays / recency / score|float64|相关度 / 更新时间差 / 指数衰减 / 加权结果|score=.55*semantic+.25*recency+.20*Importance，Pinned 再加 .35；不是置信概率。
matches|[]scored，局部结构含 item 与 score|空列表逐条追加|稳定排序后再按 limit 取前几条。
limit|int|调用方指定|非 1–20 时回退 8，约束结果条数而非输入窗口。
result|[]contextengine.Item|选中 Memory 转换|Content 加来源 ID、会话、消息、更新时间；供下一层预算选择。`),
    D('CorrectFromMessage', '输入 user、memoryID、新 content 与来源 conversationID/messageID；返回 (新 Memory, error)。构造替代事实并调用事务 UpsertMemory，保留旧事实关系。', '显式纠正领域路径使用；GetMemory → normalize/hash/classify/sensitivity → UpsertMemory。', `user / memoryID|string / string|用户身份与被纠正事实 ID|两者共同查旧事实，不能凭 memoryID 跨用户改写。
content|string|纠正后的新正文|去空白、长度校验，再计算规范化哈希与类别。
old|Memory 值|GetMemory 返回|名字易误导：随后被改为“新记录模板”；先 old.SupersedesID=old.ID，再 old.ID=nextID。
nextID|string|id.New()|真正新事实 ID；并非沿用被替代记录 ID。
conversationID / messageID|string / string|本次纠正的证据来源|写 SourceConversationID/SourceMessageID；不是记忆 ID。
now|time.Time|s.now().UTC()|新事实 ValidFrom、CreatedAt、UpdatedAt；ValidTo 清 nil。
saved|Memory|UpsertMemory 返回|以存储最终返回结果为准；第二个 bool 在这里被忽略，但 err 仍返回。`),
    D('UpsertMemory', '输入 item memory.Memory；返回 (结果 Memory, 是否新建 bool, error)。可普通去重，也可同事务关闭旧事实并插入替代事实。返回 true 仍须看 Commit 是否出错。', '记忆创建/纠正领域方法 → UpsertMemory；BeginTx → 旧事实 UPDATE → 查重/INSERT → Commit。', `item|memory.Memory|待保存事实|SupersedesID 非空才进入替代分支；UserID 限制归属，NormalizedHash 用于内容去重。
tx|*sql.Tx|BeginTx|旧事实关闭与新事实插入共用；中途 return 的 defer 负责回滚。
result / n|sql.Result / int64|旧事实 UPDATE / RowsAffected|必须恰好改变一条旧 active 事实，零行不能继续当作成功。
replaceErr|error|旧事实 UPDATE|与后面查重 err 区别，指出失败发生在关闭旧事实阶段。
existing|memory.Memory|按 user+normalized_hash 查 active 记录|普通重复返回此记录且 bool=false；替代与已有活动内容冲突则报错。
err|error|查询、插入或提交结果|仅 sql.ErrNoRows 表示可以新插入；其他查询错误不能当“没有重复”。`),
    D('BoundMemories, ReferenceText, EstimateTokens', 'BoundMemories 返回 (完整事实列表, 估算成本, 未入选数)；Snapshot.ReferenceText 返回参考文本；EstimateTokens 返回 int 估计。三者不读写数据库、不调用模型。', '构建快照时筛事实，组织消息时生成参考 JSON；两个估算口径分别用于选择正文与含来源的整项成本。', `items / item|[]Item / Item|已排序候选 / range 当前值|item 有 Content、Source、EstimatedTokens；这里只修改副本并追加 selected。
budget / selected / tokens|int / []Item / int|调用预算 / 空切片 / 0|完整一项放得下才入选；tokens 只累加入选项 cost。
seen|map[string]bool|空 map|以 Content 去重，预算判断前即标记，重复项不反复尝试。
encoded / cost|[]byte / int|整 Item 的 JSON / EstimateTokens+4|包括来源开销，与 item.EstimatedTokens 的纯正文估算不同。
s|Snapshot|ReferenceText 的值接收者|读取 Summary/Memories/Knowledge，均空时返回空串。
value / r|string / rune|EstimateTokens 输入 / 逐个 Unicode 码点|不是逐字节遍历，也不等于提供商 tokenizer。
count / other|int / int|均从 0 计|count 记中日韩文字，other 记其余码点，返回 count+(other+3)/4。`),
    D('PromptUpperBound, CheckRequest', 'PromptUpperBound(payload) 返回 (请求估算上界, error)；CheckRequest(payload, window, output) 返回 (同一上界, error)。只做本地计算，不发模型请求；超窗返回 ErrBudgetExceeded。', '模型请求预算门禁使用；选择 messages/tools/tool_choice/response_format → 序列化 → 计算结构开销 → 校验输出预留。', `payload|map[string]any|待发送的模型请求对象|不是 Agent.Run.Input；包含模型 messages 和工具定义等。
fields|map[string]any|新建空 map，再拷贝指定字段|限制计算范围，不修改原 payload。
normalized|map[string]json.RawMessage|encoded 再解码|统一不同 Go 切片类型，便于统计消息/工具数组项数。
encoded|[]byte|json.Marshal(fields)|len 数 UTF-8 字节，本实现拿它作保守上界基础，不是账单 Token 数。
tokens / overhead|int / int|len(encoded)+64 / 每 message 16、每 tool 8|上界累计器与单项结构开销。
window / output|int / int|CheckRequest 参数|window<=0 用默认窗口；output 是要预留的生成量，不是已经生成的正文。
SafetyTokens|常量 int=1024|模块常量|输入上界+output+安全余量必须不大于 window。`),
    D('history_view', '输入 context 映射与 role 节点角色；返回 (选中 list[Any], 遗漏 int)。按完整 sequence 组选择，不改存储、不调用模型；role 是 router/planner 等节点，不是 user/assistant 消息角色。', 'model_history/add_references 调用；snapshot 检查契约，estimate_tokens 做选择估算。', `context / bundle|Mapping[str, Any] / 快照 Mapping 或 None|调用参数 / snapshot(context)|Go-owned conversation_context 数据，不是 Go 的取消 Context。
raw|运行时未知，校验后 list|bundle.history 或 []|非列表直接返回空；列表内部元素还逐项检查 Mapping。
groups|list[list[Any]]|逐条历史按相邻 sequence 分组|sequence 为 None 时不并入上一组。
budget / role|int / str|角色预算表 / 函数参数|未知 role 默认 6000，router 为 2000。
selected / used|list[Any] / int|[] / 0|从最新组向旧组取，selected[0:0]=group 恢复正序。
group / cost|list[Any] / int|当前整组 / 正文估算每条+4之和|第一组超局部预算仍保留；后续组超限则 break。`),
    D('Context', '产品知识 Context 输入 query 和 history，返回 (参考 Item 列表, tokens, omitted)。从随应用提供的知识页检索并裁剪，不读取用户上传文件，不调用远端模型。', 'contextSnapshot 调用；RetrievalQuery 处理短追问 → Search → BoundMemories。', `query|string|当前问题，后经 RetrievalQuery 改写|非产品问题可变空直接返回；最多截 1000 rune 限检索工作量。
history|[]contextengine.Message|选中的历史|帮助识别短追问上下文，不是完整数据库会话。
runes|[]rune|query 转换|按码点而非字节截断。
hits / hit|搜索命中列表 / 单条命中|Search(query, 8)|每条 hit.Page 提供标题、正文和来源。
p|知识 Page 对象|hit.Page 的局部短名|p.ID/p.Version 进入来源，Title/Body 等形成参考正文。
items / selected|[]contextengine.Item / []contextengine.Item|候选 / BoundMemories 结果|selected 才被带入快照；tokens/omitted 分别是成本与未入选数。`),
  ],
  'documents-wiki': [
    D('Upload', '输入 userID、originalName、data 字节；返回 (Document, 是否新建 bool, error)。校验后先写 Blob 再建元数据/任务；queued 不是解析完，跨 Blob/SQL 不在同一事务。', '上传 HTTP 入口 → cleanName/detectMediaType → blobs.Put → store.CreateDocument。', `originalName / name|string / string|客户端文件名 / cleanName 返回|只用清理后名称入元数据，原名不能作为任意本地文件路径。
data|[]byte|上传字节|检查非空与 s.maxBytes；normalizePDFPrefix 可能调整其内容后再算哈希。
mediaType|string|detectMediaType(data)|按内容检测媒体类型，不只看文件扩展名。
digest / hash|[32]byte / string|SHA-256 / 十六进制表示|用于内容寻址与完整性标识，不是文件内容加密。
fileID / documentID / jobID|string / string / string|三次 ID 生成|分别标识文件、用户文档、解析任务，不可当同一个 ID。
item|Document|组装的新元数据|StorageKey 由用户与内容 hash 派生；Status 初始 queued。
saved / created|Document / bool|CreateDocument 返回|created=false 可能是复用；saved 才是最终返回记录。`),
    D('process', 'Ingestor.process 输入已领取 IngestJob，返回 (已处理 bool, error)。读取 Blob、解析、建索引、存解析结构并完成任务；true 不等于整个解析成功。', 'RunOnce/RunJob 领取后调用；blobs.Get → parser.Parse → prepareResult → index.Ensure/Upsert → SaveParsedDocument → CompleteIngestJob。', `i|*Ingestor|方法接收者|有 store、blobs、parser、index、workerID、now；这里 i 不是循环下标。
job|IngestJob|领取结果|job.Document 包含文件 StorageKey/MediaType 与归属；job.ID 定位后台任务。
fail|func(error) (bool, error)|函数内闭包|分类错误并尝试 FailIngestJob，返回 true 和原 cause；标记失败的错误被忽略。
cause / code|error / string|fail 入参 / failureCode(cause)|前者为原始原因，后者是可存储错误类别。
data|[]byte|blobs.Get|原始文件内容交 Python Parser，不是已切块正文。
result|ParseResult|parser.Parse|prepareResult 通过指针补稳定标识，Chunks 用于索引，完整结果用于存储。`),
    D('_chunk_pages, emit', '_chunk_pages(pages: list[Page]) 返回 list[Chunk]。纯内存切块；内部 emit 无参数、返回 None，通过闭包更新缓冲、序号和输出列表。', '文档解析流程调用；_heading 识别标题，_split_oversized_block 拆大块，count_tokens 验预算，_chunk 构造片段。', `pages / page|list[Page] / Page|解析后的页列表 / 当前页|page.page_no 保留页号，page.text 是可提取文字。
chunks|list[Chunk]|空列表|emit 每次追加一个非空片段，最终返回。
section_stack / buffer_section|list[str] / str|跨页标题栈 / 当前栈拼接|标题级别变化时截栈，再形成章节路径，不是文件系统目录。
ordinal|int|初始 0|每发出一个 Chunk 加一，跨页连续。
buffer|list[str]|每页开始 []|聚合文本段；emit 后保留 overlap 尾部，超预算时可能清掉。
content / candidate|string 对应 Python str|emit 中正文 / 尝试加入下一段的文本|content 真正进入 Chunk，candidate 先用于预算判断。
blocks / block / part|list[str] / str / str|按空行分段 / 当前段 / 进一步拆块|三个粒度不同，part 才被 append 到 buffer。
heading / level / title|可选二元组 / int / str|_heading(block)|标题时调整章节栈；level 不是页号。
emit|内部函数|闭包创建|nonlocal buffer, ordinal 允许重新绑定外层变量；chunks.append 则修改已有列表。`),
    D('Query', '输入用户与 QueryInput，返回 (QueryResult, error)。限定可见 ready 文档后检索与重排；无允许来源可正常返回“资料不足”，不是数据库错误。', '文档问答入口 → normalizeQuery/ListDocuments → index.Search → rerankedAnswer。索引/重排是否使用外部服务取决于配置。', `input|QueryInput|请求参数|Query 是问题，DocumentIDs 可限定来源，Limit 是最终命中数量约束。
query / limit|string / int|normalizeQuery(input)|规范化问题与条数，limit*3 为候选检索量。
items|[]Document|当前用户最多 200 条元数据|用于建立来源允许范围，不是所有用户文档。
requested|map[string]bool|DocumentIDs 建集合|空集表示未额外指定，不表示跨用户无限搜索。
readyIDs|[]string|从 items 筛 Status=ready 且符合 requested|真正交给索引的可用文档 ID 列表。
hits|[]SearchHit|index.Search|候选片段带内容、分数、页与章节信息，未必全部出现在最终回答。
allowed|map[string]Document|documentsByID(items, readyIDs)|重排/引用构建时校验和补全文档信息。`),
    D('ReadParsedContextRoundWindow', '输入 userID/documentID、每轮 maxTokensPerRound、1 起始 roundStart、最多 maxRounds；返回 (DocumentContext, error)。只读解析结构并组装本轮来源，coverage 描述此次调用，不累计历史调用。', '完整文件处理工具调用；GetDocument → ParsedStore.ListDocumentChunks → partitionContextRounds → cleanContextText/readSourceIR。', `item / store|Document / ParsedStore|按归属读取 / 接口能力断言|文档必须 ready，且存储必须支持读取解析片段。
chunks / roundChunks|[]Chunk / [][]Chunk|最多 10000 片段 / 按预算分轮|chunks 为读取全集，roundChunks 为分轮视图。
maxTokensPerRound / roundStart / maxRounds|int / int / int|参数，非正分别回退 4000/1/4|预算、开始轮号、窗口轮数，不是页数。
startIndex / endIndex / completedRounds|int / int / int|轮号转 0 基下标、截尾 / 差值|切片是左闭右开；返回 NextRound=endIndex+1。
selected / manifest / blocks|[]Chunk / []ContextRound / []string|逐轮累计|分别供覆盖/来源、诊断清单、最终 Markdown 文本。
report|CleaningReport|含 PolicyVersion 的初始结构|累计去重行与空行折叠，不会改原 Blob。
marker / section / roundMarker|string / string / string|页范围、章节说明、轮次标记|帮助模型与引用保留边界，不是用户原文件新增的永久内容。
roundTokens / estimatedTokens|int / int|每轮/全次累计成本|估计包含每片额外开销，不是提供商费用。
coverage / sourceIR|float64 / map[string]any|len(selected)/len(chunks) / readSourceIR|coverage 是 0–1 比例；sourceIR 为稳定实体/出处，不能用文本“已读完”替代。`),
    D('CompileNextWiki', '输入 ctx，返回 (是否领取处理 bool, error)。无 WikiStore 或无任务返回 false,nil；已领取则编译并保存成功/失败，可能调用配置的编译模型。', '后台调度调用；ClaimWiki → compileWiki → FinishWiki。', `store / ok|WikiStore / bool|接口断言|判断当前存储是否支持 Wiki 任务，而不是用户是否有阅读权限。
job|WikiJob|ClaimWiki|已领取的编译工作与执行权信息，完成时传回 FinishWiki。
compileCtx / cancel|context.Context / CancelFunc|8 分钟 WithTimeout|约束编译时长，使其短于注释所述 10 分钟租期。
pages|[]WikiPage|compileWiki 返回|待保存的派生页面，不是原文件页 Page。
err / finishErr|error / error|编译错误 / 标记失败错误|errors.Join 保留两个原因，避免只看到生成失败而漏掉持久化失败。
wikiRuntime|Service 内部计数器集合|服务初始化|failures/compiles 分别记录编译失败与成功完成，不是用户消息。`),
    D('ListWiki, ReadWiki', 'ListWiki 返回可见页面列表并标记 Stale；它本身可能包含 Stale=true 的页面。ReadWiki 查目标页并拒绝 stale，返回 (WikiPage, error)。二者不能混称为同一过滤步骤。', 'Wiki 列表/阅读接口及工具调用；wikiDocuments → ListWikiPages → 逐来源校验；ReadWiki 再查 pageID 与 Stale。', `user / workspace|string / string|调用方已校验的范围|空 workspace 表示个人范围；成员身份应在相应入口校验。
docs / allowed|[]Document / map[string]Document|可访问文档 / 只纳入 ready 的 ID 映射|allowed 决定页面每个来源是否可读。
owners / seen|[]string / map[string]bool|从可见文档收集并去重|减少要拉取的页面所有者；不是最终授权结论。
pages / p|[]WikiPage / WikiPage|Store 候选 / 当前页面值|检查 p.Sources，每个来源都要落在 allowed。
visible|bool|初值 len(p.Sources)>0|任一依赖缺失就 false；空来源页面也不直接放行。
source / d|Wiki 来源对象 / Document|p.Sources 中一项 / allowed 查询|比较 SourceVersion(d) 与 source.Version，不匹配标记 p.Stale。
result|[]WikiPage|只追加 visible 的页面|列表保留 stale 标记；ReadWiki 对指定 stale 页返回 ErrWikiConflict。`),
    D('UpdateWiki', '输入 user、pageID、预期 version、title/body；返回 (保存后 WikiPage, error)。只允许当前所有者编辑个人可见页面，并通过版本条件防旧编辑覆盖。', '人工编辑入口 → ListWiki → 字段/归属/版本校验 → SaveWikiPage(ctx, p, version)。', `pages / p|[]WikiPage / WikiPage|ListWiki(user, "") / 当前页面|同时要求 p.ID=pageID、p.OwnerID=user。
version|int|编辑页读取时保存的版本|必须等于 p.Version，且传给 Store 做条件保存；不是自动采用最新版。
title / body|string / string|用户编辑内容|title 按 rune 限 1–160；body 此处按字节 len 限 24000。
p.Edited / p.UpdatedAt|bool / time.Time|本次赋 true / s.now().UTC()|标记人工修改和时间，而非直接重写原始文件。`),
  ],
  'life-tools': [
    D('Parse', '输入 text、timezone、reference time.Time；返回 (Candidate, error)。纯领域解析，不读写数据库；含 NeedsClarification 的候选可以正常返回，之后不能直接确认。', '账本候选构造路径调用；extractAmount/detectDirection/detectCategory/parseOccurredAt 解析各字段。', `text / timezone|string / string|自然语言 / 用户时区|正文清理并校验长度；时区用于解释“今天/上午”。
reference / location|time.Time / *time.Location|调用方参考时刻 / LoadLocation|reference.In(location) 是用户当地参考时间，不改真实瞬间。
result|Candidate|初始 pending、Confidence=.25|逐字段补充；缺项追加 NeedsClarification，最后可变 needs_clarification。
amountText / currencyText / hasAmount|string / string / bool|extractAmount|字符串匹配结果与是否识别到金额；hasAmount 不代表方向和日期也完整。
amount|float64|strconv.ParseFloat|验证大于 0 后乘 100 并 Round，转换为 AmountMinor 整数分。
occurredAt / precision / ok|time.Time / string / bool|parseOccurredAt|日期是否解析成功及精度；成功后存 &utc，失败追加 occurred_at 缺项。
utc|time.Time|occurredAt.UTC()|本次局部时间点，其地址保存为可空 OccurredAt。`),
    D('Confirm', 'ledger.Service.Confirm 输入用户、candidateID、idempotencyKey、note，返回 (Entry, 是否新建 bool, error)。候选完整才调用 Store 写正式账目；bool 含义不能套到 Skill.Confirm。', 'HTTP 或 Agent 网关提交 → GetCandidate → ConfirmCandidate。', `candidate|Candidate|按 userID+candidateID 查库|Status、OccurredAt、NeedsClarification 决定能否确认。
idempotencyKey|string|调用方同次确认的键|去空白并限长度；写 Entry 字段且传 Store 防重复。
entryID / entry|string / Entry|新 ID / 从候选拷字段|AmountMinor、Currency、Direction 等来自已验证候选，Note 来自本次备注。
note|string|确认输入|TrimSpace 后存储，不用于识别用户身份。
now|time.Time|s.now().UTC()|正式记录创建/更新时间与候选确认操作时间。`),
    D('parseReminder, ConfirmReminder', 'parseReminder 输入文本、日期槽位、时区与可选来源消息，返回 (Reminder,error)，会保存候选提醒。ConfirmReminder 输入 reminderID 与幂等键，返回 (Reminder, bool, error)，检查字段完整后确认。', '提醒解析入口 → FindReminderBySourceMessage/ParseReminderWithSlots/CreateReminder；确认入口 → GetReminder/Store.ConfirmReminder。', `sourceMessageID|string|来自聊天时的源消息|非空时按来源查重；空时没有这一层来源去重。
text / title / dateHint / timezone|string|自然语言与槽位参数|交 ParseReminderWithSlots，dateHint 是补充日期线索，不是最终 UTC due time。
existing|Reminder|来源查重成功|直接返回旧候选，避免同源重复创建。
item|Reminder|解析结果或确认时查库|解析阶段填 ID/UserID/来源/时间；确认时要求 DueAt 非 nil 且无待澄清项。
reminderID / idempotencyKey|string / string|确认参数|一个定位提醒，一个识别逻辑确认，不能互换。`),
    D('AddTodayItem', '输入用户、本地日期、时区、标题、source；返回 (PlanItem,error)。同日期没计划则建计划，有则加条目；只有 chat: 来源按稳定来源身份查重。', '今日计划入口/工具调用；FindPlanItemBySource → ListPlans → CreatePlan 或 buildPlanItem/AddPlanItem。', `localDate / timezone|string / string|例如 YYYY-MM-DD 与 Asia/Shanghai|必须能按该时区解析，localDate 不是 UTC 时间戳。
title / source|string / string|用户标题 / 来源标记|去空白；source 为 chat:... 才使用源去重，普通 UI 来源不当唯一键。
location|*time.Location|LoadLocation|验证日期解释环境。
existing|PlanItem|FindPlanItemBySource|同聊天来源已有项时复用，不意味着同标题都合并。
plans / plan|[]Plan / Plan|ListPlans / CreatePlan 新结果|无计划时建“今日计划”，否则使用 plans[0]。
item|PlanItem|buildPlanItem|带计划关联与默认中优先级，之后 Store 保存。`),
    D('QueueExport', '输入 requestKey、month、timezone、currency 等，返回 (ExportJob,bool,error)。只接受并保存导出任务，不在此处生成 Excel 字节。', '账本导出请求 → monthRange 校验 → CreateExport；后台另行消费任务。', `requestKey|string|客户端一次导出动作的稳定键|去空白与限长后交 CreateExport 去重。
month / timezone|string / string|月份和时区|monthRange 校验统计边界；不是所有地区都按 UTC 月切分。
currency|string|输入规范化为大写|空回退 CNY，只允许 CNY/USD；不做汇率换算。
exportID / job|string / ExportJob|新 ID / queued 任务|保存 Month/Currency/Timezone/时间；没有立即可下载的文件字节。
now|time.Time|s.now().UTC()|任务时间基准。`),
  ],
  'skill-artifacts': [
    D('start', 'skill.Service.start 输入工具名、createKey、参数 map 与可选聊天来源，返回 (skill.Run, 是否新建 bool, error)。校验并创建任务；可等待确认、排队，或直接执行。', 'Start/会话启动包装调用；definitionForUser → validateObject → FindSkillRunByCreateKey → CreateSkillRun → 可选 execute。', `input|map[string]any|工具参数|按 Manifest.InputSchema 验证，不是 Agent 的整份 Run 输入。
definition|Definition|definitionForUser|含 Manifest 和 Handler；前者声明契约，后者实际执行算法。
createKey / existing|string / Run|一次逻辑动作键 / 已有任务|复用时还检查 SkillName 与聊天来源，不能把同键不同业务视为相同。
payload|[]byte|json.Marshal(input)|按 MaxInputBytes 检查大小后保存到 Run.Input。
status / state|string / string|初始 running/execute，再按策略改变|status 是任务状态，state 是当前步骤；waiting_confirmation/confirm 或 queued/queued。
run|skill.Run|新建并填预算/来源/版本字段|不是 agent.Run；Steps/Files 初始化为空，Revision=1、Attempt=1。
stateName / step|string / Step|receive、validate、plan 循环 / completedStep|补已完成的接受阶段步骤，再持久化。
conversationID / originMessageID|string / string|可选聊天关联|连接工具结果回原对话；非空时也参与重复键一致性检查。`),
    D('Confirm', 'skill.Service.Confirm 输入 userID、runID、key，返回 (Run,error)，没有 bool 返回值。确认等待状态，以 revision 保存，再排队或执行；同一确认键可复用结果。', 'Skill 确认入口/网关 → GetSkillRun → definitionForUser → SaveSkillRun → 可选 execute。', `run|skill.Run|按用户与 Run ID 查库|必须 waiting_confirmation 且 RequiresConfirmation，或已经用同 key 确认。
key / run.ConfirmationKey|string / string|本次确认键 / 已存键|相同键可返回已有结果，不同键与已有确认冲突。
definition|Definition|当前用户可使用的定义|这里实际重新解析定义；不能仅凭 SkillVersion 字段声称本函数从不读取当前注册表。
expected|int|修改前 run.Revision|保存时传预期旧版本，run.Revision 随本次更新加一。
step|Step|completedStep(..., "confirm", ...)|只保存新增步骤与更新 Run，不重复追加所有历史步骤。
latest / reloadErr|Run / error|CAS 冲突后重读|若别人已经用同 key 确认，可返回其结果；不是遇到任何冲突都覆盖。`),
    D('execute', 'skill.Runner.execute 输入已领取 Run，返回 (已处理 bool,error)。并发续租与执行，续租失败取消执行；本段由 Runner 管执行权，不是 Service.execute 的算法逻辑。', 'Runner 领取路径进入；RenewLease 与 ExecuteClaimed 并行，最终返回执行/续租错误。', `r|*Runner|方法接收者|r.workerID/r.lease/r.renewInterval 规定此 Worker 的续租身份与节奏。
executeCtx / cancel|context.Context / CancelFunc|WithCancel(ctx)|共享给续租与执行，失去租约时触发 cancel。
done|chan struct{}|无缓冲信号通道|主执行结束后 close(done)，通知续租循环退出，不传业务内容。
renewErrors|chan error，容量 1|make|只报告一次续租失败，避免无人接收时发送者卡住。
ticker|*time.Ticker|renewInterval|周期尝试续租，defer Stop 停定时资源。
executeErr / renewErr|error / error|ExecuteClaimed / RenewLease|分别是算法路径和持有执行权路径错误；返回 bool=true 仅说明本任务已处理。`),
    D('execute', 'skill.Service.execute 输入 Run 与 Definition，返回 (Run,error)。解码参数、按期限调用 Handler、审计成本、校验输出；失败可写失败状态，成功接着进入下一源码段保存文件。', 'start、Confirm 或 ExecuteClaimed 进入；Handler.Execute → persistModelAuditOutput/modelCostMicros → validateObject → json.Marshal。', `run|skill.Run 值|上游已确认/领取任务|清旧 Output，再按 MaxSteps/TimeoutMS/MaxCostMicros 等任务预算检查。
definition|Definition|工具定义|Handler.Execute 是真正算法入口；Manifest 提供 Schema 与工具名称等。
input|map[string]any|Run.Input JSON 解码|还原持久化参数，不重读网页表单。
timeout / executeCtx|time.Duration / context.Context|TimeoutMS 转毫秒时长 / WithTimeout|限制本次执行，ctx.Err 与 executeCtx.Err 区分父取消和自身超时。
started|time.Time|执行前 s.now().UTC()|执行 Step 的起始时刻。
result / executeErr|ToolResult / error|definition.Handler.Execute|result.Output 为结构化输出，result.Files 为待保存字节；返回成功还要校验。
cost / ok|int64 / bool|modelCostMicros(result.Output)|cost 以微单位计费，ok 表示费用可解析；有成本预算却无有效费用时拒绝。
output|[]byte|json.Marshal(result.Output)|验证后的输出序列化结果，与 result.Output 的 map 类型不同。`),
    D('execute', '同一 Service.execute 的交付后半段：写 Blob，组装 GeneratedFile，更新 Run 并以 expected revision 提交。实际成功状态字符串为 succeeded，CurrentState 为 deliver，不是 Agent 的 completed。', 'files.Put → SHA-256 → completedStep → SaveSkillRun；最终保存失败时尝试删除此次写入文件。', `newFiles|[]GeneratedFile|初始空，逐个追加|只含本次生成文件的元数据；与 Run.Files 全历史集合区分。
item|skill.FileOutput|range result.Files|Name/MediaType 是元数据，Data 是 []byte 文件字节；此处大小检查逐文件进行。
fileID / storageKey|string / string|新 ID / files.Put 返回|外部展示/关联身份与内部存储位置不同，不应把 StorageKey 当公开权限。
digest|[32]byte|SHA-256(item.Data)|转十六进制保存完整性摘要，不是文件压缩。
expected|int|写终态前的 run.Revision|CAS 使用旧版本，随后 run.Revision++。
executeStep / deliverStep / newSteps|Step / Step / []Step|执行完成与交付记录|executeStep 补 CompletedAt；保存时只传新增加的步骤。
run.Output / run.Status / run.CurrentState|JSON / string / string|output / succeeded / deliver|算法成功、文件写入后才提交；SaveSkillRun 失败仍不算终态保存成功。
run.WorkerID / run.LeaseExpiresAt|string / *time.Time|终态时清空 / nil|释放执行权；不是删除历史运行。`),
  ],
  'teams-admin-quotas': [
    D('CreateWorkspace', '输入已认证 identity.User 与 name，返回 (Workspace,error)。构造空间及创建者 owner 成员，交同一 Store 方法保存；不是只插一个空间名。', '团队创建接口 → CreateWorkspace → Store.CreateWorkspace(ctx, workspace, member)。', `user|identity.User|认证边界提供|ID 为空间归属，Email/DisplayName 复制到成员资料。
name|string|输入清理后|按 rune 限 2–80。
workspaceID / workspace|string / Workspace|新 ID / 空间对象|OwnerID=user.ID、Status=active。
member|Member|创建者的成员记录|Role=owner、Status=active，WorkspaceID 关联新空间。
now|time.Time|服务时钟|空间创建与成员加入时间共用。`),
    D('Invite', '输入 inviter identity.User、workspaceID、受邀 email、role，返回 (Invitation,error)。校验邀请方为活跃 owner/admin 后保存待接受邀请；此函数并不直接投递邮件或让对方立即成为成员。', '邀请入口 → requireActiveMember → CreateInvitation；通知投递由其他层/队列路径处理。', `inviter|identity.User|当前请求者|其 ID 用于成员资格查询和 InvitedBy。
email / role|string / string|规范化后的受邀邮箱/目标角色|role 仅 admin/member，不允许邀请参数直接赋 owner。
member|Member|邀请者在该空间的成员记录|member.Role 决定能否邀请，别与目标 role 混淆。
inviteID / invitation|string / Invitation|新 ID / pending 记录|受邀邮箱、目标角色、邀请者与到期点一并保存。
now|time.Time|s.now().UTC()|ExpiresAt=now+14 天，链接存在不代表永不过期。`),
    D('AcceptInvitation', '输入当前 identity.User 与 invitationID，返回 (Member, Invitation, error)。确认邀请仍 pending、未过期、邮箱匹配，再由 Store 接受并创建/更新成员关系。', '用户接受入口 → GetInvitation → Store.AcceptInvitation。', `user|identity.User|已认证账号|用其真实 Email 与邀请邮箱比较，不使用请求者随意填写的新邮箱。
invitationID / invitation|string / Invitation|链接/路径参数 / 查询结果|ID 仅用于定位，Status/ExpiresAt/Email 才决定是否可接受。
now|time.Time|服务时钟|要求 now 严格早于到期时刻。
err|error|读取或接受失败|冲突与禁止是不同原因，不应都展示成“邀请不存在”。`),
    D('send', 'email.Service.send 输入 Delivery 与 workerID，返回 (已处理 bool,error)。调用 Sender 发信，再保存投递状态；发送失败分支返回 true,nil 以继续队列，不代表邮件成功。', '邮件 Worker 领取后调用；sender.Send → FailDelivery 或 CompleteDelivery。', `item|Delivery|已领取的投递记录|RecipientEmail/Subject/BodyText 变为外发 Message。
workerID|string|处理者身份|写状态时用于领取权约束，不是发件人邮箱。
providerID|string|Sender 返回的投递标识|成功时存数据库；具体提供商可有不同格式，不是本地 Delivery.ID。
err|error|外部发送返回|非 nil 时分类保存失败；外发和数据库标 sent 之间仍有故障窗口。`),
    D('takeCeremony', '输入 kind 与 secret，返回 (Record, ceremony, error)。Store.Take 原子消费一次认证挑战，检查期限并解码；读取即消费，不是普通可反复 Get。', 'Passkey 完成注册/登录等入口调用；Key 派生存储键 → Store.Take → JSON 解码。', `kind / secret|string / string|仪式种类与一次性秘密|Key(kind, secret) 定位记录；secret 不应公开或写日志。
r|Record|Store.Take 返回|Expires 是记录绝对期限，Data 是保存的认证过程 JSON。
c|ceremony|r.Data 解码|承载 WebAuthn session/challenge 与 ExpectedOwner 等，供完成验证。
err|error|消费失败|失败、过期、解码不合法均对外 ErrDenied，避免继续验证半数据。`),
    D('BeginLogin, FinishLogin', 'BeginLogin 输入 expectedOwner，返回 (浏览器 options any, ceremony secret string, error)；FinishLogin 输入 secret 与 *http.Request，返回 (管理会话令牌 string,error)。会保存/消费挑战、验证设备签名、更新凭证并建 Session。', '管理登录前后两次请求；BeginDiscoverableLogin/saveCeremony → takeCeremony → FinishDiscoverableLogin → newSession。', `expectedOwner|string|可选预期管理员身份|非空时要求完成登录的账号一致，不是浏览器随便选的显示名。
options / data|浏览器认证参数 / 库会话数据；Finish 中 data 为 ceremony|WebAuthn 开始结果 / takeCeremony|同名 data 在两个函数类型不同，必须按当前签名读。
secret|string|saveCeremony 返回|连接前后两步的一次性凭证，不等于最终管理 Session Token。
owner / stored / saved|内部 user / Record / credentialRecord|验证 handler 逐步填充|owner 是管理员主体，stored 是存储包装，saved 是里面解出的凭证内容。
handler|func([]byte, []byte) (webauthn.User, error)|闭包传入库|根据 rawID 找凭证并检查 handle、RPID、ExpectedOwner；返回可供库验签的用户。
rawID / handle|[]byte / []byte|WebAuthn 库回调参数|前者标识凭证，后者标识凭证关联用户，不是明文密码。
c|*webauthn.Credential|FinishDiscoverableLogin|需 UserVerified 且无 CloneWarning，再更新 saved.Credential。
request|*http.Request|浏览器完成响应|含断言数据交协议库验证，不能只读一个“登录成功”字段。`),
    D('Authenticate', 'adminpasskey.Service.Authenticate 输入管理 token，返回 (opsauth.Account, adminpasskey.Session, error)。查会话、账号版本、凭证与期限，必要时写 LastSeen；与普通 identity.Authenticate 不是同一函数。', '管理接口认证使用；Store.Get session → Accounts.GetOperator → Get credential → 可选 Store.Update，冲突时重读认证。', `token / r|string / Record|管理会话凭证 / Key(session, token) 查出的记录|r.Expires 是绝对期限，不只看 session.LastSeen。
session|adminpasskey.Session|JSON 解码|Owner、AccountVersion、CredentialKey、LastSeen 绑定账号和凭证状态。
now|time.Time|s.now()|检查绝对/闲置期限；距 LastSeen 至少一分钟才尝试刷新。
a|opsauth.Account|GetOperator(session.Owner)|须 active 且 accountVersion 与会话捕获值一致。
c / cred|Record / credentialRecord|凭证存储包装 / 内容|验证归属和 RPID，凭证删除后旧会话不能继续放行。
err|error|读取或刷新结果|更新冲突再 Authenticate，不能借此重建已撤销会话。`),
    D('QuotaLimits', 'QuotaLimits 是结构体类型，不是函数，无调用和返回值。每字段用 *int 表达 nil 继承、0 禁止、-1 不限；实际资源上限再由覆盖流程计算。', 'QuotaPolicy.Limits 保存某一层覆盖值；values/apply 按相同字段顺序访问。', `Documents|*int|额度政策字段|文档数量上限，nil 不覆盖下一层。
SkillRunsPerMonth|*int|额度政策字段|每月 Skill 次数，不是当前同时运行数。
Workspaces|*int|额度政策字段|空间数量限制。
AgentRunsPerMonth|*int|额度政策字段|每月 Agent Run 次数，与 Skill 次数各算各的。
ModelCostMicrosMonthly|*int|额度政策字段|模型月度微单位成本上限，不能当作 Token 数。`),
    D('SetQuotaPolicy', '输入整层 QuotaPolicy 与 expected int64；返回 (新政策,error)。规范化审计信息、验证整数范围，再以预期版本替换一整层政策；不是只修改非空字段的 PATCH。', '管理员额度设置调用；limits.values 验每字段 → QuotaStore.SaveQuotaPolicy(ctx, policy, expected)。', `policy|QuotaPolicy 值|本次整层输入|UserID 区分全局/用户层；Actor/Reason 是审计必填。
expected|int64|调用方读取时的 Revision|保存用旧版本，policy.Revision=expected+1，防并发覆盖。
value|*int|逐个遍历 Limits.values()|nil 跳过；非 nil 值必须至少 -1 且不超安全整数界限。
store / ok|QuotaStore / bool|接口能力断言|不支持政策存储则返回 NotFound，不静默使用内存替代。
now|time.Time|s.now().UTC()|地址赋给 UpdatedAt，表示本次政策变更时刻。`),
    D('apply', 'QuotaLimits.apply(base Limits) 返回新的 Limits 值。对非 nil 覆盖字段赋值，只改本地 base；不读写数据库，也不直接统计用量。', '额度汇总按层调用 apply；values() 的顺序必须与 targets 对齐，后应用的非空值覆盖先前值。', `limits|QuotaLimits|方法接收者|本层可空覆盖字段，区别于已计算的非可空 base。
base|Limits 值参数|下层已计算额度|值传入后在此副本上覆盖，最后返回。
targets|[]*int|base 各字段地址列表|让循环能改对应字段，不是额度值的副本列表。
i / value|int / *int|下标 / 本层字段指针|value 非 nil 时 *targets[i]=*value，0 与 -1 也必须覆盖。`),
    D('Check', '输入 userID 与 resource 字符串，返回 error；nil 表示当前预检查通过，不返回剩余额度且不原子预留未来消耗。', '业务接受前调用 → Summary 计算计划/用量/有效上限 → 对应 UsageItem 比较。', `summary|billing.Summary|s.Summary(ctx,userID)|含 Plan 和 Usage；不是单个数字。
resource|string|参数去空白转小写|选择要检查哪类消耗，不认识则 Validation。
item|UsageItem|遍历 summary.Usage|Resource 匹配后比较 Used 与 Limit；Limit<0 表示不限。
err|error|Summary 失败或额度错误|QuotaError 保存 Resource/PlanCode/Used/Limit，便于上层转 HTTP 提示。`),
  ],
  'reliability-controlplane': [
    D('RunOnce', 'eventbus.Relay.RunOnce 返回 (本批领取事件数 int, 最后错误 error)。事件已发布不等于已成功标记数据库；返回数量不是成功投递数。', 'Relay 循环调用；ClaimOutboxEvents → publisher.Publish → MarkOutboxPublished；失败走 FailOutboxEvent。', `r|*Relay|方法接收者|持有 publisher、store、workerID、lease、batchSize、maxAttempts。
events / event|[]eventbus.Event / eventbus.Event|ClaimOutboxEvents|已经按租约领取的一批；event.Attempts 决定重试/死信。
ack|eventbus.PublishAck|publisher.Publish 成功返回|含 Topic/Partition/Offset，供 MarkOutboxPublished 保存，不是数据库事务 Commit。
publishErr / lastErr|error / error|当前发送或标记错误 / 本批最后错误|后续事件仍继续处理；nil 并不是对所有外部最终送达的证明。
message / dead|string / bool|错误文本截到 1024 字节 / 次数是否到顶|前者用于记录，后者决定死信或安排下次 available_at。`),
    D('RunOnce', 'ConsumerRunner.RunOnce 返回 error。拉取一次事件，处理毒消息/去重，执行业务后记录 Inbox，再确认消费；不返回“严格执行一次”的证明。', '消费者循环调用；Poll → InboxEventExists → processor.Process → RecordInboxEvent → consumer.Commit。', `r|*ConsumerRunner|消费实例|不同于 Relay；r.name 是消费者去重命名空间。
message|eventbus.KafkaMessage|consumer.Poll|含 Event 及消息系统定位信息；不是聊天 Message。
poison|PoisonMessageError|解析失败或事件 ID 非 UUID|先记录毒消息再确认，不能把坏载荷交普通业务。
seen|bool|InboxEventExists(ctx,r.name,eventID)|true 跳过业务处理；同事件不同消费者名称可有不同消费记录。
eventCtx / restored|context.Context / context.Context|ctx 或恢复的 trace 上下文|传递链路信息；AggregateType 以 _run 结尾时附 Run ID。
err|error|处理、记录 Inbox 或确认失败|前两步存在间隙，RecordInboxEvent 失败时业务可能已经发生。`),
    D('Observe', '输入 Sample 与 now，返回可靠性 Snapshot。加锁更新控制器内存等级/候选计数，并非直接发布数据库配置或取消所有任务。', '监控采样路径调用；classify 给目标 → 连续样本/最短保持约束 → snapshotLocked 输出当前政策。', `c|*Controller|控制器实例|c.mu 保护 level、candidateCount、changedAt 等共享状态。
sample|Sample|本次监控指标|QueueLag、OldestJobAge、ModelErrorRate、P95Latency 等决定候选等级。
target / reason|Level / string|classify(sample)|目标等级与解释，不保证本次立即切换。
candidate / required|Level / int|默认 target/EnterSamples|恢复时只先降一档，required 改 RecoverSamples。
now / changedAt|time.Time / 控制器字段 time.Time|采样时刻 / 最近实际切换时刻|恢复前检查 MinDwell，不能把观测时间当最后切换时间。
candidateCount|Controller 内 int|连续同候选累加|候选变化时重置为 1，满足 required 才改实际 level。`),
    D('classify, policyFor', 'classify(Sample) 返回 (Level,原因 string)，policyFor(Level) 返回 Policy。均为纯映射函数，无数据库/模型调用；策略还要由消费者实际应用。', 'Observe 调用 classify；快照构造使用 policyFor，把 L0–L3 转为具体开关。', `sample.ModelErrorRate|float64|采样错误比例|.80/.50/.20 等阈值用于分级，不是累计错误次数。
sample.OldestJobAge / sample.P95Latency|time.Duration|最老任务等待 / 延迟分位|前者与后者含义不同，不能都解释为一个请求耗时。
sample.QueueLag|int|积压度量|阈值决定压力等级，不是总历史消息量。
level|Level，底层 int|当前等级输入|L3 保护最强；分级判断从严重到轻，先命中先返回。
UseFullRAG / ExtractMemory / AcceptOnly|Policy 的 bool 字段|policyFor 返回结构|分别控制全量检索、记忆抽取与仅接受，不是所有路径自动生效的魔法全局开关。
PreferredModelClass / LongSkillsQueued|Policy 的 string / bool|等级映射|表达模型类别偏好与长工具排队政策，不直接指定提供商密钥。`),
    D('Create', 'controlplane.Service.Create 输入 CreateInput，返回 (Version,error)。验证载荷并保存不可变草稿配置；不是 character.Create，也不是直接让线上生效。', '控制面创建入口 → normalizeKind/normalizeKey → validatePayload → fingerprint → CreateConfigVersion。', `input|controlplane.CreateInput|配置种类、键、基线、载荷、操作者、原因|Kind/Key 规范化；Actor/Reason 是审计信息。
payload|验证后的 JSON 配置载荷|validatePayload 返回|可能规范化后的内容，随后用于存储与 fingerprint。
versionID|string|id.New()|配置版本记录 ID，区别于用于人读的版本号。
item|Version|组装 StatusDraft 记录|含 SchemaVersion、BaseVersion、Fingerprint、CreatedBy 等。
now|time.Time|s.now().UTC()|创建/更新时间，不是部署完成时刻。`),
    D('Validate, Submit, Publish', 'Validate/Submit 返回 (Version,error)，Publish 返回 (Version,Deployment,error)。逐阶段转状态，Agent Definition 还检查评测和替换发布灰度；会写控制面记录，但不证明所有运行实例已同步。', '管理流程先 Validate 再 Submit 再 Publish；TransitionConfigVersion、LatestPassingAgentEvaluationRun、PromoteAgentRollout/PublishConfigVersion 执行门禁与保存。', `versionID / actor|string / string|目标版本 / 当前操作者|Publish 要求 actor 与 CreatedBy、SubmittedBy 不同，落实职责分离。
item|Version|s.Get(versionID)|Status 必须符合当前阶段，Fingerprint 绑定评测与灰度到具体载荷。
rollout|AgentRollout|初始零值，替换已有发布时查询|有 ID 才走 Promote；要 ready 且 pass。
deployments / deployment|[]Deployment / Deployment|当前部署列表 / 循环项或最终发布结果|用于判断是否替换同 Key 的旧版本；同名在循环与返回局部中语境不同。
published|Version|PromoteAgentRollout 返回|已发布的新配置记录，不是字符串日志。
promoteErr / activeErr|error / error|晋级操作 / 读取活动部署|分别说明发布失败阶段，不能忽略后还说已上线。`),
  ],
  'python-model-graph': [
    D('Execute', 'PythonRuntimeExecutor.Execute 输入 Go Run，命名返回 (result ExecutionResult, executionErr error)。启动 Python 子进程、传 JSON、捕获输出与错误；完整函数后半段还有解码/契约检查，本段摘录只到初始化结果。', 'RuntimeWorker 的 executor.Execute → exec.CommandContext → Python agent_worker；不是拼接 shell 执行用户文本。', `e|PythonRuntimeExecutor|值接收者|Executable/Timeout/Environment/OnObserve 配置本次进程调用。
executable|string|e.Executable 去空白，空用 python3|可执行文件名字/路径，不是自然语言命令字符串。
run|agent.Run|领取快照，再附运行 trace|序列化整体结构供 Python 读取；用户无法直接改数据库租约。
payload|[]byte|json.Marshal(run)|送 stdin 的协议 JSON，不是直接发给提供商的模型 payload。
command|*exec.Cmd|exec.CommandContext(ctx, executable, "-m", ...)|stdin/env/stdout/stderr 配置进程边界；Run 等进程退出。
stdout / stderr|bytes.Buffer|初始空，进程写入|stdout 是结果协议，stderr 是诊断文本；混用会破坏解析。
message / structured|string / 结构化执行错误指针|截断后的 stderr / parseExecutorError|有已知结构时保留重试等错误信息，未知则包装普通错误。
result / executionErr|ExecutionResult / error|命名返回变量|延迟观测函数读取最终值；result.Status 与 err 共同描述本次调用。`),
    D('execute_run', '输入 Go Run 映射及显式 checkpointer/decisions/tools/definitions，返回执行结果 dict。构建/复用 AgentRuntime 并注入可信工具表，恢复图，将中断映射为 waiting_approval/waiting_tool。', 'agent_worker 进程入口调用；_build_runtime → 可信目录过滤 → runtime.execute → interrupt_payloads。', `run / run_id|Mapping[str,Any] / str|进程输入 / 必填 id|run 包含 input 与恢复信息，run_id 关联检查点。
checkpointer / decisions / tools|检查点对象 / DecisionPort / ToolGateway|入口装配的协作者|分别持久化图状态、调用决策模型、跨边界执行工具。
definitions|list[dict[str,Any]]|Go 侧提供的工具目录|过滤后覆盖 trusted_context.tools，不能信任原输入自带的工具清单。
runtime / policy / recursion_limit|AgentRuntime 可选 / BudgetPolicy / int|可复用参数或按 run 构建|runtime 已存在时不重复构建；递归上限限制图推进。
input_payload / message|dict / str|run.input / user_message 或 text|运行时必须检验类型与非空，不因 Mapping 注解自动合法。
context / trusted_context|dict / 外层复制的 dict|输入上下文 / dict(context)|复制后注入可信 tools；并非所有嵌套值都深复制。
initial / resolution|dict / dict 或 None|规范化初始图输入 / resume_resolution|resolution 非空却不是对象时拒绝。
result / interrupts / interrupt_types|dict / 中断列表 / set|runtime.execute / 提取 / type 集合|全部为 tool_approval 或 tool_wait 才映射对应等待状态，不接受任意混合。`),
    D('_classify_execution_mode', '输入 state、tool_name、可选可信 definition，返回 (ExecutionMode,原因 str)。纯分类，无模型/工具调用；direct 仍是图中的一种模式。', 'decide 校验模型提案后调用；根据旧计划、可信 requires_plan/repeatable 与工具种类分类。', `state / existing|AgentState / 可选模式值|图状态 / state.execution_mode|已有多步模式或 plan 时保持 agentic。
tool_name|str|模型提案经过清理的工具名|空时 direct；不以句子长短决定模式。
definition|Mapping[str,Any] 或 None|可信工具定义查询|requires_plan/repeatable 是可信属性，不是让模型自己声明权限。
normalized|str|工具名 strip/lower/点转下划线|识别附件提取等中间步骤，要求后续进展跟踪。`),
    D('decide', '图内 decide 输入 AgentState，返回 dict 状态增量。当前摘录是模型决策分支；函数更早还有确定性续处理分支。可能请求模型，校验后只提出下一动作，不在这段直接提交业务写入。', 'Graph decision 节点 → model_access_reason/with_model_allowance → decisions.decide → 可信定义/参数检查 → 模式分类 → 状态更新。', `state / node|AgentState / str|图状态 / 当前节点标签|state 有原问题、模块、计划、观察与预算，node 标记归属。
reason|str|model_access_reason|非空表示额度/访问受限，立即产生终态更新而不继续模型请求。
started_ns|int|time.perf_counter_ns()|单调高精度耗时起点，不是 Unix 日期，不能直接显示为时间戳。
decision_context|dict|with_model_allowance 再添加 plan/observations 等|为这次决策准备的模型上下文投影，不是整个 state 原样发出。
decision|ModelDecision|DecisionPort.decide 返回|含 intent/tool_name/tool_arguments/response/needs_response，仍需校验。
intent / tool_name / tool_arguments|str / str / dict|decision 清理/复制|意图不能为空；有工具才处理参数，没工具不能要求参数组合。
definition / requires_composition|可信定义或 None / bool|可信目录查询 / compose_arguments 属性|模型的要求不能扩大 trusted catalog 的能力。
response / model_update|str / dict|decision.response / 观测更新|前者为候选文本，后者为成本/耗时等诊断状态。
execution_mode / execution_mode_reason|模式字符串 / str|_classify_execution_mode|记录为什么走 direct/single_action/agentic。
update|dict[str,Any]|本节点构建的增量|先清旧 proposed_tool/preparation/tool_result，再写新的提案或回答，防残留结果混入。`),
    D('after_decision', '输入 AgentState，返回下一个节点名称 str。纯路由函数，返回 "plan" 并不意味着它自己已经生成计划；实际节点由图引擎随后执行。', 'decide 更新后条件边调用；有组合需求时由 composition_route 决定具体组合节点。', `state|AgentState|决策合并后的图状态|读取 execution_mode、plan、needs_response、outcome、response。
proposed|dict 或空/缺失值|state.get("proposed_tool")|非空说明有工具提案；compose_arguments 决定是否先构造参数。
execution_mode / needs_response|state 字段：模式值 / bool|前一节点设置|agentic 且无 plan 先计划；没工具但需回答则 generate_response。
outcome / response|state 字段：str / str|前面计算结果|completed 且有正文才进入质量门禁，否则可能 finalize。`),
    D('_request', '模型适配器 _request 输入 payload Mapping 与 timeout_seconds，返回 dict。真实外部 HTTP POST；读取响应字节有上限，HTTP/超时/JSON 错误转 OpenRouterError，本函数不等于所有上层重试策略。', '模型适配层构造 payload 后调用；urllib Request/urlopen → 有界读取 → json.loads → 对象校验。', `self|OpenRouterDecisionPort|方法接收者|_config 保存地址/密钥，_trace_headers 为追踪信息；不要写进教材真实密钥。
payload / body|Mapping[str,Any] / bytes|模型请求对象 / UTF-8 JSON|body 是线上发送的字节，不同于 Go 进程协议 payload。
timeout_seconds|float|上层为本次尝试分配|同时给 wall-clock deadline 与 urlopen，不是无限等待。
request / response|urllib.request.Request / HTTP 响应对象|构造 POST / urlopen 返回|Authorization 使用服务端配置密钥；response 不是最终助手文本。
raw|bytes|response.read(MAX+1)|多读一字节检测超大响应，校验后才解 JSON。
exc / retry_after|异常对象 / str|异常捕获 / Retry-After 头|保留状态码与退避线索给上层；不是这里偷偷睡眠重试。
decoded|JSON 解码后的未知类型，验证后 dict|json.loads(raw)|先验证是对象，cast 只补类型意图。`),
    D('generate_response', '输入 AgentState，返回 dict 状态增量。检查访问与预算，调用 responder 得到非空文本；错误返回失败/预算终态更新，成功之后仍要进入质量门禁。', 'after_decision 等路由进入；getattr(decisions,"respond") → with_model_allowance → responder → model_observability_update。', `node / reason|str / str|固定 generate_response / 访问检查结果|供预算与诊断区分模型节点。
responder|可调用对象或 None|getattr(decisions,"respond",None)|不是文本；必须 callable，调用后才得到 response。
response_context|dict|本次模型 allowance 与计划/观察/任务契约|不直接将整个状态及所有私有字段透传。
started_ns|int|perf_counter_ns|测耗时，不是消息创建时间。
response|str，经运行时检查|responder(...) 返回|去空白后写状态 response，空字符串视为失败。
state|AgentState|节点输入|读取 user_message/module 与已有 steps；输出 steps+1，并设 needs_response=false。
exc|异常对象|模型调用/验证抛出|预算异常与一般模型异常走不同更新函数。`),
  ],
};
