package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestProductRowsC14ReturnsFiveGroups(t *testing.T) {
	s := openTemp(t)
	seedProductRowFixture(t, s)

	result, err := s.QueryProductRows(context.Background(), ProductRowRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %#v, want one row", result.Rows)
	}
	row := result.Rows[0]
	if row.ProductID != "product-row" || row.DisplayName != "Portfolio" {
		t.Fatalf("identity = %#v", row)
	}
	if row.Stage.Maturity != "alpha" || row.Stage.AudienceCommitment != "limited" {
		t.Fatalf("stage = %#v", row.Stage)
	}
	if row.ActionCounts.State != ProductRowCountsKnown || row.ActionCounts.Values == nil {
		t.Fatalf("counts = %#v", row.ActionCounts)
	}
	if row.ActionCounts.Values.InProgress != 2 || row.ActionCounts.Values.Blocked != 1 || row.ActionCounts.Values.Ready != 2 || row.ActionCounts.Values.ActiveProblems != 1 || row.ActionCounts.Values.ApprovalRequired != 1 {
		t.Fatalf("counts = %#v", row.ActionCounts.Values)
	}
	if row.Focus == nil || row.Focus.WorkID != "approval-work" || row.Focus.AttentionKind != ProductRowAttentionApprovalRequired {
		t.Fatalf("focus = %#v", row.Focus)
	}
	if row.Focus.StageContext.Kind != "product_default" {
		t.Fatalf("inherited stage context = %#v", row.Focus.StageContext)
	}
	if row.Reliance.Authority != ProductRowAuthorityAuthoritative || result.ObservedAt == "" {
		t.Fatalf("reliance/meta = %#v %#v", row.Reliance, result.ResultMeta)
	}
}

func seedProductRowFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	defer s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`)
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES
		('product-row','Portfolio','alpha','limited',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('project-row','Project',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('product-row','project-row','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES
		('approval-work','task','Approve contract','needed',1,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z',NULL),
		('problem-work','problem','Fix problem','in_progress',2,1,'2026-08-02T00:00:00Z','2026-08-02T00:00:00Z',NULL),
		('blocked-work','task','Blocked task','needed',3,1,'2026-08-03T00:00:00Z','2026-08-03T00:00:00Z',NULL),
		('ready-work','task','Ready task','needed',4,1,'2026-08-04T00:00:00Z','2026-08-04T00:00:00Z',NULL),
		('blocker-work','task','Blocker','in_progress',5,1,'2026-08-05T00:00:00Z','2026-08-05T00:00:00Z',NULL);
		INSERT INTO work_projects(work_id,project_id,role) VALUES
		('approval-work','project-row','primary'),('problem-work','project-row','primary'),('blocked-work','project-row','primary'),('ready-work','project-row','primary'),('blocker-work','project-row','primary');
		INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES ('blocker-work','blocked-work','blocks','2026-08-05T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,started_at) VALUES (?,?,?,?,?,?,?)`, "approval-work", definition.Definition.Ref, definition.Definition.Version, definition.Digest, "planning", "ready", "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func productRowExec(t *testing.T, s *Store, statement string, args ...any) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	defer s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`)
	if _, err := s.DatabaseForTesting().ExecContext(ctx, statement, args...); err != nil {
		t.Fatal(err)
	}
}

func addProductRowProduct(t *testing.T, s *Store, id, name string) {
	productRowExec(t, s, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES (?,?, 'prototype','operator_only',1,?,?)`, id, name, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
}

func TestProductRowsC14AuthoritativeEmptyAndTerminalOnly(t *testing.T) {
	s := openTemp(t)
	addProductRowProduct(t, s, "empty", "Empty")
	result, err := s.QueryProductRows(context.Background(), ProductRowRequest{})
	if err != nil || result.Rows[0].FocusAbsentReason != ProductRowFocusAuthoritativeEmpty {
		t.Fatalf("empty result=%#v err=%v", result, err)
	}
	productRowExec(t, s, `
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('terminal-project','Terminal',1,?,?);
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('empty','terminal-project','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES ('terminal-work','task','Done','completed',1,1,?,?,?);
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES ('live-blocker','task','Live blocker','in_progress',1,1,?,?,NULL);
		INSERT INTO work_projects(work_id,project_id,role) VALUES ('terminal-work','terminal-project','primary');
		INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES ('live-blocker','terminal-work','blocks',?);
	`, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z")
	result, err = s.QueryProductRows(context.Background(), ProductRowRequest{Product: "empty"})
	if err != nil || result.Rows[0].FocusAbsentReason != ProductRowFocusNoActionableWork || result.Rows[0].Focus != nil || result.Rows[0].ActionCounts.Values == nil || result.Rows[0].ActionCounts.Values.Blocked != 0 {
		t.Fatalf("terminal-only result=%#v err=%v", result, err)
	}
}

func TestProductRowsC14TerminalWorkCannotEnterAnyFocusTier(t *testing.T) {
	for _, lifecycle := range []string{"completed", "cancelled", "superseded"} {
		work := productRowWork{Lifecycle: lifecycle, ApprovalRequired: true, ActiveProblem: true, Blocked: true, Ready: true}
		if got := work.attentionKind(); got != "" {
			t.Fatalf("terminal lifecycle %q entered focus tier %q", lifecycle, got)
		}
	}
}

func TestProductRowsC14FocusTiersStageContextAndCrossProjectDedupe(t *testing.T) {
	s := openTemp(t)
	seedProductRowFixture(t, s)
	productRowExec(t, s, `
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('project-row-2','Project 2',1,?,?);
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('project-row-3','Project 3',1,?,?);
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('product-row','project-row-2','secondary');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('product-row','project-row-3','secondary');
		INSERT INTO work_projects(work_id,project_id,role) VALUES ('approval-work','project-row-2','secondary');
		INSERT INTO work_projects(work_id,project_id,role) VALUES ('approval-work','project-row-3','secondary');
	`, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			projectStageChangedEvent("project-row-2", "project-row-2-stage", "beta", "public"),
			projectStageChangedEvent("project-row-3", "project-row-3-stage", "production", "public"),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-row-2"): 1, VersionRef(SubjectProject, "project-row-3"): 1},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryProductRows(context.Background(), ProductRowRequest{Product: "product-row"})
	if err != nil {
		t.Fatal(err)
	}
	row := result.Rows[0]
	if row.Focus == nil || row.Focus.AttentionKind != ProductRowAttentionApprovalRequired || row.Focus.ProjectCount != 3 {
		t.Fatalf("focus = %#v", row.Focus)
	}
	if row.Focus.StageContext.Kind != "mixed" || row.ActionCounts.Values.InProgress != 2 {
		t.Fatalf("stage/counts = %#v %#v", row.Focus.StageContext, row.ActionCounts.Values)
	}
	if row.ActionCounts.Values.Blocked != 1 || row.ActionCounts.Values.Ready != 2 {
		t.Fatalf("overlapping counts = %#v", row.ActionCounts.Values)
	}
}

func TestProductRowsC14SingleProjectStageOverride(t *testing.T) {
	s := openTemp(t)
	seedProductRowFixture(t, s)
	productRowExec(t, s, `
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('project-override','Override',1,?,?);
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('product-row','project-override','secondary');
		INSERT INTO work_projects(work_id,project_id,role) VALUES ('approval-work','project-override','secondary');
	`, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	if err := ApplyOperation(context.Background(), s, Operation{
		Events:           []Event{projectStageChangedEvent("project-override", "project-override-stage", "beta", "public")},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-override"): 1},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryProductRows(context.Background(), ProductRowRequest{Product: "product-row"})
	if err != nil {
		t.Fatal(err)
	}
	context := result.Rows[0].Focus.StageContext
	if context.Kind != "single_focus_override" || context.FocusOverride == nil || context.FocusOverride.Maturity != "beta" || context.FocusOverride.AudienceCommitment != "public" {
		t.Fatalf("stage context = %#v", context)
	}
}

func TestProductRowsC14FiveTierCompetitionChoosesFirstNonemptyTier(t *testing.T) {
	s := openTemp(t)
	productRowExec(t, s, `
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('tiers','Tiers','prototype','operator_only',1,?,?);
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('tiers-project','Tiers Project',1,?,?);
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('tiers','tiers-project','primary');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES
		('tier-approval','task','Approval','needed',99,1,?,?,NULL),
		('tier-problem','problem','Problem','in_progress',98,1,?,?,NULL),
		('tier-blocked','task','Blocked','needed',97,1,?,?,NULL),
		('tier-progress','task','Progress','in_progress',96,1,?,?,NULL),
		('tier-ready','task','Ready','needed',95,1,?,?,NULL),
		('tier-blocker','task','Blocker','in_progress',94,1,?,?,NULL);
		INSERT INTO work_projects(work_id,project_id,role) VALUES
		('tier-approval','tiers-project','primary'),('tier-problem','tiers-project','primary'),('tier-blocked','tiers-project','primary'),('tier-progress','tiers-project','primary'),('tier-ready','tiers-project','primary'),('tier-blocker','tiers-project','primary');
		INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES ('tier-blocker','tier-blocked','blocks',?);
	`, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	productRowExec(t, s, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,started_at) VALUES (?,?,?,?,?,?,?)`, "tier-approval", definition.Definition.Ref, definition.Definition.Version, definition.Digest, "planning", "ready", "2026-08-01T00:00:00Z")
	result, err := s.QueryProductRows(context.Background(), ProductRowRequest{Product: "tiers"})
	if err != nil || result.Rows[0].Focus == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Rows[0].Focus.WorkID != "tier-approval" || result.Rows[0].Focus.AttentionKind != ProductRowAttentionApprovalRequired {
		t.Fatalf("focus=%#v", result.Rows[0].Focus)
	}
}

func TestProductRowsC14UnavailableRequiredSourceNeverBecomesZero(t *testing.T) {
	s := openTemp(t)
	seedProductRowFixture(t, s)
	for _, tc := range []struct {
		name   string
		input  *ProductRowRelianceInput
		reason string
	}{
		{name: "stale", input: &ProductRowRelianceInput{Authority: ProductRowAuthorityAuthoritative, Stale: true, BlocksExecution: true, Reason: "source_stale", Omissions: []string{"work_snapshot"}}, reason: ProductRowFocusStaleBlock},
		{name: "degraded", input: &ProductRowRelianceInput{Authority: ProductRowAuthorityDegraded, Reason: "source_lag"}, reason: ProductRowFocusUnreachable},
		{name: "unreachable", input: &ProductRowRelianceInput{Authority: ProductRowAuthorityUnreachable, Reason: "authority_unreachable"}, reason: ProductRowFocusUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.QueryProductRows(context.Background(), ProductRowRequest{Product: "product-row", Source: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			row := result.Rows[0]
			if row.ActionCounts.State != ProductRowCountsUnavailable || row.ActionCounts.Values != nil || row.ActionCounts.Unavailable == nil {
				t.Fatalf("counts=%#v", row.ActionCounts)
			}
			if row.Focus != nil || row.FocusAbsentReason != tc.reason {
				t.Fatalf("focus=%#v reason=%q", row.Focus, row.FocusAbsentReason)
			}
			if tc.input.Authority != ProductRowAuthorityAuthoritative && result.Authority != tc.input.Authority {
				t.Fatalf("result authority=%q input=%q", result.Authority, tc.input.Authority)
			}
		})
	}
}

func TestProductRowsC14DuplicateNamesAndCursorBinding(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 25; i++ {
		name := "Product"
		if i == 24 {
			name = "Zulu"
		}
		addProductRowProduct(t, s, "product-"+fmtProductRowID(i), name)
	}
	first, err := s.QueryProductRows(context.Background(), ProductRowRequest{})
	if err != nil || len(first.Rows) != 20 || first.NextCursor == nil {
		t.Fatalf("first page=%#v err=%v", first, err)
	}
	seenSuffix := 0
	for _, row := range first.Rows {
		if row.DisplayName == "Product" {
			if row.DisplayNameSuffix == "" || !strings.Contains(row.DisplayNameSuffix, row.ProductID) {
				t.Fatalf("duplicate name metadata=%#v", row)
			}
			seenSuffix++
		}
	}
	if seenSuffix != 20 {
		t.Fatalf("duplicate suffixes=%d", seenSuffix)
	}
	second, err := s.QueryProductRows(context.Background(), ProductRowRequest{Cursor: *first.NextCursor})
	if err != nil || len(second.Rows) != 5 || second.Rows[0].ProductID == first.Rows[len(first.Rows)-1].ProductID {
		t.Fatalf("second page=%#v err=%v", second, err)
	}
	var cursor productRowCursor
	decoded, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil {
		t.Fatal("cannot decode test cursor")
	}
	cursor.Order = "id:asc"
	encoded, _ := json.Marshal(cursor)
	_, err = s.QueryProductRows(context.Background(), ProductRowRequest{Cursor: base64.RawURLEncoding.EncodeToString(encoded)})
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestProductRowsC14HundredPageAndQueryPlan(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 105; i++ {
		addProductRowProduct(t, s, "page-"+fmtProductRowID(i), "Page "+fmtProductRowID(i))
	}
	page, err := s.QueryProductRows(context.Background(), ProductRowRequest{Limit: 100})
	if err != nil || len(page.Rows) != 100 || page.NextCursor == nil {
		t.Fatalf("100 page=%#v err=%v", page, err)
	}
	planRows, err := s.DatabaseForTesting().Query(`EXPLAIN QUERY PLAN `+productRowPageSQL, "", "", "", "", "", "", 21)
	if err != nil {
		t.Fatal(err)
	}
	defer planRows.Close()
	planLines := 0
	var planDetails []string
	for planRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		if detail != "" {
			planLines++
			planDetails = append(planDetails, detail)
		}
	}
	if err := planRows.Err(); err != nil {
		t.Fatal(err)
	}
	if planLines == 0 {
		t.Fatal("bounded Product-row statement returned no query plan")
	}
	t.Logf("C14 page query plan: %s", strings.Join(planDetails, " | "))
	plan := strings.Join(planDetails, " | ")
	if !strings.Contains(plan, "products_display_name_order") || strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") || strings.Contains(plan, "USE TEMP B-TREE FOR count(DISTINCT)") {
		t.Fatalf("Product ordering lost its supporting index or requires a temp sort: %v", planDetails)
	}
	if strings.Count(productRowPageSQL, "JOIN work_items w") != 1 || strings.Count(productRowPageSQL, "LEFT JOIN workflow_instances wi") != 1 {
		t.Fatal("Product-row page statement regressed to repeated work/workflow reads")
	}
}

func TestProductRowsC14RepresentativeP99(t *testing.T) {
	if productRowSkipPerformanceUnderRace {
		t.Skip("representative latency threshold is measured without race instrumentation")
	}
	s := openTemp(t)
	seedProductRowPerformanceFixture(t, s)
	const samples = 100
	for i := 0; i < 10; i++ {
		if _, err := s.QueryProductRows(context.Background(), ProductRowRequest{Limit: 100}); err != nil {
			t.Fatal(err)
		}
	}
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		started := time.Now()
		if _, err := s.QueryProductRows(context.Background(), ProductRowRequest{Limit: 100}); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99Index := (99*len(durations)+99)/100 - 1
	p99 := durations[p99Index]
	t.Logf("C14 representative P99=%s target=100ms population=100 Products, 200 Projects, 700 work items, 1,400 Project memberships, 200 blocker edges, 100 workflow instances, 100 stage overrides, samples=%d", p99, len(durations))
	if p99 > 100*time.Millisecond {
		t.Fatalf("C14 representative P99=%s exceeds 100ms target", p99)
	}
}

func fmtProductRowID(i int) string {
	return fmt.Sprintf("%03d", i)
}

func projectStageChangedEvent(projectID, eventID, maturity, audience string) Event {
	return operationEvent(eventID, "project.stage_changed", SubjectProject, projectID, map[string]any{
		"stage_maturity_override": maturity, "stage_audience_commitment_override": audience,
	})
}

func seedProductRowPerformanceFixture(t *testing.T, s *Store) {
	t.Helper()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	var statements strings.Builder
	statements.WriteString("INSERT INTO fold_guard(active) VALUES(1);")
	for product := 0; product < 100; product++ {
		productID := fmt.Sprintf("perf-product-%03d", product)
		projectPrimary := fmt.Sprintf("perf-project-%03d-primary", product)
		projectSecondary := fmt.Sprintf("perf-project-%03d-secondary", product)
		stageMaturity := "beta"
		if product%2 == 1 {
			stageMaturity = "production"
		}
		fmt.Fprintf(&statements, "INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('%s','Perf Product %03d','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');", productID, product)
		fmt.Fprintf(&statements, "INSERT INTO projects(id,display_name,stage_maturity_override,stage_audience_commitment_override,version,created_at,updated_at) VALUES('%s','Perf Primary %03d',NULL,NULL,1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');", projectPrimary, product)
		fmt.Fprintf(&statements, "INSERT INTO projects(id,display_name,stage_maturity_override,stage_audience_commitment_override,version,created_at,updated_at) VALUES('%s','Perf Secondary %03d','%s','public',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');", projectSecondary, product, stageMaturity)
		fmt.Fprintf(&statements, "INSERT INTO product_projects(product_id,project_id,role) VALUES('%s','%s','primary'),('%s','%s','secondary');", productID, projectPrimary, productID, projectSecondary)
		for work := 0; work < 7; work++ {
			workID := fmt.Sprintf("perf-work-%03d-%02d", product, work)
			kind, lifecycle := "task", "needed"
			terminal := "NULL"
			switch work {
			case 1:
				kind, lifecycle = "problem", "in_progress"
			case 4:
				lifecycle, terminal = "completed", "'2026-08-05T00:00:00Z'"
			case 5:
				lifecycle, terminal = "cancelled", "'2026-08-05T00:00:00Z'"
			case 6:
				lifecycle = "in_progress"
			}
			fmt.Fprintf(&statements, "INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('%s','%s','Perf work %03d-%02d','%s',%d,1,'2026-08-%02dT00:00:00Z','2026-08-%02dT00:00:00Z',%s);", workID, kind, product, work, lifecycle, work+1, (work%8)+1, (work%8)+1, terminal)
			fmt.Fprintf(&statements, "INSERT INTO work_projects(work_id,project_id,role) VALUES('%s','%s','primary'),('%s','%s','secondary');", workID, projectPrimary, workID, projectSecondary)
			if work == 0 {
				fmt.Fprintf(&statements, "INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,started_at) VALUES('%s','%s',%d,'%s','planning','ready','2026-08-01T00:00:00Z');", workID, definition.Definition.Ref, definition.Definition.Version, definition.Digest)
			}
		}
		blockerID := fmt.Sprintf("perf-work-%03d-06", product)
		fmt.Fprintf(&statements, "INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('%s','perf-work-%03d-02','blocks','2026-08-05T00:00:00Z'),('%s','perf-work-%03d-04','blocks','2026-08-05T00:00:00Z');", blockerID, product, blockerID, product)
	}
	statements.WriteString("DELETE FROM fold_guard;")
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(), statements.String()); err != nil {
		t.Fatal(err)
	}
}
