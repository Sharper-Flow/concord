package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func captureCorrectionSnapshot(t *testing.T, s *store.Store) [6]int64 {
	t.Helper()
	var state [6]int64
	for i, query := range []string{
		`SELECT count(*) FROM work_items`,
		`SELECT count(*) FROM relations`,
		`SELECT count(*) FROM idempotency_records`,
		`SELECT count(*) FROM effect_receipts`,
		`SELECT last FROM store_sequences WHERE name='effect'`,
		`SELECT count(*) FROM work_projects`,
	} {
		if err := s.DatabaseForTesting().QueryRow(query).Scan(&state[i]); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func TestCaptureExternalRefInputCorrection(t *testing.T) {
	s, service, grant, scope := captureTaskFixture(t)
	input := map[string]any{
		"title": "New task", "value_statement": "A distinct task", "kind": "task",
		"project_ids": []string{"project-1"}, "external_ref": "tracker:46",
		"raised_from_work_id": "work-parent", "idempotency_key": "capture-correction",
	}
	invoke := func(scope string) Envelope {
		t.Helper()
		return dispatchCapture(t, s, service, grant, scope, "capture-correction-request", string(retryJSON(input)))
	}
	before := captureCorrectionSnapshot(t, s)
	refused := invoke(scope)
	if refused.Error == nil || refused.Error.Kind != "invalid_input" || refused.Error.EffectState != EffectNone || refused.Error.RetrySafe || refused.Error.RecoveryAction.Kind != "correct_request" || refused.Error.ExternalRefConflict != nil {
		t.Fatalf("no-owner correction refusal=%+v", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, "omit raised_from_work_id") || !strings.Contains(refused.Error.Message, "same idempotency_key") {
		t.Fatalf("missing input/key guidance: %+v", refused.Error)
	}
	if _, err := refused.Encode(); err != nil {
		t.Fatal(err)
	}
	if after := captureCorrectionSnapshot(t, s); after != before {
		t.Fatalf("no-owner refusal changed work/relation/key/receipt/sequence/membership: %v -> %v", before, after)
	}
	delete(input, "raised_from_work_id")
	if stale := invoke("stale-scope"); stale.Error == nil || stale.Error.Kind != "stale_context" {
		t.Fatalf("corrected input bypassed scope gate: %+v", stale)
	}
	if after := captureCorrectionSnapshot(t, s); after != before {
		t.Fatalf("scope refusal mutated state: %v -> %v", before, after)
	}
	corrected := invoke(scope)
	if corrected.Outcome != OutcomeOK || corrected.Replayed || corrected.ChangedRefs == nil || len(*corrected.ChangedRefs) != 1 {
		t.Fatalf("same-key corrected capture=%+v", corrected)
	}
	committed := captureCorrectionSnapshot(t, s)
	if committed[0] != before[0]+1 || committed[1] != before[1] || committed[2] != before[2]+1 || committed[5] != before[5]+1 {
		t.Fatalf("corrected capture effects: %v -> %v", before, committed)
	}
	if replay := invoke(scope); replay.Outcome != OutcomeOK || !replay.Replayed || (*replay.ChangedRefs)[0].ID != (*corrected.ChangedRefs)[0].ID {
		t.Fatalf("corrected exact replay=%+v", replay)
	}
	input["title"] = "Different digest"
	if conflict := invoke(scope); conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" || conflict.Error.RecoveryAction.Kind != "retry_same_request" {
		t.Fatalf("committed key accepted changed input: %+v", conflict.Error)
	}
	if after := captureCorrectionSnapshot(t, s); after != committed {
		t.Fatalf("replay/conflict changed capture effects: %v -> %v", committed, after)
	}
}

func TestCaptureExternalRefCorrectionRechecksLiveOwner(t *testing.T) {
	s, service, grant, scope := captureTaskFixture(t)
	input := map[string]any{
		"title": "New task", "value_statement": "A distinct task", "kind": "task",
		"project_ids": []string{"project-1"}, "external_ref": "tracker:47",
		"raised_from_work_id": "work-parent", "idempotency_key": "capture-owner-race",
	}
	invoke := func() Envelope {
		t.Helper()
		return dispatchCapture(t, s, service, grant, scope, "capture-owner-request", string(retryJSON(input)))
	}
	if refused := invoke(); refused.Error == nil || refused.Error.RecoveryAction.Kind != "correct_request" {
		t.Fatalf("no-owner refusal=%+v", refused)
	}
	owner := dispatchCapture(t, s, service, grant, scope, "new-owner-request", `{"title":"Owner","value_statement":"Own the reference","kind":"task","project_ids":["project-1"],"external_ref":"tracker:47","idempotency_key":"new-owner"}`)
	if owner.Outcome != OutcomeOK {
		t.Fatalf("new owner capture=%+v", owner)
	}
	before := captureCorrectionSnapshot(t, s)
	for _, acknowledgement := range []string{"", "work-parent"} {
		input["raised_from_work_id"] = acknowledgement
		if acknowledgement == "" {
			delete(input, "raised_from_work_id")
		}
		collision := invoke()
		if collision.Error == nil || collision.Error.Kind != "operation_conflict" || collision.Error.RecoveryAction.Kind != "reconcile_operation" || collision.Error.ExternalRefConflict == nil || collision.Error.ExternalRefConflict.ExistingWorkID != (*owner.ChangedRefs)[0].ID || collision.Error.ExternalRefConflict.ExternalRef != "tracker:47" {
			t.Fatalf("corrected input bypassed new owner: %+v", collision.Error)
		}
		if after := captureCorrectionSnapshot(t, s); after != before {
			t.Fatalf("collision mutated state: %v -> %v", before, after)
		}
	}
}
