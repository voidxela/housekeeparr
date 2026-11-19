package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

func maxDuration(ds ...time.Duration) time.Duration {
	var m time.Duration
	for _, d := range ds {
		if d > m {
			m = d
		}
	}
	return m
}

// TransferScope returns the finding scope for a client.
func TransferScope(clientID string) string { return "transfer:" + clientID + "|" }

func (e *Engine) evaluateClient(tx *store.Tx, snap domain.TransferSnapshot, gone []store.GoneTransfer, now time.Time) (findings.Result, error) {
	clientEff, err := e.cfg.EffectivePolicy(snap.ClientID)
	if err != nil {
		return findings.Result{}, err
	}
	meta := e.conns.Meta[snap.ClientID]
	// Only incomplete transfers are evaluated, so only they are loaded.
	recs, err := tx.Transfers(store.TransferFilter{ClientID: snap.ClientID, PresentOnly: true})
	if err != nil {
		return findings.Result{}, err
	}
	byID := map[string]store.TransferRecord{}
	for _, r := range recs {
		byID[r.NativeID] = r
	}
	scoped := e.cfg.ScopedSettings()
	scope := TransferScope(snap.ClientID)
	ev := findings.Evaluation{Scope: scope, Keep: map[string]bool{}}
	reasons := map[string]string{} // dedup key → verified resolution reason
	listed := map[string]bool{}
	next := now.Add(clientEff.Settings.PollInterval)
	coverageKey := scope + "tracker_coverage"
	switch {
	case snap.TrackerReadsDeferred > 0:
		ev.Drafts = append(ev.Drafts, findings.Draft{
			DedupKey: coverageKey, Type: domain.FindingTrackerCoverage, Severity: domain.SeverityWarning,
			Title: fmt.Sprintf("Tracker checks reached their limit on %s", meta.Name),
			Summary: fmt.Sprintf("%d active downloads had no tracker result checked in this listing (limit %d).",
				snap.TrackerReadsDeferred, snap.TrackerReadLimit),
			Subject:    domain.Subject{ClientID: snap.ClientID, Connections: []string{snap.ClientID}},
			EvidenceAt: now, NextEvaluationAt: &next,
			Detail: domain.FindingDetail{
				NextSteps:   []string{"Reduce simultaneous active downloads, or raise tracker_read_limit for this client if its API has capacity."},
				Limitations: []string{"Deferred tracker reads provide no evidence of tracker rejection or tracker health."},
			},
		})
	case snap.Complete && snap.TrackerReadLimit > 0:
		reasons[coverageKey] = "The latest complete listing deferred no tracker checks."
	default:
		ev.Keep[coverageKey] = true
	}
	// Load active findings and correlated queue rows once, not per transfer.
	activeList, err := tx.ActiveFindingsWithPrefix(scope)
	if err != nil {
		return findings.Result{}, err
	}
	activeByKey := make(map[string]*domain.Finding, len(activeList))
	for i := range activeList {
		activeByKey[activeList[i].DedupKey] = &activeList[i]
	}
	allQueue, err := tx.QueueItems(store.QueueFilter{PresentOnly: true})
	if err != nil {
		return findings.Result{}, err
	}
	queueByTransfer := map[string][]store.QueueRecord{}
	for _, q := range allQueue {
		if q.MatchClientID == snap.ClientID && q.MatchNativeID != "" {
			queueByTransfer[q.MatchNativeID] = append(queueByTransfer[q.MatchNativeID], q)
		}
	}

	for _, t := range snap.Transfers {
		listed[t.NativeID] = true
		base := scope + t.NativeID + "|"
		if t.StateClass == domain.StateComplete {
			for _, k := range []string{kindStalled, kindMetadata, kindError, kindSource} {
				reasons[base+k] = fmt.Sprintf("Verified: the download completed (client state %s).", t.NativeState)
			}
			continue
		}
		rec := byID[t.NativeID]
		queue := queueByTransfer[t.NativeID]
		// Diagnosis uses the transfer's own effective policy: scoped rules
		// and item overrides (a longer window for a rare release, say) apply
		// to the diagnosis exactly as they do to the action planner.
		eff, s := clientEff, clientEff.Settings
		if scoped {
			var assoc []policy.Association
			for _, q := range queue {
				assoc = append(assoc, policy.Association{ManagerID: q.ManagerID, Item: q.Latest})
			}
			t := t
			if eff, _, err = e.cfg.ResolveTransfer(policy.TransferSubject(snap.ClientID, t.NativeID, &t, assoc)); err != nil {
				return findings.Result{}, err
			}
			s = eff.Settings
		}
		lookback := 2*maxDuration(s.NoProgressWindow, s.NearCompleteWindow, s.MetadataTimeout) + s.NewDownloadGrace + s.MaxObservationGap
		obs, err := tx.Observations(snap.ClientID, t.NativeID, now.Add(-lookback), 5000)
		if err != nil {
			return findings.Result{}, err
		}
		samples := make([]policy.Sample, 0, len(obs))
		for _, o := range obs {
			samples = append(samples, o.Sample())
		}
		firstSeen := rec.FirstSeen
		if firstSeen.IsZero() {
			firstSeen = now
		}
		a := policy.EvaluateTransfer(policy.TransferInput{AddedAt: t.AddedAt, FirstSeen: firstSeen, Samples: samples,
			AvailabilityMeaning: t.Availability.Note}, s, now)
		if err := tx.SetAssessment(snap.ClientID, t.NativeID, a, now); err != nil {
			return findings.Result{}, err
		}
		active := func(kind string) (*domain.Finding, error) { return activeByKey[base+kind], nil }
		carry := func(f *domain.Finding, o transferOutcome) {
			d := carryDraft(*f, o, a.EvidenceAt, &next)
			ev.Drafts = append(ev.Drafts, d)
		}

		// Stall.
		stall, err := active(kindStalled)
		if err != nil {
			return findings.Result{}, err
		}
		qualifiedStall := a.Status == policy.StatusQualified &&
			(a.Diagnosis == policy.DiagnosisNoProgress || a.Diagnosis == policy.DiagnosisNoProgressUnavailable)
		switch {
		case qualifiedStall:
			d := e.stalledDraft(base+kindStalled, meta, eff, t, a, queue, now)
			if stall != nil && stall.Detail.Baseline != nil {
				d.Detail.Baseline = stall.Detail.Baseline
			}
			ev.Drafts = append(ev.Drafts, d)
		case stall != nil:
			if o := decideStall(*stall, t, a, s); o.resolve {
				reasons[base+kindStalled] = o.reason
			} else {
				carry(stall, o)
			}
		}

		// Metadata timeout.
		meta2, err := active(kindMetadata)
		if err != nil {
			return findings.Result{}, err
		}
		switch {
		case a.Status == policy.StatusQualified && a.Diagnosis == policy.DiagnosisMetadataTimeout:
			ev.Drafts = append(ev.Drafts, e.metadataDraft(base+kindMetadata, meta, eff, t, a, queue, now))
		case meta2 != nil:
			if o := decideMetadata(t, a); o.resolve {
				reasons[base+kindMetadata] = o.reason
			} else {
				carry(meta2, o)
			}
		}

		// Source failure (SPEC §7.2).
		srcF, err := active(kindSource)
		if err != nil {
			return findings.Result{}, err
		}
		switch {
		case a.Status == policy.StatusQualified && a.Diagnosis == policy.DiagnosisSourceUnregistered:
			ev.Drafts = append(ev.Drafts, e.sourceDraft(base+kindSource, meta, eff, t, a, queue, now))
		case srcF != nil:
			if o := decideSource(t, obs, a, s); o.resolve {
				reasons[base+kindSource] = o.reason
			} else {
				carry(srcF, o)
			}
		}

		// Client-reported error.
		errF, err := active(kindError)
		if err != nil {
			return findings.Result{}, err
		}
		switch {
		case t.StateClass == domain.StateError && consecutiveErrors(obs) >= 2:
			ev.Drafts = append(ev.Drafts, e.clientErrorDraft(base+kindError, meta, t, obs, queue, now))
		case errF != nil:
			if o := decideError(t, obs, a, s); o.resolve {
				reasons[base+kindError] = o.reason
			} else {
				carry(errF, o)
			}
		}
	}
	if err := tx.ClearCompleteAssessments(snap.ClientID); err != nil {
		return findings.Result{}, err
	}

	actives := activeList
	goneSet := map[string]bool{}
	for _, g := range gone {
		goneSet[g.NativeID] = true
	}
	drafted := map[string]bool{}
	for _, d := range ev.Drafts {
		drafted[d.DedupKey] = true
	}
	for _, f := range actives {
		if drafted[f.DedupKey] || reasons[f.DedupKey] != "" {
			continue
		}
		id := f.Subject.NativeID
		// Only a complete listing that omits the transfer establishes
		// disappearance. Anything else keeps the finding unchanged.
		if listed[id] || !snap.Complete {
			ev.Keep[f.DedupKey] = true
		}
	}
	ev.Resolve = func(f domain.Finding) findings.Resolution {
		if r := reasons[f.DedupKey]; r != "" {
			return findings.Resolution{Reason: r}
		}
		when := "in an earlier complete listing"
		if goneSet[f.Subject.NativeID] {
			when = "in the complete listing at " + now.UTC().Format(time.RFC3339)
		}
		return findings.Resolution{
			Reason:     fmt.Sprintf("The transfer is no longer present in %s (absent %s).", meta.Name, when),
			Limitation: "Housekeeparr cannot tell whether it was removed by a user, by a media manager, or by the client.",
		}
	}
	return findings.Apply(tx, ev, now)
}

// carryDraft keeps an open finding's diagnosis and evidence while recording
// how the situation changed.
func carryDraft(f domain.Finding, o transferOutcome, evidenceAt time.Time, next *time.Time) findings.Draft {
	d := findings.Draft{
		DedupKey: f.DedupKey, Type: f.Type, Severity: f.Severity, Title: f.Title, Summary: f.Summary,
		Subject: f.Subject, Detail: f.Detail, PolicyID: f.PolicyID, PolicyRevision: f.PolicyRevision,
		EvidenceAt: evidenceAt, NextEvaluationAt: next,
	}
	if evidenceAt.IsZero() {
		d.EvidenceAt = f.EvidenceAt
	}
	d.Detail.CurrentState = o.note
	if o.baseline != nil {
		d.Detail.Baseline = o.baseline
	}
	return d
}

func consecutiveErrors(obs []store.Observation) int {
	n := 0
	for i := len(obs) - 1; i >= 0 && obs[i].StateClass == domain.StateError; i-- {
		n++
	}
	return n
}

// unverifiedRecovery states the recovery limitation until a version-tested
// preservation workflow exists.
const unverifiedRecovery = "Recovery semantics (media-manager queue removal, blocklisting, replacement-search ownership, and whether the torrent, its data, its tracker registration, and its active slot are retained) have not been verified for any version, so Housekeeparr proposes no action and makes no preservation claim."

func (e *Engine) modeEligibility(extra ...string) string {
	var b strings.Builder
	switch e.cfg.Mode {
	case config.ModePaused:
		b.WriteString("Paused: no automation will start. ")
	default:
		b.WriteString("Observe-only: Housekeeparr will not change anything in the media manager or the client. ")
	}
	b.WriteString("Automatic recovery is not implemented in this release.")
	for _, x := range extra {
		b.WriteString(" ")
		b.WriteString(x)
	}
	return b.String()
}

func contentLabels(queue []store.QueueRecord) []string {
	seen := map[string]bool{}
	var out []string
	for _, q := range queue {
		l := q.Latest.ContentLabel()
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) managerLinks(queue []store.QueueRecord) ([]string, []domain.Link) {
	var ids []string
	var links []domain.Link
	seen := map[string]bool{}
	for _, q := range queue {
		if seen[q.ManagerID] {
			continue
		}
		seen[q.ManagerID] = true
		ids = append(ids, q.ManagerID)
		if m, ok := e.conns.Meta[q.ManagerID]; ok {
			links = append(links, domain.Link{Label: m.Name + " queue", URL: m.BaseURL + "/activity/queue"})
		}
	}
	return ids, links
}

func pct(v, w int64) string {
	if w <= 0 {
		return "?"
	}
	return fmt.Sprintf("%.1f%%", float64(v)*100/float64(w))
}

// HumanBytes renders a byte count with binary units.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func signalText[T any](s domain.Signal[T], format func(T) string) string {
	switch s.Status {
	case domain.Known:
		return format(s.Value)
	case domain.Unsupported:
		return "not reported by this client version"
	default:
		if s.Note != "" {
			return "unknown (" + s.Note + ")"
		}
		return "unknown"
	}
}

func intText(n int64) string { return fmt.Sprintf("%d", n) }

func transferEvidence(t domain.Transfer, a policy.Assessment, at time.Time) []domain.Evidence {
	ev := []domain.Evidence{
		{Label: "Client state", Value: fmt.Sprintf("%s (%s)", t.StateClass.Describe(), t.NativeState), At: &at},
	}
	if w, ok := t.WantedBytes.Get(); ok {
		if v, ok := t.VerifiedBytes.Get(); ok {
			ev = append(ev, domain.Evidence{Label: "Verified data", Value: fmt.Sprintf("%s of %s wanted (%s)", HumanBytes(v), HumanBytes(w), pct(v, w))})
		}
	} else {
		ev = append(ev, domain.Evidence{Label: "Verified data", Value: signalText(t.VerifiedBytes, func(v int64) string { return HumanBytes(v) })})
	}
	if a.LastProgressAt != nil {
		lp := *a.LastProgressAt
		ev = append(ev, domain.Evidence{Label: "Last verified progress", Value: lp.UTC().Format(time.RFC3339), At: &lp})
	} else if !a.StreakStart.IsZero() {
		ev = append(ev, domain.Evidence{Label: "Last verified progress", Value: "none observed since " + a.StreakStart.UTC().Format(time.RFC3339)})
	}
	if a.Window > 0 {
		ev = append(ev, domain.Evidence{Label: "Qualifying evidence", Value: fmt.Sprintf("%d observations, %s counted of %s required (%s)",
			a.Observations, policy.HumanDuration(a.Elapsed), policy.HumanDuration(a.Window), a.WindowReason)})
	}
	ev = append(ev,
		domain.Evidence{Label: "Grace period", Value: fmt.Sprintf("ended %s (from %s)", a.GraceEnds.UTC().Format(time.RFC3339), a.GraceFrom)},
		domain.Evidence{Label: "Connected seeds / peers", Value: signalText(t.ConnectedSeeds, intText) + " / " + signalText(t.ConnectedPeers, intText)},
		domain.Evidence{Label: "Swarm seeds / peers (tracker-reported, may be stale)", Value: signalText(t.SwarmSeeds, intText) + " / " + signalText(t.SwarmPeers, intText)},
		domain.Evidence{Label: "Piece availability", Value: signalText(t.Availability, func(v float64) string { return fmt.Sprintf("%.3f distributed copies", v) }) + " — " + string(a.Availability)},
		domain.Evidence{Label: "Download rate (instantaneous)", Value: signalText(t.DownloadRate, func(v int64) string { return HumanBytes(v) + "/s" })},
		domain.Evidence{Label: "Privacy", Value: signalText(t.Private, func(p bool) string {
			if p {
				return "private"
			}
			return "public"
		})},
	)
	if len(a.Resets) > 0 {
		r := a.Resets[len(a.Resets)-1]
		rt := r.At
		ev = append(ev, domain.Evidence{Label: "Most recent baseline reset", Value: r.Reason, At: &rt})
	}
	return ev
}

func missingSignals(t domain.Transfer) []string {
	var out []string
	check := func(name string, st domain.SignalStatus, note string) {
		if st != domain.Known {
			s := name + " is " + string(st)
			if note != "" {
				s += " (" + note + ")"
			}
			out = append(out, s)
		}
	}
	check("Piece availability", t.Availability.Status, t.Availability.Note)
	check("Privacy", t.Private.Status, t.Private.Note)
	check("File selection", t.AllFilesWanted.Status, t.AllFilesWanted.Note)
	check("Added time", t.AddedAt.Status, t.AddedAt.Note)
	return out
}

func (e *Engine) stalledDraft(key string, meta ConnMeta, eff policy.Effective, t domain.Transfer, a policy.Assessment, queue []store.QueueRecord, now time.Time) findings.Draft {
	title := "No verified progress: " + t.Name
	if a.Diagnosis == policy.DiagnosisNoProgressUnavailable {
		title = "No verified progress, pieces missing among connected peers: " + t.Name
	}
	managers, links := e.managerLinks(queue)
	privacyNote := "Private and unknown-privacy transfers will default to observation even when recovery exists."
	if p, ok := t.Private.Get(); ok && !p {
		privacyNote = ""
	}
	next := now.Add(eff.Settings.PollInterval)
	steps := []string{
		fmt.Sprintf("Inspect the torrent in %s: tracker messages, the peer list, and whether any connected peer has the pieces it still needs.", meta.Name),
		"If it is slow but alive, no action is needed; Housekeeparr re-evaluates on every poll and resolves this finding on verified progress.",
	}
	if len(queue) > 0 {
		mgr := e.conns.Meta[queue[0].ManagerID].Name
		steps = append(steps, fmt.Sprintf("Review the item in %s → Activity → Queue. Before removing or blocklisting anything manually, confirm in your %s version's documentation what each removal option does to the torrent, its data, and future searches — Housekeeparr has not verified those effects.", mgr, mgr))
	} else {
		steps = append(steps, "No media-manager queue item is associated with this transfer.")
	}
	return findings.Draft{
		DedupKey: key, Type: domain.FindingTransferStalled, Severity: domain.SeverityWarning, Title: title,
		Summary: a.Headline,
		Subject: domain.Subject{Connections: append([]string{meta.ID}, managers...), ClientID: meta.ID, NativeID: t.NativeID,
			TransferName: t.Name, Content: contentLabels(queue)},
		Detail: domain.FindingDetail{
			Evidence: transferEvidence(t, a, now),
			Missing:  missingSignals(t),
			Confidence: fmt.Sprintf("Established from %d consecutive successful observations with no gap over %s; verified (hash-checked) bytes are the progress measure, not network traffic.",
				a.Observations, policy.HumanDuration(eff.Settings.MaxObservationGap)),
			Limitations: append(append([]string{}, a.Limitations...),
				"A stalled torrent is not provably dead; an intermittent swarm may still complete.",
				unverifiedRecovery),
			Baseline:    stallBaseline(t),
			Eligibility: e.modeEligibility(privacyNote),
			NextSteps:   steps,
			Links:       links,
		},
		PolicyID: eff.ID, PolicyRevision: eff.Revision, EvidenceAt: a.EvidenceAt, NextEvaluationAt: &next,
	}
}

func (e *Engine) metadataDraft(key string, meta ConnMeta, eff policy.Effective, t domain.Transfer, a policy.Assessment, queue []store.QueueRecord, now time.Time) findings.Draft {
	managers, links := e.managerLinks(queue)
	next := now.Add(eff.Settings.PollInterval)
	return findings.Draft{
		DedupKey: key, Type: domain.FindingMetadataTimeout, Severity: domain.SeverityWarning,
		Title:   "Metadata not acquired: " + t.Name,
		Summary: a.Headline,
		Subject: domain.Subject{Connections: append([]string{meta.ID}, managers...), ClientID: meta.ID, NativeID: t.NativeID,
			TransferName: t.Name, Content: contentLabels(queue)},
		Detail: domain.FindingDetail{
			Evidence: []domain.Evidence{
				{Label: "Client state", Value: fmt.Sprintf("%s (%s)", t.StateClass.Describe(), t.NativeState)},
				{Label: "Waiting for metadata since", Value: a.StreakStart.UTC().Format(time.RFC3339)},
				{Label: "Qualifying evidence", Value: fmt.Sprintf("%d observations, %s of %s timeout", a.Observations, policy.HumanDuration(a.Elapsed), policy.HumanDuration(a.Window))},
				{Label: "Connected seeds / peers", Value: signalText(t.ConnectedSeeds, intText) + " / " + signalText(t.ConnectedPeers, intText)},
			},
			Confidence: "Established from consecutive metadata-state observations reported by the client.",
			Limitations: append(append([]string{}, a.Limitations...),
				"Magnet links depend on peers, trackers, or DHT to supply metadata; a magnet that cannot reach any of them may never resolve.",
				unverifiedRecovery),
			Eligibility: e.modeEligibility(),
			NextSteps: []string{
				fmt.Sprintf("Inspect the magnet in %s: tracker messages and whether any peer is connected.", meta.Name),
				"Review the item in the media manager's Activity → Queue. Before removing or blocklisting anything manually, confirm in that version's documentation what each removal option does — Housekeeparr has not verified those effects.",
			},
			Links: links,
		},
		PolicyID: eff.ID, PolicyRevision: eff.Revision, EvidenceAt: a.EvidenceAt, NextEvaluationAt: &next,
	}
}

func (e *Engine) clientErrorDraft(key string, meta ConnMeta, t domain.Transfer, obs []store.Observation, queue []store.QueueRecord, now time.Time) findings.Draft {
	managers, links := e.managerLinks(queue)
	explain := "The client reports an error for this download."
	steps := []string{fmt.Sprintf("Open the torrent in %s to see the error details.", meta.Name)}
	if t.NativeState == "missingFiles" || t.NativeState == "local-error" {
		explain = "The client reports a local problem with this download's files. This usually means an unmounted or moved download location, a full disk, or a permission problem — not a problem with the release."
		steps = append(steps, "Confirm that the download location is mounted, has free space, and is writable by the client, then ask the client to recheck (verify) the local data.")
	} else {
		steps = append(steps, "Check free disk space and permissions on the download location.")
	}
	since := obs[len(obs)-consecutiveErrors(obs)].At
	return findings.Draft{
		DedupKey: key, Type: domain.FindingTransferError, Severity: domain.SeverityError,
		Title:   fmt.Sprintf("%s reports an error: %s", meta.Name, t.Name),
		Summary: explain,
		Subject: domain.Subject{Connections: append([]string{meta.ID}, managers...), ClientID: meta.ID, NativeID: t.NativeID,
			TransferName: t.Name, Content: contentLabels(queue)},
		Detail: domain.FindingDetail{
			Evidence: []domain.Evidence{
				{Label: "Client state", Value: t.NativeState, At: &now},
				{Label: "In error since", Value: since.UTC().Format(time.RFC3339), At: &since},
				{Label: "Consecutive error observations", Value: fmt.Sprintf("%d", consecutiveErrors(obs))},
				{Label: "Client message", Value: signalText(t.ClientMessage, func(m string) string {
					if m == "" {
						return "none"
					}
					return m
				})},
			},
			Missing:     errorDetailMissing(t),
			Confidence:  "Reported directly by the client on consecutive observations.",
			Eligibility: e.modeEligibility("Storage problems are never treated as a failed release."),
			NextSteps:   steps,
			Links:       links,
		},
		EvidenceAt: now,
	}
}

// attentionState returns the import-attention state of a queue row.
func attentionState(q domain.QueueItem) string {
	ts, _ := q.TrackedState.Get()
	st, _ := q.TrackedStatus.Get()
	switch ts {
	case "importBlocked", "importPending":
		if st == "warning" || st == "error" {
			return ts
		}
	case "failedPending":
		return ts
	}
	return ""
}

func (e *Engine) evaluateQueue(tx *store.Tx, managerID string, snap domain.QueueSnapshot, now time.Time) (findings.Result, error) {
	meta := e.conns.Meta[managerID]
	global, err := e.cfg.EffectivePolicy("")
	if err != nil {
		return findings.Result{}, err
	}
	settle := global.Settings.ImportSettle
	items, err := tx.QueueItems(store.QueueFilter{ManagerID: managerID, PresentOnly: true})
	if err != nil {
		return findings.Result{}, err
	}
	scope := "queue:" + managerID + "|"
	ev := findings.Evaluation{Scope: scope, Keep: map[string]bool{}}
	type group struct {
		items []store.QueueRecord
		state string
		since time.Time
	}
	imports := map[string]*group{}
	unmatched := map[string][]store.QueueRecord{}
	ambiguous := map[string][]store.QueueRecord{}
	presentDL := map[string]bool{}
	var order []string
	for _, it := range items {
		dl := it.DownloadID
		if dl == "" {
			dl = it.ItemKey
		}
		presentDL[dl] = true
		state := attentionState(it.Latest)
		var since *time.Time
		if state != "" {
			s := now
			if it.AttentionState == state && it.AttentionSince != nil {
				s = *it.AttentionSince
			}
			since = &s
		}
		if err := tx.SetAttention(managerID, it.ItemKey, state, since); err != nil {
			return findings.Result{}, err
		}
		if state != "" && now.Sub(*since) >= settle {
			g := imports[dl]
			if g == nil {
				g = &group{state: state, since: *since}
				imports[dl] = g
				order = append(order, dl)
			}
			g.items = append(g.items, it)
			if since.Before(g.since) {
				g.since = *since
			}
		} else if state != "" {
			ev.Keep[scope+"import|"+dl] = true // settling; neither open nor resolve
		}
		switch it.MatchStatus {
		case store.MatchNotFound:
			if it.MatchSince != nil && now.Sub(*it.MatchSince) >= e.cfg.Polling.Interval.Duration {
				unmatched[dl] = append(unmatched[dl], it)
			} else {
				ev.Keep[scope+"unmatched|"+dl] = true
			}
		case store.MatchAmbiguous:
			ambiguous[dl] = append(ambiguous[dl], it)
		case store.MatchClientUnavailable:
			ev.Keep[scope+"unmatched|"+dl] = true
		}
	}
	for _, dl := range order {
		g := imports[dl]
		if meta.Kind == adapter.KindSonarr && partialPack(g.state, g.items) {
			ev.Drafts = append(ev.Drafts, e.partialPackDraft(scope+"pack|"+dl, meta, dl, g.since, g.items, global, now))
			continue
		}
		ev.Drafts = append(ev.Drafts, e.importDraft(scope+"import|"+dl, meta, dl, g.state, g.since, g.items, global, now))
	}
	for dl, its := range unmatched {
		ev.Drafts = append(ev.Drafts, e.unmatchedDraft(scope+"unmatched|"+dl, meta, dl, its, now))
	}
	for dl, its := range ambiguous {
		ev.Drafts = append(ev.Drafts, e.ambiguousDraft(scope+"ambiguous|"+dl, meta, dl, its, now))
	}
	sort.Slice(ev.Drafts, func(i, j int) bool { return ev.Drafts[i].DedupKey < ev.Drafts[j].DedupKey })
	if !snap.Complete {
		actives, err := tx.ActiveFindingsWithPrefix(scope)
		if err != nil {
			return findings.Result{}, err
		}
		for _, f := range actives {
			ev.Keep[f.DedupKey] = true
		}
	}
	ev.Resolve = func(f domain.Finding) findings.Resolution {
		if !presentDL[f.Subject.DownloadID] {
			return findings.Resolution{
				Reason:     fmt.Sprintf("No longer in %s's queue (complete listing at %s).", meta.Name, now.UTC().Format(time.RFC3339)),
				Limitation: "Housekeeparr cannot tell whether the item was imported, removed, or failed.",
			}
		}
		return findings.Resolution{Reason: fmt.Sprintf("%s no longer reports this condition.", meta.Name)}
	}
	return findings.Apply(tx, ev, now)
}

func queueContent(items []store.QueueRecord) []string { return contentLabels(items) }

func (e *Engine) importDraft(key string, meta ConnMeta, dl, state string, since time.Time, items []store.QueueRecord, eff policy.Effective, now time.Time) findings.Draft {
	q := items[0].Latest
	status, _ := q.TrackedStatus.Get()
	sev := domain.SeverityWarning
	if status == "error" {
		sev = domain.SeverityError
	}
	if state == "failedPending" {
		sev = domain.SeverityNotice
	}
	verb := map[string]string{"importBlocked": "import blocked", "importPending": "import pending with warnings", "failedPending": "marked failed, awaiting the manager's own handling"}[state]
	var msgs []string
	seen := map[string]bool{}
	for _, it := range items {
		for _, m := range it.Latest.StatusMessages {
			for _, s := range m.Messages {
				if !seen[s] {
					seen[s] = true
					msgs = append(msgs, s)
				}
			}
		}
		if it.Latest.ErrorMessage != "" && !seen[it.Latest.ErrorMessage] {
			seen[it.Latest.ErrorMessage] = true
			msgs = append(msgs, it.Latest.ErrorMessage)
		}
	}
	summary := fmt.Sprintf("%s has reported this download as %s for %s.", meta.Name, verb, policy.HumanDuration(now.Sub(since)))
	if len(msgs) > 0 {
		summary += " " + meta.Name + " says: “" + msgs[0] + "”"
	}
	evidence := []domain.Evidence{
		{Label: "Tracked state / status", Value: state + " / " + status, At: &now, Source: meta.Name},
		{Label: "In this state since", Value: since.UTC().Format(time.RFC3339), At: &since},
		{Label: "Affected items", Value: strings.Join(queueContent(items), ", ")},
	}
	for _, m := range msgs {
		evidence = append(evidence, domain.Evidence{Label: "Manager message", Value: m, Source: meta.Name})
	}
	sub := domain.Subject{Connections: []string{meta.ID}, ManagerID: meta.ID, DownloadID: dl, Content: queueContent(items), TransferName: q.Title}
	if items[0].MatchClientID != "" && items[0].MatchNativeID != "" {
		sub.ClientID, sub.NativeID = items[0].MatchClientID, items[0].MatchNativeID
		sub.Connections = append(sub.Connections, items[0].MatchClientID)
		evidence = append(evidence, domain.Evidence{Label: "Correlated transfer", Value: items[0].MatchNote})
	}
	next := now.Add(e.cfg.Polling.Interval.Duration)
	return findings.Draft{
		DedupKey: key, Type: domain.FindingImportAttention, Severity: sev,
		Title:   fmt.Sprintf("%s %s: %s", meta.Name, verb, q.Title),
		Summary: summary, Subject: sub,
		Detail: domain.FindingDetail{
			Evidence:    evidence,
			Missing:     []string{"Import history and episode file records are not read in this release."},
			Confidence:  fmt.Sprintf("Cause as reported by %s; Housekeeparr has not verified import history, files, or episode mapping, so the cause is explicitly uncertain. Reported after the %s import settling period.", meta.Name, policy.HumanDuration(eff.Settings.ImportSettle)),
			Limitations: importLimitations(meta),
			Eligibility: e.modeEligibility("No association will be cleared."),
			NextSteps: []string{
				fmt.Sprintf("Review the item in %s → Activity → Queue; the message explains what could not be imported.", meta.Name),
				fmt.Sprintf("Check whether the files %s expects are present in the download and readable by it (paths, permissions, extraction). Housekeeparr has not verified which manual step is safe for your version and does not change the queue.", meta.Name),
			},
			Links: []domain.Link{{Label: meta.Name + " queue", URL: meta.BaseURL + "/activity/queue"}},
		},
		PolicyID: eff.ID, PolicyRevision: eff.Revision, EvidenceAt: now, NextEvaluationAt: &next,
	}
}

func (e *Engine) unmatchedDraft(key string, meta ConnMeta, dl string, items []store.QueueRecord, now time.Time) findings.Draft {
	it := items[0]
	client := e.conns.Meta[it.MatchClientID]
	return findings.Draft{
		DedupKey: key, Type: domain.FindingQueueUnmatched, Severity: domain.SeverityNotice,
		Title:   fmt.Sprintf("%s queue item not found in %s: %s", meta.Name, client.Name, it.Latest.Title),
		Summary: fmt.Sprintf("%s tracks this download in %s, but %s's complete torrent listing does not contain it.", meta.Name, it.Latest.DownloadClient, client.Name),
		Subject: domain.Subject{Connections: []string{meta.ID, client.ID}, ManagerID: meta.ID, ClientID: client.ID, DownloadID: dl, Content: queueContent(items), TransferName: it.Latest.Title},
		Detail: domain.FindingDetail{
			Evidence: []domain.Evidence{
				{Label: "Correlation", Value: it.MatchNote},
				{Label: "Not found since", Value: it.MatchSince.UTC().Format(time.RFC3339), At: it.MatchSince},
			},
			Confidence:  "Based on complete client listings on consecutive cycles; a download added between the two reads would match on the next cycle.",
			Limitations: []string{fmt.Sprintf("If %s's download client name maps to the wrong Housekeeparr connection, this finding is a configuration problem.", meta.Name)},
			Eligibility: e.modeEligibility(),
			NextSteps: []string{
				"Check whether the torrent was removed from the client manually.",
				fmt.Sprintf("Verify download_clients in the %s configuration maps %q to the right client.", meta.ID, it.Latest.DownloadClient),
			},
			Links: []domain.Link{{Label: meta.Name + " queue", URL: meta.BaseURL + "/activity/queue"}},
		},
		EvidenceAt: now,
	}
}

func (e *Engine) ambiguousDraft(key string, meta ConnMeta, dl string, items []store.QueueRecord, now time.Time) findings.Draft {
	it := items[0]
	return findings.Draft{
		DedupKey: key, Type: domain.FindingQueueAmbiguous, Severity: domain.SeverityNotice,
		Title:   "Ownership ambiguous: " + it.Latest.Title,
		Summary: fmt.Sprintf("More than one transfer matches this %s download, so Housekeeparr cannot tell which one it is tracking.", meta.Name),
		Subject: domain.Subject{Connections: []string{meta.ID}, ManagerID: meta.ID, DownloadID: dl, Content: queueContent(items), TransferName: it.Latest.Title},
		Detail: domain.FindingDetail{
			Evidence:    []domain.Evidence{{Label: "Correlation", Value: it.MatchNote}},
			Confidence:  "Matching is by infohash; a matching name or path is never treated as identity.",
			Limitations: []string{"Ambiguous or shared ownership will block any future write workflow for this download until resolved."},
			Eligibility: e.modeEligibility(),
			NextSteps:   []string{fmt.Sprintf("Map %s's download client %q to one Housekeeparr connection with download_clients.", meta.Name, it.Latest.DownloadClient)},
		},
		EvidenceAt: now,
	}
}

var sourceImpact = map[string]string{
	"IndexerSearchCheck":         "Searches may have incomplete source coverage while this persists.",
	"IndexerRssCheck":            "RSS sync may miss new releases while this persists.",
	"IndexerStatusCheck":         "Some indexers are disabled after failures; searches may have incomplete source coverage.",
	"IndexerLongTermStatusCheck": "Some indexers have failed for a long time; searches may have incomplete source coverage.",
	"IndexerJackettAllCheck":     "Some indexers use an aggregate endpoint that the manager warns about.",
	"DownloadClientCheck":        "The manager may be unable to send or track downloads.",
	"DownloadClientStatusCheck":  "The manager may be unable to send or track downloads.",
	"ImportMechanismCheck":       "Completed downloads may not be imported automatically.",
	"RootFolderCheck":            "Imports into the missing root folder will fail.",
	"RemotePathMappingCheck":     "The manager may be unable to find completed downloads to import them.",
}

func (e *Engine) evaluateHealth(tx *store.Tx, managerID string, h domain.HealthSnapshot, now time.Time) (findings.Result, error) {
	meta := e.conns.Meta[managerID]
	scope := "health:" + managerID + "|"
	ev := findings.Evaluation{Scope: scope}
	seen := map[string]bool{}
	for _, it := range h.Items {
		var sev domain.Severity
		switch it.Type {
		case "error":
			sev = domain.SeverityError
		case "warning":
			sev = domain.SeverityWarning
		default:
			continue // notices are shown on the connection, not as findings
		}
		key := scope + it.Source
		if seen[key] {
			continue
		}
		seen[key] = true
		impact := sourceImpact[it.Source]
		if impact == "" {
			impact = fmt.Sprintf("See %s's System → Status page for impact.", meta.Name)
		}
		links := []domain.Link{{Label: meta.Name + " system status", URL: meta.BaseURL + "/system/status"}}
		if it.WikiURL != "" {
			links = append(links, domain.Link{Label: "Servarr wiki", URL: it.WikiURL})
		}
		title := it.Message
		if len([]rune(title)) > 90 {
			title = string([]rune(title)[:89]) + "…"
		}
		ev.Drafts = append(ev.Drafts, findings.Draft{
			DedupKey: key, Type: domain.FindingManagerHealth, Severity: sev,
			Title:   meta.Name + " health: " + title,
			Summary: it.Message + " " + impact,
			Subject: domain.Subject{Connections: []string{meta.ID}, ManagerID: meta.ID, Source: it.Source},
			Detail: domain.FindingDetail{
				Evidence:    []domain.Evidence{{Label: "Health check", Value: it.Source, At: &h.CollectedAt, Source: meta.Name}, {Label: "Message", Value: it.Message}},
				Confidence:  "Reported by " + meta.Name + "'s own health checks.",
				Limitations: []string{"This is a contemporaneous health report: it indicates possible impact, but Housekeeparr has not verified the effect on any particular search."},
				Eligibility: e.modeEligibility(),
				NextSteps:   []string{fmt.Sprintf("Open %s → System → Status and follow the linked guidance.", meta.Name)},
				Links:       links,
			},
			EvidenceAt: h.CollectedAt,
		})
	}
	ev.Resolve = func(f domain.Finding) findings.Resolution {
		return findings.Resolution{Reason: fmt.Sprintf("%s's health check no longer reports %s (checked %s).", meta.Name, f.Subject.Source, h.CollectedAt.UTC().Format(time.RFC3339))}
	}
	return findings.Apply(tx, ev, now)
}

func (e *Engine) evaluateConnection(tx *store.Tx, f *fetched, failures int, now time.Time) (findings.Result, error) {
	meta := e.conns.Meta[f.id]
	scope := "connection:" + f.id + "|"
	ev := findings.Evaluation{Scope: scope, Keep: map[string]bool{}}
	if f.err == nil {
		ev.Resolve = func(domain.Finding) findings.Resolution {
			return findings.Resolution{Reason: fmt.Sprintf("Verified: %s read succeeded at %s.", meta.Name, now.UTC().Format(time.RFC3339))}
		}
		return findings.Apply(tx, ev, now)
	}
	class := adapter.ClassOf(f.err)
	key := scope + "failure:" + string(class)
	immediate := class == adapter.ErrUnauthorized || class == adapter.ErrUnsupported || class == adapter.ErrPermanent
	if !immediate && failures < 2 {
		// A single transient failure is not yet a finding; keep whatever exists.
		actives, err := tx.ActiveFindingsWithPrefix(scope)
		if err != nil {
			return findings.Result{}, err
		}
		for _, a := range actives {
			ev.Keep[a.DedupKey] = true
		}
		return findings.Apply(tx, ev, now)
	}
	rec, err := tx.Connection(f.id)
	if err != nil {
		return findings.Result{}, err
	}
	lastOK := "never"
	if rec != nil && rec.LastSuccessAt != nil {
		lastOK = rec.LastSuccessAt.UTC().Format(time.RFC3339) + " (" + policy.HumanDuration(now.Sub(*rec.LastSuccessAt)) + " ago)"
	}
	nextTry := ""
	if rec != nil && rec.NextAttemptAt != nil {
		nextTry = rec.NextAttemptAt.UTC().Format(time.RFC3339)
	}
	sev := domain.SeverityWarning
	if immediate {
		sev = domain.SeverityError
	}
	titles := map[adapter.ErrorClass]string{
		adapter.ErrTransient:    "%s is unreachable",
		adapter.ErrUnauthorized: "%s rejected Housekeeparr's credentials",
		adapter.ErrUnsupported:  "%s is incompatible or misconfigured",
		adapter.ErrPermanent:    "%s returned unusable responses",
		adapter.ErrRateLimited:  "%s is rate limiting Housekeeparr",
	}
	tmpl := titles[class]
	if tmpl == "" {
		tmpl = "%s cannot be read"
	}
	steps := map[adapter.ErrorClass][]string{
		adapter.ErrTransient:    {fmt.Sprintf("Check that %s is running and reachable from Housekeeparr at %s.", meta.Name, meta.BaseURL)},
		adapter.ErrUnauthorized: {fmt.Sprintf("Check that the secret reference %s resolves to %s's current credentials, then restart Housekeeparr to retry immediately.", meta.SecretRef, meta.Name)},
		adapter.ErrUnsupported:  {fmt.Sprintf("Check the URL (including any reverse-proxy base path) and %s's version.", meta.Name)},
		adapter.ErrPermanent:    {"Check the URL and any reverse proxy or authentication gateway in front of the application."},
		adapter.ErrRateLimited:  {"No action is usually needed; Housekeeparr honours the server's retry-after interval."},
	}[class]
	ev.Drafts = []findings.Draft{{
		DedupKey: key, Type: domain.FindingConnectionFailure, Severity: sev,
		Title:   fmt.Sprintf(tmpl, meta.Name),
		Summary: fmt.Sprintf("%s failed: %s. Findings that depend on %s are not updated or resolved while it fails; their evidence ages visibly.", f.op, errorText(f.err), meta.Name),
		Subject: domain.Subject{Connections: []string{meta.ID}},
		Detail: domain.FindingDetail{
			Evidence: []domain.Evidence{
				{Label: "Last error", Value: fmt.Sprintf("%s (%s)", errorText(f.err), class), At: &now},
				{Label: "Consecutive failures", Value: fmt.Sprintf("%d", failures)},
				{Label: "Last successful read", Value: lastOK},
				{Label: "Next attempt", Value: nextTry},
			},
			Confidence:  "Observed directly by Housekeeparr.",
			Limitations: []string{"A failed read is never treated as an empty queue, zero peers, or a removed download."},
			Eligibility: "No automation depends on this connection while it fails.",
			NextSteps:   steps,
		},
		EvidenceAt: now,
	}}
	ev.Resolve = func(domain.Finding) findings.Resolution {
		return findings.Resolution{Reason: "The failure cause changed; see the newer connection finding."}
	}
	return findings.Apply(tx, ev, now)
}

func importLimitations(meta ConnMeta) []string {
	out := []string{"A generic import message is not proof of what the release contains; permissions, extraction, path mappings, or samples can produce similar messages."}
	if meta.Kind == adapter.KindSonarr {
		out = append(out, "Partial season-pack handling (clearing a stale association and targeted searches) is not implemented in this milestone.")
	}
	return out
}

func errorDetailMissing(t domain.Transfer) []string {
	if m, ok := t.ClientMessage.Get(); ok && m != "" {
		return nil
	}
	return []string{"Error detail is not reported by this client in its transfer listing."}
}

func (e *Engine) sourceDraft(key string, meta ConnMeta, eff policy.Effective, t domain.Transfer, a policy.Assessment, queue []store.QueueRecord, now time.Time) findings.Draft {
	managers, links := e.managerLinks(queue)
	next := now.Add(eff.Settings.PollInterval)
	msg := signalText(t.ClientMessage, func(m string) string { return nonEmptyText(m, "none") })
	steps := []string{fmt.Sprintf("Check the torrent's tracker status in %s; a tracker usually unregisters a torrent that was removed, replaced, or never uploaded.", meta.Name)}
	if len(queue) > 0 {
		mgr := e.conns.Meta[queue[0].ManagerID].Name
		steps = append(steps, fmt.Sprintf("With preserved-data failure handling enabled for this scope, Housekeeparr can fail and blocklist this grab in %s so it fetches another release, keeping the torrent and its data; otherwise review it in %s → Activity → Queue.", mgr, mgr))
	} else {
		steps = append(steps, "No media-manager queue item is associated with this transfer.")
	}
	return findings.Draft{
		DedupKey: key, Type: domain.FindingSourceRejected, Severity: domain.SeverityWarning,
		Title:   "Rejected by its tracker: " + t.Name,
		Summary: a.Headline,
		Subject: domain.Subject{Connections: append([]string{meta.ID}, managers...), ClientID: meta.ID, NativeID: t.NativeID,
			TransferName: t.Name, Content: contentLabels(queue)},
		Detail: domain.FindingDetail{
			Evidence: append([]domain.Evidence{
				{Label: "Tracker result", Value: string(t.SourceFailure.Value) + " — " + msg, At: &now},
				{Label: "Rejected without progress since", Value: a.StreakStart.UTC().Format(time.RFC3339)},
				{Label: "Qualifying evidence", Value: fmt.Sprintf("%d observations, %s of %s confirmation (proposed)", a.Observations,
					policy.HumanDuration(a.Elapsed), policy.HumanDuration(a.Window))},
			}, transferEvidence(t, a, now)...),
			Missing:    missingSignals(t),
			Confidence: "Reported by the client from the tracker's response on consecutive fresh observations while the download was active and made no verified progress.",
			Limitations: append(append([]string{}, a.Limitations...),
				"Only an unregistered rejection qualifies; other tracker errors, local storage errors, and stale messages on stopped torrents never do."),
			Eligibility: e.modeEligibility(),
			NextSteps:   steps,
			Links:       links,
		},
		PolicyID: eff.ID, PolicyRevision: eff.Revision, EvidenceAt: a.EvidenceAt, NextEvaluationAt: &next,
	}
}

func nonEmptyText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// PartialPackMessage: see domain.PartialPackMessage.
const PartialPackMessage = domain.PartialPackMessage

// partialPack reports whether a settled, import-blocked download is a
// season pack (several episode rows, one download) that Sonarr reports with
// the partial-pack message. This only selects candidates: whether the
// present episodes were positively imported is established from history at
// planning and before any write.
func partialPack(state string, items []store.QueueRecord) bool {
	if state != "importBlocked" || len(items) < 2 {
		return false
	}
	for _, it := range items {
		if it.Latest.ContentKind != "episode" || it.Latest.EpisodeID <= 0 {
			return false
		}
		found := false
		for _, m := range it.Latest.StatusMessages {
			found = found || m.Title == PartialPackMessage
		}
		if !found {
			return false
		}
	}
	return true
}

func (e *Engine) partialPackDraft(key string, meta ConnMeta, dl string, since time.Time, items []store.QueueRecord, eff policy.Effective, now time.Time) findings.Draft {
	q := items[0].Latest
	sub := domain.Subject{Connections: []string{meta.ID}, ManagerID: meta.ID, DownloadID: dl, Content: queueContent(items), TransferName: q.Title}
	if items[0].MatchClientID != "" && items[0].MatchNativeID != "" {
		sub.ClientID, sub.NativeID = items[0].MatchClientID, items[0].MatchNativeID
		sub.Connections = append(sub.Connections, items[0].MatchClientID)
	}
	var other []string
	for _, m := range q.StatusMessages {
		if m.Title != PartialPackMessage {
			other = append(other, m.Title+": "+strings.Join(m.Messages, "; "))
		}
	}
	next := now.Add(e.cfg.Polling.Interval.Duration)
	return findings.Draft{
		DedupKey: key, Type: domain.FindingPartialPack, Severity: domain.SeverityWarning,
		Title:   fmt.Sprintf("Season pack partly imported: %s", q.Title),
		Summary: fmt.Sprintf("%s imported what this pack contained but still tracks it for %d expected episodes, and reports “%s”.", meta.Name, len(items), PartialPackMessage),
		Subject: sub,
		Detail: domain.FindingDetail{
			Evidence: append([]domain.Evidence{
				{Label: "Tracked state", Value: "importBlocked / warning", At: &now, Source: meta.Name},
				{Label: "In this state since", Value: since.UTC().Format(time.RFC3339), At: &since},
				{Label: "Expected episodes (queue rows)", Value: strings.Join(queueContent(items), ", ")},
			}, func() []domain.Evidence {
				var out []domain.Evidence
				for _, o := range other {
					out = append(out, domain.Evidence{Label: "Other manager message", Value: o, Source: meta.Name})
				}
				return out
			}()...),
			Confidence: "Candidate only: which episodes were imported is established from the manager's import history for this download, and episode files, when an action is planned and again before it is sent.",
			Limitations: []string{
				"An expected range is the manager's context, not proof of what the release contains.",
				"Any other import message (a sample, a missing audio track, a mapping problem) makes the cause uncertain, and nothing is cleared.",
			},
			Eligibility: e.modeEligibility("Partial-pack handling clears only the stale association and searches the still-missing episodes individually; the torrent, its files, and the imports are kept."),
			NextSteps:   []string{fmt.Sprintf("Review the item in %s → Activity → Queue; the episodes it did not import are still expected.", meta.Name)},
			Links:       []domain.Link{{Label: meta.Name + " queue", URL: meta.BaseURL + "/activity/queue"}},
		},
		PolicyID: eff.ID, PolicyRevision: eff.Revision, EvidenceAt: now, NextEvaluationAt: &next,
	}
}
