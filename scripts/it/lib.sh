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

