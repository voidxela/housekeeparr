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
	p.Write = adapter.ReadOnlyWrites(adapter.CapFailGrab, adapter.CapDetachQueue, adapter.CapSearchCommand)
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
