package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
)

// CD-0068 D3 makes dismissal the operator's act, so the core mints an approval
// challenge and refuses. CD-0037 D1 requires that refusal to carry the typed
// consequence summary the challenge was minted with. The operator can only act
// on a refusal that reaches them, so the refusal must encode.

// TestUnapprovedObservationDismissalRefusalIsDeliverable is the reproduction.
// The refusal was built correctly and could not be marshalled, so a wrong
// Domain observation could not be dismissed: the caller received a transport
// fault carrying no approval_ref to approve with.
func TestUnapprovedObservationDismissalRefusalIsDeliverable(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "product_read"})
	if err := pm1fixture.SeedCurrentProductDomain(ctx, s, "product-1", "project-1"); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	domain := pm1fixture.FixtureRootDomainID
	env := mutationEnvelope(grant, scopeVersion)

	recordInput := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q,"statement":"This Domain has no owner for the scanner failure path.","refs":["docs/core-architecture.md"],"tags":["gap"],"idempotency_key":"dismiss-delivery-obs"}`, domain))
	if recorded, recordErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "observation_record", Input: recordInput}, env); recordErr != nil || recorded.Outcome != OutcomeOK {
		t.Fatalf("observation_record response=%+v err=%v", recorded.Error, recordErr)
	}
	var observationID string
	if scanErr := s.DatabaseForTesting().QueryRow(`SELECT observation_id FROM domain_observations WHERE product_id='product-1' AND domain_id=?`, domain).Scan(&observationID); scanErr != nil {
		t.Fatal(scanErr)
	}

	dismissInput := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q,"observation_id":%q,"idempotency_key":"dismiss-delivery-1"}`, domain, observationID))
	refusal, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "observation_dismiss", Input: dismissInput}, env)
	if err != nil || refusal.Error == nil || refusal.Error.Kind != "approval_required" {
		t.Fatalf("unapproved dismissal response=%+v err=%v", refusal, err)
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
	if summary.Tool != "concord_domain" || summary.Operation != "observation_dismiss" {
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
