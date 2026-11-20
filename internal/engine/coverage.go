package engine

import (
	"context"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// CoverageReader is a manager whose library coverage can be swept.
type CoverageReader interface {
	CoverageGroups(ctx context.Context) ([]domain.CoverageGroup, error)
	CoverageItems(ctx context.Context, g domain.CoverageGroup, now time.Time) ([]domain.CoverageItem, error)
}

const (
	// coverageInterval spaces the start of coverage sweeps per manager.
	coverageInterval = 6 * time.Hour
	// coverageGroupsPerCycle bounds item reads (one request per Sonarr
	// series; Radarr's whole list is one) per manager and cycle.
	coverageGroupsPerCycle = 20
)

// sweepCoverage advances each readable manager's coverage sweep, whether or
// not any search automation is enabled: a new sweep starts from a complete
// group list at most every coverageInterval, and each cycle reads up to
// coverageGroupsPerCycle groups not read since the sweep began. A failed
// read leaves the stored counts as they were; nothing is removed except by
// a complete group list.
func (e *Engine) sweepCoverage(ctx context.Context, results []*fetched) {
	ok := map[string]bool{}
	for _, f := range results {
		if f.queue != nil && f.err == nil {
			ok[f.id] = true
		}
	}
	for _, m := range e.conns.Managers {
		cr, can := m.(CoverageReader)
		if !can || !ok[m.ID()] {
			continue
		}
		now := e.clock.Now()
		sw, err := e.st.CoverageSweepState(ctx, m.ID())
		if err != nil {
			e.log.Error("coverage sweep state not read", "manager", m.ID(), "err", err)
			continue
		}
		if sw == nil || (sw.CompletedAt != nil && now.Sub(sw.StartedAt) >= coverageInterval) {
			groups, err := cr.CoverageGroups(ctx)
			if err != nil {
				e.log.Warn("coverage group list not read", "manager", m.ID(), "class", adapter.ClassOf(err), "error", errorText(err))
				continue
			}
			if err := e.st.Write(ctx, func(tx *store.Tx) error { return tx.StartCoverageSweep(m.ID(), groups, now) }); err != nil {
				e.log.Error("coverage sweep not started", "manager", m.ID(), "err", err)
				continue
			}
			sw = &store.CoverageSweep{ManagerID: m.ID(), StartedAt: now}
		}
		if sw.CompletedAt != nil {
			continue
		}
		due, err := e.st.CoverageGroupsDue(ctx, m.ID(), sw.StartedAt, coverageGroupsPerCycle)
		if err != nil {
			e.log.Error("coverage groups not read", "manager", m.ID(), "err", err)
			continue
		}
		for _, g := range due {
			items, err := cr.CoverageItems(ctx, g, now)
			if err != nil {
				e.log.Warn("coverage items not read; the group keeps its previous counts", "manager", m.ID(), "group", g.ID,
					"class", adapter.ClassOf(err), "error", errorText(err))
				break
			}
			var finished bool
			if err := e.st.Write(ctx, func(tx *store.Tx) error {
				if err := tx.SaveCoverageGroup(m.ID(), g.ID, items, now); err != nil {
					return err
				}
				var err error
				finished, err = tx.FinishCoverageSweepIfDone(m.ID(), now)
				return err
			}); err != nil {
				e.log.Error("coverage not saved", "manager", m.ID(), "err", err)
				break
			}
			if finished {
				e.log.Info("coverage sweep complete", "manager", m.ID())
			}
		}
	}
}
