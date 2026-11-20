package transmission

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Files reads one torrent's files and their selection with torrent-get, a
// read method; a deselected file is reported as not wanted.
func TestFilesListsSelection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != "sid" {
			w.Header().Set("X-Transmission-Session-Id", "sid")
			w.WriteHeader(http.StatusConflict)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method    string `json:"method"`
			Arguments struct {
				IDs    []string `json:"ids"`
				Fields []string `json:"fields"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method != "torrent-get" || len(req.Arguments.IDs) != 1 || req.Arguments.IDs[0] != "aa" {
			t.Errorf("request %+v", req)
		}
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrents":[{"files":[
			{"name":"Show.S01E01/Show.S01E01.mkv","length":1000,"bytesCompleted":1000},
			{"name":"Show.S01E01/Sample/sample.mkv","length":10,"bytesCompleted":0}],
			"fileStats":[{"wanted":true,"priority":0,"bytesCompleted":1000},{"wanted":false,"priority":0,"bytesCompleted":0}]}]}}`))
	}))
	defer srv.Close()
	c, err := New(Options{ID: "tr", BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := c.Files(context.Background(), "aa")
	if err != nil || len(fs) != 2 {
		t.Fatalf("%+v %v", fs, err)
	}
	if w, ok := fs[0].Wanted.Get(); fs[0].Path != "Show.S01E01/Show.S01E01.mkv" || fs[0].Size != 1000 || !ok || !w {
		t.Errorf("first %+v", fs[0])
	}
	if w, ok := fs[1].Wanted.Get(); !ok || w {
		t.Errorf("deselected file %+v", fs[1])
	}
}
