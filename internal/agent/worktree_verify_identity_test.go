package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorktreeVerifyNewRequestRunsAgain(t *testing.T) {
	t.Parallel()
	for _, newEpoch := range []bool{false, true} {
		name := "same_refine_epoch"
		if newEpoch {
			name = "new_refine_epoch"
		}
		t.Run(name, func(t *testing.T) {
			s, service, grant, _, _, _ := tiersFixture(t)
			now := fixedTime().Add(time.Second)
			service.Now = func() time.Time { return now }
			// Current accepted Review admits delivery; Review-directed
			// correction obtains a fresh refine epoch on the second leg.
			pinned := legacyApprovalPolicyDefinitionForRef(t, "workflow.implementation")
			grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
			ownerRef, ownerRefErr := store.WorkflowActorRef(grantActor)
			if ownerRefErr != nil {
				t.Fatal(ownerRefErr)
			}
			if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
				return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: "work-2", Definition: pinned, Actor: grantActor, Now: fixedTime()})
			}); err != nil {
				t.Fatal(err)
			}
			// The fixture parks the instance at the producer step under the
			// approved contract and predicate the independent verdict answers
			// to, the fold-guarded fixture pattern for fold-only tables.
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
				UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-2';
				INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains) VALUES('work-2',1,'approved verify objective','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal','[]');
				INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-2',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:verify-identity","immutable_subject_ref":"commit:verify-identity","expected_result":"pass"}');
				DELETE FROM fold_guard`, ownerRef); err != nil {
				t.Fatal(err)
			}
			// The producer pass opens with its fenced start, and its declared
			// delivery enters refine, so the refinement step opens with no
			// start of its own and the verdict route reads the execution
			// production it needs.
			verifyIdentityAction(t, s, "start_execution", map[string]any{}, grantActor, now)
			seedCurrentWorkflowDomainOnce(t, s)
			reviewService, _, reviewGrant := newAuthorizedService(t, s, "client-o-review", grant.PrincipalRef, []Capability{"work_transition", "worker_dispatch"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
			reviewService.Now = func() time.Time { return now }
			fixture := oAgentFixture(t, s, reviewService, mutationEnvelope(reviewGrant, mustScopeVersion(t, s)), "work-2")
			oAgentNativeJob(t, fixture)
			oAgentReview(t, fixture, "ready_for_final_proof", true)
			integration := oAgentVerify(t, fixture, []string{"go", "test", "./..."})
			verifyIdentityAction(t, s, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": integration}, grantActor, now)
			verifyIdentityDelivery(t, s, grantActor, "evidence:verify-identity-enter-refine", now)
			startRefine := func(key string) {
				t.Helper()
				var version int64
				if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-2'`).Scan(&version); err != nil {
					t.Fatal(err)
				}
				response := tiersInvoke(t, s, service, grant, "concord_work_transition", "workflow_action", map[string]any{
					"work_id": "work-2", "expected_version": version, "action_id": "checkpoint_refine", "fields": map[string]any{}, "idempotency_key": key,
				})
				if response.Outcome != OutcomeOK {
					t.Fatalf("start refine: %+v", response.Error)
				}
			}
			startRefine("verify-refine-1")
			now = now.Add(time.Second)
			counter := filepath.Join(t.TempDir(), "executions")
			command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
			firstEnvelope, first := invokeVerifyIdentity(t, s, service, grant, command, "verify-request-1")
			assertVerifyExecutionCount(t, counter, 1)
			if newEpoch {
				now = now.Add(time.Second)
				// A repeated qualifying action inside one pass reuses the
				// standing start (CD-0198 D3), so the same-window checkpoint
				// cannot mint a new epoch. The declared correction re-entry
				// is the route that does: the green verify run binds as the
				// refine epoch's verification evidence, the refine exit
				// asserts its delivery through the gate, an independent
				// evaluator associates the delivered check's real receipt, and
				// an accepted corrective Review returns the
				// instance to the producer step, whose delivery re-enters
				// refine on a fresh pass.
				verifyIdentityCorrectionReentry(t, s, grant, fixture, first.OperationRef, now)
				startRefine("verify-refine-2")
			}
			now = now.Add(time.Second)
			secondEnvelope, second := invokeVerifyIdentity(t, s, service, grant, command, "verify-request-2")
			assertVerifyExecutionCount(t, counter, 2)
			if second.LeaseID == first.LeaseID || second.OperationRef == first.OperationRef || secondEnvelope.Replayed {
				t.Fatalf("new request reused the first run: first=%+v second=%+v replayed=%v", first, second, secondEnvelope.Replayed)
			}
			firstRecord, found, err := s.LookupMutationIdempotency(context.Background(), store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "worktree_verify", IdempotencyKey: "verify-request-1"})
			if err != nil || !found {
				t.Fatalf("first request record found=%v: %v", found, err)
			}
			secondRecord, found, err := s.LookupMutationIdempotency(context.Background(), store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "worktree_verify", IdempotencyKey: "verify-request-2"})
			if err != nil || !found {
				t.Fatalf("second request record found=%v: %v", found, err)
			}
			if firstRecord.CanonicalDigest != secondRecord.CanonicalDigest || firstRecord.OperationID == secondRecord.OperationID {
				t.Fatalf("equal intent must retain its digest but name distinct requests: first=%+v second=%+v", firstRecord, secondRecord)
			}
			var acquiredAt, observedAt string
			if err := s.DatabaseForTesting().QueryRow(`SELECT acquired_at FROM worktree_verify_leases WHERE lease_id=?`, second.LeaseID).Scan(&acquiredAt); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT observed_at FROM durable_operations WHERE op_id=?`, second.OperationRef).Scan(&observedAt); err != nil {
				t.Fatal(err)
			}
			wantTime := now.UTC().Format(time.RFC3339Nano)
			if acquiredAt != wantTime || observedAt != wantTime {
				t.Fatalf("new run timestamps acquired=%q observed=%q, want %q", acquiredAt, observedAt, wantTime)
			}
			if newEpoch {
				var startedAt string
				if err := s.DatabaseForTesting().QueryRow(`SELECT occurred_at FROM domain_events WHERE subject_id='work-2' AND kind=? AND json_extract(payload,'$.step_id')='refine' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&startedAt); err != nil {
					t.Fatal(err)
				}
				started, err := time.Parse(time.RFC3339Nano, startedAt)
				if err != nil || !now.After(started) {
					t.Fatalf("fresh run time %s must follow new refine start %s: %v", wantTime, startedAt, err)
				}
			}
			// A replay stays the original run even after another refine epoch.
			replay, replayResult := invokeVerifyIdentity(t, s, service, grant, command, "verify-request-1")
			if !replay.Replayed || replayResult.OperationRef != first.OperationRef || !bytes.Equal(replay.Result, firstEnvelope.Result) {
				t.Fatalf("same-key replay changed its outcome: first=%s replay=%s replayed=%v", firstEnvelope.Result, replay.Result, replay.Replayed)
			}
			assertVerifyExecutionCount(t, counter, 2)
		})
	}
}

// verifyIdentityAction applies one store-level workflow action on work-2, so
// the correction re-entry journey runs through the same admission and fold
// the mutation boundary serves while the verify-identity assertions stay
// about the verify lease. The store-level run mints nothing on the agent
// challenge table.
func verifyIdentityAction(t *testing.T, s *store.Store, action string, fields map[string]any, actor store.WorkflowActor, now time.Time) {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-2'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "verify-identity-" + action + "-" + strconv.FormatInt(version, 10)
	request := store.WorkflowActionExecutionRequest{
		WorkID: "work-2", ExpectedVersion: version, ActionID: action, Payload: raw, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: ManifestDigest, Now: now,
	}
	ctx := context.Background()
	if store.WorkflowActionRequiresSubjectFreshness(action) {
		preparation, prepErr := store.PrepareWorkflowAcceptanceFreshness(ctx, s, store.PrepareWorkerDispatchRequest{WorkID: request.WorkID})
		if prepErr != nil {
			t.Fatalf("verify-identity journey action %s: %v", action, prepErr)
		}
		ctx = store.WithWorkerDispatchPreparation(ctx, preparation)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), request)
		return err
	}); err != nil {
		t.Fatalf("verify-identity journey action %s: %v", action, err)
	}
}

// verifyIdentityDelivery asserts one admitted delivery on work-2's current
// step: the advancing completion whose fold crosses the step edge.
func verifyIdentityDelivery(t *testing.T, s *store.Store, actor store.WorkflowActor, artifact string, now time.Time) {
	t.Helper()
	verifyIdentityAction(t, s, "record_delivery", map[string]any{"delivery_artifact": artifact, "delivery_state": "asserted"}, actor, now)
}

// verifyIdentityCorrectionReentry drives the declared correction re-entry the
// canonical head admits for a fresh refine epoch: the green verify run binds
// as the refine epoch's verification evidence, the refine exit asserts its
// delivery through the gate, an independent evaluator judges the delivered
// change against the approved predicate, and a Review-directed correction
// returns the instance to the producer step, whose delivery re-enters refine
// on a fresh pass. No step and no record is faked: every leg is an admitted
// action against the owning admission owner.
func verifyIdentityCorrectionReentry(t *testing.T, s *store.Store, grant Authority, fixture *oAgentFixtureState, verifyOpRef string, now time.Time) {
	t.Helper()
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	judge := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/verify-identity-judge", SessionRef: "session/verify-identity-judge", ActorClass: store.ActorAgent}
	var producer, watermark string
	if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref,request_id FROM durable_operations WHERE op_id=?`, verifyOpRef).Scan(&producer, &watermark); err != nil {
		t.Fatal(err)
	}
	verifyIdentityAction(t, s, "bind_evidence", map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": verifyOpRef,
		"producer_id": producer, "producer_run_ref": verifyOpRef, "producer_watermark": watermark,
	}, actor, now)
	oAgentNativeJob(t, fixture)
	oAgentReview(t, fixture, "ready_for_final_proof", false, verifyOpRef)
	verifyIdentityDelivery(t, s, actor, "evidence:verify-identity-refine-exit", now)
	verifyIdentityDelivery(t, s, actor, "evidence:verify-identity-gate-exit", now)
	verifyIdentityAction(t, s, "record_verdict", map[string]any{
		"contract_version": int64(1),
		"verdicts": []map[string]any{{
			"predicate_id": "predicate:primary", "receipt_refs": []string{verifyOpRef},
		}},
	}, judge, now)
	oAgentNativeJob(t, fixture)
	oAgentReview(t, fixture, "correction_required", false)
	verifyIdentityAction(t, s, "request_correction", map[string]any{
		"diagnosis":     "the delivered change does not satisfy the approved contract predicate",
		"strategy":      "reproduce the change at the producer step and verify it under a fresh refine epoch",
		"predicate_ids": []string{"predicate:primary"},
		"evidence_refs": []string{verifyOpRef},
	}, actor, now)
	oAgentNativeJob(t, fixture)
	oAgentReview(t, fixture, "ready_for_final_proof", false)
	verifyIdentityDelivery(t, s, actor, "evidence:verify-identity-correction-reentry", now)
}

func TestWorktreeVerifyRequestIdentityIncludesPrincipal(t *testing.T) {
	t.Parallel()
	s, service, grant, secondService, secondGrant, _ := tiersFixture(t)
	counter := filepath.Join(t.TempDir(), "executions")
	command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
	_, first := invokeVerifyIdentity(t, s, service, grant, command, "shared-verify-key")
	_, second := invokeVerifyIdentity(t, s, secondService, secondGrant, command, "shared-verify-key")
	assertVerifyExecutionCount(t, counter, 2)
	if first.LeaseID == second.LeaseID || first.OperationRef == second.OperationRef {
		t.Fatalf("distinct principals shared verification identity: first=%+v second=%+v", first, second)
	}
}

func TestWorktreeVerifySameRequestRefusesChangedCommand(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	counter := filepath.Join(t.TempDir(), "executions")
	command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
	invokeVerifyIdentity(t, s, service, grant, command, "verify-pinned-command")
	refused := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": []string{"true"}, "idempotency_key": "verify-pinned-command",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "idempotency_conflict" || refused.Error.EffectState != EffectNone {
		t.Fatalf("changed command under the same key: %+v", refused)
	}
	assertVerifyExecutionCount(t, counter, 1)
}

func TestWorktreeVerifyReplaysReleasedLeaseWithoutIdempotencyRecord(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	counter := filepath.Join(t.TempDir(), "executions")
	command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
	firstEnvelope, first := invokeVerifyIdentity(t, s, service, grant, command, "verify-release-recovery")
	// Model the crash window after lease release but before result persistence.
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM idempotency_records WHERE principal_ref=? AND tool='concord_work_transition' AND operation_kind='worktree_verify' AND idempotency_key='verify-release-recovery'`, grant.PrincipalRef); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.LookupMutationIdempotency(context.Background(), store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "worktree_verify", IdempotencyKey: "verify-release-recovery"}); err != nil || found {
		t.Fatalf("crash-window fixture still has a replay record: found=%v err=%v", found, err)
	}
	recovered, result := invokeVerifyIdentity(t, s, service, grant, command, "verify-release-recovery")
	if result.LeaseID != first.LeaseID || result.OperationRef != first.OperationRef || !bytes.Equal(recovered.Result, firstEnvelope.Result) {
		t.Fatalf("released-lease recovery changed the original result: first=%s recovered=%s", firstEnvelope.Result, recovered.Result)
	}
	assertVerifyExecutionCount(t, counter, 1)
}

func invokeVerifyIdentity(t *testing.T, s *store.Store, service *Service, grant Authority, command []string, key string) (Envelope, store.WorktreeVerifyResult) {
	t.Helper()
	response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": command, "idempotency_key": key,
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("verify %s: %+v", key, response.Error)
	}
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.TrackedFilesChanged || result.LeaseID == "" || result.OperationRef == "" {
		t.Fatalf("verify %s result=%+v", key, result)
	}
	return response, result
}

func assertVerifyExecutionCount(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(data, []byte("run\n")); got != want {
		t.Fatalf("verification command executions=%d, want %d", got, want)
	}
}
