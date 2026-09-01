# HomeCTL Agent

本目录是可直接安装的完整 Release 包。按顺序操作：

1. 确认当前包与设备架构一致。
2. 在 HomeCTL Web 的“设备管理”中填写可选的“首次显示名称”，再生成一次性 Enrollment Token。
3. 编辑本目录的完整 `config.json`，至少填写 `server` 和 `enroll_token`。OpenWrt 通常还要把 `shell` 改成 `/bin/ash`。
4. 设置权限并安装：

```bash
chmod 600 config.json
./install.sh
```

检查：

```bash
# systemd
systemctl status homectl-agent
journalctl -u homectl-agent -n 50 --no-pager

# OpenWrt procd
/etc/init.d/homectl-agent status
logread -e homectl-agent
```

安装脚本会创建 `/opt/homectl-agent`，复制二进制和首次配置，自动识别正在运行的 systemd 或 OpenWrt procd，安装对应服务并设置开机自启。

首次注册成功后，确认 Web 中设备在线且 `/opt/homectl-agent/state.json` 已生成，再把 `/opt/homectl-agent/config.json` 中的一次性 `enroll_token` 清空并重启服务。不要删除 `state.json`，它保存该设备的长期 Device Token。

完整说明请查看项目仓库 `docs/AGENT.md` 和 `docs/CONFIGURATION.md`。
