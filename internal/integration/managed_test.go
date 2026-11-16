//go:build integration

package integration

// Managed-content probes: Sonarr and Radarr add fictional content served by
// the metadata stand-in (internal/integration/standin), search the stand-in
// Torznab indexer, and grab a synthetic release into the disposable
// download client. That produces real grab history through the managers'
// own API; no manager database is edited. The removal probes then run on
// rows with that history. Setup writes go to disposable instances only and
// are sent from the test, never through a Housekeeparr adapter.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

type managedEntry struct {
	Key, Name, Hash string
	Size            int64
	Files           []string
	Managed         struct {
		Kind            string
		TvdbID          int `json:"tvdbId"`
		TmdbID          int `json:"tmdbId"`
		Season, Episode int
	}
}

func managedManifest(t *testing.T) map[string]managedEntry {
	t.Helper()
	b, err := os.ReadFile(strings.TrimSuffix(env(t, "HK_IT_MANIFEST"), "manifest.json") + "managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var list []managedEntry
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]managedEntry{}
	for _, e := range list {
		out[e.Key] = e
	}
	return out
}

func (e managedEntry) asEntry() entry {
	mgr := ""
	if e.Managed.Kind == "movie" {
		mgr = "radarr"
	}
	return entry{Key: e.Key, Name: e.Name, Hash: e.Hash, Manager: mgr, Files: e.Files}
}

func (a arrAPI) mustJSON(method, path string, q url.Values, body, out any) {
	a.t.Helper()
	code, b := a.do(method, path, q, body)
	if code < 200 || code > 299 {
		a.t.Fatalf("%s %s: HTTP %d %s", method, path, code, truncate(b))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			a.t.Fatalf("%s %s: %v: %s", method, path, err, truncate(b))
		}
	}
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

// commandBody posts a command with a body and waits for it to finish.
func (a arrAPI) commandBody(body map[string]any) string {
	a.t.Helper()
	var c struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	a.mustJSON(http.MethodPost, "/api/v3/command", nil, body, &c)
	deadline := time.Now().Add(120 * time.Second)
	for c.Status != "completed" && c.Status != "failed" && time.Now().Before(deadline) {
		time.Sleep(time.Second)
		a.mustJSON(http.MethodGet, fmt.Sprintf("/api/v3/command/%d", c.ID), nil, nil, &c)
	}
	return c.Status
}

// prepareManager adds a root folder and the stand-in Torznab indexer.
// forceSave skips the manager's connection test; the indexer is then
// exercised by the real search below.
func prepareManager(t *testing.T, a arrAPI, root string, categories []int) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a later managed test on the same instance reuses them.
	var roots []struct {
		Path string `json:"path"`
	}
	a.mustJSON(http.MethodGet, "/api/v3/rootfolder", nil, nil, &roots)
	for _, r := range roots {
		if r.Path == root {
			return
		}
	}
	a.mustJSON(http.MethodPost, "/api/v3/rootfolder", nil, map[string]any{"path": root}, nil)
	idx := map[string]any{
		"name": "Housekeeparr stand-in", "implementation": "Torznab", "configContract": "TorznabSettings", "protocol": "torrent",
		"enableRss": false, "enableAutomaticSearch": true, "enableInteractiveSearch": true, "priority": 25, "tags": []int{},
		"fields": []map[string]any{
			{"name": "baseUrl", "value": env(t, "HK_IT_INDEXER_URL")},
			{"name": "apiPath", "value": "/api"},
			{"name": "apiKey", "value": env(t, "HK_IT_INDEXER_KEY")},
			{"name": "categories", "value": categories},
			{"name": "minimumSeeders", "value": 1},
		},
	}
	a.mustJSON(http.MethodPost, "/api/v3/indexer", url.Values{"forceSave": {"true"}}, idx, nil)

	// The synthetic releases are a few MiB, far below the default per-minute
	// size minimums, so the disposable instance's quality definitions are
	// relaxed to a zero minimum through the manager's own API.
	var defs []map[string]any
	a.mustJSON(http.MethodGet, "/api/v3/qualitydefinition", nil, nil, &defs)
	for _, d := range defs {
		d["minSize"] = 0
	}
	a.mustJSON(http.MethodPut, "/api/v3/qualitydefinition/update", nil, defs, nil)
}

// waitGrab waits until the manager's queue tracks the release's infohash
// and its history records the grab. It returns the queue row.
//
// search issues the manager's targeted search command. It is retried at
// most three times: a search sent right after the quality definitions are
// changed can still be evaluated against the cached defaults (observed on
// Sonarr 4.0.20.3014).
func waitGrab(t *testing.T, a arrAPI, e managedEntry, search map[string]any, diag url.Values) *queueRow {
	t.Helper()
	for attempt := 1; attempt <= 3; attempt++ {
		st := a.commandBody(search)
		t.Logf("%s: %s attempt %d → %s", e.Key, search["name"], attempt, st)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if r := a.row(nil, e.Hash); r != nil {
				for _, ev := range a.history(e.Hash) {
					if ev == "grabbed" {
						return r
					}
				}
			}
			time.Sleep(2 * time.Second)
		}
	}
	// Ask the manager why: an interactive search lists each candidate with
	// its rejection reasons.
	_, b := a.do(http.MethodGet, "/api/v3/release", diag, nil)
	var rels []struct {
		Title      string   `json:"title"`
		Rejected   bool     `json:"rejected"`
		Rejections []string `json:"rejections"`
	}
	_ = json.Unmarshal(b, &rels)
	for _, r := range rels {
		t.Logf("%s: candidate %q rejected=%v %v", e.Key, r.Title, r.Rejected, r.Rejections)
	}
	t.Fatalf("%s: no tracked grab for %s (history %v; %d candidates for %s)", e.Key, e.Hash, a.history(e.Hash), len(rels), diag.Encode())
	return nil
}

// settle waits until the client reports the grabbed torrent in a stable
// (non-checking, non-transient) state, so the probe's "before" snapshot is
// not taken mid-check.
func settle(t *testing.T, e managedEntry) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if tr, ok := clientState(t, e.Hash); ok && tr.StateClass != domain.StateChecking && tr.StateClass != domain.StateTransient {
			t.Logf("%s: settled in client: %s/%s verified=%v", e.Key, tr.NativeState, tr.StateClass, tr.VerifiedBytes)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s: torrent never settled in the client", e.Key)
}

// settleIncomplete is settle for a workflow that needs the grab to remain an
// incomplete download. Transmission 4 treats an existing payload file under
// its final name (not ".part") as complete when the manager adds the
// torrent, without hashing it, so the half-present synthetic release first
// shows as seeding. The harness forces a verify on the disposable daemon,
// as its observation fixtures do, and then requires a downloading state.
func settleIncomplete(t *testing.T, e managedEntry) {
	t.Helper()
	if managerClientKind() == "transmission" {
		transmissionCall(t, env(t, "HK_IT_TR_URL"), "torrent-verify", map[string]any{"ids": []string{e.Hash}})
	}
	deadline := time.Now().Add(60 * time.Second)
	var last domain.Transfer
	for time.Now().Before(deadline) {
		if tr, ok := clientState(t, e.Hash); ok {
			last = tr
			if tr.StateClass == domain.StateDownloading {
				t.Logf("%s: settled in client: %s/%s verified=%v", e.Key, tr.NativeState, tr.StateClass, tr.VerifiedBytes)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s: grab never settled as an incomplete download (last %s/%s verified=%v)", e.Key, last.NativeState, last.StateClass, last.VerifiedBytes)
}

func firstQualityProfile(a arrAPI) int {
	var ps []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	a.mustJSON(http.MethodGet, "/api/v3/qualityprofile", nil, nil, &ps)
	for _, p := range ps {
		if p.Name == "Any" {
			return p.ID
		}
	}
	if len(ps) == 0 {
		a.t.Fatal("no quality profiles")
	}
	return ps[0].ID
}

// managedVariants are the removal variants probed on rows with grab
// history, in order, each on its own release. Every flag is explicit.
type managedVariant struct {
	name string
	// flags for DELETE /api/v3/queue/{id}
	flags url.Values
	// removeFailed sets removeFailedDownloads on the manager's download
	// client for this variant only.
	removeFailed bool
}

func managedVariants() []managedVariant {
	redownload := blocklistFlags()
	redownload.Set("skipRedownload", "false")
	return []managedVariant{
		{name: "ignore", flags: ignoreFlags()},
		{name: "blocklist", flags: blocklistFlags()},
		{name: "blocklist-removeFailedDownloads", flags: blocklistFlags(), removeFailed: true},
		{name: "blocklist-redownload", flags: redownload},
	}
}

func TestSonarrManagedRemovalSemantics(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})

	var found []map[string]any
	a.mustJSON(http.MethodGet, "/api/v3/series/lookup", url.Values{"term": {"tvdb:999001"}}, nil, &found)
	if len(found) != 1 {
		t.Fatalf("lookup returned %d series", len(found))
	}
	s := found[0]
	s["qualityProfileId"] = firstQualityProfile(a)
	s["rootFolderPath"] = env(t, "HK_IT_WORK") + "/library/tv"
	s["monitored"] = true
	s["seasonFolder"] = true
	s["addOptions"] = map[string]any{"monitor": "all", "searchForMissingEpisodes": false, "searchForCutoffUnmetEpisodes": false}
	var series struct {
		ID int `json:"id"`
	}
	a.mustJSON(http.MethodPost, "/api/v3/series", nil, s, &series)

	keys := []string{"tv-e01", "tv-e02", "tv-e03", "tv-e04"}
	var eps []struct {
		ID            int  `json:"id"`
		SeasonNumber  int  `json:"seasonNumber"`
		EpisodeNumber int  `json:"episodeNumber"`
		Monitored     bool `json:"monitored"`
	}
	deadline := time.Now().Add(90 * time.Second)
	for len(eps) < len(keys) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		a.mustJSON(http.MethodGet, "/api/v3/episode", url.Values{"seriesId": {fmt.Sprint(series.ID)}}, nil, &eps)
	}
	if len(eps) < len(keys) {
		t.Fatalf("series %d has %d episodes after refresh", series.ID, len(eps))
	}
	byKey := map[string]int{}
	for _, ep := range eps {
		t.Logf("episode id=%d S%02dE%02d monitored=%v", ep.ID, ep.SeasonNumber, ep.EpisodeNumber, ep.Monitored)
		for k, e := range mm {
			if e.Managed.Kind == "tv" && e.Managed.Season == ep.SeasonNumber && e.Managed.Episode == ep.EpisodeNumber {
				byKey[k] = ep.ID
			}
		}
	}
	for _, k := range keys {
		r := waitGrab(t, a, mm[k], map[string]any{"name": "EpisodeSearch", "episodeIds": []int{byKey[k]}},
			url.Values{"episodeId": {fmt.Sprint(byKey[k])}})
		settle(t, mm[k])
		t.Logf("%s: grabbed; queue id=%d state=%s title=%q", k, r.ID, r.State, r.Title)
	}
	for i, v := range managedVariants() {
		t.Run(v.name, func(t *testing.T) { a.t = t; probeManagedRemoval(t, a, mm[keys[i]], v) })
	}
}

func TestRadarrManagedRemovalSemantics(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	keys := []string{"film-a", "film-b", "film-c", "film-d"}
	for _, k := range keys {
		e := mm[k]
		var found []map[string]any
		a.mustJSON(http.MethodGet, "/api/v3/movie/lookup", url.Values{"term": {fmt.Sprintf("tmdb:%d", e.Managed.TmdbID)}}, nil, &found)
		if len(found) != 1 {
			t.Fatalf("%s: lookup returned %d movies", k, len(found))
		}
		mv := found[0]
		mv["qualityProfileId"] = firstQualityProfile(a)
		mv["rootFolderPath"] = env(t, "HK_IT_WORK") + "/library/movies"
		mv["monitored"] = true
		mv["minimumAvailability"] = "released"
		mv["addOptions"] = map[string]any{"monitor": "movieOnly", "searchForMovie": false}
		var movie struct {
			ID int `json:"id"`
		}
		a.mustJSON(http.MethodPost, "/api/v3/movie", nil, mv, &movie)
		r := waitGrab(t, a, e, map[string]any{"name": "MoviesSearch", "movieIds": []int{movie.ID}},
			url.Values{"movieId": {fmt.Sprint(movie.ID)}})
		settle(t, e)
		t.Logf("%s: grabbed; queue id=%d state=%s title=%q", k, r.ID, r.State, r.Title)
	}
	for i, v := range managedVariants() {
		t.Run(v.name, func(t *testing.T) { a.t = t; probeManagedRemoval(t, a, mm[keys[i]], v) })
	}
}

// setRemoveFailed sets removeFailedDownloads on the manager's single
// download client and returns a function restoring the previous value.
func setRemoveFailed(t *testing.T, a arrAPI, on bool) func() {
	t.Helper()
	var dcs []map[string]any
	a.mustJSON(http.MethodGet, "/api/v3/downloadclient", nil, nil, &dcs)
	if len(dcs) != 1 {
		t.Fatalf("expected one download client, found %d", len(dcs))
	}
	dc := dcs[0]
	prev := dc["removeFailedDownloads"]
	dc["removeFailedDownloads"] = on
	id := fmt.Sprint(dc["id"])
	a.mustJSON(http.MethodPut, "/api/v3/downloadclient/"+id, url.Values{"forceSave": {"true"}}, dc, nil)
	return func() {
		dc["removeFailedDownloads"] = prev
		a.mustJSON(http.MethodPut, "/api/v3/downloadclient/"+id, url.Values{"forceSave": {"true"}}, dc, nil)
	}
}

type arrCommand struct {
	Name    string    `json:"name"`
	Trigger string    `json:"trigger"`
	Status  string    `json:"status"`
	Queued  time.Time `json:"queued"`
}

// searchesSince lists search commands queued at or after since.
func (a arrAPI) searchesSince(since time.Time) []arrCommand {
	a.t.Helper()
	var cs []arrCommand
	a.mustJSON(http.MethodGet, "/api/v3/command", nil, nil, &cs)
	var out []arrCommand
	for _, c := range cs {
		if strings.Contains(c.Name, "Search") && !c.Queued.Before(since) {
			out = append(out, c)
		}
	}
	return out
}

func count(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}

// probeManagedRemoval removes a managed queue row (one with real grab
// history) and asserts the semantics observed on Sonarr 4.0.20.3014 and
// Radarr 6.4.4.10685 with qBittorrent 5.3.0: the removal is durable across
// refreshes; the ignore path records downloadIgnored and blocklists
// nothing; the blocklist path records downloadFailed and blocklists exactly
// that release; removeFromClient=false keeps the torrent registered, in the
// same state and category, with its payload untouched, even when the
// download client has removeFailedDownloads enabled; skipRedownload=true
// queues no search, and skipRedownload=false makes the manager queue its
// own replacement search. A version or client that behaves differently
// fails here and must be re-evaluated, not assumed.
func probeManagedRemoval(t *testing.T, a arrAPI, me managedEntry, v managedVariant) {
	t.Helper()
	e := me.asEntry()
	if v.removeFailed {
		defer setRemoveFailed(t, a, true)()
	}
	row := a.row(nil, e.Hash)
	if row == nil {
		t.Fatalf("%s: no queue row to probe", e.Key)
	}
	before, ok := clientState(t, e.Hash)
	if !ok {
		t.Fatalf("%s: torrent not in client before probe", e.Key)
	}
	digest := payloadDigest(t, e)
	blBefore := a.blocklist()
	histBefore := a.history(e.Hash)
	start := time.Now().UTC().Add(-time.Second)
	code, body := a.do(http.MethodDelete, fmt.Sprintf("/api/v3/queue/%d", row.ID), v.flags, nil)
	t.Logf("%s: DELETE /api/v3/queue/%d?%s → HTTP %d %s", e.Key, row.ID, v.flags.Encode(), code, strings.TrimSpace(string(body)))
	if code != 200 {
		t.Errorf("%s: removal not accepted (HTTP %d)", e.Key, code)
	}
	time.Sleep(2 * time.Second)
	gone := a.row(nil, e.Hash) == nil
	var refreshes []string
	present := false
	for i := 0; i < 2; i++ { // durability: two refreshes
		refreshes = append(refreshes, a.command("RefreshMonitoredDownloads"))
		time.Sleep(3 * time.Second)
		present = present || a.row(nil, e.Hash) != nil
	}
	after, ok := clientState(t, e.Hash)
	bl := a.blocklist()
	hist := a.history(e.Hash)
	searches := a.searchesSince(start)
	// The manager queues its replacement search asynchronously after the
	// failure event; on a loaded host that can take longer than the two
	// refreshes above (observed once with Radarr 6.4.4 + rTorrent 0.9.8).
	if v.flags.Get("skipRedownload") == "false" {
		for deadline := time.Now().Add(60 * time.Second); len(searches) == 0 && time.Now().Before(deadline); {
			time.Sleep(2 * time.Second)
			searches = a.searchesSince(start)
		}
	}
	t.Logf("%s: row gone immediately=%v; after RefreshMonitoredDownloads %v row ever present=%v; blocklist %v → %v; history %v → %v; searches queued %+v",
		e.Key, gone, refreshes, present, blBefore, bl, histBefore, hist, searches)
	t.Logf("%s: client before %s/%s cat=%q, after present=%v %s/%s cat=%q; payload %s", e.Key, before.NativeState, before.VerifiedBytes,
		before.Category, ok, after.NativeState, after.VerifiedBytes, after.Category, digest)

	// Preservation.
	if !ok {
		t.Errorf("%s: torrent no longer registered in the client after removal with removeFromClient=false", e.Key)
	} else if after.NativeState != before.NativeState || after.Category != before.Category {
		t.Errorf("%s: client state changed: %s %v → %s %v", e.Key, before.NativeState, before.Category, after.NativeState, after.Category)
	}
	if d := payloadDigest(t, e); d != digest {
		t.Errorf("%s: payload changed: %s → %s", e.Key, digest, d)
	}
	// Durability.
	if !gone || present {
		t.Errorf("%s: removal not durable: gone immediately=%v, present after a refresh=%v", e.Key, gone, present)
	}
	blocklisted := v.flags.Get("blocklist") == "true"
	added := len(bl) - len(blBefore)
	switch {
	case blocklisted && (added != 1 || count(bl, me.Name) != count(blBefore, me.Name)+1):
		t.Errorf("%s: blocklist path did not blocklist exactly this release: %v → %v", e.Key, blBefore, bl)
	case !blocklisted && added != 0:
		t.Errorf("%s: ignore path changed the blocklist: %v → %v", e.Key, blBefore, bl)
	}
	wantEvent := "downloadIgnored"
	if blocklisted {
		wantEvent = "downloadFailed"
	}
	if count(hist, wantEvent) != count(histBefore, wantEvent)+1 {
		t.Errorf("%s: history did not record one %s: %v → %v", e.Key, wantEvent, histBefore, hist)
	}
	if count(hist, "grabbed") != count(histBefore, "grabbed") {
		t.Errorf("%s: a new grab of the same release followed the removal: %v → %v", e.Key, histBefore, hist)
	}
	// Replacement-search ownership.
	if redownload := v.flags.Get("skipRedownload") == "false"; redownload && len(searches) == 0 {
		t.Errorf("%s: skipRedownload=false queued no replacement search", e.Key)
	} else if !redownload && len(searches) != 0 {
		t.Errorf("%s: skipRedownload=true still queued searches: %+v", e.Key, searches)
	}
}
