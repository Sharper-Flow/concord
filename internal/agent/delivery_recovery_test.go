package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// A changed criterion invalidates the accepted Review without replacing its
// candidate. The authenticated coordinator returns the parked gate to its
// producer through the evidence-bearing correction route.

func TestDeliveryGateRecoveryReturnsStaleReviewThroughAgent(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	ctx := context.Background()
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	worktree := t.TempDir()
	grant.Worktree = worktree

	currentDefinition, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: currentDefinition, Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?)`, store.WorktreeSetID("work-1"), "project-1", "claim:gate-recovery", "gate-recovery", strings.Repeat("a", 40), worktree, "repo:gate-recovery", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
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
	var producerDelivery string
	if err := s.DatabaseForTesting().QueryRow(`SELECT assertion_id FROM workflow_delivery_state WHERE work_id='work-1'`).Scan(&producerDelivery); err != nil {
		t.Fatal(err)
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
	if err != nil || open.Review == nil || len(open.Review.Findings) != 1 || open.Review.Findings[0].Status != "open" || !open.Review.Findings[0].Blocking {
		t.Fatalf("Review lost its blocking finding: %+v (%v)", open.Review, err)
	}
	reportRef := open.Review.Accepted.ReportRef
	spineOK(t, oAgentAction(t, fixture, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": reportRef}))
	spineOK(t, oAgentAction(t, fixture, "request_correction", map[string]any{
		"diagnosis": "the review rejects the refined result", "strategy": "implement the review findings and resubmit",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{reportRef},
	}))
	changeCandidate("repair")
	retained, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || retained.Review == nil || len(retained.Review.Findings) != 1 || retained.Review.Findings[0].FindingID != open.Review.Findings[0].FindingID || retained.Review.Findings[0].Status != "open" {
		t.Fatalf("candidate correction discarded the open finding: %+v (%v)", retained.Review, err)
	}

	// Main's repair acceptance changed the integrated candidate. The earlier
	// continue Review cannot authorize delivery of this different candidate.
	deliveryRefusal := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:gate-recovery", "delivery_state": "asserted"}, "idempotency_key": "gate-recovery-delivery"})
	if deliveryRefusal.Outcome != agentcontract.OutcomeError || deliveryRefusal.Error == nil || deliveryRefusal.Error.Kind != "stale_requires_review" || deliveryRefusal.Error.EffectState != "none" || deliveryRefusal.Error.Message != "recorded Review does not cover the current candidate and dependencies" {
		t.Fatalf("repair delivery exit = %+v, want a zero-effect current-candidate Review refusal", deliveryRefusal.Error)
	}
	recordFinding("closed", "ready_for_final_proof")
	reviewed, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || reviewed.Review == nil || reviewed.Review.Accepted == nil || reviewed.Review.Candidate == nil || reviewed.Review.Dependencies == nil || !reviewed.Review.DependenciesCurrent || reviewed.Review.Accepted.Report.Disposition != "ready_for_final_proof" || reviewed.Review.Accepted.Report.OutputCandidate != *reviewed.Review.Candidate {
		t.Fatalf("ready Review does not cover the current candidate: %+v (%v)", reviewed.Review, err)
	}

	// Seed the contract-correction checkpoint for the successor's store-owned
	// setup, then park its unchanged candidate at the delivery gate below.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1';
		DELETE FROM write_guard`); err != nil {
		t.Fatalf("seed contract-correction checkpoint: %v", err)
	}
	spineOK(t, oAgentAction(t, fixture, "supersede_contract", map[string]any{
		"contract_version": 2, "premise": "approved gate return objective",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:gate-recovery", "immutable_subject_ref": "commit:gate-recovery", "expected_result": "fail"}}},
		"required_evidence":  []string{"review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal", "decision_domains": []string{},
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, s),
		"supersede_reason":     "the expected criterion result changed", "audit_evidence": []string{"evidence:gate-review"},
	}))
	stale, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || stale.Review == nil || stale.Review.Accepted == nil || stale.Review.Candidate == nil || stale.Review.Dependencies == nil || stale.Review.DependenciesCurrent || *stale.Review.Candidate != *reviewed.Review.Candidate || stale.Review.Accepted.ReportRef != reviewed.Review.Accepted.ReportRef {
		t.Fatalf("successor did not stale the standing Review on the unchanged candidate: %+v (%v)", stale.Review, err)
	}
	if len(reviewed.Review.Dependencies.Criteria) != 1 || len(stale.Review.Dependencies.Criteria) != 1 || stale.Review.Dependencies.Criteria[0].PredicateID != "predicate:primary" || stale.Review.Dependencies.Criteria[0].ContentDigest == reviewed.Review.Dependencies.Criteria[0].ContentDigest {
		t.Fatal("successor did not change the depended-on criterion digest")
	}

	// Park the unchanged candidate with its now-inapplicable accepted Review.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='delivery' WHERE work_id='work-1';
		DELETE FROM write_guard`); err != nil {
		t.Fatalf("seed the parked gate position: %v", err)
	}

	// Correction binds the current criterion and durable evidence. Routine
	// recovery needs the authenticated coordinator.
	correctionInput := map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "request_correction", "idempotency_key": "gate-recovery-return", "fields": map[string]any{
		"diagnosis": "the accepted Review judged the previous criterion revision", "strategy": "return to the execution step and reassess the candidate against the current criterion",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:gate-review"},
	}}
	returned := invoke(correctionInput)
	if returned.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("authenticated evidence-bearing gate return = %+v", returned.Error)
	}
	if got := stepOf(); got != "execution" {
		t.Fatalf("step after gate return = %q, want execution", got)
	}

	// The delivery state keeps one row per item and each later assertion
	// replaces it, so the producer's assertion must still be current: neither
	// the refused delivery nor the parked gate's correction asserted delivery.
	var currentDelivery string
	if err := s.DatabaseForTesting().QueryRow(`SELECT assertion_id FROM workflow_delivery_state WHERE work_id='work-1'`).Scan(&currentDelivery); err != nil {
		t.Fatal(err)
	}
	if currentDelivery != producerDelivery {
		t.Fatalf("gate recovery replaced producer delivery %q with %q", producerDelivery, currentDelivery)
	}
	var definitionDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest FROM workflow_instances WHERE work_id='work-1'`).Scan(&definitionDigest); err != nil {
		t.Fatal(err)
	}
	if definitionDigest != currentDefinition.Digest {
		t.Fatalf("definition after recovery = %s, want the current definition %s", definitionDigest, currentDefinition.Digest)
	}
}

func agentWorkerDirectory(t *testing.T, s *store.Store, workID string) string {
	t.Helper()
	var directory string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, store.WorktreeSetID(workID)).Scan(&directory); err != nil {
		t.Fatal(err)
	}
	return directory
}
