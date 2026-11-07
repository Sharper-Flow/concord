package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedResearchExportFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,'now','now')`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,'now','now')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('product','project','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES
		 ('owner','task','Owner','needed',0,1,'now','now',NULL),
		 ('retained','task','Retained','completed',0,1,'now','now','now'),
		 ('consumer','task','Consumer','needed',0,1,'now','now',NULL)`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('owner','project','primary'),('retained','project','primary'),('consumer','project','primary')`,
		`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES
		 ('export','owner',2,'current',6,'now','now'), ('retained','retained',1,'current',1,'now','now')`,
		`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES
		 ('export',1,'Question','{}','{}','[]','Method','now'),
		 ('export',2,'Question','{}','{}','[]','Method','now'),
		 ('retained',1,'Question','{}','{}','[]','Method','now')`,
		`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES
		 ('export',1,'finding','observation','First revision','high','current','active','explicit'),
		 ('export',2,'finding','observation','First revision','high','current','active','explicit'),
		 ('export',2,'second','conclusion','Second revision','medium','unknown','active','home')`,
		`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at) VALUES
		 ('export',1,'source','official_doc','https://example.com/research','Source','Example',NULL,'2026-01-01T00:00:00Z'),
		 ('export',2,'source','official_doc','https://example.com/research','Source','Example',NULL,'2026-01-01T00:00:00Z')`,
		`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES ('export',1,'finding','source'),('export',2,'finding','source')`,
		`INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES ('export',1,'finding','project','project'),('export',2,'finding','project','project')`,
		`INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES ('export',1,'consumer','context',1,'now')`,
		`DELETE FROM write_guard`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("seed research fixture: %v", err)
		}
	}
}

func openResearchExportFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "historical.db")
	db := openMigratedTo(t, path, 182)
	seedResearchExportFixture(t, db)
	return path, db
}

func TestExportResearchDatabaseDeterministicCompleteAndReadOnly(t *testing.T) {
	path, db := openResearchExportFixture(t)
	before, err := backupCurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(t.TempDir(), "first"), t.TempDir()
	for _, destination := range []string{first, second} {
		result, err := ExportResearchDatabase(t.Context(), path, destination)
		if err != nil || result.PackCount != 2 || result.Destination != destination {
			t.Fatalf("export = %+v, %v", result, err)
		}
		if err := VerifyResearchExport(t.Context(), path, destination); err != nil {
			t.Fatalf("verify export: %v", err)
		}
	}
	entries, err := os.ReadDir(first)
	if err != nil || len(entries) != 2 {
		t.Fatalf("files = %v, %v", entries, err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(first, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		other, err := os.ReadFile(filepath.Join(second, entry.Name()))
		if err != nil || !bytes.Equal(data, other) {
			t.Fatalf("re-export differs for %s: %v", entry.Name(), err)
		}
		info, err := entry.Info()
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("export permissions = %v, %v", info, err)
		}
		var document struct {
			PackID string                      `json:"pack_id"`
			Tables map[string][]map[string]any `json:"tables"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		seen[document.PackID] = true
		counts := map[string]int{"active_research_packs": 1, "active_research_revisions": 1, "active_research_findings": 0, "active_research_sources": 0, "active_research_finding_sources": 0, "active_research_finding_scopes": 0, "active_research_consumers": 0}
		if document.PackID == "export" {
			counts["active_research_revisions"] = 2
			counts["active_research_findings"] = 3
			for _, name := range []string{"active_research_sources", "active_research_finding_sources", "active_research_finding_scopes", "active_research_consumers"} {
				counts[name] = 2
			}
			counts["active_research_consumers"] = 1
			if got := document.Tables["active_research_sources"][0]["published_at"]; got != nil {
				t.Errorf("SQL NULL changed: %v", got)
			}
			if got := document.Tables["active_research_revisions"][0]["scope_in_json"]; got != "{}" {
				t.Errorf("stored JSON text changed: %v", got)
			}
			if got := document.Tables["active_research_consumers"][0]["required"]; got != float64(1) {
				t.Errorf("consumer pin changed: %v", got)
			}
		}
		if len(document.Tables) != len(counts) {
			t.Fatalf("tables = %v", document.Tables)
		}
		for table, want := range counts {
			if got := len(document.Tables[table]); got != want {
				t.Errorf("%s %s: got %d rows, want %d", document.PackID, table, got, want)
			}
		}
	}
	if !seen["export"] || !seen["retained"] {
		t.Fatalf("export omitted active or retained pack: %v", seen)
	}
	after, err := backupCurrentStateDigest(t.Context(), db)
	if err != nil || after != before {
		t.Fatalf("export changed source state: %s -> %s, %v", before, after, err)
	}
}

func TestExportResearchVerifierRejectsChanges(t *testing.T) {
	path, _ := openResearchExportFixture(t)
	for _, change := range []string{"tamper", "missing", "extra", "symlink"} {
		t.Run(change, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "export")
			if _, err := ExportResearchDatabase(t.Context(), path, destination); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 2 {
				t.Fatalf("export files: %v, %v", entries, err)
			}
			file := filepath.Join(destination, entries[0].Name())
			switch change {
			case "tamper":
				err = os.WriteFile(file, []byte("changed\n"), 0o600)
			case "missing":
				err = os.Remove(file)
			case "extra":
				err = os.WriteFile(filepath.Join(destination, "extra"), []byte("extra"), 0o600)
			case "symlink":
				data, readErr := os.ReadFile(file)
				if readErr != nil {
					t.Fatal(readErr)
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(target, file)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyResearchExport(t.Context(), path, destination); err == nil {
				t.Fatalf("verifier accepted %s", change)
			}
		})
	}
}

func TestExportResearchDatabaseEmpty(t *testing.T) {
	s := openTemp(t)
	destination := filepath.Join(t.TempDir(), "empty")
	result, err := ExportResearchDatabase(t.Context(), s.Path(), destination)
	if err != nil || result.PackCount != 0 {
		t.Fatalf("empty export = %+v, %v", result, err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty export files = %v, %v", entries, err)
	}
}

func TestExportResearchDatabaseRefusesNonEmptyDestination(t *testing.T) {
	s := openTemp(t)
	destination := t.TempDir()
	path := filepath.Join(destination, "keep")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportResearchDatabase(t.Context(), s.Path(), destination); err == nil {
		t.Fatal("non-empty destination accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing destination changed: %q, %v", data, err)
	}
}

func TestExportResearchDatabaseMissingSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	destination := filepath.Join(t.TempDir(), "export")
	if _, err := ExportResearchDatabase(t.Context(), path, destination); err == nil {
		t.Fatal("missing source accepted")
	}
	for _, candidate := range []string{path, destination} {
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Errorf("missing-source export created %s: %v", candidate, err)
		}
	}
}

func TestExportResearchDatabaseRefusesLossyText(t *testing.T) {
	path, db := openResearchExportFixture(t)
	for _, value := range []any{string([]byte{0xff}), []byte{0xff}} {
		if _, err := db.ExecContext(t.Context(), `UPDATE active_research_revisions SET question=? WHERE pack_id=?`, value, "export"); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(t.TempDir(), "export")
		if _, err := ExportResearchDatabase(t.Context(), path, destination); err == nil || !strings.Contains(err.Error(), "non-UTF-8") {
			t.Fatalf("lossy text accepted: %v", err)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatalf("refused export created output: %v", err)
		}
	}
}

func TestExportResearchDatabaseWriteFailureExplainsRecovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	path, _ := openResearchExportFixture(t)
	destination := t.TempDir()
	if err := os.Chmod(destination, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(destination, 0o700); err != nil {
			t.Error(err)
		}
	})
	_, err := ExportResearchDatabase(t.Context(), path, destination)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Op != "research_export" || !strings.Contains(failure.RecoveryAction, "keep the source database") || !strings.Contains(failure.RecoveryAction, "rerun the upgrade") {
		t.Fatalf("write failure omits export recovery: %v", err)
	}
}
