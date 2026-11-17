package fake

import (
	"context"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// missingState is a fake manager's simulated missing list and searches.
type missingState struct {
	items     []domain.MissingItem
	err       error
	partial   bool
	searches  [][]int64
	searchErr error
	reads     int
}

// SetMissing sets the simulated missing list.
func (m *Manager) SetMissing(items ...domain.MissingItem) {
	m.mu.Lock()
	m.ms.items = items
	m.mu.Unlock()
}

// FailMissing makes missing-list reads fail; PartialMissing marks them
// incomplete.
func (m *Manager) FailMissing(err error) { m.mu.Lock(); m.ms.err = err; m.mu.Unlock() }
func (m *Manager) PartialMissing(p bool) { m.mu.Lock(); m.ms.partial = p; m.mu.Unlock() }

// MissingReads counts missing-list reads.
func (m *Manager) MissingReads() int { m.mu.Lock(); defer m.mu.Unlock(); return m.ms.reads }

// Missing returns the simulated missing list.
func (m *Manager) Missing(_ context.Context, maxItems int) (domain.MissingSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ms.reads++
	if m.ms.err != nil {
		return domain.MissingSnapshot{}, m.ms.err
	}
	items := append([]domain.MissingItem(nil), m.ms.items...)
	complete := !m.ms.partial
	if len(items) > maxItems {
		items, complete = items[:maxItems], false
	}
	for i := range items {
		items[i].ManagerID = m.id
	}
	return domain.MissingSnapshot{ManagerID: m.id, CollectedAt: m.now(), Items: items, Total: len(m.ms.items), Complete: complete}, nil
}

// Searches returns the content IDs of every search requested.
func (m *Manager) Searches() [][]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]int64(nil), m.ms.searches...)
}

// FailSearches makes search requests fail with err.
func (m *Manager) FailSearches(err error) { m.mu.Lock(); m.ms.searchErr = err; m.mu.Unlock() }

func (m *Manager) search(name string, ids []int64) (int64, error) {
	if len(ids) == 0 || len(ids) > 10 {
		return 0, &adapter.Error{Class: adapter.ErrPermanent, Op: "fake.search", Message: "bad ids"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ms.searchErr != nil {
		return 0, m.ms.searchErr
	}
	m.ms.searches = append(m.ms.searches, append([]int64(nil), ids...))
	m.w.nextID++
	now := m.now()
	c := arr.Command{ID: 1000 + m.w.nextID, Name: name, Status: "completed", Result: "successful", Trigger: "manual", Queued: now, Ended: &now}
	if m.kind == adapter.KindRadarr {
		c.MovieIDs = ids
	} else {
		c.EpisodeIDs = ids
	}
	m.w.commands = append(m.w.commands, c)
	return c.ID, nil
}

// SearchEpisodes simulates an EpisodeSearch command.
func (m *Manager) SearchEpisodes(_ context.Context, ids []int64) (int64, error) {
	return m.search("EpisodeSearch", ids)
}

// SearchMovies simulates a MoviesSearch command.
func (m *Manager) SearchMovies(_ context.Context, ids []int64) (int64, error) {
	return m.search("MoviesSearch", ids)
}

// MissingEpisode builds a monitored, aired, missing Sonarr episode.
func MissingEpisode(id, seriesID int64, title string, aired time.Time) domain.MissingItem {
	return domain.MissingItem{ContentKind: "episode", ContentID: id, SeriesID: seriesID, Title: title, Monitored: true,
		AiredAt: &aired, Available: domain.KnownValue(true)}
}

// MissingMovie builds a monitored, available, missing Radarr movie.
func MissingMovie(id int64, title string) domain.MissingItem {
	return domain.MissingItem{ContentKind: "movie", ContentID: id, Title: title, Monitored: true, Available: domain.KnownValue(true)}
}
