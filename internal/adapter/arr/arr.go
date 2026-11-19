// Package arr implements the HTTP API v3 conventions shared by Sonarr and
// Radarr: API-key authentication, system status, bounded and consistency-
// checked queue pagination, and health. Domain mapping stays in the
// manager-specific packages, because episodes and movies differ.
package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// HistoryPageSize and HistoryMaxPages bound one download's history
	// read (defaults 200 and 10).
	HistoryPageSize int
	HistoryMaxPages int
}

// New creates a client.
func New(o Options) (*Client, error) {
	hc, err := httpx.New(o.BaseURL, httpx.Options{Timeout: o.Timeout, CAFile: o.CAFile, MaxResponseBytes: 32 << 20, UserAgent: "Housekeeparr"})
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

// page is one queue page. Both fields are mandatory: a missing or null
// count or record list is a malformed response, never an empty queue.
type page struct {
	TotalRecords *int               `json:"totalRecords"`
	Records      *[]json.RawMessage `json:"records"`
}

func (pg *page) validate() error {
	return pg.validateFor("queue")
}

func (pg *page) validateFor(kind string) error {
	switch {
	case pg.TotalRecords == nil:
		return fmt.Errorf("%s page has no totalRecords", kind)
	case *pg.TotalRecords < 0:
		return fmt.Errorf("%s page reports negative totalRecords %d", kind, *pg.TotalRecords)
	case pg.Records == nil:
		return fmt.Errorf("%s page has no records list", kind)
	}
	return nil
}

// Queue reads the queue sorted by "added" (stable across pages). Missing or
// null envelope fields are malformed responses. If the page bound is
// reached, the total changes between pages, a row repeats, or the rows read
// do not account for the reported total, the snapshot is marked incomplete
// so absence cannot be inferred.
func (c *Client) Queue(ctx context.Context, managerID string, extra url.Values, normalize func(json.RawMessage) (domain.QueueItem, error)) (domain.QueueSnapshot, error) {
	op := c.app + ".queue"
	snap := domain.QueueSnapshot{ManagerID: managerID, Complete: true}
	incomplete := func(why string) {
		snap.Complete = false
		for _, l := range snap.Limitations {
			if l == why {
				return
			}
		}
		snap.Limitations = append(snap.Limitations, why)
	}
	seen := map[int64]bool{}
	total, rows := -1, 0
	truncated := false
	for p := 1; p <= c.MaxPages; p++ {
		q := url.Values{"page": {strconv.Itoa(p)}, "pageSize": {strconv.Itoa(c.PageSize)}, "sortKey": {"added"}, "sortDirection": {"ascending"}}
		for k, v := range extra {
			q[k] = v
		}
		var pg page
		if err := c.Get(ctx, "queue", "/api/v3/queue", q, &pg); err != nil {
			return domain.QueueSnapshot{}, err
		}
		if err := pg.validate(); err != nil {
			return domain.QueueSnapshot{}, httpx.Malformed(op, err)
		}
		if p == 1 {
			snap.CollectedAt = time.Now().UTC()
			total = *pg.TotalRecords
		} else if *pg.TotalRecords != total {
			incomplete("The queue changed while it was being read; absent rows are not treated as removed this cycle.")
		}
		records := *pg.Records
		rows += len(records)
		for _, raw := range records {
			var id struct {
				ID *int64 `json:"id"`
			}
			if err := json.Unmarshal(raw, &id); err != nil || id.ID == nil {
				return domain.QueueSnapshot{}, httpx.Malformed(op, fmt.Errorf("queue record without an id"))
			}
			it, err := normalize(raw)
			if err != nil {
				return domain.QueueSnapshot{}, httpx.Malformed(op, err)
			}
			it.ManagerID = managerID
			if seen[it.QueueID] {
				incomplete("The queue returned a row more than once; absent rows are not treated as removed this cycle.")
				continue
			}
			seen[it.QueueID] = true
			snap.Items = append(snap.Items, it)
		}
		if len(records) < c.PageSize || rows >= total {
			break
		}
		if p == c.MaxPages {
			truncated = true
			incomplete(fmt.Sprintf("Queue has %d rows; only the first %d were read (max_queue_pages × queue_page_size).", total, len(snap.Items)))
		}
	}
	if !truncated && rows != total {
		incomplete(fmt.Sprintf("The queue reported %d rows but returned %d; absent rows are not treated as removed this cycle.", total, rows))
	}
	snap.TotalRecords = total
	return snap, nil
}

// Missing reads monitored, missing items from /api/v3/wanted/missing within
// configured pagination bounds and maxItems (when maxItems > 0), from the
// first page. Missing or null envelope fields are malformed responses. If
// normalize returns a zero ContentID with nil error, that record is skipped
// (for example when hasFile is true) while still counting toward
// totalRecords. Complete is true only when all reported records were read,
// totalRecords did not change between pages, and no record ID repeated.
func (c *Client) Missing(ctx context.Context, managerID string, extra url.Values, maxItems int, normalize func(json.RawMessage) (domain.MissingItem, error)) (domain.MissingSnapshot, error) {
	return c.MissingFrom(ctx, managerID, extra, maxItems, 1, normalize)
}

// MissingFrom is Missing starting at startPage, so a list longer than the
// read bound can be read in rotating windows. The snapshot's NextPage is
// where the following window starts: 1 once this read reached the end of
// the list. Only a read from the first page to the end can be complete.
// The page size is capped at maxItems so every window advances.
func (c *Client) MissingFrom(ctx context.Context, managerID string, extra url.Values, maxItems, startPage int, normalize func(json.RawMessage) (domain.MissingItem, error)) (domain.MissingSnapshot, error) {
	op := c.app + ".missing"
	if startPage < 1 {
		startPage = 1
	}
	size := c.PageSize
	if maxItems > 0 && maxItems < size {
		size = maxItems
	}
	snap := domain.MissingSnapshot{ManagerID: managerID, Complete: true, StartPage: startPage, NextPage: 1, PageSize: size}
	seen := map[int64]bool{}
	total, rows := -1, 0
	truncated, ended := false, false
	for p := startPage; p < startPage+c.MaxPages; p++ {
		q := url.Values{
			"page":          {strconv.Itoa(p)},
			"pageSize":      {strconv.Itoa(size)},
			"sortKey":       {"id"},
			"sortDirection": {"ascending"},
			"monitored":     {"true"},
		}
		for k, v := range extra {
			q[k] = v
		}
		var pg page
		if err := c.Get(ctx, "missing", "/api/v3/wanted/missing", q, &pg); err != nil {
			return domain.MissingSnapshot{}, err
		}
		if err := pg.validateFor("missing"); err != nil {
			return domain.MissingSnapshot{}, httpx.Malformed(op, err)
		}
		if p == startPage {
			snap.CollectedAt = time.Now().UTC()
			total = *pg.TotalRecords
			// The list shrank below the window's start: read from the
			// beginning now rather than spend this read on nothing.
			if startPage > 1 && total <= (startPage-1)*size {
				return c.MissingFrom(ctx, managerID, extra, maxItems, 1, normalize)
			}
		} else if *pg.TotalRecords != total {
			snap.Complete = false
		}
		records := *pg.Records
		for _, raw := range records {
			if maxItems > 0 && len(snap.Items) >= maxItems {
				// Mid-page: the next window starts on this page again.
				truncated, snap.NextPage = true, p
				break
			}
			rows++
			var id struct {
				ID *int64 `json:"id"`
			}
			if err := json.Unmarshal(raw, &id); err != nil || id.ID == nil || *id.ID <= 0 {
				return domain.MissingSnapshot{}, httpx.Malformed(op, fmt.Errorf("missing record without a valid id"))
			}
			if seen[*id.ID] {
				snap.Complete = false
				continue
			}
			seen[*id.ID] = true
			it, err := normalize(raw)
			if err != nil {
				return domain.MissingSnapshot{}, httpx.Malformed(op, err)
			}
			if it.ContentID == 0 {
				continue
			}
			it.ManagerID = managerID
			snap.Items = append(snap.Items, it)
		}
		if truncated {
			break
		}
		if len(records) < size || (p-1)*size+len(records) >= total {
			ended = true
			break
		}
		if (maxItems > 0 && len(snap.Items) >= maxItems) || p == startPage+c.MaxPages-1 {
			truncated, snap.NextPage = true, p+1
			break
		}
	}
	if truncated || !ended || startPage != 1 || rows != total {
		snap.Complete = false
	}
	if !truncated && !ended {
		snap.NextPage = 1
	}
	snap.Total = total
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
	// A null body is malformed; only an explicit list (possibly empty)
	// establishes the current set of health checks.
	var recs *[]healthRecord
	if err := c.Get(ctx, "health", "/api/v3/health", nil, &recs); err != nil {
		return domain.HealthSnapshot{}, err
	}
	if recs == nil {
		return domain.HealthSnapshot{}, httpx.Malformed(c.app+".health", fmt.Errorf("health response is null, not a list"))
	}
	snap := domain.HealthSnapshot{ManagerID: managerID, CollectedAt: time.Now().UTC()}
	for _, r := range *recs {
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

// RemoveFlags are the DELETE /api/v3/queue/{id} parameters. All four are
// always sent: the released QueueController defaults removeFromClient to
// true, which would delete the torrent.
type RemoveFlags struct {
	RemoveFromClient bool
	Blocklist        bool
	SkipRedownload   bool
	ChangeCategory   bool
}

// Query returns the explicit query string.
func (f RemoveFlags) Query() url.Values {
	b := func(v bool) []string { return []string{fmt.Sprint(v)} }
	return url.Values{"removeFromClient": b(f.RemoveFromClient), "blocklist": b(f.Blocklist),
		"skipRedownload": b(f.SkipRedownload), "changeCategory": b(f.ChangeCategory)}
}

// RemoveQueueItem removes one queue row with explicit flags. A timeout is
// reported as an uncertain outcome; callers must observe before acting
// again. Semantics on released versions are recorded in
// docs/compatibility.md ("Managed rows with real grab history").
func (c *Client) RemoveQueueItem(ctx context.Context, queueID int64, f RemoveFlags) error {
	op := c.app + ".queue_remove"
	_, err := c.http.Do(ctx, httpx.Request{Op: op, Method: http.MethodDelete, Path: fmt.Sprintf("/api/v3/queue/%d", queueID),
		Query: f.Query(), Header: http.Header{"X-Api-Key": {c.apiKey}}, Write: true})
	return err
}

// BlocklistMatch identifies blocklist entries by evidence. Released
// Sonarr 4.0.20 and Radarr 6.4.4 BlocklistResource expose no infohash, so
// an entry is matched by its byte-exact source title, recorded at or after
// Since (when set), and (when set) for the same series/episode or movie.
// This is evidence of identity, not proof: two releases may share a
// title. Callers attribute an entry to their own request only by
// comparing with a baseline taken before the request.
type BlocklistMatch struct {
	SourceTitle string
	Since       time.Time
	SeriesID    int64
	EpisodeID   int64
	MovieID     int64
}

// BlocklistEntries returns the IDs of matching blocklist entries among the
// newest pages; an entry beyond them is not returned, so callers treat
// absence as "not observed".
func (c *Client) BlocklistEntries(ctx context.Context, m BlocklistMatch) ([]int64, error) {
	if strings.TrimSpace(m.SourceTitle) == "" {
		return nil, fmt.Errorf("blocklist match needs a source title")
	}
	var out []int64
	for p := 1; p <= 5; p++ {
		var pg struct {
			TotalRecords *int `json:"totalRecords"`
			Records      []struct {
				ID          *int64    `json:"id"`
				SourceTitle string    `json:"sourceTitle"`
				Date        time.Time `json:"date"`
				SeriesID    int64     `json:"seriesId"`
				EpisodeIDs  []int64   `json:"episodeIds"`
				MovieID     int64     `json:"movieId"`
			} `json:"records"`
		}
		q := url.Values{"page": {fmt.Sprint(p)}, "pageSize": {"100"}, "sortKey": {"date"}, "sortDirection": {"descending"}}
		if err := c.Get(ctx, "blocklist", "/api/v3/blocklist", q, &pg); err != nil {
			return nil, err
		}
		if pg.TotalRecords == nil || pg.Records == nil {
			return nil, httpx.Malformed(c.app+".blocklist", fmt.Errorf("missing totalRecords or records"))
		}
		for _, r := range pg.Records {
			if r.ID == nil {
				return nil, httpx.Malformed(c.app+".blocklist", fmt.Errorf("record without id"))
			}
			if r.SourceTitle != m.SourceTitle || (!m.Since.IsZero() && r.Date.Before(m.Since)) {
				continue
			}
			if m.SeriesID != 0 && r.SeriesID != m.SeriesID || m.MovieID != 0 && r.MovieID != m.MovieID {
				continue
			}
			if m.EpisodeID != 0 {
				found := false
				for _, e := range r.EpisodeIDs {
					found = found || e == m.EpisodeID
				}
				if !found {
					continue
				}
			}
			out = append(out, *r.ID)
		}
		if p*100 >= *pg.TotalRecords {
			break
		}
	}
	return out, nil
}

// Blocklisted reports whether a matching blocklist entry exists.
func (c *Client) Blocklisted(ctx context.Context, m BlocklistMatch) (bool, error) {
	ids, err := c.BlocklistEntries(ctx, m)
	return len(ids) > 0, err
}

// HistoryEvent is one manager history record for a download.
type HistoryEvent struct {
	ID        int64     `json:"id"`
	EventType string    `json:"event_type"`
	Date      time.Time `json:"date"`
	// ReleaseSource is the grab's data.releaseSource (for example Search,
	// Rss, InteractiveSearch), when the record has one.
	ReleaseSource string `json:"release_source,omitempty"`
	// SourceTitle is the release title the record was written for. For a
	// grab it is the indexer's release title, which the manager also uses
	// for the blocklist entry; the queue row's title can differ (a magnet
	// without metadata is listed under the client's placeholder name).
	SourceTitle string `json:"source_title,omitempty"`
	// EpisodeID is the Sonarr episode the record is for (0 for Radarr or
	// when not reported); a pack writes one record per episode.
	EpisodeID int64 `json:"episode_id,omitempty"`
}

// History returns the newest history records for a download id, newest
// first. Records are filtered by download id client-side as well.
func (c *Client) History(ctx context.Context, downloadID string) ([]HistoryEvent, error) {
	op := c.app + ".history"
	var out []HistoryEvent
	seen := map[int64]bool{}
	total, rows := -1, 0
	var incomplete []string
	size, maxPages := c.HistoryPageSize, c.HistoryMaxPages
	if size <= 0 {
		size = 200
	}
	if maxPages <= 0 {
		maxPages = 10
	}
	for p := 1; p <= maxPages; p++ {
		var pg struct {
			TotalRecords *int `json:"totalRecords"`
			Records      []struct {
				ID          *int64    `json:"id"`
				EventType   string    `json:"eventType"`
				SourceTitle string    `json:"sourceTitle"`
				DownloadID  string    `json:"downloadId"`
				EpisodeID   int64     `json:"episodeId"`
				Date        time.Time `json:"date"`
				Data        struct {
					ReleaseSource string `json:"releaseSource"`
				} `json:"data"`
			} `json:"records"`
		}
		// Newest first. One record per episode and event, so a large pack
		// with repeated import attempts can need several pages.
		q := url.Values{"page": {strconv.Itoa(p)}, "pageSize": {strconv.Itoa(size)}, "downloadId": {strings.ToUpper(downloadID)},
			"sortKey": {"date"}, "sortDirection": {"descending"}}
		if err := c.Get(ctx, "history", "/api/v3/history", q, &pg); err != nil {
			return nil, err
		}
		if pg.Records == nil {
			return nil, httpx.Malformed(op, fmt.Errorf("missing records"))
		}
		if pg.TotalRecords == nil || *pg.TotalRecords < 0 {
			return nil, httpx.Malformed(op, fmt.Errorf("missing or negative totalRecords"))
		}
		if p == 1 {
			total = *pg.TotalRecords
		} else if *pg.TotalRecords != total {
			incomplete = append(incomplete, "the history changed while it was being read")
		}
		rows += len(pg.Records)
		for _, r := range pg.Records {
			if r.ID == nil {
				return nil, httpx.Malformed(op, fmt.Errorf("record without id"))
			}
			if seen[*r.ID] {
				continue // shifted by a newer record between pages
			}
			seen[*r.ID] = true
			if strings.EqualFold(r.DownloadID, downloadID) {
				out = append(out, HistoryEvent{ID: *r.ID, EventType: r.EventType, Date: r.Date, ReleaseSource: r.Data.ReleaseSource, SourceTitle: r.SourceTitle,
					EpisodeID: r.EpisodeID})
			}
		}
		if len(pg.Records) < size || rows >= total {
			break
		}
		if p == maxPages {
			incomplete = append(incomplete, fmt.Sprintf("it has %d records and only the newest %d were read (%d pages of %d)", total, rows, maxPages, size))
		}
	}
	if rows < total && len(incomplete) == 0 {
		incomplete = append(incomplete, fmt.Sprintf("it reported %d records but returned %d", total, rows))
	}
	if len(incomplete) > 0 {
		return out, fmt.Errorf("%w for download %s: %s", ErrHistoryIncomplete, strings.ToUpper(downloadID), strings.Join(incomplete, "; "))
	}
	return out, nil
}

// ErrHistoryIncomplete accompanies the newest records History could read
// when it could not read a download's whole history. The newest records
// still establish the latest grab and anything added after a baseline;
// evidence that needs every record (which episodes a pack imported) must
// not be inferred from them.
var ErrHistoryIncomplete = errors.New("manager history incomplete")

// RedownloadSettings are the manager settings that decide whether failing a
// grab makes the manager search for a replacement itself (released
// RedownloadFailedDownloadService in Sonarr 4.0.20 and Radarr 6.4.4: no
// search unless AutoRedownloadFailed, nor for a release grabbed through
// interactive search unless AutoRedownloadFailedFromInteractiveSearch).
type RedownloadSettings struct {
	Failed bool `json:"auto_redownload_failed"`
	// FromInteractive is nil when the manager does not report it.
	FromInteractive *bool `json:"auto_redownload_failed_from_interactive_search,omitempty"`
}

// Redownload reads the redownload settings (GET /api/v3/config/downloadclient).
func (c *Client) Redownload(ctx context.Context) (RedownloadSettings, error) {
	var raw struct {
		Failed          *bool `json:"autoRedownloadFailed"`
		FromInteractive *bool `json:"autoRedownloadFailedFromInteractiveSearch"`
	}
	if err := c.Get(ctx, "download client config", "/api/v3/config/downloadclient", nil, &raw); err != nil {
		return RedownloadSettings{}, err
	}
	if raw.Failed == nil {
		return RedownloadSettings{}, httpx.Malformed(c.app+".config", fmt.Errorf("missing autoRedownloadFailed"))
	}
	return RedownloadSettings{Failed: *raw.Failed, FromInteractive: raw.FromInteractive}, nil
}

// Command is one entry of the manager's command list (GET /api/v3/command).
// The list holds queued and running commands and recently finished ones;
// Sonarr and Radarr eventually drop finished commands from it (a periodic
// cleanup; finished commands were still listed seven minutes later on the
// tested versions, but the exact retention is not established), so
// absence of a finished command is not evidence that it never ran.
type Command struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`           // queued, started, completed, failed, aborted, cancelled, orphaned
	Result     string     `json:"result,omitempty"` // successful, unsuccessful, unknown
	Message    string     `json:"message,omitempty"`
	Trigger    string     `json:"trigger,omitempty"`
	Queued     time.Time  `json:"queued"`
	Started    *time.Time `json:"started,omitempty"`
	Ended      *time.Time `json:"ended,omitempty"`
	EpisodeIDs []int64    `json:"episode_ids,omitempty"`
	MovieIDs   []int64    `json:"movie_ids,omitempty"`
}

// Commands returns the manager's current command list.
func (c *Client) Commands(ctx context.Context) ([]Command, error) {
	var raw []struct {
		ID      *int64     `json:"id"`
		Name    string     `json:"name"`
		Status  string     `json:"status"`
		Result  string     `json:"result"`
		Message string     `json:"message"`
		Trigger string     `json:"trigger"`
		Queued  *time.Time `json:"queued"`
		Started *time.Time `json:"started"`
		Ended   *time.Time `json:"ended"`
		Body    struct {
			EpisodeIDs []int64 `json:"episodeIds"`
			MovieIDs   []int64 `json:"movieIds"`
		} `json:"body"`
	}
	if err := c.Get(ctx, "command", "/api/v3/command", nil, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, httpx.Malformed(c.app+".command", fmt.Errorf("missing command list"))
	}
	out := make([]Command, 0, len(raw))
	for _, r := range raw {
		if r.ID == nil || r.Name == "" || r.Status == "" || r.Queued == nil {
			return nil, httpx.Malformed(c.app+".command", fmt.Errorf("command without id, name, status, or queued time"))
		}
		out = append(out, Command{ID: *r.ID, Name: r.Name, Status: r.Status, Result: r.Result, Message: r.Message, Trigger: r.Trigger,
			Queued: *r.Queued, Started: r.Started, Ended: r.Ended, EpisodeIDs: r.Body.EpisodeIDs, MovieIDs: r.Body.MovieIDs})
	}
	return out, nil
}

// Search dispatches a search command (POST /api/v3/command) with body
// {"name": name, idsField: ids} for 1 to 10 positive IDs. A timeout is
// classified as an uncertain outcome (Write: true).
func (c *Client) Search(ctx context.Context, name string, idsField string, ids []int64) (int64, error) {
	op := c.app + ".search"
	if name == "" || idsField == "" {
		return 0, &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: "search requires command name and id field"}
	}
	if len(ids) == 0 {
		return 0, &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: "search requires at least one id"}
	}
	if len(ids) > 10 {
		return 0, &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: fmt.Sprintf("search accepts at most 10 ids, got %d", len(ids))}
	}
	for _, id := range ids {
		if id <= 0 {
			return 0, &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: "search id must be positive"}
		}
	}
	payload, err := json.Marshal(map[string]any{"name": name, idsField: ids})
	if err != nil {
		return 0, &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: "could not encode search command", Err: err}
	}
	resp, err := c.http.Do(ctx, httpx.Request{
		Op:     op,
		Method: http.MethodPost,
		Path:   "/api/v3/command",
		Header: http.Header{"X-Api-Key": {c.apiKey}, "Content-Type": {"application/json"}},
		Body:   bytes.NewReader(payload),
		Write:  true,
	})
	if err != nil {
		return 0, err
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return 0, &adapter.Error{Class: adapter.ErrPermanent, Op: op,
			Message: fmt.Sprintf("expected JSON but received %q (wrong base path or an authentication proxy?)", ct)}
	}
	var out struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return 0, httpx.Malformed(op, err)
	}
	if out.ID == nil || *out.ID <= 0 {
		return 0, httpx.Malformed(op, fmt.Errorf("command response missing positive id"))
	}
	return *out.ID, nil
}

// Content is the live state of one managed episode or movie.
type Content struct {
	Key       string `json:"key"`
	Exists    bool   `json:"exists"`
	Monitored bool   `json:"monitored"`
	// HasFile is whether the manager has a file for it (nil when not read).
	HasFile *bool `json:"has_file,omitempty"`
	// Available is the live release or air-date check, if reported.
	Available *bool `json:"available,omitempty"`
	// Detail explains how monitoring was established (for example, that
	// the series is unmonitored although the episode is not).
	Detail string `json:"detail,omitempty"`
}

// NotFound reports whether err is an HTTP 404 from the application.
func NotFound(err error) bool {
	var ae *adapter.Error
	return errors.As(err, &ae) && ae.HTTPStatus == http.StatusNotFound
}

// Monitored reads a resource's id and monitored flag. found is false for a
// 404; a response without both fields is malformed.
func (c *Client) Monitored(ctx context.Context, op, path string) (found, monitored bool, raw map[string]json.RawMessage, err error) {
	var r map[string]json.RawMessage
	if err := c.Get(ctx, op, path, nil, &r); err != nil {
		if NotFound(err) {
			return false, false, nil, nil
		}
		return false, false, nil, err
	}
	var id int64
	var mon *bool
	if r == nil || json.Unmarshal(r["id"], &id) != nil || id <= 0 || json.Unmarshal(r["monitored"], &mon) != nil || mon == nil {
		return false, false, nil, httpx.Malformed(c.app+"."+op, fmt.Errorf("missing id or monitored"))
	}
	return true, *mon, r, nil
}

type indexerRecord struct {
	ID                    *int64 `json:"id"`
	Name                  string `json:"name"`
	Protocol              string `json:"protocol"`
	EnableAutomaticSearch *bool  `json:"enableAutomaticSearch"`
	// fields, baseUrl, and settings are deliberately not decoded.
}

type indexerStatusRecord struct {
	IndexerID         int64   `json:"indexerId"`
	DisabledTill      *string `json:"disabledTill"`
	MostRecentFailure *string `json:"mostRecentFailure"`
}

func parseOptionalUTCTime(raw *string) *time.Time {
	if raw == nil || *raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

// Sources reads the manager's configured indexers (/api/v3/indexer) and
// their failure status (/api/v3/indexerstatus). Indexer settings, URLs, and
// credentials ("fields", "baseUrl") are never decoded or retained.
func (c *Client) Sources(ctx context.Context, managerID string) (domain.SourceSnapshot, error) {
	op := c.app + ".sources"
	var indexers *[]indexerRecord
	if err := c.Get(ctx, "indexer", "/api/v3/indexer", nil, &indexers); err != nil {
		return domain.SourceSnapshot{}, err
	}
	if indexers == nil {
		return domain.SourceSnapshot{}, httpx.Malformed(op, fmt.Errorf("indexer response is null, not a list"))
	}
	// Indexer status is optional: released Sonarr 4.0.20 answers 404
	// (observed), so status is then unknown rather than healthy.
	var statuses *[]indexerStatusRecord
	statusKnown := true
	if err := c.Get(ctx, "indexerstatus", "/api/v3/indexerstatus", nil, &statuses); err != nil {
		if adapter.ClassOf(err) != adapter.ErrUnsupported {
			return domain.SourceSnapshot{}, err
		}
		statusKnown, statuses = false, &[]indexerStatusRecord{}
	}
	if statuses == nil {
		return domain.SourceSnapshot{}, httpx.Malformed(op, fmt.Errorf("indexerstatus response is null, not a list"))
	}
	statusByID := make(map[int64]indexerStatusRecord, len(*statuses))
	for _, st := range *statuses {
		if st.IndexerID > 0 {
			statusByID[st.IndexerID] = st
		}
	}
	snap := domain.SourceSnapshot{
		ManagerID:   managerID,
		CollectedAt: time.Now().UTC(),
		Sources:     make([]domain.Source, 0, len(*indexers)),
	}
	for _, r := range *indexers {
		if r.ID == nil || *r.ID <= 0 || r.EnableAutomaticSearch == nil {
			return domain.SourceSnapshot{}, httpx.Malformed(op, fmt.Errorf("indexer record without valid id or enableAutomaticSearch"))
		}
		src := domain.Source{
			ID:              *r.ID,
			Name:            redact.Truncate(redact.String(r.Name), 100),
			Protocol:        r.Protocol,
			AutomaticSearch: *r.EnableAutomaticSearch,
			StatusKnown:     statusKnown,
		}
		if st, ok := statusByID[*r.ID]; ok {
			src.DisabledUntil = parseOptionalUTCTime(st.DisabledTill)
			src.LastFailure = parseOptionalUTCTime(st.MostRecentFailure)
		}
		snap.Sources = append(snap.Sources, src)
	}
	return snap, nil
}
