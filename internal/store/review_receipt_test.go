package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCriterionReceiptBatchIsExplicitAndAtomic(t *testing.T) {
	entry := func(predicate, ref string) map[string]any {
		return map[string]any{"predicate_id": predicate, "receipt_refs": []string{ref}}
	}
	for _, tc := range []struct {
		name   string
		fields func(string) map[string]any
		kind   FailureKind
		count  int
		badRef string
	}{
		{"two criteria", func(ref string) map[string]any {
			return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:second", ref)}}
		}, "", 2, ""},
		{"one criterion", func(ref string) map[string]any {
			return map[string]any{"verdicts": []any{entry("predicate:first", ref)}}
		}, "", 1, ""},
		{"resumed Main", func(ref string) map[string]any {
			return map[string]any{"verdicts": []any{entry("predicate:first", ref)}}
		}, "", 1, ""},
		{"resumed Main unproved last criterion", func(ref string) map[string]any {
			return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:second", "research:invented")}}
		}, KindMissingEvidence, 0, "research:invented"},
		{"unapproved last criterion", func(ref string) map[string]any {
			return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:foreign", ref)}}
		}, KindInvalidPayload, 0, ""},
		{"unproved last criterion", func(ref string) map[string]any {
			return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:second", "research:invented")}}
		}, KindMissingEvidence, 0, "research:invented"},
		{"duplicate criterion", func(ref string) map[string]any {
			return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:first", ref)}}
		}, KindInvalidPayload, 0, ""},
		{"missing receipts", func(string) map[string]any {
			return map[string]any{"verdicts": []any{map[string]any{"predicate_id": "predicate:first"}}}
		}, KindInvalidPayload, 0, ""},
		{"empty receipts", func(string) map[string]any {
			return map[string]any{"verdicts": []any{map[string]any{"predicate_id": "predicate:first", "receipt_refs": []string{}}}}
		}, KindInvalidPayload, 0, ""},
		{"empty batch", func(string) map[string]any { return map[string]any{"verdicts": []any{}} }, KindInvalidPayload, 0, ""},
		{"scalar verdict", func(ref string) map[string]any {
			return map[string]any{"predicate_id": "predicate:first", "verdict_kind": "ok", "evaluation_evidence": []string{ref}}
		}, KindInvalidPayload, 0, ""},
		{"caller verdict", func(ref string) map[string]any {
			value := entry("predicate:first", ref)
			value["verdict_kind"] = "ok"
			return map[string]any{"verdicts": []any{value}}
		}, KindInvalidPayload, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var predicates []map[string]any
			for ordinal, id := range []string{"predicate:first", "predicate:second"} {
				predicates = append(predicates, map[string]any{"predicate_id": id, "ordinal": ordinal, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
			}
			const workID = "research-receipt-batch"
			route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "conclude", []string{}, []string{}, predicates...)
			defer route.store.Close()
			f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
			proof := qAssociateCurrentProofAt(t, f, "conclude", "", false)
			ref := proof.receipt
			payload := mustJSONValue(tc.fields(ref))
			var evidenceBefore int
			if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowEvidenceBound).Scan(&evidenceBefore); err != nil {
				t.Fatal(err)
			}
			before := contractCorrectionEffectSnapshot(t, f.store, workID)
			actor := f.owner
			resumed := strings.HasPrefix(tc.name, "resumed Main")
			if resumed {
				actor.SessionRef += ":resumed"
			}
			actorRef := mustActorRef(t, actor)
			actorsBefore := countWorkflowActor(t, f.store, actorRef)
			if resumed && actorsBefore != 0 {
				t.Fatal("resumed Main fixture already recorded its actor")
			}
			_, preflight := WorkflowActionDefinitionFor(t.Context(), f.store, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: "record_verdict", Payload: payload, Actor: actor})
			if after := contractCorrectionEffectSnapshot(t, f.store, workID); after != before {
				t.Fatal("criterion preflight changed durable state")
			}
			if countWorkflowActor(t, f.store, actorRef) != actorsBefore {
				t.Fatal("criterion preflight recorded its actor")
			}
			if tc.name == "resumed Main" && preflight == nil {
				rollback := errors.New("roll back the accepted criterion action")
				version := readWorkVersion(t, f.store, workID)
				err := f.store.Transact(t.Context(), func(tx *Transaction) error {
					_, err := ApplyWorkflowActionTx(t.Context(), tx, WorkflowActionExecutionRequest{
						WorkID: workID, ExpectedVersion: version, ActionID: "record_verdict", Payload: payload, Actor: actor,
						AcceptedInputsDigest: testManifestDigest, IdempotencyIdentity: "criterion-rollback", OperationID: "criterion-rollback",
						PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "criterion-rollback", RequestID: "criterion-rollback",
						ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
					})
					if err != nil {
						return err
					}
					var actors, verdicts int
					if err := tx.tx.QueryRow(`SELECT (SELECT count(*) FROM workflow_actors WHERE actor_ref=?),(SELECT count(*) FROM workflow_predicate_verdicts WHERE work_id=? AND actor=?)`, actorRef, workID, actorRef).Scan(&actors, &verdicts); err != nil {
						return err
					}
					if actors != 1 || verdicts != 1 {
						return errors.New("criterion action did not record its actor and verdict together")
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					t.Fatalf("accepted criterion action did not reach rollback: %v", err)
				}
				if after := contractCorrectionEffectSnapshot(t, f.store, workID); after != before || countWorkflowActor(t, f.store, actorRef) != 0 {
					t.Fatal("rolled-back criterion action retained effects or its actor")
				}
			}
			err := runVerdictActionAs(t, f.store, workID, "record_verdict", payload, 0, actor)
			if tc.kind != "" {
				if !hasFailureKind(preflight, tc.kind) || !hasFailureKind(err, tc.kind) {
					t.Fatalf("preflight=%v mutation=%v, want %s", preflight, err, tc.kind)
				}
				if tc.badRef != "" {
					for _, refusal := range []error{preflight, err} {
						if !strings.Contains(refusal.Error(), tc.badRef) || strings.Contains(refusal.Error(), ref) {
							t.Errorf("refusal must name only the disqualified reference %q, not %q: %v", tc.badRef, ref, refusal)
						}
						var failure *Failure
						if !failureAs(refusal, &failure) || !strings.Contains(failure.RecoveryAction, "passing report") {
							t.Errorf("refusal must explain how to bind qualifying proof: %v", refusal)
						}
					}
				}
				if after := contractCorrectionEffectSnapshot(t, f.store, workID); after != before {
					t.Fatal("refused criterion batch changed durable state")
				}
				if countWorkflowActor(t, f.store, actorRef) != actorsBefore {
					t.Fatal("refused criterion batch recorded its actor")
				}
			} else if preflight != nil || err != nil {
				t.Fatalf("current criterion proof refused: preflight=%v mutation=%v", preflight, err)
			}
			if tc.name == "resumed Main" {
				if got := countWorkflowActor(t, f.store, actorRef); got != 1 {
					t.Fatalf("accepted criterion action recorded %d actors, want 1", got)
				}
				var actorSeq, verdictSeq int64
				if err := f.store.db.QueryRow(`SELECT (SELECT effect_seq FROM effect_receipts WHERE subject_id=? AND actor=? AND kind=?),(SELECT min(verdict_seq) FROM workflow_predicate_verdicts WHERE work_id=? AND actor=?)`, workID, actorRef, WorkflowActorRecorded, workID, actorRef).Scan(&actorSeq, &verdictSeq); err != nil || actorSeq >= verdictSeq {
					t.Fatalf("actor registration must precede its verdict: actor=%d verdict=%d err=%v", actorSeq, verdictSeq, err)
				}
				for index := 0; index < tc.count; index++ {
					var raw []byte
					if err := f.store.db.QueryRow(`SELECT payload FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq LIMIT 1 OFFSET ?`, workID, index).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					var association workflowVerdictRecordedPayload
					if err := json.Unmarshal(raw, &association); err != nil {
						t.Fatal(err)
					}
					if association.Candidate == nil || association.CriterionDigest == "" || len(association.EvaluationEvidence) != 1 || association.EvaluationEvidence[0] != ref {
						t.Fatalf("association did not retain exact proof and current criterion content: %+v", association)
					}
					if association.VerdictActorRef != DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef) {
						t.Fatal("criterion association did not retain the calling Main identity")
					}
					if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, workID, association); err != nil {
						t.Fatalf("recorded criterion association cannot substantiate acceptance: %v", err)
					}
					unrecorded := actor
					unrecorded.SessionRef += ":unrecorded"
					association.VerdictActorRef = mustActorRef(t, unrecorded)
					if countWorkflowActor(t, f.store, association.VerdictActorRef) != 0 {
						t.Fatal("negative association fixture already recorded its actor")
					}
					if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, workID, association); !hasFailureKind(err, KindProjectionNotFound) {
						t.Fatalf("stored association admitted an unrecorded actor: %v", err)
					}
				}
			}
			plan, err := readWorkflowPlanTx(t.Context(), f.store.db, workID)
			if err != nil || plan == nil {
				t.Fatalf("read research plan: %v", err)
			}
			frontier := WorkflowStepPlanFrontier(*plan)
			if len(frontier) != 1 || frontier[0].ID != "conclude" {
				t.Fatalf("criterion association changed research frontier: %v", frontier)
			}
			var count int
			if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&count); err != nil || count != tc.count {
				t.Fatalf("recorded %d criterion associations, want %d: %v", count, tc.count, err)
			}
			var evidenceAfter int
			if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowEvidenceBound).Scan(&evidenceAfter); err != nil || evidenceAfter != evidenceBefore {
				t.Fatalf("criterion association minted or rebound evidence: before=%d after=%d err=%v", evidenceBefore, evidenceAfter, err)
			}
			for index := 0; index < tc.count; index++ {
				var raw []byte
				if err := f.store.db.QueryRow(`SELECT payload FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq LIMIT 1 OFFSET ?`, workID, index).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var association workflowVerdictRecordedPayload
				if err := json.Unmarshal(raw, &association); err != nil {
					t.Fatal(err)
				}
				if association.Candidate == nil || association.CriterionDigest == "" || len(association.EvaluationEvidence) != 1 || association.EvaluationEvidence[0] != ref {
					t.Fatalf("association did not retain exact proof and current criterion content: %+v", association)
				}
				if association.VerdictActorRef != DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef) {
					t.Fatal("criterion association did not retain the calling Main identity")
				}
				if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, workID, association); err != nil {
					t.Fatalf("recorded criterion association cannot substantiate acceptance: %v", err)
				}
				unrecorded := actor
				unrecorded.SessionRef += ":unrecorded"
				association.VerdictActorRef = mustActorRef(t, unrecorded)
				if countWorkflowActor(t, f.store, association.VerdictActorRef) != 0 {
					t.Fatal("negative association fixture already recorded its actor")
				}
				if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, workID, association); !hasFailureKind(err, KindProjectionNotFound) {
					t.Fatalf("stored association admitted an unrecorded actor: %v", err)
				}
			}
		})
	}
}
