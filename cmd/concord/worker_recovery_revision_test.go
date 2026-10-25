package main

// Retained-result recovery binds the exact recorded schema
// identity. The recovery context exposes the retained packet/report pair the
// original dispatch recorded, refuses an attempt whose lane definition no
// longer resolves, and refuses a recorded dispatch whose schema pair is not
// one of the released revisions — typed, before any recovery worker or
// replacement evidence can exist.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func recoveryContextForTest(t *testing.T, dbPath string) store.WorkerRecoveryContext {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	worktree := filepath.Join(filepath.Dir(dbPath), "worktree-work-1")
	actor := "client:" + workerEvidenceClientRef + ":operator-1"
	var recovery store.WorkerRecoveryContext
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		var err error
		recovery, err = store.WorkerRecoveryContextTx(context.Background(), tx, "work-1", "attempt-1", "project-1", worktree, actor)
		return err
	}); err != nil {
		t.Fatalf("recovery context refused: %v", err)
	}
	return recovery
}

func TestWorkerRecoveryContextCarriesRetainedSchemaPair(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, preferredLaneModel(lane))
	recovery := recoveryContextForTest(t, dbPath)
	raw, err := json.Marshal(recovery)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["packet_schema_version"] != store.WorkerPacketSchemaVersion || wire["report_schema_version"] != store.WorkerReportSchemaVersion {
		t.Fatalf("recovery wire carries %v/%v, want the retained pair on the recovery surface", wire["packet_schema_version"], wire["report_schema_version"])
	}
}

func TestWorkerRecoveryContextRefusesUnavailableLaneDefinition(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	// A recorded dispatch whose lane digest no registered definition holds:
	// the log row and the fold-only projection row both record it, the way a
	// store whose lane definition retired from the registry would carry it.
	lostLane := "sha256:" + strings.Repeat("0", 64)
	dispatchPayload := mustJSON(t, store.WorkerDispatchedPayload{
		AttemptID: "attempt-1", LaneID: "research", LaneDigest: lostLane,
		CapabilityClass: "research", ReadbackModel: "openai/test-model",
		PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion,
		PacketDigest: "sha256:" + strings.Repeat("c", 64),
		HostProvenance: &store.WorkerHostProvenance{
			Digest:  "sha256:" + strings.Repeat("a", 64),
			Sources: []store.WorkerHostProvenanceSource{{Kind: "agent_definition", Path: ".opencode/agents/research.md", SHA256: "sha256:" + strings.Repeat("b", 64)}},
		},
	})
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
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events (event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES ('event-recovery-lost-lane',?,'work_item','work-1',?,'2026-10-10T00:00:00Z',5,?)`, store.WorkerDispatched, "client:"+workerEvidenceClientRef+":operator-1", dispatchPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO worker_attempts (work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at,dispatch_event_id,dispatch_actor,dispatch_payload_version,dispatch_payload,dispatch_seq) VALUES ('work-1','attempt-1','research',?,'research','openai/test-model',?,?,'dispatched','','','2026-10-10T00:00:00Z',NULL,NULL,'event-recovery-lost-lane',?,5,?,last_insert_rowid())`,
		lostLane, store.WorkerPacketSchemaVersion, store.WorkerReportSchemaVersion, "client:"+workerEvidenceClientRef+":operator-1", dispatchPayload); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.WorkerRecoveryContextTx(context.Background(), tx, "work-1", "attempt-1", "project-1", filepath.Join(filepath.Dir(dbPath), "worktree-work-1"), "client:"+workerEvidenceClientRef+":operator-1")
		return err
	}); err == nil {
		t.Fatal("recovery admitted an attempt whose recorded lane definition does not resolve")
	} else if !strings.Contains(err.Error(), "lane") {
		t.Fatalf("recovery refusal %v does not name the unavailable lane", err)
	}
}

func TestWorkerRecoveryContextRefusesUnretainedSchemaPair(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, preferredLaneModel(lane))
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Corrupt the current dispatch descriptor the recovery boundary reads.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
UPDATE worker_attempts SET dispatch_payload=json_set(dispatch_payload,'$.packet_schema_version','9.9','$.report_schema_version','9.9') WHERE work_id='work-1' AND attempt_id='attempt-1';
DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	err = s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.WorkerRecoveryContextTx(context.Background(), tx, "work-1", "attempt-1", "project-1", filepath.Join(filepath.Dir(dbPath), "worktree-work-1"), "client:"+workerEvidenceClientRef+":operator-1")
		return err
	})
	if err == nil {
		t.Fatal("recovery admitted a recorded dispatch whose schema pair is not a released revision")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Fatalf("refusal %v does not name the schema identity", err)
	}
}
