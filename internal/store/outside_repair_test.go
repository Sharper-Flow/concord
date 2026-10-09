package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedOutsideRepairTestWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	event := workflowEvent(workID+"-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "ref": registered.Definition.Ref, "version": registered.Definition.Version, "digest": registered.Digest, "work_kind": string(registered.Definition.WorkKind)})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, 2)}); err != nil {
		t.Fatal(err)
	}
}

// These receipts model a boundary that fetched the merge, complete successful
// required-check set and published tag, then proved merge ancestry. No live
// database or forge is involved in store tests.
func outsideRepairSampleEvidence() OutsideRepairEvidence {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	e := OutsideRepairEvidence{AuthorityRef: "client/github", ObservedAt: at.Add(2 * time.Hour), Repository: "octo-org/concord", ReleaseTag: "v1.2.3", ReleaseURL: "https://github.com/octo-org/concord/releases/tag/v1.2.3", ReleaseSHA: strings.Repeat("1", 40), PublishedAt: at.Add(time.Hour)}
	for i := int64(1); i <= 2; i++ {
		head := strings.Repeat(jsonInt(i+1), 40)
		e.PullRequests = append(e.PullRequests, OutsideRepairPullRequestEvidence{URL: "https://github.com/octo-org/concord/pull/" + jsonInt(i), Number: i, HeadSHA: head, MergeSHA: strings.Repeat(jsonInt(i+3), 40), MergedAt: at,
			RequiredChecks: []OutsideRepairRequiredCheck{{Name: "required-ci", URL: "https://github.com/octo-org/concord/actions/runs/" + jsonInt(i) + "/job/1", CommitSHA: head, Conclusion: "success", CheckRunID: 400 + i, RunID: i, JobID: 1}},
		})
	}
	return e
}

func outsideRepairTestRequest(t *testing.T, s *Store, workID, operation string) OutsideRepairRequest {
	t.Helper()
	version := deliveryWorkVersion(t, s, workID)
	eventID := workID + ":" + operation + ":" + jsonInt(version)
	hash := sha256.Sum256([]byte(eventID))
	ref := hex.EncodeToString(hash[:])
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insertConsumedApproval(t, s, ref, workID, version)
	return OutsideRepairRequest{OutsideRepairApproval: OutsideRepairApproval{ApprovalRef: ref, ApprovalOperationDigest: deliveryCorrectionDigest, ApprovalScopeJSON: deliveryCorrectionScopeJSON(workID), ApprovalVersionsJSON: deliveryCorrectionVersionsJSON(version), ApprovalConsequence: deliveryCorrectionConsequence}, WorkID: workID, Reason: "bounded outside defect repair", EventID: eventID, ExpectedVersion: version, OccurredAt: time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)}
}

func outsideRepairApply(t *testing.T, s *Store, req OutsideRepairRequest, kind string, evidence OutsideRepairEvidence) error {
	t.Helper()
	return s.TransactDurable(context.Background(), func(tx *Transaction) error {
		var err error
		switch kind {
		case WorkflowOutsideRepairDispositionSet:
			_, err = SetOutsideRepairDispositionTx(context.Background(), tx, req)
		case WorkflowOutsideRepairReconciled:
			_, err = ReconcileOutsideRepairTx(context.Background(), tx, OutsideRepairReconcileRequest{OutsideRepairRequest: req, Evidence: evidence})
		case WorkflowOutsideRepairResumed:
			_, err = ResumeOutsideRepairTx(context.Background(), tx, req)
		}
		return err
	})
}

func TestOutsideRepairRecoveryNamesOnlyDeclaredReconcileRoute(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-surface.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var surface struct {
		Operations []struct {
			ID string `json:"id"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &surface); err != nil {
		t.Fatal(err)
	}
	routes := outsideRepairRouteNames()
	for _, route := range routes {
		declared := false
		for _, operation := range surface.Operations {
			if operation.ID == "concord_work_transition."+route {
				declared = true
			}
		}
		if !declared {
			t.Errorf("outside-repair recovery advertises undeclared route %q", route)
		}
	}
	if !reflect.DeepEqual(routes, []string{"outside_repair_reconcile"}) {
		t.Fatalf("both completed and resume use the single reconcile route: %v", routes)
	}
	if failure := newOutsideRepairRouteFailure("test", "active hold"); !reflect.DeepEqual(failure.RecoveryRefs, routes) {
		t.Fatalf("recovery refs = %v, want %v", failure.RecoveryRefs, routes)
	}
}

func TestOutsideRepairHoldRequiresApprovalNotCompletionEvidence(t *testing.T) {
	s := openTemp(t)
	work := "outside-hold"
	seedOutsideRepairTestWork(t, s, work)
	req := outsideRepairTestRequest(t, s, work, "hold")
	bad := req
	bad.ApprovalRef = ""
	assertFailureKind(t, outsideRepairApply(t, s, bad, WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}), KindInvalidPayload)
	bad = req
	bad.ApprovalOperationDigest = "sha256:" + strings.Repeat("a", 64)
	assertFailureKind(t, outsideRepairApply(t, s, bad, WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}), KindUnauthorized)
	if got := deliveryWorkVersion(t, s, work); got != 3 {
		t.Fatalf("refused hold changed version: %d", got)
	}
	if err := outsideRepairApply(t, s, req, WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	if got := deliveryWorkVersion(t, s, work); got != 5 {
		t.Fatalf("hold version = %d", got)
	}
	pin, err := ReadWorkPin(context.Background(), s, work)
	if err != nil {
		t.Fatal(err)
	}
	if pin.OutsideRepairDisposition == nil || pin.OutsideRepairDisposition.State != OutsideRepairStateActive || pin.OutsideRepairDisposition.Evidence != nil || len(pin.NextValidIntents) != 0 || !reflect.DeepEqual(pin.OutsideRepairRoute, []string{"outside_repair_reconcile"}) {
		t.Fatalf("held pin = %+v", pin)
	}
	continuity, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: work, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if continuity.OutsideRepairDisposition == nil || len(continuity.StepActions) != 0 || !reflect.DeepEqual(continuity.OutsideRepairRoute, pin.OutsideRepairRoute) {
		t.Fatalf("held continuity = %+v", continuity)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	after, err := ReadWorkPin(context.Background(), s, work)
	if err != nil || !reflect.DeepEqual(pin, after) {
		t.Fatalf("hold replay drift: before=%+v after=%+v err=%v", pin, after, err)
	}
}

func TestOutsideRepairCentralAdmissionSuppressesAllActionsAndLifecycleBypasses(t *testing.T) {
	s := openTemp(t)
	work := "outside-central"
	seedOutsideRepairTestWork(t, s, work)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	definition, _ := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	state, _, _, err := loadWorkflowAdmissionStateTx(context.Background(), tx, work, definition.Definition, "proposal", "test")
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range definition.Definition.ActionDefinitions {
		decision := workflowAdmit(definition.Definition, state, action.ID)
		if decision.Admitted || decision.ApprovalRequired || decision.Failure == nil || decision.Failure.Kind != KindOutsideRepairActive {
			t.Fatalf("admission %s = %+v", action.ID, decision)
		}
		assertFailureKind(t, InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{WorkID: work, ActionID: action.ID}), KindOutsideRepairActive)
		err := s.Transact(context.Background(), func(tx *Transaction) error {
			_, err := ApplyWorkflowActionTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: work, ActionID: action.ID, ExpectedVersion: 5})
			return err
		})
		assertFailureKind(t, err, KindOutsideRepairActive)
	}
	assertFailureKind(t, s.Transact(context.Background(), func(tx *Transaction) error {
		return RepinWorkflowTx(context.Background(), tx, WorkflowRepinRequest{WorkID: work, EventID: work + ":repin", Definition: definition, Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/test", SessionRef: "session/test", ActorClass: ActorAgent}})
	}), KindOutsideRepairActive)
	for _, target := range []string{"completed", "cancelled", "in_progress"} {
		err := applyWorkEvent(t, s, workTransitionEvent(work+":"+target, work, "needed", target, 5, 6), workVersion(work, 5))
		assertFailureKind(t, err, KindOutsideRepairActive)
	}
	if got := deliveryWorkVersion(t, s, work); got != 5 {
		t.Fatalf("refusal changed version: %d", got)
	}
}

func TestOutsideRepairReconcileBothLiveLifecyclesAndReplay(t *testing.T) {
	for _, lifecycle := range []string{"needed", "in_progress"} {
		t.Run(lifecycle, func(t *testing.T) {
			s := openTemp(t)
			work := "outside-reconcile-" + lifecycle
			seedOutsideRepairTestWork(t, s, work)
			if lifecycle == "in_progress" {
				if err := applyWorkEvent(t, s, workTransitionEvent(work+":start", work, "needed", "in_progress", 3, 4), workVersion(work, 3)); err != nil {
					t.Fatal(err)
				}
			}
			if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
				t.Fatal(err)
			}
			evidence := outsideRepairSampleEvidence()
			if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "reconcile"), WorkflowOutsideRepairReconciled, evidence); err != nil {
				t.Fatal(err)
			}
			pin, err := ReadWorkPin(context.Background(), s, work)
			if err != nil {
				t.Fatal(err)
			}
			if pin.Lifecycle != "completed" || pin.OutsideRepairDisposition.State != OutsideRepairStateCompleted || !reflect.DeepEqual(pin.OutsideRepairDisposition.Evidence, &evidence) || len(pin.NextValidIntents) != 0 {
				t.Fatalf("reconciled pin = %+v", pin)
			}
			var instance string
			if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, work).Scan(&instance); err != nil || instance != "outside_repair" {
				t.Fatalf("instance=%s err=%v", instance, err)
			}
			var fabricated int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind IN ('workflow.completed','workflow.verdict_recorded','workflow.premise_confirmed','workflow.evidence_bound','work.transitioned') AND event_id NOT LIKE '%:start'`, work).Scan(&fabricated); err != nil || fabricated != 0 {
				t.Fatalf("fabricated=%d err=%v", fabricated, err)
			}
			assertFailureKind(t, InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{WorkID: work, ActionID: "record_proposal"}), KindInvalidOperation)
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			after, err := ReadWorkPin(context.Background(), s, work)
			if err != nil || !reflect.DeepEqual(pin, after) {
				t.Fatalf("reconcile replay drift: before=%+v after=%+v err=%v", pin, after, err)
			}
		})
	}
}

// TestOutsideRepairReceiptNativeIdentitiesSurvivePersistenceAndReplay pins
// CD-0210 D2's receipt obligation on the durable side: the exact native
// check-run, run and job identities the boundary authenticated must persist
// in both durable evidence projections and survive a full log rebuild, so a
// later rerun cannot rewrite what the recorded receipt proves.
func TestOutsideRepairReceiptNativeIdentitiesSurvivePersistenceAndReplay(t *testing.T) {
	s := openTemp(t)
	work := "outside-identity"
	seedOutsideRepairTestWork(t, s, work)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	evidence := outsideRepairSampleEvidence()
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "reconcile"), WorkflowOutsideRepairReconciled, evidence); err != nil {
		t.Fatal(err)
	}
	assertNativeIdentities := func(t *testing.T, raw string) {
		t.Helper()
		var persisted OutsideRepairEvidence
		if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
			t.Fatal(err)
		}
		for index, pr := range persisted.PullRequests {
			for checkIndex, check := range pr.RequiredChecks {
				if check.CheckRunID <= 0 || check.RunID <= 0 || check.JobID <= 0 {
					t.Fatalf("pr %d check %d lost native identity: %+v", index, checkIndex, check)
				}
			}
		}
		if !reflect.DeepEqual(persisted.PullRequests[0].RequiredChecks, evidence.PullRequests[0].RequiredChecks) {
			t.Fatalf("persisted receipt identities drifted: %+v", persisted.PullRequests[0].RequiredChecks)
		}
	}
	var dispositionRaw, reconciliationRaw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT evidence_json FROM outside_repair_dispositions WHERE work_id=?`, work).Scan(&dispositionRaw); err != nil {
		t.Fatal(err)
	}
	assertNativeIdentities(t, dispositionRaw)
	if err := s.DatabaseForTesting().QueryRow(`SELECT evidence_json FROM outside_repair_reconciliations WHERE work_id=?`, work).Scan(&reconciliationRaw); err != nil {
		t.Fatal(err)
	}
	assertNativeIdentities(t, reconciliationRaw)
	pin, err := ReadWorkPin(context.Background(), s, work)
	if err != nil {
		t.Fatal(err)
	}
	if pin.OutsideRepairDisposition == nil || pin.OutsideRepairDisposition.Evidence == nil || !reflect.DeepEqual(pin.OutsideRepairDisposition.Evidence.PullRequests[0].RequiredChecks, evidence.PullRequests[0].RequiredChecks) {
		t.Fatalf("pin receipt lost native identities: %+v", pin.OutsideRepairDisposition)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	after, err := ReadWorkPin(context.Background(), s, work)
	if err != nil || !reflect.DeepEqual(pin, after) {
		t.Fatalf("identity replay drift: before=%+v after=%+v err=%v", pin, after, err)
	}
}

func TestOutsideRepairResumePreservesStateAndAllowsAnotherHold(t *testing.T) {
	s := openTemp(t)
	work := "outside-resume"
	seedOutsideRepairTestWork(t, s, work)
	before, err := ReadWorkPin(context.Background(), s, work)
	if err != nil {
		t.Fatal(err)
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "resume"), WorkflowOutsideRepairResumed, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	if err := InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{WorkID: work, ActionID: "record_proposal", Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/test", SessionRef: "session/test", ActorClass: ActorAgent}, Payload: json.RawMessage(`{"problem":"repair the defect","affected":["service"],"stakes":"safe coordination","constraints":[],"user_outcomes":["typed repair"],"open_questions":[],"out_of_scope":[]}`)}); err != nil {
		t.Fatalf("resume did not restore admission: %v", err)
	}
	after, err := ReadWorkPin(context.Background(), s, work)
	if err != nil {
		t.Fatal(err)
	}
	if after.Lifecycle != before.Lifecycle || after.Step != before.Step || len(after.NextValidIntents) != len(before.NextValidIntents) || len(after.OutsideRepairRoute) != 0 || after.OutsideRepairDisposition.State != OutsideRepairStateResumed {
		t.Fatalf("resume state drift: before=%+v after=%+v", before, after)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	replayed, err := ReadWorkPin(context.Background(), s, work)
	if err != nil || !reflect.DeepEqual(after, replayed) {
		t.Fatalf("resume replay drift: %+v %v", replayed, err)
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold-again"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
}

func TestOutsideRepairRefusesInactiveDuplicateTerminalAndStaleMutations(t *testing.T) {
	s := openTemp(t)
	work := "outside-refusals"
	seedOutsideRepairTestWork(t, s, work)
	assertFailureKind(t, outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "inactive"), WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()), KindInvalidOperation)
	assertFailureKind(t, outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "inactive-resume"), WorkflowOutsideRepairResumed, OutsideRepairEvidence{}), KindInvalidOperation)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "duplicate"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}), KindOutsideRepairActive)
	req := outsideRepairTestRequest(t, s, work, "stale")
	req.ExpectedVersion += 5
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()), KindVersionConflict)
	if got := deliveryWorkVersion(t, s, work); got != 5 {
		t.Fatalf("failed mutations left effects: %d", got)
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "reconcile"), WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "terminal"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}), KindIllegalLifecycleTransition)
}

func TestOutsideRepairReconciliationEvidenceRefusesInvalidReceipts(t *testing.T) {
	cases := map[string]func(*OutsideRepairEvidence){
		"no prs":             func(e *OutsideRepairEvidence) { e.PullRequests = nil },
		"duplicate pr":       func(e *OutsideRepairEvidence) { e.PullRequests = append(e.PullRequests, e.PullRequests[0]) },
		"no authority":       func(e *OutsideRepairEvidence) { e.AuthorityRef = "" },
		"no observation":     func(e *OutsideRepairEvidence) { e.ObservedAt = time.Time{} },
		"no release":         func(e *OutsideRepairEvidence) { e.ReleaseTag = "" },
		"digest not git sha": func(e *OutsideRepairEvidence) { e.ReleaseSHA = "sha256:" + strings.Repeat("1", 64) },
		"nonhex sha":         func(e *OutsideRepairEvidence) { e.ReleaseSHA = strings.Repeat("z", 40) },
		"foreign release": func(e *OutsideRepairEvidence) {
			e.ReleaseURL = "https://github.com/foreign/repository/releases/tag/v1.2.3"
		},
		"foreign pr":          func(e *OutsideRepairEvidence) { e.PullRequests[0].URL = "https://github.com/foreign/repository/pull/1" },
		"unmerged":            func(e *OutsideRepairEvidence) { e.PullRequests[0].MergedAt = time.Time{} },
		"unpublished":         func(e *OutsideRepairEvidence) { e.PublishedAt = time.Time{} },
		"merge after release": func(e *OutsideRepairEvidence) { e.PullRequests[0].MergedAt = e.PublishedAt.Add(time.Hour) },
		"failed check":        func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks[0].Conclusion = "failure" },
		"stale check":         func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks[0].CommitSHA = e.ReleaseSHA },
		"no checks":           func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks = nil },
		"duplicate check": func(e *OutsideRepairEvidence) {
			e.PullRequests[0].RequiredChecks = append(e.PullRequests[0].RequiredChecks, e.PullRequests[0].RequiredChecks[0])
		},
		"check without check-run identity": func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks[0].CheckRunID = 0 },
		"check without run identity":       func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks[0].RunID = 0 },
		"check without job identity":       func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks[0].JobID = 0 },
		"run identity disagrees with url":  func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks[0].RunID = 991 },
		"job identity disagrees with url":  func(e *OutsideRepairEvidence) { e.PullRequests[0].RequiredChecks[0].JobID = 997 },
		"one check-run identity proves two checks": func(e *OutsideRepairEvidence) {
			e.PullRequests[0].RequiredChecks = append(e.PullRequests[0].RequiredChecks, e.PullRequests[0].RequiredChecks[0])
			e.PullRequests[0].RequiredChecks[1].Name = "other-required"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			work := "outside-invalid-receipt"
			seedOutsideRepairTestWork(t, s, work)
			if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
				t.Fatal(err)
			}
			e := outsideRepairSampleEvidence()
			mutate(&e)
			assertFailureKind(t, outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "reconcile"), WorkflowOutsideRepairReconciled, e), KindInvalidPayload)
			if got := deliveryWorkVersion(t, s, work); got != 5 {
				t.Fatalf("invalid receipt changed version: %d", got)
			}
		})
	}
}

func TestOutsideRepairGenericAppendCannotAcquireAuthority(t *testing.T) {
	s := openTemp(t)
	work := "outside-generic"
	seedOutsideRepairTestWork(t, s, work)
	for _, kind := range []string{WorkflowOutsideRepairDispositionSet, WorkflowOutsideRepairReconciled, WorkflowOutsideRepairResumed} {
		e := workflowTypedEvent(work+":"+kind, kind, work, "actor:forged", time.Now(), 3, map[string]any{"reason": "forged", "state": "active", "approval_ref": "approval:forged"})
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{e}, ExpectedVersions: workVersion(work, 3)})
		if err == nil {
			t.Fatalf("generic append accepted %s", kind)
		}
	}
}

func TestOutsideRepairFoldRejectsFabricatedApprovalAndEvidence(t *testing.T) {
	s := openTemp(t)
	work := "outside-fold"
	seedOutsideRepairTestWork(t, s, work)
	e := workflowTypedEvent("outside-forged", WorkflowOutsideRepairDispositionSet, work, "actor:forged", time.Now(), 3, map[string]any{"reason": "forged", "state": "active", "approval_ref": "approval:forged"})
	assertFailureKind(t, applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{e}, ExpectedVersions: workVersion(work, 3)}), KindInvalidPayload)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	// Construct an otherwise legitimate recorded binding but corrupt evidence
	// after assembly. Fold validation must not rely solely on API validation.
	req := outsideRepairTestRequest(t, s, work, "reconcile")
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		operator, err := workflowOperatorFromConsumedApprovalTx(context.Background(), tx.tx, req.binding(), "outside_repair")
		if err != nil {
			return err
		}
		actor := workflowTypedEvent(req.EventID+":actor", WorkflowActorRecorded, work, operator.ref, req.OccurredAt, 5, map[string]any{"actor_ref": operator.ref, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
		payload, _ := json.Marshal(workflowOutsideRepairReconcilePayload{workflowOutsideRepairDispositionPayload: workflowOutsideRepairDispositionPayload{OutsideRepairApproval: req.OutsideRepairApproval, Reason: req.Reason, State: OutsideRepairStateCompleted}, Evidence: OutsideRepairEvidence{}, EvidenceSource: OutsideRepairEvidenceSource})
		var fields map[string]any
		_ = json.Unmarshal(payload, &fields)
		invalid := workflowTypedEvent(req.EventID, WorkflowOutsideRepairReconciled, work, operator.ref, req.OccurredAt, 6, fields)
		_, err = applyWorkflowOperationTx(context.Background(), tx.tx, Operation{Events: []Event{actor, invalid}, ExpectedVersions: workVersion(work, 5)}, newFoldScope(tx.tx))
		return err
	})
	assertFailureKind(t, err, KindInvalidPayload)
	if got := deliveryWorkVersion(t, s, work); got != 5 {
		t.Fatalf("fold refusal left effects: %d", got)
	}
}

func TestOutsideRepairDoesNotChangeOrdinaryCompletion(t *testing.T) {
	s, completion := seedCompletionGateCase(t, "outside-ordinary", completionGateCase{requiredEvidence: []string{"verification"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowCompletedRefusesWhileOutsideRepairActive(t *testing.T) {
	s, completion := seedCompletionGateCase(t, "outside-completion", completionGateCase{requiredEvidence: []string{"verification"}, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	// The completion fixture leaves execution started. Close that action before
	// the hold so this case tests completion admission, not execution liveness.
	executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/outside-completion")
	version := readWorkVersion(t, s, "outside-completion")
	settled := workflowActionCompletedFixture("outside-completion-execution-settled", "outside-completion", executor, version, "execution", "record_delivery")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{settled}, ExpectedVersions: workVersion("outside-completion", version)}); err != nil {
		t.Fatal(err)
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, "outside-completion", "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, CompleteWorkflow(context.Background(), s, completion), KindOutsideRepairActive)
}

func TestOutsideRepairMigrationPreservesPopulatedWorkflowInstance(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "migration.db")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	index := -1
	for i, migration := range migrations {
		if migration.Name == "outside_repair_disposition_tables" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("outside-repair migration is missing")
	}
	for _, migration := range migrations[:index] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json,narrative,urgency)
VALUES('migration-work','task','Repair','in_progress',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','{}','','standard');
INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_model,started_at,execution_started_at)
VALUES('migration-work','workflow.implementation',1,'sha256:0000000000000000000000000000000000000000000000000000000000000000','execution','running','test/model','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
DELETE FROM fold_guard;`); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, migrations[index]); err != nil {
		t.Fatal(err)
	}
	var model, step, state, started string
	if err := db.QueryRowContext(ctx, `SELECT execution_model,current_step,instance_state,execution_started_at FROM workflow_instances WHERE work_id='migration-work'`).Scan(&model, &step, &state, &started); err != nil {
		t.Fatal(err)
	}
	if model != "test/model" || step != "execution" || state != "running" || started != "2026-01-01T00:00:00Z" {
		t.Fatalf("migration lost instance data: %s %s %s %s", model, step, state, started)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_instances SET execution_model='' WHERE work_id='migration-work'`); err == nil {
		t.Fatal("migration lost fold guard")
	}
}

// dropMigration121Objects removes every schema object migration 121 creates,
// so a store whose manifest tail was removed can re-apply the step cleanly.
// The workflow_instances rebuild is inverted with the pre-step table shape,
// data included: the re-apply must find the table as migration 121 left it.
func dropMigration121Objects(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS outside_repair_reconciliations`,
		`DROP TABLE IF EXISTS outside_repair_dispositions`,
		`CREATE TABLE workflow_instances_v121_backup AS
SELECT work_id, definition_ref, definition_version, definition_digest, current_step, instance_state,
       execution_actor_ref, execution_model, started_at, completed_at, last_checkpoint_at, execution_started_at
  FROM workflow_instances`,
		`DROP TRIGGER IF EXISTS workflow_instances_guard_insert`,
		`DROP TRIGGER IF EXISTS workflow_instances_guard_update`,
		`DROP TRIGGER IF EXISTS workflow_instances_guard_delete`,
		`DROP TABLE workflow_instances`,
		`CREATE TABLE workflow_instances (
    work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE RESTRICT,
    definition_ref TEXT NOT NULL,
    definition_version INTEGER NOT NULL,
    definition_digest TEXT NOT NULL,
    current_step TEXT NOT NULL,
    instance_state TEXT NOT NULL CHECK(instance_state IN ('planned','ready','running','blocked','awaiting_condition','verifying','completed','cancelled','superseded')),
    execution_actor_ref TEXT REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    execution_model TEXT NOT NULL DEFAULT '' CHECK(length(execution_model) <= 128),
    started_at TEXT,
    completed_at TEXT,
    last_checkpoint_at TEXT,
    execution_started_at TEXT,
    CHECK(definition_version > 0 AND definition_version <= 2147483647),
    CHECK(length(definition_ref) BETWEEN 2 AND 128),
    CHECK(length(definition_digest) = 71 AND substr(definition_digest,1,7) = 'sha256:'),
    CHECK(length(current_step) BETWEEN 2 AND 128)
)`,
		`INSERT INTO workflow_instances
    (work_id, definition_ref, definition_version, definition_digest, current_step, instance_state,
     execution_actor_ref, execution_model, started_at, completed_at, last_checkpoint_at, execution_started_at)
SELECT work_id, definition_ref, definition_version, definition_digest, current_step, instance_state,
       execution_actor_ref, execution_model, started_at, completed_at, last_checkpoint_at, execution_started_at
  FROM workflow_instances_v121_backup`,
		`DROP TABLE workflow_instances_v121_backup`,
		`CREATE INDEX workflow_instances_state ON workflow_instances(instance_state, work_id)`,
		`CREATE TRIGGER workflow_instances_guard_insert BEFORE INSERT ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END`,
		`CREATE TRIGGER workflow_instances_guard_update BEFORE UPDATE ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END`,
		`CREATE TRIGGER workflow_instances_guard_delete BEFORE DELETE ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
