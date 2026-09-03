# HomeCTL

<img width="1194" height="1046" alt="demo09" src="https://github.com/user-attachments/assets/9f6ae757-5846-46d5-b94d-4124bf6a78ca" />

HomeCTL 是一个面向家庭网络、实验室和小型私有环境的轻量 Linux 远程管理面板。它由一个 Go Server 和每台受控 Linux 主机上的 Go Agent 组成。

Agent **主动**建立到 Server 的 WSS 长连接，因此家庭宽带没有公网 IP、Agent 主机无法开放入站端口时也可以使用。状态上报、命令、终端和按需文件操作复用同一条连接。

## 功能

- 多 Linux Agent 管理，每台设备使用独立长期 Device Token
- 六类可选指标卡片：CPU/温度、内存、存储、网络吞吐、进程状态和磁盘 I/O
- 主机名、OS、Kernel、架构、CPU、IP、Uptime、最后心跳
- 心跳超时离线判定
- Web 命令执行、退出码、stdout/stderr 和结果自动收起
- 重启 / 关机
- 内置 xterm.js + FitAddon 的 PTY Web Terminal，不依赖第三方 CDN；支持拖动、缩放、最大化和自动适配行列数
- 设备自定义名称、手柄拖动排序、按设备选择指标卡片、删除和一次性 Enrollment Token
- 暗色 / 亮色 / 跟随系统主题
- 用户名 + 密码认证，可选 TOTP 两步验证
- 两阶段 TOTP 登录：密码通过后才显示 6 位验证码步骤
- 可选“记住用户名”和“保持登录”
- 可选文件浏览器：浏览、上传、下载、新建目录、重命名、删除文件/空目录
- SQLite-only Server 持久化
- Docker Compose、纯二进制、Cloudflare Tunnel 均可按需使用
- Agent 提供无 Docker 依赖的静态二进制，并附 systemd 与 OpenWrt procd 启动文件
- 提供普通二进制和可选 UPX 压缩版 Release 产物

## 架构

```text
Browser
   |
HTTPS / WSS
   |
HomeCTL Server + SQLite
   ^
   |
outbound WSS
   |
Linux Agent (root + systemd / OpenWrt procd / manual)
```

Cloudflare Tunnel、Caddy、Nginx 等都只是可选的公网/HTTPS 入口，不是 HomeCTL 本身的运行依赖。

## 快速开始

选择一种 Server 部署方式：

- **Docker Compose**：见 [docs/INSTALL_DOCKER.md](docs/INSTALL_DOCKER.md)
- **纯二进制 / systemd**：见 [docs/INSTALL_BINARY.md](docs/INSTALL_BINARY.md)

Server 启动后，再按 [docs/AGENT.md](docs/AGENT.md) 添加 Agent。

面板操作见 [docs/WEB_PANEL.md](docs/WEB_PANEL.md)，完整参数说明见 [docs/CONFIGURATION.md](docs/CONFIGURATION.md)。如果使用 Cloudflare Tunnel，见 [docs/CLOUDFLARE.md](docs/CLOUDFLARE.md)。

## 推荐默认值

```text
Agent heartbeat          10s
Server offline timeout   25s
Web refresh               5s
File browser              disabled
Session                   24h
Keep signed in            disabled by default; 7d when selected
TOTP                      optional
```

## 安全说明

- 新密码最少 16 个字符，不强制特殊字符；支持长密码和 Unicode。
- 密码使用 Argon2id PHC 字符串存储；数据库只保存不可逆密码哈希。
- Session 和设备认证原始 Token 不写入 SQLite，只保存不可逆摘要；浏览器 Session 使用 HttpOnly Cookie。
- TOTP 登录使用 Server-side、短期、一次性 pre-auth，第二步不再重复提交密码。
- 文件浏览器通过 Go `os.Root` 将操作限制在 `file_browser_root` 内；默认关闭。
- Agent 以 root 运行，因此启用 Web Terminal、命令执行或根目录文件浏览都等价于授予控制台 root 级管理能力。

部署到公网前请阅读 [docs/SECURITY.md](docs/SECURITY.md)。

## 文档

- [Docker Compose 部署](docs/INSTALL_DOCKER.md)
- [纯二进制部署](docs/INSTALL_BINARY.md)
- [Agent 安装与升级](docs/AGENT.md)
- [Web 面板使用](docs/WEB_PANEL.md)
- [完整配置参数](docs/CONFIGURATION.md)
- [Cloudflare Tunnel](docs/CLOUDFLARE.md)
- [安全模型](docs/SECURITY.md)
- [版本升级](docs/UPGRADING.md)
- [普通版与 UPX 版](docs/RELEASES.md)
- [故障排查](docs/TROUBLESHOOTING.md)

## License

见 [LICENSE](LICENSE)。
