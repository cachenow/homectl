#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:-dev}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST="$ROOT/dist"
cd "$ROOT"
LDFLAGS="-s -w -X main.version=${VERSION}"

rm -rf "$DIST"
mkdir -p "$DIST"

build_bin() {
  local package=$1 output=$2 goarch=$3 goarm=${4:-}
  echo "==> building $output"
  if [[ -n "$goarm" ]]; then
    CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" \
      go build -trimpath -ldflags="$LDFLAGS" -o "$output" "$package"
  else
    CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
      go build -trimpath -ldflags="$LDFLAGS" -o "$output" "$package"
  fi
}

package_agent() {
  local label=$1 goarch=$2 goarm=${3:-}
  local work="$DIST/.agent-$label"
  mkdir -p "$work"
  build_bin ./cmd/agent "$work/homectl-agent" "$goarch" "$goarm"
  cp deploy/agent/config.example.json "$work/config.json"
  chmod 0600 "$work/config.json"
  cp deploy/agent/homectl-agent.service "$work/homectl-agent.service"
  cp deploy/agent/install.sh "$work/install.sh"
  cp deploy/agent/README.md "$work/README.md"
  chmod +x "$work/install.sh" "$work/homectl-agent"
  tar -C "$work" -czf "$DIST/homectl-agent-${VERSION}-linux-${label}.tar.gz" .
  rm -rf "$work"
}

package_agent amd64 amd64
package_agent arm64 arm64
package_agent armv7 arm 7

build_bin ./cmd/server "$DIST/homectl-server-${VERSION}-linux-amd64" amd64
build_bin ./cmd/server "$DIST/homectl-server-${VERSION}-linux-arm64" arm64

# A ready-to-run server bundle is generated automatically in GitHub Actions.
# It points to the GHCR image belonging to the current repository.
if [[ -n "${GITHUB_REPOSITORY:-}" ]]; then
  image="ghcr.io/${GITHUB_REPOSITORY,,}"
else
  image="ghcr.io/REPLACE_WITH_OWNER/REPLACE_WITH_REPO"
fi
work="$DIST/.server-deploy"
mkdir -p "$work/data"
cp deploy/server/config.example.json "$work/config.json"
chmod 0600 "$work/config.json"
cp deploy/server/README.md "$work/README.md"
sed \
  -e "s|__IMAGE__|${image}|g" \
  -e "s|__TAG__|${VERSION}|g" \
  deploy/server/docker-compose.release.yml > "$work/docker-compose.yml"
tar -C "$work" -czf "$DIST/homectl-server-deploy-${VERSION}.tar.gz" .
rm -rf "$work"

(
  cd "$DIST"
  sha256sum homectl-* > SHA256SUMS
)

echo "Release assets written to $DIST"
