package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecordedInvestigationArtifactRequiresResolvableDomainAndWorkRefs(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "investigation-gate-work"
	otherWorkID := "investigation-gate-other"
	seedWork(t, s, workID)
	seedWork(t, s, otherWorkID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)

	// The committed registry decides whether a ref names a current Domain:
	// the proof is established per operation before the gate runs.
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("1", 16), []string{workID, "root"})
	if err := requireRecordedInvestigationArtifact(ctx, s.DatabaseForTesting(), workID); err == nil {
		t.Fatal("observation accepted the owning work item as the comparison ref")
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("2", 16), []string{"missing-domain", otherWorkID})
	if err := requireRecordedInvestigationArtifact(ctx, s.DatabaseForTesting(), workID); err == nil {
		t.Fatal("observation accepted an unknown Domain ref")
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("3", 16), []string{"root", otherWorkID})
	if err := requireRecordedInvestigationArtifact(ctx, s.DatabaseForTesting(), workID); err != nil {
		t.Fatalf("resolvable investigation artifact refused: %v", err)
	}
}

func TestPendingQuestionsRequireBoundResearchRevision(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "investigation-gate-research"
	actor, version := continuityTestWorkflow(t, s, workID)
	_, err := continuityAction(t, s, workID, version, "checkpoint_context", "investigation-gate-checkpoint", map[string]any{
		"active_unit":       "unit:implementation",
		"hypothesis":        "hypothesis:one",
		"diagnosis":         "diagnosis:one",
		"strategy":          "strategy:one",
		"touched_refs":      []string{"ref:file"},
		"evidence_refs":     []string{"evidence:one"},
		"pending_questions": []string{"question:technical"},
		"pending_decisions": []string{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireResearchForPendingQuestions(context.Background(), s.DatabaseForTesting(), workID); err == nil {
		t.Fatal("approval admitted recorded pending questions without research")
	} else {
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("pending-question refusal=%v, want missing evidence", err)
		} else if !strings.Contains(failure.RecoveryAction, "research_bindings") || !strings.Contains(failure.RecoveryAction, "approving action") {
			t.Fatalf("pending-question recovery=%q, want research_bindings on the approving action", failure.RecoveryAction)
		}
	}

	pack := createSimplePack(t, s, "investigation-gate-research", workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = BindResearchRelianceTx(context.Background(), tx, workID, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}}, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	leaveErr := leaveWrite(context.Background(), tx)
	if err == nil {
		err = leaveErr
	}
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := requireResearchForPendingQuestions(context.Background(), s.DatabaseForTesting(), workID); err != nil {
		t.Fatalf("bound research revision did not admit approval: %v", err)
	}
}

func TestApproveContractResearchBindingTakesEffectBeforeItsGate(t *testing.T) {
	s := openTemp(t)
	workID := "investigation-gate-same-action"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:investigation", ClientRef: "client:investigation", AgentRef: "agent:investigation", SessionRef: "session:investigation", ActorClass: ActorAgent}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := int64(4)
	for _, action := range []string{"record_proposal", "record_alignment"} {
		version = issue31WorkflowAction(t, s, workID, version, action, "same-action-"+action, actor)
	}
	version, err = continuityAction(t, s, workID, version, "checkpoint_context", "same-action-checkpoint", map[string]any{
		"active_unit":       "unit:implementation",
		"hypothesis":        "hypothesis:one",
		"diagnosis":         "diagnosis:one",
		"strategy":          "strategy:one",
		"touched_refs":      []string{"ref:file"},
		"evidence_refs":     []string{"evidence:one"},
		"pending_questions": []string{"question:technical"},
		"pending_decisions": []string{},
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	pack := createSimplePack(t, s, "same-action", workID)
	approval := json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`)
	if err := applyInvestigationGateApproval(t, s, workID, version, "same-action-refusal", actor, approval, nil); err == nil {
		t.Fatal("approve_contract admitted pending questions without research_bindings")
	} else {
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("pending-question refusal=%v, want missing evidence", err)
		}
		if failure.Detail != "contract approval requires a research pack revision for recorded pending questions" || !strings.Contains(failure.RecoveryAction, "add research_bindings to the approving action") {
			t.Fatalf("planning exit did not name its missing requirement and admitted recovery: %+v", failure)
		}
		if readWorkVersion(t, s, workID) != version || readInstanceStep(t, s, workID) != "planning" {
			t.Fatal("missing research advanced the planning exit")
		}
	}
	if err := applyInvestigationGateApproval(t, s, workID, version, "same-action-approval", actor, approval, []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}}); err != nil {
		t.Fatalf("approve_contract with same-action research_bindings refused: %v", err)
	}
	var consumers int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE consumer_work_id=? AND pack_id=? AND revision=?`, workID, pack.PackID, 1).Scan(&consumers); err != nil {
		t.Fatal(err)
	}
	if consumers != 1 {
		t.Fatalf("same-action research binding count=%d, want 1", consumers)
	}
	if readInstanceStep(t, s, workID) != "design" {
		t.Fatal("the refusal's same-action research recovery did not admit the planning exit")
	}
}

func applyInvestigationGateApproval(t *testing.T, s *Store, workID string, version int64, operationID string, actor WorkflowActor, payload json.RawMessage, bindings []ResearchBindingDeclaration) error {
	t.Helper()
	payload = testApprovalPayload(t, s, workID, "approve_contract", payload)
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		return err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:investigation-gate", IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ResearchBindings: bindings,
	})
	if leaveErr := leaveWrite(context.Background(), tx); actionErr == nil {
		actionErr = leaveErr
	}
	if actionErr != nil {
		_ = tx.Rollback()
		return actionErr
	}
	return tx.Commit()
}

func insertInvestigationGateObservation(t *testing.T, s *Store, workID, observationID string, refs []string) {
	t.Helper()
	encoded, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = tx.ExecContext(context.Background(), `INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,?)`, observationID, workID, "technical investigation", string(encoded), `[]`, time.Now().UTC().Format(time.RFC3339Nano))
	leaveErr := leaveWrite(context.Background(), tx)
	if err == nil {
		err = leaveErr
	}
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// CD-0173: the comparison-work half of the investigation gate is conditional.
// A Product that holds one work item in any lifecycle has no other work item
// to name, so the artifact needs only the current Domain ref. The core counts
// the Product's work items in the same transaction; no agent assertion
// decides it.
func TestInvestigationArtifactAdmitsSingleItemProductByDomainRef(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "investigation-gate-single"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)

	// The Product holds no second work item, so a current Domain ref alone
	// satisfies the artifact.
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("a", 16), []string{"root"})
	if err := requireRecordedInvestigationArtifact(ctx, s.DatabaseForTesting(), workID); err != nil {
		t.Fatalf("single-item Product refused a domain-only investigation artifact: %v", err)
	}
}

// The Domain half stays mandatory in a single-item Product: a fresh store
// holds only an observation whose Domain ref does not resolve.
func TestInvestigationArtifactSingleItemProductStillRequiresDomainRef(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "investigation-gate-single-badref"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("b", 16), []string{"missing-domain"})
	requireFailureKind(t, requireRecordedInvestigationArtifact(ctx, s.DatabaseForTesting(), workID), KindMissingEvidence, "an artifact without a current Domain ref")
}

// Without a proof that binds the Product's committed registry, the advisory
// fold cannot decide the artifact's Domain refs. It must say so with a typed
// stale-review answer rather than report a valid artifact as missing, and a
// registry change after the verification refuses the same way.
func TestInvestigationArtifactDomainRefsUnverifiedWithoutCurrentProof(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	workID := "investigation-gate-unverified"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("c", 16), []string{"root"})
	requireFailureKind(t, requireRecordedInvestigationArtifact(context.Background(), s.DatabaseForTesting(), workID), KindStaleRequiresReview, "an artifact read without a registry proof")

	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireRecordedInvestigationArtifact(ctx, s.DatabaseForTesting(), workID); err != nil {
		t.Fatalf("a verified current Domain ref was refused: %v", err)
	}
	var repo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT normalized_value FROM project_locators WHERE project_id='project' AND locator_id='workflow-law-locator'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeRegistryPath, strings.Replace(fixtureLawFolderRegistry, `"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"current"`, `"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"deprecated"`, 1))
	commitKnowledgeRepo(t, repo, "deprecate the root child Domain")
	requireFailureKind(t, requireRecordedInvestigationArtifact(ctx, s.DatabaseForTesting(), workID), KindStaleRequiresReview, "an artifact read against a moved registry")
}
