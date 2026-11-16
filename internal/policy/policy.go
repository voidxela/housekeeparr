// Package policy is the pure decision boundary: given observation history,
// effective settings, and a clock reading, it returns assessments and
// explanations. It performs no I/O and holds no state, so the CLI, web
// interface, daemon, and tests all share exactly the same decisions.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Settings are the effective diagnosis settings for one scope.
type Settings struct {
	PollInterval       time.Duration `json:"poll_interval"`
	NewDownloadGrace   time.Duration `json:"new_download_grace"`
	NoProgressWindow   time.Duration `json:"no_progress_window"`
	NearCompleteRatio  float64       `json:"near_complete_ratio"`
	NearCompleteWindow time.Duration `json:"near_complete_window"`
	MinObservations    int           `json:"min_observations"`
	MaxObservationGap  time.Duration `json:"max_observation_gap"`
	MetadataTimeout    time.Duration `json:"metadata_timeout"`
	ImportSettle       time.Duration `json:"import_settle"`
	MinProgressBytes   int64         `json:"min_progress_bytes"`
	// ClockTolerance bounds disagreement between wall-clock and monotonic
	// elapsed time within one process before a clock jump is declared.
	ClockTolerance time.Duration `json:"clock_tolerance"`
}

// Proposed defaults from the specification (section 5.1). These are
// starting proposals, not validated recommendations.
func DefaultSettings() Settings {
	return Settings{
		PollInterval:       10 * time.Minute,
		NewDownloadGrace:   30 * time.Minute,
		NoProgressWindow:   90 * time.Minute,
		NearCompleteRatio:  0.95,
		NearCompleteWindow: 4 * time.Hour,
		MinObservations:    3,
		MaxObservationGap:  20 * time.Minute,
		MetadataTimeout:    60 * time.Minute,
		ImportSettle:       30 * time.Minute,
		MinProgressBytes:   1,
		ClockTolerance:     2 * time.Minute,
	}
}

// Validate checks internal consistency.
func (s Settings) Validate() error {
	switch {
	case s.PollInterval <= 0:
		return fmt.Errorf("poll interval must be positive")
	case s.NewDownloadGrace < 0:
		return fmt.Errorf("new_download_grace must not be negative")
	case s.NoProgressWindow <= 0 || s.NearCompleteWindow <= 0 || s.MetadataTimeout <= 0 || s.ImportSettle <= 0:
		return fmt.Errorf("diagnosis windows must be positive")
	case s.NearCompleteRatio <= 0 || s.NearCompleteRatio > 1:
		return fmt.Errorf("near_complete_ratio must be in (0, 1]")
	case s.MinObservations < 2:
		return fmt.Errorf("min_observations must be at least 2")
	case s.MaxObservationGap < s.PollInterval:
		return fmt.Errorf("max_observation_gap (%s) must be at least the polling interval (%s)", s.MaxObservationGap, s.PollInterval)
	case s.MinProgressBytes < 1:
		return fmt.Errorf("min_progress_bytes must be at least 1")
	case s.ClockTolerance <= 0:
		return fmt.Errorf("clock tolerance must be positive")
	}
	return nil
}

// Revision is a stable short digest of the settings, recorded with every
// assessment so explanations can be tied to the policy that produced them.
func (s Settings) Revision() string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%d|%d|%d|%g|%d|%d|%d|%d|%d|%d|%d",
		s.PollInterval, s.NewDownloadGrace, s.NoProgressWindow, s.NearCompleteRatio,
		s.NearCompleteWindow, s.MinObservations, s.MaxObservationGap, s.MetadataTimeout,
		s.ImportSettle, s.MinProgressBytes, s.ClockTolerance)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Source records where an effective setting came from.
type Source struct {
	Setting string `json:"setting"`
	Value   string `json:"value"`
	From    string `json:"from"` // "built-in proposed default", "config [policy]", "config [qbittorrent.<id>.policy]"
}

// Effective is a resolved policy together with its provenance, automation
// permissions, and matching exclusions.
type Effective struct {
	ID            string               `json:"id"` // scope identity, e.g. "client:qbit"
	Mode          string               `json:"mode"`
	EmergencyStop bool                 `json:"emergency_stop"`
	Settings      Settings             `json:"settings"`
	Revision      string               `json:"revision"`
	Sources       []Source             `json:"sources"`
	Automation    []WorkflowPermission `json:"automation"`
	Exclusions    []ExclusionMatch     `json:"exclusions,omitempty"`
}

// Limits bound acquisition-starting actions (SPEC §§7, 9.1, 10). Pacing
// spreads eligible work across an installation and returns by itself;
// content attempts stop repeated ineffective replacement of one episode or
// movie and return only after review and an explicit reset. The default
// values are implementation proposals, not validated product defaults.
type Limits struct {
	// Acquisitions is the most acquisition-starting actions, across all
	// managers, within AcquisitionWindow.
	Acquisitions      int           `json:"acquisitions"`
	AcquisitionWindow time.Duration `json:"acquisition_window"`
	// ManagerAcquisitions is the same limit for each manager connection.
	ManagerAcquisitions int `json:"manager_acquisitions"`
	// ContentAttempts is how many recovery attempts one episode or movie
	// may have before review is required.
	ContentAttempts int `json:"content_attempts"`
	// SuccessCooldown keeps a recovered transfer and its content from
	// being acted on again for this long.
	SuccessCooldown time.Duration `json:"success_cooldown"`
	// RetainedTransfers is the most transfers recovery may keep stopped
	// with their data (SPEC §7.1); at the ceiling, recovery stops.
	RetainedTransfers int `json:"retained_transfers"`
	// RetainedBytes optionally bounds their known logical size; 0 means
	// no byte ceiling is configured.
	RetainedBytes int64 `json:"retained_bytes"`
	// ActionsPerCycle bounds automatic actions dispatched after one
	// observation cycle; they run one at a time.
	ActionsPerCycle int      `json:"actions_per_cycle"`
	Sources         []Source `json:"sources"`
}

// DefaultLimits returns the proposed limits.
func DefaultLimits() Limits {
	return Limits{Acquisitions: 20, AcquisitionWindow: 24 * time.Hour, ManagerAcquisitions: 20, ContentAttempts: 3, SuccessCooldown: 24 * time.Hour,
		RetainedTransfers: 25, ActionsPerCycle: 1}
}

// From returns where a limit setting came from.
func (l Limits) From(setting string) string {
	for _, s := range l.Sources {
		if s.Setting == setting {
			return s.From
		}
	}
	return "built-in proposed default"
}
