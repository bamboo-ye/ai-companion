# 01 启动、登录与角色

[阅读总入口](README.md)

行为例子：用户打开页面，注册账号并创建一个生活助手。先读启动装配，确认下面每次调用的 Service/Store 从哪里来，再追用户身份和角色。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 先把“服务”分成三个层次

浏览器不知道数据库表名，只知道请求地址和 JSON。HTTP Handler 把请求翻译成业务输入；Service 判断“这件事允许吗、需要做什么”；Store 负责把结果读写到存储。这里的 Service 通常是一个 Go 结构体，不是每个 Service 都独占一台服务器。

依赖注入就是“创建对象时把它需要的协作者交给它”。例如身份服务只要一个能创建用户、查会话的 Store；main 决定交给它内存实现还是 PostgreSQL 实现。接口定义能做什么，具体实现决定怎样做。不要从变量名字相同推断两个进程共享同一份内存。

### 登录成功究竟得到了什么

User 是长期账号，Device 描述设备，Session 是这次登录建立的可撤销会话，Token 是客户端出示的凭证。一个用户可以有多个设备和 Session；撤销一个 Session 不等于删除账号。PersonaVersion 又是另一种实体：它是角色配置快照，不是登录会话。

读下面每个 return 时都问：现在数据库是否已经写过东西？函数返回错误不意味着此前所有独立写入都自动撤销。只有明确处于同一事务中的写入才有共同回滚边界。

## 启动时选择持久化实现

源码：[cmd/api/main.go:48](/Users/windcry1/Documents/ai_companion/cmd/api/main.go:48)

<a id="symbols-1-1"></a>

### 函数与变量详解

函数/类型声明：[`main`](/Users/windcry1/Documents/ai_companion/cmd/api/main.go:38)。

**职责、输入输出与影响：** 程序入口，无参数、无返回值。此段装配存储和文件依赖，可能建立数据库连接、创建本地文件存储对象；初始化失败退出进程，并非业务请求函数。

**调用关系：** 运行程序时进入 main；NewMemoryStore 创建内存实现，persistence.Open 打开持久化实现，后续把选好的依赖交给各 Service。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `cfg` | config.Config | 启动阶段 config.Load 返回 | DatabaseDriver 决定内存/持久化分支，DocumentStorageDir 决定 Blob 目录。 |
| `identityStore / characterStore / conversationStore` | 各领域 Store 接口 | 先包装 NewMemoryStore 返回值 | 变量类型是接口，随后可赋同一个 persistentStore，而不是创建相同名字的多份数据库。 |
| `localBlobs / documentBlobs` | 本地实现 / document.BlobStore 接口 | NewLocalBlobStore | 前者为具体对象，后者按接口传给文档服务，保存的是原文件字节。 |
| `persistentStore` | persistence.ApplicationStore | 初始 nil，Open 成功后赋值 | 承载多个领域接口，连接失败时不继续混用部分内存数据。 |
| `connectCtx / cancel` | context.Context / context.CancelFunc | WithTimeout，期限 10 秒 | 只限制本次打开连接；cancel 释放计时资源，不是关闭已建立的整个数据库池。 |
| `systemLogStore / incidentStore / performanceStore` | 对应 Store 接口 | 初始 nil | 可选运行诊断依赖；不是每条聊天消息的内容。 |
| `err` | error | 初始化或打开连接的返回值 | 非 nil 时日志说明具体阶段并退出。 |

### 源码与逐行解释

```text
  48 │ 	identityStore := identity.Store(identity.NewMemoryStore())
  49 │ 	characterStore := character.Store(character.NewMemoryStore())
  50 │ 	conversationStore := conversation.Store(conversation.NewMemoryStore())
  51 │ 	memoryStore := memory.Store(memory.NewMemoryStore())
  52 │ 	documentStore := document.Store(document.NewMemoryStore())
  53 │ 	ledgerStore := ledger.Store(ledger.NewMemoryStore())
  54 │ 	plannerStore := planner.Store(planner.NewMemoryStore())
  55 │ 	skillStore := skill.Store(skill.NewMemoryStore())
  56 │ 	teamStore := team.Store(team.NewMemoryStore())
  57 │ 	emailStore := email.Store(email.NewMemoryStore())
  58 │ 	billingStore := billing.Store(billing.NewMemoryStore())
  59 │ 	safetyStore := safety.Store(safety.NewMemoryStore())
  60 │ 	localBlobs, err := document.NewLocalBlobStore(cfg.DocumentStorageDir)
  61 │ 	if err != nil {
  62 │ 		logger.Error("initialize document storage", "error", err)
  63 │ 		os.Exit(1)
  64 │ 	}
  65 │ 	documentBlobs := document.BlobStore(localBlobs)
```

- **L48–L59**：每行把一个具体内存 Store 转为对应领域接口；identity、character、conversation 等变量的静态类型是接口，因此稍后能替换为同一 PostgreSQL Store。
- **L60–L65**：创建文档文件存储；初始化失败记录错误并退出。成功后转为 BlobStore 接口，业务层无需知道目录细节。

```text
  66 │ 	var persistentStore persistence.ApplicationStore
  67 │ 	var systemLogStore opslog.Store
  68 │ 	var incidentStore incident.Store
  69 │ 	var performanceStore performance.Store
  70 │ 	if cfg.DatabaseDriver != "memory" {
  71 │ 		connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
  72 │ 		persistentStore, err = persistence.Open(connectCtx, cfg.DatabaseDriver, cfg.MySQLDSN, cfg.PostgresDSN)
  73 │ 		cancel()
  74 │ 		if err != nil {
  75 │ 			logger.Error("connect persistent database", "driver", cfg.DatabaseDriver, "error", err)
  76 │ 			os.Exit(1)
  77 │ 		}
  78 │ 		defer persistentStore.Close()
```

- **L66–L69**：声明可选持久化、日志、事件与性能 Store；零值为 nil，后面按实际能力装配。
- **L70**：只要配置不为 memory，就进入持久化分支。
- **L71**：给数据库连接设置 10 秒截止时间，防止启动无限等待。
- **L72**：按 driver 打开数据库；连接串由配置传入，领域服务不直接读取环境变量。
- **L73**：连接完成立刻释放本次连接 Context 的定时器。
- **L74–L77**：连接失败终止进程，不带着部分内存状态继续提供服务。
- **L78**：注册 main 正常返回时关闭数据库连接池；若直接调用 os.Exit，Go 不会执行 defer。

```text
  79 │ 		identityStore = persistentStore
  80 │ 		characterStore = persistentStore
  81 │ 		conversationStore = persistentStore
  82 │ 		memoryStore = persistentStore
  83 │ 		documentStore = persistentStore
  84 │ 		ledgerStore = persistentStore
  85 │ 		plannerStore = persistentStore
  86 │ 		skillStore = persistentStore
  87 │ 		teamStore = persistentStore
  88 │ 		emailStore = persistentStore
  89 │ 		billingStore = persistentStore
  90 │ 		safetyStore = persistentStore
  91 │ 		logger.Info("using persistent store", "driver", cfg.DatabaseDriver)
  92 │ 	} else {
  93 │ 		logger.Warn("DATABASE_DRIVER=memory; using non-persistent in-memory store")
  94 │ 		if cfg.SkillWorkerEnabled {
  95 │ 			cfg.SkillWorkerEnabled = false
  96 │ 			logger.Warn("durable Skill queue disabled because API and Worker cannot share the in-memory store")
  97 │ 		}
  98 │ 	}
```

- **L79–L90**：逐行将所有业务 Store 指向 persistentStore。接口一致，运行实例共享持久化底座。
- **L91–L93**：记录当前采用持久化还是内存模式，便于运行时排查。
- **L94–L98**：内存模式关闭持久化 Skill Worker：API 与另一进程的内存不共享，入队后无法由另一进程领取。


## 注册：输入转为用户和会话

源码：[internal/identity/service.go:116](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:116)

<a id="symbols-1-2"></a>

### 函数与变量详解

函数/类型声明：[`Register`](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:116)。

**职责、输入输出与影响：** 输入 ctx 与 RegisterInput；返回 (TokenPair, error)。创建 User 后再创建登录会话，涉及多次存储写入；校验失败返回空 TokenPair，两个阶段不在这一段共同事务里。

**调用关系：** 注册 HTTP 入口 → Register；normalizeEmail/校验 → hashPassword → CreateUser → createSession。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `s` | *identity.Service | 方法接收者 | 携带 store、tokens、refreshTTL、now；不是当前登录用户。 |
| `input` | RegisterInput 值 | HTTP 解码后传入 | Email 被规范化；Password 仅用于派生；DisplayName/Timezone/Locale/Device 决定用户与设备资料。 |
| `hash` | string | hashPassword(input.Password) | 带算法、盐、参数的密码派生记录；赋给 User.PasswordHash，不是明文密码。 |
| `now` | time.Time | s.now().UTC() | 统一本次创建/更新时间，测试可替换 s.now。 |
| `userID` | string | id.New() | 新账号身份，和随后 Session ID 不同。 |
| `user` | User | 由输入、ID、hash 组装 | CreateUser 写入，随后传入 createSession；创建成功不等于 Session 也成功。 |

### 源码与逐行解释

```text
 116 │ func (s *Service) Register(ctx context.Context, input RegisterInput) (TokenPair, error) {
 117 │ 	input.Email = normalizeEmail(input.Email)
 118 │ 	if err := validateEmailPasswordTimezone(input.Email, input.Password, input.Timezone); err != nil {
 119 │ 		return TokenPair{}, err
 120 │ 	}
 121 │ 	if strings.TrimSpace(input.DisplayName) == "" || len([]rune(input.DisplayName)) > 40 {
 122 │ 		return TokenPair{}, fmt.Errorf("%w: display_name must contain 1-40 characters", ErrValidation)
 123 │ 	}
 124 │ 	hash, err := hashPassword(input.Password)
 125 │ 	if err != nil {
 126 │ 		return TokenPair{}, fmt.Errorf("hash password: %w", err)
 127 │ 	}
 128 │ 	now := s.now().UTC()
 129 │ 	userID, err := id.New()
 130 │ 	if err != nil {
 131 │ 		return TokenPair{}, err
 132 │ 	}
 133 │ 	user := User{ID: userID, Email: input.Email, DisplayName: strings.TrimSpace(input.DisplayName), Timezone: input.Timezone, Locale: defaultString(input.Locale, "zh-CN"), Status: "active", PasswordHash: hash, CreatedAt: now, UpdatedAt: now}
```

- **L116**：注册服务接收 Context 和 RegisterInput，返回令牌对或错误。
- **L117**：统一邮箱格式，减少大小写、空白造成的重复身份。
- **L118–L120**：共同校验邮箱、密码和时区；失败在创建用户之前返回。
- **L121–L123**：昵称必须非空且不超过 40 个 Unicode 字符；rune 计数避免中文按 UTF-8 字节误算。
- **L124–L127**：密码先变为哈希，失败用 %w 包装并保留原错误链。
- **L128**：生成统一 UTC 时间；s.now 可在测试中替换。
- **L129–L132**：生成新用户 ID；失败不落库。
- **L133**：组装 User：昵称去空白、默认语言 zh-CN、状态 active，只保存 PasswordHash。

```text
 134 │ 	if err := s.store.CreateUser(ctx, user); err != nil {
 135 │ 		return TokenPair{}, err
 136 │ 	}
 137 │ 	return s.createSession(ctx, user, input.Device, input.Timezone)
 138 │ }
```

- **L134–L136**：通过 Store 创建用户；唯一邮箱等数据库约束在 Store 层生效。
- **L137–L138**：创建会话并返回令牌。注意 CreateUser 与 createSession 是先后调用；这里没有跨二者的同一事务保证。


## 把密码变成可验证、不可直接还原的记录

源码：[internal/identity/password.go:19](/Users/windcry1/Documents/ai_companion/internal/identity/password.go:19)

先区分原文、编码、哈希、带密钥的认证码。第 00 章有对照；不要把“Base64”“哈希”“加密”当成同义词。

<a id="symbols-1-3"></a>

### 函数与变量详解

函数/类型声明：[`hashPassword`](/Users/windcry1/Documents/ai_companion/internal/identity/password.go:19)、[`verifyPassword`](/Users/windcry1/Documents/ai_companion/internal/identity/password.go:31)。

**职责、输入输出与影响：** hashPassword(password string) 返回 (编码记录 string, error)；verifyPassword(encoded, password string) 返回 bool。前者生成随机盐，后者用记录中的参数重算并比较；二者不直接访问数据库。

**调用关系：** Register/改密码等路径调用派生函数；Login 调用 verifyPassword。pbkdf2.Key 计算派生结果，hmac.Equal 比较结果。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `password` | string | 本次用户输入 | 用于派生，不应记录到日志。 |
| `salt` | []byte | 生成时 make 16 字节后填随机数；验证时 Base64 解码 | 同一密码不同盐通常得到不同派生记录；验证必须复用原盐。 |
| `key` | []byte | pbkdf2.Key 的结果 | 编码后作为记录第四段；不是可解密的原密码。 |
| `encoded / parts` | string / []string | 存储记录 / 按 $ 拆分 | parts 包含算法、迭代数、盐、期望 key；先验段数再访问下标。 |
| `iterations` | int | strconv.Atoi(parts[1]) | 控制重新派生工作量，先检查范围，不能把坏参数直接交给计算。 |
| `want / got` | []byte / []byte | 存储期望值 / 本次重算值 | 名字分别表示“应该得到”和“实际得到”；相同且无错误才返回 true。 |

### 源码与逐行解释

```text
  19 │ func hashPassword(password string) (string, error) {
  20 │ 	salt := make([]byte, 16)
  21 │ 	if _, err := rand.Read(salt); err != nil {
  22 │ 		return "", fmt.Errorf("generate password salt: %w", err)
  23 │ 	}
  24 │ 	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyLength)
  25 │ 	if err != nil {
  26 │ 		return "", fmt.Errorf("derive password hash: %w", err)
  27 │ 	}
  28 │ 	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
  29 │ }
  30 │
  31 │ func verifyPassword(encoded, password string) bool {
  32 │ 	parts := strings.Split(encoded, "$")
  33 │ 	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
  34 │ 		return false
  35 │ 	}
  36 │ 	iterations, err := strconv.Atoi(parts[1])
```

- **L19**：输入是用户密码字符串，输出是编码后的派生记录与 error；成功也不返回原密码。
- **L20**：make 分配 16 字节盐值空间；此时每个字节为零，下一行才填入随机数。
- **L21**：rand.Read 写入 salt；_ 丢弃字节数，但仍检查错误。if 初始化中的 err 只在这个 if 范围内使用。
- **L22–L23**：随机数读取失败立刻返回空字符串和包装错误；不能拿全零盐继续运行。
- **L24**：用 SHA-256 作为 PBKDF2 内部函数，结合密码、随机盐、600000 次迭代和 32 字节长度得到 key。它不是把密码加密后等待解密。
- **L25–L27**：派生失败也终止；没有有效 key 就不能创建密码记录。
- **L28**：Sprintf 把算法、迭代次数、Base64 盐和 Base64 key 用 $ 分隔；第二个返回值 nil 表示本次成功。盐与参数一起存储是为了将来能用同样方法验证。
- **L29–L30**：结束生成函数，空行分隔下一个行为。
- **L31**：验证函数同时接收已存记录 encoded 与本次用户输入 password，返回是否匹配。
- **L32**：按 $ 拆出四部分，不是按点拆 Access Token。
- **L33–L35**：字段数或算法标记不匹配就拒绝；不能把不认识的数据当有效密码记录。
- **L36**：Atoi 把第二部分从十进制字符串转为整数。

```text
  37 │ 	if err != nil || iterations < 100_000 || iterations > 2_000_000 {
  38 │ 		return false
  39 │ 	}
  40 │ 	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
  41 │ 	if err != nil || len(salt) < 16 {
  42 │ 		return false
  43 │ 	}
  44 │ 	want, err := base64.RawStdEncoding.DecodeString(parts[3])
  45 │ 	if err != nil || len(want) != passwordKeyLength {
  46 │ 		return false
  47 │ 	}
  48 │ 	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
  49 │ 	return err == nil && hmac.Equal(got, want)
  50 │ }
```

- **L37–L39**：解析错误，或迭代数低于 100000、高于 2000000 时拒绝；这是本实现接受的参数边界。
- **L40**：把第三部分从 Base64 文本还原为盐的原始字节。
- **L41–L43**：解码失败或盐长度不足 16 字节时拒绝。
- **L44**：第四部分解码为数据库保存的期望结果 want。
- **L45–L47**：要求 want 恰好为配置的 32 字节。
- **L48**：用本次输入密码、原盐和原迭代数重新派生 got；这里不是反向还原原密码。
- **L49**：必须计算成功并且 got 与 want 匹配；&& 短路意味着发生错误后不再进行后一个判断。hmac.Equal 用于比较密码学字节结果。
- **L50**：验证函数结束。


## Access Token 的字段、签发与验证

源码：[internal/identity/token.go:18](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:18)

本段能帮助看懂结构体标签、多返回值、切片、短路逻辑与指针。不要把任何真实 Token、密钥或密码粘贴到公开解码网站。

<a id="symbols-1-4"></a>

### 函数与变量详解

函数/类型声明：[`AccessClaims`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:18)、[`TokenManager`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:25)、[`NewTokenManager`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:30)、[`IssueAccess`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:34)、[`ParseAccess`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:46)、[`NewRefreshToken`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:62)、[`HashRefreshToken`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:71)、[`sign`](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:76)。

**职责、输入输出与影响：** AccessClaims 是声明字段类型，TokenManager 保存密钥和 TTL。IssueAccess 返回 (令牌, 过期时刻, error)，ParseAccess 返回 (声明, error)；NewRefreshToken 返回 (原文, 哈希, error)，HashRefreshToken/sign 返回字符串。除随机数读取外，本段是编码与校验，不自行查 Session。

**调用关系：** 身份 Service 调用 IssueAccess/ParseAccess；构造器保存配置；IssueAccess 与 ParseAccess 共用 sign，刷新会话用 NewRefreshToken/HashRefreshToken。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `m` | TokenManager | 方法接收者值 | m.secret 用于 HMAC；m.accessTTL 决定 Access Token 有效时长。 |
| `claims` | AccessClaims | IssueAccess 中组装；ParseAccess 中由 JSON 填充 | UserID/sub、SessionID/sid、ExpiresAt/exp、IssuedAt/iat；两个时间为 Unix 秒。 |
| `expiresAt` | time.Time | now.Add(m.accessTTL) | 业务时间点；不同于 int64 的 claims.ExpiresAt 表示形式。 |
| `payload` | []byte 或 sign 参数 string | JSON 编码/解码结果；sign 接收编码文本 | 同名跨函数类型不同：先看当前签名，不要按名字推断类型。 |
| `encoded / signature` | string | Base64URL(JSON) / sign(encoded) | 拼为 v1.payload.signature；编码不隐藏声明，认证码防止无密钥篡改。 |
| `parts` | []string | strings.Split(token, ".") | 检查三段与 v1 后才验证/解码；不是 JWT Header。 |
| `plain / hash` | string / string | 32 随机字节的编码 / SHA-256 文本 | plain 交客户端，hash 供服务端查刷新会话；不是同一份内容的两个变量名。 |
| `sum` | [32]byte | sha256.Sum256 | 固定数组；sum[:] 取切片交十六进制编码。 |
| `mac` | hash.Hash 接口 | hmac.New | Write 输入待认证文本，Sum(nil) 取认证码字节。 |

### 源码与逐行解释

```text
  18 │ type AccessClaims struct {
  19 │ 	UserID    string `json:"sub"`
  20 │ 	SessionID string `json:"sid"`
  21 │ 	ExpiresAt int64  `json:"exp"`
  22 │ 	IssuedAt  int64  `json:"iat"`
  23 │ }
  24 │
  25 │ type TokenManager struct {
  26 │ 	secret    []byte
  27 │ 	accessTTL time.Duration
  28 │ }
  29 │
  30 │ func NewTokenManager(secret string, accessTTL time.Duration) TokenManager {
  31 │ 	return TokenManager{secret: []byte(secret), accessTTL: accessTTL}
  32 │ }
  33 │
  34 │ func (m TokenManager) IssueAccess(userID, sessionID string, now time.Time) (string, time.Time, error) {
  35 │ 	expiresAt := now.Add(m.accessTTL)
  36 │ 	claims := AccessClaims{UserID: userID, SessionID: sessionID, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix()}
  37 │ 	payload, err := json.Marshal(claims)
```

- **L18**：定义一个结构体类型；类型声明本身不创建任何用户会话。
- **L19**：Go 字段名为 UserID，JSON 输出键为 sub；反引号中的标签指导序列化，不是字段值。
- **L20**：sid 保存 Session ID，让请求能回查数据库会话。
- **L21–L22**：exp 与 iat 是 Unix 秒时间，用 int64 存储；前者控制到期，后者记录签发时刻。
- **L23–L24**：结束字段集合，分隔下一类型。
- **L25**：TokenManager 是签发与验证所需配置的组合。
- **L26**：secret 为字节切片，供 HMAC 使用；小写字段名不从包外直接访问。
- **L27–L28**：accessTTL 表示从签发到过期的时长，而不是绝对时间点。
- **L29–L32**：构造函数把字符串密钥转为字节切片，按值返回 TokenManager；尚未签发任何 Token。
- **L33–L34**：IssueAccess 是 TokenManager 的方法；括号中的 m 是接收者，最后括号声明三个返回值类型。
- **L35**：过期点等于 now 加 accessTTL；now 由调用者提供便于测试。
- **L36**：创建本次声明对象；userID 与 sessionID 来自可信服务逻辑，而不是 Token 自己证明出来的。
- **L37**：json.Marshal 把结构体序列化为 JSON 字节；payload 是字节切片。

```text
  38 │ 	if err != nil {
  39 │ 		return "", time.Time{}, fmt.Errorf("marshal access claims: %w", err)
  40 │ 	}
  41 │ 	encoded := base64.RawURLEncoding.EncodeToString(payload)
  42 │ 	signature := m.sign(encoded)
  43 │ 	return "v1." + encoded + "." + signature, expiresAt, nil
  44 │ }
  45 │
  46 │ func (m TokenManager) ParseAccess(token string, now time.Time) (AccessClaims, error) {
  47 │ 	parts := strings.Split(token, ".")
  48 │ 	if len(parts) != 3 || parts[0] != "v1" || !hmac.Equal([]byte(parts[2]), []byte(m.sign(parts[1]))) {
  49 │ 		return AccessClaims{}, ErrInvalidToken
  50 │ 	}
  51 │ 	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
  52 │ 	if err != nil {
  53 │ 		return AccessClaims{}, ErrInvalidToken
  54 │ 	}
  55 │ 	var claims AccessClaims
  56 │ 	if err := json.Unmarshal(payload, &claims); err != nil || claims.UserID == "" || claims.SessionID == "" || now.Unix() >= claims.ExpiresAt {
```

- **L38–L40**：序列化错误时，三个返回位置分别给空字符串、零时间和错误。
- **L41**：Base64URL 把 JSON 字节变成适合传输的文本；任何拿到它的人都可以解码，这不是加密。
- **L42**：对编码后的 payload 计算 HMAC 认证码。
- **L43**：本项目格式为 v1.payload.signature；不是标准 JWT 的 header.payload.signature，不能仅凭三段就叫 JWT。
- **L44–L46**：结束签发方法，开始解析方法；解析成功返回结构化声明。
- **L47**：按点切分输入字符串，结果为字符串切片。
- **L48**：依次检查段数、v1 标记、HMAC；|| 短路保护 parts 下标：段数不是 3 时不执行后续访问。签名比较也不会证明会话尚未撤销。
- **L49–L50**：任一条件失败就返回空声明与统一 InvalidToken。
- **L51**：签名匹配后，把 payload 从 Base64URL 解码回 JSON 字节。
- **L52–L54**：不合法编码直接拒绝。
- **L55**：声明零值 AccessClaims，等待下一行填充。
- **L56**：&claims 把变量地址交给 Unmarshal，允许其修改 claims；同时检查两个 ID 非空、当前时间严格早于 exp。if 内部的 err 属于这个 if 的作用域。

```text
  57 │ 		return AccessClaims{}, ErrInvalidToken
  58 │ 	}
  59 │ 	return claims, nil
  60 │ }
  61 │
  62 │ func NewRefreshToken() (plain, hash string, err error) {
  63 │ 	value := make([]byte, 32)
  64 │ 	if _, err = rand.Read(value); err != nil {
  65 │ 		return "", "", fmt.Errorf("generate refresh token: %w", err)
  66 │ 	}
  67 │ 	plain = base64.RawURLEncoding.EncodeToString(value)
  68 │ 	return plain, HashRefreshToken(plain), nil
  69 │ }
  70 │
  71 │ func HashRefreshToken(token string) string {
  72 │ 	sum := sha256.Sum256([]byte(token))
  73 │ 	return hex.EncodeToString(sum[:])
  74 │ }
  75 │
  76 │ func (m TokenManager) sign(payload string) string {
```

- **L57–L58**：JSON 不合法、缺少核心字段或已到期，都拒绝。
- **L59–L60**：返回声明与 nil；后续 Authenticate 仍需查询 Session/User。
- **L61–L62**：新函数使用命名返回值 plain、hash、err；它生成的是 Refresh Token。
- **L63**：分配 32 字节随机原文的缓冲区。
- **L64–L66**：rand.Read 填充它；读取失败不返回一个可用令牌。
- **L67**：把随机字节编码成客户端可持有的文本。
- **L68–L69**：返回原文与原文哈希；原文用于客户端刷新请求，数据库保存哈希。
- **L70–L71**：声明对 Refresh Token 原文计算哈希的纯函数。
- **L72**：SHA-256 产生固定大小字节数组；输入 string 先转为 []byte。
- **L73–L74**：sum[:] 把数组视为切片，十六进制编码成存储文本。
- **L75–L76**：sign 是包内方法；接收要认证的 payload 字符串。

```text
  77 │ 	mac := hmac.New(sha256.New, m.secret)
  78 │ 	_, _ = mac.Write([]byte(payload))
  79 │ 	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
  80 │ }
```

- **L77**：创建带服务端密钥的 HMAC-SHA256 计算器；sha256.New 是函数值。
- **L78**：写入 payload；这里的 Write 接口返回字节数和错误，但该 HMAC 实现不会因普通字节写入返回错误，代码显式丢弃它们。
- **L79–L80**：Sum(nil) 取得当前认证码，再编码为 Base64URL 文本。


## 登录后建立设备和 Session

源码：[internal/identity/service.go:235](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:235)

<a id="symbols-1-5"></a>

### 函数与变量详解

函数/类型声明：[`createSession`](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:235)。

**职责、输入输出与影响：** 输入 ctx、已验证 User、DeviceInput、timezone；返回 (TokenPair, error)。写/复用设备，再写新 Session，最后签发访问令牌；无模型调用。

**调用关系：** Register/Login 共用此函数；UpsertDevice → NewRefreshToken → CreateSession → IssueAccess。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `user` | User | 注册创建或登录查出的账号 | user.ID 绑定设备与会话，不从 DeviceInput 决定归属。 |
| `input` | DeviceInput | 客户端设备描述 | DeviceKey 用于设备归并；Name/Platform 是展示和平台信息，不是账户权限。 |
| `deviceID / device` | string / Device | 备用新 ID / UpsertDevice 返回对象 | Upsert 可能复用旧设备，所以 Session 使用 device.ID 而非机械使用 deviceID。 |
| `plain / hash` | string / string | NewRefreshToken | plain 只返回客户端；hash 保存到 session.TokenHash。 |
| `sessionID / session` | string / Session | 新会话 ID / 组装记录 | 保存 UserID、DeviceID、期限、最近使用时刻等；与 AccessClaims 中 sid 对应。 |
| `access / accessExpiry` | string / time.Time | IssueAccess 返回值 | 分别放入 TokenPair.AccessToken、AccessExpiresAt。 |
| `timezone` | string | 上层已选择的时区 | 写入设备记录，用来解释本地时间；不是令牌时钟偏移。 |

### 源码与逐行解释

```text
 235 │ func (s *Service) createSession(ctx context.Context, user User, input DeviceInput, timezone string) (TokenPair, error) {
 236 │ 	if input.Platform != "" && input.Platform != "web" && input.Platform != "ios" && input.Platform != "android" && input.Platform != "service" {
 237 │ 		return TokenPair{}, fmt.Errorf("%w: invalid device platform", ErrValidation)
 238 │ 	}
 239 │ 	now := s.now().UTC()
 240 │ 	deviceID, err := id.New()
 241 │ 	if err != nil {
 242 │ 		return TokenPair{}, err
 243 │ 	}
 244 │ 	device, err := s.store.UpsertDevice(ctx, Device{ID: deviceID, UserID: user.ID, DeviceKey: defaultString(input.DeviceKey, deviceID), Name: defaultString(input.Name, "Web Browser"), Platform: defaultString(input.Platform, "web"), Timezone: timezone, LastSeen: now})
 245 │ 	if err != nil {
 246 │ 		return TokenPair{}, err
 247 │ 	}
 248 │ 	plain, hash, err := NewRefreshToken()
 249 │ 	if err != nil {
 250 │ 		return TokenPair{}, err
 251 │ 	}
```

- **L235**：注册与登录共用此函数，身份已由上游确定。
- **L236–L238**：平台只能为空或 web/ios/android/service，非法平台立即拒绝。
- **L239**：固定本次操作时间。
- **L240–L243**：先生成备用设备 ID。
- **L244**：按用户和 DeviceKey 更新/创建设备；缺省名称、平台、时区和 LastSeen 在这里落入结构体。Upsert 可能返回已有设备，因此后面使用 device.ID。
- **L245–L247**：设备写入失败结束本次会话建立。
- **L248–L251**：生成 Refresh Token 原文与哈希；原文只交给客户端，哈希用于数据库查找。

```text
 252 │ 	sessionID, err := id.New()
 253 │ 	if err != nil {
 254 │ 		return TokenPair{}, err
 255 │ 	}
 256 │ 	session := Session{ID: sessionID, UserID: user.ID, DeviceID: device.ID, TokenHash: hash, ExpiresAt: now.Add(s.refreshTTL), CreatedAt: now, LastUsedAt: now}
 257 │ 	if err := s.store.CreateSession(ctx, session); err != nil {
 258 │ 		return TokenPair{}, err
 259 │ 	}
 260 │ 	access, accessExpiry, err := s.tokens.IssueAccess(user.ID, session.ID, now)
 261 │ 	if err != nil {
 262 │ 		return TokenPair{}, err
 263 │ 	}
 264 │ 	return TokenPair{AccessToken: access, RefreshToken: plain, TokenType: "Bearer", AccessExpiresAt: accessExpiry, RefreshExpiresAt: session.ExpiresAt, User: user}, nil
 265 │ }
```

- **L252–L255**：给新 Session 分配独立 ID。
- **L256**：Session 绑定用户、设备、刷新令牌哈希、过期时间和使用时间。
- **L257–L259**：先写 Session，失败不签发可用的 Access Token。
- **L260–L263**：Access Token 绑定 user.ID 与 session.ID，便于撤销时回查 Session。
- **L264–L265**：一次返回两个 Token、两个过期时间和用户资料。Refresh Token 原文出现在响应中，不在 Session 记录里。


## 刷新令牌：轮换旧会话

源码：[internal/identity/service.go:152](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:152)

<a id="symbols-1-6"></a>

### 函数与变量详解

函数/类型声明：[`Refresh`](/Users/windcry1/Documents/ai_companion/internal/identity/service.go:152)。

**职责、输入输出与影响：** 输入刷新令牌原文，返回新的 (TokenPair, error)。先查旧会话，再 RotateSession 轮换；旧会话与新会话的原子关系由 Store 实现，不是重复返回旧 Token。

**调用关系：** 刷新 HTTP 入口调用；HashRefreshToken → FindSessionByTokenHash → GetUser → RotateSession → IssueAccess。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `refreshToken` | string | 客户端提交 | 哈希后查库；不拿明文 Token 查询存储字段。 |
| `current` | Session | FindSessionByTokenHash | 旧会话，检查撤销与过期；其 UserID/DeviceID 继承给新会话。 |
| `user` | User | 按 current.UserID 查库 | 用于新 Access Token 声明与响应资料。 |
| `plain / hash` | string / string | 新生成的刷新令牌对 | 覆盖的是客户端后续持有的凭证，不是把旧明文写回。 |
| `newID / next` | string / Session | id.New / 新记录 | next.ID 不等于 current.ID；RotateSession 连接新旧会话。 |
| `access / accessExpiry` | string / time.Time | 绑定 next.ID 的 IssueAccess | 旧 Access Token 的会话身份与新签发的不一样。 |

### 源码与逐行解释

```text
 152 │ func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
 153 │ 	now := s.now().UTC()
 154 │ 	current, err := s.store.FindSessionByTokenHash(ctx, HashRefreshToken(refreshToken))
 155 │ 	if err != nil || current.RevokedAt != nil || !now.Before(current.ExpiresAt) {
 156 │ 		return TokenPair{}, ErrUnauthorized
 157 │ 	}
 158 │ 	user, err := s.store.GetUser(ctx, current.UserID)
 159 │ 	if err != nil {
 160 │ 		return TokenPair{}, ErrUnauthorized
 161 │ 	}
 162 │ 	plain, hash, err := NewRefreshToken()
 163 │ 	if err != nil {
 164 │ 		return TokenPair{}, err
 165 │ 	}
 166 │ 	newID, err := id.New()
 167 │ 	if err != nil {
 168 │ 		return TokenPair{}, err
 169 │ 	}
 170 │ 	next := Session{ID: newID, UserID: current.UserID, DeviceID: current.DeviceID, TokenHash: hash, ExpiresAt: now.Add(s.refreshTTL), CreatedAt: now, LastUsedAt: now}
```

- **L152–L154**：获取当前时间，把客户端刷新令牌哈希后查 Session；不是用明文查库。
- **L155–L157**：不存在、已撤销或到期均统一视为未授权。
- **L158–L161**：重新读取用户；用户不存在或无法读取时拒绝刷新。
- **L162–L165**：生成下一对刷新令牌原文/哈希。
- **L166–L169**：生成新 Session ID，而不是重用旧记录。
- **L170**：保留用户和设备关系，新建有效期及 TokenHash。

```text
 171 │ 	if err := s.store.RotateSession(ctx, current.ID, next, now); err != nil {
 172 │ 		return TokenPair{}, ErrUnauthorized
 173 │ 	}
 174 │ 	access, accessExpiry, err := s.tokens.IssueAccess(user.ID, next.ID, now)
 175 │ 	if err != nil {
 176 │ 		return TokenPair{}, err
 177 │ 	}
 178 │ 	return TokenPair{AccessToken: access, RefreshToken: plain, TokenType: "Bearer", AccessExpiresAt: accessExpiry, RefreshExpiresAt: next.ExpiresAt, User: user}, nil
 179 │ }
```

- **L171–L173**：Store.RotateSession 负责旧会话失效与新会话创建；并发刷新冲突映射成未授权。
- **L174–L177**：签发绑定新 Session 的 Access Token。
- **L178–L179**：把新的 TokenPair 返回客户端；前端需替换旧令牌。


## 每次业务请求怎样获得身份

源码：[internal/httpserver/auth.go:116](/Users/windcry1/Documents/ai_companion/internal/httpserver/auth.go:116)

<a id="symbols-1-7"></a>

### 函数与变量详解

函数/类型声明：[`requireAuth`](/Users/windcry1/Documents/ai_companion/internal/httpserver/auth.go:116)。

**职责、输入输出与影响：** 输入 next http.Handler，返回包裹后的 http.Handler。返回值不是认证结果；新 Handler 每次收到请求才认证，并可能写错误 HTTP 响应或更新在线状态。

**调用关系：** 路由装配时用 requireAuth 包裹业务 Handler；运行时调用 identity.Authenticate → realtime.Touch → next.ServeHTTP。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `s` | *httpserver.Server | HTTP 服务器依赖对象 | 其中 identity 是身份服务，不是 Store。 |
| `next` | http.Handler | 路由装配传入的下游 | 只有认证成功才调用；闭包保留对它的访问。 |
| `w / r` | http.ResponseWriter / *http.Request | 服务器交给 Handler | w 输出响应，r 读取头、路径、请求体和 Context。 |
| `header / parts` | string / []string | Authorization / 按首个空格拆分 | parts[0] 为 Bearer 方案，parts[1] 为凭证原文。 |
| `user / claims` | identity.User / identity.AccessClaims | Authenticate 的可信返回 | 封装为 authenticated 写入新请求 Context，供 currentAuth 读取。 |
| `authContextKey` | 私有键类型 | Context.WithValue 使用 | 隔离上下文键，避免把普通字符串键碰撞成身份数据。 |

### 源码与逐行解释

```text
 116 │ func (s *Server) requireAuth(next http.Handler) http.Handler {
 117 │ 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
 118 │ 		header := r.Header.Get("Authorization")
 119 │ 		parts := strings.SplitN(header, " ", 2)
 120 │ 		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
 121 │ 			writeDomainError(w, identity.ErrUnauthorized)
 122 │ 			return
 123 │ 		}
 124 │ 		user, claims, err := s.identity.Authenticate(r.Context(), parts[1])
 125 │ 		if err != nil {
 126 │ 			writeDomainError(w, err)
 127 │ 			return
 128 │ 		}
 129 │ 		_ = s.realtime.Touch(r.Context(), user.ID, claims.SessionID, s.presenceTTL)
 130 │ 		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, authenticated{User: user, Claims: claims})))
 131 │ 	})
 132 │ }
```

- **L116–L117**：这是中间件工厂：接收后续 Handler，返回一个包住它的新 Handler。
- **L118–L119**：读取 Authorization，并只切成 scheme 与 token 两段。
- **L120–L123**：要求 Bearer 格式；失败写 HTTP 错误并终止，不进入业务。
- **L124**：Authenticate 校验 Token 并查 Session/User；不能只靠 Token 签名判断当前会话仍有效。
- **L125–L128**：认证失败由公共错误映射函数转换响应。
- **L129**：Touch 在线状态属于尽力而为，返回错误被显式忽略，因此在线状态更新失败不阻断业务。
- **L130**：将 authenticated 结构放入 Context，再调用 next。后续 currentAuth(r) 取得的是这里建立的可信身份。
- **L131–L132**：关闭闭包与工厂函数。


## 创建和版本化角色

源码：[internal/character/service.go:78](/Users/windcry1/Documents/ai_companion/internal/character/service.go:78)

<a id="symbols-1-8"></a>

### 函数与变量详解

函数/类型声明：[`Create`](/Users/windcry1/Documents/ai_companion/internal/character/service.go:78)、[`compile`](/Users/windcry1/Documents/ai_companion/internal/character/service.go:187)。

**职责、输入输出与影响：** Create 接收 userID 和角色 Input，返回 (Character, PersonaVersion, error)。创建角色及第 1 版编译快照并存储；compile 将配置转为 Persona，不向模型发请求。

**调用关系：** 角色创建入口 → applyCreateDefaults/validate → fromInput → compile → store.Create。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `input` | character.Input | 客户端角色设置 | 默认模块等由 applyCreateDefaults 补齐，validate 后才建对象。 |
| `characterID` | string | id.New() | 稳定角色身份，不随 Persona 版本递增而换 ID。 |
| `character` | Character | fromInput(..., 1, ...) | 包含归属、模块和 PersonaVersion=1。 |
| `persona` | PersonaVersion | compile(character, now) | 该版可执行/可引用的角色配置快照；并非一次模型回复。 |
| `now` | time.Time | s.now().UTC() | 角色及本版快照的共同时间依据。 |

### 源码与逐行解释

```text
  78 │ func (s *Service) Create(ctx context.Context, userID string, input Input) (Character, PersonaVersion, error) {
  79 │ 	input = applyCreateDefaults(input)
  80 │ 	if err := validate(input); err != nil {
  81 │ 		return Character{}, PersonaVersion{}, err
  82 │ 	}
  83 │ 	characterID, err := id.New()
  84 │ 	if err != nil {
  85 │ 		return Character{}, PersonaVersion{}, err
  86 │ 	}
  87 │ 	now := s.now().UTC()
  88 │ 	character := fromInput(characterID, userID, 1, now, now, input)
  89 │ 	persona := compile(character, now)
  90 │ 	if err := s.store.Create(ctx, character, persona); err != nil {
  91 │ 		return Character{}, PersonaVersion{}, err
  92 │ 	}
  93 │ 	return character, persona, nil
  94 │ }
```

- **L78–L79**：创建角色先按 companion/life/work 填默认值。
- **L80–L82**：验证角色字段；失败返回两个空结果。
- **L83–L86**：生成 Character ID。
- **L87–L88**：创建第 1 版 Character，并固定创建/更新时间。
- **L89**：compile 把角色字段编译成 PersonaVersion；它不是模型调用，而是结构化配置转换。
- **L90–L92**：Store 同时接收 Character 和 Persona，保存当前角色及版本快照。
- **L93–L94**：返回角色和已编译 Persona。


## 修改角色为什么保留旧版本

源码：[internal/character/service.go:134](/Users/windcry1/Documents/ai_companion/internal/character/service.go:134)

<a id="symbols-1-9"></a>

### 函数与变量详解

函数/类型声明：[`Update`](/Users/windcry1/Documents/ai_companion/internal/character/service.go:134)。

**职责、输入输出与影响：** 输入 userID、characterID、完整角色 Input；返回 (新 Character, 新 PersonaVersion, error)。先按归属读旧角色，再保留身份并提升版本、保存更新。

**调用关系：** 角色修改入口 → store.Get → validate → fromInput/compile → store.Update。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `current` | Character | 按 userID+characterID 查询 | 原 ID、归属、创建时刻与旧版本的依据。 |
| `input` | Input | 本次修改字段 | Module 为空时继承 current.Module，仍需重新校验。 |
| `updated` | Character | fromInput(current.ID, ..., current.PersonaVersion+1, ...) | 新版本对象；不是把 current.ID 增一。 |
| `persona` | PersonaVersion | compile(updated, updated.UpdatedAt) | 与 updated 对应的配置快照。 |
| `characterID / userID` | string / string | 调用方参数 | 一个定位资源，一个限定归属，不能只保留 characterID 查询。 |

### 源码与逐行解释

```text
 134 │ func (s *Service) Update(ctx context.Context, userID, characterID string, input Input) (Character, PersonaVersion, error) {
 135 │ 	current, err := s.store.Get(ctx, userID, characterID)
 136 │ 	if err != nil {
 137 │ 		return Character{}, PersonaVersion{}, err
 138 │ 	}
 139 │ 	if strings.TrimSpace(input.Module) == "" {
 140 │ 		input.Module = current.Module
 141 │ 	}
 142 │ 	if err := validate(input); err != nil {
 143 │ 		return Character{}, PersonaVersion{}, err
 144 │ 	}
 145 │ 	updated := fromInput(current.ID, current.UserID, current.PersonaVersion+1, current.CreatedAt, s.now().UTC(), input)
 146 │ 	persona := compile(updated, updated.UpdatedAt)
 147 │ 	if err := s.store.Update(ctx, updated, persona); err != nil {
 148 │ 		return Character{}, PersonaVersion{}, err
 149 │ 	}
 150 │ 	return updated, persona, nil
 151 │ }
```

- **L134–L138**：带 userID 读取原角色，避免修改其他用户角色；读取失败终止。
- **L139–L141**：旧客户端没传 Module 时保留原模块。
- **L142–L144**：新输入仍须通过同一校验。
- **L145**：保留 ID、用户、创建时间，PersonaVersion 加 1，并更新其余字段。
- **L146**：使用新版本角色编译新的 Persona 快照。
- **L147–L151**：保存两个对象并返回；旧 Persona 版本因此可继续被历史运行引用。


## 把代码连成一次真实行为

### 用虚构数据走一遍注册与访问

假设用户提交邮箱“ A@example.com ”、昵称“小林”、合法密码和 Asia/Shanghai。以下 U1、D1、S1 只是阅读代号，不是实际 UUID。

| 步骤 | 变量/存储变化 | 此时还没有发生什么 |
|---|---|---|
| 规范化 | 邮箱去空白并统一格式；昵称计数按 Unicode 码点 | 没有账号写入 |
| 密码派生 | 生成随机盐，得到带参数的 PasswordHash | 没有把明文密码作为 User 字段保存 |
| CreateUser | 用户 U1 写入 | 还没有登录 Session |
| UpsertDevice | 得到设备 D1；可能复用已有设备 | 还没有有效的新刷新令牌记录 |
| CreateSession | S1 绑定 U1、D1、刷新令牌哈希和期限 | 尚未返回给浏览器 |
| IssueAccess | Token 声明包含 U1、S1、iat、exp | 没有调用大模型 |
| 业务请求 | Bearer Token 验签、查 Session、查用户，再进入 Handler | 客户端写在正文里的 user_id 不能替代可信身份 |

为什么注册写 User 后建立 Session 失败，不能简单说“注册完全没发生”？因为这两个步骤不在这一段共同事务里。排障要先查哪个步骤已成功；产品补偿与重试策略需要另读对应实现。

### 三种看起来像“乱码”的东西

密码记录使用带随机盐的 PBKDF2 派生结果；验证是重新计算后比较。Refresh Token 原文是高随机性的字节编码，数据库保存它的 SHA-256 哈希。Access Token 则保留可解码声明，通过服务端密钥生成 HMAC。三者目的不同，不能互换。Base64 只改变表示方式，不隐藏内容；盐不是需要与密码一起保密的“第二个密码”。

源码 Access Token 以 v1 开头，是本项目自定义格式，不是标准 JWT。验证签名只能说明内容未被无密钥的一方任意修改；Session 查询才能应用撤销等当前状态。过期时间也不是前端倒计时决定的，而由服务端校验。

### 自测与答案

问：把 UserID 放进前端请求体，为什么不能直接当登录身份？答：请求体可由请求者自行构造；可信身份来自通过 Token 和 Session 校验的中间件。

问：角色更新为什么不把旧 Persona 直接覆盖掉？答：历史运行已捕获某一版本；保留版本让恢复和排查可以追溯当时实际配置，但这不保证随机模型每次生成完全相同文本。

## 对照阅读与验证

前端登录/注册入口：[web/app/companion-client.tsx](/Users/windcry1/Documents/ai_companion/web/app/companion-client.tsx) 的 login、register、storeSession、refreshSession。继续追 [internal/identity/service.go](/Users/windcry1/Documents/ai_companion/internal/identity/service.go) 的 Login、Authenticate、ChangePassword；数据库细节在 [internal/persistence/postgresstore/identity.go](/Users/windcry1/Documents/ai_companion/internal/persistence/postgresstore/identity.go)。认证与资料相关测试可从 [internal/httpserver/auth_profile_test.go](/Users/windcry1/Documents/ai_companion/internal/httpserver/auth_profile_test.go) 阅读。

[返回目录](README.md) · [下一章：从发送按钮到聊天入口](02-chat-intake.md)
