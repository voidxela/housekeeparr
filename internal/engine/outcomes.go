package engine

import (
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

// outcomeBounds returns the configured outcome-tracking bounds: after
// noGrabAfter without an observed replacement grab the outcome says so;
// after outcomeWindow tracking ends.
func (e *Engine) outcomeBounds() (noGrabAfter, outcomeWindow time.Duration) {
	t := e.cfg.ActionTiming
	noGrabAfter, outcomeWindow = t.NoGrabAfter.Duration, t.OutcomeWindow.Duration
	if outcomeWindow <= 0 {
		outcomeWindow = 7 * 24 * time.Hour
	}
	if noGrabAfter <= 0 {
		noGrabAfter = 24 * time.Hour
	}
	return noGrabAfter, outcomeWindow
}

// trackOutcomes updates what has been observed after verified recoveries
// (SPEC §7 step 6): a replacement grab is a present queue row of the same
// manager for the same content with a different download ID; progress is
// an increase of that transfer's verified bytes over their value when it
// was first seen; completion is that transfer reaching the complete state.
// The replacement row must have been first seen after the recovery.
// Only managers whose queue was read completely this cycle are evaluated,
// so a failed read never moves an outcome. Progress and completion further
// require a complete listing of the replacement's client in this same cycle
// (freshClients): a fresh grab in the manager says nothing about the
// client's current progress, and cached client facts may predate the
// recovery. Absence of a grab is reported
// as not observed, never as proof that no release exists.
//
// The replacement search is followed separately, from the manager's command
// list when it was read this cycle (see workflow.ObserveSearch). The two are
// independent: a terminal download outcome keeps its search followed (or
// expired within its bounded window) until that is settled too.
func (e *Engine) trackOutcomes(tx *store.Tx, freshManagers, freshClients map[string]bool, commands map[string][]arr.Command, now time.Time) error {
	open, err := tx.OpenOutcomes()
	if err != nil {
		return err
	}
	_, outcomeWindow := e.outcomeBounds()
	windows := workflow.WindowsFrom(e.cfg.ActionTiming)
	rowsBy := map[string][]store.QueueRecord{}
	for _, o := range open {
		before := o
		before.PerContent = slices.Clone(o.PerContent)
		searchChanged := false
		if cmds, ok := commands[o.ManagerID]; ok {
			searchChanged = windows.Observe(&o, cmds, now)
		} else {
			searchChanged = windows.Expire(&o, now)
		}
		switch {
		case o.State == store.OutcomeCompleted || o.State == store.OutcomeUntracked:
			// Download outcome settled; only the search is still followed.
		case now.Sub(o.StartedAt) > outcomeWindow:
			o.State = store.OutcomeUntracked
			o.Note = "tracking ended after " + policy.HumanDuration(outcomeWindow) + " without an observed completion; check the manager's activity"
		case freshManagers[o.ManagerID]:
			rows, ok := rowsBy[o.ManagerID]
			if !ok {
				if rows, err = tx.QueueItems(store.QueueFilter{ManagerID: o.ManagerID, PresentOnly: true}); err != nil {
					return err
				}
				rowsBy[o.ManagerID] = rows
			}
			if err := e.advanceOutcome(tx, &o, rows, freshClients, now); err != nil {
				return err
			}
		}
		if searchChanged || o.State != before.State || o.NewNativeID != before.NewNativeID || o.GrabVerified != before.GrabVerified ||
			!reflect.DeepEqual(o.PerContent, before.PerContent) {
			o.CheckedAt = now
			if err := tx.SaveOutcome(o); err != nil {
				return err
			}
			if o.State != before.State {
				e.log.Info("recovery outcome", "action", o.IntentID, "manager", o.ManagerID, "content", strings.Join(o.Content, ","), "state", o.State)
			}
			if o.Search.State != before.Search.State {
				e.log.Info("replacement search", "action", o.IntentID, "manager", o.ManagerID, "content", strings.Join(o.Content, ","), "state", o.Search.State)
			}
		}
	}
	return nil
}

func (e *Engine) advanceOutcome(tx *store.Tx, o *store.RecoveryOutcome, rows []store.QueueRecord, freshClients map[string]bool, now time.Time) error {
	if len(o.PerContent) == 0 {
		c := store.ContentOutcome{State: o.State, GrabbedAt: o.GrabbedAt, NewDownloadID: o.NewDownloadID, NewClientID: o.NewClientID,
			NewNativeID: o.NewNativeID, GrabVerified: o.GrabVerified, ProgressAt: o.ProgressAt, CompletedAt: o.CompletedAt, Note: o.Note}
		if err := e.advanceContent(tx, &c, o.Content, o, rows, freshClients, now); err != nil {
			return err
		}
		o.State, o.GrabbedAt, o.NewDownloadID, o.NewClientID, o.NewNativeID = c.State, c.GrabbedAt, c.NewDownloadID, c.NewClientID, c.NewNativeID
		o.GrabVerified, o.ProgressAt, o.CompletedAt, o.Note = c.GrabVerified, c.ProgressAt, c.CompletedAt, c.Note
		return nil
	}
	// Several content items (a multi-episode release): each episode's
	// replacement is followed on its own (one grab may cover several of
	// them, or each may get its own), and the outcome summarises them.
	for i := range o.PerContent {
		c := &o.PerContent[i]
		if store.OutcomeTerminal(c.State) {
			continue
		}
		if err := e.advanceContent(tx, c, []string{c.Content}, o, rows, freshClients, now); err != nil {
			return err
		}
	}
	summarizeContent(o, now)
	return nil
}

// outcomeRank orders download outcome states by how far they got.
func outcomeRank(state string) int {
	switch state {
	case store.OutcomeGrabbed:
		return 1
	case store.OutcomeProgressing:
		return 2
	case store.OutcomeCompleted:
		return 3
	}
	return 0
}

// summarizeContent sets a multi-content outcome's state to its least
// advanced item, its replacement fields to the first item with a grab, and
// its note to every item's state.
func summarizeContent(o *store.RecoveryOutcome, now time.Time) {
	least, waiting := 3, store.OutcomeAwaitingGrab
	var first *store.ContentOutcome
	var parts []string
	for i := range o.PerContent {
		c := &o.PerContent[i]
		if r := outcomeRank(c.State); r < least {
			least = r
		}
		if c.State == store.OutcomeNoGrabYet {
			waiting = store.OutcomeNoGrabYet
		}
		if first == nil && c.GrabbedAt != nil {
			first = c
		}
		part := c.Content + ": " + strings.ReplaceAll(c.State, "_", " ")
		if c.Note != "" && c.State != store.OutcomeAwaitingGrab {
			part += " (" + c.Note + ")"
		}
		parts = append(parts, part)
	}
	switch least {
	case 0:
		o.State = waiting
	case 1:
		o.State = store.OutcomeGrabbed
	case 2:
		o.State = store.OutcomeProgressing
	case 3:
		if o.State != store.OutcomeCompleted {
			at := now
			o.CompletedAt = &at
		}
		o.State = store.OutcomeCompleted
	}
	if first != nil {
		o.GrabbedAt, o.NewDownloadID, o.NewClientID, o.NewNativeID, o.GrabVerified = first.GrabbedAt, first.NewDownloadID, first.NewClientID, first.NewNativeID, first.GrabVerified
	}
	o.Note = "per episode: " + strings.Join(parts, "; ")
}

// advanceContent follows one replacement: a grab of any of keys, then that
// download's progress and completion.
func (e *Engine) advanceContent(tx *store.Tx, c *store.ContentOutcome, keys []string, o *store.RecoveryOutcome, rows []store.QueueRecord,
	freshClients map[string]bool, now time.Time) error {
	if c.GrabbedAt == nil {
		for _, r := range rows {
			key := r.Latest.ContentRef()
			// A row for the same content that already existed before the
			// recovery (another grab in flight) is not its replacement.
			if key == "" || !containsStr(keys, key) || r.DownloadID == "" || strings.EqualFold(r.DownloadID, o.OldDownloadID) ||
				r.FirstSeen.Before(o.StartedAt) {
				continue
			}
			at := now
			c.GrabbedAt, c.NewDownloadID, c.State = &at, r.DownloadID, store.OutcomeGrabbed
			c.Note = "replacement grabbed: " + r.Latest.Title
			break
		}
		if c.GrabbedAt == nil {
			if noGrabAfter, _ := e.outcomeBounds(); c.State == store.OutcomeAwaitingGrab && now.Sub(o.StartedAt) > noGrabAfter {
				c.State = store.OutcomeNoGrabYet
				c.Note = "no replacement grab observed in " + policy.HumanDuration(noGrabAfter) +
					"; this is not evidence that no release exists (the search may have found none acceptable, or indexers may have failed): check the manager's history"
			}
			return nil
		}
	}
	if c.NewNativeID == "" {
		for _, r := range rows {
			if strings.EqualFold(r.DownloadID, c.NewDownloadID) && r.MatchStatus == store.MatchMatched {
				c.NewClientID, c.NewNativeID = r.MatchClientID, r.MatchNativeID
			}
		}
		if c.NewNativeID == "" {
			return nil
		}
	}
	if !freshClients[c.NewClientID] {
		// No complete client listing this cycle: keep the last known state
		// (its CheckedAt shows its age) rather than infer progress from
		// cached facts.
		return nil
	}
	// A complete listing this cycle marks absent transfers gone, so a present
	// record is current (unchanged complete rows keep an older last_seen).
	tr, err := tx.Transfer(c.NewClientID, c.NewNativeID)
	if err != nil || tr == nil || !tr.Present {
		return err
	}
	v, known := tr.Latest.VerifiedBytes.Get()
	if known && c.GrabVerified == nil {
		c.GrabVerified = &v
	}
	switch {
	case tr.StateClass == domain.StateComplete:
		at := now
		c.CompletedAt, c.State = &at, store.OutcomeCompleted
		c.Note = "the replacement download completed in the client (import is the manager's)"
	case known && c.GrabVerified != nil && v > *c.GrabVerified && c.State != store.OutcomeProgressing:
		at := now
		c.ProgressAt, c.State = &at, store.OutcomeProgressing
		c.Note = "the replacement is making verified progress"
	}
	return nil
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
