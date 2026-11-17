package action

import (
	"fmt"
	"sort"
	"strings"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
)

// Planned is the external, decision-relevant state a proposal was planned
// against. Immediately before each consequential write the coordinator
// compares it with a fresh Live read (Revalidate); the comparison is
// step-aware, so the intended effects of earlier steps are expected rather
// than mistaken for divergence.
type Planned struct {
	ClientID      string                `json:"client_id"`
	NativeID      string                `json:"native_id"`
	Name          string                `json:"name,omitempty"`
	StateClass    domain.StateClass     `json:"state_class"`
	Verified      domain.Signal[int64]  `json:"verified_bytes"`
	Wanted        domain.Signal[int64]  `json:"wanted_bytes"`
	Private       domain.Signal[bool]   `json:"private"`
	Category      domain.Signal[string] `json:"category"`
	ClientVersion string                `json:"client_version"`

	ManagerID      string `json:"manager_id,omitempty"`
	ManagerVersion string `json:"manager_version,omitempty"`
	QueueID        int64  `json:"queue_id,omitempty"`
	DownloadID     string `json:"download_id,omitempty"`
	// Content lists the manager content keys (episode:<id>, movie:<id>)
	// the association is for, sorted.
	Content []string `json:"content,omitempty"`
	// Owners are the managers that could own this transfer (configured to
	// use its client, or unmapped); their queues are read before every
	// write. Empty means every manager.
	Owners []string `json:"owners,omitempty"`
}

// fingerprint digests the identity-relevant planned facts. It feeds the
// local idempotency key, so it deliberately excludes volatile facts such
// as health that do not change what the action is.
func (p Planned) fingerprint() string {
	v, vk := p.Verified.Get()
	w, wk := p.Wanted.Get()
	pr, pk := p.Private.Get()
	c, ck := p.Category.Get()
	return digest(fmt.Sprintf("v2|%s|%s|%s|%v:%d|%v:%d|%v:%v|%v:%q|%s|%d|%s|%s", p.ClientID, p.NativeID, p.StateClass,
		vk, v, wk, w, pk, pr, ck, c, p.ManagerID, p.QueueID, strings.ToLower(p.DownloadID), strings.Join(p.Content, ",")))
}

// ContentState is the live state of one managed episode or movie.
type ContentState struct {
	Key       string `json:"key"`
	Exists    bool   `json:"exists"`
	Monitored bool   `json:"monitored"`
	HasFile   *bool  `json:"has_file,omitempty"`
	Available *bool  `json:"available,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Live is a fresh read of everything Revalidate compares. Read failures of
// optional parts are recorded rather than returned, so the checks can
// explain exactly what could not be established; every such gap fails
// closed.
type Live struct {
	// Transfer is nil when the client no longer lists the transfer.
	Transfer      *domain.Transfer `json:"transfer,omitempty"`
	ClientVersion string           `json:"client_version"`
	// Associations are the queue rows, in every manager that could own the
	// transfer (Planned.Owners), whose download ID is the transfer's.
	Associations []domain.QueueItem `json:"associations"`
	// Unread names managers whose queue could not be read completely, so
	// an association there cannot be ruled out.
	Unread []string `json:"unread,omitempty"`

	ManagerVersion string              `json:"manager_version,omitempty"`
	Content        []ContentState      `json:"content,omitempty"`
	ContentErr     string              `json:"content_error,omitempty"`
	Health         []domain.HealthItem `json:"health,omitempty"`
	HealthErr      string              `json:"health_error,omitempty"`
	// Redownload is whether failing the grab makes the manager search for
	// a replacement itself (the recovery's only replacement owner).
	Redownload    *Redownload `json:"redownload,omitempty"`
	RedownloadErr string      `json:"redownload_error,omitempty"`
	// Pack is the import evidence for a partial-pack clearance.
	Pack    *PackEvidence `json:"pack,omitempty"`
	PackErr string        `json:"pack_error,omitempty"`
}

// Redownload is the manager's redownload configuration and the grab's
// release source, as read live.
type Redownload struct {
	Failed bool `json:"auto_redownload_failed"`
	// FromInteractive is nil when the manager does not report it.
	FromInteractive *bool `json:"from_interactive_search,omitempty"`
	// GrabSource is the grab's release source from history ("" unknown).
	GrabSource string `json:"grab_source,omitempty"`
}

// LiveChecks describes, for previews, what is re-read before every write.
var LiveChecks = []string{
	"the torrent is still registered, in the same state, with no new verified progress and the same file selection",
	"privacy (public) and category are unchanged",
	"client and manager versions are still the tested ones",
	"no other queue row in any manager refers to this torrent; the planned row is unchanged before the manager step",
	"before the manager step: the content still exists and is monitored, and the manager reports no download-client or search-indexer failure",
	"before the manager step: the manager's \"Redownload Failed\" setting is on (and, for a release grabbed through interactive search, \"Redownload Failed from Interactive Search\"), so failing the grab makes the manager search for a replacement",
	"mode, emergency stop, exclusions, and workflow permission still allow the action",
}

// acquisitionOps are operations whose acceptance starts an acquisition
// (here: the manager-native replacement search after a failed grab).
var acquisitionOps = map[string]bool{"manager.queue.fail": true}

// Revalidate compares a fresh read with the planned state before
// operation i of p. Before the first operation everything must match.
// After the manager has failed the grab, its queue row is expected to be
// gone, and any association that appears is a conflict; the transfer must
// still be registered, unchanged, and without progress, because progress,
// completion, a pause or recheck by someone else, or a new owner between
// the steps means the remaining steps no longer apply as planned.
func Revalidate(p Proposal, i int, live Live) []Check {
	if p.Workflow == string(policy.WorkflowMissingSearch) {
		return revalidateSearch(p, live)
	}
	if p.Workflow == string(policy.WorkflowPartialPack) {
		return revalidatePack(p, live)
	}
	pl := p.Planned
	var out []Check
	add := func(name string, ok bool, detail string) {
		out = append(out, Check{Name: name, Satisfied: ok, Hard: true, Detail: detail})
	}
	failedBefore := false
	for _, op := range p.Operations[:i] {
		failedBefore = failedBefore || op.Op == "manager.queue.fail"
	}
	acquiring := i < len(p.Operations) && acquisitionOps[p.Operations[i].Op]

	// --- transfer --------------------------------------------------------
	t := live.Transfer
	if t == nil {
		add("transfer still registered", false, "the client no longer lists the torrent")
	} else {
		add("transfer still registered", true, "the client lists the torrent")
		add("transfer state unchanged", t.StateClass == pl.StateClass,
			fmt.Sprintf("planned %s, now %s (native %q)", pl.StateClass, t.StateClass, t.NativeState))
		add("no new verified progress", sameSignal(pl.Verified, t.VerifiedBytes),
			fmt.Sprintf("verified bytes planned %s, now %s", pl.Verified, t.VerifiedBytes))
		add("file selection unchanged", sameSignal(pl.Wanted, t.WantedBytes),
			fmt.Sprintf("wanted bytes planned %s, now %s", pl.Wanted, t.WantedBytes))
		priv, known := t.Private.Get()
		if p.PrivateMode != "" {
			add("public transfer", true, "privacy now "+t.Private.String()+"; planned under private_recovery = \""+string(p.PrivateMode)+"\"")
		} else if p.UnknownPrivacy && !known {
			add("public transfer", true, "privacy still unknown ("+t.Private.String()+"); still no metadata, so still no payload")
		} else {
			add("public transfer", known && !priv, "privacy now "+t.Private.String())
		}
		if p.Workflow == string(policy.WorkflowPreservedDataFailure) && !failedBefore {
			v, ok := t.SourceFailure.Get()
			add("tracker still rejects it", ok && v == domain.SourceUnregistered, "tracker result now "+t.SourceFailure.String()+
				"; the grab is failed only while the tracker still reports the torrent as not registered")
		}
		add("category unchanged", sameSignal(pl.Category, t.Category),
			fmt.Sprintf("category planned %s, now %s", pl.Category, t.Category))
	}
	add("client version unchanged", normVersion(live.ClientVersion) == normVersion(pl.ClientVersion),
		fmt.Sprintf("planned %q, now %q", pl.ClientVersion, live.ClientVersion))

	// --- ownership ---------------------------------------------------------
	switch {
	case len(live.Unread) > 0:
		add("ownership unchanged", false, "could not read the queue of "+strings.Join(live.Unread, ", ")+", so another association cannot be ruled out")
	case failedBefore && len(live.Associations) > 0:
		add("ownership unchanged", false, fmt.Sprintf("after the grab was failed, %s; it belongs to someone else now", describeRows(live.Associations)))
	case failedBefore:
		add("ownership unchanged", true, "no queue row refers to the torrent any more, as expected after failing the grab")
	case len(live.Associations) != 1:
		add("ownership unchanged", false, fmt.Sprintf("planned one association, now %s", describeRows(live.Associations)))
	default:
		r := live.Associations[0]
		keys := contentKeys(live.Associations)
		same := r.ManagerID == pl.ManagerID && r.QueueID == pl.QueueID && strings.EqualFold(r.DownloadID, pl.DownloadID) &&
			strings.Join(keys, ",") == strings.Join(pl.Content, ",")
		add("ownership unchanged", same, fmt.Sprintf("planned %s queue item %d for %s, now %s", pl.ManagerID, pl.QueueID,
			strings.Join(pl.Content, ", "), describeRows(live.Associations)))
	}

	// --- manager, before an acquisition ------------------------------------
	if acquiring {
		add("manager version unchanged", normVersion(live.ManagerVersion) == normVersion(pl.ManagerVersion),
			fmt.Sprintf("planned %q, now %q", pl.ManagerVersion, live.ManagerVersion))
		switch {
		case live.ContentErr != "":
			add("content monitored", false, "content state could not be read: "+live.ContentErr)
		case len(live.Content) != len(pl.Content) || len(pl.Content) == 0:
			add("content monitored", false, fmt.Sprintf("content state read for %d of %d items", len(live.Content), len(pl.Content)))
		default:
			ok, details := true, []string{}
			for _, c := range live.Content {
				ok = ok && c.Exists && c.Monitored
				details = append(details, c.Key+": "+c.Detail)
			}
			add("content monitored", ok, strings.Join(details, "; ")+
				" (a replacement is only wanted, and only searched by the manager, for existing monitored content)")
		}
		const owns = "manager searches for the replacement"
		switch rd := live.Redownload; {
		case live.RedownloadErr != "":
			add(owns, false, "the manager's redownload settings could not be read: "+live.RedownloadErr)
		case rd == nil:
			add(owns, false, "the manager's redownload settings were not read")
		case !rd.Failed:
			add(owns, false, "the manager's \"Redownload Failed\" setting is off, so failing this grab would not make it search for a replacement, "+
				"and Housekeeparr-scheduled replacement searches are not implemented")
		case (rd.GrabSource == "InteractiveSearch" || rd.GrabSource == "") && (rd.FromInteractive == nil || !*rd.FromInteractive):
			src := "was grabbed through interactive search"
			if rd.GrabSource == "" {
				src = "has no known release source in history (it may have been grabbed interactively)"
			}
			add(owns, false, "this release "+src+" and the manager's \"Redownload Failed from Interactive Search\" setting is off or not reported, "+
				"so the manager might not search for a replacement")
		default:
			add(owns, true, fmt.Sprintf("the manager redownloads failed grabs (this grab's source: %s)", nonEmpty(rd.GrabSource, "unknown")))
		}
		if live.HealthErr != "" {
			add("manager health", false, "health could not be read: "+live.HealthErr)
		} else {
			bad := RelevantHealth(live.Health)
			add("manager health", len(bad) == 0, nonEmpty(strings.Join(bad, "; "),
				"no download-client or search-indexer failure reported"))
		}
	}
	return out
}

// RelevantHealth returns the manager health problems that make failing a
// grab unsafe: without a working download client or search-capable
// indexer the replacement cannot happen, and failing grabs then only
// blocklists releases (SPEC §13.4 scenario 5). RSS-only checks are not
// relevant: the replacement is a search. Released Sonarr 4.0.20 reports
// IndexerSearchCheck as a warning even when no indexer can search.
func RelevantHealth(items []domain.HealthItem) []string {
	var out []string
	for _, h := range items {
		always := h.Source == "IndexerSearchCheck" || h.Source == "DownloadClientCheck" || h.Source == "DownloadClientStatusCheck"
		failing := h.Type == "error" && (h.Source == "IndexerStatusCheck" || h.Source == "IndexerLongTermStatusCheck" ||
			strings.HasPrefix(h.Source, "DownloadClient"))
		if always || failing {
			out = append(out, fmt.Sprintf("%s (%s): %s", h.Source, h.Type, h.Message))
		}
	}
	return out
}

func sameSignal[T comparable](a, b domain.Signal[T]) bool {
	av, ak := a.Get()
	bv, bk := b.Get()
	return ak == bk && (!ak || av == bv)
}

func normVersion(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

func contentKeys(rows []domain.QueueItem) []string {
	var keys []string
	for _, r := range rows {
		if k, ok := r.ContentKey(); ok {
			keys = append(keys, k)
		} else {
			keys = append(keys, "unknown content")
		}
	}
	sort.Strings(keys)
	return keys
}

func describeRows(rows []domain.QueueItem) string {
	if len(rows) == 0 {
		return "no queue row refers to the torrent"
	}
	var parts []string
	for _, r := range rows {
		k, ok := r.ContentKey()
		if !ok {
			k = "unknown content"
		}
		parts = append(parts, fmt.Sprintf("%s queue item %d (%s)", r.ManagerID, r.QueueID, k))
	}
	return fmt.Sprintf("%d queue row(s) refer to it: %s", len(rows), strings.Join(parts, ", "))
}

// revalidateSearch re-checks a search immediately before its request: the
// manager is unchanged and fully readable, nothing in its queue is for the
// content, and the content still exists and is monitored.
func revalidateSearch(p Proposal, live Live) []Check {
	pl := p.Planned
	var out []Check
	add := func(name string, ok bool, detail string) {
		out = append(out, Check{Name: name, Satisfied: ok, Hard: true, Detail: detail})
	}
	add("manager version unchanged", normVersion(live.ManagerVersion) == normVersion(pl.ManagerVersion),
		fmt.Sprintf("planned %s, now %s", pl.ManagerVersion, nonEmpty(live.ManagerVersion, "unknown")))
	switch {
	case len(live.Unread) > 0:
		add("not queued", false, "could not read the queue of "+strings.Join(live.Unread, ", ")+", so a download for it cannot be ruled out")
	case len(live.Associations) > 0:
		add("not queued", false, "the manager is now downloading or importing it: "+describeRows(live.Associations))
	default:
		add("not queued", true, "no queue item for the content")
	}
	switch {
	case live.ContentErr != "":
		add("content monitored", false, "content state could not be read: "+live.ContentErr)
	case len(live.Content) != len(pl.Content):
		add("content monitored", false, fmt.Sprintf("content state read for %d of %d items", len(live.Content), len(pl.Content)))
	default:
		ok, details := true, []string{}
		for _, c := range live.Content {
			ok = ok && c.Exists && c.Monitored
			details = append(details, fmt.Sprintf("%s exists=%v monitored=%v", c.Key, c.Exists, c.Monitored))
		}
		add("content monitored", ok, strings.Join(details, "; "))
	}
	missing := len(live.Content) == len(pl.Content) && live.ContentErr == ""
	var fileState []string
	for _, c := range live.Content {
		switch {
		case c.HasFile == nil:
			missing = false
			fileState = append(fileState, c.Key+": file status unknown")
		case *c.HasFile:
			missing = false
			fileState = append(fileState, c.Key+": already has a file")
		}
	}
	add("content still missing", missing, nonEmpty(strings.Join(fileState, "; "), "the manager confirms file absence for the target"))
	available := len(live.Content) == len(pl.Content) && live.ContentErr == ""
	for _, c := range live.Content {
		available = available && c.Available != nil && *c.Available
	}
	add("content still available", available, "live manager evidence must establish that the target is aired or released")
	return out
}

// revalidatePack re-checks a partial-pack clearance immediately before the
// write: the same rows are still tracked, nothing else is reported, and the
// import evidence is positive and complete.
func revalidatePack(p Proposal, live Live) []Check {
	pl := p.Planned
	var out []Check
	add := func(name string, ok bool, detail string) {
		out = append(out, Check{Name: name, Satisfied: ok, Hard: true, Detail: detail})
	}
	add("manager version unchanged", normVersion(live.ManagerVersion) == normVersion(pl.ManagerVersion),
		fmt.Sprintf("planned %s, now %s", pl.ManagerVersion, nonEmpty(live.ManagerVersion, "unknown")))
	want := map[string]bool{}
	for _, k := range pl.Content {
		want[k] = true
	}
	got := map[string]bool{}
	targetPresent := false
	var bad []string
	for _, r := range live.Associations {
		if k, ok := r.ContentKey(); ok {
			got[k] = true
		}
		targetPresent = targetPresent || r.QueueID == pl.QueueID
		ts, _ := r.TrackedState.Get()
		if ts != "importBlocked" {
			bad = append(bad, fmt.Sprintf("%s is %s", r.Title, nonEmpty(ts, "unknown")))
		}
		partial := false
		for _, msg := range r.StatusMessages {
			partial = partial || msg.Title == domain.PartialPackMessage
		}
		if !partial {
			bad = append(bad, fmt.Sprintf("row %d no longer carries the partial-pack message", r.QueueID))
		}
	}
	same := targetPresent && len(live.Associations) == len(pl.Content) && len(got) == len(want) && len(live.Unread) == 0
	for k := range want {
		same = same && got[k]
	}
	add("same pack rows", same && len(bad) == 0, fmt.Sprintf("target row %d present: %t, planned %d rows, now %d; %s",
		pl.QueueID, targetPresent, len(want), len(live.Associations), nonEmpty(strings.Join(append(bad, live.Unread...), "; "), "all import-blocked")))
	switch {
	case live.PackErr != "":
		add("positive import evidence", false, "could not be read: "+live.PackErr)
	case live.Pack == nil:
		add("positive import evidence", false, "not read")
	default:
		ev := live.Pack
		ok := len(ev.Imported) > 0 && len(ev.Missing) > 0 && len(ev.Unexplained) == 0 && len(ev.Imported)+len(ev.Missing) == len(want)
		add("positive import evidence", ok, fmt.Sprintf("imported (history record and file) %v; missing (neither) %v%s", ev.Imported, ev.Missing,
			prefixed("; uncertain: ", strings.Join(ev.Unexplained, "; "))))
	}
	return out
}
