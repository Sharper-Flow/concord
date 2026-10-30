package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// serializeRemedyRefusal projects one genuine store refusal across the public
// byte boundary and returns the encoded envelope. It tests projection of the
// store's own refusal, never a reimplementation of admission.
func serializeRemedyRefusal(t *testing.T, failure *store.Failure, wantKind string) []byte {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "remedy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	service := agent.NewService(s)
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if err := service.RegisterTrustedClient(ctx, agent.ClientRegistration{
		ClientRef: "remedy-client", KeyID: "remedy-key", PublicKey: key.Public().(ed25519.PublicKey),
		Policy: agent.TrustedClientPolicy{PrincipalRef: "remedy-operator", Capabilities: []agentcontract.Capability{"product_read"}, AgentScope: []string{"remedy-primary"}},
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.ProjectHostProber = func(context.Context, string, string) (store.ResolvedProjectHost, error) {
		calls++
		return store.ResolvedProjectHost{}, failure
	}
	service.ProjectHostMatcher = func(context.Context, *store.Transaction, store.ResolvedProjectHost) (store.ProjectResolution, error) {
		t.Fatal("refused probe reached the host matcher")
		return store.ProjectResolution{}, nil
	}
	raw, err := json.Marshal(map[string]any{
		"tool": "concord_product_view", "operation": "resolve", "input": map[string]any{},
		"call_envelope": agent.CallEnvelope{
			SchemaVersion: "1.0", RequestID: "remedy-refusal", ClientRef: "remedy-client",
			PrincipalRef: "remedy-operator", SessionRef: "remedy-session", AgentRef: "remedy-primary",
			ManifestDigest: agentcontract.ManifestDigest, ScopeVersion: "remedy-scope",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := agent.Invoke(ctx, s, service, raw)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || out.Error == nil || out.Error.Kind != wantKind || out.Error.EffectState != agentcontract.EffectNone || out.Error.RetrySafe {
		t.Fatalf("typed refusal changed: calls=%d error=%+v", calls, out.Error)
	}
	encoded, err := out.Encode()
	if err != nil {
		t.Fatalf("refusal cannot cross the model boundary: %v", err)
	}
	return encoded
}

func serializedRemedyDetail(t *testing.T, encoded []byte) string {
	t.Helper()
	var wire struct {
		Error struct {
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	remedy, _ := wire.Error.Details["recovery_detail"].(string)
	if remedy == "" {
		t.Fatalf("serialized refusal lost the authored remedy: %s", encoded)
	}
	return remedy
}

func TestBootstrapOriginRemedyVisible(t *testing.T) {
	failure := store.BootstrapOriginRefusalFixture(t)
	if failure.Kind != store.KindInvalidOperation {
		t.Fatalf("bootstrap origin refusal kind changed: %+v", failure)
	}
	encoded := serializeRemedyRefusal(t, failure, "invalid_input")
	var wire struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wire.Error.Message, "linked bootstrap origin is not an active Concord worktree") {
		t.Fatalf("refusal lost its cause: %s", encoded)
	}
	remedy := serializedRemedyDetail(t, encoded)
	for _, want := range []string{"capture", "default checkout", "resume", "active worktree"} {
		if !strings.Contains(remedy, want) {
			t.Fatalf("serialized bootstrap remedy lost %q: %s", want, encoded)
		}
	}
}
