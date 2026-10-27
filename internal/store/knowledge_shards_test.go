package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// writeAggregateAsShards lays an aggregate-shaped manifest document out as the
// shard tree the store composes (CD-0114): head fields, the domain registry,
// and one record shard per record. Fixtures keep authoring the aggregate
// shape because it is the shape the parser validates; the tree is how the
// repository commits it.
func writeAggregateAsShards(t *testing.T, repo string, aggregate []byte) {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(aggregate, &document); err != nil {
		t.Fatalf("aggregate fixture is not a JSON object: %v", err)
	}
	head := map[string]json.RawMessage{}
	for key, value := range document {
		if key != "domain_registry" && key != "records" {
			head[key] = value
		}
	}
	headBytes, err := json.MarshalIndent(head, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeHeadPath, string(headBytes)+"\n")
	if registry, ok := document["domain_registry"]; ok {
		var indented map[string]any
		if err := json.Unmarshal(registry, &indented); err != nil {
			t.Fatal(err)
		}
		registryBytes, err := json.MarshalIndent(indented, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeKnowledgeFile(t, repo, knowledgeRegistryPath, string(registryBytes)+"\n")
	}
	recordDir := filepath.Join(repo, filepath.FromSlash(knowledgeRecordTree))
	if err := os.RemoveAll(recordDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if raw, ok := document["records"]; ok {
		if err := json.Unmarshal(raw, &records); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range records {
		id, _ := record["id"].(string)
		if id == "" {
			t.Fatalf("record fixture has no id: %v", record)
		}
		if _, present := record["authority"]; !present {
			tier := "derived"
			if kind, _ := record["kind"].(string); kind == "constitution" || kind == "decision" {
				tier = "legislated"
				record["authority"] = map[string]any{"tier": tier, "legislated_by": "work-0f1e2d3c4b5a6978", "contract_version": 1}
			} else {
				record["authority"] = map[string]any{"tier": tier}
			}
		}
		shard, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeKnowledgeFile(t, repo, knowledgeRecordTree+"/"+id+".json", string(shard)+"\n")
	}
}

// writeManifestShards writes a parsed manifest as the shard tree.
func writeManifestShards(t *testing.T, repo string, manifest KnowledgeManifest) {
	t.Helper()
	for index := range manifest.Records {
		if manifest.Records[index].Authority.Tier == "" {
			manifest.Records[index].Authority = KnowledgeAuthority{Tier: "derived"}
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAggregateAsShards(t, repo, encoded)
}

// readKnowledgeShardsWorkingTree reads the shard tree from the checked-out
// repository, the form lesson publication edits before it commits. It lives in
// the test build: only the shard-composition fixtures read the working tree.
func readKnowledgeShardsWorkingTree(repo string) (knowledgeShards, error) {
	head, err := os.ReadFile(path.Join(repo, knowledgeHeadPath)) //nolint:gosec // knowledgeHeadPath is fixed and repo is the operator-selected Git authority.
	if err != nil {
		return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot read the knowledge manifest head", true, "restore the git knowledge home", err)
	}
	shards := knowledgeShards{head: head, records: map[string][]byte{}, recordPathPrefix: manifestRecordPathPrefix}
	if registry, err := os.ReadFile(path.Join(repo, knowledgeRegistryPath)); err == nil { //nolint:gosec // knowledgeRegistryPath is fixed and repo is the operator-selected Git authority.
		shards.registry = registry
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot read the domain registry shard", true, "restore the git knowledge home", err)
	}
	entries, err := os.ReadDir(path.Join(repo, knowledgeRecordTree))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot list the knowledge record shards", true, "restore the git knowledge home", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(path.Join(repo, knowledgeRecordTree, entry.Name())) //nolint:gosec // the record tree is fixed and repo is the operator-selected Git authority.
		if err != nil {
			return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot read a knowledge record shard", true, "restore the git knowledge home", err)
		}
		shards.records[entry.Name()] = content
	}
	return shards, nil
}

// composeWorkingTreeManifest reads the manifest the repository's working-tree
// shards compose.
func composeWorkingTreeManifest(t *testing.T, repo string) KnowledgeManifest {
	t.Helper()
	shards, err := readKnowledgeShardsWorkingTree(repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards, manifestSharedHomeRole)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func repositoryRootForTest(t *testing.T) string {
	t.Helper()
	root := "."
	if _, err := os.Stat(filepath.Join(root, "scripts/generate-knowledge-index.py")); err != nil {
		root = filepath.Join("..", "..")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestComposeKnowledgeManifestReadsTheLiveShards proves the committed shard
// tree composes under the store's strict parser and carries one record per
// shard file.
// TestParseRefusesSchema13WithoutAuthority holds the other half of the
// migration. 1.3 is the version a corpus adopts once it has classified every
// record, so an absent tier there is an authoring defect and not a legacy
// shape. Defaulting it would let a corpus claim the current version while
// leaving its law unclassified.
func TestParseRefusesSchema13WithoutAuthority(t *testing.T) {
	t.Parallel()
	_, err := parseKnowledgeManifest(tierlessManifestBytes(t, "1.3"))
	if err == nil {
		t.Fatal("a schema 1.3 corpus without an authority object was accepted")
	}
	assertFailureKind(t, err, KindInvalidNoteProof)
}

// TestParseRefusesAnUnknownSchemaVersion holds that the accepted set stays
// closed. The declared version is the only signal that separates a corpus
// carrying tiers from one that predates them, so an unrecognized version is
// refused rather than guessed at.
func TestParseRefusesAnUnknownSchemaVersion(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1.1", "1.4", "2.0", ""} {
		t.Run(version, func(t *testing.T) {
			if _, err := parseKnowledgeManifest(tierlessManifestBytes(t, version)); err == nil {
				t.Fatalf("schema version %q was accepted", version)
			}
		})
	}
}

func TestComposeKnowledgeManifestReadsTheLiveShards(t *testing.T) {
	t.Parallel()
	root := repositoryRootForTest(t)
	manifest := composeWorkingTreeManifest(t, root)
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(knowledgeRecordTree)))
	if err != nil {
		t.Fatal(err)
	}
	shardCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			shardCount++
		}
	}
	if len(manifest.Records) != shardCount || shardCount == 0 {
		t.Fatalf("composed %d records from %d shard files", len(manifest.Records), shardCount)
	}
	ids := make([]string, 0, len(manifest.Records))
	for _, record := range manifest.Records {
		ids = append(ids, record.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("composed records are not sorted by id")
	}
	if len(manifest.KnowledgeRoots) == 0 {
		t.Fatal("composed manifest lost the head's knowledge_roots")
	}
}

// TestReadKnowledgeManifestAtCommitPrefersShardsAndReadsLegacyAggregates
// covers the three shapes a commit can have: shards, the aggregate file that
// predates them, and neither.
func TestReadKnowledgeManifestAtCommitPrefersShardsAndReadsLegacyAggregates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--quiet", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Shard Test")

	writeKnowledgeFile(t, repo, "README.md", "seed\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "no manifest")
	bare := run("rev-parse", "HEAD")
	if _, missing, err := readKnowledgeManifest(ctx, repo, bare, manifestSharedHomeRole); err != nil || !missing {
		t.Fatalf("commit with no manifest: missing=%t err=%v", missing, err)
	}

	writeManifestFixture(t, repo, manifestFixture{ID: "shard-one", Kind: "lesson", Path: ".concord/docs/lessons/shard-one.md", Status: "published", Date: "2026-09-01T00:00:00Z", Title: "Shard one", Summary: "Composed from a shard", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	run("add", ".")
	run("commit", "--quiet", "-m", "shards")
	sharded := run("rev-parse", "HEAD")
	manifest, missing, err := readKnowledgeManifest(ctx, repo, sharded, manifestSharedHomeRole)
	if err != nil || missing || len(manifest.Records) != 1 || manifest.Records[0].ID != "shard-one" {
		t.Fatalf("sharded commit: missing=%t err=%v records=%+v", missing, err, manifest.Records)
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(knowledgeManifestPath))); !os.IsNotExist(err) {
		t.Fatal("the shard fixture must not write the aggregate file")
	}

	// A commit that predates the shards carries the aggregate alone.
	aggregate, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	run("rm", "-r", "--quiet", knowledgeShardRoot)
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(aggregate)+"\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "legacy aggregate")
	legacy := run("rev-parse", "HEAD")
	fromAggregate, missing, err := readKnowledgeManifest(ctx, repo, legacy, manifestSharedHomeRole)
	if err != nil || missing || len(fromAggregate.Records) != 1 || fromAggregate.Records[0].ID != "shard-one" {
		t.Fatalf("legacy commit: missing=%t err=%v records=%+v", missing, err, fromAggregate.Records)
	}

	// The digest moves across every shape change.
	home := KnowledgeHome{RepoPath: repo}
	digests := map[string]bool{}
	for _, commit := range []string{bare, sharded, legacy} {
		digest, err := knowledgeContentDigest(ctx, home, commit)
		if err != nil {
			t.Fatal(err)
		}
		digests[digest] = true
	}
	if len(digests) != 3 {
		t.Fatalf("content digests did not distinguish the three shapes: %v", digests)
	}
}

// TestShardRoundTripPreservesV12LawHomes proves a law record written as a
// shard composes back with its Domain home intact.
func TestShardRoundTripPreservesV12LawHomes(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	manifest := KnowledgeManifest{
		SchemaVersion: "1.2", SupportedKinds: []string{"decision", "lesson"}, IndexedKinds: []string{"decision", "lesson"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "store", Name: "Store", Purpose: "Storage", ParentDomainID: "product-root:concord", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{{
			ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-law.md", Status: "accepted", Date: "2026-08-18T00:00:00Z",
			Title: "Law", Summary: "A current law retains its Domain ownership across the shard round trip.", Tags: []string{},
			Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
			HomeDomainID: "product-root:concord", AppliesToDomainIDs: []string{"store"}, SHA256: "sha256:" + strings.Repeat("a", 64),
			ProductWideRationale: "Ownership survives shard composition across every child Domain.",
		}},
	}
	writeManifestShards(t, repo, manifest)
	parsed := composeWorkingTreeManifest(t, repo)
	if got := parsed.Records[0]; got.HomeDomainID != "product-root:concord" || len(got.AppliesToDomainIDs) != 1 || got.AppliesToDomainIDs[0] != "store" {
		t.Fatalf("law homes lost during the shard round trip: %+v", got)
	}
}

// TestShardTreeComposesIdenticallyInGoAndPython proves the store and
// scripts/knowledge_index.py compose the same manifest from the same shards.
func TestShardTreeComposesIdenticallyInGoAndPython(t *testing.T) {
	t.Parallel()
	repoRoot := repositoryRootForTest(t)
	goManifest := composeWorkingTreeManifest(t, repoRoot)
	cmd := exec.Command("python3", "-c", "import sys, json; sys.path.insert(0, 'scripts'); import knowledge_index; sys.stdout.write(knowledge_index.compose_manifest_bytes().decode('utf-8'))")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("python composition failed: %v", err)
	}
	pyManifest, err := parseKnowledgeManifest(output)
	if err != nil {
		t.Fatalf("python composition is not runtime-readable: %v", err)
	}
	if len(pyManifest.Records) != len(goManifest.Records) {
		t.Fatalf("python composed %d records, Go composed %d", len(pyManifest.Records), len(goManifest.Records))
	}
	for i := range goManifest.Records {
		if goManifest.Records[i].ID != pyManifest.Records[i].ID || goManifest.Records[i].SHA256 != pyManifest.Records[i].SHA256 {
			t.Fatalf("record %d differs: go=%s/%s python=%s/%s", i, goManifest.Records[i].ID, goManifest.Records[i].SHA256, pyManifest.Records[i].ID, pyManifest.Records[i].SHA256)
		}
	}
	if !reflect.DeepEqual(goManifest.KnowledgeRoots, pyManifest.KnowledgeRoots) || !reflect.DeepEqual(goManifest.Exclusions, pyManifest.Exclusions) {
		t.Fatal("head fields differ between the Go and Python compositions")
	}
}
