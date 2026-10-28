package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestApprovalChallengeBindingDiagnostics(t *testing.T) {
	// A valid host answer to an expired, unused invitation is positive
	// coverage in TestExpiredChallengeExchangeRearmsForFreshAnswer; every
	// other binding failure keeps its refusal here.
	for _, reason := range []string{"inactive", "operation_digest", "scope", "versions", "consequence", "host_assertion_digest", "malformed_expiry"} {
		t.Run(reason, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
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
				_, err := service.AcceptHostApprovalTx(ctx, tx, probedHost(), inv, assertion, check)
				return err
			})
			if err == nil {
				t.Fatal("invalid challenge binding admitted")
			}
			out := failureEnvelope(agentcontract.NewBase("binding-test", "concord_work_relate", "set_memberships"), err)
			if out.Error.Kind != "approval_invalid" || out.Error.RecoveryAction.Kind != "request_approval" || out.Error.RetrySafe || out.Error.EffectState != agentcontract.EffectNone {
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

// TestExpiredChallengeExchangeRearmsForFreshAnswer is the explicit positive
// coverage for a valid host answer to an expired, unused invitation: one
// fresh affirmative answer to an unchanged decision re-arms the expired
// unused single-use challenge for its original interval and consumes it,
// with no second challenge row.
func TestExpiredChallengeExchangeRearmsForFreshAnswer(t *testing.T) {
	t.Parallel()
	db := openAgentDB(t)
	seedSimpleAuthorityScope(t, db)
	service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
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
	answerAt := fixedTime().Add(2 * time.Hour)
	service.Now = func() time.Time { return answerAt }
	assertion := HostApprovalAssertion{ChallengeRef: check.ApprovalRef, RequestDigest: check.OperationDigest, Scope: approvalScopeBindings(check.Scope), Versions: approvalVersionBindings(check.Versions), SessionRef: inv.SessionRef, AgentRef: inv.AgentRef, Worktree: inv.Worktree, IssuedAt: answerAt.Format(time.RFC3339Nano)}
	var approvalRef string
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		var err error
		approvalRef, err = service.AcceptHostApprovalTx(ctx, tx, probedHost(), inv, assertion, check)
		return err
	}); err != nil {
		t.Fatalf("fresh answer to the expired invitation was refused: %v", err)
	}
	if len(approvalRef) != 64 {
		t.Fatalf("exchange returned approval reference %q", approvalRef)
	}
	var status, issuedAt, expiresAt string
	var used, maxUses int
	if err := db.DatabaseForTesting().QueryRow(`SELECT status, used_count, max_uses, issued_at, expires_at FROM agent_approval_challenges WHERE challenge_ref=?`, check.ApprovalRef).Scan(&status, &used, &maxUses, &issuedAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if status != "consumed" || used != 1 || maxUses != 1 {
		t.Fatalf("re-armed challenge state status=%s used=%d max=%d", status, used, maxUses)
	}
	if issuedAt != fixedTime().Format(time.RFC3339Nano) {
		t.Fatalf("re-arm moved the challenge issue time to %s", issuedAt)
	}
	if expiresAt != answerAt.Add(time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("re-armed deadline = %s, want the original interval from the fresh answer", expiresAt)
	}
	if countRows(t, db.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 1 {
		t.Fatal("late exchange minted a second challenge")
	}
	if countRows(t, db.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals WHERE used_count=1`) != 1 {
		t.Fatal("late exchange did not consume exactly one derived approval")
	}
}

// TestInvalidChallengeIntervalRefusesLateExchange pins closed refusal when
// the stored interval cannot authorize a re-arm: an unparseable stored issue
// time, a nonpositive interval, or an interval beyond the 24-hour creation
// bound on an expired invitation never re-arms, keeps the binding refusal,
// and discloses no stored value.
func TestInvalidChallengeIntervalRefusesLateExchange(t *testing.T) {
	for _, tc := range []struct {
		name, issuedAt, expiresAt, leaked string
	}{
		// The row CHECK compares the stored TEXT values, so the malformed
		// marker sorts before any RFC3339 instant while staying unparseable.
		{"malformed_issue", "0-private-malformed-issue", "", "0-private-malformed-issue"},
		// Equal instants through different offsets: a nonpositive interval.
		{"nonpositive_interval", "2026-08-08T12:00:00Z", "2026-08-08T13:00:00+01:00", ""},
		// 26 hours: beyond the 24-hour creation bound a challenge may arm.
		{"interval_over_creation_bound", "2026-08-07T10:00:00Z", "2026-08-08T12:00:00Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
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
			if tc.expiresAt == "" {
				if _, err := db.DatabaseForTesting().Exec(`UPDATE agent_approval_challenges SET issued_at=? WHERE challenge_ref=?`, tc.issuedAt, check.ApprovalRef); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.DatabaseForTesting().Exec(`UPDATE agent_approval_challenges SET issued_at=?, expires_at=? WHERE challenge_ref=?`, tc.issuedAt, tc.expiresAt, check.ApprovalRef); err != nil {
				t.Fatal(err)
			}
			answerAt := fixedTime().Add(2 * time.Hour)
			service.Now = func() time.Time { return answerAt }
			assertion := HostApprovalAssertion{ChallengeRef: check.ApprovalRef, RequestDigest: check.OperationDigest, Scope: approvalScopeBindings(check.Scope), Versions: approvalVersionBindings(check.Versions), SessionRef: inv.SessionRef, AgentRef: inv.AgentRef, Worktree: inv.Worktree, IssuedAt: answerAt.Format(time.RFC3339Nano)}
			err := db.Transact(ctx, func(tx *store.Transaction) error {
				_, err := service.AcceptHostApprovalTx(ctx, tx, probedHost(), inv, assertion, check)
				return err
			})
			if err == nil {
				t.Fatal("invalid stored interval re-armed an expired invitation")
			}
			out := failureEnvelope(agentcontract.NewBase("interval-test", "concord_work_relate", "set_memberships"), err)
			if out.Error.Kind != "approval_invalid" || out.Error.RecoveryAction.Kind != "request_approval" || out.Error.RetrySafe || out.Error.EffectState != agentcontract.EffectNone {
				t.Fatalf("interval refusal lost its cause: %+v", out.Error)
			}
			if out.Error.Details["boundary"] != "approval_challenge_binding" || out.Error.Details["reason"] != "expired" {
				t.Fatalf("missing exact binding diagnostic: %+v", out.Error.Details)
			}
			wire, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if tc.leaked != "" && strings.Contains(string(wire), tc.leaked) {
				t.Fatal("unparseable stored issue time entered the public diagnostic")
			}
			var status string
			var used int
			var storedIssuedAt, storedExpiresAt string
			if err := db.DatabaseForTesting().QueryRow(`SELECT status, used_count, issued_at, expires_at FROM agent_approval_challenges WHERE challenge_ref=?`, check.ApprovalRef).Scan(&status, &used, &storedIssuedAt, &storedExpiresAt); err != nil {
				t.Fatal(err)
			}
			if status != "active" || used != 0 || countRows(t, db.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
				t.Fatal("interval refusal consumed challenge authority")
			}
			wantExpires := tc.expiresAt
			if wantExpires == "" {
				wantExpires = fixedTime().Add(time.Hour).Format(time.RFC3339Nano)
			}
			if storedIssuedAt != tc.issuedAt || storedExpiresAt != wantExpires {
				t.Fatalf("interval refusal moved the stored window: issued=%s expires=%s", storedIssuedAt, storedExpiresAt)
			}
		})
	}
}

// TestExpiredChallengeWithChangedDecisionBindingRefuses pins that the
// pending-challenge policy re-arms only an unchanged decision: an expired
// invitation whose checked operation digest or scope no longer matches keeps
// the binding refusal, with no deadline change and no consumption.
func TestExpiredChallengeWithChangedDecisionBindingRefuses(t *testing.T) {
	for _, binding := range []string{"operation_digest", "scope"} {
		t.Run(binding, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service, inv, _ := newAuthorizedService(t, db, "client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
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
			// The checked intent moved while the invitation was open, and the
			// host answered for the moved intent.
			switch binding {
			case "operation_digest":
				check.OperationDigest = "sha256:moved-operation"
			case "scope":
				check.Scope = map[string]any{"product_id": "product-other"}
			}
			answerAt := fixedTime().Add(2 * time.Hour)
			service.Now = func() time.Time { return answerAt }
			assertion := HostApprovalAssertion{ChallengeRef: check.ApprovalRef, RequestDigest: check.OperationDigest, Scope: approvalScopeBindings(check.Scope), Versions: approvalVersionBindings(check.Versions), SessionRef: inv.SessionRef, AgentRef: inv.AgentRef, Worktree: inv.Worktree, IssuedAt: answerAt.Format(time.RFC3339Nano)}
			err := db.Transact(ctx, func(tx *store.Transaction) error {
				_, err := service.AcceptHostApprovalTx(ctx, tx, probedHost(), inv, assertion, check)
				return err
			})
			if err == nil {
				t.Fatal("expired invitation re-armed a moved decision")
			}
			out := failureEnvelope(agentcontract.NewBase("moved-decision-test", "concord_work_relate", "set_memberships"), err)
			if out.Error.Kind != "approval_invalid" || out.Error.RecoveryAction.Kind != "request_approval" || out.Error.RetrySafe || out.Error.EffectState != agentcontract.EffectNone {
				t.Fatalf("moved decision refusal lost its cause: %+v", out.Error)
			}
			if out.Error.Details["boundary"] != "approval_challenge_binding" || out.Error.Details["reason"] != "expired" {
				t.Fatalf("missing exact binding diagnostic: %+v", out.Error.Details)
			}
			var status, issuedAt, expiresAt string
			var used int
			if err := db.DatabaseForTesting().QueryRow(`SELECT status, used_count, issued_at, expires_at FROM agent_approval_challenges WHERE challenge_ref=?`, check.ApprovalRef).Scan(&status, &used, &issuedAt, &expiresAt); err != nil {
				t.Fatal(err)
			}
			if status != "active" || used != 0 || issuedAt != fixedTime().Format(time.RFC3339Nano) || expiresAt != fixedTime().Add(time.Hour).Format(time.RFC3339Nano) || countRows(t, db.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
				t.Fatal("moved decision refusal changed the deadline or consumed authority")
			}
		})
	}
}
