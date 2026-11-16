package action

import (
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// BudgetClaim reserves units of a persistent budget (store.BudgetPacing
// or store.BudgetAttempts).
type BudgetClaim struct {
	Kind   string        `json:"kind"`
	Scope  string        `json:"scope"`
	Label  string        `json:"label"`
	Window time.Duration `json:"window,omitempty"`
	Limit  int           `json:"limit"`
	Units  int           `json:"units"`
	// From is the provenance of the limit.
	From string `json:"from"`
}

// Budget returns the store budget the claim reserves against.
func (b BudgetClaim) Budget() store.Budget {
	return store.Budget{Kind: b.Kind, Scope: b.Scope, Window: b.Window, Limit: b.Limit}
}

// AttemptScope is the attempts budget scope for manager content.
func AttemptScope(managerID, contentKey string) string {
	return "attempts:" + managerID + "|" + contentKey
}

// acquisitionClaims are the budgets an acquisition-starting action on one
// manager's content reserves: installation-wide and per-manager pacing,
// and the content's attempt allowance.
func acquisitionClaims(l policy.Limits, managerID string, contentKeys []string) []BudgetClaim {
	win := policy.HumanDuration(l.AcquisitionWindow)
	out := []BudgetClaim{
		{Kind: store.BudgetPacing, Scope: "pacing:global", Label: "acquisition pacing, all managers (" + win + ")",
			Window: l.AcquisitionWindow, Limit: l.Acquisitions, Units: 1, From: l.From("acquisitions")},
		{Kind: store.BudgetPacing, Scope: "pacing:manager:" + managerID, Label: "acquisition pacing, " + managerID + " (" + win + ")",
			Window: l.AcquisitionWindow, Limit: l.ManagerAcquisitions, Units: 1, From: l.From("manager_acquisitions")},
	}
	for _, k := range contentKeys {
		out = append(out, BudgetClaim{Kind: store.BudgetAttempts, Scope: AttemptScope(managerID, k), Label: "recovery attempts for " + managerID + "|" + k,
			Limit: l.ContentAttempts, Units: 1, From: l.From("content_attempts")})
	}
	return out
}

// BudgetCheck explains a claim against its current usage. Pacing and
// attempt exhaustion are explained differently: pacing defers eligible
// work and returns by itself; exhausted attempts need review.
func BudgetCheck(b BudgetClaim, u store.BudgetUsage) Check {
	c := Check{Name: b.Label, Satisfied: u.Fits(b.Units), Hard: true}
	switch b.Kind {
	case store.BudgetPacing:
		c.Detail = fmt.Sprintf("%d of %d used in the last %s (limit from %s)", u.Used, u.Limit, policy.HumanDuration(b.Window), b.From)
		if !c.Satisfied {
			c.Detail += "; this is pacing across the installation, not a judgement about this item: the finding stays open"
			if u.NextAt != nil {
				c.Detail += " and becomes eligible again at " + u.NextAt.UTC().Format(time.RFC3339)
			}
		}
	case store.BudgetAttempts:
		since := "ever"
		if !u.Since.IsZero() {
			since = "since the reset at " + u.Since.UTC().Format(time.RFC3339)
		}
		c.Detail = fmt.Sprintf("%d of %d recovery attempts used %s (limit from %s)", u.Used, u.Limit, since, b.From)
		if !c.Satisfied {
			c.Detail += "; repeated replacements have not resolved this content, so further attempts need review. " +
				"This does not mean no other release exists. After checking the manager's history and blocklist, allow more attempts with: " +
				"housekeeparr attempts reset " + b.Scope[len("attempts:"):] + " --note \"what you checked\""
		}
	}
	return c
}

// CheckBudgets adds a prerequisite for each budget claim from current
// usage keyed by scope. A missing usage fails closed.
func (p *Proposal) CheckBudgets(usage map[string]store.BudgetUsage) {
	for _, b := range p.Budgets {
		u, ok := usage[b.Scope]
		if !ok {
			p.Prerequisites = append(p.Prerequisites, Check{Name: b.Label, Hard: true, Detail: "usage could not be read"})
			continue
		}
		p.Prerequisites = append(p.Prerequisites, BudgetCheck(b, u))
	}
}
