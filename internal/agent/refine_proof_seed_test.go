package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/store"
)

func seedAgentEvidenceProducer(t *testing.T, s *store.Store, workID, principal, ref, runRef, watermark string) {
	t.Helper()
	refs, err := json.Marshal([]string{ref})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,result_kind,evidence_refs,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runRef, 1, workID, "fix", "sha256:"+strings.Repeat("a", 64), "implement", "internal_sqlite", "sha256:"+strings.Repeat("b", 64), "", principal, watermark, "2026-09-17T00:00:00Z", "completed", string(refs), "sha256:"+strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func agentBindVerificationReport(t *testing.T, f *spineFixture, commit, runRef string) string {
	t.Helper()
	ref := "commit:" + commit
	watermark := fmt.Sprintf("request:producer-%d", f.request)
	seedAgentEvidenceProducer(t, f.s, f.work, f.env.PrincipalRef, ref, runRef, watermark)
	spineOK(t, f.action(t, "bind_evidence", map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": ref,
		"producer_id": f.env.PrincipalRef, "producer_run_ref": runRef, "producer_watermark": watermark,
	}))
	return ref
}

// fixtureRepoWithOrigin creates one real git repository whose origin HEAD is
// set, the shape the tooling resolution reads the default ref from.
func fixtureRepoWithOrigin(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd, err := gittest.Command(repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "fixture")
	run("remote", "add", "origin", "https://example.invalid/fixture.git")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}
