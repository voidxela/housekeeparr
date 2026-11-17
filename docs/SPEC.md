# Housekeeparr specification

Status: approved product direction; implementation specification. Housekeeparr is partially implemented: the read-only and durable action foundations exist; no-progress recovery is real-instance-tested for Sonarr/Radarr with all four clients (qBittorrent 5.3.0, Transmission 4.0.6 and 4.1.4, Deluge 2.2.0, rTorrent 0.9.8; manual and automatic dispatch, the latter on real instances only through repeated one-shot runs with shortened windows), replacement-search acceptance is tracked on real instances, and retained-data ceilings and replacement grab/progress tracking are tested with simulated applications; other workflows and the remaining v1 scope are outstanding; see `docs/STATUS.md` for exactly what is done, tested, and remaining. Proposed defaults below must be validated against representative deployments before release. Supported versions and action capabilities must be established by integration tests, not inferred from API names.

## 1. Purpose and goals

Housekeeparr keeps managed downloads and media libraries consistent with user intent. It begins with reliable download-queue diagnosis, safe recovery, partial-import handling, and controlled searches for missing content. Later releases expand into inspecting actual downloaded and imported content, detecting inconsistencies and unwanted releases, and applying user-defined rules with coherent, complete workflows.

Three goals govern implementation decisions:

1. **Lightweight:** small operational footprint, bounded storage and concurrency, modest API traffic, simple installation, and no distributed infrastructure requirement.
2. **Highly reliable:** evidence-based decisions, preserved content, durable action tracking, conservative handling of uncertainty, and tested recovery from interruptions.
3. **Informative and human-friendly:** a fast, simple interface with thoughtful visual polish that explains what is happening, why, what happens next, and what the user can do.

A stalled torrent is not provably dead. Recovery is a bounded operational choice based on sustained evidence, not a claim that a release can never complete. Media managers retain authority over monitoring, release selection, quality, language, import behavior, and configured acquisition sources. Housekeeparr must not silently weaken those preferences.

### 1.1 V1 outcomes

- Distinguish persistent lack of progress from ordinary pauses, queuing, slow transfers, checks, and temporary outages.
- Recover eligible failed or unproductive grabs without deleting downloaded or library content.
- Resolve confirmed incomplete Sonarr season-pack associations while retaining successful imports, source files, torrents, and seeding state.
- Search released, monitored missing content at controlled rates without creating retry loops or download bursts.
- Identify incomplete search coverage when available evidence establishes source problems, and alert the user clearly.
- Explain findings, eligibility, actions, limitations, and outcomes before and after automation is enabled.

### 1.2 V1 exclusions

Housekeeparr does not provide a torrent client, indexer, tracker, Usenet client, media server, transcoder, or direct acquisition service. V1 supports torrent workflows only; other protocols are future scope. It does not add sources, bypass access controls, change manager profiles, manage seeding ratios, force imports, or modify other applications' databases.

V1 does not rename, move, delete, quarantine, or re-encode library or download files. Recursive media-tree scanning, direct content analysis, general filesystem cleanup, and automatic cleanup of retained downloads are excluded. Content inspection and explicitly controlled remediation are post-v1 directions, described in section 14.

Housekeeparr does not guess media identity, episode ordering, language, or file correctness when evidence is insufficient. It reports uncertainty. It is not a replacement for media managers or download clients.

## 2. Scope and deployment

### 2.1 Integrations

V1 media managers are **Sonarr and Radarr**, including multiple instances. Sonarr supplies series, season, episode, and import context; Radarr supplies movie and import context. Lidarr and other managers are planned after v1.

V1 plans full workflow support for **Transmission, qBittorrent, rTorrent, and Deluge**. If an adapter proves incapable of expressing a required safe workflow, or disproportionately complicated to implement and maintain, observation-only support may be proposed with documented evidence and an explicit scope decision. Observation-only must not be presented as full automation support.

Prowlarr is optional and is not required for v1. Managers remain the acquisition interface and authority over their configured sources. Direct Prowlarr diagnostics may be added later without making it a dependency.

### 2.2 Deployment contract

Support home servers, Docker and NAS stacks, distributed services across hosts or VPNs, and managed unprivileged seedboxes. Core operation requires no root access, privileged container, shared media filesystem, inbound port, public port assignment, or direct database access to another application. A small writable directory holds configuration and persistent state. Respect host-provider rules and assigned ports.

Provide a native foreground process and container packaging. Support external supervision, including user-level systemd or equivalent. A one-shot mode for externally scheduled runs is desirable where it preserves the same timing, locking, and recovery guarantees. Windows, macOS, and specific CPU architectures require explicit validation before being advertised as supported.

Ship a web interface in v1 while supporting complete headless configuration and operation. A headless invocation opens no listener. When enabled, the interface binds to loopback by default. Remote exposure requires authentication and documented TLS or trusted reverse-proxy configuration. Packaging may enable the interface through explicit configuration; it must not silently bind to all interfaces.

## 3. Domain model and evidence

### 3.1 Identity and ownership

A physical transfer is identified by client instance plus its stable native download identity. Preserve available infohash variants and normalize them explicitly; do not assume a single hash format across protocols or versions. Manager instance and content IDs form separate relationships to that transfer. Queue row IDs are transient references, not primary identities.

One transfer may relate to multiple queue rows, episodes, or managers. Different transfers may share files through cross-seeding or other arrangements. A matching name or path is not sufficient evidence of identity or shared ownership. Ambiguous ownership, shared ownership, or uncertain shared-data effects prohibit potentially conflicting writes until resolved.

Maintain distinct identities for manager content, physical transfers, release attempts, findings, and action executions. Correlation must retain its evidence and uncertainty.

### 3.2 Observations

Store bounded, timestamped observations with source, collection time, and capability context. Each signal is known, unknown, or unsupported. A failed read must not turn into an empty queue, zero peers, public privacy, zero bytes, or unavailable pieces.

Capture as supported:

- Native client state, pause/schedule/queue state, errors, and checking or verification activity.
- Wanted and verified payload bytes, remaining bytes, file selection, transfer progress, and metadata availability.
- Connected-peer availability, tracker/swarm signals, and privacy, with their differing meanings preserved.
- Manager queue relationships, import status, history, monitoring, release availability, health, and pending searches.

Tracker and indexer seeder counts may be stale and do not prove that required pieces are available. Native stalled states and instantaneous transfer speeds are evidence inputs rather than conclusions.

Prefer increasing verified wanted payload bytes as the progress measure. Where unavailable, declare the weaker available measure and its limits. Network traffic alone is insufficient: repeated corrupt or discarded transfers must not indefinitely postpone diagnosis. File-selection changes, rechecks, client counter resets, and incompatible observations establish a new baseline. Slow but sustained meaningful progress is not failure merely because it misses a speed target.

### 3.3 Findings

A finding is a persistent, user-facing assessment, independent of whether an action is allowed. The shared workflow is:

**Evidence → finding → policy evaluation → proposed action → verified outcome.**

Each finding includes:

- Stable identity and deduplication scope; type, severity, lifecycle state, and affected transfers/content/connections.
- First seen, last observed, last evaluated, and evidence freshness.
- Evidence references, missing information, confidence explanation, and capability limitations.
- Applicable policy identity and revision, eligibility or exclusion reasons, proposed effects, and next evaluation time.
- Linked actions, verified outcomes, user acknowledgement, snooze, and resolution history.

Finding states include open, acknowledged, snoozed, and resolved. Action states and observed transfer states are separate. Acknowledgement does not resolve a condition; snoozing suppresses configured notifications and automation for that finding until expiry. Exclusions remain independently configurable. Repeated observations update the same finding; recurrence after resolution retains a link to prior occurrences. Resolve on verified recovery or authoritative disappearance, not an API failure. If the cause of disappearance is unknown, record that limitation.

## 4. Adapter contracts and compatibility

Adapters normalize facts and execute explicitly requested operations. They contain no policy decisions. Each reports authentication, transport, version, read capabilities, write capabilities, and errors classified as transient, permanent, rate-limited, unauthorized, unsupported, or uncertain outcome.

Support credentials, TLS validation, reverse-proxy base paths, timeouts, pagination, and bounded response sizes. Do not silently disable TLS verification. Pin and test minimum versions and transports before release. For rTorrent, choose and document supported XML-RPC transport arrangements; for Deluge, choose and document daemon or web API support. Avoid committing to every possible transport.

Publish a matrix by application/version and workflow: observation, no-progress recovery, metadata recovery, preserved-data failure handling, and partial-pack handling. Optional signals may be unavailable without invalidating observation support. Automatic actions require all safety prerequisites for their particular workflow. Unsupported or newly incompatible behavior produces a visible finding and disables affected writes.

Contract tests use sanitized fixtures; real workflow tests use disposable instances. Compatibility claims must identify tested versions. Development-branch source code is useful design evidence but does not establish released-version behavior.

## 5. Observation and diagnosis

Use successful observations over time; persist history across restarts. Distinguish waiting for a queue slot, scheduled or user pause, checking, metadata acquisition, active transfer, completion, import, seeding, and client error states. Distinguish tracker/source failures from local disk, permissions, connection, application, and capacity problems.

Both a minimum count of qualifying observations and an elapsed qualifying window are required. An observation gap never counts as continuous failure evidence. Define a maximum tolerated gap per policy; exceeding it requires fresh qualification. Short interruptions suspend qualification, while state changes, meaningful progress, or baseline changes reset it as appropriate. Persist wall-clock timestamps for audit; use monotonic elapsed time within a process. Detect backward or implausible forward clock movement and require renewed evidence rather than making immediate actions eligible.

Define separate diagnoses for:

1. Sustained no meaningful progress, even when piece availability is unknown.
2. Sustained no progress with positive evidence that required pieces are unavailable.
3. Metadata acquisition timeout.
4. Import blocked or incomplete, with cause established or explicitly uncertain.
5. Connection, source coverage, storage, or worker health problems.

Unknown availability does not become evidence of unavailable pieces. A user may explicitly enable a no-progress policy that does not require piece availability, provided its remaining prerequisites hold.

Use circuit breakers scoped to the affected client, manager, source, or shared dependency. Correlated failures must suspend affected automation rather than fail every download. Unrelated healthy connections continue operating. Recovery uses bounded probes and staggered resumption.

### 5.1 Initial timing proposals

Starting proposals are ten-minute transfer sampling, a thirty-minute new-download grace period, a ninety-minute public-torrent no-progress window, at least three qualifying observations, and a maximum qualifying observation gap of twice the polling interval. Nearly complete transfers receive a longer window, initially four hours at 95% completion or above. Metadata acquisition uses a separate proposed sixty-minute timeout after applicable grace. Import settling begins with a proposed thirty-minute window after the last relevant activity.

These are tunable initial values, not universal recommendations. State exactly when grace and diagnosis windows begin and overlap in configuration documentation and previews. All thresholds must be exercised against representative deployments before defaults are promoted. More conservative scopes may override them. Metadata automation requires supported metadata evidence.

## 6. Policies and operator controls

Observe-only is the default. Modes are observe-only, controlled automatic, and paused. Observe-only reads and evaluates but issues no operational writes to managers or clients. Paused stops new automation while preserving the interface, health visibility, and necessary reconciliation reads. Neither mode can undo a request already accepted externally.

Automation permissions are explicit per workflow and scope. Enabling no-progress recovery does not enable metadata recovery, partial-pack clearing, or proactive missing searches. Mode changes and policy changes are audited.

Policy resolution follows global defaults, connection scope, explicit scoped rules, then item overrides. Rules have explicit priority; ambiguous equal-priority conflicting rules are validation errors. Multi-connection workflows must satisfy restrictions on every affected scope. Hard safety prerequisites and global emergency stop cannot be overridden by a rule or manual action. Show the resolved policy and the source of each effective setting.

Support exclusions by connection, manager content, client category/label where available, and transfer. Provide snooze with expiry, permanent exclusion, workflow pause, global stop, and previews. Preview shows evidence, missing prerequisites, exact intended effects, and external side effects. Manual action uses the same coordinator and safety checks as automation; it is not a bypass.

Private and unknown-privacy transfers are never stopped by default. For no-progress recovery of a transfer known to be private, the default fails and blocklists the grab in the manager, so collection continues, while leaving the torrent running in the client; stopping it as well, or observation only, are explicit scoped choices (`private_recovery`). Unknown-privacy transfers, and other recovery operations on private transfers, default to observation. Exception: metadata recovery accepts privacy that is unknown only because metadata has not arrived, since no client can report it then and no payload has been downloaded, so no ratio, hit-and-run, or seeding obligation is at stake; a scope may turn this off, and a transfer known to be private is never acted on. Explicit opt-in requires a documented workflow compatible with the user's obligations. If preserving registration, freeing an active slot, and respecting obligations cannot all be satisfied, decline the workflow. Do not guess that stopping a torrent is harmless.

## 7. Stalled-grab recovery

Recovery is a durable multi-step workflow, not an atomic operation across services:

1. Re-fetch the transfer, manager association, monitoring/content state, privacy, relevant health, and effective policy. Confirm it remains incomplete, eligible, owned, active in an applicable state, and unchanged in decision-relevant ways. Reject user-paused, queued, checking, excluded, or uncertain cases.
2. Acquire the relevant transfer/content locks, reserve rate and attempt budgets, and persist an intent containing evidence, policy revision, intended operations, and expected postconditions before external writes.
3. Use a version-tested manager/client workflow to fail or block the specific grab and detach or otherwise resolve the queue association while retaining all content. Set every removal/blocklist/redownload flag explicitly; never rely on endpoint defaults.
4. Ensure a retained incomplete transfer cannot consume an unintended active slot or unexpectedly resume. Preserve prior state and record any deliberate stop. Unsupported safe separation stops the workflow with an actionable finding.
5. Initiate replacement through exactly one owner: manager-native failed-download redownload, or a Housekeeparr-scheduled manager search with native redownload suppressed. Document and verify other side effects such as automatic client removal. Do not intentionally initiate both paths.
6. Verify queue, blocklist, client registration, content preservation, search acceptance, subsequent grab, and progress as separate outcomes. Completion of a command does not establish a successful replacement.

A timeout can mean an operation succeeded. Reconcile before retrying. If the outcome cannot be established safely, mark it uncertain and hold further conflicting actions for review. Local idempotency keys prevent duplicate local execution; they cannot guarantee exactly-once behavior from external APIs.

Retain attempt history, release/hash history, cooldowns, and per-content retry budgets. Different listings may share a hash; different hashes may still contain the same files. Use available identities to prevent known cycles without claiming perfect duplicate detection. Do not bypass manager rules to force an alternative.

Manager blocklists and Housekeeparr cooldowns have different semantics. Do not promise hash-only scope or temporary expiry unless supported and verified. Default to no automatic expiry of manager blocklists. Any reconsideration must be explicit, supported, narrowly scoped, and must not remove unrelated entries.

When a search yields no grab, report the observed outcome and back off. Do not claim that no alternative exists unless evidence actually establishes it. Attempts reaching their budget require review or an explicit later policy reset.

### 7.1 Retained data and capacity

Track retained transfers and known retained payload size, distinguishing logical size from actual disk use. Shared files and hardlinks make naive summation misleading. Do not require filesystem access merely to obtain a capacity estimate.

Support configured retained-count/byte ceilings and minimum-free-space thresholds when suitable measurements exist. At a ceiling, stop initiating replacements or proactive acquisitions in the affected scope and alert the user. Unknown capacity is visible; it is not zero free space. Do not claim to enforce a free-space threshold without measurements. Retention metadata must outlive short observation retention. V1 offers guidance and links for external cleanup, never automatic content deletion.

## 8. Partial imports and incomplete season packs

This workflow is Sonarr-specific in v1. A generic queue message about missing or unimported episodes is insufficient evidence: permissions, extraction, mappings, samples, media analysis, and transient failures can produce similar messages.

After a settling period, correlate tracked downloads, grab/import history, current episode/file records, queue diagnostics, and client file lists where supported. Confirm positive evidence of completed applicable imports and identify episodes that remain missing. An expected range such as E01–E37 is manager context, not proof that a release contains every episode.

Require supported coverage/mapping evidence for the absent subset. Names alone, a quiet period alone, or a completed torrent alone do not establish that extraction/import has finished. Uncertain extraction, episode order, multi-episode files, manager health, or ownership produces a specific finding and no association-clearing action. No dependency on one extractor is required; if no reliable evidence path exists, this installation receives diagnosis only.

For a confirmed partial pack:

- Preserve the torrent, source files, imported library files, and seeding state.
- Clear only the stale manager association through a tested operation that preserves those guarantees. Do not migrate categories as an implicit workaround.
- Reconcile episode monitoring, presence, queue, and pending imports immediately before targeted searches.
- Search only still-missing monitored and released episodes, in bounded individual batches. Do not issue a broad season search as fallback.
- Retain a pack/content finding that prevents repeated handling of the same association. Scope any supported suppression narrowly; do not indiscriminately ban a release useful for other episodes.
- Verify association clearance remains effective after subsequent refreshes. If it reappears, diagnose the failure rather than repeatedly clearing it.

Housekeeparr does not overwrite existing files or force uncertain imports. Unsupported safe separation is an explicit capability limitation.

## 9. Missing-content scheduling and search coverage

### 9.1 Eligibility and budgets

Proactive missing searches are optional and separately enabled. Eligible units are monitored, released missing Sonarr episodes and available missing Radarr movies according to manager availability settings. Exclude content queued, grabbed, pending import, already being searched, unmonitored, unavailable, excluded, or in cooldown. Recheck eligibility before dispatch.

Prioritize recovery and new gaps, while using bounded aging/fairness so older content is not permanently starved. Use episode-level searches for partially filled seasons when supported. Housekeeparr controls requested search scope, but must not promise that a manager will never select a pack.

Enforce global and per-manager command budgets, daily limits, concurrent search limits, active replacement limits, per-series limits, jitter, and exponential backoff. Release defaults must be explicit configuration values validated in testing. Start with one in-flight acquisition search globally and conservative configurable command rates; do not hide unbounded queues behind those limits. Persist budget consumption so restart does not reset it. Startup and recovery from downtime must not replay a backlog in a burst.

Housekeeparr can strictly budget its own commands. One manager command can fan out across indexers and grabs; do not advertise strict per-indexer request or download-byte limits without enforceable capabilities. Honor exposed retry-after signals and manager health. Capacity ceilings stop new acquisition requests but cannot cancel already accepted external work safely by assumption.

Track not searched, scheduled, command accepted, search completed when observable, grabbed, no acceptable result, incomplete coverage, cooldown, uncertain outcome, and intervention needed separately. Search completion is not synonymous with a grab or successful download.

### 9.2 Incomplete-source findings

Source coverage is evaluated independently of result and acquisition outcome. A search may find releases while one source fails. Zero results alone is not evidence of incomplete coverage.

| Evidence | Required interpretation |
| --- | --- |
| Relevant configured source unavailable, rate-limited, unauthorized, disabled for the applicable search, or reporting an error | Incomplete coverage with the known source, scope, and cause |
| Search completed with no acceptable releases and adequate diagnostics | No acceptable result; include exposed rejection reasons |
| Manager does not expose enough search/source diagnostics | Coverage could not be verified |
| Search command itself failed | Command failure, distinct from source failure or no grab |
| Manager command accepted but completion unobservable | Accepted; final outcome unverified |

Disabled sources should be described as configuration limitations, not automatically treated as accidental failures. Distinguish intentionally excluded sources where configuration expresses intent. An empty applicable source set is a configuration finding. Housekeeparr does not enable sources automatically.

Use manager health, source configuration, command state, and search diagnostics when available. Distinguish direct evidence for a particular search from contemporaneous source-health evidence; the latter indicates possible impact rather than proving that search failed. Do not introduce a second independent indexer search merely to diagnose coverage. Sanitize source URLs and diagnostics.

Group recurring source issues into a stable finding per source/connection and cause, linking affected searches when known. Explain the impact, retry time, available corrective steps, and diagnostic limitations. Notify on actionable new problems, significant changes, and verified recovery using deduplication and rate limits. A single later successful API read does not prove a source recovered; require a relevant successful health/search observation.

Housekeeparr cannot reliably establish that a user needs additional unconfigured sources. Explain configured-source limitations without inventing missing sources, silently weakening profiles, or promising results that those sources cannot supply.

## 10. Execution, persistence, and operational reliability

Use one writable embedded state store and a single writer process, with an operating-system-level ownership lock appropriate to the platform. Do not rely solely on expiring timestamps for exclusive ownership. Prevent daemon, one-shot, and CLI invocations from becoming concurrent writers; route commands through the owner or refuse clearly. A shared network filesystem is not an assumed supported state-store location.

Serialize conflicting transfer and content workflows. Persist intents and journal state before external writes. Suggested execution states are proposed, reserved, executing, reconciling, succeeded, failed, uncertain, and cancelled before dispatch. Record each step independently, including retries and observed postconditions. A partially completed action must remain visible and block incompatible work until reconciled.

Re-fetch decision-relevant state before each consequential write. External state may still race after that read; use conditional operations where available and stop on detected divergence. Manual changes invalidate stale proposals. Existing accepted external requests may complete after pause or emergency stop; show that limitation and continue safe read-only reconciliation.

An unwritable/full/corrupt state store prevents new operational mutations. Expose the failure clearly; never proceed using only in-memory intent. Bound API timeouts, concurrency, retry count, and backoff. Authentication failures do not produce rapid retry loops. Graceful shutdown stops dispatch and persists current step status; abrupt termination is handled by startup reconciliation.

Define versioned migrations with backups and rollback guidance. Startup after restore or upgrade reconciles outstanding intents and checks identity/configuration changes before enabling writes. Restoring an old database must not blindly replay old actions. Preserve durable attempts, active intents, unresolved findings, and retained-data records even when pruning detailed observations.

Ship configurable retention with bounded defaults for observations, resolved findings, and completed audit events. Never silently discard unresolved or uncertain actions. Document backup, restore, disk requirements, retention, and safe disposal of state.

## 11. Interface and operator experience

The v1 interface is a thin client of the same core services used by the CLI. It must feel immediate on a modest home server and remain usable when integrations are slow or unavailable. Present cached status with visible freshness; viewing a page must not trigger expensive scans or acquisition searches.

### 11.1 Primary views

| View | Required information and controls |
| --- | --- |
| Overview | Mode, worker heartbeat, dependency health, finding counts, recent outcomes, capacity/budget state, next scheduled work, global pause |
| Needs attention | Prioritized findings, impact, evidence, uncertainty, corrective steps, snooze, acknowledge, exclude, and links to affected applications |
| Downloads | Manager/content relationships, native and inferred state, progress history, effective policy, eligibility timeline, next evaluation, and limitations |
| Activity | Durable action and decision history, intended effects, individual steps, verified outcomes, failures, and supported compensating actions |
| Connections and policies | Connection validation, tested/detected capabilities, secret references, automation scopes, rule precedence, effective-policy preview |

Example explanation: “No verified progress for 84 minutes. Recovery becomes eligible in 6 minutes if conditions remain unchanged. This connection does not expose piece availability.” Eligibility estimates must reflect all applicable grace, observation-count, and safety requirements.

Use clear typography, consistent spacing, restrained color, and polished loading, empty, stale, and error states. Status must not depend on color alone. Support keyboard navigation, readable contrast, responsive layouts, and reduced motion. Avoid decorative charts that obscure decisions. Technical details belong in expandable evidence, not the primary workflow.

Show the operational consequences before enabling automation or invoking a manual action: retained content, any torrent stop, manager blocklisting, queue changes, and search ownership. Expired previews must be recalculated. Controls should communicate queued or uncertain execution rather than optimistic success.

### 11.2 CLI and notifications

Provide configuration validation, connection/capability checks, health, findings, effective-policy preview, action preview, mode controls, and supported manual actions. Include machine-readable output for automation. Configuration remains usable without the web interface.

Notifications are optional and rate-limited. Include the condition, affected scope, impact, next step, and a useful interface link when configured. Do not leak credentials, private tracker URLs, or unnecessarily detailed media information. Worker heartbeat and dependency status must be externally inspectable without requiring a functioning notification path.

## 12. Security and configuration

Use a versioned, validated configuration file with secret references from protected files or environment/secret stores. Keep credentials out of command arguments, repository files, URLs in logs, and frontend responses. Redact cookies, tokens, API keys, tracker passkeys, and sensitive query strings. Diagnostics exports must be sanitized by default.

Use least-privilege credentials where supported; disclose when upstream APIs require broad privileges. Default local connections may follow documented trusted-host assumptions. Remote credentials require encrypted transport or an explicitly documented trusted tunnel/proxy arrangement.

If the web interface is enabled, protect configuration and mutations with authentication, safe session handling, and CSRF protection where cookie sessions are used. Remote unauthenticated access must not be enabled accidentally. Any local unauthenticated mode must be explicit, loopback-only, and protected against cross-origin mutations and DNS-rebinding-style host abuse. Secrets remain server-side. Reverse-proxy trust and forwarded headers require explicit configuration.

Configuration updates are validated before application and applied atomically. Show the active configuration revision and distinguish restart-required changes. Keep secrets separate from ordinary configuration exports. Do not implement arbitrary executable policy expressions or shell hooks as a v1 rule mechanism.

## 13. Architecture and validation

### 13.1 Components

- **Adapters:** versioned facts, explicit capabilities, API operations, and classified errors.
- **Observation and correlation:** normalized evidence, freshness, ownership, and content relationships.
- **Finding service:** durable findings, deduplication, lifecycle, and evidence links.
- **Decision engine:** pure policy evaluation producing findings, explanations, and action proposals.
- **Action coordinator:** durable steps, exclusivity, safety checks, budgets, reconciliation, and verified outcomes.
- **Scheduler:** bounded polling, prioritization, cooldowns, fairness, and circuit breakers.
- **State/configuration:** embedded persistence, migrations, retention, validation, and secret references.
- **Web/CLI interfaces:** shared application services with no independent policy implementations.

Do not require a broker, external database, distributed scheduler, or separate worker fleet for normal use. Language, embedded database, and frontend tooling remain implementation decisions. Select simple, maintainable options after establishing packaging and adapter constraints. Do not build a general plugin framework merely to prepare for future managers.

### 13.2 Performance acceptance

Before release, publish a reference workload and numeric budgets for idle/steady/peak memory, CPU, API calls, state growth, startup/reconciliation time, and interface latency. Include at least a typical small installation and a larger multi-instance installation, with explicit queue and library sizes. Separate normal polling from expensive full reconciliation.

Benchmark on modest documented hardware. Use bounded work queues, incremental synchronization where available, pagination, capped histories, and no recursive filesystem reads in v1. UI requests should read local state rather than wait for dependency round trips. Performance results and limits are release criteria; “lightweight” is not satisfied solely by implementation language or container size.

### 13.3 Required testing

Use deterministic policy tests for time windows, progress, corrupt/repeated traffic, missing capabilities, privacy, paused/queued/checking states, file selection, client restarts, observation gaps, clock changes, cooldowns, budget persistence, policy precedence, and circuit breakers.

Use sanitized fixtures and adapter contract tests for every advertised version/capability. Exercise real action sequences against disposable clients and managers, including automatic failed-download behavior and preservation of source files. No test depends on live public torrents or real user data.

Fault-injection tests cover requests that succeed before timing out, partial multi-step failures, process termination between journal and network operations, state-store write failure, restored backups, manual concurrent changes, malformed responses, and shared ownership. Validate frontend stale/error states and shared policy/action semantics.

### 13.4 Core acceptance scenarios

1. An eligible public no-progress transfer is diagnosed from sustained observations and recovered once through a verified workflow; slow progress, user pause, and queue waiting do not trigger failure.
2. Unknown piece availability remains explicit. A separately enabled no-progress policy can qualify without it; a policy requiring positive unavailability cannot.
3. Metadata stalls use their own evidence and timeout. Unknown metadata capability disables automatic metadata recovery.
4. A completed season pack containing E01–E31 with E32–E37 still expected retains imports, source files, torrent, and seeding. After positive settled-import evidence, only the stale association is cleared and missing monitored released episodes are searched. Extraction or mapping uncertainty prevents clearance.
5. Sonarr, Radarr, a client, or sources becoming unavailable cannot cause mass blocklisting or uncontrolled searches. Healthy unrelated connections continue.
6. A timeout after a successful write, restart, or repeated poll does not blindly repeat an acquisition request. Unreconcilable outcomes remain visible and held for review.
7. A successful search with one failed relevant source reports incomplete coverage independently of results. Zero results without diagnostics reports uncertainty rather than source failure.
8. No operational path deletes payload/library content, bypasses hard safety prerequisites, or silently weakens manager rules. Capacity limits stop new affected acquisition work.
9. The web interface and CLI explain the same finding, policy, evidence, action effects, and verified outcome. Headless operation requires no listener.
10. All four planned clients pass documented workflow tests or receive an explicitly approved, visible support limitation before v1 release.

## 14. Post-v1 content monitoring and rules

The expansion goal is comprehensive, consistent content auditing and controlled remediation, using the same finding and action lifecycle as v1. It is not limited to queue messages or release-name checks.

### 14.1 Read-only audit foundation

Start with manager-provided file and media metadata. Add optional filesystem access through explicit path mappings and allowed roots, without making shared storage a core requirement. Establish file identity, accessibility, and mapping evidence; report unavailable mounts or permissions rather than declaring files missing. Inspect downloaded payloads and imported library files as distinct stages, respecting ongoing writes, extraction, and imports.

Use incremental, resumable inspection. Budget metadata reads, stream probes, bounded decoding, full-file reads, and hashing separately. Watchers may accelerate discovery but cannot be the sole correctness mechanism. Handle path changes, symlinks, hardlinks, shared seeding files, and network mounts deliberately. Cache results against file identity/change evidence and invalidate stale results.

### 14.2 User-defined rules

Provide versioned declarative rules with scope, priority, evidence requirements, parameters, severity, exceptions, and permitted responses. Evaluation returns pass, fail, or unknown; missing evidence is never an automatic violation. Preview rules against existing evidence before enabling remediation. Rule edits create new evaluation revisions while retaining prior explanations.

Candidate checks include declared versus actual resolution, video/audio codecs, audio/subtitle language requirements, release-group preferences, episode coverage, duplicate or conflicting versions, file presence, and discrepancies between manager records and observed content. Distinguish unwanted-but-valid releases from technical integrity problems and ambiguous identity. Stream metadata alone does not prove episode identity or full-file integrity.

Maintain manager authority over release selection. A content finding may justify requesting replacement, but Housekeeparr must not secretly relax profiles or create repeated replacements when every available release violates the user's rule. Explain policy conflicts and unavailable acceptable alternatives.

### 14.3 Controlled remediation

Introduce replacement, reimport, quarantine, or other mutation only as individually specified, explicitly enabled capabilities after read-only audits are proven. Each needs identity and ownership checks, preservation/recovery behavior, preview, durable execution, and verified outcomes. Do not advertise universal undo. Shared files and seeding obligations must be considered before any file operation.

The intended progression is v1 queue reliability and usability; read-only content audits; richer user-defined rules; then controlled remediation. Add Lidarr and other managers through domain-aware contracts rather than pretending albums, movies, and episodes have identical semantics.

## 15. Delivery and unresolved implementation decisions

1. **Compatibility prototypes:** establish supported versions/transports and safe queue, blocklist, redownload, ignore, privacy, and preservation semantics. Record unsupported workflows before enabling writes.
2. **Read-only foundation:** persistent observations, identity/correlation, findings, policy preview, CLI, and the initial web interface. Validate deployment topologies and reference workloads.
3. **Recovery:** prove one manager/client workflow, then extend to Sonarr, Radarr, and all four planned clients with contract and fault tests.
4. **Partial-pack workflow:** prove evidence and safe association clearance against representative Sonarr cases; add bounded targeted searches.
5. **Search and release hardening:** proactive searches, coverage findings, notifications, capacity handling, packaging, documentation, security review, and performance acceptance.
6. **Post-v1:** content auditing, rules, remediation, and additional managers as described above.

Before release, resolve language/distribution, supported version floors and transports, concrete embedded-store choice, tested source diagnostics, supported extraction/import evidence, numeric budgets, retention defaults, and final timing defaults. Resolve through prototypes and tests rather than optimistic compatibility assumptions. Any client demotion or reduction of agreed v1 workflows requires a documented scope decision.

## 16. Technical references

These references inform implementation research; compatibility must be validated against pinned released versions.

- [Sonarr queue controller](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Api.V3/Queue/QueueController.cs)
- [Sonarr failed-download service](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Download/FailedDownloadService.cs)
- [Sonarr API documentation](https://sonarr.tv/docs/api/)
- [Radarr API documentation](https://radarr.video/docs/api/)
- [Transmission RPC specification](https://github.com/transmission/transmission/blob/main/docs/rpc-spec.md)
- [qBittorrent Web API documentation](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-%28qBittorrent-5.0%29)
- [Deluge documentation](https://deluge.readthedocs.io/en/latest/)
