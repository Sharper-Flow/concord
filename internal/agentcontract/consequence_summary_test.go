package agentcontract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		if *declared.MaxItems != MaxConsequenceSummaryBindings {
			t.Fatalf("%s capacity = %d in the contract, %d at challenge mint", field, *declared.MaxItems, MaxConsequenceSummaryBindings)
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

// TestConsequenceSummaryStillRejectsNonCanonicalBindings pins the strict
// envelope half of the contract: canonicalization happens at the scope-list
// owner, never by loosening the summary validator.
func TestConsequenceSummaryStillRejectsNonCanonicalBindings(t *testing.T) {
	t.Parallel()
	duplicate := &ConsequenceSummary{Tool: "concord_work_relate", Operation: "message_send", Consequence: "relation", OperationDigest: "sha256:" + strings.Repeat("0", 64), Scope: []string{"work_ids:work-a", "work_ids:work-a"}, Versions: []string{}, ExpiresAt: fixedTime().Format(time.RFC3339Nano)}
	if err := validateConsequenceSummaryShape(duplicate); err == nil {
		t.Fatal("duplicate summary bindings accepted")
	}
	unsorted := &ConsequenceSummary{Tool: "concord_work_relate", Operation: "message_send", Consequence: "relation", OperationDigest: "sha256:" + strings.Repeat("0", 64), Scope: []string{"work_ids:work-b", "work_ids:work-a"}, Versions: []string{}, ExpiresAt: fixedTime().Format(time.RFC3339Nano)}
	if err := validateConsequenceSummaryShape(unsorted); err == nil {
		t.Fatal("unsorted summary bindings accepted")
	}
	canonical := &ConsequenceSummary{Tool: "concord_work_relate", Operation: "message_send", Consequence: "relation", OperationDigest: "sha256:" + strings.Repeat("0", 64), Scope: []string{"work_ids:work-a", "work_ids:work-b"}, Versions: []string{}, ExpiresAt: fixedTime().Format(time.RFC3339Nano)}
	if err := validateConsequenceSummaryShape(canonical); err != nil {
		t.Fatalf("canonical summary bindings rejected: %v", err)
	}
}

// TestValidateErrorRefusesNonCanonicalSummary holds the Go error validator to
// the canonical approval summary the runtime derives for a workflow action.
func TestValidateErrorRefusesNonCanonicalSummary(t *testing.T) {
	t.Parallel()
	base := TypedError{Kind: "approval_required", RecoveryAction: RecoveryAction{Kind: "request_approval"}, EffectState: EffectNone}
	base.ConsequenceSummary = &ConsequenceSummary{
		Tool: "concord_work_transition", Operation: "workflow_action", Consequence: "production_write",
		OperationDigest: "sha256:" + strings.Repeat("0", 64),
		Scope:           []string{"product_id:prod-a"},
		Versions:        []string{"work:1"},
		ExpiresAt:       fixedTime().Format(time.RFC3339Nano),
	}
	if err := validateError(base); err != nil {
		t.Fatalf("canonical summary rejected by envelope validation: %v", err)
	}
	base.ConsequenceSummary.Scope = []string{"work_ids:work-b", "work_ids:work-a"}
	if err := validateError(base); err == nil {
		t.Fatal("unsorted scope bindings accepted; the summary must be canonical")
	}
	base.ConsequenceSummary.Scope = []string{"product_id:prod-a"}
	base.ConsequenceSummary.OperationDigest = "not-a-digest"
	if err := validateError(base); err == nil {
		t.Fatal("malformed digest accepted")
	}
}
