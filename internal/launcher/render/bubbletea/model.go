// Package bubbletea is the only launcher package that imports Charm types.
package bubbletea

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sharper-flow/concord/internal/launcher"
)

type Profile struct{ Color bool }

type keyMap struct {
	Move    key.Binding
	Filter  key.Binding
	Refresh key.Binding
	Open    key.Binding
	Back    key.Binding
	Page    key.Binding
	Help    key.Binding
	Quit    key.Binding
	Clear   key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Move, k.Filter, k.Refresh, k.Open, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Move, k.Page, k.Filter, k.Refresh}, {k.Open, k.Back, k.Help, k.Quit, k.Clear}}
}

type Model struct {
	core       *launcher.Model
	ctx        context.Context
	input      textinput.Model
	table      table.Model
	help       help.Model
	profile    Profile
	projection launcher.Projection
	filterMode bool
	showHelp   bool
	keys       keyMap
	cursor     int
	scroll     int
	width      int
	height     int
}

func New(core *launcher.Model, ctx context.Context, profile Profile) *Model {
	input := textinput.New()
	input.Prompt = "FILTER: "
	input.SetStyles(textinput.Styles{})
	model := &Model{
		core: core, ctx: ctx, input: input, table: table.New(), help: help.New(),
		profile: profile, width: 80, height: 24,
		keys: keyMap{
			Move:    key.NewBinding(key.WithKeys("j", "k", "↑", "↓"), key.WithHelp("j/k", "move")),
			Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
			Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Open:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			Back:    key.NewBinding(key.WithKeys("esc", "h", "←"), key.WithHelp("esc", "back")),
			Page:    key.NewBinding(key.WithKeys("ctrl+d", "ctrl+u", "n", "p"), key.WithHelp("ctrl-d/u", "page")),
			Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
			Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
			Clear:   key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "clear")),
		},
	}
	if !profile.Color {
		model.help.Styles = help.Styles{}
	}
	model.help.SetWidth(model.width)
	model.Sync()
	return model
}

// Sync projects the latest in-memory launcher snapshot after an explicit read
// or UI event. Render never reads the core or its read port.
func (m *Model) Sync() {
	m.projection = launcher.Project(m.core.Snapshot(), m.width)
	m.clampCursor()
}

// OpenFilter enters S1's read-free local filter mode.
func (m *Model) OpenFilter() tea.Cmd {
	m.filterMode = true
	return m.input.Focus()
}

// OpenQuery remains a compatibility name from the renderer spike. It opens
// only the S1 local filter; no semantic query read is available on S1.
func (m *Model) OpenQuery() tea.Cmd { return m.OpenFilter() }

func (m *Model) QueryValue() string  { return m.input.Value() }
func (m *Model) FilterValue() string { return m.input.Value() }
func (m *Model) Cursor() int         { return m.cursor }
func (m *Model) HelpVisible() bool   { return m.showHelp }

// UpdateKey feeds a deterministic key event to tests and internal callers.
func (m *Model) UpdateKey(value string) tea.Cmd {
	key := tea.Key{Text: value, Code: firstRune(value)}
	switch value {
	case "enter":
		key = tea.Key{Code: tea.KeyEnter}
	case "esc":
		key = tea.Key{Code: tea.KeyEscape}
	case "up":
		key = tea.Key{Code: tea.KeyUp}
	case "down":
		key = tea.Key{Code: tea.KeyDown}
	case "left":
		key = tea.Key{Code: tea.KeyLeft}
	case "ctrl+l":
		key = tea.Key{Code: 'l', Mod: tea.ModCtrl, Text: "l"}
	case "ctrl+d":
		key = tea.Key{Code: 'd', Mod: tea.ModCtrl, Text: "d"}
	case "ctrl+u":
		key = tea.Key{Code: 'u', Mod: tea.ModCtrl, Text: "u"}
	case "ctrl+c":
		key = tea.Key{Code: 'c', Mod: tea.ModCtrl, Text: "c"}
	}
	_, cmd := m.Update(tea.KeyPressMsg(key))
	return cmd
}

func firstRune(value string) rune {
	for _, r := range value {
		return r
	}
	return 0
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.core.Resize(msg.Width, msg.Height)
		m.input.SetWidth(max(1, msg.Width-8))
		m.help.SetWidth(max(1, msg.Width))
		m.Sync()
		return m, nil
	case tea.PasteMsg:
		if m.filterMode {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.clampCursor()
			return m, cmd
		}
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	keyValue := msg.Key()
	if m.filterMode {
		switch {
		case keyValue.Mod&tea.ModCtrl != 0 && keyValue.Code == 'l':
			m.input.Reset()
			m.clampCursor()
			return m, nil
		case key == "enter", key == "esc":
			m.filterMode = false
			m.input.Blur()
			m.clampCursor()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.clampCursor()
		return m, cmd
	}

	switch key {
	case "/":
		return m, m.OpenFilter()
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "r":
		if err := m.core.Refresh(m.ctx); err != nil {
			m.setError(err)
		}
		m.Sync()
		return m, nil
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g":
		m.cursor, m.scroll = 0, 0
	case "G":
		m.cursor = max(0, len(m.filteredRows())-1)
		m.adjustScroll()
	case "ctrl+d":
		m.move(m.pageSize() / 2)
	case "ctrl+u":
		m.move(-m.pageSize() / 2)
	case "n":
		m.move(m.pageSize())
	case "p":
		m.move(-m.pageSize())
	case "enter":
		if m.core.Snapshot().Screen == launcher.ScreenPortfolio {
			rows := m.filteredRows()
			if len(rows) > 0 {
				if err := m.core.SelectProduct(m.ctx, rows[m.cursor].ID); err != nil {
					m.setError(err)
				}
				m.Sync()
			}
		}
	case "esc", "h", "left":
		if err := m.core.Back(); err != nil {
			m.setError(err)
		}
		m.Sync()
	case "q", "ctrl+c":
		if m.core.Snapshot().Screen == launcher.ScreenProduct {
			_ = m.core.Back()
			m.Sync()
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) setError(err error) {
	snapshot := m.core.Snapshot()
	snapshot.StatusMessage = err.Error()
	snapshot.Coverage = "unreachable"
	// The core owns snapshots; a failed explicit read is represented by the
	// read port's typed snapshot on the next successful call. Keep the render
	// stable here without adding a second authority or retry command.
	_ = snapshot
}

func (m *Model) filteredRows() []launcher.ProductRow {
	rows := m.core.Snapshot().Rows
	needle := strings.ToLower(m.input.Value())
	if needle == "" {
		return rows
	}
	filtered := make([]launcher.ProductRow, 0, len(rows))
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Name), needle) || strings.Contains(strings.ToLower(row.Stage), needle) || strings.Contains(strings.ToLower(row.Focus), needle) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func (m *Model) move(delta int) {
	rows := m.filteredRows()
	if len(rows) == 0 {
		m.cursor, m.scroll = 0, 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(rows) {
		m.cursor = len(rows) - 1
	}
	m.adjustScroll()
}

func (m *Model) pageSize() int {
	if m.height < 8 {
		return 1
	}
	return max(1, m.height-8)
}

func (m *Model) adjustScroll() {
	page := m.pageSize()
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+page {
		m.scroll = m.cursor - page + 1
	}
}

func (m *Model) clampCursor() {
	rows := m.filteredRows()
	if len(rows) == 0 {
		m.cursor, m.scroll = 0, 0
		return
	}
	if m.cursor >= len(rows) {
		m.cursor = len(rows) - 1
	}
	m.adjustScroll()
}

func (m *Model) Init() tea.Cmd  { return nil }
func (m *Model) View() tea.View { return tea.NewView(m.Render()) }

// Render uses only the last explicit projection and local interaction state.
// It never reads the core or ReadPort.
func (m *Model) Render() string {
	projection := m.projection
	rows := m.filteredRows()
	if m.core.Snapshot().Screen == launcher.ScreenProduct {
		return m.renderS2(projection.Header)
	}
	widths := columnWidths(m.width)
	columns := make([]table.Column, len(projection.Columns))
	for i, title := range projection.Columns {
		columns[i] = table.Column{Title: title, Width: widths[i]}
	}
	projectedRows := make([]table.Row, 0, len(rows))
	start := m.scroll
	if start > len(rows) {
		start = len(rows)
	}
	end := min(len(rows), start+m.pageSize())
	for _, row := range rows[start:end] {
		value := []string{row.Name + row.NameSuffix, row.Stage, relianceText(row), actionText(row), row.Focus}
		projectedRows = append(projectedRows, wrappedRows(value, widths)...)
	}
	m.table.SetColumns(columns)
	m.table.SetRows(projectedRows)
	m.table.SetWidth(m.width)
	m.table.SetHeight(max(1, m.height-6))
	styles := table.Styles{}
	if m.profile.Color {
		accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
		styles = table.Styles{Header: accent, Cell: lipgloss.NewStyle(), Selected: accent}
	}
	m.table.SetStyles(styles)
	lines := append([]string{}, projection.Header...)
	lines = wrapHeaders(lines, m.width)
	if m.filterMode {
		lines = append(lines, m.input.View())
	} else if m.input.Value() != "" {
		hidden := len(m.core.Snapshot().Rows) - len(rows)
		lines = append(lines, "FILTERED: "+m.input.Value()+" (hidden: "+fmtInt(hidden)+")")
	}
	snapshot := m.core.Snapshot()
	if len(snapshot.Rows) == 0 && snapshot.Coverage == "first_run" {
		lines = append(lines, "FIRST RUN: no database; initialize through the operator setup")
	} else if len(snapshot.Rows) == 0 && snapshot.Coverage == "authoritative" {
		lines = append(lines, "PORTFOLIO: authoritative-empty")
	} else if snapshot.StatusMessage != "" {
		lines = append(lines, "STATUS: "+snapshot.StatusMessage)
	}
	lines = append(lines, m.table.View())
	if m.showHelp {
		lines = append(lines, "HELP: "+m.help.View(m.keys))
	} else {
		lines = append(lines, m.help.View(m.keys))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderS2(headers []string) string {
	lines := append([]string{}, headers...)
	lines = append(lines, "S2 PRODUCT: not_implemented", "S2: Product coordination remains outside S1", "KEYS: esc/h/left back  q back")
	if m.showHelp {
		lines = append(lines, "HELP: esc/h/left back  q back  ? hide")
	}
	return strings.Join(wrapHeaders(lines, m.width), "\n")
}

func actionText(row launcher.ProductRow) string {
	if row.CountsState == "unavailable" {
		text := "unavailable: " + row.UnavailableReason
		if len(row.UnavailableOmissions) > 0 {
			text += " (omissions: " + strings.Join(row.UnavailableOmissions, ",") + ")"
		}
		return text
	}
	if row.InProgress == 0 && row.Blocked == 0 && row.Ready == 0 && row.ActiveProblems == 0 && row.ApprovalRequired == 0 && row.Actions != 0 {
		return fmtInt(row.Actions)
	}
	return "ip:" + fmtInt(row.InProgress) + " b:" + fmtInt(row.Blocked) + " r:" + fmtInt(row.Ready) + " p:" + fmtInt(row.ActiveProblems) + " a:" + fmtInt(row.ApprovalRequired)
}

func relianceText(row launcher.ProductRow) string {
	text := row.Reliance
	if row.RelianceStale || row.BlocksExecution {
		text = "stale"
	}
	if text == "" || text == "clear" || text == "ready" {
		return text
	}
	if row.RelianceReason != "" {
		text += ":" + row.RelianceReason
	}
	return "! " + text
}

func fmtInt(value int) string {
	if value == 0 {
		return "0"
	}
	if value < 0 {
		return "-" + fmtInt(-value)
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
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
		return []string{label}
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
