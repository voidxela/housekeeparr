package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/policy"
)

func writeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return exitFailure
	}
	return exitOK
}

func tw(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0) }

func ts(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func printCycle(w io.Writer, rep engine.CycleReport) {
	if rep.StoreError != "" {
		fmt.Fprintf(w, "Cycle not persisted: %s\n", rep.StoreError)
		return
	}
	t := tw(w)
	fmt.Fprintln(t, "CONNECTION\tKIND\tRESULT")
	for _, c := range rep.Connections {
		res := "ok"
		switch {
		case c.Skipped != "":
			res = "skipped: " + c.Skipped
		case !c.OK:
			res = fmt.Sprintf("failed (%s): %s", c.ErrorClass, c.Error)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\n", c.ID, c.Kind, res)
	}
	t.Flush()
	fmt.Fprintf(w, "Findings: %d opened, %d recurred, %d resolved.\n", rep.Opened, rep.Recurred, rep.Resolved)
}

func printOverview(w io.Writer, o *app.Overview) {
	if o.Origin == "demo" {
		fmt.Fprintln(w, "*** DEMO DATA — synthetic, not observations of your services ***")
	}
	fmt.Fprintf(w, "Mode:     %s (configuration revision %s)\n", o.Mode, o.ConfigRevision)
	fmt.Fprintf(w, "Worker:   %s — %s\n", o.Worker.State, o.Worker.Explanation)
	if len(o.Automation) > 0 {
		var parts []string
		for _, r := range []string{"dispatched", "deferred", "held", "refused", "needs_review", "at_ceiling", "waiting", "not_permitted", "ineligible"} {
			if n := o.Automation[r]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, r))
			}
		}
		fmt.Fprintf(w, "Automation: latest decisions: %s (see each finding)\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "Findings: %d open, %d acknowledged, %d snoozed (errors %d, warnings %d, notices %d)\n",
		o.Counts.Open, o.Counts.Acknowledged, o.Counts.Snoozed, o.Counts.BySeverity["error"], o.Counts.BySeverity["warning"], o.Counts.BySeverity["notice"])
	fmt.Fprintf(w, "Downloads: %d incomplete, %d accumulating evidence, %d diagnosed\n", o.Transfers.Incomplete, o.Transfers.Observing, o.Transfers.Diagnosed)
	fmt.Fprintln(w)
	printConnections(w, o.Connections)
	for _, warn := range o.Warnings {
		fmt.Fprintln(w, "\nWARNING:", warn)
	}
	fmt.Fprintln(w, "\nLimitations of this release:")
	for _, l := range o.Capabilities {
		fmt.Fprintln(w, "  -", l)
	}
}

func printConnections(w io.Writer, cs []app.ConnectionView) {
	t := tw(w)
	fmt.Fprintln(t, "CONNECTION\tKIND\tSTATUS\tFRESHNESS\tVERSION\tDETAIL")
	for _, c := range cs {
		ver := "—"
		if c.Probe != nil {
			ver = fmt.Sprintf("%s %s (%s)", c.Probe.App, c.Probe.Version, c.Probe.Compatibility)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Kind, c.Status, c.Freshness, ver, c.Explanation)
	}
	t.Flush()
}

func printFindings(w io.Writer, fs []app.FindingView) {
	if len(fs) == 0 {
		fmt.Fprintln(w, "No findings.")
		return
	}
	t := tw(w)
	fmt.Fprintln(t, "ID\tSEVERITY\tSTATE\tEVIDENCE\tTITLE")
	for _, f := range fs {
		ev := f.EvidenceAge + " ago"
		if f.Freshness == app.FreshStale && f.State != "resolved" {
			ev += " (stale)"
		}
		fmt.Fprintf(t, "%d\t%s\t%s\t%s\t%s\n", f.ID, f.Severity, f.State, ev, f.Title)
	}
	t.Flush()
}

func printFinding(w io.Writer, d *app.FindingDetail) {
	f := d.Finding
	fmt.Fprintf(w, "#%d  %s  [%s, %s]\n", f.ID, f.Title, f.Severity, f.State)
	fmt.Fprintf(w, "\n%s\n", f.Summary)
	if f.Detail.CurrentState != "" {
		fmt.Fprintf(w, "\nSince diagnosis: %s\n", f.Detail.CurrentState)
	}
	fmt.Fprintf(w, "\nFirst seen %s · last observed %s · evidence %s ago", ts(&f.FirstSeen), ts(&f.LastObserved), d.EvidenceAge)
	if d.Freshness == app.FreshStale && f.State != "resolved" {
		fmt.Fprint(w, " (STALE)")
	}
	fmt.Fprintln(w)
	if f.Occurrence > 1 {
		fmt.Fprintf(w, "Occurrence %d (previous: #%d)\n", f.Occurrence, *f.PreviousID)
	}
	if f.ResolvedAt != nil {
		fmt.Fprintf(w, "Resolved %s: %s\n", ts(f.ResolvedAt), f.Resolution)
	}
	if a := d.Automation; a != nil {
		fmt.Fprintf(w, "\nAutomation (evaluated %s): %s — %s", ts(&a.EvaluatedAt), a.Result, a.Detail)
		if a.NextAt != nil {
			fmt.Fprintf(w, " (next eligible %s)", ts(a.NextAt))
		}
		fmt.Fprintln(w)
	}
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s:\n", title)
		for _, it := range items {
			fmt.Fprintf(w, "  - %s\n", it)
		}
	}
	var ev []string
	for _, e := range f.Detail.Evidence {
		ev = append(ev, e.Label+": "+e.Value)
	}
	section("Evidence", ev)
	section("Missing information", f.Detail.Missing)
	if f.Detail.Confidence != "" {
		section("Confidence", []string{f.Detail.Confidence})
	}
	section("Limitations", f.Detail.Limitations)
	if f.Detail.Eligibility != "" {
		section("What Housekeeparr will do", []string{f.Detail.Eligibility})
	}
	section("Next steps", f.Detail.NextSteps)
	var links []string
	for _, l := range f.Detail.Links {
		links = append(links, l.Label+": "+l.URL)
	}
	section("Links", links)
	if f.PolicyID != "" {
		fmt.Fprintf(w, "\nPolicy %s, revision %s\n", f.PolicyID, f.PolicyRevision)
	}
	fmt.Fprintln(w, "\nHistory:")
	for _, e := range d.Events {
		fmt.Fprintf(w, "  %s  %-14s %-6s %s\n", ts(&e.At), e.Kind, e.Actor, e.Detail)
	}
}

func printDownloads(w io.Writer, d *app.Downloads) {
	if len(d.Transfers) == 0 {
		fmt.Fprintln(w, "No incomplete downloads observed.")
	} else {
		t := tw(w)
		fmt.Fprintln(t, "CLIENT\tSTATE\tPROGRESS\tASSESSMENT\tNAME")
		for _, v := range d.Transfers {
			prog := "?"
			if v.Progress != nil {
				prog = fmt.Sprintf("%.1f%%", *v.Progress*100)
			}
			as := "—"
			if v.Transfer.Assessment != nil {
				as = string(v.Transfer.Assessment.Status)
				if v.Transfer.Assessment.QualifiesAt != nil {
					as += " (would qualify ~" + v.Transfer.Assessment.QualifiesAt.Local().Format("15:04") + ")"
				}
			}
			if v.Freshness == app.FreshStale {
				as += " [stale]"
			}
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", v.Transfer.ClientID, v.Transfer.Latest.NativeState, prog, as, v.Transfer.Name)
		}
		t.Flush()
	}
	if len(d.Unmatched) > 0 {
		fmt.Fprintln(w, "\nQueue rows without a correlated transfer:")
		t := tw(w)
		fmt.Fprintln(t, "MANAGER\tCORRELATION\tTITLE\tNOTE")
		for _, q := range d.Unmatched {
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", q.ManagerID, q.MatchStatus, q.Latest.Title, q.MatchNote)
		}
		t.Flush()
	}
}

func printPolicies(w io.Writer, ps []policy.Effective) {
	for i, p := range ps {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s (revision %s)\n", p.ID, p.Revision)
		t := tw(w)
		for _, s := range p.Sources {
			fmt.Fprintf(t, "  %s\t%s\t%s\n", s.Setting, s.Value, s.From)
		}
		t.Flush()
	}
}

func printActivity(w io.Writer, a *app.Activity) {
	fmt.Fprintln(w, a.Note)
	fmt.Fprintln(w, "\nFinding events:")
	t := tw(w)
	for _, e := range a.Events {
		fmt.Fprintf(t, "  %s\t#%d\t%s\t%s\t%s\n", ts(&e.At), e.FindingID, e.Kind, e.Actor, e.Detail)
	}
	t.Flush()
	fmt.Fprintln(w, "\nAudit:")
	t = tw(w)
	for _, e := range a.Audit {
		fmt.Fprintf(t, "  %s\t%s\t%s\t%s\n", ts(&e.At), e.Kind, e.Actor, e.Detail)
	}
	t.Flush()
}

func cmdCheck(args []string, out, errOut io.Writer) int {
	o := newFlags("check", errOut)
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	cfg, code := o.loadConfig()
	if cfg == nil {
		return code
	}
	secrets, probs := cfg.ResolvedSecrets(nil)
	for _, p := range probs {
		fmt.Fprintln(errOut, p.String())
		if p.Severity == "error" {
			return exitUsage
		}
	}
	conns, err := engine.Build(cfg, secrets)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	res := engine.Check(context.Background(), conns, cfg.Polling.RequestTimeout.Duration, cfg.Polling.MaxConcurrency)
	failed := false
	for _, r := range res {
		failed = failed || !r.OK
	}
	if o.json {
		writeJSON(out, res)
	} else {
		for i, r := range res {
			if i > 0 {
				fmt.Fprintln(out)
			}
			status := "OK"
			if !r.OK {
				status = fmt.Sprintf("FAILED at %s (%s): %s", r.Step, r.ErrorClass, r.Error)
			}
			fmt.Fprintf(out, "%s (%s) %s — %s [%s]\n", r.Name, r.Kind, r.URL, status, r.Duration.Round(time.Millisecond))
			if r.Probe != nil {
				p := r.Probe
				fmt.Fprintf(out, "  %s %s, %s; auth: %s\n", p.App, p.Version, p.Transport, p.Auth)
				fmt.Fprintf(out, "  compatibility: %s — %s\n", p.Compatibility, p.CompatNote)
				printCaps(out, "read", p.Read)
				printCaps(out, "write", p.Write)
			}
			if r.Summary != "" {
				fmt.Fprintf(out, "  read: %s\n", r.Summary)
			}
		}
	}
	if failed {
		return exitProblem
	}
	return exitOK
}

func printCaps(w io.Writer, label string, caps adapter.Capabilities) {
	if len(caps) == 0 {
		return
	}
	var keys []string
	for k := range caps {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, caps[adapter.Capability(k)].Support))
	}
	fmt.Fprintf(w, "  %s capabilities: %s\n", label, strings.Join(parts, ", "))
}
