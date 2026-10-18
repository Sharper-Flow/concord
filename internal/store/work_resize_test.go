package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
)

var resizeTestAgent = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/test", SessionRef: "session/test", ActorClass: ActorAgent}

// pinnedLargerWork seeds a pinned task the model sizes larger, with a Linear
// issue identity.
func pinnedLargerWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	seedOutsideRepairTestWork(t, s, workID)
	if err := applyWorkEvent(t, s, workSizedEvent(workID+"-sized-1", workID, WorkSizeModelVersion, WorkSizeLarger, largerWorkSizeInputs()), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := RecordLinearIssueLinkTx(context.Background(), tx, LinearIssueLink{WorkID: workID, RemoteIssueUUID: "uuid-" + workID, HumanKey: "CON-1", URL: "https://linear.app/example/issue/CON-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func overrideWorkSize(t *testing.T, s *Store, workID, eventID string, size WorkSize) (WorkSizeRevisionResult, error) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	request := workSizeOverrideRequest(t, s, workID, eventID, size)
	sum := sha256.Sum256([]byte(eventID))
	request.ApprovalRef = hex.EncodeToString(sum[:])
	insertConsumedApprovalState(t, s, request.ApprovalRef, workID, request.ExpectedVersion, 1)
	var result WorkSizeRevisionResult
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		result, err = OverrideWorkSizeTx(context.Background(), tx, request)
		return err
	})
	return result, err
}

type resizeSnapshot struct {
	Lifecycle   map[string]string
	Relations   []string
	Pins        map[string]string
	Lineage     []string
	Effective   map[string]WorkSize
	LinearOwner string
	Contracts   int
}

func readResizeSnapshot(t *testing.T, s *Store, ids ...string) resizeSnapshot {
	t.Helper()
	db := s.DatabaseForTesting()
	snapshot := resizeSnapshot{Lifecycle: map[string]string{}, Pins: map[string]string{}, Effective: map[string]WorkSize{}}
	for _, id := range ids {
		var lifecycle string
		if err := db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		snapshot.Lifecycle[id] = lifecycle
		var pin string
		_ = db.QueryRow(`SELECT definition_ref||'@'||definition_version||'@'||definition_digest FROM workflow_instances WHERE work_id=?`, id).Scan(&pin)
		snapshot.Pins[id] = pin
		if decision, sized, err := readEffectiveWorkSize(context.Background(), db, id); err != nil {
			t.Fatal(err)
		} else if sized {
			snapshot.Effective[id] = decision.Effective
		}
	}
	rows, err := db.Query(`SELECT kind||':'||work_id_from||'->'||work_id_to FROM relations WHERE kind IN ('supersedes','forward_link') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var relation string
		_ = rows.Scan(&relation)
		snapshot.Relations = append(snapshot.Relations, relation)
	}
	rows.Close()
	rows, err = db.Query(`SELECT predecessor_work_id||'->'||successor_work_id||' root '||root_work_id FROM work_resize_lineage ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var lineage string
		_ = rows.Scan(&lineage)
		snapshot.Lineage = append(snapshot.Lineage, lineage)
	}
	rows.Close()
	_ = db.QueryRow(`SELECT work_id FROM linear_issue_links`).Scan(&snapshot.LinearOwner)
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts`).Scan(&snapshot.Contracts); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// An override that changes the effective size supersedes the item without
// completing it: one link, one supersedes, one new pin, the old pin intact,
// the identity and the operator's size moved, and replay agrees.
func TestWorkSizeOverrideResizesTheWorkToASuccessor(t *testing.T) {
	s := openTemp(t)
	pinnedLargerWork(t, s, "resize")
	before := readResizeSnapshot(t, s, "resize")
	result, err := overrideWorkSize(t, s, "resize", "resize-up", WorkSizeCrucial)
	if err != nil {
		t.Fatal(err)
	}
	successor := result.SuccessorWorkID
	if successor == "" {
		t.Fatal("a changed effective size left no successor")
	}
	after := readResizeSnapshot(t, s, "resize", successor)
	want := resizeSnapshot{
		Lifecycle:   map[string]string{"resize": "superseded", successor: "needed"},
		Relations:   []string{"forward_link:resize->" + successor, "supersedes:" + successor + "->resize"},
		Pins:        map[string]string{"resize": before.Pins["resize"], successor: before.Pins["resize"]},
		Lineage:     []string{"resize->" + successor + " root resize"},
		Effective:   map[string]WorkSize{"resize": WorkSizeCrucial, successor: WorkSizeCrucial},
		LinearOwner: successor,
		Contracts:   before.Contracts,
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("after resize:\n got %+v\nwant %+v", after, want)
	}
	var kind, intent, sourceIntent string
	if err := s.DatabaseForTesting().QueryRow(`SELECT s.kind, s.intent_json, p.intent_json FROM work_items s, work_items p WHERE s.id=? AND p.id='resize'`, successor).Scan(&kind, &intent, &sourceIntent); err != nil {
		t.Fatal(err)
	}
	if kind != "task" || intent != sourceIntent {
		t.Fatalf("successor kind %s intent %s, want the predecessor's %s", kind, intent, sourceIntent)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if replayed := readResizeSnapshot(t, s, "resize", successor); !reflect.DeepEqual(replayed, after) {
		t.Fatalf("replay drift:\n got %+v\nwant %+v", replayed, after)
	}
}

// An override that keeps the effective size keeps the item and its pin.
func TestWorkSizeOverrideThatKeepsTheSizeKeepsTheItem(t *testing.T) {
	s := openTemp(t)
	pinnedLargerWork(t, s, "steady")
	result, err := overrideWorkSize(t, s, "steady", "steady-same", WorkSizeLarger)
	if err != nil || result.SuccessorWorkID != "" {
		t.Fatalf("result %+v, err %v; want no successor", result, err)
	}
	if got := readResizeSnapshot(t, s, "steady"); got.Lifecycle["steady"] != "needed" || len(got.Lineage) != 0 || got.LinearOwner != "steady" {
		t.Fatalf("an unchanged size moved the work: %+v", got)
	}
}

// A resize refuses with no effect on an open attempt or an existing
// successor; a resized item and its successor are never reopened.
func TestWorkResizeRefusesAtomically(t *testing.T) {
	t.Run("open attempt", func(t *testing.T) {
		s := openTemp(t)
		pinnedLargerWork(t, s, "busy")
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('busy','attempt-open','implement',1,'sha256:'||hex(zeroblob(32)),'implementation','','1.0','1.0','dispatched','2026-01-01T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
		before := readResizeSnapshot(t, s, "busy")
		_, err := overrideWorkSize(t, s, "busy", "busy-up", WorkSizeCrucial)
		assertFailureKind(t, err, KindResourceBusy)
		if after := readResizeSnapshot(t, s, "busy"); !reflect.DeepEqual(after, before) {
			t.Fatalf("a refused resize changed state:\n got %+v\nwant %+v", after, before)
		}
		assertNoWorkSizeOverride(t, s, "busy")
	})
	t.Run("second resize and reopen", func(t *testing.T) {
		s := openTemp(t)
		pinnedLargerWork(t, s, "twice")
		result, err := overrideWorkSize(t, s, "twice", "twice-up", WorkSizeCrucial)
		if err != nil {
			t.Fatal(err)
		}
		_, err = overrideWorkSize(t, s, "twice", "twice-down", WorkSizeSmall)
		if err == nil {
			t.Fatal("a superseded item was resized again")
		}
		for _, id := range []string{"twice", result.SuccessorWorkID} {
			err := s.Transact(context.Background(), func(tx *Transaction) error {
				return refuseResizeLineageReopenTx(context.Background(), tx.tx, id)
			})
			assertFailureKind(t, err, KindInvalidTransition)
		}
		down, err := overrideWorkSize(t, s, result.SuccessorWorkID, "successor-down", WorkSizeSmall)
		if err != nil {
			t.Fatal(err)
		}
		if got := readResizeSnapshot(t, s, result.SuccessorWorkID, down.SuccessorWorkID); got.Lineage[1] != result.SuccessorWorkID+"->"+down.SuccessorWorkID+" root twice" || got.LinearOwner != down.SuccessorWorkID {
			t.Fatalf("second resize lineage %+v", got)
		}
	})
}
