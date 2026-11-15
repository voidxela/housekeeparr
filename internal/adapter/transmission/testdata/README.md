# Transmission adapter fixtures

| File | Provenance |
| --- | --- |
| `v4.0.6-session-get.json`, `v4.0.6-torrent-get.json` | **Source-derived, not captured.** Legacy RPC field names and semantics from `docs/rpc-spec.md` and `libtransmission/transmission.h` (`tr_stat`) at tag `4.0.6`; values synthetic. |
| `v4.1.4-session-get.json` | **Source-derived, not captured.** Version fields per `docs/rpc-spec.md` at tag `4.1.4` (legacy protocol deprecated but still accepted). |
| `captured-v4.0.6-torrent-get.json`, `captured-v4.1.4-torrent-get.json` | **Captured** on 2026-11-15 from disposable daemons built from the 4.0.6 and 4.1.4 release tarballs (`scripts/it/run.sh --clients transmission --capture`), using the adapter's exact field list. Torrents are the harness's synthetic fixtures; no paths, trackers, or credentials are present. |

`magnetLink` and `downloadDir` appear although the adapter never requests
them, and one tracker error message embeds a fake passkey, so contract tests
prove such values are not retained. Observation is validated against
released 4.0.6 and 4.1.4; see `docs/compatibility.md`.
