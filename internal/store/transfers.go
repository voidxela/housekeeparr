package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
)

// TransferRecord is the latest known state of one transfer.
type TransferRecord struct {
	ClientID   string             `json:"client_id"`
	NativeID   string             `json:"native_id"`
	Name       string             `json:"name"`
	StateClass domain.StateClass  `json:"state_class"`
	FirstSeen  time.Time          `json:"first_seen"`
	LastSeen   time.Time          `json:"last_seen"`
	Present    bool               `json:"present"`
	GoneAt     *time.Time         `json:"gone_at,omitempty"`
	Latest     domain.Transfer    `json:"latest"`
	Assessment *policy.Assessment `json:"assessment,omitempty"`
	AssessedAt *time.Time         `json:"assessed_at,omitempty"`
}

// Observation is one stored sample; it keeps only decision-relevant and
// display-relevant signals to bound storage.
type Observation struct {
	At                time.Time              `json:"at"`
	RunID             string                 `json:"run_id"`
	MonoMS            int64                  `json:"mono_ms"`
	StateClass        domain.StateClass      `json:"state_class"`
	NativeState       string                 `json:"native_state"`
	WantedBytes       domain.Signal[int64]   `json:"wanted"`
	VerifiedBytes     domain.Signal[int64]   `json:"verified"`
	NetworkDownloaded domain.Signal[int64]   `json:"network"`
	DownloadRate      domain.Signal[int64]   `json:"rate"`
	ConnectedSeeds    domain.Signal[int64]   `json:"seeds"`
	ConnectedPeers    domain.Signal[int64]   `json:"peers"`
	Availability      domain.Signal[float64] `json:"avail"`
	AllFilesWanted    domain.Signal[bool]    `json:"all_wanted"`
	AvailWantedOnly   bool                   `json:"avail_wanted_only,omitempty"`
}

// Sample converts an observation for the policy evaluator.
func (o Observation) Sample() policy.Sample {
	return policy.Sample{
		At: o.At, RunID: o.RunID, MonoMS: o.MonoMS, StateClass: o.StateClass, NativeState: o.NativeState,
		WantedBytes: o.WantedBytes, VerifiedBytes: o.VerifiedBytes, Availability: o.Availability, AllFilesWanted: o.AllFilesWanted,
		AvailabilityWantedOnly: o.AvailWantedOnly,
	}
}

func observationOf(t domain.Transfer, at time.Time, runID string, mono int64) Observation {
	return Observation{
		At: at, RunID: runID, MonoMS: mono, StateClass: t.StateClass, NativeState: t.NativeState,
		WantedBytes: t.WantedBytes, VerifiedBytes: t.VerifiedBytes, NetworkDownloaded: t.NetworkDownloadedBytes,
		DownloadRate: t.DownloadRate, ConnectedSeeds: t.ConnectedSeeds, ConnectedPeers: t.ConnectedPeers,
		Availability: t.Availability, AllFilesWanted: t.AllFilesWanted, AvailWantedOnly: t.AvailabilityWantedOnly,
	}
}

// GoneTransfer identifies a transfer that disappeared from a complete listing.
type GoneTransfer struct {
	ClientID, NativeID, Name string
}

// SaveTransferSnapshot persists a successful listing. Observations are
// stored for transfers that are incomplete (or just became complete);
// complete transfers only update their latest state, bounding growth for
// large seeding libraries. Only a complete listing may mark transfers gone.
func (t *Tx) SaveTransferSnapshot(snap domain.TransferSnapshot, runID string, mono int64, maxPerTransfer int) ([]GoneTransfer, error) {
	at := snap.CollectedAt
	seen := make(map[string]bool, len(snap.Transfers))
	type prevRow struct {
		class, json string
		present     bool
	}
	prev := make(map[string]prevRow, len(snap.Transfers))
	rows0, err := t.query("SELECT native_id, state_class, latest_json, present FROM transfers WHERE client_id=?", snap.ClientID)
	if err != nil {
		return nil, err
	}
	for rows0.Next() {
		var id string
		var r prevRow
		if err := rows0.Scan(&id, &r.class, &r.json, &r.present); err != nil {
			rows0.Close()
			return nil, err
		}
		prev[id] = r
	}
	rows0.Close()
	if err := rows0.Err(); err != nil {
		return nil, err
	}
	for _, tr := range snap.Transfers {
		seen[tr.NativeID] = true
		key := snap.ClientID + "|" + tr.NativeID
		var fp uint64
		if tr.StateClass == domain.StateComplete {
			fp = fingerprint(tr)
			if t.confirmed[key] == fp {
				continue // stored row already matches; nothing to write or serialize
			}
		} else {
			t.pending[key] = 0
		}
		latest, err := json.Marshal(storedForm(tr))
		if err != nil {
			return nil, err
		}
		p, existed := prev[tr.NativeID]
		prevClass := sql.NullString{String: p.class, Valid: existed}
		// Complete transfers that are unchanged and already present are not
		// rewritten: large seeding libraries would otherwise rewrite every
		// row each cycle. Their freshness is the connection's last success.
		unchanged := existed && p.json == string(latest) && p.present
		if tr.StateClass == domain.StateComplete {
			t.pending[key] = fp
			if unchanged {
				continue
			}
		}
		_, err = t.exec(`INSERT INTO transfers(client_id, native_id, hash_v1, hash_v2, name, state_class, first_seen, last_seen, present, latest_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(client_id, native_id) DO UPDATE SET hash_v1=excluded.hash_v1, hash_v2=excluded.hash_v2, name=excluded.name,
				state_class=excluded.state_class, last_seen=excluded.last_seen, present=1, gone_at=NULL, latest_json=excluded.latest_json`,
			snap.ClientID, tr.NativeID, nullStr(tr.Hashes.V1), nullStr(tr.Hashes.V2), tr.Name, string(tr.StateClass), ms(at), ms(at), string(latest))
		if err != nil {
			return nil, err
		}
		record := tr.StateClass != domain.StateComplete || (prevClass.Valid && prevClass.String != string(domain.StateComplete))
		if !record {
			continue
		}
		ob, err := json.Marshal(observationOf(tr, at, runID, mono))
		if err != nil {
			return nil, err
		}
		if _, err := t.exec(`INSERT INTO transfer_observations(client_id, native_id, at, run_id, mono_ms, state_class, sample_json)
			VALUES(?, ?, ?, ?, ?, ?, ?)`, snap.ClientID, tr.NativeID, ms(at), runID, mono, string(tr.StateClass), string(ob)); err != nil {
			return nil, err
		}
		// Bound per-transfer history using the (client, transfer, at) index.
		if _, err := t.exec(`DELETE FROM transfer_observations WHERE client_id=? AND native_id=? AND at <= (
				SELECT at FROM transfer_observations WHERE client_id=? AND native_id=? ORDER BY at DESC LIMIT 1 OFFSET ?)`,
			snap.ClientID, tr.NativeID, snap.ClientID, tr.NativeID, maxPerTransfer); err != nil {
			return nil, err
		}
	}
	if !snap.Complete {
		return nil, nil
	}
	rows, err := t.query("SELECT native_id, name FROM transfers WHERE client_id=? AND present=1", snap.ClientID)
	if err != nil {
		return nil, err
	}
	var gone []GoneTransfer
	for rows.Next() {
		var g GoneTransfer
		if err := rows.Scan(&g.NativeID, &g.Name); err != nil {
			rows.Close()
			return nil, err
		}
		if !seen[g.NativeID] {
			g.ClientID = snap.ClientID
			gone = append(gone, g)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, g := range gone {
		if _, err := t.exec("UPDATE transfers SET present=0, gone_at=? WHERE client_id=? AND native_id=?", ms(at), g.ClientID, g.NativeID); err != nil {
			return nil, err
		}
		t.pending[g.ClientID+"|"+g.NativeID] = 0
	}
	return gone, nil
}

// Observations returns stored samples for a transfer since a time, oldest first.
func observations(ctx context.Context, q querier, clientID, nativeID string, since time.Time, limit int) ([]Observation, error) {
	rows, err := q.QueryContext(ctx, `SELECT sample_json FROM (
			SELECT id, at, sample_json FROM transfer_observations WHERE client_id=? AND native_id=? AND at>=?
			ORDER BY at DESC, id DESC LIMIT ?) ORDER BY at, id`, clientID, nativeID, ms(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Observation
	for rows.Next() {
		var js string
		if err := rows.Scan(&js); err != nil {
			return nil, err
		}
		var o Observation
		if err := json.Unmarshal([]byte(js), &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Observations returns samples within a transaction.
func (t *Tx) Observations(clientID, nativeID string, since time.Time, limit int) ([]Observation, error) {
	return observations(t.ctx, t.tx, clientID, nativeID, since, limit)
}

// Observations returns samples for display.
func (s *Store) Observations(ctx context.Context, clientID, nativeID string, since time.Time, limit int) ([]Observation, error) {
	return observations(ctx, s.r, clientID, nativeID, since, limit)
}

// SetAssessment stores the latest policy assessment for a transfer.
func (t *Tx) SetAssessment(clientID, nativeID string, a policy.Assessment, at time.Time) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = t.exec("UPDATE transfers SET assessment_json=?, assessed_at=? WHERE client_id=? AND native_id=?", string(b), ms(at), clientID, nativeID)
	return err
}

const transferCols = `client_id, native_id, name, state_class, first_seen, last_seen, present, gone_at, latest_json, assessment_json, assessed_at`

func scanTransfer(sc interface{ Scan(...any) error }) (TransferRecord, error) {
	var r TransferRecord
	var fs, ls int64
	var gone, assessedAt sql.NullInt64
	var latest string
	var assess sql.NullString
	var class string
	if err := sc.Scan(&r.ClientID, &r.NativeID, &r.Name, &class, &fs, &ls, &r.Present, &gone, &latest, &assess, &assessedAt); err != nil {
		return r, err
	}
	r.StateClass = domain.StateClass(class)
	r.FirstSeen, r.LastSeen, r.GoneAt, r.AssessedAt = fromMS(fs), fromMS(ls), ptrMS(gone), ptrMS(assessedAt)
	if err := json.Unmarshal([]byte(latest), &r.Latest); err != nil {
		return r, err
	}
	if assess.Valid {
		var a policy.Assessment
		if err := json.Unmarshal([]byte(assess.String), &a); err == nil {
			r.Assessment = &a
		}
	}
	return r, nil
}

// TransferFilter narrows transfer listings.
type TransferFilter struct {
	ClientID    string
	PresentOnly bool
	IncludeDone bool // include complete transfers
	Limit       int
}

func listTransfers(ctx context.Context, q querier, f TransferFilter) ([]TransferRecord, error) {
	query := "SELECT " + transferCols + " FROM transfers WHERE 1=1"
	var args []any
	if f.ClientID != "" {
		query += " AND client_id=?"
		args = append(args, f.ClientID)
	}
	if f.PresentOnly {
		query += " AND present=1"
	}
	if !f.IncludeDone {
		query += " AND state_class != 'complete'"
	}
	query += " ORDER BY present DESC, last_seen DESC, name"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TransferRecord
	for rows.Next() {
		r, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Transfers lists transfers for display.
func (s *Store) Transfers(ctx context.Context, f TransferFilter) ([]TransferRecord, error) {
	return listTransfers(ctx, s.r, f)
}

// Transfers lists transfers inside a transaction.
func (t *Tx) Transfers(f TransferFilter) ([]TransferRecord, error) {
	return listTransfers(t.ctx, t.tx, f)
}

// Transfer returns one transfer or nil.
func (s *Store) Transfer(ctx context.Context, clientID, nativeID string) (*TransferRecord, error) {
	r, err := scanTransfer(s.r.QueryRowContext(ctx, "SELECT "+transferCols+" FROM transfers WHERE client_id=? AND native_id=?", clientID, nativeID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// TransferRef is a lightweight identity used for correlation.
type TransferRef struct {
	ClientID, NativeID, Name string
	Hashes                   domain.Hashes
	Present                  bool
	LastSeen                 time.Time
}

// TransfersByHash finds transfers whose identity matches a download ID.
// v1 and full v2 hashes match exactly; a 40-character ID may also be the
// truncated v2 form some clients report.
func (t *Tx) TransfersByHash(id string) ([]TransferRef, error) {
	id = domain.NormalizeHash(id)
	if id == "" {
		return nil, nil
	}
	rows, err := t.query(`SELECT client_id, native_id, name, hash_v1, hash_v2, present, last_seen FROM transfers
		WHERE native_id=? OR hash_v1=? OR hash_v2=? OR (length(?)=40 AND substr(hash_v2, 1, 40)=?)`, id, id, id, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TransferRef
	for rows.Next() {
		var r TransferRef
		var v1, v2 sql.NullString
		var ls int64
		if err := rows.Scan(&r.ClientID, &r.NativeID, &r.Name, &v1, &v2, &r.Present, &ls); err != nil {
			return nil, err
		}
		r.Hashes = domain.Hashes{V1: v1.String, V2: v2.String}
		r.LastSeen = fromMS(ls)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClearCompleteAssessments removes stale assessments from transfers that
// have completed; no progress diagnosis applies to them.
func (t *Tx) ClearCompleteAssessments(clientID string) error {
	_, err := t.exec("UPDATE transfers SET assessment_json=NULL, assessed_at=NULL WHERE client_id=? AND state_class='complete' AND assessment_json IS NOT NULL", clientID)
	return err
}

const notRetained = "not retained for complete transfers (bounds state writes)"

// storedForm drops volatile peer and rate signals from complete transfers,
// which no diagnosis uses, so unchanged seeding torrents are not rewritten.
func storedForm(t domain.Transfer) domain.Transfer {
	if t.StateClass != domain.StateComplete {
		return t
	}
	t.DownloadRate = domain.UnknownValue[int64](notRetained)
	t.ConnectedSeeds = domain.UnknownValue[int64](notRetained)
	t.ConnectedPeers = domain.UnknownValue[int64](notRetained)
	t.SwarmSeeds = domain.UnknownValue[int64](notRetained)
	t.SwarmPeers = domain.UnknownValue[int64](notRetained)
	t.Availability = domain.UnknownValue[float64](notRetained)
	t.NetworkDownloadedBytes = domain.UnknownValue[int64](notRetained)
	return t
}

// TransferIndex maps every identity variant of present transfers.
type TransferIndex map[string][]TransferRef

// PresentTransferIndex loads all present transfers once and indexes them by
// native ID, v1 hash, full v2 hash, and truncated (40 hex) v2 hash, so
// correlation needs no per-row queries.
func (t *Tx) PresentTransferIndex() (TransferIndex, error) {
	rows, err := t.query("SELECT client_id, native_id, name, hash_v1, hash_v2, last_seen FROM transfers WHERE present=1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := TransferIndex{}
	for rows.Next() {
		var r TransferRef
		var v1, v2 sql.NullString
		var ls int64
		if err := rows.Scan(&r.ClientID, &r.NativeID, &r.Name, &v1, &v2, &ls); err != nil {
			return nil, err
		}
		r.Hashes, r.Present, r.LastSeen = domain.Hashes{V1: v1.String, V2: v2.String}, true, fromMS(ls)
		keys := map[string]bool{r.NativeID: true}
		if r.Hashes.V1 != "" {
			keys[r.Hashes.V1] = true
		}
		if r.Hashes.V2 != "" {
			keys[r.Hashes.V2] = true
			if len(r.Hashes.V2) == 64 {
				keys[r.Hashes.V2[:40]] = true
			}
		}
		for k := range keys {
			idx[k] = append(idx[k], r)
		}
	}
	return idx, rows.Err()
}

// Lookup returns present transfers matching a download ID.
func (i TransferIndex) Lookup(id string) []TransferRef { return i[domain.NormalizeHash(id)] }

// fingerprint hashes the stored (non-volatile) form of a complete transfer.
func fingerprint(t domain.Transfer) uint64 {
	h := fnv.New64a()
	w := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	i := func(s domain.Signal[int64]) string {
		return string(s.Status) + ":" + strconv.FormatInt(s.Value, 10) + ":" + s.Note
	}
	b := func(s domain.Signal[bool]) string {
		return string(s.Status) + ":" + strconv.FormatBool(s.Value) + ":" + s.Note
	}
	str := func(s domain.Signal[string]) string { return string(s.Status) + ":" + s.Value + ":" + s.Note }
	w(t.NativeID, t.Hashes.V1, t.Hashes.V2, t.Name, t.NativeState, string(t.StateClass), str(t.Category),
		string(t.AddedAt.Status), strconv.FormatInt(t.AddedAt.Value.UnixNano(), 10), t.AddedAt.Note,
		i(t.WantedBytes), i(t.VerifiedBytes), i(t.RemainingBytes), i(t.TotalBytes),
		b(t.AllFilesWanted), b(t.Private), str(t.ClientMessage), strconv.FormatBool(t.AvailabilityWantedOnly))
	v := h.Sum64()
	if v == 0 {
		v = 1 // 0 is reserved for "not confirmed"
	}
	return v
}
