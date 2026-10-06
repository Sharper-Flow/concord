package store

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // Git's SHA-1 object format is the identity under test.
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// con830SyntheticOID returns the object ID Git's SHA-1 object format
// assigns to a body, so synthetic frames can be built content-addressed.
func con830SyntheticOID(typ string, content string) string {
	header := typ + " " + fmt.Sprint(len(content)) + "\x00"
	sum := sha1.Sum([]byte(header + content)) //nolint:gosec // Git object format, not a security primitive.
	return hex.EncodeToString(sum[:])
}

// con830HardeningWriteCloser records outgoing request bytes.
type con830HardeningWriteCloser struct {
	written []byte
}

func (w *con830HardeningWriteCloser) Write(p []byte) (int, error) {
	w.written = append(w.written, p...)
	return len(p), nil
}
func (w *con830HardeningWriteCloser) Close() error { return nil }

// con830Frame builds one batch frame line for a body.
func con830Frame(oid, typ, content string) string {
	return fmt.Sprintf("%s %s %d\n%s\n", oid, typ, len(content), content)
}

// The batch protocol is a framed request/response protocol: every frame is
// bound to the spec that produced it, its object type must be one Git
// reports, its object-ID width must stay consistent, and its bytes must
// parse completely. Malformed, truncated, binary, oversized, or unbound
// frames fail the session instead of being interpreted.
func TestCON830ProverFrameDomainRefused(t *testing.T) {
	want := strings.Repeat("1", 40)
	blobBody := "hello"
	blobOID := con830SyntheticOID("blob", blobBody)
	commitBody := "tree " + strings.Repeat("3", 40) + "\n"
	commitOID := con830SyntheticOID("commit", commitBody)
	corruptOID := strings.Repeat("a", 40)
	cases := map[string]struct {
		spec     string
		response string
		accept   bool
		served   bool // the frame served an object rather than answering missing
		corrupt  bool
	}{
		"malformed_two_fields":         {want, "abc def\n", false, false, false},
		"malformed_four_fields":        {want, "a b c d\n", false, false, false},
		"binary_garbage_header":        {want, "git\x00blob 3\nabc\n", false, false, false},
		"non_digit_size":               {want, want + " blob 1x2\nabc\n", false, false, false},
		"oversized_size":               {want, fmt.Sprintf("%s blob %d\n", want, proverMaxFrameBytes+1), false, false, false},
		"truncated_content":            {want, want + " blob 10\nshort", false, false, false},
		"missing_trailer_newline":      {want, want + " blob 3\nabcX", false, false, false},
		"oversized_header_line":        {want, strings.Repeat("a", proverHeaderBuffer+1) + "\n", false, false, false},
		"wrong_oid_for_raw_request":    {want, con830Frame(strings.Repeat("2", 40), "blob", ""), false, false, false},
		"unknown_object_type":          {want, con830Frame(want, "imaginary", ""), false, false, false},
		"unrelated_missing":            {want, "unrelated missing\n", false, false, false},
		"missing_of_other_spec":        {want, strings.Repeat("2", 40) + " missing\n", false, false, false},
		"matching_missing":             {want, want + " missing\n", true, false, false},
		"valid_identity_frame":         {blobOID, con830Frame(blobOID, "blob", blobBody), true, true, false},
		"corrupt_identity_is_named":    {corruptOID, con830Frame(corruptOID, "blob", blobBody), true, true, true},
		"ref_expression_frame":         {"refs/heads/main^{commit}", con830Frame(commitOID, "commit", commitBody), true, true, false},
		"ref_expression_other_missing": {"refs/heads/main^{commit}", "refs/heads/other^{commit} missing\n", false, false, false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(testCase.response))}
			object, err := p.request(testCase.spec)
			if testCase.accept {
				if err != nil {
					t.Fatalf("in-domain frame refused: %v", err)
				}
				if object.ok != testCase.served {
					t.Fatalf("frame served=%t missing=%t, want served %t", object.ok, !object.ok, testCase.served)
				}
				if object.corrupt != testCase.corrupt {
					t.Fatalf("frame corrupt flag = %t, want %t", object.corrupt, testCase.corrupt)
				}
				return
			}
			if err == nil {
				t.Fatalf("out-of-domain frame accepted: %+v", object)
			}
			if !errors.Is(err, errGitProverUnreachable) {
				t.Fatalf("framing failure is not a transport failure: %v", err)
			}
		})
	}
}

// A session that resolved one object-ID width refuses the other width: a
// repository has exactly one object format, and tree parsing depends on
// the pinned width.
func TestCON830ProverHashFormatConsistencyHeld(t *testing.T) {
	body := "x"
	narrowOID := con830SyntheticOID("blob", body)
	frame := con830Frame(narrowOID, "blob", body)
	wide := strings.Repeat("c", 64)
	p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(frame + con830Frame(wide, "blob", body)))}
	if _, err := p.request(narrowOID); err != nil {
		t.Fatalf("first frame refused: %v", err)
	}
	if p.hashBytes != 20 {
		t.Fatalf("hash width pinned at %d bytes, want 20", p.hashBytes)
	}
	if _, err := p.request(wide); err == nil {
		t.Fatal("mixed object-ID width accepted")
	}
}

// Positional binding of a pipelined batch: the first frame answers the
// first spec and the second the second; a swap is a framing failure, and
// a corrupt frame is named on its object without failing the batch.
func TestCON830ProverBatchFramesBindPositionally(t *testing.T) {
	bodyOne, bodyTwo := "one", "two"
	first := con830SyntheticOID("blob", bodyOne)
	second := con830SyntheticOID("blob", bodyTwo)
	frameOne := con830Frame(first, "blob", bodyOne)
	frameTwo := con830Frame(second, "blob", bodyTwo)
	t.Run("in_order", func(t *testing.T) {
		p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(frameOne + frameTwo))}
		objects, err := p.requestMany([]string{first, second})
		if err != nil {
			t.Fatalf("ordered batch refused: %v", err)
		}
		if len(objects) != 2 || objects[0].oid != con830SyntheticOID("blob", bodyOne) || objects[1].oid != con830SyntheticOID("blob", bodyTwo) {
			t.Fatalf("batch frames not bound by position: %+v", objects)
		}
	})
	t.Run("swapped", func(t *testing.T) {
		p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(frameTwo + frameOne))}
		if _, err := p.requestMany([]string{first, second}); err == nil {
			t.Fatal("swapped batch frames accepted")
		}
	})
	t.Run("corrupt_frame_named_per_object", func(t *testing.T) {
		corrupt := con830Frame(strings.Repeat("9", 40), "blob", bodyOne)
		p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(corrupt + frameTwo))}
		objects, err := p.requestMany([]string{strings.Repeat("9", 40), second})
		if err != nil {
			t.Fatalf("corrupt frame failed the batch: %v", err)
		}
		if !objects[0].corrupt || objects[1].corrupt {
			t.Fatalf("per-object corruption lost: %+v", objects)
		}
	})
}

// requestMany validates the whole outgoing domain before writing: a burst
// carrying one out-of-domain spec writes nothing at all.
func TestCON830ProverRequestManyValidatesBeforeWriting(t *testing.T) {
	writer := &con830HardeningWriteCloser{}
	p := &gitObjectProver{repo: "synthetic", stdin: writer, stdout: bufio.NewReader(strings.NewReader(""))}
	if _, err := p.requestMany([]string{strings.Repeat("1", 40), "BAD SPEC", strings.Repeat("2", 40)}); err == nil {
		t.Fatal("out-of-domain burst accepted")
	}
	if len(writer.written) != 0 {
		t.Fatalf("out-of-domain burst wrote %d bytes before validation", len(writer.written))
	}
	if err := validateProverSpec("-leading-dash"); err == nil {
		t.Fatal("option-like spec admitted")
	}
}

// A frame whose bytes do not hash to its object ID is corruption, not
// transport trouble and never proof: every live reader of the prover
// refuses it by name.
func TestCON830ProverCorruptObjectsRefuseProof(t *testing.T) {
	wrongOID := strings.Repeat("b", 40)
	t.Run("tree", func(t *testing.T) {
		p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(con830Frame(wrongOID, "tree", "100644 a\x00"+strings.Repeat("c", 20))))}
		if _, err := p.readTree(wrongOID); !errors.Is(err, errGitProverCorrupt) {
			t.Fatalf("corrupt tree error = %v", err)
		}
	})
	t.Run("commit", func(t *testing.T) {
		p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(con830Frame(wrongOID, "commit", "tree "+strings.Repeat("c", 40))))}
		if _, err := p.commitRootTree(wrongOID); !errors.Is(err, errGitProverCorrupt) {
			t.Fatalf("corrupt commit error = %v", err)
		}
	})
	t.Run("head", func(t *testing.T) {
		p := &gitObjectProver{repo: "synthetic", stdin: &con830HardeningWriteCloser{}, stdout: bufio.NewReader(strings.NewReader(con830Frame(wrongOID, "commit", "tree "+strings.Repeat("c", 40))))}
		if _, err := p.resolveHeadCommit("HEAD"); !errors.Is(err, errGitProverCorrupt) {
			t.Fatalf("corrupt head error = %v", err)
		}
	})
}

// Canonical tree entries only: unknown or zero-padded modes, path
// separators in names, truncated object IDs, and duplicate names are
// corrupt trees, never silent defaults or overwrites.
func TestCON830ProverTreeDomainHeld(t *testing.T) {
	oid := strings.Repeat("0", 20)
	cases := map[string]struct {
		content string
		wantErr bool
	}{
		"valid_regular":       {"100644 a\x00" + oid, false},
		"valid_executable":    {"100755 a\x00" + oid, false},
		"valid_symlink":       {"120000 a\x00" + oid, false},
		"valid_subtree":       {"40000 a\x00" + oid, false},
		"valid_gitlink":       {"160000 a\x00" + oid, false},
		"zero_padded_mode":    {"040000 a\x00" + oid, true},
		"group_writable_mode": {"100664 a\x00" + oid, true},
		"unknown_mode":        {"123456 a\x00" + oid, true},
		"empty_mode":          {" a\x00" + oid, true},
		"name_with_slash":     {"100644 a/b\x00" + oid, true},
		"duplicate_name":      {"100644 a\x00" + oid + "100755 a\x00" + oid, true},
		"truncated_oid":       {"100644 a\x00" + strings.Repeat("0", 19), true},
		"leftover_byte":       {"100644 a\x00" + oid + "x", true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			entries, err := parseGitTreeContent([]byte(testCase.content), 20)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("out-of-domain tree accepted: %+v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonical tree refused: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("canonical tree parsed into %d entries, want 1", len(entries))
			}
		})
	}
	for mode, wantType := range map[string]string{"100644": "blob", "100755": "blob", "120000": "blob", "40000": "tree", "160000": "commit"} {
		if got := gitProverObjectType(mode); got != wantType {
			t.Fatalf("mode %s maps to %q, want %q", mode, got, wantType)
		}
	}
	if got := gitProverObjectType("123456"); got != "" {
		t.Fatalf("non-canonical mode maps to %q, want the empty type", got)
	}
}

// con830HardeningRepo creates a one-commit repository for lifetime probes.
func con830HardeningRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "con830@example.invalid"},
		{"config", "user.name", "con830"},
	} {
		if _, err := runGit(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(repo+"/one.txt", []byte("one\n"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "add", "one.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "commit", "-qm", "one"); err != nil {
		t.Fatal(err)
	}
	return repo
}

// The owner bounds the session lifetime: the process context carries the
// package's git command deadline, a stalled partner is killed at that
// bound so a blocked read returns, and a canceled or closed session is
// reaped without hanging.
func TestCON830ProverOwnerLifetimeBounded(t *testing.T) {
	repo := con830HardeningRepo(t)
	t.Run("live_session_resolves", func(t *testing.T) {
		p, err := startGitObjectProver(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		oid, err := p.resolveHeadCommit("HEAD")
		if err != nil || len(oid) != 40 {
			t.Fatalf("live head resolution: %v %q", err, oid)
		}
		if _, err := p.request(oid); err != nil {
			t.Fatalf("live raw-OID request refused: %v", err)
		}
	})
	t.Run("stalled_read_returns_at_bound", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		p, err := startGitObjectProver(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		if err := p.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatalf("cannot stall git: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := p.resolveHeadCommit("HEAD")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("stalled head resolution returned proof")
			}
		case <-time.After(gitCommandTimeout + 5*time.Second):
			t.Fatal("stalled read outlived the owner's lifetime bound")
		}
		_ = p.cmd.Process.Signal(syscall.SIGCONT)
	})
	t.Run("canceled_session_is_reaped", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		p, err := startGitObjectProver(ctx, repo)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		reaped := make(chan struct{}, 1)
		go func() {
			p.close()
			reaped <- struct{}{}
		}()
		select {
		case <-reaped:
		case <-time.After(gitCommandTimeout + 5*time.Second):
			t.Fatal("canceled session was not reaped within the bound")
		}
	})
}
