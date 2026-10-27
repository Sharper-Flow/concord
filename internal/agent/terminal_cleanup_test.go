package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func publicationContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Bind the cancellation judgement through its lifecycle owner before a workflow
// fixture is initialized. No evidence projection or receipt is seeded directly.
func reviewCancelledReceipt(t *testing.T, s *store.Store, hash string) {
	t.Helper()
	for i, kind := range []string{"work.reopened", "work.transitioned"} {
		_, version := readWorkFromStore(t, s, "work-cancelled")
		fields := map[string]any{"from": "cancelled", "reason": "reviewed cancellation", "expected_version": version, "resulting_version": version + 1}
		if i == 1 {
			fields["from"], fields["to"], fields["evidence_refs"] = "needed", "cancelled", []string{hash}
		}
		payload, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyOperation(t.Context(), s, store.Operation{Events: []store.Event{{EventID: fmt.Sprintf("review-cancelled-%d-%d", version, i), Kind: kind, SubjectType: store.SubjectWorkItem, SubjectID: "work-cancelled", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-cancelled"): version}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CheckReviewedClosureContentHash(t.Context(), "work-cancelled", hash); err != nil {
		t.Fatal(err)
	}
}

func cancelledPublicationContent(t *testing.T, publication cancelledNotePublication) compactPublishInput {
	t.Helper()
	var input compactPublishInput
	if err := json.Unmarshal(publication.input, &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func holdCompactionCleanup(t *testing.T, s *store.Store) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES('cleanup-claim','work-cancelled','proj-api',?,'work/closed',?,'/synthetic/work','pending','operator','fixture','now','now')`, store.WorktreeSetID("work-cancelled"), strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCleanupAfterCompactionSettlement(t *testing.T) {
	for _, route := range []string{"publish", "reconcile", "cached_publish", "claimed_publish", "completed_reconcile"} {
		t.Run(route, func(t *testing.T) {
			s, service, grant, key, home := agentJobsCompactionFixture(t)
			receipt := cancelledPublicationContent(t, newCancelledNotePublication(t, s, grant, home, "reviewed-receipt"))
			reviewCancelledReceipt(t, s, receipt.ContentDigest)
			initializeMutationPinWorkflow(t, s, "work-cancelled", store.BuiltinWorkflowDefinitions()[0])
			publication := newCancelledNotePublication(t, s, grant, home, "compact-cancelled-approval-roundtrip-1")
			if route != "publish" && route != "reconcile" {
				holdCompactionCleanup(t, s)
			}
			if route == "reconcile" {
				service.publicationObserver = func(phase string) error {
					if phase == "verify_commit" {
						return context.Canceled
					}
					return nil
				}
			}
			response := publishCancelledNote(t, s, service, grant, key, home)
			if route == "publish" {
				if response.Outcome != OutcomeOK {
					t.Fatalf("publication settlement: %+v", response)
				}
			} else {
				var operationID string
				if err := s.DatabaseForTesting().QueryRow(`SELECT op_id FROM durable_operations WHERE work_id='work-cancelled'`).Scan(&operationID); err != nil {
					t.Fatal(err)
				}
				if route != "reconcile" && (response.Outcome != OutcomeOK || len(response.Warnings) == 0 || response.Warnings[0].Kind != "resource_busy") {
					t.Fatalf("deferred cleanup must preserve publication success and typed blocker: %+v", response)
				}
				if _, err := s.DatabaseForTesting().Exec(`DELETE FROM worktree_claims WHERE op_id='cleanup-claim'`); err != nil {
					t.Fatal(err)
				}
				service.publicationObserver = nil
				env := agentJobsMutationEnvelope(t, s, grant, "proj-api")
				input, _ := json.Marshal(map[string]any{"operation_id": operationID, "expected_operation_version": 1, "idempotency_key": "cleanup-reconcile"})
				if route == "cached_publish" || route == "claimed_publish" {
					// CD-0243 D3/D7 retire publication-only consent, not
					// reviewed-content or cleanup gates. Replay the original request.
					input, env = publication.input, publication.env
					if route == "cached_publish" {
						response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "publish", Input: input}, env)
					} else {
						r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_compact", Operation: "publish"}
						op, _ := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
						var err error
						response, err = r.mutateCompaction(t.Context(), NewBase(env.RequestID, r.Tool, r.Operation), input, mutationDigest(r.Tool, r.Operation, env, input), op)
						if err != nil {
							t.Fatal(err)
						}
					}
				} else {
					response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}, env)
				}
				if response.Outcome != OutcomeOK {
					t.Fatalf("settled %s must succeed: %+v", route, response)
				}
			}
			var count int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id='work-cancelled'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s retained execution: count=%d err=%v warnings=%+v", route, count, err, response.Warnings)
			}
			var result struct {
				Pins []store.WorkPin `json:"work_pins"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil || len(result.Pins) != 0 || len(response.Omissions) != 1 || response.Omissions[0].Kind != "work_pin_unavailable" {
				t.Fatalf("cleaned result must omit live pins: result=%s omissions=%+v err=%v", response.Result, response.Omissions, err)
			}
			for _, operation := range []string{"publish", "reconcile"} {
				request := publication.request()
				if operation == "reconcile" {
					input, _ := json.Marshal(map[string]any{"work_id": "work-cancelled", "expected_work_version": publication.versions["work"], "idempotency_key": "collected-reconcile"})
					request = InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}
				}
				late := dispatchMutation(t, s, service, request, publication.env)
				if late.Outcome != OutcomeError || late.Error == nil || late.Error.Kind != "unknown_scope" || !strings.Contains(late.Error.Message, "canonical outcome:") {
					t.Fatalf("late %s lost canonical absence: %+v", operation, late)
				}
			}
			locator, commit, ok := archivedWorkLocator(t, s)
			if !ok {
				t.Fatal("settled publication lost its canonical locator")
			}
			note, err := store.VerifyCommittedNote(t.Context(), home.RepoPath, commit, locator, receipt.ContentDigest)
			if err != nil || string(note.Content) != receipt.Content {
				t.Fatalf("publication changed reviewed caller bytes: %v", err)
			}
		})
	}
}

func TestTerminalCleanupPublicationRejectsUnreviewedBytes(t *testing.T) {
	for _, tc := range []struct {
		name           string
		approved       bool
		reviewOriginal bool
		overrideDigest bool
	}{
		{name: "unapproved"},
		{name: "approved", approved: true},
		{name: "borrowed_review_digest", approved: true, overrideDigest: true},
		{name: "wrong_declared_digest", approved: true, reviewOriginal: true, overrideDigest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, key, home := agentJobsCompactionFixture(t)
			hash := "sha256:" + strings.Repeat("a", 64)
			if tc.reviewOriginal {
				hash = cancelledPublicationContent(t, newCancelledNotePublication(t, s, grant, home, "reviewed-receipt")).ContentDigest
			}
			reviewCancelledReceipt(t, s, hash)
			initializeMutationPinWorkflow(t, s, "work-cancelled", store.BuiltinWorkflowDefinitions()[0])
			publication := newCancelledNotePublication(t, s, grant, home, "unreviewed-publication")
			if tc.overrideDigest {
				var input map[string]any
				if err := json.Unmarshal(publication.input, &input); err != nil {
					t.Fatal(err)
				}
				input["content_digest"] = "sha256:" + strings.Repeat("a", 64)
				publication.input, _ = json.Marshal(input)
				publication.digest = mutationDigest("concord_work_compact", "publish", publication.env, publication.input)
			}
			request, env := publication.request(), publication.env
			var approvalRef string
			if tc.approved {
				inv := Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: "work_compact", RequiredOperation: "publish", ProductID: env.SelectedProductID}
				if err := s.Transact(t.Context(), func(tx *store.Transaction) error {
					var err error
					approvalRef, err = service.CreateApprovalChallengeTx(t.Context(), tx, probedHost(), inv, ApprovalChallengeSpec{OperationDigest: publication.digest, Scope: publication.scope, Versions: publication.versions, Consequence: "publication", HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: fixedTime().Add(time.Hour)})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				request, env = publication.approvedBy(t, key, approvalRef)
			}
			head := gitRun(t, home.RepoPath, "rev-parse", "HEAD")
			response := dispatchMutation(t, s, service, request, env)
			if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "outcome_mismatch" {
				t.Fatalf("unreviewed publication must refuse before approval/Git: outcome=%s error=%+v", response.Outcome, response.Error)
			}
			if gitRun(t, home.RepoPath, "rev-parse", "HEAD") != head || gitRun(t, home.RepoPath, "status", "--porcelain") != "" {
				t.Fatal("refused publication changed Git")
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM durable_operations WHERE work_id='work-cancelled'`) != 0 || archivedCancelledWorkCount(t, s) != 0 {
				t.Fatal("refused publication claimed or linked work")
			}
			if tc.approved {
				var status string
				var uses int
				if err := s.DatabaseForTesting().QueryRow(`SELECT status,used_count FROM agent_approval_challenges WHERE challenge_ref=?`, approvalRef).Scan(&status, &uses); err != nil || status != "active" || uses != 0 {
					t.Fatalf("refused publication consumed approval: status=%s uses=%d err=%v", status, uses, err)
				}
			} else if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges WHERE consequence='publication'`) != 0 {
				t.Fatal("mismatching publication requested approval")
			}
		})
	}
}

func TestTerminalCleanupReconcileRejectsUnreviewedCommittedBytes(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	receipt := cancelledPublicationContent(t, newCancelledNotePublication(t, s, grant, home, "reviewed-receipt"))
	reviewCancelledReceipt(t, s, receipt.ContentDigest)
	initializeMutationPinWorkflow(t, s, "work-cancelled", store.BuiltinWorkflowDefinitions()[0])
	wrongContent := receipt.Content + "\nUnreviewed addition.\n"
	wrongHash := publicationContentHash(wrongContent)
	var wrong store.CommittedNote
	service.publicationObserver = func(phase string) error {
		if phase == "git_publish" {
			var err error
			wrong, err = store.PublishCanonicalNote(t.Context(), home, "work-cancelled", wrongContent, wrongHash)
			return err
		}
		if phase == "verify_commit" {
			return context.Canceled
		}
		return nil
	}
	response := publishCancelledNote(t, s, service, grant, key, home)
	if response.Outcome != OutcomePartial || archivedCancelledWorkCount(t, s) != 0 || wrong.CommitOID == "" {
		t.Fatalf("expected unlinked committed publication: %+v", response)
	}
	service.publicationObserver = nil
	var operationID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT op_id FROM durable_operations WHERE work_id='work-cancelled'`).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, home.RepoPath, "rev-parse", "HEAD")
	input, _ := json.Marshal(map[string]any{"operation_id": operationID, "expected_operation_version": 1, "expected_proof_digest": wrongHash, "idempotency_key": "wrong-proof-reconcile"})
	response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}, agentJobsMutationEnvelope(t, s, grant, "proj-api"))
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "outcome_mismatch" {
		t.Fatalf("reconciliation must refuse different committed bytes: outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if archivedCancelledWorkCount(t, s) != 0 || gitRun(t, home.RepoPath, "rev-parse", "HEAD") != head {
		t.Fatal("refused reconciliation linked or changed Git")
	}
	step, err := store.Step(t.Context(), s, operationID)
	if err != nil || step.ResultKind == store.ResultCompleted {
		t.Fatalf("refused reconciliation settled the pending operation: step=%+v err=%v", step, err)
	}
	_, version := readWorkFromStore(t, s, "work-cancelled")
	err = store.PublishCompactionLink(t.Context(), s, store.CompactionLinkRequest{EventID: "wrong-proof-direct-link", WorkID: "work-cancelled", ExpectedVersion: version, Actor: "operator", OccurredAt: fixedTime(), Home: home, CommitOID: wrong.CommitOID, NotePath: wrong.NotePath, ExpectedHash: wrongHash, Reason: "reconcile wrong bytes", Boundary: store.CompactionBoundaryRecoveryExempt})
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindOutcomeMismatch || archivedCancelledWorkCount(t, s) != 0 {
		t.Fatalf("atomic link owner admitted unreviewed bytes: %v", err)
	}
}

func TestTerminalCleanupLinkRejectsUnreviewedCommittedBytes(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(fmt.Sprintf("indexed_%t", indexed), func(t *testing.T) {
			s, _, grant, _, home := agentJobsCompactionFixture(t)
			receipt := cancelledPublicationContent(t, newCancelledNotePublication(t, s, grant, home, "wrong-link-receipt"))
			note, err := store.PublishCanonicalNote(t.Context(), home, "work-cancelled", receipt.Content, receipt.ContentDigest)
			if err != nil {
				t.Fatal(err)
			}
			req := store.CompactionLinkRequest{EventID: "wrong-link-receipt", WorkID: "work-cancelled", ExpectedVersion: receipt.ExpectedVersion, Actor: "operator", OccurredAt: fixedTime(), Home: home, CommitOID: note.CommitOID, NotePath: note.NotePath, Reason: "verified committed note", Boundary: store.CompactionBoundaryRecoveryExempt}
			if indexed {
				if err := store.PublishCompactionLink(t.Context(), s, req); err != nil {
					t.Fatal(err)
				}
			}
			reviewCancelledReceipt(t, s, "sha256:"+strings.Repeat("a", 64))
			_, req.ExpectedVersion = readWorkFromStore(t, s, "work-cancelled")
			callbackCalled := false
			req.WithinTx = func(*store.Transaction) error { callbackCalled = true; return nil }
			before := archivedCancelledWorkCount(t, s)
			err = store.PublishCompactionLink(t.Context(), s, req)
			var failure *store.Failure
			if !errors.As(err, &failure) || failure.Kind != store.KindOutcomeMismatch || callbackCalled || archivedCancelledWorkCount(t, s) != before {
				t.Fatalf("link owner must reject actual committed hash even without expected_hash: err=%v callback=%t", err, callbackCalled)
			}
		})
	}
}

func TestTerminalCleanupUnboundPublicationRemainsRetained(t *testing.T) {
	s, service, grant, key, home := agentJobsCompactionFixture(t)
	response := publishCancelledNote(t, s, service, grant, key, home)
	if response.Outcome != OutcomeOK || archivedCancelledWorkCount(t, s) != 1 {
		t.Fatalf("unbound publication must still succeed: %+v", response)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-cancelled'`) != 1 {
		t.Fatal("unbound terminal execution was collected")
	}
}
