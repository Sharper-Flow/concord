package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord"
	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func schemaDef(t *testing.T, name string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(concord.PayloadSchemaDocument), &document); err != nil {
		t.Fatalf("embedded payload schema does not parse: %v", err)
	}
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("embedded payload schema carries no $defs")
	}
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("embedded payload schema carries no $defs/%s", name)
	}
	return def
}

// TestGeneratedPremiseBoundMatchesStore retired by CON-1710 under CD-0243 D3:
// the published premise bound and the store bound it checked are retired.

func TestGeneratedReferenceDefMatchesStore(t *testing.T) {
	t.Parallel()
	schemaReference := schemaDef(t, "reference")
	document := payloadschema.Document()
	definitions, _ := document["$defs"].(map[string]any)
	reference, _ := definitions["reference"].(map[string]any)
	if reference == nil {
		t.Fatal("embedded payload schema carries no $defs/reference")
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
	untouched := withEvidenceKindDefault("record_delivery", payload, []agentcontract.EvidenceRef{{Kind: "commit"}})
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
	if err := json.Unmarshal([]byte(concord.PayloadSchemaDocument), &document); err != nil {
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
			s, service, engine := workflowEngineFixture(t, "checkpoint evidence reaches history")
			refs := make([]string, count)
			for i := range refs {
				prefix := fmt.Sprintf("artifact:checkpoint-%02d:", i)
				refs[i] = prefix + strings.Repeat("e", 128-len(prefix))
			}
			for i, ref := range refs {
				run := fmt.Sprintf("producer:history-%d", i)
				watermark := fmt.Sprintf("request:history-%d", i)
				seedAgentEvidenceProducer(t, s, "work-1", "human-1", ref, run, watermark)
				engine("bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": ref, "producer_id": "human-1", "producer_run_ref": run, "producer_watermark": watermark})
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
