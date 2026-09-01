#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "run this installer as root" >&2
  exit 1
fi

SRC_DIR=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
INSTALL_DIR=/opt/homectl-agent

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  SERVICE_MANAGER=systemd
elif [ -x /sbin/procd ] && [ -d /etc/init.d ]; then
  SERVICE_MANAGER=openwrt
else
  echo "no supported service manager found (systemd or OpenWrt procd)" >&2
  echo "install the binary and run it with -config manually, or use the documented init setup" >&2
  exit 1
fi

if [ ! -f "$SRC_DIR/homectl-agent" ]; then
  echo "missing $SRC_DIR/homectl-agent" >&2
  exit 1
fi
if [ ! -f "$SRC_DIR/config.json" ]; then
  echo "missing $SRC_DIR/config.json; edit it before installing" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
chmod 0700 "$INSTALL_DIR"
cp "$SRC_DIR/homectl-agent" "$INSTALL_DIR/.homectl-agent.new"
chmod 0755 "$INSTALL_DIR/.homectl-agent.new"
mv -f "$INSTALL_DIR/.homectl-agent.new" "$INSTALL_DIR/homectl-agent"
if [ ! -f "$INSTALL_DIR/config.json" ]; then
  cp "$SRC_DIR/config.json" "$INSTALL_DIR/config.json"
  chmod 0600 "$INSTALL_DIR/config.json"
else
  echo "keeping existing $INSTALL_DIR/config.json"
fi

if [ "$SERVICE_MANAGER" = systemd ]; then
  cp "$SRC_DIR/homectl-agent.service" /etc/systemd/system/homectl-agent.service
  chmod 0644 /etc/systemd/system/homectl-agent.service
  systemctl daemon-reload
  systemctl enable --now homectl-agent
  systemctl status --no-pager homectl-agent || true
else
  cp "$SRC_DIR/homectl-agent.openwrt.init" /etc/init.d/homectl-agent
  chmod 0755 /etc/init.d/homectl-agent
  /etc/init.d/homectl-agent enable
  /etc/init.d/homectl-agent restart
  /etc/init.d/homectl-agent status || true
fi
