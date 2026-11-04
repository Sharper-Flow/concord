package store

import (
	"encoding/json"
	"testing"
)

func attributionItem(t *testing.T, s *Store, id string, laws, domains []string, configure ...func(map[string]json.RawMessage)) {
	t.Helper()
	seedWork(t, s, id)
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: id, Actor: lawChangeConstructorActor})
	}); err != nil {
		t.Fatal(err)
	}
	fields := lawChangeConstructorFields(t, s, 1, laws, domains)
	for _, edit := range configure {
		edit(fields)
	}
	constructLawChangeContract(t, s, WorkflowStepPlanChange, id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	var mandates []string
	if err := json.Unmarshal(fields["spec_mandate"], &mandates); err != nil {
		t.Fatal(err)
	}
	for _, law := range mandates {
		op := "attribution-mandate-" + id + "-" + law
		seedWorkflowAuthority(t, s, op, id, "principal/law", "request/law", []string{law})
		v := readWorkVersion(t, s, id)
		event := workflowEventWithActor(op, WorkflowEvidenceBound, id, ownerRefForTest(t, lawChangeConstructorActor), map[string]any{"work_id": id, "expected_version": v, "resulting_version": v + 1, "evidence_kind": "artifact", "immutable_subject_ref": law, "producer_id": "principal/law", "producer_run_ref": op, "producer_watermark": "request/law", "observed_at": "2026-10-20T00:00:00Z"})
		if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(id, v)}); err != nil {
			t.Fatal(err)
		}
	}
	moveWorkflowPlanFixtureFrontier(t, s, id, "implement")
}

func attributionCompletion(t *testing.T, s *Store, id string) Event {
	t.Helper()
	return attributionCompletionWith(t, s, id, nil)
}

// attributionCompletionWith lets candidate edit the repository before
// its HEAD becomes the reviewed candidate.
func attributionCompletionWith(t *testing.T, s *Store, id string, candidate func(repo string)) Event {
	t.Helper()
	return attributionCompletionWithConfirmation(t, s, id, candidate, nil)
}

func attributionCompletionWithConfirmation(t *testing.T, s *Store, id string, candidate func(repo string), confirm func(acceptanceRecoveryFixture) error) Event {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "fixture.txt", "fixture\n")
	commitKnowledgeRepo(t, repo, "fixture")
	if candidate != nil {
		candidate(repo)
	}
	owner := ownerRefForTest(t, lawChangeConstructorActor)
	moveWorkflowPlanFixtureFrontier(t, s, id, "review")
	f := acceptanceRecoveryFixture{store: s, workID: id, owner: lawChangeConstructorActor, repo: repo}
	if confirm == nil {
		confirm = func(f acceptanceRecoveryFixture) error {
			return f.run(t.Context(), "confirm_premise", "confirm", readWorkVersion(t, s, id), nil, nil)
		}
	}
	if err := confirm(f); err != nil {
		t.Fatal(err)
	}
	completeWorkflowPlanFixture(t, s, id)
	v := readWorkVersion(t, s, id)
	event := workflowEventWithActor("attribution-complete-"+id, WorkflowCompleted, id, owner, map[string]any{
		"work_id": id, "expected_version": v, "resulting_version": v + 1, "terminal_state": "completed", "changed_refs_digest": testDigest("attribution"), "warnings": []string{}, "impact_verdict": "non-breaking",
	})
	event.PayloadVersion = CurrentEventPayloadVersion(WorkflowCompleted)
	return event
}
