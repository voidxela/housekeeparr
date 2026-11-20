package domain

import "time"

// CoverageGroup is a unit of a manager's coverage sweep: a Sonarr series,
// or Radarr's whole movie list (ID 0).
type CoverageGroup struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Monitored bool   `json:"monitored"`
}

// CoverageItem is one episode or movie as a coverage sweep reads it.
// Wanted: the manager monitors it (and, for an episode, its series) and it
// has aired or is available; only wanted items count toward coverage.
type CoverageItem struct {
	Key     string     `json:"key"` // ContentKey form: "episode:<id>" or "movie:<id>"
	GroupID int64      `json:"group_id"`
	Label   string     `json:"label"`
	Wanted  bool       `json:"wanted"`
	HasFile bool       `json:"has_file"`
	AiredAt *time.Time `json:"aired_at,omitempty"`
}
