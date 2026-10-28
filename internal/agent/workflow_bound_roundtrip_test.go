package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

// The store's write validation and the generated continuity read schema must
// agree, or a legal write wedges the item's continuity trace (issue #817).
// These tests hold the two surfaces to one bound each and prove the legal
// maximum round-trips through the real envelope validation.

func schemaDef(t *testing.T, name string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(payloadschema.GeneratedPayloadSchemaDocument), &document); err != nil {
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
	t.Parallel()
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
	t.Parallel()
	schemaReference := schemaDef(t, "reference")
	document := payloadschema.Document()
	definitions, _ := document["$defs"].(map[string]any)
	reference, _ := definitions["reference"].(map[string]any)
	if reference == nil {
		t.Fatal("generated schema carries no $defs/reference")
	}
	pattern, ok := schemaReference["pattern"].(string)
	if !ok || pattern == "" {
		t.Fatalf("$defs/reference pattern = %v, want the enumerated Unicode White_Space rule", schemaReference["pattern"])
	}
	// ValidReference counts UTF-8 bytes. The byte bounds travel as
	// x-minBytes/x-maxBytes; the code-point bounds derive from them
	// (minLength = ceil(2/4)) so they never refuse a store-admitted value.
	for keyword, want := range map[string]float64{"x-minBytes": 2, "x-maxBytes": 128, "minLength": 1, "maxLength": 128} {
		if got := schemaReference[keyword]; got != want {
			t.Fatalf("$defs/reference %s = %v, want %v", keyword, got, want)
		}
	}
	probes := map[string]bool{
		"internal/store/workflow.go": true,
		"commit:aa11bb22":            true,
		"é":                          true,
		strings.Repeat("é", 64):      true,
		"a":                          false,
		strings.Repeat("x", 129):     false,
		strings.Repeat("é", 65):      false,
		"has space":                  false,
		"a\fb":                       false,
		"a\u00a0b":                   false,
		"a\ufeffb":                   true,
	}
	for value, want := range probes {
		if got := store.ValidReference(value); got != want {
			t.Fatalf("ValidReference(%q) = %v, want %v", value, got, want)
		}
		if err := payloadschema.ValidateValue(value, reference, document, "$"); (err == nil) != want {
			t.Fatalf("generated reference schema admission for %q = %v, want %v", value, err == nil, want)
		}
	}
}

func TestWithEvidenceKindDefaultMapsInputKind(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{"evidence_ref":"commit:aa11bb22"}`)
	got := withEvidenceKindDefault("bind_evidence", payload, []agentcontract.EvidenceRef{{Kind: "commit", Locator: "commit:aa11bb22"}})
	var fields map[string]any
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["evidence_kind"] != "commit" {
		t.Fatalf("evidence_kind = %v, want commit mapped from the input evidence array", fields["evidence_kind"])
	}
	explicit := json.RawMessage(`{"evidence_kind":"verification"}`)
	kept := withEvidenceKindDefault("bind_evidence", explicit, []agentcontract.EvidenceRef{{Kind: "commit"}})
	if string(kept) != string(explicit) {
		t.Fatalf("explicit evidence_kind was overridden: %s", kept)
	}
	untouched := withEvidenceKindDefault("record_verdict", payload, []agentcontract.EvidenceRef{{Kind: "commit"}})
	if string(untouched) != string(payload) {
		t.Fatalf("non-evidence action payload was rewritten: %s", untouched)
	}
}

// Current lifecycle metadata preserves the transition's admitted evidence list.
func TestHistoryEvidenceBoundAdmitsEveryActionEvidenceList(t *testing.T) {
	t.Parallel()
	page := schemaDef(t, "work_event_page")
	events, _ := page["properties"].(map[string]any)["entries"].(map[string]any)
	event, _ := events["items"].(map[string]any)
	evidence, _ := event["properties"].(map[string]any)["evidence_refs"].(map[string]any)
	bound, ok := evidence["maxItems"].(float64)
	if !ok {
		// An uncapped read admits every stored list; the output bound
		// ledger proves the stored count has no finite producer and names
		// the typed refusal that bounds the result instead.
		if _, items := evidence["items"].(map[string]any); !items || evidence["type"] != "array" {
			t.Fatalf("work_event_page events[].evidence is not an evidence array: %v", evidence)
		}
		return
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(payloadschema.GeneratedPayloadSchemaDocument), &document); err != nil {
		t.Fatal(err)
	}
	largest, owner := 0.0, ""
	for name, def := range document["$defs"].(map[string]any) {
		if !strings.HasPrefix(name, "work_transition_action_variant_") {
			continue
		}
		fields, _ := def.(map[string]any)["properties"].(map[string]any)["fields"].(map[string]any)
		properties, _ := fields["properties"].(map[string]any)
		refs, _ := properties["evidence_refs"].(map[string]any)
		if items, ok := refs["maxItems"].(float64); ok && items > largest {
			largest, owner = items, name
		}
	}
	if owner == "" {
		t.Fatal("no workflow action variant declares evidence_refs")
	}
	for _, name := range []string{"work_define_revise_input", "work_transition_lifecycle_input"} {
		input, _ := schemaDef(t, name)["properties"].(map[string]any)["evidence"].(map[string]any)
		items, ok := input["maxItems"].(float64)
		if !ok {
			t.Fatalf("%s evidence carries no maxItems: %v", name, input)
		}
		if items > largest {
			largest, owner = items, name
		}
	}
	if bound < largest {
		t.Fatalf("work_event_page events[].evidence maxItems = %v, but %s admits %v evidence_refs", bound, owner, largest)
	}
}

// CD-0242 D2 retires checkpoint traces; current proof receipts remain readable.
func TestBoundEvidenceReceiptsRoundTripThroughHistory(t *testing.T) {
	t.Parallel()
	for _, count := range []int{32, 33, 64} {
		t.Run(fmt.Sprintf("%d references", count), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, service, _, _, engine := workflowEngineFixture(t, "checkpoint evidence reaches history")
			refs := make([]string, count)
			for i := range refs {
				prefix := fmt.Sprintf("artifact:checkpoint-%02d:", i)
				refs[i] = prefix + strings.Repeat("e", 128-len(prefix))
			}
			for _, ref := range refs {
				engine("bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": ref})
			}
			grant, err := service.Authorize(ctx, Invocation{ClientRef: "client-session-exec-aaaa", PrincipalRef: "human-1", SessionRef: "session-exec-aaaa", AgentRef: "agent-exec", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"})
			if err != nil {
				t.Fatal(err)
			}
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, count)
			input := map[string]any{"work_id": "work-1", "record_kinds": []string{"evidence_binding"}, "limit": 10}
			for len(got) <= count {
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: raw}, mutationEnvelope(grant, scopeVersion))
				if err != nil {
					t.Fatal(err)
				}
				if response.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("history receipt page refused: %+v", response.Error)
				}
				var page struct {
					Entries []struct {
						EvidenceRefs []string `json:"evidence_refs"`
					} `json:"entries"`
				}
				if err := json.Unmarshal(response.Result, &page); err != nil {
					t.Fatal(err)
				}
				for _, entry := range page.Entries {
					got = append(got, entry.EvidenceRefs...)
				}
				if response.NextCursor == nil {
					break
				}
				input["page"] = map[string]any{"cursor": *response.NextCursor}
			}
			if !reflect.DeepEqual(got, refs) {
				t.Fatalf("history evidence = %d entries, want all %d checkpoint references in order", len(got), count)
			}
		})
	}
}

func TestContinuityRoundTripLegalMaximum(t *testing.T) {
	t.Parallel()
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
	grant, err := service.Authorize(ctx, Invocation{ClientRef: "client-session-exec-aaaa", PrincipalRef: "human-1", SessionRef: "session-exec-aaaa", AgentRef: "agent-exec", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"})
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
	if response.Outcome != agentcontract.OutcomeOK {
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
