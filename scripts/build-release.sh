#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:-dev}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST="$ROOT/dist"

if [[ ! "$VERSION" =~ ^(dev|v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?)$ ]]; then
  echo "version must be dev or a release such as v1.0.0 or v1.0.0-rc.1" >&2
  exit 2
fi

cd "$ROOT"
LDFLAGS="-s -w -X main.version=${VERSION}"

rm -rf "$DIST"
mkdir -p "$DIST"

build_bin() {
  local package=$1 output=$2 goarch=$3 goarm=${4:-}
  echo "==> building $output"
  if [[ -n "$goarm" ]]; then
    CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" \
      go build -buildvcs=false -trimpath -ldflags="$LDFLAGS" -o "$output" "$package"
  else
    CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
      go build -buildvcs=false -trimpath -ldflags="$LDFLAGS" -o "$output" "$package"
  fi
}

try_upx() {
  local src=$1 dst=$2 smoke=${3:-false}
  command -v upx >/dev/null 2>&1 || return 1
  cp "$src" "$dst"
  chmod +x "$dst"
  echo "==> UPX compressing $(basename "$dst")"
  if ! upx --best --lzma "$dst" >/dev/null; then
    rm -f "$dst"
    return 1
  fi
  if ! upx -t "$dst" >/dev/null; then
    rm -f "$dst"
    return 1
  fi
  if [[ "$smoke" == "true" ]]; then
    if ! "$dst" -version >/dev/null 2>&1; then
      rm -f "$dst"
      return 1
    fi
  fi
  return 0
}

package_agent() {
  local label=$1 goarch=$2 goarm=${3:-}
  local work="$DIST/.agent-$label"
  mkdir -p "$work"
  build_bin ./cmd/agent "$work/homectl-agent" "$goarch" "$goarm"
  cp deploy/agent/config.example.json "$work/config.json"
  chmod 0600 "$work/config.json"
  cp deploy/agent/homectl-agent.service "$work/homectl-agent.service"
  cp deploy/agent/homectl-agent.openwrt.init "$work/homectl-agent.openwrt.init"
  cp deploy/agent/install.sh "$work/install.sh"
  cp deploy/agent/README.md "$work/README.md"
  chmod 0755 "$work/install.sh" "$work/homectl-agent" "$work/homectl-agent.openwrt.init"
  tar -C "$work" -czf "$DIST/homectl-agent-${VERSION}-linux-${label}.tar.gz" .

  local upxwork="$DIST/.agent-${label}-upx"
  cp -a "$work" "$upxwork"
  local smoke=false
  if [[ "$label" == "amd64" && "$(uname -m)" == "x86_64" ]]; then smoke=true; fi
  if try_upx "$work/homectl-agent" "$upxwork/homectl-agent" "$smoke"; then
    tar -C "$upxwork" -czf "$DIST/homectl-agent-${VERSION}-linux-${label}-upx.tar.gz" .
  else
    echo "==> UPX unavailable/unsupported for agent $label; skipping optional UPX asset"
  fi
  rm -rf "$work" "$upxwork"
}

package_server() {
  local label=$1 goarch=$2
  local raw="$DIST/homectl-server-${VERSION}-linux-${label}"
  build_bin ./cmd/server "$raw" "$goarch"
  chmod +x "$raw"

  local work="$DIST/.server-$label"
  mkdir -p "$work/data" "$work/docs"
  chmod 0700 "$work/data"
  cp "$raw" "$work/homectl-server"
  cp deploy/server/config.binary.example.json "$work/config.json"
  chmod 0600 "$work/config.json"
  cp deploy/server/homectl-server.service "$work/homectl-server.service"
  sed 's#(../../docs/#(docs/#g' deploy/server/README.binary.md > "$work/README.md"
  cp docs/*.md "$work/docs/"
  tar -C "$work" -czf "$DIST/homectl-server-${VERSION}-linux-${label}.tar.gz" .

  local upxraw="$DIST/homectl-server-${VERSION}-linux-${label}-upx"
  local smoke=false
  if [[ "$label" == "amd64" && "$(uname -m)" == "x86_64" ]]; then smoke=true; fi
  if try_upx "$raw" "$upxraw" "$smoke"; then
    local upxwork="$DIST/.server-${label}-upx"
    cp -a "$work" "$upxwork"
    cp "$upxraw" "$upxwork/homectl-server"
    tar -C "$upxwork" -czf "$DIST/homectl-server-${VERSION}-linux-${label}-upx.tar.gz" .
    rm -rf "$upxwork"
  else
    echo "==> UPX unavailable/unsupported for server $label; skipping optional UPX asset"
  fi
  rm -rf "$work"
}

package_agent amd64 amd64
package_agent arm64 arm64
package_agent armv7 arm 7
package_server amd64 amd64
package_server arm64 arm64

# Ready-to-run Docker deployment bundle.
if [[ -n "${GITHUB_REPOSITORY:-}" ]]; then
  image="ghcr.io/${GITHUB_REPOSITORY,,}"
else
  image="ghcr.io/REPLACE_WITH_OWNER/REPLACE_WITH_REPO"
fi
work="$DIST/.server-deploy"
mkdir -p "$work/data" "$work/docs"
chmod 0700 "$work/data"
cp deploy/server/config.example.json "$work/config.json"
chmod 0600 "$work/config.json"
cp deploy/server/README.md "$work/README.md"
cp docs/*.md "$work/docs/"
sed \
  -e "s|__IMAGE__|${image}|g" \
  -e "s|__TAG__|${VERSION}|g" \
  deploy/server/docker-compose.release.yml > "$work/docker-compose.yml"
tar -C "$work" -czf "$DIST/homectl-server-deploy-${VERSION}.tar.gz" .
rm -rf "$work"

(
  cd "$DIST"
  sha256sum homectl-* | sort > SHA256SUMS
)

echo "Release assets written to $DIST"
