package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// The sweep reads a bounded number of groups per cycle, completes once
// every listed group was read, keeps counts across failed reads, and drops
// a group only from a complete group list.
func TestCoverageSweepIsBoundedAndKeepsUnseenGroups(t *testing.T) {
	h := newHarness(t, baseConfig)
	var groups []domain.CoverageGroup
	items := map[int64][]domain.CoverageItem{}
	for i := int64(1); i <= 25; i++ {
		groups = append(groups, domain.CoverageGroup{ID: i, Title: fmt.Sprint("S", i), Monitored: true})
		items[i] = []domain.CoverageItem{
			{Key: fmt.Sprintf("episode:%d01", i), GroupID: i, Label: "e1", Wanted: true, HasFile: true},
			{Key: fmt.Sprintf("episode:%d02", i), GroupID: i, Label: "e2", Wanted: true, HasFile: false},
			{Key: fmt.Sprintf("episode:%d03", i), GroupID: i, Label: "unaired", Wanted: false},
		}
	}
	h.mgr.SetCoverage(groups, items)
	h.cycle()
	if n := h.mgr.CoverageItemReads(); n != coverageGroupsPerCycle {
		t.Fatalf("first cycle read %d groups, want %d", n, coverageGroupsPerCycle)
	}
	tot, _ := h.st.CoverageTotals(ctx)
	if len(tot) != 1 || tot[0].Groups != 25 || tot[0].GroupsRead != 20 {
		t.Fatalf("partial sweep %+v", tot)
	}
	h.clock.Advance(10 * time.Minute)
	h.cycle()
	tot, _ = h.st.CoverageTotals(ctx)
	if tot[0].Wanted != 50 || tot[0].Have != 25 || tot[0].Gaps != 25 || tot[0].GroupsRead != 25 {
		t.Fatalf("complete sweep %+v", tot)
	}
	if sw, _ := h.st.CoverageSweepState(ctx, "sonarr"); sw == nil || sw.CompletedAt == nil {
		t.Fatalf("sweep %+v", sw)
	}
	// Nothing more is read until the next sweep is due.
	reads := h.mgr.CoverageItemReads()
	h.run(60, 10)
	if h.mgr.CoverageItemReads() != reads {
		t.Fatal("read again before the next sweep")
	}
	// A failed group list keeps everything; a later complete list drops the
	// removed series.
	h.mgr.FailCoverage(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"}, nil)
	h.clock.Advance(coverageInterval)
	h.cycle()
	if tot, _ := h.st.CoverageTotals(ctx); tot[0].Groups != 25 || tot[0].Wanted != 50 {
		t.Fatalf("after a failed list %+v", tot)
	}
	h.mgr.FailCoverage(nil, nil)
	h.mgr.SetCoverage(groups[:24], items)
	h.run(30, 10)
	if tot, _ := h.st.CoverageTotals(ctx); tot[0].Groups != 24 || tot[0].Wanted != 48 || tot[0].Gaps != 24 {
		t.Fatalf("after a series was removed %+v", tot)
	}
	// Failed item reads keep the previous counts.
	h.mgr.FailCoverage(nil, &adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
	h.clock.Advance(coverageInterval)
	h.run(30, 10)
	if tot, _ := h.st.CoverageTotals(ctx); tot[0].Wanted != 48 || tot[0].Have != 24 {
		t.Fatalf("after failed item reads %+v", tot)
	}
}
