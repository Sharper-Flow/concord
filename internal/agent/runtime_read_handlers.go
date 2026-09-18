package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sharper-flow/concord/internal/portfolio"
	"github.com/sharper-flow/concord/internal/store"
)

// The per-read-kind handlers behind (runtime).read. Each holds one read's own
// decode-and-query logic; the dispatch in runtime.go routes tool.operation to
// one of these and refuses an unmatched operation. The shared scaffolding they
// call — decodeOperationInput, the ambient-product fallbacks, unwrapCursor and
// wrapCursor, failureEnvelope and resultEnvelope — stays in runtime.go.

func (r runtime) readProductResolve(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
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
	q, err := r.Store.QueryQ1(ctx, store.Q1Request{Product: in.ProductID, Project: in.ProjectID, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q1(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "summary")
}

func (r runtime) readProductSnapshot(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in productSnapshotInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	q, err := r.Store.QueryQ2(ctx, store.Q2Request{Product: in.ProductID, ProjectIDs: in.ProjectIDs, PreviewLimit: r.boundedPreview(in.PreviewLimit)})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.q2(base, q)
}

func (r runtime) readProductPortfolio(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in productRowPortfolioInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	q, err := portfolio.Read(ctx, r.Store, store.ProductRowRequest{Product: in.ProductID, Limit: r.boundedLimit(in.Page.Limit), Cursor: cursorValue(in.Page), Source: in.Source})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.productRows(base, q)
}

func (r runtime) readProductBlockedSessions(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in blockedSessionsInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	products := []string{}
	if in.ProductID != "" {
		products = append(products, in.ProductID)
	}
	result, err := r.Store.BlockedSessions(ctx, time.Now().UTC(), products, r.boundedLimit(in.Page.Limit))
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), map[string]any{"sessions": result.Sessions})
}

func (r runtime) readProductResources(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in resourcesInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" && in.ResourceID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	result, err := r.Store.Resources(ctx, store.ResourcesRequest{ProductID: in.ProductID, ResourceID: in.ResourceID, Class: in.Class, Kind: in.Kind, Environment: in.Environment, Limit: r.boundedLimit(in.Page.Limit)})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), map[string]any{"resources": result.Resources})
}

func (r runtime) readWorkList(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in workListInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	bindingInput := in
	bindingInput.Page.Cursor = nil
	binding, _ := json.Marshal(bindingInput)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	req := store.Q3Request{Product: in.ProductID, LifecycleStates: nonEmpty(in.Lifecycle), Limit: r.boundedLimit(in.Page.Limit), Cursor: inner, Kind: in.Kind, ProjectIDs: in.ProjectIDs, WorkIDs: in.WorkIDs, TagIDs: in.TagIDs, PriorityMin: in.PriorityMin, PriorityMax: in.PriorityMax, TerminalSince: in.TerminalSince, Detail: in.Detail}
	q, err := r.Store.QueryQ3(ctx, req)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q3(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "summary")
}

func (r runtime) readWorkReady(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in workReadyInput
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
	q, err := r.Store.QueryQ5(ctx, store.Q5Request{Product: in.ProductID, Project: in.ProjectID, Kind: in.Kind, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q5(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "summary")
}

func (r runtime) readWorkBlocked(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in workBlockedInput
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
	q, err := r.Store.QueryQ4(ctx, store.Q4Request{Product: in.ProductID, Project: in.ProjectID, Work: in.WorkID, Kind: in.Kind, Depth: in.Depth, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q4(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "summary")
}

func (r runtime) readWorkScope(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in workScopeInput
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
	q, err := r.Store.QueryQ6(ctx, store.Q6Request{Product: in.ProductID, Project: in.ProjectID, Work: in.WorkID, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q6(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "summary")
}

func (r runtime) readResourceClaims(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in resourceClaimsInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	claims, err := r.Store.ResourceClaims(ctx, in.ResourceKey, in.ProductID, r.boundedLimit(in.Page.Limit))
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "PM1.Q13", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{ProductID: in.ProductID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"claimed_at"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"claims": claims})
}

func (r runtime) readWorkMessages(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in messagesInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	messages, err := r.Store.MessagesForWork(ctx, in.WorkID, r.boundedLimit(in.Page.Limit))
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "PM1.Q14", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"sent_at"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"messages": messages})
}

func (r runtime) readWorktreeAudit(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in worktreeAuditInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	audit, err := r.Store.WorktreeAudit(ctx, store.WorktreeAuditRequest{ProductID: in.ProductID, Limit: r.boundedLimit(in.Page.Limit)})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "PM1.Q16", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{ProductID: in.ProductID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"class", "path"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"root": audit.Root, "drift": audit.Drift})
}

func (r runtime) readWorktreeInspect(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in worktreeInspectInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	// CD-0096 D3 Inspect: the worktree resolves through the session's
	// Project anchor and the work item's folded entry. No path input
	// exists, no lease is taken, and the persistent target never moves.
	project := r.Envelope.AmbientProjectID
	if project == "" {
		return coreError(base, "unknown_scope", "worktree tiers resolve through the session's Project; this session holds none", "refresh_context", false), nil
	}
	inspect, err := r.Store.InspectWorktree(ctx, store.WorktreeInspectRequest{WorkID: in.WorkID, ProjectID: project, Mode: in.Mode, Path: in.Path})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "CD-0096.R1", ContractVersion: "CD-0096/1.0", ResolvedScope: store.ResolvedScope{ProductID: r.Envelope.SelectedProductID, ProjectID: project, WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"mode"}}
	return r.resultEnvelope(base, meta, r.scope(meta), inspect)
}

func (r runtime) readTraceHistory(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
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
	q, err := r.Store.QueryQ7(ctx, store.Q7Request{Work: in.WorkID, Direction: historyDirection(in.Direction), EventKinds: in.EventKinds, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q7(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "summary")
}

func (r runtime) readTraceObservations(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in observationReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	now := r.Authority.now()
	observations, err := r.Store.ObservationsForWork(ctx, in.WorkID, r.boundedLimit(in.Page.Limit))
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "CD-0030.R1", ContractVersion: "CD-0030/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: now.UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"recorded_at", "observation_id"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"observations": observations})
}

func (r runtime) readTraceExternalObservations(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in externalObservationReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	now := r.Authority.now()
	externalObservations, err := r.Store.ExternalObservationsForWork(ctx, in.WorkID, now, r.boundedLimit(in.Limit))
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: "CD-0040.R1", ContractVersion: "CD-0040/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: now.UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"created_event_seq", "observation_id"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"external_observations": externalObservations})
}

func (r runtime) readTraceContinuity(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
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
	// CD-0096 D5: a session that signs its identity re-pins its held
	// verify leases in the pinned projection. Envelopes without a session
	// identity keep the work-keyed snapshot the session-boot path renders.
	continuityReq := store.ContinuityRequest{Work: in.WorkID, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner}
	if r.Envelope.ClientRef != "" && r.Envelope.AgentRef != "" && r.Envelope.SessionRef != "" {
		continuityReq.Owner = &store.SessionWorktreeOwner{ClientRef: r.Envelope.ClientRef, AgentRef: r.Envelope.AgentRef, SessionRef: r.Envelope.SessionRef}
	}
	snapshot, err := store.ReadWorkflowContinuity(ctx, r.Store, continuityReq)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.continuity(base, snapshot)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "continuity")
}

func (r runtime) readTraceResearch(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in researchReadInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if (in.PackID == "") == (in.WorkID == "") {
		return coreError(base, "invalid_input", "research read requires exactly one of pack_id or work_id", "resolve_ambiguity", false), nil
	}
	var pack store.ResearchPack
	var readErr error
	if in.PackID != "" {
		pack, readErr = store.GetResearchPack(ctx, r.Store, in.PackID, r.boundedLimit(in.Page.Limit))
	} else {
		packs, listErr := store.ResearchPacksByOwner(ctx, r.Store, in.WorkID, r.boundedLimit(in.Page.Limit))
		if listErr != nil {
			return failureEnvelope(base, listErr), nil
		}
		if len(packs) == 0 {
			return coreError(base, "unknown_scope", "no active research pack for that work item", "reread_entities", false), nil
		}
		pack = packs[0]
	}
	if readErr != nil {
		return failureEnvelope(base, readErr), nil
	}
	return r.resultEnvelope(base, store.ResultMeta{QueryID: "PM1.Q11", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{WorkID: pack.OwnerWorkID}, SourceVersionWatermark: pack.CurrentRevision, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: pack.UpdatedAt}, OrderingKeys: []string{"pack:" + pack.PackID}}, r.scope(store.ResultMeta{}), pack)
}

func (r runtime) readTraceRelations(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
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

func (r runtime) readInitiativeEntries(ctx context.Context, base Envelope, input []byte, queryID string) (Envelope, error) {
	var in initiativeEntriesInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	summary, err := r.Store.ReadWorkItemSummary(ctx, in.InitiativeWorkID)
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) && failure.Kind == store.KindProjectionNotFound {
			return coreError(base, "unknown_scope", "Initiative does not exist", "reread_entities", false), nil
		}
		return failureEnvelope(base, err), nil
	}
	if summary.Kind != "initiative" {
		return coreError(base, "invariant_violation", "entry read target is not an Initiative", "reread_entities", false), nil
	}
	products, err := r.Store.ProductsForWorkIDs(ctx, []string{in.InitiativeWorkID})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	if len(products[in.InitiativeWorkID]) != 1 {
		return coreError(base, "invariant_violation", "Initiative does not derive exactly one Product", "resolve_ambiguity", false), nil
	}
	entries, err := r.Store.ReadInitiativeEntries(ctx, in.InitiativeWorkID)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	meta := store.ResultMeta{QueryID: queryID, ContractVersion: "C21/1.0", ResolvedScope: store.ResolvedScope{ProductID: products[in.InitiativeWorkID][0], WorkID: in.InitiativeWorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"position", "child_work_id"}}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"entries": entries, "narrative": summary.Narrative})
}

func (r runtime) readKnowledgeSearch(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in knowledgeSearchInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	var home store.KnowledgeHome
	var homeErr error
	if in.ProductID != "" || in.ProjectID != "" {
		home, homeErr = r.Store.ResolveKnowledgeQueryHome(ctx, in.ProductID, in.ProjectID, store.KnowledgeHome{}, "PM1.Q9")
	} else {
		home, homeErr = r.knowledgeHome(ctx)
	}
	if homeErr != nil {
		return failureEnvelope(base, homeErr), nil
	}
	bindingInput := in
	bindingInput.Page.Cursor = nil
	binding, _ := json.Marshal(bindingInput)
	inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary", home)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	// The read is the demand (CD-0082 D1): a stale index rebuilds here,
	// before the query and outside any transaction. A home git cannot
	// reach is left for the query itself to refuse or degrade by rule.
	if err := r.Store.EnsureKnowledgeIndexFresh(ctx, home); err != nil && !in.AllowDegraded {
		return failureEnvelope(base, err), nil
	}
	q, err := r.Store.QueryQ9(ctx, store.Q9Request{Product: in.ProductID, Project: in.ProjectID, Domain: in.DomainID, Kinds: knowledgeKinds(in.Kinds), Tags: in.Tags, Text: in.Text, Since: deref(in.Since), Until: deref(in.Until), Limit: r.boundedLimit(in.Page.Limit), Cursor: inner, Home: home, AllowDegraded: in.AllowDegraded})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	response, err := r.q9(base, q)
	if err != nil {
		return response, err
	}
	return r.wrapCursor(ctx, response, inner, string(binding), "summary")
}

func (r runtime) readKnowledgeResolveNote(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in knowledgeResolveInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	q, err := r.Store.QueryQ10(ctx, store.Q10Request{Work: in.WorkID, KnowledgeID: in.KnowledgeID, Product: r.Envelope.SelectedProductID, Home: store.KnowledgeHome{}})
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	return r.q10(base, q)
}

func (r runtime) readKnowledgeUnprocessed(ctx context.Context, base Envelope, input []byte) (Envelope, error) {
	var in knowledgeUnprocessedInput
	if err := decodeOperationInput(input, &in); err != nil {
		return base, err
	}
	if in.ProductID == "" {
		in.ProductID = r.Envelope.SelectedProductID
	}
	var home store.KnowledgeHome
	var homeErr error
	if in.ProductID != "" || in.ProjectID != "" {
		home, homeErr = r.Store.ResolveKnowledgeQueryHome(ctx, in.ProductID, in.ProjectID, store.KnowledgeHome{}, "PM1.Q15")
	} else {
		home, homeErr = r.knowledgeHome(ctx)
	}
	if homeErr != nil {
		return failureEnvelope(base, homeErr), nil
	}
	paths, err := r.Store.ReadUnprocessedKnowledgeDocs(ctx, home)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = in.Page.Limit
	}
	limit = r.boundedLimit(limit)
	if limit == 0 || limit > 100 {
		limit = 100
	}
	if len(paths) > limit {
		paths = paths[:limit]
	}
	meta := store.ResultMeta{
		QueryID:         "PM1.Q15",
		ContractVersion: "PM1/1.0",
		ResolvedScope:   store.ResolvedScope{ProductID: in.ProductID, ProjectID: in.ProjectID},
		Authority:       "authoritative",
		Freshness:       store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)},
		OrderingKeys:    []string{"path"},
	}
	return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"paths": paths})
}

func (r runtime) readDomain(ctx context.Context, base Envelope, input []byte, queryID string) (Envelope, error) {
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
	// The Domain registry is derived from the Product's knowledge home,
	// and these reads are where an agent learns the registry hash it pins
	// in an architecture binding. The read is the demand (CD-0082 D1):
	// a stale registry rebuilds here, outside any transaction, so an
	// approval never refuses for an index nobody has read yet. A Product
	// with no designated home keeps the domain read's own refusal.
	if err := r.freshenProductKnowledge(ctx, product); err != nil {
		return failureEnvelope(base, err), nil
	}
	switch r.Operation {
	case "list":
		result, err := r.Store.QueryDomainList(ctx, store.DomainListRequest{Product: product, Limit: r.boundedLimit(in.Page.Limit), Cursor: deref(in.Page.Cursor)})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainListPayload(result))
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, deref(in.Page.Cursor), queryID+":"+product, "domains")
	case "detail":
		if in.DomainID == "" {
			return coreError(base, "invalid_input", "Domain detail requires domain_id", "reread_entities", false), nil
		}
		result, err := r.Store.QueryDomainDetail(ctx, store.DomainDetailRequest{Product: product, Domain: in.DomainID})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainDetailPayload(result))
	case "active_work":
		if in.DomainID == "" {
			return coreError(base, "invalid_input", "active Domain work requires domain_id", "reread_entities", false), nil
		}
		result, err := r.Store.QueryDomainActiveWork(ctx, store.DomainActiveWorkRequest{Product: product, Domain: in.DomainID, Limit: r.boundedLimit(in.Page.Limit), Cursor: deref(in.Page.Cursor)})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainActiveWorkPayload(result))
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, deref(in.Page.Cursor), queryID+":"+product+":"+in.DomainID, "work")
	case "attachments":
		if in.DomainID == "" {
			return coreError(base, "invalid_input", "Domain attachments require domain_id", "reread_entities", false), nil
		}
		result, err := r.Store.QueryDomainAttachments(ctx, store.DomainAttachmentsRequest{Product: product, Domain: in.DomainID})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainAttachmentsPayload(result))
	case "overlaps":
		result, err := r.Store.QueryDomainOverlaps(ctx, store.DomainOverlapsRequest{Product: product, Domain: in.DomainID})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainOverlapsPayload(result))
	default:
		// Every Domain operation names its own read. Refusing an unmatched
		// operation keeps a new one from silently answering with another
		// Domain read's payload.
		return base, errors.New("unsupported Domain read operation")
	}
}
