//go:build integration

package integration

// The manager-wide history feed the release-source scorecard reads, on
// released Sonarr and Radarr after the recovery workflows have left real
// grab history: pages sorted by id descend without overlap, a grab names
// the stand-in indexer as the manager reports it (data.indexer) with its
// release source, and nothing URL-like is kept.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/adapter/sonarr"
)

type feedReader interface {
	HistoryFeed(ctx context.Context, page, pageSize int) (arr.HistoryPage, error)
}

func checkHistoryFeed(t *testing.T, r feedReader) {
	ctx := context.Background()
	var all []arr.HistoryRecord
	total := -1
	for page := 1; ; page++ {
		pg, err := r.HistoryFeed(ctx, page, 5)
		if err != nil {
			t.Fatal(err)
		}
		if total < 0 {
			total = pg.Total
		} else if pg.Total != total {
			t.Fatalf("totalRecords changed while paging: %d then %d", total, pg.Total)
		}
		all = append(all, pg.Records...)
		if len(pg.Records) < 5 || page > 200 {
			break
		}
	}
	if len(all) != total || total == 0 {
		t.Fatalf("read %d records of %d", len(all), total)
	}
	grabs := 0
	for i, rec := range all {
		if i > 0 && rec.ID >= all[i-1].ID {
			t.Fatalf("records not in descending id order across pages: %d after %d", rec.ID, all[i-1].ID)
		}
		if strings.Contains(rec.Indexer+rec.ReleaseGroup+rec.ReleaseSource, "://") {
			t.Fatalf("URL kept: %+v", rec)
		}
		if rec.EventType == "grabbed" {
			grabs++
			if rec.Indexer != "Housekeeparr stand-in" || rec.ReleaseSource == "" || rec.DownloadID == "" {
				t.Errorf("grab record %+v: want the stand-in indexer, a release source and a download id", rec)
			}
		}
	}
	if grabs == 0 {
		t.Fatal("no grab in the history; run after the recovery workflows")
	}
	t.Logf("%d records, %d grabs; newest: %+v", len(all), grabs, all[0])
}

func TestSonarrHistoryFeed(t *testing.T) {
	c, err := sonarr.New(sonarr.Options{ID: "sonarr", BaseURL: env(t, "HK_IT_SONARR_URL"), APIKey: env(t, "HK_IT_SONARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	checkHistoryFeed(t, c)
}

func TestRadarrHistoryFeed(t *testing.T) {
	c, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	checkHistoryFeed(t, c)
}
