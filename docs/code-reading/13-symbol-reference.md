# 13 变量与函数速查手册

[阅读总入口](README.md)

这一版把第 01–11 章每个源码段补上了“函数卡片 + 变量表”。在函数卡片中看职责、参数、返回值、谁调用谁和副作用；在变量表中看名字、类型、从哪里来、之后怎样用。已有逐行注释继续说明具体语句，两层互相补充。

类型表中使用“对象/映射形状”的地方是在说明动态数据契约，不冒充编译器已经验证了它。Go 的类型名以当前包为准；Python/TypeScript 类型注解不代替运行时校验。多个名字并列时解释其对应关系；同名变量跨函数应重新看类型与作用域。

覆盖范围是已有 76 个核心源码阅读段，不是声称仓库每个标识符都已有词条。索引中的函数/类型可跳转声明位置，“变量解释”可跳到章节内相应卡片。源码中的真实标识符保留不翻译，中文解释帮助理解，方便你用编辑器按名字搜索。

## 1. 先判断一个名字属于哪一类

| 写法 | 是什么 | 项目里怎么读 |
|---|---|---|
| `RegisterInput` | 类型 | 定义注册所需字段，并不表示已经收到一份请求 |
| `input` | 局部变量/参数 | 当前函数这一份输入，其类型由签名或声明决定 |
| `input.Email` | 字段访问 | 取结构体中的邮箱值，不是调用函数 |
| `normalizeEmail(input.Email)` | 函数调用 | 读取原邮箱，返回规范化字符串；需接收返回值才会更新变量 |
| `s.store.CreateUser(ctx, user)` | 经成员对象调用方法 | s 是服务，store 是它持有的存储依赖，CreateUser 才是真正的调用 |
| `time.Now` 与 `time.Now()` | 函数值 / 调用结果 | 前者可保存到 s.now 供以后调用，后者立刻取得时间 |
| `run.Status` 与 `"status"` | Go 字段 / 字符串键 | 经 JSON 标签映射后同一信息可有不同写法 |
| `ErrConflict` | 领域错误值 | 需同时看所属包；Agent 冲突和 Wiki 冲突不是同一个业务事件 |
| `DefaultMemoryTokens` | 常量 | 是默认预算，不是已经消费的 Token 数 |

例如 `s.store` 并不是 Go 关键字：作者把接收者命名 s，把成员命名 store。identity.Service.store、skill.Service.store、postgresstore.Store.db 是不同对象。把全项目所有 s 当成“同一个服务实例”，会从第一层就读错调用链。

## 2. 高频局部变量：名字只是线索，类型与来源才是证据

| 名字 | 常见类型/单位 | 在本项目的具体阅读方法 |
|---|---|---|
| `ctx` | Go context.Context | 取消/超时/请求信息，通常传给 SQL、网络和执行器；不是给模型的历史文本 |
| `context` | Python dict/Mapping | 在 Graph 中多表示业务上下文；与 Go ctx 不同 |
| `s` | 某包的 *Service 或 *Server | 先看函数签名前面的接收者，才能知道它有哪些依赖 |
| `r` | *http.Request、*Runner、*Relay、Record 等 | HTTP 中是请求，Worker 中可能是执行器，Passkey 中又可能是记录；没有全局固定解释 |
| `w` | http.ResponseWriter 或 *RuntimeWorker | 前者写 HTTP，后者执行任务；按所在文件/签名区分 |
| `g`、`e`、`c` | ToolGateway、Executor、Controller 等接收者 | 是作者选择的短名字，不是标准库特殊变量 |
| `err` | error | nil 表示该次调用没有返回错误；不同块内同名 err 可能不是同一变量 |
| `ok` | bool | 可能是键存在、类型断言成功、解析成功；不必然是业务完成 |
| `created` | bool | 是否新建记录，false 可能是幂等复用，不代表失败 |
| `changed` | bool | 是否发生新状态变化，如审批重复提交可为 false |
| `processed` | bool | 是否取到并处理过工作，可能连失败记录也算已处理 |
| `input` | 结构体/map/dict | 入参容器；同名不保证字段相同，要先查签名 |
| `item` | 循环中的当前元素或查询结果 | 可能是文档、记忆、账目或 Run；不能统一解释为“消息” |
| `items` | 列表/切片 | 与 item 对应的集合，常作为后续筛选候选 |
| `current`、`old` | 读取到的旧对象 | 经常用于保留原版本；但 CorrectFromMessage 的 old 会被改成新事实模板 |
| `next`、`updated` | 新对象，也可能是 Handler | identity.Refresh 的 next 是新 Session，requireAuth 的 next 是下游 HTTP Handler |
| `payload` | []byte、map、JSON 对象等 | 只表示某一层的载荷，不说明它是请求还是响应、是不是给模型的 |
| `result` | 某一调用结果 | 需看右侧函数的返回类型；可能是工具对象，也可能是 SQL Result |
| `output` | JSON、map 或正文 | Skill 的 output 字节与 result.Output map 不同，序列化前后也不同 |
| `now` | time.Time | 一个时刻；同一函数固定一次可让多个字段使用同一时间 |
| `timeout`、`lease` | time.Duration | 时长，不是时刻；毫秒数字要乘 time.Millisecond 才成 Duration |
| `expected` | int/int64/time.Time | 调用方持有的旧版本/时间条件，不是希望最后保存的目标版本 |
| `revision` | int/int64 | 并发版本号；与业务版本、执行次数、消息排序号不同 |
| `attempt` | 次数 | 某任务尝试计数；重试与状态变更不保证一一对应 |
| `key` | string | 可为幂等键、配置键、存储键，必须看传向哪里，不能一概叫密钥 |
| `plain`、`hash` | string | 刷新令牌原文与摘要；密码里的 hash 又是带盐派生记录，不能互换 |

### 五组容易混淆的数字

- sequence：会话内逻辑排序；bubble：同一 sequence 的气泡位置。不是 Run revision。
- revision：并发控制版本；SkillVersion/GraphVersion：执行协议或能力版本。不是 Retry attempt。
- AmountMinor：金额最小单位；CostMicros：微单位费用。不要都直接显示成“元”，还要看领域币种与计价口径。
- SizeBytes/MaxInputBytes：字节；EstimatedTokens：估计 Token。一个中文码点通常占多个 UTF-8 字节。
- CreatedAt/DeadlineAt：时刻；TimeoutMS：毫秒数；time.Duration：Go 时长。字段后缀提醒单位，但仍要看实际计算。

## 3. 同名 payload 经过哪些表示

以聊天到模型为例，这是对象关系说明，不表示所有函数共享同一块内存：

| 位置 | 变量名与形状 | 内容 |
|---|---|---|
| 浏览器 send | payload，响应对象 | 服务器返回的 message、job 或 agent_run |
| Agent.AcceptChat | payload，map[string]any | message_id、text、context，是待保存的输入 |
| Agent.AcceptChat | encoded，[]byte | 上一个 map 编码成 JSON，存入 Run.Input |
| PythonRuntimeExecutor.Execute | payload，[]byte | 整个 Go Run 的进程协议 JSON，经 stdin 发给 Python |
| Python execute_run | input_payload，dict | Python 解码后的 run.input，取出 text/user_message 与 context |
| 模型适配器 _request | payload，Mapping | 本次模型 messages、tools 等参数，不是整份 Go Run |
| 模型适配器 _request | body/raw，bytes | 发出的请求 JSON 字节 / 收到的响应字节 |

因此“payload 中有 context”不能推出浏览器收到的 payload 一定也有相同 context 字段。每跨一个协议边界，都要检查构造者与读取者，而不是沿用变量名猜测。

## 4. 同名 Run 与 status 的区别

| 对象 | 主要角色 | 与本教程直接相关的状态例子 |
|---|---|---|
| 用户 Message | 已保存的一次输入 | completed 表示输入完整，不能推导助手已回答 |
| conversation.Job | 普通生成任务 | accepted、completed、failed 等，前端观察其事件 |
| agent.Run | 可恢复的多步 Agent | accepted、running、waiting_approval、waiting_tool、completed |
| skill.Run | 具体工具算法与文件交付 | waiting_confirmation、queued、running、succeeded |
| ToolPreparation | 工具准备阶段的返回协议 | completed 只表示准备返回，异步 Skill 可能仍运行 |
| document.Document | 用户上传文件的业务元数据 | queued 到 ready；不与 Agent.status 共用语义 |

这不是各类型所有状态的穷举。变量表会指出具体类型，真正分支必须比较该类型实际使用的字符串。尤其不要把 waiting_approval 与 waiting_confirmation、completed 与 succeeded 随意替换。

## 5. 常见被调函数：不仅翻译名字，还要知道返回什么

### Go 字符串、编码与时间

| 函数/方法 | 输入 → 输出 | 对本项目阅读的影响 |
|---|---|---|
| strings.TrimSpace | string → 新 string | 返回两端空白去除的值；须赋回才更新变量，不删除中间空格 |
| strings.SplitN | string、分隔符、次数 → []string | 认证头拆成最多两段；先检查长度再读 parts[1] |
| strings.EqualFold | 两 string → bool | 忽略大小写比较，用于 Bearer/邮箱等指定规则；不是万能邮箱合法性检查 |
| json.Marshal | Go 值 → []byte,error | 内存对象变 JSON 字节；序列化不等于保存数据库 |
| json.Unmarshal | JSON 字节、可写目标 → error | 通过 &input 填结构；结构能解码不代表业务字段已校验 |
| id.New | 无入参 → string,error | 项目 ID 工具，可能失败；不同实体分别生成身份 |
| s.now / time.Now | 调用函数 → time.Time | s.now 是可替换时钟依赖，便于测试固定“现在” |
| time.LoadLocation | 时区名 → *time.Location,error | 验证与加载时区，不能把任意字符串直接当有效时区 |
| t.Add / t.Before / t.After | 时长或时刻 → 新时刻/bool | Add 不原地改变 t；Before/After 为严格早于/晚于，相等时均 false |
| context.WithCancel / WithTimeout | 父 Context → 子 Context、取消函数 | 取消需要下游响应，不是倒放已经发生的外部动作 |
| errors.Is / errors.As | error、目标 → bool | 按错误链判断类别或提取结构，避免只匹配易变错误文本 |
| append | 切片与元素 → 切片 | 需接回返回值；可能更换底层数组，但不自动持久化 |
| len / make | 长度查询 / 分配容器 | make([]T,0,n) 长度是 0，n 是容量，不是已装了 n 条记录 |

### Go 数据库与业务边界

| 函数/方法 | 输入 → 输出 | 哪些地方可能改变状态 |
|---|---|---|
| db.BeginTx | ctx、选项 → *sql.Tx,error | 开启数据库事务；不是直到模型结束都保持它 |
| tx.QueryRowContext(...).Scan(...) | SQL/参数、接收地址 → error | 查询并填接收变量；无行常以 sql.ErrNoRows 表示 |
| tx.ExecContext | SQL/参数 → sql.Result,error | 执行 INSERT/UPDATE 等；Result 不等于业务对象，需查 RowsAffected |
| RowsAffected | 无参数 → int64,error | 变更行数可用于判断 CAS 是否命中；0 行与执行 SQL 报错不同 |
| tx.Commit / Rollback | 无参数 → error | 提交/撤销当前数据库事务，不包含外部模型、文件系统、邮件 |
| Store.Get… / List… | 按领域签名 → 单对象/列表,error | 通常读取，但具体接收者和实现仍需查；名字不是安全证明 |
| Store.Save… / Create… | 按领域签名 → 结果/error | 通常写存储；查看是否接收 expected、是否事务、是否追加事件 |
| Handler.Execute | ctx、工具参数 → ToolResult,error | Skill 算法入口；可能生成文件、请求模型，不仅是结构转换 |
| publisher.Publish | ctx、Event → PublishAck,error | 外部事件投递；成功后仍需写 Outbox 状态 |
| consumer.Commit | ctx、消息 → error | 确认消息消费进度，不是 PostgreSQL tx.Commit |

### 浏览器与 Python

| 函数/方法 | 输入 → 输出 | 阅读时注意 |
|---|---|---|
| fetch | URL、选项 → Promise<Response> | HTTP 409/500 通常仍得到 Response，要看 ok/status |
| response.json | 响应体 → Promise<解析值> | 可能解析失败，类型断言不验证实际字段 |
| setMessages(current => …) | 状态更新函数 → 无业务结果 | current 是 React 提供的旧状态；返回新数组给 React，不写数据库 |
| mergeMessage / mergeMessages | 本项目消息列表与新记录 → 合并列表 | 去重以 ID 为依据，同文不同 ID 可同时存在 |
| window.confirm | 提示字符串 → boolean | false 表示明确拒绝，不代表网络失败 |
| dict.get | 键、缺省值 → 取得的值 | 键存在且值为 None 时不会使用缺省值，仍需校验 |
| isinstance / callable | 对象、类型或对象 → bool | 是运行时检查；TypedDict/cast 不会自动执行这些检查 |
| dict(...) / list(...) | 可转换对象 → 新外层容器 | 通常浅复制，嵌套对象仍可共享 |
| json.dumps / loads | 对象 → str；str/bytes → 对象 | dumps 后 encode 才变网络字节；loads 后类型仍可能是列表或数字 |
| interrupt / Command | 等待载荷 / 恢复或跳转描述 | 框架暂停与路由机制，不是直接写账本的业务函数 |
| graph.get_state / invoke | 运行配置/输入 → 快照/结果 | get_state 读取恢复位置；invoke 可能走多个节点和外部调用 |

## 6. 逐个拆解一条真实调用

来自记忆纠正路径的语句：

```go
saved, _, err := s.store.UpsertMemory(ctx, old)
return saved, err
```

从左向右拆开读：

1. s：当前 memory.Service 的接收者；不是用户。
2. s.store：这个服务持有的 memory.Store 接口；启动装配决定背后具体实现。
3. UpsertMemory：接口方法，返回 (Memory,bool,error)；要去 Store 实现看事务。
4. ctx：取消/超时上下文；old：本函数已改成新事实模板的 Memory 值。
5. saved：接收存储返回的结果；_：明确忽略“是否新建”；err：仍保留错误。
6. return saved,err：结束当前 CorrectFromMessage，不等于结束 HTTP Server 或整个程序。

为什么不是 return old,nil？因为真正保存可能复用、失败，甚至在 Commit 时失败，调用者必须以存储返回的结果与错误为准。

再看 Skill 的版本条件。下面把分散语句简化并排，属于教学摘写，不是连续原码：

```go
expected := run.Revision
run.Revision++
err = s.store.SaveSkillRun(ctx, run, expected, newSteps, newFiles)
```

expected 是旧版本，run.Revision 是要写入的新版本；二者故意不同。假设读到 7，提交要求数据库仍是 7，然后写入 8。若别的 Worker 已把它改为 8，这次应冲突，而不是把 expected 临时改为 8 强行通过。newSteps/newFiles 只代表本次增量，不应把全部历史再插一遍。

## 7. 怎样使用后面的函数索引

读第 01–11 章时，先看源码段前的卡片，再看代码和逐行解释。遇到陌生变量先找表；遇到表中只列代表性字段的结构体，再点击函数声明并跳到类型定义展开其余字段。不要把“本章未展开这个字段”当成“代码不存在这个字段”。

每张卡片的“调用关系”说明当前阅读链路中的上游与关键下游，不是仓库全部调用者的穷举。Ctrl/Cmd 搜索同名函数时注意包、接收者和文件路径，多个 Service 都有 Create/Confirm/Execute。

以下索引按原行为链路排序，点击函数/类型看声明，点击变量解释回到原代码段。

## 01 启动、登录与角色

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`main`](/Users/windcry1/Documents/ai_companion/cmd/api/main.go:38) | [启动时选择持久化实现](01-startup-identity.md#symbols-1-1) |
| [`Register`](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:116) | [注册：输入转为用户和会话](01-startup-identity.md#symbols-1-2) |
| [`hashPassword`](/Users/windcry1/Documents/ai_companion/internal/identity/password.go:19)、[`verifyPassword`](/Users/windcry1/Documents/ai_companion/internal/identity/password.go:31) | [把密码变成可验证、不可直接还原的记录](01-startup-identity.md#symbols-1-3) |
| [`AccessClaims`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:18)、[`TokenManager`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:25)、[`NewTokenManager`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:30)、[`IssueAccess`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:34)、[`ParseAccess`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:46)、[`NewRefreshToken`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:62)、[`HashRefreshToken`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:71)、[`sign`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:76) | [Access Token 的字段、签发与验证](01-startup-identity.md#symbols-1-4) |
| [`createSession`](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:235) | [登录后建立设备和 Session](01-startup-identity.md#symbols-1-5) |
| [`Refresh`](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:152) | [刷新令牌：轮换旧会话](01-startup-identity.md#symbols-1-6) |
| [`requireAuth`](/Users/windcry1/Documents/ai_companion/internal/httpserver/auth.go:116) | [每次业务请求怎样获得身份](01-startup-identity.md#symbols-1-7) |
| [`Create`](/Users/windcry1/Documents/ai_companion/internal/character/service.go:78)、[`compile`](/Users/windcry1/Documents/ai_companion/internal/character/service.go:187) | [创建和版本化角色](01-startup-identity.md#symbols-1-8) |
| [`Update`](/Users/windcry1/Documents/ai_companion/internal/character/service.go:134) | [修改角色为什么保留旧版本](01-startup-identity.md#symbols-1-9) |

## 02 从发送按钮到聊天入口

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`send`](/Users/windcry1/Documents/ai_companion/web/app/chat-panel.tsx:25) | [浏览器发送一条消息](02-chat-intake.md#symbols-2-1) |
| [`sendMessage`](/Users/windcry1/Documents/ai_companion/internal/httpserver/conversations.go:60) | [发送接口的共同前置检查](02-chat-intake.md#symbols-2-2) |
| [`sendMessage`](/Users/windcry1/Documents/ai_companion/internal/httpserver/conversations.go:60) | [分流、构建上下文并返回 202](02-chat-intake.md#symbols-2-3) |
| [`consumeAgentRun`](/Users/windcry1/Documents/ai_companion/web/app/chat-panel.tsx:57) | [Agent 状态怎样恢复到页面](02-chat-intake.md#symbols-2-4) |

## 03 Agent 入库、领取与完成事务

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`AcceptChat`](/Users/windcry1/Documents/ai_companion/internal/agent/chat.go:26) | [AcceptChat 创建持久化运行](03-agent-transactions.md#symbols-3-1) |
| [`AcceptAgentMessage`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent_chat.go:14) | [同一事务保存输入和调度依据](03-agent-transactions.md#symbols-3-2) |
| [`ClaimNextAgentRun`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent.go:161) | [Worker 用 SKIP LOCKED 领取下一条](03-agent-transactions.md#symbols-3-3) |
| [`CompleteAgentRun`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent.go:220) | [完成时防止迟到 Worker 覆盖](03-agent-transactions.md#symbols-3-4) |

## 04 工具审批、恢复、取消与重试

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`loadRequest`](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:331) | [每次工具调用重新验证执行权](04-agent-approval-recovery.md#symbols-4-1) |
| [`Prepare`](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:136) | [Prepare：执行只读/低风险步骤或产生确认](04-agent-approval-recovery.md#symbols-4-2) |
| [`approval`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:6021) | [approval 节点：暂停并选择分支](04-agent-approval-recovery.md#symbols-4-3) |
| [`resolveAgentRun`](/Users/windcry1/Documents/ai_companion/internal/httpserver/agent_runs.go:50) | [浏览器确认怎样送入持久化恢复](04-agent-approval-recovery.md#symbols-4-4) |
| [`Commit`](/Users/windcry1/Documents/ai_companion/internal/agent/tool_gateway.go:192) | [批准之后真正提交账本写入](04-agent-approval-recovery.md#symbols-4-5) |
| [`executeClaimed`](/Users/windcry1/Documents/ai_companion/internal/agent/worker.go:200) | [执行期间同时响应控制信号](04-agent-approval-recovery.md#symbols-4-6) |
| [`execute`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:7797) | [检查点决定从哪里继续](04-agent-approval-recovery.md#symbols-4-7) |

## 05 普通聊天、上下文与记忆

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`Send`](/Users/windcry1/Documents/ai_companion/internal/conversation/service.go:320) | [普通聊天先接受、后执行](05-context-memory.md#symbols-5-1) |
| [`selectRecentMessages`](/Users/windcry1/Documents/ai_companion/internal/conversation/context.go:157) | [历史按完整消息组选择](05-context-memory.md#symbols-5-2) |
| [`BuildContext`](/Users/windcry1/Documents/ai_companion/internal/conversation/context_snapshot.go:18)、[`contextSnapshot`](/Users/windcry1/Documents/ai_companion/internal/conversation/context_snapshot.go:29) | [两个执行路径共用 BuildContext](05-context-memory.md#symbols-5-3) |
| [`RecallContext`](/Users/windcry1/Documents/ai_companion/internal/memory/service.go:157) | [召回记忆：相关性、时效和重要度](05-context-memory.md#symbols-5-4) |
| [`CorrectFromMessage`](/Users/windcry1/Documents/ai_companion/internal/memory/semantic.go:69) | [用户纠正事实时保留替代链](05-context-memory.md#symbols-5-5) |
| [`UpsertMemory`](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/memory.go:14) | [纠正记忆的事务：关闭旧事实与插入新事实](05-context-memory.md#symbols-5-6) |
| [`BoundMemories`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:72)、[`ReferenceText`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:93)、[`EstimateTokens`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:105) | [记忆预算怎样做到不截断事实](05-context-memory.md#symbols-5-7) |
| [`PromptUpperBound`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:120)、[`CheckRequest`](/Users/windcry1/Documents/ai_companion/internal/contextengine/context.go:146) | [真正发请求前另做窗口上限检查](05-context-memory.md#symbols-5-8) |
| [`history_view`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/conversation_context.py:42) | [Python 按节点角色投影完整消息组](05-context-memory.md#symbols-5-9) |
| [`Context`](/Users/windcry1/Documents/ai_companion/internal/productknowledge/context.go:53) | [产品问题读取随应用发布的知识](05-context-memory.md#symbols-5-10) |

## 06 上传、解析、检索与 Wiki

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`Upload`](/Users/windcry1/Documents/ai_companion/internal/document/service.go:90) | [上传：Blob 与元数据分开保存](06-documents-wiki.md#symbols-6-1) |
| [`process`](/Users/windcry1/Documents/ai_companion/internal/document/ingest.go:120) | [Worker 把文件变成可搜索片段](06-documents-wiki.md#symbols-6-2) |
| [`_chunk_pages`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/document_parser.py:352)、[`emit`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/document_parser.py:360) | [Python 按章节和 Token 切块](06-documents-wiki.md#symbols-6-3) |
| [`Query`](/Users/windcry1/Documents/ai_companion/internal/document/retrieval.go:50) | [问答检索首先限制用户可见范围](06-documents-wiki.md#symbols-6-4) |
| [`ReadParsedContextRoundWindow`](/Users/windcry1/Documents/ai_companion/internal/document/context.go:147) | [整份文件以轮次继续读取](06-documents-wiki.md#symbols-6-5) |
| [`CompileNextWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki_compiler.go:31) | [解析结果异步编译为 Wiki](06-documents-wiki.md#symbols-6-6) |
| [`ListWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:122)、[`ReadWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:170) | [Wiki 依赖全部可见才可返回](06-documents-wiki.md#symbols-6-7) |
| [`UpdateWiki`](/Users/windcry1/Documents/ai_companion/internal/document/wiki.go:351) | [人工编辑 Wiki 使用版本条件](06-documents-wiki.md#symbols-6-8) |

## 07 生活助手：记账、计划、提醒和导出

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`Parse`](/Users/windcry1/Documents/ai_companion/internal/ledger/parser.go:27) | [自然语言解析成待确认账单](07-life-tools.md#symbols-7-1) |
| [`Confirm`](/Users/windcry1/Documents/ai_companion/internal/ledger/service.go:318) | [用户确认后才写正式 Entry](07-life-tools.md#symbols-7-2) |
| [`parseReminder`](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:332)、[`ConfirmReminder`](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:360) | [聊天创建提醒并做来源去重](07-life-tools.md#symbols-7-3) |
| [`AddTodayItem`](/Users/windcry1/Documents/ai_companion/internal/planner/service.go:176) | [加入今日计划：首次建 Plan，否则加 Item](07-life-tools.md#symbols-7-4) |
| [`QueueExport`](/Users/windcry1/Documents/ai_companion/internal/ledger/service.go:159) | [Excel 导出是持久化任务](07-life-tools.md#symbols-7-5) |

## 08 Skill 执行与文件交付

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`start`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:412) | [接受一次版本化 Skill 任务](08-skill-artifacts.md#symbols-8-1) |
| [`Confirm`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:486) | [确认任务并使用 revision 更新](08-skill-artifacts.md#symbols-8-2) |
| [`execute`](/Users/windcry1/Documents/ai_companion/internal/skill/runner.go:48) | [Worker 执行时定时续租](08-skill-artifacts.md#symbols-8-3) |
| [`execute`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:861) | [执行算法与解释错误](08-skill-artifacts.md#symbols-8-4) |
| [`execute`](/Users/windcry1/Documents/ai_companion/internal/skill/service.go:861) | [文件保存后提交完成状态](08-skill-artifacts.md#symbols-8-5) |

## 09 团队分享、管理员登录与额度

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`CreateWorkspace`](/Users/windcry1/Documents/ai_companion/internal/team/service.go:76) | [创建空间时同时成为 Owner](09-teams-admin-quotas.md#symbols-9-1) |
| [`Invite`](/Users/windcry1/Documents/ai_companion/internal/team/service.go:109) | [邀请是受权限约束的待接受记录](09-teams-admin-quotas.md#symbols-9-2) |
| [`AcceptInvitation`](/Users/windcry1/Documents/ai_companion/internal/team/service.go:148) | [接受邀请要绑定真实账号邮箱](09-teams-admin-quotas.md#symbols-9-3) |
| [`send`](/Users/windcry1/Documents/ai_companion/internal/email/service.go:173) | [邮件 Worker 处理投递结果](09-teams-admin-quotas.md#symbols-9-4) |
| [`takeCeremony`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:212) | [挑战只能原子消费一次](09-teams-admin-quotas.md#symbols-9-5) |
| [`BeginLogin`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:251)、[`FinishLogin`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:260) | [Passkey 开始与完成登录](09-teams-admin-quotas.md#symbols-9-6) |
| [`Authenticate`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:317) | [管理员 Session 逐次校验撤销与闲置](09-teams-admin-quotas.md#symbols-9-7) |
| [`QuotaLimits`](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:14) | [额度的 null、0、-1 是不同意思](09-teams-admin-quotas.md#symbols-9-8) |
| [`SetQuotaPolicy`](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:51) | [管理员设置一层额度](09-teams-admin-quotas.md#symbols-9-9) |
| [`apply`](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:77) | [政策逐字段覆盖](09-teams-admin-quotas.md#symbols-9-10) |
| [`Check`](/Users/windcry1/Documents/ai_companion/internal/billing/service.go:302) | [业务接收前检查实际用量](09-teams-admin-quotas.md#symbols-9-11) |

## 10 故障恢复、降级与配置发布

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`RunOnce`](/Users/windcry1/Documents/ai_companion/internal/eventbus/relay.go:33) | [Outbox Relay 怎样投递并重试](10-reliability-controlplane.md#symbols-10-1) |
| [`RunOnce`](/Users/windcry1/Documents/ai_companion/internal/eventbus/consumer.go:196) | [Inbox 去重与业务幂等的关系](10-reliability-controlplane.md#symbols-10-2) |
| [`Observe`](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:124) | [用连续样本避免降级抖动](10-reliability-controlplane.md#symbols-10-3) |
| [`classify`](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:183)、[`policyFor`](/Users/windcry1/Documents/ai_companion/internal/reliability/controller.go:196) | [等级映射为具体执行策略](10-reliability-controlplane.md#symbols-10-4) |
| [`Create`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:161) | [新配置以不可变草稿版本创建](10-reliability-controlplane.md#symbols-10-5) |
| [`Validate`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:185)、[`Submit`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:199)、[`Publish`](/Users/windcry1/Documents/ai_companion/internal/controlplane/service.go:221) | [验证、提交与发布门禁](10-reliability-controlplane.md#symbols-10-6) |

## 11 深入 Python：模型调用怎样驱动图

| 函数/类型声明 | 对应行为与变量解释 |
|---|---|
| [`Execute`](/Users/windcry1/Documents/ai_companion/internal/agent/python_executor.go:92) | [Go 将运行记录发送到 Python 进程](11-python-model-graph.md#symbols-11-1) |
| [`execute_run`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_worker.py:175) | [输入转换与可信工具目录](11-python-model-graph.md#symbols-11-2) |
| [`_classify_execution_mode`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3326) | [选择直接回答、单工具还是多步执行](11-python-model-graph.md#symbols-11-3) |
| [`decide`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3701) | [模型决策转为受约束的状态更新](11-python-model-graph.md#symbols-11-4) |
| [`after_decision`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:3851) | [after_decision 是实际分支选择器](11-python-model-graph.md#symbols-11-5) |
| [`_request`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/openrouter_decision.py:2230) | [真正的模型 HTTP 请求](11-python-model-graph.md#symbols-11-6) |
| [`generate_response`](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py:5745) | [生成文本并进入质量检查](11-python-model-graph.md#symbols-11-7) |

## 本版覆盖与校验

覆盖 76 个源码阅读段、94 个不同声明位置的函数/类型、482 条变量/字段条目（一个条目可能对照多个关联名字，不等于唯一变量数量）。所有卡片与原章节共用同一份人工数据。

生成器检查章节卡片齐全、函数/类型声明存在、变量标识符存在、源码指纹及本地跳转；这些机械检查不等于自动证明类型推断与语义正确。本次仅增补文档，不改变运行时业务。

[返回阅读总入口](README.md)
