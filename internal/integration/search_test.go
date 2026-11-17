//go:build integration

package integration

// Proactive missing-content searches (SPEC §9.1) on disposable released
// managers, through the production path: the real engine reads the
// manager's wanted/missing list, and the real dispatcher plans, journals,
// revalidates, and sends exactly one search through the coordinator and
// executor. Independent checks then establish that the manager queued a
// search command for exactly that content and grabbed the stand-in's
// release for it (the indexer offers one), which is more than the workflow
// itself claims: acceptance is all Housekeeparr verifies.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

func TestSonarrProactiveSearch(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := episodeID(t, a, sid, 1, 11)
	runProactiveSearch(t, a, "sonarr", mm["tv-e11-search"], fmt.Sprintf("episode:%d", ep))
}

func TestRadarrProactiveSearch(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	e := mm["film-k-search"]
	id := addMovie(t, a, e.Managed.TmdbID)
	runProactiveSearch(t, a, "radarr", e, fmt.Sprintf("movie:%d", id))
}

func runProactiveSearch(t *testing.T, a arrAPI, section string, e managedEntry, key string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dcMap, clientBlock := managerTOMLConfig(t)
	keyEnv := "HK_IT_" + strings.ToUpper(section) + "_KEY"
	src := "version = 1\nmode = \"automatic\"\n[polling]\njitter = \"0s\"\n[automation.workflows]\nmissing_search = true\n[[" + section + "]]\nid = \"" +
		section + "\"\nurl = \"" + env(t, "HK_IT_"+strings.ToUpper(section)+"_URL") + "\"\napi_key = \"env:" + keyEnv + "\"\n" + dcMap + "\n" + clientBlock
	cfg, _, err := config.Parse([]byte(src), filepath.Join(dir, "h.toml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := cfg.ResolvedSecrets(nil)
	conns, err := engine.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Conns: conns, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "once", Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	rep := eng.Cycle(ctx, nil)
	for _, c := range rep.Connections {
		if !c.OK {
			t.Fatalf("cycle %s: %s", c.ID, c.Error)
		}
	}
	it, err := st.MissingRecord(ctx, section, key)
	if err != nil || it == nil {
		list, _ := st.MissingContent(ctx, store.MissingFilter{})
		t.Fatalf("%s not in the stored missing list (%d items): %v", key, len(list), err)
	}
	if it.Available == nil || !*it.Available {
		t.Fatalf("missing item not available: %+v", it)
	}
	t.Logf("missing list read: %s %q aired %v available %v", it.ContentKey, it.Title, it.AiredAt, *it.Available)
	// What this test establishes, for exactly this manager version.
	var mp *adapter.Probe
	recs, _ := st.Connections(ctx)
	for _, c := range recs {
		if c.ID == section {
			mp = c.Probe
		}
	}
	saved := action.Tested
	action.Tested = []action.TestedWorkflow{{Workflow: policy.WorkflowMissingSearch, Manager: adapter.Kind(section), ManagerVersion: strings.TrimPrefix(mp.Version, "v")}}
	defer func() { action.Tested = saved }()

	svc := app.New(st, cfg, nil, eng)
	svc.SetExecutor(workflow.FromConnections(conns.Clients, conns.Managers))
	pv, err := svc.PreviewSearch(ctx, section, key, action.OriginAutomatic)
	if err != nil {
		t.Fatal(err)
	}
	if b := pv.Proposal.Blockers(); len(b) > 0 {
		t.Fatalf("search not eligible: %+v", b)
	}
	sr, err := svc.DispatchSearches(ctx, rep.Fresh())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dispatch: %+v", sr)
	if sr.Sent != section+"|"+key || sr.Outcome != string(store.IntentSucceeded) {
		t.Fatalf("search not sent for %s: %+v", key, sr)
	}
	after, _ := st.MissingRecord(ctx, section, key)
	if after.State != store.MissingSearched || after.Searches != 1 || after.NextSearchAt == nil {
		t.Fatalf("search state %+v", after)
	}
	// Independent of the executor's own command check: the manager grabbed
	// the stand-in's release for exactly this content.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if r := a.row(nil, e.Hash); r != nil {
			t.Logf("grabbed after the search: queue item %d %q", r.ID, r.Title)
			// A second pass right away sends nothing: the content is queued,
			// the search is spaced, and it is backing off.
			if sr2, err := svc.DispatchSearches(ctx, rep.Fresh()); err != nil || sr2.Sent != "" {
				t.Fatalf("second pass sent %+v %v", sr2, err)
			}
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no grab of %s after the search (history %v)", e.Hash, a.history(e.Hash))
}
