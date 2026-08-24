package agent

import (
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// Grant validation refuses for four distinct reasons. Each is the
// authorization boundary working as designed, so each must reach the caller as
// `unauthorized`. Reporting a refusal as `internal_error` tells an agent that
// Concord malfunctioned, pins recovery to `contact_operator`, and marks the
// request unsafe to retry — which is how CD-0017 D4's nested-worker refusal
// and CD-0059 D3's non-grantable capability would both present today.
//
// The assertion is on the envelope kind rather than the message text, because
// the message is not the contract and a test that reads it cannot fail when
// the kind is wrong (issue #437).
func TestGrantRefusalsCarryTheUnauthorizedKind(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*store.GrantRecord, *Invocation)
		detail string
	}{
		{
			name:   "capability missing",
			detail: "grant capability missing",
			mutate: func(_ *store.GrantRecord, in *Invocation) {
				in.RequiredCapability = Capability("worker_dispatch")
			},
		},
		{
			name:   "grant expired",
			detail: "grant expired or revoked",
			mutate: func(record *store.GrantRecord, _ *Invocation) {
				record.ExpiresAt = "2025-01-01T00:00:00Z"
			},
		},
		{
			name:   "binding mismatch",
			detail: "invocation binding mismatch",
			mutate: func(_ *store.GrantRecord, in *Invocation) {
				in.Worktree = "/somewhere-else"
			},
		},
		{
			name:   "use limit reached",
			detail: "grant use limit reached",
			mutate: func(record *store.GrantRecord, _ *Invocation) {
				record.MaxUses = 1
				record.UsedCount = 1
			},
		},
		{
			name:   "scope snapshot unreadable",
			detail: "grant scope snapshot is unreadable",
			mutate: func(record *store.GrantRecord, _ *Invocation) {
				record.ScopeSnapshotJSON = "{not json"
			},
		},
		{
			name:   "product outside grant scope",
			detail: "product outside grant scope",
			mutate: func(_ *store.GrantRecord, in *Invocation) {
				in.ProductID = "product-outside-scope"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, invocation := validGrantFixture("2030-01-01T00:00:00Z")
			tc.mutate(&record, &invocation)
			service := &Service{}
			_, err := service.validateGrantRecord(record, invocation, mustTime(t, "2026-01-01T00:00:00Z"))
			if err == nil {
				t.Fatal("grant validation accepted a record it must refuse")
			}
			envelope := failureEnvelope(Envelope{}, err)
			if envelope.Error == nil {
				t.Fatal("refusal produced an envelope with no error")
			}
			if envelope.Error.Kind != "unauthorized" {
				t.Fatalf("refusal kind = %q, want %q (detail: %s)", envelope.Error.Kind, "unauthorized", err)
			}
			if envelope.Error.Message != tc.detail {
				t.Fatalf("refusal message = %q, want %q", envelope.Error.Message, tc.detail)
			}
		})
	}
}

// internal_error must stay reachable for outcomes Concord cannot explain, so
// the fix must not blanket-map every error out of this package.
func TestUnexplainedErrorsRemainInternalErrors(t *testing.T) {
	envelope := failureEnvelope(Envelope{}, errors.New("disk caught fire"))
	if envelope.Error == nil || envelope.Error.Kind != "internal_error" {
		t.Fatalf("unexplained error kind = %v, want internal_error", envelope.Error)
	}
}
