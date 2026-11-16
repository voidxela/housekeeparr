# Radarr adapter fixtures

| File | Provenance |
| --- | --- |
| `v6.4.4-system_status.json` | **Source-derived, not captured.** Values synthetic. |
| `v6.4.4-queue.json` | **Source-derived, not captured.** `PagingResource<QueueResource>` per `src/Radarr.Api.V3/Queue/QueueResource.cs` at tag `v6.4.4.10685`; camelCase enum values. |
| `v6.4.4-health.json` | **Source-derived, not captured.** |

Observation is validated against a real Radarr 6.4.4.10685 instance; see
`docs/compatibility.md`. These files remain source-derived.

| File | Provenance |
| --- | --- |
| `v6.4.4-command.json` | **Captured** from the disposable Radarr 6.4.4.10685 instance of `scripts/it/run.sh` on 2026-11-16 (fictional stand-in content; a subset of `GET /api/v3/command`, no paths or credentials), right after a recovery: command 46 is the manager-native replacement `MoviesSearch` (trigger `unspecified`) for movie 5. |
