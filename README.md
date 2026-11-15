# Housekeeparr

Housekeeparr keeps media download queues moving through evidence-based diagnosis, safe recovery, and controlled searches. It aims to be lightweight, highly reliable, and easy to understand, with a fast, polished web interface and complete headless operation.

The project is in the specification stage; no application is implemented yet.

V1 targets Sonarr and Radarr with Transmission, qBittorrent, rTorrent, and Deluge. Planned features include stalled-download recovery, incomplete season-pack handling, bounded missing-content searches, and alerts for incomplete search coverage. Automation starts in observe-only mode and preserves downloaded and library content.

Later releases will expand into actual content inspection, user-defined rules for inconsistencies and unwanted releases, and controlled remediation, with additional media managers including Lidarr.

See [the specification](docs/SPEC.md) for scope, behavior, safety requirements, and delivery phases. Implementation and installation instructions will follow as the application is built.
