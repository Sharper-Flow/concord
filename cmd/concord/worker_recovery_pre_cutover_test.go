package main

// Pre-cutover recovery witnesses at the real evidence boundary. A store that
// recorded a dispatched attempt under a released 1.0 or 1.1 packet/report
// pair — with the canonical digest of the exact retained packet bytes on its
// dispatch window — must finish that attempt from the original completed
// host Task through the ordinary signed evidence verbs, even when the
// terminal event never persisted. Retiring the live dispatch producer did
// not retire these attempts: the completion fold owns the report identity by
// comparing it against the pair the attempt recorded, not by today's
// current revision.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

// canonicalPacketDigest computes the digest the dispatch_worker authorization
// records for one packet: the canonical JSON encoding of the exact packet
// bytes, sha256-digested.
func canonicalPacketDigest(t *testing.T, packet map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// seedPreCutoverDispatchWindow records the dispatch_worker authorization for
// one attempt against the exact canonical digest of the retained packet, the
// way the pre-cutover coordinator recorded it. The digest is written at
// insert time: the domain log is append-only, so no seeding step may rewrite
// a recorded authorization.
func seedPreCutoverDispatchWindow(t *testing.T, dbPath, workID, attemptID, packetDigest string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	actor := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: store.ActorAgent}
	actorRef, err := store.WorkflowActorRef(actor)
	if err != nil {
		t.Fatalf("compute actor_ref: %v", err)
	}
	entry, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	expected := int64(2)
	resulting := expected + 1
	worktreeDir := filepath.Join(filepath.Dir(dbPath), "worktree-"+workID)
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	worktreePath, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		t.Fatalf("resolve worktree: %v", err)
	}
	worktreeSum := sha256.Sum256([]byte(filepath.Clean(worktreePath)))
	worktreeIdentity := "sha256:" + hex.EncodeToString(worktreeSum[:])
	startPayload := mustJSON(t, map[string]any{
		"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
		"step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 1,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64),
		"idempotency_identity":   "dispatch-open:" + attemptID, "actor_ref": actorRef,
	})
	completionPayload := mustJSON(t, map[string]any{
		"work_id": workID, "expected_version": resulting, "resulting_version": resulting + 1,
		"step_id": "execution", "action_id": "dispatch_worker", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": actorRef,
		"worker_attempt_id":           attemptID,
		"worker_packet_digest":        packetDigest,
		"worker_packet_predicate_ids": []string{},
		"worker_worktree_identity":    worktreeIdentity,
	})
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-1', 'Concord', 'prototype', 'operator_only', 1, '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-1', 'Repository', 1, '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES('product-1', 'project-1', 'primary')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?, 'task', ?, 'in_progress', 1, 2, '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')`, workID, "pre-cutover "+workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_projects(work_id,project_id,role) VALUES(?, 'project-1', 'primary')`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?, ?, ?, ?, ?, 'agent', '2026-08-22T00:00:00Z')`, actorRef, actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?, 'project-1', ?, 'work/dispatch', ?, ?, 'repo-1', 'active', '2026-08-22T00:00:00Z', '{}')`, store.WorktreeSetID(workID), "claim-"+attemptID, strings.Repeat("a", 40), worktreePath); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES(?, 'workflow.implementation', ?, ?, 'execution', 'running', ?, '2026-08-22T00:00:00Z')`, workID, entry.Definition.Version, entry.Digest, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_started', 'work_item', ?, ?, '2026-08-22T00:00:00Z', 1, ?)`, "dispatch-start-"+attemptID, workID, actorRef, []byte(startPayload)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_completed', 'work_item', ?, ?, '2026-08-22T00:00:00Z', 2, ?)`, "dispatch-completed-"+attemptID, workID, actorRef, []byte(completionPayload)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload,event_id,start_seq,start_payload)
SELECT completion.subject_id,?,completion.seq,completion.payload,completion.event_id,start.seq,start.payload
FROM domain_events completion JOIN domain_events start ON start.event_id=? WHERE completion.event_id=?`, attemptID, "dispatch-start-"+attemptID, "dispatch-completed-"+attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedPreCutoverDispatchedAttempt records the worker.dispatched evidence and
// its projection row exactly as a pre-cutover binary left them: the released
// pair on both, the job binding when the dispatch carried one, and no
// terminal event.
func seedPreCutoverDispatchedAttempt(t *testing.T, dbPath, workID, eventID, attemptID string, lane store.LaneDefinition, readback, packetDigest string, pair [2]string, job *store.WorkerJobBinding) {
	t.Helper()
	payload := store.WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, ReadbackModel: readback,
		PacketSchemaVersion: pair[0], ReportSchemaVersion: pair[1],
		PacketDigest: packetDigest,
		HostProvenance: &store.WorkerHostProvenance{
			Digest:  "sha256:" + strings.Repeat("a", 64),
			Sources: []store.WorkerHostProvenanceSource{{Kind: "agent_definition", Path: ".opencode/agents/" + lane.ID + ".md", SHA256: "sha256:" + strings.Repeat("b", 64)}},
		},
		WorkerJob: job,
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db := s.DatabaseForTesting()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events (event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES (?,?,?,?,?,'2026-10-01T00:00:00Z',5,?)`,
		eventID, store.WorkerDispatched, "work_item", workID, "client:"+workerEvidenceClientRef+":operator-1", mustJSON(t, payload)); err != nil {
		t.Fatalf("seed pre-cutover dispatch event: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO worker_attempts (work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at,dispatch_event_id,dispatch_actor,dispatch_payload,dispatch_payload_version,dispatch_seq) VALUES (?,?,?,?,?,?,?,?,?,'dispatched','','','2026-10-01T00:00:00Z',NULL,NULL,?,?,?,5,last_insert_rowid())`,
		workID, attemptID, lane.ID, lane.Version, lane.Digest, lane.CapabilityClass, readback, pair[0], pair[1], eventID, "client:"+workerEvidenceClientRef+":operator-1", mustJSON(t, payload)); err != nil {
		t.Fatalf("seed pre-cutover attempt row: %v", err)
	}
	// Migration 145 backfills this identity for an older binary's dispatch.
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(mustJSON(t, payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) SELECT event_id,seq,kind,subject_type,subject_id,actor,payload_version,? FROM domain_events WHERE event_id=?`, "sha256:"+hex.EncodeToString(sum[:]), eventID); err != nil {
		t.Fatalf("seed migrated dispatch identity: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// retainedCardBearingPacket is the exact retained packet of a pre-cutover 1.0
// dispatch: the released card-bearing work-context shape, later retired by
// the card-free revision, carried verbatim in the host transcript.
func retainedCardBearingPacket(t *testing.T, workID, attemptID string, lane store.LaneDefinition, pair [2]string) map[string]any {
	t.Helper()
	return map[string]any{
		"schema_version": pair[0],
		"attempt_id":     attemptID,
		"lane_id":        lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"work_id": workID, "step_id": "execution",
		"inputs": map[string]any{
			"task":    "verify the retained change",
			"binding": map[string]any{"objective_source": "contract_premise", "work_version": 2, "contract_version": 1, "assigned_result": lane.EvidenceObligations[0]},
			"work_context": map[string]any{
				"source_event_frontier": 7, "required_reading": []any{}, "findings": []any{},
				"domain_groups": []any{map[string]any{
					"domain_id":                 "domain:retained",
					"required_reading_ordinals": []any{},
					"finding_ids":               []any{},
					"domain_cards": []any{map[string]any{
						"kind": "repository_file", "project_id": "project-1",
						"path": "internal/store/worker_lanes.go", "commit_oid": strings.Repeat("a", 40),
					}},
				}},
			},
		},
	}
}

// retainedJobBoundPacket is the exact retained packet of a pre-cutover 1.1
// job-bound dispatch: the released job-capable shape with the recorded
// revision the dispatch authorized.
func retainedJobBoundPacket(t *testing.T, workID, attemptID string, lane store.LaneDefinition, pair [2]string, job store.WorkerJobBinding) map[string]any {
	t.Helper()
	return map[string]any{
		"schema_version": pair[0],
		"attempt_id":     attemptID,
		"lane_id":        lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"work_id": workID, "step_id": "execution",
		"inputs": map[string]any{
			"task":    "repair the retained change",
			"binding": map[string]any{"objective_source": "contract_premise", "work_version": 2, "contract_version": 1, "assigned_result": lane.EvidenceObligations[0]},
			"worker_job": map[string]any{
				"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest,
				"objective": "repair the retained change", "stopping_condition": "the recorded checks pass",
				"project_scope": "project-1", "path_scope": []any{}, "predicate_ids": []any{},
				"checks": []any{}, "prerequisites": []any{}, "unresolved_refs": []any{},
				"reserved_integration": "",
			},
		},
	}
}

func runPreCutoverEvidence(t *testing.T, verb string, key []byte, lane store.LaneDefinition, readback, nonce string, mutate func(request map[string]any, assertion *agent.WorkerEvidenceAssertion)) (int, string) {
	t.Helper()
	request, assertion := workerEvidenceRequest(t, verb, lane, readback, nonce)
	if mutate != nil {
		mutate(request, &assertion)
	}
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	var out, diagnostic bytes.Buffer
	code := runWithInput([]string{verb}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic)
	return code, diagnostic.String()
}

// A pre-cutover dispatched attempt whose packet carried the released
// card-bearing 1.0 shape finishes through the real evidence boundary: the
// exact replay acknowledges the original dispatch by its event identity and
// packet digest, and the retained completion records under the pair the
// attempt recorded, leaving that pair on the row.
func TestWorkerRecoveryFinishesPreCutoverCardBearingDispatchedAttempt(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	pair := [2]string{store.WorkerPacketSchemaVersionLegacy, store.WorkerReportSchemaVersionLegacy}
	packet := retainedCardBearingPacket(t, "work-1", "attempt-1", lane, pair)
	digest := canonicalPacketDigest(t, packet)
	seedPreCutoverDispatchWindow(t, dbPath, "work-1", "attempt-1", digest)
	seedPreCutoverDispatchedAttempt(t, dbPath, "work-1", "event-pre-cutover-dispatch-1.0", "attempt-1", lane, readback, digest, pair, nil)

	// Exact replay of the original dispatch evidence: same event identity,
	// same released pair, same canonical packet digest.
	for i, nonce := range []string{"nonce-precutover-ack-1.0-001", "nonce-precutover-ack-1.0-002"} {
		code, diagnostic := runPreCutoverEvidence(t, "worker-dispatch", key, lane, readback, nonce, func(request map[string]any, assertion *agent.WorkerEvidenceAssertion) {
			request["event_id"] = "event-pre-cutover-dispatch-1.0"
			request["packet_schema_version"] = pair[0]
			request["report_schema_version"] = pair[1]
			request["packet_digest"] = digest
			assertion.PacketDigest = digest
		})
		if code != 0 {
			t.Fatalf("exact replay of the pre-cutover dispatch (pass %d): exit=%d stderr=%s", i+1, code, diagnostic)
		}
	}

	// The terminal event never persisted; the retained completed host Task
	// finishes the attempt under the report identity the dispatch recorded.
	code, diagnostic := runPreCutoverEvidence(t, "worker-complete", key, lane, readback, "nonce-precutover-complete-1.01", func(request map[string]any, assertion *agent.WorkerEvidenceAssertion) {
		request["event_id"] = "event-pre-cutover-complete-1.0"
		request["report_schema_version"] = pair[1]
	})
	if code != 0 {
		t.Fatalf("retained completion of the pre-cutover dispatched attempt: exit=%d stderr=%s", code, diagnostic)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var lifecycle, packetVersion, reportVersion string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,packet_schema_version,report_schema_version FROM worker_attempts WHERE attempt_id='attempt-1'`).Scan(&lifecycle, &packetVersion, &reportVersion); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || packetVersion != pair[0] || reportVersion != pair[1] {
		t.Fatalf("recovered attempt = %s at %s/%s, want completed at the recorded pair %s/%s", lifecycle, packetVersion, reportVersion, pair[0], pair[1])
	}
}

// A pre-cutover dispatched attempt whose packet carried the released
// job-capable 1.1 shape and the recorded job revision finishes the same way:
// the retained completion reclaims the job binding the dispatch evidence
// carried, under the exact original packet digest.
func TestWorkerRecoveryFinishesPreCutoverJobBoundDispatchedAttempt(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	pair := [2]string{store.WorkerPacketSchemaVersionPrior, store.WorkerReportSchemaVersionPrior}
	recording := store.WorkerJobRecordedPayload{
		JobID: "job:pre-cutover-recovery", Revision: 2, ContractVersion: 1,
		Objective: "repair the retained change", StoppingCondition: "the recorded checks pass", ProjectScope: "project-1",
		PathScope: []string{}, PredicateIDs: []string{}, Checks: []string{}, Prerequisites: []store.WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
	}
	recording.Digest = store.DeriveWorkerJobDigest(recording)
	job := store.WorkerJobBinding{JobID: recording.JobID, Revision: recording.Revision, Digest: recording.Digest}
	packet := retainedJobBoundPacket(t, "work-1", "attempt-1", lane, pair, job)
	digest := canonicalPacketDigest(t, packet)
	seedPreCutoverDispatchWindow(t, dbPath, "work-1", "attempt-1", digest)
	// The dispatched binding names immutable job content, not an unattested
	// digest. Retain its recording beside the pre-cutover dispatch evidence.
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, recordErr := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('pre-cutover-job','worker.job_recorded','work_item','work-1','operator','2026-09-30T00:00:00Z',1,?)`, mustJSON(t, recording))
	if recordErr == nil {
		_, recordErr = s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,recorded_payload,recorded_seq,recorded_event_id)
SELECT subject_id,json_extract(payload,'$.job_id'),json_extract(payload,'$.revision'),json_extract(payload,'$.contract_version'),json_extract(payload,'$.objective'),json_extract(payload,'$.stopping_condition'),json_extract(payload,'$.project_scope'),json_extract(payload,'$.path_scope'),json_extract(payload,'$.predicate_ids'),json_extract(payload,'$.checks'),json_extract(payload,'$.prerequisites'),json_extract(payload,'$.unresolved_refs'),json_extract(payload,'$.digest'),'recorded',occurred_at,payload,seq,event_id
FROM domain_events WHERE event_id='pre-cutover-job';
DELETE FROM fold_guard;`)
	}
	_ = s.Close()
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	seedPreCutoverDispatchedAttempt(t, dbPath, "work-1", "event-pre-cutover-dispatch-1.1", "attempt-1", lane, readback, digest, pair, &job)

	code, diagnostic := runPreCutoverEvidence(t, "worker-dispatch", key, lane, readback, "nonce-precutover-ack-1.1-001", func(request map[string]any, assertion *agent.WorkerEvidenceAssertion) {
		request["event_id"] = "event-pre-cutover-dispatch-1.1"
		request["packet_schema_version"] = pair[0]
		request["report_schema_version"] = pair[1]
		request["packet_digest"] = digest
		request["worker_job"] = map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest}
		assertion.PacketDigest = digest
	})
	if code != 0 {
		t.Fatalf("exact replay of the pre-cutover job-bound dispatch: exit=%d stderr=%s", code, diagnostic)
	}
	code, diagnostic = runPreCutoverEvidence(t, "worker-complete", key, lane, readback, "nonce-precutover-complete-1.11", func(request map[string]any, assertion *agent.WorkerEvidenceAssertion) {
		request["event_id"] = "event-pre-cutover-complete-1.1"
		request["report_schema_version"] = pair[1]
		request["worker_job"] = map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest}
	})
	if code != 0 {
		t.Fatalf("retained job-bound completion of the pre-cutover dispatched attempt: exit=%d stderr=%s", code, diagnostic)
	}
	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var lifecycle, reportVersion string
	var completionJob string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.lifecycle_state, a.report_schema_version, json_extract(c.payload,'$.worker_job.job_id') FROM worker_attempts a JOIN domain_events c ON c.kind=? AND json_extract(c.payload,'$.attempt_id')='attempt-1' WHERE a.attempt_id='attempt-1'`, store.WorkerCompleted).Scan(&lifecycle, &reportVersion, &completionJob); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || reportVersion != pair[1] || completionJob != job.JobID {
		t.Fatalf("recovered job-bound attempt = %s at %s with job %q, want completed at %s with %q", lifecycle, reportVersion, completionJob, pair[1], job.JobID)
	}
}

// The completion fold owns the recorded report identity: a completion that
// claims a revision the attempt was never dispatched under refuses, so
// recovery can finish recorded history but never relabel it.
func TestWorkerRecoveryRefusesUnrecordedReportIdentity(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	pair := [2]string{store.WorkerPacketSchemaVersionLegacy, store.WorkerReportSchemaVersionLegacy}
	packet := retainedCardBearingPacket(t, "work-1", "attempt-1", lane, pair)
	digest := canonicalPacketDigest(t, packet)
	seedPreCutoverDispatchWindow(t, dbPath, "work-1", "attempt-1", digest)
	seedPreCutoverDispatchedAttempt(t, dbPath, "work-1", "event-pre-cutover-dispatch-mismatch", "attempt-1", lane, readback, digest, pair, nil)
	code, diagnostic := runPreCutoverEvidence(t, "worker-complete", key, lane, readback, "nonce-precutover-mismatch-001", func(request map[string]any, assertion *agent.WorkerEvidenceAssertion) {
		request["event_id"] = "event-pre-cutover-complete-mismatch"
		request["report_schema_version"] = store.WorkerReportSchemaVersion
	})
	if code == 0 {
		t.Fatal("a completion claiming the current revision against a pre-cutover attempt was admitted")
	}
	if !strings.Contains(diagnostic, "report_schema_version") {
		t.Fatalf("refusal %s does not name the recorded report identity", diagnostic)
	}
}
