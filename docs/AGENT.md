# Agent 安装、首次上线与升级

Agent 主动连接 HomeCTL Server，因此设备不需要公网 IP，也不需要开放入站端口。设备必须能访问 Server 的 `ws://` 或 `wss://` 地址。由于 Agent 可执行重启、关机、PTY Shell 和可选文件操作，标准部署以 root 运行。

以下首次安装流程按实际操作顺序编排。systemd 与 OpenWrt 用户都先完成第 1、2 步，再在第 3 步由安装脚本自动选择服务管理器。

## 1. 选择并解压正确的包

先确认设备架构：

```sh
uname -m
```

| `uname -m` 常见结果 | Release 包 |
|---|---|
| `x86_64` | `linux-amd64` |
| `aarch64`、`arm64` | `linux-arm64` |
| `armv7l`、`armv7` | `linux-armv7` |

普通版和文件名带 `-upx` 的压缩版功能相同，只选一个。空间不紧张时推荐兼容性更稳的普通版。

```sh
mkdir -p homectl-agent-install
tar -xzf /path/to/homectl-agent-vX.Y.Z-linux-amd64.tar.gz -C homectl-agent-install
cd homectl-agent-install
chmod 755 homectl-agent install.sh homectl-agent.openwrt.init
chmod 600 config.json
```

把示例中的包名换成设备对应架构。解压目录只是安装源；安装脚本随后会把运行文件放到 `/opt/homectl-agent`。

## 2. 生成 Token 并配置首次连接

准备好文件后再进入 Server Web 控制台，避免一次性 Token 在安装过程中提前过期：

```text
设备管理 → 添加设备 → 填写“首次显示名称”（可选）→ 生成一次性 Agent Token
```

“首次显示名称”会成为设备首次上线后的控制台名称，并保留原始大小写，例如 `HomeServer`。留空时才依次使用 Agent `config.json` 中的 `name` 和设备 hostname。每台设备要单独生成 Token；默认 30 分钟有效，成功注册一次后立即失效。

直接编辑解压目录中的完整 `config.json`。该文件已列出 Agent 的全部可配置项，首次安装至少确认：

```json
{
  "server": "wss://panel.example.com/agent/ws",
  "name": "",
  "enroll_token": "这里粘贴刚生成的一次性Token",
  "state_file": "state.json",
  "shell": "/bin/bash"
}
```

- `server` 必须以 `ws://` 或 `wss://` 开头，并以 `/agent/ws` 结尾。
- `name` 只在 Web 的“首次显示名称”留空时作为后备名称；留空则再使用 hostname。
- Debian、Ubuntu 等普通 Linux 通常使用 `/bin/bash`。
- OpenWrt 通常没有 Bash，应改成实际存在的绝对路径，常见为 `/bin/ash`。
- 不要删除配置文件中的其他默认字段；全部参数见 [CONFIGURATION.md](CONFIGURATION.md)。

如果使用 Cloudflare Access Service Token，在同一文件中填写 `cloudflare_access.client_id` 与 `cloudflare_access.client_secret`；两项必须同时填写或同时留空。

## 3. 安装并首次启动

在解压目录执行：

```sh
./install.sh
```

脚本会连续完成以下工作：

1. 创建权限为 `0700` 的 `/opt/homectl-agent`。
2. 安装二进制到 `/opt/homectl-agent/homectl-agent`。
3. 首次安装时复制 `config.json` 并设置为 `0600`；升级时保留现有配置。
4. 检测正在运行的 systemd 或 OpenWrt procd。
5. 安装对应服务文件、设置开机自启并立即启动。

如果 `/opt/homectl-agent/config.json` 已经存在，脚本会保留它。重装或修复既有实例时，应直接编辑该已安装配置，而不是只修改解压目录中的副本。

systemd 检查命令：

```sh
systemctl status homectl-agent
journalctl -u homectl-agent -n 50 --no-pager
```

OpenWrt / procd 检查命令：

```sh
/etc/init.d/homectl-agent status
logread -e homectl-agent
```

如果系统既没有 systemd 也没有 OpenWrt procd，可以手动运行：

```sh
mkdir -p /opt/homectl-agent
cp homectl-agent config.json /opt/homectl-agent/
chmod 700 /opt/homectl-agent
chmod 755 /opt/homectl-agent/homectl-agent
chmod 600 /opt/homectl-agent/config.json
cd /opt/homectl-agent
./homectl-agent -config ./config.json
```

## 4. 验证上线并完成注册收尾

首次连接成功后应同时满足：

- Web 设备列表显示在线；填写过的“首次显示名称”保持原始大小写。
- `/opt/homectl-agent/state.json` 已生成，包含该设备独立的 Device ID 与长期 Device Token。
- `config.json` 和 `state.json` 权限均为 `0600`。

```sh
ls -l /opt/homectl-agent/config.json /opt/homectl-agent/state.json
```

确认 `state.json` 已成功生成后，把已安装配置中的一次性 Token 清空：

```json
"enroll_token": ""
```

然后重启并再次确认在线：

```sh
systemctl restart homectl-agent
```

OpenWrt 使用：

```sh
/etc/init.d/homectl-agent restart
```

不要删除 `state.json`。以后连接使用其中的长期设备凭据，不再依赖 Enrollment Token。

## 5. OpenWrt 与资源受限设备

Agent 是 `CGO_ENABLED=0` 构建的单文件二进制，不依赖 Docker 或目标设备上的 Go 运行时。官方 Release 提供 `linux-amd64`、`linux-arm64` 和 `linux-armv7`；MIPS、MIPS64 等设备需要从源码按目标 `GOARCH` 交叉构建并在实机验证。

资源紧张时先关闭不需要的高权限功能：

```json
{
  "exec_enabled": false,
  "terminal_enabled": false,
  "file_browser_enabled": false
}
```

只有闪存空间确实紧张时再选用 `-upx` 包。Agent 还内置固定并发上限：同一时刻最多执行 1 个系统动作、4 条普通命令、4 个 PTY、4 个上传、4 个下载和 8 个短文件操作；达到上限会明确拒绝新请求，不会无界创建进程或缓冲区。

## 6. 升级

升级只替换二进制，必须保留 `/opt/homectl-agent/config.json` 和 `/opt/homectl-agent/state.json`。

systemd：

```sh
systemctl stop homectl-agent
cp ./homectl-agent /opt/homectl-agent/.homectl-agent.new
chmod 755 /opt/homectl-agent/.homectl-agent.new
mv -f /opt/homectl-agent/.homectl-agent.new /opt/homectl-agent/homectl-agent
systemctl start homectl-agent
systemctl status homectl-agent
```

OpenWrt：

```sh
/etc/init.d/homectl-agent stop
cp ./homectl-agent /opt/homectl-agent/.homectl-agent.new
chmod 755 /opt/homectl-agent/.homectl-agent.new
mv -f /opt/homectl-agent/.homectl-agent.new /opt/homectl-agent/homectl-agent
/etc/init.d/homectl-agent start
/etc/init.d/homectl-agent status
```

## 7. 删除设备后的重新注册

Web 中删除设备会吊销它的长期 Device Token。只有确实要重新添加时才执行：

```sh
systemctl stop homectl-agent
rm -f /opt/homectl-agent/state.json
```

然后在 Web 重新填写“首次显示名称”、生成新的 Enrollment Token，写入 `/opt/homectl-agent/config.json`，再启动：

```sh
systemctl start homectl-agent
```

OpenWrt 把上述 `systemctl stop/start` 换成 `/etc/init.d/homectl-agent stop/start`。

## 8. Cloudflare Access

如果 `/agent/ws` 被 Cloudflare Access Service Token 保护，可配置：

```json
"cloudflare_access": {
  "client_id": "YOUR_CLIENT_ID",
  "client_secret": "YOUR_CLIENT_SECRET"
}
```

Agent 会在 WSS 握手中发送对应 Header。两项是敏感凭据，保持 `config.json` 为 `0600`。
