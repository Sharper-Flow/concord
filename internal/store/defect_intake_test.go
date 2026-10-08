package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests hold the CON-797 capture admission contract: a new public bug
// capture carries a defect_intake record, the core computes the same-shape
// sibling cluster inside the capture transaction, and a recurrent cluster is
// admitted only behind a completed workflow.research root cause whose snapshot
// covers the cluster. The work.created fold and the capture membership fold
// are the one shared owner, so every test drives them through the ordinary
// ApplyOperation route both public capture paths use.

// defectFixture opens one store whose scope holds two Products, each with one
// Project, so cross-product exclusion has a real second scope to match
// against.
func defectFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "defect-product-a", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product-a", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product A","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "defect-product-b", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product-b", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product B","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "defect-project-a", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project-a", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project A"}`)},
		{EventID: "defect-project-b", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project-b", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project B"}`)},
		{EventID: "defect-scope-a", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-a", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-a","project_id":"project-a","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "defect-scope-b", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-b", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-b","project_id":"project-b","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-a"): 0, VersionRef(SubjectProduct, "product-b"): 0, VersionRef(SubjectProject, "project-a"): 0, VersionRef(SubjectProject, "project-b"): 0}}); err != nil {
		t.Fatalf("seed defect fixture scope: %v", err)
	}
	return s
}

// defectIntakeJSON renders the intake record both public capture paths carry.
func defectIntakeJSON(shape string, related []string, rootCause string) map[string]any {
	if related == nil {
		related = []string{}
	}
	return map[string]any{"failure_shape": shape, "reproduction": "run the failing route and observe the refusal", "searched": "product-a work items and the public issue tracker", "related_defect_ids": related, "root_cause_work_id": rootCause}
}

// captureDefectWork appends the capture operation shape both public paths
// append: one work.created at the current payload version carrying the intake,
// followed by the capture membership replacement. The membership fold runs the
// admission owner, so the whole operation refuses or commits as one unit.
func captureDefectWork(t *testing.T, s *Store, workID, kind string, intake map[string]any, projectIDs ...string) error {
	t.Helper()
	if len(projectIDs) == 0 {
		projectIDs = []string{"project-a"}
	}
	fields := map[string]any{"work_kind": kind, "title": "Defect " + workID, "value_statement": "The captured defect needs admission", "priority": 1}
	if intake != nil {
		fields["defect_intake"] = intake
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	memberships := make([]workMembershipPayload, len(projectIDs))
	for i, project := range projectIDs {
		memberships[i] = workMembershipPayload{ProjectID: project, Role: map[bool]string{true: "primary", false: "secondary"}[i == 0]}
	}
	membershipPayload, err := json.Marshal(workMembershipsPayload{Memberships: memberships, ExpectedVersion: 1, ResultingVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2, 0).UTC()
	op := Operation{Events: []Event{
		{EventID: workID + ":create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 3, Payload: payload},
		{EventID: workID + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}
	err = ApplyOperation(context.Background(), s, op)
	assertFoldGuardEmpty(t, s)
	return err
}

// defectBlock reads the persisted classification the admission owner writes
// into intent_json, or nil when the item holds none.
func defectBlock(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	var intentJSON string
	if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id=?`, workID).Scan(&intentJSON); err != nil {
		t.Fatalf("read intent for %s: %v", workID, err)
	}
	var intent struct {
		Defect map[string]any `json:"defect"`
	}
	if err := json.Unmarshal([]byte(intentJSON), &intent); err != nil {
		t.Fatalf("decode intent for %s: %v", workID, err)
	}
	return intent.Defect
}

// defectBlockSiblings reads the core's persisted sibling snapshot, sorted.
func defectBlockSiblings(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	block := defectBlock(t, s, workID)
	if block == nil {
		return nil
	}
	raw, ok := block["sibling_ids"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if text, ok := entry.(string); ok {
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

// forceWorkLifecycleTransition records a lifecycle transition the way domain
// log replay folds it: the recorded from state is the item's current one, and
// the workflow-completion gate the live path enforces is a live admission
// rule. By the time a later capture reads the root cause item its completion
// is historical fact, which is exactly the state replay owns.
func forceWorkLifecycleTransition(t *testing.T, s *Store, workID, to, reason string) {
	t.Helper()
	ctx := context.Background()
	var version int64
	var from string
	if err := s.DatabaseForTesting().QueryRow(`SELECT version, lifecycle FROM work_items WHERE id=?`, workID).Scan(&version, &from); err != nil {
		t.Fatalf("read version for %s: %v", workID, err)
	}
	payload, err := json.Marshal(map[string]any{"from": from, "to": to, "reason": reason, "evidence_refs": []string{}, "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	op := Operation{Events: []Event{{EventID: "defect-force-" + workID + "-" + to, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
	if _, err := applyOperationTx(workflowReplayContext(ctx), tx, op, newFoldScope(tx), false); err != nil {
		t.Fatalf("force transition %s -> %s for %s: %v", from, to, workID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedOpenResearchRCA captures one research item carrying the intake for
// shape and pins workflow.research on it, leaving the lifecycle open.
func seedOpenResearchRCA(t *testing.T, s *Store, workID, shape string, related []string) string {
	t.Helper()
	if err := captureDefectWork(t, s, workID, "research", defectIntakeJSON(shape, related, "")); err != nil {
		t.Fatalf("capture research RCA %s: %v", workID, err)
	}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		definition, err := BuiltinWorkflowDefinitionForRef("workflow.research")
		if err != nil {
			return err
		}
		return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/" + workID, ActorClass: ActorOperator}, Now: time.Unix(3, 0).UTC()})
	}); err != nil {
		t.Fatalf("initialize research workflow for %s: %v", workID, err)
	}
	return workID
}

// seedCompletedResearchRCA leaves the open fixture completed: the root cause
// item a later recurrent bug capture names.
func seedCompletedResearchRCA(t *testing.T, s *Store, workID, shape string, related []string) string {
	t.Helper()
	seedOpenResearchRCA(t, s, workID, shape, related)
	forceWorkLifecycleTransition(t, s, workID, "completed", "root cause analysis recorded")
	return workID
}

// supersedeWorkForFixture supersede-closes one item behind a successor through
// the ordinary supersession event.
func supersedeWorkForFixture(t *testing.T, s *Store, workID, successor string) {
	t.Helper()
	ctx := context.Background()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"successor": successor, "superseded": workID, "reason": "fixture supersession", "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{Events: []Event{{EventID: "defect-supersede-" + workID, Kind: "work.superseded", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
	if err := ApplyOperation(ctx, s, op); err != nil {
		t.Fatalf("supersede %s behind %s: %v", workID, successor, err)
	}
}

func seedHistoricalV2Bug(t *testing.T, s *Store, workID, projectID string) {
	t.Helper()
	now := time.Unix(1, 0).UTC()
	memberships, err := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: projectID, Role: "primary"}}, ExpectedVersion: 1, ResultingVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{Events: []Event{
		{EventID: workID + ":create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"bug","title":"Historical unclassified bug","priority":1}`)},
		{EventID: workID + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: memberships},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}
	if err := ApplyOperation(context.Background(), s, op); err != nil {
		t.Fatalf("seed historical v2 bug %s: %v", workID, err)
	}
}

func failureText(err error) string {
	if err == nil {
		return ""
	}
	var failure *Failure
	if failureAs(err, &failure) {
		return string(failure.Kind) + " " + failure.Op + " " + failure.Detail + " | " + failure.RecoveryAction
	}
	return err.Error()
}

func workRowCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A new public bug capture that carries no defect_intake record refuses at the
// fold, naming the missing record, and leaves no work item behind.
func TestFirstBugCannotAssertMissingRootCause(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	err := captureDefectWork(t, s, "work-bug-phantom-rca", "bug", defectIntakeJSON("phantom-root-cause", nil, "work-missing-rca"))
	if err == nil {
		t.Fatal("first bug asserted a nonexistent root cause work item")
	}
	if count := workRowCount(t, s, "work-bug-phantom-rca"); count != 0 {
		t.Fatalf("refused capture left %d work rows", count)
	}
}

func TestResearchIntakeCannotAssertRootCausePrerequisite(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	err := captureDefectWork(t, s, "work-research-phantom-rca", "research", defectIntakeJSON("phantom-root-cause", nil, "work-missing-rca"))
	if err == nil {
		t.Fatal("research intake asserted a root cause prerequisite")
	}
}

func TestNativeBugCaptureRequiresDefectIntake(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	err := captureDefectWork(t, s, "work-bug-unclassified", "bug", nil)
	if err == nil {
		t.Fatal("unclassified bug capture was admitted")
	}
	text := failureText(err)
	if !strings.Contains(text, "defect_intake") {
		t.Fatalf("refusal %q does not name defect_intake", text)
	}
	if count := workRowCount(t, s, "work-bug-unclassified"); count != 0 {
		t.Fatalf("refused capture left %d work rows", count)
	}
}

// The first bug of a shape is admissible with its classification, and the
// persisted intent carries the core's sibling snapshot: empty for a first bug.
func TestFirstBugCaptureAdmitsAndPersistsClassification(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-first", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("first classified bug capture refused: %v", err)
	}
	block := defectBlock(t, s, "work-bug-first")
	if block == nil {
		t.Fatal("admitted capture persisted no defect classification")
	}
	if block["failure_shape"] != "upload-checksum-mismatch" {
		t.Fatalf("persisted failure_shape=%v", block["failure_shape"])
	}
	if siblings := defectBlockSiblings(t, s, "work-bug-first"); len(siblings) != 0 {
		t.Fatalf("first bug sibling snapshot=%v, want empty", siblings)
	}
}

// One earlier matching bug is already recurrence: the second capture refuses
// before any durable effect, names the shape, the sibling, and the recovery
// route, and leaves no work item.
func TestRecurrentBugCaptureRefusesNamingSiblingAndRoute(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-one", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("first bug capture refused: %v", err)
	}
	err := captureDefectWork(t, s, "work-bug-two", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, ""))
	if err == nil {
		t.Fatal("recurrent bug capture was admitted")
	}
	text := failureText(err)
	for _, needle := range []string{"upload-checksum-mismatch", "work-bug-one", "research", "root_cause_work_id"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("recurrence refusal %q does not name %s", text, needle)
		}
	}
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("recurrence refusal is untyped: %v", err)
	}
	if len(failure.CandidateIDs) == 0 || failure.CandidateIDs[0] != "work-bug-one" {
		t.Fatalf("recurrence refusal candidates=%v, want the sibling cluster", failure.CandidateIDs)
	}
	if count := workRowCount(t, s, "work-bug-two"); count != 0 {
		t.Fatalf("refused recurrent capture left %d work rows", count)
	}
}

// A recurrent bug capture is admitted when root_cause_work_id names a
// completed workflow.research item of the same shape whose snapshot covers the
// complete current cluster.
func TestRecurrentBugCaptureAdmittedBehindCompletedRCA(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-one", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("first bug capture refused: %v", err)
	}
	rca := seedCompletedResearchRCA(t, s, "work-rca-upload", "upload-checksum-mismatch", []string{"work-bug-one"})
	if err := captureDefectWork(t, s, "work-bug-two", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, rca)); err != nil {
		t.Fatalf("recurrent bug capture behind completed RCA refused: %v", err)
	}
	if siblings := defectBlockSiblings(t, s, "work-bug-two"); len(siblings) != 1 || siblings[0] != "work-bug-one" {
		t.Fatalf("admitted recurrent capture siblings=%v, want [work-bug-one]", siblings)
	}
}

// A research capture may carry the same record as a cluster RCA identity with
// no completed RCA anywhere: refusing it would enclose the cluster behind the
// very analysis that must exist first.
func TestResearchCaptureCarryingIntakeNeedsNoRCA(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-one", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("first bug capture refused: %v", err)
	}
	if err := captureDefectWork(t, s, "work-rca-open", "research", defectIntakeJSON("upload-checksum-mismatch", []string{"work-bug-one"}, "")); err != nil {
		t.Fatalf("research capture with intake refused: %v", err)
	}
	if block := defectBlock(t, s, "work-rca-open"); block == nil {
		t.Fatal("research capture persisted no cluster identity")
	}
	if siblings := defectBlockSiblings(t, s, "work-rca-open"); len(siblings) != 1 || siblings[0] != "work-bug-one" {
		t.Fatalf("research cluster snapshot=%v, want [work-bug-one]", siblings)
	}
}

// Kinds other than bug and research refuse the record: a task cannot smuggle
// a defect classification, and research without the record stays legal.
func TestOtherKindsRefuseDefectIntake(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	for _, kind := range []string{"task", "decision", "other"} {
		if err := captureDefectWork(t, s, "work-"+kind+"-intake", kind, defectIntakeJSON("upload-checksum-mismatch", nil, "")); err == nil {
			t.Fatalf("%s capture with defect_intake was admitted", kind)
		} else if !strings.Contains(failureText(err), "defect_intake") {
			t.Fatalf("%s refusal %q does not name defect_intake", kind, failureText(err))
		}
	}
	if err := captureDefectWork(t, s, "work-research-plain", "research", nil); err != nil {
		t.Fatalf("research capture without intake refused: %v", err)
	}
}

// Explicit related_defect_ids are validated: each must exist, be a bug, share
// a Product with the capture, not be the capture itself, and hold the same
// shape when it is already classified.
func TestExplicitRelatedDefectValidation(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	seedHistoricalV2Bug(t, s, "work-hist-a", "project-a")
	seedHistoricalV2Bug(t, s, "work-hist-b", "project-b")
	if err := captureDefectWork(t, s, "work-bug-shaped", "bug", defectIntakeJSON("login-token-expiry", nil, "")); err != nil {
		t.Fatalf("shaped bug capture refused: %v", err)
	}
	cases := []struct {
		name    string
		workID  string
		related []string
		needle  string
	}{
		{"missing id", "work-rel-missing", []string{"work-nope"}, "work-nope"},
		{"non-bug sibling", "work-rel-nonbug", []string{"work-rca-other"}, "bug"},
		{"cross-product sibling", "work-rel-cross", []string{"work-hist-b"}, "Product"},
		{"self sibling", "work-rel-self", []string{"work-rel-self"}, "itself"},
		{"different classified shape", "work-rel-shape", []string{"work-bug-shaped"}, "shape"},
	}
	if err := captureDefectWork(t, s, "work-rca-other", "research", defectIntakeJSON("other-shape", nil, ""), "project-a"); err != nil {
		t.Fatalf("research fixture capture refused: %v", err)
	}
	for _, tc := range cases {
		if err := captureDefectWork(t, s, tc.workID, "bug", defectIntakeJSON("login-token-expiry", tc.related, "")); err == nil {
			t.Fatalf("%s: related defect validation admitted the capture", tc.name)
		} else if !strings.Contains(failureText(err), tc.needle) {
			t.Fatalf("%s: refusal %q does not name %q", tc.name, failureText(err), tc.needle)
		}
		if count := workRowCount(t, s, tc.workID); count != 0 {
			t.Fatalf("%s: refused capture left %d work rows", tc.name, count)
		}
	}
	// The lawful union: one historical unclassified bug in the same Product is
	// an explicit sibling, and recurrence rules apply to it.
	if err := captureDefectWork(t, s, "work-rel-good", "bug", defectIntakeJSON("login-token-expiry", []string{"work-hist-a"}, "")); err == nil {
		t.Fatal("explicit historical sibling without root cause was admitted")
	} else if !strings.Contains(failureText(err), "work-hist-a") {
		t.Fatalf("explicit sibling refusal %q does not name work-hist-a", failureText(err))
	}
}

// Historical unclassified bugs join a cluster only through explicit
// related_defect_ids: the stored classification of a v2 bug is empty, and the
// union with a completed RCA admits the retry.
func TestHistoricalUnclassifiedBugThroughExplicitSiblingAndRCA(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	seedHistoricalV2Bug(t, s, "work-hist-a", "project-a")
	rca := seedCompletedResearchRCA(t, s, "work-rca-hist", "login-token-expiry", []string{"work-hist-a"})
	if err := captureDefectWork(t, s, "work-new-hist", "bug", defectIntakeJSON("login-token-expiry", []string{"work-hist-a"}, rca)); err != nil {
		t.Fatalf("explicit sibling behind completed RCA refused: %v", err)
	}
	if siblings := defectBlockSiblings(t, s, "work-new-hist"); len(siblings) != 1 || siblings[0] != "work-hist-a" {
		t.Fatalf("explicit sibling snapshot=%v, want [work-hist-a]", siblings)
	}
}

// assertRecurrenceDiagnostic holds the refusal contract every disqualifying
// recurrence branch satisfies: the branch's own failure kind and specific
// reason, plus the failure shape, the complete sibling total, the bounded
// candidates, and the complete research-then-retry route, rendered inside the
// public detail budget. It returns the checked detail for extra assertions.
func assertRecurrenceDiagnostic(t *testing.T, err error, wantKind FailureKind, reason, shape string, cluster []string) string {
	t.Helper()
	if err == nil {
		t.Fatal("recurrent bug capture was admitted")
	}
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("recurrence refusal is untyped: %v", err)
	}
	if failure.Kind != wantKind {
		t.Fatalf("refusal kind=%s, want %s (detail %q)", failure.Kind, wantKind, failure.Detail)
	}
	if failure.RecoveryAction != recurrenceResearchRouteInstruction {
		t.Fatalf("refusal recovery=%q, want the complete research route %q", failure.RecoveryAction, recurrenceResearchRouteInstruction)
	}
	if len(failure.Detail) > maxPublicRefusalDetailBytes {
		t.Fatalf("refusal detail measures %d bytes, want at most %d: %q", len(failure.Detail), maxPublicRefusalDetailBytes, failure.Detail)
	}
	needles := []string{reason, "failure_shape=" + shape, fmt.Sprintf("siblings=%d", len(cluster)), recurrenceResearchRouteInstruction, "candidates="}
	for _, needle := range needles {
		if !strings.Contains(failure.Detail, needle) {
			t.Fatalf("refusal detail %q does not carry %q", failure.Detail, needle)
		}
	}
	if !slices.Equal(failure.CandidateIDs, boundedDefectIDs(cluster)) {
		t.Fatalf("refusal candidates=%v, want the bounded sibling cluster %v", failure.CandidateIDs, boundedDefectIDs(cluster))
	}
	return failure.Detail
}

// Every disqualifying root cause state refuses through the one shared
// diagnostic: wrong kind, wrong shape, absent or wrong workflow family,
// cross-Product, open, cancelled, or superseded lifecycle, stale coverage that
// misses a cluster member, and a missing item each keep their own failure kind
// and specific reason while carrying the shape, the complete sibling total,
// the bounded candidates, and the research-then-retry route.
func TestRootCauseDisqualificationsRefuse(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-one", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("first bug capture refused: %v", err)
	}
	// The first covering RCA predates the second bug, which is exactly how a
	// later retry finds it stale once the cluster has grown.
	stale := seedCompletedResearchRCA(t, s, "work-rca-stale", "upload-checksum-mismatch", []string{"work-bug-one"})
	if err := captureDefectWork(t, s, "work-bug-two", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, stale)); err != nil {
		t.Fatalf("second bug behind its covering RCA refused: %v", err)
	}

	// A completed research RCA on a different shape.
	wrongShape := seedCompletedResearchRCA(t, s, "work-rca-wrongshape", "login-token-expiry", nil)
	// A completed research RCA on the same shape whose snapshot covers the
	// grown cluster.
	covering := seedCompletedResearchRCA(t, s, "work-rca-covering", "upload-checksum-mismatch", []string{"work-bug-one", "work-bug-two"})
	// A completed task item: the wrong kind of prerequisite.
	taskCause := "work-task-rca"
	if err := captureDefectWork(t, s, taskCause, "task", nil); err != nil {
		t.Fatalf("task fixture capture refused: %v", err)
	}
	forceWorkLifecycleTransition(t, s, taskCause, "completed", "completed as the wrong kind")
	// A completed research RCA that pins no workflow family at all.
	unpinned := "work-rca-unpinned"
	if err := captureDefectWork(t, s, unpinned, "research", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("unpinned research capture refused: %v", err)
	}
	forceWorkLifecycleTransition(t, s, unpinned, "completed", "completed without a pinned family")
	// A research item pinned to the generic workflow family rather than the
	// research family its kind defaults to.
	if err := captureDefectWork(t, s, "work-rca-generic", "research", defectIntakeJSON("upload-checksum-mismatch", []string{"work-bug-one", "work-bug-two"}, "")); err != nil {
		t.Fatalf("generic research capture refused: %v", err)
	}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		definition, err := BuiltinWorkflowDefinitionForRef("workflow.generic_one_off")
		if err != nil {
			return err
		}
		return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: "work-rca-generic", Definition: definition, Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/work-rca-generic", ActorClass: ActorOperator}, Now: time.Unix(3, 0).UTC()})
	}); err != nil {
		t.Fatalf("initialize generic workflow: %v", err)
	}
	forceWorkLifecycleTransition(t, s, "work-rca-generic", "completed", "completed on the wrong family")
	// A completed research RCA in the other Product.
	if err := captureDefectWork(t, s, "work-rca-otherproduct", "research", defectIntakeJSON("upload-checksum-mismatch", nil, ""), "project-b"); err != nil {
		t.Fatalf("cross-product research capture refused: %v", err)
	}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		definition, err := BuiltinWorkflowDefinitionForRef("workflow.research")
		if err != nil {
			return err
		}
		return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: "work-rca-otherproduct", Definition: definition, Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/work-rca-otherproduct", ActorClass: ActorOperator}, Now: time.Unix(3, 0).UTC()})
	}); err != nil {
		t.Fatalf("initialize cross-product research workflow: %v", err)
	}
	forceWorkLifecycleTransition(t, s, "work-rca-otherproduct", "completed", "completed in another Product")

	// An in-progress RCA: captured and pinned but never completed.
	inProgress := seedOpenResearchRCA(t, s, "work-rca-inprogress", "upload-checksum-mismatch", []string{"work-bug-one", "work-bug-two"})
	// A cancelled RCA: captured, pinned, then cancelled from needed.
	cancelled := seedOpenResearchRCA(t, s, "work-rca-cancelled", "upload-checksum-mismatch", []string{"work-bug-one", "work-bug-two"})
	forceWorkLifecycleTransition(t, s, cancelled, "cancelled", "cancelled before completion")
	// A superseded RCA: captured, pinned, then superseded from needed.
	superseded := seedOpenResearchRCA(t, s, "work-rca-superseded", "upload-checksum-mismatch", []string{"work-bug-one", "work-bug-two"})
	if err := captureDefectWork(t, s, "work-rca-successor", "research", defectIntakeJSON("successor-shape", nil, "")); err != nil {
		t.Fatalf("successor research capture refused: %v", err)
	}
	supersedeWorkForFixture(t, s, superseded, "work-rca-successor")

	cluster := []string{"work-bug-one", "work-bug-two"}
	cases := []struct {
		name      string
		rootCause string
		kind      FailureKind
		reason    string
	}{
		{"wrong kind", taskCause, KindInvalidPayload, "root cause work item " + taskCause + " is kind task, not research"},
		{"wrong shape", wrongShape, KindInvalidPayload, "root cause work item " + wrongShape + " carries failure_shape login-token-expiry, not upload-checksum-mismatch"},
		{"absent workflow family", unpinned, KindInvalidPayload, "root cause work item " + unpinned + " pins no workflow family, not workflow.research"},
		{"wrong workflow family", "work-rca-generic", KindInvalidPayload, "root cause work item work-rca-generic pins workflow family workflow.generic_one_off, not workflow.research"},
		{"cross product", "work-rca-otherproduct", KindInvalidPayload, "root cause work item work-rca-otherproduct is not in this capture's Product"},
		{"in progress", inProgress, KindInvalidPayload, "root cause work item " + inProgress + " is lifecycle needed, not completed"},
		{"cancelled", cancelled, KindInvalidPayload, "root cause work item " + cancelled + " is lifecycle cancelled, not completed"},
		{"superseded", superseded, KindInvalidPayload, "root cause work item " + superseded + " is lifecycle superseded, not completed"},
		{"stale coverage", stale, KindInvalidPayload, "root cause work item " + stale + " does not cover cluster sibling work-bug-two"},
		{"missing item", "work-rca-missing", KindProjectionNotFound, "root_cause_work_id work-rca-missing does not exist"},
	}
	for _, tc := range cases {
		err := captureDefectWork(t, s, "work-bug-three-"+sanitizeSlug(tc.name), "bug", defectIntakeJSON("upload-checksum-mismatch", nil, tc.rootCause))
		assertRecurrenceDiagnostic(t, err, tc.kind, tc.reason, "upload-checksum-mismatch", cluster)
	}
	// The covering RCA admits the same retry.
	if err := captureDefectWork(t, s, "work-bug-three-good", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, covering)); err != nil {
		t.Fatalf("covering RCA refused the recurrent capture: %v", err)
	}
}

func sanitizeSlug(text string) string {
	replacer := strings.NewReplacer(" ", "-", "(", "", ")", "")
	return replacer.Replace(strings.ToLower(text))
}

// A same-shape bug in another Product is not a sibling: the cluster is scoped
// to the capture's own Product.
func TestSameShapeOtherProductIsNotSibling(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-product-b", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, ""), "project-b"); err != nil {
		t.Fatalf("product-b bug capture refused: %v", err)
	}
	if err := captureDefectWork(t, s, "work-bug-product-a", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, ""), "project-a"); err != nil {
		t.Fatalf("same-shape capture in another Product refused as recurrent: %v", err)
	}
}

// Concurrent same-shape captures serialize on the one admission owner: exactly
// one first capture commits and every other goroutine's capture refuses on the
// sibling it can then see.
func TestConcurrentSameShapeCapturesAdmitExactlyOne(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	const captures = 4
	var mu sync.Mutex
	admitted, refused := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < captures; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := captureDefectWork(t, s, fmt.Sprintf("work-bug-race-%d", i), "bug", defectIntakeJSON("race-shape", nil, ""))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				admitted++
			} else if strings.Contains(failureText(err), "race-shape") {
				refused++
			}
		}(i)
	}
	wg.Wait()
	if admitted != 1 || refused != captures-1 {
		t.Fatalf("admitted=%d refused=%d, want exactly one admission and %d refusals", admitted, refused, captures-1)
	}
}

// A projection rebuild replays every capture admission from the log and
// reproduces the persisted classification and sibling snapshots byte for byte.
func TestRebuildReplaysDefectAdmissionDeterministically(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-one", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("first bug capture refused: %v", err)
	}
	rca := seedCompletedResearchRCA(t, s, "work-rca-upload", "upload-checksum-mismatch", []string{"work-bug-one"})
	if err := captureDefectWork(t, s, "work-bug-two", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, rca)); err != nil {
		t.Fatalf("recurrent capture behind RCA refused: %v", err)
	}
	before := map[string]string{}
	for _, workID := range []string{"work-bug-one", "work-rca-upload", "work-bug-two"} {
		var intentJSON string
		if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id=?`, workID).Scan(&intentJSON); err != nil {
			t.Fatal(err)
		}
		before[workID] = intentJSON
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild refused: %v", err)
	}
	for workID, want := range before {
		var intentJSON string
		if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id=?`, workID).Scan(&intentJSON); err != nil {
			t.Fatal(err)
		}
		if intentJSON != want {
			t.Fatalf("%s intent_json after rebuild:\n got %s\nwant %s", workID, intentJSON, want)
		}
	}
}

// A revision cannot erase the persisted classification and cannot turn an
// unclassified item into a bug, which would bypass capture admission.
func TestRevisePreservesClassificationAndRefusesBugConversion(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-bug-rev", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("classified bug capture refused: %v", err)
	}
	if err := captureDefectWork(t, s, "work-task-rev", "task", nil); err != nil {
		t.Fatalf("task capture refused: %v", err)
	}
	workVersion := func(workID string) int64 {
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	revise := func(workID, kind string, version int64) error {
		payload, err := json.Marshal(map[string]any{"title": "Revised " + workID, "value_statement": "Revised value", "kind": kind, "priority": 1, "urgency": "standard", "tags": []string{}, "workflow_type_ref": "", "reason": "revision reason", "evidence_refs": []string{}, "expected_version": version, "resulting_version": version + 1})
		if err != nil {
			t.Fatal(err)
		}
		op := Operation{Events: []Event{{EventID: "revise-" + workID + "-" + kind + "-" + fmt.Sprint(version), Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
		return ApplyOperation(context.Background(), s, op)
	}
	if err := revise("work-bug-rev", "bug", workVersion("work-bug-rev")); err != nil {
		t.Fatalf("revision of a classified bug refused: %v", err)
	}
	if block := defectBlock(t, s, "work-bug-rev"); block == nil || block["failure_shape"] != "upload-checksum-mismatch" {
		t.Fatalf("revision erased the defect classification: %+v", block)
	}
	if err := revise("work-task-rev", "bug", workVersion("work-task-rev")); err == nil {
		t.Fatal("revision converted an unclassified task into a bug")
	} else if !strings.Contains(failureText(err), "bug") {
		t.Fatalf("conversion refusal %q does not name the bug kind", failureText(err))
	}
	// The captured kind is immutable for a classified item: leaving the bug
	// kind would hide a prior defect from the sibling matching query.
	if err := revise("work-bug-rev", "task", workVersion("work-bug-rev")); err == nil {
		t.Fatal("revision moved a classified bug away from the bug kind")
	}
	if block := defectBlock(t, s, "work-bug-rev"); block == nil {
		t.Fatal("refused revision erased the classification")
	}
}

// Historical v1 and v2 work.created events replay unchanged: an unclassified
// v2 bug folds without refusal and holds no classification block.
func TestHistoricalWorkCreatedVersionsStayUnclassified(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	seedHistoricalV2Bug(t, s, "work-hist-v2", "project-a")
	if block := defectBlock(t, s, "work-hist-v2"); block != nil {
		t.Fatalf("historical v2 bug holds a classification block: %+v", block)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with historical events refused: %v", err)
	}
	if block := defectBlock(t, s, "work-hist-v2"); block != nil {
		t.Fatalf("historical v2 bug holds a classification block after rebuild: %+v", block)
	}
}

// An explicitly named sibling that is also the root cause item refuses: the
// root cause is a research item, and a related defect must be a bug.
func TestRootCauseWorkIDNotAlsoRelation(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	seedHistoricalV2Bug(t, s, "work-hist-a", "project-a")
	rca := seedCompletedResearchRCA(t, s, "work-rca-rel", "login-token-expiry", []string{"work-hist-a"})
	if err := captureDefectWork(t, s, "work-bug-rel-rca", "bug", defectIntakeJSON("login-token-expiry", []string{rca}, rca)); err == nil {
		t.Fatal("research root cause accepted as a related defect id")
	}
}
