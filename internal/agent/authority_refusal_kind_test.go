package agent

import (
	"context"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestAuthorityRefusalsCarryTheUnauthorizedKind(t *testing.T) {
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
				in.RequiredCapability = CapabilityWorkerDispatch
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
				service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
					return store.ProjectResolution{ProjectID: "project-1", MainWorktree: true}, nil
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openAgentDB(t)
			seedSimpleAuthorityScope(t, db)
			service, invocation, _ := newAuthorizedService(t, db, "client-1", "principal-1", []Capability{"product_read", "work_define", "work_transition"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
			tc.mutate(&invocation, service)
			_, err := service.Authorize(context.Background(), invocation)
			if err == nil {
				t.Fatal("authorization accepted a refusal case")
			}
			envelope := failureEnvelope(Envelope{}, err)
			if envelope.Error == nil {
				t.Fatal("refusal produced an envelope with no error")
			}
			if envelope.Error.Kind != "unauthorized" {
				t.Fatalf("refusal kind = %q, want %q", envelope.Error.Kind, "unauthorized")
			}
		})
	}
}
