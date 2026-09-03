# HomeCTL Server 部署包

1. 编辑 `config.json`，至少替换 `admin_password`。
2. 如果 Compose 中包含 cloudflared，把 Tunnel Token 替换成自己的值；不使用 Cloudflare 时删除 `cloudflared` service。
3. 启动：

```bash
docker compose up -d
docker compose logs -f homectl
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

完整文档请查看项目仓库的 `docs/INSTALL_DOCKER.md` 和 `docs/CONFIGURATION.md`。
