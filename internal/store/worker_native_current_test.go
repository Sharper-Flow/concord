package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestWorkerNativeCurrentAuthorizationWithoutLog(t *testing.T) {
	f := newNativeOracleFixture(t)
	ctx := context.Background()
	req := f.request("current-authorization", &f.a)
	wantBundle, wantVersion, wantAuth, err := readNativeOracleDispatchTx(ctx, f.s.db, req, f.head, workerWorktreeIdentity(f.repo))
	if err != nil || wantVersion != 1 || wantAuth.StartSeq <= 0 || wantAuth.StartSeq >= wantAuth.Seq {
		t.Fatalf("original authorization: version=%d auth=%+v err=%v", wantVersion, wantAuth, err)
	}
	// The fixture opens b after a at the same step. Its newer start must not
	// replace the original start captured by a's authorization writer.
	_, newer, err := readNativeOracleDispatchBindingTx(ctx, f.s.db, f.work, f.b.AttemptID)
	if err != nil || newer.Seq <= wantAuth.Seq {
		t.Fatalf("newer authorization missing: %+v %v", newer, err)
	}
	assertEventLogAbsent(t, f.s)
	t.Run("dispatch-binding", func(t *testing.T) {
		d, auth, err := readNativeOracleDispatchBindingTx(ctx, f.s.db, f.work, f.a.AttemptID)
		if err != nil || auth.EventID != wantAuth.EventID || auth.Seq != wantAuth.Seq || d.WorkerAttemptID != f.a.AttemptID || d.AttemptEpoch != f.a.AttemptEpoch {
			t.Fatalf("dispatch binding changed: auth=%+v err=%v", auth, err)
		}
	})
	t.Run("dispatch-bundle-original-start", func(t *testing.T) {
		bundle, version, auth, err := readNativeOracleDispatchTx(ctx, f.s.db, req, f.head, workerWorktreeIdentity(f.repo))
		if err != nil || version != wantVersion || auth != wantAuth || !reflect.DeepEqual(bundle, wantBundle) {
			t.Fatalf("authorization changed: version=%d auth=%+v err=%v", version, auth, err)
		}
	})
	t.Run("window-single-use", func(t *testing.T) {
		tx, err := f.s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		window, err := findAuthorizedDispatchWindowBefore(ctx, tx, f.work, f.a.AttemptID, 0)
		if err != nil || window.StartSeq != wantAuth.StartSeq || window.AttemptEpoch != f.a.AttemptEpoch || window.PacketDigest != f.a.WorkerPacketDigest || window.SubjectCommit != f.head || window.WorktreeIdentity != workerWorktreeIdentity(f.repo) {
			t.Fatalf("wrong original window: %+v %v", window, err)
		}
		open, err := WorkerDispatchWindowIsOpenTx(ctx, tx, window)
		if err != nil || !open {
			t.Fatalf("unused authorization: open=%v err=%v", open, err)
		}
		// Exercise the current consumption coordinate at its strict boundary.
		if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worker_attempts SET dispatch_seq=? WHERE work_id=? AND attempt_id=?`, window.StartSeq, f.work, f.a.AttemptID); err != nil {
			t.Fatal(err)
		}
		open, err = WorkerDispatchWindowIsOpenTx(ctx, tx, window)
		if err != nil || !open {
			t.Fatalf("equal coordinate consumed authorization: open=%v err=%v", open, err)
		}
		if _, err := tx.Exec(`UPDATE worker_attempts SET dispatch_seq=? WHERE work_id=? AND attempt_id=?`, window.StartSeq+1, f.work, f.a.AttemptID); err != nil {
			t.Fatal(err)
		}
		open, err = WorkerDispatchWindowIsOpenTx(ctx, tx, window)
		if err != nil || open {
			t.Fatalf("spent authorization reopened: open=%v err=%v", open, err)
		}
	})
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"wrong-start-epoch", `UPDATE worker_authorizations SET start_payload=json_set(start_payload,'$.attempt_epoch',999) WHERE attempt_id='attempt-a'`},
		{"wrong-start-step", `UPDATE worker_authorizations SET start_payload=json_set(start_payload,'$.step_id','other') WHERE attempt_id='attempt-a'`},
		{"job-at-authorization", `UPDATE worker_job_revisions SET recorded_seq=(SELECT seq FROM worker_authorizations WHERE attempt_id='attempt-a') WHERE job_id='job:a'`},
		{"job-after-authorization", `UPDATE worker_job_revisions SET recorded_seq=(SELECT seq+1 FROM worker_authorizations WHERE attempt_id='attempt-a') WHERE job_id='job:a'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1); ` + tc.sql); err != nil {
				t.Fatal(err)
			}
			_, _, _, err = readNativeOracleDispatchTx(ctx, tx, req, f.head, workerWorktreeIdentity(f.repo))
			if failureKind(err) != KindUnauthorizedDispatch {
				t.Fatalf("invalid join admitted or wrong refusal: %v", err)
			}
		})
	}
}

func TestWorkerNativeCurrentReceiptWithoutLog(t *testing.T) {
	f := newNativeOracleFixture(t)
	ctx := context.Background()
	r, err := f.s.VerifyWorktree(ctx, f.request("current-receipt", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("execute receipt: %+v %v", r, err)
	}
	p, _, err := readNativeOracleExecutePlanTx(ctx, f.s.db, f.work, &r)
	if err != nil {
		t.Fatal(err)
	}
	d, err := readNativeOracleAuthorizingDispatchTx(ctx, f.s.db, f.work, p)
	if err != nil || d.WorkerAttemptID != f.a.AttemptID {
		t.Fatalf("exact authorizing dispatch: %+v %v", d, err)
	}
	assertEventLogAbsent(t, f.s)
	t.Run("complete-producer", func(t *testing.T) {
		if err := validateNativeOracleProducerTx(ctx, f.s.db, f.work, &r, "pass"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("authorizing-dispatch", func(t *testing.T) {
		got, err := readNativeOracleAuthorizingDispatchTx(ctx, f.s.db, f.work, p)
		if err != nil || !reflect.DeepEqual(got, d) {
			t.Fatalf("authorizing dispatch changed: %v", err)
		}
	})
	t.Run("history-original-start", func(t *testing.T) {
		if err := validateNativeOracleHistoryTx(ctx, f.s.db, f.work, p, d, r.Oracle); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("exact-event-identity", func(t *testing.T) {
		wrong := p
		wrong.Authorization.EventID = "not-the-authorization"
		if _, err := readNativeOracleAuthorizingDispatchTx(ctx, f.s.db, f.work, wrong); !errors.Is(err, errOracleReceiptRefused) {
			t.Fatalf("foreign event accepted: %v", err)
		}
	})
	t.Run("exact-start-identity", func(t *testing.T) {
		wrong := p
		wrong.Authorization.StartSeq++
		if err := validateNativeOracleHistoryTx(ctx, f.s.db, f.work, wrong, d, r.Oracle); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("foreign start accepted: %v", err)
		}
	})
	t.Run("job-must-precede-authorization", func(t *testing.T) {
		tx, err := f.s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worker_job_revisions SET recorded_seq=? WHERE work_id=? AND job_id=?`, p.Authorization.Seq, f.work, f.a.WorkerJobBinding.JobID); err != nil {
			t.Fatal(err)
		}
		if err := validateNativeOracleHistoryTx(ctx, tx, f.work, p, d, r.Oracle); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("later job accepted: %v", err)
		}
	})
}

func TestWorkerNativeCurrentReleaseStampWithoutLog(t *testing.T) {
	f := newNativeOracleFixture(t)
	ctx := context.Background()
	req := f.request("current-stamp", &f.a)
	want, err := readNativeOracleReleaseStamp(ctx, f.s.db, req)
	if err != nil || want.Frontier <= 0 || want.CoreReceipt == "" || want.Attempt == "" || want.Preparations == "[]" {
		t.Fatalf("incomplete release stamp: %+v %v", want, err)
	}
	assertEventLogAbsent(t, f.s)
	got, err := readNativeOracleReleaseStamp(ctx, f.s.db, req)
	if err != nil || got != want {
		t.Fatalf("stamp changed without log: %+v %v", got, err)
	}
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"frontier", `UPDATE work_items SET effect_seq=effect_seq+1`},
		{"core-receipt", `UPDATE durable_operations SET result_payload=json_set(result_payload,'$.output','changed') WHERE workflow_type_ref='worktree.verify'`},
		{"attempt", `UPDATE worker_attempts SET lifecycle_state='dispatched' WHERE attempt_id='attempt-a'`},
		{"preparation-metadata", `UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.oracle.detail','changed') WHERE json_extract(result_json,'$.oracle.phase')='prepare'`},
		{"preparation-plan", `UPDATE worktree_verify_leases SET native_plan_sha256='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE json_extract(result_json,'$.oracle.phase')='prepare'`},
		{"preparation-stream-length", `UPDATE worktree_verify_leases SET stdout_blob=CAST('changed' AS BLOB) WHERE json_extract(result_json,'$.oracle.phase')='prepare'`},
		{"unreferenced-lease", `UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.output','changed') WHERE json_extract(result_json,'$.oracle.phase') IS NULL`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1); ` + tc.sql); err != nil {
				t.Fatal(err)
			}
			stamp, err := readNativeOracleReleaseStamp(ctx, tx, req)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "unreferenced-lease" {
				if stamp.Preparations != want.Preparations {
					t.Fatal("unreferenced lease entered preparation descriptor")
				}
			} else if stamp == want {
				t.Fatalf("dependency change escaped stamp: %+v", stamp)
			}
		})
	}
}

func TestWorkerNativeCurrentRevisionReadersWithoutLog(t *testing.T) {
	f := newNativeOracleFixture(t)
	ctx := context.Background()
	assertEventLogAbsent(t, f.s)
	t.Run("U09-exact-recorded-existence", func(t *testing.T) {
		for _, tc := range []struct {
			work string
			job  WorkerJobBinding
			want bool
		}{
			{f.work, *f.a.WorkerJobBinding, true},
			{"other-work", *f.a.WorkerJobBinding, false},
			{f.work, WorkerJobBinding{JobID: f.a.WorkerJobBinding.JobID, Revision: 2, Digest: f.a.WorkerJobBinding.Digest}, false},
			{f.work, WorkerJobBinding{JobID: f.a.WorkerJobBinding.JobID, Revision: 1, Digest: f.b.WorkerJobBinding.Digest}, false},
		} {
			var got bool
			if err := f.s.db.QueryRowContext(ctx, workerFailureIntegrationJobRecordedQuery, tc.work, tc.job.JobID, tc.job.Revision, tc.job.Digest).Scan(&got); err != nil || got != tc.want {
				t.Fatalf("recorded %s/%+v=%v want %v: %v", tc.work, tc.job, got, tc.want, err)
			}
		}
	})
	t.Run("U10-exact-predicates", func(t *testing.T) {
		tx, err := f.s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ids, err := workerJobRevisionPredicateIDsTx(ctx, tx, f.work, *f.a.WorkerJobBinding)
		if err != nil || len(ids) == 0 || !reflect.DeepEqual(ids, f.bundle.Control.PredicateIDs) {
			t.Fatalf("exact predicates: %v %v", ids, err)
		}
		bad := *f.a.WorkerJobBinding
		bad.Digest = f.b.WorkerJobBinding.Digest
		if _, err := workerJobRevisionPredicateIDsTx(ctx, tx, f.work, bad); failureKind(err) != KindInvariantViolation {
			t.Fatalf("foreign digest widened predicates: %v", err)
		}
	})
	t.Run("U11-attempt-capability", func(t *testing.T) {
		var state WorkflowAdmissionState
		if err := (workflowAdmissionLoad{ctx: ctx, q: f.s.db, workID: f.work}).attemptAndReview(&state); err != nil {
			t.Fatal(err)
		}
		var lifecycle, capability string
		if err := f.s.db.QueryRow(`SELECT lifecycle_state,capability_class FROM worker_attempts WHERE work_id=? ORDER BY dispatched_at DESC,attempt_id DESC LIMIT 1`, f.work).Scan(&lifecycle, &capability); err != nil {
			t.Fatal(err)
		}
		if !state.PendingWorkerAuthorization || state.AttemptState != lifecycle || state.AttemptCapabilityClass != capability || capability == "" {
			raw, _ := json.Marshal(state)
			t.Fatalf("attempt facts lost: %s want %s/%s", raw, lifecycle, capability)
		}
	})
}
