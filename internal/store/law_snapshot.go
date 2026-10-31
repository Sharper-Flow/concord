package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The committed law folder is the authority carrier for Product law: a file's
// tier placement supplies kind and acceptance, and its JSON frontmatter carries
// the retained metadata. The loader reads one explicit caller-resolved commit
// through the committed-read Git runner; it holds no SQL, no Store state, no
// body text, and no cache.
const lawSnapshotOp = "load_law_snapshot"

const lawFolderRoot = ".concord/law/"

// lawFolderTierKind resolves a tier directory to the kind it supplies. Every
// tier here holds law-bearing records only, so the folder always yields
// accepted law.
func lawFolderTierKind(tier string) (string, bool) {
	switch tier {
	case "constitution":
		return "constitution", true
	case "specs":
		return "spec", true
	case "decisions":
		return "decision", true
	}
	return "", false
}

// lawFolderShadowKey reports the folder-derived fields whose authored presence
// gets its own refusal wording: a dropped shadow field would leave a file that
// reads as law while claiming another classification.
func lawFolderShadowKey(key string) bool {
	switch key {
	case "kind", "status", "sha256":
		return true
	}
	return false
}

// lawFolderFrontMatterKey reports the closed frontmatter vocabulary: the
// retained metadata only. Kind, status, path, and digest are folder- or
// content-derived and deliberately unmodeled.
func lawFolderFrontMatterKey(key string) bool {
	switch key {
	case "id", "title", "authority", "home_domain_id", "applies_to_domain_ids",
		"law_relations", "criterion_bindings", "product_wide_rationale":
		return true
	}
	return false
}

// lawFolderSnapshot is one immutable committed law-folder read. The maps and
// slices are private; every accessor returns a value or a defensive copy.
type lawFolderSnapshot struct {
	commitOID string
	// registry is nil when the commit carries neither a Domain registry nor
	// any law: such a home holds no law, and every Domain read refuses.
	registry *KnowledgeDomainRegistry
	laws     map[string]KnowledgeRecord
	order    []string
}

// loadLawFolderSnapshot reads the committed law folder and the independent
// Domain registry at one explicit resolved commit; it never resolves a ref and
// never reads working-tree bytes. A commit with no law folder is a lawful
// empty inventory. Law needs its Domain homes, so law without a registry
// refuses, as does a malformed registry; a home with neither holds no law.
func loadLawFolderSnapshot(ctx context.Context, repo, commitOID string) (*lawFolderSnapshot, error) {
	if err := validateCommitOID(commitOID); err != nil {
		return nil, err
	}
	runner := lawFolderGitRunner{}
	if err := verifyLawFolderCommitObject(ctx, runner, repo, commitOID); err != nil {
		return nil, err
	}
	registry, err := loadLawFolderRegistry(ctx, runner, repo, commitOID)
	if err != nil {
		return nil, err
	}
	placements, err := scanLawFolderTree(ctx, runner, repo, commitOID)
	if err != nil {
		return nil, err
	}
	if registry == nil {
		if len(placements) > 0 {
			return nil, domainRegistryAbsent()
		}
		return &lawFolderSnapshot{commitOID: commitOID, laws: map[string]KnowledgeRecord{}, order: []string{}}, nil
	}
	laws := make(map[string]KnowledgeRecord, len(placements))
	records := make([]KnowledgeRecord, 0, len(placements))
	order := make([]string, 0, len(placements))
	firstPath := map[string]string{}
	for _, placement := range placements {
		record, err := loadLawFolderRecord(ctx, runner, repo, commitOID, *registry, placement)
		if err != nil {
			return nil, err
		}
		if _, duplicate := laws[record.ID]; duplicate {
			return nil, newFailure(KindKnowledgeAmbiguous, lawSnapshotOp,
				"law folder carries duplicate stable ID "+record.ID+" in "+firstPath[record.ID]+" and "+placement.path,
				false, "assign one stable ID to one law file")
		}
		laws[record.ID] = record
		records = append(records, record)
		order = append(order, record.ID)
		firstPath[record.ID] = placement.path
	}
	seen := map[string]bool{}
	graph := map[string][]string{}
	for _, id := range order {
		if err := validateManifestRecordRelations(laws[id], laws, seen, graph); err != nil {
			return nil, lawFileFailure(laws[id].Path, err)
		}
	}
	if graphHasCycle(graph) {
		return nil, newFailure(KindCycleDetected, lawSnapshotOp,
			"law folder relations contain a cycle", false, "remove the cycle from the authored law graph")
	}
	if err := validateKnowledgeDomainLawReferences(*registry, records); err != nil {
		return nil, err
	}
	return &lawFolderSnapshot{commitOID: commitOID, registry: registry, laws: laws, order: order}, nil
}

// A pinned law read must neither substitute replacement objects nor fetch
// missing promisor objects implicitly.
type lawFolderGitRunner struct{}

func (lawFolderGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runGitEnv(ctx, dir, []string{"GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}, args...)
}

// Tree reads accept tree and tag OIDs too; neither is a committed revision.
func verifyLawFolderCommitObject(ctx context.Context, runner GitRunner, repo, commitOID string) error {
	if strings.ToLower(commitOID) != commitOID {
		return newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law snapshot commit OID is not lowercase hexadecimal", false, "supply the resolved commit OID as Git prints it")
	}
	out, err := runner.Run(ctx, repo, "cat-file", "-t", commitOID)
	if err != nil {
		return wrapFailure(KindGitUnreachable, lawSnapshotOp,
			"cannot inspect the explicit law snapshot commit", true, "confirm the git home and commit are reachable", err)
	}
	if strings.TrimSpace(string(out)) != "commit" {
		return newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law snapshot OID does not name a commit", false, "supply the resolved default-branch commit OID")
	}
	return nil
}

// CommitOID returns the frozen commit the snapshot was read at.
func (s *lawFolderSnapshot) CommitOID() string { return s.commitOID }

// LawIDs returns the snapshot's stable law IDs in path order.
func (s *lawFolderSnapshot) LawIDs() []string {
	return copyLawFolderSlice(s.order)
}

// Law returns a defensive copy of one folder law record.
func (s *lawFolderSnapshot) Law(lawID string) (KnowledgeRecord, bool) {
	record, ok := s.laws[lawID]
	if !ok {
		return KnowledgeRecord{}, false
	}
	return copyLawFolderRecord(record), true
}

// Registry returns a defensive copy of the independent Domain registry read at
// the same frozen commit. A commit that committed no registry refuses: an
// empty Domain answer is never invented.
func (s *lawFolderSnapshot) Registry() (KnowledgeDomainRegistry, error) {
	if s.registry == nil {
		return KnowledgeDomainRegistry{}, domainRegistryAbsent()
	}
	return copyLawFolderRegistry(*s.registry), nil
}

func domainRegistryAbsent() error {
	return newFailure(KindDomainRegistryAbsent, lawSnapshotOp,
		"the committed home carries no Domain registry at "+knowledgeRegistryPath,
		false, "commit the independent domain-registry.json before reading law")
}

func copyLawFolderRecord(record KnowledgeRecord) KnowledgeRecord {
	record.AppliesToDomainIDs = copyLawFolderSlice(record.AppliesToDomainIDs)
	record.LawRelations = copyLawFolderSlice(record.LawRelations)
	record.CriterionBindings = copyLawFolderSlice(record.CriterionBindings)
	return record
}

func copyLawFolderRegistry(registry KnowledgeDomainRegistry) KnowledgeDomainRegistry {
	copied := registry
	copied.Domains = make([]KnowledgeDomain, len(registry.Domains))
	for index, domain := range registry.Domains {
		copied.Domains[index] = domain
		copied.Domains[index].ArchitectureRelations = copyLawFolderSlice(domain.ArchitectureRelations)
		for position, relation := range domain.ArchitectureRelations {
			copied.Domains[index].ArchitectureRelations[position].GoverningLawIDs = copyLawFolderSlice(relation.GoverningLawIDs)
		}
	}
	return copied
}

func copyLawFolderSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	return append(make([]T, 0, len(values)), values...)
}

// loadLawFolderRegistry reads and validates the independent registry at the
// frozen commit. It returns nil when the commit carries no registry file.
func loadLawFolderRegistry(ctx context.Context, runner GitRunner, repo, commitOID string) (*KnowledgeDomainRegistry, error) {
	out, err := runner.Run(ctx, repo, "ls-tree", "-z", commitOID, "--", knowledgeRegistryPath)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, lawSnapshotOp,
			"cannot inspect the committed Domain registry", true, "confirm the git home and commit are reachable", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
			"committed Domain registry tree entry is malformed", false, "repair the committed registry file", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	_, content, err := committedRegularBlobBytes(ctx, runner, repo, commitOID, knowledgeRegistryPath)
	if err != nil {
		return nil, err
	}
	if err := payloadschema.ValidateUniqueJSON(content); err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
			"Domain registry contains duplicate JSON keys", false, "author each registry key once", err)
	}
	var registry KnowledgeDomainRegistry
	if err := json.Unmarshal(content, &registry); err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
			"Domain registry is not valid registry JSON", false, "repair "+knowledgeRegistryPath, err)
	}
	if err := validateKnowledgeDomainRegistry(registry); err != nil {
		return nil, err
	}
	return &registry, nil
}

// lawFolderPlacement is one admitted law file: its committed path and the kind
// its tier directory supplies.
type lawFolderPlacement struct {
	path string
	kind string
}

// scanLawFolderTree enumerates the committed law tree. Nothing under the law
// root is skipped: a non-blob entry or an undeterminable tier refuses the
// whole load, so a broken entry can never read as absent law. Files sit under
// any descendant of a recognized tier with a clean Git-relative path; the
// stable metadata ID is independent of the file name.
func scanLawFolderTree(ctx context.Context, runner GitRunner, repo, commitOID string) ([]lawFolderPlacement, error) {
	out, err := runner.Run(ctx, repo, "ls-tree", "-r", "-z", commitOID, "--", strings.TrimSuffix(lawFolderRoot, "/"))
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, lawSnapshotOp,
			"cannot inspect the committed law tree", true, "confirm the git home and commit are reachable", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
			"committed law tree is malformed", false, "repair the committed law tree", err)
	}
	placements := make([]lawFolderPlacement, 0, len(entries))
	for _, entry := range entries {
		placement, err := lawFolderEntryPlacement(entry)
		if err != nil {
			return nil, err
		}
		placements = append(placements, placement)
		if len(placements) > maxManifestRecords {
			return nil, newFailure(KindKnowledgeIndexIncomplete, lawSnapshotOp,
				"committed law folder exceeds the bounded file count", true,
				"retire superseded law so the folder stays bounded")
		}
	}
	sort.Slice(placements, func(i, j int) bool { return placements[i].path < placements[j].path })
	return placements, nil
}

func lawFolderEntryPlacement(entry treeEntry) (lawFolderPlacement, error) {
	if entry.mode != "100644" && entry.mode != "100755" || entry.kind != "blob" {
		if entry.mode == "120000" {
			return lawFolderPlacement{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
				"law folder entry is a symlink: "+entry.path, false, "commit a regular law file")
		}
		if entry.mode == "160000" || entry.kind == "commit" {
			return lawFolderPlacement{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
				"law folder entry is a submodule: "+entry.path, false, "commit a regular law file")
		}
		return lawFolderPlacement{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law folder entry is not a regular file: "+entry.path, false, "commit a regular law file")
	}
	segments := strings.Split(strings.TrimPrefix(entry.path, lawFolderRoot), "/")
	if len(segments) < 2 {
		return lawFolderPlacement{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law folder entry is not placed under a law tier directory: "+entry.path,
			false, "place law files under constitution, specs, or decisions")
	}
	kind, ok := lawFolderTierKind(segments[0])
	if !ok {
		return lawFolderPlacement{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law folder entry uses an unknown tier directory: "+entry.path,
			false, "place law files under constitution, specs, or decisions")
	}
	if !lawFolderRelPathClean(segments[1:]) {
		return lawFolderPlacement{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law file path is not clean: "+entry.path,
			false, "use clean relative paths without . or .. segments")
	}
	return lawFolderPlacement{path: entry.path, kind: kind}, nil
}

func lawFolderRelPathClean(segments []string) bool {
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// loadLawFolderRecord parses one law file's frontmatter into a
// folder-classified record and runs the retained per-record validators,
// including the law home and its applicability rules against the registry.
// The SHA-256 covers the whole raw file including the frontmatter, so any
// metadata or body change moves the digest. Body size rides the
// committed-read runner's maxGitOutput bound.
func loadLawFolderRecord(ctx context.Context, runner GitRunner, repo, commitOID string, registry KnowledgeDomainRegistry, placement lawFolderPlacement) (KnowledgeRecord, error) {
	_, content, err := committedRegularBlobBytes(ctx, runner, repo, commitOID, placement.path)
	if err != nil {
		return KnowledgeRecord{}, lawFileFailure(placement.path, err)
	}
	front, ok := splitLawFolderFrontMatter(content)
	if !ok {
		return KnowledgeRecord{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law file does not open with a closed --- JSON frontmatter block: "+placement.path,
			false, "author one JSON frontmatter block between --- lines")
	}
	if err := payloadschema.ValidateUniqueJSON(front); err != nil {
		return KnowledgeRecord{}, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law frontmatter contains duplicate or malformed JSON keys: "+placement.path,
			false, "author each frontmatter key exactly once", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(front, &fields); err != nil {
		return KnowledgeRecord{}, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law frontmatter is not one JSON object: "+placement.path,
			false, "author the frontmatter as one JSON object", err)
	}
	for key := range fields {
		if lawFolderShadowKey(key) {
			return KnowledgeRecord{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
				"law frontmatter authors the folder-derived field "+key+": "+placement.path,
				false, "remove "+key+"; the folder placement and the committed bytes supply it")
		}
		if !lawFolderFrontMatterKey(key) {
			return KnowledgeRecord{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
				"law frontmatter carries an unmodeled field "+key+": "+placement.path,
				false, "author only the retained law metadata fields")
		}
	}
	var record KnowledgeRecord
	if err := json.Unmarshal(front, &record); err != nil {
		return KnowledgeRecord{}, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law frontmatter does not decode into the retained record fields: "+placement.path,
			false, "author bounded retained law metadata", err)
	}
	// The record's custom unmarshaler drops unknown nested fields silently, so
	// each nested retained object decodes strictly before the metadata is
	// trusted.
	for _, nested := range []struct {
		key     string
		target  any
		members []string
	}{
		{"authority", &record.Authority, []string{"tier", "legislated_by", "contract_version"}},
		{"law_relations", &record.LawRelations, []string{"kind", "target_id"}},
		{"criterion_bindings", &record.CriterionBindings, []string{"criterion", "scenario", "exemption", "work_id", "predicate_id"}},
	} {
		if raw, ok := fields[nested.key]; ok {
			if err := lawFolderStrictDecode(nested.target, raw, nested.members); err != nil {
				return KnowledgeRecord{}, wrapFailure(KindInvalidNoteProof, lawSnapshotOp,
					"law frontmatter field "+nested.key+" holds unknown or malformed fields: "+placement.path,
					false, "author only the retained "+nested.key+" fields", err)
			}
		}
	}
	if len(record.LawRelations) > maxManifestRelations {
		return KnowledgeRecord{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law_relations exceed the bounded relation count: "+placement.path,
			false, fmt.Sprintf("declare at most %d typed law relations", maxManifestRelations))
	}
	record.Kind = placement.kind
	record.Status = "accepted"
	record.Path = placement.path
	sum := sha256.Sum256(content)
	record.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
	if !validManifestID(record.ID) || strings.ContainsAny(record.ID, "/\r\n\u2028\u2029") {
		return KnowledgeRecord{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law ID is empty, oversized, unclean, or uses the reserved qualified form: "+placement.path,
			false, "author one bounded stable law ID without '/'")
	}
	if record.Title == "" || utf8.RuneCountInString(record.Title) > maxManifestTitle || strings.TrimSpace(record.Title) != record.Title {
		return KnowledgeRecord{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"law title is empty, oversized, or not clean: "+placement.path, false, "author one bounded trimmed title")
	}
	if err := validateKnowledgeRecordAuthority(record); err != nil {
		return KnowledgeRecord{}, lawFileFailure(placement.path, err)
	}
	if record.Authority.ContractVersion > 2147483647 {
		return KnowledgeRecord{}, newFailure(KindInvalidNoteProof, lawSnapshotOp,
			"authority contract_version exceeds the bounded version: "+placement.path,
			false, "declare a positive contract_version no larger than 2147483647")
	}
	if err := validateKnowledgeCriterionBindings(record); err != nil {
		return KnowledgeRecord{}, lawFileFailure(placement.path, err)
	}
	if err := validateManifestLawHome(record, registry); err != nil {
		return KnowledgeRecord{}, lawFileFailure(placement.path, err)
	}
	return record, nil
}

// encoding/json matches keys case-insensitively, so DisallowUnknownFields
// alone cannot prevent a case-variant spelling from overwriting a field.
func lawFolderStrictDecode(target any, raw json.RawMessage, members []string) error {
	if err := lawFolderExactMembers(raw, members); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func lawFolderExactMembers(raw json.RawMessage, members []string) error {
	trimmed := bytes.TrimSpace(raw)
	var objects []map[string]json.RawMessage
	switch {
	case bytes.HasPrefix(trimmed, []byte("[")):
		if err := json.Unmarshal(trimmed, &objects); err != nil {
			return err
		}
	case bytes.HasPrefix(trimmed, []byte("{")):
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return err
		}
		objects = append(objects, object)
	}
	for _, object := range objects {
		for key := range object {
			if !slices.Contains(members, key) {
				return fmt.Errorf("unknown field %q", key)
			}
		}
	}
	return nil
}

// splitLawFolderFrontMatter splits the leading --- delimited JSON block from a
// law file. LF and CRLF delimiters parse, as does a closing --- at EOF; the
// raw bytes are never normalized, because the hash covers them verbatim. A
// literal newline followed by --- cannot occur inside JSON, so the first
// closing delimiter ends the block.
func splitLawFolderFrontMatter(content []byte) ([]byte, bool) {
	rest, ok := lawFolderTrimOpenDelimiter(content)
	if !ok {
		return nil, false
	}
	index, ok := lawFolderCloseDelimiterIndex(rest)
	if !ok {
		return nil, false
	}
	front := bytes.TrimRight(rest[:index], "\r")
	if len(bytes.TrimSpace(front)) == 0 {
		return nil, false
	}
	return front, true
}

func lawFolderTrimOpenDelimiter(content []byte) ([]byte, bool) {
	if bytes.HasPrefix(content, []byte("---\r\n")) {
		return content[len("---\r\n"):], true
	}
	if bytes.HasPrefix(content, []byte("---\n")) {
		return content[len("---\n"):], true
	}
	return nil, false
}

// lawFolderCloseDelimiterIndex returns the offset of the first line that is
// exactly --- (optionally \r-terminated, LF, CRLF, or EOF-ended).
func lawFolderCloseDelimiterIndex(rest []byte) (int, bool) {
	for position := 0; position+3 <= len(rest); position++ {
		if position > 0 && rest[position-1] != '\n' {
			continue
		}
		if string(rest[position:position+3]) != "---" {
			continue
		}
		switch {
		case position+3 == len(rest):
			return position, true
		case rest[position+3] == '\n':
			return position, true
		case rest[position+3] == '\r' && (position+4 == len(rest) || rest[position+4] == '\n'):
			return position, true
		}
	}
	return 0, false
}

// lawFileFailure names the owning file in a retained helper's typed refusal
// without changing its kind or recovery.
func lawFileFailure(lawPath string, err error) error {
	var failure *Failure
	if errors.As(err, &failure) {
		failure.Detail = lawPath + ": " + failure.Detail
		return failure
	}
	return err
}
