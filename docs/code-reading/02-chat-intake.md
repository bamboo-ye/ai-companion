# 02 从发送按钮到聊天入口

[阅读总入口](README.md)

行为例子：用户给工作助手发送“根据这个文件做一份演示”。前端先上传附件，随后发送文档 ID；Go 根据当前角色模块和制品请求决定执行路径。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 浏览器里有两种不同状态

输入框、正在发送提示、弹框去重集合属于页面状态；用户消息、Agent Run、最终回答属于服务器持久化状态。刷新会丢失一部分页面状态，但不应把服务端已经接受的任务删除。恢复过程因此要重新查询服务器，不能只靠 React 变量。

async 表示函数返回 Promise；await 暂停当前异步函数的后续步骤，等待该 Promise 得到结果，不会把整个网页冻结。fetch 收到 HTTP 409、500 等响应通常仍会得到 Response，所以源码显式检查 response.ok。网络连接失败和“服务器用错误状态拒绝了请求”不是同一种情况。

### 读发送入口时划出三条边界

第一条是浏览器的体验检查，例如空输入、有任务时禁发。第二条是服务器的可信校验，包括身份、频率、额度、附件所有权、内容。第三条是持久化接受。只有越过第三条，202 才有“已经接受”的依据；仍不代表助手已经回答。

前端限制可以被绕过，所以服务端重复校验不是多余代码。附件 ID 只是一个指向文件记录的值，不是“知道 ID 就获得读取权限”的通行证。

## 浏览器发送一条消息

源码：[web/app/chat-panel.tsx:25](/Users/windcry1/Documents/ai_companion/web/app/chat-panel.tsx:25)

<a id="symbols-2-1"></a>

### 函数与变量详解

函数/类型声明：[`send`](/Users/windcry1/Documents/ai_companion/web/app/chat-panel.tsx:25)。

**职责、输入输出与影响：** React 表单事件函数，输入 FormEvent<HTMLFormElement>，返回 Promise<void>。会 POST 消息、修改页面状态并等待任务观察；它不直接写数据库，也不自行推理。

**调用关系：** form 的 onSubmit 调用；fetch → mergeMessage → consumeAgentRun 或 consumeEvents；catch 转为页面错误提示。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `event / form` | FormEvent<HTMLFormElement> / HTMLFormElement | 提交事件 / event.currentTarget | preventDefault 阻止原生跳转；成功接受后才 form.reset。 |
| `data / content` | FormData / string | 表单内容 / content 字段去空白 | content 是用户可见文字，不是完整模型 messages。 |
| `pendingDocuments` | PendingDocument[]，React state | 附件上传响应积累 | 提取 id 作为 document_ids；已上传不代表已经解析完。 |
| `conversationID / jobID / agentRunID` | string，React state | 创建/恢复接口返回 | 依次是会话、普通任务、Agent 运行 ID；已有任务时阻止新发送。 |
| `token / apiBase` | string / string | 组件参数 / 环境配置或默认值 | token 放 Authorization；apiBase 是 API 地址，不是模型地址。 |
| `response` | Response | await fetch | 看 status/ok 决定 HTTP 成败；不是模型生成文本。 |
| `payload` | 对象：message?、job?、agent_run? | await response.json 后的类型断言 | message 还可能为错误字符串，需运行时检查；409 可携带活跃运行。 |
| `userMessage / current` | Message / Message[] | 确认响应 / setMessages 回调旧列表 | 按 ID 合并，避免恢复查询与实时结果重复展示。 |
| `cause` | catch 中的未知值 | 任意抛出的异常 | instanceof Error 后读 message；不会因为进入 catch 自动重发 POST。 |

### 源码与逐行解释

```text
  25 │   async function send(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form=event.currentTarget;const data=new FormData(form);const content=String(data.get("content")??"").trim();if((!content&&pendingDocuments.length===0)||!conversationID||jobID||agentRunID||uploading)return;setError("");setToolNotice("");setStatus("正在回应…");
  26 │     try{const response=await fetch(`${apiBase}/v1/conversations/${conversationID}/messages`,{method:"POST",headers:{"Content-Type":"application/json",Authorization:`Bearer ${token}`},body:JSON.stringify({content,document_ids:pendingDocuments.map(item=>item.id)})});const payload=await response.json() as {message?:Message|string;job?:{id:string};agent_run?:AgentRun};if(response.status===409&&payload.agent_run){setAgentRunID(payload.agent_run.id);await consumeAgentRun(payload.agent_run.id);return}if(!response.ok||!payload.message||typeof payload.message==="string")throw new Error(typeof payload.message==="string"?payload.message:"发送失败");const userMessage=payload.message;form.reset();setPendingDocuments([]);setMessages(current=>mergeMessage(current,userMessage));if(payload.agent_run){setAgentRunID(payload.agent_run.id);await consumeAgentRun(payload.agent_run.id)}else if(payload.job){setJobID(payload.job.id);await consumeEvents(payload.job.id)}else{throw new Error("服务端未返回可执行任务")}}
  27 │     catch(cause){setError(cause instanceof Error?cause.message:"发送失败");setStatus("生成失败，可重试");} }
```

- **L25**：依次阻止表单刷新、取得表单和文本；文本与附件都为空、会话未建立、有运行中任务或正在上传时直接返回；否则清空错误/工具提示并显示“正在回应”。
- **L26**：依次 POST content/document_ids → 解析响应 → 若 409 附带 active Agent 则恢复观察旧运行 → 其他非成功响应抛错 → 清空表单和待发附件 → 按消息 ID 合并服务器确认的用户消息 → 有 agent_run 则轮询，没有则用 job 建 SSE；两者都没有属于响应契约错误。
- **L27**：捕获上述任一异常，保存人类可读错误与失败提示；这里的 catch 不会自动重发原 POST，避免无意重复输入。


同一源码行 26 有多条语句，按解释中的箭头顺序阅读。form.reset() 发生在服务端已接受之后；请求失败时输入不会提前被清空。

## 发送接口的共同前置检查

源码：[internal/httpserver/conversations.go:60](/Users/windcry1/Documents/ai_companion/internal/httpserver/conversations.go:60)

<a id="symbols-2-2"></a>

### 函数与变量详解

函数/类型声明：[`sendMessage`](/Users/windcry1/Documents/ai_companion/internal/httpserver/conversations.go:60)。

**职责、输入输出与影响：** HTTP 发送入口前半段，输入 w/r，无 Go 返回值；结果写进 HTTP。先限流、解码、查额度与附件，失败就停止，不进入后半段接受运行。

**调用关系：** 经 requireAuth 的消息路由进入；realtime.Allow、currentAuth、decodeJSON、requireQuota、documents.Get 等提供前置检查。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `allowed / remaining / retryAfter` | bool / int / time.Duration | 限流器 Allow 返回 | allowed 决定继续；remaining 写响应头；retryAfter 转秒用于重试提示。 |
| `input` | 匿名结构 {Content string; DocumentIDs []string} | 请求 JSON 解码 | 是不可信输入，附件 ID 必须逐一按用户校验。 |
| `auth` | authenticated | currentAuth(r) | 可信 User 与 Claims；auth.User.ID 限定资源归属。 |
| `visibleContent` | string | input.Content 去空白或旧附件引用修复 | 保留用户可见文本；仅附件输入时补“请处理这个文件”。 |
| `documents` | []document.Document | 空切片逐项追加已通过 Get 的记录 | 保存验证后的附件元数据，不是原文件字节。 |
| `documentID / item` | string / document.Document | 循环输入 ID / Get 返回 | 不能把 documentID 本身当访问许可。 |
| `messageContent` | string | visibleContent 的副本 | 追加可信附件标记后用于内容校验与任务输入。 |

### 源码与逐行解释

```text
  60 │ func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
  61 │ 	allowed, remaining, retryAfter, err := s.realtime.Allow(r.Context(), "chat:user:"+currentAuth(r).User.ID, s.chatRateLimit, s.chatRateWindow)
  62 │ 	if err != nil {
  63 │ 		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rate_limit_unavailable", Message: "限流服务暂时不可用"})
  64 │ 		return
  65 │ 	}
  66 │ 	w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
  67 │ 	if !allowed {
  68 │ 		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
  69 │ 		writeJSON(w, http.StatusTooManyRequests, apiError{Code: "rate_limited", Message: "发送太快了，请稍后再试"})
  70 │ 		return
  71 │ 	}
  72 │ 	var input struct {
  73 │ 		Content     string   `json:"content"`
  74 │ 		DocumentIDs []string `json:"document_ids"`
  75 │ 	}
  76 │ 	if !decodeJSON(w, r, &input) {
  77 │ 		return
  78 │ 	}
  79 │ 	auth := currentAuth(r)
```

- **L60**：HTTP Handler；w 写响应，r 携带路径、请求体和已认证 Context。
- **L61**：以 chat:user:用户ID 为限流键，请求共享限流网关；返回剩余次数与重试时间。
- **L62–L65**：限流依赖不可用时返回 503；当前行为没有继续执行。
- **L66**：把剩余配额写入响应头，便于客户端显示或诊断。
- **L67–L71**：超过频率限制则返回 Retry-After 与 429，并立即停止。
- **L72–L75**：匿名结构只接受业务所需 content 和 document_ids。
- **L76–L78**：统一解析 JSON，解析失败由 decodeJSON 写错误。
- **L79**：读取可信认证结果。

```text
  80 │ 	if !s.requireQuota(w, r, billing.ResourceModelCost) {
  81 │ 		return
  82 │ 	}
  83 │ 	if len(input.DocumentIDs) > 3 {
  84 │ 		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "too_many_attachments", Message: "单条消息最多发送 3 个文件"})
  85 │ 		return
  86 │ 	}
  87 │ 	visibleContent := strings.TrimSpace(input.Content)
  88 │ 	if len(input.DocumentIDs) == 0 {
  89 │ 		var resolveErr error
  90 │ 		visibleContent, input.DocumentIDs, resolveErr = s.resolveVisibleDocumentReferences(
  91 │ 			r.Context(), auth.User.ID, visibleContent,
  92 │ 		)
```

- **L80–L82**：先检查模型成本额度；这是接受前检查，不等于对未来模型费用做事务预留。
- **L83–L86**：附件最多 3 个，前后端都检查。
- **L87–L89**：保留用户可见文本；若无文档 ID，尝试修复旧消息中的可见附件名称。
- **L90–L92**：恢复函数按当前用户和可见文本找文档，不全库模糊猜测。

```text
  93 │ 		if resolveErr != nil {
  94 │ 			writeJSON(w, http.StatusUnprocessableEntity, apiError{
  95 │ 				Code:    "attachment_reference_invalid",
  96 │ 				Message: "消息中的附件无法关联到文档库，请重新选择文件后发送",
  97 │ 			})
  98 │ 			return
  99 │ 		}
 100 │ 	}
 101 │ 	documents := make([]document.Document, 0, len(input.DocumentIDs))
 102 │ 	for _, documentID := range input.DocumentIDs {
 103 │ 		item, documentErr := s.documents.Get(r.Context(), auth.User.ID, documentID)
 104 │ 		if documentErr != nil {
 105 │ 			writeDocumentError(w, documentErr)
 106 │ 			return
 107 │ 		}
 108 │ 		documents = append(documents, item)
 109 │ 	}
 110 │ 	if visibleContent == "" && len(input.DocumentIDs) > 0 {
 111 │ 		visibleContent = "请处理这个文件"
 112 │ 	}
```

- **L93–L100**：无法关联旧附件时返回 422，要求重新选择；不会静默继续一个没有来源的文件任务。
- **L101–L103**：逐个用 userID 获取文档；这一步验证资源归属。
- **L104–L109**：任一文档不可访问就终止，否则加入已验证文档列表。
- **L110–L112**：只有附件没有文本时，填充“请处理这个文件”。

```text
 113 │ 	messageContent := visibleContent
 114 │ 	for _, item := range documents {
 115 │ 		messageContent = chatattachment.AppendDocument(messageContent, item.ID, item.Name)
 116 │ 	}
```

- **L113–L116**：以可见文本为基础，追加可信的文档标记，供后续 Agent/工具定位附件。


## 分流、构建上下文并返回 202

源码：[internal/httpserver/conversations.go:117](/Users/windcry1/Documents/ai_companion/internal/httpserver/conversations.go:117)

<a id="symbols-2-3"></a>

### 函数与变量详解

函数/类型声明：[`sendMessage`](/Users/windcry1/Documents/ai_companion/internal/httpserver/conversations.go:60)。

**职责、输入输出与影响：** 同一函数后半段：按角色模块和制品要求分流。Agent 返回 {message, agent_run}，普通路径返回 {message, job}，HTTP 均为 202；产生持久化任务而非同步最终回答。

**调用关系：** conversations.Get/characters.Get → ActiveForConversation → ValidateContent/BuildContext → agentRuns.AcceptChat；未命中 Agent 则 conversations.Send。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `forceArtifactAgent` | bool | requiresDurableArtifactAgent(messageContent) | 标识制品需求；仍与 work 模块等分支条件结合，不能单独跳过校验。 |
| `conversationItem` | conversation.Conversation | 按已认证用户及路径 ID 查询 | 其 CharacterID 确定读取哪个角色。 |
| `persona` | character.Character | characters.Get 返回 | 此处名字叫 persona 但类型是角色对象，不是 character.PersonaVersion。 |
| `activeRun / activeErr` | agent.Run / error | ActiveForConversation | 找到则返回 409；只有 ErrNotFound 表示没有活跃任务。 |
| `snapshot` | contextengine.Snapshot | BuildContext 的返回 | History、Summary、Memories、Knowledge、Manifest；作为运行上下文输入。 |
| `message / run` | conversation.Message / agent.Run | Agent AcceptChat 返回 | message 是已收录的用户发言，run 是后续可恢复执行记录。 |
| `job` | conversation.Job | 普通 Send 返回 | 与 agent.Run 不同的运行协议，前端接 SSE/事件路径。 |
| `acceptErr / historyErr` | error / error | 接受运行 / 构建上下文 | 名字说明失败阶段，便于知道此时是否已经入库。 |

### 源码与逐行解释

```text
 117 │ 	forceArtifactAgent := requiresDurableArtifactAgent(messageContent)
 118 │ 	if len(s.agentChatModules) > 0 || forceArtifactAgent {
 119 │ 		conversationItem, conversationErr := s.conversations.Get(
 120 │ 			r.Context(),
 121 │ 			auth.User.ID,
 122 │ 			r.PathValue("conversation_id"),
 123 │ 		)
 124 │ 		if conversationErr != nil {
 125 │ 			writeConversationError(w, conversationErr)
 126 │ 			return
 127 │ 		}
 128 │ 		persona, characterErr := s.characters.Get(
 129 │ 			r.Context(),
 130 │ 			auth.User.ID,
 131 │ 			conversationItem.CharacterID,
 132 │ 		)
 133 │ 		if characterErr != nil {
 134 │ 			writeConversationError(w, conversation.ErrNotFound)
 135 │ 			return
 136 │ 		}
```

- **L117**：检测文件生成等必须进入持久化 Agent 的行为。
- **L118–L123**：只要配置启用了任一 Agent 模块或本次强制走 Agent，就先取得当前会话。
- **L124–L127**：会话不存在或无权访问时返回错误。
- **L128–L132**：按会话的 CharacterID 读取用户的角色配置。
- **L133–L136**：角色不可访问时按会话未找到处理。

```text
 137 │ 		if s.agentModuleEnabled(persona.Module) || (forceArtifactAgent && persona.Module == "work") {
 138 │ 			if !s.requireQuota(w, r, billing.ResourceAgentRuns) {
 139 │ 				return
 140 │ 			}
 141 │ 			if s.agentRuns == nil {
 142 │ 				writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_unavailable", Message: "当前模块的 Agent 运行时暂不可用"})
 143 │ 				return
 144 │ 			}
 145 │ 			activeRun, activeErr := s.agentRuns.ActiveForConversation(
 146 │ 				r.Context(), auth.User.ID, conversationItem.ID,
 147 │ 			)
 148 │ 			if activeErr == nil {
 149 │ 				writeJSON(w, http.StatusConflict, map[string]any{
 150 │ 					"code": "agent_run_active", "message": "当前会话已有任务正在执行",
 151 │ 					"agent_run": publicAgentRun(activeRun),
 152 │ 				})
 153 │ 				return
 154 │ 			}
```

- **L137**：最终按当前角色模块判断：配置启用该模块，或 work 模块且强制制品 Agent。
- **L138–L140**：此时再检查 Agent Run 月度额度。
- **L141–L144**：已要求 Agent 但服务未装配时返回 503；不会伪装为成功。
- **L145–L147**：查询同一会话已有的活跃运行。
- **L148–L154**：已有运行返回 409 和公开版 Run；前端据此恢复轮询，而不是新建第二个正在处理的输入。

```text
 155 │ 			if !errors.Is(activeErr, agent.ErrNotFound) {
 156 │ 				writeAgentRunError(w, activeErr)
 157 │ 				return
 158 │ 			}
 159 │ 			if validateErr := s.conversations.ValidateContent(messageContent); validateErr != nil {
 160 │ 				writeConversationError(w, validateErr)
 161 │ 				return
 162 │ 			}
 163 │ 			snapshot, historyErr := s.conversations.BuildContext(
 164 │ 				r.Context(),
 165 │ 				auth.User.ID,
 166 │ 				conversationItem.ID,
 167 │ 				messageContent,
 168 │ 			)
 169 │ 			if historyErr != nil {
 170 │ 				writeConversationError(w, historyErr)
 171 │ 				return
 172 │ 			}
```

- **L155–L158**：只有“没有活跃运行”的 ErrNotFound 才允许继续，其他存储错误不能当成空队列。
- **L159–L162**：执行聊天内容长度与安全校验。
- **L163–L168**：调用共用 BuildContext：传当前文本作为 query，历史与当前请求分开保存。
- **L169–L172**：上下文构建失败立即返回。

```text
 173 │ 			message, run, acceptErr := s.agentRuns.AcceptChat(r.Context(), agent.AcceptChatInput{
 174 │ 				UserID: auth.User.ID, ConversationID: conversationItem.ID,
 175 │ 				CharacterID: persona.ID, Module: persona.Module, Content: messageContent,
 176 │ 				Context: map[string]any{
 177 │ 					"timezone":             auth.User.Timezone,
 178 │ 					"system_prompt":        conversation.PersonaSystemPrompt(persona),
 179 │ 					"history":              snapshot.History,
 180 │ 					"conversation_context": snapshot,
 181 │ 					"email_profile": map[string]any{
 182 │ 						"sender_name":      auth.User.DisplayName,
 183 │ 						"default_language": defaultEmailLanguage(auth.User.Locale),
 184 │ 						"profile_version":  "account-email-profile-v1",
 185 │ 					},
 186 │ 				},
 187 │ 			})
 188 │ 			if acceptErr != nil {
 189 │ 				writeAgentRunError(w, acceptErr)
 190 │ 				return
 191 │ 			}
```

- **L173–L175**：交给 Agent 服务，传用户、会话、角色、模块和最终消息正文。
- **L176–L180**：Context 同时包含时区、Persona 系统提示、兼容 history 和新 conversation_context 快照。
- **L181–L187**：邮箱显示名/默认语言形成邮件上下文，随后结束 AcceptChat 调用；这不是发送邮件。
- **L188–L191**：接受阶段错误映射为 HTTP 错误。

```text
 192 │ 			writeJSON(w, http.StatusAccepted, map[string]any{
 193 │ 				"message": message, "agent_run": publicAgentRun(run), "runtime": "agent",
 194 │ 			})
 195 │ 			return
 196 │ 		}
 197 │ 	}
 198 │ 	message, job, err := s.conversations.Send(r.Context(), auth.User.ID, r.PathValue("conversation_id"), messageContent)
 199 │ 	if err != nil {
 200 │ 		writeConversationError(w, err)
 201 │ 		return
 202 │ 	}
 203 │ 	writeJSON(w, http.StatusAccepted, map[string]any{"message": message, "job": job})
 204 │ }
```

- **L192–L195**：返回 202、持久化用户消息和 publicAgentRun。此时任务只被接受，模型未必已经运行。
- **L196–L197**：结束 Agent 分支。
- **L198–L202**：未进入 Agent 的请求走普通 conversation.Send；错误仍统一映射。
- **L203–L204**：普通路径也返回 202，但结果字段是 job 而不是 agent_run。


## Agent 状态怎样恢复到页面

源码：[web/app/chat-panel.tsx:57](/Users/windcry1/Documents/ai_companion/web/app/chat-panel.tsx:57)

<a id="symbols-2-4"></a>

### 函数与变量详解

函数/类型声明：[`consumeAgentRun`](/Users/windcry1/Documents/ai_companion/web/app/chat-panel.tsx:57)。

**职责、输入输出与影响：** 输入 Run ID 和可选 conversationOverride，返回 Promise<void>。轮询、弹审批、提交决策、刷新消息并更新 UI；连接失败退出观察不自动取消服务器任务。

**调用关系：** send、初次恢复、重试调用；GET Run → waiting_approval 时 POST resolve → 终态时 loadMessages/mergeMessages。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `id` | string | 调用方传入 | 固定要观察的 Run，不是 conversationID。 |
| `conversationOverride / targetConversationID` | string / string | 显式恢复参数 / override 或当前 state | 避免刚 setConversationID 后闭包仍读旧值。 |
| `failures` | number | 开始 0 | 一次正常读取后归零，异常后递增，第 5 次退出；不是任务已执行次数。 |
| `run` | AgentRun 加可选 message | GET JSON | status/revision 驱动观察；不因前端类型断言自动可信。 |
| `interrupt` | AgentInterrupt 或 undefined | run.output?.interrupts?.[0] | 审批展示内容，缺失时不能凭空构造确认。 |
| `resolutionKey` | string | Run ID 与 revision 拼接 | 本次逻辑确认的键；handledAgentResolutionsRef.current 保存已处理键。 |
| `approved / resolved` | boolean / Response | window.confirm / POST resolve 返回 | false 也是合法拒绝；resolved.ok 说明提交响应而非业务最终完成。 |
| `latest` | Message[] | 终态时 loadMessages | 以服务器已持久化消息刷新 UI。 |
| `handledAgentResolutionsRef` | React Ref，内部 Set<string> | 组件初始化 | 去重提示，不是服务端幂等数据库，刷新页面可丢失。 |

### 源码与逐行解释

```text
  57 │   async function consumeAgentRun(id:string,conversationOverride=""){let failures=0;const targetConversationID=conversationOverride||conversationID;
  58 │     while(true){try{const response=await fetch(`${apiBase}/v1/agent-runs/${id}`,{headers:{Authorization:`Bearer ${token}`}});const run=await response.json() as AgentRun&{message?:string};if(!response.ok)throw new Error(run.message??"无法读取 Agent 状态");failures=0;
  59 │       if(run.status==="waiting_approval"){const interrupt=run.output?.interrupts?.[0];if(!interrupt)throw new Error("确认信息不完整，请稍后重试");const resolutionKey=`${id}:${run.revision}`;if(!handledAgentResolutionsRef.current.has(resolutionKey)){handledAgentResolutionsRef.current.add(resolutionKey);const approved=window.confirm(confirmationPrompt(interrupt.summary));const resolved=await fetch(`${apiBase}/v1/agent-runs/${id}/resolve`,{method:"POST",headers:{"Content-Type":"application/json","Idempotency-Key":resolutionKey,Authorization:`Bearer ${token}`},body:JSON.stringify({approved})});if(!resolved.ok){handledAgentResolutionsRef.current.delete(resolutionKey);const body=await resolved.json().catch(()=>({message:"提交确认失败"})) as {message?:string};throw new Error(body.message??"提交确认失败")}setToolNotice(approved?"已确认，正在执行更新…":"已取消，本次更新不会写入。")}setStatus("正在恢复任务…")}
  60 │       else if(run.status==="completed"){const latest=await loadMessages(token,targetConversationID);setMessages(current=>mergeMessages(current,latest));setStatus("");setAgentRunID("");setToolNotice(executionResultNotice(run.output?.tool_result?.response));return}
  61 │       else if(run.status==="cancelled"){const latest=await loadMessages(token,targetConversationID);setMessages(current=>mergeMessages(current,latest));setStatus("已停止生成");setAgentRunID("");setToolNotice("操作已取消，原对话已保留。");return}
  62 │       else if(run.status==="timed_out"){const latest=await loadMessages(token,targetConversationID);setMessages(current=>mergeMessages(current,latest));setError("这次处理超时，原对话已保留，可在对应消息下重试。");setStatus("");setAgentRunID("");return}
  63 │       else if(run.status==="failed"){const latest=await loadMessages(token,targetConversationID);setMessages(current=>mergeMessages(current,latest));setError("这次处理没有完成，原对话已保留，可在对应消息下重试。");setStatus("");setAgentRunID("");return}
  64 │       else{setStatus(run.status==="queued"?"任务已进入恢复队列…":run.status==="cancel_requested"?"正在停止…":"正在回应…")}
  65 │       await delay(400);
  66 │     }catch(cause){failures++;if(failures>=5){setAgentRunID("");throw cause}setStatus("连接暂时中断，正在自动恢复…");await delay(Math.min(400*failures,1600))}}
  67 │   }
```

- **L57**：允许调用方显式传 conversationOverride；页面刚恢复会话时 React state 可能尚未提交，不能只依赖旧闭包里的 conversationID。
- **L58**：持续 GET Agent Run；错误响应抛异常，成功读取则重置网络失败计数。
- **L59**：遇到 waiting_approval，取待确认请求，以 Run ID + revision 去重弹框；提交 approved 和 Idempotency-Key。提交失败撤回本地去重标记，之后可以再试；拒绝也作为合法决策发给服务端。
- **L60**：completed 时重新从数据库消息接口加载最终回答、合并去重、解除输入锁，显示工具结果提示。
- **L61**：cancelled 同样刷新消息并清除活跃 Run；保留历史对话。
- **L62**：timed_out 显示可重试提示，清除活跃 ID；原运行和原消息仍保留。
- **L63**：failed 与超时分别提示，用户可在原消息下重试。
- **L64**：未终结状态只更新等待文案，不把 queued 或 cancel_requested 当作完成。
- **L65**：健康状态每隔 400 毫秒再次读取；这是轮询，不是 token 流。
- **L66–L67**：网络错误最多连续容忍 4 次，退避 400–1600ms；第 5 次清除当前活跃指示并抛出。服务端运行不会因为浏览器断线自动消失。


## 把代码连成一次真实行为

### 将源码中挤在一行的动作重新分成时间顺序

假设输入框是“分析这份报告”，附件为 D1，且没有活跃任务。下面是阅读用等价顺序，不是另一套实现：取得输入 → 发送 JSON → 收到响应 → 解析 JSON → 判断状态 → 清理输入 → 合并确认消息 → 观察后续运行。

| 响应/阶段 | 页面做什么 | 服务端任务含义 |
|---|---|---|
| 请求发出，尚未返回 | 显示等待 | 不能假设已经入库 |
| 409 且带活跃 Agent | 恢复观察该 Run | 本次没有创建第二个新 Run；存在旧任务 |
| 202 + message + agent_run | 接收用户消息，轮询 Run | 接受成功，结果未完成 |
| 202 + message + job | 观察普通生成任务的事件 | 普通生成路径，不是 Agent 轮询 |
| waiting_approval | 同一 Run/revision 只提示一次，提交批准或拒绝 | 需要用户决定，不是执行崩溃 |
| completed | 再加载数据库消息并合并 | 以服务端已保存消息展示最终回答 |
| 连续网络失败到上限 | 报连接问题，退出当前观察 | 不能据此断言后台 Run 已 failed |

为什么成功后才 form.reset？这样服务端拒绝请求时用户仍有原输入。为什么最终要再 loadMessages？因为轮询返回的是运行状态，不等同于聊天消息列表完整内容。为什么 merge 按 ID 而非文本？用户可连续发两句相同的话，文本相同不代表同一条消息。

### 一个最容易误读的分支

catch 中显示错误，并不表示自动重新发送 POST。假如服务器提交成功后网络断开，盲目重发可能新建另一个消息。AcceptChat 的内部幂等键来自每次新建的 Message ID，不能推导成“整个发送接口支持任意客户端重试去重”。先查询活跃 Run/消息，才知道服务器实际状态。

### 自测与答案

问：按钮禁用了，为什么服务器还要查活跃任务？答：多标签页、重放请求和网络重试都可能绕过当前页面状态，服务端必须维护自己的约束。

问：HTTP 202 的中文应该理解成“成功生成”还是“已接受处理”？答：后者。还要观察对应 Run/Job 的终态；相同数字不能替代业务字段。

## 对照阅读与验证

观察输入/输出结构比只看函数名更有用：普通返回 {message, job}，Agent 返回 {message, agent_run}。页面加载时 ensureConversation → loadMessages → loadActiveAgentRun；历史通过 sequence+bubble 游标分页，mergeMessage 依据 ID 去重。相应函数位于 chat-panel.tsx 文件尾。

[返回目录](README.md) · [下一章：Agent 入库、领取与完成事务](03-agent-transactions.md)
