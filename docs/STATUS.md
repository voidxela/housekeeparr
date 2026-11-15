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

[Review findings and verification](reviews/2026-11-15.md) confirm the read-only
foundation and both existing integration combinations, but identify four
remaining evidence/lifecycle defects (R1–R4). These must be fixed before
closing this milestone or enabling writes. The native race detector failure
was reproduced and diagnosed as a 39-bit address-space limitation; a
supported runner is still required. [Next-session prompt](CLAUDE_NEXT_SESSION.md)
records the user-approved testing decisions and implementation sequence.

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
| Radarr read adapter (API v3; queue, health, status) sharing the *arr v3 client with Sonarr | unverified | Contract-tested against fixtures derived from released source at `v6.4.4.10685`; never run against a live instance; the harness does not start Radarr yet. |
| Transmission read adapter (legacy RPC with session handshake) | unverified | Contract-tested against fixtures derived from `docs/rpc-spec.md` and `transmission.h` at `4.0.6`; verified progress is `sizeWhenDone − leftUntilDone`; availability is wanted-only (`desiredAvailable`). Uses the legacy protocol that Transmission 4.1 documents as deprecated — JSON-RPC 2.0 support is *remaining*. Swarm counts are not collected (they are only available alongside tracker URLs). Never run against a live instance. |
| Deluge read adapter (deluge-web JSON API; daemon protocol deliberately not supported) | unverified | Contract-tested against fixtures derived from `deluge/core/torrent.py` and `deluge/ui/web/json_api.py` at `deluge-2.1.1`. deluge-web must already be connected to a daemon. `distributed_copies` = 0 and `private` without metadata are treated as unknown because Deluge reports ambiguous values there. Never run against a live instance. |
| rTorrent read adapter (XML-RPC over an HTTP SCGI gateway) | unverified | Contract-tested against fixtures derived from `src/command_download.cc` at `v0.9.8` and `v0.16.28` (every multicall command exists at the 0.9.8 floor). No availability or structured error state. Never run against a live instance. |
| Pure no-progress / metadata diagnosis policy | done | Deterministic tests cover grace, windows, observation counts, gaps, clock anomalies, pauses, rechecks, file selection, near-complete windows, and piece availability. |
| Persistent findings (dedup, recurrence, acknowledge, snooze, evidence-based resolution) | done | |
| Observation engine (bounded polling, backoff, correlation, evaluation) | done | Engine tests use fake adapters and a deterministic clock; see `internal/engine/engine_test.go`. |
| Shared application services (`internal/app`) used by both CLI and web | done | Pages and read commands read local state only. |
| CLI: `run` (daemon / `--once` / `--headless`), `status`, `findings`, `finding`, `downloads`, `connections`, `policy`, `activity`, `ack`, `snooze`, `reopen`, `check`, `config validate`, `demo`, `--json` | done | End-to-end test runs the real adapters against fixture-backed HTTP servers (`cmd/housekeeparr/main_test.go`). Local mutations take the writer lock or refuse with exit code 3; routing them through a running headless worker is *remaining*. |
| Web interface: overview, needs attention, finding detail with ack/snooze/reopen, downloads, download detail, activity, connections & policy, JSON API, `/healthz` | done | Loopback only; Host allowlist, strict CSP, same-origin + CSRF checks on mutations. Exercised in tests and manually via `curl`; not yet reviewed in a real browser. |
| Demo mode (`housekeeparr demo`) | done | Real engine over scripted fakes and a simulated clock; state marked `demo`, refused as live state; persistent banner in UI and CLI. |
| Integration harness against disposable released instances (`scripts/it/run.sh`) | done | Pinned, checksum-verified qBittorrent 5.3.0 / 4.6.7 and Sonarr 4.0.20 releases on loopback with locally generated torrents (closed-port tracker, DHT/PeX/LSD off). Executed and passing on 2026-11-15 (aarch64). Radarr, Transmission, Deluge, and rTorrent are not in the harness yet. |
| Packaging: `Dockerfile` (distroless, non-root, headless by default), user-level systemd unit | unverified | Written but never built or run here (no container runtime or user systemd session). The web interface cannot be exposed from a container until authentication exists. See `packaging/README.md`. |

## Remaining v1 scope (not started unless noted)

- Radarr, Transmission, Deluge, and rTorrent in the integration harness;
  Transmission JSON-RPC 2.0. All four clients and both managers have read
  adapters; none is validated against a real instance (see
  [compatibility.md](compatibility.md)).
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

Observation is validated for qBittorrent 5.3.0 and 4.6.7 and Sonarr
4.0.20.3014 (see [compatibility.md](compatibility.md) for exactly what was
exercised). Radarr, Transmission, Deluge, and rTorrent are contract-tested
against source-derived fixtures only. No write workflow exists for any
application.

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
