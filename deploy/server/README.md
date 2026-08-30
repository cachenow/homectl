# HomeCTL Server deployment

1. 编辑 `config.json`，至少修改 `admin_username` / `admin_password`。
2. 编辑 `docker-compose.yml`，将 `PASTE_YOUR_CLOUDFLARE_TUNNEL_TOKEN_HERE` 替换为 Cloudflare remotely-managed Tunnel Token。
3. 启动：

```bash
docker compose up -d
docker compose logs -f
```

默认 Compose 使用：

```yaml
network_mode: "service:homectl"
```

因此 cloudflared 与 HomeCTL 共享网络命名空间，Cloudflare Dashboard 的 Service URL 直接填写：

```text
http://localhost:8080
```

如果希望 cloudflared 使用 host 网络，可以手动改成：

```yaml
network_mode: host
```

HomeCTL 已经只发布在宿主机 `127.0.0.1:8080`，此时 Cloudflare Service URL 仍然填写 `http://localhost:8080`。

首次启动后管理员账号写入 SQLite `/data/homectl.db`。之后用户名、密码、TOTP 均可在 Web → 账户中修改。

新 Agent 不共享全局注册 Token：登录 Web 后点击“添加设备”为每台新 Agent 单独生成一次性 Token。

默认存活参数：

```text
Agent heartbeat        10s
Server offline timeout 25s
Web refresh             5s
```

文件浏览器默认关闭。如需启用，Server 和对应 Agent 的 `file_browser_enabled` 都要设置为 `true`。
