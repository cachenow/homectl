# HomeCTL Agent deployment

The normal installation directory is:

```text
/opt/homectl-agent/
├── homectl-agent
├── config.json
└── state.json       # created automatically after first start
```

Edit `config.json`, then run `./install.sh` as root. No environment variables are required.

Logs:

```bash
journalctl -u homectl-agent -f
```

Upgrade: replace `/opt/homectl-agent/homectl-agent` with the new binary and run:

```bash
systemctl restart homectl-agent
```
