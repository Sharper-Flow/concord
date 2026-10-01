package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// overrideManifestBytes encodes a schema-1.3 manifest whose records and head
// exercise the CD-0194 D2 override route. The head carries the given
// overrides; the records pair one canonical anchor decision with the given
// external paths.
func overrideManifestBytes(t *testing.T, overrides []KnowledgeOperatorOverride, externalID, externalPath, externalKind string) []byte {
	t.Helper()
	manifest := KnowledgeManifest{
		SchemaVersion:     "1.3",
		SupportedKinds:    []string{"decision", "reference"},
		IndexedKinds:      []string{"decision", "reference"},
		OperatorOverrides: overrides,
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		},
		Records: []KnowledgeRecord{
			{
				ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-override.md", Status: "accepted",
				Date: "2026-09-29T00:00:00Z", Title: "Override anchor", Summary: "Carries the operator instruction", Tags: []string{},
				Authority:    KnowledgeAuthority{Tier: "legislated", LegislatedBy: "operator", ContractVersion: 1},
				Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
				HomeDomainID: "product-root:concord", ProductWideRationale: "The override route is Product-wide law.",
				SHA256: "sha256:" + strings.Repeat("a", 64),
			},
			externalRecord(externalID, externalPath, externalKind),
		},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func externalRecord(id, path, kind string) KnowledgeRecord {
	record := KnowledgeRecord{
		ID: id, Kind: kind, Path: path, Status: "published",
		Date: "2026-09-29T00:00:00Z", Title: "External record", Summary: "Operator-approved external placement", Tags: []string{},
		Authority: KnowledgeAuthority{Tier: "derived"},
		Scopes:    KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
		SHA256:    "sha256:" + strings.Repeat("b", 64),
	}
	if kind == "decision" {
		record.Status = "accepted"
		record.HomeDomainID = "product-root:concord"
		record.ProductWideRationale = "The override route is Product-wide law."
	}
	return record
}

func validOverride() KnowledgeOperatorOverride {
	return KnowledgeOperatorOverride{
		Path: "external/knowledge/", ProductID: "concord", RecordedIn: "CD-0001",
		Reason: "the operator recorded this placement for the external tree",
	}
}

// TestOverrideAdmitsExternalRecordThroughComposition is the end-to-end store
// route: the head carries a complete override, the record shards compose, and
// the composed manifest parses with the external record resolved.
func TestOverrideAdmitsExternalRecordThroughComposition(t *testing.T) {
	t.Parallel()
	shards := knowledgeShards{
		head: shardJSON(t, map[string]any{
			"schema_version":  "1.3",
			"supported_kinds": []string{"decision", "reference"},
			"indexed_kinds":   []string{"decision", "reference"},
			"operator_overrides": []map[string]any{{
				"path": "external/knowledge/", "product_id": "concord",
				"recorded_in": "CD-0001", "reason": "the operator recorded this placement for the external tree",
			}},
		}),
		registry: shardJSON(t, KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		}),
		records: map[string][]byte{
			"CD-0001.json":            shardJSON(t, canonicalAnchorShard()),
			"external-reference.json": shardJSON(t, externalShard("external-reference", "external/knowledge/reference.md")),
		},
		recordPathPrefix: manifestRecordPathPrefix,
	}
	manifest, err := composeKnowledgeManifest(shards, manifestSharedHomeRole)
	if err != nil {
		t.Fatalf("an approved external record refused to compose and parse: %v", err)
	}
	if len(manifest.OperatorOverrides) != 1 || manifest.OperatorOverrides[0].Path != "external/knowledge/" {
		t.Fatalf("composition dropped the operator override: %+v", manifest.OperatorOverrides)
	}
	found := false
	for _, record := range manifest.Records {
		if record.ID == "external-reference" {
			found = true
			if record.Path != "external/knowledge/reference.md" {
				t.Fatalf("external record path = %q", record.Path)
			}
		}
	}
	if !found {
		t.Fatal("composition lost the external record")
	}
}

func canonicalAnchorShard() map[string]any {
	return map[string]any{
		"id": "CD-0001", "kind": "decision", "path": ".concord/docs/decisions/CD-0001-override.md",
		"status": "accepted", "date": "2026-09-29T00:00:00Z", "title": "Override anchor",
		"summary": "Carries the operator instruction", "tags": []string{},
		"authority":      map[string]any{"tier": "legislated", "legislated_by": "operator", "contract_version": 1},
		"scopes":         map[string]any{"mode": "home", "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{}},
		"sha256":         "sha256:" + strings.Repeat("a", 64),
		"home_domain_id": "product-root:concord", "product_wide_rationale": "The override route is Product-wide law.",
	}
}

func externalShard(id, path string) map[string]any {
	return map[string]any{
		"id": id, "kind": "reference", "path": path, "status": "published",
		"date": "2026-09-29T00:00:00Z", "title": "External record",
		"summary": "Operator-approved external placement", "tags": []string{},
		"authority": map[string]any{"tier": "derived"},
		"scopes":    map[string]any{"mode": "home", "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{}},
		"sha256":    "sha256:" + strings.Repeat("b", 64),
	}
}

func shardJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// TestUnapprovedExternalRecordRefusesAtParse holds the refusal side: without
// a covering override the parse refuses with the override guidance, not the
// plain tier-prefix guidance.
func TestUnapprovedExternalRecordRefusesAtParse(t *testing.T) {
	t.Parallel()
	_, err := parseKnowledgeManifest(overrideManifestBytes(t, nil, "external-reference", "external/knowledge/reference.md", "reference"))
	assertFailureKind(t, err, KindInvalidNoteProof)
	if err == nil || !strings.Contains(err.Error(), "operator override") {
		t.Fatalf("unapproved external placement refused without override guidance: %v", err)
	}
}

// TestOverrideAdmissionRefusesWhenCoverageIsMissingOrUnrelated exercises the
// boundary of the coverage predicate: an override for another tree, an
// override inside .concord/, a malformed override, and a duplicate all refuse
// the external record.
func TestOverrideAdmissionRefusesWhenCoverageIsMissingOrUnrelated(t *testing.T) {
	t.Parallel()
	cases := map[string][]KnowledgeOperatorOverride{
		"another tree": {{Path: "elsewhere/", ProductID: "concord", RecordedIn: "CD-0001", Reason: "covers a different external tree entirely"}},
		"sibling":      {{Path: "external/other/", ProductID: "concord", RecordedIn: "CD-0001", Reason: "a sibling tree never narrows the override"}},
		"inside .concord": {{Path: ".concord/external/", ProductID: "concord", RecordedIn: "CD-0001",
			Reason: "an override cannot name a path inside the default tree"}},
		"short reason": {{Path: "external/knowledge/", ProductID: "concord", RecordedIn: "CD-0001", Reason: "too short"}},
		"unclean reason": {{Path: "external/knowledge/", ProductID: "concord", RecordedIn: "CD-0001",
			Reason: " padded reason is not a trimmed justification "}},
		"missing product": {{Path: "external/knowledge/", ProductID: "", RecordedIn: "CD-0001", Reason: "the override names no Product it belongs to"}},
	}
	for name, overrides := range cases {
		_, err := parseKnowledgeManifest(overrideManifestBytes(t, overrides, "external-reference", "external/knowledge/reference.md", "reference"))
		if err == nil {
			t.Errorf("%s: override admitted an external record", name)
		}
	}
	_, err := parseKnowledgeManifest(overrideManifestBytes(t, []KnowledgeOperatorOverride{validOverride(), validOverride()}, "external-reference", "external/knowledge/reference.md", "reference"))
	if err == nil {
		t.Error("duplicate override paths were admitted")
	}
}

// TestOverrideAdmissionRefusesUnsafeExternalPaths holds the closed external
// shape: traversal, non-markdown, and generated paths refuse even when an
// override covers them.
func TestOverrideAdmissionRefusesUnsafeExternalPaths(t *testing.T) {
	t.Parallel()
	for name, path := range map[string]string{
		"traversal":      "external/../secrets.md",
		"not markdown":   "external/knowledge/reference.txt",
		"generated":      "external/knowledge/generated-reference.md",
		"Generated case": "external/knowledge/Generated.md",
		"absolute":       "/etc/knowledge/reference.md",
	} {
		_, err := parseKnowledgeManifest(overrideManifestBytes(t, []KnowledgeOperatorOverride{validOverride()}, "external-reference", path, "reference"))
		if err == nil {
			t.Errorf("%s: override admitted unsafe external path %q", name, path)
		}
	}
}

// TestOverrideAdmittedDecisionKeepsCanonicalCDName mirrors the Go decision
// rule: an override-admitted decision keeps the canonical CD-NNNN filename.
func TestOverrideAdmittedDecisionKeepsCanonicalCDName(t *testing.T) {
	t.Parallel()
	approved := []KnowledgeOperatorOverride{validOverride()}
	if _, err := parseKnowledgeManifest(overrideManifestBytes(t, approved, "CD-0002", "external/knowledge/decisions/CD-0002-external.md", "decision")); err != nil {
		t.Fatalf("override-admitted decision with a canonical CD name refused: %v", err)
	}
	if _, err := parseKnowledgeManifest(overrideManifestBytes(t, approved, "CD-0002", "external/knowledge/decisions/external-decision.md", "decision")); err == nil {
		t.Fatal("an external decision without the canonical CD filename was admitted")
	}
}

// TestLegacyCommittedNoteRemainsVerifiable proves the immutable historical
// half of CD-0194 D5: a note recorded before the move keeps verifying at the
// path its commit carries.
func TestLegacyCommittedNoteRemainsVerifiable(t *testing.T) {
	repo := initKnowledgeRepo(t)
	oldPath := "docs/work/2026-08-07-legacy.md"
	writeKnowledgeFile(t, repo, oldPath, canonicalWorkNote("legacy-note", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "legacy committed note")
	if _, err := VerifyCommittedNote(context.Background(), repo, commit, oldPath, ""); err != nil {
		t.Fatalf("immutable legacy note proof refused: %v", err)
	}
	if err := validateNotePath(oldPath); err != nil {
		t.Fatalf("legacy note path refused: %v", err)
	}
}

// TestLegacyWorkNoteRemainsInScan proves the scan projects the notes a
// pre-move revision actually carries.
func TestLegacyWorkNoteRemainsInScan(t *testing.T) {
	repo := initKnowledgeRepo(t)
	oldPath := "docs/work/2026-08-07-legacy.md"
	writeKnowledgeFile(t, repo, oldPath, canonicalWorkNote("legacy-note", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "legacy committed note")
	paths, err := scanKnowledgeTree(context.Background(), KnowledgeHome{RepoPath: repo}, commit)
	if err != nil || len(paths) != 1 || paths[0] != oldPath {
		t.Fatalf("historical work-note scan: paths=%v error=%v; want [%s]", paths, err, oldPath)
	}
}

// TestLegacyNoteEditChangesContentDigest proves a legacy note edit moves the
// content identity, so the projection cannot miss it.
func TestLegacyNoteEditChangesContentDigest(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	oldPath := "docs/work/2026-08-07-legacy.md"
	writeKnowledgeFile(t, repo, oldPath, canonicalWorkNote("legacy-note", "2026-08-07T00:00:00Z"))
	first := commitKnowledgeRepo(t, repo, "legacy note")
	d1, err := knowledgeContentDigest(ctx, KnowledgeHome{RepoPath: repo}, first)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, oldPath, strings.ReplaceAll(canonicalWorkNote("legacy-note", "2026-08-07T00:00:00Z"), "Auth release", "Changed title"))
	second := commitKnowledgeRepo(t, repo, "changed legacy note")
	d2, err := knowledgeContentDigest(ctx, KnowledgeHome{RepoPath: repo}, second)
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Fatal("a legacy work-note edit is invisible to the content digest")
	}
}

// TestLegacyCompactionEventRemainsFoldable proves replay of a recorded
// compaction event that names a pre-move note path still folds.
func TestLegacyCompactionEventRemainsFoldable(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	seedEventDerivedLocator(t, s, "p", "l", repo)
	seedKnowledgeWork(t, s, "work-done", "Completed legacy work")
	payload, err := json.Marshal(compactionLinkPayload{
		ID: "work-done", Type: "work_note", Title: "Completed legacy work", CompletedAt: "2026-08-07T00:00:00Z", OutcomeTag: "shipped",
		LessonTags: []string{}, TerminalState: "completed", Summary: "Legacy durable summary", ProductIDs: []string{}, ProjectIDs: []string{},
		DomainIDs: []string{}, TagIDs: []string{}, HomeProjectID: "p", HomeLocatorID: "l", NotePath: "docs/work/legacy.md",
		CommitOID: strings.Repeat("a", 40), ContentHash: "sha256:" + strings.Repeat("b", 64), Reason: "Verified legacy compaction", ExpectedVersion: 3, ResultingVersion: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scope.close(ctx) }()
	event := Event{EventID: "legacy-compaction", Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: "work-done", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 2, Payload: payload}
	if err := foldCompactionLinkPublished(ctx, tx, event); err != nil {
		t.Fatalf("existing legacy compaction event cannot fold: %v", err)
	}
}

// TestPublishCanonicalNoteStillWritesTheCanonicalHome pins the authoring
// half: new notes land under .concord/docs/work only, so reading history in
// its own shape never becomes authoring in it.
func TestPublishCanonicalNoteStillWritesTheCanonicalHome(t *testing.T) {
	repo := initKnowledgeRepo(t)
	// git commit refuses an empty initial commit, so the fixture seeds one
	// tracked file before the publication builds on HEAD.
	writeKnowledgeFile(t, repo, ".concord/docs/work/seed.md", canonicalWorkNote("work-seed", "2026-09-28T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "seed")
	committed, err := PublishCanonicalNote(context.Background(), KnowledgeHome{RepoPath: repo}, "work-new", canonicalWorkNote("work-new", "2026-09-29T00:00:00Z"), "")
	if err != nil {
		t.Fatalf("canonical publication refused: %v", err)
	}
	if !strings.HasPrefix(committed.NotePath, ".concord/docs/work/") {
		t.Fatalf("new note authored at %q, want the canonical home", committed.NotePath)
	}
}
