package rtorrent

import (
	"context"
	"fmt"

	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Files lists a download's files (f.multicall with path, size and
// priority). Priority 0 means "off".
func (c *Client) Files(ctx context.Context, nativeID string) ([]domain.TransferFile, error) {
	v, err := c.call(ctx, "f.multicall", nativeID, "", "f.path=", "f.size_bytes=", "f.priority=")
	if err != nil {
		return nil, err
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, httpx.Malformed("rtorrent.f.multicall", fmt.Errorf("file list is not an array"))
	}
	out := make([]domain.TransferFile, 0, len(rows))
	for _, r := range rows {
		cols, ok := r.([]any)
		if !ok || len(cols) != 3 {
			return nil, httpx.Malformed("rtorrent.f.multicall", fmt.Errorf("malformed file row"))
		}
		path, ok1 := cols[0].(string)
		size, ok2 := cols[1].(int64)
		prio, ok3 := cols[2].(int64)
		if !ok1 || !ok2 || !ok3 || size < 0 {
			return nil, httpx.Malformed("rtorrent.f.multicall", fmt.Errorf("malformed file row"))
		}
		out = append(out, domain.TransferFile{Path: path, Size: size, Wanted: domain.KnownValue(prio != 0)})
	}
	return out, nil
}
