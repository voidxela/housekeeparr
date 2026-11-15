// Package deluge is a read-only adapter for Deluge 2.x through the
// deluge-web JSON API (POST /json). The daemon's own rencode/TLS protocol is
// deliberately not supported. deluge-web must already be connected to a
// daemon; Housekeeparr never changes the web session's daemon connection.
package deluge

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/httpx"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/redact"
)

// AvailabilityMeaning qualifies Deluge's availability figure (Deluge 2.1.1
// torrent.py: 'distributed_copies': max(0.0, status.distributed_copies)).
const AvailabilityMeaning = "Deluge availability is libtorrent's distributed-copies figure (this client plus connected peers, all pieces). Deluge reports libtorrent's 'not computed' value as 0, so 0 is treated as unknown."

// Options configure the adapter.
type Options struct {
	ID       string
	BaseURL  string // deluge-web base URL, including any reverse-proxy path
	Password string
	Timeout  time.Duration
	CAFile   string
}

// Client implements adapter.DownloadClient.
type Client struct {
	id       string
	http     *httpx.Client
	password string

	mu       sync.Mutex
	loggedIn bool
	seq      int
}

// New creates a Deluge adapter.
func New(o Options) (*Client, error) {
	hc, err := httpx.New(o.BaseURL, httpx.Options{Timeout: o.Timeout, CAFile: o.CAFile, CookieJar: true, MaxResponseBytes: 64 << 20, UserAgent: "Housekeeparr (read-only)"})
	if err != nil {
		return nil, err
	}
	return &Client{id: o.ID, http: hc, password: o.Password}, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindDeluge }

// allowed lists the only JSON API methods this adapter may call. auth.login
// creates a web session; it changes nothing in the daemon.
var allowed = map[string]bool{"auth.login": true, "web.connected": true, "daemon.info": true, "core.get_torrents_status": true}

type rpcError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// errNotAuthenticated is Deluge's JSON API error code for a missing session.
const errNotAuthenticated = 1

func (c *Client) rawCall(ctx context.Context, method string, params []any, out any) (*rpcError, error) {
	op := "deluge." + method
	if !allowed[method] {
		return nil, &adapter.Error{Class: adapter.ErrUnsupported, Op: op, Message: "method not permitted in this milestone"}
	}
	c.seq++
	body, _ := json.Marshal(map[string]any{"method": method, "params": params, "id": c.seq})
	resp, err := c.http.Do(ctx, httpx.Request{Op: op, Method: http.MethodPost, Path: "/json",
		Header: http.Header{"Content-Type": {"application/json"}}, Body: bytes.NewReader(body)})
	if err != nil {
		return nil, err
	}
	var r response
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, httpx.Malformed(op, err)
	}
	if r.Error != nil {
		return r.Error, nil
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return nil, httpx.Malformed(op, err)
		}
	}
	return nil, nil
}

func (c *Client) login(ctx context.Context) error {
	var ok bool
	rerr, err := c.rawCall(ctx, "auth.login", []any{c.password}, &ok)
	if err != nil {
		return err
	}
	if rerr != nil || !ok {
		return &adapter.Error{Class: adapter.ErrUnauthorized, Op: "deluge.auth.login", Message: "deluge-web rejected the password"}
	}
	c.loggedIn = true
	return nil
}

// call performs an authenticated call, re-establishing the session once.
func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loggedIn {
		if err := c.login(ctx); err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		rerr, err := c.rawCall(ctx, method, params, out)
		if err != nil {
			return err
		}
		if rerr == nil {
			return nil
		}
		if rerr.Code == errNotAuthenticated && attempt == 0 {
			c.loggedIn = false
			if err := c.login(ctx); err != nil {
				return err
			}
			continue
		}
		return &adapter.Error{Class: adapter.ErrPermanent, Op: "deluge." + method, Message: redact.Truncate(redact.String(rerr.Message), 200)}
	}
	return &adapter.Error{Class: adapter.ErrUnauthorized, Op: "deluge." + method, Message: "session could not be re-established"}
}

func (c *Client) ensureConnected(ctx context.Context) error {
	var connected bool
	if err := c.call(ctx, "web.connected", nil, &connected); err != nil {
		return err
	}
	if !connected {
		return &adapter.Error{Class: adapter.ErrUnsupported, Op: "deluge.web.connected",
			Message: "deluge-web is not connected to a Deluge daemon; connect it in the web UI's Connection Manager (Housekeeparr does not change this)"}
	}
	return nil
}

// TestedVersions lists released versions whose read path passed the
// disposable-instance integration suite. Only add after a real run.
var TestedVersions = []string{}

// Probe detects the daemon version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	if err := c.ensureConnected(ctx); err != nil {
		return adapter.Probe{}, err
	}
	var version string
	if err := c.call(ctx, "daemon.info", nil, &version); err != nil {
		return adapter.Probe{}, err
	}
	p := adapter.Probe{App: "Deluge", Version: version, Transport: "deluge-web JSON API over HTTP(S)", Auth: "web password accepted"}
	major, _ := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	switch {
	case major < 2:
		p.Compatibility, p.CompatNote = adapter.CompatUnsupported, "Deluge 1.x is not supported"
	case contains(TestedVersions, version):
		p.Compatibility, p.CompatNote = adapter.CompatTested, "read workflow validated against this released version"
	default:
		p.Compatibility, p.CompatNote = adapter.CompatUntested, "observation proceeds; this version has not been validated by the integration suite"
	}
	p.Read = adapter.Capabilities{
		adapter.CapTransferList:      {Support: adapter.Supported, Note: "core.get_torrents_status for all torrents"},
		adapter.CapVerifiedProgress:  {Support: adapter.Supported, Note: "total_done when every file is wanted; otherwise progress × total_wanted (float precision)"},
		adapter.CapPieceAvailability: {Support: adapter.Supported, Note: AvailabilityMeaning},
		adapter.CapPrivacy:           {Support: adapter.Supported, Note: "private; reported as false without metadata, so treated as unknown then"},
		adapter.CapMetadataState:     {Support: adapter.Supported, Note: "num_pieces = 0 means metadata is not yet available (no dedicated state)"},
		adapter.CapFileSelection:     {Support: adapter.Supported, Note: "file_priorities"},
		adapter.CapSwarmCounts:       {Support: adapter.Supported, Note: "tracker-reported counts; may be stale"},
		adapter.CapInfohashV2:        {Support: adapter.NotSupported, Note: "torrent id is the v1 infohash"},
		adapter.CapClientFileList:    {Support: adapter.NotImplemented},
	}
	p.Write = adapter.ReadOnlyWrites(adapter.CapStopTransfer, adapter.CapTransferLabel)
	p.Limitations = []string{
		"Requires deluge-web to stay connected to the daemon.",
		"Piece availability reflects connected peers only; zero is indistinguishable from not computed.",
		"Tracker URLs, tracker status text, and paths are never requested.",
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

// keys requested. trackers, tracker_status, tracker_host, save_path,
// download_location, comment, files, and peers are deliberately excluded.
var keys = []string{"hash", "name", "state", "message", "time_added", "total_wanted", "total_done", "total_size", "progress",
	"all_time_download", "download_payload_rate", "num_seeds", "num_peers", "total_seeds", "total_peers",
	"distributed_copies", "private", "is_finished", "file_priorities", "num_pieces", "label"}

type status struct {
	Hash              string   `json:"hash"`
	Name              string   `json:"name"`
	State             string   `json:"state"`
	Message           string   `json:"message"`
	TimeAdded         *float64 `json:"time_added"`
	TotalWanted       *int64   `json:"total_wanted"`
	TotalDone         *int64   `json:"total_done"`
	TotalSize         *int64   `json:"total_size"`
	Progress          *float64 `json:"progress"`
	AllTimeDownload   *int64   `json:"all_time_download"`
	DownloadRate      *float64 `json:"download_payload_rate"`
	NumSeeds          *int64   `json:"num_seeds"`
	NumPeers          *int64   `json:"num_peers"`
	TotalSeeds        *int64   `json:"total_seeds"`
	TotalPeers        *int64   `json:"total_peers"`
	DistributedCopies *float64 `json:"distributed_copies"`
	Private           *bool    `json:"private"`
	IsFinished        *bool    `json:"is_finished"`
	FilePriorities    []int    `json:"file_priorities"`
	NumPieces         *int64   `json:"num_pieces"`
	Label             *string  `json:"label"`
}

// ListTransfers returns every torrent.
func (c *Client) ListTransfers(ctx context.Context) (domain.TransferSnapshot, error) {
	if err := c.ensureConnected(ctx); err != nil {
		return domain.TransferSnapshot{}, err
	}
	var res map[string]status
	if err := c.call(ctx, "core.get_torrents_status", []any{map[string]any{}, keys}, &res); err != nil {
		return domain.TransferSnapshot{}, err
	}
	snap := domain.TransferSnapshot{ClientID: c.id, CollectedAt: time.Now().UTC(), Complete: true}
	for id, st := range res {
		if st.Hash == "" {
			st.Hash = id
		}
		snap.Transfers = append(snap.Transfers, normalize(c.id, st))
	}
	return snap, nil
}

func known(p *int64, f string) domain.Signal[int64] {
	if p == nil {
		return domain.UnsupportedValue[int64](f + " not reported")
	}
	return domain.KnownValue(*p)
}

func count(p *int64, f string) domain.Signal[int64] {
	if p == nil {
		return domain.UnsupportedValue[int64](f + " not reported")
	}
	if *p < 0 {
		return domain.UnknownValue[int64]("not known to the client")
	}
	return domain.KnownValue(*p)
}

func normalize(clientID string, s status) domain.Transfer {
	id := domain.NormalizeHash(s.Hash)
	t := domain.Transfer{ClientID: clientID, NativeID: id, Name: s.Name, NativeState: s.State}
	if len(id) == 40 {
		t.Hashes.V1 = id
	}
	hasMeta := s.NumPieces != nil && *s.NumPieces > 0
	finished := s.IsFinished != nil && *s.IsFinished
	switch s.State {
	case "Downloading":
		t.StateClass = domain.StateDownloading
		if !hasMeta {
			t.StateClass = domain.StateMetadata
		}
	case "Seeding":
		t.StateClass = domain.StateComplete
	case "Paused":
		t.StateClass = domain.StatePaused
		if finished {
			t.StateClass = domain.StateComplete
		}
	case "Queued":
		t.StateClass = domain.StateQueued
		if finished {
			t.StateClass = domain.StateComplete
		}
	case "Checking":
		t.StateClass = domain.StateChecking
	case "Error":
		t.StateClass = domain.StateError
	case "Allocating", "Moving":
		t.StateClass = domain.StateTransient
	default:
		t.StateClass = domain.StateUnknown
	}
	t.ClientMessage = domain.KnownValue("")
	if s.Message != "" && s.Message != "OK" {
		t.ClientMessage = domain.KnownValue(redact.Truncate(redact.String(s.Message), 300))
	}
	t.Category = domain.UnsupportedValue[string]("label plugin value not used as category")
	t.AddedAt = domain.UnknownValue[time.Time]("added time not reported")
	if s.TimeAdded != nil && *s.TimeAdded > 0 {
		t.AddedAt = domain.KnownValue(time.Unix(int64(*s.TimeAdded), 0).UTC())
	}
	allWanted := domain.UnknownValue[bool]("file priorities unavailable until metadata is acquired")
	if hasMeta && s.FilePriorities != nil {
		all := true
		for _, p := range s.FilePriorities {
			if p == 0 {
				all = false
			}
		}
		allWanted = domain.KnownValue(all)
	}
	t.AllFilesWanted = allWanted
	noMeta := func(f string) domain.Signal[int64] {
		return domain.UnknownValue[int64](f + " unavailable until metadata is acquired")
	}
	switch {
	case !hasMeta:
		t.WantedBytes, t.VerifiedBytes, t.RemainingBytes, t.TotalBytes = noMeta("wanted size"), noMeta("verified progress"), noMeta("remaining size"), noMeta("total size")
	default:
		t.WantedBytes = known(s.TotalWanted, "total_wanted")
		t.TotalBytes = known(s.TotalSize, "total_size")
		switch {
		case t.StateClass == domain.StateChecking || t.StateClass == domain.StateTransient || t.StateClass == domain.StateError:
			t.VerifiedBytes = domain.UnknownValue[int64]("progress describes the current check or move, not verified data")
		case allWanted.IsKnown() && allWanted.Value && s.TotalDone != nil:
			t.VerifiedBytes = domain.KnownValue(*s.TotalDone)
		case s.Progress != nil && s.TotalWanted != nil:
			v := int64(math.Round(*s.Progress / 100 * float64(*s.TotalWanted)))
			t.VerifiedBytes = domain.Signal[int64]{Status: domain.Known, Value: v, Note: "derived from progress × total_wanted"}
		default:
			t.VerifiedBytes = domain.UnsupportedValue[int64]("verified progress not reported")
		}
		if w, ok := t.WantedBytes.Get(); ok {
			if v, ok := t.VerifiedBytes.Get(); ok {
				t.RemainingBytes = domain.KnownValue(max(0, w-v))
			}
		}
		if !t.RemainingBytes.IsKnown() && t.RemainingBytes.Status == "" {
			t.RemainingBytes = domain.UnknownValue[int64]("not derivable")
		}
	}
	t.NetworkDownloadedBytes = known(s.AllTimeDownload, "all_time_download")
	t.DownloadRate = domain.UnsupportedValue[int64]("download_payload_rate not reported")
	if s.DownloadRate != nil {
		t.DownloadRate = domain.KnownValue(int64(*s.DownloadRate))
	}
	t.ConnectedSeeds = count(s.NumSeeds, "num_seeds")
	t.ConnectedPeers = count(s.NumPeers, "num_peers")
	t.SwarmSeeds = count(s.TotalSeeds, "total_seeds")
	t.SwarmPeers = count(s.TotalPeers, "total_peers")
	switch {
	case s.DistributedCopies == nil:
		t.Availability = domain.UnsupportedValue[float64]("distributed_copies not reported")
	case *s.DistributedCopies <= 0:
		t.Availability = domain.UnknownValue[float64]("Deluge reports 0 both for 'not computed' and for no copies")
	default:
		t.Availability = domain.Signal[float64]{Status: domain.Known, Value: *s.DistributedCopies, Note: AvailabilityMeaning}
	}
	switch {
	case s.Private == nil:
		t.Private = domain.UnsupportedValue[bool]("private not reported")
	case !hasMeta:
		t.Private = domain.UnknownValue[bool]("Deluge reports private=false until metadata is acquired")
	default:
		t.Private = domain.KnownValue(*s.Private)
	}
	return t
}
