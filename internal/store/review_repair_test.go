package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func reviewObservedTree(t *testing.T, repo, commit string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, err := (ExecGitRunner{}).Run(ctx, repo, "rev-parse", commit+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func TestReviewRepairUnchangedTerminalRecordsAnEmptyDelta(t *testing.T) {
	f := newNativeOracleFixture(t)
	o := f.authorizeLane(t, "review", f.actor, "review")
	prepared, err := PrepareWorkerTerminalDelivery(context.Background(), f.s, PrepareWorkerTerminalDeliveryRequest{WorkID: f.work, AttemptID: o.AttemptID, EventID: "review-unchanged"})
	if err != nil {
		t.Fatal(err)
	}
	output := prepared.proof.observation
	if output == nil || output.Commit != f.head || output.Base != f.head || output.State != "clean" || output.ChangedPaths == nil || len(output.ChangedPaths) != 0 {
		t.Fatalf("unchanged Review must record an observed empty delta: %+v", output)
	}
	tree := reviewObservedTree(t, f.repo, f.head)
	if output.Tree != tree || output.BaseTree != tree {
		t.Fatalf("unchanged Review must bind its observed tree %s: %+v", tree, output)
	}
}

func TestReviewDispositionRequiresCorrectiveEditCapability(t *testing.T) {
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == "review" {
			if !slices.Contains(lane.Capabilities, "edit_scoped_files") {
				t.Fatal("Review cannot repair its candidate: edit_scoped_files is absent")
			}
			return
		}
	}
	t.Fatal("Review lane is absent")
}

func TestReviewRepairContainmentDoesNotGrantAncestorPaths(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"pkg/value.go", true}, {"pkg/value.go/child", true},
		{"pkg", false}, {"pkg/other.go", false},
	} {
		if got := workerJobContainsPath([]string{"pkg/value.go"}, tc.path); got != tc.want {
			t.Errorf("contained(%q)=%t, want %t", tc.path, got, tc.want)
		}
	}
}

func TestReviewDispositionRepairMustNotDemandEvaluator(t *testing.T) {
	f := newNativeOracleFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "pkg/value.go"), []byte("package oracle\nfunc Value() int { return 8 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "broken-baseline")
	o := f.authorizeLane(t, "review", f.actor, "review")
	baseline, err := f.s.VerifyWorktree(context.Background(), f.request("review-baseline", &o))
	if err != nil || baseline.Oracle == nil || baseline.Oracle.Qualification != "fail" || baseline.Oracle.SubjectCommit != f.head {
		t.Fatalf("B did not expose the defect: %+v %v", baseline.Oracle, err)
	}
	// Change production bytes, not the pinned harness or the recorded job.
	if err := os.WriteFile(filepath.Join(f.repo, "pkg/value.go"), []byte("package oracle\nfunc Value() int { return 3 + 4 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "add", "pkg/value.go")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "repair value")
	head, err := (ExecGitRunner{}).Run(context.Background(), f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	candidate := strings.TrimSpace(string(head))
	r, err := f.s.VerifyWorktree(context.Background(), f.request("review-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.Oracle.SubjectCommit != candidate || r.SubjectRef != "commit:"+candidate {
		t.Fatalf("same-pass verification of repaired C: %+v err=%v", r.Oracle, err)
	}
	if f.launches != 2 || r.Oracle.InputManifestDigest == "" || r.Oracle.InputManifestDigest == baseline.Oracle.InputManifestDigest || r.Oracle.BinaryDigest == "" {
		t.Fatalf("C was not actually built and executed: %+v launches=%d", r.Oracle, f.launches)
	}
	if f.prepare.Oracle.SubjectCommit != f.head || f.head == candidate {
		t.Fatal("baseline preparation was relabelled as C")
	}
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "pass"); err != nil {
		t.Fatalf("producer cannot resolve its own C receipt: %v", err)
	}
	retainedB, err := readOracleControlProducerTx(context.Background(), f.s.db, f.work, baseline.OperationRef, "fail")
	if err != nil || retainedB == nil || retainedB.Oracle.SubjectCommit != f.head || retainedB.Oracle.Qualification != "fail" {
		t.Fatalf("B's failing receipt changed during repair: %+v %v", retainedB, err)
	}
	// No packet or worker claim supplies the producer's subject.
	exit := 0
	source := f.bundle.Control.RecipeSource
	receipt := &WorkerOracleReceipt{ControlIDs: []string{o.ControlID}, CaseIDs: f.bundle.Control.CaseIDs, RecipeSource: WorkerOracleRecipeSource{Kind: source.Kind, ProjectID: source.ProjectID, Path: source.Path, CommitOID: source.CommitOID}, Result: "pass", ExitCode: &exit, RunRef: r.OperationRef, EvidenceRefs: []string{r.OperationRef}}
	evidence := []WorkerReportEvidence{{Obligation: "verification_commands", Detail: "native control passed", OracleReceipt: receipt}}
	if err := bindWorkerReceiptSubjectsTx(context.Background(), f.s.db, f.work, evidence); err != nil || receipt.SubjectCommit != candidate {
		t.Fatalf("producer C lost at receipt ingress: %+v err=%v", receipt, err)
	}
	receipt.SubjectCommit = f.head
	if err := bindWorkerReceiptSubjectsTx(context.Background(), f.s.db, f.work, evidence); err == nil {
		t.Fatal("a packet-bound B subject overwrote the native C receipt")
	}
}

func commitReviewRepair(t *testing.T, f *nativeOracleFixture, path string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.repo, path), []byte("package oracle\nfunc Value() int { return 7 } // scoped repair\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "add", path)
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "scoped repair")
	head, err := (ExecGitRunner{}).Run(context.Background(), f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(head))
}

func TestReviewRepairTerminalObservationAndContainment(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		readOnly   bool
		refuse     bool
	}{
		{"contained", "pkg/value.go", false, false},
		{"outside", "outside.go", false, true},
		{"caller-claim", "pkg/value.go", false, true},
		{"read-only", "pkg/value.go", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeOracleFixture(t)
			o := f.authorizeLane(t, "review", f.actor, "review", tc.readOnly)
			nativeFixtureDispatch(t, f, o)
			c := commitReviewRepair(t, f, tc.path)
			inputTree, outputTree := reviewObservedTree(t, f.repo, f.head), reviewObservedTree(t, f.repo, c)
			event := oracleCompletionEvent(t, f.s, f.work, o.AttemptID, "repair-completed", func(p *WorkerCompletedPayload) {
				dependencies, err := workflowReviewDependencies(t.Context(), f.s.db, f.work)
				if err != nil {
					t.Fatal(err)
				}
				p.Review = &WorkerReviewBlock{Disposition: "ready_for_final_proof",
					InputCandidate:  WorkerReviewCandidate{ProjectID: f.bundle.Owner.Mechanism.ProjectID, Commit: f.head, Tree: inputTree},
					OutputCandidate: WorkerReviewCandidate{ProjectID: f.bundle.Owner.Mechanism.ProjectID, Commit: c, Tree: outputTree}, Dependencies: dependencies,
					Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
				if tc.name == "caller-claim" {
					p.DirectCallerRepairs = []WorkerDirectCallerRepair{{
						Owner: WorkerCallerMechanism{Path: "pkg/value.go", EntryPoint: "Value"}, Caller: WorkerCallerMechanism{Path: "pkg/value.go", EntryPoint: "Use"},
						Edge: "Use invokes Value", Exposing: "go test", ChangedPaths: []string{"pkg/value.go"},
						Checks: WorkerCallerRepairChecks{BaselinePass: WorkerCallerRepairCheck{Observed: "ok"}, OwnerOnlyFail: WorkerCallerRepairCheck{Observed: "compile error", InferredCause: "signature changed"}, CombinedPass: WorkerCallerRepairCheck{Observed: "ok"}},
					}}
				}
			})
			prepared, err := PrepareWorkerTerminalDelivery(context.Background(), f.s, PrepareWorkerTerminalDeliveryRequest{WorkID: f.work, AttemptID: o.AttemptID, EventID: event.EventID})
			if tc.readOnly {
				if err == nil {
					t.Fatal("read-only Review edited its input")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithWorkerTerminalPreparation(context.Background(), prepared)
			workerTerminalEventThroughSeamCtx(t, ctx, f.s, event)
			tx, err := f.s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, output, err := readWorkerCompletedDeliveryTx(ctx, tx, f.work, o.AttemptID)
			if err != nil || output == nil || output.Commit != c || output.Base != f.head || output.State != "clean" || !slices.Equal(output.ChangedPaths, []string{tc.path}) {
				t.Fatalf("terminal C=%+v err=%v", output, err)
			}
			if output.Tree != outputTree || output.BaseTree != inputTree {
				t.Fatalf("terminal must retain producer-observed input/output trees: %+v", output)
			}
			lane, err := workerAttemptLane(ctx, tx, o.AttemptID)
			if err != nil || lane.CapabilityClass != "review" {
				t.Fatalf("output observation relabelled Review: %+v %v", lane, err)
			}
			entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), f.work)
			if err != nil {
				t.Fatal(err)
			}
			err = admitWorkerDeliveredPathsTx(ctx, tx, f.work, entry.Definition, o.WorkerJobBinding, o.AttemptID)
			if (err != nil) != tc.refuse {
				t.Fatalf("scope admission = %v, refuse=%t", err, tc.refuse)
			}
		})
	}
}

func TestReviewRepairNativeScopeAndIdentityNegatives(t *testing.T) {
	for _, tc := range []string{"outside", "read-only", "dirty", "unrelated", "revoked", "foreign-preparation", "foreign-control", "wrong-recipe"} {
		t.Run(tc, func(t *testing.T) {
			f := newNativeOracleFixture(t)
			o := f.authorizeLane(t, "review", f.actor, "review", tc == "read-only")
			path := "pkg/value.go"
			if tc == "outside" {
				path = "outside.go"
			}
			commitReviewRepair(t, f, path)
			switch tc {
			case "dirty":
				if err := os.WriteFile(filepath.Join(f.repo, "untracked"), []byte("dirty\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unrelated":
				gitRunStore(t, f.repo, "checkout", "--orphan", "unrelated")
				gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "unrelated")
			case "revoked":
				setNativeAttemptStateFixture(t, f, o.AttemptID, "failed")
			case "foreign-preparation":
				o.PreparationRunRef = "worktree_verify:invented"
			case "foreign-control":
				o.ControlID = "control:invented"
			case "wrong-recipe":
				if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.oracle.recipe_source.path','pkg/foreign.json') WHERE lease_id='native-prepare'`); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.s.VerifyWorktree(context.Background(), f.request("repair-negative", &o))
			if err == nil {
				t.Fatal("unauthorized repair verification ran")
			}
			f.assertNoLaunch(t)
		})
	}
}

func TestReviewRepairTerminalRejectsDirtyUnrelatedOrFailedObservation(t *testing.T) {
	for _, tc := range []string{"dirty", "unrelated", "failed-git"} {
		t.Run(tc, func(t *testing.T) {
			f := newNativeOracleFixture(t)
			o := f.authorizeLane(t, "review", f.actor, "review")
			c := commitReviewRepair(t, f, "pkg/value.go")
			if tc == "dirty" {
				if err := os.WriteFile(filepath.Join(f.repo, "dirty"), []byte("dirty\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "unrelated" {
				gitRunStore(t, f.repo, "checkout", "--orphan", "unrelated")
				gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "unrelated")
			}
			var runner GitRunner
			if tc == "failed-git" {
				runner = failingReviewGitRunner{}
			}
			before := maxEventSeq(t, f.s, f.work)
			_, err := PrepareWorkerTerminalDelivery(context.Background(), f.s, PrepareWorkerTerminalDeliveryRequest{WorkID: f.work, AttemptID: o.AttemptID, EventID: "negative-terminal", Runner: runner})
			if err == nil || maxEventSeq(t, f.s, f.work) != before {
				t.Fatalf("invalid terminal observation for %s landed: %v C=%s", tc, err, c)
			}
		})
	}
}

type failingReviewGitRunner struct{}

func (failingReviewGitRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("synthetic Git observation failure")
}
