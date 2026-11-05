package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func initializeMutationPinWorkflow(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	ctx := agentWorkflowSourceProof(t, context.Background(), s, workID)
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: workID, Actor: store.WorkflowActor{PrincipalRef: "principal/fixture", ClientRef: "client/fixture", AgentRef: "agent/fixture", SessionRef: "session/fixture", ActorClass: store.ActorAgent}, Now: fixedTime()})
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

func TestMutationPinMissingWorkRefuses(t *testing.T) {
	s, _, _ := enrichmentPinFixture(t)
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

func TestMutationPinOmissionKeepsOtherWorkPins(t *testing.T) {
	ctx := context.Background()
	s, workID, pin := enrichmentPinFixture(t)
	membership, _ := json.Marshal(map[string]any{"memberships": []map[string]string{{"project_id": pin.ProjectID, "role": "primary"}}, "expected_version": 1, "resulting_version": 2})
	events := []store.Event{
		{EventID: "unmanaged-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "unmanaged-work", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Unmanaged work"}`)},
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
