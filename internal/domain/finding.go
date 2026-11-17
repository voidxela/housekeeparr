package domain

import "time"

// FindingState is the user-facing lifecycle state of a finding. It is
// independent of action state and of the observed transfer state.
type FindingState string

const (
	FindingOpen         FindingState = "open"
	FindingAcknowledged FindingState = "acknowledged"
	FindingSnoozed      FindingState = "snoozed"
	FindingResolved     FindingState = "resolved"
)

// Severity orders findings for attention.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityNotice  Severity = "notice"
)

// Rank sorts severities, most severe first.
func (s Severity) Rank() int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// Finding types produced in this milestone.
const (
	FindingConnectionFailure = "connection.failure"
	FindingConnectionVersion = "connection.unsupported_version"
	FindingManagerHealth     = "manager.health"
	FindingTransferStalled   = "transfer.stalled"
	FindingMetadataTimeout   = "transfer.metadata_timeout"
	FindingTransferError     = "transfer.client_error"
	FindingSourceRejected    = "transfer.source_rejected"
	FindingPartialPack       = "import.partial_pack"
	FindingImportAttention   = "import.attention"
	FindingQueueUnmatched    = "queue.unmatched"
	FindingQueueAmbiguous    = "queue.ambiguous_ownership"
)

// Subject identifies what a finding is about.
type Subject struct {
	Connections  []string `json:"connections,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	NativeID     string   `json:"native_id,omitempty"`
	TransferName string   `json:"transfer_name,omitempty"`
	ManagerID    string   `json:"manager_id,omitempty"`
	DownloadID   string   `json:"download_id,omitempty"`
	Content      []string `json:"content,omitempty"` // human labels, e.g. "Show S01E02"
	Source       string   `json:"source,omitempty"`  // health check source, etc.
}

// Evidence is one fact supporting a finding.
type Evidence struct {
	Label  string     `json:"label"`
	Value  string     `json:"value"`
	At     *time.Time `json:"at,omitempty"`
	Source string     `json:"source,omitempty"`
}

// Link points to an affected application or documentation.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// FindingDetail is the explanatory body of a finding.
type FindingDetail struct {
	Evidence    []Evidence `json:"evidence,omitempty"`
	Missing     []string   `json:"missing,omitempty"`
	Confidence  string     `json:"confidence,omitempty"`
	Limitations []string   `json:"limitations,omitempty"`
	// Eligibility explains whether any action could follow and why not.
	Eligibility string `json:"eligibility,omitempty"`
	// ProposedEffects lists exact intended external effects; empty in
	// observe-only operation.
	ProposedEffects []string `json:"proposed_effects,omitempty"`
	NextSteps       []string `json:"next_steps,omitempty"`
	Links           []Link   `json:"links,omitempty"`
	// CurrentState explains how the situation changed since the condition
	// was established, when the finding stays open without currently
	// re-qualifying (for example a stalled transfer that is now paused).
	CurrentState string `json:"current_state,omitempty"`
	// Baseline holds machine-readable values captured when the condition
	// was established; resolution compares fresh evidence against them.
	Baseline map[string]int64 `json:"baseline,omitempty"`
}

// Finding is a persistent, user-facing assessment.
type Finding struct {
	ID       int64         `json:"id"`
	DedupKey string        `json:"dedup_key"`
	Type     string        `json:"type"`
	Severity Severity      `json:"severity"`
	State    FindingState  `json:"state"`
	Title    string        `json:"title"`
	Summary  string        `json:"summary"`
	Subject  Subject       `json:"subject"`
	Detail   FindingDetail `json:"detail"`

	PolicyID       string `json:"policy_id,omitempty"`
	PolicyRevision string `json:"policy_revision,omitempty"`

	FirstSeen        time.Time  `json:"first_seen"`
	LastObserved     time.Time  `json:"last_observed"`
	LastEvaluated    time.Time  `json:"last_evaluated"`
	EvidenceAt       time.Time  `json:"evidence_at"`
	NextEvaluationAt *time.Time `json:"next_evaluation_at,omitempty"`
	AcknowledgedAt   *time.Time `json:"acknowledged_at,omitempty"`
	SnoozedUntil     *time.Time `json:"snoozed_until,omitempty"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	Resolution       string     `json:"resolution,omitempty"`
	PreviousID       *int64     `json:"previous_id,omitempty"`
	Occurrence       int        `json:"occurrence"`
}

// FindingEvent is one lifecycle entry.
type FindingEvent struct {
	ID        int64     `json:"id"`
	FindingID int64     `json:"finding_id"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`  // opened, recurred, updated, acknowledged, unacknowledged, snoozed, snooze_expired, unsnoozed, resolved
	Actor     string    `json:"actor"` // system, cli, web
	Detail    string    `json:"detail"`
}

// PartialPackMessage is released Sonarr 4.0.20's status message for a pack
// whose expected episodes were not all imported, and AlreadyImportedPrefix
// the per-file message for a file it already imported (observed; see
// docs/compatibility.md, "Partial season-pack probe").
const (
	PartialPackMessage    = "One or more episodes expected in this release were not imported or missing from the release"
	AlreadyImportedPrefix = "Episode file already imported at "
)
