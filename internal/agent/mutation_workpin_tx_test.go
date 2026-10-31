package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func initializeMutationPinWorkflow(t *testing.T, s *store.Store, workID string, definition store.WorkflowDefinition) {
	t.Helper()
	registered, err := store.BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentWorkflowSourceProof(t, context.Background(), s, workID)
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: store.WorkflowActor{PrincipalRef: "principal/fixture", ClientRef: "client/fixture", AgentRef: "agent/fixture", SessionRef: "session/fixture", ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
}

func driftWorkPinVersion(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	// Inject a later writer after the owning transaction has committed.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE work_items SET version=version+1 WHERE id=?;
		DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
}

func requireMutationPinVersion(t *testing.T, response agentcontract.Envelope, workID string, version int64) {
	t.Helper()
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("outcome=%s error=%+v", response.Outcome, response.Error)
	}
	var result struct {
		WorkPins []store.WorkPin `json:"work_pins"`
		Changed  []struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"changed_refs"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.WorkPins) != 1 || result.WorkPins[0].WorkID != workID || result.WorkPins[0].Version != version {
		t.Fatalf("pins=%+v, want %s at the mutation's version %d", result.WorkPins, workID, version)
	}
	if len(result.Changed) != 1 || result.Changed[0].ID != workID || result.Changed[0].Version != version || response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].Version != strconv.FormatInt(version, 10) {
		t.Fatalf("result refs=%+v envelope refs=%+v, want version %d", result.Changed, response.ChangedRefs, version)
	}
}

func TestCompactionPublishPinUsesLinkTransaction(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	holdCompactionCleanup(t, s)
	initializeMutationPinWorkflow(t, s, "work-cancelled", store.BuiltinWorkflowDefinitions()[0])
	_, before := readWorkFromStore(t, s, "work-cancelled")
	observed := false
	service.publicationObserver = func(phase string) error {
		if phase == "record_locator" {
			observed = true
			driftWorkPinVersion(t, s, "work-cancelled")
		}
		return nil
	}
	response := publishCancelledNote(t, s, service, grant, key, home)
	if !observed {
		t.Fatal("link transaction never committed")
	}
	requireMutationPinVersion(t, response, "work-cancelled", before)
	_, current := readWorkFromStore(t, s, "work-cancelled")
	if current != before+1 {
		t.Fatalf("later writer did not advance: %d", current)
	}
}

func TestCompactionReconcilePinFailureRollsBackLink(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	holdCompactionCleanup(t, s)
	initializeMutationPinWorkflow(t, s, "work-cancelled", store.BuiltinWorkflowDefinitions()[0])
	_, before := readWorkFromStore(t, s, "work-cancelled")
	service.publicationObserver = func(phase string) error {
		if phase == "verify_commit" {
			return errors.New("stop after committed proof")
		}
		return nil
	}
	pending := publishCancelledNote(t, s, service, grant, key, home)
	if pending.Outcome != agentcontract.OutcomePartial || pending.OperationRef == nil {
		t.Fatalf("orphan fixture: %+v", pending)
	}
	if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER drift_compaction_pin AFTER INSERT ON archived_work
		BEGIN UPDATE workflow_instances SET definition_ref='workflow.unavailable_fixture' WHERE work_id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-api")
	input, _ := json.Marshal(map[string]any{"operation_id": pending.OperationRef.ID, "expected_operation_version": 1, "idempotency_key": "reconcile-pin"})
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}, env)
	if response.Outcome != agentcontract.OutcomePartial {
		t.Fatalf("unreadable pin must refuse the link: outcome=%s error=%+v", response.Outcome, response.Error)
	}
	_, current := readWorkFromStore(t, s, "work-cancelled")
	if current != before || archivedCancelledWorkCount(t, s) != 0 || durableOperationCompleted(t, s, "work-cancelled") {
		t.Fatalf("pin failure committed: version=%d, want %d with no locator/completion", current, before)
	}
	if _, err := s.DatabaseForTesting().Exec(`DROP TRIGGER drift_compaction_pin`); err != nil {
		t.Fatal(err)
	}
	retry := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}, env)
	requireMutationPinVersion(t, retry, "work-cancelled", before)
}

func TestCompactionCompletedReplaysKeepTransactionPin(t *testing.T) {
	for _, route := range []string{"cached_publish", "claimed_publish", "completed_reconcile"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, key, home := agentJobsCompactionFixture(t)
			holdCompactionCleanup(t, s)
			initializeMutationPinWorkflow(t, s, "work-cancelled", store.BuiltinWorkflowDefinitions()[0])
			_, before := readWorkFromStore(t, s, "work-cancelled")
			first := publishCancelledNote(t, s, service, grant, key, home)
			requireMutationPinVersion(t, first, "work-cancelled", before)
			var operationID string
			if err := s.DatabaseForTesting().QueryRow(`SELECT op_id FROM durable_operations WHERE work_id='work-cancelled'`).Scan(&operationID); err != nil {
				t.Fatal(err)
			}
			driftWorkPinVersion(t, s, "work-cancelled")
			env := agentJobsMutationEnvelope(t, s, grant, "proj-api")
			r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_compact", Operation: "publish"}
			content := pm1fixture.CanonicalWorkNote("work-cancelled", "2026-08-02T12:00:00Z", "cancelled", "proj-api")
			// CD-0243 D3/D7 retire publication-only consent. Replay the
			// original unsigned request; its committed transaction pin stays fixed.
			input := map[string]any{"work_id": "work-cancelled", "expected_version": before, "content": content, "content_digest": fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content))), "home_project_id": home.HomeProjectID, "home_locator_id": home.HomeLocatorID, "idempotency_key": "compact-cancelled-approval-roundtrip-1"}
			if route == "completed_reconcile" {
				r.Operation = "reconcile"
				input = map[string]any{"operation_id": operationID, "expected_operation_version": 1, "idempotency_key": "completed-reconcile-pin"}
			}
			raw, _ := json.Marshal(input)
			op, _ := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
			base := agentcontract.NewBase(env.RequestID, r.Tool, r.Operation)
			base.ResolvedScope = &agentcontract.Scope{ProductID: "prod-alpha", ProjectIDs: []string{"proj-api"}, WorkIDs: []string{"work-cancelled"}, ScopeVersion: env.ScopeVersion}
			var response agentcontract.Envelope
			var err error
			if route == "cached_publish" {
				var handled bool
				response, handled, err = r.replayMutationBeforeScope(ctx, base, raw, grant, op)
				if !handled {
					t.Fatalf("cached replay not handled: %v", err)
				}
			} else {
				response, err = r.mutateCompaction(ctx, base, raw, mutationDigest(r.Tool, r.Operation, env, raw), op)
			}
			if err != nil {
				t.Fatal(err)
			}
			requireMutationPinVersion(t, response, "work-cancelled", before)
		})
	}
}

func TestMutationPinMissingWorkRefuses(t *testing.T) {
	s, _, _, _, _ := agentJobsCompactionFixture(t)
	r := runtime{Store: s}
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: "work-not-recorded", Version: "1"}}
	err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, _, _, err := r.enrichMutationPayloadTx(context.Background(), tx, mutationPayload(changed, nil), changed)
		return err
	})
	if err == nil {
		t.Fatal("missing required work pin was silently omitted")
	}
}

func TestWorktreeAuditReclaimMissingWorkflowPinIsExplicit(t *testing.T) {
	s, _, _, service, grant, _ := tiersFixture(t)
	completeWork(t, s, "work-2")
	vacateLinkedWorktree(t, s, service, grant, filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2"), "missing-pin-vacate")
	input := map[string]any{"product_id": "product-1", "default_ref": "main", "idempotency_key": "missing-pin-audit"}
	response := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("audit=%+v", response.Error)
	}
	found := false
	for _, notice := range response.Omissions {
		if notice.Kind == "work_pin_unavailable" && notice.Count == 1 && notice.SourceID == "" && len(notice.Details) == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing workflow pin was not explicit: omissions=%+v", response.Omissions)
	}
	initializeMutationPinWorkflow(t, s, "work-2", store.BuiltinWorkflowDefinitions()[0])
	commits := s.DurableCommits()
	replay := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(response.Result) || !reflect.DeepEqual(replay.Omissions, response.Omissions) || !reflect.DeepEqual(replay.ChangedRefs, response.ChangedRefs) {
		t.Fatalf("audit replay replaced its retained omission after a workflow was created: first=%+v replay=%+v", response, replay)
	}
	requireReplayCommit(t, s, "missing-pin-audit", commits, 0, false)
}

func TestWorktreeAuditReclaimReplayKeepsRowPin(t *testing.T) {
	s, _, _, service, grant, _ := tiersFixture(t)
	completeWork(t, s, "work-2")
	vacateLinkedWorktree(t, s, service, grant, filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2"), "replay-pin-vacate")
	initializeMutationPinWorkflow(t, s, "work-2", store.BuiltinWorkflowDefinitions()[0])
	input := map[string]any{"product_id": "product-1", "default_ref": "main", "idempotency_key": "replay-pin-audit"}
	first := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	_, version := readWorkFromStore(t, s, "work-2")
	requireMutationPinVersion(t, first, "work-2", version)
	driftWorkPinVersion(t, s, "work-2")
	replay := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	requireMutationPinVersion(t, replay, "work-2", version)
	if !replay.Replayed {
		t.Fatal("audit replay must answer from its recorded row pin")
	}
}

func TestAuditReclaimEnvelopeSizingIncludesPinNotice(t *testing.T) {
	base := agentcontract.NewBase("audit-pin-size", "concord_work_transition", "worktree_audit_reclaim")
	base.Omissions = []agentcontract.Notice{{Kind: "work_pin_unavailable", Count: 1}}
	payload := json.RawMessage(`{"root":"fixture-root","rows":[],"report_only":[],"changed_refs":[],"next_valid_intents":[]}`)
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: "work-pinless", Version: "1"}}
	type wire agentcontract.Envelope
	encoded, err := json.Marshal(wire(agentcontract.NewOKMutation(base, payload, changed, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := auditReclaimEnvelopeBytes(base, payload, changed, nil, nil); got != len(encoded) {
		t.Fatalf("audit envelope estimate=%d, actual including the pin notice=%d", got, len(encoded))
	}
}

func TestEveryMutationResultGuardsWorkPins(t *testing.T) {
	_, workID, pin := enrichmentPinFixture(t)
	defs := payloadSchemaDefs(t)
	for _, op := range agentcontract.ContractOperations {
		if op.Kind != agentcontract.OperationMutation {
			continue
		}
		changed := enrichmentChangedRefs(workID, pin.Version, op.ID)
		if changed[0].EntityKind != "work_item" {
			continue
		}
		if op.ID == "concord_work_define.research_retire" {
			continue // Retirement reports pack results, never work-item refs.
		}
		t.Run(op.ID, func(t *testing.T) {
			raw, err := json.Marshal(schemaFixtureValue(t, defs[op.ResultSchema], defs, 0))
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			delete(object, "work_pins")
			object["changed_refs"], _ = json.Marshal(mutationResultChangedRefs(changed))
			raw, _ = json.Marshal(object)
			if err := agentcontract.ValidateOperationPayload(op.Tool, op.Operation, raw, true); err != nil {
				t.Fatalf("unenriched fixture must pass the payload schema: %v", err)
			}
			r := runtime{Tool: op.Tool, Operation: op.Operation}
			base := agentcontract.NewBase("pin-guard", op.Tool, op.Operation)
			missing := r.mutationResult(base, raw, changed, nil)
			if missing.Outcome != agentcontract.OutcomeError || missing.Error == nil || !strings.Contains(missing.Error.Message, "work pin") {
				t.Fatalf("operation accepted a work ref with no pin or explicit omission: outcome=%s error=%+v", missing.Outcome, missing.Error)
			}
			if response := r.mutationResult(base, raw, nil, nil); response.Outcome != agentcontract.OutcomeError {
				t.Fatal("work refs in the payload also require pins")
			}
			object["work_pins"], _ = json.Marshal([]store.WorkPin{pin})
			object["next_valid_intents"], _ = json.Marshal(mutationResultIntents(nextIntentsFromPin(pin)))
			valid, _ := json.Marshal(object)
			if response := r.mutationResult(base, valid, changed, nil); response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("matching pin rejected: %+v", response.Error)
			}
			wrong := pin
			wrong.Version++
			object["work_pins"], _ = json.Marshal([]store.WorkPin{wrong})
			mismatched, _ := json.Marshal(object)
			if response := r.mutationResult(base, mismatched, changed, nil); response.Outcome != agentcontract.OutcomeError || response.Error == nil || !strings.Contains(response.Error.Message, "work pin") {
				t.Fatalf("mismatched pin accepted: %+v", response)
			}
			base.Omissions = []agentcontract.Notice{{Kind: "work_pin_unavailable", Count: 1}}
			if response := r.mutationResult(base, raw, changed, nil, workID); response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("explicit pin omission rejected: %+v", response.Error)
			}
			if response := r.mutationResult(base, raw, changed, nil, "other-work"); response.Outcome != agentcontract.OutcomeError {
				t.Fatal("an allowed set for another work item cannot excuse this missing pin")
			}
			base.Omissions[0].Count = 2
			if response := r.mutationResult(base, raw, changed, nil, workID); response.Outcome != agentcontract.OutcomeError {
				t.Fatal("the omission count must equal the allowed set size")
			}
		})
	}
}

func TestWorktreeAuditReclaimThirtyTwoPinlessItems(t *testing.T) {
	ctx := context.Background()
	s, service, grant, repo := tiersRepoFixture(t)
	baseSHA := gitRun(t, repo, "rev-parse", "HEAD")
	for i := range agentcontract.MaxChangedRefs {
		workID := fmt.Sprintf("pinless-%d", i)
		events := []store.Event{
			{EventID: workID + ":created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Pinless work","priority":1}`)},
			{EventID: workID + ":member", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		}
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimWorktree(ctx, store.WorktreeClaimRequest{OpID: workID + ":claim", WorkID: workID, ProjectID: "project-1", BaseSHA: baseSHA, PrincipalRef: grant.PrincipalRef, RequestID: workID + ":claim", ExpectedVersion: 2, Now: fixedTime()}); err != nil {
			t.Fatal(err)
		}
	}
	response := authorityInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", map[string]any{"product_id": "product-1", "default_ref": "main", "limit": agentcontract.MaxChangedRefs, "idempotency_key": "thirty-two-pinless"})
	if response.Outcome != agentcontract.OutcomeOK || response.ChangedRefs == nil || len(*response.ChangedRefs) != agentcontract.MaxChangedRefs {
		t.Fatalf("32 pinless rows must succeed: error=%+v refs=%+v", response.Error, response.ChangedRefs)
	}
	count := 0
	for _, notice := range response.Omissions {
		if notice.Kind == "work_pin_unavailable" {
			count++
			if notice.Count != agentcontract.MaxChangedRefs || notice.SourceID != "" || len(notice.Details) != 0 {
				t.Fatalf("pinless notice must carry only the count: %+v", notice)
			}
		}
	}
	if count != 1 {
		t.Fatalf("pinless notice count=%d, want one", count)
	}
	if _, err := response.Encode(); err != nil {
		t.Fatalf("32-item audit envelope: %v", err)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id LIKE 'pinless-%' AND version=4`) != agentcontract.MaxChangedRefs {
		t.Fatal("the 32 rows did not reclaim")
	}
}

func TestMutationPinOmissionKeepsOtherWorkPins(t *testing.T) {
	ctx := context.Background()
	s, workID, pin := enrichmentPinFixture(t)
	membership, _ := json.Marshal(map[string]any{"memberships": []map[string]string{{"project_id": pin.ProjectID, "role": "primary"}}, "expected_version": 1, "resulting_version": 2})
	events := []store.Event{
		{EventID: "unmanaged-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "unmanaged-work", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Unmanaged work","priority":1}`)},
		{EventID: "unmanaged-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "unmanaged-work", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: membership},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "unmanaged-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	for _, unmanagedFirst := range []bool{true, false} {
		changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(pin.Version, 10)}, {EntityKind: "work_item", ID: "unmanaged-work", Version: "2"}}
		if unmanagedFirst {
			changed[0], changed[1] = changed[1], changed[0]
		}
		var payload json.RawMessage
		var pinless []string
		if err := s.Transact(ctx, func(tx *store.Transaction) error {
			var err error
			payload, _, pinless, err = (runtime{}).enrichMutationPayloadTx(ctx, tx, mutationPayload(changed, nil), changed)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Pins []store.WorkPin `json:"work_pins"`
		}
		if err := json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Pins) != 1 || result.Pins[0].WorkID != workID || result.Pins[0].Version != pin.Version || len(pinless) != 1 || pinless[0] != "unmanaged-work" {
			t.Fatalf("one missing workflow must not drop another work pin: pins=%+v pinless=%+v", result.Pins, pinless)
		}
	}
}

func TestMutationPinOmissionDoesNotTruncateExistingNotices(t *testing.T) {
	r := runtime{Tool: "concord_work_define", Operation: "capture"}
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: "unmanaged-work", Version: "1"}}
	base := agentcontract.NewBase("full-notice-budget", r.Tool, r.Operation)
	base.Omissions = make([]agentcontract.Notice, agentcontract.MaxNotices)
	for i := range base.Omissions {
		base.Omissions[i] = agentcontract.Notice{Kind: "existing_notice", SourceID: fmt.Sprintf("source-%d", i)}
	}
	base.Omissions = append(base.Omissions, mutationPinOmissions([]string{"unmanaged-work"})...)
	response := r.mutationResult(base, mutationPayload(changed, nil), changed, nil, "unmanaged-work")
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || !strings.Contains(response.Error.Message, "envelope") {
		t.Fatalf("notice overflow must refuse, never trim to fit: %+v", response)
	}
}

func TestMutationPinOmissionRequiresExactShape(t *testing.T) {
	r := runtime{Tool: "concord_work_define", Operation: "capture"}
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: "unmanaged-work", Version: "1"}}
	for name, notices := range map[string][]agentcontract.Notice{
		"missing":       nil,
		"zero_count":    {{Kind: "work_pin_unavailable"}},
		"wrong_count":   {{Kind: "work_pin_unavailable", Count: 2}},
		"named_source":  {{Kind: "work_pin_unavailable", Count: 1, SourceID: "unmanaged-work"}},
		"named_details": {{Kind: "work_pin_unavailable", Count: 1, Details: map[string]any{"work_id": "unmanaged-work"}}},
		"duplicate":     {{Kind: "work_pin_unavailable", Count: 1}, {Kind: "work_pin_unavailable", Count: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			base := agentcontract.NewBase("notice-shape", r.Tool, r.Operation)
			base.Omissions = notices
			response := r.mutationResult(base, mutationPayload(changed, nil), changed, nil, "unmanaged-work")
			if response.Outcome != agentcontract.OutcomeError {
				t.Fatalf("noncanonical pin omission accepted: %+v", response)
			}
		})
	}
}

func TestWorkflowReplayKeepsCommittedPinAfterLaterVersion(t *testing.T) {
	r, grant, raw, first := committedWorkflowReplay(t)
	before := workflowReplayEffectCounts(t, r.Store)
	driftWorkPinVersion(t, r.Store, "work-1")
	afterWrite := workflowReplayEffectCounts(t, r.Store)
	if before.Version == afterWrite.Version {
		t.Fatal("later writer did not advance the item")
	}
	replay := workflowTransactionReplay(t, r, grant, raw)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || string(replay.Result) != string(first.Result) {
		t.Fatalf("transaction replay lost the committed result: replay=%+v original=%s", replay, first.Result)
	}
	requireMutationPinVersion(t, replay, "work-1", before.Version)
	if workflowReplayEffectCounts(t, r.Store) != afterWrite {
		t.Fatal("replay repeated the effect or changed the later version")
	}
}

func TestCompactionPreservesFollowingWriteVersion(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	holdCompactionCleanup(t, s)
	_, version := readWorkFromStore(t, s, "work-cancelled")
	response := publishCancelledNote(t, s, service, grant, key, home)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("publish: %+v", response.Error)
	}
	for _, expected := range []int64{version + 1, version} {
		payload, _ := json.Marshal(map[string]any{"from": "cancelled", "reason": "reopen", "expected_version": expected, "resulting_version": expected + 1})
		err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: fmt.Sprintf("reopen-after-compact-%d", expected), Kind: "work.reopened", SubjectType: store.SubjectWorkItem, SubjectID: "work-cancelled", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-cancelled"): expected}})
		if expected == version+1 {
			var failure *store.Failure
			if !errors.As(err, &failure) || failure.Kind != store.KindVersionConflict {
				t.Fatalf("write at N+1 must refuse: %v", err)
			}
		} else if err != nil {
			t.Fatalf("write at N must succeed: %v", err)
		}
	}
}

func TestCompactionFencesFollowingWritesAfterCleanup(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	receipt := cancelledPublicationContent(t, newCancelledNotePublication(t, s, grant, home, "reviewed-receipt"))
	reviewCancelledReceipt(t, s, receipt.ContentDigest)
	_, version := readWorkFromStore(t, s, "work-cancelled")
	response := publishCancelledNote(t, s, service, grant, key, home)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("publish: %+v", response.Error)
	}
	for _, expected := range []int64{version + 1, version} {
		payload, _ := json.Marshal(map[string]any{"from": "cancelled", "reason": "reopen", "expected_version": expected, "resulting_version": expected + 1})
		err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: fmt.Sprintf("reopen-after-compact-%d", expected), Kind: "work.reopened", SubjectType: store.SubjectWorkItem, SubjectID: "work-cancelled", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-cancelled"): expected}})
		// CD-0242 D2 releases terminal execution after publication; its old
		// version no longer authorizes reopening collected execution state.
		var failure *store.Failure
		if !errors.As(err, &failure) || failure.Kind != store.KindProjectionNotFound {
			t.Fatalf("write after cleanup must return canonical absence: %v", err)
		}
	}
}
