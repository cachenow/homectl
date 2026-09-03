# Agent 安装与升级

Agent 必须能够主动访问 HomeCTL Server 的 `ws://` 或 `wss://` 地址。它不需要公网 IP，也不需要开放任何入站端口。

由于 Agent 要执行重启、关机、PTY Shell 和可选文件管理，因此标准部署以 root 运行。

## 1. 生成一次性 Enrollment Token

Server Web 控制台：

```text
设备管理 → 添加设备 → 生成一次性 Agent Token
```

每台新设备单独生成一个。Token 默认 30 分钟有效，只能成功使用一次。

## 2. 解压并完成首次配置

普通版和 UPX 版任选其一，不要同时安装；空间不紧张时推荐普通版。先把下载的 Release 包解压到一个临时工作目录并进入该目录：

```bash
mkdir -p /tmp/homectl-agent-install
tar -xzf /path/to/homectl-agent-vX.Y.Z-linux-amd64.tar.gz -C /tmp/homectl-agent-install
cd /tmp/homectl-agent-install
```

请按设备架构替换归档文件名。解压后直接编辑同目录的 `config.json`，至少填写 `server` 和刚生成的 `enroll_token`：

```json
{
  "server": "wss://panel.example.com/agent/ws",
  "name": "",
  "enroll_token": "这里填刚生成的一次性Token",
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
  "disk_exclude_device_prefixes": ["/dev/loop", "/dev/zram", "/dev/ram"],
  "cloudflare_access": {
    "client_id": "",
    "client_secret": ""
  },
  "tls": {
    "insecure_skip_verify": false
  }
}
```

OpenWrt 通常没有 Bash，此时安装前把 `shell` 改为实际存在的 `/bin/ash`。不需要命令、终端或文件管理的设备，也应在此时关闭对应开关以减少权限面。全部参数见 [CONFIGURATION.md](CONFIGURATION.md)。

## 3. 安装并启动

确认 `config.json` 已保存后，在刚才的解压目录执行：

```bash
./install.sh
```

安装器会自动完成以下操作，不需要再手工重复复制：

1. 创建权限为 `0700` 的 `/opt/homectl-agent`。
2. 安装二进制，并在首次安装时复制权限为 `0600` 的 `config.json`。
3. 检测 systemd 或 OpenWrt procd，安装对应服务文件并设置开机自启。
4. 立即启动 Agent。

最终目录会是：

```text
/opt/homectl-agent/
├── homectl-agent
├── config.json
└── state.json       # 首次注册成功后自动生成，权限 0600
```

如果 `/opt/homectl-agent/config.json` 已存在，安装器会保留它，避免升级时覆盖配置。只有明确不使用安装器时，systemd 用户才需要执行下面的手工流程：

```bash
mkdir -p /opt/homectl-agent
chmod 700 /opt/homectl-agent
install -m 755 homectl-agent /opt/homectl-agent/homectl-agent
install -m 600 config.json /opt/homectl-agent/config.json
cp homectl-agent.service /etc/systemd/system/homectl-agent.service
chmod 644 /etc/systemd/system/homectl-agent.service
systemctl daemon-reload
systemctl enable --now homectl-agent
```

不要在同一次安装中同时运行自动安装器和手工流程。

## 4. 验证首次注册

systemd 设备执行：

```bash
systemctl status homectl-agent
journalctl -u homectl-agent -f
```

OpenWrt 设备执行：

```sh
/etc/init.d/homectl-agent status
logread -f
```

设备在 Web 面板显示在线后，`/opt/homectl-agent/state.json` 中已经保存 Device ID 和该设备独立的长期 Device Token。此时可以把**已安装的** `/opt/homectl-agent/config.json` 中 `enroll_token` 的值清空；Agent 后续重启会使用 `state.json`，不再重复注册。

如果设备没有上线，请先看上述日志，再核对 Server URL、时间、DNS、TLS 证书和一次性 Token 是否仍在有效期内。不要反复删除目录或重新运行两套安装步骤。

## 5. OpenWrt 与小型设备注意事项

Agent 是 `CGO_ENABLED=0` 构建的单文件二进制，不依赖 Docker 或目标设备上的 Go 运行时。当前 Release 提供 `linux-amd64`、`linux-arm64` 和 `linux-armv7`；MIPS、MIPS64 等 OpenWrt 设备不能使用这三个现成产物，需要从源码按对应 `GOARCH` 自行构建并在实机验证。

资源紧张时优先关闭不需要的高权限功能：

```json
{
  "exec_enabled": false,
  "terminal_enabled": false,
  "file_browser_enabled": false
}
```

普通二进制启动更快、兼容性更稳；只有闪存空间确实紧张时再使用可选 `-upx` 包。

为避免误操作或异常控制端耗尽小设备资源，Agent 内置并发保护：同一时刻最多执行 1 个系统动作、4 条普通命令、4 个 PTY、4 个上传、4 个下载和 8 个短文件操作。达到上限的新增请求会明确失败，不会无界创建进程或缓冲区。

## 6. 升级

普通升级不会修改 `config.json` 或 `state.json`：

```bash
systemctl stop homectl-agent
cp ./homectl-agent /opt/homectl-agent/homectl-agent
chmod 755 /opt/homectl-agent/homectl-agent
systemctl start homectl-agent
```

OpenWrt：

```sh
/etc/init.d/homectl-agent stop
cp ./homectl-agent /opt/homectl-agent/.homectl-agent.new
chmod 755 /opt/homectl-agent/.homectl-agent.new
mv -f /opt/homectl-agent/.homectl-agent.new /opt/homectl-agent/homectl-agent
/etc/init.d/homectl-agent start
```

## 7. 重新注册设备

如果在 Web 中删除了一台设备，Server 已经吊销它的长期 Token。要重新添加：

```bash
systemctl stop homectl-agent
rm -f /opt/homectl-agent/state.json
```

在 Web 生成新的 Enrollment Token，写入 `config.json`，然后：

```bash
systemctl start homectl-agent
```

OpenWrt 请把上面的 `systemctl stop/start` 换成 `/etc/init.d/homectl-agent stop/start`。

## 8. Cloudflare Access

如果 `/agent/ws` 被 Cloudflare Access Service Token 保护，可设置：

```json
"cloudflare_access": {
  "client_id": "YOUR_CLIENT_ID",
  "client_secret": "YOUR_CLIENT_SECRET"
}
```

Agent 会在 WSS 握手中发送对应 Cloudflare Access headers。
