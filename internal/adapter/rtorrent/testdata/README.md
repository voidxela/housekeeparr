# rTorrent adapter fixtures

| File | Provenance |
| --- | --- |
| `v0.16.28-d.multicall2.xml`, `v0.16.28-client_version.xml` | **Source-derived, not captured.** Command set from `src/command_download.cc` at tags `v0.16.28` and `v0.9.8`; standard XML-RPC encoding; values synthetic. |
| `fault.xml` | **Synthetic** XML-RPC fault for an undefined method. |

One tracker message embeds a fake passkey so tests prove it is redacted.
Observation is validated against a real rTorrent 0.9.8 behind a lighttpd SCGI
gateway; see `docs/compatibility.md`. These files remain source-derived.
