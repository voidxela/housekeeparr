package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/adapter/deluge"
	"github.com/voidxela/housekeeparr/internal/adapter/qbittorrent"
	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/adapter/rtorrent"
	"github.com/voidxela/housekeeparr/internal/adapter/sonarr"
	"github.com/voidxela/housekeeparr/internal/adapter/transmission"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// The real adapters provide the recovery operations.
var (
	_ Manager = (*sonarr.Client)(nil)
	_ Manager = (*radarr.Client)(nil)
	_ Client  = (*qbittorrent.Client)(nil)
	_ Client  = (*transmission.Client)(nil)
	_ Client  = (*deluge.Client)(nil)
	_ Client  = (*rtorrent.Client)(nil)
)

type fakeManager struct {
	id       string
	rows     []domain.QueueItem
	queueErr error
	health   []domain.HealthItem
	content  map[string]arr.Content
	blockIDs []int64
	history  []arr.HistoryEvent
	removed  []arr.RemoveFlags
	match    arr.BlocklistMatch
}

func (m *fakeManager) ID() string {
	if m.id == "" {
		return "sonarr"
	}
	return m.id
}
func (m *fakeManager) Probe(context.Context) (adapter.Probe, error) {
	return adapter.Probe{Version: "4.0.20.3014"}, nil
}
func (m *fakeManager) Queue(context.Context) (domain.QueueSnapshot, error) {
	return domain.QueueSnapshot{Items: m.rows, Complete: m.queueErr == nil}, m.queueErr
}
func (m *fakeManager) Health(context.Context) (domain.HealthSnapshot, error) {
	return domain.HealthSnapshot{Items: m.health}, nil
}
func (m *fakeManager) Content(_ context.Context, key string) (arr.Content, error) {
	if c, ok := m.content[key]; ok {
		return c, nil
	}
	return arr.Content{Key: key, Exists: true, Monitored: true, Detail: "monitored"}, nil
}
func (m *fakeManager) RemoveQueueItem(_ context.Context, _ int64, f arr.RemoveFlags) error {
	m.removed = append(m.removed, f)
	return nil
}
func (m *fakeManager) BlocklistEntries(_ context.Context, b arr.BlocklistMatch) ([]int64, error) {
	m.match = b
	return m.blockIDs, nil
}
func (m *fakeManager) History(context.Context, string) ([]arr.HistoryEvent, error) {
	return m.history, nil
}

type fakeClient struct {
	ts      []domain.Transfer
	stopped []string
}

func (c *fakeClient) ID() string { return "qbit" }
func (c *fakeClient) Probe(context.Context) (adapter.Probe, error) {
	return adapter.Probe{Version: "v5.3.0"}, nil
}
func (c *fakeClient) ListTransfers(context.Context) (domain.TransferSnapshot, error) {
	return domain.TransferSnapshot{Transfers: c.ts, Complete: true}, nil
}
func (c *fakeClient) Stop(_ context.Context, id string) error {
	c.stopped = append(c.stopped, id)
	return nil
}

func rec(m *fakeManager, c *fakeClient) *Recovery {
	return &Recovery{Managers: map[string]Manager{"sonarr": m}, Clients: map[string]Client{"qbit": c}, Settle: 10 * time.Millisecond, Poll: time.Millisecond}
}

func step(op, target, request string) store.Step {
	return store.Step{Op: op, Target: json.RawMessage(target), Request: json.RawMessage(request)}
}

const qtarget = `{"manager_id":"sonarr","queue_id":7,"download_id":"aa","title":"Show.S01E01","series_id":3,"episode_id":9}`

func TestDispatchRefusesImplicitOrDestructiveFlags(t *testing.T) {
	m, c := &fakeManager{}, &fakeClient{}
	r := rec(m, c)
	ctx := context.Background()
	for _, req := range []string{`{"blocklist":true}`, `{"removeFromClient":true,"blocklist":true,"skipRedownload":true,"changeCategory":false}`} {
		if _, err := r.Dispatch(ctx, step("manager.queue.fail", qtarget, req)); err == nil {
			t.Errorf("accepted %s", req)
		}
	}
	if _, err := r.Dispatch(ctx, step("client.stop", `{"client_id":"qbit","native_id":"aa"}`, `{}`)); err == nil {
		t.Error("stop without explicit delete_data=false accepted")
	}
	if len(m.removed)+len(c.stopped) != 0 {
		t.Fatal("refused requests reached an adapter")
	}
	if _, err := r.Dispatch(ctx, step("manager.queue.fail", qtarget, `{"removeFromClient":false,"blocklist":true,"skipRedownload":false,"changeCategory":false}`)); err != nil {
		t.Fatal(err)
	}
	if len(m.removed) != 1 || m.removed[0] != (arr.RemoveFlags{Blocklist: true}) {
		t.Fatalf("flags %+v", m.removed)
	}
}

func hist(pairs ...any) []arr.HistoryEvent {
	var out []arr.HistoryEvent
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, arr.HistoryEvent{ID: int64(pairs[i].(int)), EventType: pairs[i+1].(string)})
	}
	return out
}

func TestObserveQueueFail(t *testing.T) {
	ctx := context.Background()
	req := `{"removeFromClient":false,"blocklist":true,"skipRedownload":false,"changeCategory":false}`
	// Before dispatch: the grab (history 10) and an old, unrelated
	// blocklist entry with the same title for the same episode (id 5).
	baseline := `{"blocklist_ids":[5],"history_ids":[10]}`
	cases := []struct {
		name     string
		m        fakeManager
		baseline string
		want     action.Verdict
	}{
		{"effective", fakeManager{blockIDs: []int64{6, 5}, history: hist(11, "downloadFailed", 10, "grabbed")}, baseline, action.Holds},
		{"row still tracked, nothing new", fakeManager{rows: []domain.QueueItem{{DownloadID: "AA", QueueID: 7}}, blockIDs: []int64{5},
			history: hist(10, "grabbed")}, baseline, action.NotHolds},
		{"row gone but not blocklisted", fakeManager{blockIDs: []int64{5}, history: hist(11, "downloadFailed", 10, "grabbed")}, baseline, action.Unknown},
		// Review decision 3: an old failure and an old matching entry are
		// not this action's effect.
		{"only stale evidence", fakeManager{blockIDs: []int64{5}, history: hist(10, "grabbed", 9, "downloadFailed")}, `{"blocklist_ids":[5],"history_ids":[10,9]}`, action.Unknown},
		{"no baseline", fakeManager{blockIDs: []int64{6}, history: hist(11, "downloadFailed")}, "", action.Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.m
			st := step("manager.queue.fail", qtarget, req)
			at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
			st.DispatchedAt = &at
			if c.baseline != "" {
				st.Baseline = json.RawMessage(c.baseline)
			}
			obs, err := rec(&m, &fakeClient{}).Observe(ctx, st)
			if err != nil || obs.Verdict != c.want {
				t.Fatalf("%+v %v", obs, err)
			}
			if c.want == action.Holds && (m.match.SourceTitle != "Show.S01E01" || m.match.EpisodeID != 9) {
				t.Fatalf("blocklist matched by %+v", m.match)
			}
		})
	}
}

func TestBaselineRecordsExistingEvidence(t *testing.T) {
	m := &fakeManager{blockIDs: []int64{5}, history: hist(10, "grabbed")}
	b, err := rec(m, &fakeClient{}).Baseline(context.Background(), step("manager.queue.fail", qtarget, `{}`))
	if err != nil || string(b) != `{"blocklist_ids":[5],"history_ids":[10]}` {
		t.Fatalf("baseline %s %v", b, err)
	}
	if b, err := rec(m, &fakeClient{}).Baseline(context.Background(), step("client.stop", `{}`, `{}`)); err != nil || b != nil {
		t.Fatalf("stop baseline %s %v", b, err)
	}
}

func TestObserveStop(t *testing.T) {
	ctx := context.Background()
	st := step("client.stop", `{"client_id":"qbit","native_id":"aa"}`, `{"delete_data":false}`)
	for _, c := range []struct {
		ts   []domain.Transfer
		want action.Verdict
	}{
		{[]domain.Transfer{{NativeID: "aa", StateClass: domain.StatePaused}}, action.Holds},
		{[]domain.Transfer{{NativeID: "aa", StateClass: domain.StateDownloading}}, action.Unknown},
		{nil, action.NotHolds},
	} {
		obs, err := rec(&fakeManager{}, &fakeClient{ts: c.ts}).Observe(ctx, st)
		if err != nil || obs.Verdict != c.want {
			t.Fatalf("%v: %+v %v", c.ts, obs, err)
		}
	}
}

func liveProposal() action.Proposal {
	return action.Proposal{Operations: []action.Operation{{Op: "manager.queue.fail"}, {Op: "client.stop"}},
		Planned: action.Planned{ClientID: "qbit", NativeID: "aa", StateClass: domain.StateDownloading, Verified: domain.KnownValue[int64](10),
			Private: domain.KnownValue(false), ClientVersion: "5.3.0", ManagerID: "sonarr", ManagerVersion: "4.0.20.3014", QueueID: 7, DownloadID: "aa",
			Content: []string{"episode:9"}}}
}

func TestLiveReadsEveryDecisionInput(t *testing.T) {
	ctx := context.Background()
	tr := domain.Transfer{NativeID: "aa", StateClass: domain.StateDownloading, VerifiedBytes: domain.KnownValue[int64](10), Private: domain.KnownValue(false)}
	m := &fakeManager{rows: []domain.QueueItem{{ManagerID: "sonarr", DownloadID: "AA", QueueID: 7, ContentKind: "episode", SeriesID: 3, EpisodeID: 9}}}
	other := &fakeManager{id: "radarr"}
	c := &fakeClient{ts: []domain.Transfer{tr}}
	r := rec(m, c)
	r.Managers["radarr"] = other
	p := liveProposal()
	check := func(i int) []string {
		t.Helper()
		live, err := r.Live(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		var bad []string
		for _, ch := range action.Revalidate(p, i, live) {
			if !ch.Satisfied {
				bad = append(bad, ch.Name)
			}
		}
		return bad
	}
	if bad := check(0); len(bad) != 0 {
		t.Fatalf("matching state fails %v", bad)
	}
	// Review R5 reproduction: privacy and content changes are visible.
	c.ts[0].Private = domain.KnownValue(true)
	if bad := check(0); len(bad) != 1 || bad[0] != "public transfer" {
		t.Fatalf("privacy change: %v", bad)
	}
	c.ts[0].Private = domain.KnownValue(false)
	m.rows[0].SeriesID, m.rows[0].EpisodeID = 999, 999
	if bad := check(0); len(bad) != 1 || bad[0] != "ownership unchanged" {
		t.Fatalf("content change: %v", bad)
	}
	m.rows[0].EpisodeID = 9
	// Monitoring and health of the planned manager.
	m.content = map[string]arr.Content{"episode:9": {Key: "episode:9", Exists: true, Detail: "the series is unmonitored"}}
	if bad := check(0); len(bad) != 1 || bad[0] != "content monitored" {
		t.Fatalf("unmonitored: %v", bad)
	}
	m.content = nil
	m.health = []domain.HealthItem{{Source: "DownloadClientStatusCheck", Type: "error", Message: "All download clients are unavailable"}}
	if bad := check(0); len(bad) != 1 || bad[0] != "manager health" {
		t.Fatalf("health: %v", bad)
	}
	m.health = nil
	// Another manager's row for the same torrent, or an unreadable queue.
	other.rows = []domain.QueueItem{{DownloadID: "aa", QueueID: 3, ContentKind: "movie", MovieID: 4}}
	if bad := check(0); len(bad) != 1 || bad[0] != "ownership unchanged" {
		t.Fatalf("shared ownership: %v", bad)
	}
	other.rows = nil
	other.queueErr = errors.New("timeout")
	if bad := check(0); len(bad) != 1 || bad[0] != "ownership unchanged" {
		t.Fatalf("unread manager: %v", bad)
	}
	other.queueErr = nil
	// Before the stop, the failed row's absence is expected; its presence
	// is not, and manager health no longer matters.
	if bad := check(1); len(bad) != 1 || bad[0] != "ownership unchanged" {
		t.Fatalf("row still present before stop: %v", bad)
	}
	m.rows = nil
	m.health = []domain.HealthItem{{Source: "IndexerSearchCheck", Type: "warning"}}
	if bad := check(1); len(bad) != 0 {
		t.Fatalf("clean state before stop fails %v", bad)
	}
	c.ts[0].VerifiedBytes = domain.KnownValue[int64](11)
	if bad := check(1); len(bad) != 1 || bad[0] != "no new verified progress" {
		t.Fatalf("progress before stop: %v", bad)
	}
}
