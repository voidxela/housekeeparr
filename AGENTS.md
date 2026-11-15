# Project context

Housekeeparr is currently a specification-stage project. Read `docs/SPEC.md` before proposing or implementing behavior; it is the authoritative product specification. `README.md` is the concise project introduction. No application stack, build commands, or test runner has been selected yet.

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

As implementation tooling is introduced, document actual setup and verification commands here. Do not invent commands or claim checks passed without running them.
