package agent

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestMutationResultReceiptFailureRollsBackEffect(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	ctx := t.Context()
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	version := workOneVersion(t, s)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	raw := json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"record_intake","fields":{"outcome":"proceed","note":"The bounded stakes statement."},"idempotency_key":"receipt-rollback"}`)
	req := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}
	if _, err := s.DatabaseForTesting().Exec(`CREATE TEMP TRIGGER reject_projection_receipt BEFORE UPDATE ON idempotency_records WHEN NEW.idempotency_key='receipt-rollback' BEGIN SELECT RAISE(ABORT,'synthetic projection receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := mutationProjectionState(t, s)
	refused, err := Dispatch(ctx, s, service, req, env)
	if err != nil || refused.Error == nil || refused.Error.EffectState != agentcontract.EffectNone || !reflect.DeepEqual(before, mutationProjectionState(t, s)) {
		t.Fatalf("receipt failure did not roll back: %+v %v", refused.Error, err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DROP TRIGGER reject_projection_receipt`); err != nil {
		t.Fatal(err)
	}
	result, err := Dispatch(ctx, s, service, req, env)
	if err != nil || result.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("retry after rollback: %+v %v", result.Error, err)
	}
}

func TestMutationProjectionRequiresRetainedIdentity(t *testing.T) {
	source, _, _, first := committedWorkflowReplay(t)
	base := agentcontract.NewBase("projection-identity", source.Tool, source.Operation)
	for range 23 {
		base.EvidenceRefs = append(base.EvidenceRefs, agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)})
	}
	base.EvidenceRefs = append(base.EvidenceRefs, agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 950)})
	if first.ChangedRefs == nil || first.NextValidIntents == nil {
		t.Fatal("fixture missing retained refs or intents")
	}
	for _, tc := range []struct {
		name, tool, operation, key string
		projected                  bool
	}{
		{"missing identity", source.Tool, source.Operation, "", false},
		{"retained identity", source.Tool, source.Operation, "retained-replay", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runtime{Tool: tc.tool, Operation: tc.operation, mutationIdempotencyKey: tc.key}
			out := r.mutationResult(base, first.Result, *first.ChangedRefs, *first.NextValidIntents)
			if tc.projected {
				if out.Outcome != agentcontract.OutcomeOK || len(out.Warnings) == 0 || out.Warnings[len(out.Warnings)-1].Kind != "mutation_result_projected" || out.NextValidIntents == nil || len(*out.NextValidIntents) != 0 {
					t.Fatalf("projection did not withhold intent authority: %+v", out.Error)
				}
			} else if out.Error == nil || out.Error.Kind != "limit_exceeded" || out.Error.EffectState != agentcontract.EffectNone {
				t.Fatalf("missing identity projected: %+v", out.Error)
			}
		})
	}
}

func TestMutationProjectionRejectsUntrustedRetainedDocuments(t *testing.T) {
	source, grant, _, _ := committedWorkflowReplay(t)
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: source.Tool, OperationKind: source.Operation, IdempotencyKey: "retained-replay"}
	prior, found, err := source.Store.LookupMutationIdempotency(t.Context(), key)
	if err != nil || !found {
		t.Fatalf("retained result missing: %t %v", found, err)
	}
	r := runtime{Store: source.Store, Tool: "concord_work_trace", Operation: "mutation_result", Envelope: source.Envelope, Reader: grant}
	base := agentcontract.NewBase("invalid-document", r.Tool, r.Operation)
	compatible := prior
	compatible.AuthorizedScopeSnapshot = strings.ReplaceAll(prior.AuthorizedScopeSnapshot, agentcontract.ManifestDigest, "sha256:"+strings.Repeat("0", 64))
	doc, _, err := r.retainedMutationDocument(compatible, mutationResultReadInput{SourceTool: key.Tool, SourceOperation: key.OperationKind})
	if err != nil || string(doc) != prior.ResultPayload {
		t.Fatalf("compatible retained result refused: %v", err)
	}
	for _, vector := range []struct {
		name   string
		change func(*store.MutationIdempotencyRecord)
	}{
		{"malformed original", func(p *store.MutationIdempotencyRecord) { p.ResultPayload = `{"unexpected":"field"}` }},
		{"reference is not original", func(p *store.MutationIdempotencyRecord) { p.ResultPayload = `{"projection_reference":{}}` }},
		{"changed reference version", func(p *store.MutationIdempotencyRecord) {
			p.ChangedRefs = `[{"entity_kind":"work_item","id":"work-1","version":"9999"}]`
		}},
		{"missing changed references", func(p *store.MutationIdempotencyRecord) { p.ChangedRefs = `null` }},
		{"missing snapshot", func(p *store.MutationIdempotencyRecord) {
			p.AuthorizedScopeSnapshot = `{"product_id":"product-1","project_ids":["project-1"]}`
		}},
		{"missing snapshot producer", func(p *store.MutationIdempotencyRecord) {
			p.AuthorizedScopeSnapshot = strings.ReplaceAll(p.AuthorizedScopeSnapshot, agentcontract.ManifestDigest, "")
		}},
		{"snapshot intent authority", func(p *store.MutationIdempotencyRecord) {
			var obj map[string]any
			if err := json.Unmarshal([]byte(p.AuthorizedScopeSnapshot), &obj); err != nil {
				t.Fatal(err)
			}
			obj["mutation_response"].(map[string]any)["next_valid_intents"] = []any{}
			p.AuthorizedScopeSnapshot = string(retryJSON(obj))
		}},
		{"document intent authority", func(p *store.MutationIdempotencyRecord) {
			var obj map[string]any
			if err := json.Unmarshal([]byte(p.ResultPayload), &obj); err != nil {
				t.Fatal(err)
			}
			obj["next_valid_intents"] = []any{}
			p.ResultPayload = string(retryJSON(obj))
		}},
	} {
		t.Run(vector.name, func(t *testing.T) {
			p := prior
			vector.change(&p)
			newKey := key
			newKey.IdempotencyKey = "invalid-" + strings.ReplaceAll(vector.name, " ", "-")
			if err := source.Store.Transact(t.Context(), func(tx *store.Transaction) error {
				return store.InsertMutationIdempotencyTx(t.Context(), tx, store.MutationIdempotencyInsert{Key: newKey, CanonicalDigest: p.CanonicalDigest, OperationID: p.OperationID, ResultPayload: p.ResultPayload, ChangedRefs: p.ChangedRefs, AuthorizedScopeSnapshot: p.AuthorizedScopeSnapshot, ResultEventIDs: p.ResultEventIDs, ObservedAt: fixedTime()})
			}); err != nil {
				t.Fatal(err)
			}
			before := premiseConsentSnapshot(t, source.Store)
			out, err := r.readMutationResult(t.Context(), base, retryJSON(map[string]any{"source_tool": key.Tool, "source_operation": key.OperationKind, "idempotency_key": newKey.IdempotencyKey, "digest": mutationDocumentDigest([]byte(p.ResultPayload))}))
			kind, recovery := "operation_conflict", "reconcile_operation"
			if vector.name == "missing snapshot" || vector.name == "missing snapshot producer" {
				kind, recovery = "malformed_response", "contact_operator"
			}
			if err != nil || out.Error == nil || out.Error.Kind != kind || out.Error.RecoveryAction.Kind != recovery || out.Error.EffectState != agentcontract.EffectNone || len(out.Result) != 0 {
				t.Fatalf("untrusted document exposed: %+v %v", out.Error, err)
			}
			if !reflect.DeepEqual(before, premiseConsentSnapshot(t, source.Store)) {
				t.Fatal("rejected read changed state")
			}
		})
	}
}

func TestMutationResultRetainedBytesScopeAndReplay(t *testing.T) {
	source, grant, raw, first := committedWorkflowReplay(t)
	key := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: source.Tool, OperationKind: source.Operation, IdempotencyKey: "retained-replay"}
	prior, found, err := source.Store.LookupMutationIdempotency(t.Context(), key)
	if err != nil || !found {
		t.Fatalf("missing receipt: %t %v", found, err)
	}
	r := runtime{Store: source.Store, Tool: "concord_work_trace", Operation: "mutation_result", Envelope: source.Envelope, Reader: grant}
	base := agentcontract.NewBase("retained-result", r.Tool, r.Operation)
	input := map[string]any{"source_tool": key.Tool, "source_operation": key.OperationKind, "idempotency_key": key.IdempotencyKey, "digest": mutationDocumentDigest([]byte(prior.ResultPayload))}
	read := func(reader runtime, in map[string]any) agentcontract.Envelope {
		out, err := reader.readMutationResult(t.Context(), base, retryJSON(in))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := premiseConsentSnapshot(t, source.Store)
	out := read(r, input)
	var fragment mutationFragmentPage
	if err := json.Unmarshal(out.Result, &fragment); err != nil || out.Error != nil || !fragment.DocumentComplete || out.NextCursor != nil || fragment.Offset != 0 || fragment.FragmentBytes != len(fragment.Data) || fragment.TotalBytes != len(prior.ResultPayload) || fragment.Digest != input["digest"] || fragment.Data != prior.ResultPayload {
		t.Fatalf("retained bytes changed: %+v %+v %v", fragment, out.Error, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*runtime, map[string]any)
		kind   string
	}{
		{"digest", func(_ *runtime, in map[string]any) { in["digest"] = "sha256:" + strings.Repeat("0", 64) }, "invalid_input"},
		{"principal", func(r *runtime, _ map[string]any) { r.Reader.PrincipalRef = "other" }, "unknown_scope"},
		{"missing principal", func(r *runtime, _ map[string]any) { r.Reader.PrincipalRef = "" }, "unauthorized"},
		{"product grant", func(r *runtime, _ map[string]any) { r.Reader.ProductScope = nil }, "unauthorized"},
		{"project grant", func(r *runtime, _ map[string]any) { r.Reader.ProjectScope = nil }, "unauthorized"},
		{"product scope", func(r *runtime, _ map[string]any) { r.Envelope.SelectedProductID = "other" }, "unauthorized"},
		{"project scope", func(r *runtime, _ map[string]any) { r.Envelope.AmbientProjectID = "other" }, "unauthorized"},
		{"source pair", func(_ *runtime, in map[string]any) { in["source_tool"] = "concord_work_browse" }, "invalid_input"},
		{"unknown field", func(_ *runtime, in map[string]any) { in["offset"] = 0 }, "invalid_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := r
			in := map[string]any{}
			for k, v := range input {
				in[k] = v
			}
			tc.change(&reader, in)
			result := read(reader, in)
			if result.Error == nil || result.Error.Kind != tc.kind || result.Error.EffectState != agentcontract.EffectNone || len(result.Result) != 0 {
				t.Fatalf("unexpected refusal: %+v", result.Error)
			}
		})
	}
	if !reflect.DeepEqual(before, premiseConsentSnapshot(t, source.Store)) {
		t.Fatal("read changed state")
	}
	largeBase := base
	for range 23 {
		largeBase.EvidenceRefs = append(largeBase.EvidenceRefs, agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)})
	}
	largeBase.EvidenceRefs = append(largeBase.EvidenceRefs, agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 500)})
	pagedInput := map[string]any{}
	for k, v := range input {
		pagedInput[k] = v
	}
	var document []byte
	pages := 0
	for {
		page, err := r.readMutationResult(t.Context(), largeBase, retryJSON(pagedInput))
		if err != nil || page.Error != nil {
			t.Fatalf("retained page: %+v %v", page.Error, err)
		}
		assertBoundedRead(t, page)
		var part mutationFragmentPage
		if err := json.Unmarshal(page.Result, &part); err != nil || part.Offset != len(document) || part.FragmentBytes != len(part.Data) || part.Digest != input["digest"] {
			t.Fatalf("bad fragment: %+v %v", part, err)
		}
		document = append(document, part.Data...)
		pages++
		if part.DocumentComplete {
			if page.NextCursor != nil {
				t.Fatal("complete document has continuation")
			}
			break
		}
		if page.NextCursor == nil {
			t.Fatal("incomplete document lacks continuation")
		}
		if pages == 1 {
			bad := map[string]any{}
			for k, v := range input {
				bad[k] = v
			}
			bad["page"] = map[string]any{"cursor": *page.NextCursor + "!"}
			refusal, err := r.readMutationResult(t.Context(), largeBase, retryJSON(bad))
			if err != nil {
				refusal = failureEnvelope(largeBase, err)
			}
			if refusal.Error == nil || refusal.Error.Kind != "invalid_cursor" || len(refusal.Result) != 0 {
				t.Fatalf("tampered continuation: %+v", refusal.Error)
			}
		}
		pagedInput["page"] = map[string]any{"cursor": *page.NextCursor}
		if pages > 100 {
			t.Fatal("projection did not finish")
		}
	}
	if pages < 2 || string(document) != prior.ResultPayload || mutationDocumentDigest(document) != input["digest"] {
		t.Fatalf("retained reconstruction differs: %d pages, %d bytes", pages, len(document))
	}
	replay, err := Dispatch(t.Context(), source.Store, source.Authority, InvokeRequest{Tool: source.Tool, Operation: source.Operation, Input: raw}, source.Envelope)
	if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) {
		t.Fatalf("replay changed retained result: %+v %v", replay.Error, err)
	}
	if !reflect.DeepEqual(before, premiseConsentSnapshot(t, source.Store)) {
		t.Fatal("replay repeated the effect")
	}
}
