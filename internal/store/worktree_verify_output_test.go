package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// dropMigration125Objects restores the pre-oracle lease shape for migration
// reapplication fixtures. Pair triggers must leave before their columns.
func dropMigration125Objects(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
DROP TRIGGER native_oracle_plan_pair_insert;
DROP TRIGGER native_oracle_plan_pair_update;
ALTER TABLE worktree_verify_leases DROP COLUMN native_plan_json;
ALTER TABLE worktree_verify_leases DROP COLUMN native_plan_sha256;
ALTER TABLE worktree_verify_leases DROP COLUMN stdout_blob;
ALTER TABLE worktree_verify_leases DROP COLUMN stderr_blob;
`)
	return err
}

func TestNativeOracleStreamBudgetBoundary(t *testing.T) {
	for _, size := range []int{nativeOracleStreamLimit, nativeOracleStreamLimit + 1} {
		ctx, cancel := context.WithCancel(context.Background())
		c := &nativeStreamCapture{complete: true}
		w := nativeCaptureWriter{capture: c, cancel: cancel}
		if n, err := w.Write(bytes.Repeat([]byte{0xff}, size)); err != nil || n != size {
			t.Fatalf("capture %d %v", n, err)
		}
		if len(c.stdout) != nativeOracleStreamLimit || c.complete != (size == nativeOracleStreamLimit) || (ctx.Err() == nil) != (size == nativeOracleStreamLimit) {
			t.Fatalf("size=%d stored=%d complete=%v cancel=%v", size, len(c.stdout), c.complete, ctx.Err())
		}
		cancel()
	}
}

func seedNativeOutputLease(t *testing.T) (*Store, WorktreeVerifyResult, []byte, []byte) {
	t.Helper()
	s, repo := realGitTiersFixture(t)
	r, err := s.VerifyWorktree(context.Background(), oracleRealVerifyRequest("native-output", nil))
	if err != nil {
		t.Fatal(err)
	}
	stdout := append(bytes.Repeat([]byte("stdout"), 4000), 0xff, 0x00)
	stderr := append(bytes.Repeat([]byte("stderr"), 4000), 0xfe, 0x00)
	plan := `{"protocol":"native_oracle_v2","subject_commit":"synthetic"}`
	r.Oracle = &NativeOracleResult{NativeOraclePreparation: NativeOraclePreparation{Protocol: "native_oracle_v2", Phase: "execute", Qualification: "pass", RunRef: r.OperationRef, NativePlanSHA256: nativeDigest([]byte(plan))}}
	c := &nativeStreamCapture{stdout: stdout, stderr: stderr, complete: true}
	raw, err := marshalNativeVerifyRecord(&r, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE worktree_verify_leases SET native_plan_json=?,native_plan_sha256=?,stdout_blob=?,stderr_blob=?,result_json=? WHERE lease_id=?`, plan, r.Oracle.NativePlanSHA256, stdout, stderr, string(raw), r.LeaseID); err != nil {
		t.Fatal(err)
	}
	_ = repo
	return s, r, stdout, stderr
}

func TestNativeOracleOutputReadAfterReclaim(t *testing.T) {
	s, r, stdout, stderr := seedNativeOutputLease(t)
	if _, err := s.db.Exec(`UPDATE worktree_entries SET state='reclaimed' WHERE set_id=?`, WorktreeSetID(r.WorkID)); err != nil {
		t.Fatal(err)
	}
	for stream, want := range map[string][]byte{"stdout": stdout, "stderr": stderr} {
		var all []byte
		for offset := int64(0); ; {
			page, err := s.InspectWorktree(context.Background(), WorktreeInspectRequest{WorkID: r.WorkID, ProjectID: r.ProjectID, Mode: "oracle_output", RunRef: r.OperationRef, Stream: stream, Offset: offset, Length: 16384})
			if err != nil {
				t.Fatal(err)
			}
			p := page.OracleOutput
			data, err := base64.StdEncoding.DecodeString(p.DataBase64)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, data...)
			encoded, _ := json.Marshal(page)
			if len(encoded) > 51200 {
				t.Fatal("page exceeds unchanged response envelope")
			}
			if p.EOF {
				break
			}
			offset = p.NextOffset
		}
		if !bytes.Equal(all, want) {
			t.Fatalf("%s raw bytes changed", stream)
		}
	}
	if _, err := s.InspectWorktree(context.Background(), WorktreeInspectRequest{WorkID: r.WorkID, ProjectID: r.ProjectID, Mode: "status"}); failureKind(err) != KindProjectionNotFound {
		t.Fatalf("legacy inspect lost active-tree gate: %v", err)
	}
}

func TestNativeOracleOutputReadScopeAndBounds(t *testing.T) {
	s, r, _, _ := seedNativeOutputLease(t)
	base := WorktreeInspectRequest{WorkID: r.WorkID, ProjectID: r.ProjectID, Mode: "oracle_output", RunRef: r.OperationRef, Stream: "stdout", Length: 16384}
	for _, change := range []func(*WorktreeInspectRequest){func(r *WorktreeInspectRequest) { r.WorkID = "foreign" }, func(r *WorktreeInspectRequest) { r.ProjectID = "foreign" }, func(r *WorktreeInspectRequest) { r.RunRef = "foreign" }, func(r *WorktreeInspectRequest) { r.Offset = -1 }, func(r *WorktreeInspectRequest) { r.Length = 16385 }, func(r *WorktreeInspectRequest) { r.Length = 0 }, func(r *WorktreeInspectRequest) { r.Stream = "combined" }, func(r *WorktreeInspectRequest) { r.Path = "arbitrary" }, func(r *WorktreeInspectRequest) { r.Offset = 999999 }} {
		req := base
		change(&req)
		if _, err := s.InspectWorktree(context.Background(), req); err == nil {
			t.Fatalf("invalid page admitted: %+v", req)
		}
	}
	if _, err := s.db.Exec(`UPDATE worktree_verify_leases SET stdout_blob=x'FF' WHERE lease_id=?`, r.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InspectWorktree(context.Background(), base); failureKind(err) != KindInvariantViolation {
		t.Fatalf("corrupt BLOB qualified: %v", err)
	}
}

func TestNativeOracleOutputReadNoIdempotency(t *testing.T) {
	s, r, _, _ := seedNativeOutputLease(t)
	var before, after int
	if err := s.db.QueryRow(`SELECT count(*) FROM idempotency_records`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.InspectWorktree(context.Background(), WorktreeInspectRequest{WorkID: r.WorkID, ProjectID: r.ProjectID, Mode: "oracle_output", RunRef: r.OperationRef, Stream: "stderr", Length: 16384}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM idempotency_records`).Scan(&after); err != nil || after != before {
		t.Fatalf("output read persisted mutation responses: %d/%d %v", before, after, err)
	}
}

func TestNativeOracleMigrationAdditiveLegacyNulls(t *testing.T) {
	s, _ := realGitTiersFixture(t)
	r, err := s.VerifyWorktree(context.Background(), oracleRealVerifyRequest("legacy-null", nil))
	if err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := s.db.QueryRow(`SELECT (native_plan_json IS NULL)+(native_plan_sha256 IS NULL)+(stdout_blob IS NULL)+(stderr_blob IS NULL) FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&nulls); err != nil || nulls != 4 {
		t.Fatalf("legacy columns upcast: %d %v", nulls, err)
	}
	if migrations[len(migrations)-1].Breaking {
		t.Fatal("native migration is not additive")
	}
	if _, err := s.db.Exec(`UPDATE worktree_verify_leases SET stdout_blob=? WHERE lease_id=?`, bytes.Repeat([]byte{1}, nativeOracleStreamLimit+1), r.LeaseID); err == nil {
		t.Fatal("BLOB bound is not enforced")
	}
	if _, err := s.db.Exec(`UPDATE worktree_verify_leases SET native_plan_json='{}' WHERE lease_id=?`, r.LeaseID); err == nil {
		t.Fatal("plan pairing is not enforced")
	}
}

func TestNativeOracleAbandonedHasNoDiagnosticsPromise(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)
	req := verifyRequest(git, "native-abandoned", []string{"true"}, nil)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := acquireVerifyLeaseTx(context.Background(), tx, req, entry, workflowJSON(req.Command), req.Now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	released := false
	releaseAbandonedVerifyLease(s, context.Background(), req.LeaseID, &released)
	var outcome string
	var absent int
	if err := s.db.QueryRow(`SELECT outcome,(result_json IS NULL)+(stdout_blob IS NULL)+(stderr_blob IS NULL) FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).Scan(&outcome, &absent); err != nil || outcome != "aborted" || absent != 3 {
		t.Fatalf("abandoned diagnostics fabricated: %s %d %v", outcome, absent, err)
	}
	if _, err := s.VerifyWorktree(context.Background(), req); err == nil {
		t.Fatal("spent abandoned lease replayed")
	}
}

func TestNativeOraclePackageInputClosure(t *testing.T) {
	for _, name := range []string{"extra.dat", "unsupported.s", "unsupported.h", "unsupported.syso", "default.pgo"} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "pkg"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "pkg", name), []byte("undeclared"), 0600); err != nil {
			t.Fatal(err)
		}
		p := nativeOraclePlan{Bundle: NativeOracleControlBundle{Control: OracleControl{Cwd: "pkg"}}}
		if _, err := validateNativeOraclePackageInputs(root, p); failureKind(err) != KindUnavailable {
			t.Fatalf("undeclared %s admitted: %v", name, err)
		}
	}
}

func TestNativeOracleEmbedFixtureClosure(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "pkg")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "value_test.go"), []byte("package p\nimport \"testing\"\nfunc TestValue(t *testing.T){}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".hidden"), []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	p := nativeOraclePlan{Manifest: nativeGoOracleManifest{TestFiles: []string{"pkg/value_test.go"}, FixtureFiles: []string{}, Cases: map[string][]string{"case:x": {"TestValue"}}}, Bundle: NativeOracleControlBundle{Control: OracleControl{Cwd: "pkg", CaseIDs: []string{"case:x"}, Argv: []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."}}}}
	pkg := nativeGoPackage{Dir: cwd, ImportPath: "p", TestGoFiles: []string{"value_test.go"}, TestEmbedFiles: []string{".hidden"}}
	raw, _ := json.Marshal(pkg)
	if _, err := validateNativeGoMetadata(raw, root, p); failureKind(err) != KindUnavailable {
		t.Fatalf("undeclared concrete all: embed admitted: %v", err)
	}
	p.Manifest.FixtureFiles = []string{"pkg/.hidden"}
	p.Files = []NativeOracleFile{{Path: "pkg/.hidden", SHA256: nativeDigest([]byte("pinned"))}}
	if _, err := validateNativeGoMetadata(raw, root, p); err != nil {
		t.Fatalf("declared exact hidden fixture refused: %v", err)
	}
	p.Files[0].SHA256 = nativeDigest([]byte("different"))
	if _, err := validateNativeGoMetadata(raw, root, p); err == nil {
		t.Fatal("changed embedded bytes admitted")
	}
}

func TestNativeOracleEnvironmentPinned(t *testing.T) {
	t.Setenv("GOFLAGS", "-exec=evil")
	t.Setenv("GOTOOLCHAIN", "auto")
	t.Setenv("GOPROXY", "https://example.invalid")
	t.Setenv("GOCACHE", "/untrusted/cache")
	e, err := resolveNativeGoEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	env := nativeControlledEnv(e, "/private/lease/cache")
	for _, v := range []string{"GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=readonly", "CGO_ENABLED=0", "GOWORK=off", "GOCACHE=/private/lease/cache"} {
		if !slices.Contains(env, v) {
			t.Fatalf("pinned environment omits %s", v)
		}
	}
	for _, v := range env {
		if strings.Contains(v, "evil") || v == "GOCACHE=/untrusted/cache" {
			t.Fatalf("host compiler input leaked: %s", v)
		}
	}
}

func TestNativeOracleRootedInputGraphIncludesDependencies(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"pkg/value.go", "dependency/value.go"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte("package p"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := nativeOraclePlan{Bundle: NativeOracleControlBundle{Control: OracleControl{Cwd: "pkg"}}}
	before, err := validateNativeOraclePackageInputs(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dependency/value.go"), []byte("package changed"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := validateNativeOraclePackageInputs(root, p)
	if err != nil || before == after {
		t.Fatalf("local compiler dependency drift went unobserved: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "dependency/link")); err != nil {
		t.Fatal(err)
	}
	if _, err := validateNativeOraclePackageInputs(root, p); failureKind(err) != KindUnavailable {
		t.Fatalf("input graph followed a link: %v", err)
	}
}

func TestNativeOracleModuleToolchainDirectiveIsParsed(t *testing.T) {
	for _, data := range []string{"// toolchain go1.26.7\ngo 1.26.0\n", "toolchain go1.26.7-extra", "toolchain go1.26.7\ntoolchain go1.26.7", "toolchain go1.26.6"} {
		if nativePinnedGoToolchain([]byte(data)) {
			t.Fatalf("unapproved toolchain directive qualified: %q", data)
		}
	}
	if !nativePinnedGoToolchain([]byte("toolchain\tgo1.26.7 // installed pin\n")) {
		t.Fatal("valid exact module directive refused")
	}
}

func TestNativeOracleMetadataBudgetPreservesRawStreams(t *testing.T) {
	capture := &nativeStreamCapture{stdout: []byte{0xff, 0}, stderr: []byte{}, preview: bytes.Repeat([]byte{0}, defaultWorktreeVerifyBytes), complete: true}
	r := WorktreeVerifyResult{OperationRef: "native:overflow", Oracle: &NativeOracleResult{NativeOraclePreparation: NativeOraclePreparation{Qualification: "pass"}}}
	raw, err := marshalNativeVerifyRecord(&r, capture)
	if err != nil || len(raw) > 48*1024 || r.Oracle.Qualification != "pass" || !r.OutputTruncated || !r.Oracle.Stdout.Complete || r.Oracle.Stdout.Length != 2 {
		t.Fatalf("display expansion changed complete evidence: len=%d result=%+v err=%v", len(raw), r, err)
	}
	r.Oracle.Files = []NativeOracleFile{{Path: strings.Repeat("x", 49*1024)}}
	raw, err = marshalNativeVerifyRecord(&r, capture)
	if err != nil || len(raw) > 48*1024 || r.Oracle.Qualification != "unavailable" || r.Oracle.Stdout.SHA256 != nativeDigest(capture.stdout) {
		t.Fatalf("oversize mandatory inventory qualified: len=%d result=%+v err=%v", len(raw), r, err)
	}
}

func TestNativeOraclePrepareBootstrapExactJoin(t *testing.T) {
	f := newNativeOracleFixture(t)
	ctx := context.Background()
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	b := f.bundle
	b.Control.ReadinessEvidenceRefs = []string{f.prepare.OperationRef}
	oracle := &AcceptanceOracle{Owners: []OracleOwner{b.Owner}, Cases: b.Cases, Controls: []OracleControl{b.Control}}
	if err := validateAcceptanceOracleAuthorityTx(ctx, tx, f.work, 1, oracle); err != nil {
		t.Fatalf("qualified bootstrap refused: %v", err)
	}
	for _, change := range []func(*AcceptanceOracle){func(o *AcceptanceOracle) {
		o.Controls[0].ReadinessEvidenceRefs = []string{"evidence:return-route-verification"}
	}, func(o *AcceptanceOracle) { o.Owners[0].Obligation = "changed obligation" }, func(o *AcceptanceOracle) { o.Cases[0].InputClass = "changed case" }, func(o *AcceptanceOracle) { o.Controls[0].Argv[4] = "^(TestOther)$" }, func(o *AcceptanceOracle) { o.Controls[0].Cwd = "other" }, func(o *AcceptanceOracle) { o.Controls[0].RecipeSource.CommitOID = strings.Repeat("a", 40) }} {
		raw, _ := json.Marshal(oracle)
		var altered AcceptanceOracle
		_ = json.Unmarshal(raw, &altered)
		change(&altered)
		if err := validateAcceptanceOracleAuthorityTx(ctx, tx, f.work, 1, &altered); err == nil {
			t.Fatal("mismatched or arbitrary preparation qualified")
		}
	}
}

func TestNativeOracleCachePolicyAndMarshalBeforeRelease(t *testing.T) {
	f := newNativeOracleFixture(t)
	var cache string
	stages := 0
	f.s.nativeOracleStageObserved = func(name, dir string, argv, env []string) {
		stages++
		var held int
		if err := f.s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE state='held'`).Scan(&held); err != nil || held != 1 {
			t.Errorf("stage holds SQL transaction: %d %v", held, err)
		}
		for _, v := range env {
			if strings.HasPrefix(v, "GOCACHE=") {
				candidate := strings.TrimPrefix(v, "GOCACHE=")
				if cache == "" {
					cache = candidate
				} else if cache != candidate {
					t.Error("one phase uses different caches")
				}
				if !strings.Contains(candidate, "concord-native-oracle-") {
					t.Error("cache is not private lease scratch")
				}
			}
		}
	}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("cache-check", &f.a))
	if err != nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("real execution %+v %v", r, err)
	}
	if stages != 7 || cache == "" {
		t.Fatalf("observed stages=%d cache=%s", stages, cache)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("cache retained after cleanup: %v", err)
	}
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "pass"); err != nil {
		t.Fatalf("qualified native receipt rejected: %v", err)
	}
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("cache-check", &f.a)); err != nil || stages != 7 {
		t.Fatalf("replay recreated cache or stages: %d %v", stages, err)
	}
	var stdout, stderr []byte
	var metadata string
	if err := f.s.db.QueryRow(`SELECT stdout_blob,stderr_blob,result_json FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&stdout, &stderr, &metadata); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout, []byte{255, 0}) || !bytes.Contains(stderr, []byte{254, 0}) || len(stderr) <= 16384 || nativeDigest(stdout) != r.Oracle.Stdout.SHA256 || nativeDigest(stderr) != r.Oracle.Stderr.SHA256 {
		t.Fatalf("separate non-UTF-8 full streams changed: stdout=%d marker=%v hash=%v stderr=%d marker=%v hash=%v", len(stdout), bytes.Contains(stdout, []byte{255, 0}), nativeDigest(stdout) == r.Oracle.Stdout.SHA256, len(stderr), bytes.Contains(stderr, []byte{254, 0}), nativeDigest(stderr) == r.Oracle.Stderr.SHA256)
	}
	if len(metadata) > 51200 || strings.Contains(metadata, "data_base64") || strings.Contains(metadata, "stdout_blob") {
		t.Fatal("raw stream body entered bounded metadata")
	}
}

func TestNativeOracleReleaseSQLOnly(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "worktree_verify_oracle.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "releaseNativeOracle" {
			continue
		}
		found = true
		begin := token.NoPos
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if method, ok := call.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == "BeginTx" {
				begin = call.Pos()
			}
			if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "beginOrdinaryTx" {
				begin = call.Pos()
			}
			if name, ok := call.Fun.(*ast.Ident); ok && begin.IsValid() && call.Pos() > begin {
				switch name.Name {
				case "readNativeOracleAuthorizationTx", "nativeDigest", "nativeBundleDigest", "workflowJSON", "marshalNativeVerifyRecord", "readCurrentOracleSubject":
					t.Errorf("release performs metadata work inside its transaction: %s", name.Name)
				}
			}
			return true
		})
		if !begin.IsValid() {
			t.Fatal("release transaction was not inspected")
		}
	}
	if !found {
		t.Fatal("native release owner was not inspected")
	}
}

func TestNativeOracleReleaseStampBindsDependencies(t *testing.T) {
	f := newNativeOracleFixture(t)
	req := f.request("stamp", &f.a)
	before, err := readNativeOracleReleaseStamp(context.Background(), f.s.db, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.oracle.detail','changed') WHERE lease_id=?`, f.prepare.LeaseID); err != nil {
		t.Fatal(err)
	}
	after, err := readNativeOracleReleaseStamp(context.Background(), f.s.db, req)
	if err != nil || before == after || before.Preparations == after.Preparations {
		t.Fatalf("preparation change escaped release proof: %v", err)
	}
	setNativeAttemptStateFixture(t, f, f.a.AttemptID, "completed")
	terminal, err := readNativeOracleReleaseStamp(context.Background(), f.s.db, req)
	if err != nil || after == terminal || after.Attempt == terminal.Attempt {
		t.Fatalf("terminal attempt escaped release proof: %v", err)
	}
}

func TestNativeOracleLateCancellationPublishesNoGreen(t *testing.T) {
	f := newNativeOracleFixture(t)
	req := f.request("late-cancel", &f.a)
	r, err := f.s.VerifyWorktree(context.Background(), req)
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("execution fixture failed: %v", err)
	}
	var planRaw, digest string
	capture := &nativeStreamCapture{complete: true}
	if err := f.s.db.QueryRow(`SELECT native_plan_json,native_plan_sha256,stdout_blob,stderr_blob FROM worktree_verify_leases WHERE lease_id=?`, r.LeaseID).Scan(&planRaw, &digest, &capture.stdout, &capture.stderr); err != nil {
		t.Fatal(err)
	}
	var plan nativeOraclePlan
	if err := json.Unmarshal([]byte(planRaw), &plan); err != nil {
		t.Fatal(err)
	}
	req.Command = r.Command
	req.nativePlanJSON, req.nativePlanSHA256 = planRaw, digest
	req.nativeCommandJSON = workflowJSON(req.Command)
	req.nativeAcceptedInputsDigest = nativeDigest([]byte(req.nativeCommandJSON + "\x00" + digest))
	req.nativeEvidenceRefsJSON = workflowJSON([]string{r.OperationRef})
	raw, err := marshalNativeVerifyRecord(&r, capture)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := r
	unavailableOracle := *r.Oracle
	unavailable.Oracle = &unavailableOracle
	unavailable.Oracle.Qualification = "unavailable"
	unavailableRaw, err := marshalNativeVerifyRecord(&unavailable, capture)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the held release boundary with complete pre-finalized metadata.
	// Cancellation arrives after that metadata qualified, not during execution.
	if _, err := f.s.db.Exec(`DELETE FROM durable_operations WHERE op_id=?`, r.OperationRef); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`UPDATE worktree_verify_leases SET state='held',outcome='running' WHERE lease_id=?`, r.LeaseID); err != nil {
		t.Fatal(err)
	}
	entry, err := activeWorktreeEntryForProject(context.Background(), f.s.db, "test", f.work, "project")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.s.releaseNativeOracle(context.Background(), req, entry, oracleGitSubjectSnapshot{head: plan.SubjectCommit, clean: true}, plan.WorktreeIdentity, plan, r, raw, unavailable, unavailableRaw, capture, true)
	if err != nil {
		t.Fatal(err)
	}
	var green int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil {
		t.Fatal(err)
	}
	if green != 0 || result.Oracle.Qualification != "unavailable" {
		t.Fatalf("cancelled release qualified: green=%d qualification=%s", green, result.Oracle.Qualification)
	}
}

// A native run executes at most once. A concurrent duplicate that passed the
// unlocked replay read meets the held lease inside its own acquiring
// transaction and refuses, where a non-native same-owner retry resumes.
func TestNativeOracleHeldDuplicateRefusesInAcquiringTransaction(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)
	acquire := func(req WorktreeVerifyRequest) error {
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := acquireVerifyLeaseTx(context.Background(), tx, req, entry, workflowJSON(req.Command), req.Now); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	plain := verifyRequest(git, "plain-held", []string{"true"}, nil)
	if err := acquire(plain); err != nil {
		t.Fatal(err)
	}
	if err := acquire(plain); err != nil {
		t.Fatalf("non-native same-owner retry did not resume its held lease: %v", err)
	}
	released := false
	releaseAbandonedVerifyLease(s, context.Background(), plain.LeaseID, &released)
	native := verifyRequest(git, "native-held", []string{"true"}, nil)
	native.Oracle = &NativeOracleRequest{Phase: "execute", ExpectedContractVersion: 1}
	native.nativePlanJSON = `{}`
	native.nativePlanSHA256 = nativeDigest([]byte(native.nativePlanJSON))
	native.nativeLeaseOwner = currentProcessIdentity()
	if err := acquire(native); err != nil {
		t.Fatal(err)
	}
	if err := acquire(native); failureKind(err) != KindWorktreeLeaseHeld || !strings.Contains(err.Error(), "cannot be executed twice") {
		t.Fatalf("held native duplicate was not refused: %v", err)
	}
}
