package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/launcher/render/bubbletea"
	"github.com/sharper-flow/concord/internal/launcher/storeport"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func preferredLaneModel(lane store.LaneDefinition) string {
	switch lane.CapabilityClass {
	case "review":
		return "zai-coding-plan/glm-5.3"
	default:
		return "openai/gpt-5.6-luna"
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("run() exit code = %d, want 0", code)
	}
	if got := out.String(); got != "dev\n" {
		t.Fatalf("version output = %q, want %q", got, "dev\n")
	}
	if errOut.Len() != 0 {
		t.Fatalf("version error output = %q, want empty", errOut.String())
	}
}

func TestStampedBuildReportsVersion(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	executable := filepath.Join(t.TempDir(), "concord")
	const stamped = "v1.2.3"
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-X github.com/sharper-flow/concord/internal/version.Value="+stamped, "-o", executable, "./cmd/concord")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("stamped build failed: %v\n%s", err, output)
	}
	output, err := exec.Command(executable, "--version").Output()
	if err != nil {
		t.Fatalf("stamped binary failed: %v", err)
	}
	if got := string(output); got != stamped+"\n" {
		t.Fatalf("stamped version output = %q, want %q", got, stamped+"\n")
	}
}

func TestDatabaseOverrideRefusesRepositoryLocalPath(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", repo)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, filepath.Join(repo, "nested", "concord.db"))
	if _, err := databasePath(); err == nil {
		t.Fatal("repository-local database override accepted")
	}
	if _, err := os.Stat(filepath.Join(repo, "nested")); !os.IsNotExist(err) {
		t.Fatal("database override created a repository-local directory")
	}
}

func TestInvokeRejectsUnknownFieldWithoutEcho(t *testing.T) {
	unknownValue := strings.Repeat("a", 63) + "b"
	raw := []byte(`{"call_envelope":{"schema_version":"1.0","request_id":"r","unexpected_field":"` + unknownValue + `","client_ref":"c","scope_version":"","manifest_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"tool":"concord_product_view","operation":"resolve","input":{}}`)
	var out, errOut bytes.Buffer
	if code := runInvoke(raw, nil, nil, &out, &errOut); code == 0 {
		t.Fatalf("runInvoke accepted unknown field: stderr=%q", errOut.String())
	}
	if strings.Contains(out.String(), unknownValue) || strings.Contains(errOut.String(), unknownValue) {
		t.Fatal("unknown field value leaked through invoke output")
	}
}

func TestRunWithoutArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code == 0 {
		t.Fatalf("run() exit code = %d, want nonzero", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("run() output = %q / %q, want usage on stderr", out.String(), errOut.String())
	}
}

func TestHelpListsHostBootstrapCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("help exit code=%d stderr=%q", code, errOut.String())
	}
	for _, command := range []string{"concord work-bootstrap < JSON stdin", "concord session-prepare < JSON stdin"} {
		if !strings.Contains(out.String(), command) {
			t.Fatalf("help does not list %q", command)
		}
	}
}

func TestLauncherRoutesBeforeJSONAndRejectsNonTTY(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"launcher"}, strings.NewReader("not json"), &out, &errOut); code != 2 {
		t.Fatalf("launcher exit code = %d, want 2; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "requires an interactive TTY") {
		t.Fatalf("non-TTY diagnostic = %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "JSON") {
		t.Fatalf("launcher was routed through JSON handling: %q", errOut.String())
	}
}

// freshMigratedCLIDatabase points the database override at a copy of the
// shared migrated template and returns the path. The first in-process CLI
// run then validates a current schema instead of replaying every migration,
// which under the race detector costs seconds per fresh database. Tests that
// deliberately exercise fresh-install or migration behavior construct their
// own path instead of calling this.
func freshMigratedCLIDatabase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := storetest.WriteMigratedDatabase(dir, "concord.db"); err != nil {
		t.Fatalf("materialize migrated template: %v", err)
	}
	path := filepath.Join(dir, "concord.db")
	t.Setenv(dbOverrideEnv, path)
	return path
}

func TestLauncherFirstRunRendersWithoutCreatingAuthority(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut bytes.Buffer
	if code := runLauncherCommand(nil, strings.NewReader("q"), &out, &errOut, true); code != 0 {
		t.Fatalf("first-run launcher exit code = %d; stderr=%q", code, errOut.String())
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("first-run launcher changed authority path: stat=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("first-run launcher created an authority parent: stat=%v", err)
	}
	firstRun, err := firstRunPort{}.Read(context.Background(), launcher.ReadRequest{Kind: launcher.ReadPortfolio})
	if err != nil || !firstRun.FirstRun || firstRun.Coverage != "first_run" || firstRun.StatusMessage == "" {
		t.Fatalf("first-run state = %#v, err=%v", firstRun, err)
	}
}

func TestLauncherSessionHasNoDurableEffects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProductWithProject(context.Background(), store.ProductCreation{
		ProductID: "product-1", DisplayName: "Concord", StageMaturity: "prototype",
		StageAudienceCommitment: "operator_only", ProjectID: "project-1", ProjectDisplayName: "Core", Role: "primary",
	}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	before := launcherDurableCounts(t, s)
	if before == nil {
		s.Close()
		return
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := runLauncherCommand(nil, strings.NewReader("q"), &out, &errOut, true); code != 0 {
		t.Fatalf("launcher exit code = %d; stderr=%q", code, errOut.String())
	}
	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := launcherDurableCounts(t, s)
	if after == nil {
		return
	}
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("launcher changed durable state: before=%v after=%v", before, after)
	}
}

func launcherDurableCounts(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"domain_events", "agent_approvals", "agent_approval_challenges", "idempotency_records", "durable_operations"} {
		var count int
		if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Errorf("count %s: %v", table, err)
			return nil
		}
		counts[table] = count
	}
	return counts
}

// recordingPort records which read kinds a launcher session actually reached,
// so a durability assertion cannot pass by never reading at all.
type recordingPort struct {
	inner launcher.ReadPort
	kinds map[launcher.ReadKind]int
}

func (p *recordingPort) Read(ctx context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	if p.kinds == nil {
		p.kinds = map[launcher.ReadKind]int{}
	}
	p.kinds[request.Kind]++
	return p.inner.Read(ctx, request)
}

func TestFullLauncherSessionAppendsNothingToTheEventLog(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	if code, _, diag := runPredecessorImportRequest(t, dbPath, predecessorImportRequest(t, writeSyntheticSnapshot(t))); code != 0 {
		t.Fatalf("seed import exit=%d, want 0; stderr=%q", code, diag)
	}
	s := openFreshImportStore(t, dbPath)
	defer s.Close()

	before := eventLogState(t, s)
	if before == "" {
		t.Fatal("seeded event log is empty, so an unchanged log would prove nothing")
	}
	beforeCounts := launcherDurableCounts(t, s)
	if beforeCounts == nil {
		return
	}

	port := &recordingPort{inner: storeport.New(s)}
	core := launcher.New(port)
	if err := core.Enter(ctx); err != nil {
		t.Fatalf("S1 entry: %v", err)
	}
	m := bubbletea.New(core, ctx, bubbletea.Profile{})
	m.Sync()
	if got := core.Snapshot(); len(got.Rows) == 0 {
		t.Fatalf("seeded S1 rendered no Products, so the session reads nothing: %#v", got)
	}

	m.UpdateKey("r")     // S1 explicit refresh
	m.UpdateKey("enter") // S1 -> S2, which also reads the focused knowledge section
	if got := core.Snapshot(); got.Screen != launcher.ScreenProduct || got.AmbientProduct == "" {
		t.Fatalf("S2 entry = %#v", got)
	}
	m.UpdateKey("tab") // domain -> blocked
	m.UpdateKey("tab") // blocked -> next, the ranked work mode
	m.UpdateKey("r")   // S2 explicit refresh
	m.UpdateKey("s")   // S2 semantic query
	m.UpdateKey("b")
	m.UpdateKey("enter")
	m.UpdateKey("esc") // leave the query result

	// The seeded work item is addressed directly so S3 is reached even when the
	// ranked mode renders nothing. A failure here would leave the read-coverage
	// assertion below satisfied by an attempted read, so it fails the test.
	if err := core.SelectWork(ctx, "import-advance-work-synth-change-alpha-1"); err != nil {
		t.Fatalf("S3 entry: %v", err)
	}
	if got := core.Snapshot(); got.Screen != launcher.ScreenWork {
		t.Fatalf("S3 entry left the session on %v, so the work screen is unexercised", got.Screen)
	}
	m.Sync()
	m.UpdateKey("tab") // S3 sections, ending on knowledge
	m.UpdateKey("tab")
	m.UpdateKey("tab")
	m.UpdateKey("r") // S3 explicit refresh
	m.UpdateKey("s") // S3 semantic query
	m.UpdateKey("b")
	m.UpdateKey("enter")
	m.UpdateKey("esc") // leave the query result
	m.UpdateKey("esc") // S3 -> S2
	m.UpdateKey("esc") // S2 -> S1
	m.UpdateKey("r")   // S1 explicit refresh
	m.Render()

	for _, kind := range []launcher.ReadKind{
		launcher.ReadPortfolio, launcher.ReadDomains, launcher.ReadProduct,
		launcher.ReadWork, launcher.ReadKnowledge, launcher.ReadSearch,
	} {
		if port.kinds[kind] == 0 {
			t.Fatalf("session never issued a %s read: kinds=%v", kind, port.kinds)
		}
	}

	if after := eventLogState(t, s); after != before {
		t.Fatalf("launcher session changed the event log:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if after := launcherDurableCounts(t, s); after == nil || fmt.Sprint(after) != fmt.Sprint(beforeCounts) {
		t.Fatalf("launcher session changed durable state: before=%v after=%v", beforeCounts, after)
	}
}

// eventLogState serializes every column of every event in log order. It reads
// the raw handle outside any transaction, so it never contends with the store's
// single pooled connection.
func eventLogState(t *testing.T, s *store.Store) string {
	t.Helper()
	rows, err := s.DatabaseForTesting().QueryContext(context.Background(),
		`SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	defer rows.Close()
	var log strings.Builder
	for rows.Next() {
		var seq int64
		var payloadVersion int
		var eventID, kind, subjectType, subjectID, actor, occurredAt, payload string
		if err := rows.Scan(&seq, &eventID, &kind, &subjectType, &subjectID, &actor, &occurredAt, &payloadVersion, &payload); err != nil {
			t.Fatalf("scan event log: %v", err)
		}
		fmt.Fprintf(&log, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			seq, eventID, kind, subjectType, subjectID, actor, occurredAt, payloadVersion, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate event log: %v", err)
	}
	return log.String()
}

func TestRunHelpListsExactCommandFormsAndStdinShapes(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("help exit code = %d, want 0; stderr=%q", code, errOut.String())
	}
	for _, want := range []string{
		"concord --help",
		"concord client-register < JSON stdin",
		"concord client register < JSON stdin",
		"concord client-policy-expand < JSON stdin",
		"concord client policy-expand < JSON stdin",
		"concord product-create < JSON stdin",
		"concord product create < JSON stdin",
		"concord resource-create < JSON stdin",
		"concord resource create < JSON stdin",
		"concord resource-share < JSON stdin",
		"concord resource share < JSON stdin",
		"concord domain-project-attachments-replace < JSON stdin",
		"concord domain project-attachments-replace < JSON stdin",
		"concord domain-resource-attachments-replace < JSON stdin",
		"concord domain resource-attachments-replace < JSON stdin",
		"concord project-locator-add < JSON stdin",
		"concord project locator-add < JSON stdin",
		"concord invoke < JSON stdin",
		"required: client_ref, key_id, principal_ref, public_key, capabilities, product_scope, project_scope",
		"required: product_id, display_name, stage_maturity, stage_audience_commitment, project_id, project_display_name, role",
		"stage_maturity: prototype | alpha | beta | production | deprecated",
		"stage_audience_commitment: operator_only | limited | public",
		"kind: canonical_path | git_remote",
		"attachments replaces the complete Domain-to-Project edge set; it does not append",
		"attachments[].role: primary | secondary",
		"capabilities: product_read | work_define | work_transition | work_relate | work_compact | work_initiative | cross_scope",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output missing %q", want)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("help stderr = %q, want empty", errOut.String())
	}
	if out.Len() > 12288 {
		t.Fatalf("help output is unbounded: %d bytes", out.Len())
	}
}

func TestProductStageUpdateCLIRecordsPromotion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "stage-update-product", "stage-update-project")
	runOperatorJSON(t, dbPath, []string{"product-stage-update"}, map[string]any{
		"product_id": "stage-update-product", "stage_maturity": "alpha", "stage_audience_commitment": "operator_only",
		"reason": "CD-0091 alpha rung manifest satisfied", "expected_version": 2,
	})
	s2, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var gotKind string
	if err := s2.DatabaseForTesting().QueryRow(`SELECT kind FROM domain_events WHERE kind='product.stage_changed' AND subject_id='stage-update-product'`).Scan(&gotKind); err != nil {
		t.Fatalf("no product.stage_changed event: %v", err)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var maturity, audience string
	var version int
	if err := s.DatabaseForTesting().QueryRow(`SELECT stage_maturity, stage_audience_commitment, version FROM products WHERE id='stage-update-product'`).Scan(&maturity, &audience, &version); err != nil {
		t.Fatal(err)
	}
	if maturity != "alpha" || audience != "operator_only" || version != 3 {
		t.Fatalf("stage after CLI update: %s/%s v%d", maturity, audience, version)
	}
	// A refusal stays typed: the closed enum is enforced before an event lands.
	var out, errOut bytes.Buffer
	t.Setenv(dbOverrideEnv, dbPath)
	if code := runWithInput([]string{"product-stage-update"}, strings.NewReader(`{"product_id":"stage-update-product","stage_maturity":"ga","stage_audience_commitment":"operator_only","expected_version":3}`), &out, &errOut); code == 0 {
		t.Fatal("invalid maturity must exit non-zero")
	}
	if !strings.Contains(errOut.String(), "accepted Product stage values") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}

func TestResourceCreateCLIRecordsExpectedEvent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "resource-create-product", "resource-create-project")
	runOperatorJSON(t, dbPath, []string{"resource-create"}, map[string]any{
		"event_id": "resource-create-event", "resource_id": "resource-create-id", "product_id": "resource-create-product",
		"display_name": "Queue", "class": "infrastructure", "kind": "queue", "purpose": "dispatches work",
		"stage_maturity": "production", "stage_audience_commitment": "limited", "environments": []string{"production"},
		"metadata_schema_version": "1", "metadata": map[string]any{}, "expected_product_version": 2,
	})
	assertCLIEvent(t, dbPath, "resource-create-event", "managed_resource.created")
}

func TestResourceShareCLIRecordsExpectedEvent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "resource-share-owner", "resource-share-owner-project")
	seedCLIProduct(t, dbPath, "resource-share-consumer", "resource-share-consumer-project")
	runOperatorJSON(t, dbPath, []string{"resource-create"}, map[string]any{
		"event_id": "resource-share-create", "resource_id": "resource-share-id", "product_id": "resource-share-owner",
		"display_name": "Database", "class": "infrastructure", "kind": "database", "purpose": "stores data",
		"stage_maturity": "production", "stage_audience_commitment": "limited", "environments": []string{"production"},
		"metadata_schema_version": "1", "metadata": map[string]any{}, "expected_product_version": 2,
	})
	runOperatorJSON(t, dbPath, []string{"resource-share"}, map[string]any{
		"event_id": "resource-share-event", "resource_id": "resource-share-id", "product_id": "resource-share-consumer",
		"purpose": "reads data", "environments": []string{"production"}, "expected_resource_version": 1,
	})
	assertCLIEvent(t, dbPath, "resource-share-event", "managed_resource.consumer_added")
}

func TestDomainProjectAttachmentsReplaceCLIRecordsExpectedEvent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "domain-project-product", "domain-project-project")
	seedCLIDomain(t, dbPath, "domain-project-product", "domain-project-id")
	runOperatorJSON(t, dbPath, []string{"domain-project-attachments-replace"}, map[string]any{
		"event_id": "domain-project-attachments-event", "product_id": "domain-project-product", "domain_id": "domain-project-id",
		"expected_version": 0, "attachments": []map[string]string{{"project_id": "domain-project-project", "role": "primary"}},
	})
	assertCLIEvent(t, dbPath, "domain-project-attachments-event", "domain.project_attachments_replaced")
}

func TestDomainResourceAttachmentsReplaceCLIRecordsExpectedEvent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "domain-resource-product", "domain-resource-project")
	runOperatorJSON(t, dbPath, []string{"resource-create"}, map[string]any{
		"event_id": "domain-resource-create", "resource_id": "domain-resource-id", "product_id": "domain-resource-product",
		"display_name": "Queue", "class": "infrastructure", "kind": "queue", "purpose": "dispatches work",
		"stage_maturity": "production", "stage_audience_commitment": "limited", "environments": []string{"production"},
		"metadata_schema_version": "1", "metadata": map[string]any{}, "expected_product_version": 2,
	})
	seedCLIDomain(t, dbPath, "domain-resource-product", "domain-resource-id")
	runOperatorJSON(t, dbPath, []string{"domain-resource-attachments-replace"}, map[string]any{
		"event_id": "domain-resource-attachments-event", "product_id": "domain-resource-product", "domain_id": "domain-resource-id",
		"expected_version": 0, "attachments": []map[string]any{{"resource_id": "domain-resource-id", "purpose": "uses queue", "environments": []string{"production"}}},
	})
	assertCLIEvent(t, dbPath, "domain-resource-attachments-event", "domain.resource_attachments_replaced")
}

func runOperatorJSON(t *testing.T, dbPath string, args []string, payload map[string]any) {
	t.Helper()
	t.Setenv(dbOverrideEnv, dbPath)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput(args, bytes.NewReader(raw), &out, &errOut); code != 0 {
		t.Fatalf("%s exit=%d stdout=%q stderr=%q", strings.Join(args, " "), code, out.String(), errOut.String())
	}
}

func seedCLIProduct(t *testing.T, dbPath, productID, projectID string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateProductWithProject(context.Background(), store.ProductCreation{
		ProductID: productID, DisplayName: productID, StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: projectID, ProjectDisplayName: projectID, Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
}

func seedCLIDomain(t *testing.T, dbPath, productID, domainID string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	homeProject, homeLocator := "home-"+productID, "locator-"+productID
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES(?,?,1,'now','now')`, []any{homeProject, homeProject}},
		{`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?, 'canonical_path', ?, ?, 'now', 'now')`, []any{homeLocator, homeProject, "/test/" + homeLocator, "/test/" + homeLocator}},
		{`INSERT INTO product_projects(product_id,project_id,role) VALUES(?,?, 'secondary')`, []any{productID, homeProject}},
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,'1.0',?,'commit')`, []any{productID, homeProject, homeLocator, "key-" + productID, domainID, hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{homeProject, homeLocator, productID, domainID, domainID, "test domain", nil, "current", hash, "commit"}},
	} {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertCLIEvent(t *testing.T, dbPath, eventID, kind string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var gotKind, actor, occurredAt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind,actor,occurred_at FROM domain_events WHERE event_id=?`, eventID).Scan(&gotKind, &actor, &occurredAt); err != nil {
		t.Fatal(err)
	}
	if gotKind != kind || actor != "operator" || occurredAt == "" {
		t.Fatalf("event = kind:%q actor:%q occurred_at:%q, want %q/operator/non-empty", gotKind, actor, occurredAt, kind)
	}
}

func TestDocumentedBootstrapPayloadsRemainExecutable(t *testing.T) {
	readme, err := os.ReadFile("../../adapter/opencode/README.md")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := extractBootstrapSteps(string(readme))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("README bootstrap steps = %d, want 3", len(steps))
	}
	root := t.TempDir()
	repository := filepath.Join(root, "workspace", "concord-demo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "--quiet", repository).Run(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	for _, step := range steps {
		var out, errOut bytes.Buffer
		if code := runWithInput(step.args, strings.NewReader(step.payload), &out, &errOut); code != 0 {
			t.Fatalf("documented %s payload failed: exit=%d stderr=%q", step.command, code, errOut.String())
		}
	}
}

type bootstrapStep struct {
	command string
	args    []string
	payload string
}

var bootstrapPayloadLine = regexp.MustCompile(`^printf '%s\\n' '([^']*)' \| concord (.+)$`)

func extractBootstrapSteps(readme string) ([]bootstrapStep, error) {
	const heading = "## Verbatim first installation"
	start := strings.Index(readme, heading)
	if start < 0 {
		return nil, fmt.Errorf("README is missing %q", heading)
	}
	blockStart := strings.Index(readme[start:], "```sh\n")
	if blockStart < 0 {
		return nil, fmt.Errorf("README bootstrap shell block is missing")
	}
	blockStart += start + len("```sh\n")
	blockEnd := strings.Index(readme[blockStart:], "\n```")
	if blockEnd < 0 {
		return nil, fmt.Errorf("README bootstrap shell block is unterminated")
	}
	block := readme[blockStart : blockStart+blockEnd]
	steps := []bootstrapStep{}
	for lineNumber, rawLine := range strings.Split(block, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "export CONCORD_DB_PATH=") || line == "mkdir -p workspace/concord-demo" || line == "git -C workspace/concord-demo init --quiet" || (strings.HasPrefix(line, "printf '%s\\n' 'base64:") && strings.HasSuffix(line, " | secret-tool store --label='Concord demo client' service concord account demo-client")) {
			continue
		}
		matches := bootstrapPayloadLine.FindStringSubmatch(line)
		if len(matches) != 3 {
			return nil, fmt.Errorf("README bootstrap line %d is malformed: %q", lineNumber+1, line)
		}
		args := strings.Fields(matches[2])
		command, commandArgs, ok := routeCommand(args)
		if !ok || len(commandArgs) != 0 {
			return nil, fmt.Errorf("README bootstrap line %d uses unroutable command %q", lineNumber+1, matches[2])
		}
		steps = append(steps, bootstrapStep{command: command, args: args, payload: matches[1]})
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("README bootstrap shell block contains no Concord commands")
	}
	return steps, nil
}

func TestRunRejectsUnsupportedArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"status"}, &out, &errOut); code != 2 {
		t.Fatalf("run() exit code = %d, want 2", code)
	}
	if got := errOut.String(); !strings.Contains(got, "concord: unsupported arguments: status") || !strings.Contains(got, "Usage:") {
		t.Fatalf("error output = %q, want diagnostic plus usage", got)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}

func TestOperatorErrorsUseCommandDiagnosticPrefix(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"client-register"}, strings.NewReader(`{"public_key":"not-a-key"}`), &out, &errOut); code == 0 {
		t.Fatal("invalid operator command succeeded")
	}
	if got := errOut.String(); !strings.HasPrefix(got, "concord client-register: ") || strings.Contains(got, "not-a-key") {
		t.Fatalf("operator diagnostic = %q, want command prefix without payload value", got)
	}
}

func TestCommandBoundaryRejectsInvalidTrailingJSONAcrossCommands(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"two objects", `{} {}`, "trailing JSON"},
		{"invalid trailing content", `{} garbage`, "trailing JSON"},
		{"trailing whitespace", "{} \n\t", ""},
		{"empty input", "", ""},
	}
	for _, command := range []string{"client-register"} {
		for _, testCase := range cases {
			t.Run(command+"/"+testCase.name, func(t *testing.T) {
				freshMigratedCLIDatabase(t)
				var out, errOut bytes.Buffer
				if code := runWithInput([]string{command}, strings.NewReader(testCase.input), &out, &errOut); code == 0 {
					t.Fatalf("input was accepted: stdout=%q stderr=%q", out.String(), errOut.String())
				}
				if testCase.want != "" && !strings.Contains(errOut.String(), testCase.want) {
					t.Fatalf("diagnostic = %q, want %q", errOut.String(), testCase.want)
				}
				if testCase.want == "" && strings.Contains(errOut.String(), "trailing JSON") {
					t.Fatalf("whitespace/empty input reported trailing JSON: %q", errOut.String())
				}
			})
		}
	}
}

func TestCommandRouterAcceptsCanonicalAndTwoWordFormsWithoutPanicking(t *testing.T) {
	db := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, db)
	validJSON := strings.NewReader(`{}`)
	forms := [][]string{
		{"invoke"},
		{"client-register"}, {"client", "register"},
		{"client-policy-update"}, {"client", "policy-update"},
		{"client-policy-expand"}, {"client", "policy-expand"},
		{"client-key-rotate"}, {"client", "key-rotate"},
		{"client-revoke"}, {"client", "revoke"},
		{"project-locator-add"}, {"project", "locator-add"},
		{"project-locator-update"}, {"project", "locator-update"},
		{"project-locator-remove"}, {"project", "locator-remove"},
		{"project-resolve"}, {"project", "resolve"},
		{"product-create"}, {"product", "create"},
		{"resource-create"}, {"resource", "create"},
		{"resource-share"}, {"resource", "share"},
		{"domain-project-attachments-replace"}, {"domain", "project-attachments-replace"},
		{"domain-resource-attachments-replace"}, {"domain", "resource-attachments-replace"},
		{"project-create"}, {"project", "create"},
		{"product-project-add"}, {"product", "project-add"},
		{"predecessor-import"}, {"predecessor", "import"},
	}
	for _, args := range forms {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("command panicked: %v", recovered)
				}
			}()
			code := runWithInput(args, validJSON, &out, &errOut)
			if code == 2 {
				t.Fatalf("accepted command was rejected: stderr=%q", errOut.String())
			}
		})
	}
}

func TestCommandRouterRejectsUnsupportedFormsCleanly(t *testing.T) {
	forms := [][]string{
		{"client"}, {"client", "unknown"}, {"client", "register", "extra"},
		{"project", "locator", "add"}, {"product", "create", "extra"},
		{"product-unknown"}, {"unknown"},
	}
	for _, args := range forms {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("command panicked: %v", recovered)
				}
			}()
			if code := runWithInput(args, strings.NewReader(`{}`), &out, &errOut); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr=%q", code, errOut.String())
			}
		})
	}
}

func TestWorkerCLIRecordsLifecycleAndReadbackOnly(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	workerKey := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	// Seed the first dispatch window before each dispatch: the
	// dispatch_window gate resolves the active window from the most
	// recent dispatch_worker start, so seeding all three up front would
	// bind the gate to attempt-3 before attempt-1 ever dispatches.
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	dispatch := workerDispatchJSON(t, workerKey, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-lifecycle-dispatch1")
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(dispatch), &out, &errOut); code != 0 {
		t.Fatalf("worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
	complete := workerCompleteJSON(t, workerKey, "complete-1", "work-1", "attempt-1", preferredLaneModel(lane), "nonce-lifecycle-complete1", &lane)
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(complete), &out, &errOut); code != 0 {
		t.Fatalf("worker-complete exit=%d stderr=%q", code, errOut.String())
	}

	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-2")
	failedDispatch := workerDispatchJSON(t, workerKey, "dispatch-2", "work-1", "attempt-2", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-lifecycle-dispatch2")
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(failedDispatch), &out, &errOut); code != 0 {
		t.Fatalf("failed worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
	fail := workerFailJSON(t, workerKey, "fail-2", "work-1", "attempt-2", lane, preferredLaneModel(lane), string(store.WorkerFailureFallbackBlocked), "provider unavailable", "nonce-lifecycle-fail000002")
	if code := runWithInput([]string{"worker-fail"}, strings.NewReader(fail), &out, &errOut); code != 0 {
		t.Fatalf("worker-fail exit=%d stderr=%q", code, errOut.String())
	}

	// CD-0058: a completion whose readback differs from the dispatch readback
	// is accepted as a normal completion. The readback is whatever the host
	// reported, and the only model evidence Concord records.
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-3")
	divergentDispatch := workerDispatchJSON(t, workerKey, "dispatch-3", "work-1", "attempt-3", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-lifecycle-dispatch3")
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(divergentDispatch), &out, &errOut); code != 0 {
		t.Fatalf("divergent worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
	divergent := workerCompleteJSON(t, workerKey, "complete-3", "work-1", "attempt-3", "openai/fallback-model", "nonce-lifecycle-mismatch01", &lane)
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(divergent), &out, &errOut); code != 0 {
		t.Fatalf("divergent worker-complete exit=%d stderr=%q", code, errOut.String())
	}

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var completed, failed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkerCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkerFailed).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if completed != 2 || failed != 1 {
		t.Fatalf("worker lifecycle events = completed:%d failed:%d, want completed:2 failed:1", completed, failed)
	}
	var state, storedReadback string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id=?`, "attempt-3").Scan(&state, &storedReadback); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || storedReadback != "openai/fallback-model" {
		t.Fatalf("divergent projection = %s/%s, want completed/openai/fallback-model", state, storedReadback)
	}
}

func TestWorkerCLIRejectsUnknownAndInvalidDispatchIdentity(t *testing.T) {
	lane := store.BuiltinLaneDefinitions()[0]
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "unknown lane", mutate: func(value map[string]any) { value["lane_id"] = "unknown" }, want: string(store.KindLaneDefinitionNotRegistered)},
		{name: "digest mismatch", mutate: func(value map[string]any) { value["lane_digest"] = "sha256:" + strings.Repeat("0", 64) }, want: string(store.KindLaneDefinitionDigestMismatch)},
		{name: "missing host provenance", mutate: func(value map[string]any) { delete(value, "host_provenance") }, want: "v3 evidence requires host_provenance"},
		{name: "packet schema mismatch", mutate: func(value map[string]any) { value["packet_schema_version"] = "9.0" }, want: string(store.KindInvalidPayload)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			dbPath := freshMigratedCLIDatabase(t)
			seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
			value := map[string]any{}
			if err := json.Unmarshal([]byte(workerDispatchJSON(t, seedWorkerEvidenceClient(t), "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-identity-dispatch01")), &value); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(value)
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"worker-dispatch"}, bytes.NewReader(raw), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), testCase.want) {
				t.Fatalf("exit=%d stderr=%q, want typed failure %q", code, errOut.String(), testCase.want)
			}
		})
	}
}

func TestWorkerCLIAcceptsRecordedFallbackAndCompletesOnMatchingReadback(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	workerKey := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := "openai/gpt-5.6-luna"
	dispatch := workerDispatchJSON(t, workerKey, "dispatch-1", "work-1", "attempt-1", lane, readback, store.WorkerPacketSchemaVersion, "nonce-fallback-dispatch01")
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(dispatch), &out, &errOut); code != 0 {
		t.Fatalf("dispatch exit=%d stderr=%q", code, errOut.String())
	}
	complete := workerCompleteJSON(t, workerKey, "complete-1", "work-1", "attempt-1", readback, "nonce-fallback-complete01", &lane)
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(complete), &out, &errOut); code != 0 {
		t.Fatalf("complete exit=%d stderr=%q", code, errOut.String())
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state, storedReadback string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id=?`, "attempt-1").Scan(&state, &storedReadback); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || storedReadback != readback {
		t.Fatalf("CLI projection = %s/%s, want completed/%s", state, storedReadback, readback)
	}
}

func workerDispatchJSON(t *testing.T, key ed25519.PrivateKey, eventID, workID, attemptID string, lane store.LaneDefinition, resolvedModel, packetVersion, nonce string) string {
	t.Helper()
	return workerDispatchJSONWith(t, key, eventID, workID, attemptID, lane, resolvedModel, nonce, func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
		return a
	}, packetVersion)
}

// workerDispatchJSONWith builds signed dispatch evidence and lets a caller
// perturb the assertion before signing, so negative tests can prove that a
// signature over the wrong identity is refused.
func workerDispatchJSONWith(t *testing.T, key ed25519.PrivateKey, eventID, workID, attemptID string, lane store.LaneDefinition, readbackModel, nonce string, mutate func(agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion, packetVersion ...string) string {
	t.Helper()
	packet := store.WorkerPacketSchemaVersion
	if len(packetVersion) == 1 {
		packet = packetVersion[0]
	}
	provenanceDigest := "sha256:" + strings.Repeat("a", 64)
	// CD-0067 D6: the dispatched request carries the canonical packet
	// digest the dispatch authorization recorded. The dispatch window
	// the seedAuthorizedDispatchWindow helper folds already carries a
	// matching digest, so the test seed keeps them in lockstep.
	packetDigest := "sha256:" + strings.Repeat("c", 64)
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbDispatch, WorkID: workID, AttemptID: attemptID,
		LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		ReadbackModel: readbackModel, HostProvenanceDigest: provenanceDigest, PacketDigest: packetDigest, Nonce: nonce,
	}
	if mutate != nil {
		assertion = mutate(assertion)
	}
	value := map[string]any{
		"event_id": eventID, "work_id": workID, "attempt_id": attemptID,
		"lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"readback_model":        readbackModel,
		"packet_schema_version": packet, "report_schema_version": store.WorkerReportSchemaVersion,
		"packet_digest": packetDigest,
		// CD-0032: v3 dispatch evidence requires declared host provenance.
		"host_provenance": map[string]any{
			"digest":  provenanceDigest,
			"sources": []map[string]any{{"kind": "agents_md", "path": "/repo/AGENTS.md", "sha256": "sha256:" + strings.Repeat("b", 64)}, {"kind": "unenumerated"}},
		},
		"assertion": signWorkerEvidence(t, key, assertion),
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCLIEndToEndRegistersClientAndInvokesRead(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", repo).Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	publicKey, _, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registerCLIClient(t, "client-1", publicKey, "product-1", "project-1")

	creationRaw := runCLIJSON(t, []string{"product", "create"}, map[string]any{
		"product_id":                "product-1",
		"display_name":              "Concord",
		"stage_maturity":            "prototype",
		"stage_audience_commitment": "operator_only",
		"project_id":                "project-1",
		"project_display_name":      "Concord repository",
		"role":                      "primary",
	})
	assertChangedRefVersion(t, creationRaw, "product", "product-1", "2")
	projectVersion := changedRefVersion(t, creationRaw, "project", "project-1")
	if projectVersion != 1 {
		t.Fatalf("new Project version = %d, want 1", projectVersion)
	}
	locatorRaw := runCLIJSON(t, []string{"project-locator-add"}, map[string]any{
		"project_id":       "project-1",
		"locator_id":       "repo-locator",
		"kind":             "canonical_path",
		"value":            repo,
		"expected_version": projectVersion,
	})
	assertChangedRefVersion(t, locatorRaw, "project", "project-1", "2")

	resolvedRaw := runCLIJSON(t, []string{"project", "resolve"}, map[string]any{"directory": repo, "worktree": repo})
	var resolved struct {
		ScopeVersion string `json:"scope_version"`
	}
	if err := json.Unmarshal(resolvedRaw, &resolved); err != nil {
		t.Fatal(err)
	}
	invokeRaw := runCLIJSON(t, []string{"invoke"}, map[string]any{
		"call_envelope": map[string]any{
			"schema_version": "1.0", "request_id": "request-e2e", "client_ref": "client-1", "principal_ref": "operator-1",
			"session_ref": "session-1", "agent_ref": "agent-1", "directory": repo, "worktree": repo,
			"ambient_project_id": "project-1", "selected_product_id": "product-1", "scope_version": resolved.ScopeVersion,
			"manifest_digest": agent.ManifestDigest,
		},
		"tool": "concord_product_view", "operation": "resolve", "input": map[string]any{"project_id": "project-1"},
	})
	var envelope agent.Envelope
	// Envelope.UnmarshalJSON carries the schema validation; decoding through it
	// is the published way to read a TS7 envelope.
	err = json.Unmarshal(invokeRaw, &envelope)
	if err != nil {
		t.Fatalf("invoke output is not one schema-valid TS7 envelope: %v; raw=%s", err, invokeRaw)
	}
	if envelope.Outcome != agent.OutcomeOK {
		t.Fatalf("invoke outcome=%s, want ok; raw=%s", envelope.Outcome, invokeRaw)
	}
}

func TestBackupAndRestoreRoundTripViaCLI(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	if _, err := store.Open(context.Background(), dbPath); err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	destination := filepath.Join(backupDir, "snapshot.db")
	runBackupRaw := runCLIJSON(t, []string{"backup"}, map[string]any{"destination": destination})
	var backupManifest struct {
		SchemaVersion  int    `json:"schema_version"`
		SnapshotID     string `json:"snapshot_id"`
		BinaryVersion  string `json:"binary_version"`
		IntegrityCheck string `json:"integrity_check"`
	}
	if err := json.Unmarshal(runBackupRaw, &backupManifest); err != nil {
		t.Fatalf("backup response is not a manifest: %v; raw=%s", err, runBackupRaw)
	}
	if backupManifest.SchemaVersion < 1 || backupManifest.SnapshotID == "" || backupManifest.IntegrityCheck != "ok" {
		t.Fatalf("backup manifest fields = %+v, want verified snapshot", backupManifest)
	}

	restoreHome := t.TempDir()
	restoreParent := filepath.Join(restoreHome, "concord")
	if err := os.MkdirAll(restoreParent, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreDestination := filepath.Join(restoreParent, "concord.db")
	runRestoreRaw := runCLIJSON(t, []string{"restore"}, map[string]any{"source": destination, "destination": restoreDestination})
	var restoreManifest struct {
		SchemaVersion  int    `json:"schema_version"`
		SnapshotID     string `json:"snapshot_id"`
		BinaryVersion  string `json:"binary_version"`
		IntegrityCheck string `json:"integrity_check"`
	}
	if err := json.Unmarshal(runRestoreRaw, &restoreManifest); err != nil {
		t.Fatalf("restore response is not a manifest: %v; raw=%s", err, runRestoreRaw)
	}
	if restoreManifest.SnapshotID != backupManifest.SnapshotID || restoreManifest.IntegrityCheck != "ok" {
		t.Fatalf("restore manifest = %+v, want snapshot matching backup", restoreManifest)
	}
	if _, err := os.Stat(restoreDestination); err != nil {
		t.Fatalf("restore destination file is missing: %v", err)
	}
}

func TestRestoreRefusesLiveDatabaseDestination(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	if _, err := store.Open(context.Background(), dbPath); err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	destination := filepath.Join(backupDir, "snapshot.db")
	runCLIJSON(t, []string{"backup"}, map[string]any{"destination": destination})
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"restore"}, strings.NewReader(fmt.Sprintf(`{"source":%q,"destination":%q}`, destination, dbPath)), &out, &errOut); code != 1 {
		t.Fatalf("restore to live db exit=%d, want 1; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "live database path") {
		t.Fatalf("restore diagnostic = %q, want live database path refusal", errOut.String())
	}
}

func assertChangedRefVersion(t *testing.T, raw []byte, entityKind, id, version string) {
	t.Helper()
	if got := changedRefVersion(t, raw, entityKind, id); got != mustParseVersion(t, version) {
		t.Fatalf("changed_refs = %s, missing %s/%s version %s", raw, entityKind, id, version)
	}
}

func changedRefVersion(t *testing.T, raw []byte, entityKind, id string) int64 {
	t.Helper()
	var response struct {
		ChangedRefs []struct {
			EntityKind string `json:"entity_kind"`
			ID         string `json:"id"`
			Version    string `json:"version"`
		} `json:"changed_refs"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	for _, ref := range response.ChangedRefs {
		if ref.EntityKind == entityKind && ref.ID == id {
			version, err := strconv.ParseInt(ref.Version, 10, 64)
			if err != nil {
				t.Fatalf("changed ref version %q is not an integer", ref.Version)
			}
			return version
		}
	}
	t.Fatalf("changed_refs = %s, missing %s/%s", raw, entityKind, id)
	return 0
}

func mustParseVersion(t *testing.T, value string) int64 {
	t.Helper()
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func registerCLIClient(t *testing.T, client string, publicKey ed25519.PublicKey, productID, projectID string) {
	t.Helper()
	runCLIJSON(t, []string{"client", "register"}, map[string]any{
		"client_ref": client, "key_id": "key-1", "principal_ref": "operator-1",
		"public_key": base64.StdEncoding.EncodeToString(publicKey), "capabilities": []string{"product_read"},
		"product_scope": []string{productID}, "project_scope": []string{projectID}, "agent_scope": []string{"agent-1"},
	})
}

func runCLIJSON(t *testing.T, args []string, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput(args, bytes.NewReader(raw), &out, &errOut); code != 0 {
		t.Fatalf("%s exit=%d stderr=%q", strings.Join(args, " "), code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("%s stdout must contain exactly one JSON line, got %q", strings.Join(args, " "), out.String())
	}
	return []byte(lines[0])
}

// predecessorSyntheticFixture is a deliberately fictional two-project snapshot.
// All identifiers are obviously synthetic so the public-content validator
// cannot mistake any token for a real predecessor state name. The shape
// matches contracts/predecessor-snapshot.schema.json verbatim.
const predecessorSyntheticFixture = `{
  "schema_version": 1,
  "captured_at": "2026-08-21T14:00:00Z",
  "producer": "synthetic-harvest-v1",
  "source_system": "advance",
  "projects": [
    {
      "project_id": "synth-proj-alpha",
      "locator": "synth://alpha",
      "archived_changes": 2,
      "closed_changes": 1,
      "active_changes": [
        {
          "change_id": "synth-change-alpha-1",
          "summary": "synthetic active change alpha one",
          "status": "draft",
          "completed_gates": ["proposal"],
          "tasks_total": 4,
          "tasks_done": 1,
          "updated_at": "2026-08-21T13:00:00Z"
        },
        {
          "change_id": "synth-change-alpha-2",
          "summary": "synthetic active change alpha two",
          "status": "discovery",
          "completed_gates": [],
          "tasks_total": 0,
          "tasks_done": 0,
          "updated_at": "2026-08-21T13:30:00Z"
        }
      ],
      "wisdom_entries": [
        {
          "id": "synth-wisdom-alpha-1",
          "type": "lesson",
          "content": "synthetic alpha lesson",
          "change_id": "synth-change-alpha-1",
          "promoted": true,
          "recorded_at": "2026-08-21T13:00:00Z"
        }
      ],
      "reflections": [
        {
          "id": "synth-reflection-alpha-1",
          "change_id": "synth-change-alpha-1",
          "recorded_at": "2026-08-21T13:00:00Z",
          "friction_count": 1,
          "suggestion_count": 0
        }
      ]
    },
    {
      "project_id": "synth-proj-beta",
      "locator": "synth://beta",
      "archived_changes": 1,
      "closed_changes": 0,
      "active_changes": [
        {
          "change_id": "synth-change-beta-1",
          "summary": "synthetic active change beta one",
          "status": "draft",
          "completed_gates": [],
          "tasks_total": 2,
          "tasks_done": 0,
          "updated_at": "2026-08-21T13:45:00Z"
        }
      ],
      "wisdom_entries": [
        {
          "id": "synth-wisdom-beta-project",
          "type": "rule",
          "content": "synthetic project-level wisdom",
          "change_id": "",
          "promoted": false,
          "recorded_at": "2026-08-21T13:00:00Z"
        }
      ],
      "reflections": []
    }
  ]
}`

func writeSyntheticSnapshot(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-snapshot.json")
	if err := os.WriteFile(path, []byte(predecessorSyntheticFixture), 0o600); err != nil {
		t.Fatalf("write synthetic snapshot: %v", err)
	}
	return path
}

func TestPredecessorInventoryHappyPath(t *testing.T) {
	snapshotPath := writeSyntheticSnapshot(t)
	request, err := json.Marshal(map[string]any{"snapshot_path": snapshotPath})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"predecessor-inventory"}, bytes.NewReader(request), &out, &errOut); code != 0 {
		t.Fatalf("predecessor-inventory exit=%d stderr=%q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("predecessor-inventory stderr=%q, want empty", errOut.String())
	}
	var report struct {
		SchemaVersion int    `json:"schema_version"`
		Producer      string `json:"producer"`
		SourceSystem  string `json:"source_system"`
		CapturedAt    string `json:"captured_at"`
		Totals        struct {
			Projects        int `json:"projects"`
			ActiveChanges   int `json:"active_changes"`
			ArchivedChanges int `json:"archived_changes"`
			ClosedChanges   int `json:"closed_changes"`
			WisdomEntries   int `json:"wisdom_entries"`
			Reflections     int `json:"reflections"`
		} `json:"totals"`
		Surfaces []struct {
			Surface    string `json:"surface"`
			Inclusion  string `json:"inclusion"`
			Count      int    `json:"count"`
			CaptureGap string `json:"capture_gap"`
		} `json:"surfaces"`
		Projects []struct {
			ProjectID            string   `json:"project_id"`
			Locator              string   `json:"locator"`
			ActiveChangesCount   int      `json:"active_changes_count"`
			ArchivedChanges      int      `json:"archived_changes"`
			ClosedChanges        int      `json:"closed_changes"`
			WisdomEntriesCount   int      `json:"wisdom_entries_count"`
			ReflectionsCount     int      `json:"reflections_count"`
			ActiveChangeIDs      []string `json:"active_change_ids"`
			ActiveChangesListed  int      `json:"active_changes_listed"`
			ActiveChangesOmitted int      `json:"active_changes_omitted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("report is not parseable JSON: %v\nraw=%s", err, out.String())
	}
	if report.SchemaVersion != 1 || report.Producer != "synthetic-harvest-v1" || report.SourceSystem != "advance" {
		t.Fatalf("provenance = %+v, want schema_version=1 producer=synthetic-harvest-v1 source_system=advance", report)
	}
	if report.Totals.Projects != 2 || report.Totals.ActiveChanges != 3 || report.Totals.WisdomEntries != 2 || report.Totals.Reflections != 1 {
		t.Fatalf("totals = %+v, want projects=2 active=3 wisdom=2 reflections=1", report.Totals)
	}
	if len(report.Projects) != 2 {
		t.Fatalf("len(projects) = %d, want 2", len(report.Projects))
	}
	// The second project carries only one active change so the cap is not
	// exercised; we still verify listed/omitted bookkeeping is exact.
	beta := report.Projects[1]
	if beta.ActiveChangesCount != 1 || beta.ActiveChangesListed != 1 || beta.ActiveChangesOmitted != 0 {
		t.Fatalf("beta bookkeeping = %+v, want listed=1 omitted=0", beta)
	}
	// The preflight surfaces block names every mode surface with counts:
	// active work is included; terminal history counts archived+closed.
	if len(report.Surfaces) != 5 {
		t.Fatalf("surfaces length = %d, want 5", len(report.Surfaces))
	}
	active := report.Surfaces[1]
	if active.Surface != "active_work" || active.Inclusion != "included" || active.Count != 3 {
		t.Fatalf("active_work surface = %+v, want included count=3", active)
	}
	history := report.Surfaces[2]
	if history.Surface != "terminal_history" || history.Inclusion != "excluded" || history.Count != 4 {
		t.Fatalf("terminal_history surface = %+v, want excluded count=4", history)
	}
	specs := report.Surfaces[0]
	if specs.Surface != "specifications" || specs.CaptureGap == "" {
		t.Fatalf("specifications surface = %+v, want a named capture gap", specs)
	}
}

func TestPredecessorInventoryRejectsMissingSnapshotFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-snapshot.json")
	request, err := json.Marshal(map[string]any{"snapshot_path": missing})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"predecessor-inventory"}, bytes.NewReader(request), &out, &errOut); code != 1 {
		t.Fatalf("predecessor-inventory missing-file exit=%d, want 1; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "does not exist") {
		t.Fatalf("predecessor-inventory missing-file diagnostic = %q, want does-not-exist wording", errOut.String())
	}
}

// predecessorImportRequest builds a complete, valid `predecessor import`
// payload from the shared synthetic fixture. Tests can mutate the returned
// map before marshalling to exercise specific refusal paths.
func predecessorImportRequest(t *testing.T, snapshotPath string) map[string]any {
	t.Helper()
	return map[string]any{
		"snapshot_path": snapshotPath,
		"product": map[string]any{
			"product_id":                "synth-product",
			"display_name":              "Synthetic Product",
			"stage_maturity":            "prototype",
			"stage_audience_commitment": "operator_only",
		},
		"projects": []map[string]any{
			{"snapshot_project_id": "synth-proj-alpha", "project_id": "synth-project-alpha", "display_name": "Synthetic Alpha", "role": "primary"},
			{"snapshot_project_id": "synth-proj-beta", "project_id": "synth-project-beta", "display_name": "Synthetic Beta", "role": "secondary"},
		},
		"select_change_ids": []string{"synth-change-alpha-1", "synth-change-alpha-2"},
	}
}

func runPredecessorImportRequest(t *testing.T, dbPath string, payload map[string]any) (int, []byte, string) {
	t.Helper()
	t.Setenv(dbOverrideEnv, dbPath)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"predecessor-import"}, bytes.NewReader(raw), &out, &errOut)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var firstLine []byte
	if len(lines) >= 1 && lines[0] != "" {
		firstLine = []byte(lines[0])
	}
	return code, firstLine, errOut.String()
}

func openFreshImportStore(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPredecessorImportHappyPath(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	payload := predecessorImportRequest(t, snapshotPath)

	code, raw, diag := runPredecessorImportRequest(t, dbPath, payload)
	if code != 0 {
		t.Fatalf("predecessor-import happy exit=%d, want 0; stderr=%q", code, diag)
	}
	if len(raw) == 0 {
		t.Fatalf("predecessor-import stdout = empty; stderr=%q", diag)
	}
	var report struct {
		DryRun           bool     `json:"dry_run"`
		ProductsCreated  int      `json:"products_created"`
		ProjectsCreated  int      `json:"projects_created"`
		WorkImported     int      `json:"work_imported"`
		AlreadyImported  int      `json:"already_imported"`
		ImportedProducts []string `json:"imported_products"`
		ImportedProjects []string `json:"imported_projects"`
		Work             []struct {
			ChangeID         string   `json:"change_id"`
			WorkID           string   `json:"work_id"`
			ExternalRef      string   `json:"external_ref"`
			PredecessorPhase string   `json:"predecessor_phase"`
			CompletedGates   []string `json:"predecessor_completed_gates"`
		} `json:"work"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("report is not parseable JSON: %v; raw=%s", err, raw)
	}
	if report.ProductsCreated != 1 || report.ProjectsCreated != 2 || report.WorkImported != 2 || report.AlreadyImported != 0 {
		t.Fatalf("happy report counts = products:%d projects:%d work:%d already:%d, want 1/2/2/0", report.ProductsCreated, report.ProjectsCreated, report.WorkImported, report.AlreadyImported)
	}
	if !containsString(report.ImportedProducts, "synth-product") {
		t.Fatalf("imported_products = %v, want synth-product present", report.ImportedProducts)
	}
	if !containsString(report.ImportedProjects, "synth-project-alpha") || !containsString(report.ImportedProjects, "synth-project-beta") {
		t.Fatalf("imported_projects = %v, want both concord project ids present", report.ImportedProjects)
	}
	if len(report.Work) != 2 {
		t.Fatalf("work list = %d entries, want 2", len(report.Work))
	}

	s := openFreshImportStore(t, dbPath)
	defer s.Close()

	var productExists, projectAlphaExists int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM products WHERE id=?`, "synth-product").Scan(&productExists); err != nil {
		t.Fatal(err)
	}
	if productExists != 1 {
		t.Fatalf("products row count = %d, want 1", productExists)
	}
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM projects WHERE id IN (?, ?)`, "synth-project-alpha", "synth-project-beta").Scan(&projectAlphaExists); err != nil {
		t.Fatal(err)
	}
	if projectAlphaExists != 2 {
		t.Fatalf("projects row count = %d, want 2", projectAlphaExists)
	}

	var workCount int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM work_items WHERE id LIKE 'import-advance-work-%'`).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 2 {
		t.Fatalf("work_items row count = %d, want 2", workCount)
	}

	var actor string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT actor FROM domain_events WHERE event_id=?`, "import-advance-work-synth-change-alpha-1").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != "operator:predecessor-import" {
		t.Fatalf("actor on work event = %q, want operator:predecessor-import", actor)
	}

	var intentJSON string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT intent_json FROM work_items WHERE id=?`, "import-advance-work-synth-change-alpha-1").Scan(&intentJSON); err != nil {
		t.Fatal(err)
	}
	var intent map[string]any
	if err := json.Unmarshal([]byte(intentJSON), &intent); err != nil {
		t.Fatal(err)
	}
	if !contains(intent, "external_ref") || intent["external_ref"] != "advance:synth-change-alpha-1" {
		t.Fatalf("intent.external_ref = %v, want advance:synth-change-alpha-1", intent["external_ref"])
	}
	tags, ok := intent["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != "predecessor-migrated" {
		t.Fatalf("intent.tags = %v, want [predecessor-migrated]", intent["tags"])
	}
	if intent["priority"] != float64(3) {
		t.Fatalf("intent.priority = %v, want 3", intent["priority"])
	}
}

func TestPredecessorImportIdempotentRerun(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	payload := predecessorImportRequest(t, snapshotPath)

	firstCode, _, firstDiag := runPredecessorImportRequest(t, dbPath, payload)
	if firstCode != 0 {
		t.Fatalf("first predecessor-import exit=%d, want 0; stderr=%q", firstCode, firstDiag)
	}

	secondCode, secondRaw, secondDiag := runPredecessorImportRequest(t, dbPath, payload)
	if secondCode != 0 {
		t.Fatalf("second predecessor-import exit=%d, want 0; stderr=%q", secondCode, secondDiag)
	}
	var report struct {
		ProductsCreated int `json:"products_created"`
		ProjectsCreated int `json:"projects_created"`
		WorkImported    int `json:"work_imported"`
		AlreadyImported int `json:"already_imported"`
	}
	if err := json.Unmarshal(secondRaw, &report); err != nil {
		t.Fatalf("second report is not parseable JSON: %v; raw=%s", err, secondRaw)
	}
	// The first re-run counts the Product bootstrap (counts as 2: Product +
	// primary project), the secondary project, and both work events as
	// already_imported (5 total). The counts of new writes must all be zero.
	if report.ProductsCreated != 0 || report.ProjectsCreated != 0 || report.WorkImported != 0 || report.AlreadyImported != 5 {
		t.Fatalf("idempotent re-run counts = products:%d projects:%d work:%d already:%d, want 0/0/0/5", report.ProductsCreated, report.ProjectsCreated, report.WorkImported, report.AlreadyImported)
	}

	s := openFreshImportStore(t, dbPath)
	defer s.Close()

	var workCount int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM work_items WHERE id LIKE 'import-advance-work-%'`).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 2 {
		t.Fatalf("work_items row count = %d, want 2 (no duplicates)", workCount)
	}

	var membershipCount int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM product_projects WHERE product_id=?`, "synth-product").Scan(&membershipCount); err != nil {
		t.Fatal(err)
	}
	if membershipCount != 2 {
		t.Fatalf("product_projects membership count = %d, want 2 (no duplicates)", membershipCount)
	}
}

func TestPredecessorImportRefusesPartialProductOnMembershipDivergence(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)

	firstPayload := predecessorImportRequest(t, snapshotPath)
	firstCode, _, firstDiag := runPredecessorImportRequest(t, dbPath, firstPayload)
	if firstCode != 0 {
		t.Fatalf("baseline import exit=%d, want 0; stderr=%q", firstCode, firstDiag)
	}

	// Second run declares an extra project. The existing Product membership
	// will not match, so the import must refuse.
	divergentPayload := map[string]any{
		"snapshot_path": snapshotPath,
		"product": map[string]any{
			"product_id":                "synth-product",
			"display_name":              "Synthetic Product",
			"stage_maturity":            "prototype",
			"stage_audience_commitment": "operator_only",
		},
		"projects": []map[string]any{
			{"snapshot_project_id": "synth-proj-alpha", "project_id": "synth-project-alpha", "display_name": "Synthetic Alpha", "role": "primary"},
			{"snapshot_project_id": "synth-proj-beta", "project_id": "synth-project-beta", "display_name": "Synthetic Beta", "role": "secondary"},
			{"snapshot_project_id": "synth-proj-alpha", "project_id": "synth-project-newcomer", "display_name": "Synthetic Newcomer", "role": "secondary"},
		},
		"select_change_ids": []string{"synth-change-alpha-1"},
	}

	code, _, diag := runPredecessorImportRequest(t, dbPath, divergentPayload)
	if code != 1 {
		t.Fatalf("partial-Product divergent import exit=%d, want 1; stderr=%q", code, diag)
	}
	if !strings.Contains(diag, "partial-Product import is refused") {
		t.Fatalf("partial-Product diagnostic = %q, want partial-Product wording", diag)
	}

	s := openFreshImportStore(t, dbPath)
	defer s.Close()

	var newcomer int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM projects WHERE id=?`, "synth-project-newcomer").Scan(&newcomer); err != nil {
		t.Fatal(err)
	}
	if newcomer != 0 {
		t.Fatalf("divergent newcomer project was created: count = %d, want 0", newcomer)
	}
}

func TestPredecessorImportSelectionRefusals(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(map[string]any)
		wantSubstr string
	}{
		{
			name: "unknown_change_id",
			mutate: func(payload map[string]any) {
				payload["select_change_ids"] = []string{"synth-does-not-exist"}
			},
			wantSubstr: "is not an active change",
		},
		{
			name: "undeclared_project_change",
			mutate: func(payload map[string]any) {
				// The beta project's active change is synth-change-beta-1.
				// Declaring it without declaring synth-proj-beta is the
				// undeclared-project refusal.
				payload["projects"] = []map[string]any{
					{"snapshot_project_id": "synth-proj-alpha", "project_id": "synth-project-alpha", "display_name": "Synthetic Alpha", "role": "primary"},
				}
				payload["select_change_ids"] = []string{"synth-change-beta-1"}
			},
			wantSubstr: "is not declared in projects",
		},
		{
			name: "terminal_phase_change",
			mutate: func(payload map[string]any) {
				// Hand-craft a snapshot whose change is in a terminal phase.
				terminalSnapshot := strings.Replace(predecessorSyntheticFixture, `"status": "draft"`, `"status": "released"`, 1)
				path := filepath.Join(t.TempDir(), "terminal-snapshot.json")
				if err := os.WriteFile(path, []byte(terminalSnapshot), 0o600); err != nil {
					t.Fatal(err)
				}
				payload["snapshot_path"] = path
			},
			wantSubstr: "terminal phase",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := freshMigratedCLIDatabase(t)
			snapshotPath := writeSyntheticSnapshot(t)
			payload := predecessorImportRequest(t, snapshotPath)
			tc.mutate(payload)
			code, _, diag := runPredecessorImportRequest(t, dbPath, payload)
			if code != 1 {
				t.Fatalf("selection refusal exit=%d, want 1; stderr=%q", code, diag)
			}
			if !strings.Contains(diag, tc.wantSubstr) {
				t.Fatalf("selection refusal diagnostic = %q, want substring %q", diag, tc.wantSubstr)
			}
		})
	}
}

func TestPredecessorImportDryRunDoesNotWrite(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	payload := predecessorImportRequest(t, snapshotPath)
	payload["dry_run"] = true

	code, raw, diag := runPredecessorImportRequest(t, dbPath, payload)
	if code != 0 {
		t.Fatalf("dry-run exit=%d, want 0; stderr=%q", code, diag)
	}
	var report struct {
		DryRun          bool `json:"dry_run"`
		ProductsCreated int  `json:"products_created"`
		ProjectsCreated int  `json:"projects_created"`
		WorkImported    int  `json:"work_imported"`
		AlreadyImported int  `json:"already_imported"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("dry-run report is not parseable JSON: %v; raw=%s", err, raw)
	}
	if !report.DryRun {
		t.Fatalf("dry_run flag = false, want true")
	}
	if report.ProductsCreated != 0 || report.ProjectsCreated != 0 || report.WorkImported != 0 {
		t.Fatalf("dry-run write counts = products:%d projects:%d work:%d, want 0/0/0", report.ProductsCreated, report.ProjectsCreated, report.WorkImported)
	}

	s := openFreshImportStore(t, dbPath)
	defer s.Close()
	var productCount, projectCount, workCount int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM products`).Scan(&productCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM projects`).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM work_items`).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if productCount != 0 || projectCount != 0 || workCount != 0 {
		t.Fatalf("dry_run wrote: products=%d projects=%d work=%d, want 0/0/0", productCount, projectCount, workCount)
	}
}

func TestPredecessorImportTruncatesWALAfterSyncDurable(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	payload := predecessorImportRequest(t, snapshotPath)
	code, _, diag := runPredecessorImportRequest(t, dbPath, payload)
	if code != 0 {
		t.Fatalf("import exit=%d, want 0; stderr=%q", code, diag)
	}
	// SQLite deletes the WAL sidecar entirely when the last connection
	// closes after a TRUNCATE checkpoint left it at zero length. Either
	// observation proves the durability barrier ran: an absent file means
	// the barrier reset and the file was unlinked; a present zero-byte
	// file means the barrier truncated but kept the sidecar.
	walPath := dbPath + "-wal"
	info, err := os.Stat(walPath)
	if err == nil && info.Size() != 0 {
		t.Fatalf("WAL file %s size = %d after import, want 0 (TRUNCATE did not reset)", walPath, info.Size())
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestPredecessorImportSurfacesRefusals pins CD-0097's surface contract at the
// operator boundary: a surface outside the mode's five-surface set refuses
// before import, and a mode surface that migrates by its own route refuses
// with that route. Only active_work imports.
func TestPredecessorImportSurfacesRefusals(t *testing.T) {
	cases := []struct {
		name       string
		surfaces   any
		wantSubstr string
	}{
		{
			name:       "surface_outside_mode",
			surfaces:   []string{"sessions"},
			wantSubstr: "is outside the parallel migration mode",
		},
		{
			name:       "wisdom_routes_to_curation",
			surfaces:   []string{"active_work", "wisdom"},
			wantSubstr: "does not import",
		},
		{
			name:       "terminal_history_routes_to_snapshot",
			surfaces:   []string{"terminal_history"},
			wantSubstr: "does not import",
		},
		{
			name:       "specifications_route_to_knowledge",
			surfaces:   []string{"specifications"},
			wantSubstr: "does not import",
		},
		{
			name:       "reflections_route_to_research",
			surfaces:   []string{"reflections"},
			wantSubstr: "does not import",
		},
		{
			name:       "duplicate_surface",
			surfaces:   []string{"active_work", "active_work"},
			wantSubstr: "duplicates an earlier entry",
		},
		{
			name:       "empty_surfaces_list",
			surfaces:   []string{},
			wantSubstr: "must name at least one surface",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := freshMigratedCLIDatabase(t)
			snapshotPath := writeSyntheticSnapshot(t)
			payload := predecessorImportRequest(t, snapshotPath)
			payload["surfaces"] = tc.surfaces
			code, _, diag := runPredecessorImportRequest(t, dbPath, payload)
			if code != 1 {
				t.Fatalf("surfaces refusal exit=%d, want 1; stderr=%q", code, diag)
			}
			if !strings.Contains(diag, tc.wantSubstr) {
				t.Fatalf("surfaces refusal diagnostic = %q, want substring %q", diag, tc.wantSubstr)
			}
			if workCount(t, dbPath) != 0 {
				t.Fatal("refused surfaces request wrote work items")
			}
		})
	}
}

// TestPredecessorImportAcceptsExplicitActiveWorkSurface pins that the only
// importable mode surface names explicitly and imports normally.
func TestPredecessorImportAcceptsExplicitActiveWorkSurface(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	payload := predecessorImportRequest(t, snapshotPath)
	payload["surfaces"] = []string{"active_work"}
	code, raw, diag := runPredecessorImportRequest(t, dbPath, payload)
	if code != 0 {
		t.Fatalf("explicit active_work exit=%d, want 0; stderr=%q", code, diag)
	}
	var report struct {
		WorkImported int `json:"work_imported"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("report is not parseable JSON: %v; raw=%s", err, raw)
	}
	if report.WorkImported != 2 {
		t.Fatalf("work_imported = %d, want 2", report.WorkImported)
	}
}

// predecessorWorkReport is the report slice shape the repeated-run tests read.
type predecessorWorkReport struct {
	ChangeID         string   `json:"change_id"`
	WorkID           string   `json:"work_id"`
	ExternalRef      string   `json:"external_ref"`
	Status           string   `json:"status"`
	PredecessorPhase string   `json:"predecessor_phase"`
	CompletedGates   []string `json:"predecessor_completed_gates"`
}

// TestPredecessorImportRepeatedRunReturnsTypedNoOp pins the CD-0097 D4 replay
// contract: a repeated migration run with an unchanged harvest returns a
// typed no-op report, and provenance and stable identity carry across runs.
func TestPredecessorImportRepeatedRunReturnsTypedNoOp(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	payload := predecessorImportRequest(t, snapshotPath)

	firstCode, firstRaw, firstDiag := runPredecessorImportRequest(t, dbPath, payload)
	if firstCode != 0 {
		t.Fatalf("first import exit=%d, want 0; stderr=%q", firstCode, firstDiag)
	}
	var firstReport struct {
		NoOp bool                    `json:"no_op"`
		Work []predecessorWorkReport `json:"work"`
	}
	if err := json.Unmarshal(firstRaw, &firstReport); err != nil {
		t.Fatalf("first report is not parseable JSON: %v; raw=%s", err, firstRaw)
	}
	if firstReport.NoOp {
		t.Fatal("first import reported no_op, want false")
	}
	for _, item := range firstReport.Work {
		if item.Status != "imported" {
			t.Fatalf("first import status for %s = %q, want imported", item.ChangeID, item.Status)
		}
	}

	secondCode, secondRaw, secondDiag := runPredecessorImportRequest(t, dbPath, payload)
	if secondCode != 0 {
		t.Fatalf("repeated import exit=%d, want 0; stderr=%q", secondCode, secondDiag)
	}
	var secondReport struct {
		NoOp            bool                    `json:"no_op"`
		ProductsCreated int                     `json:"products_created"`
		ProjectsCreated int                     `json:"projects_created"`
		WorkImported    int                     `json:"work_imported"`
		AlreadyImported int                     `json:"already_imported"`
		Work            []predecessorWorkReport `json:"work"`
	}
	if err := json.Unmarshal(secondRaw, &secondReport); err != nil {
		t.Fatalf("second report is not parseable JSON: %v; raw=%s", err, secondRaw)
	}
	if !secondReport.NoOp {
		t.Fatal("repeated import no_op = false, want true")
	}
	if secondReport.ProductsCreated != 0 || secondReport.ProjectsCreated != 0 || secondReport.WorkImported != 0 {
		t.Fatalf("repeated import wrote: products:%d projects:%d work:%d, want 0/0/0",
			secondReport.ProductsCreated, secondReport.ProjectsCreated, secondReport.WorkImported)
	}
	if secondReport.AlreadyImported != 5 {
		t.Fatalf("repeated import already_imported = %d, want 5", secondReport.AlreadyImported)
	}
	if len(secondReport.Work) != 2 {
		t.Fatalf("repeated import work list = %d, want 2", len(secondReport.Work))
	}
	// Stable identity and provenance: the same change ids resolve to the same
	// work ids and external refs as the first run.
	for i := range firstReport.Work {
		first, second := firstReport.Work[i], secondReport.Work[i]
		if first.ChangeID != second.ChangeID || first.WorkID != second.WorkID || first.ExternalRef != second.ExternalRef {
			t.Fatalf("identity drifted across runs: first=%+v second=%+v", first, second)
		}
		if second.Status != "already_imported" {
			t.Fatalf("repeated import status for %s = %q, want already_imported", second.ChangeID, second.Status)
		}
	}
}

// TestPredecessorImportHarvestDriftToleratesTerminalAndAbsentChanges pins the
// parallel mode's repeated-harvest behavior: a selected change the predecessor
// moved to a terminal phase, or removed from the active set, since the last
// harvest is an idempotent no-op when its import already exists, while the
// same drift on a first run keeps the typed refusal.
func TestPredecessorImportHarvestDriftToleratesTerminalAndAbsentChanges(t *testing.T) {
	terminalSnapshot := strings.Replace(predecessorSyntheticFixture, `"status": "discovery"`, `"status": "released"`, 1)
	absentSnapshot := strings.Replace(
		predecessorSyntheticFixture,
		`        },
        {
          "change_id": "synth-change-alpha-2",
          "summary": "synthetic active change alpha two",
          "status": "discovery",
          "completed_gates": [],
          "tasks_total": 0,
          "tasks_done": 0,
          "updated_at": "2026-08-21T13:30:00Z"
        }
      ],
`,
		`        }
      ],
`, 1)

	for _, tc := range []struct {
		name     string
		snapshot string
		phase    string
	}{
		{name: "terminal_since_last_harvest", snapshot: terminalSnapshot, phase: "released"},
		{name: "absent_since_last_harvest", snapshot: absentSnapshot, phase: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := freshMigratedCLIDatabase(t)
			snapshotPath := writeSyntheticSnapshot(t)
			payload := predecessorImportRequest(t, snapshotPath)
			if code, _, diag := runPredecessorImportRequest(t, dbPath, payload); code != 0 {
				t.Fatalf("baseline import exit=%d, want 0; stderr=%q", code, diag)
			}

			driftPath := filepath.Join(t.TempDir(), "drift-snapshot.json")
			if err := os.WriteFile(driftPath, []byte(tc.snapshot), 0o600); err != nil {
				t.Fatal(err)
			}
			driftPayload := predecessorImportRequest(t, driftPath)
			code, raw, diag := runPredecessorImportRequest(t, dbPath, driftPayload)
			if code != 0 {
				t.Fatalf("drifted re-import exit=%d, want 0; stderr=%q", code, diag)
			}
			var report struct {
				NoOp            bool                    `json:"no_op"`
				WorkImported    int                     `json:"work_imported"`
				AlreadyImported int                     `json:"already_imported"`
				Work            []predecessorWorkReport `json:"work"`
			}
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatalf("drifted report is not parseable JSON: %v; raw=%s", err, raw)
			}
			if !report.NoOp || report.WorkImported != 0 {
				t.Fatalf("drifted re-import no_op=%v work_imported=%d, want true/0", report.NoOp, report.WorkImported)
			}
			// 2 (product bootstrap) + 1 (secondary project) + 1 (alpha-1
			// unchanged) + 1 (alpha-2 drifted but already imported).
			if report.AlreadyImported != 5 {
				t.Fatalf("drifted re-import already_imported = %d, want 5", report.AlreadyImported)
			}
			if len(report.Work) != 2 {
				t.Fatalf("drifted re-import work list = %d, want 2", len(report.Work))
			}
			for _, item := range report.Work {
				if item.Status != "already_imported" {
					t.Fatalf("drifted re-import status for %s = %q, want already_imported", item.ChangeID, item.Status)
				}
				if item.ChangeID == "synth-change-alpha-2" && item.PredecessorPhase != tc.phase {
					t.Fatalf("alpha-2 phase = %q, want %q", item.PredecessorPhase, tc.phase)
				}
			}

			// The same drift against a store without the import keeps the
			// first-run refusal: terminal and absent changes are not active
			// work.
			freshDB := freshMigratedCLIDatabase(t)
			freshPayload := predecessorImportRequest(t, driftPath)
			refusalCode, _, refusalDiag := runPredecessorImportRequest(t, freshDB, freshPayload)
			if refusalCode != 1 {
				t.Fatalf("first-run drift exit=%d, want 1; stderr=%q", refusalCode, refusalDiag)
			}
			if !strings.Contains(refusalDiag, "synth-change-alpha-2") {
				t.Fatalf("first-run drift diagnostic = %q, want alpha-2 named", refusalDiag)
			}
		})
	}
}

// TestPredecessorImportConflictRefusalNamesOwningSource pins CD-0097 D4: a
// Concord-side change to an imported item the predecessor still owns refuses
// the next migration run before any write, naming the owning source, so no
// side is silently overwritten.
func TestPredecessorImportConflictRefusalNamesOwningSource(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	snapshotPath := writeSyntheticSnapshot(t)
	payload := predecessorImportRequest(t, snapshotPath)
	if code, _, diag := runPredecessorImportRequest(t, dbPath, payload); code != 0 {
		t.Fatalf("baseline import exit=%d, want 0; stderr=%q", code, diag)
	}

	// A Concord-side change: an operator revision of the imported intent.
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := json.Marshal(map[string]any{
		"title": "re-contracted on the Concord side", "value_statement": "re-contracted", "kind": "task",
		"priority": 3, "tags": []string{"predecessor-migrated"}, "reason": "operator re-contract",
		"expected_version": 2, "resulting_version": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	txErr := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, applyErr := store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{{
			EventID:        "operator-revision-synth-change-alpha-1",
			Kind:           "work.intent_revised",
			SubjectType:    store.SubjectWorkItem,
			SubjectID:      "import-advance-work-synth-change-alpha-1",
			Actor:          "operator",
			OccurredAt:     s.Now(),
			PayloadVersion: 1,
			Payload:        revised,
		}}})
		return applyErr
	})
	if txErr != nil {
		t.Fatalf("append Concord-side revision: %v", txErr)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	code, _, diag := runPredecessorImportRequest(t, dbPath, payload)
	if code != 1 {
		t.Fatalf("conflict import exit=%d, want 1; stderr=%q", code, diag)
	}
	for _, want := range []string{"conflict", "advance:synth-change-alpha-1", "work.intent_revised", "operator"} {
		if !strings.Contains(diag, want) {
			t.Fatalf("conflict diagnostic = %q, want substring %q", diag, want)
		}
	}

	// The refusal fired before any write: no third work item, and the
	// conflict probe is durable across repeated attempts.
	if got := workCount(t, dbPath); got != 2 {
		t.Fatalf("work items after conflict refusal = %d, want 2", got)
	}
	againCode, _, againDiag := runPredecessorImportRequest(t, dbPath, payload)
	if againCode != 1 || !strings.Contains(againDiag, "advance:synth-change-alpha-1") {
		t.Fatalf("repeat conflict exit=%d diag=%q, want refusal naming the owning source", againCode, againDiag)
	}
}

// workCount counts imported work items in the store at dbPath.
func workCount(t *testing.T, dbPath string) int {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT count(*) FROM work_items WHERE id LIKE 'import-advance-work-%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func contains(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

// TestClientPolicyExpandCLIPreservesEveryExistingGrant pins CD-0097 D6 at the
// operator boundary: the expand verb widens a trusted client policy by union,
// both accepted command forms work, replay through the two-word form is
// idempotent, and the stored row keeps every prior grant and the principal.
func TestClientPolicyExpandCLIPreservesEveryExistingGrant(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	publicKey, _, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runOperatorJSON(t, dbPath, []string{"client-register"}, map[string]any{
		"client_ref": "client-1", "key_id": "key-1", "principal_ref": "human-1",
		"public_key":    base64.StdEncoding.EncodeToString(publicKey),
		"capabilities":  []string{"product_read", "work_define"},
		"product_scope": []string{"product-1"},
		"project_scope": []string{"project-1", "project-2"},
		"agent_scope":   []string{"agent-1"},
	})
	runOperatorJSON(t, dbPath, []string{"client-policy-expand"}, map[string]any{
		"client_ref":    "client-1",
		"capabilities":  []string{"cross_scope"},
		"product_scope": []string{"pokeedge"},
		"project_scope": []string{"pokeedge-main"},
	})
	runOperatorJSON(t, dbPath, []string{"client", "policy-expand"}, map[string]any{
		"client_ref":    "client-1",
		"product_scope": []string{"pokeedge"},
	})
	// The agent scope is a policy dimension like the others, so an expansion
	// must carry it. A command spec that names the field while the handler
	// drops it would grant an agent the client still cannot present.
	runOperatorJSON(t, dbPath, []string{"client-policy-expand"}, map[string]any{
		"client_ref":  "client-1",
		"agent_scope": []string{"agent-2"},
	})

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var capabilitiesJSON, productsJSON, projectsJSON, agentsJSON, principal string
	row := s.DatabaseForTesting().QueryRow(`SELECT capabilities_json,product_scope_json,project_scope_json,agent_scope_json,principal_ref FROM agent_clients WHERE client_ref=?`, "client-1")
	if err := row.Scan(&capabilitiesJSON, &productsJSON, &projectsJSON, &agentsJSON, &principal); err != nil {
		t.Fatal(err)
	}
	if agentsJSON != `["agent-1","agent-2"]` {
		t.Fatalf("agent scope=%s", agentsJSON)
	}
	var capabilities, products, projects []string
	for _, decode := range []struct {
		encoded string
		target  *[]string
	}{
		{capabilitiesJSON, &capabilities},
		{productsJSON, &products},
		{projectsJSON, &projects},
	} {
		if err := json.Unmarshal([]byte(decode.encoded), decode.target); err != nil {
			t.Fatalf("decode %q: %v", decode.encoded, err)
		}
	}
	assertCLIStringSet := func(label string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want exactly %v", label, got, want)
		}
		for _, value := range want {
			found := false
			for _, candidate := range got {
				if candidate == value {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s = %v, want exactly %v", label, got, want)
			}
		}
	}
	assertCLIStringSet("capabilities", capabilities, "product_read", "work_define", "cross_scope")
	assertCLIStringSet("product scope", products, "product-1", "pokeedge")
	assertCLIStringSet("project scope", projects, "project-1", "project-2", "pokeedge-main")
	if principal != "human-1" {
		t.Fatalf("principal = %q, want the stored principal human-1", principal)
	}

	var out, errOut bytes.Buffer
	t.Setenv(dbOverrideEnv, dbPath)
	if code := runWithInput([]string{"client-policy-expand"}, strings.NewReader(`{"client_ref":"client-1","capabilities":["root_everything"]}`), &out, &errOut); code == 0 {
		t.Fatal("unknown capability was accepted")
	}
	if !strings.HasPrefix(errOut.String(), "concord client-policy-expand: ") {
		t.Fatalf("diagnostic = %q, want the command-prefixed refusal", errOut.String())
	}
}
