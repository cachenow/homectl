# 故障排查

## Server 启动失败：placeholder password

如果日志提示 bootstrap 密码仍是示例值，生成新密码：

```bash
openssl rand -hex 32
```

写入 `admin_password` 后重启。管理员已经成功创建后可把该字段改成空字符串。

## 登录成功但马上又回登录页

如果通过 HTTP 直接访问，而配置是：

```json
"cookie_secure": true
```

浏览器不会在 HTTP 连接中正常使用 Secure Session Cookie。可信 LAN 的纯 HTTP 测试可改：

```json
"cookie_secure": false
```

公网/HTTPS 应保持 `true`。

## TOTP 一直错误

先检查 Server 主机时间：

```bash
timedatectl status
```

TOTP 依赖时间同步。HomeCTL 允许前后各一个 30 秒时间窗口，但严重时钟漂移仍会失败。

连续失败达到限制会临时锁定登录。等待 `totp_lockout_duration` 后再试。

## Agent 一直连接不上

```bash
journalctl -u homectl-agent -f
```

检查：

- `server` 是否为正确 `ws://` / `wss://` 地址
- 第一次注册的 `enroll_token` 是否过期或已经被使用
- Cloudflare Access Service Token 是否需要配置
- Server `/healthz` 是否正常
- Agent 系统时间和 TLS CA 是否正常

## 删除设备后 Agent 无法回来

这是预期行为：删除设备会吊销长期 Device Token。

重新注册：

```bash
systemctl stop homectl-agent
rm -f /opt/homectl-agent/state.json
```

Web 生成新的 Enrollment Token，写入 Agent config 后：

```bash
systemctl start homectl-agent
```

## 设备重启后长时间仍显示在线

确认：

```text
Agent heartbeat_interval     10s
Server agent_offline_timeout 25s
```

`agent_offline_timeout` 应大于心跳周期至少 2 倍。Web 默认每 5 秒刷新，因此默认最慢约 25–30 秒反映离线。

## Terminal 缩放异常或交互程序在缩放后结束

HomeCTL 会在创建 PTY 前传递浏览器实际 `cols/rows`。拖动窗口时，浏览器先立即适配本地画布，停止拖动后再向远端发送一次稳定尺寸；Server 和 Agent 还会拒绝非法尺寸并忽略重复尺寸，避免连续 `SIGWINCH` 让 `htop`、`vim` 等交互程序反复重绘。该逻辑不针对特定程序。

如果缩放后仍显示异常或终端连接被关闭：

1. 强制刷新浏览器缓存。
2. 确认 Server 与 Agent 都已升级；只更新一端无法获得完整修复。
3. 浏览器开发者工具 Console 检查 `terminal fit failed` 或 WebSocket 关闭信息。
4. 同时查看 Server 与 Agent 日志，确认没有写入超时、网络断开或资源上限错误。
5. 在浏览器 Network 中确认 `/vendor/xterm/xterm-6.0.0.mjs`、`addon-fit-0.11.0.mjs` 和 `xterm-6.0.0.css` 均返回 200。这些文件已嵌入 Server 二进制，不依赖外部 CDN；若返回 404，通常说明仍在运行旧版 Server。

## 文件浏览器按钮没有出现

Server 和 Agent 必须同时开启：

```json
"file_browser_enabled": true
```

然后重启对应进程。

## 文件浏览器返回 path 错误

文件浏览器使用 `os.Root`。试图通过 `..` 或 symlink 逃出 `file_browser_root` 的路径会被拒绝。这属于安全行为。

## SQLite 锁或损坏

先停止 Server，再备份数据库及 WAL/SHM 文件。HomeCTL 使用单连接、WAL、busy timeout，正常情况下不会出现高并发写冲突。

不要在 Server 正在写数据库时直接用文本工具修改 `.db`。
