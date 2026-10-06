package store

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoordinatorCON830ProverRejectsUnboundFrames(t *testing.T) {
	want := strings.Repeat("1", 40)
	for name, response := range map[string]string{
		"different_oid":     strings.Repeat("2", 40) + " blob 0\n\n",
		"unknown_type":      want + " imaginary 0\n\n",
		"unrelated_missing": "unrelated missing\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := &gitObjectProver{repo: "synthetic", stdin: con830ProbeWriteCloser{Writer: io.Discard}, stdout: bufio.NewReader(strings.NewReader(response))}
			if object, err := p.request(want); err == nil {
				t.Fatalf("unbound batch response was accepted: object=%+v", object)
			}
		})
	}
}

func TestCoordinatorCON830ProverFailedBatchIsRaceFree(t *testing.T) {
	p := &gitObjectProver{repo: "synthetic", stdin: con830ProbeWriteCloser{Writer: con830ProbeFailedWriter{}}, stdout: bufio.NewReader(strings.NewReader(""))}
	if _, err := p.requestMany([]string{strings.Repeat("1", 40)}); err == nil {
		t.Fatal("failed batch returned no error")
	}
}

func TestCoordinatorCON830ProverHeadRootMustBelongToCommit(t *testing.T) {
	commit := []byte("tree " + strings.Repeat("1", 40) + "\n\nsynthetic\n")
	tree := []byte{}
	frames := fmt.Sprintf("%s commit %d\n%s\n%s tree 0\n\n", proverContentAddress(20, "commit", commit), len(commit), commit, proverContentAddress(20, "tree", tree))
	p := &gitObjectProver{
		repo: "synthetic", stdin: con830ProbeWriteCloser{Writer: io.Discard},
		stdout: bufio.NewReader(strings.NewReader(frames)), rootTrees: map[string]string{},
	}
	if head, root, err := p.resolveHeadWithRoot("HEAD"); err == nil {
		t.Fatalf("head commit's tree was replaced with another ref snapshot: head=%s root=%s", head, root)
	}
}

type con830ProbeWriteCloser struct{ io.Writer }

func (con830ProbeWriteCloser) Close() error { return nil }

type con830ProbeFailedWriter struct{}

func (con830ProbeFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCoordinatorCON830CorruptIntermediateTreeCannotProveAuthoritative(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	home := seedAmendmentContextHome(t)
	authorizeKnowledgeProductHome(t, s, "amendment-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	req := Q10Request{Product: "amendment-product", KnowledgeID: "CD-9999", IncludeAmendmentContext: true}
	first, err := s.QueryQ10(ctx, req)
	if err != nil || first.Authority != "authoritative" || first.Status != "missing" {
		t.Fatalf("initial complete negative: %+v, %v", first, err)
	}
	rawOID, err := runGit(ctx, home.RepoPath, "rev-parse", "HEAD:.concord/docs/decisions")
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(rawOID))
	body, err := runGit(ctx, home.RepoPath, "cat-file", "tree", oid)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(body, []byte("100644 "), []byte("100755 "), 1)
	if bytes.Equal(body, changed) {
		t.Fatal("fixture has no regular entry to corrupt")
	}
	var encoded bytes.Buffer
	zw := zlib.NewWriter(&encoded)
	if _, err := fmt.Fprintf(zw, "tree %d\x00", len(changed)); err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(changed); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(home.RepoPath, ".git", "objects", oid[:2], oid[2:])
	if err := os.Chmod(objectPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := runGit(ctx, home.RepoPath, "cat-file", "tree", oid)
	if err != nil || !bytes.Equal(observed, changed) {
		t.Fatalf("fixture does not serve the corrupted tree: %v", err)
	}
	for _, allow := range []bool{false, true} {
		req.AmendmentContextAllowDegraded = allow
		out, err := s.QueryQ10(ctx, req)
		if err == nil && (out.Authority == "authoritative" || len(out.Omissions) == 0) {
			t.Errorf("allow_degraded=%t: corrupt tree under unchanged OID proved authority: %+v", allow, out)
		}
	}
}
