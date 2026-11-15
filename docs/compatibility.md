# Compatibility matrix

Status values: **contract-tested** = adapter tests pass against fixtures
derived from the cited released source, but no real instance has been
exercised · **tested** = a disposable-instance integration run of that exact
released version passed (none yet) · **not implemented** = Housekeeparr has
no code for this workflow · **unsupported** = refused by design.

No version below is claimed as *tested*. Probes report `untested` for every
version until a real run is recorded here and in the adapter's
`TestedVersions`.

## Workflows by application

| Application | Floor | Source evidence | Transport | Observation | No-progress recovery | Metadata recovery | Preserved-data failure handling | Partial-pack handling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Sonarr | 4.0 | `v4.0.20.3014` | HTTP API v3, API key header | contract-tested | not implemented | not implemented | not implemented | not implemented |
| Radarr | 5.0 | `v6.4.4.10685` | HTTP API v3, API key header | contract-tested | not implemented | not implemented | not implemented | n/a (Sonarr-specific in v1) |
| qBittorrent | 4.1 (WebUI API v2) | `release-4.6.7`, `release-5.3.0` | WebUI API v2, session cookie | contract-tested | not implemented | not implemented | not implemented | — |
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

Assumptions the first integration run must confirm are listed in
[STATUS.md](STATUS.md#compatibility).
