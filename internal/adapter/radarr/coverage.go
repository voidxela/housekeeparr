package radarr

import (
	"context"
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// CoverageGroups is one group: the whole movie list.
func (c *Client) CoverageGroups(context.Context) ([]domain.CoverageGroup, error) {
	return []domain.CoverageGroup{{ID: 0, Title: "Movies", Monitored: true}}, nil
}

// CoverageItems lists every movie (GET /api/v3/movie). A movie is wanted
// when it is monitored and Radarr reports it available.
func (c *Client) CoverageItems(ctx context.Context, g domain.CoverageGroup, _ time.Time) ([]domain.CoverageItem, error) {
	var rows []struct {
		ID          *int64 `json:"id"`
		Title       string `json:"title"`
		Year        int    `json:"year"`
		Monitored   *bool  `json:"monitored"`
		IsAvailable *bool  `json:"isAvailable"`
		HasFile     *bool  `json:"hasFile"`
	}
	if err := c.api.Get(ctx, "movie", "/api/v3/movie", nil, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, httpx.Malformed("radarr.movie", fmt.Errorf("movie list is null"))
	}
	out := make([]domain.CoverageItem, 0, len(rows))
	for _, r := range rows {
		if r.ID == nil || *r.ID <= 0 || r.Monitored == nil || r.IsAvailable == nil || r.HasFile == nil {
			return nil, httpx.Malformed("radarr.movie", fmt.Errorf("movie without id, monitored, isAvailable or hasFile"))
		}
		out = append(out, domain.CoverageItem{Key: fmt.Sprintf("movie:%d", *r.ID), GroupID: g.ID, Label: fmt.Sprintf("%s (%d)", r.Title, r.Year),
			HasFile: *r.HasFile, Wanted: *r.Monitored && *r.IsAvailable})
	}
	return out, nil
}
