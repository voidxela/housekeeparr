// Package workflow implements action executors: the adapter calls behind
// each journaled step, and the read-only observations that verify them.
// Policy and safety decisions are not made here; the coordinator and
// planner (internal/action) make them.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Manager is a media manager connection with the recovery operations.
type Manager interface {
	ID() string
	Probe(ctx context.Context) (adapter.Probe, error)
	Queue(ctx context.Context) (domain.QueueSnapshot, error)
	Health(ctx context.Context) (domain.HealthSnapshot, error)
	Content(ctx context.Context, key string) (arr.Content, error)
	RemoveQueueItem(ctx context.Context, queueID int64, f arr.RemoveFlags) error
	BlocklistEntries(ctx context.Context, m arr.BlocklistMatch) ([]int64, error)
	History(ctx context.Context, downloadID string) ([]arr.HistoryEvent, error)
}

// Client is a download client connection with the recovery operations.
type Client interface {
	ID() string
	Probe(ctx context.Context) (adapter.Probe, error)
	ListTransfers(ctx context.Context) (domain.TransferSnapshot, error)
	Stop(ctx context.Context, nativeID string) error
}

// Recovery executes the recovery plan built by action.Plan.
type Recovery struct {
	Managers map[string]Manager
	Clients  map[string]Client
	// Settle bounds how long Observe waits for an accepted change to
	// become visible (default 15s), polling every Poll (default 1s).
	Settle, Poll time.Duration
}

// FromConnections returns an executor over the connections that support
// the recovery operations.
func FromConnections(clients []adapter.DownloadClient, managers []adapter.MediaManager) *Recovery {
	r := &Recovery{Managers: map[string]Manager{}, Clients: map[string]Client{}}
	for _, c := range clients {
		if w, ok := c.(Client); ok {
			r.Clients[c.ID()] = w
		}
	}
	for _, m := range managers {
		if w, ok := m.(Manager); ok {
			r.Managers[m.ID()] = w
		}
	}
	return r
}

type subject struct {
	ClientID string `json:"client_id"`
	NativeID string `json:"native_id"`
}

type queueTarget struct {
	ManagerID  string `json:"manager_id"`
	QueueID    int64  `json:"queue_id"`
	DownloadID string `json:"download_id"`
	Title      string `json:"title"`
	SeriesID   int64  `json:"series_id"`
	EpisodeID  int64  `json:"episode_id"`
	MovieID    int64  `json:"movie_id"`
}

// queueBaseline is the manager evidence that already matched before the
// queue step was sent. Only entries and records absent from it are
// attributed to the step.
type queueBaseline struct {
	BlocklistIDs []int64 `json:"blocklist_ids"`
	HistoryIDs   []int64 `json:"history_ids"`
}

func (tg queueTarget) blocklistMatch() arr.BlocklistMatch {
	return arr.BlocklistMatch{SourceTitle: tg.Title, SeriesID: tg.SeriesID, EpisodeID: tg.EpisodeID, MovieID: tg.MovieID}
}

type queueRequest struct {
	RemoveFromClient *bool `json:"removeFromClient"`
	Blocklist        *bool `json:"blocklist"`
	SkipRedownload   *bool `json:"skipRedownload"`
	ChangeCategory   *bool `json:"changeCategory"`
}

func (q queueRequest) flags() (arr.RemoveFlags, error) {
	if q.RemoveFromClient == nil || q.Blocklist == nil || q.SkipRedownload == nil || q.ChangeCategory == nil {
		return arr.RemoveFlags{}, fmt.Errorf("queue removal request must set every flag explicitly")
	}
	if *q.RemoveFromClient {
		return arr.RemoveFlags{}, fmt.Errorf("refusing removeFromClient=true: v1 never deletes a torrent or its data")
	}
	return arr.RemoveFlags{RemoveFromClient: false, Blocklist: *q.Blocklist, SkipRedownload: *q.SkipRedownload, ChangeCategory: *q.ChangeCategory}, nil
}

func permanent(op, msg string) error {
	return &adapter.Error{Class: adapter.ErrPermanent, Op: op, Message: msg, HTTPStatus: 400}
}

func (r *Recovery) transfer(ctx context.Context, clientID, nativeID string) (domain.Transfer, bool, error) {
	c, ok := r.Clients[clientID]
	if !ok {
		return domain.Transfer{}, false, fmt.Errorf("client %q has no recovery support", clientID)
	}
	snap, err := c.ListTransfers(ctx)
	if err != nil {
		return domain.Transfer{}, false, err
	}
	if !snap.Complete {
		return domain.Transfer{}, false, fmt.Errorf("client %s listing is incomplete: %v", clientID, snap.Limitations)
	}
	for _, t := range snap.Transfers {
		if t.NativeID == nativeID {
			return t, true, nil
		}
	}
	return domain.Transfer{}, false, nil
}

func (r *Recovery) queueRow(ctx context.Context, managerID, downloadID string) (*domain.QueueItem, error) {
	m, ok := r.Managers[managerID]
	if !ok {
		return nil, fmt.Errorf("manager %q has no recovery support", managerID)
	}
	q, err := m.Queue(ctx)
	if err != nil {
		return nil, err
	}
	if !q.Complete {
		return nil, fmt.Errorf("manager %s queue listing is incomplete: %v", managerID, q.Limitations)
	}
	var found *domain.QueueItem
	for i := range q.Items {
		if strings.EqualFold(q.Items[i].DownloadID, downloadID) {
			if found != nil {
				return nil, fmt.Errorf("more than one %s queue row for %s", managerID, downloadID)
			}
			found = &q.Items[i]
		}
	}
	return found, nil
}

// Live re-reads everything action.Revalidate compares: the transfer and
// client version; the queue rows for the transfer's download ID in every
// manager that could own it (all managers when the plan names none); and, for the planned manager, its version, health, and the
// content's existence and monitoring. Failure to read the transfer is an
// error; other gaps are recorded so that the checks fail closed with a
// specific explanation.
func (r *Recovery) Live(ctx context.Context, p action.Proposal) (action.Live, error) {
	pl := p.Planned
	var live action.Live
	c, ok := r.Clients[pl.ClientID]
	if !ok {
		return live, fmt.Errorf("client %q has no recovery support", pl.ClientID)
	}
	t, present, err := r.transfer(ctx, pl.ClientID, pl.NativeID)
	if err != nil {
		return live, err
	}
	if present {
		live.Transfer = &t
	}
	probe, err := c.Probe(ctx)
	if err != nil {
		return live, fmt.Errorf("client %s version: %w", pl.ClientID, err)
	}
	live.ClientVersion = probe.Version

	downloadID := pl.DownloadID
	if downloadID == "" {
		downloadID = pl.NativeID
	}
	ids := pl.Owners
	if len(ids) == 0 {
		for id := range r.Managers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	for _, id := range ids {
		m, ok := r.Managers[id]
		if !ok {
			live.Unread = append(live.Unread, id)
			continue
		}
		q, err := m.Queue(ctx)
		if err != nil || !q.Complete {
			live.Unread = append(live.Unread, id)
			continue
		}
		for _, it := range q.Items {
			if strings.EqualFold(it.DownloadID, downloadID) {
				if it.ManagerID == "" {
					it.ManagerID = id
				}
				live.Associations = append(live.Associations, it)
			}
		}
	}
	if pl.ManagerID == "" {
		return live, nil
	}
	m, ok := r.Managers[pl.ManagerID]
	if !ok {
		live.Unread = append(live.Unread, pl.ManagerID)
		return live, nil
	}
	if mp, err := m.Probe(ctx); err == nil {
		live.ManagerVersion = mp.Version
	}
	if h, err := m.Health(ctx); err != nil {
		live.HealthErr = err.Error()
	} else {
		live.Health = h.Items
	}
	for _, key := range pl.Content {
		cs, err := m.Content(ctx, key)
		if err != nil {
			live.ContentErr = key + ": " + err.Error()
			break
		}
		live.Content = append(live.Content, action.ContentState{Key: cs.Key, Exists: cs.Exists, Monitored: cs.Monitored, Detail: cs.Detail})
	}
	return live, nil
}

// Baseline reads, before the queue step, the matching blocklist entries
// and history records that already exist.
func (r *Recovery) Baseline(ctx context.Context, st store.Step) (json.RawMessage, error) {
	if st.Op != "manager.queue.fail" {
		return nil, nil
	}
	var tg queueTarget
	if err := json.Unmarshal(st.Target, &tg); err != nil {
		return nil, err
	}
	m, ok := r.Managers[tg.ManagerID]
	if !ok {
		return nil, fmt.Errorf("no recovery support for manager %s", tg.ManagerID)
	}
	if tg.Title == "" {
		return nil, fmt.Errorf("no release title to match blocklist entries")
	}
	b := queueBaseline{BlocklistIDs: []int64{}, HistoryIDs: []int64{}}
	ids, err := m.BlocklistEntries(ctx, tg.blocklistMatch())
	if err != nil {
		return nil, err
	}
	b.BlocklistIDs = append(b.BlocklistIDs, ids...)
	hist, err := m.History(ctx, tg.DownloadID)
	if err != nil {
		return nil, err
	}
	for _, h := range hist {
		b.HistoryIDs = append(b.HistoryIDs, h.ID)
	}
	return json.Marshal(b)
}

// Dispatch sends one step.
func (r *Recovery) Dispatch(ctx context.Context, st store.Step) (json.RawMessage, error) {
	switch st.Op {
	case "manager.queue.fail":
		var tg queueTarget
		var req queueRequest
		if err := json.Unmarshal(st.Target, &tg); err != nil {
			return nil, permanent(st.Op, err.Error())
		}
		if err := json.Unmarshal(st.Request, &req); err != nil {
			return nil, permanent(st.Op, err.Error())
		}
		flags, err := req.flags()
		if err != nil {
			return nil, permanent(st.Op, err.Error())
		}
		m, ok := r.Managers[tg.ManagerID]
		if !ok {
			return nil, permanent(st.Op, "no recovery support for manager "+tg.ManagerID)
		}
		return nil, m.RemoveQueueItem(ctx, tg.QueueID, flags)
	case "client.stop":
		var sub subject
		if err := json.Unmarshal(st.Target, &sub); err != nil {
			return nil, permanent(st.Op, err.Error())
		}
		var req struct {
			DeleteData *bool `json:"delete_data"`
		}
		if err := json.Unmarshal(st.Request, &req); err != nil || req.DeleteData == nil || *req.DeleteData {
			return nil, permanent(st.Op, "client stop must explicitly retain data")
		}
		c, ok := r.Clients[sub.ClientID]
		if !ok {
			return nil, permanent(st.Op, "no recovery support for client "+sub.ClientID)
		}
		return nil, c.Stop(ctx, sub.NativeID)
	}
	return nil, permanent(st.Op, "unknown operation")
}

func (r *Recovery) wait(ctx context.Context, check func() (action.Observation, error)) (action.Observation, error) {
	settle, poll := r.Settle, r.Poll
	if settle <= 0 {
		settle = 15 * time.Second
	}
	if poll <= 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(settle)
	for {
		obs, err := check()
		if (err == nil && obs.Verdict == action.Holds) || time.Now().After(deadline) {
			return obs, err
		}
		select {
		case <-ctx.Done():
			return obs, err
		case <-time.After(poll):
		}
	}
}

// Observe verifies a step's postcondition with fresh reads.
func (r *Recovery) Observe(ctx context.Context, st store.Step) (action.Observation, error) {
	switch st.Op {
	case "manager.queue.fail":
		var tg queueTarget
		var req queueRequest
		if err := json.Unmarshal(st.Target, &tg); err != nil {
			return action.Observation{}, err
		}
		_ = json.Unmarshal(st.Request, &req)
		m, ok := r.Managers[tg.ManagerID]
		if !ok {
			return action.Observation{}, fmt.Errorf("no recovery support for manager %s", tg.ManagerID)
		}
		blocklist := req.Blocklist != nil && *req.Blocklist
		event := "downloadIgnored"
		if blocklist {
			event = "downloadFailed"
		}
		var base *queueBaseline
		if len(st.Baseline) > 0 {
			base = &queueBaseline{}
			if err := json.Unmarshal(st.Baseline, base); err != nil {
				return action.Observation{}, err
			}
		}
		return r.wait(ctx, func() (action.Observation, error) {
			row, err := r.queueRow(ctx, tg.ManagerID, tg.DownloadID)
			if err != nil {
				return action.Observation{}, err
			}
			if base == nil {
				// Without a baseline an existing entry or record cannot be
				// told apart from this step's effect.
				ev, _ := json.Marshal(map[string]any{"queue_row_present": row != nil})
				return action.Observation{Verdict: action.Unknown, Evidence: ev,
					Detail: "no pre-dispatch baseline, so blocklist and history evidence cannot be attributed to this request"}, nil
			}
			var newBlock []int64
			if blocklist {
				ids, err := m.BlocklistEntries(ctx, tg.blocklistMatch())
				if err != nil {
					return action.Observation{}, err
				}
				newBlock = newIDs(ids, base.BlocklistIDs)
			}
			hist, err := m.History(ctx, tg.DownloadID)
			if err != nil {
				return action.Observation{}, err
			}
			var newEvents []string
			recorded := false
			for _, h := range hist {
				if !contains(base.HistoryIDs, h.ID) {
					newEvents = append(newEvents, h.EventType)
					recorded = recorded || h.EventType == event
				}
			}
			bl := len(newBlock) > 0
			ev, _ := json.Marshal(map[string]any{"queue_row_present": row != nil, "new_blocklist_ids": newBlock, "new_history": newEvents,
				"baseline": base, "matching": "byte-exact release title and content; title evidence, not infohash identity"})
			switch {
			case row == nil && recorded && (bl || !blocklist):
				return action.Observation{Verdict: action.Holds, Evidence: ev}, nil
			case row != nil && !recorded && !bl:
				return action.Observation{Verdict: action.NotHolds, Evidence: ev, Detail: "the queue row is still tracked and nothing new was recorded"}, nil
			default:
				return action.Observation{Verdict: action.Unknown, Evidence: ev,
					Detail: fmt.Sprintf("partial effect: row present=%v, new %s recorded=%v, new blocklist entries=%d", row != nil, event, recorded, len(newBlock))}, nil
			}
		})
	case "client.stop":
		var sub subject
		if err := json.Unmarshal(st.Target, &sub); err != nil {
			return action.Observation{}, err
		}
		return r.wait(ctx, func() (action.Observation, error) {
			t, present, err := r.transfer(ctx, sub.ClientID, sub.NativeID)
			if err != nil {
				return action.Observation{}, err
			}
			ev, _ := json.Marshal(map[string]any{"registered": present, "state_class": t.StateClass, "native_state": t.NativeState})
			switch {
			case !present:
				// Registration must be preserved; losing it is not success.
				return action.Observation{Verdict: action.NotHolds, Evidence: ev, Detail: "the torrent is no longer registered in the client"}, nil
			case t.StateClass == domain.StatePaused:
				return action.Observation{Verdict: action.Holds, Evidence: ev}, nil
			default:
				return action.Observation{Verdict: action.Unknown, Evidence: ev, Detail: "the torrent is " + string(t.StateClass) + ", not stopped"}, nil
			}
		})
	}
	return action.Observation{}, fmt.Errorf("unknown operation %s", st.Op)
}

func contains(xs []int64, x int64) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func newIDs(now, before []int64) []int64 {
	var out []int64
	for _, id := range now {
		if !contains(before, id) {
			out = append(out, id)
		}
	}
	return out
}
