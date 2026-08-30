# HomeCTL Agent deployment

标准安装目录：

```text
/opt/homectl-agent/
├── homectl-agent
├── config.json
└── state.json       # 首次运行自动创建
```

先在 HomeCTL Web → “添加设备”生成一个 **一次性 Agent Token**，填写到 `config.json` 的 `enroll_token`。

然后以 root 运行：

```bash
./install.sh
```

无需环境变量。

首次注册成功后 `state.json` 会保存本机独立 Device Token。此后可以把 `config.json` 中旧的一次性 `enroll_token` 清空。

默认：

```text
heartbeat_interval 10s
handshake_timeout  15s
write_timeout      10s
```


存储信息分成两部分：物理容量直接读取 `/sys/class/block/*/size` 并按底层设备去重；文件系统使用率通过 `/proc/self/mountinfo` + `/sys/dev/block` + `statfs()` 汇总本地块设备。这样不会把 `/` 根分区的大小误当成整块磁盘容量。

文件系统统计默认排除 loop/zram/ram；需要调整时可在 `config.json` 中设置：

```json
"disk_exclude_device_prefixes": ["/dev/loop", "/dev/zram", "/dev/ram"]
```

文件浏览器默认关闭，不会产生额外周期性文件系统负载。需要时设置：

```json
"file_browser_enabled": true,
"file_browser_root": "/"
```

查看日志：

```bash
journalctl -u homectl-agent -f
```

升级：替换 `/opt/homectl-agent/homectl-agent` 后：

```bash
systemctl restart homectl-agent
```
