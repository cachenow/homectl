# Agent 安装与升级

Agent 必须能够主动访问 HomeCTL Server 的 `ws://` 或 `wss://` 地址。它不需要公网 IP，也不需要开放任何入站端口。

由于 Agent 要执行重启、关机、PTY Shell 和可选文件管理，因此标准部署以 root 运行。

## 1. 生成一次性 Enrollment Token

Server Web 控制台：

```text
设备管理 → 添加设备 → 生成一次性 Agent Token
```

每台新设备单独生成一个。Token 默认 30 分钟有效，只能成功使用一次。

## 2. 准备目录

Release Agent 包可直接解压。标准安装目录：

```text
/opt/homectl-agent/
├── homectl-agent
├── config.json
└── state.json       # 首次启动自动生成
```

普通版和 UPX 版任选其一，不要同时安装。空间不紧张时推荐普通版。

## 3. 配置

示例：

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

全部参数见 [CONFIGURATION.md](CONFIGURATION.md)。

首次注册成功后 `state.json` 会保存：

- Device ID
- 该设备独立的长期 Device Token

文件权限为 `0600`。之后 Agent 不再需要一次性 Enrollment Token，可以把 `config.json` 中的 `enroll_token` 清空。

## 4. systemd 安装

Release Agent 包中：

```bash
./install.sh
```

或者手动：

```bash
mkdir -p /opt/homectl-agent
cp homectl-agent config.json /opt/homectl-agent/
chmod 755 /opt/homectl-agent/homectl-agent
chmod 600 /opt/homectl-agent/config.json
cp homectl-agent.service /etc/systemd/system/homectl-agent.service
systemctl daemon-reload
systemctl enable --now homectl-agent
```

检查：

```bash
systemctl status homectl-agent
journalctl -u homectl-agent -f
```

## 5. OpenWrt / procd

Release 包中的 `install.sh` 会自动识别 OpenWrt procd，并把 `homectl-agent.openwrt.init` 安装到 `/etc/init.d/homectl-agent`。OpenWrt 通常没有 Bash，安装前把 `config.json` 改为：

```json
"shell": "/bin/ash"
```

然后执行：

```sh
./install.sh
/etc/init.d/homectl-agent status
logread -e homectl-agent
```

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
