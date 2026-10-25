package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// seedCLIReceipt records synthetic current fixture identity at insert time.
// Callers own the matching current row and hold the write guard in this tx.
func seedCLIReceipt(t *testing.T, tx *sql.Tx, event store.Event) int64 {
	t.Helper()
	digest := cliPayloadDigest(t, event.Payload)
	var seq int64
	if err := tx.QueryRow(`UPDATE store_sequences SET last=last+1 WHERE name='effect' RETURNING last`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, seq, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.PayloadVersion, digest); err != nil {
		t.Fatal(err)
	}
	return seq
}

func cliPayloadDigest(t *testing.T, raw []byte) string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func seedCLIAuthorization(t *testing.T, tx *sql.Tx, workID, attemptID, actor, startPayload, completionPayload string) {
	t.Helper()
	startSeq := seedCLIReceipt(t, tx, store.Event{EventID: "dispatch-start-" + attemptID, Kind: store.WorkflowActionStarted, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: actor, PayloadVersion: 1, Payload: []byte(startPayload)})
	completionSeq := seedCLIReceipt(t, tx, store.Event{EventID: "dispatch-completed-" + attemptID, Kind: store.WorkflowActionCompleted, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: actor, PayloadVersion: 2, Payload: []byte(completionPayload)})
	if _, err := tx.Exec(`INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload,event_id,start_seq,start_payload) VALUES(?,?,?,?,?,?,?)`, workID, attemptID, completionSeq, completionPayload, "dispatch-completed-"+attemptID, startSeq, startPayload); err != nil {
		t.Fatal(err)
	}
}
