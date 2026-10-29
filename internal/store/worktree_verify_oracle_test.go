package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

type nativeOracleFixture struct {
	s                                        *Store
	work, repo, head, initMarker, testMarker string
	bundle                                   NativeOracleControlBundle
	prepare                                  WorktreeVerifyResult
	a                                        NativeOracleRequest
	launches                                 int
}

func TestNativeOraclePreparedMetadataMatchesPublicResult(t *testing.T) {
	f := newNativeOracleFixture(t)
	r := f.prepare
	raw, err := json.Marshal(map[string]any{
		"work_id": r.WorkID, "project_id": r.ProjectID, "branch": r.Branch, "path": r.Path,
		"lease_id": r.LeaseID, "operation_ref": r.OperationRef, "command": r.Command,
		"exit_code": r.ExitCode, "output": r.Output, "output_truncated": r.OutputTruncated,
		"tracked_files_changed": r.TrackedFilesChanged, "oracle": r.Oracle,
		"changed_refs": []any{}, "next_valid_intents": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("worktree_verify_result", raw); err != nil {
		t.Fatalf("producer preparation violates the public result: %v", err)
	}
}

func TestNativeOracleRequestVariants(t *testing.T) {
	bundle := NativeOracleControlBundle{}
	for _, tc := range []struct {
		name string
		req  WorktreeVerifyRequest
		ok   bool
	}{
		{"prepare", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &bundle}}, true},
		{"execute", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "execute", PreparationRunRef: "worktree_verify:ready"}}, true},
		{"no_oracle", WorktreeVerifyRequest{}, false},
		{"unknown_phase", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "unknown"}}, false},
		{"command_and_oracle", WorktreeVerifyRequest{Command: []string{"go", "test"}, Oracle: &NativeOracleRequest{Phase: "execute", PreparationRunRef: "worktree_verify:ready"}}, false},
		{"prepare_no_contract", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "prepare", ControlBundle: &bundle}}, false},
		{"prepare_no_bundle", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1}}, false},
		{"prepare_with_reference", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &bundle, PreparationRunRef: "worktree_verify:ready"}}, false},
		{"execute_no_reference", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "execute"}}, false},
		{"execute_with_bundle", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "execute", ControlBundle: &bundle, PreparationRunRef: "worktree_verify:ready"}}, false},
		{"execute_with_contract", WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: "execute", ExpectedContractVersion: 1, PreparationRunRef: "worktree_verify:ready"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNativeOracleRequest(tc.req)
			if tc.ok && err != nil || !tc.ok && failureKind(err) != KindInvalidPayload {
				t.Fatalf("request variant: %v, valid=%t", err, tc.ok)
			}
		})
	}
}

func newNativeOracleFixture(t *testing.T) *nativeOracleFixture {
	t.Helper()
	const work = "native-oracle-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeOracleFixture{s: seed.store, work: work, repo: t.TempDir(), initMarker: filepath.Join(t.TempDir(), "init.marker"), testMarker: filepath.Join(t.TempDir(), "test.marker")}
	markers, err := json.Marshal(map[string]string{"init": f.initMarker, "test": f.testMarker})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"go.mod":       "module example.invalid/oracle\n\ngo 1.26.0\ntoolchain go1.26.7\n",
		"pkg/value.go": "package oracle\nfunc Value() int { return 7 }\n",
		"pkg/value_test.go": `package oracle
import("testing";"os";"strings";"encoding/json")
func marker(kind string){ data,err:=os.ReadFile("markers.json");if err!=nil { panic(err) };var paths map[string]string;if err=json.Unmarshal(data,&paths);err!=nil { panic(err) };if err=os.WriteFile(paths[kind],[]byte(kind),0600);err!=nil { panic(err) } }
func init(){ marker("init") }
func TestValue(t *testing.T){ if os.Getenv("CONCORD_ORACLE_SECRET_SENTINEL")!="" { t.Fatal("server sentinel leaked") };marker("test");_,_=os.Stdout.Write(append([]byte(strings.Repeat("out",6000)),255,0,10));_,_=os.NewFile(2,"stderr").Write(append([]byte(strings.Repeat("err",6000)),254,0,10));if Value()!=7 { t.Fatal("wrong value") } }
`,
		"pkg/markers.json": string(markers),
		"pkg/oracle.json":  `{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/value_test.go"],"fixture_files":["pkg/markers.json"],"cases":{"case:fixture":["TestValue"]}}`,
	} {
		name = filepath.Join(f.repo, name)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitRunStore(t, f.repo, "init", "-b", "oracle-branch")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "add", ".")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "pinned harness")
	head, err := (ExecGitRunner{}).Run(context.Background(), f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	f.head = strings.TrimSpace(string(head))
	f.qualify(t, "oracle-branch", "pkg", "pkg/oracle.json", "case:fixture", "TestValue")
	return f
}

// qualify claims f.repo at f.head for f.work, prepares the pinned recipe's
// single control for caseID without launching it.
func (f *nativeOracleFixture) qualify(t *testing.T, branch, cwd, recipePath, caseID, testName string) {
	t.Helper()
	if _, err := f.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?,'project','native-claim',?,?,?,?,'active','2026-10-09T00:00:00Z','{}');DELETE FROM write_guard`, WorktreeSetID(f.work), branch, f.head, f.repo, f.repo); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("subject", nil)); err != nil {
		t.Fatal(err)
	}
	fields := acceptanceOracleFieldsWithoutPreparationsForTest(t, f.s, f.work)
	raw, _ := json.Marshal(fields)
	var oracle AcceptanceOracle
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	f.bundle = NativeOracleControlBundle{Owner: oracle.Owners[0], Cases: oracle.Cases, Control: oracle.Controls[0]}
	f.bundle.Cases[0].CaseID = caseID
	f.bundle.Control.CaseIDs = []string{caseID}
	f.bundle.Control.Cwd = cwd
	f.bundle.Control.RecipeSource = WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: recipePath, CommitOID: f.head}
	f.bundle.Control.Argv = []string{"go", "test", "-count=1", "-run", "^(" + testName + ")$", "."}
	f.bundle.Control.ReadinessEvidenceRefs = nil
	f.s.nativeOracleProgramLaunched = func() {
		f.launches++
		var count int
		if err := f.s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE state='held'`).Scan(&count); err != nil || count != 1 {
			t.Errorf("launch with unavailable sole SQL connection: %d %v", count, err)
		}
	}
	prepare := &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	var err error
	f.prepare, err = f.s.VerifyWorktree(context.Background(), f.request("prepare", prepare))
	if err != nil || f.prepare.Oracle == nil || f.prepare.Oracle.Qualification != "ready" {
		t.Fatalf("prepare: %+v err=%v", f.prepare, err)
	}
	f.assertNoLaunch(t)
	f.a = NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
}

func (f *nativeOracleFixture) request(id string, o *NativeOracleRequest) WorktreeVerifyRequest {
	r := WorktreeVerifyRequest{WorkID: f.work, ProjectID: "project", LeaseID: "native-" + id, RequestID: "request-" + id, PrincipalRef: "principal/operator", Owner: SessionWorktreeOwner{ClientRef: "client/oracle", AgentRef: "agent/native", SessionRef: "session/native"}, Runner: ExecGitRunner{}, Oracle: o, Now: time.Now().UTC()}
	r.SupportedBudgetSeconds = 30
	if o == nil {
		r.Command = []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}
	}
	return r
}

func (f *nativeOracleFixture) assertNoLaunch(t *testing.T) {
	t.Helper()
	if f.launches != 0 {
		t.Errorf("test-program launches=%d, want 0", f.launches)
	}
	for _, name := range []string{f.initMarker, f.testMarker} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Errorf("marker exists or cannot prove absence: %s: %v", name, err)
		}
	}
}

func (f *nativeOracleFixture) refusal(t *testing.T, id string, o NativeOracleRequest) {
	t.Helper()
	_, err := f.s.VerifyWorktree(context.Background(), f.request(id, &o))
	if failureKind(err) != KindMissingEvidence {
		t.Errorf("typed refusal=%v, want missing_evidence", err)
	}
	f.assertNoLaunch(t)
	t.Logf("refusal=%s test_program_launches=%d", failureKind(err), f.launches)
}

func TestNativeOracleExecuteUnknownPreparationNeverLaunches(t *testing.T) {
	f := newNativeOracleFixture(t)
	o := f.a
	o.PreparationRunRef = "worktree_verify:invented-preparation"
	f.refusal(t, "forged", o)
}

func TestNativeOraclePreparationScopeAndReplayIdentity(t *testing.T) {
	f := newNativeOracleFixture(t)
	for _, scope := range []struct{ work, project string }{{"foreign-work", "project"}, {f.work, "foreign-project"}} {
		p, err := readOraclePreparationReceiptTx(t.Context(), f.s.db, scope.work, scope.project, f.prepare.OperationRef)
		if err != nil || p != nil {
			t.Fatalf("foreign preparation scope %+v returned %+v, %v", scope, p, err)
		}
	}
	for name, change := range map[string]func(*WorktreeVerifyRequest){
		"work":      func(r *WorktreeVerifyRequest) { r.WorkID = "foreign-work" },
		"project":   func(r *WorktreeVerifyRequest) { r.ProjectID = "foreign-project" },
		"request":   func(r *WorktreeVerifyRequest) { r.RequestID = "foreign-request" },
		"principal": func(r *WorktreeVerifyRequest) { r.PrincipalRef = "principal/foreign" },
		"client":    func(r *WorktreeVerifyRequest) { r.Owner.ClientRef = "client/foreign" },
		"agent":     func(r *WorktreeVerifyRequest) { r.Owner.AgentRef = "agent/foreign" },
		"session":   func(r *WorktreeVerifyRequest) { r.Owner.SessionRef = "session/foreign" },
		"contract":  func(r *WorktreeVerifyRequest) { r.Oracle.ExpectedContractVersion = 2 },
		"phase": func(r *WorktreeVerifyRequest) {
			r.Oracle = &NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := f.request("prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle})
			change(&r)
			if _, err := f.s.VerifyWorktree(t.Context(), r); failureKind(err) != KindInvalidOperation {
				t.Fatalf("replay changed %s: %v", name, err)
			}
			f.assertNoLaunch(t)
		})
	}
}

func TestNativeOraclePrepareContractVersionRefusal(t *testing.T) {
	f := newNativeOracleFixture(t)
	o := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 2, ControlBundle: &f.bundle}
	if _, err := f.s.VerifyWorktree(t.Context(), f.request("foreign-contract", &o)); failureKind(err) != KindVersionConflict {
		t.Fatalf("inactive preparation contract: %v", err)
	}
	f.assertNoLaunch(t)
}

func TestNativeOracleDirtyExecutionNeverLaunches(t *testing.T) {
	f := newNativeOracleFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "pkg/value.go"), []byte("package oracle\nfunc Value() int { return 8 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.refusal(t, "dirty", f.a)
}

func TestNativeOracleExecuteMissingQualifiedSubjectNeverLaunches(t *testing.T) {
	f := newNativeOracleFixture(t)
	if _, err := f.s.db.Exec(`DELETE FROM durable_operations WHERE op_id='worktree_verify:native-subject'`); err != nil {
		t.Fatal(err)
	}
	f.refusal(t, "missing-subject", f.a)
}

func TestNativeOracleExecuteEditedCandidateRequiresFreshPreparation(t *testing.T) {
	f := newNativeOracleFixture(t)
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "--allow-empty", "-m", "edited candidate")
	f.refusal(t, "candidate-unpinned", f.a)
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("candidate-subject", nil)); err != nil {
		t.Fatal(err)
	}
	f.refusal(t, "candidate-pinned", f.a)
	prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("candidate-prepare", &prepare))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "ready" {
		t.Fatalf("fresh candidate preparation: %+v %v", r, err)
	}
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: r.OperationRef}
	r, err = f.s.VerifyWorktree(context.Background(), f.request("candidate-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 1 {
		t.Fatalf("fresh candidate execute: %+v %v launches=%d", r, err, f.launches)
	}
}

func TestNativeOracleExecuteCoreDisagreementNeverLaunches(t *testing.T) {
	f := newNativeOracleFixture(t)
	// Preserve HEAD and preparation. A different native core receipt is
	// the sole changed fact, not a caller subject or a dirty working tree.
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.subject_ref',?) WHERE lease_id='native-subject';UPDATE durable_operations SET result_payload=(SELECT result_json FROM worktree_verify_leases WHERE lease_id='native-subject') WHERE op_id='worktree_verify:native-subject'`, "commit:"+strings.Repeat("b", 40)); err != nil {
		t.Fatal(err)
	}
	f.refusal(t, "core-disagreement", f.a)
}

func TestNativeOracleGenuineExecutionAndImmutableReplay(t *testing.T) {
	f := newNativeOracleFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 {
		t.Fatalf("execute %+v err=%v", r, err)
	}
	if f.launches != 1 {
		t.Fatalf("launches=%d", f.launches)
	}
	for _, name := range []string{f.initMarker, f.testMarker} {
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}
	}
	var lease, operation, command, plan string
	if err := f.s.db.QueryRow(`SELECT l.result_json,d.result_payload,l.command_json,l.native_plan_json FROM worktree_verify_leases l JOIN durable_operations d ON d.op_id=? WHERE l.lease_id=?`, r.OperationRef, r.LeaseID).Scan(&lease, &operation, &command, &plan); err != nil {
		t.Fatal(err)
	}
	if lease != operation || strings.Contains(command, "request") || !strings.HasPrefix(command, "[") || nativeDigest([]byte(plan)) != r.Oracle.NativePlanSHA256 {
		t.Fatal("metadata/plan/argv retention mismatch")
	}
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "--allow-empty", "-m", "later subject")
	replay, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || !oracleEntryIdentical(r, replay) || f.launches != 1 {
		t.Fatalf("replay %+v err=%v launches=%d", replay, err, f.launches)
	}
	if _, err := f.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);UPDATE worktree_entries SET state='reclaimed' WHERE set_id=?;DELETE FROM write_guard`, WorktreeSetID(f.work)); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.InspectWorktree(context.Background(), WorktreeInspectRequest{WorkID: f.work, ProjectID: "project", Mode: "oracle_output", RunRef: r.OperationRef, Stream: "stdout", Length: 16384})
	if err != nil || page.OracleOutput == nil {
		t.Fatalf("retained page %+v %v", page, err)
	}
	var greenPrepare int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, f.prepare.OperationRef).Scan(&greenPrepare); err != nil || greenPrepare != 0 {
		t.Fatalf("preparation created green authority: %d %v", greenPrepare, err)
	}
	t.Logf("genuine test execution: %s; %d distinct witnesses; one launch; identical replay", r.OperationRef, len(r.Oracle.ObservedTestNames))
}

func TestNativeOracleServerEnvironmentDoesNotReachNativeProgram(t *testing.T) {
	t.Setenv("CONCORD_ORACLE_SECRET_SENTINEL", "synthetic-server-secret")
	f := newNativeOracleFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("sentinel", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 1 {
		t.Fatalf("native program inherited server environment: qualification=%+v err=%v launches=%d", r.Oracle, err, f.launches)
	}
}

func TestNativeOracleHangingTestDeadlineReleasesLease(t *testing.T) {
	f := newNativeOracleFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "pkg/value.go"), []byte("package oracle\nimport \"time\"\nfunc Value() int { time.Sleep(3*time.Second); return 7 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "slow")
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	req := f.request("slow-execute", &o)
	req.SupportedBudgetSeconds = 1
	r, err := f.s.VerifyWorktree(context.Background(), req)
	if failureKind(err) != KindUnavailable || r.Oracle == nil || r.Oracle.Qualification != "unavailable" || f.launches != 1 {
		t.Fatalf("hanging test did not expire: qualification=%+v err=%v launches=%d", r.Oracle, err, f.launches)
	}
	var state string
	if err := f.s.db.QueryRow(`SELECT state FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).Scan(&state); err != nil || state != "released" {
		t.Fatalf("expired lease state=%s: %v", state, err)
	}
	var green int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil || green != 0 {
		t.Fatal("deadline expiry published green authority")
	}
}

func TestNativeOracleContextSelectsDirectPreparations(t *testing.T) {
	f := newNativeOracleFixture(t)
	preparations, err := readSelectedOraclePreparations(context.Background(), f.s.db, f.work)
	if err != nil || len(preparations) != 1 || preparations[0].RunRef != f.prepare.OperationRef {
		t.Fatalf("direct preparations=%+v err=%v", preparations, err)
	}
}

func TestNativeOracleCorePreparationAdmissionAndAdapterBoundary(t *testing.T) {
	f := newNativeOracleFixture(t)
	var original string
	if err := f.s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id='native-prepare'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for name, alteration := range map[string]string{
		"unready":             `json_set(result_json,'$.oracle.qualification','unavailable')`,
		"foreign_subject":     `json_set(result_json,'$.oracle.subject_commit','` + strings.Repeat("c", 40) + `')`,
		"contract_drift":      `json_set(result_json,'$.oracle.contract_version',2)`,
		"streams_incomplete":  `json_set(result_json,'$.oracle.streams_complete',json('false'))`,
		"no_selected_names":   `json_set(result_json,'$.oracle.selected_test_names',json_array())`,
		"distinct_count_zero": `json_set(result_json,'$.oracle.selected_distinct_count',0)`,
		"empty_case_map":      `json_set(result_json,'$.oracle.case_to_test_map',json_object())`,
		"foreign_work":        `json_set(result_json,'$.oracle.work_id','foreign-work')`,
		"foreign_project":     `json_set(result_json,'$.oracle.project_id','foreign-project')`,
		"bundle_drift":        `json_set(result_json,'$.oracle.bundle_digest','` + nativeDigest([]byte("foreign bundle")) + `')`,
		"recipe_drift":        `json_set(result_json,'$.oracle.recipe_source.path','pkg/foreign.json')`,
		"environment_drift":   `json_set(result_json,'$.oracle.build_environment_digest','` + nativeDigest([]byte("foreign environment")) + `')`,
		"missing_stage":       `json_set(result_json,'$.oracle.stages',json_array())`,
		"failed_stage":        `json_set(result_json,'$.oracle.stages[0].exit_code',1)`,
		"stream_length_drift": `json_set(result_json,'$.oracle.stdout.length',-1)`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET result_json=` + alteration + ` WHERE lease_id='native-prepare'`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.VerifyWorktree(context.Background(), f.request("preparation-"+name, &f.a)); err == nil {
				t.Fatal("execute admission accepted a mismatched preparation")
			}
			f.assertNoLaunch(t)
			if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET result_json=? WHERE lease_id='native-prepare'`, original); err != nil {
				t.Fatal(err)
			}
		})
	}
	// The adapter's stream-reference checks are not duplicates of core
	// receipt admission. Keep those checks until their owner changes.
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.oracle.stdout.ref','') WHERE lease_id='native-prepare'`); err != nil {
		t.Fatal(err)
	}
	if _, err := readOraclePreparationReceiptTx(context.Background(), f.s.db, f.work, "project", f.prepare.OperationRef); err != nil {
		t.Fatalf("adapter/core boundary changed; reconsider duplicate checks: %v", err)
	}
	f.assertNoLaunch(t)
}

func TestNativeOracleSelectorArgvCeiling(t *testing.T) {
	for _, size := range []int{255, 256, 257} {
		names := []string{"Test" + strings.Repeat("A", 60), "Test" + strings.Repeat("B", 60), "Test" + strings.Repeat("C", 60), "Test" + strings.Repeat("D", size-13-3*65-5)}
		selector := "^(" + strings.Join(names, "|") + ")$"
		control := OracleControl{CaseIDs: []string{"case:x"}, Argv: []string{"go", "test", "-count=1", "-run", selector, "."}}
		_, err := nativeOracleSelector(nativeGoOracleManifest{Cases: map[string][]string{"case:x": names}}, control)
		if (err == nil) != (size <= 256) {
			t.Fatalf("selector bytes %d: %v", size, err)
		}
	}
}

func TestNativeOracleExecutedCasesRequired(t *testing.T) {
	for _, raw := range []string{"", `{"Action":"pass","Package":"p"}`, `{"Action":"run","Package":"p","Test":"TestValue"}
{"Action":"skip","Package":"p","Test":"TestValue"}`, `{"Action":"pass","Package":"p","Test":"TestValue/sub"}`} {
		if _, err := nativeOracleWitnesses([]byte(raw), "p", []string{"TestValue"}, nativeWitnessFramesForTest()); err == nil {
			t.Fatalf("non-execution qualified: %s", raw)
		}
	}
}

func TestNativeOracleWitnessGrammar(t *testing.T) {
	run := `{"Action":"run","Package":"p","Test":"TestValue"}` + "\n"
	pass := `{"Action":"pass","Package":"p","Test":"TestValue"}` + "\n"
	terminal := `{"Action":"pass","Package":"p"}` + "\n"
	for name, raw := range map[string]string{
		"missing_package_pass": run + pass,
		"duplicate_run":        run + run + pass + terminal,
		"duplicate_pass":       run + pass + pass + terminal,
		"duplicate_terminal":   run + pass + terminal + terminal,
		"foreign_failure":      run + pass + `{"Action":"fail","Package":"other","Test":"TestOther"}` + "\n" + terminal,
		"late_witness":         terminal + run + pass,
		"unknown_action":       run + `{"Action":"unknown","Package":"p"}` + "\n" + pass + terminal,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := nativeOracleWitnesses([]byte(raw), "p", []string{"TestValue"}, nativeWitnessFramesForTest()); err == nil {
				t.Fatal("conflicting or incomplete witness stream qualified")
			}
		})
	}
	valid := []byte(run + pass + terminal)
	if _, err := nativeOracleWitnesses(valid, "p", []string{"TestValue"}, nativeWitnessFramesForTest()); err != nil {
		t.Fatalf("unique framed witnesses refused: %v", err)
	}
	if _, err := nativeOracleWitnesses(valid, "p", []string{"TestValue"}, []byte("=== RUN   TestValue\n--- PASS: TestValue (0.00s)\nPASS\n")); err == nil {
		t.Fatal("unmarked witnesses qualified")
	}
}

func nativeWitnessFramesForTest() []byte {
	return []byte("\x16=== RUN   TestValue\n\x16--- PASS: TestValue (0.00s)\n\x16PASS\n")
}

func TestNativeOracleExitZeroDuringTestFails(t *testing.T) {
	f := newNativeOracleFixture(t)
	source := "package oracle\nimport \"os\"\nfunc Value() int { _,_ = os.Stdout.WriteString(\"\\x16--- PASS: TestValue (0.00s)\\n\\x16PASS\\n\"); os.Exit(0); return 7 }\n"
	if err := os.WriteFile(filepath.Join(f.repo, "pkg", "value.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "test-exit-zero")
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("test-exit-zero", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 {
		t.Fatalf("test-time exit zero: exit=%d oracle=%+v err=%v", r.ExitCode, r.Oracle, err)
	}
	if _, err := os.Stat(f.testMarker); err != nil {
		t.Fatalf("the selected test did not reach the exit probe: %v", err)
	}
}

func (f *nativeOracleFixture) commitAndPrepare(t *testing.T, id string) {
	t.Helper()
	gitRunStore(t, f.repo, "add", ".")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", id)
	head, err := (ExecGitRunner{}).Run(context.Background(), f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	f.head = strings.TrimSpace(string(head))
	if _, err := f.s.VerifyWorktree(context.Background(), f.request(id+"-subject", nil)); err != nil {
		t.Fatal(err)
	}
	prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	f.prepare, err = f.s.VerifyWorktree(context.Background(), f.request(id+"-prepare", &prepare))
	if err != nil || f.prepare.Oracle.Qualification != "ready" {
		t.Fatalf("fresh preparation %v %+v", err, f.prepare)
	}
}

func TestNativeOraclePinnedHarnessRejectsFalseGreenAndLaterRepairPasses(t *testing.T) {
	f := newNativeOracleFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "pkg", "value.go"), []byte("package oracle\nfunc Value() int { return 8 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "pkg", "value_test.go"), []byte("package oracle\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "faulty-candidate")
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("faulty-execute", &o))
	if err != nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 || f.launches != 1 {
		t.Fatalf("false-green candidate escaped pinned harness: %+v %v launches=%d", r, err, f.launches)
	}
	var green int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil || green != 0 {
		t.Fatal("failed control acquired verification authority")
	}
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "fail"); err != nil {
		t.Fatalf("truthful native failure receipt refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "pkg", "value.go"), []byte("package oracle\nfunc Value() int { return 7 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "repaired-candidate")
	o = NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err = f.s.VerifyWorktree(context.Background(), f.request("repaired-execute", &o))
	if err != nil || r.Oracle.Qualification != "pass" || f.launches != 2 {
		t.Fatalf("later genuine repair %+v %v launches=%d", r, err, f.launches)
	}
}

func TestNativeOraclePlanTamperRefusesAndHeldRunCannotResume(t *testing.T) {
	f := newNativeOracleFixture(t)
	request := f.request("prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle})
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET state='held',outcome='running' WHERE lease_id=?`, request.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.VerifyWorktree(context.Background(), request); failureKind(err) != KindWorktreeLeaseHeld {
		t.Fatalf("held plan reran: %v", err)
	}
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET state='released',outcome='completed',native_plan_json=json_set(native_plan_json,'$.bundle.control.cwd','changed') WHERE lease_id=?`, request.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.VerifyWorktree(context.Background(), request); failureKind(err) != KindInvariantViolation {
		t.Fatalf("tampered native plan replayed: %v", err)
	}
	f.assertNoLaunch(t)
}

func TestNativeOraclePreparationRecheckBeforePublication(t *testing.T) {
	for _, stageName := range []string{"converter_compile", "convert"} {
		t.Run(stageName, func(t *testing.T) {
			f := newNativeOracleFixture(t)
			f.s.nativeOracleStageObserved = func(name, _ string, _, _ []string) {
				if name == stageName {
					if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.oracle.qualification','unavailable') WHERE lease_id=?`, f.prepare.LeaseID); err != nil {
						t.Error(err)
					}
				}
			}
			r, err := f.s.VerifyWorktree(context.Background(), f.request("authority-drift", &f.a))
			if r.Oracle == nil || r.Oracle.Qualification != "unavailable" {
				t.Fatalf("changed preparation qualified: %+v %v", r, err)
			}
			if err != nil || f.launches != 1 {
				t.Fatalf("release drift fabricated rerun: %v launches=%d", err, f.launches)
			}
			var green int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil || green != 0 {
				t.Fatal("preparation drift created verification authority")
			}
			if _, err := f.s.InspectWorktree(context.Background(), WorktreeInspectRequest{WorkID: f.work, ProjectID: "project", Mode: "oracle_output", RunRef: r.OperationRef, Stream: "stdout", Length: 16384}); err != nil {
				t.Fatalf("committed diagnostics were lost: %v", err)
			}
		})
	}
}

// Candidate stdout does not authenticate the origin of test2json frames.
// This initializer emits selected-test frames and exits before the test runs.
// The marker assertion proves that path; the logged qualification and receipt
// admission expose its effect without requiring a forged pass to be accepted.
func TestNativeOracleCandidateInitForgeryReproducer(t *testing.T) {
	f := newNativeOracleFixture(t)
	forged := "package oracle\nimport \"os\"\nfunc init() { _, _ = os.Stdout.WriteString(\"\\x16=== RUN   TestValue\\n\\x16--- PASS: TestValue (0.00s)\\n\\x16PASS\\n\"); os.Exit(0) }\nfunc Value() int { return 8 }\n"
	if err := os.WriteFile(filepath.Join(f.repo, "pkg", "value.go"), []byte(forged), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "forged-candidate")
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("forged-execute", &o))
	if _, statErr := os.Stat(f.testMarker); !os.IsNotExist(statErr) {
		t.Fatalf("selected test ran; the fixture no longer exercises the initializer forgery: %v", statErr)
	}
	if r.Oracle == nil {
		t.Fatal("forged candidate execute recorded no native result")
	}
	admission := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "pass")
	if r.Oracle.Qualification == "pass" {
		if err != nil || admission != nil {
			t.Fatalf("residual qualified inconsistently: execute=%v admission=%v", err, admission)
		}
		t.Log("KNOWN RESIDUAL, NOT LAW CONFORMANCE: framed init output qualifies although the selected test never ran")
	} else if admission == nil {
		t.Fatal("refused init forgery admitted as a pass")
	}
	t.Logf("initializer forgery outcome: qualification=%s exit=%d launches=%d observed=%v pass_admission_err=%v execute_err=%v", r.Oracle.Qualification, r.ExitCode, f.launches, r.Oracle.ObservedTestNames, admission, err)
}

// The harness removes the recipe from the snapshot before it writes the
// declared files, so a recipe at a Go source, a module file, or a declared
// harness input path refuses at preparation, before any snapshot or launch.
func TestNativeOracleRecipePathCollisionRefuses(t *testing.T) {
	f := newNativeOracleFixture(t)
	valid := `{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/value_test.go"],"fixture_files":[],"cases":{"case:fixture":["TestValue"]}}`
	declared := `{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/value_test.go"],"fixture_files":["pkg/declared.json"],"cases":{"case:fixture":["TestValue"]}}`
	recipes := map[string]string{"pkg/recipe.go": valid, "pkg/go.sum": valid, "pkg/go.work": valid, "pkg/declared.json": declared}
	for name, data := range recipes {
		if err := os.WriteFile(filepath.Join(f.repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitRunStore(t, f.repo, "add", ".")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "colliding recipes")
	head, err := (ExecGitRunner{}).Run(context.Background(), f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("collision-subject", nil)); err != nil {
		t.Fatal(err)
	}
	for name := range recipes {
		bundle := f.bundle
		bundle.Control.RecipeSource = WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: name, CommitOID: commit}
		id := "collision-" + strings.NewReplacer("/", "-", ".", "-").Replace(name)
		_, err := f.s.VerifyWorktree(context.Background(), f.request(id, &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &bundle}))
		if failureKind(err) != KindUnavailable || !strings.Contains(err.Error(), "recipe path collides") {
			t.Fatalf("recipe %s did not refuse as a collision: %v", name, err)
		}
	}
	f.assertNoLaunch(t)
}
