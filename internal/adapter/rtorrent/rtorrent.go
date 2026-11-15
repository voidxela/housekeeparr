// Package rtorrent is a read-only adapter for rTorrent's XML-RPC interface.
//
// Supported transport: XML-RPC over HTTP(S), as exposed by an SCGI gateway
// (for example nginx `scgi_pass`, or ruTorrent's /RPC2), with optional HTTP
// basic authentication. Raw SCGI sockets are deliberately not supported.
// Only the read methods listed in `allowed` are ever sent.
package rtorrent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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

// Options configure the adapter.
type Options struct {
	ID       string
	BaseURL  string
	RPCPath  string // default /RPC2
	Username string
	Password string
	Timeout  time.Duration
	CAFile   string
}

// Client implements adapter.DownloadClient.
type Client struct {
	id      string
	http    *httpx.Client
	rpcPath string
	auth    string
	mu      sync.Mutex
}

// New creates an rTorrent adapter.
func New(o Options) (*Client, error) {
	hc, err := httpx.New(o.BaseURL, httpx.Options{Timeout: o.Timeout, CAFile: o.CAFile, MaxResponseBytes: 64 << 20, UserAgent: "Housekeeparr (read-only)"})
	if err != nil {
		return nil, err
	}
	if o.RPCPath == "" {
		o.RPCPath = "/RPC2"
	}
	c := &Client{id: o.ID, http: hc, rpcPath: o.RPCPath}
	if o.Username != "" || o.Password != "" {
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(o.Username+":"+o.Password))
	}
	return c, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindRTorrent }

// allowed are the only methods this adapter may call. d.multicall2 is read
// only because every command it is given comes from the fixed `fields` list.
var allowed = map[string]bool{"system.client_version": true, "system.library_version": true, "d.multicall2": true}

func (c *Client) call(ctx context.Context, method string, params ...string) (any, error) {
	op := "rtorrent." + method
	if !allowed[method] {
		return nil, &adapter.Error{Class: adapter.ErrUnsupported, Op: op, Message: "method not permitted in this milestone"}
	}
	h := http.Header{"Content-Type": {"text/xml"}, "Accept": {"text/xml"}}
	if c.auth != "" {
		h.Set("Authorization", c.auth)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	resp, err := c.http.Do(ctx, httpx.Request{Op: op, Method: http.MethodPost, Path: c.rpcPath, Header: h, Body: bytes.NewReader(encodeCall(method, params))})
	if err != nil {
		return nil, err
	}
	v, err := decodeResponse(resp.Body)
	var f *Fault
	if errors.As(err, &f) {
		return nil, &adapter.Error{Class: adapter.ErrUnsupported, Op: op, Message: redact.Truncate(redact.String(f.Error()), 200)}
	}
	if err != nil {
		return nil, httpx.Malformed(op, err)
	}
	return v, nil
}

// MinimumVersion is the oldest rTorrent whose command set provides every
// field in `fields` (verified against v0.9.8 src/command_download.cc).
const MinimumVersion = "0.9.8"

// TestedVersions lists released versions whose read path passed the
// disposable-instance integration suite. Only add after a real run.
var TestedVersions = []string{}

func versionAtLeast(v, min string) bool {
	pv, pm := strings.Split(v, "."), strings.Split(min, ".")
	for i := 0; i < len(pm); i++ {
		var a, b int
		if i < len(pv) {
			a, _ = strconv.Atoi(strings.TrimFunc(pv[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		b, _ = strconv.Atoi(pm[i])
		if a != b {
			return a > b
		}
	}
	return true
}

// Probe detects version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	v, err := c.call(ctx, "system.client_version")
	if err != nil {
		return adapter.Probe{}, err
	}
	version, _ := v.(string)
	lib := ""
	if l, err := c.call(ctx, "system.library_version"); err == nil {
		lib, _ = l.(string)
	}
	p := adapter.Probe{App: "rTorrent", Version: version, APIVersion: "libtorrent " + lib, Transport: "XML-RPC over HTTP(S) (SCGI gateway)", Auth: "accepted"}
	if c.auth == "" {
		p.Auth = "no credentials sent"
	}
	switch {
	case !versionAtLeast(version, MinimumVersion):
		p.Compatibility, p.CompatNote = adapter.CompatUnsupported, "rTorrent before "+MinimumVersion+" lacks commands this adapter reads"
	case contains(TestedVersions, version):
		p.Compatibility, p.CompatNote = adapter.CompatTested, "read workflow validated against this released version"
	default:
		p.Compatibility, p.CompatNote = adapter.CompatUntested, "observation proceeds; this version has not been validated by the integration suite"
	}
	p.Read = adapter.Capabilities{
		adapter.CapTransferList:      {Support: adapter.Supported, Note: "d.multicall2 over the main view"},
		adapter.CapVerifiedProgress:  {Support: adapter.Supported, Note: "d.completed_bytes (hash-checked chunks); wanted size is chunk-granular when files are deselected"},
		adapter.CapPieceAvailability: {Support: adapter.NotImplemented, Note: "only available as a per-chunk map (d.chunks_seen); not used"},
		adapter.CapPrivacy:           {Support: adapter.Supported, Note: "d.is_private; unknown while fetching metadata"},
		adapter.CapMetadataState:     {Support: adapter.Supported, Note: "d.is_meta"},
		adapter.CapFileSelection:     {Support: adapter.Supported, Note: "derived from d.wanted_chunks"},
		adapter.CapSwarmCounts:       {Support: adapter.NotImplemented, Note: "tracker scrape counts are per tracker (t.multicall); not used"},
		adapter.CapInfohashV2:        {Support: adapter.NotSupported},
		adapter.CapClientFileList:    {Support: adapter.NotImplemented},
	}
	p.Write = adapter.ReadOnlyWrites(adapter.CapStopTransfer, adapter.CapTransferLabel)
	p.Limitations = []string{
		"rTorrent has no structured error state; error and tracker messages are shown as text only, and storage errors are not classified.",
		"Piece availability is not collected, so stalls are never attributed to unavailable pieces.",
		"The added time is rTorrent's load time, which can reset on client restart (grace can only lengthen).",
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

// fields are the d.multicall2 commands, in result order.
var fields = []string{"d.hash=", "d.name=", "d.state=", "d.is_active=", "d.complete=", "d.hashing=", "d.is_meta=",
	"d.size_bytes=", "d.completed_bytes=", "d.left_bytes=", "d.size_chunks=", "d.completed_chunks=", "d.wanted_chunks=",
	"d.chunk_size=", "d.down.rate=", "d.down.total=", "d.peers_connected=", "d.peers_complete=", "d.is_private=",
	"d.message=", "d.custom1=", "d.load_date="}

// ListTransfers returns every torrent in the main view.
func (c *Client) ListTransfers(ctx context.Context) (domain.TransferSnapshot, error) {
	v, err := c.call(ctx, "d.multicall2", append([]string{"", "main"}, fields...)...)
	if err != nil {
		return domain.TransferSnapshot{}, err
	}
	rows, ok := v.([]any)
	if !ok {
		return domain.TransferSnapshot{}, httpx.Malformed("rtorrent.d.multicall2", fmt.Errorf("result is %T, not an array", v))
	}
	snap := domain.TransferSnapshot{ClientID: c.id, CollectedAt: time.Now().UTC(), Complete: true}
	for _, r := range rows {
		cols, ok := r.([]any)
		if !ok || len(cols) != len(fields) {
			return domain.TransferSnapshot{}, httpx.Malformed("rtorrent.d.multicall2", fmt.Errorf("row has %d columns, want %d", len(cols), len(fields)))
		}
		t, err := normalize(c.id, cols)
		if err != nil {
			return domain.TransferSnapshot{}, httpx.Malformed("rtorrent.d.multicall2", err)
		}
		snap.Transfers = append(snap.Transfers, t)
	}
	return snap, nil
}

func normalize(clientID string, c []any) (domain.Transfer, error) {
	str := func(i int) string { s, _ := c[i].(string); return s }
	num := func(i int) (int64, bool) {
		switch v := c[i].(type) {
		case int64:
			return v, true
		case bool:
			if v {
				return 1, true
			}
			return 0, true
		}
		return 0, false
	}
	n := func(i int) int64 { v, _ := num(i); return v }
	hash := domain.NormalizeHash(str(0))
	if len(hash) != 40 {
		return domain.Transfer{}, fmt.Errorf("unexpected hash %q", redact.Truncate(hash, 64))
	}
	state, active, complete, hashing, meta := n(2), n(3), n(4), n(5), n(6)
	t := domain.Transfer{ClientID: clientID, NativeID: hash, Hashes: domain.Hashes{V1: hash}, Name: str(1)}
	switch {
	case hashing != 0:
		t.StateClass, t.NativeState = domain.StateChecking, "hashing"
	case complete == 1:
		t.StateClass, t.NativeState = domain.StateComplete, map[bool]string{true: "seeding", false: "complete-stopped"}[state == 1 && active == 1]
	case state == 0:
		t.StateClass, t.NativeState = domain.StatePaused, "stopped"
	case active == 0:
		t.StateClass, t.NativeState = domain.StatePaused, "paused"
	case meta != 0:
		t.StateClass, t.NativeState = domain.StateMetadata, "meta-download"
	default:
		t.StateClass, t.NativeState = domain.StateDownloading, "downloading"
	}
	hasMeta := meta == 0
	t.Category = domain.KnownValue(str(20))
	if ld := n(21); ld > 0 {
		t.AddedAt = domain.Signal[time.Time]{Status: domain.Known, Value: time.Unix(ld, 0).UTC(), Note: "rTorrent load time"}
	} else {
		t.AddedAt = domain.UnknownValue[time.Time]("load date not reported")
	}
	if msg := str(19); msg != "" {
		t.ClientMessage = domain.KnownValue(redact.Truncate(redact.String(msg), 300))
	} else {
		t.ClientMessage = domain.KnownValue("")
	}
	if hasMeta {
		size, completed := n(7), n(8)
		sizeChunks, doneChunks, wantedChunks, chunk := n(10), n(11), n(12), n(13)
		missing := sizeChunks - doneChunks
		t.TotalBytes = domain.KnownValue(size)
		t.VerifiedBytes = domain.KnownValue(completed)
		if wantedChunks >= missing {
			t.AllFilesWanted = domain.KnownValue(true)
			t.WantedBytes = domain.KnownValue(size)
			t.RemainingBytes = domain.KnownValue(n(9))
		} else {
			t.AllFilesWanted = domain.KnownValue(false)
			w := completed + wantedChunks*chunk
			if w > size {
				w = size
			}
			t.WantedBytes = domain.Signal[int64]{Status: domain.Known, Value: w, Note: "chunk-granular estimate (some files deselected)"}
			t.RemainingBytes = domain.Signal[int64]{Status: domain.Known, Value: w - completed, Note: "chunk-granular estimate"}
		}
	} else {
		u := func(f string) domain.Signal[int64] {
			return domain.UnknownValue[int64](f + " unavailable until metadata is acquired")
		}
		t.TotalBytes, t.VerifiedBytes, t.WantedBytes, t.RemainingBytes = u("total size"), u("verified progress"), u("wanted size"), u("remaining size")
		t.AllFilesWanted = domain.UnknownValue[bool]("file list unavailable until metadata is acquired")
	}
	t.DownloadRate = domain.KnownValue(n(14))
	t.NetworkDownloadedBytes = domain.KnownValue(n(15))
	t.ConnectedPeers = domain.KnownValue(n(16))
	t.ConnectedSeeds = domain.KnownValue(n(17))
	t.SwarmSeeds = domain.UnsupportedValue[int64]("tracker scrape counts not collected")
	t.SwarmPeers = t.SwarmSeeds
	t.Availability = domain.UnsupportedValue[float64]("rTorrent exposes availability only as a per-chunk map")
	if hasMeta {
		t.Private = domain.KnownValue(n(18) != 0)
	} else {
		t.Private = domain.UnknownValue[bool]("privacy unknown until metadata is acquired")
	}
	return t, nil
}
