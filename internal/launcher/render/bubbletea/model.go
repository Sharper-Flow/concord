// Package bubbletea is the only launcher package that imports Charm types.
package bubbletea

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

type navigationPosition struct {
	cursor int
	scroll int
}

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
	Search  key.Binding
	Section key.Binding
	Launch  key.Binding
	Pin     key.Binding
	Unpin   key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Move, k.Filter, k.Search, k.Section, k.Refresh, k.Open, k.Launch, k.Pin, k.Unpin, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Move, k.Page, k.Filter, k.Search, k.Refresh}, {k.Open, k.Back, k.Section, k.Launch, k.Pin, k.Unpin, k.Help, k.Quit, k.Clear}}
}

type Model struct {
	core                     *launcher.Model
	ctx                      context.Context
	input                    textinput.Model
	table                    table.Model
	help                     help.Model
	profile                  Profile
	projection               launcher.Projection
	filterMode               bool
	queryMode                bool
	queryDisplayed           bool
	filterValue, queryValue  string
	queryBase                launcher.Snapshot
	showHelp                 bool
	keys                     keyMap
	cursor                   int
	scroll                   int
	width                    int
	height                   int
	launch                   func(launcher.SessionHandoff) tea.Cmd
	navigation               []navigationPosition
	queryCursor, queryScroll int
}

func New(core *launcher.Model, ctx context.Context, profile Profile) *Model {
	input := textinput.New()
	input.Prompt = "FILTER: "
	input.SetStyles(textinput.Styles{})
	model := &Model{
		core: core, ctx: ctx, input: input, table: table.New(), help: help.New(),
		profile: profile, width: 80, height: 24,
		keys: keyMap{
			Move:    key.NewBinding(key.WithKeys("↑", "↓"), key.WithHelp("arrows", "move")),
			Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
			Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Open:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			Back:    key.NewBinding(key.WithKeys("esc", "h", "←"), key.WithHelp("esc", "back")),
			Page:    key.NewBinding(key.WithKeys("ctrl+d", "ctrl+u", "n", "p"), key.WithHelp("ctrl-d/u", "page")),
			Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
			Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
			Clear:   key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "clear")),
			Search:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "search")),
			Section: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "panel/section")),
			Launch:  key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "launch")),
			Pin:     key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl-p", "pin")),
			Unpin:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl-u", "unpin")),
		},
	}
	model.launch = defaultSessionLauncher
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
	if m.core.Snapshot().Screen == launcher.ScreenWork {
		return nil
	}
	m.filterMode = true
	m.queryMode = false
	m.input.Prompt = "FILTER: "
	m.input.SetValue(m.filterValue)
	return m.input.Focus()
}

// openQuery enters the S2/S3 semantic query input. S1 has no semantic-query
// binding, so the portfolio screen opens no query mode.
func (m *Model) openQuery() tea.Cmd {
	screen := m.core.Snapshot().Screen
	if screen != launcher.ScreenProduct && screen != launcher.ScreenWork {
		return nil
	}
	m.queryBase = m.core.Snapshot()
	m.queryCursor, m.queryScroll = m.cursor, m.scroll
	m.queryMode = true
	m.filterMode = false
	m.input.Prompt = "QUERY: "
	m.queryValue = ""
	m.input.SetValue("")
	return m.input.Focus()
}

func (m *Model) SetSessionLauncher(fn func(launcher.SessionHandoff) tea.Cmd) {
	if fn != nil {
		m.launch = fn
	}
}

func (m *Model) QueryValue() string {
	if m.queryMode || m.filterMode {
		return m.input.Value()
	}
	return m.queryValue
}
func (m *Model) FilterValue() string {
	if m.filterMode {
		return m.input.Value()
	}
	return m.filterValue
}
func (m *Model) Cursor() int       { return m.cursor }
func (m *Model) HelpVisible() bool { return m.showHelp }

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
	case "ctrl+p":
		key = tea.Key{Code: 'p', Mod: tea.ModCtrl, Text: "p"}
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
	case sessionLaunchError:
		m.setError(msg.err)
		m.Sync()
		return m, nil
	case tea.PasteMsg:
		if m.filterMode || m.queryMode {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.storeInputValue()
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
	if m.filterMode || m.queryMode {
		switch {
		case keyValue.Mod&tea.ModCtrl != 0 && keyValue.Code == 'l':
			m.input.Reset()
			m.storeInputValue()
			m.clampCursor()
			return m, nil
		case key == "enter":
			wasQuery := m.queryMode
			value := m.input.Value()
			m.filterMode, m.queryMode = false, false
			m.input.Blur()
			if wasQuery {
				m.queryValue = value
				if err := m.core.SubmitQuery(m.ctx, value); err != nil {
					m.setError(err)
				} else {
					m.queryDisplayed = true
				}
			} else {
				m.filterValue = value
			}
			m.clampCursor()
			m.Sync()
			return m, nil
		case key == "esc":
			wasQuery := m.queryMode
			m.filterMode, m.queryMode = false, false
			m.input.Blur()
			if wasQuery {
				m.core.RestoreSnapshot(m.queryBase)
				m.cursor, m.scroll = m.queryCursor, m.queryScroll
				m.queryDisplayed = m.queryBase.QueryResult
				m.queryValue = ""
				m.Sync()
			}
			m.clampCursor()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.storeInputValue()
		m.clampCursor()
		return m, cmd
	}

	switch key {
	case "/":
		return m, m.OpenFilter()
	case "s":
		return m, m.openQuery()
	case "tab":
		if m.core.Snapshot().Screen == launcher.ScreenProduct {
			_ = m.core.CyclePanelFocus()
			m.Sync()
		} else if m.core.Snapshot().Screen == launcher.ScreenWork {
			next := launcher.SectionDomains
			switch m.core.Section() {
			case launcher.SectionDomains:
				next = launcher.SectionRelations
			case launcher.SectionRelations:
				next = launcher.SectionRanked
			case launcher.SectionRanked:
				next = launcher.SectionKnowledge
			}
			if next == launcher.SectionKnowledge {
				if err := m.core.EnsureKnowledge(m.ctx); err != nil {
					m.setError(err)
				}
			}
			_ = m.core.SetSection(next)
			m.Sync()
		}
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "r":
		if err := m.core.Refresh(m.ctx); err != nil {
			m.setError(err)
		}
		m.Sync()
		return m, nil
	case "ctrl+p", "ctrl+u":
		m.togglePin(key == "ctrl+p")
		m.Sync()
		return m, nil
	case "l":
		if m.core.Snapshot().Screen == launcher.ScreenProduct || m.core.Snapshot().Screen == launcher.ScreenWork {
			return m, m.launch(m.core.Handoff())
		}
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g":
		m.cursor, m.scroll = 0, 0
	case "G":
		m.cursor = max(0, m.rowCount()-1)
		m.adjustScroll()
	case "ctrl+d":
		m.move(m.pageSize() / 2)
	case "n":
		m.move(m.pageSize())
	case "p":
		m.move(-m.pageSize())
	case "enter":
		if m.core.Snapshot().Screen == launcher.ScreenPortfolio {
			candidates := m.filteredCandidates()
			if len(candidates) > 0 {
				candidate := candidates[m.cursor]
				if candidate.Kind == launcher.CandidateProduct {
					previousScreen := m.core.Snapshot().Screen
					if err := m.core.SelectProduct(m.ctx, candidate.ProductID); err != nil {
						m.setError(err)
					} else if m.core.Snapshot().Screen != previousScreen {
						m.navigation = append(m.navigation, navigationPosition{cursor: m.cursor, scroll: m.scroll})
					}
					m.Sync()
					return m, nil
				}
				if candidate.Kind == launcher.CandidateWork {
					if !candidate.Available {
						m.setError(fmt.Errorf("work item %s has no claimed worktree", candidate.ID))
						m.Sync()
						return m, nil
					}
					m.core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: candidate.ProductID, SelectedWorkID: candidate.WorkID, Session: launcher.SessionHandoff{ProductID: candidate.ProductID, WorkID: candidate.WorkID}, Coverage: "authoritative", Section: launcher.SectionRanked})
					return m, m.launch(m.core.Handoff())
				}
			}
			rows := m.filteredRows()
			if len(rows) > 0 && m.cursor < len(rows) {
				previousScreen := m.core.Snapshot().Screen
				if err := m.core.SelectProduct(m.ctx, rows[m.cursor].ID); err != nil {
					m.setError(err)
				} else if m.core.Snapshot().Screen != previousScreen {
					m.navigation = append(m.navigation, navigationPosition{cursor: m.cursor, scroll: m.scroll})
				}
				m.filterValue = ""
				m.input.Reset()
				m.Sync()
			}
		} else if m.core.Snapshot().Screen == launcher.ScreenProduct && m.core.Section() == launcher.SectionRanked {
			rows := m.filteredRanked()
			if len(rows) > 0 && m.cursor < len(rows) {
				previousScreen := m.core.Snapshot().Screen
				selectionErr := m.core.SelectWork(m.ctx, rows[m.cursor].ID)
				if selectionErr != nil {
					m.setError(selectionErr)
				} else if m.core.Snapshot().Screen != previousScreen {
					m.navigation = append(m.navigation, navigationPosition{cursor: m.cursor, scroll: m.scroll})
				}
				m.filterValue = ""
				m.input.Reset()
				m.Sync()
				if selectionErr == nil {
					return m, m.launch(m.core.Handoff())
				}
			}
		}
	case "esc", "h", "left":
		if m.queryDisplayed {
			m.core.RestoreSnapshot(m.queryBase)
			m.cursor, m.scroll = m.queryCursor, m.queryScroll
			m.queryDisplayed = m.queryBase.QueryResult
			m.queryValue = ""
			m.input.Reset()
			m.Sync()
			return m, nil
		}
		m.back()
	case "q", "ctrl+c":
		if m.core.Snapshot().Screen == launcher.ScreenProduct || m.core.Snapshot().Screen == launcher.ScreenWork {
			m.back()
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) back() {
	before := m.core.Snapshot().Screen
	if err := m.core.Back(); err != nil {
		m.setError(err)
	}
	after := m.core.Snapshot().Screen
	m.Sync()
	if before == after || len(m.navigation) == 0 {
		return
	}
	last := len(m.navigation) - 1
	position := m.navigation[last]
	m.navigation = m.navigation[:last]
	m.cursor, m.scroll = position.cursor, position.scroll
	m.clampCursor()
}

func (m *Model) storeInputValue() {
	if m.queryMode {
		m.queryValue = m.input.Value()
	} else if m.filterMode {
		m.filterValue = m.input.Value()
	}
}

func (m *Model) setError(err error) {
	snapshot := m.core.Snapshot()
	snapshot.StatusMessage = err.Error()
	snapshot.Coverage = "unreachable"
	// This is process-launch status, not workflow authority. Preserve the
	// current snapshot and expose the typed failure without retrying.
	m.core.RestoreSnapshot(snapshot)
}

func (m *Model) filteredRows() []launcher.ProductRow {
	rows := m.core.Snapshot().Rows
	needle := strings.ToLower(m.filterValue)
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

func (m *Model) filteredRanked() []launcher.RankedWork {
	rows := m.core.Snapshot().Ranked
	needle := strings.ToLower(m.filterValue)
	if needle == "" {
		return rows
	}
	out := make([]launcher.RankedWork, 0, len(rows))
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.ID+" "+row.Title+" "+row.Kind+" "+row.Lifecycle), needle) {
			out = append(out, row)
		}
	}
	return out
}

func (m *Model) move(delta int) {
	count := m.rowCount()
	if count == 0 {
		m.cursor, m.scroll = 0, 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= count {
		m.cursor = count - 1
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
	count := m.rowCount()
	if count == 0 {
		m.cursor, m.scroll = 0, 0
		return
	}
	if m.cursor >= count {
		m.cursor = count - 1
	}
	m.adjustScroll()
}

func (m *Model) rowCount() int {
	s := m.core.Snapshot()
	if s.Screen == launcher.ScreenProduct {
		if m.core.PanelFocus() == launcher.S2PanelDomain {
			return len(s.Domains.Domains)
		}
		return len(m.filteredRanked())
	}
	if s.Screen == launcher.ScreenWork {
		return len(s.Detail.History)
	}
	if len(s.Candidates) > 0 {
		return len(m.filteredCandidates())
	}
	return len(m.filteredRows())
}

func (m *Model) filteredCandidates() []launcher.Candidate {
	return launcher.FilterCandidates(m.core.Snapshot().Candidates, m.filterValue)
}

func (m *Model) togglePin(pin bool) {
	snapshot := m.core.Snapshot()
	if len(snapshot.Candidates) == 0 || m.cursor < 0 || m.cursor >= len(m.filteredCandidates()) {
		return
	}
	selected := m.filteredCandidates()[m.cursor]
	for i := range snapshot.Candidates {
		if snapshot.Candidates[i].ID == selected.ID && snapshot.Candidates[i].Kind == selected.Kind && snapshot.Candidates[i].Path == selected.Path {
			snapshot.Candidates[i].Pinned = pin
			break
		}
	}
	snapshot.Candidates = launcher.OrderCandidates(snapshot.Candidates)
	m.core.RestoreSnapshot(snapshot)
	m.clampCursor()
}

func (m *Model) Init() tea.Cmd  { return nil }
func (m *Model) View() tea.View { return tea.NewView(m.Render()) }

// Render uses only the last explicit projection and local interaction state.
// It never reads the core or ReadPort.
func (m *Model) Render() string {
	projection := m.projection
	rows := m.filteredRows()
	snapshot := m.core.Snapshot()
	screen := snapshot.Screen
	m.keys.Search.SetEnabled(screen != launcher.ScreenPortfolio)
	m.keys.Filter.SetEnabled(screen != launcher.ScreenWork)
	m.keys.Section.SetEnabled(screen != launcher.ScreenPortfolio)
	m.keys.Launch.SetEnabled(screen != launcher.ScreenPortfolio)
	if screen == launcher.ScreenProduct {
		return m.renderS2(projection.Header)
	}
	if screen == launcher.ScreenWork {
		return m.renderS3(projection.Header)
	}
	if len(snapshot.Candidates) > 0 {
		return m.renderCandidates(snapshot)
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
	} else if m.filterValue != "" {
		hidden := len(m.core.Snapshot().Rows) - len(rows)
		lines = append(lines, "FILTERED: "+m.filterValue+" (hidden: "+fmtInt(hidden)+")")
	}
	if len(snapshot.Rows) == 0 && snapshot.Coverage == "first_run" {
		lines = append(lines, "FIRST RUN: no database; initialize through the operator setup")
	} else if len(snapshot.Rows) == 0 && snapshot.Coverage == "authoritative" {
		lines = append(lines, "PORTFOLIO: authoritative-empty")
	} else if snapshot.StatusMessage != "" {
		lines = append(lines, "STATUS: "+snapshot.StatusMessage)
	}
	lines = append(lines, m.table.View())
	if m.showHelp {
		lines = append(lines, helpLines("HELP: "+m.help.View(m.keys), m.width)...)
	} else {
		lines = append(lines, helpLines(m.help.View(m.keys), m.width)...)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderS2(headers []string) string {
	lines := append([]string{}, headers...)
	s := m.core.Snapshot()
	lines = append(lines, probeLines(s.Probes)...)
	stack := s.S2AnswerStack()
	lines = append(lines, "S2 PRODUCT COORDINATION")
	if s.StatusMessage != "" {
		lines = append(lines, "STATUS: "+s.StatusMessage)
	}
	if m.filterMode {
		lines = append(lines, m.input.View())
	} else if m.filterValue != "" {
		lines = append(lines, "FILTERED: "+m.filterValue+" (hidden: "+fmtInt(len(s.Ranked)-len(m.filteredRanked()))+")")
	}
	for _, panel := range stack.Panels {
		focused := s.PanelFocus == panel || (s.PanelFocus == "" && panel == launcher.S2PanelDomain)
		lines = append(lines, s2PanelLines(panel, focused, stack, s, m.filteredRanked())...)
	}
	if s.QueryResult {
		lines = append(lines, "KNOWLEDGE WATERMARK: "+s.Knowledge.Watermark+" STATE: "+s.Knowledge.State)
	}
	if s.QueryResult && len(s.Knowledge.Items) > 0 {
		lines = append(lines, "KNOWLEDGE MATCHES:")
		for _, item := range s.Knowledge.Items {
			lines = append(lines, "  "+item.Kind+" "+item.ID+" "+item.Title)
		}
	}
	if s.QueryResult {
		lines = append(lines, "QUERY RESULT: "+s.QuerySubmitted+" (Esc restores prior view)")
	}
	if m.showHelp {
		lines = append(lines, helpLines("HELP: "+m.help.View(m.keys), m.width)...)
	} else {
		lines = append(lines, helpLines(m.help.View(m.keys), m.width)...)
	}
	return strings.Join(wrapHeaders(lines, m.width), "\n")
}

func s2PanelLines(panel launcher.S2Panel, expanded bool, stack launcher.S2AnswerStack, snapshot launcher.Snapshot, ranked []launcher.RankedWork) []string {
	if !expanded {
		switch panel {
		case launcher.S2PanelDomain:
			return domainSummaryLines(stack.Domain.Domain)
		case launcher.S2PanelBlocked:
			return blockedSummaryLines(stack.Blocked.Work, snapshot)
		case launcher.S2PanelNext:
			return nextSummaryLines(stack.Next.Work, snapshot)
		}
	}
	switch panel {
	case launcher.S2PanelDomain:
		lines := []string{"DOMAIN:"}
		lines = append(lines, domainLines(snapshot.Domains)...)
		lines = append(lines, knowledgeLines(snapshot.Knowledge)...)
		lines = append(lines, relationLines(snapshot.Relations)...)
		return lines
	case launcher.S2PanelBlocked, launcher.S2PanelNext:
		return append([]string{"BLOCKED/BLOCKERS:"}, rankedLines(ranked, snapshot)...)
	default:
		return nil
	}
}

func domainSummaryLines(summary launcher.S2DomainSummary) []string {
	if summary.UnavailableReason != "" {
		return []string{"DOMAIN: unavailable: " + summary.UnavailableReason}
	}
	if !summary.Evaluated {
		return []string{"DOMAIN: unavailable: not_read"}
	}
	if len(summary.UnresolvedOverlaps) == 0 {
		return []string{"DOMAIN: no unresolved overlaps"}
	}
	parts := make([]string, 0, len(summary.UnresolvedOverlaps))
	for _, pair := range summary.UnresolvedOverlaps {
		state := pair.State
		if state == "" {
			state = "absent"
		}
		parts = append(parts, pair.From+" & "+pair.To+" domains="+strings.Join(pair.SharedDomains, ",")+" resolution="+state)
	}
	return []string{"DOMAIN: unresolved overlap: " + strings.Join(parts, "; ")}
}

func blockedSummaryLines(item *launcher.RankedWork, snapshot launcher.Snapshot) []string {
	if item == nil {
		return []string{"BLOCKED: " + drillDownEmptyState(snapshot)}
	}
	state := rankedMarker(item)
	blockers := make([]string, 0, len(item.Blockers))
	for _, blocker := range item.Blockers {
		blockers = append(blockers, blocker.ID+"["+blocker.Authority+"]")
	}
	return []string{"BLOCKED: " + item.ID + " " + item.Title + " marker=" + state + " blockers=" + strings.Join(blockers, ",")}
}

func nextSummaryLines(item *launcher.RankedWork, snapshot launcher.Snapshot) []string {
	if item == nil {
		return []string{"NEXT: " + drillDownEmptyState(snapshot)}
	}
	return []string{"NEXT: " + rankedMarker(item) + " " + item.ID + " " + item.Title}
}

// drillDownEmptyState types the empty drill-down answer so a degraded source
// never renders as an authoritative-empty list.
func drillDownEmptyState(snapshot launcher.Snapshot) string {
	if snapshot.Coverage != "" && snapshot.Coverage != "authoritative" {
		reason := strings.TrimPrefix(snapshot.StatusMessage, "unavailable: ")
		if reason == "" {
			reason = snapshot.Coverage
		}
		return "unavailable: " + reason
	}
	return "authoritative-empty"
}

func rankedMarker(item *launcher.RankedWork) string {
	switch item.Readiness() {
	case "terminal":
		return "-TERMINAL"
	case "blocked":
		return "!BLOCKED"
	case "ready":
		return "+READY"
	default:
		return "~ACTIVE"
	}
}

func relationLines(relations launcher.RelationTree) []string {
	lines := []string{}
	if relations.Unavailable != "" {
		lines = append(lines, "RELATIONS: unavailable: "+relations.Unavailable)
	}
	if relations.Invariant != "" {
		lines = append(lines, "INVARIANT: "+relations.Invariant)
	}
	if len(relations.Roots) > 0 {
		lines = append(lines, "ROOTS: "+strings.Join(relations.Roots, ", "))
	}
	if len(relations.Clusters) == 0 {
		lines = append(lines, "RELATIONS: authoritative-empty")
	}
	for i, cluster := range relations.Clusters {
		lines = append(lines, "CLUSTER "+fmtInt(i+1)+": "+strings.Join(cluster, " -> "))
	}
	for _, edge := range relations.Edges {
		lines = append(lines, "EDGE "+edge.Kind+": "+edge.Source+" -> "+edge.Target)
	}
	return lines
}

func rankedLines(ranked []launcher.RankedWork, snapshot launcher.Snapshot) []string {
	if len(ranked) == 0 {
		return []string{"WORK: " + drillDownEmptyState(snapshot)}
	}
	lines := make([]string, 0, len(ranked))
	for i, item := range ranked {
		urgency := item.Urgency
		if urgency == "" {
			urgency = "standard"
		}
		terminal := ""
		if item.TerminalAt != "" {
			terminal = " terminal=" + item.TerminalAt
		}
		kind := item.Kind
		if kind == "" {
			kind = "-"
		}
		lines = append(lines, fmtInt(i+1)+" "+rankedMarker(&item)+" "+item.ID+" "+item.Title+" kind="+kind+" priority="+fmtInt64(item.Priority)+" urgency="+urgency+" lifecycle="+item.Lifecycle+terminal+" projects="+fmtInt(item.ProjectCount))
		for _, blocker := range item.Blockers {
			external := ""
			if blocker.External {
				external = " external"
			}
			lines = append(lines, "  BLOCKER "+blocker.ID+" "+blocker.Title+" authority="+blocker.Authority+" age="+blocker.Age+external)
		}
	}
	return lines
}

func (m *Model) renderS3(headers []string) string {
	lines := append([]string{}, headers...)
	s := m.core.Snapshot()
	lines = append(lines, probeLines(s.Probes)...)
	if s.QueryResult {
		lines = append(lines, "S3 WORK SEARCH", "QUERY RESULT: "+s.QuerySubmitted+" (Esc restores prior view)")
		for _, item := range s.Ranked {
			lines = append(lines, "WORK MATCH: "+item.ID+" "+item.Title+" lifecycle="+item.Lifecycle)
		}
		lines = append(lines, "KNOWLEDGE WATERMARK: "+s.Knowledge.Watermark+" STATE: "+s.Knowledge.State)
		lines = append(lines, knowledgeLines(s.Knowledge)...)
		if m.showHelp {
			lines = append(lines, helpLines("HELP: "+m.help.View(m.keys), m.width)...)
		} else {
			lines = append(lines, helpLines(m.help.View(m.keys), m.width)...)
		}
		return strings.Join(wrapHeaders(lines, m.width), "\n")
	}
	d := s.Detail
	urgency := d.Item.Urgency
	if urgency == "" {
		urgency = "standard"
	}
	lines = append(lines, "S3 WORK DETAIL", "WORK: "+d.Item.ID+" "+d.Item.Title, "LIFECYCLE: "+d.Item.Lifecycle+" PRIORITY: "+fmtInt64(d.Item.Priority)+" URGENCY: "+urgency, "SECTION: "+string(s.Section), "PROJECTS: "+strings.Join(d.Projects, ", "), "WORKFLOW: "+d.Workflow)
	if s.StatusMessage != "" {
		lines = append(lines, "STATUS: "+s.StatusMessage)
	}
	if d.Item.Blocked {
		for _, b := range d.Item.Blockers {
			lines = append(lines, "BLOCKER: "+b.ID+" "+b.Title+" authority="+b.Authority+" age="+b.Age)
		}
	} else {
		lines = append(lines, "BLOCKED: no")
	}
	switch s.Section {
	case launcher.SectionKnowledge:
		lines = append(lines, knowledgeLines(s.Knowledge)...)
	case launcher.SectionRelations:
		for _, e := range d.Edges {
			lines = append(lines, "EDGE "+e.Kind+": "+e.Source+" -> "+e.Target)
		}
	case launcher.SectionRanked:
		for _, h := range d.History {
			lines = append(lines, "HISTORY: "+h)
		}
	}
	if s.QueryResult && len(s.Knowledge.Items) > 0 {
		lines = append(lines, "KNOWLEDGE MATCHES:")
		for _, item := range s.Knowledge.Items {
			lines = append(lines, "  "+item.Kind+" "+item.ID+" "+item.Title)
		}
	}
	if m.showHelp {
		lines = append(lines, helpLines("HELP: "+m.help.View(m.keys), m.width)...)
	} else {
		lines = append(lines, helpLines(m.help.View(m.keys), m.width)...)
	}
	return strings.Join(wrapHeaders(lines, m.width), "\n")
}

func domainLines(section launcher.DomainSection) []string {
	if !section.Read {
		return []string{"DOMAINS: unavailable: not_read"}
	}
	if section.State == "unavailable" {
		reason := section.Reason
		if reason == "" {
			reason = "unavailable"
		}
		return []string{"DOMAINS: unavailable: " + reason}
	}
	var lines []string
	if len(section.Domains) == 0 {
		lines = append(lines, "DOMAINS: authoritative-empty")
	}
	for _, domain := range section.Domains {
		marker := "DOMAIN"
		if domain.Home {
			marker = "HOME"
		}
		parent := ""
		if domain.ParentID != "" {
			parent = " parent=" + domain.ParentID
		}
		lines = append(lines, marker+" "+domain.ID+" "+domain.Name+parent+" law="+fmtInt(domain.CurrentLawCount)+" active="+fmtInt(domain.ActiveWorkCount))
	}
	for _, relation := range section.Relations {
		lines = append(lines, "RELATION "+relation.Kind+": "+relation.Source+" -> "+relation.Target+" state="+relation.State)
	}
	for _, pair := range section.Overlaps {
		resolution := pair.State
		if resolution == "" {
			resolution = "absent"
		}
		lines = append(lines, "OVERLAP "+pair.From+" & "+pair.To+" domains="+strings.Join(pair.SharedDomains, ",")+" resolution="+resolution)
	}
	if section.Truncated {
		lines = append(lines, "DOMAINS: truncated: bounded read reached")
	}
	return lines
}

func knowledgeLines(section launcher.KnowledgeSection) []string {
	if !section.Read {
		return []string{"KNOWLEDGE: unread"}
	}
	if section.State == "unavailable" {
		return []string{"KNOWLEDGE: unavailable: " + section.Reason}
	}
	if len(section.Items) == 0 {
		return []string{"KNOWLEDGE: authoritative-empty"}
	}
	out := []string{"KNOWLEDGE:"}
	for _, item := range section.Items {
		out = append(out, "  "+item.Kind+" "+item.ID+" "+item.Title+" "+item.Reference)
	}
	return out
}

func probeLines(probes []launcher.ProbeStatus) []string {
	lines := make([]string, 0, len(probes))
	for _, probe := range probes {
		state := "unavailable"
		if probe.Available {
			state = "available"
		}
		reason := ""
		if probe.Reason != "" {
			reason = ": " + probe.Reason
		}
		lines = append(lines, strings.ToUpper(probe.Name)+": "+state+reason)
	}
	return lines
}

func helpLines(value string, width int) []string { return splitDisplay(value, width) }

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

func fmtInt64(value int64) string {
	if value < 0 {
		return "-" + fmtInt64(-value)
	}
	return fmtInt(int(value))
}

var executablePath = os.Executable

type sessionLaunchError struct{ err error }

func defaultSessionLauncher(handoff launcher.SessionHandoff) tea.Cmd {
	cmd, err := sessionProcess(handoff)
	if err != nil {
		return func() tea.Msg { return sessionLaunchError{err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return sessionLaunchError{err: err}
		}
		return nil
	})
}

func sessionProcess(handoff launcher.SessionHandoff) (*exec.Cmd, error) {
	executable, err := executablePath()
	if err != nil || executable == "" {
		return nil, fmt.Errorf("cannot identify the running Concord binary")
	}
	cmd := exec.Command(executable, "session") //nolint:gosec // executable comes from os.Executable and the fixed argv does not invoke a shell.
	cmd.Env = handoffEnv(handoff)
	return cmd, nil
}

// SessionCommand returns the fixed Concord session bootstrap command for
// non-interactive forwarding. It does not construct a host command directly.
func SessionCommand(handoff launcher.SessionHandoff) (*exec.Cmd, error) {
	return sessionProcess(handoff)
}

func handoffEnv(handoff launcher.SessionHandoff) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "CONCORD_SELECTED_PRODUCT_ID=") || strings.HasPrefix(value, "CONCORD_SELECTED_WORK_ID=") || strings.HasPrefix(value, "CONCORD_SELECTED_PROMPT=") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "CONCORD_SELECTED_PRODUCT_ID="+handoff.ProductID)
	if handoff.WorkID != "" {
		env = append(env, "CONCORD_SELECTED_WORK_ID="+handoff.WorkID)
	}
	if handoff.Prompt != "" {
		env = append(env, "CONCORD_SELECTED_PROMPT="+handoff.Prompt)
	}
	return env
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

func (m *Model) renderCandidates(snapshot launcher.Snapshot) string {
	lines := []string{"CONCORD LAUNCHER", "CANDIDATES", "STATUS: " + snapshot.Coverage}
	if snapshot.StatusMessage != "" {
		lines = append(lines, "MESSAGE: "+snapshot.StatusMessage)
	}
	lines = append(lines, probeLines(snapshot.Probes)...)
	values := launcher.FilterCandidates(snapshot.Candidates, m.filterValue)
	for i, candidate := range values {
		marker := " "
		if candidate.Pinned {
			marker = "*"
		}
		available := "unavailable"
		if candidate.Available {
			available = "available"
		}
		name := candidate.Name
		if candidate.Path != "" {
			name += " " + candidate.Path
		}
		lines = append(lines, fmtInt(i+1)+" "+marker+" "+string(candidate.Kind)+" "+name+" state="+available+" live="+fmtInt(candidate.Live))
	}
	if len(values) == 0 {
		lines = append(lines, "CANDIDATES: authoritative-empty")
	}
	if m.filterMode {
		lines = append(lines, m.input.View())
	}
	if m.showHelp {
		lines = append(lines, helpLines("HELP: "+m.help.View(m.keys), m.width)...)
	} else {
		lines = append(lines, helpLines(m.help.View(m.keys), m.width)...)
	}
	return strings.Join(wrapHeaders(lines, m.width), "\n")
}
