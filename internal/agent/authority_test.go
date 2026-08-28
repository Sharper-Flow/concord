package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func testClientRegistration(client, principal string, capabilities []Capability, products, projects []string) ClientRegistration {
	publicKey, _, _ := ed25519.GenerateKey(cryptorand.Reader)
	return ClientRegistration{ClientRef: client, KeyID: "key-" + client, PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: principal, Capabilities: capabilities, ProductScope: products, ProjectScope: projects}}
}

// newAuthorizedService creates a registered client and a fixed project resolver for a fixture.
func newAuthorizedService(t *testing.T, db *store.Store, client, principal string, capabilities []Capability, products, projects []string, resolution store.ProjectResolution) (*Service, Invocation, Authority) {
	t.Helper()
	ctx := context.Background()
	service := NewService(db)
	service.Now = fixedTime
	service.ProjectResolver = func(context.Context, string, string) (store.ProjectResolution, error) {
		return resolution, nil
	}
	if err := service.RegisterTrustedClient(ctx, testClientRegistration(client, principal, capabilities, products, projects)); err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		ClientRef: client, PrincipalRef: principal, SessionRef: "session-1", AgentRef: "agent-1",
		Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest,
		RequiredCapability: capabilities[0], ProductID: products[0], ProjectID: resolution.ProjectID,
	}
	authority, err := service.Authorize(ctx, invocation)
	if err != nil {
		t.Fatal(err)
	}
	return service, invocation, authority
}

func TestApprovalConsumptionIsTransactionBoundAndSingleUse(t *testing.T) {
	db := openAgentDB(t)
	service := NewService(db)
	service.Now = func() time.Time { return fixedTime() }
	seedSimpleAuthorityScope(t, db)
	if err := service.RegisterTrustedClient(context.Background(), testClientRegistration("client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	service.ProjectResolver = func(context.Context, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1"}, nil
	}
	ctx := context.Background()
	invocation := Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "product_read", HostAssertionDigest: "sha256:host-resolution", ProductID: "product-1", ProjectID: "project-1"}
	var challenge string
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		var err error
		challenge, err = service.CreateApprovalChallengeTx(ctx, tx, invocation, ApprovalChallengeSpec{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", HostAssertionDigest: invocation.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var approval string
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		var err error
		approval, err = service.CreateApprovalFromChallengeTx(ctx, tx, invocation, challenge)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check := ApprovalCheck{ApprovalRef: approval, OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", ClientRef: invocation.ClientRef, SessionRef: invocation.SessionRef}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		if err := service.ValidateAndConsumeApprovalTx(ctx, tx, approval, check); err != nil {
			return err
		}
		return errors.New("sentinel rollback")
	}); err == nil || err.Error() != "sentinel rollback" {
		t.Fatalf("rollback error = %v", err)
	}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		return service.ValidateAndConsumeApprovalTx(ctx, tx, approval, check)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		return service.ValidateAndConsumeApprovalTx(ctx, tx, approval, check)
	}); err == nil {
		t.Fatal("single-use approval reused")
	}
}

func TestDirectApprovalAssertionConsumesExistingApprovalWithoutChallenge(t *testing.T) {
	db := openAgentDB(t)
	seedSimpleAuthorityScope(t, db)
	service, invocation, _ := newAuthorizedService(t, db, "client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	ctx := context.Background()
	invocation.HostAssertionDigest = "sha256:host-resolution"
	const approvalRef = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const operationDigest = "sha256:operation"
	scope := map[string]any{"product_id": "product-1"}
	versions := map[string]any{"work": 3}
	check := ApprovalCheck{ApprovalRef: approvalRef, OperationDigest: operationDigest, Scope: scope, Versions: versions, Consequence: "publication", ClientRef: invocation.ClientRef, SessionRef: invocation.SessionRef}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		return store.InsertApprovalTx(ctx, tx, store.ApprovalInsert{ApprovalRef: approvalRef, OperationDigest: operationDigest, ScopeJSON: `{"product_id":"product-1"}`, VersionJSON: `{"work":3}`, Consequence: check.Consequence, HumanPrincipalRef: invocation.PrincipalRef, ClientRef: invocation.ClientRef, SessionRef: invocation.SessionRef, IssuedAt: fixedTime().Format(time.RFC3339Nano), ExpiresAt: fixedTime().Add(time.Hour).Format(time.RFC3339Nano), MaxUses: 1, ProtectedEvidenceRef: "direct-approval-test", ProtectedEvidenceDigest: "sha256:evidence"})
	}); err != nil {
		t.Fatal(err)
	}
	assertion := HostApprovalAssertion{ChallengeRef: approvalRef, RequestDigest: operationDigest, Scope: approvalScopeBindings(scope), Versions: approvalVersionBindings(versions), SessionRef: invocation.SessionRef, AgentRef: invocation.AgentRef, Worktree: invocation.Worktree, IssuedAt: fixedTime().Format(time.RFC3339Nano)}
	if err := db.Transact(ctx, func(tx *store.Transaction) error {
		isChallenge, err := service.ValidateHostApprovalAssertionTx(ctx, tx, invocation, assertion, check)
		if err != nil {
			return err
		}
		if isChallenge {
			return errors.New("existing approval was treated as a challenge")
		}
		return service.ValidateAndConsumeApprovalTx(ctx, tx, approvalRef, check)
	}); err != nil {
		t.Fatal(err)
	}
	var challenges, used int
	if err := db.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM agent_approval_challenges`).Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if err := db.DatabaseForTesting().QueryRow(`SELECT used_count FROM agent_approvals WHERE approval_ref=?`, approvalRef).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if challenges != 0 || used != 1 {
		t.Fatalf("direct approval state = challenges %d used %d, want 0 and 1", challenges, used)
	}
}

func seedSimpleAuthorityScope(t *testing.T, db *store.Store) {
	t.Helper()
	events := []store.Event{
		{EventID: "authority-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "authority-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Project"}`)},
		{EventID: "authority-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: []byte(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"authority test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(context.Background(), db, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityMethodsGuardNilServiceAndStore(t *testing.T) {
	ctx := context.Background()
	var nilService *Service
	if _, err := nilService.Authorize(ctx, Invocation{}); err == nil {
		t.Fatal("nil service authorization did not fail")
	}
	service := &Service{ProjectResolver: func(context.Context, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1"}, nil
	}}
	if err := service.UpdateTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{PrincipalRef: "principal-1"}); err == nil {
		t.Fatal("nil store policy update did not fail")
	}
	var failure *store.Failure
	if !errors.As(service.UpdateTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{PrincipalRef: "principal-1"}), &failure) || failure.Kind != store.KindUnavailable {
		t.Fatalf("nil store policy failure=%v, want %s", failure, store.KindUnavailable)
	}
}

func TestUnexplainedErrorsRemainInternalErrors(t *testing.T) {
	envelope := failureEnvelope(Envelope{}, errors.New("disk caught fire"))
	if envelope.Error == nil || envelope.Error.Kind != "internal_error" {
		t.Fatalf("unexplained error kind = %v, want internal_error", envelope.Error)
	}
}

func openAgentDB(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
