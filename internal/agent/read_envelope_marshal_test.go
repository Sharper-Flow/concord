package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
)

// Every ok envelope is validated in MarshalJSON, and only there. The
// in-process dispatch helpers return the struct and never marshal it, so a
// read can pass every test and fail on every real call. Two did: the
// knowledge resolution read emitted a notice kind built from a 40-hex commit and
// realistic home identifiers, 92 bytes against a 64-byte bound, and the
// Domain reads carried a fabricated zero freshness. This test dispatches
// each read against a real fixture and marshals what it would send.
func TestKnowledgeReadEnvelopeMarshalsWithRealisticIdentifiers(t *testing.T) {
	ctx := context.Background()
	s, service, grant, corpus := agentJobsPM1Fixture(t)
	if _, err := pm1fixture.SeedKnowledge(ctx, s, corpus, t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedKnowledge: %v", err)
	}
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")
	for _, tc := range []struct {
		tool, op, input string
	}{
		{"concord_knowledge", "search", `{"product_id":"prod-alpha","kinds":["decision","lesson"],"page":{"cursor":null,"limit":10}}`},
		{"concord_knowledge", "resolve_note", `{"knowledge_id":"knowledge-decision"}`},
		{"concord_knowledge", "unprocessed", `{"product_id":"prod-alpha"}`},
	} {
		resp := dispatchRead(t, s, service, InvokeRequest{Tool: tc.tool, Operation: tc.op, Input: json.RawMessage(tc.input)}, env)
		if resp.Outcome != OutcomeOK {
			t.Fatalf("%s.%s: %+v", tc.tool, tc.op, resp.Error)
		}
		if _, err := json.Marshal(resp); err != nil {
			t.Fatalf("%s.%s does not marshal: %v", tc.tool, tc.op, err)
		}
		for _, notice := range append(resp.Omissions, resp.Warnings...) {
			if len(notice.Kind) > 64 {
				t.Fatalf("%s.%s emits a notice kind of %d bytes: %q", tc.tool, tc.op, len(notice.Kind), notice.Kind)
			}
		}
	}
}

// The Domain reads carry no observation time from the store. The envelope
// admits a null freshness for that; a zero instant is refused at marshal.
func TestDomainReadEnvelopeMarshalsWithoutFreshness(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	resp := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "list", Input: json.RawMessage(`{"product_id":"product-1","page":{"cursor":null,"limit":10}}`)}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("domain list: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("domain list does not marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"freshness":null`) {
		t.Fatalf("a read with no observation time must carry a null freshness, got: %s", firstBytes(raw, 400))
	}
}

// A tool may carry reads and mutations. The envelope validator once asked
// whether the tool was a mutation tool, so every read on a mixed tool was
// validated as a mutation and refused at marshal. This enumerates the
// surface: for each read on a tool that also has a mutation, a minimal ok
// read envelope must marshal without mutation metadata.
func TestEveryReadOnAMixedToolMarshalsAsARead(t *testing.T) {
	mutating := map[string]bool{}
	for _, op := range ContractOperations {
		if op.Kind == OperationMutation {
			mutating[op.Tool] = true
		}
	}
	checked := 0
	for _, op := range ContractOperations {
		if op.Kind != OperationRead || !mutating[op.Tool] {
			continue
		}
		checked++
		e := Envelope{SchemaVersion: "1.0", ManifestDigest: ManifestDigest, RequestID: "r", Origin: "core", Tool: op.Tool, Operation: op.Operation, QueryID: op.QueryID, Outcome: OutcomeOK, Authority: AuthorityAuthoritative, Items: []json.RawMessage{}}
		if err := e.validateOK(); err != nil && strings.Contains(err.Error(), "mutation metadata") {
			t.Errorf("%s.%s is a read on a mixed tool and is validated as a mutation: %v", op.Tool, op.Operation, err)
		}
	}
	if checked == 0 {
		t.Fatal("no read on a mixed tool found; the surface changed shape")
	}
}

// An initiative is a stored work kind that no agent may capture. The list
// result once reused the capture input's enum, so a browse that included an
// initiative refused its own answer. A result admits every stored kind.
func TestWorkBrowseListAnswersWhenAnInitiativeIsInTheResult(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_initiative"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	created, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "create", Input: json.RawMessage(`{"title":"Initiative","value_statement":"Coordinate work","project_ids":["project-1"],"idempotency_key":"initiative-in-list"}`)}, env)
	if err != nil || created.Outcome != OutcomeOK {
		t.Fatalf("create initiative: err=%v resp=%+v", err, created.Error)
	}
	initiativeID := (*created.ChangedRefs)[0].ID
	listed := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: json.RawMessage(`{"work_ids":["` + initiativeID + `"],"page":{"cursor":null,"limit":5}}`)}, env)
	if listed.Outcome != OutcomeOK {
		t.Fatalf("list including an initiative: %+v", listed.Error)
	}
	if _, err := json.Marshal(listed); err != nil {
		t.Fatalf("list including an initiative does not marshal: %v", err)
	}
	if !strings.Contains(string(listed.Result), `"kind":"initiative"`) {
		t.Fatalf("initiative not in result: %s", firstBytes(listed.Result, 300))
	}
}

func firstBytes(raw []byte, n int) string {
	if len(raw) < n {
		return string(raw)
	}
	return string(raw[:n])
}
