package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestPublishedWorkflowReadShapesPopulatedNestedFields(t *testing.T) {
	for _, field := range []string{"conditions", "impact_notices"} {
		t.Run(field, func(t *testing.T) {
			projection := fullyPopulatedWorkflowReadProjection()
			switch field {
			case "conditions":
				projection.Conditions = []store.WorkflowReadCondition{{ID: "condition:ci", AwaitType: "ci_result", AwaitRef: "run:ci", ResolutionAuthority: "durable_operation:ci", State: "open", ExpectedWithinSeconds: 10, AgeSeconds: 1, Health: "waiting"}}
			case "impact_notices":
				oldHash, newHash := "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64)
				projection.ImpactNotices = []store.WorkflowReadNotice{{NoticeID: "notice:" + strings.Repeat("d", 64), SourceWorkID: "work-read", SourceContractVersion: 1, EntityKind: "spec", EntityRef: "spec:one", TargetWorkID: "work-read", EdgeOwnerWorkID: "work-read", EdgeID: "edge:one", OldHash: &oldHash, NewHash: &newHash, Severity: "informational"}}
			}
			published, err := publishedWorkflowRead(&projection)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(published)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePayloadSchema("workflow_read", raw); err != nil {
				t.Fatalf("populated %s violates the published workflow_read contract: %v", field, err)
			}
			items, ok := published[field].([]any)
			if !ok || len(items) != 1 {
				t.Fatalf("published %s = %v, want one retained item", field, published[field])
			}
		})
	}
}

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
	if err := ValidatePayloadSchema("workflow_read", raw); err != nil {
		t.Fatalf("qualified deleted law ID violates the published workflow_read contract: %v", err)
	}
}
