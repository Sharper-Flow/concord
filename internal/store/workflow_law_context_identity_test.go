package store

import (
	"context"
	"reflect"
	"testing"
)

func TestLawContextQualifiedReferenceCannotCaptureLocalLaw(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "work-law-identity")
	seedWorkflowLaw(t, s)
	repo := workflowLawFixtureRepo(t, s)
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), "work-law-identity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveLawContextSubjects(ctx, "project", "workflow-law-locator", repo, "work", []WorkflowLawContextLaw{{LawID: "spec:one"}, {LawID: "other-project/spec:one"}})
	requireFailureKind(t, err, KindProjectionNotFound, "another Project's law must not resolve to a local record with the same bare ID")
}

func TestLawContextAbsentOwnHomeReferencesKeepOnlyContractIdentity(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const workID = "absent-law-identity"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	repo := workflowLawFixtureRepo(t, s)
	removeCommittedFixtureLaw(t, lawChangeFixtureHome(t, s))
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"spec:one", "project/spec:one"} {
		roles := []string{"mandated", "modified", "obligation"}
		laws, err := resolveLawContextSubjects(ctx, "project", "workflow-law-locator", repo, workID, []WorkflowLawContextLaw{{
			LawID: id, Roles: roles, ObligationIDs: []string{"verification"}, Kind: "spec", Status: "accepted", Title: "stale title", Path: "stale-path", Criteria: []WorkflowLawContextCriterionBinding{{Criterion: 1, PredicateID: "predicate:stale"}},
		}})
		want := []WorkflowLawContextLaw{{LawID: id, Roles: roles, ObligationIDs: []string{"verification"}, Status: "absent"}}
		if err != nil || !reflect.DeepEqual(laws, want) {
			t.Fatalf("absent %s = %+v, %v; want %+v", id, laws, err, want)
		}
	}
	_, err = resolveLawContextSubjects(ctx, "project", "workflow-law-locator", repo, workID, []WorkflowLawContextLaw{{LawID: "other-project/spec:missing", Roles: []string{"added"}}})
	requireFailureKind(t, err, KindProjectionNotFound, "foreign-home absence is not own-home absence")
}
