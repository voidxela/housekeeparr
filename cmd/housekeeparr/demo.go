package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/web"
)

func cmdDemo(args []string, out, errOut io.Writer) int {
	o := newFlags("demo", errOut)
	listen := o.fs.String("listen", "127.0.0.1:8485", "loopback address for the demo interface")
	dir := o.fs.String("state", "", "directory for demo state (default: a temporary directory removed on exit)")
	if _, err := o.parse(args); err != nil {
		return exitUsage
	}
	stateDir := *dir
	if stateDir == "" {
		d, err := os.MkdirTemp("", "housekeeparr-demo-*")
		if err != nil {
			fmt.Fprintln(errOut, err)
			return exitFailure
		}
		defer os.RemoveAll(d)
		stateDir = d
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(errOut, "Generating synthetic demo data (no network access, no real services)…")
	d, err := demo.Generate(ctx, stateDir, time.Now())
	if err != nil {
		fmt.Fprintln(errOut, "demo:", err)
		return exitFailure
	}
	defer d.Store.Close()
	d.Config.Web.Listen = *listen
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	srv, err := web.New(svc, web.Options{Listen: *listen, Logger: newLogger(d.Config, errOut)})
	if err != nil {
		fmt.Fprintln(errOut, "demo:", err)
		return exitFailure
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintln(errOut, "demo web:", err)
			stop()
		}
	}()
	fmt.Fprintf(out, "Demo interface: http://%s/  (synthetic data in %s; Ctrl-C to stop)\n", *listen, stateDir)
	last := time.Now()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(sctx)
			_ = d.Engine.Stop(sctx, "demo stopped")
			cancel()
			return exitOK
		case now := <-t.C:
			d.Tick(ctx, now, &last)
		}
	}
}
