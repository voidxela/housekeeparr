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

// Effective is a resolved policy together with its provenance.
type Effective struct {
	ID       string   `json:"id"` // scope identity, e.g. "client:qbit"
	Settings Settings `json:"settings"`
	Revision string   `json:"revision"`
	Sources  []Source `json:"sources"`
}
