package deluge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFilesListsSelection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)
		switch req.Method {
		case "auth.login":
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "s", Path: "/"})
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true, "error": nil, "id": req.ID})
		case "core.get_torrent_status":
			_, _ = w.Write([]byte(`{"id":1,"error":null,"result":{"files":[
				{"index":0,"path":"Show.S01/Show.S01E01.rar","size":500,"offset":0},
				{"index":1,"path":"Show.S01/Show.S01E01.r00","size":500,"offset":500}],"file_priorities":[1,0]}}`))
		default:
			t.Errorf("unexpected method %s", req.Method)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": nil, "id": req.ID})
		}
	}))
	defer srv.Close()
	c, err := New(Options{ID: "deluge", BaseURL: srv.URL, Password: "pw", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := c.Files(context.Background(), "aa")
	if err != nil || len(fs) != 2 || fs[0].Path != "Show.S01/Show.S01E01.rar" {
		t.Fatalf("%+v %v", fs, err)
	}
	if w, ok := fs[1].Wanted.Get(); !ok || w {
		t.Errorf("skipped file %+v", fs[1])
	}
}
