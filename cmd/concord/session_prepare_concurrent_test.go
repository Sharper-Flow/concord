package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestConcurrentSessionPreparationsDoNotWriteIdentity(t *testing.T) {
	repo := initLocatorRepo(t)
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, repo)
	work := seedCLISessionWorktree(t, s, repo)
	t.Setenv(dbOverrideEnv, dbPath)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(work.Entry.Path)
	writeAgentDefinition(t, filepath.Join(work.Entry.Path, ".opencode", "agents"), agentDefinitionFileName("concord-1"))
	state := func() [2]int64 {
		t.Helper()
		var value [2]int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM effect_receipts), revision FROM store_revision WHERE id=1`).Scan(&value[0], &value[1]); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := state()
	input := commandSessionPrepareInput(t, work.WorkID, "run")
	host := hostCommandAt(registryResolutionFor("concord-1"))
	start := make(chan struct{})
	type outcome struct {
		code        int
		output      string
		diagnostics string
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			var out, errOut bytes.Buffer
			code := runSessionPrepare(input, s, &out, &errOut, host, hostOrchestratorIdentity, DeriveSessionBoot)
			results <- outcome{code, out.String(), errOut.String()}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.code != 0 || result.output == "" || result.diagnostics != "" {
			t.Errorf("prepare exit=%d stdout=%q stderr=%q", result.code, result.output, result.diagnostics)
		}
	}
	if after := state(); after != before {
		t.Fatalf("session preparation wrote identity state: receipts/revision %v -> %v", before, after)
	}
}
