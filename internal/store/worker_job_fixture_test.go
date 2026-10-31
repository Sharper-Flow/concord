package store

import (
	"testing"
)

func seedChecksRepairFixture(t *testing.T, workID, step string) workflowReturnRouteFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, step, []string{"verification"}, []string{"verification", "review", "artifact"})
}

func passExitCode() *int { value := 0; return &value }
