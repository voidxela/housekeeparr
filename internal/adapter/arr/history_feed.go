package arr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
)

// HistoryRecord is one manager history record of the manager-wide feed,
// with the acquisition facts the release-source scorecard needs. Only
// fields the manager actually supplies are kept; tracker URLs, download
// URLs and other data fields are never read.
type HistoryRecord struct {
	ID         int64     `json:"id"`
	EventType  string    `json:"event_type"`
	Date       time.Time `json:"date"`
	DownloadID string    `json:"download_id,omitempty"`
	// EpisodeID (Sonarr) or MovieID (Radarr) is the content the record is
	// for; a pack writes one record per episode.
	EpisodeID   int64  `json:"episode_id,omitempty"`
	MovieID     int64  `json:"movie_id,omitempty"`
	SourceTitle string `json:"source_title,omitempty"`
	// Indexer is the indexer's name as configured in the manager (a grab's
	// data.indexer), ReleaseGroup the release group the manager parsed,
	// ReleaseSource how the grab was found (Search, Rss, InteractiveSearch).
	// Each is "" when the record does not carry it.
	Indexer       string `json:"indexer,omitempty"`
	ReleaseGroup  string `json:"release_group,omitempty"`
	ReleaseSource string `json:"release_source,omitempty"`
}

// HistoryPage is one page of the manager-wide history, newest first.
type HistoryPage struct {
	Records []HistoryRecord
	// Total is the manager's totalRecords for the whole history.
	Total int
}

// HistoryFeed reads one page of the manager-wide history, newest first
// (GET /api/v3/history, sorted by id descending, so pages are stable for
// records that existed when the first page was read).
func (c *Client) HistoryFeed(ctx context.Context, page, pageSize int) (HistoryPage, error) {
	op := c.app + ".history"
	var pg struct {
		TotalRecords *int `json:"totalRecords"`
		Records      []struct {
			ID          *int64    `json:"id"`
			EventType   string    `json:"eventType"`
			SourceTitle string    `json:"sourceTitle"`
			DownloadID  string    `json:"downloadId"`
			EpisodeID   int64     `json:"episodeId"`
			MovieID     int64     `json:"movieId"`
			Date        time.Time `json:"date"`
			Data        struct {
				Indexer       string `json:"indexer"`
				ReleaseGroup  string `json:"releaseGroup"`
				ReleaseSource string `json:"releaseSource"`
			} `json:"data"`
		} `json:"records"`
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}, "sortKey": {"id"}, "sortDirection": {"descending"}}
	if err := c.Get(ctx, "history", "/api/v3/history", q, &pg); err != nil {
		return HistoryPage{}, err
	}
	if pg.Records == nil {
		return HistoryPage{}, httpx.Malformed(op, fmt.Errorf("missing records"))
	}
	if pg.TotalRecords == nil || *pg.TotalRecords < 0 {
		return HistoryPage{}, httpx.Malformed(op, fmt.Errorf("missing or negative totalRecords"))
	}
	out := HistoryPage{Total: *pg.TotalRecords}
	for _, r := range pg.Records {
		if r.ID == nil {
			return HistoryPage{}, httpx.Malformed(op, fmt.Errorf("record without id"))
		}
		out.Records = append(out.Records, HistoryRecord{ID: *r.ID, EventType: r.EventType, Date: r.Date, DownloadID: strings.ToUpper(r.DownloadID),
			EpisodeID: r.EpisodeID, MovieID: r.MovieID, SourceTitle: r.SourceTitle, Indexer: clean(r.Data.Indexer),
			ReleaseGroup: clean(r.Data.ReleaseGroup), ReleaseSource: clean(r.Data.ReleaseSource)})
	}
	return out, nil
}

// clean keeps a short display name: trimmed, without URLs or control
// characters (a name is never a credential, but a misconfigured indexer
// name could contain a URL).
func clean(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") || strings.ContainsAny(s, "?&=") {
		return "(redacted)"
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
