package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func mutationProjectionSupported(tool, operation string) bool {
	return tool == "concord_work_relate" && operation == "message_send" || tool == "concord_work_transition" && operation == "workflow_action"
}

func mutationDocumentDigest(document []byte) string {
	sum := sha256.Sum256(document)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// validateMutationDocument accepts only a complete original document, never a
// reference that would recursively defer its authority to another read.
func (r runtime) validateMutationDocument(payload json.RawMessage, changed []agentcontract.ChangedRef, pinless []string, omissions []agentcontract.Notice) ([]store.WorkPin, error) {
	if err := agentcontract.ValidateOperationPayload(r.Tool, r.Operation, payload, true); err != nil {
		return nil, err
	}
	if mutationProjectionSupported(r.Tool, r.Operation) {
		if err := payloadschema.Validate("mutation_result", payload); err != nil {
			return nil, err
		}
	}
	var document struct {
		Pins    []store.WorkPin            `json:"work_pins"`
		Refs    []mutationResultChangedRef `json:"changed_refs"`
		Intents []mutationResultIntent     `json:"next_valid_intents"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, err
	}
	refs := append([]agentcontract.ChangedRef{}, changed...)
	for _, ref := range document.Refs {
		refs = append(refs, agentcontract.ChangedRef{EntityKind: ref.EntityKind, ID: ref.ID, Version: strconv.FormatInt(ref.Version, 10)})
	}
	if err := validateMutationPins(refs, document.Pins, pinless, omissions); err != nil {
		return nil, err
	}
	if mutationProjectionSupported(r.Tool, r.Operation) && len(document.Pins) > 0 {
		derived, _ := json.Marshal(mutationResultIntents(nextIntentsFromPin(document.Pins[0])))
		retained, _ := json.Marshal(document.Intents)
		if !bytes.Equal(derived, retained) {
			return nil, errors.New("mutation document intents do not derive from its retained pin")
		}
	}
	return document.Pins, nil
}

func (r runtime) projectMutationResult(base agentcontract.Envelope, payload json.RawMessage, changed []agentcontract.ChangedRef) agentcontract.Envelope {
	type workRef struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	refs := make([]workRef, 0, len(changed))
	for _, ref := range workItemRefs(changed) {
		version, err := strconv.ParseInt(ref.Version, 10, 64)
		if err != nil {
			return coreError(base, "malformed_response", "mutation work reference version is invalid", "contact_operator", false)
		}
		refs = append(refs, workRef{ref.ID, version})
	}
	digest := mutationDocumentDigest(payload)
	reference, err := json.Marshal(map[string]any{"projection_reference": map[string]any{
		"encoding": "json-fragment", "digest": digest, "total_bytes": len(payload),
		"source_tool": r.Tool, "source_operation": r.Operation, "work_refs": refs,
		"read_pointer": map[string]any{"tool": "concord_work_trace", "operation": "mutation_result", "input": map[string]any{
			"source_tool": r.Tool, "source_operation": r.Operation, "idempotency_key": r.mutationIdempotencyKey, "digest": digest,
		}},
	}})
	if err != nil {
		return coreError(base, "malformed_response", err.Error(), "contact_operator", false)
	}
	if err := agentcontract.ValidateOperationPayload(r.Tool, r.Operation, reference, true); err != nil {
		return coreError(base, "malformed_response", err.Error(), "contact_operator", false)
	}
	base.Warnings = append(base.Warnings, agentcontract.Notice{Kind: "mutation_result_projected"})
	response := agentcontract.NewOKMutation(base, reference, changed, nil)
	size, err := readEnvelopeSize(response)
	if err != nil {
		return coreError(base, "malformed_response", err.Error(), "contact_operator", false)
	}
	if size > agentcontract.MaxResultEnvelopeBytes {
		return coreError(base, "limit_exceeded", fmt.Sprintf("mutation reference envelope exceeds %d bytes; contact the operator for producer repair", agentcontract.MaxResultEnvelopeBytes), "contact_operator", false)
	}
	return response
}

type mutationResultReadInput struct {
	SourceTool      string    `json:"source_tool"`
	SourceOperation string    `json:"source_operation"`
	IdempotencyKey  string    `json:"idempotency_key"`
	Digest          string    `json:"digest"`
	Page            pageInput `json:"page"`
}

type mutationFragmentPage struct {
	Encoding         string `json:"encoding"`
	Digest           string `json:"digest"`
	TotalBytes       int    `json:"total_bytes"`
	Offset           int    `json:"offset"`
	Data             string `json:"data"`
	FragmentBytes    int    `json:"fragment_bytes"`
	DocumentComplete bool   `json:"document_complete"`
}

func (r runtime) readMutationResult(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	if err := agentcontract.ValidateOperationPayload(r.Tool, r.Operation, input, false); err != nil {
		return coreError(base, "invalid_input", err.Error(), "correct_request", false), nil
	}
	var in mutationResultReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return coreError(base, "invalid_input", err.Error(), "correct_request", false), nil
	}
	if !mutationProjectionSupported(in.SourceTool, in.SourceOperation) {
		return coreError(base, "invalid_input", "unsupported mutation projection source", "correct_request", false), nil
	}
	// The trusted reader, not the hidden caller assertion, owns this partition.
	if r.Reader.PrincipalRef == "" {
		return coreError(base, "unauthorized", "mutation projection requires an authenticated reader", "contact_operator", false), nil
	}
	record, found, err := r.Store.LookupMutationIdempotency(ctx, store.MutationIdempotencyKey{
		PrincipalRef: r.Reader.PrincipalRef, Tool: in.SourceTool, OperationKind: in.SourceOperation, IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return base, err
	}
	if !found {
		return coreError(base, "unknown_scope", "retained mutation result is not available to this reader", "reread_entities", false), nil
	}
	scope, err := authorizedScopeFromSnapshot(record.AuthorizedScopeSnapshot)
	if err != nil || scope == nil || !scopeWithinAuthority(scope, r.Reader) {
		return coreError(base, "unauthorized", "original mutation scope is no longer authorized by the current grant", "contact_operator", false), nil
	}
	snapshot, err := retainedMutationResponse(record.AuthorizedScopeSnapshot)
	if err != nil {
		return coreError(base, "operation_conflict", err.Error(), "reconcile_operation", false), nil
	}
	resolved := snapshot.ResolvedScope
	if resolved == nil || agentcontract.ValidateScope(resolved) != nil || resolved.ProductID == "" || len(resolved.ProjectIDs) == 0 {
		return coreError(base, "operation_conflict", "retained mutation result has no complete scope snapshot", "reconcile_operation", false), nil
	}
	if resolved.ProductID != r.Envelope.SelectedProductID || !slices.Contains(resolved.ProjectIDs, r.Envelope.AmbientProjectID) || !scopeWithinAuthority(map[string]any{"product_id": resolved.ProductID, "project_ids": resolved.ProjectIDs}, r.Reader) {
		return coreError(base, "unauthorized", "retained mutation result belongs to another Product or Project scope", "contact_operator", false), nil
	}
	var changed []agentcontract.ChangedRef
	if err := json.Unmarshal([]byte(record.ChangedRefs), &changed); err != nil || changed == nil {
		return coreError(base, "operation_conflict", "retained mutation changed references are incomplete", "reconcile_operation", false), nil
	}
	doc := []byte(record.ResultPayload)
	source := runtime{Tool: in.SourceTool, Operation: in.SourceOperation}
	pins, err := source.validateMutationDocument(doc, changed, snapshot.PinlessWorkIDs, snapshot.Omissions)
	if err != nil {
		return coreError(base, "operation_conflict", "retained mutation result is not a complete valid original document", "reconcile_operation", false), nil
	}
	for _, pin := range pins {
		if !slices.Contains(r.Reader.ProjectScope, pin.ProjectID) || !slices.Contains(resolved.WorkIDs, pin.WorkID) {
			return coreError(base, "unauthorized", "retained mutation pin is outside the authorized response scope", "contact_operator", false), nil
		}
	}
	if len(pins) > 0 {
		derived, _ := json.Marshal(nextIntentsFromPin(pins[0]))
		retained, _ := json.Marshal(snapshot.NextValidIntents)
		if !bytes.Equal(derived, retained) {
			return coreError(base, "operation_conflict", "retained mutation response intents do not match the original pin", "reconcile_operation", false), nil
		}
	}
	if !utf8.Valid(doc) || mutationDocumentDigest(doc) != in.Digest {
		return coreError(base, "invalid_cursor", "retained mutation result digest does not match the requested document", "restart_query", false), nil
	}
	// Budget changes are not query changes. Page limit, principal, source and
	// document identity are; bind all of them without exposing the key in a cursor.
	bindingJSON, _ := json.Marshal([]any{r.Reader.PrincipalRef, in.SourceTool, in.SourceOperation, in.IdempotencyKey, in.Digest, in.Page.Limit})
	binding := mutationDocumentDigest(bindingJSON)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), binding, "mutation_result")
	if err != nil {
		return base, err
	}
	offset := 0
	if inner != "" {
		offset, err = strconv.Atoi(inner)
		if err != nil || offset <= 0 || offset >= len(doc) || !utf8.RuneStart(doc[offset]) {
			return coreError(base, "invalid_cursor", "mutation result cursor offset is invalid", "restart_query", false), nil
		}
	}
	base.ResolvedScope = resolved
	base.SourceVersionWatermark = []agentcontract.Watermark{{SourceKind: "product_memory", SourceID: record.OperationID, Version: binding}}
	base.OrderingKeys = []string{"offset"}
	render := func(n int) (agentcontract.Envelope, int, error) {
		end := offset + n
		response := base
		response.Outcome = agentcontract.OutcomeOK
		response.Result, err = json.Marshal(mutationFragmentPage{"json-fragment", in.Digest, len(doc), offset, string(doc[offset:end]), n, end == len(doc)})
		if err != nil {
			return base, 0, err
		}
		if end < len(doc) {
			next := strconv.Itoa(end)
			response.NextCursor = &next
		}
		if err := agentcontract.ValidateOperationPayload(r.Tool, r.Operation, response.Result, true); err != nil {
			return base, 0, err
		}
		response, err := r.wrapCursor(ctx, response, binding, "mutation_result")
		if err != nil {
			return base, 0, err
		}
		size, err := readEnvelopeSize(response)
		return response, size, err
	}
	length, err := continuityFragmentLength(doc, offset, func(n int) (bool, error) {
		_, size, err := render(n)
		return size <= agentcontract.MaxResultEnvelopeBytes, err
	})
	if err != nil {
		return base, err
	}
	if length == 0 {
		return coreError(base, "limit_exceeded", "mutation result fragment frame exceeds the envelope cap; contact the operator for producer repair", "contact_operator", false), nil
	}
	response, _, err := render(length)
	return response, err
}
