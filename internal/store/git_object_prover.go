package store

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // Git's SHA-1 object format is an established content-addressing format, not a security primitive; this verifies object identity exactly the way Git computes it.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// One bounded read-scoped Git object prover per repository (CON-830
// structural repair). A single `git cat-file --batch` process owns every
// live object read of one verification pass: live configured-head commit
// resolution, the pinned commit/tree identities behind the knowledge
// content digest, and endpoint path/content proof. Every path is traversed
// through live tree objects read from the same process — no cached
// path-to-object map can stand in for the traversal — and every later
// probe is pinned to object IDs that process itself returned, so a deleted
// or replaced ref, commit, intermediate tree, or blob cannot ride an
// earlier verdict. Git flushes batch output after each object by default,
// which is what permits the interactive request/response use here
// (git-cat-file documentation, "BATCH OUTPUT FORMAT" and --buffer).
//
// Every served frame is bound to the request that produced it and to its
// own content-addressed identity. A frame's object type must be one of
// Git's closed object types; a missing answer must echo the exact spec it
// answers; a frame for a raw object-ID request must name that object; and
// the served type/length/body must hash, in Git's object format, to the
// object ID the frame names. Live bytes under an unchanged object ID do
// not prove anything by themselves — Git reads loose objects without
// re-verifying their hash — so a tampered commit, tree, or blob that kept
// its object ID reads as corrupt, never as proof.
//
// The session's lifetime is bounded at the owner: the process runs under
// a context derived from the caller's with the package's git command
// timeout, so a stalled partner cannot park a read forever, and close
// reaps the process within the same bound. A prover is not safe for
// arbitrary concurrent use: one verification pass owns its pool, each
// repository session is driven by one goroutine, and the only internal
// concurrency is a requestMany burst writer, whose failures synchronize
// with the reader through the session's sticky-failure mutex.
// Nothing crosses reads: every read pass starts, uses, and closes its own
// processes, so live integrity is never memoized across reads.
type gitObjectProver struct {
	repo      string
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	hashBytes int // 20 or 32, pinned from the first object the process resolved
	requests  int
	readBytes int64
	mu        sync.Mutex // guards failed, the one field both sides of a burst touch
	failed    error
	rootTrees map[string]string
	trees     map[string]map[string]gitProverTreeEntry
}

// Bounds for one prover session. A frame larger than the whole-command
// output bound is refused, total session reads stay under a fixed memory
// ceiling, and the request count ceiling keeps any parse loop from driving
// unbounded interaction.
const (
	proverMaxFrameBytes  = maxGitOutput
	proverMaxSessionByte = 64 << 20
	proverMaxRequests    = 65536
	proverHeaderBuffer   = 64 * 1024
	proverMaxSpecBytes   = 512
)

// gitProverTreeEntry is one entry of a parsed live tree object.
type gitProverTreeEntry struct {
	mode string
	name string
	oid  string
}

// gitProverEntry is the resolved entry of one path: the entry's raw mode,
// the object type that mode denotes, and the object ID.
type gitProverEntry struct {
	mode string
	typ  string
	oid  string
}

// gitProverCanonicalModes is the closed set of tree-entry modes canonical
// Git writes: regular and executable blobs, symbolic links, subtrees, and
// gitlinks. Anything else is not a canonical tree entry and never gets a
// silent default type.
var gitProverCanonicalModes = map[string]string{
	"100644": "blob",
	"100755": "blob",
	"120000": "blob",
	"40000":  "tree",
	"160000": "commit",
}

// gitProverObjectType maps a raw tree-entry mode onto the object type Git
// itself reports for it. The mode must be canonical; parseGitTreeContent
// enforces that before any entry reaches this map.
func gitProverObjectType(mode string) string {
	if typ, ok := gitProverCanonicalModes[mode]; ok {
		return typ
	}
	return ""
}

// errGitProverUnreachable marks a transport or framing failure: the process
// could not be started, died, or produced malformed output. It is never a
// verdict about an object's existence.
var errGitProverUnreachable = fmt.Errorf("git object prover transport failed")

// errGitProverCorrupt marks a content-addressed identity failure: Git
// served bytes under an object ID those bytes do not hash to, or a
// canonical object body that cannot be parsed. The stream stays in sync —
// the frame was consumed — so the session may keep serving other objects,
// but nothing about this object can be proved.
var errGitProverCorrupt = fmt.Errorf("git object fails its content address")

func startGitObjectProver(ctx context.Context, repo string) (*gitObjectProver, error) {
	if repo == "" {
		return nil, fmt.Errorf("git object prover requires a repository path")
	}
	// The owner bounds the session's lifetime with the package's git
	// command timeout: every other git invocation of this package carries
	// the same bound, and a batch session is one command. A partner that
	// stalls past the bound is killed by the context, which unblocks both
	// pipes; close then reaps it.
	cmdCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	cmd := exec.CommandContext(cmdCtx, "git", "-C", repo, "cat-file", "--batch") //nolint:gosec // git is fixed, argv values are package constants, and no shell is invoked.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: cannot open the prover stdin of %s: %v", errGitProverUnreachable, repo, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: cannot open the prover stdout of %s: %v", errGitProverUnreachable, repo, err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%w: cannot start git cat-file --batch in %s: %v", errGitProverUnreachable, repo, err)
	}
	return &gitObjectProver{
		repo:      repo,
		cmd:       cmd,
		cancel:    cancel,
		stdin:     stdin,
		stdout:    bufio.NewReaderSize(stdout, proverHeaderBuffer),
		rootTrees: map[string]string{},
		trees:     map[string]map[string]gitProverTreeEntry{},
	}, nil
}

// close ends the session: close stdin so Git exits at end of input, wait
// for the process with a bounded timeout, kill it if it does not exit, and
// release the owner's lifetime bound.
func (p *gitObjectProver) close() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(gitCommandTimeout):
		_ = p.cmd.Process.Kill()
		<-done
	}
	if p.cancel != nil {
		p.cancel()
	}
}

// kill ends the process without waiting for Git to exit; it unblocks a
// stalled partner goroutine of a failed batch.
func (p *gitObjectProver) kill() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
}

// fail records the session's sticky failure: after a transport or framing
// failure every later request refuses instead of guessing. Both sides of a
// pipelined burst can fail — the writer on a broken stdin, the reader on a
// dead stream — so the flag is mutex-guarded rather than caller-owned.
func (p *gitObjectProver) fail(err error) error {
	p.mu.Lock()
	if p.failed == nil {
		p.failed = fmt.Errorf("%w: %v", errGitProverUnreachable, err)
	}
	stuck := p.failed
	p.mu.Unlock()
	return stuck
}

// sticky returns the session's recorded failure, if any.
func (p *gitObjectProver) sticky() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed
}

// count guards requests against the session bounds.
func (p *gitObjectProver) count(n int) error {
	if err := p.sticky(); err != nil {
		return err
	}
	p.requests += n
	if p.requests > proverMaxRequests {
		return p.fail(fmt.Errorf("prover request ceiling exceeded in %s", p.repo))
	}
	return nil
}

// validateProverSpec admits only bounded object specs the batch protocol
// can carry as one line: package-built ref expressions or hexadecimal
// object IDs, never free-form caller input. The same rule guards every
// outgoing line, pipelined or not.
func validateProverSpec(spec string) error {
	if spec == "" || len(spec) > proverMaxSpecBytes || strings.ContainsAny(spec, " \t\r\n\x00") || strings.HasPrefix(spec, "-") {
		return fmt.Errorf("prover request %q is not a bounded object spec", spec)
	}
	return nil
}

// writeSpec sends one request line.
func (p *gitObjectProver) writeSpec(spec string) error {
	if err := validateProverSpec(spec); err != nil {
		return p.fail(err)
	}
	if _, err := io.WriteString(p.stdin, spec+"\n"); err != nil {
		return p.fail(err)
	}
	return nil
}

// readLine reads one bounded header line.
func (p *gitObjectProver) readLine() (string, error) {
	slice, err := p.stdout.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		return "", p.fail(fmt.Errorf("prover header line exceeds %d bytes in %s", proverHeaderBuffer, p.repo))
	}
	if err != nil {
		return "", p.fail(err)
	}
	return strings.TrimSuffix(string(slice), "\n"), nil
}

// readContent reads exactly size content bytes plus the trailing newline
// Git appends to every batch frame.
func (p *gitObjectProver) readContent(size int) ([]byte, error) {
	if size < 0 || size > proverMaxFrameBytes {
		return nil, p.fail(fmt.Errorf("prover frame of %d bytes is out of bounds in %s", size, p.repo))
	}
	if p.readBytes+int64(size) > proverMaxSessionByte {
		return nil, p.fail(fmt.Errorf("prover session reads exceed %d bytes in %s", proverMaxSessionByte, p.repo))
	}
	content := make([]byte, size)
	if _, err := io.ReadFull(p.stdout, content); err != nil {
		return nil, p.fail(err)
	}
	p.readBytes += int64(size)
	trailer, err := p.stdout.ReadByte()
	if err != nil || trailer != '\n' {
		return nil, p.fail(fmt.Errorf("prover frame is not newline-terminated in %s", p.repo))
	}
	return content, nil
}

// request resolves one spec to its object frame. ok=false reports Git's
// own "missing" answer for that spec; every other failure is a typed
// transport error.
func (p *gitObjectProver) request(spec string) (gitProverObject, error) {
	if err := p.count(1); err != nil {
		return gitProverObject{}, err
	}
	if err := p.writeSpec(spec); err != nil {
		return gitProverObject{}, err
	}
	return p.readFrame(spec)
}

// requestMany resolves a batch of specs through pipelined requests: the
// spec lines are validated in full before anything is written, then
// streamed in one write while the caller reads frames in order, so neither
// side can deadlock on a full pipe. Any failure kills the process so the
// partner goroutine cannot block on a dead stream. Every frame is bound to
// the spec at its own position: a stream that answers a different object,
// or reports another spec missing, is out of sync and fails the session.
func (p *gitObjectProver) requestMany(specs []string) ([]gitProverObject, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if err := p.count(len(specs)); err != nil {
		return nil, err
	}
	burst := make([]byte, 0, len(specs)*43)
	for _, spec := range specs {
		if err := validateProverSpec(spec); err != nil {
			return nil, p.fail(err)
		}
		burst = append(burst, spec...)
		burst = append(burst, '\n')
	}
	writeDone := make(chan error, 1)
	go func() {
		if err := p.writeBurst(burst); err != nil {
			p.kill()
			writeDone <- err
			return
		}
		writeDone <- nil
	}()
	objects := make([]gitProverObject, 0, len(specs))
	for _, spec := range specs {
		object, err := p.readFrame(spec)
		if err != nil {
			p.kill()
			<-writeDone
			return nil, err
		}
		objects = append(objects, object)
	}
	if writeErr := <-writeDone; writeErr != nil {
		p.kill()
		return nil, writeErr
	}
	return objects, nil
}

// writeBurst sends pre-validated spec lines in one bounded write.
func (p *gitObjectProver) writeBurst(burst []byte) error {
	if len(burst) > proverMaxRequests*(proverMaxSpecBytes+1) {
		return p.fail(fmt.Errorf("prover request burst exceeds the session bound in %s", p.repo))
	}
	if _, err := p.stdin.Write(burst); err != nil {
		return p.fail(err)
	}
	return nil
}

// readFrame reads the one framed response bound to spec: "<oid> SP <type>
// SP <size> LF" followed by size content bytes and a newline, or "<spec>
// SP missing" for an object Git cannot serve. Binding is by construction:
// the missing answer must echo the exact spec, the object type must be one
// of Git's closed object types, a raw object-ID request must be answered
// by that object, the object-ID width must stay consistent with everything
// the session already resolved, and the served type/length/body must hash,
// in Git's object format, to the object ID the frame names. A frame whose
// bytes do not hash to its object ID reads as corrupt (ok=true,
// corrupt=true): the frame was consumed completely, the stream stays in
// sync, but the object cannot be proved.
func (p *gitObjectProver) readFrame(spec string) (gitProverObject, error) {
	line, err := p.readLine()
	if err != nil {
		return gitProverObject{}, err
	}
	if strings.HasSuffix(line, " missing") {
		if line != spec+" missing" {
			return gitProverObject{}, p.fail(fmt.Errorf("prover answered %q missing for request %q in %s", line, spec, p.repo))
		}
		return gitProverObject{}, nil
	}
	// The header is split by hand: three space-separated fields, no
	// allocation, and any fourth field is rejected by the digit-only size
	// parser below.
	firstSpace := strings.IndexByte(line, ' ')
	if firstSpace <= 0 {
		return gitProverObject{}, p.fail(fmt.Errorf("malformed prover header %q in %s", line, p.repo))
	}
	oid := line[:firstSpace]
	rest := line[firstSpace+1:]
	secondSpace := strings.IndexByte(rest, ' ')
	if secondSpace <= 0 {
		return gitProverObject{}, p.fail(fmt.Errorf("malformed prover header %q in %s", line, p.repo))
	}
	typ, sizeField := rest[:secondSpace], rest[secondSpace+1:]
	if err := validateCommitOID(oid); err != nil {
		return gitProverObject{}, p.fail(fmt.Errorf("prover returned an invalid object id %q in %s", oid, p.repo))
	}
	if _, known := gitProverObjectTypes[typ]; !known {
		return gitProverObject{}, p.fail(fmt.Errorf("prover returned the unknown object type %q in %s", typ, p.repo))
	}
	if proverSpecIsRawOID(spec) && !strings.EqualFold(oid, spec) {
		return gitProverObject{}, p.fail(fmt.Errorf("prover answered object %s for the requested object %s in %s", oid, spec, p.repo))
	}
	if p.hashBytes != 0 && len(oid) != p.hashBytes*2 {
		return gitProverObject{}, p.fail(fmt.Errorf("prover mixed %d- and %d-hex object ids in %s", p.hashBytes*2, len(oid), p.repo))
	}
	size, sizeErr := parseProverSize(sizeField)
	if sizeErr != nil {
		return gitProverObject{}, p.fail(fmt.Errorf("prover returned an invalid size %q in %s", sizeField, p.repo))
	}
	content, err := p.readContent(size)
	if err != nil {
		return gitProverObject{}, err
	}
	p.pinHashLength(oid)
	corrupt := !strings.EqualFold(proverContentAddress(p.hashBytes, typ, content), oid)
	return gitProverObject{oid: oid, typ: typ, content: content, ok: true, corrupt: corrupt}, nil
}

// gitProverObjectTypes is the closed set of object types the batch
// protocol can report.
var gitProverObjectTypes = map[string]bool{"blob": true, "tree": true, "commit": true, "tag": true}

// gitProverObject is one resolved batch frame; ok=false reports Git's
// "missing" answer for the requested spec, and corrupt=true marks a frame
// whose served bytes do not hash, in Git's object format, to the object ID
// the frame named.
type gitProverObject struct {
	oid     string
	typ     string
	content []byte
	ok      bool
	corrupt bool
}

// proverSpecIsRawOID reports whether a spec is a bare hexadecimal object
// ID, whose response must name exactly that object.
func proverSpecIsRawOID(spec string) bool {
	if len(spec) != 40 && len(spec) != 64 {
		return false
	}
	for _, r := range spec {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// proverContentAddress computes the object ID Git's object format assigns
// to a body: the hash, SHA-1 or SHA-256 by the repository's object
// format, of "<type> <size> NUL <body>". The body is hashed in place — a
// burst of frames never copies every object's bytes a second time.
func proverContentAddress(hashBytes int, typ string, content []byte) string {
	header := []byte(typ + " " + strconv.Itoa(len(content)) + "\x00")
	if hashBytes == 32 {
		digest := sha256.New()
		digest.Write(header)
		digest.Write(content)
		return hex.EncodeToString(digest.Sum(nil))
	}
	digest := sha1.New() //nolint:gosec // Git's SHA-1 object format is the identity being verified, not a security decision.
	digest.Write(header)
	digest.Write(content)
	return hex.EncodeToString(digest.Sum(nil))
}

// parseProverSize is the prover's own decimal parser: digits only, no
// signs, bounded by the frame ceiling.
func parseProverSize(value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("empty size")
	}
	out := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit in size")
		}
		out = out*10 + int(r-'0')
		if out > proverMaxFrameBytes {
			return 0, fmt.Errorf("size out of bounds")
		}
	}
	return out, nil
}

// pinHashLength fixes the object-ID byte length from the first object this
// process resolved; tree parsing depends on it.
func (p *gitObjectProver) pinHashLength(oid string) {
	if p.hashBytes == 0 {
		if len(oid) == 64 {
			p.hashBytes = 32
		} else {
			p.hashBytes = 20
		}
	}
}

// resolveHeadCommit proves the live configured head: the ref expression is
// resolved by the batch process itself at request time, the frame is bound
// to the request through the closed type set and its content-addressed
// identity, and the returned commit OID is the object Git currently holds.
// Callers pin every later probe of the pass to the OID returned here. The
// resolution is never cached: a freshen retry in the same pass must
// observe a head that moved.
func (p *gitObjectProver) resolveHeadCommit(headRef string) (string, error) {
	if err := validateKnowledgeHomeRef(headRef); err != nil {
		return "", err
	}
	object, err := p.request(headRef + "^{commit}")
	if err != nil {
		return "", err
	}
	if !object.ok {
		return "", fmt.Errorf("%w: head ref %s does not resolve to a commit in %s", errGitProverUnreachable, headRef, p.repo)
	}
	if object.typ != "commit" {
		return "", fmt.Errorf("%w: head ref %s resolves to a %s in %s", errGitProverUnreachable, headRef, object.typ, p.repo)
	}
	if object.corrupt {
		return "", fmt.Errorf("%w: head commit of %s does not hash to its object id in %s", errGitProverCorrupt, headRef, p.repo)
	}
	return object.oid, nil
}

// resolveHeadWithRoot proves the live configured head and its root tree in
// one pipelined batch: both "<ref>^{commit}" and "<ref>^{tree}" resolve in
// a single exchange, each frame bound through the closed type set and its
// content-addressed identity, so a pass that immediately walks the root
// tree pays one round trip instead of two. Missing head, corrupt commit,
// or corrupt root tree refuse exactly as the sequential reads would.
func (p *gitObjectProver) resolveHeadWithRoot(headRef string) (string, string, error) {
	if err := validateKnowledgeHomeRef(headRef); err != nil {
		return "", "", err
	}
	frames, err := p.requestMany([]string{headRef + "^{commit}", headRef + "^{tree}"})
	if err != nil {
		return "", "", err
	}
	if len(frames) != 2 {
		return "", "", fmt.Errorf("%w: head resolution batch of %s returned %d frames", errGitProverUnreachable, p.repo, len(frames))
	}
	head, root := frames[0], frames[1]
	if !head.ok || !root.ok {
		return "", "", fmt.Errorf("%w: head ref %s does not resolve to a commit and tree in %s", errGitProverUnreachable, headRef, p.repo)
	}
	if head.typ != "commit" || root.typ != "tree" {
		return "", "", fmt.Errorf("%w: head ref %s resolves to a %s/%s in %s", errGitProverUnreachable, headRef, head.typ, root.typ, p.repo)
	}
	if head.corrupt {
		return "", "", fmt.Errorf("%w: head commit of %s does not hash to its object id in %s", errGitProverCorrupt, headRef, p.repo)
	}
	if root.corrupt {
		return "", "", fmt.Errorf("%w: root tree of %s does not hash to its object id in %s", errGitProverCorrupt, headRef, p.repo)
	}
	commitTree, err := gitProverCommitRoot(head.content)
	if err != nil {
		return "", "", fmt.Errorf("head commit %s in %s: %w", head.oid, p.repo, err)
	}
	if !strings.EqualFold(commitTree, root.oid) {
		return "", "", fmt.Errorf("%w: head ref %s changed between commit and tree resolution in %s", errGitProverUnreachable, headRef, p.repo)
	}
	// The root tree this pass proved for the head commit is the same
	// content-addressed identity commitRootTree would return, so the
	// pass's commit cache carries it and every later walk of the same
	// commit reuses it without another exchange.
	p.rootTrees[head.oid] = root.oid
	return head.oid, root.oid, nil
}

// commitRootTree reads one commit object live and returns its root tree
// ID. Results are content-addressed by commit ID, so one pass may reuse
// them; nothing survives the pass. A commit whose served bytes do not
// hash to its object ID is corrupt and proves nothing.
func (p *gitObjectProver) commitRootTree(commitOID string) (string, error) {
	if err := validateCommitOID(commitOID); err != nil {
		return "", err
	}
	if root, ok := p.rootTrees[commitOID]; ok {
		return root, nil
	}
	object, err := p.request(commitOID)
	if err != nil {
		return "", err
	}
	if !object.ok {
		return "", fmt.Errorf("commit object %s is missing in %s", commitOID, p.repo)
	}
	if object.typ != "commit" {
		return "", fmt.Errorf("object %s is a %s, not a commit, in %s", commitOID, object.typ, p.repo)
	}
	if object.corrupt {
		return "", fmt.Errorf("%w: commit %s does not hash to its object id in %s", errGitProverCorrupt, commitOID, p.repo)
	}
	treeLine, err := gitProverCommitRoot(object.content)
	if err != nil {
		return "", fmt.Errorf("commit %s in %s: %w", commitOID, p.repo, err)
	}
	p.rootTrees[commitOID] = treeLine
	return treeLine, nil
}

// gitProverCommitRoot extracts the root identity from a verified commit
// body. Both direct and pipelined resolution use this identity, so a ref
// movement cannot substitute another commit's root tree.
func gitProverCommitRoot(content []byte) (string, error) {
	header := content
	if index := indexByte(header, '\n'); index >= 0 {
		header = header[:index]
	}
	treeLine, found := strings.CutPrefix(string(header), "tree ")
	if !found || !validOIDString(treeLine) {
		return "", fmt.Errorf("%w: commit carries no valid tree line", errGitProverCorrupt)
	}
	return treeLine, nil
}

// readTree reads and parses one tree object live into a name-indexed map.
// Parsed entries are reused within the pass by content-addressed tree ID.
// A tree that does not hash to its object ID, or whose verified body is
// not a sequence of canonical entries, is corrupt: the error names the
// object, not the session, because the frame was consumed and the stream
// stays usable for other objects.
func (p *gitObjectProver) readTree(treeOID string) (map[string]gitProverTreeEntry, error) {
	if cached, ok := p.trees[treeOID]; ok {
		return cached, nil
	}
	object, err := p.request(treeOID)
	if err != nil {
		return nil, err
	}
	if !object.ok {
		return nil, fmt.Errorf("tree object %s is missing in %s", treeOID, p.repo)
	}
	if object.typ != "tree" {
		return nil, fmt.Errorf("object %s is a %s, not a tree, in %s", treeOID, object.typ, p.repo)
	}
	if p.hashBytes == 0 {
		p.hashBytes = 20
	}
	if object.corrupt {
		return nil, fmt.Errorf("%w: tree %s does not hash to its object id in %s", errGitProverCorrupt, treeOID, p.repo)
	}
	entries, err := parseGitTreeContent(object.content, p.hashBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: tree %s is not a canonical tree in %s: %v", errGitProverCorrupt, treeOID, p.repo, err)
	}
	p.trees[treeOID] = entries
	return entries, nil
}

// pathEntry resolves one path by walking live tree objects from the given
// root tree: every intermediate tree is read from the batch process in
// this pass, so a deleted or unreachable intermediate object fails the
// walk instead of riding a cached path map. found=false names a path the
// traversed trees do not carry; transport failures return err.
func (p *gitObjectProver) pathEntry(rootTreeOID, objectPath string) (gitProverEntry, bool, error) {
	if err := validateProverPath(objectPath); err != nil {
		return gitProverEntry{}, false, err
	}
	parts := strings.Split(objectPath, "/")
	current := rootTreeOID
	for index, part := range parts {
		entries, err := p.readTree(current)
		if err != nil {
			return gitProverEntry{}, false, err
		}
		entry, present := entries[part]
		if !present {
			return gitProverEntry{}, false, nil
		}
		if index == len(parts)-1 {
			return gitProverEntry{mode: entry.mode, typ: gitProverObjectType(entry.mode), oid: entry.oid}, true, nil
		}
		if gitProverObjectType(entry.mode) != "tree" {
			return gitProverEntry{}, false, nil
		}
		current = entry.oid
	}
	return gitProverEntry{}, false, nil
}

// prefetchTreePaths reads, level by level, every intermediate tree object
// the given paths traverse, one pipelined batch per tree depth. It is the
// same live traversal pathEntry performs — every tree of every directory
// prefix is read from this pass's batch process and identity-verified,
// never from a cross-pass memo — so afterwards each pathEntry of the pass
// resolves through trees the pass itself proved. Round trips per pass are
// bounded by the deepest path instead of growing with the path count: a
// thousand-endpoint proof costs the same few batch exchanges as one. A
// tree entry that names a needed subtree whose object is missing or
// corrupt fails here exactly as the sequential walk would have failed.
func (p *gitObjectProver) prefetchTreePaths(rootTreeOID, objectPath string, morePaths ...string) error {
	paths := append([]string{objectPath}, morePaths...)
	// children maps each directory prefix to the sorted child prefixes
	// some path needs; the root prefix is the empty string.
	children := map[string][]string{}
	prefixes := make([]string, 0, 16)
	for _, objectPath := range paths {
		if err := validateProverPath(objectPath); err != nil {
			return err
		}
		parts := strings.Split(objectPath, "/")
		for depth := 1; depth < len(parts); depth++ {
			prefix := strings.Join(parts[:depth], "/")
			parent := strings.Join(parts[:depth-1], "/")
			if _, seen := children[parent]; !seen {
				children[parent] = nil
				prefixes = append(prefixes, parent)
			}
			children[parent] = appendUnique(children[parent], prefix)
		}
	}
	sort.Strings(prefixes)
	type frontier struct {
		prefix string
		oid    string
	}
	current := []frontier{{prefix: "", oid: rootTreeOID}}
	for len(current) > 0 {
		cold := make([]string, 0, len(current))
		for _, step := range current {
			if _, warm := p.trees[step.oid]; !warm {
				cold = appendUnique(cold, step.oid)
			}
		}
		if len(cold) > 0 {
			sort.Strings(cold)
			frames, err := p.requestMany(cold)
			if err != nil {
				return err
			}
			for index, oid := range cold {
				frame := frames[index]
				if !frame.ok {
					return fmt.Errorf("tree object %s is missing in %s", oid, p.repo)
				}
				if frame.typ != "tree" {
					return fmt.Errorf("object %s is a %s, not a tree, in %s", oid, frame.typ, p.repo)
				}
				if frame.corrupt {
					return fmt.Errorf("%w: tree %s does not hash to its object id in %s", errGitProverCorrupt, oid, p.repo)
				}
				entries, parseErr := parseGitTreeContent(frame.content, p.hashBytesFor(oid))
				if parseErr != nil {
					return fmt.Errorf("%w: tree %s is not a canonical tree in %s: %v", errGitProverCorrupt, oid, p.repo, parseErr)
				}
				p.trees[oid] = entries
			}
		}
		next := make([]frontier, 0, len(current))
		for _, step := range current {
			entries := p.trees[step.oid]
			for _, prefix := range children[step.prefix] {
				name := prefix[strings.LastIndexByte(prefix, '/')+1:]
				if entry, present := entries[name]; present && gitProverObjectType(entry.mode) == "tree" {
					next = append(next, frontier{prefix: prefix, oid: entry.oid})
				}
			}
		}
		current = next
	}
	return nil
}

// hashBytesFor pins the raw object-ID width of one object ID the session
// resolved, for tree parsing.
func (p *gitObjectProver) hashBytesFor(oid string) int {
	p.pinHashLength(oid)
	return p.hashBytes
}

// appendUnique appends value when absent, keeping first-insertion order.
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// blobContents resolves many blob objects through one pipelined batch.
// Frames come back positionally bound to their request; a corrupt frame is
// reported on the object, not as a session failure. Identical object IDs
// are requested once: within a pass a content-addressed identity that was
// proved live is reusable, the same rule the pass's tree cache follows.
func (p *gitObjectProver) blobContents(oids []string) ([]gitProverObject, error) {
	if len(oids) == 0 {
		return nil, nil
	}
	distinct := make([]string, 0, len(oids))
	indexOf := make(map[string]int, len(oids))
	for _, oid := range oids {
		if _, seen := indexOf[oid]; !seen {
			indexOf[oid] = len(distinct)
			distinct = append(distinct, oid)
		}
	}
	frames, err := p.requestMany(distinct)
	if err != nil {
		return nil, err
	}
	objects := make([]gitProverObject, len(oids))
	for index, oid := range oids {
		objects[index] = frames[indexOf[oid]]
	}
	return objects, nil
}

// parseGitTreeContent parses one raw tree object body: a repeated sequence
// of "<mode> SP <name> NUL <raw object id>". The raw id length is pinned by
// the repository's object format; every mode must be one Git writes
// canonically; a name may not repeat — a duplicate entry is a corrupt
// tree, never a silent overwrite; and any leftover byte is malformed.
func parseGitTreeContent(content []byte, hashBytes int) (map[string]gitProverTreeEntry, error) {
	entries := make(map[string]gitProverTreeEntry, 16)
	pos := 0
	for pos < len(content) {
		space := indexByte(content[pos:], ' ')
		if space <= 0 {
			return nil, fmt.Errorf("malformed tree entry mode")
		}
		mode := string(content[pos : pos+space])
		if _, canonical := gitProverCanonicalModes[mode]; !canonical {
			return nil, fmt.Errorf("non-canonical tree entry mode %q", mode)
		}
		pos += space + 1
		nul := indexByte(content[pos:], 0)
		if nul <= 0 {
			return nil, fmt.Errorf("malformed tree entry name")
		}
		name := content[pos : pos+nul]
		if indexByte(name, '/') >= 0 {
			return nil, fmt.Errorf("malformed tree entry name with path separator")
		}
		pos += nul + 1
		if pos+hashBytes > len(content) {
			return nil, fmt.Errorf("truncated tree entry object id")
		}
		raw := content[pos : pos+hashBytes]
		pos += hashBytes
		oid := make([]byte, len(raw)*2)
		const hexDigits = "0123456789abcdef"
		for index, b := range raw {
			oid[index*2] = hexDigits[b>>4]
			oid[index*2+1] = hexDigits[b&0x0f]
		}
		if _, duplicate := entries[string(name)]; duplicate {
			return nil, fmt.Errorf("duplicate tree entry name %q", string(name))
		}
		entries[string(name)] = gitProverTreeEntry{mode: mode, name: string(name), oid: string(oid)}
	}
	return entries, nil
}

// validateProverPath admits only clean bounded relative paths the walk can
// step through one component at a time.
func validateProverPath(objectPath string) error {
	if objectPath == "" || len(objectPath) > 512 || strings.ContainsRune(objectPath, '\x00') ||
		strings.HasPrefix(objectPath, "/") || strings.HasPrefix(objectPath, "-") {
		return fmt.Errorf("prover path %q is not a bounded relative path", objectPath)
	}
	parts := strings.Split(objectPath, "/")
	if len(parts) > 64 {
		return fmt.Errorf("prover path %q exceeds the component bound", objectPath)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("prover path %q contains a forbidden component", objectPath)
		}
	}
	return nil
}

func indexByte(data []byte, b byte) int {
	for index, current := range data {
		if current == b {
			return index
		}
	}
	return -1
}

func validOIDString(oid string) bool {
	return validateCommitOID(oid) == nil
}

// gitProverPool hands one verification pass at most one live prover per
// distinct repository. The pass that created it is the only caller; its
// per-source validations may run concurrently, so the prover table is
// mutex-guarded while every session still has exactly one driving
// goroutine. close ends every process it started before the pass's read
// transaction opens.
type gitProverPool struct {
	ctx     context.Context
	mu      sync.Mutex
	provers map[string]*gitObjectProver
	repos   []string
	started int
}

func newGitProverPool(ctx context.Context) *gitProverPool {
	return &gitProverPool{ctx: ctx, provers: map[string]*gitObjectProver{}}
}

// prover returns the pass's prover for one repository, starting it on
// first use. Two sources over one repository share one process, so the
// process count of a pass is bounded by distinct repositories.
func (pool *gitProverPool) prover(repo string) (*gitObjectProver, error) {
	if pool == nil {
		return nil, fmt.Errorf("git prover pool is not initialized")
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if existing, ok := pool.provers[repo]; ok {
		return existing, nil
	}
	prover, err := startGitObjectProver(pool.ctx, repo)
	if err != nil {
		return nil, err
	}
	pool.provers[repo] = prover
	pool.repos = append(pool.repos, repo)
	pool.started++
	return prover, nil
}

// close ends every prover the pass started. Each session's exit is waited
// for concurrently: under load one slow git exit must not stack on top of
// another, so the pass's teardown costs the slowest exit, not their sum.
func (pool *gitProverPool) close() {
	if pool == nil {
		return
	}
	pool.mu.Lock()
	repos := append([]string(nil), pool.repos...)
	pool.mu.Unlock()
	sort.Strings(repos)
	if len(repos) == 0 {
		return
	}
	done := make(chan struct{}, len(repos))
	for _, repo := range repos {
		go func(prover *gitObjectProver) {
			defer func() { done <- struct{}{} }()
			prover.close()
		}(pool.provers[repo])
	}
	for range repos {
		<-done
	}
	pool.mu.Lock()
	pool.provers = map[string]*gitObjectProver{}
	pool.repos = nil
	pool.mu.Unlock()
}

// processCount reports how many Git processes this pass started.
func (pool *gitProverPool) processCount() int {
	if pool == nil {
		return 0
	}
	return pool.started
}
