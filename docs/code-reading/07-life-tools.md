# 07 生活助手：记账、计划、提醒和导出

[阅读总入口](README.md)

行为例子：“午饭花了 35 元”“明天下午 3 点提醒我开会”“加入今天计划”“导出本月账单”。聊天模型先识别工具，实际业务规则由 ledger/planner 服务执行；页面直接调用这些 API 时复用同一服务。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 把“理解一句话”和“改变账本”拆开

“午饭 32.5 元”首先会被解析成候选：金额、类型、分类、时间以及是否需澄清。候选是待决定的结构化解释；正式 Entry 才是账本中的业务记录。如果金额或时间模糊，应能追问，而不是为了让链路结束随便填值。

金额保存为最小货币单位整数，例如 32.50 元对应 3250 分，可以避免后续汇总不断累积浮点表示误差。但入口当前确实使用 ParseFloat 再乘 100/取整；因此不能声称从输入到输出所有步骤都是精确十进制运算。

时间也有两层：用户所说“今天上午”要在用户时区解释；数据库常用 UTC 时间点保存。相同瞬间在上海可能已是第二天，在别处仍是前一天，日/月边界必须说明使用哪个时区。

## 自然语言解析成待确认账单

源码：[internal/ledger/parser.go:27](/Users/windcry1/Documents/ai_companion/internal/ledger/parser.go:27)

<a id="symbols-7-1"></a>

### 函数与变量详解

函数/类型声明：[`Parse`](/Users/windcry1/Documents/ai_companion/internal/ledger/parser.go:27)。

**职责、输入输出与影响：** 输入 text、timezone、reference time.Time；返回 (Candidate, error)。纯领域解析，不读写数据库；含 NeedsClarification 的候选可以正常返回，之后不能直接确认。

**调用关系：** 账本候选构造路径调用；extractAmount/detectDirection/detectCategory/parseOccurredAt 解析各字段。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `text / timezone` | string / string | 自然语言 / 用户时区 | 正文清理并校验长度；时区用于解释“今天/上午”。 |
| `reference / location` | time.Time / *time.Location | 调用方参考时刻 / LoadLocation | reference.In(location) 是用户当地参考时间，不改真实瞬间。 |
| `result` | Candidate | 初始 pending、Confidence=.25 | 逐字段补充；缺项追加 NeedsClarification，最后可变 needs_clarification。 |
| `amountText / currencyText / hasAmount` | string / string / bool | extractAmount | 字符串匹配结果与是否识别到金额；hasAmount 不代表方向和日期也完整。 |
| `amount` | float64 | strconv.ParseFloat | 验证大于 0 后乘 100 并 Round，转换为 AmountMinor 整数分。 |
| `occurredAt / precision / ok` | time.Time / string / bool | parseOccurredAt | 日期是否解析成功及精度；成功后存 &utc，失败追加 occurred_at 缺项。 |
| `utc` | time.Time | occurredAt.UTC() | 本次局部时间点，其地址保存为可空 OccurredAt。 |

### 源码与逐行解释

```text
  27 │ func Parse(text, timezone string, reference time.Time) (Candidate, error) {
  28 │ 	text = strings.TrimSpace(text)
  29 │ 	if len([]rune(text)) < 2 || len([]rune(text)) > 2000 {
  30 │ 		return Candidate{}, fmt.Errorf("%w: text must contain 2-2000 characters", ErrValidation)
  31 │ 	}
  32 │ 	location, err := time.LoadLocation(timezone)
  33 │ 	if err != nil {
  34 │ 		return Candidate{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
  35 │ 	}
  36 │ 	result := Candidate{RawText: text, Timezone: timezone, Direction: detectDirection(text), Category: detectCategory(text), Merchant: detectMerchant(text), NeedsClarification: []string{}, Status: "pending", Confidence: .25}
  37 │ 	amountText, currencyText, hasAmount := extractAmount(text)
  38 │ 	if hasAmount {
  39 │ 		amount, parseErr := strconv.ParseFloat(amountText, 64)
  40 │ 		if parseErr != nil || amount <= 0 || amount > 1_000_000_000 {
  41 │ 			return Candidate{}, fmt.Errorf("%w: invalid amount", ErrValidation)
  42 │ 		}
  43 │ 		result.AmountMinor = int64(math.Round(amount * 100))
  44 │ 		result.Currency = normalizeCurrency(currencyText)
  45 │ 		result.Confidence += .3
```

- **L27–L31**：清理文本并限制 2–2000 个字符，防止空输入和超大正则工作量。
- **L32–L35**：加载用户时区，无法识别时拒绝。
- **L36**：初始化 Candidate：原文、时区、方向、类别、商户、pending、初始置信度；尚未写正式 Entry。
- **L37**：从原文提取金额和币种文本。
- **L38–L42**：有金额则转浮点并检查正数及最大值。
- **L43**：乘 100 后四舍五入为整数最小金额单位；账本金额存整数，但解析入口仍用了浮点，不能称全链路定点计算。
- **L44–L45**：规范化币种并提高置信度。

```text
  46 │ 	} else {
  47 │ 		result.NeedsClarification = append(result.NeedsClarification, "amount")
  48 │ 	}
  49 │ 	if result.Direction != "" {
  50 │ 		result.Confidence += .15
  51 │ 	} else {
  52 │ 		result.NeedsClarification = append(result.NeedsClarification, "direction")
  53 │ 	}
  54 │ 	if result.Category != "other" {
  55 │ 		result.Confidence += .1
  56 │ 	}
  57 │ 	occurredAt, precision, ok := parseOccurredAt(text, reference.In(location), location)
  58 │ 	if ok {
  59 │ 		utc := occurredAt.UTC()
  60 │ 		result.OccurredAt = &utc
  61 │ 		result.TimePrecision = precision
  62 │ 		result.Confidence += .2
  63 │ 	} else {
  64 │ 		result.NeedsClarification = append(result.NeedsClarification, "occurred_at")
  65 │ 	}
```

- **L46–L48**：缺金额时把 amount 加入待澄清字段。
- **L49–L53**：识别收入/支出则提高置信度，否则要求补 direction。
- **L54–L56**：非 other 类别增加确定度。
- **L57**：以用户本地时区解释时间表达。
- **L58–L62**：时间解析成功后转换 UTC，并保存时间精度。
- **L63–L65**：无法确定发生时间则要求补 occurred_at。

```text
  66 │ 	if result.Confidence > 1 {
  67 │ 		result.Confidence = 1
  68 │ 	}
  69 │ 	if len(result.NeedsClarification) > 0 {
  70 │ 		result.Status = "needs_clarification"
  71 │ 	}
  72 │ 	return result, nil
  73 │ }
```

- **L66–L68**：置信度最多 1。
- **L69–L73**：任何必需字段仍缺失则 needs_clarification，返回 Candidate，不创建正式账目。


## 用户确认后才写正式 Entry

源码：[internal/ledger/service.go:318](/Users/windcry1/Documents/ai_companion/internal/ledger/service.go:318)

<a id="symbols-7-2"></a>

### 函数与变量详解

函数/类型声明：[`Confirm`](/Users/windcry1/Documents/ai_companion/internal/ledger/service.go:318)。

**职责、输入输出与影响：** ledger.Service.Confirm 输入用户、candidateID、idempotencyKey、note，返回 (Entry, 是否新建 bool, error)。候选完整才调用 Store 写正式账目；bool 含义不能套到 Skill.Confirm。

**调用关系：** HTTP 或 Agent 网关提交 → GetCandidate → ConfirmCandidate。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `candidate` | Candidate | 按 userID+candidateID 查库 | Status、OccurredAt、NeedsClarification 决定能否确认。 |
| `idempotencyKey` | string | 调用方同次确认的键 | 去空白并限长度；写 Entry 字段且传 Store 防重复。 |
| `entryID / entry` | string / Entry | 新 ID / 从候选拷字段 | AmountMinor、Currency、Direction 等来自已验证候选，Note 来自本次备注。 |
| `note` | string | 确认输入 | TrimSpace 后存储，不用于识别用户身份。 |
| `now` | time.Time | s.now().UTC() | 正式记录创建/更新时间与候选确认操作时间。 |

### 源码与逐行解释

```text
 318 │ func (s *Service) Confirm(ctx context.Context, userID, candidateID, idempotencyKey, note string) (Entry, bool, error) {
 319 │ 	idempotencyKey = strings.TrimSpace(idempotencyKey)
 320 │ 	if idempotencyKey == "" || len(idempotencyKey) > 191 {
 321 │ 		return Entry{}, false, ErrIdempotencyKey
 322 │ 	}
 323 │ 	candidate, err := s.store.GetCandidate(ctx, userID, candidateID)
 324 │ 	if err != nil {
 325 │ 		return Entry{}, false, err
 326 │ 	}
 327 │ 	if candidate.Status == "needs_clarification" || candidate.OccurredAt == nil || len(candidate.NeedsClarification) > 0 {
 328 │ 		return Entry{}, false, ErrConfirmation
 329 │ 	}
 330 │ 	entryID, err := id.New()
 331 │ 	if err != nil {
 332 │ 		return Entry{}, false, err
 333 │ 	}
 334 │ 	now := s.now().UTC()
 335 │ 	entry := Entry{
 336 │ 		ID: entryID, UserID: userID, CandidateID: candidate.ID, Direction: candidate.Direction,
 337 │ 		Currency: candidate.Currency, AmountMinor: candidate.AmountMinor, Category: candidate.Category,
```

- **L318–L322**：确认请求必须提供非空且不超长的幂等键。
- **L323–L326**：按当前用户读取 Candidate。
- **L327–L329**：仍需澄清或没有发生时间时禁止写账。
- **L330–L333**：生成正式账目 ID。
- **L334**：固定确认时间。
- **L335–L336**：Entry 绑定用户和 Candidate，复制确认过的收支方向。
- **L337**：复制币种、整数金额和类别。

```text
 338 │ 		Merchant: candidate.Merchant, OccurredAt: candidate.OccurredAt.UTC(), Timezone: candidate.Timezone,
 339 │ 		Note: strings.TrimSpace(note), Status: "active", CreatedAt: now, UpdatedAt: now, IdempotencyKey: idempotencyKey,
 340 │ 	}
 341 │ 	return s.store.ConfirmCandidate(ctx, candidate, entry, idempotencyKey, now)
 342 │ }
```

- **L338**：复制商户及 UTC 发生时间，同时保留原时区。
- **L339–L340**：清理备注，标记 active 并保存幂等键。
- **L341–L342**：Store.ConfirmCandidate 负责候选→正式 Entry 的原子确认和重复请求处理。返回 bool 表示本次是否新建。


## 聊天创建提醒并做来源去重

源码：[internal/planner/service.go:332](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:332)

<a id="symbols-7-3"></a>

### 函数与变量详解

函数/类型声明：[`parseReminder`](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:332)、[`ConfirmReminder`](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:360)。

**职责、输入输出与影响：** parseReminder 输入文本、日期槽位、时区与可选来源消息，返回 (Reminder,error)，会保存候选提醒。ConfirmReminder 输入 reminderID 与幂等键，返回 (Reminder, bool, error)，检查字段完整后确认。

**调用关系：** 提醒解析入口 → FindReminderBySourceMessage/ParseReminderWithSlots/CreateReminder；确认入口 → GetReminder/Store.ConfirmReminder。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `sourceMessageID` | string | 来自聊天时的源消息 | 非空时按来源查重；空时没有这一层来源去重。 |
| `text / title / dateHint / timezone` | string | 自然语言与槽位参数 | 交 ParseReminderWithSlots，dateHint 是补充日期线索，不是最终 UTC due time。 |
| `existing` | Reminder | 来源查重成功 | 直接返回旧候选，避免同源重复创建。 |
| `item` | Reminder | 解析结果或确认时查库 | 解析阶段填 ID/UserID/来源/时间；确认时要求 DueAt 非 nil 且无待澄清项。 |
| `reminderID / idempotencyKey` | string / string | 确认参数 | 一个定位提醒，一个识别逻辑确认，不能互换。 |

### 源码与逐行解释

```text
 332 │ func (s *Service) parseReminder(ctx context.Context, userID, sourceMessageID, text, title, dateHint, timezone string) (Reminder, error) {
 333 │ 	if sourceMessageID != "" {
 334 │ 		existing, err := s.store.FindReminderBySourceMessage(ctx, userID, sourceMessageID)
 335 │ 		if err == nil {
 336 │ 			return existing, nil
 337 │ 		}
 338 │ 		if !errors.Is(err, ErrNotFound) {
 339 │ 			return Reminder{}, err
 340 │ 		}
 341 │ 	}
 342 │ 	item, err := ParseReminderWithSlots(text, title, dateHint, timezone, s.now().UTC())
 343 │ 	if err != nil {
 344 │ 		return Reminder{}, err
 345 │ 	}
 346 │ 	item.ID, err = id.New()
 347 │ 	if err != nil {
 348 │ 		return Reminder{}, err
 349 │ 	}
```

- **L332–L334**：若有 sourceMessageID，先按用户与原消息查已有提醒。
- **L335–L337**：找到就复用，重放同一输入不会再次创建候选。
- **L338–L341**：只有 NotFound 允许新建，数据库故障不可当作无记录。
- **L342–L345**：解析文本、补充标题/日期槽和时区；追问补字段可走同一解析器。
- **L346–L349**：生成提醒 ID。

```text
 350 │ 	item.UserID = userID
 351 │ 	item.SourceMessageID = sourceMessageID
 352 │ 	item.CreatedAt = s.now().UTC()
 353 │ 	item.UpdatedAt = item.CreatedAt
 354 │ 	if err = s.store.CreateReminder(ctx, item); err != nil {
 355 │ 		return Reminder{}, err
 356 │ 	}
 357 │ 	return item, nil
 358 │ }
 359 │
 360 │ func (s *Service) ConfirmReminder(ctx context.Context, userID, reminderID, idempotencyKey string) (Reminder, bool, error) {
 361 │ 	idempotencyKey = strings.TrimSpace(idempotencyKey)
 362 │ 	if idempotencyKey == "" || len(idempotencyKey) > 191 {
 363 │ 		return Reminder{}, false, ErrIdempotencyKey
 364 │ 	}
 365 │ 	item, err := s.store.GetReminder(ctx, userID, reminderID)
 366 │ 	if err != nil {
 367 │ 		return Reminder{}, false, err
 368 │ 	}
```

- **L350–L353**：绑定用户、原消息和创建/更新时间。
- **L354–L358**：保存 Reminder 候选并返回；此时仍需下一节确认。
- **L359–L364**：ConfirmReminder 先校验幂等键。
- **L365–L368**：按用户读取候选。

```text
 369 │ 	if item.DueAt == nil || len(item.NeedsClarification) > 0 || item.Status == "needs_clarification" {
 370 │ 		return Reminder{}, false, ErrConfirmation
 371 │ 	}
 372 │ 	return s.store.ConfirmReminder(ctx, item, idempotencyKey, s.now().UTC())
 373 │ }
```

- **L369–L371**：DueAt 缺失、待澄清字段非空或 needs_clarification 状态都不能确认。
- **L372–L373**：Store 层原子确认，返回记录和是否新确认。


解析器在 planner/parser.go：上午/下午歧义、过去时间、夏令时缺口都在自然语言解释阶段处理。通知 scheduler 扫到期提醒后生成应用通知记录；系统日历/提醒权限属于平台适配边界，不等于 Web 已经静默写入手机系统。

## 加入今日计划：首次建 Plan，否则加 Item

源码：[internal/planner/service.go:176](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:176)

<a id="symbols-7-4"></a>

### 函数与变量详解

函数/类型声明：[`AddTodayItem`](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:176)。

**职责、输入输出与影响：** 输入用户、本地日期、时区、标题、source；返回 (PlanItem,error)。同日期没计划则建计划，有则加条目；只有 chat: 来源按稳定来源身份查重。

**调用关系：** 今日计划入口/工具调用；FindPlanItemBySource → ListPlans → CreatePlan 或 buildPlanItem/AddPlanItem。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `localDate / timezone` | string / string | 例如 YYYY-MM-DD 与 Asia/Shanghai | 必须能按该时区解析，localDate 不是 UTC 时间戳。 |
| `title / source` | string / string | 用户标题 / 来源标记 | 去空白；source 为 chat:... 才使用源去重，普通 UI 来源不当唯一键。 |
| `location` | *time.Location | LoadLocation | 验证日期解释环境。 |
| `existing` | PlanItem | FindPlanItemBySource | 同聊天来源已有项时复用，不意味着同标题都合并。 |
| `plans / plan` | []Plan / Plan | ListPlans / CreatePlan 新结果 | 无计划时建“今日计划”，否则使用 plans[0]。 |
| `item` | PlanItem | buildPlanItem | 带计划关联与默认中优先级，之后 Store 保存。 |

### 源码与逐行解释

```text
 176 │ func (s *Service) AddTodayItem(ctx context.Context, userID, localDate, timezone, title, source string) (PlanItem, error) {
 177 │ 	title = strings.TrimSpace(title)
 178 │ 	source = strings.TrimSpace(source)
 179 │ 	if title == "" || len([]rune(title)) > 255 {
 180 │ 		return PlanItem{}, fmt.Errorf("%w: plan item title is required and must not exceed 255 characters", ErrValidation)
 181 │ 	}
 182 │ 	if len([]rune(source)) > 64 {
 183 │ 		return PlanItem{}, fmt.Errorf("%w: plan item source must not exceed 64 characters", ErrValidation)
 184 │ 	}
 185 │ 	location, err := time.LoadLocation(timezone)
 186 │ 	if err != nil {
 187 │ 		return PlanItem{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
 188 │ 	}
 189 │ 	if _, err = time.ParseInLocation("2006-01-02", localDate, location); err != nil {
 190 │ 		return PlanItem{}, fmt.Errorf("%w: local_date must use YYYY-MM-DD", ErrValidation)
 191 │ 	}
 192 │ 	// Generic UI origins describe where an item came from; they are not stable
 193 │ 	// idempotency keys. Chat sources include the message ID and can safely
 194 │ 	// deduplicate a replay of the same confirmed action.
 195 │ 	if strings.HasPrefix(source, "chat:") {
```

- **L176–L181**：清理标题/来源并要求标题非空、不超过 255 字符。
- **L182–L184**：来源字符串最多 64 字符。
- **L185–L188**：验证时区。
- **L189–L191**：按本地时区解析 YYYY-MM-DD。
- **L192–L195**：只有 chat: 开头的稳定消息来源作为去重键；通用 UI 来源只是标签，不能用于拦截后续所有人工添加。

```text
 196 │ 		existing, findErr := s.store.FindPlanItemBySource(ctx, userID, localDate, source)
 197 │ 		if findErr == nil {
 198 │ 			return existing, nil
 199 │ 		}
 200 │ 		if !errors.Is(findErr, ErrNotFound) {
 201 │ 			return PlanItem{}, findErr
 202 │ 		}
 203 │ 	}
 204 │ 	plans, err := s.store.ListPlans(ctx, userID, localDate)
 205 │ 	if err != nil {
 206 │ 		return PlanItem{}, err
 207 │ 	}
 208 │ 	if len(plans) == 0 {
 209 │ 		plan, createErr := s.CreatePlan(ctx, userID, PlanInput{
 210 │ 			Title: "今日计划", LocalDate: localDate, Timezone: timezone,
 211 │ 			Items: []PlanItemInput{{Title: title, Priority: "medium", EstimatedMinutes: 0, Source: source}},
 212 │ 		})
```

- **L196–L203**：同用户、同日期、同聊天来源已有 Item 就复用；其他查询错误终止。
- **L204–L207**：查询当天 Plan。
- **L208–L212**：没有当天计划时，创建默认“今日计划”并一次包含新 Item。

```text
 213 │ 		if createErr != nil {
 214 │ 			return PlanItem{}, createErr
 215 │ 		}
 216 │ 		return plan.Items[0], nil
 217 │ 	}
 218 │ 	now := s.now().UTC()
 219 │ 	item, err := buildPlanItem(plans[0].ID, PlanItemInput{Title: title, Priority: "medium", Source: source}, now)
 220 │ 	if err != nil {
 221 │ 		return PlanItem{}, err
 222 │ 	}
 223 │ 	if err = s.store.AddPlanItem(ctx, userID, plans[0].ID, item); err != nil {
 224 │ 		return PlanItem{}, err
 225 │ 	}
 226 │ 	return item, nil
 227 │ }
```

- **L213–L217**：创建失败返回错误，成功直接返回刚创建的首项。
- **L218–L222**：已有 Plan 时构造新 PlanItem，默认中优先级。
- **L223–L227**：Store 按用户及 Plan ID 添加并返回 Item。


## Excel 导出是持久化任务

源码：[internal/ledger/service.go:159](/Users/windcry1/Documents/ai_companion/internal/ledger/service.go:159)

<a id="symbols-7-5"></a>

### 函数与变量详解

函数/类型声明：[`QueueExport`](/Users/windcry1/Documents/ai_companion/internal/ledger/service.go:159)。

**职责、输入输出与影响：** 输入 requestKey、month、timezone、currency 等，返回 (ExportJob,bool,error)。只接受并保存导出任务，不在此处生成 Excel 字节。

**调用关系：** 账本导出请求 → monthRange 校验 → CreateExport；后台另行消费任务。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `requestKey` | string | 客户端一次导出动作的稳定键 | 去空白与限长后交 CreateExport 去重。 |
| `month / timezone` | string / string | 月份和时区 | monthRange 校验统计边界；不是所有地区都按 UTC 月切分。 |
| `currency` | string | 输入规范化为大写 | 空回退 CNY，只允许 CNY/USD；不做汇率换算。 |
| `exportID / job` | string / ExportJob | 新 ID / queued 任务 | 保存 Month/Currency/Timezone/时间；没有立即可下载的文件字节。 |
| `now` | time.Time | s.now().UTC() | 任务时间基准。 |

### 源码与逐行解释

```text
 159 │ func (s *Service) QueueExport(ctx context.Context, userID, requestKey, month, timezone, currency string) (ExportJob, bool, error) {
 160 │ 	requestKey = strings.TrimSpace(requestKey)
 161 │ 	if requestKey == "" || len(requestKey) > 191 {
 162 │ 		return ExportJob{}, false, ErrIdempotencyKey
 163 │ 	}
 164 │ 	if _, _, _, err := monthRange(month, timezone); err != nil {
 165 │ 		return ExportJob{}, false, err
 166 │ 	}
 167 │ 	currency = strings.ToUpper(strings.TrimSpace(currency))
 168 │ 	if currency == "" {
 169 │ 		currency = "CNY"
 170 │ 	}
 171 │ 	if currency != "CNY" && currency != "USD" {
 172 │ 		return ExportJob{}, false, fmt.Errorf("%w: unsupported currency", ErrValidation)
 173 │ 	}
 174 │ 	exportID, err := id.New()
 175 │ 	if err != nil {
 176 │ 		return ExportJob{}, false, err
 177 │ 	}
```

- **L159–L163**：用户的导出 requestKey 必填且长度有限，重复点击可复用同一任务。
- **L164–L166**：验证月份与时区；真正取账单时按同一月边界解释。
- **L167–L170**：规范化币种，空则默认 CNY。
- **L171–L173**：目前此接口只支持 CNY/USD。
- **L174–L177**：生成 Export ID。

```text
 178 │ 	now := s.now().UTC()
 179 │ 	job := ExportJob{ID: exportID, UserID: userID, Month: month, Currency: currency, Timezone: timezone, Status: "queued", CreatedAt: now, UpdatedAt: now}
 180 │ 	return s.store.CreateExport(ctx, job, requestKey)
 181 │ }
```

- **L178–L181**：构造 queued ExportJob，交 Store.CreateExport 保存并按 requestKey 去重；现在还没有 Excel 字节。


Worker 的 RunExportReconciler → executeExport → ArtifactToolExporter 调用 [workers/spreadsheet/ledger_export.mjs](/Users/windcry1/Documents/ai_companion/workers/spreadsheet/ledger_export.mjs)。任务成功后 DownloadExport 检查归属、状态和文件；前端读取 ai-ledger-export 标记显示下载。

## 把代码连成一次真实行为

### 一个候选的状态与幂等键

假设解析结果为候选 C1，金额 3250，用户确认请求逻辑键为 K1。首次确认在有效性检查后写 Entry E1；网络丢失响应导致重试时，仍使用 K1，应复用既有结果，而不是因为按钮又点了一次就再记一笔。

| 概念 | 示例 | 不能当作什么 |
|---|---|---|
| 候选 ID | C1 | 正式账目 ID |
| 确认幂等键 | K1 | 任意新请求都重复使用的固定字符串 |
| 正式记录 ID | E1 | 消息或 Agent Run ID |
| 来源消息 ID | M1 | 用户对所有未来操作的永久授权 |

重试同一次业务动作应复用键；另一笔相同金额午饭应该是新的动作与新键。幂等并不是“数据库禁止出现两条相同内容”。

### 计划和提醒的去重范围不完全相同

聊天提醒可按 userID 与 sourceMessageID 找已有项，从而让同一来源的重复处理复用结果。今日计划的某些去重针对 chat 来源前缀，不是所有手动 UI 添加都按文本强行去重。阅读一个领域后不要把它的规则直接套到另一个领域。

修改时间时 expectedUpdatedAt 表示“我基于这个旧版本编辑”。若另一个设备已修改，旧预期不匹配应报冲突，而不是悄悄覆盖对方。它与 Run 的 revision 都在表达乐观并发控制，只是版本表示不同。

### 导出为什么也是任务

导出可能读取大量记录并生成文件，不适合把用户等待期间所有计算都绑在短 HTTP 生命周期。请求键关联持久化导出任务，Worker 生成制品，客户端随后领取结果。接受导出不意味着文件已经可下载；导出任务失败也不应改动原账本。

### 自测与答案

问：第二次确认带一个全新的 K2，是否还能只靠 K1 的幂等记录保证不重复？答：不能；新键是新的请求身份，还需领域中候选状态、唯一约束等规则，不能把某一层保证无限外推。

问：两次同样的“午饭 32.5 元”必须合并吗？答：不一定，可能确实是两笔；去重应依据具体动作身份和来源，而非只比较文字。

## 对照阅读与验证

修改计划时间与提醒改期继续读 SchedulePlanItem、RescheduleReminder：expectedUpdatedAt 防止旧页面覆盖；完成、列表、月汇总主要是同一领域 Store 的短包装。测试应关注重复确认只生成一条、跨时区月边界、上午下午追问、过期改期与并发修改冲突。

[返回目录](README.md) · [下一章：Skill 执行与文件交付](08-skill-artifacts.md)
