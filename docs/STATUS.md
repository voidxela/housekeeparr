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
| Pure no-progress / metadata diagnosis policy | done | Deterministic tests cover grace, windows, observation counts, gaps, clock anomalies, pauses, rechecks, file selection, near-complete windows, and piece availability. |
| Persistent findings (dedup, recurrence, acknowledge, snooze, evidence-based resolution) | done | |
| Observation engine (bounded polling, backoff, correlation, evaluation) | done | Engine tests use fake adapters and a deterministic clock; see `internal/engine/engine_test.go`. |
| Shared application services, CLI | remaining | |
| Web interface | remaining | |
| Demo mode (clearly separated synthetic data) | remaining | `internal/fake` exists; demo command does not. |
| Integration harness against disposable released instances | remaining | Running downloaded release binaries was not permitted in the development environment; see "Compatibility". |

## Remaining v1 scope (not started unless noted)

- Radarr read adapter; Transmission, rTorrent, and Deluge read adapters
  (all four clients remain in v1 scope).
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

No application version is currently claimed as tested. Every adapter's
`TestedVersions` list is empty, so probes report `untested`. Fixtures under
`internal/adapter/*/testdata` are synthetic; their READMEs record the released
source they were derived from.

## Environment limitations observed during development

- `go test -race` cannot run on the development host (ThreadSanitizer:
  unsupported 39-bit VMA range). Concurrency code has not been race-checked.
- No container runtime was available, so no container image has been built.
