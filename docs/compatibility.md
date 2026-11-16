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

Not exercised: qBittorrent `error`/`missingFiles` and Deluge `Error` states
(never observed: payload deletion without a restart left both complete; see
below), missing files after a client restart or forced recheck, queueing,
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

libtorrent-based clients do not notice missing payload until they read it,
so a "complete" state is not evidence that the data still exists. Missing
files after a client restart or a forced recheck have not been exercised yet.
Preserved-data failure handling must not rely on client state alone for
qBittorrent and Deluge.

## Workflow-semantics probes

Harness-side requests to disposable instances (`internal/integration/probe_test.go`,
run after the observation tests); Housekeeparr adapters still have no write
methods. Released `QueueController.RemoveAction` (Sonarr `v4.0.20.3014`,
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

Not exercised yet: these variants with Transmission, Deluge, or rTorrent as
the managers' download client; `changeCategory=true`; a download the client
itself reports as failed (the `removeFailedDownloads` path the managers use
for automatic failed-download handling); completed and imported releases;
season packs.

## Recovery workflow runs

`internal/integration/recovery_test.go` runs the complete no-progress
recovery workflow through Housekeeparr's own planner (`action.Plan`),
coordinator (`internal/action`), executor (`internal/workflow`), and
adapters, on a fresh managed grab, with every flag explicit:

1. `manager.queue.fail`: `DELETE /api/v3/queue/{id}?removeFromClient=false&blocklist=true&skipRedownload=false&changeCategory=false`,
   verified by observation (queue row absent, a matching blocklist entry, a `downloadFailed` history event);
2. `client.stop` retaining data, verified by listing (still registered, stopped).

The test then verifies independently: the row stays absent after two
`RefreshMonitoredDownloads`; exactly one new `downloadFailed` and no new
grab; the torrent still registered, stopped, same category, payload
byte-identical; the manager queued its own replacement search
(`skipRedownload=false`, so Housekeeparr does not search); no lock left and
a cooldown recorded. The stall diagnosis is supplied by the test (the
90-minute window is covered by deterministic policy tests); everything else
in the proposal comes from live reads.

| Date | Versions | Result |
| --- | --- | --- |
| 2026-11-15 (first attempt) | Sonarr 4.0.20.3014, Radarr 6.4.4.10685, qBittorrent 5.3.0 | failed safely: the executor could not confirm the blocklist entry because released `BlocklistResource` (both managers) exposes **no infohash**; it reported a partial effect, the coordinator held the action as *uncertain* with its locks, and nothing was re-sent. Fixed by matching blocklist entries by exact source title, content (series/episode or movie), and time since dispatch, which is evidence rather than hash identity |
| 2026-11-15 | Sonarr 4.0.20.3014 + qBittorrent 5.3.0; Radarr 6.4.4.10685 + qBittorrent 5.3.0 (isolated) | **passed**: every step verified, every independent check above held |
| 2026-11-15 | Sonarr 4.0.20.3014 + qBittorrent 4.6.7; Radarr 6.4.4.10685 + qBittorrent 4.6.7 (isolated) | passed as a refusal: privacy unknown, the only blocker is "public transfer", nothing sent |

These two combinations are registered in `action.Tested`; no other
combination is eligible. qBittorrent 4.x does not report privacy, so
recovery is refused there by the "public transfer" prerequisite (the test
asserts this refusal). Not covered: engine-initiated (automatic) dispatch,
which is not implemented; tracking of the replacement's search result, grab,
and progress after the workflow; retained-data and capacity limits; other
clients.

## Workflows by application

| Application | Floor | Source evidence | Transport | Observation | No-progress recovery | Metadata recovery | Preserved-data failure handling | Partial-pack handling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Sonarr | 4.0 | `v4.0.20.3014` | HTTP API v3, API key header | **tested: 4.0.20.3014**; others contract-tested | **tested: 4.0.20.3014 with qBittorrent 5.3.0** (manual dispatch) | not implemented | not implemented | not implemented |
| Radarr | 5.0 | `v6.4.4.10685` | HTTP API v3, API key header | **tested: 6.4.4.10685**; others contract-tested | **tested: 6.4.4.10685 with qBittorrent 5.3.0** (manual dispatch) | not implemented | not implemented | n/a (Sonarr-specific in v1) |
| qBittorrent | 4.1 (WebUI API v2) | `release-4.6.7`, `release-5.3.0` | WebUI API v2, session cookie | **tested: 5.3.0, 4.6.7**; others contract-tested | **tested: 5.3.0** with Sonarr/Radarr above; 4.x refused (privacy unknown) | not implemented | not implemented | — |
| Transmission | 2.80 (RPC 15) | `4.0.6`, `4.1.4` | legacy RPC (deprecated in 4.1, still supported throughout 4.x), session-id handshake, optional basic auth | **tested: 4.0.6, 4.1.4**; others contract-tested | not implemented | not implemented | not implemented | — |
| Deluge | 2.0 | `deluge-2.0.0`, `deluge-2.1.1`, `deluge-2.2.0` | deluge-web JSON API (`/json`); daemon protocol unsupported | **tested: 2.2.0**; others contract-tested | not implemented | not implemented | not implemented | — |
| rTorrent | 0.9.8 | `v0.9.8`, `v0.16.28` | XML-RPC over an HTTP(S) SCGI gateway (tested: lighttpd `mod_scgi`); raw SCGI unsupported | **tested: 0.9.8**; others contract-tested | not implemented | not implemented | not implemented | — |

## Observation signals by client

| Signal | qBittorrent | Transmission | Deluge | rTorrent |
| --- | --- | --- | --- | --- |
| Verified progress | `completed` (wanted, hash-checked) | `sizeWhenDone − leftUntilDone` | `total_done` if all files wanted, else `progress × total_wanted` | `completed_bytes`; wanted size chunk-granular if files deselected |
| Piece availability | distributed copies (all pieces, self + connected peers) | `desiredAvailable ÷ leftUntilDone` (wanted only) | distributed copies; 0 treated as unknown | not collected |
| Privacy | 5.0+ (`private`); unsupported on 4.x | `isPrivate` | `private` (unknown without metadata) | `d.is_private` |
| Metadata state | `metaDL` / `forcedMetaDL` | `metadataPercentComplete` | `num_pieces = 0` | `d.is_meta` |
| Swarm counts | tracker-reported | not collected (only with tracker URLs) | tracker-reported | not collected |
| Client error state | `error`, `missingFiles` (mapping not yet observed on a real instance) | `error = 3` (local; observed) | `Error` (mapping not yet observed) | none structured; a download stopped by rTorrent with a non-tracker `d.message` is a client error (observed) |
| Added time | `added_on` | `addedDate` | `time_added` | `d.load_date` (may reset on restart) |


