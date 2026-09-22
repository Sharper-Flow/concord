package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Every seed statement inserts one row whose identifiers carry the rblSeed
// prefix, so the post-rebuild assertions can tell a seeded row from a row the
// replay restored. Seeds attach to the two work items the log restores
// (work-rbl, work-rbl2) and to the product and project the log restores.
//
// The seed set spans every table the objective names: each table that holds a
// RESTRICT foreign key to work_items holds a row before the recovery runs, and
// so do the projection tables the workflow contract rows RESTRICT-reference
// (contracts, architecture bindings, law-addition reservations, impact
// edges), which exercise the dependency order of the clear. Active research is
// seeded too, and must survive: it is direct-table authority the rebuild
// snapshots and restores, never clears.
const rblSeed = "rbl"

var (
	rblDigest = "sha256:" + strings.Repeat("a", 64)
	rblActor  = "actor:" + strings.Repeat("a", 64)
	rblStamp  = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
)

// rblSeedStatements maps each seeded table to one INSERT. Order matters: a
// row may only name parents seeded earlier in this list. The test fails when
// the clear list gains a RESTRICT-FK table this map does not seed.
func rblSeedStatements() []struct{ table, insert string } {
	return []struct{ table, insert string }{
		{"workflow_actors", `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('` + rblActor + `','principal-rbl','client-rbl','agent-rbl','session-rbl','agent','` + rblStamp + `')`},
		{"workflow_contracts", `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate) VALUES('work-rbl',1,'premise','internal_sqlite','[]','[]','` + rblStamp + `','` + rblActor + `','[]'),('work-rbl2',1,'premise','internal_sqlite','[]','[]','` + rblStamp + `','` + rblActor + `','[]')`},
		{"workflow_architecture_bindings", `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES('work-rbl',1,'prod','` + rblDigest + `','dom-rbl','` + rblDigest + `')`},
		{"workflow_law_addition_reservations", `INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('prod','law-res-rbl','work-rbl',1,'dom-rbl')`},
		{"workflow_contract_law_additions", `INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES('work-rbl',1,'prod','law-res-rbl','dom-rbl','work-rbl',1)`},
		{"workflow_contract_law_modifications", `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('work-rbl',1,'law-rbl')`},
		{"workflow_contract_affected_domains", `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES('work-rbl',1,'dom-rbl')`},
		{"workflow_contract_domain_modifications", `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('work-rbl',1,'dom-rbl')`},
		{"workflow_contract_domain_relation_modifications", `INSERT INTO workflow_contract_domain_relation_modifications(work_id,contract_version,source_domain_id,kind,target_domain_id) VALUES('work-rbl',1,'dom-a','depends_on','dom-b')`},
		{"workflow_contract_verification_obligations", `INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES('work-rbl',1,'law-rbl','obligation-rbl')`},
		{"workflow_contract_predicates", `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-rbl',1,'predicate:rbl',0,'check','{"check_ref":"check:rbl","expected_result":"pass"}')`},
		{"workflow_candidate_sets", `INSERT INTO workflow_candidate_sets(work_id,contract_version,candidate_kind,candidate_ref,candidate_role,candidate_scope,recorded_at,recorded_by) VALUES('work-rbl',1,'work_item','work-rbl2','include','scope','` + rblStamp + `','` + rblActor + `')`},
		{"workflow_premise_confirmations", `INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES('work-rbl',1,'` + rblActor + `','` + rblStamp + `')`},
		{"workflow_contract_law_revisions", `INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('work-rbl',1,'law-rbl','` + rblDigest + `')`},
		{"workflow_impact_edges", `INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES('work-rbl','edge-rbl','modifies','hard','work-rbl2','work_item','breaking','` + rblStamp + `')`},
		{"workflow_impact_notices", `INSERT INTO workflow_impact_notices(notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,edge_owner_work_id,edge_id,severity,recorded_at) VALUES('notice:` + strings.Repeat("a", 64) + `','work-rbl',1,'work_item','work-rbl2','work-rbl2','work-rbl','edge-rbl','breaking','` + rblStamp + `')`},
		{"workflow_instances", `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state) VALUES('work-rbl','wf-rebuild-seed',1,'` + rblDigest + `','repair','planned')`},
		{"workflow_checkpoints", `INSERT INTO workflow_checkpoints(work_id,checkpoint_id,step_id,step_kind,attempt_epoch,accepted_inputs_digest,result_evidence_refs,resume_cursor,idempotency_identity,actor_ref,request_id,recorded_at) VALUES('work-rbl','checkpoint-rbl','step-rbl','internal_sqlite',1,'digest','[]','cursor','identity-rbl','` + rblActor + `','request-rbl','` + rblStamp + `')`},
		{"workflow_context_checkpoints", `INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('work-rbl',1,1,'context-rbl','step-rbl',1,'unit-rbl','hypothesis','diagnosis','strategy','["a"]','["b"]','[]','[]','wf-rebuild-seed',1,'` + rblDigest + `','` + rblActor + `','request-rbl','` + rblStamp + `')`},
		{"workflow_context_boundaries", `INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('work-rbl',1,1,1,'boundary-rbl','summary','context-rbl',1,1,'summary','wf-rebuild-seed',1,'` + rblDigest + `','` + rblActor + `','request-rbl','` + rblStamp + `')`},
		{"workflow_external_conditions", `INSERT INTO workflow_external_conditions(work_id,condition_id,await_type,await_ref,resolution_authority,condition_state,recorded_at) VALUES('work-rbl','condition-rbl','human_approval','await-rbl','authority-rbl','open','` + rblStamp + `')`},
		{"workflow_native_runs", `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref) VALUES('work-rbl','run-rbl','start','started','event-rbl','authority-rbl','` + rblActor + `','subject-rbl','` + rblDigest + `','evidence-rbl','digest-rbl','` + rblStamp + `','` + rblStamp + `','trusted_client_report','{}','fresh-rbl','divergence-rbl')`},
		{"workflow_decision_records", `INSERT INTO workflow_decision_records(work_id,question,options_considered,decision,rationale,consequences,inputs,poc_findings,recorded_at) VALUES('work-rbl','question-rbl','["one"]','accepted_decision','rationale','["effect"]','["input"]','findings','` + rblStamp + `')`},
		{"workflow_design_records", `INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES('work-rbl',1,'approach','[{"id":"d1","choice":"c"}]','["ref"]','` + rblStamp + `')`},
		{"workflow_proposal_records", `INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at) VALUES('work-rbl',1,'problem','["a"]','stakes','["outcome"]','[]','[]','` + rblStamp + `')`},
		{"workflow_backlog_alignment", `INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by) VALUES('work-rbl',NULL,'searched','none_found','` + rblStamp + `','` + rblActor + `')`},
		{"workflow_overlap_resolutions", `INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at) VALUES('resolution-rbl',(SELECT seq FROM domain_events WHERE event_id='rbl-work-2'),'prod','work-rbl','work-rbl2',1,1,'depends_on','reason','','` + rblStamp + `')`},
		{"initiative_entries", `INSERT INTO initiative_entries(initiative_work_id,child_work_id,position,required) VALUES('work-rbl','work-rbl2',0,0)`},
		{"work_observations", `INSERT INTO work_observations(observation_id,work_id,statement,recorded_at) VALUES('obs:` + strings.Repeat("a", 16) + `','work-rbl','statement','` + rblStamp + `')`},
		{"work_messages", `INSERT INTO work_messages(message_id,sender_work_id,recipient_work_id,body,state,sent_at) VALUES('msg:` + strings.Repeat("a", 32) + `','work-rbl','work-rbl2','body','sent','` + rblStamp + `')`},
		{"resource_claims", `INSERT INTO resource_claims(resource_key,holder_work_id,holder_agent,holder_session,reason,state,claimed_at) VALUES('resource:rbl','work-rbl','agent-rbl','session-rbl','reason','held','` + rblStamp + `')`},
		{"external_observations", `INSERT INTO external_observations(observation_id,work_id,subject_kind,subject_ref,capture_method,captured_at,reporting_authority_ref,observed_universe,freshness_policy_ref,divergence_policy_ref,created_event_seq) VALUES('xobs:rbl','work-rbl','work_item','work-rbl2','trusted_client_report','` + rblStamp + `','authority-rbl','{}','fresh-rbl','divergence-rbl',(SELECT seq FROM domain_events WHERE event_id='rbl-work-1'))`},
		{"bootstrap_operations", `INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES('idem-rbl','operation-rbl','` + rblDigest + `','{}','prod','proj','work-rbl','/repo',1,'completed','` + rblStamp + `','` + rblStamp + `')`},
		{"linear_outbox", `INSERT INTO linear_outbox(operation_id,work_id,op_kind,idempotency_key,payload,created_at,updated_at) VALUES('operation-rbl','work-rbl','issue_create','idem-outbox-rbl','{}','` + rblStamp + `','` + rblStamp + `')`},
		{"linear_outbox_dispositions", `INSERT INTO linear_outbox_dispositions(operation_id,work_id,disposition,reason,created_at) VALUES('operation-rbl','work-rbl','acknowledged','reason','` + rblStamp + `')`},
		{"worktree_verify_leases", `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('lease-rbl','work-rbl','proj','/repo/wt-rbl','released','client-rbl','agent-rbl','session-rbl','principal-rbl','["true"]','` + rblStamp + `','completed')`},
		{"active_research_packs", `INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('pack-rbl','work-rbl',1,'current',1,'` + rblStamp + `','` + rblStamp + `')`},
		{"active_research_revisions", `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES('pack-rbl',1,'question','[]','[]','[]','method','` + rblStamp + `')`},
		{"active_research_consumers", `INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES('pack-rbl',1,'work-rbl2','context',1,'` + rblStamp + `')`},
	}
}

// A stranded fold guard on a database seeded across every RESTRICT-FK
// projection table must recover: the rebuild clears each seeded projection
// before work_items, folds the log back, and leaves the direct-authority
// active research rows and the event log byte-for-byte in place.
func TestRecoverFoldGuardCompletesOnFullySeededStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	path := s.Path()

	seedRepairProduct(t, s)
	rblWorkEvent := func(eventID, workID, title string) Event {
		return Event{
			EventID:        eventID,
			Kind:           "work.created",
			SubjectType:    SubjectWorkItem,
			SubjectID:      workID,
			Actor:          "operator",
			OccurredAt:     time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
			PayloadVersion: 2,
			Payload:        []byte(`{"work_kind":"task","title":"` + title + `","priority":1}`),
		}
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			rblWorkEvent("rbl-work-1", "work-rbl", "Rebuild seed one"),
			rblWorkEvent("rbl-work-2", "work-rbl2", "Rebuild seed two"),
			operationEvent("rbl-membership-1", "work_project.added", SubjectWorkItem, "work-rbl", map[string]any{
				"work_id": "work-rbl", "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
			operationEvent("rbl-membership-2", "work_project.added", SubjectWorkItem, "work-rbl2", map[string]any{
				"work_id": "work-rbl2", "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectWorkItem, "work-rbl"):  0,
			VersionRef(SubjectWorkItem, "work-rbl2"): 0,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Coverage: every cleared table that carries a RESTRICT foreign key to
	// work_items must hold a seeded row, and every cleared table a seeded
	// row names must be in the clear list or exempt direct authority.
	// work_projects is the one RESTRICT-FK exception: the replayed
	// membership events populate it, and its one-primary-per-work index
	// leaves no seedable row beside them.
	restrictTargets := map[string]bool{
		"external_observations": true, "initiative_entries": true,
		"linear_outbox_dispositions": true, "resource_claims": true,
		"work_messages": true, "work_observations": true,
		"workflow_backlog_alignment": true, "workflow_checkpoints": true,
		"workflow_context_boundaries": true, "workflow_context_checkpoints": true,
		"workflow_contract_law_revisions": true, "workflow_contracts": true,
		"workflow_decision_records": true, "workflow_design_records": true,
		"workflow_external_conditions": true, "workflow_impact_edges": true,
		"workflow_impact_notices": true, "workflow_instances": true,
		"workflow_native_runs": true, "workflow_overlap_resolutions": true,
		"workflow_proposal_records": true, "worktree_verify_leases": true,
	}
	seeded := map[string]bool{}
	db := s.DatabaseForTesting()
	// The projections are fold-only, so the seeds commit inside an open fold
	// scope together with the guard row. Committing without closing the
	// scope strands the guard exactly as a crashed fold leaves the
	// database: projection rows and a guard row, with every later fold
	// refused.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := beginFold(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	_ = scope // deliberately never closed: the commit strands the guard
	for _, seed := range rblSeedStatements() {
		if _, err := tx.ExecContext(ctx, seed.insert); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed %s: %v", seed.table, err)
		}
		seeded[seed.table] = true
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Drift guards: a seed may only target a cleared projection or
	// direct-table authority (active research, the snapshotted runtime
	// authority tables, or the untouched linear_outbox), and every
	// RESTRICT-FK projection table must hold a seed.
	snapshotSet := map[string]bool{}
	for _, table := range operationalRebuildTables {
		snapshotSet[table] = true
	}
	for table := range seeded {
		if _, inList := rebuildClearTableSet()[table]; inList {
			continue
		}
		if strings.HasPrefix(table, "active_research_") {
			continue // direct-table authority; snapshotted and restored, never cleared
		}
		if table == "linear_outbox" {
			continue // durable queue; no foreign key, so the rebuild never touches it
		}
		if snapshotSet[table] {
			continue // direct-table authority; snapshotted and restored, never cleared
		}
		t.Errorf("seeded table %s is neither in the clear list nor exempt direct authority; update the seed set", table)
	}
	for table := range restrictTargets {
		if !seeded[table] {
			t.Errorf("RESTRICT-FK projection table %s has no seed; a recovery proven without it proves nothing", table)
		}
	}

	var events int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := RecoverFoldGuard(ctx, path)
	if err != nil {
		t.Fatalf("RecoverFoldGuard() on a fully seeded store error = %v", err)
	}
	if !report.Rebuilt || report.Events != events {
		t.Fatalf("report = %+v, want rebuilt with %d events", report, events)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after recovery error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rdb := reopened.DatabaseForTesting()

	if got := foldGuardRows(t, rdb); got != 0 {
		t.Fatalf("guard rows after recovery = %d, want 0", got)
	}

	// The log-replayed authorities survive: both work items, their product
	// and project.
	for _, check := range []struct{ table, id string }{
		{"work_items", "work-rbl"}, {"work_items", "work-rbl2"}, {"products", "prod"}, {"projects", "proj"},
	} {
		var n int
		if err := rdb.QueryRowContext(ctx, `SELECT count(*) FROM `+check.table+` WHERE id=?`, check.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s row %s = %d after recovery, want 1", check.table, check.id, n)
		}
	}

	// Direct-table authority survives byte-for-byte: active research, the
	// snapshotted runtime authority tables, and the untouched linear_outbox
	// queue. A queued Linear operation is pending work no event can restore,
	// so its survival is the point of the snapshot treatment.
	for _, check := range []struct{ table, condition string }{
		{"active_research_packs", "pack_id='pack-rbl'"},
		{"active_research_revisions", "pack_id='pack-rbl'"},
		{"active_research_consumers", "pack_id='pack-rbl'"},
		{"bootstrap_operations", "operation_id='operation-rbl'"},
		{"linear_outbox", "operation_id='operation-rbl'"},
		{"linear_outbox_dispositions", "operation_id='operation-rbl'"},
		{"worktree_verify_leases", "lease_id='lease-rbl'"},
	} {
		var n int
		if err := rdb.QueryRowContext(ctx, `SELECT count(*) FROM `+check.table+` WHERE `+check.condition).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s rows with %s = %d after recovery, want 1", check.table, check.condition, n)
		}
	}

	// Every seeded cleared projection loses its rows: the log does not
	// restore any of them in this fixture, so any survivor is a row the
	// rebuild failed to clear.
	for _, seed := range rblSeedStatements() {
		if strings.HasPrefix(seed.table, "active_research_") {
			continue
		}
		if snapshotSet[seed.table] || seed.table == "linear_outbox" {
			continue // direct-table authority; preserved above
		}
		var n int
		if err := rdb.QueryRowContext(ctx, `SELECT count(*) FROM `+seed.table).Scan(&n); err != nil {
			t.Fatalf("count %s after recovery: %v", seed.table, err)
		}
		if n != 0 {
			t.Errorf("%s holds %d rows after recovery, want 0", seed.table, n)
		}
	}

	var eventsAfter int64
	if err := rdb.QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != events {
		t.Fatalf("event log rows = %d after recovery, want %d", eventsAfter, events)
	}
}
