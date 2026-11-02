package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func seedAgentOverlapFixture(t *testing.T) (*store.Store, *Service, Authority, ed25519.PrivateKey, CallEnvelope, []byte) {
	t.Helper()
	return seedAgentOverlapFixtureWith(t, []agentcontract.Capability{"work_relate"})
}

// seedAgentOverlapFixtureWith builds the same unresolved-overlap fixture under
// a caller-chosen capability set, so a test may probe operations on tools other
// than concord_work_relate.
func seedAgentOverlapFixtureWith(t *testing.T, capabilities []agentcontract.Capability) (*store.Store, *Service, Authority, ed25519.PrivateKey, CallEnvelope, []byte) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, capabilities)
	events := []store.Event{
		{EventID: "overlap-agent-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Other work","priority":1,"governing_requirements":[]}`)},
		{EventID: "overlap-agent-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	// The pair's contracts bind the committed Domain registry at product-1's
	// designated knowledge home (law-folder-authority.md, Committed reads):
	// the fixture seeds the same committed home a fresh operator store
	// carries, then reads the committed registry through the production
	// Domain read for the binding's content hash and home Domain.
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	registry := committedProductRegistry(t, s)
	root := registry.RootDomainID
	hash := registry.ContentHash
	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-1")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		name  string
		query string
		args  []any
	}{
		{"write guard", `INSERT INTO write_guard(active) VALUES(1)`, nil},
		{"actor", `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','operator','now')`, []any{actorRef}},
		{"work-1 contract", `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'overlap','internal_sqlite','[]','[]','now',?,'[]','[]',1,'prototype_internal')`, []any{actorRef}},
		{"work-2 contract", `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-2',1,'overlap','internal_sqlite','[]','[]','now',?,'[]','[]',1,'prototype_internal')`, []any{actorRef}},
		{"bindings", `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES('work-1',1,'product-1',?,?),('work-2',1,'product-1',?,?)`, []any{root, hash, root, hash}},
		{"affected domains", `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES('work-1',1,?),('work-2',1,?)`, []any{root, root}},
		// CD-0183: a contract holds its Domain claim once the item's workflow
		// instance carries the execution-start fact, so the fixture gives both
		// sides a started instance. Without this the pair claims nothing and
		// derives no overlap to resolve.
		{"started instances", `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_started_at) VALUES('work-1','workflow.break_fix',?, 'repair','running','2026-08-19T00:00:00Z'),('work-2','workflow.break_fix',?,'repair','running','2026-08-19T00:00:00Z')`, []any{breakFixFixtureDigest(t), breakFixFixtureDigest(t)}},
		{"started lifecycles", `UPDATE work_items SET lifecycle='in_progress' WHERE id IN ('work-1','work-2')`, nil},
		// Only a write intersection blocks, so the pair must modify the same
		// Domain. A shared affected Domain alone derives no overlap.
		{"shared Domain writes", `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('work-1',1,?),('work-2',1,?)`, []any{root, root}},
		{"leave write", `DELETE FROM write_guard`, nil},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatalf("seed %s: %v", statement.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	input := []byte(`{"from_work_id":"work-1","to_work_id":"work-2","from_expected_version":2,"to_expected_version":2,"from_contract_version":1,"to_contract_version":1,"resolution_kind":"depends_on","reason":"sequence work","idempotency_key":"resolve-overlap-1"}`)
	return s, service, grant, privateKey, env, input
}

// TestResolveOverlapRoutineDispatchRecordsUnsigned proves the routine
// sequencing caller records its coordination answer unsigned: the
// recorded event and projection carry the exact direction, kind, contract,
// and version pins, and the routine dispatch bumps both endpoints one step.
// The governing-requirement scope cut's cross-scope refusal is the governing
// overlap test's own witness.
func TestResolveOverlapRoutineDispatchRecordsUnsigned(t *testing.T) {
	t.Parallel()
	s, service, _, _, env, input := seedAgentOverlapFixture(t)
	// The unsigned dispatch records the routine answer: the exact recorded
	// pins and the single-step endpoint version bumps.
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("routine overlap resolution outcome=%s error=%+v", response.Outcome, response.Error)
	}
	var fromID, toID, kind string
	var fromContract, toContract int64
	var invalidated any
	if err := s.DatabaseForTesting().QueryRow(`SELECT from_work_id,to_work_id,resolution_kind,from_contract_version,to_contract_version,invalidated_seq FROM workflow_overlap_resolutions`).Scan(&fromID, &toID, &kind, &fromContract, &toContract, &invalidated); err != nil {
		t.Fatal(err)
	}
	if fromID != "work-1" || toID != "work-2" || kind != "depends_on" || fromContract != 1 || toContract != 1 || invalidated != nil {
		t.Fatalf("recorded resolution=%s->%s %s contracts %d/%d invalidated=%v, want the unsigned current work-1->work-2 pair at contracts 1/1", fromID, toID, kind, fromContract, toContract, invalidated)
	}
	var eventKind, eventToID string
	var resulting, toResulting int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT o.resolution_kind,o.to_work_id,f.version,t.version FROM workflow_overlap_resolutions o JOIN effect_receipts r ON r.event_id=o.resolution_id AND r.effect_seq=o.event_seq JOIN work_items f ON f.id=o.from_work_id JOIN work_items t ON t.id=o.to_work_id WHERE r.kind='workflow.overlap_resolved'`).Scan(&eventKind, &eventToID, &resulting, &toResulting); err != nil {
		t.Fatal(err)
	}
	if eventKind != "depends_on" || eventToID != "work-2" || resulting != 3 || toResulting != 3 {
		t.Fatalf("resolution event kind=%s to=%s versions=%d/%d, want the unsigned sequencing event to work-2 at 3/3", eventKind, eventToID, resulting, toResulting)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM relations WHERE kind='depends_on' AND work_id_from='work-1' AND work_id_to='work-2' AND resolution_id IS NOT NULL`); got != 1 {
		t.Fatalf("resolution relation rows=%d, want 1", got)
	}
	if workVersion(t, s, "work-1") != 3 || workVersion(t, s, "work-2") != 3 {
		t.Fatal("routine resolution left the endpoints off the single-step versions")
	}
}
func TestResolveOverlapBlocksRemovesWorkFromReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, _, _, env, input := seedAgentOverlapFixture(t)
	// The peer has an execution-start claim but awaits its next work step.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET lifecycle='needed' WHERE id='work-2'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	for _, column := range tableColumns(t, s.DatabaseForTesting()) {
		switch column {
		case "blocked", "ready", "blocking", "is_blocked", "blocked_by":
			t.Fatalf("readiness must be derived, found stored column %q", column)
		}
	}
	before, err := s.QueryQ5(ctx, store.Q5Request{Product: "product-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 1 || before.Items[0].ID != "work-2" || deriveWorkBlocked(t, s, "work-2") {
		t.Fatalf("peer was not ready before resolution: %+v", before.Items)
	}
	input = []byte(strings.Replace(string(input), `"resolution_kind":"depends_on"`, `"resolution_kind":"blocks"`, 1))
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}
	resolved := dispatchMutation(t, s, service, request, env)
	if resolved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("routine blocks resolution=%+v", resolved.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM relations WHERE kind='blocks' AND work_id_from='work-1' AND work_id_to='work-2' AND resolution_id IS NOT NULL`); got != 1 {
		t.Fatalf("resolution blocks rows=%d, want 1", got)
	}
	after, err := s.QueryQ5(ctx, store.Q5Request{Product: "product-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 0 || !deriveWorkBlocked(t, s, "work-2") {
		t.Fatalf("resolution did not remove the blocked peer from ready: %+v", after.Items)
	}
	beforeEvents := agentCurrentEffects(t, s)
	replay := dispatchMutation(t, s, service, request, env)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || agentCurrentEffects(t, s) != beforeEvents {
		t.Fatalf("blocks resolution replay=%+v", replay)
	}
}

// breakFixFixtureDigest resolves the registered break-fix digest the overlap
// fixture's synthetic instance pins, so pin reads verify the fixture.
func breakFixFixtureDigest(t *testing.T) string {
	t.Helper()
	registered, ok := store.BuiltinWorkflowRegistry().Current("workflow.break_fix")
	if !ok {
		t.Fatal("workflow.break_fix v9 is unavailable")
	}
	return registered.Digest
}
