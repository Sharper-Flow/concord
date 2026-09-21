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
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/sharper-flow/concord/internal/launcher"
)

type Profile struct{ Color bool }

// rowSeverity marks an attention state the row already carries as text. The
// projection computes it where the rows are built, so the renderer colours a
// data fact and never sniffs a rendered string for its colour.
type rowSeverity int

const (
	severityNone rowSeverity = iota
	severityAttention
)

// attentionColor is ANSI index 1 (red). The operator's terminal theme owns
// the actual hue, and the fixed index keeps the rendered escape sequence
// deterministic for tests.
var attentionColor = lipgloss.Color("1")

type navigationPosition struct {
	cursor int
	scroll int
}

type renderedPane struct {
	header       []string
	tableHeaders []string
	rows         [][]string
	severities   []rowSeverity
	tail         []string
	footer       []string
	cursor       int
	color        bool
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
	help                     help.Model
	profile                  Profile
	projection               launcher.Projection
	snapshot                 launcher.Snapshot
	filterMode               bool
	queryMode                bool
	issueMode                bool
	confirmWork              bool
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
		core: core, ctx: ctx, input: input, help: help.New(),
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
	m.snapshot = m.core.Snapshot()
	m.projection = launcher.Project(m.snapshot, m.width)
	m.clampCursor()
}

// OpenFilter enters S1's read-free local filter mode.
func (m *Model) OpenFilter() tea.Cmd {
	if m.core.Snapshot().Screen == launcher.ScreenWork || m.core.Snapshot().ProjectSelect {
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
		m.help.SetWidth(max(1, msg.Width-2))
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
	if m.confirmWork {
		return m.updateConfirmKey(msg)
	}
	if m.filterMode || m.queryMode || m.issueMode {
		return m.updateInputKey(msg)
	}
	return m.updateCommandKey(msg)
}

func (m *Model) updateConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.confirmWork = false
		return m.openSelectedWork()
	case "esc", "q":
		m.confirmWork = false
		return m, nil
	default:
		return m, nil
	}
}

func (m *Model) updateInputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	keyValue := msg.Key()
	switch {
	case keyValue.Mod&tea.ModCtrl != 0 && keyValue.Code == 'l':
		m.input.Reset()
		m.storeInputValue()
		m.clampCursor()
		return m, nil
	case key == "enter":
		if m.issueMode {
			return m.submitIssueKey()
		}
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
		if m.issueMode {
			m.issueMode = false
			m.input.Blur()
			m.input.Reset()
			return m, nil
		}
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

// submitIssueKey closes the New/Backlog prompt. An empty value or a key that
// resolves to nothing degrades to the Project select; a resolved key launches
// the session where that work item lives.
func (m *Model) submitIssueKey() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	m.issueMode = false
	m.input.Blur()
	if value == "" {
		if err := m.core.SelectProjects(m.ctx); err != nil {
			m.setError(err)
		}
		m.Sync()
		return m, nil
	}
	handoff, err := m.core.ResolveIssue(m.ctx, value)
	if err == nil && handoff.WorkID != "" {
		snapshot := m.core.Snapshot()
		snapshot.Session = handoff
		m.core.RestoreSnapshot(snapshot)
		return m, m.launch(handoff)
	}
	if projectErr := m.core.SelectProjects(m.ctx); projectErr != nil {
		m.setError(projectErr)
	}
	m.Sync()
	return m, nil
}

func (m *Model) updateCommandKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "/":
		return m, m.OpenFilter()
	case "s":
		return m, m.openQuery()
	case "tab":
		m.tabKey()
		return m, nil
	case "?":
		m.showHelp = !m.showHelp
		m.help.ShowAll = m.showHelp
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
		return m.enterKey()
	case "esc", "h", "left":
		return m.escapeKey()
	case "q", "ctrl+c":
		if m.core.Snapshot().Screen == launcher.ScreenProduct || m.core.Snapshot().Screen == launcher.ScreenWork {
			m.back()
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) tabKey() {
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
}

func (m *Model) enterKey() (tea.Model, tea.Cmd) {
	if m.core.Snapshot().ProjectSelect {
		return m.enterProjectSelect()
	}
	if m.core.Snapshot().Screen == launcher.ScreenPortfolio {
		return m.enterPortfolio()
	}
	if m.core.Snapshot().Screen == launcher.ScreenProduct && m.core.Section() == launcher.SectionRanked {
		return m.enterRanked()
	}
	return m, nil
}

func (m *Model) enterProjectSelect() (tea.Model, tea.Cmd) {
	projects := m.core.Snapshot().Projects
	if len(projects) > 0 && m.cursor < len(projects) {
		m.core.SelectProject(projects[m.cursor])
		m.Sync()
		return m, m.launch(m.core.Handoff())
	}
	return m, nil
}

func (m *Model) enterPortfolio() (tea.Model, tea.Cmd) {
	candidates := m.filteredCandidates()
	if len(candidates) > 0 {
		if cmd, handled := m.activateCandidate(candidates[min(m.cursor, len(candidates)-1)]); handled {
			return m, cmd
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
	return m, nil
}

// activateCandidate runs the first-run action for the selected scan-root
// candidate. The bool reports whether the candidate produced an action; an
// unknown kind falls through to the portfolio rows.
func (m *Model) activateCandidate(candidate launcher.Candidate) (tea.Cmd, bool) {
	switch candidate.Kind {
	case launcher.CandidateProduct:
		previousScreen := m.core.Snapshot().Screen
		if err := m.core.SelectProduct(m.ctx, candidate.ProductID); err != nil {
			m.setError(err)
		} else if m.core.Snapshot().Screen != previousScreen {
			m.navigation = append(m.navigation, navigationPosition{cursor: m.cursor, scroll: m.scroll})
		}
		m.Sync()
		return nil, true
	case launcher.CandidateWork:
		if !candidate.Available {
			m.setError(fmt.Errorf("work item %s has no claimed worktree", candidate.ID))
			m.Sync()
			return nil, true
		}
		m.core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenProduct, AmbientProduct: candidate.ProductID, SelectedWorkID: candidate.WorkID, Session: launcher.SessionHandoff{ProductID: candidate.ProductID, WorkID: candidate.WorkID, Agent: launcher.DefaultSessionAgent}, Coverage: "authoritative", Section: launcher.SectionRanked})
		return m.launch(m.core.Handoff()), true
	case launcher.CandidateProject:
		m.core.RestoreSnapshot(launcher.Snapshot{Screen: launcher.ScreenPortfolio, Session: launcher.SessionHandoff{ProjectPath: candidate.Path, Agent: launcher.DefaultSessionAgent}, Coverage: "authoritative"})
		return m.launch(m.core.Handoff()), true
	}
	return nil, false
}

func (m *Model) enterRanked() (tea.Model, tea.Cmd) {
	rows := m.filteredRanked()
	if len(rows) == 0 || m.cursor >= len(rows) {
		return m, nil
	}
	selected := rows[m.cursor]
	if selected.Backlog {
		m.issueMode = true
		m.input.Prompt = "ISSUE KEY: "
		m.input.Reset()
		return m, m.input.Focus()
	}
	if selected.Live > 0 {
		m.confirmWork = true
		return m, nil
	}
	return m.openSelectedWork()
}

func (m *Model) escapeKey() (tea.Model, tea.Cmd) {
	if m.core.Snapshot().ProjectSelect {
		m.core.BackProjects()
		m.Sync()
		return m, nil
	}
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

func (m *Model) openSelectedWork() (tea.Model, tea.Cmd) {
	rows := m.filteredRanked()
	if len(rows) == 0 || m.cursor >= len(rows) || rows[m.cursor].Backlog {
		return m, nil
	}
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
	return m, nil
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
	snapshot := m.core.Snapshot()
	rows := snapshot.Ranked
	needle := strings.ToLower(m.filterValue)
	out := make([]launcher.RankedWork, 0, len(rows)+1)
	for _, row := range rows {
		if snapshot.ActiveWorkOnly && row.Terminal {
			continue
		}
		if needle == "" || strings.Contains(strings.ToLower(row.ID+" "+row.Title+" "+row.Kind+" "+row.Lifecycle), needle) {
			out = append(out, row)
		}
	}
	if snapshot.Backlog && (needle == "" || strings.Contains("new backlog", needle)) {
		out = append(out, launcher.RankedWork{ID: "backlog", Kind: "new", Title: "New / Backlog", Lifecycle: "needed", Backlog: true})
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
	if m.width < 3 || m.height < 3 {
		return 1
	}
	content := m.renderContent(m.snapshot, m.cursor)
	page := m.height - 4 - len(content.header) - len(content.tail) - len(content.footer)
	if len(content.tableHeaders) > 0 {
		page--
	}
	if len(content.rows) > 0 {
		page--
	}
	return max(1, page)
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
	if s.ProjectSelect {
		return len(s.Projects)
	}
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

func (m *Model) Init() tea.Cmd { return nil }
func (m *Model) View() tea.View {
	view := tea.NewView(m.Render())
	view.AltScreen = true
	return view
}

// Render uses the last explicit projection and local interaction state.
func (m *Model) Render() string {
	snapshot := m.snapshot
	m.keys.Search.SetEnabled(snapshot.Screen != launcher.ScreenPortfolio)
	m.keys.Filter.SetEnabled(snapshot.Screen != launcher.ScreenWork)
	m.keys.Section.SetEnabled(snapshot.Screen != launcher.ScreenPortfolio)
	m.keys.Launch.SetEnabled(snapshot.Screen != launcher.ScreenPortfolio)

	content := m.renderContent(snapshot, m.cursor)
	if m.confirmWork {
		content.header = append(content.header, "CONFIRM: a live session holds this work. Press Enter to launch or Esc to cancel.")
	}
	header := "CONCORD LAUNCHER"
	if snapshot.AmbientProduct != "" {
		header += " | PRODUCT: " + snapshot.AmbientProduct
	}
	status := "COVERAGE: " + coverageValue(snapshot.Coverage)
	if snapshot.StatusMessage != "" {
		status = "STATUS: " + snapshot.StatusMessage
	}
	statusBar := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(m.width/2).Render("FOCUS: "+focusText(snapshot)),
		lipgloss.NewStyle().Width(m.width-m.width/2).Render(status),
	)
	// The pane owns the help footer; the frame carries only the header and
	// status bar above it.
	return lipgloss.NewStyle().
		Width(max(1, m.width)).
		Height(max(1, m.height)).
		Render(lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Width(m.width).Render(header),
			statusBar,
			pane(content, m.width, max(1, m.height-2), m.scroll),
		))
}

func (m *Model) renderContent(snapshot launcher.Snapshot, cursor int) renderedPane {
	if snapshot.ProjectSelect {
		return m.renderProjects(snapshot, cursor)
	}
	if snapshot.Screen == launcher.ScreenProduct {
		return m.renderS2(m.projection.Header, cursor)
	}
	if snapshot.Screen == launcher.ScreenWork {
		return m.renderS3(m.projection.Header, cursor)
	}
	return m.renderPortfolio(snapshot, cursor)
}

func (m *Model) renderPortfolio(snapshot launcher.Snapshot, cursor int) renderedPane {
	projection := m.projection
	rows := m.filteredRows()
	if len(rows) == 0 && len(snapshot.Candidates) > 0 {
		return m.renderCandidates(snapshot, cursor)
	}
	header := append([]string{}, projection.Header...)
	header = append(header, probeLines(snapshot.Probes)...)
	if m.filterMode {
		header = append(header, m.input.View())
	} else if m.filterValue != "" {
		hidden := len(snapshot.Rows) - len(rows)
		header = append(header, "FILTERED: "+m.filterValue+" (hidden: "+fmtInt(hidden)+")")
	}
	if len(snapshot.Rows) == 0 && snapshot.Coverage == "first_run" {
		header = append(header, "FIRST RUN: no database; initialize through the operator setup")
	} else if len(snapshot.Rows) == 0 && snapshot.Coverage == "authoritative" {
		header = append(header, "PORTFOLIO: authoritative-empty")
	} else if snapshot.StatusMessage != "" {
		header = append(header, "STATUS: "+snapshot.StatusMessage)
	}
	renderedRows := make([][]string, 0, len(rows))
	severities := make([]rowSeverity, 0, len(rows))
	for _, row := range rows {
		values := []string{row.Name + row.NameSuffix, row.Stage, relianceText(row), actionText(row), row.Focus}
		renderedRows = append(renderedRows, values)
		severities = append(severities, productRowSeverity(row))
	}
	footer := m.footerLines()
	return renderedPane{header: header, tableHeaders: projection.Columns, rows: renderedRows, severities: severities, footer: footer, cursor: cursor, color: m.profile.Color}
}

func (m *Model) renderS2(headers []string, cursor int) renderedPane {
	header := append([]string{}, headers...)
	s := m.snapshot
	header = append(header, probeLines(s.Probes)...)
	stack := s.S2AnswerStack()
	header = append(header, "S2 PRODUCT COORDINATION")
	if s.StatusMessage != "" {
		header = append(header, "STATUS: "+s.StatusMessage)
	}
	if m.filterMode || m.issueMode {
		header = append(header, m.input.View())
	} else if m.filterValue != "" {
		header = append(header, "FILTERED: "+m.filterValue+" (hidden: "+fmtInt(len(s.Ranked)-len(m.filteredRanked()))+")")
	}
	var rows [][]string
	var severities []rowSeverity
	var tableHeaders []string
	var tail []string
	focusedSeen := false
	for _, panel := range stack.Panels {
		focused := s.PanelFocus == panel || (s.PanelFocus == "" && panel == launcher.S2PanelDomain)
		panelHeader, panelTableHeaders, panelRows, panelTail, panelSeverities := s2PanelContent(panel, focused, stack, s, m.filteredRanked())
		if !focusedSeen && !focused {
			header = append(header, panelHeader...)
			continue
		}
		if focusedSeen {
			tail = append(tail, panelHeader...)
			tail = append(tail, panelTail...)
			continue
		}
		header = append(header, panelHeader...)
		tableHeaders = panelTableHeaders
		rows = append(rows, panelRows...)
		severities = append(severities, panelSeverities...)
		tail = append(tail, panelTail...)
		focusedSeen = true
	}
	if s.QueryResult {
		tail = append(tail, "KNOWLEDGE WATERMARK: "+s.Knowledge.Watermark+" STATE: "+s.Knowledge.State)
	}
	if s.QueryResult && len(s.Knowledge.Items) > 0 {
		tail = append(tail, "KNOWLEDGE MATCHES:")
		for _, item := range s.Knowledge.Items {
			tail = append(tail, "  "+item.Kind+" "+item.ID+" "+item.Title)
		}
	}
	if s.QueryResult {
		tail = append(tail, "QUERY RESULT: "+s.QuerySubmitted+" (Esc restores prior view)")
	}
	footer := m.footerLines()
	return renderedPane{header: header, tableHeaders: tableHeaders, rows: rows, severities: severities, tail: tail, footer: footer, cursor: cursor, color: m.profile.Color}
}

func s2PanelContent(panel launcher.S2Panel, expanded bool, stack launcher.S2AnswerStack, snapshot launcher.Snapshot, ranked []launcher.RankedWork) (header []string, tableHeaders []string, rows [][]string, tail []string, severities []rowSeverity) {
	if !expanded {
		switch panel {
		case launcher.S2PanelDomain:
			return domainSummaryLines(stack.Domain.Domain), nil, nil, nil, nil
		case launcher.S2PanelBlocked:
			return blockedSummaryLines(stack.Blocked.Work, snapshot), nil, nil, nil, nil
		case launcher.S2PanelNext:
			return nextSummaryLines(stack.Next.Work, snapshot), nil, nil, nil, nil
		}
	}
	switch panel {
	case launcher.S2PanelDomain:
		domainHeader, domainHeaders, domainRows, domainSeverities := domainLines(snapshot.Domains)
		return append([]string{"DOMAIN:"}, domainHeader...), domainHeaders, domainRows, append(knowledgeLines(snapshot.Knowledge), relationLines(snapshot.Relations)...), domainSeverities
	case launcher.S2PanelBlocked, launcher.S2PanelNext:
		rankedHeaders, rankedRows, rankedSeverities := rankedTable(ranked, snapshot)
		return []string{"BLOCKED/BLOCKERS:"}, rankedHeaders, rankedRows, nil, rankedSeverities
	default:
		return nil, nil, nil, nil, nil
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

func rankedTable(ranked []launcher.RankedWork, snapshot launcher.Snapshot) ([]string, [][]string, []rowSeverity) {
	if len(ranked) == 0 {
		return []string{"Work"}, [][]string{{"WORK: " + drillDownEmptyState(snapshot)}}, []rowSeverity{severityNone}
	}
	collapsed := launcher.CollapsedRankedKeys(ranked, snapshot)
	keys := make([]launcher.RankedColumn, 0, len(launcher.RankedColumns(ranked[0], snapshot)))
	for _, column := range launcher.RankedColumns(ranked[0], snapshot) {
		if collapsed[column.Key] {
			continue
		}
		for _, item := range ranked {
			for _, candidate := range launcher.RankedColumns(item, snapshot) {
				if candidate.Key == column.Key && candidate.Value != "" {
					keys = append(keys, column)
					goto nextColumn
				}
			}
		}
	nextColumn:
	}
	headers := []string{"Work"}
	for _, column := range keys {
		headers = append(headers, column.Key)
	}
	rows := make([][]string, 0, len(ranked))
	severities := make([]rowSeverity, 0, len(ranked))
	for i, item := range ranked {
		line := fmtInt(i+1) + " " + rankedMarker(&item) + " " + item.ID + " " + item.Title
		if len(item.Blockers) > 0 {
			blockers := make([]string, 0, len(item.Blockers))
			for _, blocker := range item.Blockers {
				external := ""
				if blocker.External {
					external = " external"
				}
				blockers = append(blockers, blocker.ID+"/"+blocker.Title+" authority="+blocker.Authority+" age="+blocker.Age+external)
			}
			line += " blockers=" + strings.Join(blockers, ",")
		}
		row := []string{line}
		severities = append(severities, rankedRowSeverity(&item))
		values := launcher.RankedColumns(item, snapshot)
		for _, key := range keys {
			for _, column := range values {
				if column.Key == key.Key {
					row = append(row, column.Key+"="+column.Value)
					break
				}
			}
		}
		rows = append(rows, row)
	}
	return headers, rows, severities
}

// rankedRowSeverity colours the rows whose rendered text already carries the
// "!BLOCKED" readiness marker.
func rankedRowSeverity(item *launcher.RankedWork) rowSeverity {
	if item.Readiness() == "blocked" {
		return severityAttention
	}
	return severityNone
}

func (m *Model) renderS3(headers []string, cursor int) renderedPane {
	header := append([]string{}, headers...)
	s := m.snapshot
	header = append(header, probeLines(s.Probes)...)
	if s.QueryResult {
		header = append(header, "S3 WORK SEARCH", "QUERY RESULT: "+s.QuerySubmitted+" (Esc restores prior view)")
		for _, item := range s.Ranked {
			header = append(header, "WORK MATCH: "+item.ID+" "+item.Title+" lifecycle="+item.Lifecycle)
		}
		header = append(header, "KNOWLEDGE WATERMARK: "+s.Knowledge.Watermark+" STATE: "+s.Knowledge.State)
		header = append(header, knowledgeLines(s.Knowledge)...)
		footer := m.footerLines()
		return renderedPane{header: header, footer: footer, cursor: cursor, color: m.profile.Color}
	}
	d := s.Detail
	urgency := d.Item.Urgency
	if urgency == "" {
		urgency = "standard"
	}
	header = append(header, "S3 WORK DETAIL", "WORK: "+d.Item.ID+" "+d.Item.Title, "LIFECYCLE: "+d.Item.Lifecycle+" PRIORITY: "+fmtInt64(d.Item.Priority)+" URGENCY: "+urgency, "SECTION: "+string(s.Section), "PROJECTS: "+strings.Join(d.Projects, ", "), "WORKFLOW: "+d.Workflow)
	if s.StatusMessage != "" {
		header = append(header, "STATUS: "+s.StatusMessage)
	}
	if d.Item.Blocked {
		for _, b := range d.Item.Blockers {
			header = append(header, "BLOCKER: "+b.ID+" "+b.Title+" authority="+b.Authority+" age="+b.Age)
		}
	} else {
		header = append(header, "BLOCKED: no")
	}
	var rows [][]string
	severities := make([]rowSeverity, 0, len(d.History))
	switch s.Section {
	case launcher.SectionKnowledge:
		header = append(header, knowledgeLines(s.Knowledge)...)
	case launcher.SectionRelations:
		for _, e := range d.Edges {
			header = append(header, "EDGE "+e.Kind+": "+e.Source+" -> "+e.Target)
		}
	case launcher.SectionRanked:
		rows = make([][]string, 0, len(d.History))
		for _, h := range d.History {
			rows = append(rows, []string{"HISTORY: " + h})
			severities = append(severities, severityNone)
		}
	}
	tail := []string{}
	if s.QueryResult && len(s.Knowledge.Items) > 0 {
		tail = append(tail, "KNOWLEDGE MATCHES:")
		for _, item := range s.Knowledge.Items {
			tail = append(tail, "  "+item.Kind+" "+item.ID+" "+item.Title)
		}
	}
	footer := m.footerLines()
	tableHeaders := []string(nil)
	if s.Section == launcher.SectionRanked {
		tableHeaders = []string{"History"}
	}
	return renderedPane{header: header, tableHeaders: tableHeaders, rows: rows, severities: severities, tail: tail, footer: footer, cursor: cursor, color: m.profile.Color}
}

func domainLines(section launcher.DomainSection) ([]string, []string, [][]string, []rowSeverity) {
	if !section.Read {
		return []string{"DOMAINS: unavailable: not_read"}, nil, nil, nil
	}
	if section.State == "unavailable" {
		reason := section.Reason
		if reason == "" {
			reason = "unavailable"
		}
		return []string{"DOMAINS: unavailable: " + reason}, nil, nil, nil
	}
	var header []string
	var rows [][]string
	var severities []rowSeverity
	if len(section.Domains) == 0 {
		header = append(header, "DOMAINS: authoritative-empty")
	}
	for _, domain := range section.Domains {
		marker := "DOMAIN"
		if domain.Home {
			marker = "HOME"
		}
		parent := domain.ParentID
		if parent == "" {
			parent = "-"
		}
		relations := 0
		for _, relation := range section.Relations {
			if relation.Source == domain.ID || relation.Target == domain.ID {
				relations++
			}
		}
		rows = append(rows, []string{
			domain.ID + " " + domain.Name,
			marker,
			parent,
			fmt.Sprintf("r%d law%d act%d", relations, domain.CurrentLawCount, domain.ActiveWorkCount),
		})
		severities = append(severities, severityNone)
	}
	for _, relation := range section.Relations {
		header = append(header, "RELATION "+relation.Kind+": "+relation.Source+" -> "+relation.Target+" state="+relation.State)
	}
	for _, pair := range section.Overlaps {
		resolution := pair.State
		if resolution == "" {
			resolution = "absent"
		}
		header = append(header, "OVERLAP "+pair.From+" & "+pair.To+" domains="+strings.Join(pair.SharedDomains, ",")+" resolution="+resolution)
	}
	if section.Truncated {
		header = append(header, "DOMAINS: truncated: bounded read reached")
	}
	return header, []string{"Domain", "Marker", "Parent", "Relations"}, rows, severities
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

func (m *Model) footerLines() []string {
	m.help.SetWidth(max(1, m.width-2))
	value := m.help.View(m.keys)
	if m.showHelp {
		value = "HELP: " + value
	}
	return strings.Split(value, "\n")
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

// productRowSeverity reads the same row facts relianceText and actionText
// read: a reliance that is not clear, ready or empty, a stale or
// execution-blocking reliance, and unavailable counts all already carry a
// text marker, so colour stays redundant with the words on screen.
func productRowSeverity(row launcher.ProductRow) rowSeverity {
	if row.CountsState == "unavailable" {
		return severityAttention
	}
	reliance := row.Reliance
	if row.RelianceStale || row.BlocksExecution {
		reliance = "stale"
	}
	if reliance != "" && reliance != "clear" && reliance != "ready" {
		return severityAttention
	}
	return severityNone
}

// candidateRowSeverity colours the rows whose rendered text already says
// "unavailable" or "blocked=true".
func candidateRowSeverity(candidate launcher.Candidate) rowSeverity {
	if candidate.Blocked || (!candidate.Available && candidate.State == "") {
		return severityAttention
	}
	return severityNone
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
	env := make([]string, 0, len(os.Environ())+5)
	inheritedAgent := ""
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "CONCORD_SELECTED_AGENT=") {
			inheritedAgent = strings.TrimPrefix(value, "CONCORD_SELECTED_AGENT=")
			env = append(env, value)
			continue
		}
		if strings.HasPrefix(value, "CONCORD_SELECTED_PRODUCT_ID=") || strings.HasPrefix(value, "CONCORD_SELECTED_WORK_ID=") || strings.HasPrefix(value, "CONCORD_SELECTED_PROMPT=") || strings.HasPrefix(value, "CONCORD_SELECTED_PROJECT_PATH=") {
			continue
		}
		env = append(env, value)
	}
	if inheritedAgent == "" && handoff.Agent != "" {
		env = append(env, "CONCORD_SELECTED_AGENT="+handoff.Agent)
	}
	if handoff.ProjectPath != "" {
		env = append(env, "CONCORD_SELECTED_PROJECT_PATH="+handoff.ProjectPath)
	} else {
		env = append(env, "CONCORD_SELECTED_PRODUCT_ID="+handoff.ProductID)
	}
	if handoff.WorkID != "" {
		env = append(env, "CONCORD_SELECTED_WORK_ID="+handoff.WorkID)
	}
	if handoff.Prompt != "" {
		env = append(env, "CONCORD_SELECTED_PROMPT="+handoff.Prompt)
	}
	return env
}

func (m *Model) renderCandidates(snapshot launcher.Snapshot, cursor int) renderedPane {
	header := []string{"CANDIDATES", "STATUS: " + snapshot.Coverage}
	if snapshot.StatusMessage != "" {
		header = append(header, "MESSAGE: "+snapshot.StatusMessage)
	}
	header = append(header, probeLines(snapshot.Probes)...)
	values := launcher.FilterCandidates(snapshot.Candidates, m.filterValue)
	if m.cursor >= 0 && m.cursor < len(values) {
		header = append(header, candidatePreviewLines(values[m.cursor])...)
	}
	rows := make([][]string, 0, len(values))
	severities := make([]rowSeverity, 0, len(values))
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
		state := candidate.State
		if state == "" {
			state = available
		}
		blocked := ""
		if candidate.Blocked {
			blocked = " blocked=true"
		}
		rows = append(rows, []string{fmtInt(i+1) + " " + marker + " " + string(candidate.Kind) + " " + name + " state=" + state + blocked + " live=" + fmtInt(candidate.Live)})
		severities = append(severities, candidateRowSeverity(candidate))
	}
	if len(values) == 0 {
		header = append(header, "CANDIDATES: authoritative-empty")
	}
	if m.filterMode {
		header = append(header, m.input.View())
	}
	footer := m.footerLines()
	return renderedPane{header: header, tableHeaders: []string{"Candidate"}, rows: rows, severities: severities, footer: footer, cursor: cursor, color: m.profile.Color}
}

func (m *Model) renderProjects(snapshot launcher.Snapshot, cursor int) renderedPane {
	header := []string{"PROJECTS", "PRODUCT: " + snapshot.AmbientProduct}
	rows := make([][]string, 0, len(snapshot.Projects))
	severities := make([]rowSeverity, 0, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		rows = append(rows, []string{project.Name + " role=" + project.Role + " path=" + project.Path})
		severities = append(severities, severityNone)
	}
	if len(rows) == 0 {
		header = append(header, "PROJECTS: authoritative-empty")
	}
	return renderedPane{header: header, tableHeaders: []string{"Project"}, rows: rows, severities: severities, footer: m.footerLines(), cursor: cursor, color: m.profile.Color}
}

func focusText(snapshot launcher.Snapshot) string {
	if snapshot.SelectedWorkID != "" {
		return "work/" + snapshot.SelectedWorkID
	}
	if snapshot.AmbientProduct != "" {
		return "product/" + snapshot.AmbientProduct
	}
	return "portfolio"
}

func coverageValue(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func pane(content renderedPane, width, height, offset int) string {
	width = max(3, width)
	height = max(3, height)
	innerWidth := width - 2
	innerHeight := height - 2
	header := renderTextRows(content.header, innerWidth)
	tail := renderTextRows(content.tail, innerWidth)
	footer := renderTextRows(content.footer, innerWidth)
	tableHeight := max(1, innerHeight-len(header)-len(tail)-len(footer))
	if len(content.rows) > 0 {
		tableHeight = max(3, tableHeight)
	}
	data := renderTable(content.tableHeaders, content.rows, content.severities, innerWidth, tableHeight, offset, content.cursor, content.color)
	lines := make([]string, 0, len(header)+len(tail)+len(footer)+strings.Count(data, "\n")+1)
	lines = append(lines, header...)
	if data != "" {
		lines = append(lines, strings.Split(data, "\n")...)
	}
	lines = append(lines, tail...)
	lines = append(lines, footer...)
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(lipgloss.RoundedBorder()).
		Render(strings.Join(lines, "\n"))
}

func renderTextRows(lines []string, width int) []string {
	if len(lines) == 0 {
		return nil
	}
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, []string{line})
	}
	renderedLines := strings.Split(renderTable([]string{""}, rows, nil, width, len(rows)+2, 0, -1, false), "\n")
	if len(renderedLines) > 0 {
		return renderedLines[1:]
	}
	return nil
}

// renderTable renders a data table through lipgloss. cursor selects the row
// that carries the gutter marker; cursor < 0 renders plain text rows with no
// gutter. The gutter is its own leading column with no right padding, so a
// header cell and its data cell share one left edge at every width and the
// resizing pass allocates the remaining columns once for header and rows.
func renderTable(headers []string, rows [][]string, severities []rowSeverity, width, height, offset, cursor int, color bool) string {
	if len(headers) == 0 && len(rows) == 0 {
		return ""
	}
	gutter := cursor >= 0
	tableHeaders := headers
	tableRows := rows
	if gutter {
		tableHeaders = append([]string{""}, headers...)
		tableRows = make([][]string, 0, len(rows))
		for i, row := range rows {
			marker := "  "
			if i == cursor {
				marker = "> "
			}
			tableRows = append(tableRows, append([]string{marker}, row...))
		}
	}
	view := table.New().
		Headers(tableHeaders...).
		Rows(tableRows...).
		Width(max(1, width)).
		Height(max(1, height)).
		YOffset(max(0, offset)).
		Wrap(false).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderHeader(false).
		BorderColumn(false).
		BorderRow(false).
		StyleFunc(func(row, col int) lipgloss.Style {
			if gutter && col == 0 {
				// The gutter is a fixed two-column budget: the resizing pass
				// must never grow or shrink it, or a single-column table
				// would push its content to an arbitrary offset.
				return lipgloss.NewStyle().Width(2)
			}
			if len(tableHeaders) == 1 {
				// A lone column has no right-hand neighbor, so its padding
				// buys nothing and steals width from single-line text rows
				// such as the help footer.
				return attentionForeground(lipgloss.NewStyle(), color, row, severities)
			}
			style := lipgloss.NewStyle().PaddingRight(2)
			if color && (row == table.HeaderRow || row == cursor) {
				style = style.Bold(true)
			}
			return attentionForeground(style, color, row, severities)
		})
	rendered := view.Render()
	_, _ = view.FirstVisibleRowIndex(), view.LastVisibleRowIndex()
	return rendered
}

// attentionForeground spends the colour flag on the row's projected severity.
// It never inspects the rendered cells, and row < 0 (the header row) and a
// shorter severities slice both render plain.
func attentionForeground(style lipgloss.Style, color bool, row int, severities []rowSeverity) lipgloss.Style {
	if color && row >= 0 && row < len(severities) && severities[row] == severityAttention {
		return style.Foreground(attentionColor)
	}
	return style
}

func candidatePreviewLines(candidate launcher.Candidate) []string {
	lines := []string{"PREVIEW: " + string(candidate.Kind) + " " + candidate.Name}
	if candidate.Path != "" {
		lines = append(lines, "PATH: "+candidate.Path)
	}
	if candidate.Worktree != "" {
		lines = append(lines, "WORKTREE: "+candidate.Worktree)
	}
	state := candidate.State
	if state == "" {
		state = "unavailable"
		if candidate.Available {
			state = "available"
		}
	}
	lines = append(lines, "STATE: "+state, "BLOCKED: "+fmtBool(candidate.Blocked), "LIVE SESSIONS: "+fmtInt(candidate.Live))
	return lines
}

func fmtBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
