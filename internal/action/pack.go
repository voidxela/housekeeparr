package action

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Partial season packs (SPEC §8). The action clears only the stale queue
// association of a completed Sonarr pack whose present episodes were
// imported, through the manager's ignore path with every flag explicit:
// removeFromClient=false, blocklist=false, skipRedownload=true,
// changeCategory=false (observed on Sonarr 4.0.20: every row of the
// download leaves the queue durably, one downloadIgnored history event per
// expected episode, torrent, payload and library files untouched).
//
// Which episodes were imported is not read when previewing (pages read
// local state only); it is established live, before the write, from the
// manager's import history for the download and the episodes' files
// (PackEvidence), and the write is refused unless that evidence is
// positive and complete.

// OpPackIgnore is the manager's ignore-path queue removal.
const OpPackIgnore = "manager.queue.ignore"

// PackTarget is the ignore operation's target.
type PackTarget struct {
	ManagerID  string  `json:"manager_id"`
	QueueID    int64   `json:"queue_id"`
	DownloadID string  `json:"download_id"`
	EpisodeIDs []int64 `json:"episode_ids"`
}

// PackEvidence is what the live check establishes about a pack.
type PackEvidence struct {
	// Imported are the expected episodes with a downloadFolderImported
	// history record for this download and a file now.
	Imported []int64 `json:"imported"`
	// Missing are the expected episodes without an import record and
	// without a file.
	Missing []int64 `json:"missing"`
	// Unexplained lists anything that makes the cause uncertain: an
	// imported episode without a file, a file without an import record
	// from this download, or another import message.
	Unexplained []string `json:"unexplained,omitempty"`
}

// PackPlanInput is everything PlanPack needs, read from local state.
type PackPlanInput struct {
	Finding      domain.Finding
	Rows         []store.QueueRecord // present rows of the download
	Policy       policy.Effective
	Scope        policy.Subject
	Origin       Origin
	ManagerKind  adapter.Kind
	ManagerProbe *adapter.Probe
	// ClientKind/ClientProbe: the download client holding the pack; how a
	// manager imports from it differs by client, so registration is per
	// exact manager and client versions.
	ClientKind     adapter.Kind
	ClientProbe    *adapter.Probe
	Locks          map[string]int64
	Cooldowns      map[string]store.CooldownRecord
	Paused         *store.WorkflowPause
	Handled        int64 // an earlier succeeded clearance of this download
	Tested         []TestedWorkflow
	ConfigRevision string
	Now            time.Time
	PreviewTTL     time.Duration
}

// PlanPack builds the clearance proposal for a partial-pack finding.
func PlanPack(in PackPlanInput) *Proposal {
	wf := policy.WorkflowPartialPack
	ttl := in.PreviewTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	f := in.Finding
	p := &Proposal{Workflow: string(wf), Origin: in.Origin, Scope: in.Scope, PolicyRevision: in.Policy.Revision, ConfigRevision: in.ConfigRevision,
		Subject: raw(map[string]string{"manager_id": f.Subject.ManagerID, "download_id": f.Subject.DownloadID,
			"client_id": f.Subject.ClientID, "native_id": f.Subject.NativeID}), CreatedAt: in.Now, ExpiresAt: in.Now.Add(ttl),
		LiveChecks: []string{"every row of the download is still in the queue, import-blocked with the partial-pack message and nothing else",
			"the manager's import history for the download covers exactly the episodes that have files; the others have none",
			"the manager version is unchanged"}}
	fid := f.ID
	p.FindingID = &fid
	add := func(name string, ok bool, detail string) {
		p.Prerequisites = append(p.Prerequisites, Check{Name: name, Satisfied: ok, Hard: true, Detail: detail})
	}
	mv, cv := probeVersion(in.ManagerProbe), probeVersion(in.ClientProbe)
	tested := false
	for _, tw := range in.Tested {
		tested = tested || tw.Workflow == wf && tw.Manager == in.ManagerKind && tw.ManagerVersion == mv && tw.Client == in.ClientKind && tw.ClientVersion == cv
	}
	combo := fmt.Sprintf("%s %s with %s %s", nonEmpty(string(in.ManagerKind), "an unknown manager"), nonEmpty(mv, "(version unknown)"),
		nonEmpty(string(in.ClientKind), "an unknown client"), nonEmpty(cv, "(version unknown)"))
	if tested {
		add("tested write workflow", true, "partial-pack clearance passed disposable-instance tests for "+combo)
	} else {
		add("tested write workflow", false, "no partial-pack workflow has been tested for "+combo+"; this build sends nothing there")
	}
	switch f.State {
	case domain.FindingOpen, domain.FindingAcknowledged:
		add("finding is active", true, "the finding is "+string(f.State))
	case domain.FindingSnoozed:
		add("finding is active", in.Origin == OriginManual, "the finding is snoozed; snoozing suppresses automation")
	default:
		add("finding is active", false, "the finding is "+string(f.State))
	}
	var ids []int64
	var queueID int64
	same := true
	for _, r := range in.Rows {
		if !r.Present {
			continue
		}
		ids = append(ids, r.Latest.EpisodeID)
		if queueID == 0 {
			queueID = r.QueueID
		}
		same = same && strings.EqualFold(r.DownloadID, f.Subject.DownloadID) && r.ManagerID == f.Subject.ManagerID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	add("pack rows present", len(ids) >= 2 && same, fmt.Sprintf("%d queue rows for download %s", len(ids), f.Subject.DownloadID))
	if in.Handled != 0 {
		add("not already cleared", false, fmt.Sprintf("action #%d already cleared this download's association, and it is tracked again: "+
			"that is a failure to diagnose, not to repeat", in.Handled))
	}
	var keys []string
	for _, id := range ids {
		keys = append(keys, fmt.Sprintf("episode:%d", id))
		p.Locks = append(p.Locks, ContentLock(f.Subject.ManagerID, fmt.Sprintf("episode:%d", id)))
	}
	var held []string
	for _, l := range p.Locks {
		if h := in.Locks[l]; h != 0 {
			held = append(held, fmt.Sprintf("action #%d holds %s", h, l))
		}
		if c, ok := in.Cooldowns[l]; ok && in.Now.Before(c.Until) {
			held = append(held, fmt.Sprintf("%s in cooldown until %s", l, c.Until.UTC().Format(time.RFC3339)))
		}
	}
	add("no conflicting action", len(held) == 0, nonEmpty(strings.Join(held, "; "), "no other action holds these episodes"))
	p.Prerequisites = append(p.Prerequisites, PolicyCheck(in.Policy, wf, in.Origin), PauseCheck(in.Paused, wf))
	p.Operations = []Operation{{
		Op:          OpPackIgnore,
		Target:      raw(PackTarget{ManagerID: f.Subject.ManagerID, QueueID: queueID, DownloadID: f.Subject.DownloadID, EpisodeIDs: ids}),
		Request:     raw(map[string]bool{"removeFromClient": false, "blocklist": false, "skipRedownload": true, "changeCategory": false}),
		Expect:      raw(map[string]any{"queue_rows_absent": true, "history_event": "downloadIgnored"}),
		Description: fmt.Sprintf("Clear %s's stale association with this pack (the ignore path: no blocklist, no removal from the client)", f.Subject.ManagerID),
	}}
	p.Effects = []string{
		fmt.Sprintf("%s stops tracking the pack, so its rows leave the queue; the episodes it imported keep their files", f.Subject.ManagerID),
		"the release is not blocklisted, so it stays usable for other episodes",
	}
	p.SideEffects = []string{
		"nothing is sent to the download client: the torrent keeps seeding with all its files",
		"the episodes the pack did not contain stay missing; they are searched individually (never by season) by proactive searches where enabled",
	}
	p.Planned = Planned{ManagerID: f.Subject.ManagerID, ManagerVersion: mv, QueueID: queueID, DownloadID: f.Subject.DownloadID, Content: keys}
	p.Evidence = raw(map[string]any{"finding": f.ID, "rows": len(ids)})
	p.Fingerprint = digest(fmt.Sprintf("pack|%s|%s|%v|%s", f.Subject.ManagerID, strings.ToLower(f.Subject.DownloadID), ids, mv))[:16]
	return p
}
