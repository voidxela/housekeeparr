package fake

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// The write methods below simulate, deterministically, the effects that
// the released applications were observed to have (docs/compatibility.md):
// failing a managed grab removes its queue row, records a history event,
// and (with blocklist) adds a blocklist entry; stopping keeps the torrent
// registered with its data. They exist for end-to-end tests of the action
// pipeline; they are not evidence about any real application.

type blEntry struct {
	id                  int64
	title               string
	seriesID, episodeID int64
	movieID             int64
}

type histEntry struct {
	arr.HistoryEvent
	downloadID string
}

// managerWrites holds a fake manager's simulated write state.
type managerWrites struct {
	nextID      int64
	blocklist   []blEntry
	history     []histEntry
	removals    []arr.RemoveFlags
	removeErr   error
	removeNoop  bool
	unmonitored map[string]bool
	// commands is the simulated command list; a blocklisting removal with
	// redownload not skipped queues the manager-native search, as the
	// released managers do (trigger "unspecified").
	commands    []arr.Command
	commandsErr error
	searchState string // status of queued replacement searches (default "completed")
	redownload  *arr.RedownloadSettings
	clock       func() time.Time
}

func (m *Manager) now() time.Time {
	if m.w.clock != nil {
		return m.w.clock()
	}
	return time.Now()
}

// SetClock sets the clock used for simulated command times.
func (m *Manager) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w.clock = now
}

// SetReplacementSearchStatus sets the status given to replacement searches
// the fake queues (for example "queued" or "failed"); "" means completed.
func (m *Manager) SetReplacementSearchStatus(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w.searchState = status
}

// SetCommandsError makes command-list reads fail.
func (m *Manager) SetCommandsError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w.commandsErr = err
}

// ClearCommands empties the simulated command list, as the released
// managers do a few minutes after commands finish.
func (m *Manager) ClearCommands() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w.commands = nil
}

// AddCommand adds a command to the simulated list, replacing one with the
// same ID.
func (m *Manager) AddCommand(c arr.Command) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.w.commands {
		if m.w.commands[i].ID == c.ID {
			m.w.commands[i] = c
			return
		}
	}
	m.w.commands = append(m.w.commands, c)
}

// SetRedownload sets the simulated redownload settings (default: both on).
func (m *Manager) SetRedownload(rs arr.RedownloadSettings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w.redownload = &rs
}

// Redownload returns the simulated redownload settings.
func (m *Manager) Redownload(context.Context) (arr.RedownloadSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w.redownload != nil {
		return *m.w.redownload, nil
	}
	on := true
	return arr.RedownloadSettings{Failed: true, FromInteractive: &on}, nil
}

// Commands returns the simulated command list.
func (m *Manager) Commands(context.Context) ([]arr.Command, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w.commandsErr != nil {
		return nil, m.w.commandsErr
	}
	return append([]arr.Command{}, m.w.commands...), nil
}

// RemoveQueueItem removes a queue row as the released managers do for a
// managed grab, never touching the client (removeFromClient must be false).
func (m *Manager) RemoveQueueItem(_ context.Context, queueID int64, f arr.RemoveFlags) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w.removals = append(m.w.removals, f)
	if m.w.removeErr != nil {
		return m.w.removeErr
	}
	if f.RemoveFromClient {
		return fmt.Errorf("fake manager: removeFromClient=true would delete data")
	}
	if m.w.removeNoop {
		return nil
	}
	for i, q := range m.items {
		if q.QueueID != queueID {
			continue
		}
		m.items = append(m.items[:i:i], m.items[i+1:]...)
		event := "downloadIgnored"
		if f.Blocklist {
			event = "downloadFailed"
			m.w.nextID++
			m.w.blocklist = append(m.w.blocklist, blEntry{id: m.w.nextID, title: q.Title, seriesID: q.SeriesID, episodeID: q.EpisodeID, movieID: q.MovieID})
		}
		m.w.nextID++
		m.w.history = append(m.w.history, histEntry{HistoryEvent: arr.HistoryEvent{ID: m.w.nextID, EventType: event, Date: time.Now()}, downloadID: q.DownloadID})
		if f.Blocklist && !f.SkipRedownload {
			m.w.nextID++
			at := m.now()
			c := arr.Command{ID: m.w.nextID, Status: "completed", Result: "successful", Trigger: "unspecified", Queued: at, Started: &at, Ended: &at}
			if m.w.searchState != "" {
				c.Status = m.w.searchState
			}
			if q.MovieID > 0 {
				c.Name, c.MovieIDs, c.Message = "MoviesSearch", []int64{q.MovieID}, "Completed search for 1 movies. 0 reports downloaded."
			} else {
				c.Name, c.EpisodeIDs, c.Message = "EpisodeSearch", []int64{q.EpisodeID}, "Episode search completed. 0 reports downloaded."
			}
			m.w.commands = append(m.w.commands, c)
		}
		return nil
	}
	return &adapter.Error{Class: adapter.ErrPermanent, Op: "fake.queue_remove", HTTPStatus: 404, Message: "no such queue item"}
}

// BlocklistEntries returns matching simulated blocklist entry IDs.
func (m *Manager) BlocklistEntries(_ context.Context, b arr.BlocklistMatch) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []int64
	for _, e := range m.w.blocklist {
		if e.title == b.SourceTitle && (b.EpisodeID == 0 || e.episodeID == b.EpisodeID) && (b.MovieID == 0 || e.movieID == b.MovieID) {
			out = append(out, e.id)
		}
	}
	return out, nil
}

// History returns simulated history records for a download, newest first.
func (m *Manager) History(_ context.Context, downloadID string) ([]arr.HistoryEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []arr.HistoryEvent
	for i := len(m.w.history) - 1; i >= 0; i-- {
		if strings.EqualFold(m.w.history[i].downloadID, downloadID) {
			out = append(out, m.w.history[i].HistoryEvent)
		}
	}
	return out, nil
}

// Content reports content as existing and monitored unless unmonitored.
func (m *Manager) Content(_ context.Context, key string) (arr.Content, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w.unmonitored[key] {
		return arr.Content{Key: key, Exists: true, Detail: "unmonitored"}, nil
	}
	return arr.Content{Key: key, Exists: true, Monitored: true, Detail: "monitored"}, nil
}

// Removals returns every queue removal requested, with its flags.
func (m *Manager) Removals() []arr.RemoveFlags {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]arr.RemoveFlags(nil), m.w.removals...)
}

// FailRemovals makes queue removals return err; with noop, they are
// accepted without any effect.
func (m *Manager) FailRemovals(err error, noop bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.w.removeErr, m.w.removeNoop = err, noop
}

// Unmonitor marks content unmonitored.
func (m *Manager) Unmonitor(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w.unmonitored == nil {
		m.w.unmonitored = map[string]bool{}
	}
	m.w.unmonitored[key] = true
}

// Stop stops a transfer, keeping it registered with its data.
func (c *Client) Stop(_ context.Context, nativeID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stops = append(c.stops, nativeID)
	for i := range c.transfers {
		if c.transfers[i].NativeID == nativeID {
			c.transfers[i].StateClass, c.transfers[i].NativeState = domain.StatePaused, "stoppedDL"
		}
	}
	return nil
}

// Stops returns every stop requested.
func (c *Client) Stops() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.stops...)
}
