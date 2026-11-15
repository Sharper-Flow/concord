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

func TestInvokeWorkRemovalHandoffByteLimit(t *testing.T) {
	for _, section := range []string{"findings", "remaining_scope", "blockers", "artifacts", "renewal_conditions"} {
		for _, boundary := range []struct{ name, entry string }{
			{"ascii", strings.Repeat("a", 4096)},
			{"unicode", strings.Repeat("é", 2048)},
		} {
			t.Run(section+"/"+boundary.name, func(t *testing.T) {
				ctx := t.Context()
				s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
				if err := s.Transact(ctx, func(tx *store.Transaction) error {
					_, err := store.RecordLinearIssueLinkTx(ctx, tx, store.LinearIssueLink{WorkID: "work-1", RemoteIssueUUID: "remote-1", HumanKey: "EX-1", URL: "https://linear.app/example/issue/EX-1"})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
				env.RequestID = "request:removal-bytes"
				handoff := map[string][]string{"findings": {"finding"}, "remaining_scope": {"scope"}, "blockers": {"blocker"}, "artifacts": {"artifact"}, "renewal_conditions": {"renewal"}}
				input := map[string]any{
					"operation_id": "removal-bytes", "idempotency_key": "removal-bytes-key", "work_id": "work-1", "expected_version": workVersion(t, s, "work-1"),
					"reason": "shelved", "actor": "human-1", "product_id": "product-1", "handoff": handoff,
					"execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true,
				}
				invoke := func(entry string) agentcontract.Envelope {
					t.Helper()
					handoff[section] = []string{entry}
					canonical := store.WorkRemovalHandoff{Findings: handoff["findings"], RemainingScope: handoff["remaining_scope"], Blockers: handoff["blockers"], Artifacts: handoff["artifacts"], RenewalConditions: handoff["renewal_conditions"]}
					encoded, err := json.Marshal(canonical)
					if err != nil {
						t.Fatal(err)
					}
					sum := sha256.Sum256(encoded)
					input["linear"] = map[string]string{"product_id": "product-1", "remote_issue_uuid": "remote-1", "destination": "linear", "handoff_digest": "sha256:" + hex.EncodeToString(sum[:])}
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
				refused := invoke(strings.Repeat("é", 2049))
				if after := snapshot(); before != after {
					t.Fatalf("refusal changed durable state: %v -> %v", before, after)
				}
				if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.EffectState != agentcontract.EffectNone || !strings.Contains(refused.Error.Message, "x-maxBytes") || !strings.Contains(refused.Error.Message, section) {
					t.Errorf("expected schema byte-limit refusal for %s, got %+v", section, refused.Error)
				}
				accepted := invoke(boundary.entry)
				if accepted.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("4096-byte entry refused: %+v", accepted.Error)
				}
				if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='removal-bytes' AND state='committed'`) != 1 {
					t.Fatal("boundary removal did not commit exactly once")
				}
			})
		}
	}
}
