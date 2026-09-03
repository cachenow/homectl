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

## Terminal 外框很大但 htop 仍很小

HomeCTL 会在创建 PTY 前把浏览器实际 `cols/rows` 传给 Server，并在窗口缩放后继续发送 `term_resize`。如果仍异常：

1. 强制刷新浏览器缓存。
2. 确认 Server 已升级到最新版本。
3. 浏览器开发者工具 Console 检查 `terminal fit failed`。
4. 打开浏览器 Network，确认 `/vendor/xterm/xterm-6.0.0.mjs`、`/vendor/xterm/addon-fit-0.11.0.mjs` 和 `/vendor/xterm/xterm-6.0.0.css` 均返回 200。它们已内置于 Server，不需要访问 CDN。

调整终端窗口时，浏览器会先立即适配本地画布，再经过短暂防抖向 PTY 发送真正变化后的行列数。持续交互程序不应因为重复 Resize 被关闭；如果连接本身显示“已关闭”，同时检查 Server 与 Agent 日志中的 WebSocket 断线原因。

## 指标卡片没有数据

先在“设备管理”中点击目标设备行的“指标卡片”，确认该卡片已启用。这里的选择按设备独立保存；只操作六点拖拽手柄不会切换当前设置对象。

- 网络和磁盘 I/O 速率第一次采样为 0，下一次心跳后才有差值。
- 进程统计最多缓存 60 秒。
- CPU 温度无法从 Linux thermal/hwmon 读取时会自动隐藏。
- 设备离线后，卡片中的动态值会立即显示为 0；主机名、系统、架构、IP 和 Agent 版本等静态详情仍保留，便于识别设备。

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
