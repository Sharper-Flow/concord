package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWorkflowPlanDispatchBoundsAndClosure(t *testing.T) {
	const workID = "plan-boundary"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "plan")
	defer f.store.Close()
	outcome := func(fields map[string]any) map[string]any { return fields["outcomes"].([]any)[0].(map[string]any) }
	touch := func(fields map[string]any) map[string]any { return fields["touches"].([]any)[0].(map[string]any) }
	decision := func(fields map[string]any) map[string]any { return fields["decisions"].([]any)[0].(map[string]any) }
	tests := []struct {
		name   string
		field  string
		change func(map[string]any)
	}{
		{"no outcomes", "outcomes", func(f map[string]any) { f["outcomes"] = []any{} }},
		{"17 outcomes", "outcomes", func(f map[string]any) { f["outcomes"] = make([]any, 17) }},
		{"statement", "outcomes", func(f map[string]any) { outcome(f)["statement"] = strings.Repeat("x", 513) }},
		{"done condition", "outcomes", func(f map[string]any) { outcome(f)["done_condition"] = strings.Repeat("x", 1025) }},
		{"evidence", "outcomes", func(f map[string]any) { outcome(f)["evidence"] = strings.Repeat("x", 1025) }},
		{"9 dependencies", "outcomes", func(f map[string]any) {
			outcome(f)["depends_on"] = []string{"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh", "ii"}
		}},
		{"absent dependency", "depends_on", func(f map[string]any) { outcome(f)["depends_on"] = []string{"outcome:absent"} }},
		{"self dependency", "depends_on", func(f map[string]any) { outcome(f)["depends_on"] = []string{"outcome:one"} }},
		{"duplicate outcome", "outcomes", func(f map[string]any) { f["outcomes"] = append(f["outcomes"].([]any), outcome(f)) }},
		{"no touches", "touches", func(f map[string]any) { f["touches"] = []any{} }},
		{"65 touches", "touches", func(f map[string]any) { f["touches"] = make([]any, 65) }},
		{"long path", "touches", func(f map[string]any) { touch(f)["path"] = strings.Repeat("x", 129) }},
		{"spaced path", "touches", func(f map[string]any) { touch(f)["path"] = "file:one two" }},
		{"unknown role", "touches", func(f map[string]any) { touch(f)["role"] = "unknown" }},
		{"duplicate path", "touches", func(f map[string]any) { f["touches"] = append(f["touches"].([]any), touch(f)) }},
		{"17 decisions", "decisions", func(f map[string]any) { f["decisions"] = make([]any, 17) }},
		{"5 rejected options", "decisions", func(f map[string]any) {
			decision(f)["rejected"] = []string{"one", "two", "three", "four", "five"}
		}},
		{"long rejected option", "decisions", func(f map[string]any) {
			decision(f)["rejected"] = []string{strings.Repeat("x", 513)}
		}},
		{"unknown status", "decisions", func(f map[string]any) { decision(f)["status"] = "unknown" }},
		{"unknown approval class", "decisions", func(f map[string]any) {
			decision(f)["approval"] = map[string]any{"class": "unknown", "consequence": "risk", "evidence_ref": "evidence:one"}
		}},
		{"long consequence", "decisions", func(f map[string]any) {
			decision(f)["approval"] = map[string]any{"class": "security", "consequence": strings.Repeat("x", 513), "evidence_ref": "evidence:one"}
		}},
		{"question without approval", "approval", func(f map[string]any) { decision(f)["status"] = "operator_question" }},
		{"question with evidence", "evidence_ref", func(f map[string]any) {
			decision(f)["status"] = "operator_question"
			decision(f)["approval"] = map[string]any{"class": "security", "consequence": "risk", "evidence_ref": "evidence:one"}
		}},
		{"settled approval without evidence", "evidence_ref", func(f map[string]any) {
			decision(f)["approval"] = map[string]any{"class": "security", "consequence": "risk"}
		}},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := testWorkflowPlanFields()
			tc.change(fields)
			version := readWorkVersion(t, f.store, workID)
			err := f.store.Transact(t.Context(), func(tx *Transaction) error {
				_, err := ApplyWorkflowActionTx(t.Context(), tx, WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "record_plan", Payload: mustJSONValue(fields), Actor: f.owner, OperationID: fmt.Sprintf("boundary-%d", i), IdempotencyIdentity: fmt.Sprintf("boundary-%d", i), PrincipalRef: f.owner.PrincipalRef, RequestID: fmt.Sprintf("boundary-%d", i), AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()})
				if err == nil {
					return fmt.Errorf("invalid payload was accepted")
				}
				return err
			})
			requirePayloadFailure(t, err, tc.field, "")
			if readWorkVersion(t, f.store, workID) != version {
				t.Fatal("refused payload advanced the version")
			}
		})
	}
	for i, role := range []string{"change", "caller", "reader", "check", "law"} {
		t.Run("accept "+role, func(t *testing.T) {
			fields := testWorkflowPlanFields()
			touch(fields)["role"] = role
			if i == 0 {
				option := strings.Repeat("x", 512)
				decision(fields)["rejected"] = []string{option, option, option, option}
			} else {
				fields["decisions"] = []any{}
			}
			if i > 0 {
				fields["reason"] = "role revision"
			}
			version := readWorkVersion(t, f.store, workID)
			err := f.store.Transact(t.Context(), func(tx *Transaction) error {
				_, err := ApplyWorkflowActionTx(t.Context(), tx, WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "record_plan", Payload: mustJSONValue(fields), Actor: f.owner, OperationID: fmt.Sprintf("role-%d", i), IdempotencyIdentity: fmt.Sprintf("role-%d", i), PrincipalRef: f.owner.PrincipalRef, RequestID: fmt.Sprintf("role-%d", i), AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
