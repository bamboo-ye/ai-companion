# 09 团队分享、管理员登录与额度

[阅读总入口](README.md)

这些行为复用共同数据库，但身份不同：普通用户 Token 操作个人/团队资源；管理员 Passkey Session 操作管理接口。配额在高成本行为接受之前检查，额度策略与已发生用量分别存储。

每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。

## 初学者预备课：先理解问题

### 认证、授权、限额是三个问题

认证问“你是谁”，授权问“你能操作这个资源吗”，限额问“这次还能否消耗资源”。用户登录成功不表示能读另一个 Workspace 文件；是 Workspace 成员不表示能用管理员接口；管理员身份也不自动证明一次高风险操作满足新鲜验证要求。

邀请是一条带邮箱和期限的待接受记录，不是发出邮件就已经把人加进团队。邮件投递状态与团队成员状态独立。文件分享也要显式建立相应关系，不会因为两个人同处一个空间就默认公开各自全部个人资料。

Passkey 通过服务器挑战和设备凭证签名来证明控制权；服务端验证依赖库处理协议细节。这里读的是应用如何保存/消费挑战、绑定用户、建立/撤销管理会话，不要求初学者先实现密码学协议。

## 创建空间时同时成为 Owner

源码：[internal/team/service.go:76](/Users/windcry1/Documents/ai_companion/internal/team/service.go:76)

<a id="symbols-9-1"></a>

### 函数与变量详解

函数/类型声明：[`CreateWorkspace`](/Users/windcry1/Documents/ai_companion/internal/team/service.go:76)。

**职责、输入输出与影响：** 输入已认证 identity.User 与 name，返回 (Workspace,error)。构造空间及创建者 owner 成员，交同一 Store 方法保存；不是只插一个空间名。

**调用关系：** 团队创建接口 → CreateWorkspace → Store.CreateWorkspace(ctx, workspace, member)。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `user` | identity.User | 认证边界提供 | ID 为空间归属，Email/DisplayName 复制到成员资料。 |
| `name` | string | 输入清理后 | 按 rune 限 2–80。 |
| `workspaceID / workspace` | string / Workspace | 新 ID / 空间对象 | OwnerID=user.ID、Status=active。 |
| `member` | Member | 创建者的成员记录 | Role=owner、Status=active，WorkspaceID 关联新空间。 |
| `now` | time.Time | 服务时钟 | 空间创建与成员加入时间共用。 |

### 源码与逐行解释

```text
  76 │ func (s *Service) CreateWorkspace(ctx context.Context, user identity.User, name string) (Workspace, error) {
  77 │ 	name = strings.TrimSpace(name)
  78 │ 	if len([]rune(name)) < 2 || len([]rune(name)) > 80 {
  79 │ 		return Workspace{}, fmt.Errorf("%w: workspace name must contain 2-80 characters", ErrValidation)
  80 │ 	}
  81 │ 	workspaceID, err := id.New()
  82 │ 	if err != nil {
  83 │ 		return Workspace{}, err
  84 │ 	}
  85 │ 	now := s.now().UTC()
  86 │ 	workspace := Workspace{ID: workspaceID, Name: name, OwnerID: user.ID, Status: "active", CreatedAt: now, UpdatedAt: now}
  87 │ 	member := Member{WorkspaceID: workspaceID, UserID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Role: "owner", Status: "active", JoinedAt: now, UpdatedAt: now}
  88 │ 	if err = s.store.CreateWorkspace(ctx, workspace, member); err != nil {
  89 │ 		return Workspace{}, err
  90 │ 	}
  91 │ 	return workspace, nil
  92 │ }
```

- **L76–L80**：清理并限制空间名称为 2–80 个字符。
- **L81–L84**：生成 Workspace ID。
- **L85–L87**：构造 active Workspace 和 owner Member，成员显示资料来自当前已认证用户。
- **L88–L92**：Store 同时保存空间与首位成员，再返回；不会通过邮件邀请自己成为 Owner。


## 邀请是受权限约束的待接受记录

源码：[internal/team/service.go:109](/Users/windcry1/Documents/ai_companion/internal/team/service.go:109)

<a id="symbols-9-2"></a>

### 函数与变量详解

函数/类型声明：[`Invite`](/Users/windcry1/Documents/ai_companion/internal/team/service.go:109)。

**职责、输入输出与影响：** 输入 inviter identity.User、workspaceID、受邀 email、role，返回 (Invitation,error)。校验邀请方为活跃 owner/admin 后保存待接受邀请；此函数并不直接投递邮件或让对方立即成为成员。

**调用关系：** 邀请入口 → requireActiveMember → CreateInvitation；通知投递由其他层/队列路径处理。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `inviter` | identity.User | 当前请求者 | 其 ID 用于成员资格查询和 InvitedBy。 |
| `email / role` | string / string | 规范化后的受邀邮箱/目标角色 | role 仅 admin/member，不允许邀请参数直接赋 owner。 |
| `member` | Member | 邀请者在该空间的成员记录 | member.Role 决定能否邀请，别与目标 role 混淆。 |
| `inviteID / invitation` | string / Invitation | 新 ID / pending 记录 | 受邀邮箱、目标角色、邀请者与到期点一并保存。 |
| `now` | time.Time | s.now().UTC() | ExpiresAt=now+14 天，链接存在不代表永不过期。 |

### 源码与逐行解释

```text
 109 │ func (s *Service) Invite(ctx context.Context, inviter identity.User, workspaceID, email, role string) (Invitation, error) {
 110 │ 	email = normalizeEmail(email)
 111 │ 	if _, err := mail.ParseAddress(email); err != nil {
 112 │ 		return Invitation{}, fmt.Errorf("%w: invalid email", ErrValidation)
 113 │ 	}
 114 │ 	role = normalizeRole(role)
 115 │ 	if role != "admin" && role != "member" {
 116 │ 		return Invitation{}, fmt.Errorf("%w: invalid role", ErrValidation)
 117 │ 	}
 118 │ 	member, err := s.requireActiveMember(ctx, inviter.ID, workspaceID)
 119 │ 	if err != nil {
 120 │ 		return Invitation{}, err
 121 │ 	}
 122 │ 	if member.Role != "owner" && member.Role != "admin" {
 123 │ 		return Invitation{}, ErrForbidden
 124 │ 	}
 125 │ 	inviteID, err := id.New()
 126 │ 	if err != nil {
 127 │ 		return Invitation{}, err
 128 │ 	}
```

- **L109–L113**：规范化并验证受邀邮箱。
- **L114–L117**：只允许授予 admin/member，不通过邀请接口转移 owner。
- **L118–L121**：邀请人必须仍为活跃成员。
- **L122–L124**：普通 member 无邀请权限，owner/admin 才可继续。
- **L125–L128**：生成 Invitation ID。

```text
 129 │ 	now := s.now().UTC()
 130 │ 	invitation := Invitation{ID: inviteID, WorkspaceID: workspaceID, Email: email, Role: role, Status: "pending", InvitedBy: inviter.ID, CreatedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour)}
 131 │ 	if err = s.store.CreateInvitation(ctx, invitation); err != nil {
 132 │ 		return Invitation{}, err
 133 │ 	}
 134 │ 	return invitation, nil
 135 │ }
```

- **L129–L130**：创建 pending 邀请，保存邀请人和 14 天到期时间。
- **L131–L135**：邀请先入库。HTTP Handler 随后排邮件；邮件投递失败不会让这条邀请不存在。


## 接受邀请要绑定真实账号邮箱

源码：[internal/team/service.go:148](/Users/windcry1/Documents/ai_companion/internal/team/service.go:148)

<a id="symbols-9-3"></a>

### 函数与变量详解

函数/类型声明：[`AcceptInvitation`](/Users/windcry1/Documents/ai_companion/internal/team/service.go:148)。

**职责、输入输出与影响：** 输入当前 identity.User 与 invitationID，返回 (Member, Invitation, error)。确认邀请仍 pending、未过期、邮箱匹配，再由 Store 接受并创建/更新成员关系。

**调用关系：** 用户接受入口 → GetInvitation → Store.AcceptInvitation。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `user` | identity.User | 已认证账号 | 用其真实 Email 与邀请邮箱比较，不使用请求者随意填写的新邮箱。 |
| `invitationID / invitation` | string / Invitation | 链接/路径参数 / 查询结果 | ID 仅用于定位，Status/ExpiresAt/Email 才决定是否可接受。 |
| `now` | time.Time | 服务时钟 | 要求 now 严格早于到期时刻。 |
| `err` | error | 读取或接受失败 | 冲突与禁止是不同原因，不应都展示成“邀请不存在”。 |

### 源码与逐行解释

```text
 148 │ func (s *Service) AcceptInvitation(ctx context.Context, user identity.User, invitationID string) (Member, Invitation, error) {
 149 │ 	invitation, err := s.store.GetInvitation(ctx, invitationID)
 150 │ 	if err != nil {
 151 │ 		return Member{}, Invitation{}, err
 152 │ 	}
 153 │ 	now := s.now().UTC()
 154 │ 	if invitation.Status != "pending" || !now.Before(invitation.ExpiresAt) {
 155 │ 		return Member{}, Invitation{}, ErrConflict
 156 │ 	}
 157 │ 	if !strings.EqualFold(invitation.Email, user.Email) {
 158 │ 		return Member{}, Invitation{}, ErrForbidden
 159 │ 	}
 160 │ 	return s.store.AcceptInvitation(ctx, invitationID, user, now)
 161 │ }
```

- **L148–L152**：按 Invitation ID 读取邀请。
- **L153–L156**：只接受 pending 且尚未到期的邀请。
- **L157–L159**：受邀邮箱必须与当前登录用户匹配；知道邀请 ID 不等于有权加入。
- **L160–L161**：Store 在事务中再次处理接受、成员写入及状态变更。


## 邮件 Worker 处理投递结果

源码：[internal/email/service.go:173](/Users/windcry1/Documents/ai_companion/internal/email/service.go:173)

<a id="symbols-9-4"></a>

### 函数与变量详解

函数/类型声明：[`send`](/Users/windcry1/Documents/ai_companion/internal/email/service.go:173)。

**职责、输入输出与影响：** email.Service.send 输入 Delivery 与 workerID，返回 (已处理 bool,error)。调用 Sender 发信，再保存投递状态；发送失败分支返回 true,nil 以继续队列，不代表邮件成功。

**调用关系：** 邮件 Worker 领取后调用；sender.Send → FailDelivery 或 CompleteDelivery。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `item` | Delivery | 已领取的投递记录 | RecipientEmail/Subject/BodyText 变为外发 Message。 |
| `workerID` | string | 处理者身份 | 写状态时用于领取权约束，不是发件人邮箱。 |
| `providerID` | string | Sender 返回的投递标识 | 成功时存数据库；具体提供商可有不同格式，不是本地 Delivery.ID。 |
| `err` | error | 外部发送返回 | 非 nil 时分类保存失败；外发和数据库标 sent 之间仍有故障窗口。 |

### 源码与逐行解释

```text
 173 │ func (s *Service) send(ctx context.Context, item Delivery, workerID string) (bool, error) {
 174 │ 	providerID, err := s.sender.Send(ctx, Message{To: item.RecipientEmail, Subject: item.Subject, BodyText: item.BodyText})
 175 │ 	if err != nil {
 176 │ 		_ = s.store.FailDelivery(ctx, item, workerID, failureCode(err), s.now().UTC())
 177 │ 		return true, nil
 178 │ 	}
 179 │ 	return true, s.store.CompleteDelivery(ctx, item, workerID, providerID, s.now().UTC())
 180 │ }
```

- **L173–L174**：将持久化 Delivery 转为 Message，调用配置好的 SMTP 或 Noop Sender。
- **L175–L177**：发送失败写 FailDelivery，并返回 true,nil，表示本任务已处理，队列可以继续服务其他任务；不是声称邮件已成功送达。
- **L178–L180**：发送成功后把 providerID 与终态交给 Store，提交错误会返回。


SMTP 调用与数据库提交不是同一事务：若外部已投递、CompleteDelivery 写失败，恢复可能重复投递。不能仅凭 Outbox/租约声称邮件端到端 exactly-once。团队资源分享还需读 httpserver/workspace_documents.go、workspace_skill_files.go、workspace_ledger_exports.go：验证成员后建立显式分享关系，聊天/个人记忆不会自动共享。

## 挑战只能原子消费一次

源码：[internal/adminpasskey/service.go:212](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:212)

<a id="symbols-9-5"></a>

### 函数与变量详解

函数/类型声明：[`takeCeremony`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:212)。

**职责、输入输出与影响：** 输入 kind 与 secret，返回 (Record, ceremony, error)。Store.Take 原子消费一次认证挑战，检查期限并解码；读取即消费，不是普通可反复 Get。

**调用关系：** Passkey 完成注册/登录等入口调用；Key 派生存储键 → Store.Take → JSON 解码。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `kind / secret` | string / string | 仪式种类与一次性秘密 | Key(kind, secret) 定位记录；secret 不应公开或写日志。 |
| `r` | Record | Store.Take 返回 | Expires 是记录绝对期限，Data 是保存的认证过程 JSON。 |
| `c` | ceremony | r.Data 解码 | 承载 WebAuthn session/challenge 与 ExpectedOwner 等，供完成验证。 |
| `err` | error | 消费失败 | 失败、过期、解码不合法均对外 ErrDenied，避免继续验证半数据。 |

### 源码与逐行解释

```text
 212 │ func (s *Service) takeCeremony(ctx context.Context, kind, secret string) (Record, ceremony, error) {
 213 │ 	r, err := s.Store.Take(ctx, Key(kind, secret))
 214 │ 	if err != nil || !r.Expires.After(s.now()) {
 215 │ 		return Record{}, ceremony{}, ErrDenied
 216 │ 	}
 217 │ 	var c ceremony
 218 │ 	if json.Unmarshal(r.Data, &c) != nil {
 219 │ 		return Record{}, ceremony{}, ErrDenied
 220 │ 	}
 221 │ 	return r, c, nil
 222 │ }
```

- **L212–L213**：takeCeremony 按 kind+secret 派生键，Store.Take 读取并消费挑战。
- **L214–L216**：取不到或过期拒绝；已消费的挑战不能重复登录。
- **L217–L220**：还原序列化的 ceremony，结构无效拒绝。
- **L221–L222**：返回记录与 WebAuthn 会话数据供签名验证。


## Passkey 开始与完成登录

源码：[internal/adminpasskey/service.go:251](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:251)

<a id="symbols-9-6"></a>

### 函数与变量详解

函数/类型声明：[`BeginLogin`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:251)、[`FinishLogin`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:260)。

**职责、输入输出与影响：** BeginLogin 输入 expectedOwner，返回 (浏览器 options any, ceremony secret string, error)；FinishLogin 输入 secret 与 *http.Request，返回 (管理会话令牌 string,error)。会保存/消费挑战、验证设备签名、更新凭证并建 Session。

**调用关系：** 管理登录前后两次请求；BeginDiscoverableLogin/saveCeremony → takeCeremony → FinishDiscoverableLogin → newSession。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `expectedOwner` | string | 可选预期管理员身份 | 非空时要求完成登录的账号一致，不是浏览器随便选的显示名。 |
| `options / data` | 浏览器认证参数 / 库会话数据；Finish 中 data 为 ceremony | WebAuthn 开始结果 / takeCeremony | 同名 data 在两个函数类型不同，必须按当前签名读。 |
| `secret` | string | saveCeremony 返回 | 连接前后两步的一次性凭证，不等于最终管理 Session Token。 |
| `owner / stored / saved` | 内部 user / Record / credentialRecord | 验证 handler 逐步填充 | owner 是管理员主体，stored 是存储包装，saved 是里面解出的凭证内容。 |
| `handler` | func([]byte, []byte) (webauthn.User, error) | 闭包传入库 | 根据 rawID 找凭证并检查 handle、RPID、ExpectedOwner；返回可供库验签的用户。 |
| `rawID / handle` | []byte / []byte | WebAuthn 库回调参数 | 前者标识凭证，后者标识凭证关联用户，不是明文密码。 |
| `c` | *webauthn.Credential | FinishDiscoverableLogin | 需 UserVerified 且无 CloneWarning，再更新 saved.Credential。 |
| `request` | *http.Request | 浏览器完成响应 | 含断言数据交协议库验证，不能只读一个“登录成功”字段。 |

### 源码与逐行解释

```text
 251 │ func (s *Service) BeginLogin(ctx context.Context, expectedOwner string) (any, string, error) {
 252 │ 	options, data, err := s.WebAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
 253 │ 	if err != nil {
 254 │ 		return nil, "", err
 255 │ 	}
 256 │ 	secret, err := s.saveCeremony(ctx, "login", expectedOwner, ceremony{Data: *data, ExpectedOwner: expectedOwner})
 257 │ 	return options, secret, err
 258 │ }
 259 │
 260 │ func (s *Service) FinishLogin(ctx context.Context, secret string, request *http.Request) (string, error) {
 261 │ 	_, data, err := s.takeCeremony(ctx, "login", secret)
 262 │ 	if err != nil {
 263 │ 		return "", err
 264 │ 	}
 265 │ 	var owner user
 266 │ 	var stored Record
 267 │ 	var saved credentialRecord
 268 │ 	handler := func(rawID, handle []byte) (webauthn.User, error) {
 269 │ 		var err error
```

- **L251–L255**：创建 discoverable login，并要求设备侧用户验证；生成 options 和服务端 challenge/session 数据。
- **L256–L258**：把 ceremony 短期持久化并返回浏览器所需参数及关联 secret。
- **L259–L264**：FinishLogin 首先消费对应挑战，失败不继续验签。
- **L265–L269**：准备存储凭据与账号，handler 供 WebAuthn 库按 Credential ID 定位真实用户。

```text
 270 │ 		stored, err = s.Store.Get(ctx, Key("credential", string(rawID)))
 271 │ 		if err != nil {
 272 │ 			return nil, ErrDenied
 273 │ 		}
 274 │ 		if json.Unmarshal(stored.Data, &saved) != nil || saved.RPID != s.WebAuthn.Config.RPID {
 275 │ 			return nil, ErrDenied
 276 │ 		}
 277 │ 		owner, err = s.loadUser(ctx, stored.Owner)
 278 │ 		if err != nil {
 279 │ 			return nil, ErrDenied
 280 │ 		}
 281 │ 		if !bytes.Equal(handle, owner.handle) || (data.ExpectedOwner != "" && data.ExpectedOwner != owner.account.ID) {
 282 │ 			return nil, ErrDenied
 283 │ 		}
 284 │ 		return owner, nil
 285 │ 	}
 286 │ 	c, err := s.WebAuthn.FinishDiscoverableLogin(handler, data.Data, request)
 287 │ 	if err != nil || !c.Flags.UserVerified || c.Authenticator.CloneWarning {
 288 │ 		return "", ErrDenied
 289 │ 	}
```

- **L270–L273**：查凭据记录，未知凭据拒绝。
- **L274–L276**：解析凭据并核对 RP ID，防止把另一站点的凭据拿来登录。
- **L277–L280**：重新加载凭据拥有者与其有效账号信息。
- **L281–L285**：验证 User Handle；如果本次明确要求某个账号，还要核对 ExpectedOwner。
- **L286**：由 WebAuthn 库校验 challenge、来源、RP 与签名等协议要求。
- **L287–L289**：还要求 UserVerified 且没有 CloneWarning。

```text
 290 │ 	saved.Credential = *c
 291 │ 	stored.Data = marshal(saved)
 292 │ 	if err = s.Store.Update(ctx, stored); err != nil {
 293 │ 		return "", err
 294 │ 	}
 295 │ 	return s.newSession(ctx, owner.account, stored.Key)
 296 │ }
```

- **L290–L294**：保存最新认证器凭据状态；保存失败不发 Session。
- **L295–L296**：最后签发项目自己的管理 Session，Passkey 私钥不进入服务端。


## 管理员 Session 逐次校验撤销与闲置

源码：[internal/adminpasskey/service.go:317](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:317)

<a id="symbols-9-7"></a>

### 函数与变量详解

函数/类型声明：[`Authenticate`](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service.go:317)。

**职责、输入输出与影响：** adminpasskey.Service.Authenticate 输入管理 token，返回 (opsauth.Account, adminpasskey.Session, error)。查会话、账号版本、凭证与期限，必要时写 LastSeen；与普通 identity.Authenticate 不是同一函数。

**调用关系：** 管理接口认证使用；Store.Get session → Accounts.GetOperator → Get credential → 可选 Store.Update，冲突时重读认证。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `token / r` | string / Record | 管理会话凭证 / Key(session, token) 查出的记录 | r.Expires 是绝对期限，不只看 session.LastSeen。 |
| `session` | adminpasskey.Session | JSON 解码 | Owner、AccountVersion、CredentialKey、LastSeen 绑定账号和凭证状态。 |
| `now` | time.Time | s.now() | 检查绝对/闲置期限；距 LastSeen 至少一分钟才尝试刷新。 |
| `a` | opsauth.Account | GetOperator(session.Owner) | 须 active 且 accountVersion 与会话捕获值一致。 |
| `c / cred` | Record / credentialRecord | 凭证存储包装 / 内容 | 验证归属和 RPID，凭证删除后旧会话不能继续放行。 |
| `err` | error | 读取或刷新结果 | 更新冲突再 Authenticate，不能借此重建已撤销会话。 |

### 源码与逐行解释

```text
 317 │ func (s *Service) Authenticate(ctx context.Context, token string) (opsauth.Account, Session, error) {
 318 │ 	r, err := s.Store.Get(ctx, Key("session", token))
 319 │ 	if err != nil {
 320 │ 		return opsauth.Account{}, Session{}, ErrDenied
 321 │ 	}
 322 │ 	var session Session
 323 │ 	if json.Unmarshal(r.Data, &session) != nil {
 324 │ 		return opsauth.Account{}, Session{}, ErrDenied
 325 │ 	}
 326 │ 	now := s.now()
 327 │ 	if !r.Expires.After(now) || !session.LastSeen.Add(IdleTTL).After(now) {
 328 │ 		return opsauth.Account{}, Session{}, ErrDenied
 329 │ 	}
 330 │ 	a, err := s.Accounts.GetOperator(ctx, session.Owner)
 331 │ 	if err != nil || a.Status != "active" || accountVersion(a) != session.AccountVersion {
 332 │ 		return opsauth.Account{}, Session{}, ErrDenied
 333 │ 	}
```

- **L317–L321**：通过 Token 派生键查管理 Session，失效或不存在拒绝。
- **L322–L325**：反序列化 Session。
- **L326–L329**：同时检查绝对到期和 IdleTTL 闲置到期。
- **L330–L333**：重查管理员，要求 active 且账号版本与会话版本一致；改版/撤销可使旧 Session 失效。

```text
 334 │ 	c, err := s.Store.Get(ctx, session.CredentialKey)
 335 │ 	if err != nil || c.Owner != a.ID {
 336 │ 		return opsauth.Account{}, Session{}, ErrDenied
 337 │ 	}
 338 │ 	var cred credentialRecord
 339 │ 	if json.Unmarshal(c.Data, &cred) != nil || cred.RPID != s.WebAuthn.Config.RPID {
 340 │ 		return opsauth.Account{}, Session{}, ErrDenied
 341 │ 	}
 342 │ 	if now.Sub(session.LastSeen) >= time.Minute {
 343 │ 		session.LastSeen = now
 344 │ 		r.Data = marshal(session)
 345 │ 		if err = s.Store.Update(ctx, r); err != nil {
 346 │ 			// Another dashboard request may have touched this session concurrently.
 347 │ 			// Re-read to distinguish that from revocation; never recreate a session.
 348 │ 			if errors.Is(err, ErrConflict) {
 349 │ 				return s.Authenticate(ctx, token)
 350 │ 			}
```

- **L334–L337**：检查创建该 Session 的凭据仍存在且归属于这个管理员。
- **L338–L341**：再次核对凭据 RP ID。
- **L342–L345**：LastSeen 至少相隔 1 分钟才更新，减少每个 dashboard 请求都写库。
- **L346–L350**：并发更新冲突时重读认证，不会把已删除 Session 重新创建回来。

```text
 351 │ 			return opsauth.Account{}, Session{}, ErrDenied
 352 │ 		}
 353 │ 	}
 354 │ 	return a, session, nil
 355 │ }
 356 │ func (s *Service) Fresh(session Session) bool {
 357 │ 	return session.AuthenticatedAt.Add(FreshTTL).After(s.now())
 358 │ }
```

- **L351–L355**：其他写入错误拒绝；成功返回账号与 Session。
- **L356–L358**：Fresh 单独判断最近验证时间；管理写操作可要求重新 Passkey 验证，而不只是普通登录有效。


HTTP 层 Cookie、安全属性、同源校验及 Fresh 要求继续读 [internal/httpserver/admin_passkey.go](/Users/windcry1/Documents/ai_companion/internal/httpserver/admin_passkey.go) 的 setAdminCookie、adminOriginOK、authenticateAdminSession。

## 额度的 null、0、-1 是不同意思

源码：[internal/billing/quota.go:13](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:13)

<a id="symbols-9-8"></a>

### 函数与变量详解

函数/类型声明：[`QuotaLimits`](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:14)。

**职责、输入输出与影响：** QuotaLimits 是结构体类型，不是函数，无调用和返回值。每字段用 *int 表达 nil 继承、0 禁止、-1 不限；实际资源上限再由覆盖流程计算。

**调用关系：** QuotaPolicy.Limits 保存某一层覆盖值；values/apply 按相同字段顺序访问。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `Documents` | *int | 额度政策字段 | 文档数量上限，nil 不覆盖下一层。 |
| `SkillRunsPerMonth` | *int | 额度政策字段 | 每月 Skill 次数，不是当前同时运行数。 |
| `Workspaces` | *int | 额度政策字段 | 空间数量限制。 |
| `AgentRunsPerMonth` | *int | 额度政策字段 | 每月 Agent Run 次数，与 Skill 次数各算各的。 |
| `ModelCostMicrosMonthly` | *int | 额度政策字段 | 模型月度微单位成本上限，不能当作 Token 数。 |

### 源码与逐行解释

```text
  13 │ // Null inherits the next layer. Zero blocks new usage; -1 is unlimited.
  14 │ type QuotaLimits struct {
  15 │ 	Documents              *int `json:"documents"`
  16 │ 	SkillRunsPerMonth      *int `json:"skill_runs_per_month"`
  17 │ 	Workspaces             *int `json:"workspaces"`
  18 │ 	AgentRunsPerMonth      *int `json:"agent_runs_per_month"`
  19 │ 	ModelCostMicrosMonthly *int `json:"model_cost_micros_monthly"`
  20 │ }
```

- **L13–L20**：每个上限用 *int：nil 表示继承下一层，0 禁止新增，-1 不限额；普通 int 无法表达这三种不同语义。


## 管理员设置一层额度

源码：[internal/billing/quota.go:51](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:51)

<a id="symbols-9-9"></a>

### 函数与变量详解

函数/类型声明：[`SetQuotaPolicy`](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:51)。

**职责、输入输出与影响：** 输入整层 QuotaPolicy 与 expected int64；返回 (新政策,error)。规范化审计信息、验证整数范围，再以预期版本替换一整层政策；不是只修改非空字段的 PATCH。

**调用关系：** 管理员额度设置调用；limits.values 验每字段 → QuotaStore.SaveQuotaPolicy(ctx, policy, expected)。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `policy` | QuotaPolicy 值 | 本次整层输入 | UserID 区分全局/用户层；Actor/Reason 是审计必填。 |
| `expected` | int64 | 调用方读取时的 Revision | 保存用旧版本，policy.Revision=expected+1，防并发覆盖。 |
| `value` | *int | 逐个遍历 Limits.values() | nil 跳过；非 nil 值必须至少 -1 且不超安全整数界限。 |
| `store / ok` | QuotaStore / bool | 接口能力断言 | 不支持政策存储则返回 NotFound，不静默使用内存替代。 |
| `now` | time.Time | s.now().UTC() | 地址赋给 UpdatedAt，表示本次政策变更时刻。 |

### 源码与逐行解释

```text
  51 │ func (s *Service) SetQuotaPolicy(ctx context.Context, policy QuotaPolicy, expected int64) (QuotaPolicy, error) {
  52 │ 	policy.UserID = strings.TrimSpace(policy.UserID)
  53 │ 	policy.Actor = strings.TrimSpace(policy.Actor)
  54 │ 	policy.Reason = strings.TrimSpace(policy.Reason)
  55 │ 	if expected < 0 || expected >= 9_007_199_254_740_991 || len(policy.UserID) > 64 || policy.Actor == "" || len(policy.Actor) > 128 || policy.Reason == "" || len(policy.Reason) > 512 {
  56 │ 		return QuotaPolicy{}, ErrValidation
  57 │ 	}
  58 │ 	for _, value := range policy.Limits.values() {
  59 │ 		if value != nil && (*value < -1 || *value > 9_007_199_254_740_991) {
  60 │ 			return QuotaPolicy{}, fmt.Errorf("%w: 额度须为 -1 或不小于 0 的安全整数", ErrValidation)
  61 │ 		}
  62 │ 	}
  63 │ 	store, ok := s.store.(QuotaStore)
  64 │ 	if !ok {
  65 │ 		return QuotaPolicy{}, ErrNotFound
  66 │ 	}
  67 │ 	now := s.now().UTC()
  68 │ 	policy.UpdatedAt = &now
  69 │ 	policy.Revision = expected + 1
  70 │ 	return store.SaveQuotaPolicy(ctx, policy, expected)
```

- **L51–L54**：替换整层政策，清理 UserID、Actor 和修改原因。
- **L55–L57**：expected revision、ID、操作者和原因必须合法；上界兼容 JavaScript 安全整数。
- **L58–L62**：逐项允许 nil、-1 或非负安全整数，拒绝更小的负值。
- **L63–L66**：要求底层 Store 实现 QuotaStore。
- **L67–L70**：记录时间与 expected+1，将旧 expected 交给 SQL 条件写入；人工理由/审计在 Store 事务中保存。

```text
  71 │ }
```

- **L71**：函数结束。


## 政策逐字段覆盖

源码：[internal/billing/quota.go:77](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:77)

<a id="symbols-9-10"></a>

### 函数与变量详解

函数/类型声明：[`apply`](/Users/windcry1/Documents/ai_companion/internal/billing/quota.go:77)。

**职责、输入输出与影响：** QuotaLimits.apply(base Limits) 返回新的 Limits 值。对非 nil 覆盖字段赋值，只改本地 base；不读写数据库，也不直接统计用量。

**调用关系：** 额度汇总按层调用 apply；values() 的顺序必须与 targets 对齐，后应用的非空值覆盖先前值。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `limits` | QuotaLimits | 方法接收者 | 本层可空覆盖字段，区别于已计算的非可空 base。 |
| `base` | Limits 值参数 | 下层已计算额度 | 值传入后在此副本上覆盖，最后返回。 |
| `targets` | []*int | base 各字段地址列表 | 让循环能改对应字段，不是额度值的副本列表。 |
| `i / value` | int / *int | 下标 / 本层字段指针 | value 非 nil 时 *targets[i]=*value，0 与 -1 也必须覆盖。 |

### 源码与逐行解释

```text
  77 │ func (limits QuotaLimits) apply(base Limits) Limits {
  78 │ 	targets := []*int{&base.Documents, &base.SkillRunsPerMonth, &base.Workspaces, &base.AgentRunsPerMonth, &base.ModelCostMicrosMonthly}
  79 │ 	for i, value := range limits.values() {
  80 │ 		if value != nil {
  81 │ 			*targets[i] = *value
  82 │ 		}
  83 │ 	}
  84 │ 	return base
  85 │ }
```

- **L77–L78**：base 是按值传入的 Limits；targets 保存其各字段地址。
- **L79–L83**：与 values() 的字段顺序一一对应，只覆盖非 nil 值；0 和 -1 都会真实覆盖。
- **L84–L85**：返回合成上限，调用者的原套餐对象不因这个值拷贝被改写。


billing.Service.Summary 的 L186 是核心：policies.User.Limits.apply(policies.Global.Limits.apply(limits))。先套餐，再全局，再用户；并记录每个最终值来自哪一层。

## 业务接收前检查实际用量

源码：[internal/billing/service.go:302](/Users/windcry1/Documents/ai_companion/internal/billing/service.go:302)

<a id="symbols-9-11"></a>

### 函数与变量详解

函数/类型声明：[`Check`](/Users/windcry1/Documents/ai_companion/internal/billing/service.go:302)。

**职责、输入输出与影响：** 输入 userID 与 resource 字符串，返回 error；nil 表示当前预检查通过，不返回剩余额度且不原子预留未来消耗。

**调用关系：** 业务接受前调用 → Summary 计算计划/用量/有效上限 → 对应 UsageItem 比较。

| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |
|---|---|---|---|
| `summary` | billing.Summary | s.Summary(ctx,userID) | 含 Plan 和 Usage；不是单个数字。 |
| `resource` | string | 参数去空白转小写 | 选择要检查哪类消耗，不认识则 Validation。 |
| `item` | UsageItem | 遍历 summary.Usage | Resource 匹配后比较 Used 与 Limit；Limit<0 表示不限。 |
| `err` | error | Summary 失败或额度错误 | QuotaError 保存 Resource/PlanCode/Used/Limit，便于上层转 HTTP 提示。 |

### 源码与逐行解释

```text
 302 │ func (s *Service) Check(ctx context.Context, userID, resource string) error {
 303 │ 	summary, err := s.Summary(ctx, userID)
 304 │ 	if err != nil {
 305 │ 		return err
 306 │ 	}
 307 │ 	resource = strings.ToLower(strings.TrimSpace(resource))
 308 │ 	for _, item := range summary.Usage {
 309 │ 		if item.Resource != resource {
 310 │ 			continue
 311 │ 		}
 312 │ 		if item.Limit >= 0 && item.Used >= item.Limit {
 313 │ 			return QuotaError{Resource: resource, PlanCode: summary.Plan.Code, Used: item.Used, Limit: item.Limit}
 314 │ 		}
 315 │ 		return nil
 316 │ 	}
 317 │ 	return fmt.Errorf("%w: unknown resource %s", ErrValidation, resource)
 318 │ }
```

- **L302–L306**：重新生成当前用户 Summary，失败不默认放行。
- **L307**：规范化资源名。
- **L308–L311**：只找对应资源，不把所有资源共用一个额度。
- **L312–L314**：上限非负且 Used>=Limit 时返回带资源/套餐/实际值的 QuotaError；-1 跳过此判断。
- **L315–L318**：资源存在且未超限返回 nil，未知资源返回校验错误。


这里是已累计用量检查，不是对每次并发模型调用做成本预扣。月度总额度、单 Run 调用/Token/费用预算、账后用量纠正是三个不同层面。

## 把代码连成一次真实行为

### 邀请与邮件不要读成同一个事务

Owner 创建邀请 I1 给 a@example.com。邮件队列投递成功只是通知送出；接受接口还要核验 I1 状态、期限与当前账号邮箱，才建立成员关系。拿到 I1 的其他账号不能只凭链接绕过邮箱绑定。

邮件 Sender 调用成功后再标记数据库 sent。若外部邮件已发出但 sent 写入失败，重试有重复投递窗口。反过来，Worker 返回 true,nil 也可能只是“本轮处理并保存了投递失败”，不要把这个 bool 当成邮件送达。

### 用数字算一次三层限额

以下以某一个字段为例，实际按每个字段分别覆盖：

| 套餐基值 | 全局覆盖 | 用户覆盖 | 最终含义 |
|---|---|---|---|
| 100 | null | null | 100 |
| 100 | 60 | null | 60 |
| 100 | 60 | 12 | 12 |
| 100 | 60 | 0 | 禁止新增该类消耗 |
| 100 | 60 | -1 | 该字段不限额 |

覆盖不是取三层最小值；0 不是继承，-1 不是“负数配额”。代码用 *int 区分“这一层没填”和“明确填零”。判断通常是 limit>=0 且 used>=limit 时拒绝。

假设 used=9、limit=10，两个并发请求都在实际消费前检查到 9，它们可能都通过。当前接受前检查不是原子费用预留，不能只凭这个 if 声称严格避免所有并发超额。

### 挑战为什么只消费一次

挑战是为一次认证过程生成的新鲜随机内容；同一签名结果反复重放不应该建立无限会话。Store.Take 读取并消费 ceremony 是这一保护的应用层组成部分。后续 Authenticate 还检查绝对期限、空闲期限、账号版本、凭证是否仍存在；“设备曾经验过签”不是永远有效。

### 自测与答案

问：用户覆盖 null 能取消全局 60 变成不限额吗？答：不能，null 是继承；明确的 -1 才表达本字段不限额。

问：邀请邮件发送失败，是否可以认为邀请记录一定不存在？答：不能，业务记录与异步投递有不同生命周期。

## 对照阅读与验证

验证入口：[internal/httpserver/teams_test.go](/Users/windcry1/Documents/ai_companion/internal/httpserver/teams_test.go)、[internal/adminpasskey/service_test.go](/Users/windcry1/Documents/ai_companion/internal/adminpasskey/service_test.go)、[internal/billing/quota_durable_test.go](/Users/windcry1/Documents/ai_companion/internal/billing/quota_durable_test.go)。当前 Web 管理员路径已有 Passkey；旧 operator API 的 Token/TOTP 兼容能力要按具体路由判断，不能把两种认证方式混为同一实现。

[返回目录](README.md) · [下一章：故障恢复、降级与配置发布](10-reliability-controlplane.md)
