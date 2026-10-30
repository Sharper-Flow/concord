package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func declareSampleWorkContext(t *testing.T, f workContextFixture, statement string) {
	t.Helper()
	if err := f.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{f.repoReading(workContextTestRoot, workContextRootRationale, ""), f.knowledgeReading()},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "rejected_approach", statement, "")},
	}); err != nil {
		t.Fatalf("record_work_context refused: %v", err)
	}
}

func TestWorkPinCarriesCurrentWorkContextView(t *testing.T) {
	t.Parallel()
	f := seedWorkContextFixture(t, "ctx-pin-view")
	defer f.store.Close()
	declareSampleWorkContext(t, f, "the unbounded log scan was rejected")
	view, err := readWorkContextView(t.Context(), f.store.db, f.workID)
	if err != nil || view == nil {
		t.Fatalf("current work context = %+v err=%v", view, err)
	}
	pin, err := ReadWorkPin(t.Context(), f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	viewJSON, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, viewJSON) {
		t.Fatalf("work pin omits exact current view:\n%s\nwant:\n%s", raw, viewJSON)
	}
	if err := payloadschema.Validate("work_pin", raw); err != nil {
		t.Fatalf("work pin with work context fails closed schema: %v", err)
	}
}

func TestWorkPinOmitsWorkContextWithoutCurrentView(t *testing.T) {
	t.Parallel()
	f := seedWorkContextFixture(t, "ctx-pin-absent")
	defer f.store.Close()
	pin, err := ReadWorkPin(t.Context(), f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"work_context"`)) {
		t.Fatalf("work pin carries work context without a current view:\n%s", raw)
	}
	if err := payloadschema.Validate("work_pin", raw); err != nil {
		t.Fatalf("work pin without work context fails closed schema: %v", err)
	}
}

func TestWorkPinRefusesWorkContextViewOverflow(t *testing.T) {
	f := seedWorkContextFixture(t, "ctx-pin-overflow")
	defer f.store.Close()
	seedWorkContextOverflow(t, f)
	if view, err := readWorkContextView(t.Context(), f.store.db, f.workID); !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("overflow fixture view = %+v, %v, want limit_exceeded", view, err)
	}
	before := outsideRepairDatabaseSnapshot(t, f.store)
	pin, err := ReadWorkPin(t.Context(), f.store, f.workID)
	if err != nil || pin.WorkContext != nil || pin.WorkContextUnavailable == nil || pin.WorkContextUnavailable.Reason != string(KindLimitExceeded) {
		t.Fatalf("overflow work pin = %+v, %v, want unavailable context without truncation", pin, err)
	}
	if err := payloadschema.Validate("work_pin", mustJSONValue(pin)); err != nil {
		t.Fatalf("overflow work pin fails its closed schema: %v", err)
	}
	if outsideRepairDatabaseSnapshot(t, f.store) != before {
		t.Fatal("refused overflow read changed durable state")
	}
}

func seedWorkContextOverflow(t *testing.T, f workContextFixture) {
	t.Helper()
	findings := func(statement string) []map[string]any {
		out := make([]map[string]any, 16)
		for i := range out {
			out[i] = workContextFinding(workContextTestChild, "observation", statement, "")
		}
		return out
	}
	if err := f.recordWorkContext(t, map[string]any{"context_findings": findings("first")}); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := f.store.db.QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, f.workID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 16)
	for i := range refs {
		refs[i] = fmt.Sprintf("finding:%d:%d", seq, i)
	}
	if err := f.recordWorkContext(t, map[string]any{"finding_refs": refs, "context_findings": findings("second")}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(`SELECT declaration_seq FROM work_context_state WHERE work_id=?`, f.workID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		refs = append(refs, fmt.Sprintf("finding:%d:%d", seq, i))
	}
	// Historical over-bound input must still refuse on the live reader.
	recordHistoricalWorkContext(t, f, map[string]any{"finding_refs": refs, "context_findings": findings("third")})
}

func viewSubject(view *WorkContextView) string {
	if view == nil {
		return ""
	}
	return view.SubjectCommit
}

func TestWorkContextSubjectFreshnessCoreOwned(t *testing.T) {
	for _, shape := range []string{"moved", "dirty", "unreachable"} {
		t.Run(shape, func(t *testing.T) {
			f := seedWorkContextFixture(t, "ctx-freshness-"+shape)
			defer f.store.Close()
			repo, head := reviewSubjectClaim(t, f.store, f.workID)
			view, err := readWorkContextView(t.Context(), f.store.db, f.workID)
			if err != nil || view == nil || view.SubjectCommit != head {
				t.Fatalf("view subject = %q err=%v, want %s", viewSubject(view), err, head)
			}
			want := KindStaleRequiresReview
			if shape == "unreachable" {
				if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
					t.Fatal(err)
				}
				want = KindGitUnreachable
			} else {
				if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("changed candidate\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if shape == "moved" {
					gitRunStore(t, repo, "add", "-A")
					gitRunStore(t, repo, "commit", "-m", "move past qualified subject")
				}
			}
			before := outsideRepairDatabaseSnapshot(t, f.store)
			if _, err := PrepareWorkflowAcceptanceFreshness(t.Context(), f.store, PrepareWorkflowSubjectRequest{WorkID: f.workID}); !hasFailureKind(err, want) {
				t.Fatalf("%s subject preparation = %v, want %s", shape, err, want)
			}
			if outsideRepairDatabaseSnapshot(t, f.store) != before {
				t.Fatal("refused subject preparation changed durable state")
			}
		})
	}
}

type probeCountingRunner struct{ count *int }

func (r probeCountingRunner) Run(context.Context, string, ...string) ([]byte, error) {
	*r.count++
	return nil, nil
}

func TestWorkContextSubjectLessPreparationProbesNothing(t *testing.T) {
	f := seedWorkContextFixture(t, "ctx-subjectless-preparation")
	defer f.store.Close()
	seen := 0
	before := outsideRepairDatabaseSnapshot(t, f.store)
	prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), f.store, PrepareWorkflowSubjectRequest{WorkID: f.workID, Runner: probeCountingRunner{count: &seen}})
	if err != nil || prepared.candidate != nil || prepared.receipt != nil || seen != 0 {
		t.Fatalf("subjectless preparation = %+v err=%v probes=%d, want no fabricated subject", prepared, err, seen)
	}
	if outsideRepairDatabaseSnapshot(t, f.store) != before {
		t.Fatal("subjectless preparation changed durable state")
	}
}

type contextSubjectHoldingRunner struct {
	entered chan context.Context
	release chan struct{}
	once    sync.Once
}

func (r *contextSubjectHoldingRunner) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	r.once.Do(func() { r.entered <- ctx })
	select {
	case <-r.release:
		return (ExecGitRunner{}).Run(ctx, repo, args...)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestWorkContextSubjectPreparationBoundaries(t *testing.T) {
	t.Run("probe holds no SQL connection", func(t *testing.T) {
		f := seedWorkContextFixture(t, "ctx-probe-outside-sql")
		defer f.store.Close()
		reviewSubjectClaim(t, f.store, f.workID)
		runner := &contextSubjectHoldingRunner{entered: make(chan context.Context, 1), release: make(chan struct{})}
		var release sync.Once
		defer release.Do(func() { close(runner.release) })
		result := make(chan error, 1)
		before := outsideRepairDatabaseSnapshot(t, f.store)
		go func() {
			_, err := PrepareWorkflowAcceptanceFreshness(t.Context(), f.store, PrepareWorkflowSubjectRequest{WorkID: f.workID, Runner: runner})
			result <- err
		}()
		select {
		case <-runner.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("preparation never entered the Git probe")
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		var version int64
		if err := f.store.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, f.workID).Scan(&version); err != nil {
			t.Fatalf("independent SQL read blocked while Git probe held: %v", err)
		}
		select {
		case err := <-result:
			t.Fatalf("preparation finished before probe release: %v", err)
		default:
		}
		release.Do(func() { close(runner.release) })
		if err := <-result; err != nil {
			t.Fatalf("preparation after release: %v", err)
		}
		if outsideRepairDatabaseSnapshot(t, f.store) != before {
			t.Fatal("subject preparation changed durable state")
		}
	})
	t.Run("fixed deadline has zero effects", func(t *testing.T) {
		f := seedWorkContextFixture(t, "ctx-probe-deadline")
		defer f.store.Close()
		reviewSubjectClaim(t, f.store, f.workID)
		runner := &contextSubjectHoldingRunner{entered: make(chan context.Context, 1), release: make(chan struct{})}
		before := outsideRepairDatabaseSnapshot(t, f.store)
		_, err := PrepareWorkflowAcceptanceFreshness(t.Context(), f.store, PrepareWorkflowSubjectRequest{WorkID: f.workID, Runner: runner})
		if !hasFailureKind(err, KindGitUnreachable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline preparation = %v, want wrapped probe deadline", err)
		}
		probe := <-runner.entered
		if _, bounded := probe.Deadline(); !bounded || probe.Err() != context.DeadlineExceeded {
			t.Fatal("Git probe did not receive its bounded deadline")
		}
		if outsideRepairDatabaseSnapshot(t, f.store) != before {
			t.Fatal("deadline refusal changed durable state")
		}
	})
	t.Run("identity drift has zero effects", func(t *testing.T) {
		f := seedWorkContextFixture(t, "ctx-probe-identity-drift")
		defer f.store.Close()
		reviewSubjectClaim(t, f.store, f.workID)
		report := currentReviewBlockForTest(t, f.store, f.workID, "", "continue")
		prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), f.store, PrepareWorkflowSubjectRequest{WorkID: f.workID})
		if err != nil {
			t.Fatal(err)
		}
		declareSampleWorkContext(t, f, "context changed after subject observation")
		j := &candidateJourney{fixture: f, s: f.store, workID: f.workID}
		ctx := WithWorkflowSubjectPreparation(t.Context(), prepared)
		before := outsideRepairDatabaseSnapshot(t, f.store)
		if err := j.terminalActionContext(t, ctx, "record_review", mustJSONValue(map[string]any{"review": report}), f.owner, nil); !hasFailureKind(err, KindProjectionConflict) {
			t.Fatalf("changed subject observation = %v, want projection_conflict", err)
		}
		if outsideRepairDatabaseSnapshot(t, f.store) != before {
			t.Fatal("identity drift refusal changed durable state")
		}
	})
}
