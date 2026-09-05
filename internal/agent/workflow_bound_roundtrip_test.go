package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The store's write validation and the generated continuity read schema must
// agree, or a legal write wedges the item's continuity trace (issue #817).
// These tests hold the two surfaces to one bound each and prove the legal
// maximum round-trips through the real envelope validation.

func schemaDef(t *testing.T, name string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(GeneratedPayloadSchemaDocument), &document); err != nil {
		t.Fatalf("generated schema does not parse: %v", err)
	}
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("generated schema carries no $defs")
	}
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("generated schema carries no $defs/%s", name)
	}
	return def
}

func TestGeneratedPremiseBoundMatchesStore(t *testing.T) {
	premise := schemaDef(t, "workflow_premise")
	if got := premise["maxLength"]; got != float64(store.WorkflowPremiseMaxLength) {
		t.Fatalf("$defs/workflow_premise maxLength = %v, want %d: the write and read bounds drifted apart", got, store.WorkflowPremiseMaxLength)
	}
	contract := schemaDef(t, "workflow_contract")
	properties, _ := contract["properties"].(map[string]any)
	premiseRef, _ := properties["premise"].(map[string]any)
	if premiseRef["$ref"] != "#/$defs/workflow_premise" {
		t.Fatalf("workflow_contract.premise = %v, want a $defs/workflow_premise reference", premiseRef)
	}
}

func TestGeneratedReferenceDefMatchesStore(t *testing.T) {
	reference := schemaDef(t, "reference")
	if got := reference["pattern"]; got != "^\\S+$" {
		t.Fatalf("$defs/reference pattern = %v, want the no-whitespace rule the store validates", got)
	}
	if got := reference["minLength"]; got != float64(2) {
		t.Fatalf("$defs/reference minLength = %v, want 2", got)
	}
	if got := reference["maxLength"]; got != float64(128) {
		t.Fatalf("$defs/reference maxLength = %v, want 128", got)
	}
	probes := map[string]bool{
		"internal/store/workflow.go": true,
		"commit:aa11bb22":            true,
		"a":                          false,
		strings.Repeat("x", 129):     false,
		"has space":                  false,
	}
	for value, want := range probes {
		if got := store.ValidReference(value); got != want {
			t.Fatalf("ValidReference(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestWithEvidenceKindDefaultMapsInputKind(t *testing.T) {
	payload := json.RawMessage(`{"evidence_ref":"commit:aa11bb22"}`)
	got := withEvidenceKindDefault("bind_evidence", payload, []EvidenceRef{{Kind: "commit", Locator: "commit:aa11bb22"}})
	var fields map[string]any
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["evidence_kind"] != "commit" {
		t.Fatalf("evidence_kind = %v, want commit mapped from the input evidence array", fields["evidence_kind"])
	}
	explicit := json.RawMessage(`{"evidence_kind":"verification"}`)
	kept := withEvidenceKindDefault("bind_evidence", explicit, []EvidenceRef{{Kind: "commit"}})
	if string(kept) != string(explicit) {
		t.Fatalf("explicit evidence_kind was overridden: %s", kept)
	}
	untouched := withEvidenceKindDefault("record_verdict", payload, []EvidenceRef{{Kind: "commit"}})
	if string(untouched) != string(payload) {
		t.Fatalf("non-evidence action payload was rewritten: %s", untouched)
	}
}

func TestContinuityRoundTripLegalMaximum(t *testing.T) {
	ctx := context.Background()
	s, service, _, _, engine := workflowEngineFixture(t, strings.Repeat("p", store.WorkflowPremiseMaxLength))
	engine("checkpoint_context", map[string]any{
		"checkpoint_id": "checkpoint-roundtrip", "checkpoint_sequence": 1,
		"active_unit": "internal/store and internal/agent", "hypothesis": "bounds agree on both sides", "diagnosis": "one shared bound source", "strategy": "round-trip the legal maximum",
		"touched_refs":      []string{"internal/store/workflow.go", "internal/agent/generated_payload_schemas.go"},
		"evidence_refs":     []string{"commit:aa11bb22ccdd3344"},
		"pending_questions": []string{}, "pending_decisions": []string{},
	})
	for _, kind := range []string{"verification", "review", "approval", "commit", "durable_note", "artifact"} {
		engine("bind_evidence", map[string]any{"evidence_kind": kind, "evidence_ref": "roundtrip:" + kind})
	}
	grant, err := service.Authorize(ctx, Invocation{ClientRef: "client-session-exec-aaaa", PrincipalRef: "human-1", SessionRef: "session-exec-aaaa", AgentRef: "agent-exec", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "continuity", Input: json.RawMessage(`{"work_id":"work-1","page":{"cursor":null,"limit":10}}`)}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("continuity read with legal-maximum writes failed: %+v", response.Error)
	}
	var payload struct {
		Pinned struct {
			Contract *struct {
				Premise string `json:"premise"`
			} `json:"contract"`
			LatestCheckpoint *struct {
				TouchedRefs []string `json:"touched_refs"`
			} `json:"latest_checkpoint"`
		} `json:"pinned"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Pinned.Contract == nil || len(payload.Pinned.Contract.Premise) != store.WorkflowPremiseMaxLength {
		t.Fatalf("pinned contract premise did not round-trip at the legal maximum")
	}
	if payload.Pinned.LatestCheckpoint == nil {
		t.Fatal("pinned checkpoint missing")
	}
	for _, ref := range payload.Pinned.LatestCheckpoint.TouchedRefs {
		if !strings.Contains(ref, "/") {
			t.Fatalf("checkpoint touched_refs lost repository paths: %v", payload.Pinned.LatestCheckpoint.TouchedRefs)
		}
	}
}
