package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// anchorDocument is the decision document an override anchors to. withBlock
// appends the closed instruction block naming the given Product and path.
func anchorDocument(product string, withBlock bool) string {
	document := "# Override anchor\n\nThe operator recorded the instruction below.\n"
	if !withBlock {
		return document
	}
	return document + "\n<!-- concord-operator-override\nproduct: " + product + "\npath: external/knowledge/" + "\ndecision: approve\n-->\n"
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// anchorGateManifest builds a manifest with one override and one anchor
// record whose sha256 is caller-controlled, so each refusal class can aim at
// exactly one broken boundary.
func anchorGateManifest(anchor KnowledgeRecord, overrides []KnowledgeOperatorOverride) KnowledgeManifest {
	return KnowledgeManifest{
		SchemaVersion:     "1.3",
		SupportedKinds:    []string{"decision", "reference"},
		IndexedKinds:      []string{"decision", "reference"},
		OperatorOverrides: overrides,
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		},
		Records: []KnowledgeRecord{anchor},
	}
}

func anchorRecord(document string) KnowledgeRecord {
	return KnowledgeRecord{
		ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-override.md", Status: "accepted",
		Date: "2026-09-29T00:00:00Z", Title: "Override anchor", Summary: "Carries the operator instruction", Tags: []string{},
		Authority:    KnowledgeAuthority{Tier: "legislated", LegislatedBy: "operator", ContractVersion: 1},
		Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
		HomeDomainID: "product-root:concord", ProductWideRationale: "The override route is Product-wide law.",
		SHA256: sha256Hex(document),
	}
}

func wantAnchorRefusal(t *testing.T, err error, detail string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an anchor refusal (%s), got nil", detail)
	}
	if !strings.Contains(err.Error(), detail) {
		t.Fatalf("refusal does not name the defect %q: %v", detail, err)
	}
}

// TestValidateOverrideAnchorsRefusalClasses walks every boundary the anchor
// gate owns: a missing record, a non-decision, a superseded decision, an
// unreadable document, a hash mismatch, an absent block, a malformed block, a
// denial, and an instruction for another Product all refuse; the complete
// anchor admits.
func TestValidateOverrideAnchorsRefusalClasses(t *testing.T) {
	t.Parallel()
	document := anchorDocument("concord", true)
	read := func(string) ([]byte, error) { return []byte(document), nil }
	override := KnowledgeOperatorOverride{Path: "external/knowledge/", ProductID: "concord", RecordedIn: "CD-0001", Reason: "the operator recorded this placement for the external tree"}

	if err := validateOverrideAnchors(anchorGateManifest(anchorRecord(document), []KnowledgeOperatorOverride{override}), read); err != nil {
		t.Fatalf("a complete anchor refused: %v", err)
	}

	missing := override
	missing.RecordedIn = "CD-4096"
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(anchorRecord(document), []KnowledgeOperatorOverride{missing}), read), "names no manifest record")

	nonDecision := anchorRecord(document)
	nonDecision.Kind = "reference"
	nonDecision.Status = "published"
	nonDecision.Authority = KnowledgeAuthority{Tier: "derived"}
	nonDecision.HomeDomainID = ""
	nonDecision.ProductWideRationale = ""
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(nonDecision, []KnowledgeOperatorOverride{override}), read), "only a decision carries operator override authority")

	superseded := anchorRecord(document)
	superseded.Status = "superseded"
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(superseded, []KnowledgeOperatorOverride{override}), read), "only an accepted decision carries operator override authority")

	unreadable := func(string) ([]byte, error) { return nil, errors.New("unreadable") }
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(anchorRecord(document), []KnowledgeOperatorOverride{override}), unreadable), "cannot be read")

	hashMismatch := anchorRecord(document)
	hashMismatch.SHA256 = "sha256:" + strings.Repeat("f", 64)
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(hashMismatch, []KnowledgeOperatorOverride{override}), read), "does not match the record's immutable hash proof")

	noBlock := anchorDocument("concord", false)
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(anchorRecord(noBlock), []KnowledgeOperatorOverride{override}), func(string) ([]byte, error) { return []byte(noBlock), nil }), "carries no operator instruction for Product")

	malformed := document + "\n<!-- concord-operator-override\nnote: stray field\ndecision: approve\n-->\n"
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(anchorRecord(malformed), []KnowledgeOperatorOverride{override}), func(string) ([]byte, error) { return []byte(malformed), nil }), "instruction is malformed")

	denied := strings.Replace(document, "decision: approve", "decision: deny", 1)
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(anchorRecord(denied), []KnowledgeOperatorOverride{override}), func(string) ([]byte, error) { return []byte(denied), nil }), "records a denial, not an approval")

	crossProduct := anchorDocument("other-product", true)
	wantAnchorRefusal(t, validateOverrideAnchors(anchorGateManifest(anchorRecord(crossProduct), []KnowledgeOperatorOverride{override}), func(string) ([]byte, error) { return []byte(crossProduct), nil }), "carries no operator instruction for Product concord")
}

// TestFencedInstructionExampleNeverAdmits proves the parser reads only real
// HTML comment blocks: an instruction shown inside a fenced code example is
// documentation, not a grant.
func TestFencedInstructionExampleNeverAdmits(t *testing.T) {
	t.Parallel()
	block := "<!-- concord-operator-override\nproduct: concord\npath: external/knowledge/\ndecision: approve\n-->\n"
	fenced := "# Grammar\n\n```\n" + block + "```\n"
	instructions, malformations := parseOverrideInstructionBlocks([]byte(fenced))
	if len(instructions) != 0 || len(malformations) != 0 {
		t.Fatalf("fenced example parsed as an instruction: %v %v", instructions, malformations)
	}
	fenced = "# Grammar\n\n~~~\n" + block + "~~~\n"
	instructions, malformations = parseOverrideInstructionBlocks([]byte(fenced))
	if len(instructions) != 0 || len(malformations) != 0 {
		t.Fatalf("tilde-fenced example parsed as an instruction: %v %v", instructions, malformations)
	}
	// A real block after the fenced example still parses.
	mixed := fenced + "\n" + block
	instructions, malformations = parseOverrideInstructionBlocks([]byte(mixed))
	if len(malformations) != 0 || len(instructions) != 1 || instructions[0].decision != "approve" {
		t.Fatalf("real block beside a fenced example parsed wrong: %v %v", instructions, malformations)
	}
}

// overrideCommitRepo builds a repository whose HEAD carries the complete
// shard tree for an override-backed manifest, plus the anchor decision
// document and the external placement file. The anchor record's shard carries
// the caller-supplied hash proof, so a tampered fixture can aim the refusal
// at the hash boundary.
func overrideCommitRepo(t *testing.T, anchorBody string, anchorSHA string) (string, string) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	registry := map[string]any{
		"schema_version": "1.0", "product_key": "concord", "root_domain_id": "product-root:concord",
		"domains": []map[string]any{{
			"domain_id": "product-root:concord", "name": "Concord", "purpose": "Product-wide law", "status": "current", "architecture_relations": []string{},
		}},
	}
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", string(encoded)+"\n")
	head := map[string]any{
		"schema_version":  "1.3",
		"supported_kinds": []string{"decision", "reference"},
		"indexed_kinds":   []string{"decision", "reference"},
		"operator_overrides": []map[string]any{{
			"path": "external/knowledge/", "product_id": "concord",
			"recorded_in": "CD-0001", "reason": "the operator recorded this placement for the external tree",
		}},
	}
	encoded, err = json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/manifest.json", string(encoded)+"\n")
	anchor := canonicalAnchorShard()
	anchor["sha256"] = anchorSHA
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/records/CD-0001.json", string(shardJSON(t, anchor))+"\n")
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/records/external-reference.json", string(shardJSON(t, externalShard("external-reference", "external/knowledge/reference.md")))+"\n")
	writeKnowledgeFile(t, repo, ".concord/docs/decisions/CD-0001-override.md", anchorBody)
	writeKnowledgeFile(t, repo, "external/knowledge/reference.md", "external knowledge body\n")
	return repo, commitKnowledgeRepo(t, repo, "override-backed manifest")
}

// TestCommittedReaderProvesOverrideAnchors is the end-to-end committed read:
// the manifest composes and the anchor proves out at the commit, and a
// tampered anchor hash or a missing instruction refuses before the manifest
// is used.
func TestCommittedReaderProvesOverrideAnchors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	document := anchorDocument("concord", true)
	repo, commit := overrideCommitRepo(t, document, sha256Hex(document))
	manifest, missing, err := readKnowledgeManifest(ctx, repo, commit)
	if err != nil || missing {
		t.Fatalf("a committed override-backed manifest refused to read: missing=%v err=%v", missing, err)
	}
	if len(manifest.OperatorOverrides) != 1 || len(manifest.Records) != 2 {
		t.Fatalf("committed manifest lost the override or records: %+v", manifest)
	}

	brokenRepo, brokenCommit := overrideCommitRepo(t, document, sha256Hex(document+"tampered\n"))
	_, _, err = readKnowledgeManifest(ctx, brokenRepo, brokenCommit)
	wantAnchorRefusal(t, err, "does not match the record's immutable hash proof")

	blocklessDoc := anchorDocument("concord", false)
	blocklessRepo, blocklessCommit := overrideCommitRepo(t, blocklessDoc, sha256Hex(blocklessDoc))
	_, _, err = readKnowledgeManifest(ctx, blocklessRepo, blocklessCommit)
	wantAnchorRefusal(t, err, "carries no operator instruction for Product concord")
}

// TestCrossProductOverrideRefusesAtParse holds the CD-0194 D2 Product
// boundary in the parser: an override recorded for another Product never
// admits this manifest's external placement.
func TestCrossProductOverrideRefusesAtParse(t *testing.T) {
	t.Parallel()
	foreign := validOverride()
	foreign.ProductID = "other-product"
	_, err := parseKnowledgeManifest(overrideManifestBytes(t, []KnowledgeOperatorOverride{foreign}, "external-reference", "external/knowledge/reference.md", "reference"))
	wantAnchorRefusal(t, err, "names Product other-product, not this manifest's owning Product concord")
}
