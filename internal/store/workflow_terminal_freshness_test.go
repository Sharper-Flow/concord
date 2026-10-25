package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func prepareTerminalTestContext(ctx context.Context, s *Store, workID, action string) (context.Context, error) {
	if !WorkflowActionRequiresSubjectFreshness(action) {
		return ctx, nil
	}
	prepared, err := PrepareWorkflowAcceptanceFreshness(ctx, s, PrepareWorkerDispatchRequest{WorkID: workID})
	if err != nil {
		return ctx, err
	}
	return WithWorkerDispatchPreparation(ctx, prepared), nil
}

func TestManagedCandidateTerminalFreshness(t *testing.T) {
	for _, action := range []string{"record_verdict", "confirm_premise", "complete"} {
		for _, shape := range []string{"dirty", "unobserved_head"} {
			t.Run(action+"/"+shape, func(t *testing.T) {
				j := journeyThroughA(t, "terminal-"+action+"-"+shape, true)
				if action == "complete" {
					if err := j.confirmPremise(t); err != nil {
						t.Fatalf("advance clean A to complete: %v", err)
					}
				}
				payload := criterionAssociationPayload(j.runA.OperationRef, "predicate:work-context")
				if action == "confirm_premise" {
					payload = json.RawMessage(`{"contract_version":1}`)
				} else if action == "complete" {
					payload = json.RawMessage(`{"impact_verdict":"non-breaking"}`)
				}
				if shape == "dirty" {
					if err := os.WriteFile(filepath.Join(j.repo, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					j.commitB(t)
				}
				before := j.effects(t)
				if err := j.terminalAction(t, action, payload, j.fixture.owner); failureKind(err) != KindStaleRequiresReview {
					t.Errorf("%s with %s = %v, want stale_requires_review", action, shape, err)
				}
				if after := j.effects(t); after != before {
					t.Errorf("refused terminal action changed durable state:\nbefore=%s\nafter=%s", before, after)
				}
				j.requireRecordsUnchanged(t, j.snapshotA)
			})
		}
	}
}

type terminalSQLProbe struct {
	store *Store
	calls int
	err   error
}

func (p *terminalSQLProbe) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	p.calls++
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var count int
	if err := p.store.db.QueryRowContext(probeCtx, `SELECT count(*) FROM work_items`).Scan(&count); err != nil {
		return nil, err
	}
	if p.err != nil {
		return nil, p.err
	}
	return (ExecGitRunner{}).Run(ctx, path, args...)
}

func TestWorkflowTerminalPreparationOutsideSQL(t *testing.T) {
	j, head := descendantReviewFixture(t, "terminal-probe-outside-sql")
	for _, unreachable := range []bool{false, true} {
		probe := &terminalSQLProbe{store: j.s}
		if unreachable {
			probe.err = errors.New("synthetic Git outage")
		}
		before := j.effects(t)
		prepared, err := PrepareWorkflowAcceptanceFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: t.TempDir(), Runner: probe})
		if unreachable {
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable || !failure.RetrySafe {
				t.Fatalf("unreachable terminal probe = %v, want retryable git_unreachable", err)
			}
		} else if err != nil || prepared.proof.observation == nil || prepared.proof.observation.Commit != head || prepared.proof.observation.State != "clean" {
			t.Fatalf("terminal preparation = %+v err=%v, want clean observed HEAD", prepared, err)
		}
		if probe.calls == 0 {
			t.Fatal("terminal preparation did not probe Git")
		}
		if after := j.effects(t); after != before {
			t.Fatal("terminal preparation changed durable state")
		}
	}
}

func TestWorkflowTerminalPreparedPinDriftRefuses(t *testing.T) {
	j, _ := descendantReviewFixture(t, "terminal-prepared-pin-drift")
	prepared, err := PrepareWorkflowAcceptanceFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
	if err != nil {
		t.Fatal(err)
	}
	descendantCommit(t, j, "b")
	ctx := WithWorkerDispatchPreparation(context.Background(), prepared)
	before := j.effects(t)
	err = j.s.Transact(ctx, func(transaction *Transaction) error {
		return validateWorkflowAcceptanceFreshness(ctx, transaction.tx, j.workID)
	})
	if failureKind(err) != KindStaleRequiresReview {
		t.Fatalf("terminal prepared-pin drift = %v, want stale_requires_review", err)
	}
	if after := j.effects(t); after != before {
		t.Fatal("prepared-pin refusal changed durable state")
	}
}

// Post-observation movement at a terminal action is bound at delivery, not here.
func TestManagedCandidateTerminalPostObservationMovementKeepsObservedSubject(t *testing.T) {
	j := journeyThroughA(t, "terminal-unobserved-after-preparation", true)
	if err := j.action(t, "record_verdict", criterionAssociationPayload(j.runA.OperationRef, "predicate:work-context"), j.fixture.owner); err != nil {
		t.Fatalf("record the immutable A run in the completion verdict: %v", err)
	}
	if err := j.confirmPremise(t); err != nil {
		t.Fatalf("confirm clean A: %v", err)
	}
	bindCompletionClosure(t, j.s, j.workID, j.fixture.owner)
	prepared, err := PrepareWorkflowAcceptanceFreshness(context.Background(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID})
	if err != nil {
		t.Fatal(err)
	}
	j.commitB(t)
	if j.pinned(t) != j.a || j.head(t) != j.b {
		t.Fatal("fixture must preserve pin A while live HEAD moves to B")
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), prepared)
	err = j.terminalActionContext(t, ctx, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), j.fixture.owner, nil)
	if err != nil {
		t.Fatalf("complete over sealed A observation: %v", err)
	}
	if prepared.proof.observation.Commit != j.a || j.pinned(t) != j.a {
		t.Fatal("completion replaced the observed A subject with unobserved B")
	}
	var state string
	if err := j.s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, j.workID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("completion state = %q err=%v, want completed", state, err)
	}
	verdicts, err := latestWorkflowVerdicts(context.Background(), j.s.db, j.workID, 1)
	if err != nil || len(verdicts) != 1 || len(verdicts[0].EvaluationEvidence) != 1 || verdicts[0].EvaluationEvidence[0] != j.runA.OperationRef {
		t.Fatalf("completion verdict evidence = %+v err=%v, want the retained A run", verdicts, err)
	}
	receipt, err := readOracleVerifySubjectReceipt(context.Background(), j.s.db, j.workID, verdicts[0].EvaluationEvidence[0])
	if err != nil || receipt == nil || receipt.SubjectRef != "commit:"+j.a || receipt.SubjectRef == "commit:"+j.b {
		t.Fatalf("completion's retained evidence = %+v err=%v, want immutable A, never B", receipt, err)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}
