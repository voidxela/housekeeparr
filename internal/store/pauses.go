package store

import (
	"context"
	"time"
)

// WorkflowPause records that an operator paused a workflow.
type WorkflowPause struct {
	Workflow string    `json:"workflow"`
	PausedAt time.Time `json:"paused_at"`
	Actor    string    `json:"actor"`
	Note     string    `json:"note"`
}

// PauseWorkflow pauses a workflow (replacing an earlier pause record).
func (t *Tx) PauseWorkflow(p WorkflowPause) error {
	_, err := t.exec(`INSERT INTO workflow_pauses(workflow, paused_at, actor, note) VALUES(?, ?, ?, ?)
		ON CONFLICT(workflow) DO UPDATE SET paused_at=excluded.paused_at, actor=excluded.actor, note=excluded.note`,
		p.Workflow, ms(p.PausedAt), p.Actor, p.Note)
	return err
}

// ResumeWorkflow removes a workflow's pause; it reports whether one existed.
func (t *Tx) ResumeWorkflow(workflow string) (bool, error) {
	res, err := t.exec(`DELETE FROM workflow_pauses WHERE workflow=?`, workflow)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// WorkflowPauses returns the current pauses by workflow.
func (s *Store) WorkflowPauses(ctx context.Context) (map[string]WorkflowPause, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT workflow, paused_at, actor, note FROM workflow_pauses`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]WorkflowPause{}
	for rows.Next() {
		var p WorkflowPause
		var at int64
		if err := rows.Scan(&p.Workflow, &at, &p.Actor, &p.Note); err != nil {
			return nil, err
		}
		p.PausedAt = fromMS(at)
		out[p.Workflow] = p
	}
	return out, rows.Err()
}
