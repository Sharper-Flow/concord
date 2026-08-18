package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	knowledgeScanLimit  = 1000
	maxKnowledgeNote    = 256 * 1024
	maxKnowledgeFront   = 32 * 1024
	maxKnowledgeValue   = 4096
	knowledgeQueryLimit = 100
	gitCommandTimeout   = 10 * time.Second
	maxGitOutput        = 4 * 1024 * 1024
)

// KnowledgeHome is the explicit authority context for git-derived knowledge.
// Stable IDs, not RepoPath, identify a home in the SQLite index.
type KnowledgeHome struct {
	HomeProjectID string
	HomeLocatorID string
	RepoPath      string
	HeadRef       string
}

// VerifiedNote is the committed byte proof and bounded metadata for one note.
// Content is returned to callers that need to inspect the exact proof; the
// SQLite index stores only the metadata and locator fields.
type VerifiedNote struct {
	ID            string
	Kind          string
	Title         string
	CompletedAt   string
	OutcomeTag    string
	LessonTags    []string
	TerminalState string
	Priority      int64
	Summary       string
	SuccessorID   string
	ProductIDs    []string
	ProjectIDs    []string
	ComponentIDs  []string
	TagIDs        []string
	ScopeMode     string
	NotePath      string
	CommitOID     string
	ContentHash   string
	Content       []byte
}

// CommittedNote is the git-side proof of a publish: the commit that was made
// and the path it carries. It is deliberately not a VerifiedNote.
//
// Verification is a separately ordered step owned by the caller. The accepted
// cross-authority order is publish, then verify, then record the SQLite locator
// (docs/agent-mutation-tool-contract.md). Returning an unverified proof here
// keeps that boundary visible: a caller holding a CommittedNote has written to
// git and has not yet earned the right to record a locator.
type CommittedNote struct {
	CommitOID string
	NotePath  string
}

// PublishCanonicalNote writes one approved note into the selected git home and
// commits only that path. It is intentionally separate from the SQLite
// transaction because Git and SQLite cannot share one atomic commit, and
// intentionally separate from verification so the caller orders the two.
func PublishCanonicalNote(ctx context.Context, home KnowledgeHome, workID, content, expectedHash string) (CommittedNote, error) {
	if home.RepoPath == "" || workID == "" || content == "" {
		return CommittedNote{}, newFailure(KindInvalidNoteProof, "publish_note", "canonical note publication is missing home, work, or content", false, "supply the approved note and git home")
	}
	note, err := parseKnowledgeNote([]byte(content))
	if err != nil {
		return CommittedNote{}, err
	}
	if note.ID != workID || note.Kind != "work_note" {
		return CommittedNote{}, newFailure(KindInvalidNoteProof, "publish_note", "approved note identity does not match the terminal work", false, "publish the canonical work note for the requested work ID")
	}
	if expectedHash != "" {
		sum := sha256.Sum256([]byte(content))
		if expectedHash != "sha256:"+hex.EncodeToString(sum[:]) {
			return CommittedNote{}, newFailure(KindInvalidNoteProof, "publish_note", "approved content digest does not match note bytes", false, "recompute the content digest over the exact approved bytes")
		}
	}
	date := time.Now().UTC().Format("2006-01-02")
	if len(note.CompletedAt) >= 10 {
		date = note.CompletedAt[:10]
	}
	name := slugifyKnowledgeTitle(note.Title)
	suffix := workID
	if len(suffix) > 16 {
		suffix = suffix[len(suffix)-16:]
	}
	notePath := "docs/work/" + date + "-" + name + "-" + suffix + ".md"
	fullPath := path.Join(home.RepoPath, notePath)
	// Detecting an already-published note is a precondition probe, not the
	// ordered verification step: it decides whether this call has anything to
	// write at all. The caller still verifies the proof it is handed back.
	if head, headErr := runGit(ctx, home.RepoPath, "rev-parse", "HEAD"); headErr == nil {
		if existing, verifyErr := VerifyCommittedNote(ctx, home.RepoPath, strings.TrimSpace(string(head)), notePath, expectedHash); verifyErr == nil {
			if existing.ID != workID {
				return CommittedNote{}, newFailure(KindKnowledgeAmbiguous, "publish_note", "canonical path already claims a different work ID", false, "resolve the competing canonical note before retrying")
			}
			return CommittedNote{CommitOID: existing.CommitOID, NotePath: existing.NotePath}, nil
		}
	}
	if err := os.MkdirAll(path.Dir(fullPath), 0o755); err != nil {
		return CommittedNote{}, wrapFailure(KindGitUnreachable, "publish_note", "cannot create the canonical note directory", true, "restore write access to the git home", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return CommittedNote{}, wrapFailure(KindGitUnreachable, "publish_note", "cannot write the canonical note draft", true, "restore write access to the git home", err)
	}
	if _, err := runGit(ctx, home.RepoPath, "add", "--", notePath); err != nil {
		return CommittedNote{}, wrapFailure(KindGitUnreachable, "publish_note", "cannot stage the canonical note", true, "restore git write access and retry", err)
	}
	if _, err := runGit(ctx, home.RepoPath, "commit", "--quiet", "-m", "docs: publish Concord work note", "--", notePath); err != nil {
		return CommittedNote{}, wrapFailure(KindGitUnreachable, "publish_note", "cannot commit the canonical note", true, "complete the native git commit and reconcile", err)
	}
	commit, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD")
	if err != nil {
		return CommittedNote{}, err
	}
	return CommittedNote{CommitOID: strings.TrimSpace(string(commit)), NotePath: notePath}, nil
}

func slugifyKnowledgeTitle(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// VerifyCommittedNote proves a canonical note from a committed git tree. It
// deliberately never reads note_path from the working tree.
func VerifyCommittedNote(ctx context.Context, repo, commitOID, notePath, expectedHash string) (VerifiedNote, error) {
	if err := validateCommitOID(commitOID); err != nil {
		return VerifiedNote{}, err
	}
	if err := validateNotePath(notePath); err != nil {
		return VerifiedNote{}, err
	}
	entry, err := gitTreeEntry(ctx, repo, commitOID, notePath)
	if err != nil {
		return VerifiedNote{}, err
	}
	if entry.mode != "100644" && entry.mode != "100755" || entry.kind != "blob" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "canonical note is not an ordinary blob", false, "commit a regular markdown file under an eligible note directory")
	}
	content, err := runGit(ctx, repo, "cat-file", "blob", commitOID+":"+notePath)
	if err != nil {
		return VerifiedNote{}, wrapFailure(KindInvalidNoteProof, "verify_note", "cannot read the committed note blob", true, "confirm the commit and path are available", err)
	}
	if len(content) > maxKnowledgeNote {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "note exceeds the bounded size", false, "split the note into bounded durable records")
	}
	sum := sha256.Sum256(content)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	if expectedHash != "" && expectedHash != hash {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "committed note hash does not match the expected hash", false, "re-read the committed blob and use its SHA-256")
	}
	note, err := parseKnowledgeNote(content)
	if err != nil {
		return VerifiedNote{}, err
	}
	note.NotePath, note.CommitOID, note.ContentHash, note.Content = notePath, commitOID, hash, append([]byte(nil), content...)
	return note, nil
}

// FindVerifiedWorkNote scans only the bounded canonical work-note directory at a
// recorded home head, verifies every candidate from the committed tree, and
// returns the unique note claiming workID. It never reads the working tree and
// never chooses between competing locators.
func FindVerifiedWorkNote(ctx context.Context, home KnowledgeHome, workID, expectedHash string) (VerifiedNote, error) {
	if workID == "" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "find_work_note", "work ID is empty", false, "supply the terminal work identity")
	}
	head, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		return VerifiedNote{}, err
	}
	paths, err := scanKnowledgeTree(ctx, home, head)
	if err != nil {
		return VerifiedNote{}, err
	}
	var matches []VerifiedNote
	var hashMismatch error
	for _, notePath := range paths {
		note, verifyErr := VerifyCommittedNote(ctx, home.RepoPath, head, notePath, "")
		if verifyErr != nil {
			continue
		}
		if note.ID != workID {
			continue
		}
		if expectedHash != "" && note.ContentHash != expectedHash {
			hashMismatch = newFailure(KindInvalidNoteProof, "find_work_note", "orphan note content hash does not match the approved proof", false, "supply the exact committed note digest")
			continue
		}
		matches = append(matches, note)
	}
	if len(matches) > 1 {
		return VerifiedNote{}, newFailure(KindKnowledgeAmbiguous, "find_work_note", "multiple canonical notes claim the same work ID", false, "resolve the competing canonical locators before reconciling")
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if hashMismatch != nil {
		return VerifiedNote{}, hashMismatch
	}
	return VerifiedNote{}, newFailure(KindKnowledgeMissing, "find_work_note", "no committed canonical note claims the terminal work ID", true, "commit the approved note or retry reconciliation")
}

type treeEntry struct {
	mode string
	kind string
	oid  string
	path string
}

func gitTreeEntry(ctx context.Context, repo, commitOID, notePath string) (treeEntry, error) {
	out, err := runGit(ctx, repo, "ls-tree", "-z", commitOID, "--", notePath)
	if err != nil {
		return treeEntry{}, wrapFailure(KindInvalidNoteProof, "verify_note", "cannot inspect the committed tree", true, "confirm the git home and commit are reachable", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil || len(entries) != 1 || entries[0].path != notePath {
		return treeEntry{}, newFailure(KindInvalidNoteProof, "verify_note", "note path is missing or ambiguous in the committed tree", false, "use exactly one ordinary blob at the canonical path")
	}
	return entries[0], nil
}

func scanKnowledgeTree(ctx context.Context, home KnowledgeHome, commitOID string) ([]string, error) {
	out, err := runGit(ctx, home.RepoPath, "ls-tree", "-r", "-z", commitOID, "--", "docs/work/")
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "rebuild_knowledge_index", "cannot scan the canonical work-note directory", true, "restore access to the git home and retry", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "git returned malformed tree entries", false, "repair the canonical git tree", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.mode != "100644" && entry.mode != "100755" || entry.kind != "blob" {
			return nil, newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "canonical work-note tree contains a non-blob entry", false, "remove symlink, gitlink, tree, or other non-regular entries from docs/work")
		}
		if !strings.HasSuffix(entry.path, ".md") {
			continue
		}
		if err := validateNotePath(entry.path); err != nil {
			return nil, err
		}
		paths = append(paths, entry.path)
		if len(paths) > knowledgeScanLimit {
			return nil, newFailure(KindKnowledgeIndexIncomplete, "rebuild_knowledge_index", "canonical note scan exceeds 1000 notes", true, "resume with an explicit continuation cursor")
		}
	}
	return paths, nil
}

func parseTreeEntries(out []byte) ([]treeEntry, error) {
	parts := strings.Split(string(out), "\x00")
	entries := make([]treeEntry, 0, len(parts))
	for _, record := range parts {
		if record == "" {
			continue
		}
		line := strings.SplitN(record, "\t", 2)
		if len(line) != 2 {
			return nil, fmt.Errorf("malformed tree record")
		}
		fields := strings.Fields(line[0])
		if len(fields) != 3 || line[1] == "" {
			return nil, fmt.Errorf("malformed tree entry")
		}
		entries = append(entries, treeEntry{mode: fields[0], kind: fields[1], oid: fields[2], path: line[1]})
	}
	return entries, nil
}

func resolveKnowledgeHead(ctx context.Context, home KnowledgeHome) (string, error) {
	if home.HomeProjectID == "" || home.HomeLocatorID == "" || home.RepoPath == "" || home.HeadRef == "" {
		return "", newFailure(KindInvalidFilter, "knowledge_home", "KnowledgeHome requires stable IDs, repository path, and head ref", false, "supply a complete explicit KnowledgeHome")
	}
	ref, err := runGit(ctx, home.RepoPath, "rev-parse", "--verify", home.HeadRef+"^{commit}")
	if err != nil {
		return "", wrapFailure(KindGitUnreachable, "knowledge_home", "cannot resolve the current git head", true, "restore access to the git home and retry", err)
	}
	commit := strings.TrimSpace(string(ref))
	if err := validateCommitOID(commit); err != nil {
		return "", wrapFailure(KindGitUnreachable, "knowledge_home", "git returned an invalid commit OID", false, "repair the configured head reference", err)
	}
	return commit, nil
}

func runGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	if repo == "" {
		return nil, fmt.Errorf("empty git repository path")
	}
	gitCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(gitCtx, "git", cmdArgs...)
	output := boundedGitOutput{limit: maxGitOutput}
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if output.exceeded {
		return nil, fmt.Errorf("git command output exceeds %d-byte limit", maxGitOutput)
	}
	return output.Bytes(), nil
}

// boundedGitOutput prevents a hostile or malformed git object/tree from
// allocating unbounded memory while preserving the fixed-command invocation.
type boundedGitOutput struct {
	limit    int
	exceeded bool
	data     bytes.Buffer
}

func (b *boundedGitOutput) Write(p []byte) (int, error) {
	remaining := b.limit - b.data.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	return b.data.Write(p)
}

func (b *boundedGitOutput) Bytes() []byte { return b.data.Bytes() }

func validateCommitOID(oid string) error {
	if len(oid) != 40 && len(oid) != 64 {
		return newFailure(KindInvalidNoteProof, "verify_note", "commit OID must contain 40 or 64 hexadecimal characters", false, "supply a full commit OID")
	}
	for _, r := range oid {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return newFailure(KindInvalidNoteProof, "verify_note", "commit OID contains a non-hexadecimal character", false, "supply a full commit OID")
		}
	}
	return nil
}

func validateContentHash(hash string) error {
	if len(hash) != len("sha256:")+64 || !strings.HasPrefix(hash, "sha256:") {
		return newFailure(KindInvalidNoteProof, "verify_note", "content hash must be a SHA-256 digest", false, "supply sha256 followed by 64 hexadecimal characters")
	}
	for _, r := range hash[len("sha256:"):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return newFailure(KindInvalidNoteProof, "verify_note", "content hash contains a non-hexadecimal character", false, "supply a SHA-256 digest")
		}
	}
	return nil
}

func validateNotePath(notePath string) error {
	if notePath == "" || strings.ContainsRune(notePath, '\x00') || strings.HasPrefix(notePath, "-") || strings.HasPrefix(notePath, "/") || path.Clean(notePath) != notePath {
		return newFailure(KindInvalidNoteProof, "verify_note", "note path is not a clean relative path", false, "use a clean relative canonical note path")
	}
	parts := strings.Split(notePath, "/")
	for _, part := range parts {
		if part == ".." || part == "" {
			return newFailure(KindInvalidNoteProof, "verify_note", "note path contains a forbidden path component", false, "use a clean relative canonical note path")
		}
	}
	if !strings.HasSuffix(notePath, ".md") || !(strings.HasPrefix(notePath, "docs/work/") || strings.HasPrefix(notePath, "docs/lessons/") || strings.HasPrefix(notePath, "docs/decisions/")) {
		return newFailure(KindInvalidNoteProof, "verify_note", "note path is outside the canonical markdown directories", false, "use docs/work, docs/lessons, or docs/decisions markdown")
	}
	return nil
}

func parseKnowledgeNote(content []byte) (VerifiedNote, error) {
	if len(content) > maxKnowledgeNote || len(content) < 8 || !strings.HasPrefix(string(content), "---\n") {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "note lacks bounded front matter", false, "use the accepted canonical front-matter template")
	}
	text := string(content)
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter is not closed", false, "close front matter with a standalone --- line")
	}
	end += 4
	front := text[4:end]
	if len(front) > maxKnowledgeFront {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter exceeds the bounded size", false, "keep canonical metadata bounded")
	}
	values := map[string]string{}
	allowed := map[string]bool{"concord_work_id": true, "id": true, "work_type": true, "type": true, "title": true, "completed_at": true, "outcome_tag": true, "lesson_tags": true, "terminal_state": true, "priority": true, "summary": true, "successor_work_id": true, "product_ids": true, "project_ids": true, "component_ids": true, "tag_ids": true}
	for _, line := range strings.Split(front, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" || !allowed[key] || values[key] != "" {
			return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter has an unknown, malformed, or duplicate key", false, "use only the accepted canonical metadata keys")
		}
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxKnowledgeValue {
			return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter contains an empty or oversized value", false, "supply bounded metadata values")
		}
		if (strings.HasPrefix(value, "\"") && (len(value) < 2 || !strings.HasSuffix(value, "\""))) || (strings.HasPrefix(value, "'") && (len(value) < 2 || !strings.HasSuffix(value, "'"))) {
			return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter contains an unclosed quoted scalar", false, "use a closed scalar value")
		}
		values[key] = unquoteScalar(value)
	}
	if values["concord_work_id"] != "" && values["id"] != "" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter declares two stable identity keys", false, "use concord_work_id for work notes or id for other knowledge notes")
	}
	if values["work_type"] != "" && values["type"] != "" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter declares two note type keys", false, "use work_type for work notes or type for other knowledge notes")
	}

	note := VerifiedNote{Title: values["title"], CompletedAt: values["completed_at"], OutcomeTag: values["outcome_tag"], TerminalState: values["terminal_state"], Summary: values["summary"], SuccessorID: values["successor_work_id"], ScopeMode: "explicit"}
	note.ID = values["concord_work_id"]
	if note.ID == "" {
		note.ID = values["id"]
	}
	note.Kind = values["type"]
	if note.Kind == "" && values["work_type"] != "" {
		note.Kind = "work_note"
	}
	if note.ID == "" || note.Kind == "" || note.Title == "" || note.CompletedAt == "" || note.OutcomeTag == "" || note.Summary == "" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter is missing required identity or summary fields", false, "complete the accepted canonical metadata template")
	}
	if note.Kind != "work_note" && note.Kind != "lesson" && note.Kind != "decision" && note.Kind != "spec" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "note type is not accepted", false, "use work_note, lesson, decision, or spec")
	}
	if note.Kind == "work_note" && values["concord_work_id"] == "" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "work note lacks concord_work_id", false, "identify work notes with concord_work_id")
	}
	if note.Kind != "work_note" && values["id"] == "" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "knowledge note lacks id", false, "identify lesson, decision, and spec notes with id")
	}
	if _, err := time.Parse(time.RFC3339Nano, note.CompletedAt); err != nil {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "completed_at is not RFC3339", false, "use an RFC3339 completion timestamp")
	}
	if note.TerminalState == "" {
		note.TerminalState = "completed"
	}
	if note.TerminalState != "completed" && note.TerminalState != "cancelled" && note.TerminalState != "superseded" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "terminal_state is not accepted", false, "use completed, cancelled, or superseded")
	}
	if note.TerminalState == "superseded" && note.SuccessorID == "" {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "superseded note lacks successor_work_id", false, "supply the canonical successor")
	}
	priority := values["priority"]
	if priority == "" {
		priority = "0"
	}
	note.Priority, _ = strconv.ParseInt(priority, 10, 64)
	if _, err := strconv.ParseInt(priority, 10, 64); err != nil || note.Priority < 0 {
		return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "priority is not a non-negative integer", false, "supply a bounded non-negative priority")
	}
	var err error
	for key, target := range map[string]*[]string{"lesson_tags": &note.LessonTags, "product_ids": &note.ProductIDs, "project_ids": &note.ProjectIDs, "component_ids": &note.ComponentIDs, "tag_ids": &note.TagIDs} {
		*target, err = parseScalarArray(values[key])
		if err != nil {
			return VerifiedNote{}, newFailure(KindInvalidNoteProof, "verify_note", "front matter array is malformed", false, "use a closed scalar array such as [sqlite, state-authority]")
		}
	}
	return note, nil
}

func unquoteScalar(value string) string {
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

func parseScalarArray(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("not an array")
	}
	inner := strings.TrimSpace(value[1 : len(value)-1])
	if inner == "" {
		return []string{}, nil
	}
	parts := strings.Split(inner, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		item := unquoteScalar(strings.TrimSpace(part))
		if item == "" || seen[item] {
			return nil, fmt.Errorf("empty or duplicate array item")
		}
		for _, r := range item {
			if unicode.IsSpace(r) && r != ' ' {
				return nil, fmt.Errorf("invalid array item")
			}
		}
		seen[item] = true
		result = append(result, item)
	}
	return result, nil
}

func marshalStrings(values []string) string {
	b, _ := json.Marshal(values)
	return string(b)
}
