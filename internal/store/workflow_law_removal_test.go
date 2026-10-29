package store

import (
	"testing"
	"time"
)

func removeLawChangeLaw(t *testing.T, s *Store) {
	t.Helper()
	const law = "law-remove"
	lawChangeFold(t, s, `DELETE FROM law_domain_applicability WHERE law_id=?`, law)
	lawChangeFold(t, s, `DELETE FROM law_domain_homes WHERE law_id=?`, law)
	lawChangeFold(t, s, `DELETE FROM law_subjects WHERE law_id=?`, law)
}

func TestLawRemovalThroughCurrentSuccessor(t *testing.T) {
	const id = "law-removal-successor"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	attributionItem(t, s, id, []string{"law-remove"}, []string{})
	removeLawChangeLaw(t, s)
	entry := mustBuiltinDefinition(t, "workflow.implementation")
	fields := lawChangeConstructorFields(t, s, 2, []string{}, []string{})
	fields[workflowLawChangeReasonField] = mustJSONValue("remove the obsolete law")
	fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
	change, binding := constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	if change == nil || len(change.LawIDs) != 1 || change.LawBefore[0].Absent {
		t.Fatalf("successor lost the deletion's owning-start scope: %+v", change)
	}
	if err := enactLawChange(t, s, id, change, binding); err != nil {
		t.Fatalf("declared removal cannot enact after current-law recovery: %v", err)
	}
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, id)); err != nil {
		t.Fatalf("declared removal cannot complete: %v", err)
	}
}

func TestLawRemovalClassifiedEnactment(t *testing.T) {
	const id = "law-removal-classified"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-remove", "legislated", "agent-surface")
	startLawChange(t, s, id)
	change, err := deriveLawChange(t, s, id, lawChangeV1, lawChangeFields(t, map[string]any{
		workflowLawChangeReasonField: "remove the obsolete law", workflowLawChangeRemovalsField: []string{"law-remove"},
	}), []string{"law-remove"}, nil, []string{"product"})
	if err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, validateWorkflowLawChangeShape(change, []string{"product"}, []string{"law-remove"}, nil), KindApprovalRequired, "removals-only classified change still needs consumed approval")
	change.Approval = lawChangeApprovalProof()
	if err := validateWorkflowLawChangeShape(change, []string{"product"}, []string{"law-remove"}, nil); err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindOutcomeMismatch, "declared removal still present")
	removeLawChangeLaw(t, s)
	if err := enactLawChange(t, s, id, change, nil); err != nil {
		t.Fatalf("classified removal cannot enact: %v", err)
	}
}

func TestLawRemovalDeclarationSafeguards(t *testing.T) {
	const id = "law-removal-safeguards"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-retain", "derived", "agent-surface")
	startLawChange(t, s, id)
	for name, removals := range map[string]any{
		"undeclared": []string{"law-other"}, "duplicate": []string{"law-remove", "law-remove"},
		"null": nil, "not a list": "law-remove",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := deriveLawChange(t, s, id, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "remove", workflowLawChangeRemovalsField: removals}), []string{"law-remove"}, nil, []string{})
			requireFailureKind(t, err, KindInvalidPayload, name)
		})
	}
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "remove one, revise one", workflowLawChangeRemovalsField: []string{"law-remove"}, workflowLawChangeTextPinsField: lawChangePins("law-retain")})
	change, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-remove", "law-retain"}, nil, []string{"security"})
	if err != nil {
		t.Fatal(err)
	}
	change.Approval = lawChangeApprovalProof()
	removeLawChangeLaw(t, s)
	if err := enactLawChange(t, s, id, change, nil); err != nil {
		t.Fatal(err)
	}
	editLawChangeLaw(t, s, "law-retain")
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindApprovalRequired, "retained law still matches approved text exactly")
	agentOwned := *change
	agentOwned.Approval, agentOwned.TextPins = nil, nil
	lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='legislated' WHERE law_id='law-retain'`)
	requireFailureKind(t, enactLawChange(t, s, id, &agentOwned, nil), KindMissingEvidence, "a removal does not exempt a retained agent revision from the derived tier")
	lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='derived' WHERE law_id='law-retain'`)
	if err := enactLawChange(t, s, id, &agentOwned, nil); err != nil {
		t.Fatal(err)
	}
	fields[workflowLawChangeTextPinsField] = mustJSONValue(lawChangePins("law-remove", "law-retain"))
	_, err = deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-remove", "law-retain"}, nil, []string{"security"})
	requireFailureKind(t, err, KindInvalidPayload, "removal cannot also claim retained text")
	change.Removals = nil
	change.TextPins = lawChangePins("law-remove", "law-retain")
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindMissingEvidence, "unapproved absence is not an approved text revision")
	change.Approval = nil
	change.TextPins = nil
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindMissingEvidence, "agent cannot silently turn a text revision into removal")
	change.Removals = []string{"law-other"}
	requireFailureKind(t, validateWorkflowLawChangeShape(change, []string{}, change.LawIDs, nil), KindInvalidPayload, "recorded removal outside scope")
	change.Removals = []string{"law-remove"}
	change.LawBefore[0].Absent = true
	change.LawBefore[0].ContentHash = ""
	change.LawBefore[0].Tier = ""
	change.LawBefore[0].DomainIDs = []string{}
	requireFailureKind(t, validateWorkflowLawChangeShape(change, []string{}, change.LawIDs, nil), KindInvalidPayload, "a new law cannot be declared removed")
}

func TestLawRemovalClassifiedCurrentSuccessor(t *testing.T) {
	const id = "law-classified-successor"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-remove", "legislated", "agent-surface")
	entry := mustBuiltinDefinition(t, "workflow.implementation")
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: id, Definition: entry, Actor: lawChangeConstructorActor})
	}); err != nil {
		t.Fatal(err)
	}
	proof := lawChangeApprovalProof()
	lawChangeFold(t, s, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, ownerRefForTest(t, proof.Operator), proof.Operator.PrincipalRef, proof.Operator.ClientRef, proof.Operator.AgentRef, proof.Operator.SessionRef, proof.Operator.ActorClass, "2026-10-20T00:00:00Z")
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,'active',?,'[]','[]','[]','2026-09-10T00:00:00Z')`, proof.Operator.ClientRef, proof.Operator.PrincipalRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,protected_evidence_ref,protected_evidence_digest) VALUES(?,?,?,?,?,?,?,?,'2026-09-10T00:00:00Z','2026-09-11T00:00:00Z',1,1,'fixture',?)`, proof.ApprovalRef, proof.OperationDigest, proof.ScopeJSON, proof.VersionsJSON, proof.Consequence, proof.Operator.PrincipalRef, proof.Operator.ClientRef, proof.Operator.SessionRef, lawChangeHashA); err != nil {
		t.Fatal(err)
	}
	configure := func(r *WorkflowActionExecutionRequest) {
		r.OperatorApprovalRef, r.ApprovalOperationDigest = proof.ApprovalRef, proof.OperationDigest
		r.ApprovalScopeJSON, r.ApprovalVersionsJSON, r.ApprovalConsequence = proof.ScopeJSON, proof.VersionsJSON, proof.Consequence
	}
	owner := ownerRefForTest(t, lawChangeConstructorActor)
	for version := 1; version <= 2; version++ {
		laws := []string{"law-remove"}
		if version == 2 {
			removeLawChangeLaw(t, s)
			laws = []string{}
		}
		fields := lawChangeConstructorFields(t, s, version, laws, []string{})
		fields[workflowLawChangeReasonField] = mustJSONValue("remove the obsolete law")
		fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
		fields["decision_domains"] = mustJSONValue([]string{"product"})
		change, binding := constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, owner, fields, configure)
		if change.Approval == nil || change.Approval.Operator != proof.Operator {
			t.Fatal("removals-only consent lost the consumed operator identity")
		}
		if version == 2 {
			if err := enactLawChange(t, s, id, change, binding); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), s, id, "execution", owner); err != nil {
		t.Fatal(err)
	}
	event := attributionCompletionWithConfirmation(t, s, id, nil, func(f acceptanceRecoveryFixture) error {
		v := readWorkVersion(t, s, id)
		tx := lawChangeTx(t, s)
		request := WorkflowActionExecutionRequest{WorkID: id, OperatorActor: &proof.Operator, Now: time.Unix(20, 0).UTC()}
		events, err := workflowConfirmPremiseEventsForDefinition(t.Context(), tx, entry.Definition, request, ownerRefForTest(t, proof.Operator), nil, "law-removal-confirm", v)
		_ = tx.Rollback()
		if err != nil {
			return err
		}
		return applyWorkflowTestOperation(t.Context(), s, Operation{Events: events, ExpectedVersions: workVersion(id, v)})
	})
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatalf("classified removal cannot complete after current-law recovery: %v", err)
	}
}
