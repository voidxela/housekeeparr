# Feature expansion proposal — November 2026

Status: proposal for the user's decision. Nothing here is approved scope.
It extends [SPEC.md](../SPEC.md) only where a decision below is accepted.

## Where this comes from

Housekeeparr has run on a real shared seedbox (Ultra.cc host "eclipse")
since 2026-11-20. That host runs Sonarr 4.0.20, Radarr 6.4.4, Lidarr 3.1.6,
Prowlarr 2.7.0 and Transmission 4.0.5, with about 590 torrents, 1,330
monitored aired episodes and 413 missing. The earlier maintenance notes for
the same host (2026-11-15 review, 2026-11-17 Naruto investigation) record
the problems an operator fixed by hand before Housekeeparr existed there.
Each proposal below cites the evidence that motivates it and says what
Housekeeparr does today.

The trial itself is summarised in the last section.

## Summary

| # | Area | Evidence on eclipse | Today | Proposal | Priority |
| --- | --- | --- | --- | --- | --- |
| 1 | Release-source reliability | 1337x: 309 grabs, 24% imported; TorrentLeech: 353 grabs, 100% | Not visible | Per-indexer and per-group outcome scorecard with findings | High, v1.x |
| 2 | Coverage view | 917/1330 episodes; Beat Bobby Flay 205/545 | Missing list only | Per-series coverage with why each gap persists | High, v1.x |
| 3 | Lidarr | 6 dead music torrents, 7 queue items (1 import failed), 55 missing albums | Unmanaged; skews the correlated hold | Lidarr observation, then recovery | High, post-v1 first |
| 4 | Import pipeline stalls | 31 archived downloads waited for extraction; a complete torrent stopped by a tracker error blocked 220 queue rows | Generic import findings | Specific diagnoses for extraction, samples and completed-but-errored torrents | Medium |
| 5 | Series configuration that defeats search | Naruto searched S01 for every season; Slime S02 missing 16 episodes | Repeated missing searches, no cause | Numbering and series-type audit findings | Medium |
| 6 | Retained transfer lifecycle | ~120 dead torrents removed by hand on 11-17; 50 no-payload magnets now | Retained and counted, never cleaned | Opt-in registration cleanup after a verified replacement | Medium, controlled remediation |
| 7 | Client and tracker compatibility | Tracker answered "blacklisted client, update or change client" to Transmission 4.0.5 | Client-error finding | Tracker-rejects-client diagnosis with upgrade guidance | Medium |
| 8 | Version-line watch | Radarr 7.0.3 offered as an update | Health warning only | Warn before an update leaves the tested lines; pin new majors | Medium |
| 9 | Supporting services | Unpackerr, Prowlarr rate limits (HTTP 429), stale backups | Not observed | Read-only health for Prowlarr and the extraction stage | Low |
| 10 | Capacity | Account quota 3.7 TB, shared-disk `df` misleading | Retained logical size only | Quota-aware capacity findings | Low |

## 1. Release-source reliability

**Problem.** Over six days of Sonarr history, 309 grabs from 1337x produced
73 imports (24%); 147 failed and 89 were still dead in the queue. 353 grabs
from TorrentLeech produced 352 imports. One missing-episode search on
2026-11-18 grabbed 91 Beat Bobby Flay releases from 1337x and EZTV mirrors;
none completed in 46 hours, and half never obtained metadata. Recovery
fixes each grab after the fact, but the manager will keep choosing the
same kind of release.

**Proposal.**

- Join manager history (grab, import, failure) with Housekeeparr's own
  outcomes (stall diagnosis, recovery, replacement result) per indexer and
  per release group, over a rolling window.
- Open an informational finding when an indexer's import rate falls below
  a threshold with enough samples (proposed: under 50% over at least 30
  grabs in 14 days), naming the evidence and concrete manager-side
  options: raise its minimum seeders, lower its priority, or restrict it
  with tags. Housekeeparr never edits indexer settings itself.
- Show the scorecard on a "Sources" page and in `housekeeparr sources`.
- Feed the scorecard into recovery explanations ("this is the 40th dead
  grab from 1337x this week").

**Why first.** It turns repeated recoveries into one fix at the source and
needs only reads Housekeeparr already performs (history paging exists).

## 2. Coverage view

**Problem.** The operator's question is "how complete is my library and
why not". Today Housekeeparr lists missing items and search states, but not
per-series coverage or the reason each gap persists.

**Proposal.** A per-series and per-season coverage page: aired, present,
missing, and for each missing item one state from evidence: never
searched, searched with no result, grabbed and downloading, grabbed and
dead (recovered N times), or blocked by configuration (see 5). Track
coverage over time so a trial or a configuration change shows its effect.
This also gives the trial's "coverage maintained or improved" check a
first-class home instead of an external script.

## 3. Lidarr

**Problem.** Lidarr's music grabs share the client. Six dead music torrents
sit beside the TV grabs, one Lidarr import failed, and 55 albums are
missing. Housekeeparr sees those torrents as unmanaged: it cannot recover
them, and they count toward the correlated-stall hold.

**Proposal.** As SPEC §14.3 anticipates, add Lidarr through an
album-aware contract: queue and history reads first (observation and
correlation), then no-progress and metadata recovery once released
instances pass the same workflow tests. Track albums and tracks as their
own content identity rather than mapping them onto episodes.

## 4. Import pipeline stalls

**Problem.** Before Unpackerr was installed, 31 completed downloads waited
indefinitely because they contained RAR archives and Transmission's
completion hook pointed at a missing script. The Naruto season torrent was
complete but stopped by a tracker error, so Sonarr kept 220 episode rows
"downloading". Housekeeparr's import findings report the manager's status
messages but not these causes.

**Proposal.** Specific, evidence-backed diagnoses:

- **Needs extraction:** an import-pending download whose file list holds
  archives and no full-length video. Suggest an extraction tool, and never
  extract it.
- **Sample only:** the only video is shorter than the manager's sample
  threshold.
- **Complete but stopped by an error:** verified bytes equal wanted bytes,
  the client reports an error, and the manager still tracks the grab as
  downloading. Explain that the payload is complete and point at manual
  import.

## 5. Series configuration that defeats search

**Problem.** Naruto used scene numbering as a standard series. TheXEM
mapped all 220 episodes to scene season 1, so every season search queried
S01, and an S01 pack was associated with all 220 episodes. "That Time I Got
Reincarnated as a Slime" had the same configuration and 16 episodes
missing. Proactive searches would repeat, find nothing useful, and back off
without naming the cause.

**Proposal.** A read-only configuration audit, with findings for:

- every episode of several TVDB seasons sharing one scene season;
- an anime-categorised series configured as standard;
- a season pack grab associated with more episodes than the pack's season
  contains.

Each finding names the setting to review. Housekeeparr changes no series
settings.

## 6. Retained transfer lifecycle

**Problem.** Recovery keeps every failed grab's torrent and data (v1
preserves content). On eclipse that means about 50 no-payload magnets and
about 40 partial dead downloads join the retained set at once. On
2026-11-17 the operator removed about 120 such torrents by hand. The
retained ceiling stops recovery once it is reached, so without cleanup
recovery eventually stalls.

**Proposal** (controlled remediation, SPEC §14.3; opt-in per scope).

- **Registration-only cleanup:** remove the torrent from the client while
  keeping its data. It applies after the manager has imported a
  replacement for every affected content item, or immediately for a
  magnet that never had a payload.
- **Partial-data cleanup:** delete the data of a retained transfer that
  holds no imported file (no hardlink in the library), only with a
  separate explicit permission and a grace period. It is never used for
  private torrents still inside a seeding obligation.

Both are journaled and previewed like every other write. Preserved data
would remain the default.

## 7. Client and tracker compatibility

**Problem.** A tracker rejected Transmission 4.0.5 with "blacklisted
client, update or change client". The client stopped the complete
download with an error.

**Proposal.** Classify that tracker message family as "tracker rejects
this client version". Explain that the payload may still be complete, that
other trackers or DHT may still work, and that the fix is a client upgrade
(naming the tested lines). It would not be treated as a dead release.

## 8. Version-line watch

**Problem.** Radarr offers 7.0.3. Version support is now per API line
(SPEC §4.1). An installed Radarr 7 would be observed but not written to
until a Radarr 7 release passes the integration suite.

**Proposal.**

- Use the managers' own update notices: when an offered update is
  outside the lines with evidence, add the consequence to that health
  finding ("installing it will pause automatic recovery for Radarr until
  Housekeeparr supports Radarr 7").
- On the project side, pin each new major line in the integration
  harness when it is released.

## 9. Supporting services

**Problem.** The download pipeline depends on Prowlarr (indexer rate
limits, HTTP 429 seen in Lidarr logs), Unpackerr, and application backups
(Sonarr's only backup predated most of its library). None of these is
visible to Housekeeparr.

**Proposal.** Read-only Prowlarr health and indexer status. Optionally,
the age of each manager's latest backup as a finding. Unpackerr has no
API; cover it through the extraction diagnosis in 4.

## 10. Capacity

**Problem.** Retained bytes are tracked, but free capacity is not; on
shared hosts `df` reports the shared filesystem rather than the account
quota.

**Proposal.** An optional quota source (a command or file path) and
findings when retained data plus active downloads approach it.

## Production trial so far

In three hours of automatic operation, Housekeeparr dispatched 41 actions
(34 no-progress recoveries, 2 metadata recoveries, 5 missing searches), and
all of them were verified with no failures. Sonarr then grabbed 35
replacements: 32 from 1337x and 3 from EZTV. At the checkpoint, 29 of them
had no metadata, so coverage rose by only 2 files. Recovery is doing its
job, but the manager keeps choosing from sources that rarely work. That is
the strongest evidence for area 1.

The trial also produced the version-line support decision (SPEC §4.1), the
correlated-hold exemption for reached swarms, `[web] base_path`, and the
no-payload exclusion from the retained ceiling.

See `docs/STATUS.md` ("Production trial") and `docs/compatibility.md`
("Production trial runs") for the full record.
