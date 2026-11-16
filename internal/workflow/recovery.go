// Package workflow implements action executors: the adapter calls behind
// each journaled step, and the read-only observations that verify them.
// Policy and safety decisions are not made here; the coordinator and
// planner (internal/action) make them.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
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
	Queue(ctx context.Context) (domain.QueueSnapshot, error)
	RemoveQueueItem(ctx context.Context, queueID int64, f arr.RemoveFlags) error
	Blocklisted(ctx context.Context, m arr.BlocklistMatch) (bool, error)
	HistoryEvents(ctx context.Context, downloadID string) ([]string, error)
}

// Client is a download client connection with the recovery operations.
type Client interface {
	ID() string
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

// clockSlack tolerates a manager clock behind ours when matching blocklist
// entries recorded after dispatch.
const clockSlack = 5 * time.Minute

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

// Refetch re-reads the transfer and its queue row and recomputes the
// planner's fingerprint.
func (r *Recovery) Refetch(ctx context.Context, p action.Proposal) (string, error) {
	var sub subject
	if err := json.Unmarshal(p.Subject, &sub); err != nil {
		return "", err
	}
	t, present, err := r.transfer(ctx, sub.ClientID, sub.NativeID)
	if err != nil {
		return "", err
	}
	f := action.Facts{ClientID: sub.ClientID, NativeID: sub.NativeID, Present: present, StateClass: t.StateClass, Verified: t.VerifiedBytes}
	for _, op := range p.Operations {
		if op.Op != "manager.queue.fail" {
			continue
		}
		var tg queueTarget
		if err := json.Unmarshal(op.Target, &tg); err != nil {
			return "", err
		}
		row, err := r.queueRow(ctx, tg.ManagerID, tg.DownloadID)
		if err != nil {
			return "", err
		}
		f.ManagerID, f.DownloadID = tg.ManagerID, tg.DownloadID
		if row != nil {
			f.QueueID = row.QueueID
		}
	}
	return f.Fingerprint(), nil
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
		return r.wait(ctx, func() (action.Observation, error) {
			row, err := r.queueRow(ctx, tg.ManagerID, tg.DownloadID)
			if err != nil {
				return action.Observation{}, err
			}
			bl := false
			if blocklist {
				if st.DispatchedAt == nil || tg.Title == "" {
					return action.Observation{Verdict: action.Unknown, Detail: "no dispatch time or release title to match a blocklist entry"}, nil
				}
				var err error
				bl, err = m.Blocklisted(ctx, arr.BlocklistMatch{SourceTitle: tg.Title, Since: st.DispatchedAt.Add(-clockSlack),
					SeriesID: tg.SeriesID, EpisodeID: tg.EpisodeID, MovieID: tg.MovieID})
				if err != nil {
					return action.Observation{}, err
				}
			}
			hist, err := m.HistoryEvents(ctx, tg.DownloadID)
			if err != nil {
				return action.Observation{}, err
			}
			recorded := false
			for _, h := range hist {
				recorded = recorded || h == event
			}
			ev, _ := json.Marshal(map[string]any{"queue_row_present": row != nil, "blocklisted": bl, "history": hist})
			switch {
			case row == nil && recorded && (bl || !blocklist):
				return action.Observation{Verdict: action.Holds, Evidence: ev}, nil
			case row != nil && !recorded && !bl:
				return action.Observation{Verdict: action.NotHolds, Evidence: ev, Detail: "the queue row is still tracked and nothing was recorded"}, nil
			default:
				return action.Observation{Verdict: action.Unknown, Evidence: ev,
					Detail: fmt.Sprintf("partial effect: row present=%v, %s recorded=%v, blocklisted=%v", row != nil, event, recorded, bl)}, nil
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
