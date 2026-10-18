package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDispatchReadingContentUsesPinnedSources(t *testing.T) {
	fixture := readingResolutionFixture(t, "work-reading-bytes", readingSources)
	defer fixture.store.Close()
	ctx := context.Background()
	witnesses, err := sampleDispatchReadingIdentities(ctx, fixture.store.db, fixture.workID, currentFixtureView(t, fixture).RequiredReading)
	if err != nil {
		t.Fatal(err)
	}
	for _, witness := range witnesses {
		t.Run(witness.arm, func(t *testing.T) {
			repo, commit, path := witness.repoPath, witness.commitOID, witness.path
			if witness.arm == WorkContextSourceKnowledge {
				repo, commit, path = witness.sourceRepo, witness.recordCommit, witness.recordPath
			}
			expected := []byte(fixtureGitRun(t, repo, "cat-file", "blob", commit+":"+path))
			changed := []byte("mutable checkout is not the pinned source\n")
			if err := os.WriteFile(filepath.Join(repo, path), changed, 0o600); err != nil {
				t.Fatal(err)
			}
			content, err := readDispatchReadingContent(ctx, CommittedReadGitRunner{}, witness)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(content, expected) || bytes.Equal(content, changed) {
				t.Fatalf("reading %d returned mutable or different source bytes", witness.ordinal)
			}
		})
	}
}

func TestReadDispatchReadingContentRefusesMissingPinnedCommit(t *testing.T) {
	fixture := readingResolutionFixture(t, "work-reading-bytes-missing", readingSources)
	defer fixture.store.Close()
	ctx := context.Background()
	witnesses, err := sampleDispatchReadingIdentities(ctx, fixture.store.db, fixture.workID, currentFixtureView(t, fixture).RequiredReading)
	if err != nil {
		t.Fatal(err)
	}
	witness := witnesses[0]
	witness.commitOID = strings.Repeat("f", 40)
	witness.identity = repositoryReadingIdentity(witness.projectID, witness.path, witness.commitOID)
	content, err := readDispatchReadingContent(ctx, CommittedReadGitRunner{}, witness)
	if err == nil || len(content) != 0 || !strings.Contains(err.Error(), "required reading 0") {
		t.Fatalf("missing pinned commit returned content %q, error %v", content, err)
	}
}

func TestReadDispatchReadingContentRefusesInvalidKnowledgeProof(t *testing.T) {
	fixture := readingResolutionFixture(t, "work-reading-bytes-invalid", readingSources)
	defer fixture.store.Close()
	ctx := context.Background()
	witnesses, err := sampleDispatchReadingIdentities(ctx, fixture.store.db, fixture.workID, currentFixtureView(t, fixture).RequiredReading)
	if err != nil {
		t.Fatal(err)
	}
	witness := witnesses[1]
	projected := *witness.projectedRecord
	projected.SHA256 = "sha256:" + strings.Repeat("0", 64)
	witness.projectedRecord = &projected
	content, err := readDispatchReadingContent(ctx, CommittedReadGitRunner{}, witness)
	if err == nil || len(content) != 0 || !strings.Contains(err.Error(), "required reading 1") {
		t.Fatalf("invalid knowledge proof returned content %q, error %v", content, err)
	}
}

type corruptReadingBlobRunner struct {
	GitRunner
	spec string
}

func (r corruptReadingBlobRunner) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	if len(args) == 3 && args[0] == "cat-file" && args[1] == "blob" && args[2] == r.spec {
		return []byte("unverified knowledge bytes"), nil
	}
	return r.GitRunner.Run(ctx, repo, args...)
}

func TestReadDispatchReadingContentDoesNotExposeUnverifiedKnowledgeBytes(t *testing.T) {
	fixture := readingResolutionFixture(t, "work-reading-bytes-corrupt", readingSources)
	defer fixture.store.Close()
	ctx := context.Background()
	witnesses, err := sampleDispatchReadingIdentities(ctx, fixture.store.db, fixture.workID, currentFixtureView(t, fixture).RequiredReading)
	if err != nil {
		t.Fatal(err)
	}
	witness := witnesses[1]
	runner := corruptReadingBlobRunner{GitRunner: CommittedReadGitRunner{}, spec: witness.recordCommit + ":" + witness.recordPath}
	content, err := readDispatchReadingContent(ctx, runner, witness)
	if err == nil || len(content) != 0 || !strings.Contains(err.Error(), "hash does not match blob bytes") {
		t.Fatalf("unverified blob returned content %q, error %v", content, err)
	}
}
