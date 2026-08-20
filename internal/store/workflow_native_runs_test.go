package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// CD-0039 D1-D4: the event is an attributed report, the projection never
// separates status from reporter, and a reused run ID cannot change its
// subject or authority.

func nativeRunTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []Event{
		{EventID: "nr-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "nr-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Project"}`)},
		{EventID: "nr-link", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "nr-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-nr", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"kind":"task","title":"native run","priority":1,"expected_version":0,"resulting_version":1}`)},
		{EventID: "nr-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-nr", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-1"): 0, VersionRef(SubjectProject, "project-1"): 0, VersionRef(SubjectWorkItem, "work-nr"): 0}}); err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		registered, err := BuiltinWorkflowRegistry().Register(builtinOpsRunbook())
		if err != nil {
			return err
		}
		return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: "work-nr", Definition: registered, Actor: actor, Now: time.Now().UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return s, actorRef
}

func nativeRunEvent(workID, actor string, version int64, values map[string]any) Event {
	return workflowTypedEvent("test:native-"+values["phase"].(string)+"-"+values["status"].(string), WorkflowNativeRunRecorded, workID, actor, time.Now().UTC(), version, values)
}

func nativeRunValues() map[string]any {
	return map[string]any{
		"run_id": "run-1", "native_subject_ref": "routing-provider:prod-edge",
		"subject_digest": "sha256:" + strings.Repeat("ab", 32),
		"capture_method": "provider_api", "observed_universe": "prod-edge routing table",
		"freshness_policy_ref": "policy:fresh-v1", "divergence_policy_ref": "policy:diverge-v1",
		"asserted_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// nativeRunValuesWith sets the reporting authority and actor to the derived
// workflow actor, as the dispatch path does: authority is never caller-chosen.
func nativeRunValuesWith(actorRef string) map[string]any {
	values := nativeRunValues()
	values["reporting_authority_ref"] = actorRef
	values["actor_ref"] = actorRef
	return values
}

// applyNativeRuns applies report folds inside an open fold window, as the
// authoritative workflow route does; the generic ApplyOperation path refuses
// workflow-authority events by design.
func applyNativeRuns(t *testing.T, s *Store, actorRef string, reports []map[string]any) error {
	t.Helper()
	var lastErr error
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		if err := enterFold(context.Background(), tx.tx); err != nil {
			return err
		}
		defer func() { _ = leaveFold(context.Background(), tx.tx) }()
		var version int64
		if err := tx.tx.QueryRowContext(context.Background(), `SELECT version FROM work_items WHERE id='work-nr'`).Scan(&version); err != nil {
			return err
		}
		for _, values := range reports {
			if err := foldWorkflowNativeRunRecorded(context.Background(), tx.tx, nativeRunEvent("work-nr", actorRef, version, values)); err != nil {
				lastErr = err
				return err
			}
			version++
		}
		return nil
	}); err != nil {
		return lastErr
	}
	return nil
}

func TestWorkflowNativeRunFoldAndAttribution(t *testing.T) {
	s, actorRef := nativeRunTestStore(t)

	values := nativeRunValuesWith(actorRef)
	values["phase"], values["status"] = "start", "started"
	health := nativeRunValuesWith(actorRef)
	health["phase"], health["status"] = "health", "failed"
	health["evidence_ref"], health["evidence_digest"] = "probe://run-1/health", "sha256:"+strings.Repeat("cd", 32)
	rollback := nativeRunValuesWith(actorRef)
	rollback["phase"], rollback["status"] = "rollback", "rolled_back"
	rollback["evidence_ref"], rollback["evidence_digest"] = "provider://run-1/rollback", "sha256:"+strings.Repeat("ef", 32)

	if err := applyNativeRuns(t, s, actorRef, []map[string]any{values, health, rollback}); err != nil {
		t.Fatalf("reports did not fold: %v", err)
	}

	var snapshot nativeRunSnapshot
	var found bool
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var readErr error
		snapshot, found, readErr = readNativeRun(context.Background(), tx.tx, "work-nr", "run-1")
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("projection row missing")
	}
	if snapshot.Phase != "rollback" || snapshot.Status != "rolled_back" {
		t.Fatalf("latest phase/status wrong: %+v", snapshot)
	}
	if snapshot.ReportingAuthorityRef == "" || snapshot.EvidenceRef == "" || snapshot.AssertedAt == "" || snapshot.RecordedAt == "" {
		t.Fatalf("status travels without its attribution: %+v", snapshot)
	}

	// D3: a reused run ID with a different subject fails structurally.
	reused := nativeRunValuesWith(actorRef)
	reused["phase"], reused["status"] = "cleanup", "cleaned"
	reused["native_subject_ref"] = "routing-provider:other"
	if err := applyNativeRuns(t, s, actorRef, []map[string]any{reused}); err == nil {
		t.Fatal("reused run ID with a different subject was accepted")
	}
}

func TestWorkflowNativeRunFoldRejectsUnknownStatusAndSkew(t *testing.T) {
	s, actorRef := nativeRunTestStore(t)

	unknown := nativeRunValuesWith(actorRef)
	unknown["phase"], unknown["status"] = "health", "exploded"
	unknown["evidence_ref"], unknown["evidence_digest"] = "probe://run-2", "sha256:"+strings.Repeat("cd", 32)
	if err := applyNativeRuns(t, s, actorRef, []map[string]any{unknown}); err == nil {
		t.Fatal("status outside the phase vocabulary was accepted")
	}

	skewed := nativeRunValuesWith(actorRef)
	skewed["phase"], skewed["status"] = "health", "failed"
	skewed["evidence_ref"], skewed["evidence_digest"] = "probe://run-2", "sha256:"+strings.Repeat("cd", 32)
	skewed["asserted_at"] = time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano)
	if err := applyNativeRuns(t, s, actorRef, []map[string]any{skewed}); err == nil {
		t.Fatal("asserted_at beyond the skew bound was accepted")
	}

	missingEvidence := nativeRunValuesWith(actorRef)
	missingEvidence["phase"], missingEvidence["status"] = "rollback", "rolled_back"
	if err := applyNativeRuns(t, s, actorRef, []map[string]any{missingEvidence}); err == nil {
		t.Fatal("rollback report without evidence was accepted")
	}
}
