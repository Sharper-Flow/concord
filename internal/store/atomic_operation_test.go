package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestJSON(value any) string {
	raw, _ := json.Marshal(value)
	return testDigest(string(raw))
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestAtomicCommitHookRollsBackAllWrites(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	operation := Operation{Events: []Event{
		productCreatedEvent("hook-product", "hook-product-created"), projectCreatedEvent("hook-project", "hook-project-created"),
		membershipEvent("hook-membership", "product_project.added", SubjectProduct, "hook-product", map[string]any{"product_id": "hook-product", "project_id": "hook-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "hook-product"): 0, VersionRef(SubjectProject, "hook-project"): 0}}
	_, err := applyOperationWithResult(context.Background(), s, operation, func() error { return errors.New("commit gate") })
	if err == nil {
		t.Fatal("commit hook unexpectedly succeeded")
	}
	if got := countRows(t, s, "effect_receipts"); got != 0 {
		t.Fatalf("events after hook rollback = %d", got)
	}
	if got := countRows(t, s, "products"); got != 0 {
		t.Fatalf("products after hook rollback = %d", got)
	}
	if _, err := ApplyOperationWithResult(context.Background(), s, operation); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestBackupVerifyTamperAndOlderSchemaRejection(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	operation := Operation{Events: []Event{
		productCreatedEvent("backup-product", "backup-product-created"), projectCreatedEvent("backup-project", "backup-project-created"),
		membershipEvent("backup-membership", "product_project.added", SubjectProduct, "backup-product", map[string]any{"product_id": "backup-product", "project_id": "backup-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "backup-product"): 0, VersionRef(SubjectProject, "backup-project"): 0}}
	if _, err := ApplyOperationWithResult(context.Background(), s, operation); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := Backup(context.Background(), s, destination)
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	verified, err := VerifyBackup(context.Background(), destination)
	if err != nil || verified.FileSHA256 != manifest.FileSHA256 || verified.SourceEventMaxSeq != manifest.SourceEventMaxSeq {
		t.Fatalf("VerifyBackup() = %+v, %v", verified, err)
	}
	if _, err := VerifyBackup(context.Background(), destination, CurrentSchemaVersion()-1); err == nil {
		t.Fatal("older supported max accepted current snapshot")
	} else {
		assertFailureKind(t, err, KindSchemaUnsupported)
	}
	bytes, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, append(bytes, byte('x')), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(context.Background(), destination); err == nil {
		t.Fatal("tampered backup verified")
	}
}
