package rtorrent

import (
	"context"
	"testing"
	"time"
)

func TestFilesListsSelection(t *testing.T) {
	resp := `<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><array><data><value><string>Show.S01E01.mkv</string></value><value><i8>1000</i8></value><value><i8>1</i8></value></data></array></value>
<value><array><data><value><string>Sample/sample.mkv</string></value><value><i8>10</i8></value><value><i8>0</i8></value></data></array></value>
</data></array></value></param></params></methodResponse>`
	srv := server(t, "", "", map[string]string{"f.multicall": resp})
	defer srv.Close()
	c, err := New(Options{ID: "rt", BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := c.Files(context.Background(), "AA")
	if err != nil || len(fs) != 2 || fs[0].Size != 1000 {
		t.Fatalf("%+v %v", fs, err)
	}
	if w, ok := fs[1].Wanted.Get(); !ok || w {
		t.Errorf("priority-off file %+v", fs[1])
	}
}
