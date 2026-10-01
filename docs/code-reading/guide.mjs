// 人工编写的源码讲解。范围必须连续；build.mjs 只负责摘录与排版。
const S = (file, title, notes, intro = '', after = '') => ({ file, title, notes, intro, after });

export default [
  {
    slug: 'startup-identity', title: '启动、登录与角色', focus: '依赖注入、Token/Session、Persona 版本',
    intro: '行为例子：用户打开页面，注册账号并创建一个生活助手。先读启动装配，确认下面每次调用的 Service/Store 从哪里来，再追用户身份和角色。',
    sections: [
      S('cmd/api/main.go', '启动时选择持久化实现', `48-59|每行把一个具体内存 Store 转为对应领域接口；identity、character、conversation 等变量的静态类型是接口，因此稍后能替换为同一 PostgreSQL Store。
60-65|创建文档文件存储；初始化失败记录错误并退出。成功后转为 BlobStore 接口，业务层无需知道目录细节。
66-69|声明可选持久化、日志、事件与性能 Store；零值为 nil，后面按实际能力装配。
70|只要配置不为 memory，就进入持久化分支。
71|给数据库连接设置 10 秒截止时间，防止启动无限等待。
72|按 driver 打开数据库；连接串由配置传入，领域服务不直接读取环境变量。
73|连接完成立刻释放本次连接 Context 的定时器。
74-77|连接失败终止进程，不带着部分内存状态继续提供服务。
78|注册 main 正常返回时关闭数据库连接池；若直接调用 os.Exit，Go 不会执行 defer。
79-90|逐行将所有业务 Store 指向 persistentStore。接口一致，运行实例共享持久化底座。
91-93|记录当前采用持久化还是内存模式，便于运行时排查。
94-98|内存模式关闭持久化 Skill Worker：API 与另一进程的内存不共享，入队后无法由另一进程领取。`),
      S('internal/identity/service.go', '注册：输入转为用户和会话', `116|注册服务接收 Context 和 RegisterInput，返回令牌对或错误。
117|统一邮箱格式，减少大小写、空白造成的重复身份。
118-120|共同校验邮箱、密码和时区；失败在创建用户之前返回。
121-123|昵称必须非空且不超过 40 个 Unicode 字符；rune 计数避免中文按 UTF-8 字节误算。
124-127|密码先变为哈希，失败用 %w 包装并保留原错误链。
128|生成统一 UTC 时间；s.now 可在测试中替换。
129-132|生成新用户 ID；失败不落库。
133|组装 User：昵称去空白、默认语言 zh-CN、状态 active，只保存 PasswordHash。
134-136|通过 Store 创建用户；唯一邮箱等数据库约束在 Store 层生效。
137-138|创建会话并返回令牌。注意 CreateUser 与 createSession 是先后调用；这里没有跨二者的同一事务保证。`),
      S('internal/identity/password.go', '把密码变成可验证、不可直接还原的记录', `19|输入是用户密码字符串，输出是编码后的派生记录与 error；成功也不返回原密码。
20|make 分配 16 字节盐值空间；此时每个字节为零，下一行才填入随机数。
21|rand.Read 写入 salt；_ 丢弃字节数，但仍检查错误。if 初始化中的 err 只在这个 if 范围内使用。
22-23|随机数读取失败立刻返回空字符串和包装错误；不能拿全零盐继续运行。
24|用 SHA-256 作为 PBKDF2 内部函数，结合密码、随机盐、600000 次迭代和 32 字节长度得到 key。它不是把密码加密后等待解密。
25-27|派生失败也终止；没有有效 key 就不能创建密码记录。
28|Sprintf 把算法、迭代次数、Base64 盐和 Base64 key 用 $ 分隔；第二个返回值 nil 表示本次成功。盐与参数一起存储是为了将来能用同样方法验证。
29-30|结束生成函数，空行分隔下一个行为。
31|验证函数同时接收已存记录 encoded 与本次用户输入 password，返回是否匹配。
32|按 $ 拆出四部分，不是按点拆 Access Token。
33-35|字段数或算法标记不匹配就拒绝；不能把不认识的数据当有效密码记录。
36|Atoi 把第二部分从十进制字符串转为整数。
37-39|解析错误，或迭代数低于 100000、高于 2000000 时拒绝；这是本实现接受的参数边界。
40|把第三部分从 Base64 文本还原为盐的原始字节。
41-43|解码失败或盐长度不足 16 字节时拒绝。
44|第四部分解码为数据库保存的期望结果 want。
45-47|要求 want 恰好为配置的 32 字节。
48|用本次输入密码、原盐和原迭代数重新派生 got；这里不是反向还原原密码。
49|必须计算成功并且 got 与 want 匹配；&& 短路意味着发生错误后不再进行后一个判断。hmac.Equal 用于比较密码学字节结果。
50|验证函数结束。`, '先区分原文、编码、哈希、带密钥的认证码。第 00 章有对照；不要把“Base64”“哈希”“加密”当成同义词。'),
      S('internal/identity/token.go', 'Access Token 的字段、签发与验证', `18|定义一个结构体类型；类型声明本身不创建任何用户会话。
19|Go 字段名为 UserID，JSON 输出键为 sub；反引号中的标签指导序列化，不是字段值。
20|sid 保存 Session ID，让请求能回查数据库会话。
21-22|exp 与 iat 是 Unix 秒时间，用 int64 存储；前者控制到期，后者记录签发时刻。
23-24|结束字段集合，分隔下一类型。
25|TokenManager 是签发与验证所需配置的组合。
26|secret 为字节切片，供 HMAC 使用；小写字段名不从包外直接访问。
27-28|accessTTL 表示从签发到过期的时长，而不是绝对时间点。
29-32|构造函数把字符串密钥转为字节切片，按值返回 TokenManager；尚未签发任何 Token。
33-34|IssueAccess 是 TokenManager 的方法；括号中的 m 是接收者，最后括号声明三个返回值类型。
35|过期点等于 now 加 accessTTL；now 由调用者提供便于测试。
36|创建本次声明对象；userID 与 sessionID 来自可信服务逻辑，而不是 Token 自己证明出来的。
37|json.Marshal 把结构体序列化为 JSON 字节；payload 是字节切片。
38-40|序列化错误时，三个返回位置分别给空字符串、零时间和错误。
41|Base64URL 把 JSON 字节变成适合传输的文本；任何拿到它的人都可以解码，这不是加密。
42|对编码后的 payload 计算 HMAC 认证码。
43|本项目格式为 v1.payload.signature；不是标准 JWT 的 header.payload.signature，不能仅凭三段就叫 JWT。
44-46|结束签发方法，开始解析方法；解析成功返回结构化声明。
47|按点切分输入字符串，结果为字符串切片。
48|依次检查段数、v1 标记、HMAC；|| 短路保护 parts 下标：段数不是 3 时不执行后续访问。签名比较也不会证明会话尚未撤销。
49-50|任一条件失败就返回空声明与统一 InvalidToken。
51|签名匹配后，把 payload 从 Base64URL 解码回 JSON 字节。
52-54|不合法编码直接拒绝。
55|声明零值 AccessClaims，等待下一行填充。
56|&claims 把变量地址交给 Unmarshal，允许其修改 claims；同时检查两个 ID 非空、当前时间严格早于 exp。if 内部的 err 属于这个 if 的作用域。
57-58|JSON 不合法、缺少核心字段或已到期，都拒绝。
59-60|返回声明与 nil；后续 Authenticate 仍需查询 Session/User。
61-62|新函数使用命名返回值 plain、hash、err；它生成的是 Refresh Token。
63|分配 32 字节随机原文的缓冲区。
64-66|rand.Read 填充它；读取失败不返回一个可用令牌。
67|把随机字节编码成客户端可持有的文本。
68-69|返回原文与原文哈希；原文用于客户端刷新请求，数据库保存哈希。
70-71|声明对 Refresh Token 原文计算哈希的纯函数。
72|SHA-256 产生固定大小字节数组；输入 string 先转为 []byte。
73-74|sum[:] 把数组视为切片，十六进制编码成存储文本。
75-76|sign 是包内方法；接收要认证的 payload 字符串。
77|创建带服务端密钥的 HMAC-SHA256 计算器；sha256.New 是函数值。
78|写入 payload；这里的 Write 接口返回字节数和错误，但该 HMAC 实现不会因普通字节写入返回错误，代码显式丢弃它们。
79-80|Sum(nil) 取得当前认证码，再编码为 Base64URL 文本。`, '本段能帮助看懂结构体标签、多返回值、切片、短路逻辑与指针。不要把任何真实 Token、密钥或密码粘贴到公开解码网站。'),
      S('internal/identity/service.go', '登录后建立设备和 Session', `235|注册与登录共用此函数，身份已由上游确定。
236-238|平台只能为空或 web/ios/android/service，非法平台立即拒绝。
239|固定本次操作时间。
240-243|先生成备用设备 ID。
244|按用户和 DeviceKey 更新/创建设备；缺省名称、平台、时区和 LastSeen 在这里落入结构体。Upsert 可能返回已有设备，因此后面使用 device.ID。
245-247|设备写入失败结束本次会话建立。
248-251|生成 Refresh Token 原文与哈希；原文只交给客户端，哈希用于数据库查找。
252-255|给新 Session 分配独立 ID。
256|Session 绑定用户、设备、刷新令牌哈希、过期时间和使用时间。
257-259|先写 Session，失败不签发可用的 Access Token。
260-263|Access Token 绑定 user.ID 与 session.ID，便于撤销时回查 Session。
264-265|一次返回两个 Token、两个过期时间和用户资料。Refresh Token 原文出现在响应中，不在 Session 记录里。`),
      S('internal/identity/service.go', '刷新令牌：轮换旧会话', `152-154|获取当前时间，把客户端刷新令牌哈希后查 Session；不是用明文查库。
155-157|不存在、已撤销或到期均统一视为未授权。
158-161|重新读取用户；用户不存在或无法读取时拒绝刷新。
162-165|生成下一对刷新令牌原文/哈希。
166-169|生成新 Session ID，而不是重用旧记录。
170|保留用户和设备关系，新建有效期及 TokenHash。
171-173|Store.RotateSession 负责旧会话失效与新会话创建；并发刷新冲突映射成未授权。
174-177|签发绑定新 Session 的 Access Token。
178-179|把新的 TokenPair 返回客户端；前端需替换旧令牌。`),
      S('internal/httpserver/auth.go', '每次业务请求怎样获得身份', `116-117|这是中间件工厂：接收后续 Handler，返回一个包住它的新 Handler。
118-119|读取 Authorization，并只切成 scheme 与 token 两段。
120-123|要求 Bearer 格式；失败写 HTTP 错误并终止，不进入业务。
124|Authenticate 校验 Token 并查 Session/User；不能只靠 Token 签名判断当前会话仍有效。
125-128|认证失败由公共错误映射函数转换响应。
129|Touch 在线状态属于尽力而为，返回错误被显式忽略，因此在线状态更新失败不阻断业务。
130|将 authenticated 结构放入 Context，再调用 next。后续 currentAuth(r) 取得的是这里建立的可信身份。
131-132|关闭闭包与工厂函数。`),
      S('internal/character/service.go', '创建和版本化角色', `78-79|创建角色先按 companion/life/work 填默认值。
80-82|验证角色字段；失败返回两个空结果。
83-86|生成 Character ID。
87-88|创建第 1 版 Character，并固定创建/更新时间。
89|compile 把角色字段编译成 PersonaVersion；它不是模型调用，而是结构化配置转换。
90-92|Store 同时接收 Character 和 Persona，保存当前角色及版本快照。
93-94|返回角色和已编译 Persona。`),
      S('internal/character/service.go', '修改角色为什么保留旧版本', `134-138|带 userID 读取原角色，避免修改其他用户角色；读取失败终止。
139-141|旧客户端没传 Module 时保留原模块。
142-144|新输入仍须通过同一校验。
145|保留 ID、用户、创建时间，PersonaVersion 加 1，并更新其余字段。
146|使用新版本角色编译新的 Persona 快照。
147-151|保存两个对象并返回；旧 Persona 版本因此可继续被历史运行引用。`),
    ],
    end: '前端登录/注册入口：web/app/companion-client.tsx 的 login、register、storeSession、refreshSession。继续追 internal/identity/service.go 的 Login、Authenticate、ChangePassword；数据库细节在 internal/persistence/postgresstore/identity.go。认证与资料相关测试可从 internal/httpserver/auth_profile_test.go 阅读。'
  },
  {
    slug: 'chat-intake', title: '从发送按钮到聊天入口', focus: '前端恢复、鉴权、附件、配额、Agent 分流',
    intro: '行为例子：用户给工作助手发送“根据这个文件做一份演示”。前端先上传附件，随后发送文档 ID；Go 根据当前角色模块和制品请求决定执行路径。',
    sections: [
      S('web/app/chat-panel.tsx', '浏览器发送一条消息', `25|依次阻止表单刷新、取得表单和文本；文本与附件都为空、会话未建立、有运行中任务或正在上传时直接返回；否则清空错误/工具提示并显示“正在回应”。
26|依次 POST content/document_ids → 解析响应 → 若 409 附带 active Agent 则恢复观察旧运行 → 其他非成功响应抛错 → 清空表单和待发附件 → 按消息 ID 合并服务器确认的用户消息 → 有 agent_run 则轮询，没有则用 job 建 SSE；两者都没有属于响应契约错误。
27|捕获上述任一异常，保存人类可读错误与失败提示；这里的 catch 不会自动重发原 POST，避免无意重复输入。`, '', '同一源码行 26 有多条语句，按解释中的箭头顺序阅读。form.reset() 发生在服务端已接受之后；请求失败时输入不会提前被清空。'),
      S('internal/httpserver/conversations.go', '发送接口的共同前置检查', `60|HTTP Handler；w 写响应，r 携带路径、请求体和已认证 Context。
61|以 chat:user:用户ID 为限流键，请求共享限流网关；返回剩余次数与重试时间。
62-65|限流依赖不可用时返回 503；当前行为没有继续执行。
66|把剩余配额写入响应头，便于客户端显示或诊断。
67-71|超过频率限制则返回 Retry-After 与 429，并立即停止。
72-75|匿名结构只接受业务所需 content 和 document_ids。
76-78|统一解析 JSON，解析失败由 decodeJSON 写错误。
79|读取可信认证结果。
80-82|先检查模型成本额度；这是接受前检查，不等于对未来模型费用做事务预留。
83-86|附件最多 3 个，前后端都检查。
87-89|保留用户可见文本；若无文档 ID，尝试修复旧消息中的可见附件名称。
90-92|恢复函数按当前用户和可见文本找文档，不全库模糊猜测。
93-100|无法关联旧附件时返回 422，要求重新选择；不会静默继续一个没有来源的文件任务。
101-103|逐个用 userID 获取文档；这一步验证资源归属。
104-109|任一文档不可访问就终止，否则加入已验证文档列表。
110-112|只有附件没有文本时，填充“请处理这个文件”。
113-116|以可见文本为基础，追加可信的文档标记，供后续 Agent/工具定位附件。`),
      S('internal/httpserver/conversations.go', '分流、构建上下文并返回 202', `117|检测文件生成等必须进入持久化 Agent 的行为。
118-123|只要配置启用了任一 Agent 模块或本次强制走 Agent，就先取得当前会话。
124-127|会话不存在或无权访问时返回错误。
128-132|按会话的 CharacterID 读取用户的角色配置。
133-136|角色不可访问时按会话未找到处理。
137|最终按当前角色模块判断：配置启用该模块，或 work 模块且强制制品 Agent。
138-140|此时再检查 Agent Run 月度额度。
141-144|已要求 Agent 但服务未装配时返回 503；不会伪装为成功。
145-147|查询同一会话已有的活跃运行。
148-154|已有运行返回 409 和公开版 Run；前端据此恢复轮询，而不是新建第二个正在处理的输入。
155-158|只有“没有活跃运行”的 ErrNotFound 才允许继续，其他存储错误不能当成空队列。
159-162|执行聊天内容长度与安全校验。
163-168|调用共用 BuildContext：传当前文本作为 query，历史与当前请求分开保存。
169-172|上下文构建失败立即返回。
173-175|交给 Agent 服务，传用户、会话、角色、模块和最终消息正文。
176-180|Context 同时包含时区、Persona 系统提示、兼容 history 和新 conversation_context 快照。
181-187|邮箱显示名/默认语言形成邮件上下文，随后结束 AcceptChat 调用；这不是发送邮件。
188-191|接受阶段错误映射为 HTTP 错误。
192-195|返回 202、持久化用户消息和 publicAgentRun。此时任务只被接受，模型未必已经运行。
196-197|结束 Agent 分支。
198-202|未进入 Agent 的请求走普通 conversation.Send；错误仍统一映射。
203-204|普通路径也返回 202，但结果字段是 job 而不是 agent_run。`),
      S('web/app/chat-panel.tsx', 'Agent 状态怎样恢复到页面', `57|允许调用方显式传 conversationOverride；页面刚恢复会话时 React state 可能尚未提交，不能只依赖旧闭包里的 conversationID。
58|持续 GET Agent Run；错误响应抛异常，成功读取则重置网络失败计数。
59|遇到 waiting_approval，取待确认请求，以 Run ID + revision 去重弹框；提交 approved 和 Idempotency-Key。提交失败撤回本地去重标记，之后可以再试；拒绝也作为合法决策发给服务端。
60|completed 时重新从数据库消息接口加载最终回答、合并去重、解除输入锁，显示工具结果提示。
61|cancelled 同样刷新消息并清除活跃 Run；保留历史对话。
62|timed_out 显示可重试提示，清除活跃 ID；原运行和原消息仍保留。
63|failed 与超时分别提示，用户可在原消息下重试。
64|未终结状态只更新等待文案，不把 queued 或 cancel_requested 当作完成。
65|健康状态每隔 400 毫秒再次读取；这是轮询，不是 token 流。
66-67|网络错误最多连续容忍 4 次，退避 400–1600ms；第 5 次清除当前活跃指示并抛出。服务端运行不会因为浏览器断线自动消失。`),
    ],
    end: '观察输入/输出结构比只看函数名更有用：普通返回 {message, job}，Agent 返回 {message, agent_run}。页面加载时 ensureConversation → loadMessages → loadActiveAgentRun；历史通过 sequence+bubble 游标分页，mergeMessage 依据 ID 去重。相应函数位于 chat-panel.tsx 文件尾。'
  },
  {
    slug: 'agent-transactions', title: 'Agent 入库、领取与完成事务', focus: 'Run 快照、sequence、Outbox、租约、CAS',
    intro: '行为例子：发送接口返回 202 后，即使浏览器关闭或 API 重启，任务仍应能被 Worker 找到。这个保证建立在消息、运行记录、事件和调度数据的事务上。',
    sections: [
      S('internal/agent/chat.go', 'AcceptChat 创建持久化运行', `26-31|依次规范化用户、会话、角色、模块和内容字段。
32-35|核心 ID 不能为空，内容不能为空且最多 8000 个 Unicode 字符。
36-38|只接受 companion/life/work 三个模块。
39-42|要求底层 Store 实现 ChatStore，才能原子保存消息和 Run；缺此能力返回冲突。
43-46|生成 Message ID。
47-50|生成另一个 Run ID，消息与运行是不同实体。
51|固定本次 UTC 时间。
52-56|用户消息已经是 completed，因为接收到的输入完整；这与助手是否生成成功无关。
57-61|运行输入封装 message_id、text 与上游 Context。
62-65|JSON 序列化用于持久化；失败不产生半个运行。
66-70|创建 Run，ThreadID 等于 RunID，是 LangGraph 检查点的运行身份；记录模块和 Graph 版本。
71|初始 accepted；幂等键使用本次新生成 Message ID。注意这不等于整个发送 HTTP 接口按客户端请求键去重。
72-74|立即可领取，设置最终截止时间、初始 revision=1 和时间戳。
75-77|捕获当前 Agent Definition 版本与快照；失败停止接受。
78-80|捕获 Model Profile 快照；长任务恢复时不随新发布配置漂移。
81-82|交给 Store.AcceptAgentMessage，下面进入真正事务。`),
      S('internal/persistence/postgresstore/agent_chat.go', '同一事务保存输入和调度依据', `14-18|开启事务，失败立即返回。
19|注册兜底回滚，任何中途 return 都不会留下事务中的部分写入。
20-21|准备读取会话序列及角色 ID。
22-28|用 conversation_id、user_id、active 状态锁定会话行，同时读取 next_sequence。FOR UPDATE 使同会话的序列分配串行。
29-34|没有匹配会话返回领域 NotFound，其他 SQL 错误原样返回。
35-38|再次核对 Run 与 Message 的用户、会话、角色关系，不只信任上层构造正确。
39|给用户消息赋予刚锁定的 sequence。
40-48|INSERT app.messages：参数按字段列表一一对应；SQL 参数绑定不拼接用户文本。
49-50|消息写入失败回滚。
51-60|INSERT agent.runs 的列包含 Graph、Agent 配置、模型配置、输入、时间和 revision，足以恢复运行。
61-66|空可选字段用 NULLIF 转 NULL，UUID 字段显式转换；这是 SQL 值占位，不是字符串模板插值。
67-75|按顺序绑定 Run 的完整快照和状态。
76-78|Run 插入失败则前面的消息 INSERT 也回滚。
79-85|追加 accepted 事件，记录配置版本和指纹用于诊断。
86-87|事件记录失败同样终止事务。
88-90|写入 Agent 调度 Outbox；消息与派发意图同生共死。
91-98|更新会话 next_sequence 为 sequence+2，给该轮助手回复预留后一序号，同时更新最近消息时间。
99-101|只有全部步骤成功才 Commit。
102-103|返回已分配 sequence 的消息和运行；这时上层才能返回 202。`, '', '这里的 FOR UPDATE 直接保护序列分配。不能仅凭这段锁就声称同一会话并发发送绝对只产生一个 Run；活跃运行限制还需看入口检查与数据库约束。'),
      S('internal/persistence/postgresstore/agent.go', 'Worker 用 SKIP LOCKED 领取下一条', `161-166|开启领取事务并注册回滚。
167-168|计算租约到期点，准备接收返回的 Run。
169-178|CTE 挑选 available_at 已到、deadline 尚未到的 accepted/queued/waiting_tool，或租约已过期的 running。
179-181|按可执行时间排序，锁定一条候选并跳过其他 Worker 已锁的行。
182-188|同一 SQL 更新为 running、写租约 owner/expiry、revision 加 1、清理错误，再 RETURNING 完整 Run。
189-194|没候选是 ErrNotFound，不是系统故障；其他 SQL 错误需要上报。
195-198|记录 running 事件、owner、revision，source 标明来自数据库 reconciler。
199-203|事件成功后提交，返回带新 revision 的执行权。`),
      S('internal/persistence/postgresstore/agent.go', '完成时防止迟到 Worker 覆盖', `220-225|完成操作也使用独立事务及兜底回滚。
226-232|准备更新：status=completed，保存 output，清除恢复参数/租约，revision 加 1，并设置 completed_at。
233-237|WHERE 必须同时匹配 running、owner、revision、有效租约和未到 deadline。即使模型返回成功，失去执行权的 Worker 也不能提交。
238-243|UPDATE 未命中返回 ErrConflict；区分业务竞争与 SQL 故障。
244-248|追加 completed 事件，仍在事务内。
249-251|把 Agent 输出转成助手消息，同事务写入。Run 完成和用户可见回复不会只成功一半。
252-255|最终提交并返回终态 Run。`),
    ],
    end: '继续追 agent.go 中 appendAgentAssistantMessage：助手消息通过原 Message ID 建 reply_to，使用为本轮预留的序号。读 agent/service.go 的 captureAgentDefinition、captureModelProfile 理解快照内容；读 agent/worker_test.go 的取消、deadline 和接管案例验证这些 WHERE 条件的意义。'
  },
  {
    slug: 'agent-approval-recovery', title: '工具审批、恢复、取消与重试', focus: 'Go 网关、Python interrupt、持久化控制',
    intro: '行为例子：生活助手识别一笔账单，等待用户确认，再写入账本；用户也可能在等待模型时点击停止。Go 保存执行权限和业务结果，Python 使用检查点选择下一节点。',
    sections: [
      S('internal/agent/tool_gateway.go', '每次工具调用重新验证执行权', `331-334|拒绝未装配依赖或确认密钥过短的网关。
335-338|规范化并验证 Run ID。
339-342|从权威 Store 重新取 Run，不接受 Python 自报的用户或状态作为依据。
343-348|同时检查 running、预期 revision、有效租约和总 deadline；任何一项不符返回冲突。
349-352|解析持久化 Run.Input，畸形 JSON 属于输入契约错误。
353-357|要求有 Message ID 和文本，防止工具失去来源。
358-364|只留下非空 user/assistant 历史；过滤其他角色，避免来源数据冒充高权限消息。
365-367|工具网关的兼容 History 只保留最近 20 条；这与模型层统一 Token 预算快照是不同用途。
368-373|返回 Run 和可信 ToolRequest，其中用户、会话、角色和模块均来自数据库。`),
      S('internal/agent/tool_gateway.go', 'Prepare：执行只读/低风险步骤或产生确认', `136-140|先做上一节的执行权检查。
141-145|清理 CallKey/ToolName 并限制长度；CallKey 标识图内具体动作。
146-148|工具名必须属于 Go 为当前模块返回的 ModelTools；模型无法自行创造工具权限。
149-151|缺失参数规范化为空 map。
152-155|把 action CallKey 用作本次业务任务幂等边界；同一动作重放复用 Skill，不同附件动作可创建不同任务。
156-161|调用真正的业务 Tool Executor。Prepare 并非绝对只读：可读数据、创建候选、启动允许的低风险任务；失败立即返回。
162-168|如果没有通用 Confirmation，但返回的是等待确认 Skill，包装一个 skill_run 审批。
169-172|未要求审批时，保证 Response 非空。
173-177|返回 Preparation completed；这表示准备调用已产出结果，Data 中的异步 Skill 仍可能 queued/running。
178-179|其他需确认结果签名为中风险审批请求。`),
      S('workers/python/src/ai_companion_worker/agent_runtime.py', 'approval 节点：暂停并选择分支', `6021-6024|声明只可能跳转 commit_tool/reject_tool 的节点，并读取已准备好的工具动作。
6025-6029|interrupt 产生 tool_approval 中断，携带 Graph 身份；图检查点保存后把控制权交回 Worker。
6030-6034|记录模型和工具目录指纹，恢复时校验运行上下文没有被替换。
6035-6042|审批数据携带 Run、工具、风险、摘要、参数和服务端确认令牌。浏览器可见版本还要经 publicAgentRun 脱敏，不能把这个内部 payload 原样当公开接口。
6043-6044|恢复时严格要求 approved 是布尔值，字符串 true 不被接受。
6045-6051|把审批决策和恢复计数写入下一状态。
6052-6061|追加 resumed 节点轨迹，并递增已执行步骤数。
6062-6064|批准返回 Command 跳 commit_tool；拒绝跳 reject_tool。拒绝是正常业务分支，不是网络异常。`),
      S('internal/httpserver/agent_runs.go', '浏览器确认怎样送入持久化恢复', `50-54|Agent 服务不可用返回 503。
55-57|Approved 用 *bool，因此可区分没有字段和明确 false。
58-64|解析并拒绝缺失字段，避免默认 false 掩盖客户端请求错误。
65-71|把用户 ID、Run ID、决策和 Idempotency-Key 交给 ResolveApproval；不信任请求体自行指定用户。
72-75|错误统一映射。
76-80|changed=true 用 202 表示已重新排队；重复相同决策用 200 和 deduplicated=true。
81|Handler 结束，不直接同步运行 Python。`, '', '继续读 postgresstore/agent.go 的 ResolveAgentRun（L434）：锁 Run → 检查已解析 resolutionKey → 锁 pending interrupt → 保存 approved → Run 变 queued、revision+1 → 写事件及恢复 Outbox → Commit。审批结果和重新派发在同一事务中。'),
      S('internal/agent/tool_gateway.go', '批准之后真正提交账本写入', `192-196|Commit 仍需当前有效执行权，审批本身不能绕过租约/revision。
197-201|校验动作键和确认 Token 是否存在且长度合规。
202-204|必须是 Approved=true；拒绝分支不允许执行 Commit。
205-208|验签与检查 Token；不信任客户端描述的待写入内容。
209-211|Token 必须绑定当前 Run 和用户。
212|构造业务幂等键 agent:runID:callKey。
213-215|按签名 claims.Kind 分发；ledger 使用已签名 Candidate ID 调用领域 Confirm。
216-219|只有领域确认成功才返回“已写入生活账本”；模型自由文本不能代替这个结果。`),
      S('internal/agent/worker.go', '执行期间同时响应控制信号', `200-203|若所等工具仍未就绪，先持久化挂起并返回，避免无意义启动 Python。
204-205|派生可取消执行 Context，并保证离开函数时取消。
206-210|把结果与错误组合成结构，使用容量为 1 的 channel；控制分支先退出后，执行 goroutine 仍可放下结果，不会卡在发送上。
211-214|独立 goroutine 调用 Python executor，主循环同时处理取消/超时。
215-221|建立控制轮询 ticker，未配置默认 1 秒；退出时停 ticker。
222-224|deadline channel 初始 nil；nil channel 对应的 select 分支不会触发。
225-232|总截止时间换成 Timer；已经到期则设 0，马上触发，并保证释放 Timer。
233-237|select 收到执行结果就交 finishExecution；提交前那里会再次读取状态。
238-246|每次 ticker 读取数据库；Run 已删除则取消执行，其他读取错误返回。
247-253|看到 cancel_requested 就取消 Python，并用独立 Background Context 持久化 cancelled，避免沿用已取消执行 Context。
254-256|数据库已 cancelled/timed_out 时停止本地执行，不重复提交。
257-261|即使还是 running，只要 revision 或 owner 已变化，也说明执行权失效。
262-265|其他状态意味着当前执行不应继续。
266-270|到总 deadline 则停止执行并尝试写 timed_out。
271-274|超时提交冲突时读取/协调最新状态，而不是盲目覆盖。
275-280|进程上游 Context 取消时停止执行；任务后续由持久化恢复机制决定。`),
      S('workers/python/src/ai_companion_worker/agent_runtime.py', '检查点决定从哪里继续', `7797-7803|execute 同时支持首次 initial 和后续 resolution；关键是同一个 run_id。
7804-7808|构造运行配置，读取检查点值和后续节点列表；非法 values 归为空。
7809-7813|没有检查点时不允许 resume，首次必须提供 initial。
7814-7818|校验初始输入后 invoke 图，并返回结果。
7819-7824|已有检查点时验证 Run、用户、模块、模型/Graph 等身份，防止把别的输入恢复到此运行。
7825-7829|有待处理 interrupt 但未收到决策时，返回同一个待确认数据，不重复推进。
7830-7835|决策与 pending interrupt 匹配后，用 Command(resume=...) 恢复图。
7836-7837|没有后续节点说明图已走完，直接返回持久化结果。
7838-7843|若审批已消费、后续节点失败，调用 invoke(None) 从下一节点续跑，不二次消费原审批。`),
    ],
    end: '重试策略在 agent/worker.go 的 finishExecution：区分永久失败、执行重试次数上限、退避和 deadline；只有重试时间仍落在 deadline 前才 Defer 回 queued。前端 stop 使用固定取消键，retryAgentRun 创建新的运行记录，旧 Run 留作证据。自动执行恢复与用户新建重试不是同一种操作。'
  },
  {
    slug: 'context-memory', title: '普通聊天、上下文与记忆', focus: '持久化 Job、消息组、来源快照、记忆纠正',
    intro: '行为例子：用户连续对话并纠正“我现在住上海”。本章把普通 Job 路径补齐，再解释普通聊天与 Agent 如何共享历史、摘要、记忆、内置知识。',
    sections: [
      S('internal/conversation/service.go', '普通聊天先接受、后执行', `320-323|先调用统一内容校验；失败不产生 Message/Job。
324|去掉首尾空白。
325-328|生成用户消息 ID。
329-332|生成独立 Job ID，后续取消/重试操作指向 Job。
333|固定 UTC 时间。
334|用户输入已经完整到达，所以消息为 completed。
335|Job 初始 accepted，记录原用户消息、attempt=1 和截止时间。
336-339|Store.AcceptMessage 原子保存消息及 Job，返回已分配序列的信息。
340|追加 accepted 事件；这里两个返回值都被忽略，因此不能说本行事件与上一步始终是同一事务，也不能说失败会撤销已接受消息。
341-345|L3 AcceptOnly 时记录降级事件并返回，任务已保存但本调用不启动生成。
346-348|只有 asyncDispatch=false 才在当前进程 start；否则由 Worker 领取。start 仍是后台执行，不是 HTTP 同步等完整回答。
349-350|返回已接受 Message/Job，HTTP 层把它们封装为 202。`, '', '执行续读：runClaimed 加载角色 → ContextBuilder → 选择工具/执行 → 生成回复 → 安全检查 → 拆气泡 → 记录用量与终态。SSE 返回的是持久化事件/气泡，不应自动理解成模型原始 token 逐字透传。'),
      S('internal/conversation/context.go', '历史按完整消息组选择', `157-160|空历史直接返回三个空结果：recent、token 数、待摘要 dropped。
161|groupMessages 把相同 sequence 的气泡放在一组。
162-163|初始化保留起点和估算用量。
164-165|从最新组向最旧组倒着计算成本。
166-168|至少留下一组后才执行预算截断；因此最新组会完整保留，即使其自身超过选择预算。
169-171|累加已保留成本并移动起点。
172-178|若强制推进摘要且所有组都还保留，至少移出前半组，重新统计后半组 Token；这是摘要进度保护。
179-181|展开两段组：recent 进入当前历史，dropped 供滚动摘要处理。
182|函数结束。`, '', '这个预算是历史选择预算，不是最终模型窗口保证。当前请求、工具定义、输出预留等还要由模型调用处做完整请求检查。'),
      S('internal/conversation/context_snapshot.go', '两个执行路径共用 BuildContext', `18-21|先验证当前用户有权读取会话；不能仅凭 conversationID 构建上下文。
22-25|ContextBuilder 负责近期历史和滚动摘要，错误传递给发送入口。
26-27|按当前可靠性策略决定是否召回记忆，然后输出统一 Snapshot。
28-33|初始化版本、空历史/记忆和诊断 manifest，避免 null/缺失数组形成多种协议形态。
34-37|只允许用户和助手历史进入快照。
38|保留消息 ID、角色、正文、sequence、bubble，Python 后续可按完整语义组选择。
39-40|给每条历史追加来源记录，记录会话和序号边界。
41-45|把摘要正文、Token 数、版本、覆盖序号和更新时间一起带入快照，保留来源边界。
46-48|更新摘要用量与 manifest 来源。
49-51|内置产品知识是本地检索，即使外部 RAG 降级仍可用；它不包含个人上传资料。
52-54|把知识来源也写入 manifest。
55-58|不允许 recall 时留下 memory_disabled_by_policy 原因并返回，不会连带关闭本地知识。
59-62|优先使用带来源的 RecallContext，最多召回 8 条候选。
63-69|兼容旧 MemoryContext：仅有字符串时包装成 memory 类型来源；信息量不及新协议。
70-73|记忆调用失败写入 degraded 原因后返回其余上下文，聊天不一定因此失败。
74|BoundMemories 按预算选择完整事实，返回选中项、估算用量和省略数。
75-79|记录最终选中的来源并返回 Snapshot。`),
      S('internal/memory/service.go', '召回记忆：相关性、时效和重要度', `157-161|只读取当前用户的最多 500 条记忆；存储失败返回错误。
162|可选 Embedding 路径生成语义分数；未配置或失败时 semanticScores 返回空分数，词法仍可运行。
163|当前问题分词。
164-169|定义候选+分数结构，初始化结果与当前 UTC 时间。
170-173|过滤非 active、未来才生效、已经结束有效期的事实。
174-180|统计记忆词与查询词重叠数。
181-183|既无词法命中、语义分数低于 .5 又未置顶的候选被丢弃。
184-188|词法归一化为比例，再取它与 Embedding 分数的较大值；变量名 semantic 在这里表示融合前的相关性值。
189-193|更新时间换算天数，未来时间钳制为 0，再计算指数衰减。函数叫 halfLife，但此公式是 exp(-age/参数)，不要直接当作严格数学半衰期公式。
194|综合分数为 .55×相关性 + .25×时效 + .20×重要度。
195-197|置顶额外加 .35。
198-201|有相关性或被置顶才加入候选结果。
202|稳定降序排序，相同分数保留原次序。
203-205|limit 无效时用 8，上限 20。
206-212|输出完整事实、Token 估算、原消息和会话来源。
213-218|达到条数限制停止；返回的是带证据归属的 contextengine.Item。`),
      S('internal/memory/semantic.go', '用户纠正事实时保留替代链', `69-74|清理新内容并按用户查旧 Memory，无法访问时拒绝。
75-77|新事实为 2–2000 字符。
78-81|生成新的 Memory ID。
82-84|记录纠正时间及这次纠正的会话/消息来源。
85|先保存原 ID 到 SupersedesID，形成“新事实替代旧事实”的指针。
86-90|改为新 ID、正文、规范化哈希、分类和敏感性；变量 old 此时已是新记录的构造模板。
91-94|新事实现在开始生效，没有结束时间，设置新建/更新时间。
95-97|UpsertMemory 负责原子替代处理；不要把本函数当成简单 UPDATE 旧正文。`),
      S('internal/persistence/postgresstore/memory.go', '纠正记忆的事务：关闭旧事实与插入新事实', `14|返回值依次是 Memory、是否新建、错误；不能把第二个 bool 当作整个操作是否成功。
15-18|开启事务，失败返回原 item、false 与错误。
19|任何提前返回都兜底回滚；后面成功 Commit 后不会被它撤销。
20|SupersedesID 非空，说明本次新事实要替代某条旧事实。
21|把属于同一用户且仍 active 的旧事实改为 superseded；valid_to 等于新事实生效时刻。参数 $1 在两个列位置复用。
22-24|SQL 执行失败直接结束，尚未提交的修改会回滚。
25-27|必须恰好改变一行；零行意味着旧事实不存在、无权访问或已被其他请求替代，不能继续插入。
28|结束可选的替代步骤。
29-34|按用户和规范化内容哈希找仍 active 的同内容事实，并锁住查到的行；LIMIT 1 只取一条。查不到行不会锁住一个虚构的不存在行。
35|err 为 nil 表示查到了已有事实。
36-38|若又要求替代又撞到已有活动事实，返回 Validation；defer 会撤销刚才关闭旧事实的 UPDATE。
39-40|普通重复插入则复用 existing，false 表示未新建；这里没有待提交的新写入。
41-43|只有“查不到行”是允许继续 INSERT 的情况，网络/数据库故障必须返回，不能伪装成不存在。
44-48|构造 INSERT 的目标列；source 字段保留事实来源，valid 字段保存时间区间。
49-52|列顺序与 $1 到 $17 对齐；空可选 ID 经 NULLIF 变为 NULL，再转 uuid。
53-57|逐个绑定 item 的值；用户内容作为参数传递，不作为 SQL 语法执行。
58|结束执行调用。
59-61|新记录插入失败时返回错误，也回滚之前旧事实的状态修改。
62|先调用 tx.Commit() 再形成返回值；调用者必须检查第三个返回值。true 只是走到了新建分支，Commit 仍可能报错。
63|事务函数结束。`, '纠正链不等于所有编辑：Correct 走“新记录替代旧记录”，而普通 UpdateMemory 是同一记录更新。要按调用入口区分。'),
      S('internal/contextengine/context.go', '记忆预算怎样做到不截断事实', `72|输入已经按优先级排列的事实与预算；返回选中事实、累计估算成本、未入选数量。
73|make 创建长度为 0、容量最多可容纳所有输入的切片；容量不是已有元素数。
74|累计成本从 0 开始。
75|map 的键为内容，值为是否看过；不存在键读取 bool 零值 false。
76|range 每次取一个 item 值；_ 忽略索引。
77-79|空文本或重复内容直接 continue，只跳过当前轮，不退出循环。
80|即使后面因预算落选，也先标记为已看过；相同内容不会再次尝试。
81|把整个 Item 序列化，包含来源等字段；此具体结构没有不可序列化字段，代码丢弃 error。
82|入选成本按整个 JSON 估算，再加 4；不是只计算 Content 的字数。
83-85|放不下就跳过；这里是 continue 而非 break，后面较短事实仍可能入选。
86|写入内容自身的 EstimatedTokens，注意它与包含来源的入选 cost 不是同一个数。
87|append 返回更新后的切片，必须接回 selected。
88|只有入选的事实才占累计预算。
89-91|循环结束并返回；未入选数包括空、重复和预算不足的项，不能全解释为预算超限。
92-93|接下来把 Snapshot 中的参考资料转成一段字符串。
94-96|没有摘要、记忆、知识时返回空，不增加无意义参考消息。
97-101|构造匿名结构体再序列化，只包含参考资料字段；omitempty 使空的可选字段不出现在 JSON 中。
102-103|拼接明确的资料前缀；这不是将历史事实升级为用户操作授权。
104-105|下一个函数是预算估计器，不是模型提供商的真实 tokenizer。
106|分别统计中日韩文字数量 count 和其他 Unicode 码点数量 other。
107|Go 遍历 string 时逐个解码 rune；不是逐个 UTF-8 字节。
108-109|命中列出的 Unicode 字符分类就按 1 计数。
110-112|其余字符累计到 other。
113|遍历结束。
114-115|非负整数 (other+3)/4 相当于向上取整 other/4；与 count 相加作为选择阶段估计。`),
      S('internal/contextengine/context.go', '真正发请求前另做窗口上限检查', `120|输入是模型请求对象，返回保守上界和错误。
121|创建新 map，避免直接改写原 payload。
122|只挑 messages、tools、tool_choice、response_format 四个文本相关字段。
123-125|map 的 value, ok 写法同时得到值和是否存在；没有该字段就不复制。
126|结束字段选择。
127-130|序列化失败就退出，不能在无法计算请求大小时默认放行。
131-132|准备通用 JSON 视图；RawMessage 保存尚未继续解码的 JSON 字节。
133-135|再解码能统一处理原来不同 Go 类型的切片。
136|以序列化后的 UTF-8 字节数加 64 作为基础；这不是提供商账单 Token 数。
137|遍历两类结构开销：每条 message 加 16，每个 tool 加 8。
138|准备接收这类数组的各个 JSON 项。
139-141|只有字段存在且能解码为数组才追加项数开销。
142-144|循环完成，返回上界。
145-146|CheckRequest 在上界之外还接收窗口大小与预留输出量。
147-149|窗口未指定或非正时使用默认值 131072。
150-153|调用上界计算，错误立即向上传递。
154|输出预留必须为正，且输入上界+输出预留+1024 安全余量不能超过窗口。
155-156|不满足时包装 BudgetExceeded，并把四个数字放进错误，方便定位是哪部分超了。
157-158|检查通过返回输入上界和 nil；这只说明本地门禁通过，不保证模型服务必定接受或业务必定成功。`),
      S('workers/python/src/ai_companion_worker/conversation_context.py', 'Python 按节点角色投影完整消息组', `42|类型注解说明 context 是键值映射，返回消息列表与遗漏数；注解不自动验证运行时输入。
43|snapshot 检查契约版本，旧输入缺少快照时可返回 None。
44|条件表达式优先取快照 history，否则用空列表。
45-46|运行时发现 history 不是列表就返回空结果；这是实际校验，不是类型注解。
47|初始化二维列表 groups，每个内部列表是一组相同 sequence 的消息。
48|按原历史顺序逐项读取。
49-50|非映射项跳过，避免下一行调用 .get 时崩溃。
51|只有已有组、当前 sequence 不是 None、且等于上一组 sequence 才合并；and 短路保护 groups[-1]。
52|把这一气泡追加到最后一组；多个气泡不必是完整的一问一答，分组标准是 sequence。
53-54|否则创建新组，方括号 [item] 表示只含一项的列表。
55|按 router/planner/responder 等角色取预算，未知角色默认 6000。
56-57|初始化选中列表和累计成本。
58|从最新组向旧组反向遍历。
59|生成表达式逐个估算组内 content，并每条加 4，再 sum 得到整组成本。
60|注释强调保留最新完整组，不从中间截掉条件或否定。
61-62|selected 已非空且再加入会超预算才 break；因此第一组即使独自超限仍会保留。
63|在列表开头的空切片插入整个 group，保持最终输出仍按时间正序。
64|累计当前组的估算成本。
65|返回选中项与总数差；被跳过的无效项也进入遗漏数量。`, 'Go 与 Python 的选择估算不是所有 Unicode 字符上完全相同；最终模型窗口检查与局部历史选择也不是同一层约束。'),
      S('internal/productknowledge/context.go', '产品问题读取随应用发布的知识', `53-57|RetrievalQuery 判断是否为产品问题或依赖历史的短追问，不相关就不注入资料。
58-62|限制检索查询最多 1000 个 rune，控制本地检索 CPU；并没有裁掉发给模型的当前完整用户指令。
63|取最多 8 个知识候选。
64-70|包装资料正文与可打开的 /#knowledge 链接，来源类型为 builtin_knowledge 并带版本。
71-74|按 ContextBudget 选择完整资料项，返回 Token 和省略数；这是与用户文档额度独立的本地帮助。`),
    ],
    end: '继续读 internal/contextengine/context.go 的快照与 ReferenceInstruction；Python 节点投影在 workers/python/src/ai_companion_worker/conversation_context.py。摘要生成在 conversation/context.go 的 rollSummary 及相关 summary 文件；来源校验和抽取回退避免把无出处摘要当事实。对应测试：context_snapshot_test.go、memory 的语义/纠正测试、Python test_conversation_context.py。'
  },
  {
    slug: 'documents-wiki', title: '上传、解析、检索与 Wiki', focus: 'Blob、Chunk、引用、完整性、依赖权限',
    intro: '行为例子：上传 PDF，询问其中某页，再将整份文件生成制品。问答可检索部分相关片段；整份制品处理要继续沿分轮接口获取全部来源。Wiki 是这些片段的可追溯派生知识层。',
    sections: [
      S('internal/document/service.go', '上传：Blob 与元数据分开保存', `90-94|清理文件名，非法名称立即拒绝。
95-97|上传必须非空且不超过配置字节上限。
98-102|处理 PDF 前缀并检测真实媒体类型，失败拒绝，而不是只信文件扩展名。
103-105|拒绝 EICAR 测试标记；这段检查本身不是完整杀毒引擎。
106-107|对规范化后的内容计算 SHA-256。
108-119|依次生成文件、文档、摄取任务的 ID；三个实体用途不同，任一步失败不继续。
120-125|构造 queued 文档，StorageKey 由用户和内容哈希生成，记录大小与媒体类型。
126-129|先将字节保存到 BlobStore；失败不能创建可解析的文档元数据。
130-135|再创建数据库文档/任务元数据；失败时内容寻址 Blob 有意保留供重试复用，文件系统和 SQL 不是一个原子事务。
136-137|返回持久化记录和 created 标志；created=false 可能是去重复用，不是上传失败。`),
      S('internal/document/ingest.go', 'Worker 把文件变成可搜索片段', `120-125|局部 fail 函数把错误分类并尝试写任务失败状态；true 表示已处理/领取过任务，不代表成功。
126-129|读取 Blob，错误前缀 read_blob 帮助定位故障阶段。
130-133|调用 Python Parser，解析失败记录 parse_document。
134|为页和 Chunk 补充确定性 ID、解析器版本等字段，重试时保持稳定。
135-137|确认索引集合可用。
138-140|先向索引 Upsert Chunks；失败标记 index_chunks。
141-143|把解析结果写入 SQL；这里只是外部索引和 SQL 的有序更新，不是跨系统事务。
144-147|完成摄取 Job；返回 true,nil 才表示本次成功处理。恢复/重试依赖稳定 ID 和任务状态。`),
      S('workers/python/src/ai_companion_worker/document_parser.py', 'Python 按章节和 Token 切块', `352-355|初始化 Chunk 列表、跨页章节栈和全局 ordinal。
356-359|逐页处理，每页新建缓冲；章节路径可继承前页标题。
360-362|emit 闭包访问外层 buffer/ordinal，先合并当前段落。
363-365|空内容不生成 Chunk，并清空 buffer。
366-369|ordinal 加 1，以页码、章节与正文生成 Chunk。
370-371|保留尾部重叠，作为下一 Chunk 的上下文。
372-374|按空段落分块并检测标题。
375-378|新标题前先提交旧 buffer，然后清空，避免跨章节沿用重叠内容。
379-382|按标题级别裁章节栈并追加当前标题，更新 section_path。
383-384|过大段落先拆成小 part，再试着拼入 buffer。
385-387|超出 Token 上限时提交已有块，再与重叠尾部重新计算候选。
388-390|重叠加新段仍超限则丢弃重叠；随后放入新段。此处保留正文，新块不为重叠无限超预算。
391-393|每页最后剩余 buffer 也提交，最终返回有序 Chunk。`),
      S('internal/document/retrieval.go', '问答检索首先限制用户可见范围', `50-54|清理 query 和 limit，非法查询返回错误。
55-58|只列当前用户文档。
59-62|将请求中的 DocumentIDs 转为集合便于过滤。
63-68|只选择 ready 且属于请求范围的文档；空请求范围表示当前用户可见的 ready 文档。
69-71|没有可用证据时返回 insufficient，而不是调用模型猜测。
72-75|带用户和白名单文档 ID 检索 3×limit 候选，给后续排序留余地。
76-78|再次建立允许文档映射，经 rerankedAnswer 过滤/重排并生成带引用结果。语义适配器与重排是否启用取决于配置。`),
      S('internal/document/context.go', '整份文件以轮次继续读取', `147-154|接口显式接收每轮预算、起始轮次和最多轮数，所以调用方可保存游标恢复。
155-161|按用户取文档且必须 ready。
162-165|要求 Store 能读取解析后的内容。
166-172|读取最多 10000 个 Chunk；空结果返回不可用。这个上限是真实实现边界，不能笼统承诺无限文件。
173-181|缺省每轮 4000 估算 Token、每次 4 轮、从第 1 轮开始。
182-187|按预算分轮，将一基 roundStart 转为零基索引；越界返回错误。
188-194|计算窗口末尾和本次轮数，初始化已选 Chunk、轮次清单、文本及清洗报告。
195-201|逐轮逐 Chunk 清理正文，并累计重复/空白行处理数量。
202-205|保留单页或跨页 PAGE 标记，后续引用不依赖模型猜页码。
206-210|保留章节信息，与页码和清洗文本组装片段。
211-215|计算本轮/本次 Token，记录实际选中的 Chunk。
216-223|给每轮正文加标记，manifest 记录原 ordinal 首尾、Chunk 数与 Token 数。
224|本次覆盖率=本次选中 Chunk 数/此次读到的总 Chunk 数；它不是整个 Agent 多次调用的累计覆盖率。
225-228|读取与当前选中 Chunk 对应的 Source IR，供结构化制品生成。
229-233|返回文件身份、解析器版本、文本和是否尚未覆盖全部。
234-238|返回总轮数、本次完成数、NextRound、HasMore、覆盖和清洗证据；Agent 依靠这些决定继续读取。
239|函数结束。`, '', 'Python 累计覆盖需要对 Chunk 区间求并集，重复读取同一轮不能增加完成率。Source IR/并行 Composer 的下一跳在 agent_runtime.py 的 _presentation_document_batches、_merge_presentation_arguments、_document_source_coverage。'),
      S('internal/document/wiki_compiler.go', '解析结果异步编译为 Wiki', `31-35|底层没有 WikiStore 时表示此运行环境不支持编译，返回未处理。
36-42|领取一个 Wiki Job；没有任务不是错误，其他存储错误返回。
43-45|租约为 10 分钟，本次编译 Context 限制 8 分钟，给收尾留时间。
46-50|执行编译，失败累加指标并持久化 compile_failed；errors.Join 保留算法和收尾两个错误。
51-56|成功页面交 FinishWiki 提交；只有提交成功才计 compiles。true 表示领取过任务。
57|函数结束。`, '', 'compileWiki 会验证 ready 状态及 Chunk 数等于 Document.ChunkCount；生成每个片段的证据页、来源页，再可选调用模型组织主题页。自动编译、人工修改保护和租约校验最终由 Wiki Store 提交边界约束。'),
      S('internal/document/wiki.go', 'Wiki 依赖全部可见才可返回', `122-126|获得当前个人或 Workspace 范围内文档；Workspace 成员身份由 HTTP/Agent 边界预先验证。
127-130|无 WikiStore 返回空页列表。
131-133|建立作者集合与允许文档 ID 映射。
134-142|只纳入 ready 文档；收集这些文档的作者，用于读取候选 Wiki 页。
143-145|个人范围即使没有 ready 文档，也可列该用户候选页再做下面过滤。
146-149|从 Store 读取候选 Wiki，存储失败返回。
150-154|每页先要求至少有一个来源，重置 stale 并逐一审查依赖。
155-159|任何来源不在 allowed 中就让整页不可见：多来源页面采用权限交集。
160-163|来源版本不一致则标记 stale，避免旧知识作为当前事实继续使用。
164-168|仅将可见页加入列表；列表可显示 stale 状态，但直接阅读下一函数会拒绝过期页。
169-174|ReadWiki 先增加读取计数，再调用同一 ListWiki 权限路径。
175-181|找到目标页后，stale 返回 ErrWikiConflict，否则返回正文。
182-185|不可见或不存在统一返回 NotFound。`),
      S('internal/document/wiki.go', '人工编辑 Wiki 使用版本条件', `351-355|编辑仅从当前用户个人可见 Wiki 集合查找。
356-360|同时要求 pageID 匹配、OwnerID 为当前用户、客户端 version 等于当前页版本。
361-363|标题 1–160 字符，正文最多 24000 字节；两种长度单位不同。
364-367|写新标题/正文、设置 Edited=true、更新时间，标记人工编辑保护。
368|SaveWikiPage 再以 version 做数据库条件写入；读取后到写入前仍可能并发变化。
369-372|未找到可编辑页统一返回 NotFound。`),
    ],
    end: '问答入口是 Query，完整制品入口是 ReadParsedContextRoundWindow，两者不要互换。Wiki 搜索与链接扩展继续读 wiki.go 的 SearchWiki、FollowWikiLinks；原文件删除还要读 document/cleanup.go。验证：document/wiki_test.go、document/context_test.go、Python 文档解析与 Agent 覆盖率测试。'
  },
  {
    slug: 'life-tools', title: '生活助手：记账、计划、提醒和导出', focus: '候选与确认、金额、时区、幂等键',
    intro: '行为例子：“午饭花了 35 元”“明天下午 3 点提醒我开会”“加入今天计划”“导出本月账单”。聊天模型先识别工具，实际业务规则由 ledger/planner 服务执行；页面直接调用这些 API 时复用同一服务。',
    sections: [
      S('internal/ledger/parser.go', '自然语言解析成待确认账单', `27-31|清理文本并限制 2–2000 个字符，防止空输入和超大正则工作量。
32-35|加载用户时区，无法识别时拒绝。
36|初始化 Candidate：原文、时区、方向、类别、商户、pending、初始置信度；尚未写正式 Entry。
37|从原文提取金额和币种文本。
38-42|有金额则转浮点并检查正数及最大值。
43|乘 100 后四舍五入为整数最小金额单位；账本金额存整数，但解析入口仍用了浮点，不能称全链路定点计算。
44-45|规范化币种并提高置信度。
46-48|缺金额时把 amount 加入待澄清字段。
49-53|识别收入/支出则提高置信度，否则要求补 direction。
54-56|非 other 类别增加确定度。
57|以用户本地时区解释时间表达。
58-62|时间解析成功后转换 UTC，并保存时间精度。
63-65|无法确定发生时间则要求补 occurred_at。
66-68|置信度最多 1。
69-73|任何必需字段仍缺失则 needs_clarification，返回 Candidate，不创建正式账目。`),
      S('internal/ledger/service.go', '用户确认后才写正式 Entry', `318-322|确认请求必须提供非空且不超长的幂等键。
323-326|按当前用户读取 Candidate。
327-329|仍需澄清或没有发生时间时禁止写账。
330-333|生成正式账目 ID。
334|固定确认时间。
335-336|Entry 绑定用户和 Candidate，复制确认过的收支方向。
337|复制币种、整数金额和类别。
338|复制商户及 UTC 发生时间，同时保留原时区。
339-340|清理备注，标记 active 并保存幂等键。
341-342|Store.ConfirmCandidate 负责候选→正式 Entry 的原子确认和重复请求处理。返回 bool 表示本次是否新建。`),
      S('internal/planner/service.go', '聊天创建提醒并做来源去重', `332-334|若有 sourceMessageID，先按用户与原消息查已有提醒。
335-337|找到就复用，重放同一输入不会再次创建候选。
338-341|只有 NotFound 允许新建，数据库故障不可当作无记录。
342-345|解析文本、补充标题/日期槽和时区；追问补字段可走同一解析器。
346-349|生成提醒 ID。
350-353|绑定用户、原消息和创建/更新时间。
354-358|保存 Reminder 候选并返回；此时仍需下一节确认。
359-364|ConfirmReminder 先校验幂等键。
365-368|按用户读取候选。
369-371|DueAt 缺失、待澄清字段非空或 needs_clarification 状态都不能确认。
372-373|Store 层原子确认，返回记录和是否新确认。`, '', '解析器在 planner/parser.go：上午/下午歧义、过去时间、夏令时缺口都在自然语言解释阶段处理。通知 scheduler 扫到期提醒后生成应用通知记录；系统日历/提醒权限属于平台适配边界，不等于 Web 已经静默写入手机系统。'),
      S('internal/planner/service.go', '加入今日计划：首次建 Plan，否则加 Item', `176-181|清理标题/来源并要求标题非空、不超过 255 字符。
182-184|来源字符串最多 64 字符。
185-188|验证时区。
189-191|按本地时区解析 YYYY-MM-DD。
192-195|只有 chat: 开头的稳定消息来源作为去重键；通用 UI 来源只是标签，不能用于拦截后续所有人工添加。
196-203|同用户、同日期、同聊天来源已有 Item 就复用；其他查询错误终止。
204-207|查询当天 Plan。
208-212|没有当天计划时，创建默认“今日计划”并一次包含新 Item。
213-217|创建失败返回错误，成功直接返回刚创建的首项。
218-222|已有 Plan 时构造新 PlanItem，默认中优先级。
223-227|Store 按用户及 Plan ID 添加并返回 Item。`),
      S('internal/ledger/service.go', 'Excel 导出是持久化任务', `159-163|用户的导出 requestKey 必填且长度有限，重复点击可复用同一任务。
164-166|验证月份与时区；真正取账单时按同一月边界解释。
167-170|规范化币种，空则默认 CNY。
171-173|目前此接口只支持 CNY/USD。
174-177|生成 Export ID。
178-181|构造 queued ExportJob，交 Store.CreateExport 保存并按 requestKey 去重；现在还没有 Excel 字节。`, '', 'Worker 的 RunExportReconciler → executeExport → ArtifactToolExporter 调用 workers/spreadsheet/ledger_export.mjs。任务成功后 DownloadExport 检查归属、状态和文件；前端读取 ai-ledger-export 标记显示下载。'),
    ],
    end: '修改计划时间与提醒改期继续读 SchedulePlanItem、RescheduleReminder：expectedUpdatedAt 防止旧页面覆盖；完成、列表、月汇总主要是同一领域 Store 的短包装。测试应关注重复确认只生成一条、跨时区月边界、上午下午追问、过期改期与并发修改冲突。'
  },
  {
    slug: 'skill-artifacts', title: 'Skill 执行与文件交付', focus: 'Schema、任务去重、预算、续租、结果文件',
    intro: '行为例子：工作助手生成 PPT。Agent 负责选择能力、准备内容和检查结果；Skill Run 是真正执行生成工具并保存文件的任务。阅读时分别跟踪 Agent Run ID 和 Skill Run ID。',
    sections: [
      S('internal/skill/service.go', '接受一次版本化 Skill 任务', `412-417|内部 start 同时服务独立工具调用和来自会话的调用，后者额外带 conversationID/originMessageID。
418-420|创建幂等键必填，最多 191 字节。
421-424|读取该用户可使用的 Skill 定义；用户禁用能力时不能继续。
425-427|按 Manifest.InputSchema 校验输入，模型生成的参数也必须过关。
428-431|先按用户和 createKey 查已有任务；同键若指向不同 Skill 则冲突。
432-438|会话关联不一致也冲突；同动作重放返回已有 Run、created=false，非 NotFound 查询错误原样返回。
439-442|把合法参数编码为 JSON。
443-445|输入字节不得超过 Manifest.MaxInputBytes，Schema 合法仍可能因体积超限被拒绝。
446-449|生成新的 Skill Run ID。
450-456|初始可 running/execute；需确认优先 waiting_confirmation/confirm，否则按执行模式选择 queued。
457-461|Run 快照保存 Skill 版本、模式、风险、输入、会话和原消息、CreateKey。
462-465|将步数、超时、输入大小、成本预算复制到 Run，设置 attempt=1、revision=1 与空步骤/文件列表。
466-472|持久化前先记录 receive/validate/plan 三个完成步骤；这些是平台阶段记录，不意味着每个阶段都调用模型。
473-479|创建时仍可能与并发请求竞争。唯一键冲突后重查，解决“先查都不存在、同时插入”的竞态。
480-483|只有初始 running 的任务立即执行；queued 或 waiting_confirmation 原样返回，created=true 不代表已生成文件。
484|函数结束。`),
      S('internal/skill/service.go', '确认任务并使用 revision 更新', `486-489|确认幂等键必须合法。
490-493|按用户读取 Skill Run。
494-499|已有确认键且同键则复用结果，不同键则冲突。
500-502|仅 waiting_confirmation 且 RequiresConfirmation 才能继续。
503-506|重新核对用户当前是否仍可使用该能力。
507-509|为确认、执行和交付留足步骤预算。
510|保存旧 revision 作为条件写入依据。
511-512|记录确认键，准备 running/execute。
513-515|长任务改为 queued，并设置可领取时间。
516-518|追加 confirm 步骤，内存对象 revision 加 1。
519-527|SaveSkillRun 以 expected 做 CAS。若并发相同确认先成功，重读相同 ConfirmationKey 后返回已有结果。
528-531|queued 立即返回，否则同步走本服务 execute；HTTP 上层仍需按实际返回状态判断完成。`),
      S('internal/skill/runner.go', 'Worker 执行时定时续租', `48-51|建立执行 Context、正常完成信号和容量为 1 的续租错误 channel。
52-55|独立 goroutine 定期续租，退出停 ticker。
56-60|任务完成或执行 Context 结束则续租循环退出。
61-66|续租失败先记录错误，再取消工具执行；旧 Worker 不应继续持有失效执行权。
67-69|结束循环分支和 goroutine。
70|主协程执行已领取任务；不重新创建 Run。
71-72|执行返回后关闭完成信号并取消 Context，清理续租工作。
73-77|优先返回续租错误；default 使没有错误时不阻塞等待。
78-81|外层停止时平滑返回已处理；正常情况返回工具执行错误。
82|函数结束。`),
      S('internal/skill/service.go', '执行算法与解释错误', `861-865|清旧 Output，并为 execute/deliver 两步检查剩余步数；超限持久化失败。
866-869|从持久化 JSON 恢复输入；坏输入不能进入算法。
870-873|按 Run.TimeoutMS 设置子 Context 截止时间，并记录执行起点。
874|实际工具入口是 definition.Handler.Execute；内置处理器或 PythonOfficeWorker 都实现这个接口。
875-885|算法出错且上层已取消时：内联任务可记录取消/超时，Worker 任务交执行/租约控制层处理，不用失效 Context 强写状态。
886-894|区分普通工具失败、超时与工具提供的结构化失败码，统一交 s.fail 持久化。
895|保留模型审计输出，即使后续质量/预算失败也不能丢失已发生的调用证据。
896-904|有限成本任务必须返回可识别用量，缺用量或成本超限都失败。这个步骤是结果校验，不能把已发生费用撤销。
905-907|工具 Output 内部显式报告模型操作失败时，不因 Go error=nil 而当成功。
908-910|输出同样要过 Manifest.OutputSchema。
911-916|编码输出、确定完成时间，准备新文件列表。`),
      S('internal/skill/service.go', '文件保存后提交完成状态', `917-920|逐个文件校验名称长度与媒体类型。
921-930|逐文件检查 MaxOutputFileBytes；此字段并非所有文件总大小。超限记录结构化 resource_limit，副作用未知。
931-934|每个生成文件拥有独立 ID。
935-938|按用户、Run、File ID 保存文件字节，写入失败结束本次任务。
939-941|计算文件 SHA-256，记录名称、类型、大小、StorageKey 与时间。
942|保存旧 revision，用于最终 CAS。
943-945|转 succeeded/deliver，清租约和错误，写输出与完成时间。
946-949|构造 execute 与 deliver 的步骤记录，执行步保留真实起止时间。
950-952|将步骤和文件附到 Run，并生成重试交付内容。
953|新 revision 加 1。
954-958|Store 一次保存终态、步骤与文件元数据；失败则尽力删除本批新文件，避免 CAS 输掉仍暴露重复交付。
959-961|返回成功 Run。文件系统与 SQL 仍是分阶段操作，不能声称具备分布式原子事务。`, '', '下载经过 /v1/skill-runs/{run_id}/files/{file_id}，由服务按用户/Run 检查访问。消息正文中的 ai-generated-file 标记只是 UI 关联，不是绕过权限的直链。'),
    ],
    end: '继续跳转：internal/skill/builtins.go 注册能力与 Manifest；office_worker.go 定义 Go/Python 进程协议；workers/python/src/ai_companion_worker/office_tools.py 执行文档算法。重试与自动修复分别在 Retry、RepairRetry；工具终态再通过 Agent waiting_tool 恢复整条任务。文件格式实现细节和渲染质量门禁可参照 docs/blog/ppt-generation-vibe-coding 系列。'
  },
  {
    slug: 'teams-admin-quotas', title: '团队分享、管理员登录与额度', focus: '成员范围、邮件队列、Passkey、分层政策',
    intro: '这些行为复用共同数据库，但身份不同：普通用户 Token 操作个人/团队资源；管理员 Passkey Session 操作管理接口。配额在高成本行为接受之前检查，额度策略与已发生用量分别存储。',
    sections: [
      S('internal/team/service.go', '创建空间时同时成为 Owner', `76-80|清理并限制空间名称为 2–80 个字符。
81-84|生成 Workspace ID。
85-87|构造 active Workspace 和 owner Member，成员显示资料来自当前已认证用户。
88-92|Store 同时保存空间与首位成员，再返回；不会通过邮件邀请自己成为 Owner。`),
      S('internal/team/service.go', '邀请是受权限约束的待接受记录', `109-113|规范化并验证受邀邮箱。
114-117|只允许授予 admin/member，不通过邀请接口转移 owner。
118-121|邀请人必须仍为活跃成员。
122-124|普通 member 无邀请权限，owner/admin 才可继续。
125-128|生成 Invitation ID。
129-130|创建 pending 邀请，保存邀请人和 14 天到期时间。
131-135|邀请先入库。HTTP Handler 随后排邮件；邮件投递失败不会让这条邀请不存在。`),
      S('internal/team/service.go', '接受邀请要绑定真实账号邮箱', `148-152|按 Invitation ID 读取邀请。
153-156|只接受 pending 且尚未到期的邀请。
157-159|受邀邮箱必须与当前登录用户匹配；知道邀请 ID 不等于有权加入。
160-161|Store 在事务中再次处理接受、成员写入及状态变更。`),
      S('internal/email/service.go', '邮件 Worker 处理投递结果', `173-174|将持久化 Delivery 转为 Message，调用配置好的 SMTP 或 Noop Sender。
175-177|发送失败写 FailDelivery，并返回 true,nil，表示本任务已处理，队列可以继续服务其他任务；不是声称邮件已成功送达。
178-180|发送成功后把 providerID 与终态交给 Store，提交错误会返回。`, '', 'SMTP 调用与数据库提交不是同一事务：若外部已投递、CompleteDelivery 写失败，恢复可能重复投递。不能仅凭 Outbox/租约声称邮件端到端 exactly-once。团队资源分享还需读 httpserver/workspace_documents.go、workspace_skill_files.go、workspace_ledger_exports.go：验证成员后建立显式分享关系，聊天/个人记忆不会自动共享。'),
      S('internal/adminpasskey/service.go', '挑战只能原子消费一次', `212-213|takeCeremony 按 kind+secret 派生键，Store.Take 读取并消费挑战。
214-216|取不到或过期拒绝；已消费的挑战不能重复登录。
217-220|还原序列化的 ceremony，结构无效拒绝。
221-222|返回记录与 WebAuthn 会话数据供签名验证。`),
      S('internal/adminpasskey/service.go', 'Passkey 开始与完成登录', `251-255|创建 discoverable login，并要求设备侧用户验证；生成 options 和服务端 challenge/session 数据。
256-258|把 ceremony 短期持久化并返回浏览器所需参数及关联 secret。
259-264|FinishLogin 首先消费对应挑战，失败不继续验签。
265-269|准备存储凭据与账号，handler 供 WebAuthn 库按 Credential ID 定位真实用户。
270-273|查凭据记录，未知凭据拒绝。
274-276|解析凭据并核对 RP ID，防止把另一站点的凭据拿来登录。
277-280|重新加载凭据拥有者与其有效账号信息。
281-285|验证 User Handle；如果本次明确要求某个账号，还要核对 ExpectedOwner。
286|由 WebAuthn 库校验 challenge、来源、RP 与签名等协议要求。
287-289|还要求 UserVerified 且没有 CloneWarning。
290-294|保存最新认证器凭据状态；保存失败不发 Session。
295-296|最后签发项目自己的管理 Session，Passkey 私钥不进入服务端。`),
      S('internal/adminpasskey/service.go', '管理员 Session 逐次校验撤销与闲置', `317-321|通过 Token 派生键查管理 Session，失效或不存在拒绝。
322-325|反序列化 Session。
326-329|同时检查绝对到期和 IdleTTL 闲置到期。
330-333|重查管理员，要求 active 且账号版本与会话版本一致；改版/撤销可使旧 Session 失效。
334-337|检查创建该 Session 的凭据仍存在且归属于这个管理员。
338-341|再次核对凭据 RP ID。
342-345|LastSeen 至少相隔 1 分钟才更新，减少每个 dashboard 请求都写库。
346-350|并发更新冲突时重读认证，不会把已删除 Session 重新创建回来。
351-355|其他写入错误拒绝；成功返回账号与 Session。
356-358|Fresh 单独判断最近验证时间；管理写操作可要求重新 Passkey 验证，而不只是普通登录有效。`, '', 'HTTP 层 Cookie、安全属性、同源校验及 Fresh 要求继续读 internal/httpserver/admin_passkey.go 的 setAdminCookie、adminOriginOK、authenticateAdminSession。'),
      S('internal/billing/quota.go', '额度的 null、0、-1 是不同意思', `13-20|每个上限用 *int：nil 表示继承下一层，0 禁止新增，-1 不限额；普通 int 无法表达这三种不同语义。`),
      S('internal/billing/quota.go', '管理员设置一层额度', `51-54|替换整层政策，清理 UserID、Actor 和修改原因。
55-57|expected revision、ID、操作者和原因必须合法；上界兼容 JavaScript 安全整数。
58-62|逐项允许 nil、-1 或非负安全整数，拒绝更小的负值。
63-66|要求底层 Store 实现 QuotaStore。
67-70|记录时间与 expected+1，将旧 expected 交给 SQL 条件写入；人工理由/审计在 Store 事务中保存。
71|函数结束。`),
      S('internal/billing/quota.go', '政策逐字段覆盖', `77-78|base 是按值传入的 Limits；targets 保存其各字段地址。
79-83|与 values() 的字段顺序一一对应，只覆盖非 nil 值；0 和 -1 都会真实覆盖。
84-85|返回合成上限，调用者的原套餐对象不因这个值拷贝被改写。`, '', 'billing.Service.Summary 的 L186 是核心：policies.User.Limits.apply(policies.Global.Limits.apply(limits))。先套餐，再全局，再用户；并记录每个最终值来自哪一层。'),
      S('internal/billing/service.go', '业务接收前检查实际用量', `302-306|重新生成当前用户 Summary，失败不默认放行。
307|规范化资源名。
308-311|只找对应资源，不把所有资源共用一个额度。
312-314|上限非负且 Used>=Limit 时返回带资源/套餐/实际值的 QuotaError；-1 跳过此判断。
315-318|资源存在且未超限返回 nil，未知资源返回校验错误。`, '', '这里是已累计用量检查，不是对每次并发模型调用做成本预扣。月度总额度、单 Run 调用/Token/费用预算、账后用量纠正是三个不同层面。'),
    ],
    end: '验证入口：internal/httpserver/teams_test.go、internal/adminpasskey/service_test.go、internal/billing/quota_durable_test.go。当前 Web 管理员路径已有 Passkey；旧 operator API 的 Token/TOTP 兼容能力要按具体路由判断，不能把两种认证方式混为同一实现。'
  },
  {
    slug: 'reliability-controlplane', title: '故障恢复、降级与配置发布', focus: 'Outbox/Inbox、至少一次、状态迟滞、治理发布',
    intro: '行为例子：Worker 重启、Kafka 重复投递、模型异常、管理员发布新 Agent 配置。持久化任务负责可恢复状态；传输层、可靠性策略和配置控制面分别承担不同职责。',
    sections: [
      S('internal/eventbus/relay.go', 'Outbox Relay 怎样投递并重试', `33-34|按 workerID/lease 领取一批待投递 Outbox；它们由业务事务先保存。
35-37|没有事件返回 0,nil，属于正常空闲。
38-40|数据库读取异常返回错误。
41-43|逐条发布，Publisher 可代表 Kafka 或数据库派发适配器。
44-47|发布成功还需 MarkOutboxPublished；外部发送成功但数据库标记失败仍视为本次失败。
48-50|发布及标记都成功才继续下一条。
51-55|保留错误并截断错误消息长度，避免异常内容无限增长。
56-57|超过最大 attempts 标 dead，否则计算下次可投递时间；FailOutboxEvent 的错误被忽略，是尽力记录。
58-60|返回本批领取数与最后一次错误。`, '', '这意味着至少一次投递：外部 Publish 成功而 Mark 失败可能导致重发。消费者及具体业务的幂等是必要条件。'),
      S('internal/eventbus/consumer.go', 'Inbox 去重与业务幂等的关系', `196-198|从消费者 Poll 一条消息。
199-206|若是 PoisonMessage，先持久化坏消息证据，再提交 Offset 避免同一坏消息永远堵住分区。
207-208|普通 Poll 错误返回，交外层决定重试。
209-216|Event ID 必须为合法 UUID；不合法按 Poison 处理。
217-220|按消费者名称+Event ID 查 Inbox；存储不可用时不能假设未处理。
221-227|只有未见过的事件才执行；从事件恢复 Trace，无法恢复就新建。
228-230|Run 类型事件把 AggregateID 附到 Trace Context，串联诊断。
231-233|调用领域 Processor，它通常还会通过租约/幂等键领取真正任务。
234-237|业务成功后才记录 Inbox；记录失败返回，未来消息可能重放，所以 Inbox 本身不提供跨业务写的 exactly-once。
238-239|最后提交消费 Offset；重复已见事件跳过 Processor 但仍可提交。`),
      S('internal/reliability/controller.go', '用连续样本避免降级抖动', `124-126|Controller 有并发读写，进入观察时锁定状态。
127-129|转 UTC，依据 sample 分类目标等级，保存本次采样。
130-132|首次记录当前级别起点。
133-136|目标等级未变时清候选计数并返回当前快照。
137|变差时使用 EnterSamples 连续样本阈值。
138-140|恢复时只下降一个等级，并使用 RecoverSamples，恢复更谨慎。
141-145|当前级别驻留时间不够则不恢复，重置候选计数。
146-150|候选级别变化从 1 计数，相同候选持续累加。
151-154|满足连续样本阈值才更新真正 level/reason/changedAt，并清计数。
155-156|返回反映当前与候选状态的快照。`),
      S('internal/reliability/controller.go', '等级映射为具体执行策略', `183-186|模型错误率达到 .80 或最老任务达到 2 分钟，进入 L3 候选。
187-189|队列>=500、老任务>=1分钟、P95>=10秒或错误率>=.50，进入 L2 候选。
190-192|更低一档阈值进入 L1 候选。
193-195|全部健康则 L0；classify 给的是候选，上一节 Observe 决定是否真正切换。
196-199|L1 关闭完整 RAG 与记忆提取，模型偏好 small。
200-201|L2 模型偏好 low_latency，长工具继续排队。
202-203|L3 偏好 local_only 并 AcceptOnly；是否生效还要看具体调用方消费这些字段。
204-206|默认 L0 允许完整 RAG/提取，主模型偏好 primary。`, '', '不能只看 Policy 字段就认定所有路径全都自动降级。普通 conversation.Send 读取 AcceptOnly，BuildContext 读取 UseFullRAG；Agent 还有自己的模型预算/重试策略，需沿实际调用点确认。'),
      S('internal/controlplane/service.go', '新配置以不可变草稿版本创建', `161-166|统一配置类型/键，要求合法基线版本、操作者和修改原因。
167-170|按配置类型验证 Payload；不同 billing/model/agent/prompt 有不同契约。
171-174|创建 Version ID。
175-180|保存 SchemaVersion、draft 状态、规范化 Payload、内容 Fingerprint 和作者时间信息。
181-183|交 Store 建立不可变版本，后续状态变化不代表随意原地修改 Payload。`),
      S('internal/controlplane/service.go', '验证、提交与发布门禁', `185-189|Validate 先读取指定配置版本。
190-192|只有 draft 可进入校验状态。
193-197|再次验证 Payload，然后以 draft→validated 条件更新。
198-202|Submit 要求操作者身份。
203-206|读取版本。
207-209|只允许 validated 提交。
210-217|Agent Definition 必须存在匹配本版本 ID 和 Fingerprint 的通过评测；旧版本通过报告不能替代。
218-219|条件转 submitted。
220-226|Publish 读取版本并规范化发布者。
227-229|只有 submitted 可发布。
230-232|发布者不能为空，也不能等于创建者或提交者；代码在此实现职责分离。
233-235|发布前重新校验 Payload，防止依赖变化使旧验证失效。
236-243|若为 Agent，再要求当前精确版本的评测通过。
244-246|validateAgentActivation 检查模型/模块等运行依赖。
247-250|读取当前活动 Agent 部署，判断是否替换已有版本。
251-254|跳过不同 key 或相同版本部署。
255-261|替换已有版本时查匹配本版本/指纹的最近灰度结果；缺失或存储失败拒绝。
262-267|灰度必须 Ready 且 Decision=Pass，满足后退出查找。
268-273|已有合格 rollout 时，带 rollout revision 原子提升为正式部署。
274-275|无需晋升既有 rollout 的情形走普通 PublishConfigVersion，例如首次部署。`),
    ],
    end: '运行时同步：cmd/api/main.go 定时调用 SyncRuntimeConfiguration；Agent Worker 观察模型配置，每个实例上报实际 Version/Fingerprint。日志、告警、成本分析分别读 internal/opslog、incident、performance；发布评测和灰度深入 internal/controlplane/agent_evaluation.go、agent_rollout.go。这些是工程/内部发布能力，不自动证明当前外部生产环境已经上线或通过所有质量门禁。'
  },
  {
    slug: 'python-model-graph', title: '深入 Python：模型调用怎样驱动图', focus: '进程协议、工具目录、路由决策、真实 HTTP 调用',
    intro: '这一章接在第 3 章 Worker 领取之后、第 4 章工具审批之前阅读。它回答“Python 怎么拿到 Go Run”“模型选择怎样变成节点跳转”“真正请求模型的代码在哪”。下面展示进程 executor；启用 Python 进程池时复用相同运行协议与检查点，而不是每次重新启动进程。',
    sections: [
      S('internal/agent/python_executor.go', 'Go 将运行记录发送到 Python 进程', `105-108|选择配置的 Python 可执行程序，空则 python3。
109-113|如果设置进程超时，就在上游 Context 上派生更短截止时间；退出释放取消函数。
114|把当前 Trace 传播信息并入运行输入。
115-118|Run 序列化为 JSON，编码失败不启动进程。
119|exec.CommandContext 直接执行 Python 模块，无 shell 字符串拼接；Context 取消可停止该进程。
120|JSON 从 stdin 输入，用户内容不会作为命令行参数执行。
121|使用受控环境变量设置模型/模块路径等运行配置。
122-124|stdout 专用于返回协议，stderr 用于诊断，两者分开。
125-128|子进程失败且 Context 已结束，优先报告 Context 错误。
129-135|其余错误从 stderr 获取，最多保留 4000 字符，空则使用进程错误。
136-139|若存在结构化执行错误，保留其错误码/重试语义；否则包装通用 Python 失败。
140-143|成功退出仍要求 stdout 非空且不超过响应上限，再在后续行解码 ExecutionResult。`),
      S('workers/python/src/ai_companion_worker/agent_worker.py', '输入转换与可信工具目录', `175-184|execute_run 显式接收 checkpointer、决策端口、工具网关和工具定义，允许复用 runtime/telemetry。
185-186|Run ID 必填，且必须与支持的 Graph 身份匹配。
187-196|没有可复用运行时则根据 Run 快照解析预算和递归上限，构建图并绑定检查点。
197-200|校验模块，input 必须为字典。
201-203|兼容 user_message/text 字段，但正文必须非空。
204-206|Context 必须是对象。
207-210|复制 Context 后，用可信且经过 Agent 定义过滤的工具目录覆盖 tools 字段；用户文本不能自行授予工具。
211-219|建立规范 AgentInput：Run/用户/会话/角色身份、模块、当前请求和可信 Context。
220-223|恢复决策可空，但非空必须为对象。
224-225|配置可选 Langfuse 观察上下文；核心运行不以远端观察成功为前提。
226-230|调用前面解释过的 runtime.execute，首跑和恢复共用同一 Run ID。
231-239|读取 interrupt 类型，仅允许纯 tool_approval 或纯 tool_wait，分别映射 waiting_approval/waiting_tool；混合或未知类型为协议错误。`),
      S('workers/python/src/ai_companion_worker/agent_runtime.py', '选择直接回答、单工具还是多步执行', `3326-3331|分类输入为当前状态、已选工具名及可信定义，输出模式和诊断原因。
3332-3334|已有多步计划或已经 agentic，就保留该模式，避免执行中途丢掉进度追踪。
3335-3336|无工具为 direct；direct 仍在这个 Graph 中运行，不代表绕过 Agent。
3337-3338|可信工具定义要求前置规划时选择 agentic。
3339-3340|repeatable 工具需要跟踪重复执行的进度，因此 agentic。
3341-3343|附件提取是中间步骤，不应误当交付终点，强制多步模式。
3344|其余一个可信工具即可完成请求的情形为 single_action，可省 Planner 模型调用。`),
      S('workers/python/src/ai_companion_worker/agent_runtime.py', '模型决策转为受约束的状态更新', `3752-3754|调用模型前检查版本/预算等限制，有阻断原因直接生成终态更新。
3755|记录高精度起始时间。
3756-3760|建立带本节点剩余模型调用/Token/成本 allowance 的决策 Context。
3761-3766|加入当前计划、真实工具观察、动作序号、任务契约、制品验证和文档覆盖情况。
3767-3772|调用 DecisionPort.decide，将模块、当前用户请求和上下文交具体模型适配器。
3773-3775|intent 必须存在，空字符串不是合法决策。
3776-3778|提取工具名/参数，默认不需要 Composer。
3779-3783|是否可做参数组合取自可信工具定义的 compose_arguments。
3784-3788|模型自行要求目录未允许的 Composer 能力时拒绝。
3789-3792|无需 Composer 的工具参数直接校验；没有工具却要求组合也拒绝。
3793-3795|无工具时至少已有回答或明确 needs_response，否则决策为空。
3796-3797|模型预算耗尽进入预算终态，不做无限重试。
3798-3805|其他异常形成 router 角色的结构化模型失败状态。
3806-3811|成功后结算模型调用观察与用量。
3812-3816|使用前一节分类函数决定 direct/single_action/agentic。
3817-3827|组装新的状态增量，清理旧 proposed_tool/preparation/tool_result，步骤数加 1。
3828-3836|如果模型选了工具，还要校验模块权限；拒绝时输出明确失败结果，不进入 Go 写业务。
3837-3842|合法工具写入 proposed_tool，包含参数和 Composer 标记，等待路由函数选择下一节点。
3843-3845|已经给出回答则记 outcome=completed，但下一跳还要过响应质量门禁。
3846-3849|需独立 Responder 则设置 needs_response，返回状态增量。`),
      S('workers/python/src/ai_companion_worker/agent_runtime.py', 'after_decision 是实际分支选择器', `3851-3852|读取模型选择形成的 proposed_tool。
3853-3854|多步任务尚无 Plan，下一节点是 plan。
3855-3856|参数需组合时交 composition_route 决定串行或按文档批次并行。
3857-3858|已有可执行工具且不用组合，进入 preflight_normalize，然后才能 Prepare。
3859-3860|无工具且需文本回答时进入 generate_response。
3861-3862|模型已经直接给出回答时进入 response_quality_gate。
3863|其他情况由 finalize 收尾，不凭空补一次业务写入。`),
      S('workers/python/src/ai_companion_worker/openrouter_decision.py', '真正的模型 HTTP 请求', `2230-2235|底层请求接受已构造的 payload 和本次 timeout；具体模型选择/回退预算由上层方法处理。
2236-2240|紧凑编码 JSON 并转 UTF-8，保留中文，减少无用转义。
2241-2244|请求配置的 base_url/chat/completions，正文为 JSON，方法 POST。
2245-2249|携带服务端 API Key、JSON Content/Accept 以及 Trace 头；此密钥不从前端传入。
2250-2257|可选附应用来源和标题，不影响业务工具权限。
2258-2264|同时设墙钟截止与网络 timeout，最多读响应上限+1 字节，以识别超限。
2265-2271|HTTP 错误保留状态码与 Retry-After，供上层退避/分类，而不是把错误 HTML 当模型回答。
2272-2276|墙钟超时映射成 408 类模型错误。
2277-2280|超时与其他 URL 连接错误分别包装。
2281-2282|超过最大响应字节数拒绝。
2283-2286|解析 JSON；编码或 JSON 错误都报告供应商协议问题。
2287-2289|要求响应顶层是对象，再交上层读取 choices/tool_calls/usage。`),
      S('workers/python/src/ai_companion_worker/agent_runtime.py', '生成文本并进入质量检查', `5745-5749|Responder 节点先做模型访问/预算检查。
5750-5752|决策端口必须实现 respond；缺能力是契约错误。
5753-5758|记录时间并附当前节点调用 allowance。
5759-5761|输入计划、真实观察和任务契约，让回答基于当前执行状态。
5762-5767|调用 responder，模块/当前请求/Context 分开传递。
5768-5769|拒绝空或非字符串回答。
5770-5771|预算耗尽转终态。
5772-5779|其他异常保存 responder 角色失败与时序。
5780-5786|合并观察数据和已发生模型用量。
5787-5791|保存正文、清 needs_response、记 completed 并步数+1；Graph 边随后将其送入 response_quality_gate。`, '', 'response_quality_gate 调用 inspect_and_repair_response，先做确定性检查/修复；已存在失败终态不能被文字质量通过覆盖成成功。未通过时至多发起一次模型改写。artifact_quality_gate 则针对真实生成文件及来源完整性，两种门禁检查的对象不同。'),
    ],
    end: '大文件并行的阅读顺序：fanout_composition 分预算/分批 → compose_document_batch 执行独立 Composer → join_composition 按原顺序合并 → artifact_quality_gate 检查来源/产物 → assess_progress 决定继续还是交付。注册表在 agent_runtime.py L7052，Graph 条件边在 L7085。Go 进程池在 internal/agent/python_pool.go，调度器在 internal/agent/dispatcher.go。相关测试入口：workers/python/tests/test_agent_runtime.py、internal/agent/python_executor_test.go、internal/agent/dispatcher_test.go。'
  },
];
