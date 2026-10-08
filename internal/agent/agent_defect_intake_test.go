package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// These tests drive the CON-797 defect intake contract through the public
// agent capture verb concord_work_define.capture. The store fold is the
// admission owner; these tests hold the agent boundary: the input schema
// carries defect_intake, the kind rules refuse at decode, the recurrence
// refusal crosses the envelope with the sibling cluster and the research
// route, and a revision cannot bypass admission by kind conversion.

func dispatchDefectCapture(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion, requestID, input string) (Envelope, error) {
	t.Helper()
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = requestID
	return Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(input)}, env)
}

// dispatchDefectCaptureOK asserts the dispatch reached a typed envelope.
func dispatchDefectCaptureOK(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion, requestID, input string) Envelope {
	t.Helper()
	response, err := dispatchDefectCapture(t, s, service, grant, scopeVersion, requestID, input)
	if err != nil {
		t.Fatalf("capture dispatch failed: %v", err)
	}
	return response
}

// A public bug capture without defect_intake refuses at the agent boundary
// and records no work item.
func TestPublicCaptureBugWithoutIntakeRefused(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	response, dispatchErr := dispatchDefectCapture(t, s, service, grant, scopeVersion, "defect-agent-unclassified",
		`{"title":"Unspecified defect","value_statement":"The defect needs a shape","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-unclassified"}`)
	if dispatchErr != nil {
		if !strings.Contains(dispatchErr.Error(), "defect_intake") {
			t.Fatalf("dispatch refusal %q does not name defect_intake", dispatchErr.Error())
		}
	} else if response.Outcome != OutcomeError || response.Error == nil || !strings.Contains(response.Error.Message, "defect_intake") {
		t.Fatalf("unclassified bug capture outcome=%s error=%+v, want a refusal naming defect_intake", response.Outcome, response.Error)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("refused capture left %d work items", count)
	}
}

// The first public bug capture of a shape is admitted with its intake and the
// persisted classification names the shape.
func TestPublicCaptureBugWithIntakeAdmitted(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	response := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-first",
		`{"title":"Checksum mismatch","value_statement":"Uploads lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-first","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	if response.Outcome != OutcomeOK {
		t.Fatalf("classified first bug capture response=%+v", response)
	}
	workID := (*response.ChangedRefs)[0].ID
	var shape string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json,'$.defect.failure_shape') FROM work_items WHERE id=?`, workID).Scan(&shape); err != nil {
		t.Fatal(err)
	}
	if shape != "upload-checksum-mismatch" {
		t.Fatalf("persisted failure_shape=%q", shape)
	}
}

// A repeat public capture of the same shape refuses across the envelope,
// names the sibling and the research route, and carries the cluster as typed
// candidates.
func TestPublicCaptureRepeatShapeRefusedWithRoute(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	first := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-repeat-1",
		`{"title":"Checksum mismatch","value_statement":"Uploads lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-repeat-1","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first capture response=%+v", first)
	}
	sibling := (*first.ChangedRefs)[0].ID
	repeat := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-repeat-2",
		`{"title":"Checksum mismatch again","value_statement":"Uploads still lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-repeat-2","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests again","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	if repeat.Outcome != OutcomeError || repeat.Error == nil {
		t.Fatalf("repeat capture outcome=%s error=%+v, want typed refusal", repeat.Outcome, repeat.Error)
	}
	for _, needle := range []string{"upload-checksum-mismatch", sibling, "research", "root_cause_work_id"} {
		if !strings.Contains(repeat.Error.Message, needle) {
			t.Fatalf("repeat refusal message %q does not name %s", repeat.Error.Message, needle)
		}
	}
	if len(repeat.Error.Candidates) == 0 || repeat.Error.Candidates[0] != sibling {
		t.Fatalf("repeat refusal candidates=%v, want the sibling %s", repeat.Error.Candidates, sibling)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("refused repeat left %d work items, want 1", count)
	}
}

// A public research capture may carry the same record as its cluster RCA
// identity with no completed RCA anywhere.
func TestPublicCaptureResearchWithIntakeAdmitted(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	first := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-research-bug",
		`{"title":"Checksum mismatch","value_statement":"Uploads lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-research-bug","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	if first.Outcome != OutcomeOK {
		t.Fatalf("bug capture response=%+v", first)
	}
	sibling := (*first.ChangedRefs)[0].ID
	research := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-research",
		`{"title":"Why uploads lose checksums","value_statement":"The cluster needs a root cause","kind":"research","project_ids":["project-1"],"idempotency_key":"defect-agent-research","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":["`+sibling+`"]}}`)
	if research.Outcome != OutcomeOK {
		t.Fatalf("research capture with intake response=%+v", research)
	}
}

// Kinds other than bug and research refuse the record at the agent boundary.
func TestPublicCaptureTaskWithIntakeRefused(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	response := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-task-intake",
		`{"title":"Ordinary task","value_statement":"No defect shape here","kind":"task","project_ids":["project-1"],"idempotency_key":"defect-agent-task-intake","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	if response.Outcome != OutcomeError || response.Error == nil {
		t.Fatalf("task capture with intake outcome=%s error=%+v, want typed refusal", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "defect_intake") {
		t.Fatalf("refusal message %q does not name defect_intake", response.Error.Message)
	}
}

// A revision cannot convert an unclassified task into a bug: capture admission
// owns classification, and revise_intent carries no intake.
func TestPublicReviseCannotConvertTaskIntoBug(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	captured := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-convert-capture",
		`{"title":"Plain task","value_statement":"A task that stays a task","kind":"task","project_ids":["project-1"],"idempotency_key":"defect-agent-convert-capture"}`)
	if captured.Outcome != OutcomeOK {
		t.Fatalf("task capture response=%+v", captured)
	}
	workID := (*captured.ChangedRefs)[0].ID
	var expectedVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&expectedVersion); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "defect-agent-convert-revise"
	revise, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: json.RawMessage(`{"work_id":"` + workID + `","expected_version":` + strconv.FormatInt(expectedVersion, 10) + `,"title":"Converted defect","value_statement":"Now claimed a bug","kind":"bug","priority":1,"tags":[],"reason":"kind conversion","idempotency_key":"defect-agent-convert-revise"}`)}, env)
	if err != nil {
		t.Fatal(err)
	}
	if revise.Outcome != OutcomeError || revise.Error == nil {
		t.Fatalf("bug conversion revise outcome=%s error=%+v, want typed refusal", revise.Outcome, revise.Error)
	}
	if !strings.Contains(revise.Error.Message, "bug") {
		t.Fatalf("conversion refusal message %q does not name the bug kind", revise.Error.Message)
	}
	var kind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind FROM work_items WHERE id=?`, workID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "task" {
		t.Fatalf("refused conversion left kind=%q", kind)
	}
}

// Replaying the identical admitted capture returns the same work item and
// records no duplicate.
func TestPublicCaptureReplayRecordsNoDuplicate(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	input := `{"title":"Checksum mismatch","value_statement":"Uploads lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-replay","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`
	first := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-replay-1", input)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first capture response=%+v", first)
	}
	replay := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-replay-2", input)
	if replay.Outcome != OutcomeOK {
		t.Fatalf("replayed capture response=%+v", replay)
	}
	if !replay.Replayed {
		t.Fatal("identical capture did not replay")
	}
	if (*replay.ChangedRefs)[0].ID != (*first.ChangedRefs)[0].ID {
		t.Fatalf("replay work ID=%s, want %s", (*replay.ChangedRefs)[0].ID, (*first.ChangedRefs)[0].ID)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("replay left %d work items, want 1", count)
	}
}

// dispatchDefectRead dispatches one work_browse read and returns its result.
func dispatchDefectRead(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion, requestID, operation, input string) json.RawMessage {
	t.Helper()
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = requestID
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_browse", Operation: operation, Input: json.RawMessage(input)}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("%s read outcome=%s error=%+v", operation, response.Outcome, response.Error)
	}
	return response.Result
}

// The full-detail list read returns the persisted defect classification with
// the core's sibling snapshot, so a coordinator can retrieve the canonical
// cluster keys without reading storage.
func TestPublicListFullDetailCarriesDefectIntake(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	first := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-list-1",
		`{"title":"Checksum mismatch","value_statement":"Uploads lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-list-1","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	workID := (*first.ChangedRefs)[0].ID
	result := dispatchDefectRead(t, s, service, grant, scopeVersion, "defect-agent-list-read", "list",
		`{"product_id":"product-1","detail":"full"}`)
	var page struct {
		Items []struct {
			ID           string `json:"id"`
			DefectIntake *struct {
				FailureShape     string   `json:"failure_shape"`
				Reproduction     string   `json:"reproduction"`
				Searched         string   `json:"searched"`
				RelatedDefectIDs []string `json:"related_defect_ids"`
				RootCauseWorkID  string   `json:"root_cause_work_id"`
				SiblingIDs       []string `json:"sibling_ids"`
			} `json:"defect_intake"`
		} `json:"items"`
	}
	if err := json.Unmarshal(result, &page); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	for _, item := range page.Items {
		if item.ID != workID {
			continue
		}
		if item.DefectIntake == nil {
			t.Fatal("full-detail list item carries no defect_intake record")
		}
		if item.DefectIntake.FailureShape != "upload-checksum-mismatch" {
			t.Fatalf("list failure_shape=%q", item.DefectIntake.FailureShape)
		}
		if item.DefectIntake.Reproduction == "" || item.DefectIntake.Searched == "" {
			t.Fatal("list defect_intake drops the reproduction or searched text")
		}
		if item.DefectIntake.SiblingIDs == nil {
			t.Fatal("list defect_intake drops the sibling snapshot")
		}
		return
	}
	t.Fatalf("list read did not return work %s: %+v", workID, page.Items)
}

// The single-record scope read returns the same persisted record.
func TestPublicScopeCarriesDefectIntake(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	first := dispatchDefectCaptureOK(t, s, service, grant, scopeVersion, "defect-agent-scope-1",
		`{"title":"Checksum mismatch","value_statement":"Uploads lose their checksum","kind":"bug","project_ids":["project-1"],"idempotency_key":"defect-agent-scope-1","defect_intake":{"failure_shape":"upload-checksum-mismatch","reproduction":"upload the fixture and compare digests","searched":"the Product work list and the public tracker","related_defect_ids":[]}}`)
	workID := (*first.ChangedRefs)[0].ID
	result := dispatchDefectRead(t, s, service, grant, scopeVersion, "defect-agent-scope-read", "scope",
		`{"product_id":"product-1","work_id":"`+workID+`"}`)
	var readback struct {
		Work *struct {
			ID           string `json:"id"`
			DefectIntake *struct {
				FailureShape string   `json:"failure_shape"`
				SiblingIDs   []string `json:"sibling_ids"`
			} `json:"defect_intake"`
		} `json:"work"`
	}
	if err := json.Unmarshal(result, &readback); err != nil {
		t.Fatalf("decode scope result: %v", err)
	}
	if readback.Work == nil || readback.Work.ID != workID {
		t.Fatalf("scope read returned %+v", readback.Work)
	}
	if readback.Work.DefectIntake == nil || readback.Work.DefectIntake.FailureShape != "upload-checksum-mismatch" || readback.Work.DefectIntake.SiblingIDs == nil {
		t.Fatalf("scope work carries no complete defect record: %+v", readback.Work.DefectIntake)
	}
}
