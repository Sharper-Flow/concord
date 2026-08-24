package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/storetest"
)

// The invoke envelope accepts one bearer identifier, named for the bearer.
// Supplying the record grant_ref where the token belongs used to fail closed
// with the generic message "unknown grant", which left the caller unable to
// tell whether they had used the wrong identifier or sent a corrupted token.
// The new refusal names the expected identifier so the operator can correct
// the call rather than retrying blindly.
func TestInvokeEnvelopeRefusesRecordGrantRefAsBearerToken(t *testing.T) {
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	service := NewService(s)
	service.Now = func() time.Time { return fixedTime() }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	grant, err := service.IssueGrant(ctx, grantRequest(privateKey, "token-field-nonce-0001"))
	if err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		GrantToken:         grant.RecordID,
		ClientRef:          grant.ClientRef,
		PrincipalRef:       grant.PrincipalRef,
		SessionRef:         grant.SessionRef,
		AgentRef:           grant.AgentRef,
		Directory:          grant.Directory,
		Worktree:           grant.Worktree,
		ManifestDigest:     grant.ManifestDigest,
		RequiredCapability: Capability("product_read"),
		ProductID:          "product-1",
		ProjectID:          "project-1",
	}
	_, err = service.ValidateInvocation(ctx, invocation)
	if err == nil {
		t.Fatal("record grant_ref accepted as the bearer grant_token")
	}
	if !strings.Contains(err.Error(), "grant_token") || !strings.Contains(err.Error(), "grant_ref") {
		t.Fatalf("refusal %q does not name both identifiers", err)
	}
}

// DecodeInvokeRequest rejects unknown fields strictly. After the rename,
// call_envelope.grant_ref is no longer accepted: a caller that still sends
// the old key is refused at decode time with a typed "unknown field" error
// rather than silently falling back to the token.
func TestInvokeEnvelopeRejectsLegacyGrantRefKey(t *testing.T) {
	raw := []byte(`{"call_envelope":{"schema_version":"1.0","request_id":"r","grant_ref":"` + strings.Repeat("a", 64) + `","client_ref":"c","scope_version":"","manifest_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"tool":"concord_product_view","operation":"resolve","input":{}}`)
	_, _, err := DecodeInvokeRequest(raw)
	if err == nil {
		t.Fatal("decode accepted legacy grant_ref key")
	}
	if !strings.Contains(err.Error(), "grant_ref") {
		t.Fatalf("decode error %q does not name the rejected field", err)
	}
}
