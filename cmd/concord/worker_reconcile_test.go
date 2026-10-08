package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkerEvidenceExactReconciliation(t *testing.T) {
	for _, verb := range []string{"worker-dispatch", "worker-complete", "worker-fail"} {
		t.Run(verb, func(t *testing.T) {
			dbPath := freshMigratedCLIDatabase(t)
			key := seedWorkerEvidenceClient(t)
			lane := store.BuiltinLaneDefinitions()[0]
			readback := preferredLaneModel(lane)
			if verb == "worker-dispatch" {
				seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
			} else {
				seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
			}
			request, assertion := workerEvidenceRequest(t, verb, lane, readback, "nonce-reconcile-first-001")
			request["assertion"] = signWorkerEvidence(t, key, assertion)
			var out, diagnostic bytes.Buffer
			if code := runWithInput([]string{verb}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
				t.Fatalf("initial append: exit=%d %s", code, diagnostic.String())
			}
			for i := range 2 {
				assertion.Nonce = fmt.Sprintf("nonce-reconcile-repeat-%03d", i)
				request["assertion"] = signWorkerEvidence(t, key, assertion)
				out.Reset()
				diagnostic.Reset()
				if code := runWithInput([]string{verb}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
					t.Fatalf("exact authenticated reconciliation %d: exit=%d %s", i, code, diagnostic.String())
				}
			}
			// Reusing the last signed assertion is not a reconciliation credential.
			if code := runWithInput([]string{verb}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code == 0 {
				t.Fatal("consumed assertion replay succeeded")
			}
			s, err := store.Open(context.Background(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var count int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id=?`, request["event_id"]).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("event count=%d, want 1", count)
			}
		})
	}
}

func TestWorkerRecoveryContextOriginalIdentity(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, preferredLaneModel(lane))
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	worktree := filepath.Join(filepath.Dir(dbPath), "worktree-work-1")
	actor := "client:" + workerEvidenceClientRef + ":operator-1"
	var recovery store.WorkerRecoveryContext
	err = s.Transact(context.Background(), func(tx *store.Transaction) error {
		var err error
		recovery, err = store.WorkerRecoveryContextTx(context.Background(), tx, "work-1", "attempt-1", "project-1", worktree, actor)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovery.CoordinatorSession != "session/work-1" || recovery.AttemptEpoch != 1 || recovery.DispatchEventID == "" || recovery.Dispatch.HostProvenance == nil || recovery.Dispatch.ReadbackModel != preferredLaneModel(lane) || recovery.Worktree != worktree {
		t.Fatalf("original recovery identity = %+v", recovery)
	}
	for _, fault := range []string{"project", "principal", "worktree", "attempt"} {
		t.Run(fault, func(t *testing.T) {
			project, principal, tree, attempt := "project-1", actor, worktree, "attempt-1"
			switch fault {
			case "project":
				project = "another-project"
			case "principal":
				principal = "client:another-client:operator-2"
			case "worktree":
				tree = filepath.Dir(worktree)
			case "attempt":
				attempt = "another-attempt"
			}
			if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
				_, err := store.WorkerRecoveryContextTx(context.Background(), tx, "work-1", attempt, project, tree, principal)
				return err
			}); err == nil {
				t.Fatal("mismatched recovery identity admitted")
			}
		})
	}
	request, assertion := workerEvidenceRequest(t, "worker-fail", lane, preferredLaneModel(lane), "nonce-recovery-terminal-fail001")
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	var out, diagnostic bytes.Buffer
	if code := runWithInput([]string{"worker-fail"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
		t.Fatalf("record real terminal failure: %s", diagnostic.String())
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.WorkerRecoveryContextTx(context.Background(), tx, "work-1", "attempt-1", "project-1", worktree, actor)
		return err
	}); err == nil {
		t.Fatal("failed attempt admitted for completed-report recovery")
	}
}

func TestWorkerEvidenceReconciliationRefusesChangedPayloadAndRevokedCaller(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, preferredLaneModel(lane))
	request, assertion := workerEvidenceRequest(t, "worker-complete", lane, preferredLaneModel(lane), "nonce-reconcile-payload001")
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	var out, diagnostic bytes.Buffer
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
		t.Fatalf("initial completion: %s", diagnostic.String())
	}
	original := request["evidence"]
	request["evidence"] = []map[string]any{{"obligation": lane.EvidenceObligations[0], "detail": "changed result"}}
	assertion.Nonce = "nonce-reconcile-changed001"
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	out.Reset()
	diagnostic.Reset()
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code == 0 || !strings.Contains(diagnostic.String(), "identity conflicts") {
		t.Fatalf("changed payload admitted: exit=%d %s", code, diagnostic.String())
	}
	request["evidence"] = original
	originalModel := request["readback_model"]
	request["readback_model"] = "openai/different-recovery-model"
	assertion.ReadbackModel = "openai/different-recovery-model"
	assertion.Nonce = "nonce-reconcile-model001"
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	out.Reset()
	diagnostic.Reset()
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code == 0 || !strings.Contains(diagnostic.String(), "identity conflicts") {
		t.Fatalf("changed original model admitted: exit=%d %s", code, diagnostic.String())
	}
	request["readback_model"] = originalModel
	assertion.ReadbackModel = originalModel.(string)
	runCLIJSON(t, []string{"client-revoke"}, map[string]any{"client_ref": workerEvidenceClientRef})
	assertion.Nonce = "nonce-reconcile-revoked001"
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	out.Reset()
	diagnostic.Reset()
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code == 0 {
		t.Fatal("revoked caller reconciled existing completion")
	}
}

func TestWorkerEvidenceDurableReconciliationIgnoresPinnedReader(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	reader, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	request, assertion := workerEvidenceRequest(t, "worker-dispatch", lane, preferredLaneModel(lane), "nonce-busy-effect-first001")
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	var out, diagnostic bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
		t.Fatalf("FULL commit under a pinned reader: exit=%d %s", code, diagnostic.String())
	}
	var result struct {
		EventIDs []string `json:"event_ids"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.EventIDs) == 0 {
		t.Fatalf("missing acknowledged event identity: %v; stdout=%s stderr=%s", err, out.String(), diagnostic.String())
	}
	assertion.Nonce = "nonce-busy-effect-retry001"
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	out.Reset()
	diagnostic.Reset()
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
		t.Fatalf("exact durable reconciliation under the same pinned reader: exit=%d %s", code, diagnostic.String())
	}
}

// An in_flight binding closed by abandonment (CON-791) has no worker
// dispatch event, so its exact replay validates against the authorization
// window's core-recorded digest rather than a dispatch that never existed.
func TestWorkerAbandonReplayValidatesAnInFlightBindingWithoutDispatchEvidence(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO fold_guard(active) VALUES(1)`,
		`INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('work-1','attempt-1',?,?,?,'implementation','','1.0','1.0','in_flight','2026-01-01T00:00:00Z')`,
		`DELETE FROM fold_guard`,
	} {
		if _, err := seedTx.ExecContext(ctx, statement, lane.ID, lane.Version, lane.Digest); err != nil {
			t.Fatalf("seed in-flight attempt: %v", err)
		}
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbFail, lane, preferredLaneModel(lane), "nonce-inflight-abandon-001")
	delete(request, "readback_model")
	delete(request, "failure_kind")
	assertion.ReadbackModel = ""
	assertion.FailureKind = string(store.WorkerFailureAbandoned)
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	var out, diagnostic bytes.Buffer
	if code := runWithInput([]string{"worker-abandon"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
		t.Fatalf("close the in-flight binding: exit=%d %s", code, diagnostic.String())
	}
	assertion.Nonce = "nonce-inflight-abandon-002"
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	out.Reset()
	diagnostic.Reset()
	if code := runWithInput([]string{"worker-abandon"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
		t.Fatalf("exact replay of the in-flight abandonment: exit=%d %s", code, diagnostic.String())
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=? AND json_extract(payload,'$.failure_kind')=?`, store.WorkerFailed, store.WorkerFailureAbandoned).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("in-flight abandonment replay recorded %d events, want one", events)
	}
}
