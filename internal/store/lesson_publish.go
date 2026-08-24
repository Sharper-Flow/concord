package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// CD-0026: a lesson is captured per change and promoted by scope. Publishing
// writes the lesson markdown, appends its manifest record, and commits both
// through the repository's git authority in one commit. The manifest — not a
// parallel event stream — remains the lesson's durable backing (CD-0020), so
// no new event kind or projection table is introduced; resolve_note verifies
// against the manifest immediately, and the next index rebuild picks the
// record up for search.

const (
	lessonManifestPath = "docs/concord-knowledge-index.v1.json"
	lessonRecordDir    = "docs/knowledge/records"
	maxLessonContent   = 32768
	maxLessonTags      = 8
	maxLessonEvidence  = 32
)

// LessonPublication is one separately accepted durable lesson (CD-0009 D7).
type LessonPublication struct {
	LessonID string
	Title    string
	Summary  string
	Content  string
	Tags     []string
	Scopes   KnowledgeRecordScopes
	// Evidence names implementation paths this lesson's guidance rests on;
	// the offline validator fails when they rot (drift audit).
	Evidence []string
	Now      time.Time
}

// PublishedLesson is the verified result of a lesson publication.
type PublishedLesson struct {
	Record    KnowledgeRecord
	Note      VerifiedNote
	CommitOID string
}

func (req LessonPublication) record(contentSHA string, date, notePath, schemaVersion string) KnowledgeRecord {
	scopes := req.Scopes
	if scopes.Mode == "" {
		scopes.Mode = "home"
	}
	scopes.ProductIDs = nonNilIDs(scopes.ProductIDs)
	scopes.ProjectIDs = nonNilIDs(scopes.ProjectIDs)
	scopes.TagIDs = nonNilIDs(scopes.TagIDs)
	if schemaVersion == "1.2" {
		scopes.ComponentIDs = nil
		scopes.componentIDsPresent = false
		scopes.DomainIDs = nonNilIDs(scopes.DomainIDs)
		scopes.domainIDsPresent = true
	} else {
		scopes.ComponentIDs = nonNilIDs(scopes.ComponentIDs)
		scopes.componentIDsPresent = true
		scopes.DomainIDs = nil
		scopes.domainIDsPresent = false
	}
	tags := nonNilIDs(req.Tags)
	return KnowledgeRecord{
		ID: req.LessonID, Kind: "lesson", Path: notePath, Status: "published",
		Date: date, Title: req.Title, Summary: req.Summary, Tags: tags,
		Scopes: scopes, SHA256: contentSHA, Evidence: req.Evidence,
	}
}

func nonNilIDs(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func validateLessonPublication(req LessonPublication) error {
	if len(req.LessonID) < 2 || len(req.LessonID) > 128 || strings.ContainsAny(req.LessonID, " \t\n") {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson id must be a bounded non-space identifier", false, "supply a stable lesson id")
	}
	if len(req.Title) < 1 || len(req.Title) > 256 || len(req.Summary) < 1 || len(req.Summary) > 1024 {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson title or summary is outside bounds", false, "supply a bounded title and summary")
	}
	if len(req.Content) < 1 || len(req.Content) > maxLessonContent {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson content is empty or exceeds the bounded size", false, "supply bounded lesson content")
	}
	if len(req.Tags) > maxLessonTags {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson carries too many tags", false, "supply at most eight tags")
	}
	for _, tag := range req.Tags {
		if len(tag) < 1 || len(tag) > 32 {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson tags must be bounded", false, "supply bounded tags")
		}
	}
	if len(req.Evidence) > maxLessonEvidence {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson carries too many evidence paths", false, "supply at most thirty-two evidence paths")
	}
	for _, evidence := range req.Evidence {
		if len(evidence) < 1 || len(evidence) > 512 || strings.HasPrefix(evidence, "/") || strings.Contains(evidence, "..") {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "evidence must be bounded repository-relative paths", false, "supply relative evidence paths")
		}
	}
	if err := validateLessonScopes(req.Scopes); err != nil {
		return err
	}
	return nil
}

// validateLessonScopes mirrors the durable-knowledge scope rule: home carries
// no explicit IDs; explicit carries at least one.
func validateLessonScopes(scopes KnowledgeRecordScopes) error {
	if scopes.Mode == "" {
		scopes.Mode = "home"
	}
	counts := len(scopes.ProductIDs) + len(scopes.ProjectIDs) + len(scopes.ComponentIDs) + len(scopes.DomainIDs) + len(scopes.TagIDs)
	switch scopes.Mode {
	case "home":
		if counts != 0 {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "home scope cannot carry explicit scope IDs", false, "use explicit scope mode to declare IDs")
		}
	case "explicit":
		if counts == 0 {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "explicit scope must declare at least one scope ID", false, "declare the Product, Project, Domain, component, or tag scopes")
		}
	default:
		return newFailure(KindInvalidNoteProof, "publish_lesson", "scope mode must be home or explicit", false, "supply home or explicit")
	}
	return nil
}

// PublishLessonRecord publishes one accepted lesson through the git
// knowledge authority. It is idempotent: when the manifest already carries
// the exact record, the committed lesson verifies and is returned without a
// new commit. It performs no SQLite writes.
func PublishLessonRecord(ctx context.Context, home KnowledgeHome, req LessonPublication) (PublishedLesson, error) {
	out := PublishedLesson{}
	if home.RepoPath == "" {
		return out, newFailure(KindInvalidNoteProof, "publish_lesson", "lesson publication requires the git home", false, "publish through a registered knowledge home")
	}
	if err := validateLessonPublication(req); err != nil {
		return out, err
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	date := now.UTC().Format("2006-01-02T00:00:00Z")
	sum := sha256.Sum256([]byte(req.Content))
	contentSHA := "sha256:" + hex.EncodeToString(sum[:])

	notePath := "docs/lessons/" + now.UTC().Format("2006-01-02") + "-" + slugifyKnowledgeTitle(req.Title) + ".md"
	fullNotePath := path.Join(home.RepoPath, notePath)
	manifestFullPath := path.Join(home.RepoPath, lessonManifestPath)
	recordShardPath := path.Join(lessonRecordDir, req.LessonID+".json")
	recordShardFullPath := path.Join(home.RepoPath, recordShardPath)

	// Existing manifest governs idempotency and conflicts.
	manifestBytes, readErr := os.ReadFile(manifestFullPath)
	if readErr != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot read the knowledge manifest", true, "restore the git knowledge home", readErr)
	}
	manifest, parseErr := parseKnowledgeManifest(manifestBytes)
	if parseErr != nil {
		return out, parseErr
	}
	for _, existing := range manifest.Records {
		if existing.ID == req.LessonID {
			if existing.Kind == "lesson" && existing.Path == notePath && existing.SHA256 == contentSHA {
				commit, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD")
				if err != nil {
					return out, err
				}
				out.Record = existing
				out.Note = manifestRecordNote(existing, strings.TrimSpace(string(commit)), manifest.SchemaVersion)
				out.CommitOID = strings.TrimSpace(string(commit))
				return out, nil
			}
			return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson id is already claimed by a different record", false, "choose a new lesson id or supersede the existing lesson")
		}
		if existing.Path == notePath && existing.SHA256 != contentSHA {
			return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson path is already claimed by different content", false, "retitle the lesson")
		}
	}
	if manifest.SchemaVersion == "1.2" && len(req.Scopes.ComponentIDs) > 0 {
		return out, newFailure(KindInvalidNoteProof, "publish_lesson", "schema 1.2 lesson publication cannot use component scope IDs", false, "use domain scope IDs for a schema 1.2 manifest")
	}
	if manifest.SchemaVersion != "1.2" && len(req.Scopes.DomainIDs) > 0 {
		return out, newFailure(KindInvalidNoteProof, "publish_lesson", "schema 1.0 or 1.1 lesson publication cannot use domain scope IDs", false, "use component scope IDs for a compatibility manifest")
	}

	record := req.record(contentSHA, date, notePath, manifest.SchemaVersion)
	if err := validateKnowledgeRecordForSchema(record, knowledgeKindsClosed, knowledgeKindsClosed, manifest.SchemaVersion); err != nil {
		return out, err
	}

	if err := os.MkdirAll(path.Dir(fullNotePath), 0o755); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot create the lesson directory", true, "restore write access to the git home", err)
	}
	if err := os.WriteFile(fullNotePath, []byte(req.Content), 0o644); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot write the lesson draft", true, "restore write access to the git home", err)
	}
	if err := os.MkdirAll(path.Dir(recordShardFullPath), 0o755); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot create the lesson record directory", true, "restore write access to the git home", err)
	}
	shard, err := marshalKnowledgeRecord(record)
	if err != nil {
		return out, err
	}
	if err := os.WriteFile(recordShardFullPath, shard, 0o644); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot write the lesson record shard", true, "restore write access to the git home", err)
	}

	// Append the record and rewrite the manifest preserving the generator's
	// canonical formatting: root order as authored, records sorted by ID,
	// record keys sorted, indent two.
	manifest.Records = append(manifest.Records, record)
	sort.SliceStable(manifest.Records, func(i, j int) bool { return manifest.Records[i].ID < manifest.Records[j].ID })
	if err := validateKnowledgeManifest(manifest); err != nil {
		return out, err
	}
	updated, err := marshalKnowledgeManifest(manifest)
	if err != nil {
		return out, err
	}
	if err := os.WriteFile(manifestFullPath, updated, 0o644); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot write the knowledge manifest", true, "restore write access to the git home", err)
	}

	if _, err := runGit(ctx, home.RepoPath, "add", "--", notePath, recordShardPath, lessonManifestPath); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot stage the lesson", true, "restore git write access and retry", err)
	}
	if _, err := runGit(ctx, home.RepoPath, "commit", "--quiet", "-m", "docs: publish Concord lesson "+req.LessonID, "--", notePath, recordShardPath, lessonManifestPath); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot commit the lesson", true, "complete the native git commit and reconcile", err)
	}
	commit, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD")
	if err != nil {
		return out, err
	}
	oid := strings.TrimSpace(string(commit))
	out.Record = record
	out.Note = manifestRecordNote(record, oid, manifest.SchemaVersion)
	out.CommitOID = oid
	return out, nil
}

// marshalKnowledgeManifest renders the manifest with two-space indent, root
// keys in the authored order, canonical record key order, and one trailing
// newline — the same bytes scripts/generate-knowledge-index.py derives from the
// record shards. It is lossless: every top-level key the parsed manifest
// carried is present in the output, whether or not this package models it.
func marshalKnowledgeManifest(manifest KnowledgeManifest) ([]byte, error) {
	// Start from the keys this package does not interpret so they survive the
	// rewrite, then overwrite the ones it owns. A top-level key added to the
	// contract later is carried here, not enumerated; only its position comes
	// from canonicalManifestRootOrder.
	values := make(map[string]any, len(manifest.uninterpreted)+len(manifestRootKeys))
	for key, value := range manifest.uninterpreted {
		values[key] = value
	}
	values["schema_version"] = manifest.SchemaVersion
	values["supported_kinds"] = manifest.SupportedKinds
	values["indexed_kinds"] = manifest.IndexedKinds
	if len(manifest.KnowledgeRoots) > 0 {
		values["knowledge_roots"] = manifest.KnowledgeRoots
	}
	if len(manifest.Exclusions) > 0 {
		values["exclusions"] = manifest.Exclusions
	}
	if manifest.SchemaVersion == "1.2" {
		values["domain_registry"] = manifest.DomainRegistry
	}
	records := make([]any, 0, len(manifest.Records))
	for _, record := range manifest.Records {
		records = append(records, manifestRecordEntry(record))
	}
	values["records"] = records
	if manifest.dispositionsPresent || len(manifest.Dispositions) > 0 {
		values["dispositions"] = manifest.Dispositions
	}
	root := orderManifestFields(values, canonicalManifestRootOrder)
	compact, err := marshalManifestValue(root)
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, "publish_lesson", "cannot encode the knowledge manifest", false, "repair the manifest record", err)
	}
	indented := bytes.Buffer{}
	if err := json.Indent(&indented, compact, "", "  "); err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, "publish_lesson", "cannot encode the knowledge manifest", false, "repair the manifest record", err)
	}
	return append(indented.Bytes(), '\n'), nil
}

func marshalKnowledgeRecord(record KnowledgeRecord) ([]byte, error) {
	out, err := json.MarshalIndent(manifestRecordEntry(record), "", "  ")
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, "publish_lesson", "cannot encode the lesson record shard", false, "repair the manifest record", err)
	}
	return append(out, '\n'), nil
}
