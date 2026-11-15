# Packaging

Status: **unverified**. Neither the container image nor the systemd unit has
been built or run in this repository's development environment (no container
runtime or user systemd session was available). Treat both as reviewed
starting points, not tested artefacts.

## Native binary

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --always)" -o bin/housekeeparr ./cmd/housekeeparr
```

The binary is static (pure-Go SQLite), needs no root, opens no listener in
headless mode, and keeps all state in the configured state directory. Linux
and other Unix systems only: other platforms refuse to run as the state
writer until an OS-level lock is validated for them.

## User-level systemd

See `systemd/housekeeparr.service`. It keeps configuration and state under
`~/.config/housekeeparr`, restarts on failure with a 30-second delay, and
relies on `housekeeparr status` (exit 1 when the worker heartbeat is stale)
for external health checks.

## Container

```sh
docker build -t housekeeparr --build-arg VERSION=$(git describe --always) .
docker run -d --name housekeeparr -v /srv/housekeeparr:/config housekeeparr
```

Put `housekeeparr.toml` in the mounted directory (relative `state.dir`
resolves next to it). Secrets can come from mounted files (`file:/run/secrets/...`)
or environment variables (`env:NAME`). The image runs as an unprivileged user.

**Web interface in containers:** the interface binds to loopback only and
refuses other addresses, because remote exposure requires authentication that
this release does not implement. Inside a container, loopback is not reachable
from the host. Until authentication exists, either run headless (the default
`CMD`), use host networking with `listen = "127.0.0.1:8484"`, or reach the
interface with `docker exec`/an SSH tunnel. Housekeeparr will not silently bind
to all interfaces.
