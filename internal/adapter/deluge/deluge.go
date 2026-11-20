// Package deluge is an adapter for Deluge 2.x through the deluge-web JSON API
// (POST /json). The daemon's own rencode/TLS protocol is deliberately not
// supported. deluge-web must already be connected to a daemon; Housekeeparr
// never changes the web session's daemon connection.
package deluge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	hc, err := httpx.New(o.BaseURL, httpx.Options{Timeout: o.Timeout, CAFile: o.CAFile, CookieJar: true, MaxResponseBytes: 64 << 20, UserAgent: "Housekeeparr"})
	if err != nil {
		return nil, err
	}
	return &Client{id: o.ID, http: hc, password: o.Password}, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindDeluge }

// allowed lists the only JSON API methods this adapter may call. auth.login
// creates a web session; it changes nothing in the daemon. writeMethods marks
// methods whose timeout could leave an external mutation in doubt.
var (
	allowed = map[string]bool{
		"auth.login":               true,
		"web.connected":            true,
		"daemon.get_version":       true,
		"core.get_torrents_status": true,
		"core.get_torrent_status":  true,
		"core.pause_torrent":       true,
	}
	writeMethods = map[string]bool{
		"core.pause_torrent": true,
	}
)

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
		return nil, &adapter.Error{Class: adapter.ErrUnsupported, Op: op, Message: "method not permitted"}
	}
	c.seq++
	// deluge-web unpacks params with *params, so an empty call must send
	// [] rather than null (observed on Deluge 2.2.0).
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"method": method, "params": params, "id": c.seq})
	resp, err := c.http.Do(ctx, httpx.Request{Op: op, Method: http.MethodPost, Path: "/json", Write: writeMethods[method],
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
		if rerr.Code == errNotAuthenticated {
			if attempt == 0 {
				// deluge-web returns error code 1 ("Not authenticated") before
				// dispatching the RPC method, so nothing was executed and
				// re-logging in and resending once is safe even for writes.
				c.loggedIn = false
				if err := c.login(ctx); err != nil {
					return err
				}
				continue
			}
			return &adapter.Error{Class: adapter.ErrUnauthorized, Op: "deluge." + method, Message: "session could not be re-established"}
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
//
// 2.2.0 (Debian trixie build, deluged + deluge-web, libtorrent 2.0.11)
// passed on 2026-11-15; see docs/compatibility.md.
var TestedVersions = []string{"2.2.0"}

// Probe detects the daemon version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	if err := c.ensureConnected(ctx); err != nil {
		return adapter.Probe{}, err
	}
	var version string
	if err := c.call(ctx, "daemon.get_version", nil, &version); err != nil {
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
		adapter.CapClientFileList:    {Support: adapter.Supported, Note: "core.get_torrent_status files and file_priorities, read for a bounded number of settled import candidates"},
		adapter.CapSwarmCounts:       {Support: adapter.Supported, Note: "tracker-reported counts; may be stale"},
		adapter.CapInfohashV2:        {Support: adapter.NotSupported, Note: "torrent id is the v1 infohash"},
	}
	p.Write = adapter.ReadOnlyWrites(adapter.CapTransferLabel)
	p.Write[adapter.CapStopTransfer] = adapter.CapabilityStatus{Support: adapter.Supported, Note: "core.pause_torrent, retaining data"}
	p.Limitations = []string{
		"Requires deluge-web to stay connected to the daemon.",
		"Piece availability reflects connected peers only; zero is indistinguishable from not computed.",
		"Tracker URLs and paths are never requested; tracker status text is classified locally and never stored.",
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

// keys requested. tracker_status is requested only to classify a tracker
// rejection into SourceFailure and is never stored; trackers, tracker_host,
// save_path, download_location, comment, files, and peers remain excluded.
var keys = []string{"hash", "name", "state", "message", "tracker_status", "time_added", "total_wanted", "total_done", "total_size", "progress",
	"all_time_download", "download_payload_rate", "num_seeds", "num_peers", "total_seeds", "total_peers",
	"distributed_copies", "private", "is_finished", "file_priorities", "num_pieces", "label"}

type status struct {
	Hash              string   `json:"hash"`
	Name              string   `json:"name"`
	State             string   `json:"state"`
	Message           string   `json:"message"`
	TrackerStatus     *string  `json:"tracker_status"`
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
	// Only an explicit object (possibly empty) is a complete listing; a
	// null or missing result is malformed.
	var res map[string]status
	if err := c.call(ctx, "core.get_torrents_status", []any{map[string]any{}, keys}, &res); err != nil {
		return domain.TransferSnapshot{}, err
	}
	if res == nil {
		return domain.TransferSnapshot{}, httpx.Malformed("deluge.core.get_torrents_status", fmt.Errorf("result is null or missing"))
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
	// A paused torrent may keep its last tracker status; the diagnosis only
	// uses SourceFailure while the torrent is actively downloading (SPEC §7.2),
	// so the adapter does not need to suppress it.
	switch {
	case s.State == "Error":
		t.SourceFailure = domain.UnknownValue[domain.SourceFailure]("a local error hides any tracker result")
	case s.TrackerStatus == nil:
		t.SourceFailure = domain.UnknownValue[domain.SourceFailure]("tracker status not reported")
	default:
		if msg, ok := strings.CutPrefix(strings.TrimSpace(*s.TrackerStatus), "Error:"); ok {
			t.SourceFailure = domain.KnownValue(domain.ClassifyTrackerError(msg))
		} else {
			t.SourceFailure = domain.KnownValue(domain.SourceNoFailure)
		}
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

// Stop pauses one torrent via core.pause_torrent, retaining the torrent
// registration and all of its data. Deluge 2.x pause_torrent takes a list of
// torrent IDs as its single positional parameter. It is a write: a timeout is
// an uncertain outcome. Callers verify the effect by listing.
func (c *Client) Stop(ctx context.Context, nativeID string) error {
	return c.call(ctx, "core.pause_torrent", []any{[]string{nativeID}}, nil)
}
