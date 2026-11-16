package config

import "testing"

// Diagnosis resolves each transfer's own policy every cycle, so a rule or
// override that would make the resolved settings invalid must be rejected
// when the configuration is loaded, not discovered by a failing cycle.
func TestInvalidScopedSettingsRejectedAtLoad(t *testing.T) {
	base := "version = 1\n[[qbittorrent]]\nid = \"qbit\"\nurl = \"http://127.0.0.1:8080\"\n"
	for name, extra := range map[string]string{
		"override min_observations": "[[override]]\ntransfer = \"qbit|aa\"\nreason = \"x\"\n[override.policy]\nmin_observations = 1\n",
		"override negative grace":   "[[override]]\ntransfer = \"qbit|aa\"\nreason = \"x\"\n[override.policy]\nnew_download_grace = \"-1m\"\n",
		"rule zero window":          "[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\nclient = \"qbit\"\n[rule.policy]\nno_progress_window = \"0s\"\n",
		"override gap below poll":   "[[override]]\ntransfer = \"qbit|aa\"\nreason = \"x\"\n[override.policy]\nmax_observation_gap = \"1s\"\n",
	} {
		if _, _, err := Parse([]byte(base+extra), "/x/h.toml"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
