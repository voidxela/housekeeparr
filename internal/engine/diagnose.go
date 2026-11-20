package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/store"
)

// clientRefused reports whether the client currently reports that a
// tracker refuses the client or its version.
func clientRefused(t domain.Transfer) bool {
	v, ok := t.SourceFailure.Get()
	return ok && v == domain.SourceClientRejected
}

// payloadComplete reports whether every wanted byte is positively verified.
func payloadComplete(t domain.Transfer) bool {
	w, okW := t.WantedBytes.Get()
	v, okV := t.VerifiedBytes.Get()
	return okW && okV && w > 0 && v >= w
}

func trailingRefusals(obs []store.Observation) int {
	n := 0
	for i := len(obs) - 1; i >= 0; i-- {
		if v, ok := obs[i].SourceFailure.Get(); !ok || v != domain.SourceClientRejected {
			break
		}
		n++
	}
	return n
}

// diagnoseTransfer adds the diagnoses that also apply to complete
// transfers: a tracker refusing the client, and a complete payload the
// manager does not import because the client reports an error. obs is nil
// for a complete transfer, which is not observed continuously; then the
// latest listing is the evidence.
func (e *Engine) diagnoseTransfer(ev *findings.Evaluation, reasons map[string]string, active map[string]*domain.Finding, base string,
	meta ConnMeta, t domain.Transfer, obs []store.Observation, queue []store.QueueRecord, now time.Time) {
	// Tracker refuses the client.
	key := base + kindClientRefused
	refusedFor := 0
	if clientRefused(t) {
		refusedFor = 1
		if obs != nil {
			refusedFor = trailingRefusals(obs)
		}
	}
	switch v, known := t.SourceFailure.Get(); {
	case refusedFor >= 2 || (obs == nil && refusedFor == 1):
		ev.Drafts = append(ev.Drafts, e.clientRefusedDraft(key, meta, t, obs, queue, refusedFor, now))
	case active[key] != nil && known && v != domain.SourceClientRejected:
		reasons[key] = "The tracker no longer refuses the client (tracker result now " + string(v) + ")."
	case active[key] != nil && obs == nil && !known:
		reasons[key] = "The download completed, and the client reports no tracker result for it (" + t.SourceFailure.String() + "); no refusal is in evidence."
	case active[key] != nil:
		ev.Keep[key] = true
	}

	// Complete payload not imported.
	key = base + kindCompleteBlocked
	blocked, why := completeBlocked(t, queue)
	switch {
	case blocked:
		ev.Drafts = append(ev.Drafts, e.completeBlockedDraft(key, meta, t, queue, why, now))
	case active[key] != nil && len(queue) == 0:
		reasons[key] = "No media-manager queue item tracks the download any more (imported or removed)."
	case active[key] != nil && !payloadComplete(t):
		reasons[key] = "The client no longer reports every wanted byte as verified."
	case active[key] != nil:
		reasons[key] = "The client no longer reports an error, or the manager no longer tracks the grab as downloading."
	}
}

// completeBlocked: every wanted byte verified, the client reports an error
// (an error state, or a tracker failure with its message), and a manager
// still tracks the grab as downloading.
func completeBlocked(t domain.Transfer, queue []store.QueueRecord) (bool, string) {
	if !payloadComplete(t) || len(queue) == 0 {
		return false, ""
	}
	msg, _ := t.ClientMessage.Get()
	sf, sfOK := t.SourceFailure.Get()
	var why string
	switch {
	case t.StateClass == domain.StateError:
		why = "the client reports an error state (" + t.NativeState + ")"
	case sfOK && sf != domain.SourceNoFailure && strings.TrimSpace(msg) != "":
		why = "the client reports a tracker failure (" + string(sf) + ")"
	default:
		return false, ""
	}
	for _, q := range queue {
		if ts, ok := q.Latest.TrackedState.Get(); !ok || !strings.EqualFold(ts, "downloading") {
			return false, ""
		}
	}
	return true, why
}

func (e *Engine) clientRefusedDraft(key string, meta ConnMeta, t domain.Transfer, obs []store.Observation, queue []store.QueueRecord, n int, now time.Time) findings.Draft {
	managers, links := e.managerLinks(queue)
	msg := signalText(t.ClientMessage, func(m string) string { return nonEmptyText(m, "none") })
	confidence := fmt.Sprintf("Reported by the client from the tracker's response on %d consecutive observations.", n)
	if obs == nil {
		confidence = "Reported by the client from the tracker's response in the latest listing (complete downloads are not observed continuously)."
	}
	steps := []string{
		fmt.Sprintf("Upgrade %s to a release this tracker accepts (check the tracker's list of allowed clients); Connections shows the versions Housekeeparr has tested.", meta.Name),
		"Other trackers, DHT or peer exchange may still serve this torrent; nothing about the release itself is wrong.",
	}
	if payloadComplete(t) {
		steps = append(steps, "The payload is complete. If the media manager still shows it as downloading, import it manually there.")
	}
	return findings.Draft{
		DedupKey: key, Type: domain.FindingTrackerRejectsClient, Severity: domain.SeverityWarning,
		Title:   fmt.Sprintf("Tracker refuses %s: %s", meta.Name, t.Name),
		Summary: "A tracker refuses this client or its version. This is not a dead release, so Housekeeparr does not fail the grab or search for a replacement.",
		Subject: domain.Subject{Connections: append([]string{meta.ID}, managers...), ClientID: meta.ID, NativeID: t.NativeID,
			TransferName: t.Name, Content: contentLabels(queue)},
		Detail: domain.FindingDetail{
			Evidence: []domain.Evidence{
				{Label: "Tracker result", Value: string(domain.SourceClientRejected) + " — " + msg, At: &now},
				{Label: "Client state", Value: t.NativeState},
				{Label: "Verified / wanted", Value: signalText(t.VerifiedBytes, bytesText) + " / " + signalText(t.WantedBytes, bytesText)},
			},
			Confidence: confidence,
			Limitations: []string{
				"Only tracker messages that name the client and a refusal are classified this way; other wording stays an ordinary tracker error.",
				"Where trackers disagree (one refuses the client, another works), no refusal is reported. qBittorrent's tracker results are read only for active downloads.",
			},
			Eligibility: e.modeEligibility("No recovery workflow applies: a tracker refusing the client is not a dead release."),
			NextSteps:   steps,
			Links:       links,
		},
		EvidenceAt: now,
	}
}

func (e *Engine) completeBlockedDraft(key string, meta ConnMeta, t domain.Transfer, queue []store.QueueRecord, why string, now time.Time) findings.Draft {
	managers, links := e.managerLinks(queue)
	mgr := e.conns.Meta[queue[0].ManagerID].Name
	msg := signalText(t.ClientMessage, func(m string) string { return nonEmptyText(m, "none") })
	steps := []string{
		fmt.Sprintf("Open the torrent in %s and clear the error (for a tracker refusing the client, upgrade the client); %s imports once the client reports the download complete.", meta.Name, mgr),
		fmt.Sprintf("Or import the files manually in %s: the payload is complete.", mgr),
	}
	if clientRefused(t) {
		steps = append(steps, "A tracker refusing the client is not a dead release; Housekeeparr does not fail this grab.")
	}
	return findings.Draft{
		DedupKey: key, Type: domain.FindingCompleteNotImported, Severity: domain.SeverityWarning,
		Title:   "Complete but not imported: " + t.Name,
		Summary: fmt.Sprintf("Every wanted byte is verified, but %s, so %s still tracks the grab as downloading and does not import it.", why, mgr),
		Subject: domain.Subject{Connections: append([]string{meta.ID}, managers...), ClientID: meta.ID, NativeID: t.NativeID,
			TransferName: t.Name, Content: contentLabels(queue)},
		Detail: domain.FindingDetail{
			Evidence: []domain.Evidence{
				{Label: "Verified / wanted", Value: signalText(t.VerifiedBytes, bytesText) + " / " + signalText(t.WantedBytes, bytesText), At: &now},
				{Label: "Client state", Value: t.NativeState},
				{Label: "Client message", Value: msg},
				{Label: "Manager state", Value: fmt.Sprintf("%s tracks %d item(s) as downloading", mgr, len(queue))},
			},
			Confidence:  "Reported by the client (verified bytes and error) and the media manager (tracked state) in the latest reads.",
			Limitations: []string{"Housekeeparr does not import, move or modify files; a complete payload is never treated as a dead release."},
			Eligibility: e.modeEligibility("No recovery workflow applies to a complete payload."),
			NextSteps:   steps,
			Links:       links,
		},
		EvidenceAt: now,
	}
}

func bytesText(n int64) string { return fmt.Sprintf("%d bytes", n) }
