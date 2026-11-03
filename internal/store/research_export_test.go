package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportResearchDatabaseDeterministicCompleteAndReadOnly(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "retained", "consumer")
	pack := createSimplePack(t, s, "export", "owner")
	if _, err := recordResearchSource(t.Context(), s, ResearchSourceRequest{
		PackID: pack.PackID, ExpectedVersion: 1,
		Source: ResearchSource{SourceID: "source", Kind: SourceOfficialDoc, Locator: "https://example.com/research", Title: "Source", PublisherOrAuthor: "Example", AccessedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchFinding(t.Context(), s, ResearchFindingRequest{
		PackID: pack.PackID, ExpectedVersion: 2,
		Finding: ResearchFinding{FindingID: "finding", Kind: FindingObservation, Statement: "First revision", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"source"}, Scopes: ResearchScopes{Mode: "explicit", ProjectIDs: []string{"project"}}},
	}); err != nil {
		t.Fatal(err)
	}
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
	if _, err := appendResearchRevision(t.Context(), s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 4, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchFinding(t.Context(), s, ResearchFindingRequest{
		PackID: pack.PackID, ExpectedVersion: 5,
		Finding: ResearchFinding{FindingID: "second", Kind: FindingConclusion, Statement: "Second revision", Confidence: ConfidenceMedium, Freshness: ResearchUnknown, Status: FindingActive},
	}); err != nil {
		t.Fatal(err)
	}
	retained := createSimplePack(t, s, "retained", "retained")
	terminalizeResearchOwner(t, s, "retained")
	before, err := backupCurrentStateDigest(t.Context(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(t.TempDir(), "first"), t.TempDir()
	for _, destination := range []string{first, second} {
		result, err := ExportResearchDatabase(t.Context(), s.Path(), destination)
		if err != nil || result.PackCount != 2 || result.Destination != destination {
			t.Fatalf("export = %+v, %v", result, err)
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
		if document.PackID == pack.PackID {
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
	if !seen[pack.PackID] || !seen[retained.PackID] {
		t.Fatalf("export omitted active or retained pack: %v", seen)
	}
	after, err := backupCurrentStateDigest(t.Context(), s.db)
	if err != nil || after != before {
		t.Fatalf("export changed source state: %s -> %s, %v", before, after, err)
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
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "invalid-text", "owner")
	for _, value := range []any{string([]byte{0xff}), []byte{0xff}} {
		if _, err := s.db.ExecContext(t.Context(), `UPDATE active_research_revisions SET question=? WHERE pack_id=?`, value, pack.PackID); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(t.TempDir(), "export")
		if _, err := ExportResearchDatabase(t.Context(), s.Path(), destination); err == nil || !strings.Contains(err.Error(), "non-UTF-8") {
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
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	createSimplePack(t, s, "write-failure", "owner")
	destination := t.TempDir()
	if err := os.Chmod(destination, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(destination, 0o700); err != nil {
			t.Error(err)
		}
	})
	_, err := ExportResearchDatabase(t.Context(), s.Path(), destination)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Op != "research_export" || !strings.Contains(failure.RecoveryAction, "keep the source database") || !strings.Contains(failure.RecoveryAction, "new empty directory") {
		t.Fatalf("write failure omits export recovery: %v", err)
	}
}
