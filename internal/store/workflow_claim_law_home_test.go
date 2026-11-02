package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutionClaimNeedsNoLawHomeProof(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const workID = "claim-without-law-home"
	s := seedD7BoundaryOverlap(t, workID, "claim-home-peer", "execution")
	home := workflowLawFixtureRepo(t, s)
	if err := os.Rename(home, filepath.Join(t.TempDir(), "unavailable-home")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareWorkLawProof(ctx, workID); err == nil {
		t.Fatal("fixture law home remains verifiable")
	}
	var ref, digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &digest); err != nil {
		t.Fatal(err)
	}
	req := ClaimRequest{
		OpID: "op:claim-home", WorkID: workID, WorkflowTypeRef: ref, WorkflowTypeDigest: digest,
		StepID: "execution", StepKind: StepExternalEffect, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64),
		AcceptedScopeSnapshot: `{"work_id":"` + workID + `"}`, PrincipalRef: "principal/operator", Tool: "concord_work_transition",
		IdempotencyKey: "claim-home", RequestID: "request:claim-home", ObservedAt: time.Unix(1, 0).UTC(), ContractDigest: testManifestDigest,
	}
	denied := errors.New("claim authorization denied")
	if _, err := ClaimStepAuthorized(ctx, s, req, func(*Transaction) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("claim authorization = %v, want %v", err, denied)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, req.OpID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied claim persisted: count=%d err=%v", count, err)
	}
	authorized := 0
	first, err := ClaimStepAuthorized(ctx, s, req, func(*Transaction) error { authorized++; return nil })
	if err != nil || first.AttemptEpoch != 1 || authorized != 1 {
		t.Fatalf("authorized claim = %+v, %v; authorizations=%d", first, err, authorized)
	}
	replay, err := ClaimStepAuthorized(ctx, s, req, nil)
	if err != nil || !replay.Replayed || replay.AttemptEpoch != first.AttemptEpoch {
		t.Fatalf("claim replay = %+v, %v", replay, err)
	}
	req.IdempotencyKey = "claim-home-next"
	next, err := ClaimStepAuthorized(ctx, s, req, nil)
	if err != nil || next.AttemptEpoch != 2 {
		t.Fatalf("next fenced claim = %+v, %v", next, err)
	}
}
