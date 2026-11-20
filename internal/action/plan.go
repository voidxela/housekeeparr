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

// Tested is the registry of supported versionss. Each entry is backed
// by a passing run of internal/integration/recovery_test.go recorded in
// docs/compatibility.md ("Recovery workflow runs"); add an entry only with
// such a run.
var Tested = []TestedWorkflow{
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindRTorrent, ClientVersion: "0.9.8"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindRTorrent, ClientVersion: "0.9.8"},
	// Transmission reports its build revision with the release; both are
	// matched exactly.
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.6 (38c164933e)"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.0.6 (38c164933e)"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.1.4 (a6d907d9ea)"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.1.4 (a6d907d9ea)"},
	// Metadata recovery (docs/compatibility.md, "Metadata recovery runs").
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindRTorrent, ClientVersion: "0.9.8"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindRTorrent, ClientVersion: "0.9.8"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.6 (38c164933e)"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.0.6 (38c164933e)"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.1.4 (a6d907d9ea)"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.1.4 (a6d907d9ea)"},
	// Preserved-data failure handling: tracker "unregistered" rejection only
	// (docs/compatibility.md, "Preserved-data failure runs").
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	// Partial-pack clearance (docs/compatibility.md, "Partial season-pack
	// probe" and its workflow run).
	{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"},
	{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.6 (38c164933e)"},
	{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.1.4 (a6d907d9ea)"},
	{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindRTorrent, ClientVersion: "0.9.8"},
	// qBittorrent 4.6.7 (privacy unknown: treated as private, kept running
	// by default; docs/compatibility.md, "qBittorrent 4.6.7 runs").
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "4.6.7"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindQBittorrent, ClientVersion: "4.6.7"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "4.6.7"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindQBittorrent, ClientVersion: "4.6.7"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "4.6.7"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindQBittorrent, ClientVersion: "4.6.7"},
	{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindQBittorrent, ClientVersion: "4.6.7"},
	// Proactive missing searches involve no client (docs/compatibility.md,
	// "Proactive search runs").
	{Workflow: policy.WorkflowMissingSearch, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014"},
	{Workflow: policy.WorkflowMissingSearch, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindRTorrent, ClientVersion: "0.9.8"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindRTorrent, ClientVersion: "0.9.8"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindDeluge, ClientVersion: "2.2.0"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.6 (38c164933e)"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.0.6 (38c164933e)"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.1.4 (a6d907d9ea)"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.1.4 (a6d907d9ea)"},
	// Transmission 4.0.5 (docs/compatibility.md, "Production trial runs").
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.5 (a6fe2a64aa)"},
	{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.0.5 (a6fe2a64aa)"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.5 (a6fe2a64aa)"},
	{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.0.5 (a6fe2a64aa)"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.5 (a6fe2a64aa)"},
	{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindRadarr, ManagerVersion: "6.4.4.10685", Client: adapter.KindTransmission, ClientVersion: "4.0.5 (a6fe2a64aa)"},
	{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014", Client: adapter.KindTransmission, ClientVersion: "4.0.5 (a6fe2a64aa)"},
}

// diagnosesFor lists the qualified diagnoses each workflow addresses.
var diagnosesFor = map[policy.Workflow][]policy.Diagnosis{
	policy.WorkflowNoProgressRecovery:   {policy.DiagnosisNoProgress, policy.DiagnosisNoProgressUnavailable},
	policy.WorkflowMetadataRecovery:     {policy.DiagnosisMetadataTimeout},
	policy.WorkflowPreservedDataFailure: {policy.DiagnosisSourceUnregistered},
}

// WorkflowFor maps a finding type to the workflow that would address it.
func WorkflowFor(findingType string) (policy.Workflow, bool) {
	switch findingType {
	case domain.FindingTransferStalled:
		return policy.WorkflowNoProgressRecovery, true
	case domain.FindingMetadataTimeout:
		return policy.WorkflowMetadataRecovery, true
	case domain.FindingSourceRejected:
		return policy.WorkflowPreservedDataFailure, true
	case domain.FindingPartialPack:
		return policy.WorkflowPartialPack, true
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
	// RecoveredBy is the action that already failed this transfer's grab
	// in its manager, if any (explanation only).
	RecoveredBy int64
	// Paused is the operator's pause of this workflow, if any.
	Paused *store.WorkflowPause
	// Retained is the current retained-transfer usage (SPEC §7.1).
	Retained       store.RetainedUsage
	PollInterval   time.Duration
	ConfigRevision string
	Tested         []TestedWorkflow
	// UntestedVersions is the operator's opt-in to writes through versions
	// outside the API lines with tested evidence ([automation]
	// untested_versions = "act").
	UntestedVersions bool
	Now              time.Time
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
	if in.Finding.Type == domain.FindingTransferError {
		return nil, &NoPlan{Reason: "a client-reported error is diagnosis only: local storage, permission, and missing-data problems are never " +
			"treated as a failed release, and the client's state alone does not establish what happened to the data (SPEC §7.2)"}
	}
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
	case len(present) == 0 && in.RecoveredBy != 0:
		add("one manager association", false, true, fmt.Sprintf("action #%d already failed and blocklisted this grab in its manager, which searched for a "+
			"replacement; the torrent itself was left in the client (a private torrent is left running under private_recovery = \"keep_running\"), "+
			"so nothing more is done here: stop or remove it in the client when you no longer want it", in.RecoveredBy))
	case len(present) == 0:
		add("one manager association", false, true, "no manager queue item is correlated with this transfer, so there is no grab to fail")
	case isPack(present):
		add("one manager association", false, true, fmt.Sprintf("this release covers %d %s items (a multi-episode pack); failing it affects every one of them, "+
			"and pack recovery has not been tested, so it stays observation-only", len(present), present[0].ManagerID))
	default:
		add("one manager association", false, true, fmt.Sprintf("%d queue items relate to this transfer or the correlation is not exact; ownership is ambiguous", len(present)))
	}
	if wf == policy.WorkflowPreservedDataFailure && row != nil {
		// Import ambiguity refuses (SPEC §7.2): only a grab the manager
		// still tracks as downloading is failed.
		ts, known := row.Latest.TrackedState.Get()
		add("no import in progress", known && strings.EqualFold(ts, "downloading"), true,
			fmt.Sprintf("%s tracks this grab as %q; only a grab it still tracks as downloading is failed, so a pending, partial, or blocked import "+
				"is never interrupted", row.ManagerID, nonEmpty(ts, "unknown ("+row.Latest.TrackedState.Note+")")))
	}
	if len(unknownContent) > 0 {
		add("content identity known", false, true, strings.Join(unknownContent, ", ")+
			" did not report an episode or movie ID, so attempts, locks, and exclusions cannot be tied to the content")
	}

	// --- version support ------------------------------------------------------
	cv, mv := probeVersion(in.ClientProbe), probeVersion(in.ManagerProbe)
	{
		sup := VersionSupport(in.Tested, wf, Side{in.ManagerKind, mv}, Side{in.ClientKind, cv}, in.UntestedVersions)
		add("supported versions", sup.OK && row != nil, true, sup.Detail)
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
	{
		q := t.Assessment != nil && t.Assessment.Status == policy.StatusQualified
		detail := "no qualified diagnosis"
		if t.Assessment != nil {
			detail = "assessment: " + string(t.Assessment.Status) + " — " + t.Assessment.Headline
		}
		add("diagnosis qualified", q, true, detail)
		if t.Assessment != nil && q {
			want := diagnosesFor[wf]
			ok := false
			for _, d := range want {
				ok = ok || t.Assessment.Diagnosis == d
			}
			add("diagnosis matches workflow", ok, true, fmt.Sprintf("diagnosis %q; %s addresses only %v", t.Assessment.Diagnosis, wf, want))
		}
		if t.Assessment != nil {
			cur := in.Policy.Settings.Revision()
			same := t.Assessment.PolicyRevision == cur
			d := "the diagnosis was made under the current diagnostic settings (revision " + cur + ")"
			if !same {
				d = "the diagnosis was made under different diagnostic settings (revision " + nonEmpty(t.Assessment.PolicyRevision, "unrecorded") +
					", now " + cur + "); it must be re-evaluated under the current policy by the next observation cycle before any action"
			}
			add("diagnosis under current policy", same, true, d)
		}
	}
	want := map[policy.Workflow]domain.StateClass{
		policy.WorkflowNoProgressRecovery: domain.StateDownloading, policy.WorkflowMetadataRecovery: domain.StateMetadata,
		policy.WorkflowPreservedDataFailure: domain.StateDownloading,
	}[wf]
	add("applicable transfer state", t.StateClass == want, true,
		fmt.Sprintf("transfer is %s (native %q); this workflow applies only to %s transfers — paused, queued, and checking transfers are never acted on",
			t.StateClass, t.Latest.NativeState, want))

	// --- capability -------------------------------------------------------
	if wf == policy.WorkflowMetadataRecovery {
		st := adapter.CapabilityStatus{Support: adapter.SupportUnknown}
		if in.ClientProbe != nil {
			if c, ok := in.ClientProbe.Read[adapter.CapMetadataState]; ok {
				st = c
			}
		}
		add("metadata state supported", st.Support == adapter.Supported, true, fmt.Sprintf("the client's metadata-state capability is %s%s; "+
			"metadata recovery needs it so a stall is known to be metadata acquisition", st.Support, prefixed(": ", st.Note)))
	}

	// --- privacy ------------------------------------------------------------
	priv, known := t.Latest.Private.Get()
	optIn, optFrom := in.Policy.AllowsUnknownPrivacy(wf)
	optIn = optIn && policy.UnknownPrivacyDefined(wf)
	stop := true
	what := "private"
	if !known {
		what = "of unknown privacy (" + t.Latest.Private.String() + "), which is treated as private (unknown_privacy." + string(wf) + ", " + optFrom + ")"
	}
	from := nonEmpty(in.Policy.PrivateRecoveryFrom, "no policy stated")
	// private_recovery covers torrents known to be private and, unless the
	// scope turned that off, torrents of unknown privacy (user decisions).
	// Preserved-data handling of a tracker rejection follows it the same way
	// (user decision 2026-11-17).
	privateNP := (wf == policy.WorkflowNoProgressRecovery || wf == policy.WorkflowPreservedDataFailure) &&
		((known && priv) || (!known && optIn))
	switch {
	case known && !priv:
		add("public transfer", true, true, "the client reports the torrent as public")
	case privateNP && in.Policy.PrivateRecovery == policy.PrivateKeepRunning:
		p.PrivateMode, stop = policy.PrivateKeepRunning, false
		add("public transfer", true, true, "the torrent is "+what+": the grab is failed and blocklisted in the manager so a replacement can be fetched, "+
			"and the torrent is left running in the client, so nothing is stopped against tracker obligations (private_recovery = \"keep_running\", from "+
			from+"). It keeps its active slot until you stop or remove it")
	case privateNP && in.Policy.PrivateRecovery == policy.PrivateStop:
		p.PrivateMode = policy.PrivateStop
		add("public transfer", true, true, "the torrent is "+what+"; this scope chose to stop it as well (private_recovery = \"stop\", from "+from+
			"): it will no longer seed its partial data, which you have judged compatible with your tracker's rules")
	case privateNP:
		add("public transfer", false, true, "the torrent is "+what+", and this scope keeps such transfers observation-only (private_recovery = \"observe\", from "+
			from+"); \"keep_running\" would blocklist the grab and leave the torrent running")
	case known && priv:
		add("public transfer", false, true, "private torrents are observation-only for recovery: stopping or replacing one may conflict with tracker obligations")
	case optIn:
		p.UnknownPrivacy = true
		add("public transfer", true, true, "privacy cannot be known before metadata arrives ("+t.Latest.Private.String()+"). The client reports "+
			"no payload, so there is nothing to seed; this scope accepts stopping such a torrent (kept registered, with its category) by policy, "+
			"allowed by "+optFrom+". Housekeeparr cannot read a tracker's rules: set unknown_privacy."+string(wf)+" = false if yours forbid it")
	case policy.UnknownPrivacyDefined(wf):
		add("public transfer", false, true, "privacy is unknown ("+t.Latest.Private.String()+") and this scope leaves such transfers alone for "+
			string(wf)+" (unknown_privacy."+string(wf)+" = false, "+optFrom+")")
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
	p.Prerequisites = append(p.Prerequisites, PolicyCheck(in.Policy, wf, in.Origin), PauseCheck(in.Paused, wf))

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
	if !stop {
		p.Effects = append(p.Effects, "the torrent is not stopped: it stays active in the client with all its data and keeps seeding what it has")
	}
	if stop {
		p.Operations = append(p.Operations, Operation{
			Op:          "client.stop",
			Target:      raw(map[string]string{"client_id": t.ClientID, "native_id": t.NativeID}),
			Request:     raw(map[string]bool{"delete_data": false}),
			Expect:      raw(map[string]any{"state_class": domain.StatePaused, "registered": true}),
			Description: "Stop the torrent in the client, retaining it and its data, so it no longer occupies an active slot",
		})
		if t.StateClass == domain.StateMetadata {
			p.Effects = append(p.Effects, "the torrent is stopped and kept registered, with its category or label unchanged; it has no payload data yet (metadata was never acquired)")
		} else {
			p.Effects = append(p.Effects, "the torrent is stopped and kept, with all downloaded data and its category or label unchanged")
		}
	}
	// Any replacement is gated on current retained capacity, whether or not
	// this transfer is stopped; the transfer left behind must also fit.
	p.Prerequisites = append(p.Prerequisites, capacityCheck(in.Retained, limits), roomCheck(in, limits, t, stop))
	p.SideEffects = append(p.SideEffects,
		"no file is deleted, moved, or renamed; the transfer and its data stay in the client and are counted as retained "+
			"(housekeeparr retained) until the client no longer lists it",
		"after the steps above are verified, Housekeeparr follows the manager's replacement search, a replacement grab, and that "+
			"download's progress as separate outcomes for a bounded time; a queued search does not mean a release will be found or grabbed")
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

// PauseCheck refuses a workflow an operator paused: unlike disabled
// automation, a pause stops manual actions too. Planning and the per-write
// gate use it.
func PauseCheck(paused *store.WorkflowPause, wf policy.Workflow) Check {
	if paused == nil {
		return Check{Name: "workflow not paused", Satisfied: true, Hard: true, Detail: "not paused"}
	}
	return Check{Name: "workflow not paused", Hard: true, Detail: fmt.Sprintf("%s was paused by %s at %s: %s (resume it to allow actions again)",
		wf, paused.Actor, paused.PausedAt.UTC().Format(time.RFC3339), paused.Note)}
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

// retainedAdds is what leaving this transfer behind adds to the retained
// byte total: its logical wanted size when known. A transfer stopped
// before its metadata arrived has no payload and, while stopped, cannot
// obtain any, so it adds nothing (its logical size stays unknown and is
// refreshed if it is resumed). Otherwise an unknown size is unknown, never
// zero.
func retainedAdds(t *store.TransferRecord, stop bool) (int64, bool) {
	if v, ok := t.Latest.WantedBytes.Get(); ok {
		return v, true
	}
	if stop && t.StateClass == domain.StateMetadata {
		return 0, true
	}
	return 0, false
}

// capacityCheck is the gate on any replacement (SPEC §7.1): at or over a
// configured retained ceiling, no new replacement is requested, whatever
// this action itself would retain. With a byte ceiling, retained
// transfers of unknown size make the total unknown, which is not room.
func capacityCheck(u store.RetainedUsage, l policy.Limits) Check {
	var over []string
	if u.Count >= l.RetainedTransfers {
		over = append(over, fmt.Sprintf("the retained-transfer ceiling is reached (%d of %d, from %s)", u.Count, l.RetainedTransfers, l.From("retained_transfers")))
	}
	if l.RetainedBytes > 0 {
		switch {
		case u.UnknownSize > 0:
			over = append(over, fmt.Sprintf("%d retained transfer(s) are of unknown size, so the byte ceiling %s (from %s) cannot be shown to have room",
				u.UnknownSize, gib(l.RetainedBytes), l.From("retained_gib")))
		case u.KnownBytes >= l.RetainedBytes:
			over = append(over, fmt.Sprintf("the retained byte ceiling is reached (%s of %s, from %s)", gib(u.KnownBytes), gib(l.RetainedBytes), l.From("retained_gib")))
		}
	}
	d := usageDetail(u, l)
	if len(over) > 0 {
		d = strings.Join(over, "; ") + ". " + d + ". No replacement is requested in this scope until retained transfers are cleaned up " +
			"outside Housekeeparr (see housekeeparr retained) or the ceiling is raised"
	}
	return Check{Name: "retained-data ceiling", Satisfied: len(over) == 0, Hard: true, Detail: d + capacityLimits}
}

// roomCheck is the incremental cost: the transfer this action leaves
// behind, stopped or running, must itself fit.
func roomCheck(in PlanInput, l policy.Limits, t *store.TransferRecord, stop bool) Check {
	u := in.Retained
	how := "kept running"
	if stop {
		how = "stopped and kept"
	}
	ok := u.Count+1 <= l.RetainedTransfers
	d := fmt.Sprintf("this transfer is %s and counted as retained: %d of %d transfers afterwards", how, u.Count+1, l.RetainedTransfers)
	if l.RetainedBytes > 0 {
		size, known := retainedAdds(t, stop)
		switch {
		case !known:
			ok = false
			d += fmt.Sprintf("; its size is unknown, so the byte ceiling %s cannot be shown to hold", gib(l.RetainedBytes))
		default:
			ok = ok && u.KnownBytes+size <= l.RetainedBytes
			d += fmt.Sprintf("; %s of %s afterwards", gib(u.KnownBytes+size), gib(l.RetainedBytes))
			if size == 0 && t.StateClass == domain.StateMetadata {
				d += " (it has no payload: metadata never arrived)"
			}
		}
	}
	if !stop {
		d += "; a running transfer keeps downloading and seeding, so its use can grow up to its logical size"
	}
	return Check{Name: "room to retain this transfer", Satisfied: ok, Hard: true, Detail: d}
}

func usageDetail(u store.RetainedUsage, l policy.Limits) string {
	d := fmt.Sprintf("%d of %d retained transfers (limit from %s)", u.Count, l.RetainedTransfers, l.From("retained_transfers"))
	if u.Running > 0 {
		d += fmt.Sprintf(", %d of them left running", u.Running)
	}
	d += "; known logical size " + gib(u.KnownBytes)
	if u.UnknownSize > 0 {
		d += fmt.Sprintf(", %d of unknown size", u.UnknownSize)
	}
	if u.NoPayload > 0 {
		d += fmt.Sprintf(", %d stopped before metadata (no payload)", u.NoPayload)
	}
	if l.RetainedBytes > 0 {
		d += fmt.Sprintf("; byte ceiling %s (from %s)", gib(l.RetainedBytes), l.From("retained_gib"))
	} else {
		d += "; no byte ceiling is configured"
	}
	return d
}

const capacityLimits = ". Shared files and hardlinks make logical size an estimate, not disk use; free space is not measured, so no free-space threshold is enforced"

func gib(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

func prefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}
