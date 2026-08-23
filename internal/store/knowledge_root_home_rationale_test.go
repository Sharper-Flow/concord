package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The root Domain is the only home a record can carry without deciding
// anything, and CD-0041 D2 makes it legitimate for Product-wide law. Those two
// facts together mean a record that defaulted to the root and a record that
// belongs there are indistinguishable once written. CD-0059 reached the root
// exactly that way.
//
// These tests hold the rule that closes it: a root home is a claim, and the
// claim is carried in the record. Omission is not a way to reach the root.

func rootHomeRecord(t *testing.T, extra map[string]any) []byte {
	t.Helper()
	return taxonomyManifestBytes(t, taxonomyManifest{
		Records: []json.RawMessage{taxonomyRecord(t, "CD-0001", "decision", "docs/decisions/CD-0001.md", "accepted", extra)},
	})
}

// A root home without a stated reason is refused. This is the direction that
// matters: it is the shape every silently-defaulted record takes.
func TestRootHomeRequiresProductWideRationale(t *testing.T) {
	manifest := rootHomeRecord(t, map[string]any{"home_domain_id": "product-root:concord"})
	if _, err := parseKnowledgeManifest(manifest); err == nil {
		t.Fatal("a record homed to the root Domain was accepted without a product-wide rationale")
	}
}

func TestRootHomeAcceptsAStatedProductWideRationale(t *testing.T) {
	manifest := rootHomeRecord(t, map[string]any{
		"home_domain_id":         "product-root:concord",
		"product_wide_rationale": "Binds every child Domain; no single child owns the constraint.",
	})
	if _, err := parseKnowledgeManifest(manifest); err != nil {
		t.Fatalf("a root home with a stated rationale was rejected: %v", err)
	}
}

// The field is scoped to the claim it carries. A child-homed record has already
// decided, so a rationale there would assert a Product-wide reach the home
// contradicts.
func TestChildHomeCannotCarryProductWideRationale(t *testing.T) {
	registry := `{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[` +
		`{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law","status":"current","architecture_relations":[]},` +
		`{"domain_id":"workflow-engine","name":"Workflow engine","purpose":"Work lifecycle","parent_domain_id":"product-root:concord","status":"current","architecture_relations":[]}]}`
	manifest := taxonomyManifest{
		Records: []json.RawMessage{taxonomyRecord(t, "CD-0001", "decision", "docs/decisions/CD-0001.md", "accepted", map[string]any{
			"home_domain_id":         "workflow-engine",
			"product_wide_rationale": "Binds every child Domain.",
		})},
	}
	encoded := taxonomyManifestBytes(t, manifest)
	encoded = []byte(strings.Replace(string(encoded), taxonomyRegistry, registry, 1))
	if _, err := parseKnowledgeManifest(encoded); err == nil {
		t.Fatal("a child-homed record was allowed to claim Product-wide scope")
	}
}

// An empty or whitespace rationale is omission wearing the field's name.
func TestRootHomeRationaleMustCarryContent(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t\n"} {
		manifest := rootHomeRecord(t, map[string]any{
			"home_domain_id":         "product-root:concord",
			"product_wide_rationale": blank,
		})
		if _, err := parseKnowledgeManifest(manifest); err == nil {
			t.Fatalf("a blank product-wide rationale (%q) was accepted as a stated reason", blank)
		}
	}
}

// The CD-0041 D9.2 upcast assigns the root to legacy law carrying zero or
// several component IDs. That home is undecided by construction, not claimed.
// The migration must therefore say so in the record rather than emit a silent
// root home, which is the shape this change exists to remove.
func TestLegacyMigrationMarksUndecidedRootHomes(t *testing.T) {
	legacy := KnowledgeManifest{
		SchemaVersion:  "1.1",
		SupportedKinds: []string{"work_note", "constitution", "decision", "spec", "lesson", "reference", "research"},
		IndexedKinds:   []string{"work_note", "constitution", "decision", "spec", "lesson", "reference", "research"},
		Records: []KnowledgeRecord{{
			ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001.md", Status: "accepted",
			Date: "2026-08-22T00:00:00Z", Title: "Record", Summary: "Summary", Tags: []string{},
			SHA256: "sha256:" + strings.Repeat("a", 64),
			Scopes: KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, TagIDs: []string{}, ComponentIDs: []string{}},
		}},
	}
	migrated, err := MigrateLegacyKnowledgeManifest(legacy, "concord")
	if err != nil {
		t.Fatalf("migration rejected a legacy manifest: %v", err)
	}
	record := migrated.Records[0]
	if record.HomeDomainID != "product-root:concord" {
		t.Fatalf("legacy law with no component IDs should home to the root, got %q", record.HomeDomainID)
	}
	if record.ProductWideRationale != UndecidedRootHomeRationale {
		t.Fatalf("migrated root home must be marked undecided, got %q", record.ProductWideRationale)
	}
}

// The claim has to survive the projection, not merely the parse. PM1.Q10
// rebuilds the declared record out of SQLite and requires byte equality with
// the manifest, so a field the projection drops turns every root-homed record
// into a Q10 failure against its own declaration. law_domain_homes carried no
// rationale column before migration 45.
func TestRootHomeRationaleSurvivesTheQ10Projection(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	decision := "docs/decisions/CD-0001.md"
	writeManifestFixture(t, repo, manifestFixture{
		ID: "decision-root", Kind: "decision", Path: decision, Status: "accepted",
		Date: "2026-08-10T00:00:00Z", Title: "Decision", Summary: "Decision summary",
		Tags: []string{"sqlite"}, Scopes: KnowledgeRecordScopes{Mode: "home"}, RootHomed: true,
	})
	commit := commitKnowledgeRepo(t, repo, "root-homed law")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatalf("rebuild rejected a root-homed record: %v", err)
	}
	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "decision-root"})
	if err != nil || q10.Status != "canonical" || q10.Note == nil || q10.Note.CommitOID != commit {
		t.Fatalf("root-homed law failed its own Q10 proof: q10=%#v err=%v", q10, err)
	}
}
