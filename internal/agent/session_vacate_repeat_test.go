package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// TestSessionVacateRepeatsWithinOneSession pins the vacate event identity
// against one session that vacates more than once. session_vacate carries no
// domain input, so mutationDigest yields one constant value per Project. An
// event_id derived from that digest alone collides on the second vacate, whose
// payload names a different worktree, and classifyEventIDConflict reports
// idempotency_conflict. Naming the work item in the id makes one relocation the
// unit of identity, so a second linked worktree records its own event and a
// same-key retry of the same relocation replays before the effect runs.
func TestSessionVacateRepeatsWithinOneSession(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _, baseSHA := worktreeDispatchFixture(t)

	secondWork := []store.Event{
		{EventID: "wt-dispatch-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Second Worktree","priority":1}`)},
		{EventID: "wt-dispatch-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: secondWork, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}

	claim := func(workID, worktreePath, branch, key string) {
		t.Helper()
		input, _ := json.Marshal(map[string]any{
			"work_id": workID, "project_id": "project-1",
			"branch": branch, "base_sha": baseSHA, "path": worktreePath,
			"expected_version": 2, "idempotency_key": key,
		})
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: input}, mutationEnvelope(grant, scopeVersion))
		if dispatchErr != nil || response.Outcome != OutcomeOK {
			t.Fatalf("claim %s response=%+v error=%+v err=%v", workID, response, response.Error, dispatchErr)
		}
	}

	vacate := func(worktreePath, key string) Envelope {
		t.Helper()
		env := mutationEnvelope(grant, scopeVersion)
		env.Worktree = worktreePath
		env.Directory = worktreePath
		input, _ := json.Marshal(map[string]any{"idempotency_key": key})
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "session_vacate", Input: input}, env)
		if dispatchErr != nil {
			t.Fatalf("vacate dispatch err=%v", dispatchErr)
		}
		return response
	}

	first := filepath.Join(t.TempDir(), "linked-wt-1")
	second := filepath.Join(t.TempDir(), "linked-wt-2")
	claim("work-1", first, "work/vacate-1", "claim-vacate-1")
	claim("work-2", second, "work/vacate-2", "claim-vacate-2")

	if response := vacate(first, "vacate-key-1"); response.Outcome != OutcomeOK {
		t.Fatalf("first vacate response=%+v error=%+v", response, response.Error)
	}
	if response := vacate(first, "vacate-key-1"); response.Outcome != OutcomeOK || !response.Replayed {
		t.Fatalf("same-key vacate retry response=%+v error=%+v", response, response.Error)
	}

	// One session vacates a second linked worktree after the first. This is a
	// different relocation, so it records its own event rather than colliding
	// with the first.
	repeat := vacate(second, "vacate-key-2")
	if repeat.Outcome != OutcomeOK {
		t.Fatalf("second vacate in one session response=%+v error=%+v", repeat, repeat.Error)
	}

	var events int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind=?`, "work.session_vacated").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("session_vacated event count=%d, want 2", events)
	}
}
