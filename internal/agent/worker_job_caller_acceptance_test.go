package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// TestWorkerJobCallerRepairAcceptanceThroughAgent proves the acceptance
// containment (CD-0227 D2/D3, CD-0242 D6) through the agent boundary over a
// real Git delivery: the core observes the changed paths between the
// dispatched subject and the delivered commit, and every path outside the
// job's path scope needs an evidenced repair under the recorded allowance
// that no other work item owns.
// The lookup change exceeds the retired fixture's 400-line production cap;
// CD-0242 D6 keeps this coherent assignment intact without a split ritual.
func TestWorkerJobCallerRepairAcceptanceThroughAgent(t *testing.T) {
	for _, scenario := range []string{"contained", "missing repair", "wrong owner", "foreign owner"} {
		t.Run(scenario, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
			// Caller repair needs the allowance member absent from legacy
			// consent pins.
			live, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
			if err != nil {
				t.Fatal(err)
			}
			grant.Worktree = seedWorkerRetryMutationFixtureAt(t, s, grant, live)
			write := func(path, contents string) {
				t.Helper()
				path = filepath.Join(grant.Worktree, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", "module example.invalid/caller\n\ngo 1.26.0\ntoolchain "+goruntime.Version()+"\n")
			write("owner/value.go", "package owner\nfunc Value() int { return 7 }\n")
			write("caller/use.go", "package caller\nimport \"example.invalid/caller/owner\"\nfunc Use() int { return owner.Value() }\n")
			run := func(program string, args ...string) (string, error) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, program, args...)
				cmd.Dir = grant.Worktree
				cmd.Env = append(os.Environ(), "GOMAXPROCS=4", "GOWORK=off")
				out, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("%s %v timed out: %s", program, args, out)
				}
				return strings.TrimSpace(string(out)), err
			}
			mustGit := func(args ...string) string {
				t.Helper()
				out, err := run("git", args...)
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return out
			}
			baseline, err := run("go", "test", "./...")
			if err != nil {
				t.Fatalf("baseline compile check: %v\n%s", err, baseline)
			}
			fixtureClaimSubject(t, grant.Worktree)
			mustGit("branch", "-m", "retry")
			scope, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scope)
			invoke := func(action, key string, fields map[string]any, caller CallEnvelope) Envelope {
				t.Helper()
				caller.RequestID = "request:" + key
				return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
					"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": action, "idempotency_key": key, "fields": fields,
				})}, caller)
			}
			if out := invoke("start_execution", "caller-start", map[string]any{}, env); out.Outcome != OutcomeOK {
				t.Fatalf("start_execution: %+v", out.Error)
			}
			// The oracle fixture prepares its real control on this baseline.
			oracle := agentNativeOracle(t, s, service, env)
			fields := map[string]any{"job_id": "job:caller", "objective": "Change Value and repair its direct caller", "stopping_condition": "the combined compile check passes",
				"ready": true, "readiness_evidence": []string{"contract:work-1:1"}, "checks": []string{"go test ./..."}, "acceptance_oracle": oracle}
			addAgentJobCallerMembers(t, s, "work-1", fields)
			if fields["direct_caller_allowance"] == nil {
				t.Fatal("the live implementation pin declares no direct-caller allowance")
			}
			fields["path_scope"] = []string{"owner"}
			fields["direct_caller_allowance"].(map[string]any)["mechanisms"] = []map[string]any{{"project_id": "project-1", "path": "owner/value.go", "entry_point": "Value"}}
			if out := invoke("record_worker_job", "caller-job", fields, env); out.Outcome != OutcomeOK {
				t.Fatalf("record_worker_job: %+v", out.Error)
			}
			// The architecture Review binds the job it permits (CD-0205 D3).
			oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "continue", false)
			if scenario == "foreign owner" {
				// Another work item in the same Project holds a path claim
				// over the caller path the repair changes.
				const holder = "work-holder"
				if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{
					{EventID: holder + "-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: holder, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Holder","priority":1}`)},
					{EventID: holder + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: holder, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
					{EventID: holder + "-claim", Kind: "work.resource_claimed", SubjectType: store.SubjectWorkItem, SubjectID: holder, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"work_id":"` + holder + `","expected_version":2,"resulting_version":3,"resource_key":"path:caller/use.go","reason":"another item's held path claim","holder_agent":"agent/other","holder_session":"session/other"}`)},
				}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, holder): 0}}); err != nil {
					t.Fatalf("seed the foreign holder's path claim: %v", err)
				}
			}
			const attempt = "attempt:caller"
			if out := invoke("dispatch_worker", "caller-dispatch", map[string]any{"attempt_id": attempt, "worker_packet": deliveryRecoveryPacket(t, s, "execution", attempt, "implementation")}, env); out.Outcome != OutcomeOK {
				t.Fatalf("dispatch_worker: %+v", out.Error)
			}
			job := authorizedWorkerJob(t, s, attempt)
			lane := deliveryRecoveryLane(t, "implementation")
			var packetDigest string
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM worker_authorizations WHERE work_id='work-1' AND attempt_id=?`, attempt).Scan(&packetDigest); err != nil {
				t.Fatal(err)
			}
			dispatch := store.Event{EventID: "caller-lane-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched),
				Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attempt, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest, ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
			if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
				prepared, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
				if err != nil {
					return err
				}
				_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, prepared)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var lookup strings.Builder
			lookup.WriteString("package owner\nfunc Value(key string) int {\nswitch key {\n")
			for i := range 501 {
				fmt.Fprintf(&lookup, "case %q:\nreturn %d\n", strconv.Itoa(i), i)
			}
			lookup.WriteString("default:\nreturn 7\n}\n}\n")
			write("owner/value.go", lookup.String())
			ownerOnly, err := run("go", "test", "./...")
			if err == nil || !strings.Contains(ownerOnly, "not enough arguments in call to owner.Value") {
				t.Fatalf("owner-only check = %v\n%s, want the direct caller's compile failure", err, ownerOnly)
			}
			write("caller/use.go", "package caller\nimport \"example.invalid/caller/owner\"\nfunc Use() int { return owner.Value(\"caller\") }\n")
			write("caller/use_test.go", "package caller\nimport (\"testing\"; \"strconv\"; \"example.invalid/caller/owner\")\nfunc TestUse(t *testing.T) { if Use() != 7 { t.Fatal(\"wrong value\") }; for i := range 501 { if got := owner.Value(strconv.Itoa(i)); got != i { t.Fatalf(\"lookup %d = %d\", i, got) } } }\n")
			combined, err := run("go", "test", "./...")
			if err != nil {
				t.Fatalf("combined check: %v\n%s", err, combined)
			}
			mustGit("add", ".")
			mustGit("commit", "-m", "change the owner and its direct caller")
			stat := strings.Fields(mustGit("diff", "--numstat", "HEAD^", "HEAD", "--", "owner/value.go"))
			if len(stat) != 3 {
				t.Fatalf("production diff is not one text file: %v", stat)
			}
			added, err := strconv.Atoi(stat[0])
			if err != nil || added <= 400 {
				t.Fatalf("production chunk is not above the retired cap: %v (%v)", stat, err)
			}
			repair := store.WorkerDirectCallerRepair{
				Owner: store.WorkerCallerMechanism{Path: "owner/value.go", EntryPoint: "Value"}, Caller: store.WorkerCallerMechanism{Path: "caller/use.go", EntryPoint: "Use"},
				Edge: "Use invokes Value", Exposing: "go test ./... on the owner-only change", ChangedPaths: []string{"caller/use.go", "caller/use_test.go"},
				Checks: store.WorkerCallerRepairChecks{BaselinePass: store.WorkerCallerRepairCheck{Observed: baseline}, OwnerOnlyFail: store.WorkerCallerRepairCheck{Observed: ownerOnly, InferredCause: "Use still passes no argument"}, CombinedPass: store.WorkerCallerRepairCheck{Observed: combined}},
			}
			if scenario == "wrong owner" {
				repair.Owner.EntryPoint = "AnotherOwner"
			}
			payload := store.WorkerCompletedPayload{AttemptID: attempt, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job}
			if scenario != "missing repair" {
				payload.DirectCallerRepairs = []store.WorkerDirectCallerRepair{repair}
			}
			agentWorkerCompletedThroughSeam(t, s, attempt, "caller-completed", payload)
			var observed []string
			var raw string
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(terminal_payload,'$.observed_delivery.changed_paths') FROM worker_attempts WHERE work_id='work-1' AND terminal_kind=? AND attempt_id=?`, store.WorkerCompleted, attempt).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &observed); err != nil || !slices.Equal(observed, []string{"caller/use.go", "caller/use_test.go", "owner/value.go"}) {
				t.Fatalf("observed changed paths = %s (%v), want the delivered owner and caller files", raw, err)
			}
			var epoch int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_dispatch_start_payload,'$.attempt_epoch') FROM workflow_step_epochs WHERE work_id='work-1' ORDER BY latest_dispatch_start_seq DESC LIMIT 1`).Scan(&epoch); err != nil {
				t.Fatal(err)
			}
			acceptFields := map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}
			before := workVersion(t, s, "work-1")
			untrusted := env
			untrusted.ClientRef = "client-unregistered"
			if out := invoke("accept_worker_result", "caller-untrusted", acceptFields, untrusted); out.Outcome == OutcomeOK || workVersion(t, s, "work-1") != before {
				t.Fatalf("untrusted client gained acceptance: %+v", out)
			}
			out := invoke("accept_worker_result", "caller-accept", acceptFields, env)
			if scenario == "contained" {
				if out.Outcome != OutcomeOK {
					t.Fatalf("contained caller acceptance: %+v", out.Error)
				}
				var revisions int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_job_revisions WHERE work_id='work-1' AND job_id='job:caller'`).Scan(&revisions); err != nil || revisions != 1 {
					t.Fatalf("above-cap acceptance needed another assignment: revisions=%d err=%v", revisions, err)
				}
			} else {
				want := map[string]string{
					"missing repair": "changed path caller/use.go outside its path_scope without an evidenced direct-caller repair",
					"wrong owner":    "which is not a mechanism of its direct-caller allowance",
					"foreign owner":  "which work item work-holder holds through claim path:caller/use.go over",
				}[scenario]
				if out.Outcome == OutcomeOK || out.Error == nil || out.Error.Kind != "invalid_input" || !strings.Contains(out.Error.Message, want) || workVersion(t, s, "work-1") != before {
					t.Fatalf("invalid repair acceptance = %+v, want invalid_input %q without effects", out, want)
				}
			}
			var digest string
			if err := s.DatabaseForTesting().QueryRow(`SELECT digest FROM worker_job_revisions WHERE work_id='work-1' ORDER BY recorded_seq DESC LIMIT 1`).Scan(&digest); err != nil {
				t.Fatal(err)
			}
			if digest != job.Digest {
				t.Fatalf("acceptance changed the original job digest: %s", digest)
			}
			if encoded, err := out.Encode(); err != nil || !json.Valid(encoded) {
				t.Fatalf("acceptance result cannot be delivered: %v", err)
			}
		})
	}
}
