package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// Every ok envelope is validated in MarshalJSON, and only there. The
// in-process dispatch helpers return the struct and never marshal it, so a
// read can pass every test and fail on every real call. Two did: the
// knowledge resolution read emitted a notice kind built from a 40-hex commit and
// realistic home identifiers, 92 bytes against a 64-byte bound, and the
// Domain reads carried a fabricated zero freshness. This test dispatches
// each read against a real fixture and marshals what it would send.
func TestKnowledgeReadEnvelopeMarshalsWithRealisticIdentifiers(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestDomainReadsMarshalWithAmbiguousContractOmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, env := domainEvidenceFixture(t)
	db := s.DatabaseForTesting()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class
		FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1;
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("seed duplicate active contract: %v", err)
	}
	for _, tc := range []struct {
		operation string
		input     string
	}{
		{"active_work", `{"product_id":"product-1","domain_id":"` + singleDomainRootID + `","page":{"cursor":null,"limit":10}}`},
		{"overlaps", `{"product_id":"product-1"}`},
	} {
		resp := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: tc.operation, Input: json.RawMessage(tc.input)}, env)
		if resp.Outcome != OutcomeOK {
			t.Fatalf("concord_domain.%s: %+v", tc.operation, resp.Error)
		}
		if !containsNoticeKind(resp.Omissions, "ambiguous-contract:work-1") {
			t.Fatalf("concord_domain.%s does not report the ambiguous work item: %#v", tc.operation, resp.Omissions)
		}
		for _, notice := range resp.Omissions {
			if len(notice.Kind) > 64 {
				t.Fatalf("concord_domain.%s emits a notice kind of %d bytes: %q", tc.operation, len(notice.Kind), notice.Kind)
			}
		}
		if _, err := json.Marshal(resp); err != nil {
			t.Fatalf("concord_domain.%s does not marshal: %v", tc.operation, err)
		}
	}
}

func containsNoticeKind(notices []Notice, want string) bool {
	for _, notice := range notices {
		if notice.Kind == want {
			return true
		}
	}
	return false
}

// A tool may carry reads and mutations. The envelope validator answers the
// mutation-metadata question per operation, and the generated TS7 schema is
// a separate gate reached only at marshal, so a read can pass the
// structural check and still fail MarshalJSON. This enumerates the surface:
// for each read on a tool that also has a mutation, a minimal ok read
// envelope must marshal, and the schema check is only exercised by the
// marshal itself.
func TestEveryReadOnAMixedToolMarshalsAsARead(t *testing.T) {
	t.Parallel()
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
		e := Envelope{SchemaVersion: "1.0", ManifestDigest: ManifestDigest, RequestID: "r", Origin: "core", Tool: op.Tool, Operation: op.Operation, QueryID: op.QueryID, Outcome: OutcomeOK, Authority: AuthorityAuthoritative, SourceVersionWatermark: []Watermark{}, OrderingKeys: []string{}, Omissions: []Notice{}, Warnings: []Notice{}, EvidenceRefs: []EvidenceRef{}, Items: []json.RawMessage{json.RawMessage(`{}`)}}
		if _, err := json.Marshal(e); err != nil {
			t.Errorf("%s.%s is a read on a mixed tool and does not marshal as a read: %v", op.Tool, op.Operation, err)
		}
	}
	if checked == 0 {
		t.Fatal("no read on a mixed tool found; the surface changed shape")
	}
}

// concord_work_initiative.entries answers ok with entries and narrative, and
// a read envelope carries no mutation metadata. This walks the full path the
// transport sees: create the initiative, add an entry, read the entries, and
// marshal the envelope, which is where the generated schema check runs.
func TestInitiativeEntriesReadMarshalsWithEntriesAndNarrative(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_initiative"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	created, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "create", Input: json.RawMessage(`{"title":"Initiative","value_statement":"Coordinate work","project_ids":["project-1"],"idempotency_key":"initiative-entries-read"}`)}, env)
	if err != nil || created.Outcome != OutcomeOK {
		t.Fatalf("create response=%+v err=%v", created, err)
	}
	initiativeID := (*created.ChangedRefs)[0].ID
	added, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "add_entry", Input: json.RawMessage(`{"initiative_work_id":"` + initiativeID + `","child_work_id":"work-1","expected_version":2,"position":0,"idempotency_key":"initiative-entries-add"}`)}, env)
	if err != nil || added.Outcome != OutcomeOK {
		t.Fatalf("add_entry response=%+v err=%v", added, err)
	}
	read, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "entries", Input: json.RawMessage(`{"initiative_work_id":"` + initiativeID + `"}`)}, env)
	if err != nil || read.Outcome != OutcomeOK {
		t.Fatalf("entries response=%+v err=%v", read, err)
	}
	raw, err := json.Marshal(read)
	if err != nil {
		t.Fatalf("entries read does not marshal: %v", err)
	}
	var entryResult struct {
		Entries   []store.InitiativeEntry `json:"entries"`
		Narrative string                  `json:"narrative"`
	}
	if err := json.Unmarshal(read.Result, &entryResult); err != nil || len(entryResult.Entries) != 1 || entryResult.Entries[0].ChildWorkID != "work-1" {
		t.Fatalf("entries result=%s err=%v", read.Result, err)
	}
	wire := string(raw)
	if strings.Contains(wire, `"changed_refs"`) || strings.Contains(wire, `"next_valid_intents"`) {
		t.Fatalf("a read envelope must not carry mutation metadata: %s", firstBytes(raw, 400))
	}
	if !strings.Contains(wire, `"entries"`) || !strings.Contains(wire, `"narrative"`) {
		t.Fatalf("entries read lost its entries or narrative member: %s", firstBytes(raw, 400))
	}
}

// An initiative is a stored work kind that no agent may capture. The list
// result once reused the capture input's enum, so a browse that included an
// initiative refused its own answer. A result admits every stored kind.
func TestWorkBrowseListAnswersWhenAnInitiativeIsInTheResult(t *testing.T) {
	t.Parallel()
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
