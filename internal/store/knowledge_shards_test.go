package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAggregateAsShards(t, repo, encoded)
}

// composeWorkingTreeManifest reads the manifest the repository's working-tree
// shards compose.
func composeWorkingTreeManifest(t *testing.T, repo string) KnowledgeManifest {
	t.Helper()
	shards, err := readKnowledgeShardsWorkingTree(repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composeKnowledgeManifest(shards)
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
	return root
}

// TestComposeKnowledgeManifestReadsTheLiveShards proves the committed shard
// tree composes under the store's strict parser and carries one record per
// shard file.
func TestComposeKnowledgeManifestReadsTheLiveShards(t *testing.T) {
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
	if _, missing, err := readKnowledgeManifest(ctx, repo, bare); err != nil || !missing {
		t.Fatalf("commit with no manifest: missing=%t err=%v", missing, err)
	}

	writeManifestFixture(t, repo, manifestFixture{ID: "shard-one", Kind: "lesson", Path: "docs/lessons/shard-one.md", Status: "published", Date: "2026-09-01T00:00:00Z", Title: "Shard one", Summary: "Composed from a shard", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	run("add", ".")
	run("commit", "--quiet", "-m", "shards")
	sharded := run("rev-parse", "HEAD")
	manifest, missing, err := readKnowledgeManifest(ctx, repo, sharded)
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
	fromAggregate, missing, err := readKnowledgeManifest(ctx, repo, legacy)
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

// TestPublishLessonAddsExactlyOneShard states the outcome CD-0114 exists for:
// a publication commits the note and its record shard, and touches no other
// file, so two publications never conflict.
func TestPublishLessonAddsExactlyOneShard(t *testing.T) {
	repo := lessonRepoFixture(t)
	headBefore, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(knowledgeHeadPath)))
	if err != nil {
		t.Fatal(err)
	}
	published, err := PublishLessonRecord(context.Background(), KnowledgeHome{RepoPath: repo}, LessonPublication{
		LessonID: "lesson-one-shard", Title: "A publication adds one shard",
		Summary: "Publishing a lesson writes its record shard and nothing else.",
		Content: "# A publication adds one shard\n\nThe diff is the shard.\n",
		Tags:    []string{"knowledge"},
		Scopes:  KnowledgeRecordScopes{Mode: "home"},
		Now:     time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	out, err := exec.Command("git", "-C", repo, "diff", "--name-status", "HEAD~1", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git diff: %v\n%s", err, out)
	}
	changed := strings.Split(strings.TrimSpace(string(out)), "\n")
	sort.Strings(changed)
	want := []string{"A\t" + knowledgeRecordTree + "/lesson-one-shard.json", "A\t" + published.Record.Path}
	sort.Strings(want)
	if strings.Join(changed, "|") != strings.Join(want, "|") {
		t.Fatalf("publication changed %v, want exactly %v", changed, want)
	}
	headAfter, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(knowledgeHeadPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(headBefore) != string(headAfter) {
		t.Fatal("publication rewrote the manifest head")
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(knowledgeManifestPath))); !os.IsNotExist(err) {
		t.Fatal("publication wrote an aggregate file")
	}
}
