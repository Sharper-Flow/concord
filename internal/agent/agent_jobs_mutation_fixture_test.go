package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"testing"
	"time"

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
// work_relate. work_compact and work_epic are deliberately excluded
// because the AJ3/AJ4/AJ5 mutation corpus exercises only the four core
// mutation families; expanding the capability set invites unrelated
// recovery coupling to pass when a scenario ought to be refused.
func agentJobsMutationPM1Fixture(t *testing.T) (*store.Store, *Service, Grant, ed25519.PrivateKey, pm1fixture.Corpus) {
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
	ctx := context.Background()
	service := NewService(s.DB())
	service.Now = fixedTime
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{
		ClientRef: "client-mutation",
		KeyID:     "key-mutation",
		PublicKey: publicKey,
		Policy: TrustedClientPolicy{
			PrincipalRef: "human-operator",
			Capabilities: []Capability{"product_read", "work_define", "work_transition", "work_relate"},
			ProductScope: []string{"prod-alpha", "prod-beta"},
			ProjectScope: []string{"proj-web", "proj-api", "proj-shared"},
		},
	}); err != nil {
		t.Fatalf("RegisterTrustedClient: %v", err)
	}
	assertion := SignedAssertion{
		ClientRef:             "client-mutation",
		ClientVersion:         ManifestVersion,
		SessionRef:            "session-mutation",
		AgentRef:              "agent-engineer",
		Directory:             "/repo",
		Worktree:              "/repo-wt",
		RequestedProductID:    "prod-alpha",
		RequestedProjectIDs:   []string{"proj-web", "proj-api", "proj-shared"},
		RequestedCapabilities: []Capability{"product_read", "work_define", "work_transition", "work_relate"},
		IssuedAt:              fixedTime(),
		Nonce:                 "agent-jobs-mutation-nonce",
		SurfaceRange:          ManifestVersion + "-" + ManifestVersion,
		EnvelopeVersions:      "1.0",
		ManifestDigest:        ManifestDigest,
	}
	assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(assertion))
	grantReq := GrantRequest{
		Assertion:       assertion,
		SurfaceVersion:  ManifestVersion,
		EnvelopeVersion: "1.0",
		ExpiresAt:       fixedTime().Add(time.Hour),
	}
	grant, err := service.IssueGrant(ctx, grantReq)
	if err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}
	return s, service, grant, privateKey, corpus
}

// agentJobsMutationEnvelope extends agentJobsEnvelope with a scope_version
// resolved against the ambient project. Bindings call this so they can
// dispatch mutations through the same envelope the production runtime
// builds (mutation requests without a scope_version are refused with
// stale_context).
func agentJobsMutationEnvelope(t *testing.T, s *store.Store, grant Grant, ambientProject, selectedProduct string) CallEnvelope {
	t.Helper()
	env := agentJobsEnvelope(grant, ambientProject, selectedProduct)
	env.ScopeVersion = scopeVersionForProject(t, s, ambientProject)
	return env
}

// dispatchMutation executes a mutation through Dispatch. Like dispatchRead,
// a non-nil Go error is a fatal infrastructure failure. The returned
// envelope is what callers turn into a jobObservation. The store is
// passed so the runtime can hand it to ApplyOperationTx (and the
// preflight reads); the underlying connection is the same one
// service.DB() owns.
func dispatchMutation(t *testing.T, s *store.Store, service *Service, req InvokeRequest, env CallEnvelope, input ...[]byte) Envelope {
	t.Helper()
	if len(input) > 0 {
		req.Input = input[0]
	}
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
	if err := s.DB().QueryRow(`SELECT lifecycle, version FROM work_items WHERE id=?`, id).Scan(&lifecycle, &version); err != nil {
		t.Fatalf("read work_items[%s]: %v", id, err)
	}
	return lifecycle, version
}

// readTransitionEvents returns the number of work.transitioned events
// in the domain log for the given work id, plus the evidence_refs of the
// latest one. Bindings use this to assert atomicity of the durable
// effect (a single transition event with the requested evidence
// attached).
func readTransitionEvents(t *testing.T, s *store.Store, workID string) (count int, latestEvidenceRefs []string) {
	t.Helper()
	rows, err := s.DB().Query(`SELECT payload FROM domain_events WHERE kind='work.transitioned' AND subject_id=? ORDER BY seq ASC`, workID)
	if err != nil {
		t.Fatalf("read transition events for %s: %v", workID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan transition event payload: %v", err)
		}
		count++
		var payload struct {
			EvidenceRefs []string `json:"evidence_refs"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			latestEvidenceRefs = payload.EvidenceRefs
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return count, latestEvidenceRefs
}

// readRelationsFor returns only relations whose from/to/kind matches
// the filter — bindings use this to assert a single new edge landed
// without scanning the whole table.
func readRelationsFor(t *testing.T, s *store.Store, from, to, kind string) (count int) {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from=? AND work_id_to=? AND kind=?`, from, to, kind).Scan(&n); err != nil {
		t.Fatalf("read relations for %s->%s:%s: %v", from, to, kind, err)
	}
	return n
}

// deriveWorkBlocked reports whether the given work item has at least one
// active blocks relation from a needed/in_progress peer. It is the
// derivation the Q4/Q5 read queries use; bindings assert against the
// SAME query so that AJ5-add-dependency's derived_blocked=true
// assertion is genuinely observed, not read from a stored column.
func deriveWorkBlocked(t *testing.T, s *store.Store, workID string) bool {
	t.Helper()
	var blocked bool
	if err := s.DB().QueryRow(`
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
// already be declared in the schema (it is, in
// work_define_capture_input, work_transition_lifecycle_input, and
// work_relate_link_input).
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
