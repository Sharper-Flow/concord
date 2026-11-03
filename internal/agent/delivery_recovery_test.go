package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// A changed criterion invalidates the accepted Review without replacing its candidate.

func TestDeliveryGateRetainsStaleReviewThroughAgent(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	ctx := context.Background()
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	worktree := t.TempDir()
	grant.Worktree = worktree

	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	fixture := oAgentFixture(t, s, service, env, "work-1")
	env = fixture.env
	invoke := func(input map[string]any) agentcontract.Envelope {
		t.Helper()
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	stepOf := func() string {
		t.Helper()
		var step string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
			t.Fatal(err)
		}
		return step
	}
	version := func() int64 { return workVersion(t, s, "work-1") }
	changeCandidate := func(label string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(fixture.path, "o_fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.path, "o_fixture/value.go"), []byte("package fixture\n// "+label+"\nfunc Value() int { return 3 + 4 }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, fixture.path, "add", "o_fixture/value.go")
		gitRun(t, fixture.path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", label)
		oAgentReport(t, fixture)
	}
	recordFinding := func(status, disposition string) {
		t.Helper()
		proof := oAgentReport(t, fixture)
		pin, err := store.ReadWorkPin(ctx, s, "work-1")
		if err != nil || pin.Review == nil || pin.Review.Dependencies == nil {
			t.Fatalf("gate Review dependencies: %v", err)
		}
		candidate := store.ReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, fixture.path, "rev-parse", "HEAD"), Tree: gitRun(t, fixture.path, "rev-parse", "HEAD^{tree}")}
		finding := store.ReviewFinding{FindingID: "finding:gate-result", Status: status, Severity: "P0", Confidence: "high", Detail: "the refined result misses the approved contract", InScope: true, Blocking: true, EvidenceRefs: []string{proof}, ClosureRefs: []string{}}
		if status == "closed" {
			finding.ClosureRefs = []string{proof}
		}
		report := store.ReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: *pin.Review.Dependencies,
			Assessments: []store.ReviewAssessment{}, RemainingObligations: []store.ReviewObligation{}, Findings: []store.ReviewFinding{finding}}
		spineOK(t, oAgentAction(t, fixture, "record_review", map[string]any{"review": report}))
	}

	advanceCapturedWorkToContract(t, s, service, env, "work-1", "gate-recovery")
	contractFields := workflowContractFieldsFixture()
	contractFields["premise"] = "approved gate return objective"
	contractFields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:gate-recovery", "immutable_subject_ref": "commit:gate-recovery", "expected_result": "pass"}}}
	contractFields["required_evidence"] = []string{"review"}
	contractFields["rigor_class"] = "prototype_internal"
	contractFields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "approve_contract", "fields": contractFields, "idempotency_key": "gate-recovery-contract"}); response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("gate recovery approve_contract: %+v", response.Error)
	}

	// Delivery consumes ready Review before the mandatory refinement pass.
	recordApprovedWorkDesign(t, s, service, env, "work-1", "gate-recovery-design")
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "start_execution", "idempotency_key": "gate-execution-start"}); response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("gate recovery start_execution: %+v", response.Error)
	}
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "review", "immutable_subject_ref": "evidence:gate-review"}, "idempotency_key": "gate-evidence-bind"}); response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("gate recovery bind_evidence: %+v", response.Error)
	}
	changeCandidate("execution")
	integrationRef := oAgentReport(t, fixture)
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "verification", "evidence_ref": integrationRef}, "idempotency_key": "gate-execution-integration"}); response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("gate recovery integration bind: %+v", response.Error)
	}
	oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "ready_for_final_proof", false)
	if stepOf() != "execution" {
		t.Fatalf("ready Review moved execution to %q", stepOf())
	}
	spineOK(t, oAgentAction(t, fixture, "record_delivery", map[string]any{"delivery_artifact": "artifact:gate-recovery", "delivery_state": "asserted"}))
	if stepOf() != "refine" {
		t.Fatalf("step after execution delivery = %q, want refine", stepOf())
	}
	// The canonical head derives the refine start from the qualifying
	// checkpoint pair; the callable start_refine retired with it.
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "checkpoint_refine", "fields": map[string]any{}, "idempotency_key": "gate-refine-start"}); response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("gate recovery checkpoint_refine: %+v", response.Error)
	}

	// A rejected review, then an accepted implement repair that changes the
	// candidate the earlier Review covered.
	recordFinding("open", "correction_required")
	open, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || open.Review == nil || open.Review.Accepted == nil || open.Review.Candidate == nil || len(open.Review.Findings) != 1 || open.Review.Findings[0].Status != "open" || !open.Review.Findings[0].Blocking {
		t.Fatalf("Review lost its blocking finding: %+v (%v)", open.Review, err)
	}
	reportRef := open.Review.Accepted.ReportRef
	spineOK(t, oAgentAction(t, fixture, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": reportRef}))
	changeCandidate("repair")
	retained, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || retained.Review == nil || !reflect.DeepEqual(retained.Review.Accepted, open.Review.Accepted) || !reflect.DeepEqual(retained.Review.Findings, open.Review.Findings) {
		t.Fatalf("candidate correction discarded the open finding: %+v (%v)", retained.Review, err)
	}
	currentCandidate := store.ReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, fixture.path, "rev-parse", "HEAD"), Tree: gitRun(t, fixture.path, "rev-parse", "HEAD^{tree}")}
	if !reflect.DeepEqual(retained.Review.Candidate, open.Review.Candidate) || retained.Review.DependenciesCurrent || currentCandidate == *open.Review.Candidate || retained.Review.Accepted.Report.OutputCandidate == currentCandidate {
		t.Fatalf("repair did not stale the retained Review on the changed candidate: %+v", retained.Review)
	}

	// An inapplicable Review cannot authorize delivery of the changed candidate.
	beforeDelivery := agentCurrentEffects(t, s)
	deliveryRefusal := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:gate-recovery", "delivery_state": "asserted"}, "idempotency_key": "gate-recovery-delivery"})
	if deliveryRefusal.Outcome != agentcontract.OutcomeError || deliveryRefusal.Error == nil || deliveryRefusal.Error.Kind != "stale_requires_review" || deliveryRefusal.Error.EffectState != "none" || deliveryRefusal.Error.RecoveryAction.Kind != "reread_entities" {
		t.Fatalf("repair delivery exit = %+v, want a zero-effect refusal", deliveryRefusal.Error)
	}
	afterDelivery, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || !reflect.DeepEqual(afterDelivery, retained) || agentCurrentEffects(t, s) != beforeDelivery {
		t.Fatalf("refused delivery changed workflow authority, candidate, Review, findings or delivery state: %v", err)
	}
	recordFinding("closed", "ready_for_final_proof")
	reviewed, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || reviewed.Review == nil || reviewed.Review.Accepted == nil || reviewed.Review.Candidate == nil || reviewed.Review.Dependencies == nil || !reviewed.Review.DependenciesCurrent || reviewed.Review.Accepted.Report.Disposition != "ready_for_final_proof" || reviewed.Review.Accepted.Report.OutputCandidate != *reviewed.Review.Candidate {
		t.Fatalf("ready Review does not cover the current candidate: %+v (%v)", reviewed.Review, err)
	}
	if reviewed.Review.Accepted.ReportRef == reportRef || *reviewed.Review.Candidate != currentCandidate || len(reviewed.Review.Findings) != 1 || reviewed.Review.Findings[0].FindingID != open.Review.Findings[0].FindingID || reviewed.Review.Findings[0].Status != "closed" || len(reviewed.Review.Findings[0].ClosureRefs) == 0 {
		t.Fatalf("fresh Review did not close the retained finding on the repaired candidate: %+v", reviewed.Review)
	}

	revised, _ := dispatchContractRevisionResult(t, s, service, env, "work-1", version(), map[string]any{
		"contract_version": 2, "premise": "approved gate return objective",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:gate-recovery", "immutable_subject_ref": "commit:gate-recovery", "expected_result": "fail"}}},
		"required_evidence":  []string{"review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal", "decision_domains": []string{},
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, s),
	}, "gate-revise-criterion")
	spineOK(t, revised)
	stale, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || stale.Review == nil || stale.Review.Accepted == nil || stale.Review.Candidate == nil || stale.Review.Dependencies == nil || stale.Review.DependenciesCurrent || *stale.Review.Candidate != *reviewed.Review.Candidate || stale.Review.Accepted.ReportRef != reviewed.Review.Accepted.ReportRef {
		t.Fatalf("successor did not stale the standing Review on the unchanged candidate: %+v (%v)", stale.Review, err)
	}
	if len(reviewed.Review.Dependencies.Criteria) != 1 || len(stale.Review.Dependencies.Criteria) != 1 || stale.Review.Dependencies.Criteria[0].PredicateID != "predicate:primary" || stale.Review.Dependencies.Criteria[0].ContentDigest == reviewed.Review.Dependencies.Criteria[0].ContentDigest {
		t.Fatal("successor did not change the depended-on criterion digest")
	}
}
