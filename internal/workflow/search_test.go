package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/store"
)

func TestObserveSearchTracksTheContentsSearchSeparately(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	since := t0
	newOutcome := func(content string) store.RecoveryOutcome {
		return store.RecoveryOutcome{IntentID: 1, ManagerID: "sonarr", Content: []string{content}, StartedAt: t0.Add(20 * time.Second),
			Search: store.SearchOutcome{State: store.SearchUnobserved, Since: &since}}
	}
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	ptr := func(t time.Time) *time.Time { return &t }

	t.Run("matches only this content, queued after the request", func(t *testing.T) {
		o := newOutcome("episode:5")
		cmds := []arr.Command{
			{ID: 1, Name: "EpisodeSearch", Status: "completed", Queued: at(-10 * time.Minute), EpisodeIDs: []int64{5}}, // before the request
			{ID: 2, Name: "EpisodeSearch", Status: "queued", Queued: at(time.Second), EpisodeIDs: []int64{6}},          // other content
			{ID: 3, Name: "MoviesSearch", Status: "queued", Queued: at(time.Second), MovieIDs: []int64{5}},             // other kind
			{ID: 4, Name: "RefreshMonitoredDownloads", Status: "queued", Queued: at(time.Second)},
		}
		if !ObserveSearch(&o, cmds, at(30*time.Second)) || o.Search.State != store.SearchUnobserved || o.Search.CommandID != nil {
			t.Fatalf("matched something else: %+v", o.Search)
		}
		cmds = append(cmds, arr.Command{ID: 7, Name: "EpisodeSearch", Status: "queued", Queued: at(-30 * time.Second), EpisodeIDs: []int64{4, 5}})
		ObserveSearch(&o, cmds, at(31*time.Second))
		if o.Search.State != store.SearchAccepted || o.Search.CommandID == nil || *o.Search.CommandID != 7 {
			t.Fatalf("within the clock tolerance, want accepted #7: %+v", o.Search)
		}
	})

	t.Run("follows the command to completion, which is not a grab", func(t *testing.T) {
		o := newOutcome("movie:9")
		c := arr.Command{ID: 40, Name: "MoviesSearch", Status: "started", Queued: at(time.Second), MovieIDs: []int64{9}}
		ObserveSearch(&o, []arr.Command{c}, at(5*time.Second))
		if o.Search.State != store.SearchRunning {
			t.Fatalf("state %s", o.Search.State)
		}
		// A later search for the same content does not replace the
		// command already followed.
		c.Status, c.Result, c.Ended = "completed", "successful", ptr(at(9*time.Second))
		later := arr.Command{ID: 41, Name: "MoviesSearch", Status: "queued", Queued: at(8 * time.Second), MovieIDs: []int64{9}}
		ObserveSearch(&o, []arr.Command{later, c}, at(10*time.Second))
		if o.Search.State != store.SearchCompleted || *o.Search.CommandID != 40 || o.Search.EndedAt == nil {
			t.Fatalf("want completed #40: %+v", o.Search)
		}
		if !strings.Contains(o.Search.Note, "not a grab") {
			t.Fatalf("completion note must not imply a grab: %q", o.Search.Note)
		}
		if ObserveSearch(&o, nil, at(2*time.Hour)) {
			t.Fatal("a settled search changed")
		}
	})

	t.Run("failure is a command failure, with a redacted message", func(t *testing.T) {
		o := newOutcome("episode:5")
		c := arr.Command{ID: 8, Name: "EpisodeSearch", Status: "failed", Queued: at(time.Second), EpisodeIDs: []int64{5},
			Message: "Search failed: https://indexer.example/api?apikey=SECRETKEY123"}
		ObserveSearch(&o, []arr.Command{c}, at(time.Minute))
		if o.Search.State != store.SearchFailed || strings.Contains(o.Search.Note, "SECRETKEY123") || !strings.Contains(o.Search.Note, "command failure") {
			t.Fatalf("%+v", o.Search)
		}
	})

	t.Run("seen then dropped before its end is unverified", func(t *testing.T) {
		o := newOutcome("episode:5")
		ObserveSearch(&o, []arr.Command{{ID: 3, Name: "EpisodeSearch", Status: "queued", Queued: at(time.Second), EpisodeIDs: []int64{5}}}, at(time.Minute))
		ObserveSearch(&o, []arr.Command{}, at(20*time.Minute))
		if o.Search.State != store.SearchEndUnverified {
			t.Fatalf("%+v", o.Search)
		}
	})

	t.Run("never seen is not observed, not absence of a search", func(t *testing.T) {
		o := newOutcome("episode:5")
		ObserveSearch(&o, []arr.Command{}, at(30*time.Minute))
		if o.Search.State != store.SearchUnobserved {
			t.Fatalf("gave up early: %+v", o.Search)
		}
		ObserveSearch(&o, []arr.Command{}, at(SearchFollowWindow+time.Minute))
		if o.Search.State != store.SearchNotObserved || !strings.Contains(o.Search.Note, "not evidence that no search ran") {
			t.Fatalf("%+v", o.Search)
		}
	})

	t.Run("still running after the window is no longer followed", func(t *testing.T) {
		o := newOutcome("episode:5")
		ObserveSearch(&o, []arr.Command{{ID: 3, Name: "EpisodeSearch", Status: "started", Queued: at(time.Second), EpisodeIDs: []int64{5}}},
			at(SearchFollowWindow+time.Minute))
		if o.Search.State != store.SearchEndUnverified {
			t.Fatalf("%+v", o.Search)
		}
	})
}
