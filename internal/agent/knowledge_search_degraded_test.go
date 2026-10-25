package agent

// Public search passes suppressed admission demands to Q9's per-source
// authority path even when the Git watermark stays unchanged.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// demandHomeWork and demandSourceWork name the synthetic works whose contract
// history each fixture manifest declares.
const (
	demandHomeWork   = "work-0f1e2d3c4b5a6978"
	demandSourceWork = "work-aabbccdd11223344"
)

// demandSearchHarness builds one Product whose designated home — and, when
// withSource is set, one registered source — each carry a derived lesson and a
// legislated decision. The decisions declare the synthetic works above, which
// the store can later learn with a held negative; Git never moves, so only the
// demand path can see the change.
func demandSearchHarness(t *testing.T, withSource bool) (*store.Store, store.KnowledgeHome, store.KnowledgeHome) {
	t.Helper()
	source := store.KnowledgeHome{}
	if withSource {
		source = store.KnowledgeHome{HomeProjectID: "d-src", HomeLocatorID: "d-srcl"}
	}
	return demandSearchHarnessWithSources(t, store.KnowledgeHome{HomeProjectID: "d-home", HomeLocatorID: "d-loc"}, source)
}

func demandSearchHarnessWithSources(t *testing.T, home, source store.KnowledgeHome) (*store.Store, store.KnowledgeHome, store.KnowledgeHome) {
	t.Helper()
	withSource := source.HomeProjectID != ""
	home.RepoPath = initDemandRepo(t, "home", demandHomeWork, true)
	home.HeadRef = "HEAD"
	if withSource {
		source.RepoPath = initDemandRepo(t, "source", demandSourceWork, false)
		source.HeadRef = "HEAD"
	}
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	seed := func(project, locator, repo string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES(?,?,1,'now','now')`, project, project); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?,'canonical_path',?,?, 'now','now')`, locator, project, repo, repo); err != nil {
			t.Fatal(err)
		}
	}
	seed(home.HomeProjectID, home.HomeLocatorID, home.RepoPath)
	if withSource {
		seed(source.HomeProjectID, source.HomeLocatorID, source.RepoPath)
	}
	if _, err := db.Exec(`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('demand-product','Demand product','prototype','operator_only',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('demand-product',?,?)`, home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if withSource {
		if _, err := db.Exec(`INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('demand-product',?,?,'now')`, source.HomeProjectID, source.HomeLocatorID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if withSource {
		if err := s.RebuildKnowledgeIndex(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
	return s, home, source
}

// initDemandRepo lays one committed lesson and one committed legislated
// decision whose authority names the given synthetic work id. Only the
// shared-law home carries the Domain registry; a registered source's manifest
// omits it (CD-0200).
func initDemandRepo(t *testing.T, name, workID string, withRegistry bool) string {
	t.Helper()
	repo := t.TempDir()
	lessonPath := ".concord/docs/lessons/demand-" + name + ".md"
	lessonContent := "durable demand fixture blob\n"
	writeDemandFile(t, repo, lessonPath, lessonContent)
	decisionPath := ".concord/docs/decisions/CD-8751.md"
	decisionContent := "durable demand fixture law\n"
	writeDemandFile(t, repo, decisionPath, decisionContent)
	manifest := map[string]any{
		"schema_version":  "1.3",
		"supported_kinds": []string{"work_note", "decision", "spec", "constitution", "lesson", "reference", "research"},
		"indexed_kinds":   []string{"work_note", "decision", "spec", "constitution", "lesson", "reference"},
		"records": []any{
			map[string]any{
				"id": "demand-" + name + "-lesson", "kind": "lesson", "path": lessonPath, "status": "published", "date": "2026-10-11T00:00:00Z",
				"title": "Durable lesson", "summary": "Durable summary", "tags": []string{},
				"authority": map[string]any{"tier": "derived"},
				"scopes":    map[string]any{"mode": "home", "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{}},
				"sha256":    demandContentHash(lessonContent),
			},
			map[string]any{
				"id": "CD-8751", "kind": "decision", "path": decisionPath, "status": "accepted", "date": "2026-10-11T00:00:00Z",
				"title": "Demand fixture law", "summary": "Fixture law whose demand the search handler suppresses.", "tags": []string{},
				"authority":              map[string]any{"tier": "legislated", "legislated_by": workID, "contract_version": 1},
				"scopes":                 map[string]any{"mode": "home", "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{}},
				"home_domain_id":         "product-root:demand-fixture",
				"product_wide_rationale": "Fixture law binds every child Domain.",
				"sha256":                 demandContentHash(decisionContent),
			},
		},
	}
	encoded := map[string]any{}
	for key, value := range manifest {
		encoded[key] = value
	}
	if withRegistry {
		encoded["domain_registry"] = map[string]any{
			"schema_version": "1.0", "product_key": "demand-fixture", "root_domain_id": "product-root:demand-fixture",
			"domains": []any{
				map[string]any{"domain_id": "product-root:demand-fixture", "name": "Demand fixture", "purpose": "fixture registry", "status": "current", "architecture_relations": []any{}},
				map[string]any{"domain_id": "fixture-law", "name": "fixture-law", "purpose": "fixture law home", "parent_domain_id": "product-root:demand-fixture", "status": "current", "architecture_relations": []any{}},
			},
		}
	}
	manifestBytes, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var seed store.KnowledgeManifest
	if err := json.Unmarshal(manifestBytes, &seed); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.WriteKnowledgeShards(repo, seed); err != nil {
		t.Fatal(err)
	}
	if !withRegistry {
		// The zero-value DomainRegistry field marshals with the key present,
		// so WriteKnowledgeShards writes an empty registry shard. A registered
		// source's manifest must carry none: remove it, mirroring the
		// source-role fixtures the store tests write.
		if err := os.Remove(filepath.Join(repo, ".concord/docs/knowledge/domain-registry.json")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "--initial-branch=main")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Concord Test")
	run("add", ".")
	run("commit", "--quiet", "-m", "demand fixture")
	return repo
}

func writeDemandFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func demandContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// seedDemandHeldNegative teaches the store a work whose history is a held
// negative: the declared work exists and carries no approved contract. No Git
// content moves.
func seedDemandHeldNegative(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := db.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Demand fixture work','completed',1,1,'now','now')`, workID); err != nil {
		t.Fatal(err)
	}
}

func searchDemand(t *testing.T, s *store.Store, allowDegraded bool) Envelope {
	t.Helper()
	r := runtime{Store: s, Tool: "concord_knowledge", Operation: "search", Envelope: CallEnvelope{SelectedProductID: "demand-product"}}
	input := `{"product_id":"demand-product","allow_degraded":` + strconv.FormatBool(allowDegraded) + `}`
	response, err := r.read(context.Background(), NewBase("demand-search", r.Tool, r.Operation), []byte(input), "PM1.Q9")
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestKnowledgeSearchHeldNegativeDemandRefusesWithoutDegraded(t *testing.T) {
	t.Parallel()
	s, _, _ := demandSearchHarness(t, false)
	defer s.Close()
	seedDemandHeldNegative(t, s, demandHomeWork)
	response := searchDemand(t, s, false)
	if response.Outcome != OutcomeError || response.Error == nil {
		t.Fatalf("a held negative must refuse the strict search, got %+v", response)
	}
}

func TestKnowledgeSearchHeldNegativeDemandDegradesWithAllowDegraded(t *testing.T) {
	t.Parallel()
	s, _, _ := demandSearchHarness(t, false)
	defer s.Close()
	seedDemandHeldNegative(t, s, demandHomeWork)
	response := searchDemand(t, s, true)
	if response.Outcome != OutcomeOK {
		t.Fatalf("the suppressed demand must degrade, not fail: %+v", response)
	}
	if response.Authority != "degraded" {
		t.Fatalf("a suppressed demand failure caps the answer at degraded, got %q", response.Authority)
	}
	assertKnowledgeNotice(t, response.Omissions, "knowledge_source_demand_failed", map[string]any{
		"project_id": "d-home", "locator": "d-loc", "cause": "invalid_note_proof",
	})
	var payload struct {
		Items            []map[string]any `json:"items"`
		SourceWatermarks []map[string]any `json:"source_watermarks"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) == 0 {
		t.Fatal("a single-source answer keeps its degraded rows")
	}
	if len(payload.SourceWatermarks) != 1 || payload.SourceWatermarks[0]["authority"] != "degraded" {
		t.Fatalf("the affected source watermark must be explicit: %v", payload.SourceWatermarks)
	}
}

func TestKnowledgeSearchMultiSourceSkipsDemandFailedSourceItems(t *testing.T) {
	t.Parallel()
	s, _, source := demandSearchHarness(t, true)
	defer s.Close()
	seedDemandHeldNegative(t, s, demandHomeWork)
	response := searchDemand(t, s, true)
	if response.Outcome != OutcomeOK || response.Authority != "degraded" {
		t.Fatalf("the federated answer must degrade: outcome=%v authority=%q", response.Outcome, response.Authority)
	}
	assertKnowledgeNotice(t, response.Omissions, "knowledge_source_demand_failed", map[string]any{
		"project_id": "d-home", "locator": "d-loc", "cause": "invalid_note_proof",
	})
	var payload struct {
		Items            []map[string]any `json:"items"`
		SourceWatermarks []map[string]any `json:"source_watermarks"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) == 0 {
		t.Fatal("the demand-failed home's items must be skipped, not every item")
	}
	for _, item := range payload.Items {
		if item["home_project_id"] != source.HomeProjectID {
			t.Fatalf("only the answered source's items survive: %#v", payload.Items)
		}
	}
	bySource := map[string]string{}
	for _, mark := range payload.SourceWatermarks {
		bySource[mark["project_id"].(string)] = mark["authority"].(string)
	}
	if bySource["d-home"] != "degraded" || bySource["d-src"] != "authoritative" {
		t.Fatalf("per-source authority must follow the demand outcomes: %v", bySource)
	}
}

func TestKnowledgeSearchMultiSourceKeepsHomeWhenSourceDemandFailed(t *testing.T) {
	t.Parallel()
	s, home, _ := demandSearchHarness(t, true)
	defer s.Close()
	seedDemandHeldNegative(t, s, demandSourceWork)
	response := searchDemand(t, s, true)
	if response.Outcome != OutcomeOK || response.Authority != "degraded" {
		t.Fatalf("the federated answer must degrade when a source demand fails: outcome=%v authority=%q", response.Outcome, response.Authority)
	}
	assertKnowledgeNotice(t, response.Omissions, "knowledge_source_demand_failed", map[string]any{
		"project_id": "d-src", "locator": "d-srcl", "cause": "invalid_note_proof",
	})
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) == 0 {
		t.Fatal("the home's items must survive a failed source demand")
	}
	for _, item := range payload.Items {
		if item["home_project_id"] != home.HomeProjectID {
			t.Fatalf("the failed source's items must be skipped: %#v", payload.Items)
		}
	}
}

func assertKnowledgeNotice(t *testing.T, notices []Notice, kind string, details map[string]any) {
	t.Helper()
	for _, notice := range notices {
		if notice.Kind == kind && reflect.DeepEqual(notice.Details, details) {
			return
		}
	}
	t.Fatalf("missing %s with exact details %#v: %#v", kind, details, notices)
}

func TestKnowledgeSearchDemandFailureNoticeMarshalsWithRealisticIdentifiers(t *testing.T) {
	t.Parallel()
	s, home, _ := demandSearchHarnessWithSources(t, store.KnowledgeHome{HomeProjectID: "concord", HomeLocatorID: "canonical-path"}, store.KnowledgeHome{})
	defer s.Close()
	seedDemandHeldNegative(t, s, demandHomeWork)
	response := searchDemand(t, s, true)
	if response.Outcome != OutcomeOK || response.Authority != AuthorityDegraded {
		t.Fatalf("suppressed demand must answer degraded: %+v", response)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("realistic demand failure does not marshal: %v", err)
	}
	assertKnowledgeNotice(t, response.Omissions, "knowledge_source_demand_failed", map[string]any{
		"project_id": home.HomeProjectID, "locator": home.HomeLocatorID, "cause": "invalid_note_proof",
	})
}

func TestKnowledgeSearchDegradedSourceNoticeMarshalsWithLongLocator(t *testing.T) {
	t.Parallel()
	s, _, source := demandSearchHarnessWithSources(t,
		store.KnowledgeHome{HomeProjectID: "d-home", HomeLocatorID: "d-loc"},
		store.KnowledgeHome{HomeProjectID: "d-src", HomeLocatorID: "canonical-" + strings.Repeat("x", 80)})
	defer s.Close()
	// This repository belongs to the temporary synthetic fixture only.
	if err := os.RemoveAll(source.RepoPath); err != nil {
		t.Fatal(err)
	}
	response := searchDemand(t, s, true)
	if response.Outcome != OutcomeOK || response.Authority != AuthorityDegraded {
		t.Fatalf("unreachable source must answer degraded: %+v", response)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("long-locator degraded source does not marshal: %v", err)
	}
	assertKnowledgeNotice(t, response.Omissions, "knowledge_source_degraded", map[string]any{
		"project_id": source.HomeProjectID, "locator": source.HomeLocatorID,
	})
}
