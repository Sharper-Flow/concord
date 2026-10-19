package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A retained overrun commit whose Git object is missing from the successor
// repository refuses the successor dispatch safely: the ancestry probe's
// failure is an outage, never proven non-ancestry (only exit 1 proves that),
// and it leaves the work unchanged. Once the object is restored for real —
// fetched back from a second repository that retained it — the
// original-baseline successor is admitted.
func TestWorkerJobSplitSuccessorMissingOverrunObject(t *testing.T) {
	f, dir, job, overrun := splitBaselineFixture(t, "split-baseline-missing-object", "original")
	// A second repository retains the overrun object across the outage.
	keeper := filepath.Join(t.TempDir(), "overrun-keeper.git")
	gitRunStore(t, filepath.Dir(keeper), "init", "-q", "--bare", keeper)
	gitRunStore(t, dir, "update-ref", "refs/keeper/overrun", overrun)
	gitRunStore(t, keeper, "fetch", dir, "refs/keeper/overrun:refs/keeper/overrun")
	// The successor repository loses the object for real: the foundation
	// reset left the overrun unreachable, so an expiring expire plus prune
	// removes it.
	gitRunStore(t, dir, "update-ref", "-d", "refs/keeper/overrun")
	gitRunStore(t, dir, "reflog", "expire", "--expire=now", "--all")
	gitRunStore(t, dir, "reflog", "expire", "--expire=now", "HEAD")
	gitRunStore(t, dir, "gc", "--prune=now")
	git := ExecGitRunner{}
	if _, err := git.Run(context.Background(), dir, "cat-file", "-e", overrun); err == nil {
		t.Fatal("the overrun object survived the prune; the fixture does not exercise the outage")
	}
	before := readWorkVersion(t, f.store, f.workID)
	err := sizeJourneyDispatch(t, f, dir, "attempt:successor", job, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable || !strings.Contains(failure.Detail, "split overrun ancestry") {
		t.Fatalf("the missing overrun object = %v; want the %s split-overrun-ancestry refusal", err, KindGitUnreachable)
	}
	if !strings.Contains(failure.RecoveryAction, "restore") {
		t.Fatalf("the outage recovery %q does not name restoring the referenced objects", failure.RecoveryAction)
	}
	if after := readWorkVersion(t, f.store, f.workID); after != before {
		t.Fatalf("the outage refusal changed work version %d to %d", before, after)
	}
	// The real restoration: fetch the retained object back, then the
	// original-baseline successor is admitted.
	gitRunStore(t, dir, "fetch", keeper, "refs/keeper/overrun")
	if _, err := git.Run(context.Background(), dir, "cat-file", "-e", overrun); err != nil {
		t.Fatalf("the fetch did not restore the overrun object: %v", err)
	}
	if err := sizeJourneyDispatch(t, f, dir, "attempt:successor", job, nil); err != nil {
		t.Fatalf("the original-baseline successor after the object is restored: %v", err)
	}
}
