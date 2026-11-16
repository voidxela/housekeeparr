// Package actiontest provides helpers for tests of action executors and
// their callers.
package actiontest

import (
	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Matching returns the live state that exactly matches p's plan: the
// transfer and association are unchanged, the content is monitored, and
// the manager is healthy. With failed set, the queue row is gone, as after
// the manager step.
func Matching(p action.Proposal, failed bool) action.Live {
	pl := p.Planned
	t := domain.Transfer{ClientID: pl.ClientID, NativeID: pl.NativeID, StateClass: pl.StateClass, VerifiedBytes: pl.Verified,
		WantedBytes: pl.Wanted, Private: pl.Private, Category: pl.Category}
	live := action.Live{Transfer: &t, ClientVersion: pl.ClientVersion, ManagerVersion: pl.ManagerVersion}
	if pl.ManagerID != "" && !failed {
		for _, key := range pl.Content {
			kind, id, _ := domain.ParseContentKey(key)
			q := domain.QueueItem{ManagerID: pl.ManagerID, QueueID: pl.QueueID, DownloadID: pl.DownloadID, ContentKind: kind}
			if kind == "movie" {
				q.MovieID = id
			} else {
				q.EpisodeID = id
			}
			live.Associations = append(live.Associations, q)
		}
	}
	for _, key := range pl.Content {
		live.Content = append(live.Content, action.ContentState{Key: key, Exists: true, Monitored: true, Detail: "monitored"})
	}
	live.Redownload = &action.Redownload{Failed: true, GrabSource: "Search"}
	return live
}
