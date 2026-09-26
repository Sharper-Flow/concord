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
	Launch  key.Binding
	Pin     key.Binding
	Unpin   key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Move, k.Filter, k.Search, k.Refresh, k.Open, k.Launch, k.Pin, k.Unpin, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Move, k.Page, k.Filter, k.Search, k.Refresh}, {k.Open, k.Back, k.Launch, k.Pin, k.Unpin, k.Help, k.Quit, k.Clear}}
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
			Open:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open issue")),
			Back:    key.NewBinding(key.WithKeys("esc", "h", "←"), key.WithHelp("esc", "back")),
			Page:    key.NewBinding(key.WithKeys("ctrl+d", "ctrl+u", "n", "p"), key.WithHelp("ctrl-d/u", "page")),
			Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
			Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
			Clear:   key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "clear")),
			Search:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "search")),
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
	// The renderer prices cells with the measurement library it renders
	// with and hands it to the projection: the core keeps column priority
	// without carrying width logic of its own.
	m.projection = launcher.Project(m.snapshot, m.columnBudget(), lipgloss.Width)
	m.clampCursor()
}

// columnBudget is the width budget the projected table may span: the frame's
// inner width after the table's border, less the cursor gutter the table
// renders inside itself.
func (m *Model) columnBudget() int {
	return max(1, m.width-4)
}

// OpenFilter enters S1's read-free local filter mode.
func (m *Model) OpenFilter() tea.Cmd {
	if m.core.Snapshot().ProjectSelect {
		return nil
	}
	m.filterMode = true
	m.queryMode = false
	m.input.Prompt = "FILTER: "
	m.input.SetValue(m.filterValue)
	return m.input.Focus()
}

// openQuery enters the semantic query input. The portfolio screen opens no
// query mode: it has no ambient Product to search under.
func (m *Model) openQuery() tea.Cmd {
	if m.core.Snapshot().Screen != launcher.ScreenProduct {
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
	// Control keys carry no Text: String() returns Text when it is set, and a
	// real terminal control press decodes to {Code, Mod} with empty Text, so
	// a Text here would dispatch the bare letter instead of the chord.
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
		key = tea.Key{Code: 'l', Mod: tea.ModCtrl}
	case "ctrl+d":
		key = tea.Key{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		key = tea.Key{Code: 'u', Mod: tea.ModCtrl}
	case "ctrl+c":
		key = tea.Key{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+p":
		key = tea.Key{Code: 'p', Mod: tea.ModCtrl}
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
		m.setLaunchError(msg.err)
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
		return m.launchSelectedRow()
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
	case "o":
		m.openSelectedIssue()
		return m, nil
	case "l":
		if m.core.Snapshot().Screen == launcher.ScreenProduct {
			return m.launchSelected()
		}
	case "enter":
		return m.enterKey()
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
	case "esc", "h", "left":
		return m.escapeKey()
	case "q", "ctrl+c":
		if m.core.Snapshot().Screen == launcher.ScreenProduct {
			m.back()
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) enterKey() (tea.Model, tea.Cmd) {
	if m.core.Snapshot().ProjectSelect {
		return m.enterProjectSelect()
	}
	if m.core.Snapshot().Screen == launcher.ScreenPortfolio {
		return m.enterPortfolio()
	}
	if m.core.Snapshot().Screen == launcher.ScreenProduct {
		return m.launchSelected()
	}
	return m, nil
}

// launchSelected acts on the selected work row: the New/Backlog picker row
// opens the issue prompt, a live session's work opens the occupied-work
// confirmation, and every other row launches the session bootstrap directly
// from the row.
func (m *Model) launchSelected() (tea.Model, tea.Cmd) {
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
	return m.launchSelectedRow()
}

// launchSelectedRow launches the session bootstrap for the row under the
// cursor without a detail read or screen change.
func (m *Model) launchSelectedRow() (tea.Model, tea.Cmd) {
	rows := m.filteredRanked()
	if len(rows) == 0 || m.cursor >= len(rows) || rows[m.cursor].Backlog {
		return m, nil
	}
	return m, m.launch(m.workHandoff(rows[m.cursor]))
}

func (m *Model) workHandoff(item launcher.RankedWork) launcher.SessionHandoff {
	return launcher.SessionHandoff{ProductID: m.core.Snapshot().AmbientProduct, WorkID: item.ID, Agent: launcher.DefaultSessionAgent}
}

// openIssueURL opens a linked Linear issue in the operator's browser. The
// indirection keeps the process out of tests.
var openIssueURL = func(url string) error {
	return exec.Command("xdg-open", url).Start() //nolint:gosec // the URL comes from the store's confirmed link, and the fixed argv does not invoke a shell.
}

// openSelectedIssue opens the selected row's linked Linear issue. A row
// without a linked issue offers nothing to open.
func (m *Model) openSelectedIssue() {
	if m.core.Snapshot().Screen != launcher.ScreenProduct {
		return
	}
	rows := m.filteredRanked()
	if len(rows) == 0 || m.cursor >= len(rows) {
		return
	}
	url := rows[m.cursor].LinearIssueURL
	if url == "" {
		return
	}
	if err := openIssueURL(url); err != nil {
		m.setLaunchError(err)
	}
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
	displayed := m.displayedPortfolio()
	if displayed.candidates != nil {
		if len(displayed.candidates) > 0 {
			if cmd, handled := m.activateCandidate(displayed.candidates[min(m.cursor, len(displayed.candidates)-1)]); handled {
				return m, cmd
			}
		}
		return m, nil
	}
	rows := displayed.products
	if len(rows) > 0 && m.cursor < len(rows) {
		if err := m.core.SelectProduct(m.ctx, rows[m.cursor].ID); err != nil {
			m.setError(err)
		} else {
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
			m.setLaunchError(fmt.Errorf("work item %s has no claimed worktree", candidate.ID))
			m.Sync()
			return nil, true
		}
		// The handoff comes from the candidate alone. The screen keeps what
		// the read produced, so a refused launch changes no state.
		return m.launch(launcher.SessionHandoff{ProductID: candidate.ProductID, WorkID: candidate.WorkID, Agent: launcher.DefaultSessionAgent}), true
	case launcher.CandidateProject:
		return m.launch(launcher.SessionHandoff{ProjectPath: candidate.Path, Agent: launcher.DefaultSessionAgent}), true
	}
	return nil, false
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

// setLaunchError reports a refused launch through the status message alone.
// A failed launch reports the refusal and changes no screen state: coverage,
// reliance, watermark, and rows stay as the work read set them.
func (m *Model) setLaunchError(err error) {
	snapshot := m.core.Snapshot()
	snapshot.StatusMessage = err.Error()
	m.core.RestoreSnapshot(snapshot)
}

// The filtered* and count helpers read the synced snapshot, never the core:
// event handlers Sync after every core mutation, so both paths see the same
// state and the render path cannot observe a core change before its Sync.

// displayedPortfolio is the one decision of which portfolio list the frame
// displays: the Product rows when any survives the filter, otherwise the
// interleaved candidate list. Render, the cursor bound, Enter, and pin all
// read it, so every key acts on the row the frame highlights. A nil
// candidates slice means the Product table displays; a non-nil slice means
// the candidate list displays, possibly empty after a filter that leaves it
// nothing to show.
type displayedPortfolio struct {
	products   []launcher.ProductRow
	candidates []launcher.Candidate
}

func (m *Model) displayedPortfolio() displayedPortfolio {
	if rows := m.filteredRows(); len(rows) > 0 {
		return displayedPortfolio{products: rows}
	}
	if len(m.snapshot.Candidates) > 0 {
		return displayedPortfolio{candidates: m.filteredCandidates()}
	}
	return displayedPortfolio{}
}

func (m *Model) filteredRows() []launcher.ProductRow {
	rows := m.snapshot.Rows
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

// filterRanked is the active-only picker: terminal work stays in the read
// projection but never lands on the list, the needle keeps the rows whose
// rendered facts match, and the New/Backlog picker row ends the list.
func filterRanked(snapshot launcher.Snapshot, needle string) []launcher.RankedWork {
	needle = strings.ToLower(needle)
	out := make([]launcher.RankedWork, 0, len(snapshot.Ranked)+1)
	for _, row := range launcher.SortRankedByRecency(snapshot.Ranked) {
		if snapshot.ActiveWorkOnly && row.Terminal {
			continue
		}
		if needle == "" || strings.Contains(strings.ToLower(row.ID+" "+row.Title+" "+row.Kind+" "+row.Lifecycle+" "+row.LinearIssueKey), needle) {
			out = append(out, row)
		}
	}
	if snapshot.Backlog && (needle == "" || strings.Contains("new backlog", needle)) {
		out = append(out, launcher.RankedWork{ID: "backlog", Kind: "new", Title: "New / Backlog", Lifecycle: "needed", Backlog: true})
	}
	return out
}

func (m *Model) filteredRanked() []launcher.RankedWork {
	return filterRanked(m.snapshot, m.filterValue)
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
	// The status bar, the pane border, and the footer frame the table; the
	// last two subtractions price the table's header row and its
	// scroll-indicator line.
	page := m.height - 3 - len(m.footerLines()) - len(content.header)
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
	s := m.snapshot
	if s.ProjectSelect {
		return len(s.Projects)
	}
	if s.Screen == launcher.ScreenProduct {
		return len(m.filteredRanked())
	}
	displayed := m.displayedPortfolio()
	if displayed.candidates != nil {
		return len(displayed.candidates)
	}
	return len(displayed.products)
}

func (m *Model) filteredCandidates() []launcher.Candidate {
	return launcher.FilterCandidates(m.snapshot.Candidates, m.filterValue)
}

func (m *Model) togglePin(pin bool) {
	displayed := m.displayedPortfolio()
	if displayed.candidates == nil {
		// A displayed Product row has no path, so pin and unpin have no
		// subject on the portfolio table.
		return
	}
	if m.cursor < 0 || m.cursor >= len(displayed.candidates) {
		return
	}
	selected := displayed.candidates[m.cursor]
	snapshot := m.core.Snapshot()
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

// Render uses the last explicit projection and local interaction state. The
// frame is one status line, one bordered table, and the help footer: no
// title line, no diagnostic block, and no second pane. The status bar keeps
// FOCUS left and COVERAGE right, with STATUS replacing COVERAGE only while
// something is actually abnormal.
func (m *Model) Render() string {
	snapshot := m.snapshot
	m.keys.Search.SetEnabled(snapshot.Screen == launcher.ScreenProduct)
	m.keys.Open.SetEnabled(snapshot.Screen == launcher.ScreenProduct)
	m.keys.Launch.SetEnabled(snapshot.Screen == launcher.ScreenProduct)

	content := m.renderContent(snapshot, m.cursor)
	if m.confirmWork {
		content.header = append(content.header, "CONFIRM: a live session holds this work. Press Enter to launch or Esc to cancel.")
	}
	status := "COVERAGE: " + coverageValue(snapshot.Coverage)
	if snapshot.StatusMessage != "" {
		status = "STATUS: " + snapshot.StatusMessage
	}
	// The status bar is one line at every width: each half clips in place
	// instead of wrapping the frame taller.
	statusBar := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(m.width/2).MaxWidth(m.width/2).MaxHeight(1).Render("FOCUS: "+focusText(snapshot)),
		lipgloss.NewStyle().Width(m.width-m.width/2).MaxWidth(m.width-m.width/2).MaxHeight(1).Render(status),
	)
	// The frame owns the help footer as its full-width bottom row: the help
	// model emits an overlong line when no whole binding fits beside the
	// ellipsis, and a wrapped row would push the frame past the terminal
	// height.
	footer := m.footerLines()
	bodyHeight := max(3, m.height-1-len(footer))
	body := pane(content, m.width, bodyHeight, m.scroll)
	return lipgloss.NewStyle().
		Width(max(1, m.width)).
		Height(max(1, m.height)).
		Render(lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Width(m.width).Render(statusBar),
			body,
			lipgloss.NewStyle().MaxWidth(max(1, m.width)).Render(strings.Join(footer, "\n")),
		))
}

func (m *Model) renderContent(snapshot launcher.Snapshot, cursor int) renderedPane {
	if snapshot.ProjectSelect {
		return m.renderProjects(snapshot, cursor)
	}
	return m.renderScreen(snapshot, cursor)
}

// renderScreen renders the one table a screen shows. Every header line is
// abnormal or a mode indicator: quiet means healthy.
func (m *Model) renderScreen(snapshot launcher.Snapshot, cursor int) renderedPane {
	header := m.abnormalLines(snapshot)
	rows, severities, tableHeaders := m.displayedRows(snapshot)
	return renderedPane{header: header, tableHeaders: tableHeaders, rows: rows, severities: severities, cursor: cursor, color: m.profile.Color}
}

// displayedRows selects the rows the frame shows: the candidate feed when the
// portfolio has no surviving Product rows, otherwise the projection's rows
// narrowed by the local filter. The projection's rows and markers stay
// parallel to the snapshot's work order plus the picker row, so the filter
// keeps the marker with its row.
func (m *Model) displayedRows(snapshot launcher.Snapshot) (rows [][]string, severities []rowSeverity, tableHeaders []string) {
	if snapshot.Screen == launcher.ScreenPortfolio && m.displayedPortfolio().candidates != nil {
		return m.candidateRows(snapshot)
	}
	tableHeaders = m.projection.Columns
	if snapshot.Screen == launcher.ScreenProduct {
		allowed := map[string]bool{}
		for _, item := range m.filteredRanked() {
			allowed[item.ID] = true
		}
		ids := make([]string, 0, len(snapshot.Ranked)+1)
		for _, item := range launcher.SortRankedByRecency(snapshot.Ranked) {
			ids = append(ids, item.ID)
		}
		if snapshot.Backlog {
			ids = append(ids, "backlog")
		}
		for i, row := range m.projection.Rows {
			if i < len(ids) && allowed[ids[i]] {
				rows = append(rows, row)
				severities = append(severities, markerSeverity(m.projection.Markers, i))
			}
		}
		// A degraded or empty read renders its single typed state row.
		if len(rows) == 0 && len(snapshot.Ranked) == 0 && len(m.projection.Rows) == 1 && !snapshot.Backlog {
			rows = m.projection.Rows
			severities = []rowSeverity{markerSeverity(m.projection.Markers, 0)}
		}
		return rows, severities, tableHeaders
	}
	allowed := map[string]bool{}
	for _, row := range m.filteredRows() {
		allowed[row.ID] = true
	}
	for i, row := range m.projection.Rows {
		if i < len(snapshot.Rows) && allowed[snapshot.Rows[i].ID] {
			rows = append(rows, row)
			severities = append(severities, markerSeverity(m.projection.Markers, i))
		}
	}
	return rows, severities, tableHeaders
}

func (m *Model) candidateRows(snapshot launcher.Snapshot) (rows [][]string, severities []rowSeverity, tableHeaders []string) {
	tableHeaders = []string{"Candidate"}
	values := m.filteredCandidates()
	for _, candidate := range values {
		marker := " "
		if candidate.Pinned {
			marker = "*"
		}
		state := candidate.State
		if state == "" {
			state = "unavailable"
			if candidate.Available {
				state = "available"
			}
		}
		blocked := ""
		if candidate.Blocked {
			blocked = " blocked=true"
		}
		name := candidate.Name
		if candidate.Path != "" {
			name += " " + candidate.Path
		}
		rows = append(rows, []string{fmtInt(len(rows)+1) + " " + marker + " " + string(candidate.Kind) + " " + name + " state=" + state + blocked + " live=" + fmtInt(candidate.Live)})
		severities = append(severities, markerSeverity(candidateSeverityMarkers(values), len(rows)-1))
	}
	if len(values) == 0 {
		rows = append(rows, []string{"CANDIDATES: authoritative-empty"})
		severities = append(severities, severityNone)
	}
	return rows, severities, tableHeaders
}

// abnormalLines names every line the frame renders above its table. Each one
// reports a state the operator must act on or a mode they turned on.
func (m *Model) abnormalLines(snapshot launcher.Snapshot) []string {
	lines := []string{}
	if snapshot.Screen == launcher.ScreenPortfolio && len(snapshot.Rows) == 0 && snapshot.Coverage == "first_run" {
		lines = append(lines, "FIRST RUN: no database; initialize through the operator setup")
	}
	lines = append(lines, abnormalProbeLines(snapshot.Probes)...)
	lines = append(lines, abnormalDomainLines(snapshot)...)
	if m.filterMode || m.queryMode || m.issueMode {
		lines = append(lines, m.input.View())
	} else if m.filterValue != "" {
		lines = append(lines, "FILTERED: "+m.filterValue+" (hidden: "+fmtInt(m.hiddenRowCount())+")")
	}
	if snapshot.QueryResult {
		lines = append(lines, "QUERY RESULT: "+snapshot.QuerySubmitted+" (Esc restores prior view)")
	}
	return lines
}

// abnormalProbeLines names only the probes that are unavailable: a healthy
// probe stays silent.
func abnormalProbeLines(probes []launcher.ProbeStatus) []string {
	lines := []string{}
	for _, probe := range probes {
		if probe.Available {
			continue
		}
		reason := ""
		if probe.Reason != "" {
			reason = ": " + probe.Reason
		}
		lines = append(lines, strings.ToUpper(probe.Name)+": unavailable"+reason)
	}
	return lines
}

// abnormalDomainLines names the Domain context only when it is abnormal: an
// unavailable section, an incomplete registry, a bounded relation or overlap
// read, or unresolved overlaps. A clean Domain registry stays silent.
func abnormalDomainLines(snapshot launcher.Snapshot) []string {
	section := snapshot.Domains
	if !section.Read {
		return nil
	}
	if section.State == "unavailable" {
		reason := section.Reason
		if reason == "" {
			reason = "unavailable"
		}
		return []string{"DOMAIN: unavailable: " + reason}
	}
	if section.RegistryIncomplete {
		return []string{"DOMAIN: unavailable: domain_registry_incomplete"}
	}
	if section.RelationsTruncated || section.OverlapsTruncated {
		var bounded []string
		if section.RelationsTruncated {
			bounded = append(bounded, "domain_relations_bounded")
		}
		if section.OverlapsTruncated {
			bounded = append(bounded, "domain_overlaps_bounded")
		}
		return []string{"DOMAIN: unavailable: " + strings.Join(bounded, ",")}
	}
	var unresolved []string
	for _, pair := range section.Overlaps {
		if pair.State != "absent" {
			continue
		}
		unresolved = append(unresolved, pair.From+" & "+pair.To+" domains="+strings.Join(pair.SharedDomains, ",")+" resolution=absent")
	}
	if len(unresolved) == 0 {
		return nil
	}
	return []string{"DOMAIN: unresolved overlap: " + strings.Join(unresolved, "; ")}
}

// hiddenRowCount prices the FILTERED line: the rows the filter removes from
// the screen's own list.
func (m *Model) hiddenRowCount() int {
	if m.snapshot.Screen == launcher.ScreenProduct {
		return len(filterRanked(m.snapshot, "")) - len(m.filteredRanked())
	}
	return len(m.snapshot.Rows) - len(m.filteredRows())
}

func (m *Model) footerLines() []string {
	// The footer spans the frame, so the help budget is the frame width less
	// two columns of slack: the help model elides whole bindings at this
	// width, and the text rows must never truncate a binding label the help
	// model already fit.
	m.help.SetWidth(max(1, m.width-2))
	value := m.help.View(m.keys)
	if m.showHelp {
		value = "HELP: " + value
	}
	return strings.Split(value, "\n")
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

func (m *Model) renderProjects(snapshot launcher.Snapshot, cursor int) renderedPane {
	rows := make([][]string, 0, len(snapshot.Projects))
	severities := make([]rowSeverity, 0, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		rows = append(rows, []string{project.Name + " role=" + project.Role + " path=" + project.Path})
		severities = append(severities, severityNone)
	}
	header := []string{}
	if len(rows) == 0 {
		rows = append(rows, []string{"PROJECTS: authoritative-empty"})
		severities = append(severities, severityNone)
	}
	return renderedPane{header: header, tableHeaders: []string{"Project"}, rows: rows, severities: severities, cursor: cursor, color: m.profile.Color}
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
	tableHeight := max(1, innerHeight-len(header))
	if len(content.rows) > 0 {
		tableHeight = max(3, tableHeight)
	}
	data := renderTable(content.tableHeaders, content.rows, content.severities, innerWidth, tableHeight, offset, content.cursor, content.color)
	lines := make([]string, 0, len(header)+strings.Count(data, "\n")+1)
	lines = append(lines, header...)
	if data != "" {
		lines = append(lines, strings.Split(data, "\n")...)
	}
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
			if len(tableHeaders) == 1 || (gutter && len(tableHeaders) == 2) {
				// A lone column has no right-hand neighbor, so its padding
				// buys nothing and steals width from single-line text rows
				// such as the help footer. The same holds for one data column
				// beside the cursor gutter: the padding would price the row
				// over the pane and clip its tail cell.
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

// markerSeverity converts the projection's row markers into the colour
// severities the renderer spends: "!" is attention, anything else is plain.
func markerSeverity(markers []string, row int) rowSeverity {
	if row < len(markers) && markers[row] == "!" {
		return severityAttention
	}
	return severityNone
}

// candidateSeverityMarkers marks the candidate rows whose text already says
// "unavailable" or "blocked=true".
func candidateSeverityMarkers(candidates []launcher.Candidate) []string {
	markers := make([]string, len(candidates))
	for i, candidate := range candidates {
		if candidate.Blocked || (!candidate.Available && candidate.State == "") {
			markers[i] = "!"
		}
	}
	return markers
}
