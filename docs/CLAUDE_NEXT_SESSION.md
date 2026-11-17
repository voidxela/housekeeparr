# Housekeeparr continuation

Resume from `review/remediation`, based on `dev` at `3d08077`. Read
[AGENTS.md](../AGENTS.md), the full [SPEC.md](SPEC.md), the dated review
section in [STATUS.md](STATUS.md), and the exact run table in
[compatibility.md](compatibility.md). The Go source checkpoint is
`9fe5f3f`. Use released instances for compatibility claims. Treat the
Sonarr research note as design input.

## Preserve operational rules

- Preserve all download and library files. Send all four manager removal
  flags explicitly with `removeFromClient=false`. Managers own recovery
  redownloads. Housekeeparr-owned replacement searches belong to the post-v1
  roadmap.
- Journal every external workflow write. Keep ambiguous outcomes held for
  review and preserve unknown privacy, sizes and availability. Use the
  shared planner, gate and executor for manual and automatic work.
- Keep the approved private and unknown-privacy `private_recovery` default.
  The kept-running torrent is retained. The metadata exception relies on
  positive evidence that metadata is still absent.
- Keep exact-version evidence aligned with `action.Tested` and support both
  Transmission 4.0 and 4.1 using the tested legacy RPC contract. Put schema
  changes in a new migration with a previous-schema upgrade test.
- Use focused, subject-only conventional commits. Run heavy suites
  serially and retain failed fixture artifacts. Record commits, selections,
  versions and results. A required skip is a failed run.

## Resume validation

1. Run build, vet, repository-wide gofmt, uncached tests and the full race
   suite. Repeat focused concurrency tests for app, engine, action, web and
   notify. The prior amd64 host ran the baseline full race suite, then
   disconnected during the final verification sequence.
2. Run qBittorrent 5.3.0 with both managers on an isolated runner that can
   send tracker announces. The amd64 review host reached the Web API and
   reproduced the manager workflows, but qBittorrent reported a denied
   socket operation and the stand-in received no tracker announces. Record
   a clean unselected run and exercise the latest search and pack live
   checks. Run selected rTorrent, Deluge and Transmission combinations for
   the affected shared contracts. The repository has verified amd64
   fixture pins for rTorrent and Deluge and build dependencies for
   Transmission. Use Debian trixie's Python for its Deluge packages.
3. Continue the remaining timing and evidence items in STATUS. Keep
   actionable findings for work held by evidence or bounds, and validate
   the daemon loop and trial-length windows. Follow SPEC for outstanding
   release readiness and product work.

## Current changes

Missing searches revalidate monitoring, availability, queue state and file
absence. They retain pacing across restart and follow the manager's
specific command. An incomplete listing has a visible finding. Search
timings live in `[search]`.

Sonarr pack clearance checks the live row and content set, the tracked
diagnostic, client and manager state, and affected permissions. It records
targeted follow-ups alongside verified clearance under the pack's governing
permission. Import settling restarts on evidence gaps or changes.

Recovery persists verified manager effects before later steps.
Reconciliation holds skipped or ambiguous steps. Web form and Basic
authentication share atomic failure accounting. Webhook redirects fail
delivery and notification titles and URL fragments are sanitized. Current
schema remains v18.
