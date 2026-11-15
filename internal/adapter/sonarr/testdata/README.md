# Sonarr adapter fixtures

| File | Provenance |
| --- | --- |
| `v4.0.20-system_status.json` | **Source-derived, not captured.** Shape from `SystemController.cs` at tag `v4.0.20.3014`; values synthetic. |
| `v4.0.20-queue_page{1,2}.json` | **Source-derived, not captured.** `PagingResource<QueueResource>` from `QueueResource.cs` / `PagingResource.cs` at tag `v4.0.20.3014`; enum values use Sonarr's camelCase serialization. Download IDs follow Sonarr's qBittorrent client (uppercase hash). |
| `v4.0.20-health.json` | **Source-derived, not captured.** `HealthResource.cs` at tag `v4.0.20.3014`. |

`outputPath` values and an `apikey=` query string are included deliberately so
contract tests prove they are not retained. These fixtures are not captures
from a running instance; released-version compatibility has not been
established (see `docs/STATUS.md`).
