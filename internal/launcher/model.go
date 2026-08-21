// Package launcher owns the framework-independent launcher state and read port.
package launcher

import "context"

type Screen string

const (
	ScreenPortfolio Screen = "portfolio"
	ScreenProduct   Screen = "product"
	ScreenWork      Screen = "work"
)

type ReadKind string

const (
	ReadPortfolio ReadKind = "portfolio"
	ReadProduct   ReadKind = "product"
	ReadDomains   ReadKind = "domains"
	ReadWork      ReadKind = "work"
	ReadKnowledge ReadKind = "knowledge"
	ReadSearch    ReadKind = "search"
)

type Section string

const (
	// SectionDomains is S2's primary section: Domain hierarchy, architecture
	// relations, and unresolved overlap render before the subordinate C17
	// work modes (CD-0041 amended S2; no fourth screen).
	SectionDomains   Section = "domains"
	SectionRelations Section = "relations"
	SectionRanked    Section = "ranked"
	SectionKnowledge Section = "knowledge"
)

// S2Panel identifies one answer in Product screen order. The order is a
// contract, not a ranking computed by the launcher.
type S2Panel string

const (
	S2PanelDomain  S2Panel = "domain"
	S2PanelBlocked S2Panel = "blocked"
	S2PanelNext    S2Panel = "next"
)

func S2PanelOrder() []S2Panel {
	return []S2Panel{S2PanelDomain, S2PanelBlocked, S2PanelNext}
}

// ReadPort is the only authority the launcher can read. It deliberately does
// not expose store or domain types.
type ReadPort interface {
	Read(context.Context, ReadRequest) (Snapshot, error)
}

type ReadRequest struct {
	Kind    ReadKind
	Product string
	Query   string
	Cursor  string
	Limit   int
	Work    string
	Section Section
}

type ProductRow struct {
	ID                         string
	Name                       string
	NameSuffix                 string
	Stage                      string
	StageMaturity              string
	StageAudienceCommitment    string
	Reliance                   string
	RelianceReason             string
	RelianceObservedAt         string
	RelianceAge                int64
	RelianceStale              bool
	BlocksExecution            bool
	RelianceOmissions          []string
	Actions                    int
	InProgress                 int
	Blocked                    int
	Ready                      int
	ActiveProblems             int
	ApprovalRequired           int
	OverdueAwaits              int
	CountsState                string
	UnavailableReason          string
	UnavailableOmissions       []string
	Focus                      string
	FocusID                    string
	FocusWorkKind              string
	FocusLifecycle             string
	FocusAttentionKind         string
	FocusBlockedSessionCount   int
	FocusOldestBlockedSession  string
	FocusPriority              int64
	FocusWorkflowStepLabel     string
	FocusProjectCount          int
	FocusStageContext          string
	FocusStageOverrideMaturity string
	FocusStageOverrideAudience string
	FocusAbsentReason          string
}

type RelationEdge struct {
	Kind, Source, Target string
}

type RelationTree struct {
	Edges []RelationEdge
	// Clusters are undirected graph clusters of the rendered work-relation
	// graph (graph structure labels, unrelated to Product Domains).
	Clusters    [][]string
	Roots       []string
	Invariant   string
	Depth       int
	Coverage    string
	Unavailable string
}

type Blocker struct {
	ID, Title, Authority, Age, ConditionID string
	External                               bool
}

type RankedWork struct {
	ID, Kind, Title, Lifecycle string
	Priority                   int64
	Urgency                    string
	CreatedAt, UpdatedAt       string
	ProjectCount               int
	Blocked, Ready             bool
	Blockers                   []Blocker
}

type DomainRow struct {
	ID, Name, Purpose, ParentID string
	Home                        bool
	CurrentLawCount             int
	ActiveWorkCount             int
}

type DomainRelationEdge struct {
	Kind, Source, Target, State string
}

type OverlapPair struct {
	From, To, State string
	SharedDomains   []string
}

// DomainSection is S2's Domain navigation body. Unavailable is typed and
// distinct from authoritative-empty: an absent registry never renders as an
// empty Domain list.
type DomainSection struct {
	Read      bool
	State     string
	Reason    string
	Registry  string
	Domains   []DomainRow
	Relations []DomainRelationEdge
	Overlaps  []OverlapPair
	Truncated bool
}

type S2DomainSummary struct {
	Evaluated          bool
	UnavailableReason  string
	UnresolvedOverlaps []OverlapPair
}

type S2PanelSummary struct {
	Panel  S2Panel
	Domain S2DomainSummary
	Work   *RankedWork
}

// S2AnswerStack is the framework-independent composition of the values the
// store already materialized for the Product screen.
type S2AnswerStack struct {
	Panels  []S2Panel
	Domain  S2PanelSummary
	Blocked S2PanelSummary
	Next    S2PanelSummary
}

type KnowledgeItem struct {
	ID, Kind, Title, Summary, Reference, Watermark string
}

type KnowledgeSection struct {
	Items                    []KnowledgeItem
	State, Reason, Watermark string
	Read                     bool
}

type WorkDetail struct {
	Item      RankedWork
	Projects  []string
	History   []string
	Workflow  string
	Edges     []RelationEdge
	Knowledge KnowledgeSection
}

type SessionHandoff struct {
	ProductID string
	WorkID    string
}

type Snapshot struct {
	Screen                 Screen
	AmbientProduct         string
	QueryID                string
	ContractVersion        string
	SourceVersionWatermark int64
	Watermark              string
	ObservedAt             string
	Reliance               string
	Coverage               string
	OrderingKeys           []string
	NextCursor             *string
	Rows                   []ProductRow
	Query                  string
	StatusMessage          string
	FirstRun               bool
	Section                Section
	PanelFocus             S2Panel
	Domains                DomainSection
	Relations              RelationTree
	Ranked                 []RankedWork
	Knowledge              KnowledgeSection
	Detail                 WorkDetail
	QueryResult            bool
	QuerySubmitted         string
	SelectedWorkID         string
	Session                SessionHandoff
}

type Model struct {
	port       ReadPort
	snapshot   Snapshot
	width      int
	height     int
	section    Section
	cursor     int
	scroll     int
	navigation []Snapshot
}

func New(port ReadPort) *Model {
	return &Model{port: port, width: 80, height: 24, section: SectionRelations, snapshot: Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative"}}
}

func (m *Model) Enter(ctx context.Context) error {
	return m.read(ctx, ReadRequest{Kind: ReadPortfolio, Limit: 20})
}

func (m *Model) SelectProduct(ctx context.Context, product string) error {
	for _, row := range m.snapshot.Rows {
		if row.ID == product {
			m.navigation = append(m.navigation, m.Snapshot())
			err := m.read(ctx, ReadRequest{Kind: ReadDomains, Product: product, Limit: 20, Section: SectionDomains})
			if err != nil {
				m.navigation = m.navigation[:len(m.navigation)-1]
				m.snapshot = Snapshot{Screen: ScreenPortfolio, Coverage: "unreachable", Reliance: "unreachable", StatusMessage: err.Error()}
				return err
			}
			// The Domain panel is focused on entry, so load its bounded knowledge
			// section through the existing lazy read path. A failed knowledge read
			// remains typed in the Product snapshot without losing navigation.
			_ = m.EnsureKnowledge(ctx)
			m.snapshot.Session = SessionHandoff{ProductID: product}
			m.snapshot.PanelFocus = S2PanelDomain
			m.snapshot.Section = SectionDomains
			m.section = SectionDomains
			return err
		}
	}
	return nil
}

func (m *Model) SelectWork(ctx context.Context, work string) error {
	if m.snapshot.Screen != ScreenProduct || m.snapshot.AmbientProduct == "" {
		return nil
	}
	previous := m.Snapshot()
	m.navigation = append(m.navigation, previous)
	err := m.read(ctx, ReadRequest{Kind: ReadWork, Product: m.snapshot.AmbientProduct, Work: work, Limit: 20, Section: SectionRelations})
	if err != nil {
		m.navigation = m.navigation[:len(m.navigation)-1]
		m.snapshot = previous
		m.snapshot.Coverage, m.snapshot.Reliance = "unreachable", "unreachable"
		m.snapshot.StatusMessage = err.Error()
		m.snapshot.Ranked, m.snapshot.Relations = nil, RelationTree{}
		return err
	}
	m.snapshot.Session = SessionHandoff{ProductID: m.snapshot.AmbientProduct, WorkID: work}
	return err
}

func (m *Model) SubmitQuery(ctx context.Context, query string) error {
	if m.snapshot.Screen == ScreenPortfolio {
		m.snapshot.Query = query
		return nil
	}
	if m.snapshot.AmbientProduct == "" {
		return nil
	}
	return m.read(ctx, ReadRequest{Kind: ReadSearch, Product: m.snapshot.AmbientProduct, Work: m.snapshot.SelectedWorkID, Query: query, Limit: 20, Section: m.section})
}

func (m *Model) Refresh(ctx context.Context) error {
	s := m.snapshot
	switch s.Screen {
	case ScreenPortfolio:
		return m.read(ctx, ReadRequest{Kind: ReadPortfolio, Limit: 20})
	case ScreenProduct:
		if s.Section == SectionKnowledge {
			return m.read(ctx, ReadRequest{Kind: ReadKnowledge, Product: s.AmbientProduct, Limit: 20, Section: SectionKnowledge})
		}
		if s.Section == SectionDomains {
			return m.read(ctx, ReadRequest{Kind: ReadDomains, Product: s.AmbientProduct, Limit: 20, Section: SectionDomains})
		}
		return m.read(ctx, ReadRequest{Kind: ReadProduct, Product: s.AmbientProduct, Limit: 20, Section: s.Section})
	case ScreenWork:
		if s.Section == SectionKnowledge {
			return m.read(ctx, ReadRequest{Kind: ReadKnowledge, Product: s.AmbientProduct, Work: s.SelectedWorkID, Limit: 20, Section: SectionKnowledge})
		}
		return m.read(ctx, ReadRequest{Kind: ReadWork, Product: s.AmbientProduct, Work: s.SelectedWorkID, Limit: 20, Section: s.Section})
	}
	return nil
}

// Back changes the in-memory navigation stack without performing a read.
func (m *Model) Back() error {
	if len(m.navigation) == 0 {
		return nil
	}
	last := len(m.navigation) - 1
	m.snapshot = m.navigation[last]
	m.navigation = m.navigation[:last]
	m.section = m.snapshot.Section
	return nil
}

func (m *Model) SetSection(section Section) error {
	if m.snapshot.Screen != ScreenProduct && m.snapshot.Screen != ScreenWork {
		return nil
	}
	m.section = section
	m.snapshot.Section = section
	return nil
}

func (m *Model) PanelFocus() S2Panel {
	if m.snapshot.PanelFocus == "" {
		return S2PanelDomain
	}
	return m.snapshot.PanelFocus
}

func (m *Model) SetPanelFocus(panel S2Panel) error {
	if m.snapshot.Screen != ScreenProduct {
		return nil
	}
	if !isS2Panel(panel) {
		return nil
	}
	m.snapshot.PanelFocus = panel
	if panel == S2PanelDomain {
		m.snapshot.Section = SectionDomains
		m.section = SectionDomains
	} else {
		m.snapshot.Section = SectionRanked
		m.section = SectionRanked
	}
	return nil
}

func (m *Model) CyclePanelFocus() S2Panel {
	current := m.PanelFocus()
	if m.snapshot.Screen != ScreenProduct {
		return current
	}
	order := S2PanelOrder()
	for i, panel := range order {
		if panel == current {
			next := order[(i+1)%len(order)]
			_ = m.SetPanelFocus(next)
			return next
		}
	}
	_ = m.SetPanelFocus(S2PanelDomain)
	return S2PanelDomain
}

func (m *Model) EnsureKnowledge(ctx context.Context) error {
	if m.snapshot.Screen != ScreenProduct && m.snapshot.Screen != ScreenWork {
		return nil
	}
	if m.snapshot.Knowledge.Read {
		return nil
	}
	m.section = SectionKnowledge
	return m.read(ctx, ReadRequest{Kind: ReadKnowledge, Product: m.snapshot.AmbientProduct, Work: m.snapshot.SelectedWorkID, Limit: 20, Section: SectionKnowledge})
}

func (m *Model) Section() Section { return m.section }

func (m *Model) Handoff() SessionHandoff { return m.snapshot.Session }

func (m *Model) RestoreSnapshot(snapshot Snapshot) {
	if snapshot.Screen == ScreenProduct {
		if snapshot.Section == "" {
			snapshot.Section = SectionDomains
		}
		if snapshot.PanelFocus == "" {
			snapshot.PanelFocus = S2PanelDomain
			if snapshot.Section == SectionRanked {
				snapshot.PanelFocus = S2PanelNext
			}
		}
	}
	m.snapshot = snapshot
	m.section = snapshot.Section
}

func (m *Model) Resize(width, height int) {
	if width > 0 {
		m.width = width
	}
	if height > 0 {
		m.height = height
	}
}

func (m *Model) Snapshot() Snapshot {
	return cloneSnapshot(m.snapshot)
}

func (snapshot Snapshot) S2AnswerStack() S2AnswerStack {
	stack := S2AnswerStack{Panels: S2PanelOrder()}
	stack.Domain = S2PanelSummary{Panel: S2PanelDomain, Domain: domainSummary(snapshot.Domains)}
	if len(snapshot.Ranked) > 0 {
		stack.Blocked = S2PanelSummary{Panel: S2PanelBlocked, Work: &snapshot.Ranked[0]}
		stack.Next = S2PanelSummary{Panel: S2PanelNext, Work: &snapshot.Ranked[0]}
	} else {
		stack.Blocked.Panel = S2PanelBlocked
		stack.Next.Panel = S2PanelNext
	}
	return stack
}

func domainSummary(section DomainSection) S2DomainSummary {
	summary := S2DomainSummary{}
	if section.State == "unavailable" || !section.Read {
		summary.UnavailableReason = section.Reason
		if summary.UnavailableReason == "" && !section.Read {
			summary.UnavailableReason = "not_read"
		}
		return summary
	}
	summary.Evaluated = true
	for _, pair := range section.Overlaps {
		if pair.State == "absent" {
			summary.UnresolvedOverlaps = append(summary.UnresolvedOverlaps, pair)
		}
	}
	return summary
}

func isS2Panel(panel S2Panel) bool {
	return panel == S2PanelDomain || panel == S2PanelBlocked || panel == S2PanelNext
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	cloned := snapshot
	cloned.Rows = make([]ProductRow, len(snapshot.Rows))
	copy(cloned.Rows, snapshot.Rows)
	for i := range cloned.Rows {
		cloned.Rows[i].UnavailableOmissions = cloneStrings(snapshot.Rows[i].UnavailableOmissions)
		cloned.Rows[i].RelianceOmissions = cloneStrings(snapshot.Rows[i].RelianceOmissions)
	}
	cloned.OrderingKeys = cloneStrings(snapshot.OrderingKeys)
	cloned.Ranked = cloneRanked(snapshot.Ranked)
	cloned.Relations.Edges = append([]RelationEdge(nil), snapshot.Relations.Edges...)
	cloned.Relations.Clusters = cloneStringGroups(snapshot.Relations.Clusters)
	cloned.Domains.Read = snapshot.Domains.Read
	cloned.Domains.Registry = snapshot.Domains.Registry
	cloned.Domains.State, cloned.Domains.Reason = snapshot.Domains.State, snapshot.Domains.Reason
	cloned.Domains.Truncated = snapshot.Domains.Truncated
	cloned.Domains.Domains = nil
	for _, domain := range snapshot.Domains.Domains {
		cloned.Domains.Domains = append(cloned.Domains.Domains, domain)
	}
	cloned.Domains.Relations = nil
	for _, relation := range snapshot.Domains.Relations {
		cloned.Domains.Relations = append(cloned.Domains.Relations, relation)
	}
	cloned.Domains.Overlaps = nil
	for _, pair := range snapshot.Domains.Overlaps {
		pair.SharedDomains = cloneStrings(pair.SharedDomains)
		cloned.Domains.Overlaps = append(cloned.Domains.Overlaps, pair)
	}
	cloned.Relations.Roots = cloneStrings(snapshot.Relations.Roots)
	cloned.Knowledge.Items = append([]KnowledgeItem(nil), snapshot.Knowledge.Items...)
	cloned.Detail = snapshot.Detail
	cloned.Detail.Item.Blockers = cloneBlockers(snapshot.Detail.Item.Blockers)
	cloned.Detail.Projects = append([]string(nil), snapshot.Detail.Projects...)
	cloned.Detail.History = append([]string(nil), snapshot.Detail.History...)
	cloned.Detail.Edges = append([]RelationEdge(nil), snapshot.Detail.Edges...)
	if snapshot.NextCursor != nil {
		cursor := *snapshot.NextCursor
		cloned.NextCursor = &cursor
	}
	return cloned
}

func (m *Model) Size() (width, height int) { return m.width, m.height }

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func cloneStringGroups(values [][]string) [][]string {
	if values == nil {
		return nil
	}
	out := make([][]string, len(values))
	for i := range values {
		out[i] = cloneStrings(values[i])
	}
	return out
}

func cloneBlockers(values []Blocker) []Blocker {
	if values == nil {
		return nil
	}
	return append([]Blocker{}, values...)
}
func cloneRanked(values []RankedWork) []RankedWork {
	if values == nil {
		return nil
	}
	out := make([]RankedWork, len(values))
	copy(out, values)
	for i := range out {
		out[i].Blockers = cloneBlockers(values[i].Blockers)
	}
	return out
}

func (m *Model) read(ctx context.Context, request ReadRequest) error {
	previous := m.snapshot
	snapshot, err := m.port.Read(ctx, request)
	if err != nil {
		// A failed foreground read must never leave the previous rows looking
		// current. Read ports may return typed unavailable state alongside the
		// error; retain that state, clear rows, and let the caller render it.
		snapshot.Rows = nil
		if snapshot.Screen == "" {
			snapshot.Screen = ScreenPortfolio
		}
		if snapshot.Coverage == "" {
			snapshot.Coverage = "unreachable"
		}
		if snapshot.Reliance == "" {
			snapshot.Reliance = "unreachable"
		}
		if snapshot.StatusMessage == "" {
			snapshot.StatusMessage = err.Error()
		}
		if request.Kind == ReadKnowledge {
			snapshot = mergeKnowledgeSnapshot(previous, snapshot)
		}
		m.snapshot = snapshot
		return err
	}
	if snapshot.Screen == "" {
		snapshot.Screen = m.snapshot.Screen
	}
	if request.Kind == ReadWork && snapshot.Screen == ScreenPortfolio {
		snapshot.Screen = ScreenWork
	}
	if request.Kind == ReadProduct || request.Kind == ReadDomains || request.Kind == ReadWork || request.Kind == ReadKnowledge || request.Kind == ReadSearch {
		if snapshot.AmbientProduct == "" {
			snapshot.AmbientProduct = request.Product
		}
		if request.Work != "" && snapshot.SelectedWorkID == "" {
			snapshot.SelectedWorkID = request.Work
		}
	}
	if request.Kind == ReadKnowledge {
		snapshot = mergeKnowledgeSnapshot(previous, snapshot)
	}
	if snapshot.Screen == ScreenProduct && snapshot.PanelFocus == "" {
		snapshot.PanelFocus = previous.PanelFocus
		if snapshot.PanelFocus == "" {
			snapshot.PanelFocus = S2PanelDomain
			if snapshot.Section == SectionRanked {
				snapshot.PanelFocus = S2PanelNext
			}
		}
	}
	if snapshot.Coverage == "" {
		snapshot.Coverage = "authoritative"
	}
	m.snapshot = snapshot
	if snapshot.Screen == ScreenProduct || snapshot.Screen == ScreenWork {
		m.section = snapshot.Section
	}
	return nil
}

func mergeKnowledgeSnapshot(previous, knowledge Snapshot) Snapshot {
	knowledge.Screen = previous.Screen
	knowledge.AmbientProduct = previous.AmbientProduct
	knowledge.SelectedWorkID = previous.SelectedWorkID
	knowledge.Rows = previous.Rows
	knowledge.Domains = previous.Domains
	knowledge.Ranked = previous.Ranked
	knowledge.Relations = previous.Relations
	knowledge.Detail = previous.Detail
	knowledge.Detail.Knowledge = knowledge.Knowledge
	knowledge.Section = SectionKnowledge
	knowledge.PanelFocus = previous.PanelFocus
	knowledge.Session = previous.Session
	return knowledge
}
