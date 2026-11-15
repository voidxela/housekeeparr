// Package sonarr is a read-only adapter for the Sonarr v3 HTTP API as
// served by Sonarr 4.x. It reads system status, the queue, and health.
package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/redact"
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
	id       string
	http     *httpx.Client
	apiKey   string
	pageSize int
	maxPages int
}

// New creates a Sonarr adapter.
func New(o Options) (*Client, error) {
	hc, err := httpx.New(o.BaseURL, httpx.Options{
		Timeout: o.Timeout, CAFile: o.CAFile, MaxResponseBytes: 32 << 20, UserAgent: "Housekeeparr (read-only)",
	})
	if err != nil {
		return nil, err
	}
	if o.PageSize <= 0 {
		o.PageSize = 200
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 25
	}
	return &Client{id: o.ID, http: hc, apiKey: o.APIKey, pageSize: o.PageSize, maxPages: o.MaxPages}, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindSonarr }

func (c *Client) get(ctx context.Context, op, path string, q url.Values, out any) error {
	resp, err := c.http.Do(ctx, httpx.Request{Op: op, Method: http.MethodGet, Path: path, Query: q,
		Header: http.Header{"X-Api-Key": {c.apiKey}}})
	if err != nil {
		return err
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return &adapter.Error{Class: adapter.ErrPermanent, Op: op,
			Message: fmt.Sprintf("expected JSON but received %q (wrong base path or an authentication proxy?)", ct)}
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return httpx.Malformed(op, err)
	}
	return nil
}

type systemStatus struct {
	AppName      string `json:"appName"`
	InstanceName string `json:"instanceName"`
	Version      string `json:"version"`
}

// MinimumMajor is the oldest supported Sonarr major version.
const MinimumMajor = 4

// TestedVersions lists released versions whose read path passed the
// integration suite (scripts/it). Only add versions after a real run.
var TestedVersions = []string{}

// Probe authenticates and detects version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	var st systemStatus
	if err := c.get(ctx, "sonarr.system_status", "/api/v3/system/status", nil, &st); err != nil {
		return adapter.Probe{}, err
	}
	if !strings.EqualFold(st.AppName, "Sonarr") {
		return adapter.Probe{}, &adapter.Error{Class: adapter.ErrUnsupported, Op: "sonarr.system_status",
			Message: fmt.Sprintf("application reports itself as %q, not Sonarr", redact.Truncate(st.AppName, 40))}
	}
	p := adapter.Probe{App: "Sonarr", Version: st.Version, APIVersion: "v3", Transport: "HTTP API v3", Auth: "API key accepted"}
	major, _ := strconv.Atoi(strings.SplitN(st.Version, ".", 2)[0])
	switch {
	case major < MinimumMajor:
		p.Compatibility, p.CompatNote = adapter.CompatUnsupported, fmt.Sprintf("Sonarr %d.x is not supported; Housekeeparr requires Sonarr %d or later", major, MinimumMajor)
	case contains(TestedVersions, st.Version):
		p.Compatibility, p.CompatNote = adapter.CompatTested, "read workflow validated against this released version"
	default:
		p.Compatibility, p.CompatNote = adapter.CompatUntested, "observation proceeds; this version has not been validated by the integration suite"
	}
	p.Read = adapter.Capabilities{
		adapter.CapQueue:         {Support: adapter.Supported, Note: fmt.Sprintf("paginated /api/v3/queue, up to %d × %d rows", c.maxPages, c.pageSize)},
		adapter.CapHealth:        {Support: adapter.Supported, Note: "/api/v3/health"},
		adapter.CapImportHistory: {Support: adapter.NotImplemented, Note: "needed for partial season-pack handling; not used yet"},
	}
	p.Write = adapter.ReadOnlyWrites(adapter.CapFailGrab, adapter.CapDetachQueue, adapter.CapSearchCommand)
	p.Limitations = []string{
		"Import diagnostics are Sonarr's own messages; Housekeeparr does not yet verify import history or files.",
	}
	return p, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type page struct {
	Page         int           `json:"page"`
	PageSize     int           `json:"pageSize"`
	TotalRecords int           `json:"totalRecords"`
	Records      []queueRecord `json:"records"`
}

type queueRecord struct {
	ID           int64  `json:"id"`
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
	Title                 string   `json:"title"`
	Size                  *float64 `json:"size"`
	SizeLeft              *float64 `json:"sizeleft"`
	Added                 *string  `json:"added"`
	Status                string   `json:"status"`
	TrackedDownloadStatus *string  `json:"trackedDownloadStatus"`
	TrackedDownloadState  *string  `json:"trackedDownloadState"`
	StatusMessages        []struct {
		Title    string   `json:"title"`
		Messages []string `json:"messages"`
	} `json:"statusMessages"`
	ErrorMessage   string `json:"errorMessage"`
	DownloadID     string `json:"downloadId"`
	Protocol       string `json:"protocol"`
	DownloadClient string `json:"downloadClient"`
	Indexer        string `json:"indexer"`
	// outputPath is deliberately not decoded.
}

// Queue reads the full queue within configured pagination bounds. If the
// bound is reached or the queue changes between pages, the snapshot is
// marked incomplete so absence cannot be inferred from it.
func (c *Client) Queue(ctx context.Context) (domain.QueueSnapshot, error) {
	snap := domain.QueueSnapshot{ManagerID: c.id, Complete: true}
	seen := map[int64]bool{}
	total := -1
	for p := 1; p <= c.maxPages; p++ {
		q := url.Values{
			"page": {strconv.Itoa(p)}, "pageSize": {strconv.Itoa(c.pageSize)},
			"sortKey": {"added"}, "sortDirection": {"ascending"},
			"includeUnknownSeriesItems": {"true"}, "includeSeries": {"true"}, "includeEpisode": {"true"},
		}
		var pg page
		if err := c.get(ctx, "sonarr.queue", "/api/v3/queue", q, &pg); err != nil {
			return domain.QueueSnapshot{}, err
		}
		if p == 1 {
			snap.CollectedAt = time.Now().UTC()
			total = pg.TotalRecords
		} else if pg.TotalRecords != total {
			snap.Complete = false
			snap.Limitations = append(snap.Limitations, "The queue changed while it was being read; absent rows are not treated as removed this cycle.")
		}
		for _, r := range pg.Records {
			if seen[r.ID] {
				snap.Complete = false
				continue
			}
			seen[r.ID] = true
			snap.Items = append(snap.Items, normalize(c.id, r))
		}
		if len(pg.Records) < c.pageSize || len(snap.Items) >= total {
			break
		}
		if p == c.maxPages {
			snap.Complete = false
			snap.Limitations = append(snap.Limitations, fmt.Sprintf("Queue has %d rows; only the first %d were read (max_queue_pages × queue_page_size).", total, len(snap.Items)))
		}
	}
	snap.TotalRecords = total
	return snap, nil
}

func normalize(managerID string, r queueRecord) domain.QueueItem {
	q := domain.QueueItem{
		ManagerID: managerID, QueueID: r.ID, DownloadID: domain.NormalizeHash(r.DownloadID),
		Protocol: r.Protocol, DownloadClient: r.DownloadClient, Indexer: r.Indexer, Title: r.Title,
		ContentKind: "episode", Status: r.Status, ErrorMessage: redact.Truncate(redact.String(r.ErrorMessage), 500),
	}
	if r.SeriesID != nil {
		q.SeriesID = *r.SeriesID
	}
	if r.EpisodeID != nil {
		q.EpisodeID = *r.EpisodeID
	}
	if r.Series != nil {
		q.SeriesTitle = r.Series.Title
	}
	q.SeasonNumber = domain.UnknownValue[int64]("not reported")
	q.EpisodeNumber = domain.UnknownValue[int64]("not reported")
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
	q.TrackedState = optString(r.TrackedDownloadState)
	q.TrackedStatus = optString(r.TrackedDownloadStatus)
	for _, m := range r.StatusMessages {
		msgs := make([]string, 0, len(m.Messages))
		for _, s := range m.Messages {
			msgs = append(msgs, redact.Truncate(redact.String(s), 300))
		}
		q.StatusMessages = append(q.StatusMessages, domain.StatusMessage{Title: redact.Truncate(m.Title, 200), Messages: msgs})
	}
	if r.Size != nil {
		q.SizeBytes = domain.KnownValue(int64(*r.Size))
	} else {
		q.SizeBytes = domain.UnknownValue[int64]("not reported")
	}
	if r.SizeLeft != nil {
		q.RemainingBytes = domain.KnownValue(int64(*r.SizeLeft))
	} else {
		q.RemainingBytes = domain.UnknownValue[int64]("not reported")
	}
	q.Added = domain.UnknownValue[time.Time]("not reported")
	if r.Added != nil {
		if t, err := time.Parse(time.RFC3339, *r.Added); err == nil {
			q.Added = domain.KnownValue(t.UTC())
		}
	}
	return q
}

func optString(p *string) domain.Signal[string] {
	if p == nil {
		return domain.UnknownValue[string]("not reported")
	}
	return domain.KnownValue(*p)
}

type healthRecord struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

// Health reads Sonarr's health checks.
func (c *Client) Health(ctx context.Context) (domain.HealthSnapshot, error) {
	var recs []healthRecord
	if err := c.get(ctx, "sonarr.health", "/api/v3/health", nil, &recs); err != nil {
		return domain.HealthSnapshot{}, err
	}
	snap := domain.HealthSnapshot{ManagerID: c.id, CollectedAt: time.Now().UTC()}
	for _, r := range recs {
		item := domain.HealthItem{Source: r.Source, Type: strings.ToLower(r.Type), Message: redact.Truncate(redact.String(r.Message), 500)}
		if u, err := url.Parse(r.WikiURL); err == nil && u.Scheme == "https" {
			item.WikiURL = redact.URL(r.WikiURL) + fragment(u)
		}
		snap.Items = append(snap.Items, item)
	}
	return snap, nil
}

func fragment(u *url.URL) string {
	if u.Fragment == "" {
		return ""
	}
	return "#" + u.EscapedFragment()
}
