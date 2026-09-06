package pm1fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sharper-flow/concord/internal/store"
)

// The fixture repositories commit the knowledge manifest as the shard tree the
// store composes (CD-0114): a head shard, the domain registry, and one record
// shard per record under docs/knowledge/records.
const (
	knowledgeHeadPath     = "docs/knowledge/manifest.json"
	knowledgeRegistryPath = "docs/knowledge/domain-registry.json"
	knowledgeRecordTree   = "docs/knowledge/records"
)

// writeKnowledgeShards lays a manifest out as its shard tree, replacing any
// record shards already present.
func writeKnowledgeShards(repo string, manifest store.KnowledgeManifest) error {
	return WriteKnowledgeShards(repo, manifest)
}

// WriteKnowledgeShards lays a manifest out as the shard tree the store composes,
// replacing any record shards already present. Test fixtures in other
// packages seed their knowledge homes through it.
func WriteKnowledgeShards(repo string, manifest store.KnowledgeManifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("pm1fixture: marshal knowledge manifest: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("pm1fixture: decode knowledge manifest: %w", err)
	}
	head := map[string]json.RawMessage{}
	for key, value := range document {
		if key != "domain_registry" && key != "records" {
			head[key] = value
		}
	}
	headBytes, err := json.MarshalIndent(head, "", "  ")
	if err != nil {
		return fmt.Errorf("pm1fixture: encode knowledge manifest head: %w", err)
	}
	if err := writeKnowledgeFile(repo, knowledgeHeadPath, string(headBytes)+"\n"); err != nil {
		return fmt.Errorf("pm1fixture: write knowledge manifest head: %w", err)
	}
	if registry, ok := document["domain_registry"]; ok {
		if err := writeKnowledgeFile(repo, knowledgeRegistryPath, string(registry)+"\n"); err != nil {
			return fmt.Errorf("pm1fixture: write domain registry shard: %w", err)
		}
	}
	recordDir := filepath.Join(repo, filepath.FromSlash(knowledgeRecordTree))
	if err := os.RemoveAll(recordDir); err != nil {
		return fmt.Errorf("pm1fixture: clear record shards: %w", err)
	}
	for _, record := range manifest.Records {
		shard, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return fmt.Errorf("pm1fixture: encode record shard %s: %w", record.ID, err)
		}
		if err := writeKnowledgeFile(repo, knowledgeRecordTree+"/"+record.ID+".json", string(shard)+"\n"); err != nil {
			return fmt.Errorf("pm1fixture: write record shard %s: %w", record.ID, err)
		}
	}
	return nil
}

// readKnowledgeShards composes the manifest the fixture repository's shards
// describe, without the store's strict validation: fixtures edit and re-write
// it, and the store validates the result when it reads the commit.
func readKnowledgeShards(repo string) (store.KnowledgeManifest, error) {
	var manifest store.KnowledgeManifest
	head, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(knowledgeHeadPath))) //nolint:gosec // knowledgeHeadPath is fixed and repo is a fixture repository this package created.
	if err != nil {
		return manifest, fmt.Errorf("pm1fixture: read knowledge manifest head: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(head, &document); err != nil {
		return manifest, fmt.Errorf("pm1fixture: decode knowledge manifest head: %w", err)
	}
	if registry, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(knowledgeRegistryPath))); err == nil { //nolint:gosec // knowledgeRegistryPath is fixed and repo is a fixture repository this package created.
		document["domain_registry"] = registry
	}
	entries, err := os.ReadDir(filepath.Join(repo, filepath.FromSlash(knowledgeRecordTree)))
	if err != nil && !os.IsNotExist(err) {
		return manifest, fmt.Errorf("pm1fixture: list record shards: %w", err)
	}
	records := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		shard, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(knowledgeRecordTree), entry.Name())) //nolint:gosec // the record tree is fixed and repo is a fixture repository this package created.
		if err != nil {
			return manifest, fmt.Errorf("pm1fixture: read record shard %s: %w", entry.Name(), err)
		}
		records = append(records, shard)
	}
	encodedRecords, err := json.Marshal(records)
	if err != nil {
		return manifest, fmt.Errorf("pm1fixture: encode record shards: %w", err)
	}
	document["records"] = encodedRecords
	composed, err := json.Marshal(document)
	if err != nil {
		return manifest, fmt.Errorf("pm1fixture: compose knowledge manifest: %w", err)
	}
	if err := json.Unmarshal(composed, &manifest); err != nil {
		return manifest, fmt.Errorf("pm1fixture: parse composed knowledge manifest: %w", err)
	}
	return manifest, nil
}
