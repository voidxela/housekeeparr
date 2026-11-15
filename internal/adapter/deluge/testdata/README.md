# Deluge adapter fixtures

| File | Provenance |
| --- | --- |
| `v2.1.1-get_torrents_status.json` | **Source-derived, not captured.** Status keys and semantics from `deluge/core/torrent.py` (`get_status` key table) and the JSON API envelope from `deluge/ui/web/json_api.py` at tag `deluge-2.1.1`; values synthetic. |

`tracker_status` and `save_path` appear although the adapter never requests
them, so contract tests prove they are not retained. Released-version
compatibility has not been established, and the integration harness does not
start Deluge yet.
