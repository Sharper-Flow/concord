// Package launcher owns the framework-independent launcher state and read port.
package launcher

import "context"

type Screen string

const (
	ScreenPortfolio Screen = "portfolio"
	ScreenProduct   Screen = "product"
)

type ReadKind string

const (
	ReadPortfolio ReadKind = "portfolio"
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
	FocusPriority              int64
	FocusWorkflowStepLabel     string
	FocusProjectCount          int
	FocusStageContext          string
	FocusStageOverrideMaturity string
	FocusStageOverrideAudience string
	FocusAbsentReason          string
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
}

type Model struct {
	port     ReadPort
	snapshot Snapshot
	width    int
	height   int
}

func New(port ReadPort) *Model {
	return &Model{port: port, width: 80, height: 24, snapshot: Snapshot{Screen: ScreenPortfolio, Coverage: "authoritative"}}
}

func (m *Model) Enter(ctx context.Context) error {
	return m.read(ctx, ReadRequest{Kind: ReadPortfolio, Limit: 20})
}

func (m *Model) SelectProduct(ctx context.Context, product string) error {
	_ = ctx
	for _, row := range m.snapshot.Rows {
		if row.ID == product {
			m.snapshot.Screen = ScreenProduct
			m.snapshot.AmbientProduct = product
			m.snapshot.StatusMessage = "not_implemented"
			m.snapshot.Query = ""
			return nil
		}
	}
	return nil
}

func (m *Model) SubmitQuery(ctx context.Context, query string) error {
	_ = ctx
	// S1 deliberately has a local filter only. Keep this method as a small
	// compatibility seam for callers of the renderer spike; it never broadens
	// the read surface or issues a cross-Product query.
	m.snapshot.Query = query
	return nil
}

func (m *Model) Refresh(ctx context.Context) error {
	if m.snapshot.Screen != ScreenPortfolio {
		return nil
	}
	return m.read(ctx, ReadRequest{Kind: ReadPortfolio, Limit: 20})
}

// Back returns from the typed S2 placeholder without performing a read.
func (m *Model) Back() error {
	if m.snapshot.Screen == ScreenProduct {
		m.snapshot.Screen = ScreenPortfolio
		m.snapshot.AmbientProduct = ""
		m.snapshot.StatusMessage = ""
	}
	return nil
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

func (m *Model) read(ctx context.Context, request ReadRequest) error {
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
		m.snapshot = snapshot
		return err
	}
	if snapshot.Screen == "" {
		snapshot.Screen = ScreenPortfolio
	}
	if snapshot.Coverage == "" {
		snapshot.Coverage = "authoritative"
	}
	m.snapshot = snapshot
	return nil
}
