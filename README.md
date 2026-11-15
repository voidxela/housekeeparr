# Housekeeparr

Housekeeparr keeps media download queues moving through evidence-based diagnosis, safe recovery, and controlled searches. It aims to be lightweight, highly reliable, and easy to understand, with a fast, polished web interface and complete headless operation.

**Status: early implementation, not usable yet.** A read-only foundation is being built: configuration, the state store, Sonarr and qBittorrent read adapters, the diagnosis policy, persistent findings, and the observation engine exist and are tested against synthetic fixtures. There is no command-line entry point or web interface yet, and no compatibility with any real Sonarr or qBittorrent version has been verified. See [the implementation status](docs/STATUS.md).

V1 targets Sonarr and Radarr with Transmission, qBittorrent, rTorrent, and Deluge. Planned features include stalled-download recovery, incomplete season-pack handling, bounded missing-content searches, and alerts for incomplete search coverage. Automation starts in observe-only mode and preserves downloaded and library content.

Later releases will expand into actual content inspection, user-defined rules for inconsistencies and unwanted releases, and controlled remediation, with additional media managers including Lidarr.

See [the specification](docs/SPEC.md) for scope, behavior, safety requirements, and delivery phases.

## Development

Requires Go 1.26 or later.

```sh
go build ./...
go vet ./...
go test ./...
```

Installation instructions will follow once there is something to install.
