// Package sonarr is a read-only adapter for the Sonarr v3 HTTP API as
// served by Sonarr 4.x. It reads system status, the queue, and health.
package sonarr

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
	APIKey   string // resolved secret; sent only as a header
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

// New creates a Sonarr adapter.
func New(o Options) (*Client, error) {
	c, err := arr.New(arr.Options{App: "sonarr", BaseURL: o.BaseURL, APIKey: o.APIKey, Timeout: o.Timeout, CAFile: o.CAFile, PageSize: o.PageSize, MaxPages: o.MaxPages})
	if err != nil {
		return nil, err
	}
	return &Client{id: o.ID, api: c}, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindSonarr }

// MinimumMajor is the oldest supported Sonarr major version.
const MinimumMajor = 4

// TestedVersions lists released versions whose read path passed the
// disposable-instance integration suite (scripts/it/run.sh).
// Only add versions after a real, passing run.
var TestedVersions = []string{}

// Probe authenticates and detects version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	st, err := c.api.Status(ctx, "Sonarr")
	if err != nil {
		return adapter.Probe{}, err
	}
	p := adapter.Probe{App: "Sonarr", Version: st.Version, APIVersion: "v3", Transport: "HTTP API v3", Auth: "API key accepted"}
	p.Compatibility, p.CompatNote = arr.Compatibility("Sonarr", st.Version, MinimumMajor, TestedVersions)
	p.Read = adapter.Capabilities{
		adapter.CapQueue:         {Support: adapter.Supported, Note: fmt.Sprintf("paginated /api/v3/queue, up to %d × %d rows", c.api.MaxPages, c.api.PageSize)},
		adapter.CapHealth:        {Support: adapter.Supported, Note: "/api/v3/health"},
		adapter.CapImportHistory: {Support: adapter.NotImplemented, Note: "needed for partial season-pack handling; not used yet"},
	}
	p.Write = adapter.ReadOnlyWrites(adapter.CapFailGrab, adapter.CapDetachQueue, adapter.CapSearchCommand)
	p.Limitations = []string{"Import diagnostics are Sonarr's own messages; Housekeeparr does not yet verify import history or files."}
	return p, nil
}

type record struct {
	arr.Record
	SeriesID     *int64 `json:"seriesId"`
	EpisodeID    *int64 `json:"episodeId"`
	SeasonNumber *int64 `json:"seasonNumber"`
	Series       *struct {
		Title string `json:"title"`
	} `json:"series"`
	Episode *struct {
		SeasonNumber  *int64 `json:"seasonNumber"`
		EpisodeNumber *int64 `json:"episodeNumber"`
		Title         string `json:"title"`
	} `json:"episode"`
}

func normalize(raw json.RawMessage) (domain.QueueItem, error) {
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return domain.QueueItem{}, err
	}
	q := r.Common()
	q.ContentKind = "episode"
	q.SeasonNumber = domain.UnknownValue[int64]("not reported")
	q.EpisodeNumber = domain.UnknownValue[int64]("not reported")
	if r.SeriesID != nil {
		q.SeriesID = *r.SeriesID
	}
	if r.EpisodeID != nil {
		q.EpisodeID = *r.EpisodeID
	}
	if r.Series != nil {
		q.SeriesTitle = r.Series.Title
	}
	if r.SeasonNumber != nil {
		q.SeasonNumber = domain.KnownValue(*r.SeasonNumber)
	}
	if r.Episode != nil {
		if r.Episode.EpisodeNumber != nil {
			q.EpisodeNumber = domain.KnownValue(*r.Episode.EpisodeNumber)
		}
		if r.Episode.SeasonNumber != nil {
			q.SeasonNumber = domain.KnownValue(*r.Episode.SeasonNumber)
		}
		q.EpisodeTitle = r.Episode.Title
	}
	return q, nil
}

// Queue reads the full queue within configured pagination bounds.
func (c *Client) Queue(ctx context.Context) (domain.QueueSnapshot, error) {
	return c.api.Queue(ctx, c.id, url.Values{"includeUnknownSeriesItems": {"true"}, "includeSeries": {"true"}, "includeEpisode": {"true"}}, normalize)
}

// Health reads Sonarr's health checks.
func (c *Client) Health(ctx context.Context) (domain.HealthSnapshot, error) {
	return c.api.Health(ctx, c.id)
}
