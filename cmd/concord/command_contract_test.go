package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The committed contract is what the adapter's payload tests read. If it may
// drift from commandSpecs, it proves nothing: an adapter payload could satisfy
// a stale contract and still be refused by the CLI. This test is the coupling.
func TestCommandContractMatchesCommandSpecs(t *testing.T) {
	want, err := commandContractBytes()
	if err != nil {
		t.Fatalf("render command contract: %v", err)
	}
	path := filepath.Join("..", "..", commandContractPath)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", commandContractPath, err)
	}
	if string(got) != string(want) {
		if os.Getenv("CONCORD_UPDATE_COMMAND_CONTRACT") == "1" {
			if writeErr := os.WriteFile(path, want, 0o644); writeErr != nil {
				t.Fatalf("update %s: %v", commandContractPath, writeErr)
			}
			t.Fatalf("%s was stale and has been rewritten; commit it", commandContractPath)
		}
		t.Fatalf("%s is stale; re-run with CONCORD_UPDATE_COMMAND_CONTRACT=1 and commit the result", commandContractPath)
	}
}

// worker-dispatch is the write whose omitted field refused every completion
// after the worker had already run. Naming it here states the requirement the
// adapter must satisfy, independent of the projection's own correctness.
func TestCommandContractBindsWorkerDispatchPacketDigest(t *testing.T) {
	for _, command := range commandContractProjection().Commands {
		if command.Canonical != "worker-dispatch" {
			continue
		}
		for _, field := range command.RequiredFields {
			if field.Name == "packet_digest" {
				return
			}
		}
		t.Fatal("worker-dispatch must require packet_digest")
	}
	t.Fatal("worker-dispatch is absent from the command contract")
}
