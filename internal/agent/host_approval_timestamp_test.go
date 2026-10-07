package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestHostApprovalAssertionTimestampRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, issued, reason string
	}{
		{"malformed", "private-malformed-assertion", "malformed"},
		{"expired", fixedTime().Add(-2*time.Minute - time.Nanosecond).Format(time.RFC3339Nano), "expired"},
		{"future", fixedTime().Add(2*time.Minute + time.Nanosecond).Format(time.RFC3339Nano), "future"},
		{"fresh", fixedTime().Format(time.RFC3339Nano), ""},
		{"past-boundary", fixedTime().Add(-2 * time.Minute).Format(time.RFC3339Nano), ""},
		{"future-boundary", fixedTime().Add(2 * time.Minute).Format(time.RFC3339Nano), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
			inv.HostAssertionDigest = "sha256:host-resolution"
			ctx := context.Background()
			const ref = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			check := ApprovalCheck{ApprovalRef: ref, OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", ClientRef: inv.ClientRef, SessionRef: inv.SessionRef}
			if err := db.Transact(ctx, func(tx *store.Transaction) error {
				return store.InsertApprovalTx(ctx, tx, store.ApprovalInsert{ApprovalRef: ref, OperationDigest: check.OperationDigest, ScopeJSON: `{"product_id":"product-1"}`, VersionJSON: `{"work":3}`, Consequence: check.Consequence, HumanPrincipalRef: inv.PrincipalRef, ClientRef: inv.ClientRef, SessionRef: inv.SessionRef, IssuedAt: fixedTime().Format(time.RFC3339Nano), ExpiresAt: fixedTime().Add(time.Hour).Format(time.RFC3339Nano), MaxUses: 1, ProtectedEvidenceRef: "timestamp-test", ProtectedEvidenceDigest: "sha256:evidence"})
			}); err != nil {
				t.Fatal(err)
			}
			assertion := HostApprovalAssertion{ChallengeRef: ref, RequestDigest: check.OperationDigest, Scope: approvalScopeBindings(check.Scope), Versions: approvalVersionBindings(check.Versions), SessionRef: inv.SessionRef, AgentRef: inv.AgentRef, Worktree: inv.Worktree, IssuedAt: tc.issued}
			consume := func() error {
				return db.Transact(ctx, func(tx *store.Transaction) error {
					challenge, err := service.ValidateHostApprovalAssertionTx(ctx, tx, inv, assertion, check)
					if err != nil {
						return err
					}
					if challenge {
						t.Fatal("existing approval was replaced with a challenge")
					}
					return service.ValidateAndConsumeApprovalTx(ctx, tx, ref, check)
				})
			}
			err := consume()
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid timestamp admitted")
				}
				out := failureEnvelope(NewBase("timestamp-test", "concord_work_transition", "workflow_action"), err)
				if out.Error.Kind != "internal_error" || out.Error.RecoveryAction.Kind != "contact_operator" || out.Error.RetrySafe || out.Error.EffectState != EffectNone {
					t.Fatalf("timestamp refusal lost its cause or asks for another approval: %+v", out.Error)
				}
				if out.Error.Details["reason"] != tc.reason || out.Error.Details["boundary"] != "host_approval_timestamp" || out.Error.Details["validated_at"] != fixedTime().Format(time.RFC3339Nano) || out.Error.Details["allowed_clock_skew_seconds"] != float64(120) {
					t.Fatalf("missing bounded timestamp diagnostic: %+v", out.Error.Details)
				}
				if tc.reason == "malformed" {
					if _, present := out.Error.Details["issued_at"]; present {
						t.Fatal("malformed host input leaked into timestamp diagnostic")
					}
				} else if out.Error.Details["issued_at"] != tc.issued {
					t.Fatalf("issued timestamp lost: %+v", out.Error.Details)
				}
				encoded, marshalErr := json.Marshal(out)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if strings.Contains(string(encoded), "private-malformed-assertion") || strings.Contains(string(encoded), ref) || strings.Contains(string(encoded), inv.SessionRef) {
					t.Fatal("timestamp diagnostic leaked approval contents or caller identity")
				}
				var used int
				if err := db.DatabaseForTesting().QueryRow(`SELECT used_count FROM agent_approvals WHERE approval_ref=?`, ref).Scan(&used); err != nil || used != 0 {
					t.Fatalf("refusal consumed approval: used=%d, error=%v", used, err)
				}
				// The host can attest the still-valid decision without a new approval.
				assertion.IssuedAt = fixedTime().Format(time.RFC3339Nano)
				if err := consume(); err != nil {
					t.Fatalf("refusal invalidated the approved decision: %v", err)
				}
			}
			if err := consume(); err == nil {
				t.Fatal("single-use approval replay admitted")
			}
		})
	}
}

func TestHostApprovalAssertionBindingRefusal(t *testing.T) {
	for _, binding := range []string{"caller", "scope", "versions", "challenge"} {
		t.Run(binding, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
			inv.HostAssertionDigest = "sha256:host-resolution"
			ctx := context.Background()
			var ref string
			if err := db.Transact(ctx, func(tx *store.Transaction) error {
				var err error
				ref, err = service.CreateApprovalChallengeTx(ctx, tx, probedHost(), inv, ApprovalChallengeSpec{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			check := ApprovalCheck{ApprovalRef: ref, OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", ClientRef: inv.ClientRef, SessionRef: inv.SessionRef}
			assertion := HostApprovalAssertion{ChallengeRef: ref, RequestDigest: check.OperationDigest, Scope: approvalScopeBindings(check.Scope), Versions: approvalVersionBindings(check.Versions), SessionRef: inv.SessionRef, AgentRef: inv.AgentRef, Worktree: inv.Worktree, IssuedAt: fixedTime().Format(time.RFC3339Nano)}
			switch binding {
			case "caller":
				assertion.AgentRef = "other-agent"
			case "scope":
				assertion.Scope = []string{"product_id:other-product"}
			case "versions":
				assertion.Versions = []string{"work:4"}
			case "challenge":
				check.Consequence = "different-consequence"
			}
			err := db.Transact(ctx, func(tx *store.Transaction) error {
				_, err := service.ValidateHostApprovalAssertionTx(ctx, tx, inv, assertion, check)
				return err
			})
			if err == nil {
				t.Fatal("changed binding admitted")
			}
			out := failureEnvelope(NewBase("binding-test", "concord_work_transition", "workflow_action"), err)
			if out.Error.Kind != "approval_invalid" || out.Error.RecoveryAction.Kind != "request_approval" || out.Error.RetrySafe || out.Error.EffectState != EffectNone {
				t.Fatalf("binding refusal lost: %+v", out.Error)
			}
			if _, err := json.Marshal(out); err != nil {
				t.Fatal(err)
			}
			var used int
			if err := db.DatabaseForTesting().QueryRow(`SELECT used_count FROM agent_approval_challenges WHERE challenge_ref=?`, ref).Scan(&used); err != nil || used != 0 {
				t.Fatalf("refusal consumed challenge: used=%d, error=%v", used, err)
			}
		})
	}
}
