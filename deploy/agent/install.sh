#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "run this installer as root" >&2
  exit 1
fi

SRC_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
INSTALL_DIR=/opt/homectl-agent

if [ ! -f "$SRC_DIR/homectl-agent" ]; then
  echo "missing $SRC_DIR/homectl-agent" >&2
  exit 1
fi
if [ ! -f "$SRC_DIR/config.json" ]; then
  echo "missing $SRC_DIR/config.json; edit it before installing" >&2
  exit 1
fi

install -d -m 0700 "$INSTALL_DIR"
install -m 0755 "$SRC_DIR/homectl-agent" "$INSTALL_DIR/homectl-agent"
if [ ! -f "$INSTALL_DIR/config.json" ]; then
  install -m 0600 "$SRC_DIR/config.json" "$INSTALL_DIR/config.json"
else
  echo "keeping existing $INSTALL_DIR/config.json"
fi
install -m 0644 "$SRC_DIR/homectl-agent.service" /etc/systemd/system/homectl-agent.service
systemctl daemon-reload
systemctl enable --now homectl-agent
systemctl status --no-pager homectl-agent || true
