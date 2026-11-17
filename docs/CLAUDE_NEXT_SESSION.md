# Prompt for the next Claude session

Continue implementing Housekeeparr through the complete approved v1 scope in
`docs/SPEC.md`. Read `AGENTS.md`, the entire SPEC, `docs/STATUS.md`,
`docs/compatibility.md`, and **`docs/reviews/2026-11-16-latest.md`** before
changing behavior. Earlier reviews `docs/reviews/2026-11-15.md` and
`docs/reviews/2026-11-16.md` supply background; R1–R7 have fixes, but the
latest review reproduces R8–R10 below. Implement and verify concrete
increments, not just plans.

Checkpoint reviewed: `f0f2ae3` on `dev`, code last changed at `c966439`
(session of 2026-11-16, second; 7b0d1d0..HEAD, not pushed). This review adds
documentation only. Validation: full Go suite, vet, gofmt and
the static binary at `c966439`; real-instance matrix at `118f15c` from a
pinned worktree (all five registered combinations passed, including both
recovery workflows, the automatic test and exact search attribution;
qBittorrent 4.6.7 failed only on the `errored-restart` observation fixture,
undiagnosed); qBittorrent 5.3.0 + Sonarr + Radarr again at `c966439` (all
passed). Commits after `118f15c` add operator controls (attempts
view/reset, workflow pause), tests and wording. Not run: browser check, race
detector (host limitation), Docker, the daemon loop with default windows on
real instances.

No-progress recovery is now real-instance-tested and registered for Sonarr
4.0.20.3014 and Radarr 6.4.4.10685 with **all four clients** (qBittorrent
5.3.0, Transmission 4.0.6 and 4.1.4, Deluge 2.2.0, rTorrent 0.9.8), manual
and automatic dispatch; search acceptance is tracked as its own outcome.
Read STATUS ("Session of 2026-11-16 (second)") for exactly what changed,
including a fixed diagnosis defect (scoped rules/overrides were ignored by
diagnosis) and a new prerequisite (the manager must redownload failed
grabs).

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
controls, but do **not** approve the proposed values now in
`[automation.limits]` (20 per rolling 24 h globally and per manager, 3
attempts per episode/movie until reset, 24-hour success cooldown, and the
rest listed in STATUS "Implementation proposals still hard-coded"). Pacing,
per-content attempts with `housekeeparr attempts reset`, visible deferral and
persisted per-finding automatic decisions are implemented; numbers remain
proposals awaiting a product decision.
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

1. **R8 — policy/evidence consistency before manual writes.** The new
   review proves that a fresh CLI service with a 24-hour no-progress policy
   can execute a diagnosis made over three hours under the old policy.
   `Plan` checks qualified status, not the assessment's settings revision.
   Require a current assessment or recompute under current policy. Compare
   `Assessment.PolicyRevision` to `Settings.Revision()`, not the broader
   effective permission revision; a mode-only change should remain usable.
   Add deterministic regression tests before extending recovery writes.
2. **R9/R10 — outcome evidence and independent lifecycle.** A healthy
   manager plus an offline client can produce "replacement completed" from
   pre-recovery cached client facts. Require fresh suitable client evidence
   for progress/completion. Separately, completed/untracked downloads are
   excluded from search polling/expiry, leaving accepted/running search
   states forever. Continue bounded search observation independently of the
   download's terminal state. Three reproducible overlay tests are described
   in the latest review and remain under `/tmp/housekeeparr-review-latest/`.
   These are local logic fixes; no full real-instance matrix is needed just
   to validate them.
3. **Deliver metadata recovery next**, followed by preserved-data failure
   handling (SPEC §§5, 7, 13.4 scenario 3). Use distinct evidence, timeouts,
   workflow permission and capability gating. Prove one supported
   manager/client slice before extending to all four. Preserved-data handling
   must not rely on client state alone; retain the missing-data and rTorrent
   hash-race limitations. Do not let an unrelated observation-fixture flake
   block these increments.
4. **Focused qBittorrent 4.6.7 harness diagnosis**, independently of new
   workflow work: `errored-restart` failed at `118f15c` (`stalledDL`, verified
   0, rather than `missingFiles`). The harness already polls for non-checking
   full torrents but never requires their verified bytes and does not fail
   when that deadline expires. Inspect saved artifacts, establish the actual
   cause, fix readiness if supported by evidence, then use a client-only
   observation run. Fastresume timing is still a hypothesis. Recovery stays
   refused for unsupported privacy. The earlier slow automatic-test failure
   did not recur; preserve both dated results. Failure log:
   `~/.cache/hk-rtdiag/keep/qbt467-118f15c.log` (outside the repository).
5. **Operator experience and remaining workflows:** notifications and
   deferral status, then positive import/coverage evidence for partial packs,
   targeted/proactive searches and source coverage, remote web/auth,
   packaging/security/performance. Keep the complete v1 checklist below.

Validation follow-ups to batch with relevant work: probe redownload-disabled
and interactive-grab refusal against real managers (only the enabled,
UserInvokedSearch path is real-instance-tested); add a shortened-window
real daemon smoke for scheduler/lifecycle wiring. Default-window timing is
already exercised deterministically through engine → dispatcher → coordinator;
do not wait two hours per session to repeat it on real instances. A later
unattended default-window soak remains an explicitly unperformed acceptance
check, not a blocker for the next workflow.

Housekeeparr-owned replacement searches (when "Redownload Failed" is off)
remain unimplemented and refused. SPEC §7 step 5 permits either owner; the
current refusal is a visible limitation. Resolve supported coverage before
release without starting a second replacement owner implicitly.

Proposals awaiting a product decision (implemented, not approved): a
workflow pause also blocks manual actions; recovery is refused when the
manager would not redownload; the replacement-search constants (30 s
post-recovery poll, 1-minute clock tolerance without a baseline, 1-hour
follow window); and the existing `[automation.limits]` values.

Process lesson: run the integration matrix from a git worktree pinned to the
commit being recorded (`git worktree add --detach ~/.cache/hk-wt-run <sha>`):
each probe is its own `go test` invocation and compiles the current tree.
`/tmp` is RAM-backed on this host; keep large synthetic files on disk.

Numeric limits remain proposals (`[automation.limits]`); see the budget
concern section. Exhausted attempts need `housekeeparr attempts reset`.

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
- The manual recovery test plans from engine-written state but supplies the
  qualified stall diagnosis; the automatic test does not (the engine
  diagnoses under transfer-scoped overrides). Production sequence:
  fail/blocklist with every flag explicit, then stop while retaining data.
  Independent checks establish
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

- Select validation by changed behavior, not by whether it is the final
  session HEAD. A docs/checkpoint commit needs no Go suite or real-instance
  run. Existing evidence remains tied to its tested commit; identify newer
  changes and limitations accurately without rerunning unchanged adapters.
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
- Shared executor/harness changes: use deterministic fault/boundary tests
  first. If external call order, flags or contracts change across clients,
  run the affected matrix once as a bounded batch at the integrated workflow
  milestone. Pure local outcome/policy logic or UI controls do not require a
  matrix merely because their package is shared. Adapter-local changes need
  affected versions first; unrelated combinations retain dated evidence.
- Keep real runs serial on this host. Add focused phase/test selection,
  per-phase timings and compile-once reuse when working on the harness;
  a selected run must state its scope and keep required tests mandatory.
  Investigate the 4.6.7 fixture with managers disabled. Avoid paying for all
  managed probes and automatic dispatch just to debug one client fixture.
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

Latest independent validation at `f0f2ae3`: full uncached Go suite passed
(outside sandbox for loopback fixture sockets; slowest packages app 19.1 s,
engine 17.7 s), build/vet/format/diff checks and a static binary passed.
Three temporary Go-overlay tests deliberately fail (four assertions),
confirming R8–R10. No expensive instance matrix was rerun. See the latest
review for exact reproductions and commands. Passing existing tests does not
close those defects. No repeat full-suite run is needed for this docs-only
handoff refresh.

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
