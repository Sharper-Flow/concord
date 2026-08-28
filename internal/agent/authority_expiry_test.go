package agent

import (
	"context"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestUnparseableStoredExpiryFailsClosed(t *testing.T) {
	db := openAgentDB(t)
	seedSimpleAuthorityScope(t, db)
	service, invocation, _ := newAuthorizedService(t, db, "client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	ctx := context.Background()
	const approvalRef = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	check := ApprovalCheck{ApprovalRef: approvalRef, OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", ClientRef: invocation.ClientRef, SessionRef: invocation.SessionRef}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		return store.InsertApprovalTx(ctx, tx, store.ApprovalInsert{ApprovalRef: approvalRef, OperationDigest: check.OperationDigest, ScopeJSON: `{"product_id":"product-1"}`, VersionJSON: `{"work":3}`, Consequence: check.Consequence, HumanPrincipalRef: invocation.PrincipalRef, ClientRef: invocation.ClientRef, SessionRef: invocation.SessionRef, IssuedAt: fixedTime().Format(time.RFC3339Nano), ExpiresAt: "not-a-timestamp", MaxUses: 1, ProtectedEvidenceRef: "expiry-test", ProtectedEvidenceDigest: "sha256:evidence"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		return service.ValidateAndConsumeApprovalTx(ctx, tx, approvalRef, check)
	}); err == nil {
		t.Fatal("an approval with an unparseable stored expiry was accepted")
	}
}
