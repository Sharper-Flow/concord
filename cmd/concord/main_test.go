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
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

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

func TestHelpListsSessionVacateOperation(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("help exit code=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "concord_work_transition.session_vacate") {
		t.Fatalf("help does not list the session vacate operation")
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

func durableCounts(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"effect_receipts", "idempotency_records", "durable_operations"} {
		var count int
		if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
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
		"concord project-locator-add < JSON stdin",
		"concord project locator-add < JSON stdin",
		"concord invoke < JSON stdin",
		"required: client_ref, key_id, principal_ref, public_key, capabilities, product_scope, project_scope",
		"required: product_id, display_name, stage_maturity, stage_audience_commitment, project_id, project_display_name, role",
		"stage_maturity: prototype | alpha | beta | production | deprecated",
		"stage_audience_commitment: operator_only | limited | public",
		"kind: canonical_path | git_remote",
		"capabilities: product_read | work_define | work_transition | work_relate | work_compact | cross_scope",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output missing %q", want)
		}
	}
	if strings.Contains(out.String(), "work_initiative") {
		t.Fatal("help still advertises the retired work_initiative capability")
	}
	if errOut.Len() != 0 {
		t.Fatalf("help stderr = %q, want empty", errOut.String())
	}
	if err := commandHelpSectionsError(out.String(), commandSpecs); err != nil {
		t.Fatal(err)
	}
}

// commandHelpSectionsError requires exactly the forms and JSON-stdin shapes
// commandSpecs declares, in order, with no repeated or unexplained output.
// Documentation growth is bounded by the declarations, not a fixed byte budget.
func commandHelpSectionsError(help string, specs []commandSpec) error {
	const heading = "Commands read one strict JSON object from stdin:\n"
	if strings.Count(help, heading) != 1 {
		return fmt.Errorf("help must contain exactly one JSON-stdin command heading")
	}
	_, sections, _ := strings.Cut(help, heading)
	var want []string
	for _, spec := range specs {
		want = append(want, "  concord "+spec.Canonical+" < JSON stdin")
		if spec.TwoWord != "" {
			want = append(want, "  concord "+spec.TwoWord+" < JSON stdin")
		}
		want = append(want, "    required: "+formatRequiredFields(spec.RequiredFields))
		if spec.Optional != "" && spec.Optional != "none" {
			want = append(want, "    optional: "+spec.Optional)
		}
		if spec.Enums != "" && spec.Enums != "none" {
			want = append(want, "    accepted values: "+spec.Enums)
		}
	}
	if sections != strings.Join(want, "\n")+"\n" {
		return fmt.Errorf("help command sections differ from commandSpecs: missing, repeated, reordered, or unexpected documentation")
	}
	return nil
}

func TestCommandHelpSectionsFollowDeclaredDocumentation(t *testing.T) {
	spec := commandSpec{
		Canonical:      "fixture-command",
		TwoWord:        "fixture command",
		RequiredFields: requiredFields(nestedField("payload", "value")),
		Optional:       strings.Repeat("declared documentation; ", 800),
		Enums:          "value: first | second",
	}
	var section bytes.Buffer
	writeCommandSection(&section, spec)
	body := section.String()
	heading := "Commands read one strict JSON object from stdin:\n"
	valid := heading + body
	for _, tc := range []struct {
		name string
		help string
		want bool
	}{
		{"declared growth", valid, true},
		{"duplicate section", valid + body, false},
		{"duplicate heading", heading + valid, false},
		{"missing heading", body, false},
		{"missing canonical form", strings.Replace(valid, "  concord fixture-command < JSON stdin\n", "", 1), false},
		{"missing two-word form", strings.Replace(valid, "  concord fixture command < JSON stdin\n", "", 1), false},
		{"changed required shape", strings.Replace(valid, "payload{value}", "payload", 1), false},
		{"missing optional documentation", strings.Replace(valid, "    optional: "+spec.Optional+"\n", "", 1), false},
		{"missing accepted values", strings.Replace(valid, "    accepted values: "+spec.Enums+"\n", "", 1), false},
		{"unexpected trailing output", valid + "unexplained output\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := commandHelpSectionsError(tc.help, []commandSpec{spec}); (err == nil) != tc.want {
				t.Fatalf("help admission error = %v, want admitted %v", err, tc.want)
			}
		})
	}
}

// countingStdin records every Read so a help route can prove it never
// consumes stdin.
type countingStdin struct{ reads int }

func (r *countingStdin) Read([]byte) (int, error) {
	r.reads++
	return 0, fmt.Errorf("stdin read attempted")
}

func topLevelHelp(t *testing.T) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("--help exit code = %d, want 0; stderr=%q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("--help stderr = %q, want empty", errOut.String())
	}
	return out.String()
}

// topLevelCommandSection returns the byte-exact section of the top-level
// usage that documents a canonical command name.
func topLevelCommandSection(t *testing.T, topLevel, canonical string) string {
	t.Helper()
	line := "  concord " + canonical + " < JSON stdin\n"
	start := strings.Index(topLevel, line)
	if start < 0 {
		t.Fatalf("top-level usage has no section for %s", canonical)
	}
	rest := topLevel[start+len(line):]
	end := strings.Index(rest, "\n  concord ")
	if end < 0 {
		return topLevel[start:]
	}
	return topLevel[start : start+len(line)+end+1]
}

// topLevelVerbLines returns the byte-exact usage lines that the top-level
// help's verb list gives one verb outside the JSON-stdin command sections.
func topLevelVerbLines(t *testing.T, topLevel, verb string) string {
	t.Helper()
	preamble, _, _ := strings.Cut(topLevel, "\nRequired-field refusals")
	var lines strings.Builder
	for _, line := range strings.Split(preamble, "\n") {
		if strings.HasPrefix(line, "  concord "+verb+" ") {
			lines.WriteString(line + "\n")
		}
	}
	if lines.Len() == 0 {
		t.Fatalf("top-level usage lists no line for %s", verb)
	}
	return lines.String()
}

// topLevelGroupSections returns the sections of every command whose two-word
// form starts with the group word, in top-level order.
func topLevelGroupSections(t *testing.T, topLevel, group string) string {
	t.Helper()
	var sections strings.Builder
	for _, spec := range commandSpecs {
		if strings.HasPrefix(spec.TwoWord, group+" ") {
			sections.WriteString(topLevelCommandSection(t, topLevel, spec.Canonical))
		}
	}
	if sections.Len() == 0 {
		t.Fatalf("group %s has no commands in commandSpecs", group)
	}
	return sections.String()
}

// TestEveryVerbHasAHelpRoute proves that every verb the top-level usage names,
// every command form, and every group word answer
// --help and -h with their own usage on stdout, exit 0, and never read stdin
// or open the store.
func TestEveryVerbHasAHelpRoute(t *testing.T) {
	topLevel := topLevelHelp(t)
	type verbCase struct {
		name string
		args []string
		want string
	}
	cases := []verbCase{{name: "concord", want: topLevel}}
	for _, spec := range commandSpecs {
		for _, form := range []string{spec.Canonical, spec.TwoWord} {
			if form != "" {
				cases = append(cases, verbCase{name: form, args: strings.Fields(form), want: topLevelCommandSection(t, topLevel, spec.Canonical)})
			}
		}
	}
	for _, group := range commandGroups() {
		cases = append(cases, verbCase{name: group, args: []string{group}, want: topLevelGroupSections(t, topLevel, group)})
	}
	preamble, _, _ := strings.Cut(topLevel, "\nRequired-field refusals")
	var verbs []string
	for _, line := range strings.Split(preamble, "\n") {
		verb, _, _ := strings.Cut(strings.TrimPrefix(line, "  concord "), " ")
		if !strings.HasPrefix(line, "  concord ") || strings.HasPrefix(verb, "-") || slices.Contains(verbs, verb) || slices.ContainsFunc(commandSpecs, func(spec commandSpec) bool { return spec.Canonical == verb }) {
			continue
		}
		verbs = append(verbs, verb)
		cases = append(cases, verbCase{name: verb, args: []string{verb}, want: topLevelVerbLines(t, topLevel, verb)})
	}
	for _, want := range []string{"continuity-block"} {
		if !slices.Contains(verbs, want) {
			t.Fatalf("top-level verbs outside commandSpecs = %v, missing %q", verbs, want)
		}
	}
	for _, tc := range cases {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(tc.name+" "+flag, func(t *testing.T) {
				dbPath := filepath.Join(t.TempDir(), "concord.db")
				t.Setenv(dbOverrideEnv, dbPath)
				var out, errOut bytes.Buffer
				stdin := &countingStdin{}
				if code := runWithInput(append(slices.Clone(tc.args), flag), stdin, &out, &errOut); code != 0 {
					t.Fatalf("exit code = %d, want 0; stderr=%q", code, errOut.String())
				}
				if errOut.Len() != 0 {
					t.Fatalf("stderr = %q, want empty", errOut.String())
				}
				if stdin.reads != 0 {
					t.Fatalf("read stdin %d times, want 0", stdin.reads)
				}
				if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
					t.Fatalf("touched the store database at %s", dbPath)
				}
				if out.String() != tc.want {
					t.Fatalf("stdout = %q, want %q", out.String(), tc.want)
				}
			})
		}
	}
}

func TestGroupHelp(t *testing.T) {
	topLevel := topLevelHelp(t)
	groups := commandGroups()
	for _, want := range []string{"client", "product", "domain", "project"} {
		if !slices.Contains(groups, want) {
			t.Fatalf("commandGroups() = %v, missing %q", groups, want)
		}
	}
	for _, group := range groups {
		t.Run(group, func(t *testing.T) {
			wantSections := topLevelGroupSections(t, topLevel, group)

			var out, errOut bytes.Buffer
			stdin := &countingStdin{}
			if code := runWithInput([]string{group}, stdin, &out, &errOut); code != 2 {
				t.Fatalf("bare %q exit code = %d, want 2", group, code)
			}
			if out.Len() != 0 {
				t.Fatalf("bare %q stdout = %q, want empty", group, out.String())
			}
			if !strings.Contains(errOut.String(), fmt.Sprintf("concord: %s names a command group, not a command", group)) {
				t.Fatalf("bare %q stderr lacks the group diagnostic: %q", group, errOut.String())
			}
			if !strings.Contains(errOut.String(), wantSections) {
				t.Fatalf("bare %q stderr lacks the group sections", group)
			}
			if stdin.reads != 0 {
				t.Fatalf("bare %q read stdin %d times, want 0", group, stdin.reads)
			}

			for _, args := range [][]string{{group, "bogus"}, {group, "bogus", "--help"}} {
				out.Reset()
				errOut.Reset()
				if code := runWithInput(args, stdin, &out, &errOut); code != 2 {
					t.Fatalf("%v exit code = %d, want 2", args, code)
				}
				if out.Len() != 0 {
					t.Fatalf("%v stdout = %q, want empty", args, out.String())
				}
				diagnostic := fmt.Sprintf("concord: %s has no command bogus; use 'concord %s --help' to list its commands\n", group, group)
				if errOut.String() != diagnostic+wantSections {
					t.Fatalf("%v stderr = %q, want the group diagnostic and its sections", args, errOut.String())
				}
				if stdin.reads != 0 {
					t.Fatalf("%v read stdin %d times, want 0", args, stdin.reads)
				}
			}
		})
	}
}

func TestScopedUnsupportedUsage(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		canonical string
	}{
		{"canonical", []string{"backup", "--bogus"}, "backup"},
		{"two-word", []string{"client", "register", "extra"}, "client-register"},
		{"release-verb", []string{"host-lease", "--bogus"}, "host-lease"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topLevel := topLevelHelp(t)
			want := topLevelCommandSection(t, topLevel, tc.canonical)
			t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
			var out, errOut bytes.Buffer
			if code := runWithInput(tc.args, &countingStdin{}, &out, &errOut); code != 2 {
				t.Fatalf("%v exit code = %d, want 2", tc.args, code)
			}
			if out.Len() != 0 {
				t.Fatalf("%v stdout = %q, want empty", tc.args, out.String())
			}
			if !strings.Contains(errOut.String(), "unsupported arguments") {
				t.Fatalf("%v stderr lacks the diagnostic: %q", tc.args, errOut.String())
			}
			if !strings.Contains(errOut.String(), want) {
				t.Fatalf("%v stderr lacks the command's own section", tc.args)
			}
			if strings.Contains(errOut.String(), "Usage:") {
				t.Fatalf("%v stderr still prints the full usage", tc.args)
			}
		})
	}
}

func TestTopLevelHelpUnchanged(t *testing.T) {
	got := topLevelHelp(t)
	path := filepath.Join("testdata", "top-level-help.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("concord --help differs from %s; regenerate with UPDATE_GOLDEN=1 only for an approved usage change", path)
	}
}

func TestInvokeHelpOmitsNativeVerification(t *testing.T) {
	var out, errOut bytes.Buffer
	in := &countingStdin{}
	if code := runWithInput([]string{"invoke", "--help"}, in, &out, &errOut); code != 0 {
		t.Fatalf("help: %d %s", code, errOut.String())
	}
	for _, text := range []string{"concord_work_transition.worktree_verify", "command OR oracle", "phase prepare | execute", "oracle_output"} {
		if strings.Contains(out.String(), text) {
			t.Errorf("invoke help retains %q", text)
		}
	}
	if in.reads != 0 {
		t.Fatal("invoke help consumed JSON input")
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
	if err := s2.DatabaseForTesting().QueryRow(`SELECT kind FROM effect_receipts WHERE kind='product.stage_changed' AND subject_id='stage-update-product'`).Scan(&gotKind); err != nil {
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
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
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
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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
		{"project-create"}, {"project", "create"},
		{"product-project-add"}, {"product", "project-add"},
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

func TestRetiredPlanningCommandsRefuseBeforeStdinOrStore(t *testing.T) {
	help := topLevelHelp(t)
	for _, command := range []string{
		"product-mode-set",
		"linear-health",
		"linear-divergence",
		"linear-unlinked-remote-in-progress",
		"linear-issue-enqueue",
		"linear-outbox-drain",
		"linear-outbox-disposition",
		"linear-backfill",
		"linear-connection-update",
		"linear-initiative-import",
	} {
		for _, args := range [][]string{{command}, strings.SplitN(command, "-", 2)} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				if strings.Contains(help, "  concord "+strings.Join(args, " ")+" < JSON stdin") {
					t.Fatal("help still advertises the retired planning command")
				}
				dbPath := filepath.Join(t.TempDir(), "concord.db")
				t.Setenv(dbOverrideEnv, dbPath)
				stdin := &countingStdin{}
				var out, errOut bytes.Buffer
				if code := runWithInput(args, stdin, &out, &errOut); code != 2 {
					t.Fatalf("retired command exit code = %d, want 2; stderr=%q", code, errOut.String())
				}
				want := "unsupported arguments"
				if _, group := groupWord(args[0]); group {
					want = "concord: " + args[0] + " has no command " + args[1] + ";"
				}
				if out.Len() != 0 || !strings.Contains(errOut.String(), want) {
					t.Fatalf("retired command stdout=%q stderr=%q, want %q", out.String(), errOut.String(), want)
				}
				if stdin.reads != 0 {
					t.Fatalf("retired command read stdin %d times, want 0", stdin.reads)
				}
				if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
					t.Fatalf("retired command touched the store database at %s", dbPath)
				}
			})
		}
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
			"manifest_digest": agentcontract.ManifestDigest,
		},
		"tool": "concord_product_view", "operation": "resolve", "input": map[string]any{"project_id": "project-1"},
	})
	var envelope agentcontract.Envelope
	// Envelope.UnmarshalJSON carries the schema validation; decoding through it
	// is the published way to read a TS7 envelope.
	err = json.Unmarshal(invokeRaw, &envelope)
	if err != nil {
		t.Fatalf("invoke output is not one schema-valid TS7 envelope: %v; raw=%s", err, invokeRaw)
	}
	if envelope.Outcome != agentcontract.OutcomeOK {
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
	var restoreReport store.RestoreReport
	if err := json.Unmarshal(runRestoreRaw, &restoreReport); err != nil {
		t.Fatalf("restore response is not an inventory report: %v; raw=%s", err, runRestoreRaw)
	}
	if restoreReport.SourceSnapshot.SnapshotID != backupManifest.SnapshotID || restoreReport.SourceSnapshot.IntegrityCheck != "ok" || restoreReport.SourceSnapshot.SchemaVersion != backupManifest.SchemaVersion {
		t.Fatalf("restore source inventory = %+v, want snapshot matching backup", restoreReport.SourceSnapshot)
	}
	if restoreReport.RestoredDatabase.SchemaVersion != store.CurrentSchemaVersion() || restoreReport.RestoredDatabase.DBChecksum == "" || restoreReport.RestoredDatabase.EventWatermark != restoreReport.SourceSnapshot.EventWatermark {
		t.Fatalf("restored database inventory = %+v, want the verified destination", restoreReport.RestoredDatabase)
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

// TestClientPolicyExpandCLIPreservesEveryExistingGrant pins TS5 at the
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
