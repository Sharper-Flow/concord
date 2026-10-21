package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Boundary tests for the shared packet-bounds guard on the dispatch-time
// caller exclusions (worker_packet5_worker_job): at most
// WorkerJobCallerExclusionsMax paths of at most
// WorkerJobCallerExclusionPathMax Unicode characters each. The computation
// owner refuses the dispatch on its error, and the continuity ready
// projection omits the whole set, so the read stays available and the
// dispatch refuses with its recovery.
func TestValidateWorkerJobPacketExclusionsBounds(t *testing.T) {
	const jobID = "job:exclusion-bounds"
	t.Run("empty set passes", func(t *testing.T) {
		if err := validateWorkerJobPacketExclusions(jobID, nil); err != nil {
			t.Fatalf("empty exclusions refused: %v", err)
		}
		if err := validateWorkerJobPacketExclusions(jobID, []string{}); err != nil {
			t.Fatalf("empty exclusions refused: %v", err)
		}
	})
	t.Run("count bound edge", func(t *testing.T) {
		paths := make([]string, WorkerJobCallerExclusionsMax)
		for i := range paths {
			paths[i] = fmt.Sprintf("internal/store/path%d.go", i)
		}
		if err := validateWorkerJobPacketExclusions(jobID, paths); err != nil {
			t.Fatalf("%d paths refused: %v", len(paths), err)
		}
		paths = append(paths, "internal/store/one-too-many.go")
		err := validateWorkerJobPacketExclusions(jobID, paths)
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("%d paths error = %v, want a Failure", len(paths), err)
		}
		if failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, fmt.Sprintf("would carry %d paths", len(paths))) {
			t.Fatalf("over-count refusal = %s, want the exact count diagnostic", failure.Detail)
		}
	})
	t.Run("character bound is unicode, not bytes", func(t *testing.T) {
		runes := []rune(strings.Repeat("ä", WorkerJobCallerExclusionPathMax-3) + ".go")
		path := string(runes)
		if utf8.RuneCountInString(path) != WorkerJobCallerExclusionPathMax {
			t.Fatalf("fixture path carries %d runes, want %d", utf8.RuneCountInString(path), WorkerJobCallerExclusionPathMax)
		}
		if err := validateWorkerJobPacketExclusions(jobID, []string{path}); err != nil {
			t.Fatalf("%d-rune path refused: %v", utf8.RuneCountInString(path), err)
		}
		over := path + "x"
		if utf8.RuneCountInString(over) != WorkerJobCallerExclusionPathMax+1 {
			t.Fatalf("over-bound fixture path carries %d runes", utf8.RuneCountInString(over))
		}
		err := validateWorkerJobPacketExclusions(jobID, []string{over})
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("over-bound path error = %v, want a Failure", err)
		}
		if failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, fmt.Sprintf("above the packet's %d-character bound", WorkerJobCallerExclusionPathMax)) {
			t.Fatalf("over-bound refusal = %s, want the exact character-bound diagnostic", failure.Detail)
		}
	})
}

// observedDeltaRunner answers the terminal delivery probe without touching
// the filesystem: the tree is clean, HEAD is the delivered commit, and the
// changed-path probe answers the named delta between the dispatch subject
// and HEAD.
type observedDeltaRunner struct {
	head    string
	changed []string
}

func (r observedDeltaRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch {
	case len(args) > 0 && args[0] == "status":
		return nil, nil
	case len(args) > 0 && args[0] == "rev-parse":
		return []byte(r.head + "\n"), nil
	case slices.Contains(args, "--name-only"):
		if args[len(args)-1] != r.head {
			return nil, fmt.Errorf("changed-path probe ends at %s, want the delivered HEAD", args[len(args)-1])
		}
		return []byte(strings.Join(r.changed, "\x00") + "\x00"), nil
	}
	return nil, fmt.Errorf("unexpected git invocation: %v", args)
}

// TestWorkerJobDeliveredPathContainmentAtAcceptance holds the acceptance
// containment (CD-0227 D2/D3, CD-0242 D6) over the delta the core observed
// between the dispatched subject commit and the delivered commit: in-scope
// paths are contained; every other observed path needs exactly one evidenced
// repair under the dispatched revision's allowance, off the dispatch's caller
// exclusions and free of another work item's scope or held claim. Each
// scenario seeds its own work.
func TestWorkerJobDeliveredPathContainmentAtAcceptance(t *testing.T) {
	const delivered = "2222222222222222222222222222222222222222"
	repair := func(owner, changed string) WorkerDirectCallerRepair {
		return WorkerDirectCallerRepair{
			Owner:  WorkerCallerMechanism{Path: owner, EntryPoint: "Value"},
			Caller: WorkerCallerMechanism{Path: changed, EntryPoint: "Use"},
			Edge:   "Use invokes Value", Exposing: "go test ./... on the owner-only change",
			ChangedPaths: []string{changed},
			Checks: WorkerCallerRepairChecks{
				BaselinePass:  WorkerCallerRepairCheck{Observed: "ok"},
				OwnerOnlyFail: WorkerCallerRepairCheck{Observed: "compile error", InferredCause: "Use still passes no argument"},
				CombinedPass:  WorkerCallerRepairCheck{Observed: "ok"},
			},
		}
	}
	// recordJob records one ready oracle job scoped to owner/, with or
	// without the direct-caller allowance naming owner/value.go Value.
	recordJob := func(t *testing.T, f oracleRepairFixture, workID, jobID string, scope string, withAllowance bool) WorkerJobBinding {
		t.Helper()
		oracle := twoPathOracleFields(t, f.store, workID, nil)
		overrides := map[string]any{"path_scope": []string{scope}}
		if withAllowance {
			domain := oracle["owners"].([]map[string]any)[0]["domain_id"].(string)
			overrides["direct_caller_allowance"] = map[string]any{
				"project_id":   "project",
				"mechanisms":   []map[string]any{{"project_id": "project", "path": "owner/value.go", "entry_point": "Value"}},
				"domain_ids":   []string{domain},
				"law_bindings": []map[string]any{},
			}
		}
		return recordOracleJobRevisionWith(t, f.store, workID, f.owner, jobID, oracle, overrides)
	}
	// deliver dispatches the job's implementation attempt at the qualified
	// subject, then records its completion through the terminal preparation
	// with the named observed delta and reported repairs.
	deliver := func(t *testing.T, f oracleRepairFixture, workID string, job WorkerJobBinding, changed []string, repairs []WorkerDirectCallerRepair) string {
		t.Helper()
		attempt := "attempt:" + job.JobID
		subject := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
		completion := oracleCompletionEvent(t, f.store, workID, attempt, "delivered", func(payload *WorkerCompletedPayload) {
			payload.DirectCallerRepairs = repairs
		})
		prepared, err := PrepareWorkerTerminalDelivery(context.Background(), f.store, PrepareWorkerTerminalDeliveryRequest{
			WorkID: workID, AttemptID: attempt, EventID: completion.EventID, Runner: observedDeltaRunner{head: delivered, changed: changed},
		})
		if err != nil {
			t.Fatal(err)
		}
		workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), f.store, completion)
		var raw []byte
		if err := f.store.db.QueryRow(`SELECT json_extract(payload,'$.observed_delivery') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerCompleted, attempt).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var observed WorkerObservedDelivery
		if err := json.Unmarshal(raw, &observed); err != nil {
			t.Fatal(err)
		}
		if observed.Base != subject || observed.Commit != delivered || !slices.Equal(observed.ChangedPaths, changed) {
			t.Fatalf("observed delivery = %+v, want base %s, commit %s, changed %v", observed, subject, delivered, changed)
		}
		return attempt
	}
	seed := func(t *testing.T, workID string) oracleRepairFixture {
		t.Helper()
		f := oracleRepairFixtureFrom(t, workID, seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 34, "repair"))
		if !testWorkPinCallerCapable(t, f.store, workID) {
			t.Fatal("the oracle repair fixture pinned no caller-capable definition")
		}
		return f
	}
	refused := func(t *testing.T, f oracleRepairFixture, workID, attempt string, kind FailureKind, want string) {
		t.Helper()
		before := maxEventSeq(t, f.store, workID)
		err := acceptChangeTestAttempt(t, f, workID, attempt)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != kind || !strings.Contains(failure.Detail, want) {
			t.Fatalf("acceptance = %v, want %s %q", err, kind, want)
		}
		if after := maxEventSeq(t, f.store, workID); after != before {
			t.Fatalf("refused acceptance changed the event log: before=%d after=%d", before, after)
		}
	}

	t.Run("contained delta and evidenced repair accept", func(t *testing.T) {
		workID := "delivered-contained"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:contained", "owner", true)
		attempt := deliver(t, f, workID, job, []string{"caller/use.go", "owner/value.go"}, []WorkerDirectCallerRepair{repair("owner/value.go", "caller/use.go")})
		if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
			t.Fatalf("contained acceptance refused: %v", err)
		}
	})

	t.Run("unreported out-of-scope path refuses", func(t *testing.T) {
		workID := "delivered-out-of-scope"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:out-of-scope", "owner", true)
		attempt := deliver(t, f, workID, job, []string{"owner/value.go", "unrelated/extra.go"}, nil)
		refused(t, f, workID, attempt, KindInvalidOperation, "changed path unrelated/extra.go outside its path_scope without an evidenced direct-caller repair")
	})

	t.Run("repair path outside the observed delta refuses", func(t *testing.T) {
		workID := "delivered-missing-repair-path"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:missing-repair-path", "owner", true)
		attempt := deliver(t, f, workID, job, []string{"owner/value.go"}, []WorkerDirectCallerRepair{repair("owner/value.go", "caller/use.go")})
		refused(t, f, workID, attempt, KindInvalidOperation, "caller-repair path caller/use.go, which is not a path of its observed delta")
	})

	t.Run("wrong owner mechanism refuses", func(t *testing.T) {
		workID := "delivered-wrong-owner"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:wrong-owner", "owner", true)
		bad := repair("owner/value.go", "caller/use.go")
		bad.Owner.EntryPoint = "AnotherOwner"
		attempt := deliver(t, f, workID, job, []string{"caller/use.go", "owner/value.go"}, []WorkerDirectCallerRepair{bad})
		refused(t, f, workID, attempt, KindInvalidOperation, "which is not a mechanism of its direct-caller allowance")
	})

	t.Run("excluded path refuses", func(t *testing.T) {
		workID := "delivered-excluded"
		f := seed(t, workID)
		defer f.store.Close()
		// A sibling job dispatched and failed first: its recorded scope is
		// the correction this job's implementation dispatch excludes.
		sibling := recordJob(t, f, workID, "job:sibling", "sibling", false)
		siblingAttempt := "attempt:" + sibling.JobID
		dispatchChangeAcceptanceAttempt(t, f, workID, siblingAttempt, sibling)
		s2WorkerFailedAttributedLive(t, f.store, workID, siblingAttempt, "lane fallback blocked before the model ran")
		job := recordJob(t, f, workID, "job:excluded", "owner", true)
		attempt := deliver(t, f, workID, job, []string{"owner/value.go", "sibling/use.go"}, []WorkerDirectCallerRepair{repair("owner/value.go", "sibling/use.go")})
		refused(t, f, workID, attempt, KindInvalidOperation, "repaired caller path sibling/use.go, which its dispatch excluded as another correction's recorded path")
	})

	t.Run("foreign-owned path refuses", func(t *testing.T) {
		workID := "delivered-foreign"
		f := seed(t, workID)
		defer f.store.Close()
		// Another work item in the same Project holds a path claim over the
		// caller path this job's repair changes.
		const otherWork = "delivered-foreign-owner"
		seedWork(t, f.store, otherWork)
		version := readWorkVersion(t, f.store, otherWork)
		resulting := version + 1
		claim := resourceClaimedPayload{ResourceKey: "path:caller/use.go", Reason: "another item's held path claim", HolderAgent: "agent/other", HolderSession: "session/other"}
		claim.WorkflowVersionFields = WorkflowVersionFields{WorkID: otherWork, ExpectedVersion: &version, ResultingVersion: &resulting}
		event := Event{EventID: "claim-foreign-caller", Kind: "work.resource_claimed", SubjectType: SubjectWorkItem, SubjectID: otherWork, Actor: "worker:test", OccurredAt: time.Unix(19, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(claim)}
		if err := applyWorkflowTestOperation(context.Background(), f.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, otherWork): version}}); err != nil {
			t.Fatalf("record the foreign item's path claim: %v", err)
		}
		job := recordJob(t, f, workID, "job:foreign", "owner", true)
		attempt := deliver(t, f, workID, job, []string{"caller/use.go", "owner/value.go"}, []WorkerDirectCallerRepair{repair("owner/value.go", "caller/use.go")})
		refused(t, f, workID, attempt, KindInvalidOperation, "holds through claim path:caller/use.go over")
	})

	t.Run("repairs without an allowance refuse", func(t *testing.T) {
		workID := "delivered-no-allowance"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:no-allowance", "owner", false)
		attempt := deliver(t, f, workID, job, []string{"caller/use.go", "owner/value.go"}, []WorkerDirectCallerRepair{repair("owner/value.go", "caller/use.go")})
		refused(t, f, workID, attempt, KindInvalidOperation, "records no direct-caller allowance")
	})

	t.Run("out-of-scope path without an allowance refuses", func(t *testing.T) {
		workID := "delivered-no-allowance-extra"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:no-allowance-extra", "owner", false)
		attempt := deliver(t, f, workID, job, []string{"owner/value.go", "unrelated/extra.go"}, nil)
		refused(t, f, workID, attempt, KindInvalidOperation, "changed path unrelated/extra.go outside its path_scope")
	})

	t.Run("historical measured delta stays contained", func(t *testing.T) {
		workID := "delivered-historical-measured"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:historical-measured", "owner", true)
		attempt := deliver(t, f, workID, job, []string{"owner/value.go"}, nil)
		// A completion stored before the changed-path observation carries
		// its delta as the retired measured paths instead.
		tx, err := f.store.DatabaseForTesting().Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var trigger string
		if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='domain_events_no_update'`).Scan(&trigger); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DROP TRIGGER domain_events_no_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE domain_events SET payload=json_set(json_remove(payload,'$.observed_delivery.changed_paths'),'$.observed_delivery.paths',json('[{"path":"owner/value.go","added":1,"deleted":0},{"path":"unrelated/extra.go","added":1,"deleted":0}]')) WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerCompleted, attempt); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(trigger); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		refused(t, f, workID, attempt, KindInvalidOperation, "changed path unrelated/extra.go outside its path_scope without an evidenced direct-caller repair")
	})

	t.Run("allowance without an observed delta refuses", func(t *testing.T) {
		workID := "delivered-unobserved"
		f := seed(t, workID)
		defer f.store.Close()
		job := recordJob(t, f, workID, "job:unobserved", "owner", true)
		attempt := "attempt:" + job.JobID
		dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
		if err := completeJobBoundAttemptForTest(f.store, workID, attempt, reviewGateLane(t, "implementation"), &job); err != nil {
			t.Fatal(err)
		}
		refused(t, f, workID, attempt, KindMissingEvidence, "carries no clean delta observed from the dispatched subject commit")
	})
}
