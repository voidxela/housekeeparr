package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// Correlation statuses for queue items.
const (
	MatchMatched           = "matched"            // exactly one transfer, consistent client mapping
	MatchHashOnly          = "matched_hash_only"  // one transfer by infohash; manager client name not mapped
	MatchAmbiguous         = "ambiguous"          // more than one candidate transfer
	MatchNotFound          = "not_found"          // mapped client listed completely; transfer absent
	MatchClientUnavailable = "client_unavailable" // mapped client has no fresh complete listing
	MatchNotConfigured     = "client_not_configured"
	MatchNotTorrent        = "not_torrent" // outside v1 scope
	MatchNoDownloadID      = "no_download_id"
	MatchUnknown           = "unknown"
)

// QueueRecord is the latest known state of a manager queue row.
type QueueRecord struct {
	ManagerID      string           `json:"manager_id"`
	ItemKey        string           `json:"item_key"`
	QueueID        int64            `json:"queue_id"`
	DownloadID     string           `json:"download_id"`
	FirstSeen      time.Time        `json:"first_seen"`
	LastSeen       time.Time        `json:"last_seen"`
	Present        bool             `json:"present"`
	GoneAt         *time.Time       `json:"gone_at,omitempty"`
	Latest         domain.QueueItem `json:"latest"`
	MatchStatus    string           `json:"match_status"`
	MatchClientID  string           `json:"match_client_id,omitempty"`
	MatchNativeID  string           `json:"match_native_id,omitempty"`
	MatchNote      string           `json:"match_note,omitempty"`
	MatchSince     *time.Time       `json:"match_since,omitempty"`
	AttentionState string           `json:"attention_state,omitempty"`
	AttentionSince *time.Time       `json:"attention_since,omitempty"`
}

// QueueItemKey derives a stable key from content identity rather than the
// transient queue row ID.
func QueueItemKey(q domain.QueueItem) string {
	content := q.EpisodeID
	if q.ContentKind == "movie" {
		content = q.MovieID
	}
	if content == 0 {
		content = q.SeriesID
	}
	switch {
	case q.DownloadID != "" && content != 0:
		return fmt.Sprintf("%s|%s:%d", q.DownloadID, q.ContentKind, content)
	case q.DownloadID != "":
		return q.DownloadID + "|" + q.Title
	default:
		return fmt.Sprintf("row:%d", q.QueueID)
	}
}

// SaveQueueSnapshot persists a successful queue read. Only a complete
// listing marks absent rows gone.
func (t *Tx) SaveQueueSnapshot(snap domain.QueueSnapshot) (gone []QueueRecord, err error) {
	at := snap.CollectedAt
	seen := map[string]bool{}
	for _, q := range snap.Items {
		key := QueueItemKey(q)
		seen[key] = true
		b, err := json.Marshal(q)
		if err != nil {
			return nil, err
		}
		_, err = t.exec(`INSERT INTO queue_items(manager_id, item_key, queue_id, download_id, first_seen, last_seen, present, latest_json)
			VALUES(?, ?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(manager_id, item_key) DO UPDATE SET queue_id=excluded.queue_id, download_id=excluded.download_id,
				last_seen=excluded.last_seen, present=1, gone_at=NULL, latest_json=excluded.latest_json`,
			snap.ManagerID, key, q.QueueID, nullStr(q.DownloadID), ms(at), ms(at), string(b))
		if err != nil {
			return nil, err
		}
	}
	if !snap.Complete {
		return nil, nil
	}
	present, err := t.QueueItems(QueueFilter{ManagerID: snap.ManagerID, PresentOnly: true})
	if err != nil {
		return nil, err
	}
	for _, r := range present {
		if seen[r.ItemKey] {
			continue
		}
		if _, err := t.exec("UPDATE queue_items SET present=0, gone_at=? WHERE manager_id=? AND item_key=?", ms(at), r.ManagerID, r.ItemKey); err != nil {
			return nil, err
		}
		gone = append(gone, r)
	}
	return gone, nil
}

// SetQueueMatch records correlation evidence for a queue row and returns
// when the current status was first observed.
func (t *Tx) SetQueueMatch(managerID, key, status, clientID, nativeID, note string, at time.Time) (time.Time, error) {
	_, err := t.exec(`UPDATE queue_items SET
			match_since = CASE WHEN match_status = ? AND match_since IS NOT NULL THEN match_since ELSE ? END,
			match_status=?, match_client_id=?, match_native_id=?, match_note=?
		WHERE manager_id=? AND item_key=?`, status, ms(at), status, nullStr(clientID), nullStr(nativeID), nullStr(note), managerID, key)
	if err != nil {
		return at, err
	}
	var since sql.NullInt64
	if err := t.queryRow("SELECT match_since FROM queue_items WHERE manager_id=? AND item_key=?", managerID, key).Scan(&since); err != nil {
		return at, err
	}
	if !since.Valid {
		return at, nil
	}
	return fromMS(since.Int64), nil
}

// SetAttention records when a row entered its current attention state.
func (t *Tx) SetAttention(managerID, key, state string, since *time.Time) error {
	_, err := t.exec("UPDATE queue_items SET attention_state=?, attention_since=? WHERE manager_id=? AND item_key=?",
		nullStr(state), nullMS(since), managerID, key)
	return err
}

// QueueFilter narrows queue listings.
type QueueFilter struct {
	ManagerID   string
	PresentOnly bool
	ClientID    string
	NativeID    string
}

const queueCols = `manager_id, item_key, queue_id, download_id, first_seen, last_seen, present, gone_at, latest_json,
	match_status, match_client_id, match_native_id, match_note, match_since, attention_state, attention_since`

func listQueue(ctx context.Context, q querier, f QueueFilter) ([]QueueRecord, error) {
	query := "SELECT " + queueCols + " FROM queue_items WHERE 1=1"
	var args []any
	if f.ManagerID != "" {
		query += " AND manager_id=?"
		args = append(args, f.ManagerID)
	}
	if f.PresentOnly {
		query += " AND present=1"
	}
	if f.ClientID != "" {
		query += " AND match_client_id=? AND match_native_id=?"
		args = append(args, f.ClientID, f.NativeID)
	}
	query += " ORDER BY present DESC, last_seen DESC, item_key"
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueRecord
	for rows.Next() {
		var r QueueRecord
		var fs, ls int64
		var qid, gone, since, msince sql.NullInt64
		var dl, mc, mn, note, astate sql.NullString
		var latest string
		if err := rows.Scan(&r.ManagerID, &r.ItemKey, &qid, &dl, &fs, &ls, &r.Present, &gone, &latest,
			&r.MatchStatus, &mc, &mn, &note, &msince, &astate, &since); err != nil {
			return nil, err
		}
		r.QueueID, r.DownloadID, r.MatchSince = qid.Int64, dl.String, ptrMS(msince)
		r.FirstSeen, r.LastSeen, r.GoneAt, r.AttentionSince = fromMS(fs), fromMS(ls), ptrMS(gone), ptrMS(since)
		r.MatchClientID, r.MatchNativeID, r.MatchNote, r.AttentionState = mc.String, mn.String, note.String, astate.String
		if err := json.Unmarshal([]byte(latest), &r.Latest); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueueItems lists queue rows for display.
func (s *Store) QueueItems(ctx context.Context, f QueueFilter) ([]QueueRecord, error) {
	return listQueue(ctx, s.r, f)
}

// QueueItems lists queue rows inside a transaction.
func (t *Tx) QueueItems(f QueueFilter) ([]QueueRecord, error) { return listQueue(t.ctx, t.tx, f) }
