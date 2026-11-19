# Housekeeparr continuation

Start from `review/remediation`, based on `origin/dev` at `3d08077`.
The latest Go source checkpoint is `0eae874`. Read [AGENTS.md](../AGENTS.md),
[SPEC.md](SPEC.md), the latest review section in [STATUS.md](STATUS.md), and
the continuation table in [compatibility.md](compatibility.md).

## Preserve the workflow contracts

- Preserve every download and library file. Send all four manager-removal
  flags explicitly with `removeFromClient=false`. Managers own replacement
  redownloads in v1.
- Journal each external workflow request and hold ambiguous outcomes for
  review. Require current positive evidence and keep unknown privacy,
  sizes, and availability intact.
- Apply the scoped `private_recovery` mode to private and unknown-privacy
  transfers. The default keeps the torrent running and accounts for it as
  retained. Metadata recovery accepts unknown privacy only while the
  client can positively establish that metadata is still absent.
- Keep exact-version write registrations tied to released-instance runs.
  Both Transmission lines use legacy RPC. Use a new migration and a
  previous-schema upgrade test for database changes.
- Use focused, subject-only conventional commits. Exercise released
  fixtures serially and record versions, commit, selection, outcomes, and
  mechanisms for failures in compatibility.md.

## Current behavior

Proactive searches use fresh, available, due missing content across
managers. They revalidate file, availability, and queue state, respect
scoped holds, and follow the receipt of the manager's actual command.
Search pacing and a verified clearance's targeted follow-ups are
durable. Pack follow-ups recover the original transfer permission from
retained evidence and use the partial-pack workflow's policy.

Tracker results with missing or malformed evidence stay unknown.
Clients report cap-limited tracker coverage in Attention with a
per-client setting. Repeated definite refusals hold the dependency
that rejected the write. An ambiguous outcome retains its journal
review hold.

The outbox expires undelivered events when a destination is unavailable
or removed. Schema 19 tracks per-target webhook attempts before sending
and preserves the hourly usage of verified deliveries from schema 18.
Web session and sign-in bounds, request timing, and outbox settings have
trial defaults in the example configuration.

## Validation and remaining work

The compatibility table tracks the exact test runs. GitHub-hosted
Ubuntu 24.04 ran the complete qBittorrent 5.3.0 suite with both managers
in the normal loopback-only namespace. On the managed workspace,
libtorrent's denied `AF_NETLINK` socket can prevent qBittorrent tracker
announces and pause Deluge's session. The hosted Linux workflow runs
qBittorrent and a Deluge selection in serial. Its Deluge step uses a
pinned Debian 13 container for the Python 3.13 packages and constructs
the harness's nested loopback-only namespace.

Continue default-length daemon-loop and production-trial verification
from SPEC. Prioritize broad pack histories, wanted queues beyond the
configured observation bound, and real reverse-proxy/TLS browser
checks. Use released-instance evidence for changes to assertions.
