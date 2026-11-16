package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/adapter/qbittorrent"
	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/adapter/sonarr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// The real adapters provide the recovery operations.
var (
	_ Manager = (*sonarr.Client)(nil)
	_ Manager = (*radarr.Client)(nil)
	_ Client  = (*qbittorrent.Client)(nil)
)

type fakeManager struct {
	rows      []domain.QueueItem
	blocklist bool
	history   []string
	removed   []arr.RemoveFlags
	match     arr.BlocklistMatch
}

func (m *fakeManager) ID() string { return "sonarr" }
func (m *fakeManager) Queue(context.Context) (domain.QueueSnapshot, error) {
	return domain.QueueSnapshot{Items: m.rows, Complete: true}, nil
}
func (m *fakeManager) RemoveQueueItem(_ context.Context, _ int64, f arr.RemoveFlags) error {
	m.removed = append(m.removed, f)
	return nil
}
func (m *fakeManager) Blocklisted(_ context.Context, b arr.BlocklistMatch) (bool, error) {
	m.match = b
	return m.blocklist, nil
}
func (m *fakeManager) HistoryEvents(context.Context, string) ([]string, error) { return m.history, nil }

type fakeClient struct {
	ts      []domain.Transfer
	stopped []string
}

func (c *fakeClient) ID() string { return "qbit" }
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

func TestObserveQueueFail(t *testing.T) {
	ctx := context.Background()
	req := `{"removeFromClient":false,"blocklist":true,"skipRedownload":false,"changeCategory":false}`
	cases := []struct {
		name string
		m    fakeManager
		want action.Verdict
	}{
		{"effective", fakeManager{blocklist: true, history: []string{"downloadFailed", "grabbed"}}, action.Holds},
		{"row still tracked, nothing recorded", fakeManager{rows: []domain.QueueItem{{DownloadID: "AA", QueueID: 7}}, history: []string{"grabbed"}}, action.NotHolds},
		{"row gone but not blocklisted", fakeManager{history: []string{"downloadFailed", "grabbed"}}, action.Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.m
			st := step("manager.queue.fail", qtarget, req)
			at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
			st.DispatchedAt = &at
			obs, err := rec(&m, &fakeClient{}).Observe(ctx, st)
			if err != nil || obs.Verdict != c.want {
				t.Fatalf("%+v %v", obs, err)
			}
			if c.want == action.Holds && (m.match.SourceTitle != "Show.S01E01" || m.match.EpisodeID != 9 || !m.match.Since.Before(at)) {
				t.Fatalf("blocklist matched by %+v", m.match)
			}
		})
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

func TestRefetchMatchesPlannerFingerprint(t *testing.T) {
	tr := domain.Transfer{NativeID: "aa", StateClass: domain.StateDownloading, VerifiedBytes: domain.KnownValue[int64](10)}
	m := &fakeManager{rows: []domain.QueueItem{{DownloadID: "aa", QueueID: 7}}}
	c := &fakeClient{ts: []domain.Transfer{tr}}
	want := action.Facts{ClientID: "qbit", NativeID: "aa", Present: true, StateClass: domain.StateDownloading, Verified: tr.VerifiedBytes,
		ManagerID: "sonarr", QueueID: 7, DownloadID: "aa"}.Fingerprint()
	p := action.Proposal{Subject: json.RawMessage(`{"client_id":"qbit","native_id":"aa"}`),
		Operations: []action.Operation{{Op: "manager.queue.fail", Target: json.RawMessage(qtarget)}}}
	got, err := rec(m, c).Refetch(context.Background(), p)
	if err != nil || got != want {
		t.Fatalf("fingerprint %s want %s (%v)", got, want, err)
	}
	// Progress since the preview changes the fingerprint.
	c.ts[0].VerifiedBytes = domain.KnownValue[int64](11)
	if got, _ := rec(m, c).Refetch(context.Background(), p); got == want {
		t.Fatal("fingerprint ignores verified progress")
	}
}
