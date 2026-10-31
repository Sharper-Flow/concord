package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
)

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
