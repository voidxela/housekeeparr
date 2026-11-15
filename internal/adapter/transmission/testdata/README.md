# Transmission adapter fixtures

| File | Provenance |
| --- | --- |
| `v4.0.6-session-get.json`, `v4.0.6-torrent-get.json` | **Source-derived, not captured.** Legacy RPC field names and semantics from `docs/rpc-spec.md` and `libtransmission/transmission.h` (`tr_stat`) at tag `4.0.6`; values synthetic. |
| `v4.1.4-session-get.json` | **Source-derived, not captured.** Version fields per `docs/rpc-spec.md` at tag `4.1.4` (legacy protocol deprecated but still accepted). |

`magnetLink` and `downloadDir` appear although the adapter never requests
them, and one tracker error message embeds a fake passkey, so contract tests
prove such values are not retained. Released-version compatibility has not
been established, and the integration harness does not start Transmission yet.
