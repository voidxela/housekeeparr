# Deluge adapter fixtures

| File | Provenance |
| --- | --- |
| `v2.1.1-get_torrents_status.json` | **Source-derived, not captured.** Status keys and semantics from `deluge/core/torrent.py` (`get_status` key table) and the JSON API envelope from `deluge/ui/web/json_api.py` at tag `deluge-2.1.1`; values synthetic. |

`tracker_status` and `save_path` appear although the adapter never requests
them, so contract tests prove they are not retained. Released-version
observation is validated against Deluge 2.2.0 (see `docs/compatibility.md`).

The first real run (2026-11-15, Deluge 2.2.0) corrected two errors that these
source-derived fixtures had hidden: the version is read with
`daemon.get_version` (exported in 2.0.0, 2.1.1, and 2.2.0; `daemon.info` does
not exist in 2.x), and a call without arguments must send `"params": []`
because deluge-web unpacks params with `*params`. The fake server now rejects
non-list params.
