// Package qbittorrent is a read-only adapter for the qBittorrent WebUI API
// (v2). It reports normalized transfers and never issues operational
// writes; the only POST it sends is the session login.
package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// AvailabilityMeaning qualifies qBittorrent's availability figure. Verified
// against qBittorrent 5.3.0 / libtorrent 2.0.11 source: the value is
// libtorrent's distributed_copies, which counts this client's own pieces and
// connected peers across all pieces of the torrent, and is -1 when not
// computed.
const AvailabilityMeaning = "qBittorrent availability is libtorrent's distributed-copies figure: it covers this client and currently connected peers only, not the whole swarm."

// Options configure the adapter.
type Options struct {
	ID       string
	BaseURL  string
	Username string
	Password string // resolved secret; never logged
	Timeout  time.Duration
	CAFile   string
}

// Client implements adapter.DownloadClient.
type Client struct {
	id       string
	http     *httpx.Client
	username string
	password string
	referer  string

	mu       sync.Mutex
	loggedIn bool
	version  version
}

// New creates a qBittorrent adapter.
func New(o Options) (*Client, error) {
	hc, err := httpx.New(o.BaseURL, httpx.Options{
		Timeout: o.Timeout, CAFile: o.CAFile, CookieJar: true,
		// Large seeding libraries produce big listings; bound them anyway.
		MaxResponseBytes: 64 << 20,
		UserAgent:        "Housekeeparr (read-only)",
	})
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(o.BaseURL)
	return &Client{id: o.ID, http: hc, username: o.Username, password: o.Password,
		referer: u.Scheme + "://" + u.Host}, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindQBittorrent }

type version struct {
	raw          string
	major, minor int
	patch        int
	webAPI       string
}

func parseVersion(s string) (version, bool) {
	v := version{raw: strings.TrimSpace(s)}
	t := strings.TrimPrefix(v.raw, "v")
	// Strip suffixes such as "5.0.0beta1" or "4.6.7-1".
	end := len(t)
	for i, r := range t {
		if (r < '0' || r > '9') && r != '.' {
			end = i
			break
		}
	}
	parts := strings.Split(t[:end], ".")
	if len(parts) < 2 {
		return v, false
	}
	var err error
	if v.major, err = strconv.Atoi(parts[0]); err != nil {
		return v, false
	}
	if v.minor, err = strconv.Atoi(parts[1]); err != nil {
		return v, false
	}
	if len(parts) > 2 {
		v.patch, _ = strconv.Atoi(parts[2])
	}
	return v, true
}

func (v version) atLeast(major, minor int) bool {
	return v.major > major || (v.major == major && v.minor >= minor)
}

// login establishes a session cookie. It is not an operational write.
func (c *Client) login(ctx context.Context) error {
	if c.username == "" {
		return nil // rely on the client's authentication bypass
	}
	form := url.Values{"username": {c.username}, "password": {c.password}}
	resp, err := c.http.Do(ctx, httpx.Request{
		Op: "qbittorrent.login", Method: http.MethodPost, Path: "/api/v2/auth/login",
		Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Referer": {c.referer}},
		Body:   strings.NewReader(form.Encode()),
	})
	if err != nil {
		if ae, ok := err.(*adapter.Error); ok && ae.HTTPStatus == http.StatusForbidden {
			ae.Message = "HTTP 403: qBittorrent refused login (address banned after failed attempts?)"
		}
		return err
	}
	if strings.TrimSpace(string(resp.Body)) == "Fails." {
		return &adapter.Error{Class: adapter.ErrUnauthorized, Op: "qbittorrent.login", Message: "username or password rejected"}
	}
	c.loggedIn = true
	return nil
}

// get performs an authenticated GET, re-establishing an expired session once.
func (c *Client) get(ctx context.Context, op, path string, q url.Values) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loggedIn {
		if err := c.login(ctx); err != nil {
			return nil, err
		}
	}
	req := httpx.Request{Op: op, Method: http.MethodGet, Path: path, Query: q, Header: http.Header{"Referer": {c.referer}}}
	resp, err := c.http.Do(ctx, req)
	if ae, ok := err.(*adapter.Error); ok && ae.HTTPStatus == http.StatusForbidden && c.username != "" {
		c.loggedIn = false
		if lerr := c.login(ctx); lerr != nil {
			return nil, lerr
		}
		resp, err = c.http.Do(ctx, req)
	}
	if ae, ok := err.(*adapter.Error); ok && ae.Class == adapter.ErrUnauthorized && c.username == "" {
		ae.Message = "qBittorrent requires authentication; configure username and password, or enable its authentication bypass for this address"
	}
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Probe authenticates and detects version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	body, err := c.get(ctx, "qbittorrent.version", "/api/v2/app/version", nil)
	if err != nil {
		return adapter.Probe{}, err
	}
	v, ok := parseVersion(string(body))
	if !ok {
		return adapter.Probe{}, &adapter.Error{Class: adapter.ErrUnsupported, Op: "qbittorrent.version",
			Message: "unrecognised version string; is this a qBittorrent WebUI?"}
	}
	if api, err := c.get(ctx, "qbittorrent.webapi_version", "/api/v2/app/webapiVersion", nil); err == nil {
		v.webAPI = strings.TrimSpace(string(api))
	}
	c.mu.Lock()
	c.version = v
	c.mu.Unlock()

	p := adapter.Probe{
		App: "qBittorrent", Version: v.raw, APIVersion: v.webAPI, Transport: "WebUI API v2 over HTTP(S)",
		Auth: "session login accepted",
	}
	if c.username == "" {
		p.Auth = "no credentials sent (client authentication bypass)"
	}
	p.Compatibility, p.CompatNote = compatibility(v)
	privacy := adapter.CapabilityStatus{Support: adapter.Supported, Note: "torrent list 'private' field (qBittorrent ≥ 5.0); unknown until metadata is available"}
	if !v.atLeast(5, 0) {
		privacy = adapter.CapabilityStatus{Support: adapter.NotSupported, Note: "qBittorrent < 5.0 does not report privacy in the torrent list"}
	}
	v2 := adapter.CapabilityStatus{Support: adapter.Supported, Note: "infohash_v1 / infohash_v2 fields"}
	if !v.atLeast(4, 4) {
		v2 = adapter.CapabilityStatus{Support: adapter.NotSupported, Note: "requires qBittorrent ≥ 4.4"}
	}
	p.Read = adapter.Capabilities{
		adapter.CapTransferList:      {Support: adapter.Supported, Note: "complete listing via /torrents/info"},
		adapter.CapVerifiedProgress:  {Support: adapter.Supported, Note: "'completed' (libtorrent total_wanted_done: hash-checked wanted bytes)"},
		adapter.CapPieceAvailability: {Support: adapter.Supported, Note: AvailabilityMeaning},
		adapter.CapPrivacy:           privacy,
		adapter.CapMetadataState:     {Support: adapter.Supported, Note: "metaDL / forcedMetaDL states"},
		adapter.CapFileSelection:     {Support: adapter.Supported, Note: "wanted size compared with total size"},
		adapter.CapSwarmCounts:       {Support: adapter.Supported, Note: "tracker-reported counts; may be stale and do not prove piece availability"},
		adapter.CapInfohashV2:        v2,
		adapter.CapClientFileList:    {Support: adapter.NotImplemented, Note: "available upstream (/torrents/files); not used yet"},
	}
	p.Write = adapter.ReadOnlyWrites(adapter.CapStopTransfer, adapter.CapTransferLabel)
	p.Limitations = []string{
		"Piece availability reflects connected peers only.",
		"Tracker URLs, magnet links, and paths are deliberately not collected.",
	}
	return p, nil
}

func compatibility(v version) (adapter.Compatibility, string) {
	if !v.atLeast(4, 1) {
		return adapter.CompatUnsupported, "qBittorrent before 4.1 does not provide WebUI API v2"
	}
	for _, t := range TestedVersions {
		if tv, ok := parseVersion(t); ok && tv.major == v.major && tv.minor == v.minor && tv.patch == v.patch {
			return adapter.CompatTested, "read workflow validated against this released version"
		}
	}
	return adapter.CompatUntested, "observation proceeds; this version has not been validated by the integration suite"
}

// TestedVersions lists released versions whose read path passed the
// integration suite (scripts/it). Only add versions after a real run.
var TestedVersions = []string{}

// torrent mirrors the fields Housekeeparr reads. Sensitive fields
// (tracker, magnet_uri, save_path, content_path, comment) are deliberately
// absent so they are never decoded or stored.
type torrent struct {
	Hash          string          `json:"hash"`
	InfohashV1    string          `json:"infohash_v1"`
	InfohashV2    string          `json:"infohash_v2"`
	Name          string          `json:"name"`
	State         string          `json:"state"`
	Category      *string         `json:"category"`
	AddedOn       *int64          `json:"added_on"`
	Size          *int64          `json:"size"`
	TotalSize     *int64          `json:"total_size"`
	Completed     *int64          `json:"completed"`
	AmountLeft    *int64          `json:"amount_left"`
	Downloaded    *int64          `json:"downloaded"`
	DLSpeed       *int64          `json:"dlspeed"`
	NumSeeds      *int64          `json:"num_seeds"`
	NumLeechs     *int64          `json:"num_leechs"`
	NumComplete   *int64          `json:"num_complete"`
	NumIncomplete *int64          `json:"num_incomplete"`
	Availability  *float64        `json:"availability"`
	HasMetadata   *bool           `json:"has_metadata"`
	Private       json.RawMessage `json:"private"`
}

// ListTransfers returns every torrent.
func (c *Client) ListTransfers(ctx context.Context) (domain.TransferSnapshot, error) {
	body, err := c.get(ctx, "qbittorrent.torrents_info", "/api/v2/torrents/info", nil)
	if err != nil {
		return domain.TransferSnapshot{}, err
	}
	collected := time.Now().UTC()
	var raw []torrent
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&raw); err != nil {
		return domain.TransferSnapshot{}, httpx.Malformed("qbittorrent.torrents_info", err)
	}
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	snap := domain.TransferSnapshot{ClientID: c.id, CollectedAt: collected, Complete: true}
	snap.Transfers = make([]domain.Transfer, 0, len(raw))
	for i := range raw {
		t, err := normalize(c.id, &raw[i], v)
		if err != nil {
			return domain.TransferSnapshot{}, httpx.Malformed("qbittorrent.torrents_info", err)
		}
		snap.Transfers = append(snap.Transfers, t)
	}
	return snap, nil
}

// stateClass maps qBittorrent 4.x and 5.x state strings.
func stateClass(s string) domain.StateClass {
	switch s {
	case "downloading", "stalledDL", "forcedDL":
		return domain.StateDownloading
	case "metaDL", "forcedMetaDL":
		return domain.StateMetadata
	case "pausedDL", "stoppedDL":
		return domain.StatePaused
	case "queuedDL":
		return domain.StateQueued
	case "checkingDL", "checkingUP", "checkingResumeData":
		return domain.StateChecking
	case "uploading", "stalledUP", "forcedUP", "queuedUP", "pausedUP", "stoppedUP":
		return domain.StateComplete
	case "error", "missingFiles":
		return domain.StateError
	case "allocating", "moving":
		return domain.StateTransient
	default:
		return domain.StateUnknown
	}
}

func knownInt(p *int64, field string) domain.Signal[int64] {
	if p == nil {
		return domain.UnsupportedValue[int64](field + " not reported by this version")
	}
	return domain.KnownValue(*p)
}

// countSignal treats negative counts as unknown ("-1 if not known").
func countSignal(p *int64, field string) domain.Signal[int64] {
	if p == nil {
		return domain.UnsupportedValue[int64](field + " not reported by this version")
	}
	if *p < 0 {
		return domain.UnknownValue[int64]("not known to the client")
	}
	return domain.KnownValue(*p)
}

func normalize(clientID string, r *torrent, v version) (domain.Transfer, error) {
	id := domain.NormalizeHash(r.Hash)
	if id == "" {
		return domain.Transfer{}, fmt.Errorf("torrent without hash")
	}
	class := stateClass(r.State)
	hasMeta := class != domain.StateMetadata
	if r.HasMetadata != nil {
		hasMeta = *r.HasMetadata
	}
	t := domain.Transfer{
		ClientID: clientID, NativeID: id, Name: r.Name, NativeState: r.State, StateClass: class,
		Hashes: domain.Hashes{V1: domain.NormalizeHash(r.InfohashV1), V2: domain.NormalizeHash(r.InfohashV2)},
	}
	if t.Hashes.V1 == "" && t.Hashes.V2 == "" && len(id) == 40 && !v.atLeast(4, 4) {
		t.Hashes.V1 = id // pre-4.4 clients only support v1 torrents
	}
	if r.Category != nil {
		t.Category = domain.KnownValue(*r.Category)
	} else {
		t.Category = domain.UnsupportedValue[string]("category not reported")
	}
	if r.AddedOn != nil && *r.AddedOn > 0 {
		t.AddedAt = domain.KnownValue(time.Unix(*r.AddedOn, 0).UTC())
	} else {
		t.AddedAt = domain.UnknownValue[time.Time]("added time not reported")
	}
	noMeta := func(field string) domain.Signal[int64] {
		return domain.UnknownValue[int64](field + " unavailable until metadata is acquired")
	}
	if hasMeta {
		t.WantedBytes = knownInt(r.Size, "size")
		t.VerifiedBytes = knownInt(r.Completed, "completed")
		t.RemainingBytes = knownInt(r.AmountLeft, "amount_left")
		t.TotalBytes = knownInt(r.TotalSize, "total_size")
		if r.Size != nil && r.TotalSize != nil {
			t.AllFilesWanted = domain.KnownValue(*r.Size >= *r.TotalSize)
		} else {
			t.AllFilesWanted = domain.UnsupportedValue[bool]("sizes not reported")
		}
	} else {
		t.WantedBytes, t.VerifiedBytes = noMeta("wanted size"), noMeta("verified progress")
		t.RemainingBytes, t.TotalBytes = noMeta("remaining size"), noMeta("total size")
		t.AllFilesWanted = domain.UnknownValue[bool]("file list unavailable until metadata is acquired")
	}
	t.NetworkDownloadedBytes = knownInt(r.Downloaded, "downloaded")
	t.DownloadRate = knownInt(r.DLSpeed, "dlspeed")
	t.ConnectedSeeds = countSignal(r.NumSeeds, "num_seeds")
	t.ConnectedPeers = countSignal(r.NumLeechs, "num_leechs")
	t.SwarmSeeds = countSignal(r.NumComplete, "num_complete")
	t.SwarmPeers = countSignal(r.NumIncomplete, "num_incomplete")
	switch {
	case r.Availability == nil:
		t.Availability = domain.UnsupportedValue[float64]("availability not reported")
	case *r.Availability < 0:
		t.Availability = domain.UnknownValue[float64]("not computed by the client in this state")
	default:
		t.Availability = domain.Signal[float64]{Status: domain.Known, Value: *r.Availability, Note: AvailabilityMeaning}
	}
	switch {
	case len(r.Private) == 0:
		t.Private = domain.UnsupportedValue[bool]("qBittorrent < 5.0 does not report privacy in the torrent list")
	case string(r.Private) == "null":
		t.Private = domain.UnknownValue[bool]("privacy unknown until metadata is acquired")
	default:
		var p bool
		if err := json.Unmarshal(r.Private, &p); err != nil {
			return t, fmt.Errorf("private field: %w", err)
		}
		t.Private = domain.KnownValue(p)
	}
	t.ClientMessage = domain.UnsupportedValue[string]("qBittorrent does not report error details in the torrent list")
	return t, nil
}
