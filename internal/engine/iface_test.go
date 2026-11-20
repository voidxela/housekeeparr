package engine

import (
	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/adapter/sonarr"
	"github.com/voidxela/housekeeparr/internal/fake"
)

// The released-manager adapters and the fakes are history-feed readers.
var (
	_ HistoryFeedReader = (*sonarr.Client)(nil)
	_ HistoryFeedReader = (*radarr.Client)(nil)
	_ HistoryFeedReader = (*fake.Manager)(nil)
)
