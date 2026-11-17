# Shared helpers for the integration scripts. Requires HERE (scripts/it),
# CACHE (artifact cache), and ARCH (aarch64 or x86_64) to be set.

# fetch_debs root name... : downloads pinned Debian packages with apt-get
# (archive signatures verified by apt), checks the pinned sha256, and
# extracts them into a private root. Nothing is installed.
fetch_debs() {
	local root=$1; shift
	mkdir -p "$root" "$CACHE/debs"
	local n line ver sum spec f
	for n in "$@"; do
		line=$(awk -v n="deb:$n" -v a="$ARCH" '$1==n && $3==a' "$HERE/artifacts.txt")
		[ -n "$line" ] || { echo "no pinned deb:$n for $ARCH in artifacts.txt" >&2; exit 3; }
		ver=$(echo "$line" | awk '{print $2}'); sum=$(echo "$line" | awk '{print $4}'); spec=$(echo "$line" | awk '{print $5}')
		f=$(ls "$CACHE/debs/${n}_"*.deb 2>/dev/null | head -1 || true)
		if [ -z "$f" ] || ! echo "$sum  $f" | sha256sum -c --status; then
			(cd "$CACHE/debs" && apt-get download "${spec#apt:}" >&2)
			f=$(ls "$CACHE/debs/${n}_"*.deb | head -1)
		fi
		echo "$sum  $f" | sha256sum -c --status || { echo "checksum mismatch for $f (Debian may have updated $n $ver; re-pin deliberately)" >&2; exit 3; }
		dpkg-deb --fsys-tarfile "$f" | tar -x --no-same-owner -C "$root"
	done
}


# Check the namespace's complete interface list before starting any client.
# A failed connection to an external address alone also passes on many
# connected hosts where that particular address is blocked.
verify_loopback_only() {
	local interfaces
	interfaces=$(awk -F: 'NR > 2 { gsub(/[[:space:]]/, "", $1); print $1 }' /proc/net/dev)
	if [ "$interfaces" != lo ]; then
		echo "network namespace must contain only loopback" >&2
		return 1
	fi
}

# Some runtimes provide a working loopback in new namespaces and disallow
# netlink configuration. Check real TCP connectivity before using ip.
loopback_ready() {
	python3 - <<'PY'
import socket

try:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        listener.listen()
        with socket.create_connection(listener.getsockname(), timeout=1):
            pass
except OSError:
    raise SystemExit(1)
PY
}
