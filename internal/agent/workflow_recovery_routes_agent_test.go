package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func recoveryRegistry(t *testing.T, s *store.Store) (hash, root string) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash,root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&hash, &root); err != nil {
		t.Fatal(err)
	}
	return hash, root
}

func recoveryDomainRepository(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	toolingPath := filepath.Join(home.RepoPath, ".concord", "tooling.v1.json")
	if err := os.WriteFile(toolingPath, []byte(`{"schema_version":"1.0","project":"fixture","tools":[{"id":"go-vet","invocation":"go vet ./...","tier":"fast"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".concord/tooling.v1.json"}, {"commit", "--quiet", "-m", "declare fixture verification tooling"}} {
		if output, err := exec.Command("git", append([]string{"-C", home.RepoPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture tooling: %v: %s", err, output)
		}
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"remote", "add", "origin", "https://example.invalid/fixture.git"}, {"update-ref", "refs/remotes/origin/main", "HEAD"}, {"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}} {
		if output, err := exec.Command("git", append([]string{"-C", home.RepoPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture repository: %v: %s", err, output)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE project_locators SET locator_value=?1, normalized_value=?1 WHERE project_id='project-1' AND kind='canonical_path'; DELETE FROM fold_guard`, home.RepoPath); err != nil {
		t.Fatalf("point the fixture locator at the repository: %v", err)
	}
	tooling, err := store.ResolveWorkProjectTooling(ctx, s, "work-1")
	if err != nil || tooling == nil || !store.ProjectToolingInvocationDeclared(tooling, []string{"go", "vet", "./..."}) {
		t.Fatalf("resolve the fixture's default-ref tooling: %+v, %v", tooling, err)
	}
	return home.RepoPath
}

// Record the job under the active contract, including after supersession.
func recordRecoveryWorkerJob(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string) {
	t.Helper()
	active, err := s.ActiveWorkflowContract(context.Background(), "work-1")
	if err != nil {
		t.Fatal(err)
	}
	recordReadyRetryJobWithChecks(t, s, service, env, jobID, active.Version, []string{"go vet ./..."})
}

// TestWorkflowRecoveryRoutesFrozenGenericOneOffV13HasUnchangedDigest is
// the frozen-bound guard (CD-0115 D1): the table-less v13 the
// quarantined table serves stays the only published digest for the
// generic_one_off family at that version, no declared table is carried
// into v13, and the v14 declared table is the only source of the
// routes the engine resolves. A new author cannot restore the route
// table on v13 by writing a future v14; the quarantine refuses to
// leak (ref, version) data the new author does not name.
func TestWorkflowRecoveryRoutesFrozenGenericOneOffV13HasUnchangedDigest(t *testing.T) {
	t.Parallel()
	v13, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.generic_one_off", 13)
	if !ok {
		t.Fatal("workflow.generic_one_off v13 is not registered")
	}
	if v13.Definition.RecoveryRoutes != nil {
		t.Fatalf("v13 carries a declared recovery-route table: %+v", v13.Definition.RecoveryRoutes)
	}
	// The frozen v14 builder must not alter the v13 digest. The
	// frozen builders compose the predecessor's content; the only
	// content change at v14 is the declared recovery_routes field,
	// which the field's omitempty tag excludes from the canonical
	// bytes. The pin's digest in
	// workflow_definition_version_pins_test.go remains the source
	// of truth; the live computation must match.
	expected := "sha256:964098ba2681f7fab8e1f67f90478af30ad7fb7442e6af14e9ac232d07825d0f"
	computed, err := store.WorkflowDefinitionDigest(v13.Definition)
	if err != nil {
		t.Fatalf("compute v13 digest: %v", err)
	}
	if computed != expected {
		t.Fatalf("v13 digest %s drifted from the frozen pin %s", computed, expected)
	}
	// v14 carries a declared table that the v13 instance never
	// inherits. The two sources (declared v14 table, quarantined
	// v13 data) must not carry each other; a v14 instance is never
	// served the v13 quarantined routes, and a v13 instance is
	// never served the v14 declared routes. The reader's choice
	// order (declared before quarantine) and the test in
	// workflow_recovery_routes_test.go hold the boundary.
	v14, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.generic_one_off", 14)
	if !ok {
		t.Fatal("workflow.generic_one_off v14 is not registered")
	}
	if len(v14.Definition.RecoveryRoutes) != 2 {
		t.Fatalf("v14 declared %d routes, want 2 (the declared table is the only owner)", len(v14.Definition.RecoveryRoutes))
	}
	for _, route := range v14.Definition.RecoveryRoutes {
		if route.Trigger != store.WorkflowRecoveryTriggerUnhealthyVerdict || route.Action != "request_correction" {
			t.Errorf("v14 route %+v pairs trigger with the wrong action", route)
		}
	}
}

// recoveryProducerLane resolves the producer lane the tuple names.
// A missing or unregistered lane is a fixture defect, not a test
// pass.
func recoveryProducerLane(t *testing.T, laneID string) store.LaneDefinition {
	t.Helper()
	for _, candidate := range store.BuiltinLaneDefinitions() {
		if candidate.ID == laneID {
			return candidate
		}
	}
	t.Fatalf("producer lane %q is not registered", laneID)
	return store.LaneDefinition{}
}

// attemptEpoch reads the epoch of this exact authorized dispatch.
func attemptEpoch(t *testing.T, s *store.Store, attemptID string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted, attemptID).Scan(&epoch); err != nil {
		t.Fatalf("read dispatch epoch for %s: %v", attemptID, err)
	}
	if epoch == 0 {
		t.Fatalf("attempt %s has no recorded dispatch epoch", attemptID)
	}
	return epoch
}

// stepOf reads the workflow instance's current step.
func stepOf(t *testing.T, s *store.Store) string {
	t.Helper()
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}
