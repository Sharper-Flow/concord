package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	knowledgeManifestPath = "docs/concord-knowledge-index.v1.json"
	maxKnowledgeManifest  = 256 * 1024
	maxManifestRecords    = 1000
	maxManifestArray      = 64
	maxManifestID         = 256
	maxManifestTitle      = 256
	maxManifestSummary    = 4096
	maxManifestPath       = 512
)

var knowledgeKindsClosed = map[string]bool{
	"work_note": true,
	"lesson":    true,
	"decision":  true,
	"spec":      true,
	"research":  true,
}

var manifestRecordKinds = map[string]bool{"lesson": true, "decision": true, "spec": true}

// KnowledgeManifest is the one tracked registry for non-work-note durable
// knowledge. It contains metadata and proofs, never document bodies.
type KnowledgeManifest struct {
	SchemaVersion  string            `json:"schema_version"`
	SupportedKinds []string          `json:"supported_kinds"`
	IndexedKinds   []string          `json:"indexed_kinds"`
	Records        []KnowledgeRecord `json:"records"`
}

// KnowledgeRecord is a bounded declaration whose path and hash identify the
// authoritative markdown blob at one commit.
type KnowledgeRecord struct {
	ID        string                `json:"id"`
	Kind      string                `json:"kind"`
	Path      string                `json:"path"`
	Status    string                `json:"status"`
	Date      string                `json:"date"`
	Title     string                `json:"title"`
	Summary   string                `json:"summary"`
	Tags      []string              `json:"tags"`
	Scopes    KnowledgeRecordScopes `json:"scopes"`
	Successor string                `json:"successor,omitempty"`
	SHA256    string                `json:"sha256"`
}

type KnowledgeRecordScopes struct {
	Mode         string   `json:"mode"`
	ProductIDs   []string `json:"product_ids"`
	ProjectIDs   []string `json:"project_ids"`
	ComponentIDs []string `json:"component_ids"`
	TagIDs       []string `json:"tag_ids"`
}

func parseKnowledgeManifest(data []byte) (KnowledgeManifest, error) {
	if len(data) == 0 || len(data) > maxKnowledgeManifest {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest is empty or exceeds the bounded size", false, "publish a bounded v1 manifest")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest contains duplicate JSON keys", false, "remove duplicate keys from the manifest")
	}
	var manifest KnowledgeManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return KnowledgeManifest{}, wrapFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest is not strict v1 JSON", false, "repair the manifest schema and remove unknown fields", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest contains trailing JSON values", false, "publish exactly one JSON object")
	}
	if err := validateKnowledgeManifest(manifest); err != nil {
		return KnowledgeManifest{}, err
	}
	return manifest, nil
}

func validateKnowledgeManifest(manifest KnowledgeManifest) error {
	if manifest.SchemaVersion != "1.0" || manifest.SupportedKinds == nil || manifest.IndexedKinds == nil || manifest.Records == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest schema version or required root fields are invalid", false, "publish strict v1 root fields")
	}
	supported, err := validateManifestKindList(manifest.SupportedKinds, "supported_kinds")
	if err != nil {
		return err
	}
	indexed, err := validateManifestKindList(manifest.IndexedKinds, "indexed_kinds")
	if err != nil {
		return err
	}
	for kind := range indexed {
		if kind != "work_note" && !manifestRecordKinds[kind] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "indexed_kinds contains a kind without manifest record support: "+kind, false, "index only lesson, decision, or spec")
		}
		if !supported[kind] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "indexed kind is not supported: "+kind, false, "include every indexed kind in supported_kinds")
		}
	}
	if len(manifest.Records) > maxManifestRecords {
		return newFailure(KindKnowledgeIndexIncomplete, "parse_knowledge_manifest", "manifest contains too many records", true, "split the knowledge authority into bounded homes")
	}
	ids := map[string]bool{}
	paths := map[string]bool{}
	for _, record := range manifest.Records {
		if err := validateKnowledgeRecord(record, supported, indexed); err != nil {
			return err
		}
		if ids[record.ID] {
			return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "manifest contains duplicate stable IDs", false, "assign one stable ID to one canonical record")
		}
		if paths[record.Path] {
			return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "manifest contains duplicate canonical paths", false, "assign one canonical path to one record")
		}
		ids[record.ID], paths[record.Path] = true, true
	}
	if err := validateManifestSuccessors(manifest.Records); err != nil {
		return err
	}
	return nil
}

func validateManifestSuccessors(records []KnowledgeRecord) error {
	byID := make(map[string]KnowledgeRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, record := range records {
		if record.Successor == "" {
			continue
		}
		if record.Successor == record.ID {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record cannot succeed itself", false, "reference a distinct canonical successor")
		}
		successor, ok := byID[record.Successor]
		if !ok {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record references an undeclared successor: "+record.Successor, false, "declare the successor in the same manifest")
		}
		if successor.Kind != record.Kind {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record successor kind does not match", false, "reference a successor of the same knowledge kind")
		}
		wantStatus := "accepted"
		if record.Kind == "lesson" {
			wantStatus = "published"
		}
		if successor.Status != wantStatus {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record successor status is incompatible", false, "reference the active accepted or published successor")
		}
	}
	return nil
}

func validateManifestKindList(values []string, field string) (map[string]bool, error) {
	max := len(knowledgeKindsClosed)
	if field == "indexed_kinds" {
		max = len(knowledgeKindsClosed) - 1
	}
	if len(values) > max {
		return nil, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" exceeds the closed kind bound", false, "use the five closed knowledge kinds")
	}
	result := make(map[string]bool, len(values))
	for _, kind := range values {
		if !knowledgeKindsClosed[kind] {
			return nil, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" contains an unsupported kind: "+kind, false, "use the closed knowledge kind vocabulary")
		}
		if result[kind] {
			return nil, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" contains duplicate kinds", false, "list each kind once")
		}
		result[kind] = true
	}
	return result, nil
}

func validateKnowledgeRecord(record KnowledgeRecord, supported, indexed map[string]bool) error {
	if record.ID == "" || utf8.RuneCountInString(record.ID) > maxManifestID || strings.TrimSpace(record.ID) != record.ID {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record ID is empty, oversized, or not clean", false, "use a bounded stable ID")
	}
	if !manifestRecordKinds[record.Kind] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record kind is not manifest-backed: "+record.Kind, false, "use lesson, decision, or spec")
	}
	if !supported[record.Kind] || !indexed[record.Kind] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record kind is not indexed: "+record.Kind, false, "include the record kind in supported_kinds and indexed_kinds")
	}
	if err := validateManifestPath(record.Path); err != nil {
		return err
	}
	if record.Kind == "decision" && !canonicalDecisionPath(record.Path) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "decision record is outside the canonical CD decision path", false, "use docs/decisions/CD-NNNN markdown")
	}
	if record.Status != "accepted" && record.Status != "published" && record.Status != "superseded" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record status is not closed", false, "use accepted, published, or superseded")
	}
	if record.Status == "published" && record.Kind != "lesson" || record.Status == "accepted" && record.Kind == "lesson" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "status is invalid for record kind", false, "lessons are published; decisions and specs are accepted")
	}
	if record.Status == "superseded" && record.Successor == "" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record lacks successor", false, "declare the stable successor ID")
	}
	if record.Successor != "" && (utf8.RuneCountInString(record.Successor) > maxManifestID || strings.TrimSpace(record.Successor) != record.Successor) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "successor is oversized or not clean", false, "use a bounded stable successor ID")
	}
	if record.Status != "superseded" && record.Successor != "" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "successor is only valid for superseded records", false, "remove successor or mark the record superseded")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.Date); err != nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record date is not RFC3339", false, "use an RFC3339 date")
	}
	if record.Title == "" || utf8.RuneCountInString(record.Title) > maxManifestTitle || strings.TrimSpace(record.Title) != record.Title || record.Summary == "" || utf8.RuneCountInString(record.Summary) > maxManifestSummary || strings.TrimSpace(record.Summary) != record.Summary {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record title or summary is empty, oversized, or not clean", false, "supply bounded authored metadata")
	}
	if record.Tags == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record tags field is missing or null", false, "supply an explicit tags array")
	}
	if err := validateManifestStringArray(record.Tags, "tags"); err != nil {
		return err
	}
	if err := validateManifestScopes(record.Scopes); err != nil {
		return err
	}
	if err := validateContentHash(record.SHA256); err != nil {
		return err
	}
	return nil
}

func canonicalDecisionPath(value string) bool {
	base := path.Base(value)
	if !strings.HasPrefix(base, "CD-") || !strings.HasSuffix(base, ".md") {
		return false
	}
	identifier := strings.TrimSuffix(strings.TrimPrefix(base, "CD-"), ".md")
	if len(identifier) < 4 {
		return false
	}
	for _, digit := range identifier[:4] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return len(identifier) == 4 || identifier[4] == '-'
}

func validateManifestPath(value string) error {
	if value == knowledgeManifestPath || value == "" || utf8.RuneCountInString(value) > maxManifestPath || path.Clean(value) != value || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "-") || !strings.HasPrefix(value, "docs/") || !strings.HasSuffix(value, ".md") {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path is not a clean docs markdown path", false, "use one regular markdown blob below docs/")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == ".." || strings.ContainsRune(part, '\x00') {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path contains traversal or empty components", false, "use a clean relative path")
		}
	}
	if strings.HasPrefix(value, "docs/work/") || strings.Contains(strings.ToLower(value), "generated") || strings.HasPrefix(value, "docs/research/") || value == "docs/product-coordination-view.md" || value == "docs/terminal-launcher-contract.md" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path is not an eligible authored knowledge blob", false, "exclude work notes, research packs, and generated docs")
	}
	return nil
}

func validateManifestScopes(scopes KnowledgeRecordScopes) error {
	if scopes.Mode != "home" && scopes.Mode != "explicit" || scopes.ProductIDs == nil || scopes.ProjectIDs == nil || scopes.ComponentIDs == nil || scopes.TagIDs == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "scope mode is not closed", false, "use home or explicit scope mode")
	}
	for name, values := range map[string][]string{"product_ids": scopes.ProductIDs, "project_ids": scopes.ProjectIDs, "component_ids": scopes.ComponentIDs, "tag_ids": scopes.TagIDs} {
		if err := validateManifestStringArray(values, name); err != nil {
			return err
		}
	}
	if scopes.Mode == "home" && (len(scopes.ProductIDs) > 0 || len(scopes.ProjectIDs) > 0 || len(scopes.ComponentIDs) > 0 || len(scopes.TagIDs) > 0) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "home scope cannot carry explicit scope IDs", false, "choose explicit mode for declared scope IDs")
	}
	return nil
}

func validateManifestStringArray(values []string, field string) error {
	if len(values) > maxManifestArray {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" exceeds the bounded array size", false, "use a bounded unique ID array")
	}
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || utf8.RuneCountInString(value) > maxManifestID || strings.TrimSpace(value) != value || seen[value] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" contains an empty, oversized, or duplicate value", false, "use bounded unique IDs")
		}
		seen[value] = true
	}
	return nil
}

func readKnowledgeManifest(ctx context.Context, repo, commit string) (KnowledgeManifest, bool, error) {
	entry, err := gitTreeEntry(ctx, repo, commit, knowledgeManifestPath)
	if err != nil {
		// A missing manifest is the legacy, explicitly-supported state.
		out, gitErr := runGit(ctx, repo, "ls-tree", "-z", commit, "--", knowledgeManifestPath)
		if gitErr != nil {
			return KnowledgeManifest{}, false, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot inspect the knowledge manifest", true, "restore the git object and retry", gitErr)
		}
		entries, parseErr := parseTreeEntries(out)
		if parseErr != nil {
			return KnowledgeManifest{}, false, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest tree entry is malformed", false, "repair the canonical git tree", parseErr)
		}
		if len(entries) == 0 {
			return KnowledgeManifest{}, true, nil
		}
		return KnowledgeManifest{}, false, err
	}
	if entry.kind != "blob" || entry.mode != "100644" {
		return KnowledgeManifest{}, false, newFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest is not a regular blob", false, "commit a regular manifest file")
	}
	content, err := runGit(ctx, repo, "cat-file", "blob", commit+":"+knowledgeManifestPath)
	if err != nil {
		return KnowledgeManifest{}, false, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "cannot read the committed manifest blob", true, "restore the manifest blob and retry", err)
	}
	manifest, err := parseKnowledgeManifest(content)
	return manifest, false, err
}

func verifyManifestRecord(ctx context.Context, repo, commit string, record KnowledgeRecord) error {
	manifest, missing, err := readKnowledgeManifest(ctx, repo, commit)
	if err != nil {
		return err
	}
	if missing {
		return newFailure(KindKnowledgeMissing, "verify_manifest_record", "recorded manifest is missing", false, "restore the manifest at the recorded commit")
	}
	var declared *KnowledgeRecord
	for i := range manifest.Records {
		if manifest.Records[i].ID == record.ID {
			declared = &manifest.Records[i]
			break
		}
	}
	if declared == nil || !sameKnowledgeRecord(*declared, record) {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "recorded projection does not match the exact manifest declaration", false, "rebuild from the manifest commit and preserve its metadata")
	}
	entry, err := gitTreeEntry(ctx, repo, commit, record.Path)
	if err != nil || entry.kind != "blob" || entry.mode != "100644" {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "manifest record blob is missing or not regular", false, "restore the referenced regular markdown blob")
	}
	content, err := runGit(ctx, repo, "cat-file", "blob", commit+":"+record.Path)
	if err != nil {
		return wrapFailure(KindInvalidNoteProof, "verify_manifest_record", "cannot read the referenced manifest blob", true, "restore the git object and retry", err)
	}
	sum := sha256.Sum256(content)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != record.SHA256 {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "manifest record hash does not match blob bytes", false, "recompute the authored sha256 proof")
	}
	return nil
}

func sameKnowledgeRecord(a, b KnowledgeRecord) bool {
	normalize := func(record KnowledgeRecord) KnowledgeRecord {
		record.Tags = append([]string{}, record.Tags...)
		record.Scopes.ProductIDs = append([]string{}, record.Scopes.ProductIDs...)
		record.Scopes.ProjectIDs = append([]string{}, record.Scopes.ProjectIDs...)
		record.Scopes.ComponentIDs = append([]string{}, record.Scopes.ComponentIDs...)
		record.Scopes.TagIDs = append([]string{}, record.Scopes.TagIDs...)
		sort.Strings(record.Tags)
		sort.Strings(record.Scopes.ProductIDs)
		sort.Strings(record.Scopes.ProjectIDs)
		sort.Strings(record.Scopes.ComponentIDs)
		sort.Strings(record.Scopes.TagIDs)
		return record
	}
	a, b = normalize(a), normalize(b)
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate or invalid object key")
				}
				seen[name] = true
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		case '[':
			for decoder.More() {
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		return err
	}
	return nil
}
