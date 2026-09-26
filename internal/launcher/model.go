// Package launcher owns the framework-independent launcher state and read port.
package launcher

import (
	"context"
	"sort"
	"strings"
)

type ReadKind string

const (
	ReadPortfolio  ReadKind = "portfolio"
	ReadProduct    ReadKind = "product"
	ReadProjects   ReadKind = "projects"
	ReadDomains    ReadKind = "domains"
	ReadSearch     ReadKind = "search"
	ReadCandidates ReadKind = "candidates"
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
	IssueKey                               string
	External                               bool
}

type RankedWork struct {
	ID, Kind, Title, Lifecycle string
	LinearIssueKey             string
	LinearIssueURL             string
	Worktree                   string
	ProjectID                  string
	Live                       int
	Backlog                    bool
	Priority                   int64
	Urgency                    string
	CreatedAt, UpdatedAt       string
	TerminalAt                 string
	ProjectCount               int
	Blocked, Ready, Terminal   bool
	Blockers                   []Blocker
}

// Readiness is the single derivation of the drill-down readiness marker.
// Terminal is checked first: a terminal item is not actionable, so blocker and
// ready state do not apply to it.
func (item RankedWork) Readiness() string {
	switch {
	case item.Terminal:
		return "terminal"
	case item.Blocked:
		return "blocked"
	case item.Ready:
		return "ready"
	default:
		return "active"
	}
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

// DomainSection is the Domain navigation body. Unavailable is typed and
// distinct from authoritative-empty: an absent registry never renders as an
// empty Domain list. The registry, relation, and overlap reads fail
// independently at their bounds, so a bound on overlaps or relations marks
// only that part and never withholds complete registry rows or their
// watermark. The renderer shows the section only when it is abnormal.
type DomainSection struct {
	Read               bool
	State              string
	Reason             string
	Registry           string
	RegistryIncomplete bool
	RelationsTruncated bool
	OverlapsTruncated  bool
	Domains            []DomainRow
	Relations          []DomainRelationEdge
	Overlaps           []OverlapPair
}

type SessionHandoff struct {
	ProductID   string
	WorkID      string
	Prompt      string
	ProjectPath string
	Agent       string
}

type ProjectOption struct {
	ID, Name, Role, Path string
}

const DefaultSessionAgent = "concord-1"

type CandidateKind string

const (
	CandidateProduct CandidateKind = "product"
	CandidateWork    CandidateKind = "work"
	CandidateProject CandidateKind = "project"
)

// Candidate is one launcher entry. It contains display data only. The launcher
// never treats a candidate as a second store authority.
type Candidate struct {
	ID             string        `json:"id"`
	Kind           CandidateKind `json:"kind"`
	Name           string        `json:"name"`
	LinearIssueKey string        `json:"linear_issue_key,omitempty"`
	State          string        `json:"state,omitempty"`
	Blocked        bool          `json:"blocked"`
	Path           string        `json:"path,omitempty"`
	ProductID      string        `json:"product_id,omitempty"`
	WorkID         string        `json:"work_id,omitempty"`
	Worktree       string        `json:"worktree,omitempty"`
	Pinned         bool          `json:"pinned"`
	LastUsed       string        `json:"last_used,omitempty"`
	Rank           int           `json:"rank"`
	Live           int           `json:"live_sessions"`
	Available      bool          `json:"available"`
}

type ProbeStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// ProbePort supplies optional local status. Probe failure is preview data, not
// a launcher read failure.
type ProbePort interface {
	Probe(context.Context) []ProbeStatus
}

// CandidatePort supplies the replacement launcher's bounded candidate read.
type CandidatePort interface {
	Candidates(context.Context, int) ([]Candidate, error)
}

type CandidatePreview struct {
	Worktree string
	State    string
	Blocked  bool
	Sessions []string
}

type Snapshot struct {
	Screen                 Surface
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
	Projects               []ProjectOption
	Candidates             []Candidate
	Preview                CandidatePreview
	Probes                 []ProbeStatus
	StatusMessage          string
	FirstRun               bool
	Domains                DomainSection
	Relations              RelationTree
	Ranked                 []RankedWork
	QueryResult            bool
	QuerySubmitted         string
	SelectedWorkID         string
	Session                SessionHandoff
	ProjectSelect          bool
	ActiveWorkOnly         bool
	Backlog                bool
}

// ProjectPort supplies the Product's locally registered Projects. It is a
// read-only extension of ReadPort so the launcher can omit Projects without a
// recorded repository path.
type ProjectPort interface {
	Projects(context.Context, string) ([]ProjectOption, error)
}

type IssuePort interface {
	ResolveIssue(context.Context, string, string) (SessionHandoff, error)
}

type Model struct {
	port       ReadPort
	snapshot   Snapshot
	width      int
	height     int
	navigation []Snapshot
}

func New(port ReadPort) *Model {
	return &Model{port: port, width: 80, height: 24, snapshot: Snapshot{Screen: SurfacePortfolio, Coverage: "authoritative"}}
}

func (m *Model) Enter(ctx context.Context) error {
	return m.read(ctx, ReadRequest{Kind: ReadPortfolio, Limit: 100})
}

// SelectProduct carries the one Product-selection invariant shared by the
// portfolio-row route and the candidate route: a selection by Product ID
// reads that Product and shows its work list. The read is the Product
// coordination read, which carries both the ranked work rows and the Domain
// context, so the work list is the entry view and the Domain and law data
// surfaces only when it is abnormal. Callers bound the selection to a visible
// entry; visibility in a previous snapshot's rows is not required.
func (m *Model) SelectProduct(ctx context.Context, product string) error {
	m.navigation = append(m.navigation, m.Snapshot())
	err := m.read(ctx, ReadRequest{Kind: ReadDomains, Product: product, Limit: 100})
	if err != nil {
		m.navigation = m.navigation[:len(m.navigation)-1]
		m.snapshot = Snapshot{Screen: SurfacePortfolio, Coverage: "unreachable", Reliance: "unreachable", StatusMessage: err.Error()}
		return err
	}
	m.snapshot.Session = SessionHandoff{ProductID: product, Agent: DefaultSessionAgent}
	return nil
}

func (m *Model) SelectProjects(ctx context.Context) error {
	port, ok := m.port.(ProjectPort)
	if !ok || m.snapshot.AmbientProduct == "" {
		return nil
	}
	projects, err := port.Projects(ctx, m.snapshot.AmbientProduct)
	if err != nil {
		return err
	}
	m.snapshot.Projects = append([]ProjectOption(nil), projects...)
	m.snapshot.ProjectSelect = true
	return nil
}

func (m *Model) SelectProject(project ProjectOption) {
	m.snapshot.ProjectSelect = false
	m.snapshot.Projects = nil
	m.snapshot.Session = SessionHandoff{ProjectPath: project.Path, Agent: DefaultSessionAgent}
}

func (m *Model) BackProjects() {
	m.snapshot.ProjectSelect = false
	m.snapshot.Projects = nil
}

func (m *Model) ResolveIssue(ctx context.Context, key string) (SessionHandoff, error) {
	port, ok := m.port.(IssuePort)
	if !ok {
		return SessionHandoff{}, nil
	}
	return port.ResolveIssue(ctx, key, "")
}

func (m *Model) SubmitQuery(ctx context.Context, query string) error {
	// S1 carries no semantic-query binding, so a query submitted against the
	// portfolio issues no read. The ambient guard below is not a substitute: an
	// S1 snapshot with a non-empty ambient Product is representable.
	if m.snapshot.Screen == SurfacePortfolio {
		return nil
	}
	if m.snapshot.AmbientProduct == "" {
		return nil
	}
	return m.read(ctx, ReadRequest{Kind: ReadSearch, Product: m.snapshot.AmbientProduct, Work: m.snapshot.SelectedWorkID, Query: query, Limit: 20})
}

func (m *Model) Refresh(ctx context.Context) error {
	s := m.snapshot
	switch s.Screen {
	case SurfacePortfolio:
		return m.read(ctx, ReadRequest{Kind: ReadPortfolio, Limit: 100})
	case SurfaceProduct:
		if s.ProjectSelect {
			return m.SelectProjects(ctx)
		}
		// The Domain read composes the Product work read, so one refresh
		// re-reads the work list and the abnormal-only Domain context in the
		// same bounded transaction the entry read used.
		return m.read(ctx, ReadRequest{Kind: ReadDomains, Product: s.AmbientProduct, Limit: 100})
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
	return nil
}

func (m *Model) Handoff() SessionHandoff { return m.snapshot.Session }

// Candidates returns the current bounded candidate projection.
func (m *Model) Candidates() []Candidate { return append([]Candidate(nil), m.snapshot.Candidates...) }

func (m *Model) RestoreSnapshot(snapshot Snapshot) {
	m.snapshot = snapshot
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
	cloned.Relations.Roots = cloneStrings(snapshot.Relations.Roots)
	cloned.Domains.Domains = append([]DomainRow(nil), snapshot.Domains.Domains...)
	cloned.Domains.Relations = append([]DomainRelationEdge(nil), snapshot.Domains.Relations...)
	cloned.Domains.Overlaps = nil
	for _, pair := range snapshot.Domains.Overlaps {
		pair.SharedDomains = cloneStrings(pair.SharedDomains)
		cloned.Domains.Overlaps = append(cloned.Domains.Overlaps, pair)
	}
	if snapshot.NextCursor != nil {
		cursor := *snapshot.NextCursor
		cloned.NextCursor = &cursor
	}
	cloned.Candidates = append([]Candidate(nil), snapshot.Candidates...)
	cloned.Projects = append([]ProjectOption(nil), snapshot.Projects...)
	cloned.Probes = append([]ProbeStatus(nil), snapshot.Probes...)
	cloned.Preview.Sessions = cloneStrings(snapshot.Preview.Sessions)
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
	for i := range out {
		out[i] = cloneStrings(values[i])
	}
	return out
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

func cloneBlockers(values []Blocker) []Blocker {
	if values == nil {
		return nil
	}
	return append([]Blocker{}, values...)
}

// OrderCandidates applies the contract order: pins first, then most-recently
// used values, then the stored rank and stable identity.
func OrderCandidates(values []Candidate) []Candidate {
	out := append([]Candidate(nil), values...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		if out[i].LastUsed != out[j].LastUsed {
			return out[i].LastUsed > out[j].LastUsed
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func FilterCandidates(values []Candidate, query string) []Candidate {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return append([]Candidate(nil), values...)
	}
	out := make([]Candidate, 0, len(values))
	for _, candidate := range values {
		if strings.Contains(strings.ToLower(candidate.ID), needle) || strings.Contains(strings.ToLower(candidate.Name), needle) || strings.Contains(strings.ToLower(candidate.Path), needle) || strings.Contains(strings.ToLower(candidate.LinearIssueKey), needle) {
			out = append(out, candidate)
		}
	}
	return out
}

func (m *Model) read(ctx context.Context, request ReadRequest) error {
	snapshot, err := m.port.Read(ctx, request)
	if err != nil {
		// A failed foreground read must never leave the previous rows looking
		// current. Read ports may return typed unavailable state alongside the
		// error; retain that state, clear rows, and let the caller render it.
		snapshot.Rows = nil
		snapshot.Candidates = nil
		if snapshot.Screen == "" {
			snapshot.Screen = SurfacePortfolio
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
		if probes, ok := m.port.(ProbePort); ok {
			snapshot.Probes = append([]ProbeStatus(nil), probes.Probe(ctx)...)
		}
		m.snapshot = snapshot
		return err
	}
	if snapshot.Screen == "" {
		snapshot.Screen = m.snapshot.Screen
	}
	if request.Kind == ReadProduct || request.Kind == ReadDomains || request.Kind == ReadSearch {
		if snapshot.AmbientProduct == "" {
			snapshot.AmbientProduct = request.Product
		}
		if request.Work != "" && snapshot.SelectedWorkID == "" {
			snapshot.SelectedWorkID = request.Work
		}
	}
	if snapshot.Coverage == "" {
		snapshot.Coverage = "authoritative"
	}
	if request.Kind == ReadPortfolio {
		if candidates, ok := m.port.(CandidatePort); ok {
			values, candidateErr := candidates.Candidates(ctx, request.Limit)
			if candidateErr == nil {
				snapshot.Candidates = OrderCandidates(values)
			} else if snapshot.StatusMessage == "" {
				snapshot.StatusMessage = "candidate preview unavailable: " + candidateErr.Error()
			}
		}
	}
	if probes, ok := m.port.(ProbePort); ok {
		snapshot.Probes = append([]ProbeStatus(nil), probes.Probe(ctx)...)
	}
	m.snapshot = snapshot
	return nil
}
