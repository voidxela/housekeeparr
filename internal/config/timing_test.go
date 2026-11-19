package config

import (
	"strings"
	"testing"
	"time"
)

func TestWebListenerTimeouts(t *testing.T) {
	cfg := mustParse(t, minimal)
	w := cfg.Web
	if w.ReadHeaderTimeout.Duration != 5*time.Second || w.ReadTimeout.Duration != 15*time.Second || w.WriteTimeout.Duration != 2*time.Minute || w.IdleTimeout.Duration != time.Minute {
		t.Fatalf("defaults %+v", w)
	}
	cfg = mustParse(t, minimal+"\n[web]\nwrite_timeout = \"5m\"\n")
	if cfg.Web.WriteTimeout.Duration != 5*time.Minute {
		t.Fatalf("write_timeout %v", cfg.Web.WriteTimeout)
	}
	// Shorter than a manual action can take with the default settle.
	expectProblem(t, minimal+"\n[web]\nwrite_timeout = \"30s\"\n", "web.write_timeout", "action_timing.settle")
	expectProblem(t, minimal+"\n[web]\nidle_timeout = \"0s\"\n", "web.idle_timeout", "between")
}

func TestCorrelatedProbeInterval(t *testing.T) {
	if l := mustParse(t, minimal).Limits(); l.CorrelatedProbeInterval != time.Hour {
		t.Fatalf("default %v", l.CorrelatedProbeInterval)
	}
	if l := mustParse(t, minimal+"\n[automation.limits]\ncorrelated_probe_interval = \"0s\"\n").Limits(); l.CorrelatedProbeInterval != 0 || !strings.Contains(l.From("correlated_probe_interval"), "config") {
		t.Fatalf("disabled %+v", l)
	}
	expectProblem(t, minimal+"\n[automation.limits]\ncorrelated_probe_interval = \"5m\"\n", "automation.limits.correlated_probe_interval", "10m")
}

func TestOutcomeWindowSettings(t *testing.T) {
	a := mustParse(t, minimal).ActionTiming
	if a.CommandFollow.Duration != time.Hour || a.CommandClockTolerance.Duration != time.Minute || a.NoGrabAfter.Duration != 24*time.Hour || a.OutcomeWindow.Duration != 7*24*time.Hour {
		t.Fatalf("defaults %+v", a)
	}
	expectProblem(t, minimal+"\n[action_timing]\nno_grab_after = \"200h\"\n", "action_timing.no_grab_after", "outcome_window")
	expectProblem(t, minimal+"\n[action_timing]\ncommand_follow = \"1m\"\n", "action_timing.command_follow", "between")
}
