package qbittorrent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/domain"
)

func TestSourceFailureClassification(t *testing.T) {
	const trackerURL = "https://tracker.example/announce?passkey=" + fakePasskey
	const rawUnregMsg = "Unregistered torrent (custom tracker note)"
	const rawOtherMsg = "Connection timed out from secret-host"

	torrentsJSON := `[
		{"hash":"0000000000000000000000000000000000000001","name":"working","state":"stalledDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":1,"num_incomplete":0,"availability":0.5,"private":true},
		{"hash":"0000000000000000000000000000000000000002","name":"unregistered","state":"downloading","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":10,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.5,"private":true},
		{"hash":"0000000000000000000000000000000000000003","name":"mixed-errors","state":"forcedDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.5,"private":false},
		{"hash":"0000000000000000000000000000000000000004","name":"both-unregistered","state":"stalledDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.5,"private":false},
		{"hash":"0000000000000000000000000000000000000005","name":"working-plus-error","state":"stalledDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.5,"private":false},
		{"hash":"0000000000000000000000000000000000000006","name":"pseudo-only","state":"stalledDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.5,"private":false},
		{"hash":"0000000000000000000000000000000000000007","name":"not-contacted","state":"stalledDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.5,"private":true},
		{"hash":"0000000000000000000000000000000000000008","name":"status4-empty-msg","state":"stalledDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.5,"private":true},
		{"hash":"0000000000000000000000000000000000000009","name":"paused","state":"stoppedDL","size":1000,"total_size":1000,"completed":500,"amount_left":500,"downloaded":500,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":-1,"private":true},
		{"hash":"000000000000000000000000000000000000000a","name":"complete","state":"stalledUP","size":1000,"total_size":1000,"completed":1000,"amount_left":0,"downloaded":1000,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":-1,"private":true},
		{"hash":"000000000000000000000000000000000000000b","name":"queued","state":"queuedDL","size":1000,"total_size":1000,"completed":0,"amount_left":1000,"downloaded":0,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0,"private":true},
		{"hash":"000000000000000000000000000000000000000c","name":"meta","state":"metaDL","size":0,"total_size":0,"completed":0,"amount_left":0,"downloaded":0,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":-1,"num_incomplete":-1,"availability":0,"has_metadata":false,"private":null}
	]`

	trackers := map[string][]byte{
		"0000000000000000000000000000000000000001": []byte(fmt.Sprintf(`[
			{"url":"** [DHT] **","status":0,"msg":""},
			{"url":%q,"status":2,"msg":""}
		]`, trackerURL)),
		"0000000000000000000000000000000000000002": []byte(fmt.Sprintf(`[
			{"url":"** [DHT] **","status":0,"msg":""},
			{"url":%q,"status":4,"msg":%q}
		]`, trackerURL, rawUnregMsg)),
		"0000000000000000000000000000000000000003": []byte(fmt.Sprintf(`[
			{"url":%q,"status":4,"msg":%q},
			{"url":"https://tracker2.example/announce?passkey=%s","status":4,"msg":%q}
		]`, trackerURL, rawUnregMsg, fakePasskey, rawOtherMsg)),
		"0000000000000000000000000000000000000004": []byte(fmt.Sprintf(`[
			{"url":%q,"status":4,"msg":%q},
			{"url":"https://tracker2.example/announce?passkey=%s","status":4,"msg":"Torrent not found"}
		]`, trackerURL, rawUnregMsg, fakePasskey)),
		"0000000000000000000000000000000000000005": []byte(fmt.Sprintf(`[
			{"url":%q,"status":4,"msg":%q},
			{"url":"https://tracker2.example/announce?passkey=%s","status":2,"msg":""}
		]`, trackerURL, rawUnregMsg, fakePasskey)),
		"0000000000000000000000000000000000000006": []byte(`[
			{"url":"** [DHT] **","status":4,"msg":"Unregistered torrent"},
			{"url":"** [PeX] **","status":0,"msg":""},
			{"url":"** [LSD] **","status":0,"msg":""}
		]`),
		"0000000000000000000000000000000000000007": []byte(fmt.Sprintf(`[
			{"url":"** [DHT] **","status":0,"msg":""},
			{"url":%q,"status":1,"msg":""},
			{"url":"https://tracker2.example/announce?passkey=%s","status":3,"msg":""}
		]`, trackerURL, fakePasskey)),
		"0000000000000000000000000000000000000008": []byte(fmt.Sprintf(`[
			{"url":%q,"status":4,"msg":""},
			{"url":"https://tracker2.example/announce?passkey=%s","status":0,"msg":""}
		]`, trackerURL, fakePasskey)),
		// Should never be queried for non-downloading transfers.
		"0000000000000000000000000000000000000009": []byte(fmt.Sprintf(`[{"url":%q,"status":4,"msg":%q}]`, trackerURL, rawUnregMsg)),
	}

	f := &fakeQBit{
		version:  "v5.3.0",
		webapi:   "2.11.4",
		torrents: []byte(torrentsJSON),
		trackers: trackers,
		user:     "admin",
		pass:     "pw",
	}
	c := setup(t, f)
	snap, err := c.ListTransfers(context.Background())
	if err != nil {
		t.Fatalf("ListTransfers: %v", err)
	}
	if !snap.Complete || len(snap.Transfers) != 12 {
		t.Fatalf("complete=%v len=%d", snap.Complete, len(snap.Transfers))
	}

	// Only the 8 downloading transfers should have been queried.
	if got := f.trackerReqs.Load(); got != 8 {
		t.Fatalf("trackerReqs = %d, want 8 (non-downloading transfers must not be queried)", got)
	}
	for _, h := range f.trackerHashes {
		if strings.HasSuffix(h, "09") || strings.HasSuffix(h, "0a") || strings.HasSuffix(h, "0b") || strings.HasSuffix(h, "0c") {
			t.Errorf("unexpected /torrents/trackers request for non-downloading hash %s", h)
		}
	}

	m := byName(snap)
	wantKnown := map[string]domain.SourceFailure{
		"working":            domain.SourceNoFailure,
		"unregistered":       domain.SourceUnregistered,
		"mixed-errors":       domain.SourceTrackerError,
		"both-unregistered":  domain.SourceUnregistered,
		"working-plus-error": domain.SourceNoFailure,
		"pseudo-only":        domain.SourceNoFailure,
	}
	for name, want := range wantKnown {
		got, ok := m[name].SourceFailure.Get()
		if !ok || got != want {
			t.Errorf("%s: SourceFailure = %+v, want known %q", name, m[name].SourceFailure, want)
		}
	}

	for _, name := range []string{"not-contacted", "status4-empty-msg"} {
		sf := m[name].SourceFailure
		if sf.Status != domain.Unknown || sf.Note != "no tracker has answered yet" {
			t.Errorf("%s: SourceFailure = %+v, want unknown(\"no tracker has answered yet\")", name, sf)
		}
	}

	for _, name := range []string{"paused", "complete", "queued", "meta"} {
		sf := m[name].SourceFailure
		if sf.Status != domain.Unknown || sf.Note != "tracker results are read only for active downloads" {
			t.Errorf("%s: SourceFailure = %+v, want unknown(\"tracker results are read only for active downloads\")", name, sf)
		}
	}

	// Neither tracker URLs, passkeys, nor raw tracker messages may appear in the snapshot.
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{fakePasskey, "tracker.example", "tracker2.example", rawUnregMsg, rawOtherMsg, "secret-host"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("marshalled snapshot leaked %q", secret)
		}
	}
}

func TestTrackerReadsBoundedPerListing(t *testing.T) {
	const total = 30
	var items []string
	trackers := map[string][]byte{}
	for i := 0; i < total; i++ {
		h := fmt.Sprintf("%040x", i+1)
		items = append(items, fmt.Sprintf(`{"hash":%q,"name":"dl-%02d","state":"stalledDL","size":1000,"total_size":1000,"completed":100,"amount_left":900,"downloaded":100,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.1,"private":true}`, h, i))
		trackers[h] = []byte(`[{"url":"https://tracker.example/announce?passkey=` + fakePasskey + `","status":4,"msg":"Unregistered torrent"}]`)
	}
	f := &fakeQBit{
		version:  "v5.3.0",
		webapi:   "2.11.4",
		torrents: []byte("[" + strings.Join(items, ",") + "]"),
		trackers: trackers,
		user:     "admin",
		pass:     "pw",
	}
	c := setup(t, f)
	snap, err := c.ListTransfers(context.Background())
	if err != nil {
		t.Fatalf("ListTransfers: %v", err)
	}
	if !snap.Complete || len(snap.Transfers) != total {
		t.Fatalf("complete=%v len=%d", snap.Complete, len(snap.Transfers))
	}
	if snap.TrackerReadLimit != maxTrackerReads || snap.TrackerReadsDeferred != total-maxTrackerReads {
		t.Fatalf("coverage summary limit=%d deferred=%d", snap.TrackerReadLimit, snap.TrackerReadsDeferred)
	}
	if got := f.trackerReqs.Load(); got != maxTrackerReads {
		t.Fatalf("trackerReqs = %d, want %d", got, maxTrackerReads)
	}
	for i, tr := range snap.Transfers {
		if i < maxTrackerReads {
			if v, ok := tr.SourceFailure.Get(); !ok || v != domain.SourceUnregistered {
				t.Errorf("transfer %d: got %+v, want known unregistered", i, tr.SourceFailure)
			}
		} else {
			if tr.SourceFailure.Status != domain.Unknown || tr.SourceFailure.Note != "tracker results not read this cycle (bounded to 25 per listing)" {
				t.Errorf("transfer %d: got %+v, want unknown bounded note", i, tr.SourceFailure)
			}
		}
	}
}

func TestTrackerEndpointFailuresDoNotFailListing(t *testing.T) {
	const h500 = "0000000000000000000000000000000000000001"
	const hMalformed = "0000000000000000000000000000000000000002"
	const hNull = "0000000000000000000000000000000000000003"
	const hOK = "0000000000000000000000000000000000000004"

	torrentsJSON := fmt.Sprintf(`[
		{"hash":%q,"name":"http-500","state":"stalledDL","size":1000,"total_size":1000,"completed":100,"amount_left":900,"downloaded":100,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.1,"private":true},
		{"hash":%q,"name":"malformed-json","state":"stalledDL","size":1000,"total_size":1000,"completed":100,"amount_left":900,"downloaded":100,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.1,"private":true},
		{"hash":%q,"name":"null-json","state":"stalledDL","size":1000,"total_size":1000,"completed":100,"amount_left":900,"downloaded":100,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.1,"private":true},
		{"hash":%q,"name":"healthy","state":"stalledDL","size":1000,"total_size":1000,"completed":100,"amount_left":900,"downloaded":100,"dlspeed":0,"num_seeds":0,"num_leechs":0,"num_complete":0,"num_incomplete":0,"availability":0.1,"private":true}
	]`, h500, hMalformed, hNull, hOK)

	f := &fakeQBit{
		version:  "v5.3.0",
		webapi:   "2.11.4",
		torrents: []byte(torrentsJSON),
		trackerStatus: map[string]int{
			h500: http.StatusInternalServerError,
		},
		trackers: map[string][]byte{
			hMalformed: []byte(`{not json`),
			hNull:      []byte(`null`),
			hOK:        []byte(`[{"url":"https://tracker.example/announce?passkey=` + fakePasskey + `","status":2,"msg":""}]`),
		},
		user: "admin",
		pass: "pw",
	}
	c := setup(t, f)
	snap, err := c.ListTransfers(context.Background())
	if err != nil {
		t.Fatalf("ListTransfers must succeed when /torrents/trackers fails: %v", err)
	}
	if !snap.Complete || len(snap.Transfers) != 4 {
		t.Fatalf("snapshot must remain complete: complete=%v len=%d", snap.Complete, len(snap.Transfers))
	}

	m := byName(snap)
	if sf := m["http-500"].SourceFailure; sf.Status != domain.Unknown || sf.Note != "tracker list could not be read: transient" {
		t.Errorf("http-500: SourceFailure = %+v", sf)
	}
	if sf := m["malformed-json"].SourceFailure; sf.Status != domain.Unknown || sf.Note != "tracker list could not be read: permanent" {
		t.Errorf("malformed-json: SourceFailure = %+v", sf)
	}
	if sf := m["null-json"].SourceFailure; sf.Status != domain.Unknown || sf.Note != "tracker list could not be read: permanent" {
		t.Errorf("null-json: SourceFailure = %+v", sf)
	}
	if v, ok := m["healthy"].SourceFailure.Get(); !ok || v != domain.SourceNoFailure {
		t.Errorf("healthy: SourceFailure = %+v, want known none", m["healthy"].SourceFailure)
	}

	raw, _ := json.Marshal(snap)
	if strings.Contains(string(raw), fakePasskey) || strings.Contains(string(raw), "tracker.example") {
		t.Fatalf("marshalled snapshot leaked tracker URL or passkey")
	}
}

// qBittorrent 5 reports a tracker's failure answer as status 5 ("tracker
// error") and an unreachable tracker as 6, which is never a rejection.
func TestTrackerStatusesOf5x(t *testing.T) {
	for _, tc := range []struct {
		entries []trackerEntry
		want    domain.SourceFailure
	}{
		{[]trackerEntry{{URL: "https://t/a", Status: 5, Msg: "Unregistered torrent"}}, domain.SourceUnregistered},
		{[]trackerEntry{{URL: "https://t/a", Status: 5, Msg: "Passkey not found"}}, domain.SourceTrackerError},
		{[]trackerEntry{{URL: "https://t/a", Status: 6, Msg: "Connection refused"}}, domain.SourceTrackerError},
		{[]trackerEntry{{URL: "https://t/a", Status: 5, Msg: "Unregistered torrent"}, {URL: "https://u/a", Status: 6}}, domain.SourceTrackerError},
	} {
		if v, ok := classifyTrackers(tc.entries).Get(); !ok || v != tc.want {
			t.Errorf("%+v: got %v (known %v)", tc.entries, v, ok)
		}
	}
}

func TestTrackerRejectionWithUncontactedTrackerIsUnknown(t *testing.T) {
	for _, status := range []int{1, 3, 99} {
		got := classifyTrackers([]trackerEntry{
			{URL: "https://t/a", Status: 5, Msg: "Unregistered torrent"},
			{URL: "https://u/a", Status: status},
		})
		if got.IsKnown() {
			t.Errorf("tracker status %d was turned into a definitive result: %+v", status, got)
		}
	}
}

func TestMalformedTrackerEvidenceCannotQualifyRejection(t *testing.T) {
	const h = "0000000000000000000000000000000000000001"
	for name, body := range map[string]string{
		"missing tracker identity": `[{"status":5,"msg":"Unregistered torrent"}]`,
		"trailing value":           `[{"url":"https://tracker.example/announce","status":5,"msg":"Unregistered torrent"}] {}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := setup(t, &fakeQBit{
				version: "v5.3.0", webapi: "2.11.4", user: "admin", pass: "pw",
				trackers: map[string][]byte{h: []byte(body)},
			})
			if got := c.readSourceFailure(context.Background(), h); got.IsKnown() {
				t.Fatalf("malformed tracker payload was trusted: %+v", got)
			}
		})
	}
}
