package qbittorrent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Files lists a torrent's files (GET /api/v2/torrents/files). Priority 0
// means "do not download".
func (c *Client) Files(ctx context.Context, nativeID string) ([]domain.TransferFile, error) {
	body, err := c.get(ctx, "qbittorrent.torrents_files", "/api/v2/torrents/files", url.Values{"hash": {nativeID}})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name     *string `json:"name"`
		Size     *int64  `json:"size"`
		Priority *int    `json:"priority"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, httpx.Malformed("qbittorrent.torrents_files", err)
	}
	if rows == nil {
		return nil, httpx.Malformed("qbittorrent.torrents_files", fmt.Errorf("file list is null"))
	}
	out := make([]domain.TransferFile, 0, len(rows))
	for _, r := range rows {
		if r.Name == nil || r.Size == nil || *r.Size < 0 {
			return nil, httpx.Malformed("qbittorrent.torrents_files", fmt.Errorf("file without name or size"))
		}
		f := domain.TransferFile{Path: strings.ReplaceAll(*r.Name, "\\", "/"), Size: *r.Size, Wanted: domain.UnknownValue[bool]("priority not reported")}
		if r.Priority != nil {
			f.Wanted = domain.KnownValue(*r.Priority != 0)
		}
		out = append(out, f)
	}
	return out, nil
}
