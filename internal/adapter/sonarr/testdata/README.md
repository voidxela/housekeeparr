# Sonarr adapter fixtures

| File | Provenance |
| --- | --- |
| `v4.0.20-system_status.json` | **Source-derived, not captured.** Shape from `SystemController.cs` at tag `v4.0.20.3014`; values synthetic. |
| `v4.0.20-queue_page{1,2}.json` | **Source-derived, not captured.** `PagingResource<QueueResource>` from `QueueResource.cs` / `PagingResource.cs` at tag `v4.0.20.3014`; enum values use Sonarr's camelCase serialization. Download IDs follow Sonarr's qBittorrent client (uppercase hash). |
| `v4.0.20-health.json` | **Source-derived, not captured.** `HealthResource.cs` at tag `v4.0.20.3014`. |
| `v4.0.20-wanted_missing.json` | **Synthetic, not captured.** `PagingResource<EpisodeResource>` shape for `GET /api/v3/wanted/missing` at tag `v4.0.20.3014`; values synthetic. |
| `v4.0.20-indexer.json` | **Synthetic, not captured.** `IndexerResource` array shape for `GET /api/v3/indexer`; includes a fake passkey URL in `fields` to assert credentials and URLs are never retained. |
| `v4.0.20-indexerstatus.json` | **Synthetic, not captured.** `IndexerStatusResource` array shape for `GET /api/v3/indexerstatus`; values synthetic. |

`outputPath` values and an `apikey=` query string are included deliberately so
contract tests prove they are not retained. These fixtures are not captures
from a running instance. Observation compatibility with Sonarr 4.0.20.3014 was
established by `scripts/it/run.sh` on 2026-11-15 (see `docs/compatibility.md`);
Sonarr responses are not yet captured as fixtures.

| File | Provenance |
| --- | --- |
| `v4.0.20-command.json` | **Captured** from the disposable Sonarr 4.0.20.3014 instance of `scripts/it/run.sh` on 2026-11-16 (fictional stand-in content; a subset of `GET /api/v3/command`, no paths or credentials), right after a recovery: command 45 is the manager-native replacement `EpisodeSearch` (trigger `unspecified`) for episode 5. |
