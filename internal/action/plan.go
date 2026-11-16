package action

import (
	"crypto/sha256"
	"encoding/hex"
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

// Tested is the registry of tested write workflows. It is empty: the
// managed-row queue-removal semantics are recorded in
// docs/compatibility.md, but no Housekeeparr adapter has a write method and
// no complete workflow (separation, replacement ownership, verification)
// has been exercised end to end. Add an entry only with the integration
// run that establishes it.
var Tested []TestedWorkflow

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
	Finding        domain.Finding
	Transfer       *store.TransferRecord
	Queue          []store.QueueRecord // rows correlated with the transfer
	Policy         policy.Effective
	Origin         Origin
	ClientKind     adapter.Kind
	ClientProbe    *adapter.Probe
	ManagerKind    adapter.Kind
	ManagerProbe   *adapter.Probe
	LockHolder     int64
	CooldownUntil  *time.Time
	CooldownReason string
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
	ttl := in.PreviewTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	p := &Proposal{
		Workflow: string(wf), Origin: in.Origin, Subject: raw(map[string]string{"client_id": t.ClientID, "native_id": t.NativeID}),
		Locks:          []string{"transfer:" + t.ClientID + "|" + t.NativeID},
		PolicyRevision: in.Policy.Revision, ConfigRevision: in.ConfigRevision,
		CreatedAt: in.Now, ExpiresAt: in.Now.Add(ttl), Cooldown: 24 * time.Hour,
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
	switch {
	case len(present) == 1 && present[0].MatchStatus == store.MatchMatched:
		row = &present[0]
		add("one manager association", true, true, fmt.Sprintf("%s queue item %q is correlated with this transfer", row.ManagerID, row.Latest.Title))
		p.Locks = append(p.Locks, "content:"+row.ManagerID+"|"+row.ItemKey)
	case len(present) == 0:
		add("one manager association", false, true, "no manager queue item is correlated with this transfer, so there is no grab to fail")
	default:
		add("one manager association", false, true, fmt.Sprintf("%d queue items relate to this transfer or the correlation is not exact; ownership is ambiguous", len(present)))
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
	add("no conflicting action", in.LockHolder == 0, true, lockDetail(in.LockHolder))
	if in.CooldownUntil != nil && in.Now.Before(*in.CooldownUntil) {
		add("not in cooldown", false, true, fmt.Sprintf("cooldown until %s (%s)", in.CooldownUntil.UTC().Format(time.RFC3339), in.CooldownReason))
	} else {
		add("not in cooldown", true, true, "no cooldown")
	}

	// --- policy -------------------------------------------------------------
	pol := in.Policy
	if in.Origin == OriginAutomatic {
		ok, reasons := pol.Permits(wf)
		add("automation permitted by policy", ok, false, strings.Join(nonEmptyList(reasons, "permitted"), "; "))
	} else {
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
		add("manual action permitted by policy", len(reasons) == 0, false, strings.Join(nonEmptyList(reasons, "permitted"), "; "))
	}

	// --- operations ---------------------------------------------------------
	// Every flag is explicit (SPEC §7.3). The semantics are those recorded
	// for managed rows on released Sonarr/Radarr: the removal is durable,
	// the exact release is blocklisted, removeFromClient=false keeps the
	// torrent and data, and skipRedownload=false makes the manager the sole
	// owner of the replacement search.
	if row != nil {
		p.Operations = append(p.Operations, Operation{
			Op:          "manager.queue.fail",
			Target:      raw(map[string]any{"manager_id": row.ManagerID, "queue_id": row.QueueID, "download_id": row.DownloadID}),
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
	}
	p.SideEffects = append(p.SideEffects,
		"no file is deleted, moved, or renamed; the retained transfer counts toward retained-data tracking",
		"a later search result, grab, and download progress are verified separately; a search that finds nothing is reported, not retried")
	if row != nil {
		p.Budgets = []BudgetClaim{
			{Scope: "global:acquisition", Window: 24 * time.Hour, Limit: 20, Units: 1},
			{Scope: "content:" + row.ManagerID + "|" + row.ItemKey, Window: 7 * 24 * time.Hour, Limit: 3, Units: 1},
		}
	}
	p.Evidence = raw(map[string]any{"finding": in.Finding.ID, "finding_type": in.Finding.Type, "assessment": t.Assessment,
		"transfer_state": t.StateClass, "native_state": t.Latest.NativeState, "verified_bytes": t.Latest.VerifiedBytes, "last_seen": t.LastSeen})
	p.Fingerprint = fingerprint(in, row)
	return p, nil
}

// Facts are the external, decision-relevant facts a proposal was planned
// against. The executor recomputes them from a fresh read immediately
// before the first write; any difference cancels the action. (Local facts,
// such as finding state and policy, are re-checked by re-planning.)
type Facts struct {
	ClientID, NativeID string
	Present            bool
	StateClass         domain.StateClass
	Verified           domain.Signal[int64]
	ManagerID          string
	QueueID            int64
	DownloadID         string
}

// Fingerprint digests the facts.
func (f Facts) Fingerprint() string {
	h := sha256.New()
	v, known := f.Verified.Get()
	fmt.Fprintf(h, "v1|%s|%s|%v|%s|%v:%d|%s|%d|%s", f.ClientID, f.NativeID, f.Present, f.StateClass, known, v,
		f.ManagerID, f.QueueID, strings.ToLower(f.DownloadID))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func fingerprint(in PlanInput, row *store.QueueRecord) string {
	t := in.Transfer
	f := Facts{ClientID: t.ClientID, NativeID: t.NativeID, Present: t.Present, StateClass: t.StateClass, Verified: t.Latest.VerifiedBytes}
	if row != nil {
		f.ManagerID, f.QueueID, f.DownloadID = row.ManagerID, row.QueueID, row.DownloadID
	}
	return f.Fingerprint()
}

func probeVersion(p *adapter.Probe) string {
	if p == nil {
		return ""
	}
	return strings.TrimPrefix(p.Version, "v")
}

func lockDetail(holder int64) string {
	if holder == 0 {
		return "no other action holds this transfer"
	}
	return fmt.Sprintf("action #%d holds this transfer; it must finish or be reviewed first", holder)
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
