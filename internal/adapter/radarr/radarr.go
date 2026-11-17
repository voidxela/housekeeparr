// Package radarr is a read-only adapter for the Radarr v3 HTTP API as
// served by Radarr 5.x and 6.x. It reads system status, the queue, and
// health.
package radarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Options configure the adapter.
type Options struct {
	ID       string
	BaseURL  string
	APIKey   string
	Timeout  time.Duration
	CAFile   string
	PageSize int
	MaxPages int
}

// Client implements adapter.MediaManager.
type Client struct {
	id  string
	api *arr.Client
}

// New creates a Radarr adapter.
func New(o Options) (*Client, error) {
	c, err := arr.New(arr.Options{App: "radarr", BaseURL: o.BaseURL, APIKey: o.APIKey, Timeout: o.Timeout, CAFile: o.CAFile, PageSize: o.PageSize, MaxPages: o.MaxPages})
	if err != nil {
		return nil, err
	}
	return &Client{id: o.ID, api: c}, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindRadarr }

// MinimumMajor is the oldest supported Radarr major version.
const MinimumMajor = 5

// TestedVersions lists released versions whose read path passed the
// disposable-instance integration suite. Only add versions after a real,
// passing run.
//
// 6.4.4.10685 (linux-core-arm64) passed on 2026-11-15; see docs/compatibility.md.
var TestedVersions = []string{"6.4.4.10685"}

// Probe authenticates and detects version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	st, err := c.api.Status(ctx, "Radarr")
	if err != nil {
		return adapter.Probe{}, err
	}
	p := adapter.Probe{App: "Radarr", Version: st.Version, APIVersion: "v3", Transport: "HTTP API v3", Auth: "API key accepted"}
	p.Compatibility, p.CompatNote = arr.Compatibility("Radarr", st.Version, MinimumMajor, TestedVersions)
	p.Read = adapter.Capabilities{
		adapter.CapQueue:         {Support: adapter.Supported, Note: fmt.Sprintf("paginated /api/v3/queue, up to %d × %d rows", c.api.MaxPages, c.api.PageSize)},
		adapter.CapHealth:        {Support: adapter.Supported, Note: "/api/v3/health"},
		adapter.CapImportHistory: {Support: adapter.NotImplemented, Note: "not used yet"},
	}
	p.Write = adapter.Capabilities{
		adapter.CapFailGrab:      {Support: adapter.Supported, Note: "DELETE /api/v3/queue/{id} with every flag explicit; complete recovery workflow tested with qBittorrent 5.3.0, Transmission 4.0.6/4.1.4, Deluge 2.2.0, and rTorrent 0.9.8 (docs/compatibility.md)"},
		adapter.CapDetachQueue:   {Support: adapter.Supported, Note: "same request with blocklist=false; durable only for rows with grab history"},
		adapter.CapSearchCommand: {Support: adapter.NotImplemented, Note: "replacement searches are left to the manager (skipRedownload=false)"},
	}
	p.Limitations = []string{"Import diagnostics are Radarr's own messages; Housekeeparr does not yet verify import history or files."}
	return p, nil
}

type record struct {
	arr.Record
	MovieID *int64 `json:"movieId"`
	Movie   *struct {
		Title string `json:"title"`
		Year  *int64 `json:"year"`
	} `json:"movie"`
}

func normalize(raw json.RawMessage) (domain.QueueItem, error) {
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return domain.QueueItem{}, err
	}
	q := r.Common()
	q.ContentKind = "movie"
	q.Year = domain.UnknownValue[int64]("not reported")
	if r.MovieID != nil {
		q.MovieID = *r.MovieID
	}
	if r.Movie != nil {
		q.MovieTitle = r.Movie.Title
		if r.Movie.Year != nil && *r.Movie.Year > 0 {
			q.Year = domain.KnownValue(*r.Movie.Year)
		}
	}
	return q, nil
}

// Queue reads the full queue within configured pagination bounds.
func (c *Client) Queue(ctx context.Context) (domain.QueueSnapshot, error) {
	return c.api.Queue(ctx, c.id, url.Values{"includeUnknownMovieItems": {"true"}, "includeMovie": {"true"}}, normalize)
}

// Health reads Radarr's health checks.
func (c *Client) Health(ctx context.Context) (domain.HealthSnapshot, error) {
	return c.api.Health(ctx, c.id)
}

// Sources reads Radarr's configured indexers and their failure status.
func (c *Client) Sources(ctx context.Context) (domain.SourceSnapshot, error) {
	return c.api.Sources(ctx, c.id)
}

// RemoveQueueItem removes a queue row with every flag explicit.
func (c *Client) RemoveQueueItem(ctx context.Context, queueID int64, f arr.RemoveFlags) error {
	return c.api.RemoveQueueItem(ctx, queueID, f)
}

// BlocklistEntries returns matching blocklist entry IDs (see
// arr.BlocklistMatch for why this is evidence rather than hash identity).
func (c *Client) BlocklistEntries(ctx context.Context, m arr.BlocklistMatch) ([]int64, error) {
	return c.api.BlocklistEntries(ctx, m)
}

// Blocklisted reports whether a matching blocklist entry exists.
func (c *Client) Blocklisted(ctx context.Context, m arr.BlocklistMatch) (bool, error) {
	return c.api.Blocklisted(ctx, m)
}

// History lists history records for a download id, newest first.
func (c *Client) History(ctx context.Context, downloadID string) ([]arr.HistoryEvent, error) {
	return c.api.History(ctx, downloadID)
}

// Commands lists the manager's queued, running, and recently finished
// commands (read-only).
func (c *Client) Commands(ctx context.Context) ([]arr.Command, error) { return c.api.Commands(ctx) }

// Redownload reads whether the manager searches again after a failed grab.
func (c *Client) Redownload(ctx context.Context) (arr.RedownloadSettings, error) {
	return c.api.Redownload(ctx)
}

// Content reports whether a movie ("movie:<id>") exists and is monitored.
func (c *Client) Content(ctx context.Context, key string) (arr.Content, error) {
	kind, id, ok := domain.ParseContentKey(key)
	if !ok || kind != "movie" {
		return arr.Content{}, fmt.Errorf("radarr content key %q is not a movie", key)
	}
	found, mon, raw, err := c.api.Monitored(ctx, "movie", fmt.Sprintf("/api/v3/movie/%d", id))
	if err != nil {
		return arr.Content{}, err
	}
	out := arr.Content{Key: key, Exists: found, Monitored: found && mon}
	if err := json.Unmarshal(raw["hasFile"], &out.HasFile); err != nil {
		out.HasFile = nil
	}
	if err := json.Unmarshal(raw["isAvailable"], &out.Available); err != nil {
		out.Available = nil
	}
	switch {
	case !found:
		out.Detail = "the movie no longer exists in Radarr"
	case !mon:
		out.Detail = "the movie is unmonitored"
	default:
		out.Detail = "the movie is monitored"
	}
	return out, nil
}

type missingRecord struct {
	ID                  *int64  `json:"id"`
	Title               string  `json:"title"`
	Year                int     `json:"year"`
	Monitored           *bool   `json:"monitored"`
	HasFile             bool    `json:"hasFile"`
	IsAvailable         *bool   `json:"isAvailable"`
	MinimumAvailability string  `json:"minimumAvailability"`
	InCinemas           *string `json:"inCinemas"`
	DigitalRelease      *string `json:"digitalRelease"`
	PhysicalRelease     *string `json:"physicalRelease"`
}

func normalizeMissing(raw json.RawMessage) (domain.MissingItem, error) {
	var r missingRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return domain.MissingItem{}, err
	}
	if r.ID == nil || *r.ID <= 0 || r.Monitored == nil {
		return domain.MissingItem{}, fmt.Errorf("missing movie record without valid id or monitored")
	}
	if r.HasFile {
		return domain.MissingItem{}, nil
	}
	title := r.Title
	if r.Year > 0 {
		title = fmt.Sprintf("%s (%d)", r.Title, r.Year)
	}
	avail := domain.UnknownValue[bool]("availability not reported")
	if r.IsAvailable != nil {
		avail = domain.KnownValue(*r.IsAvailable)
	}
	var airedAt *time.Time
	for _, rawDate := range []*string{r.DigitalRelease, r.PhysicalRelease, r.InCinemas} {
		if rawDate == nil || *rawDate == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, *rawDate); err == nil {
			utc := t.UTC()
			if airedAt == nil || utc.Before(*airedAt) {
				airedAt = &utc
			}
		}
	}
	return domain.MissingItem{
		ContentKind: "movie",
		ContentID:   *r.ID,
		Title:       title,
		Monitored:   *r.Monitored,
		AiredAt:     airedAt,
		Available:   avail,
	}, nil
}

// Missing reads monitored, missing movies up to maxItems (or the configured
// page limit when maxItems <= 0).
func (c *Client) Missing(ctx context.Context, maxItems int) (domain.MissingSnapshot, error) {
	q := url.Values{"sortKey": {"movieMetadata.sortTitle"}, "sortDirection": {"ascending"}}
	return c.api.Missing(ctx, c.id, q, maxItems, normalizeMissing)
}

// SearchMovies triggers a Radarr MoviesSearch command for 1 to 10 movie IDs.
func (c *Client) SearchMovies(ctx context.Context, movieIDs []int64) (int64, error) {
	return c.api.Search(ctx, "MoviesSearch", "movieIds", movieIDs)
}
