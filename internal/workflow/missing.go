package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/store"
)

// EpisodeSearcher and MovieSearcher are managers that accept a search
// command for specific content (Sonarr EpisodeSearch, Radarr MoviesSearch).
type EpisodeSearcher interface {
	SearchEpisodes(ctx context.Context, ids []int64) (int64, error)
}

// MovieSearcher: see EpisodeSearcher.
type MovieSearcher interface {
	SearchMovies(ctx context.Context, ids []int64) (int64, error)
}

type searchBaseline struct {
	MaxCommandID *int64 `json:"max_command_id"`
}

// searchLive reads what revalidateSearch compares: the manager version,
// its queue rows for the content, and the content's monitoring.
func (r *Recovery) searchLive(ctx context.Context, p action.Proposal) (action.Live, error) {
	pl := p.Planned
	var live action.Live
	m, ok := r.Managers[pl.ManagerID]
	if !ok {
		live.Unread = append(live.Unread, pl.ManagerID)
		return live, nil
	}
	if mp, err := m.Probe(ctx); err == nil {
		live.ManagerVersion = mp.Version
	}
	q, err := m.Queue(ctx)
	if err != nil || !q.Complete {
		live.Unread = append(live.Unread, pl.ManagerID)
	} else {
		for _, it := range q.Items {
			if k, ok := it.ContentKey(); ok {
				for _, c := range pl.Content {
					if k == c {
						if it.ManagerID == "" {
							it.ManagerID = pl.ManagerID
						}
						live.Associations = append(live.Associations, it)
					}
				}
			}
		}
	}
	for _, key := range pl.Content {
		cs, err := m.Content(ctx, key)
		if err != nil {
			live.ContentErr = key + ": " + err.Error()
			break
		}
		live.Content = append(live.Content, action.ContentState{Key: cs.Key, Exists: cs.Exists, Monitored: cs.Monitored, HasFile: cs.HasFile, Detail: cs.Detail})
	}
	return live, nil
}

func (r *Recovery) searchBaseline(ctx context.Context, st store.Step) (json.RawMessage, error) {
	var tg action.SearchTarget
	if err := json.Unmarshal(st.Target, &tg); err != nil {
		return nil, err
	}
	m, ok := r.Managers[tg.ManagerID]
	if !ok {
		return nil, fmt.Errorf("no search support for manager %s", tg.ManagerID)
	}
	cr, ok := m.(CommandReader)
	if !ok {
		return nil, fmt.Errorf("manager %s does not list commands, so a search could not be verified", tg.ManagerID)
	}
	cmds, err := cr.Commands(ctx)
	if err != nil {
		return nil, err
	}
	var max int64
	for _, c := range cmds {
		if (c.Status == "queued" || c.Status == "started") && searchCovers(c, []string{tg.Key}) {
			return nil, fmt.Errorf("the manager already has an active search #%d covering %s", c.ID, tg.Key)
		}
		if c.ID > max {
			max = c.ID
		}
	}
	return json.Marshal(searchBaseline{MaxCommandID: &max})
}

func (r *Recovery) dispatchSearch(ctx context.Context, st store.Step) (json.RawMessage, error) {
	var tg action.SearchTarget
	if err := json.Unmarshal(st.Target, &tg); err != nil {
		return nil, permanent(st.Op, err.Error())
	}
	if len(tg.IDs) != 1 || tg.IDs[0] <= 0 {
		return nil, permanent(st.Op, "a search names exactly one episode or movie")
	}
	m, ok := r.Managers[tg.ManagerID]
	if !ok {
		return nil, permanent(st.Op, "no search support for manager "+tg.ManagerID)
	}
	var id int64
	var err error
	switch tg.Kind {
	case "episode":
		s, ok := m.(EpisodeSearcher)
		if !ok {
			return nil, permanent(st.Op, "manager "+tg.ManagerID+" cannot search episodes")
		}
		id, err = s.SearchEpisodes(ctx, tg.IDs)
	case "movie":
		s, ok := m.(MovieSearcher)
		if !ok {
			return nil, permanent(st.Op, "manager "+tg.ManagerID+" cannot search movies")
		}
		id, err = s.SearchMovies(ctx, tg.IDs)
	default:
		return nil, permanent(st.Op, "unknown content kind "+tg.Kind)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]int64{"command_id": id})
}

// observeSearch establishes that the manager queued the search: a command
// of the expected name naming the content, newer than every command
// listed just before the request. Acceptance only: it says nothing about
// results or a grab.
func (r *Recovery) observeSearch(ctx context.Context, st store.Step) (action.Observation, error) {
	var tg action.SearchTarget
	if err := json.Unmarshal(st.Target, &tg); err != nil {
		return action.Observation{}, err
	}
	m, ok := r.Managers[tg.ManagerID]
	if !ok {
		return action.Observation{}, fmt.Errorf("no search support for manager %s", tg.ManagerID)
	}
	cr, ok := m.(CommandReader)
	if !ok {
		return action.Observation{}, fmt.Errorf("manager %s does not list commands", tg.ManagerID)
	}
	var base searchBaseline
	_ = json.Unmarshal(st.Baseline, &base)
	if base.MaxCommandID == nil {
		return action.Observation{}, fmt.Errorf("no command baseline was recorded before the request")
	}
	name := map[string]string{"episode": "EpisodeSearch", "movie": "MoviesSearch"}[tg.Kind]
	return r.wait(ctx, func() (action.Observation, error) {
		cmds, err := cr.Commands(ctx)
		if err != nil {
			return action.Observation{}, err
		}
		for _, c := range cmds {
			if c.ID <= *base.MaxCommandID || !strings.EqualFold(c.Name, name) || !names(c, tg) {
				continue
			}
			return action.Observation{Verdict: action.Holds, Detail: fmt.Sprintf("%s queued command %d (%s, status %s)", tg.ManagerID, c.ID, c.Name, c.Status)}, nil
		}
		return action.Observation{Verdict: action.NotHolds, Detail: "no new " + name + " for " + tg.Key + " in the command list"}, nil
	})
}

func names(c arr.Command, tg action.SearchTarget) bool {
	ids := c.EpisodeIDs
	if tg.Kind == "movie" {
		ids = c.MovieIDs
	}
	for _, want := range tg.IDs {
		if !contains(ids, want) {
			return false
		}
	}
	return true
}
