package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmallNativeIdentitySurvivesReclaimedSymlinkPrefix(t *testing.T) {
	ctx := context.Background()
	realRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "claim-root")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}
	j := smallReviewDispositionJourneyAtRoot(t, "small-retained-identity", alias)
	canonical, err := canonicalWorkerWorktreePath(j.repo)
	if err != nil {
		t.Fatal(err)
	}
	var authSeq int64
	var authRaw []byte
	var claim string
	if err := j.s.DatabaseForTesting().QueryRow(`SELECT seq,payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')='small-review' AND json_extract(payload,'$.action_id')='dispatch_worker'`, j.workID, WorkflowActionCompleted).Scan(&authSeq, &authRaw); err != nil {
		t.Fatal(err)
	}
	if err := j.s.DatabaseForTesting().QueryRow(`SELECT claim_op_id FROM worktree_entries WHERE set_id=? AND project_id='project'`, WorktreeSetID(j.workID)).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	var dispatch map[string]json.RawMessage
	if err := json.Unmarshal(authRaw, &dispatch); err != nil {
		t.Fatal(err)
	}
	assertField := func(fields map[string]json.RawMessage, key, want string) {
		t.Helper()
		var got string
		if err := json.Unmarshal(fields[key], &got); err != nil || got != want {
			t.Errorf("retained %s = %q, %v; want %q", key, got, err, want)
		}
	}
	assertField(dispatch, "worker_project_id", "project")
	assertField(dispatch, "worker_claim_op_id", claim)
	assertField(dispatch, "worker_worktree_identity", workerWorktreeIdentity(canonical))
	var raw string
	if err := j.s.DatabaseForTesting().QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE work_id=? AND lease_id='candidate-small-integration'`, j.workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		t.Fatal(err)
	}
	assertField(receipt, "project_id", "project")
	assertField(receipt, "claim_op_id", claim)
	assertField(receipt, "worktree_identity", workerWorktreeIdentity(canonical))
	// Model the retained auxiliary projection with a lexical symlink path,
	// which the recovery owner supports, after reclamation. Native events
	// and their claim provenance stay unchanged. This does not test admission
	// of reclamation or assert that the current claim producer emits aliases.
	relative, err := filepath.Rel(realRoot, canonical)
	if err != nil {
		t.Fatal(err)
	}
	retainedPath := filepath.Join(alias, relative)
	if _, err := j.s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worktree_entries SET state='reclaimed',path=? WHERE set_id=?; DELETE FROM fold_guard`, retainedPath, WorktreeSetID(j.workID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(realRoot); err != nil {
		t.Fatal(err)
	}
	readOriginalProject := func() {
		t.Helper()
		tx, err := j.s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		project, err := workerAttemptProjectTx(ctx, tx, j.workID, "small-review", "test", "restore the native claim")
		if err != nil || project != "project" {
			t.Errorf("the retained native identity needs the missing symlink: %q, %v", project, err)
		}
	}
	readOriginalProject()
	if _, err := findAuthorizedDispatchWindowBefore(ctx, j.s.DatabaseForTesting(), j.workID, "small-review", authSeq); err == nil {
		t.Error("future native authorization qualified an earlier prefix")
	}
	if window, err := findAuthorizedDispatchWindowBefore(ctx, j.s.DatabaseForTesting(), j.workID, "small-review", authSeq+1); err != nil || window.WorktreeIdentity != workerWorktreeIdentity(canonical) || window.ProjectID != "project" || window.ClaimOpID != claim {
		t.Errorf("the original authorization disappeared with its filesystem: %+v, %v", window, err)
	}
	if err := RebuildFromLog(ctx, j.s); err != nil {
		t.Fatalf("rebuild with the reclaimed, retargeted symlink: %v", err)
	}
	readOriginalProject()
}

func TestNativeDispatchIdentityUpcastPreservesPayloadBytes(t *testing.T) {
	t.Parallel()
	raw := []byte(`{ "work_id": "old-work", "step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 1, "worker_context_frontier": 0, "worker_packet_predicate_ids": [], "worker_worktree_identity": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }`)
	event := Event{Kind: WorkflowActionCompleted, PayloadVersion: 7, Payload: raw}
	upcast, err := upcastWorkflowActionCompletedV7(event)
	if err != nil || upcast.PayloadVersion != workflowActionCompletedDispatchIdentityVersion || !bytes.Equal(upcast.Payload, raw) {
		t.Fatalf("the additive identity boundary changed old payload bytes: %+v, %v", upcast, err)
	}
	var decoded workflowActionCompletedPayload
	if err := decodeWorkflowPayload(upcast, &decoded); err != nil || decoded.WorkerProjectID != "" || decoded.WorkerClaimOpID != "" {
		t.Fatalf("old payload acquired native identity: %+v, %v", decoded, err)
	}
	for _, key := range []string{"worker_project_id", "worker_claim_op_id"} {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			t.Fatal(err)
		}
		members[key] = json.RawMessage(`"forged"`)
		event.Payload = mustJSONValue(members)
		if _, err := upcastWorkflowActionCompletedV7(event); err == nil {
			t.Errorf("the old payload smuggled %s across the identity boundary", key)
		}
	}
}

func TestNativeDispatchIdentityRejectsForeignSealedClaim(t *testing.T) {
	j := smallReviewDispositionJourney(t, "small-native-claim-forgery")
	ctx := context.Background()
	proof, err := PrepareWorkerDispatchFreshness(ctx, j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := j.s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')='small-review' AND json_extract(payload,'$.action_id')='dispatch_worker'`, j.workID, WorkflowActionCompleted).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, j.s, j.workID)
	for field, value := range map[string]string{
		"worker_project_id": "foreign-project", "worker_claim_op_id": "foreign-claim", "worker_worktree_identity": "sha256:" + strings.Repeat("c", 64),
	} {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		fields["expected_version"], fields["resulting_version"] = version, version+1
		fields[field] = value
		event := workflowEventWithActor("native-forgery-"+field, WorkflowActionCompleted, j.workID, footprintBindingActorRef(t, j.workID), fields)
		event.PayloadVersion = workflowActionCompletedDispatchIdentityVersion
		err := applyWorkflowTestOperation(WithWorkerDispatchPreparation(ctx, proof), j.s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, j.workID): version}})
		if err == nil || !strings.Contains(err.Error(), "sealed native preparation") {
			t.Errorf("forged %s was not refused by its native claim owner: %v", field, err)
		}
		if got := readWorkVersion(t, j.s, j.workID); got != version {
			t.Fatalf("forged claim changed version %d to %d", version, got)
		}
	}
}

// Query-only fixture: an invalid old record cannot acquire new provenance
// merely because a later decoder recognizes the member names.
func TestNativeWindowIgnoresPreBoundaryClaimIdentity(t *testing.T) {
	t.Parallel()
	db, appendEvent := correctionHistoryFixture(t)
	if _, err := db.Exec(`ALTER TABLE domain_events ADD COLUMN payload_version INTEGER NOT NULL DEFAULT 5`); err != nil {
		t.Fatal(err)
	}
	appendEvent(WorkflowActionStarted, map[string]any{"action_id": "dispatch_worker", "step_id": "execution", "attempt_epoch": 1})
	appendEvent(WorkflowActionCompleted, map[string]any{
		"action_id": "dispatch_worker", "step_id": "execution", "attempt_epoch": 1, "worker_attempt_id": "old-attempt",
		"worker_project_id": "forged-project", "worker_claim_op_id": "forged-claim",
	})
	window, err := findAuthorizedDispatchWindowBefore(context.Background(), db, "work-history", "old-attempt", 0)
	if err != nil || window.ProjectID != "" || window.ClaimOpID != "" {
		t.Fatalf("a pre-boundary authorization supplied native claim provenance: %+v, %v", window, err)
	}
}

func TestNativeOracleResultRetainsItsOriginalClaim(t *testing.T) {
	t.Parallel()
	identity := "sha256:" + strings.Repeat("d", 64)
	result := nativeOracleInitialResult(
		WorktreeVerifyRequest{WorkID: "work", ProjectID: "project", Oracle: &NativeOracleRequest{Phase: "prepare"}},
		WorktreeEntry{ProjectID: "project", ClaimOpID: "claim-original"},
		nativeOraclePlan{WorktreeIdentity: identity},
	)
	if result.ProjectID != "project" || result.ClaimOpID != "claim-original" || result.WorktreeIdentity != identity {
		t.Fatalf("native oracle result lost original claim provenance: %+v", result)
	}
}
