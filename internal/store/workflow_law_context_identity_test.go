package store

import (
	"context"
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
