package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func serializeSelectorRefusal(t *testing.T, failure *store.Failure) []byte {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "selector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	service := agent.NewService(s)
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if err := service.RegisterTrustedClient(ctx, agent.ClientRegistration{
		ClientRef: "selector-client", KeyID: "selector-key", PublicKey: key.Public().(ed25519.PublicKey),
		Policy: agent.TrustedClientPolicy{PrincipalRef: "selector-operator", Capabilities: []agentcontract.Capability{"product_read"}, AgentScope: []string{"selector-primary"}},
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.ProjectHostProber = func(context.Context, string, string) (store.ResolvedProjectHost, error) {
		calls++
		return store.ResolvedProjectHost{}, failure
	}
	service.ProjectHostMatcher = func(context.Context, *store.Transaction, store.ResolvedProjectHost) (store.ProjectResolution, error) {
		t.Fatal("refused selector reached the host matcher")
		return store.ProjectResolution{}, nil
	}
	raw, err := json.Marshal(map[string]any{
		"tool": "concord_product_view", "operation": "resolve", "input": map[string]any{},
		"call_envelope": agent.CallEnvelope{
			SchemaVersion: "1.0", RequestID: "selector-refusal", ClientRef: "selector-client",
			PrincipalRef: "selector-operator", SessionRef: "selector-session", AgentRef: "selector-primary",
			ManifestDigest: agentcontract.ManifestDigest, ScopeVersion: "selector-scope",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := agent.Invoke(ctx, s, service, raw)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || out.Error == nil || out.Error.Kind != "unreachable" || out.Error.EffectState != agentcontract.EffectNone || out.Error.RetrySafe {
		t.Fatalf("selector refusal changed classification or effect: calls=%d error=%+v", calls, out.Error)
	}
	encoded, err := out.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
