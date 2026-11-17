package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// AttributionCandidate binds the complete stored relation, including nullable
// resolution identity, without retaining its values in the operation record.
type AttributionCandidate struct {
	ID          int64  `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

// AttributionManifest is derived from one verified snapshot and cutoff.
type AttributionManifest struct {
	Version        int                    `json:"version"`
	Cutoff         string                 `json:"cutoff"`
	BackupChecksum string                 `json:"backup_checksum"`
	Candidates     []AttributionCandidate `json:"candidates"`
	GraphDigest    string                 `json:"graph_digest"`
	ReceiptDigest  string                 `json:"receipt_digest"`
}

type AttributionRequest struct {
	BackupPath     string              `json:"backup_path"`
	Manifest       AttributionManifest `json:"manifest"`
	IdempotencyKey string              `json:"idempotency_key"`
}

type AttributionUndoRequest struct {
	AttributionRequest
	ClearKey string `json:"clear_key"`
}

type AttributionResult struct {
	Treated            []int64 `json:"treated"`
	Restored           []int64 `json:"restored"`
	Gone               []int64 `json:"gone"`
	FingerprintChanged []int64 `json:"fingerprint_changed"`
	GraphDigest        string  `json:"graph_digest"`
	ReceiptDigest      string  `json:"receipt_digest"`
	GraphPreserved     bool    `json:"graph_preserved"`
	ReceiptsPreserved  bool    `json:"receipts_preserved"`
}

type attributionRow struct {
	ID         int64     `json:"id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Kind       string    `json:"kind"`
	Created    string    `json:"created"`
	Resolution *string   `json:"resolution"`
	Reason     string    `json:"reason"`
	Version    int64     `json:"version"`
	Types      [8]string `json:"types"`
}

const attributionColumns = `id,work_id_from,work_id_to,kind,created_at,resolution_id,reason,record_version,
 typeof(id),typeof(work_id_from),typeof(work_id_to),typeof(kind),typeof(created_at),typeof(resolution_id),typeof(reason),typeof(record_version)`

func attributionFingerprint(r attributionRow) string {
	var resolution *[]byte
	if r.Resolution != nil {
		value := []byte(*r.Resolution)
		resolution = &value
	}
	return attributionHash(struct {
		ID                      int64
		From, To, Kind, Created []byte
		Resolution              *[]byte
		Reason                  []byte
		Version                 int64
		Types                   [8]string
	}{r.ID, []byte(r.From), []byte(r.To), []byte(r.Kind), []byte(r.Created), resolution, []byte(r.Reason), r.Version, r.Types})
}

func attributionHash(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// verifiedAttributionBackup creates an immutable private copy whose bytes match
// the verified checksum. SQLite never reads the mutable source path afterward.
// The copy is staged beside the backup, as Backup stages its own copy, so a
// crash can leave only a hidden duplicate in the directory that already holds
// the same bytes, never a store copy in a shared temporary directory.
func verifiedAttributionBackup(ctx context.Context, path string) (*sql.DB, string, func(), error) {
	m, err := VerifyBackup(ctx, path)
	if err != nil {
		return nil, "", nil, err
	}
	source, err := os.Open(path) //nolint:gosec // VerifyBackup admitted this absolute clean snapshot path.
	if err != nil {
		return nil, "", nil, err
	}
	defer func() { _ = source.Close() }()
	f, err := os.CreateTemp(filepath.Dir(path), ".concord-attribution-*")
	if err != nil {
		return nil, "", nil, err
	}
	name := f.Name()
	cleanup := func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(name + suffix)
		}
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), source)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.DBChecksum {
		cleanup()
		return nil, "", nil, fmt.Errorf("backup checksum changed during copy")
	}
	copyManifest, err := verifyBackupFile(ctx, name, CurrentSchemaVersion(), nil)
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	if copyManifest.DBChecksum != m.DBChecksum || copyManifest.SchemaVersion != m.SchemaVersion || copyManifest.currentStateDigest != m.currentStateDigest {
		cleanup()
		return nil, "", nil, attributionConflict("verified backup differs from its immutable copy")
	}
	db, err := sqlitedb.Open(SQLiteFileURI(name) + "?mode=ro&immutable=1")
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	return db, m.DBChecksum, func() { _ = db.Close(); cleanup() }, nil
}

func scanAttributionRows(rows *sql.Rows) ([]attributionRow, error) {
	defer rows.Close()
	out := []attributionRow{}
	for rows.Next() {
		var r attributionRow
		var resolution sql.NullString
		if err := rows.Scan(&r.ID, &r.From, &r.To, &r.Kind, &r.Created, &resolution, &r.Reason, &r.Version, &r.Types[0], &r.Types[1], &r.Types[2], &r.Types[3], &r.Types[4], &r.Types[5], &r.Types[6], &r.Types[7]); err != nil {
			return nil, err
		}
		if resolution.Valid {
			r.Resolution = &resolution.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func attributionPopulation(ctx context.Context, q queryer, cutoff string) ([]attributionRow, error) {
	when, err := time.Parse(time.RFC3339Nano, cutoff)
	if err != nil {
		return nil, fmt.Errorf("invalid cutoff: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT r.id,r.work_id_from,r.work_id_to,r.kind,r.created_at,r.resolution_id,r.reason,r.record_version,typeof(r.id),typeof(r.work_id_from),typeof(r.work_id_to),typeof(r.kind),typeof(r.created_at),typeof(r.resolution_id),typeof(r.reason),typeof(r.record_version) FROM relations r JOIN effect_receipts f ON f.relation_seq=r.id WHERE `+currentQ7RelationPredicate+` ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	all, err := scanAttributionRows(rows)
	if err != nil {
		return nil, err
	}
	out := []attributionRow{}
	for _, r := range all {
		created, err := time.Parse(time.RFC3339Nano, r.Created)
		if err != nil {
			return nil, fmt.Errorf("invalid relation creation time for %d: %w", r.ID, err)
		}
		if created.Before(when) {
			out = append(out, r)
		}
	}
	return out, nil
}

func attributionDigests(ctx context.Context, q queryer) (string, string, error) {
	graph, err := q.QueryContext(ctx, `SELECT id,work_id_from,work_id_to,kind FROM relations ORDER BY id`)
	if err != nil {
		return "", "", err
	}
	g := [][]any{}
	for graph.Next() {
		var id int64
		var from, to, kind string
		if err := graph.Scan(&id, &from, &to, &kind); err != nil {
			graph.Close()
			return "", "", err
		}
		g = append(g, []any{id, []byte(from), []byte(to), []byte(kind)})
	}
	err = graph.Err()
	graph.Close()
	if err != nil {
		return "", "", err
	}
	receipts, err := q.QueryContext(ctx, `SELECT event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest,relation_seq,delivery_assertion,typeof(event_id),typeof(effect_seq),typeof(kind),typeof(subject_type),typeof(subject_id),typeof(actor),typeof(payload_version),typeof(payload_digest),typeof(relation_seq),typeof(delivery_assertion) FROM effect_receipts ORDER BY event_id COLLATE BINARY`)
	if err != nil {
		return "", "", err
	}
	r := [][]any{}
	for receipts.Next() {
		v := make([]any, 20)
		p := make([]any, 20)
		for i := range v {
			p[i] = &v[i]
		}
		if err := receipts.Scan(p...); err != nil {
			receipts.Close()
			return "", "", err
		}
		// JSON strings replace invalid UTF-8. Preserve the bytes of TEXT as
		// well as BLOB values, with SQLite storage types kept separately.
		for i := 0; i < 10; i++ {
			switch value := v[i].(type) {
			case string:
				v[i] = []byte(value)
			case float64:
				v[i] = fmt.Sprintf("%016x", math.Float64bits(value))
			}
		}
		r = append(r, v)
	}
	err = receipts.Err()
	receipts.Close()
	if err != nil {
		return "", "", err
	}
	return attributionHash(g), attributionHash(r), nil
}

func attributionManifest(ctx context.Context, q queryer, checksum, cutoff string) (AttributionManifest, []attributionRow, error) {
	rows, err := attributionPopulation(ctx, q, cutoff)
	if err != nil {
		return AttributionManifest{}, nil, err
	}
	g, r, err := attributionDigests(ctx, q)
	if err != nil {
		return AttributionManifest{}, nil, err
	}
	m := AttributionManifest{Version: 1, Cutoff: cutoff, BackupChecksum: checksum, Candidates: []AttributionCandidate{}, GraphDigest: g, ReceiptDigest: r}
	for _, row := range rows {
		m.Candidates = append(m.Candidates, AttributionCandidate{row.ID, attributionFingerprint(row)})
	}
	return m, rows, nil
}

// InspectRelationAttribution derives the complete Q7-displayed population from
// a checksum-verified backup, never from the live store.
func InspectRelationAttribution(ctx context.Context, backupPath, cutoff string) (AttributionManifest, error) {
	db, checksum, closeBackup, err := verifiedAttributionBackup(ctx, backupPath)
	if err != nil {
		return AttributionManifest{}, err
	}
	defer closeBackup()
	m, _, err := attributionManifest(ctx, db, checksum, cutoff)
	return m, err
}

type attributionStoredResult struct {
	ManifestDigest string            `json:"manifest_digest"`
	Result         AttributionResult `json:"result"`
}

func attributionKey(kind, key string) MutationIdempotencyKey {
	return MutationIdempotencyKey{PrincipalRef: "operator", Tool: "relation_attribution", OperationKind: kind, IdempotencyKey: key}
}

// RelationAttributionRecorded reports whether action already holds a durable
// result under key, so the operator command can skip preflight a replay does not need.
func (s *Store) RelationAttributionRecorded(ctx context.Context, action, key string) (bool, error) {
	_, found, err := s.LookupMutationIdempotency(ctx, attributionKey(action, key))
	return found, err
}

func refuseAttributionKeyReuse(ctx context.Context, tx *Transaction, kind, key string) error {
	other := "clear"
	if kind == "clear" {
		other = "undo"
	}
	_, found, err := LookupMutationIdempotencyTx(ctx, tx, attributionKey(other, key))
	if err != nil {
		return err
	}
	if found {
		return attributionConflict("idempotency key belongs to another attribution operation")
	}
	return nil
}

func attributionConflict(message string) error {
	return newFailure(KindOperationConflict, "relation_attribution", message, false, "inspect a fresh verified backup and reconcile the operation")
}

func attributionReplay(ctx context.Context, tx *Transaction, key MutationIdempotencyKey, digest string) (AttributionResult, bool, error) {
	record, found, err := LookupMutationIdempotencyTx(ctx, tx, key)
	if err != nil || !found {
		return AttributionResult{}, found, err
	}
	out, err := decodeAttributionReplay(record, digest)
	return out, true, err
}

func decodeAttributionReplay(record MutationIdempotencyRecord, digest string) (AttributionResult, error) {
	if record.CanonicalDigest != digest {
		return AttributionResult{}, attributionConflict("idempotency key belongs to a different request")
	}
	var stored attributionStoredResult
	if err := json.Unmarshal([]byte(record.ResultPayload), &stored); err != nil {
		return AttributionResult{}, err
	}
	if stored.ManifestDigest == "" || stored.Result.Treated == nil || stored.Result.Restored == nil || stored.Result.Gone == nil || stored.Result.FingerprintChanged == nil || stored.Result.GraphDigest == "" || stored.Result.ReceiptDigest == "" {
		return AttributionResult{}, attributionConflict("retained operation result is malformed")
	}
	return stored.Result, nil
}

// A replay does not depend on the backup remaining online; the durable result
// already binds the request. New effects always re-verify the backup.
func (s *Store) replayAttributionIfPresent(ctx context.Context, key MutationIdempotencyKey, digest string) (AttributionResult, bool, error) {
	record, found, err := s.LookupMutationIdempotency(ctx, key)
	if err != nil || !found {
		return AttributionResult{}, found, err
	}
	out, err := decodeAttributionReplay(record, digest)
	return out, true, err
}

func attributionRecord(ctx context.Context, tx *Transaction, key MutationIdempotencyKey, digest string, m AttributionManifest, result AttributionResult) error {
	data, err := json.Marshal(attributionStoredResult{ManifestDigest: attributionHash(m), Result: result})
	if err != nil {
		return err
	}
	return InsertMutationIdempotencyTx(ctx, tx, MutationIdempotencyInsert{Key: key, CanonicalDigest: digest, OperationID: digest, ResultPayload: string(data), ResultEventIDs: "[]", ChangedRefs: "[]", AuthorizedScopeSnapshot: "{}", ObservedAt: tx.now()})
}

func checkedAttributionBackup(ctx context.Context, path string, expected AttributionManifest) ([]attributionRow, error) {
	db, checksum, closeBackup, err := verifiedAttributionBackup(ctx, path)
	if err != nil {
		return nil, err
	}
	defer closeBackup()
	actual, rows, err := attributionManifest(ctx, db, checksum, expected.Cutoff)
	if err != nil {
		return nil, err
	}
	if attributionHash(actual) != attributionHash(expected) {
		return nil, attributionConflict("backup population or fingerprint does not match manifest")
	}
	return rows, nil
}

func emptyAttributionResult() AttributionResult {
	return AttributionResult{Treated: []int64{}, Restored: []int64{}, Gone: []int64{}, FingerprintChanged: []int64{}}
}

func relationByID(ctx context.Context, q queryer, id int64) (attributionRow, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+attributionColumns+` FROM relations WHERE id=?`, id)
	if err != nil {
		return attributionRow{}, false, err
	}
	r, err := scanAttributionRows(rows)
	if err != nil {
		return attributionRow{}, false, err
	}
	if len(r) == 0 {
		return attributionRow{}, false, nil
	}
	return r[0], true, nil
}

// attributionClearedRow is the exact stored row a clear leaves behind.
func attributionClearedRow(r attributionRow) attributionRow {
	r.Reason, r.Version = "", 0
	r.Types[6], r.Types[7] = "text", "integer"
	return r
}

// boundAttributionClear returns the retained clear result only when it was
// recorded for this manifest and treated exactly the backup's population.
func boundAttributionClear(ctx context.Context, tx *Transaction, clearKey string, manifest AttributionManifest, backup []attributionRow) (attributionStoredResult, error) {
	record, found, err := LookupMutationIdempotencyTx(ctx, tx, attributionKey("clear", clearKey))
	if err != nil {
		return attributionStoredResult{}, err
	}
	if !found {
		return attributionStoredResult{}, attributionConflict("clear operation is missing")
	}
	var stored attributionStoredResult
	if err := json.Unmarshal([]byte(record.ResultPayload), &stored); err != nil {
		return attributionStoredResult{}, err
	}
	if stored.ManifestDigest != attributionHash(manifest) {
		return attributionStoredResult{}, attributionConflict("clear operation does not match manifest")
	}
	ids := make([]int64, 0, len(backup))
	for _, row := range backup {
		ids = append(ids, row.ID)
	}
	if attributionHash(ids) != attributionHash(stored.Result.Treated) {
		return attributionStoredResult{}, attributionConflict("clear operation result does not match manifest")
	}
	return stored, nil
}

// ClearRelationAttribution refuses the entire operation unless the backup, the
// current Q7 population, and the current graph and receipts all match the
// supplied manifest exactly.
func (s *Store) ClearRelationAttribution(ctx context.Context, req AttributionRequest) (AttributionResult, error) {
	if req.IdempotencyKey == "" {
		return AttributionResult{}, attributionConflict("idempotency key is required")
	}
	key := attributionKey("clear", req.IdempotencyKey)
	digest := attributionHash(req)
	if replay, found, err := s.replayAttributionIfPresent(ctx, key, digest); found || err != nil {
		return replay, err
	}
	backup, err := checkedAttributionBackup(ctx, req.BackupPath, req.Manifest)
	if err != nil {
		return AttributionResult{}, err
	}
	out := emptyAttributionResult()
	if err := s.TransactDurable(ctx, func(tx *Transaction) error {
		return clearRelationAttributionTx(ctx, tx, req, key, digest, backup, &out)
	}); err != nil {
		return AttributionResult{}, err
	}
	return out, nil
}

func clearRelationAttributionTx(ctx context.Context, tx *Transaction, req AttributionRequest, key MutationIdempotencyKey, digest string, backup []attributionRow, out *AttributionResult) error {
	if replay, found, err := attributionReplay(ctx, tx, key, digest); found || err != nil {
		*out = replay
		return err
	}
	if err := refuseAttributionKeyReuse(ctx, tx, "clear", req.IdempotencyKey); err != nil {
		return err
	}
	live, err := attributionPopulation(ctx, tx.tx, req.Manifest.Cutoff)
	if err != nil {
		return err
	}
	liveCandidates := make([]AttributionCandidate, 0, len(live))
	for _, row := range live {
		liveCandidates = append(liveCandidates, AttributionCandidate{row.ID, attributionFingerprint(row)})
	}
	if attributionHash(liveCandidates) != attributionHash(req.Manifest.Candidates) {
		return attributionConflict("current population or fingerprint changed")
	}
	beforeGraph, beforeReceipt, err := attributionDigests(ctx, tx.tx)
	if err != nil {
		return err
	}
	if beforeGraph != req.Manifest.GraphDigest || beforeReceipt != req.Manifest.ReceiptDigest {
		return attributionConflict("current graph or receipts differ from the backup")
	}
	guard := sqlitedb.NewWriteGuard(tx.tx)
	if err := guard.Enter(ctx); err != nil {
		return mechanismFailure(err)
	}
	for _, row := range backup {
		if row.Version < 1 {
			return attributionConflict("relation version must be positive")
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE relations SET reason='',record_version=0 WHERE id=?`, row.ID); err != nil {
			return err
		}
		out.Treated = append(out.Treated, row.ID)
	}
	if err := guard.Close(ctx); err != nil {
		return mechanismFailure(err)
	}
	out.GraphDigest, out.ReceiptDigest, err = attributionDigests(ctx, tx.tx)
	if err != nil {
		return err
	}
	if out.GraphDigest != beforeGraph || out.ReceiptDigest != beforeReceipt {
		return attributionConflict("graph or receipts changed during clear")
	}
	out.GraphPreserved, out.ReceiptsPreserved = true, true
	return attributionRecord(ctx, tx, key, digest, req.Manifest, *out)
}

// UndoRelationAttribution restores only unchanged post-clear rows from the
// verified backup. It never inserts a missing relation.
func (s *Store) UndoRelationAttribution(ctx context.Context, req AttributionUndoRequest) (AttributionResult, error) {
	if req.IdempotencyKey == "" || req.ClearKey == "" || req.IdempotencyKey == req.ClearKey {
		return AttributionResult{}, attributionConflict("distinct clear and undo keys are required")
	}
	key := attributionKey("undo", req.IdempotencyKey)
	digest := attributionHash(req)
	if replay, found, err := s.replayAttributionIfPresent(ctx, key, digest); found || err != nil {
		return replay, err
	}
	backup, err := checkedAttributionBackup(ctx, req.BackupPath, req.Manifest)
	if err != nil {
		return AttributionResult{}, err
	}
	out := emptyAttributionResult()
	if err := s.TransactDurable(ctx, func(tx *Transaction) error {
		return undoRelationAttributionTx(ctx, tx, req, key, digest, backup, &out)
	}); err != nil {
		return AttributionResult{}, err
	}
	return out, nil
}

func undoRelationAttributionTx(ctx context.Context, tx *Transaction, req AttributionUndoRequest, key MutationIdempotencyKey, digest string, backup []attributionRow, out *AttributionResult) error {
	if replay, found, err := attributionReplay(ctx, tx, key, digest); found || err != nil {
		*out = replay
		return err
	}
	if err := refuseAttributionKeyReuse(ctx, tx, "undo", req.IdempotencyKey); err != nil {
		return err
	}
	stored, err := boundAttributionClear(ctx, tx, req.ClearKey, req.Manifest, backup)
	if err != nil {
		return err
	}
	beforeGraph, beforeReceipt, err := attributionDigests(ctx, tx.tx)
	if err != nil {
		return err
	}
	guard := sqlitedb.NewWriteGuard(tx.tx)
	if err := guard.Enter(ctx); err != nil {
		return mechanismFailure(err)
	}
	for _, original := range backup {
		current, found, err := relationByID(ctx, tx.tx, original.ID)
		if err != nil {
			return err
		}
		if !found {
			out.Gone = append(out.Gone, original.ID)
			continue
		}
		if attributionFingerprint(current) != attributionFingerprint(attributionClearedRow(original)) {
			out.FingerprintChanged = append(out.FingerprintChanged, original.ID)
			continue
		}
		var reason any = original.Reason
		if original.Types[6] == "blob" {
			reason = []byte(original.Reason)
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE relations SET reason=?,record_version=? WHERE id=?`, reason, original.Version, original.ID); err != nil {
			return err
		}
		out.Restored = append(out.Restored, original.ID)
	}
	if err := guard.Close(ctx); err != nil {
		return mechanismFailure(err)
	}
	out.GraphDigest, out.ReceiptDigest, err = attributionDigests(ctx, tx.tx)
	if err != nil {
		return err
	}
	if out.GraphDigest != beforeGraph || out.ReceiptDigest != beforeReceipt {
		return attributionConflict("graph or receipts changed during undo")
	}
	out.GraphPreserved = out.GraphDigest == stored.Result.GraphDigest
	out.ReceiptsPreserved = out.ReceiptDigest == stored.Result.ReceiptDigest
	return attributionRecord(ctx, tx, key, digest, req.Manifest, *out)
}

// VerifyRelationAttribution classifies the candidate rows against both their
// backup values and the expected post-clear values in one read snapshot.
func (s *Store) VerifyRelationAttribution(ctx context.Context, req AttributionRequest) (AttributionResult, error) {
	if req.IdempotencyKey == "" {
		return AttributionResult{}, attributionConflict("clear key is required")
	}
	backup, err := checkedAttributionBackup(ctx, req.BackupPath, req.Manifest)
	if err != nil {
		return AttributionResult{}, err
	}
	out := emptyAttributionResult()
	if err := s.ReadTransaction(ctx, func(tx *Transaction) error {
		return verifyRelationAttributionTx(ctx, tx, req, backup, &out)
	}); err != nil {
		return AttributionResult{}, err
	}
	return out, nil
}

func verifyRelationAttributionTx(ctx context.Context, tx *Transaction, req AttributionRequest, backup []attributionRow, out *AttributionResult) error {
	stored, err := boundAttributionClear(ctx, tx, req.IdempotencyKey, req.Manifest, backup)
	if err != nil {
		return err
	}
	for _, original := range backup {
		current, found, err := relationByID(ctx, tx.tx, original.ID)
		if err != nil {
			return err
		}
		if !found {
			out.Gone = append(out.Gone, original.ID)
			continue
		}
		switch attributionFingerprint(current) {
		case attributionFingerprint(original):
			out.Restored = append(out.Restored, original.ID)
		case attributionFingerprint(attributionClearedRow(original)):
			out.Treated = append(out.Treated, original.ID)
		default:
			out.FingerprintChanged = append(out.FingerprintChanged, original.ID)
		}
	}
	out.GraphDigest, out.ReceiptDigest, err = attributionDigests(ctx, tx.tx)
	if err != nil {
		return err
	}
	out.GraphPreserved = out.GraphDigest == stored.Result.GraphDigest
	out.ReceiptsPreserved = out.ReceiptDigest == stored.Result.ReceiptDigest
	return nil
}
