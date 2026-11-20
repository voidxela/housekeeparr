package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/store"
)

// An unchanged pass logs nothing above Debug: per-finding lines and held
// scopes are logged when they change, actions always.
func TestPassLogIsProportionalToChange(t *testing.T) {
	var buf bytes.Buffer
	p := newPassLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	pass := func(evs []store.AutomationEvaluation, breakers map[string]string) int {
		buf.Reset()
		p.recovery(app.PassReport{Recovery: app.DispatchReport{Evaluations: evs, Breakers: breakers}})
		return strings.Count(buf.String(), "\n")
	}
	var waiting []store.AutomationEvaluation
	for i := int64(1); i <= 70; i++ {
		waiting = append(waiting, store.AutomationEvaluation{FindingID: i, Result: store.EvalWaiting})
	}
	held := map[string]string{"client:q": "3 of 5 stalled"}
	if n := pass(waiting, held); n != 72 { // 70 new results, the hold, the summary
		t.Fatalf("first pass logged %d lines:\n%s", n, buf.String())
	}
	if n := pass(waiting, map[string]string{"client:q": "4 of 5 stalled"}); n != 0 {
		t.Fatalf("an unchanged pass logged %d lines at Info or above:\n%s", n, buf.String())
	}
	changed := append([]store.AutomationEvaluation(nil), waiting...)
	changed[0] = store.AutomationEvaluation{FindingID: 1, Result: store.EvalDispatched, IntentID: 9}
	if n := pass(changed, nil); n != 3 || !strings.Contains(buf.String(), "hold lifted") || !strings.Contains(buf.String(), "action=9") {
		t.Fatalf("a pass with an action and a lifted hold logged %d lines:\n%s", n, buf.String())
	}
}
