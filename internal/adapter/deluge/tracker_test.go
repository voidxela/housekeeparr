package deluge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/domain"
)

func strPtr(s string) *string { return &s }

func TestSourceFailureClassification(t *testing.T) {
	pieces := int64(1024)
	for _, tc := range []struct {
		name          string
		state         string
		trackerStatus *string
		wantStatus    domain.SignalStatus
		wantValue     domain.SourceFailure
		wantNote      string
	}{
		{
			name:          "announce ok",
			state:         "Downloading",
			trackerStatus: strPtr("Announce OK"),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceNoFailure,
		},
		{
			name:          "announce sent",
			state:         "Downloading",
			trackerStatus: strPtr("Announce Sent"),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceNoFailure,
		},
		{
			name:          "empty status",
			state:         "Downloading",
			trackerStatus: strPtr(""),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceNoFailure,
		},
		{
			name:          "unregistered torrent",
			state:         "Downloading",
			trackerStatus: strPtr("Error: Unregistered torrent"),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceUnregistered,
		},
		{
			name:          "torrent not registered with this tracker",
			state:         "Downloading",
			trackerStatus: strPtr("Error: torrent not registered with this tracker"),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceUnregistered,
		},
		{
			name:          "trimmed whitespace before Error prefix",
			state:         "Downloading",
			trackerStatus: strPtr("  Error: Unregistered torrent  "),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceUnregistered,
		},
		{
			name:          "other tracker error",
			state:         "Downloading",
			trackerStatus: strPtr("Error: Passkey not found"),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceTrackerError,
		},
		{
			name:          "warning is not failure",
			state:         "Downloading",
			trackerStatus: strPtr("Warning: something"),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceNoFailure,
		},
		{
			name:          "case-sensitive Error prefix",
			state:         "Downloading",
			trackerStatus: strPtr("error: Unregistered torrent"),
			wantStatus:    domain.Known,
			wantValue:     domain.SourceNoFailure,
		},
		{
			name:          "absent tracker status",
			state:         "Downloading",
			trackerStatus: nil,
			wantStatus:    domain.Unknown,
			wantNote:      "tracker status not reported",
		},
		{
			name:          "local error hides tracker result",
			state:         "Error",
			trackerStatus: strPtr("Error: Unregistered torrent"),
			wantStatus:    domain.Unknown,
			wantNote:      "a local error hides any tracker result",
		},
		{
			name:          "local error hides absent tracker status",
			state:         "Error",
			trackerStatus: nil,
			wantStatus:    domain.Unknown,
			wantNote:      "a local error hides any tracker result",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := normalize("deluge", status{
				Hash:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Name:          "Example.Show.S01E01.1080p.WEB.h264-TEST",
				State:         tc.state,
				Message:       "OK",
				TrackerStatus: tc.trackerStatus,
				NumPieces:     &pieces,
			})
			if got.SourceFailure.Status != tc.wantStatus {
				t.Fatalf("SourceFailure.Status = %q, want %q (note=%q)", got.SourceFailure.Status, tc.wantStatus, got.SourceFailure.Note)
			}
			if tc.wantStatus == domain.Known {
				if v, ok := got.SourceFailure.Get(); !ok || v != tc.wantValue {
					t.Fatalf("SourceFailure = %+v, want known %q", got.SourceFailure, tc.wantValue)
				}
			} else if got.SourceFailure.Note != tc.wantNote {
				t.Fatalf("SourceFailure.Note = %q, want %q", got.SourceFailure.Note, tc.wantNote)
			}
			if msg, ok := got.ClientMessage.Get(); !ok || msg != "" {
				t.Fatalf("ClientMessage = %+v, want empty known string", got.ClientMessage)
			}
			if tc.trackerStatus != nil && *tc.trackerStatus != "" {
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), *tc.trackerStatus) {
					t.Fatalf("raw tracker_status leaked into Transfer JSON: %s", raw)
				}
			}
		})
	}
}

func TestListTransfersAbsentOrNullTrackerStatus(t *testing.T) {
	body := `{"result":{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":{"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Absent","state":"Downloading","message":"OK","num_pieces":100},
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb":{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"Null","state":"Downloading","message":"OK","tracker_status":null,"num_pieces":100}
	},"error":null,"id":3}`
	f := &fakeWeb{password: "pw", connected: true, status: []byte(body)}
	snap, err := newClient(t, f, "pw").ListTransfers(context.Background())
	if err != nil {
		t.Fatalf("ListTransfers failed: %v", err)
	}
	if len(snap.Transfers) != 2 {
		t.Fatalf("len(Transfers) = %d, want 2", len(snap.Transfers))
	}
	for _, tr := range snap.Transfers {
		if tr.SourceFailure.Status != domain.Unknown {
			t.Errorf("%s: SourceFailure = %+v, want unknown", tr.Name, tr.SourceFailure)
		}
	}
}
