# Compatibility matrix

Status values: **contract-tested** = adapter tests pass against fixtures
derived from the cited released source, but no real instance has been
exercised · **tested** = a disposable-instance integration run of that exact
released version passed (see *Integration runs* below) · **not implemented** = Housekeeparr has
no code for this workflow · **unsupported** = refused by design.

Probes report `tested` only for exact versions listed in an adapter's
`TestedVersions`; every other version reports `untested` and is still
observed. **Tested means the observation workflow only**, except where the
workflow table and *Recovery workflow runs* below record a tested write
workflow.

## Integration runs

| Date | Host | Versions | Result |
| --- | --- | --- | --- |
| 2026-11-15 | Linux aarch64 | qBittorrent 5.3.0 (static qbittorrent-nox, WebUI API 2.16.2) + Sonarr 4.0.20.3014 | all assertions passed |
| 2026-11-15 | Linux aarch64 | qBittorrent 4.6.7 (static qbittorrent-nox, WebUI API 2.9.3) + Sonarr 4.0.20.3014 | all assertions passed |
| 2026-11-15 (independent review rerun) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 | all observation and correlation assertions passed |
| 2026-11-15 (independent review rerun) | Linux aarch64, Go 1.27.4 | qBittorrent 4.6.7 + Sonarr 4.0.20.3014 | all observation and correlation assertions passed |
| 2026-11-15 (after R1–R4 fixes, with new `Complete`/`totalRecords` assertions) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 | all assertions passed; real listings complete and queue row count equals `totalRecords` |
| 2026-11-15 (after R1–R4 fixes, with new `Complete`/`totalRecords` assertions) | Linux aarch64, Go 1.27.4 | qBittorrent 4.6.7 + Sonarr 4.0.20.3014 | all assertions passed; same |
| 2026-11-15 | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (linux-core-arm64, host ICU) | all assertions passed, including Radarr status/health/queue (unknown-movie row, download id = infohash) and engine correlation |
| 2026-11-15 | Linux aarch64, Go 1.27.4 | Transmission 4.0.6 (daemon built from the release tarball; sha256 cross-checked against Arch's PKGBUILD) | all assertions passed; every requested `torrent-get` field present |
| 2026-11-15 | Linux aarch64, Go 1.27.4 | Transmission 4.1.4 (daemon built from the release tarball; GitHub-published digest) | all assertions passed with the legacy protocol; every requested field present |
| 2026-11-15 | Linux aarch64, Go 1.27.4 | rTorrent 0.9.8 / libtorrent 0.13.8 (Debian trixie build, extracted, not installed) via lighttpd 1.4.79 `mod_scgi` with HTTP basic auth | all assertions passed, including wrong-password rejection at the gateway |
| 2026-11-15 | Linux aarch64, Go 1.27.4 | Deluge 2.2.0 (Debian trixie deluged + deluge-web, libtorrent 2.0.11; extracted, not installed) | all assertions passed after two adapter fixes found by this run (`daemon.get_version`; `[]` params) |
| 2026-11-15 (isolated: loopback-only network namespace, the new default) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685; qBittorrent 4.6.7 + Sonarr; Transmission 4.0.6; Transmission 4.1.4; rTorrent 0.9.8; Deluge 2.2.0 | all assertions passed in every combination |
| 2026-11-15 (isolated, with the removal probes and a second partially downloaded movie fixture) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr + Radarr; qBittorrent 4.6.7 + Sonarr; Transmission 4.0.6 and 4.1.4; rTorrent 0.9.8; Deluge 2.2.0 | all observation assertions and both probes passed |
| 2026-11-15 (isolated, with the `errored` fixture: verified, stopped, payload deleted, started) | Linux aarch64, Go 1.27.4 | full matrix as above | all assertions passed; error-state semantics below |
| 2026-11-15 (isolated, with the metadata/indexer stand-in and managed-row probes) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Transmission 4.0.6 + Deluge 2.2.0 + rTorrent 0.9.8 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (one run) | all observation tests, both unknown-row probes, and all four managed-row variants on each manager passed (managers' download client: qBittorrent 5.3.0) |
| 2026-11-15 (same) | Linux aarch64, Go 1.27.4 | qBittorrent 4.6.7 + Transmission 4.1.4 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 | all passed, including the managed-row variants with qBittorrent 4.6.7 as the managers' download client |
| 2026-11-15 (with the recovery workflow tests) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 | all passed, including both complete recovery workflows (see *Recovery workflow runs*) |
| 2026-11-15 (same) | Linux aarch64, Go 1.27.4 | qBittorrent 4.6.7 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 | all passed; recovery refused for unknown privacy as required |
| 2026-11-15 (with restart/recheck missing-data fixtures, rTorrent `d.message` probe, `--manager-client`; harness extension by a delegated agent, reruns by Claude) | Linux aarch64, Go 1.27.4 | all four clients (qBittorrent 5.3.0, Transmission 4.0.6, Deluge 2.2.0, rTorrent 0.9.8) + Sonarr + Radarr | all passed |
| 2026-11-15 (same) | Linux aarch64, Go 1.27.4 | qBittorrent 4.6.7 + Transmission 4.1.4 + Sonarr + Radarr | all passed |
| 2026-11-15 (same) | Linux aarch64, Go 1.27.4 | managers' download client Transmission 4.0.6; Transmission 4.1.4; Deluge 2.2.0 | all passed, including all four managed-row variants on each manager |
| 2026-11-15 (same) | Linux aarch64, Go 1.27.4 | managers' download client rTorrent 0.9.8 | first run: one failure, Radarr's replacement search for `skipRedownload=false` was not yet queued when checked (test took 510 s against about 82 s normally, host under load); rerun: all passed. The check now polls up to 60 s for the expected search; a further run with the poll passed |
| 2026-11-16 (after the R5 live-check rewrite, `ee3d164`) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | observation and probe tests passed; both recovery workflows **refused safely** before any request: the new manager-health check treated `IndexerRssCheck` (error: "No indexers available with RSS sync enabled") as blocking. RSS is irrelevant to a search-based replacement; fixed in `2984479` |
| 2026-11-16 (`2984479`) | Linux aarch64, Go 1.27.4 | same | all passed, including both recovery workflows with live per-write checks |
| 2026-11-16 (`05b2afb`: content identity, attempt ledger, baseline verification) | Linux aarch64, Go 1.27.4 | same | all passed, including both recovery workflows |
| 2026-11-16 (`d1d10f3`: retained records, outcome tracking, automatic dispatcher in the commit path) | Linux aarch64, Go 1.27.4 | same | all passed, including both recovery workflows |
| 2026-11-16 (`99cf198`, `--clients deluge --manager-client deluge --managers sonarr,radarr`) | Linux aarch64, Go 1.27.4 | Deluge 2.2.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | all passed, including both recovery workflows through Deluge |
| 2026-11-16 (`b8d3313`, `--clients rtorrent --manager-client rtorrent --managers sonarr,radarr`) | Linux aarch64, Go 1.27.4 | rTorrent 0.9.8 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | **run failed**: both recovery workflows and every probe passed, but the pre-existing observation fixture `errored-recheck` (payload deleted while rTorrent runs, then `d.check_hash`) was reported `seeding`/complete with verified bytes instead of a client error, unlike the 2026-11-15 runs. Diagnosed 2026-11-16 as a harness race, not an adapter defect (see *rTorrent hash-check race* below); fixed in `1f24634` |
| 2026-11-16 (`725f96b`, final session HEAD: owner-scoped ownership blocking, dispatcher fixes) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | all passed, including both recovery workflows |
| 2026-11-16 (`725f96b`, same) | Linux aarch64, Go 1.27.4 | Deluge 2.2.0 + Sonarr + Radarr, Deluge as the managers' client (isolated) | all passed, including both recovery workflows through Deluge |
| 2026-11-16 (`7b0d1d0` + the fixture fix committed as `1f24634`, `--clients rtorrent --manager-client rtorrent --managers sonarr,radarr`) | Linux aarch64, Go 1.27.4 | rTorrent 0.9.8 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | **all passed**: every observation assertion (`errored`, `errored-restart` and `errored-recheck` all client errors), the `d.message` probe, all probes, and both recovery workflows through rTorrent |
| 2026-11-16 (`dff2166`, `--clients transmission --manager-client transmission --managers sonarr,radarr --tr 4.0.6`) | Linux aarch64, Go 1.27.4 | Transmission 4.0.6 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | first run: both recovery workflows **refused safely** before any request ("transfer is complete"): Transmission 4 treats the pre-existing half payload under its final file name as complete when the manager adds it, without hashing. Harness fix `c26f83a`: the recovery test forces `torrent-verify` on the disposable daemon and requires a downloading state. Rerun: **all passed**, including both recovery workflows |
| 2026-11-16 (`dff2166` + `c26f83a`, same with `--tr 4.1.4`) | Linux aarch64, Go 1.27.4 | Transmission 4.1.4 + Sonarr + Radarr (isolated) | **all passed**, including both recovery workflows, over the legacy RPC |

What the suite exercised (`scripts/it/run.sh`, `internal/integration`):
session login; version probe; listing of complete, half-complete (after
recheck), empty, private, partially deselected, stopped, and magnet-only
torrents; state mapping (`stalledUP`, `stalledDL`, `stoppedDL`/`pausedDL`,
`metaDL`); verified bytes; privacy (5.x known, 4.6 unsupported); file
selection; availability (a half-complete torrent with no peers reports 0.5,
confirming that libtorrent counts this client's own pieces); absence of
tracker data in normalized snapshots; Sonarr status, health, and queue for
torrents of unknown series (download ID = infohash); end-to-end correlation
through the engine. With `--managers sonarr,radarr`, the same for Radarr
6.4.4.10685 with a torrent in its own category (unknown movie), including
that Sonarr's rows never appear in Radarr's queue. With `--clients transmission` (`--tr 4.0.6|4.1.4`) and `--clients rtorrent`,
the same torrent set is observed in those clients: authentication
(including rejection of a wrong password), version probe, complete,
verified-half, empty, private, deselected, stopped, and magnet torrents,
privacy, file selection, and availability semantics (Transmission reports
known 0 with no connected peers; rTorrent availability is unsupported). For
Transmission the test also sends the adapter's exact `torrent-get` request and
checks every field is present, which is what establishes that the legacy
protocol remains sufficient on 4.1.4. With `--clients deluge`, the same set is
observed through deluge-web, which the harness (not Housekeeparr) connects to
the daemon; Deluge reports distributed copies of 0.5 for the half torrent
(own pieces counted, as with qBittorrent) and 0, treated as unknown, otherwise. rTorrent 0.9.8 does not list a magnet
without a tracker when DHT is off, so the harness adds the closed loopback
tracker to it. Since 2026-11-15 the harness pre-fetches every artifact and then re-runs
itself in an unprivileged loopback-only network namespace (`unshare -rn`;
`--no-netns` opts out). Earlier runs were not isolated: qBittorrent ran with
UPnP/NAT-PMP enabled (now disabled explicitly, with peer-country lookup off,
which crashed qBittorrent 4.6.7 without network), and Deluge's libtorrent
session starts with libtorrent defaults before Deluge applies its own
settings. Those earlier runs may therefore have reached the LAN gateway
(UPnP/NAT-PMP) or DHT bootstrap nodes. That was not observed directly; the
configuration only establishes that DHT/PeX/LSD were disabled before any
synthetic torrent was added and that every tracker is a closed loopback port.
Isolation now enforces that nothing leaves the host. Every run also asserts
that real listings are complete and that queue row counts equal
`totalRecords`.

Observed corrections applied: qBittorrent reports availability 0 (not −1) for
stopped torrents and torrents without metadata, so the adapter now treats
availability as unknown outside active downloading.

Not exercised: the qBittorrent `error` state (distinct from `missingFiles`), queueing,
checking states as stable observations, real peers or swarm counts, v2/hybrid
torrents, authentication bypass, reverse-proxy base paths, TLS, large
libraries, import completion, and manager correlation with any client other
than qBittorrent. Series/movies in the manager library and grab history are
exercised only by the managed-row probes below, with qBittorrent.

## Missing-data (client error) semantics

Observed 2026-11-15 for a torrent that was verified, stopped, had its payload
deleted, and was started again (no peers; no restart of the client):

| Client | Reported | Housekeeparr class |
| --- | --- | --- |
| Transmission 4.0.6, 4.1.4 | `error = 3`, "No data found! Ensure your drives are connected…" | client error |
| rTorrent 0.9.8 | stopped by rTorrent, `d.message` "Download registered as completed, but hash check returned unfinished chunks.", completed bytes reset to 0 | client error (since this run; previously misread as a user pause) |
| qBittorrent 5.3.0, 4.6.7 | still `stalledUP` (complete) | complete |
| Deluge 2.2.0 | still `Seeding` | complete |

Missing data after a client restart, and after a forced recheck (fixtures
`errored-restart`: verified, client stopped cleanly, payload deleted,
client restarted; `errored-recheck`: verified, payload deleted while
running, recheck forced):

| Client | after restart | after forced recheck |
| --- | --- | --- |
| qBittorrent 5.3.0, 4.6.7 | `missingFiles` → client error (verified 0) | `stalledDL` → **downloading**, verified 0, no error |
| Transmission 4.0.6, 4.1.4 | `error = 3` → client error | `error = 3` → client error |
| Deluge 2.2.0 | `Error` "Missing or invalid torrent data!" → client error | `Downloading`, verified 0, no error |
| rTorrent 0.9.8 | stopped with local message → client error | stopped with local message → client error |

So on the libtorrent clients, lost data surfaces as an error only after a
restart; a recheck turns it into what looks like a fresh download from zero.
Deluge must have persisted its fastresume state (wait for `Seeding` before
the restart), or the restarted daemon does not notice the loss.

rTorrent `d.message` (fixture `rt-msg-probe`): a user `d.stop`/`d.start` or
`d.close`/`d.open` does **not** clear a local message rTorrent set when it
stopped a download itself; only a later tracker announce (active download)
or a successful recheck overwrites it. A healthy torrent stopped by the user
keeps an empty message. Consequence for the adapter's mapping (stopped +
non-tracker message = client error): a user-stopped torrent that still
carries a stale local message is reported as a client error, and after
`d.start` with `d.state=1` but inactive it is reported as paused while the
stale message remains. Diagnosis must treat rTorrent local messages as
possibly stale.

### rTorrent hash-check race

The `b8d3313` failure of `errored-recheck` was not re-triggered: every
standalone, harness-shaped attempt (disposable rTorrent 0.9.8, loopback-only
namespace, with and without CPU contention) reported the expected error. A
mechanism with exactly the failure signature was demonstrated instead, on a
256 MiB synthetic payload whose hash check is slow enough to interleave with:
rTorrent opens a download's files only while it hashes them (none are open
while the check is merely queued). A payload deleted **during** a running
hash check is still read through the open descriptor, the check passes, and
a `d.check_hash` sent meanwhile is silently ignored because `d.hashing` is
non-zero. The download then stays started, `d.complete=1`, with all bytes
verified, over a file `/proc` shows as `(deleted)`: exactly the failure
signature. The harness used fixed sleeps (3 s after hashing every torrent,
before `session.save` and the restart; none before the deletion), so on a
loaded host a check could still be running when the payload was deleted.
The fixture now waits (bounded, early exit) until every download is loaded
and idle before saving and before deleting, and requires the forced check
to fail, so a missed window fails the setup loudly instead of producing a
misleading adapter result. Product consequence: on rTorrent too,
"complete" can coexist with deleted data if the deletion races a hash check
(until the descriptor is closed). This has not been examined for
Transmission.

libtorrent-based clients do not notice missing payload until they read it,
so a "complete" state is not evidence that the data still exists.
Preserved-data failure handling must not rely on client state alone for
qBittorrent and Deluge.

## Workflow-semantics probes

Harness-side requests to disposable instances (`internal/integration/probe_test.go`,
run after the observation tests). These probes are distinct from the
Housekeeparr recovery executor runs below. Released `QueueController.RemoveAction` (Sonarr `v4.0.20.3014`,
Radarr `v6.4.4.10685`) defaults `removeFromClient` to **true**, so every flag
is sent explicitly.

| Date | Versions | Request | Observed |
| --- | --- | --- | --- |
| 2026-11-15 | Sonarr 4.0.20.3014, Radarr 6.4.4.10685, qBittorrent 5.3.0 (isolated) | `DELETE /api/v3/queue/{id}?removeFromClient=false&blocklist=false&skipRedownload=true&changeCategory=false` (ignore path) on an unknown-series / unknown-movie row with partial data | HTTP 200; row removed immediately; **row tracked again after `RefreshMonitoredDownloads`**; no blocklist entry or history event; torrent still registered, same state and verified bytes, payload bytes identical |
| 2026-11-15 | same | same with `blocklist=true` (`MarkAsFailed`, redownload suppressed) | identical to the ignore path: accepted, row returns after refresh, **nothing blocklisted**, client and payload unchanged |

Interpretation: for content without grab history, acceptance of a removal
is not a durable effect and does not blocklist anything, so a recovery
workflow must verify queue state after a refresh and treat these rows as
ineligible.

### Managed rows with real grab history

`internal/integration/managed_test.go`, run only inside the isolated
namespace. The managers add fictional content (Sonarr tvdb 999001, a
four-episode 2020 series; Radarr tmdb 999002–999005) from a loopback
metadata stand-in (`internal/integration/standin`), search a loopback
Torznab stand-in, and grab a synthetic, half-present 4 MiB release into
qBittorrent. The managers' metadata hosts are hard-coded HTTPS names: a
private `/etc/hosts` bind-mounted inside the namespace resolves them to
loopback, and the managers trust the stand-in's throwaway CA through
`SSL_CERT_FILE` (verified for both Sonarr's .NET 6 and Radarr's .NET 8
runtimes). No manager database is edited; setup uses only the managers'
API: root folder, indexer (`forceSave`), quality definitions relaxed to a
zero minimum size (the default WEBDL-1080p minimum rejects a 4 MiB
release), lookup, add (search on add disabled), then a targeted
`EpisodeSearch`/`MoviesSearch`. Sonarr's first search right after the
quality-definition change was still evaluated against the old minimum
(cached); the test retries the targeted search at most three times.

Each variant runs on its own grabbed release, with every flag explicit
(`removeFromClient=false&changeCategory=false` always), followed by two
`RefreshMonitoredDownloads` commands:

| Date | Versions | Variant | Observed (identical on both managers) |
| --- | --- | --- | --- |
| 2026-11-15 | Sonarr 4.0.20.3014, Radarr 6.4.4.10685, qBittorrent 5.3.0 and 4.6.7 (isolated) | ignore: `blocklist=false&skipRedownload=true` | HTTP 200; row removed and **stays removed** after both refreshes; history gains `downloadIgnored`; blocklist unchanged; no search queued; torrent registered, same state and category, payload identical |
| 2026-11-15 | same | blocklist: `blocklist=true&skipRedownload=true` | HTTP 200; row stays removed; history gains `downloadFailed`; **exactly that release is blocklisted**; no search queued; no regrab; torrent and payload preserved as above |
| 2026-11-15 | same | blocklist with `removeFailedDownloads=true` on the manager's download client | identical to blocklist: the explicit `removeFromClient=false` is honoured, and the torrent stays registered |
| 2026-11-15 | same | blocklist with `skipRedownload=false` | as blocklist, plus the manager queues its own replacement search (`EpisodeSearch`/`MoviesSearch`, trigger `unspecified`) immediately: manager-native redownload is the default owner unless suppressed |

Interpretation:
- Unlike unknown rows, removal of a managed row is durable, and the
  blocklist path blocklists only that release.
- The torrent is left running in its original category, so a retained
  incomplete transfer is not stopped by the manager. Separating it (SPEC §7
  step 4) is a separate client operation.
- `skipRedownload` decides which component owns the replacement search.
  Housekeeparr must send `true` whenever it schedules the search itself.

With Transmission 4.0.6/4.1.4, Deluge 2.2.0 (Label plugin enabled for
categories), and rTorrent 0.9.8 (`d.custom1` label) as the managers'
download client (`scripts/it/run.sh --manager-client …`), all four variants
behaved identically: durable removal, the same blocklist and history
effects, the same `skipRedownload` search ownership, torrent and payload
preserved. Transmission does not hash pre-existing partial files on
`torrent-add`, so there the synthetic grabs settled as complete
(`importPending` in the manager) rather than downloading.

Not exercised yet: `changeCategory=true`; a download the client
itself reports as failed (the `removeFailedDownloads` path the managers use
for automatic failed-download handling); completed and imported releases;
season packs.

## Recovery workflow runs

`internal/integration/recovery_test.go` runs the complete no-progress
recovery workflow through Housekeeparr's own planner (`action.Plan`),
coordinator (`internal/action`), executor (`internal/workflow`), and
adapters, on a fresh managed grab, planned from records an engine cycle
wrote to a real store, with every flag explicit:

1. `manager.queue.fail`: `DELETE /api/v3/queue/{id}?removeFromClient=false&blocklist=true&skipRedownload=false&changeCategory=false`,
   verified by observation (queue row absent, a matching blocklist entry and a `downloadFailed` history record that are both new relative to a baseline read just before dispatch);
2. `client.stop` retaining data, verified by listing (still registered, stopped).

The test then verifies independently: the row stays absent after two
`RefreshMonitoredDownloads`; exactly one new `downloadFailed` and no new
grab; the torrent still registered, stopped, same category, payload
byte-identical; the manager queued its own replacement search
(`skipRedownload=false`, so Housekeeparr does not search); no lock left and
a cooldown recorded. The stall diagnosis is supplied by the test (the
90-minute window is covered by deterministic policy tests); everything else
in the proposal comes from engine-written state and live re-fetches.

| Date | Versions | Result |
| --- | --- | --- |
| 2026-11-15 (first attempt) | Sonarr 4.0.20.3014, Radarr 6.4.4.10685, qBittorrent 5.3.0 | failed safely: the executor could not confirm the blocklist entry because released `BlocklistResource` (both managers) exposes **no infohash**; it reported a partial effect, the coordinator held the action as *uncertain* with its locks, and nothing was re-sent. Fixed by matching blocklist entries by exact source title, content (series/episode or movie), and time since dispatch, which is evidence rather than hash identity |
| 2026-11-15 | Sonarr 4.0.20.3014 + qBittorrent 5.3.0; Radarr 6.4.4.10685 + qBittorrent 5.3.0 (isolated) | **passed**: every step verified, every independent check above held |
| 2026-11-15 | Sonarr 4.0.20.3014 + qBittorrent 4.6.7; Radarr 6.4.4.10685 + qBittorrent 4.6.7 (isolated) | passed as a refusal: privacy unknown, the only blocker is "public transfer", nothing sent |
| 2026-11-15 (production path) | Sonarr 4.0.20.3014 + qBittorrent 5.3.0; Radarr 6.4.4.10685 + qBittorrent 5.3.0 (isolated) | **passed**: an engine cycle with engine-built adapters wrote the transfer, correlated queue row, and probes to a real store; the proposal was planned from those stored records (as `act` and the web form do), its fingerprint matched the executor's live re-fetch, and every step and independent check held |

| 2026-11-16 (`2984479`, R5 live checks) | Sonarr 4.0.20.3014 + qBittorrent 5.3.0; Radarr 6.4.4.10685 + qBittorrent 5.3.0 (isolated) | **passed**. Before each write the executor re-read the transfer (state, verified and wanted bytes, privacy, category), the client version, every manager's queue for the download ID, and, before the manager step, the manager version, health, and the content's existence and monitoring (`GET /api/v3/episode/{id}` + `/api/v3/series/{id}`; `GET /api/v3/movie/{id}`), with the step-aware comparison expecting the queue row to be gone before the stop. The first attempt at `ee3d164` refused both before sending anything (see *Integration runs*) |
| 2026-11-16 (`05b2afb`, baseline verification) | same | **passed**. The queue step's evidence is now attributed only against a baseline read immediately before dispatch and journaled with it: on Sonarr the baseline held the grab's history record (id 9) and no matching blocklist entry; verification found one new blocklist entry (id 4, byte-exact title and episode) and one new `downloadFailed` record. Radarr likewise. Content locks, cooldowns, and attempt budgets used manager content identity (`content:sonarr|episode:5`, `attempts:sonarr|episode:5`) |

| 2026-11-16 (`99cf198`) | Sonarr 4.0.20.3014 + Deluge 2.2.0; Radarr 6.4.4.10685 + Deluge 2.2.0 (isolated) | **passed**: same planner, live checks, baseline-attributed verification, and independent checks, with `core.pause_torrent` as the data-retaining stop; the torrent remained registered as `Paused` with the same label and byte-identical payload, and each manager queued its own replacement search |

| 2026-11-16 (`dff2166` + `c26f83a`) | Sonarr 4.0.20.3014 + Transmission 4.0.6; Radarr 6.4.4.10685 + Transmission 4.0.6; the same with Transmission 4.1.4 (isolated) | **passed** on both branches with the legacy RPC: `torrent-stop` as the data-retaining stop; the torrent stayed registered and stopped in its download directory with a byte-identical payload, and each manager queued its own replacement search. The grab was first re-verified by the harness (see *Integration runs*). Registered in `action.Tested` with the exact reported versions `4.0.6 (38c164933e)` and `4.1.4 (a6d907d9ea)`. No second protocol was needed |
| 2026-11-16 (`7b0d1d0` + `1f24634`) | Sonarr 4.0.20.3014 + rTorrent 0.9.8; Radarr 6.4.4.10685 + rTorrent 0.9.8 (isolated) | **passed** in a clean run: `d.stop` (never `d.close`/`d.erase`) as the data-retaining stop; the download stayed registered with `d.state=0`/`d.is_active=0`, the same `d.custom1` label and a byte-identical payload, and each manager queued its own replacement search. Registered in `action.Tested` |
| 2026-11-16 (`b8d3313`) | Sonarr 4.0.20.3014 + rTorrent 0.9.8; Radarr 6.4.4.10685 + rTorrent 0.9.8 (isolated) | workflow **passed** (every step and independent check, `d.stop` as the stop: the download stayed registered, `d.state=0`/`d.is_active=0`, payload byte-identical), but the run as a whole failed on an unrelated observation fixture (see *Integration runs*), so this combination is **not registered** until a clean run. The same run's `d.message` probe again showed messages persisting across `d.stop`/`d.start` |

The qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, and Transmission 4.0.6/4.1.4 combinations above are registered in `action.Tested`; no other
combination is eligible. qBittorrent 4.x does not report privacy, so
recovery is refused there by the "public transfer" prerequisite (the test
asserts this refusal). Not covered on real instances: engine-initiated
(automatic) dispatch (implemented, tested only with simulated
applications); the replacement's grab, progress and completion tracking
(fixture-tested); search acceptance as an outcome tracked by Housekeeparr;
retained-data and capacity limits (fixture-tested).

## Workflows by application

| Application | Floor | Source evidence | Transport | Observation | No-progress recovery | Metadata recovery | Preserved-data failure handling | Partial-pack handling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Sonarr | 4.0 | `v4.0.20.3014` | HTTP API v3, API key header | **tested: 4.0.20.3014**; others contract-tested | **tested: 4.0.20.3014 with qBittorrent 5.3.0, Transmission 4.0.6/4.1.4, Deluge 2.2.0, rTorrent 0.9.8** (manual dispatch) | not implemented | not implemented | not implemented |
| Radarr | 5.0 | `v6.4.4.10685` | HTTP API v3, API key header | **tested: 6.4.4.10685**; others contract-tested | **tested: 6.4.4.10685 with qBittorrent 5.3.0, Transmission 4.0.6/4.1.4, Deluge 2.2.0, rTorrent 0.9.8** (manual dispatch) | not implemented | not implemented | n/a (Sonarr-specific in v1) |
| qBittorrent | 4.1 (WebUI API v2) | `release-4.6.7`, `release-5.3.0` | WebUI API v2, session cookie | **tested: 5.3.0, 4.6.7**; others contract-tested | **tested: 5.3.0** with Sonarr/Radarr above; 4.x refused (privacy unknown) | not implemented | not implemented | — |
| Transmission | 2.80 (RPC 15) | `4.0.6`, `4.1.4` | legacy RPC (deprecated in 4.1, still supported throughout 4.x), session-id handshake, optional basic auth | **tested: 4.0.6, 4.1.4**; others contract-tested | **tested: 4.0.6, 4.1.4** with Sonarr/Radarr above (`torrent-stop`, legacy RPC) | not implemented | not implemented | — |
| Deluge | 2.0 | `deluge-2.0.0`, `deluge-2.1.1`, `deluge-2.2.0` | deluge-web JSON API (`/json`); daemon protocol unsupported | **tested: 2.2.0**; others contract-tested | **tested: 2.2.0** with Sonarr/Radarr above (`core.pause_torrent`) | not implemented | not implemented | — |
| rTorrent | 0.9.8 | `v0.9.8`, `v0.16.28` | XML-RPC over an HTTP(S) SCGI gateway (tested: lighttpd `mod_scgi`); raw SCGI unsupported | **tested: 0.9.8**; others contract-tested | **tested: 0.9.8** with Sonarr/Radarr above (`d.stop`) | not implemented | not implemented | — |

## Observation signals by client

| Signal | qBittorrent | Transmission | Deluge | rTorrent |
| --- | --- | --- | --- | --- |
| Verified progress | `completed` (wanted, hash-checked) | `sizeWhenDone − leftUntilDone` | `total_done` if all files wanted, else `progress × total_wanted` | `completed_bytes`; wanted size chunk-granular if files deselected |
| Piece availability | distributed copies (all pieces, self + connected peers) | `desiredAvailable ÷ leftUntilDone` (wanted only) | distributed copies; 0 treated as unknown | not collected |
| Privacy | 5.0+ (`private`); unsupported on 4.x | `isPrivate` | `private` (unknown without metadata) | `d.is_private` |
| Metadata state | `metaDL` / `forcedMetaDL` | `metadataPercentComplete` | `num_pieces = 0` | `d.is_meta` |
| Swarm counts | tracker-reported | not collected (only with tracker URLs) | tracker-reported | not collected |
| Client error state | `missingFiles` (observed after a restart); `error` not yet observed | `error = 3` (local; observed) | `Error` (observed after a restart) | none structured; a download stopped by rTorrent with a non-tracker `d.message` is a client error (observed) |
| Added time | `added_on` | `addedDate` | `time_added` | `d.load_date` (may reset on restart) |


