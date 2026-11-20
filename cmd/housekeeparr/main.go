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

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
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
	st, err := store.Open(ctx, cfg.State.Dir, store.Options{})
	if err != nil {
		var le *store.LockedError
		if errors.As(err, &le) {
			fmt.Fprintln(errOut, err)
			fmt.Fprintln(errOut, "Local changes such as ack/snooze must go through the running worker's web interface, or stop the worker first.")
			return nil, exitStateBusy
		}
		fmt.Fprintln(errOut, err)
		return nil, exitFailure
	}
	for _, w := range st.Warnings() {
		fmt.Fprintln(errOut, "warning:", w)
	}
	return st, exitOK
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
	dispatch := func(ctx context.Context, rep engine.CycleReport) {
		r, err := svc.DispatchAutomatic(ctx, rep.Fresh())
		switch {
		case err != nil:
			log.Error("automatic dispatch failed; nothing further was sent this cycle", "err", err)
		case r.Skipped != "":
			log.Debug("automatic dispatch skipped", "reason", r.Skipped)
		default:
			for _, e := range r.Evaluations {
				log.Info("automatic evaluation", "finding", e.FindingID, "result", e.Result, "action", e.IntentID, "detail", e.Detail)
			}
			for scope, why := range r.Breakers {
				log.Warn("automation held", "scope", scope, "reason", why)
			}
		}
		// At most one proactive search per pass (SPEC §9.1).
		if sr, err := svc.DispatchSearches(ctx, rep.Fresh()); err != nil {
			log.Error("proactive search failed; nothing further was sent this cycle", "err", err)
		} else if sr.Sent != "" {
			log.Info("proactive search", "content", sr.Sent, "action", sr.IntentID, "outcome", sr.Outcome)
		} else if len(sr.Waiting) > 0 {
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
			return writeJSON(out, rep)
		}
		printCycle(out, rep)
		if rep.StoreError != "" {
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
	var srv *web.Server
	if cfg.Web.Enabled && !*headless {
		wo := web.Options{Listen: cfg.Web.Listen, Logger: log, AllowedHosts: cfg.Web.AllowedHosts, TLSCert: cfg.Web.TLSCert, TLSKey: cfg.Web.TLSKey,
			TrustedProxy: cfg.Web.TrustedProxy, BasePath: cfg.Web.BasePath, ReadHeaderTimeout: cfg.Web.ReadHeaderTimeout.Duration, ReadTimeout: cfg.Web.ReadTimeout.Duration,
			WriteTimeout: cfg.Web.WriteTimeout.Duration, IdleTimeout: cfg.Web.IdleTimeout.Duration}
		if cfg.Web.Password != "" {
			wo.Auth = &web.Auth{Username: cfg.Web.Username, Password: secrets["web.password"],
				SessionTTL: cfg.Web.SessionTTL.Duration, SignInWindow: cfg.Web.SignInWindow.Duration, SignInFailures: cfg.Web.SignInFailures}
		}
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

func mutateCmd(name string, args []string, out, errOut io.Writer, extra func(*opts) func(*app.Service, int64) error) int {
	o := newFlags(name, errOut)
	act := extra(o)
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
	st, code := openWriter(context.Background(), cfg, errOut)
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
	return mutateCmd("ack", args, out, errOut, func(*opts) func(*app.Service, int64) error {
		return func(s *app.Service, id int64) error {
			_, err := s.Acknowledge(context.Background(), id, app.ActorCLI)
			return err
		}
	})
}

func cmdReopen(args []string, out, errOut io.Writer) int {
	return mutateCmd("reopen", args, out, errOut, func(*opts) func(*app.Service, int64) error {
		return func(s *app.Service, id int64) error {
			_, err := s.Reopen(context.Background(), id, app.ActorCLI)
			return err
		}
	})
}

func cmdSnooze(args []string, out, errOut io.Writer) int {
	return mutateCmd("snooze", args, out, errOut, func(o *opts) func(*app.Service, int64) error {
		d := o.fs.Duration("for", 24*time.Hour, "snooze duration (1h to 2160h)")
		return func(s *app.Service, id int64) error {
			_, err := s.Snooze(context.Background(), id, *d, app.ActorCLI)
			return err
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
