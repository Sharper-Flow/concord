// Package storeport adapts Concord's canonical Product-row read to the
// framework-independent launcher boundary.
package storeport

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/portfolio"
	"github.com/sharper-flow/concord/internal/store"
)

type Port struct{ Store *store.Store }

func New(s *store.Store) *Port { return &Port{Store: s} }

func (p *Port) Read(ctx context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	switch request.Kind {
	case launcher.ReadPortfolio:
		result, err := portfolio.Read(ctx, p.Store, store.ProductRowRequest{Limit: request.Limit, Cursor: request.Cursor})
		if err != nil {
			return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "unreachable", StatusMessage: err.Error()}, err
		}
		return snapshotFromProductRows(result), nil
	case launcher.ReadProduct:
		result, err := p.Store.QueryLauncherProduct(ctx, store.LauncherProductRequest{Product: request.Product, Limit: request.Limit, Depth: 3})
		if err != nil {
			return launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: request.Product, Coverage: "unreachable", StatusMessage: err.Error()}, err
		}
		return snapshotFromProduct(result, request.Product, request.Section), nil
	case launcher.ReadWork:
		result, err := p.Store.QueryLauncherWork(ctx, store.LauncherWorkRequest{Product: request.Product, Work: request.Work, Limit: request.Limit})
		if err != nil {
			return launcher.Snapshot{Screen: launcher.ScreenWork, AmbientProduct: request.Product, SelectedWorkID: request.Work, Coverage: "unreachable", StatusMessage: err.Error()}, err
		}
		return snapshotFromWork(result, request.Product, request.Work, request.Section), nil
	case launcher.ReadKnowledge:
		return p.readKnowledge(ctx, request)
	case launcher.ReadSearch:
		return p.readSearch(ctx, request)
	default:
		return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "unavailable", StatusMessage: "unsupported_read"}, nil
	}
}

func snapshotFromProduct(result store.LauncherProductResult, product string, section launcher.Section) launcher.Snapshot {
	s := launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: product, Section: section, QueryID: result.QueryID, ContractVersion: result.ContractVersion, SourceVersionWatermark: result.SourceVersionWatermark, Watermark: strconv.FormatInt(result.SourceVersionWatermark, 10), ObservedAt: result.Freshness.ObservedAt, Reliance: result.Authority, Coverage: result.Authority, OrderingKeys: append([]string(nil), result.OrderingKeys...)}
	s.Ranked = make([]launcher.RankedWork, 0, len(result.Works))
	for _, item := range result.Works {
		s.Ranked = append(s.Ranked, mapWork(item))
	}
	s.Relations = relationTree(result.Edges, 3, result.Authority)
	if len(result.Omissions) > 0 {
		s.Relations.Unavailable = joinOmissions(s.Relations.Unavailable, result.Omissions)
		s.Relations.Coverage = "unavailable"
		s.Coverage = "unavailable"
		s.StatusMessage = "unavailable: " + strings.Join(result.Omissions, ", ")
	}
	return s
}

func snapshotFromWork(result store.LauncherWorkResult, product, work string, section launcher.Section) launcher.Snapshot {
	s := launcher.Snapshot{Screen: launcher.ScreenWork, AmbientProduct: product, SelectedWorkID: work, Section: section, QueryID: result.QueryID, ContractVersion: result.ContractVersion, SourceVersionWatermark: result.SourceVersionWatermark, Watermark: strconv.FormatInt(result.SourceVersionWatermark, 10), ObservedAt: result.Freshness.ObservedAt, Reliance: result.Authority, Coverage: result.Authority}
	s.Detail.Item = mapWork(result.Work)
	for _, project := range result.Projects {
		s.Detail.Projects = append(s.Detail.Projects, project.ID+" ("+project.Role+")")
	}
	for _, event := range result.Events {
		s.Detail.History = append(s.Detail.History, event.OccurredAt+" "+event.Kind+" "+event.Reason)
	}
	s.Detail.Workflow = workflowText(result.Workflow)
	s.Detail.Edges = mapEdges(result.Edges)
	return s
}

func mapWork(item store.LauncherWork) launcher.RankedWork {
	out := launcher.RankedWork{ID: item.ID, Kind: item.Kind, Title: item.Title, Lifecycle: item.Lifecycle, Priority: item.Priority, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, ProjectCount: item.ProjectCount, Blocked: item.Blocked, Ready: item.Ready}
	for _, blocker := range item.Blockers {
		out.Blockers = append(out.Blockers, launcher.Blocker{ID: blocker.ID, Title: blocker.Title, Authority: blocker.Authority, Age: blocker.Age, External: blocker.External, ConditionID: blocker.ConditionID})
	}
	return out
}

func mapEdges(edges []store.RelationEdge) []launcher.RelationEdge {
	out := make([]launcher.RelationEdge, 0, len(edges))
	for _, e := range edges {
		out = append(out, launcher.RelationEdge{Kind: e.Kind, Source: e.Source, Target: e.Target})
	}
	return out
}

func relationTree(edges []store.RelationEdge, depth int, authority string) launcher.RelationTree {
	raw := mapEdges(edges)
	// A superseded chain is represented by its canonical successor once. The
	// stored relation remains untouched; this is only the launcher projection.
	successor := map[string]string{}
	for _, edge := range raw {
		if edge.Kind == "supersedes" {
			successor[edge.Source] = edge.Target
		}
	}
	for i := range raw {
		if raw[i].Kind != "supersedes" {
			continue
		}
		current, seen := raw[i].Target, map[string]bool{raw[i].Source: true}
		for next, ok := successor[current]; ok && !seen[current]; next, ok = successor[current] {
			seen[current] = true
			current = next
		}
		raw[i].Target = current
	}
	dedup := map[string]bool{}
	compact := raw[:0]
	for _, edge := range raw {
		key := edge.Kind + "|" + edge.Source + "|" + edge.Target
		if !dedup[key] {
			dedup[key] = true
			compact = append(compact, edge)
		}
	}
	raw = compact
	tree := launcher.RelationTree{Edges: raw, Depth: depth, Coverage: authority}
	canonical := map[string][]string{}
	nodes := map[string]bool{}
	indegree := map[string]int{}
	for _, edge := range raw {
		nodes[edge.Source], nodes[edge.Target] = true, true
		if edge.Kind != "depends_on" {
			canonical[edge.Source] = append(canonical[edge.Source], edge.Target)
			indegree[edge.Target]++
		}
		if edge.Source == edge.Target {
			tree.Invariant = "invariant_violation"
		}
	}
	ordered := make([]string, 0, len(nodes))
	for node := range nodes {
		ordered = append(ordered, node)
	}
	sort.Strings(ordered)
	for _, node := range ordered {
		if indegree[node] == 0 {
			tree.Roots = append(tree.Roots, node)
		}
	}
	// Filter the rendered graph at the accepted depth boundary. Inverse labels
	// use the same canonical distance and never create a false cycle.
	distance := map[string]int{}
	queue := []string{}
	for _, node := range ordered {
		if indegree[node] == 0 {
			distance[node] = 0
			queue = append(queue, node)
		}
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, next := range canonical[node] {
			candidate := distance[node] + 1
			if old, ok := distance[next]; !ok || candidate < old {
				distance[next] = candidate
				queue = append(queue, next)
			}
		}
	}
	if len(queue) == 0 && len(distance) == 0 {
		for _, node := range ordered {
			distance[node] = 0
		}
	}
	filtered := make([]launcher.RelationEdge, 0, len(raw))
	for _, edge := range raw {
		sourceDepth, sourceOK := distance[edge.Source]
		targetDepth, targetOK := distance[edge.Target]
		if !sourceOK || !targetOK || sourceDepth < depth && targetDepth <= depth {
			filtered = append(filtered, edge)
		}
	}
	if len(filtered) < len(raw) {
		tree.Coverage = "unavailable"
		tree.Unavailable = "relation depth limit reached"
	}
	tree.Edges = filtered
	undirected := map[string][]string{}
	for _, edge := range tree.Edges {
		undirected[edge.Source] = append(undirected[edge.Source], edge.Target)
		undirected[edge.Target] = append(undirected[edge.Target], edge.Source)
	}
	visited := map[string]bool{}
	componentNodes := map[string]bool{}
	for _, edge := range tree.Edges {
		componentNodes[edge.Source], componentNodes[edge.Target] = true, true
	}
	ordered = ordered[:0]
	for node := range componentNodes {
		ordered = append(ordered, node)
	}
	sort.Strings(ordered)
	for _, root := range ordered {
		if visited[root] {
			continue
		}
		component := []string{}
		queue := []string{root}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if visited[current] {
				continue
			}
			visited[current] = true
			component = append(component, current)
			queue = append(queue, undirected[current]...)
		}
		sort.Strings(component)
		tree.Components = append(tree.Components, component)
	}
	state := map[string]int{}
	var cycle func(string)
	cycle = func(node string) {
		if tree.Invariant == "invariant_violation" {
			return
		}
		state[node] = 1
		for _, next := range canonical[node] {
			if state[next] == 1 {
				tree.Invariant = "invariant_violation"
				return
			}
			if state[next] == 0 {
				cycle(next)
			}
		}
		state[node] = 2
	}
	for _, node := range ordered {
		if indegree[node] == 0 && state[node] == 0 {
			cycle(node)
		}
	}
	for _, node := range ordered {
		if state[node] == 0 {
			cycle(node)
		}
	}
	return tree
}

func joinOmissions(existing string, omissions []string) string {
	parts := make([]string, 0, len(omissions)+1)
	if existing != "" {
		parts = append(parts, existing)
	}
	parts = append(parts, omissions...)
	return strings.Join(parts, ", ")
}

func workflowText(workflow *store.WorkflowReadProjection) string {
	if workflow == nil {
		return "unavailable"
	}
	return workflow.CurrentStep
}

func (p *Port) readKnowledge(ctx context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	if request.Work != "" && request.Query == "" {
		result, err := p.Store.QueryQ10(ctx, store.Q10Request{Product: request.Product, Work: request.Work, AllowDegraded: true})
		if err != nil {
			return launcher.Snapshot{Screen: launcher.ScreenWork, AmbientProduct: request.Product, SelectedWorkID: request.Work, Coverage: "unreachable", StatusMessage: err.Error()}, err
		}
		s := launcher.Snapshot{Screen: launcher.ScreenWork, AmbientProduct: request.Product, SelectedWorkID: request.Work, Section: launcher.SectionKnowledge, QueryID: result.QueryID, ContractVersion: result.ContractVersion, SourceVersionWatermark: result.SourceVersionWatermark, Watermark: "q10", ObservedAt: result.Freshness.ObservedAt, Reliance: result.Authority, Coverage: result.Authority}
		s.Knowledge.Read = true
		if result.Status == "canonical" && result.Note != nil {
			s.Knowledge.State = "authoritative"
			s.Knowledge.Items = []launcher.KnowledgeItem{{ID: request.Work, Kind: "work_note", Title: "canonical work note", Reference: result.Note.NotePath, Watermark: result.Note.CommitOID}}
		} else if result.Authority != "authoritative" {
			s.Knowledge.State, s.Knowledge.Reason = "unavailable", "canonical_note_unavailable"
		} else {
			s.Knowledge.State = "authoritative-empty"
		}
		return s, nil
	}
	req := store.Q9Request{Product: request.Product, Text: request.Query, Limit: request.Limit, AllowDegraded: true}
	if request.Work != "" {
		req.Text = request.Query
	}
	result, err := p.Store.QueryQ9(ctx, req)
	if err != nil {
		return launcher.Snapshot{Screen: screenForWork(request.Work), AmbientProduct: request.Product, SelectedWorkID: request.Work, Coverage: "unreachable", StatusMessage: err.Error()}, err
	}
	s := launcher.Snapshot{Screen: screenForWork(request.Work), AmbientProduct: request.Product, SelectedWorkID: request.Work, Section: launcher.SectionKnowledge, QueryID: result.QueryID, ContractVersion: result.ContractVersion, SourceVersionWatermark: result.SourceVersionWatermark, Watermark: result.IndexWatermark, ObservedAt: result.Freshness.ObservedAt, Reliance: result.Authority, Coverage: result.Authority}
	s.Knowledge = mapKnowledge(result)
	return s, nil
}

func (p *Port) readSearch(ctx context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	result, err := p.Store.QueryLauncherSearch(ctx, store.LauncherSearchRequest{Product: request.Product, Query: request.Query, Limit: request.Limit})
	if err != nil {
		return launcher.Snapshot{Screen: screenForWork(request.Work), AmbientProduct: request.Product, SelectedWorkID: request.Work, Coverage: "unreachable", StatusMessage: err.Error()}, err
	}
	return snapshotFromSearch(result, request.Product, request.Work, request.Query), nil
}

func snapshotFromSearch(result store.LauncherSearchResult, product, work, query string) launcher.Snapshot {
	s := launcher.Snapshot{Screen: screenForWork(work), AmbientProduct: product, SelectedWorkID: work, Section: launcher.SectionRanked, QueryID: result.QueryID, ContractVersion: result.ContractVersion, SourceVersionWatermark: result.SourceVersionWatermark, Watermark: strconv.FormatInt(result.SourceVersionWatermark, 10), ObservedAt: result.Freshness.ObservedAt, Reliance: result.Authority, Coverage: result.Authority, OrderingKeys: append([]string(nil), result.OrderingKeys...), QueryResult: true, QuerySubmitted: query}
	for _, item := range result.Works {
		s.Ranked = append(s.Ranked, mapWork(item))
	}
	s.Knowledge = mapLauncherKnowledge(result)
	if len(result.Omissions) > 0 {
		s.Coverage = "unavailable"
		s.StatusMessage = "unavailable: " + strings.Join(result.Omissions, ", ")
	}
	return s
}

func mapLauncherKnowledge(result store.LauncherSearchResult) launcher.KnowledgeSection {
	section := launcher.KnowledgeSection{Read: true, State: "authoritative-empty", Watermark: result.KnowledgeWatermark}
	for _, item := range result.Knowledge {
		section.State = "authoritative"
		section.Items = append(section.Items, launcher.KnowledgeItem{ID: item.ID, Kind: item.Kind, Title: item.Title, Summary: item.Summary, Reference: item.NotePath, Watermark: result.KnowledgeWatermark})
	}
	if result.KnowledgeAuthority != "authoritative" || len(result.KnowledgeOmissions) > 0 {
		section.State = "unavailable"
		section.Reason = strings.Join(result.KnowledgeOmissions, ", ")
	}
	return section
}

func mapKnowledge(result store.Q9Result) launcher.KnowledgeSection {
	section := launcher.KnowledgeSection{Read: true, State: "authoritative-empty", Watermark: result.IndexWatermark}
	for _, item := range result.Items {
		section.State = "authoritative"
		section.Items = append(section.Items, launcher.KnowledgeItem{ID: item.ID, Kind: item.Kind, Title: item.Title, Summary: item.Summary, Reference: item.NotePath, Watermark: result.IndexWatermark})
	}
	if result.Authority != "authoritative" {
		section.State, section.Reason = "unavailable", "knowledge_index_lagging_or_unreachable"
	}
	return section
}

func screenForWork(work string) launcher.Screen {
	if work != "" {
		return launcher.ScreenWork
	}
	return launcher.ScreenProduct
}

func FromProductRows(result store.ProductRowResult) launcher.Snapshot {
	result = portfolio.Map(result)
	rows := make([]launcher.ProductRow, 0, len(result.Rows))
	for _, row := range result.Rows {
		present := launcher.ProductRow{
			ID: row.ProductID, Name: row.DisplayName, NameSuffix: row.DisplayNameSuffix,
			Stage:         row.Stage.Maturity + "/" + row.Stage.AudienceCommitment,
			StageMaturity: row.Stage.Maturity, StageAudienceCommitment: row.Stage.AudienceCommitment,
			Reliance: row.Reliance.Authority, RelianceReason: row.Reliance.Reason,
			RelianceObservedAt: row.Reliance.ObservedAt, RelianceAge: row.Reliance.Age,
			RelianceStale: row.Reliance.Stale, BlocksExecution: row.Reliance.BlocksExecution,
			RelianceOmissions: cloneStrings(row.Reliance.Omissions), CountsState: row.ActionCounts.State,
			FocusAbsentReason: row.FocusAbsentReason,
		}
		if row.ActionCounts.Values != nil {
			present.InProgress = row.ActionCounts.Values.InProgress
			present.Blocked = row.ActionCounts.Values.Blocked
			present.Ready = row.ActionCounts.Values.Ready
			present.ActiveProblems = row.ActionCounts.Values.ActiveProblems
			present.ApprovalRequired = row.ActionCounts.Values.ApprovalRequired
			present.Actions = present.InProgress + present.Blocked + present.Ready + present.ActiveProblems + present.ApprovalRequired
		}
		if row.ActionCounts.Unavailable != nil {
			present.UnavailableReason = row.ActionCounts.Unavailable.Reason
			present.UnavailableOmissions = cloneStrings(row.ActionCounts.Unavailable.Omissions)
		}
		if row.Focus != nil {
			present.Focus = row.Focus.Title
			present.FocusID = row.Focus.WorkID
			present.FocusWorkKind = row.Focus.WorkKind
			present.FocusLifecycle = row.Focus.Lifecycle
			present.FocusAttentionKind = row.Focus.AttentionKind
			present.FocusPriority = row.Focus.Priority
			present.FocusWorkflowStepLabel = row.Focus.WorkflowStepLabel
			present.FocusProjectCount = row.Focus.ProjectCount
			present.FocusStageContext = row.Focus.StageContext.Kind
			if row.Focus.StageContext.FocusOverride != nil {
				present.FocusStageOverrideMaturity = row.Focus.StageContext.FocusOverride.Maturity
				present.FocusStageOverrideAudience = row.Focus.StageContext.FocusOverride.AudienceCommitment
			}
		}
		rows = append(rows, present)
	}
	return launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, QueryID: result.QueryID, ContractVersion: result.ContractVersion,
		Watermark: strconv.FormatInt(result.SourceVersionWatermark, 10), SourceVersionWatermark: result.SourceVersionWatermark,
		ObservedAt: result.ObservedAt, Reliance: result.Authority, Coverage: result.Authority,
		OrderingKeys: append([]string(nil), result.OrderingKeys...), NextCursor: result.NextCursor,
		Rows: rows,
	}
}

func snapshotFromProductRows(result store.ProductRowResult) launcher.Snapshot {
	return FromProductRows(result)
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
