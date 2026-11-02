package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type lawPinUpgradeCase struct {
	family, step, lifecycle string
	stale                   bool
}

// This fixture authors reports at the actual schema-174 boundary. The old
// dependency encoding is test input, never a current writer or decoder.
func lawPinUpgradeFixture(t *testing.T) (*sql.DB, string, []lawPinUpgradeCase, map[string]string) {
	t.Helper()
	return lawPinUpgradeFixtureAt(t, 174)
}

func lawPinUpgradeFixtureAt(t *testing.T, schema int) (*sql.DB, string, []lawPinUpgradeCase, map[string]string) {
	t.Helper()
	ctx := t.Context()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, schema)
	cases := []lawPinUpgradeCase{
		{family: "workflow.implementation", step: "acceptance"},
		{family: "workflow.implementation", step: "release"},
		{family: "workflow.break_fix", step: "verify"},
		{family: "workflow.break_fix", step: "complete"},
		{family: "workflow.research", step: "conclude"},
		{family: "workflow.research", step: "complete"},
		{family: "workflow.implementation", step: "release", lifecycle: "completed"},
		{family: "workflow.break_fix", step: "complete", lifecycle: "cancelled"},
		{family: "workflow.implementation", step: "release", lifecycle: "superseded"},
		{family: "workflow.implementation", step: "release", stale: true},
	}
	workIDs := map[string]string{}
	actorRef := "actor:" + strings.Repeat("a", 64)
	lawHash := "sha256:" + strings.Repeat("c", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for i, tc := range cases {
		registered := mustBuiltinDefinition(t, tc.family)
		for _, pinned := range []bool{false, true} {
			id := fmt.Sprintf("upgrade-review-%d-%t", i, pinned)
			if tc.lifecycle == "" && !tc.stale {
				workIDs[tc.family+"/"+tc.step+fmt.Sprint(pinned)] = id
			}
			lifecycle := tc.lifecycle
			if lifecycle == "" {
				lifecycle = "in_progress"
			}
			retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task',?,?,0,1,?,?)`, id, id, lifecycle, retirementUpgradeTime, retirementUpgradeTime)
			retirementUpgradeExec(t, db, `INSERT INTO work_projects VALUES(?,'project-upgrade','primary')`, id)
			mandate := "[]"
			if pinned {
				mandate = `["spec:one"]`
			}
			retirementUpgradeExec(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_digest) VALUES(?,1,'Re-review the unchanged candidate','internal_sqlite','[]','[]',?, ?,?,'[]',1,'prototype_internal',?,?)`, id, retirementUpgradeTime, actorRef, mandate, registered.Definition.Ref, registered.Digest)
			kind, payload := "check", `{"kind":"check","check_ref":"check:unchanged","immutable_subject_ref":"commit:unchanged","expected_result":"pass"}`
			if tc.family == "workflow.research" {
				kind, payload = "outcome", `{"kind":"outcome","allowed":["resolved"]}`
			}
			retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,?,?)`, id, kind, payload)
			retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at,execution_started_at) VALUES(?,?,?,?,'running',?,?,?)`, id, registered.Definition.Ref, registered.Digest, tc.step, actorRef, retirementUpgradeTime, retirementUpgradeTime)
			if workflowDefinitionProducesDelivery(registered.Definition) {
				binding := WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
				retirementUpgradeExec(t, db, `INSERT INTO workflow_architecture_bindings VALUES(?,1,'product-upgrade',?,'root',?)`, id, lawHash, architectureBindingProjectionHash(binding))
				retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_affected_domains VALUES(?,1,'root')`, id)
				retirementUpgradeExec(t, db, `INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES(?,1,'Retain the unchanged candidate.','[{"id":"decision:owner","question":"Which subject needs review?","choice":"The unchanged candidate.","rationale":"Only the dependency encoding changes.","rejected":[]}]','["internal/store/workflow_review.go"]',?)`, id, retirementUpgradeTime)
			}
			deps, err := workflowReviewDependencies(ctx, db, id)
			if err != nil {
				t.Fatal(err)
			}
			if pinned {
				retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_law_revisions VALUES(?,1,'spec:one',?)`, id, lawHash)
				design, err := readCurrentWorkflowDesign(ctx, db, id)
				if err != nil {
					t.Fatal(err)
				}
				if design == nil {
					design = &WorkflowDesignRecord{}
				}
				deps.ArchitectureNotApplicable = false
				deps.ArchitectureDigest = reviewDigest(struct {
					Approach    string                   `json:"approach"`
					Decisions   []WorkflowDesignDecision `json:"decisions"`
					TouchedRefs []string                 `json:"touched_refs"`
					LawPins     []struct {
						LawID       string `json:"law_id"`
						ContentHash string `json:"content_hash"`
					} `json:"law_pins,omitempty"`
				}{Approach: design.Approach, Decisions: design.Decisions, TouchedRefs: design.TouchedRefs, LawPins: []struct {
					LawID       string `json:"law_id"`
					ContentHash string `json:"content_hash"`
				}{{"spec:one", lawHash}}})
			}
			if tc.stale {
				deps.Criteria[0].ContentDigest = "sha256:" + strings.Repeat("e", 64)
			}
			report := reviewBlockFixture()
			report.InputCandidate.ProjectID, report.OutputCandidate.ProjectID = "project-upgrade", "project-upgrade"
			report.Disposition, report.Dependencies, report.Findings = "ready_for_final_proof", deps, []ReviewFinding{}
			if err := validateReviewBlock(report); err != nil {
				t.Fatal(err)
			}
			seq := 30 + i*2
			if pinned {
				seq++
			}
			ref := "accepted-" + id
			originalPayload := workflowJSON(map[string]any{"action_id": "record_review", "actor_ref": actorRef, "review": report})
			retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,'workflow.action_completed','work_item',?,?,1,?)`, ref, seq, id, actorRef, historicalFixturePayloadDigest(t, originalPayload))
			state := WorkflowReviewState{Candidate: &report.OutputCandidate, Accepted: &WorkflowAcceptedReview{ReportRef: ref, ReportDigest: reviewDigest(report), ActorRef: actorRef, Report: *report}, Findings: []ReviewFinding{}}
			if err := writeWorkflowReviewFacts(ctx, db, id, state); err != nil {
				t.Fatal(err)
			}
			if pinned {
				retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings VALUES(?,?,?,?,'{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}')`, id, seq, ref, actorRef)
			}
		}
	}
	retirementUpgradeExec(t, db, `UPDATE store_sequences SET last=? WHERE name='effect'`, 30+len(cases)*2-1)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db, path, cases[:6], workIDs
}

func cancelLawPinUpgradeBlockers(t *testing.T, path string) {
	t.Helper()
	s, err := openUnmigrated(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for _, id := range []string{"upgrade-review-1-true", "upgrade-review-3-true"} {
		event := workTransitionEvent("cancel-"+id, id, "in_progress", "cancelled", 1, 2)
		if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 1}}); err != nil {
			t.Fatalf("cancel blocker through lifecycle operation: %v", err)
		}
	}
}

func TestLawPinUpgradeSafety(t *testing.T) {
	testLawPinUpgradeSafetyAt(t, 174)
}

func TestLawPinUpgradeSafetyFromInstalledPredecessor(t *testing.T) {
	testLawPinUpgradeSafetyAt(t, 175)
}

func testLawPinUpgradeSafetyAt(t *testing.T, schema int) {
	t.Helper()
	useStampedBuild(t)
	ctx := t.Context()
	db, path, cases, workIDs := lawPinUpgradeFixtureAt(t, schema)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(ctx, path, nil)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUpgradeBlocked {
		t.Fatalf("unsafe pin retirement = %v, want upgrade_blocked", err)
	}
	if want := "law-pin retirement would invalidate accepted Review without an admitted re-review route for work: upgrade-review-1-true, upgrade-review-3-true; no migration was applied"; failure.Detail != want {
		t.Fatalf("blocker census = %q, want %q", failure.Detail, want)
	}
	for _, id := range []string{"upgrade-review-1-true", "upgrade-review-3-true"} {
		if !strings.Contains(failure.Detail, id) {
			t.Errorf("missing blocker %s: %s", id, failure.Detail)
		}
	}
	for _, id := range []string{"upgrade-review-0-true", "upgrade-review-1-false", "upgrade-review-2-true", "upgrade-review-3-false", "upgrade-review-4-true", "upgrade-review-5-true", "upgrade-review-6-true", "upgrade-review-7-true", "upgrade-review-8-true", "upgrade-review-9-true"} {
		if strings.Contains(failure.Detail, id) {
			t.Errorf("charged unaffected control %s: %s", id, failure.Detail)
		}
	}
	if !failure.RetrySafe || !strings.Contains(failure.RecoveryAction, "complete or cancel") || !strings.Contains(failure.RecoveryAction, "installed release") {
		t.Fatalf("unsafe recovery: %+v", failure)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refusal changed schema, state, proofs, receipts, guards or sequences")
	}
	refusedSnapshot := filepath.Join(t.TempDir(), "unsafe.db")
	if _, err := BackupDatabase(ctx, path, refusedSnapshot); !hasFailureKind(err, KindUpgradeBlocked) {
		t.Fatalf("unsafe snapshot restore verification = %v", err)
	}
	for _, file := range []string{refusedSnapshot, manifestPath(refusedSnapshot)} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("refused snapshot left %s: %v", file, err)
		}
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused backup changed predecessor")
	}
	cancelLawPinUpgradeBlockers(t, path)
	beforeReports := retirementUpgradeRows(t, db, `SELECT * FROM workflow_review_state ORDER BY work_id`)
	beforeReceipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	snapshot := filepath.Join(t.TempDir(), "before-pin-retirement.db")
	manifest, err := BackupDatabase(ctx, path, snapshot)
	if err != nil {
		t.Fatalf("verified predecessor snapshot: %v", err)
	}
	if _, err := Upgrade(ctx, path, nil); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_review_state ORDER BY work_id`); !reflect.DeepEqual(got, beforeReports) {
		t.Fatal("upgrade changed attributed Review facts")
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, beforeReceipts) {
		t.Fatal("upgrade changed effect receipt bindings")
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	restore, err := RestoreBackup(ctx, snapshot, restored)
	if err != nil {
		t.Fatal(err)
	}
	if restore.SourceSnapshot.SchemaVersion != schema {
		t.Fatalf("source snapshot schema=%d, want %d", restore.SourceSnapshot.SchemaVersion, schema)
	}
	restoredDB, err := sql.Open(driverName, dataSourceName(restored))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	if got := retirementUpgradeRows(t, restoredDB, `SELECT * FROM workflow_review_state ORDER BY work_id`); !reflect.DeepEqual(got, beforeReports) {
		t.Fatal("restore changed attributed Review facts")
	}
	if got := retirementUpgradeRows(t, restoredDB, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, beforeReceipts) {
		t.Fatal("restore changed receipts")
	}
	if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name='workflow_contract_law_revisions'`); len(got) != 0 {
		t.Fatal("pin table remains")
	}
	if got := retirementUpgradeRows(t, restoredDB, `SELECT name FROM pragma_table_info('workflow_architecture_bindings') WHERE name='domain_registry_content_hash'`); len(got) != 0 {
		t.Fatal("retired registry column restored")
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("source snapshot changed: %+v %v", verified, err)
	}
	for _, id := range []string{"upgrade-review-9-false", "upgrade-review-9-true"} {
		stale, err := loadWorkflowReviewState(ctx, db, id)
		if err != nil || stale.DependenciesCurrent {
			t.Fatalf("already stale Review became current: %+v %v", stale, err)
		}
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range cases {
		t.Run(tc.family+"/"+tc.step, func(t *testing.T) {
			registered := mustBuiltinDefinition(t, tc.family)
			for _, pinned := range []bool{false, true} {
				id := workIDs[tc.family+"/"+tc.step+fmt.Sprint(pinned)]
				review, err := loadWorkflowReviewState(ctx, db, id)
				if err != nil {
					t.Fatal(err)
				}
				if review.DependenciesCurrent == pinned {
					t.Fatalf("pinned=%t current=%t, want only the pin encoding to change", pinned, review.DependenciesCurrent)
				}
				if !pinned {
					continue
				}
				if tc.step == "release" || (tc.family == "workflow.break_fix" && tc.step == "complete") {
					continue
				}
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				state, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, id, registered.Definition, tc.step, "workflow_action", BuiltinWorkflowRegistry())
				_ = tx.Rollback()
				if err != nil {
					t.Fatal(err)
				}
				for _, action := range []string{"record_review", "request_correction", "supersede_contract", "complete"} {
					decision := workflowAdmit(registered.Definition, state, action)
					t.Logf("%s: admitted=%t off_step=%t recovery=%t failure=%v", action, decision.Admitted, decision.OffStep, decision.RecoveryRoute, decision.Failure)
				}
				if !workflowAdmit(registered.Definition, state, "record_review").Admitted {
					if tc.family != "workflow.research" || tc.step != "complete" || !workflowAdmit(registered.Definition, state, "complete").Admitted {
						t.Error("unexpected missing re-review route")
					}
					continue
				}
				fresh := review.Accepted.Report
				fresh.Dependencies = *review.Dependencies
				var receiptBefore string
				if err := db.QueryRowContext(ctx, `SELECT payload_digest FROM effect_receipts WHERE event_id=?`, review.Accepted.ReportRef).Scan(&receiptBefore); err != nil {
					t.Fatal(err)
				}
				actor := WorkflowActor{PrincipalRef: "operator-upgrade", ClientRef: "client-upgrade", AgentRef: "agent-rereview", SessionRef: "session-rereview", ActorClass: ActorAgent}
				err = s.Transact(ctx, func(tx *Transaction) error {
					_, applyErr := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
						WorkID: id, ExpectedVersion: 1, ActionID: "record_review", Payload: mustJSONValue(map[string]any{"review": fresh}), Actor: actor,
						AcceptedInputsDigest: testManifestDigest, IdempotencyIdentity: "fresh-" + id, OperationID: "fresh-" + id, PrincipalRef: actor.PrincipalRef,
						Tool: "concord_work_transition", IdempotencyKey: "fresh-" + id, RequestID: "fresh-" + id, ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
					})
					return applyErr
				})
				if err != nil {
					t.Fatalf("record independent re-review of unchanged candidate: %v", err)
				}
				after, err := loadWorkflowReviewState(context.Background(), db, id)
				if err != nil || !after.DependenciesCurrent || !reflect.DeepEqual(after.Candidate, review.Candidate) {
					t.Fatalf("unchanged candidate did not recover: %+v err=%v", after, err)
				}
				var originalDigest string
				if err := db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, review.Accepted.ReportRef).Scan(&originalDigest); err != nil {
					t.Fatal(err)
				}
				if originalDigest != receiptBefore {
					t.Fatal("re-review changed predecessor receipt binding")
				}
				t.Logf("unchanged candidate re-reviewed; predecessor receipt retained: %s", originalDigest)
			}
		})
	}
}

func TestLawPinUpgradeMalformedReviewRefuses(t *testing.T) {
	useStampedBuild(t)
	db, path, _, _ := lawPinUpgradeFixture(t)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE workflow_review_state SET state=json_set(state,'$.accepted','malformed') WHERE work_id='upgrade-review-1-true'; DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if !hasFailureKind(err, KindInvariantViolation) {
		t.Fatalf("malformed Review upgrade=%v, want invariant_violation", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("malformed-state refusal changed predecessor")
	}
}

func TestLawPinUpgradeSafetyFromOlderPrefix(t *testing.T) {
	useStampedBuild(t)
	db, path, _, _ := lawPinUpgradeFixtureAt(t, 166)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUpgradeBlocked || !strings.Contains(failure.Detail, "upgrade-review-1-true, upgrade-review-3-true") {
		t.Fatalf("unsafe older-prefix upgrade=%v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("cutover refusal committed preceding migrations")
	}
}
