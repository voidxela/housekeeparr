package action

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// TestedWorkflow records that a complete write workflow (every step and
// its verification) passed disposable-instance tests for these exact
// versions. A workflow is only ever eligible for a tested combination.
type TestedWorkflow struct {
	Workflow       policy.Workflow
	Manager        adapter.Kind
	ManagerVersion string
	Client         adapter.Kind
	ClientVersion  string
}

// Tested is the registry of tested write workflows. Each entry is backed
// by a passing run of internal/integration/recovery_test.go recorded in
// docs/compatibility.md ("Recovery workflow runs"); add an entry only with
// such a run. qBittorrent 4.x is absent on purpose: it does not report
// privacy, so recovery is refused there regardless.
var Tested = []TestedWorkflow{
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
}

// WorkflowFor maps a finding type to the workflow that would address it.
func WorkflowFor(findingType string) (policy.Workflow, bool) {
	switch findingType {
	case domain.FindingTransferStalled:
		return policy.WorkflowNoProgressRecovery, true
	case domain.FindingMetadataTimeout:
		return policy.WorkflowMetadataRecovery, true
	case domain.FindingTransferError:
		return policy.WorkflowPreservedDataFailure, true
	}
	return "", false
}

// PlanInput is everything the planner needs; it is read from local state
// by the caller so planning itself is pure and identical for CLI and web.
type PlanInput struct {
	Finding  domain.Finding
	Transfer *store.TransferRecord
	Queue    []store.QueueRecord // rows correlated with the transfer
	Policy   policy.Effective
	// Scope is the policy subject Policy was resolved for; the gate
	// re-resolves it before every write.
	Scope        policy.Subject
	Origin       Origin
	ClientKind   adapter.Kind
	ClientProbe  *adapter.Probe
	ManagerKind  adapter.Kind
	ManagerProbe *adapter.Probe
	// Locks maps every currently held lock subject to its intent, and
	// Cooldowns every recorded cooldown; the planner checks each subject
	// the proposal would lock (the transfer and every affected content).
	Locks     map[string]int64
	Cooldowns map[string]store.CooldownRecord
	// Limits are the effective acquisition limits; zero means the
	// proposed defaults.
	Limits policy.Limits
	// Owners are the managers that could own the transfer (see
	// Planned.Owners).
	Owners []string
	// Retained is the current retained-transfer usage (SPEC §7.1).
	Retained       store.RetainedUsage
	PollInterval   time.Duration
	ConfigRevision string
	Tested         []TestedWorkflow
	Now            time.Time
	// PreviewTTL bounds how long a preview may be executed; 0 means 5m.
	PreviewTTL time.Duration
}

// NoPlan explains why a finding has no action workflow.
type NoPlan struct {
	Reason string `json:"reason"`
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Plan builds the proposal for a finding. It returns a NoPlan when the
// finding type has no workflow in v1.
func Plan(in PlanInput) (*Proposal, *NoPlan) {
	wf, ok := WorkflowFor(in.Finding.Type)
	if !ok {
		return nil, &NoPlan{Reason: "this kind of finding has no automated workflow; it is diagnosis only"}
	}
	if in.Transfer == nil {
		return nil, &NoPlan{Reason: "the affected transfer is no longer known locally"}
	}
	t := in.Transfer
	limits := in.Limits
	if limits.Acquisitions == 0 {
		limits = policy.DefaultLimits()
	}
	ttl := in.PreviewTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	p := &Proposal{
		Workflow: string(wf), Origin: in.Origin, Subject: raw(map[string]string{"client_id": t.ClientID, "native_id": t.NativeID}),
		Locks:          []string{TransferLock(t.ClientID, t.NativeID)},
		PolicyRevision: in.Policy.Revision, ConfigRevision: in.ConfigRevision, Scope: in.Scope, LiveChecks: LiveChecks,
		CreatedAt: in.Now, ExpiresAt: in.Now.Add(ttl), Cooldown: limits.SuccessCooldown,
	}
	fid := in.Finding.ID
	p.FindingID = &fid
	add := func(name string, ok, hard bool, detail string) {
		p.Prerequisites = append(p.Prerequisites, Check{Name: name, Satisfied: ok, Hard: hard, Detail: detail})
	}

	// --- manager association ------------------------------------------------
	var present []store.QueueRecord
	for _, q := range in.Queue {
		if q.Present {
			present = append(present, q)
		}
	}
	var row *store.QueueRecord
	var unknownContent []string
	for _, q := range present {
		if key, ok := q.Latest.ContentKey(); ok {
			p.Locks = appendUnique(p.Locks, ContentLock(q.ManagerID, key))
		} else {
			unknownContent = append(unknownContent, fmt.Sprintf("%s queue item %d", q.ManagerID, q.QueueID))
		}
	}
	switch {
	case len(present) == 1 && present[0].MatchStatus == store.MatchMatched:
		row = &present[0]
		add("one manager association", true, true, fmt.Sprintf("%s queue item %q is correlated with this transfer", row.ManagerID, row.Latest.Title))
	case len(present) == 0:
		add("one manager association", false, true, "no manager queue item is correlated with this transfer, so there is no grab to fail")
	case isPack(present):
		add("one manager association", false, true, fmt.Sprintf("this release covers %d %s items (a multi-episode pack); failing it affects every one of them, "+
			"and pack recovery has not been tested, so it stays observation-only", len(present), present[0].ManagerID))
	default:
		add("one manager association", false, true, fmt.Sprintf("%d queue items relate to this transfer or the correlation is not exact; ownership is ambiguous", len(present)))
	}
	if len(unknownContent) > 0 {
		add("content identity known", false, true, strings.Join(unknownContent, ", ")+
			" did not report an episode or movie ID, so attempts, locks, and exclusions cannot be tied to the content")
	}

	// --- tested workflow ----------------------------------------------------
	cv, mv := probeVersion(in.ClientProbe), probeVersion(in.ManagerProbe)
	tested := false
	for _, tw := range in.Tested {
		if tw.Workflow == wf && tw.Client == in.ClientKind && tw.ClientVersion == cv && row != nil && tw.Manager == in.ManagerKind && tw.ManagerVersion == mv {
			tested = true
		}
	}
	combo := fmt.Sprintf("%s %s with %s %s", nonEmpty(string(in.ManagerKind), "an unknown manager"), nonEmpty(mv, "(version unknown)"),
		nonEmpty(string(in.ClientKind), "an unknown client"), nonEmpty(cv, "(version unknown)"))
	if tested {
		add("tested write workflow", true, true, string(wf)+" passed disposable-instance workflow tests for "+combo)
	} else {
		add("tested write workflow", false, true, "no complete "+string(wf)+" workflow has been tested for "+combo+
			"; this build performs no writes for it (see docs/compatibility.md)")
	}

	// --- finding and evidence -----------------------------------------------
	switch in.Finding.State {
	case domain.FindingOpen, domain.FindingAcknowledged:
		add("finding is active", true, true, "the finding is "+string(in.Finding.State))
	case domain.FindingSnoozed:
		ok := in.Origin == OriginManual
		add("finding is active", ok, true, "the finding is snoozed; snoozing suppresses automation (a manual action is still possible)")
	default:
		add("finding is active", false, true, "the finding is "+string(in.Finding.State))
	}
	fresh := in.PollInterval > 0 && in.Now.Sub(t.LastSeen) <= 2*in.PollInterval && t.Present
	add("fresh evidence", fresh, true, fmt.Sprintf("transfer last observed %s ago", policy.HumanDuration(in.Now.Sub(t.LastSeen))))
	if wf != policy.WorkflowPreservedDataFailure {
		q := t.Assessment != nil && t.Assessment.Status == policy.StatusQualified
		detail := "no qualified diagnosis"
		if t.Assessment != nil {
			detail = "assessment: " + string(t.Assessment.Status) + " — " + t.Assessment.Headline
		}
		add("diagnosis qualified", q, true, detail)
	}
	want := map[policy.Workflow]domain.StateClass{
		policy.WorkflowNoProgressRecovery: domain.StateDownloading, policy.WorkflowMetadataRecovery: domain.StateMetadata,
		policy.WorkflowPreservedDataFailure: domain.StateError,
	}[wf]
	add("applicable transfer state", t.StateClass == want, true,
		fmt.Sprintf("transfer is %s (native %q); this workflow applies only to %s transfers — paused, queued, and checking transfers are never acted on",
			t.StateClass, t.Latest.NativeState, want))

	// --- privacy ------------------------------------------------------------
	priv, known := t.Latest.Private.Get()
	switch {
	case known && !priv:
		add("public transfer", true, true, "the client reports the torrent as public")
	case known && priv:
		add("public transfer", false, true, "private torrents are observation-only for recovery: stopping or replacing one may conflict with tracker obligations")
	default:
		add("public transfer", false, true, "privacy is unknown on this client, so recovery stays observation-only")
	}

	// --- exclusivity --------------------------------------------------------
	var held, cooling []string
	for _, l := range p.Locks {
		if h := in.Locks[l]; h != 0 {
			held = append(held, fmt.Sprintf("action #%d holds %s; it must finish or be reviewed first", h, l))
		}
		if c, ok := in.Cooldowns[l]; ok && in.Now.Before(c.Until) {
			cooling = append(cooling, fmt.Sprintf("%s until %s (%s)", l, c.Until.UTC().Format(time.RFC3339), c.Reason))
		}
	}
	add("no conflicting action", len(held) == 0, true, nonEmpty(strings.Join(held, "; "), "no other action holds "+strings.Join(p.Locks, " or ")))
	add("not in cooldown", len(cooling) == 0, true, nonEmpty(strings.Join(cooling, "; "), "no cooldown on "+strings.Join(p.Locks, " or ")))

	// --- policy -------------------------------------------------------------
	p.Prerequisites = append(p.Prerequisites, PolicyCheck(in.Policy, wf, in.Origin))

	// --- operations ---------------------------------------------------------
	// Every flag is explicit (SPEC §7.3). The semantics are those recorded
	// for managed rows on released Sonarr/Radarr: the removal is durable,
	// the exact release is blocklisted, removeFromClient=false keeps the
	// torrent and data, and skipRedownload=false makes the manager the sole
	// owner of the replacement search.
	if row != nil {
		p.Operations = append(p.Operations, Operation{
			Op: "manager.queue.fail",
			Target: raw(map[string]any{"manager_id": row.ManagerID, "queue_id": row.QueueID, "download_id": row.DownloadID,
				"title": row.Latest.Title, "series_id": row.Latest.SeriesID, "episode_id": row.Latest.EpisodeID, "movie_id": row.Latest.MovieID}),
			Request:     raw(map[string]bool{"removeFromClient": false, "blocklist": true, "skipRedownload": false, "changeCategory": false}),
			Expect:      raw(map[string]any{"queue_row_absent": true, "release_blocklisted": true, "history_event": "downloadFailed"}),
			Description: fmt.Sprintf("Mark the grab failed in %s: blocklist this release and remove the queue item, keeping the torrent in the client", row.ManagerID),
		})
		p.Effects = append(p.Effects, fmt.Sprintf("%s blocklists this exact release (%q) and stops tracking it", row.ManagerID, row.Latest.Title),
			fmt.Sprintf("%s starts its own replacement search (manager-native redownload); Housekeeparr does not search as well", row.ManagerID))
	}
	if wf != policy.WorkflowPreservedDataFailure {
		p.Operations = append(p.Operations, Operation{
			Op:          "client.stop",
			Target:      raw(map[string]string{"client_id": t.ClientID, "native_id": t.NativeID}),
			Request:     raw(map[string]bool{"delete_data": false}),
			Expect:      raw(map[string]any{"state_class": domain.StatePaused, "registered": true}),
			Description: "Stop the torrent in the client, retaining it and its data, so it no longer occupies an active slot",
		})
		p.Effects = append(p.Effects, "the torrent is stopped and kept, with all downloaded data, in its current category")
		add("retained-data ceiling", retainedOK(in, limits, t), true, retainedDetail(in, limits, t))
	}
	p.SideEffects = append(p.SideEffects,
		"no file is deleted, moved, or renamed; the stopped transfer and its data stay in the client "+
			"(this build does not yet count retained transfers against capacity limits)",
		"this build verifies the steps above and that the manager queued its search; whether the search finds and grabs a release, "+
			"and how that download progresses, is not yet tracked here: check the manager's activity")
	if row != nil {
		var keys []string
		if key, ok := row.Latest.ContentKey(); ok {
			keys = append(keys, key)
		}
		p.Budgets = acquisitionClaims(limits, row.ManagerID, keys)
		p.SideEffects = append(p.SideEffects, fmt.Sprintf("one manager replacement search may query every enabled indexer in %s and grab a release; "+
			"Housekeeparr counts it as one acquisition but cannot bound the manager's own requests", row.ManagerID))
	}
	p.Evidence = raw(map[string]any{"finding": in.Finding.ID, "finding_type": in.Finding.Type, "assessment": t.Assessment,
		"transfer_state": t.StateClass, "native_state": t.Latest.NativeState, "verified_bytes": t.Latest.VerifiedBytes, "last_seen": t.LastSeen})
	p.Planned = planned(in, cv, mv, row, present)
	p.Fingerprint = p.Planned.fingerprint()
	return p, nil
}

// PolicyCheck evaluates mode, emergency stop, exclusions, and (for
// automation) workflow permission. Planning and the per-write gate use it.
func PolicyCheck(pol policy.Effective, wf policy.Workflow, origin Origin) Check {
	if origin == OriginAutomatic {
		ok, reasons := pol.Permits(wf)
		return Check{Name: "automation permitted by policy", Satisfied: ok, Detail: strings.Join(nonEmptyList(reasons, "permitted"), "; ")}
	}
	var reasons []string
	if pol.Mode == "observe" || pol.Mode == "" {
		reasons = append(reasons, "mode is observe: no operational writes, manual or automatic")
	}
	if pol.EmergencyStop {
		reasons = append(reasons, "emergency stop is set")
	}
	for _, ex := range pol.Exclusions {
		for _, w := range ex.Workflows {
			if w == wf {
				reasons = append(reasons, "excluded: "+ex.Reason+" ("+ex.From+")")
			}
		}
	}
	return Check{Name: "manual action permitted by policy", Satisfied: len(reasons) == 0, Detail: strings.Join(nonEmptyList(reasons, "permitted"), "; ")}
}

func planned(in PlanInput, cv, mv string, row *store.QueueRecord, present []store.QueueRecord) Planned {
	t := in.Transfer
	pl := Planned{ClientID: t.ClientID, NativeID: t.NativeID, Name: t.Name, StateClass: t.StateClass, Verified: t.Latest.VerifiedBytes,
		Wanted: t.Latest.WantedBytes, Private: t.Latest.Private, Category: t.Latest.Category, ClientVersion: cv, Owners: in.Owners}
	if row != nil {
		pl.ManagerID, pl.ManagerVersion, pl.QueueID, pl.DownloadID = row.ManagerID, mv, row.QueueID, row.DownloadID
		var rows []domain.QueueItem
		for _, q := range present {
			rows = append(rows, q.Latest)
		}
		pl.Content = contentKeys(rows)
	}
	return pl
}

func probeVersion(p *adapter.Probe) string {
	if p == nil {
		return ""
	}
	return strings.TrimPrefix(p.Version, "v")
}

// TransferLock is the lock subject for a physical transfer.
func TransferLock(clientID, nativeID string) string { return "transfer:" + clientID + "|" + nativeID }

// ContentLock is the lock and cooldown subject for manager content. It is
// independent of the release, so every replacement attempt for the same
// episode or movie shares it.
func ContentLock(managerID, contentKey string) string {
	return "content:" + managerID + "|" + contentKey
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// isPack reports whether every row is the same manager's row for the same
// download: one release covering several episodes.
func isPack(rows []store.QueueRecord) bool {
	for _, r := range rows[1:] {
		if r.ManagerID != rows[0].ManagerID || !strings.EqualFold(r.DownloadID, rows[0].DownloadID) || r.DownloadID == "" {
			return false
		}
	}
	return len(rows) > 1
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nonEmptyList(xs []string, def string) []string {
	if len(xs) == 0 {
		return []string{def}
	}
	return xs
}

// retainedOK reports whether keeping one more stopped transfer stays
// within the configured ceilings. With a byte ceiling, a retained transfer
// of unknown size makes the total unknown, which is not treated as room.
func retainedOK(in PlanInput, l policy.Limits, t *store.TransferRecord) bool {
	if in.Retained.Count+1 > l.RetainedTransfers {
		return false
	}
	if l.RetainedBytes > 0 {
		size, known := t.Latest.WantedBytes.Get()
		if !known || in.Retained.UnknownSize > 0 || in.Retained.KnownBytes+size > l.RetainedBytes {
			return false
		}
	}
	return true
}

func retainedDetail(in PlanInput, l policy.Limits, t *store.TransferRecord) string {
	u := in.Retained
	d := fmt.Sprintf("%d of %d retained transfers (limit from %s); known logical size %s", u.Count, l.RetainedTransfers,
		l.From("retained_transfers"), gib(u.KnownBytes))
	if u.UnknownSize > 0 {
		d += fmt.Sprintf(", %d of unknown size", u.UnknownSize)
	}
	if l.RetainedBytes > 0 {
		d += fmt.Sprintf("; byte ceiling %s (from %s)", gib(l.RetainedBytes), l.From("retained_gib"))
		if _, known := t.Latest.WantedBytes.Get(); !known {
			d += "; this transfer's size is unknown, so the ceiling cannot be shown to hold"
		}
	} else {
		d += "; no byte ceiling is configured"
	}
	d += ". Shared files and hardlinks make logical size an estimate, not disk use; free space is not measured, so no free-space threshold is enforced." +
		" At the ceiling, recovery stops until retained transfers are cleaned up outside Housekeeparr (see housekeeparr retained)"
	return d
}

func gib(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
