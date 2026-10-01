# 00 从零开始：把源码读成可以解释的行为

[阅读总入口](README.md) · [下一章：启动、登录与角色](01-startup-identity.md) · [案例与实验](12-worked-cases-and-labs.md)

这章不要求你先学完 Go、TypeScript、Python、数据库和大模型。我们的目标是：遇到项目中的一段代码，能说出它接受什么、检查什么、改变什么、交给谁、失败时留下什么。语法服务于这些问题，而不是背完语法才准读项目。

本章代码凡写“教学示例”，都是为了隔离一个概念而简化的例子，不是仓库原函数。第 01–11 章的带行号代码才是从工作区直接摘录的真实源码。章节基于当前源码静态分析；没有把阅读例子说成已经在真实账号、数据库或模型上执行过。

## 1. 项目到底在做什么

从用户角度，项目有陪伴聊天、生活管理、办公制品三类行为。它们共享身份、角色、会话、知识和工具基础设施。一个“帮我记一笔午餐”的请求，会跨越网页、HTTP、Go 服务、持久化任务、Python 决策、工具确认、账本写入，最后再回到网页。

“跨越”不是说每一步都在不同机器上：多个服务对象可以同处一个 Go 进程；一个机器也可以运行多个进程。类/结构体、进程、容器、机器是四个不同层次。

| 位置 | 主要职责 | 阅读入口 |
|---|---|---|
| Web：TypeScript、React、Next.js | 输入、页面状态、HTTP 调用、展示结果 | [聊天面板](/Users/windcry1/Documents/ai_companion/web/app/chat-panel.tsx) |
| Go HTTP 层 | 身份、参数、响应状态、业务入口 | [会话接口](/Users/windcry1/Documents/ai_companion/internal/httpserver/conversations.go) |
| Go 领域服务 | 接受消息、记账、权限、预算、工具治理 | [Agent 接受服务](/Users/windcry1/Documents/ai_companion/internal/agent/chat.go) |
| PostgreSQL Store | 记录、事务、领取、版本条件、可恢复状态 | [Agent 存储](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/agent.go) |
| Worker | 不依赖页面一直打开的后台执行 | [Agent Worker](/Users/windcry1/Documents/ai_companion/internal/agent/worker.go) |
| Python 与 LangGraph | 状态图、模型决策、暂停恢复、文档算法 | [图运行时](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/agent_runtime.py) |
| 配置的模型提供商 | 接收模型请求并返回文本/动作建议 | [模型请求实现](/Users/windcry1/Documents/ai_companion/workers/python/src/ai_companion_worker/openrouter_decision.py) |

依赖证据可直接看 [Go 模块](/Users/windcry1/Documents/ai_companion/go.mod)、[Web 依赖与脚本](/Users/windcry1/Documents/ai_companion/web/package.json)、[Python 依赖与入口](/Users/windcry1/Documents/ai_companion/workers/python/pyproject.toml)。这里讲仓库声明与实现，不推断本机每个可选服务已经启动，也不讨论库的最新线上版本。

## 2. 第一遍读代码，只写下六句话

拿到一个函数，不要一开始就追进所有库。先填完：

1. 谁调用它？例如 HTTP Handler 调用 Service。
2. 输入是什么？哪些来自用户，哪些来自数据库或可信配置？
3. 它拒绝什么？把每个提前 return 列出来。
4. 它改变什么？局部变量、内存对象、数据库、文件还是外部系统？
5. 成功返回什么？“已接受”“已领取”“已完成”分别是哪一种？
6. 如果在中间退出，什么已经发生，什么尚未发生？

第二遍才跟局部变量；第三遍再追跨层调用。不要按文件树字母顺序把所有文件从第一行读到最后一行：项目的行为不按文件名顺序执行。

### 一个函数签名就能读出很多信息

真实签名来自 [记忆存储](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/memory.go:14)：

```go
func (s *Store) UpsertMemory(ctx context.Context, item memory.Memory) (memory.Memory, bool, error)
```

| 片段 | 逐个解释 |
|---|---|
| `func` | 声明函数/方法，不是调用它 |
| `(s *Store)` | 方法接收者；在方法内用 `s` 访问 Store，`*Store` 表示指向 Store 的指针 |
| `UpsertMemory` | 方法名；名字提示“更新或插入”，具体分支仍以函数体为准 |
| `ctx context.Context` | 名为 ctx 的参数，类型是 context 包的 Context |
| `item memory.Memory` | 名为 item 的参数，类型是 memory 包的 Memory 结构体 |
| `(memory.Memory, bool, error)` | 返回三个值：结果记录、是否新建、错误 |

调用时的 `s.store.UpsertMemory(ctx, item)` 才会进入函数体。声明、构造对象和执行方法不是同一件事。遇到 `return item, true, tx.Commit()`，要先求值 `Commit()`；返回的 bool 可以为 true 而 error 非 nil，所以必须先检查错误。

## 3. Go：看懂本项目最常用的语法

### 3.1 变量、类型、零值与赋值

教学示例：

```go
var total int         // 声明 int，零值是 0
name := "小林"       // 短变量声明，由右边推断类型 string
total = 2             // 给已有变量赋新值
total += 3            // 相当于 total = total + 3
```

`:=` 和 `=` 不完全一样：在同一作用域使用 `:=` 时，左边至少有一个新的非空白变量。块内部又声明同名变量可能遮蔽外面的变量，所以不能看见同名 `err` 就认为它始终是同一个存储位置。

常见零值：数字 0、bool false、string 空串、指针 nil；结构体每个字段分别取零值。`nil` 表示某些类型没有引用值，不是通用数字零。访问 nil 指针所指对象会出错。

`if err := operation(); err != nil { ... }` 先执行 operation，再判断；这里的 err 在这个 if（含 else）范围内有效，不会自动变成整个函数的同名 err。

### 3.2 结构体、字段与 JSON 标签

真实片段见 [Token 字段](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:18)：

```go
type AccessClaims struct {
    UserID    string `json:"sub"`
    SessionID string `json:"sid"`
    ExpiresAt int64  `json:"exp"`
    IssuedAt  int64  `json:"iat"`
}
```

结构体把相关字段组合成一种类型。Go 里读 `claims.UserID`；转 JSON 后键为 `sub`。标签不会把值改名，它指导编码器如何表示字段。`json:"summary,omitempty"` 中的 omitempty 表示编码时可省略空值；它本身不是“请求里缺字段就报错”的校验规则。

Go 大写开头的标识符通常可导出供其他包访问；小写留在本包。包不是文件：同目录同包多个文件可共同定义方法。遇到 `memory.Memory`，前者是包名，后者是导出的类型名。

### 3.3 指针：为什么有 `*bool`、`&claims`

教学示例：

```go
approved := false
p := &approved
fmt.Println(*p) // false
*p = true
fmt.Println(approved) // true
```

`&变量` 取地址；声明里的 `*bool` 是“指向 bool 的指针类型”；表达式里的 `*p` 是读取/修改指向的值。不是所有星号都在做乘法。

审批请求用 `*bool` 表达：nil=未提供，非 nil 且 false=明确拒绝，非 nil 且 true=明确批准。标准 JSON 解码到新结构体时，缺失和显式 null 通常都得到 nil；如要区分这两个 JSON 情况，还需要额外表示，这个字段没有区分。

`json.Unmarshal(payload, &claims)` 要填充调用方已有变量，因此传地址。若不传可写目标，就无法按该 API 的契约把 JSON 解码结果写回。

### 3.4 切片、map、range：不要把容量当长度

教学示例：

```go
selected := make([]string, 0, 3)
selected = append(selected, "第一条")
seen := map[string]bool{}
seen["第一条"] = true
for index, value := range selected {
    fmt.Println(index, value)
}
```

`[]string` 是字符串切片；make 的 0 是当前元素数，3 是预分配容量。append 返回新的切片描述，可能更换底层数组，所以要接回结果。索引从 0 开始，长度为 1 时不能访问 `[1]`。

map 是键到值的映射。`seen["未知"]` 返回 bool 的零值 false；`value, ok := someMap[key]` 还会告诉你键是否真的存在。两者常用于去重和可选字段。

`range` 遍历切片时通常取得索引和值；`_` 表示显式忽略某个结果。遍历得到的结构体 item 是该元素的值副本；修改这个局部副本的普通字段不会直接改原切片元素，但若字段里含指针、map、切片，底层引用仍可能共享。

### 3.5 string、字节、rune、Token 不相等

教学例子“你好A”：UTF-8 字节数为 7，Unicode 码点数为 3。Go 的 `len(text)` 数字节；`len([]rune(text))` 数码点。用户感知的一个复杂 emoji 可能由多个码点组成，因此 rune 也不能无条件称为“屏幕上一个字形”。

模型 Token 是另一个计量体系，需要对应 tokenizer；项目快速估计器只是选择策略的一部分。长度上限、文件字节限制、模型窗口不要共用同一种直觉。

### 3.6 错误返回与短路判断

教学示例：

```go
value, err := load()
if err != nil {
    return fmt.Errorf("load failed: %w", err)
}
use(value)
```

正常的错误路径不是必须 panic 才算错误；函数可以返回 error 让上层决定 HTTP 状态或重试。`%w` 包装原错误，`errors.Is` 可沿包装链判断是否某种错误，`errors.As` 可提取特定错误类型。

`||` 是“或”，左边 true 后右边不求值；`&&` 是“且”，左边 false 后右边不求值。Token 解析的 `len(parts) != 3 || parts[0] != "v1" ...` 靠短路防止段数不足时访问越界。调整条件顺序可能改变安全性，不能只看逻辑词义。

`continue` 跳过当前循环项；`break` 离开循环；`return` 离开当前函数。第 05 章中，独立记忆放不下用 continue，连续历史再加会超预算用 break，这是业务规则，不是随手选的同义语法。

### 3.7 接口、类型断言和依赖注入

接口声明一组方法；具体类型只要实现相应方法，就可满足接口，不需要写 `implements`。这让 Service 可使用内存 Store 进行测试，也可使用 PostgreSQL Store 持久化。

教学示例：

```go
store, ok := s.store.(ChatStore)
if !ok {
    return ErrConflict
}
```

这是运行时询问“当前具体对象是否还实现 ChatStore 所需能力”。它不创建新数据库，也不把一个不支持事务的对象魔法升级。若用不带 ok 的断言且不满足类型，可能 panic；源码用 ok 明确处理不支持的情况。

一个接口变量内部可持有具体类型和对应值；包含 typed nil 指针的接口不一定等于 nil。这是进阶陷阱：排查依赖装配时，要区分接口本身为空与它装了一个空指针。不要为了学这一点修改业务装配。

### 3.8 `defer`、闭包、Context、goroutine 与 channel

`defer f()` 注册在当前函数返回前调用 f；多个 defer 通常后注册先执行。普通参数在注册时求值；闭包捕获变量时还要考虑它之后的变化。常见用途是关闭资源、回滚未提交事务、停止计时器。

真实中间件 `requireAuth(next)` 返回一个 Handler 闭包：闭包保留对 next 的访问，收到请求后先认证，认证成功才调用 next。不是用户注册时把所有后续业务一次执行完。

`context.WithCancel` 或 WithTimeout 创建可取消/有期限的 Context；取消后 `ctx.Done()` 可被等待到。这不是自动把数据库和外部世界恢复原状。执行器、网络请求、SQL 驱动等需要在各自边界响应取消。

`go f()` 启动 goroutine。channel 像有类型的交接通道：`ch <- value` 发送，`<-ch` 接收；无缓冲时通常要双方交接，容量 1 则允许暂存一个值。`select` 等待多个可能就绪的通信分支，它不是把所有分支依次执行。

读取 nil channel 的分支永远不会就绪，因此源码有时用 nil channel 关闭某个可选等待分支。不要把它误读为等待 0 秒。具体计时器、取消和结果竞争见第 04 章。

## 4. Web：把 TypeScript 和 React 读成页面行为

### 4.1 类型不等于网络数据校验

TypeScript 在开发时检查很多类型错误；浏览器最终执行 JavaScript。把 `await response.json()` 断言成某个类型，不会自动验证远端确实返回那些字段。因此实际代码仍要检查 status、error、agent_run、job 等契约。

`const` 不允许重新绑定变量名，但对象内部不因此自动不可变。`let` 可重新赋值。箭头函数 `(value) => ...` 是函数表达式，常传给状态更新、map、事件处理器等。

### 4.2 展开、解构、可选链与合并

教学示例：

```ts
const { message, agent_run } = payload;
const next = [...previous, message];
const id = agent_run?.id;
const chosen = override ?? current;
```

解构从对象取指定字段；展开语法创建新数组，但里面的对象通常还是原引用。`?.` 在前面是 null/undefined 时停止访问并得到 undefined；`??` 只在左边 null/undefined 时取右边，不会把 0、false、空串都替换掉。它与 `||` 的假值规则不同。

聊天消息去重应基于服务器 ID。两条文字相同但 ID 不同的消息应能同时存在；同一个 ID 的状态更新则应合并。这也是刷新后加载与实时观察同时到达时需要合并的原因。

### 4.3 React state 为什么可能“读到旧值”

组件渲染得到本次状态值和事件处理函数。处理函数的闭包看到的是相应渲染的值；调用 setter 请求下一次状态更新，不代表当前函数内已有变量立即换值。

教学示例：

```ts
setConversationID(newID);
await observeRun(runID, newID);
```

显式传 newID 可以避免后续逻辑依赖尚未提交的新 state。真实代码的 conversationOverride 就解决此类恢复场景；不要概括为“React 总是延迟固定几毫秒”，实际不是这个契约。

### 4.4 await、HTTP 错误与网络错误

教学示例：

```ts
const response = await fetch(url, options);
const payload = await response.json();
if (!response.ok) throw new Error("请求被拒绝");
```

第一个 await 等网络响应，第二个 await 等响应体解析。服务端返回 409/500 时 fetch 通常不因状态码本身 reject；代码要检查 ok。连接失败会以异常进入 catch；JSON 解析失败也可能进入 catch。不同异常可能处于不同持久化时点，所以报错后不能都直接重发写请求。

轮询是反复 GET 状态；SSE 是保持一个响应并持续接收事件。本项目普通生成事件流不应自动称为“逐 Token 原样流”，Agent 页面当前则按 Run 状态轮询。协议、事件粒度、模型输出粒度是不同维度。

## 5. Python：把状态图拆成普通函数

### 5.1 缩进、容器、条件和注解

Python 用缩进表示代码块，冒号开始 if/for/def 的主体。dict 类似键值映射，list 是有序列表；`value is None` 判断是否为 None，`==` 判断相等；`and/or` 同样有短路行为。

`def f(context: Mapping[str, Any]) -> list[Any]:` 中注解说明预期类型，不自动验证输入。`context.get("history", [])` 在键不存在时给默认值；键存在但值是 None 时仍返回 None，因此接下来 `isinstance(raw, list)` 很必要。

Python 的 bool 是 int 的子类，所以 `isinstance(True, int)` 为真。诊断计数校验使用“int 且不是 bool”，避免把 true 当成数量 1。看似多余的检查有具体语言原因。

### 5.2 推导式、切片和浅复制

教学示例：

```python
names = [item["name"] for item in items if item.get("enabled")]
recent = history[-20:]
selected[0:0] = group
copied = dict(context)
```

第一行循环 items，只保留 enabled 为真值的项并取 name。第二行保留最后最多 20 个元素，不够 20 也不会越界。第三行在开头空区间插入 group，而不是把 group 作为一个嵌套元素追加。第四行复制外层字典；内部 list/dict 仍可能共享引用，修改 copied 中嵌套列表可能影响 context。

`cast(T, value)` 不会转换对象或检查其内容；`dict(value)` 才是具体构造，但也不是任意对象都可转字典，更不是深复制。第 12 章有可运行验证。

### 5.3 Graph 的五个词

| 词 | 代码层意思 | 生活助手例子 |
|---|---|---|
| state | 多个步骤共享的结构化执行信息 | 输入、已选工具、结果、预算 |
| node | 执行一个步骤的函数 | decision、approval、commit_tool |
| edge | 决定接着去哪个节点 | 需要审批就等待，否则继续 |
| checkpoint | 恢复所需状态与执行位置的持久化记录 | 等待确认时保存 |
| interrupt / resume | 请求外部输入并在之后继续 | 用户批准/拒绝后恢复 |

这不是本项目完整节点清单，而是理解框架概念的最小词表。真实节点、路由和约束看第 11 章。可恢复不代表任意节点里的外部动作都天然只执行一次；节点恢复可能再次到达某个调用，副作用必须由业务边界保护。

## 6. HTTP 与 SQL：跨进程之后不能靠“我以为”

### 6.1 HTTP 请求的四部分

方法说明动作类别；路径定位资源；Header 传认证/内容类型/幂等键等元信息；Body 传业务 JSON。服务端返回状态码与正文，二者合起来才构成契约。不要在日志或截图中展示真实 Authorization、Cookie、刷新令牌。

| 状态 | 阅读本项目时的常见含义 | 下一步 |
|---|---|---|
| 200 | 同步成功或读取成功；也可为幂等复用 | 看正文结果 |
| 202 | 已接受后台处理 | 查任务状态 |
| 401 | 未获得有效认证 | 检查会话/刷新路径 |
| 403 | 身份已知但无相应许可 | 检查角色/资源权限 |
| 404 | 不存在，某些路径也用它隐藏无权资源 | 不猜其他用户资源 |
| 409 | 当前状态/版本冲突 | 读冲突对象，不能一概盲重试 |
| 422 | 业务输入无法按契约处理，例如附件恢复失败 | 修正输入 |
| 429 | 频率等限制命中 | 看 Retry-After 与具体错误 |
| 503 | 依赖或功能当前不可用 | 查装配与服务状态 |

表是阅读导航，不是全项目每个错误的穷举映射；具体仍以 Handler 和公共错误映射为准。

### 6.2 SQL 占位参数为什么重要

教学示例：

```sql
UPDATE agent.runs
SET status = 'completed'
WHERE id = $1 AND user_id = $2 AND revision = $3;
```

表内每行是一条记录；SET 决定改什么，WHERE 决定哪些行有资格被改。`$1` 等是绑定值的位置，不是把用户文本拼成 SQL 语法。`ExecContext(ctx, sql, runID, userID, expected)` 的参数位置必须与编号对应。

`RowsAffected()==0` 可能是 ID 不存在、归属不符或版本过期；不代表数据库引擎一定故障。代码通常按领域语义映射为 NotFound/Conflict。`QueryRow...Scan` 负责读取列到变量，列的次序与目标类型也必须对齐。

### 6.3 事务、唯一约束、行锁、CAS 分别保护什么

事务保证这次提交中的关联数据库写入整体生效。唯一约束防止某键出现不允许的重复；业务层先查不存在再插入仍有并发间隙，最终要看数据库约束/冲突处理。行锁让争用同一现存行的短事务协调；`SKIP LOCKED` 允许领取者跳过别的事务已经锁住的候选。

CAS/版本条件防止基于旧状态的写覆盖新状态。租约限制长任务在某段时间内谁可执行。后两者常组合：版本没变但租约已过期也不能提交；租约有效但 revision 已变也不能提交。

`defer tx.Rollback()` 是兜底。Commit 成功后再 Rollback 不会撤销已提交结果；中途 return 时它会撤销未提交写入。但“提交报错”有时需要区分明确失败和通信中断造成的结果不确定，应用仍须凭数据库身份/幂等信息核实。

### 6.4 持久化不等于所有东西都在一个事务里

本项目文件、索引、模型、邮件与 SQL 有独立边界。先发邮件再写 sent 状态，中间中断可能重复发；先保存文件再提交任务元数据，提交失败需要清理/恢复。不要把“用了 PostgreSQL”概括成“任何失败都没有副作用”。

Outbox 将业务事实与派发意图放进同一数据库事务；Relay 之后投递。Inbox 记录处理过的事件；仍要看记录与业务操作是否同一事务。第 10 章专门展开这些窗口。

## 7. AI 相关技术：每个名词要落到一个变量或检查上

### 7.1 Prompt、Persona、历史、记忆、知识

Prompt 是本次模型输入的组织；Persona 是角色配置编译出的行为描述；History 是已保存对话中选中的消息；Memory 是可召回的长期事实；Knowledge 是文档或随版本发布的资料。这些不会因为存在数据库就自动进入模型，需要 BuildContext 和节点投影显式选择。

来源资料中的指令性文字不能扩大工具权限。模型层用说明划分资料与指令；可信代码层仍检查身份、工具范围、确认与执行权。这是多层约束，不是“写一句系统提示即可百分之百安全”。

### 7.2 RAG：先找资料，再带资料生成

RAG 在此可理解为“检索增强生成”：查询 → 允许来源集合 → 检索候选 → 排序/裁剪 → 带来源交给模型 → 生成回答。向量相似只是找到相关文本的一种手段，不证明事实真实，也不证明用户有权看。

Chunk 是片段；embedding 是数值表示；rerank 是再次排序；citation 是指向来源的引用；Source IR 是后续处理可稳定引用的中间来源结构。每个概念解决不同问题。完整文件制品还要求轮次与覆盖清单，不可仅看检索得分。

### 7.3 Tool call、Skill、Agent 与预算

Tool call 是模型/执行图提出的一个结构化动作；Skill 是承载具体算法和交付约束的任务能力；Agent 将多步串成用户目标。工具注册表决定有哪些能力，Schema 校验参数形状，业务服务决定权限和副作用。

预算可能按输入/输出 Token、模型成本、执行时长、步骤数、文件大小等限制，不能用一个数字代表全部。窗口预算要预留输出和安全余量；小输入也可能因工具 Schema 很大而使请求整体超限。

### 7.4 这份项目代码没有承诺什么

开发模式固定响应不代表真实模型效果；本地部署应用不等于本地离线推理；可选语义服务代码存在不代表已启用；Graph completed 不证明文件视觉质量完美；测试名称含 exactly once 不代表所有分布式外部动作都有严格一次语义。

这些不是否定项目功能，而是准确划分每个实现和测试能证明的范围。读懂边界比背会框架名称更接近“把技术啃透”。

## 8. 怎么知道自己真的读懂了

选任一行为，不看文档口述：

1. 一组具体输入，进入哪个函数？
2. 哪个条件会提前拒绝，为什么必须放在写入前？
3. 成功后新增/更新哪些记录，它们分别存什么？
4. 返回给用户的是什么，后台还欠哪些步骤？
5. 页面关闭、模型超时、Worker 接管、重复请求各会怎样？
6. 哪个测试或数据库条件支持你的判断，哪个部分只是推测？

先完成第 01–04 章主链路，再回来运行 [三个隔离实验](12-worked-cases-and-labs.md)。实验在教学目录内，不启动服务、不调用模型、不连接数据库；真实工程测试与简化演示的证明范围会分开说明。

[返回目录](README.md) · [开始读第 01 章](01-startup-identity.md)
