package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// agentJobsMutationPM1Fixture builds a PM1 fixture with full mutation
// capabilities and returns the ed25519 private key so bindings can sign
// host-approval assertions when scenarios demand an approval round-trip.
// The fixture is otherwise identical to agentJobsPM1Fixture so scenario
// observations are comparable.
//
// Capabilities granted: product_read, work_define, work_transition,
// work_relate. work_compact is deliberately excluded
// because the AJ3/AJ4/AJ5 mutation corpus exercises only the mutation
// families covered by these bindings.
func agentJobsMutationPM1Fixture(t *testing.T) (*store.Store, *Service, Authority, ed25519.PrivateKey, pm1fixture.Corpus) {
	t.Helper()
	corpus, err := pm1fixture.Load()
	if err != nil {
		t.Fatalf("pm1fixture.Load: %v", err)
	}
	s, err := pm1fixture.OpenTemp(t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.OpenTemp: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := pm1fixture.Seed(context.Background(), s, corpus); err != nil {
		t.Fatalf("pm1fixture.Seed: %v", err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-mutation", "human-operator", []agentcontract.Capability{"product_read", "work_define", "work_transition", "work_relate", "work_compact"}, []string{"prod-alpha", "prod-beta"}, []string{"proj-web", "proj-api", "proj-shared"}, store.ProjectResolution{ProjectID: "proj-web"})
	grant.SessionRef = "session-mutation"
	grant.AgentRef = "agent-engineer"
	privateKey := mustKey(t)
	return s, service, grant, privateKey, corpus
}

// agentJobsMutationEnvelope extends agentJobsEnvelope with a scope_version
// resolved against the ambient project. Bindings call this so they can
// dispatch mutations through the same envelope the production runtime
// builds (mutation requests without a scope_version are refused with
// stale_context).
func agentJobsMutationEnvelope(t *testing.T, s *store.Store, grant Authority, ambientProject string) CallEnvelope {
	t.Helper()
	env := agentJobsEnvelope(grant, ambientProject, "prod-alpha")
	env.ScopeVersion = scopeVersionForProject(t, s, ambientProject)
	return env
}

// dispatchMutation executes a mutation through Dispatch. Like dispatchRead,
// a non-nil Go error is a fatal infrastructure failure. The returned
// envelope is what callers turn into a jobObservation. The store is
// passed so the runtime can hand it to ApplyOperationTx (and the
// preflight reads); the underlying connection is the same one
// service.DatabaseForTesting() owns.
func dispatchMutation(t *testing.T, s *store.Store, service *Service, req InvokeRequest, env CallEnvelope) agentcontract.Envelope {
	t.Helper()
	resp, err := Dispatch(context.Background(), s, service, req, env)
	if err != nil {
		t.Fatalf("Dispatch %s.%s: %v", req.Tool, req.Operation, err)
	}
	return resp
}

// readWorkFromStore returns (lifecycle, version) for the given work id.
// Used by bindings to assert durable state after a mutation.
func readWorkFromStore(t *testing.T, s *store.Store, id string) (string, int64) {
	t.Helper()
	var lifecycle string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle, version FROM work_items WHERE id=?`, id).Scan(&lifecycle, &version); err != nil {
		t.Fatalf("read work_items[%s]: %v", id, err)
	}
	return lifecycle, version
}

// readTransitionEvents joins transition receipts to the current lifecycle's
// evidence. Bindings check exactly one acknowledged effect and its row content.
func readTransitionEvents(t *testing.T, s *store.Store, workID string) (count int, latestEvidenceRefs []string) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='work.transitioned' AND subject_type='work_item' AND subject_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT transition_evidence_refs FROM work_items WHERE id=?`, workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &latestEvidenceRefs); err != nil {
		t.Fatal(err)
	}
	if count > 0 {
		var matches bool
		if err := s.DatabaseForTesting().QueryRow(`SELECT EXISTS(SELECT 1 FROM work_items w JOIN effect_receipts r ON r.effect_seq=w.transition_seq AND r.subject_id=w.id WHERE w.id=? AND r.subject_type='work_item' AND r.kind='work.transitioned' AND r.actor=w.transition_actor)`, workID).Scan(&matches); err != nil || !matches {
			t.Fatalf("current transition has no matching receipt: matches=%v err=%v", matches, err)
		}
	}
	return count, latestEvidenceRefs
}

// deriveWorkBlocked reports whether the given work item has at least one
// active blocks relation from a needed/in_progress peer. It is the
// derivation the Q4/Q5 read queries use, not a stored blocked column.
func deriveWorkBlocked(t *testing.T, s *store.Store, workID string) bool {
	t.Helper()
	var blocked bool
	if err := s.DatabaseForTesting().QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM relations r
			JOIN work_items b ON b.id = r.work_id_from
			WHERE r.work_id_to = ?
			AND r.kind = 'blocks'
			AND b.lifecycle IN ('needed','in_progress')
		)`, workID).Scan(&blocked); err != nil {
		t.Fatalf("derive blocked for %s: %v", workID, err)
	}
	return blocked
}

// injectApproval merges {"approval":{"approval_ref":"..."}} into a
// strict-decoded mutation input. The input is otherwise opaque so we
// decode into map[string]any and re-emit; the schema validator runs
// after this and refuses unknown fields, so the approval block must
// already be declared in the operation's input schema.
func injectApproval(raw []byte, ref string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["approval"] = map[string]any{"approval_ref": ref}
	return json.Marshal(m)
}

// nonceForChallenge derives a deterministic but unique approval nonce
// from the challenge ref. The signed approval assertion is
// single-nonce per signature window so the agent can replay the
// captured nonce deterministically. The returned string is at least
// 16 characters long so the validator at
// internal/agent/authority.go:784 accepts it.
func nonceForChallenge(ref string) string {
	out := []byte("agent-jobs-approve-")
	if len(ref) > 64 {
		ref = ref[:64]
	}
	out = append(out, []byte(ref)...)
	for len(out) < 32 {
		out = append(out, byte('0'))
	}
	return string(out)
}
