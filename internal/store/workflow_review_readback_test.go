package store

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestRetainedReviewCandidateReadback(t *testing.T) {
	for _, tc := range []struct{ name, project, tree string }{
		{"complete", "project-one", strings.Repeat("b", 40)},
		{"unobserved_tree", "project-one", ""},
		{"unrecorded_project", "", strings.Repeat("b", 40)},
		{"commit_only", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := WorkerReviewCandidate{ProjectID: tc.project, Commit: strings.Repeat("a", 40), Tree: tc.tree}
			state := WorkflowReviewState{Candidate: &candidate, Findings: []WorkerReviewFinding{}, PublicationOutstanding: true}
			if tc.name == "complete" {
				report := reviewBlockFixture()
				report.InputCandidate, report.OutputCandidate = candidate, candidate
				state.Dependencies = &report.Dependencies
				state.Accepted = &WorkflowAcceptedReview{
					AttemptID: "attempt-one", ReportRef: "worker-report:one", ActorRef: "actor-one",
					ReportDigest: "sha256:" + strings.Repeat("c", 64), Report: *report,
				}
				state.Findings = report.Findings
				state.ContinueConsumed, state.DependenciesCurrent = true, true
				state.GroupID = "group-one"
				state.Groups = map[string]workflowAssignmentGroup{"group-one": {}}
				state.StaleJobs = map[string]bool{"job-one": true}
				state.ExecutionProgressed = true
			}
			raw := mustJSONValue(state)
			if err := payloadschema.Validate("workflow_review_state", raw); err != nil {
				t.Fatalf("retained review readback rejected: %v", err)
			}
			var view struct {
				Candidate map[string]string `json:"candidate"`
			}
			if err := json.Unmarshal(raw, &view); err != nil {
				t.Fatal(err)
			}
			if view.Candidate["commit"] != candidate.Commit {
				t.Fatal("readback lost the recorded commit")
			}
			for field, want := range map[string]string{"project_id": tc.project, "tree": tc.tree} {
				got, present := view.Candidate[field]
				if got != want || present != (want != "") {
					t.Fatalf("%s = %q, present=%v; want %q, present=%v", field, got, present, want, want != "")
				}
			}
			if candidate != (WorkerReviewCandidate{ProjectID: tc.project, Commit: strings.Repeat("a", 40), Tree: tc.tree}) {
				t.Fatal("readback changed the retained candidate")
			}
			complete := tc.project != "" && tc.tree != ""
			if err := payloadschema.Validate("lane_report_review_candidate", mustJSONValue(candidate)); (err == nil) != complete {
				t.Fatalf("strict report candidate accepted=%v, want %v: %v", err == nil, complete, err)
			}
			report := reviewBlockFixture()
			report.InputCandidate = candidate
			if err := validateWorkerReviewBlock(report); (err == nil) != complete {
				t.Fatalf("worker report admitted=%v, want %v: %v", err == nil, complete, err)
			}
			if complete {
				type unshapedState WorkflowReviewState
				if want := mustJSONValue(unshapedState(state)); !bytes.Equal(raw, want) {
					t.Fatalf("complete readback bytes changed:\ngot %s\nwant %s", raw, want)
				}
			}
		})
	}
}

func TestRetainedUnidentifiedFindingReadback(t *testing.T) {
	for _, oracle := range []*WorkerOracleFinding{nil, {Classification: OracleClassificationFollowUp}} {
		finding := WorkerReviewFinding{Severity: "P2", Confidence: "high", Detail: "The recorded proof is incomplete.", Oracle: oracle}
		state := WorkflowReviewState{Findings: []WorkerReviewFinding{finding}, PublicationOutstanding: true}
		raw := mustJSONValue(state)
		if err := payloadschema.Validate("workflow_review_state", raw); err != nil {
			t.Fatalf("unidentified finding readback rejected: %v", err)
		}
		var view struct {
			Findings []map[string]json.RawMessage `json:"findings"`
		}
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"severity": finding.Severity, "confidence": finding.Confidence, "detail": finding.Detail}
		if oracle != nil {
			want["oracle"] = oracle
		}
		var expected map[string]json.RawMessage
		if err := json.Unmarshal(mustJSONValue(want), &expected); err != nil {
			t.Fatal(err)
		}
		if len(view.Findings) != 1 || !reflect.DeepEqual(view.Findings[0], expected) {
			t.Fatalf("finding invented or lost recorded fields: %s", raw)
		}
		type reportedFinding WorkerReviewFinding
		if !bytes.Equal(mustJSONValue(finding), mustJSONValue(reportedFinding(finding))) || reviewDigest(finding) != reviewDigest(reportedFinding(finding)) {
			t.Fatal("worker report bytes or digest differ from the unprojected finding")
		}
		if err := payloadschema.Validate("lane_report_review_finding", mustJSONValue(want)); err == nil {
			t.Fatal("strict report schema admitted an unidentified finding")
		}
		if err := validateWorkerReviewFinding(finding); err == nil {
			t.Fatal("worker report admission accepted an unidentified finding")
		}
	}
}

func TestRetainedFindingReadbackRejectsPartialIdentity(t *testing.T) {
	cases := map[string]func(*WorkerReviewFinding){
		"identity_only":    func(f *WorkerReviewFinding) { f.FindingID = "finding:known" },
		"status_only":      func(f *WorkerReviewFinding) { f.Status = "open" },
		"scope_only":       func(f *WorkerReviewFinding) { f.InScope = true },
		"blocking_only":    func(f *WorkerReviewFinding) { f.Blocking = true },
		"evidence_empty":   func(f *WorkerReviewFinding) { f.EvidenceRefs = []string{} },
		"closure_empty":    func(f *WorkerReviewFinding) { f.ClosureRefs = []string{} },
		"closure_claim":    func(f *WorkerReviewFinding) { f.ClosureRefs = []string{"evidence:closure"} },
		"missing_severity": func(f *WorkerReviewFinding) { f.Severity = "" },
		"bad_confidence":   func(f *WorkerReviewFinding) { f.Confidence = "certain" },
		"oversized_detail": func(f *WorkerReviewFinding) { f.Detail = strings.Repeat("x", 513) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			finding := WorkerReviewFinding{Severity: "P2", Confidence: "high", Detail: "The recorded proof is incomplete."}
			change(&finding)
			state := WorkflowReviewState{Findings: []WorkerReviewFinding{finding}, PublicationOutstanding: true}
			if err := payloadschema.Validate("workflow_review_state", mustJSONValue(state)); err == nil {
				t.Fatal("readback admitted a partial or malformed finding")
			}
		})
	}
}

func TestRetainedFindingReadbackFieldInventory(t *testing.T) {
	fields := map[string]bool{
		"Severity": true, "Confidence": true, "Detail": true, "Oracle": true,
		"FindingID": true, "Status": true, "InScope": true, "Blocking": true,
		"EvidenceRefs": true, "ClosureRefs": true,
	}
	shape := reflect.TypeFor[WorkerReviewFinding]()
	for i := range shape.NumField() {
		name := shape.Field(i).Name
		if !fields[name] {
			t.Fatalf("finding field %s needs an explicit recorded-presence rule", name)
		}
		delete(fields, name)
	}
	if len(fields) != 0 {
		t.Fatalf("finding readback rules name absent fields: %v", fields)
	}
}

func TestRetainedFindingBackfillReadbackPreservesFacts(t *testing.T) {
	db, _ := g32ReviewFixture(t)
	records := []workflowReviewBackfillRecord{
		{kind: WorkflowActionCompleted, raw: mustJSONValue(workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: "review:retained", WorkerCapabilityClass: "review"})},
		{kind: WorkerCompleted, raw: []byte(`{"attempt_id":"review:retained","review":{"verdict":"no_ship","findings":[{"severity":"P2","confidence":"high","detail":"The recorded proof is incomplete."}],"resolved_findings":[]}}`)},
	}
	if err := backfillWorkflowReviewWork(t.Context(), db, "work-history", records); err != nil {
		t.Fatal(err)
	}
	readStored := func() string {
		t.Helper()
		var raw string
		if err := db.QueryRow(`SELECT state FROM workflow_review_state WHERE work_id='work-history'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := readStored()
	state, err := readWorkflowReviewFacts(t.Context(), db, "work-history")
	if err != nil || len(state.Findings) != 1 || state.Findings[0].FindingID != "" || state.Findings[0].ClosureRefs != nil {
		t.Fatalf("retained finding changed: %+v, %v", state.Findings, err)
	}
	if err := payloadschema.Validate("workflow_review_state", mustJSONValue(state)); err != nil {
		t.Fatalf("retained finding readback rejected: %v", err)
	}
	if after := readStored(); after != before {
		t.Fatalf("readback rewrote stored facts:\n%s\n%s", before, after)
	}
}

func TestRetainedReviewCandidateRejectsInvalidKnownIdentity(t *testing.T) {
	for field, value := range map[string]string{"project_id": "?", "commit": "not-a-commit", "tree": "not-a-tree"} {
		t.Run(field, func(t *testing.T) {
			candidate := map[string]string{"project_id": "project-one", "commit": strings.Repeat("a", 40), "tree": strings.Repeat("b", 40)}
			candidate[field] = value
			raw := mustJSONValue(map[string]any{
				"candidate": candidate, "accepted_review": nil, "findings": []any{}, "continue_consumed": false,
				"dependencies_current": false, "publication_outstanding": true,
			})
			if err := payloadschema.Validate("workflow_review_state", raw); err == nil {
				t.Fatalf("readback admitted invalid known %s", field)
			}
		})
	}
}

func TestAcceptedTreeLessCandidateReadbackDoesNotRewriteFacts(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	const attempt = "implementation:without-tree"
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "dispatch_worker", WorkerAttemptID: attempt, WorkerCapabilityClass: "implementation"})
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: attempt, ObservedDelivery: &WorkerObservedDelivery{State: "clean", Commit: strings.Repeat("a", 40)}})
	appendEvent(WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: attempt})
	readStored := func() string {
		t.Helper()
		var raw string
		if err := db.QueryRow(`SELECT state FROM workflow_review_state WHERE work_id='work-history'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := readStored()
	for _, phase := range []string{"current", "backfilled"} {
		if phase == "backfilled" {
			if err := backfillWorkflowReviewFacts(t.Context(), db); err != nil {
				t.Fatal(err)
			}
		}
		state, err := readWorkflowReviewFacts(t.Context(), db, "work-history")
		if err != nil || state.Candidate == nil || state.Candidate.Commit != strings.Repeat("a", 40) || state.Candidate.Tree != "" {
			t.Fatalf("%s lost recorded identity: %+v, %v", phase, state.Candidate, err)
		}
		if err := payloadschema.Validate("workflow_review_state", mustJSONValue(state)); err != nil {
			t.Fatalf("%s review readback rejected: %v", phase, err)
		}
		if after := readStored(); after != before {
			t.Fatalf("%s readback changed retained facts:\n%s\n%s", phase, before, after)
		}
	}
}
