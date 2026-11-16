package arr

import (
	"context"
	"testing"
)

// Shapes follow released DownloadClientConfigResource (Sonarr v4.0.20.3014,
// Radarr v6.4.4.10685); the real instances' values were read by the
// integration suite's live check.
func TestRedownloadSettings(t *testing.T) {
	rs, err := serve(t, map[string]string{"/api/v3/config/downloadclient": `{"enableCompletedDownloadHandling":true,"autoRedownloadFailed":true,"autoRedownloadFailedFromInteractiveSearch":false,"id":1}`}).Redownload(context.Background())
	if err != nil || !rs.Failed || rs.FromInteractive == nil || *rs.FromInteractive {
		t.Fatalf("%+v %v", rs, err)
	}
	rs, err = serve(t, map[string]string{"/api/v3/config/downloadclient": `{"autoRedownloadFailed":false}`}).Redownload(context.Background())
	if err != nil || rs.Failed || rs.FromInteractive != nil {
		t.Fatalf("absent interactive flag must be unknown: %+v %v", rs, err)
	}
	for _, body := range []string{`{}`, `{"autoRedownloadFailed":null}`, `null`} {
		if _, err := serve(t, map[string]string{"/api/v3/config/downloadclient": body}).Redownload(context.Background()); err == nil {
			t.Fatalf("%s accepted", body)
		}
	}
}

func TestHistoryReleaseSource(t *testing.T) {
	body := `{"records":[{"id":9,"eventType":"grabbed","downloadId":"AA","date":"2026-11-16T19:00:00Z","data":{"releaseSource":"InteractiveSearch","indexer":"x"}},
		{"id":8,"eventType":"downloadFailed","downloadId":"AA","date":"2026-11-16T19:01:00Z","data":{}}]}`
	hs, err := serve(t, map[string]string{"/api/v3/history": body}).History(context.Background(), "aa")
	if err != nil || len(hs) != 2 || hs[0].ReleaseSource != "InteractiveSearch" || hs[1].ReleaseSource != "" {
		t.Fatalf("%+v %v", hs, err)
	}
}
