package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestDispatchRejectsObsoleteEpicSurfaceAndGenericInitiativeCapture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_define"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	obsolete, obsoleteErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_epic", Operation: "create", Input: json.RawMessage(`{"title":"obsolete","value_statement":"must reject","project_ids":["project-1"],"idempotency_key":"obsolete"}`)}, env)
	if obsoleteErr == nil && (obsolete.Error == nil || obsolete.Error.Kind != "invalid_input" && obsolete.Error.Kind != "unauthorized") {
		t.Fatalf("obsolete Epic surface response=%+v err=%v", obsolete, obsoleteErr)
	}
	for _, kind := range []string{"epic", "initiative"} {
		response, responseErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(fmt.Sprintf(`{"title":"%s","value_statement":"must reject","kind":"%s","project_ids":["project-1"],"idempotency_key":"capture-%s"}`, kind, kind, kind))}, env)
		if responseErr == nil && (response.Error == nil || response.Error.Kind != "invalid_input") {
			t.Fatalf("generic %s capture response=%+v err=%v", kind, response, responseErr)
		}
	}
}

func TestDispatchReconcileLinksVerifiedOrphanWithoutSecondNote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	repo := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", repo).Run(); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "config", "user.email", "test@example.invalid").Run()
	_ = exec.Command("git", "-C", repo, "config", "user.name", "Concord Test").Run()
	content := "---\nconcord_work_id: work-orphan\nwork_type: task\ntitle: Orphan\ncompleted_at: 2026-08-07T00:00:00Z\noutcome_tag: shipped\nlesson_tags: [test]\nterminal_state: completed\npriority: 1\nsummary: Orphan summary\nproduct_ids: [product-1]\nproject_ids: [project-1]\ndomain_ids: []\ntag_ids: []\n---\n\nOrphan.\n"
	sum := sha256.Sum256([]byte(content))
	proofDigest := "sha256:" + hex.EncodeToString(sum[:])
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "orphan-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "orphan-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "orphan-product-project", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "orphan-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Orphan","priority":1}`)},
		{EventID: "orphan-work-project", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "orphan-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-orphan"): 0}}); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join(repo, ".concord/docs/work/2026-08-07-orphan-work.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "add", ".").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "commit", "--quiet", "-m", "orphan note").Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-1','project-1','canonical_path',?,?,'now','now'); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','locator-1'); DELETE FROM write_guard`, repo, repo); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"work_compact"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	grant.Worktree = "/unrelated-worktree"
	claimScope := `{"product_id":"product-1","project_ids":["project-1"],"work_ids":["work-orphan"],"scope_version":"","work_version":3,"content_digest":"` + proofDigest + `","home_project_id":"project-1","home_locator_id":"locator-1","head_ref":"HEAD"}`
	claim, err := store.ClaimStepAuthorized(ctx, s, store.ClaimRequest{OpID: "orphan-operation", WorkID: "work-orphan", WorkflowTypeRef: "concord.pm6.compaction", StepID: "git_proof", StepKind: store.StepCrossAuthority, AcceptedInputsDigest: "sha256:" + strings.Repeat("0", 64), AcceptedScopeSnapshot: claimScope, PrincipalRef: grant.PrincipalRef, Tool: "concord_work_compact", IdempotencyKey: "publish-key", RequestID: "publish-request", ObservedAt: fixedTime(), ContractDigest: agentcontract.ManifestDigest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: json.RawMessage(`{"operation_id":"orphan-operation","expected_operation_version":` + strconv.FormatInt(claim.AttemptEpoch, 10) + `,"expected_proof_digest":"` + proofDigest + `","idempotency_key":"reconcile-key"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	env.Worktree = "/unrelated-worktree"
	env.RequestID = "reconcile-request"
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("reconcile=%+v err=%v", response, err)
	}
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM archived_work WHERE id='work-orphan'`); count != 1 {
		t.Fatalf("orphan link count=%d", count)
	}
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='compaction_link.published' AND subject_id='work-orphan'`); count != 1 {
		t.Fatalf("compaction event count=%d", count)
	}
}

func TestDispatchIdempotentReplaySurvivesAmbientScopeDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Replay","value_statement":"Replay value","kind":"task","project_ids":["project-1"],"idempotency_key":"replay-key"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	first, err := Dispatch(ctx, s, service, request, env)
	if err != nil || first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	env.ScopeVersion = "drifted-scope-version"
	env.RequestID = "replay-after-drift"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("replay created effects: before=%d after=%d", eventsBefore, eventsAfter)
	}
	request.Input = json.RawMessage(`{"title":"Changed","value_statement":"Replay value","kind":"task","project_ids":["project-1"],"idempotency_key":"replay-key"}`)
	conflict, err := Dispatch(ctx, s, service, request, env)
	if err != nil || conflict.Outcome != agentcontract.OutcomeError || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestDispatchCrossProductCaptureKeepsCapabilityWithoutConsent(t *testing.T) {
	// CD-0243 D3/D7 retire cross-Product coordination consent, not capability.
	t.Parallel()
	ctx := context.Background()
	deniedStore, deniedService, deniedGrant := crossProductDispatchFixture(t, []agentcontract.Capability{"work_define"})
	deniedScope, _, err := deniedStore.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Cross","value_statement":"Cross value","kind":"task","project_ids":["project-1"],"idempotency_key":"cross-denied"}`)}
	denied, err := Dispatch(ctx, deniedStore, deniedService, request, mutationEnvelope(deniedGrant, deniedScope))
	if err != nil || denied.Outcome != agentcontract.OutcomeError || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Fatalf("cross-product denial=%+v err=%v", denied, err)
	}
	if count := countRows(t, deniedStore.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE title='Cross'`); count != 0 {
		t.Fatalf("denied capture created %d work items", count)
	}

	s, service, grant := crossProductDispatchFixture(t, []agentcontract.Capability{"work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request = InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Cross","value_statement":"Cross value","kind":"task","project_ids":["project-1"],"idempotency_key":"cross-approved"}`)}
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("cross approved response=%+v err=%v", approved, err)
	}
	// The derived cross-product set is authority bookkeeping: it binds the
	// stored idempotency snapshot, while resolved_scope carries only the
	// members the envelope law declares.
	var snapshot string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key=?`, "cross-approved").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, `"product_ids":["product-1","product-2"]`) {
		t.Fatalf("stored authorized scope snapshot=%s", snapshot)
	}
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE title='Cross'`); count != 1 {
		t.Fatalf("approved capture work count=%d", count)
	}
}

func crossProductDispatchFixture(t *testing.T, capabilities []agentcontract.Capability) (*store.Store, *Service, Authority) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "cross-product-1", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product One","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product Two","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Shared Project"}`)},
		{EventID: "cross-membership-1", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-membership-2", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-1","role":"secondary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-work-1", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Cross One","priority":1,"governing_requirements":[]}`)},
		{EventID: "cross-work-1-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Cross Two","priority":1,"governing_requirements":[]}`)},
		{EventID: "cross-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, []string{"product-1", "product-2"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant
}

func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func mutationDispatchFixture(t *testing.T, capabilities []agentcontract.Capability) (*store.Store, *Service, Authority, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "fixture-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("product.created"), Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "fixture-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "fixture-product-project", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("product_project.added"), Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "fixture-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Work","priority":1,"governing_requirements":[]}`)},
		{EventID: "fixture-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.memberships_replaced"), Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	privateKey := mustKey(t)
	return s, service, grant, privateKey
}

func mutationEnvelope(grant Authority, scopeVersion string) CallEnvelope {
	return CallEnvelope{SchemaVersion: "1.0", RequestID: "dispatcher-request", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func usedCount(t *testing.T, db *sql.DB, query, arg string) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, arg).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func workVersion(t *testing.T, s *store.Store, id string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func workLifecycle(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	return lifecycle
}
