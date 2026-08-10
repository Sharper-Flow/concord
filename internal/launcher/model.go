// Package launcher owns the framework-independent launcher state and read port.
package launcher

import "context"

type Screen string

const (
	ScreenPortfolio Screen = "portfolio"
	ScreenProduct   Screen = "product"
	ScreenQuery     Screen = "query"
)

type ReadKind string

const (
	ReadPortfolio ReadKind = "portfolio"
	ReadProduct   ReadKind = "product"
	ReadQuery     ReadKind = "query"
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
	Limit   int
}

type ProductRow struct {
	ID       string
	Name     string
	Stage    string
	Reliance string
	Actions  int
	Focus    string
}

type Snapshot struct {
	Screen         Screen
	AmbientProduct string
	Watermark      string
	ObservedAt     string
	Reliance       string
	Coverage       string
	Rows           []ProductRow
	Query          string
	StatusMessage  string
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
	return m.read(ctx, ReadRequest{Kind: ReadProduct, Product: product, Limit: 20})
}

func (m *Model) SubmitQuery(ctx context.Context, query string) error {
	return m.read(ctx, ReadRequest{Kind: ReadQuery, Product: m.snapshot.AmbientProduct, Query: query, Limit: 20})
}

func (m *Model) Refresh(ctx context.Context) error {
	request := ReadRequest{Kind: ReadPortfolio, Product: m.snapshot.AmbientProduct, Query: m.snapshot.Query, Limit: 20}
	switch m.snapshot.Screen {
	case ScreenProduct:
		request.Kind = ReadProduct
	case ScreenQuery:
		request.Kind = ReadQuery
	}
	return m.read(ctx, request)
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
	snapshot.Rows = append([]ProductRow(nil), m.snapshot.Rows...)
	return snapshot
}

func (m *Model) Size() (width, height int) { return m.width, m.height }

func (m *Model) read(ctx context.Context, request ReadRequest) error {
	snapshot, err := m.port.Read(ctx, request)
	if err != nil {
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
