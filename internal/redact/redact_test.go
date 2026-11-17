package redact

import (
	"strings"
	"testing"
)

func TestURLStripsCredentialsAndQuery(t *testing.T) {
	got := URL("https://user:hunter2@tracker.example/announce/0123456789abcdef0123456789abcdef?passkey=zzz#frag")
	for _, bad := range []string{"hunter2", "user", "zzz", "0123456789abcdef0123456789abcdef", "frag"} {
		if strings.Contains(got, bad) {
			t.Fatalf("URL() leaked %q: %s", bad, got)
		}
	}
	if !strings.HasPrefix(got, "https://tracker.example/announce/") {
		t.Fatalf("unexpected URL form: %s", got)
	}
}

func TestStringRedactsCommonSecrets(t *testing.T) {
	cases := []string{
		"GET http://host/api?apikey=SECRETVALUE failed",
		"X-Api-Key: SECRETVALUE",
		"Cookie: SID=SECRETVALUE",
		"announce http://u:SECRETVALUE@tracker/announce",
		"tracker https://t.example/SECRETVALUE0000000000000000000/announce",
		"passkey=SECRETVALUE&x=1",
	}
	for _, c := range cases {
		if got := String(c); strings.Contains(got, "SECRETVALUE") {
			t.Errorf("String(%q) = %q, still contains secret", c, got)
		}
	}
}

func TestStringKeepsOrdinaryText(t *testing.T) {
	in := "Unable to connect to qBittorrent: connection refused"
	if got := String(in); got != in {
		t.Fatalf("ordinary text modified: %q", got)
	}
}

func TestRedactAdjacentPasskeysAndURLFragment(t *testing.T) {
	in := "https://tracker.example/SECRETVALUE000000000000000001/SECRETVALUE000000000000000002/announce#SECRETVALUE"
	if got := String(in); strings.Contains(got, "SECRETVALUE") {
		t.Fatalf("unredacted tracker URL: %s", got)
	}
	if got := URL(in); strings.Contains(got, "SECRETVALUE") {
		t.Fatalf("unredacted display URL: %s", got)
	}
}
