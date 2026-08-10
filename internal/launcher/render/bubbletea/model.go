// Package bubbletea is the only launcher package that imports Charm types.
package bubbletea

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sharper-flow/concord/internal/launcher"
)

type Profile struct{ Color bool }

type keyMap struct {
	Refresh key.Binding
	Clear   key.Binding
	Submit  key.Binding
}

func (k keyMap) ShortHelp() []key.Binding  { return []key.Binding{k.Refresh, k.Clear, k.Submit} }
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{{k.Refresh, k.Clear, k.Submit}} }

type Model struct {
	core       *launcher.Model
	ctx        context.Context
	input      textinput.Model
	table      table.Model
	viewport   viewport.Model
	help       help.Model
	keys       keyMap
	profile    Profile
	projection launcher.Projection
	queryMode  bool
	width      int
	height     int
}

func New(core *launcher.Model, ctx context.Context, profile Profile) *Model {
	input := textinput.New()
	input.Prompt = "QUERY: "
	input.SetStyles(textinput.Styles{})
	helpView := help.New()
	if !profile.Color {
		helpView.Styles = help.Styles{}
	}
	model := &Model{
		core: core, ctx: ctx, input: input, table: table.New(),
		viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(16)), help: helpView,
		profile: profile, width: 80, height: 24,
		keys: keyMap{
			Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Clear:   key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "clear")),
			Submit:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit")),
		},
	}
	model.Sync()
	return model
}

// Sync projects the latest in-memory launcher snapshot after an explicit read
// or resize event. Render never reads the core or its read port.
func (m *Model) Sync() { m.projection = launcher.Project(m.core.Snapshot(), m.width) }

func (m *Model) OpenQuery() tea.Cmd {
	m.queryMode = true
	return m.input.Focus()
}

func (m *Model) QueryValue() string { return m.input.Value() }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Resize is one explicit event. It changes layout only and never reads.
		m.width, m.height = msg.Width, msg.Height
		m.core.Resize(msg.Width, msg.Height)
		m.Sync()
		m.input.SetWidth(max(1, msg.Width-8))
		m.viewport.SetWidth(max(1, msg.Width))
		m.viewport.SetHeight(max(1, msg.Height-6))
		return m, nil
	case tea.PasteMsg:
		if m.queryMode {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	case tea.KeyPressMsg:
		if m.queryMode {
			if key.Matches(msg, m.keys.Submit) {
				m.queryMode = false
				m.input.Blur()
				_ = m.core.SubmitQuery(m.ctx, m.input.Value())
				m.Sync()
				return m, nil
			}
			if key.Matches(msg, m.keys.Clear) {
				m.input.Reset()
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if key.Matches(msg, m.keys.Refresh) {
			_ = m.core.Refresh(m.ctx)
			m.Sync()
		}
	}
	return m, nil
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) View() tea.View { return tea.NewView(m.Render()) }

// Render uses only the last explicit projection. It never reads the core or ReadPort.
func (m *Model) Render() string {
	projection := m.projection
	widths := columnWidths(m.width)
	columns := make([]table.Column, len(projection.Columns))
	for i, title := range projection.Columns {
		columns[i] = table.Column{Title: title, Width: widths[i]}
	}
	rows := make([]table.Row, 0, len(projection.Rows))
	for _, row := range projection.Rows {
		rows = append(rows, wrappedRows(row, widths)...)
	}
	styles := table.Styles{}
	if m.profile.Color {
		accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
		styles = table.Styles{Header: accent, Cell: lipgloss.NewStyle(), Selected: accent}
	}
	m.table.SetColumns(columns)
	m.table.SetRows(rows)
	m.table.SetWidth(m.width)
	m.table.SetHeight(max(1, m.height-6))
	m.table.SetStyles(styles)
	m.viewport.SetContent(m.table.View())
	m.viewport.SoftWrap = true
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(max(1, m.height-6))
	m.help.SetWidth(m.width)
	query := "QUERY: press enter to submit; typing is local"
	if m.queryMode {
		query = m.input.View()
	}
	lines := append([]string{}, projection.Header...)
	lines = wrapHeaders(lines, m.width)
	lines = append(lines, m.viewport.View(), query, m.help.View(m.keys))
	return strings.Join(lines, "\n")
}

func columnWidths(width int) []int {
	if width < 80 {
		width = 80
	}
	return []int{18, 14, 18, 12, width - 18 - 14 - 18 - 12}
}

func wrappedRows(row []string, widths []int) []table.Row {
	wrapped := make([][]string, len(row))
	lineCount := 1
	for i := range row {
		wrapped[i] = splitDisplay(row[i], widths[i])
		if len(wrapped[i]) == 0 {
			wrapped[i] = []string{""}
		}
		if len(wrapped[i]) > lineCount {
			lineCount = len(wrapped[i])
		}
	}
	rows := make([]table.Row, lineCount)
	for line := 0; line < lineCount; line++ {
		rows[line] = make(table.Row, len(row))
		for field := range row {
			if line < len(wrapped[field]) {
				rows[line][field] = wrapped[field][line]
			}
		}
	}
	return rows
}

func wrapHeaders(headers []string, width int) []string {
	wrapped := make([]string, 0, len(headers))
	for _, header := range headers {
		parts := strings.SplitN(header, ": ", 2)
		if len(parts) == 2 {
			wrapped = append(wrapped, wrapLabeled(parts[0], parts[1], width)...)
		} else {
			wrapped = append(wrapped, splitDisplay(header, width)...)
		}
	}
	return wrapped
}

func wrapLabeled(label, value string, width int) []string {
	prefix := label + ": "
	available := width - lipgloss.Width(prefix)
	if available < 1 {
		return splitDisplay(prefix, width)[:1]
	}
	chunks := splitDisplay(value, available)
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	wrapped := make([]string, len(chunks))
	for i, chunk := range chunks {
		wrapped[i] = prefix + chunk
	}
	return wrapped
}

func splitDisplay(value string, width int) []string {
	if value == "" {
		return nil
	}
	var chunks []string
	current := ""
	for _, r := range value {
		candidate := current + string(r)
		if current != "" && lipgloss.Width(candidate) > width {
			chunks = append(chunks, current)
			current = string(r)
			continue
		}
		current = candidate
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
