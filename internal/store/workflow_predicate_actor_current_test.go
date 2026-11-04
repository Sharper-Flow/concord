package store

import "testing"

func TestWorkflowActorValidation(t *testing.T) {
	if err := ValidateWorkflowActor(WorkflowActor{PrincipalRef: "p1", ClientRef: "c1", AgentRef: "a1", SessionRef: "s1", ActorClass: ActorAgent}); err != nil {
		t.Fatal(err)
	}
}
