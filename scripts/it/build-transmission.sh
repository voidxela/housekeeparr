#!/usr/bin/env bash
# Builds transmission-daemon from a pinned, checksum-verified release
# tarball into the integration cache. Only the daemon is built; no GUI, web
# client build, or system installation. Build tools may come from the host or
# from Debian packages extracted (not installed) into $CACHE/sysroot.
#
# Usage: scripts/it/build-transmission.sh 4.0.6|4.1.4 [jobs]
set -euo pipefail
VERSION=$1
JOBS=${2:-3}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
HERE="$ROOT/scripts/it"
CACHE=${HOUSEKEEPARR_IT_CACHE:-$HOME/.cache/housekeeparr-it}
PREFIX="$CACHE/transmission-$VERSION-$(uname -m)"
if [ -x "$PREFIX/bin/transmission-daemon" ]; then echo "$PREFIX/bin/transmission-daemon"; exit 0; fi
line=$(awk -v v="$VERSION" '$1=="transmission-src" && $2==v' "$HERE/artifacts.txt")
[ -n "$line" ] || { echo "no pinned transmission-src $VERSION in artifacts.txt" >&2; exit 3; }
sum=$(echo "$line" | awk '{print $4}'); url=$(echo "$line" | awk '{print $5}')
TARBALL="$CACHE/transmission-$VERSION.tar.xz"
if [ ! -f "$TARBALL" ] || ! echo "$sum  $TARBALL" | sha256sum -c --status; then
	curl -fsSL --max-time 900 -o "$TARBALL.part" "$url" && mv "$TARBALL.part" "$TARBALL"
fi
echo "$sum  $TARBALL" | sha256sum -c --status || { echo "checksum mismatch for $TARBALL" >&2; exit 3; }

# Build tools: use the host's cmake, ninja, and libcurl headers when present;
# otherwise extract pinned Debian packages (never installed) into a private
# sysroot. A C/C++ compiler and OpenSSL headers must come from the host.
ARCH=$(uname -m)
case "$ARCH" in arm64) ARCH=aarch64 ;; amd64) ARCH=x86_64 ;; esac
# shellcheck source=scripts/it/lib.sh
. "$HERE/lib.sh"
for tool in cc c++ tar xz sha256sum; do command -v "$tool" >/dev/null || { echo "$tool is required to build Transmission" >&2; exit 2; }; done
[ -f /usr/include/openssl/ssl.h ] || { echo "OpenSSL headers (libssl-dev) are required to build Transmission" >&2; exit 2; }
SYSROOT="$CACHE/sysroot"
if ! command -v cmake >/dev/null || ! command -v ninja >/dev/null || ! command -v curl-config >/dev/null; then
	if [ ! -x "$SYSROOT/usr/bin/cmake" ] || [ ! -x "$SYSROOT/usr/bin/ninja" ]; then
		fetch_debs "$SYSROOT" cmake cmake-data ninja-build libcurl4-openssl-dev libjsoncpp26 librhash1 libuv1t64 libarchive13t64
	fi
fi
if [ -d "$SYSROOT/usr/bin" ]; then
	export PATH="$SYSROOT/usr/bin:$PATH"
	export LD_LIBRARY_PATH="$SYSROOT/usr/lib/$(uname -m)-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
	export CMAKE_ROOT="$SYSROOT/usr/share/cmake-$(cmake --version | awk 'NR==1{split($3,v,"."); print v[1]"."v[2]}')"
fi
CURL_ARGS=()
if [ -f "$SYSROOT/usr/include/$(uname -m)-linux-gnu/curl/curl.h" ]; then
	lib=$(ls /usr/lib/$(uname -m)-linux-gnu/libcurl.so.4 2>/dev/null || true)
	CURL_ARGS=(-DCURL_INCLUDE_DIR="$SYSROOT/usr/include/$(uname -m)-linux-gnu" -DCURL_LIBRARY="$lib")
fi
SRC=$(mktemp -d "${TMPDIR:-/tmp}/hk-tr-build.XXXXXX")
trap 'rm -rf "$SRC"' EXIT
tar -xJf "$TARBALL" --no-same-owner -C "$SRC"
# The 4.0.6 release tarball ships an empty third-party/miniupnpc while the
# same sources are included under third-party/miniupnp/miniupnpc. UPnP is
# not on the RPC path; using the bundled copy keeps the build self-contained.
T="$SRC/transmission-$VERSION/third-party"
if [ -d "$T/miniupnpc" ] && [ -z "$(ls -A "$T/miniupnpc")" ] && [ -d "$T/miniupnp/miniupnpc" ]; then
	rmdir "$T/miniupnpc" && cp -a "$T/miniupnp/miniupnpc" "$T/miniupnpc"
fi
cmake -S "$SRC/transmission-$VERSION" -B "$SRC/build" -G Ninja -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$PREFIX" \
	-DENABLE_DAEMON=ON -DENABLE_GTK=OFF -DENABLE_QT=OFF -DENABLE_MAC=OFF -DENABLE_CLI=OFF -DENABLE_UTILS=OFF \
	-DENABLE_TESTS=OFF -DENABLE_NLS=OFF -DINSTALL_WEB=OFF -DENABLE_WEB=OFF -DINSTALL_DOC=OFF "${CURL_ARGS[@]}" >&2
cmake --build "$SRC/build" -j "$JOBS" --target transmission-daemon >&2
install -Dm755 "$SRC/build/daemon/transmission-daemon" "$PREFIX/bin/transmission-daemon"
echo "$PREFIX/bin/transmission-daemon"
