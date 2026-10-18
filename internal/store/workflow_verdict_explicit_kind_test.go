package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestWorkflowVerdictRequiresExplicitKind(t *testing.T) {
	document := payloadschema.Document()
	published := recordVerdictParityPublishedFields(t, document)
	live, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name     string
		scalar   bool
		payload  string
		wantKind string
	}{
		{"scalar omission", true, `{"contract_version":1,"predicate_id":"predicate:batch-present","evaluation_evidence":["evidence:return-route-verification"]}`, ""},
		{"batch omission", false, `{"contract_version":1,"verdicts":[{"predicate_id":"predicate:batch-present","evaluation_evidence":["evidence:return-route-verification"]}]}`, ""},
		{"batch last entry omission", false, `{"contract_version":1,"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]},{"predicate_id":"predicate:batch-absent","evaluation_evidence":["evidence:return-route-review"]}]}`, ""},
		{"scalar null", true, `{"predicate_id":"predicate:batch-present","verdict_kind":null,"evaluation_evidence":["evidence:return-route-verification"]}`, ""},
		{"scalar empty", true, `{"predicate_id":"predicate:batch-present","verdict_kind":"","evaluation_evidence":["evidence:return-route-verification"]}`, ""},
		{"scalar undeclared", true, `{"predicate_id":"predicate:batch-present","verdict_kind":"excellent","evaluation_evidence":["evidence:return-route-verification"]}`, ""},
		{"batch null", false, `{"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":null,"evaluation_evidence":["evidence:return-route-verification"]}]}`, ""},
		{"batch empty", false, `{"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":"","evaluation_evidence":["evidence:return-route-verification"]}]}`, ""},
		{"batch undeclared", false, `{"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":"excellent","evaluation_evidence":["evidence:return-route-verification"]}]}`, ""},
		{"explicit ok", false, `{"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}]}`, "ok"},
		{"explicit mismatch", false, `{"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"]}]}`, "outcome_mismatch"},
		{"explicit insufficient", false, `{"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":"insufficient_evidence","evaluation_evidence":["evidence:return-route-verification"]}]}`, "insufficient_evidence"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if !testCase.scalar {
				wantAdmitted := testCase.wantKind != ""
				if admitted := validateWorkflowActionPayload(live.Definition, "record_verdict", []byte(testCase.payload)) == nil; admitted != wantAdmitted {
					t.Errorf("core payload admission = %t, want %t", admitted, wantAdmitted)
				}
				var fields map[string]any
				if err := json.Unmarshal([]byte(testCase.payload), &fields); err != nil {
					t.Fatal(err)
				}
				if admitted := payloadschema.ValidateValue(recordVerdictParityDecode(t, fields), published, document, "fields") == nil; admitted != wantAdmitted {
					t.Errorf("published payload admission = %t, want %t", admitted, wantAdmitted)
				}
			}
			const workID = "verdict-explicit-kind"
			var fixture workflowReturnRouteFixture
			if testCase.scalar {
				pinned, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 29)
				if !ok {
					t.Fatal("implementation v29 is not registered")
				}
				fixture = seedWorkflowVerdictBatchFixtureOn(t, workID, 0, []string{"verification"}, batchVerdictContractPredicates(workID), pinned, "workflow.implementation")
			} else {
				fixture = seedWorkflowVerdictBatchFixture(t, workID)
			}
			s := fixture.store
			before := contractCorrectionEffectSnapshot(t, s, workID)
			beforeHash, err := WorkflowProjectionHash(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			err = runIssue933OperatorAction(t, s, workID, "record_verdict", json.RawMessage(testCase.payload), fixture.owner, fixture.operator)
			switch {
			case err == nil:
				var kind string
				if queryErr := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.verdict_kind') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded).Scan(&kind); queryErr != nil {
					t.Fatal(queryErr)
				}
				if testCase.wantKind != "" {
					if kind != testCase.wantKind {
						t.Errorf("recorded verdict_kind = %q, want %q", kind, testCase.wantKind)
					}
					return
				}
				t.Errorf("invalid verdict_kind was admitted and recorded as %q; want invalid_payload before any write", kind)
			case testCase.wantKind != "":
				t.Fatalf("explicit verdict_kind refused: %v", err)
			case !hasFailureKind(err, KindInvalidPayload):
				t.Errorf("omitted verdict_kind refusal = %v, want invalid_payload", err)
			}
			if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
				t.Errorf("omitted verdict_kind changed durable effects: before %s; after %s", before, after)
			}
			afterHash, err := WorkflowProjectionHash(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			if afterHash != beforeHash {
				t.Error("omitted verdict_kind changed workflow projections")
			}
		})
	}
}
