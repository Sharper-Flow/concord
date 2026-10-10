package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func field(name string, required bool, maxLength int64) store.WorkflowPayloadField {
	return store.WorkflowPayloadField{Name: name, ValueType: store.PayloadString, Required: required, MaxLength: &maxLength}
}

func closedPayload(fields ...store.WorkflowPayloadField) store.WorkflowPayloadDefinition {
	if fields == nil {
		fields = []store.WorkflowPayloadField{}
	}
	return store.WorkflowPayloadDefinition{Closed: true, Fields: fields}
}

func definition(ref string, version int64, actions ...store.WorkflowActionDefinition) store.WorkflowDefinition {
	return store.WorkflowDefinition{Ref: ref, Version: version, ActionDefinitions: actions}
}

func action(id string, payload store.WorkflowPayloadDefinition) store.WorkflowActionDefinition {
	return store.WorkflowActionDefinition{ID: id, Payload: payload}
}

// TestCurrentFieldsetAdditionProjectsVariants holds the variant shape: two
// current families declare the same action, one with an appended optional
// field, and the projection answers two exact closed variants instead of
// refusing the pair.
func TestCurrentFieldsetAdditionProjectsVariants(t *testing.T) {
	base := closedPayload(field("diagnosis", true, 4096), field("strategy", true, 4096))
	extended := closedPayload(field("diagnosis", true, 4096), field("strategy", true, 4096), field("open_finding_ids", false, 32))
	contracts, err := collectActionContracts(
		[]store.WorkflowDefinition{
			definition("workflow.implementation", 26, action("request_correction", extended)),
			definition("workflow.research", 15, action("request_correction", base)),
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("compatible current variants refused: %v", err)
	}
	contract := contracts["request_correction"]
	if len(contract.Variants) != 2 {
		t.Fatalf("variants = %d, want 2 (extended first, in registration order)", len(contract.Variants))
	}
	if !reflect.DeepEqual(contract.Variants[0].Payload, extended) || !reflect.DeepEqual(contract.Variants[1].Payload, base) {
		t.Fatalf("variant order or shape drifted: %#v", contract.Variants)
	}
	for _, variant := range contract.Variants {
		if !variant.Payload.Closed || !variant.PublicPayload.Closed {
			t.Fatalf("variant payload is not closed: %#v", variant)
		}
	}
	if len(contract.LegacyPayloads) != 0 {
		t.Fatalf("legacy payloads = %d, want 0", len(contract.LegacyPayloads))
	}
}

// TestCurrentSameFieldConflictStillRefuses holds the surviving invariant: a
// field two current families declare with different declarations is a real
// conflict, and the projection refuses it rather than publishing a union no
// definition allows.
func TestCurrentSameFieldConflictStillRefuses(t *testing.T) {
	soft := closedPayload(field("diagnosis", true, 1024))
	hard := closedPayload(field("diagnosis", true, 4096))
	_, err := collectActionContracts(
		[]store.WorkflowDefinition{
			definition("workflow.implementation", 26, action("request_correction", hard)),
			definition("workflow.research", 15, action("request_correction", soft)),
		},
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "action request_correction has inconsistent current payload contracts") {
		t.Fatalf("error = %v, want the inconsistent-current-contracts refusal", err)
	}
	if err == nil || !strings.Contains(err.Error(), "diagnosis") {
		t.Fatalf("error = %v, want the conflicting field named", err)
	}
}

// TestPublicPayloadDivergenceKeepsThePair keeps dispatch_worker's override
// truthful: one variant whose payload and public payload differ stays one
// variant carrying both halves, and a second family repeating the same pair
// dedupes rather than adding a variant.
func TestPublicPayloadDivergenceKeepsThePair(t *testing.T) {
	secret := closedPayload(field("attempt_id", true, 128), field("worker_packet", true, 64))
	public := closedPayload(field("lane_id", true, 128))
	repeat := action("dispatch_worker", secret)
	repeat.PublicPayload = &public
	contracts, err := collectActionContracts(
		[]store.WorkflowDefinition{
			definition("workflow.implementation", 26, repeat),
			definition("workflow.break_fix", 23, func() store.WorkflowActionDefinition {
				again := action("dispatch_worker", secret)
				again.PublicPayload = &public
				return again
			}()),
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("repeated pair refused: %v", err)
	}
	contract := contracts["dispatch_worker"]
	if len(contract.Variants) != 1 {
		t.Fatalf("variants = %d, want the one deduped pair", len(contract.Variants))
	}
	if !reflect.DeepEqual(contract.Variants[0].PublicPayload, public) {
		t.Fatalf("public half lost: %#v", contract.Variants[0])
	}
}

// TestHistoryFoldAdmitsOnlyCompatibleShapes holds the versioned history
// rule: a retained version's closed shape rides the projection only when
// every field it shares with a current variant is declared identically. The
// oracle-free record_worker_job shape stays authorable; a retained shape
// that conflicts on a shared field stays unauthorable, exactly as before;
// the closed-empty legacy era and non-closed shapes keep their old handling.
func TestHistoryFoldAdmitsOnlyCompatibleShapes(t *testing.T) {
	shared := field("job_id", true, 128)
	current := closedPayload(shared, field("objective", true, 4096), field("acceptance_oracle", true, 64))
	oracleFree := closedPayload(shared, field("objective", true, 4096))
	conflicting := closedPayload(shared, field("objective", true, 2048))
	contracts, err := collectActionContracts(
		[]store.WorkflowDefinition{definition("workflow.implementation", 26, action("record_worker_job", current))},
		nil,
		[]store.WorkflowDefinition{
			definition("workflow.implementation", 25, action("record_worker_job", oracleFree)),
			definition("workflow.implementation", 24, action("record_worker_job", conflicting)),
			definition("workflow.implementation", 23, action("record_worker_job", closedPayload())),
			definition("workflow.implementation", 22, action("record_worker_job", store.WorkflowPayloadDefinition{Fields: []store.WorkflowPayloadField{}})),
		},
	)
	if err != nil {
		t.Fatalf("history fold refused: %v", err)
	}
	contract := contracts["record_worker_job"]
	if len(contract.Variants) != 1 {
		t.Fatalf("variants = %d, want the single current oracle shape", len(contract.Variants))
	}
	if len(contract.LegacyPayloads) != 2 {
		t.Fatalf("legacy payloads = %#v, want the oracle-free shape and the closed-empty era only", contract.LegacyPayloads)
	}
	if !reflect.DeepEqual(contract.LegacyPayloads[0], oracleFree) {
		t.Fatalf("first legacy payload = %#v, want the compatible oracle-free shape", contract.LegacyPayloads[0])
	}
	if len(contract.LegacyPayloads[1].Fields) != 0 {
		t.Fatalf("second legacy payload = %#v, want the closed-empty era", contract.LegacyPayloads[1])
	}
}

// TestShippedBuiltinsProjectWithoutRefusal is the integration the branch
// broke: the real current definitions (impl26/breakfix23 with the oracle
// fields, the recovery list and other families without) and the real
// retained versions fold into one truthful projection.
func TestShippedBuiltinsProjectWithoutRefusal(t *testing.T) {
	contracts, err := collectActionContracts(
		store.BuiltinWorkflowDefinitions(),
		store.BuiltinWorkflowRecoveryActionDefinitions(),
		store.BuiltinWorkflowDefinitionsWithHistory(),
	)
	if err != nil {
		t.Fatalf("shipped builtins refused: %v", err)
	}
	correction := contracts["request_correction"]
	if len(correction.Variants) != 2 {
		t.Fatalf("request_correction variants = %d, want the oracle shape and the recovery shape", len(correction.Variants))
	}
	hasOpenFindings := false
	for _, variant := range correction.Variants {
		for _, f := range variant.Payload.Fields {
			if f.Name == "open_finding_ids" {
				hasOpenFindings = true
			}
		}
	}
	if !hasOpenFindings {
		t.Fatalf("no request_correction variant declares open_finding_ids")
	}
	job := contracts["record_worker_job"]
	if len(job.Variants) != 1 {
		t.Fatalf("record_worker_job variants = %d, want the single oracle current shape", len(job.Variants))
	}
	if len(job.LegacyPayloads) != 1 || len(job.LegacyPayloads[0].Fields) == 0 {
		t.Fatalf("record_worker_job legacy = %#v, want the oracle-free retained shape", job.LegacyPayloads)
	}
	oracleDeclared := false
	for _, f := range job.Variants[0].Payload.Fields {
		if f.Name == "acceptance_oracle" && f.Required && f.SchemaRef == "worker_acceptance_oracle" {
			oracleDeclared = true
		}
	}
	if !oracleDeclared {
		t.Fatalf("record_worker_job current variant does not require acceptance_oracle via worker_acceptance_oracle")
	}
	for id, contract := range contracts {
		for _, variant := range contract.Variants {
			if !variant.Payload.Closed || !variant.PublicPayload.Closed {
				t.Fatalf("action %s carries an open variant", id)
			}
		}
		for _, legacy := range contract.LegacyPayloads {
			if !legacy.Closed {
				t.Fatalf("action %s carries a non-closed legacy payload", id)
			}
		}
	}
}

// TestProjectionIsDeterministicAcrossRuns holds the regeneration contract:
// the same inputs fold to the same variant and legacy order every run, so
// generated artifacts stay byte-stable.
func TestProjectionIsDeterministicAcrossRuns(t *testing.T) {
	first, firstErr := collectActionContracts(store.BuiltinWorkflowDefinitions(), store.BuiltinWorkflowRecoveryActionDefinitions(), store.BuiltinWorkflowDefinitionsWithHistory())
	second, secondErr := collectActionContracts(store.BuiltinWorkflowDefinitions(), store.BuiltinWorkflowRecoveryActionDefinitions(), store.BuiltinWorkflowDefinitionsWithHistory())
	if firstErr != nil || secondErr != nil {
		t.Fatalf("determinism run failed: %v / %v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two runs of the same builtins produced different projections")
	}
}
