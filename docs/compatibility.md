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
| 2026-11-16 (exploratory, working tree after `f0721bb`; not a recorded commit) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr + Radarr | first run of `TestAutomaticRecovery`: **failed** (no finding in 13 cycles) because diagnosis ignored transfer-scoped overrides, a product defect fixed in `f0721bb`; second run passed (one dispatch per pass, independent checks held). Exploratory only: the tree changed during the run |
| 2026-11-16 (`6507e87`, pinned worktree, six combinations) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, Transmission 4.0.6, Transmission 4.1.4, each as the managers' client, + Sonarr + Radarr | **all passed**, including both recovery workflows and the automatic test, but the direct recovery tests' recorded replacement search was the harness's own earlier **manual** grab search (for example command 42, "1 reports downloaded", instead of 45, trigger `unspecified`): the time-based matcher's one-minute tolerance reached back to it. Not used as search evidence; fixed in `118f15c` (command-ID baseline) and the tests now require the independently identified command |
| 2026-11-16 (`6507e87`, same matrix) | Linux aarch64, Go 1.27.4 | qBittorrent 4.6.7 + Sonarr + Radarr | **failed**: `TestAutomaticRecovery` saw no qualified diagnosis for either grab within 5 minutes (only three `run --once` cycles completed in that time); everything else passed, including the recovery refusal for unknown privacy. Cause not established |
| 2026-11-16 (`118f15c`, pinned worktree, same five combinations) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, Transmission 4.0.6, Transmission 4.1.4, each as the managers' client, + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | **all passed**: every observation test and probe, both recovery workflows, and the automatic test; the new live check read both managers' redownload settings (`/api/v3/config/downloadclient`); Housekeeparr recorded exactly the manager-native replacement search (trigger `unspecified`, newer than the journaled command baseline, "0 reports downloaded") for all four recoveries of each run |
| 2026-11-16 (`118f15c`, same matrix) | Linux aarch64, Go 1.27.4 | qBittorrent 4.6.7 + Sonarr + Radarr | **failed** on the observation fixture `errored-restart`: after the client restart qBittorrent 4.6.7 reported `stalledDL`, verified 0, instead of `missingFiles` (as it did on 2026-11-15). Not yet diagnosed; hypothesis, not established: the restart happened before qBittorrent persisted its fastresume state (compare the Deluge note below). Everything else passed: the recovery refusals for unknown privacy, and the automatic test (both stalls diagnosed by the engine in four 0–1 s cycles; automatic recovery refused as untested and privacy-unknown, nothing sent). The `6507e87` automatic failure did not recur |
| 2026-11-16 (`c966439`, final session HEAD, pinned worktree) | Linux aarch64, Go 1.27.4 | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 (isolated) | **all passed**, including both recovery workflows, exact search attribution, and the automatic test, with the workflow-pause gate and attempts view in the tree. Live redownload state on both managers: `autoRedownloadFailed=true`, `autoRedownloadFailedFromInteractiveSearch=true`, grab `releaseSource="UserInvokedSearch"` |

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
libraries, and import completion. Manager correlation and managed grabs are
exercised with each of the four clients as the managers' download client
(`--manager-client`; see the probe, recovery, and automatic sections below).

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
| qBittorrent 5.3.0, 4.6.7 | `missingFiles` → client error (verified 0); not reproduced on 4.6.7 at `118f15c` (`stalledDL`, verified 0; undiagnosed harness failure) | `stalledDL` → **downloading**, verified 0, no error |
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

| 2026-11-16 (`118f15c`) | Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 with each of qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, Transmission 4.0.6, 4.1.4 (isolated) | **passed** again after the shared-executor changes (manager redownload-settings live check, command-ID baseline in the manager step, post-recovery search observation); Housekeeparr recorded the manager's replacement search as completed for exactly the command the test identifies independently |
| 2026-11-16 (`dff2166` + `c26f83a`) | Sonarr 4.0.20.3014 + Transmission 4.0.6; Radarr 6.4.4.10685 + Transmission 4.0.6; the same with Transmission 4.1.4 (isolated) | **passed** on both branches with the legacy RPC: `torrent-stop` as the data-retaining stop; the torrent stayed registered and stopped in its download directory with a byte-identical payload, and each manager queued its own replacement search. The grab was first re-verified by the harness (see *Integration runs*). Registered in `action.Tested` with the exact reported versions `4.0.6 (38c164933e)` and `4.1.4 (a6d907d9ea)`. No second protocol was needed |
| 2026-11-16 (`7b0d1d0` + `1f24634`) | Sonarr 4.0.20.3014 + rTorrent 0.9.8; Radarr 6.4.4.10685 + rTorrent 0.9.8 (isolated) | **passed** in a clean run: `d.stop` (never `d.close`/`d.erase`) as the data-retaining stop; the download stayed registered with `d.state=0`/`d.is_active=0`, the same `d.custom1` label and a byte-identical payload, and each manager queued its own replacement search. Registered in `action.Tested` |
| 2026-11-16 (`b8d3313`) | Sonarr 4.0.20.3014 + rTorrent 0.9.8; Radarr 6.4.4.10685 + rTorrent 0.9.8 (isolated) | workflow **passed** (every step and independent check, `d.stop` as the stop: the download stayed registered, `d.state=0`/`d.is_active=0`, payload byte-identical), but the run as a whole failed on an unrelated observation fixture (see *Integration runs*), so this combination is **not registered** until a clean run. The same run's `d.message` probe again showed messages persisting across `d.stop`/`d.start` |

The qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, and Transmission 4.0.6/4.1.4 combinations above are registered in `action.Tested`; no other
combination is eligible. qBittorrent 4.x does not report privacy; at the
recorded commits recovery was refused there by the "public transfer"
prerequisite. Since the user decision of 2026-11-16 unknown privacy is
treated as private (grab blocklisted, torrent left running) unless a scope
turns that off, but no 4.x combination is registered, so it still sends
nothing; that path needs its own real-instance run before registration. Automatic dispatch and replacement-search observation are covered in
the sections below. Not covered on real instances: the replacement's grab,
progress and completion tracking, and retained-data and capacity limits
(fixture-tested).

### Metadata recovery runs

`TestSonarrMetadataRecoveryWorkflow` and `TestRadarrMetadataRecoveryWorkflow`
run the same planner, coordinator, executor and independent checks for the
`metadata_recovery` workflow. The stand-in indexer offers a magnet-only
release for a random hash whose only tracker is a closed loopback port (DHT
is off), so the manager grabs it and the client can never obtain metadata.
The metadata-timeout diagnosis is supplied by the test (the timeout is
covered by deterministic policy tests). There is no payload to compare.

| Date | Versions | Result |
| --- | --- | --- |
| 2026-11-16 (first attempt, uncommitted tree) | Sonarr 4.0.20.3014, Radarr 6.4.4.10685, qBittorrent 5.3.0 | setup failed, nothing sent: both managers refuse a **trackerless** magnet when the client reports DHT disabled (`Magnet Links without trackers not supported if DHT is disabled`). The fixture magnet now carries the closed loopback tracker |
| 2026-11-16 (`64062ee`, pinned worktree, `--only RecoveryWorkflow`) | Sonarr 4.0.20.3014 + qBittorrent 5.3.0; Radarr 6.4.4.10685 + qBittorrent 5.3.0 (isolated) | **passed**: qBittorrent reported the grab as `metaDL` with privacy and wanted size unknown; `manager.queue.fail` and `client.stop` were each verified; independently the row stayed absent after two refreshes, exactly this release was blocklisted with one new `downloadFailed`, the torrent stayed registered as `stoppedDL` in its category, and each manager queued its own replacement search, which Housekeeparr recorded. The same selected run re-passed both no-progress workflow tests after the fixture changes. Not run in this selection: observation tests, removal-semantics probes, automatic dispatch |
| 2026-11-16 (`52f99c5`, `--only MetadataRecovery`) | both managers with Deluge 2.2.0; Transmission 4.0.6; Transmission 4.1.4 (isolated, one run each) | **passed** (Deluge reports the magnet as `Downloading` with no pieces, Transmission as status 4 with `metadataPercentComplete` < 1; both stop to a paused state) |
| 2026-11-16 (`52f99c5`) | both managers with rTorrent 0.9.8 | **failed safely**: the manager step was held as *uncertain* and not retried. Without metadata rTorrent names the download `<HASH>.meta`, so the managers' queue row carries that title, while they blocklist under the grabbed release's title; the executor matched blocklist entries by queue title and found none (row absent, new `downloadFailed` recorded, 0 new blocklist entries). Fixed in `c5e050a`: entries are matched by the source title of the newest grab in the download's history (journaled in the baseline), falling back to the queue title |
| 2026-11-16 (`c5e050a`, `--only RecoveryWorkflow`) | both managers with qBittorrent 5.3.0 | **passed**: no-progress, metadata and private (below) workflow tests after the shared matching change |
| 2026-11-16 (`c5e050a`, `--only RecoveryWorkflow`) | both managers with rTorrent 0.9.8 | no-progress and private **passed**; metadata workflow succeeded with every step verified, but the test's own independent blocklist check still used the queue title and failed (test defect, fixed in `7925ce2`) |
| 2026-11-16 (`7925ce2`, `--only MetadataRecovery`) | both managers with rTorrent 0.9.8 | **passed**: `d.stop` leaves the metadata-less download registered and stopped; blocklisted under the release title |
| 2026-11-16 (`7925ce2`, `--only 'MetadataRecovery\|PrivateRecovery'`) | both managers with Deluge 2.2.0; Transmission 4.0.6; Transmission 4.1.4 (one run each) | **passed** again after the shared matching change. On Transmission 4.0.6 the Sonarr test took 275 s because the harness's first grab search needed a second attempt (setup, before the workflow) |

Registered in `action.Tested` for `metadata_recovery`: Sonarr 4.0.20.3014 and
Radarr 6.4.4.10685 with qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8 and
Transmission 4.0.6/4.1.4. These selected runs did not repeat observation
tests, removal-semantics probes or the real-instance automatic-dispatch
test; automatic dispatch of metadata recovery is fixture-tested only
(`TestAutomaticMetadataRecoveryEndToEnd`), and the 60-minute metadata
timeout itself is covered by deterministic policy tests, not waited out.

### Private-torrent recovery runs

`TestSonarrPrivateRecoveryWorkflow` and `TestRadarrPrivateRecoveryWorkflow`
grab a private release (`info.private = 1`) and run no-progress recovery
under the default `private_recovery = "keep_running"`: the plan must hold
only `manager.queue.fail` (same explicit flags). Independent checks: row
absent after refreshes, release blocklisted, one new `downloadFailed`, the
manager's replacement search queued and recorded, payload byte-identical,
and the torrent **still downloading in the client, in its category, never
stopped, and not recorded as retained**.

| Date | Versions | Result |
| --- | --- | --- |
| 2026-11-16 (`c5e050a`) | both managers with qBittorrent 5.3.0; rTorrent 0.9.8 | **passed** |
| 2026-11-16 (`7925ce2`) | both managers with Deluge 2.2.0; Transmission 4.0.6; Transmission 4.1.4 | **passed** |

### Runs after the shared blocklist-matching change (session of 2026-11-16, third)

| Date | Commit and selection | Versions | Result |
| --- | --- | --- | --- |
| 2026-11-16 | `e072d85`, `--only '^Test(Sonarr\|Radarr)RecoveryWorkflow$'` | both managers with Deluge 2.2.0; Transmission 4.0.6; Transmission 4.1.4 (one run each) | **passed**: public no-progress recovery with blocklist evidence matched by the grab's source title |
| 2026-11-16 | `e072d85`, `--only TestAutomaticRecovery` | both managers with qBittorrent 5.3.0 | **passed** (production `run --once` binary) |
| 2026-11-16 | `7437981`, **unselected** (the harness's default path, first full run since `--only`, the four new required probes and the magnet/private fixtures were added) | qBittorrent 5.3.0 + Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 | **all 16 required tests passed**: observation, queue and managed removal semantics, no-progress, metadata and private recovery for both managers, then automatic dispatch with a kept-running private torrent and a stopped magnet present in the client |

Not re-run after `c5e050a`: automatic dispatch on Deluge, rTorrent and
Transmission (last at `118f15c`), and rTorrent's observation tests. Commit
`8efd56d` (unknown privacy treated as private) changes planning only for
transfers whose privacy is unknown, which none of the registered client
versions report; it has no real-instance run.

`private_recovery = "stop"` uses the same two steps as public recovery and
was not run separately on a private torrent; `"observe"` sends nothing
(fixture-tested).

## Automatic dispatch runs

`internal/integration/automatic_test.go` (`TestAutomaticRecovery`, run last
with both managers) builds the production binary static and runs
`housekeeparr run --once --headless` repeatedly on one state directory. Two
fresh managed grabs (Sonarr S01E06, Radarr movie 999007) genuinely make no
progress (no peer can connect). Two `[[override]]`s for exactly those
transfers set `new_download_grace = "0s"`, `no_progress_window = "1m"`,
`min_observations = 3` and enable `no_progress_recovery`; global automation
stays off, so nothing else on the client is eligible and the other
never-progressing fixtures keep the default 90-minute window (they neither
qualify nor trip the correlated-stall breaker). Observe mode first: the real
engine diagnoses both stalls from its own observations and nothing is sent.
Then automatic mode: one pass dispatches exactly one recovery
(`actions_per_cycle` 1) while the other is recorded as waiting, the next pass
dispatches it, two later passes send nothing, `finding <id> --json` shows the
dispatched decision, and the same independent checks as the manual recovery
test hold for both. The binary plans against the compiled `action.Tested`;
the test fails if any registered combination is refused, and requires a
visible refusal for qBittorrent 4.x.

| Date | Versions | Result |
| --- | --- | --- |
| 2026-11-16 (`118f15c`) | Sonarr 4.0.20.3014 + Radarr 6.4.4.10685 with qBittorrent 5.3.0, Deluge 2.2.0, rTorrent 0.9.8, Transmission 4.0.6, Transmission 4.1.4 (isolated) | **passed** on all five |
| 2026-11-16 (`118f15c`) | the same with qBittorrent 4.6.7 | **passed as a refusal**: diagnosed, then refused (untested combination, privacy unknown); nothing sent |

Not established by these runs: the daemon loop (`run` without `--once`),
the default diagnosis windows (deterministic tests only), timing across
real polling intervals; SPEC §13.4 scenario 1 is therefore not complete on
real instances.

## Replacement search observation

After a recovery, the manager-native redownload queues `EpisodeSearch` (one
episode) or `MoviesSearch` with trigger `unspecified` and the content IDs in
its body (trigger and body confirmed on the instances above). From released source
(`RedownloadFailedDownloadService`, Sonarr `v4.0.20.3014`, Radarr
`v6.4.4.10685`; not exercised on an instance with the settings off) it is skipped
when `autoRedownloadFailed` is off, or for an interactively grabbed release
when `autoRedownloadFailedFromInteractiveSearch` is off; recovery now
requires those settings live before the manager write. The command list
(`GET /api/v3/command`, captured fixtures under the adapters' `testdata`)
still showed finished commands seven minutes after they ended; how long the
managers keep them is not established. Sonarr 4.0.20 also pushes
`SeasonSearch` when a whole season failed; packs are refused, so that is not
exercised.

## Workflows by application

| Application | Floor | Source evidence | Transport | Observation | No-progress recovery | Metadata recovery | Preserved-data failure handling | Partial-pack handling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Sonarr | 4.0 | `v4.0.20.3014` | HTTP API v3, API key header | **tested: 4.0.20.3014**; others contract-tested | **tested: 4.0.20.3014 with qBittorrent 5.3.0, Transmission 4.0.6/4.1.4, Deluge 2.2.0, rTorrent 0.9.8** (manual and automatic dispatch) | **tested with qBittorrent 5.3.0, Transmission 4.0.6/4.1.4, Deluge 2.2.0, rTorrent 0.9.8** (manual path; automatic fixture-tested) | not implemented | not implemented |
| Radarr | 5.0 | `v6.4.4.10685` | HTTP API v3, API key header | **tested: 6.4.4.10685**; others contract-tested | **tested: 6.4.4.10685 with qBittorrent 5.3.0, Transmission 4.0.6/4.1.4, Deluge 2.2.0, rTorrent 0.9.8** (manual and automatic dispatch) | **tested with qBittorrent 5.3.0, Transmission 4.0.6/4.1.4, Deluge 2.2.0, rTorrent 0.9.8** (manual path; automatic fixture-tested) | not implemented | n/a (Sonarr-specific in v1) |
| qBittorrent | 4.1 (WebUI API v2) | `release-4.6.7`, `release-5.3.0` | WebUI API v2, session cookie | **tested: 5.3.0, 4.6.7**; others contract-tested | **tested: 5.3.0** with Sonarr/Radarr above; 4.x not registered (privacy unknown: would be treated as private; untested) | **tested: 5.3.0** with Sonarr/Radarr above | not implemented | — |
| Transmission | 2.80 (RPC 15) | `4.0.6`, `4.1.4` | legacy RPC (deprecated in 4.1, still supported throughout 4.x), session-id handshake, optional basic auth | **tested: 4.0.6, 4.1.4**; others contract-tested | **tested: 4.0.6, 4.1.4** with Sonarr/Radarr above (`torrent-stop`, legacy RPC) | **tested: 4.0.6, 4.1.4** with Sonarr/Radarr above | not implemented | — |
| Deluge | 2.0 | `deluge-2.0.0`, `deluge-2.1.1`, `deluge-2.2.0` | deluge-web JSON API (`/json`); daemon protocol unsupported | **tested: 2.2.0**; others contract-tested | **tested: 2.2.0** with Sonarr/Radarr above (`core.pause_torrent`) | **tested: 2.2.0** with Sonarr/Radarr above | not implemented | — |
| rTorrent | 0.9.8 | `v0.9.8`, `v0.16.28` | XML-RPC over an HTTP(S) SCGI gateway (tested: lighttpd `mod_scgi`); raw SCGI unsupported | **tested: 0.9.8**; others contract-tested | **tested: 0.9.8** with Sonarr/Radarr above (`d.stop`) | **tested: 0.9.8** with Sonarr/Radarr above | not implemented | — |

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


