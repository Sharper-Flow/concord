package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkflowVerificationReport(t *testing.T) {
	commit := strings.Repeat("a", 40)
	valid := VerificationReport{Source: "local", Commit: commit, Result: "pass", RunRef: "run:one"}
	cases := []struct {
		name string
		edit func(*VerificationReport)
		ref  string
		want bool
	}{
		{"local", func(*VerificationReport) {}, "commit:" + commit, true},
		{"required ci", func(r *VerificationReport) { r.Source = "required_ci" }, "commit:" + commit, true},
		{"failed result is recordable", func(r *VerificationReport) { r.Result = "fail" }, "commit:" + commit, true},
		{"wrong source", func(r *VerificationReport) { r.Source = "other" }, "commit:" + commit, false},
		{"wrong result", func(r *VerificationReport) { r.Result = "unknown" }, "commit:" + commit, false},
		{"short commit", func(r *VerificationReport) { r.Commit = "abc" }, "commit:abc", false},
		{"independent subject", func(*VerificationReport) {}, "report:independent", true},
		{"short subject", func(*VerificationReport) {}, "x", false},
		{"subject with whitespace", func(*VerificationReport) {}, "report:bad ref", false},
		{"oversized subject", func(*VerificationReport) {}, strings.Repeat("x", 129), false},
		{"whitespace run ref", func(r *VerificationReport) { r.RunRef = " \t" }, "report:one", false},
		{"empty reference", func(r *VerificationReport) { r.RunRef = "" }, "commit:" + commit, false},
		{"oversized reference", func(r *VerificationReport) { r.RunRef = strings.Repeat("x", 513) }, "commit:" + commit, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := valid
			tc.edit(&report)
			if got := validateVerificationReport(tc.ref, &report) == nil; got != tc.want {
				t.Fatalf("report validity = %t, want %t", got, tc.want)
			}
		})
	}
	candidate := &ReviewCandidate{ProjectID: "project:one", Commit: commit}
	if !reportCovers(valid, candidate) || reportCovers(valid, nil) || reportCovers(valid, &ReviewCandidate{Commit: strings.Repeat("b", 40)}) {
		t.Fatal("report must cover exactly its recorded commit")
	}
	valid.Result = "fail"
	if reportCovers(valid, candidate) {
		t.Fatal("a failed report cannot cover a candidate")
	}
}

func TestVerificationReportBindingAttributesSubmittingOperation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE workflow_contracts(work_id TEXT, contract_version INTEGER, superseded_by INTEGER); INSERT INTO workflow_contracts VALUES('work:one',1,NULL)`); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	ref := "commit:" + commit
	report := VerificationReport{Source: "local", Commit: commit, Result: "pass", RunRef: "run:one"}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]json.RawMessage{
		"immutable_subject_ref": json.RawMessage(`"` + ref + `"`),
		"verification_report":   raw,
		"producer_id":           json.RawMessage(`"spoofed"`),
		"producer_run_ref":      json.RawMessage(`"spoofed"`),
		"producer_watermark":    json.RawMessage(`"spoofed"`),
	}
	request := WorkflowActionExecutionRequest{WorkID: "work:one", ActionID: "bind_evidence", EvidenceRefs: []string{ref}, PrincipalRef: "principal:one", OperationID: "operation:one", RequestID: "request:one", Now: time.Unix(1, 0)}
	events, err := workflowEvidenceBindingEvents(context.Background(), db, request, "actor:one", fields, "event:one", 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("report binding events = %+v, %v", events, err)
	}
	var payload workflowEvidenceBoundPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ProducerID != request.PrincipalRef || payload.ProducerRunRef != request.OperationID || payload.ProducerWatermark != request.RequestID || payload.VerificationReport == nil || *payload.VerificationReport != report {
		t.Fatalf("report binding did not attribute the submitting operation: %+v", payload)
	}
	var eventFields map[string]json.RawMessage
	if err := json.Unmarshal(events[0].Payload, &eventFields); err != nil {
		t.Fatal(err)
	}
	if _, present := eventFields["report_contract_version"]; present {
		t.Fatal("report binding must not carry a contract version")
	}
	for _, distinct := range []string{"report:one", "report:two"} {
		request.EvidenceRefs = []string{distinct}
		fields["immutable_subject_ref"] = json.RawMessage(`"` + distinct + `"`)
		if _, err := workflowEvidenceBindingEvents(context.Background(), db, request, "actor:one", fields, "event:one", 1); err != nil {
			t.Fatalf("independent report %q refused: %v", distinct, err)
		}
	}
	fields["verification_report"] = json.RawMessage(`{"source":"local","commit":"` + commit + `","result":"pass","run_ref":"run:one","project_id":"project:old"}`)
	if _, err := workflowEvidenceBindingEvents(context.Background(), db, request, "actor:one", fields, "event:one", 1); err == nil {
		t.Fatal("retired report fields must be rejected")
	}
}

func TestWorkflowVerificationReportsUseRecordedBinding(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE workflow_evidence_bindings(work_id TEXT, binding_seq INTEGER, payload TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE workflow_instances(work_id TEXT, definition_ref TEXT); INSERT INTO workflow_instances VALUES('work:one','workflow.implementation'),('work:research','workflow.research')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE workflow_contracts(work_id TEXT, contract_version INTEGER, superseded_by INTEGER); INSERT INTO workflow_contracts VALUES('work:one',1,NULL)`); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	ref := "commit:" + commit
	insert := func(work string, seq int, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workflow_evidence_bindings VALUES(?,?,?)`, work, seq, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	insert("work:one", 1, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": ref, "producer_run_ref": "worktree_verify:old"})
	insert("work:other", 2, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": ref, "verification_report": VerificationReport{Source: "local", Commit: commit, Result: "pass", RunRef: "elsewhere"}})
	read := func() []VerificationReport {
		t.Helper()
		reports, err := readVerificationReports(context.Background(), db, "work:one", ref)
		if err != nil {
			t.Fatal(err)
		}
		return reports
	}
	if reports := read(); len(reports) != 0 {
		t.Fatalf("old or other-work bindings qualified: %+v", reports)
	}
	candidate := &ReviewCandidate{Commit: commit}
	if err := workflowAcceptanceSubjectCurrent(context.Background(), db, "work:one", candidate, []string{ref}); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("old binding satisfied acceptance: %v", err)
	}
	if err := workflowAcceptanceSubjectCurrent(context.Background(), db, "work:research", candidate, []string{"finding:research"}); err != nil {
		t.Fatalf("research evidence was treated as a verification report: %v", err)
	}
	insert("work:one", 3, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": ref, "verification_report": VerificationReport{Source: "required_ci", Commit: commit, Result: "fail", RunRef: "run:failed"}})
	reports := read()
	if len(reports) != 1 || reportCovers(reports[0], candidate) {
		t.Fatalf("failed report qualified: %+v", reports)
	}
	if err := workflowAcceptanceSubjectCurrent(context.Background(), db, "work:one", candidate, []string{ref}); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("failed report satisfied acceptance: %v", err)
	}
	insert("work:one", 4, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": ref, "verification_report": VerificationReport{Source: "local", Commit: commit, Result: "pass", RunRef: "run:passed"}})
	reports = read()
	if len(reports) != 1 || !reportCovers(reports[0], candidate) || reportCovers(reports[0], &ReviewCandidate{Commit: strings.Repeat("b", 40)}) {
		t.Fatalf("recorded candidate coverage is wrong: %+v", reports)
	}
	if err := workflowAcceptanceSubjectCurrent(context.Background(), db, "work:one", candidate, []string{ref}); err != nil {
		t.Fatalf("matching passing report refused: %v", err)
	}
	otherRef := "report:independent"
	insert("work:one", 4, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": otherRef, "verification_report": VerificationReport{Source: "required_ci", Commit: commit, Result: "pass", RunRef: "run:independent"}})
	if err := workflowAcceptanceSubjectCurrent(context.Background(), db, "work:one", candidate, []string{ref, otherRef}); err != nil {
		t.Fatalf("independent reports for one candidate refused: %v", err)
	}
	if err := workflowAcceptanceSubjectCurrent(context.Background(), db, "work:one", &ReviewCandidate{Commit: strings.Repeat("b", 40)}, []string{ref}); !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("mixed recorded commits satisfied acceptance: %v", err)
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET superseded_by=2 WHERE work_id='work:one'; INSERT INTO workflow_contracts VALUES('work:one',2,NULL)`); err != nil {
		t.Fatal(err)
	}
	if reports := read(); len(reports) != 1 || !reportCovers(reports[0], candidate) {
		t.Fatalf("supersession discarded the recorded passing report: %+v", reports)
	}
	if err := workflowAcceptanceSubjectCurrent(context.Background(), db, "work:one", candidate, []string{ref}); err != nil {
		t.Fatalf("supersession discarded candidate coverage: %v", err)
	}
	insert("work:one", 5, map[string]any{"evidence_kind": "verification", "immutable_subject_ref": ref, "verification_report": VerificationReport{Source: "local", Commit: commit, Result: "fail", RunRef: "run:latest-failed"}})
	reports = read()
	if len(reports) != 1 || reportCovers(reports[0], candidate) {
		t.Fatalf("latest failed report must not resolve to an older pass: %+v", reports)
	}
}
