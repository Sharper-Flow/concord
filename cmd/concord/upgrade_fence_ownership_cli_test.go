package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

// The promoted foreign-upgrade probe: a maintenance boundary an
// operator or another operation left behind — present on disk, maybe even
// carrying a fence id — is not this binary's upgrade boundary. The native
// migration validates the boundary's owner before any migration runs while
// exclusion is held: an unattributed record (no operation, no release
// identity) must stay byte-for-byte unchanged and must authorize no
// migration, in particular no incompatible step of the 111 floor's tail.
func TestUpgradeRefusesAnUnattributedForeignBoundary(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	foreign := []byte(`{"fence_id":"foreign-uncertain-operation"}`)
	if err := os.WriteFile(hostlease.FencePath(root), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code == 0 && strings.Contains(out, "111") {
		t.Fatalf("an incompatible migration committed under an unattributed foreign boundary: code=%d out=%s err=%s", code, out, errOut)
	}
	if code != 1 {
		t.Fatalf("the foreign boundary must refuse the upgrade: code=%d out=%s err=%s", code, out, errOut)
	}
	kept, err := os.ReadFile(hostlease.FencePath(root))
	if err != nil || string(kept) != string(foreign) {
		t.Fatalf("the refused boundary must stay unchanged, got %q (%v)", string(kept), err)
	}
	if !strings.Contains(errOut, "offline bootstrap") {
		t.Fatalf("the refusal must name the operator-owned route: %s", errOut)
	}
}

// Two migrating runs never overlap. A run that adopted another
// run's boundary could commit the breaking tail while the opener, seeing
// nothing left to apply, closes the boundary before activation. The second
// run refuses before it reads the store, opens no fence, and commits nothing.
func TestUpgradeRefusesWhileAnotherMaintenanceRuns(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	release, err := hostlease.AcquireMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runUpgradeStdin(t, `{}`)
	release()
	if code != 1 || !strings.Contains(errOut, "another maintenance command is in progress") {
		t.Fatalf("a concurrent upgrade must refuse: code=%d out=%s err=%s", code, out, errOut)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("the refused run must open no boundary: %+v %v", fence, err)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || len(plan.PendingBreaking) == 0 {
		t.Fatalf("the refused run must commit nothing: %+v %v", plan, err)
	}
	if code, _, errOut := runUpgradeStdin(t, `{"plan":true}`); code != 0 {
		t.Fatalf("the read-only plan takes no maintenance lock: %s", errOut)
	}
}
