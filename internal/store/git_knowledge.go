package store

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	gitCommandTimeout = 10 * time.Second
	maxGitOutput      = 4 * 1024 * 1024
)

// KnowledgeHome is the explicit authority context for git-derived knowledge.
// Stable IDs, not RepoPath, identify a home in the SQLite index.
type KnowledgeHome struct {
	HomeProjectID string
	HomeLocatorID string
	RepoPath      string
	HeadRef       string
}

type treeEntry struct {
	mode string
	kind string
	oid  string
	path string
}

// gitTreeEntryRunner resolves exactly one tree entry at the declared path
// through the caller's runner, so the dispatch preparation can inject a test
// runner and keep one shared deadline. A missing or ambiguous path refuses.
func gitTreeEntryRunner(ctx context.Context, runner GitRunner, repo, commitOID, notePath string) (treeEntry, error) {
	out, err := runner.Run(ctx, repo, "ls-tree", "-z", commitOID, "--", notePath)
	if err != nil {
		return treeEntry{}, wrapFailure(KindInvalidNoteProof, "verify_note", "cannot inspect the committed tree", true, "confirm the git home and commit are reachable", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil || len(entries) != 1 || entries[0].path != notePath {
		return treeEntry{}, newFailure(KindInvalidNoteProof, "verify_note", "note path is missing or ambiguous in the committed tree", false, "use exactly one ordinary blob at the canonical path")
	}
	return entries[0], nil
}

// committedRegularBlobBytes is the shared committed-blob proof primitive: it
// resolves exactly one ordinary blob (100644/100755) at the declared path and
// commit through the caller's runner and returns its bytes. Committed law and
// registry reads prove content through this one owner.
func committedRegularBlobBytes(ctx context.Context, runner GitRunner, repo, commitOID, path string) ([]byte, error) {
	entry, err := gitTreeEntryRunner(ctx, runner, repo, commitOID, path)
	if err != nil {
		return nil, err
	}
	if entry.mode != "100644" && entry.mode != "100755" || entry.kind != "blob" {
		return nil, newFailure(KindInvalidNoteProof, "verify_note", "committed blob is not an ordinary blob", false, "commit a regular blob at the declared path")
	}
	content, err := runner.Run(ctx, repo, "cat-file", "blob", commitOID+":"+path)
	if err != nil {
		return nil, wrapFailure(KindInvalidNoteProof, "verify_note", "cannot read the committed blob", true, "confirm the commit and path are available", err)
	}
	return content, nil
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
	if strings.HasPrefix(home.HeadRef, "-") || strings.ContainsAny(home.HeadRef, " \t\n\r\x00") {
		return "", newFailure(KindInvalidFilter, "knowledge_home", "KnowledgeHome head ref contains option or delimiter bytes", false, "supply a Git ref without a leading dash, whitespace, or NUL")
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

// resolveKnowledgeHeadCheap re-reads a repository's head commit with a few
// small file reads and no subprocess, network call, or bulk filesystem
// operation — exactly the cheap in-transaction re-validation the
// connection-pool rule admits. It resolves the head the way git does: a detached HEAD
// names the commit directly, a symbolic HEAD names a ref that a loose ref
// file resolves, and a ref absent from the loose tree resolves through the
// packed-refs file. A linked worktree keeps its own HEAD and takes shared
// refs from its common directory. Everything it cannot resolve cheaply is an
// error, never a guess: the admission that calls it is fail-closed, and the
// caller re-establishes its verification outside the transaction.
func resolveKnowledgeHeadCheap(repoPath, headRef string) (string, error) {
	if repoPath == "" || headRef == "" || strings.HasPrefix(headRef, "-") || strings.ContainsAny(headRef, " \t\n\r\x00") {
		return "", fmt.Errorf("knowledge head needs a repository path and a ref without option or delimiter bytes")
	}
	gitDir, err := knowledgeGitDir(repoPath)
	if err != nil {
		return "", err
	}
	ref := headRef
	if ref == "HEAD" {
		raw, err := os.ReadFile(path.Join(gitDir, "HEAD")) //nolint:gosec // gitDir is derived from the operator-selected git authority and HEAD is a fixed name.
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(raw))
		if oid, valid := commitOIDFromRefLine(line); valid {
			return oid, nil
		}
		symbolic, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			return "", fmt.Errorf("git HEAD is neither a commit nor a symbolic ref")
		}
		ref = symbolic
	}
	return resolveGitRefCheap(gitDir, ref)
}

// knowledgeGitDir locates a repository's git directory: the `.git` directory
// beside the work tree, or the directory the `.git` file names.
func knowledgeGitDir(repoPath string) (string, error) {
	dotGit := path.Join(repoPath, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dotGit, nil
	}
	raw, err := os.ReadFile(dotGit) //nolint:gosec // dotGit is a fixed name under the operator-selected git authority.
	if err != nil {
		return "", err
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok {
		return "", fmt.Errorf(".git is neither a git directory nor a gitdir pointer")
	}
	if !path.IsAbs(gitdir) {
		gitdir = path.Join(repoPath, gitdir)
	}
	return gitdir, nil
}

// resolveGitRefCheap resolves one fully qualified ref through the loose ref
// file, then the packed-refs file, in the worktree git directory and its
// common directory. Only refs under `refs/` whose every path component is a
// plain name resolve, which bounds every path to a fixed subtree of the git
// directory and refuses a ref that would traverse out of it.
func resolveGitRefCheap(gitDir, ref string) (string, error) {
	return resolveGitRefDepthCheap(gitDir, ref, 0)
}

func resolveGitRefDepthCheap(gitDir, ref string, depth int) (string, error) {
	if depth >= 5 {
		return "", fmt.Errorf("git symbolic ref exceeds the cheap resolution bound")
	}
	if !strings.HasPrefix(ref, "refs/") || !safeGitRefPath(ref) {
		return "", fmt.Errorf("git ref stays outside the refs/ tree")
	}
	dirs := []string{gitDir}
	if raw, err := os.ReadFile(path.Join(gitDir, "commondir")); err == nil { //nolint:gosec // commondir is a fixed name under the operator-selected git authority.
		common := strings.TrimSpace(string(raw))
		if !path.IsAbs(common) {
			common = path.Join(gitDir, common)
		}
		dirs = append(dirs, common)
	}
	for _, dir := range dirs {
		if raw, err := os.ReadFile(path.Join(dir, ref)); err == nil { //nolint:gosec // safeGitRefPath bounds ref to plain components under the fixed refs/ subtree.
			line := strings.TrimSpace(string(raw))
			if oid, ok := commitOIDFromRefLine(line); ok {
				return oid, nil
			}
			if symbolic, ok := strings.CutPrefix(line, "ref: "); ok {
				return resolveGitRefDepthCheap(gitDir, symbolic, depth+1)
			}
			return "", fmt.Errorf("git loose ref is malformed")
		}
	}
	for _, dir := range dirs {
		oid, err := packedGitRef(dir, ref)
		if err == nil {
			return oid, nil
		}
	}
	return "", fmt.Errorf("git ref cannot be resolved with a cheap read")
}

// safeGitRefPath reports whether a ref path carries only plain components:
// no empty, dot, or dot-dot segment. A ref that fails it could make the
// loose-ref read below escape the git directory, so the caller refuses it
// before touching the filesystem.
func safeGitRefPath(ref string) bool {
	for _, part := range strings.Split(ref, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func packedGitRef(gitDir, ref string) (string, error) {
	raw, err := os.ReadFile(path.Join(gitDir, "packed-refs")) //nolint:gosec // packed-refs is a fixed name under the operator-selected git authority.
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		oid, name, ok := strings.Cut(line, " ")
		if !ok || name != ref {
			continue
		}
		if _, valid := commitOIDFromRefLine(strings.TrimSpace(oid)); valid {
			return strings.TrimSpace(oid), nil
		}
	}
	return "", fmt.Errorf("packed-refs does not carry the ref")
}

// commitOIDFromRefLine reports whether a trimmed git ref line is a commit
// OID of either hash length.
func commitOIDFromRefLine(line string) (string, bool) {
	if err := validateCommitOID(line); err != nil {
		return "", false
	}
	return line, true
}

func runGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return runGitEnv(ctx, repo, nil, args...)
}

func runGitEnv(ctx context.Context, repo string, env []string, args ...string) ([]byte, error) {
	if repo == "" {
		return nil, fmt.Errorf("empty git repository path")
	}
	gitCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(gitCtx, "git", cmdArgs...) //nolint:gosec // git is fixed, callers pass separate guarded argv values, and no shell is invoked.
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
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

// marshalStrings renders a string slice for the JSON-array columns of the
// projection. Every such column declares json_type(...)='array', which a nil
// slice would violate: encoding/json renders nil as null, so an absent
// optional list would fail the CHECK rather than store an empty array.
func marshalStrings(values []string) string {
	if values == nil {
		return "[]"
	}
	b, _ := json.Marshal(values)
	return string(b)
}

// cheapLawSourceOIDLimit bounds every loose object the cheap law-content
// walk reads. A commit or directory tree larger than this is not a small
// file read, and the walk is fail-closed: the caller re-establishes its
// verification outside the transaction instead of guessing.
const cheapLawSourceOIDLimit = 64 * 1024

// resolveLawSourceOIDsCheap re-reads a repository's committed law content
// identity — the law-folder tree OID and the Domain registry blob OID at the
// resolved head — with a few small object file reads and no subprocess,
// network call, or bulk filesystem operation, under the same cheap-read
// admission resolveKnowledgeHeadCheap runs under. It exists so the transaction-scoped
// law boundary can distinguish a head that moved without touching committed
// law content (admitted, PM1 §3.3) from one that moved the law itself
// (refused). Everything it cannot resolve cheaply is an error, never a
// guess: packed-only objects, oversized trees, and missing paths that Git
// itself would have to decode refuse, and the caller re-verifies.
func resolveLawSourceOIDsCheap(repoPath, headRef string) (knowledgeSourceVerifiedRevision, error) {
	revision := knowledgeSourceVerifiedRevision{lawTreeOID: "absent", registryOID: "absent"}
	head, err := resolveKnowledgeHeadCheap(repoPath, headRef)
	if err != nil {
		return revision, err
	}
	revision.head = head
	gitDir, err := knowledgeGitDir(repoPath)
	if err != nil {
		return revision, err
	}
	hexLength := len(head)
	commit, err := readLooseGitObjectCheap(gitDir, head, "commit")
	if err != nil {
		return revision, err
	}
	rootTree, err := gitCommitTreeOIDCheap(commit, hexLength)
	if err != nil {
		return revision, err
	}
	root, err := readLooseGitObjectCheap(gitDir, rootTree, "tree")
	if err != nil {
		return revision, err
	}
	concordOID, err := gitTreeEntryOIDCheap(root, ".concord", hexLength)
	if err != nil {
		return revision, err
	}
	if concordOID == "" {
		return revision, nil
	}
	concord, err := readLooseGitObjectCheap(gitDir, concordOID, "tree")
	if err != nil {
		return revision, err
	}
	lawOID, err := gitTreeEntryOIDCheap(concord, "law", hexLength)
	if err != nil {
		return revision, err
	}
	if lawOID != "" {
		revision.lawTreeOID = "tree:" + lawOID
	}
	docsOID, err := gitTreeEntryOIDCheap(concord, "docs", hexLength)
	if err != nil {
		return revision, err
	}
	if docsOID == "" {
		return revision, nil
	}
	docs, err := readLooseGitObjectCheap(gitDir, docsOID, "tree")
	if err != nil {
		return revision, err
	}
	knowledgeOID, err := gitTreeEntryOIDCheap(docs, "knowledge", hexLength)
	if err != nil {
		return revision, err
	}
	if knowledgeOID == "" {
		return revision, nil
	}
	knowledge, err := readLooseGitObjectCheap(gitDir, knowledgeOID, "tree")
	if err != nil {
		return revision, err
	}
	registryOID, err := gitTreeEntryOIDCheap(knowledge, "domain-registry.json", hexLength)
	if err != nil {
		return revision, err
	}
	if registryOID != "" {
		revision.registryOID = "blob:" + registryOID
	}
	return revision, nil
}

// cheapGitObjectDirs lists the object directories a repository's loose
// objects may live in: the worktree git directory and, for a linked
// worktree, its common directory.
func cheapGitObjectDirs(gitDir string) []string {
	dirs := []string{gitDir}
	if raw, err := os.ReadFile(path.Join(gitDir, "commondir")); err == nil { //nolint:gosec // commondir is a fixed name under the operator-selected git authority.
		common := strings.TrimSpace(string(raw))
		if !path.IsAbs(common) {
			common = path.Join(gitDir, common)
		}
		dirs = append(dirs, common)
	}
	return dirs
}

// readLooseGitObjectCheap reads and inflates one small loose object of the
// expected type from the repository's object directories and returns its
// body without the "<type> <size>\0" header every loose object carries. A
// packed-only object, an absent object, an object of another type, a header
// whose size disagrees with the body, or an object beyond the small-read
// bound is an error: the walk is fail-closed and never falls back to a
// subprocess inside a transaction.
func readLooseGitObjectCheap(gitDir, oid, objectType string) ([]byte, error) {
	if len(oid) != 40 && len(oid) != 64 {
		return nil, fmt.Errorf("object name is not a 40- or 64-hex git object name")
	}
	if _, err := hex.DecodeString(oid); err != nil || strings.ToLower(oid) != oid {
		return nil, fmt.Errorf("object name is not lowercase hexadecimal")
	}
	for _, dir := range cheapGitObjectDirs(gitDir) {
		objectPath := path.Join(dir, "objects", oid[:2], oid[2:])
		info, err := os.Stat(objectPath)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > cheapLawSourceOIDLimit {
			return nil, fmt.Errorf("git object %s exceeds the cheap read bound", oid[:8])
		}
		raw, err := os.ReadFile(objectPath) //nolint:gosec // the object path is assembled from the validated object name and the operator-selected git authority.
		if err != nil {
			return nil, err
		}
		reader, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		object, err := io.ReadAll(io.LimitReader(reader, cheapLawSourceOIDLimit+1))
		closeErr := reader.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(object) > cheapLawSourceOIDLimit {
			return nil, fmt.Errorf("git object %s exceeds the cheap read bound", oid[:8])
		}
		header, body, found := bytes.Cut(object, []byte{0})
		if !found || string(header) != objectType+" "+strconv.Itoa(len(body)) {
			return nil, fmt.Errorf("git object %s is not a well-formed loose %s object", oid[:8], objectType)
		}
		return body, nil
	}
	return nil, fmt.Errorf("git object %s is not available as a loose object", oid[:8])
}

// gitCommitTreeOIDCheap reads the tree object name out of a commit object
// body, whose first line is always "tree <hex>".
func gitCommitTreeOIDCheap(commit []byte, hexLength int) (string, error) {
	first, _, _ := strings.Cut(string(commit), "\n")
	if oid, ok := strings.CutPrefix(first, "tree "); ok && len(oid) == hexLength {
		return oid, nil
	}
	return "", fmt.Errorf("commit object carries no tree header the cheap walk can read")
}

// gitTreeEntryOIDCheap resolves one entry of a decoded tree object by name.
// Tree entries are "<mode> <name>\0<raw object id>"; the raw id width follows
// the repository's object-name length. A missing name resolves to an empty
// OID, which the caller treats as committed absence.
func gitTreeEntryOIDCheap(tree []byte, name string, hexLength int) (string, error) {
	rawLength := hexLength / 2
	position := 0
	for position < len(tree) {
		space := bytes.IndexByte(tree[position:], ' ')
		if space < 0 {
			return "", fmt.Errorf("tree object entry has no mode separator")
		}
		nul := bytes.IndexByte(tree[position+space:], 0)
		if nul < 0 {
			return "", fmt.Errorf("tree object entry has no name terminator")
		}
		entryName := string(tree[position+space+1 : position+space+nul])
		start := position + space + nul + 1
		if start+rawLength > len(tree) {
			return "", fmt.Errorf("tree object entry is truncated")
		}
		if entryName == name {
			return hex.EncodeToString(tree[start : start+rawLength]), nil
		}
		position = start + rawLength
	}
	return "", nil
}
