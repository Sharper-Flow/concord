package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The instance and contract are retained DATA in the real schema prefix, not
// current initialization with its baseline removed. No Git or publication
// fixture is needed: this Product has no canonical knowledge home.
func lawUpgradePrefix(t *testing.T, id, step string, contract, boundary int) (*Store, WorkflowActor) {
	t.Helper()
	return lawUpgradePrefixForDefinition(t, id, step, contract, boundary, "workflow.research")
}

func lawUpgradePrefixForDefinition(t *testing.T, id, step string, contract, boundary int, definitionRef string) (*Store, WorkflowActor) {
	t.Helper()
	prefix := migrationPrefixBefore(t, "workflow_owning_start_law_baseline")
	path := filepath.Join(t.TempDir(), "before-owning-start.db")
	s := &Store{db: openMigratedTo(t, path, prefix), path: path, Clock: func() time.Time { return time.Unix(100, 0).UTC() }}
	var tables int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='workflow_law_baselines'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("fixture is not pre-baseline: tables=%d error=%v", tables, err)
	}
	seedWork(t, s, id)
	owner := WorkflowActor{PrincipalRef: "principal/upgrade", ClientRef: "client/upgrade", AgentRef: "agent/main", SessionRef: "session/upgrade", ActorClass: ActorAgent}
	ref := ownerRefForTest(t, owner)
	entry := mustBuiltinDefinition(t, definitionRef)
	if !workflowDeclaresDecisionDomains(entry.Definition) {
		t.Fatal("upgrade witness must exercise the declared decision fact")
	}
	execSixClassSQL(t, s.db, `INSERT INTO write_guard(active) VALUES(1)`)
	execSixClassSQL(t, s.db, `UPDATE work_items SET lifecycle='in_progress' WHERE id=?`, id)
	execSixClassSQL(t, s.db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','2026-10-01T00:00:00Z')`, ref, owner.PrincipalRef, owner.ClientRef, owner.AgentRef, owner.SessionRef)
	execSixClassSQL(t, s.db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES(?,?,?,?,'running',?,'2026-10-01T00:00:00Z')`, id, entry.Definition.Ref, entry.Digest, step, ref)
	if contract != 0 {
		execSixClassSQL(t, s.db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,decision_domains,definition_ref,definition_digest) VALUES(?,1,'Record the bounded research result','internal_sqlite','["artifact"]','[]','2026-10-01T00:00:00Z',?,'[]','[]',?,'[]',?,?)`, id, ref, boundary, entry.Definition.Ref, entry.Digest)
		kind, outcome := "outcome", `{"kind":"outcome","allowed":["report_recorded"]}`
		if definitionRef != "workflow.research" {
			kind, outcome = "check", `{"kind":"check","check_ref":"check:upgrade","immutable_subject_ref":"commit:upgrade","expected_result":"pass"}`
		}
		execSixClassSQL(t, s.db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,?,?)`, id, kind, outcome)
	}
	execSixClassSQL(t, s.db, `DELETE FROM write_guard`)
	if definitionRef != "workflow.research" {
		lawUpgradeRegistry(t, s)
	}
	if step == "complete" {
		lawUpgradeCompletionHistory(t, s, id, owner)
	}
	before := sixClassSnapshot(t, s.db, "work_items", "workflow_instances", "workflow_contracts", "workflow_contract_predicates", "workflow_actors", "effect_receipts")
	advanceHistoricalPrefix(t, s.db, prefix+1)
	if got := sixClassSnapshot(t, s.db, "work_items", "workflow_instances", "workflow_contracts", "workflow_contract_predicates", "workflow_actors", "effect_receipts"); !reflect.DeepEqual(got, before) {
		t.Fatal("migration rewrote retained workflow facts")
	}
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	lawUpgradeNoBaseline(t, s, id)
	return s, owner
}

// Build the ordinary report, research association and coordinator confirmation
// before migration. The remote locator identifies retained registry data; it is
// not a canonical knowledge home and supplies no source-verification proof.
func lawUpgradeCompletionHistory(t *testing.T, s *Store, id string, owner WorkflowActor) {
	t.Helper()
	lawUpgradeRegistry(t, s)
	execSixClassSQL(t, s.db, `INSERT INTO write_guard(active) VALUES(1)`)
	execSixClassSQL(t, s.db, `UPDATE workflow_instances SET current_step='findings' WHERE work_id=?`, id)
	execSixClassSQL(t, s.db, `DELETE FROM write_guard`)
	f := workContextFixture{store: s, workID: id, owner: owner}
	pack := seedResearchContextPack(t, f, "upgrade-proof")
	bindResearchPack(t, f, pack.PackID, 1, true)
	selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
	selection["product_wide_rationale"] = "The research result supports the whole bounded Product question."
	if err := lawUpgradeAction(t, s, id, owner, "record_work_context", map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
		t.Fatalf("record ordinary research selection before upgrade: %v", err)
	}
	for _, action := range []string{"record_report", "record_conclusion"} {
		fields := map[string]any{}
		if action == "record_report" {
			fields = map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "evidence:upgrade-report"}
		}
		if err := lawUpgradeAction(t, s, id, owner, action, fields); err != nil {
			t.Fatalf("ordinary %s before upgrade: %v", action, err)
		}
	}
	view, err := readWorkContextView(t.Context(), s.db, id)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 || view.ResearchFindings[0].State != WorkContextResearchStateResolved {
		t.Fatalf("historical research did not resolve: view=%+v error=%v", view, err)
	}
	if err := lawUpgradeAction(t, s, id, owner, "record_verdict", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{view.ResearchFindings[0].ViewID}}}}); err != nil {
		t.Fatalf("ordinary research proof association before upgrade: %v", err)
	}
	insertInvestigationGateObservation(t, s, id, "obs:"+strings.Repeat("d", 16), []string{"root", "upgrade-proof-owner"})
	if err := lawUpgradeAction(t, s, id, owner, "confirm_premise", map[string]any{}); err != nil {
		t.Fatalf("ordinary coordinator confirmation before upgrade: %v", err)
	}
}

func lawUpgradeRegistry(t *testing.T, s *Store) {
	t.Helper()
	execSixClassSQL(t, s.db, `INSERT INTO write_guard(active) VALUES(1)`)
	execSixClassSQL(t, s.db, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('workflow-law-locator','project','git_remote','https://example.invalid/research.git','https://example.invalid/research.git','t','t')`)
	execSixClassSQL(t, s.db, `DELETE FROM write_guard`)
}

func lawUpgradeNoBaseline(t *testing.T, s *Store, id string) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines WHERE work_id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("historical item acquired invented start proof: count=%d error=%v", count, err)
	}
}

// Observe every application table, including claims, actors, receipts, phase
// exits and write guards. Schema bookkeeping alone is outside the comparison.
func lawUpgradeSnapshot(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name<>'schema_migrations' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	return sixClassSnapshot(t, s.db, tables...)
}

func lawUpgradeAction(t *testing.T, s *Store, id string, owner WorkflowActor, action string, fields map[string]any) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), s, id, action)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, id)
	op := fmt.Sprintf("upgrade:%s:%s:%d", id, action, version)
	payload := mustJSONValue(fields)
	return s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: id, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
			EvidenceRefs:         actionEvidenceRefs(action, payload),
			AcceptedInputsDigest: testManifestDigest, IdempotencyIdentity: op, OperationID: op,
			PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: op, RequestID: "request:" + op,
			ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
		})
		return err
	})
}

func lawUpgradeRefusal(t *testing.T, s *Store, id string, clause int, run func() error) {
	t.Helper()
	before := lawUpgradeSnapshot(t, s)
	err := run()
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || failure.Clause != clause || !strings.Contains(failure.Detail, "no law before-state anchored at its owning start") {
		t.Fatalf("route did not reach the missing owning-start clause: %v", err)
	}
	if !strings.Contains(failure.RecoveryAction, "cancel and re-capture") || !strings.Contains(failure.RecoveryAction, "boundary-authenticated outside repair") {
		t.Fatalf("missing-baseline refusal lost the distinct recovery routes: %+v", failure)
	}
	if after := lawUpgradeSnapshot(t, s); !reflect.DeepEqual(after, before) {
		t.Error("refusal changed the application-table snapshot")
		for table := range after {
			if !reflect.DeepEqual(before[table], after[table]) {
				t.Errorf("refusal wrote %s: before=%v after=%v", table, before[table], after[table])
			}
		}
	}
	lawUpgradeNoBaseline(t, s, id)
}

func lawUpgradeContractFields(successor bool) map[string]any {
	fields := map[string]any{
		"premise": "Record the bounded research result", "decision_domains": []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
		"required_evidence":  []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
	}
	if successor {
		fields["contract_version"] = 2
		fields["supersede_reason"] = "Retain the remaining research scope"
		fields["audit_evidence"] = []string{"evidence:upgrade"}
	}
	return fields
}

func TestWorkflowLawUpgradeContractRoutes(t *testing.T) {
	for _, definitionRef := range []string{"workflow.research", "workflow.implementation", "workflow.break_fix"} {
		for _, action := range []string{"approve_contract", "supersede_contract"} {
			t.Run(definitionRef+"/"+action, func(t *testing.T) {
				id := "upgrade-" + definitionRef + "-" + action
				contract := 0
				step := "frame"
				if definitionRef != "workflow.research" {
					step = "planning"
				}
				if action == "supersede_contract" {
					contract = 1
					step = "conclude"
					if definitionRef == "workflow.implementation" {
						step = "acceptance"
					}
					if definitionRef == "workflow.break_fix" {
						step = "verify"
					}
				}
				s, owner := lawUpgradePrefixForDefinition(t, id, step, contract, 1, definitionRef)
				fields := lawUpgradeContractFields(contract != 0)
				if definitionRef != "workflow.research" {
					fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:upgrade", "immutable_subject_ref": "commit:upgrade", "expected_result": "pass"}}}
					fields["architecture_binding"] = map[string]any{"home_domain_id": "root", "affected_domain_ids": []string{"root"}, "domain_modifies": []string{}, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}
				}
				lawUpgradeRefusal(t, s, id, 0, func() error {
					return lawUpgradeAction(t, s, id, owner, action, fields)
				})
				pin, err := ReadWorkPin(t.Context(), s, id)
				if err != nil || pin.Lifecycle != "in_progress" || pin.Step != step {
					t.Fatalf("ordinary read lost historical state: pin=%+v error=%v", pin, err)
				}
				if err := lawUpgradeAction(t, s, id, owner, "checkpoint_context", map[string]any{
					"active_unit": "unit:remaining-research", "hypothesis": "Historical work remains readable", "diagnosis": "Owning-start proof is absent", "strategy": "Retain context without claiming completion",
					"touched_refs": []string{"ref:research"}, "evidence_refs": []string{"evidence:upgrade"}, "pending_questions": []string{}, "pending_decisions": []string{},
				}); err != nil {
					t.Fatalf("ordinary context write refused: %v", err)
				}
				checkpoint, err := readLatestContextCheckpointTx(t.Context(), s.db, id, "workflow_action")
				if err != nil || checkpoint == nil || checkpoint.Diagnosis != "Owning-start proof is absent" {
					t.Fatalf("context did not retain the missing-proof diagnosis: %+v error=%v", checkpoint, err)
				}
				pin, err = ReadWorkPin(t.Context(), s, id)
				if err != nil || pin.Lifecycle != "in_progress" || pin.Step != step {
					t.Fatalf("context write changed the historical route: %+v error=%v", pin, err)
				}
				lawUpgradeNoBaseline(t, s, id)
			})
		}
	}
}

func TestWorkflowLawUpgradeCompletionRoutes(t *testing.T) {
	for _, route := range []string{"ordered_completion", "complete_action"} {
		t.Run(route, func(t *testing.T) {
			id := "upgrade-" + route
			s, owner := lawUpgradePrefix(t, id, "complete", 1, 1)
			// Real close-step binding supplies the exact settled action, producer,
			// hash and phase-exit provenance required by completion clause 1.
			if err := lawUpgradeAction(t, s, id, owner, "bind_evidence", map[string]any{"evidence_kind": "review", "immutable_subject_ref": closureTestHash}); err != nil {
				t.Fatalf("prepare ordinary closure evidence: %v", err)
			}
			lawUpgradeRefusal(t, s, id, 3, func() error {
				if route == "complete_action" {
					return lawUpgradeAction(t, s, id, owner, "complete", map[string]any{"impact_verdict": "non-breaking"})
				}
				version := readWorkVersion(t, s, id)
				event := workflowEventWithActor("upgrade:ordered-completion", WorkflowCompleted, id, ownerRefForTest(t, owner), map[string]any{"work_id": id, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed", "impact_verdict": "non-breaking"})
				event.PayloadVersion = CurrentEventPayloadVersion(WorkflowCompleted)
				return CompleteWorkflow(t.Context(), s, event)
			})
		})
	}
}

// The same ordinary gates really complete a retained boundary-0 contract.
// This is a separate historical item, not a downgraded boundary-1 contract.
func TestWorkflowLawUpgradeLegacyCompletion(t *testing.T) {
	const id = "upgrade-legacy-completion"
	s, owner := lawUpgradePrefix(t, id, "complete", 1, 0)
	if err := lawUpgradeAction(t, s, id, owner, "bind_evidence", map[string]any{"evidence_kind": "review", "immutable_subject_ref": closureTestHash}); err != nil {
		t.Fatal(err)
	}
	if err := lawUpgradeAction(t, s, id, owner, "complete", map[string]any{"impact_verdict": "non-breaking"}); err != nil {
		t.Fatalf("retained boundary-0 completion: %v", err)
	}
	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil || pin.Lifecycle != "completed" {
		t.Fatalf("legacy completion pin=%+v error=%v", pin, err)
	}
	lawUpgradeNoBaseline(t, s, id)
}

func TestWorkflowLawUpgradeCancellationRecapture(t *testing.T) {
	const old = "upgrade-cancelled"
	s, owner := lawUpgradePrefix(t, old, "frame", 1, 1)
	version := readWorkVersion(t, s, old)
	if err := applyWorkEvent(t, s, workTransitionEvent("upgrade:cancel", old, "in_progress", "cancelled", version, version+1), workVersion(old, version)); err != nil {
		t.Fatalf("supported lifecycle cancellation: %v", err)
	}
	lawUpgradeNoBaseline(t, s, old)
	const fresh = "upgrade-recaptured"
	seedWork(t, s, fresh)
	entry := mustBuiltinDefinition(t, "workflow.research")
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: fresh, Definition: entry, Actor: owner, Now: time.Unix(101, 0).UTC()})
	}); err != nil {
		t.Fatalf("fresh owning start: %v", err)
	}
	var baseline string
	if err := s.db.QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, fresh).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	var decoded workflowLawBaseline
	if err := json.Unmarshal([]byte(baseline), &decoded); err != nil || !decoded.NoHome {
		t.Fatalf("fresh start did not truthfully record absence of a knowledge home: %s error=%v", baseline, err)
	}
	if err := lawUpgradeAction(t, s, fresh, owner, "approve_contract", lawUpgradeContractFields(false)); err != nil {
		t.Fatalf("genuine new start did not admit ordinary approval: %v", err)
	}
	var completed, reconciled int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind='workflow.completed'`, old).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM outside_repair_reconciliations WHERE work_id=?`, old).Scan(&reconciled); err != nil || completed != 0 || reconciled != 0 {
		t.Fatalf("cancellation became ordinary completion or outside repair: completed=%d reconciled=%d error=%v", completed, reconciled, err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, old).Scan(&lifecycle); err != nil || lifecycle != "cancelled" {
		t.Fatalf("old lifecycle=%q error=%v", lifecycle, err)
	}
}
