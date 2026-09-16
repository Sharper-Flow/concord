package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKnowledgeManifestCeilingDerivesFromTheRecordBound holds both bounds to
// their derivation instead of trusting the literals: the record bound is the
// bounded 16 KiB, and the manifest ceiling is records times the record bound
// plus the head allowance.
func TestKnowledgeManifestCeilingDerivesFromTheRecordBound(t *testing.T) {
	t.Parallel()
	if maxKnowledgeRecord != 16*1024 {
		t.Fatalf("maxKnowledgeRecord is %d, want the bounded 16 KiB", maxKnowledgeRecord)
	}
	if maxKnowledgeManifest != maxManifestRecords*maxKnowledgeRecord+256*1024 {
		t.Fatalf("maxKnowledgeManifest is %d, want maxManifestRecords * maxKnowledgeRecord plus the 256 KiB head allowance", maxKnowledgeManifest)
	}
}

// TestMaximalScalarRecordEncodesWithinTheRecordBound proves the record bound
// admits a realistic maximal record: every scalar field at its cap and one
// entry in each collection encodes within maxKnowledgeRecord.
func TestMaximalScalarRecordEncodesWithinTheRecordBound(t *testing.T) {
	t.Parallel()
	record := KnowledgeRecord{
		ID:      strings.Repeat("i", maxManifestID),
		Kind:    "decision",
		Path:    "docs/decisions/CD-0000-" + strings.Repeat("p", maxManifestPath-len("docs/decisions/CD-0000-.md")) + ".md",
		Status:  "accepted",
		Date:    "2026-09-16T00:00:00Z",
		Title:   strings.Repeat("t", maxManifestTitle),
		Summary: strings.Repeat("s", maxManifestSummary),
		Tags:    []string{strings.Repeat("g", maxManifestID)},
		Scopes: KnowledgeRecordScopes{
			Mode:       "explicit",
			ProductIDs: []string{strings.Repeat("p", maxManifestID)},
			ProjectIDs: []string{strings.Repeat("j", maxManifestID)},
			DomainIDs:  []string{strings.Repeat("d", maxManifestID)},
			TagIDs:     []string{strings.Repeat("k", maxManifestID)},
		},
		Successor:            strings.Repeat("u", maxManifestID),
		SHA256:               "sha256:" + strings.Repeat("a", 64),
		LawRelations:         []KnowledgeRelation{{Kind: "refines", TargetID: strings.Repeat("r", maxManifestID)}},
		HomeDomainID:         strings.Repeat("h", maxManifestID),
		AppliesToDomainIDs:   []string{strings.Repeat("a", maxManifestID)},
		Evidence:             []string{strings.Repeat("e", 512)},
		CriterionBindings:    []KnowledgeCriterionBinding{{Criterion: 1, Scenario: strings.Repeat("c", maxManifestID)}},
		ProductWideRationale: strings.Repeat("w", maxManifestRootHomeRationale),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxKnowledgeRecord {
		t.Fatalf("a record at the per-field caps encodes to %d bytes, above the %d-byte record bound", len(encoded), maxKnowledgeRecord)
	}
}

// TestKnowledgeRecordAtCollectionCountCapsExceedsTheRecordBound proves the
// record bound, not the per-field caps, is the binding constraint: a record
// that fills every capped collection to its count with entries at their
// per-entry caps encodes above maxKnowledgeRecord. The consequence the
// operator accepted — such a record is illegal — is held here by a test
// rather than by prose. The evidence count cap is the validator's literal 32.
func TestKnowledgeRecordAtCollectionCountCapsExceedsTheRecordBound(t *testing.T) {
	t.Parallel()
	cappedIDs := func(fill byte, count int) []string {
		values := make([]string, count)
		for index := range values {
			prefix := fmt.Sprintf("%04d-", index)
			values[index] = prefix + strings.Repeat(string(fill), maxManifestID-len(prefix))
		}
		return values
	}
	evidence := make([]string, 32)
	for index := range evidence {
		prefix := fmt.Sprintf("docs/evidence/%04d-", index)
		evidence[index] = prefix + strings.Repeat("e", 512-len(prefix))
	}
	bindings := make([]KnowledgeCriterionBinding, maxCriterionBindings)
	for index := range bindings {
		prefix := fmt.Sprintf("%04d-", index)
		bindings[index] = KnowledgeCriterionBinding{Criterion: index + 1, Scenario: prefix + strings.Repeat("c", maxManifestID-len(prefix))}
	}
	record := KnowledgeRecord{
		ID:      strings.Repeat("i", maxManifestID),
		Kind:    "spec",
		Path:    "docs/specs/" + strings.Repeat("p", maxManifestPath-len("docs/specs/.md")) + ".md",
		Status:  "accepted",
		Date:    "2026-09-16T00:00:00Z",
		Title:   strings.Repeat("t", maxManifestTitle),
		Summary: strings.Repeat("s", maxManifestSummary),
		Tags:    cappedIDs('g', maxManifestArray),
		Scopes: KnowledgeRecordScopes{
			Mode:       "explicit",
			ProductIDs: cappedIDs('p', maxManifestArray),
			ProjectIDs: cappedIDs('j', maxManifestArray),
			DomainIDs:  cappedIDs('d', maxManifestArray),
			TagIDs:     cappedIDs('k', maxManifestArray),
		},
		SHA256:             "sha256:" + strings.Repeat("a", 64),
		HomeDomainID:       strings.Repeat("h", maxManifestID),
		AppliesToDomainIDs: cappedIDs('a', maxManifestArray),
		Evidence:           evidence,
		CriterionBindings:  bindings,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= maxKnowledgeRecord {
		t.Fatalf("a record at the collection count caps encodes to only %d bytes, at or below the %d-byte record bound; the record bound would not be the binding constraint", len(encoded), maxKnowledgeRecord)
	}
}

// TestReadKnowledgeManifestAtHeadParsesTheLiveCorpus reads the working
// repository at HEAD through the bounded archive reader, so the live record
// corpus parses under the per-shard record bound and composes exactly the
// shards the record tree carries.
func TestReadKnowledgeManifestAtHeadParsesTheLiveCorpus(t *testing.T) {
	t.Parallel()
	root := repositoryRootForTest(t)
	head := strings.TrimSpace(runKnowledgeGit(t, root, "rev-parse", "HEAD"))
	manifest, missing, err := readKnowledgeManifest(context.Background(), root, head)
	if err != nil || missing {
		t.Fatalf("live corpus at HEAD: missing=%t err=%v", missing, err)
	}
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
		t.Fatalf("HEAD composed %d records from %d shard files", len(manifest.Records), shardCount)
	}
	t.Logf("live corpus at HEAD: %d records composed under the record bound", len(manifest.Records))
}

// TestReadKnowledgeManifestRefusesAnOversizedRecordShard proves the record
// bound refuses at the archive reader and names the offending shard path.
func TestReadKnowledgeManifestRefusesAnOversizedRecordShard(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	// The head shard marks the commit as sharded, so the read composes from
	// the archive instead of falling back to the legacy aggregate path.
	writeKnowledgeFile(t, repo, knowledgeHeadPath, "{}\n")
	oversized := knowledgeRecordTree + "/oversized-record.json"
	writeKnowledgeFile(t, repo, oversized, `{"id":"`+strings.Repeat("o", maxKnowledgeRecord)+`"}`)
	writeKnowledgeFile(t, repo, "README.md", "seed\n")
	head := commitKnowledgeRepo(t, repo, "oversized shard")
	_, _, err := readKnowledgeManifest(context.Background(), repo, head)
	if err == nil {
		t.Fatal("an oversized record shard parsed")
	}
	if !strings.Contains(err.Error(), oversized) {
		t.Fatalf("refusal does not name the offending shard path %s: %v", oversized, err)
	}
}
