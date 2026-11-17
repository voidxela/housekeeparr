package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// Workflow names an individually-permissioned automation workflow.
type Workflow string

const (
	WorkflowNoProgressRecovery   Workflow = "no_progress_recovery"
	WorkflowMetadataRecovery     Workflow = "metadata_recovery"
	WorkflowPreservedDataFailure Workflow = "preserved_data_failure"
	WorkflowPartialPack          Workflow = "partial_pack"
	WorkflowMissingSearch        Workflow = "missing_search"
)

// AllWorkflows returns every defined workflow in canonical order.
func AllWorkflows() []Workflow {
	return []Workflow{
		WorkflowNoProgressRecovery,
		WorkflowMetadataRecovery,
		WorkflowPreservedDataFailure,
		WorkflowPartialPack,
		WorkflowMissingSearch,
	}
}

// ValidWorkflow reports whether w is a defined workflow.
func ValidWorkflow(w Workflow) bool {
	switch w {
	case WorkflowNoProgressRecovery,
		WorkflowMetadataRecovery,
		WorkflowPreservedDataFailure,
		WorkflowPartialPack,
		WorkflowMissingSearch:
		return true
	default:
		return false
	}
}

// Subject identifies the transfer, content, and connection scope being
// evaluated for policy resolution.
type Subject struct {
	ClientID   string `json:"client_id,omitempty"`
	Category   string `json:"category,omitempty"`
	NativeID   string `json:"native_id,omitempty"`
	ManagerID  string `json:"manager_id,omitempty"`
	ContentKey string `json:"content_key,omitempty"`
	Privacy    string `json:"privacy,omitempty"` // public, private, unknown, or ""
}

// Association is one present manager queue row related to a transfer.
type Association struct {
	ManagerID string
	Item      domain.QueueItem
}

// TransferSubject is the policy subject of a transfer: its client and
// native ID, category and privacy when known (privacy "unknown" otherwise),
// and, when exactly one manager association is present, that manager's
// content identity (not the release, so overrides and exclusions follow the
// episode or movie across replacement attempts). Diagnosis and action
// planning both resolve this subject, so scoped rules and item overrides
// apply identically to the diagnosis windows and to the action.
func TransferSubject(clientID, nativeID string, t *domain.Transfer, present []Association) Subject {
	sub := Subject{ClientID: clientID, NativeID: nativeID}
	if t != nil {
		if c, ok := t.Category.Get(); ok {
			sub.Category = c
		}
		switch p, ok := t.Private.Get(); {
		case !ok:
			sub.Privacy = "unknown"
		case p:
			sub.Privacy = "private"
		default:
			sub.Privacy = "public"
		}
	}
	if len(present) == 1 {
		sub.ManagerID = present[0].ManagerID
		sub.ContentKey, _ = present[0].Item.ContentKey()
	}
	return sub
}

// WorkflowPermission records whether automation is enabled for one workflow
// and where that decision came from.
type WorkflowPermission struct {
	Workflow Workflow `json:"workflow"`
	Enabled  bool     `json:"enabled"`
	From     string   `json:"from"`
}

// UnknownPrivacyDefined reports whether a workflow may be opted in to act
// on transfers whose privacy is unknown. Only metadata recovery: before
// metadata arrives no supported client can report the private flag, and
// released Sonarr/Radarr expose no indexer privacy. The opt-in is a
// proposal (not an approved default): it is off unless configured, never
// applies to a transfer known to be private, and the operator accepts that
// stopping and blocklisting a release whose tracker obligations are
// unknown is compatible with those obligations.
func UnknownPrivacyDefined(w Workflow) bool { return w == WorkflowMetadataRecovery }

// AllowsUnknownPrivacy reports whether the workflow is opted in to acting
// on transfers of unknown privacy in this scope, and where that is set.
func (e Effective) AllowsUnknownPrivacy(w Workflow) (bool, string) {
	if !UnknownPrivacyDefined(w) {
		return false, "not defined for " + string(w)
	}
	for _, p := range e.UnknownPrivacy {
		if p.Workflow == w {
			return p.Enabled, p.From
		}
	}
	return false, "not defined for " + string(w)
}

// ExclusionMatch records an exclusion entry matching the evaluated subject.
type ExclusionMatch struct {
	Workflows []Workflow `json:"workflows"`
	Reason    string     `json:"reason"`
	From      string     `json:"from"`
}

// ComputeRevision returns a deterministic short digest of the effective
// settings, mode, emergency stop, workflow permissions, and matching
// exclusions. It is independent of map iteration order.
func (e Effective) ComputeRevision() string {
	h := sha256.New()
	fmt.Fprintf(h, "v2|settings=%s|mode=%q|estop=%t", e.Settings.Revision(), e.Mode, e.EmergencyStop)

	perms := append([]WorkflowPermission(nil), e.Automation...)
	sort.Slice(perms, func(i, j int) bool { return perms[i].Workflow < perms[j].Workflow })
	for _, p := range perms {
		fmt.Fprintf(h, "|perm=%q:%t", p.Workflow, p.Enabled)
	}
	// Only opt-ins are hashed, so configurations without one keep their
	// earlier revision.
	for _, p := range e.UnknownPrivacy {
		if p.Enabled {
			fmt.Fprintf(h, "|unknown-privacy=%q", p.Workflow)
		}
	}

	type normExcl struct {
		from   string
		reason string
		wfs    []Workflow
	}
	excls := make([]normExcl, len(e.Exclusions))
	for i, ex := range e.Exclusions {
		wfs := append([]Workflow(nil), ex.Workflows...)
		sort.Slice(wfs, func(a, b int) bool { return wfs[a] < wfs[b] })
		excls[i] = normExcl{from: ex.From, reason: ex.Reason, wfs: wfs}
	}
	sort.Slice(excls, func(i, j int) bool {
		if excls[i].from != excls[j].from {
			return excls[i].from < excls[j].from
		}
		if excls[i].reason != excls[j].reason {
			return excls[i].reason < excls[j].reason
		}
		return slices.Compare(excls[i].wfs, excls[j].wfs) < 0
	})
	for _, ex := range excls {
		fmt.Fprintf(h, "|excl=%q:%q:%q", ex.from, ex.reason, ex.wfs)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Permits reports whether automation of w is permitted by mode, emergency
// stop, workflow enablement, and exclusions, returning human-readable reasons
// for every blocker. Hard safety prerequisites are evaluated elsewhere.
func (e Effective) Permits(w Workflow) (bool, []string) {
	var blockers []string
	if e.Mode != "automatic" {
		if e.Mode == "" {
			blockers = append(blockers, "mode is unset")
		} else {
			blockers = append(blockers, fmt.Sprintf("mode is %s", e.Mode))
		}
	}
	if e.EmergencyStop {
		blockers = append(blockers, "emergency stop is set")
	}

	if !ValidWorkflow(w) {
		blockers = append(blockers, fmt.Sprintf("unknown workflow %q", w))
	} else {
		found := false
		for _, p := range e.Automation {
			if p.Workflow != w {
				continue
			}
			found = true
			if !p.Enabled {
				if p.From != "" {
					blockers = append(blockers, fmt.Sprintf("%s is not enabled (%s)", w, p.From))
				} else {
					blockers = append(blockers, fmt.Sprintf("%s is not enabled", w))
				}
			}
			break
		}
		if !found {
			blockers = append(blockers, fmt.Sprintf("%s is not enabled (not configured)", w))
		}
	}

	for _, ex := range e.Exclusions {
		if slices.Contains(ex.Workflows, w) {
			if ex.From != "" {
				blockers = append(blockers, fmt.Sprintf("excluded: %s (%s)", ex.Reason, ex.From))
			} else {
				blockers = append(blockers, fmt.Sprintf("excluded: %s", ex.Reason))
			}
		}
	}

	return len(blockers) == 0, blockers
}
