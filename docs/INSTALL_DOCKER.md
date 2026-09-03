# Docker Compose 部署 Server

HomeCTL Server 可以通过 Docker Compose 运行，Agent 不需要 Docker。Cloudflare Tunnel 是可选组件；如果已有 Caddy、Nginx、VPN 或只在局域网使用，可以完全不运行 `cloudflared`。

推荐优先使用 Release 中的 Server 部署包；需要修改源码时再从源码仓库构建镜像。

## 1. 使用 Release 部署包（推荐）

Release 中选择：

```text
homectl-server-deploy-vX.Y.Z.tar.gz
```

创建目录并解压：

```bash
mkdir -p /opt/homectl-server
cd /opt/homectl-server
tar -xzf /path/to/homectl-server-deploy-vX.Y.Z.tar.gz
chmod 600 config.json
chmod 700 data
```

解压后的结构：

```text
/opt/homectl-server/
├── config.json
├── docker-compose.yml
├── data/
├── README.md
└── docs/
```

`docker-compose.yml` 已指向对应 Release 的 GHCR 镜像，不需要本机 Go 编译环境。

## 2. 从源码仓库构建

如果你拿到的是完整源码仓库：

```bash
cd /path/to/homectl
cp deploy/server/config.example.json deploy/server/config.json
mkdir -p data
chmod 600 deploy/server/config.json
chmod 700 data
```

仓库根目录的 `docker-compose.yml` 使用当前源码和 `Dockerfile` 构建 Server。

> 源码部署请在**仓库根目录**运行 `docker compose`，不要只复制 `docker-compose.yml` 到一个空目录后执行 `--build`，因为构建上下文需要完整源码。

## 3. 生成首次管理员密码

推荐生成高熵随机密码：

```bash
openssl rand -hex 32
```

如果精简系统没有 OpenSSL，也可以使用：

```bash
od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
```

把生成结果写入 `config.json`：

```json
"admin_username": "admin",
"admin_password": "这里替换为刚生成的密码"
```

`admin_username` 和 `admin_password` 只在 SQLite 中尚未存在管理员时用于首次初始化。管理员创建成功后，后续认证完全以 SQLite 中的账户为准，可以把配置中的 `admin_password` 改成空字符串。

## 4. 完整 Server 配置示例

Cloudflare Tunnel 场景示例：

```json
{
  "listen_addr": ":8080",
  "database_path": "/data/homectl.db",
  "legacy_device_store": "",
  "admin_username": "admin",
  "admin_password": "替换为刚生成的密码",
  "cookie_secure": true,
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
  "client_ip_header": "CF-Connecting-IP",
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

所有字段的用途、默认值和限制见 [CONFIGURATION.md](CONFIGURATION.md)。

## 5. 完整 Compose 示例

### Release 镜像

Release 部署包中的 Compose 类似：

```yaml
services:
  homectl:
    image: ghcr.io/OWNER/REPO:vX.Y.Z
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
    volumes:
      - ./config.json:/app/config.json:ro
      - ./data:/data
    ports:
      - "127.0.0.1:8080:8080"

  cloudflared:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
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

### 源码构建

仓库根目录的 Compose：

```yaml
services:
  homectl:
    build:
      context: .
      args:
        VERSION: dev
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
    volumes:
      - ./deploy/server/config.json:/app/config.json:ro
      - ./data:/data
    ports:
      - "127.0.0.1:8080:8080"

  cloudflared:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
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

不使用 Cloudflare 时，删除整个 `cloudflared` service 即可。之后可以让 Caddy/Nginx 反代 `127.0.0.1:8080`，或者按可信 LAN 的需求调整监听地址。

## 6. Cloudflare Tunnel（可选）

在 Cloudflare Dashboard 创建 remotely-managed Tunnel，把得到的 Tunnel Token 替换到 Compose 中。

默认 Compose 让 `cloudflared` 与 HomeCTL 共用网络命名空间，因此 Public Hostname 的 Service 填：

```text
http://localhost:8080
```

域名例如：

```text
panel.example.com
```

`host` 网络替代方案、Cloudflare Access 和真实客户端 IP 设置见 [CLOUDFLARE.md](CLOUDFLARE.md)。

## 7. 启动

### Release 部署包

```bash
cd /opt/homectl-server
docker compose pull
docker compose up -d
docker compose ps
docker compose logs -f homectl
```

### 源码仓库

```bash
cd /path/to/homectl
docker compose up -d --build
docker compose ps
docker compose logs -f homectl
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

应返回：

```text
ok
```

## 8. 首次登录

通过你配置的浏览器入口打开 HomeCTL，用首次 `admin_username` 和刚生成的密码登录。

建议首次登录后：

1. 打开“账户设置”。
2. 按需修改用户名和密码。
3. 按需启用 TOTP 两步验证。
4. 将 `config.json` 中的 `admin_password` 改成 `""`。
5. 重启 Server，确认数据库中的管理员账户可以正常登录。

```bash
docker compose restart homectl
```

## 9. 添加 Agent

进入：

```text
设备管理 → 添加设备 → 生成一次性 Agent Token
```

然后按 [AGENT.md](AGENT.md) 安装 Agent。

## 10. 更新

Release 镜像：

```bash
docker compose pull
docker compose up -d
```

升级前建议备份 `data/homectl.db`，详见 [UPGRADING.md](UPGRADING.md)。
