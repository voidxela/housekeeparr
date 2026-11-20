package fake

import (
	"context"
	"sort"
	"strings"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
)

// AddHistory appends manager history records as a released manager would
// write them (fixture replay of the manager-wide history). Records without
// an ID get the next one.
func (m *Manager) AddHistory(recs ...arr.HistoryRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range recs {
		if r.ID == 0 {
			m.w.nextID++
			r.ID = m.w.nextID
		} else if r.ID > m.w.nextID {
			m.w.nextID = r.ID
		}
		m.feed = append(m.feed, r)
	}
}

// HistoryFeed pages the whole simulated history newest first by ID: the
// records added with AddHistory and those the simulated writes recorded.
func (m *Manager) HistoryFeed(_ context.Context, page, pageSize int) (arr.HistoryPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return arr.HistoryPage{}, m.err
	}
	all := append([]arr.HistoryRecord(nil), m.feed...)
	for _, h := range m.w.history {
		r := arr.HistoryRecord{ID: h.ID, EventType: h.EventType, Date: h.Date, DownloadID: strings.ToUpper(h.downloadID), SourceTitle: h.SourceTitle,
			ReleaseSource: h.ReleaseSource}
		if m.kind == adapter.KindRadarr {
			r.MovieID = h.EpisodeID
		} else {
			r.EpisodeID = h.EpisodeID
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	start := (page - 1) * pageSize
	out := arr.HistoryPage{Total: len(all), Records: []arr.HistoryRecord{}}
	if start < len(all) {
		out.Records = all[start:min(start+pageSize, len(all))]
	}
	return out, nil
}
