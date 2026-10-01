# 11 深入 Python：模型调用怎样驱动图

[阅读总入口](README.md)

这一章接在第 3 章 Worker 领取之后、第 4 章工具审批之前阅读。它回答“Python 怎么拿到 Go Run”“模型选择怎样变成节点跳转”“真正请求模型的代码在哪”。下面展示进程 executor；启用 Python 进程池时复用相同运行协议与检查点，而不是每次重新启动进程。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### Python 并不是“模型本体”

Go 管身份、持久化执行权和业务工具边界；Python 组织模型请求、状态图与文本/文档算法；远端模型通过 HTTP 返回文本或动作建议。读到 Python 函数并不表示本地正在训练或运行模型权重。

Graph 可以理解成一组有名字的处理函数和跳转规则。节点读取状态并产生更新；条件边选择下一节点；检查点保存恢复所需状态。它比“一次调用一个超长函数”多了显式的步骤、暂停和恢复位置，但仍由普通代码执行，不是模型凭空决定一切。

类型注解 TypedDict、Mapping、cast 帮助表达开发期契约；isinstance、Schema 校验和显式字段判断才实际检查运行时数据。模型返回的 JSON 是不可信建议，语法上能解析不等于业务上可以执行。

## Go 将运行记录发送到 Python 进程

源码：[internal/agent/python_executor.go:105](/Users/windcry1/Documents/ai_companion/internal/agent/python_executor.go:105)

<a id="symbols-11-1"></a>

### 函数与变量详解

函数/类型声明：[`Execute`](/Users/windcry1/Documents/ai_companion/internal/agent/python_executor.go:92)。

**职责、输入输出与影响：** PythonRuntimeExecutor.Execute 输入 Go Run，命名返回 (result ExecutionResult, executionErr error)。启动 Python 子进程、传 JSON、捕获输出与错误；完整函数后半段还有解码/契约检查，本段摘录只到初始化结果。

**调用关系：** RuntimeWorker 的 executor.Execute → exec.CommandContext → Python agent_worker；不是拼接 shell 执行用户文本。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `e` | PythonRuntimeExecutor | 值接收者 | Executable/Timeout/Environment/OnObserve 配置本次进程调用。 |
| `executable` | string | e.Executable 去空白，空用 python3 | 可执行文件名字/路径，不是自然语言命令字符串。 |
| `run` | agent.Run | 领取快照，再附运行 trace | 序列化整体结构供 Python 读取；用户无法直接改数据库租约。 |
| `payload` | []byte | json.Marshal(run) | 送 stdin 的协议 JSON，不是直接发给提供商的模型 payload。 |
| `command` | *exec.Cmd | exec.CommandContext(ctx, executable, "-m", ...) | stdin/env/stdout/stderr 配置进程边界；Run 等进程退出。 |
| `stdout / stderr` | bytes.Buffer | 初始空，进程写入 | stdout 是结果协议，stderr 是诊断文本；混用会破坏解析。 |
| `message / structured` | string / 结构化执行错误指针 | 截断后的 stderr / parseExecutorError | 有已知结构时保留重试等错误信息，未知则包装普通错误。 |
| `result / executionErr` | ExecutionResult / error | 命名返回变量 | 延迟观测函数读取最终值；result.Status 与 err 共同描述本次调用。 |

### 源码与逐行解释

```text
 105 │ 	executable := strings.TrimSpace(e.Executable)
 106 │ 	if executable == "" {
 107 │ 		executable = "python3"
 108 │ 	}
 109 │ 	if e.Timeout > 0 {
 110 │ 		var cancel context.CancelFunc
 111 │ 		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
 112 │ 		defer cancel()
 113 │ 	}
 114 │ 	run = runtimeRunWithTraceContext(ctx, run)
 115 │ 	payload, err := json.Marshal(run)
 116 │ 	if err != nil {
 117 │ 		return ExecutionResult{}, err
 118 │ 	}
 119 │ 	command := exec.CommandContext(ctx, executable, "-m", "ai_companion_worker.agent_worker")
 120 │ 	command.Stdin = bytes.NewReader(payload)
 121 │ 	command.Env = e.commandEnv()
 122 │ 	var stdout, stderr bytes.Buffer
 123 │ 	command.Stdout = &stdout
 124 │ 	command.Stderr = &stderr
```

- **L105–L108**：选择配置的 Python 可执行程序，空则 python3。
- **L109–L113**：如果设置进程超时，就在上游 Context 上派生更短截止时间；退出释放取消函数。
- **L114**：把当前 Trace 传播信息并入运行输入。
- **L115–L118**：Run 序列化为 JSON，编码失败不启动进程。
- **L119**：exec.CommandContext 直接执行 Python 模块，无 shell 字符串拼接；Context 取消可停止该进程。
- **L120**：JSON 从 stdin 输入，用户内容不会作为命令行参数执行。
- **L121**：使用受控环境变量设置模型/模块路径等运行配置。
- **L122–L124**：stdout 专用于返回协议，stderr 用于诊断，两者分开。

```text
 125 │ 	if err = command.Run(); err != nil {
 126 │ 		if ctxErr := ctx.Err(); ctxErr != nil {
 127 │ 			return ExecutionResult{}, ctxErr
 128 │ 		}
 129 │ 		message := strings.TrimSpace(stderr.String())
 130 │ 		if len(message) > 4000 {
 131 │ 			message = message[:4000]
 132 │ 		}
 133 │ 		if message == "" {
 134 │ 			message = err.Error()
 135 │ 		}
 136 │ 		if structured := parseExecutorError(message); structured != nil {
 137 │ 			return ExecutionResult{}, structured
 138 │ 		}
 139 │ 		return ExecutionResult{}, fmt.Errorf("Python Agent runtime failed: %s", message)
 140 │ 	}
 141 │ 	if stdout.Len() == 0 || stdout.Len() > maxRuntimeOutputBytes {
 142 │ 		return ExecutionResult{}, runtimeContractError("Python Agent runtime returned invalid output")
 143 │ 	}
```

- **L125–L128**：子进程失败且 Context 已结束，优先报告 Context 错误。
- **L129–L135**：其余错误从 stderr 获取，最多保留 4000 字符，空则使用进程错误。
- **L136–L139**：若存在结构化执行错误，保留其错误码/重试语义；否则包装通用 Python 失败。
- **L140–L143**：成功退出仍要求 stdout 非空且不超过响应上限，再在后续行解码 ExecutionResult。


## 输入转换与可信工具目录

源码：[workers/python/src/ai_companion_worker/agent_worker.py:175](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_worker.py:175)

<a id="symbols-11-2"></a>

### 函数与变量详解

函数/类型声明：[`execute_run`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_worker.py:175)。

**职责、输入输出与影响：** 输入 Go Run 映射及显式 checkpointer/decisions/tools/definitions，返回执行结果 dict。构建/复用 AgentRuntime 并注入可信工具表，恢复图，将中断映射为 waiting_approval/waiting_tool。

**调用关系：** agent_worker 进程入口调用；_build_runtime → 可信目录过滤 → runtime.execute → interrupt_payloads。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `run / run_id` | Mapping[str,Any] / str | 进程输入 / 必填 id | run 包含 input 与恢复信息，run_id 关联检查点。 |
| `checkpointer / decisions / tools` | 检查点对象 / DecisionPort / ToolGateway | 入口装配的协作者 | 分别持久化图状态、调用决策模型、跨边界执行工具。 |
| `definitions` | list[dict[str,Any]] | Go 侧提供的工具目录 | 过滤后覆盖 trusted_context.tools，不能信任原输入自带的工具清单。 |
| `runtime / policy / recursion_limit` | AgentRuntime 可选 / BudgetPolicy / int | 可复用参数或按 run 构建 | runtime 已存在时不重复构建；递归上限限制图推进。 |
| `input_payload / message` | dict / str | run.input / user_message 或 text | 运行时必须检验类型与非空，不因 Mapping 注解自动合法。 |
| `context / trusted_context` | dict / 外层复制的 dict | 输入上下文 / dict(context) | 复制后注入可信 tools；并非所有嵌套值都深复制。 |
| `initial / resolution` | dict / dict 或 None | 规范化初始图输入 / resume_resolution | resolution 非空却不是对象时拒绝。 |
| `result / interrupts / interrupt_types` | dict / 中断列表 / set | runtime.execute / 提取 / type 集合 | 全部为 tool_approval 或 tool_wait 才映射对应等待状态，不接受任意混合。 |

### 源码与逐行解释

```text
 175 │ def execute_run(
 176 │     run: Mapping[str, Any],
 177 │     *,
 178 │     checkpointer: Any,
 179 │     decisions: DecisionPort,
 180 │     tools: ToolGateway,
 181 │     definitions: list[dict[str, Any]],
 182 │     runtime: AgentRuntime | None = None,
 183 │     telemetry: LangfuseTelemetry | None = None,
 184 │ ) -> dict[str, Any]:
 185 │     run_id = _required_string(run, "id")
 186 │     _validate_graph_identity(run)
```

- **L175–L184**：execute_run 显式接收 checkpointer、决策端口、工具网关和工具定义，允许复用 runtime/telemetry。
- **L185–L186**：Run ID 必填，且必须与支持的 Graph 身份匹配。

```text
 187 │     if runtime is None:
 188 │         policy, recursion_limit = _agent_runtime_policy(run)
 189 │         runtime = _build_runtime(
 190 │             run,
 191 │             checkpointer=checkpointer,
 192 │             decisions=decisions,
 193 │             tools=tools,
 194 │             policy=policy,
 195 │             recursion_limit=recursion_limit,
 196 │         )
 197 │     module = _module(run.get("module"))
 198 │     input_payload = run.get("input")
 199 │     if not isinstance(input_payload, dict):
 200 │         raise ValueError("Agent Run input must be an object")
 201 │     message = input_payload.get("user_message", input_payload.get("text"))
 202 │     if not isinstance(message, str) or not message.strip():
 203 │         raise ValueError("Agent Run input requires user_message or text")
 204 │     context = input_payload.get("context", {})
 205 │     if not isinstance(context, dict):
 206 │         raise ValueError("Agent Run context must be an object")
```

- **L187–L196**：没有可复用运行时则根据 Run 快照解析预算和递归上限，构建图并绑定检查点。
- **L197–L200**：校验模块，input 必须为字典。
- **L201–L203**：兼容 user_message/text 字段，但正文必须非空。
- **L204–L206**：Context 必须是对象。

```text
 207 │     trusted_context = dict(context)
 208 │     trusted_context["tools"] = [
 209 │         dict(item) for item in _filter_agent_tool_definitions(run, definitions)
 210 │     ]
 211 │     initial = {
 212 │         "run_id": run_id,
 213 │         "user_id": _required_string(run, "user_id"),
 214 │         "conversation_id": _required_string(run, "conversation_id"),
 215 │         "character_id": _required_string(run, "character_id"),
 216 │         "module": module,
 217 │         "user_message": message.strip(),
 218 │         "context": trusted_context,
 219 │     }
 220 │     raw_resolution = run.get("resume_resolution")
 221 │     resolution = raw_resolution if isinstance(raw_resolution, dict) else None
 222 │     if raw_resolution is not None and resolution is None:
 223 │         raise ValueError("Agent Run resume_resolution must be an object")
 224 │     active_telemetry = telemetry or LangfuseTelemetry(LangfuseSettings())
 225 │     with active_telemetry.observe_run(run) as langfuse_run:
```

- **L207–L210**：复制 Context 后，用可信且经过 Agent 定义过滤的工具目录覆盖 tools 字段；用户文本不能自行授予工具。
- **L211–L219**：建立规范 AgentInput：Run/用户/会话/角色身份、模块、当前请求和可信 Context。
- **L220–L223**：恢复决策可空，但非空必须为对象。
- **L224–L225**：配置可选 Langfuse 观察上下文；核心运行不以远端观察成功为前提。

```text
 226 │         result = runtime.execute(
 227 │             run_id,
 228 │             initial=initial,
 229 │             resolution=resolution,
 230 │         )
 231 │         interrupts = interrupt_payloads(result)
 232 │         if interrupts:
 233 │             interrupt_types = {item.get("type") for item in interrupts}
 234 │             if interrupt_types == {"tool_approval"}:
 235 │                 status = "waiting_approval"
 236 │             elif interrupt_types == {"tool_wait"}:
 237 │                 status = "waiting_tool"
 238 │             else:
 239 │                 raise ValueError("Agent runtime returned unsupported mixed interrupts")
```

- **L226–L230**：调用前面解释过的 runtime.execute，首跑和恢复共用同一 Run ID。
- **L231–L239**：读取 interrupt 类型，仅允许纯 tool_approval 或纯 tool_wait，分别映射 waiting_approval/waiting_tool；混合或未知类型为协议错误。


## 选择直接回答、单工具还是多步执行

源码：[workers/python/src/ai_companion_worker/agent_runtime.py:3326](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3326)

<a id="symbols-11-3"></a>

### 函数与变量详解

函数/类型声明：[`_classify_execution_mode`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3326)。

**职责、输入输出与影响：** 输入 state、tool_name、可选可信 definition，返回 (ExecutionMode,原因 str)。纯分类，无模型/工具调用；direct 仍是图中的一种模式。

**调用关系：** decide 校验模型提案后调用；根据旧计划、可信 requires_plan/repeatable 与工具种类分类。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `state / existing` | AgentState / 可选模式值 | 图状态 / state.execution_mode | 已有多步模式或 plan 时保持 agentic。 |
| `tool_name` | str | 模型提案经过清理的工具名 | 空时 direct；不以句子长短决定模式。 |
| `definition` | Mapping[str,Any] 或 None | 可信工具定义查询 | requires_plan/repeatable 是可信属性，不是让模型自己声明权限。 |
| `normalized` | str | 工具名 strip/lower/点转下划线 | 识别附件提取等中间步骤，要求后续进展跟踪。 |

### 源码与逐行解释

```text
3326 │ def _classify_execution_mode(
3327 │     state: AgentState,
3328 │     *,
3329 │     tool_name: str,
3330 │     definition: Mapping[str, Any] | None,
3331 │ ) -> tuple[ExecutionMode, str]:
3332 │     existing = state.get("execution_mode")
3333 │     if existing == "agentic" or state.get("plan"):
3334 │         return "agentic", "existing_multi_step_plan"
3335 │     if not tool_name:
3336 │         return "direct", "model_answered_without_project_tool"
3337 │     if definition is not None and definition.get("requires_plan") is True:
3338 │         return "agentic", "trusted_tool_requires_precondition_plan"
3339 │     if definition is not None and definition.get("repeatable") is True:
3340 │         return "agentic", "repeatable_tool_requires_progress_tracking"
3341 │     normalized = tool_name.strip().lower().replace(".", "_")
3342 │     if normalized == "work_extract_attached_document":
3343 │         return "agentic", "intermediate_artifact_step"
3344 │     return "single_action", "one_trusted_tool_can_complete_request"
```

- **L3326–L3331**：分类输入为当前状态、已选工具名及可信定义，输出模式和诊断原因。
- **L3332–L3334**：已有多步计划或已经 agentic，就保留该模式，避免执行中途丢掉进度追踪。
- **L3335–L3336**：无工具为 direct；direct 仍在这个 Graph 中运行，不代表绕过 Agent。
- **L3337–L3338**：可信工具定义要求前置规划时选择 agentic。
- **L3339–L3340**：repeatable 工具需要跟踪重复执行的进度，因此 agentic。
- **L3341–L3343**：附件提取是中间步骤，不应误当交付终点，强制多步模式。
- **L3344**：其余一个可信工具即可完成请求的情形为 single_action，可省 Planner 模型调用。


## 模型决策转为受约束的状态更新

源码：[workers/python/src/ai_companion_worker/agent_runtime.py:3752](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3752)

<a id="symbols-11-4"></a>

### 函数与变量详解

函数/类型声明：[`decide`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3701)。

**职责、输入输出与影响：** 图内 decide 输入 AgentState，返回 dict 状态增量。当前摘录是模型决策分支；函数更早还有确定性续处理分支。可能请求模型，校验后只提出下一动作，不在这段直接提交业务写入。

**调用关系：** Graph decision 节点 → model_access_reason/with_model_allowance → decisions.decide → 可信定义/参数检查 → 模式分类 → 状态更新。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `state / node` | AgentState / str | 图状态 / 当前节点标签 | state 有原问题、模块、计划、观察与预算，node 标记归属。 |
| `reason` | str | model_access_reason | 非空表示额度/访问受限，立即产生终态更新而不继续模型请求。 |
| `started_ns` | int | time.perf_counter_ns() | 单调高精度耗时起点，不是 Unix 日期，不能直接显示为时间戳。 |
| `decision_context` | dict | with_model_allowance 再添加 plan/observations 等 | 为这次决策准备的模型上下文投影，不是整个 state 原样发出。 |
| `decision` | ModelDecision | DecisionPort.decide 返回 | 含 intent/tool_name/tool_arguments/response/needs_response，仍需校验。 |
| `intent / tool_name / tool_arguments` | str / str / dict | decision 清理/复制 | 意图不能为空；有工具才处理参数，没工具不能要求参数组合。 |
| `definition / requires_composition` | 可信定义或 None / bool | 可信目录查询 / compose_arguments 属性 | 模型的要求不能扩大 trusted catalog 的能力。 |
| `response / model_update` | str / dict | decision.response / 观测更新 | 前者为候选文本，后者为成本/耗时等诊断状态。 |
| `execution_mode / execution_mode_reason` | 模式字符串 / str | _classify_execution_mode | 记录为什么走 direct/single_action/agentic。 |
| `update` | dict[str,Any] | 本节点构建的增量 | 先清旧 proposed_tool/preparation/tool_result，再写新的提案或回答，防残留结果混入。 |

### 源码与逐行解释

```text
3752 │         reason = model_access_reason(state, node)
3753 │         if reason:
3754 │             return model_terminal_update(state, node=node, reason=reason)
3755 │         started_ns = time.perf_counter_ns()
3756 │         decision_context = with_model_allowance(
3757 │             state,
3758 │             state.get("context", {}),
3759 │             node=node,
3760 │         )
3761 │         decision_context["agent_plan"] = dict(state.get("plan", {}))
3762 │         decision_context["observations"] = list(state.get("observations", []))
3763 │         decision_context["action_index"] = state.get("action_index", 0)
3764 │         decision_context["task_contract"] = dict(state.get("task_contract", {}))
3765 │         decision_context["artifact_validation"] = dict(state.get("artifact_validation", {}))
3766 │         decision_context["source_coverage"] = _document_source_coverage(state)
```

- **L3752–L3754**：调用模型前检查版本/预算等限制，有阻断原因直接生成终态更新。
- **L3755**：记录高精度起始时间。
- **L3756–L3760**：建立带本节点剩余模型调用/Token/成本 allowance 的决策 Context。
- **L3761–L3766**：加入当前计划、真实工具观察、动作序号、任务契约、制品验证和文档覆盖情况。

```text
3767 │         try:
3768 │             decision = decisions.decide(
3769 │                 module=state["module"],
3770 │                 message=state["user_message"],
3771 │                 context=decision_context,
3772 │             )
3773 │             intent = decision.intent.strip()
3774 │             if not intent:
3775 │                 raise ValueError("model decision intent is required")
3776 │             tool_name = decision.tool_name.strip()
3777 │             tool_arguments = dict(decision.tool_arguments) if tool_name else {}
3778 │             requires_composition = False
3779 │             if tool_name:
3780 │                 definition = _trusted_tool_definition(state, tool_name)
3781 │                 requires_composition = bool(
3782 │                     definition is not None and definition.get("compose_arguments") is True
3783 │                 )
```

- **L3767–L3772**：调用 DecisionPort.decide，将模块、当前用户请求和上下文交具体模型适配器。
- **L3773–L3775**：intent 必须存在，空字符串不是合法决策。
- **L3776–L3778**：提取工具名/参数，默认不需要 Composer。
- **L3779–L3783**：是否可做参数组合取自可信工具定义的 compose_arguments。

```text
3784 │                 if decision.requires_argument_composition and not requires_composition:
3785 │                     raise ValueError(
3786 │                         "model decision requested argument composition outside "
3787 │                         "the trusted tool catalog"
3788 │                     )
3789 │                 if not requires_composition and definition is not None:
3790 │                     _validate_tool_arguments(tool_arguments, definition)
3791 │             elif decision.requires_argument_composition:
3792 │                 raise ValueError("argument composition requires a selected tool")
3793 │             response = decision.response.strip()
3794 │             if not tool_name and not response and not decision.needs_response:
3795 │                 raise ValueError("model decision must contain a response or one tool call")
3796 │         except ModelBudgetExceeded as exc:
3797 │             return model_terminal_update(state, node=node, reason=str(exc))
```

- **L3784–L3788**：模型自行要求目录未允许的 Composer 能力时拒绝。
- **L3789–L3792**：无需 Composer 的工具参数直接校验；没有工具却要求组合也拒绝。
- **L3793–L3795**：无工具时至少已有回答或明确 needs_response，否则决策为空。
- **L3796–L3797**：模型预算耗尽进入预算终态，不做无限重试。

```text
3798 │         except Exception as exc:
3799 │             return model_failure_update(
3800 │                 state,
3801 │                 node=node,
3802 │                 role="router",
3803 │                 started_ns=started_ns,
3804 │                 error=exc,
3805 │             )
3806 │         model_update = model_observability_update(
3807 │             state,
3808 │             node=node,
3809 │             role="router",
3810 │             started_ns=started_ns,
3811 │         )
3812 │         execution_mode, execution_mode_reason = _classify_execution_mode(
3813 │             state,
3814 │             tool_name=tool_name,
3815 │             definition=(_trusted_tool_definition(state, tool_name) if tool_name else None),
3816 │         )
```

- **L3798–L3805**：其他异常形成 router 角色的结构化模型失败状态。
- **L3806–L3811**：成功后结算模型调用观察与用量。
- **L3812–L3816**：使用前一节分类函数决定 direct/single_action/agentic。

```text
3817 │         update: dict[str, Any] = {
3818 │             **model_update,
3819 │             "intent": intent,
3820 │             "execution_mode": execution_mode,
3821 │             "execution_mode_reason": execution_mode_reason,
3822 │             "proposed_tool": {},
3823 │             "preparation": {},
3824 │             "tool_result": {},
3825 │             "needs_response": False,
3826 │             "steps": state.get("steps", 0) + 1,
3827 │         }
3828 │         if tool_name:
3829 │             if not tool_allowed(state["module"], tool_name):
3830 │                 update.update(
3831 │                     {
3832 │                         "outcome": "tool_denied",
3833 │                         "response": "当前角色无权调用该工具，请切换到对应模块后重试。",
3834 │                     }
3835 │                 )
3836 │                 return update
```

- **L3817–L3827**：组装新的状态增量，清理旧 proposed_tool/preparation/tool_result，步骤数加 1。
- **L3828–L3836**：如果模型选了工具，还要校验模块权限；拒绝时输出明确失败结果，不进入 Go 写业务。

```text
3837 │             update["proposed_tool"] = {
3838 │                 "name": tool_name,
3839 │                 "arguments": tool_arguments,
3840 │                 "compose_arguments": requires_composition,
3841 │             }
3842 │             return update
3843 │         if response:
3844 │             update.update({"outcome": "completed", "response": response})
3845 │             return update
3846 │         if decision.needs_response:
3847 │             update.update({"outcome": "", "response": "", "needs_response": True})
3848 │             return update
3849 │         return update
```

- **L3837–L3842**：合法工具写入 proposed_tool，包含参数和 Composer 标记，等待路由函数选择下一节点。
- **L3843–L3845**：已经给出回答则记 outcome=completed，但下一跳还要过响应质量门禁。
- **L3846–L3849**：需独立 Responder 则设置 needs_response，返回状态增量。


## after_decision 是实际分支选择器

源码：[workers/python/src/ai_companion_worker/agent_runtime.py:3851](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3851)

<a id="symbols-11-5"></a>

### 函数与变量详解

函数/类型声明：[`after_decision`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3851)。

**职责、输入输出与影响：** 输入 AgentState，返回下一个节点名称 str。纯路由函数，返回 "plan" 并不意味着它自己已经生成计划；实际节点由图引擎随后执行。

**调用关系：** decide 更新后条件边调用；有组合需求时由 composition_route 决定具体组合节点。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `state` | AgentState | 决策合并后的图状态 | 读取 execution_mode、plan、needs_response、outcome、response。 |
| `proposed` | dict 或空/缺失值 | state.get("proposed_tool") | 非空说明有工具提案；compose_arguments 决定是否先构造参数。 |
| `execution_mode / needs_response` | state 字段：模式值 / bool | 前一节点设置 | agentic 且无 plan 先计划；没工具但需回答则 generate_response。 |
| `outcome / response` | state 字段：str / str | 前面计算结果 | completed 且有正文才进入质量门禁，否则可能 finalize。 |

### 源码与逐行解释

```text
3851 │     def after_decision(state: AgentState) -> str:
3852 │         proposed = state.get("proposed_tool")
3853 │         if proposed and state.get("execution_mode") == "agentic" and not state.get("plan"):
3854 │             return "plan"
3855 │         if proposed and proposed.get("compose_arguments") is True:
3856 │             return composition_route(state)
3857 │         if proposed:
3858 │             return "preflight_normalize"
3859 │         if state.get("needs_response"):
3860 │             return "generate_response"
3861 │         if state.get("outcome") == "completed" and state.get("response"):
3862 │             return "response_quality_gate"
3863 │         return "finalize"
```

- **L3851–L3852**：读取模型选择形成的 proposed_tool。
- **L3853–L3854**：多步任务尚无 Plan，下一节点是 plan。
- **L3855–L3856**：参数需组合时交 composition_route 决定串行或按文档批次并行。
- **L3857–L3858**：已有可执行工具且不用组合，进入 preflight_normalize，然后才能 Prepare。
- **L3859–L3860**：无工具且需文本回答时进入 generate_response。
- **L3861–L3862**：模型已经直接给出回答时进入 response_quality_gate。
- **L3863**：其他情况由 finalize 收尾，不凭空补一次业务写入。


## 真正的模型 HTTP 请求

源码：[workers/python/src/ai_companion_worker/openrouter_decision.py:2230](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/openrouter_decision.py:2230)

<a id="symbols-11-6"></a>

### 函数与变量详解

函数/类型声明：[`_request`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/openrouter_decision.py:2230)。

**职责、输入输出与影响：** 模型适配器 _request 输入 payload Mapping 与 timeout_seconds，返回 dict。真实外部 HTTP POST；读取响应字节有上限，HTTP/超时/JSON 错误转 OpenRouterError，本函数不等于所有上层重试策略。

**调用关系：** 模型适配层构造 payload 后调用；urllib Request/urlopen → 有界读取 → json.loads → 对象校验。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `self` | OpenRouterDecisionPort | 方法接收者 | _config 保存地址/密钥，_trace_headers 为追踪信息；不要写进教材真实密钥。 |
| `payload / body` | Mapping[str,Any] / bytes | 模型请求对象 / UTF-8 JSON | body 是线上发送的字节，不同于 Go 进程协议 payload。 |
| `timeout_seconds` | float | 上层为本次尝试分配 | 同时给 wall-clock deadline 与 urlopen，不是无限等待。 |
| `request / response` | urllib.request.Request / HTTP 响应对象 | 构造 POST / urlopen 返回 | Authorization 使用服务端配置密钥；response 不是最终助手文本。 |
| `raw` | bytes | response.read(MAX+1) | 多读一字节检测超大响应，校验后才解 JSON。 |
| `exc / retry_after` | 异常对象 / str | 异常捕获 / Retry-After 头 | 保留状态码与退避线索给上层；不是这里偷偷睡眠重试。 |
| `decoded` | JSON 解码后的未知类型，验证后 dict | json.loads(raw) | 先验证是对象，cast 只补类型意图。 |

### 源码与逐行解释

```text
2230 │     def _request(
2231 │         self,
2232 │         payload: Mapping[str, Any],
2233 │         *,
2234 │         timeout_seconds: float,
2235 │     ) -> dict[str, Any]:
2236 │         body = json.dumps(
2237 │             payload,
2238 │             ensure_ascii=False,
2239 │             separators=(",", ":"),
2240 │         ).encode("utf-8")
2241 │         request = urllib.request.Request(
2242 │             self._config.base_url.rstrip("/") + "/chat/completions",
2243 │             data=body,
2244 │             method="POST",
2245 │             headers={
2246 │                 "Authorization": f"Bearer {self._config.api_key}",
2247 │                 "Content-Type": "application/json",
2248 │                 "Accept": "application/json",
2249 │                 **self._trace_headers,
```

- **L2230–L2235**：底层请求接受已构造的 payload 和本次 timeout；具体模型选择/回退预算由上层方法处理。
- **L2236–L2240**：紧凑编码 JSON 并转 UTF-8，保留中文，减少无用转义。
- **L2241–L2244**：请求配置的 base_url/chat/completions，正文为 JSON，方法 POST。
- **L2245–L2249**：携带服务端 API Key、JSON Content/Accept 以及 Trace 头；此密钥不从前端传入。

```text
2250 │                 **(
2251 │                     {"HTTP-Referer": self._config.http_referer} if self._config.http_referer else {}
2252 │                 ),
2253 │                 **(
2254 │                     {"X-OpenRouter-Title": self._config.app_title} if self._config.app_title else {}
2255 │                 ),
2256 │             },
2257 │         )
2258 │         try:
2259 │             with _wall_clock_deadline(timeout_seconds):
2260 │                 with urllib.request.urlopen(
2261 │                     request,
2262 │                     timeout=timeout_seconds,
2263 │                 ) as response:
2264 │                     raw = response.read(_MAX_RESPONSE_BYTES + 1)
```

- **L2250–L2257**：可选附应用来源和标题，不影响业务工具权限。
- **L2258–L2264**：同时设墙钟截止与网络 timeout，最多读响应上限+1 字节，以识别超限。

```text
2265 │         except urllib.error.HTTPError as exc:
2266 │             retry_after = exc.headers.get("Retry-After", "")
2267 │             raise OpenRouterError(
2268 │                 f"OpenRouter request failed with status {exc.code}",
2269 │                 status_code=exc.code,
2270 │                 retry_after=retry_after,
2271 │             ) from exc
2272 │         except _WallClockDeadlineExceeded as exc:
2273 │             raise OpenRouterError(
2274 │                 "OpenRouter HTTP attempt exceeded its wall-clock deadline",
2275 │                 status_code=408,
2276 │             ) from exc
2277 │         except TimeoutError as exc:
2278 │             raise OpenRouterError("OpenRouter request timed out", status_code=408) from exc
2279 │         except urllib.error.URLError as exc:
2280 │             raise OpenRouterError("OpenRouter is unavailable") from exc
2281 │         if len(raw) > _MAX_RESPONSE_BYTES:
2282 │             raise OpenRouterError("OpenRouter response exceeded the size limit")
```

- **L2265–L2271**：HTTP 错误保留状态码与 Retry-After，供上层退避/分类，而不是把错误 HTML 当模型回答。
- **L2272–L2276**：墙钟超时映射成 408 类模型错误。
- **L2277–L2280**：超时与其他 URL 连接错误分别包装。
- **L2281–L2282**：超过最大响应字节数拒绝。

```text
2283 │         try:
2284 │             decoded = json.loads(raw)
2285 │         except (json.JSONDecodeError, UnicodeDecodeError) as exc:
2286 │             raise OpenRouterError("OpenRouter returned invalid JSON") from exc
2287 │         if not isinstance(decoded, dict):
2288 │             raise OpenRouterError("OpenRouter response must be an object")
2289 │         return cast(dict[str, Any], decoded)
```

- **L2283–L2286**：解析 JSON；编码或 JSON 错误都报告供应商协议问题。
- **L2287–L2289**：要求响应顶层是对象，再交上层读取 choices/tool_calls/usage。


## 生成文本并进入质量检查

源码：[workers/python/src/ai_companion_worker/agent_runtime.py:5745](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:5745)

<a id="symbols-11-7"></a>

### 函数与变量详解

函数/类型声明：[`generate_response`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:5745)。

**职责、输入输出与影响：** 输入 AgentState，返回 dict 状态增量。检查访问与预算，调用 responder 得到非空文本；错误返回失败/预算终态更新，成功之后仍要进入质量门禁。

**调用关系：** after_decision 等路由进入；getattr(decisions,"respond") → with_model_allowance → responder → model_observability_update。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `node / reason` | str / str | 固定 generate_response / 访问检查结果 | 供预算与诊断区分模型节点。 |
| `responder` | 可调用对象或 None | getattr(decisions,"respond",None) | 不是文本；必须 callable，调用后才得到 response。 |
| `response_context` | dict | 本次模型 allowance 与计划/观察/任务契约 | 不直接将整个状态及所有私有字段透传。 |
| `started_ns` | int | perf_counter_ns | 测耗时，不是消息创建时间。 |
| `response` | str，经运行时检查 | responder(...) 返回 | 去空白后写状态 response，空字符串视为失败。 |
| `state` | AgentState | 节点输入 | 读取 user_message/module 与已有 steps；输出 steps+1，并设 needs_response=false。 |
| `exc` | 异常对象 | 模型调用/验证抛出 | 预算异常与一般模型异常走不同更新函数。 |

### 源码与逐行解释

```text
5745 │     def generate_response(state: AgentState) -> dict[str, Any]:
5746 │         node = "generate_response"
5747 │         reason = model_access_reason(state, node)
5748 │         if reason:
5749 │             return model_terminal_update(state, node=node, reason=reason)
5750 │         responder = getattr(decisions, "respond", None)
5751 │         if not callable(responder):
5752 │             raise ValueError("decision port must implement respond for conversational output")
5753 │         started_ns = time.perf_counter_ns()
5754 │         response_context = with_model_allowance(
5755 │             state,
5756 │             state.get("context", {}),
5757 │             node=node,
5758 │         )
5759 │         response_context["agent_plan"] = dict(state.get("plan", {}))
5760 │         response_context["observations"] = list(state.get("observations", []))
5761 │         response_context["task_contract"] = dict(state.get("task_contract", {}))
```

- **L5745–L5749**：Responder 节点先做模型访问/预算检查。
- **L5750–L5752**：决策端口必须实现 respond；缺能力是契约错误。
- **L5753–L5758**：记录时间并附当前节点调用 allowance。
- **L5759–L5761**：输入计划、真实观察和任务契约，让回答基于当前执行状态。

```text
5762 │         try:
5763 │             response = responder(
5764 │                 module=state["module"],
5765 │                 message=state["user_message"],
5766 │                 context=response_context,
5767 │             )
5768 │             if not isinstance(response, str) or not response.strip():
5769 │                 raise ValueError("model response must be non-empty")
5770 │         except ModelBudgetExceeded as exc:
5771 │             return model_terminal_update(state, node=node, reason=str(exc))
5772 │         except Exception as exc:
5773 │             return model_failure_update(
5774 │                 state,
5775 │                 node=node,
5776 │                 role="responder",
5777 │                 started_ns=started_ns,
5778 │                 error=exc,
5779 │             )
```

- **L5762–L5767**：调用 responder，模块/当前请求/Context 分开传递。
- **L5768–L5769**：拒绝空或非字符串回答。
- **L5770–L5771**：预算耗尽转终态。
- **L5772–L5779**：其他异常保存 responder 角色失败与时序。

```text
5780 │         return {
5781 │             **model_observability_update(
5782 │                 state,
5783 │                 node=node,
5784 │                 role="responder",
5785 │                 started_ns=started_ns,
5786 │             ),
5787 │             "response": response.strip(),
5788 │             "needs_response": False,
5789 │             "outcome": "completed",
5790 │             "steps": state.get("steps", 0) + 1,
5791 │         }
```

- **L5780–L5786**：合并观察数据和已发生模型用量。
- **L5787–L5791**：保存正文、清 needs_response、记 completed 并步数+1；Graph 边随后将其送入 response_quality_gate。


response_quality_gate 调用 inspect_and_repair_response，先做确定性检查/修复；已存在失败终态不能被文字质量通过覆盖成成功。未通过时至多发起一次模型改写。artifact_quality_gate 则针对真实生成文件及来源完整性，两种门禁检查的对象不同。

## 把代码连成一次真实行为

### 跟踪同一个对象跨越三层表示

| 位置 | 表示 | 读代码时寻找 |
|---|---|---|
| Go 中 | Run 结构、Input JSON、配置快照 | 哪些来自数据库，哪些来自用户 |
| 进程输入 | 标准输入里的 JSON 字节 | 编码、超时、stdout/stderr 协议 |
| Python 中 | dict、列表、Graph state | 运行时校验、状态更新、检查点身份 |
| 模型网络请求 | messages/tools 等 HTTP JSON | 实际 endpoint、认证头、预算、异常 |
| 模型响应 | 解析后的对象/字符串 | Schema、工具白名单、下一节点 |
| 返回 Go | 执行结果协议 | waiting/completed/error 如何映射 |

标准输出是机器读取的协议通道；随意 print 一句调试信息可能污染结果，诊断应按现有约定走标准错误或日志。Go 使用参数列表启动 Python，不是把用户文字拼成 shell 命令，但工具内部每个执行边界仍要独立检查。

### “选工具”到“真的做了”的距离

decision 节点先检查允许访问和预算，再调用决策端口；模型提出工具后，代码校验工具名、参数 Schema、模块允许范围和版本限制，然后才把 proposed_tool 写入状态。after_decision 负责在计划、组合、预检、响应与质量检查等分支间跳转。模型提出非法工具并不应自动增加注册表中的能力。

direct 表示不需要工具的执行模式，仍经过图中的响应/质量逻辑，不等于绕过所有节点直接把原始字符串透传。single_action 与 agentic 的区别也不是只按用户句子长度判断，而涉及工具定义、是否可重复和是否需要计划等条件。

### 一次 await/invoke 不一定是一轮模型调用

一个 Graph 运行可能包含路由、规划、执行、评估、修复与最终回答，多次节点都可能各自调用模型。每次调用要看自己的输入投影、预算和结果验证；不能拿最终回答字数推断总成本。网络层还负责响应体大小、超时、错误状态与 Retry-After 等信息。

模型生成成功以后还会有质量门禁；当前文本修写受次数限制，不是“直到好为止”无限循环。Graph completed、工具 completed、HTTP 请求完成是不同层级，必须指明在说哪一个。

### 自测与答案

问：cast(dict, value) 会把列表真正变成字典吗？答：不会；它主要表达类型意图，运行时对象不会因 cast 自动转换。用 dict(...) 也只是具体构造操作，嵌套值通常仍共享引用。

问：恢复时 Graph 配置被全局更新，能直接用最新版重新解释旧状态吗？答：不能想当然；要看 Run 捕获的图/配置身份与运行时兼容检查，恢复必须尊重原任务契约。

## 对照阅读与验证

大文件并行的阅读顺序：fanout_composition 分预算/分批 → compose_document_batch 执行独立 Composer → join_composition 按原顺序合并 → artifact_quality_gate 检查来源/产物 → assess_progress 决定继续还是交付。注册表在 agent_runtime.py L7052，Graph 条件边在 L7085。Go 进程池在 [internal/agent/python_pool.go](/Users/windcry1/Documents/ai_companion/internal/agent/python_pool.go)，调度器在 [internal/agent/dispatcher.go](/Users/windcry1/Documents/ai_companion/internal/agent/dispatcher.go)。相关测试入口：[workers/python/tests/test_agent_runtime.py](/Users/windcry1/Documents/ai_companion/workers/python/tests/test_agent_runtime.py)、[internal/agent/python_executor_test.go](/Users/windcry1/Documents/ai_companion/internal/agent/python_executor_test.go)、[internal/agent/dispatcher_test.go](/Users/windcry1/Documents/ai_companion/internal/agent/dispatcher_test.go)。

[返回目录](README.md) · [下一章：完整案例与实验](12-worked-cases-and-labs.md)
