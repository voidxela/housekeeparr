# Implementation status

This checklist tracks implementation against [the specification](SPEC.md). It
separates working-and-tested code from unverified assumptions and remaining v1
scope. Nothing here reduces v1 scope; every item marked *remaining* is still
required for v1.

Legend: **done** = implemented with automated tests that pass in this
repository · **unverified** = implemented, but behaviour against real
released applications has not been established · **remaining** = not
implemented.

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
| qBittorrent read adapter (WebUI API v2) | unverified | Contract-tested against fixtures derived from released source at `release-4.6.7` and `release-5.3.0`; never run against a live instance. |
| Sonarr read adapter (API v3; queue, health, status) | unverified | Contract-tested against fixtures derived from released source at `v4.0.20.3014`; never run against a live instance. |
| Radarr read adapter (API v3; queue, health, status) sharing the *arr v3 client with Sonarr | unverified | Contract-tested against fixtures derived from released source at `v6.4.4.10685`; never run against a live instance; the harness does not start Radarr yet. |
| Transmission read adapter (legacy RPC with session handshake) | unverified | Contract-tested against fixtures derived from `docs/rpc-spec.md` and `transmission.h` at `4.0.6`; verified progress is `sizeWhenDone − leftUntilDone`; availability is wanted-only (`desiredAvailable`). Uses the legacy protocol that Transmission 4.1 documents as deprecated — JSON-RPC 2.0 support is *remaining*. Swarm counts are not collected (they are only available alongside tracker URLs). Never run against a live instance. |
| Deluge read adapter (deluge-web JSON API; daemon protocol deliberately not supported) | unverified | Contract-tested against fixtures derived from `deluge/core/torrent.py` and `deluge/ui/web/json_api.py` at `deluge-2.1.1`. deluge-web must already be connected to a daemon. `distributed_copies` = 0 and `private` without metadata are treated as unknown because Deluge reports ambiguous values there. Never run against a live instance. |
| Pure no-progress / metadata diagnosis policy | done | Deterministic tests cover grace, windows, observation counts, gaps, clock anomalies, pauses, rechecks, file selection, near-complete windows, and piece availability. |
| Persistent findings (dedup, recurrence, acknowledge, snooze, evidence-based resolution) | done | |
| Observation engine (bounded polling, backoff, correlation, evaluation) | done | Engine tests use fake adapters and a deterministic clock; see `internal/engine/engine_test.go`. |
| Shared application services (`internal/app`) used by both CLI and web | done | Pages and read commands read local state only. |
| CLI: `run` (daemon / `--once` / `--headless`), `status`, `findings`, `finding`, `downloads`, `connections`, `policy`, `activity`, `ack`, `snooze`, `reopen`, `check`, `config validate`, `demo`, `--json` | done | End-to-end test runs the real adapters against fixture-backed HTTP servers (`cmd/housekeeparr/main_test.go`). Local mutations take the writer lock or refuse with exit code 3; routing them through a running headless worker is *remaining*. |
| Web interface: overview, needs attention, finding detail with ack/snooze/reopen, downloads, download detail, activity, connections & policy, JSON API, `/healthz` | done | Loopback only; Host allowlist, strict CSP, same-origin + CSRF checks on mutations. Exercised in tests and manually via `curl`; not yet reviewed in a real browser. |
| Demo mode (`housekeeparr demo`) | done | Real engine over scripted fakes and a simulated clock; state marked `demo`, refused as live state; persistent banner in UI and CLI. |
| Integration harness against disposable released instances (`scripts/it/run.sh`) | unverified | Written: pinned, checksum-verified qBittorrent 5.3.0 / 4.6.7 and Sonarr 4.0.20 releases on loopback with locally generated torrents (closed-port tracker, DHT/PeX/LSD off); Go assertions in `internal/integration` (build tag `integration`). **Never executed**: running downloaded release binaries was not permitted in the development environment. |
| Packaging (container image, user-level systemd unit) | remaining | |

## Remaining v1 scope (not started unless noted)

- rTorrent read adapter (all four clients remain in v1 scope); Radarr,
  Transmission, and Deluge in the integration harness; Transmission JSON-RPC 2.0.
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

No application version is currently claimed as tested. The first harness run is expected to confirm or correct, in particular: qBittorrent state names and `private`/`has_metadata` fields per version, the claim that qBittorrent availability counts the client's own pieces, and whether Sonarr 4 reports torrents of unknown series in the queue with the infohash as `downloadId`. Every adapter's
`TestedVersions` list is empty, so probes report `untested`. Fixtures under
`internal/adapter/*/testdata` are synthetic; their READMEs record the released
source they were derived from.

## Environment limitations observed during development

- `go test -race` cannot run on the development host (ThreadSanitizer:
  unsupported 39-bit VMA range). Concurrency code has not been race-checked.
- No container runtime was available, so no container image has been built.
