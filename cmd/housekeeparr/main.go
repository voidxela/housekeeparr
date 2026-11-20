// Command housekeeparr is the single Housekeeparr binary: worker, one-shot
// runner, CLI, optional web interface, and demo.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/control"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/notify"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/web"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitOK        = 0
	exitProblem   = 1 // command ran; it found problems (failed checks, stale worker)
	exitUsage     = 2 // bad arguments or invalid configuration
	exitStateBusy = 3 // another process owns the state store
	exitFailure   = 4 // unexpected runtime failure
)

type command struct {
	name    string
	summary string
	run     func(args []string, out, errOut io.Writer) int
}

func commands() []command {
	return []command{
		{"run", "run the worker (add --once for a single cycle); serves the web interface when enabled", cmdRun},
		{"status", "show worker, connections, and finding counts (exit 1 if the worker is not healthy)", cmdStatus},
		{"findings", "list unresolved findings (--all includes resolved)", cmdFindings},
		{"finding", "show one finding with evidence and history: finding <id>", cmdFinding},
		{"downloads", "list observed downloads and their assessments", cmdDownloads},
		{"connections", "show connection health and capabilities from state", cmdConnections},
		{"policy", "show effective diagnosis policy and where each value comes from", cmdPolicy},
		{"activity", "show recent finding lifecycle and audit events", cmdActivity},
		{"ack", "acknowledge a finding: ack <id>", cmdAck},
		{"snooze", "snooze a finding: snooze <id> --for 24h", cmdSnooze},
		{"reopen", "return an acknowledged or snoozed finding to open: reopen <id>", cmdReopen},
		{"preview", "preview the action for a finding: prerequisites, exact operations, effects: preview <id>", cmdPreview},
		{"act", "run the previewed manual action through the coordinator: act <id> --confirm", cmdAct},
		{"actions", "list journaled actions and holds (exit 1 if any need review)", cmdActions},
		{"review", "resolve an uncertain action after checking: review <action-id> --as failed --note ...", cmdReview},
		{"retained", "list transfers recovery left in the client with their data (stopped or running), against the configured ceilings (--all includes released)", cmdRetained},
		{"notifications", "list recent notification events and their delivery state", cmdNotifications},
		{"sources", "release-source scorecard: grabs per indexer through import, from the managers' history (--acquisitions lists each grab)", cmdSources},
		{"missing", "list missing content managers report and Housekeeparr's proactive-search state (--all includes no longer missing)", cmdMissing},
		{"attempts", "list recovery attempts and pacing use; allow more after review: attempts reset <manager>|episode:<id> --note ...", cmdAttempts},
		{"pause", "pause a workflow (no automatic or manual actions) until resumed: pause <workflow> --note ...", cmdPause},
		{"resume", "resume a paused workflow: resume <workflow> --note ...", cmdResume},
		{"paused", "list paused workflows", cmdPaused},
		{"restore-ack", "accept a restored state database as authoritative: restore-ack --confirm", cmdRestoreAck},
		{"check", "probe every configured connection now (read-only; does not change state)", cmdCheck},
		{"config", "validate configuration and secret references: config validate", cmdConfig},
		{"demo", "serve the web interface on synthetic demo data in a separate state directory", cmdDemo},
		{"version", "print the version", func([]string, io.Writer, io.Writer) int { fmt.Println(version); return exitOK }},
	}
}

func main() { os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr)) }

func dispatch(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(errOut)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(args[1:], out, errOut)
		}
	}
	fmt.Fprintf(errOut, "unknown command %q\n\n", args[0])
	usage(errOut)
	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: housekeeparr <command> [--config path] [flags]")
	fmt.Fprintln(w, "\nCommands:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-12s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w, "\nConfiguration is read from --config, $HOUSEKEEPARR_CONFIG, or ./housekeeparr.toml.")
	fmt.Fprintln(w, "Most read commands accept --json for machine-readable output.")
}

// common flags
type opts struct {
	fs     *flag.FlagSet
	config string
	json   bool
	errOut io.Writer
}

func newFlags(name string, errOut io.Writer) *opts {
	o := &opts{fs: flag.NewFlagSet(name, flag.ContinueOnError), errOut: errOut}
	o.fs.SetOutput(errOut)
	o.fs.StringVar(&o.config, "config", "", "configuration file")
	o.fs.BoolVar(&o.json, "json", false, "machine-readable JSON output")
	return o
}

// parse accepts flags before and after positional arguments.
func (o *opts) parse(args []string) ([]string, error) {
	var pos []string
	for {
		if err := o.fs.Parse(args); err != nil {
			return nil, err
		}
		rest := o.fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func (o *opts) loadConfig() (*config.Config, int) {
	path, err := config.Locate(o.config)
	if err != nil {
		fmt.Fprintln(o.errOut, err)
		return nil, exitUsage
	}
	cfg, probs, err := config.Load(path)
	for _, p := range probs {
		if p.Severity == "warning" {
			fmt.Fprintln(o.errOut, p.String())
		}
	}
	if err != nil {
		fmt.Fprintln(o.errOut, err)
		return nil, exitUsage
	}
	return cfg, exitOK
}

func openReadOnly(ctx context.Context, cfg *config.Config, errOut io.Writer) (*store.Store, int) {
	st, err := store.Open(ctx, cfg.State.Dir, store.Options{ReadOnly: true})
	if err != nil {
		fmt.Fprintln(errOut, err)
		if errors.Is(err, store.ErrNoState) {
			return nil, exitProblem
		}
		return nil, exitFailure
	}
	return st, exitOK
}

func openWriter(ctx context.Context, cfg *config.Config, errOut io.Writer) (*store.Store, int) {
	st, _, code := openOrRoute(ctx, cfg, errOut, nil)
	return st, code
}

// workerTimeout bounds how long a CLI command waits for the worker's
// answer; a manual action answers once it has finished.
const workerTimeout = 15 * time.Minute

// openOrRoute opens the state store as its single writer or, when the
// running worker owns it and req is set, sends req to the worker over its
// control socket (SPEC §10) and returns the worker's answer instead. A
// request whose answer is lost is reported as of unknown outcome and never
// retried.
func openOrRoute(ctx context.Context, cfg *config.Config, errOut io.Writer, req *control.Request) (*store.Store, *control.Response, int) {
	st, err := store.Open(ctx, cfg.State.Dir, store.Options{})
	if err != nil {
		var le *store.LockedError
		if !errors.As(err, &le) {
			fmt.Fprintln(errOut, err)
			return nil, nil, exitFailure
		}
		if req == nil {
			fmt.Fprintln(errOut, err)
			fmt.Fprintln(errOut, "This command needs exclusive use of the state store; stop the worker first.")
			return nil, nil, exitStateBusy
		}
		sctx, cancel := context.WithTimeout(ctx, workerTimeout)
		defer cancel()
		resp, err := control.Send(sctx, cfg.State.Dir, *req)
		switch {
		case errors.Is(err, control.ErrNoWorker):
			fmt.Fprintln(errOut, le)
			fmt.Fprintln(errOut, err)
			fmt.Fprintln(errOut, "The process holding the store does not accept commands (a one-shot run, or an older worker); retry when it finishes, or use the web interface.")
			return nil, nil, exitStateBusy
		case err != nil:
			fmt.Fprintln(errOut, err)
			return nil, nil, exitFailure
		}
		return nil, &resp, exitOK
	}
	for _, w := range st.Warnings() {
		fmt.Fprintln(errOut, "warning:", w)
	}
	return st, nil, exitOK
}

// reportRouted prints the running worker's answer to a routed command.
func reportRouted(out, errOut io.Writer, resp *control.Response, success string) int {
	switch {
	case resp.OK:
		fmt.Fprintln(out, success+" (by the running worker)")
		return exitOK
	case resp.NotFound:
		fmt.Fprintln(errOut, resp.Error)
		return exitProblem
	case resp.Refused:
		fmt.Fprintln(errOut, "refused by the running worker; nothing was sent:", resp.Error)
		return exitProblem
	}
	fmt.Fprintln(errOut, "the running worker reported:", resp.Error)
	return exitFailure
}

func newLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.Log.Level))
	ho := &slog.HandlerOptions{Level: lvl}
	if cfg.Log.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, ho))
	}
	return slog.New(slog.NewTextHandler(w, ho))
}

func cmdRun(args []string, out, errOut io.Writer) int {
	o := newFlags("run", errOut)
	once := o.fs.Bool("once", false, "run a single observation cycle and exit")
	headless := o.fs.Bool("headless", false, "never open a listener, even if the web interface is enabled")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	cfg, code := o.loadConfig()
	if cfg == nil {
		return code
	}
	secrets, probs := cfg.ResolvedSecrets(nil)
	failed := false
	for _, p := range probs {
		fmt.Fprintln(errOut, p.String())
		failed = failed || p.Severity == "error"
	}
	if failed {
		return exitUsage
	}
	conns, err := engine.Build(cfg, secrets)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	log := newLogger(cfg, errOut)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, code := openWriter(ctx, cfg, errOut)
	if st == nil {
		return code
	}
	defer st.Close()
	// Startup reconciliation of the action journal: interrupted actions are
	// observed with the real executor (never re-sent); anything that cannot
	// be established is held as uncertain with its locks for review, and a
	// suspected restore holds all writes.
	{
		jsvc := app.New(st, cfg, nil, nil)
		jsvc.SetExecutor(workflow.FromConnections(conns.Clients, conns.Managers, cfg.ActionTiming))
		rep, err := jsvc.Reconcile(ctx)
		if err != nil {
			fmt.Fprintln(errOut, "action journal reconciliation:", err)
			return exitFailure
		}
		if len(rep.Settled)+len(rep.Cancelled)+len(rep.Uncertain) > 0 {
			log.Warn("reconciled interrupted actions", "settled", rep.Settled, "cancelled", rep.Cancelled, "uncertain", rep.Uncertain)
		}
		js, err := jsvc.Journal(ctx)
		if err != nil {
			fmt.Fprintln(errOut, "action journal:", err)
			return exitFailure
		}
		if js.RestoreHold != nil {
			log.Warn("writes held: state database may predate actions already sent", "reason", js.RestoreHold.Reason)
		}
		if js.Uncertain > 0 {
			log.Warn("actions need review before related work can proceed", "uncertain", js.Uncertain)
		}
	}
	kind := "daemon"
	if *once {
		kind = "once"
	}
	eng, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Conns: conns, Logger: log, RunKind: kind, Version: version})
	if err != nil {
		fmt.Fprintln(errOut, "start:", err)
		return exitFailure
	}
	stopRun := func(reason string) {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := eng.Stop(sctx, reason); err != nil {
			log.Error("could not record shutdown", "err", err)
		}
	}
	// One service for the web interface and automatic dispatch, so manual
	// and automatic execution are serialised by the same lock.
	svc := app.New(st, cfg, nil, eng)
	svc.SetExecutor(workflow.FromConnections(conns.Clients, conns.Managers, cfg.ActionTiming))
	var passErr error
	passLog := newPassLogger(log)
	dispatch := func(ctx context.Context, rep engine.CycleReport) {
		// Release-source findings come from stored history (observation
		// only), whatever the mode.
		if err := svc.EvaluateSources(ctx); err != nil {
			log.Error("release-source evaluation failed", "err", err)
		}
		pr := svc.DispatchPass(ctx, rep.Fresh())
		passErr = pr.Err()
		passLog.recovery(pr)
		// At most one proactive search per pass (SPEC §9.1), and none after
		// a recovery pass that stopped on an error.
		switch sr := pr.Search; {
		case pr.SearchSkipped != "":
			log.Warn("proactive search skipped", "reason", pr.SearchSkipped)
		case pr.SearchErr != nil:
			logWriteFailure(log, "proactive search", pr.SearchErr)
		case sr.Sent != "":
			log.Info("proactive search", "content", sr.Sent, "action", sr.IntentID, "outcome", sr.Outcome)
		case len(sr.Waiting) > 0:
			log.Debug("proactive search waiting", "reasons", sr.Waiting)
		}
		// Notifications go out after the cycle and dispatch; a failing
		// notification path only affects notifications.
		urls := make(map[string]string, len(cfg.Notify))
		for _, n := range cfg.Notify {
			urls[n.ID] = secrets["notify."+n.ID+".url"]
		}
		nr, err := notify.Deliver(ctx, st, cfg.Notify, cfg.Notifications, urls, nil, time.Now())
		switch {
		case err != nil:
			log.Error("notification delivery stopped", "err", err)
		case nr.Sent+nr.Failed+nr.Dropped+nr.Held > 0:
			log.Info("notifications", "sent", nr.Sent, "failed", nr.Failed, "dropped", nr.Dropped, "held_by_rate_limit", nr.Held)
		}
	}
	eng.OnCycle(dispatch)
	if *once {
		rep := eng.Cycle(ctx, nil)
		if rep.StoreError == "" {
			dispatch(ctx, rep)
		}
		stopRun("one-shot cycle complete")
		if o.json {
			if code := writeJSON(out, rep); code != exitOK || passErr != nil {
				return exitFailure
			}
			return exitOK
		}
		printCycle(out, rep)
		if rep.StoreError != "" || passErr != nil {
			if passErr != nil {
				fmt.Fprintln(errOut, "write phase:", passErr)
			}
			return exitFailure
		}
		for _, c := range rep.Connections {
			if !c.OK {
				return exitProblem
			}
		}
		return exitOK
	}
	log.Info("housekeeparr worker starting", "version", version, "mode", cfg.Mode, "config_revision", cfg.Revision,
		"connections", len(conns.Meta), "interval", cfg.Polling.Interval.Duration)
	// Headless CLI mutations reach this worker over an owner-only socket.
	if ctl, err := control.Listen(svc, cfg.State.Dir); err != nil {
		log.Warn("control socket unavailable: CLI changes are refused while the worker runs", "err", err)
	} else {
		defer ctl.Close()
	}
	var srv *web.Server
	if cfg.Web.Enabled && !*headless {
		wo := web.OptionsFromConfig(cfg.Web, secrets["web.password"], log)
		srv, err = web.New(svc, wo)
		if err != nil {
			fmt.Fprintln(errOut, "web:", err)
			stopRun("web interface failed to start")
			return exitFailure
		}
		go func() {
			if err := srv.ListenAndServe(); err != nil {
				log.Error("web interface stopped", "err", err)
				stop()
			}
		}()
		scheme := "http"
		if cfg.Web.TLSCert != "" {
			scheme = "https"
		}
		log.Info("web interface listening", "url", scheme+"://"+cfg.Web.Listen+cfg.Web.BasePath+"/")
	} else {
		log.Info("headless: no listener opened")
	}
	err = eng.Run(ctx)
	if srv != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(sctx)
		cancel()
	}
	stopRun("signal received")
	log.Info("housekeeparr worker stopped")
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	return exitOK
}

// readService opens state read-only and builds the shared service.
func readService(o *opts) (*app.Service, func(), int) {
	cfg, code := o.loadConfig()
	if cfg == nil {
		return nil, nil, code
	}
	st, code := openReadOnly(context.Background(), cfg, o.errOut)
	if st == nil {
		return nil, nil, code
	}
	if st.Origin() == store.OriginDemo {
		fmt.Fprintln(o.errOut, "NOTE: this state directory contains synthetic DEMO data, not real observations.")
	}
	return app.New(st, cfg, nil, nil), func() { st.Close() }, exitOK
}

func cmdStatus(args []string, out, errOut io.Writer) int {
	o := newFlags("status", errOut)
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	ov, err := svc.Overview(context.Background())
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	healthy := ov.Worker.Healthy()
	if o.json {
		writeJSON(out, ov)
	} else {
		printOverview(out, ov)
	}
	if !healthy {
		return exitProblem
	}
	return exitOK
}

func cmdFindings(args []string, out, errOut io.Writer) int {
	o := newFlags("findings", errOut)
	all := o.fs.Bool("all", false, "include resolved findings")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	q := app.FindingQuery{}
	if *all {
		q.States = append(append(q.States, app.ActiveStates...), "resolved")
	}
	fs, err := svc.Findings(context.Background(), q)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, fs)
	}
	printFindings(out, fs)
	return exitOK
}

func parseID(pos []string, errOut io.Writer) (int64, bool) {
	if len(pos) != 1 {
		fmt.Fprintln(errOut, "expected exactly one id")
		return 0, false
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(errOut, "invalid id %q\n", pos[0])
		return 0, false
	}
	return id, true
}

func cmdFinding(args []string, out, errOut io.Writer) int {
	o := newFlags("finding", errOut)
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
	d, err := svc.Finding(context.Background(), id)
	if errors.Is(err, app.ErrNotFound) {
		fmt.Fprintf(errOut, "finding %d not found\n", id)
		return exitProblem
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, d)
	}
	printFinding(out, d)
	return exitOK
}

func cmdDownloads(args []string, out, errOut io.Writer) int {
	o := newFlags("downloads", errOut)
	all := o.fs.Bool("all", false, "include complete transfers")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	d, err := svc.Downloads(context.Background(), *all)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, d)
	}
	printDownloads(out, d)
	return exitOK
}

func cmdConnections(args []string, out, errOut io.Writer) int {
	o := newFlags("connections", errOut)
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	cs, err := svc.Connections(context.Background())
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, cs)
	}
	printConnections(out, cs)
	return exitOK
}

func cmdPolicy(args []string, out, errOut io.Writer) int {
	o := newFlags("policy", errOut)
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	cfg, code := o.loadConfig()
	if cfg == nil {
		return code
	}
	// Policy is derived from configuration alone; no state is needed.
	svc := app.New(nil, cfg, nil, nil)
	ps, err := svc.Policies()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	if o.json {
		return writeJSON(out, ps)
	}
	printPolicies(out, ps)
	return exitOK
}

func cmdActivity(args []string, out, errOut io.Writer) int {
	o := newFlags("activity", errOut)
	limit := o.fs.Int("limit", 50, "maximum events per section")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	svc, done, code := readService(o)
	if svc == nil {
		return code
	}
	defer done()
	a, err := svc.Activity(context.Background(), *limit)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitFailure
	}
	if o.json {
		return writeJSON(out, a)
	}
	printActivity(out, a)
	return exitOK
}

func mutateCmd(name string, args []string, out, errOut io.Writer, extra func(*opts) (func(*app.Service, int64) error, func(int64) control.Request)) int {
	o := newFlags(name, errOut)
	act, request := extra(o)
	pos, err := o.parse(args)
	if err != nil {
		return exitUsage
	}
	id, ok := parseID(pos, errOut)
	if !ok {
		return exitUsage
	}
	cfg, code := o.loadConfig()
	if cfg == nil {
		return code
	}
	req := request(id)
	st, resp, code := openOrRoute(context.Background(), cfg, errOut, &req)
	if resp != nil {
		return reportRouted(out, errOut, resp, fmt.Sprintf("finding %d updated", id))
	}
	if st == nil {
		return code
	}
	defer st.Close()
	svc := app.New(st, cfg, nil, nil)
	if err := act(svc, id); err != nil {
		fmt.Fprintln(errOut, err)
		if errors.Is(err, app.ErrNotFound) {
			return exitProblem
		}
		return exitUsage
	}
	fmt.Fprintf(out, "finding %d updated\n", id)
	return exitOK
}

func cmdAck(args []string, out, errOut io.Writer) int {
	return mutateCmd("ack", args, out, errOut, func(*opts) (func(*app.Service, int64) error, func(int64) control.Request) {
		return func(s *app.Service, id int64) error {
			_, err := s.Acknowledge(context.Background(), id, app.ActorCLI)
			return err
		}, func(id int64) control.Request { return control.Request{Op: "ack", ID: id} }
	})
}

func cmdReopen(args []string, out, errOut io.Writer) int {
	return mutateCmd("reopen", args, out, errOut, func(*opts) (func(*app.Service, int64) error, func(int64) control.Request) {
		return func(s *app.Service, id int64) error {
			_, err := s.Reopen(context.Background(), id, app.ActorCLI)
			return err
		}, func(id int64) control.Request { return control.Request{Op: "reopen", ID: id} }
	})
}

func cmdSnooze(args []string, out, errOut io.Writer) int {
	return mutateCmd("snooze", args, out, errOut, func(o *opts) (func(*app.Service, int64) error, func(int64) control.Request) {
		d := o.fs.Duration("for", 24*time.Hour, "snooze duration (1h to 2160h)")
		return func(s *app.Service, id int64) error {
				_, err := s.Snooze(context.Background(), id, *d, app.ActorCLI)
				return err
			}, func(id int64) control.Request {
				return control.Request{Op: "snooze", ID: id, Duration: *d}
			}
	})
}

func cmdConfig(args []string, out, errOut io.Writer) int {
	o := newFlags("config", errOut)
	pos, err := o.parse(args)
	if err != nil {
		return exitUsage
	}
	if len(pos) != 1 || pos[0] != "validate" {
		fmt.Fprintln(errOut, "usage: housekeeparr config validate [--config path] [--json]")
		return exitUsage
	}
	path, err := config.Locate(o.config)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitUsage
	}
	cfg, probs, err := config.Load(path)
	if cfg != nil {
		_, sp := cfg.ResolvedSecrets(nil)
		probs = append(probs, sp...)
	}
	result := struct {
		Path     string           `json:"path"`
		Valid    bool             `json:"valid"`
		Revision string           `json:"revision,omitempty"`
		Problems []config.Problem `json:"problems"`
	}{Path: path, Problems: probs}
	result.Valid = err == nil
	for _, p := range probs {
		if p.Severity == "error" {
			result.Valid = false
		}
	}
	if cfg != nil {
		result.Revision = cfg.Revision
	}
	if o.json {
		writeJSON(out, result)
	} else {
		if err != nil && len(probs) == 0 {
			fmt.Fprintln(out, err)
		}
		for _, p := range probs {
			fmt.Fprintln(out, p.String())
		}
		if result.Valid {
			fmt.Fprintf(out, "%s: valid (revision %s)\n", path, result.Revision)
		} else {
			fmt.Fprintf(out, "%s: invalid\n", path)
		}
	}
	if !result.Valid {
		return exitUsage
	}
	return exitOK
}

// logWriteFailure reports a write-phase failure truthfully: whether a
// request may already have taken effect, and that no further acquisition
// is started until reconciliation succeeds.
func logWriteFailure(log *slog.Logger, phase string, err error) {
	if action.MaybeSent(err) {
		log.Error(phase+" stopped: a request was sent but its outcome could not be recorded; it will be settled by observation and never re-sent; "+
			"no further acquisition is started until the journal is reconciled", "err", err)
		return
	}
	log.Error(phase+" stopped before sending anything further; no further acquisition is started until the journal is reconciled", "err", err)
}

// passLogger keeps the worker's log proportional to change: one summary
// per pass, each evaluation at Info only when an action was taken or its
// result changed since the previous pass (Debug otherwise), and breakers
// when they open or close. Actions, errors and their records are never
// suppressed.
type passLogger struct {
	log      *slog.Logger
	results  map[int64]string
	breakers map[string]bool
}

func newPassLogger(log *slog.Logger) *passLogger {
	return &passLogger{log: log, results: map[int64]string{}, breakers: map[string]bool{}}
}

func (p *passLogger) recovery(pr app.PassReport) {
	r := pr.Recovery
	counts := map[string]int{}
	results := make(map[int64]string, len(r.Evaluations))
	changed := 0
	for _, e := range r.Evaluations {
		counts[e.Result]++
		results[e.FindingID] = e.Result
		level := slog.LevelDebug
		if e.Result == store.EvalDispatched || p.results[e.FindingID] != e.Result {
			level = slog.LevelInfo
			changed++
		}
		p.log.Log(context.Background(), level, "automatic evaluation", "finding", e.FindingID, "result", e.Result, "action", e.IntentID, "detail", e.Detail)
	}
	p.results = results
	switch {
	case pr.RecoveryErr != nil:
		logWriteFailure(p.log, "automatic dispatch", pr.RecoveryErr)
	case r.Skipped != "":
		p.log.Debug("automatic dispatch skipped", "reason", r.Skipped)
	default:
		open := make(map[string]bool, len(r.Breakers))
		for scope, why := range r.Breakers {
			open[scope] = true
			if !p.breakers[scope] {
				p.log.Warn("automation held", "scope", scope, "reason", why)
			} else {
				p.log.Debug("automation still held", "scope", scope, "reason", why)
			}
		}
		for scope := range p.breakers {
			if !open[scope] {
				p.log.Info("automation hold lifted", "scope", scope)
			}
		}
		p.breakers = open
	}
	if len(r.Evaluations) > 0 {
		level := slog.LevelDebug
		if changed > 0 {
			level = slog.LevelInfo
		}
		args := []any{"evaluated", len(r.Evaluations), "changed", changed}
		for _, k := range []string{store.EvalDispatched, store.EvalRefused, store.EvalHeld, store.EvalDeferred, store.EvalWaiting,
			store.EvalNotPermitted, store.EvalAtCeiling, store.EvalNeedsReview, store.EvalIneligible} {
			if counts[k] > 0 {
				args = append(args, k, counts[k])
			}
		}
		p.log.Log(context.Background(), level, "automatic pass", args...)
	}
}
