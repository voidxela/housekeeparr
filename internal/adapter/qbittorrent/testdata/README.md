# qBittorrent adapter fixtures

| File | Provenance |
| --- | --- |
| `v5.3.0-torrents_info.json` | **Source-derived, not captured.** Field set from `src/webui/api/serialize/serialize_torrent.{h,cpp}` at tag `release-5.3.0`; values are synthetic. |
| `v4.6.7-torrents_info.json` | **Source-derived, not captured.** Field set from the same files at tag `release-4.6.7` (no `private`/`has_metadata`; `pausedDL`/`pausedUP` state names). |

The tracker URL and magnet link intentionally contain a fake passkey
(`0123456789abcdef0123456789abcdef`) so contract tests can prove that such
fields are never decoded, stored, or displayed.

These fixtures establish adapter *contracts* only; they are not captures from
a running instance. Released-version compatibility has not been established.
The disposable-instance harness `scripts/it/run.sh` (which can also write
sanitized captures with `--capture`) exists but has not yet been executed;
see `docs/STATUS.md`.
