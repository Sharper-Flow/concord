package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// oracleGoArgv is the closed native selector argv for one fixture control.
// Each label selects its own literal top-level test, so distinct labels stay
// distinct controls.
func oracleGoArgv(label string) []string {
	return []string{"go", "test", "-count=1", "-run", "^(" + oracleFixtureTestName(label) + ")$", "."}
}

func oracleFixtureTestName(label string) string {
	name := "TestOracle"
	for _, part := range strings.FieldsFunc(label, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		name += strings.ToUpper(part[:1]) + part[1:]
	}
	return name
}

// oracleFixtureCases maps every case of a control to the single test its
// closed selector names. A control without that argv shape cannot carry a
// native preparation, so the fixture refuses it.
func oracleFixtureCases(t *testing.T, control OracleControl) map[string][]string {
	t.Helper()
	if len(control.Argv) != 6 || !strings.HasPrefix(control.Argv[4], "^(") || !strings.HasSuffix(control.Argv[4], ")$") {
		t.Fatalf("control %s argv %q is not one closed native selector", control.ControlID, control.Argv)
	}
	name := strings.TrimSuffix(strings.TrimPrefix(control.Argv[4], "^("), ")$")
	cases := map[string][]string{}
	for _, id := range control.CaseIDs {
		cases[id] = []string{name}
	}
	return cases
}

func oracleFixtureFile(path, content string) NativeOracleFile {
	digest := nativeDigest([]byte(content))
	return NativeOracleFile{Path: path, BlobOID: strings.TrimPrefix(digest, "sha256:")[:40], SHA256: digest}
}

// oracleFixtureRuntime names the runtime every ledger fixture preparation records.
const oracleFixtureRuntime = "fixture"

// oracleFixtureRuntimeKey carries the fixture runtime a preparation observes.
type oracleFixtureRuntimeKey struct{}

func oracleFixtureRuntimeFrom(ctx context.Context) string {
	if runtime, ok := ctx.Value(oracleFixtureRuntimeKey{}).(string); ok {
		return runtime
	}
	return oracleFixtureRuntime
}

// oracleFixtureNativePlan builds the immutable Go prepare plan a fixture
// preparation records on the named runtime.
func oracleFixtureNativePlan(t *testing.T, req WorktreeVerifyRequest, version int64, subject string, bundle NativeOracleControlBundle, runtime string) nativeOraclePlan {
	t.Helper()
	control := bundle.Control
	testFile := control.Cwd + "/oracle_fixture_test.go"
	manifest := nativeGoOracleManifest{Kind: "go_top_level_v1", PackageCwd: control.Cwd, TestFiles: []string{testFile}, FixtureFiles: []string{}, Cases: oracleFixtureCases(t, control)}
	manifestRaw, _ := json.Marshal(manifest)
	environment := nativeGoEnvironment{GoExecutable: "go", GoRoot: "goroot", ModuleCache: "modcache", Version: "go1.26", GOOS: "linux", GOARCH: "amd64", ToolchainIdentity: "go1.26 linux/amd64", ModuleInputsDigest: nativeDigest([]byte("fixture module inputs"))}
	if runtime != oracleFixtureRuntime {
		environment.ToolchainIdentity += " " + runtime
	}
	environment.Digest = nativeDigest([]byte(runtime + " environment\x00" + environment.ModuleInputsDigest))
	return nativeOraclePlan{Request: *req.Oracle, RequestID: req.RequestID, WorkID: req.WorkID, ProjectID: req.ProjectID, ContractVersion: version, SubjectCommit: subject, Bundle: bundle, BundleDigest: nativeBundleDigest(bundle),
		Manifest: manifest, ManifestFile: oracleFixtureFile(control.RecipeSource.Path, string(manifestRaw)), Files: []NativeOracleFile{oracleFixtureFile(testFile, "package fixture")}, Environment: environment,
		StreamLimit: nativeOracleStreamLimit, CachePolicy: "private-disposable-per-lease",
		Stages: []NativeOracleStage{{Name: "dependencies", Argv: []string{"go", "mod", "verify"}}, {Name: "metadata", Argv: []string{"go", "list", "-json", "-deps", "-test", "."}}, {Name: "compile", Argv: []string{"go", "test", "-c", "-o", "$scratch/oracle.test", "."}}, {Name: "converter_compile", Argv: []string{"go", "build", "-o", "$scratch/test2json", "cmd/test2json"}}, {Name: "dependencies_final", Argv: []string{"go", "mod", "verify"}}}}
}

// These trusted ledger fixtures isolate authoring, receipt, debt, and role joins.
// They make no execution claim. The NativeOracle tests separately exercise the
// real prepare/execute producer and count actual test-program launches.
//
// A preparation binds the work's actual qualified verify subject and its
// active claim, and records every field the immutable prepare plan carries,
// so it passes the same completeness check a produced preparation passes.
func seedOraclePreparationLedgerFixture(t *testing.T, s *Store, work string, oracle *AcceptanceOracle) {
	t.Helper()
	seedOraclePreparationLedgerFixtureOn(t, s, work, oracle, oracleFixtureRuntime)
}

// seedOraclePreparationLedgerFixtureOn seeds the preparations on the named
// fixture runtime.
func seedOraclePreparationLedgerFixtureOn(t *testing.T, s *Store, work string, oracle *AcceptanceOracle, runtime string) {
	t.Helper()
	ctx := context.Background()
	subject, err := readCurrentOracleSubject(ctx, s.db, work)
	if err != nil {
		t.Fatal(err)
	}
	if subject == "" {
		t.Fatalf("preparation fixture for %s has no qualified verify subject on an active claim; run a worktree verify first", work)
	}
	version, err := activeWorkflowContractVersion(ctx, s.db, work, "fixture")
	if err == sql.ErrNoRows {
		// A worktree-only fixture work carries no workflow contract; the
		// preparation only needs one internally consistent version.
		version = 1
	} else if err != nil {
		t.Fatal(err)
	}
	for i := range oracle.Controls {
		control := &oracle.Controls[i]
		bundle, err := nativeBundleForControl(oracle, control.ControlID)
		if err != nil {
			t.Fatal(err)
		}
		bundle.Control.ReadinessEvidenceRefs = nil
		project := control.RecipeSource.ProjectID
		var root string
		if err := s.db.QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id=? DESC LIMIT 1`, WorktreeSetID(work), project).Scan(&root); err != nil {
			t.Fatalf("preparation fixture for %s has no active claim: %v", work, err)
		}
		// The lease id carries the contract version: the insert folds on it,
		// so a successor contract seeds its own preparation instead of
		// silently keeping the predecessor's row, whose contract version the
		// authority join would refuse.
		id := "fixture-prepare-" + work + "-v" + strconv.FormatInt(version, 10) + "-" + strings.TrimPrefix(nativeBundleDigest(bundle), "sha256:") + "-" + subject
		if runtime != oracleFixtureRuntime {
			id += "-" + runtime
		}
		ref := worktreeVerifyOperationRef(id)
		control.ReadinessEvidenceRefs = []string{ref}
		req := WorktreeVerifyRequest{WorkID: work, ProjectID: project, LeaseID: id, RequestID: id, Oracle: &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: version, ControlBundle: &bundle}, Command: slices.Clone(control.Argv)}
		plan := oracleFixtureNativePlan(t, req, version, subject, bundle, runtime)
		planRaw, _ := json.Marshal(plan)
		req.nativePlanSHA256 = nativeDigest(planRaw)
		r := nativeOracleInitialResult(req, WorktreeEntry{Path: root}, plan)
		r.Oracle.Qualification = "ready"
		for _, stage := range plan.Stages {
			r.Oracle.Stages = append(r.Oracle.Stages, NativeOracleStage{Name: stage.Name, Argv: slices.Clone(stage.Argv)})
		}
		raw, err := marshalNativeVerifyRecord(&r, &nativeStreamCapture{stdout: []byte{}, stderr: []byte{}, complete: true})
		if err != nil {
			t.Fatal(err)
		}
		if !nativePreparationComplete(r.Oracle, plan) {
			t.Fatalf("preparation fixture for %s does not satisfy the production completeness check", control.ControlID)
		}
		_, err = s.db.Exec(`INSERT OR IGNORE INTO worktree_verify_leases(lease_id,work_id,project_id,principal_ref,client_ref,agent_ref,session_ref,path,command_json,acquired_at,state,released_at,exit_code,outcome,result_json,native_plan_json,native_plan_sha256,stdout_blob,stderr_blob) VALUES(?,?,?,'principal/fixture','client/fixture','agent/fixture','session/fixture',?,?,'2026-10-09T00:00:00Z','released','2026-10-09T00:00:00Z',0,'completed',?,?,?,x'',x'')`, id, work, project, root, workflowJSON(req.Command), string(raw), string(planRaw), req.nativePlanSHA256)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func seedOraclePreparationFieldsFixture(t *testing.T, s *Store, work string, fields map[string]any) {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var graph AcceptanceOracle
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatal(err)
	}
	seedOraclePreparationLedgerFixture(t, s, work, &graph)
	for i, control := range fields["controls"].([]map[string]any) {
		control["readiness_evidence_refs"] = graph.Controls[i].ReadinessEvidenceRefs
	}
}

func seedOracleExecuteLedgerFixture(t *testing.T, s *Store, r *WorktreeVerifyResult, oracle *AcceptanceOracle) {
	t.Helper()
	ctx := context.Background()
	var control *OracleControl
	for i := range oracle.Controls {
		if slices.Equal(oracle.Controls[i].Argv, r.Command) {
			control = &oracle.Controls[i]
			break
		}
	}
	if control == nil {
		t.Fatal("execute fixture has no exact declared control")
	}
	bundle, err := nativeBundleForControl(oracle, control.ControlID)
	if err != nil {
		t.Fatal(err)
	}
	// The execution joins one complete trusted preparation of the same
	// subject and bundle. An oracle that names no such preparation gets one.
	var preparation *NativeOraclePreparation
	if len(control.ReadinessEvidenceRefs) > 0 {
		preparation, err = readOraclePreparationReceiptTx(ctx, s.db, r.WorkID, r.ProjectID, control.ReadinessEvidenceRefs[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	if preparation == nil {
		seedOraclePreparationLedgerFixture(t, s, r.WorkID, oracle)
		if preparation, err = readOraclePreparationReceiptTx(ctx, s.db, r.WorkID, r.ProjectID, control.ReadinessEvidenceRefs[0]); err != nil || preparation == nil {
			t.Fatalf("execute fixture preparation for %s is unreadable: %v", control.ControlID, err)
		}
		if bundle, err = nativeBundleForControl(oracle, control.ControlID); err != nil {
			t.Fatal(err)
		}
	}
	preparationRef := control.ReadinessEvidenceRefs[0]
	request := NativeOracleRequest{Phase: "execute", PreparationRunRef: preparationRef}
	subject := strings.TrimPrefix(r.SubjectRef, "commit:")
	cases := oracleFixtureCases(t, *control)
	names, err := nativeOracleCaseTests(nativeGoOracleManifest{Cases: cases}, *control)
	if err != nil {
		t.Fatal(err)
	}
	plan := nativeOraclePlan{Request: request, RequestID: r.LeaseID, WorkID: r.WorkID, ProjectID: r.ProjectID, ContractVersion: preparation.ContractVersion, SubjectCommit: subject, WorktreeIdentity: nativeWorktreePathIdentity(r.Path), Bundle: bundle, BundleDigest: nativeBundleDigest(bundle),
		Manifest:     nativeGoOracleManifest{Kind: "go_top_level_v1", PackageCwd: control.Cwd, Cases: cases},
		ManifestFile: NativeOracleFile{Path: control.RecipeSource.Path, BlobOID: preparation.ManifestBlob, SHA256: preparation.ManifestDigest}, Files: slices.Clone(preparation.Files),
		Environment: nativeGoEnvironment{ToolchainIdentity: preparation.ToolchainIdentity, Digest: preparation.BuildEnvironmentDigest},
		Stages:      []NativeOracleStage{{Name: "test", Argv: slices.Clone(control.Argv)}}}
	planRaw, _ := json.Marshal(plan)
	req := WorktreeVerifyRequest{WorkID: r.WorkID, ProjectID: r.ProjectID, LeaseID: r.LeaseID, Command: r.Command, Oracle: &request, nativePlanSHA256: nativeDigest(planRaw)}
	native := nativeOracleInitialResult(req, WorktreeEntry{Path: r.Path, Branch: r.Branch}, plan)
	r.Oracle = native.Oracle
	r.Oracle.InputManifestDigest = nativeDigest([]byte("trusted input fixture"))
	r.Oracle.BinaryDigest = nativeDigest([]byte("trusted binary fixture"))
	r.Oracle.SelectedTestNames = names
	r.Oracle.ObservedTestNames = slices.Clone(names)
	r.Oracle.SelectedDistinctCount = len(names)
	r.Oracle.CaseToTestMap = cases
	r.Oracle.Stages = []NativeOracleStage{{Name: "test", Argv: slices.Clone(control.Argv), ExitCode: r.ExitCode}}
	r.Oracle.Qualification = "pass"
	if r.ExitCode != 0 {
		r.Oracle.Qualification = "fail"
	}
	raw, err := marshalNativeVerifyRecord(r, &nativeStreamCapture{stdout: []byte{}, stderr: []byte{}, complete: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`UPDATE worktree_verify_leases SET native_plan_json=?,native_plan_sha256=?,stdout_blob=x'',stderr_blob=x'',result_json=? WHERE lease_id=?`, string(planRaw), req.nativePlanSHA256, string(raw), r.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE durable_operations SET result_payload=? WHERE op_id=?`, string(raw), r.OperationRef); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeOracleProducerTx(ctx, s.db, r.WorkID, r, r.Oracle.Qualification); err != nil {
		t.Fatalf("producer fixture does not join its native preparation: %v", err)
	}
}

func oracleFixtureGraphForCommand(t *testing.T, s *Store, work string, command []string) *AcceptanceOracle {
	t.Helper()
	rows, err := s.db.Query(`SELECT native_plan_json FROM worktree_verify_leases WHERE work_id=? AND state='released' AND outcome='completed' AND json_extract(result_json,'$.oracle.phase')='prepare' AND json_extract(result_json,'$.oracle.qualification')='ready' ORDER BY acquired_at DESC`, work)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p nativeOraclePlan
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		if slices.Equal(p.Bundle.Control.Argv, command) {
			return &AcceptanceOracle{Owners: []OracleOwner{p.Bundle.Owner}, Cases: p.Bundle.Cases, Controls: []OracleControl{p.Bundle.Control}}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("receipt fixture has no exact prepared oracle")
	return nil
}
