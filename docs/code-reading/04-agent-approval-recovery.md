# 04 工具审批、恢复、取消与重试

[阅读总入口](README.md)

行为例子：生活助手识别一笔账单，等待用户确认，再写入账本；用户也可能在等待模型时点击停止。Go 保存执行权限和业务结果，Python 使用检查点选择下一节点。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 模型的建议不等于业务授权

模型可以提出“调用某工具、参数是这些”，但最终是否允许调用由 Go 网关检查运行身份、有效租约、模块工具范围等条件。对需要确认的写入，Prepare 产生候选或确认信息，Commit 才执行相应写入。部分只读或安全低风险动作在 Prepare 就会执行，所以不能把 Prepare 一概理解为“什么也没做”。

interrupt 是把“此处需要外部输入”保存到可恢复执行状态中，不是让一个网页弹框把服务器线程一直锁在那里。审批结果持久化后，新的执行尝试可通过同一 Run 的检查点继续。

HTTP 中 approved 使用 *bool：缺失为 nil，明确拒绝为指向 false，批准为指向 true。拒绝是完整合法的决策，不能写成 if !approved 就当参数缺失。第 12 章 Go 实验可亲自观察这三个值。

## 每次工具调用重新验证执行权

源码：[internal/agent/tool_gateway.go:331](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:331)

<a id="symbols-4-1"></a>

### 函数与变量详解

函数/类型声明：[`loadRequest`](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:331)。

**职责、输入输出与影响：** 输入 runID、expectedRevision，返回 (当前 Run, conversation.ToolRequest, error)。查数据库、校验运行状态/版本/期限并还原可信工具上下文；此函数本身不提交工具副作用。

**调用关系：** Prepare、Commit、Definitions 等网关入口共用；runs.Get → Unmarshal Run.Input → 过滤历史 → 组装 ToolRequest。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `g` | *ToolGateway | 方法接收者 | 持有 runs/tools/secret/now 等依赖；g=nil 或依赖缺失则拒绝。 |
| `run / expectedRevision` | Run / int | 数据库当前记录 / 调用方领取版本 | 两者 Revision 必须一致且状态 running；不能只信任 Python 传来的用户身份。 |
| `input` | agentRunInput | Run.Input 解码 | MessageID/Text/Context.History 是已接受输入，不是 HTTP 审批体。 |
| `history` | []conversation.Message | 过滤后的历史 | 只留 user/assistant 非空正文，再取最近最多 20 条。 |
| `now` | time.Time | g.now().UTC() | 检查 LeaseExpiresAt 与 DeadlineAt，不是模型上下文时间。 |
| `runID` | string | 去空白后的调用参数 | 用于读取权威运行记录，ToolRequest 的 UserID/Module 从 run 取。 |

### 源码与逐行解释

```text
 331 │ func (g *ToolGateway) loadRequest(ctx context.Context, runID string, expectedRevision int) (Run, conversation.ToolRequest, error) {
 332 │ 	if g == nil || g.runs == nil || g.tools == nil || len(g.secret) < 16 {
 333 │ 		return Run{}, conversation.ToolRequest{}, ErrValidation
 334 │ 	}
 335 │ 	runID = strings.TrimSpace(runID)
 336 │ 	if runID == "" {
 337 │ 		return Run{}, conversation.ToolRequest{}, ErrValidation
 338 │ 	}
 339 │ 	run, err := g.runs.Get(ctx, runID)
 340 │ 	if err != nil {
 341 │ 		return Run{}, conversation.ToolRequest{}, err
 342 │ 	}
 343 │ 	now := g.now().UTC()
 344 │ 	if run.Status != "running" || expectedRevision <= 0 || run.Revision != expectedRevision ||
 345 │ 		run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) ||
 346 │ 		run.DeadlineAt.IsZero() || !run.DeadlineAt.After(now) {
 347 │ 		return Run{}, conversation.ToolRequest{}, ErrConflict
 348 │ 	}
```

- **L331–L334**：拒绝未装配依赖或确认密钥过短的网关。
- **L335–L338**：规范化并验证 Run ID。
- **L339–L342**：从权威 Store 重新取 Run，不接受 Python 自报的用户或状态作为依据。
- **L343–L348**：同时检查 running、预期 revision、有效租约和总 deadline；任何一项不符返回冲突。

```text
 349 │ 	var input agentRunInput
 350 │ 	if err = json.Unmarshal(run.Input, &input); err != nil {
 351 │ 		return Run{}, conversation.ToolRequest{}, fmt.Errorf("%w: malformed agent run input", ErrValidation)
 352 │ 	}
 353 │ 	input.MessageID = strings.TrimSpace(input.MessageID)
 354 │ 	input.Text = strings.TrimSpace(input.Text)
 355 │ 	if input.MessageID == "" || input.Text == "" {
 356 │ 		return Run{}, conversation.ToolRequest{}, fmt.Errorf("%w: message_id and text are required", ErrValidation)
 357 │ 	}
 358 │ 	history := make([]conversation.Message, 0, len(input.Context.History))
 359 │ 	for _, message := range input.Context.History {
 360 │ 		message.Content = strings.TrimSpace(message.Content)
 361 │ 		if (message.Role == "user" || message.Role == "assistant") && message.Content != "" {
 362 │ 			history = append(history, conversation.Message{Role: message.Role, Content: message.Content})
 363 │ 		}
 364 │ 	}
 365 │ 	if len(history) > 20 {
 366 │ 		history = history[len(history)-20:]
 367 │ 	}
```

- **L349–L352**：解析持久化 Run.Input，畸形 JSON 属于输入契约错误。
- **L353–L357**：要求有 Message ID 和文本，防止工具失去来源。
- **L358–L364**：只留下非空 user/assistant 历史；过滤其他角色，避免来源数据冒充高权限消息。
- **L365–L367**：工具网关的兼容 History 只保留最近 20 条；这与模型层统一 Token 预算快照是不同用途。

```text
 368 │ 	return run, conversation.ToolRequest{
 369 │ 		UserID: run.UserID, MessageID: input.MessageID, JobID: run.ID,
 370 │ 		ConversationID: run.ConversationID, CharacterID: run.CharacterID,
 371 │ 		Module: run.Module, Text: input.Text, History: history,
 372 │ 	}, nil
 373 │ }
```

- **L368–L373**：返回 Run 和可信 ToolRequest，其中用户、会话、角色和模块均来自数据库。


## Prepare：执行只读/低风险步骤或产生确认

源码：[internal/agent/tool_gateway.go:136](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:136)

<a id="symbols-4-2"></a>

### 函数与变量详解

函数/类型声明：[`Prepare`](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:136)。

**职责、输入输出与影响：** 输入 ToolPrepareInput，返回 (ToolPreparation, error)。可能执行只读/低风险工具、启动任务，或形成待确认动作；Status=completed 只描述准备阶段。

**调用关系：** 图的工具预检链进入 Go 网关；loadRequest → ModelTools 白名单 → ExecuteModelTool → prepareConfirmation/公开结果。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | ToolPrepareInput | 工具调用提案 | CallKey 是动作键，ToolName 是工具名，Arguments 是参数 map，ExpectedRevision 来自可信运行边界。 |
| `run / request` | Run / conversation.ToolRequest | loadRequest | run 提供身份，request 是传给领域工具的上下文；两者不是同一类型。 |
| `request.JobID` | string | 先为 Run ID，随后设 input.CallKey | 此处以单动作身份作为下游任务幂等边界，一个 Run 可有多个动作。 |
| `result` | conversation.ToolResult | ExecuteModelTool 返回 | Response 是说明，Data 是业务结果，Confirmation 非 nil 表示需确认。 |
| `skillRun / ok` | skill.Run / bool | skillRunAwaitingConfirmation(result.Data) | 检查已创建的 Skill 是否仍在等待确认，而非所有 Data 都是 Skill。 |
| `response` | string | result.Response 去空白 | 空时用兜底提示；不应拿此提示替代正式工具状态。 |

### 源码与逐行解释

```text
 136 │ func (g *ToolGateway) Prepare(ctx context.Context, runID string, input ToolPrepareInput) (ToolPreparation, error) {
 137 │ 	run, request, err := g.loadRequest(ctx, runID, input.ExpectedRevision)
 138 │ 	if err != nil {
 139 │ 		return ToolPreparation{}, err
 140 │ 	}
 141 │ 	input.CallKey = strings.TrimSpace(input.CallKey)
 142 │ 	input.ToolName = strings.TrimSpace(input.ToolName)
 143 │ 	if input.CallKey == "" || len(input.CallKey) > 128 || input.ToolName == "" || len(input.ToolName) > 128 {
 144 │ 		return ToolPreparation{}, ErrValidation
 145 │ 	}
 146 │ 	if !toolInDefinitions(input.ToolName, g.tools.ModelTools(request)) {
 147 │ 		return ToolPreparation{}, fmt.Errorf("%w: %s is unavailable for module %s", ErrToolDenied, input.ToolName, run.Module)
 148 │ 	}
 149 │ 	if input.Arguments == nil {
 150 │ 		input.Arguments = map[string]any{}
 151 │ 	}
 152 │ 	// A LangGraph action is the idempotency boundary for an independent
 153 │ 	// persisted task. Replaying the same action reuses its Skill Run, while a
 154 │ 	// second attachment action receives its own queue lease and retry state.
 155 │ 	request.JobID = input.CallKey
```

- **L136–L140**：先做上一节的执行权检查。
- **L141–L145**：清理 CallKey/ToolName 并限制长度；CallKey 标识图内具体动作。
- **L146–L148**：工具名必须属于 Go 为当前模块返回的 ModelTools；模型无法自行创造工具权限。
- **L149–L151**：缺失参数规范化为空 map。
- **L152–L155**：把 action CallKey 用作本次业务任务幂等边界；同一动作重放复用 Skill，不同附件动作可创建不同任务。

```text
 156 │ 	result, err := g.tools.ExecuteModelTool(ctx, request, conversation.ModelToolCall{
 157 │ 		Name: input.ToolName, Arguments: input.Arguments,
 158 │ 	})
 159 │ 	if err != nil {
 160 │ 		return ToolPreparation{}, err
 161 │ 	}
 162 │ 	if result.Confirmation == nil {
 163 │ 		if skillRun, ok := skillRunAwaitingConfirmation(result.Data); ok {
 164 │ 			return g.prepareConfirmation(run, input, result, conversation.ToolConfirmation{
 165 │ 				Kind: "skill_run", CandidateID: skillRun.ID,
 166 │ 				Summary: fmt.Sprintf("工具：%s\n风险：%s\n操作：%s", skillRun.SkillName, skillRun.RiskLevel, result.Response),
 167 │ 			}, skillRun.RiskLevel)
 168 │ 		}
 169 │ 		response := strings.TrimSpace(result.Response)
 170 │ 		if response == "" {
 171 │ 			response = "当前请求无需调用项目工具。"
 172 │ 		}
```

- **L156–L161**：调用真正的业务 Tool Executor。Prepare 并非绝对只读：可读数据、创建候选、启动允许的低风险任务；失败立即返回。
- **L162–L168**：如果没有通用 Confirmation，但返回的是等待确认 Skill，包装一个 skill_run 审批。
- **L169–L172**：未要求审批时，保证 Response 非空。

```text
 173 │ 		return ToolPreparation{
 174 │ 			Status: "completed", ToolName: input.ToolName, Response: response,
 175 │ 			RiskLevel: "none", NormalizedArguments: cloneMap(input.Arguments), Data: publicToolData(result.Data),
 176 │ 		}, nil
 177 │ 	}
 178 │ 	return g.prepareConfirmation(run, input, result, *result.Confirmation, "medium")
 179 │ }
```

- **L173–L177**：返回 Preparation completed；这表示准备调用已产出结果，Data 中的异步 Skill 仍可能 queued/running。
- **L178–L179**：其他需确认结果签名为中风险审批请求。


## approval 节点：暂停并选择分支

源码：[workers/python/src/ai_companion_worker/agent_runtime.py:6021](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:6021)

<a id="symbols-4-3"></a>

### 函数与变量详解

函数/类型声明：[`approval`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:6021)。

**职责、输入输出与影响：** 输入 AgentState，返回跳往 commit_tool/reject_tool 的 Command。interrupt 先持久化等待；恢复后验证 resolution.approved 确为 bool，再产生状态更新，不直接记账。

**调用关系：** LangGraph 进入 approval 节点；interrupt 等外部结果；_updated_recovery/_trace_event 构造恢复诊断；Command 指定下一节点。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `state` | AgentState，字典形状态 | 图引擎传入 | 含 run_id、preparation、model_manifest、steps 等，不是数据库事务。 |
| `preparation` | 工具准备结果字典 | state["preparation"] | 提供 tool_name/risk_level/summary/normalized_arguments/confirmation_token。 |
| `resolution` | 运行时先验为未知对象，验证后 dict | interrupt 恢复返回 | 必须含真正 bool 的 approved；字符串 "false" 不合格。 |
| `update` | dict | 本节点局部构造 | 保存 approval、副恢复计数、node_trace 和 steps+1，再交图合并。 |
| `steps` | state 中 int 计数 | state.get("steps", 0) | 本节点执行后加一，不是网络重试次数。 |

### 源码与逐行解释

```text
6021 │     def approval(
6022 │         state: AgentState,
6023 │     ) -> Command[Literal["commit_tool", "reject_tool"]]:
6024 │         preparation = state["preparation"]
6025 │         resolution = interrupt(
6026 │             {
6027 │                 "type": "tool_approval",
6028 │                 "graph_name": GRAPH_NAME,
6029 │                 "graph_version": GRAPH_VERSION,
6030 │                 "model_config_version": model_config_version(state.get("model_manifest", {})),
6031 │                 "model_manifest_fingerprint": state.get("model_manifest", {}).get(
6032 │                     "fingerprint", ""
6033 │                 ),
6034 │                 "tool_catalog_fingerprint": state.get("tool_catalog_fingerprint", ""),
```

- **L6021–L6024**：声明只可能跳转 commit_tool/reject_tool 的节点，并读取已准备好的工具动作。
- **L6025–L6029**：interrupt 产生 tool_approval 中断，携带 Graph 身份；图检查点保存后把控制权交回 Worker。
- **L6030–L6034**：记录模型和工具目录指纹，恢复时校验运行上下文没有被替换。

```text
6035 │                 "run_id": state["run_id"],
6036 │                 "tool_name": preparation["tool_name"],
6037 │                 "risk_level": preparation["risk_level"],
6038 │                 "summary": preparation["summary"],
6039 │                 "arguments": preparation["normalized_arguments"],
6040 │                 "confirmation_token": preparation["confirmation_token"],
6041 │             }
6042 │         )
6043 │         if not isinstance(resolution, dict) or not isinstance(resolution.get("approved"), bool):
6044 │             raise ValueError("approval resolution must contain approved:boolean")
6045 │         update = {
6046 │             "approval": dict(resolution),
6047 │             "recovery": _updated_recovery(
6048 │                 state,
6049 │                 counter="approval_resumes",
6050 │                 interrupt_type="tool_approval",
6051 │             ),
```

- **L6035–L6042**：审批数据携带 Run、工具、风险、摘要、参数和服务端确认令牌。浏览器可见版本还要经 publicAgentRun 脱敏，不能把这个内部 payload 原样当公开接口。
- **L6043–L6044**：恢复时严格要求 approved 是布尔值，字符串 true 不被接受。
- **L6045–L6051**：把审批决策和恢复计数写入下一状态。

```text
6052 │             "node_trace": [
6053 │                 *state.get("node_trace", []),
6054 │                 _trace_event(
6055 │                     "approval",
6056 │                     "resumed",
6057 │                     details={"approved": resolution["approved"]},
6058 │                 ),
6059 │             ],
6060 │             "steps": state.get("steps", 0) + 1,
6061 │         }
6062 │         if resolution["approved"]:
6063 │             return Command(update=update, goto="commit_tool")
6064 │         return Command(update=update, goto="reject_tool")
```

- **L6052–L6061**：追加 resumed 节点轨迹，并递增已执行步骤数。
- **L6062–L6064**：批准返回 Command 跳 commit_tool；拒绝跳 reject_tool。拒绝是正常业务分支，不是网络异常。


## 浏览器确认怎样送入持久化恢复

源码：[internal/httpserver/agent_runs.go:50](/Users/windcry1/Documents/ai_companion/internal/httpserver/agent_runs.go:50)

<a id="symbols-4-4"></a>

### 函数与变量详解

函数/类型声明：[`resolveAgentRun`](/Users/windcry1/Documents/ai_companion/internal/httpserver/agent_runs.go:50)。

**职责、输入输出与影响：** HTTP 审批入口，无直接 Go 返回值。读取 approved 三态、当前用户、Run 路径与幂等键，调用 ResolveApproval；新决策 HTTP 202，幂等复用 HTTP 200。

**调用关系：** 浏览器 consumeAgentRun POST resolve → ResolveApproval → writeJSON(publicAgentRun)。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | 匿名结构，Approved *bool | JSON 请求体 | nil=未提供/null，指向 false=拒绝，指向 true=批准。 |
| `item` | agent.Run | ResolveApproval 返回 | 服务端已处理审批后的运行记录，不是待确认账目。 |
| `changed` | bool | ResolveApproval 第二返回值 | 表示本次是否发生新决策变化；不是用户是否批准。 |
| `status` | int | 默认 StatusOK=200 | changed=true 才变为 StatusAccepted=202。 |
| `r / w` | *http.Request / http.ResponseWriter | HTTP 框架 | r 提供 run_id/Idempotency-Key；w 写公开响应。 |
| `err` | error | ResolveApproval 第三返回值 | 错误时先返回，不能用 changed 推断失败/成功。 |

### 源码与逐行解释

```text
  50 │ func (s *Server) resolveAgentRun(w http.ResponseWriter, r *http.Request) {
  51 │ 	if s.agentRuns == nil {
  52 │ 		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_unavailable", Message: "Agent 运行时暂不可用"})
  53 │ 		return
  54 │ 	}
  55 │ 	var input struct {
  56 │ 		Approved *bool `json:"approved"`
  57 │ 	}
  58 │ 	if !decodeJSON(w, r, &input) {
  59 │ 		return
  60 │ 	}
  61 │ 	if input.Approved == nil {
  62 │ 		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "approved 必须是布尔值"})
  63 │ 		return
  64 │ 	}
```

- **L50–L54**：Agent 服务不可用返回 503。
- **L55–L57**：Approved 用 *bool，因此可区分没有字段和明确 false。
- **L58–L64**：解析并拒绝缺失字段，避免默认 false 掩盖客户端请求错误。

```text
  65 │ 	item, changed, err := s.agentRuns.ResolveApproval(
  66 │ 		r.Context(),
  67 │ 		currentAuth(r).User.ID,
  68 │ 		r.PathValue("run_id"),
  69 │ 		*input.Approved,
  70 │ 		r.Header.Get("Idempotency-Key"),
  71 │ 	)
  72 │ 	if err != nil {
  73 │ 		writeAgentRunError(w, err)
  74 │ 		return
  75 │ 	}
  76 │ 	status := http.StatusOK
  77 │ 	if changed {
  78 │ 		status = http.StatusAccepted
  79 │ 	}
  80 │ 	writeJSON(w, status, map[string]any{"run": publicAgentRun(item), "deduplicated": !changed})
  81 │ }
```

- **L65–L71**：把用户 ID、Run ID、决策和 Idempotency-Key 交给 ResolveApproval；不信任请求体自行指定用户。
- **L72–L75**：错误统一映射。
- **L76–L80**：changed=true 用 202 表示已重新排队；重复相同决策用 200 和 deduplicated=true。
- **L81**：Handler 结束，不直接同步运行 Python。


继续读 postgresstore/agent.go 的 ResolveAgentRun（L434）：锁 Run → 检查已解析 resolutionKey → 锁 pending interrupt → 保存 approved → Run 变 queued、revision+1 → 写事件及恢复 Outbox → Commit。审批结果和重新派发在同一事务中。

## 批准之后真正提交账本写入

源码：[internal/agent/tool_gateway.go:192](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:192)

<a id="symbols-4-5"></a>

### 函数与变量详解

函数/类型声明：[`Commit`](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:192)。

**职责、输入输出与影响：** 输入 ToolCommitInput，返回 (ToolOutcome, error)。必须批准且确认令牌合法，按 claims.Kind 调用具体领域确认；本摘录展示 ledger 分支，它会产生正式账目。

**调用关系：** 恢复后的 commit_tool → Go Commit → loadRequest/verifyToken → ledger.Confirm；其他 Kind 在同函数后续分支处理。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | ToolCommitInput | 恢复路径传入 | CallKey、ConfirmationToken、Approved、ExpectedRevision；不是前端只含 approved 的原始 HTTP 体。 |
| `run` | Run | loadRequest | 再次验证版本与期限，批准不等于永久执行权。 |
| `claims` | confirmationClaims | verifyToken | 含 RunID/UserID/Kind/CandidateID 等签名绑定信息，防止随意替换候选。 |
| `key` | string | "agent:" + run.ID + ":" + input.CallKey | 下游领域确认的幂等键，连接运行与动作身份。 |
| `item` | ledger.Entry（本分支） | g.ledger.Confirm | 正式业务记录，放进 ToolOutcome.Data。 |
| `confirmErr` | error | 领域 Confirm 返回 | 保存失败时不能返回“已写入”提示。 |

### 源码与逐行解释

```text
 192 │ func (g *ToolGateway) Commit(ctx context.Context, runID string, input ToolCommitInput) (ToolOutcome, error) {
 193 │ 	run, _, err := g.loadRequest(ctx, runID, input.ExpectedRevision)
 194 │ 	if err != nil {
 195 │ 		return ToolOutcome{}, err
 196 │ 	}
 197 │ 	input.CallKey = strings.TrimSpace(input.CallKey)
 198 │ 	input.ConfirmationToken = strings.TrimSpace(input.ConfirmationToken)
 199 │ 	if input.CallKey == "" || len(input.CallKey) > 128 || input.ConfirmationToken == "" {
 200 │ 		return ToolOutcome{}, ErrValidation
 201 │ 	}
 202 │ 	if !input.Approved {
 203 │ 		return ToolOutcome{}, ErrConfirmationState
 204 │ 	}
 205 │ 	claims, err := g.verifyToken(input.ConfirmationToken)
 206 │ 	if err != nil {
 207 │ 		return ToolOutcome{}, err
 208 │ 	}
 209 │ 	if claims.RunID != run.ID || claims.UserID != run.UserID {
 210 │ 		return ToolOutcome{}, ErrInvalidToken
 211 │ 	}
```

- **L192–L196**：Commit 仍需当前有效执行权，审批本身不能绕过租约/revision。
- **L197–L201**：校验动作键和确认 Token 是否存在且长度合规。
- **L202–L204**：必须是 Approved=true；拒绝分支不允许执行 Commit。
- **L205–L208**：验签与检查 Token；不信任客户端描述的待写入内容。
- **L209–L211**：Token 必须绑定当前 Run 和用户。

```text
 212 │ 	key := "agent:" + run.ID + ":" + input.CallKey
 213 │ 	switch claims.Kind {
 214 │ 	case "ledger":
 215 │ 		item, _, confirmErr := g.ledger.Confirm(ctx, run.UserID, claims.CandidateID, key, "由 Agent 确认写入")
 216 │ 		if confirmErr != nil {
 217 │ 			return ToolOutcome{}, confirmErr
 218 │ 		}
 219 │ 		return ToolOutcome{Response: "已写入生活账本。", Data: item}, nil
```

- **L212**：构造业务幂等键 agent:runID:callKey。
- **L213–L215**：按签名 claims.Kind 分发；ledger 使用已签名 Candidate ID 调用领域 Confirm。
- **L216–L219**：只有领域确认成功才返回“已写入生活账本”；模型自由文本不能代替这个结果。


## 执行期间同时响应控制信号

源码：[internal/agent/worker.go:200](/Users/windcry1/Documents/ai_companion/internal/agent/worker.go:200)

<a id="symbols-4-6"></a>

### 函数与变量详解

函数/类型声明：[`executeClaimed`](/Users/windcry1/Documents/ai_companion/internal/agent/worker.go:200)。

**职责、输入输出与影响：** 输入已领取 Run，返回 error。启动执行 goroutine，同时观察结果、控制状态、deadline、取消；可能触发终态写入，不只是在内存里等结果。

**调用关系：** RuntimeWorker 领取后进入；suspendPendingTool → executor.Execute；结果交 finishExecution，控制变化交 FinalizeCancellation/Timeout。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `w` | *RuntimeWorker | 后台执行者实例 | 包含 service、executor、owner 与轮询间隔；w 不再是 HTTP ResponseWriter。 |
| `run / latest` | Run / Run | 最初领取快照 / 后续重新读取 | 比较版本与 owner 识别接管；latest 不是自动覆盖 run 的同一变量。 |
| `execCtx / cancelExec` | context.Context / CancelFunc | WithCancel(ctx) | 传递给执行器，失权/超时等路径触发取消。 |
| `execution` | 函数内结构类型；select 内也有同名局部变量 | 包含 result ExecutionResult、err error | 一次执行既有协议结果又可能有错误，二者一起经通道交回。 |
| `resultCh` | chan execution，容量 1 | make | 允许主协程先退出时发送者仍发送一次结果。 |
| `controlInterval / ticker` | time.Duration / *time.Ticker | 配置或默认 1 秒 | 周期查服务器状态，不是模型重试间隔。 |
| `deadlineTimer / deadline` | *time.Timer / <-chan time.Time | 存在 DeadlineAt 才建立 | deadline 初始 nil 时对应 select 分支不会就绪。 |
| `until` | time.Duration | time.Until(run.DeadlineAt) | 负值钳到 0，表示已到期应尽快处理。 |

### 源码与逐行解释

```text
 200 │ func (w *RuntimeWorker) executeClaimed(ctx context.Context, run Run) error {
 201 │ 	if suspended, err := w.suspendPendingTool(ctx, run); suspended || err != nil {
 202 │ 		return err
 203 │ 	}
 204 │ 	execCtx, cancelExec := context.WithCancel(ctx)
 205 │ 	defer cancelExec()
 206 │ 	type execution struct {
 207 │ 		result ExecutionResult
 208 │ 		err    error
 209 │ 	}
 210 │ 	resultCh := make(chan execution, 1)
 211 │ 	go func() {
 212 │ 		result, err := w.executor.Execute(execCtx, run)
 213 │ 		resultCh <- execution{result: result, err: err}
 214 │ 	}()
```

- **L200–L203**：若所等工具仍未就绪，先持久化挂起并返回，避免无意义启动 Python。
- **L204–L205**：派生可取消执行 Context，并保证离开函数时取消。
- **L206–L210**：把结果与错误组合成结构，使用容量为 1 的 channel；控制分支先退出后，执行 goroutine 仍可放下结果，不会卡在发送上。
- **L211–L214**：独立 goroutine 调用 Python executor，主循环同时处理取消/超时。

```text
 215 │
 216 │ 	controlInterval := w.controlPollInterval
 217 │ 	if controlInterval <= 0 {
 218 │ 		controlInterval = time.Second
 219 │ 	}
 220 │ 	ticker := time.NewTicker(controlInterval)
 221 │ 	defer ticker.Stop()
 222 │ 	var deadlineTimer *time.Timer
 223 │ 	var deadline <-chan time.Time
 224 │ 	if !run.DeadlineAt.IsZero() {
 225 │ 		until := time.Until(run.DeadlineAt)
 226 │ 		if until < 0 {
 227 │ 			until = 0
 228 │ 		}
 229 │ 		deadlineTimer = time.NewTimer(until)
 230 │ 		deadline = deadlineTimer.C
 231 │ 		defer deadlineTimer.Stop()
 232 │ 	}
```

- **L215–L221**：建立控制轮询 ticker，未配置默认 1 秒；退出时停 ticker。
- **L222–L224**：deadline channel 初始 nil；nil channel 对应的 select 分支不会触发。
- **L225–L232**：总截止时间换成 Timer；已经到期则设 0，马上触发，并保证释放 Timer。

```text
 233 │
 234 │ 	for {
 235 │ 		select {
 236 │ 		case execution := <-resultCh:
 237 │ 			return w.finishExecution(ctx, run, execution.result, execution.err)
 238 │ 		case <-ticker.C:
 239 │ 			latest, err := w.service.Get(ctx, run.ID)
 240 │ 			if err != nil {
 241 │ 				if errors.Is(err, ErrNotFound) {
 242 │ 					cancelExec()
 243 │ 					return nil
 244 │ 				}
 245 │ 				return err
 246 │ 			}
```

- **L233–L237**：select 收到执行结果就交 finishExecution；提交前那里会再次读取状态。
- **L238–L246**：每次 ticker 读取数据库；Run 已删除则取消执行，其他读取错误返回。

```text
 247 │ 			switch latest.Status {
 248 │ 			case "cancel_requested":
 249 │ 				cancelExec()
 250 │ 				_, err = w.service.FinalizeCancellation(
 251 │ 					context.Background(), latest.ID, w.owner, latest.Revision,
 252 │ 				)
 253 │ 				return err
 254 │ 			case "cancelled", "timed_out":
 255 │ 				cancelExec()
 256 │ 				return nil
 257 │ 			case "running":
 258 │ 				if latest.Revision != run.Revision || latest.LeaseOwner != w.owner {
 259 │ 					cancelExec()
 260 │ 					return nil
 261 │ 				}
 262 │ 			default:
 263 │ 				cancelExec()
 264 │ 				return nil
 265 │ 			}
```

- **L247–L253**：看到 cancel_requested 就取消 Python，并用独立 Background Context 持久化 cancelled，避免沿用已取消执行 Context。
- **L254–L256**：数据库已 cancelled/timed_out 时停止本地执行，不重复提交。
- **L257–L261**：即使还是 running，只要 revision 或 owner 已变化，也说明执行权失效。
- **L262–L265**：其他状态意味着当前执行不应继续。

```text
 266 │ 		case <-deadline:
 267 │ 			cancelExec()
 268 │ 			_, err := w.service.Timeout(
 269 │ 				context.Background(), run.ID, w.owner, run.Revision,
 270 │ 			)
 271 │ 			if errors.Is(err, ErrConflict) {
 272 │ 				return w.settleTransitionConflict(run.ID)
 273 │ 			}
 274 │ 			return err
 275 │ 		case <-ctx.Done():
 276 │ 			cancelExec()
 277 │ 			return ctx.Err()
 278 │ 		}
 279 │ 	}
 280 │ }
```

- **L266–L270**：到总 deadline 则停止执行并尝试写 timed_out。
- **L271–L274**：超时提交冲突时读取/协调最新状态，而不是盲目覆盖。
- **L275–L280**：进程上游 Context 取消时停止执行；任务后续由持久化恢复机制决定。


## 检查点决定从哪里继续

源码：[workers/python/src/ai_companion_worker/agent_runtime.py:7797](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:7797)

<a id="symbols-4-7"></a>

### 函数与变量详解

函数/类型声明：[`execute`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:7797)。

**职责、输入输出与影响：** AgentRuntime.execute 输入 run_id，以及仅关键字参数 initial/resolution；返回 dict。检查持久化检查点，选择首次 invoke、等待返回、resume 或从剩余节点继续；可能调用多个节点和外部工具。

**调用关系：** execute_run → runtime.execute；runtime_config/get_state → _validate_checkpoint_identity → _graph.invoke。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `self` | AgentRuntime 实例 | 方法接收者 | 持有编译后的 _graph、递归上限、预期检查点身份。 |
| `run_id` | str | Go Run 的 ID | 用于运行配置与检查点定位，不是用户会话总 ID。 |
| `initial / resolution` | Mapping[str, Any] 或 None | 初始输入 / 已持久化恢复决策 | 没有检查点不能只给 resolution；已消费的决策不能重复 resume。 |
| `config` | 运行配置 dict | runtime_config(run_id, ...) | 绑定线程身份和递归上限，传 get_state/invoke。 |
| `snapshot / values` | 图状态快照对象 / dict | _graph.get_state / snapshot.values | values 是复制的状态视图，不是模型输入 Token 清单。 |
| `next_nodes` | tuple | snapshot.next | 为空且无待审批通常说明无剩余节点；非空可 invoke(None) 继续。 |
| `pending / normalized` | 中断载荷列表 / 校验后的决策对象 | 快照提取 / _validate_resolution | 前者说明等待什么，后者才交 Command(resume=...)。 |
| `result` | dict 形图输出 | _graph.invoke | cast 只提供类型意图，不自动修复错误数据。 |

### 源码与逐行解释

```text
7797 │     def execute(
7798 │         self,
7799 │         run_id: str,
7800 │         *,
7801 │         initial: Mapping[str, Any] | None = None,
7802 │         resolution: Mapping[str, Any] | None = None,
7803 │     ) -> dict[str, Any]:
7804 │         config = runtime_config(run_id, self._recursion_limit)
7805 │         snapshot = self._graph.get_state(config)
7806 │         values = getattr(snapshot, "values", {})
7807 │         values = dict(values) if isinstance(values, Mapping) else {}
7808 │         next_nodes = tuple(getattr(snapshot, "next", ()) or ())
7809 │         if not values:
7810 │             if resolution is not None:
7811 │                 raise ValueError("cannot resume an Agent Run without a checkpoint")
7812 │             if initial is None:
7813 │                 raise ValueError("initial Agent input is required")
```

- **L7797–L7803**：execute 同时支持首次 initial 和后续 resolution；关键是同一个 run_id。
- **L7804–L7808**：构造运行配置，读取检查点值和后续节点列表；非法 values 归为空。
- **L7809–L7813**：没有检查点时不允许 resume，首次必须提供 initial。

```text
7814 │             result = self._graph.invoke(
7815 │                 _validate_input(initial),
7816 │                 config=config,
7817 │             )
7818 │             return cast(dict[str, Any], result)
7819 │         _validate_checkpoint_identity(
7820 │             values,
7821 │             run_id=run_id,
7822 │             initial=initial,
7823 │             expected=self._checkpoint_identity,
7824 │         )
7825 │         pending = _snapshot_interrupt_payloads(snapshot)
7826 │         if pending:
7827 │             if resolution is None:
7828 │                 values["__interrupt_payloads__"] = pending
7829 │                 return cast(dict[str, Any], values)
```

- **L7814–L7818**：校验初始输入后 invoke 图，并返回结果。
- **L7819–L7824**：已有检查点时验证 Run、用户、模块、模型/Graph 等身份，防止把别的输入恢复到此运行。
- **L7825–L7829**：有待处理 interrupt 但未收到决策时，返回同一个待确认数据，不重复推进。

```text
7830 │             normalized = _validate_resolution(resolution, pending)
7831 │             result = self._graph.invoke(
7832 │                 Command(resume=normalized),
7833 │                 config=config,
7834 │             )
7835 │             return cast(dict[str, Any], result)
7836 │         if not next_nodes:
7837 │             return cast(dict[str, Any], values)
7838 │         # A durable caller may retain the trusted resolution after LangGraph
7839 │         # has checkpointed the interrupt node but a later node failed. In that
7840 │         # state the resolution is already consumed; replay from the next node
7841 │         # without applying it a second time.
7842 │         result = self._graph.invoke(None, config=config)
7843 │         return cast(dict[str, Any], result)
```

- **L7830–L7835**：决策与 pending interrupt 匹配后，用 Command(resume=...) 恢复图。
- **L7836–L7837**：没有后续节点说明图已走完，直接返回持久化结果。
- **L7838–L7843**：若审批已消费、后续节点失败，调用 invoke(None) 从下一节点续跑，不二次消费原审批。


## 把代码连成一次真实行为

### 分清四份不同的审批信息

| 信息 | 谁产生/保存 | 用来解决什么 |
|---|---|---|
| 工具候选及确认令牌 | 可信网关、运行内部状态 | 绑定被确认动作及运行/用户，防止随便改参数 |
| 待确认公开信息 | 对外 Run 视图 | 告诉用户将做什么，不直接暴露内部密钥 |
| approved 布尔值 | 用户明确选择 | 批准或拒绝这次动作 |
| Idempotency-Key | 提交方为一次逻辑决策指定 | 网络重试时识别同一次确认，不重复消费 |

审批等待中浏览器关闭，不等于批准，也不等于拒绝。回来后读取 waiting_approval 才能继续。用户点拒绝，Graph 走 reject 分支，不应执行对应 Commit。

### 取消为什么不是“立刻杀掉所有东西”

Worker 同时等待执行结果、控制状态、deadline 和 Context 取消。goroutine 是 Go 中的并发执行单元，channel 传递结果，select 选择当前可就绪的分支。取消是一条需要传播并被执行层响应的信号；已发生的外部副作用不能靠 cancel 自动倒放。

结果通道容量为 1 很关键：控制分支先退出后，后台执行如果恰好返回，仍可发送一次结果而不因没人接收永远卡在发送处。这不等于它允许无限结果；也不解决任意不响应取消的外部服务。

### 恢复与重试的身份差别

自动恢复继续同一个持久化 Run/检查点，使用数据库批准信息和当前领取权；用户在失败消息上点重试会创建新运行，旧运行留作记录。不能看到“又调用了 execute”就断言从第一步重新执行，也不能看到同一 run_id 就跳过租约检查。

Python 检查点里有待执行节点时，必须按状态选择初始输入、Command(resume=...) 或继续未完成节点。重复把一个已消费的审批塞入 resume，不是可靠重试。

### 自测与答案

问：approved=false 为什么不能用普通 bool 零值判断是否提交？答：普通 bool 会把未提交与明确拒绝都解成 false；指针保留了三态。

问：用户已批准，旧 Worker 为什么仍可能提交失败？答：批准只是动作许可，不授予永久执行权；当前 owner/revision/租约仍必须有效。

## 对照阅读与验证

重试策略在 agent/worker.go 的 finishExecution：区分永久失败、执行重试次数上限、退避和 deadline；只有重试时间仍落在 deadline 前才 Defer 回 queued。前端 stop 使用固定取消键，retryAgentRun 创建新的运行记录，旧 Run 留作证据。自动执行恢复与用户新建重试不是同一种操作。

[返回目录](README.md) · [下一章：普通聊天、上下文与记忆](05-context-memory.md)
