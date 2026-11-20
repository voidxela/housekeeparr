package action

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

func TestParseMultiEpisode(t *testing.T) {
	for title, want := range map[string][]int64{
		"Show.S03E01E02.1080p.WEB-DL.x264-GRP": {1, 2},
		"Show S03E01-E03 1080p":                {1, 2, 3},
		"Show.S03E01-03.720p":                  {1, 2, 3},
		"show.s03e05e06e07.hdtv":               {5, 6, 7},
		"Show.S03E01.E02.1080p":                {1, 2},
		"Show.S03E01.1080p.WEB-DL":             nil, // one episode
		"Show.S03.1080p.WEB-DL":                nil, // season pack
		"Show.S03E02E01.1080p":                 {1, 2},
		"Show.S03E01E01.1080p":                 nil, // repeated
		"Show.S03E05-02.1080p":                 nil, // backwards range
		"Show.S03E01E02.and.S04E01E02":         nil, // two seasons named
		"Show.S03E01.Episode.Two.1080p":        nil,
	} {
		_, got, ok := parseMultiEpisode(title)
		if ok != (want != nil) || !slices.Equal(got, want) {
			t.Errorf("%q: %v %v, want %v", title, got, ok, want)
		}
	}
}

// multiInput is planInput with a stalled two-episode release, S03E01E02,
// whose manager shows one queue row per episode for the same download
// (observed on Sonarr 4.0.20).
func multiInput(multiTested bool) PlanInput {
	in := planInput()
	row := func(qid, ep, num int64) store.QueueRecord {
		return store.QueueRecord{ManagerID: "sonarr", ItemKey: "aa|episode:" + itoa(ep), QueueID: qid, DownloadID: "aa", Present: true, MatchStatus: store.MatchMatched,
			Latest: domain.QueueItem{ManagerID: "sonarr", QueueID: qid, Title: "Show.S03E01E02.1080p.WEB-DL.x264-GRP", DownloadID: "aa", ContentKind: "episode",
				SeriesID: 3, EpisodeID: ep, SeasonNumber: domain.KnownValue[int64](3), EpisodeNumber: domain.KnownValue(num)}}
	}
	// Listed in the manager's order, not episode order.
	in.Queue = []store.QueueRecord{row(902, 17, 2), row(901, 16, 1)}
	in.Tested = []TestedWorkflow{{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014",
		Client: adapter.KindQBittorrent, ClientVersion: "5.3.0", MultiEpisode: multiTested}}
	return in
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestPlanMultiEpisodeRelease(t *testing.T) {
	// A combination tested only with single-episode releases is not enough.
	p, np := Plan(multiInput(false))
	if np != nil {
		t.Fatal(np.Reason)
	}
	if b := p.Blockers(); len(b) != 1 || b[0].Name != "supported versions" || !strings.HasPrefix(b[0].Detail, "multi-episode recovery: ") {
		t.Fatalf("blockers %+v", b)
	}

	p, _ = Plan(multiInput(true))
	if !p.Eligible() {
		t.Fatalf("multi-episode release blocked by %v", blockerNames(p))
	}
	// One manager request for the whole download, through the first
	// episode's row, every flag explicit.
	if len(p.Operations) != 2 || p.Operations[0].Op != "manager.queue.fail" || p.Operations[1].Op != "client.stop" {
		t.Fatalf("operations %+v", p.Operations)
	}
	var tg struct {
		QueueID    int64   `json:"queue_id"`
		EpisodeID  int64   `json:"episode_id"`
		EpisodeIDs []int64 `json:"episode_ids"`
	}
	_ = json.Unmarshal(p.Operations[0].Target, &tg)
	if tg.QueueID != 901 || tg.EpisodeID != 16 || !slices.Equal(tg.EpisodeIDs, []int64{16, 17}) {
		t.Fatalf("target %+v", tg)
	}
	var req map[string]bool
	_ = json.Unmarshal(p.Operations[0].Request, &req)
	if len(req) != 4 || req["removeFromClient"] || !req["blocklist"] || req["skipRedownload"] || req["changeCategory"] {
		t.Fatalf("flags %v", req)
	}
	// A lock and an attempt budget per episode, one acquisition.
	locks := slices.Sorted(slices.Values(p.Locks))
	if !slices.Equal(locks, []string{ContentLock("sonarr", "episode:16"), ContentLock("sonarr", "episode:17"), TransferLock("qbit", "aa")}) {
		t.Fatalf("locks %v", p.Locks)
	}
	var attempts, pacing []string
	for _, b := range p.Budgets {
		if b.Kind == store.BudgetAttempts {
			attempts = append(attempts, b.Scope)
		} else if b.Units != 1 {
			t.Errorf("pacing claim %+v takes more than one unit", b)
		} else {
			pacing = append(pacing, b.Scope)
		}
	}
	slices.Sort(attempts)
	if !slices.Equal(attempts, []string{AttemptScope("sonarr", "episode:16"), AttemptScope("sonarr", "episode:17")}) || len(pacing) != 2 {
		t.Fatalf("attempts %v pacing %v", attempts, pacing)
	}
	if !slices.Equal(p.Planned.Content, []string{"episode:16", "episode:17"}) || p.Planned.QueueID != 901 {
		t.Fatalf("planned %+v", p.Planned)
	}
	if !strings.Contains(strings.Join(p.Effects, "\n"), "S03E01–E02") {
		t.Errorf("effects do not name the episodes: %v", p.Effects)
	}

	// A private torrent is failed in the manager and left running.
	in := multiInput(true)
	in.Transfer.Latest.Private = domain.KnownValue(true)
	in.Policy.PrivateRecovery, in.Policy.PrivateRecoveryFrom = policy.PrivateKeepRunning, "test"
	if p, _ := Plan(in); !p.Eligible() || len(p.Operations) != 1 || p.Operations[0].Op != "manager.queue.fail" {
		t.Fatalf("private multi-episode: %v %+v", blockerNames(p), p.Operations)
	}
}

func TestPlanMultiItemRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*PlanInput)
		want   string
	}{
		"season pack": {func(in *PlanInput) {
			for i := range in.Queue {
				in.Queue[i].Latest.Title = "Show.S03.1080p.WEB-DL.x264-GRP"
			}
		}, "not a recognised multi-episode release"},
		"title names other episodes": {func(in *PlanInput) {
			in.Queue[0].Latest.EpisodeNumber = domain.KnownValue[int64](3)
		}, "names S03 episodes [1 2]"},
		"one row not exactly matched": {func(in *PlanInput) { in.Queue[1].MatchStatus = store.MatchHashOnly }, "not exactly correlated"},
		"episode number unknown":      {func(in *PlanInput) { in.Queue[0].Latest.EpisodeNumber = domain.UnknownValue[int64]("x") }, "does not report"},
		"different download":          {func(in *PlanInput) { in.Queue[1].DownloadID = "bb" }, "ownership is ambiguous"},
		"radarr":                      {func(in *PlanInput) { in.ManagerKind = adapter.KindRadarr }, "only Sonarr"},
		"preserved-data failure": {func(in *PlanInput) {
			in.Finding.Type = domain.FindingSourceRejected
		}, "has not been tested"},
	} {
		t.Run(name, func(t *testing.T) {
			in := multiInput(true)
			tc.mutate(&in)
			p, np := Plan(in)
			if np != nil {
				t.Fatal(np.Reason)
			}
			c := find(p.Prerequisites, "one manager association")
			if c == nil || c.Satisfied || !strings.Contains(c.Detail, tc.want) {
				t.Fatalf("association check %+v, want a refusal containing %q", c, tc.want)
			}
			if len(p.Operations) > 0 && p.Operations[0].Op == "manager.queue.fail" {
				t.Fatalf("refused plan still fails the grab: %+v", p.Operations)
			}
		})
	}
}

// Before the manager step, the live rows must be the same set, and every
// episode must still exist and be monitored: the manager's replacement
// search covers every episode of the release, including unmonitored ones.
func TestRevalidateMultiEpisode(t *testing.T) {
	p, _ := Plan(multiInput(true))
	live := matching(*p, false)
	live.Associations = []domain.QueueItem{
		{ManagerID: "sonarr", QueueID: 902, DownloadID: "AA", ContentKind: "episode", EpisodeID: 17},
		{ManagerID: "sonarr", QueueID: 901, DownloadID: "aa", ContentKind: "episode", EpisodeID: 16},
	}
	live.Content = []ContentState{{Key: "episode:16", Exists: true, Monitored: true, Detail: "monitored"},
		{Key: "episode:17", Exists: true, Monitored: true, Detail: "monitored"}}
	for _, c := range Revalidate(*p, 0, live) {
		if !c.Satisfied {
			t.Fatalf("unchanged multi-episode release refused: %+v", c)
		}
	}
	one := live
	one.Associations = live.Associations[:1]
	if c := find(Revalidate(*p, 0, one), "ownership unchanged"); c == nil || c.Satisfied {
		t.Fatalf("a missing episode row accepted: %+v", c)
	}
	other := live
	other.Associations = []domain.QueueItem{live.Associations[0], {ManagerID: "sonarr", QueueID: 905, DownloadID: "aa", ContentKind: "episode", EpisodeID: 16}}
	if c := find(Revalidate(*p, 0, other), "ownership unchanged"); c == nil || c.Satisfied {
		t.Fatalf("a different queue item for the request accepted: %+v", c)
	}
	unmon := live
	unmon.Content = []ContentState{live.Content[0], {Key: "episode:17", Exists: true, Monitored: false, Detail: "the episode is unmonitored"}}
	c := find(Revalidate(*p, 0, unmon), "content monitored")
	if c == nil || c.Satisfied || !strings.Contains(c.Detail, "including unmonitored ones") {
		t.Fatalf("an unmonitored episode accepted or unexplained: %+v", c)
	}
}

// A verified multi-episode recovery follows every episode's replacement,
// not only the episode whose queue item carried the request.
func TestMultiEpisodeRecoveryTracksEveryEpisode(t *testing.T) {
	c, f, _ := setup(t)
	p := proposal("qbit|aa")
	p.Operations[0].Target = json.RawMessage(`{"manager_id":"sonarr","queue_id":7,"download_id":"aa","episode_id":1,"episode_ids":[1,2]}`)
	p.Planned.Content = []string{"episode:1", "episode:2"}
	f.live = func(p Proposal) Live {
		failed := false
		for _, s := range f.sent {
			failed = failed || s == "manager.queue.fail"
		}
		live := matching(p, failed)
		if !failed {
			live.Associations = append(live.Associations, domain.QueueItem{ManagerID: "sonarr", QueueID: 8, DownloadID: "aa", ContentKind: "episode", EpisodeID: 2})
		}
		live.Content = append(live.Content, ContentState{Key: "episode:2", Exists: true, Monitored: true, Detail: "monitored"})
		return live
	}
	res, err := c.Execute(ctx, p)
	if err != nil || res.State != store.IntentSucceeded {
		t.Fatalf("execute: %+v %v", res, err)
	}
	oc, _ := c.Store.Outcomes(ctx, []int64{res.IntentID})
	o := oc[res.IntentID]
	if !slices.Equal(o.Content, []string{"episode:1", "episode:2"}) || len(o.PerContent) != 2 {
		t.Fatalf("outcome %+v, want both episodes followed", o)
	}
}
