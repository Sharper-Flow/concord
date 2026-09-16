package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// seedLinearCLIWorkWithExternalRef seeds a work item carrying an external_ref,
// so the backfill test can exercise the Linear-identity exclusion.
func seedLinearCLIWorkWithExternalRef(t *testing.T, dbPath, workID, projectID, title, externalRef string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	intent, _ := json.Marshal(map[string]any{"title": title, "value_statement": "Backfill value statement", "kind": "task", "priority": 0, "urgency": "standard", "external_ref": externalRef})
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', ?, 'needed', 0, 'standard', 1, ?, '2026-09-16T00:00:00Z', '2026-09-16T00:00:00Z')`, workID, title, string(intent)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLinearBackfillCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "bf-product", "bf-product-project")
	// Captured before the Linear connection exists: the capture enqueue is a
	// silent no-op and the backfill owns these items' visibility.
	seedLinearCLIWork(t, dbPath, "bf-cli-a", "bf-product-project", "Backfill A")
	seedLinearCLIWork(t, dbPath, "bf-cli-b", "bf-product-project", "Backfill B")
	seedLinearCLIWorkWithExternalRef(t, dbPath, "bf-cli-extref", "bf-product-project", "Backfill extref", "linear:existing-issue-uuid")
	enableLinearProduct(t, dbPath, "bf-product")

	var out, errOut strings.Builder
	t.Setenv(dbOverrideEnv, dbPath)
	if code := runWithInput([]string{"linear", "backfill"}, strings.NewReader(`{"product_id":"bf-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("backfill exit=%d stderr=%q", code, errOut.String())
	}
	var first struct {
		OK         bool `json:"ok"`
		Enqueued   int  `json:"enqueued"`
		Operations []struct {
			OperationID string `json:"operation_id"`
			WorkID      string `json:"work_id"`
			OpKind      string `json:"op_kind"`
			State       string `json:"state"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &first); err != nil {
		t.Fatalf("backfill output %q: %v", out.String(), err)
	}
	if !first.OK || first.Enqueued != 2 || len(first.Operations) != 2 {
		t.Fatalf("first backfill = %+v, want 2 queued operations", first)
	}
	seen := map[string]bool{}
	for _, op := range first.Operations {
		if op.OpKind != "issue_create" || op.State != "queued" || op.OperationID == "" {
			t.Fatalf("queued operation = %+v", op)
		}
		seen[op.WorkID] = true
	}
	if !seen["bf-cli-a"] || !seen["bf-cli-b"] {
		t.Fatalf("backfill queued %v, want bf-cli-a and bf-cli-b", seen)
	}

	// The identity exclusion holds in the store, and a second pass finds
	// nothing left to queue because every backfilled item now holds a link.
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var extrefRows, linkRows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='bf-cli-extref'`).Scan(&extrefRows); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_issue_links WHERE work_id IN ('bf-cli-a','bf-cli-b')`).Scan(&linkRows); err != nil {
		t.Fatal(err)
	}
	if extrefRows != 0 || linkRows != 2 {
		t.Fatalf("post-backfill state = extref %d rows, %d links, want 0 and 2", extrefRows, linkRows)
	}
	s.Close()
	out.Reset()
	if code := runWithInput([]string{"linear", "backfill"}, strings.NewReader(`{"product_id":"bf-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("second backfill exit=%d stderr=%q", code, errOut.String())
	}
	var second struct {
		Enqueued int `json:"enqueued"`
	}
	if err := json.Unmarshal([]byte(out.String()), &second); err != nil {
		t.Fatalf("second backfill output %q: %v", out.String(), err)
	}
	if second.Enqueued != 0 {
		t.Fatalf("second backfill enqueued %d operations, want 0", second.Enqueued)
	}
}

func TestLinearBackfillCLIRefusesLocalOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "bf-local-product", "bf-local-project")
	seedLinearCLIWork(t, dbPath, "bf-local-work", "bf-local-project", "Local title")

	var out, errOut strings.Builder
	t.Setenv(dbOverrideEnv, dbPath)
	if code := runWithInput([]string{"linear", "backfill"}, strings.NewReader(`{"product_id":"bf-local-product"}`), &out, &errOut); code == 0 {
		t.Fatalf("backfill on a local_only Product must exit non-zero; stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "local_only") {
		t.Fatalf("stderr=%q, want a local_only diagnostic", errOut.String())
	}
}
