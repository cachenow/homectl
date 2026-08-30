# HomeCTL Server deployment

1. Edit `config.json` and replace `admin_password` / `enroll_token`.
2. Edit `docker-compose.yml` and replace `PASTE_YOUR_CLOUDFLARE_TUNNEL_TOKEN_HERE` with the Cloudflare remotely-managed Tunnel token.
3. Start:

```bash
docker compose up -d
docker compose logs -f
```

In the Cloudflare dashboard, create the Public Hostname and set its Service URL to:

```text
http://localhost:8080
```

`cloudflared` shares the HomeCTL container's network namespace, so `localhost:8080` is intentionally valid here.

The HomeCTL HTTP service is also bound to host loopback only (`127.0.0.1:8080`) for local diagnostics.

> The Tunnel token is intentionally stored directly in `docker-compose.yml`. If this repository is public, do not commit a real production token; only put the real token on the deployment server.
