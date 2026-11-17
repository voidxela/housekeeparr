#!/usr/bin/env bash
# Disposable-instance integration suite.
#
# Starts pinned, checksum-verified released applications on loopback in a
# temporary directory, adds locally generated torrents whose tracker is a
# closed loopback port (DHT/PeX/LSD disabled: no swarm can ever be
# contacted), and runs the Go integration tests against them. Everything is
# removed on exit.
#
# Usage: scripts/it/run.sh [--clients LIST] [--managers LIST] [--manager-client CLIENT]
#                          [--qbt 5.3.0|4.6.7] [--tr 4.0.6|4.1.4] [--capture] [--keep]
#   --clients         comma-separated: qbittorrent,transmission,deluge,rtorrent (default qbittorrent)
#   --managers        comma-separated: sonarr,radarr, or none (default sonarr)
#   --manager-client  qbittorrent|transmission|deluge|rtorrent (default qbittorrent; must be in --clients)
#   --capture         write sanitized raw responses to testdata/captured/ for review
#   --keep            keep the work directory for debugging
#   --no-netns        do not isolate the run in a loopback-only network namespace
#   --only REGEX      run only the registered tests whose names match REGEX
#                     (extended regex); the selection is printed and each
#                     selected test is still required to pass
#
# Every selected application's tests must report PASS; a skipped test fails
# the run, so a missing environment variable can never be recorded as a pass.
#
# Inside the network namespace, the managers' hard-coded metadata/update
# hosts resolve (via a private bind-mounted /etc/hosts) to a loopback
# stand-in serving fictional content, and a loopback Torznab stand-in offers
# synthetic releases, so the managed-content probes produce real grab
# history. With --no-netns those probes are skipped and the managers may try
# to reach their real services. No real user data or public torrent is
# involved.
set -euo pipefail

QBT_VERSION=5.3.0
TR_VERSION=4.0.6
SONARR_VERSION=4.0.20.3014
RADARR_VERSION=6.4.4.10685
CLIENTS=qbittorrent
MANAGERS=sonarr
MANAGER_CLIENT=qbittorrent
CAPTURE=0
KEEP=0
NETNS=1
ONLY=
ORIG_ARGS=("$@")
while [ $# -gt 0 ]; do
	case "$1" in
	--clients) CLIENTS="$2"; shift 2 ;;
	--managers) MANAGERS="$2"; shift 2 ;;
	--manager-client) MANAGER_CLIENT="$2"; shift 2 ;;
	--qbt) QBT_VERSION="$2"; shift 2 ;;
	--tr) TR_VERSION="$2"; shift 2 ;;
	--capture) CAPTURE=1; shift ;;
	--keep) KEEP=1; shift ;;
	--no-netns) NETNS=0; shift ;;
	--only) ONLY="$2"; shift 2 ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
[ "$MANAGERS" = none ] && MANAGERS=""
case "$MANAGER_CLIENT" in
qbittorrent|transmission|deluge|rtorrent) ;;
*) echo "unknown --manager-client: $MANAGER_CLIENT" >&2; exit 2 ;;
esac
has() { case ",$1," in *",$2,"*) return 0 ;; esac; return 1; }
if [ -n "$MANAGERS" ] && ! has "$CLIENTS" "$MANAGER_CLIENT"; then
	echo "manager client $MANAGER_CLIENT must be included in --clients (or pass --managers none)" >&2; exit 2
fi

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
if ! command -v go >/dev/null 2>&1; then
	if [ -x "$HOME/.local/go/bin/go" ]; then export PATH="$HOME/.local/go/bin:$PATH"; else echo "go not found on PATH" >&2; exit 2; fi
fi
for tool in curl python3 sha256sum tar; do command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }; done
HERE="$ROOT/scripts/it"
CACHE=${HOUSEKEEPARR_IT_CACHE:-$HOME/.cache/housekeeparr-it}
ARCH=$(uname -m)
case "$ARCH" in arm64) ARCH=aarch64 ;; amd64) ARCH=x86_64 ;; esac
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

# shellcheck source=scripts/it/lib.sh
. "$HERE/lib.sh"

freeport() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
secret() { python3 -c 'import secrets; print(secrets.token_urlsafe(18))'; }

waitfor() { # url seconds
	for _ in $(seq 1 "$2"); do curl -fs -o /dev/null "$1" 2>/dev/null && return 0; curl -s -o /dev/null -w '%{http_code}' "$1" 2>/dev/null | grep -qE '^(401|403|405|409)$' && return 0; sleep 1; done
	echo "timed out waiting for $1" >&2; return 1
}

# Each client gets its own copy of the payload directory so no two clients
# ever share files.
payload() { mkdir -p "$WORK/$1"; cp -a "$WORK/downloads/." "$WORK/$1/"; echo "$WORK/$1"; }

DELUGE_PKGS="deluge-common deluge-web deluged libboost-python1.83.0 libtorrent-rasterbar2.0t64 python3-attr python3-autocommand
python3-automat python3-bcrypt python3-cffi-backend python3-chardet python3-constantly python3-cryptography python3-hamcrest
python3-hyperlink python3-idna python3-importlib-metadata python3-importlib-resources python3-incremental python3-inflect
python3-jaraco.context python3-jaraco.functools python3-jaraco.text python3-legacy-cgi python3-libtorrent python3-mako
python3-markupsafe python3-more-itertools python3-openssl python3-pkg-resources python3-pyasn1 python3-pyasn1-modules
python3-rencode python3-service-identity python3-setuptools python3-tomli python3-twisted python3-typeguard
python3-typing-extensions python3-xdg python3-zipp python3-zope.interface"

# --- network isolation ------------------------------------------------------
# By default every artifact is fetched first, then the suite re-runs itself
# in an unprivileged user + network namespace (unshare -rn) whose only
# interface is loopback. No started application can reach any other network:
# not a swarm, a DHT bootstrap node, a LAN gateway (UPnP/NAT-PMP), or an
# update service. This matters because some clients (e.g. Deluge 2.2.0)
# create their libtorrent session with defaults before applying their own
# configuration.
prefetch() {
	local scratch; scratch=$(mktemp -d "${TMPDIR:-/tmp}/housekeeparr-prefetch.XXXXXX")
	local c m
	for c in ${CLIENTS//,/ }; do
		case "$c" in
		qbittorrent) fetch qbittorrent "$QBT_VERSION" >/dev/null ;;
		transmission) "$HERE/build-transmission.sh" "$TR_VERSION" >/dev/null ;;
		rtorrent) fetch_debs "$scratch" rtorrent libtorrent21t64 libxmlrpc-core-c3t64 libxmlrpc-util4 lighttpd ;;
		# shellcheck disable=SC2086
		deluge) fetch_debs "$scratch" $DELUGE_PKGS ;;
		esac
	done
	for m in ${MANAGERS//,/ }; do
		case "$m" in
		sonarr) fetch sonarr "$SONARR_VERSION" >/dev/null ;;
		radarr) fetch radarr "$RADARR_VERSION" >/dev/null ;;
		esac
	done
	rm -rf "$scratch"
}
if [ "$NETNS" = 1 ] && [ -z "${HK_IT_IN_NETNS:-}" ]; then
	command -v unshare >/dev/null && command -v ip >/dev/null || { echo "unshare and ip are required for network isolation (or pass --no-netns)" >&2; exit 2; }
	prefetch
	# -m: a private mount namespace, so the managers' metadata hosts can be
	# redirected to the loopback stand-in by bind-mounting a private hosts
	# file over /etc/hosts. The host's /etc/hosts is never modified.
	exec unshare -rnm env HK_IT_IN_NETNS=1 bash -c 'ip link set lo up && exec "$@"' _ "$0" "${ORIG_ARGS[@]}"
fi
if [ -n "${HK_IT_IN_NETNS:-}" ]; then
	python3 -c 'import socket,sys; s=socket.socket(); s.settimeout(2); sys.exit(0 if s.connect_ex(("192.0.2.1", 80)) != 0 else 1)' ||
		{ echo "network namespace is not isolated" >&2; exit 2; }
	echo "running in an isolated loopback-only network namespace" >&2
	# Go must not reach the network either.
	export GOFLAGS="${GOFLAGS:+$GOFLAGS }-mod=readonly" GOPROXY=off GOTOOLCHAIN=local
fi

WORK=$(mktemp -d "${TMPDIR:-/tmp}/housekeeparr-it.XXXXXX")
PIDS=()
LOGS=()
cleanup() {
	for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
	# A manager a test restarted itself (HK_IT_<APP>_START) is not in PIDS.
	pkill -f "$WORK/[a-z]*-app/" 2>/dev/null || true
	wait 2>/dev/null || true
	if [ "$KEEP" = 1 ]; then echo "kept $WORK"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT

TESTS=()
PROBES=() # state-changing workflow probes, run after observation tests
MANAGED_PROBES=() # run after every unknown-content probe

# --- metadata and indexer stand-in (managed-content probes) ------------------
# Only inside the isolated namespace: the managers' metadata hosts are
# hard-coded HTTPS names, redirected here to a loopback stand-in whose
# throwaway CA the managers trust through SSL_CERT_FILE.
STANDIN=0
META_HOSTS="skyhook.sonarr.tv services.sonarr.tv api.radarr.video radarr.servarr.com api.themoviedb.org"
start_standin() {
	printf '127.0.0.1 localhost\n::1 localhost\n127.0.0.1 %s\n' "$META_HOSTS" > "$WORK/hosts"
	mount --bind "$WORK/hosts" /etc/hosts
	go build -tags integration -o "$WORK/standin" "$ROOT/internal/integration/standin"
	local port=$STANDIN_PORT key; key=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
	"$WORK/standin" -tls-addr 127.0.0.1:443 -indexer-addr "127.0.0.1:$port" -hosts "${META_HOSTS// /,}" \
		-ca-out "$WORK/standin-ca.pem" -routes "$ROOT/internal/integration/testdata/standin/routes.json" \
		-manifest "$WORK/torrents/managed.json" -torrents "$WORK/torrents" -apikey "$key" -ready "$WORK/standin.ready" \
		> "$WORK/standin.log" 2>&1 &
	PIDS+=($!); LOGS+=("$WORK/standin.log")
	for _ in $(seq 1 30); do [ -f "$WORK/standin.ready" ] && break; sleep 1; done
	[ -f "$WORK/standin.ready" ] || { echo "stand-in did not start" >&2; tail "$WORK/standin.log" >&2; exit 1; }
	export HK_IT_INDEXER_URL="http://127.0.0.1:$port" HK_IT_INDEXER_KEY="$key"
	STANDIN=1
}
export HK_IT_MANIFEST="$WORK/torrents/manifest.json" HK_IT_WORK="$WORK"

# --- synthetic torrents -----------------------------------------------------
# The stand-in's port is chosen first: its rejecting tracker is announced by
# the preserved-data fixtures.
STANDIN_PORT=$(freeport)
HK_IT_REJECT_ANNOUNCE="http://127.0.0.1:$STANDIN_PORT/announce" python3 -I "$HERE/fixtures.py" "$WORK/torrents" "$WORK/downloads"

# --- qBittorrent --------------------------------------------------------------
start_qbittorrent() {
	QBT_BIN=$(fetch qbittorrent "$QBT_VERSION"); chmod +x "$QBT_BIN"
	QBT_PORT=$(freeport); local bt; bt=$(freeport)
	QBT_PASS=$(secret)
	local save; save=$(payload qbt-downloads)
	mkdir -p "$WORK/qbt/qBittorrent/config"
	cat > "$WORK/qbt/qBittorrent/config/qBittorrent.conf" <<EOF
[LegalNotice]
Accepted=true

[BitTorrent]
Session\\DHTEnabled=false
Session\\PeXEnabled=false
Session\\LSDEnabled=false
Session\\Port=$bt
Session\\InterfaceAddress=127.0.0.1
Session\\DefaultSavePath=$save
Session\\QueueingSystemEnabled=false
Session\\AddTorrentStopped=false

[Network]
PortForwardingEnabled=false

[Preferences]
Connection\\ResolvePeerCountries=false
WebUI\\Address=127.0.0.1
WebUI\\Port=$QBT_PORT
WebUI\\Username=admin
WebUI\\Password_PBKDF2=$(printf '%s' "$QBT_PASS" | python3 -I "$HERE/pbkdf2.py")
WebUI\\LocalHostAuth=true
WebUI\\CSRFProtection=true
WebUI\\HostHeaderValidation=true
General\\Locale=en
EOF
	local args=(--profile="$WORK/qbt" --webui-port="$QBT_PORT")
	case "$QBT_VERSION" in 5.*) args+=(--confirm-legal-notice) ;; esac
	"$QBT_BIN" "${args[@]}" > "$WORK/qbt.log" 2>&1 &
	local qbt_pid=$!
	PIDS+=("$qbt_pid"); LOGS+=("$WORK/qbt.log")
	QBT="http://127.0.0.1:$QBT_PORT"
	waitfor "$QBT/api/v2/app/version" 60

	# Setup writes go to the disposable instance only (never through Housekeeparr).
	local jar="$WORK/qbt.cookies"
	curl -fsS -c "$jar" -H "Referer: $QBT" --data-urlencode "username=admin" --data-urlencode "password=$QBT_PASS" "$QBT/api/v2/auth/login" >/dev/null
	python3 -I - "$WORK/torrents/manifest.json" "$WORK/torrents" "$QBT" "$jar" "$save" <<'PY'
import json, os, subprocess, sys, time
manifest, tdir, qbt, jar, save = sys.argv[1:]
def curl(*a):
    return subprocess.run(["curl", "-fsS", "-b", jar, "-H", "Referer: " + qbt, *a], check=True, capture_output=True, text=True).stdout
def act(h, *eps):
    for ep in eps:  # 5.x renamed pause/resume to stop/start
        try: curl("--data", "hashes=" + h, qbt + "/api/v2/torrents/" + ep); return
        except subprocess.CalledProcessError: pass
for t in json.load(open(manifest)):
    cat = "movies-radarr" if t.get("manager") == "radarr" else "tv-sonarr"
    common = ["-F", "category=" + cat, "-F", "savepath=" + save]
    if t["key"] == "magnet":
        curl(*common, "-F", "urls=magnet:?xt=urn:btih:%s&dn=%s" % (t["hash"], t["name"]), qbt + "/api/v2/torrents/add")
    else:
        curl(*common, "-F", "torrents=@%s/%s.torrent" % (tdir, t["name"]), qbt + "/api/v2/torrents/add")
time.sleep(3)
for t in json.load(open(manifest)):
    if t.get("deselect"):
        idx = t["files"].index(t["deselect"])
        curl("--data", "hash=%s&id=%d&priority=0" % (t["hash"], idx), qbt + "/api/v2/torrents/filePrio")
    if t.get("data", "none") != "none":  # verify any torrent with local data
        curl("--data", "hashes=" + t["hash"], qbt + "/api/v2/torrents/recheck")
    if t["key"] == "stopped":
        act(t["hash"], "stop", "pause")
full_hashes = {t["hash"] for t in json.load(open(manifest)) if t.get("data") == "full"}
# Every fully present payload must be checked and 100% verified before the
# restart: a check still running (or not yet started) when the payload of
# errored-restart is deleted produced stalledDL with nothing verified
# (qBittorrent 4.6.7 at 118f15c) instead of missingFiles.
deadline = time.time() + 60
while True:
    rows = json.loads(curl(qbt + "/api/v2/torrents/info"))
    info = {r["hash"]: r for r in rows}
    pending = [h for h in full_hashes if h not in info or info[h]["state"].startswith("checking") or info[h].get("progress", 0) < 1]
    if not pending:
        break
    if time.time() > deadline:
        sys.exit("qBittorrent did not verify the full payloads before the restart: %s" % {h: (info.get(h) or {}).get("state") for h in pending})
    time.sleep(0.5)
time.sleep(1)
PY
	kill -TERM "$qbt_pid" 2>/dev/null || true
	wait "$qbt_pid" 2>/dev/null || true
	python3 -I - "$WORK/torrents/manifest.json" "$save" <<'PY'
import json, os, sys
manifest, save = sys.argv[1:]
for t in json.load(open(manifest)):
    if t["key"] == "errored-restart":
        os.remove(os.path.join(save, t["name"]))
PY
	"$QBT_BIN" "${args[@]}" >> "$WORK/qbt.log" 2>&1 &
	PIDS+=($!)
	waitfor "$QBT/api/v2/app/version" 60
	curl -fsS -c "$jar" -H "Referer: $QBT" --data-urlencode "username=admin" --data-urlencode "password=$QBT_PASS" "$QBT/api/v2/auth/login" >/dev/null
	python3 -I - "$WORK/torrents/manifest.json" "$QBT" "$jar" "$save" <<'PY'
import json, os, subprocess, sys, time
manifest, qbt, jar, save = sys.argv[1:]
def curl(*a):
    return subprocess.run(["curl", "-fsS", "-b", jar, "-H", "Referer: " + qbt, *a], check=True, capture_output=True, text=True).stdout
def act(h, *eps):
    for ep in eps:  # 5.x renamed pause/resume to stop/start
        try: curl("--data", "hashes=" + h, qbt + "/api/v2/torrents/" + ep); return
        except subprocess.CalledProcessError: pass
for t in json.load(open(manifest)):
    if t["key"] == "errored-restart":
        rows = json.loads(curl(qbt + "/api/v2/torrents/info?hashes=" + t["hash"]))
        if rows and rows[0].get("state", "").startswith(("stopped", "paused")):
            act(t["hash"], "start", "resume")
    elif t["key"] == "errored":
        act(t["hash"], "stop", "pause")
        time.sleep(1)
        os.remove(os.path.join(save, t["name"]))
        act(t["hash"], "start", "resume")
    elif t["key"] == "errored-recheck":
        os.remove(os.path.join(save, t["name"]))
        curl("--data", "hashes=" + t["hash"], qbt + "/api/v2/torrents/recheck")
time.sleep(5)
PY
	export HK_IT_QBT_URL="$QBT" HK_IT_QBT_USER=admin HK_IT_QBT_PASS="$QBT_PASS" HK_IT_QBT_VERSION="$QBT_VERSION" HK_IT_QBT_SAVE="$save"
	TESTS+=(TestQBittorrentReleasedBehaviour)
}

# --- Transmission (built from a pinned release by build-transmission.sh) ----
start_transmission() {
	local bin; bin=$("$HERE/build-transmission.sh" "$TR_VERSION")
	local port bt pass save cfg
	port=$(freeport); bt=$(freeport); pass=$(secret); save=$(payload tr-downloads); cfg="$WORK/tr-config"
	mkdir -p "$cfg" "$save/tv-sonarr" "$save/movies-radarr"
	cp -a "$WORK/downloads/." "$save/tv-sonarr/"
	cp -a "$WORK/downloads/." "$save/movies-radarr/"
	# Plain rpc-password is hashed by the daemon on first start.
	cat > "$cfg/settings.json" <<EOF
{"rpc-enabled": true, "rpc-bind-address": "127.0.0.1", "rpc-port": $port,
 "rpc-authentication-required": true, "rpc-username": "admin", "rpc-password": "$pass",
 "rpc-whitelist-enabled": true, "rpc-whitelist": "127.0.0.1", "rpc-host-whitelist-enabled": true,
 "dht-enabled": false, "pex-enabled": false, "lpd-enabled": false, "utp-enabled": false,
 "port-forwarding-enabled": false, "peer-port": $bt, "peer-port-random-on-start": false,
 "bind-address-ipv4": "127.0.0.1", "bind-address-ipv6": "::1",
 "download-dir": "$save", "incomplete-dir-enabled": false, "start-added-torrents": true,
 "download-queue-enabled": false, "seed-queue-enabled": false, "message-level": 2}
EOF
	"$bin" -f -g "$cfg" > "$WORK/tr.log" 2>&1 &
	local tr_pid=$!
	PIDS+=("$tr_pid"); LOGS+=("$WORK/tr.log")
	TR="http://127.0.0.1:$port"
	TR_PORT="$port"; TR_PASS="$pass"
	waitfor "$TR/transmission/rpc" 60
	python3 -I "$HERE/setup_clients.py" transmission "$TR/transmission/rpc" admin "$pass" "$WORK/torrents/manifest.json" "$WORK/torrents" "$save"
	kill -TERM "$tr_pid" 2>/dev/null || true
	wait "$tr_pid" 2>/dev/null || true
	python3 -I - "$WORK/torrents/manifest.json" "$save" <<'PY'
import json, os, sys
manifest, save = sys.argv[1:]
for t in json.load(open(manifest)):
    if t["key"] == "errored-restart":
        cat = "movies-radarr" if t.get("manager") == "radarr" else "tv-sonarr"
        os.remove(os.path.join(save, cat, t["name"]))
PY
	"$bin" -f -g "$cfg" >> "$WORK/tr.log" 2>&1 &
	PIDS+=($!)
	waitfor "$TR/transmission/rpc" 60
	python3 -I "$HERE/setup_clients.py" transmission-post-restart "$TR/transmission/rpc" admin "$pass" "$WORK/torrents/manifest.json" "$save"
	export HK_IT_TR_URL="$TR" HK_IT_TR_USER=admin HK_IT_TR_PASS="$pass" HK_IT_TR_VERSION="$TR_VERSION" HK_IT_TR_SAVE="$save"
	TESTS+=(TestTransmissionReleasedBehaviour)
}

# --- rTorrent (Debian 0.9.8) behind a lighttpd SCGI gateway with basic auth ---
start_rtorrent() {
	local root="$WORK/rt-root"
	fetch_debs "$root" rtorrent libtorrent21t64 libxmlrpc-core-c3t64 libxmlrpc-util4 lighttpd
	local lib="$root/usr/lib/$(uname -m)-linux-gnu"
	local scgi bt http pass save
	scgi=$(freeport); bt=$(freeport); http=$(freeport); pass=$(secret); save=$(payload rt-downloads)
	mkdir -p "$WORK/rt-session" "$WORK/rt-home" "$WORK/lighttpd-root"
	cat > "$WORK/rtorrent.rc" <<EOF
directory.default.set = $save
session.path.set = $WORK/rt-session
network.scgi.open_port = 127.0.0.1:$scgi
network.port_range.set = $bt-$bt
network.port_random.set = no
network.bind_address.set = 127.0.0.1
dht.mode.set = disable
protocol.pex.set = no
trackers.use_udp.set = no
system.daemon.set = true
EOF
	HOME="$WORK/rt-home" LD_LIBRARY_PATH="$lib" "$root/usr/bin/rtorrent" -n -o "import=$WORK/rtorrent.rc" > "$WORK/rtorrent.log" 2>&1 &
	local rt_pid=$!
	PIDS+=("$rt_pid"); LOGS+=("$WORK/rtorrent.log")
	printf 'admin:%s\n' "$pass" > "$WORK/lighttpd-users"
	cat > "$WORK/lighttpd.conf" <<EOF
server.document-root = "$WORK/lighttpd-root"
server.bind = "127.0.0.1"
server.port = $http
server.errorlog = "$WORK/lighttpd-error.log"
server.modules = ("mod_auth", "mod_authn_file", "mod_scgi")
auth.backend = "plain"
auth.backend.plain.userfile = "$WORK/lighttpd-users"
auth.require = ("/RPC2" => ("method" => "basic", "realm" => "rtorrent", "require" => "valid-user"))
scgi.server = ("/RPC2" => (("host" => "127.0.0.1", "port" => $scgi, "check-local" => "disable")))
EOF
	LD_LIBRARY_PATH="$lib" "$root/usr/sbin/lighttpd" -D -f "$WORK/lighttpd.conf" -m "$root/usr/lib/lighttpd" > "$WORK/lighttpd.log" 2>&1 &
	PIDS+=($!); LOGS+=("$WORK/lighttpd.log" "$WORK/lighttpd-error.log")
	for _ in $(seq 1 30); do python3 -c "import socket; socket.create_connection(('127.0.0.1', $scgi), 1)" 2>/dev/null && break; sleep 1; done
	waitfor "http://127.0.0.1:$http/RPC2" 30
	python3 -I "$HERE/setup_clients.py" rtorrent "$scgi" "$WORK/torrents/manifest.json" "$WORK/torrents" "$save"
	kill -TERM "$rt_pid" 2>/dev/null || true
	wait "$rt_pid" 2>/dev/null || true
	python3 -I - "$WORK/torrents/manifest.json" "$save" <<'PY'
import json, os, sys
manifest, save = sys.argv[1:]
for t in json.load(open(manifest)):
    if t["key"] == "errored-restart":
        os.remove(os.path.join(save, t["name"]))
PY
	HOME="$WORK/rt-home" LD_LIBRARY_PATH="$lib" "$root/usr/bin/rtorrent" -n -o "import=$WORK/rtorrent.rc" >> "$WORK/rtorrent.log" 2>&1 &
	PIDS+=($!)
	for _ in $(seq 1 30); do python3 -c "import socket; socket.create_connection(('127.0.0.1', $scgi), 1)" 2>/dev/null && break; sleep 1; done
	python3 -I "$HERE/setup_clients.py" rtorrent-post-restart "$scgi" "$WORK/torrents/manifest.json" "$save"
	RT_HTTP_PORT="$http"; RT_PASS="$pass"
	export HK_IT_RT_URL="http://127.0.0.1:$http" HK_IT_RT_USER=admin HK_IT_RT_PASS="$pass" HK_IT_RT_VERSION=0.9.8 HK_IT_RT_SAVE="$save"
	TESTS+=(TestRTorrentReleasedBehaviour)
}

# --- Deluge (Debian 2.2.0 daemon + deluge-web, extracted; host python3) -------
start_deluge() {
	local root="$WORK/deluge-root"
	# shellcheck disable=SC2086
	fetch_debs "$root" $DELUGE_PKGS
	local daemon bt web dpass wpass save cfg salt
	daemon=$(freeport); bt=$(freeport); web=$(freeport); dpass=$(secret); wpass=$(secret); save=$(payload deluge-downloads)
	cfg="$WORK/deluge-config"; mkdir -p "$cfg"
	# localclient is the account deluge-web's default host list reads.
	printf 'localclient:%s:10\nadmin:%s:10\n' "$(secret)" "$dpass" > "$cfg/auth"
	# Deluge config files are a format header followed by the settings;
	# missing keys take Deluge's defaults. Queue limits are lifted so no
	# torrent waits for a slot, and every discovery mechanism is off.
	printf '{"file": 1, "format": 1}{"dht": false, "upnp": false, "natpmp": false, "lsd": false, "utpex": false,
 "random_port": false, "listen_ports": [%s, %s], "listen_interface": "127.0.0.1", "download_location": "%s",
 "move_completed": false, "new_release_check": false, "max_active_limit": -1, "max_active_downloading": -1,
 "max_active_seeding": -1, "allow_remote": false, "enabled_plugins": ["Label"]}' "$bt" "$bt" "$save" > "$cfg/core.conf"
	salt=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
	printf '{"file": 1, "format": 1}{"pwd_salt": "%s", "pwd_sha1": "%s", "first_login": false, "default_daemon": "", "https": false, "interface": "127.0.0.1", "port": %s}' \
		"$salt" "$(printf '%s%s' "$salt" "$wpass" | sha1sum | cut -d' ' -f1)" "$web" > "$cfg/web.conf"
	local env=(PYTHONPATH="$root/usr/lib/python3/dist-packages" LD_LIBRARY_PATH="$root/usr/lib/$(uname -m)-linux-gnu" HOME="$WORK")
	env "${env[@]}" python3 "$root/usr/bin/deluged" -d -c "$cfg" -u 127.0.0.1 -p "$daemon" -i 127.0.0.1 -L info -l "$WORK/deluged.log" > "$WORK/deluged.out" 2>&1 &
	local deluged_pid=$!
	PIDS+=("$deluged_pid"); LOGS+=("$WORK/deluged.log" "$WORK/deluged.out")
	for _ in $(seq 1 60); do python3 -c "import socket; socket.create_connection(('127.0.0.1', $daemon), 1)" 2>/dev/null && break; sleep 1; done
	env "${env[@]}" python3 "$root/usr/bin/deluge-web" -d -c "$cfg" -i 127.0.0.1 -p "$web" -L info -l "$WORK/deluge-web.log" > "$WORK/deluge-web.out" 2>&1 &
	PIDS+=($!); LOGS+=("$WORK/deluge-web.log" "$WORK/deluge-web.out")
	waitfor "http://127.0.0.1:$web/" 60
	python3 -I "$HERE/setup_clients.py" deluge "http://127.0.0.1:$web" "$wpass" "$daemon" admin "$dpass" "$WORK/torrents/manifest.json" "$WORK/torrents" "$save"
	kill -TERM "$deluged_pid" 2>/dev/null || true
	wait "$deluged_pid" 2>/dev/null || true
	python3 -I - "$WORK/torrents/manifest.json" "$save" <<'PY'
import json, os, sys
manifest, save = sys.argv[1:]
for t in json.load(open(manifest)):
    if t["key"] == "errored-restart":
        os.remove(os.path.join(save, t["name"]))
PY
	env "${env[@]}" python3 "$root/usr/bin/deluged" -d -c "$cfg" -u 127.0.0.1 -p "$daemon" -i 127.0.0.1 -L info -l "$WORK/deluged.log" >> "$WORK/deluged.out" 2>&1 &
	PIDS+=($!)
	for _ in $(seq 1 60); do python3 -c "import socket; socket.create_connection(('127.0.0.1', $daemon), 1)" 2>/dev/null && break; sleep 1; done
	python3 -I "$HERE/setup_clients.py" deluge-post-restart "http://127.0.0.1:$web" "$wpass" "$daemon" admin "$dpass" "$WORK/torrents/manifest.json" "$save"
	DELUGE_WEB_PORT="$web"; DELUGE_PASS="$wpass"
	export HK_IT_DELUGE_URL="http://127.0.0.1:$web" HK_IT_DELUGE_PASS="$wpass" HK_IT_DELUGE_VERSION=2.2.0 HK_IT_DELUGE_SAVE="$save"
	if [ -f "$WORK/deluge-label.ok" ]; then export HK_IT_DELUGE_LABEL=1; fi
	TESTS+=(TestDelugeReleasedBehaviour)
}

# --- *arr managers ------------------------------------------------------------
start_arr() { # app version port-var key-var
	local app=$1 version=$2 tgz port key title branch=main
	title=$(python3 -c "print('$app'.capitalize())")
	[ "$app" = radarr ] && branch=master
	tgz=$(fetch "$app" "$version")
	mkdir -p "$WORK/$app-app" "$WORK/$app-data"
	tar -xzf "$tgz" --no-same-owner -C "$WORK/$app-app"
	port=$(freeport); key=$(python3 -c 'import secrets; print(secrets.token_hex(16))')
	cat > "$WORK/$app-data/config.xml" <<EOF
<Config>
  <BindAddress>127.0.0.1</BindAddress>
  <Port>$port</Port>
  <ApiKey>$key</ApiKey>
  <AuthenticationMethod>Forms</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <UpdateMechanism>External</UpdateMechanism>
  <AnalyticsEnabled>False</AnalyticsEnabled>
  <LaunchBrowser>False</LaunchBrowser>
  <Branch>$branch</Branch>
  <LogLevel>info</LogLevel>
</Config>
EOF
	# Sonarr runs (and was validated) with invariant globalization; Radarr's
	# migrations require ICU cultures, so it uses the host ICU.
	local invariant=0
	[ "$app" = sonarr ] && invariant=1
	local tls=()
	[ "$STANDIN" = 1 ] && tls=(SSL_CERT_FILE="$WORK/standin-ca.pem")
	env "${tls[@]}" DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=$invariant "$WORK/$app-app/$title/$title" -nobrowser -data="$WORK/$app-data" > "$WORK/$app.log" 2>&1 &
	# How a test can start the manager again after shutting it down
	# (restart-durability probes): binary, data directory, environment.
	export "HK_IT_${title^^}_START=$WORK/$app-app/$title/$title|$WORK/$app-data|$invariant|${tls[*]:-}"
	PIDS+=($!); LOGS+=("$WORK/$app.log")
	local url="http://127.0.0.1:$port"
	waitfor "$url/api/v3/system/status?apikey=$key" 180
	local catfield=tvCategory cat=tv-sonarr
	if [ "$app" = radarr ]; then catfield=movieCategory; cat=movies-radarr; fi
	local dc_payload
	case "$MANAGER_CLIENT" in
	qbittorrent)
		dc_payload=$(cat <<EOF
{"enable": true, "protocol": "torrent", "priority": 1, "name": "qBittorrent", "implementation": "QBittorrent",
 "configContract": "QBittorrentSettings", "removeCompletedDownloads": false, "removeFailedDownloads": false, "tags": [],
 "fields": [{"name": "host", "value": "127.0.0.1"}, {"name": "port", "value": $QBT_PORT}, {"name": "useSsl", "value": false},
            {"name": "username", "value": "admin"}, {"name": "password", "value": "$QBT_PASS"}, {"name": "$catfield", "value": "$cat"}]}
EOF
)
		;;
	transmission)
		dc_payload=$(cat <<EOF
{"enable": true, "protocol": "torrent", "priority": 1, "name": "Transmission", "implementation": "Transmission",
 "configContract": "TransmissionSettings", "removeCompletedDownloads": false, "removeFailedDownloads": false, "tags": [],
 "fields": [{"name": "host", "value": "127.0.0.1"}, {"name": "port", "value": $TR_PORT}, {"name": "useSsl", "value": false},
            {"name": "urlBase", "value": "/transmission/"}, {"name": "username", "value": "admin"}, {"name": "password", "value": "$TR_PASS"},
            {"name": "$catfield", "value": "$cat"}]}
EOF
)
		;;
	deluge)
		local deluge_cat=""
		if [ -f "$WORK/deluge-label.ok" ]; then deluge_cat="$cat"; fi
		dc_payload=$(cat <<EOF
{"enable": true, "protocol": "torrent", "priority": 1, "name": "Deluge", "implementation": "Deluge",
 "configContract": "DelugeSettings", "removeCompletedDownloads": false, "removeFailedDownloads": false, "tags": [],
 "fields": [{"name": "host", "value": "127.0.0.1"}, {"name": "port", "value": $DELUGE_WEB_PORT}, {"name": "useSsl", "value": false},
            {"name": "urlBase", "value": ""}, {"name": "password", "value": "$DELUGE_PASS"}, {"name": "$catfield", "value": "$deluge_cat"}]}
EOF
)
		;;
	rtorrent)
		dc_payload=$(cat <<EOF
{"enable": true, "protocol": "torrent", "priority": 1, "name": "rTorrent", "implementation": "RTorrent",
 "configContract": "RTorrentSettings", "removeCompletedDownloads": false, "removeFailedDownloads": false, "tags": [],
 "fields": [{"name": "host", "value": "127.0.0.1"}, {"name": "port", "value": $RT_HTTP_PORT}, {"name": "useSsl", "value": false},
            {"name": "urlBase", "value": "RPC2"}, {"name": "username", "value": "admin"}, {"name": "password", "value": "$RT_PASS"},
            {"name": "$catfield", "value": "$cat"}]}
EOF
)
		;;
	esac
	curl -fsS -H "X-Api-Key: $key" -H "Content-Type: application/json" "$url/api/v3/downloadclient?forceSave=true" -d "$dc_payload" >/dev/null
	curl -fsS -H "X-Api-Key: $key" -H "Content-Type: application/json" "$url/api/v3/command" -d '{"name": "RefreshMonitoredDownloads"}' >/dev/null
	eval "${3}=\$url"; eval "${4}=\$key"
}

start_sonarr() {
	start_arr sonarr "$SONARR_VERSION" SONARR SONARR_KEY
	export HK_IT_SONARR_URL="$SONARR" HK_IT_SONARR_KEY="$SONARR_KEY" HK_IT_SONARR_VERSION="$SONARR_VERSION"
	TESTS+=(TestSonarrReleasedBehaviour TestEngineCorrelatesRealInstances)
	PROBES+=(TestSonarrQueueRemovalSemantics)
	if [ "$STANDIN" = 1 ]; then
		MANAGED_PROBES+=(TestSonarrManagedRemovalSemantics)
		# The full recovery workflow runs through whichever client the
		# managers use (--manager-client).
		MANAGED_PROBES+=(TestSonarrRecoveryWorkflow TestSonarrMetadataRecoveryWorkflow TestSonarrPrivateRecoveryWorkflow TestSonarrPreservedDataFailureWorkflow TestSonarrPrivatePreservedDataFailureWorkflow TestSonarrProactiveSearch TestSonarrPartialPackWorkflow)
	fi
}

start_radarr() {
	start_arr radarr "$RADARR_VERSION" RADARR RADARR_KEY
	export HK_IT_RADARR_URL="$RADARR" HK_IT_RADARR_KEY="$RADARR_KEY" HK_IT_RADARR_VERSION="$RADARR_VERSION"
	TESTS+=(TestRadarrReleasedBehaviour TestEngineCorrelatesRadarr)
	PROBES+=(TestRadarrQueueRemovalSemantics)
	if [ "$STANDIN" = 1 ]; then
		MANAGED_PROBES+=(TestRadarrManagedRemovalSemantics)
		MANAGED_PROBES+=(TestRadarrRecoveryWorkflow TestRadarrMetadataRecoveryWorkflow TestRadarrPrivateRecoveryWorkflow TestRadarrPreservedDataFailureWorkflow TestRadarrPrivatePreservedDataFailureWorkflow TestRadarrProactiveSearch)
	fi
}

for c in ${CLIENTS//,/ }; do
	case "$c" in
	qbittorrent) start_qbittorrent ;;
	transmission) start_transmission ;;
	rtorrent) start_rtorrent ;;
	deluge) start_deluge ;;
	*) echo "client $c is not in the harness yet" >&2; exit 2 ;;
	esac
done
if [ -n "$MANAGERS" ]; then
	export HK_IT_MANAGER_CLIENT="$MANAGER_CLIENT"
	if [ -n "${HK_IT_IN_NETNS:-}" ]; then start_standin; else echo "managed-content probes need the network namespace; skipped with --no-netns" >&2; fi
fi
for m in ${MANAGERS//,/ }; do
	case "$m" in
	sonarr) start_sonarr ;;
	radarr) start_radarr ;;
	*) echo "unknown manager $m" >&2; exit 2 ;;
	esac
done
[ -n "$MANAGERS" ] && sleep 10

# --- tests ------------------------------------------------------------------
if [ "$CAPTURE" = 1 ]; then export HK_IT_CAPTURE_DIR="$ROOT/internal/integration/testdata/captured"; fi
cd "$ROOT"
if [ "$STANDIN" = 1 ] && has "$MANAGERS" sonarr && has "$MANAGERS" radarr; then
	MANAGED_PROBES+=(TestAutomaticRecovery TestAutomaticPreservedDataFailure)
fi
PROBES+=("${MANAGED_PROBES[@]}")
if [ -n "${ONLY:-}" ]; then
	select_only() { local t; for t in "$@"; do [[ "$t" =~ $ONLY ]] && echo "$t"; done; return 0; }
	mapfile -t TESTS < <(select_only "${TESTS[@]}")
	mapfile -t PROBES < <(select_only "${PROBES[@]}")
	echo "--only $ONLY selected: ${TESTS[*]} ${PROBES[*]}"
	[ $((${#TESTS[@]} + ${#PROBES[@]})) -gt 0 ] || { echo "--only $ONLY selected no registered test" >&2; exit 2; }
fi
set +e
: > "$WORK/go-test.log"
STATUS=0
if [ ${#TESTS[@]} -gt 0 ]; then
	RUN=$(IFS='|'; echo "^(${TESTS[*]})\$")
	go test -tags integration -count=1 -v -run "$RUN" ./internal/integration/ | tee "$WORK/go-test.log"
	STATUS=${PIPESTATUS[0]}
fi
# Probes change manager and client state, so each runs on its own, in the
# order registered (unknown-content probes before managed-content probes).
# The automatic-dispatch test needs both managers and runs last.
for p in "${PROBES[@]}"; do
	go test -tags integration -count=1 -v -run "^$p\$" ./internal/integration/ | tee -a "$WORK/go-test.log"
	[ "${PIPESTATUS[0]}" -eq 0 ] || STATUS=1
done
set -e
for t in "${TESTS[@]}" "${PROBES[@]}"; do
	if ! grep -q -- "^--- PASS: $t " "$WORK/go-test.log"; then
		echo "required test $t did not pass (skipped, failed, or not run)" >&2
		STATUS=1
	fi
done
if [ $STATUS -ne 0 ]; then
	for l in "${LOGS[@]}"; do echo "--- $(basename "$l") (tail)"; tail -n 40 "$l" || true; done
fi
exit $STATUS
