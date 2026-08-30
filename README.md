# HomeCTL

HomeCTL 是一个面向家庭/小型私有网络的轻量 Linux 远程管理面板，采用 **Go Server + Linux Agent + Cloudflare Tunnel**。

内网 Agent 只主动建立到服务端的 WSS 长连接，不要求家庭宽带具有公网 IP，也不需要在内网设备开放任何入站端口。

## 功能

- 多台 Linux 设备自动注册与长期独立 Device Token
- 主机名、系统、内核、架构、CPU、负载、内存、磁盘、IP、运行时间等状态
- Web 执行命令
- 重启 / 关机
- xterm.js + PTY 真 Web Terminal，可运行 `bash`、`top`、`vim`、`tmux` 等交互程序
- Server 使用单个 `config.json` 配置
- Agent 使用与二进制同目录的 `config.json`
- Agent 首次启动后自动生成同目录 `state.json`
- Docker Compose + remotely-managed Cloudflare Tunnel
- GitHub Actions 自动测试、跨架构编译、GitHub Release、GHCR 多架构镜像

## 架构

```text
Browser
   |
HTTPS / WSS
   |
Cloudflare
   |
Cloudflare Tunnel (Docker)
   |
HomeCTL Server (Docker)
   ^
   |
outbound WSS
   |
Linux Agent (root)
```

Agent 与控制指令复用一条 WSS 连接，终端通过 `session_id` 多路复用，因此一台 Server 可以同时管理多台设备和多个终端会话。

---

# 一、服务端

推荐使用 GitHub Release 自动生成的：

```text
homectl-server-deploy-vX.Y.Z.tar.gz
```

解压后目录结构：

```text
server/
├── docker-compose.yml
├── config.json
└── data/
```

## 1. 配置 `config.json`

```json
{
  "listen_addr": ":8080",
  "database_path": "/data/homectl.json",
  "admin_password": "CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD",
  "enroll_token": "CHANGE_ME_TO_A_DIFFERENT_LONG_RANDOM_TOKEN",
  "cookie_secure": true,
  "session_ttl": "24h",
  "allow_exec": true,
  "allow_terminal": true
}
```

建议生成两个不同的随机值：

```bash
openssl rand -hex 32
openssl rand -hex 32
```

一个用于 `admin_password`，另一个用于 `enroll_token`。

## 2. Cloudflare Tunnel

在 Cloudflare Dashboard 创建 **remotely-managed Tunnel**，复制 Tunnel Token，然后直接编辑 `docker-compose.yml`：

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

把最后一行替换为真实 Tunnel Token 即可，然后：

> 如果 GitHub 仓库是公开的，不要把真实 Tunnel Token 提交到仓库；仓库里保留占位符，只在部署服务器上的 Compose 文件中替换。


```bash
docker compose up -d
docker compose logs -f
```

本项目让 `cloudflared` 与 HomeCTL Server 共享 network namespace，因此在 Cloudflare Dashboard 的 Public Hostname 中，Service 可以直接填写：

```text
http://localhost:8080
```

域名例如：

```text
panel.example.com
```

不需要在服务器防火墙上开放 HomeCTL 的 8080 公网端口。Compose 仅把它绑定到：

```text
127.0.0.1:8080
```

方便服务器本机调试。

---

# 二、内网 Linux Agent

从 GitHub Release 下载对应架构：

```text
homectl-agent-vX.Y.Z-linux-amd64.tar.gz
homectl-agent-vX.Y.Z-linux-arm64.tar.gz
homectl-agent-vX.Y.Z-linux-armv7.tar.gz
```

解压后：

```text
homectl-agent
config.json
homectl-agent.service
install.sh
README.md
```

## 1. 编辑 `config.json`

最重要的只有：

```json
{
  "server": "wss://panel.example.com/agent/ws",
  "name": "",
  "enroll_token": "和服务端相同的 enroll_token",
  "state_file": "state.json",
  "heartbeat_interval": "15s",
  "reconnect_min": "1s",
  "reconnect_max": "30s",
  "dial_timeout": "15s",
  "command_timeout": "30s",
  "max_command_output_bytes": 524288,
  "shell": "/bin/bash",
  "exec_enabled": true,
  "terminal_enabled": true,
  "cloudflare_access": {
    "client_id": "",
    "client_secret": ""
  },
  "tls": {
    "insecure_skip_verify": false
  }
}
```

`name` 留空时自动使用系统 hostname。

如果 Cloudflare Access 同时保护了 Agent 路径，可以创建 Access Service Token 并填写：

```json
"cloudflare_access": {
  "client_id": "xxxxxxxx.access",
  "client_secret": "xxxxxxxx"
}
```

## 2. 安装

Agent 的标准安装目录就是：

```text
/opt/homectl-agent/
├── homectl-agent
├── config.json
└── state.json
```

直接：

```bash
chmod +x install.sh
./install.sh
```

安装脚本只做三件事：复制二进制和配置到 `/opt/homectl-agent`、安装 systemd unit、启动服务；**没有环境变量文件**。

查看状态：

```bash
systemctl status homectl-agent
journalctl -u homectl-agent -f
```

首次连接成功后会自动生成：

```text
/opt/homectl-agent/state.json
```

里面保存随机 Device ID 与服务器签发的独立 Device Token。后续启动优先使用 Device Token，不再使用全局 enrollment token 认证该设备。

完成所有设备注册后，可以修改服务端的 `enroll_token` 并重启服务端，从而使旧 enrollment token 失效。

## 3. 升级 Agent

只需要替换二进制：

```bash
systemctl stop homectl-agent
cp homectl-agent /opt/homectl-agent/homectl-agent
chmod 755 /opt/homectl-agent/homectl-agent
systemctl start homectl-agent
```

`config.json` 和 `state.json` 不需要动。

---

# 三、GitHub Actions

仓库已经包含三个 Workflow。

## CI

```text
.github/workflows/ci.yml
```

Push / Pull Request 时自动：

- `gofmt` 检查
- `go test ./...`
- `go vet ./...`
- 编译 Server / Agent

## Manual Build

```text
.github/workflows/build.yml
```

GitHub -> Actions -> **Manual Build** -> **Run workflow**。

它会自动：

- 编译 amd64 / arm64 / armv7 Agent
- 编译 amd64 / arm64 Server
- 打包可下载 Artifact
- 构建 amd64 + arm64 Server Docker 镜像
- 推送到 GHCR：

```text
ghcr.io/<owner>/<repo>:dev-xxxxxxx
ghcr.io/<owner>/<repo>:edge
```

因此平时修改代码后，不需要本地安装 Go 编译环境。

## Release

```text
.github/workflows/release.yml
```

创建 tag 即可发布正式版本：

```bash
git tag v0.2.0
git push origin v0.2.0
```

Actions 会自动生成 GitHub Release，并发布：

```text
homectl-agent-v0.2.0-linux-amd64.tar.gz
homectl-agent-v0.2.0-linux-arm64.tar.gz
homectl-agent-v0.2.0-linux-armv7.tar.gz
homectl-server-v0.2.0-linux-amd64
homectl-server-v0.2.0-linux-arm64
homectl-server-deploy-v0.2.0.tar.gz
SHA256SUMS
```

同时发布：

```text
ghcr.io/<owner>/<repo>:v0.2.0
ghcr.io/<owner>/<repo>:latest
```

Release 里的 `homectl-server-deploy-*.tar.gz` 会自动把当前 GitHub 仓库对应的 GHCR 地址写进 `docker-compose.yml`，因此下载后无需自己改 Docker image 名称。

如果 GHCR Container Package 保持为 Private，部署服务器需要先 `docker login ghcr.io`；如果把该 Package 的可见性改为 Public，则部署端无需登录即可拉取镜像。

---

# 四、从源码直接运行

服务端本地 Docker 构建：

```bash
cp deploy/server/config.example.json deploy/server/config.json

# 编辑 deploy/server/config.json 和 docker-compose.yml
# 将 docker-compose.yml 中的 PASTE_YOUR_CLOUDFLARE_TUNNEL_TOKEN_HERE 替换为真实 Tunnel Token
docker compose up -d --build
```

本地 Go 编译：

```bash
go mod tidy
make build
```

---

# 五、配置说明

Server：

| 字段 | 说明 |
|---|---|
| `listen_addr` | HTTP 监听地址 |
| `database_path` | 设备信息和 Device Token 数据文件 |
| `admin_password` | Web 管理密码 |
| `enroll_token` | 新 Agent 首次注册使用的全局 Token |
| `cookie_secure` | 登录 Cookie 是否只允许 HTTPS |
| `session_ttl` | Web 登录 Session 生命周期 |
| `allow_exec` | 是否允许网页执行命令 |
| `allow_terminal` | 是否允许 Web Terminal |

Agent：

| 字段 | 说明 |
|---|---|
| `server` | Server Agent WebSocket 地址 |
| `name` | 面板显示名称；留空使用 hostname |
| `enroll_token` | 第一次注册使用 |
| `state_file` | Device ID / Device Token 状态文件；相对路径以 config 所在目录为基准 |
| `heartbeat_interval` | 状态上报间隔 |
| `reconnect_min/max` | 断线重连退避 |
| `command_timeout` | 普通命令最大执行时间 |
| `max_command_output_bytes` | 普通命令最大输出 |
| `shell` | 命令和终端所使用的 Shell |
| `exec_enabled` | Agent 本机是否允许执行命令 |
| `terminal_enabled` | Agent 本机是否允许 Web Terminal |
| `cloudflare_access` | 可选 Cloudflare Access Service Token |
| `tls.insecure_skip_verify` | 是否忽略 TLS 验证，默认必须为 `false` |

## 安全边界

Agent 以 root 运行，所以获得 HomeCTL Web 控制权本质上等价于获得受管设备的 root 权限。建议：

1. 使用 Cloudflare Access 保护 Web 控制台。
2. `admin_password` 与 `enroll_token` 使用不同随机值。
3. 完成设备注册后轮换 `enroll_token`。
4. 不需要命令执行或终端时分别关闭 `allow_exec` / `allow_terminal` 和 Agent 对应选项。
5. 保护 `config.json`、`state.json`、`homectl.json`，并限制包含真实 Tunnel Token 的 `docker-compose.yml` 访问权限。

## License

MIT
