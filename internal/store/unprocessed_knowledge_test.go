package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUnprocessedKnowledgeDocsFixture is a subprocess seam for the paired
// Python validator test. The fixture root comes from the test environment.
func TestUnprocessedKnowledgeDocsFixture(t *testing.T) {
	root := os.Getenv("CONCORD_KNOWLEDGE_FIXTURE_ROOT")
	if root == "" {
		t.Skip("CONCORD_KNOWLEDGE_FIXTURE_ROOT is not set")
	}
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest KnowledgeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	paths, err := UnprocessedKnowledgeDocs(manifest, root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("UNPROCESSED_JSON=%s", encoded)
}
