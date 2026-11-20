package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// payloadReadsPerCycle bounds client file-list requests per cycle, across
// clients. Only settled import candidates are read, each once while it
// remains a candidate.
const payloadReadsPerCycle = 3

type payloadRecord struct {
	contents domain.PayloadContents
	at       time.Time
}

func payloadKey(clientID, nativeID string) string { return clientID + "|" + nativeID }

// readPayloads lists the files of settled import candidates: queue rows the
// manager has reported as import blocked or pending with a warning for at
// least the import settling period, correlated with a transfer on a client
// that lists files and was read completely this cycle. Results are kept
// while the row remains a candidate; a failed read is retried next cycle.
// Nothing is written to the store.
func (e *Engine) readPayloads(ctx context.Context, results []*fetched, settle time.Duration, now time.Time) {
	fresh := map[string]adapter.FileLister{}
	for _, f := range results {
		if f.err != nil || f.transfer == nil || !f.transfer.Complete {
			continue
		}
		for _, c := range e.conns.Clients {
			if fl, ok := c.(adapter.FileLister); ok && c.ID() == f.id {
				fresh[f.id] = fl
			}
		}
	}
	rows, err := e.st.QueueItems(ctx, store.QueueFilter{PresentOnly: true})
	if err != nil {
		return
	}
	want := map[string]bool{}
	var todo []store.QueueRecord
	for _, r := range rows {
		if (r.AttentionState != "importBlocked" && r.AttentionState != "importPending") || r.AttentionSince == nil ||
			now.Sub(*r.AttentionSince) < settle || r.MatchStatus != store.MatchMatched || r.MatchNativeID == "" {
			continue
		}
		k := payloadKey(r.MatchClientID, r.MatchNativeID)
		if want[k] {
			continue
		}
		want[k] = true
		e.mu.Lock()
		_, have := e.payloads[k]
		e.mu.Unlock()
		if !have && fresh[r.MatchClientID] != nil {
			todo = append(todo, r)
		}
	}
	e.mu.Lock()
	for k := range e.payloads {
		if !want[k] {
			delete(e.payloads, k)
		}
	}
	e.mu.Unlock()
	for i, r := range todo {
		if i >= payloadReadsPerCycle {
			e.log.Info("file lists deferred to later cycles", "deferred", len(todo)-i, "limit", payloadReadsPerCycle)
			break
		}
		files, err := fresh[r.MatchClientID].Files(ctx, r.MatchNativeID)
		if err != nil {
			e.log.Warn("could not list a download's files; the import finding stays without them", "client", r.MatchClientID, "err", errorText(err))
			continue
		}
		e.mu.Lock()
		e.payloads[payloadKey(r.MatchClientID, r.MatchNativeID)] = payloadRecord{contents: domain.ClassifyPayload(files), at: now}
		e.mu.Unlock()
	}
}

// payloadAdvice is the evidence and next step for an import finding whose
// download's files were listed.
func (e *Engine) payloadAdvice(items []store.QueueRecord, managerName string) (*domain.Evidence, string, string) {
	if len(items) == 0 || items[0].MatchClientID == "" {
		return nil, "", ""
	}
	e.mu.Lock()
	p, ok := e.payloads[payloadKey(items[0].MatchClientID, items[0].MatchNativeID)]
	e.mu.Unlock()
	if !ok {
		return nil, "", ""
	}
	at := p.at
	ev := &domain.Evidence{Label: "Download contents", Value: string(p.contents.Kind) + ": " + p.contents.Describe(), At: &at,
		Source: e.conns.Meta[items[0].MatchClientID].Name}
	switch p.contents.Kind {
	case domain.PayloadArchiveOnly:
		return ev, "The download holds archives and no video file, so it needs extracting before it can be imported.",
			fmt.Sprintf("Extract the archives (for example with an unpacking tool that %s can wait for), then let %s import; Housekeeparr never extracts or changes files.", managerName, managerName)
	case domain.PayloadSampleOnly:
		return ev, "The only video files are samples, so there is nothing full-length to import.",
			fmt.Sprintf("Treat the release as incomplete: remove or replace it in %s; Housekeeparr does not do this for an import problem.", managerName)
	case domain.PayloadMixed:
		return ev, "The download holds a full video beside archives; the archives are not necessarily the cause.",
			fmt.Sprintf("Check %s's message for the video; the archives may be extras.", managerName)
	}
	return ev, "", ""
}
