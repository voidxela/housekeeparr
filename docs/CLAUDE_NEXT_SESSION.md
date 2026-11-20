# Housekeeparr continuation

Work on `dev`. `review/remediation` (`21638d8`) was fast-forwarded into it.
Read [AGENTS.md](../AGENTS.md), [SPEC.md](SPEC.md), the
"Default-timing validation" section at the top of [STATUS.md](STATUS.md),
and the last two run tables in [compatibility.md](compatibility.md).
`main`, releases and deployments were out of scope and are unchanged.

## Preserve the workflow contracts

- Preserve every download and library file. Send all four manager-removal
  flags explicitly with `removeFromClient=false`. Managers own replacement
  redownloads in v1.
- Journal each external workflow request and hold ambiguous outcomes for
  review. Require current positive evidence and keep unknown privacy,
  sizes, and availability intact. Incomplete evidence (truncated history,
  a partial wanted-list read) never authorizes a write.
- Apply the scoped `private_recovery` mode to private and unknown-privacy
  transfers. The default keeps the torrent running and accounts for it as
  retained.
- Writes are supported per API line (SPEC §4.1): every `action.Tested` entry
  still needs a released-instance run, and a new line needs one before it
  is added to `action.Lines`. Use a new migration and a previous-schema upgrade test for database
  changes.
- Use focused, subject-only conventional commits. Run released fixtures
  serially from a worktree pinned to the tested commit and record
  versions, commit, selection, outcomes and failure mechanisms in
  compatibility.md.

## What this session established

- The production daemon loop at the accepted default timing recovers two
  genuinely stalled grabs on released Sonarr/Radarr with qBittorrent
  5.3.0. Both qualified about 2 h after the client added them. A paused
  restart kept the streaks and sent nothing. The automatic restart
  recovered one download per cycle (`scripts/it/run.sh --daemon`, about
  2¼ h).
- The correlated-stall hold now lets one spaced probe through while the
  client shows verified progress elsewhere (`correlated_probe_interval`,
  1 h). A hold with no progress anywhere notifies daily.
- Manager history is paged, and incomplete history refuses pack clearance.
  Wanted lists beyond the read bound are read in rotating windows.
- Web actions outlive the request. Listener timeouts and the
  manager-native outcome windows are configurable; their defaults are
  unchanged, except the write timeout, which went from 30 s to 2 min.
- `TestWebDeployment` covers signed sessions and CSRF over direct TLS and
  behind a trusted proxy, inside the namespace.

## Host notes

This workspace is aarch64 with a 39-bit user address space, so
`go test -race` cannot run locally. The hosted workflow
(`.github/workflows/qbittorrent-verification.yml`, `workflow_dispatch`)
has a `race` job for that; it passed at `f468c7e` (run 44613995480). `/tmp` is RAM-backed. Set `TMPDIR` to the
workspace volume for fixture runs and keep runs serial. Run
`go clean -cache` if the disk fills.

## Production trial (2026-11-20, running)

Housekeeparr runs in automatic mode on the user's Ultra.cc host "eclipse"
(see STATUS.md, "Production trial", and AGENTS.md in `~/eclipse`). It is
installed in `~/.apps/housekeeparr` as a user systemd service, and the web
interface is behind the account's nginx at `/housekeeparr/`. The deployed
binary is `30ab743` (built-in limits after the blocking-rule audit in STATUS.md), and the previous one is kept as `bin/housekeeparr.prev`.
`trial/coverage.py` snapshots coverage against the baseline taken when the
trial started (917/1330 files, 413 missing). The open feature proposal is
`docs/proposals/feature-expansion-2026-11.md`.

## Remaining

- No real browser engine has driven the web interface. The deployment
  test uses an HTTP client with a cookie jar.
- The default-timing daemon run used one client with the correlated hold
  below threshold. A longer production trial with representative
  installations should measure probes past an engaged hold and recovery
  latency with real swarms.
- Multi-episode pack recovery is still observation-only: add a released-instance
  test for failing a stalled pack row, then enable it.
- The remaining v1 scope in STATUS.md.
