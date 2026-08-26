package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/launcher/render/bubbletea"
	"github.com/sharper-flow/concord/internal/launcher/storeport"
	"github.com/sharper-flow/concord/internal/portfolio"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

type launcherPortfolioCorpus struct {
	SchemaVersion string                  `json:"schema_version"`
	Contract      string                  `json:"contract"`
	Operation     string                  `json:"operation"`
	Cases         []launcherPortfolioCase `json:"cases"`
}

type launcherPortfolioCase struct {
	ID         string   `json:"id"`
	Rows       []string `json:"rows"`
	Events     []string `json:"events"`
	Database   string   `json:"database"`
	Assertions []string `json:"assertions"`
}

type launcherPortfolioObservation struct {
	Rows             map[string]store.ProductRow
	Coverage         map[string]store.ProductRow
	SessionReads     []launcher.ReadRequest
	SessionScreen    launcher.Screen
	SessionReadsWant int
	DurableBefore    map[string]int
	DurableAfter     map[string]int
	FirstRun         launcher.Snapshot
	FirstRunOutput   string
	AuthorityPath    string
}

type launcherPortfolioBinding func(*testing.T, launcherPortfolioCase) launcherPortfolioObservation

type trackingReadPort struct {
	delegate *storeport.Port
	requests []launcher.ReadRequest
}

func (p *trackingReadPort) Read(ctx context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	p.requests = append(p.requests, request)
	return p.delegate.Read(ctx, request)
}

var launcherPortfolioBindings = map[string]launcherPortfolioBinding{
	"active-quiet-duplicate": bindActiveQuietDuplicate,
	"focus-priority":         bindFocusPriority,
	"coverage-states":        bindCoverageStates,
	"launcher-session":       bindLauncherSession,
	"first-run":              bindFirstRun,
}

func TestLauncherPortfolioCorpus(t *testing.T) {
	corpus := loadLauncherPortfolioCorpus(t)
	if corpus.SchemaVersion != "1.0" || corpus.Contract != "C14/C18" || corpus.Operation != "concord_product_view.portfolio" {
		t.Fatalf("corpus header = %#v", corpus)
	}
	if len(corpus.Cases) != 5 {
		t.Fatalf("corpus cases = %d, want 5", len(corpus.Cases))
	}
	seen := map[string]bool{}
	for _, scenario := range corpus.Cases {
		if seen[scenario.ID] {
			t.Fatalf("duplicate scenario %q", scenario.ID)
		}
		seen[scenario.ID] = true
		binding, ok := launcherPortfolioBindings[scenario.ID]
		if !ok {
			t.Fatalf("scenario %q has no production binding", scenario.ID)
		}
		t.Run(scenario.ID, func(t *testing.T) {
			observation := binding(t, scenario)
			for _, assertion := range scenario.Assertions {
				if err := evaluateLauncherAssertion(observation, scenario, assertion); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for id := range launcherPortfolioBindings {
		if !seen[id] {
			t.Fatalf("binding %q is not declared by the corpus", id)
		}
	}
}

func TestLauncherPortfolioCorpusMutationFails(t *testing.T) {
	corpus := loadLauncherPortfolioCorpus(t)
	var scenario launcherPortfolioCase
	for _, candidate := range corpus.Cases {
		if candidate.ID == "active-quiet-duplicate" {
			scenario = candidate
			break
		}
	}
	if scenario.ID == "" {
		t.Fatal("active-quiet-duplicate is missing")
	}
	observation := bindActiveQuietDuplicate(t, scenario)

	// Structural mutation: an unsupported assertion name. The harness must
	// refuse a key it does not recognise rather than silently evaluating
	// nothing — proving the corpus asserts something the runner knows.
	structural := scenario
	structural.Assertions = append([]string(nil), scenario.Assertions...)
	structural.Assertions[0] = "mutated_c14_groups"
	if err := evaluateLauncherAssertion(observation, structural, structural.Assertions[0]); err == nil {
		t.Fatal("unsupported launcher assertion was accepted")
	} else if !strings.Contains(err.Error(), "unsupported launcher assertion") {
		t.Fatalf("structural mutation failed for the wrong reason: %v", err)
	}

	// Value-based mutation: substitute stable_name_suffix with a known
	// assertion whose required shape contradicts the seeded fixture. The
	// active-quiet-duplicate observation has no focus-competition row, so
	// approval_required_wins must fail with its own typed error — proving
	// the runner executes the assertion against the production observation
	// rather than passing on a renamed-but-unevaluated key.
	valueMutation := scenario
	valueMutation.Assertions = append([]string(nil), scenario.Assertions...)
	valueMutation.Assertions[1] = "approval_required_wins"
	if err := evaluateLauncherAssertion(observation, valueMutation, valueMutation.Assertions[1]); err == nil {
		t.Fatal("value-mutated launcher assertion was accepted")
	} else if !strings.Contains(err.Error(), "approval-required focus did not win") {
		t.Fatalf("value mutation failed for the wrong reason: %v", err)
	}
}

func loadLauncherPortfolioCorpus(t *testing.T) launcherPortfolioCorpus {
	t.Helper()
	data, err := os.ReadFile("../../scenarios/launcher-portfolio.v1.json")
	if err != nil {
		t.Fatalf("load launcher portfolio corpus: %v", err)
	}
	var corpus launcherPortfolioCorpus
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatalf("decode launcher portfolio corpus: %v", err)
	}
	return corpus
}

func evaluateLauncherAssertion(observation launcherPortfolioObservation, scenario launcherPortfolioCase, assertion string) error {
	switch assertion {
	case "five_c14_groups":
		if len(observation.Rows) != 4 {
			return fmt.Errorf("%s: rows = %d, want 4", scenario.ID, len(observation.Rows))
		}
		for _, id := range scenario.Rows {
			row, ok := observation.Rows[id]
			if !ok {
				return fmt.Errorf("%s: row %q is missing", scenario.ID, id)
			}
			if row.ProductID == "" || row.DisplayName == "" || row.Stage.Maturity == "" || row.Stage.AudienceCommitment == "" {
				return fmt.Errorf("%s: row %q lacks identity or stage", scenario.ID, id)
			}
			if row.Reliance.Authority == "" || row.Reliance.ObservedAt == "" {
				return fmt.Errorf("%s: row %q lacks reliance metadata", scenario.ID, id)
			}
			if row.ActionCounts.State != store.ProductRowCountsKnown || row.ActionCounts.Values == nil {
				return fmt.Errorf("%s: row %q lacks the action-count group", scenario.ID, id)
			}
			if row.Focus == nil && row.FocusAbsentReason == "" {
				return fmt.Errorf("%s: row %q lacks the focus group", scenario.ID, id)
			}
		}
		return nil
	case "stable_name_suffix":
		for _, id := range []string{"duplicate-a", "duplicate-b"} {
			row := observation.Rows[id]
			if row.DisplayNameSuffix == "" || !strings.Contains(row.DisplayNameSuffix, row.ProductID) {
				return fmt.Errorf("%s: duplicate %q has unstable suffix %#v", scenario.ID, id, row.DisplayNameSuffix)
			}
		}
		return nil
	case "approval_required_wins":
		row, ok := observation.Rows["focus-competition"]
		if !ok || row.Focus == nil || row.Focus.AttentionKind != store.ProductRowAttentionApprovalRequired {
			return fmt.Errorf("%s: approval-required focus did not win: %#v", scenario.ID, row.Focus)
		}
		return nil
	case "deterministic_priority_then_time_then_id":
		want := map[string]string{
			"focus-competition": "approval-low-priority",
			"active-problem":    "problem-low-priority",
			"blocked":           "blocked-low-priority",
			"in-progress":       "progress-old",
			"ready":             "ready-a",
		}
		for id, workID := range want {
			row, ok := observation.Rows[id]
			if !ok || row.Focus == nil || row.Focus.WorkID != workID {
				return fmt.Errorf("%s: %s focus = %#v, want %q", scenario.ID, id, row.Focus, workID)
			}
		}
		return nil
	case "unavailable_counts_are_not_zero":
		for id, row := range observation.Coverage {
			if row.ActionCounts.State != store.ProductRowCountsUnavailable || row.ActionCounts.Values != nil || row.ActionCounts.Unavailable == nil {
				return fmt.Errorf("%s: %s counts were not typed unavailable: %#v", scenario.ID, id, row.ActionCounts)
			}
		}
		return nil
	case "typed_focus_absence":
		want := map[string]string{"degraded": store.ProductRowFocusUnreachable, "unreachable": store.ProductRowFocusUnreachable, "stale-blocked": store.ProductRowFocusStaleBlock}
		for id, reason := range want {
			row, ok := observation.Coverage[id]
			if !ok || row.Focus != nil || row.FocusAbsentReason != reason {
				return fmt.Errorf("%s: %s focus absence = %#v, want %q", scenario.ID, id, row.FocusAbsentReason, reason)
			}
		}
		return nil
	case "only_entry_and_refresh_read":
		if observation.SessionReadsWant != 2 || len(observation.SessionReads) != 2 {
			return fmt.Errorf("%s: reads = %d, want entry and refresh only", scenario.ID, len(observation.SessionReads))
		}
		for _, request := range observation.SessionReads {
			if request.Kind != launcher.ReadPortfolio {
				return fmt.Errorf("%s: unexpected read kind %q", scenario.ID, request.Kind)
			}
		}
		return nil
	case "no_durable_effects":
		if fmt.Sprint(observation.DurableBefore) != fmt.Sprint(observation.DurableAfter) {
			return fmt.Errorf("%s: durable state changed: before=%v after=%v", scenario.ID, observation.DurableBefore, observation.DurableAfter)
		}
		return nil
	case "s2_not_implemented":
		if observation.SessionScreen != launcher.ScreenPortfolio {
			return fmt.Errorf("%s: session entered an S2 screen: %s", scenario.ID, observation.SessionScreen)
		}
		return nil
	case "typed_first_run":
		if !observation.FirstRun.FirstRun || observation.FirstRun.Coverage != "first_run" || observation.FirstRun.StatusMessage == "" || !strings.Contains(observation.FirstRunOutput, "FIRST RUN:") {
			return fmt.Errorf("%s: first-run observation = %#v output=%q", scenario.ID, observation.FirstRun, observation.FirstRunOutput)
		}
		return nil
	case "filesystem_unchanged":
		if _, err := os.Stat(observation.AuthorityPath); !os.IsNotExist(err) {
			return fmt.Errorf("%s: authority path changed: stat=%v", scenario.ID, err)
		}
		if _, err := os.Stat(filepath.Dir(observation.AuthorityPath)); !os.IsNotExist(err) {
			return fmt.Errorf("%s: authority parent changed: stat=%v", scenario.ID, err)
		}
		return nil
	default:
		return fmt.Errorf("%s: unsupported launcher assertion %q", scenario.ID, assertion)
	}
}

func bindActiveQuietDuplicate(t *testing.T, _ launcherPortfolioCase) launcherPortfolioObservation {
	s := openLauncherCorpusStore(t)
	for _, product := range []struct{ id, name string }{
		{"active", "Active"}, {"quiet", "Quiet"}, {"duplicate-a", "Duplicate"}, {"duplicate-b", "Duplicate"},
	} {
		seedLauncherCorpusProduct(t, s, product.id, product.name)
	}
	seedLauncherCorpusWork(t, s, "active-work", "active", "task", "Active work", "in_progress", 1, "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z")
	rows := readLauncherCorpusRows(t, s)
	return launcherPortfolioObservation{Rows: rows}
}

func bindFocusPriority(t *testing.T, _ launcherPortfolioCase) launcherPortfolioObservation {
	s := openLauncherCorpusStore(t)
	seedFocusProduct(t, s, "focus-competition", "Focus competition")
	seedFocusProduct(t, s, "active-problem", "Active problem")
	seedFocusProduct(t, s, "blocked", "Blocked")
	seedFocusProduct(t, s, "in-progress", "In progress")
	seedFocusProduct(t, s, "ready", "Ready")

	seedLauncherCorpusWork(t, s, "approval-high-priority", "focus-competition", "task", "Approval high", "needed", 99, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedLauncherCorpusWork(t, s, "approval-low-priority", "focus-competition", "task", "Approval low", "needed", 1, "2026-08-02T00:00:00Z", "2026-08-02T00:00:00Z")
	seedApprovalWorkflow(t, s, "approval-high-priority")
	seedApprovalWorkflow(t, s, "approval-low-priority")
	seedLauncherCorpusWork(t, s, "competition-problem", "focus-competition", "bug", "Problem", "needed", 100, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedLauncherCorpusWork(t, s, "competition-blocked", "focus-competition", "task", "Blocked", "needed", 100, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedLauncherCorpusWork(t, s, "competition-progress", "focus-competition", "task", "Progress", "in_progress", 100, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedLauncherCorpusWork(t, s, "competition-ready", "focus-competition", "task", "Ready", "needed", 100, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")

	seedTieWork(t, s, "active-problem", "problem-high-priority", "problem-low-priority", "bug", "needed", 2, 1, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedTieWork(t, s, "blocked", "blocked-high-priority", "blocked-low-priority", "task", "needed", 2, 1, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedTieWork(t, s, "in-progress", "progress-new", "progress-old", "task", "in_progress", 1, 1, "2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z")
	seedTieWork(t, s, "ready", "ready-b", "ready-a", "task", "needed", 1, 1, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedOutsideProject(t, s)
	seedLauncherCorpusWork(t, s, "blocked-low-blocker", "outside", "task", "Blocker", "in_progress", 1, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	seedLauncherCorpusWork(t, s, "blocked-high-blocker", "outside", "task", "Blocker", "in_progress", 1, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	addBlockingRelation(t, s, "blocked-low-priority", "blocked-low-blocker")
	addBlockingRelation(t, s, "blocked-high-priority", "blocked-high-blocker")

	rows := readLauncherCorpusRows(t, s)
	return launcherPortfolioObservation{Rows: rows}
}

func bindCoverageStates(t *testing.T, _ launcherPortfolioCase) launcherPortfolioObservation {
	s := openLauncherCorpusStore(t)
	seedLauncherCorpusProduct(t, s, "coverage", "Coverage")
	inputs := map[string]*store.ProductRowRelianceInput{
		"degraded":      {Authority: store.ProductRowAuthorityDegraded, Reason: "source_lag", Omissions: []string{"work_snapshot"}},
		"unreachable":   {Authority: store.ProductRowAuthorityUnreachable, Reason: "authority_unreachable", Omissions: []string{"work_snapshot"}},
		"stale-blocked": {Authority: store.ProductRowAuthorityAuthoritative, Stale: true, BlocksExecution: true, Reason: "source_stale", Omissions: []string{"work_snapshot"}},
	}
	rows := map[string]store.ProductRow{}
	for id, input := range inputs {
		result, err := portfolio.Read(context.Background(), s, store.ProductRowRequest{Product: "coverage", Limit: 20, Source: input})
		if err != nil {
			t.Fatalf("read %s coverage: %v", id, err)
		}
		if len(result.Rows) != 1 {
			t.Fatalf("read %s rows = %d, want 1", id, len(result.Rows))
		}
		rows[id] = result.Rows[0]
	}
	return launcherPortfolioObservation{Coverage: rows}
}

func bindLauncherSession(t *testing.T, scenario launcherPortfolioCase) launcherPortfolioObservation {
	dir := t.TempDir()
	s, err := storetest.Open(dir)
	if err != nil {
		t.Fatalf("open launcher session store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	before := launcherDurableCounts(t, s)
	port := &trackingReadPort{delegate: storeport.New(s)}
	core := launcher.New(port)
	ui := bubbletea.New(core, context.Background(), bubbletea.Profile{})
	for _, event := range scenario.Events {
		switch event {
		case "s1_entry":
			if err := core.Enter(context.Background()); err != nil {
				t.Fatalf("enter launcher session: %v", err)
			}
		case "move":
			ui.UpdateKey("down")
		case "scroll":
			ui.UpdateKey("ctrl+d")
		case "filter_edit":
			ui.UpdateKey("/")
			ui.UpdateKey("x")
		case "filter_paste":
			ui.Update(tea.PasteMsg{Content: " corpus"})
		case "filter_clear":
			ui.UpdateKey("ctrl+l")
			ui.UpdateKey("enter")
		case "help":
			ui.UpdateKey("?")
		case "refresh":
			ui.UpdateKey("r")
		case "select":
			ui.UpdateKey("enter")
		case "back":
			ui.UpdateKey("esc")
		case "quit":
			if cmd := ui.UpdateKey("q"); cmd == nil {
				t.Fatal("quit event did not return a command")
			}
		default:
			t.Fatalf("unsupported launcher event %q", event)
		}
	}
	after := launcherDurableCounts(t, s)
	return launcherPortfolioObservation{SessionReads: append([]launcher.ReadRequest(nil), port.requests...), SessionScreen: core.Snapshot().Screen, SessionReadsWant: 2, DurableBefore: before, DurableAfter: after}
}

func bindFirstRun(t *testing.T, _ launcherPortfolioCase) launcherPortfolioObservation {
	dbPath := filepath.Join(t.TempDir(), "nested", "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut bytes.Buffer
	if code := runLauncherCommand(nil, strings.NewReader("q"), &out, &errOut, true); code != 0 {
		t.Fatalf("first-run launcher exit code = %d, stderr=%q", code, errOut.String())
	}
	firstRun, err := firstRunPort{}.Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadPortfolio})
	if err != nil {
		t.Fatalf("read first-run state: %v", err)
	}
	core := launcher.New(firstRunPort{})
	if err := core.Enter(context.Background()); err != nil {
		t.Fatalf("enter first-run renderer: %v", err)
	}
	rendered := bubbletea.New(core, context.Background(), bubbletea.Profile{}).Render()
	return launcherPortfolioObservation{FirstRun: firstRun, FirstRunOutput: rendered + out.String(), AuthorityPath: dbPath}
}

func openLauncherCorpusStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open launcher corpus store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func readLauncherCorpusRows(t *testing.T, s *store.Store) map[string]store.ProductRow {
	t.Helper()
	result, err := portfolio.Read(context.Background(), s, store.ProductRowRequest{Limit: 100})
	if err != nil {
		t.Fatalf("read Product rows through portfolio.Read: %v", err)
	}
	rows := make(map[string]store.ProductRow, len(result.Rows))
	for _, row := range result.Rows {
		rows[row.ProductID] = row
	}
	return rows
}

func seedLauncherCorpusProduct(t *testing.T, s *store.Store, id, name string) {
	t.Helper()
	projectID := id + "-project"
	corpusExec(t, s, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES (?,?,'prototype','operator_only',1,?,?)`, id, name, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	corpusExec(t, s, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES (?,?,1,?,?)`, projectID, name+" project", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	corpusExec(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES (?,?,'primary')`, id, projectID)
}

func seedFocusProduct(t *testing.T, s *store.Store, id, name string) {
	seedLauncherCorpusProduct(t, s, id, name)
}

func seedLauncherCorpusWork(t *testing.T, s *store.Store, id, productID, kind, title, lifecycle string, priority int, createdAt, updatedAt string) {
	t.Helper()
	projectID := productID + "-project"
	corpusExec(t, s, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES (?,?,?,?,1,1,?,?,NULL)`, id, kind, title, lifecycle, priority, createdAt, updatedAt)
	corpusExec(t, s, `INSERT INTO work_projects(work_id,project_id,role) VALUES (?,?,'primary')`, id, projectID)
}

func seedTieWork(t *testing.T, s *store.Store, productID, firstID, secondID, kind, lifecycle string, firstPriority, secondPriority int, firstTime, secondTime string) {
	seedLauncherCorpusWork(t, s, firstID, productID, kind, firstID, lifecycle, firstPriority, firstTime, firstTime)
	seedLauncherCorpusWork(t, s, secondID, productID, kind, secondID, lifecycle, secondPriority, secondTime, secondTime)
}

func seedApprovalWorkflow(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatalf("load workflow definition: %v", err)
	}
	corpusExec(t, s, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,started_at) VALUES (?,?,?,?,?,?,?)`, workID, definition.Definition.Ref, definition.Definition.Version, definition.Digest, "planning", "ready", "2026-08-01T00:00:00Z")
}

func addBlockingRelation(t *testing.T, s *store.Store, workID, blockerID string) {
	corpusExec(t, s, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES (?,?, 'blocks',?)`, blockerID, workID, "2026-08-01T00:00:00Z")
}

func seedOutsideProject(t *testing.T, s *store.Store) {
	corpusExec(t, s, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('outside-project','Outside',1,?,?)`, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
}

func corpusExec(t *testing.T, s *store.Store, statement string, args ...any) {
	t.Helper()
	db := s.DatabaseForTesting()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatalf("enable corpus fixture fold guard: %v", err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("disable corpus fixture fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, statement, args...); err != nil {
		t.Fatalf("seed launcher corpus fixture: %v", err)
	}
}
