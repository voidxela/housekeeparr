package arr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

func TestSourcesJoinRedactionAndValidation(t *testing.T) {
	indexers := `[
		{
			"id": 1,
			"name": "Failing Tracker",
			"protocol": "torrent",
			"enableAutomaticSearch": true,
			"baseUrl": "https://secret.example",
			"fields": [{"name": "passkey", "value": "https://tracker.example/announce?passkey=FAKEPASSKEY1234567890"}]
		},
		{
			"id": 2,
			"name": "Healthy Tracker",
			"protocol": "torrent",
			"enableAutomaticSearch": true,
			"fields": [{"name": "baseUrl", "value": "https://healthy.example"}]
		},
		{
			"id": 3,
			"name": "Usenet Source",
			"protocol": "usenet",
			"enableAutomaticSearch": true
		},
		{
			"id": 4,
			"name": "Disabled Auto Search",
			"protocol": "torrent",
			"enableAutomaticSearch": false
		}
	]`
	statuses := `[
		{
			"indexerId": 1,
			"disabledTill": "2099-01-01T07:00:00-05:00",
			"mostRecentFailure": "2026-11-17T05:00:00-05:00"
		},
		{
			"indexerId": 2,
			"disabledTill": null,
			"mostRecentFailure": null
		},
		{
			"indexerId": 999,
			"disabledTill": "2099-01-01T12:00:00Z",
			"mostRecentFailure": "2026-11-17T10:00:00Z"
		}
	]`

	snap, err := serve(t, map[string]string{
		"/api/v3/indexer":       indexers,
		"/api/v3/indexerstatus": statuses,
	}).Sources(context.Background(), "mgr-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.ManagerID != "mgr-1" || snap.CollectedAt.IsZero() || snap.CollectedAt.Location() != time.UTC {
		t.Fatalf("unexpected snapshot metadata: %+v", snap)
	}
	if len(snap.Sources) != 4 {
		t.Fatalf("got %d sources, want 4: %+v", len(snap.Sources), snap.Sources)
	}

	byID := map[int64]domain.Source{}
	for _, s := range snap.Sources {
		byID[s.ID] = s
	}
	if _, ok := byID[999]; ok {
		t.Fatal("unknown indexer status must be ignored")
	}

	failing := byID[1]
	wantDisabled := time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)
	wantFailure := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	if failing.Name != "Failing Tracker" || failing.Protocol != "torrent" || !failing.AutomaticSearch {
		t.Fatalf("failing source fields mismatch: %+v", failing)
	}
	if failing.DisabledUntil == nil || !failing.DisabledUntil.Equal(wantDisabled) || failing.DisabledUntil.Location() != time.UTC {
		t.Fatalf("DisabledUntil=%v, want %v UTC", failing.DisabledUntil, wantDisabled)
	}
	if failing.LastFailure == nil || !failing.LastFailure.Equal(wantFailure) || failing.LastFailure.Location() != time.UTC {
		t.Fatalf("LastFailure=%v, want %v UTC", failing.LastFailure, wantFailure)
	}

	healthy := byID[2]
	if healthy.DisabledUntil != nil || healthy.LastFailure != nil || !healthy.AutomaticSearch {
		t.Fatalf("healthy source mismatch: %+v", healthy)
	}

	usenet := byID[3]
	if usenet.Protocol != "usenet" || !usenet.AutomaticSearch || usenet.DisabledUntil != nil || usenet.LastFailure != nil {
		t.Fatalf("usenet source mismatch: %+v", usenet)
	}

	disabledAuto := byID[4]
	if disabledAuto.AutomaticSearch {
		t.Fatalf("disabled auto-search source mismatch: %+v", disabledAuto)
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "FAKEPASSKEY1234567890") || strings.Contains(string(raw), "http") {
		t.Fatalf("snapshot leaked fields/baseUrl: %s", raw)
	}
}

func TestSourcesNameSanitizedAndTruncated(t *testing.T) {
	long := strings.Repeat("a", 120)
	indexers := `[
		{"id": 1, "name": "Indexer passkey=SECRETVALUE", "protocol": "torrent", "enableAutomaticSearch": true},
		{"id": 2, "name": "` + long + `", "protocol": "torrent", "enableAutomaticSearch": true}
	]`
	snap, err := serve(t, map[string]string{
		"/api/v3/indexer":       indexers,
		"/api/v3/indexerstatus": `[]`,
	}).Sources(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap.Sources[0].Name, "SECRETVALUE") {
		t.Fatalf("name was not redacted: %q", snap.Sources[0].Name)
	}
	if got := []rune(snap.Sources[1].Name); len(got) != 101 || got[100] != '…' {
		t.Fatalf("name was not truncated to 100 runes + ellipsis: %q (len=%d)", snap.Sources[1].Name, len(got))
	}
}

func TestSourcesEmptyListsAreValid(t *testing.T) {
	snap, err := serve(t, map[string]string{
		"/api/v3/indexer":       `[]`,
		"/api/v3/indexerstatus": `[]`,
	}).Sources(context.Background(), "m")
	if err != nil || len(snap.Sources) != 0 {
		t.Fatalf("empty arrays should succeed: err=%v snap=%+v", err, snap)
	}
}

func TestSourcesMalformedResponses(t *testing.T) {
	validIndexers := `[{"id":1,"name":"Ok","protocol":"torrent","enableAutomaticSearch":true}]`
	validStatus := `[]`

	cases := map[string]map[string]string{
		"indexer null":                  {"/api/v3/indexer": `null`, "/api/v3/indexerstatus": validStatus},
		"indexer object":                {"/api/v3/indexer": `{}`, "/api/v3/indexerstatus": validStatus},
		"indexerstatus null":            {"/api/v3/indexer": validIndexers, "/api/v3/indexerstatus": `null`},
		"indexerstatus object":          {"/api/v3/indexer": validIndexers, "/api/v3/indexerstatus": `{}`},
		"missing id":                    {"/api/v3/indexer": `[{"name":"NoID","protocol":"torrent","enableAutomaticSearch":true}]`, "/api/v3/indexerstatus": validStatus},
		"zero id":                       {"/api/v3/indexer": `[{"id":0,"name":"ZeroID","protocol":"torrent","enableAutomaticSearch":true}]`, "/api/v3/indexerstatus": validStatus},
		"negative id":                   {"/api/v3/indexer": `[{"id":-1,"name":"NegID","protocol":"torrent","enableAutomaticSearch":true}]`, "/api/v3/indexerstatus": validStatus},
		"missing enableAutomaticSearch": {"/api/v3/indexer": `[{"id":1,"name":"NoAuto","protocol":"torrent"}]`, "/api/v3/indexerstatus": validStatus},
		"null enableAutomaticSearch":    {"/api/v3/indexer": `[{"id":1,"name":"NullAuto","protocol":"torrent","enableAutomaticSearch":null}]`, "/api/v3/indexerstatus": validStatus},
		"null element in indexer array": {"/api/v3/indexer": `[null]`, "/api/v3/indexerstatus": validStatus},
	}

	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := serve(t, pages).Sources(context.Background(), "m"); err == nil || adapter.ClassOf(err) != adapter.ErrPermanent {
				t.Fatalf("%s: expected malformed ErrPermanent, got %v", name, err)
			}
		})
	}
}
