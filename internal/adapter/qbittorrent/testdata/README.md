# qBittorrent adapter fixtures

| File | Provenance |
| --- | --- |
| `v5.3.0-torrents_info.json` | **Source-derived, not captured.** Field set from `src/webui/api/serialize/serialize_torrent.{h,cpp}` at tag `release-5.3.0`; values are synthetic. |
| `captured-v5.3.0-torrents_info.json` | **Captured** from a disposable qBittorrent 5.3.0 instance by `scripts/it/run.sh --capture` on 2026-11-15 (locally generated torrents; tracker, magnet, path, and comment fields replaced with `[sanitized]`). |
| `captured-v4.6.7-torrents_info.json` | **Captured** the same way from qBittorrent 4.6.7. |
| `v4.6.7-torrents_info.json` | **Source-derived, not captured.** Field set from the same files at tag `release-4.6.7` (no `private`/`has_metadata`; `pausedDL`/`pausedUP` state names). |

The tracker URL and magnet link intentionally contain a fake passkey
(`0123456789abcdef0123456789abcdef`) so contract tests can prove that such
fields are never decoded, stored, or displayed.

The source-derived files cover states the harness cannot easily produce
(errors, queueing, real peers); the captured files pin behaviour observed on
real released instances.
