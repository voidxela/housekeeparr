> **Status: design input, not compatibility evidence.** A delegated reading of
> released Sonarr v4.0.20.3014 source (2026-11-17). Claims hold only where a
> real-instance run in `docs/compatibility.md` confirms them; one delegated
> claim about Sonarr (`/api/v3/indexerstatus`) was already disproved by the
> released instance.

# Sonarr Partial Season-Pack Handling Analysis (`v4.0.20.3014`)

Source analyzed: [Sonarr/Sonarr](https://github.com/Sonarr/Sonarr) at git tag **`v4.0.20.3014`**.

All line numbers below refer to the exact source files at tag `v4.0.20.3014`.

---

## Answers

### 1. Post-Import State, Status Messages, Queue Persistence, and `CompletedDownloadService` Trace

**Short answer:**
- After importing the present episodes (e.g. `E01`–`E02`) from a season pack where Sonarr expects more episodes (e.g. `E01`–`E03`), the tracked download ends in:
  - **`TrackedDownloadState.ImportBlocked`** (JSON: `"importBlocked"`)
  - **`TrackedDownloadStatus.Warning`** (JSON: `"warning"`)
- **Attached status message (exact text):**
  - Title: `"One or more episodes expected in this release were not imported or missing from the release"` with `Messages = []` (`List<string>`).
  - On subsequent refresh cycles (when source files remain in the torrent folder because torrents import via copy/hardlink while seeding), each already-imported file is rejected by `AlreadyImportedSpecification` and an additional `TrackedDownloadStatusMessage` is appended per rejected file with `Title = Path.GetFileName(path)` and `Messages = ["Episode file already imported at ..."]`.
  - *(Edge case if the pack only had **1** file total — e.g. `E01` in a 2-episode pack — on the **second** and later refresh cycles: `importResults.Count == 1` and `RejectedImportService.Process` returns `true` before `SetStateToImportBlocked`, leaving `State = TrackedDownloadState.ImportPending` and `Status = TrackedDownloadStatus.Warning` with `Title = trackedDownload.DownloadItem.Title` and `Messages = ["Episode file already imported at ..."]`.)*
- **Does it stay in `/api/v3/queue`?** **Yes.** Both `ImportBlocked` and `ImportPending` remain trackable and stay in `/api/v3/queue` until manually removed/ignored or removed from the download client.

#### Detailed Code Trace

1. **Expectation calculation (`TrackedDownloadService` -> `ParsingService`):**
   - `TrackedDownloadService.TrackDownload` (`src/NzbDrone.Core/Download/TrackedDownloads/TrackedDownloadService.cs:118-129`) parses the download title and calls `_parsingService.Map(...)`.
   - For a season pack (`parsedEpisodeInfo.FullSeason == true`), `ParsingService.GetEpisodes` (`src/NzbDrone.Core/Parser/ParsingService.cs:230-244`) fetches **all** episodes in that season from the DB via `_episodeService.GetEpisodesBySceneSeason` / `_episodeService.GetEpisodesBySeason` (regardless of `Monitored` state). Those episodes populate `trackedDownload.RemoteEpisode.Episodes` (e.g. `E01`, `E02`, `E03`).

2. **`CompletedDownloadService.Check` (`src/NzbDrone.Core/Download/CompletedDownloadService.cs:66-132`):**
   - Only runs when `DownloadItem.Status == DownloadItemStatus.Completed` and `State` is `TrackedDownloadState.Downloading` or `TrackedDownloadState.ImportBlocked` (lines `68-79`).
   - Validates path and matches the series, then sets:
     ```csharp
     // src/NzbDrone.Core/Download/CompletedDownloadService.cs:125
     trackedDownload.State = TrackedDownloadState.ImportPending;
     ```
   - `DownloadMonitoringService.Refresh` (`src/NzbDrone.Core/Download/TrackedDownloads/DownloadMonitoringService.cs:79`) then queues `ProcessMonitoredDownloadsCommand`, handled by `DownloadProcessingService.Execute` (`src/NzbDrone.Core/Download/DownloadProcessingService.cs:47-69`), which calls `_completedDownloadService.Import(trackedDownload)` for every `IsTrackable` item in `ImportPending`.

3. **`CompletedDownloadService.Import` (`src/NzbDrone.Core/Download/CompletedDownloadService.cs:134-204`):**
   - Note: `CompletedDownloadService` has `Check`, `Import`, `VerifyImport`, `SetStateToImportBlocked`, `SetImportItem`, and `ValidatePath` — there is no `ImportFailed` method in `v4.0.20.3014`.
   - Sets `trackedDownload.State = TrackedDownloadState.Importing;` (line `151`), then calls:
     ```csharp
     // src/NzbDrone.Core/Download/CompletedDownloadService.cs:153-162
     var outputPath = trackedDownload.ImportItem.OutputPath.FullPath;
     var importResults = _downloadedEpisodesImportService.ProcessPath(outputPath,
         ImportMode.Auto,
         trackedDownload.RemoteEpisode.Series,
         trackedDownload.ImportItem);

     if (VerifyImport(trackedDownload, importResults))
     {
         return;
     }
     ```
   - Inside `ImportApprovedEpisodes.Import` (`src/NzbDrone.Core/MediaFiles/EpisodeImport/ImportApprovedEpisodes.cs:139-152`), for a seeding torrent (`downloadClientItem.CanMoveFiles == false`), `ImportMode.Auto` resolves to `copyOnly = true`, so `E01` and `E02` are copied/hardlinked to the series folder and remain in `outputPath` (and `DownloadedEpisodesImportService.ProcessFolder`, `src/NzbDrone.Core/MediaFiles/DownloadedEpisodesImportService.cs:209-211`, only deletes the folder when `importMode == ImportMode.Move`).

4. **How "all episodes imported" is determined in `VerifyImport` (`src/NzbDrone.Core/Download/CompletedDownloadService.cs:206-277`) & `TrackedDownloadAlreadyImported`:**
   - **Check 1 (current import pass count):**
     ```csharp
     // src/NzbDrone.Core/Download/CompletedDownloadService.cs:208-211
     var allEpisodesImported = importResults.Where(c => c.Result == ImportResultType.Imported)
                                            .SelectMany(c => c.ImportDecision.LocalEpisode.Episodes)
                                            .Count() >= Math.Max(1,
                                   trackedDownload.RemoteEpisode.Episodes.Count);
     ```
     For `E01`–`E02` imported (`2`) vs `E01`–`E03` expected (`3`), `2 >= 3` is `false`.
   - **Check 2 (history-based fallback):**
     `VerifyImport` fetches `_historyService.FindByDownloadId(trackedDownload.DownloadItem.DownloadId).OrderByDescending(h => h.Date).ToList()` (lines `213-215`) and calls `_trackedDownloadAlreadyImported.IsImported(trackedDownload, historyItems)` (line `243`):
     ```csharp
     // src/NzbDrone.Core/Download/TrackedDownloads/TrackedDownloadAlreadyImported.cs:33-46
     var allEpisodesImportedInHistory = trackedDownload.RemoteEpisode.Episodes.All(e =>
     {
         var lastHistoryItem = historyItems.FirstOrDefault(h => h.EpisodeId == e.Id);

         if (lastHistoryItem == null)
         {
             _logger.Trace("No history for episode: S{0:00}E{1:00} [{2}]", e.SeasonNumber, e.EpisodeNumber, e.Id);
             return false;
         }

         _logger.Trace("Last event for episode: S{0:00}E{1:00} [{2}] is: {3}", e.SeasonNumber, e.EpisodeNumber, e.Id, lastHistoryItem.EventType);

         return lastHistoryItem.EventType == EpisodeHistoryEventType.DownloadFolderImported;
     });
     ```
     For `E03`, the most recent history item for that `downloadId` is `Grabbed` (or `null`), not `DownloadFolderImported`, so `IsImported` returns `false` and `VerifyImport` returns `false` (line `276`). *(Note: `_episodeService.GetEpisodes` and `_mediaFileService.GetFiles` at lines `266-267` only run when `allEpisodesImportedInHistory` is `true`, to gather files for `DownloadCompletedEvent`.)*

5. **Post-`VerifyImport == false` handling in `Import` (`src/NzbDrone.Core/Download/CompletedDownloadService.cs:164-203`, `279-294`):**
   ```csharp
   // src/NzbDrone.Core/Download/CompletedDownloadService.cs:164-203
   trackedDownload.State = TrackedDownloadState.ImportPending;

   if (importResults.Empty())
   {
       trackedDownload.Warn("No files found are eligible for import in {0}", outputPath);

       return;
   }

   if (importResults.Count == 1)
   {
       var firstResult = importResults.First();

       if (_rejectedImportService.Process(trackedDownload, firstResult))
       {
           return;
       }
   }

   var statusMessages = new List<TrackedDownloadStatusMessage>
                        {
                           new TrackedDownloadStatusMessage("One or more episodes expected in this release were not imported or missing from the release", new List<string>())
                        };

   if (importResults.Any(c => c.Result != ImportResultType.Imported))
   {
       statusMessages.AddRange(
           importResults
               .Where(v => v.Result != ImportResultType.Imported && v.ImportDecision.LocalEpisode != null)
               .OrderBy(v => v.ImportDecision.LocalEpisode.Path)
               .Select(v =>
                   new TrackedDownloadStatusMessage(Path.GetFileName(v.ImportDecision.LocalEpisode.Path),
                       v.Errors)));
   }

   if (statusMessages.Any())
   {
       trackedDownload.Warn(statusMessages.ToArray());
       SetStateToImportBlocked(trackedDownload);
   }
   ```
   - `trackedDownload.Warn(...)` (`src/NzbDrone.Core/Download/TrackedDownloads/TrackedDownload.cs:33-37`) sets `Status = TrackedDownloadStatus.Warning` and `StatusMessages = statusMessages`.
   - `SetStateToImportBlocked` (`CompletedDownloadService.cs:279-294`) sets `trackedDownload.State = TrackedDownloadState.ImportBlocked` and publishes `ManualInteractionRequiredEvent` once.
   - On the first pass with 2+ files (e.g. `E01`, `E02` both `Imported`), `RejectedImportService.Process` is not called (`Count != 1`), and `importResults.Any(c => c.Result != ImportResultType.Imported)` is `false`, so `StatusMessages` has only `"One or more episodes expected in this release were not imported or missing from the release"`.
   - On the next `RefreshMonitoredDownloadsCommand` cycle, `DownloadMonitoringService.ProcessClientItem` (`DownloadMonitoringService.cs:125-129`) invokes `_completedDownloadService.Check(trackedDownload)` because `State == ImportBlocked`, which sets `ImportPending` again, causing `Import` to re-run; `AlreadyImportedSpecification` (`src/NzbDrone.Core/MediaFiles/EpisodeImport/Specifications/AlreadyImportedSpecification.cs:41-68`) rejects `E01` and `E02` with `"Episode file already imported at {0}"`, so lines `188-197` append per-file rejection messages and `SetStateToImportBlocked` sets `ImportBlocked` again.
   - If `importResults.Count == 1` and that 1 file is `Rejected` on a re-run, `RejectedImportService.Process` (`src/NzbDrone.Core/Download/RejectedImportService.cs:25-59`) sets `Warn(...)` with the file's errors and returns `true`, returning early at line `179` with `State = TrackedDownloadState.ImportPending`.

6. **Queue retention:**
   - `DownloadMonitoringService.DownloadIsTrackable` (`src/NzbDrone.Core/Download/TrackedDownloads/DownloadMonitoringService.cs:139-156`) only returns `false` for `Imported`, `Failed`, `Ignored` (or `Completed` when Completed Download Handling is disabled).
   - `QueueService.Handle(TrackedDownloadRefreshedEvent)` (`src/NzbDrone.Core/Queue/QueueService.cs:97-106`) includes all `t.IsTrackable` downloads. Thus the item stays in `/api/v3/queue`.

---

### 2. History Events for Pack Grab and Partial Pack Import

JSON serialization uses `STJson.ApplySerializerSettings` (`src/NzbDrone.Common/Serializer/System.Text.Json/STJson.cs:24-38`, wired in `src/NzbDrone.Host/Startup.cs:89-91`), which applies:
- `JsonStringEnumConverter(JsonNamingPolicy.CamelCase, true)` — enum values are camelCase strings.
- `DictionaryKeyPolicy = JsonNamingPolicy.CamelCase` — `Data` dictionary keys are camelCased in JSON responses.

#### A. Grabbed Event (`EpisodeHistoryEventType.Grabbed` -> JSON `"grabbed"`)

`HistoryService.Handle(EpisodeGrabbedEvent)` (`src/NzbDrone.Core/History/HistoryService.cs:144-194`) loops over every episode in `message.Episode.Episodes` and writes **one `EpisodeHistory` row per expected episode** in the pack (e.g. 3 rows for `E01`, `E02`, `E03`), all sharing the same `DownloadId`:

```csharp
// src/NzbDrone.Core/History/HistoryService.cs:144-194
public void Handle(EpisodeGrabbedEvent message)
{
    foreach (var episode in message.Episode.Episodes)
    {
        var history = new EpisodeHistory
        {
            EventType = EpisodeHistoryEventType.Grabbed,
            Date = DateTime.UtcNow,
            Quality = message.Episode.ParsedEpisodeInfo.Quality,
            SourceTitle = message.Episode.Release.Title,
            SeriesId = episode.SeriesId,
            EpisodeId = episode.Id,
            DownloadId = message.DownloadId,
            Languages = message.Episode.Languages,
        };
        // ... Data populated (see keys below) ...
        _historyRepository.Insert(history);
    }
}
```

- **`Data` keys written on `"grabbed"`** (lines `160-190`, camelCased in JSON):
  `Indexer` (`indexer`), `NzbInfoUrl` (`nzbInfoUrl`), `ReleaseGroup` (`releaseGroup`), `Age` (`age`), `AgeHours` (`ageHours`), `AgeMinutes` (`ageMinutes`), `PublishedDate` (`publishedDate`), `DownloadClient` (`downloadClient`), `DownloadClientName` (`downloadClientName`), `Size` (`size`), `DownloadUrl` (`downloadUrl`), `Guid` (`guid`), `TvdbId` (`tvdbId`), `TvRageId` (`tvRageId`), `ImdbId` (`imdbId`), `Protocol` (`protocol`), `CustomFormatScore` (`customFormatScore`), `SeriesMatchType` (`seriesMatchType`), `ReleaseSource` (`releaseSource`), `IndexerFlags` (`indexerFlags`), `ReleaseType` (`releaseType`), plus optional `ReleaseHash` (`releaseHash`) and `TorrentInfoHash` (`torrentInfoHash`) when `message.Episode.Release is TorrentInfo`.

#### B. Imported Event (`EpisodeHistoryEventType.DownloadFolderImported` -> JSON `"downloadFolderImported"`)

`ImportApprovedEpisodes.Import` (`src/NzbDrone.Core/MediaFiles/EpisodeImport/ImportApprovedEpisodes.cs:409`) publishes `EpisodeImportedEvent` (`newDownload = true`) for each imported `EpisodeFile`. `HistoryService.Handle(EpisodeImportedEvent)` (`src/NzbDrone.Core/History/HistoryService.cs:196-237`) loops over `message.EpisodeInfo.Episodes` and inserts **one `EpisodeHistory` row per imported episode** (so 2 rows for `E01` and `E02`; none for missing `E03`; a multi-episode file produces one row per covered episode sharing the same `FileId` and `ImportedPath`):

```csharp
// src/NzbDrone.Core/History/HistoryService.cs:210-236
foreach (var episode in message.EpisodeInfo.Episodes)
{
    var history = new EpisodeHistory
    {
        EventType = EpisodeHistoryEventType.DownloadFolderImported,
        Date = DateTime.UtcNow,
        Quality = message.EpisodeInfo.Quality,
        SourceTitle = message.ImportedEpisode.SceneName ?? Path.GetFileNameWithoutExtension(message.EpisodeInfo.Path),
        SeriesId = message.ImportedEpisode.SeriesId,
        EpisodeId = episode.Id,
        DownloadId = downloadId,
        Languages = message.EpisodeInfo.Languages
    };

    history.Data.Add("FileId", message.ImportedEpisode.Id.ToString());
    history.Data.Add("DroppedPath", message.EpisodeInfo.Path);
    history.Data.Add("ImportedPath", Path.Combine(message.EpisodeInfo.Series.Path, message.ImportedEpisode.RelativePath));
    history.Data.Add("DownloadClient", message.DownloadClientInfo?.Type);
    history.Data.Add("DownloadClientName", message.DownloadClientInfo?.Name);
    history.Data.Add("ReleaseGroup", message.EpisodeInfo.ReleaseGroup);
    history.Data.Add("CustomFormatScore", message.EpisodeInfo.CustomFormatScore.ToString());
    history.Data.Add("Size", message.EpisodeInfo.Size.ToString());
    history.Data.Add("IndexerFlags", message.ImportedEpisode.IndexerFlags.ToString());
    history.Data.Add("ReleaseType", message.ImportedEpisode.ReleaseType.ToString());

    _historyRepository.Insert(history);
}
```

- **`Data` keys written on `"downloadFolderImported"`** (camelCased in `/api/v3/history` JSON):
  `fileId`, `droppedPath`, `importedPath`, `downloadClient`, `downloadClientName`, `releaseGroup`, `customFormatScore`, `size`, `indexerFlags`, `releaseType`.
- *(Internal note: `DownloadHistoryService` (`src/NzbDrone.Core/Download/History/DownloadHistoryService.cs:95-184`) also writes `DownloadGrabbed` and per-file `FileImported` rows to the internal `DownloadHistory` table, which is not exposed on `/api/v3/history`; `DownloadImported` is **not** written because `DownloadCompletedEvent` is never published.)*

---

### 3. Sample Detection for Files Without Parsable Runtime (e.g., 2–8 MiB Random-Byte `.mkv`)

**Short answer:**
Small random-byte `.mkv` files (2–8 MiB) are rejected as **`"Unable to determine if file is a sample"`** (`ImportRejectionReason.SampleIndeterminate`), **not** as `"Sample"` (`ImportRejectionReason.Sample`), assuming `ffprobe` fails to parse the random bytes and returns no `MediaInfo`. File size (MiB) is **never** checked anywhere in `DetectSample` or `DiskScanService`.

#### Exact Conditions and Thresholds

1. **No file-size filter on scan:**
   `DiskScanService.GetVideoFiles` (`src/NzbDrone.Core/MediaFiles/DiskScanService.cs:202-215`) only filters files by `MediaFileExtensions.Extensions` (which includes `.mkv`).
2. **MediaInfo parsing on random bytes:**
   `AggregationService.Augment` (`src/NzbDrone.Core/MediaFiles/EpisodeImport/Aggregation/AggregationService.cs:58-61`) calls `_videoFileInfoReader.GetMediaInfo(localEpisode.Path)`.
   `VideoFileInfoReader.GetMediaInfo` (`src/NzbDrone.Core/MediaFiles/MediaInfo/VideoFileInfoReader.cs:67-142`) runs `FFProbe.GetStreamJson(filename, ...)` inside a `try/catch (Exception ex)`:
   ```csharp
   // src/NzbDrone.Core/MediaFiles/MediaInfo/VideoFileInfoReader.cs:136-141
   catch (Exception ex)
   {
       _logger.Error(ex, "Unable to parse media info from file: {0}", filename);
   }

   return null;
   ```
   When `ffprobe` fails on random bytes, `localEpisode.MediaInfo` is `null`.
3. **`DetectSample.IsSample(LocalEpisode)` (`src/NzbDrone.Core/MediaFiles/EpisodeImport/DetectSample.cs:47-101`):**
   ```csharp
   // src/NzbDrone.Core/MediaFiles/EpisodeImport/DetectSample.cs:47-76
   public DetectSampleResult IsSample(LocalEpisode localEpisode)
   {
       var extensionResult = IsSample(localEpisode.Path, localEpisode.IsSpecial);

       if (extensionResult != DetectSampleResult.Indeterminate)
       {
           return extensionResult;
       }
       ...
       if (localEpisode.MediaInfo == null)
       {
           _logger.Error("Failed to get runtime from the file, make sure ffprobe is available");
           return DetectSampleResult.Indeterminate;
       }
       ...
       return IsSample(localEpisode.Path, localEpisode.MediaInfo.RunTime, runtime);
   }
   ```
   - `IsSample(path, isSpecial)` (`DetectSample.cs:78-101`) returns `DetectSampleResult.NotSample` **only** if `isSpecial == true` or the extension is `.flv` or `.strm` (case-insensitive). For `.mkv` (non-special), it returns `DetectSampleResult.Indeterminate`.
   - Then `localEpisode.MediaInfo == null` immediately returns **`DetectSampleResult.Indeterminate`**.
4. **`NotSampleSpecification.IsSatisfiedBy` (`src/NzbDrone.Core/MediaFiles/EpisodeImport/Specifications/NotSampleSpecification.cs:16-47`):**
   ```csharp
   // src/NzbDrone.Core/MediaFiles/EpisodeImport/Specifications/NotSampleSpecification.cs:30-39
   var sample = _detectSample.IsSample(localEpisode);

   if (sample == DetectSampleResult.Sample)
   {
       return ImportSpecDecision.Reject(ImportRejectionReason.Sample, "Sample");
   }
   else if (sample == DetectSampleResult.Indeterminate)
   {
       return ImportSpecDecision.Reject(ImportRejectionReason.SampleIndeterminate, "Unable to determine if file is a sample");
   }
   ```
   - Both `Sample` and `Indeterminate` **reject** the file, but with distinct reasons/messages: `Indeterminate` yields `ImportRejectionReason.SampleIndeterminate` with `"Unable to determine if file is a sample"`.
   - *(Note: `ImportDecisionMaker.GetNonSampleVideoFileCount` (`src/NzbDrone.Core/MediaFiles/EpisodeImport/ImportDecisionMaker.cs:229-239`) treats `DetectSampleResult.Indeterminate` as non-sample only for computing `localEpisode.OtherVideoFiles`.)*
5. **Exact runtime thresholds when `MediaInfo != null` (`DetectSample.cs:103-151`):**
   - If `fileRuntime.TotalMinutes.Equals(0)` (e.g. `TimeSpan.Zero` when `ffprobe` succeeds with 0 duration) -> `DetectSampleResult.Sample`.
   - Otherwise, `expectedRuntime` is the sum of `episode.Runtime > 0 ? episode.Runtime : localEpisode.Series.Runtime` across `localEpisode.Episodes` (defaulting to `45` if `0`), and `fileRuntime.TotalSeconds < GetMinimumAllowedRuntime(expectedRuntime)` returns `DetectSampleResult.Sample`:
     - `expectedRuntime <= 3` min -> `< 15` seconds
     - `expectedRuntime <= 10` min -> `< 90` seconds
     - `expectedRuntime <= 30` min -> `< 300` seconds (5 min)
     - `expectedRuntime > 30` min -> `< 600` seconds (10 min)

---

### 4. Effect of `DELETE /api/v3/queue/{id}?removeFromClient=false&blocklist=false&skipRedownload=true&changeCategory=false`

1. **Controller -> Service flow (`src/Sonarr.Api.V3/Queue/QueueController.cs:73-94`, `331-389`):**
   - `[RestDeleteById]` (`src/Sonarr.Http/REST/Attributes/RestDeleteByIdAttribute.cs:7-12`) routes `DELETE /api/v3/queue/{id:int}` to `QueueController.RemoveAction`.
   - After confirming `id` is not a pending release, `GetTrackedDownload(id)` resolves the queue item (`_queueService.Find(id)`) and its `TrackedDownload` (`_trackedDownloadService.Find(queueItem.DownloadId)`).
   - `QueueController.Remove` (`QueueController.cs:331-370`) executes:
     ```csharp
     // src/Sonarr.Api.V3/Queue/QueueController.cs:331-370
     private TrackedDownload Remove(TrackedDownload trackedDownload, bool removeFromClient, bool blocklist, bool skipRedownload, bool changeCategory)
     {
         if (removeFromClient) { ... downloadClient.RemoveItem(trackedDownload.DownloadItem, true); }
         else if (changeCategory) { ... downloadClient.MarkItemAsImported(trackedDownload.DownloadItem); }

         if (blocklist)
         {
             _failedDownloadService.MarkAsFailed(trackedDownload, skipRedownload);
         }

         if (!removeFromClient && !blocklist && !changeCategory)
         {
             if (!_ignoredDownloadService.IgnoreDownload(trackedDownload))
             {
                 return null;
             }
         }

         return trackedDownload;
     }
     ```
   - With `removeFromClient=false`, `blocklist=false`, `changeCategory=false`:
     - **`skipRedownload=true` has no effect** (it is only passed to `_failedDownloadService.MarkAsFailed` inside `if (blocklist)`).
     - Calls **`_ignoredDownloadService.IgnoreDownload(trackedDownload)`** (`QueueController.cs:363`).
   - Then `QueueController.RemoveAction` (`QueueController.cs:93`) calls `_trackedDownloadService.StopTracking(trackedDownload.DownloadItem.DownloadId)`, which removes the `downloadId` from `_cache`, fires `TrackedDownloadsRemovedEvent`, and causes `DownloadMonitoringService` -> `QueueService` to immediately drop all queue rows for that `downloadId`.

2. **Does it "Ignore" the download?**
   - **Yes.** `IgnoredDownloadService.IgnoreDownload` (`src/NzbDrone.Core/Download/IgnoredDownloadService.cs:25-52`) checks `trackedDownload.RemoteEpisode.Series != null` and publishes `DownloadIgnoredEvent` with `SeriesId`, `EpisodeIds = episodes.Select(e => e.Id).ToList()`, `DownloadId`, `DownloadClientInfo`, and `Message = "Manually ignored"`.

3. **Does it write a history event `"downloadIgnored"`?**
   - **Yes.**
     - `HistoryService.Handle(DownloadIgnoredEvent)` (`src/NzbDrone.Core/History/HistoryService.cs:334-363`) inserts one `EpisodeHistory` row **per episode** in `message.EpisodeIds` (e.g. all of `E01`, `E02`, `E03`) with `EventType = EpisodeHistoryEventType.DownloadIgnored` (JSON: `"downloadIgnored"`), `DownloadId`, `Data["Message"] = "Manually ignored"`, `Data["DownloadClient"]`, `Data["DownloadClientName"]`, etc.
     - `DownloadHistoryService.Handle(DownloadIgnoredEvent)` (`src/NzbDrone.Core/Download/History/DownloadHistoryService.cs:211-228`) inserts one `DownloadHistory` row with `EventType = DownloadHistoryEventType.DownloadIgnored` and `DownloadId`.

4. **Is it durable across `RefreshMonitoredDownloadsCommand`?**
   - **Yes** (provided `trackedDownload.RemoteEpisode.Series != null` when ignored, which is true for a matched pack). Because `StopTracking` removed the item from `_cache`, the next `RefreshMonitoredDownloadsCommand` re-reads `DownloadHistory` from the DB via `_downloadHistoryService.GetLatestDownloadHistoryItem(downloadId)` (`TrackedDownloadService.cs:131-137`), maps `DownloadIgnored` -> `TrackedDownloadState.Ignored` (`TrackedDownloadService.cs:266-267`), and `DownloadMonitoringService.DownloadIsTrackable` (`DownloadMonitoringService.cs:142-147`) excludes `Ignored` downloads from the queue and from `CompletedDownloadService.Check`.

5. **Does anything touch the torrent or files?**
   - **No.** Because `removeFromClient=false` and `changeCategory=false`, neither `downloadClient.RemoveItem` nor `downloadClient.MarkItemAsImported` is called. No torrent client API calls and no filesystem operations occur.

---

### 5. Re-Tracking an Ignored Completed Download After Restart or Refresh

**Short answer:** **No.** Sonarr does not re-track an ignored completed download after `RefreshMonitoredDownloadsCommand` or after a full Sonarr restart, as long as its `DownloadId` remains the same and no newer `DownloadGrabbed` event exists for that `DownloadId`.

#### Mechanism

- **On `RefreshMonitoredDownloadsCommand` (same process):**
  - Immediately after `DELETE /api/v3/queue/{id}`, `StopTracking` evicted the entry from `TrackedDownloadService._cache` (`TrackedDownloadService.cs:67-73`).
  - On the next refresh, `TrackDownload` (`TrackedDownloadService.cs:131-137`) finds `existingItem == null`, queries `_downloadHistoryService.GetLatestDownloadHistoryItem(downloadItem.DownloadId)` (`DownloadHistoryService.cs:58-87`), gets `DownloadHistoryEventType.DownloadIgnored`, and sets `trackedDownload.State = TrackedDownloadState.Ignored` via `GetStateFromHistory` (`TrackedDownloadService.cs:252-271`).
  - On subsequent refreshes while that `TrackedDownload` remains in `_cache`, `Find(downloadItem.DownloadId)` returns `existingItem` with `State == TrackedDownloadState.Ignored != TrackedDownloadState.Downloading` (`TrackedDownloadService.cs:93-101`), preserving `State = TrackedDownloadState.Ignored` (note: line `98` temporarily sets `IsTrackable = true`, which `DownloadMonitoringService.Refresh` immediately clears back to `false` via `Where(DownloadIsTrackable)` + `UpdateTrackable` at `DownloadMonitoringService.cs:74,77`).
- **After Sonarr restart (`_cache` empty):**
  - `TrackDownload` runs the same DB lookup against the persisted `DownloadHistory` table, sees `DownloadIgnored` as the latest expected event (`DownloadHistoryService.cs:58-87`), and initializes `trackedDownload.State = TrackedDownloadState.Ignored`.
- **Why it is never processed or shown in queue:**
  - `DownloadMonitoringService.ProcessClientItem` (`DownloadMonitoringService.cs:125-129`) only calls `_failedDownloadService.Check` and `_completedDownloadService.Check` when `State` is `Downloading` or `ImportBlocked`.
  - `DownloadMonitoringService.DownloadIsTrackable` (`DownloadMonitoringService.cs:139-147`) returns `false` when `State == TrackedDownloadState.Ignored`.
  - `DownloadProcessingService.Execute` (`DownloadProcessingService.cs:50-69`) only processes `IsTrackable` items in `ImportPending` or `FailedPending`.
  - `QueueService.Handle(TrackedDownloadRefreshedEvent)` (`QueueService.cs:97-106`) only maps `trackedDownloads` with `IsTrackable == true`.
- **Exceptions / edge cases:**
  - If the download had `RemoteEpisode.Series == null` (unknown series) when `DELETE` was called, `IgnoredDownloadService.IgnoreDownload` (`IgnoredDownloadService.cs:29-33`) returns `false` without writing `DownloadIgnored` to `DownloadHistory`, so it *would* be re-tracked on the next refresh/restart.
  - If the exact same torrent (`downloadId`) is grabbed again by Sonarr later, `DownloadHistoryService.Handle(EpisodeGrabbedEvent)` inserts a newer `DownloadGrabbed` row; after a restart (when `_cache` is empty), `GetLatestDownloadHistoryItem` would return `DownloadGrabbed` -> `TrackedDownloadState.Downloading`. (While the old `State = Ignored` entry is still in `_cache`, `TrackedDownloadService.cs:93` `existingItem.State != TrackedDownloadState.Downloading` would keep `State = Ignored`.)

---

### 6. `/api/v3/queue` and `/api/v3/history` Fields for Identifying Covered vs. Imported Episodes

#### A. `/api/v3/queue` Fields for a Pack

- **One row per episode:** `QueueService.MapQueue` (`src/NzbDrone.Core/Queue/QueueService.cs:45-58`) yields **one `Queue` object per episode** in `trackedDownload.RemoteEpisode.Episodes` (or one row with `Episode = null` if `Episodes` is null/empty).
- **Per-row ID (`QueueService.cs:87`):**
  `HashConverter.GetHashInt31($"trackedDownload-{trackedDownload.DownloadClient}-{trackedDownload.DownloadItem.DownloadId}-ep{episode?.Id ?? 0}")`
  Each episode row has its own `id`, even though they share the same `downloadId`. Deleting *any* one of those row `id`s via `DELETE /api/v3/queue/{id}` stops tracking the entire `downloadId` and removes all rows for the pack.
- **Fields on `QueueResource` (`src/Sonarr.Api.V3/Queue/QueueResource.cs:16-54`):**
  There is **no** `episodeIds` array field on a `QueueResource` item; instead each row exposes:
  - `id` (`int`)
  - `seriesId` (`int?`)
  - `episodeId` (`int?`) — the single episode covered by this row
  - `seasonNumber` (`int?`)
  - `episode` (`EpisodeResource`, populated only when `?includeEpisode=true`)
  - `episodeHasFile` (`bool`)
  - `downloadId` (`string`) — common across all rows of the pack
  - `trackedDownloadStatus` (`"ok"` | `"warning"` | `"error"`), `trackedDownloadState` (e.g. `"importBlocked"`), `statusMessages` (`[{ "title": "...", "messages": [...] }]`).
  - *(Note: `GET /api/v3/queue/details` (`src/Sonarr.Api.V3/Queue/QueueDetailsController.cs:42-59`) returns the same `QueueResource` shape unfiltered by paging, optionally filtered by `seriesId` or `episodeIds`.)*

#### B. `/api/v3/history` Fields to Confirm Imported Episodes for a `downloadId`

- **Endpoint & filtering (`src/Sonarr.Api.V3/History/HistoryController.cs:65-98`):**
  `GET /api/v3/history` accepts:
  - `downloadId` (`string`) — exact filter `h => h.DownloadId == downloadId` (line `89`)
  - `eventType` (`int[]` via `[FromQuery(Name = "eventType")] int[] eventTypes`, line `65`) — e.g. `eventType=3` for `DownloadFolderImported`, `eventType=1` for `Grabbed`, `eventType=7` for `DownloadIgnored` (`src/NzbDrone.Core/History/EpisodeHistory.cs:34-44`)
  - `includeEpisode` (`bool`), `includeSeries` (`bool`), `page`, `pageSize`.
- **Fields on `HistoryResource` (`src/Sonarr.Api.V3/History/HistoryResource.cs:14-33`):**
  - `episodeId` (`int`) — confirms each specific episode ID that has an event
  - `seriesId` (`int`)
  - `downloadId` (`string`)
  - `eventType` (`EpisodeHistoryEventType` JSON string: `"downloadFolderImported"`, `"grabbed"`, `"downloadIgnored"`, etc.)
  - `date` (`DateTime`), `sourceTitle` (`string`), `quality`, `languages`
  - `episode` (`EpisodeResource`, if `includeEpisode=true`), `series` (`SeriesResource`, if `includeSeries=true`)
  - `data` (`Dictionary<string, string>`, camelCase keys): for `"downloadFolderImported"`, includes `importedPath`, `droppedPath`, `fileId`, `size`, `downloadClient`, `downloadClientName`, `releaseGroup`, `customFormatScore`, `indexerFlags`, `releaseType`.
- To confirm which episodes were imported from a given `downloadId`, a client can query `GET /api/v3/history?downloadId=<downloadId>&eventType=3&pageSize=...` and collect `episodeId` from all records where `eventType == "downloadFolderImported"`. Any expected `episodeId` from `/api/v3/queue` (or from `eventType=1` `"grabbed"` rows for that `downloadId`) not present in the `"downloadFolderImported"` set is still missing.

---

## Uncertain / Needs Real-Instance Verification

1. **`ffprobe` exit behavior on random-byte `.mkv` files:** Static inspection shows `VideoFileInfoReader.GetMediaInfo` (`src/NzbDrone.Core/MediaFiles/MediaInfo/VideoFileInfoReader.cs:67-142`) catches exceptions from `FFProbe.GetStreamJson` and returns `null`, which produces `DetectSampleResult.Indeterminate` (`"Unable to determine if file is a sample"`). Whether Sonarr's bundled `ffprobe` always throws in `FFMpegCore` on 2–8 MiB random-byte `.mkv` files (rather than ever returning valid JSON with `Duration == 0`, which would yield `DetectSampleResult.Sample`) should be verified on a live instance.
2. **Download client `CanMoveFiles` / `OutputPath` behavior:** Whether the specific torrent client setup (e.g. qBittorrent/Transmission while seeding vs. paused/finished) reports `CanMoveFiles = false` (causing `ImportMode.Copy`/hardlink and leaving files in the torrent directory, triggering per-file `AlreadyImportedSpecification` rejections on subsequent refreshes) vs `CanMoveFiles = true` (`ImportMode.Move`, where `ShouldDeleteFolder` may delete the download folder if no other non-sample video files remain) depends on client state and settings.
3. **`/api/v3/history` and `/api/v3/queue` default pagination:** `HistoryController.GetHistory` and `QueueController.GetQueue` wrap results in `PagingResource`, so a large season pack (e.g. >10 episodes) will be truncated unless `pageSize` is explicitly set; exact default `pageSize` and response payload should be verified against a live instance.
4. **Re-grabbing the exact same torrent (`downloadId`) without restarting Sonarr:** By inspection of `TrackedDownloadService.cs:93`, an in-memory `_cache` entry with `State == Ignored` skips re-initializing `State` from `DownloadHistory`, whereas a restart reloads from DB (`DownloadGrabbed` newer than `DownloadIgnored`). Cache eviction/TTL of `ICached<TrackedDownload>` was not verified against runtime behavior.
