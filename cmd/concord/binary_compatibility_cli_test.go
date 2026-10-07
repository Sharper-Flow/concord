package main

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestCLIRefusesBinarySkewBeforeMutation(t *testing.T) {
	path := freshMigratedCLIDatabase(t)
	seedAuthorizedDispatchWindow(t, path, "work-con411", "attempt-con411")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db := s.DatabaseForTesting()
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET definition_version=2147483647,definition_digest=?;
		DELETE FROM fold_guard;
		INSERT INTO runtime_state_writers(surface,definition_ref,version,digest,binary_version)
		SELECT 'workflow',definition_ref,definition_version,definition_digest,'v99.0.0' FROM workflow_instances GROUP BY definition_ref,definition_version,definition_digest`, digest); err != nil {
		t.Fatal(err)
	}
	before, err := s.DomainEventWatermark(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binary := buildCoreFrom(t, repoSourceDir(t), "v11.0.0-con411-serving")
	code, out, diagnostic := runRelease(t, binary, path, "client-revoke", `{"client_ref":"client-con411"}`)
	if code != 1 || out != "" {
		t.Fatalf("incompatible binary must refuse before command output: code=%d out=%q stderr=%q", code, out, diagnostic)
	}
	for _, want := range []string{"binary compatibility refused", "v11.0.0-con411-serving", "v99.0.0", "stop traffic", "drain incompatible binaries"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("operator diagnostic omitted %q: %q", want, diagnostic)
		}
	}
	after, err := s.DomainEventWatermark(context.Background())
	if err != nil || after != before {
		t.Fatalf("refused command changed the event log: before=%d after=%d error=%v", before, after, err)
	}
}
