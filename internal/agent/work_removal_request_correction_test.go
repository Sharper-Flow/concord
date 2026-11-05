package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestInvokeWorkRemovalRequestCorrection(t *testing.T) {
	for _, tc := range []struct {
		name, field, diagnostic string
	}{
		{"handoff", "handoff.remaining_scope", "contains an empty or oversized entry"},
		{"identity", "operation_id", "identities must be bounded"},
		{"safety", "artifacts_verified", "safety evidence is incomplete"},
		{"digest", "linear.handoff_digest", "Linear confirmation does not match the handoff"},
		{"linked_confirmation", "linear", "records a Linear issue but the removal carries no Linear handoff confirmation"},
		{"unlinked_confirmation", "linear", "no recorded Linear issue identity"},
		{"terminal_state", "", "completed and superseded work cannot be removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
			link := func() {
				t.Helper()
				if err := s.Transact(ctx, func(tx *store.Transaction) error {
					_, err := store.RecordLinearIssueLinkTx(ctx, tx, store.LinearIssueLink{WorkID: "work-1", RemoteIssueUUID: "remote-1", HumanKey: "EX-1", URL: "https://linear.app/example/issue/EX-1"})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name != "unlinked_confirmation" {
				link()
			}
			env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
			env.RequestID = "request:removal-correction"
			version := workVersion(t, s, "work-1")
			handoff := store.WorkRemovalHandoff{Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"}, Artifacts: []string{"artifact"}, RenewalConditions: []string{"renewal"}}
			encoded, err := json.Marshal(handoff)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(encoded)
			linear := map[string]string{"product_id": "product-1", "remote_issue_uuid": "remote-1", "destination": "linear", "handoff_digest": "sha256:" + hex.EncodeToString(sum[:])}
			input := map[string]any{
				"operation_id": "remove-correction", "idempotency_key": "remove-correction-key", "work_id": "work-1", "expected_version": version,
				"reason": "shelved", "actor": "human-1", "product_id": "product-1", "handoff": handoff, "linear": linear,
				"execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true,
			}
			switch tc.name {
			case "handoff":
				bad := handoff
				bad.RemainingScope = []string{" "}
				input["handoff"] = bad
			case "identity":
				input["operation_id"] = "x"
			case "safety":
				input["artifacts_verified"] = false
			case "digest":
				linear["handoff_digest"] = "sha256:" + strings.Repeat("0", 64)
			case "linked_confirmation", "unlinked_confirmation":
				delete(input, "linear")
			case "terminal_state":
				removalFixtureSQL(t, s, `UPDATE work_items SET lifecycle='completed' WHERE id='work-1'`)
			}
			invoke := func() agentcontract.Envelope {
				t.Helper()
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				out, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_transition", Operation: "remove", Input: raw}, env))
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			snapshot := func() [6]int64 {
				t.Helper()
				var result [6]int64
				for i, q := range []string{`SELECT count(*) FROM work_items`, `SELECT count(*) FROM work_removal_operations`, `SELECT count(*) FROM idempotency_records`, `SELECT count(*) FROM effect_receipts`, `SELECT last FROM store_sequences WHERE name='effect'`, `SELECT version FROM work_items WHERE id='work-1'`} {
					if err := s.DatabaseForTesting().QueryRow(q).Scan(&result[i]); err != nil {
						t.Fatal(err)
					}
				}
				return result
			}
			before := snapshot()
			refused := invoke()
			if after := snapshot(); before != after {
				t.Fatalf("refusal changed durable state: %v -> %v", before, after)
			}
			if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil {
				t.Fatalf("expected refusal: %+v", refused)
			}
			if !strings.Contains(refused.Error.Message, tc.diagnostic) {
				t.Errorf("wrong guard, expected %q: %+v", tc.diagnostic, refused.Error)
			}
			if tc.name == "terminal_state" {
				if refused.Error.EffectState != agentcontract.EffectNone || refused.Error.RecoveryAction.Kind == "correct_request" {
					t.Errorf("state refusal misclassified as request correction: %+v", refused.Error)
				}
				return
			}
			if refused.Error.EffectState != agentcontract.EffectNone || refused.Error.RetrySafe || refused.Error.Kind != "invalid_input" || refused.Error.RecoveryAction.Kind != "correct_request" {
				t.Errorf("wrong refusal classification: %+v", refused.Error)
			}
			if !strings.Contains(refused.Error.Message, tc.field) {
				t.Errorf("missing request field %q: %+v", tc.field, refused.Error)
			}
			if _, err := refused.Encode(); err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "handoff":
				input["handoff"] = handoff
			case "identity":
				input["operation_id"] = "remove-correction"
			case "safety":
				input["artifacts_verified"] = true
			case "digest":
				linear["handoff_digest"] = "sha256:" + hex.EncodeToString(sum[:])
			case "unlinked_confirmation":
				link()
				input["linear"] = linear
			case "linked_confirmation":
				input["linear"] = linear
			}
			corrected := invoke()
			if corrected.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("corrected request refused: %+v", corrected.Error)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='remove-correction' AND state='committed'`) != 1 {
				t.Fatal("corrected removal did not commit exactly once")
			}
		})
	}
}

func TestInvokeDefaultCheckoutRemovalDigestCorrection(t *testing.T) {
	s, service, env, input := defaultRemovalFixture(t)
	grant, err := service.Authorize(t.Context(), Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, RequiredCapability: "work_transition", RequiredOperation: "remove"})
	if err != nil || !grant.MainWorktree {
		t.Fatalf("expected default checkout authority: %+v %v", grant, err)
	}
	correct := input["linear"].(store.LinearHandoffConfirmation)
	bad := correct
	bad.HandoffDigest = "sha256:" + strings.Repeat("0", 64)
	input["linear"] = bad
	invoke := func() agentcontract.Envelope {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Invoke(t.Context(), s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_transition", Operation: "remove", Input: raw}, env))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	snapshot := func() [6]int64 {
		t.Helper()
		var result [6]int64
		for i, q := range []string{`SELECT count(*) FROM work_items`, `SELECT count(*) FROM work_removal_operations`, `SELECT count(*) FROM idempotency_records`, `SELECT count(*) FROM effect_receipts`, `SELECT last FROM store_sequences WHERE name='effect'`, `SELECT version FROM work_items WHERE id='work-1'`} {
			if err := s.DatabaseForTesting().QueryRow(q).Scan(&result[i]); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	before := snapshot()
	refused := invoke()
	if after := snapshot(); before != after {
		t.Fatalf("refusal changed durable state: %v -> %v", before, after)
	}
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil {
		t.Fatalf("expected refusal: %+v", refused)
	}
	if !strings.Contains(refused.Error.Message, "Linear confirmation does not match the handoff") {
		t.Fatalf("wrong guard: %+v", refused.Error)
	}
	if refused.Error.EffectState != agentcontract.EffectNone || refused.Error.RetrySafe || refused.Error.Kind != "invalid_input" || refused.Error.RecoveryAction.Kind != "correct_request" || !strings.Contains(refused.Error.Message, "linear.handoff_digest") {
		t.Errorf("wrong refusal classification: %+v", refused.Error)
	}
	input["linear"] = correct
	corrected := invoke()
	if corrected.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("corrected request refused: %+v", corrected.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='shelving-removal' AND state='committed'`) != 1 {
		t.Fatal("corrected removal did not commit exactly once")
	}
}
