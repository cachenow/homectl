# 纯二进制部署 Server

HomeCTL Server 不依赖 Docker，也不依赖 cloudflared。该方式适合 VPS、NAS、ARM 小主机以及存储空间较小的嵌入式 Linux。官方 Release 的 Server 产物当前覆盖 Linux amd64 与 arm64；其他架构需要从源码自行构建并在目标系统验证。

## 1. 选择二进制

推荐普通包：

```text
homectl-server-vX.Y.Z-linux-amd64.tar.gz
homectl-server-vX.Y.Z-linux-arm64.tar.gz
```

只有存储空间明显受限时才选择文件名带 `-upx` 的附加压缩包。两种包功能相同，见 [RELEASES.md](RELEASES.md)。

## 2. 解压并准备运行目录

```bash
mkdir -p /opt/homectl-server
tar -xzf /path/to/homectl-server-vX.Y.Z-linux-amd64.tar.gz -C /opt/homectl-server
mkdir -p /opt/homectl-server/data
```

把示例包名换成第 1 步选择的架构。Release 包会直接形成：

```text
/opt/homectl-server/
├── homectl-server
├── config.json
├── homectl-server.service
├── README.md
├── docs/
└── data/
```

```bash
chmod 755 /opt/homectl-server/homectl-server
chmod 600 /opt/homectl-server/config.json
chmod 700 /opt/homectl-server/data
```

## 3. 运行账号

Server 不要求 root。普通 Linux 推荐：

```bash
useradd --system --home /opt/homectl-server --shell /usr/sbin/nologin homectl
chown -R homectl:homectl /opt/homectl-server
```

如果精简嵌入式系统没有 `useradd`，可以使用现有服务账号；确有需要时也可以使用 root。

## 4. 生成首次管理员密码

```bash
openssl rand -hex 32
```

打开 `/opt/homectl-server/config.json`，把结果写入 `admin_password`。Release 中的该文件已包含 Server 的全部可配置项，不要用只含少数字段的新文件覆盖它。

可信 LAN 中直接使用 HTTP 的完整示例：

```json
{
  "listen_addr": "0.0.0.0:8080",
  "database_path": "data/homectl.db",
  "legacy_device_store": "",
  "admin_username": "admin",
  "admin_password": "替换为刚生成的密码",
  "cookie_secure": false,
  "session_ttl": "24h",
  "remember_session_ttl": "168h",
  "preauth_ttl": "5m",
  "preauth_max_attempts": 5,
  "password_max_failures": 10,
  "password_failure_window": "15m",
  "password_lockout_duration": "1m",
  "password_hash_concurrency": 1,
  "password_hash_queue_timeout": "5s",
  "totp_max_failures": 10,
  "totp_failure_window": "15m",
  "totp_lockout_duration": "1m",
  "totp_setup_ttl": "10m",
  "client_ip_header": "",
  "trusted_proxy_cidrs": ["127.0.0.1/32", "::1/128"],
  "allow_exec": true,
  "allow_terminal": true,
  "file_browser_enabled": false,
  "agent_offline_timeout": "25s",
  "agent_handshake_timeout": "15s",
  "agent_write_timeout": "10s",
  "action_timeout": "8s",
  "exec_response_timeout": "40s",
  "file_transfer_timeout": "2m",
  "enrollment_token_ttl": "30m",
  "web_refresh_interval": "5s",
  "ui_result_ttl": "20s",
  "http_read_header_timeout": "10s",
  "shutdown_timeout": "10s",
  "file_transfer_chunk_bytes": 65536,
  "max_file_transfer_bytes": 1073741824,
  "max_command_length": 4096
}
```

如果浏览器最终通过 HTTPS 访问，请改为：

```json
"cookie_secure": true
```

全部参数见 [CONFIGURATION.md](CONFIGURATION.md)。

## 5. 手动启动验证

使用第 3 步创建的运行账号启动，避免由 root 提前创建一个 systemd 服务无法写入的数据库：

```bash
cd /opt/homectl-server
runuser -u homectl -- ./homectl-server -config ./config.json
```

如果第 3 步选择了其他服务账号，把 `homectl` 换成该账号；如果明确选择由 root 运行，才直接执行二进制。

另一个终端：

```bash
curl http://127.0.0.1:8080/healthz
```

看到 `ok` 后结束前台进程。

## 6. systemd

默认 service 使用 `User=homectl` 和 `Group=homectl`。如果第 3 步选择了其他账号，先编辑 `/opt/homectl-server/homectl-server.service` 中这两行，再继续：

```bash
cp /opt/homectl-server/homectl-server.service /etc/systemd/system/homectl-server.service
systemctl daemon-reload
systemctl enable --now homectl-server
systemctl status homectl-server
```

日志：

```bash
journalctl -u homectl-server -f
```

## 7. HTTPS / 公网入口

Server 自身提供 HTTP，TLS 通常由入口层终止。可选：

- Cloudflare Tunnel：见 [CLOUDFLARE.md](CLOUDFLARE.md)
- Caddy / Nginx
- WireGuard / Tailscale 等私有网络
- 可信 LAN 中直接 HTTP

使用宿主机 `cloudflared` 时推荐：

```json
"listen_addr": "127.0.0.1:8080",
"cookie_secure": true,
"client_ip_header": "CF-Connecting-IP",
"trusted_proxy_cidrs": ["127.0.0.1/32", "::1/128"]
```

然后 Tunnel Service 指向：

```text
http://127.0.0.1:8080
```

## 8. 首次登录和 Agent

首次登录成功表示管理员已经写入 SQLite。此时将配置中的：

```json
"admin_password": ""
```

然后重启一次并确认仍能登录：

```bash
systemctl restart homectl-server
systemctl status homectl-server
```

最后从“设备管理”生成一次性 Enrollment Token，并继续 [AGENT.md](AGENT.md)。
