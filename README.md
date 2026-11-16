# Housekeeparr

Housekeeparr keeps media download queues moving through evidence-based diagnosis, safe recovery, and controlled searches. It aims to be lightweight, highly reliable, and easy to understand, with a fast, polished web interface and complete headless operation.

**Status: early implementation, observation only.** The read-only foundation runs: it observes media managers and download clients, diagnoses sustained lack of verified progress, metadata stalls, client errors, blocked imports, manager health problems, and failing connections, and explains each finding in a CLI and a local web interface. It observes Sonarr, Radarr, qBittorrent, Transmission, Deluge, and rTorrent and never changes any of them. Observation has been validated against real released instances of all six: qBittorrent 5.3.0 / 4.6.7, Transmission 4.0.6 / 4.1.4, rTorrent 0.9.8, Deluge 2.2.0, Sonarr 4.0.20, and Radarr 6.4.4 ([compatibility matrix](docs/compatibility.md)); every recovery, partial-pack, and search workflow is still to come. The policy and durable action foundation exists: per-workflow permissions, scoped rules, item overrides, and exclusions with provenance; a journal with locks, persistent budgets, and restore detection; and action previews shared by the CLI (`housekeeparr preview <id>`) and the web interface. No workflow has passed its write tests yet, so every preview explains why nothing is eligible and nothing is sent. See [the implementation status](docs/STATUS.md).

V1 targets Sonarr and Radarr with Transmission, qBittorrent, rTorrent, and Deluge. Planned features include stalled-download recovery, incomplete season-pack handling, bounded missing-content searches, and alerts for incomplete search coverage. Automation starts in observe-only mode and preserves downloaded and library content.

Later releases will expand into actual content inspection, user-defined rules for inconsistencies and unwanted releases, and controlled remediation, with additional media managers including Lidarr.

See [the specification](docs/SPEC.md) for scope, behavior, safety requirements, and delivery phases.

## Trying it

Requires Go 1.26 or later to build.

```sh
CGO_ENABLED=0 go build -o bin/housekeeparr ./cmd/housekeeparr

# Synthetic demo (no real services; data is clearly marked as demo):
bin/housekeeparr demo                     # http://127.0.0.1:8485/

# Real observation:
cp housekeeparr.example.toml housekeeparr.toml   # then edit URLs and secret references
bin/housekeeparr config validate
bin/housekeeparr check                    # live read-only probe of each connection
bin/housekeeparr run --once               # one observation cycle
bin/housekeeparr run                      # worker; serves http://127.0.0.1:8484/ if [web] enabled
bin/housekeeparr status                   # exit 1 when the worker is not healthy
bin/housekeeparr findings
bin/housekeeparr preview <finding-id>     # what would be done, every prerequisite, exact flags
bin/housekeeparr actions                  # journaled actions and holds
```

Credentials are never written in the configuration file: use `env:NAME` or `file:/absolute/path` references. The web interface binds to loopback only; use an SSH tunnel for remote viewing.

Diagnoses need sustained evidence: with the proposed defaults, a stall is established after a 30-minute new-download grace period plus 90 minutes of qualifying observations at a 10-minute polling interval.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```
