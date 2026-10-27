package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// CD-0037 D1 requires a minted challenge to carry its typed consequence summary.
// The operator can act only when that refusal encodes with the approval ref.
// CD-0243 D3/D7 retain security grant consent, not membership consent.
func TestUnapprovedAccessGrantRefusalIsDeliverable(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := productProjectLinkFixture(t, []Capability{"work_relate", "cross_scope"})
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

// TestConsequenceSummaryBindingBoundsMatchTheContract ties this package's
// validation of the two binding lists to the schema that owns their shape. The
// validator is a hand-written copy of those bounds, and a copy that drifts
// either refuses a summary the contract accepts, which is how a minted
// challenge became undeliverable, or emits one the contract refuses.
func TestConsequenceSummaryBindingBoundsMatchTheContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-envelope.schema.json"))
	if err != nil {
		t.Fatalf("read envelope contract: %v", err)
	}
	var schema struct {
		Defs struct {
			ConsequenceSummary struct {
				Properties map[string]struct {
					MinItems *int `json:"minItems"`
					MaxItems *int `json:"maxItems"`
				} `json:"properties"`
			} `json:"consequenceSummary"`
		} `json:"$defs"`
	}
	if unmarshalErr := json.Unmarshal(raw, &schema); unmarshalErr != nil {
		t.Fatalf("decode envelope contract: %v", unmarshalErr)
	}
	for _, field := range []string{"scope", "versions"} {
		declared, ok := schema.Defs.ConsequenceSummary.Properties[field]
		if !ok || declared.MinItems == nil || declared.MaxItems == nil {
			t.Fatalf("the contract declares no bounds for consequence summary %s; the schema shape moved", field)
		}
		if *declared.MaxItems != maxConsequenceSummaryBindings {
			t.Fatalf("%s capacity = %d in the contract, %d at challenge mint", field, *declared.MaxItems, maxConsequenceSummaryBindings)
		}
		empty := []string{}
		oneShort := make([]string, 0, *declared.MaxItems+1)
		for i := 0; i <= *declared.MaxItems; i++ {
			oneShort = append(oneShort, fmt.Sprintf("binding:%03d", i))
		}
		accepts := map[string]func([]string, int) bool{"scope": sortedBoundedList, "versions": sortedOptionalBindings}[field]
		if got, want := accepts(empty, *declared.MaxItems), *declared.MinItems == 0; got != want {
			t.Errorf("the contract sets minItems %d for %s, so an empty list must be accepted=%v; the validator accepts=%v", *declared.MinItems, field, want, got)
		}
		if accepts(oneShort, *declared.MaxItems) {
			t.Errorf("the contract lists at most %d bindings in %s, the validator accepted %d", *declared.MaxItems, field, len(oneShort))
		}
	}
}
