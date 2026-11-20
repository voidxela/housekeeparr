package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// replayTrial writes, as Sonarr's history, the production trial's pattern
// (sanitized): 309 grabs from one indexer of which 73 imported, and 353
// from another of which 352 imported, spread over 13 days so the newest
// grabs are still pending. Imports follow their grab by two hours; the
// imported grabs of the first indexer are its oldest.
func replayTrial(w *world) {
	now := w.clock.Now()
	span := 13 * 24 * time.Hour
	var recs []arr.HistoryRecord
	grab := func(idx string, i, n, imported int) {
		at := now.Add(-span).Add(time.Duration(i) * span / time.Duration(n))
		dl := fmt.Sprintf("%s-%06d", strings.ToUpper(idx), i)
		ep := int64(1000*len(idx) + i)
		recs = append(recs, arr.HistoryRecord{EventType: "grabbed", Date: at, DownloadID: dl, EpisodeID: ep, Indexer: idx, ReleaseSource: "Search",
			SourceTitle: "Show.S01E" + fmt.Sprint(i)})
		if i < imported {
			recs = append(recs, arr.HistoryRecord{EventType: "downloadFolderImported", Date: at.Add(2 * time.Hour), DownloadID: dl, EpisodeID: ep})
		}
	}
	for i := 0; i < 309; i++ {
		grab("PublicA", i, 309, 73)
	}
	for i := 0; i < 353; i++ {
		grab("PublicBB", i, 353, 352)
	}
	w.mgr.AddHistory(recs...)
}

func source(sc *app.Scorecard, idx string) app.SourceStats {
	for _, s := range sc.Sources {
		if s.Indexer == idx {
			return s
		}
	}
	return app.SourceStats{}
}

// The scorecard counts acquisitions from the manager's own history: raw
// counts with outcomes (pending visible), mature grabs and their import
// fraction, and an alert only for a source with enough mature grabs well
// below the threshold, kept until it recovers past the clearing level.
func TestScorecardReplaysTheTrialPattern(t *testing.T) {
	w := newWorld(t, "observe", 20)
	replayTrial(w)
	w.observe(10 * time.Minute)
	sc, err := w.svc.Scorecard(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	a, b := source(sc, "PublicA"), source(sc, "PublicBB")
	if a.Grabs != 309 || a.Outcomes[app.OutcomeImported] != 73 || b.Grabs != 353 || b.Outcomes[app.OutcomeImported]+b.Outcomes[app.OutcomePending] != 353 {
		t.Fatalf("raw counts: A %+v B %+v", a, b)
	}
	if a.Outcomes[app.OutcomePending] == 0 || a.Outcomes[app.OutcomeUnknown] == 0 {
		t.Fatalf("pending and unknown not distinguished: %+v", a.Outcomes)
	}
	if a.ImportFraction == nil || *a.ImportFraction >= 0.5 || !a.Alert || a.Quality != "" {
		t.Fatalf("source A: %+v", a)
	}
	if b.ImportFraction == nil || *b.ImportFraction < 0.99 || b.Alert {
		t.Fatalf("source B: %+v", b)
	}
	if len(sc.Coverage) == 0 || !sc.Coverage[0].CoversWindow {
		t.Fatalf("coverage %+v", sc.Coverage)
	}
	if err := w.svc.EvaluateSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: "source.low_import_rate", States: []domain.FindingState{domain.FindingOpen}})
	if len(fs) != 1 || !strings.Contains(fs[0].Title, "PublicA") {
		t.Fatalf("findings %+v", fs)
	}
	// Late imports raise A's rate to 55%: still below the clearing level,
	// so the alert stays (hysteresis); at 65% it resolves.
	lateImports := func(upTo int) {
		var recs []arr.HistoryRecord
		evs, _ := w.st.HistoryForGrabsSince(context.Background(), time.Time{})
		n := 0
		for _, e := range evs {
			if e.Indexer != "PublicA" || e.EventType != "grabbed" || w.clock.Now().Sub(e.At) < app.ScorecardMaturity {
				continue
			}
			n++
			if n > 73 && n <= upTo {
				recs = append(recs, arr.HistoryRecord{EventType: "downloadFolderImported", Date: w.clock.Now(), DownloadID: e.DownloadID,
					EpisodeID: mustEpisode(e.ContentKey)})
			}
		}
		w.mgr.AddHistory(recs...)
		w.observe(40 * time.Minute)
	}
	mature := a.Mature
	lateImports(mature * 55 / 100)
	if err := w.svc.EvaluateSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	sc, _ = w.svc.Scorecard(context.Background(), false)
	if a := source(sc, "PublicA"); *a.ImportFraction < 0.5 || !a.Alert {
		t.Fatalf("at %.2f the alert should hold: %+v", *a.ImportFraction, a)
	}
	lateImports(mature * 65 / 100)
	if err := w.svc.EvaluateSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: "source.low_import_rate", States: []domain.FindingState{domain.FindingOpen}}); len(fs) != 0 {
		t.Fatalf("alert did not resolve at 65%%: %+v", fs)
	}
}

func mustEpisode(key string) int64 {
	var id int64
	fmt.Sscanf(key, "episode:%d", &id)
	return id
}

// A pack's per-episode grab records are one acquisition; importing some
// of its episodes is a partial outcome. The same download ID on another
// manager is another acquisition. Too few mature grabs, or history that
// does not cover the window, judge no rate.
func TestScorecardDeduplicatesPacksAndKeepsManagersApart(t *testing.T) {
	w := newWorld(t, "observe", 20)
	now := w.clock.Now()
	at := now.Add(-5 * 24 * time.Hour)
	const pack = "PACKAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var recs []arr.HistoryRecord
	for ep := int64(1); ep <= 3; ep++ {
		recs = append(recs, arr.HistoryRecord{EventType: "grabbed", Date: at, DownloadID: pack, EpisodeID: ep, Indexer: "Packs"})
	}
	for ep := int64(1); ep <= 2; ep++ {
		recs = append(recs, arr.HistoryRecord{EventType: "downloadFolderImported", Date: at.Add(time.Hour), DownloadID: pack, EpisodeID: ep})
	}
	w.mgr.AddHistory(recs...)
	w.radarr.AddHistory(arr.HistoryRecord{EventType: "grabbed", Date: at, DownloadID: pack, MovieID: 9, Indexer: "Packs"},
		arr.HistoryRecord{EventType: "downloadFolderImported", Date: at.Add(time.Hour), DownloadID: pack, MovieID: 9})
	w.observe(10 * time.Minute)
	sc, err := w.svc.Scorecard(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	s := source(sc, "Packs")
	if s.Grabs != 2 || s.Outcomes[app.OutcomePartial] != 1 || s.Outcomes[app.OutcomeImported] != 1 || len(s.Managers) != 2 {
		t.Fatalf("pack and cross-manager accounting: %+v", s)
	}
	if s.Alert || !strings.Contains(s.Quality, "mature grabs") {
		t.Fatalf("a two-grab sample was judged: %+v", s)
	}
	var partial *app.Acquisition
	for i := range sc.Acquisitions {
		if sc.Acquisitions[i].Outcome == app.OutcomePartial {
			partial = &sc.Acquisitions[i]
		}
	}
	if partial == nil || len(partial.Content) != 3 || len(partial.Imported) != 2 {
		t.Fatalf("partial pack %+v", partial)
	}
}

// A recovery's replacement is followed through to import by its download
// identity; until the manager's history shows the import it is pending,
// then unknown once mature.
func TestScorecardFollowsRecoveryReplacementsToImport(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.stalled(hashA, 1, 1)
	w.run(4 * time.Hour)
	// The manager grabs a replacement, hashB.
	w.stalled(hashB, 2, 1)
	w.client.Update(hashB, func(tr *domain.Transfer) { tr.VerifiedBytes = domain.KnownValue(3 * gib) })
	w.run(time.Hour)
	sc, err := w.svc.Scorecard(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if r := sc.Recovery; r.Recoveries != 1 || r.Grabbed != 1 || r.Outcomes[app.OutcomePending] != 1 {
		t.Fatalf("before the import: %+v", r)
	}
	w.mgr.AddHistory(arr.HistoryRecord{EventType: "grabbed", Date: w.clock.Now().Add(-time.Hour), DownloadID: strings.ToUpper(hashB), EpisodeID: 101, Indexer: "PublicA"},
		arr.HistoryRecord{EventType: "downloadFolderImported", Date: w.clock.Now(), DownloadID: strings.ToUpper(hashB), EpisodeID: 101})
	w.observe(40 * time.Minute)
	sc, _ = w.svc.Scorecard(context.Background(), false)
	if r := sc.Recovery; r.Outcomes[app.OutcomeImported] != 1 {
		t.Fatalf("after the import: %+v", r)
	}
}
