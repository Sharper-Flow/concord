package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestCanonicalHostApprovalVector(t *testing.T) {
	raw, err := os.ReadFile("../../adapter/opencode/approval-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		ChallengeRef    string   `json:"challenge_ref"`
		RequestDigest   string   `json:"request_digest"`
		Scope           []string `json:"scope"`
		Versions        []string `json:"versions"`
		SessionRef      string   `json:"session_ref"`
		AgentRef        string   `json:"agent_ref"`
		Worktree        string   `json:"worktree"`
		ClientVersion   string   `json:"client_version"`
		IssuedAt        string   `json:"issued_at"`
		Nonce           string   `json:"nonce"`
		CanonicalBase64 string   `json:"canonical_base64"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	assertion := HostApprovalAssertion{ChallengeRef: vector.ChallengeRef, RequestDigest: vector.RequestDigest, Scope: vector.Scope, Versions: vector.Versions, SessionRef: vector.SessionRef, AgentRef: vector.AgentRef, Worktree: vector.Worktree, ClientVersion: vector.ClientVersion, IssuedAt: vector.IssuedAt, Nonce: vector.Nonce}
	if got := base64.StdEncoding.EncodeToString(CanonicalHostApprovalAssertion(assertion)); got != vector.CanonicalBase64 {
		t.Fatalf("canonical assertion mismatch: got %s want %s", got, vector.CanonicalBase64)
	}
}

func TestCanonicalAssertionArrayVector(t *testing.T) {
	raw, err := os.ReadFile("../../adapter/opencode/grant-assertion-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		ClientRef             string   `json:"client_ref"`
		ClientVersion         string   `json:"client_version"`
		SessionRef            string   `json:"session_ref"`
		AgentRef              string   `json:"agent_ref"`
		Directory             string   `json:"directory"`
		Worktree              string   `json:"worktree"`
		RequestedProductID    string   `json:"requested_product_id"`
		RequestedProjectIDs   []string `json:"requested_project_ids"`
		RequestedCapabilities []string `json:"requested_capabilities"`
		IssuedAt              string   `json:"issued_at"`
		Nonce                 string   `json:"nonce"`
		SurfaceRange          string   `json:"surface_range"`
		EnvelopeVersions      string   `json:"envelope_versions"`
		ManifestDigest        string   `json:"manifest_digest"`
		CanonicalBase64       string   `json:"canonical_base64"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, vector.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := make([]Capability, len(vector.RequestedCapabilities))
	for i, capability := range vector.RequestedCapabilities {
		capabilities[i] = Capability(capability)
	}
	assertion := SignedAssertion{
		ClientRef: vector.ClientRef, ClientVersion: vector.ClientVersion, SessionRef: vector.SessionRef,
		AgentRef: vector.AgentRef, Directory: vector.Directory, Worktree: vector.Worktree,
		RequestedProductID: vector.RequestedProductID, RequestedProjectIDs: vector.RequestedProjectIDs,
		RequestedCapabilities: capabilities, IssuedAt: issuedAt, Nonce: vector.Nonce,
		SurfaceRange: vector.SurfaceRange, EnvelopeVersions: vector.EnvelopeVersions, ManifestDigest: vector.ManifestDigest,
	}
	if got := base64.StdEncoding.EncodeToString(CanonicalAssertion(assertion)); got != vector.CanonicalBase64 {
		t.Fatalf("canonical assertion mismatch: got %s want %s", got, vector.CanonicalBase64)
	}
}

func TestGrantBootstrapAndInvocationBinding(t *testing.T) {
	db := openAgentDB(t)
	now := fixedTime()
	service := NewService(db)
	service.Now = func() time.Time { return now }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(context.Background(), ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	request := grantRequest(privateKey, "nonce-000000000001")
	request.SurfaceVersion = "99.0.0" // core must negotiate, not trust this caller field.
	grant, err := service.IssueGrant(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if grant.SurfaceVersion != ManifestVersion {
		t.Fatalf("server-selected surface = %s, want %s", grant.SurfaceVersion, ManifestVersion)
	}
	var persistedRef string
	if err := db.QueryRow(`SELECT grant_ref FROM agent_grants WHERE grant_hash=?`, sha256Bytes([]byte(grant.Token))).Scan(&persistedRef); err != nil {
		t.Fatal(err)
	}
	if persistedRef == grant.Token {
		t.Fatal("bearer token persisted as grant record id")
	}
	invocation := Invocation{GrantToken: grant.Token, ClientRef: "client-1", ClientVersion: "1.0.0", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", SurfaceVersion: "1.0.0", EnvelopeVersion: "1.0", ManifestDigest: ManifestDigest, RequiredCapability: Capability("product_read"), ProductID: "product-1", ProjectID: "project-1"}
	if _, err := service.ValidateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	invocation.SessionRef = "other-session"
	if _, err := service.ValidateInvocation(context.Background(), invocation); err == nil {
		t.Fatal("wrong session accepted")
	}
	invocation.SessionRef = "session-1"
	invocation.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := service.ValidateInvocation(context.Background(), invocation); err == nil {
		t.Fatal("manifest mismatch accepted")
	}

	wrongPublic, wrongPrivate, _ := ed25519.GenerateKey(cryptorand.Reader)
	_ = wrongPublic
	bad := grantRequest(wrongPrivate, "nonce-000000000002")
	if _, err := service.IssueGrant(context.Background(), bad); err == nil {
		t.Fatal("wrong signing key accepted")
	}
	if _, err := service.IssueGrant(context.Background(), request); err == nil {
		t.Fatal("replayed nonce accepted")
	}
	tampered := grantRequest(privateKey, "nonce-tampered-0001")
	tampered.Assertion.RequestedCapabilities = []Capability{"cross_scope"}
	if _, err := service.IssueGrant(context.Background(), tampered); err == nil {
		t.Fatal("unsigned requested authority mutation accepted")
	}
	for name, mutate := range map[string]func(*SignedAssertion){
		"capability": func(a *SignedAssertion) { a.RequestedCapabilities = []Capability{"cross_scope"} },
		"product":    func(a *SignedAssertion) { a.RequestedProductID = "product-2" },
		"project":    func(a *SignedAssertion) { a.RequestedProjectIDs = []string{"project-2"} },
	} {
		badRequest := grantRequest(privateKey, "nonce-policy-"+name+"-001")
		mutate(&badRequest.Assertion)
		badRequest.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(badRequest.Assertion))
		if _, err := service.IssueGrant(context.Background(), badRequest); err == nil {
			t.Fatalf("disallowed %s authority accepted", name)
		}
	}
	if err := service.UpdateTrustedClientPolicy(context.Background(), "client-1", TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"work_define"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateInvocation(context.Background(), invocation); err == nil {
		t.Fatal("policy update left prior grant active")
	}
	updated := grantRequest(privateKey, "nonce-policy-updated-001")
	if _, err := service.IssueGrant(context.Background(), updated); err == nil {
		t.Fatal("old capability accepted after policy update")
	}
	if err := service.UpdateTrustedClientPolicy(context.Background(), "client-1", TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}); err != nil {
		t.Fatal(err)
	}
	future := grantRequest(privateKey, "nonce-000000000003")
	future.Assertion.IssuedAt = now.Add(service.MaxClockSkew + time.Second)
	future.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(future.Assertion))
	if _, err := service.IssueGrant(context.Background(), future); err == nil {
		t.Fatal("future assertion accepted")
	}

	newPublic, newPrivate, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RotateClientKey(context.Background(), ClientRegistration{ClientRef: "client-1", KeyID: "key-2", PublicKey: newPublic}); err != nil {
		t.Fatal(err)
	}
	rotated := grantRequest(privateKey, "nonce-000000000004")
	if _, err := service.IssueGrant(context.Background(), rotated); err == nil {
		t.Fatal("old key accepted after rotation")
	}
	rotated = grantRequest(newPrivate, "nonce-000000000005")
	if _, err := service.IssueGrant(context.Background(), rotated); err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeClient(context.Background(), "client-1"); err != nil {
		t.Fatal(err)
	}
	revoked := grantRequest(newPrivate, "nonce-000000000006")
	if _, err := service.IssueGrant(context.Background(), revoked); err == nil {
		t.Fatal("revoked client accepted")
	}

	encoded, _ := json.Marshal(grant)
	if strings.Contains(string(encoded), grant.Token) || strings.Contains(fmt.Sprint(grant), grant.Token) {
		t.Fatal("grant token leaked in formatting")
	}
}

func TestGrantUseLimitIsAtomicInsideCallerTransaction(t *testing.T) {
	db := openAgentDB(t)
	service := NewService(db)
	now := fixedTime()
	service.Now = func() time.Time { return now }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(context.Background(), ClientRegistration{ClientRef: "client-1", KeyID: "key", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	request := grantRequest(privateKey, "nonce-race-000001")
	request.MaxUses = 1
	grant, err := service.IssueGrant(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	workers := 8
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			tx, e := db.BeginTx(context.Background(), nil)
			if e != nil {
				results <- e
				return
			}
			_, e = service.ValidateAndConsumeGrantTx(context.Background(), tx, Invocation{GrantToken: grant.Token, ClientRef: "client-1", ClientVersion: "1.0.0", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", SurfaceVersion: "1.0.0", EnvelopeVersion: "1.0", ManifestDigest: ManifestDigest, RequiredCapability: Capability("product_read"), ProductID: "product-1", ProjectID: "project-1"})
			if e == nil {
				e = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			results <- e
		}()
	}
	successes := 0
	for i := 0; i < workers; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful grant consumptions=%d, want 1", successes)
	}
}

func TestApprovalConsumptionIsTransactionBoundAndSingleUse(t *testing.T) {
	db := openAgentDB(t)
	service := NewService(db)
	service.Now = func() time.Time { return fixedTime() }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(context.Background(), ClientRegistration{ClientRef: "client-1", KeyID: "key", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := grantRequest(privateKey, "nonce-approval-0001")
	grant, err := service.IssueGrant(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{GrantToken: grant.Token, ClientRef: grant.ClientRef, ClientVersion: grant.ClientVersion, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, SurfaceVersion: grant.SurfaceVersion, EnvelopeVersion: grant.EnvelopeVersion, ManifestDigest: grant.ManifestDigest, RequiredCapability: Capability("product_read"), HostAssertionDigest: "sha256:host-resolution"}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.CreateApprovalChallengeTx(ctx, tx, invocation, ApprovalChallengeSpec{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", HostAssertionDigest: invocation.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var maxUses, usedCount int
	if err := tx.QueryRow(`SELECT status,max_uses,used_count FROM agent_approval_challenges WHERE challenge_ref=?`, challenge).Scan(&status, &maxUses, &usedCount); err != nil {
		t.Fatal(err)
	}
	if status != "active" || maxUses != 1 || usedCount != 0 {
		t.Fatalf("challenge durability = status %q max_uses %d used_count %d", status, maxUses, usedCount)
	}
	spoof := invocation
	spoof.PrincipalRef = "spoofed"
	if _, err := service.CreateApprovalFromChallengeTx(ctx, tx, spoof, challenge); err == nil {
		t.Fatal("spoofed principal accepted")
	}
	wrongHost := invocation
	wrongHost.HostAssertionDigest = "sha256:other"
	if _, err := service.CreateApprovalFromChallengeTx(ctx, tx, wrongHost, challenge); err == nil {
		t.Fatal("wrong host correlation accepted")
	}
	ref, err := service.CreateApprovalFromChallengeTx(ctx, tx, invocation, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check := ApprovalCheck{ApprovalRef: ref, OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 3}, Consequence: "publication", ClientRef: grant.ClientRef, SessionRef: grant.SessionRef}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateAndConsumeApprovalTx(ctx, tx, ref, check); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateAndConsumeApprovalTx(ctx, tx, ref, check); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateAndConsumeApprovalTx(ctx, tx, ref, check); err == nil {
		t.Fatal("single-use approval reused")
	}
	_ = tx.Rollback()
	wrong := check
	wrong.OperationDigest = "sha256:changed"
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateAndConsumeApprovalTx(ctx, tx, ref, wrong); err == nil {
		t.Fatal("changed approval input accepted")
	}
	_ = tx.Rollback()
}

func grantRequest(privateKey ed25519.PrivateKey, nonce string) GrantRequest {
	assertion := SignedAssertion{ClientRef: "client-1", ClientVersion: "1.0.0", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", RequestedProductID: "product-1", RequestedProjectIDs: []string{"project-1"}, RequestedCapabilities: []Capability{"product_read"}, IssuedAt: fixedTime(), Nonce: nonce, SurfaceRange: "1.0.0-1.0.0", EnvelopeVersions: "1.0", ManifestDigest: ManifestDigest}
	assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(assertion))
	return GrantRequest{Assertion: assertion, SurfaceVersion: "1.0.0", EnvelopeVersion: "1.0", ExpiresAt: fixedTime().Add(time.Hour)}
}

func openAgentDB(t *testing.T) *sql.DB {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.DB()
}
