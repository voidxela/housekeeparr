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

## Independent follow-up review — 2026-11-16

[Review and decision dispositions](reviews/2026-11-16.md) at `73d37d5`
confirmed progress but reproduced two safety gaps: live checks omit privacy
and content changes and run only before the first write (R5); content
locks/budgets/overrides use a release-dependent identity (R6). Budget
configuration, preview visibility and exhaustion semantics remain incomplete
(R7). Milestone 2 remains in progress; fix these before expanding writes or
enabling automatic dispatch. Existing unit/contract tests pass, but three
review overlay tests fail as expected. No new real-instance matrix run was
performed. [Handoff](CLAUDE_NEXT_SESSION.md) includes all five decision
reviews, retained agy authorization and tiered validation to improve velocity.

## Milestone 1 — read-only foundation (in progress)

| Area | Status | Notes |
| --- | --- | --- |
| Go module, toolchain, build/vet/test commands | done | See [AGENTS.md](../AGENTS.md). |
| Versioned TOML configuration, validation, secret references (`env:`/`file:`) | done | Literal secrets, unknown keys, non-loopback web binds, plain HTTP to non-local hosts, and unimplemented integrations are rejected. |
| Effective policy resolution with per-setting provenance | done | Global defaults → `[policy]`/`[automation.workflows]` → `[<client>.<id>.policy]` → `[[rule]]` by priority → `[[override]]`; `[[exclude]]` always applies. See milestone 2. |
| Embedded state store, OS-level single-writer lock, read-only opens | done | SQLite (pure Go), WAL, `synchronous=FULL`, `flock`. Unix only; other platforms refuse to run as writer. |
| Versioned forward migrations with pre-migration backup | done | v1 → v2 tested against a real v1 database. |
| Bounded retention (observations, checks, resolved findings, audit) | done | Unresolved findings and transfers they reference are never pruned. |
| Adapter contract: capabilities, classified errors, bounded HTTP transport, redaction | done | Write methods exist only for Sonarr/Radarr queue removal and qBittorrent stop (milestone 3). |
| qBittorrent read adapter (WebUI API v2) | done | Observation validated against real qBittorrent 5.3.0 and 4.6.7 instances on 2026-11-15 (scope in [compatibility.md](compatibility.md)); captured, sanitized responses are contract fixtures. Other versions: contract-tested only. |
| Sonarr read adapter (API v3; queue, health, status) | done | Observation validated against a real Sonarr 4.0.20.3014 instance on 2026-11-15 (unknown-series queue items only; scope in [compatibility.md](compatibility.md)). |
| Radarr read adapter (API v3; queue, health, status) sharing the *arr v3 client with Sonarr | done | Observation validated against a real Radarr 6.4.4.10685 instance on 2026-11-15 (unknown-movie queue items only; scope in [compatibility.md](compatibility.md)); fixtures derived from released source at `v6.4.4.10685`. |
| Transmission read adapter (legacy RPC with session handshake) | done | Observation validated against real 4.0.6 and 4.1.4 daemons (built from release tarballs) on 2026-11-15; captured responses are contract fixtures. For observation, the legacy protocol (deprecated but retained throughout Transmission 4) returned every requested field on both, so no second protocol is implemented. That is established for observation only; each later write workflow must be validated on both branches before the same conclusion holds for it. Harness-side setup already used `torrent-add`, `torrent-set`, `torrent-verify`, `torrent-stop`, and `torrent-start` over the legacy protocol on 4.0.6 and 4.1.4 (harness evidence, not a Housekeeparr workflow). Verified progress is `sizeWhenDone − leftUntilDone`; availability is wanted-only (`desiredAvailable`). Swarm counts are not collected (they are only available alongside tracker URLs). |
| Deluge read adapter (deluge-web JSON API; daemon protocol deliberately not supported) | done | Observation validated against real Deluge 2.2.0 on 2026-11-15; that run found and fixed two adapter bugs the source-derived fixtures had hidden (`daemon.info` does not exist in 2.x; empty params must be `[]`). deluge-web must already be connected to a daemon. `distributed_copies` = 0 and `private` without metadata are treated as unknown. 2.0.x and 2.1.x are contract-tested only. |
| rTorrent read adapter (XML-RPC over an HTTP SCGI gateway) | done | Observation validated against real rTorrent 0.9.8 behind lighttpd 1.4.79 `mod_scgi` with basic auth on 2026-11-15. Fixtures derived from `src/command_download.cc` at `v0.9.8` and `v0.16.28`; later versions contract-tested only. No availability or structured error state: a download rTorrent stops itself with a non-tracker `d.message` is reported as a client error (observed after payload loss). |
| Pure no-progress / metadata diagnosis policy | done | Deterministic tests cover grace, windows, observation counts, gaps, clock anomalies, pauses, rechecks, file selection, near-complete windows, and piece availability. |
| Persistent findings (dedup, recurrence, acknowledge, snooze, evidence-based resolution) | done | |
| Observation engine (bounded polling, backoff, correlation, evaluation) | done | Engine tests use fake adapters and a deterministic clock; see `internal/engine/engine_test.go`. |
| Shared application services (`internal/app`) used by both CLI and web | done | Pages and read commands read local state only. |
| CLI: `run` (daemon / `--once` / `--headless`), `status`, `findings`, `finding`, `downloads`, `connections`, `policy`, `activity`, `ack`, `snooze`, `reopen`, `check`, `config validate`, `demo`, `--json` | done | End-to-end test runs the real adapters against fixture-backed HTTP servers (`cmd/housekeeparr/main_test.go`). Local mutations take the writer lock or refuse with exit code 3; routing them through a running headless worker is *remaining*. |
| Web interface: overview, needs attention, finding detail with ack/snooze/reopen, downloads, download detail, activity, connections & policy, JSON API, `/healthz` | done | Loopback only; Host allowlist, strict CSP, same-origin + CSRF checks on mutations. Exercised in tests and manually via `curl`; not yet reviewed in a real browser. |
| Demo mode (`housekeeparr demo`) | done | Real engine over scripted fakes and a simulated clock; state marked `demo`, refused as live state; persistent banner in UI and CLI. |
| Integration harness against disposable released instances (`scripts/it/run.sh`) | done | Pinned, checksum-verified qBittorrent 5.3.0 / 4.6.7 and Sonarr 4.0.20 releases on loopback with locally generated torrents (closed-port tracker, DHT/PeX/LSD off). Executed and passing on 2026-11-15 (aarch64). Selectable per application (`--clients`, `--managers`); a required test that skips fails the run. Transmission is built from pinned release tarballs (`scripts/it/build-transmission.sh`); rTorrent and lighttpd come from pinned Debian packages extracted into the cache, never installed. Deluge 2.2.0 likewise. Runs are isolated in a loopback-only network namespace by default. |
| Packaging: `Dockerfile` (distroless, non-root, headless by default), user-level systemd unit | unverified | Written but never built or run here (no container runtime or user systemd session). The web interface cannot be exposed from a container until authentication exists. See `packaging/README.md`. |

## Milestone 2 — policy and durable action foundation (in progress)

The first write workflow exists (see milestone 3). Everything in this
table is exercised with fakes, plus the real-instance recovery runs.

| Area | Status | Notes |
| --- | --- | --- |
| Modes `observe` / `paused` / `automatic`, `[automation] emergency_stop`, per-workflow enablement (`no_progress_recovery`, `metadata_recovery`, `preserved_data_failure`, `partial_pack`, `missing_search`; all off by default) | done | `automatic` is accepted but permits nothing without a tested workflow. Interpretation (proposed, not yet in SPEC): manual actions are refused in `observe` (no operational writes at all) and allowed in `paused` (which stops automation only); emergency stop blocks both. |
| Scoped rules (`[[rule]]`, priority, match on client/category/manager/privacy), item overrides (`[[override]]` by transfer or content, reason required), exclusions (`[[exclude]]`, per workflow) with provenance in every effective setting | done | Equal-priority rules with possibly overlapping matchers and conflicting values are validation errors. The effective revision covers settings, permissions, exclusions, mode, and emergency stop. Implemented by a delegated agent (Gemini via `agy`) in an isolated worktree, reviewed, and applied as one commit. |
| Action journal: migration 0003 (intents, steps, locks, budgets, cooldowns), upgrade test from a real v2 database | done | Intent, steps, locks, and budget reservation are committed in one transaction before any request; each step is committed as `dispatching` before it is sent. Uncertain intents keep their locks until reviewed. Budgets persist across restart. |
| Restore detection | done | A journal high-water marker beside the database (`journal.hwm`, fsynced) detects a restored or replaced database; writes are held until `housekeeparr restore-ack --confirm`. |
| Action coordinator (`internal/action`) | implemented; safety closure remaining (R5) | Gate and prerequisites, preview expiry, pre-write re-fetch compared with the preview fingerprint, journaled dispatch, postcondition verification by observation, conservative error classification (only unauthorized/unsupported/rate-limited/4xx count as refused; a timeout or malformed 2xx response is uncertain), no blind retry, partial-workflow holds, graceful stop that persists the current step, startup `Reconcile` that observes but never re-sends, and operator `Review`. Fault tests: accepted-then-timed-out, timeout reconciled by observation, accepted-but-not-effective, partial failure, crash between network and commit, crash before dispatch, stop between steps, unwritable store, restored database, budget across restart, duplicate execution. |
| Recovery planner (`action.Plan`) | implemented; gaps R5–R7 | Proposal for stalled, metadata-timeout, and client-error findings with every prerequisite stated: tested workflow for the exact versions, finding active, fresh evidence, qualified diagnosis, applicable state (never paused/queued/checking), known-public privacy, exactly one matched manager association, no lock, no cooldown, policy. Operations carry every flag explicitly, using the managed-row semantics recorded in compatibility.md (`removeFromClient=false&blocklist=true&skipRedownload=false&changeCategory=false`: the manager owns the replacement search), then a client stop that retains data. Budget limits (20/day global, nominally 3/week per content item) are hard-coded proposals; configuration and exhaustion visibility are *remaining*. Review R6 found that the supposed content key includes the download ID, so replacements do not share the intended retry allowance/content lock. Review R5 found incomplete live safety checks; these are not closed foundation requirements. |
| Shared previews and manual actions | done | `housekeeparr preview <id> [--automatic] [--json]`, `act <id> --confirm`, `actions`, `review <id> --as … --note …`, `restore-ack --confirm`; web finding page shows the same preview, with an action form (confirmation required) only when eligible; `GET /api/v1/findings/{id}/preview`; Activity lists journaled actions and restore holds. `run` reports holds and actions that need review at start-up. Not yet reviewed in a real browser. |
| Retention of terminal intents | done | Succeeded/failed/cancelled intents, expired cooldowns, and old budget windows are pruned after the audit-event retention; uncertain and active intents and their locks never are, and pruning never lowers the restore high-water mark. |
| Routing CLI mutations through a running worker | remaining | `act`, `review`, and `restore-ack` need the writer lock (exit 3 while the worker runs), like `ack`. |

## Milestone 3 — recovery (in progress)

| Area | Status | Notes |
| --- | --- | --- |
| Adapter writes: `RemoveQueueItem` (Sonarr/Radarr, all four flags always sent; timeout = uncertain), `Blocklisted`, `HistoryEvents`; qBittorrent `Stop` (`torrents/stop` on 5.x, `torrents/pause` on 4.x) | done | Contract tests plus real-instance runs. Released `BlocklistResource` has no infohash, so a blocklist entry is matched by exact source title, content id, and time since dispatch: evidence, not identity (two releases sharing a title in the same window would be indistinguishable). |
| Recovery executor (`internal/workflow`) | done | Refuses implicit or destructive flags (`removeFromClient=true`, missing flags, a stop without explicit data retention) before any request; verifies each step by fresh reads with a bounded settle time. |
| Complete no-progress recovery on real instances | **real-instance-tested** for Sonarr 4.0.20.3014 + qBittorrent 5.3.0 and Radarr 6.4.4.10685 + qBittorrent 5.3.0 | `internal/integration/recovery_test.go`; registered in `action.Tested`. Manual dispatch only (`act --confirm` or the web form), and only outside observe mode. The diagnosis is supplied in the test. qBittorrent 4.x is refused (privacy unknown), and the test asserts that. |
| Automatic dispatch from the engine | remaining | Nothing invokes the coordinator automatically yet, even in `automatic` mode. |
| Replacement outcome tracking (search accepted, grab, progress as separate outcomes) | remaining | The workflow verifies its own steps and that the manager queued a search; what follows is not tracked yet. |
| Metadata recovery, preserved-data failure handling | remaining | Planner proposals exist; no tested workflow. Missing data on qBittorrent/Deluge is not visible in client state. |
| Transmission, Deluge, rTorrent recovery | remaining | Managed-row probes passed with all four clients, including both Transmission branches; each still needs its own `Stop` and full Housekeeparr recovery workflow run before registration. |
| Retained-data and capacity limits | remaining | Recovery retains the stopped transfer; its size is not yet tracked against ceilings. Preview text claiming retained-data accounting or subsequent outcome verification describes intended behavior, not implemented tracking. |

## Remaining v1 scope (not started unless noted)

- Observation of all four clients (qBittorrent 5.3.0/4.6.7, Transmission
  4.0.6/4.1.4, rTorrent 0.9.8, Deluge 2.2.0), Sonarr, and Radarr is validated
  against real instances (see
  [compatibility.md](compatibility.md)); correlation is validated only with
  qBittorrent as the managers' download client. Managed content with real
  grab history (fictional series/movies through the metadata and Torznab
  stand-ins) is exercised for queue-removal semantics only; imports and
  season packs are not.
- Exclude from the interfaces (exclusions are configuration-only so far);
  configurable budgets; automatic dispatch from the engine (the coordinator
  exists; nothing invokes it automatically yet).
- Stalled-grab recovery beyond the tested qBittorrent 5.3.0 combinations,
  automatic dispatch, and outcome tracking; metadata recovery and
  preserved-data failure handling (see milestone 3).
- Sonarr partial season-pack handling (import history, coverage evidence,
  association clearing, targeted searches).
- Bounded proactive missing searches, budgets, and search-coverage findings.
- Retained-data and capacity tracking.
- Notifications.
- Web authentication for remote exposure (currently loopback only).
- Container packaging, user-level systemd unit, release validation,
  performance budgets, security review.

## Design-constraining findings (real instances, 2026-11-15)

- **Queue removal of rows with real grab history is durable** (Sonarr
  4.0.20, Radarr 6.4.4, qBittorrent 5.3.0/4.6.7 as their download client):
  the ignore path records `downloadIgnored`; the blocklist path blocklists
  exactly that release and records `downloadFailed`; `removeFromClient=false`
  keeps the torrent running in its category with data intact, even with
  `removeFailedDownloads=true`; `skipRedownload=false` makes the manager
  queue its own replacement search at once. The torrent is not stopped by
  the manager, so separation is a separate client step. The same managed-row
  removal probes passed with Transmission 4.0.6/4.1.4, Deluge 2.2.0 and
  rTorrent 0.9.8 as the managers' client; full Housekeeparr recovery remains
  untested there.
- **Queue removal of rows without grab history is accepted but not durable.**
  Sonarr 4.0.20 and Radarr 6.4.4 return HTTP 200 to `DELETE /api/v3/queue/{id}`
  (ignore and blocklist paths, every flag explicit), but the row is tracked
  again after the next refresh and nothing is blocklisted. Recovery must verify
  the outcome after a refresh, and such rows are ineligible.
- **After a forced recheck, lost data looks like a fresh download on
  qBittorrent and Deluge** (verified bytes 0, no error state); only a client
  restart produces `missingFiles`/`Error`. Preserved-data handling and the
  stall diagnosis must not treat a post-recheck reset to zero as an ordinary
  new download.
- **rTorrent local messages can be stale**: user stop/start does not clear
  them, so the stopped-with-local-message → client-error mapping can
  misreport a user-stopped torrent.
- **"Complete" does not prove the data exists on qBittorrent or Deluge.** After
  the payload of a verified torrent was deleted, both kept reporting it
  complete/seeding; Transmission and rTorrent reported errors. Preserved-data
  handling cannot rely on client state alone for libtorrent clients.
- **`removeFromClient` defaults to true** in both managers' released
  `QueueController`, so every consequential flag must be sent explicitly.

## Compatibility

Observation is validated for qBittorrent 5.3.0 and 4.6.7, Transmission 4.0.6
and 4.1.4, rTorrent 0.9.8 (lighttpd SCGI gateway), Deluge 2.2.0 (deluge-web), Sonarr
4.0.20.3014, and Radarr 6.4.4.10685 (see [compatibility.md](compatibility.md)
for exactly what was exercised). No-progress recovery is tested only for Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 with qBittorrent 5.3.0.

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
