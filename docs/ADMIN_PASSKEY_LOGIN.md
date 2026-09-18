# 管理员通行密钥登录

`/admin` 使用独立管理员账号和 Passkey（WebAuthn）。点击登录后在系统弹窗中选择设备 PIN；PIN 留在设备本地，服务器只验证公钥签名。网站强制用户验证，但不能指定系统必须显示 PIN。若当前平台不提供 PIN，可选择支持 PIN 的 FIDO2 安全密钥或其他设备。

## 部署与首次管理员

1. 为后台确定稳定域名。设置 API 的 `WEB_ORIGIN` 为完整来源，例如 `https://app.example.com`，不包含 `/admin`。该来源的 hostname 是 RP ID，注册后的凭证绑定该域名。开发使用 `http://localhost:3000`。普通 HTTP 域名和 IP 地址不支持。
2. 设置 Next.js 服务端 `ADMIN_API_BASE_URL` 为 API 地址，例如本机 `http://localhost:8080`。Compose 已设置为 `http://api:8080`。浏览器始终通过同源 `/api/admin/*` 请求，不依赖跨站 Cookie。
3. 在部署新版 API 前执行迁移：PostgreSQL `000042_admin_passkeys`，MySQL `000031_admin_passkeys`。需要 PostgreSQL 或 MySQL；纯内存开发模式不提供可跨进程初始化的管理员账号。
4. 在可信终端中使用与 API 相同的 `DATABASE_DRIVER`、数据库 DSN 和 `WEB_ORIGIN`：

```sh
go run ./cmd/migrate
go run ./cmd/admin-invite --id owner --name '管理员' --reason '初始化后台管理员'
```

已构建的 API 容器内提供 `/admin-invite` 二进制，可以在该容器中执行相同参数。命令使用容器现有数据库配置；不需要关闭生产 MFA。命令不会自动加载 `.env`，运行前应通过部署工具或可信环境注入所需变量。

5. 打开命令输出的 `/admin#invite=...` 链接，点击「绑定通行密钥并登录」，在系统弹窗中完成设备 PIN 验证。

邀请是账号绑定凭据，30 分钟后失效，在开始绑定时即被一次性消费。链接使用 fragment，页面读取后从地址栏移除。取消、超时或首次绑定失败后需重新签发。已有账号尚未绑定时，重复初始化命令会撤销其旧邀请和待完成绑定。

## 日常管理

- 在 `/admin` 点击「使用通行密钥登录」。浏览器显示已绑定的管理员账号，完成设备验证即可登录。
- 刷新页面可恢复服务器会话。会话最长 8 小时，连续 30 分钟没有请求则失效。后台自动刷新也属于请求；离开共享设备时应主动退出。
- 管理写入要求最近 5 分钟内完成 Passkey 验证，过期后自动重新验证同一账号，再执行原请求。
- 侧栏「账号与通行密钥」可添加备用设备/安全密钥或退出该账号所有会话。每个账号最多绑定 10 个凭证。
- admin 可以创建成员邀请并指定 `viewer`、`support` 或 `admin`。邀请尚未完成时，用相同 ID、名称、角色可重新签发；旧邀请失效。已绑定账号不能通过这个入口重新接管。
- 普通退出只撤销当前会话。退出所有会话同时旋转该账号的 Operator Token，使并发旧会话失效；已绑定 Passkey 仍可登录。
- 账号禁用、Token 重置、TOTP 重置递增账号会话版本。重新启用账号不会复活旧会话。权限由 API 在每个请求中检查。

## 丢失设备后的恢复

优先用预先绑定的备用凭证登录。全部凭证丢失时，部署管理员核验身份后，在可信终端执行：

```sh
go run ./cmd/admin-invite --id owner --recover --reason '核验身份后恢复丢失设备的管理员账号'
```

恢复会撤销该账号全部 Passkey、旧 Token、会话、邀请与未完成绑定，然后返回新邀请。禁用账号需要先由有权限的管理员启用。不要把邀请链接写入公共工单或日志。

## 安全与持久化

- 生产 Cookie 使用 `__Host-` 前缀、`Path=/`、`HttpOnly`、`Secure`、`SameSite=Strict`；localhost 开发使用不带前缀的 HttpOnly Cookie。
- 注册与登录验证 challenge、来源、RP ID、公钥签名和 UV 标志。挑战原子消费；支持计数器的验证器出现回退时拒绝登录。
- 登录和绑定请求受持久化速率限制：每个直接连接来源每分钟最多 120 次开始请求。反向代理部署下多个用户共享该额度；外围网关可另行配置可信客户端 IP 限流。
- 会话令牌、邀请令牌和验证流程 Cookie 的随机标识仅以 SHA-256 哈希键存储；公开的 WebAuthn challenge 随验证上下文保存。公钥凭证按 RP ID 区分。数据库不存储设备 PIN 或私钥。
- 账号授权操作记录在既有 operator 审计中；Passkey 邀请、绑定、会话创建和退出事件记录在 `operator_passkey_records` 的 `audit` 类型中，保留 90 天。开始验证时清理过期临时记录和过期审计。
- CLI/自动化继续使用现有 Bearer + TOTP API；网页登录不接收长期管理密钥。生产保持 `OPERATOR_MFA_REQUIRED=true`。

## 验证

协议测试使用软件 ES256 验证器生成真实注册数据和签名，覆盖错误来源、RP ID、UV、签名、计数器、挑战重放、邀请过期、会话撤销、并发消费、CSRF 和权限隔离。

```sh
go test -race ./internal/adminpasskey ./internal/opsauth ./internal/httpserver
cd web
pnpm check
pnpm test
```

在应用全部迁移的**专用可丢弃数据库**中设置 `ADMIN_PASSKEY_POSTGRES_TEST_DSN` / `ADMIN_PASSKEY_MYSQL_TEST_DSN`，再运行 `go test ./internal/adminpasskey -run TestDurablePasskeys -v`，可验证重启后登录、SQL 原子消费和最后一个 Passkey 管理员保护。该测试会创建账号，不应连接生产数据库。
