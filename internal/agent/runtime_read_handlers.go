package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
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
		return store.Q3Request{Product: in.ProductID, AmbientProject: r.Envelope.AmbientProjectID, LifecycleStates: nonEmpty(in.Lifecycle), Limit: limit, Cursor: inner, Kind: in.Kind, ProjectIDs: in.ProjectIDs, WorkIDs: in.WorkIDs, TagIDs: in.TagIDs, PriorityMin: in.PriorityMin, PriorityMax: in.PriorityMax, TerminalSince: in.TerminalSince, Detail: in.Detail}
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
	q, err := r.Store.QueryQ7(ctx, store.Q7Request{Work: in.WorkID, Direction: historyDirection(in.Direction), RecordKinds: in.RecordKinds, Limit: effectiveLimit(in.Limit, in.Page), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q7(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, string(binding), "summary")
}

type resumeInput struct {
	WorkID string    `json:"work_id"`
	Page   pageInput `json:"page"`
	Limit  int       `json:"limit,omitempty"`
}

type resumeCursor struct {
	Digest    string `json:"digest"`
	Watermark int64  `json:"watermark"`
	Offset    int    `json:"offset"`
}

func (r runtime) readTraceResume(ctx context.Context, base agentcontract.Envelope, input []byte) (agentcontract.Envelope, error) {
	var in resumeInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	// Like mutation_result, the TS3 §3 limit alias is query identity for the
	// fragment transport, not a fragment size: pageBinding marshals the whole
	// input with the cursor cleared, so a continuation that changes the limit
	// refuses alongside principal, work and document drift.
	bindingData, _ := json.Marshal([]string{r.Reader.PrincipalRef, pageBinding(in, func(p *resumeInput) { p.Page.Cursor = nil })})
	binding := string(bindingData)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), binding, "resume")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	var cursor resumeCursor
	if inner != "" {
		if json.Unmarshal([]byte(inner), &cursor) != nil || !agentcontract.ValidSHA256Proof(cursor.Digest) || cursor.Offset <= 0 {
			return coreError(base, "invalid_cursor", "resume cursor is malformed", "restart_query", false), nil
		}
	}
	resume, err := store.ReadWorkflowResume(ctx, r.Store, in.WorkID)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "C19.Resume", ContractVersion: "C19/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, SourceVersionWatermark: resume.SourceVersionWatermark, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: r.Authority.now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"plan:" + in.WorkID}}
	doc, err := json.Marshal(resume)
	if err != nil {
		return base, err
	}
	if err := payloadschema.Validate("resume_read_document", doc); err != nil {
		return base, err
	}
	digest := mutationDocumentDigest(doc)
	if inner != "" && (cursor.Digest != digest || cursor.Watermark != resume.SourceVersionWatermark || cursor.Offset >= len(doc) || !utf8.RuneStart(doc[cursor.Offset])) {
		return coreError(base, "invalid_cursor", "resume document or source version changed; restart the read", "restart_query", false), nil
	}
	if inner == "" {
		response, err := r.resultEnvelope(base, meta, r.scope(store.ResultMeta{}), resume)
		if err != nil {
			return response, err
		}
		size, err := readEnvelopeSize(response)
		if err != nil {
			return base, err
		}
		if size <= agentcontract.MaxResultEnvelopeBytes {
			return response, nil
		}
	}
	offset := cursor.Offset
	render := func(n int) (agentcontract.Envelope, int, error) {
		end := offset + n
		page := mutationFragmentPage{"json-fragment", digest, len(doc), offset, string(doc[offset:end]), n, end == len(doc)}
		pageMeta := meta
		if end < len(doc) {
			token, err := json.Marshal(resumeCursor{digest, resume.SourceVersionWatermark, end})
			if err != nil {
				return base, 0, err
			}
			next := string(token)
			pageMeta.NextCursor = &next
		}
		response, err := r.resultEnvelope(base, pageMeta, r.scope(store.ResultMeta{}), page)
		if err != nil {
			return base, 0, err
		}
		response, err = r.wrapCursor(ctx, response, binding, "resume")
		if err != nil {
			return base, 0, err
		}
		size, err := readEnvelopeSize(response)
		return response, size, err
	}
	length, err := fragmentLength(doc, offset, func(n int) (bool, error) {
		_, size, err := render(n)
		return size <= agentcontract.MaxResultEnvelopeBytes, err
	})
	if err != nil {
		return base, err
	}
	if length == 0 {
		return coreError(base, "limit_exceeded", "resume fragment frame exceeds the envelope cap", "contact_operator", false), nil
	}
	response, _, err := render(length)
	return response, err
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
