# 升级与回滚

升级 Server 前应同时保留当前二进制或镜像标签、`config.json` 和 SQLite 数据库；升级 Agent 时必须保留 `state.json`。

## 升级到 v2.0.0

建议先升级 Server，再逐台升级 Agent。v2.0.0 会把数据库 schema 升级到版本 2，为设备增加持久排序和按设备指标策略；首次迁移会保留旧版按名称展示的既有顺序。升级前务必完成下面的数据库备份。

新 Server 与旧 Agent 可以继续完成心跳和既有操作，但旧 Agent 不支持“隐藏卡片后停止采集”。新 Agent 连接旧 Server 时会安全回退为采集全部六类指标。Server 与 Agent 都升级后，按需采集、网络/进程/磁盘 I/O 和温度指标才完整生效。

## Server 数据库备份

为了让 SQLite 主文件与 WAL 保持一致，最简单可靠的方式是先停止 Server 再复制数据库：

```bash
systemctl stop homectl-server
cp /opt/homectl-server/data/homectl.db /opt/homectl-server/data/homectl.db.bak
systemctl start homectl-server
```

Docker 部署可先执行 `docker compose stop homectl`，在宿主机备份 `./data`，然后执行 `docker compose start homectl`。如果使用 SQLite 自带的在线 `.backup` 命令，也可以在服务运行时创建一致性备份。

Server 启动时会在事务中补齐当前数据库结构。已有 bcrypt 管理员密码仍可登录，并会在成功验证后迁移成 Argon2id。若数据库的 schema 版本高于当前程序支持的版本，Server 会在执行结构修改前拒绝启动，避免旧程序改写新数据库。

## Docker Server

明确指定要部署的版本标签，不要依赖本地缓存：

```bash
docker compose pull homectl
docker compose up -d homectl
docker compose logs -f homectl
```

确认 `/healthz`、管理员登录和至少一台 Agent 的心跳正常后，再清理旧镜像。

## 二进制 Server

```bash
systemctl stop homectl-server
cp ./homectl-server /opt/homectl-server/.homectl-server.new
chmod 755 /opt/homectl-server/.homectl-server.new
mv -f /opt/homectl-server/.homectl-server.new /opt/homectl-server/homectl-server
systemctl start homectl-server
systemctl status homectl-server
```

## Agent

不要删除或覆盖 `/opt/homectl-agent/state.json`。systemd：

```bash
systemctl stop homectl-agent
cp ./homectl-agent /opt/homectl-agent/.homectl-agent.new
chmod 755 /opt/homectl-agent/.homectl-agent.new
mv -f /opt/homectl-agent/.homectl-agent.new /opt/homectl-agent/homectl-agent
systemctl start homectl-agent
```

OpenWrt procd：

```sh
/etc/init.d/homectl-agent stop
cp ./homectl-agent /opt/homectl-agent/.homectl-agent.new
chmod 755 /opt/homectl-agent/.homectl-agent.new
mv -f /opt/homectl-agent/.homectl-agent.new /opt/homectl-agent/homectl-agent
/etc/init.d/homectl-agent start
```

## 验证与回滚

升级后检查：

- Server `/healthz` 返回 `ok`，管理员密码和 TOTP 登录正常。
- Agent 恢复在线，已有自定义设备名没有被主机名覆盖。
- 设备拖动顺序、每台设备的“指标卡片”选择在刷新页面后仍保留。
- 实际启用的命令、Terminal 和文件功能各做一次最小验证。
- Server/Agent 日志中没有持续重连、数据库或权限错误。

需要回滚时，先停止服务，再恢复成套保存的旧二进制（或旧镜像标签）、配置和数据库备份。不要让旧 Server 打开已经由更高 schema 版本运行过、但没有对应备份的数据库。
