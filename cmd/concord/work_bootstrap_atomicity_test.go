package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func seedBootstrapRetainedCapture(t *testing.T, s *store.Store) {
	t.Helper()
	apply := func(kind, id, eventID string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyOperation(t.Context(), s, store.Operation{Events: []store.Event{{
			EventID: eventID, Kind: kind, SubjectType: store.SubjectWorkItem, SubjectID: id,
			Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: store.CurrentEventPayloadVersion(kind), Payload: raw,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"capture-owner", "capture-retired", "capture-target"} {
		externalRef := ""
		if id == "capture-owner" {
			externalRef = "tracker:retained"
		}
		created, err := json.Marshal(map[string]any{"work_kind": "task", "title": id, "priority": 1, "external_ref": externalRef})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyOperation(t.Context(), s, store.Operation{Events: []store.Event{
			{EventID: id + ":created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: created},
			{EventID: id + ":memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-wl","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, id): 0}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, edge := range []struct {
		kind, from, eventID string
		expected            int64
	}{
		{"relation.added", "capture-retired", "capture-edge-removed", 2},
		{"relation.removed", "capture-retired", "capture-remove-edge", 3},
		{"relation.added", "capture-target", "capture-edge-retained", 2},
	} {
		apply(edge.kind, edge.from, edge.eventID, map[string]any{
			"from": edge.from, "to": "capture-owner", "kind": "implements", "reason": "fixture",
			"expected_version": edge.expected, "resulting_version": edge.expected + 1,
		})
	}
	var count, position int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*),max(id) FROM relations`).Scan(&count, &position); err != nil || count != 1 || position != 2 {
		t.Fatalf("retained relations: count=%d position=%d err=%v", count, position, err)
	}
}

func bootstrapCaptureSnapshot(t *testing.T, s *store.Store) [11]int64 {
	t.Helper()
	var snapshot [11]int64
	for i, table := range []string{"work_items", "relations", "work_projects", "effect_receipts", "bootstrap_operations", "worktree_claims", "worktree_entries", "workflow_instances", "idempotency_records"} {
		if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&snapshot[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i, name := range []string{"effect", "relation"} {
		if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name=?`, name).Scan(&snapshot[9+i]); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func TestWorkBootstrapRetainedRelationCaptureAtomicity(t *testing.T) {
	for _, mode := range []string{"acknowledged", "unacknowledged", "claim_failure"} {
		t.Run(mode, func(t *testing.T) {
			repo := initLocatorRepo(t)
			s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
			seedLocatorAuthority(t, s, repo)
			seedBootstrapRetainedCapture(t, s)
			t.Chdir(repo)
			input := workBootstrapInput{
				ProductID: "product-wl", ProjectID: "project-wl", Title: "Acknowledged capture",
				ValueStatement: "Retained relations do not prevent distinct work", Kind: "task", Task: "run the task",
				IdempotencyKey: "capture-retained-key", ExternalRef: "tracker:retained", RaisedFromWorkID: "capture-owner",
			}
			req := bootstrapRequest()
			req.Title, req.ValueStatement, req.Priority = input.Title, input.ValueStatement, input.Priority
			req.IdempotencyKey, req.ExternalRef, req.RaisedFromWorkID = input.IdempotencyKey, input.ExternalRef, input.RaisedFromWorkID
			operationID, workID, _, err := store.CanonicalBootstrapIdentity(req)
			if err != nil {
				t.Fatal(err)
			}
			location, err := s.LocateWorktree(t.Context(), input.ProjectID, workID, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			refusedPath := location.Path
			before := bootstrapCaptureSnapshot(t, s)
			branches := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
			trees := gitOutput(t, repo, "worktree", "list", "--porcelain")
			if mode == "claim_failure" {
				// The claim is written after work, raised_from, workflow and journal.
				// Refusing it must roll the entire prepare transaction back.
				if _, err := s.DatabaseForTesting().Exec(`CREATE TEMP TRIGGER capture_atomicity_fail_claim AFTER INSERT ON worktree_claims BEGIN SELECT RAISE(ABORT,'injected capture claim failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unacknowledged" {
				input.RaisedFromWorkID = ""
				refusedRequest := req
				refusedRequest.RaisedFromWorkID = ""
				_, refusedWorkID, _, err := store.CanonicalBootstrapIdentity(refusedRequest)
				if err != nil {
					t.Fatal(err)
				}
				refusedLocation, err := s.LocateWorktree(t.Context(), input.ProjectID, refusedWorkID, "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				refusedPath = refusedLocation.Path
			}
			var out, errOut bytes.Buffer
			run := func() int {
				t.Helper()
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				out.Reset()
				errOut.Reset()
				return runWorkBootstrap(raw, s, &out, &errOut)
			}
			if mode != "acknowledged" {
				code := run()
				var refused workBootstrapFailureOutput
				if code == 0 || json.Unmarshal(out.Bytes(), &refused) != nil || refused.EffectState != "none" || refused.WorkID != "" {
					t.Fatalf("refused capture code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
				}
				if mode == "unacknowledged" && (code != workBootstrapRefusalExit || !strings.Contains(errOut.String(), "external reference already belongs to live work item capture-owner")) {
					t.Fatalf("collision did not name its live owner: code=%d stderr=%q", code, errOut.String())
				}
				if mode == "claim_failure" && !strings.Contains(errOut.String(), "injected capture claim failure") {
					t.Fatalf("capture did not reach the claim failure: code=%d stderr=%q", code, errOut.String())
				}
				if after := bootstrapCaptureSnapshot(t, s); after != before {
					t.Fatalf("failed capture left partial work/relation/receipt/workflow/journal/claim state or allocation: %v -> %v", before, after)
				}
				if _, err := os.Stat(refusedPath); !os.IsNotExist(err) {
					t.Fatalf("failed capture left native worktree: %v", err)
				}
				if after := gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); after != branches {
					t.Fatalf("failed capture changed branches: %q -> %q", branches, after)
				}
				if after := gitOutput(t, repo, "worktree", "list", "--porcelain"); after != trees {
					t.Fatalf("failed capture changed native worktree registrations: %q -> %q", trees, after)
				}
				if mode == "claim_failure" {
					if _, err := s.DatabaseForTesting().Exec(`DROP TRIGGER capture_atomicity_fail_claim`); err != nil {
						t.Fatal(err)
					}
				}
				input.RaisedFromWorkID = "capture-owner"
			}
			if code := run(); code != 0 {
				t.Fatalf("valid acknowledged capture code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			var captured workBootstrapOutput
			if err := json.Unmarshal(out.Bytes(), &captured); err != nil || captured.SchemaVersion != "1.0" || captured.WorkID != workID || captured.WorkID == "capture-owner" || captured.OperationID != operationID || captured.Replayed || captured.Worktree.State != "active" || captured.Worktree.Path != location.Path {
				t.Fatalf("distinct capture output=%+v err=%v", captured, err)
			}
			for _, check := range []struct {
				query string
				args  []any
				want  int
			}{
				{`SELECT count(*) FROM work_items WHERE id IN ('capture-owner',?)
 AND json_extract(intent_json,'$.external_ref')='tracker:retained' AND terminal_time IS NULL`, []any{workID}, 2},
				{`SELECT count(*) FROM relations r JOIN effect_receipts e ON e.relation_seq=r.id
 WHERE r.id=3 AND r.work_id_from=? AND r.work_id_to='capture-owner' AND r.kind='raised_from'
 AND r.record_version=1 AND e.kind='work.created' AND e.event_id=?`, []any{workID, operationID + ":work-created"}, 1},
				{`SELECT count(*) FROM relations WHERE id=2 AND work_id_from='capture-target'
 AND work_id_to='capture-owner' AND kind='implements'`, nil, 1},
				{`SELECT version FROM work_items WHERE id='capture-owner'`, nil, 2},
				{`SELECT last FROM store_sequences WHERE name='relation'`, nil, 3},
				{`SELECT count(*) FROM workflow_instances WHERE work_id=?`, []any{workID}, 1},
				{`SELECT count(*) FROM work_projects WHERE work_id=? AND project_id='project-wl' AND role='primary'`, []any{workID}, 1},
			} {
				var count int
				if err := s.DatabaseForTesting().QueryRow(check.query, check.args...).Scan(&count); err != nil || count != check.want {
					t.Fatalf("capture assertion %s = %d, want %d; err=%v", check.query, count, check.want, err)
				}
			}
			var journal, claim string
			if err := s.DatabaseForTesting().QueryRow(`SELECT b.state,c.state FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.operation_id=? AND b.work_id=?`, operationID, workID).Scan(&journal, &claim); err != nil || journal != "completed" || claim != "verified" {
				t.Fatalf("capture journal=%q claim=%q err=%v", journal, claim, err)
			}
			if _, err := os.Stat(filepath.Join(location.Path, ".git")); err != nil {
				t.Fatalf("successful capture has no native worktree: %v", err)
			}
			committed := bootstrapCaptureSnapshot(t, s)
			for _, index := range []int{0, 1, 2, 4, 5, 6, 7} {
				if committed[index] != before[index]+1 {
					t.Fatalf("capture must add exactly one work/relation/membership/journal/claim/entry/workflow: %v -> %v", before, committed)
				}
			}
			if code := run(); code != 0 || json.Unmarshal(out.Bytes(), &captured) != nil || !captured.Replayed || captured.WorkID != workID {
				t.Fatalf("exact capture replay code=%d output=%+v stderr=%q", code, captured, errOut.String())
			}
			if replayed := bootstrapCaptureSnapshot(t, s); replayed != committed {
				t.Fatalf("replay created additional state: %v -> %v", committed, replayed)
			}
		})
	}
}
