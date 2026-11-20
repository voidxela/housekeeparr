#!/usr/bin/env bash
# Reverse-proxy acceptance test (TestNginxProxy).
#
# Puts Debian trixie's nginx, extracted from a pinned, checksum-verified
# package into the artifact cache (never installed), in front of
# Housekeeparr serving synthetic demo data under the documented shared-host
# configuration: loopback listener, password, trusted_proxy, base_path and
# allowed_hosts. nginx runs as the current user on a free loopback port with
# a temporary prefix and a throwaway self-signed certificate, using the exact
# location blocks from packaging/README.md.
#
# Usage: scripts/it/nginx.sh
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
if ! command -v go >/dev/null 2>&1; then
	if [ -x "$HOME/.local/go/bin/go" ]; then export PATH="$HOME/.local/go/bin:$PATH"; else echo "go not found on PATH" >&2; exit 2; fi
fi
for tool in apt-get dpkg-deb sha256sum tar; do command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }; done
HERE="$ROOT/scripts/it"
CACHE=${HOUSEKEEPARR_IT_CACHE:-$HOME/.cache/housekeeparr-it}
ARCH=$(uname -m)
case "$ARCH" in arm64) ARCH=aarch64 ;; amd64) ARCH=x86_64 ;; esac
# shellcheck source=scripts/it/lib.sh
. "$HERE/lib.sh"

NGINX_ROOT="$CACHE/nginx/root"
rm -rf "$NGINX_ROOT"
fetch_debs "$NGINX_ROOT" nginx
"$NGINX_ROOT/usr/sbin/nginx" -v
log=$(mktemp "${TMPDIR:-/tmp}/housekeeparr-nginx.XXXXXX")
trap 'rm -f "$log"' EXIT
(cd "$ROOT" && HK_IT_NGINX="$NGINX_ROOT/usr/sbin/nginx" go test -tags integration -count=1 -v -run '^TestNginxProxy$' ./internal/integration) 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
if grep -q -- '--- SKIP' "$log"; then echo "TestNginxProxy skipped" >&2; exit 1; fi
grep -q -- '--- PASS: TestNginxProxy ' "$log" || { echo "TestNginxProxy did not pass" >&2; exit 1; }
exit "$status"
