# 安全模型

HomeCTL Agent 通常以 root 运行。开启 Terminal、命令执行或文件浏览后，能够登录控制台的管理员拥有相应主机的高权限管理能力，因此公网部署首先要保护 Server 账号、Session、TLS 入口和 SQLite 数据。

## 管理员密码

新密码使用 Argon2id，SQLite `password_hash` 使用 `TEXT` 保存标准 PHC 字符串。当前参数：

```text
memory      19 MiB
iterations  2
parallelism 1
salt        16 random bytes
hash        32 bytes
```

PHC 字符串包含算法参数，因此未来提高密码哈希成本时无需修改数据库字段格式。

密码策略：

- 新密码至少 16 个 Unicode 字符
- 不强制“必须包含大写、小写、数字或特殊字符”之类组合规则
- 输入按 NFC 规范化
- 支持长 passphrase

新建管理员或主动修改密码时始终执行当前密码策略。

Server 使用可配置的 Argon2id 并发槽位限制，避免大量同时到达的登录请求把小内存设备拖入高内存压力；密码失败计数按客户端来源隔离。

## 首次管理员 bootstrap

`admin_username` 和 `admin_password` 只在 SQLite 中没有管理员时使用。数据库一旦已有管理员，启动过程不会再使用配置文件中的 bootstrap 密码进行认证或覆盖账号。

因此首次初始化完成后可以把配置中的：

```json
"admin_password": ""
```

保留数据库备份即可恢复管理员数据。

## Session

正式浏览器 Session：

- 原始随机 Token 只存在客户端的 `HttpOnly` Cookie
- SQLite 只保存 Token 的 SHA-256 摘要、创建时间和过期时间
- HTTPS 模式使用 `Secure`、`SameSite=Strict` 和 `__Host-` Cookie 前缀
- 未勾选“保持登录”时 Cookie 为浏览器 Session Cookie
- 勾选后使用 `remember_session_ttl` 的持久 Cookie
- 修改密码、开启 TOTP、关闭 TOTP会在一个数据库事务中吊销现有 Session
- 已打开的 Web Terminal 与创建它的正式 Session 绑定，Session 退出、过期或被安全设置变更吊销后连接会关闭

`localStorage` 只保存非敏感偏好，例如记住的用户名和主题模式，不保存正式 Session Token 或 TOTP pre-auth Token。

## TOTP 两阶段登录

启用 TOTP 后：

```text
用户名 + 密码
      ↓
密码正确
      ↓
Server 内存创建短期一次性 pre-auth
      ↓
浏览器显示 6 位验证码步骤
      ↓
浏览器只提交验证码
      ↓
验证成功后销毁 pre-auth 并创建正式 Session
```

pre-auth 的随机凭据保存在一个短期 `HttpOnly`、`SameSite=Strict` Cookie 中；JavaScript 不读取该凭据，Server 只在内存中保存对应状态，不写 SQLite。

保护措施包括：

- 默认 5 分钟过期
- 单个 pre-auth 的验证码尝试上限
- 已通过密码后的 TOTP 阶段还有独立失败窗口和临时锁定
- TOTP 锁定时未完成的 pre-auth 全部作废
- 成功验证码 time-step 在 SQLite 中原子记录，避免同一时间窗口的登录验证码被重复使用

TOTP 使用 RFC 6238 常见兼容参数：30 秒、6 位、HMAC-SHA1，并允许前后各一个时间步的时钟偏差。

### 配置 TOTP

账户设置生成的待确认 TOTP Secret 只临时保存在 Server 内存，并绑定到当前正式 Session；浏览器拿到 Secret/otpauth URI 用于添加到认证器，但启用请求不会再把 Secret 回传给 Server。待确认状态默认 10 分钟后失效。

TOTP 最终共享 Secret 必须能被验证端取回，因此不能像密码一样只存不可逆哈希。启用完成后它会保存在 SQLite 中。应把数据库、数据库备份和 Server 主机权限按敏感凭据保护。

## 登录限流与真实客户端 IP

HomeCTL 默认使用 TCP 对端 IP 做密码失败隔离。位于可信反向代理后方时，可以通过：

```json
"client_ip_header": "CF-Connecting-IP",
"trusted_proxy_cidrs": ["127.0.0.1/32", "::1/128"]
```

恢复真实客户端 IP。

只有直接 TCP 来源属于 `trusted_proxy_cidrs` 时 Header 才会被信任。不要为了“让 IP 显示正确”把 `0.0.0.0/0` 或不受控代理网络加入信任列表，否则客户端可能伪造 Header 绕过按 IP 的密码限流。

## Agent 身份和 Enrollment

添加设备时，控制台生成一次性 Enrollment Token：

- Server 只保存 SHA-256 摘要
- 默认 30 分钟有效
- 成功绑定设备后立即消费
- 同时填写的“首次显示名称”只用于创建该设备记录，保留原始大小写；后续 heartbeat 不会覆盖已保存名称

Agent 在首次绑定前本地生成自己的高熵 Device Token 并先写入 `state.json`。Enrollment Token 只授权这个 Device ID 与 Device Token 的首次绑定；Server 在同一个 SQLite transaction 中完成“消费 Enrollment Token + 创建设备凭据”。

后续每台 Agent 都使用自己的长期 Device Token，Server 仍然只保存 SHA-256 摘要。删除设备会删除凭据并断开当前 Agent。

Agent `state.json` 包含原始长期凭据，应保持 `0600` 权限。

## 文件浏览器

文件浏览器默认关闭。

Agent 使用 Go `os.Root` 执行 root-relative 文件访问。浏览、下载、上传临时文件、新建目录、删除和重命名都在配置的 `file_browser_root` 下解析，避免普通 `..` 路径穿越、symlink 逃逸和“先检查路径、后打开文件”之间的竞态。

下载只允许 regular file，避免 Web 请求把 FIFO、socket 或设备节点当普通文件读取。

`os.Root` 的安全边界是路径解析，不把 Linux mount point 当成独立安全边界。若目标主机上的 root 已被其他攻击者控制，对方本来就可以修改 mount namespace、文件和进程；HomeCTL 不试图在 Agent root 已失陷后建立额外沙箱。

如果只需要管理数据目录，建议：

```json
"file_browser_root": "/srv/data"
```

而不是默认授权整个 `/`。

## 命令执行

单条命令输出从写入阶段就受 `max_command_output_bytes` 限制，超出部分不继续缓存在 Agent 内存中。命令超时会终止该命令的进程组，减少后台子进程在超时后继续存活的情况。

Terminal 是独立 PTY，会按照浏览器终端窗口的实际行列数运行。窗口拖动期间只在浏览器本地逐帧适配，稳定后才发送远端尺寸；Server 与 Agent 都会过滤非法或重复 Resize，避免不必要的连续 `SIGWINCH`。

xterm.js、FitAddon 和样式文件以固定版本随 Server 二进制本地嵌入，浏览器不会向第三方 CDN 请求 Terminal 代码。Web CSP 的脚本和样式来源因此只允许 `'self'` 与当前页面所需的内联代码。

Agent 对命令、PTY 和文件操作设置固定并发上限；PTY 输入使用有界队列。新 Server/Agent 还会协商上传和下载信用窗口，使慢磁盘、慢浏览器或大文件不会在内存中形成无界队列，也不会让文件写入长期阻塞 WebSocket 读取循环。协议能力按连接协商，滚动升级期间可与不支持信用窗口的旧端继续通信。

## TLS

公网环境应使用 HTTPS/WSS。Cloudflare Tunnel、Caddy 或 Nginx 可以负责 TLS 终止。

HTTPS 访问时：

```json
"cookie_secure": true
```

Agent 不应启用：

```json
"tls": {"insecure_skip_verify": true}
```

除非这是明确受控的自签名测试环境。

## SQLite 和备份

SQLite 包含：

- 管理员密码 PHC 哈希
- TOTP Secret（启用时）
- Device Token 摘要
- Session Token 摘要
- Enrollment Token 摘要
- 设备状态信息

Server 会尝试把数据库权限设置为 `0600`。数据库文件、WAL/SHM 和备份都应按敏感数据处理。
