package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The package map and the coverage anchors are architecture data, not law
// (CD-0222 D2). The knowledge index rebuild reads them for the Product's
// designated knowledge home at the scanned commit and refuses the whole
// rebuild when either is malformed, the way a malformed record shard
// refuses it. A registered source home carries neither in this version.
const (
	packageMapSchemaVersion = "1.0"
	packageMapShapeVersion  = "1.1"
	maxPackageMapBytes      = 16 * 1024
	maxPackageMapEntries    = 512
	maxPackageMapDomains    = 16
	maxPackageMapPath       = 256
	maxCoverageAnchors      = 32
	maxCoverageAnchorValue  = 512
	maxCoverageReason       = 1024
)

// coverageStateObligation is the field each coverage state requires. The
// other two obligation fields are forbidden (CD-0047 D2).
var coverageStateObligation = map[string]string{
	"satisfied":    "evidence",
	"outstanding":  "issue",
	"unmeasured":   "reason",
	"out_of_scope": "reason",
}

var coverageAnchorKinds = map[string]bool{"adapter_test": true, "generated": true, "go_test": true, "scenario": true, "validator": true}

var coverageIssuePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)

type knowledgePackageEntry struct {
	Path          string   `json:"path"`
	DomainIDs     []string `json:"domain_ids"`
	Role          string   `json:"role,omitempty"`
	OwnerDomainID string   `json:"owner_domain_id,omitempty"`
}

// MarshalJSON retains the historical field order and mandatory domain_ids for
// 1.0 entries. Explicit roles encode only the ownership fields their role uses.
func (entry knowledgePackageEntry) MarshalJSON() ([]byte, error) {
	if entry.Role == "" {
		type historical knowledgePackageEntry
		return json.Marshal(historical(entry))
	}
	return json.Marshal(struct {
		Path          string   `json:"path"`
		Role          string   `json:"role"`
		OwnerDomainID string   `json:"owner_domain_id,omitempty"`
		DomainIDs     []string `json:"domain_ids,omitempty"`
	}{entry.Path, entry.Role, entry.OwnerDomainID, entry.DomainIDs})
}

type knowledgePackageMapDocument struct {
	SchemaVersion string                  `json:"schema_version"`
	Packages      []knowledgePackageEntry `json:"packages"`
}

type knowledgeCoverageAnchor struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// knowledgeArchitecture is what one rebuild projects beside the law rows:
// the package map (MapHash empty when the commit carries none) and the
// typed coverage anchors of each satisfied record.
type knowledgeArchitecture struct {
	MapHash  string
	Packages []knowledgePackageEntry
	Anchors  map[string][]knowledgeCoverageAnchor
	Shape    *domainShapeProjection
}

// prepareKnowledgeArchitecture parses the designated home's package map and
// coverage shards against the manifest they were read beside. Any other home
// projects no package and no anchor.
func prepareKnowledgeArchitecture(shards knowledgeShards, manifest KnowledgeManifest, designated bool) (knowledgeArchitecture, error) {
	architecture := knowledgeArchitecture{Anchors: map[string][]knowledgeCoverageAnchor{}}
	if !designated {
		return architecture, nil
	}
	if shards.packageMapPresent {
		document, err := parseKnowledgePackageMap(shards.packageMap, manifest.DomainRegistry)
		if err != nil {
			return architecture, err
		}
		architecture.Packages = document.Packages
		architecture.MapHash = knowledgePackageMapContentHash(document)
	}
	records := map[string]bool{}
	for _, record := range manifest.Records {
		records[record.ID] = true
	}
	for name, content := range shards.coverage {
		id, anchors, err := parseKnowledgeCoverageShard(name, content)
		if err != nil {
			return architecture, err
		}
		if !records[id] {
			return architecture, coverageShardFailure(name, "names no knowledge record")
		}
		if len(anchors) > 0 {
			architecture.Anchors[id] = anchors
		}
	}
	return architecture, nil
}

func packageMapFailure(detail string) error {
	return newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "package map "+detail, false, "repair "+knowledgePackageMapPath)
}

// parseKnowledgePackageMap parses the map strictly and returns it with its
// entries sorted by path and each entry's Domains sorted.
func parseKnowledgePackageMap(data []byte, registry KnowledgeDomainRegistry) (knowledgePackageMapDocument, error) {
	document, entries, err := decodeKnowledgePackageMap(data)
	if err != nil {
		return document, err
	}
	minimum := 1
	if document.SchemaVersion == packageMapShapeVersion {
		minimum = 0
	}
	if len(document.Packages) < minimum || len(document.Packages) > maxPackageMapEntries {
		return document, packageMapFailure("packages count is outside the schema's bound (maximum 512)")
	}
	declared := map[string]bool{}
	for _, domain := range registry.Domains {
		declared[domain.DomainID] = domain.Status == "current" || (document.SchemaVersion == packageMapSchemaVersion && domain.Status == "")
	}
	seenPaths := map[string]bool{}
	for index := range document.Packages {
		entry := &document.Packages[index]
		if !validPackageRootPath(entry.Path) {
			return document, packageMapFailure("path is not a normalized relative directory without globs: " + entry.Path)
		}
		if seenPaths[entry.Path] {
			return document, packageMapFailure("lists a package path twice: " + entry.Path)
		}
		seenPaths[entry.Path] = true
		if err := validateKnowledgePackageEntry(entry, entries[index], document.SchemaVersion, declared); err != nil {
			return document, err
		}
	}
	sort.Slice(document.Packages, func(left, right int) bool {
		return document.Packages[left].Path < document.Packages[right].Path
	})
	return document, nil
}

// decodeKnowledgePackageMap retains exact field presence beside typed entries.
// An omitted field is not an empty or null ownership claim, and JSON's
// case-insensitive struct matching must not admit alternate field names.
func decodeKnowledgePackageMap(data []byte) (knowledgePackageMapDocument, []map[string]json.RawMessage, error) {
	var document knowledgePackageMapDocument
	if len(data) > maxPackageMapBytes || !utf8.Valid(data) {
		return document, nil, packageMapFailure("must be UTF-8 JSON within 16384 bytes")
	}
	if err := decodeStrictKnowledgeJSON(data, &document); err != nil {
		return document, nil, packageMapFailure("is not one strict JSON object with schema_version and packages")
	}
	if document.SchemaVersion != packageMapSchemaVersion && document.SchemaVersion != packageMapShapeVersion {
		return document, nil, packageMapFailure("schema_version must be 1.0 or 1.1")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || len(raw) != 2 || raw["schema_version"] == nil || raw["packages"] == nil || bytes.Equal(bytes.TrimSpace(raw["packages"]), []byte("null")) {
		return document, nil, packageMapFailure("requires exactly schema_version and a packages array")
	}
	var entries []map[string]json.RawMessage
	_ = json.Unmarshal(raw["packages"], &entries) // The strict decode proved the array's element types.
	return document, entries, nil
}

// validateKnowledgePackageEntry checks the role's closed ownership shape and
// current Domain references, then normalizes only its listed Domain IDs.
func validateKnowledgePackageEntry(entry *knowledgePackageEntry, fields map[string]json.RawMessage, schemaVersion string, declared map[string]bool) error {
	expected := []string{"path", "domain_ids"}
	domains := entry.DomainIDs
	minimumDomains := 1
	if schemaVersion == packageMapShapeVersion {
		expected = []string{"path", "role"}
		switch entry.Role {
		case "domain":
			expected = append(expected, "owner_domain_id")
			if !validManifestID(entry.OwnerDomainID) {
				return packageMapFailure("domain root requires exactly one owner_domain_id: " + entry.Path)
			}
			domains = []string{entry.OwnerDomainID}
		case "mixed":
			expected = append(expected, "domain_ids")
			minimumDomains = 2
		case "composition", "test_support":
			minimumDomains = 0
		default:
			return packageMapFailure("entry requires an explicit domain, mixed, composition or test_support role: " + entry.Path)
		}
	}
	if len(fields) != len(expected) {
		return packageMapFailure("entry carries fields forbidden by its schema or role: " + entry.Path)
	}
	for _, field := range expected {
		if fields[field] == nil || bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
			return packageMapFailure("entry requires non-null " + field + ": " + entry.Path)
		}
	}
	if len(domains) < minimumDomains || len(domains) > maxPackageMapDomains {
		return packageMapFailure("entry Domain count is outside its role's bound: " + entry.Path)
	}
	sortedDomains := uniqueSorted(domains)
	if len(sortedDomains) != len(domains) {
		return packageMapFailure("entry names an empty or repeated Domain: " + entry.Path)
	}
	for _, domainID := range sortedDomains {
		if !declared[domainID] {
			return newFailure(KindUnknownScope, "rebuild_knowledge_index", "package map names an unknown or non-current Domain: "+domainID, false, "name only current Domains the same commit's registry declares")
		}
	}
	if entry.Role == "" || entry.Role == "mixed" {
		entry.DomainIDs = sortedDomains
	}
	return nil
}

// domainFactRoots converts a validated map without inferring exclusive ownership
// from 1.0. Even a historical single-Domain entry is transitional mixed; it does
// not satisfy the fact validator's explicit domain or multi-Domain mixed rule.
func (document knowledgePackageMapDocument) domainFactRoots() map[string]DomainFactRoot {
	roots := make(map[string]DomainFactRoot, len(document.Packages))
	for _, entry := range document.Packages {
		role := entry.Role
		if document.SchemaVersion == packageMapSchemaVersion {
			role = "mixed"
		}
		owners := append([]string(nil), entry.DomainIDs...)
		if role == "domain" {
			owners = []string{entry.OwnerDomainID}
		}
		roots[entry.Path] = DomainFactRoot{Role: role, OwnerDomainIDs: owners}
	}
	return roots
}

// Parsing checks path syntax without guessing file type from an extension.
// The filesystem-aware validator proves that roots are contained directories.
func validPackageRootPath(value string) bool {
	return validPackageMapPath(value) && !strings.Contains(value, "\\") && !strings.ContainsFunc(value, func(char rune) bool { return char < 32 })
}

// validPackageMapPath admits a normalized relative directory path: no glob
// character, no absolute or parent-relative form, no empty, dot, or
// trailing segment.
func validPackageMapPath(value string) bool {
	if value == "" || len(value) > maxPackageMapPath || strings.ContainsAny(value, "*?[]{}") {
		return false
	}
	if path.Clean(value) != value || path.IsAbs(value) || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	return true
}

// knowledgePackageMapContentHash is the map's own identity. It is
// independent of the registry content hash, so a map edit leaves every
// architecture binding's registry pin current (CD-0222 D2).
func knowledgePackageMapContentHash(document knowledgePackageMapDocument) string {
	canonical, err := json.Marshal(document)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func coverageShardFailure(name, detail string) error {
	return newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "coverage shard "+name+" "+detail, false, "repair the shard under "+knowledgeCoverageTreeDir)
}

// parseKnowledgeCoverageShard parses one coverage shard strictly and returns
// its record ID and, for a satisfied record, its typed anchors in authored
// order. Only a satisfied record carries anchors.
func parseKnowledgeCoverageShard(name string, data []byte) (string, []knowledgeCoverageAnchor, error) {
	var shard struct {
		ID       string          `json:"id"`
		State    string          `json:"state"`
		Evidence json.RawMessage `json:"evidence"`
		Issue    json.RawMessage `json:"issue"`
		Reason   json.RawMessage `json:"reason"`
	}
	if err := decodeStrictKnowledgeJSON(data, &shard); err != nil {
		return "", nil, coverageShardFailure(name, "is not one strict JSON object")
	}
	if shard.ID == "" || name != shard.ID+".json" {
		return "", nil, coverageShardFailure(name, "must be named for its record id")
	}
	obligation, known := coverageStateObligation[shard.State]
	if !known {
		return "", nil, coverageShardFailure(name, "carries an unknown state")
	}
	present := map[string]bool{"evidence": shard.Evidence != nil, "issue": shard.Issue != nil, "reason": shard.Reason != nil}
	for field, isPresent := range present {
		if isPresent != (field == obligation) {
			return "", nil, coverageShardFailure(name, "state "+shard.State+" requires "+obligation+" and no other obligation field")
		}
	}
	switch obligation {
	case "issue":
		var number int64
		var key string
		if json.Unmarshal(shard.Issue, &number) == nil && number >= 1 {
			return shard.ID, nil, nil
		}
		if json.Unmarshal(shard.Issue, &key) != nil || !coverageIssuePattern.MatchString(key) {
			return "", nil, coverageShardFailure(name, "issue must be a positive number or an issue key")
		}
		return shard.ID, nil, nil
	case "reason":
		var reason string
		if json.Unmarshal(shard.Reason, &reason) != nil || strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > maxCoverageReason {
			return "", nil, coverageShardFailure(name, "reason must be a bounded nonempty string")
		}
		return shard.ID, nil, nil
	}
	var anchors []knowledgeCoverageAnchor
	if err := decodeStrictKnowledgeJSON(shard.Evidence, &anchors); err != nil {
		return "", nil, coverageShardFailure(name, "evidence must be a list of kind and value anchors")
	}
	if len(anchors) == 0 || len(anchors) > maxCoverageAnchors {
		return "", nil, coverageShardFailure(name, "evidence must list between 1 and 32 anchors")
	}
	for _, anchor := range anchors {
		if !coverageAnchorKinds[anchor.Kind] {
			return "", nil, coverageShardFailure(name, "names an unknown anchor kind: "+anchor.Kind)
		}
		if anchor.Value == "" || len(anchor.Value) > maxCoverageAnchorValue {
			return "", nil, coverageShardFailure(name, "carries an empty or oversized anchor value")
		}
	}
	return shard.ID, anchors, nil
}

// decodeStrictKnowledgeJSON decodes exactly one JSON value with no duplicate
// key and no field the target does not model.
func decodeStrictKnowledgeJSON(data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// insertKnowledgeArchitecture writes the home's package Domains, each
// carrying the map's content hash, and each law's evidence paths and
// coverage anchors.
func insertKnowledgeArchitecture(ctx context.Context, tx *sql.Tx, home KnowledgeHome, commit string, laws []indexedLaw, architecture knowledgeArchitecture) error {
	for _, entry := range architecture.Packages {
		domains := entry.DomainIDs
		if entry.Role == "domain" {
			domains = []string{entry.OwnerDomainID}
		}
		for _, domainID := range domains {
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_package_domains(home_project_id,home_locator_id,package_path,domain_id,map_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, entry.Path, domainID, architecture.MapHash, commit); err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write a package Domain", true, "retry once the database is writable", err)
			}
		}
	}
	for _, law := range laws {
		for _, evidence := range uniqueSorted(law.record.Evidence) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO law_evidence_paths(home_project_id,home_locator_id,law_id,path,scanned_commit_oid) VALUES(?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, law.record.ID, evidence, commit); err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write a law evidence path", true, "retry once the database is writable", err)
			}
		}
		for ordinal, anchor := range architecture.Anchors[law.record.ID] {
			if _, err := tx.ExecContext(ctx, `INSERT INTO law_coverage_anchors(home_project_id,home_locator_id,law_id,ordinal,kind,value,scanned_commit_oid) VALUES(?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, law.record.ID, ordinal, anchor.Kind, anchor.Value, commit); err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot write a law coverage anchor", true, "retry once the database is writable", err)
			}
		}
	}
	return nil
}
