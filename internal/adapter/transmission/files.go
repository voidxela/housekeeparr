package transmission

import (
	"context"
	"fmt"

	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Files lists a torrent's files (torrent-get "files" and "fileStats" for
// one hash).
func (c *Client) Files(ctx context.Context, nativeID string) ([]domain.TransferFile, error) {
	var args struct {
		Torrents []struct {
			Files []struct {
				Name   *string `json:"name"`
				Length *int64  `json:"length"`
			} `json:"files"`
			FileStats []struct {
				Wanted *bool `json:"wanted"`
			} `json:"fileStats"`
		} `json:"torrents"`
	}
	if err := c.call(ctx, "torrent-get", map[string]any{"ids": []string{nativeID}, "fields": []string{"files", "fileStats"}}, &args); err != nil {
		return nil, err
	}
	if len(args.Torrents) != 1 {
		return nil, httpx.Malformed("transmission.torrent-get", fmt.Errorf("%d torrents for one hash", len(args.Torrents)))
	}
	tr := args.Torrents[0]
	stats := len(tr.FileStats) == len(tr.Files)
	out := make([]domain.TransferFile, 0, len(tr.Files))
	for i, f := range tr.Files {
		if f.Name == nil || f.Length == nil || *f.Length < 0 {
			return nil, httpx.Malformed("transmission.torrent-get", fmt.Errorf("file without name or length"))
		}
		tf := domain.TransferFile{Path: *f.Name, Size: *f.Length, Wanted: domain.UnknownValue[bool]("fileStats not reported")}
		if stats && tr.FileStats[i].Wanted != nil {
			tf.Wanted = domain.KnownValue(*tr.FileStats[i].Wanted)
		}
		out = append(out, tf)
	}
	return out, nil
}
