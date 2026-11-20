package sonarr

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// CoverageGroups lists the series (GET /api/v3/series).
func (c *Client) CoverageGroups(ctx context.Context) ([]domain.CoverageGroup, error) {
	var rows []struct {
		ID        *int64 `json:"id"`
		Title     string `json:"title"`
		Monitored *bool  `json:"monitored"`
	}
	if err := c.api.Get(ctx, "series", "/api/v3/series", nil, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, httpx.Malformed("sonarr.series", fmt.Errorf("series list is null"))
	}
	out := make([]domain.CoverageGroup, 0, len(rows))
	for _, r := range rows {
		if r.ID == nil || *r.ID <= 0 || r.Monitored == nil {
			return nil, httpx.Malformed("sonarr.series", fmt.Errorf("series without id or monitored"))
		}
		out = append(out, domain.CoverageGroup{ID: *r.ID, Title: r.Title, Monitored: *r.Monitored})
	}
	return out, nil
}

// CoverageItems lists one series' episodes (GET /api/v3/episode?seriesId=).
// An episode is wanted when it and its series are monitored and it has
// aired; an episode without an air date is not counted.
func (c *Client) CoverageItems(ctx context.Context, g domain.CoverageGroup, now time.Time) ([]domain.CoverageItem, error) {
	var rows []struct {
		ID            *int64  `json:"id"`
		SeasonNumber  int     `json:"seasonNumber"`
		EpisodeNumber int     `json:"episodeNumber"`
		AirDateUtc    *string `json:"airDateUtc"`
		Monitored     *bool   `json:"monitored"`
		HasFile       *bool   `json:"hasFile"`
	}
	if err := c.api.Get(ctx, "episode", "/api/v3/episode", url.Values{"seriesId": {fmt.Sprint(g.ID)}}, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, httpx.Malformed("sonarr.episode", fmt.Errorf("episode list is null"))
	}
	out := make([]domain.CoverageItem, 0, len(rows))
	for _, r := range rows {
		if r.ID == nil || *r.ID <= 0 || r.Monitored == nil || r.HasFile == nil {
			return nil, httpx.Malformed("sonarr.episode", fmt.Errorf("episode without id, monitored or hasFile"))
		}
		it := domain.CoverageItem{Key: fmt.Sprintf("episode:%d", *r.ID), GroupID: g.ID, HasFile: *r.HasFile,
			Label: fmt.Sprintf("%s S%02dE%02d", g.Title, r.SeasonNumber, r.EpisodeNumber)}
		if r.AirDateUtc != nil && *r.AirDateUtc != "" {
			if t, err := time.Parse(time.RFC3339, *r.AirDateUtc); err == nil {
				u := t.UTC()
				it.AiredAt = &u
			}
		}
		it.Wanted = g.Monitored && *r.Monitored && it.AiredAt != nil && !it.AiredAt.After(now)
		out = append(out, it)
	}
	return out, nil
}
