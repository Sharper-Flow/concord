package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
)

// Every ok envelope is validated in MarshalJSON, and only there. The
// in-process dispatch helpers return the struct and never marshal it, so a
// read can pass struct-level tests while its wire answer fails validation.
// Each read dispatches against a real fixture and marshals its wire answer.
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
	} {
		resp := dispatchRead(t, s, service, InvokeRequest{Tool: tc.tool, Operation: tc.op, Input: json.RawMessage(tc.input)}, env)
		if resp.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("%s.%s: %+v", tc.tool, tc.op, resp.Error)
		}
		if _, err := json.Marshal(resp); err != nil {
			t.Fatalf("%s.%s does not marshal: %v", tc.tool, tc.op, err)
		}
		if tc.op == "resolve_note" {
			assertKnowledgeNotice(t, resp.Warnings, "legislated_authority_unverified", map[string]any{
				"law_id": "knowledge-decision", "work_id": demandHomeWork, "contract_version": int64(1),
			})
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
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read"})
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	resp := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "list", Input: json.RawMessage(`{"product_id":"product-1","page":{"cursor":null,"limit":10}}`)}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
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
		INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class
		FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1;
		DELETE FROM write_guard`); err != nil {
		t.Fatalf("seed duplicate active contract: %v", err)
	}
	for _, tc := range []struct {
		operation string
		input     string
	}{
		{"overlaps", `{"product_id":"product-1"}`},
	} {
		resp := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: tc.operation, Input: json.RawMessage(tc.input)}, env)
		if resp.Outcome != agentcontract.OutcomeOK {
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

func containsNoticeKind(notices []agentcontract.Notice, want string) bool {
	for _, notice := range notices {
		if notice.Kind == want {
			return true
		}
	}
	return false
}

// Every declared read must marshal without mutation metadata through both
// the per-operation envelope validator and the generated TS7 schema gate.
func TestEveryReadOperationMarshalsAsARead(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, op := range agentcontract.ContractOperations {
		if op.Kind != agentcontract.OperationRead {
			continue
		}
		checked++
		t.Run(op.Tool+"."+op.Operation, func(t *testing.T) {
			e := agentcontract.Envelope{SchemaVersion: "1.0", ManifestDigest: agentcontract.ManifestDigest, RequestID: "r", Origin: "core", Tool: op.Tool, Operation: op.Operation, QueryID: op.QueryID, Outcome: agentcontract.OutcomeOK, Authority: agentcontract.AuthorityAuthoritative, SourceVersionWatermark: []agentcontract.Watermark{}, OrderingKeys: []string{}, Omissions: []agentcontract.Notice{}, Warnings: []agentcontract.Notice{}, EvidenceRefs: []agentcontract.EvidenceRef{}, Items: []json.RawMessage{json.RawMessage(`{}`)}}
			if _, err := json.Marshal(e); err != nil {
				t.Fatalf("read does not marshal without mutation metadata: %v", err)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no declared read operation found")
	}
}

func firstBytes(raw []byte, n int) string {
	if len(raw) < n {
		return string(raw)
	}
	return string(raw[:n])
}
