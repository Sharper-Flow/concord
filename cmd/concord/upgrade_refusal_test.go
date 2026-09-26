package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
)

// A breaking-migration outage must name the terminals that hold the older
// releases, with their directories, so the operator ends the right sessions
// without correlating pids by hand (CD-0111 D3's refusal stays; only the
// hunt is removed).
func TestActionableUpgradeRefusalNamesOlderHoldingSessions(t *testing.T) {
	refusal := &store.Failure{Kind: store.KindUpgradeRequired, Op: "open",
		Detail: "the database stops before breaking migration 93 (example); this binary defines schema version 94 and never applies a breaking migration at open"}
	older := hostlease.Lease{PID: 164135, ReleaseRoot: "/releases/v10.0.5", SchemaVersion: 92, Directory: "/workspace/card-site"}
	olderNoLocation := hostlease.Lease{PID: 81206, ReleaseRoot: "/releases/v9.0.4", SchemaVersion: 90}
	current := hostlease.Lease{PID: 822725, ReleaseRoot: "/releases/v11.0.0", SchemaVersion: 93, Directory: "/workspace/toolbox"}

	err := actionableUpgradeRefusal(refusal, []hostlease.Lease{current, older, olderNoLocation}, 93)
	message := err.Error()
	for _, want := range []string{
		"pid 164135 holds /releases/v10.0.5 at schema version 92, directory /workspace/card-site",
		"pid 81206 holds /releases/v9.0.4 at schema version 90",
		"end or move those sessions to the installed release, then run concord upgrade",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal %q must name %q", message, want)
		}
	}
	if strings.Contains(message, "pid 822725") {
		t.Fatalf("refusal %q must not name a session on the current schema", message)
	}
	if !errors.Is(err, refusal) && err.Error() != refusal.Error() && !strings.Contains(message, refusal.Error()) {
		t.Fatalf("enriched refusal %q lost the original message %q", message, refusal.Error())
	}
}

// Every other failure passes through unchanged, and an upgrade-required
// refusal with no older holder observable keeps its original text.
func TestActionableUpgradeRefusalPassesOtherFailuresThrough(t *testing.T) {
	other := errors.New("store: cannot open the database")
	if got := actionableUpgradeRefusal(other, nil, 93); got != other {
		t.Fatalf("a non-upgrade failure was rewritten: %v", got)
	}
	refusal := &store.Failure{Kind: store.KindUpgradeRequired, Op: "open", Detail: "stops before breaking migration 93"}
	sameSchema := hostlease.Lease{PID: 1, ReleaseRoot: "/releases/v11.0.0", SchemaVersion: 93}
	if got := actionableUpgradeRefusal(refusal, []hostlease.Lease{sameSchema}, 93); got.Error() != refusal.Error() {
		t.Fatalf("an upgrade refusal without older holders was rewritten: %v", got)
	}
}

// An operator diagnostic names every blocking session, however many the
// refusal lists: the operator ends sessions from this text alone.
func TestOperatorDiagnosticPrintsEveryBlockingSession(t *testing.T) {
	var holders []string
	for i := 0; i < 14; i++ {
		holders = append(holders, fmt.Sprintf(
			"pid %d holds /data/concord/v11.27.4 at schema version 103, before migration 105 (worktree_occupancy_widen_worktree_id_bound), directory /data/concord/worktrees/example-project/work-%024d",
			3000000+i, i))
	}
	refusal := fmt.Sprintf("store: upgrade: upgrade_blocked: a pending breaking migration waits for %d live session(s) that predate it: %s",
		len(holders), strings.Join(holders, "; "))

	var errOut bytes.Buffer
	writeOperatorDiagnostic(&errOut, "upgrade", refusal)

	got := errOut.String()
	for i := range holders {
		if !strings.Contains(got, holders[i]) {
			t.Fatalf("diagnostic of %d bytes lost blocking session %d: %q", len(got), i, holders[i])
		}
	}
}
