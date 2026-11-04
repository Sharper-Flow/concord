package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

type outcomeRetirementProofKey struct{}

type outcomeRetirementProof struct {
	snapshot string
	checksum string
	digest   string
	verified bool
	// refusal is raised only if migration 185 has rows that need the proof.
	refusal error
}

func withOutcomeRetirementProof(ctx context.Context, snapshot string, manifest BackupManifest) context.Context {
	return context.WithValue(ctx, outcomeRetirementProofKey{}, &outcomeRetirementProof{snapshot: snapshot, checksum: manifest.DBChecksum, digest: manifest.currentStateDigest})
}

func prepareOutcomeRetirementUpgrade(ctx context.Context, before map[int]appliedMigration, options []UpgradeOptions) (context.Context, error) {
	if _, done := before[185]; !done {
		for _, option := range options {
			if option.SnapshotPath != "" {
				return prepareOutcomeRetirementSnapshot(ctx, option.SnapshotPath)
			}
		}
	}
	return ctx, nil
}

func prepareOutcomeRetirementSnapshot(ctx context.Context, path string) (context.Context, error) {
	manifest, err := VerifyBackup(ctx, path)
	if err != nil {
		return ctx, err
	}
	return withOutcomeRetirementProof(ctx, path, manifest), nil
}

// A supplied snapshot is compared with the same current-state digest as the
// backup verifier, under the migration transaction, before any pending
// migration changes rows. Drift is recorded rather than refused here: only
// migration 185 knows whether it has rows the snapshot must preserve.
func verifyOutcomeRetirementSnapshot(ctx context.Context, tx *sql.Tx, scope migrationScope, applied map[int]appliedMigration) error {
	if scope != migrateForUpgrade || len(applied) == 0 {
		return nil
	}
	if _, done := applied[185]; done {
		return nil
	}
	proof, ok := ctx.Value(outcomeRetirementProofKey{}).(*outcomeRetirementProof)
	if !ok {
		return nil
	}
	// A retried migration transaction must not inherit an earlier attempt's
	// result: the store may have changed between attempts.
	proof.verified, proof.refusal = false, nil
	checksum, err := fileSHA256(proof.snapshot)
	if err != nil || checksum != proof.checksum {
		proof.refusal = newFailure(KindUpgradeBlocked, "migrate", "outcome retirement snapshot changed after verification", false, "create a fresh verified snapshot")
		return nil
	}
	digest, err := sqlitedb.CurrentStateDigestTx(ctx, tx)
	if err != nil {
		return mechanismFailure(err)
	}
	if digest != proof.digest || digest == "" {
		proof.refusal = newFailure(KindUpgradeBlocked, "migrate", "store changed after the verified snapshot", false, "create a fresh verified snapshot")
		return nil
	}
	proof.verified = true
	return nil
}

// Migration 185 requires verified preservation of outcome rows and evidence
// declarations. Empty sources allow fresh creation and lower-level fixtures. The
// upgrade command independently snapshots existing databases before breaking
// migrations, regardless of the outcome tables' population.
func requireOutcomeRetirementSnapshot(ctx context.Context, q migrationRunner) error {
	var populated bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_contract_predicates)
 OR EXISTS(SELECT 1 FROM workflow_predicate_verdicts)
 OR EXISTS(SELECT 1 FROM workflow_review_state)
 OR EXISTS(SELECT 1 FROM workflow_contracts WHERE json_array_length(required_evidence)>0)`).Scan(&populated); err != nil {
		return err
	}
	if !populated {
		return nil
	}
	if proof, ok := ctx.Value(outcomeRetirementProofKey{}).(*outcomeRetirementProof); ok {
		if proof.verified {
			return nil
		}
		if proof.refusal != nil {
			return proof.refusal
		}
	}
	return newFailure(KindUpgradeBlocked, "migrate", "outcome retirement requires a verified snapshot", false, "run concord upgrade with a verified snapshot")
}

type historicalOutcomeContract struct {
	work    string
	version int64
}

var historicalOutcomeSubject = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{1,127}$`)
var historicalOutcomeSurface = regexp.MustCompile(`^[a-z][a-z0-9_.:/-]{1,127}$`)
var historicalOutcomeCheck = regexp.MustCompile(`^check:[a-z][a-z0-9_.:/-]{1,127}$`)
var historicalOutcomeActor = regexp.MustCompile(`^actor:[0-9a-f]{64}$`)
var historicalOutcomeApproval = regexp.MustCompile(`^approval:[a-z0-9][a-z0-9_.:/-]{1,127}$`)

func retireOutcomeContract(ctx context.Context, q migrationRunner, migrationSQL string) error {
	if err := requireOutcomeRetirementSnapshot(ctx, q); err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT work_id,contract_version,outcome_kind,outcome_payload FROM workflow_contract_predicates ORDER BY work_id,contract_version,ordinal`)
	if err != nil {
		return err
	}
	sets := make(map[historicalOutcomeContract]map[string]struct{})
	for rows.Next() {
		var key historicalOutcomeContract
		var kind, payload string
		if err := rows.Scan(&key.work, &key.version, &kind, &payload); err != nil {
			rows.Close()
			return err
		}
		subjects, err := historicalOutcomeSubjects(kind, []byte(payload))
		if err != nil {
			rows.Close()
			return fmt.Errorf("malformed historical predicate for %s version %d: %w", key.work, key.version, err)
		}
		if len(subjects) == 0 {
			continue
		}
		if sets[key] == nil {
			sets[key] = make(map[string]struct{})
		}
		for _, subject := range subjects {
			sets[key][subject] = struct{}{}
		}
		if len(sets[key]) > 800 {
			rows.Close()
			return fmt.Errorf("historical legislative subjects exceed 800 for %s version %d", key.work, key.version)
		}
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	alter, drops, ok := strings.Cut(migrationSQL, ";")
	if !ok {
		return fmt.Errorf("outcome retirement SQL is malformed")
	}
	if _, err := q.ExecContext(ctx, alter); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		return err
	}
	for key, set := range sets {
		subjects := slices.Sorted(maps.Keys(set))
		encoded, err := json.Marshal(subjects)
		if err != nil {
			return err
		}
		result, err := q.ExecContext(ctx, `UPDATE workflow_contracts SET legislative_subjects=? WHERE work_id=? AND contract_version=?`, string(encoded), key.work, key.version)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return fmt.Errorf("historical legislative subject transfer did not update its contract")
		}
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, drops)
	return err
}

// maxHistoricalPredicateBytes is the bound every pre-185 writer and reader
// applied to one stored predicate; a larger row was never admitted.
const maxHistoricalPredicateBytes = 64 * 1024

// Historical decoding is confined to migration 185; it is not an authoring
// interface or a compatibility path for new contracts.
func historicalOutcomeSubjects(kind string, payload []byte) ([]string, error) {
	fields, err := historicalOutcomeFields(kind, payload)
	if err != nil {
		return nil, err
	}
	if kind != "exists" && kind != "absent" {
		return nil, validateHistoricalNonPresence(kind, fields)
	}
	return historicalPresenceSubjects(kind, fields)
}

func historicalOutcomeFields(kind string, payload []byte) (map[string]json.RawMessage, error) {
	if len(payload) == 0 || len(payload) > maxHistoricalPredicateBytes {
		return nil, fmt.Errorf("invalid predicate size")
	}
	structure := json.NewDecoder(bytes.NewReader(payload))
	if err := validateHistoricalJSON(structure, 0); err != nil {
		return nil, err
	}
	if _, err := structure.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing predicate data")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || fields[key] != nil {
			return nil, fmt.Errorf("duplicate or invalid field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing predicate data")
	}
	var encodedKind string
	if json.Unmarshal(fields["kind"], &encodedKind) != nil || encodedKind != kind {
		return nil, fmt.Errorf("predicate kind mismatch")
	}
	return fields, nil
}

func historicalPresenceSubjects(kind string, fields map[string]json.RawMessage) ([]string, error) {
	for key := range fields {
		if key != "kind" && key != "surface" && key != "subjects" && !(kind == "absent" && key == "distinguish_from") {
			return nil, fmt.Errorf("unexpected predicate field")
		}
	}
	var surface string
	if json.Unmarshal(fields["surface"], &surface) != nil || !historicalOutcomeSurface.MatchString(surface) {
		return nil, fmt.Errorf("invalid predicate surface")
	}
	if kind == "absent" {
		var distinctions []string
		if json.Unmarshal(fields["distinguish_from"], &distinctions) != nil || len(distinctions) < 1 || len(distinctions) > 4 {
			return nil, fmt.Errorf("invalid predicate distinctions")
		}
		seen := make(map[string]bool, len(distinctions))
		for _, value := range distinctions {
			if (value != "archived" && value != "relocated" && value != "renamed" && value != "disabled") || seen[value] {
				return nil, fmt.Errorf("invalid predicate distinction")
			}
			seen[value] = true
		}
	}
	var subjects []string
	if json.Unmarshal(fields["subjects"], &subjects) != nil || len(subjects) < 1 || len(subjects) > 100 {
		return nil, fmt.Errorf("invalid subjects")
	}
	seen := make(map[string]bool, len(subjects))
	for _, subject := range subjects {
		if !historicalOutcomeSubject.MatchString(subject) || seen[subject] {
			return nil, fmt.Errorf("invalid or duplicate subject")
		}
		seen[subject] = true
	}
	return subjects, nil
}

func validateHistoricalJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("predicate nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected JSON delimiter")
	}
	keys := map[string]bool{}
	count := 0
	for decoder.More() {
		count++
		if count > 128 {
			return fmt.Errorf("predicate container too large")
		}
		if delim == '{' {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || keys[name] {
				return fmt.Errorf("duplicate or invalid predicate field")
			}
			keys[name] = true
		}
		if err := validateHistoricalJSON(decoder, depth+1); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
		return fmt.Errorf("invalid predicate container")
	}
	return nil
}

func validateHistoricalNonPresence(kind string, fields map[string]json.RawMessage) error {
	allowed := map[string][]string{
		"outcome": {"kind", "allowed", "decision_record"},
		"check":   {"kind", "check_ref", "immutable_subject_ref", "expected_result"},
	}
	names, ok := allowed[kind]
	if !ok {
		return fmt.Errorf("unknown predicate kind")
	}
	for name := range fields {
		if !slices.Contains(names, name) {
			return fmt.Errorf("unexpected predicate field")
		}
	}
	if kind == "check" {
		var ref, subject, result string
		if json.Unmarshal(fields["check_ref"], &ref) != nil || json.Unmarshal(fields["immutable_subject_ref"], &subject) != nil || json.Unmarshal(fields["expected_result"], &result) != nil ||
			!historicalOutcomeCheck.MatchString(ref) || !historicalOutcomeSubject.MatchString(subject) ||
			!slices.Contains([]string{"true", "false", "pass", "fail", "present", "absent", "healthy", "unhealthy", "accepted", "rejected"}, result) {
			return fmt.Errorf("invalid check predicate")
		}
		return nil
	}
	var tokens []string
	if json.Unmarshal(fields["allowed"], &tokens) != nil || len(tokens) < 1 || len(tokens) > 8 {
		return fmt.Errorf("invalid allowed outcomes")
	}
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if seen[token] || !slices.Contains([]string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}, token) {
			return fmt.Errorf("invalid allowed outcome")
		}
		seen[token] = true
	}
	// A JSON null decision_record decoded to an absent record historically.
	// Writers before the approval challenge retirement required
	// operator_approval_ref; later writers omitted it. Both shapes are stored.
	var decision *struct {
		Question            string          `json:"question"`
		OptionsConsidered   []string        `json:"options_considered"`
		Decision            string          `json:"decision"`
		Rationale           string          `json:"rationale"`
		Consequences        []string        `json:"consequences"`
		Inputs              []string        `json:"inputs"`
		POCFindings         string          `json:"poc_findings"`
		Supersedes          *string         `json:"supersedes"`
		SupersededBy        *string         `json:"superseded_by"`
		Unknowns            []string        `json:"unknowns"`
		RequiredToDecide    []string        `json:"required_to_decide"`
		ReviewerActorRef    string          `json:"reviewer_actor_ref"`
		OperatorApprovalRef json.RawMessage `json:"operator_approval_ref"`
	}
	if raw, exists := fields["decision_record"]; exists {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&decision) != nil {
			return fmt.Errorf("invalid decision record")
		}
	}
	if decision != nil {
		if len(decision.Question) < 1 || len(decision.Question) > 4096 ||
			len(decision.OptionsConsidered) < 1 || len(decision.OptionsConsidered) > 16 ||
			!slices.Contains([]string{"accepted_decision", "insufficient_evidence"}, decision.Decision) ||
			len(decision.Rationale) < 1 || len(decision.Rationale) > 4096 ||
			len(decision.Consequences) < 1 || len(decision.Consequences) > 16 ||
			len(decision.Inputs) < 1 || len(decision.Inputs) > 32 ||
			len(decision.POCFindings) < 1 || len(decision.POCFindings) > 4096 ||
			len(decision.Unknowns) > 16 || len(decision.RequiredToDecide) > 16 ||
			!historicalOutcomeActor.MatchString(decision.ReviewerActorRef) ||
			!historicalApprovalRef(decision.OperatorApprovalRef) ||
			(decision.Decision == "insufficient_evidence" && (len(decision.Unknowns) == 0 || len(decision.RequiredToDecide) == 0)) ||
			(len(tokens) == 1 && tokens[0] != decision.Decision) {
			return fmt.Errorf("invalid decision record")
		}
		options := make(map[string]bool, len(decision.OptionsConsidered))
		for _, option := range decision.OptionsConsidered {
			if options[option] {
				return fmt.Errorf("duplicate decision option")
			}
			options[option] = true
		}
	}
	return nil
}

func historicalApprovalRef(raw json.RawMessage) bool {
	if raw == nil {
		return true
	}
	var ref string
	return !bytes.Equal(raw, []byte("null")) && json.Unmarshal(raw, &ref) == nil && historicalOutcomeApproval.MatchString(ref)
}
