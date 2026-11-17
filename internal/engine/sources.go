package engine

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/notify"
	"github.com/voidxela/housekeeparr/internal/store"
)

// SourceReader is a manager whose search sources can be read.
type SourceReader interface {
	Sources(ctx context.Context) (domain.SourceSnapshot, error)
}

// Source finding types (SPEC §9.2).
const (
	FindingSourceUnavailable = "source.unavailable"
	FindingSourceNone        = "source.none"
)

// sourceRecovery is how many consecutive reads must find a source not
// disabled before its finding resolves (a single read does not prove it
// recovered).
const sourceRecovery = 2

// readSources refreshes the managers' search sources and reconciles the
// source findings: a source the manager uses for automatic torrent
// searches that it has disabled after failures, and a manager with no such
// source at all. A failed read changes nothing.
func (e *Engine) readSources(ctx context.Context, results []*fetched) {
	ok := map[string]bool{}
	for _, f := range results {
		if f.queue != nil && f.err == nil {
			ok[f.id] = true
		}
	}
	for _, m := range e.conns.Managers {
		sr, can := m.(SourceReader)
		if !can || !ok[m.ID()] {
			continue
		}
		snap, err := sr.Sources(ctx)
		if err != nil {
			e.log.Warn("search sources not read", "manager", m.ID(), "class", adapter.ClassOf(err), "error", errorText(err))
			continue
		}
		snap.ManagerID = m.ID()
		now := e.clock.Now()
		var res findings.Result
		if err := e.st.Write(ctx, func(tx *store.Tx) error {
			if err := tx.SaveSources(snap); err != nil {
				return err
			}
			r, err := e.sourceFindings(tx, m.ID(), now)
			if err != nil {
				return err
			}
			res = r
			return notify.Enqueue(tx, e.cfg.Notify, r, now)
		}); err != nil {
			e.log.Error("search sources not saved", "manager", m.ID(), "err", err)
			continue
		}
		for _, f := range append(res.Opened, res.Recurred...) {
			e.log.Info("finding opened", "id", f.ID, "type", f.Type, "title", f.Title)
		}
	}
}

func (e *Engine) sourceFindings(tx *store.Tx, managerID string, now time.Time) (findings.Result, error) {
	meta := e.conns.Meta[managerID]
	scope := "source:" + managerID + "|"
	ev := findings.Evaluation{Scope: scope, Keep: map[string]bool{}, Actor: "engine"}
	list, err := tx.SourcesOf(managerID)
	if err != nil {
		return findings.Result{}, err
	}
	byID := map[string]store.SourceRecord{}
	usable := 0
	for _, s := range list {
		byID[strconv.FormatInt(s.ID, 10)] = s
		if !s.AutomaticSearch || s.Protocol != "torrent" {
			continue
		}
		usable++
		if !s.Unavailable(now) {
			continue
		}
		ev.Drafts = append(ev.Drafts, findings.Draft{
			DedupKey: scope + strconv.FormatInt(s.ID, 10) + "|unavailable", Type: FindingSourceUnavailable, Severity: domain.SeverityWarning,
			Title:   fmt.Sprintf("%s: indexer %q is disabled after failures", meta.Name, s.Name),
			Summary: fmt.Sprintf("%s disabled %q until %s, so its searches do not query it: search coverage is incomplete until then.", meta.Name, s.Name, s.DisabledUntil.UTC().Format(time.RFC3339)),
			Subject: domain.Subject{Connections: []string{managerID}, ManagerID: managerID, Source: s.Name},
			Detail: domain.FindingDetail{
				Evidence: []domain.Evidence{
					{Label: "Disabled until", Value: s.DisabledUntil.UTC().Format(time.RFC3339), At: &now, Source: managerID + " indexer status"},
					{Label: "Most recent failure", Value: timeText(s.LastFailure)},
				},
				Confidence:  "Reported by the manager's indexer status, which it sets after failed requests to the indexer.",
				Limitations: []string{"This is contemporaneous source health: it shows possible impact on searches, not that a particular search failed.", "Housekeeparr does not query indexers itself."},
				NextSteps: []string{fmt.Sprintf("Open %s → Settings → Indexers and test %q; check the tracker's status and your account.", meta.Name, s.Name),
					"Housekeeparr resolves this after two consecutive reads in which the manager no longer has it disabled."},
			},
			EvidenceAt: now,
		})
	}
	if usable == 0 {
		ev.Drafts = append(ev.Drafts, findings.Draft{
			DedupKey: scope + "none", Type: FindingSourceNone, Severity: domain.SeverityWarning,
			Title:   meta.Name + " has no indexer enabled for automatic torrent searches",
			Summary: "Neither its own searches nor Housekeeparr's proactive searches can find torrent releases.",
			Subject: domain.Subject{Connections: []string{managerID}, ManagerID: managerID},
			Detail: domain.FindingDetail{
				Confidence:  "From the manager's indexer configuration (protocol and \"Enable Automatic Search\").",
				Limitations: []string{"This is a configuration finding: Housekeeparr does not add or enable sources, and cannot tell which sources you need."},
				NextSteps:   []string{fmt.Sprintf("Open %s → Settings → Indexers and enable automatic search on a torrent indexer, if that is intended.", meta.Name)},
			},
			EvidenceAt: now,
		})
	}
	ev.Resolve = func(f domain.Finding) findings.Resolution {
		if f.Type == FindingSourceNone {
			return findings.Resolution{Reason: meta.Name + " now has an indexer enabled for automatic torrent searches."}
		}
		id := f.DedupKey[len(scope):]
		if i := len(id) - len("|unavailable"); i > 0 {
			id = id[:i]
		}
		s, present := byID[id]
		switch {
		case !present:
			return findings.Resolution{Reason: "The indexer is no longer configured in " + meta.Name + "."}
		case s.OKStreak < sourceRecovery:
			return findings.Resolution{}
		}
		return findings.Resolution{Reason: fmt.Sprintf("Verified: %s has not had %q disabled on %d consecutive reads.", meta.Name, s.Name, s.OKStreak)}
	}
	// A source not yet shown recovered keeps its finding.
	actives, err := tx.ActiveFindingsWithPrefix(scope)
	if err != nil {
		return findings.Result{}, err
	}
	for _, f := range actives {
		if f.Type != FindingSourceUnavailable {
			continue
		}
		id := f.DedupKey[len(scope) : len(f.DedupKey)-len("|unavailable")]
		if s, ok := byID[id]; ok && s.OKStreak < sourceRecovery && !s.Unavailable(now) {
			ev.Keep[f.DedupKey] = true
		}
	}
	return findings.Apply(tx, ev, now)
}

func timeText(t *time.Time) string {
	if t == nil {
		return "not reported"
	}
	return t.UTC().Format(time.RFC3339)
}
