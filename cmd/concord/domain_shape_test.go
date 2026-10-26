package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func shapeCLIGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Synthetic", "GIT_AUTHOR_EMAIL=synthetic@example.invalid", "GIT_COMMITTER_NAME=Synthetic", "GIT_COMMITTER_EMAIL=synthetic@example.invalid")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, raw)
	}
	return strings.TrimSpace(string(raw))
}

func shapeCLIWrite(t *testing.T, repo, name, text string) {
	t.Helper()
	full := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func shapeCLICommit(t *testing.T, repo string, updateDefault bool) string {
	t.Helper()
	shapeCLIGit(t, repo, "add", ".")
	shapeCLIGit(t, repo, "commit", "-m", "Synthetic check fixture")
	commit := shapeCLIGit(t, repo, "rev-parse", "HEAD")
	if updateDefault {
		shapeCLIGit(t, repo, "update-ref", "refs/remotes/origin/main", commit)
		shapeCLIGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	return commit
}

func shapeCLIHome(t *testing.T, id string) string {
	t.Helper()
	repo := t.TempDir()
	shapeCLIGit(t, repo, "init", "-b", "main")
	runCLIJSON(t, []string{"product-create"}, map[string]any{"product_id": id, "display_name": id, "stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "project_id": id + "-source", "project_display_name": id + " source", "role": "primary"})
	runCLIJSON(t, []string{"project-locator-add"}, map[string]any{"project_id": id + "-source", "locator_id": id + "-home", "kind": "canonical_path", "value": repo, "expected_version": 1})
	runCLIJSON(t, []string{"product-knowledge-home-designate"}, map[string]any{"product_id": id, "project_id": id + "-source", "locator_id": id + "-home", "expected_version": 2})
	// The production designation route must create shape, not merely record a
	// home. Reading the files here makes missing CLI orchestration fail early.
	for _, name := range []string{"domain-registry.json", "package-domains.json", "domain-shape-ratchet.json"} {
		if _, err := os.Stat(filepath.Join(repo, ".concord/docs/knowledge", name)); err != nil {
			t.Fatal(err)
		}
	}
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/domain-registry.json", strings.ReplaceAll(fmt.Sprintf(`{"schema_version":"1.0","product_key":%q,"root_domain_id":"root","domains":[{"domain_id":"root","name":"Root","purpose":"Synthetic product behavior","status":"current","architecture_relations":[]},{"domain_id":"domain-a","name":"A","purpose":"Synthetic source behavior","parent_domain_id":"root","status":"current","architecture_relations":[{"kind":"depends_on","target_domain_id":"domain-b","governing_law_ids":["CD-0001"]}]},{"domain_id":"domain-b","name":"B","purpose":"Synthetic target behavior","parent_domain_id":"root","status":"current","architecture_relations":[]}]}`, id), `"root"`, `"product-root:`+id+`"`))
	lawText := "Synthetic " + id + " dependency law.\n"
	lawPath := ".concord/docs/decisions/CD-0001-synthetic.md"
	shapeCLIWrite(t, repo, lawPath, lawText)
	law := store.KnowledgeRecord{ID: "CD-0001", Kind: "decision", Path: lawPath, Status: "accepted", Date: "2026-10-01T00:00:00Z", Title: "Synthetic dependency law", Summary: "Allows the synthetic dependency", Tags: []string{}, Authority: store.KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-0f1e2d3c4b5a6978", ContractVersion: 1}, Scopes: store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{"domain-a", "domain-b"}, TagIDs: []string{}}, Evidence: []string{}, SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(lawText))), HomeDomainID: "domain-a", CriterionBindings: []store.KnowledgeCriterionBinding{}}
	lawRaw, err := json.Marshal(law)
	if err != nil {
		t.Fatal(err)
	}
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/records/CD-0001.json", string(lawRaw))
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/package-domains.json", `{"schema_version":"1.1","packages":[{"path":"pkg/a","role":"domain","owner_domain_id":"domain-a"},{"path":"pkg/b","role":"domain","owner_domain_id":"domain-b"}]}`)
	shapeCLIWrite(t, repo, "pkg/a/config.py", "def read_setting():\n    return 'synthetic'\n")
	shapeCLIWrite(t, repo, "pkg/b/module.py", "VALUE = 1\n")
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/facts/config.json", fmt.Sprintf(`{"schema_version":"1.0","id":"config","kind":"config_coupling","owner_domain_id":"domain-a","owning_root":"pkg/a","standing":"observed","config_coupling":{"setting":%q,"source":"environment","read_by":{"project_id":%q,"path":"pkg/a/config.py","symbol":"read_setting"},"effect":"Controls the synthetic wait","environments":["test"]}}`, id+"_WAIT", id+"-source"))
	shapeCLIWrite(t, repo, ".concord/tooling.v1.json", fmt.Sprintf(`{"schema_version":"1.0","project":%q,"tools":[{"id":"shape-check","purpose":"Synthetic shape checker","tier":"fast","invocation":"python3 checker.py"},{"id":"check-z","purpose":"Synthetic required check","tier":"fast","invocation":"true"},{"id":"check-a","purpose":"Synthetic required check","tier":"fast","invocation":"true"}],"domain_shape":{"boundary_check":"shape-check","data_object_extractor":"shape-check","config_extractor":"shape-check","reference_check":"shape-check","required_checks":["check-z","check-a"]}}`, id+"-source"))
	shapeCLIWrite(t, repo, "observations.json", fmt.Sprintf(`{"boundary":{"imports":[{"source_root":"pkg/a","target_root":"pkg/b"}]},"data_objects":{"data_objects":[]},"config_reads":{"config_reads":[{"owning_root":"pkg/a","setting":%q,"source":"environment","read_by":{"project_id":%q,"path":"pkg/a/config.py","symbol":"read_setting"}}]}}`, id+"_WAIT", id+"-source"))
	shapeCLIWrite(t, repo, "checker.py", `import json, pathlib, sys
request = json.load(sys.stdin)
operation = request["operation"]
result = {"schema_version": "1.0", "operation": operation, "subject": request["subject"], "complete": True}
if operation == "references":
    result["repository_anchors"] = [{"anchor": a, "resolved": a["symbol"] in pathlib.Path(a["path"]).read_text()} for a in request["repository_anchors"]]
    result["enforcement_anchors"] = []
else:
    result.update(json.loads(pathlib.Path("observations.json").read_text())[operation])
json.dump(result, sys.stdout)
`)
	if id == "beta" {
		// Authored repository keys need not equal SQLite IDs. Product selection
		// comes from the registered locator, not these display identities.
		name := ".concord/docs/knowledge/domain-registry.json"
		raw, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		shapeCLIWrite(t, repo, name, strings.ReplaceAll(strings.ReplaceAll(string(raw), `"product_key":"beta"`, `"product_key":"stable-beta"`), `product-root:beta`, `product-root:stable-beta`))
		name = ".concord/tooling.v1.json"
		raw, err = os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		shapeCLIWrite(t, repo, name, strings.Replace(string(raw), `"project":"beta-source"`, `"project":"stable-source"`, 1))
	}
	shapeCLICommit(t, repo, true)
	return repo
}

func shapeCLICheck(t *testing.T, product, checkout string) (int, store.DomainShapeOperatorCheck, string) {
	t.Helper()
	request := map[string]any{"product_id": product}
	if checkout != "" {
		request["checkout"] = checkout
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	code := runWithInput([]string{"domain-shape-check"}, bytes.NewReader(raw), &out, &diagnostics)
	if out.Len() > store.DomainShapeMaxBytes || diagnostics.Len() > 4097 {
		t.Fatalf("unbounded report/diagnostic: %d/%d", out.Len(), diagnostics.Len())
	}
	var result store.DomainShapeOperatorCheck
	if out.Len() != 0 {
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("invalid result: %v: %s", err, &out)
		}
	}
	t.Logf("operator report bytes=%d diagnostics=%d", out.Len(), diagnostics.Len())
	return code, result, diagnostics.String()
}

func TestDomainShapeOperatorCLIExecutesPinnedProductChecks(t *testing.T) {
	t.Setenv(dbOverrideEnv, freshMigratedCLIDatabase(t))
	for _, product := range []string{"alpha", "beta"} {
		repo := shapeCLIHome(t, product)
		code, result, diagnostic := shapeCLICheck(t, product, "")
		if code != 0 {
			t.Fatalf("check: %s", diagnostic)
		}
		if result.Subject.ProductID != product || result.Subject.ProjectID != product+"-source" || result.Subject.CommitOID != shapeCLIGit(t, repo, "rev-parse", "HEAD") {
			t.Fatalf("foreign/unpinned subject: %+v", result.Subject)
		}
		for _, op := range []string{"boundary", "data_objects", "config_reads", "references"} {
			evaluation, ok := result.Checks[op]
			if !ok || evaluation.State != "satisfied" || evaluation.Verification != "verified" {
				t.Fatalf("%s: %+v", op, evaluation)
			}
		}
		// Repetition uses the current Product version and creates no event.
		raw := runCLIJSON(t, []string{"product-knowledge-home-designate"}, map[string]any{"product_id": product, "project_id": product + "-source", "locator_id": product + "-home", "expected_version": 3})
		assertChangedRefVersion(t, raw, "product", product, "3")
	}
}

func TestDomainShapeOperatorCLIRejectsViolationsAndCommandFailures(t *testing.T) {
	for _, scenario := range []string{"edge", "strict-ratchet", "missing-fact", "orphan-fact", "command", "subject", "external-harness"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv(dbOverrideEnv, freshMigratedCLIDatabase(t))
			repo := shapeCLIHome(t, "alpha")
			want := "outstanding"
			switch scenario {
			case "edge":
				name := ".concord/docs/knowledge/domain-registry.json"
				raw, err := os.ReadFile(filepath.Join(repo, name))
				if err != nil {
					t.Fatal(err)
				}
				shapeCLIWrite(t, repo, name, strings.Replace(string(raw), `[{"kind":"depends_on","target_domain_id":"domain-b","governing_law_ids":["CD-0001"]}]`, `[]`, 1))
			case "strict-ratchet":
				shapeCLIWrite(t, repo, ".concord/docs/knowledge/package-domains.json", `{"schema_version":"1.1","packages":[{"path":"pkg/a","role":"mixed","domain_ids":["domain-a","domain-b"]},{"path":"pkg/b","role":"domain","owner_domain_id":"domain-b"}]}`)
			case "missing-fact":
				if err := os.Remove(filepath.Join(repo, ".concord/docs/knowledge/facts/config.json")); err != nil {
					t.Fatal(err)
				}
			case "orphan-fact":
				raw, err := os.ReadFile(filepath.Join(repo, "observations.json"))
				if err != nil {
					t.Fatal(err)
				}
				var observed map[string]any
				if err := json.Unmarshal(raw, &observed); err != nil {
					t.Fatal(err)
				}
				observed["config_reads"] = map[string]any{"config_reads": []any{}}
				encoded, _ := json.Marshal(observed)
				shapeCLIWrite(t, repo, "observations.json", string(encoded))
			case "command":
				shapeCLIWrite(t, repo, "checker.py", "raise SystemExit(7)\n")
				want = "command failed"
			case "subject":
				shapeCLIWrite(t, repo, "checker.py", `import json,sys
x=json.load(sys.stdin)
x["subject"]["product_id"]="foreign"
json.dump({"schema_version":"1.0","operation":"boundary","subject":x["subject"],"complete":True,"imports":[]},sys.stdout)
`)
				want = "does not match"
			case "external-harness":
				raw, err := os.ReadFile(filepath.Join(repo, ".concord/tooling.v1.json"))
				if err != nil {
					t.Fatal(err)
				}
				shapeCLIWrite(t, repo, ".concord/tooling.v1.json", strings.Replace(string(raw), `python3 checker.py`, `python3 /outside/checkout/checker.py`, 1))
				want = "outside the pinned checkout"
			}
			shapeCLICommit(t, repo, true)
			code, _, diagnostic := shapeCLICheck(t, "alpha", "")
			if code == 0 || !strings.Contains(diagnostic, want) {
				t.Fatalf("%s: code=%d: %s", scenario, code, diagnostic)
			}
		})
	}
}

func TestDomainShapeOperatorCLICheckoutAdmission(t *testing.T) {
	t.Setenv(dbOverrideEnv, freshMigratedCLIDatabase(t))
	repo := shapeCLIHome(t, "alpha")
	first := shapeCLIGit(t, repo, "rev-parse", "HEAD")
	shapeCLIWrite(t, repo, "extra.txt", "synthetic subject revision\n")
	shapeCLICommit(t, repo, true)
	old := filepath.Join(t.TempDir(), "old")
	shapeCLIGit(t, repo, "worktree", "add", "--detach", old, first)
	current := filepath.Join(t.TempDir(), "current")
	shapeCLIGit(t, repo, "worktree", "add", "--detach", current, "HEAD")
	foreign := t.TempDir()
	shapeCLIGit(t, foreign, "init", "-b", "main")
	shapeCLIWrite(t, foreign, "placeholder.txt", "synthetic foreign repository\n")
	shapeCLICommit(t, foreign, false)
	for _, item := range []struct{ checkout, want string }{{old, "exact pinned subject"}, {foreign, "foreign repository"}, {filepath.Join(repo, "pkg"), "checkout root"}} {
		code, _, diagnostic := shapeCLICheck(t, "alpha", item.checkout)
		if code == 0 || !strings.Contains(diagnostic, item.want) {
			t.Fatalf("checkout=%s code=%d: %s", item.checkout, code, diagnostic)
		}
	}
	shapeCLIWrite(t, repo, "checker.py", "raise SystemExit('mutable harness must never run')\n")
	code, _, diagnostic := shapeCLICheck(t, "alpha", "")
	if code == 0 || !strings.Contains(diagnostic, "dirty") {
		t.Fatalf("dirty harness ran: %d %s", code, diagnostic)
	}
	shapeCLIGit(t, repo, "update-index", "--assume-unchanged", "checker.py")
	code, _, diagnostic = shapeCLICheck(t, "alpha", "")
	if code == 0 || !strings.Contains(diagnostic, "raw checkout bytes") {
		t.Fatalf("index flag hid a mutable harness: %d %s", code, diagnostic)
	}
	code, _, diagnostic = shapeCLICheck(t, "alpha", current)
	if code != 0 {
		t.Fatalf("clean exact linked checkout fell back to mutable home: %s", diagnostic)
	}
}

func TestDomainShapeOperatorCLIDefaultDeclarationAndAbsentExtractors(t *testing.T) {
	t.Setenv(dbOverrideEnv, freshMigratedCLIDatabase(t))
	repo := shapeCLIHome(t, "alpha")
	defaultCommit := shapeCLIGit(t, repo, "rev-parse", "HEAD")
	name := ".concord/tooling.v1.json"
	raw, err := os.ReadFile(filepath.Join(repo, name))
	if err != nil {
		t.Fatal(err)
	}
	shapeCLIWrite(t, repo, name, strings.Replace(string(raw), "python3 checker.py", "false", 1))
	subject := shapeCLICommit(t, repo, false)
	code, result, diagnostic := shapeCLICheck(t, "alpha", "")
	if code != 0 || result.ToolingCommit != defaultCommit || result.Subject.CommitOID != subject {
		t.Fatalf("candidate tooling replaced default: %+v %s", result, diagnostic)
	}
	shapeCLIWrite(t, repo, name, `{"schema_version":"1.0","project":"alpha-source","tools":[{"id":"check-z","purpose":"Synthetic required check","tier":"fast","invocation":"true"},{"id":"check-a","purpose":"Synthetic required check","tier":"fast","invocation":"true"}],"domain_shape":{"required_checks":["check-z","check-a"]}}`)
	shapeCLICommit(t, repo, true)
	code, result, diagnostic = shapeCLICheck(t, "alpha", "")
	if code != 0 {
		t.Fatal(diagnostic)
	}
	for op, evaluation := range result.Checks {
		if evaluation.Verification != "unverified" || evaluation.State != "unmeasured" {
			t.Fatalf("%s absent command claimed verified: %+v", op, evaluation)
		}
	}
}

func TestDomainShapeOperatorCLIDiagnosticBound(t *testing.T) {
	var out bytes.Buffer
	writeDomainShapeDiagnostic(&out, fmt.Errorf("%s", strings.Repeat("界", 10000)))
	if out.Len() > 4096 || !strings.HasSuffix(out.String(), "...\n") {
		t.Fatal("diagnostic did not obey bound")
	}
}

func TestDomainShapeOperatorCLIRefusesOversizedReports(t *testing.T) {
	result := store.DomainShapeOperatorCheck{Checks: map[string]store.DomainShapeEvaluation{"boundary": {Findings: []store.DomainShapeFinding{{Subject: strings.Repeat("x", store.DomainShapeMaxBytes)}}}}}
	var out, diagnostic bytes.Buffer
	code := writeDomainShapeReport(result, nil, &out, &diagnostic)
	if code == 0 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "32768") {
		t.Fatalf("oversize report escaped: %d %d %s", code, out.Len(), &diagnostic)
	}
}
