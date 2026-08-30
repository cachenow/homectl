# HomeCTL

HomeCTL 是一个面向家庭/小型私有网络的轻量 Linux 远程管理面板：**Go Server + Linux Agent + Cloudflare Tunnel**。

Agent 只主动建立到服务端的 WSS 长连接，不要求家庭宽带有公网 IP，也不需要在受控 Linux 主机开放入站端口。

## 主要功能

- 多台 Linux Agent
- 一次性 Enrollment Token；注册完成后每台设备拥有独立 Device Token
- Device Token 在 Server SQLite 中只保存 SHA-256 哈希
- 主机名、OS、Kernel、架构、CPU 使用率/核心数、Load、内存、磁盘、IP、Uptime
- Web 设备卡片使用 CPU / 内存 / 磁盘百分比和进度条展示，静态信息分区排列
- 心跳超时离线判定，不依赖 TCP 半开连接自行超时
- Web 执行命令，显示 stdout/stderr、退出码、耗时
- 命令/操作结果面板可手动关闭，也可配置自动消失时间
- 重启 / 关机
- xterm.js + PTY Web Terminal；默认接近全窗口显示，支持最大化并自动同步终端行列数
- 用户名 + 密码登录；密码和用户名可在 Web 中修改
- 可选 RFC 6238 TOTP 两步验证，可使用 Google Authenticator 等 6 位验证码应用
- 可选文件浏览器：浏览、上传、下载、新建目录、重命名、删除文件/空目录
- 文件浏览器默认关闭；不开启时不会增加 Agent 的持续 CPU/内存负载
- Server 持久化只使用 SQLite
- Docker Compose + remotely-managed Cloudflare Tunnel
- GitHub Actions：CI、手动 Build & Release、Dependabot

---

## 架构

```text
Browser
   |
HTTPS / WSS
   |
Cloudflare
   |
cloudflared (Docker)
   |
HomeCTL Server (Docker + SQLite)
   ^
   |
outbound WSS
   |
Linux Agent (root + systemd)
```

Agent 状态、命令、终端和按需文件操作都复用同一条 WSS 连接。

---

# 一、服务端部署

如果直接从源码仓库使用 Compose，先创建本地配置文件：

```bash
cp deploy/server/config.example.json deploy/server/config.json
```

`deploy/server/config.json` 已加入 `.gitignore`，适合在部署机上直接修改。

推荐正式部署从 GitHub Release 下载：

```text
homectl-server-deploy-vX.Y.Z.tar.gz
```

解压后：

```text
server/
├── docker-compose.yml
├── config.json
└── data/
```

## 1. Server `config.json`

默认示例：

```json
{
  "listen_addr": ":8080",
  "database_path": "/data/homectl.db",
  "legacy_device_store": "",
  "admin_username": "admin",
  "admin_password": "CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD",
  "cookie_secure": true,
  "session_ttl": "24h",
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

### 管理员账号

`admin_username` / `admin_password` 是 **SQLite 尚未初始化管理员时的 bootstrap 值**。

首次启动后，管理员账号会写入 SQLite，密码以 bcrypt 哈希保存。之后可以在 Web → **账户** 中：

- 修改用户名
- 修改密码（修改后会使现有 Web Session 失效并要求重新登录）
- 开启 / 关闭 TOTP 两步验证

SQLite 已存在管理员后，Server 不再用配置文件中的 `admin_password` 覆盖数据库密码，因此可以把配置文件中的 `admin_password` 改成空字符串：

```json
"admin_password": ""
```

### SQLite

HomeCTL Server 的持久化数据库只有：

```text
/data/homectl.db
```

设备、管理员、TOTP Secret、一次性 Enrollment Token 状态都保存在 SQLite 中。

### 重要时间参数

推荐默认组合：

```text
Agent heartbeat          10s
Server offline timeout   25s
Web refresh               5s
```

所以设备突然断电、重启或网络黑洞后，通常在约 **25~30 秒**内显示离线。

CPU 使用率通过 Agent 每次心跳时读取一次 `/proc/stat`，用相邻两次心跳的 CPU 时间差计算，不创建额外采样线程。首次心跳会显示“采集中”，从下一次心跳开始显示百分比。

可调整：

| 参数 | 默认 | 用途 |
|---|---:|---|
| `agent_offline_timeout` | `25s` | 多久收不到 Agent 消息即关闭连接并判离线 |
| `agent_handshake_timeout` | `15s` | Agent 首次 hello 超时 |
| `agent_write_timeout` | `10s` | Server 写入 Agent WSS 超时 |
| `action_timeout` | `8s` | 重启/关机确认响应等待时间 |
| `exec_response_timeout` | `40s` | Web 命令响应等待时间 |
| `max_command_length` | `4096` | 单条 Web 命令最大字符/字节长度 |
| `file_transfer_timeout` | `2m` | 文件传输无进展超时 |
| `enrollment_token_ttl` | `30m` | 一次性 Agent 注册 Token 有效期 |
| `web_refresh_interval` | `5s` | Web 设备列表刷新周期 |
| `ui_result_ttl` | `20s` | 命令结果自动收起；`0s` 表示只手动关闭 |

建议：`agent_offline_timeout` 至少大于 Agent `heartbeat_interval` 的 2 倍。

## 2. Cloudflare Tunnel

仓库默认 Compose：

```yaml
cloudflared:
  image: cloudflare/cloudflared:latest
  restart: unless-stopped
  depends_on:
    - homectl
  network_mode: "service:homectl"
  command:
    - tunnel
    - --no-autoupdate
    - run
    - --token
    - PASTE_YOUR_CLOUDFLARE_TUNNEL_TOKEN_HERE
```

将最后一项替换成 Cloudflare remotely-managed Tunnel Token。

`network_mode: "service:homectl"` 表示 cloudflared 与 HomeCTL 容器共享网络命名空间，因此 Cloudflare Dashboard 的 Service URL 可以直接配置：

```text
http://localhost:8080
```

然后：

```bash
docker compose up -d
docker compose logs -f
```

> 仓库公开时不要提交真实 Cloudflare Tunnel Token；只在部署机上的 Compose 文件中替换。

### 可选：cloudflared 使用 host 网络

如果你更喜欢 host 网络，也可以手动改成：

```yaml
network_mode: host
```

HomeCTL 保持：

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

Cloudflare Dashboard 仍填写：

```text
http://localhost:8080
```

默认仓库仍保留 `service:homectl` 方式。

---

# 二、添加 Agent

## 1. 在 Web 中生成一次性 Token

登录 HomeCTL 后：

```text
添加设备 → 生成一次性 Agent Token
```

Server 只在生成时把原始 Token 返回给浏览器；SQLite 中保存 Token 哈希。Token：

- 默认 30 分钟过期
- 只能成功使用一次
- 每台新 Agent 应生成一个新的 Token

因此多台 Agent **不再共享一个全局 enrollment token**。

## 2. Agent `config.json`

```json
{
  "server": "wss://panel.example.com/agent/ws",
  "name": "",
  "enroll_token": "PASTE_A_ONE_TIME_TOKEN_FROM_THE_WEB_CONSOLE",
  "state_file": "state.json",
  "heartbeat_interval": "10s",
  "reconnect_min": "1s",
  "reconnect_max": "30s",
  "dial_timeout": "15s",
  "handshake_timeout": "15s",
  "write_timeout": "10s",
  "command_timeout": "30s",
  "max_command_output_bytes": 524288,
  "shell": "/bin/bash",
  "exec_enabled": true,
  "terminal_enabled": true,
  "file_browser_enabled": false,
  "file_browser_root": "/",
  "file_transfer_chunk_bytes": 65536,
  "max_file_transfer_bytes": 1073741824,
  "cloudflare_access": {
    "client_id": "",
    "client_secret": ""
  },
  "tls": {
    "insecure_skip_verify": false
  }
}
```

首次注册成功后自动生成：

```text
state.json
```

其中保存 Device ID 和该 Agent 独立的长期 Device Token。之后连接优先使用 Device Token；原来的一次性 `enroll_token` 不再参与认证，可以从 Agent 配置中清空。

## 3. 安装

标准目录：

```text
/opt/homectl-agent/
├── homectl-agent
├── config.json
└── state.json
```

Release 包内直接：

```bash
./install.sh
```

查看：

```bash
systemctl status homectl-agent
journalctl -u homectl-agent -f
```

升级 Agent：

```bash
systemctl stop homectl-agent
cp homectl-agent /opt/homectl-agent/homectl-agent
chmod 755 /opt/homectl-agent/homectl-agent
systemctl start homectl-agent
```

---

# 三、Web Terminal

终端使用 `@xterm/xterm 6.0.0` + `@xterm/addon-fit 0.10.0`。打开终端时会根据实际浏览器容器自动计算 cols/rows，并同步调整 Agent 侧 PTY。浏览器窗口变化、终端最大化/还原时也会自动重新适配，因此 `htop`、`vim`、`tmux` 等全屏 TUI 程序可以正常使用整个可见终端区域。

终端默认接近整个浏览器可视区域；右上角提供 **最大化** / **关闭**。

# 四、文件浏览器

文件浏览器默认 **双端关闭**。

Server：

```json
"file_browser_enabled": true
```

Agent：

```json
"file_browser_enabled": true,
"file_browser_root": "/"
```

两边都开启后，设备卡片会出现 **文件** 按钮。

当前支持：

- 浏览目录
- 上传
- 下载
- 新建目录
- 重命名
- 删除文件
- 删除空目录

删除目录故意采用非递归删除，避免在 root Agent 上误操作整棵目录树。

`file_browser_root` 可以限制 Web 文件浏览器看到的逻辑根目录，例如：

```json
"file_browser_root": "/srv"
```

此时 Web 中的 `/` 对应 Agent 的 `/srv`。路径解析会检查符号链接，避免借由 symlink 逃出配置根目录。

文件浏览器不开启或无人使用时：

- 不扫描目录
- 不建立额外网络连接
- 不做文件索引
- 不增加周期性 CPU/内存任务

只有实际浏览/上传/下载时才产生对应 I/O。

文件传输大小可以通过两端的：

```json
"max_file_transfer_bytes": 1073741824
```

限制；`0` 表示不限制。传输采用分块方式，不需要一次性把整个文件载入 Agent 内存。

---

# 五、TOTP 两步验证

Web → **账户 → 两步验证**。

HomeCTL 使用标准 RFC 6238 TOTP：

```text
SHA-1
6 digits
30 seconds
```

兼容 Google Authenticator、Microsoft Authenticator、1Password 等应用。

开启时页面会显示 Secret 和 `otpauth://` URI。在 Authenticator 中手动添加 Secret，然后输入当前 6 位验证码确认即可。

启用后登录需要：

```text
用户名 + 密码 + 6 位 TOTP
```

TOTP 为可选功能，默认关闭。

---

# 六、从旧版 JSON Store 升级

本版 Server 持久化已经改为 SQLite。旧版本如果存在：

```text
/data/homectl.json
```

可以临时配置：

```json
"database_path": "/data/homectl.db",
"legacy_device_store": "/data/homectl.json"
```

Server 启动时会把旧 JSON 中的 Device ID、独立 Device Token、LastSeen、SystemInfo 导入 SQLite；已有 SQLite 记录不会被覆盖。

确认旧 Agent 均能正常重新上线以后，把：

```json
"legacy_device_store": ""
```

并可删除旧 JSON 文件。

这只是一次性迁移入口，运行时数据库仍然只有 SQLite。

---

# 七、建议的安全边界

HomeCTL Agent 以 root 运行，并且可以执行命令、开 PTY、可选读写文件，因此 Web 控制台本身相当于主机 root 控制入口。

建议：

- 使用 HTTPS/WSS
- 使用 Cloudflare Tunnel
- 管理员使用长随机密码
- 开启 TOTP
- 不需要时关闭 `allow_exec` / `allow_terminal` / `file_browser_enabled`
- File Browser 默认保持关闭
- 每台新 Agent 使用独立的一次性 Enrollment Token
- 不把真实 Cloudflare Tunnel Token 提交到公开仓库
