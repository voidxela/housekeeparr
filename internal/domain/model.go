package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StateClass is Housekeeparr's normalized interpretation of a native client
// state. The native state string is always retained alongside it.
type StateClass string

const (
	StateQueued      StateClass = "queued"      // waiting for an active slot
	StatePaused      StateClass = "paused"      // user or scheduler pause/stop
	StateChecking    StateClass = "checking"    // hash check / verification / resume-data check
	StateMetadata    StateClass = "metadata"    // acquiring metadata (magnet)
	StateDownloading StateClass = "downloading" // active, incomplete (including client "stalled")
	StateComplete    StateClass = "complete"    // all wanted data present (seeding or finished)
	StateError       StateClass = "error"       // client-reported error, including missing files
	StateTransient   StateClass = "transient"   // moving, allocating, or similar short-lived work
	StateUnknown     StateClass = "unknown"     // not recognised by this adapter version
)

// Describe returns a short human label for a state class.
func (c StateClass) Describe() string {
	switch c {
	case StateQueued:
		return "Queued"
	case StatePaused:
		return "Paused"
	case StateChecking:
		return "Checking"
	case StateMetadata:
		return "Fetching metadata"
	case StateDownloading:
		return "Downloading"
	case StateComplete:
		return "Complete"
	case StateError:
		return "Client error"
	case StateTransient:
		return "Busy"
	default:
		return "Unknown state"
	}
}

// Hashes carries every available infohash variant of a transfer, normalized
// to lowercase hex. Neither variant is assumed to exist.
type Hashes struct {
	V1 string `json:"v1,omitempty"` // SHA-1 (40 hex)
	V2 string `json:"v2,omitempty"` // SHA-256 (64 hex)
}

// NormalizeHash lowercases and trims a hash-like identifier. It does not
// convert between encodings (e.g. base32 magnet hashes); callers that see
// other encodings must convert explicitly.
func NormalizeHash(h string) string {
	return strings.ToLower(strings.TrimSpace(h))
}

// Matches reports whether a normalized identifier equals any known variant.
// qBittorrent reports hybrid/v2 torrents by a truncated (40 hex) v2 hash, so
// a 40-character prefix of V2 is also accepted.
func (h Hashes) Matches(id string) bool {
	id = NormalizeHash(id)
	if id == "" {
		return false
	}
	if id == h.V1 || id == h.V2 {
		return true
	}
	return len(id) == 40 && len(h.V2) == 64 && strings.HasPrefix(h.V2, id)
}

// Transfer is one physical download as reported by a client in a single
// observation. ClientID + NativeID is the stable identity.
type Transfer struct {
	ClientID    string     `json:"client_id"`
	NativeID    string     `json:"native_id"`
	Hashes      Hashes     `json:"hashes"`
	Name        string     `json:"name"`
	NativeState string     `json:"native_state"`
	StateClass  StateClass `json:"state_class"`

	Category Signal[string]    `json:"category"`
	AddedAt  Signal[time.Time] `json:"added_at"`

	// WantedBytes is the size of the selected (wanted) payload.
	WantedBytes Signal[int64] `json:"wanted_bytes"`
	// VerifiedBytes is wanted payload confirmed by hash check; the preferred
	// progress measure. Network traffic is deliberately kept separate.
	VerifiedBytes  Signal[int64] `json:"verified_bytes"`
	RemainingBytes Signal[int64] `json:"remaining_bytes"`
	TotalBytes     Signal[int64] `json:"total_bytes"`
	// NetworkDownloadedBytes counts traffic, including discarded/corrupt
	// data. It is never used as a progress measure.
	NetworkDownloadedBytes Signal[int64] `json:"network_downloaded_bytes"`
	DownloadRate           Signal[int64] `json:"download_rate"`

	// Connected* are peers this client is currently connected to.
	ConnectedSeeds Signal[int64] `json:"connected_seeds"`
	ConnectedPeers Signal[int64] `json:"connected_peers"`
	// Swarm* are tracker/DHT reported counts; they may be stale and do not
	// prove that required pieces are available.
	SwarmSeeds Signal[int64] `json:"swarm_seeds"`
	SwarmPeers Signal[int64] `json:"swarm_peers"`
	// Availability is the client's piece-availability figure (for
	// qBittorrent: distributed copies among connected peers). Its Note
	// carries the source-specific meaning.
	Availability Signal[float64] `json:"availability"`
	// AvailabilityWantedOnly is true when the availability figure already
	// covers only wanted data (e.g. Transmission's desiredAvailable), so
	// deselected files cannot distort it.
	AvailabilityWantedOnly bool `json:"availability_wanted_only,omitempty"`
	// AllFilesWanted is true when no files are deselected. Piece availability
	// figures computed over all pieces are only specific to wanted data when
	// this is known true.
	AllFilesWanted Signal[bool] `json:"all_files_wanted"`
	Private        Signal[bool] `json:"private"`
	// ClientMessage is a client-provided error or status message, if any.
	ClientMessage Signal[string] `json:"client_message"`
}

// Progress returns verified/wanted when both are known and wanted > 0.
func (t Transfer) Progress() (float64, bool) {
	w, okW := t.WantedBytes.Get()
	v, okV := t.VerifiedBytes.Get()
	if !okW || !okV || w <= 0 {
		return 0, false
	}
	return float64(v) / float64(w), true
}

// TransferSnapshot is the result of one successful listing.
type TransferSnapshot struct {
	ClientID    string     `json:"client_id"`
	CollectedAt time.Time  `json:"collected_at"`
	Transfers   []Transfer `json:"transfers"`
	// Complete is true only when the listing is known to contain every
	// transfer. Only complete listings may establish disappearance.
	Complete    bool     `json:"complete"`
	Limitations []string `json:"limitations,omitempty"`
}

// StatusMessage is a manager-provided diagnostic message group.
type StatusMessage struct {
	Title    string   `json:"title"`
	Messages []string `json:"messages"`
}

// QueueItem is one manager queue row. The row ID is a transient reference;
// identity comes from the manager instance, content IDs, and download ID.
type QueueItem struct {
	ManagerID      string `json:"manager_id"`
	QueueID        int64  `json:"queue_id"`
	DownloadID     string `json:"download_id"` // normalized
	Protocol       string `json:"protocol"`
	DownloadClient string `json:"download_client"` // client name as configured in the manager
	Indexer        string `json:"indexer,omitempty"`
	Title          string `json:"title"`

	ContentKind   string        `json:"content_kind"` // "episode" or "movie"
	SeriesID      int64         `json:"series_id,omitempty"`
	SeriesTitle   string        `json:"series_title,omitempty"`
	EpisodeID     int64         `json:"episode_id,omitempty"`
	SeasonNumber  Signal[int64] `json:"season_number"`
	EpisodeNumber Signal[int64] `json:"episode_number"`
	EpisodeTitle  string        `json:"episode_title,omitempty"`
	MovieID       int64         `json:"movie_id,omitempty"`
	MovieTitle    string        `json:"movie_title,omitempty"`
	Year          Signal[int64] `json:"year"`

	Status         string          `json:"status"`
	TrackedState   Signal[string]  `json:"tracked_state"`
	TrackedStatus  Signal[string]  `json:"tracked_status"`
	StatusMessages []StatusMessage `json:"status_messages,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`

	SizeBytes      Signal[int64]     `json:"size_bytes"`
	RemainingBytes Signal[int64]     `json:"remaining_bytes"`
	Added          Signal[time.Time] `json:"added"`
}

// ContentLabel renders a compact human label such as "Show S01E02".
func (q QueueItem) ContentLabel() string {
	if q.ContentKind == "movie" {
		if q.MovieTitle == "" {
			return q.Title
		}
		if y, ok := q.Year.Get(); ok {
			return fmt.Sprintf("%s (%d)", q.MovieTitle, y)
		}
		return q.MovieTitle
	}
	label := q.SeriesTitle
	if label == "" {
		label = q.Title
	}
	if s, ok := q.SeasonNumber.Get(); ok {
		if e, ok := q.EpisodeNumber.Get(); ok {
			return label + " " + episodeCode(s, e)
		}
	}
	return label
}

// ContentKey is the manager-scoped identity of the content a queue row is
// for: "episode:<id>" or "movie:<id>". It is independent of the release
// (download ID) and of the transient queue row, so replacement attempts for
// the same episode or movie share it. ok is false when the manager did not
// report the episode or movie ID; a series ID or title is not a substitute.
func (q QueueItem) ContentKey() (string, bool) {
	switch {
	case q.ContentKind == "movie" && q.MovieID > 0:
		return fmt.Sprintf("movie:%d", q.MovieID), true
	case q.ContentKind == "episode" && q.EpisodeID > 0:
		return fmt.Sprintf("episode:%d", q.EpisodeID), true
	}
	return "", false
}

// ParseContentKey splits a content key into its kind and manager ID.
func ParseContentKey(key string) (kind string, id int64, ok bool) {
	kind, num, found := strings.Cut(key, ":")
	if !found || (kind != "episode" && kind != "movie") {
		return "", 0, false
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != num {
		return "", 0, false
	}
	return kind, n, true
}

func episodeCode(s, e int64) string { return fmt.Sprintf("S%02dE%02d", s, e) }

// QueueSnapshot is the result of one successful queue read.
type QueueSnapshot struct {
	ManagerID    string      `json:"manager_id"`
	CollectedAt  time.Time   `json:"collected_at"`
	Items        []QueueItem `json:"items"`
	TotalRecords int         `json:"total_records"`
	// Complete is false when pagination limits truncated the listing.
	Complete    bool     `json:"complete"`
	Limitations []string `json:"limitations,omitempty"`
}

// HealthItem is one manager health check result.
type HealthItem struct {
	Source  string `json:"source"`
	Type    string `json:"type"` // ok, notice, warning, error
	Message string `json:"message"`
	WikiURL string `json:"wiki_url,omitempty"`
}

// HealthSnapshot is the result of one successful health read.
type HealthSnapshot struct {
	ManagerID   string       `json:"manager_id"`
	CollectedAt time.Time    `json:"collected_at"`
	Items       []HealthItem `json:"items"`
}
