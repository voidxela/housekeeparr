# Implementation status

This checklist tracks implementation against [the specification](SPEC.md). It
separates working-and-tested code from unverified assumptions and remaining v1
scope. Nothing here reduces v1 scope; every item marked *remaining* is still
required for v1.

Legend: **done** = implemented with automated tests that pass in this
repository · **unverified** = implemented, but behaviour against real
released applications has not been established · **remaining** = not
implemented.

## Session of 2026-11-17 — what changed

- **R11–R13 fixed** with regression tests (the review's two reproductions
  are in-tree: `TestRecoveredHashDoesNotHideOtherClientOutage`,
  `TestRetainedCapacityGatesEveryReplacement`), plus
  `TestReassociatedRecoveredTransferCountsTowardHold`,
  `TestForwardMigrationV12HandledIdentity`, `TestRetainedDispositionAndRefresh`
  and `TestPreviewDescribesCurrentTracking`. Migration 0013.
- **SPEC clarifications** (§5, §6, §7 step 4, §7.1, new §7.2) — consistency
  corrections and the preserved-data contract; no numeric default approved.
- **`require_pieces_unavailable`** diagnostic setting (agy delegation,
  reviewed), part of `Settings.Revision()`.
- **Preserved-data failure handling, first slice** (see milestone 3 row and
  compatibility.md): tracker "unregistered" rejection on Transmission and
  Deluge; real-instance-tested and registered for both managers with Transmission 4.0.6/4.1.4 and Deluge 2.2.0 (selected runs at `7b4a32b`/`c423114`).
- Local changes with no real-instance re-run: retained accounting of
  kept-running transfers (the private integration assertion was updated to
  expect a `running` record), capacity gating, client-scoped handled
  identity, preview text.
- **User decisions (2026-11-17):** private and unknown-privacy torrents
  rejected as unregistered follow `private_recovery` (default
  `keep_running`). Real-instance-tested (private variant) with Transmission
  4.0.6/4.1.4 and Deluge 2.2.0; the batch also re-ran the private
  keep-running recovery with its changed retained-record assertion.
- **Tracker classification on all four clients:** Transmission
  `error = 2`; Deluge `tracker_status` (agy, reviewed); qBittorrent
  `torrents/trackers` for active downloads, at most 25 per listing (agy,
  reviewed; statuses 5/6 of qBittorrent 5 added after the first real run
  found no classification); rTorrent `d.message` counted only when an
  enabled tracker's latest failure is recent (≤ 1 h) and newer than its
  latest success (`t.multicall`). See compatibility.md for which passed on
  real instances.
- **Automatic preserved-data dispatch on a real instance** (production
  binary, real diagnosis) passed with Sonarr and qBittorrent 5.3.0.
- **Partial season packs (SPEC §8), first slice:** a real-instance probe
  established Sonarr 4.0.20's representation (compatibility.md, "Partial
  season-pack probe"); `import.partial_pack` findings for a settled,
  import-blocked pack with the partial-pack message; `PlanPack` clears only
  the association through the ignore path (`removeFromClient=false,
  blocklist=false, skipRedownload=true, changeCategory=false`), after a
  live check that the import history for the download plus episode files
  positively cover the present episodes, the rest have neither, and no
  other import message exists; verification is rows gone plus new
  `downloadIgnored` history; a reappearing association is refused as a
  failure to diagnose. Nothing is sent to the client. Fixture-tested
  (`TestPartialPackEndToEnd`, `TestPartialPackUncertaintyRefuses`);
  real-instance result in compatibility.md. Not yet: targeted searches
  for the still-missing episodes as part of the workflow (they remain
  missing content for proactive searches), other clients, restart
  durability.
- **`source_confirm_window`** (default 30 m, proposal) is a diagnostic
  setting, part of the diagnostic revision, replacing the constant.
- **Notifications** (`[[notify]]`, migration 0014): durable outbox written
  in the cycle's transaction; findings opened (more than five at once
  become one digest), resolutions of delivered findings, automatic action
  outcomes, exhausted attempts, and the retained ceiling (daily); persisted
  hourly limit per target, backoff, drop after 8 attempts or 24 h; URLs are
  secret references and never logged; `housekeeparr notifications` and the
  Activity page show delivery. Bounds are proposals.
- **Web authentication** (`web.password`, `allowed_hosts`, `tls_cert`/
  `tls_key` or `trusted_proxy`): signed session cookies (HttpOnly,
  SameSite=Strict, Secure off loopback; 12 h; per process), per-session
  CSRF tokens, global sign-in rate limit (5 per 15 min), local-only
  redirects, HTTP Basic for the read-only API, HSTS with TLS.
- **Proactive missing searches, first slice** (`missing_search`,
  migration 0015): the engine reads each manager's wanted/missing list
  hourly (≤ 500 items) only while some scope could enable the workflow; a
  complete read marks items gone. The dispatcher sends at most one search
  per pass for a monitored, available, missing episode or movie that is not
  queued (never a season or broad search), at least 15 minutes apart
  installation-wide, within the acquisition pacing budgets and the retained
  ceiling, through the shared coordinator (journaled, locked per content,
  revalidated live: content monitored, nothing queued for it, manager
  unchanged). Verification is acceptance only (a new search command for the
  content). A still-missing item backs off 1 day, doubling to 30 days.
  `housekeeparr missing` and the Activity page list state. Spacing, read
  cadence and backoff are proposals. Read/search adapter calls delegated to
  agy and reviewed. Real-instance-tested and registered for Sonarr
  4.0.20.3014 and Radarr 6.4.4.10685 (search accepted, release grabbed).
- **Search outcomes and coverage (SPEC §9.1–9.2), first slice:** each
  search is followed to its end in the manager's command list and recorded
  as grabbed, no result ("0 reports downloaded" on the released versions,
  never read as "no release exists"), failed, or end unverified; a
  no-result outcome notes torrent indexers the manager had disabled at the
  time (possible impact, not proof). Indexers and indexer status are read
  every cycle (names, protocol and automatic-search flag only; settings,
  URLs and keys are never decoded). Real-instance finding: released Sonarr
  4.0.20 has no `/api/v3/indexerstatus` (HTTP 404), so its sources' status
  is recorded as unknown (never healthy) and coverage notes say coverage
  could not be verified; `source.unavailable` (a disabled
  automatic torrent indexer; resolves after two consecutive reads without
  it disabled) and `source.none` (no automatic torrent indexer) findings.
  Not yet: direct per-search source diagnostics, rejection reasons, or
  grouping recurring source issues beyond one finding per indexer.
- Not run this session: browser check, race detector, Docker, qBittorrent
  4.6.7 diagnosis, automatic dispatch on real instances.

## Latest-progress review — 2026-11-17 (`f83cf4d`)

[Review and reproducible tests](reviews/2026-11-17.md) confirms the metadata/
private-recovery progress and identified three findings, **all fixed in the
session of 2026-11-17** (above):

- **R11 (high):** handled-recovery lookup is hash-global, not client-scoped.
  A deterministic reproduction recovers a hash on one client, then incorrectly
  dispatches on a second client with three correlated stalls sharing that hash.
- **R12 (high):** keep-running recovery bypasses an already-reached retained
  ceiling because the capacity check exists only in the stop branch.
- **R13 (medium):** shared previews still claim capacity and replacement-outcome
  tracking are unimplemented, contradicting current behavior.

Existing Go tests pass (cached); build, vet and formatting pass. Two review
reproductions fail as expected. No new real-instance run. Before expanding
writes, fix these and reconcile SPEC's slot-release requirement with the approved
keep-running default, define retained-size/running-transfer accounting, and add
a concrete preserved-data failure contract. Recommendations are not newly
approved defaults. [Updated handoff](CLAUDE_NEXT_SESSION.md) retains agy
parallelization authorization and targeted validation for velocity.

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

### R5–R7 closure — 2026-11-16

All three review findings are fixed with regression tests (the review's
overlay reproductions are now in-tree tests) and the qBittorrent 5.3.0 +
Sonarr/Radarr recovery runs pass again on the changed executor (see
compatibility.md):

- **R5**: before *every* write the coordinator re-evaluates the gate (finding
  still active, restore marker, effective policy unchanged and still
  permitting the workflow for its origin) and a fresh live read compared with
  the plan by `action.Revalidate`: transfer registration, state class,
  verified and wanted bytes, privacy, category, client version, every
  manager's queue rows for the download ID, and, before the manager step,
  manager version, relevant health, and content existence/monitoring. The
  comparison is step-aware: after the grab is failed, its queue row is
  expected to be gone and any row is a conflicting association. Divergence
  before the first write refuses with the failing checks; later, it holds
  the partial workflow as uncertain for review.
- **R6**: content locks, cooldowns, attempt budgets, overrides, and
  exclusions use `<manager>|episode:<id>` / `<manager>|movie:<id>`, shared by
  replacement releases. Unknown content IDs and multi-episode packs refuse
  (pack recovery untested). Legacy release-specific `content` references in
  configuration are rejected loudly. Migration 0004 carries held content
  locks and cooldowns over.
- **R7**: limits are configurable (`[automation.limits]`, proposals not
  validated defaults, cannot be disabled) with provenance, and previews show
  usage. Pacing (installation-wide and per manager, rolling window) defers
  and returns by itself with the time it returns; per-content attempts need
  review and `housekeeparr attempts reset <manager>|episode:<id> --note …`.
  An append-only attempt ledger (migration 0005, backfilled from the journal)
  survives replacements, restarts, and retention; reservations are returned
  only for provably unsent or definitely refused requests. Accepted requests
  whose effect is not seen in time are now held as uncertain, not failed.
  Blocklist/history evidence must be new relative to a baseline journaled
  with the dispatching record (migration 0006); titles match byte-exactly.

### Latest-progress review — 2026-11-16 (`f0f2ae3`)

[Review, reproductions and validation cadence](reviews/2026-11-16-latest.md)
confirmed the four-client progress and identified three defects, now fixed
with regression tests that fail on the reviewed code (local logic only; no
real-instance run was needed or performed for them):

- **R8 (high) — fixed:** the planner adds the hard prerequisite "diagnosis
  under current policy": the assessment's `PolicyRevision` must equal the
  current effective diagnostic `Settings.Revision()` for the transfer's
  scope. Missing/legacy revisions fail closed; the explanation asks for
  re-evaluation by the next observation cycle and the finding stays open.
  A mode/permission-only change (the broader effective revision) keeps the
  diagnosis usable. Tests: `TestManualActionRequiresDiagnosisUnderCurrentSettings`
  (fresh service with a 24 h window refuses a 3 h diagnosis, sends nothing),
  `TestScopedSettingsDiagnosisIsCurrentUntilEdited` (a transfer override's
  diagnosis dispatches automatically; another transfer's override does not
  invalidate it; editing its own override does), and planner cases for
  window, observation-count, unrecorded revision and mode-only changes.
  Gap: there is no "require positive piece unavailability" policy setting
  yet (SPEC §13.4 scenario 2); when added it must be part of
  `Settings.Revision()`.
- **R9 (medium) — fixed:** outcome progress/completion requires a complete
  listing of the replacement's client in the same cycle; a fresh manager
  grab alone records only the grab, and the last known state is kept.
  `TestOutcomeProgressRequiresFreshClient` covers failed, backed-off and
  incomplete client reads with a healthy manager.
- **R10 (medium) — fixed:** outcomes stay open while either the download or
  the search is unsettled; terminal downloads are no longer advanced, but
  their search is followed or expired within its bounded window, and the
  command list stops being read once both settle.
  `TestSearchSettlesIndependentlyOfDownload` covers completed and
  tracking-expired downloads, restart and command-list outage.

### Implementation proposals still hard-coded (not approved defaults)

Configurable with provenance in `[automation.limits]` (values are proposals):
acquisitions 20 and manager_acquisitions 20 per rolling 24 h, content_attempts
3 (until reset), success_cooldown 24 h, retained_transfers 25, retained_gib
none, actions_per_cycle 1, correlated_stalls 3 / correlated_percent 50.
Still constants in code, also proposals needing a product decision or
configuration: the repeated-refusal circuit breaker (2 definite refusals in
24 h per client/manager scope), 5 live evaluations per dispatch pass,
outcome tracking's "no grab observed" after 24 h and tracking window of 7
days, the 5-minute preview expiry, the executor's 15 s observation
settle time, and replacement-search observation: a post-recovery poll of up
to 30 s (twice the settle time), a 1-minute clock tolerance when no command
baseline exists, and a 1-hour follow window.

## Session of 2026-11-16 (third) — what changed

- **R8–R10 fixed** with regression tests (see the review section above).
- **Metadata recovery delivered** for all four clients with both managers
  (see Milestone 3 and compatibility.md), including the user-directed
  default that unknown privacy before metadata is acceptable.
- **Private-torrent handling** for no-progress recovery (`private_recovery`,
  default `keep_running`), by user decision; SPEC §6 updated for both.
- **Shared executor change:** blocklist evidence is matched by the grabbed
  release's title. Re-run on real instances for qBittorrent 5.3.0 and
  rTorrent 0.9.8 (no-progress, metadata, private) and for Deluge and both
  Transmission versions (metadata, private, then no-progress at `e072d85`);
  the automatic-dispatch test re-ran on qBittorrent 5.3.0 only, and a full
  unselected qBittorrent 5.3.0 run passed at `7437981`. Automatic dispatch
  on the other clients was **not** re-run (last at `118f15c`).
- **Unknown privacy is treated as private** in no-progress recovery (user
  decision), configurable per scope; fixture-tested only.
- **Harness:** `scripts/it/run.sh --only REGEX` runs selected registered
  tests (still required to pass); magnet and private managed fixtures.
- Proposals awaiting a decision: metadata stalls count toward the
  correlated-stall hold; transfers a recovery already handled are left out
  of it; a metadata-stage transfer is retained as zero bytes.
- Not run this session: browser check, race detector, Docker, qBittorrent
  4.6.7 diagnosis, real-instance automatic dispatch of metadata recovery.

## Session of 2026-11-16 (second) — what changed

- **rTorrent 0.9.8 registered.** The `errored-recheck` failure at `b8d3313`
  was a harness race (deleting a payload while rTorrent hashes it), shown by
  mechanism; the fixture now polls instead of sleeping. Clean run, then
  registration. See compatibility.md.
- **Transmission 4.0.6 and 4.1.4 registered** (legacy RPC on both). The
  harness re-verifies the grabbed release because Transmission 4 treats a
  pre-existing file under its final name as complete.
- **Defect found and fixed: diagnosis ignored scoped rules and item
  overrides.** The engine diagnosed every transfer with its client's policy,
  so an override such as `no_progress_window = "24h"` for a rare release
  did not delay its diagnosis or the recovery planned from it. Diagnosis,
  planning, and the download page now resolve the same per-transfer subject
  (`policy.TransferSubject`). `import_settle` is still resolved globally
  (import findings are per queue row).
- **Real-instance automatic dispatch** through the production
  `run --once` binary (see milestone 3 and compatibility.md).
- **Search acceptance tracked as its own outcome**, attributed against a
  pre-request command-ID baseline (an earlier time-based matcher picked the
  harness's own grab search; found by review on the real-instance matrix and
  fixed before any record).
- **New prerequisite:** the manager must redownload failed grabs
  (`autoRedownloadFailed`, and `autoRedownloadFailedFromInteractiveSearch` for
  interactively grabbed or unknown-source releases), read live before the
  manager write; otherwise failing a grab would leave no replacement owner.
- **Operator controls:** per-content attempts with a reviewed reset on the
  Activity page; per-workflow pause/resume (CLI and web) that stops
  automatic and manual actions.

## Milestone 1 — read-only foundation (in progress)

| Area | Status | Notes |
| --- | --- | --- |
| Go module, toolchain, build/vet/test commands | done | See [AGENTS.md](../AGENTS.md). |
| Versioned TOML configuration, validation, secret references (`env:`/`file:`) | done | Literal secrets, unknown keys, non-loopback web binds without authentication, allowed hosts and TLS (or a declared TLS-terminating proxy), plain HTTP to non-local hosts, and unimplemented integrations are rejected. |
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
| Packaging: `Dockerfile` (distroless, non-root, headless by default), user-level systemd unit | unverified | Written but never built or run here (no container runtime or user systemd session). Remote access from a container needs `web.password`, `allowed_hosts` and TLS or `trusted_proxy` (authentication exists since 2026-11-17; not exercised in a container). See `packaging/README.md`. |

## Milestone 2 — policy and durable action foundation (in progress)

The first write workflow exists (see milestone 3). Everything in this
table is exercised with fakes, plus the real-instance recovery runs.

| Area | Status | Notes |
| --- | --- | --- |
| Modes `observe` / `paused` / `automatic`, `[automation] emergency_stop`, per-workflow enablement (`no_progress_recovery`, `metadata_recovery`, `preserved_data_failure`, `partial_pack`, `missing_search`; all off by default) | done | `automatic` is accepted but permits nothing without a tested workflow. Interpretation (proposed, not yet in SPEC): manual actions are refused in `observe` (no operational writes at all) and allowed in `paused` (which stops automation only); emergency stop blocks both. |
| Scoped rules (`[[rule]]`, priority, match on client/category/manager/privacy), item overrides (`[[override]]` by transfer or content, reason required), exclusions (`[[exclude]]`, per workflow) with provenance in every effective setting | done | Equal-priority rules with possibly overlapping matchers and conflicting values are validation errors. The effective revision covers settings, permissions, exclusions, mode, and emergency stop. Implemented by a delegated agent (Gemini via `agy`) in an isolated worktree, reviewed, and applied as one commit. |
| Action journal: migration 0003 (intents, steps, locks, budgets, cooldowns), upgrade test from a real v2 database | done | Intent, steps, locks, and budget reservation are committed in one transaction before any request; each step is committed as `dispatching` before it is sent. Uncertain intents keep their locks until reviewed. Budgets persist across restart. |
| Restore detection | done | A journal high-water marker beside the database (`journal.hwm`, fsynced) detects a restored or replaced database; writes are held until `housekeeparr restore-ack --confirm`. |
| Action coordinator (`internal/action`) | done (R5 closed) | Gate and prerequisites re-evaluated before every write, preview expiry, local lock pre-check, step-aware live revalidation before every write, evidence baselines journaled with each dispatch, budget release only for provably unsent requests, journaled dispatch, postcondition verification by observation, conservative error classification (only unauthorized/unsupported/rate-limited/4xx count as refused; a timeout or malformed 2xx response is uncertain), no blind retry, partial-workflow holds, graceful stop that persists the current step, startup `Reconcile` that observes but never re-sends, and operator `Review`. Fault tests: accepted-then-timed-out, timeout reconciled by observation, accepted-but-not-effective (held, not failed), divergence between steps (progress, completion, user pause, recheck, privacy, new association, unread manager, version change, emergency stop), partial failure, crash between network and commit, crash before dispatch, stop between steps, unwritable store, restored database, budget across restart, duplicate execution. |
| Recovery planner (`action.Plan`) | done (R6/R7 closed) | Proposal for stalled, metadata-timeout, and source-rejected findings (client errors are diagnosis only) with every prerequisite stated: tested workflow for the exact versions, finding active, fresh evidence, qualified diagnosis made under the current diagnostic settings (R8) and matching the workflow, applicable state (never paused/queued/checking), privacy (public; private and unknown privacy per `private_recovery`/`unknown_privacy` for no-progress recovery, unknown-before-metadata for metadata recovery, otherwise observation), exactly one matched manager association with known content identity (packs refused), no lock and no cooldown on the transfer or any affected content, policy, configurable pacing and content attempts with usage, the retained-data ceiling on current usage (every replacement, R12) and room for the transfer left behind, stopped or running. Operations carry every flag explicitly (`removeFromClient=false&blocklist=true&skipRedownload=false&changeCategory=false`: the manager owns the replacement search), then, unless the torrent is deliberately left running (`keep_running`), a client stop that retains data. The preview also lists what is re-read before every write and the affected content key. |
| Shared previews and manual actions | done | `housekeeparr preview <id> [--automatic] [--json]`, `act <id> --confirm`, `actions`, `review <id> --as … --note …`, `restore-ack --confirm`, `attempts [reset]`, `pause|resume <workflow> --note`, `paused`; web finding page shows the same preview, with an action form (confirmation required) only when eligible; `GET /api/v1/findings/{id}/preview`; Activity lists journaled actions and restore holds. `run` reports holds and actions that need review at start-up. Not yet reviewed in a real browser. |
| Retention of terminal intents | done | Succeeded/failed/cancelled intents, expired cooldowns, pacing reservations older than the audit retention, and attempt rows that predate their scope's latest reset are pruned after the audit-event retention; attempts since the latest reset and retained-transfer records are kept; uncertain and active intents and their locks never are, and pruning never lowers the restore high-water mark. |
| Mode × origin × enablement × exclusion × emergency-stop matrix | done | `TestPolicyCheckMatrix`: observe forbids all operational writes; paused stops automation but permits a deliberate manual action; disabled automation for a workflow still permits a manual one; exclusions and emergency stop forbid both. A per-workflow *pause* (migration 0012; `housekeeparr pause|resume <workflow> --note`, `paused`, and the Connections & policy page) is distinct from disabling automation: it stops automatic **and** manual actions for that workflow, is re-checked by the gate before every write, and is audited (`TestWorkflowPauseStopsAutomaticAndManualActions`, `TestGateRefusesAPausedWorkflowBeforeEveryWrite`). |
| Routing CLI mutations through a running worker | remaining | `act`, `review`, and `restore-ack` need the writer lock (exit 3 while the worker runs), like `ack`. |

## Milestone 3 — recovery (in progress)

| Area | Status | Notes |
| --- | --- | --- |
| Adapter writes and verification reads: `RemoveQueueItem` (Sonarr/Radarr, all four flags always sent; timeout = uncertain), `BlocklistEntries` and `History` (with record IDs), `Content` (episode+series or movie monitoring); qBittorrent `Stop` (`torrents/stop` on 5.x, `torrents/pause` on 4.x) | done | Contract tests plus real-instance runs. Released `BlocklistResource` has no infohash, so a blocklist entry is matched by byte-exact source title and content, and attributed to an action only if it is absent from the baseline read just before dispatch: evidence, not identity (a concurrent manual blocklisting of the same title for the same content in that interval would be indistinguishable). |
| Recovery executor (`internal/workflow`) | done | Refuses implicit or destructive flags (`removeFromClient=true`, missing flags, a stop without explicit data retention) before any request; verifies each step by fresh reads with a bounded settle time. |
| Complete no-progress recovery on real instances | **real-instance-tested** for Sonarr 4.0.20.3014 and Radarr 6.4.4.10685, each with qBittorrent 5.3.0, Transmission 4.0.6 and 4.1.4, Deluge 2.2.0, and rTorrent 0.9.8 | `internal/integration/recovery_test.go`; registered in `action.Tested`. Manual (`act --confirm` or the web form) outside observe mode, and automatic in automatic mode (see the automatic-dispatch row). The manual test supplies the diagnosis; the automatic test does not. qBittorrent 4.x is refused (privacy unknown), and the test asserts that. |
| Automatic dispatch from the engine | done (simulated-application tested); real-instance automatic run remaining | After each persisted cycle (and after `run --once`), `DispatchAutomatic` evaluates open, unsnoozed findings whose client and manager were read completely that cycle, oldest first and interleaved across managers, through the same preview/planner/coordinator/gate as manual actions; at most `actions_per_cycle` (proposal 1) actions are dispatched, one at a time, and a candidate refused before any write does not use the slot. One service lock serialises manual and automatic execution (and their reconciliation). Scoped circuit breakers hold a client or manager while any action there is uncertain (until reviewed), after repeated definite refusals (24 h window), or when at least three downloads making up half of a client's active ones are stalled together (a shared cause such as network, tracker, or VPN, rather than bad releases). Each finding's latest automatic decision (dispatched, deferred with time, held with reason, refused by a live check, needs attempts review, at the retained ceiling, not permitted, waiting) is persisted (migration 0009) and shown on the finding page, in `housekeeparr finding`, and in the worker log. `internal/app/dispatch_test.go` drives the real engine with an injected clock through diagnosis, dispatch, the real coordinator and `workflow.Recovery` against simulated applications: exactly one fail and one stop across hours of later cycles; nothing in observe or paused mode, for slow progress, user pause, queued transfers, unmonitored content, an unreadable manager, or correlated stalls; pacing defers and resumes oldest-first; an accepted-but-unobserved request is never re-sent and holds the scope; an unreadable manager holds only work it could own (ownership is read from the managers configured for the transfer's client, or unmapped ones), so a Radarr outage does not stop Sonarr recovery on a client Radarr does not use. Managers sharing one client fail closed on each other's outage, because the unreadable one could own the transfer. **Real-instance automatic run** (`internal/integration/automatic_test.go`): the production binary, built static, runs `run --once` repeatedly on one state directory against real Sonarr 4.0.20.3014, Radarr 6.4.4.10685 and each registered client; the real engine diagnoses two genuinely stalled fresh grabs from its own observations (no supplied diagnosis), first in observe mode (nothing sent), then in automatic mode: one pass dispatches exactly one recovery while the other waits, the next dispatches the other, later passes send nothing, and independent checks confirm every effect. Limits of that evidence: repeated `run --once`, not the daemon loop; transfer-scoped overrides shorten the diagnosis (grace 0 s, window 1 m, 3 observations) and cycles run faster than `polling.interval`, so the 90-minute default windows remain covered by deterministic tests only, and SPEC §13.4 scenario 1 is not complete on real instances. qBittorrent 4.6.7 is not diagnosed within the test's bound (see compatibility.md). |
| Replacement outcome tracking (search accepted, grab, progress as separate outcomes) | done; search acceptance **real-instance-tested** (migrations 0010–0011), grab/progress/completion fixture-tested | After a verified recovery (migration 0008) the engine records, only from complete manager reads, a replacement grab (a queue row for the same content with a different download ID, first seen after the recovery), its verified progress, and completion in the client; after 24 h without a grab it reports "no replacement grab observed", explicitly not evidence that no release exists; tracking ends after 7 days. Shown under Activity and `housekeeparr actions`. **Replacement search**: the manager step's baseline journals the highest command ID the manager lists (`GET /api/v3/command`; a failed read does not block the request); right after a verified recovery the coordinator polls the command list for up to 30 s, and the engine follows the command while it is unsettled (reading command lists only then). The search is the first `EpisodeSearch`/`MoviesSearch` naming the content that is newer than the baseline (without one: trigger `unspecified`, queued after the request less 1 min). States: not seen yet, accepted, running, completed (with the manager's own message, e.g. "0 reports downloaded", and an explicit note that a completed search is not a grab and says nothing about source coverage), command failed, not observed after 1 h (not evidence that no search ran), accepted with end unverified. Managers eventually drop finished commands; finished commands were still listed 7 minutes later on the tested versions, but the retention is not established (migration 0010's comment says "a few minutes"; it is a committed migration and was not edited). Real-instance runs assert that Housekeeparr recorded exactly the manager-native command the harness identifies independently. |
| Metadata recovery | **real-instance-tested** and registered for Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 with qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, Transmission 4.0.6/4.1.4 (planner → coordinator → executor; automatic dispatch fixture-tested only) | Separate permission (`metadata_recovery`), own diagnosis (`metadata_timeout`, `policy.metadata_timeout`, proposed 60 min) and a hard "metadata state supported" capability prerequisite (unknown capability refuses). Every client reports privacy as unknown until metadata arrives, and released Sonarr 4.0.20/Radarr 6.4.4 expose no indexer privacy (no such type in their assemblies), so refusing unknown privacy would disable the workflow. As a scoped policy choice (the client reports no payload, so there is nothing to seed; Housekeeparr cannot read a tracker's rules and makes no claim about them), metadata recovery **accepts unknown privacy by default** (user direction 2026-11-16, recorded in SPEC §6); `unknown_privacy.metadata_recovery = false` in `[automation]`, a `[[rule]]` or an `[[override]]` keeps a scope observation-only. The setting exists only for this workflow, never applies to a transfer known to be private, and changes the effective, not the diagnostic, revision. Live revalidation accepts still-unknown privacy only for such a proposal and refuses if metadata arrived (state changed). A metadata-stage transfer is retained with unknown logical size and zero payload ("no payload"), refreshed if it later obtains metadata. Metadata stalls count toward the correlated-stall hold. Tests: `internal/action/metadata_test.go`, `internal/config/unknown_privacy_test.go`, `TestAutomaticMetadataRecoveryEndToEnd`. Real-instance finding: the managers blocklist under the grabbed release's title while a metadata-less rTorrent download's queue row is titled `<HASH>.meta`; blocklist evidence is now matched by the newest grab's source title (journaled in the baseline). The managers also refuse trackerless magnets when the client's DHT is off. Not done: real-instance automatic dispatch, and waiting out the real 60-minute timeout. |
| Private torrents in no-progress recovery (`private_recovery`) | **real-instance-tested** (default mode) with both managers and qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, Transmission 4.0.6/4.1.4 | User decision 2026-11-16 (SPEC §6): default `"keep_running"` fails and blocklists the grab in the manager (manager-native replacement search) and **never stops the torrent**, so nothing is stopped against tracker obligations; `"stop"` also stops it (retaining data); `"observe"` sends nothing. Scoped through `[automation]`, `[[rule]]`, `[[override]]` with provenance; part of the effective (not diagnostic) revision; a policy that does not state it fails closed. A torrent left running is recorded as retained (running), counts against the retained ceilings, keeps its slot, and its stall finding stays open (the next plan reports no manager association). Unknown privacy (qBittorrent 4.x) is treated as private by default (user decision 2026-11-16) and so follows the same mode; `unknown_privacy.no_progress_recovery = false` leaves such transfers alone in a scope. This is fixture-tested only (`TestUnknownPrivacyNoProgressFollowsPrivateMode`, `TestUnknownPrivacyTorrentRecoveryEndToEnd`): no qBittorrent 4.x combination is registered in `action.Tested`, so nothing is sent there yet. A transfer whose grab a recovery already failed — the same client and native ID, with no manager associating it again — is left out of both sides of the correlated-stall hold and its finding explains the earlier action (R11: the same hash on another client, or a new grab of it, counts; outcomes that cannot establish their client exempt nothing; migration 0013). Not observed: what the manager does if a torrent left running later completes. On real instances a private grab was blocklisted, the replacement search queued, and the torrent stayed downloading (recorded as not retained at the time; the integration assertion now expects a running retained record, not yet re-run); `"stop"` on a private torrent was not run separately (same steps as public recovery). Tests: `internal/action/private_test.go`, `TestPrivateRecoveryModeResolution`, `TestPrivateTorrentRecoveryEndToEnd`. |
| Preserved-data failure handling | first slice **real-instance-tested** and registered for Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 with Transmission 4.0.6/4.1.4 and Deluge 2.2.0 (planner → coordinator → executor, diagnosis supplied; automatic dispatch fixture-tested only) | SPEC §7.2. Only a tracker's "not registered" rejection qualifies, reported from a structured field (`domain.Transfer.SourceFailure`: Transmission `error = 2`, Deluge `tracker_status` "Error: …"; unsupported on qBittorrent, which has no tracker result in its listing, and rTorrent, whose `d.message` can be stale) and classified by a small recognised phrase set (`domain.ClassifyTrackerError`). Diagnosis `source_unregistered` (`policy.evaluateSource`): the trailing observations of the current no-progress streak — active, incomplete, known sizes, no verified progress — all report the rejection, at least `min_observations` after grace over a proposed 30-minute confirmation window (constant `policy.SourceConfirmWindow`); a no-progress diagnosis keeps precedence so private handling is unchanged. Finding `transfer.source_rejected` (resolves after two consecutive fresh observations without the rejection, completion, or disappearance). Planner: own permission `preserved_data_failure`, every recovery prerequisite, "no import in progress" (manager tracks the grab as `downloading`), private and unknown-privacy torrents follow `private_recovery` (user decision 2026-11-17; default `keep_running`: blocklist, never stop; `unknown_privacy.preserved_data_failure = false` opts unknown privacy out), fail + blocklist + manager-native redownload, then stop retaining data. The live check before the manager write re-reads the rejection. Client errors (`transfer.client_error`) are diagnosis only. Tests: `internal/policy/source_test.go`, Transmission/Deluge classification tests, `TestPreservedDataFailureEndToEnd` (unregistered acted on before the no-progress window; other tracker error, private, local error not). Not done: automatic dispatch on real instances, qBittorrent/rTorrent signals, a real 30-minute confirmation. |
| Transmission, Deluge, rTorrent recovery | **real-instance-tested** and registered for Deluge 2.2.0, rTorrent 0.9.8 (after the `errored-recheck` fixture race was diagnosed and fixed), and Transmission 4.0.6 and 4.1.4 (legacy RPC on both; no second protocol needed), each with both managers; see compatibility.md | `Stop` via `torrent-stop` (legacy RPC, both 4.0.x and 4.1.x), `core.pause_torrent`, and `d.stop` (never `d.close`/`d.erase`) are sent as writes (timeout = uncertain) with at most one resend after a pre-dispatch session rejection; implemented by a delegated agent (Gemini via `agy`) in an isolated worktree and reviewed. The recovery integration test now runs through whichever client `--manager-client` selects and is required there. Transmission versions are registered with their exact reported build string. rTorrent recovery uses only `d.stop` (the download stays registered and keeps its files and label; `d.close` is never sent). A stale rTorrent `d.message` can still misclassify a later user-stopped download as a client error (diagnosis limitation, recorded in compatibility.md); it cannot make such a download eligible for no-progress recovery, which requires a downloading state. |
| Retained-data and capacity limits | done (fixture-tested) | A verified manager step records the transfer left behind as retained and running; a verified data-retaining stop marks it stopped (migrations 0007, 0013). Sizes are logical wanted sizes refreshed from every listing while retained; an unknown size stays unknown (a transfer stopped before metadata is shown as "no payload" and adds nothing to the byte total). Released only when a complete client listing no longer has it. `retained_transfers` (proposal 25) and optional `retained_gib` ceilings gate every replacement on current usage, whatever this transfer's disposition (R12), and the transfer left behind must also fit; unknown sizes never count as room under a byte ceiling; free space is not measured and no free-space threshold is claimed. `housekeeparr retained` and the Activity page list them with cleanup guidance. |

## Remaining v1 scope (not started unless noted)

- Observation of all four clients (qBittorrent 5.3.0/4.6.7, Transmission
  4.0.6/4.1.4, rTorrent 0.9.8, Deluge 2.2.0), Sonarr, and Radarr is validated
  against real instances (see
  [compatibility.md](compatibility.md)), and correlation with each of the
  four as the managers' download client. Managed content with real grab
  history (fictional series/movies through the metadata and Torznab
  stand-ins) is exercised for queue-removal semantics and recovery only;
  imports and season packs are not.
- Exclude from the interfaces (exclusions are configuration-only so far).
- No-progress recovery: an automatic run through the daemon loop with the
  default windows (SPEC §13.4 scenario 1 end to end), notification of
  deferrals and exhausted attempts, and why qBittorrent 4.6.7 transfers were
  not diagnosed within the automatic test's bound; metadata recovery and
  preserved-data failure handling (see milestone 3). A Housekeeparr-owned
  replacement search (for managers with "Redownload Failed" off) is not
  implemented: such recoveries are refused.
- Sonarr partial season-pack handling (import history, coverage evidence,
  association clearing, targeted searches).
- Proactive missing searches: first slice implemented (see the 2026-11-17
  session); still remaining are episode batching per series, priority for
  content recovery could not replace, targeted searches after partial
  packs, and per-search source diagnostics (SPEC §9.2 beyond indexer
  status).
- Free-space measurement for retained data (counts and logical-size ceilings exist).
- Notifications: webhook targets (JSON or ntfy-style text) are implemented
  and fixture-tested (see the 2026-11-17 session); no real notification
  service was exercised, and source-coverage notifications await coverage
  findings.
- Web authentication: implemented and tested with in-process requests;
  not browser-checked, and TLS serving was not exercised with a real
  certificate.
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
  rTorrent 0.9.8 as the managers' client, and full Housekeeparr recovery
  now passes with each of them.
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
  complete/seeding; Transmission and rTorrent reported errors. rTorrent too
  reports complete over deleted data if the deletion happens during a hash
  check (the open descriptor keeps reading the unlinked file). Preserved-data
  handling cannot rely on client state alone for libtorrent clients.
- **`removeFromClient` defaults to true** in both managers' released
  `QueueController`, so every consequential flag must be sent explicitly.

## Compatibility

Observation is validated for qBittorrent 5.3.0 and 4.6.7, Transmission 4.0.6
and 4.1.4, rTorrent 0.9.8 (lighttpd SCGI gateway), Deluge 2.2.0 (deluge-web), Sonarr
4.0.20.3014, and Radarr 6.4.4.10685 (see [compatibility.md](compatibility.md)
for exactly what was exercised). No-progress recovery is tested for Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 with all four clients: qBittorrent 5.3.0, Transmission 4.0.6 and 4.1.4, Deluge 2.2.0, and rTorrent 0.9.8 (manual and automatic dispatch).

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
