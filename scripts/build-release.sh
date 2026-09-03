#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:-dev}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST="$ROOT/dist"

if [[ ! "$VERSION" =~ ^(dev|v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?)$ ]]; then
  echo "version must be dev or a release such as v2.0.0 or v2.0.0-rc.1" >&2
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

# Archive only the declared release entries. Besides preventing accidental
# inclusion of build leftovers, this avoids transient workspace synchronizer
# files from changing a staging directory while tar is reading `.`.
create_archive() {
  local source=$1 archive=$2
  shift 2
  tar --no-recursion -C "$source" -czf "$archive" "$@"
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
  local entries=(README.md config.json homectl-agent homectl-agent.openwrt.init homectl-agent.service install.sh)
  create_archive "$work" "$DIST/homectl-agent-${VERSION}-linux-${label}.tar.gz" "${entries[@]}"

  local upxwork="$DIST/.agent-${label}-upx"
  mkdir -p "$upxwork"
  cp "$work"/README.md "$work"/config.json "$work"/homectl-agent "$work"/homectl-agent.openwrt.init "$work"/homectl-agent.service "$work"/install.sh "$upxwork"/
  chmod 0600 "$upxwork/config.json"
  chmod 0755 "$upxwork/install.sh" "$upxwork/homectl-agent" "$upxwork/homectl-agent.openwrt.init"
  local smoke=false
  if [[ "$label" == "amd64" && "$(uname -m)" == "x86_64" ]]; then smoke=true; fi
  if try_upx "$work/homectl-agent" "$upxwork/homectl-agent" "$smoke"; then
    create_archive "$upxwork" "$DIST/homectl-agent-${VERSION}-linux-${label}-upx.tar.gz" "${entries[@]}"
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
  local entries=(README.md config.json homectl-server homectl-server.service data)
  local doc
  for doc in "$work"/docs/*.md; do
    entries+=("docs/${doc##*/}")
  done
  create_archive "$work" "$DIST/homectl-server-${VERSION}-linux-${label}.tar.gz" "${entries[@]}"

  local upxraw="$DIST/homectl-server-${VERSION}-linux-${label}-upx"
  local smoke=false
  if [[ "$label" == "amd64" && "$(uname -m)" == "x86_64" ]]; then smoke=true; fi
  if try_upx "$raw" "$upxraw" "$smoke"; then
    local upxwork="$DIST/.server-${label}-upx"
    mkdir -p "$upxwork/data" "$upxwork/docs"
    cp "$work"/README.md "$work"/config.json "$work"/homectl-server.service "$upxwork"/
    cp "$work"/docs/*.md "$upxwork/docs"/
    chmod 0700 "$upxwork/data"
    chmod 0600 "$upxwork/config.json"
    cp "$upxraw" "$upxwork/homectl-server"
    create_archive "$upxwork" "$DIST/homectl-server-${VERSION}-linux-${label}-upx.tar.gz" "${entries[@]}"
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
deploy_entries=(README.md config.json docker-compose.yml data)
for doc in "$work"/docs/*.md; do
  deploy_entries+=("docs/${doc##*/}")
done
create_archive "$work" "$DIST/homectl-server-deploy-${VERSION}.tar.gz" "${deploy_entries[@]}"
rm -rf "$work"

(
  cd "$DIST"
  sha256sum homectl-* | sort > SHA256SUMS
)

echo "Release assets written to $DIST"
