// Package bubbletea is the only launcher package that imports Charm types.
package bubbletea

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
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
	help                     help.Model
	profile                  Profile
	projection               launcher.Projection
	snapshot                 launcher.Snapshot
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
	if m.core.Snapshot().SelectedWorkID != "" {
		return nil
	}
	m.filterMode = true
	m.queryMode = false
	m.input.Prompt = "FILTER: "
	m.input.SetValue(m.filterValue)
	return m.input.Focus()
}

// openQuery enters the S2/S3 semantic query input. S1 has no semantic-query
// binding, so a snapshot with no ambient Product opens no query mode.
func (m *Model) openQuery() tea.Cmd {
	if m.core.Snapshot().AmbientProduct == "" {
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
		// A selected work item owns its sections, so Tab walks them. With no
		// work item open, Tab moves focus across the Product answer stack.
		if m.core.Snapshot().SelectedWorkID != "" {
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
		} else if hasAnswerStack(m.core.Snapshot()) {
			_ = m.core.CyclePanelFocus()
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
		if m.core.Snapshot().AmbientProduct != "" || m.core.Snapshot().SelectedWorkID != "" {
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
		if m.core.Snapshot().AmbientProduct == "" {
			candidates := m.filteredCandidates()
			if len(candidates) > 0 {
				candidate := candidates[m.cursor]
				if candidate.Kind == launcher.CandidateProduct {
					previousPosition := navigationIdentity(m.core.Snapshot())
					if err := m.core.SelectProduct(m.ctx, candidate.ProductID); err != nil {
						m.setError(err)
					} else if navigationIdentity(m.core.Snapshot()) != previousPosition {
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
					handoff := launcher.SessionHandoff{ProductID: candidate.ProductID, WorkID: candidate.WorkID, Worktree: candidate.Worktree, WorkflowStep: candidate.WorkflowStep, Posture: launcher.OperatorPosture(candidate.WorkflowStep), Prompt: launcher.OperatorPrompt(candidate.WorkID, candidate.WorkflowStep, "")}
					m.core.RestoreSnapshot(launcher.Snapshot{AmbientProduct: candidate.ProductID, SelectedWorkID: candidate.WorkID, Session: handoff, Coverage: "authoritative", Section: launcher.SectionRanked})
					return m, m.launch(m.core.Handoff())
				}
				if candidate.Kind == launcher.CandidateProject {
					m.core.RestoreSnapshot(launcher.Snapshot{Session: launcher.SessionHandoff{ProjectPath: candidate.Path}, Coverage: "authoritative"})
					return m, m.launch(m.core.Handoff())
				}
			}
			rows := m.filteredRows()
			if len(rows) > 0 && m.cursor < len(rows) {
				previousPosition := navigationIdentity(m.core.Snapshot())
				if err := m.core.SelectProduct(m.ctx, rows[m.cursor].ID); err != nil {
					m.setError(err)
				} else if navigationIdentity(m.core.Snapshot()) != previousPosition {
					m.navigation = append(m.navigation, navigationPosition{cursor: m.cursor, scroll: m.scroll})
				}
				m.filterValue = ""
				m.input.Reset()
				m.Sync()
			}
		} else if m.core.Snapshot().AmbientProduct != "" && m.core.Section() == launcher.SectionRanked {
			rows := m.filteredRanked()
			if len(rows) > 0 && m.cursor < len(rows) {
				previousPosition := navigationIdentity(m.core.Snapshot())
				selectionErr := m.core.SelectWork(m.ctx, rows[m.cursor].ID)
				if selectionErr != nil {
					m.setError(selectionErr)
				} else if navigationIdentity(m.core.Snapshot()) != previousPosition {
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
		if m.core.Snapshot().AmbientProduct != "" || m.core.Snapshot().SelectedWorkID != "" {
			m.back()
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

// navigationIdentity names which item the launcher is pointed at. The
// navigation stack compares it to see whether a selection moved. It carries
// data the snapshot already holds and decides no layout.
func navigationIdentity(snapshot launcher.Snapshot) [2]string {
	return [2]string{snapshot.AmbientProduct, snapshot.SelectedWorkID}
}

func (m *Model) back() {
	before := navigationIdentity(m.core.Snapshot())
	if err := m.core.Back(); err != nil {
		m.setError(err)
	}
	after := navigationIdentity(m.core.Snapshot())
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
	if s.AmbientProduct != "" && s.SelectedWorkID == "" {
		if m.core.PanelFocus() == launcher.S2PanelDomain {
			return len(s.Domains.Domains)
		}
		return len(m.filteredRanked())
	}
	if s.SelectedWorkID != "" {
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
	m.keys.Search.SetEnabled(snapshot.AmbientProduct != "")
	m.keys.Filter.SetEnabled(snapshot.SelectedWorkID == "")
	m.keys.Section.SetEnabled(snapshot.AmbientProduct != "")
	m.keys.Launch.SetEnabled(snapshot.AmbientProduct != "")

	// The header carries the C14 freshness meaning: which Product answered, at
	// which watermark, and how old that answer is.
	header := "CONCORD LAUNCHER"
	if snapshot.AmbientProduct != "" {
		header += " | PRODUCT: " + truncateDisplay(snapshot.AmbientProduct, 24)
	}
	header += " | WATERMARK: " + watermarkValue(snapshot.Watermark) + " | AGE: " + watermarkValue(snapshot.ObservedAt)
	status := "COVERAGE: " + coverageValue(snapshot.Coverage)
	// Coverage and reliance are C14 meaning and must stay on screen, so they
	// ride the status bar rather than a pane that can scroll them out of view.
	if snapshot.Reliance != "" {
		status += " | RELIANCE: " + snapshot.Reliance
	}
	// Coverage and reliance need more room than the focus label, so the status
	// bar gives the focus a third and the read state the rest.
	focusWidth := max(1, m.width/3)
	statusBar := lipgloss.JoinHorizontal(lipgloss.Top,
		fixedLine("FOCUS: "+focusText(snapshot), focusWidth),
		fixedLine(status, m.width-focusWidth),
	)
	footer := m.help.View(m.keys)
	if m.showHelp {
		footer = "HELP: " + footer
	}
	// A read that reports a condition gets a full-width line. A pane column is
	// too narrow to carry the sentence without breaking it.
	rows := []string{}
	chrome := 3
	if snapshot.StatusMessage != "" {
		rows = append(rows, fixedLine("STATUS: "+snapshot.StatusMessage, m.width))
		chrome = 4
	}
	body := []string{fixedLine(header, m.width), statusBar}
	body = append(body, rows...)
	body = append(body, m.paneLayout(snapshot, m.width, max(1, m.height-chrome)), fixedLine(footer, m.width))
	return fixedFrame(lipgloss.JoinVertical(lipgloss.Left, body...), m.width, m.height)
}

// paneLayout draws the three launcher panes. Each pane owns one data role.
// The work pane lists what the operator can select, the context pane explains
// the Product that work sits in, and the preview pane details the selection.
// The layout is unconditional and every pane states its own empty case, so a
// pane never disappears and no pane repeats another pane's content.
func (m *Model) paneLayout(snapshot launcher.Snapshot, width, height int) string {
	return fixedPanes([]string{
		strings.Join(m.workPaneLines(snapshot), "\n"),
		strings.Join(m.contextPaneLines(snapshot), "\n"),
		strings.Join(m.previewPaneLines(snapshot), "\n"),
	}, width, height)
}

// workPaneLines lists the selectable items. Work candidates, ranked Product
// work, and portfolio rows are three answers to one question, so each appears
// when its read supplied it.
func (m *Model) workPaneLines(snapshot launcher.Snapshot) []string {
	head := []string{"WORK"}
	if m.filterMode {
		head = append(head, m.input.View())
	} else if m.filterValue != "" {
		head = append(head, "FILTERED: "+m.filterValue)
	}
	body := []string{}
	for _, candidate := range launcher.FilterCandidates(snapshot.Candidates, m.filterValue) {
		if candidate.Kind != launcher.CandidateWork {
			continue
		}
		body = append(body, candidate.ID+" "+candidate.Name+" "+candidateLiveness(candidate))
	}
	// Present ranked work always lists. The read marker only decides whether an
	// empty list is an authoritative answer or absent data.
	if snapshot.RankedWorkRead || len(snapshot.Ranked) > 0 {
		body = append(body, rankedLines(filterRanked(snapshot.Ranked, m.filterValue), snapshot)...)
	}
	for _, row := range m.filteredRows() {
		body = append(body, row.Name+row.NameSuffix+" | "+row.Stage+" | "+relianceText(row)+" | "+actionText(row)+" | "+row.Focus)
	}
	if len(body) == 0 {
		body = append(body, workEmptyText(snapshot))
	}
	return append(head, body...)
}

// workEmptyText names why the work pane is empty, because a blank pane and an
// authoritative-empty answer are different facts.
func workEmptyText(snapshot launcher.Snapshot) string {
	if snapshot.FirstRun {
		return "FIRST RUN: no database; initialize through the operator setup"
	}
	if snapshot.Coverage != "" && snapshot.Coverage != "authoritative" {
		return "unavailable: " + coverageValue(snapshot.Coverage)
	}
	return "authoritative-empty"
}

// contextPaneLines explains the Product the work sits in.
func (m *Model) contextPaneLines(snapshot launcher.Snapshot) []string {
	body := []string{}
	if snapshot.AmbientProduct != "" {
		body = append(body, "PRODUCT: "+snapshot.AmbientProduct)
	}
	for _, candidate := range launcher.FilterCandidates(snapshot.Candidates, m.filterValue) {
		if candidate.Kind == launcher.CandidateProduct {
			body = append(body, "PRODUCT ITEM: "+candidate.ID+" "+candidate.Name)
		}
	}
	// The CD-0041 answer stack explains the Product: which domain it sits in,
	// what blocks it, and what comes next. The work pane already lists the
	// ranked work, so the blocked and next panels stay summaries here.
	if hasAnswerStack(snapshot) {
		stack := snapshot.S2AnswerStack()
		for _, panel := range stack.Panels {
			expanded := panel == launcher.S2PanelDomain && focusedPanel(snapshot) == launcher.S2PanelDomain
			body = append(body, s2PanelLines(panel, expanded, stack, snapshot, filterRanked(snapshot.Ranked, m.filterValue))...)
		}
	}
	if snapshot.Knowledge.Read {
		body = append(body, knowledgeLines(snapshot.Knowledge)...)
	}
	if len(snapshot.Relations.Edges) > 0 || snapshot.Relations.Unavailable != "" {
		body = append(body, relationLines(snapshot.Relations)...)
	}
	if len(snapshot.Probes) > 0 {
		body = append(body, "PROBES")
		body = append(body, probeLines(snapshot.Probes)...)
	}
	if len(body) == 0 {
		body = append(body, "no Product selected")
	}
	return append([]string{"CONTEXT"}, body...)
}

// previewPaneLines details the selected item alone. The header already carries
// the Product metadata and the footer already carries the keys, so neither is
// repeated here.
func (m *Model) previewPaneLines(snapshot launcher.Snapshot) []string {
	body := []string{}
	values := launcher.FilterCandidates(snapshot.Candidates, m.filterValue)
	if len(values) > 0 && m.cursor >= 0 && m.cursor < len(values) {
		body = append(body, candidatePreviewLines(values[m.cursor])...)
	}
	if snapshot.Detail.Item.ID != "" {
		body = append(body, workDetailLines(snapshot)...)
	}
	if snapshot.QueryResult {
		body = append(body, "QUERY RESULT: "+snapshot.QuerySubmitted+" (Esc restores prior view)")
		for _, item := range snapshot.Ranked {
			body = append(body, "WORK MATCH: "+item.ID+" "+item.Title+" lifecycle="+item.Lifecycle)
		}
		body = append(body, "KNOWLEDGE WATERMARK: "+snapshot.Knowledge.Watermark+" STATE: "+snapshot.Knowledge.State)
		body = append(body, knowledgeLines(snapshot.Knowledge)...)
	}
	if m.showHelp {
		body = append(body, "HELP: "+m.help.View(m.keys))
	}
	if len(body) == 0 {
		body = append(body, "no selection")
	}
	return append([]string{"PREVIEW"}, body...)
}

// workDetailLines describes one work item and the section the operator opened.
func workDetailLines(s launcher.Snapshot) []string {
	d := s.Detail
	urgency := d.Item.Urgency
	if urgency == "" {
		urgency = "standard"
	}
	lines := []string{
		"WORK: " + d.Item.ID + " " + d.Item.Title,
		"LIFECYCLE: " + d.Item.Lifecycle + " PRIORITY: " + fmtInt64(d.Item.Priority) + " URGENCY: " + urgency,
		"SECTION: " + string(s.Section),
		"PROJECTS: " + strings.Join(d.Projects, ", "),
		"WORKFLOW: " + d.Workflow,
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
		lines = append(lines, knowledgeLines(d.Knowledge)...)
	case launcher.SectionRelations:
		for _, e := range d.Edges {
			lines = append(lines, "EDGE "+e.Kind+": "+e.Source+" -> "+e.Target)
		}
	case launcher.SectionRanked:
		for _, h := range d.History {
			lines = append(lines, "HISTORY: "+h)
		}
	}
	return lines
}

// truncateDisplay caps a header field so a long value cannot push the fields
// after it off the line.
func truncateDisplay(value string, limit int) string {
	parts := splitDisplay(value, limit)
	if len(parts) == 0 {
		return value
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return parts[0] + "…"
}

// watermarkValue names a freshness field, or states that it is unknown.
func watermarkValue(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// hasAnswerStack reports whether a read supplied the Product answer stack.
func hasAnswerStack(snapshot launcher.Snapshot) bool {
	return snapshot.RankedWorkRead || snapshot.Domains.Read
}

// focusedPanel names the answer-stack panel that holds focus, defaulting to
// the domain panel when no read has set one.
func focusedPanel(snapshot launcher.Snapshot) launcher.S2Panel {
	if snapshot.PanelFocus == "" {
		return launcher.S2PanelDomain
	}
	return snapshot.PanelFocus
}

// candidateLiveness states whether a candidate has a live session.
func candidateLiveness(candidate launcher.Candidate) string {
	if candidate.SessionState != "" {
		return candidate.SessionState
	}
	if candidate.Live > 0 {
		return "live"
	}
	return "idle"
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
	cmd, err := tabSessionProcess(handoff)
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

func tabSessionProcess(handoff launcher.SessionHandoff) (*exec.Cmd, error) {
	if handoff.WorkID == "" && handoff.ProjectPath == "" {
		return sessionProcess(handoff)
	}
	hostTool := "ze" + "llij"
	if _, err := executableLookup(hostTool); err != nil {
		return nil, fmt.Errorf("cannot identify the host tab manager: %w", err)
	}
	name := handoff.WorkID
	if name == "" {
		name = handoff.ProductID
	}
	if name == "" {
		name = filepath.Base(handoff.ProjectPath)
	}
	if handoff.Posture != "" {
		name += " [" + handoff.Posture + "]"
	}
	query := exec.Command(hostTool, "action", "query-tab-names") //nolint:gosec // executable and arguments are fixed.
	output, err := query.Output()
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			if strings.TrimSpace(line) == name {
				return exec.Command(hostTool, "action", "go-to-tab-name", name), nil //nolint:gosec // executable and arguments are fixed.
			}
		}
	}
	bootstrap, err := sessionProcess(handoff)
	if err != nil {
		return nil, err
	}
	args := []string{"action", "new-tab", "--name", name}
	if handoff.Worktree != "" {
		args = append(args, "--cwd", handoff.Worktree)
	} else if handoff.ProjectPath != "" {
		args = append(args, "--cwd", handoff.ProjectPath)
	}
	args = append(args, "--")
	args = append(args, bootstrap.Args...)
	cmd := exec.Command(hostTool, args...) //nolint:gosec // executable and arguments are fixed or identity values.
	cmd.Env = bootstrap.Env
	return cmd, nil
}

var executableLookup = exec.LookPath

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
	env := make([]string, 0, len(os.Environ())+7)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "CONCORD_SELECTED_PRODUCT_ID=") || strings.HasPrefix(value, "CONCORD_SELECTED_WORK_ID=") || strings.HasPrefix(value, "CONCORD_SELECTED_PROMPT=") || strings.HasPrefix(value, "CONCORD_SELECTED_PROJECT_PATH=") || strings.HasPrefix(value, "CONCORD_SELECTED_WORKTREE=") || strings.HasPrefix(value, "CONCORD_SELECTED_WORKFLOW_STEP=") || strings.HasPrefix(value, "CONCORD_SELECTED_POSTURE=") {
			continue
		}
		env = append(env, value)
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
	if handoff.Worktree != "" {
		env = append(env, "CONCORD_SELECTED_WORKTREE="+handoff.Worktree)
	}
	if handoff.WorkflowStep != "" {
		env = append(env, "CONCORD_SELECTED_WORKFLOW_STEP="+handoff.WorkflowStep)
	}
	if handoff.Posture != "" {
		env = append(env, "CONCORD_SELECTED_POSTURE="+handoff.Posture)
	}
	return env
}

func columnWidths(width int) []int {
	if width < 80 {
		width = 80
	}
	return []int{18, 14, 18, 12, width - 18 - 14 - 18 - 12}
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

func candidateLine(index int, candidate launcher.Candidate) string {
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
	liveness := candidate.SessionState
	if liveness == "" {
		liveness = "idle"
		if candidate.Live > 0 {
			liveness = "live"
		}
	}
	step := ""
	if candidate.WorkflowStep != "" {
		step = " step=" + candidate.WorkflowStep
	}
	return fmtInt(index) + " " + marker + " " + string(candidate.Kind) + " " + name + " state=" + state + blocked + " session=" + liveness + step
}

func filterRanked(values []launcher.RankedWork, query string) []launcher.RankedWork {
	needle := strings.ToLower(query)
	if needle == "" {
		return values
	}
	out := make([]launcher.RankedWork, 0, len(values))
	for _, value := range values {
		if strings.Contains(strings.ToLower(value.ID+" "+value.Title+" "+value.Kind+" "+value.Lifecycle), needle) {
			out = append(out, value)
		}
	}
	return out
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

func pane(content string, width, height int) string {
	if width < 3 || height < 3 {
		return fixedBlock(content, width, height)
	}
	innerWidth := width - 2
	lines := strings.Split(content, "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := wrapPaneLine(line, innerWidth)
		if len(parts) == 0 {
			parts = []string{""}
		}
		wrapped = append(wrapped, parts...)
	}
	if len(wrapped) > height-2 {
		wrapped = wrapped[:height-2]
	}
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(lipgloss.RoundedBorder()).
		Render(strings.Join(wrapped, "\n"))
}

func fixedPanes(contents []string, width, height int) string {
	width = max(3, width)
	height = max(3, height)
	if len(contents) == 0 {
		return fixedBlock("", width, height)
	}
	gap := len(contents) - 1
	usable := max(len(contents), width-gap)
	base := usable / len(contents)
	widths := make([]int, len(contents))
	for i := range widths {
		widths[i] = base
		if i < usable%len(contents) {
			widths[i]++
		}
	}
	panes := make([]string, len(contents))
	for i, content := range contents {
		panes[i] = pane(content, widths[i], height)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panes...)
}

func wrapPaneLine(line string, width int) []string {
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(line) {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if current != "" && lipgloss.Width(candidate) > width {
			lines = append(lines, current)
			current = word
			continue
		}
		if current == "" && lipgloss.Width(candidate) > width {
			parts := splitDisplay(candidate, width)
			if len(parts) > 1 {
				lines = append(lines, parts[:len(parts)-1]...)
				current = parts[len(parts)-1]
				continue
			}
		}
		current = candidate
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func fixedBlock(content string, width, height int) string {
	width = max(1, width)
	height = max(1, height)
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = fixedLine(lines[i], width)
	}
	return strings.Join(lines, "\n")
}

func fixedLine(value string, width int) string {
	width = max(1, width)
	parts := splitDisplay(value, width)
	if len(parts) == 0 {
		return strings.Repeat(" ", width)
	}
	line := parts[0]
	if displayWidth := lipgloss.Width(line); displayWidth < width {
		line += strings.Repeat(" ", width-displayWidth)
	}
	return line
}

func fixedFrame(content string, width, height int) string {
	width = max(1, width)
	height = max(1, height)
	return fixedBlock(content, width, height)
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
	liveness := "idle"
	if candidate.Live > 0 {
		liveness = "live"
	}
	if candidate.SessionState != "" {
		liveness = candidate.SessionState
	}
	lines = append(lines, "STATE: "+state, "BLOCKED: "+fmtBool(candidate.Blocked), "SESSION: "+liveness, "LIVE SESSIONS: "+fmtInt(candidate.Live), "WORKFLOW STEP: "+candidate.WorkflowStep)
	return lines
}

func fmtBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
