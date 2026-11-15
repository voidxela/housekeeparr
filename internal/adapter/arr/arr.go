// Package arr implements the HTTP API v3 conventions shared by Sonarr and
// Radarr: API-key authentication, system status, bounded and consistency-
// checked queue pagination, and health. Domain mapping stays in the
// manager-specific packages, because episodes and movies differ.
package arr

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

// Options configure a client.
type Options struct {
	App      string // "sonarr" or "radarr", used in operation names
	BaseURL  string
	APIKey   string
	Timeout  time.Duration
	CAFile   string
	PageSize int
	MaxPages int
}

// Client is a shared *arr v3 client.
type Client struct {
	app      string
	http     *httpx.Client
	apiKey   string
	PageSize int
	MaxPages int
}

// New creates a client.
func New(o Options) (*Client, error) {
	hc, err := httpx.New(o.BaseURL, httpx.Options{Timeout: o.Timeout, CAFile: o.CAFile, MaxResponseBytes: 32 << 20, UserAgent: "Housekeeparr (read-only)"})
	if err != nil {
		return nil, err
	}
	if o.PageSize <= 0 {
		o.PageSize = 200
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 25
	}
	return &Client{app: o.App, http: hc, apiKey: o.APIKey, PageSize: o.PageSize, MaxPages: o.MaxPages}, nil
}

// Get performs an authenticated GET and decodes JSON. The API key is sent
// only as a header, never in the query string.
func (c *Client) Get(ctx context.Context, op, path string, q url.Values, out any) error {
	op = c.app + "." + op
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

// Status is the subset of /api/v3/system/status used for probing.
type Status struct {
	AppName      string `json:"appName"`
	InstanceName string `json:"instanceName"`
	Version      string `json:"version"`
}

// Status reads system status and checks the application name.
func (c *Client) Status(ctx context.Context, wantApp string) (Status, error) {
	var st Status
	if err := c.Get(ctx, "system_status", "/api/v3/system/status", nil, &st); err != nil {
		return st, err
	}
	if !strings.EqualFold(st.AppName, wantApp) {
		return st, &adapter.Error{Class: adapter.ErrUnsupported, Op: c.app + ".system_status",
			Message: fmt.Sprintf("application reports itself as %q, not %s", redact.Truncate(st.AppName, 40), wantApp)}
	}
	return st, nil
}

// Major parses the major version number.
func Major(v string) int {
	n, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return n
}

// Compatibility classifies a version against a minimum major and tested list.
func Compatibility(app, version string, minMajor int, tested []string) (adapter.Compatibility, string) {
	if m := Major(version); m < minMajor {
		return adapter.CompatUnsupported, fmt.Sprintf("%s %d.x is not supported; Housekeeparr requires %s %d or later", app, m, app, minMajor)
	}
	for _, t := range tested {
		if t == version {
			return adapter.CompatTested, "read workflow validated against this released version"
		}
	}
	return adapter.CompatUntested, "observation proceeds; this version has not been validated by the integration suite"
}

type page struct {
	TotalRecords int               `json:"totalRecords"`
	Records      []json.RawMessage `json:"records"`
}

// Queue reads the queue sorted by "added" (stable across pages). If the
// page bound is reached, the total changes between pages, or a row repeats,
// the snapshot is marked incomplete so absence cannot be inferred.
func (c *Client) Queue(ctx context.Context, managerID string, extra url.Values, normalize func(json.RawMessage) (domain.QueueItem, error)) (domain.QueueSnapshot, error) {
	snap := domain.QueueSnapshot{ManagerID: managerID, Complete: true}
	seen := map[int64]bool{}
	total := -1
	for p := 1; p <= c.MaxPages; p++ {
		q := url.Values{"page": {strconv.Itoa(p)}, "pageSize": {strconv.Itoa(c.PageSize)}, "sortKey": {"added"}, "sortDirection": {"ascending"}}
		for k, v := range extra {
			q[k] = v
		}
		var pg page
		if err := c.Get(ctx, "queue", "/api/v3/queue", q, &pg); err != nil {
			return domain.QueueSnapshot{}, err
		}
		if p == 1 {
			snap.CollectedAt = time.Now().UTC()
			total = pg.TotalRecords
		} else if pg.TotalRecords != total {
			snap.Complete = false
			snap.Limitations = append(snap.Limitations, "The queue changed while it was being read; absent rows are not treated as removed this cycle.")
		}
		for _, raw := range pg.Records {
			it, err := normalize(raw)
			if err != nil {
				return domain.QueueSnapshot{}, httpx.Malformed(c.app+".queue", err)
			}
			it.ManagerID = managerID
			if seen[it.QueueID] {
				snap.Complete = false
				continue
			}
			seen[it.QueueID] = true
			snap.Items = append(snap.Items, it)
		}
		if len(pg.Records) < c.PageSize || len(snap.Items) >= total {
			break
		}
		if p == c.MaxPages {
			snap.Complete = false
			snap.Limitations = append(snap.Limitations, fmt.Sprintf("Queue has %d rows; only the first %d were read (max_queue_pages × queue_page_size).", total, len(snap.Items)))
		}
	}
	snap.TotalRecords = total
	return snap, nil
}

// Record holds the queue fields shared by Sonarr and Radarr.
type Record struct {
	ID                    int64    `json:"id"`
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

// Common normalizes the shared fields.
func (r Record) Common() domain.QueueItem {
	q := domain.QueueItem{
		QueueID: r.ID, DownloadID: domain.NormalizeHash(r.DownloadID), Protocol: r.Protocol,
		DownloadClient: r.DownloadClient, Indexer: r.Indexer, Title: r.Title, Status: r.Status,
		ErrorMessage: redact.Truncate(redact.String(r.ErrorMessage), 500),
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
	q.SizeBytes = domain.UnknownValue[int64]("not reported")
	if r.Size != nil {
		q.SizeBytes = domain.KnownValue(int64(*r.Size))
	}
	q.RemainingBytes = domain.UnknownValue[int64]("not reported")
	if r.SizeLeft != nil {
		q.RemainingBytes = domain.KnownValue(int64(*r.SizeLeft))
	}
	q.Added = domain.UnknownValue[time.Time]("not reported")
	if r.Added != nil {
		if t, err := time.Parse(time.RFC3339, *r.Added); err == nil {
			q.Added = domain.KnownValue(t.UTC())
		}
	}
	q.SeasonNumber = domain.UnknownValue[int64]("not applicable")
	q.EpisodeNumber = domain.UnknownValue[int64]("not applicable")
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

// Health reads /api/v3/health.
func (c *Client) Health(ctx context.Context, managerID string) (domain.HealthSnapshot, error) {
	var recs []healthRecord
	if err := c.Get(ctx, "health", "/api/v3/health", nil, &recs); err != nil {
		return domain.HealthSnapshot{}, err
	}
	snap := domain.HealthSnapshot{ManagerID: managerID, CollectedAt: time.Now().UTC()}
	for _, r := range recs {
		item := domain.HealthItem{Source: r.Source, Type: strings.ToLower(r.Type), Message: redact.Truncate(redact.String(r.Message), 500)}
		if u, err := url.Parse(r.WikiURL); err == nil && u.Scheme == "https" {
			item.WikiURL = redact.URL(r.WikiURL)
			if u.Fragment != "" {
				item.WikiURL += "#" + u.EscapedFragment()
			}
		}
		snap.Items = append(snap.Items, item)
	}
	return snap, nil
}
