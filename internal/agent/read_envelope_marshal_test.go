package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
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
		if resp.Outcome != OutcomeOK {
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

// Every declared read must marshal without mutation metadata through both
// the per-operation envelope validator and the generated TS7 schema gate.
func TestEveryReadOperationMarshalsAsARead(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, op := range ContractOperations {
		if op.Kind != OperationRead {
			continue
		}
		checked++
		t.Run(op.Tool+"."+op.Operation, func(t *testing.T) {
			e := Envelope{SchemaVersion: "1.0", ManifestDigest: ManifestDigest, RequestID: "r", Origin: "core", Tool: op.Tool, Operation: op.Operation, QueryID: op.QueryID, Outcome: OutcomeOK, Authority: AuthorityAuthoritative, SourceVersionWatermark: []Watermark{}, OrderingKeys: []string{}, Omissions: []Notice{}, Warnings: []Notice{}, EvidenceRefs: []EvidenceRef{}, Items: []json.RawMessage{json.RawMessage(`{}`)}}
			if _, err := json.Marshal(e); err != nil {
				t.Fatalf("read does not marshal without mutation metadata: %v", err)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no declared read operation found")
	}
}

// An initiative is a stored work kind that no agent may capture. The list
// result once reused the capture input's enum, so a browse that included an
// initiative refused its own answer. A result admits every stored kind.
func TestWorkBrowseListAnswersWhenAnInitiativeIsInTheResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// CD-0213 retired Initiative capture; a historical Initiative still
	// replays, so the store can hold one. The events land in the log
	// directly and RebuildFromLog folds them, because the live append seam
	// refuses a new initiative capture under CD-0213 D4.
	initiativeID := "historical-initiative"
	db := s.DatabaseForTesting()
	for _, event := range []store.Event{
		{EventID: "historical-initiative-created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: initiativeID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"initiative","title":"Initiative","priority":0}`)},
		{EventID: "historical-initiative-member", Kind: "work_project.added", SubjectType: store.SubjectWorkItem, SubjectID: initiativeID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"work_id":"historical-initiative","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatalf("seed historical initiative event: %v", err)
		}
	}
	if err := store.RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("replay historical initiative: %v", err)
	}
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
