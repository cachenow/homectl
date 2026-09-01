# Cloudflare Tunnel

Cloudflare Tunnel 是可选入口。HomeCTL 本身不依赖 Cloudflare；你也可以使用 Caddy、Nginx、VPN 或仅局域网访问。

Compose 示例使用官方 `cloudflare/cloudflared:latest`，执行 `docker compose pull` 时获取当前稳定镜像；`--no-autoupdate` 只禁止容器在运行中自行替换，不影响由 Compose 统一更新。

## Remotely-managed Tunnel

在 Cloudflare Dashboard 创建 Tunnel 后会得到一个 Tunnel Token。HomeCTL 不需要保存 Cloudflare 账号 API Token。

### Docker：默认共享 HomeCTL 网络

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
    - YOUR_TUNNEL_TOKEN
```

由于共享 HomeCTL 容器的 network namespace，Cloudflare Dashboard 的 Service URL 填：

```text
http://localhost:8080
```

### Docker：可选 host 网络

如果宿主机是 Linux，也可以改为：

```yaml
cloudflared:
  image: cloudflare/cloudflared:latest
  restart: unless-stopped
  network_mode: host
  command:
    - tunnel
    - --no-autoupdate
    - run
    - --token
    - YOUR_TUNNEL_TOKEN
```

HomeCTL 建议只映射本机：

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

Dashboard Service 仍是：

```text
http://localhost:8080
```

### Server 是纯二进制

让 HomeCTL 监听：

```json
"listen_addr": "127.0.0.1:8080"
```

然后在宿主机运行 cloudflared，Tunnel Service 指向：

```text
http://127.0.0.1:8080
```

## 域名和 Agent 地址

例如管理台域名：

```text
https://panel.example.com
```

Agent 配置：

```json
"server": "wss://panel.example.com/agent/ws"
```

Web API、浏览器 Terminal WSS 和 Agent WSS 可以安全地共用一个 HTTPS/WSS 域名，通过不同 URL path 区分；HomeCTL 不要求额外端口或额外域名。

## Cookie 设置

浏览器通过 Cloudflare HTTPS 访问时必须保持：

```json
"cookie_secure": true
```

## Cloudflare Access

如果你对整个域名启用了 Cloudflare Access，需要确保 Agent 的 `/agent/ws` 也能通过认证。可在 Agent 配置 Service Token：

```json
"cloudflare_access": {
  "client_id": "...",
  "client_secret": "..."
}
```

这些值属于 Agent 端敏感凭据，应保护 `config.json` 权限。

## 登录限流使用真实客户端 IP

Cloudflare 会向源站提供 `CF-Connecting-IP`。如果 `cloudflared` 与 HomeCTL 通过本机回环地址通信，建议 Server 配置：

```json
"client_ip_header": "CF-Connecting-IP",
"trusted_proxy_cidrs": ["127.0.0.1/32", "::1/128"]
```

这样密码失败计数按真实访问者 IP 隔离，而不是把所有 Cloudflare 请求都视作同一个本地 `cloudflared` 地址。

不要把不受控网络加入 `trusted_proxy_cidrs`。HomeCTL 只有在 TCP 直接来源属于该列表时才信任 `client_ip_header`。
