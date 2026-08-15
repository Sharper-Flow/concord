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
	ReadWork      ReadKind = "work"
	ReadKnowledge ReadKind = "knowledge"
	ReadSearch    ReadKind = "search"
)

type Section string

const (
	SectionRelations Section = "relations"
	SectionRanked    Section = "ranked"
	SectionKnowledge Section = "knowledge"
)

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
	Edges       []RelationEdge
	Components  [][]string
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
	port         ReadPort
	snapshot     Snapshot
	width        int
	height       int
	section      Section
	cursor       int
	scroll       int
	productState Snapshot
	workState    Snapshot
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
			err := m.read(ctx, ReadRequest{Kind: ReadProduct, Product: product, Limit: 20, Section: SectionRelations})
			if err != nil {
				m.snapshot = Snapshot{Screen: ScreenPortfolio, Coverage: "unreachable", Reliance: "unreachable", StatusMessage: err.Error()}
				return err
			}
			m.snapshot.Session = SessionHandoff{ProductID: product}
			return err
		}
	}
	return nil
}

func (m *Model) SelectWork(ctx context.Context, work string) error {
	if m.snapshot.Screen != ScreenProduct || m.snapshot.AmbientProduct == "" {
		return nil
	}
	m.productState = m.snapshot
	err := m.read(ctx, ReadRequest{Kind: ReadWork, Product: m.snapshot.AmbientProduct, Work: work, Limit: 20, Section: SectionRelations})
	if err != nil {
		m.snapshot = m.productState
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
	switch m.snapshot.Screen {
	case ScreenWork:
		m.snapshot = m.productState
		m.section = m.snapshot.Section
	case ScreenProduct:
		m.snapshot.Screen = ScreenPortfolio
		m.snapshot.AmbientProduct = ""
		m.snapshot.StatusMessage = ""
	}
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
	snapshot := m.snapshot
	snapshot.Rows = make([]ProductRow, len(m.snapshot.Rows))
	copy(snapshot.Rows, m.snapshot.Rows)
	for i := range snapshot.Rows {
		snapshot.Rows[i].UnavailableOmissions = cloneStrings(m.snapshot.Rows[i].UnavailableOmissions)
		snapshot.Rows[i].RelianceOmissions = cloneStrings(m.snapshot.Rows[i].RelianceOmissions)
	}
	snapshot.OrderingKeys = cloneStrings(m.snapshot.OrderingKeys)
	snapshot.Ranked = cloneRanked(m.snapshot.Ranked)
	snapshot.Relations.Edges = append([]RelationEdge(nil), m.snapshot.Relations.Edges...)
	snapshot.Relations.Components = cloneStringGroups(m.snapshot.Relations.Components)
	snapshot.Relations.Roots = cloneStrings(m.snapshot.Relations.Roots)
	snapshot.Knowledge.Items = append([]KnowledgeItem(nil), m.snapshot.Knowledge.Items...)
	snapshot.Detail = m.snapshot.Detail
	snapshot.Detail.Item.Blockers = cloneBlockers(m.snapshot.Detail.Item.Blockers)
	snapshot.Detail.Projects = append([]string(nil), m.snapshot.Detail.Projects...)
	snapshot.Detail.History = append([]string(nil), m.snapshot.Detail.History...)
	snapshot.Detail.Edges = append([]RelationEdge(nil), m.snapshot.Detail.Edges...)
	if m.snapshot.NextCursor != nil {
		cursor := *m.snapshot.NextCursor
		snapshot.NextCursor = &cursor
	}
	return snapshot
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
	if request.Kind == ReadProduct || request.Kind == ReadWork || request.Kind == ReadKnowledge || request.Kind == ReadSearch {
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
	knowledge.Ranked = previous.Ranked
	knowledge.Relations = previous.Relations
	knowledge.Detail = previous.Detail
	knowledge.Detail.Knowledge = knowledge.Knowledge
	knowledge.Section = SectionKnowledge
	knowledge.Session = previous.Session
	return knowledge
}
