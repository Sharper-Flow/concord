package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestMutationProjectionFailedAttemptMutations(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.recordJob(t)
	job := f.read(t).Pinned.Jobs[0]
	const attempt = "attempt:oversized-failure"
	f.startWorker(t, "implement", attempt)
	// HTML escaping expands this legal terminal detail in each required copy
	// of the correction context. No content may be discarded to fit the wire.
	detail := strings.Repeat("<", 4096)
	failure := agentWorkerFailedAttributedEvent("oversized-failure", store.WorkerFailedPayload{
		AttemptID: attempt, ReadbackModel: "openai/test-model", FailureKind: store.WorkerFailureWorkerError, Detail: detail,
	})
	failure.SubjectID, failure.OccurredAt = f.work, time.Now().UTC()
	if err := f.s.Transact(t.Context(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(t.Context(), tx, failure)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ExpandTrustedClientPolicy(t.Context(), f.env.ClientRef, TrustedClientPolicy{Capabilities: []agentcontract.Capability{"work_relate"}}); err != nil {
		t.Fatal(err)
	}
	f.env.ScopeVersion, _, _ = f.s.ScopeVersion(t.Context(), "project-1")
	if err := seedActiveBroadcastRecipient(t.Context(), f.s, "work-projection-recipient"); err != nil {
		t.Fatal(err)
	}
	state := f.read(t)
	if state.Pinned.Pin.Correction == nil || state.Pinned.Pin.Correction.FailureDetail != detail {
		t.Fatal("failed attempt did not retain its complete correction context")
	}
	for _, vector := range []struct {
		name, tool, operation string
		fields                map[string]any
	}{
		{"job correction", "concord_work_transition", "workflow_action", map[string]any{
			"action_id": "record_worker_job", "fields": map[string]any{"job_id": job.JobID, "objective": "Correct the failed job without losing its failure context.", "stopping_condition": job.StoppingCondition,
				"ready": true, "checks": job.Checks, "path_scope": job.PathScope, "acceptance_oracle": job.AcceptanceOracle,
				"readiness_evidence": job.AcceptanceOracle.Controls[0].ReadinessEvidenceRefs},
		}},
		{"message", "concord_work_relate", "message_send", map[string]any{"recipient_work_id": "work-projection-recipient", "body": "The failed job needs its recorded correction."}},
	} {
		t.Run(vector.name, func(t *testing.T) {
			before := f.read(t).Pinned.Pin.Version
			input := vector.fields
			input["work_id"], input["expected_version"], input["idempotency_key"] = f.work, before, "project-"+vector.operation
			// Fail the receipt after the effect was prepared. Both paths must
			// roll back the complete write, not publish an unreadable reference.
			triggerAction := "INSERT"
			if vector.operation == "workflow_action" {
				triggerAction = "UPDATE"
			}
			if _, err := f.s.DatabaseForTesting().Exec(fmt.Sprintf(`CREATE TEMP TRIGGER reject_projection_receipt BEFORE %s ON idempotency_records WHEN NEW.idempotency_key='%s' BEGIN SELECT RAISE(ABORT,'synthetic projection receipt failure'); END`, triggerAction, input["idempotency_key"])); err != nil {
				t.Fatal(err)
			}
			beforeRefusal := mutationProjectionState(t, f.s)
			refused := f.call(t, vector.tool, vector.operation, input)
			if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.EffectState != agentcontract.EffectNone || !reflect.DeepEqual(beforeRefusal, mutationProjectionState(t, f.s)) {
				t.Fatalf("failed retention did not roll back: %+v", refused.Error)
			}
			if _, err := f.s.DatabaseForTesting().Exec(`DROP TRIGGER reject_projection_receipt`); err != nil {
				t.Fatal(err)
			}
			response := f.call(t, vector.tool, vector.operation, input)
			if response.Outcome != agentcontract.OutcomeOK {
				if current := f.read(t).Pinned.Pin.Version; current != before {
					t.Fatalf("refusal changed work: before=%d after=%d", before, current)
				}
				t.Fatalf("oversized %s must succeed, got %+v", vector.operation, response.Error)
			}
			if response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].ID != f.work || response.NextValidIntents == nil || len(*response.NextValidIntents) != 0 {
				t.Fatalf("reference envelope lost changed refs or granted intents: %+v", response)
			}
			if len(response.Warnings) == 0 || response.Warnings[len(response.Warnings)-1].Kind != "mutation_result_projected" {
				t.Fatal("missing projection warning")
			}
			var projected struct {
				Reference struct {
					Digest string `json:"digest"`
					Total  int    `json:"total_bytes"`
					Read   struct {
						Input map[string]any `json:"input"`
					} `json:"read_pointer"`
				} `json:"projection_reference"`
			}
			if err := json.Unmarshal(response.Result, &projected); err != nil || projected.Reference.Total <= agentcontract.MaxResultEnvelopeBytes {
				t.Fatalf("missing oversized reference: %s (%v)", response.Result, err)
			}
			key := store.MutationIdempotencyKey{PrincipalRef: f.env.PrincipalRef, Tool: vector.tool, OperationKind: vector.operation, IdempotencyKey: input["idempotency_key"].(string)}
			record, found, err := f.s.LookupMutationIdempotency(t.Context(), key)
			if err != nil || !found || len(record.ResultPayload) != projected.Reference.Total {
				t.Fatalf("original result not retained atomically: found=%t err=%v", found, err)
			}
			var original struct {
				Pins    []store.WorkPin        `json:"work_pins"`
				Intents []mutationResultIntent `json:"next_valid_intents"`
			}
			committed, err := strconv.ParseInt((*response.ChangedRefs)[0].Version, 10, 64)
			if err != nil || committed <= before {
				t.Fatalf("invalid committed version: %d %v", committed, err)
			}
			if err := json.Unmarshal([]byte(record.ResultPayload), &original); err != nil || len(original.Pins) != 1 || original.Pins[0].Version != committed || original.Pins[0].Correction.FailureDetail != detail || !reflect.DeepEqual(original.Intents, mutationResultIntents(nextIntentsFromPin(original.Pins[0]))) {
				t.Fatalf("incomplete original pin or intents: err=%v", err)
			}
			if err := agentcontract.ValidateOperationPayload(vector.tool, vector.operation, []byte(record.ResultPayload), true); err != nil {
				t.Fatal(err)
			}
			driftWorkPinVersion(t, f.s, f.work)
			beforeReplay := mutationProjectionState(t, f.s)
			replay := f.call(t, vector.tool, vector.operation, input)
			if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(response.Result) || !reflect.DeepEqual(replay.ChangedRefs, response.ChangedRefs) {
				t.Fatalf("replay changed original projection: %+v", replay.Error)
			}
			// Replay only touches observation metadata, never a business effect.
			afterReplay := mutationProjectionState(t, f.s)
			delete(beforeReplay, "idempotency_observations")
			delete(afterReplay, "idempotency_observations")
			if !reflect.DeepEqual(beforeReplay, afterReplay) {
				t.Fatal("projection replay repeated an effect")
			}
			readInput := projected.Reference.Read.Input
			readBefore := mutationProjectionState(t, f.s)
			var aliasPage agentcontract.Envelope
			for i, variant := range []map[string]any{
				{"page": map[string]any{"limit": 5}},
				{"limit": 5},
				{"limit": 5, "page": map[string]any{"limit": 3}},
			} {
				input := maps.Clone(readInput)
				maps.Copy(input, variant)
				response := f.call(t, "concord_work_trace", "mutation_result", input)
				if response.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("pagination alias %d refused: %+v", i, response.Error)
				}
				if i == 0 {
					aliasPage = response
				} else if !bytes.Equal(response.Result, aliasPage.Result) || !reflect.DeepEqual(response.NextCursor, aliasPage.NextCursor) {
					t.Fatalf("pagination alias %d changed the page or its effective-limit cursor binding", i)
				}
			}
			var document []byte
			for pages := 0; pages < 32; pages++ {
				page := f.call(t, "concord_work_trace", "mutation_result", readInput)
				if page.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("fragment read: %+v", page.Error)
				}
				wire, err := json.Marshal(page)
				if err != nil || len(wire) > agentcontract.MaxResultEnvelopeBytes {
					t.Fatalf("fragment exceeds wire cap: %d %v", len(wire), err)
				}
				var fragment struct {
					Data     string `json:"data"`
					Offset   int    `json:"offset"`
					Bytes    int    `json:"fragment_bytes"`
					Complete bool   `json:"document_complete"`
				}
				if err := json.Unmarshal(page.Result, &fragment); err != nil || fragment.Offset != len(document) || fragment.Bytes != len(fragment.Data) || fragment.Bytes == 0 {
					t.Fatalf("invalid fragment: %v", err)
				}
				document = append(document, fragment.Data...)
				if fragment.Complete {
					if page.NextCursor != nil {
						t.Fatal("complete document has cursor")
					}
					break
				}
				if page.NextCursor == nil {
					t.Fatal("missing continuation")
				}
				if pages == 0 {
					requireMutationProjectionReadRefusals(t, f, readInput, *page.NextCursor)
					// A later work version cannot change an immutable document or
					// invalidate its signed continuation.
					driftWorkPinVersion(t, f.s, f.work)
					readBefore = mutationProjectionState(t, f.s)
				}
				readInput["page"] = map[string]any{"cursor": *page.NextCursor}
			}
			digest := sha256.Sum256(document)
			if string(document) != record.ResultPayload || "sha256:"+hex.EncodeToString(digest[:]) != projected.Reference.Digest {
				t.Fatal("read did not reconstruct exact retained bytes")
			}
			if !reflect.DeepEqual(readBefore, mutationProjectionState(t, f.s)) {
				t.Fatal("projection read changed state")
			}
		})
	}
}

func mutationProjectionState(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	state := premiseConsentSnapshot(t, s)
	for name, query := range map[string]string{
		"messages":                 `SELECT json_group_array(json_array(message_id,sender_work_id,recipient_work_id,body,state)) FROM (SELECT * FROM work_messages ORDER BY message_id)`,
		"idempotency":              `SELECT json_group_array(json_array(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_payload,changed_refs,authorized_scope_snapshot)) FROM (SELECT * FROM idempotency_records ORDER BY principal_ref,tool,operation_kind,idempotency_key)`,
		"idempotency_observations": `SELECT json_group_array(json_array(idempotency_key,replayed_count,last_observed_at)) FROM (SELECT * FROM idempotency_records ORDER BY principal_ref,tool,operation_kind,idempotency_key)`,
	} {
		var value string
		if err := s.DatabaseForTesting().QueryRow(query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		state[name] = value
	}
	return state
}

func requireMutationProjectionReadRefusals(t *testing.T, f *spineFixture, original map[string]any, cursor string) {
	t.Helper()
	r := runtime{Store: f.s, Authority: f.service, Tool: "concord_work_trace", Operation: "mutation_result", Envelope: f.env,
		Reader: Authority{PrincipalRef: f.env.PrincipalRef, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}
	base := agentcontract.NewBase("projection-refusals", r.Tool, r.Operation)
	key := store.MutationIdempotencyKey{PrincipalRef: r.Reader.PrincipalRef, Tool: original["source_tool"].(string), OperationKind: original["source_operation"].(string), IdempotencyKey: original["idempotency_key"].(string)}
	prior, found, err := f.s.LookupMutationIdempotency(t.Context(), key)
	if err != nil || !found {
		t.Fatalf("projection fixture receipt missing: %t %v", found, err)
	}
	for _, clone := range []store.MutationIdempotencyKey{
		{PrincipalRef: "another-owning-principal", Tool: key.Tool, OperationKind: key.OperationKind, IdempotencyKey: key.IdempotencyKey},
		{PrincipalRef: key.PrincipalRef, Tool: key.Tool, OperationKind: key.OperationKind, IdempotencyKey: key.IdempotencyKey + "-other"},
	} {
		if err := f.s.Transact(t.Context(), func(tx *store.Transaction) error {
			return store.InsertMutationIdempotencyTx(t.Context(), tx, store.MutationIdempotencyInsert{Key: clone, CanonicalDigest: prior.CanonicalDigest, OperationID: prior.OperationID, ResultPayload: prior.ResultPayload, ChangedRefs: prior.ChangedRefs, AuthorizedScopeSnapshot: prior.AuthorizedScopeSnapshot, ResultEventIDs: prior.ResultEventIDs, ObservedAt: fixedTime()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, vector := range []struct {
		name, kind string
		change     func(*runtime, map[string]any)
	}{
		{"tamper", "invalid_cursor", func(_ *runtime, in map[string]any) {
			in["page"] = map[string]any{"cursor": cursor[:len(cursor)/2] + "!" + cursor[len(cursor)/2:]}
		}},
		{"filter", "invalid_cursor", func(_ *runtime, in map[string]any) { in["page"] = map[string]any{"cursor": cursor, "limit": 2} }},
		{"digest", "invalid_input", func(_ *runtime, in map[string]any) { in["digest"] = "sha256:" + strings.Repeat("0", 64) }},
		{"continuation digest", "invalid_cursor", func(_ *runtime, in map[string]any) {
			in["digest"] = "sha256:" + strings.Repeat("0", 64)
			in["page"] = map[string]any{"cursor": cursor}
		}},
		{"principal", "unknown_scope", func(r *runtime, _ map[string]any) { r.Reader.PrincipalRef = "another-principal" }},
		{"principal cursor binding", "invalid_cursor", func(r *runtime, in map[string]any) {
			r.Reader.PrincipalRef = "another-owning-principal"
			in["page"] = map[string]any{"cursor": cursor}
		}},
		{"key cursor binding", "invalid_cursor", func(_ *runtime, in map[string]any) {
			in["idempotency_key"] = key.IdempotencyKey + "-other"
			in["page"] = map[string]any{"cursor": cursor}
		}},
		{"missing principal", "unauthorized", func(r *runtime, _ map[string]any) { r.Reader.PrincipalRef = "" }},
		{"Product grant", "unauthorized", func(r *runtime, _ map[string]any) { r.Reader.ProductScope = nil }},
		{"Project grant", "unauthorized", func(r *runtime, _ map[string]any) { r.Reader.ProjectScope = nil }},
		{"Product scope", "unauthorized", func(r *runtime, _ map[string]any) { r.Envelope.SelectedProductID = "another-product" }},
		{"Project scope", "unauthorized", func(r *runtime, _ map[string]any) { r.Envelope.AmbientProjectID = "another-project" }},
		{"source pair", "invalid_input", func(_ *runtime, in map[string]any) { in["source_tool"] = "concord_work_browse" }},
		{"crossed source pair", "invalid_input", func(_ *runtime, in map[string]any) {
			if in["source_operation"] == "message_send" {
				in["source_tool"] = "concord_work_transition"
			} else {
				in["source_tool"] = "concord_work_relate"
			}
		}},
		{"unknown field", "invalid_input", func(_ *runtime, in map[string]any) { in["offset"] = 0 }},
	} {
		t.Run(vector.name, func(t *testing.T) {
			reader := r
			in := map[string]any{}
			for k, v := range original {
				in[k] = v
			}
			vector.change(&reader, in)
			before := mutationProjectionState(t, f.s)
			response, err := reader.readMutationResult(t.Context(), base, retryJSON(in))
			if err != nil {
				response = failureEnvelope(base, err)
			}
			if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != vector.kind || response.Error.EffectState != agentcontract.EffectNone || len(response.Result) != 0 {
				t.Fatalf("read refusal=%+v error=%v", response, err)
			}
			if vector.name == "digest" && response.Error.RecoveryAction.Kind != "correct_request" {
				t.Fatalf("wrong initial digest must name request correction: %+v", response.Error)
			}
			if _, err := json.Marshal(response); err != nil {
				t.Fatalf("unencodable refusal: %v", err)
			}
			if !reflect.DeepEqual(before, mutationProjectionState(t, f.s)) {
				t.Fatal("refused read changed state")
			}
		})
	}
	// A forged hidden principal cannot select a partition. The authenticated
	// Reader still selects exactly the original owner's bytes.
	r.Envelope.PrincipalRef = "another-principal"
	before := mutationProjectionState(t, f.s)
	response, err := r.readMutationResult(t.Context(), base, retryJSON(original))
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("read trusted the hidden principal over Reader: %+v %v", response.Error, err)
	}
	if !reflect.DeepEqual(before, mutationProjectionState(t, f.s)) {
		t.Fatal("successful read changed state")
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
	document, _, err := r.retainedMutationDocument(compatible, mutationResultReadInput{SourceTool: key.Tool, SourceOperation: key.OperationKind})
	if err != nil || string(document) != prior.ResultPayload {
		t.Fatalf("current-schema retained facts were rejected after manifest change: %v", err)
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
			var object map[string]any
			if err := json.Unmarshal([]byte(p.AuthorizedScopeSnapshot), &object); err != nil {
				t.Fatal(err)
			}
			object["mutation_response"].(map[string]any)["next_valid_intents"] = []any{}
			p.AuthorizedScopeSnapshot = string(retryJSON(object))
		}},
		{"document intent authority", func(p *store.MutationIdempotencyRecord) {
			var object map[string]any
			if err := json.Unmarshal([]byte(p.ResultPayload), &object); err != nil {
				t.Fatal(err)
			}
			object["next_valid_intents"] = []any{}
			p.ResultPayload = string(retryJSON(object))
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
			before := mutationProjectionState(t, source.Store)
			response, err := r.readMutationResult(t.Context(), base, retryJSON(map[string]any{"source_tool": key.Tool, "source_operation": key.OperationKind, "idempotency_key": newKey.IdempotencyKey, "digest": mutationDocumentDigest([]byte(p.ResultPayload))}))
			if err != nil || response.Error == nil || response.Error.Kind != "operation_conflict" || len(response.Result) != 0 {
				t.Fatalf("untrusted document was exposed: %+v %v", response.Error, err)
			}
			if !reflect.DeepEqual(before, mutationProjectionState(t, source.Store)) {
				t.Fatal("rejected retained document changed state")
			}
		})
	}
}

func TestMutationProjectionRequiresRetainedIdentity(t *testing.T) {
	_, _, pin := enrichmentPinFixture(t)
	pin.Correction = &store.WorkflowCorrectionContext{Disposition: "failed", PredicateIDs: []string{"predicate:primary"}, EvidenceRefs: []string{},
		Diagnosis: strings.Repeat("<", 4096), Strategy: strings.Repeat("<", 4096), FailureDetail: strings.Repeat("<", 4096), SourceEventID: "failed-attempt-context", SourceEventSeq: 1}
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: pin.WorkID, Version: strconv.FormatInt(pin.Version, 10)}}
	payload, intents, err := enrichMutationPayload(mutationPayload(changed, nil), []store.WorkPin{pin})
	if err != nil {
		t.Fatal(err)
	}
	for _, vector := range []struct {
		name, tool, operation, key string
		projected                  bool
	}{
		{"no identity", "concord_work_transition", "workflow_action", "", false},
		{"retained workflow identity", "concord_work_transition", "workflow_action", "original-key", true},
		{"retained message identity", "concord_work_relate", "message_send", "original-key", true},
		{"unsupported operation", "concord_work_relate", "message_withdraw", "original-key", false},
	} {
		t.Run(vector.name, func(t *testing.T) {
			r := runtime{Tool: vector.tool, Operation: vector.operation, mutationIdempotencyKey: vector.key}
			base := agentcontract.NewBase("projection-identity", r.Tool, r.Operation)
			response := r.mutationResult(base, payload, changed, intents)
			if vector.projected {
				if response.Outcome != agentcontract.OutcomeOK || response.NextValidIntents == nil || len(*response.NextValidIntents) != 0 {
					t.Fatalf("projection=%+v", response.Error)
				}
			} else if response.Error == nil || response.Error.Kind != "limit_exceeded" || response.Error.EffectState != agentcontract.EffectNone {
				t.Fatalf("unretained or unsupported operation projected: %+v", response.Error)
			}
			if _, err := json.Marshal(response); err != nil {
				t.Fatal(err)
			}
		})
	}
}
