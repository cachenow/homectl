# 配置参数

HomeCTL Server 和 Agent 都使用 JSON 配置。配置解析会拒绝未知字段，因此参数拼写错误会在启动时直接报错，而不是被静默忽略。

时间值使用 Go duration 格式，例如 `500ms`、`10s`、`5m`、`24h`。

## Server `config.json`

| 参数 | 默认值 | 说明 |
|---|---:|---|
| `listen_addr` | `:8080` | HTTP 监听地址。仅供本机反向代理时建议 `127.0.0.1:8080`。 |
| `database_path` | `/data/homectl.db` | SQLite 数据库路径；相对路径按配置文件目录解析。 |
| `legacy_device_store` | `""` | 可选 JSON 设备库导入路径；仅数据迁移场景使用，普通部署保持为空。 |
| `admin_username` | `admin` | 只在数据库尚未创建管理员时用于首次初始化。 |
| `admin_password` | 无安全默认值 | 只在数据库尚无管理员时使用。首次密码至少 16 个 Unicode 字符。管理员已存在后可以设为空字符串。 |
| `cookie_secure` | `true` | HTTPS/WSS 部署应保持 `true`；仅直接使用 HTTP 时才设为 `false`。 |
| `session_ttl` | `24h` | 普通登录 Session 的 Server 端有效期。 |
| `remember_session_ttl` | `168h` | 勾选“保持登录”后的持久 Session 有效期，必须不短于 `session_ttl`。 |
| `preauth_ttl` | `5m` | 密码验证通过后等待 TOTP 第二步的临时认证状态有效期，允许范围 30 秒–15 分钟。 |
| `preauth_max_attempts` | `5` | 单个 TOTP pre-auth 最多错误次数，范围 3–20。 |
| `password_max_failures` | `10` | 同一客户端在一个失败窗口内允许的密码失败次数，范围 3–100。 |
| `password_failure_window` | `15m` | 密码失败计数窗口。 |
| `password_lockout_duration` | `1m` | 同一客户端达到密码失败上限后的临时等待时间。 |
| `password_hash_concurrency` | `1` | 同时允许执行的 Argon2id 密码验证数量，范围 1–16。小内存设备建议保持 1。 |
| `password_hash_queue_timeout` | `5s` | Argon2id 验证槽位繁忙时，登录请求最多等待多久。 |
| `totp_max_failures` | `10` | 已通过正确密码后的 TOTP 连续失败上限，范围 3–100。 |
| `totp_failure_window` | `15m` | TOTP 失败计数窗口。 |
| `totp_lockout_duration` | `1m` | TOTP 达到失败上限后的临时等待时间。 |
| `totp_setup_ttl` | `10m` | 在“账户设置”生成 TOTP Secret 后完成确认的时间窗口，允许范围 1–30 分钟。 |
| `client_ip_header` | `""` | 仅在 HomeCTL 位于可信反向代理后方时，用于读取真实客户端 IP 的单值 Header 名；Cloudflare Tunnel 可用 `CF-Connecting-IP`。 |
| `trusted_proxy_cidrs` | `127.0.0.1/32`, `::1/128` | 只有请求的直接来源地址属于这些 CIDR 时，`client_ip_header` 才会被信任。不要把公网或不受控网络段加入这里。 |
| `allow_exec` | `true` | 是否允许控制台发送单条命令。Agent 端还必须同时允许。 |
| `allow_terminal` | `true` | 是否允许 Web Terminal。Agent 端还必须同时允许。 |
| `file_browser_enabled` | `false` | Server 是否开放文件浏览器 API/UI。Agent 端也必须开启。 |
| `agent_offline_timeout` | `25s` | 距离最后心跳超过该时间后判定设备离线。应明显大于 Agent 心跳周期。 |
| `agent_handshake_timeout` | `15s` | Agent WebSocket 建连后的 hello 握手超时。 |
| `agent_write_timeout` | `10s` | Server 向 Agent 写 WebSocket 消息的超时。 |
| `action_timeout` | `8s` | 重启/关机请求等待 Agent 接受的超时。 |
| `exec_response_timeout` | `40s` | Server 等待单条命令返回的最大时间；应大于 Agent 的 `command_timeout`。 |
| `file_transfer_timeout` | `2m` | 文件操作和单次传输请求的超时。 |
| `enrollment_token_ttl` | `30m` | Web 生成的一次性 Enrollment Token 有效期。 |
| `web_refresh_interval` | `5s` | 设备列表状态刷新间隔。 |
| `ui_result_ttl` | `20s` | 命令/操作反馈自动隐藏时间；`0s` 表示只手动关闭。 |
| `http_read_header_timeout` | `10s` | HTTP Header 读取超时。 |
| `shutdown_timeout` | `10s` | 收到停止信号后的 Server 优雅退出等待时间。 |
| `file_transfer_chunk_bytes` | `65536` | Server 与 Agent 的文件传输分块大小，允许 4096–524288。 |
| `max_file_transfer_bytes` | `1073741824` | 单文件传输上限；`0` 表示不限制。 |
| `max_command_length` | `4096` | Web 单条命令最大 UTF-8 字节长度，允许 256–1048576。 |

### 真实客户端 IP 与反向代理

`client_ip_header` 默认留空最安全，此时只使用 TCP 对端地址。只有在你**确认 HomeCTL 的直接上游是可信代理**时才配置该字段。

Cloudflare Tunnel 与 HomeCTL 共用容器网络命名空间的典型配置：

```json
"client_ip_header": "CF-Connecting-IP",
"trusted_proxy_cidrs": ["127.0.0.1/32", "::1/128"]
```

HomeCTL 只在直接 TCP 对端属于 `trusted_proxy_cidrs` 时读取该 Header，因此公网客户端不能直接伪造它来绕过登录限流。

## Agent `config.json`

| 参数 | 默认值 | 说明 |
|---|---:|---|
| `server` | 必填 | Server Agent WebSocket URL，必须是合法的 `ws://` 或 `wss://` URL，例如 `wss://panel.example.com/agent/ws`。 |
| `name` | `""` | 首次注册时的默认显示名；为空时使用主机 hostname。最长 128 字符。控制台自定义名称后不会被心跳覆盖。 |
| `enroll_token` | `""` | 首次注册使用的一次性 Token。注册完成后不再参与设备认证，可从配置中清空。 |
| `state_file` | `state.json` | Agent 身份状态文件；相对路径按配置文件目录解析。 |
| `heartbeat_interval` | `10s` | 心跳及轻量系统信息上报周期。 |
| `reconnect_min` | `1s` | 断线重连最小退避。 |
| `reconnect_max` | `30s` | 断线重连最大退避，不能小于 `reconnect_min`。 |
| `dial_timeout` | `15s` | 建立 WebSocket/TLS 连接的超时。 |
| `handshake_timeout` | `15s` | 发出 hello 后等待 Server ACK 的超时。 |
| `write_timeout` | `10s` | Agent 写 WebSocket 消息的超时。 |
| `command_timeout` | `30s` | 单条 Web 命令最长执行时间；超时会终止该命令进程组。 |
| `max_command_output_bytes` | `524288` | 单条命令保留的 stdout/stderr 上限，允许 4096–16777216。超过部分丢弃并标记截断，不会无限占用 Agent 内存。 |
| `shell` | `/bin/bash` | 执行单条命令和打开 PTY 时使用的绝对 Shell 路径，必须指向现有可执行普通文件。 |
| `exec_enabled` | `true` | Agent 是否接受单条命令。 |
| `terminal_enabled` | `true` | Agent 是否允许 PTY Web Terminal。 |
| `file_browser_enabled` | `false` | Agent 是否允许文件浏览器。Server 端也必须同时开启。 |
| `file_browser_root` | `/` | 文件浏览器授权根目录，必须为绝对路径。无需整个系统时建议缩小范围。 |
| `file_transfer_chunk_bytes` | `65536` | Agent 文件传输分块，允许 4096–524288。 |
| `max_file_transfer_bytes` | `1073741824` | Agent 单文件传输上限；`0` 表示不限制。 |
| `disk_exclude_device_prefixes` | loop/zram/ram | 文件系统统计中要排除的设备路径前缀。一般无需修改。 |
| `cloudflare_access.client_id` | `""` | Cloudflare Access Service Token Client ID；必须与 Secret 同时设置或同时留空。 |
| `cloudflare_access.client_secret` | `""` | Cloudflare Access Service Token Secret。保护 Agent 配置文件权限。 |
| `tls.insecure_skip_verify` | `false` | 跳过 TLS 证书验证。仅受控的自签名测试环境使用。 |

## 建议的配置文件权限

Server：

```bash
chmod 600 /opt/homectl-server/config.json
chmod 700 /opt/homectl-server/data
```

Agent：

```bash
chmod 600 /opt/homectl-agent/config.json
chmod 600 /opt/homectl-agent/state.json
```
