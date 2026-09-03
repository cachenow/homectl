# HomeCTL Agent

1. 在 HomeCTL Web 的“设备管理”中生成一次性 Enrollment Token。
2. 编辑 `config.json`：填写 `server` 和 `enroll_token`。
3. 安装：

```bash
./install.sh
```

检查：

```bash
# systemd
systemctl status homectl-agent
journalctl -u homectl-agent -f

# OpenWrt procd
/etc/init.d/homectl-agent status
logread -f
```

安装脚本会自动识别正在运行的 systemd 或 OpenWrt procd。OpenWrt 上还需把 `config.json` 的 `shell` 改成实际存在的绝对路径，通常为 `/bin/ash`。

首次注册成功后 `state.json` 会保存该设备独立的长期 Device Token，此后可清空 `config.json` 中的一次性 `enroll_token`。

完整说明请查看项目仓库 `docs/AGENT.md` 和 `docs/CONFIGURATION.md`。
