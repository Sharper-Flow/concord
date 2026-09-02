package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// workItemProjectionVersion reads the authoritative projection version a
// mutation's expected_version pin is checked against.
func workItemProjectionVersion(t *testing.T, s *store.Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatalf("read projection version for %s: %v", workID, err)
	}
	return version
}

// TestReadPublishesProjectionVersion asserts that the version a read returns is
// the version a mutation accepts. summary() published the constant 1 for every
// work item, so a caller that pinned expected_version from a read conflicted on
// any work item past its first folded event.
func TestReadPublishesProjectionVersion(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")

	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_browse",
		Operation: "list",
		Input:     json.RawMessage(`{"product_id":"prod-alpha","page":{"cursor":null,"limit":50}}`),
	}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("list outcome=%s err=%+v", resp.Outcome, resp.Error)
	}

	var result struct {
		Items []struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal list result: %v", err)
	}
	if len(result.Items) == 0 {
		t.Fatal("fixture returned no work items")
	}

	compared := 0
	for _, item := range result.Items {
		want := workItemProjectionVersion(t, s, item.ID)
		if want <= 1 {
			continue
		}
		compared++
		if item.Version != want {
			t.Fatalf("read published version %d for %s, projection holds %d", item.Version, item.ID, want)
		}
	}
	if compared == 0 {
		t.Fatal("no fixture work item folded more than one event, so the constant is untested")
	}
}

// transitionFixture captures one work item on the implementation workflow and
// returns it at its live projection version, which is the version its first
// workflow action must pin.
func transitionFixture(t *testing.T) (*store.Store, *Service, CallEnvelope, string) {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	capture := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Version pin work","value_statement":"Pin the live version","kind":"task","project_ids":["project-1"],"workflow_type_ref":"workflow.implementation","idempotency_key":"capture-version-pin"}`)}
	captured, err := Dispatch(context.Background(), s, service, capture, env)
	if err != nil || captured.Outcome != OutcomeOK {
		t.Fatalf("capture response=%+v err=%v", captured, err)
	}
	return s, service, env, (*captured.ChangedRefs)[0].ID
}

// TestVersionConflictOnExistingWorkMarshals asserts that a stale pin against an
// existing work item returns the typed refusal the core decided. The workflow
// preflight declared the subject absent, runtime dropped the absent subject's
// current version, and the envelope then failed its own coupling check, so the
// caller received a transport-shaped operation_conflict with effect_state
// possible in place of a readable version_conflict.
func TestVersionConflictOnExistingWorkMarshals(t *testing.T) {
	s, service, env, workID := transitionFixture(t)

	current := workItemProjectionVersion(t, s, workID)
	stale := current + 1

	action := InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "workflow_action",
		Input: json.RawMessage(`{"work_id":"` + workID + `","expected_version":` +
			jsonInt(stale) + `,"action_id":"record_proposal","idempotency_key":"stale-pin-marshals"}`),
	}
	resp, err := Dispatch(context.Background(), s, service, action, env)
	if err != nil {
		t.Fatalf("Dispatch workflow_action: %v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("stale pin was accepted: outcome=%s", resp.Outcome)
	}
	if resp.Origin != OriginCore {
		t.Fatalf("refusal came from %s, so the core envelope did not marshal: %+v", resp.Origin, resp.Error)
	}
	if resp.Error.Kind != "version_conflict" {
		t.Fatalf("kind=%s want version_conflict: %+v", resp.Error.Kind, resp.Error)
	}
	if len(resp.Error.CurrentVersions) == 0 {
		t.Fatal("version_conflict carried no current_versions, so the caller cannot recover the live version")
	}
	found := false
	for _, ref := range resp.Error.CurrentVersions {
		if ref.ID == workID && ref.Version == jsonInt(current) {
			found = true
		}
	}
	if !found {
		t.Fatalf("current_versions did not carry %s at %d: %+v", workID, current, resp.Error.CurrentVersions)
	}
}

// TestVersionConflictOnMissingWorkIsUnknownScope asserts that a pin against a
// work item that does not exist refuses as unknown scope. A version conflict
// cannot describe a subject that holds no version, and the envelope refuses
// exactly that pairing.
func TestVersionConflictOnMissingWorkIsUnknownScope(t *testing.T) {
	s, service, env, _ := transitionFixture(t)

	action := InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "workflow_action",
		Input:     json.RawMessage(`{"work_id":"work-does-not-exist","expected_version":3,"action_id":"record_proposal","idempotency_key":"missing-subject-pin"}`),
	}
	resp, err := Dispatch(context.Background(), s, service, action, env)
	if err != nil {
		t.Fatalf("Dispatch workflow_action: %v", err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("pin against a missing work item was accepted: outcome=%s", resp.Outcome)
	}
	if resp.Origin != OriginCore {
		t.Fatalf("refusal came from %s, so the core envelope did not marshal: %+v", resp.Origin, resp.Error)
	}
	if resp.Error.Kind == "version_conflict" {
		t.Fatalf("a missing subject refused as version_conflict: %+v", resp.Error)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
