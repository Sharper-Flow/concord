package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// This external-package harness drives the public byte boundary. The host probe
// injects a store refusal so the assertion tests projection, not a second
// implementation of the store's admission rules.
func invokeRefusal(t *testing.T, failure *store.Failure) (agentcontract.Envelope, []byte) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	service := agent.NewService(s)
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if err := service.RegisterTrustedClient(ctx, agent.ClientRegistration{
		ClientRef: "oracle-client", KeyID: "oracle-key", PublicKey: key.Public().(ed25519.PublicKey),
		Policy: agent.TrustedClientPolicy{PrincipalRef: "oracle-operator", Capabilities: []agentcontract.Capability{"product_read"}, AgentScope: []string{"oracle-primary"}},
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.ProjectHostProber = func(context.Context, string, string) (store.ResolvedProjectHost, error) {
		calls++
		return store.ResolvedProjectHost{}, failure
	}
	service.ProjectHostMatcher = func(context.Context, *store.Transaction, store.ResolvedProjectHost) (store.ProjectResolution, error) {
		t.Fatal("refused host probe reached the matcher")
		return store.ProjectResolution{}, nil
	}
	raw, err := json.Marshal(map[string]any{
		"tool": "concord_product_view", "operation": "resolve", "input": map[string]any{},
		"call_envelope": agent.CallEnvelope{
			SchemaVersion: "1.0", RequestID: "oracle-refusal", ClientRef: "oracle-client",
			PrincipalRef: "oracle-operator", SessionRef: "oracle-session", AgentRef: "oracle-primary",
			ManifestDigest: agentcontract.ManifestDigest, ScopeVersion: "oracle-scope",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := agent.Invoke(ctx, s, service, raw)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || out.Error == nil || out.Error.Kind != "missing_evidence" || out.Error.RecoveryAction.Kind != "provide_evidence" || out.Error.EffectState != agentcontract.EffectNone || out.Error.RetrySafe {
		t.Fatalf("typed refusal changed: calls=%d error=%+v", calls, out.Error)
	}
	encoded, err := out.Encode()
	if err != nil {
		t.Fatalf("refusal cannot cross the model boundary: %v", err)
	}
	return out, encoded
}

func TestRefusalRemedyIsModelVisible(t *testing.T) {
	const detail = "preparation requires clean HEAD equal to the current core subject"
	const remedy = `run worktree_verify with command ["git","rev-parse","--verify","HEAD^{commit}"] and a fresh request identity, then refresh context`
	out, encoded := invokeRefusal(t, &store.Failure{Kind: store.KindMissingEvidence, Op: "worker_oracle", Detail: detail, RecoveryAction: remedy})
	// Decode the serialized response: an in-memory field that never crosses the
	// wire is not a model-visible remedy.
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	errorWire, _ := wire["error"].(map[string]any)
	prose, err := json.Marshal(errorWire)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(remedy)
	if !strings.Contains(string(prose), string(want[1:len(want)-1])) {
		t.Fatalf("serialized refusal lost the authored remedy: %s", encoded)
	}
	if !strings.Contains(out.Error.Message, detail) {
		t.Fatalf("refusal lost its cause: %+v", out.Error)
	}
}

func TestRefusalRemedyKeepsEnvelopeBounds(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 2000), strings.Repeat("界", 1000)} {
		out, encoded := invokeRefusal(t, &store.Failure{Kind: store.KindMissingEvidence, Detail: text, RecoveryAction: text})
		if !utf8.Valid(encoded) || !json.Valid(encoded) || len(encoded) > agentcontract.MaxResultEnvelopeBytes || len(out.Error.Message) > 1000 {
			t.Fatalf("refusal escaped existing output bounds: message=%d envelope=%d", len(out.Error.Message), len(encoded))
		}
	}
}
