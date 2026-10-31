package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// testFixtureAgents is the closed set of agent references this package's
// fixtures present. The agent scope is fail-closed, so a fixture client must
// name every agent whose invocations it is expected to authorize.
var testFixtureAgents = []string{"agent", "agent-1", "agent-engineer", "agent-evaluator", "agent-exec", "agent-other"}

func testClientRegistration(client, principal string, capabilities []agentcontract.Capability, products, projects []string) ClientRegistration {
	publicKey, _, _ := ed25519.GenerateKey(cryptorand.Reader)
	return ClientRegistration{ClientRef: client, KeyID: "key-" + client, PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: principal, Capabilities: capabilities, ProductScope: products, ProjectScope: projects, AgentScope: testFixtureAgents}}
}

// resolveProjectAuthority installs the prober/matcher pair every
// authorization reads: the prober reports the fixture host with no
// transaction open, and the matcher answers with the resolution using SQL
// only, mirroring the CLI wiring (CD-0195 D2).
// probedHost is the fixture value a successful host probe returns.
func probedHost() store.ResolvedProjectHost {
	return store.ResolvedProjectHost{Canonical: "/repo", Host: store.HostRepository{CanonicalPath: "/repo"}}
}

func resolveProjectAuthority(service *Service, resolution store.ProjectResolution) {
	service.ProjectHostProber = func(context.Context, string, string) (store.ResolvedProjectHost, error) {
		return store.ResolvedProjectHost{Canonical: "/repo", Host: store.HostRepository{CanonicalPath: "/repo"}}, nil
	}
	service.ProjectHostMatcher = func(context.Context, *store.Transaction, store.ResolvedProjectHost) (store.ProjectResolution, error) {
		return resolution, nil
	}
}

// newAuthorizedService creates a registered client and a fixed project resolver for a fixture.
func newAuthorizedService(t *testing.T, db *store.Store, client, principal string, capabilities []agentcontract.Capability, products, projects []string, resolution store.ProjectResolution) (*Service, Invocation, Authority) {
	t.Helper()
	ctx := context.Background()
	service := NewService(db)
	service.Now = fixedTime
	resolveProjectAuthority(service, resolution)
	if err := service.RegisterTrustedClient(ctx, testClientRegistration(client, principal, capabilities, products, projects)); err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		ClientRef: client, PrincipalRef: principal, SessionRef: "session-1", AgentRef: "agent-1",
		Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest,
		RequiredCapability: capabilities[0], ProductID: products[0], ProjectID: resolution.ProjectID,
	}
	authority, err := service.Authorize(ctx, invocation)
	if err != nil {
		t.Fatal(err)
	}
	return service, invocation, authority
}

// The agent reference arrives from the caller and is written straight into
// attribution, so it needs the standing the Product and the Project already
// have: a trusted client names the agents it may present. The scope is
// fail-closed, so a client that names none authorizes none. That is the state
// every client registered before the scope existed holds, and TS5
// additive expansion is the remedy: it grants the missing agents while every
// existing grant and the stored principal survive.
func TestAgentScopeBoundsTheAgentAClientMayPresent(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		scope      []string
		agent      string
		authorized bool
		wants      []string
	}{
		{"a named agent is authorized", []string{"agent-1", "agent-2"}, "agent-1", true, nil},
		{"an unnamed agent is refused", []string{"agent-1"}, "agent-2", false, []string{`agent "agent-2"`, `trusted client "client-1"`, "client-policy-expand"}},
		{"an empty scope authorizes no agent", nil, "agent-1", false, []string{`trusted client "client-1"`, "empty agent scope", "client-policy-expand"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service := NewService(db)
			service.Now = fixedTime
			resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1"})
			publicKey, _, err := ed25519.GenerateKey(cryptorand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			registration := ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: TrustedClientPolicy{
				PrincipalRef: "human-1", Capabilities: []agentcontract.Capability{"product_read"},
				ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}, AgentScope: testCase.scope,
			}}
			if err := service.RegisterTrustedClient(ctx, registration); err != nil {
				t.Fatal(err)
			}
			_, err = service.Authorize(ctx, Invocation{
				ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: testCase.agent,
				Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest,
				RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1",
			})
			if testCase.authorized {
				if err != nil {
					t.Fatalf("agent %q named in the scope was refused: %v", testCase.agent, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("agent %q outside the scope was authorized", testCase.agent)
			}
			for _, want := range testCase.wants {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal = %v, want it to name %q", err, want)
				}
			}
		})
	}
}

func seedSimpleAuthorityScope(t *testing.T, db *store.Store) {
	t.Helper()
	events := []store.Event{
		{EventID: "authority-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "authority-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: []byte(`{"display_name":"Project"}`)},
		{EventID: "authority-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: []byte(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"authority test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(context.Background(), db, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityMethodsGuardNilServiceAndStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var nilService *Service
	if _, err := nilService.Authorize(ctx, Invocation{}); err == nil {
		t.Fatal("nil service authorization did not fail")
	}
	service := &Service{Store: nil}
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1"})
	if err := service.UpdateTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{PrincipalRef: "principal-1"}); err == nil {
		t.Fatal("nil store policy update did not fail")
	}
	var failure *store.Failure
	if !errors.As(service.UpdateTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{PrincipalRef: "principal-1"}), &failure) || failure.Kind != store.KindUnavailable {
		t.Fatalf("nil store policy failure=%v, want %s", failure, store.KindUnavailable)
	}
}

func TestUnexplainedErrorsRemainInternalErrors(t *testing.T) {
	t.Parallel()
	envelope := failureEnvelope(agentcontract.Envelope{}, errors.New("disk caught fire"))
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
