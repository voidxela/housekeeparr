package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

// writerService opens the state store for writing (refusing with exit 3
// while another process owns it).
func writerService(o *opts) (*app.Service, func(), int) {
	cfg, code := o.loadConfig()
	if cfg == nil {
		return nil, nil, code
	}
	st, code := openWriter(context.Background(), cfg, o.errOut)
	if st == nil {
		return nil, nil, code
	}
	return app.New(st, cfg, nil, nil), func() { st.Close() }, exitOK
}

func cmdPreview(args []string, out, errOut io.Writer) int {
	o := newFlags("preview", errOut)
	auto := o.fs.Bool("automatic", false, "evaluate as automation would (default: as a manual action)")
	pos, err := o.parse(args)
	if err != nil {
		return exitUsage
	}
	id, ok := parseID(pos, errOut)
	if !ok {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	origin := action.OriginManual
	if *auto {
		origin = action.OriginAutomatic
	}
	pv, err := svc.Preview(context.Background(), id, origin)
	if errors.Is(err, app.ErrNotFound) {
		fmt.Fprintf(errOut, "finding %d not found\n", id)
		return exitProblem
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, pv)
	}
	printPreview(out, pv)
	return exitOK
}

func printPreview(out io.Writer, pv *app.ActionPreview) {
	if pv.Proposal == nil {
		fmt.Fprintf(out, "Finding %d: no action. %s\n", pv.FindingID, pv.NoAction)
		return
	}
	p := pv.Proposal
	verdict := "NOT ELIGIBLE"
	if p.Eligible() {
		verdict = "eligible"
	}
	fmt.Fprintf(out, "Finding %d → %s (%s): %s\n", pv.FindingID, p.Workflow, p.Origin, verdict)
	fmt.Fprintf(out, "Policy %s (revision %s), mode %s; preview valid until %s\n\n", pv.Policy.ID, p.PolicyRevision, nonEmptyStr(pv.Policy.Mode, "observe"),
		p.ExpiresAt.UTC().Format("2006-01-02 15:04:05Z"))
	fmt.Fprintln(out, "Prerequisites:")
	for _, c := range p.Prerequisites {
		mark := "ok  "
		if !c.Satisfied {
			mark = "FAIL"
		}
		kind := "policy"
		if c.Hard {
			kind = "safety"
		}
		fmt.Fprintf(out, "  [%s] %-34s (%s) %s\n", mark, c.Name, kind, c.Detail)
	}
	for _, c := range p.Planned.Content {
		fmt.Fprintf(out, "\nAffected content (for overrides and exclusions): %s|%s\n", p.Planned.ManagerID, c)
	}
	if len(p.LiveChecks) > 0 {
		fmt.Fprintln(out, "\nRe-read from the applications immediately before each write (any change holds the action):")
		for _, c := range p.LiveChecks {
			fmt.Fprintln(out, "  - "+c)
		}
	}
	fmt.Fprintln(out, "\nIntended operations (every flag explicit):")
	for i, op := range p.Operations {
		fmt.Fprintf(out, "  %d. %s\n     %s %s\n     expect %s\n", i+1, op.Description, op.Op, op.Request, op.Expect)
	}
	fmt.Fprintln(out, "\nEffects:")
	for _, e := range p.Effects {
		fmt.Fprintln(out, "  - "+e)
	}
	for _, e := range p.SideEffects {
		fmt.Fprintln(out, "  - "+e)
	}
	if !p.Eligible() {
		fmt.Fprintln(out, "\nNothing will be sent while any prerequisite fails.")
	}
}

func nonEmptyStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func cmdAct(args []string, out, errOut io.Writer) int {
	o := newFlags("act", errOut)
	confirm := o.fs.Bool("confirm", false, "confirm the previewed action")
	pos, err := o.parse(args)
	if err != nil {
		return exitUsage
	}
	id, ok := parseID(pos, errOut)
	if !ok {
		return exitUsage
	}
	if !*confirm {
		fmt.Fprintln(errOut, "act requires --confirm; review `housekeeparr preview` first")
		return exitUsage
	}
	svc, done, code := writerService(o)
	if svc == nil {
		return code
	}
	defer done()
	// The executor needs live connections; secrets are resolved only here.
	secrets, probs := svc.Config().ResolvedSecrets(nil)
	for _, p := range probs {
		if p.Severity == "error" {
			fmt.Fprintln(errOut, p.String())
			return exitUsage
		}
	}
	conns, err := engine.Build(svc.Config(), secrets)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	svc.SetExecutor(workflow.FromConnections(conns.Clients, conns.Managers, svc.Config().ActionTiming))
	pv, res, err := svc.Act(context.Background(), id, app.ActorCLI)
	if errors.Is(err, app.ErrNotFound) {
		fmt.Fprintf(errOut, "finding %d not found\n", id)
		return exitProblem
	}
	if o.json {
		writeJSON(out, map[string]any{"preview": pv, "result": res, "error": errString(err)})
	} else if pv != nil {
		printPreview(out, pv)
	}
	var ref *action.RefusedError
	switch {
	case errors.As(err, &ref):
		fmt.Fprintln(errOut, "refused:", ref.Err)
		return exitProblem
	case err != nil:
		fmt.Fprintln(errOut, err)
		return exitProblem
	}
	if !o.json {
		fmt.Fprintf(out, "\naction #%d: %s %s\n", res.IntentID, res.State, res.Detail)
	}
	if res.State != store.IntentSucceeded {
		return exitProblem
	}
	return exitOK
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cmdActions(args []string, out, errOut io.Writer) int {
	o := newFlags("actions", errOut)
	limit := o.fs.Int("limit", 50, "maximum actions to list")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	ctx := context.Background()
	js, err := svc.Journal(ctx)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	list, err := svc.Actions(ctx, *limit)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	outcomes, err := svc.Outcomes(ctx, list)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, map[string]any{"journal": js, "actions": list, "outcomes": outcomes})
	}
	if js.RestoreHold != nil {
		fmt.Fprintf(out, "WRITES HELD: %s\nCheck external state, then run `housekeeparr restore-ack --confirm`.\n\n", js.RestoreHold.Reason)
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "No actions have been journaled.")
	}
	w := tw(out)
	if len(list) > 0 {
		fmt.Fprintln(w, "ID\tSTATE\tWORKFLOW\tORIGIN\tCREATED\tEXPLANATION")
	}
	for _, in := range list {
		review := ""
		if in.ReviewRequired {
			review = " (review required)"
		}
		fmt.Fprintf(w, "%d\t%s%s\t%s\t%s\t%s\t%s\n", in.ID, in.State, review, in.Workflow, in.Origin, ts(&in.CreatedAt), in.Explanation)
	}
	w.Flush()
	for _, in := range list {
		if oc, ok := outcomes[in.ID]; ok {
			if oc.Search.State != "" && oc.Search.State != store.SearchNotTracked {
				fmt.Fprintf(out, "#%d replacement search: %s", in.ID, app.SearchLabel(oc.Search.State))
				if oc.Search.Note != "" {
					fmt.Fprintf(out, " — %s", oc.Search.Note)
				}
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "#%d replacement grab: %s", in.ID, oc.State)
			if oc.Note != "" {
				fmt.Fprintf(out, " — %s", oc.Note)
			}
			fmt.Fprintln(out)
		}
	}
	if js.Uncertain > 0 || js.RestoreHold != nil {
		return exitProblem
	}
	return exitOK
}

func cmdReview(args []string, out, errOut io.Writer) int {
	o := newFlags("review", errOut)
	as := o.fs.String("as", "", "resolution after checking external state: succeeded, failed, or cancelled")
	note := o.fs.String("note", "", "what you checked (required)")
	pos, err := o.parse(args)
	if err != nil {
		return exitUsage
	}
	id, ok := parseID(pos, errOut)
	if !ok {
		return exitUsage
	}
	to := store.IntentState(strings.ToLower(*as))
	svc, done, code := writerService(o)
	if svc == nil {
		return code
	}
	defer done()
	if err := svc.Review(context.Background(), id, to, app.ActorCLI, *note); err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	fmt.Fprintf(out, "action #%d resolved as %s\n", id, to)
	return exitOK
}

func cmdRestoreAck(args []string, out, errOut io.Writer) int {
	o := newFlags("restore-ack", errOut)
	confirm := o.fs.Bool("confirm", false, "confirm that external state was checked")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	if !*confirm {
		fmt.Fprintln(errOut, "restore-ack requires --confirm: it accepts this state database as authoritative even though it may predate actions already sent")
		return exitUsage
	}
	svc, done, code := writerService(o)
	if svc == nil {
		return code
	}
	defer done()
	if err := svc.AcknowledgeRestore(context.Background(), app.ActorCLI); err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	fmt.Fprintln(out, "restore acknowledged; the journal marker now matches this database")
	return exitOK
}

// cmdAttempts lists the attempt ledger, or resets one content's attempts
// after review ("attempts reset <manager>|episode:<id> --note ...").
func cmdAttempts(args []string, out, errOut io.Writer) int {
	if len(args) > 0 && args[0] == "reset" {
		o := newFlags("attempts reset", errOut)
		note := o.fs.String("note", "", "what you reviewed before allowing more attempts (required)")
		pos, err := o.parse(args[1:])
		if err != nil {
			return exitUsage
		}
		if len(pos) != 1 || strings.TrimSpace(*note) == "" {
			fmt.Fprintln(errOut, "usage: housekeeparr attempts reset <manager-id>|episode:<id> --note \"what you checked\" (a note is required)")
			return exitUsage
		}
		if _, key, _ := strings.Cut(pos[0], "|"); !strings.Contains(pos[0], "|") || !validContentKey(key) {
			fmt.Fprintln(errOut, "content must be \"<manager-id>|episode:<id>\" or \"<manager-id>|movie:<id>\"")
			return exitUsage
		}
		svc, done, code := writerService(o)
		if svc == nil {
			return code
		}
		defer done()
		if err := svc.ResetAttempts(context.Background(), pos[0], app.ActorCLI, *note); err != nil {
			fmt.Fprintln(errOut, err)
			return exitUsage
		}
		fmt.Fprintf(out, "recovery attempts for %s reset; earlier attempts remain in the history\n", pos[0])
		return exitOK
	}
	o := newFlags("attempts", errOut)
	limit := o.fs.Int("limit", 50, "maximum rows to list")
	pos, err := o.parse(args)
	if err != nil {
		return exitUsage
	}
	prefix := ""
	if len(pos) == 1 {
		prefix = "attempts:" + pos[0]
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	atts, resets, err := svc.AttemptHistory(context.Background(), prefix, *limit)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, map[string]any{"attempts": atts, "resets": resets})
	}
	if len(atts) == 0 {
		fmt.Fprintln(out, "No acquisition attempts are recorded.")
	} else {
		w := tw(out)
		fmt.Fprintln(w, "AT\tSCOPE\tACTION\tCOUNTED")
		for _, a := range atts {
			counted := "yes"
			if a.ReleasedAt != nil {
				counted = "no: " + a.ReleasedReason
			}
			fmt.Fprintf(w, "%s\t%s\t#%d\t%s\n", ts(&a.At), a.Scope, a.IntentID, counted)
		}
		w.Flush()
	}
	for _, r := range resets {
		fmt.Fprintf(out, "reset %s at %s by %s: %s\n", r.Scope, ts(&r.At), r.Actor, r.Note)
	}
	return exitOK
}

func validContentKey(k string) bool {
	_, _, ok := domain.ParseContentKey(k)
	return ok
}

// cmdRetained lists transfers recovery stopped and kept with their data.
// Housekeeparr never deletes them; cleanup happens in the client.
func cmdRetained(args []string, out, errOut io.Writer) int {
	o := newFlags("retained", errOut)
	all := o.fs.Bool("all", false, "include transfers no longer retained")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	list, u, err := svc.Retained(context.Background(), *all)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	l := svc.Limits()
	if o.json {
		return writeJSON(out, map[string]any{"retained": list, "usage": u, "limits": l})
	}
	if l.RetainedTransfers > 0 {
		fmt.Fprintf(out, "%d of %d retained transfers", u.Count, l.RetainedTransfers)
	} else {
		fmt.Fprintf(out, "%d retained transfers (no count ceiling)", u.Count)
	}
	if u.Running > 0 {
		fmt.Fprintf(out, " (%d left running)", u.Running)
	}
	fmt.Fprintf(out, "; known logical size %.1f GiB", float64(u.KnownBytes)/(1<<30))
	if u.UnknownSize > 0 {
		fmt.Fprintf(out, " (%d of unknown size)", u.UnknownSize)
	}
	if u.NoPayload > 0 {
		fmt.Fprintf(out, " (%d stopped before metadata, no payload)", u.NoPayload)
	}
	if l.RetainedBytes > 0 {
		fmt.Fprintf(out, "; byte ceiling %.1f GiB", float64(l.RetainedBytes)/(1<<30))
	}
	fmt.Fprintln(out, "\nLogical size is an estimate (shared files and hardlinks), not disk use. Housekeeparr never deletes these;")
	fmt.Fprintln(out, "remove them in the client once you no longer need the partial data, and they are released at the next poll.")
	if len(list) == 0 {
		return exitOK
	}
	w := tw(out)
	fmt.Fprintln(w, "\nRETAINED\tCLIENT\tTRANSFER\tCONTENT\tLEFT\tSIZE\tACTION\tRELEASED")
	for _, r := range list {
		size := "unknown"
		if r.WantedBytes != nil {
			size = fmt.Sprintf("%.1f GiB", float64(*r.WantedBytes)/(1<<30))
		}
		rel := ""
		if r.ReleasedAt != nil {
			rel = ts(r.ReleasedAt) + " " + r.ReleasedReason
		}
		name := nonEmptyStr(r.Name, r.NativeID)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t#%d\t%s\n", ts(&r.RetainedAt), r.ClientID, name, nonEmptyStr(r.ManagerID+"|"+r.Content, "-"), r.Disposition, size, r.IntentID, rel)
	}
	w.Flush()
	return exitOK
}

func cmdPause(args []string, out, errOut io.Writer) int {
	return pauseOrResume("pause", args, out, errOut)
}
func cmdResume(args []string, out, errOut io.Writer) int {
	return pauseOrResume("resume", args, out, errOut)
}

// pauseOrResume pauses or resumes one workflow; a note is required. A
// pause stops automatic and manual actions for the workflow (unlike
// disabling its automation in configuration); requests already accepted
// externally may still complete.
func pauseOrResume(verb string, args []string, out, errOut io.Writer) int {
	o := newFlags(verb, errOut)
	note := o.fs.String("note", "", "why (required)")
	pos, err := o.parse(args)
	if err != nil {
		return exitUsage
	}
	if len(pos) != 1 || strings.TrimSpace(*note) == "" {
		fmt.Fprintf(errOut, "usage: housekeeparr %s <workflow> --note \"why\" (workflows: %s)\n", verb, workflowNames())
		return exitUsage
	}
	wf := policy.Workflow(pos[0])
	if !policy.ValidWorkflow(wf) {
		fmt.Fprintf(errOut, "unknown workflow %q (workflows: %s)\n", pos[0], workflowNames())
		return exitUsage
	}
	svc, done, code := writerService(o)
	if svc == nil {
		return code
	}
	defer done()
	ctx := context.Background()
	if verb == "pause" {
		err = svc.PauseWorkflow(ctx, wf, app.ActorCLI, *note)
	} else {
		err = svc.ResumeWorkflow(ctx, wf, app.ActorCLI, *note)
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	if verb == "pause" {
		fmt.Fprintf(out, "%s paused: nothing more will be sent for it, automatically or manually, until it is resumed (requests already accepted may still complete)\n", wf)
	} else {
		fmt.Fprintf(out, "%s resumed\n", wf)
	}
	return exitOK
}

func cmdPaused(args []string, out, errOut io.Writer) int {
	o := newFlags("paused", errOut)
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	ps, err := svc.WorkflowPauses(context.Background())
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, ps)
	}
	if len(ps) == 0 {
		fmt.Fprintln(out, "No workflow is paused.")
		return exitOK
	}
	for _, wf := range policy.AllWorkflows() {
		if p, ok := ps[string(wf)]; ok {
			fmt.Fprintf(out, "%s paused at %s by %s: %s\n", wf, ts(&p.PausedAt), p.Actor, p.Note)
		}
	}
	return exitOK
}

func workflowNames() string {
	var names []string
	for _, w := range policy.AllWorkflows() {
		names = append(names, string(w))
	}
	return strings.Join(names, ", ")
}

func cmdNotifications(args []string, out, errOut io.Writer) int {
	o := newFlags("notifications", errOut)
	limit := o.fs.Int("limit", 50, "number of events")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	list, err := svc.Notifications(context.Background(), *limit)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, list)
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "No notification events. Configure [[notify]] targets to receive them.")
		return exitOK
	}
	w := tw(out)
	fmt.Fprintln(w, "QUEUED\tTARGET\tKIND\tSEVERITY\tSTATE\tTITLE")
	for _, n := range list {
		state := fmt.Sprintf("pending (attempts %d)", n.Attempts)
		switch {
		case n.SentAt != nil:
			state = "sent " + ts(n.SentAt)
		case n.DroppedAt != nil:
			state = "dropped: " + n.LastError
		case n.LastError != "":
			state += "; last: " + n.LastError
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", ts(&n.CreatedAt), n.Target, n.Kind, n.Severity, state, n.Title)
	}
	w.Flush()
	return exitOK
}

func cmdMissing(args []string, out, errOut io.Writer) int {
	o := newFlags("missing", errOut)
	all := o.fs.Bool("all", false, "include content no longer missing")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	list, err := svc.Missing(context.Background(), !*all)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, list)
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "No missing content is recorded. Missing lists are read hourly while missing_search is enabled in some scope.")
		return exitOK
	}
	w := tw(out)
	fmt.Fprintln(w, "MANAGER\tCONTENT\tTITLE\tAVAILABLE\tSEARCHES\tLAST SEARCH\tNEXT\tSTATE")
	for _, m := range list {
		avail := "unknown"
		if m.Available != nil {
			avail = fmt.Sprint(*m.Available)
		}
		next := "-"
		if m.NextSearchAt != nil {
			next = ts(m.NextSearchAt)
		}
		last := "-"
		if m.LastSearchAt != nil {
			last = ts(m.LastSearchAt)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", m.ManagerID, m.ContentKey, m.Title, avail, m.Searches, last, next, m.State)
	}
	w.Flush()
	fmt.Fprintln(out, "\nA search the manager accepted does not mean a release was found or grabbed.")
	return exitOK
}
