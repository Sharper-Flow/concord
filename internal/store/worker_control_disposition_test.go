package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeControlInputRefusalsRetainReleasedDisposition(t *testing.T) {
	for _, beforeNext := range []bool{true, false} {
		name := "final-inventory"
		if beforeNext {
			name = "before-next-test"
		}
		t.Run(name, func(t *testing.T) {
			f := newNativeTargetUVCheckFixture(t)
			script := filepath.Join(f.repo, f.root, "scripts", "args.sh")
			mutation := "\nmkdir -p data\nprintf 'event\\n' > data/events.jsonl\n"
			want := "consumed candidate inputs changed during execution"
			stages := 3 // metadata and both checks
			if beforeNext {
				script = filepath.Join(f.repo, f.root, "scripts", "check_deps.py")
				mutation = "pathlib.Path('data').mkdir(exist_ok=True)\npathlib.Path('data/events.jsonl').write_text('event\\n')\n"
				want = "candidate inputs changed before test launch"
				stages = 2 // metadata and the first check only
			}
			body, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			if beforeNext {
				body = []byte(strings.Replace(string(body), "sys.exit(", mutation+"sys.exit(", 1))
			} else {
				body = append(body, mutation...)
			}
			if err := os.WriteFile(script, body, 0600); err != nil {
				t.Fatal(err)
			}
			f.commitAndPrepareRepin(t, name)
			o := f.authorize(t, name, f.actor)
			req := f.request(name+"-execute", &o)
			r, err := f.s.VerifyWorktree(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), want+": "+f.root+"/data/events.jsonl") || r.Oracle == nil || r.Oracle.Qualification != "unavailable" || len(r.Oracle.Stages) != stages {
				t.Fatalf("lost input refusal or producer stages: %+v, %v", r, err)
			}
			if r.LeaseDisposition == nil || *r.LeaseDisposition != (WorktreeVerifyLeaseDisposition{State: "released", Outcome: "completed"}) {
				t.Fatalf("lost released disposition: %+v", r.LeaseDisposition)
			}
			if r.OperationRef != worktreeVerifyOperationRef(req.LeaseID) || r.Oracle.RunRef != r.OperationRef || !r.Oracle.StreamsComplete || r.Oracle.Stdout.Ref == "" || r.Oracle.Stderr.Ref == "" {
				t.Fatalf("lost retained evidence locators: %+v", r)
			}
			var state, outcome string
			var green int
			if err := f.s.db.QueryRow(`SELECT state,outcome FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&state, &outcome); err != nil || state != r.LeaseDisposition.State || outcome != r.LeaseDisposition.Outcome {
				t.Fatalf("disposition differs from producer row: %s/%s %v", state, outcome, err)
			}
			if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil || green != 0 {
				t.Fatalf("unavailable control published authority: %d %v", green, err)
			}
			launches := f.launches
			replay, err := f.s.VerifyWorktree(context.Background(), req)
			if err != nil || replay.LeaseDisposition == nil || *replay.LeaseDisposition != *r.LeaseDisposition || !oracleEntryIdentical(r, replay) || f.launches != launches {
				t.Fatalf("retained replay changed or launched again: %+v %v", replay, err)
			}
		})
	}
}

func TestNativeControlCancellationReportsAbortedDisposition(t *testing.T) {
	f := newNativeOracleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.s.nativeOracleProgramLaunched = cancel
	r, err := f.s.VerifyWorktree(ctx, f.request("cancelled-execute", &f.a))
	if err == nil || r.LeaseDisposition == nil || *r.LeaseDisposition != (WorktreeVerifyLeaseDisposition{State: "released", Outcome: "aborted"}) || r.Oracle == nil || r.Oracle.Qualification != "unavailable" {
		t.Fatalf("cancelled producer lost aborted disposition: %+v %v", r, err)
	}
	var state, outcome string
	if err := f.s.db.QueryRow(`SELECT state,outcome FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&state, &outcome); err != nil || state != "released" || outcome != "aborted" {
		t.Fatalf("cancellation disposition differs from row: %s/%s %v", state, outcome, err)
	}
}
