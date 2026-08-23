package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// The record taxonomy splits six kinds into two status tiers: a law-bearing
// record is accepted, every other kind is published. These tests exercise both
// directions of that rule, and of the record/disposition exclusion, so a tier
// that silently collapses is a failure rather than a quieter manifest.

type taxonomyManifest struct {
	SchemaVersion  string            `json:"schema_version"`
	SupportedKinds []string          `json:"supported_kinds"`
	IndexedKinds   []string          `json:"indexed_kinds"`
	DomainRegistry json.RawMessage   `json:"domain_registry"`
	Dispositions   []json.RawMessage `json:"dispositions,omitempty"`
	Records        []json.RawMessage `json:"records"`
}

const taxonomyRegistry = `{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law and architecture","status":"current","architecture_relations":[]}]}`

func taxonomyRecord(t *testing.T, id, kind, path, status string, extra map[string]any) json.RawMessage {
	t.Helper()
	record := map[string]any{
		"id": id, "kind": kind, "path": path, "status": status,
		"date": "2026-08-22T00:00:00Z", "title": "Record", "summary": "Summary",
		"tags":   []string{},
		"scopes": map[string]any{"mode": "home", "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{}},
		"sha256": "sha256:" + strings.Repeat("a", 64),
	}
	for key, value := range extra {
		record[key] = value
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func taxonomyManifestBytes(t *testing.T, manifest taxonomyManifest) []byte {
	t.Helper()
	manifest.SchemaVersion = "1.2"
	manifest.DomainRegistry = json.RawMessage(taxonomyRegistry)
	manifest.SupportedKinds = []string{"work_note", "constitution", "decision", "spec", "lesson", "reference", "research"}
	manifest.IndexedKinds = manifest.SupportedKinds
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// TestRecordStatusFollowsTheKindTier is the invariant in both directions: the
// status a kind may carry is decided by its tier, so accepted is unavailable to
// a non-law kind and published is unavailable to a law-bearing one.
func TestRecordStatusFollowsTheKindTier(t *testing.T) {
	cases := []struct {
		kind      string
		path      string
		lawHome   bool
		valid     string
		forbidden string
	}{
		{kind: "constitution", path: "docs/constitution.md", lawHome: true, valid: "accepted", forbidden: "published"},
		{kind: "decision", path: "docs/decisions/CD-0001.md", lawHome: true, valid: "accepted", forbidden: "published"},
		{kind: "spec", path: "docs/spec.md", lawHome: true, valid: "accepted", forbidden: "published"},
		{kind: "lesson", path: "docs/lessons/one.md", valid: "published", forbidden: "accepted"},
		{kind: "reference", path: "docs/installation.md", valid: "published", forbidden: "accepted"},
		{kind: "research", path: "docs/market-landscape.md", valid: "published", forbidden: "accepted"},
	}
	for _, test := range cases {
		t.Run(test.kind, func(t *testing.T) {
			extra := map[string]any{}
			if test.lawHome {
				extra["home_domain_id"] = "product-root:concord"
				extra["product_wide_rationale"] = "Fixture law binds every child Domain."
			}
			valid := taxonomyManifestBytes(t, taxonomyManifest{
				Records: []json.RawMessage{taxonomyRecord(t, "record-1", test.kind, test.path, test.valid, extra)},
			})
			if _, err := parseKnowledgeManifest(valid); err != nil {
				t.Fatalf("%s with status %q rejected: %v", test.kind, test.valid, err)
			}
			forbidden := taxonomyManifestBytes(t, taxonomyManifest{
				Records: []json.RawMessage{taxonomyRecord(t, "record-1", test.kind, test.path, test.forbidden, extra)},
			})
			if _, err := parseKnowledgeManifest(forbidden); err == nil {
				t.Fatalf("%s accepted the status of the opposite tier: %q", test.kind, test.forbidden)
			}
		})
	}
}

// A non-law record carries no law-home fields, whatever its kind. The rule used
// to name lessons; reference and research are non-law for the same reason and
// must inherit it rather than acquire a law home by omission.
func TestNonLawRecordsCannotAuthorLawHomeFields(t *testing.T) {
	for kind, path := range map[string]string{
		"lesson":    "docs/lessons/one.md",
		"reference": "docs/installation.md",
		"research":  "docs/market-landscape.md",
	} {
		t.Run(kind, func(t *testing.T) {
			raw := taxonomyManifestBytes(t, taxonomyManifest{
				Records: []json.RawMessage{taxonomyRecord(t, "record-1", kind, path, "published", map[string]any{
					"home_domain_id": "product-root:concord",
				})},
			})
			if _, err := parseKnowledgeManifest(raw); err == nil {
				t.Fatalf("%s authored a law home", kind)
			}
		})
	}
}

// law_relations stay a decision/spec graph. A constitution is law-bearing for
// status purposes without joining that graph, so authoring a relation on one is
// a failure rather than an unremarked extension of the law model.
func TestLawRelationsRemainDecisionAndSpecOnly(t *testing.T) {
	raw := taxonomyManifestBytes(t, taxonomyManifest{
		Records: []json.RawMessage{
			taxonomyRecord(t, "CO-0001", "constitution", "docs/constitution.md", "accepted", map[string]any{
				"home_domain_id":         "product-root:concord",
				"product_wide_rationale": "Fixture law binds every child Domain.",
				"law_relations":          []map[string]string{{"kind": "refines", "target_id": "CD-0001"}},
			}),
			taxonomyRecord(t, "CD-0001", "decision", "docs/decisions/CD-0001.md", "accepted", map[string]any{
				"home_domain_id":         "product-root:concord",
				"product_wide_rationale": "Fixture law binds every child Domain.",
			}),
		},
	})
	if _, err := parseKnowledgeManifest(raw); err == nil {
		t.Fatal("a constitution authored a law relation")
	}
}

func TestDispositionsAreBoundedAndExcludeRecordPaths(t *testing.T) {
	disposition := func(path, kind, reason string) json.RawMessage {
		encoded, err := json.Marshal(map[string]string{"path": path, "disposition": kind, "reason": reason})
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	record := taxonomyRecord(t, "CD-0001", "decision", "docs/decisions/CD-0001.md", "accepted", map[string]any{
		"home_domain_id":         "product-root:concord",
		"product_wide_rationale": "Fixture law binds every child Domain.",
	})

	valid := taxonomyManifestBytes(t, taxonomyManifest{
		Records:      []json.RawMessage{record},
		Dispositions: []json.RawMessage{disposition("docs/scratch.md", "archived", "Superseded working note kept for provenance only.")},
	})
	manifest, err := parseKnowledgeManifest(valid)
	if err != nil {
		t.Fatalf("valid disposition rejected: %v", err)
	}
	if len(manifest.Dispositions) != 1 || manifest.Dispositions[0].Path != "docs/scratch.md" {
		t.Fatalf("disposition did not survive the parse: %+v", manifest.Dispositions)
	}

	for name, entry := range map[string]json.RawMessage{
		"record path":       disposition("docs/decisions/CD-0001.md", "archived", "Already recorded."),
		"empty reason":      disposition("docs/scratch.md", "archived", ""),
		"unclosed kind":     disposition("docs/scratch.md", "ignored", "Not a closed disposition."),
		"non markdown path": disposition("docs/scratch.txt", "archived", "The closure walk only sees markdown."),
		"absolute path":     disposition("/docs/scratch.md", "archived", "Absolute paths escape the repository."),
		"traversal path":    disposition("docs/../scratch.md", "archived", "Traversal escapes the knowledge roots."),
	} {
		t.Run(name, func(t *testing.T) {
			raw := taxonomyManifestBytes(t, taxonomyManifest{
				Records: []json.RawMessage{record}, Dispositions: []json.RawMessage{entry},
			})
			if _, err := parseKnowledgeManifest(raw); err == nil {
				t.Fatalf("invalid disposition accepted: %s", name)
			}
		})
	}

	t.Run("duplicate path", func(t *testing.T) {
		entry := disposition("docs/scratch.md", "archived", "Superseded working note.")
		raw := taxonomyManifestBytes(t, taxonomyManifest{
			Records: []json.RawMessage{record}, Dispositions: []json.RawMessage{entry, entry},
		})
		if _, err := parseKnowledgeManifest(raw); err == nil {
			t.Fatal("the same path was disposed of twice")
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		raw := taxonomyManifestBytes(t, taxonomyManifest{
			Records:      []json.RawMessage{record},
			Dispositions: []json.RawMessage{json.RawMessage(`{"path":"docs/scratch.md","disposition":"archived","reason":"Reason.","owner":"operator"}`)},
		})
		if _, err := parseKnowledgeManifest(raw); err == nil {
			t.Fatal("a disposition carried an undeclared field")
		}
	})
}
