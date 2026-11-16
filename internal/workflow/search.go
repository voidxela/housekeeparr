package workflow

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/redact"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Replacement-search observation bounds (implementation proposals):
// SearchClockTolerance allows the manager's clock to run slightly behind
// ours when comparing a command's queued time with the dispatch of the
// recovery's manager request; SearchFollowWindow is how long after that
// dispatch a search is looked for and followed.
const (
	SearchClockTolerance = time.Minute
	SearchFollowWindow   = time.Hour
)

// CommandReader is a manager whose command list can be read.
type CommandReader interface {
	Commands(ctx context.Context) ([]arr.Command, error)
}

// ObserveSearch updates o's replacement-search outcome from the manager's
// command list as read at now. The search is the earliest EpisodeSearch or
// MoviesSearch whose body names the outcome's content and that was queued
// no earlier than the recovery's manager request (less the clock
// tolerance); once found, that command is followed by ID. This is matching
// evidence, not proof of causation: a search for the same content queued by
// someone else in that interval would be indistinguishable. A completed
// search says nothing about whether anything was grabbed (the grab is a
// separate outcome). It reports whether anything changed.
func ObserveSearch(o *store.RecoveryOutcome, cmds []arr.Command, now time.Time) bool {
	s := &o.Search
	if store.SearchTerminal(s.State) {
		return false
	}
	before := *s
	since := o.StartedAt
	if s.Since != nil {
		since = *s.Since
	}
	var match *arr.Command
	for i := range cmds {
		c := &cmds[i]
		if s.CommandID != nil {
			if c.ID == *s.CommandID {
				match = c
				break
			}
			continue
		}
		if c.Queued.Before(since.Add(-SearchClockTolerance)) || !searchCovers(*c, o.Content) {
			continue
		}
		if match == nil || c.Queued.Before(match.Queued) || (c.Queued.Equal(match.Queued) && c.ID < match.ID) {
			match = c
		}
	}
	at := now
	s.CheckedAt = &at
	switch {
	case match == nil && s.CommandID != nil:
		s.State = store.SearchEndUnverified
		s.Note = fmt.Sprintf("the manager's %s (command %d) was accepted but is no longer in its command list, and its end was not observed; "+
			"the final search outcome is unverified (check the manager's history)", s.Name, *s.CommandID)
	case match == nil && now.Sub(since) > SearchFollowWindow:
		s.State = store.SearchNotObserved
		s.Note = "no search for this content was seen in the manager's command list within " + SearchFollowWindow.String() +
			"; managers drop finished commands after a few minutes, so this is not evidence that no search ran (check the manager's history)"
	case match == nil:
		s.State = store.SearchUnobserved
		s.Note = "no matching search seen in the manager's command list yet"
	default:
		id, queued := match.ID, match.Queued
		s.CommandID, s.Name, s.QueuedAt = &id, match.Name, &queued
		switch match.Status {
		case "queued":
			s.State, s.Note = store.SearchAccepted, fmt.Sprintf("the manager queued %s (command %d) for this content", match.Name, id)
		case "started":
			s.State, s.Note = store.SearchRunning, fmt.Sprintf("the manager is running %s (command %d) for this content", match.Name, id)
		case "completed":
			s.State, s.EndedAt = store.SearchCompleted, match.Ended
			s.Note = fmt.Sprintf("the manager's %s (command %d) for this content completed (result %q)", match.Name, id, match.Result)
			if match.Message != "" {
				s.Note += "; the manager reported: " + redact.Truncate(redact.String(match.Message), 200)
			}
			s.Note += ". A completed search is not a grab, and this does not show which sources were searched or why no release was accepted"
		case "failed", "aborted", "cancelled", "orphaned":
			s.State, s.EndedAt = store.SearchFailed, match.Ended
			s.Note = fmt.Sprintf("the manager's %s (command %d) for this content ended %s", match.Name, id, match.Status)
			if match.Message != "" {
				s.Note += ": " + redact.String(match.Message)
			}
			s.Note += "; this is a command failure, not evidence about the release or its sources"
		default:
			s.State = store.SearchAccepted
			s.Note = fmt.Sprintf("the manager has %s (command %d) for this content in an unrecognised status %q", match.Name, id, match.Status)
		}
		if !store.SearchTerminal(s.State) && now.Sub(since) > SearchFollowWindow {
			s.State = store.SearchEndUnverified
			s.Note = fmt.Sprintf("the manager's %s (command %d) was still %s after %s; it is no longer followed and its outcome is unverified",
				match.Name, id, match.Status, SearchFollowWindow)
		}
	}
	return s.State != before.State || s.Note != before.Note || !samePtr(s.CommandID, before.CommandID)
}

// searchCovers reports whether a search command names any of the content
// keys ("episode:<id>" for EpisodeSearch, "movie:<id>" for MoviesSearch).
func searchCovers(c arr.Command, content []string) bool {
	for _, k := range content {
		kind, id, ok := domain.ParseContentKey(k)
		if !ok {
			continue
		}
		if (kind == "episode" && c.Name == "EpisodeSearch" && slices.Contains(c.EpisodeIDs, id)) ||
			(kind == "movie" && c.Name == "MoviesSearch" && slices.Contains(c.MovieIDs, id)) {
			return true
		}
	}
	return false
}

func samePtr(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// ObserveOutcome reads the manager's command list until the replacement
// search for o is seen to finish, or for at most the settle bound (twice the
// step settle time), whichever comes first. It never writes externally.
// The poll deadline uses real time; the outcome is judged at now().
func (r *Recovery) ObserveOutcome(ctx context.Context, o *store.RecoveryOutcome, now func() time.Time) (bool, error) {
	m, ok := r.Managers[o.ManagerID]
	if !ok {
		return false, nil
	}
	cr, ok := m.(CommandReader)
	if !ok {
		return false, nil
	}
	settle, poll := r.Settle, r.Poll
	if settle <= 0 {
		settle = 15 * time.Second
	}
	if poll <= 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(2 * settle)
	changed := false
	for {
		cmds, err := cr.Commands(ctx)
		if err != nil {
			return changed, err
		}
		changed = ObserveSearch(o, cmds, now()) || changed
		if store.SearchTerminal(o.Search.State) || time.Now().After(deadline) {
			return changed, nil
		}
		select {
		case <-ctx.Done():
			return changed, ctx.Err()
		case <-time.After(poll):
		}
	}
}
