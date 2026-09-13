package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// Repro against a copy of the live database: dispatch the operator-signed
// complete for work-ea46e62fe00c233f4be78900 as the live session.
func TestReproLiveDispatchComplete(t *testing.T) {
	path := os.Getenv("REPRO_DB")
	if path == "" {
		t.Skip("REPRO_DB not set")
	}
	const workID = "work-ea46e62fe00c233f4be78900"
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service := NewService(s)
	service.Now = func() time.Time { return time.Now().UTC() }
	service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "concord"}, nil
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "concord")
	if err != nil {
		t.Fatal(err)
	}
	worktree := "/home/jon/.local/share/concord/worktrees/concord/work-ea46e62fe00c233f4be78900"
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "repro-request-1", ClientRef: "opencode", SessionRef: "ses_f82187f14ffeK3OoOUhsH9HYLY", AgentRef: "concord-1", Directory: worktree, Worktree: worktree, AmbientProjectID: "concord", SelectedProductID: "concord", ScopeVersion: scopeVersion, ManifestDigest: ManifestDigest}
	inv := Invocation{ClientRef: env.ClientRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: ManifestDigest, RequiredCapability: "work_transition", ProductID: "concord", ProjectID: "concord"}
	grant, err := service.Authorize(ctx, inv)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	t.Logf("grant principal=%s session=%s agent=%s", grant.PrincipalRef, grant.SessionRef, grant.AgentRef)
	env.PrincipalRef = grant.PrincipalRef

	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": workID, "expected_version": version, "action_id": "complete", "fields": map[string]any{"impact_verdict": "non-breaking", "summary": "repro"}, "idempotency_key": "repro-complete-live"}
	raw, _ := json.Marshal(input)
	resp, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	t.Logf("first outcome=%s err=%+v", resp.Outcome, resp.Error)
	if resp.Error == nil || resp.Error.Kind != "approval_required" {
		t.Fatalf("expected approval_required, got %+v", resp.Error)
	}
	challengeRef, _ := resp.Error.Details["approval_ref"].(string)
	input["approval"] = map[string]any{"approval_ref": challengeRef}
	approvedRaw, _ := json.Marshal(input)
	scope := map[string]any{"product_id": "concord", "project_ids": []string{"concord"}, "work_ids": []string{workID}, "scope_version": scopeVersion}
	versions := map[string]any{"work": version}
	approvalEnv := env
	approvalEnv.RequestID = "repro-request-2"
	approvalEnv.HostApproval = &HostApprovalAssertion{ChallengeRef: challengeRef, RequestDigest: mutationDigest("concord_work_transition", "workflow_action", approvalEnv, approvedRaw), Scope: approvalScopeBindings(scope), Versions: approvalVersionBindings(versions), SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	resp, err = Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, approvalEnv)
	if err != nil {
		t.Fatalf("dispatch approved: %v", err)
	}
	t.Logf("approved outcome=%s err=%+v", resp.Outcome, resp.Error)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("approved complete refused: %+v", resp.Error)
	}
}
