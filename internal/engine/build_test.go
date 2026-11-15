package engine

import (
	"testing"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/config"
)

func TestBuildSelectsAdaptersAndNeverExposesSecrets(t *testing.T) {
	cfg, _, err := config.Parse([]byte(`version = 1
[[sonarr]]
id = "s"
url = "http://127.0.0.1:8989"
api_key = "env:S"
[[radarr]]
id = "r"
url = "http://127.0.0.1:7878"
api_key = "env:R"
[[qbittorrent]]
id = "q"
url = "http://127.0.0.1:8080"
[[transmission]]
id = "t"
url = "http://127.0.0.1:9091"
username = "u"
password = "env:T"
[[deluge]]
id = "d"
url = "http://127.0.0.1:8112"
password = "env:D"
[[rtorrent]]
id = "rt"
url = "http://127.0.0.1:8000"
[[transmission]]
id = "off"
url = "http://127.0.0.1:9092"
enabled = false
`), "/x/h.toml")
	if err != nil {
		t.Fatal(err)
	}
	conns, err := Build(cfg, map[string]string{"sonarr.s.api_key": "a", "radarr.r.api_key": "b", "transmission.t.password": "c", "deluge.d.password": "d"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]adapter.Kind{}
	for _, c := range conns.Clients {
		kinds[c.ID()] = c.Kind()
	}
	for _, m := range conns.Managers {
		kinds[m.ID()] = m.Kind()
	}
	want := map[string]adapter.Kind{"s": adapter.KindSonarr, "r": adapter.KindRadarr, "q": adapter.KindQBittorrent, "t": adapter.KindTransmission, "d": adapter.KindDeluge, "rt": adapter.KindRTorrent}
	if len(kinds) != len(want) {
		t.Fatalf("built %v", kinds)
	}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("%s: %s, want %s", id, kinds[id], k)
		}
	}
	if m := conns.Meta["off"]; m.Enabled || m.Kind != adapter.KindTransmission {
		t.Fatalf("disabled connection meta %+v", m)
	}
	if conns.Meta["t"].SecretRef != "env:T" {
		t.Fatalf("meta must carry the reference, not the value: %q", conns.Meta["t"].SecretRef)
	}
	if _, err := Build(cfg, map[string]string{}); err == nil {
		t.Fatal("unresolved secrets must fail the build")
	}
}
