package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkerJobCallerRepairAcceptanceThroughAgent(t *testing.T) {
	for _, scenario := range []string{"contained", "missing repair", "wrong owner"} {
		t.Run(scenario, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
			// Caller repair needs size members absent from legacy consent pins.
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
			write("go.mod", "module example.invalid/caller\n\ngo 1.26.0\n")
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
			// The oracle fixture qualifies this real baseline before dispatch.
			oracle := agentOracleFieldsForWork(t, s, "work-1")
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
			fields := map[string]any{"job_id": "job:caller", "objective": "Change Value and repair its direct caller", "stopping_condition": "the combined compile check passes",
				"ready": true, "readiness_evidence": []string{"contract:work-1:1"}, "checks": []string{"go test ./..."}, "acceptance_oracle": oracle}
			addAgentJobSizeMembers(t, s, "work-1", fields)
			fields["path_scope"] = []string{"owner"}
			fields["production_size_max"] = 10
			base := fields["measurement_basis"].(map[string]any)["base_commit"].(string)
			fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{{"path": "owner/value.go", "class": "production"}}
			fields["direct_caller_allowance"].(map[string]any)["mechanisms"] = []map[string]any{{"project_id": "project-1", "path": "owner/value.go", "entry_point": "Value"}}
			if out := invoke("record_worker_job", "caller-job", fields, env); out.Outcome != OutcomeOK {
				t.Fatalf("record_worker_job: %+v", out.Error)
			}
			const attempt = "attempt:caller"
			if out := invoke("dispatch_worker", "caller-dispatch", map[string]any{"attempt_id": attempt, "worker_packet": deliveryRecoveryPacket(t, s, "work-1", "execution", attempt, "implementation")}, env); out.Outcome != OutcomeOK {
				t.Fatalf("dispatch_worker: %+v", out.Error)
			}
			job := authorizedWorkerJob(t, s, attempt)
			lane := deliveryRecoveryLane(t, "implementation")
			var packetDigest string
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, attempt).Scan(&packetDigest); err != nil {
				t.Fatal(err)
			}
			dispatch := store.Event{EventID: "caller-lane-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2,
				Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest, ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
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
			write("owner/value.go", "package owner\nfunc Value(_ string) int { return 7 }\n")
			ownerOnly, err := run("go", "test", "./...")
			if err == nil || !strings.Contains(ownerOnly, "not enough arguments in call to owner.Value") {
				t.Fatalf("owner-only check = %v\n%s, want the direct caller's compile failure", err, ownerOnly)
			}
			write("caller/use.go", "package caller\nimport \"example.invalid/caller/owner\"\nfunc Use() int { return owner.Value(\"caller\") }\n")
			write("caller/use_test.go", "package caller\nimport \"testing\"\nfunc TestUse(t *testing.T) { if Use() != 7 { t.Fatal(\"wrong value\") } }\n")
			combined, err := run("go", "test", "./...")
			if err != nil {
				t.Fatalf("combined check: %v\n%s", err, combined)
			}
			mustGit("add", ".")
			mustGit("commit", "-m", "change the owner and its direct caller")
			tree := mustGit("rev-parse", "HEAD")
			repair := store.WorkerDirectCallerRepair{
				Owner: store.WorkerCallerMechanism{Path: "owner/value.go", EntryPoint: "Value"}, Caller: store.WorkerCallerMechanism{Path: "caller/use.go", EntryPoint: "Use"},
				Edge: "Use invokes Value", Exposing: "go test ./... on the owner-only change", ChangedPaths: []string{"caller/use.go", "caller/use_test.go"},
				Checks: store.WorkerCallerRepairChecks{BaselinePass: store.WorkerCallerRepairCheck{Observed: baseline}, OwnerOnlyFail: store.WorkerCallerRepairCheck{Observed: ownerOnly, InferredCause: "Use still passes no argument"}, CombinedPass: store.WorkerCallerRepairCheck{Observed: combined}},
			}
			if scenario == "wrong owner" {
				repair.Owner.EntryPoint = "AnotherOwner"
			}
			payload := store.WorkerCompletedPayload{AttemptID: attempt, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job,
				ProductionSize: &store.WorkerProductionSize{Base: base, Tree: tree, Count: 7}}
			if scenario != "missing repair" {
				payload.DirectCallerRepairs = []store.WorkerDirectCallerRepair{repair}
			}
			agentWorkerCompletedThroughSeam(t, s, attempt, "caller-completed", payload)
			var epoch int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&epoch); err != nil {
				t.Fatal(err)
			}
			acceptFields := map[string]any{"attempt_id": attempt, "attempt_epoch": epoch, "supplemental_classifications": []map[string]any{{"path": "caller/use_test.go", "class": "test"}}}
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
			} else {
				want := "outside its path_scope without an evidenced direct-caller repair"
				if scenario == "wrong owner" {
					want = "which is not a mechanism of its direct-caller allowance"
				}
				if out.Outcome == OutcomeOK || out.Error == nil || out.Error.Kind != "invalid_input" || !strings.Contains(out.Error.Message, want) || workVersion(t, s, "work-1") != before {
					t.Fatalf("invalid repair acceptance = %+v, want invalid_input %q without effects", out, want)
				}
			}
			var digest string
			var sizeCap int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.digest'), json_extract(payload,'$.production_size_max') FROM domain_events WHERE subject_id='work-1' AND kind=? ORDER BY seq DESC LIMIT 1`, store.WorkerJobRecorded).Scan(&digest, &sizeCap); err != nil {
				t.Fatal(err)
			}
			if digest != job.Digest || sizeCap != 10 {
				t.Fatalf("acceptance changed the original digest/cap: %s / %d", digest, sizeCap)
			}
			if scenario == "contained" {
				var final int64
				if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_production_size.count') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted).Scan(&final); err != nil {
					t.Fatal(err)
				}
				if final != 4 {
					t.Fatalf("final accepted count = %d, want 4 after the coordinator classifies the three-line test", final)
				}
			}
			if encoded, err := out.Encode(); err != nil || !json.Valid(encoded) {
				t.Fatalf("acceptance result cannot be delivered: %v", err)
			}
		})
	}
}
