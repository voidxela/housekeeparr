package engine

import (
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

// Outcome tracking bounds (implementation proposals): after noGrabAfter
// without an observed replacement grab the outcome says so; after
// outcomeWindow tracking ends.
const (
	noGrabAfter   = 24 * time.Hour
	outcomeWindow = 7 * 24 * time.Hour
)

// trackOutcomes updates what has been observed after verified recoveries
// (SPEC §7 step 6): a replacement grab is a present queue row of the same
// manager for the same content with a different download ID; progress is
// an increase of that transfer's verified bytes over their value when it
// was first seen; completion is that transfer reaching the complete state.
// The replacement row must have been first seen after the recovery.
// Only managers whose queue was read completely this cycle are evaluated,
// so a failed read never moves an outcome. Absence of a grab is reported
// as not observed, never as proof that no release exists.
//
// The replacement search is followed separately, from the manager's command
// list when it was read this cycle (see workflow.ObserveSearch).
func (e *Engine) trackOutcomes(tx *store.Tx, freshManagers map[string]bool, commands map[string][]arr.Command, now time.Time) error {
	open, err := tx.OpenOutcomes()
	if err != nil {
		return err
	}
	rowsBy := map[string][]store.QueueRecord{}
	for _, o := range open {
		before := o
		searchChanged := false
		if cmds, ok := commands[o.ManagerID]; ok {
			searchChanged = workflow.ObserveSearch(&o, cmds, now)
		}
		switch {
		case now.Sub(o.StartedAt) > outcomeWindow:
			o.State = store.OutcomeUntracked
			o.Note = "tracking ended after " + outcomeWindow.String() + " without an observed completion; check the manager's activity"
		case freshManagers[o.ManagerID]:
			rows, ok := rowsBy[o.ManagerID]
			if !ok {
				if rows, err = tx.QueueItems(store.QueueFilter{ManagerID: o.ManagerID, PresentOnly: true}); err != nil {
					return err
				}
				rowsBy[o.ManagerID] = rows
			}
			if err := e.advanceOutcome(tx, &o, rows, now); err != nil {
				return err
			}
		}
		if searchChanged || o.State != before.State || o.NewNativeID != before.NewNativeID || o.GrabVerified != before.GrabVerified {
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

func (e *Engine) advanceOutcome(tx *store.Tx, o *store.RecoveryOutcome, rows []store.QueueRecord, now time.Time) error {
	if o.GrabbedAt == nil {
		for _, r := range rows {
			key := r.Latest.ContentRef()
			// A row for the same content that already existed before the
			// recovery (another grab in flight) is not its replacement.
			if key == "" || !containsStr(o.Content, key) || r.DownloadID == "" || strings.EqualFold(r.DownloadID, o.OldDownloadID) ||
				r.FirstSeen.Before(o.StartedAt) {
				continue
			}
			at := now
			o.GrabbedAt, o.NewDownloadID, o.State = &at, r.DownloadID, store.OutcomeGrabbed
			o.Note = "replacement grabbed: " + r.Latest.Title
			break
		}
		if o.GrabbedAt == nil {
			if o.State == store.OutcomeAwaitingGrab && now.Sub(o.StartedAt) > noGrabAfter {
				o.State = store.OutcomeNoGrabYet
				o.Note = "no replacement grab observed in " + noGrabAfter.String() +
					"; this is not evidence that no release exists (the search may have found none acceptable, or indexers may have failed): check the manager's history"
			}
			return nil
		}
	}
	if o.NewNativeID == "" {
		for _, r := range rows {
			if strings.EqualFold(r.DownloadID, o.NewDownloadID) && r.MatchStatus == store.MatchMatched {
				o.NewClientID, o.NewNativeID = r.MatchClientID, r.MatchNativeID
			}
		}
		if o.NewNativeID == "" {
			return nil
		}
	}
	tr, err := tx.Transfer(o.NewClientID, o.NewNativeID)
	if err != nil || tr == nil || !tr.Present {
		return err
	}
	v, known := tr.Latest.VerifiedBytes.Get()
	if known && o.GrabVerified == nil {
		o.GrabVerified = &v
	}
	switch {
	case tr.StateClass == domain.StateComplete:
		at := now
		o.CompletedAt, o.State = &at, store.OutcomeCompleted
		o.Note = "the replacement download completed in the client (import is the manager's)"
	case known && o.GrabVerified != nil && v > *o.GrabVerified && o.State != store.OutcomeProgressing:
		at := now
		o.ProgressAt, o.State = &at, store.OutcomeProgressing
		o.Note = "the replacement is making verified progress"
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
