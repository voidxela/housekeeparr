package qbittorrent

import (
	"context"
	"testing"
)

// Priority 0 is "do not download"; a missing name or size is malformed.
func TestFilesListsSelection(t *testing.T) {
	f := &fakeQBit{version: "v5.3.0", webapi: "2.11.4", torrents: []byte("[]"), user: "admin", pass: "pw", files: map[string][]byte{
		"aa": []byte(`[{"index":0,"name":"Show.S01E01/Show.S01E01.mkv","size":1000,"progress":1,"priority":1},
			{"index":1,"name":"Show.S01E01/Sample/sample.mkv","size":10,"progress":0,"priority":0}]`),
		"bad": []byte(`[{"index":0,"size":1000,"priority":1}]`),
	}}
	c := setup(t, f)
	fs, err := c.Files(context.Background(), "aa")
	if err != nil || len(fs) != 2 || fs[0].Path != "Show.S01E01/Show.S01E01.mkv" {
		t.Fatalf("%+v %v", fs, err)
	}
	if w, ok := fs[1].Wanted.Get(); !ok || w {
		t.Errorf("priority 0 file %+v", fs[1])
	}
	if _, err := c.Files(context.Background(), "bad"); err == nil {
		t.Error("a file without a name was accepted")
	}
}
