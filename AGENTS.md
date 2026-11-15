# Project context

Housekeeparr is in early implementation: a read-only foundation (milestone 1) is partially built; no v1 workflow is complete. Read `docs/SPEC.md` before proposing or implementing behavior; it is the authoritative product specification. `docs/STATUS.md` is the implementation checklist (done / unverified / remaining) and must be updated with every milestone. `README.md` is the concise project introduction.

## Stack

Go (single static binary; `CGO_ENABLED=0` works), embedded SQLite through the pure-Go `modernc.org/sqlite` driver, TOML configuration via `github.com/BurntSushi/toml`. The web interface will use server-rendered `html/template` with embedded assets; no Node toolchain. Avoid adding dependencies without a clear need.

Layout: `internal/domain` (facts, signals, findings), `internal/adapter/*` (read-only adapters, no policy), `internal/policy` (pure decisions), `internal/findings` (lifecycle), `internal/store` (state, lock, migrations), `internal/engine` (polling, correlation, evaluation), `internal/fake` (deterministic fakes for tests and demo).

## Product priorities

- Keep Housekeeparr lightweight, highly reliable, and informative, with a simple, fast, thoughtfully polished web interface and complete headless operation.
- V1 supports Sonarr and Radarr and plans all four torrent clients: Transmission, qBittorrent, rTorrent, and Deluge. Document evidence and obtain an explicit scope decision before demoting a client to observation only.
- Plan post-v1 content auditing, declarative user-defined rules, controlled remediation, and additional managers including Lidarr. Do not implement that future scope implicitly in v1.

## Development expectations

- Preserve content. V1 must not delete, move, rename, quarantine, or re-encode download or library files. Observe-only is the default; automation permissions are explicit by workflow and scope.
- Preserve uncertainty. Missing or unsupported evidence is not zero, failure, successful search coverage, or proof that a release is unavailable.
- Use persistent evidence-backed findings and the shared decision/action pipeline. Web and CLI must use the same policy and execution services.
- Verify released-version API semantics with fixtures and disposable integration instances. Explicitly set consequential API flags; never rely on removal or redownload defaults.
- Journal intent before external writes, reconcile ambiguous outcomes, and never blindly retry a timed-out acquisition request. Local idempotency cannot guarantee exactly-once external effects.
- Keep concurrency, retries, polling, search budgets, history, and storage bounded. Avoid unnecessary services or a speculative plugin framework.
- Make explanations actionable: evidence, effective policy, limitations, intended effects, verified outcome, and next step. Distinguish search acceptance, search results, source coverage, grabs, and download success.
- Never commit credentials or sensitive tracker URLs. Sanitize logs, fixtures, diagnostics, and documentation examples.
- Test policy boundaries, adapter contracts, crash recovery, and real preservation workflows. Do not use live public torrents or real user data in tests.
- Keep the specification and README aligned with deliberate scope changes. Clearly distinguish approved requirements, proposed defaults, tested compatibility, and unimplemented plans.

## Setup and verification

Requires Go 1.26 or later (`go.mod`); development has used Go 1.27.4. If Go is not on `PATH`, install an official release from go.dev (verify its SHA-256) under `~/.local/go` and use `export PATH=$HOME/.local/go/bin:$PATH`.

```sh
go build ./...          # compile everything
go vet ./...            # static checks
gofmt -l .              # must print nothing
go test ./...           # unit, contract, store, and engine tests
```

`go test -race ./...` should be run where supported; it fails on hosts whose kernel uses a 39-bit virtual address space (ThreadSanitizer limitation).

There is no CLI entry point, integration harness, or container build yet. Do not invent commands or claim checks passed without running them. Adapter `TestedVersions` lists stay empty until a real disposable-instance run passes; fixtures in `testdata/` are synthetic and must say so.

## Migrations

Never edit a committed migration in `internal/store/migrations`; add a new numbered file and a test that opens a database created by the previous schema.
