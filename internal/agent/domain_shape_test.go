package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func TestDomainShapeInputSchema(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
	}{
		{`{"product_id":"prod-alpha","path":"internal/store/store.go"}`, true},
		{`{"product_id":"prod-alpha","domain_id":"root"}`, true},
		{`{"product_id":"prod-alpha","subject":"settings"}`, true},
		{`{"product_id":"prod-alpha","subject":"` + strings.Repeat("s", 1024) + `"}`, true},
		{`{"product_id":"prod-alpha"}`, false},
		{`{"path":"source"}`, false},
		{`{"product_id":"prod-alpha","subject":""}`, false},
		{`{"product_id":"prod-alpha","subject":" "}`, false},
		{`{"product_id":"prod-alpha","path":"src","domain_id":"root"}`, false},
		{`{"product_id":"prod-alpha","path":"src","subject":"settings"}`, false},
		{`{"product_id":"prod-alpha","domain_id":"root","subject":"settings"}`, false},
		{`{"product_id":"prod-alpha","subject":"settings","command":["run"]}`, false},
		{`{"product_id":"prod-alpha","path":"` + strings.Repeat("p", 513) + `"}`, false},
		{`{"product_id":"prod-alpha","domain_id":"` + strings.Repeat("d", 257) + `"}`, false},
		{`{"product_id":"prod-alpha","subject":"` + strings.Repeat("é", 513) + `"}`, false},
	} {
		if err := ValidateOperationPayload("concord_domain", "shape", json.RawMessage(tc.input), false); (err == nil) != tc.valid {
			t.Errorf("input %s: valid=%t, err=%v", tc.input, tc.valid, err)
		}
	}
}

func TestDomainShapeHandlerMatchesCoreAndFootprint(t *testing.T) {
	s, service, grant, _, _ := agentJobsKnowledgeFixture(t, false)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	domains, err := s.QueryDomainList(context.Background(), store.DomainListRequest{Product: "prod-alpha"})
	if err != nil || domains.Registry == nil {
		t.Fatalf("fixture registry: %+v / %v", domains.Registry, err)
	}
	for _, selector := range []store.DomainShapeSelector{{Path: "internal/store/store.go"}, {DomainID: domains.Registry.RootDomainID}, {Subject: "unknown-fact"}} {
		input, _ := json.Marshal(domainShapeInput{ProductID: "prod-alpha", DomainShapeSelector: selector})
		before := domainEventCountForTest(t, s)
		response := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "shape", Input: input}, env)
		if response.Outcome != OutcomeOK {
			t.Fatalf("shape %s: %s / %+v", input, response.Outcome, response.Error)
		}
		want, err := s.QueryDomainShape(context.Background(), "prod-alpha", selector)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(want)
		if string(response.Result) != string(raw) {
			t.Fatalf("handler changed core result: %s != %s", response.Result, raw)
		}
		if len(response.Result) > store.DomainShapeResultMaxBytes {
			t.Fatalf("result exceeds Store bound: %d", len(response.Result))
		}
		envelope, err := json.Marshal(response)
		if err != nil || len(envelope) > 51200 {
			t.Fatalf("shape envelope bytes=%d: %v", len(envelope), err)
		}
		if domainEventCountForTest(t, s) != before {
			t.Fatal("shape read changed authoritative events")
		}
		if selector.Path != "" {
			footprint := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "footprint", Input: json.RawMessage(`{"product_id":"prod-alpha","paths":["` + selector.Path + `"]}`)}, env)
			var payload store.DomainFootprintPayload
			if footprint.Outcome != OutcomeOK || json.Unmarshal(footprint.Result, &payload) != nil || !reflect.DeepEqual(payload.Shapes, []store.DomainShapeResult{want}) {
				t.Fatalf("footprint lost selected shapes: %s / %+v", footprint.Result, footprint.Error)
			}
		}
		t.Logf("selector=%s result_bytes=%d envelope_bytes=%d", input, len(raw), len(envelope))
	}
}

func TestDomainShapeProductReadAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, product, want string
		capabilities        []Capability
	}{
		{"explicit authorized foreign Product", "product-2", "unknown_scope", []Capability{"product_read"}},
		{"outside policy", "product-3", "unauthorized", []Capability{"product_read"}},
		{"missing read capability", "product-2", "unauthorized", []Capability{"work_define"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, _ := crossProductPolicyFixture(t, tc.capabilities)
			version, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_domain", Operation: "shape", Input: json.RawMessage(`{"product_id":"` + tc.product + `","subject":"settings"}`)}, mutationEnvelope(grant, version))
			if err != nil || response.Error == nil || response.Error.Kind != tc.want {
				t.Fatalf("shape authority: %+v, err=%v; want %s", response, err, tc.want)
			}
			if tc.want == "unknown_scope" && !strings.Contains(response.Error.Message, "Product has no unique canonical knowledge home") {
				t.Fatalf("authorized read did not reach selected Product's home lookup: %+v", response.Error)
			}
		})
	}
}

func TestDomainShapeReadingSourceSchema(t *testing.T) {
	valid := map[string]any{"kind": "domain_shape", "source_id": "project", "commit_oid": strings.Repeat("a", 40), "content_hash": "sha256:" + strings.Repeat("b", 64), "selector": map[string]any{"subject": "settings"}}
	check := func(value any, want bool) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := payloadschema.Validate("work_context_reading_source_domain_shape", raw); (err == nil) != want {
			t.Fatalf("source %s: want valid=%t, err=%v", raw, want, err)
		}
	}
	check(valid, true)
	reading, _ := json.Marshal(map[string]any{"domain_id": "root", "reason": "inspect selected shape", "source": valid})
	if err := payloadschema.Validate("work_context_reading", reading); err != nil {
		t.Fatalf("required reading union omitted Domain Shape: %v", err)
	}
	for _, key := range []string{"kind", "source_id", "commit_oid", "content_hash", "selector"} {
		incomplete := map[string]any{}
		for k, v := range valid {
			if k != key {
				incomplete[k] = v
			}
		}
		check(incomplete, false)
	}
	for _, selector := range []any{map[string]any{}, map[string]any{"path": "src", "domain_id": "root"}, map[string]any{"subject": "settings", "unknown": true}} {
		valid["selector"] = selector
		check(valid, false)
	}
	valid["selector"] = map[string]any{"path": "src"}
	valid["law_id"] = "law"
	check(valid, false)
}

func TestDomainShapeTypedFactResults(t *testing.T) {
	hash, commit := "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 40)
	for kind, variant := range map[string]string{
		"invariant":       `{"statement":"keep one writer","applies_to_domain_ids":["root"],"enforced_by":[{"kind":"go_test","value":"TestWriter","check_id":"writer-check"}]}`,
		"data_object":     `{"name":"orders","resource_id":"database","writer_domain_ids":["root"],"reader_domain_ids":[],"invariant_ids":[]}`,
		"state_authority": `{"state":"orders","authoritative_resource_id":"database","derived_copies":[{"resource_id":"cache","rebuild_route":{"project_id":"project","path":"source/rebuild.go","symbol":"Rebuild"}}],"disagreement_behavior":"database wins"}`,
		"config_coupling": `{"setting":"PORT","source":"environment","read_by":{"project_id":"project","path":"source/main.go","symbol":"Main"},"effect":"sets listener","environments":["test"]}`,
	} {
		t.Run(kind, func(t *testing.T) {
			var fact store.DomainFact
			if err := json.Unmarshal([]byte(`{"schema_version":"1.0","id":"fact-1","kind":"`+kind+`","owner_domain_id":"root","owning_root":"source","standing":"observed","`+kind+`":`+variant+`}`), &fact); err != nil {
				t.Fatal(err)
			}
			result := store.DomainShapeResult{
				ProductID: "product", HomeProjectID: "project", HomeLocatorID: "repo", Selector: store.DomainShapeSelector{Subject: fact.ID},
				KnowledgeCommit: commit, SnapshotContentHash: hash, RegistryContentHash: hash, PackageMapContentHash: hash,
				ToolingCommit: commit, ToolingContentHash: hash, Roots: []store.DomainShapeRoot{{Path: "source", Role: "domain", OwnerDomainID: "root"}},
				DomainIDs: []string{"root"}, Facts: []store.DomainShapeFact{{Fact: fact, Path: ".concord/docs/knowledge/facts/fact-1.json", ContentHash: hash}},
				GoverningLaw:          []store.DomainLawRecord{{LawID: "law", Kind: "decision", Title: "Law", Path: ".concord/docs/law.md", ContentHash: hash, ScannedCommit: commit}},
				ReferenceVerification: "unverified", ExtractorVerification: "unverified",
			}
			raw, _ := json.Marshal(result)
			if err := ValidateOperationPayload("concord_domain", "shape", raw, true); err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			_ = json.Unmarshal(raw, &object)
			selected := object["facts"].([]any)[0].(map[string]any)["fact"].(map[string]any)
			body := selected[kind].(map[string]any)
			body["unknown"] = true
			bad, _ := json.Marshal(object)
			if err := ValidateOperationPayload("concord_domain", "shape", bad, true); err == nil {
				t.Fatal("nested unknown fact field accepted")
			}
			delete(body, "unknown")
			selected["standing"] = "law"
			bad, _ = json.Marshal(object)
			if err := ValidateOperationPayload("concord_domain", "shape", bad, true); err == nil {
				t.Fatal("law fact without governing law accepted")
			}
			selected["governing_law_ids"] = []string{"law"}
			good, _ := json.Marshal(object)
			if err := ValidateOperationPayload("concord_domain", "shape", good, true); err != nil {
				t.Fatalf("law fact: %v", err)
			}
			field, limit := "statement", 1024
			switch kind {
			case "data_object":
				field, limit = "name", 256
			case "state_authority":
				field = "state"
			case "config_coupling":
				field, limit = "setting", 256
			}
			original := body[field]
			body[field] = strings.Repeat("x", limit+1)
			bad, _ = json.Marshal(object)
			if err := ValidateOperationPayload("concord_domain", "shape", bad, true); err == nil {
				t.Fatal("over-bound nested fact field accepted")
			}
			body[field] = original
			selected["owning_root"] = "source/../other"
			bad, _ = json.Marshal(object)
			if err := ValidateOperationPayload("concord_domain", "shape", bad, true); err == nil {
				t.Fatal("parent traversal fact root accepted")
			}
			selected["owning_root"] = "source"
			selected["kind"] = "unknown"
			bad, _ = json.Marshal(object)
			if err := ValidateOperationPayload("concord_domain", "shape", bad, true); err == nil {
				t.Fatal("unknown fact kind accepted")
			}
			t.Logf("typed result bytes=%d", len(raw))
		})
	}
}

func TestDomainShapeMaximumResultFitsEnvelope(t *testing.T) {
	s, service, grant, _, _ := agentJobsKnowledgeFixture(t, false)
	response := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "shape", Input: json.RawMessage(`{"product_id":"prod-alpha","subject":"fact-1"}`)}, agentJobsEnvelope(grant, "proj-web", "prod-alpha"))
	if response.Outcome != OutcomeOK {
		t.Fatalf("shape read: %+v", response.Error)
	}
	var result store.DomainShapeResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		result.Facts = append(result.Facts, store.DomainShapeFact{
			Fact: store.DomainFact{SchemaVersion: "1.0", ID: fmt.Sprintf("fact-%d", i), Kind: store.DomainFactInvariant,
				OwnerDomainID: "root", OwningRoot: "source", Standing: "observed",
				Invariant: &store.DomainInvariantFact{Statement: "s", AppliesToDomainIDs: []string{"root"}, EnforcedBy: []store.DomainFactEnforcementAnchor{{Kind: "go_test", Value: "TestWriter", CheckID: "writer-check"}}}},
			Path: fmt.Sprintf(".concord/docs/knowledge/facts/fact-%d.json", i), ContentHash: "sha256:" + strings.Repeat("a", 64),
		})
	}
	raw, _ := json.Marshal(result)
	remaining := store.DomainShapeResultMaxBytes - len(raw)
	for i := range result.Facts {
		padding := min(remaining, store.DomainFactMaxTextBytes-1)
		result.Facts[i].Fact.Invariant.Statement += strings.Repeat("s", padding)
		remaining -= padding
	}
	raw, _ = json.Marshal(result)
	if remaining != 0 || len(raw) != store.DomainShapeResultMaxBytes {
		t.Fatalf("boundary fixture bytes=%d remaining=%d", len(raw), remaining)
	}
	if err := ValidateOperationPayload("concord_domain", "shape", raw, true); err != nil {
		t.Fatal(err)
	}
	response.Result = raw
	envelope, err := json.Marshal(response)
	if err != nil || len(envelope) > 51200 {
		t.Fatalf("maximum Store result envelope bytes=%d: %v", len(envelope), err)
	}
	t.Logf("maximum result_bytes=%d envelope_bytes=%d", len(raw), len(envelope))
}
