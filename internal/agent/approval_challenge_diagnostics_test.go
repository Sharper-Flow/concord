package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestApprovalChallengeBindingDiagnostics(t *testing.T) {
	for _, reason := range []string{"inactive", "expired", "operation_digest", "scope", "versions", "consequence", "host_assertion_digest", "malformed_expiry"} {
		t.Run(reason, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
			inv.HostAssertionDigest = "sha256:host-resolution"
			ctx := context.Background()
			check := ApprovalCheck{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", ClientRef: inv.ClientRef, SessionRef: inv.SessionRef}
			if err := db.Transact(ctx, func(tx *store.Transaction) error {
				var err error
				check.ApprovalRef, err = service.CreateApprovalChallengeTx(ctx, tx, probedHost(), inv, ApprovalChallengeSpec{OperationDigest: check.OperationDigest, Scope: check.Scope, Versions: check.Versions, Consequence: check.Consequence, HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "inactive":
				if _, err := db.DatabaseForTesting().Exec(`UPDATE agent_approval_challenges SET status='consumed' WHERE challenge_ref=?`, check.ApprovalRef); err != nil {
					t.Fatal(err)
				}
			case "expired":
				service.Now = func() time.Time { return fixedTime().Add(2 * time.Hour) }
			case "operation_digest":
				check.OperationDigest = "sha256:other-operation"
			case "scope":
				check.Scope["work_ids"] = []string{"work-other"}
			case "versions":
				check.Versions["work"] = 4
			case "consequence":
				check.Consequence = "other-consequence"
			case "host_assertion_digest":
				inv.HostAssertionDigest = "sha256:other-host"
			case "malformed_expiry":
				if _, err := db.DatabaseForTesting().Exec(`UPDATE agent_approval_challenges SET expires_at='private-malformed-expiry' WHERE challenge_ref=?`, check.ApprovalRef); err != nil {
					t.Fatal(err)
				}
			}
			assertion := HostApprovalAssertion{ChallengeRef: check.ApprovalRef, RequestDigest: check.OperationDigest, Scope: approvalScopeBindings(check.Scope), Versions: approvalVersionBindings(check.Versions), SessionRef: inv.SessionRef, AgentRef: inv.AgentRef, Worktree: inv.Worktree, IssuedAt: service.now().Format(time.RFC3339Nano)}
			err := db.Transact(ctx, func(tx *store.Transaction) error {
				_, err := service.ValidateHostApprovalAssertionTx(ctx, tx, inv, assertion, check)
				return err
			})
			if err == nil {
				t.Fatal("invalid challenge binding admitted")
			}
			out := failureEnvelope(NewBase("binding-test", "concord_work_relate", "set_memberships"), err)
			if out.Error.Kind != "approval_invalid" || out.Error.RecoveryAction.Kind != "request_approval" || out.Error.RetrySafe || out.Error.EffectState != EffectNone {
				t.Fatalf("binding refusal changed its authority semantics: %+v", out.Error)
			}
			wantReason := reason
			if reason == "malformed_expiry" {
				wantReason = "expired"
				if _, ok := out.Error.Details["expires_at"]; ok {
					t.Fatal("malformed expiry entered the public diagnostic")
				}
			}
			if out.Error.Details["boundary"] != "approval_challenge_binding" || out.Error.Details["reason"] != wantReason || out.Error.Details["validated_at"] != service.now().Format(time.RFC3339Nano) {
				t.Fatalf("missing exact binding diagnostic: %+v", out.Error)
			}
			wire, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{check.ApprovalRef, inv.SessionRef, "private-malformed-expiry", "work-other", "sha256:other-operation", "sha256:other-host"} {
				if strings.Contains(string(wire), private) {
					t.Fatal("challenge diagnostic disclosed approval contents or caller identity")
				}
			}
			if usedCount(t, db.DatabaseForTesting(), `SELECT used_count FROM agent_approval_challenges WHERE challenge_ref=?`, check.ApprovalRef) != 0 || countRows(t, db.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
				t.Fatal("binding refusal consumed challenge authority")
			}
		})
	}
}
