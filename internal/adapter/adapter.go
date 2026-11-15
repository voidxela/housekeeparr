// Package adapter defines the contract between Housekeeparr and external
// applications. Adapters normalize facts and report capabilities; they
// contain no policy decisions.
//
// This milestone is read-only: the interfaces below expose no operational
// write methods at all, so no code path can mutate a manager or client.
// Write capabilities are still reported (as not implemented) so the
// interface can explain what each workflow will require.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// ErrorClass classifies an adapter failure. Callers use the class to decide
// on retry and backoff; they never inspect transport errors directly.
type ErrorClass string

const (
	ErrTransient    ErrorClass = "transient"         // network failure, timeout on a read, 5xx
	ErrPermanent    ErrorClass = "permanent"         // malformed response, oversized response, bad request
	ErrRateLimited  ErrorClass = "rate_limited"      // 429 or equivalent; honor RetryAfter
	ErrUnauthorized ErrorClass = "unauthorized"      // bad or missing credentials; no rapid retry
	ErrUnsupported  ErrorClass = "unsupported"       // endpoint/version/feature not available
	ErrUncertain    ErrorClass = "uncertain_outcome" // a write may or may not have taken effect
)

// Describe gives a short operator-facing explanation of an error class.
func (c ErrorClass) Describe() string {
	switch c {
	case ErrTransient:
		return "temporarily unreachable"
	case ErrPermanent:
		return "returned an unusable response"
	case ErrRateLimited:
		return "rate limited"
	case ErrUnauthorized:
		return "rejected the credentials"
	case ErrUnsupported:
		return "does not support a required operation or version"
	case ErrUncertain:
		return "outcome uncertain"
	default:
		return "failed"
	}
}

// Error is the only error type adapters return for remote failures. Its
// message is sanitized: no credentials, query strings, or tracker URLs.
type Error struct {
	Class      ErrorClass
	Op         string // e.g. "qbittorrent.torrents_info"
	Message    string
	HTTPStatus int
	RetryAfter time.Duration
	Err        error // underlying cause; must also be sanitized before display
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s: %s", e.Op, e.Class, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Class)
}

func (e *Error) Unwrap() error { return e.Err }

// ClassOf returns the class of err, treating unclassified errors as
// transient so they are retried with backoff rather than trusted.
func ClassOf(err error) ErrorClass {
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Class
	}
	return ErrTransient
}

// Support is a capability's availability.
type Support string

const (
	Supported      Support = "supported"
	NotSupported   Support = "unsupported"
	SupportUnknown Support = "unknown"
	// NotImplemented means the remote application may support it but this
	// Housekeeparr build does not implement it.
	NotImplemented Support = "not_implemented"
)

// Capability names a read signal or write operation.
type Capability string

// Read capabilities.
const (
	CapTransferList      Capability = "transfer_list"
	CapVerifiedProgress  Capability = "verified_progress"
	CapPieceAvailability Capability = "piece_availability"
	CapPrivacy           Capability = "privacy"
	CapMetadataState     Capability = "metadata_state"
	CapFileSelection     Capability = "file_selection"
	CapSwarmCounts       Capability = "swarm_counts"
	CapInfohashV2        Capability = "infohash_v2"
	CapQueue             Capability = "queue"
	CapHealth            Capability = "health"
	CapImportHistory     Capability = "import_history"
	CapClientFileList    Capability = "client_file_list"
)

// Write capabilities (none are implemented in this milestone).
const (
	CapFailGrab      Capability = "fail_grab"      // manager: mark grab failed / blocklist
	CapDetachQueue   Capability = "detach_queue"   // manager: remove queue association, keep data
	CapSearchCommand Capability = "search_command" // manager: issue a search
	CapStopTransfer  Capability = "stop_transfer"  // client: stop/pause while retaining data
	CapTransferLabel Capability = "transfer_label" // client: tag/label for separation
)

// CapabilityStatus is a capability's support and an optional explanation.
type CapabilityStatus struct {
	Support Support `json:"support"`
	Note    string  `json:"note,omitempty"`
}

// Capabilities maps capability names to status.
type Capabilities map[Capability]CapabilityStatus

// Compatibility records whether the detected version has been validated.
type Compatibility string

const (
	CompatTested      Compatibility = "tested"      // version within the tested matrix
	CompatUntested    Compatibility = "untested"    // reads attempted; claims withheld
	CompatUnsupported Compatibility = "unsupported" // below minimum or incompatible API
)

// Probe describes a connection after a successful handshake.
type Probe struct {
	App           string        `json:"app"`
	Version       string        `json:"version"`
	APIVersion    string        `json:"api_version,omitempty"`
	Transport     string        `json:"transport"`
	Auth          string        `json:"auth"` // e.g. "api key accepted", "session login accepted", "not required (bypassed by client)"
	Compatibility Compatibility `json:"compatibility"`
	CompatNote    string        `json:"compat_note,omitempty"`
	Read          Capabilities  `json:"read"`
	Write         Capabilities  `json:"write"`
	Limitations   []string      `json:"limitations,omitempty"`
}

// Kind identifies an adapter implementation.
type Kind string

const (
	KindSonarr       Kind = "sonarr"
	KindRadarr       Kind = "radarr"
	KindQBittorrent  Kind = "qbittorrent"
	KindTransmission Kind = "transmission"
	KindRTorrent     Kind = "rtorrent"
	KindDeluge       Kind = "deluge"
)

// Role distinguishes managers from download clients.
type Role string

const (
	RoleManager Role = "manager"
	RoleClient  Role = "client"
)

// RoleOf returns the role of an adapter kind.
func RoleOf(k Kind) Role {
	switch k {
	case KindSonarr, KindRadarr:
		return RoleManager
	default:
		return RoleClient
	}
}

// Connection is common to every adapter.
type Connection interface {
	ID() string
	Kind() Kind
	// Probe authenticates and detects version and capabilities.
	Probe(ctx context.Context) (Probe, error)
}

// DownloadClient is a read-only torrent client adapter.
type DownloadClient interface {
	Connection
	// ListTransfers returns every transfer, or an *Error. A failed read
	// never yields an empty snapshot.
	ListTransfers(ctx context.Context) (domain.TransferSnapshot, error)
}

// MediaManager is a read-only media manager adapter.
type MediaManager interface {
	Connection
	Queue(ctx context.Context) (domain.QueueSnapshot, error)
	Health(ctx context.Context) (domain.HealthSnapshot, error)
}

// ReadOnlyWrites returns the write capability set for this milestone.
func ReadOnlyWrites(caps ...Capability) Capabilities {
	out := Capabilities{}
	for _, c := range caps {
		out[c] = CapabilityStatus{Support: NotImplemented, Note: "operational writes are disabled in this milestone"}
	}
	return out
}
