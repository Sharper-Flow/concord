package agent

import (
	"context"
	"encoding/json"
	"os"
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
	t.Parallel()
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
			"base_sha":         baseSHA,
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

	first := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	second := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")
	claim("work-1", first, "work/vacate-1", "claim-vacate-1")

	if response := vacate(first, "vacate-key-1"); response.Outcome != OutcomeOK {
		t.Fatalf("first vacate response=%+v error=%+v", response, response.Error)
	}
	if response := vacate(first, "vacate-key-1"); response.Outcome != OutcomeOK || !response.Replayed {
		t.Fatalf("same-key vacate retry response=%+v error=%+v", response, response.Error)
	}

	// The second claim follows the first vacate, because a claim refuses while
	// the calling session still occupies another active worktree.
	claim("work-2", second, "work/vacate-2", "claim-vacate-2")

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

// TestSessionVacateReoccupiesSameWorktree pins the vacate event identity at
// the core boundary. One session claims one cross-Project work item in its
// primary Project, vacates toward the registered main checkout, resumes the
// same claimed worktree, and vacates again. This is a core-only test: Dispatch
// receives envelopes whose Directory and Worktree fields the test reassigns by
// hand, so no host move runs here and no event proves a landing. Work resume
// is read-only, and the replayed claim that stands in for it writes no event
// and no occupancy, so the worktree entry is active with an empty occupant
// before both vacates. The recorded vacate history is the only store
// projection that orders the two operations, so the event identity carries
// the ordinal of the relocation request: one relocation request of one work
// item by one session is the unit of identity, a same-key retry replays
// before the effect runs, and each payload records the derived destination
// the core wrote before the adapter's host move, never proof of landing.
func TestSessionVacateReoccupiesSameWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, repo1, baseSHA1 := worktreeDispatchFixture(t)

	// One work item holds membership in two Projects: after both relocations
	// the same work item is claimed in the second Project.
	secondProject := []store.Event{
		{EventID: "vacate-reoccupy-project-2", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Reoccupy Project"}`)},
		{EventID: "vacate-reoccupy-project-2-added", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-2","role":"secondary","reason":"vacate reoccupy fixture","expected_version":2,"resulting_version":3}`)},
		{EventID: "vacate-reoccupy-work-1-memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-2","role":"secondary"}],"expected_version":2,"resulting_version":3}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: secondProject, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProject, "project-2"): 0, store.VersionRef(store.SubjectProduct, "product-1"): 2, store.VersionRef(store.SubjectWorkItem, "work-1"): 2}}); err != nil {
		t.Fatal(err)
	}
	// The second Project registers its own repository clone: a canonical_path
	// locator binds one repository to one Project.
	repo2 := t.TempDir()
	gitRun(t, repo2, "init", "-b", "main")
	gitRun(t, repo2, "config", "user.email", "concord@example.invalid")
	gitRun(t, repo2, "config", "user.name", "Concord Worktree Test")
	if err := os.WriteFile(filepath.Join(repo2, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo2, "add", "README.md")
	gitRun(t, repo2, "commit", "-m", "fixture base")
	gitRun(t, repo2, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, repo2, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	baseSHA2 := gitRun(t, repo2, "rev-parse", "HEAD")
	if err := s.AddProjectLocator(ctx, "project-2", store.ProjectLocator{ID: "path-2", Kind: store.LocatorCanonicalPath, Value: repo2}, 1); err != nil {
		t.Fatal(err)
	}
	service2, _, grant2 := newAuthorizedService(t, s, "client-2", "human-1", []Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-2"}, store.ProjectResolution{ProjectID: "project-2"})

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion2, _, err := s.ScopeVersion(ctx, "project-2")
	if err != nil {
		t.Fatal(err)
	}

	type vacateMove struct {
		WorkID               string `json:"work_id"`
		ProjectID            string `json:"project_id"`
		SourceDirectory      string `json:"source_directory"`
		DestinationDirectory string `json:"destination_directory"`
	}
	dispatch := func(service *Service, operation string, env CallEnvelope, input map[string]any) Envelope {
		t.Helper()
		payload, _ := json.Marshal(input)
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: operation, Input: payload}, env)
		if dispatchErr != nil {
			t.Fatalf("%s dispatch err=%v", operation, dispatchErr)
		}
		return response
	}
	claimInput := func(projectID, baseSHA string, expectedVersion int64, key string) map[string]any {
		return map[string]any{"work_id": "work-1", "project_id": projectID, "base_sha": baseSHA, "expected_version": expectedVersion, "idempotency_key": key}
	}

	worktree1 := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	inWorktree := mutationEnvelope(grant, scopeVersion)
	inWorktree.Directory, inWorktree.Worktree = worktree1, worktree1

	// The session claims work-1 in its primary Project.
	claim := dispatch(service, "worktree_claim", mutationEnvelope(grant, scopeVersion), claimInput("project-1", baseSHA1, 3, "reoccupy-claim-1"))
	if claim.Outcome != OutcomeOK || claim.Replayed {
		t.Fatalf("claim work-1 in project-1 response=%+v error=%+v", claim, claim.Error)
	}

	// First vacate: the core derives the registered main checkout and
	// records the operation; the adapter's host move would follow and is
	// outside what Dispatch runs.
	first := dispatch(service, "session_vacate", inWorktree, map[string]any{"idempotency_key": "reoccupy-vacate-1"})
	if first.Outcome != OutcomeOK || first.Replayed {
		t.Fatalf("first vacate response=%+v error=%+v", first, first.Error)
	}
	var firstMove vacateMove
	if err := json.Unmarshal(first.Result, &firstMove); err != nil {
		t.Fatal(err)
	}
	if firstMove.WorkID != "work-1" || firstMove.ProjectID != "project-1" || firstMove.SourceDirectory != worktree1 || firstMove.DestinationDirectory != repo1 {
		t.Fatalf("first vacate move=%+v, want %s relocated to %s", firstMove, worktree1, repo1)
	}
	inMainCheckout := mutationEnvelope(grant, scopeVersion)
	inMainCheckout.Directory, inMainCheckout.Worktree = repo1, repo1

	// The retry of one unchanged vacate replays the recorded operation
	// before the effect runs.
	retry := dispatch(service, "session_vacate", inMainCheckout, map[string]any{"idempotency_key": "reoccupy-vacate-1"})
	if retry.Outcome != OutcomeOK || !retry.Replayed {
		t.Fatalf("same-key vacate retry response=%+v error=%+v", retry, retry.Error)
	}

	// Work resume stands in as the replayed claim that returns the session
	// to the claimed worktree. The core writes no event and no occupancy
	// for it; the host move the adapter would perform is outside Dispatch.
	resume := dispatch(service, "worktree_claim", inMainCheckout, claimInput("project-1", baseSHA1, 3, "reoccupy-claim-1"))
	if resume.Outcome != OutcomeOK || !resume.Replayed {
		t.Fatalf("worktree_claim resume response=%+v error=%+v", resume, resume.Error)
	}

	// Second vacate: the same session leaves the same worktree again, under
	// a new request identity.
	second := dispatch(service, "session_vacate", inWorktree, map[string]any{"idempotency_key": "reoccupy-vacate-2"})
	if second.Outcome != OutcomeOK || second.Replayed {
		t.Fatalf("second vacate response=%+v error=%+v", second, second.Error)
	}

	// The two vacate operations carry distinct durable events. Each payload
	// records the derived destination written before any host move, so
	// landed_directory pins the recorded target and is not proof of landing.
	type vacatedEvent struct {
		EventID string
		Payload struct {
			WorkID               string `json:"work_id"`
			ProjectID            string `json:"project_id"`
			SessionRef           string `json:"session_ref"`
			SourceDirectory      string `json:"source_directory"`
			DestinationDirectory string `json:"destination_directory"`
			LandedDirectory      string `json:"landed_directory"`
		}
	}
	var events []vacatedEvent
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT event_id, payload FROM domain_events WHERE kind='work.session_vacated' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var event vacatedEvent
		var raw string
		if err := rows.Scan(&event.EventID, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &event.Payload); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventID == events[1].EventID {
		t.Fatalf("session_vacated events=%+v, want two distinct vacate operations of one work item", events)
	}
	for i, event := range events {
		move := event.Payload
		if move.WorkID != "work-1" || move.ProjectID != "project-1" || move.SessionRef != grant.SessionRef || move.SourceDirectory != worktree1 || move.DestinationDirectory != repo1 || move.LandedDirectory != repo1 {
			t.Fatalf("vacate %d event=%+v, want the recorded operation from %s toward %s", i, event, worktree1, repo1)
		}
	}

	// Both vacates leave the original claim active with an empty occupant:
	// the worktree projection is identical before the two vacates.
	entries, err := s.WorktreeEntries(ctx, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ProjectID != "project-1" || entries[0].State != "active" || entries[0].Path != worktree1 || entries[0].OccupantSessionRef != "" {
		t.Fatalf("work-1 entries=%+v, want the original project-1 claim preserved active and unoccupied", entries)
	}

	// A client scoped to the other Project then claims the same work item:
	// the recorded occupancy holds nothing after both vacates, so the claim
	// passes and the second worktree is created for the same work ID.
	claim2Env := mutationEnvelope(grant2, scopeVersion2)
	claim2Env.AmbientProjectID = "project-2"
	crossClaim := dispatch(service2, "worktree_claim", claim2Env, claimInput("project-2", baseSHA2, 4, "reoccupy-claim-2"))
	if crossClaim.Outcome != OutcomeOK || crossClaim.Replayed {
		t.Fatalf("claim work-1 in project-2 response=%+v error=%+v", crossClaim, crossClaim.Error)
	}
	worktree2 := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-2", "work-1")
	var crossClaimResult struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(crossClaim.Result, &crossClaimResult); err != nil {
		t.Fatal(err)
	}
	if crossClaimResult.Path != worktree2 {
		t.Fatalf("project-2 claim path=%q, want %q", crossClaimResult.Path, worktree2)
	}
	entries, err = s.WorktreeEntries(ctx, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	byProject := map[string]store.WorktreeEntry{}
	for _, entry := range entries {
		byProject[entry.ProjectID] = entry
	}
	if len(entries) != 2 || byProject["project-1"].State != "active" || byProject["project-1"].Path != worktree1 || byProject["project-2"].State != "active" || byProject["project-2"].Path != worktree2 {
		t.Fatalf("work-1 entries=%+v, want the project-1 claim preserved beside the new project-2 claim", entries)
	}
}
