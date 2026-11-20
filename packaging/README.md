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

**Web interface in containers:** inside a container, loopback is not
reachable from the host. Run headless (the default `CMD`), use host
networking with the default loopback listener, or expose it remotely as
below. Housekeeparr never binds to all interfaces silently.

## Remote web access

A non-loopback `web.listen` (for example `0.0.0.0:8484`) is refused unless
`web.password` (a secret reference), `web.allowed_hosts` (every host name the
interface is reached by) and TLS are configured. Both topologies below are
exercised by `TestWebDeployment` in the integration suite with the production
binary: sign-in, Secure/HttpOnly/SameSite=Strict session cookies, per-session
CSRF tokens, cross-site refusal, HTTP Basic for the read-only API, sign-out,
session expiry, sessions ending at restart, and the sign-in failure limit.

**Direct TLS:** set `tls_cert` and `tls_key` (absolute paths; TLS 1.2 or
later, HSTS is sent).

**TLS-terminating reverse proxy:** listen on an address only the proxy can
reach and set `trusted_proxy = true` (cookies are then marked Secure). A
declared proxy makes the interface reachable from other machines, so it
requires `web.password` and `allowed_hosts` even when `listen` is loopback;
`config validate` and the server refuse it otherwise. (A loopback listener
without a proxy, with or without `base_path` or its own TLS files, stays
local-only and needs neither.) The proxy must pass the browser's original
`Host` header *with its port*, because state-changing requests are accepted
only when their `Origin` (which includes a non-default port) matches it. In
nginx that is `$http_host`; `$host` drops the port, so on a port other than
443 every sign-in and form would be refused, with a message naming the
header. Housekeeparr reads no `X-Forwarded-*`
header. A manual action is answered only once it has finished (up to about
six times `action_timing.settle`), so the proxy's read timeout must be at
least `web.write_timeout` (default 2 minutes). For nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:8484;
    proxy_set_header Host $http_host;
    proxy_read_timeout 150s;
}
```

**Shared host name under a path** (for example Ultra.cc, where each
application has its own `location` on one HTTPS host): set
`base_path = "/housekeeparr"`, `trusted_proxy = true` and the shared host in
`allowed_hosts`, keep `listen` on loopback, and proxy the path without
rewriting it. Links, assets, redirects and the session cookie's path all
use the prefix. Requests outside the prefix get a 404 after the same Host
check and security headers as every other response.

Browsers isolate sites by scheme, host and port, not by path. Applications
sharing one host name are one origin: script running in another
application on that host (for example through a cross-site scripting flaw
in it) could read Housekeeparr's pages and submit its forms with the
signed-in browser's session. The cookie path only keeps the session cookie
out of other applications' requests. Where the host offers per-application
subdomains, prefer one for Housekeeparr.

```nginx
location /housekeeparr/ {
    proxy_pass http://127.0.0.1:8484;
    proxy_set_header Host $http_host;
    proxy_read_timeout 150s;
}
```

Listener timeouts are `web.read_header_timeout` (5 s), `read_timeout`
(15 s), `write_timeout` (2 min, at least six times `action_timing.settle`)
and `idle_timeout` (1 min). A manual action keeps running if the browser or
proxy gives up; its outcome is on the finding and Activity pages.
