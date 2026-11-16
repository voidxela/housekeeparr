package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/engine"
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
	svc.SetExecutor(workflow.FromConnections(conns.Clients, conns.Managers))
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
	if o.json {
		return writeJSON(out, map[string]any{"journal": js, "actions": list})
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
