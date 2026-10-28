package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// bindPacketToRecordedState sets a fixture packet's task and inputs.binding
// from recorded state, as the adapter builds them: the active contract premise
// and version when a contract is active, otherwise the work item's recorded
// question with a null contract version; the work version the dispatch is
// admitted at; and the lane's worker-scope assigned result. A job-executing
// lane also binds the work's one dispatch-ready worker-job revision, as the
// adapter selects it from the continuity projection (CD-0205). Call it
// immediately before the dispatch the packet rides.
func bindPacketToRecordedState(t *testing.T, s *store.Store, packet map[string]any) map[string]any {
	t.Helper()
	workID := packet["work_id"].(string)
	laneID := packet["lane_id"].(string)
	assigned, ok := store.WorkerScopeAssignedResult(laneID)
	if !ok {
		t.Fatalf("lane %s carries no worker-scope assignment", laneID)
	}
	db := s.DatabaseForTesting()
	var workVersion int64
	if err := db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&workVersion); err != nil {
		t.Fatal(err)
	}
	inputs := packet["inputs"].(map[string]any)
	binding := map[string]any{"objective_source": "work_question", "work_version": workVersion, "contract_version": nil, "assigned_result": assigned}
	var contractVersion int64
	var premise string
	err := db.QueryRow(`SELECT contract_version,premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&contractVersion, &premise)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		inputs["task"] = recordedWorkQuestion(t, s, workID)
	case err != nil:
		t.Fatal(err)
	default:
		binding["objective_source"] = "contract_premise"
		binding["contract_version"] = contractVersion
		inputs["task"] = premise
	}
	inputs["binding"] = binding
	for member, value := range recordedPacketRecords(t, s, workID) {
		inputs[member] = value
	}
	// A job-executing lane also binds the work's one dispatch-ready worker-job
	// revision, as the adapter selects it from the continuity projection
	// (CD-0205). Call it immediately before the dispatch the packet rides.
	if lane, ok := builtinLane(laneID); ok {
		snapshot, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		if lane.CapabilityClass == "review" && snapshot.ReviewInput != nil {
			inputs["review_input"] = snapshot.ReviewInput
		}
		if len(snapshot.ReadyWorkerJobs) == 1 {
			job := snapshot.ReadyWorkerJobs[0]
			// As the adapter does, only an implementation lane keeps the
			// published caller exclusions (CD-0227 D1).
			if lane.CapabilityClass != "implementation" {
				job.CallerExclusions = nil
			}
			raw, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			inputs["worker_job"] = json.RawMessage(raw)
			packet["schema_version"] = store.WorkerPacketSchemaVersion
		}
	}
	return packet
}

// recordedPacketRecords returns the recorded-state members a truthful lane
// packet carries, as the adapter builds them: the law context, work context,
// design record, and proposal from the pinned continuity, and the work item's
// recorded value statement, task, and narrative from the scope read. Members
// with no record are absent.
func recordedPacketRecords(t *testing.T, s *store.Store, workID string) map[string]any {
	t.Helper()
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]any{}
	if snapshot.LawContext != nil {
		records["law_context"] = snapshot.LawContext
	}
	if snapshot.WorkContext != nil {
		records["work_context"] = snapshot.WorkContext
	}
	if snapshot.DesignRecord != nil {
		records["design_record"] = snapshot.DesignRecord
	}
	if snapshot.ProposalRecord != nil {
		records["proposal_record"] = snapshot.ProposalRecord.PacketProposal()
	}
	var work store.WorkerPacketWorkRecord
	if err := s.DatabaseForTesting().QueryRow(`SELECT coalesce(json_extract(intent_json, '$.value_statement'), ''), coalesce(json_extract(intent_json, '$.task'), ''), coalesce(narrative, '') FROM work_items WHERE id=?`, workID).Scan(&work.ValueStatement, &work.Task, &work.Narrative); err != nil {
		t.Fatal(err)
	}
	if work != (store.WorkerPacketWorkRecord{}) {
		records["work_record"] = work
	}
	return records
}

func builtinLane(laneID string) (store.LaneDefinition, bool) {
	for _, lane := range store.BuiltinLaneDefinitions() {
		if lane.ID == laneID {
			return lane, true
		}
	}
	return store.LaneDefinition{}, false
}

// recordReadyRetryJob records one dispatch-ready worker-job revision through
// the public record_worker_job action, so a job-capable pin admits the
// implementation dispatches the fixture runs.
func recordReadyRetryJob(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string) {
	recordReadyRetryJobWithChecks(t, s, service, env, jobID, 1, nil)
}

// recordReadyRetryJobUnderContract records one dispatch-ready worker-job
// revision through the public record_worker_job action under the named active
// contract version. A revision recorded under a predecessor contract stays
// undispatchable until a successor records a fresh one (CD-0205); supersession
// paths re-record before the next dispatch.
func recordReadyRetryJobUnderContract(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string, contractVersion int64) {
	recordReadyRetryJobWithChecks(t, s, service, env, jobID, contractVersion, nil)
}

// oracleCapableAgentPin mirrors the store's declared-member derivation: the
// pinned definition's record_worker_job payload names acceptance_oracle.
// The store predicate is package-private, so the agent fixture restates the
// one derivation rule the registry owns.
func oracleCapableAgentPin(t *testing.T, s *store.Store) bool {
	t.Helper()
	return agentPinRecordsJobField(t, s, oracleFixtureWork, "acceptance_oracle")
}

// agentPinRecordsJobField reports whether the work's pinned definition's
// record_worker_job payload names the field.
func agentPinRecordsJobField(t *testing.T, s *store.Store, workID, name string) bool {
	t.Helper()
	var ref string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	registered, ok := store.BuiltinWorkflowRegistry().Current(ref)
	if !ok {
		t.Fatalf("current definition %s is not registered", ref)
	}
	for _, action := range registered.Definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == name {
				return true
			}
		}
	}
	return false
}

// addAgentJobCallerMembers authors the direct-caller allowance (CD-0227 D1 as
// amended by CD-0242 D6) an editing job carries on a caller-capable pin: a
// per-job path scope and an allowance inside the oracle owner's Project and
// Domain. The oracle fields must already be present.
func addAgentJobCallerMembers(t *testing.T, s *store.Store, workID string, fields map[string]any) {
	t.Helper()
	if !agentPinRecordsJobField(t, s, workID, "direct_caller_allowance") {
		return
	}
	owner := fields["acceptance_oracle"].(map[string]any)["owners"].([]any)[0].(map[string]any)
	project := owner["mechanism"].(map[string]any)["project_id"]
	inventory := "generated/" + strings.NewReplacer(":", "-", " ", "-").Replace(fmt.Sprint(fields["job_id"]))
	fields["path_scope"] = []string{inventory}
	fields["direct_caller_allowance"] = map[string]any{
		"project_id":   project,
		"mechanisms":   []map[string]any{{"project_id": project, "path": inventory, "entry_point": "recorded job"}},
		"domain_ids":   []any{owner["domain_id"]},
		"law_bindings": []map[string]any{},
	}
}

// The oracle fixture constants name the fixture work, its primary Project,
// and the one real control agentNativeOracle prepares.
const (
	oracleFixtureWork      = "work-1"
	oracleFixtureProject   = "project-1"
	oracleFixtureControlID = "control:o-agent-value"
)

// oracleFixtureDigest is the native digest shape the prepare producer writes:
// sha256 over the exact bytes, prefixed for the closed digest grammar.
func oracleFixtureDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// oracleFixtureControlBundle derives one control's native bundle: the
// control, its owner, and the cases it names. The store predicate is
// package-private, so the agent fixture restates the one derivation rule.
func oracleFixtureControlBundle(graph *store.AcceptanceOracle, controlID string) (store.NativeOracleControlBundle, error) {
	var bundle store.NativeOracleControlBundle
	matches := 0
	for _, control := range graph.Controls {
		if control.ControlID == controlID {
			bundle.Control = control
			matches++
		}
	}
	if matches != 1 {
		return bundle, fmt.Errorf("control %s does not resolve exactly once in the fixture oracle", controlID)
	}
	for _, owner := range graph.Owners {
		if owner.OwnerID == bundle.Control.OwnerID {
			bundle.Owner = owner
		}
	}
	for _, entry := range graph.Cases {
		if slices.Contains(bundle.Control.CaseIDs, entry.CaseID) {
			bundle.Cases = append(bundle.Cases, entry)
		}
	}
	return bundle, nil
}

func oracleFixtureActiveClaimFor(t *testing.T, s *store.Store, workID string) (string, string) {
	t.Helper()
	var path, branch string
	err := s.DatabaseForTesting().QueryRow(`SELECT path,branch FROM worktree_entries WHERE set_id=? AND project_id=? AND state='active' LIMIT 1`,
		store.WorktreeSetID(workID), oracleFixtureProject).Scan(&path, &branch)
	if err == nil {
		return path, branch
	}
	if _, execErr := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM write_guard`, store.WorktreeSetID(workID), oracleFixtureProject, "claim:oracle-fixture-"+workID, "work/oracle-fixture", strings.Repeat("a", 40), t.TempDir(), "repo:oracle-fixture", fixedTime().Format(time.RFC3339Nano)); execErr != nil {
		t.Fatalf("seed the oracle fixture worktree claim: %v", execErr)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT path,branch FROM worktree_entries WHERE set_id=? AND project_id=? AND state='active' LIMIT 1`,
		store.WorktreeSetID(workID), oracleFixtureProject).Scan(&path, &branch); err != nil {
		t.Fatalf("oracle fixture has no active worktree claim: %v", err)
	}
	return path, branch
}

// fixtureClaimSubject turns the claimed worktree into a real git repository
// and returns its HEAD, so the fixture's qualified receipt names a commit the
// claimed worktree actually holds and the dispatch freshness probe observes
// the same commit clean.
func fixtureClaimSubject(t *testing.T, path string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...) //nolint:gosec // test-only git argv, fixed binary
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("seed oracle fixture claim repo %v: %v: %s", args, err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return run("rev-parse", "--verify", "HEAD^{commit}")
	}
	run("init", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Oracle Fixture")
	if err := os.WriteFile(filepath.Join(path, "seed.txt"), []byte("oracle fixture seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "--allow-empty", "-m", "oracle fixture seed")
	return run("rev-parse", "--verify", "HEAD^{commit}")
}

// fixtureOracleSubjectFor resolves one work item's qualified current subject
// from its work pin, so preparation receipts and assertions bind the same
// commit the claimed worktree holds.
func fixtureOracleSubjectFor(t *testing.T, s *store.Store, workID string) string {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), s, workID)
	if err != nil || pin.WorkContext == nil || pin.WorkContext.SubjectCommit == "" {
		t.Fatalf("fixture oracle subject for %s: pin=%#v err=%v, want a qualified subject", workID, pin.WorkContext, err)
	}
	return pin.WorkContext.SubjectCommit
}

// seedOracleVerifySubjectForWork qualifies one work item's fixture subject
// the same way, so a captured work item's recorded oracle binds its own
// current subject rather than the shared fixture work's.
func seedOracleVerifySubjectForWork(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	path, branch := oracleFixtureActiveClaimFor(t, s, workID)
	subject := fixtureClaimSubject(t, path)
	leaseID := "fixture-subject-verify-" + workID + "-" + subject
	var existing int
	if err := db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE lease_id=?`, leaseID).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing == 1 {
		return
	}
	command := []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	result := store.WorktreeVerifyResult{WorkID: workID, ProjectID: oracleFixtureProject, Branch: branch, Path: path,
		LeaseID: leaseID, OperationRef: "worktree_verify:" + leaseID, SubjectRef: "commit:" + subject, Command: command}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,principal_ref,client_ref,agent_ref,session_ref,path,command_json,acquired_at,state,released_at,exit_code,outcome,result_json) VALUES(?,?,?,?,?,?,?,?,?,'2026-10-09T00:00:00Z','released','2026-10-09T00:00:00Z',0,'completed',?)`,
		leaseID, workID, oracleFixtureProject, "principal/fixture", "client/fixture", "agent/fixture", "session/fixture", path, string(commandJSON), string(raw)); err != nil {
		t.Fatalf("seed oracle verify subject lease: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,1,?,'worktree.verify','','worktree_verify','internal_sqlite','digest','{}','completed',?,'[]','[]','principal/fixture','request/oracle-subject','2026-10-09T00:00:00Z','2026-10-09T00:00:01Z','sha256:`+strings.Repeat("9", 64)+`')`,
		"worktree_verify:"+leaseID, workID, string(raw)); err != nil {
		t.Fatalf("seed oracle verify subject producer: %v", err)
	}
}

// deliverAgentFixtureChange commits one implementation attempt's changed file
// and qualifies the new subject before the live terminal observation reads it.
func deliverAgentFixtureChange(t *testing.T, s *store.Store, workID, attemptID string) {
	t.Helper()
	delivery := recordedJobDeliveryPath(t, s, workID, attemptID)
	path, _ := oracleFixtureActiveClaimFor(t, s, workID)
	before := fixtureClaimSubject(t, path)
	if err := os.MkdirAll(filepath.Join(path, filepath.Dir(delivery)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, delivery), []byte(attemptID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, path, "add", delivery)
	runFixtureGit(t, path, "commit", "-m", "implementation delivery")
	seedOracleVerifySubjectForWork(t, s, workID)
	if subject := fixtureOracleSubjectFor(t, s, workID); subject == before || subject != strings.TrimSpace(runFixtureGit(t, path, "rev-parse", "HEAD")) {
		t.Fatalf("qualified delivery subject = %q, want a changed committed HEAD after %q", subject, before)
	}
}

// recordedJobDeliveryPath delivers inside the authorized job's recorded path
// scope, so acceptance holds a contained delta instead of an unscoped path
// with no evidenced caller repair. An attempt without a scoped job delivers
// at the root.
func recordedJobDeliveryPath(t *testing.T, s *store.Store, workID, attemptID string) string {
	t.Helper()
	binding := authorizedWorkerJobFor(t, s, workID, attemptID)
	if binding == nil {
		return "delivery.txt"
	}
	var raw []byte
	err := s.DatabaseForTesting().QueryRow(`SELECT recorded_payload FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, workID, binding.JobID, binding.Revision, binding.Digest).Scan(&raw)
	if err != nil {
		t.Fatalf("read the authorized worker job %+v: %v", binding, err)
	}
	var job store.WorkerJobRecordedPayload
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if len(job.PathScope) == 0 {
		return "delivery.txt"
	}
	return job.PathScope[0] + "/delivery.txt"
}

// agentContractPredicates reads the approved predicates of the work's
// active contract in their recorded order.
func agentContractPredicates(t *testing.T, s *store.Store, workID string) []string {
	t.Helper()
	db := s.DatabaseForTesting()
	var contractVersion int64
	if err := db.QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY ordinal,predicate_id`, workID, contractVersion)
	if err != nil {
		t.Fatal(err)
	}
	var predicates []string
	for rows.Next() {
		var predicate string
		if err := rows.Scan(&predicate); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		predicates = append(predicates, predicate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(predicates) == 0 {
		t.Fatal("the oracle fixture requires an approved contract predicate")
	}
	return predicates
}

// oracleFixturePredicateIDs reads one authored oracle part's predicate ids,
// so the test asserts them exactly as the store decodes them.
func oracleFixturePredicateIDs(t *testing.T, entry map[string]any) []string {
	t.Helper()
	raw, ok := entry["predicate_ids"].([]any)
	if !ok {
		t.Fatalf("oracle fixture entry carries no predicate ids: %#v", entry["predicate_ids"])
	}
	ids := make([]string, 0, len(raw))
	for _, value := range raw {
		id, ok := value.(string)
		if !ok {
			t.Fatalf("oracle fixture predicate id = %#v, want a string", value)
		}
		ids = append(ids, id)
	}
	return ids
}

// oracleFixtureBundleDigest recomputes one control's native bundle digest
// from the fixture's authored oracle, the value the preparation receipt and
// the retained work context must carry.
func oracleFixtureBundleDigest(t *testing.T, oracle map[string]any, controlID string) string {
	t.Helper()
	raw, err := json.Marshal(oracle)
	if err != nil {
		t.Fatal(err)
	}
	var graph store.AcceptanceOracle
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatal(err)
	}
	bundle, err := oracleFixtureControlBundle(&graph, controlID)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Control.ReadinessEvidenceRefs = nil
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return oracleFixtureDigest(encoded)
}

func TestOwnerOracleAgentFixtureTracksApprovedPredicatesAndRetainedReadiness(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
		VALUES('work-1',1,'predicate:second-approved',1,'check','{"kind":"check","check_ref":"check:second","immutable_subject_ref":"commit:second","expected_result":"pass"}');
		DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), oracleFixtureProject)
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	oracle := agentNativeOracle(t, s, service, env)
	owner := oracle["owners"].([]any)[0].(map[string]any)
	control := oracle["controls"].([]any)[0].(map[string]any)
	for _, entry := range []map[string]any{owner, control} {
		ids := oracleFixturePredicateIDs(t, entry)
		if len(ids) != 2 || ids[0] != "predicate:retry-objective" || ids[1] != "predicate:second-approved" {
			t.Fatalf("oracle fixture predicates = %v, want both active contract predicates", ids)
		}
	}
	refs, ok := control["readiness_evidence_refs"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("readiness refs = %v, want one native preparation binding", control["readiness_evidence_refs"])
	}
	ref, _ := refs[0].(string)
	// The production recording is the qualification proof: the exact native
	// preparation join runs inside the record action, so an admitted
	// recording proves the readiness reference is the control's exact
	// qualified preparation and never a seeded claim.
	recordReadyRetryJobWithChecks(t, s, service, env, "job:owner-oracle-fixture", 1, nil)
	var phase, qualification string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(result_json,'$.oracle.phase'),json_extract(result_json,'$.oracle.qualification') FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND json_extract(result_json,'$.operation_ref')=?`, oracleFixtureWork, oracleFixtureProject, ref).Scan(&phase, &qualification); err != nil {
		t.Fatalf("readiness ref %s has no preparation receipt: %v", ref, err)
	}
	if phase != "prepare" || qualification != "ready" {
		t.Fatalf("preparation receipt phase=%s qualification=%s, want a ready prepare", phase, qualification)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, oracleFixtureWork)
	if err != nil || pin.WorkContext == nil || pin.WorkContext.SubjectCommit != fixtureOracleSubjectFor(t, s, oracleFixtureWork) {
		t.Fatalf("work pin work context = %+v err=%v, want the fixture's qualified subject", pin.WorkContext, err)
	}
	preparations := pin.WorkContext.OraclePreparations
	if len(preparations) != 1 || preparations[0].BundleDigest != oracleFixtureBundleDigest(t, oracle, oracleFixtureControlID) || preparations[0].ContractVersion != 1 || preparations[0].SubjectCommit != fixtureOracleSubjectFor(t, s, oracleFixtureWork) {
		t.Fatalf("retained preparations = %+v, want the exact control bundle at contract 1 on the fixture subject", preparations)
	}
}

// recordReadyRetryJobWithChecks records one dispatch-ready worker-job
// revision. A verification worker job requires nonempty checks; the helper
// passes them through when the caller supplies them. The closed live
// record_worker_job variant projects the canonical oracle-capable head, so
// the boundary always requires the acceptance oracle: an oracle-capable pin
// records through the public boundary with the fixture oracle, while a
// released oracle-free pin records through the store-level admit its frozen
// payload declares (CD-0218 D2/D4).
func recordReadyRetryJobWithChecks(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string, contractVersion int64, checks []string) {
	t.Helper()
	const workID = "work-1"
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"job_id": jobID, "objective": "Carry out the approved retry objective.",
		"stopping_condition": "The approved objective's checks pass.",
		"ready":              true, "readiness_evidence": []string{"contract:" + workID + ":" + strconv.FormatInt(contractVersion, 10)},
	}
	if len(checks) > 0 {
		fields["checks"] = checks
	}
	if !oracleCapableAgentPin(t, s) {
		// Released oracle-free pin: the live union publishes no job shape its
		// frozen payload admits, so the store-level admit keeps the released
		// behavior executable without a retired public authoring route.
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		actor := store.WorkflowActor{PrincipalRef: env.PrincipalRef, ClientRef: env.ClientRef, AgentRef: env.AgentRef, SessionRef: env.SessionRef, ActorClass: store.ActorAgent}
		operationID := "released-job-" + jobID + "-" + strconv.FormatInt(version, 10)
		if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
				WorkID: workID, ExpectedVersion: version, ActionID: "record_worker_job", Payload: raw, Actor: actor,
				AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
				PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
				ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
			})
			return err
		}); err != nil {
			t.Fatalf("record the released-pin ready retry job: %v", err)
		}
		return
	}
	fields["acceptance_oracle"] = agentNativeOracle(t, s, service, env)
	addAgentJobCallerMembers(t, s, workID, fields)
	raw, err := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "action_id": "record_worker_job", "idempotency_key": "record-ready-" + jobID,
		"fields": fields,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if result.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record the ready retry job: %+v", result.Error)
	}
}

// recordedWorkQuestion reads the question the store binds a read-only packet
// to before contract approval: the recorded task when it holds visible text,
// else the title, else the narrative. The fixtures here carry ASCII
// whitespace only; the store owns the full selection rule.
func recordedWorkQuestion(t *testing.T, s *store.Store, workID string) string {
	t.Helper()
	var title, narrative, task string
	if err := s.DatabaseForTesting().QueryRow(`SELECT title, narrative, coalesce(json_extract(intent_json, '$.task'), '') FROM work_items WHERE id=?`, workID).Scan(&title, &narrative, &task); err != nil {
		t.Fatal(err)
	}
	switch {
	case strings.TrimSpace(task) != "":
		return task
	case strings.TrimSpace(title) != "":
		return title
	default:
		return narrative
	}
}

// authorizedWorkerJob reads the worker-job binding the attempt's
// dispatch_worker authorization recorded, so fixture worker evidence carries
// exactly the job the core authorized (CD-0205). Nil when the authorization
// bound none or no authorization exists.
func authorizedWorkerJob(t *testing.T, s *store.Store, attemptID string) *store.WorkerJobBinding {
	t.Helper()
	return authorizedWorkerJobFor(t, s, "work-1", attemptID)
}

// authorizedWorkerJobFor reads the worker-job binding one dispatch
// authorization recorded, for work items whose id is not the work-1 fixture
// default.
func authorizedWorkerJobFor(t *testing.T, s *store.Store, workID, attemptID string) *store.WorkerJobBinding {
	t.Helper()
	var raw string
	err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.worker_job'),'') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == "" {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var job store.WorkerJobBinding
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	return &job
}

// recordedAgentOracle reads the oracle the job-recorded event retained under
// the dispatch's exact job binding.
func recordedAgentOracle(t *testing.T, s *store.Store, binding *store.WorkerJobBinding) *store.AcceptanceOracle {
	t.Helper()
	if binding == nil {
		return nil
	}
	var raw sql.NullString
	err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(recorded_payload,'$.acceptance_oracle') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, "work-1", binding.JobID, binding.Revision, binding.Digest).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the authorized worker job has no recorded revision: %+v", binding)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !raw.Valid {
		return nil
	}
	var oracle store.AcceptanceOracle
	if err := json.Unmarshal([]byte(raw.String), &oracle); err != nil {
		t.Fatal(err)
	}
	return &oracle
}
