package agent

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
)

// An ok envelope is validated in MarshalJSON, and only there. A read can
// therefore pass every in-process test and fail on every real call, because
// the dispatch helpers return the struct and never marshal it.
//
// TestKnowledgeReadEnvelopeMarshalsWithRealisticIdentifiers covers that for
// the three knowledge reads it names, and the Domain and work-browse tests
// cover one read each. A read the manifest declares and no test names is
// covered by nothing, and the omission is invisible: nothing fails when a
// read joins the manifest without a test.
//
// readMarshalInputs closes that. It carries one dispatch input per declared
// read, and TestEveryDeclaredReadMarshalsItsEnvelope requires the map and the
// manifest's read set to match exactly in both directions. A read added to
// the manifest fails here until it is given an input, and an entry left
// behind by a removed read fails here too.
var readMarshalInputs = map[string]string{
	"concord_product_view.resolve":          `{"product_id":"prod-alpha"}`,
	"concord_product_view.snapshot":         `{"product_id":"prod-alpha"}`,
	"concord_product_view.portfolio":        `{"product_id":"prod-alpha","page":{"cursor":null,"limit":10}}`,
	"concord_product_view.blocked_sessions": `{"product_id":"prod-alpha"}`,
	"concord_product_view.resources":        `{"product_id":"prod-alpha"}`,

	"concord_work_browse.list":             `{"product_id":"prod-alpha","page":{"cursor":null,"limit":10}}`,
	"concord_work_browse.blocked":          `{"product_id":"prod-alpha","page":{"cursor":null,"limit":10}}`,
	"concord_work_browse.ready":            `{"product_id":"prod-alpha","page":{"cursor":null,"limit":10}}`,
	"concord_work_browse.scope":            `{"product_id":"prod-alpha","project_id":"proj-web"}`,
	"concord_work_browse.resource_claims":  `{"product_id":"prod-alpha"}`,
	"concord_work_browse.messages":         `{"product_id":"prod-alpha","work_id":"work-done"}`,
	"concord_work_browse.worktree_audit":   `{"product_id":"prod-alpha"}`,
	"concord_work_browse.worktree_inspect": `{"work_id":"work-done","mode":"status"}`,

	"concord_work_trace.history":               `{"work_id":"work-done","page":{"cursor":null,"limit":10}}`,
	"concord_work_trace.observations":          `{"work_id":"work-done"}`,
	"concord_work_trace.external_observations": `{"work_id":"work-done"}`,
	"concord_work_trace.relations":             `{"work_id":"work-done"}`,
	"concord_work_trace.continuity":            `{"work_id":"work-done","page":{"cursor":null,"limit":10}}`,
	"concord_work_trace.research":              `{"product_id":"prod-alpha"}`,

	"concord_knowledge.search":       `{"product_id":"prod-alpha","page":{"cursor":null,"limit":10}}`,
	"concord_knowledge.resolve_note": `{"work_id":"work-done"}`,
	"concord_knowledge.unprocessed":  `{"product_id":"prod-alpha"}`,

	"concord_work_initiative.entries": `{"initiative_work_id":"work-done"}`,

	"concord_domain.list":        `{"product_id":"prod-alpha","page":{"cursor":null,"limit":10}}`,
	"concord_domain.detail":      `{"product_id":"prod-alpha","domain_id":"root"}`,
	"concord_domain.active_work": `{"product_id":"prod-alpha","domain_id":"root"}`,
	"concord_domain.attachments": `{"product_id":"prod-alpha","domain_id":"root"}`,
	"concord_domain.overlaps":    `{"product_id":"prod-alpha"}`,
}

// TestReadMarshalInputsCoverEveryDeclaredRead is the coverage gate. It holds
// the input table and the manifest to the same read set, so neither can drift
// from the other in silence.
func TestReadMarshalInputsCoverEveryDeclaredRead(t *testing.T) {
	declared := map[string]bool{}
	for _, op := range ContractOperations {
		if op.Kind == OperationRead {
			declared[op.ID] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("the manifest declares no read operation; the surface changed shape")
	}
	var missing, extra []string
	for id := range declared {
		if _, ok := readMarshalInputs[id]; !ok {
			missing = append(missing, id)
		}
	}
	for id := range readMarshalInputs {
		if !declared[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, id := range missing {
		t.Errorf("read %s is declared in the manifest and has no marshal input; add one to readMarshalInputs", id)
	}
	for _, id := range extra {
		t.Errorf("readMarshalInputs carries %s, which the manifest does not declare as a read", id)
	}
}

// TestEveryDeclaredReadMarshalsItsEnvelope dispatches each declared read
// against a seeded fixture and marshals the envelope the caller would receive.
// A read that answers ok must marshal, because MarshalJSON is where an ok
// envelope is validated and a real call is the only path that reaches it.
//
// A read that refuses is not a failure here. This test pins the marshal
// boundary, not the business outcome, and a typed refusal marshals through
// the same encoder. The refusal is reported so a fixture that stops producing
// an ok answer for a read is visible rather than silently weakening coverage.
func TestEveryDeclaredReadMarshalsItsEnvelope(t *testing.T) {
	ctx := context.Background()
	s, service, grant, corpus := agentJobsPM1Fixture(t)
	if _, err := pm1fixture.SeedKnowledge(ctx, s, corpus, t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedKnowledge: %v", err)
	}
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")

	ids := make([]string, 0, len(readMarshalInputs))
	for id := range readMarshalInputs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		op, ok := contractOperationByID(id)
		if !ok {
			continue // TestReadMarshalInputsCoverEveryDeclaredRead reports this.
		}
		t.Run(id, func(t *testing.T) {
			resp := dispatchRead(t, s, service, InvokeRequest{
				Tool:      op.Tool,
				Operation: op.Operation,
				Input:     json.RawMessage(readMarshalInputs[id]),
			}, env)
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("%s does not marshal: %v", id, err)
			}
			if resp.Outcome != OutcomeOK {
				t.Logf("%s refused rather than answering ok: %s", id, firstBytes(raw, 300))
				return
			}
			for _, notice := range append(resp.Omissions, resp.Warnings...) {
				if len(notice.Kind) > 64 {
					t.Fatalf("%s emits a notice kind of %d bytes: %q", id, len(notice.Kind), notice.Kind)
				}
			}
		})
	}
}

func contractOperationByID(id string) (ContractOperation, bool) {
	for _, op := range ContractOperations {
		if op.ID == id {
			return op, true
		}
	}
	return ContractOperation{}, false
}
