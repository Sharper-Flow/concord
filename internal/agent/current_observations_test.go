package agent

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// agentSeedReceipt supplies identity for a directly seeded current-row fixture.
// The caller owns the row and commits both in the same migrated-store transaction.
func agentSeedReceipt(t *testing.T, tx *sql.Tx, id, kind, work, actor string, version int, payload []byte) int64 {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := tx.QueryRow(`UPDATE store_sequences SET last=last+1 WHERE name='effect' RETURNING last`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,?,'work_item',?,?,?,?)`, id, seq, kind, work, actor, version, fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))); err != nil {
		t.Fatal(err)
	}
	return seq
}

// agentRowsSnapshot compares row contents as well as identity and cardinality.
// Sorting the encoded rows makes the comparison independent of query order.
func agentRowsSnapshot(t *testing.T, s *store.Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	encoded := []string{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if raw, ok := value.([]byte); ok {
				values[i] = string(raw)
			}
		}
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, string(raw))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(encoded)
	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func agentCurrentEffects(t *testing.T, s *store.Store) string {
	t.Helper()
	state := make(map[string]string)
	for _, table := range []string{
		"store_sequences", "effect_receipts", "work_items", "work_projects",
		"workflow_instances", "workflow_contracts", "workflow_phase_exits", "relations",
		"workflow_actors", "workflow_step_epochs", "workflow_action_obligations",
		"workflow_step_completions", "workflow_evidence_bindings", "workflow_delivery_state",
		"workflow_overlap_resolutions", "work_context_state", "domain_observations",
		"workflow_review_state", "worktree_verify_leases", "worktree_entries", "worktree_claims",
	} {
		state[table] = agentRowsSnapshot(t, s, `SELECT * FROM `+table)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
