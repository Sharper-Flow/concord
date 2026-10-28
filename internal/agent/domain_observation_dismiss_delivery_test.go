package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// CD-0037 D1 requires a minted challenge to carry its typed consequence summary.
// The operator can act only when that refusal encodes with the approval ref.
// CD-0243 D3/D7 retain security grant consent, not membership consent.
func TestUnapprovedAccessGrantRefusalIsDeliverable(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := productProjectLinkFixture(t, []agentcontract.Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	input := productProjectLinkRequest("", "project-2")
	refusal, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "product_project_add", Input: input}, env)
	if err != nil || refusal.Error == nil || refusal.Error.Kind != "approval_required" {
		t.Fatalf("unapproved access grant response=%+v err=%v", refusal, err)
	}
	raw, encodeErr := refusal.Encode()
	if encodeErr != nil {
		t.Fatalf("the approval challenge must reach the operator, got %v", encodeErr)
	}
	var decoded struct {
		Error struct {
			ConsequenceSummary *struct {
				Tool      string   `json:"tool"`
				Operation string   `json:"operation"`
				Scope     []string `json:"scope"`
				Versions  []string `json:"versions"`
			} `json:"consequence_summary"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(raw, &decoded); unmarshalErr != nil {
		t.Fatalf("decode delivered refusal: %v", unmarshalErr)
	}
	summary := decoded.Error.ConsequenceSummary
	if summary == nil {
		t.Fatal("CD-0037 D1: a refusal that minted a challenge must carry its consequence summary")
	}
	if summary.Tool != "concord_work_relate" || summary.Operation != "product_project_add" {
		t.Errorf("consequence summary names %s.%s", summary.Tool, summary.Operation)
	}
	if len(summary.Scope) == 0 {
		t.Error("consequence summary carries no scope binding, so the operator cannot see what they approve")
	}
	if _, ok := decoded.Error.Details["approval_ref"].(string); !ok {
		t.Error("the delivered refusal carries no approval_ref, so the operator has nothing to approve")
	}
}
