package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The per-read-kind handlers behind (runtime).read. Each holds one read's own
// decode-and-query logic; the dispatch in runtime.go routes tool.operation to
// one of these and refuses an unmatched operation. The shared scaffolding they
// call — decodeOperationInput, the ambient-product fallbacks, unwrapCursor and
// wrapCursor, failureEnvelope and resultEnvelope — stays in runtime.go.

func (r runtime) readProductResolve(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in productResolveInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" && in.ProjectID == "" {
		in.ProjectID = r.Envelope.AmbientProjectID
	}
	bindingInput := in
	bindingInput.Page.Cursor = nil
	binding, _ := json.Marshal(bindingInput)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	q, err := r.Store.QueryQ1(ctx, store.Q1Request{Product: in.ProductID, Project: in.ProjectID, Limit: effectiveLimit(in.Limit, in.Page), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q1(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, string(binding), "summary")
}

func (r runtime) readProductSnapshot(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in productSnapshotInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	// The unified TS3 §3 limit alias bounds the preview buckets when the
	// snapshot-native preview_limit is absent. TS3 pins preview at 20 items
	// per bucket, so the alias never lifts that cap.
	preview := in.PreviewLimit
	if preview == 0 {
		preview = effectiveLimit(in.Limit, in.Page)
	}
	if preview > 20 {
		preview = 20
	}
	q, err := r.Store.QueryQ2(ctx, store.Q2Request{Product: in.ProductID, ProjectIDs: in.ProjectIDs, PreviewLimit: preview})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.q2(base, q)
}

func (r runtime) readWorkList(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in workListInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	binding := pageBinding(in, func(p *workListInput) { p.Page.Cursor = nil })
	request := func(inner string, limit int) store.Q3Request {
		return store.Q3Request{Product: in.ProductID, LifecycleStates: nonEmpty(in.Lifecycle), Limit: limit, Cursor: inner, Kind: in.Kind, ProjectIDs: in.ProjectIDs, WorkIDs: in.WorkIDs, TagIDs: in.TagIDs, PriorityMin: in.PriorityMin, PriorityMax: in.PriorityMax, TerminalSince: in.TerminalSince, Detail: in.Detail}
	}
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), binding, "summary")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	if len(inner) > 0 && inner[0] == '{' {
		var cursor workListProjectionCursor
		if in.Detail != "full" || decodeOperationInput([]byte(inner), &cursor) != nil || cursor.Version != 1 || !agentcontract.ValidSHA256Proof(cursor.Digest) || cursor.NextByteOffset < 1 {
			return coreError(base, "invalid_cursor", "work-list projection cursor is malformed", "restart_query", false), nil
		}
		q, err := r.Store.QueryQ3(ctx, request(cursor.ListCursor, 1))
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.workListProjectionPage(ctx, base, binding, cursor, q)
	}
	return pagedQuery(r, ctx, base, in.Page, effectiveLimit(in.Limit, in.Page), binding, request, r.Store.QueryQ3, r.q3,
		func(req store.Q3Request, q store.Q3Result, size int) (agentcontract.Envelope, error) {
			if in.Detail != "full" || len(q.Items) == 0 {
				return readSizeRefusal(base, size), nil
			}
			single, err := q.LimitPage(req, 1)
			if err != nil {
				return failureEnvelope(base, err), nil
			}
			return r.workListProjectionPage(ctx, base, binding, workListProjectionCursor{Version: 1, ListCursor: req.Cursor}, single)
		})
}

// pagedQuery runs the shape every paged read handler shares: resolve the
// inner cursor from the caller's page token against the cursor-free binding
// of the same input, query, build the response, and wrap a fresh cursor
// around it bound to that same input. request receives the resolved inner
// cursor; query and respond are the handler's store call and response
// builder. Byte fitting selects complete prefixes of one captured store page;
// no fit iteration reads a different snapshot. The public binding keeps the
// caller's limit, so the cursor works with the unchanged request.
// An oversized handler may transport that same capture when no complete item fits.
func pagedQuery[Req any, Resp interface{ LimitPage(Req, int) (Resp, error) }](r runtime, ctx context.Context, base agentcontract.Envelope, page pageInput, limit int, binding string, request func(inner string, limit int) Req, query func(context.Context, Req) (Resp, error), respond func(agentcontract.Envelope, Resp) (agentcontract.Envelope, error), oversized ...func(Req, Resp, int) (agentcontract.Envelope, error)) (agentcontract.Envelope, error) {
	inner, err := r.unwrapCursor(ctx, cursorValue(page), binding, "summary")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	if limit == 0 {
		limit = 20
	}
	requestedLimit := limit
	req := request(inner, limit)
	q, err := query(ctx, req)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	var best agentcontract.Envelope
	var size int
	low, high := 0, requestedLimit
	for low < high {
		candidate, err := q.LimitPage(req, limit)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := respond(base, candidate)
		if err != nil {
			return response, err
		}
		response, err = r.wrapCursor(ctx, response, binding, "summary")
		if err != nil {
			return response, err
		}
		if limit < requestedLimit && response.NextCursor != nil {
			if len(response.Omissions) >= agentcontract.MaxNotices {
				return coreError(base, "limit_exceeded", "byte-bounded page needs a continuation notice but the omission limit is exhausted", "reduce_limit", false), nil
			}
			response.Omissions = append(response.Omissions, agentcontract.Notice{Kind: "byte_bounded_page", Details: map[string]any{"requested_limit": requestedLimit, "effective_limit": limit, "maximum_bytes": agentcontract.MaxResultEnvelopeBytes}})
		}
		size, err = readEnvelopeSize(response)
		if err != nil {
			return response, err
		}
		if response.Outcome != agentcontract.OutcomeOK {
			return response, nil
		}
		if size <= agentcontract.MaxResultEnvelopeBytes {
			best, low = response, limit
		} else {
			high = limit - 1
		}
		limit = low + (high-low+1)/2
	}
	if low == 0 {
		if len(oversized) > 0 {
			return oversized[0](req, q, size)
		}
		return readSizeRefusal(base, size), nil
	}
	return best, nil
}

// pageBinding returns the JSON binding of a paged read input with its cursor
// cleared, so a rebound page cannot smuggle a foreign cursor. The page
// itself keeps the caller's cursor for resolution.
func pageBinding[T any](in T, clearCursor func(*T)) string {
	clearCursor(&in)
	binding, _ := json.Marshal(in)
	return string(binding)
}

func (r runtime) readWorkReady(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in workReadyInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	return pagedQuery(r, ctx, base, in.Page, effectiveLimit(in.Limit, in.Page), pageBinding(in, func(p *workReadyInput) { p.Page.Cursor = nil }),
		func(inner string, limit int) store.Q5Request {
			return store.Q5Request{Product: in.ProductID, Project: in.ProjectID, Kind: in.Kind, Limit: limit, Cursor: inner}
		},
		r.Store.QueryQ5, r.q5)
}

func (r runtime) readWorkBlocked(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in workBlockedInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	return pagedQuery(r, ctx, base, in.Page, effectiveLimit(in.Limit, in.Page), pageBinding(in, func(p *workBlockedInput) { p.Page.Cursor = nil }),
		func(inner string, limit int) store.Q4Request {
			return store.Q4Request{Product: in.ProductID, Project: in.ProjectID, Work: in.WorkID, Kind: in.Kind, Depth: in.Depth, Limit: limit, Cursor: inner}
		},
		r.Store.QueryQ4, r.q4)
}

func (r runtime) readWorkScope(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in workScopeInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	return pagedQuery(r, ctx, base, in.Page, effectiveLimit(in.Limit, in.Page), pageBinding(in, func(p *workScopeInput) { p.Page.Cursor = nil }),
		func(inner string, limit int) store.Q6Request {
			return store.Q6Request{Product: in.ProductID, Project: in.ProjectID, Work: in.WorkID, Limit: limit, Cursor: inner}
		},
		r.Store.QueryQ6, r.q6)
}

func (r runtime) readWorkMessages(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in messagesInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	messages, err := r.Store.MessagesForWork(ctx, in.WorkID, effectiveLimit(in.Limit, in.Page))
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "PM1.Q14", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"sent_at"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"messages": messages})
}

func (r runtime) readTraceHistory(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in historyInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	bindingInput := in
	bindingInput.Page.Cursor = nil
	binding, _ := json.Marshal(bindingInput)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	q, err := r.Store.QueryQ7(ctx, store.Q7Request{Work: in.WorkID, Direction: historyDirection(in.Direction), RecordKinds: in.RecordKinds, ContractVersion: in.ContractVersion, Limit: effectiveLimit(in.Limit, in.Page), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q7(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, string(binding), "summary")
}

func (r runtime) readTraceObservations(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in observationReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	return pagedQuery(r, ctx, base, in.Page, effectiveLimit(in.Limit, in.Page), pageBinding(in, func(p *observationReadInput) { p.Page.Cursor = nil }),
		func(inner string, limit int) store.WorkObservationsRequest {
			return store.WorkObservationsRequest{WorkID: in.WorkID, Limit: limit, Cursor: inner}
		},
		r.Store.ReadWorkObservations, r.observationPage)
}

func (r runtime) observationPage(base agentcontract.Envelope, page store.WorkObservationPage) (agentcontract.Envelope, error) {
	return r.resultEnvelope(base, page.ResultMeta, r.scope(page.ResultMeta), map[string]any{"observations": page.Observations, "total": page.Total})
}

func (r runtime) readTraceExternalObservations(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in externalObservationReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	now := r.Authority.now()
	externalObservations, err := r.Store.ExternalObservationsForWork(ctx, in.WorkID, now, effectiveLimit(in.Limit, in.Page))
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "CD-0040.R1", ContractVersion: "CD-0040/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: now.UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"created_event_seq", "observation_id"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"external_observations": externalObservations})
}

func (r runtime) readTraceContinuity(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in continuityInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	bindingInput := in
	bindingInput.Page.Cursor = nil
	binding, _ := json.Marshal(bindingInput)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "continuity")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	limit := effectiveLimit(in.Limit, in.Page)
	// A versioned inner projection cursor continues an oversized projection's
	// page sequence; the version-1 boundary cursor stays the store's window
	// continuation. Only the original boundary cursor reaches the store read.
	if projection, ok := decodeContinuityProjectionCursor(inner); ok {
		if projection.Work != in.WorkID {
			return coreError(base, "invalid_cursor", "continuity projection cursor does not match the requested work", "restart_query", false), nil
		}
		response, err := r.continuityProjectionPage(ctx, base, string(binding), limit, projection, nil)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return response, nil
	}
	continuityReq := store.ContinuityRequest{Work: in.WorkID, Limit: limit, Cursor: inner, ProjectID: r.Envelope.AmbientProjectID}
	snapshot, err := store.ReadWorkflowContinuity(ctx, r.Store, continuityReq)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	// One captured snapshot is fitted to the envelope bound: every candidate
	// is a complete prefix of the same read, measured as the final wire
	// object with its authenticated cursor and notices. Complete-prefix
	// reductions name their omissions explicitly; the paged observations read
	// and the boundary cursor recover everything they drop.
	requested := len(snapshot.Boundaries)
	response, size, err := FitContinuity(snapshot, continuityReq, agentcontract.MaxResultEnvelopeBytes, func(fitted store.ContinuitySnapshot) (agentcontract.Envelope, int, error) {
		response, err := r.continuity(base, fitted)
		if err != nil {
			return response, 0, err
		}
		if response, err = r.wrapCursor(ctx, response, string(binding), "continuity"); err != nil {
			return response, 0, err
		}
		if len(fitted.Observations) < len(snapshot.Observations) {
			if len(response.Omissions) >= agentcontract.MaxNotices {
				return coreError(base, "limit_exceeded", "byte-bounded continuity page needs an observation omission notice but the omission limit is exhausted", "reduce_limit", false), 0, nil
			}
			response.Omissions = append(response.Omissions, agentcontract.Notice{Kind: "continuity_observations_reduced", Details: map[string]any{"captured": len(snapshot.Observations), "retained": len(fitted.Observations), "maximum_bytes": agentcontract.MaxResultEnvelopeBytes}})
		}
		if len(fitted.Boundaries) < requested {
			if len(response.Omissions) >= agentcontract.MaxNotices {
				return coreError(base, "limit_exceeded", "byte-bounded continuity page needs a continuation notice but the omission limit is exhausted", "reduce_limit", false), 0, nil
			}
			response.Omissions = append(response.Omissions, agentcontract.Notice{Kind: "byte_bounded_page", Details: map[string]any{"requested_limit": requested, "effective_limit": len(fitted.Boundaries), "maximum_bytes": agentcontract.MaxResultEnvelopeBytes}})
		}
		size, err := readEnvelopeSize(response)
		return response, size, err
	})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	// The inline answer stays whenever the measured final envelope fits the
	// unchanged cap. Otherwise the original requested projection — the
	// captured boundary window and observation window, never reduced data —
	// travels as lossless authenticated pages of its exact canonical
	// document, and the first page renders the very snapshot this read
	// captured. A frame-only breach is not caused by the projection and is
	// refused honestly by the measured-size refusal.
	if size > agentcontract.MaxResultEnvelopeBytes {
		return r.continuityProjectionPage(ctx, base, string(binding), limit, continuityProjectionCursor{Version: 2, Work: in.WorkID, BoundaryCursor: inner}, &snapshot)
	}
	return response, nil
}

// continuityProjectionPage renders one page of the canonical continuity
// document. A non-nil captured snapshot is the exact capture the initial
// read already made, so its first page renders that capture without a second
// store read; every continuation passes nil and re-derives the snapshot
// under the original boundary cursor. The document is re-rendered and its
// digest is refused before any fragment is emitted when it changed, so a
// completed page sequence is exactly one projection. Each fragment is the
// largest whole-code-point prefix that keeps the measured final envelope
// inside the unchanged cap.
func (r runtime) continuityProjectionPage(ctx context.Context, base agentcontract.Envelope, binding string, limit int, cursor continuityProjectionCursor, captured *store.ContinuitySnapshot) (agentcontract.Envelope, error) {
	if cursor.Work == "" || (cursor.Digest != "" && !agentcontract.ValidSHA256Proof(cursor.Digest)) || cursor.NextByteOffset < 0 {
		return coreError(base, "invalid_cursor", "continuity projection cursor is malformed", "restart_query", false), nil
	}
	snapshot, err := r.continuityProjectionSnapshot(ctx, limit, cursor, captured)
	if err != nil {
		return base, err
	}
	doc, digest, err := ContinuityCanonicalDocument(snapshot)
	if err != nil {
		return base, err
	}
	// The digest is the snapshot binding: it is checked before any fragment
	// is emitted, so a continuation whose projection changed — including a
	// changed session-specific lease view the event watermark cannot see —
	// refuses with no effects.
	if cursor.Digest != "" && digest != cursor.Digest {
		return coreError(base, "invalid_cursor", "continuity projection changed since its first page; restart the read", "restart_query", false), nil
	}
	offset := int(cursor.NextByteOffset)
	switch {
	case offset < 0 || offset > len(doc) || (offset > 0 && offset == len(doc)):
		return coreError(base, "invalid_cursor", "continuity projection cursor offset is out of range", "restart_query", false), nil
	case offset > 0 && !utf8.RuneStart(doc[offset]):
		return coreError(base, "invalid_cursor", "continuity projection cursor offset splits a code point", "restart_query", false), nil
	}
	header := continuityProjectionHeaderOf(snapshot)
	// One freshness instant for the whole page render, so the searched
	// candidate and the emitted page measure the same bytes.
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	at := func(n int) (agentcontract.Envelope, int, error) {
		fragment := string(doc[offset : offset+n])
		end := offset + n
		var next any
		nextCursor := snapshot.NextCursor
		if end < len(doc) {
			next = "projection"
			innerToken, err := json.Marshal(continuityProjectionCursor{Version: 2, Work: cursor.Work, BoundaryCursor: cursor.BoundaryCursor, Digest: digest, NextByteOffset: int64(end)})
			if err != nil {
				return base, 0, err
			}
			token := string(innerToken)
			nextCursor = &token
		} else if nextCursor != nil {
			// The document ends first; the original boundary-page
			// continuation resumes exactly where the requested read left it.
			next = "boundaries"
		}
		meta := continuityResultMetaObservedAt(snapshot, nextCursor, observedAt)
		response, err := r.continuityProjectionEnvelope(base, meta, header, digest, int64(len(doc)), int64(offset), fragment, end == len(doc), next)
		if err != nil {
			return response, 0, err
		}
		if response, err = r.wrapCursor(ctx, response, binding, "continuity"); err != nil {
			return response, 0, err
		}
		size, err := readEnvelopeSize(response)
		return response, size, err
	}
	// Any whole-code-point candidate renders a schema-admissible page, so a
	// render error there is a real failure — signing, store, or marshal —
	// and propagates unchanged instead of counting as doesn't-fit.
	length, err := continuityFragmentLength(doc, offset, func(n int) (bool, error) {
		_, size, renderErr := at(n)
		if renderErr != nil {
			return false, renderErr
		}
		return size <= agentcontract.MaxResultEnvelopeBytes, nil
	})
	if err != nil {
		return base, err
	}
	if length == 0 {
		// No admissible code point fits: the frame alone breaches the cap,
		// which the projection cannot repair. The honest measured refusal
		// names the smallest legal page — the offset's first whole code
		// point, never an empty or mid-rune cut — and a failure from that
		// render propagates instead of posing as an overflow.
		first := 1
		for offset+first < len(doc) && !utf8.RuneStart(doc[offset+first]) {
			first++
		}
		_, size, renderErr := at(first)
		if renderErr != nil {
			return base, renderErr
		}
		return readSizeRefusal(base, size), nil
	}
	response, _, err := at(length)
	return response, err
}

func (r runtime) continuityProjectionSnapshot(ctx context.Context, limit int, cursor continuityProjectionCursor, captured *store.ContinuitySnapshot) (store.ContinuitySnapshot, error) {
	if captured != nil {
		return *captured, nil
	}
	return store.ReadWorkflowContinuity(ctx, r.Store, store.ContinuityRequest{Work: cursor.Work, Limit: limit, Cursor: cursor.BoundaryCursor, ProjectID: r.Envelope.AmbientProjectID})
}

// researchPackReadV2 is the version 2 pack read result: the full pack when no
// selector was sent, or only the chosen revision and its selected findings and
// provenance when one was.
type researchPackReadV2 struct {
	ResultVersion int                `json:"result_version"`
	Pack          store.ResearchPack `json:"pack"`
}

// researchOwnerReadV2 is the version 2 owner read: bounded pack descriptors in
// the store's stable order. The authenticated continuation rides the envelope,
// never the payload.
type researchOwnerReadV2 struct {
	ResultVersion int                            `json:"result_version"`
	Packs         []store.ResearchPackDescriptor `json:"packs"`
}

// readTraceResearch returns bare full packs at version 1. Exact selections and
// owner descriptor pages require an explicit version 2 result.
func (r runtime) readTraceResearch(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in researchReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if (in.PackID == "") == (in.WorkID == "") {
		return coreError(base, "invalid_input", "research read requires exactly one of pack_id or work_id", "resolve_ambiguity", false), nil
	}
	if in.PackID != "" && cursorValue(in.Page) != "" {
		return coreError(base, "invalid_input", "research pack reads take no continuation cursor", "restart_query", false), nil
	}
	version := in.ResultVersion
	if version == 0 {
		version = 1
	}
	if in.WorkID != "" && version != 2 {
		return coreError(base, "invalid_input", "research owner descriptor pages require result_version 2", "resolve_ambiguity", false), nil
	}
	if (in.Revision != 0 || len(in.FindingIDs) > 0) && (in.PackID == "" || version != 2) {
		return coreError(base, "invalid_input", "revision and finding_ids selectors require pack_id with result_version 2", "resolve_ambiguity", false), nil
	}
	if len(in.FindingIDs) > 0 && in.Revision <= 0 {
		return coreError(base, "invalid_input", "finding_ids require an explicit positive revision", "resolve_ambiguity", false), nil
	}
	limit := effectiveLimit(in.Limit, in.Page)
	if in.PackID != "" {
		pack, readErr := r.Store.ReadResearchPack(ctx, store.ResearchReadRequest{PackID: in.PackID, Revision: in.Revision, FindingIDs: in.FindingIDs, Limit: limit})
		if readErr != nil {
			return failureEnvelope(base, readErr), nil
		}
		meta := store.ResultMeta{QueryID: "PM1.Q11", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{WorkID: pack.OwnerWorkID}, SourceVersionWatermark: pack.CurrentRevision, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: pack.UpdatedAt}, OrderingKeys: []string{"pack:" + pack.PackID}}
		if version == 1 {
			return r.resultEnvelope(base, meta, r.scope(store.ResultMeta{}), pack)
		}
		return r.resultEnvelope(base, meta, r.scope(store.ResultMeta{}), researchPackReadV2{ResultVersion: 2, Pack: pack})
	}
	bindingInput := in
	bindingInput.Page.Cursor = nil
	binding, _ := json.Marshal(bindingInput)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "research_owner")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	// Owner-list cursors use the store publication revision, not a pack's
	// content revision.
	watermark, err := r.Store.Revision(ctx)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	page, listErr := r.Store.ResearchPacksByOwner(ctx, in.WorkID, limit, inner)
	if listErr != nil {
		return failureEnvelope(base, listErr), nil
	}
	packs := page.Packs
	if packs == nil {
		packs = []store.ResearchPackDescriptor{}
	}
	meta := store.ResultMeta{QueryID: "PM1.Q11", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: r.Authority.now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"updated_at DESC, pack_id ASC"}, SourceVersionWatermark: watermark}
	if page.NextCursor != nil {
		meta.NextCursor = page.NextCursor
	}
	response, err := r.resultEnvelope(base, meta, r.scope(meta), researchOwnerReadV2{ResultVersion: 2, Packs: packs})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.wrapCursor(ctx, response, string(binding), "research_owner")
}

func (r runtime) readTraceRelations(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in relationInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	q, err := r.Store.QueryQ8(ctx, store.Q8Request{Work: in.WorkID, RelationKinds: in.RelationKinds, Direction: in.Direction, Depth: in.Depth})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.q8(base, q)
}

// readTraceProjectRetirement answers concord_work_trace.project_retirement
// (CD-0182.R1). The retirement identities come from the authenticated call
// context, never from the input: the ambient Project is the session's own,
// the envelope session reference is the acting session, and the preservation
// probe runs in the session's host-reported worktree. The derivation is
// read-only and writes nothing.
func (r runtime) readTraceProjectRetirement(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in workIDInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	project := r.Envelope.AmbientProjectID
	session := r.Envelope.SessionRef
	if project == "" || session == "" || r.Envelope.Worktree == "" {
		return coreError(base, "unknown_scope", "project retirement requires a resolved Project, an authenticated session, and the session's worktree", "refresh_context", false), nil
	}
	retirement, err := store.EvaluateProjectSessionRetirement(ctx, r.Store, in.WorkID, project, session, r.Envelope.Worktree)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.resultEnvelope(base, store.ResultMeta{QueryID: "CD-0182.R1", ContractVersion: "CD-0182/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID, ProjectIDs: []string{project}}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: r.Authority.now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"retirement:" + in.WorkID}}, r.scope(store.ResultMeta{}), retirement)
}

func (r runtime) readDomain(ctx context.Context, base agentcontract.Envelope, input []byte, queryID string) (agentcontract.Envelope, error) {
	var in domainReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	product := in.ProductID
	if product == "" {
		product = r.Envelope.SelectedProductID
	}
	if product == "" {
		return coreError(base, "unknown_scope", "Domain reads require a resolved Product", "reread_entities", false), nil
	}
	// The registry resolves from the committed snapshot
	// of the Product's designated home, so no read carries a rebuild demand;
	// a Product with no designated home keeps the domain read's own refusal.
	switch r.Operation {
	case "list":
		result, err := r.Store.QueryDomainList(ctx, store.DomainListRequest{Product: product, Limit: effectiveLimit(in.Limit, in.Page), Cursor: deref(in.Page.Cursor)})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainListPayload(result))
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, queryID+":"+product, "domains")
	case "detail":
		if in.DomainID == "" {
			return coreError(base, "invalid_input", "Domain detail requires domain_id", "reread_entities", false), nil
		}
		result, err := r.Store.QueryDomainDetail(ctx, store.DomainDetailRequest{Product: product, Domain: in.DomainID})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainDetailPayload(result))
	default:
		// Every Domain operation names its own read. Refusing an unmatched
		// operation keeps a new one from silently answering with another
		// Domain read's payload.
		return base, errors.New("unsupported Domain read operation")
	}
}
