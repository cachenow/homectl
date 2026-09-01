# HomeCTL Server 部署包

本包解压后即可部署，不需要再复制示例文件：

1. 编辑本目录的完整 `config.json`，至少替换 `admin_password`，并保持权限为 `0600`。
2. 如需 Cloudflare Tunnel，替换 `docker-compose.yml` 中的 Tunnel Token；不使用时删除整个 `cloudflared` service。
3. 启动并检查：

```bash
docker compose up -d
docker compose ps
docker compose logs --tail=100 homectl
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

首次登录成功后，把 `config.json` 中的 `admin_password` 改成空字符串，再执行 `docker compose restart homectl` 并确认仍能登录。

完整文档请查看项目仓库的 `docs/INSTALL_DOCKER.md` 和 `docs/CONFIGURATION.md`。
