package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestAuthorityRefusalsCarryTheUnauthorizedKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*Invocation, *Service)
	}{
		{
			name: "missing client",
			mutate: func(in *Invocation, _ *Service) {
				in.ClientRef = "missing-client"
			},
		},
		{
			name: "principal mismatch",
			mutate: func(in *Invocation, _ *Service) {
				in.PrincipalRef = "other-principal"
			},
		},
		{
			name: "manifest mismatch",
			mutate: func(in *Invocation, _ *Service) {
				in.ManifestDigest = "wrong-manifest"
			},
		},
		{
			name: "capability missing",
			mutate: func(in *Invocation, _ *Service) {
				in.RequiredCapability = "work_transition"
			},
		},
		{
			name: "product outside policy",
			mutate: func(in *Invocation, _ *Service) {
				in.ProductID = "product-outside-policy"
			},
		},
		{
			name: "project outside resolved scope",
			mutate: func(in *Invocation, _ *Service) {
				in.ProjectID = "project-outside-scope"
			},
		},
		{
			name: "main worktree mutation",
			mutate: func(in *Invocation, service *Service) {
				in.RequiredCapability = "work_transition"
				resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			capabilities := []agentcontract.Capability{"product_read", "work_define"}
			if tc.name != "capability missing" {
				capabilities = append(capabilities, "work_transition")
			}
			service, invocation, _ := newAuthorizedService(t, db, "client-1", "principal-1", capabilities, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
			tc.mutate(&invocation, service)
			_, err := service.Authorize(context.Background(), invocation)
			if err == nil {
				t.Fatal("authorization accepted a refusal case")
			}
			envelope := failureEnvelope(agentcontract.Envelope{}, err)
			if envelope.Error == nil {
				t.Fatal("refusal produced an envelope with no error")
			}
			if envelope.Error.Kind != "unauthorized" {
				t.Fatalf("refusal kind = %q, want %q", envelope.Error.Kind, "unauthorized")
			}
		})
	}
}

func TestApprovalChallengeVersionValidityRefusalNamesVersions(t *testing.T) {
	db := openAgentDB(t)
	service := NewService(db)
	service.Now = fixedTime
	seedSimpleAuthorityScope(t, db)
	if err := service.RegisterTrustedClient(context.Background(), testClientRegistration("client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1"})
	invocation := Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "product_read", HostAssertionDigest: "sha256:host-resolution", ProductID: "product-1", ProjectID: "project-1"}
	err := db.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := service.CreateApprovalChallengeTx(context.Background(), tx, probedHost(), invocation, ApprovalChallengeSpec{OperationDigest: "sha256:operation", Scope: map[string]any{"product_id": "product-1"}, Versions: map[string]any{"work": 0}, Consequence: "internal_sqlite", HostAssertionDigest: invocation.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
		return err
	})
	if err == nil {
		t.Fatal("challenge with work version 0 was minted, want a refusal")
	}
	if !strings.Contains(err.Error(), "approval challenge versions invalid") {
		t.Fatalf("refusal = %v, want it to name version validity", err)
	}
	if strings.Contains(err.Error(), "scope") {
		t.Fatalf("refusal = %v, want no scope-authority attribution", err)
	}
}
