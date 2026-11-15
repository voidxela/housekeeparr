# Implementation status

This checklist tracks implementation against [the specification](SPEC.md). It
separates working-and-tested code from unverified assumptions and remaining v1
scope. Nothing here reduces v1 scope; every item marked *remaining* is still
required for v1.

Legend: **done** = implemented with automated tests that pass in this
repository · **unverified** = implemented, but behaviour against real
released applications has not been established · **remaining** = not
implemented.

## Independent review — 2026-11-15

[Review findings and verification](reviews/2026-11-15.md) confirmed the
read-only foundation and both existing integration combinations, and
identified four evidence/lifecycle defects. All four are fixed with regression
tests that fail on the reviewed code:

- **R1** (malformed listings as authoritative absence): Sonarr/Radarr queue
  pages require `totalRecords` and `records`; record counts must account for
  the reported total or the snapshot is incomplete; queue rows need an `id`;
  health must be a list. Transmission requires `arguments.torrents`;
  qBittorrent and Deluge reject null listings; rTorrent rejects non-integer
  numeric columns. `internal/engine/listing_evidence_test.go` drives the real
  Transmission and Sonarr adapters through the engine to show malformed
  responses cannot remove transfers or queue rows or resolve findings, while
  explicit empty listings still do.
- **R2** (recheck counters as progress): stall resolution needs a fresh
  downloading observation; a check marks the baseline pending, and the first
  downloading observation afterwards becomes the new baseline. Limitation: a
  check that starts and finishes entirely between two polls is
  indistinguishable from download progress unless counters fall.
- **R3** (unknown states as error recovery): only recognised non-error states
  on two consecutive, fresh, gap-free observations resolve a client-error
  finding.
- **R4** (absent Transmission metadata): absent, null, or out-of-range
  `metadataPercentComplete` makes sizes, completion, privacy, and
  availability unknown; a non-numeric value rejects the listing.

The native race detector failure was reproduced and diagnosed as a 39-bit
address-space limitation; a supported runner is still required.
[Next-session prompt](CLAUDE_NEXT_SESSION.md) records the user-approved
testing decisions and implementation sequence.

## Milestone 1 — read-only foundation (in progress)

| Area | Status | Notes |
| --- | --- | --- |
| Go module, toolchain, build/vet/test commands | done | See [AGENTS.md](../AGENTS.md). |
| Versioned TOML configuration, validation, secret references (`env:`/`file:`) | done | Literal secrets, unknown keys, non-loopback web binds, plain HTTP to non-local hosts, and unimplemented integrations are rejected. |
| Effective policy resolution with per-setting provenance | done | Global defaults → `[policy]` → `[qbittorrent.<id>.policy]`. Scoped rules, item overrides, and exclusions are *remaining*. |
| Embedded state store, OS-level single-writer lock, read-only opens | done | SQLite (pure Go), WAL, `synchronous=FULL`, `flock`. Unix only; other platforms refuse to run as writer. |
| Versioned forward migrations with pre-migration backup | done | v1 → v2 tested against a real v1 database. |
| Bounded retention (observations, checks, resolved findings, audit) | done | Unresolved findings and transfers they reference are never pruned. |
| Adapter contract: capabilities, classified errors, bounded HTTP transport, redaction | done | No adapter exposes an operational write method. |
| qBittorrent read adapter (WebUI API v2) | done | Observation validated against real qBittorrent 5.3.0 and 4.6.7 instances on 2026-11-15 (scope in [compatibility.md](compatibility.md)); captured, sanitized responses are contract fixtures. Other versions: contract-tested only. |
| Sonarr read adapter (API v3; queue, health, status) | done | Observation validated against a real Sonarr 4.0.20.3014 instance on 2026-11-15 (unknown-series queue items only; scope in [compatibility.md](compatibility.md)). |
| Radarr read adapter (API v3; queue, health, status) sharing the *arr v3 client with Sonarr | done | Observation validated against a real Radarr 6.4.4.10685 instance on 2026-11-15 (unknown-movie queue items only; scope in [compatibility.md](compatibility.md)); fixtures derived from released source at `v6.4.4.10685`. |
| Transmission read adapter (legacy RPC with session handshake) | done | Observation validated against real 4.0.6 and 4.1.4 daemons (built from release tarballs) on 2026-11-15; captured responses are contract fixtures. The legacy protocol, deprecated but retained throughout Transmission 4, returned every requested field on both, so no second protocol is implemented; this is revisited only if a later release removes it. Verified progress is `sizeWhenDone − leftUntilDone`; availability is wanted-only (`desiredAvailable`). Swarm counts are not collected (they are only available alongside tracker URLs). |
| Deluge read adapter (deluge-web JSON API; daemon protocol deliberately not supported) | unverified | Contract-tested against fixtures derived from `deluge/core/torrent.py` and `deluge/ui/web/json_api.py` at `deluge-2.1.1`. deluge-web must already be connected to a daemon. `distributed_copies` = 0 and `private` without metadata are treated as unknown because Deluge reports ambiguous values there. Never run against a live instance. |
| rTorrent read adapter (XML-RPC over an HTTP SCGI gateway) | done | Observation validated against real rTorrent 0.9.8 behind lighttpd 1.4.79 `mod_scgi` with basic auth on 2026-11-15. Fixtures derived from `src/command_download.cc` at `v0.9.8` and `v0.16.28`; later versions contract-tested only. No availability or structured error state. |
| Pure no-progress / metadata diagnosis policy | done | Deterministic tests cover grace, windows, observation counts, gaps, clock anomalies, pauses, rechecks, file selection, near-complete windows, and piece availability. |
| Persistent findings (dedup, recurrence, acknowledge, snooze, evidence-based resolution) | done | |
| Observation engine (bounded polling, backoff, correlation, evaluation) | done | Engine tests use fake adapters and a deterministic clock; see `internal/engine/engine_test.go`. |
| Shared application services (`internal/app`) used by both CLI and web | done | Pages and read commands read local state only. |
| CLI: `run` (daemon / `--once` / `--headless`), `status`, `findings`, `finding`, `downloads`, `connections`, `policy`, `activity`, `ack`, `snooze`, `reopen`, `check`, `config validate`, `demo`, `--json` | done | End-to-end test runs the real adapters against fixture-backed HTTP servers (`cmd/housekeeparr/main_test.go`). Local mutations take the writer lock or refuse with exit code 3; routing them through a running headless worker is *remaining*. |
| Web interface: overview, needs attention, finding detail with ack/snooze/reopen, downloads, download detail, activity, connections & policy, JSON API, `/healthz` | done | Loopback only; Host allowlist, strict CSP, same-origin + CSRF checks on mutations. Exercised in tests and manually via `curl`; not yet reviewed in a real browser. |
| Demo mode (`housekeeparr demo`) | done | Real engine over scripted fakes and a simulated clock; state marked `demo`, refused as live state; persistent banner in UI and CLI. |
| Integration harness against disposable released instances (`scripts/it/run.sh`) | done | Pinned, checksum-verified qBittorrent 5.3.0 / 4.6.7 and Sonarr 4.0.20 releases on loopback with locally generated torrents (closed-port tracker, DHT/PeX/LSD off). Executed and passing on 2026-11-15 (aarch64). Selectable per application (`--clients`, `--managers`); a required test that skips fails the run. Transmission is built from pinned release tarballs (`scripts/it/build-transmission.sh`); rTorrent and lighttpd come from pinned Debian packages extracted into the cache, never installed. Deluge is not in the harness yet. |
| Packaging: `Dockerfile` (distroless, non-root, headless by default), user-level systemd unit | unverified | Written but never built or run here (no container runtime or user systemd session). The web interface cannot be exposed from a container until authentication exists. See `packaging/README.md`. |

## Remaining v1 scope (not started unless noted)

- Deluge in the integration harness (no root package install was
  permitted; its Python dependencies must be extracted like rTorrent's).
  Observation of qBittorrent, Transmission 4.0.6/4.1.4, rTorrent 0.9.8,
  Sonarr, and Radarr is validated against real instances (see
  [compatibility.md](compatibility.md)); correlation is validated only with
  qBittorrent as the managers' download client, and only for unknown
  series/movie queue rows.
- Exclusions, snooze/exclude from the interfaces, scoped rules and item
  overrides, effective-policy and action previews.
- Durable action coordinator (intent journal, locks, budgets, reconciliation,
  uncertain-outcome holds) and controlled automatic mode.
- Stalled-grab recovery, metadata recovery, preserved-data failure handling —
  each requires version-tested manager/client semantics first.
- Sonarr partial season-pack handling (import history, coverage evidence,
  association clearing, targeted searches).
- Bounded proactive missing searches, budgets, and search-coverage findings.
- Retained-data and capacity tracking.
- Notifications.
- Web authentication for remote exposure (currently loopback only).
- Container packaging, user-level systemd unit, release validation,
  performance budgets, security review.

## Compatibility

Observation is validated for qBittorrent 5.3.0 and 4.6.7, Transmission 4.0.6
and 4.1.4, rTorrent 0.9.8 (lighttpd SCGI gateway), Sonarr 4.0.20.3014, and
Radarr 6.4.4.10685 (see [compatibility.md](compatibility.md) for exactly what
was exercised). Deluge is contract-tested against source-derived fixtures
only. No write workflow exists for any application.

## Performance measurements

`HK_WORKLOAD=1 go test ./internal/engine -run ReferenceWorkload -v` simulates
one day (144 ten-minute cycles) of a larger multi-instance install: two
clients × 2,000 transfers (100 incomplete each, 60 of them stalled), Sonarr and
Radarr with 150 queue rows each. Fake adapters, so network time is excluded.
Measured on the development host (aarch64, 8 cores, shared VM):

| Version | Mean cycle | Worst cycle | State after 1 day | Heap in use |
| --- | --- | --- | --- | --- |
| before optimization | 5.03 s | 5.89 s | 33.3 MiB | 2.5 MiB |
| current | 1.36 s | 1.73 s | 37.1 MiB | 2.9 MiB |

Optimizations: in-memory correlation index (was a full table scan per queue
row), bulk previous-state read, no rewrite or re-serialization of unchanged
complete transfers, per-client loading of findings and queue relationships.

These are measurements, not release budgets. Known concern: observation rows
store verbose JSON signals, so a library with many long-stalled transfers
grows by tens of MiB per day under the default 7-day retention; a compact
observation encoding is *remaining*. The typical-small-installation workload,
CPU/memory budgets on documented modest hardware, and interface latency
budgets required by the specification are also *remaining*.

## Environment limitations observed during development

- `go test -race` cannot run on the development host (ThreadSanitizer:
  unsupported 39-bit VMA range). Concurrency code has not been race-checked.
- No container runtime was available, so no container image has been built.
