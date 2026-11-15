// Package transmission is a read-only adapter for the Transmission RPC
// interface. It uses the legacy (pre-JSON-RPC) protocol, which Transmission
// 3.x and 4.0 speak natively and 4.1 still accepts as deprecated. Only the
// read methods session-get and torrent-get are ever sent.
package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
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

// AvailabilityMeaning qualifies the availability figure this adapter
// derives (Transmission 4.0.6 tr_stat.desiredAvailable: "piece data we want
// and don't have yet, but that a connected peer does have").
const AvailabilityMeaning = "Transmission availability is desiredAvailable ÷ leftUntilDone: the share of still-missing wanted data that currently connected peers have. It does not describe the whole swarm."

// Options configure the adapter.
type Options struct {
	ID       string
	BaseURL  string
	RPCPath  string // default /transmission/rpc
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
	session string
	version string
	semver  string
	rpcVer  int
}

// New creates a Transmission adapter.
func New(o Options) (*Client, error) {
	hc, err := httpx.New(o.BaseURL, httpx.Options{Timeout: o.Timeout, CAFile: o.CAFile, MaxResponseBytes: 64 << 20, UserAgent: "Housekeeparr (read-only)"})
	if err != nil {
		return nil, err
	}
	if o.RPCPath == "" {
		o.RPCPath = "/transmission/rpc"
	}
	c := &Client{id: o.ID, http: hc, rpcPath: o.RPCPath}
	if o.Username != "" || o.Password != "" {
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(o.Username+":"+o.Password))
	}
	return c, nil
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindTransmission }

// readMethods are the only RPC methods this adapter may send.
var readMethods = map[string]bool{"session-get": true, "torrent-get": true}

type response struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

// call sends one read-only RPC, completing the CSRF session handshake
// (HTTP 409 with X-Transmission-Session-Id) at most once.
func (c *Client) call(ctx context.Context, method string, args any, out any) error {
	op := "transmission." + method
	if !readMethods[method] {
		return &adapter.Error{Class: adapter.ErrUnsupported, Op: op, Message: "write methods are not permitted in this milestone"}
	}
	body, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		h := http.Header{"Content-Type": {"application/json"}}
		if c.session != "" {
			h.Set("X-Transmission-Session-Id", c.session)
		}
		if c.auth != "" {
			h.Set("Authorization", c.auth)
		}
		resp, err := c.http.Do(ctx, httpx.Request{Op: op, Method: http.MethodPost, Path: c.rpcPath, Header: h, Body: bytes.NewReader(body)})
		if ae, ok := err.(*adapter.Error); ok && ae.HTTPStatus == http.StatusConflict && resp != nil && attempt == 0 {
			c.session = resp.Header.Get("X-Transmission-Session-Id")
			if c.session == "" {
				return &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: "HTTP 409 without a session id (is this a Transmission RPC endpoint?)"}
			}
			continue
		}
		if err != nil {
			return err
		}
		var r response
		if err := json.Unmarshal(resp.Body, &r); err != nil {
			return httpx.Malformed(op, err)
		}
		if r.Result != "success" {
			return &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: "RPC result: " + redact.Truncate(redact.String(r.Result), 160)}
		}
		if err := json.Unmarshal(r.Arguments, out); err != nil {
			return httpx.Malformed(op, err)
		}
		return nil
	}
	return &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: "session handshake did not converge"}
}

type session struct {
	Version          string `json:"version"`
	RPCVersion       int    `json:"rpc-version"`
	RPCVersionSemver string `json:"rpc-version-semver"`
}

// MinimumRPCVersion is RPC version 15 (Transmission 2.80): the oldest
// version whose torrent-get provides every field this adapter reads.
const MinimumRPCVersion = 15

// TestedVersions lists released versions whose read path passed the
// disposable-instance integration suite. Only add after a real run.
var TestedVersions = []string{}

// Probe detects version and capabilities.
func (c *Client) Probe(ctx context.Context) (adapter.Probe, error) {
	var s session
	if err := c.call(ctx, "session-get", map[string]any{"fields": []string{"version", "rpc-version", "rpc-version-semver"}}, &s); err != nil {
		return adapter.Probe{}, err
	}
	c.mu.Lock()
	c.version, c.semver, c.rpcVer = s.Version, s.RPCVersionSemver, s.RPCVersion
	c.mu.Unlock()
	p := adapter.Probe{App: "Transmission", Version: s.Version, APIVersion: fmt.Sprintf("rpc %d (%s)", s.RPCVersion, s.RPCVersionSemver),
		Transport: "legacy RPC over HTTP(S)", Auth: "accepted"}
	if c.auth == "" {
		p.Auth = "no credentials sent"
	}
	release := strings.Fields(s.Version)
	switch {
	case s.RPCVersion < MinimumRPCVersion:
		p.Compatibility, p.CompatNote = adapter.CompatUnsupported, fmt.Sprintf("RPC version %d is older than the minimum %d (Transmission 2.80)", s.RPCVersion, MinimumRPCVersion)
	case len(release) > 0 && contains(TestedVersions, release[0]):
		p.Compatibility, p.CompatNote = adapter.CompatTested, "read workflow validated against this released version"
	default:
		p.Compatibility, p.CompatNote = adapter.CompatUntested, "observation proceeds; this version has not been validated by the integration suite"
	}
	if semverMajor(s.RPCVersionSemver) >= 6 {
		p.Limitations = append(p.Limitations, "This Transmission version prefers JSON-RPC 2.0; Housekeeparr uses the legacy protocol, which Transmission documents as deprecated and scheduled for removal.")
	}
	p.Read = adapter.Capabilities{
		adapter.CapTransferList:      {Support: adapter.Supported, Note: "torrent-get for all torrents"},
		adapter.CapVerifiedProgress:  {Support: adapter.Supported, Note: "sizeWhenDone − leftUntilDone (completed wanted pieces)"},
		adapter.CapPieceAvailability: {Support: adapter.Supported, Note: AvailabilityMeaning},
		adapter.CapPrivacy:           {Support: adapter.Supported, Note: "isPrivate; unknown until metadata is available"},
		adapter.CapMetadataState:     {Support: adapter.Supported, Note: "metadataPercentComplete"},
		adapter.CapFileSelection:     {Support: adapter.Supported, Note: "sizeWhenDone compared with totalSize"},
		adapter.CapSwarmCounts:       {Support: adapter.NotImplemented, Note: "available only through trackerStats, which contains tracker URLs; not requested"},
		adapter.CapInfohashV2:        {Support: adapter.NotSupported, Note: "hashString is the v1 infohash"},
		adapter.CapClientFileList:    {Support: adapter.NotImplemented},
	}
	p.Write = adapter.ReadOnlyWrites(adapter.CapStopTransfer, adapter.CapTransferLabel)
	p.Limitations = append(p.Limitations, "Piece availability reflects connected peers only.", "Tracker URLs and paths are never requested.")
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

func semverMajor(s string) int {
	n, _ := strconv.Atoi(strings.SplitN(s, ".", 2)[0])
	return n
}

// fields requested from torrent-get. trackerStats, trackers, magnetLink,
// downloadDir, and files are deliberately excluded.
var fields = []string{"hashString", "name", "status", "error", "errorString", "addedDate", "sizeWhenDone", "leftUntilDone",
	"totalSize", "downloadedEver", "corruptEver", "rateDownload", "peersConnected", "peersSendingToUs", "isPrivate",
	"metadataPercentComplete", "desiredAvailable", "labels"}

type torrent struct {
	HashString       string   `json:"hashString"`
	Name             string   `json:"name"`
	Status           *int     `json:"status"`
	Error            int      `json:"error"`
	ErrorString      string   `json:"errorString"`
	AddedDate        *int64   `json:"addedDate"`
	SizeWhenDone     *int64   `json:"sizeWhenDone"`
	LeftUntilDone    *int64   `json:"leftUntilDone"`
	TotalSize        *int64   `json:"totalSize"`
	DownloadedEver   *int64   `json:"downloadedEver"`
	RateDownload     *int64   `json:"rateDownload"`
	PeersConnected   *int64   `json:"peersConnected"`
	IsPrivate        *bool    `json:"isPrivate"`
	MetadataPercent  *float64 `json:"metadataPercentComplete"`
	DesiredAvailable *int64   `json:"desiredAvailable"`
}

// ListTransfers returns every torrent.
func (c *Client) ListTransfers(ctx context.Context) (domain.TransferSnapshot, error) {
	// torrents is mandatory: a success envelope without it (or with null)
	// is malformed, not an empty client.
	var args struct {
		Torrents *[]torrent `json:"torrents"`
	}
	if err := c.call(ctx, "torrent-get", map[string]any{"fields": fields}, &args); err != nil {
		return domain.TransferSnapshot{}, err
	}
	if args.Torrents == nil {
		return domain.TransferSnapshot{}, httpx.Malformed("transmission.torrent-get", fmt.Errorf("response has no torrents list"))
	}
	list := *args.Torrents
	snap := domain.TransferSnapshot{ClientID: c.id, CollectedAt: time.Now().UTC(), Complete: true}
	for i := range list {
		t, err := normalize(c.id, &list[i])
		if err != nil {
			return domain.TransferSnapshot{}, httpx.Malformed("transmission.torrent-get", err)
		}
		snap.Transfers = append(snap.Transfers, t)
	}
	return snap, nil
}

var statusNames = map[int]string{0: "stopped", 1: "check-wait", 2: "checking", 3: "download-wait", 4: "downloading", 5: "seed-wait", 6: "seeding"}

func known(p *int64, field string) domain.Signal[int64] {
	if p == nil {
		return domain.UnsupportedValue[int64](field + " not reported")
	}
	return domain.KnownValue(*p)
}

func normalize(clientID string, r *torrent) (domain.Transfer, error) {
	id := domain.NormalizeHash(r.HashString)
	if id == "" || r.Status == nil {
		return domain.Transfer{}, fmt.Errorf("torrent without hashString or status")
	}
	// metadataPercentComplete is requested and present since RPC 15. If it
	// is absent, null, or out of range, metadata state is unknown: sizes,
	// completion, privacy, and availability are then not trusted, because
	// Transmission reports placeholder values for them before metadata.
	metaKnown := r.MetadataPercent != nil && *r.MetadataPercent >= 0 && *r.MetadataPercent <= 1
	hasMeta := metaKnown && *r.MetadataPercent >= 1
	left := int64(-1)
	if r.LeftUntilDone != nil {
		left = *r.LeftUntilDone
	}
	t := domain.Transfer{ClientID: clientID, NativeID: id, Name: r.Name, NativeState: statusNames[*r.Status]}
	if len(id) == 40 {
		t.Hashes.V1 = id
	}
	switch *r.Status {
	case 0:
		t.StateClass = domain.StatePaused
		if hasMeta && left == 0 {
			t.StateClass = domain.StateComplete
		}
	case 1, 2:
		t.StateClass = domain.StateChecking
	case 3:
		t.StateClass = domain.StateQueued
	case 4:
		switch {
		case !metaKnown:
			// Active, but whether it is fetching metadata is unknown.
			t.StateClass = domain.StateDownloading
		case !hasMeta:
			t.StateClass = domain.StateMetadata
		default:
			t.StateClass = domain.StateDownloading
		}
	case 5, 6:
		t.StateClass = domain.StateComplete
	default:
		t.StateClass, t.NativeState = domain.StateUnknown, fmt.Sprintf("status-%d", *r.Status)
	}
	// error 3 is a local error (e.g. missing files, disk); 1 and 2 are
	// tracker warnings/errors, which describe the source, not the transfer.
	if r.Error == 3 {
		t.StateClass, t.NativeState = domain.StateError, "local-error"
	}
	t.ClientMessage = domain.KnownValue("")
	if r.Error != 0 {
		t.ClientMessage = domain.KnownValue(redact.Truncate(redact.String(r.ErrorString), 300))
	}
	t.Category = domain.UnsupportedValue[string]("Transmission uses labels rather than categories")
	t.AddedAt = domain.UnknownValue[time.Time]("added time not reported")
	if r.AddedDate != nil && *r.AddedDate > 0 {
		t.AddedAt = domain.KnownValue(time.Unix(*r.AddedDate, 0).UTC())
	}
	metaNote := "unavailable until metadata is acquired"
	if !metaKnown {
		metaNote = "not trusted: metadataPercentComplete was not reported"
	}
	noMeta := func(f string) domain.Signal[int64] {
		return domain.UnknownValue[int64](f + " " + metaNote)
	}
	if hasMeta && r.SizeWhenDone != nil && left >= 0 {
		t.WantedBytes = domain.KnownValue(*r.SizeWhenDone)
		t.VerifiedBytes = domain.KnownValue(*r.SizeWhenDone - left)
		t.RemainingBytes = domain.KnownValue(left)
		t.TotalBytes = known(r.TotalSize, "totalSize")
		if r.TotalSize != nil {
			t.AllFilesWanted = domain.KnownValue(*r.SizeWhenDone >= *r.TotalSize)
		} else {
			t.AllFilesWanted = domain.UnsupportedValue[bool]("totalSize not reported")
		}
	} else {
		t.WantedBytes, t.VerifiedBytes, t.RemainingBytes, t.TotalBytes = noMeta("wanted size"), noMeta("verified progress"), noMeta("remaining size"), noMeta("total size")
		t.AllFilesWanted = domain.UnknownValue[bool]("file selection " + metaNote)
	}
	t.NetworkDownloadedBytes = known(r.DownloadedEver, "downloadedEver")
	t.DownloadRate = known(r.RateDownload, "rateDownload")
	t.ConnectedPeers = known(r.PeersConnected, "peersConnected")
	t.ConnectedSeeds = domain.UnsupportedValue[int64]("Transmission does not report connected seeds separately")
	t.SwarmSeeds = domain.UnsupportedValue[int64]("not requested (trackerStats contains tracker URLs)")
	t.SwarmPeers = t.SwarmSeeds
	t.AvailabilityWantedOnly = true
	switch {
	case r.DesiredAvailable == nil:
		t.Availability = domain.UnsupportedValue[float64]("desiredAvailable not reported")
	case !metaKnown:
		t.Availability = domain.UnknownValue[float64]("not trusted: metadataPercentComplete was not reported")
	case !hasMeta || left <= 0:
		t.Availability = domain.UnknownValue[float64]("not meaningful without metadata or missing data")
	case t.StateClass != domain.StateDownloading:
		t.Availability = domain.UnknownValue[float64]("not meaningful unless the torrent is active")
	default:
		ratio := math.Min(1, float64(*r.DesiredAvailable)/float64(left))
		t.Availability = domain.Signal[float64]{Status: domain.Known, Value: ratio, Note: AvailabilityMeaning}
	}
	switch {
	case r.IsPrivate == nil:
		t.Private = domain.UnsupportedValue[bool]("isPrivate not reported")
	case !metaKnown:
		t.Private = domain.UnknownValue[bool]("privacy unknown: metadataPercentComplete was not reported")
	case !hasMeta:
		t.Private = domain.UnknownValue[bool]("privacy unknown until metadata is acquired")
	default:
		t.Private = domain.KnownValue(*r.IsPrivate)
	}
	return t, nil
}
