package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Automatic evaluation results.
const (
	EvalDispatched   = "dispatched"    // an action was journaled (see its state)
	EvalDeferred     = "deferred"      // pacing: eligible again at NextAt
	EvalHeld         = "held"          // a circuit breaker holds the scope
	EvalRefused      = "refused"       // a live check before any write failed
	EvalNeedsReview  = "needs_review"  // content attempts exhausted
	EvalAtCeiling    = "at_ceiling"    // retained-data ceiling reached
	EvalNotPermitted = "not_permitted" // mode, enablement, exclusion, or stop
	EvalIneligible   = "ineligible"    // another prerequisite fails
	EvalWaiting      = "waiting"       // eligible, but this cycle's action slots were used
)

// AutomationEvaluation is the latest automatic evaluation of a finding.
type AutomationEvaluation struct {
	FindingID   int64      `json:"finding_id"`
	EvaluatedAt time.Time  `json:"evaluated_at"`
	Result      string     `json:"result"`
	Detail      string     `json:"detail"`
	NextAt      *time.Time `json:"next_at,omitempty"`
	IntentID    int64      `json:"intent_id,omitempty"`
}

// SaveEvaluation upserts a finding's latest automatic evaluation.
func (t *Tx) SaveEvaluation(e AutomationEvaluation) error {
	var intent sql.NullInt64
	if e.IntentID != 0 {
		intent = sql.NullInt64{Int64: e.IntentID, Valid: true}
	}
	_, err := t.exec(`INSERT INTO automation_evaluations(finding_id, evaluated_at, result, detail, next_at, intent_id) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(finding_id) DO UPDATE SET evaluated_at=excluded.evaluated_at, result=excluded.result, detail=excluded.detail,
		next_at=excluded.next_at, intent_id=excluded.intent_id`, e.FindingID, ms(e.EvaluatedAt), e.Result, e.Detail, ptrTime(e.NextAt), intent)
	return err
}

// Evaluation returns a finding's latest automatic evaluation, or nil.
func (s *Store) Evaluation(ctx context.Context, findingID int64) (*AutomationEvaluation, error) {
	var e AutomationEvaluation
	var at int64
	var next, intent sql.NullInt64
	err := s.r.QueryRowContext(ctx, "SELECT finding_id, evaluated_at, result, detail, next_at, intent_id FROM automation_evaluations WHERE finding_id=?",
		findingID).Scan(&e.FindingID, &at, &e.Result, &e.Detail, &next, &intent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.EvaluatedAt, e.NextAt, e.IntentID = fromMS(at), ptrMS(next), intent.Int64
	return &e, nil
}

// EvaluationCounts counts the latest evaluations by result.
func (s *Store) EvaluationCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT result, count(*) FROM automation_evaluations GROUP BY result")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var r string
		var n int
		if err := rows.Scan(&r, &n); err != nil {
			return nil, err
		}
		out[r] = n
	}
	return out, rows.Err()
}
