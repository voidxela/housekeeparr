# Compatibility matrix

Status values: **contract-tested** = adapter tests pass against fixtures
derived from the cited released source, but no real instance has been
exercised · **tested** = a disposable-instance integration run of that exact
released version passed (see *Integration runs* below) · **not implemented** = Housekeeparr has
no code for this workflow · **unsupported** = refused by design.

Probes report `tested` only for exact versions listed in an adapter's
`TestedVersions`; every other version reports `untested` and is still
observed. **Tested means the observation workflow only** — no write workflow
exists yet.

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
libraries, series/movies that exist in the manager library, grab history,
import completion, and manager correlation with any client other than
qBittorrent.

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
ineligible. The same requests on rows with real grab history (managed
content) are not yet probed; that is the prerequisite for any recovery
workflow.

## Workflows by application

| Application | Floor | Source evidence | Transport | Observation | No-progress recovery | Metadata recovery | Preserved-data failure handling | Partial-pack handling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Sonarr | 4.0 | `v4.0.20.3014` | HTTP API v3, API key header | **tested: 4.0.20.3014**; others contract-tested | not implemented | not implemented | not implemented | not implemented |
| Radarr | 5.0 | `v6.4.4.10685` | HTTP API v3, API key header | **tested: 6.4.4.10685** (unknown-movie queue items only); others contract-tested | not implemented | not implemented | not implemented | n/a (Sonarr-specific in v1) |
| qBittorrent | 4.1 (WebUI API v2) | `release-4.6.7`, `release-5.3.0` | WebUI API v2, session cookie | **tested: 5.3.0, 4.6.7**; others contract-tested | not implemented | not implemented | not implemented | — |
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


