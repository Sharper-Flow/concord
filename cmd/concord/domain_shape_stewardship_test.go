package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

const stewardshipRatchetPath = ".concord/docs/knowledge/domain-shape-ratchet.json"

// These are synthetic Product inputs to the existing operator checker and store
// projection, not a semantic evaluation of the stewardship skill.
func stewardshipCLIHome(t *testing.T, mixed bool) (string, *store.Store) {
	t.Helper()
	db := freshMigratedCLIDatabase(t)
	t.Setenv(dbOverrideEnv, db)
	repo := shapeCLIHome(t, "alpha")
	const lawPath = ".concord/docs/decisions/CD-0001-synthetic.md"
	lawText := "Synthetic dependency and balance law. domain-a may depend on domain-b. Stored and derived balances must be nonnegative.\n"
	shapeCLIWrite(t, repo, lawPath, lawText)
	lawRaw, err := os.ReadFile(filepath.Join(repo, ".concord/docs/knowledge/records/CD-0001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var law store.KnowledgeRecord
	if err := json.Unmarshal(lawRaw, &law); err != nil {
		t.Fatal(err)
	}
	law.SHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(lawText)))
	lawRaw, err = json.Marshal(law)
	if err != nil {
		t.Fatal(err)
	}
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/records/CD-0001.json", string(lawRaw))
	for _, resource := range []struct{ id, kind string }{{"balance-db", "database"}, {"balance-view", "storage"}} {
		runCLIJSON(t, []string{"resource-create"}, map[string]any{
			"event_id": resource.id + "-created", "resource_id": resource.id, "product_id": "alpha",
			"display_name": resource.id, "class": "infrastructure", "kind": resource.kind, "purpose": "Synthetic balance state",
			"stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "environments": []string{"test"},
			"metadata_schema_version": "1", "metadata": map[string]any{}, "expected_product_version": 3,
		})
	}
	viewPath := "pkg/b/view.py"
	if mixed {
		viewPath = "pkg/a/view.py"
		shapeCLIWrite(t, repo, ".concord/docs/knowledge/package-domains.json", `{"schema_version":"1.1","packages":[{"path":"pkg/a","role":"mixed","domain_ids":["domain-a","domain-b"]},{"path":"pkg/b","role":"domain","owner_domain_id":"domain-b"}]}`)
		shapeCLIWrite(t, repo, stewardshipRatchetPath, `{"schema_version":"1.0","mixed_roots":["pkg/a"],"imports":[]}`)
	}
	shapeCLIWrite(t, repo, viewPath, "def rebuild_view(database):\n    return {'balance': database['balance']}\n")
	shapeCLIWrite(t, repo, "pkg/a/state.py", fmt.Sprintf("from %s import rebuild_view\nfrom pkg.b.module import VALUE\n\ndef write_balance(database, balance):\n    if balance < 0:\n        raise ValueError('negative balance')\n    database['balance'] = balance * VALUE\n    return rebuild_view(database)\n", strings.ReplaceAll(strings.TrimSuffix(viewPath, ".py"), "/", ".")))
	shapeCLIWrite(t, repo, "pkg/a/config.py", "import os\n\ndef read_setting():\n    return int(os.environ.get('alpha_WAIT', '1'))\n")
	shapeCLIWrite(t, repo, "checks.py", "from pkg.a.state import write_balance\n\ndef check_balances():\n    database = {}\n    assert write_balance(database, 2)['balance'] == database['balance'] == 2\n    try:\n        write_balance(database, -1)\n    except ValueError:\n        pass\n    else:\n        raise AssertionError('negative balance accepted')\n    assert database['balance'] == 2\n\ncheck_balances()\n")
	shapeCLIWrite(t, repo, "required-checks.json", `{"check-a":[{"kind":"validator","value":"checks.py:check_balances"}],"check-z":[]}`)
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/facts/nonnegative.json", `{"schema_version":"1.0","id":"nonnegative","kind":"invariant","owner_domain_id":"domain-a","owning_root":"pkg/a","standing":"law","governing_law_ids":["CD-0001"],"invariant":{"statement":"Stored and derived balances must be nonnegative.","applies_to_domain_ids":["domain-a","domain-b"],"enforced_by":[{"kind":"validator","value":"checks.py:check_balances","check_id":"check-a"}]}}`)
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/facts/balances.json", `{"schema_version":"1.0","id":"balances","kind":"data_object","owner_domain_id":"domain-a","owning_root":"pkg/a","standing":"observed","data_object":{"name":"balances","resource_id":"balance-db","writer_domain_ids":["domain-a"],"reader_domain_ids":["domain-a","domain-b"],"invariant_ids":["nonnegative"]}}`)
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/facts/authority.json", fmt.Sprintf(`{"schema_version":"1.0","id":"authority","kind":"state_authority","owner_domain_id":"domain-a","owning_root":"pkg/a","standing":"observed","state_authority":{"state":"balance state","authoritative_resource_id":"balance-db","derived_copies":[{"resource_id":"balance-view","rebuild_route":{"project_id":"alpha-source","path":%q,"symbol":"rebuild_view"}}],"disagreement_behavior":"The database wins; discard and rebuild the view from the database."}}`, viewPath))
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/facts/config.json", `{"schema_version":"1.0","id":"config","kind":"config_coupling","owner_domain_id":"domain-a","owning_root":"pkg/a","standing":"observed","config_coupling":{"setting":"alpha_WAIT","source":"environment","read_by":{"project_id":"alpha-source","path":"pkg/a/config.py","symbol":"read_setting"},"effect":"Selects the integer returned by read_setting","environments":["test","staging"]}}`)
	shapeCLIWrite(t, repo, "observations.json", `{"boundary":{"imports":[{"source_root":"pkg/a","target_root":"pkg/b"}]},"data_objects":{"data_objects":[{"owning_root":"pkg/a","resource_id":"balance-db","name":"balances"}]},"config_reads":{"config_reads":[{"owning_root":"pkg/a","setting":"alpha_WAIT","source":"environment","read_by":{"project_id":"alpha-source","path":"pkg/a/config.py","symbol":"read_setting"}}]}}`)
	tooling, err := os.ReadFile(filepath.Join(repo, ".concord/tooling.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	shapeCLIWrite(t, repo, ".concord/tooling.v1.json", strings.Replace(string(tooling), `"id":"check-a","purpose":"Synthetic required check","tier":"fast","invocation":"true"`, `"id":"check-a","purpose":"Synthetic required check","tier":"fast","invocation":"python3 checks.py"`, 1))
	checker, err := os.ReadFile(filepath.Join(repo, "checker.py"))
	if err != nil {
		t.Fatal(err)
	}
	// Extend the existing declared reference resolver with an independently
	// authored check inventory. Request membership is not its own evidence.
	shapeCLIWrite(t, repo, "checker.py", strings.Replace(string(checker), `    result["enforcement_anchors"] = []`, `    checks = json.loads(pathlib.Path("required-checks.json").read_text())
    result["enforcement_anchors"] = []
    for a in request["enforcement_anchors"]:
        path, symbol = a["value"].split(":", 1)
        members = [key for key, anchors in checks.items() if {"kind": a["kind"], "value": a["value"]} in anchors]
        result["enforcement_anchors"].append({"anchor": a, "resolved": symbol in pathlib.Path(path).read_text(), "required_check_ids": members})`, 1))
	shapeCLICommit(t, repo, true)
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return repo, s
}

func stewardshipRead(t *testing.T, s *store.Store, repo string, selector store.DomainShapeSelector) store.DomainShapeResult {
	t.Helper()
	got, err := s.QueryDomainShape(context.Background(), "alpha", selector)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProductID != "alpha" || got.HomeProjectID != "alpha-source" || got.HomeLocatorID != "alpha-home" || got.KnowledgeCommit != shapeCLIGit(t, repo, "rev-parse", "HEAD") || got.Selector != selector {
		t.Fatalf("wrong selection identity: %+v", got)
	}
	if got.ReferenceVerification != "unverified" || got.ExtractorVerification != "unverified" {
		t.Fatalf("read promoted declarations to execution evidence: %+v", got)
	}
	return got
}

func stewardshipCheck(t *testing.T, repo, toolingCommit string) store.DomainShapeOperatorCheck {
	t.Helper()
	code, result, diagnostic := shapeCLICheck(t, "alpha", "")
	if code != 0 {
		t.Fatalf("operator check exit=%d: %s", code, diagnostic)
	}
	if result.Subject.ProductID != "alpha" || result.Subject.ProjectID != "alpha-source" || result.Subject.CommitOID != shapeCLIGit(t, repo, "rev-parse", "HEAD") || result.ToolingCommit != toolingCommit {
		t.Fatalf("lost candidate/default pins: %+v", result)
	}
	if len(result.Checks) != 4 {
		t.Fatalf("incomplete check inventory: %+v", result.Checks)
	}
	for _, op := range []string{"boundary", "data_objects", "config_reads", "references"} {
		got := result.Checks[op]
		if got.State != "satisfied" || got.Verification != "verified" || len(got.Findings) != 0 {
			t.Fatalf("%s: %+v", op, got)
		}
	}
	t.Log("boundary/data_objects/config_reads/references: satisfied, verified, no findings")
	return result
}

// Shape checks resolve required-check membership; business assertions need their
// own execution. Run the exact check-a invocation declared by stewardshipCLIHome.
func stewardshipRunBalanceCheck(t *testing.T, repo, phase string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "checks.py")
	cmd.Dir = repo
	// Keep the committed home clean and ensure Python assertions remain enabled.
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONOPTIMIZE=")
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: python3 checks.py: %v (context: %v)\n%s", phase, err, ctx.Err(), output)
	}
	t.Logf("%s: python3 checks.py passed: stored/derived balance=2; negative write rejected; stored balance unchanged", phase)
}

func TestDomainShapeStewardshipAllFourFacts(t *testing.T) {
	repo, s := stewardshipCLIHome(t, false)
	stewardshipRunBalanceCheck(t, repo, "all-four baseline")
	stewardshipCheck(t, repo, shapeCLIGit(t, repo, "rev-parse", "HEAD"))
	got := stewardshipRead(t, s, repo, store.DomainShapeSelector{DomainID: "domain-a"})
	if len(got.Facts) != 4 || len(got.Roots) != 1 || got.Roots[0].Role != "domain" || got.Roots[0].OwnerDomainID != "domain-a" {
		t.Fatalf("expected four facts under one exclusive behavior owner: %+v", got)
	}
	kinds := map[store.DomainFactKind]bool{}
	for _, item := range got.Facts {
		fact := item.Fact
		kinds[fact.Kind] = true
		if fact.OwnerDomainID != "domain-a" || fact.OwningRoot != "pkg/a" {
			t.Fatalf("foreign fact owner: %+v", fact)
		}
		switch fact.Kind {
		case store.DomainFactInvariant:
			if fact.ID != "nonnegative" || fact.Invariant.Statement != "Stored and derived balances must be nonnegative." || fact.Standing != "law" || !reflect.DeepEqual(fact.GoverningLawIDs, []string{"CD-0001"}) || !reflect.DeepEqual(fact.Invariant.AppliesToDomainIDs, []string{"domain-a", "domain-b"}) || !reflect.DeepEqual(fact.Invariant.EnforcedBy, []store.DomainFactEnforcementAnchor{{Kind: "validator", Value: "checks.py:check_balances", CheckID: "check-a"}}) {
				t.Fatalf("lost spanning invariant/check authority: %+v", fact)
			}
		case store.DomainFactDataObject:
			want := store.DomainDataObjectFact{Name: "balances", ResourceID: "balance-db", WriterDomainIDs: []string{"domain-a"}, ReaderDomainIDs: []string{"domain-a", "domain-b"}, InvariantIDs: []string{"nonnegative"}}
			if fact.ID != "balances" || !reflect.DeepEqual(*fact.DataObject, want) {
				t.Fatalf("lost data identity: %+v", fact)
			}
		case store.DomainFactStateAuthority:
			want := store.DomainStateAuthorityFact{State: "balance state", AuthoritativeResourceID: "balance-db", DerivedCopies: []store.DomainFactDerivedCopy{{ResourceID: "balance-view", RebuildRoute: store.DomainFactRepositoryAnchor{ProjectID: "alpha-source", Path: "pkg/b/view.py", Symbol: "rebuild_view"}}}, DisagreementBehavior: "The database wins; discard and rebuild the view from the database."}
			if fact.ID != "authority" || !reflect.DeepEqual(*fact.StateAuthority, want) {
				t.Fatalf("lost authoritative/derived rebuild relationship: %+v", fact)
			}
		case store.DomainFactConfigCoupling:
			want := store.DomainConfigCouplingFact{Setting: "alpha_WAIT", Source: "environment", ReadBy: store.DomainFactRepositoryAnchor{ProjectID: "alpha-source", Path: "pkg/a/config.py", Symbol: "read_setting"}, Effect: "Selects the integer returned by read_setting", Environments: []string{"test", "staging"}}
			if fact.ID != "config" || !reflect.DeepEqual(*fact.ConfigCoupling, want) {
				t.Fatalf("lost config coupling: %+v", fact)
			}
		}
		if fact.Kind != store.DomainFactInvariant && (fact.Standing != "observed" || len(fact.GoverningLawIDs) != 0) {
			t.Fatalf("observation acquired law authority: %+v", fact)
		}
	}
	if len(kinds) != 4 || !reflect.DeepEqual(got.GoverningLaw, []string{"CD-0001"}) {
		t.Fatalf("missing fact kind or current law: %+v", got)
	}
	resources, err := s.Resources(context.Background(), store.ResourcesRequest{ProductID: "alpha", Limit: 10})
	if err != nil || len(resources.Resources) != 2 {
		t.Fatalf("resources: %+v %v", resources, err)
	}
	for i, id := range []string{"balance-db", "balance-view"} {
		resource := resources.Resources[i]
		if resource.ResourceID != id || resource.Owner.ProductID != "alpha" || resource.Owner.Role != "owner" {
			t.Fatalf("resource is not Product-owned: %+v", resource)
		}
	}
	t.Log("four fact kinds retain IDs, law/observed standing, spanning enforcement, Product-owned resources, rebuild and config anchors")
}

func TestDomainShapeStewardshipMixedToExclusiveMigration(t *testing.T) {
	repo, s := stewardshipCLIHome(t, true)
	baseline := shapeCLIGit(t, repo, "rev-parse", "HEAD")
	stewardshipRunBalanceCheck(t, repo, "before migration")
	beforeCheck := stewardshipCheck(t, repo, baseline)
	before := stewardshipRead(t, s, repo, store.DomainShapeSelector{DomainID: "domain-a"})
	if len(before.Facts) != 4 || !reflect.DeepEqual(before.Roots, []store.DomainShapeRoot{{Path: "pkg/a", Role: "mixed", DomainIDs: []string{"domain-a", "domain-b"}}}) {
		t.Fatalf("fixture is not a genuinely mixed four-fact root: %+v", before)
	}
	resourcesBefore, err := s.Resources(context.Background(), store.ResourcesRequest{ProductID: "alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Lowering metadata alone must not satisfy the migration's positive check.
	shapeCLIWrite(t, repo, stewardshipRatchetPath, `{"schema_version":"1.0","mixed_roots":[],"imports":[]}`)
	shapeCLICommit(t, repo, false)
	code, control, diagnostic := shapeCLICheck(t, "alpha", "")
	if code == 0 || !strings.Contains(diagnostic, "outstanding") || !reflect.DeepEqual(control.Checks["boundary"].Findings, []store.DomainShapeFinding{{State: "outstanding", Code: "new_mixed_root", Subject: "pkg/a"}}) {
		t.Fatalf("ratchet-only control did not expose the unrestructured root: %d %+v %s", code, control, diagnostic)
	}
	t.Log("negative control: ratchet [pkg/a] -> []; still mixed => outstanding new_mixed_root pkg/a")
	viewBytes, err := os.ReadFile(filepath.Join(repo, "pkg/a/view.py"))
	if err != nil {
		t.Fatal(err)
	}
	shapeCLIGit(t, repo, "mv", "pkg/a/view.py", "pkg/b/view.py")
	state, err := os.ReadFile(filepath.Join(repo, "pkg/a/state.py"))
	if err != nil {
		t.Fatal(err)
	}
	shapeCLIWrite(t, repo, "pkg/a/state.py", strings.Replace(string(state), "from pkg.a.view import", "from pkg.b.view import", 1))
	shapeCLIWrite(t, repo, ".concord/docs/knowledge/package-domains.json", `{"schema_version":"1.1","packages":[{"path":"pkg/a","role":"domain","owner_domain_id":"domain-a"},{"path":"pkg/b","role":"domain","owner_domain_id":"domain-b"}]}`)
	expectedFacts := make([]store.DomainFact, len(before.Facts))
	for i, item := range before.Facts {
		fact := item.Fact
		if item.Fact.StateAuthority != nil {
			authority := *fact.StateAuthority
			authority.DerivedCopies = append([]store.DomainFactDerivedCopy(nil), authority.DerivedCopies...)
			authority.DerivedCopies[0].RebuildRoute.Path = "pkg/b/view.py"
			fact.StateAuthority = &authority
			raw, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			shapeCLIWrite(t, repo, item.Path, string(raw))
		}
		expectedFacts[i] = fact
	}
	candidate := shapeCLICommit(t, repo, false)
	stewardshipRunBalanceCheck(t, repo, "after migration")
	afterCheck := stewardshipCheck(t, repo, baseline)
	if candidate == baseline || afterCheck.ToolingHash != beforeCheck.ToolingHash || afterCheck.RatchetHash == beforeCheck.RatchetHash || shapeCLIGit(t, repo, "rev-parse", "refs/remotes/origin/main") != baseline {
		t.Fatal("migration did not reduce the ratchet against unchanged pinned default authority")
	}
	if beforeCheck.RatchetHash != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{"schema_version":"1.0","mixed_roots":["pkg/a"],"imports":[]}`))) || afterCheck.RatchetHash != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{"schema_version":"1.0","mixed_roots":[],"imports":[]}`))) {
		t.Fatal("unexpected before/after ratchet bytes")
	}
	after := stewardshipRead(t, s, repo, store.DomainShapeSelector{DomainID: "domain-a"})
	if !reflect.DeepEqual(after.Roots, []store.DomainShapeRoot{{Path: "pkg/a", Role: "domain", OwnerDomainID: "domain-a"}}) || len(after.Facts) != len(before.Facts) || after.RegistryContentHash != before.RegistryContentHash || after.PackageMapContentHash == before.PackageMapContentHash {
		t.Fatalf("exclusive ownership/unchanged registry not established: %+v", after)
	}
	for i, item := range after.Facts {
		want := expectedFacts[i]
		if !reflect.DeepEqual(item.Fact, want) || item.Path != before.Facts[i].Path || (item.Fact.StateAuthority == nil && item.ContentHash != before.Facts[i].ContentHash) {
			t.Fatalf("migration changed identity, standing or an unrelated fact: %+v want %+v", item, want)
		}
	}
	// The law's manifest record pins its path and content hash at each commit.
	lawRecord := ".concord/docs/knowledge/records/CD-0001.json"
	if len(after.GoverningLaw) != 1 || !reflect.DeepEqual(after.GoverningLaw, before.GoverningLaw) || shapeCLIGit(t, repo, "rev-parse", after.KnowledgeCommit+":"+lawRecord) != shapeCLIGit(t, repo, "rev-parse", before.KnowledgeCommit+":"+lawRecord) {
		t.Fatal("migration changed accepted law identity/content")
	}
	byPath := stewardshipRead(t, s, repo, store.DomainShapeSelector{Path: "pkg/b/view.py"})
	movedBytes, err := os.ReadFile(filepath.Join(repo, "pkg/b/view.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(byPath.Roots, []store.DomainShapeRoot{{Path: "pkg/b", Role: "domain", OwnerDomainID: "domain-b"}}) || len(byPath.Facts) != 0 || !bytes.Equal(movedBytes, viewBytes) {
		t.Fatalf("moved behavior did not retain bytes under its exclusive owner: %+v", byPath)
	}
	if _, err := os.Stat(filepath.Join(repo, "pkg/a/view.py")); !os.IsNotExist(err) {
		t.Fatalf("old behavior copy remains: %v", err)
	}
	bySubject := stewardshipRead(t, s, repo, store.DomainShapeSelector{Subject: "balance-view"})
	if len(bySubject.Facts) != 1 || !reflect.DeepEqual(bySubject.Facts[0].Fact, after.Facts[0].Fact) {
		t.Fatalf("derived-copy subject lost its authority/rebuild relationship: %+v", bySubject)
	}
	resourcesAfter, err := s.Resources(context.Background(), store.ResourcesRequest{ProductID: "alpha", Limit: 10})
	if err != nil || !reflect.DeepEqual(resourcesAfter.Resources, resourcesBefore.Resources) {
		t.Fatalf("migration changed resource identities/ownership: %+v %v", resourcesAfter, err)
	}
	t.Log("migration: pkg/a mixed [domain-a,domain-b] -> domain-a; view moved byte-for-byte to domain-b pkg/b; ratchet [pkg/a] -> []; law/resources/standing/dependency/required-check authority preserved")
}
