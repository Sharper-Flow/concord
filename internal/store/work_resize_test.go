package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	snapshot.Relations = resizeSnapshotStrings(t, db, `SELECT kind||':'||work_id_from||'->'||work_id_to FROM relations WHERE kind IN ('supersedes','forward_link') ORDER BY 1`)
	snapshot.Lineage = resizeSnapshotStrings(t, db, `SELECT predecessor_work_id||'->'||successor_work_id||' root '||root_work_id FROM work_resize_lineage ORDER BY 1`)
	_ = db.QueryRow(`SELECT work_id FROM linear_issue_links`).Scan(&snapshot.LinearOwner)
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts`).Scan(&snapshot.Contracts); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func resizeSnapshotStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

// An override that changes the effective size supersedes the item without
// completing it: one link, one supersedes, one new pin, the old pin intact,
// the identity and the operator's size moved, and replay agrees.
func TestResizeSuccessorSupersedesWithoutCompletion(t *testing.T) {
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
	// Evidence is read by reference from the predecessor's log; the
	// operator's size carries without its approval.
	var throughSeq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT max(seq) FROM domain_events WHERE subject_id='resize' AND event_id NOT LIKE 'resize-up:resize:%'`).Scan(&throughSeq); err != nil {
		t.Fatal(err)
	}
	lineage, err := readWorkLineage(context.Background(), s.DatabaseForTesting(), successor)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lineage, WorkLineage{RootWorkID: "resize", EvidenceSources: []WorkResizeEvidenceSource{{WorkID: "resize", ThroughSeq: throughSeq}}}) {
		t.Fatalf("lineage = %+v, want evidence through seq %d", lineage, throughSeq)
	}
	var carriedApproval string
	if err := s.DatabaseForTesting().QueryRow(`SELECT coalesce(json_extract(e.payload,'$.approval_ref'),'') FROM work_size_overrides o JOIN domain_events e ON e.event_id=o.event_id WHERE o.work_id=?`, successor).Scan(&carriedApproval); err != nil || carriedApproval != "" {
		t.Fatalf("carried override approval = %q, %v; want none", carriedApproval, err)
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

func rerunWorkSize(t *testing.T, s *Store, workID, eventID string, in WorkSizeCaptureInput) (WorkSizeRevisionResult, error) {
	t.Helper()
	plan, err := s.PlanWorkSize(context.Background(), "product-bootstrap", in)
	if err != nil {
		t.Fatal(err)
	}
	var result WorkSizeRevisionResult
	err = s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		result, err = RerunWorkSizeTx(context.Background(), tx, WorkSizeRerunRequest{Plan: plan, WorkID: workID, EventID: eventID, Actor: resizeTestAgent})
		return err
	})
	return result, err
}

// A rerun sizes a pinned unsized item without a repin, keeps the item when
// the size holds, and resizes it when the declared inputs change the size.
func TestWorkSizeRerunSizesKeepsOrResizes(t *testing.T) {
	ctx := context.Background()
	s := sizedBootstrapStore(t, true)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-rerun"
	captured, err := s.BootstrapWorktree(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	work := captured.WorkID
	pin := readResizeSnapshot(t, s, work).Pins[work]
	if pin == "" {
		t.Fatal("bootstrap capture left no pin")
	}
	in := *workSizeCaptureFixture()
	first, err := rerunWorkSize(t, s, work, "rerun-first", in)
	if err != nil || first.SuccessorWorkID != "" {
		t.Fatalf("first sizing = %+v, %v; want no successor", first, err)
	}
	if got := readResizeSnapshot(t, s, work); got.Pins[work] != pin || got.Effective[work] != WorkSizeLarger || got.Lifecycle[work] != "needed" {
		t.Fatalf("first sizing: %+v", got)
	}
	again, err := rerunWorkSize(t, s, work, "rerun-same", in)
	if err != nil || again.SuccessorWorkID != "" || workSizeRecords(t, s, work) != 2 {
		t.Fatalf("unchanged rerun = %+v, %v, records %d", again, err, workSizeRecords(t, s, work))
	}
	in.EstimatedProductionLines = 500
	grown, err := rerunWorkSize(t, s, work, "rerun-grown", in)
	if err != nil || grown.SuccessorWorkID == "" {
		t.Fatalf("grown rerun = %+v, %v; want a successor", grown, err)
	}
	if got := readResizeSnapshot(t, s, work, grown.SuccessorWorkID); got.Lifecycle[work] != "superseded" || got.Effective[grown.SuccessorWorkID] != WorkSizeCrucial || got.Pins[grown.SuccessorWorkID] != pin {
		t.Fatalf("grown rerun: %+v", got)
	}
}

// seedPinnedKindWork captures a kind's work with an external reference and,
// for a bug, its defect classification, pins the kind's workflow, and sizes
// it larger.
func seedPinnedKindWork(t *testing.T, s *Store, workID, kind, ref string, intake *DefectIntake) {
	t.Helper()
	seedWork(t, s, "resize-anchor")
	created := operationEvent("create-"+workID, "work.created", SubjectWorkItem, workID, workCreatedPayload{WorkKind: kind, Title: workID, Priority: new(int64), ExternalRef: "ext-" + workID, DefectIntake: intake})
	created.PayloadVersion = 4
	memberships := operationEvent("members-"+workID, "work.memberships_replaced", SubjectWorkItem, workID, workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: "project", Role: "primary"}}, ExpectedVersion: 1, ResultingVersion: 2})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{created, memberships}}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowLaw(t, s)
	registered, err := BuiltinWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: resizeTestAgent})
	}); err != nil {
		t.Fatal(err)
	}
	inputs := largerWorkSizeInputs()
	inputs.Kind = kind
	if err := applyWorkEvent(t, s, workSizedEvent(workID+"-sized-1", workID, WorkSizeModelVersion, WorkSizeLarger, inputs), nil); err != nil {
		t.Fatal(err)
	}
}

// Bug and research work resize within their own family; the bug keeps its
// kind and classification, and its external reference stays exclusive.
func TestWorkResizeKeepsKindAndExternalReference(t *testing.T) {
	cases := []struct {
		kind, ref string
		intake    *DefectIntake
	}{
		{"bug", "workflow.break_fix", &DefectIntake{FailureShape: "resize-shape", Reproduction: "run it", Searched: "the bug list", RelatedDefectIDs: []string{}}},
		{"research", "workflow.research", nil},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			s := openTemp(t)
			work := "resize-" + tc.kind
			seedPinnedKindWork(t, s, work, tc.kind, tc.ref, tc.intake)
			result, err := overrideWorkSize(t, s, work, work+"-up", WorkSizeCrucial)
			if err != nil {
				t.Fatal(err)
			}
			successor := result.SuccessorWorkID
			var kind, intent, sourceIntent string
			if err := s.DatabaseForTesting().QueryRow(`SELECT s.kind, s.intent_json, p.intent_json FROM work_items s, work_items p WHERE s.id=? AND p.id=?`, successor, work).Scan(&kind, &intent, &sourceIntent); err != nil {
				t.Fatal(err)
			}
			if kind != tc.kind || intent != sourceIntent {
				t.Fatalf("successor kind %s intent %s, want %s %s", kind, intent, tc.kind, sourceIntent)
			}
			got := readResizeSnapshot(t, s, work, successor)
			if got.Pins[successor] != got.Pins[work] || got.Lifecycle[work] != "superseded" {
				t.Fatalf("resize: %+v", got)
			}
			var owners int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE json_extract(intent_json,'$.external_ref')=? AND terminal_time IS NULL`, "ext-"+work).Scan(&owners); err != nil || owners != 1 {
				t.Fatalf("live external-reference owners = %d, %v", owners, err)
			}
			rival := operationEvent("create-rival", "work.created", SubjectWorkItem, "rival", workCreatedPayload{WorkKind: "task", Title: "rival", Priority: new(int64), ExternalRef: "ext-" + work})
			rival.PayloadVersion = 4
			assertFailureKind(t, applyWorkEvent(t, s, rival, nil), KindProjectionConflict)
		})
	}
}

// Only the resize core creates a resize successor.
func TestResizeOriginCreationRefusesOutsideTheResizeCore(t *testing.T) {
	s := openTemp(t)
	pinnedLargerWork(t, s, "forged")
	forged := operationEvent("forge-successor", "work.created", SubjectWorkItem, "forged-successor", workCreatedPayload{WorkKind: "task", Title: "forged", Priority: new(int64), ResizeOf: "forged"})
	forged.PayloadVersion = 4
	assertFailureKind(t, applyWorkEvent(t, s, forged, nil), KindInvalidOperation)
}

// Removal refuses both members of a resize: the predecessor is superseded
// and the successor holds the supersedes relation.
func TestWorkRemovalRefusesResizeLineageMembers(t *testing.T) {
	s := openTemp(t)
	pinnedLargerWork(t, s, "kept")
	result, err := overrideWorkSize(t, s, "kept", "kept-up", WorkSizeCrucial)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kept", result.SuccessorWorkID} {
		err := validateRemovalGatesQ(context.Background(), s.DatabaseForTesting(), WorkRemovalRequest{WorkID: id, ExpectedVersion: readWorkVersion(t, s, id)})
		if err == nil {
			t.Fatalf("removal of lineage member %s was admitted", id)
		}
	}
}
