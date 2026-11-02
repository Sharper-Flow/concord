package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// seedDerivedLawExtension adds a legislated law beside the derived spec:one
// that seedWorkflowLaw seeds: a committed decisions-tier file whose authored
// authority names an approving work the store does not hold, so the source
// verification keeps the declared tier unverified.
func seedDerivedLawExtension(t *testing.T, s *Store) {
	t.Helper()
	commitWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), "spec:legislated", lawChangeFixtureLaw("spec:legislated", "legislated", "root"))
}

// derivedRevisionLegislatedDeclarerLaw is the committed legislated revision
// whose record itself declares the conflicts_with relation against spec:one.
// Its authored shape matches seedDerivedLawExtension's legislated extension:
// the unheld approving work keeps the declared tier unverified.
const derivedRevisionLegislatedDeclarerLaw = "---\n" +
	`{"id":"spec:legislated","title":"Fixture law spec:legislated","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"root","applies_to_domain_ids":[],"law_relations":[{"kind":"conflicts_with","target_id":"spec:one"}]}` + "\n" +
	"---\n\nSynthetic fixture law body.\n"

// runDerivedLawApproval drives one approve_contract on the live
// non-Product-changing workflow and reports the action error instead of
// failing on it, because both the admitted and the refused path are the point
// of the test.
func runDerivedLawApproval(t *testing.T, s *Store, workID string, version int64, actor WorkflowActor, payload json.RawMessage) error {
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
	_, err = applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:derived-revision", IdempotencyIdentity: "approve-derived-" + workID, OperationID: "approve-derived-" + workID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "approve-derived-" + workID,
		RequestID: "request:approve-derived-" + workID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return nil
}

// seedDerivedRevisionWorkflow puts a research instance at its frame step,
// where approve_contract sits, and returns the version the approval drives.
// The research family is the live authoring family whose contract does not
// change Product truth, so the CD-0222 D5 boundaries bind to it unchanged.
func seedDerivedRevisionWorkflow(t *testing.T, s *Store, workID string, actor WorkflowActor) int64 {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	return seedDerivedRevisionWorkflowAt(t, s, workID, actor, registered)
}

// seedDerivedRevisionWorkflowAt initializes the research workflow under the
// given registered pin.
func seedDerivedRevisionWorkflowAt(t *testing.T, s *Store, workID string, actor WorkflowActor, registered RegisteredDefinition) int64 {
	t.Helper()
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
	return 4
}

// TestDerivedLawRevisionOnNonProductChangingContract holds the carve-out
// CD-0222 D5 states: a contract that does
// not change Product truth may carry law_modifies naming only derived
// records, the modification stays visible on the approved contract, and a
// legislated name is still refused. The architecture binding refusal is
// untouched.
func TestDerivedLawRevisionOnNonProductChangingContract(t *testing.T) {
	t.Parallel()
	actor := WorkflowActor{PrincipalRef: "principal:derived-revision", ClientRef: "client:derived-revision", AgentRef: "agent:derived-revision", SessionRef: "session:derived-revision", ActorClass: ActorAgent}

	t.Run("derived only modification is admitted and recorded", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		workID := "derived-revision-admitted"
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		seedDerivedLawExtension(t, s)
		version := seedDerivedRevisionWorkflow(t, s, workID, actor)
		payload := json.RawMessage(`{"premise":"Revise the derived record inside this contract.","spec_mandate":["spec:one"],"law_modifies":["spec:one"],"law_change_reason":"Record why the derived revision moves into this contract.","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["resolved"]}}]}`)
		if err := runDerivedLawApproval(t, s, workID, version, actor, payload); err != nil {
			t.Fatalf("a derived-only law_modifies was refused: %v", err)
		}
		var stored string
		if err := s.DatabaseForTesting().QueryRow(`SELECT law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stored, "spec:one") {
			t.Fatalf("the approved contract dropped the derived revision line: %s", stored)
		}
	})

	t.Run("a legislated modification stays refused", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		workID := "derived-revision-refused"
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		seedDerivedLawExtension(t, s)
		version := seedDerivedRevisionWorkflow(t, s, workID, actor)
		payload := json.RawMessage(`{"premise":"Attempt to revise legislated law without a Product-changing workflow.","spec_mandate":["spec:legislated"],"law_modifies":["spec:legislated"],"law_change_reason":"Attempt the legislated revision.","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["resolved"]}}]}`)
		err := runDerivedLawApproval(t, s, workID, version, actor, payload)
		if err == nil {
			t.Fatal("a legislated law_modifies was admitted on a non-Product-changing contract")
		}
		var failure *Failure
		if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "may revise only derived law") {
			t.Fatalf("refusal does not name the tier boundary: %v", err)
		}
	})

	t.Run("the architecture binding refusal is unchanged", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		workID := "derived-revision-binding"
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		seedDerivedLawExtension(t, s)
		version := seedDerivedRevisionWorkflow(t, s, workID, actor)
		payload := json.RawMessage(`{"premise":"Attempt to carry a binding without Product truth.","spec_mandate":[],"law_modifies":[],"architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]},"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["resolved"]}}]}`)
		err := runDerivedLawApproval(t, s, workID, version, actor, payload)
		if err == nil {
			t.Fatal("an architecture binding was admitted on a non-Product-changing contract")
		}
		var failure *Failure
		if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "cannot carry architecture_binding") {
			t.Fatalf("refusal does not name the binding boundary: %v", err)
		}
		if failure.Kind != KindInvalidPayload || failure.RetrySafe || failure.EffectPossible || len(failure.CommittedRefs) != 0 || failure.RecoveryAction != "omit architecture_binding" {
			t.Fatalf("binding refusal does not admit pre-effect request correction: %+v", failure)
		}
	})
}

func TestWorkflowContractProductGuardRequiresBinding(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"approval", "successor"} {
		for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`)} {
			fields := map[string]json.RawMessage{}
			if raw != nil {
				fields["architecture_binding"] = raw
			}
			_, err := workflowContractProductGuard(context.Background(), nil, "work", fields, nil, true, role, "complete the architecture binding")
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "requires architecture_binding") || failure.RecoveryAction != "complete the architecture binding" {
				t.Fatalf("Product-changing %s without binding must remain refused: %v", role, err)
			}
		}
	}
}

// TestTierAdmittedConflictRecordsTheContractRevisionLine holds the CD-0159
// clause that a conflict a derived record declares is revised inside the
// contract and recorded there as a revision line, at no separate approval. The
// boundary test proves the tier admits the conflict; this proves the admitted
// revision reaches the contract the approval writes. Both ends of a
// conflicts_with relation are mandated: revising the derived declaring end
// approves in one agent action and records that law as the revision line beside
// a pin for each mandated law, while leaving the conflict unrevised meets the
// conflict refusal and writes no contract. When the legislated record declares
// the conflict instead, revising the derived end cannot withdraw it, so the
// approval refuses rather than strand the work at completion.
func TestTierAdmittedConflictRecordsTheContractRevisionLine(t *testing.T) {
	t.Parallel()
	actor := WorkflowActor{PrincipalRef: "principal:tier-conflict", ClientRef: "client:tier-conflict", AgentRef: "agent:tier-conflict", SessionRef: "session:tier-conflict", ActorClass: ActorAgent}
	approve := func(t *testing.T, workID, declarer, modifies string) (*Store, error) {
		t.Helper()
		s := openTemp(t)
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		seedDerivedLawExtension(t, s)
		// A conflicts_with relation is authored on its declaring record: the
		// declarer argument names the law whose committed front matter
		// declares the relation (law-folder-authority.md, Identity and
		// change).
		home := lawChangeFixtureHome(t, s)
		switch declarer {
		case "spec:one":
			commitFixtureLawFile(t, home, "spec:one", derivedRevisionConflictLaw)
		case "spec:legislated":
			commitWorkflowFixtureLaw(t, home, "spec:legislated", derivedRevisionLegislatedDeclarerLaw)
		default:
			t.Fatalf("fixture declarer %s has no committed record", declarer)
		}
		version := seedDerivedRevisionWorkflow(t, s, workID, actor)
		// A declared modification carries the CD-0230 D4 law-change inputs; an
		// empty law_modifies changes no law and must not declare them.
		lawChange := ""
		if modifies != `[]` {
			lawChange = `,"law_change_reason":"Resolve the declared conflict inside this contract."`
		}
		payload := json.RawMessage(`{"premise":"Resolve the conflict inside this contract.","spec_mandate":["spec:legislated","spec:one"],"law_modifies":` + modifies + lawChange + `,"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["resolved"]}}]}`)
		return s, runDerivedLawApproval(t, s, workID, version, actor, payload)
	}

	t.Run("revising the derived end records the revision line", func(t *testing.T) {
		t.Parallel()
		workID := "tier-conflict-derived"
		s, err := approve(t, workID, "spec:one", `["spec:one"]`)
		if err != nil {
			t.Fatalf("revising the derived end of a conflict was refused: %v", err)
		}
		var stored string
		if err := s.DatabaseForTesting().QueryRow(`SELECT law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		var lines []string
		if err := json.Unmarshal([]byte(stored), &lines); err != nil {
			t.Fatal(err)
		}
		if len(lines) != 1 || lines[0] != "spec:one" {
			t.Fatalf("the approved contract does not carry the derived revision line: %s", stored)
		}
	})

	t.Run("leaving the conflict unrevised meets the conflict refusal", func(t *testing.T) {
		t.Parallel()
		workID := "tier-conflict-unrevised"
		s, err := approve(t, workID, "spec:one", `[]`)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindRelationConflict {
			t.Fatalf("an unrevised conflict did not meet the conflict refusal: %v", err)
		}
		var contracts int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
			t.Fatal(err)
		}
		if contracts != 0 {
			t.Fatalf("a refused conflict wrote %d contracts", contracts)
		}
	})

	t.Run("a legislated declarer refuses the derived revision at approval", func(t *testing.T) {
		t.Parallel()
		workID := "tier-conflict-legislated-declarer"
		s, err := approve(t, workID, "spec:legislated", `["spec:one"]`)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindRelationConflict || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "spec:legislated" {
			t.Fatalf("a derived revision against a legislated declaration did not refuse at approval naming the declarer: %v", err)
		}
		var contracts int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
			t.Fatal(err)
		}
		if contracts != 0 {
			t.Fatalf("a refused approval wrote %d contracts", contracts)
		}
	})
}
