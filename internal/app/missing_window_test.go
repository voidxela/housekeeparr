package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
)

// A wanted list longer than search.max_missing_items is read in rotating
// windows: an available item beyond the first window is read and searched
// within a few reads instead of never, while the incomplete reads still
// never mark unlisted items gone and the finding explains the rotation.
func TestMissingListBeyondTheBoundRotates(t *testing.T) {
	w := searchWorld(t, "automatic")
	var items []domain.MissingItem
	for id := int64(1); id <= 1001; id++ {
		it := fake.MissingEpisode(id+1000, 3, "Unavailable episode", t0.Add(-time.Hour))
		it.Available = domain.KnownValue(false)
		items = append(items, it)
	}
	items[1000] = fake.MissingEpisode(2001, 3, "Last episode", t0.Add(-72*time.Hour))
	w.mgr.SetMissing(items...)
	w.runSearches(10 * time.Minute)
	ctx := context.Background()
	fs, _ := w.st.Findings(ctx, store.FindingFilter{Type: "missing.incomplete"})
	if len(fs) != 1 || !strings.Contains(fs[0].Summary, "rotate") {
		t.Fatalf("incomplete-list finding %+v", fs)
	}
	w.runSearches(3 * time.Hour) // reads every hour: windows 2 and 3
	got := w.mgr.Searches()
	if len(got) != 1 || got[0][0] != 2001 {
		t.Fatalf("searches %v, want the item beyond the first two windows", got)
	}
	// Nothing was marked gone by the partial windows.
	for _, id := range []string{"episode:1001", "episode:1500", "episode:2001"} {
		recs, _ := w.st.MissingContent(ctx, store.MissingFilter{ManagerID: "sonarr", PresentOnly: true, Limit: 2000})
		found := false
		for _, r := range recs {
			found = found || r.ContentKey == id
		}
		if !found {
			t.Fatalf("%s marked gone by a partial window", id)
		}
	}
}
