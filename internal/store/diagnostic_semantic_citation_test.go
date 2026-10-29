package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOracleJudgedCaseShape(t *testing.T) {
	f := seedOracleRepairFixture(t, "judged-shape")
	defer f.store.Close()
	fields := twoPathOracleFields(t, f.store, "judged-shape", func(o map[string]any) {
		cases := o["cases"].([]map[string]any)
		o["cases"] = append(cases, map[string]any{"case_id": "case:semantic", "owner_id": "owner:repair", "entry_point": "citation accuracy", "input_class": "citation", "expected_state": "source supports claim", "judge": "review"})
	})
	oracle, err := decodeAcceptanceOracle(mustJSONValue(fields))
	if err != nil {
		t.Fatalf("Main-declared judged case must decode: %v", err)
	}
	if err := validateAcceptanceOracle(oracle, oracle.Owners[0].PredicateIDs); err != nil {
		t.Fatalf("judged case alongside executable predicate coverage: %v", err)
	}
	for _, mode := range []string{"unknown-judge", "judged-controls", "native-judged-case", "no-predicate-controls", "mixed-path"} {
		t.Run(mode, func(t *testing.T) {
			var graph AcceptanceOracle
			if err := json.Unmarshal(mustJSONValue(oracle), &graph); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown-judge":
				graph.Cases[2].Judge = "worker"
			case "judged-controls":
				graph.Cases[2].ControlIDs = []string{"control:check-a"}
			case "native-judged-case":
				graph.Controls[0].CaseIDs = append(graph.Controls[0].CaseIDs, "case:semantic")
			case "no-predicate-controls":
				graph.Controls = nil
			case "mixed-path":
				graph.Cases[2].EntryPoint = graph.Cases[0].EntryPoint
			}
			if err := validateAcceptanceOracle(&graph, oracle.Owners[0].PredicateIDs); err == nil {
				t.Fatal("invalid judge/control arm admitted")
			}
		})
	}
}

func TestOracleJudgedRequirementsUnion(t *testing.T) {
	oracle := testOracleGraph()
	owner := oracle.Owners[0].OwnerID
	oracle.Cases = append(oracle.Cases,
		OracleCase{CaseID: "case:judged-a", OwnerID: owner, EntryPoint: "citation", Judge: OracleJudgeReview},
		OracleCase{CaseID: "case:judged-b", OwnerID: owner, EntryPoint: "citation", Judge: OracleJudgeReview},
		OracleCase{CaseID: "case:executable", OwnerID: owner, EntryPoint: "citation", ControlIDs: []string{"control:path"}},
	)
	finding := &WorkerOpenOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: owner, ControlIDs: []string{"control:source"}, Tie: &WorkerOracleFinding{EntryPoint: "citation"}}
	controls, judged, err := oracleFindingClosureRequirements(finding, oracle)
	if err != nil || !reflect.DeepEqual(controls, map[string]bool{"control:source": true, "control:path": true}) || !reflect.DeepEqual(judged, map[string]bool{"case:judged-a": true, "case:judged-b": true}) {
		t.Fatalf("controls=%v judged=%v err=%v", controls, judged, err)
	}
	// An exact judged match must not pull unrelated owner controls into a
	// semantic-only obligation through the owner fallback.
	oracle.Cases = oracle.Cases[:len(oracle.Cases)-1]
	finding.ControlIDs = nil
	controls, judged, err = oracleFindingClosureRequirements(finding, oracle)
	if err != nil || len(controls) != 0 || len(judged) != 2 {
		t.Fatalf("pure controls=%v judged=%v err=%v", controls, judged, err)
	}
	finding.Classification = OracleClassificationDeliveryBlocker
	controls, judged, err = oracleFindingClosureRequirements(finding, oracle)
	wantControls := map[string]bool{}
	for _, control := range oracle.Controls {
		if control.OwnerID == owner {
			wantControls[control.ControlID] = true
		}
	}
	if err != nil || !reflect.DeepEqual(controls, wantControls) || len(judged) != 2 {
		t.Fatalf("owner fallback controls=%v judged=%v err=%v", controls, judged, err)
	}
}

func TestOracleJudgedLegacyDigestStability(t *testing.T) {
	type executableCase struct {
		CaseID        string   `json:"case_id"`
		OwnerID       string   `json:"owner_id"`
		EntryPoint    string   `json:"entry_point"`
		InputClass    string   `json:"input_class"`
		ExpectedState string   `json:"expected_state"`
		ControlIDs    []string `json:"control_ids"`
	}
	oracle := testOracleGraph()
	cases := make([]executableCase, len(oracle.Cases))
	for i, c := range oracle.Cases {
		cases[i] = executableCase{c.CaseID, c.OwnerID, c.EntryPoint, c.InputClass, c.ExpectedState, c.ControlIDs}
	}
	legacy := struct {
		Owners   []OracleOwner    `json:"owners"`
		Cases    []executableCase `json:"cases"`
		Controls []OracleControl  `json:"controls"`
	}{oracle.Owners, cases, oracle.Controls}
	legacyBytes := mustJSONValue(legacy)
	if string(mustJSONValue(oracle)) != string(legacyBytes) {
		t.Fatal("old executable oracle bytes changed")
	}
	job := WorkerJobRecordedPayload{JobID: "job:legacy", Revision: 1, AcceptanceOracle: oracle}
	content := `{"job_id":"job:legacy","revision":1,"contract_version":0,"objective":"","stopping_condition":"","path_scope":null,"predicate_ids":null,"checks":null,"prerequisites":null,"unresolved_refs":null,"acceptance_oracle":` + string(legacyBytes) + `}`
	if got, want := DeriveWorkerJobDigest(job), nativeDigest([]byte(content)); got != want {
		t.Fatalf("job digest=%s legacy=%s", got, want)
	}
	before, err := nativeBundleForControl(oracle, oracle.Controls[0].ControlID)
	if err != nil {
		t.Fatal(err)
	}
	oracle.Cases = append(oracle.Cases, OracleCase{CaseID: "case:judged", OwnerID: oracle.Owners[0].OwnerID, EntryPoint: "citation", Judge: OracleJudgeReview})
	after, err := nativeBundleForControl(oracle, oracle.Controls[0].ControlID)
	if err != nil || string(mustJSONValue(before)) != string(mustJSONValue(after)) || nativeBundleDigest(before) != nativeBundleDigest(after) {
		t.Fatalf("judged addition changed native bundle: err=%v", err)
	}
	job.AcceptanceOracle = oracle
	if DeriveWorkerJobDigest(job) == nativeDigest([]byte(content)) {
		t.Fatal("Main's judged case is absent from immutable job digest")
	}
	resolution := WorkerOracleResolution{FindingID: "finding:1:0", EvidenceRefs: []string{"run:one"}}
	if got := string(mustJSONValue(resolution)); got != `{"finding_id":"finding:1:0","evidence_refs":["run:one"]}` {
		t.Fatalf("old resolution bytes=%s", got)
	}
}

func TestOracleJudgedResolutionShape(t *testing.T) {
	for _, mode := range []string{"neither", "empty-native", "empty-judged", "duplicate", "too-many", "too-long", "native", "pure", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			r := WorkerOracleResolution{FindingID: "finding:1:0", JudgedCaseIDs: []string{"case:semantic"}}
			switch mode {
			case "neither":
				r.JudgedCaseIDs = nil
			case "empty-native":
				r.EvidenceRefs = []string{}
			case "empty-judged":
				r.JudgedCaseIDs = []string{}
				r.EvidenceRefs = []string{"run:one"}
			case "duplicate":
				r.JudgedCaseIDs = []string{"case:semantic", "case:semantic"}
			case "too-many":
				r.JudgedCaseIDs = []string{"case:1", "case:2", "case:3", "case:4", "case:5", "case:6", "case:7", "case:8", "case:9"}
			case "too-long":
				r.JudgedCaseIDs = []string{"case:" + strings.Repeat("a", 124)}
			case "native":
				r.JudgedCaseIDs = nil
				r.EvidenceRefs = []string{"run:one"}
			case "mixed":
				r.EvidenceRefs = []string{"run:one"}
			}
			err := validateWorkerOracleResolutionsShape([]WorkerOracleResolution{r})
			want := mode == "native" || mode == "pure" || mode == "mixed"
			if (err == nil) != want {
				t.Fatalf("shape %s err=%v want accepted=%v", mode, err, want)
			}
		})
	}
}

type semanticReviewFixture struct {
	f                            oracleRepairFixture
	job                          WorkerJobBinding
	workID, canonicalID, localID string
	tie                          WorkerOracleFinding
	fields                       map[string]any
}

func (d *semanticReviewFixture) journey() *candidateJourney {
	return &candidateJourney{fixture: workContextFixture{store: d.f.store, workID: d.workID, owner: d.f.owner}, s: d.f.store, workID: d.workID, repo: d.f.entry.Path}
}

func (d *semanticReviewFixture) dispatch(t *testing.T, attempt, lane string) WorkerReviewCandidate {
	t.Helper()
	s, ctx := d.f.store, t.Context()
	packet := joinPacketFor(t, s, d.workID, currentStep(t, s, d.workID), attempt, lane, registeredLaneIdentity(t, lane))
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, d.workID, d.job, lane)
	correction, err := workflowCorrectionContextForDispatch(ctx, s.db, d.workID, currentStep(t, s, d.workID), attempt)
	if err != nil {
		t.Fatal(err)
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	checkpoint, err := readLatestContextCheckpointTx(ctx, s.db, d.workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		packet["inputs"].(map[string]any)["checkpoint"] = checkpoint
	}
	prepared, err := PrepareWorkerDispatchFreshness(ctx, s, PrepareWorkerDispatchRequest{WorkID: d.workID, SessionWorktree: d.f.entry.Path, Runner: d.f.git, DispatchPayload: mustJSONValue(map[string]any{"attempt_id": attempt, "worker_packet": packet}), reviewPlanner: oracleFixtureReviewPlanner(t, oracleFixtureRuntime)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchJoinAttempt(WithWorkerDispatchPreparation(ctx, prepared), t, s, d.workID, readWorkVersion(t, s, d.workID), d.f.owner, packet); err != nil {
		t.Fatal(err)
	}
	capability := lane
	if lane == "implement" {
		capability = "implementation"
	}
	d.journey().laneDispatch(t, attempt, capability, &d.job)
	if prepared.proof.observation != nil {
		o := prepared.proof.observation
		return WorkerReviewCandidate{ProjectID: "project", Commit: o.Commit, Tree: o.Tree}
	}
	return WorkerReviewCandidate{}
}

func (d *semanticReviewFixture) completion(t *testing.T, attempt, lane string, candidate WorkerReviewCandidate, closeFinding bool) Event {
	t.Helper()
	capability := lane
	if lane == "implement" {
		capability = "implementation"
	}
	e := jobBoundCompletionEvent(d.workID, attempt+":completed", attempt, reviewGateLane(t, capability), time.Now().UTC(), &d.job, d.f.entry.Path)
	var p WorkerCompletedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	deps, err := workflowReviewDependencies(t.Context(), d.f.store.db, d.workID)
	if err != nil {
		t.Fatal(err)
	}
	tie := d.tie
	finding := WorkerReviewFinding{FindingID: d.localID, Status: "open", Severity: "P2", Confidence: "high", Detail: "The source must support the citation claim.", EvidenceRefs: []string{}, ClosureRefs: []string{}, Oracle: &tie}
	p.Review = &WorkerReviewBlock{Disposition: "correction_required", InputCandidate: candidate, OutputCandidate: candidate, Dependencies: deps, Findings: []WorkerReviewFinding{finding}, Assessments: []WorkerReviewAssessment{}, RemainingObligations: []WorkerReviewObligation{}}
	if closeFinding {
		p.Review.Disposition = "ready_for_final_proof"
		p.Review.Findings[0].Status = "closed"
		p.Review.Findings[0].ClosureRefs = []string{"fixture:source-supports-claim"}
		tie.ContinuesFindingID = d.canonicalID
		p.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: d.canonicalID, JudgedCaseIDs: []string{"case:semantic"}}}
	}
	e.Payload = mustJSONValue(p)
	return e
}

func (d *semanticReviewFixture) terminal(t *testing.T, e Event) error {
	t.Helper()
	var p WorkerCompletedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), d.f.store, PrepareWorkerTerminalDeliveryRequest{WorkID: d.workID, AttemptID: p.AttemptID, EventID: e.EventID, Runner: d.f.git})
	if err != nil {
		return err
	}
	ctx := WithWorkerTerminalPreparation(t.Context(), prepared)
	return d.f.store.TransactDurable(ctx, func(tx *Transaction) error { _, err := ApplyWorkerEvidenceOperationTx(ctx, tx, e); return err })
}

func newSemanticReviewFixture(t *testing.T, workID string, mixed bool, beforeSource ...func(*semanticReviewFixture)) *semanticReviewFixture {
	t.Helper()
	d := &semanticReviewFixture{f: seedOracleRepairFixture(t, workID), workID: workID, localID: "finding:original:0"}
	t.Cleanup(func() { d.f.store.Close() })
	d.tie = WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", EntryPoint: "citation accuracy", FailureFamily: "citation-accuracy", EvidenceRefs: []string{"fixture:unsupported-citation"}}
	if mixed {
		d.tie.ControlIDs = []string{"control:check-a"}
	}
	d.fields = twoPathOracleFields(t, d.f.store, workID, nil)
	for _, prepare := range beforeSource {
		prepare(d)
	}
	d.job = recordOracleJobRevision(t, d.f.store, workID, d.f.owner, "job:citation-review", d.fields)
	a := d.dispatch(t, "attempt:original", "review")
	if err := d.terminal(t, d.completion(t, "attempt:original", "review", a, false)); err != nil {
		t.Fatal(err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(t.Context(), d.f.store.db, workID)
	if err != nil || len(lineage.openFindingIDs()) != 1 {
		t.Fatalf("original lineage=%+v err=%v", lineage, err)
	}
	d.canonicalID = lineage.openFindingIDs()[0]
	advanceOracleFixtureSubject(t, d.f, workID, "2")
	d.fields["cases"] = append(d.fields["cases"].([]map[string]any), map[string]any{"case_id": "case:semantic", "owner_id": "owner:repair", "entry_point": d.tie.EntryPoint, "input_class": "citation", "expected_state": "source supports claim", "judge": "review"})
	d.job = recordOracleJobRevision(t, d.f.store, workID, d.f.owner, d.job.JobID, d.fields)
	return d
}

func (d *semanticReviewFixture) assertClosed(t *testing.T, want bool) {
	t.Helper()
	lineage, err := readWorkerOracleFindingLineageTx(t.Context(), d.f.store.db, d.workID)
	if err != nil {
		t.Fatal(err)
	}
	if f := lineage.findings[d.canonicalID]; f == nil || f.Closed != want || len(lineage.findings) != 1 {
		t.Fatalf("canonical closed=%v want=%v findings=%+v", f, want, lineage.findings)
	}
	state, err := readWorkflowReviewFacts(t.Context(), d.f.store.db, d.workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Findings) != 1 || state.Findings[0].FindingID != d.localID || (state.Findings[0].Status == "closed") != want {
		t.Fatalf("Review findings=%+v want closed=%v", state.Findings, want)
	}
}

func TestSemanticJudgedReviewAcceptance(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "pure"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			d := newSemanticReviewFixture(t, "judged-"+name, mixed)
			candidate := d.dispatch(t, "attempt:judgment", "review")
			e := d.completion(t, "attempt:judgment", "review", candidate, true)
			if mixed {
				run := oracleVerifyControl(t, d.f, d.workID, "path-a-pass", "2", oracleGoArgv("path-a"), 0)
				var p WorkerCompletedPayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatal(err)
				}
				zero := 0
				withReceipt(&p, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: candidate.Commit, RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: "pass", ExitCode: &zero, RunRef: run.OperationRef, EvidenceRefs: []string{}})
				p.Review.ResolvedFindings[0].EvidenceRefs = []string{run.OperationRef}
				p.Review.Findings[0].ClosureRefs = append(p.Review.Findings[0].ClosureRefs, run.OperationRef)
				e.Payload = mustJSONValue(p)
			}
			if err := d.terminal(t, e); err != nil {
				t.Fatalf("ingest judgment: %v", err)
			}
			d.assertClosed(t, false)
			if err := d.journey().acceptEvidence(t, "attempt:judgment"); err != nil {
				t.Fatalf("accept judgment: %v", err)
			}
			d.assertClosed(t, true)
		})
	}
}

func TestSemanticJudgedReviewRefusals(t *testing.T) {
	for _, mode := range []string{"missing-continuation", "missing-resolution", "extra-judged", "missing-judged", "fabricated-native"} {
		t.Run(mode, func(t *testing.T) {
			d := newSemanticReviewFixture(t, "judged-"+mode, false)
			candidate := d.dispatch(t, "attempt:judgment", "review")
			e := d.completion(t, "attempt:judgment", "review", candidate, true)
			var p WorkerCompletedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-continuation":
				p.Review.Findings = []WorkerReviewFinding{}
			case "missing-resolution":
				p.Review.ResolvedFindings = nil
			case "extra-judged":
				p.Review.ResolvedFindings[0].JudgedCaseIDs = append(p.Review.ResolvedFindings[0].JudgedCaseIDs, "case:other")
			case "missing-judged":
				p.Review.ResolvedFindings[0].JudgedCaseIDs = nil
				p.Review.ResolvedFindings[0].EvidenceRefs = []string{"fixture:no-native-proof"}
			case "fabricated-native":
				p.Review.ResolvedFindings[0].EvidenceRefs = []string{"fixture:not-an-execution"}
			}
			e.Payload = mustJSONValue(p)
			if err := d.terminal(t, e); err == nil {
				t.Fatal("invalid proposal was admitted")
			} else if !strings.Contains(err.Error(), "judged") && !strings.Contains(err.Error(), "evidence_refs") {
				t.Fatalf("unexpected refusal: %v", err)
			}
			d.assertClosed(t, false)
		})
	}
}

func TestSemanticJudgedReviewDisposition(t *testing.T) {
	for _, mode := range []string{"pending", "rejected", "failed", "stale-candidate", "stale-dependencies"} {
		t.Run(mode, func(t *testing.T) {
			d := newSemanticReviewFixture(t, "judged-"+mode, false)
			candidate := d.dispatch(t, "attempt:judgment", "review")
			if mode == "failed" {
				e := attributionWorkerFailedEvent(d.workID, "attempt:judgment", "judgment:failed", preferredModelForLane(reviewGateLane(t, "review")), attributionWorkerObservation())
				if err := s2WorkerFailedLive(t, d.f.store, e); err != nil {
					t.Fatal(err)
				}
				d.assertClosed(t, false)
				if err := d.journey().acceptEvidence(t, "attempt:judgment"); err == nil {
					t.Fatal("failed Review accepted")
				}
				d.assertClosed(t, false)
				return
			}
			if err := d.terminal(t, d.completion(t, "attempt:judgment", "review", candidate, true)); err != nil {
				t.Fatal(err)
			}
			d.assertClosed(t, false)
			switch mode {
			case "pending":
				return
			case "rejected":
				predicate := d.fields["owners"].([]map[string]any)[0]["predicate_ids"].([]string)[0]
				if err := d.journey().action(t, "reject_worker_result", map[string]any{"attempt_id": "attempt:judgment", "attempt_epoch": d.journey().dispatchEpoch(t, "attempt:judgment"), "diagnosis": "the source does not support the claim", "strategy": "retain the finding and inspect another source", "predicate_ids": []string{predicate}, "evidence_refs": []string{"attempt:judgment:completed"}, "open_finding_ids": []string{d.canonicalID}}, d.f.owner); err != nil {
					t.Fatal(err)
				}
			case "stale-candidate":
				advanceOracleFixtureSubject(t, d.f, d.workID, "3")
				d.job = recordOracleJobRevision(t, d.f.store, d.workID, d.f.owner, d.job.JobID, d.fields)
				newer := d.dispatch(t, "attempt:newer", "review")
				e := d.completion(t, "attempt:newer", "review", newer, false)
				var p WorkerCompletedPayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatal(err)
				}
				p.Review.Findings = []WorkerReviewFinding{}
				e.Payload = mustJSONValue(p)
				if err := d.terminal(t, e); err != nil {
					t.Fatal(err)
				}
				if err := d.journey().acceptEvidence(t, "attempt:newer"); err != nil {
					t.Fatal(err)
				}
			case "stale-dependencies":
				version := readWorkVersion(t, d.f.store, d.workID)
				actor, err := WorkflowActorRef(d.f.owner)
				if err != nil {
					t.Fatal(err)
				}
				e := workflowEventWithActor("design:replacement", WorkflowDesignRecorded, d.workID, actor, map[string]any{
					"work_id": d.workID, "expected_version": version, "resulting_version": version + 1,
					"approach":     "Recheck the claim against an updated source plan.",
					"decisions":    []map[string]any{{"id": "decision:plan", "question": "Which source plan applies?", "choice": "Inspect the updated plan.", "rationale": "The sources changed.", "rejected": []string{}}},
					"touched_refs": []string{"internal/store"},
				})
				if err := applyWorkflowTestOperation(t.Context(), d.f.store, Operation{Events: []Event{e}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, d.workID): version}}); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "rejected" {
				if err := d.journey().acceptEvidence(t, "attempt:judgment"); err == nil {
					t.Fatal("stale Review accepted")
				}
			}
			d.assertClosed(t, false)
		})
	}
}

func TestSemanticJudgedNonReviewRefuses(t *testing.T) {
	d := newSemanticReviewFixture(t, "judged-implement", false)
	candidate := d.dispatch(t, "attempt:continue", "review")
	e := d.completion(t, "attempt:continue", "review", candidate, false)
	var p WorkerCompletedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	p.Review.Disposition, p.Review.Findings = "continue", []WorkerReviewFinding{}
	e.Payload = mustJSONValue(p)
	if err := d.terminal(t, e); err != nil {
		t.Fatal(err)
	}
	if err := d.journey().acceptEvidence(t, "attempt:continue"); err != nil {
		t.Fatal(err)
	}
	d.dispatch(t, "attempt:implement", "implement")
	e = d.completion(t, "attempt:implement", "implement", candidate, true)
	if err := d.terminal(t, e); err == nil || !strings.Contains(err.Error(), "only a Review lane") {
		t.Fatalf("non-Review refusal=%v", err)
	}
	d.assertClosed(t, false)
}

func semanticExecutableHistory(t *testing.T, d *semanticReviewFixture, jobID, caseID string) {
	t.Helper()
	fields := twoPathOracleFields(t, d.f.store, d.workID, func(o map[string]any) {
		addSemanticExecutableCase(o, caseID, d.tie.EntryPoint)
	})
	recordOracleJobRevision(t, d.f.store, d.workID, d.f.owner, jobID, fields)
}

func addSemanticExecutableCase(fields map[string]any, caseID, path string) {
	fields["cases"] = append(fields["cases"].([]map[string]any), map[string]any{"case_id": caseID, "owner_id": "owner:repair", "entry_point": path, "input_class": "citation", "expected_state": "source supports claim", "control_ids": []string{"control:semantic"}})
	control := map[string]any{}
	for key, value := range fields["controls"].([]map[string]any)[0] {
		control[key] = value
	}
	control["control_id"], control["case_ids"], control["argv"] = "control:semantic", []string{caseID}, oracleGoArgv("semantic")
	fields["controls"] = append(fields["controls"].([]map[string]any), control)
}

func TestSemanticJudgedHistoryRefuses(t *testing.T) {
	for _, history := range []string{"source-same-id", "source-renamed", "dispatch-same-id", "dispatch-renamed"} {
		t.Run(history, func(t *testing.T) {
			caseID := "case:semantic"
			if strings.HasSuffix(history, "renamed") {
				caseID = "case:old-executable"
			}
			var before []func(*semanticReviewFixture)
			if strings.HasPrefix(history, "source") {
				before = append(before, func(d *semanticReviewFixture) { semanticExecutableHistory(t, d, "job:citation-review", caseID) })
			}
			d := newSemanticReviewFixture(t, "judged-"+history, false, before...)
			if strings.HasPrefix(history, "dispatch") {
				semanticExecutableHistory(t, d, "job:judgment", caseID)
				d.job = recordOracleJobRevision(t, d.f.store, d.workID, d.f.owner, "job:judgment", d.fields)
			}
			if strings.HasSuffix(history, "renamed") {
				addSemanticExecutableCase(d.fields, caseID, d.tie.EntryPoint)
				graph, err := decodeAcceptanceOracle(mustJSONValue(d.fields))
				if err != nil {
					t.Fatal(err)
				}
				if err := validateAcceptanceOracle(graph, graph.Owners[0].PredicateIDs); err == nil || !strings.Contains(err.Error(), "same owner and entry point") {
					t.Fatalf("retained executable and renamed judged case must refuse at declaration: %v", err)
				}
				d.assertClosed(t, false)
				return
			}
			candidate := d.dispatch(t, "attempt:judgment", "review")
			if err := d.terminal(t, d.completion(t, "attempt:judgment", "review", candidate, true)); err == nil || !strings.Contains(err.Error(), "recorded source or dispatched job") {
				t.Fatalf("history downgrade refusal=%v", err)
			}
			d.assertClosed(t, false)
		})
	}
}

func TestSemanticJudgedAcceptanceAtomic(t *testing.T) {
	d := newSemanticReviewFixture(t, "judged-atomic", false)
	candidate := d.dispatch(t, "attempt:judgment", "review")
	if err := d.terminal(t, d.completion(t, "attempt:judgment", "review", candidate, true)); err != nil {
		t.Fatal(err)
	}
	before := d.terminalBytes(t)
	rollback := errors.New("fixture rolls back the accepted disposition")
	version, epoch := readWorkVersion(t, d.f.store, d.workID), d.journey().dispatchEpoch(t, "attempt:judgment")
	err := d.f.store.Transact(t.Context(), func(tx *Transaction) error {
		payload := mustJSONValue(map[string]any{"attempt_id": "attempt:judgment", "attempt_epoch": epoch})
		_, err := ApplyWorkflowActionTx(t.Context(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: d.workID, ExpectedVersion: version, ActionID: "accept_worker_result", Payload: payload, Actor: d.f.owner,
			AcceptedInputsDigest: nativeDigest(payload), IdempotencyIdentity: "atomic-accept", OperationID: "atomic-accept", PrincipalRef: d.f.owner.PrincipalRef,
			Tool: "concord_work_transition", IdempotencyKey: "atomic-accept", RequestID: "atomic-accept", ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		lineage, err := readWorkerOracleFindingLineageTx(t.Context(), tx.tx, d.workID)
		if err != nil || !lineage.findings[d.canonicalID].Closed {
			t.Fatalf("canonical not closed inside accepted transaction: %v", err)
		}
		state, err := readWorkflowReviewFacts(t.Context(), tx.tx, d.workID)
		if err != nil || state.Findings[0].Status != "closed" {
			t.Fatalf("Review not closed inside accepted transaction: %v", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("acceptance rollback=%v", err)
	}
	d.assertClosed(t, false)
	if after := d.terminalBytes(t); before != after {
		t.Fatal("rolled-back acceptance changed retained reports")
	}
	if err := d.journey().acceptEvidence(t, "attempt:judgment"); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, true)
	if after := d.terminalBytes(t); before != after {
		t.Fatal("acceptance rewrote retained reports")
	}
}

func TestSemanticJudgedAcceptanceRechecksHistory(t *testing.T) {
	d := newSemanticReviewFixture(t, "judged-later-history", false)
	candidate := d.dispatch(t, "attempt:judgment", "review")
	if err := d.terminal(t, d.completion(t, "attempt:judgment", "review", candidate, true)); err != nil {
		t.Fatal(err)
	}
	before := d.terminalBytes(t)
	semanticExecutableHistory(t, d, d.job.JobID, "case:formerly-executable")
	if err := d.journey().acceptEvidence(t, "attempt:judgment"); err == nil || !strings.Contains(err.Error(), "recorded source or dispatched job") {
		t.Fatalf("new history must invalidate pending judgment: %v", err)
	}
	d.assertClosed(t, false)
	if before != d.terminalBytes(t) {
		t.Fatal("refused acceptance rewrote the retained reports")
	}
}

func (d *semanticReviewFixture) terminalBytes(t *testing.T) string {
	t.Helper()
	var raw string
	if err := d.f.store.db.QueryRow(`SELECT group_concat(terminal_event_id||terminal_payload,'') FROM (SELECT terminal_event_id,terminal_payload FROM worker_attempts WHERE work_id=? AND terminal_payload IS NOT NULL ORDER BY attempt_id)`, d.workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSemanticJudgedMixedRequiresNative(t *testing.T) {
	for _, mode := range []string{"missing", "unrelated", "later-failure", "citation-closure"} {
		t.Run(mode, func(t *testing.T) {
			d := newSemanticReviewFixture(t, "judged-native-"+mode, true)
			candidate := d.dispatch(t, "attempt:judgment", "review")
			e := d.completion(t, "attempt:judgment", "review", candidate, true)
			if mode == "missing" {
				if err := d.terminal(t, e); err == nil || !strings.Contains(err.Error(), "evidence_refs") {
					t.Fatalf("missing native refusal=%v", err)
				}
			} else {
				run := oracleVerifyControl(t, d.f, d.workID, "pass", "2", oracleGoArgv("path-a"), 0)
				var p WorkerCompletedPayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatal(err)
				}
				zero := 0
				withReceipt(&p, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: candidate.Commit, RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: "pass", ExitCode: &zero, RunRef: run.OperationRef, EvidenceRefs: []string{}})
				p.Review.ResolvedFindings[0].EvidenceRefs = []string{run.OperationRef}
				if mode == "unrelated" {
					p.Review.ResolvedFindings[0].EvidenceRefs = []string{"fixture:unrelated"}
				}
				e.Payload = mustJSONValue(p)
				err := d.terminal(t, e)
				if mode == "unrelated" {
					if err == nil || !strings.Contains(err.Error(), "does not qualify") {
						t.Fatalf("unrelated native refusal=%v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if mode == "citation-closure" {
						if err := d.journey().acceptEvidence(t, "attempt:judgment"); err == nil || !strings.Contains(err.Error(), "has no closure receipt for control control:check-a") {
							t.Fatalf("citation-only mixed closure refusal=%v", err)
						}
					} else {
						oracleVerifyControl(t, d.f, d.workID, "later-fail", "2", oracleGoArgv("path-a"), 1)
						if err := d.journey().acceptEvidence(t, "attempt:judgment"); err == nil || !strings.Contains(err.Error(), "does not qualify") {
							t.Fatalf("stale native acceptance refusal=%v", err)
						}
					}
				}
			}
			d.assertClosed(t, false)
		})
	}
}

// This diagnostic uses synthetic candidate contents and the existing trusted
// native-ledger fixtures. It exercises store Review dispatch and terminal ingest,
// not a real model, Git content read, native executable, or public adapter.
func TestDiagnosticSemanticCitationClosure(t *testing.T) {
	ctx := context.Background()
	const workID = "diagnostic-semantic-citation"
	const semanticEntry = "documentation citation accurately supports its claim"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()

	// Both versions have a consistent registry. Only the second citation
	// points to the synthetic source that actually supports the claim.
	type document struct{ id, path, claim, citation string }
	sources := map[string]string{"source:atomic": "writes are atomic", "source:format": "records are JSON"}
	beforeDoc := document{"doc:one", "docs/one.md", "writes are atomic", "source:format"}
	afterDoc := beforeDoc
	afterDoc.citation = "source:atomic"
	consistent := func(d document) bool { return d.id != "" && d.path != "" }
	accurate := func(d document) bool { return sources[d.citation] == d.claim }
	if !consistent(beforeDoc) || !consistent(afterDoc) || accurate(beforeDoc) || !accurate(afterDoc) {
		t.Fatal("fixture must isolate citation correction from registry consistency")
	}

	command := oracleGoArgv("registry-consistency")
	fields := twoPathOracleFields(t, s, workID, func(o map[string]any) {
		owner := o["owners"].([]map[string]any)[0]
		owner["obligation"] = "the document registry has an identifier and path for each record"
		control := o["controls"].([]map[string]any)[0]
		control["control_id"], control["case_ids"], control["argv"] = "control:registry", []string{"case:registry"}, command
		o["controls"] = []map[string]any{control}
		o["cases"] = []map[string]any{{"case_id": "case:registry", "owner_id": "owner:repair", "entry_point": "registry record identifier and path", "input_class": "document registry", "expected_state": "record has an identifier and path", "control_ids": []string{"control:registry"}}}
	})
	job := recordOracleJobRevision(t, s, workID, f.owner, "job:citation-review", fields)
	w := workContextFixture{store: s, workID: workID, owner: f.owner}
	j := &candidateJourney{fixture: w, s: s, workID: workID, repo: f.entry.Path}

	// The dispatch and terminal preparations mirror oCompleteOracleFixtureReview;
	// the report is authored here so both the semantic finding and closure claim
	// reach the real lane/subject/oracle validators.
	dispatchReview := func(attempt string) WorkerReviewCandidate {
		t.Helper()
		packet := joinPacketFor(t, s, workID, currentStep(t, s, workID), attempt, "review", registeredLaneIdentity(t, "review"))
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, "review")
		correction, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, currentStep(t, s, workID), attempt)
		if err != nil {
			t.Fatal(err)
		}
		if correction != nil {
			packet["inputs"].(map[string]any)["correction"] = correction
		}
		checkpoint, err := readLatestContextCheckpointTx(ctx, s.db, workID, "workflow_action")
		if err != nil {
			t.Fatal(err)
		}
		if checkpoint != nil {
			packet["inputs"].(map[string]any)["checkpoint"] = checkpoint
		}
		prepared, err := PrepareWorkerDispatchFreshness(ctx, s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: f.entry.Path, Runner: f.git,
			DispatchPayload: mustJSONValue(map[string]any{"attempt_id": attempt, "worker_packet": packet}), reviewPlanner: oracleFixtureReviewPlanner(t, oracleFixtureRuntime)})
		if err != nil {
			t.Fatalf("prepare Review %s: %v", attempt, err)
		}
		if _, err := dispatchJoinAttempt(WithWorkerDispatchPreparation(ctx, prepared), t, s, workID, readWorkVersion(t, s, workID), f.owner, packet); err != nil {
			t.Fatalf("dispatch Review %s: %v", attempt, err)
		}
		j.laneDispatch(t, attempt, "review", &job)
		if prepared.proof.observation == nil {
			t.Fatal("Review dispatch has no host candidate observation")
		}
		o := prepared.proof.observation
		return WorkerReviewCandidate{ProjectID: "project", Commit: o.Commit, Tree: o.Tree}
	}
	completion := func(attempt string, candidate WorkerReviewCandidate, disposition string, mutate func(*WorkerCompletedPayload)) Event {
		t.Helper()
		lane := reviewGateLane(t, "review")
		event := jobBoundCompletionEvent(workID, attempt+":completed", attempt, lane, time.Now().UTC(), &job, f.entry.Path)
		var payload WorkerCompletedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		dependencies, err := workflowReviewDependencies(ctx, s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		payload.Review = &WorkerReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
			Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
		mutate(&payload)
		event.Payload = mustJSONValue(payload)
		return event
	}
	terminal := func(event Event) error {
		t.Helper()
		var payload WorkerCompletedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		prepared, err := PrepareWorkerTerminalDelivery(ctx, s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: payload.AttemptID, EventID: event.EventID, Runner: f.git})
		if err != nil {
			t.Fatalf("prepare Review terminal: %v", err)
		}
		terminalCtx := WithWorkerTerminalPreparation(ctx, prepared)
		return s.TransactDurable(terminalCtx, func(tx *Transaction) error {
			_, err := ApplyWorkerEvidenceOperationTx(terminalCtx, tx, event)
			return err
		})
	}

	first := "attempt:citation:original"
	a := dispatchReview(first)
	if err := terminal(completion(first, a, "correction_required", func(p *WorkerCompletedPayload) {
		p.Review.Findings = []WorkerReviewFinding{{FindingID: "finding:" + first + ":0", Status: "open", Severity: "P2", Confidence: "high",
			Detail: "The cited JSON-format source does not support the atomic-write claim.", EvidenceRefs: []string{}, ClosureRefs: []string{},
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", EntryPoint: semanticEntry, FailureFamily: "citation-accuracy", EvidenceRefs: []string{"fixture:source-format-vs-atomic-claim"}}}}
	})); err != nil {
		t.Fatalf("retain original Review finding: %v", err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, s.db, workID)
	if err != nil || len(lineage.openFindingIDs()) != 1 {
		t.Fatalf("original lineage=%+v err=%v", lineage, err)
	}
	findingID := lineage.openFindingIDs()[0]
	findingBefore := string(mustJSONValue(lineage.findings[findingID]))

	advanceOracleFixtureSubject(t, f, workID, "2")
	// A fresh candidate needs its own qualified preparation. Re-record the
	// unchanged registry obligation, without adding a semantic case or control.
	job = recordOracleJobRevision(t, s, workID, f.owner, job.JobID, fields)
	second := "attempt:citation:corrected"
	b := dispatchReview(second)
	if a.Commit == b.Commit {
		t.Fatal("corrected Review must inspect a fresh candidate")
	}
	// Content belongs to these synthetic candidate identities only; the mock
	// Git runner does not read it from a repository checkout.
	documents := map[string]document{a.Commit: beforeDoc, b.Commit: afterDoc}
	if accurate(documents[a.Commit]) || !accurate(documents[b.Commit]) {
		t.Fatal("fresh fixture Review must observe the corrected citation")
	}
	// This is a mocked command with a trusted fixture-native execute ledger,
	// not a launched Go test or a citation-accuracy check.
	run := oracleVerifyControl(t, f, workID, "registry-pass", "2", command, 0)
	exit := 0
	receipt := WorkerOracleReceipt{ControlIDs: []string{"control:registry"}, CaseIDs: []string{"case:registry"}, SubjectCommit: b.Commit,
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
		Result:       OracleReceiptResultPass, ExitCode: &exit, RunRef: run.OperationRef, EvidenceRefs: []string{}}
	oracle := oracleFixtureGraphForCommand(t, s, workID, command)
	for _, c := range oracle.Cases {
		if c.OwnerID == "owner:repair" && c.EntryPoint == semanticEntry {
			t.Fatal("diagnostic accidentally inventoried the semantic finding")
		}
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		producer, err := workerOracleReceiptProducerTx(ctx, tx.tx, workID, &receipt, oracle, b.Commit)
		if err == nil && (producer == nil || producer.OperationRef != run.OperationRef) {
			t.Fatal("registry receipt did not resolve its exact qualified producer")
		}
		return err
	}); err != nil {
		t.Fatalf("unrelated registry producer must qualify: %v", err)
	}
	// A receipt-only report validates; the identical report plus a semantic
	// closure claim must fail. Qualification is not proof of citation accuracy.
	event := completion(second, b, "ready_for_final_proof", func(p *WorkerCompletedPayload) {
		p.Evidence[0].Detail = "Fixture Review compared the corrected citation with the atomic-write source; the claim is supported. Registry consistency alone does not establish this judgment."
		withReceipt(p, &receipt)
	})
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return validateWorkerOracleCompletedReportTx(ctx, tx.tx, workID, payload, oracle, lineage)
	}); err != nil {
		t.Fatalf("receipt without closure claim must validate: %v", err)
	}
	payload.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: findingID, EvidenceRefs: []string{run.OperationRef}}}
	event.Payload = mustJSONValue(payload)
	readAttempt := func() WorkerAttempt {
		t.Helper()
		var attempt WorkerAttempt
		if err := s.ReadTransaction(ctx, func(tx *Transaction) error {
			var err error
			attempt, err = WorkerAttemptByIDTx(ctx, tx, second)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return attempt
	}
	attemptBefore, effectsBefore := readAttempt(), j.effects(t)
	var receiptCountBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&receiptCountBefore); err != nil {
		t.Fatal(err)
	}
	err = terminal(event)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Op != "worker_oracle_findings" ||
		failure.Detail != "uncovered_case entry path is still absent from the controlled inventory" ||
		failure.RecoveryAction != "add the reproduced entry path and prove its controls" || failure.EffectPossible || failure.RetrySafe {
		t.Fatalf("semantic closure refusal=%v, want exact invalid_payload inventory contradiction", err)
	}
	if after := readAttempt(); !reflect.DeepEqual(attemptBefore, after) || WorkerAttemptIsTerminal(after) {
		t.Fatalf("refused report changed attempt: before=%+v after=%+v", attemptBefore, after)
	}
	if after := j.effects(t); after != effectsBefore {
		t.Fatalf("refused report changed workflow/subject state: before=%s after=%s", effectsBefore, after)
	}
	var receiptCountAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&receiptCountAfter); err != nil || receiptCountAfter != receiptCountBefore {
		t.Fatalf("effect receipts before=%d after=%d err=%v", receiptCountBefore, receiptCountAfter, err)
	}
	if err := s.ReadTransaction(ctx, func(tx *Transaction) error {
		if _, found, err := EventByIDTx(ctx, tx, event.EventID); err != nil || found {
			t.Fatalf("refused terminal event retained: found=%v err=%v", found, err)
		}
		if _, found, err := EventByIDTx(ctx, tx, "candidate-dispatched-"+second); err != nil || !found {
			t.Fatalf("prior Review dispatch missing: found=%v err=%v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(ctx, s.db, workID)
	if err != nil || !reflect.DeepEqual(lineage.openFindingIDs(), []string{findingID}) || string(mustJSONValue(lineage.findings[findingID])) != findingBefore {
		t.Fatalf("refused report changed retained finding: lineage=%+v err=%v", lineage, err)
	}
	t.Logf("two real store Review dispatches; corrected candidate=%s; qualified mocked registry receipt=%s; semantic judgment true; exact refusal=%s; attempt/workflow/subject/finding/event state unchanged", b.Commit, run.OperationRef, failure.Detail)
}
