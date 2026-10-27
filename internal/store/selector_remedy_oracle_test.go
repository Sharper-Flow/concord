package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
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
	if calls != 1 || out.Error == nil || out.Error.Kind != "unreachable" || out.Error.EffectState != agent.EffectNone || out.Error.RetrySafe {
		t.Fatalf("selector refusal changed classification or effect: calls=%d error=%+v", calls, out.Error)
	}
	encoded, err := out.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestNativeSelectorRemedyVisible(t *testing.T) {
	for _, runner := range []string{"check", "pytest"} {
		t.Run(runner, func(t *testing.T) {
			_, err := store.NativeSelectorRemedyFixture(runner, []string{runner, "zeta", "alpha"})
			var failure *store.Failure
			if !errors.As(err, &failure) || failure.Kind != store.KindUnavailable {
				t.Fatalf("unsorted selector must still refuse: %v", err)
			}
			encoded := serializeSelectorRefusal(t, failure)
			var wire struct {
				Error struct {
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			remedy, _ := wire.Error.Details["recovery_detail"].(string)
			argv, _ := json.Marshal([]string{runner, "alpha", "zeta"})
			if !strings.Contains(remedy, string(argv)) || !strings.Contains(remedy, "record_worker_job") || !strings.Contains(remedy, "prepar") {
				t.Fatalf("serialized refusal omits the sorted selector or recorded revision/preparation route: %s", encoded)
			}
		})
	}
}

func TestNativeSelectorRemedyCallable(t *testing.T) {
	for _, runner := range []string{"check", "pytest"} {
		got, err := store.NativeSelectorRemedyFixture(runner, []string{runner, "alpha", "zeta"})
		if err != nil || !slices.Equal(got, []string{"alpha", "zeta"}) {
			t.Fatalf("advertised sorted selector is not callable: %v, %v", got, err)
		}
		for _, argv := range [][]string{{runner, "alpha"}, {runner, "alpha", "zeta", "extra"}, {"other", "alpha", "zeta"}} {
			if _, err := store.NativeSelectorRemedyFixture(runner, argv); err == nil {
				t.Fatalf("incorrect selector became admitted: %v", argv)
			}
		}
	}
}
