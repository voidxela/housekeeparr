package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// A pack whose history is longer than the bounded read cannot establish
// which episodes it imported: an episode imported long ago whose file was
// since deleted would look never imported and be cleared and searched as
// missing. The clearance is refused, visibly, with the next step.
func TestPartialPackRefusesIncompleteHistory(t *testing.T) {
	w := packWorld(t, func(w *world, rows []domain.QueueItem) []domain.QueueItem {
		w.mgr.SetHasFile("episode:201", false) // imported, then deleted
		w.mgr.LimitHistory(1)                  // only E02's import is read
		return rows
	})
	w.run(2 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 0 {
		t.Fatalf("cleared on incomplete history: %+v", r)
	}
	fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: domain.FindingPartialPack})
	if len(fs) != 1 {
		t.Fatalf("findings %+v", fs)
	}
	d, _ := w.svc.Finding(context.Background(), fs[0].ID)
	if d.Automation == nil || !strings.Contains(d.Automation.Detail, "cannot be established") || !strings.Contains(d.Automation.Detail, "only the newest 1") {
		t.Fatalf("refusal not explained: %+v", d.Automation)
	}
}
