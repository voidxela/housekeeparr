package app

import (
	"context"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/store"
)

func TestOneShotFreshnessDrivesHealth(t *testing.T) {
	ctx := context.Background()
	cfg, _, err := config.Parse([]byte("version = 1\n"), "/x/h.toml")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st, err := store.Open(ctx, t.TempDir(), store.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.StartRun(store.Run{RunID: "r", Kind: "once", StartedAt: now, PID: 1, Version: "t", ConfigRevision: "c", Mode: "observe"}); err != nil {
			return err
		}
		if err := tx.CycleFinished("r", now, nil); err != nil {
			return err
		}
		return tx.StopRun("r", now, "one-shot cycle complete")
	})
	clock := now.Add(5 * time.Minute)
	svc := New(st, cfg, func() time.Time { return clock }, nil)
	w, _ := svc.Worker(ctx)
	if w.State != WorkerScheduled || !w.Healthy() {
		t.Fatalf("recent one-shot: %+v", w)
	}
	clock = now.Add(2 * time.Hour)
	w, _ = svc.Worker(ctx)
	if w.State != WorkerStale || w.Healthy() {
		t.Fatalf("stale one-shot must be unhealthy: %+v", w)
	}
}
