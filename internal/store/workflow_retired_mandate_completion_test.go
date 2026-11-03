package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCompletionLawAbsenceRequiresVerifiedCurrentSnapshot(t *testing.T) {
	s, home, workID := standaloneInventoryFixture(t)
	removeCommittedFixtureLaw(t, home)
	tx := lawChangeTx(t, s)
	_, _, err := currentCompletionLawReferences(t.Context(), tx, workID, []string{"spec:one"}, []string{"spec:one"})
	requireFailureKind(t, err, KindInvalidOperation, "unverified absence")
	_ = tx.Rollback()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx = lawChangeTx(t, s)
	mandated, modified, err := currentCompletionLawReferences(ctx, tx, workID, []string{"spec:one"}, []string{"spec:one"})
	if err != nil || !reflect.DeepEqual(mandated, []string{}) || !reflect.DeepEqual(modified, []string{}) {
		t.Fatalf("verified current absence was not omitted from both lists: %v %v error=%v", mandated, modified, err)
	}
	requireFailureKind(t, checkMandatedLawsTx(ctx, tx, workID, []string{"spec:one"}, []string{"spec:one"}, true), KindProjectionNotFound, "approval of an absent modified law")
}

func TestCompletionLawReferencesUseQualifiedSourceProof(t *testing.T) {
	s, home := qualifiedScopeFixture(t)
	const workID = "qualified-completion"
	lawChangeFold(t, s, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Qualified completion','needed',0,1,'t','t')`, workID)
	lawChangeFold(t, s, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?,'p1','primary')`, workID)
	for _, absent := range []bool{false, true} {
		if absent {
			homes, err := projectCanonicalHomeCandidates(t.Context(), s.db, "p2")
			if err != nil || len(homes) != 1 {
				t.Fatalf("qualified fixture home=%v error=%v", homes, err)
			}
			runKnowledgeGit(t, homes[0].RepoPath, "rm", ".concord/law/specs/q-fixture.md")
			commitKnowledgeRepo(t, homes[0].RepoPath, "retire the qualified law")
		}
		ctx, err := s.establishKnowledgeSourceSetProofAtHomes(t.Context(), home.HomeProjectID, home.HomeLocatorID, []string{"p2"})
		if err != nil {
			t.Fatal(err)
		}
		tx := lawChangeTx(t, s)
		mandated, modified, err := currentCompletionLawReferences(ctx, tx, workID, []string{"a", "p2/q"}, []string{"p2/q"})
		wantMandated, wantModified := []string{"a", "p2/q"}, []string{"p2/q"}
		if absent {
			wantMandated, wantModified = []string{"a"}, []string{}
		}
		if err != nil || !reflect.DeepEqual(mandated, wantMandated) || !reflect.DeepEqual(modified, wantModified) {
			t.Fatalf("qualified current references (absent=%t)=%v %v error=%v", absent, mandated, modified, err)
		}
		if err := checkMandatedLawsTx(ctx, tx, workID, mandated, modified, false); err != nil {
			t.Fatalf("qualified current mandate refused: %v", err)
		}
		_ = tx.Rollback()
	}
}

// completeRetiredMandate runs complete through the real action path, so
// admission and the completion gate both answer.
func completeRetiredMandate(t *testing.T, s *Store, f acceptanceRecoveryFixture, label string) error {
	t.Helper()
	return f.run(t.Context(), "complete", label, readWorkVersion(t, s, f.workID), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil)
}

// reachRetiredMandateRelease confirms the premise and parks the item at release.
func reachRetiredMandateRelease(t *testing.T, s *Store, id string) acceptanceRecoveryFixture {
	t.Helper()
	var confirmed acceptanceRecoveryFixture
	attributionCompletionWithConfirmation(t, s, id, nil, func(f acceptanceRecoveryFixture) error {
		confirmed = f
		return f.run(t.Context(), "confirm_premise", "confirm", readWorkVersion(t, s, id), nil, nil)
	})
	return confirmed
}

func requireRetiredMandateStep(t *testing.T, s *Store, id, want string) {
	t.Helper()
	var step string
	if err := s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, id).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != want {
		t.Fatalf("step = %q, want %q", step, want)
	}
}

// Completion answers the current snapshot, not a separate removal declaration.
func TestRetiredMandateModifiedAbsenceCompletes(t *testing.T) {
	for _, reference := range []string{"law-remove", "project/law-remove"} {
		t.Run(reference, func(t *testing.T) {
			const id = "retired-mandate-declared"
			s := seedLawChangeFixture(t, "inventory-seed")
			seedLawChangeLaw(t, s, "law-remove")
			attributionItem(t, s, id, []string{reference}, []string{})
			removeLawChangeLaw(t, s)
			f := reachRetiredMandateRelease(t, s, id)
			if err := completeRetiredMandate(t, s, f, "complete"); err != nil {
				t.Fatalf("declared retirement of a mandated law cannot complete: %v", err)
			}
		})
	}
}

// A missing mandate outside law_modifies still refuses.
func TestRetiredMandateUndeclaredMandateStillRefuses(t *testing.T) {
	const id = "retired-mandate-other"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove")
	seedLawChangeLaw(t, s, "law-retain")
	attributionItem(t, s, id, []string{"law-remove", "law-retain"}, []string{}, func(fields map[string]json.RawMessage) {
		fields["law_modifies"] = mustJSONValue([]string{"law-remove"})
	})
	removeLawChangeLaw(t, s)
	removeWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), "law-retain")
	err := completeRetiredMandate(t, s, reachRetiredMandateRelease(t, s, id), "complete")
	requireFailureKind(t, err, KindProjectionNotFound, "an undeclared absent mandate")
	if !strings.Contains(err.Error(), "law-retain") || strings.Contains(err.Error(), "law-remove") {
		t.Fatalf("refusal names the wrong mandate: %v", err)
	}
}

// Removing an unrelated missing mandate needs a new contract and confirmation.
func TestRetiredMandateContractUpdateRecoversAtRelease(t *testing.T) {
	const id = "retired-mandate-undeclared"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove")
	attributionItem(t, s, id, []string{"law-remove"}, []string{}, func(fields map[string]json.RawMessage) {
		fields["law_modifies"] = mustJSONValue([]string{})
	})
	removeLawChangeLaw(t, s)
	confirmed := reachRetiredMandateRelease(t, s, id)
	requireFailureKind(t, completeRetiredMandate(t, s, confirmed, "complete"), KindProjectionNotFound, "an undeclared retirement")
	// Before any update the active version is confirmed: no late route opens.
	requireFailureKind(t, confirmed.run(t.Context(), "confirm_premise", "confirm-early", readWorkVersion(t, s, id), nil, nil), KindIllegalLifecycleTransition, "late confirmation of a confirmed version")

	entry := mustBuiltinDefinition(t, "workflow.implementation")
	fields := lawChangeConstructorFields(t, s, 2, []string{}, []string{})
	constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	requireRetiredMandateStep(t, s, id, "release")

	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil {
		t.Fatal(err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "confirm_premise") || workPinContainsAction(pin.NextValidIntents, "complete") {
		t.Fatalf("work pin intents = %+v, want late confirm_premise without complete", pin.NextValidIntents)
	}
	requireFailureKind(t, completeRetiredMandate(t, s, confirmed, "complete-unconfirmed"), KindMissingEvidence, "an unconfirmed updated version")

	if err := confirmed.run(t.Context(), "confirm_premise", "confirm-late", readWorkVersion(t, s, id), nil, nil); err != nil {
		t.Fatalf("late confirmation of the updated version: %v", err)
	}
	requireRetiredMandateStep(t, s, id, "release")
	requireFailureKind(t, confirmed.run(t.Context(), "confirm_premise", "confirm-again", readWorkVersion(t, s, id), nil, nil), KindIllegalLifecycleTransition, "a second late confirmation")
	if err := completeRetiredMandate(t, s, confirmed, "complete-reconciled"); err != nil {
		t.Fatalf("reconciled retirement cannot complete: %v", err)
	}
}

// An update that changes a criterion leaves no current proof for it: the late
// confirmation refuses instead of confirming unproved criteria.
func TestRetiredMandateLateConfirmationRequiresCurrentProof(t *testing.T) {
	const id = "retired-mandate-stale-proof"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove")
	attributionItem(t, s, id, []string{"law-remove"}, []string{})
	removeLawChangeLaw(t, s)
	confirmed := reachRetiredMandateRelease(t, s, id)
	entry := mustBuiltinDefinition(t, "workflow.implementation")
	fields := lawChangeConstructorFields(t, s, 2, []string{}, []string{})
	fields["outcome_predicates"] = mustJSONValue([]map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law-changed", "immutable_subject_ref": "commit:law", "expected_result": "pass"}}})
	constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	err := confirmed.run(t.Context(), "confirm_premise", "confirm-late", readWorkVersion(t, s, id), nil, nil)
	requireFailureKind(t, err, KindMissingEvidence, "late confirmation without current criterion proof")
	if !strings.Contains(err.Error(), "predicate:primary") {
		t.Fatalf("refusal does not name the unproved criterion: %v", err)
	}
	requireRetiredMandateStep(t, s, id, "release")
}
