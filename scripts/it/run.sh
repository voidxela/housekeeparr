#!/usr/bin/env bash
# Disposable-instance integration suite.
#
# Downloads pinned, checksum-verified releases of qBittorrent (static
# qbittorrent-nox) and Sonarr, starts them on loopback in a temporary
# directory, adds locally generated torrents whose tracker is a closed
# loopback port (DHT/PeX/LSD disabled: no swarm can ever be contacted), and
# runs the Go integration tests against them. Everything is removed on exit.
#
# Usage: scripts/it/run.sh [--qbt 5.3.0|4.6.7] [--capture] [--keep]
#   --capture  write sanitized raw responses to testdata/captured/ for review
#   --keep     keep the work directory for debugging
#
# Note: Sonarr itself may contact its own update/metadata services while it
# runs. No real user data or public torrent is involved.
set -euo pipefail

QBT_VERSION=5.3.0
SONARR_VERSION=4.0.20.3014
CAPTURE=0
KEEP=0
while [ $# -gt 0 ]; do
	case "$1" in
	--qbt) QBT_VERSION="$2"; shift 2 ;;
	--capture) CAPTURE=1; shift ;;
	--keep) KEEP=1; shift ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
HERE="$ROOT/scripts/it"
CACHE=${HOUSEKEEPARR_IT_CACHE:-$HOME/.cache/housekeeparr-it}
ARCH=$(uname -m)
case "$ARCH" in arm64) ARCH=aarch64 ;; amd64) ARCH=x86_64 ;; esac
WORK=$(mktemp -d "${TMPDIR:-/tmp}/housekeeparr-it.XXXXXX")
PIDS=()
cleanup() {
	for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	if [ "$KEEP" = 1 ]; then echo "kept $WORK"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT
mkdir -p "$CACHE"

fetch() { # name version -> path of verified artifact
	local line sum url
	line=$(awk -v n="$1" -v v="$2" -v a="$ARCH" '$1==n && $2==v && $3==a' "$HERE/artifacts.txt")
	if [ -z "$line" ]; then echo "no pinned $1 $2 artifact for $ARCH; add one to artifacts.txt" >&2; exit 3; fi
	sum=$(echo "$line" | awk '{print $4}'); url=$(echo "$line" | awk '{print $5}')
	local dst="$CACHE/$1-$2-$ARCH"
	if [ ! -f "$dst" ] || ! echo "$sum  $dst" | sha256sum -c --status; then
		echo "downloading $url" >&2
		curl -fsSL --max-time 900 -o "$dst.part" "$url"
		mv "$dst.part" "$dst"
	fi
	echo "$sum  $dst" | sha256sum -c --status || { echo "checksum mismatch for $dst" >&2; rm -f "$dst"; exit 3; }
	echo "$dst"
}

freeport() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }

waitfor() { # url seconds
	for _ in $(seq 1 "$2"); do curl -fs -o /dev/null "$1" 2>/dev/null && return 0; curl -s -o /dev/null -w '%{http_code}' "$1" 2>/dev/null | grep -qE '^(401|403)$' && return 0; sleep 1; done
	echo "timed out waiting for $1" >&2; return 1
}

# --- synthetic torrents -----------------------------------------------------
python3 -I "$HERE/fixtures.py" "$WORK/torrents" "$WORK/downloads"

# --- qBittorrent --------------------------------------------------------------
QBT_BIN=$(fetch qbittorrent "$QBT_VERSION"); chmod +x "$QBT_BIN"
QBT_PORT=$(freeport); BT_PORT=$(freeport)
QBT_PASS=$(python3 -c 'import secrets; print(secrets.token_urlsafe(18))')
mkdir -p "$WORK/qbt/qBittorrent/config"
cat > "$WORK/qbt/qBittorrent/config/qBittorrent.conf" <<EOF
[LegalNotice]
Accepted=true

[BitTorrent]
Session\\DHTEnabled=false
Session\\PeXEnabled=false
Session\\LSDEnabled=false
Session\\Port=$BT_PORT
Session\\InterfaceAddress=127.0.0.1
Session\\DefaultSavePath=$WORK/downloads
Session\\QueueingSystemEnabled=false
Session\\AddTorrentStopped=false

[Preferences]
WebUI\\Address=127.0.0.1
WebUI\\Port=$QBT_PORT
WebUI\\Username=admin
WebUI\\Password_PBKDF2=$(printf '%s' "$QBT_PASS" | python3 -I "$HERE/pbkdf2.py")
WebUI\\LocalHostAuth=true
WebUI\\CSRFProtection=true
WebUI\\HostHeaderValidation=true
General\\Locale=en
EOF
QBT_ARGS=(--profile="$WORK/qbt" --webui-port="$QBT_PORT")
case "$QBT_VERSION" in 5.*) QBT_ARGS+=(--confirm-legal-notice) ;; esac
"$QBT_BIN" "${QBT_ARGS[@]}" > "$WORK/qbt.log" 2>&1 &
PIDS+=($!)
QBT="http://127.0.0.1:$QBT_PORT"
waitfor "$QBT/api/v2/app/version" 60

# Setup writes go to the disposable instance only (never through Housekeeparr).
JAR="$WORK/qbt.cookies"
curl -fsS -c "$JAR" -H "Referer: $QBT" --data-urlencode "username=admin" --data-urlencode "password=$QBT_PASS" "$QBT/api/v2/auth/login" >/dev/null
python3 -I - "$WORK/torrents/manifest.json" "$WORK/torrents" "$QBT" "$JAR" <<'PY'
import json, subprocess, sys, time
manifest, tdir, qbt, jar = sys.argv[1:]
def curl(*a):
    return subprocess.run(["curl", "-fsS", "-b", jar, "-H", "Referer: " + qbt, *a], check=True, capture_output=True, text=True).stdout
for t in json.load(open(manifest)):
    common = ["-F", "category=tv-sonarr", "-F", "savepath=" + tdir.replace("torrents", "downloads")]
    if t["key"] == "magnet":
        curl(*common, "-F", "urls=magnet:?xt=urn:btih:%s&dn=%s" % (t["hash"], t["name"]), qbt + "/api/v2/torrents/add")
    else:
        curl(*common, "-F", "torrents=@%s/%s.torrent" % (tdir, t["name"]), qbt + "/api/v2/torrents/add")
time.sleep(3)
for t in json.load(open(manifest)):
    if t.get("deselect"):
        idx = t["files"].index(t["deselect"])
        curl("--data", "hash=%s&id=%d&priority=0" % (t["hash"], idx), qbt + "/api/v2/torrents/filePrio")
    if t["key"] in ("half", "complete"):
        curl("--data", "hashes=" + t["hash"], qbt + "/api/v2/torrents/recheck")
    if t["key"] == "stopped":
        for ep in ("stop", "pause"):  # 5.x renamed pause to stop
            try: curl("--data", "hashes=" + t["hash"], qbt + "/api/v2/torrents/" + ep); break
            except subprocess.CalledProcessError: pass
time.sleep(5)
PY

# --- Sonarr ---------------------------------------------------------------
SONARR_TGZ=$(fetch sonarr "$SONARR_VERSION")
mkdir -p "$WORK/sonarr-app" "$WORK/sonarr-data"
tar -xzf "$SONARR_TGZ" -C "$WORK/sonarr-app"
SONARR_PORT=$(freeport)
SONARR_KEY=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
cat > "$WORK/sonarr-data/config.xml" <<EOF
<Config>
  <BindAddress>127.0.0.1</BindAddress>
  <Port>$SONARR_PORT</Port>
  <ApiKey>$SONARR_KEY</ApiKey>
  <AuthenticationMethod>Forms</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <UpdateMechanism>External</UpdateMechanism>
  <AnalyticsEnabled>False</AnalyticsEnabled>
  <LaunchBrowser>False</LaunchBrowser>
  <Branch>main</Branch>
  <LogLevel>info</LogLevel>
</Config>
EOF
DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1 "$WORK/sonarr-app/Sonarr/Sonarr" -nobrowser -data="$WORK/sonarr-data" > "$WORK/sonarr.log" 2>&1 &
PIDS+=($!)
SONARR="http://127.0.0.1:$SONARR_PORT"
waitfor "$SONARR/api/v3/system/status?apikey=$SONARR_KEY" 120
curl -fsS -H "X-Api-Key: $SONARR_KEY" -H "Content-Type: application/json" "$SONARR/api/v3/downloadclient?forceSave=true" -d @- >/dev/null <<EOF
{"enable": true, "protocol": "torrent", "priority": 1, "name": "qBittorrent", "implementation": "QBittorrent",
 "configContract": "QBittorrentSettings", "removeCompletedDownloads": false, "removeFailedDownloads": false, "tags": [],
 "fields": [{"name": "host", "value": "127.0.0.1"}, {"name": "port", "value": $QBT_PORT}, {"name": "useSsl", "value": false},
            {"name": "username", "value": "admin"}, {"name": "password", "value": "$QBT_PASS"}, {"name": "tvCategory", "value": "tv-sonarr"}]}
EOF
curl -fsS -H "X-Api-Key: $SONARR_KEY" -H "Content-Type: application/json" "$SONARR/api/v3/command" -d '{"name": "RefreshMonitoredDownloads"}' >/dev/null
sleep 10

# --- tests ------------------------------------------------------------------
export HK_IT_QBT_URL="$QBT" HK_IT_QBT_USER=admin HK_IT_QBT_PASS="$QBT_PASS" HK_IT_QBT_VERSION="$QBT_VERSION"
export HK_IT_SONARR_URL="$SONARR" HK_IT_SONARR_KEY="$SONARR_KEY" HK_IT_SONARR_VERSION="$SONARR_VERSION"
export HK_IT_MANIFEST="$WORK/torrents/manifest.json" HK_IT_WORK="$WORK"
if [ "$CAPTURE" = 1 ]; then export HK_IT_CAPTURE_DIR="$ROOT/internal/integration/testdata/captured"; fi
cd "$ROOT"
set +e
go test -tags integration -count=1 -v ./internal/integration/
STATUS=$?
set -e
if [ $STATUS -ne 0 ]; then
	echo "--- qBittorrent log (tail)"; tail -n 40 "$WORK/qbt.log" || true
	echo "--- Sonarr log (tail)"; tail -n 60 "$WORK/sonarr.log" || true
fi
exit $STATUS
