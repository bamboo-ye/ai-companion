# 08 Skill 执行与文件交付

[阅读总入口](README.md)

行为例子：工作助手生成 PPT。Agent 负责选择能力、准备内容和检查结果；Skill Run 是真正执行生成工具并保存文件的任务。阅读时分别跟踪 Agent Run ID 和 Skill Run ID。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### Agent 与 Skill 是不同粒度

Agent 负责围绕用户目标决定下一步、等待和整合结果；Skill 是一次有明确输入、算法、输出和预算的工具执行。一个 Agent Run 可以调用多个 Skill Run；Skill 自身也有确认、排队、领取、执行和交付状态。

Schema 描述输入/输出结构，例如需要哪些字段、字段类型是什么。Manifest 描述工具版本、能力与限制。Schema 校验通过只说明结构符合契约，不代表结论正确、数据有权限或文件视觉质量合格。

接口里返回的“文件信息”也不是文件字节本身。通常会记录名称、媒体类型、大小、哈希和读取所需标识；下载入口仍应按具体权限路径检查，不是看到一个路径字符串就可以跳过鉴权。

## 接受一次版本化 Skill 任务

源码：[internal/skill/service.go:412](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:412)

<a id="symbols-8-1"></a>

### 函数与变量详解

函数/类型声明：[`start`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:412)。

**职责、输入输出与影响：** skill.Service.start 输入工具名、createKey、参数 map 与可选聊天来源，返回 (skill.Run, 是否新建 bool, error)。校验并创建任务；可等待确认、排队，或直接执行。

**调用关系：** Start/会话启动包装调用；definitionForUser → validateObject → FindSkillRunByCreateKey → CreateSkillRun → 可选 execute。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | map[string]any | 工具参数 | 按 Manifest.InputSchema 验证，不是 Agent 的整份 Run 输入。 |
| `definition` | Definition | definitionForUser | 含 Manifest 和 Handler；前者声明契约，后者实际执行算法。 |
| `createKey / existing` | string / Run | 一次逻辑动作键 / 已有任务 | 复用时还检查 SkillName 与聊天来源，不能把同键不同业务视为相同。 |
| `payload` | []byte | json.Marshal(input) | 按 MaxInputBytes 检查大小后保存到 Run.Input。 |
| `status / state` | string / string | 初始 running/execute，再按策略改变 | status 是任务状态，state 是当前步骤；waiting_confirmation/confirm 或 queued/queued。 |
| `run` | skill.Run | 新建并填预算/来源/版本字段 | 不是 agent.Run；Steps/Files 初始化为空，Revision=1、Attempt=1。 |
| `stateName / step` | string / Step | receive、validate、plan 循环 / completedStep | 补已完成的接受阶段步骤，再持久化。 |
| `conversationID / originMessageID` | string / string | 可选聊天关联 | 连接工具结果回原对话；非空时也参与重复键一致性检查。 |

### 源码与逐行解释

```text
 412 │ func (s *Service) start(
 413 │ 	ctx context.Context,
 414 │ 	userID, skillName, createKey string,
 415 │ 	input map[string]any,
 416 │ 	conversationID, originMessageID string,
 417 │ ) (Run, bool, error) {
 418 │ 	if strings.TrimSpace(createKey) == "" || len(createKey) > 191 {
 419 │ 		return Run{}, false, ErrIdempotencyKey
 420 │ 	}
 421 │ 	definition, err := s.definitionForUser(ctx, userID, skillName)
 422 │ 	if err != nil {
 423 │ 		return Run{}, false, err
 424 │ 	}
 425 │ 	if err = validateObject(input, definition.Manifest.InputSchema); err != nil {
 426 │ 		return Run{}, false, err
 427 │ 	}
 428 │ 	if existing, findErr := s.store.FindSkillRunByCreateKey(ctx, userID, createKey); findErr == nil {
 429 │ 		if existing.SkillName != skillName {
 430 │ 			return Run{}, false, ErrConflict
 431 │ 		}
```

- **L412–L417**：内部 start 同时服务独立工具调用和来自会话的调用，后者额外带 conversationID/originMessageID。
- **L418–L420**：创建幂等键必填，最多 191 字节。
- **L421–L424**：读取该用户可使用的 Skill 定义；用户禁用能力时不能继续。
- **L425–L427**：按 Manifest.InputSchema 校验输入，模型生成的参数也必须过关。
- **L428–L431**：先按用户和 createKey 查已有任务；同键若指向不同 Skill 则冲突。

```text
 432 │ 		if conversationID != "" && (existing.ConversationID != conversationID || existing.OriginMessageID != originMessageID) {
 433 │ 			return Run{}, false, ErrConflict
 434 │ 		}
 435 │ 		return existing, false, nil
 436 │ 	} else if !errors.Is(findErr, ErrNotFound) {
 437 │ 		return Run{}, false, findErr
 438 │ 	}
 439 │ 	payload, err := json.Marshal(input)
 440 │ 	if err != nil {
 441 │ 		return Run{}, false, fmt.Errorf("%w: invalid input", ErrValidation)
 442 │ 	}
 443 │ 	if len(payload) > definition.Manifest.MaxInputBytes {
 444 │ 		return Run{}, false, fmt.Errorf("%w: input exceeds skill budget", ErrValidation)
 445 │ 	}
 446 │ 	runID, err := id.New()
 447 │ 	if err != nil {
 448 │ 		return Run{}, false, err
 449 │ 	}
```

- **L432–L438**：会话关联不一致也冲突；同动作重放返回已有 Run、created=false，非 NotFound 查询错误原样返回。
- **L439–L442**：把合法参数编码为 JSON。
- **L443–L445**：输入字节不得超过 Manifest.MaxInputBytes，Schema 合法仍可能因体积超限被拒绝。
- **L446–L449**：生成新的 Skill Run ID。

```text
 450 │ 	now := s.now().UTC()
 451 │ 	status, state := "running", "execute"
 452 │ 	if definition.Manifest.RequiresConfirmation {
 453 │ 		status, state = "waiting_confirmation", "confirm"
 454 │ 	} else if s.shouldQueue(definition.Manifest) {
 455 │ 		status, state = "queued", "queued"
 456 │ 	}
 457 │ 	run := Run{
 458 │ 		ID: runID, UserID: userID, SkillName: definition.Manifest.Name, SkillVersion: definition.Manifest.Version, ExecutionMode: definition.Manifest.ExecutionMode,
 459 │ 		Status: status, CurrentState: state, RiskLevel: definition.Manifest.RiskLevel,
 460 │ 		RequiresConfirmation: definition.Manifest.RequiresConfirmation, Input: payload,
 461 │ 		ConversationID: conversationID, OriginMessageID: originMessageID, CreateKey: createKey,
 462 │ 		Attempt: 1, MaxSteps: definition.Manifest.MaxSteps, TimeoutMS: definition.Manifest.TimeoutMS,
 463 │ 		MaxInputBytes: definition.Manifest.MaxInputBytes, MaxCostMicros: definition.Manifest.MaxCostMicros,
 464 │ 		Revision: 1, Steps: []Step{}, Files: []GeneratedFile{}, CreatedAt: now, UpdatedAt: now, AvailableAt: now,
 465 │ 	}
```

- **L450–L456**：初始可 running/execute；需确认优先 waiting_confirmation/confirm，否则按执行模式选择 queued。
- **L457–L461**：Run 快照保存 Skill 版本、模式、风险、输入、会话和原消息、CreateKey。
- **L462–L465**：将步数、超时、输入大小、成本预算复制到 Run，设置 attempt=1、revision=1 与空步骤/文件列表。

```text
 466 │ 	for _, stateName := range []string{"receive", "validate", "plan"} {
 467 │ 		step, stepErr := completedStep(len(run.Steps)+1, stateName, "", nil, nil, now)
 468 │ 		if stepErr != nil {
 469 │ 			return Run{}, false, stepErr
 470 │ 		}
 471 │ 		run.Steps = append(run.Steps, step)
 472 │ 	}
 473 │ 	if err = s.store.CreateSkillRun(ctx, run); err != nil {
 474 │ 		if errors.Is(err, ErrConflict) {
 475 │ 			existing, reloadErr := s.store.FindSkillRunByCreateKey(ctx, userID, createKey)
 476 │ 			return existing, false, reloadErr
 477 │ 		}
 478 │ 		return Run{}, false, err
 479 │ 	}
 480 │ 	if run.Status == "running" {
 481 │ 		run, err = s.execute(ctx, run, definition)
 482 │ 	}
 483 │ 	return run, true, err
 484 │ }
```

- **L466–L472**：持久化前先记录 receive/validate/plan 三个完成步骤；这些是平台阶段记录，不意味着每个阶段都调用模型。
- **L473–L479**：创建时仍可能与并发请求竞争。唯一键冲突后重查，解决“先查都不存在、同时插入”的竞态。
- **L480–L483**：只有初始 running 的任务立即执行；queued 或 waiting_confirmation 原样返回，created=true 不代表已生成文件。
- **L484**：函数结束。


## 确认任务并使用 revision 更新

源码：[internal/skill/service.go:486](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:486)

<a id="symbols-8-2"></a>

### 函数与变量详解

函数/类型声明：[`Confirm`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:486)。

**职责、输入输出与影响：** skill.Service.Confirm 输入 userID、runID、key，返回 (Run,error)，没有 bool 返回值。确认等待状态，以 revision 保存，再排队或执行；同一确认键可复用结果。

**调用关系：** Skill 确认入口/网关 → GetSkillRun → definitionForUser → SaveSkillRun → 可选 execute。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `run` | skill.Run | 按用户与 Run ID 查库 | 必须 waiting_confirmation 且 RequiresConfirmation，或已经用同 key 确认。 |
| `key / run.ConfirmationKey` | string / string | 本次确认键 / 已存键 | 相同键可返回已有结果，不同键与已有确认冲突。 |
| `definition` | Definition | 当前用户可使用的定义 | 这里实际重新解析定义；不能仅凭 SkillVersion 字段声称本函数从不读取当前注册表。 |
| `expected` | int | 修改前 run.Revision | 保存时传预期旧版本，run.Revision 随本次更新加一。 |
| `step` | Step | completedStep(..., "confirm", ...) | 只保存新增步骤与更新 Run，不重复追加所有历史步骤。 |
| `latest / reloadErr` | Run / error | CAS 冲突后重读 | 若别人已经用同 key 确认，可返回其结果；不是遇到任何冲突都覆盖。 |

### 源码与逐行解释

```text
 486 │ func (s *Service) Confirm(ctx context.Context, userID, runID, key string) (Run, error) {
 487 │ 	if strings.TrimSpace(key) == "" || len(key) > 191 {
 488 │ 		return Run{}, ErrIdempotencyKey
 489 │ 	}
 490 │ 	run, err := s.store.GetSkillRun(ctx, userID, runID)
 491 │ 	if err != nil {
 492 │ 		return Run{}, err
 493 │ 	}
 494 │ 	if run.ConfirmationKey != "" {
 495 │ 		if run.ConfirmationKey == key {
 496 │ 			return run, nil
 497 │ 		}
 498 │ 		return Run{}, ErrConflict
 499 │ 	}
 500 │ 	if run.Status != "waiting_confirmation" || !run.RequiresConfirmation {
 501 │ 		return Run{}, ErrConflict
 502 │ 	}
```

- **L486–L489**：确认幂等键必须合法。
- **L490–L493**：按用户读取 Skill Run。
- **L494–L499**：已有确认键且同键则复用结果，不同键则冲突。
- **L500–L502**：仅 waiting_confirmation 且 RequiresConfirmation 才能继续。

```text
 503 │ 	definition, err := s.definitionForUser(ctx, userID, run.SkillName)
 504 │ 	if err != nil {
 505 │ 		return Run{}, err
 506 │ 	}
 507 │ 	if len(run.Steps)+3 > run.MaxSteps {
 508 │ 		return Run{}, ErrConflict
 509 │ 	}
 510 │ 	expected := run.Revision
 511 │ 	run.ConfirmationKey = key
 512 │ 	run.Status, run.CurrentState, run.UpdatedAt = "running", "execute", s.now().UTC()
 513 │ 	if s.shouldQueue(definition.Manifest) {
 514 │ 		run.Status, run.CurrentState, run.AvailableAt = "queued", "queued", run.UpdatedAt
 515 │ 	}
 516 │ 	step, _ := completedStep(len(run.Steps)+1, "confirm", "", nil, nil, run.UpdatedAt)
 517 │ 	run.Steps = append(run.Steps, step)
 518 │ 	run.Revision++
```

- **L503–L506**：重新核对用户当前是否仍可使用该能力。
- **L507–L509**：为确认、执行和交付留足步骤预算。
- **L510**：保存旧 revision 作为条件写入依据。
- **L511–L512**：记录确认键，准备 running/execute。
- **L513–L515**：长任务改为 queued，并设置可领取时间。
- **L516–L518**：追加 confirm 步骤，内存对象 revision 加 1。

```text
 519 │ 	if err = s.store.SaveSkillRun(ctx, run, expected, []Step{step}, nil); err != nil {
 520 │ 		if errors.Is(err, ErrConflict) {
 521 │ 			latest, reloadErr := s.store.GetSkillRun(ctx, userID, runID)
 522 │ 			if reloadErr == nil && latest.ConfirmationKey == key {
 523 │ 				return latest, nil
 524 │ 			}
 525 │ 		}
 526 │ 		return Run{}, err
 527 │ 	}
 528 │ 	if run.Status == "queued" {
 529 │ 		return run, nil
 530 │ 	}
 531 │ 	return s.execute(ctx, run, definition)
```

- **L519–L527**：SaveSkillRun 以 expected 做 CAS。若并发相同确认先成功，重读相同 ConfirmationKey 后返回已有结果。
- **L528–L531**：queued 立即返回，否则同步走本服务 execute；HTTP 上层仍需按实际返回状态判断完成。


## Worker 执行时定时续租

源码：[internal/skill/runner.go:48](/Users/windcry1/Documents/ai_companion/internal/skill/runner.go:48)

<a id="symbols-8-3"></a>

### 函数与变量详解

函数/类型声明：[`execute`](/Users/windcry1/Documents/ai_companion/internal/skill/runner.go:48)。

**职责、输入输出与影响：** skill.Runner.execute 输入已领取 Run，返回 (已处理 bool,error)。并发续租与执行，续租失败取消执行；本段由 Runner 管执行权，不是 Service.execute 的算法逻辑。

**调用关系：** Runner 领取路径进入；RenewLease 与 ExecuteClaimed 并行，最终返回执行/续租错误。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `r` | *Runner | 方法接收者 | r.workerID/r.lease/r.renewInterval 规定此 Worker 的续租身份与节奏。 |
| `executeCtx / cancel` | context.Context / CancelFunc | WithCancel(ctx) | 共享给续租与执行，失去租约时触发 cancel。 |
| `done` | chan struct{} | 无缓冲信号通道 | 主执行结束后 close(done)，通知续租循环退出，不传业务内容。 |
| `renewErrors` | chan error，容量 1 | make | 只报告一次续租失败，避免无人接收时发送者卡住。 |
| `ticker` | *time.Ticker | renewInterval | 周期尝试续租，defer Stop 停定时资源。 |
| `executeErr / renewErr` | error / error | ExecuteClaimed / RenewLease | 分别是算法路径和持有执行权路径错误；返回 bool=true 仅说明本任务已处理。 |

### 源码与逐行解释

```text
  48 │ func (r *Runner) execute(ctx context.Context, run Run) (bool, error) {
  49 │ 	executeCtx, cancel := context.WithCancel(ctx)
  50 │ 	done := make(chan struct{})
  51 │ 	renewErrors := make(chan error, 1)
  52 │ 	go func() {
  53 │ 		ticker := time.NewTicker(r.renewInterval)
  54 │ 		defer ticker.Stop()
  55 │ 		for {
  56 │ 			select {
  57 │ 			case <-done:
  58 │ 				return
  59 │ 			case <-executeCtx.Done():
  60 │ 				return
  61 │ 			case <-ticker.C:
  62 │ 				if renewErr := r.service.RenewLease(executeCtx, run, r.workerID, r.lease); renewErr != nil {
  63 │ 					renewErrors <- renewErr
  64 │ 					cancel()
  65 │ 					return
  66 │ 				}
```

- **L48–L51**：建立执行 Context、正常完成信号和容量为 1 的续租错误 channel。
- **L52–L55**：独立 goroutine 定期续租，退出停 ticker。
- **L56–L60**：任务完成或执行 Context 结束则续租循环退出。
- **L61–L66**：续租失败先记录错误，再取消工具执行；旧 Worker 不应继续持有失效执行权。

```text
  67 │ 			}
  68 │ 		}
  69 │ 	}()
  70 │ 	_, executeErr := r.service.ExecuteClaimed(executeCtx, run, r.workerID)
  71 │ 	close(done)
  72 │ 	cancel()
  73 │ 	select {
  74 │ 	case renewErr := <-renewErrors:
  75 │ 		return true, renewErr
  76 │ 	default:
  77 │ 	}
  78 │ 	if ctx.Err() != nil {
  79 │ 		return true, nil
  80 │ 	}
  81 │ 	return true, executeErr
  82 │ }
```

- **L67–L69**：结束循环分支和 goroutine。
- **L70**：主协程执行已领取任务；不重新创建 Run。
- **L71–L72**：执行返回后关闭完成信号并取消 Context，清理续租工作。
- **L73–L77**：优先返回续租错误；default 使没有错误时不阻塞等待。
- **L78–L81**：外层停止时平滑返回已处理；正常情况返回工具执行错误。
- **L82**：函数结束。


## 执行算法与解释错误

源码：[internal/skill/service.go:861](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:861)

<a id="symbols-8-4"></a>

### 函数与变量详解

函数/类型声明：[`execute`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:861)。

**职责、输入输出与影响：** skill.Service.execute 输入 Run 与 Definition，返回 (Run,error)。解码参数、按期限调用 Handler、审计成本、校验输出；失败可写失败状态，成功接着进入下一源码段保存文件。

**调用关系：** start、Confirm 或 ExecuteClaimed 进入；Handler.Execute → persistModelAuditOutput/modelCostMicros → validateObject → json.Marshal。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `run` | skill.Run 值 | 上游已确认/领取任务 | 清旧 Output，再按 MaxSteps/TimeoutMS/MaxCostMicros 等任务预算检查。 |
| `definition` | Definition | 工具定义 | Handler.Execute 是真正算法入口；Manifest 提供 Schema 与工具名称等。 |
| `input` | map[string]any | Run.Input JSON 解码 | 还原持久化参数，不重读网页表单。 |
| `timeout / executeCtx` | time.Duration / context.Context | TimeoutMS 转毫秒时长 / WithTimeout | 限制本次执行，ctx.Err 与 executeCtx.Err 区分父取消和自身超时。 |
| `started` | time.Time | 执行前 s.now().UTC() | 执行 Step 的起始时刻。 |
| `result / executeErr` | ToolResult / error | definition.Handler.Execute | result.Output 为结构化输出，result.Files 为待保存字节；返回成功还要校验。 |
| `cost / ok` | int64 / bool | modelCostMicros(result.Output) | cost 以微单位计费，ok 表示费用可解析；有成本预算却无有效费用时拒绝。 |
| `output` | []byte | json.Marshal(result.Output) | 验证后的输出序列化结果，与 result.Output 的 map 类型不同。 |

### 源码与逐行解释

```text
 861 │ func (s *Service) execute(ctx context.Context, run Run, definition Definition) (Run, error) {
 862 │ 	run.Output = nil
 863 │ 	if len(run.Steps)+2 > run.MaxSteps {
 864 │ 		return s.fail(ctx, run, definition.Manifest.ToolName, "step_budget_exceeded", errors.New("skill step budget exceeded"))
 865 │ 	}
 866 │ 	var input map[string]any
 867 │ 	if err := json.Unmarshal(run.Input, &input); err != nil {
 868 │ 		return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_input", err)
 869 │ 	}
 870 │ 	timeout := time.Duration(run.TimeoutMS) * time.Millisecond
 871 │ 	executeCtx, cancel := context.WithTimeout(ctx, timeout)
 872 │ 	defer cancel()
 873 │ 	started := s.now().UTC()
 874 │ 	result, executeErr := definition.Handler.Execute(executeCtx, input)
```

- **L861–L865**：清旧 Output，并为 execute/deliver 两步检查剩余步数；超限持久化失败。
- **L866–L869**：从持久化 JSON 恢复输入；坏输入不能进入算法。
- **L870–L873**：按 Run.TimeoutMS 设置子 Context 截止时间，并记录执行起点。
- **L874**：实际工具入口是 definition.Handler.Execute；内置处理器或 PythonOfficeWorker 都实现这个接口。

```text
 875 │ 	if executeErr != nil {
 876 │ 		if ctx.Err() != nil {
 877 │ 			if run.WorkerID == "" {
 878 │ 				code := "tool_cancelled"
 879 │ 				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
 880 │ 					code = "tool_timeout"
 881 │ 				}
 882 │ 				return s.fail(context.Background(), run, definition.Manifest.ToolName, code, executeErr)
 883 │ 			}
 884 │ 			return Run{}, ctx.Err()
 885 │ 		}
 886 │ 		code := "tool_failed"
 887 │ 		if errors.Is(executeCtx.Err(), context.DeadlineExceeded) {
 888 │ 			code = "tool_timeout"
 889 │ 		}
 890 │ 		if failure := toolFailureFromError(executeErr, code); failure.Code != "" {
 891 │ 			code = failure.Code
 892 │ 		}
 893 │ 		return s.fail(ctx, run, definition.Manifest.ToolName, code, executeErr)
 894 │ 	}
```

- **L875–L885**：算法出错且上层已取消时：内联任务可记录取消/超时，Worker 任务交执行/租约控制层处理，不用失效 Context 强写状态。
- **L886–L894**：区分普通工具失败、超时与工具提供的结构化失败码，统一交 s.fail 持久化。

```text
 895 │ 	persistModelAuditOutput(&run, result.Output)
 896 │ 	if run.MaxCostMicros > 0 {
 897 │ 		cost, ok := modelCostMicros(result.Output)
 898 │ 		if !ok {
 899 │ 			return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_model_usage", errors.New("model usage cost is required by the skill budget"))
 900 │ 		}
 901 │ 		if cost > run.MaxCostMicros {
 902 │ 			return s.fail(ctx, run, definition.Manifest.ToolName, "model_cost_exceeded", fmt.Errorf("model cost %d exceeds skill budget %d", cost, run.MaxCostMicros))
 903 │ 		}
 904 │ 	}
 905 │ 	if code, ok := modelOperationError(result.Output); ok {
 906 │ 		return s.fail(ctx, run, definition.Manifest.ToolName, "model_operation_failed", errors.New(code))
 907 │ 	}
 908 │ 	if err := validateObject(result.Output, definition.Manifest.OutputSchema); err != nil {
 909 │ 		return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_output", err)
 910 │ 	}
```

- **L895**：保留模型审计输出，即使后续质量/预算失败也不能丢失已发生的调用证据。
- **L896–L904**：有限成本任务必须返回可识别用量，缺用量或成本超限都失败。这个步骤是结果校验，不能把已发生费用撤销。
- **L905–L907**：工具 Output 内部显式报告模型操作失败时，不因 Go error=nil 而当成功。
- **L908–L910**：输出同样要过 Manifest.OutputSchema。

```text
 911 │ 	output, err := json.Marshal(result.Output)
 912 │ 	if err != nil {
 913 │ 		return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_output", err)
 914 │ 	}
 915 │ 	now := s.now().UTC()
 916 │ 	newFiles := make([]GeneratedFile, 0, len(result.Files))
```

- **L911–L916**：编码输出、确定完成时间，准备新文件列表。


## 文件保存后提交完成状态

源码：[internal/skill/service.go:917](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:917)

<a id="symbols-8-5"></a>

### 函数与变量详解

函数/类型声明：[`execute`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:861)。

**职责、输入输出与影响：** 同一 Service.execute 的交付后半段：写 Blob，组装 GeneratedFile，更新 Run 并以 expected revision 提交。实际成功状态字符串为 succeeded，CurrentState 为 deliver，不是 Agent 的 completed。

**调用关系：** files.Put → SHA-256 → completedStep → SaveSkillRun；最终保存失败时尝试删除此次写入文件。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `newFiles` | []GeneratedFile | 初始空，逐个追加 | 只含本次生成文件的元数据；与 Run.Files 全历史集合区分。 |
| `item` | skill.FileOutput | range result.Files | Name/MediaType 是元数据，Data 是 []byte 文件字节；此处大小检查逐文件进行。 |
| `fileID / storageKey` | string / string | 新 ID / files.Put 返回 | 外部展示/关联身份与内部存储位置不同，不应把 StorageKey 当公开权限。 |
| `digest` | [32]byte | SHA-256(item.Data) | 转十六进制保存完整性摘要，不是文件压缩。 |
| `expected` | int | 写终态前的 run.Revision | CAS 使用旧版本，随后 run.Revision++。 |
| `executeStep / deliverStep / newSteps` | Step / Step / []Step | 执行完成与交付记录 | executeStep 补 CompletedAt；保存时只传新增加的步骤。 |
| `run.Output / run.Status / run.CurrentState` | JSON / string / string | output / succeeded / deliver | 算法成功、文件写入后才提交；SaveSkillRun 失败仍不算终态保存成功。 |
| `run.WorkerID / run.LeaseExpiresAt` | string / *time.Time | 终态时清空 / nil | 释放执行权；不是删除历史运行。 |

### 源码与逐行解释

```text
 917 │ 	for _, item := range result.Files {
 918 │ 		if len([]rune(item.Name)) == 0 || len([]rune(item.Name)) > 255 || item.MediaType == "" {
 919 │ 			return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_file", errors.New("generated file metadata is invalid"))
 920 │ 		}
 921 │ 		if len(item.Data) > definition.Manifest.MaxOutputFileBytes {
 922 │ 			failure := ToolFailure{
 923 │ 				Code: "output_file_too_large", Category: "resource_limit", Phase: "post_execution",
 924 │ 				Message: "生成文件超过该技能允许的大小。", SideEffectState: "unknown",
 925 │ 				SafeDetails: map[string]any{
 926 │ 					"actual_bytes": len(item.Data), "limit_bytes": definition.Manifest.MaxOutputFileBytes,
 927 │ 				},
 928 │ 			}
 929 │ 			return s.fail(ctx, run, definition.Manifest.ToolName, failure.Code, NewToolExecutionError(failure, errors.New("generated file exceeds skill output budget")))
 930 │ 		}
 931 │ 		fileID, idErr := id.New()
 932 │ 		if idErr != nil {
 933 │ 			return s.fail(ctx, run, definition.Manifest.ToolName, "file_id_failed", idErr)
 934 │ 		}
```

- **L917–L920**：逐个文件校验名称长度与媒体类型。
- **L921–L930**：逐文件检查 MaxOutputFileBytes；此字段并非所有文件总大小。超限记录结构化 resource_limit，副作用未知。
- **L931–L934**：每个生成文件拥有独立 ID。

```text
 935 │ 		storageKey, writeErr := s.files.Put(ctx, run.UserID, run.ID, fileID, item.Data)
 936 │ 		if writeErr != nil {
 937 │ 			return s.fail(ctx, run, definition.Manifest.ToolName, "file_write_failed", writeErr)
 938 │ 		}
 939 │ 		digest := sha256.Sum256(item.Data)
 940 │ 		newFiles = append(newFiles, GeneratedFile{ID: fileID, RunID: run.ID, Name: item.Name, MediaType: item.MediaType, SizeBytes: int64(len(item.Data)), SHA256: hex.EncodeToString(digest[:]), StorageKey: storageKey, CreatedAt: now})
 941 │ 	}
 942 │ 	expected := run.Revision
 943 │ 	run.Output, run.Status, run.CurrentState = output, "succeeded", "deliver"
 944 │ 	run.WorkerID, run.LeaseExpiresAt = "", nil
 945 │ 	run.ErrorCode, run.ErrorMessage, run.UpdatedAt, run.CompletedAt = "", "", now, &now
 946 │ 	executeStep, _ := completedStep(len(run.Steps)+1, "execute", definition.Manifest.ToolName, run.Input, output, started)
 947 │ 	executeStep.CompletedAt = &now
 948 │ 	deliverStep, _ := completedStep(len(run.Steps)+2, "deliver", "", nil, output, now)
 949 │ 	newSteps := []Step{executeStep, deliverStep}
 950 │ 	run.Steps = append(run.Steps, newSteps...)
 951 │ 	run.Files = append(run.Files, newFiles...)
 952 │ 	run.DeliveryContent = retryDeliveryContent(run)
 953 │ 	run.Revision++
```

- **L935–L938**：按用户、Run、File ID 保存文件字节，写入失败结束本次任务。
- **L939–L941**：计算文件 SHA-256，记录名称、类型、大小、StorageKey 与时间。
- **L942**：保存旧 revision，用于最终 CAS。
- **L943–L945**：转 succeeded/deliver，清租约和错误，写输出与完成时间。
- **L946–L949**：构造 execute 与 deliver 的步骤记录，执行步保留真实起止时间。
- **L950–L952**：将步骤和文件附到 Run，并生成重试交付内容。
- **L953**：新 revision 加 1。

```text
 954 │ 	if err = s.store.SaveSkillRun(ctx, run, expected, newSteps, newFiles); err != nil {
 955 │ 		for _, file := range newFiles {
 956 │ 			_ = s.files.Delete(ctx, file.StorageKey)
 957 │ 		}
 958 │ 		return Run{}, err
 959 │ 	}
 960 │ 	return run, nil
 961 │ }
```

- **L954–L958**：Store 一次保存终态、步骤与文件元数据；失败则尽力删除本批新文件，避免 CAS 输掉仍暴露重复交付。
- **L959–L961**：返回成功 Run。文件系统与 SQL 仍是分阶段操作，不能声称具备分布式原子事务。


下载经过 /v1/skill-runs/{run_id}/files/{file_id}，由服务按用户/Run 检查访问。消息正文中的 ai-generated-file 标记只是 UI 关联，不是绕过权限的直链。

## 把代码连成一次真实行为

### 从“生成文件”拆成四种完成

| 表面上看像成功的时刻 | 真正完成了什么 | 仍需继续 |
|---|---|---|
| 工具 Prepare completed | 准备阶段已完成，可能已启动异步任务 | 等 Skill Run |
| 算法返回 output/files | 执行器产生候选输出 | Schema、预算、文件字段检查 |
| Blob 写入成功 | 字节进入文件存储 | 元数据和任务终态的条件提交 |
| Skill Run succeeded | 本工具结果按任务规则提交；不要与 Agent 的 completed 混用 | Agent 取回并判断能否完成用户目标 |

假设生成两份文件 F1、F2。源码逐文件检查大小，不是这一段先把所有文件大小求和再检查总包。若需要推断总资源上限，还要看其他预算与输入限制；不能给一个局部 if 添加它没有实现的保证。

执行过程中续租失败会取消执行 Context，因为这个 Worker 可能已失去任务所有权。文件写完后仍要通过版本/状态条件保存结果；迟到结果不能越过 CAS。持久化完成失败时有清理已写文件的路径，但文件系统与 SQL 不是一个原子事务，所以排查应同时看文件和元数据，不能声称没有任何故障窗口。

### 版本记录与预算保存到了哪里

Run 保存 SkillName、SkillVersion、Input，以及 MaxSteps、TimeoutMS、MaxInputBytes、MaxCostMicros 等接受时的值，使运行可追溯并受这些预算限制。但这里没有保存整个 Manifest 和 Handler 的完整不可变快照：Confirm 会重新调用 definitionForUser，ExecuteClaimed 会按 SkillName 从注册表取 Definition。因此不能仅凭 SkillVersion 字段就声称旧运行一定执行历史 Handler/Schema；具体实现和定义读取路径仍须分别核对。这与 Agent Run 捕获模型/Agent 配置快照的机制不同。

失败分类决定“报告错误、重试执行、尝试修复”是否合适。文件名不合法与临时网络断开不是同一类问题；自动修复也必须受步数、时间和输出验证限制。返回一个字符串说“已生成”不会替代真实制品记录。

### 自测与答案

问：把 output JSON 打印出来，是否已经完成文件交付？答：没有。还需真实字节、合法元数据、成功的任务提交以及可访问的交付入口。

问：Schema 校验是不是权限校验？答：不是；一个结构完全合法的 document_id 仍可能属于别的用户。

## 对照阅读与验证

继续跳转：[internal/skill/builtins.go](/Users/windcry1/Documents/ai_companion/internal/skill/builtins.go) 注册能力与 Manifest；office_worker.go 定义 Go/Python 进程协议；[workers/python/src/ai_companion_worker/office_tools.py](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/office_tools.py) 执行文档算法。重试与自动修复分别在 Retry、RepairRetry；工具终态再通过 Agent waiting_tool 恢复整条任务。文件格式实现细节和渲染质量门禁可参照 docs/blog/ppt-generation-vibe-coding 系列。

[返回目录](README.md) · [下一章：团队分享、管理员登录与额度](09-teams-admin-quotas.md)
