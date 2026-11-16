# Prompt for the next Claude session

Continue implementing Housekeeparr through the complete approved v1 scope in
`docs/SPEC.md`. Read `AGENTS.md`, the entire SPEC, `docs/STATUS.md`,
`docs/compatibility.md`, and **`docs/reviews/2026-11-16.md`** before changing
behavior. The earlier review `docs/reviews/2026-11-15.md` supplies background;
its R1–R4 are fixed. Implement and verify concrete increments, not just plans.

Checkpoint reviewed: `73d37d5` on `dev`, initially clean and locally committed.
The new review/handoff and documentation corrections may be uncommitted: check
status and preserve them. The policy/action foundation largely exists; one
manual no-progress execution sequence passes recorded real-instance tests for
Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 with qBittorrent 5.3.0. This does not
complete recovery across clients or SPEC §13.4's full acceptance scenario.

## Existing user authorization and constraints

- Continue independently within the approved specification. Preserve files,
  user data, uncertainty, and the existing work. All four clients and both
  managers remain v1 requirements. No client demotion without documented
  evidence and an explicit scope decision. No implicit post-v1 work.
- **Delegation remains authorized:** use the unsandboxed `agy` CLI with
  `--dangerously-skip-permissions`, including the previously authorized model:
  `agy --model gemini-4-hydrogen-high --dangerously-skip-permissions -p ...`.
  Do not ask the user to re-authorize delegation. Give each delegate a precise
  file-scoped brief in its own git worktree/branch; forbid commits, push, sudo,
  credentials and real user data. Prefer a permitted temporary/workspace path
  for the worktree. Obtain any execution permissions required by the host;
  the flag does not override this environment's permission boundaries. Review
  the diff and validate the integrated result. Delegate reports/source claims
  are not compatibility evidence: one earlier claim that Sonarr's `malIds`
  was optional was disproved by the released instance.
- System package installations needed for testing are approved. Use disposable
  instances and synthetic data, no persistent package services or changes to
  user configuration. Follow the environment's execution permission mechanism.
- Skip local Docker work on this underpowered host; leave validation explicitly
  outstanding. Container support stays in scope. Do not block unrelated work.
- Transmission must retain both 4.0.x and 4.1.x support. Legacy RPC is already
  validated for observation on 4.0.6/4.1.4. Add a second protocol only if a
  concrete tested workflow requires it; do not drop 4.0 due to deprecation.
- Focused, independently buildable commits; subject-only messages, no co-author
  trailer, no git identity changes to imitate another contributor. Do not push
  without authorization.

## User's budget concern — retain this context

The user is concerned that an arbitrary recovery quota will leave stuck
transfers hanging. SPEC §§7, 9.1 and 10 require bounded acquisition and retry
controls, but do **not** approve the current hard-coded 20/day global or 3/week
per-item values (or the 24-hour cooldown). They are implementation proposals.
Explain the distinction between pacing eligible work across an installation
and stopping repeated ineffective replacement attempts for the same content.
An exhausted allowance must keep the finding visible with reason, scope,
used/limit, next evaluation and an actionable control; it must not silently
abandon the item or imply no alternative exists. Global deferral should resume
fairly as allowance returns. SPEC §7 requires review or explicit later policy
reset for repeated attempts reaching their budget. Any departure from that
requirement, or numeric default, is a proposal, not already approved here.
Do not remove all bounds or claim this user question authorized unlimited
acquisition. Implement configuration/visibility and correct identity first;
do not turn a choice of defaults into a prolonged blocker for other work.

## Immediate work, in priority order

1. **Fix review R5: live checks before each consequential step.** Current
   Refetch ignores privacy/content changes and does not read monitoring or
   relevant health; the coordinator calls it only before the first write.
   Add fresh applicable safety checks, all affected ownership/scope checks,
   monitoring/content and health/circuit-breaker evidence, policy/mode/stop
   checks and decision-relevant version/capability checks. Check again before
   each write. Step-aware checks must allow the expected effects of preceding
   steps (e.g. intentional queue removal); simply repeating the initial
   fingerprint would reject a valid second step. If progress/completion,
   recheck, user pause or a conflicting association appears between fail and
   stop, hold the partial workflow visibly rather than stopping blindly.
   Start with failing deterministic regressions; see review reproduction.
2. **Fix R6: real content identity.** QueueItemKey includes download ID and is
   wrongly reused for content locks/budgets/overrides/exclusions. Replacement
   releases for one episode/movie must share a stable manager+content key,
   distinct from release attempt and physical transfer identity. Check all
   content locks/cooldowns in preview and execution, including multi-item
   scope. Preserve attempt history; add a forward migration/upgrade test if
   durable schema/data changes are needed.
3. **Close R7 and retry/verification gaps before automatic dispatch.** Make
   budgets/cooldowns configurable, preview their actual remaining allowance,
   explain exhaustion and next steps, persist attempts across replacements and
   restarts, and distinguish pacing from per-item intervention. Define safe
   reservation release for provably unsent requests. Verify current-action
   blocklist/history evidence against a baseline, not old matching entries.
   Audit accepted-but-not-yet-effective → failed → reusable idempotency key:
   an observation timeout cannot establish that an accepted acquisition had
   no effect. Keep partial, delayed and ambiguous outcomes held for review.
4. **Extend no-progress recovery through the other clients.** Their
   manager-client managed-row probes already passed: do not rebuild that
   prerequisite from scratch. Implement/test data-preserving Stop and live
   safety verification for Transmission (`torrent-stop`, both branches),
   Deluge (`core.pause_torrent`), rTorrent (probe `d.stop`/`d.close` before
   choosing). Extend `recovery_test.go` and the harness's required-test list:
   it currently runs full recovery only for qBittorrent. Add registry entries
   only with a passing recorded full workflow run; probing removal alone
   does not establish recovery compatibility. Correct rTorrent stale-message
   diagnosis before relying on it for preserved-data recovery; user pause
   must never be mistaken for permission to act.
5. **Complete a vertical automatic-recovery slice.** Add engine scheduling
   through the shared planner/coordinator, bounded conflicting work and fair
   selection, scoped circuit breakers, retained transfer records/capacity
   gates, and post-action search/grab/progress outcomes. Gate dispatch on
   these prerequisites. Use an injected clock for sustained engine diagnosis
   through dispatch; do not wait 90 real minutes. Then prove the automatic
   path with disposable instances without overstating supplied diagnosis.

Delegate independent adapter implementations or bounded regression/config
work while retaining ownership of shared coordinator/identity changes. Avoid
multiple delegates editing the same core files. Integrate small completed
slices and review each diff; don't wait for a large combined rewrite.

## Review recommendations on Claude's five decisions

These are review recommendations/interpretations; they are not new user
approvals or silent amendments to the authoritative spec.

- Observe forbids all operational writes (§6). Paused stops automation;
  intentional confirmed manual work is a reasonable interpretation. An
  automation-disabled workflow may support a one-off manual action, clearly
  explained, but workflow pause, exclusions, global stop and hard safety
  prerequisites must remain effective. Document and test the full matrix.
- Exact-version write registration is appropriate based on recorded tests;
  fix runtime safeguards before expanding registration/dispatch. Preserve
  observe-by-default and confirmation for deliberate manual effects.
- Composite blocklist evidence is necessary when no hash is returned, but is
  not guaranteed identity. Add baseline/event correlation and collision tests;
  uncertain stays uncertain, with no broad blocklist cleanup or invented TTL.
- Retry after cancellation/refusal is reasonable only when no external effect
  is established possible, with fresh checks and retained history. Never
  translate a delayed or ambiguous acquisition into a blind retry.
- Hard-coded quotas are not final product policy. See the user's concern and
  R6/R7 above; expose deferred work and avoid arbitrary silent abandonment.

## Preserve established evidence

- Stand-in metadata/Torznab runs inside loopback-only `unshare -rnm` with a
  private hosts file and throwaway CA trusted via SSL_CERT_FILE. Both managers
  add/grab fictional content through their API; never edit their databases.
- Managed rows: explicit removeFromClient=false retains torrent/data even
  with removeFailedDownloads on. Blocklist and ignore removal remain durable
  after refresh. skipRedownload=false triggers manager-native replacement;
  do not also issue a Housekeeparr search. These probes passed with all four
  clients (Transmission 4.0.6 and 4.1.4). Unknown-content rows return after
  refresh and do not establish usable recovery semantics.
- Current recovery test plans from engine-written state, but supplies the
  qualified stall diagnosis. Production sequence: fail/blocklist with every
  flag explicit, then stop while retaining data. Independent checks establish
  durable queue removal, failure/blocklist evidence, stopped registration,
  byte-identical payload and queued manager search. They do not establish a
  replacement grab or successful download. qBittorrent 4.6.7 refusal for
  unknown privacy is tested; don't relax privacy to expand the matrix.
- Restart exposes missing data on qBittorrent/Deluge; forced recheck can make
  the same loss look like a fresh download at zero. Complete/seeding alone
  doesn't prove data exists. rTorrent local d.message can remain stale after
  user stop/start. Preserve these limitations in diagnosis and explanations.
- Native race detector is blocked by this host's 39-bit VMA layout. Use a
  supported native CI runner/host and record an actual run, not just a workflow
  file. Don't repeat the known failure after every commit, patch TSan guards,
  change ASLR/kernel settings, or install Docker to attempt to change VMA.

## Validation cadence — velocity is an explicit concern

- During edits: format changed Go files and run targeted tests for touched
  packages/behaviors. Cached tests are useful feedback; don't force uncached
  whole-repository runs per edit or per docs commit. Test meaningful safety
  boundaries, not implementation trivia.
- Per coherent shared change/milestone: run build, vet, formatting, full
  uncached Go tests once on the integrated tree and a CGO-disabled binary
  build. Broaden/repeat only for new changes, failures or unresolved concerns.
  On this review's run the full unit/contract suite completed with the slowest
  package about 15 seconds; the real-instance matrix/builds are the expensive
  layer. Seek permission for sandbox socket restrictions, not test rewrites.
- Per ready adapter: one selected client/version with both managers. Example:
  `scripts/it/run.sh --clients deluge --manager-client deluge --managers sonarr,radarr`.
  Transmission needs separate 4.0.6 and 4.1.4 runs. Reuse pinned checksum-verified
  caches; avoid concurrent heavy suites on this host. Initial Transmission
  compilation (~15 minutes) is a cache cost, not a per-edit requirement.
- Shared executor/harness changes: run the affected matrix as a bounded batch
  before marking the milestone complete. Adapter-local changes need affected
  combinations first; unrelated unchanged combinations retain dated evidence.
  Do not rerun every older version after documentation or UI-only changes.
- Use bounded polling with early success for real readiness/postconditions,
  not fixed sleeps or arbitrary longer waits. Retain failure artifacts,
  inspect causes, and report any flake. Never hide failures behind rerun-until-
  green or claim a skipped required workflow passed. Never weaken isolation,
  explicit flags, preservation checks or ambiguous-outcome handling for speed.
- Browser-check a ready web slice once, including stale/error, confirmation
  and accessibility behavior. Leave Docker, unsupported-host race checks and
  real systemd validation explicit; they don't block independent feature work.
- Record exact commands, results, tested HEAD and limits in compatibility and
  STATUS at milestones. Don't repeat benchmarks without a performance change.

Review validation at `73d37d5`: targeted tests and the full uncached Go suite
passed (full suite outside sandbox for loopback fixture sockets); three review
Go-overlay tests deliberately failed, confirming R5/R6. No expensive instance
matrix rerun was needed for this documentation/code review. See review report
for build/vet/format results. Don't mistake passing existing tests for closure
of the newly reproduced defects.

## Remaining v1 after this slice

Finish no-progress, metadata and preserved-data workflows for both managers
and all four clients; durable retained-data/capacity controls; Sonarr partial
packs with positive settled-import/coverage evidence and safe association
clearance preserving imports/files/torrent/seeding; bounded targeted and
proactive missing searches; source-coverage findings separate from zero
results; notifications; authenticated remote web use and polished accessible
UI; complete headless operation; security/packaging/performance/retention
acceptance and release documentation. Owner-lock refusal for CLI mutations
is permitted by SPEC §10; routing through the owner is a usability improvement,
not a reason to redesign the writer model before delivering recovery.

Use all ten SPEC §13.4 scenarios as the completion checklist. Update STATUS,
README and compatibility evidence at actual milestones. Never edit committed
migrations. If work spans sessions, leave a precise tested checkpoint and next
concrete tasks, preserving full v1 scope. Report implemented, fixture-tested,
real-instance-tested, deferred and unimplemented separately.
