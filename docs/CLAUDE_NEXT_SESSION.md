# Prompt for the next Claude session

Continue implementing Housekeeparr through the complete approved v1 scope in
`docs/SPEC.md`. Read `AGENTS.md`, the entire SPEC, `docs/STATUS.md` (start
with "Session of 2026-11-16 (third)") and `docs/compatibility.md` before
changing behavior. Reviews under `docs/reviews/` supply background; R1–R10
all have fixes with regression tests. Implement and verify concrete
increments, not just plans.

State after the third session of 2026-11-16 (`dev`, not pushed):

- **R8–R10 fixed.** Plans require a diagnosis made under the current
  diagnostic settings; outcome progress needs a fresh complete client
  listing; searches are followed past terminal downloads.
- **Metadata recovery** is real-instance-tested (planner → coordinator →
  executor) and registered for Sonarr 4.0.20.3014 and Radarr 6.4.4.10685
  with qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8 and Transmission
  4.0.6/4.1.4. Its automatic dispatch is fixture-tested only.
- **User decisions this session (recorded in SPEC §6):** metadata recovery
  accepts privacy that is unknown only because metadata has not arrived (no
  payload exists; `unknown_privacy.metadata_recovery = false` opts a scope
  out). No-progress recovery of a torrent **known to be private** defaults
  to `private_recovery = "keep_running"`: fail and blocklist the grab in the
  manager, never stop the torrent; `"stop"` and `"observe"` are scoped
  choices. Real-instance-tested (default mode) with all four clients.
- **Open proposal, not implemented:** letting unknown-privacy torrents
  (qBittorrent 4.x) follow `private_recovery` too. They stay refused for
  no-progress recovery. Ask before changing this.
- Blocklist evidence is matched by the grabbed release's title (the queue
  title differs for a metadata-less rTorrent download).
- `scripts/it/run.sh --only REGEX` selects registered tests; record such
  runs as selected. See compatibility.md for exactly which commit each
  result belongs to and what was not re-run.

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

1. **Preserved-data failure handling** (SPEC §§5, 7; STATUS milestone 3).
   Must not rely on client state alone: missing data is invisible on
   qBittorrent/Deluge until restart, a forced recheck can look like a fresh
   download, and rTorrent's `d.message` can be stale. Prove one
   manager/client slice first, with its own permission and evidence.
2. **Add a "require positive piece unavailability" policy setting** (SPEC
   §13.4 scenario 2). It must be part of `Settings.Revision()` so R8's
   check covers it.
3. **Metadata recovery follow-ups:** real-instance automatic dispatch
   (shortened `metadata_timeout` via overrides, like `TestAutomaticRecovery`);
   observe what a manager does when a private torrent left running later
   completes; decide the unknown-privacy proposal above with the user.
4. **Focused qBittorrent 4.6.7 harness diagnosis** (client-only):
   `errored-restart` failed at `118f15c` (`stalledDL`, verified 0, not
   `missingFiles`). `run.sh` polls for non-checking full torrents but never
   requires their verified bytes and proceeds when the deadline expires.
   Establish the cause from artifacts before changing anything; failure log
   `~/.cache/hk-rtdiag/keep/qbt467-118f15c.log`. Use `--managers none`.
5. **Operator experience and remaining workflows:** notifications and
   deferral status; partial packs with positive import/coverage evidence;
   targeted/proactive searches and source coverage; remote web/auth;
   packaging/security/performance. A kept-running private torrent keeps an
   open stall finding with an explanation; consider a distinct state.

Validation follow-ups to batch with relevant work: redownload-disabled and
interactive-grab refusal against real managers; a shortened-window real
daemon smoke. A default-window real soak remains an unperformed acceptance
check, not a blocker.

Housekeeparr-owned replacement searches (when "Redownload Failed" is off)
remain unimplemented and refused; resolve before release without starting
a second replacement owner implicitly.

Proposals awaiting a product decision (implemented, not approved): a
workflow pause also blocks manual actions; recovery is refused when the
manager would not redownload; the replacement-search constants; the
`[automation.limits]` values; metadata stalls counting toward the
correlated-stall hold; already-recovered transfers left out of it; a
metadata-stage transfer retained as zero bytes.

Process lessons: run recorded integration runs from a git worktree pinned
to the commit (`git worktree add --detach ~/.cache/hk-wt-run <sha>`); chain
checks with `&&` before committing (a `;` chain committed a red tree once
this session and had to be amended); `/tmp` is RAM-backed, keep large files
on disk.

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

Latest validation is recorded per commit in `docs/compatibility.md` and
STATUS ("Session of 2026-11-16 (third)"); do not restate it from memory.

## Remaining v1 after this slice

Finish the no-progress and metadata follow-ups and the preserved-data workflow for both managers
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
