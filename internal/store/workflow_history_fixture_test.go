package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func progressHistory(t *testing.T) (*sql.DB, func(string, any) int64, func() *WorkflowCorrectionContext) {
	t.Helper()
	db := correctionHistoryDatabase(t)
	progressFixtureTables(t, db)
	var seq int64
	appendEvent := func(kind string, payload any) int64 {
		t.Helper()
		seq++
		if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,?,?,?,?,?,?)`, seq, string(SubjectWorkItem), "work-history", kind, mustJSONValue(payload), "", fmt.Sprintf("progress-e%d", seq)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO effect_receipts SELECT event_id,seq,kind,subject_type,subject_id FROM domain_events WHERE seq=?`, seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(WorkflowDefinitionSelected, workflowDefinitionSelectedPayload{Ref: entry.Definition.Ref, Digest: entry.Digest})
	appendEvent(WorkflowContractApproved, workflowContractApprovedPayload{ContractVersion: 1, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}})
	read := func() *WorkflowCorrectionContext {
		t.Helper()
		correction, err := readWorkflowCorrectionContext(context.Background(), db, "work-history")
		if err != nil {
			t.Fatal(err)
		}
		return correction
	}
	return db, appendEvent, read
}

func progressCorrectionHistory(t *testing.T) (*sql.DB, func(string, any) int64, func() *WorkflowCorrectionContext) {
	t.Helper()
	db, appendEvent, read := progressHistory(t)
	correctionFixtureTables(t, db)
	return db, func(kind string, payload any) int64 {
		seq := appendEvent(kind, payload)
		applyCorrectionFixture(t, db, seq, fmt.Sprintf("progress-e%d", seq), kind, payload)
		return seq
	}, read
}

// progressFixtureTables creates the actor and producer projections used by
// review authority and evidence fixtures.
func progressFixtureTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`DROP TABLE domain_events`,
		`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload BLOB,actor TEXT NOT NULL DEFAULT '',event_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE effect_receipts(event_id TEXT PRIMARY KEY,effect_seq INTEGER,kind TEXT,subject_type TEXT,subject_id TEXT)`,
		`CREATE TABLE workflow_actors (actor_ref TEXT PRIMARY KEY, principal_ref TEXT NOT NULL, client_ref TEXT NOT NULL, agent_ref TEXT NOT NULL, session_ref TEXT NOT NULL, actor_class TEXT NOT NULL)`,
		`CREATE TABLE workflow_instances (work_id TEXT PRIMARY KEY, execution_actor_ref TEXT)`,
		`CREATE TABLE durable_operations (op_id TEXT, attempt_epoch INTEGER NOT NULL DEFAULT 1, work_id TEXT NOT NULL, principal_ref TEXT NOT NULL, request_id TEXT NOT NULL, result_kind TEXT, evidence_refs TEXT NOT NULL DEFAULT '[]')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

// progressActorRecord inserts one canonical actor tuple and returns its
// derived reference; replay validators require that canonical identity.
func progressActorRecord(t *testing.T, db *sql.DB, name, class string) string {
	t.Helper()
	ref := DeriveWorkflowActorRef("principal:fixture", "client:fixture", "agent:"+name, "session:"+name)
	if _, err := db.Exec(`INSERT INTO workflow_actors VALUES(?,?,?,?,?,?)`, ref, "principal:fixture", "client:fixture", "agent:"+name, "session:"+name, class); err != nil {
		t.Fatal(err)
	}
	return ref
}

func progressCompletedOperation(t *testing.T, db *sql.DB, opID, producerID, requestID, subjectRef string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO durable_operations(op_id,work_id,principal_ref,request_id,result_kind,evidence_refs) VALUES(?,?,?,?,?,?)`, opID, "work-history", producerID, requestID, "completed", mustJSONValue([]string{subjectRef})); err != nil {
		t.Fatal(err)
	}
}

// progressQualifiedEvidence binds the required artifact kind with a completed
// durable producer operation behind it.
func progressQualifiedEvidence(t *testing.T, db *sql.DB, appendEvent func(string, any) int64, ref string) {
	t.Helper()
	progressCompletedOperation(t, db, "run:qualified", "producer:independent", "request:qualified", ref)
	appendEvent(WorkflowEvidenceBound, progressEvidence("artifact", ref))
}

func progressEvidence(kind, ref string) workflowEvidenceBoundPayload {
	return workflowEvidenceBoundPayload{EvidenceKind: kind, ImmutableSubjectRef: ref, ProducerID: "producer:independent", ProducerRunRef: "run:qualified", ProducerWatermark: "request:qualified", ObservedAt: "2026-10-20T00:00:00Z"}
}
