package deluge

import (
	"context"
	"fmt"

	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Files lists a torrent's files (core.get_torrent_status with "files" and
// "file_priorities"). Priority 0 means "skip".
func (c *Client) Files(ctx context.Context, nativeID string) ([]domain.TransferFile, error) {
	var res struct {
		Files []struct {
			Path *string `json:"path"`
			Size *int64  `json:"size"`
		} `json:"files"`
		Priorities []int `json:"file_priorities"`
	}
	if err := c.call(ctx, "core.get_torrent_status", []any{nativeID, []string{"files", "file_priorities"}}, &res); err != nil {
		return nil, err
	}
	if res.Files == nil {
		return nil, httpx.Malformed("deluge.core.get_torrent_status", fmt.Errorf("no file list (unknown torrent?)"))
	}
	known := len(res.Priorities) == len(res.Files)
	out := make([]domain.TransferFile, 0, len(res.Files))
	for i, f := range res.Files {
		if f.Path == nil || f.Size == nil || *f.Size < 0 {
			return nil, httpx.Malformed("deluge.core.get_torrent_status", fmt.Errorf("file without path or size"))
		}
		tf := domain.TransferFile{Path: *f.Path, Size: *f.Size, Wanted: domain.UnknownValue[bool]("file priorities not reported")}
		if known {
			tf.Wanted = domain.KnownValue(res.Priorities[i] != 0)
		}
		out = append(out, tf)
	}
	return out, nil
}
