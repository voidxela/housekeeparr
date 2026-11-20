package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/notify"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Release-source scorecard settings (initial replay settings, plan
// 2026-11-20): grabs of the last ScorecardWindow; a grab is mature once
// ScorecardMaturity old, whatever its outcome so far (so slow imports are
// not counted against a source while fast failures are); an alert needs at
// least ScorecardMinSample mature attributable grabs and opens below
// ScorecardAlertBelow imported, and clears at ScorecardClearAt or above.
const (
	ScorecardWindow     = 14 * 24 * time.Hour
	ScorecardMaturity   = 72 * time.Hour
	ScorecardMinSample  = 30
	ScorecardAlertBelow = 0.5
	ScorecardClearAt    = 0.6
)

// Acquisition outcomes, from the manager's own history.
const (
	OutcomeImported = "imported"
	// OutcomePartial: a pack imported some, not all, of what it was grabbed for.
	OutcomePartial = "partial"
	OutcomeFailed  = "failed"
	OutcomeIgnored = "ignored"
	// OutcomePending: no import or failure yet, and younger than maturity.
	OutcomePending = "pending"
	// OutcomeUnknown: mature and nothing observed (no import, no failure).
	OutcomeUnknown = "unknown"
)

// Acquisition is one grab (one download ID on one manager; a pack's
// per-episode grab records are one acquisition) and what became of it.
type Acquisition struct {
	ManagerID     string     `json:"manager_id"`
	DownloadID    string     `json:"download_id"`
	Indexer       string     `json:"indexer,omitempty"`
	ReleaseGroup  string     `json:"release_group,omitempty"`
	ReleaseSource string     `json:"release_source,omitempty"`
	Title         string     `json:"title,omitempty"`
	GrabbedAt     time.Time  `json:"grabbed_at"`
	Content       []string   `json:"content,omitempty"`
	Imported      []string   `json:"imported,omitempty"`
	Outcome       string     `json:"outcome"`
	OutcomeAt     *time.Time `json:"outcome_at,omitempty"`
}

// SourceStats aggregates one indexer's acquisitions in the window.
type SourceStats struct {
	Indexer  string   `json:"indexer"`
	Managers []string `json:"managers"`
	Grabs    int      `json:"grabs"`
	// Counts by outcome (every grab in the window, mature or not).
	Outcomes map[string]int `json:"outcomes"`
	// Mature grabs and those of them imported (fully or partly).
	Mature         int       `json:"mature"`
	MatureImported int       `json:"mature_imported"`
	ImportFraction *float64  `json:"import_fraction,omitempty"`
	FirstGrab      time.Time `json:"first_grab"`
	LastGrab       time.Time `json:"last_grab"`
	Alert          bool      `json:"alert"`
	// Quality says why no rate is judged (too few mature grabs, or the
	// history read does not cover the window), "" when it is.
	Quality string `json:"data_quality,omitempty"`
}

// HistoryCoverage is how much of a manager's history has been read.
type HistoryCoverage struct {
	ManagerID    string     `json:"manager_id"`
	CoveredSince *time.Time `json:"covered_since,omitempty"`
	Complete     bool       `json:"complete"`
	GapAt        *time.Time `json:"gap_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	// CoversWindow: grabs in the whole window are known.
	CoversWindow bool `json:"covers_window"`
}

// RecoveryReplacements is what became of Housekeeparr's recoveries'
// replacement grabs, joined to the manager's history by positive download
// identity.
type RecoveryReplacements struct {
	Recoveries int `json:"recoveries"`
	// Grabbed: a replacement grab was observed; then by outcome.
	Grabbed  int            `json:"grabbed"`
	Outcomes map[string]int `json:"outcomes"`
	// NoGrab: no replacement grab has been observed.
	NoGrab int `json:"no_grab"`
}

// Scorecard is the release-source reliability view (CLI and web alike).
type Scorecard struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Window      time.Duration        `json:"window"`
	Maturity    time.Duration        `json:"maturity"`
	MinSample   int                  `json:"min_sample"`
	AlertBelow  float64              `json:"alert_below"`
	ClearAt     float64              `json:"clear_at"`
	Coverage    []HistoryCoverage    `json:"coverage"`
	Sources     []SourceStats        `json:"sources"`
	Totals      SourceStats          `json:"totals"`
	Recovery    RecoveryReplacements `json:"recovery"`
	// Acquisitions are listed only on request (CLI --acquisitions).
	Acquisitions []Acquisition `json:"acquisitions,omitempty"`
}

var importEvents = map[string]bool{"downloadFolderImported": true, "downloadImported": true}

// acquisitions folds history records (ordered by manager, download, time)
// into acquisitions with their outcomes.
func acquisitions(evs []store.HistoryEvent, now time.Time) []Acquisition {
	type key struct{ m, d string }
	byKey := map[key]*Acquisition{}
	var order []key
	imported := map[key]map[string]bool{}
	grabbed := map[key]map[string]bool{}
	flags := map[key]map[string]*time.Time{}
	for _, e := range evs {
		k := key{e.ManagerID, strings.ToUpper(e.DownloadID)}
		a := byKey[k]
		if a == nil {
			a = &Acquisition{ManagerID: e.ManagerID, DownloadID: k.d}
			byKey[k], imported[k], grabbed[k], flags[k] = a, map[string]bool{}, map[string]bool{}, map[string]*time.Time{}
			order = append(order, k)
		}
		at := e.At
		switch {
		case e.EventType == "grabbed":
			if a.GrabbedAt.IsZero() || at.Before(a.GrabbedAt) {
				a.GrabbedAt = at
			}
			a.Indexer, a.ReleaseGroup, a.ReleaseSource = nonEmpty(a.Indexer, e.Indexer), nonEmpty(a.ReleaseGroup, e.ReleaseGroup), nonEmpty(a.ReleaseSource, e.ReleaseSource)
			a.Title = nonEmpty(a.Title, e.SourceTitle)
			if e.ContentKey != "" {
				grabbed[k][e.ContentKey] = true
			}
		case importEvents[e.EventType]:
			if e.ContentKey != "" {
				imported[k][e.ContentKey] = true
			} else {
				imported[k]["?"] = true
			}
			if flags[k]["import"] == nil {
				flags[k]["import"] = &at
			}
		case e.EventType == "downloadFailed":
			if flags[k]["failed"] == nil {
				flags[k]["failed"] = &at
			}
		case e.EventType == "downloadIgnored":
			if flags[k]["ignored"] == nil {
				flags[k]["ignored"] = &at
			}
		}
	}
	var out []Acquisition
	for _, k := range order {
		a := byKey[k]
		if a.GrabbedAt.IsZero() {
			continue // events of a download whose grab is outside what was read
		}
		a.Content, a.Imported = sortedKeys(grabbed[k]), sortedKeys(imported[k])
		all := true
		for c := range grabbed[k] {
			all = all && imported[k][c]
		}
		f := flags[k]
		switch {
		case len(imported[k]) > 0 && all:
			a.Outcome, a.OutcomeAt = OutcomeImported, f["import"]
		case len(imported[k]) > 0:
			a.Outcome, a.OutcomeAt = OutcomePartial, f["import"]
		case f["failed"] != nil:
			a.Outcome, a.OutcomeAt = OutcomeFailed, f["failed"]
		case f["ignored"] != nil:
			a.Outcome, a.OutcomeAt = OutcomeIgnored, f["ignored"]
		case now.Sub(a.GrabbedAt) < ScorecardMaturity:
			a.Outcome = OutcomePending
		default:
			a.Outcome = OutcomeUnknown
		}
		out = append(out, *a)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Scorecard computes the release-source scorecard from stored history.
func (s *Service) Scorecard(ctx context.Context, withAcquisitions bool) (*Scorecard, error) {
	now := s.now()
	sc := &Scorecard{GeneratedAt: now, Window: ScorecardWindow, Maturity: ScorecardMaturity, MinSample: ScorecardMinSample,
		AlertBelow: ScorecardAlertBelow, ClearAt: ScorecardClearAt}
	start := now.Add(-ScorecardWindow)
	cursors, err := s.st.HistoryCursors(ctx)
	if err != nil {
		return nil, err
	}
	covers := map[string]bool{}
	var mids []string
	for _, m := range s.cfg.Sonarr {
		mids = append(mids, m.ID)
	}
	for _, m := range s.cfg.Radarr {
		mids = append(mids, m.ID)
	}
	for _, id := range mids {
		cv := HistoryCoverage{ManagerID: id}
		if c, ok := cursors[id]; ok {
			at := c.UpdatedAt
			cv.CoveredSince, cv.Complete, cv.GapAt, cv.UpdatedAt = c.CoveredSince, c.Complete, c.GapAt, &at
			// A complete read that ended before the window began covers it
			// too (the history simply has nothing older).
			cv.CoversWindow = c.GapAt == nil && (c.Complete || (c.CoveredSince != nil && !c.CoveredSince.After(start)))
			if c.GapAt != nil && c.GapAt.Before(start) {
				cv.CoversWindow = c.CoveredSince != nil && !c.CoveredSince.After(start)
			}
		}
		covers[id] = cv.CoversWindow
		sc.Coverage = append(sc.Coverage, cv)
	}
	evs, err := s.st.HistoryForGrabsSince(ctx, start)
	if err != nil {
		return nil, err
	}
	acqs := acquisitions(evs, now)
	byIdx := map[string]*SourceStats{}
	mgrs := map[string]map[string]bool{}
	sc.Totals = SourceStats{Indexer: "all sources", Outcomes: map[string]int{}}
	add := func(st *SourceStats, a Acquisition) {
		st.Grabs++
		st.Outcomes[a.Outcome]++
		if st.FirstGrab.IsZero() || a.GrabbedAt.Before(st.FirstGrab) {
			st.FirstGrab = a.GrabbedAt
		}
		if a.GrabbedAt.After(st.LastGrab) {
			st.LastGrab = a.GrabbedAt
		}
		if now.Sub(a.GrabbedAt) >= ScorecardMaturity {
			st.Mature++
			if a.Outcome == OutcomeImported || a.Outcome == OutcomePartial {
				st.MatureImported++
			}
		}
	}
	for _, a := range acqs {
		if a.GrabbedAt.Before(start) {
			continue
		}
		idx := nonEmpty(a.Indexer, "(indexer not reported)")
		st := byIdx[idx]
		if st == nil {
			st = &SourceStats{Indexer: idx, Outcomes: map[string]int{}}
			byIdx[idx], mgrs[idx] = st, map[string]bool{}
		}
		mgrs[idx][a.ManagerID] = true
		add(st, a)
		add(&sc.Totals, a)
	}
	open, err := s.openSourceAlerts(ctx)
	if err != nil {
		return nil, err
	}
	for idx, st := range byIdx {
		st.Managers = sortedKeys(mgrs[idx])
		if st.Mature > 0 {
			f := float64(st.MatureImported) / float64(st.Mature)
			st.ImportFraction = &f
		}
		for _, m := range st.Managers {
			if !covers[m] {
				st.Quality = "the history read for " + m + " does not cover the whole window yet; no rate is judged"
			}
		}
		switch {
		case st.Indexer == "(indexer not reported)":
			st.Quality = nonEmpty(st.Quality, "grabs without an indexer in the manager's history are not attributed")
		case st.Quality == "" && st.Mature < ScorecardMinSample:
			st.Quality = fmt.Sprintf("%d mature grabs; at least %d are needed to judge a rate", st.Mature, ScorecardMinSample)
		}
		if st.Quality == "" && st.ImportFraction != nil {
			// Hysteresis: an open alert clears only at ClearAt.
			st.Alert = *st.ImportFraction < ScorecardAlertBelow || (open[st.Indexer] && *st.ImportFraction < ScorecardClearAt)
		} else {
			// Without a judgeable rate an open alert is kept as it is.
			st.Alert = open[st.Indexer]
		}
		sc.Sources = append(sc.Sources, *st)
	}
	sort.Slice(sc.Sources, func(i, j int) bool {
		if sc.Sources[i].Grabs != sc.Sources[j].Grabs {
			return sc.Sources[i].Grabs > sc.Sources[j].Grabs
		}
		return sc.Sources[i].Indexer < sc.Sources[j].Indexer
	})
	if sc.Recovery, err = s.recoveryReplacements(ctx, now); err != nil {
		return nil, err
	}
	if withAcquisitions {
		sc.Acquisitions = acqs
	}
	return sc, nil
}

// recoveryReplacements joins recovery outcomes that observed a replacement
// grab to that download's history.
func (s *Service) recoveryReplacements(ctx context.Context, now time.Time) (RecoveryReplacements, error) {
	out := RecoveryReplacements{Outcomes: map[string]int{}}
	ocs, err := s.st.RecentOutcomes(ctx, now.Add(-ScorecardWindow))
	if err != nil {
		return out, err
	}
	for _, o := range ocs {
		out.Recoveries++
		if o.NewDownloadID == "" {
			out.NoGrab++
			continue
		}
		out.Grabbed++
		evs, err := s.st.HistoryForDownloads(ctx, o.ManagerID, []string{strings.ToUpper(o.NewDownloadID)})
		if err != nil {
			return out, err
		}
		as := acquisitions(evs, now)
		switch {
		case len(as) == 1:
			out.Outcomes[as[0].Outcome]++
		case o.GrabbedAt != nil && now.Sub(*o.GrabbedAt) < ScorecardMaturity:
			out.Outcomes[OutcomePending]++
		default:
			// The grab record is not in the history read (yet): unknown.
			out.Outcomes[OutcomeUnknown]++
		}
	}
	return out, nil
}

const sourceScope = "source:"

func (s *Service) openSourceAlerts(ctx context.Context) (map[string]bool, error) {
	fs, err := s.st.Findings(ctx, store.FindingFilter{Type: "source.low_import_rate",
		States: []domain.FindingState{domain.FindingOpen, domain.FindingAcknowledged, domain.FindingSnoozed}})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range fs {
		out[strings.TrimPrefix(f.DedupKey, sourceScope)] = true
	}
	return out, nil
}

// EvaluateSources keeps one finding per source whose mature, attributable
// grabs import below the alert threshold. Sources whose rate cannot be
// judged (too few mature grabs, or history not covering the window) open
// no new finding and leave an existing one as it is. Observation only.
func (s *Service) EvaluateSources(ctx context.Context) error {
	sc, err := s.Scorecard(ctx, false)
	if err != nil {
		return err
	}
	now := s.now()
	ev := findings.Evaluation{Scope: sourceScope, Actor: "engine", Keep: map[string]bool{}}
	for _, st := range sc.Sources {
		key := sourceScope + st.Indexer
		if !st.Alert {
			continue
		}
		if st.Quality != "" || st.ImportFraction == nil {
			ev.Keep[key] = true
			continue
		}
		pct := 100 * *st.ImportFraction
		ev.Drafts = append(ev.Drafts, findings.Draft{
			DedupKey: key, Type: "source.low_import_rate", Severity: domain.SeverityWarning,
			Title: fmt.Sprintf("Grabs from %s rarely import (%.0f%% of %d mature grabs)", st.Indexer, pct, st.Mature),
			Summary: fmt.Sprintf("Of %d grabs from %s that are at least %s old (last %s), %d imported fully or partly (%.0f%%). %d grabs in the window in all: %s. "+
				"This is manager-side evidence about the releases this source offers, not proof that the source is down, and Housekeeparr does not change source selection.",
				st.Mature, st.Indexer, policy.HumanDuration(ScorecardMaturity), policy.HumanDuration(ScorecardWindow), st.MatureImported, pct, st.Grabs, outcomeList(st.Outcomes)),
			Subject:    domain.Subject{Connections: st.Managers},
			EvidenceAt: now,
			Detail: domain.FindingDetail{NextSteps: []string{
				"Review the source's grabs in the manager's history (housekeeparr sources --acquisitions lists them).",
				"Consider lowering its indexer priority or adjusting release profiles in the manager; Housekeeparr does not change them.",
				fmt.Sprintf("The finding resolves when its import rate reaches %.0f%% or more.", 100*ScorecardClearAt),
			}},
		})
	}
	return s.st.Write(ctx, func(tx *store.Tx) error {
		changes, err := findings.Apply(tx, ev, now)
		if err != nil {
			return err
		}
		return notify.Enqueue(tx, s.cfg.Notify, s.cfg.Notifications, changes, now)
	})
}

func outcomeList(m map[string]int) string {
	var parts []string
	for _, k := range []string{OutcomeImported, OutcomePartial, OutcomeFailed, OutcomeIgnored, OutcomePending, OutcomeUnknown} {
		if m[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", m[k], k))
		}
	}
	return strings.Join(parts, ", ")
}
