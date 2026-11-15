package demo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

func TestDemoCoversScenariosAndIsIsolated(t *testing.T) {
	dir := t.TempDir()
	d, err := Generate(context.Background(), dir, time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := d.Store.Findings(context.Background(), store.FindingFilter{States: []domain.FindingState{domain.FindingOpen}})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, f := range fs {
		types[f.Type]++
	}
	for _, want := range []string{domain.FindingTransferStalled, domain.FindingMetadataTimeout, domain.FindingTransferError,
		domain.FindingImportAttention, domain.FindingManagerHealth, domain.FindingConnectionFailure} {
		if types[want] == 0 {
			t.Errorf("demo lacks a %s finding (have %v)", want, types)
		}
	}
	if d.Store.Origin() != store.OriginDemo {
		t.Fatal("demo store must be marked demo")
	}
	d.Store.Close()
	_, err = store.Open(context.Background(), dir, store.Options{Origin: store.OriginLive})
	var om *store.OriginMismatchError
	if !errors.As(err, &om) {
		t.Fatalf("demo state must never open as live: %v", err)
	}
}
