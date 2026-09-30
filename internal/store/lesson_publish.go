package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// CD-0026: a lesson is captured per change and promoted by scope. Preparing
// a lesson writes the lesson markdown, its manifest record shard, and its
// coverage shard, and commits the three on the claimed worktree branch of
// the knowledge-home Project in one commit. The manifest — not a parallel
// event stream — remains the lesson's durable backing (CD-0020), so no new
// event kind or projection table is introduced; resolve_note verifies against
// the manifest immediately, and the next index rebuild picks the record up
// for search. The commit is prepared delivery: publication is the
// coordinator's merged pull request plus a verified knowledge read.

const (
	lessonRecordDir   = knowledgeRecordTree
	lessonCoverageDir = ".concord/docs/knowledge/coverage"
	maxLessonContent  = 32768
	maxLessonTags     = 8
	maxLessonEvidence = 32
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
	// Coverage is the explicit CD-0047 declaration of how the lesson is
	// proved. Publication never infers a state: a missing declaration is a
	// refusal, and the state-conditional reason, issue, or evidence the
	// caller supplies is what the coverage shard commits.
	Coverage *LessonCoverageDeclaration
	Now      time.Time
}

// LessonCoverageAnchor is one typed evidence anchor (CD-0047 D3). The closed
// kind set and value shapes mirror contracts/law-coverage.schema.json exactly
// — go_test, scenario, validator, generated — because the committed coverage
// shard must pass check-json against that schema: an anchor kind the schema
// refuses would fail CI after the prepared commit landed. Anchor resolution —
// that the test, scenario, validator, or generated symbol actually exists and
// is enforced — stays with scripts/check-law-coverage.py in CI, which owns
// that proposition.
type LessonCoverageAnchor struct {
	Kind  string
	Value string
}

// LessonCoverageDeclaration is the caller's explicit coverage state for the
// lesson record, using the one shared state vocabulary: satisfied, outstanding,
// unmeasured, out_of_scope. The state names the field that justifies it, and
// forbids the others, exactly as scripts/coverage_state.py enforces.
type LessonCoverageDeclaration struct {
	State    string
	Evidence []LessonCoverageAnchor
	// Issue is the outstanding pointer in the law-coverage union: exactly
	// one of the Linear issue identifier (Issue) or the positive integer
	// issue number (IssueNumber), as contracts/law-coverage.schema.json
	// admits for an outstanding record.
	Issue       string
	IssueNumber int64
	Reason      string
}

// PublishedLesson is the verified result of a lesson publication. Branch and
// CommitOID are prepared delivery evidence on the claimed worktree branch:
// the lesson is published only after the coordinator's pull request merges
// and a knowledge read verifies it (CD-0026 D1). A replay returns the pair
// only while the claimed branch head and its worktree still deliver exactly
// the prepared commit's lesson bytes, so the pair always names one
// pull-request-deliverable lesson.
type PublishedLesson struct {
	Record    KnowledgeRecord
	Note      VerifiedNote
	Branch    string
	CommitOID string
}

func (req LessonPublication) record(contentSHA string, date, notePath string) KnowledgeRecord {
	scopes := req.Scopes
	if scopes.Mode == "" {
		scopes.Mode = "home"
	}
	scopes.ProductIDs = nonNilIDs(scopes.ProductIDs)
	scopes.ProjectIDs = nonNilIDs(scopes.ProjectIDs)
	scopes.TagIDs = nonNilIDs(scopes.TagIDs)
	scopes.DomainIDs = nonNilIDs(scopes.DomainIDs)
	tags := nonNilIDs(req.Tags)
	return KnowledgeRecord{
		ID: req.LessonID, Kind: "lesson", Path: notePath, Status: "published",
		Authority: KnowledgeAuthority{Tier: "derived"},
		Date:      date, Title: req.Title, Summary: req.Summary, Tags: tags,
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
	if len(req.LessonID) < 2 || len(req.LessonID) > 128 || !lessonIDPattern.MatchString(req.LessonID) {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson id must be a bounded path-safe identifier of at least two characters", false, "supply a lesson id matching [A-Za-z0-9][A-Za-z0-9._:-]* with two to 128 characters")
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
	if err := validateLessonCoverage(req.Coverage); err != nil {
		return err
	}
	return nil
}

// lessonCoverageStates is the one shared coverage vocabulary (CD-0047 D2),
// mirrored from scripts/coverage_state.py: the state names the field that
// justifies it, and every other obligation field is forbidden.
var lessonCoverageStates = map[string]string{
	"satisfied":    "evidence",
	"outstanding":  "issue",
	"unmeasured":   "reason",
	"out_of_scope": "reason",
}

var (
	lessonGoTestAnchor    = regexp.MustCompile(`^[a-z0-9/_]+\.Test[A-Za-z0-9_]*$`)
	lessonValidatorAnchor = regexp.MustCompile(`^scripts/check-[a-z0-9-]+\.py$`)
	lessonGeneratedAnchor = regexp.MustCompile(`^[A-Za-z0-9./_-]+#[A-Za-z_][A-Za-z0-9_]*$`)
	lessonIssuePointer    = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)
)

// lessonIDPattern is the bounded path-safe lesson id vocabulary the public
// lesson_publish input schema declares (the shared id definition of
// contracts/agent-tool-surface-payloads.schema.json). The id becomes the
// record and coverage shard file names, so separators and traversal
// components never reach a derived path.
var lessonIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// validateLessonCoverage enforces the state-conditional obligations of the
// shared vocabulary. A declaration the caller did not make is a refusal:
// publication never infers out_of_scope, and no state silently substitutes
// for the one declared.
func validateLessonCoverage(coverage *LessonCoverageDeclaration) error {
	if coverage == nil {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson publication requires an explicit coverage declaration", false, "declare the lesson's coverage state with its reason, issue, or evidence")
	}
	required, known := lessonCoverageStates[coverage.State]
	if !known {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage state must be one of satisfied, outstanding, unmeasured, out_of_scope", false, "declare one shared coverage state")
	}
	// Forbidding the unused obligation fields matters as much as requiring
	// the used one: a record carrying both evidence and reason reads as
	// though it were justified twice and is really justified by neither.
	if required != "evidence" && len(coverage.Evidence) > 0 {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage state "+coverage.State+" forbids evidence", false, "declare only the field the state requires")
	}
	if required != "issue" && (coverage.Issue != "" || coverage.IssueNumber != 0) {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage state "+coverage.State+" forbids issue", false, "declare only the field the state requires")
	}
	if required != "reason" && coverage.Reason != "" {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage state "+coverage.State+" forbids reason", false, "declare only the field the state requires")
	}
	switch required {
	case "evidence":
		if len(coverage.Evidence) == 0 {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "satisfied coverage requires evidence anchors", false, "declare at least one typed anchor")
		}
		if len(coverage.Evidence) > maxLessonEvidence {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage carries too many evidence anchors", false, "declare at most thirty-two anchors")
		}
		for _, anchor := range coverage.Evidence {
			if err := validateLessonCoverageAnchor(anchor); err != nil {
				return err
			}
		}
	case "issue":
		hasLinear := coverage.Issue != ""
		hasNumber := coverage.IssueNumber >= 1
		switch {
		case hasLinear && hasNumber:
			return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage issue must be one pointer: an issue number or a Linear issue identifier, not both", false, "declare exactly one issue pointer")
		case hasLinear && !lessonIssuePointer.MatchString(coverage.Issue):
			return newFailure(KindInvalidNoteProof, "publish_lesson", "outstanding coverage requires a live issue identifier", false, "declare the Linear issue that tracks the lesson")
		case !hasLinear && !hasNumber:
			return newFailure(KindInvalidNoteProof, "publish_lesson", "outstanding coverage requires a live issue identifier", false, "declare the issue number or the Linear issue identifier that tracks the lesson")
		}
	case "reason":
		trimmed := strings.TrimSpace(coverage.Reason)
		if len(trimmed) < 12 || len(trimmed) > 1024 || trimmed != coverage.Reason {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage reason must be trimmed text of 12 to 1024 characters", false, "state why the lesson is unmeasured or out of scope")
		}
	}
	return nil
}

func validateLessonCoverageAnchor(anchor LessonCoverageAnchor) error {
	valueOk := false
	switch anchor.Kind {
	case "go_test":
		valueOk = lessonGoTestAnchor.MatchString(anchor.Value)
	case "validator":
		// The law-coverage schema admits only the check scripts CI invokes,
		// directly or nested through check-json.py; the wider harness set the
		// floor validators accept is not part of this plane.
		valueOk = lessonValidatorAnchor.MatchString(anchor.Value)
	case "generated":
		valueOk = lessonGeneratedAnchor.MatchString(anchor.Value)
	case "scenario":
		valueOk = len(anchor.Value) >= 1 && len(anchor.Value) <= 512 && !strings.ContainsAny(anchor.Value, " \t\n")
	default:
		return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage anchor kind must be one of go_test, scenario, validator, generated", false, "declare a typed anchor from the closed set")
	}
	if !valueOk {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "coverage anchor value does not match its declared kind", false, "declare the anchor value in the kind's form")
	}
	return nil
}

// validateLessonScopes mirrors the durable-knowledge scope rule: home carries
// no explicit IDs; explicit carries at least one.
func validateLessonScopes(scopes KnowledgeRecordScopes) error {
	if scopes.Mode == "" {
		scopes.Mode = "home"
	}
	counts := len(scopes.ProductIDs) + len(scopes.ProjectIDs) + len(scopes.DomainIDs) + len(scopes.TagIDs)
	switch scopes.Mode {
	case "home":
		if counts != 0 {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "home scope cannot carry explicit scope IDs", false, "use explicit scope mode to declare IDs")
		}
	case "explicit":
		if counts == 0 {
			return newFailure(KindInvalidNoteProof, "publish_lesson", "explicit scope must declare at least one scope ID", false, "declare the Product, Project, Domain, or tag scopes")
		}
	default:
		return newFailure(KindInvalidNoteProof, "publish_lesson", "scope mode must be home or explicit", false, "supply home or explicit")
	}
	return nil
}

// PublishLessonRecord prepares one accepted lesson for publication on the
// claimed worktree branch of the knowledge-home Project. The caller passes a
// home whose RepoPath is the claimed worktree and whose HeadRef is the pinned
// branch (ResolveLessonPublicationHome); this function refuses to write
// anywhere else — never the repository's default checkout, never a foreign
// Project's tree. It writes the lesson note, the record shard, and the
// coverage shard, and commits the three atomically on the claimed branch. It
// is idempotent: when the manifest already carries the exact record, the
// committed lesson verifies and is returned without a new commit. It performs
// no SQLite writes. Every validation refusal — invalid input, an occupied
// write target, a symlinked or escaping path, or a manifest rule the
// prospective record breaks — happens before the first byte is written and
// leaves the worktree unchanged. A failure while writing, staging, or
// committing can leave the files written so far in place; that failure is
// typed and names the state the caller restores before retrying. The
// returned branch and commit are prepared delivery evidence, not a
// publication claim: a lesson is published when the coordinator's pull
// request has merged and a knowledge read verifies it (CD-0026 D1, CD-0114 D3).
func PublishLessonRecord(ctx context.Context, home KnowledgeHome, req LessonPublication) (PublishedLesson, error) {
	out := PublishedLesson{}
	if home.RepoPath == "" {
		return out, newFailure(KindInvalidNoteProof, "publish_lesson", "lesson publication requires the git home", false, "publish through a registered knowledge home")
	}
	if err := verifyClaimedKnowledgeWorktree(ctx, home); err != nil {
		return out, err
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

	notePath := ".concord/docs/lessons/" + now.UTC().Format("2006-01-02") + "-" + slugifyKnowledgeTitle(req.Title) + ".md"
	recordShardPath := path.Join(lessonRecordDir, req.LessonID+".json")

	// The manifest the working-tree shards compose governs idempotency and
	// conflicts (CD-0114).
	shards, readErr := readKnowledgeShardsWorkingTree(home.RepoPath)
	if readErr != nil {
		return out, readErr
	}
	manifest, parseErr := composeKnowledgeManifest(shards)
	if parseErr != nil {
		return out, parseErr
	}
	// The same anchor gate a committed read runs (CD-0194 D2): publication
	// builds on the working-tree manifest, so an override whose anchor does
	// not prove out refuses before anything is written.
	if err := validateOverrideAnchors(manifest, workingTreeOverrideAnchorReader(home.RepoPath)); err != nil {
		return out, err
	}
	// A lesson id already in the manifest is a replay or a conflict, decided
	// by the complete record: the candidate rebuilt on the committed
	// record's own publication date and path must equal it field for field.
	// A changed title, summary, tag, scope, evidence list, or body is a
	// different record even when the slug and the path still collide, and
	// an identical replay on a later day still matches because the date and
	// the path come from the committed record, not from Now.
	for i := range manifest.Records {
		if manifest.Records[i].ID != req.LessonID {
			continue
		}
		existing := manifest.Records[i]
		if !sameNormalizedRecord(req.record(contentSHA, existing.Date, existing.Path), existing) {
			return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson id is already claimed by a different record", false, "replay with the lesson's original record fields or supersede the existing lesson")
		}
		return replayLesson(ctx, home, req, existing, existing.Path, contentSHA, manifest.SchemaVersion)
	}
	// A fresh publication never takes a path a different record already
	// carries with different content.
	for _, existing := range manifest.Records {
		if existing.Path == notePath && existing.SHA256 != contentSHA {
			return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson path is already claimed by different content", false, "retitle the lesson")
		}
	}
	record := req.record(contentSHA, date, notePath)
	if err := validateKnowledgeRecordForSchema(record, knowledgeKindsClosed, knowledgeKindsClosed, manifest.SchemaVersion, manifestRecordPathPrefix, nil); err != nil {
		return out, err
	}

	shard, err := marshalKnowledgeRecord(record)
	if err != nil {
		return out, err
	}
	coverageShardPath := path.Join(lessonCoverageDir, req.LessonID+".json")
	coverageShard, err := marshalLessonCoverageShard(req.LessonID, *req.Coverage)
	if err != nil {
		return out, err
	}

	// The new shard joins the manifest the shards compose, and the whole
	// composed manifest is validated before the first file is written, so a
	// record that collides or breaks a manifest rule — including a second
	// lesson id claiming a note path the manifest already carries — refuses
	// as the manifest conflict it is, with the worktree unchanged (CD-0114).
	shards.records[req.LessonID+".json"] = shard
	if _, err := composeKnowledgeManifest(shards); err != nil {
		return out, err
	}

	// Every write target is validated before anything is staged or written:
	// it resolves inside the claimed worktree, is free of any existing
	// entry, and reaches the target only through real directories. These
	// pre-write checks refuse the static cases typed and leave the worktree
	// unchanged; confinement at the write itself belongs to the Root-bound
	// writes below, which hold even when a component is swapped for a
	// symlink after these checks run.
	targets := []string{notePath, recordShardPath, coverageShardPath}
	for _, target := range targets {
		full, err := confineLessonTarget(home.RepoPath, target)
		if err != nil {
			return out, err
		}
		if err := refuseOccupiedLessonTarget(full); err != nil {
			return out, err
		}
		if err := refuseUnsafeLessonParents(home.RepoPath, full); err != nil {
			return out, err
		}
	}

	// The note, the record shard, and the coverage shard land as one commit
	// on the claimed branch, so CI's law-coverage plane never sees a record
	// whose coverage declaration is missing (CD-0047 D1, CD-0114 D3). The
	// writes resolve through one Root bound to the claimed worktree, so a
	// directory component replaced by a link between the checks above and a
	// write cannot move a file outside the tree, and O_CREATE|O_EXCL
	// refuses an occupied target at the write boundary. An I/O failure
	// after the first write leaves the files written so far in place, and
	// a staging or commit failure leaves all three written but uncommitted.
	payloads := [][]byte{[]byte(req.Content), shard, coverageShard}
	dirMsgs := []string{"cannot create the lesson directory", "cannot create the lesson record directory", "cannot create the lesson coverage directory"}
	fileMsgs := []string{"cannot write the lesson draft", "cannot write the lesson record shard", "cannot write the lesson coverage shard"}
	root, err := os.OpenRoot(home.RepoPath)
	if err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot open the claimed worktree", true, "restore the claimed worktree and retry", err)
	}
	defer func() { _ = root.Close() }()
	for i, target := range targets {
		if err := writeConfinedLessonFile(root, target, payloads[i], dirMsgs[i], fileMsgs[i]); err != nil {
			return out, err
		}
	}

	if _, err := runGit(ctx, home.RepoPath, "add", "--", notePath, recordShardPath, coverageShardPath); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot stage the lesson", true, "restore git write access and retry", err)
	}
	if _, err := runGit(ctx, home.RepoPath, "commit", "--quiet", "-m", "docs: publish Concord lesson "+req.LessonID, "--", notePath, recordShardPath, coverageShardPath); err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot commit the lesson", true, "complete the native git commit and reconcile", err)
	}
	commit, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD")
	if err != nil {
		return out, err
	}
	oid := strings.TrimSpace(string(commit))
	out.Record = record
	out.Note = manifestRecordNote(record, oid, manifest.SchemaVersion)
	out.Branch = home.HeadRef
	out.CommitOID = oid
	return out, nil
}

// sameNormalizedRecord reports whether a replay candidate reproduces the
// committed record field for field. Slices compare by length and element, so
// an omitted list and an empty one read as the same record, exactly as the
// shard round trip stores them.
func sameNormalizedRecord(candidate, existing KnowledgeRecord) bool {
	return candidate.Kind == existing.Kind && candidate.Status == existing.Status &&
		candidate.Authority == existing.Authority &&
		candidate.Date == existing.Date && candidate.Path == existing.Path &&
		candidate.Title == existing.Title && candidate.Summary == existing.Summary &&
		candidate.SHA256 == existing.SHA256 &&
		slices.Equal(candidate.Tags, existing.Tags) &&
		slices.Equal(candidate.Evidence, existing.Evidence) &&
		candidate.Scopes.Mode == existing.Scopes.Mode &&
		slices.Equal(candidate.Scopes.ProductIDs, existing.Scopes.ProductIDs) &&
		slices.Equal(candidate.Scopes.ProjectIDs, existing.Scopes.ProjectIDs) &&
		slices.Equal(candidate.Scopes.DomainIDs, existing.Scopes.DomainIDs) &&
		slices.Equal(candidate.Scopes.TagIDs, existing.Scopes.TagIDs)
}

// readGitBlob reads one path's blob out of a single commit's tree. Replay
// verification reads the prepared delivery from this tree, never from the
// working tree, so the commit a replay returns carries exactly the bytes the
// replay accepted.
func readGitBlob(ctx context.Context, repo, commitOID, repoPath string) ([]byte, error) {
	return runGit(ctx, repo, "cat-file", "blob", commitOID+":"+repoPath)
}

// replayLesson verifies an identical replay in full before returning it: the
// record matched in the working-tree manifest, so the note, the record shard,
// and the coverage shard must still equal the caller's request inside the
// lesson's own prepared commit — the immutable commit that added the record
// shard. All three are read from that commit's Git tree, never from the
// working tree: an uncommitted edit or a declaration the prepared commit
// never carried must refuse rather than return a commit that does not carry
// what the caller declared. The replay returns the prepared delivery only
// while the claimed branch head and its worktree still deliver exactly the
// prepared commit's lesson bytes: head commits that leave the three paths
// unchanged qualify, and a head commit or a staged or unstaged edit that
// changed any of them refuses, because a normal pull request from the branch
// would otherwise deliver bytes the returned commit does not carry. Any
// drift refuses typed; a replay never writes.
func replayLesson(ctx context.Context, home KnowledgeHome, req LessonPublication, existing KnowledgeRecord, notePath, contentSHA, schemaVersion string) (PublishedLesson, error) {
	out := PublishedLesson{}
	recordShardPath := path.Join(lessonRecordDir, req.LessonID+".json")
	coverageShardPath := path.Join(lessonCoverageDir, req.LessonID+".json")
	adding, err := runGit(ctx, home.RepoPath, "log", "--format=%H", "--diff-filter=A", "-1", "--", recordShardPath)
	if err != nil {
		return out, wrapFailure(KindGitUnreachable, "publish_lesson", "cannot read the lesson's prepared commit", true, "restore the claimed worktree and retry", err)
	}
	oid := strings.TrimSpace(string(adding))
	if len(oid) != 40 && len(oid) != 64 {
		return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "no commit on the claimed branch carries the lesson record shard", false, "publish the lesson on the claimed branch")
	}
	// The returned commit is the prepared delivery evidence, so every byte
	// the replay accepts comes from that commit's tree.
	noteBytes, err := readGitBlob(ctx, home.RepoPath, oid, notePath)
	if err != nil {
		return out, wrapFailure(KindKnowledgeAmbiguous, "publish_lesson", "the lesson note is missing from the prepared commit", false, "replay with the lesson's committed content or supersede the lesson", err)
	}
	sum := sha256.Sum256(noteBytes)
	if "sha256:"+hex.EncodeToString(sum[:]) != contentSHA {
		return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "the committed lesson note no longer matches the manifest record", false, "replay with the lesson's committed content or supersede the lesson")
	}
	committedRecordBytes, err := readGitBlob(ctx, home.RepoPath, oid, recordShardPath)
	if err != nil {
		return out, wrapFailure(KindKnowledgeAmbiguous, "publish_lesson", "the lesson record shard is missing from the prepared commit", false, "republish the lesson on the claimed branch", err)
	}
	if err := rejectDuplicateJSONKeys(committedRecordBytes); err != nil {
		return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "the prepared commit's record shard contains duplicate JSON keys", false, "repair the committed record shard")
	}
	var committedRecord KnowledgeRecord
	if err := json.Unmarshal(committedRecordBytes, &committedRecord); err != nil {
		return out, wrapFailure(KindKnowledgeAmbiguous, "publish_lesson", "the prepared commit's record shard is not a readable record", false, "repair the committed record shard", err)
	}
	// Mirror the manifest parser's legacy-tier default so a schema-1.2 record
	// compares the same here as it does in the working-tree gate.
	if committedRecord.Authority.Tier == "" && schemaVersion == knowledgeManifestSchemaLegacy {
		committedRecord.Authority = KnowledgeAuthority{Tier: "derived"}
	}
	if !sameNormalizedRecord(req.record(contentSHA, committedRecord.Date, committedRecord.Path), committedRecord) {
		return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "the prepared commit carries a different record than the caller replayed", false, "replay with the lesson's original record fields or supersede the lesson")
	}
	coverageShard, err := marshalLessonCoverageShard(req.LessonID, *req.Coverage)
	if err != nil {
		return out, err
	}
	committedCoverage, err := readGitBlob(ctx, home.RepoPath, oid, coverageShardPath)
	if err != nil {
		return out, wrapFailure(KindKnowledgeAmbiguous, "publish_lesson", "the lesson coverage shard is missing from the prepared commit", false, "replay with the lesson's original coverage declaration or supersede the lesson", err)
	}
	if !bytes.Equal(committedCoverage, coverageShard) {
		return out, newFailure(KindKnowledgeAmbiguous, "publish_lesson", "the coverage declaration does not match the committed coverage shard", false, "replay with the lesson's original coverage declaration or supersede the lesson")
	}
	// The returned branch and commit must describe one pull-request-deliverable
	// lesson, so the claimed branch head and its worktree must still deliver
	// exactly the prepared commit's bytes for the lesson's three paths.
	if err := requireLessonDeliveryAtHead(ctx, home, oid, notePath, recordShardPath, coverageShardPath); err != nil {
		return out, err
	}
	out.Record = existing
	out.Note = manifestRecordNote(existing, oid, schemaVersion)
	out.Branch = home.HeadRef
	out.CommitOID = oid
	return out, nil
}

// requireLessonDeliveryAtHead holds the prepared-delivery join: the replay's
// returned branch and commit pair must describe one pull-request-deliverable
// lesson, so the claimed branch's head commit must still name the prepared
// commit's blob for each of the lesson's three paths, and the worktree and
// index must carry no uncommitted change to them. A later branch commit or a
// staged or unstaged edit that changed any of the three would make a normal
// pull request from the branch deliver bytes the returned commit does not
// carry, so the replay refuses typed instead of returning evidence the merge
// cannot honor. Head commits that leave all three paths unchanged keep the
// replay valid.
func requireLessonDeliveryAtHead(ctx context.Context, home KnowledgeHome, preparedCommit string, lessonPaths ...string) error {
	for _, lessonPath := range lessonPaths {
		prepared, err := gitObjectID(ctx, home.RepoPath, preparedCommit+":"+lessonPath)
		if err != nil {
			return wrapFailure(KindGitUnreachable, "publish_lesson", "cannot read "+lessonPath+" in the lesson's prepared commit", true, "restore the claimed worktree and retry", err)
		}
		head, err := gitObjectID(ctx, home.RepoPath, "HEAD:"+lessonPath)
		if err != nil || head != prepared {
			return newFailure(KindKnowledgeAmbiguous, "publish_lesson", "the claimed branch head no longer delivers "+lessonPath+" with the prepared commit's content", false, "restore the lesson's committed bytes on the claimed branch or supersede the lesson")
		}
	}
	dirty, err := runGit(ctx, home.RepoPath, append([]string{"status", "--porcelain", "--"}, lessonPaths...)...)
	if err != nil {
		return wrapFailure(KindGitUnreachable, "publish_lesson", "cannot read the claimed worktree's lesson state", true, "restore the claimed worktree and retry", err)
	}
	if strings.TrimSpace(string(dirty)) != "" {
		return newFailure(KindKnowledgeAmbiguous, "publish_lesson", "the claimed worktree or its index carries an uncommitted change to the lesson", false, "commit or restore the lesson's three files before replaying")
	}
	return nil
}

// gitObjectID resolves one object ID for a revision such as "<commit>:<path>"
// (the blob at that path in that commit's tree) or ":<path>" (the index's
// stage-0 blob). A missing object is an error, never an empty ID.
func gitObjectID(ctx context.Context, repo, rev string) (string, error) {
	out, err := runGit(ctx, repo, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// marshalLessonCoverageShard serialises the caller's declaration in the
// canonical shard byte form: alphabetical keys, two-space indent, one
// trailing newline (scripts/shard_format.py).
func marshalLessonCoverageShard(lessonID string, coverage LessonCoverageDeclaration) ([]byte, error) {
	shard := map[string]any{"id": lessonID, "state": coverage.State}
	switch lessonCoverageStates[coverage.State] {
	case "evidence":
		anchors := make([]any, 0, len(coverage.Evidence))
		for _, anchor := range coverage.Evidence {
			anchors = append(anchors, map[string]any{"kind": anchor.Kind, "value": anchor.Value})
		}
		shard["evidence"] = anchors
	case "issue":
		if coverage.IssueNumber >= 1 {
			shard["issue"] = coverage.IssueNumber
		} else {
			shard["issue"] = coverage.Issue
		}
	case "reason":
		shard["reason"] = coverage.Reason
	}
	out, err := json.MarshalIndent(shard, "", "  ")
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, "publish_lesson", "cannot encode the lesson coverage shard", false, "repair the coverage declaration", err)
	}
	return append(out, '\n'), nil
}

// verifyClaimedKnowledgeWorktree is the host check that keeps lesson
// publication inside the claimed worktree: the path is a git work tree, it is
// a linked worktree rather than the repository's default checkout, and its
// HEAD is the claimed branch. Anything else refuses before a byte is written.
func verifyClaimedKnowledgeWorktree(ctx context.Context, home KnowledgeHome) error {
	inside, err := runGit(ctx, home.RepoPath, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return newFailure(KindGitUnreachable, "publish_lesson", "the claimed knowledge-home path is not a git work tree", false, "claim the knowledge-home worktree and publish through it")
	}
	gitDir, err := runGit(ctx, home.RepoPath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return newFailure(KindGitUnreachable, "publish_lesson", "cannot verify the claimed worktree's git directory", false, "restore the claimed worktree and retry")
	}
	commonDir, err := runGit(ctx, home.RepoPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return newFailure(KindGitUnreachable, "publish_lesson", "cannot verify the claimed worktree's repository directory", false, "restore the claimed worktree and retry")
	}
	common := strings.TrimSpace(string(commonDir))
	if !filepath.IsAbs(common) {
		common = filepath.Join(home.RepoPath, common)
	}
	if filepath.Clean(strings.TrimSpace(string(gitDir))) == filepath.Clean(common) {
		return newFailure(KindGitUnreachable, "publish_lesson", "the claimed path is the repository's default checkout, not a claimed worktree", false, "claim a worktree for the knowledge-home Project and publish through it")
	}
	if home.HeadRef == "" {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson publication requires the claimed branch", false, "resolve the knowledge home through its claimed worktree")
	}
	branch, err := runGit(ctx, home.RepoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || strings.TrimSpace(string(branch)) != home.HeadRef {
		return newFailure(KindGitUnreachable, "publish_lesson", "the claimed worktree is not on the claimed branch", false, "restore the claimed branch before publishing")
	}
	return nil
}

func marshalKnowledgeRecord(record KnowledgeRecord) ([]byte, error) {
	out, err := json.MarshalIndent(manifestRecordEntry(record), "", "  ")
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, "publish_lesson", "cannot encode the lesson record shard", false, "repair the manifest record", err)
	}
	return append(out, '\n'), nil
}

// confineLessonTarget resolves one repo-relative write target inside the
// claimed worktree and refuses any result that leaves it: an escaping join,
// a path component that is empty, ".", "..", or carries a NUL. The check
// stands behind the caller-side id and slug vocabularies, so no derived
// path is trusted to have kept a safe component shape.
func confineLessonTarget(repoRoot, repoRelative string) (string, error) {
	abs := filepath.Join(repoRoot, filepath.FromSlash(repoRelative))
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", newFailure(KindInvalidNoteProof, "publish_lesson", "lesson write target escapes the claimed worktree", false, "publish with ids and titles that stay inside the claimed tree")
	}
	for _, part := range strings.Split(repoRelative, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, '\x00') {
			return "", newFailure(KindInvalidNoteProof, "publish_lesson", "lesson write target carries an unsafe path component", false, "publish with ids and titles that form clean path components")
		}
	}
	return abs, nil
}

// refuseOccupiedLessonTarget refuses any existing entry at a write target —
// regular file, directory, or symlink, including a dangling one — before any
// byte moves. Lstat, not Stat: a symlink must refuse even when its
// destination is missing. The check is validation only; O_CREATE|O_EXCL in
// writeConfinedLessonFile repeats the refusal at the write boundary, where a
// target created after this check runs is still refused.
func refuseOccupiedLessonTarget(absPath string) error {
	if _, err := os.Lstat(absPath); err == nil {
		return newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson write target is already occupied", false, "choose a lesson id and title whose note, record shard, and coverage shard paths are free")
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrapFailure(KindGitUnreachable, "publish_lesson", "cannot inspect the lesson write target", true, "restore the claimed worktree and retry", err)
	}
	return nil
}

// refuseUnsafeLessonParents walks the target's parent directories from the
// worktree root down and refuses a symlink or non-directory component, so
// the static link cases refuse typed before any write. The check is
// validation only: the Root-bound write refuses a component swapped for a
// link after this walk, because no Root name may resolve through a link
// outside the root. Components below the first missing one are created by
// the write itself as plain directories.
func refuseUnsafeLessonParents(repoRoot, absPath string) error {
	rel, err := filepath.Rel(repoRoot, filepath.Dir(absPath))
	if err != nil {
		return newFailure(KindInvalidNoteProof, "publish_lesson", "lesson write target escapes the claimed worktree", false, "publish with ids and titles that stay inside the claimed tree")
	}
	current := repoRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return wrapFailure(KindGitUnreachable, "publish_lesson", "cannot inspect a lesson target parent directory", true, "restore the claimed worktree and retry", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson target parent is a symlink", false, "restore the claimed worktree's real directories")
		}
		if !info.IsDir() {
			return newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson target parent is not a directory", false, "restore the claimed worktree's real directories")
		}
	}
	return nil
}

// writeConfinedLessonFile creates one lesson file inside the Root bound to
// the claimed worktree. The Root owns confinement at the write: every name
// component resolves against the root's directory handle, so a component
// replaced by a link after the preflight checks cannot move the file
// outside the tree, and O_CREATE|O_EXCL refuses an occupied target at the
// write itself. An occupied target keeps the preflight refusal's typed
// shape; any other write failure reports the worktree state the caller
// restores before retrying.
func writeConfinedLessonFile(root *os.Root, repoRelative string, payload []byte, dirMsg, fileMsg string) error {
	if err := root.MkdirAll(path.Dir(repoRelative), 0o755); err != nil { //nolint:gosec // lessons are public repository content and require normal Git directory permissions.
		return wrapFailure(KindGitUnreachable, "publish_lesson", dirMsg, true, "restore the claimed worktree and retry", err)
	}
	f, err := root.OpenFile(repoRelative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // lessons are public repository content and require normal Git file permissions.
	if errors.Is(err, os.ErrExist) {
		return newFailure(KindKnowledgeAmbiguous, "publish_lesson", "lesson write target is already occupied", false, "choose a lesson id and title whose note, record shard, and coverage shard paths are free")
	}
	if err != nil {
		return wrapFailure(KindGitUnreachable, "publish_lesson", fileMsg, true, "restore the claimed worktree and retry", err)
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return wrapFailure(KindGitUnreachable, "publish_lesson", fileMsg, true, "restore the claimed worktree and retry", err)
	}
	if err := f.Close(); err != nil {
		return wrapFailure(KindGitUnreachable, "publish_lesson", fileMsg, true, "restore the claimed worktree and retry", err)
	}
	return nil
}
