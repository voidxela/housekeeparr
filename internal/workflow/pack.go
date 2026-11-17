package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// packLive reads the pack's queue rows, its import history, and its
// episodes' files, and classifies every expected episode.
func (r *Recovery) packLive(ctx context.Context, p action.Proposal) (action.Live, error) {
	pl := p.Planned
	var live action.Live
	m, ok := r.Managers[pl.ManagerID]
	if !ok {
		live.Unread = append(live.Unread, pl.ManagerID)
		return live, nil
	}
	if c, ok := r.Clients[pl.ClientID]; ok {
		if probe, err := c.Probe(ctx); err == nil {
			live.ClientVersion = probe.Version
		}
		t, present, err := r.transfer(ctx, pl.ClientID, pl.NativeID)
		if err != nil {
			return live, err
		}
		if present {
			live.Transfer = &t
		}
	}
	if health, err := m.Health(ctx); err == nil {
		live.Health = health.Items
	} else {
		live.HealthErr = err.Error()
	}
	if mp, err := m.Probe(ctx); err == nil {
		live.ManagerVersion = mp.Version
	}
	q, err := m.Queue(ctx)
	if err != nil || !q.Complete {
		live.Unread = append(live.Unread, pl.ManagerID)
	} else {
		for _, it := range q.Items {
			if strings.EqualFold(it.DownloadID, pl.DownloadID) {
				live.Associations = append(live.Associations, it)
			}
		}
	}
	hist, err := m.History(ctx, pl.DownloadID)
	if err != nil {
		live.PackErr = "history: " + err.Error()
		return live, nil
	}
	imported := map[int64]bool{}
	for _, h := range hist {
		if h.EventType == "downloadFolderImported" && h.EpisodeID > 0 {
			imported[h.EpisodeID] = true
		}
	}
	ev := &action.PackEvidence{}
	for _, key := range pl.Content {
		_, id, _ := domain.ParseContentKey(key)
		c, err := m.Content(ctx, key)
		if err != nil {
			live.PackErr = key + ": " + err.Error()
			return live, nil
		}
		switch {
		case c.HasFile == nil:
			ev.Unexplained = append(ev.Unexplained, key+": file state not reported")
		case imported[id] && *c.HasFile:
			ev.Imported = append(ev.Imported, id)
		case !imported[id] && !*c.HasFile:
			ev.Missing = append(ev.Missing, id)
		case imported[id]:
			ev.Unexplained = append(ev.Unexplained, key+" was imported from this download but has no file now")
		default:
			ev.Unexplained = append(ev.Unexplained, key+" has a file that was not imported from this download")
		}
	}
	// Any import message besides the partial-pack message and "already
	// imported" leaves the cause uncertain (a sample, no audio, mapping).
	seen := map[string]bool{}
	for _, it := range live.Associations {
		for _, msg := range it.StatusMessages {
			if msg.Title == domain.PartialPackMessage {
				continue
			}
			for _, s := range msg.Messages {
				if !strings.HasPrefix(s, domain.AlreadyImportedPrefix) && !seen[msg.Title+s] {
					seen[msg.Title+s] = true
					ev.Unexplained = append(ev.Unexplained, msg.Title+": "+s)
				}
			}
			if len(msg.Messages) == 0 && !seen[msg.Title] {
				seen[msg.Title] = true
				ev.Unexplained = append(ev.Unexplained, msg.Title)
			}
		}
	}
	live.Pack = ev
	return live, nil
}

func (r *Recovery) packBaseline(ctx context.Context, st store.Step) (json.RawMessage, error) {
	var tg action.PackTarget
	if err := json.Unmarshal(st.Target, &tg); err != nil {
		return nil, err
	}
	m, ok := r.Managers[tg.ManagerID]
	if !ok {
		return nil, fmt.Errorf("no support for manager %s", tg.ManagerID)
	}
	hist, err := m.History(ctx, tg.DownloadID)
	if err != nil {
		return nil, err
	}
	b := action.PackBaseline{HistoryIDs: []int64{}}
	imported := map[int64]bool{}
	for _, h := range hist {
		b.HistoryIDs = append(b.HistoryIDs, h.ID)
		if h.EventType == "downloadFolderImported" {
			imported[h.EpisodeID] = true
		}
	}
	// Live revalidation has already established that these episodes have
	// no file and no import record. Store the subset so a restart after
	// dispatch cannot lose its targeted follow-ups.
	for _, episode := range tg.EpisodeIDs {
		if !imported[episode] {
			b.MissingEpisodes = append(b.MissingEpisodes, episode)
		}
	}
	if len(b.MissingEpisodes) == 0 || len(b.MissingEpisodes) == len(tg.EpisodeIDs) {
		return nil, fmt.Errorf("partial import could no longer be established from the pack history")
	}
	return json.Marshal(b)
}

func (r *Recovery) dispatchPack(ctx context.Context, st store.Step) (json.RawMessage, error) {
	var tg action.PackTarget
	if err := json.Unmarshal(st.Target, &tg); err != nil {
		return nil, permanent(st.Op, err.Error())
	}
	var req queueRequest
	if err := json.Unmarshal(st.Request, &req); err != nil {
		return nil, permanent(st.Op, err.Error())
	}
	flags, err := req.flags()
	if err != nil {
		return nil, permanent(st.Op, err.Error())
	}
	// Only the ignore path: nothing removed from the client, nothing
	// blocklisted, no redownload, no category change.
	if flags != (arr.RemoveFlags{SkipRedownload: true}) {
		return nil, permanent(st.Op, "a partial-pack clearance must use the ignore path with every flag explicit")
	}
	m, ok := r.Managers[tg.ManagerID]
	if !ok {
		return nil, permanent(st.Op, "no support for manager "+tg.ManagerID)
	}
	return nil, m.RemoveQueueItem(ctx, tg.QueueID, flags)
}

// observePack establishes that no row of the download is tracked and that
// the manager recorded the ignore (new downloadIgnored history).
func (r *Recovery) observePack(ctx context.Context, st store.Step) (action.Observation, error) {
	var tg action.PackTarget
	if err := json.Unmarshal(st.Target, &tg); err != nil {
		return action.Observation{}, err
	}
	m, ok := r.Managers[tg.ManagerID]
	if !ok {
		return action.Observation{}, fmt.Errorf("no support for manager %s", tg.ManagerID)
	}
	var base action.PackBaseline
	_ = json.Unmarshal(st.Baseline, &base)
	return r.wait(ctx, func() (action.Observation, error) {
		q, err := m.Queue(ctx)
		if err != nil {
			return action.Observation{}, err
		}
		if !q.Complete {
			return action.Observation{Verdict: action.NotHolds, Detail: "the queue listing was incomplete"}, nil
		}
		for _, it := range q.Items {
			if strings.EqualFold(it.DownloadID, tg.DownloadID) {
				return action.Observation{Verdict: action.NotHolds, Detail: "the download is still in the queue"}, nil
			}
		}
		hist, err := m.History(ctx, tg.DownloadID)
		if err != nil {
			return action.Observation{}, err
		}
		n := 0
		for _, h := range hist {
			if h.EventType == "downloadIgnored" && !contains(base.HistoryIDs, h.ID) {
				n++
			}
		}
		if n == 0 {
			return action.Observation{Verdict: action.NotHolds, Detail: "no new downloadIgnored history record"}, nil
		}
		return action.Observation{Verdict: action.Holds, Detail: fmt.Sprintf("no queue row remains; %d new downloadIgnored record(s)", n)}, nil
	})
}
