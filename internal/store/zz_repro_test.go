package store

import (
	"context"
	"os"
	"testing"
)

// Repro against a copy of the live database: does the lease check see this
// session as the executing actor of the decision item?
func TestReproLiveLease(t *testing.T) {
	path := os.Getenv("REPRO_DB")
	if path == "" {
		t.Skip("REPRO_DB not set")
	}
	const workID = "work-ea46e62fe00c233f4be78900"
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	executing, err := s.WorkflowExecutingActor(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	sessionRef := DeriveWorkflowActorRef("principal:operator", "opencode", "concord-1", "ses_f82187f14ffeK3OoOUhsH9HYLY")
	t.Logf("executing=%s session=%s equal=%v", executing, sessionRef, executing == sessionRef)
}
