package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
)

// CheckResult is a live, read-only connection check. It is not persisted.
type CheckResult struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Kind       adapter.Kind   `json:"kind"`
	URL        string         `json:"url"`
	OK         bool           `json:"ok"`
	Step       string         `json:"step"` // last step attempted
	ErrorClass string         `json:"error_class,omitempty"`
	Error      string         `json:"error,omitempty"`
	Probe      *adapter.Probe `json:"probe,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Duration   time.Duration  `json:"duration"`
}

// Check probes and performs one read per enabled connection, with bounded
// concurrency. It never writes to state or to the remote applications.
func Check(ctx context.Context, conns *Connections, timeout time.Duration, concurrency int) []CheckResult {
	if concurrency < 1 {
		concurrency = 1
	}
	var mu sync.Mutex
	var out []CheckResult
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	run := func(c adapter.Connection, read func(context.Context) (string, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			meta := conns.Meta[c.ID()]
			r := CheckResult{ID: c.ID(), Name: meta.Name, Kind: c.Kind(), URL: meta.BaseURL, Step: "probe"}
			start := time.Now()
			cctx, cancel := context.WithTimeout(ctx, 3*timeout)
			defer cancel()
			p, err := c.Probe(cctx)
			if err == nil {
				r.Probe = &p
				if p.Compatibility == adapter.CompatUnsupported {
					err = &adapter.Error{Class: adapter.ErrUnsupported, Op: "probe", Message: p.CompatNote}
				} else {
					r.Step = "read"
					r.Summary, err = read(cctx)
				}
			}
			r.Duration = time.Since(start)
			if err != nil {
				r.ErrorClass, r.Error = string(adapter.ClassOf(err)), errorText(err)
			} else {
				r.OK = true
			}
			mu.Lock()
			out = append(out, r)
			mu.Unlock()
		}()
	}
	for _, c := range conns.Clients {
		c := c
		run(c, func(ctx context.Context) (string, error) {
			s, err := c.ListTransfers(ctx)
			if err != nil {
				return "", err
			}
			incomplete := 0
			for _, t := range s.Transfers {
				if t.StateClass != "complete" {
					incomplete++
				}
			}
			return fmt.Sprintf("%d transfers listed (%d incomplete)", len(s.Transfers), incomplete), nil
		})
	}
	for _, m := range conns.Managers {
		m := m
		run(m, func(ctx context.Context) (string, error) {
			q, err := m.Queue(ctx)
			if err != nil {
				return "", err
			}
			h, err := m.Health(ctx)
			if err != nil {
				return "", err
			}
			issues := 0
			for _, it := range h.Items {
				if it.Type == "warning" || it.Type == "error" {
					issues++
				}
			}
			s := fmt.Sprintf("%d queue rows, %d health warnings/errors", len(q.Items), issues)
			if !q.Complete {
				s += " (queue listing incomplete)"
			}
			return s, nil
		})
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
