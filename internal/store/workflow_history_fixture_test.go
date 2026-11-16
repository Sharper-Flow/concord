package store

import (
	"database/sql"
	"testing"
)

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
