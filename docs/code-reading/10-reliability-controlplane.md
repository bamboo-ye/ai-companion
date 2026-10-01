# 10 故障恢复、降级与配置发布

[阅读总入口](README.md)

行为例子：Worker 重启、Kafka 重复投递、模型异常、管理员发布新 Agent 配置。持久化任务负责可恢复状态；传输层、可靠性策略和配置控制面分别承担不同职责。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 先学会承认网络中的不确定性

发出请求后没收到回答，可能是对方没收到，也可能是对方已经完成但响应丢了。Outbox、Inbox、业务幂等和执行权校验分别减少不同故障窗口，不能靠一个“重试”词语概括全部保证。

至少一次表示消息可能重复，目标是不因可恢复故障轻易漏掉；去重表示识别同一个事件；业务幂等表示同一逻辑动作重复处理后不会多产生业务结果。这三者相关但不相同。

降级是资源紧张时调整可用策略，发布是让新配置经过验证和审批后生效。它们都改变运行行为，因此需要可观察的版本/状态；不能仅靠修改一个全局变量并假设所有进程同步知道。

## Outbox Relay 怎样投递并重试

源码：[internal/eventbus/relay.go:33](/Users/windcry1/Documents/ai_companion/internal/eventbus/relay.go:33)

<a id="symbols-10-1"></a>

### 函数与变量详解

函数/类型声明：[`RunOnce`](/Users/windcry1/Documents/ai_companion/internal/eventbus/relay.go:33)。

**职责、输入输出与影响：** eventbus.Relay.RunOnce 返回 (本批领取事件数 int, 最后错误 error)。事件已发布不等于已成功标记数据库；返回数量不是成功投递数。

**调用关系：** Relay 循环调用；ClaimOutboxEvents → publisher.Publish → MarkOutboxPublished；失败走 FailOutboxEvent。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `r` | *Relay | 方法接收者 | 持有 publisher、store、workerID、lease、batchSize、maxAttempts。 |
| `events / event` | []eventbus.Event / eventbus.Event | ClaimOutboxEvents | 已经按租约领取的一批；event.Attempts 决定重试/死信。 |
| `ack` | eventbus.PublishAck | publisher.Publish 成功返回 | 含 Topic/Partition/Offset，供 MarkOutboxPublished 保存，不是数据库事务 Commit。 |
| `publishErr / lastErr` | error / error | 当前发送或标记错误 / 本批最后错误 | 后续事件仍继续处理；nil 并不是对所有外部最终送达的证明。 |
| `message / dead` | string / bool | 错误文本截到 1024 字节 / 次数是否到顶 | 前者用于记录，后者决定死信或安排下次 available_at。 |

### 源码与逐行解释

```text
  33 │ func (r *Relay) RunOnce(ctx context.Context) (int, error) {
  34 │ 	events, err := r.store.ClaimOutboxEvents(ctx, r.workerID, r.now().UTC(), r.lease, r.batchSize)
  35 │ 	if errors.Is(err, ErrNoEvents) {
  36 │ 		return 0, nil
  37 │ 	}
  38 │ 	if err != nil {
  39 │ 		return 0, err
  40 │ 	}
  41 │ 	var lastErr error
  42 │ 	for _, event := range events {
  43 │ 		ack, publishErr := r.publisher.Publish(ctx, event)
  44 │ 		now := r.now().UTC()
  45 │ 		if publishErr == nil {
  46 │ 			publishErr = r.store.MarkOutboxPublished(ctx, event.ID, r.workerID, ack, now)
  47 │ 		}
  48 │ 		if publishErr == nil {
  49 │ 			continue
  50 │ 		}
```

- **L33–L34**：按 workerID/lease 领取一批待投递 Outbox；它们由业务事务先保存。
- **L35–L37**：没有事件返回 0,nil，属于正常空闲。
- **L38–L40**：数据库读取异常返回错误。
- **L41–L43**：逐条发布，Publisher 可代表 Kafka 或数据库派发适配器。
- **L44–L47**：发布成功还需 MarkOutboxPublished；外部发送成功但数据库标记失败仍视为本次失败。
- **L48–L50**：发布及标记都成功才继续下一条。

```text
  51 │ 		lastErr = publishErr
  52 │ 		message := publishErr.Error()
  53 │ 		if len(message) > 1024 {
  54 │ 			message = message[:1024]
  55 │ 		}
  56 │ 		dead := event.Attempts >= r.maxAttempts
  57 │ 		_ = r.store.FailOutboxEvent(ctx, event.ID, r.workerID, strings.TrimSpace(message), now.Add(retryDelay(event.Attempts)), dead)
  58 │ 	}
  59 │ 	return len(events), lastErr
  60 │ }
```

- **L51–L55**：保留错误并截断错误消息长度，避免异常内容无限增长。
- **L56–L57**：超过最大 attempts 标 dead，否则计算下次可投递时间；FailOutboxEvent 的错误被忽略，是尽力记录。
- **L58–L60**：返回本批领取数与最后一次错误。


这意味着至少一次投递：外部 Publish 成功而 Mark 失败可能导致重发。消费者及具体业务的幂等是必要条件。

## Inbox 去重与业务幂等的关系

源码：[internal/eventbus/consumer.go:196](/Users/windcry1/Documents/ai_companion/internal/eventbus/consumer.go:196)

<a id="symbols-10-2"></a>

### 函数与变量详解

函数/类型声明：[`RunOnce`](/Users/windcry1/Documents/ai_companion/internal/eventbus/consumer.go:196)。

**职责、输入输出与影响：** ConsumerRunner.RunOnce 返回 error。拉取一次事件，处理毒消息/去重，执行业务后记录 Inbox，再确认消费；不返回“严格执行一次”的证明。

**调用关系：** 消费者循环调用；Poll → InboxEventExists → processor.Process → RecordInboxEvent → consumer.Commit。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `r` | *ConsumerRunner | 消费实例 | 不同于 Relay；r.name 是消费者去重命名空间。 |
| `message` | eventbus.KafkaMessage | consumer.Poll | 含 Event 及消息系统定位信息；不是聊天 Message。 |
| `poison` | PoisonMessageError | 解析失败或事件 ID 非 UUID | 先记录毒消息再确认，不能把坏载荷交普通业务。 |
| `seen` | bool | InboxEventExists(ctx,r.name,eventID) | true 跳过业务处理；同事件不同消费者名称可有不同消费记录。 |
| `eventCtx / restored` | context.Context / context.Context | ctx 或恢复的 trace 上下文 | 传递链路信息；AggregateType 以 _run 结尾时附 Run ID。 |
| `err` | error | 处理、记录 Inbox 或确认失败 | 前两步存在间隙，RecordInboxEvent 失败时业务可能已经发生。 |

### 源码与逐行解释

```text
 196 │ func (r *ConsumerRunner) RunOnce(ctx context.Context) error {
 197 │ 	message, err := r.consumer.Poll(ctx)
 198 │ 	if err != nil {
 199 │ 		var poison PoisonMessageError
 200 │ 		if errors.As(err, &poison) {
 201 │ 			r.report(err)
 202 │ 			if recordErr := r.recordPoison(ctx, poison); recordErr != nil {
 203 │ 				return recordErr
 204 │ 			}
 205 │ 			return r.consumer.Commit(ctx, poison.Message)
 206 │ 		}
 207 │ 		return err
 208 │ 	}
```

- **L196–L198**：从消费者 Poll 一条消息。
- **L199–L206**：若是 PoisonMessage，先持久化坏消息证据，再提交 Offset 避免同一坏消息永远堵住分区。
- **L207–L208**：普通 Poll 错误返回，交外层决定重试。

```text
 209 │ 	if !isUUID(message.Event.ID) {
 210 │ 		poison := PoisonMessageError{Message: message, Cause: fmt.Errorf("invalid Kafka event id %q", message.Event.ID)}
 211 │ 		r.report(poison)
 212 │ 		if err = r.recordPoison(ctx, poison); err != nil {
 213 │ 			return err
 214 │ 		}
 215 │ 		return r.consumer.Commit(ctx, message)
 216 │ 	}
 217 │ 	seen, err := r.store.InboxEventExists(ctx, r.name, message.Event.ID)
 218 │ 	if err != nil {
 219 │ 		return err
 220 │ 	}
 221 │ 	if !seen {
 222 │ 		eventCtx := ctx
 223 │ 		if restored, ok := tracectx.FromPropagation(ctx, message.Event.TraceParent, message.Event.TraceState, message.Event.TraceID); ok {
 224 │ 			eventCtx = tracectx.Child(restored)
 225 │ 		} else {
 226 │ 			eventCtx = tracectx.New(ctx)
 227 │ 		}
```

- **L209–L216**：Event ID 必须为合法 UUID；不合法按 Poison 处理。
- **L217–L220**：按消费者名称+Event ID 查 Inbox；存储不可用时不能假设未处理。
- **L221–L227**：只有未见过的事件才执行；从事件恢复 Trace，无法恢复就新建。

```text
 228 │ 		if strings.HasSuffix(message.Event.AggregateType, "_run") {
 229 │ 			eventCtx = tracectx.WithRunID(eventCtx, message.Event.AggregateID)
 230 │ 		}
 231 │ 		if err = r.processor.Process(eventCtx, message.Event); err != nil {
 232 │ 			return err
 233 │ 		}
 234 │ 		if _, err = r.store.RecordInboxEvent(eventCtx, r.name, message.Event.ID, r.now().UTC()); err != nil {
 235 │ 			return err
 236 │ 		}
 237 │ 	}
 238 │ 	return r.consumer.Commit(ctx, message)
 239 │ }
```

- **L228–L230**：Run 类型事件把 AggregateID 附到 Trace Context，串联诊断。
- **L231–L233**：调用领域 Processor，它通常还会通过租约/幂等键领取真正任务。
- **L234–L237**：业务成功后才记录 Inbox；记录失败返回，未来消息可能重放，所以 Inbox 本身不提供跨业务写的 exactly-once。
- **L238–L239**：最后提交消费 Offset；重复已见事件跳过 Processor 但仍可提交。


## 用连续样本避免降级抖动

源码：[internal/reliability/controller.go:124](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:124)

<a id="symbols-10-3"></a>

### 函数与变量详解

函数/类型声明：[`Observe`](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:124)。

**职责、输入输出与影响：** 输入 Sample 与 now，返回可靠性 Snapshot。加锁更新控制器内存等级/候选计数，并非直接发布数据库配置或取消所有任务。

**调用关系：** 监控采样路径调用；classify 给目标 → 连续样本/最短保持约束 → snapshotLocked 输出当前政策。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `c` | *Controller | 控制器实例 | c.mu 保护 level、candidateCount、changedAt 等共享状态。 |
| `sample` | Sample | 本次监控指标 | QueueLag、OldestJobAge、ModelErrorRate、P95Latency 等决定候选等级。 |
| `target / reason` | Level / string | classify(sample) | 目标等级与解释，不保证本次立即切换。 |
| `candidate / required` | Level / int | 默认 target/EnterSamples | 恢复时只先降一档，required 改 RecoverSamples。 |
| `now / changedAt` | time.Time / 控制器字段 time.Time | 采样时刻 / 最近实际切换时刻 | 恢复前检查 MinDwell，不能把观测时间当最后切换时间。 |
| `candidateCount` | Controller 内 int | 连续同候选累加 | 候选变化时重置为 1，满足 required 才改实际 level。 |

### 源码与逐行解释

```text
 124 │ func (c *Controller) Observe(sample Sample, now time.Time) Snapshot {
 125 │ 	c.mu.Lock()
 126 │ 	defer c.mu.Unlock()
 127 │ 	now = now.UTC()
 128 │ 	target, reason := classify(sample)
 129 │ 	c.last, c.observedAt = sample, now
 130 │ 	if c.changedAt.IsZero() {
 131 │ 		c.changedAt = now
 132 │ 	}
 133 │ 	if target == c.level {
 134 │ 		c.candidate, c.candidateCount, c.reason = c.level, 0, reason
 135 │ 		return c.snapshotLocked()
 136 │ 	}
 137 │ 	candidate, required := target, c.config.EnterSamples
 138 │ 	if target < c.level {
 139 │ 		candidate = c.level - 1
 140 │ 		required = c.config.RecoverSamples
```

- **L124–L126**：Controller 有并发读写，进入观察时锁定状态。
- **L127–L129**：转 UTC，依据 sample 分类目标等级，保存本次采样。
- **L130–L132**：首次记录当前级别起点。
- **L133–L136**：目标等级未变时清候选计数并返回当前快照。
- **L137**：变差时使用 EnterSamples 连续样本阈值。
- **L138–L140**：恢复时只下降一个等级，并使用 RecoverSamples，恢复更谨慎。

```text
 141 │ 		if now.Sub(c.changedAt) < c.config.MinDwell {
 142 │ 			c.candidate, c.candidateCount = candidate, 0
 143 │ 			return c.snapshotLocked()
 144 │ 		}
 145 │ 	}
 146 │ 	if c.candidate != candidate {
 147 │ 		c.candidate, c.candidateCount = candidate, 1
 148 │ 	} else {
 149 │ 		c.candidateCount++
 150 │ 	}
 151 │ 	if c.candidateCount >= required {
 152 │ 		c.level, c.reason, c.changedAt = candidate, reason, now
 153 │ 		c.candidate, c.candidateCount = c.level, 0
 154 │ 	}
 155 │ 	return c.snapshotLocked()
 156 │ }
```

- **L141–L145**：当前级别驻留时间不够则不恢复，重置候选计数。
- **L146–L150**：候选级别变化从 1 计数，相同候选持续累加。
- **L151–L154**：满足连续样本阈值才更新真正 level/reason/changedAt，并清计数。
- **L155–L156**：返回反映当前与候选状态的快照。


## 等级映射为具体执行策略

源码：[internal/reliability/controller.go:183](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:183)

<a id="symbols-10-4"></a>

### 函数与变量详解

函数/类型声明：[`classify`](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:183)、[`policyFor`](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:196)。

**职责、输入输出与影响：** classify(Sample) 返回 (Level,原因 string)，policyFor(Level) 返回 Policy。均为纯映射函数，无数据库/模型调用；策略还要由消费者实际应用。

**调用关系：** Observe 调用 classify；快照构造使用 policyFor，把 L0–L3 转为具体开关。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `sample.ModelErrorRate` | float64 | 采样错误比例 | .80/.50/.20 等阈值用于分级，不是累计错误次数。 |
| `sample.OldestJobAge / sample.P95Latency` | time.Duration | 最老任务等待 / 延迟分位 | 前者与后者含义不同，不能都解释为一个请求耗时。 |
| `sample.QueueLag` | int | 积压度量 | 阈值决定压力等级，不是总历史消息量。 |
| `level` | Level，底层 int | 当前等级输入 | L3 保护最强；分级判断从严重到轻，先命中先返回。 |
| `UseFullRAG / ExtractMemory / AcceptOnly` | Policy 的 bool 字段 | policyFor 返回结构 | 分别控制全量检索、记忆抽取与仅接受，不是所有路径自动生效的魔法全局开关。 |
| `PreferredModelClass / LongSkillsQueued` | Policy 的 string / bool | 等级映射 | 表达模型类别偏好与长工具排队政策，不直接指定提供商密钥。 |

### 源码与逐行解释

```text
 183 │ func classify(sample Sample) (Level, string) {
 184 │ 	if sample.ModelErrorRate >= 0.80 || sample.OldestJobAge >= 2*time.Minute {
 185 │ 		return L3, "dependency unavailable or oldest job exceeds protection threshold"
 186 │ 	}
 187 │ 	if sample.QueueLag >= 500 || sample.OldestJobAge >= time.Minute || sample.P95Latency >= 10*time.Second || sample.ModelErrorRate >= 0.50 {
 188 │ 		return L2, "queue or dependency signals exceed heavy-degradation threshold"
 189 │ 	}
 190 │ 	if sample.QueueLag >= 100 || sample.OldestJobAge >= 15*time.Second || sample.P95Latency >= 3*time.Second || sample.ModelErrorRate >= 0.20 {
 191 │ 		return L1, "queue or dependency signals exceed light-degradation threshold"
 192 │ 	}
 193 │ 	return L0, "signals healthy"
 194 │ }
 195 │
 196 │ func policyFor(level Level) Policy {
 197 │ 	switch level {
 198 │ 	case L1:
 199 │ 		return Policy{UseFullRAG: false, ExtractMemory: false, PreferredModelClass: "small", LongSkillsQueued: true}
 200 │ 	case L2:
 201 │ 		return Policy{UseFullRAG: false, ExtractMemory: false, PreferredModelClass: "low_latency", LongSkillsQueued: true}
```

- **L183–L186**：模型错误率达到 .80 或最老任务达到 2 分钟，进入 L3 候选。
- **L187–L189**：队列>=500、老任务>=1分钟、P95>=10秒或错误率>=.50，进入 L2 候选。
- **L190–L192**：更低一档阈值进入 L1 候选。
- **L193–L195**：全部健康则 L0；classify 给的是候选，上一节 Observe 决定是否真正切换。
- **L196–L199**：L1 关闭完整 RAG 与记忆提取，模型偏好 small。
- **L200–L201**：L2 模型偏好 low_latency，长工具继续排队。

```text
 202 │ 	case L3:
 203 │ 		return Policy{UseFullRAG: false, ExtractMemory: false, PreferredModelClass: "local_only", LongSkillsQueued: true, AcceptOnly: true}
 204 │ 	default:
 205 │ 		return Policy{UseFullRAG: true, ExtractMemory: true, PreferredModelClass: "primary", LongSkillsQueued: true}
 206 │ 	}
```

- **L202–L203**：L3 偏好 local_only 并 AcceptOnly；是否生效还要看具体调用方消费这些字段。
- **L204–L206**：默认 L0 允许完整 RAG/提取，主模型偏好 primary。


不能只看 Policy 字段就认定所有路径全都自动降级。普通 conversation.Send 读取 AcceptOnly，BuildContext 读取 UseFullRAG；Agent 还有自己的模型预算/重试策略，需沿实际调用点确认。

## 新配置以不可变草稿版本创建

源码：[internal/controlplane/service.go:161](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:161)

<a id="symbols-10-5"></a>

### 函数与变量详解

函数/类型声明：[`Create`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:161)。

**职责、输入输出与影响：** controlplane.Service.Create 输入 CreateInput，返回 (Version,error)。验证载荷并保存不可变草稿配置；不是 character.Create，也不是直接让线上生效。

**调用关系：** 控制面创建入口 → normalizeKind/normalizeKey → validatePayload → fingerprint → CreateConfigVersion。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | controlplane.CreateInput | 配置种类、键、基线、载荷、操作者、原因 | Kind/Key 规范化；Actor/Reason 是审计信息。 |
| `payload` | 验证后的 JSON 配置载荷 | validatePayload 返回 | 可能规范化后的内容，随后用于存储与 fingerprint。 |
| `versionID` | string | id.New() | 配置版本记录 ID，区别于用于人读的版本号。 |
| `item` | Version | 组装 StatusDraft 记录 | 含 SchemaVersion、BaseVersion、Fingerprint、CreatedBy 等。 |
| `now` | time.Time | s.now().UTC() | 创建/更新时间，不是部署完成时刻。 |

### 源码与逐行解释

```text
 161 │ func (s *Service) Create(ctx context.Context, input CreateInput) (Version, error) {
 162 │ 	input.Kind, input.Key = normalizeKind(input.Kind), normalizeKey(input.Key)
 163 │ 	input.Actor, input.Reason = strings.TrimSpace(input.Actor), strings.TrimSpace(input.Reason)
 164 │ 	if input.Kind == "" || !validKey(input.Key) || input.BaseVersion < 0 || input.Actor == "" || input.Reason == "" || len([]rune(input.Reason)) > 512 {
 165 │ 		return Version{}, ErrValidation
 166 │ 	}
 167 │ 	payload, err := s.validatePayload(ctx, input.Kind, input.Key, input.Payload)
 168 │ 	if err != nil {
 169 │ 		return Version{}, err
 170 │ 	}
 171 │ 	versionID, err := id.New()
 172 │ 	if err != nil {
 173 │ 		return Version{}, err
 174 │ 	}
 175 │ 	now := s.now().UTC()
 176 │ 	item := Version{
 177 │ 		ID: versionID, Kind: input.Kind, Key: input.Key, BaseVersion: input.BaseVersion,
 178 │ 		SchemaVersion: schemaVersion(input.Kind), Status: StatusDraft, Payload: payload,
 179 │ 		Fingerprint: fingerprint(input.Kind, input.Key, payload), Reason: input.Reason,
 180 │ 		CreatedBy: input.Actor, CreatedAt: now, UpdatedAt: now,
```

- **L161–L166**：统一配置类型/键，要求合法基线版本、操作者和修改原因。
- **L167–L170**：按配置类型验证 Payload；不同 billing/model/agent/prompt 有不同契约。
- **L171–L174**：创建 Version ID。
- **L175–L180**：保存 SchemaVersion、draft 状态、规范化 Payload、内容 Fingerprint 和作者时间信息。

```text
 181 │ 	}
 182 │ 	return s.store.CreateConfigVersion(ctx, item)
 183 │ }
```

- **L181–L183**：交 Store 建立不可变版本，后续状态变化不代表随意原地修改 Payload。


## 验证、提交与发布门禁

源码：[internal/controlplane/service.go:185](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:185)

<a id="symbols-10-6"></a>

### 函数与变量详解

函数/类型声明：[`Validate`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:185)、[`Submit`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:199)、[`Publish`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:221)。

**职责、输入输出与影响：** Validate/Submit 返回 (Version,error)，Publish 返回 (Version,Deployment,error)。逐阶段转状态，Agent Definition 还检查评测和替换发布灰度；会写控制面记录，但不证明所有运行实例已同步。

**调用关系：** 管理流程先 Validate 再 Submit 再 Publish；TransitionConfigVersion、LatestPassingAgentEvaluationRun、PromoteAgentRollout/PublishConfigVersion 执行门禁与保存。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `versionID / actor` | string / string | 目标版本 / 当前操作者 | Publish 要求 actor 与 CreatedBy、SubmittedBy 不同，落实职责分离。 |
| `item` | Version | s.Get(versionID) | Status 必须符合当前阶段，Fingerprint 绑定评测与灰度到具体载荷。 |
| `rollout` | AgentRollout | 初始零值，替换已有发布时查询 | 有 ID 才走 Promote；要 ready 且 pass。 |
| `deployments / deployment` | []Deployment / Deployment | 当前部署列表 / 循环项或最终发布结果 | 用于判断是否替换同 Key 的旧版本；同名在循环与返回局部中语境不同。 |
| `published` | Version | PromoteAgentRollout 返回 | 已发布的新配置记录，不是字符串日志。 |
| `promoteErr / activeErr` | error / error | 晋级操作 / 读取活动部署 | 分别说明发布失败阶段，不能忽略后还说已上线。 |

### 源码与逐行解释

```text
 185 │ func (s *Service) Validate(ctx context.Context, versionID, actor string) (Version, error) {
 186 │ 	item, err := s.Get(ctx, versionID)
 187 │ 	if err != nil {
 188 │ 		return Version{}, err
 189 │ 	}
 190 │ 	if item.Status != StatusDraft {
 191 │ 		return Version{}, ErrConflict
 192 │ 	}
 193 │ 	if _, err = s.validatePayload(ctx, item.Kind, item.Key, item.Payload); err != nil {
 194 │ 		return Version{}, err
 195 │ 	}
 196 │ 	return s.store.TransitionConfigVersion(ctx, item.ID, StatusDraft, StatusValidated, strings.TrimSpace(actor), s.now().UTC())
 197 │ }
 198 │
 199 │ func (s *Service) Submit(ctx context.Context, versionID, actor string) (Version, error) {
 200 │ 	if strings.TrimSpace(actor) == "" {
 201 │ 		return Version{}, ErrValidation
 202 │ 	}
```

- **L185–L189**：Validate 先读取指定配置版本。
- **L190–L192**：只有 draft 可进入校验状态。
- **L193–L197**：再次验证 Payload，然后以 draft→validated 条件更新。
- **L198–L202**：Submit 要求操作者身份。

```text
 203 │ 	item, err := s.Get(ctx, versionID)
 204 │ 	if err != nil {
 205 │ 		return Version{}, err
 206 │ 	}
 207 │ 	if item.Status != StatusValidated {
 208 │ 		return Version{}, ErrConflict
 209 │ 	}
 210 │ 	if item.Kind == KindAgentDefinition {
 211 │ 		if _, err = s.store.LatestPassingAgentEvaluationRun(ctx, item.ID, item.Fingerprint); err != nil {
 212 │ 			if errors.Is(err, ErrNotFound) {
 213 │ 				return Version{}, ErrEvaluation
 214 │ 			}
 215 │ 			return Version{}, err
 216 │ 		}
 217 │ 	}
 218 │ 	return s.store.TransitionConfigVersion(ctx, item.ID, StatusValidated, StatusSubmitted, strings.TrimSpace(actor), s.now().UTC())
 219 │ }
```

- **L203–L206**：读取版本。
- **L207–L209**：只允许 validated 提交。
- **L210–L217**：Agent Definition 必须存在匹配本版本 ID 和 Fingerprint 的通过评测；旧版本通过报告不能替代。
- **L218–L219**：条件转 submitted。

```text
 220 │
 221 │ func (s *Service) Publish(ctx context.Context, versionID, actor string) (Version, Deployment, error) {
 222 │ 	actor = strings.TrimSpace(actor)
 223 │ 	item, err := s.Get(ctx, versionID)
 224 │ 	if err != nil {
 225 │ 		return Version{}, Deployment{}, err
 226 │ 	}
 227 │ 	if item.Status != StatusSubmitted {
 228 │ 		return Version{}, Deployment{}, ErrConflict
 229 │ 	}
 230 │ 	if actor == "" || actor == item.CreatedBy || actor == item.SubmittedBy {
 231 │ 		return Version{}, Deployment{}, ErrApproval
 232 │ 	}
 233 │ 	if _, err = s.validatePayload(ctx, item.Kind, item.Key, item.Payload); err != nil {
 234 │ 		return Version{}, Deployment{}, err
 235 │ 	}
```

- **L220–L226**：Publish 读取版本并规范化发布者。
- **L227–L229**：只有 submitted 可发布。
- **L230–L232**：发布者不能为空，也不能等于创建者或提交者；代码在此实现职责分离。
- **L233–L235**：发布前重新校验 Payload，防止依赖变化使旧验证失效。

```text
 236 │ 	var rollout AgentRollout
 237 │ 	if item.Kind == KindAgentDefinition {
 238 │ 		if _, err = s.store.LatestPassingAgentEvaluationRun(ctx, item.ID, item.Fingerprint); err != nil {
 239 │ 			if errors.Is(err, ErrNotFound) {
 240 │ 				return Version{}, Deployment{}, ErrEvaluation
 241 │ 			}
 242 │ 			return Version{}, Deployment{}, err
 243 │ 		}
 244 │ 		if err = s.validateAgentActivation(ctx, item); err != nil {
 245 │ 			return Version{}, Deployment{}, err
 246 │ 		}
 247 │ 		deployments, activeErr := s.Active(ctx, KindAgentDefinition)
 248 │ 		if activeErr != nil {
 249 │ 			return Version{}, Deployment{}, activeErr
 250 │ 		}
 251 │ 		for _, deployment := range deployments {
 252 │ 			if deployment.Key != item.Key || deployment.Version.ID == item.ID {
 253 │ 				continue
 254 │ 			}
```

- **L236–L243**：若为 Agent，再要求当前精确版本的评测通过。
- **L244–L246**：validateAgentActivation 检查模型/模块等运行依赖。
- **L247–L250**：读取当前活动 Agent 部署，判断是否替换已有版本。
- **L251–L254**：跳过不同 key 或相同版本部署。

```text
 255 │ 			rollout, err = s.store.LatestAgentRolloutForVersion(ctx, item.ID, item.Fingerprint)
 256 │ 			if err != nil {
 257 │ 				if errors.Is(err, ErrNotFound) {
 258 │ 					return Version{}, Deployment{}, ErrRollout
 259 │ 				}
 260 │ 				return Version{}, Deployment{}, err
 261 │ 			}
 262 │ 			if rollout.Status != AgentRolloutStatusReady || rollout.Decision != AgentRolloutDecisionPass {
 263 │ 				return Version{}, Deployment{}, ErrRollout
 264 │ 			}
 265 │ 			break
 266 │ 		}
 267 │ 	}
 268 │ 	if rollout.ID != "" {
 269 │ 		published, deployment, _, promoteErr := s.store.PromoteAgentRollout(
 270 │ 			ctx, rollout.ID, rollout.Revision, s.environment, actor, s.now().UTC(),
 271 │ 		)
 272 │ 		return published, deployment, promoteErr
 273 │ 	}
```

- **L255–L261**：替换已有版本时查匹配本版本/指纹的最近灰度结果；缺失或存储失败拒绝。
- **L262–L267**：灰度必须 Ready 且 Decision=Pass，满足后退出查找。
- **L268–L273**：已有合格 rollout 时，带 rollout revision 原子提升为正式部署。

```text
 274 │ 	return s.store.PublishConfigVersion(ctx, item.ID, s.environment, actor, s.now().UTC())
 275 │ }
```

- **L274–L275**：无需晋升既有 rollout 的情形走普通 PublishConfigVersion，例如首次部署。


## 把代码连成一次真实行为

### Outbox/Inbox 的两个故障间隙

| 中断位置 | 重启后可能发生什么 | 代码读者应该找的保护 |
|---|---|---|
| 业务事务提交后，尚未 Publish | Outbox 仍待处理 | Relay 再领取并发布 |
| Publish 成功，尚未标记 Outbox 已发 | 同一事件再发布 | 消费端去重与业务幂等 |
| 消费业务成功，尚未记录 Inbox | 消费业务可能再执行 | 领域内部幂等键/唯一约束/状态条件 |
| Inbox 已记录，尚未 Ack | 再收到消息但可识别已消费 | 去重后确认消息 |

所以不能把“有 Inbox 表”直接翻译成“外部副作用严格执行一次”。若业务写入、Inbox 记录不在同一事务，间隙仍存在；若业务是发邮件，数据库事务也不能回滚收件箱里已经收到的邮件。

### 用连续样本理解降级迟滞

迟滞是进入和退出使用不同条件/等待要求，避免状态在边界上来回跳。本实现观察连续样本：进入需连续 3 次候选，恢复需连续 5 次，并有最短保持时间。单个错误尖峰不等于立刻永久降级；一次恢复样本也不能立刻证明服务稳定。

L1/L2/L3 映射为具体策略，再由使用方应用。例如某路径少用记忆或只接受排队，不代表全仓库每种 Agent/Skill 都自动应用同一策略。看到策略结构要继续找谁读取它。

### 发布配置与运行快照之间的关系

Create 产生不可变版本 → Validate 检查结构与规则 → Submit 记录提交依据/评测指纹 → Publish 执行职责分离与门禁 → 运行实例同步并报告实际版本。首次发布与替换已有发布的灰度要求不同，要按分支看。

一个已经接受的 Agent Run 捕获旧配置快照；新发布不意味着旧 Run 的历史输入、模型设置立即被改写。版本让运行可以追溯，也让排障能问“这个实例当时实际用的是哪一版”，而不是只看管理页面当前最新版。

### 自测与答案

问：有完整发布代码，能证明当前生产已经成功上线吗？答：不能；源码说明能力，部署状态与评测证据需另行核验。

问：重复事件被 Inbox 过滤了，能否推导历史上从未重复执行过业务？答：不能；还要检查业务完成到记录 Inbox 之间的窗口及业务幂等。

## 对照阅读与验证

运行时同步：[cmd/api/main.go](/Users/windcry1/Documents/ai_companion/cmd/api/main.go) 定时调用 SyncRuntimeConfiguration；Agent Worker 观察模型配置，每个实例上报实际 Version/Fingerprint。日志、告警、成本分析分别读 internal/opslog、incident、performance；发布评测和灰度深入 [internal/controlplane/agent_evaluation.go](/Users/windcry1/Documents/ai_companion/internal/controlplane/agent_evaluation.go)、agent_rollout.go。这些是工程/内部发布能力，不自动证明当前外部生产环境已经上线或通过所有质量门禁。

[返回目录](README.md) · [下一章：深入 Python：模型调用怎样驱动图](11-python-model-graph.md)
