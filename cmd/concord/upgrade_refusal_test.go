package main

import (
	"errors"
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
