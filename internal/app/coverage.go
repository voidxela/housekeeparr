package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Gap states: every counted gap has exactly one current primary state,
// chosen in this order; the other facts become additional reasons.
const (
	GapImportPending    = "import_pending"     // the manager holds a download for it that is not imported
	GapStalled          = "stalled"            // queued, and its transfer has an open stall finding
	GapQueued           = "queued"             // the manager is downloading it
	GapSearchedNoGrab   = "searched_no_grab"   // Housekeeparr searched; no grab was observed after it
	GapDeferred         = "deferred"           // a search is scheduled later (spacing or backoff)
	GapNoSearchObserved = "no_search_observed" // in the wanted list, but no search observed since tracking began
	GapUnknown          = "unknown"            // not in the wanted-list reads; no evidence either way
)

// GapStates lists the gap states in priority order.
var GapStates = []string{GapImportPending, GapStalled, GapQueued, GapSearchedNoGrab, GapDeferred, GapNoSearchObserved, GapUnknown}

// GapLabel is the plain-language name of a gap state.
func GapLabel(state string) string {
	return map[string]string{
		GapImportPending: "import pending", GapStalled: "stalled download", GapQueued: "downloading",
		GapSearchedNoGrab: "searched, no grab observed", GapDeferred: "search deferred",
		GapNoSearchObserved: "no search observed since tracking began", GapUnknown: "no evidence yet",
	}[state]
}

// Gap is one wanted item without a file, explained.
type Gap struct {
	store.CoverageGap
	State      string     `json:"state"`
	EvidenceAt *time.Time `json:"evidence_at,omitempty"`
	Reasons    []string   `json:"reasons,omitempty"`
}

// ManagerCoverage is one manager's coverage view.
type ManagerCoverage struct {
	store.CoverageTotals
	Name     string               `json:"name"`
	Percent  float64              `json:"percent"`
	Complete bool                 `json:"complete"` // the latest sweep read every group
	Sweep    *store.CoverageSweep `json:"sweep,omitempty"`
	Trend    []store.CoverageDay  `json:"trend,omitempty"`
	// DenominatorChange is wanted now minus wanted on the previous recorded
	// day (newly monitored or aired content, or unmonitored content).
	DenominatorChange *int           `json:"denominator_change,omitempty"`
	States            map[string]int `json:"states"`
	Gaps              []Gap          `json:"gaps,omitempty"`
	GapsTotal         int            `json:"gaps_total"`
}

// Coverage is the coverage view shared by the CLI and web.
type Coverage struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Managers    []ManagerCoverage `json:"managers"`
	Notes       []string          `json:"notes"`
}

// coverageGapLimit bounds the gaps classified per manager in one view.
const coverageGapLimit = 20000

// Coverage builds the coverage view from local state only (no upstream
// calls). gapPage gaps per manager are returned for drill-down, from
// offset; every gap up to coverageGapLimit is counted by state.
func (s *Service) Coverage(ctx context.Context, managerID string, offset, gapPage int) (*Coverage, error) {
	now := s.now()
	out := &Coverage{GeneratedAt: now, Notes: []string{
		"Coverage counts monitored, aired episodes (of monitored series) and monitored, available movies; a gap is one without a file.",
		"Counts come from a bounded sweep (one series per request); until a sweep has read every group, the figures are incomplete and say so.",
		"A verified recovery or search is not an import: these counts change only when the manager reports a file.",
	}}
	totals, err := s.st.CoverageTotals(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if conns, err := s.st.Connections(ctx); err == nil {
		for _, c := range conns {
			names[c.ID] = c.Name
		}
	}
	queue, err := s.st.QueueItems(ctx, store.QueueFilter{PresentOnly: true})
	if err != nil {
		return nil, err
	}
	stalled := map[string]bool{}
	for _, typ := range []string{domain.FindingTransferStalled, domain.FindingMetadataTimeout, domain.FindingSourceRejected} {
		fs, err := s.st.Findings(ctx, store.FindingFilter{Type: typ, States: []domain.FindingState{domain.FindingOpen, domain.FindingAcknowledged, domain.FindingSnoozed}})
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			stalled[f.Subject.ClientID+"|"+f.Subject.NativeID] = true
		}
	}
	for _, t := range totals {
		if managerID != "" && t.ManagerID != managerID {
			continue
		}
		mc := ManagerCoverage{CoverageTotals: t, Name: nonEmptyStr(names[t.ManagerID], t.ManagerID), States: map[string]int{}}
		if t.Wanted > 0 {
			mc.Percent = 100 * float64(t.Have) / float64(t.Wanted)
		}
		if mc.Sweep, err = s.st.CoverageSweepState(ctx, t.ManagerID); err != nil {
			return nil, err
		}
		mc.Complete = t.GroupsRead == t.Groups && mc.Sweep != nil && mc.Sweep.LastCompleteAt != nil
		if mc.Trend, err = s.st.CoverageTrend(ctx, t.ManagerID, 30, now); err != nil {
			return nil, err
		}
		if n := len(mc.Trend); n >= 2 {
			d := mc.Trend[n-1].Wanted - mc.Trend[n-2].Wanted
			mc.DenominatorChange = &d
		}
		gaps, err := s.st.CoverageGaps(ctx, t.ManagerID, 0, coverageGapLimit)
		if err != nil {
			return nil, err
		}
		missing, err := s.st.MissingContent(ctx, store.MissingFilter{ManagerID: t.ManagerID, Limit: coverageGapLimit})
		if err != nil {
			return nil, err
		}
		byKey := map[string]store.MissingRecord{}
		for _, m := range missing {
			byKey[m.ContentKey] = m
		}
		grabs, err := s.st.LastGrabs(ctx, t.ManagerID)
		if err != nil {
			return nil, err
		}
		rows := map[string][]store.QueueRecord{}
		for _, q := range queue {
			if q.ManagerID == t.ManagerID {
				rows[q.Latest.ContentRef()] = append(rows[q.Latest.ContentRef()], q)
			}
		}
		mc.GapsTotal = t.Gaps
		for i, g := range gaps {
			x := explainGap(g, rows[g.ContentKey], byKey[g.ContentKey], grabs, stalled, now)
			mc.States[x.State]++
			if i >= offset && len(mc.Gaps) < gapPage {
				mc.Gaps = append(mc.Gaps, x)
			}
		}
		out.Managers = append(out.Managers, mc)
	}
	sort.Slice(out.Managers, func(i, j int) bool { return out.Managers[i].ManagerID < out.Managers[j].ManagerID })
	return out, nil
}

func explainGap(g store.CoverageGap, rows []store.QueueRecord, m store.MissingRecord, grabs map[string]time.Time, stalled map[string]bool, now time.Time) Gap {
	x := Gap{CoverageGap: g}
	var reasons []string
	set := func(state string, at *time.Time, why string) {
		if x.State == "" {
			x.State, x.EvidenceAt = state, at
			if why != "" {
				x.Reasons = append(x.Reasons, why)
			}
			return
		}
		if why != "" {
			reasons = append(reasons, why)
		}
	}
	for _, q := range rows {
		if q.AttentionState == "importBlocked" || q.AttentionState == "importPending" {
			set(GapImportPending, q.AttentionSince, fmt.Sprintf("%s: %s", q.AttentionState, q.Latest.Title))
		}
	}
	for _, q := range rows {
		if q.MatchStatus == store.MatchMatched && stalled[q.MatchClientID+"|"+q.MatchNativeID] {
			at := q.LastSeen
			set(GapStalled, &at, "its download has an open stall or source finding: "+q.Latest.Title)
		}
	}
	for _, q := range rows {
		at := q.LastSeen
		ts, _ := q.Latest.TrackedState.Get()
		set(GapQueued, &at, fmt.Sprintf("queued (%s): %s", nonEmptyStr(ts, "state unknown"), q.Latest.Title))
	}
	if m.ContentKey != "" {
		since := m.FirstSeen
		if m.Searches > 0 {
			grabbedAfter := false
			if at, ok := grabs[g.ContentKey]; ok && m.LastSearchAt != nil && !at.Before(*m.LastSearchAt) {
				grabbedAfter = true
			}
			if !grabbedAfter {
				set(GapSearchedNoGrab, m.LastSearchAt, fmt.Sprintf("searched %d time(s) by Housekeeparr, last %s (%s); no grab observed after it", m.Searches,
					timeText(m.LastSearchAt), strings.ReplaceAll(m.State, "_", " ")))
			}
		}
		if m.NextSearchAt != nil && m.NextSearchAt.After(now) {
			set(GapDeferred, m.NextSearchAt, "next search not before "+m.NextSearchAt.UTC().Format(time.RFC3339))
		}
		if m.Searches == 0 {
			set(GapNoSearchObserved, &since, "in the wanted list since "+since.UTC().Format(time.RFC3339)+"; no search observed since tracking began")
		}
	}
	if at, ok := grabs[g.ContentKey]; ok && len(rows) == 0 {
		a := at
		set(GapUnknown, &a, "grabbed at "+at.UTC().Format(time.RFC3339)+" (manager history) and no longer queued")
	}
	if x.State == "" {
		set(GapUnknown, nil, "not yet in the wanted-list reads or the queue; no evidence either way")
	}
	x.Reasons = append(x.Reasons, reasons...)
	return x
}

func timeText(t *time.Time) string {
	if t == nil {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

func nonEmptyStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
