package domain

import "testing"

func TestClassifyTrackerError(t *testing.T) {
	for msg, want := range map[string]SourceFailure{
		"Unregistered torrent":                                 SourceUnregistered,
		"Torrent not found":                                    SourceUnregistered,
		"blacklisted client, update or change client":          SourceClientRejected, // seen with Transmission 4.0.5
		"Your client is not allowed here (client not allowed)": SourceClientRejected,
		"Failure: Unsupported client version":                  SourceClientRejected,
		"Banned client detected":                               SourceClientRejected,
		// The release itself is gone: that wins over a client complaint.
		"Unregistered torrent (unsupported client)": SourceUnregistered,
		"Passkey not found":                         SourceTrackerError,
		"client error":                              SourceTrackerError, // names no refusal
		"Connection refused":                        SourceTrackerError,
	} {
		if got := ClassifyTrackerError(msg); got != want {
			t.Errorf("%q: %s, want %s", msg, got, want)
		}
	}
}
