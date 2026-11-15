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

What the suite exercised (`scripts/it/run.sh`, `internal/integration`):
session login; version probe; listing of complete, half-complete (after
recheck), empty, private, partially deselected, stopped, and magnet-only
torrents; state mapping (`stalledUP`, `stalledDL`, `stoppedDL`/`pausedDL`,
`metaDL`); verified bytes; privacy (5.x known, 4.6 unsupported); file
selection; availability (a half-complete torrent with no peers reports 0.5,
confirming that libtorrent counts this client's own pieces); absence of
tracker data in normalized snapshots; Sonarr status, health, and queue for
torrents of unknown series (download ID = infohash); end-to-end correlation
through the engine.

Observed corrections applied: qBittorrent reports availability 0 (not −1) for
stopped torrents and torrents without metadata, so the adapter now treats
availability as unknown outside active downloading.

Not exercised: client error states (`error`, `missingFiles`), queueing,
checking, real peers or swarm counts, v2/hybrid torrents, authentication
bypass, reverse-proxy base paths, TLS, large libraries, Sonarr series that
exist in the library, import completion, and every other application.

## Workflows by application

| Application | Floor | Source evidence | Transport | Observation | No-progress recovery | Metadata recovery | Preserved-data failure handling | Partial-pack handling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Sonarr | 4.0 | `v4.0.20.3014` | HTTP API v3, API key header | **tested: 4.0.20.3014**; others contract-tested | not implemented | not implemented | not implemented | not implemented |
| Radarr | 5.0 | `v6.4.4.10685` | HTTP API v3, API key header | contract-tested | not implemented | not implemented | not implemented | n/a (Sonarr-specific in v1) |
| qBittorrent | 4.1 (WebUI API v2) | `release-4.6.7`, `release-5.3.0` | WebUI API v2, session cookie | **tested: 5.3.0, 4.6.7**; others contract-tested | not implemented | not implemented | not implemented | — |
| Transmission | 2.80 (RPC 15) | `4.0.6`, `4.1.4` | legacy RPC (deprecated in 4.1), session-id handshake, optional basic auth | contract-tested | not implemented | not implemented | not implemented | — |
| Deluge | 2.0 | `deluge-2.1.1` | deluge-web JSON API (`/json`); daemon protocol unsupported | contract-tested | not implemented | not implemented | not implemented | — |
| rTorrent | 0.9.8 | `v0.9.8`, `v0.16.28` | XML-RPC over an HTTP(S) SCGI gateway; raw SCGI unsupported | contract-tested | not implemented | not implemented | not implemented | — |

## Observation signals by client

| Signal | qBittorrent | Transmission | Deluge | rTorrent |
| --- | --- | --- | --- | --- |
| Verified progress | `completed` (wanted, hash-checked) | `sizeWhenDone − leftUntilDone` | `total_done` if all files wanted, else `progress × total_wanted` | `completed_bytes`; wanted size chunk-granular if files deselected |
| Piece availability | distributed copies (all pieces, self + connected peers) | `desiredAvailable ÷ leftUntilDone` (wanted only) | distributed copies; 0 treated as unknown | not collected |
| Privacy | 5.0+ (`private`); unsupported on 4.x | `isPrivate` | `private` (unknown without metadata) | `d.is_private` |
| Metadata state | `metaDL` / `forcedMetaDL` | `metadataPercentComplete` | `num_pieces = 0` | `d.is_meta` |
| Swarm counts | tracker-reported | not collected (only with tracker URLs) | tracker-reported | not collected |
| Client error state | `error`, `missingFiles` | `error = 3` (local) | `Error` | none structured (message text only) |
| Added time | `added_on` | `addedDate` | `time_added` | `d.load_date` (may reset on restart) |


