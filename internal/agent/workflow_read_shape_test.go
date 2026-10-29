package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestPublishedWorkflowReadQualifiedStaleLawRevision(t *testing.T) {
	projection := fullyPopulatedWorkflowReadProjection()
	projection.StaleLawRevision.OldLawID = "project-1/spec-1"
	published, err := publishedWorkflowRead(&projection)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("workflow_read", raw); err != nil {
		t.Fatalf("qualified deleted law ID violates the published workflow_read contract: %v", err)
	}
}
