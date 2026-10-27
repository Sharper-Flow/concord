package store

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNativeIsolationRecipePolicy(t *testing.T) {
	for _, raw := range []string{`{"kind":"target_runner_v1"}`, `{"kind":"target_runner_v1","isolation":"loopback_only_v1"}`} {
		if _, err := decodeNativeTargetManifest([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		`{"isolation":""}`, `{"isolation":null}`, `{"isolation":true}`, `{"isolation":{}}`,
		`{"isolation":"loopback_only_v2"}`, `{"isolation":"loopback_only_v1","mounts":[]}`,
		`{"isolation":"loopback_only_v1","isolation":""}`, `{"isolation":"loopback_only_v1"} {}`,
	} {
		if _, err := decodeNativeTargetManifest([]byte(raw)); err == nil {
			t.Fatalf("admitted %s", raw)
		}
	}
	m, c := nativeVitestManifestForTest()
	m.Isolation = nativeIsolationLoopback
	if err := validateNativeTargetManifest(m, c); err != nil {
		t.Fatal(err)
	}
	m.Isolation = "share-net"
	if err := validateNativeTargetManifest(m, c); err == nil {
		t.Fatal("unknown policy admitted")
	}
}

func TestNativeIsolationStaticLauncherRefusal(t *testing.T) {
	// A launcher-shaped script writes a marker if anyone executes it. The
	// static reader must reject it without starting even a version probe.
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	script := filepath.Join(root, "bwrap")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n# bubblewrap 0.12.0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := nativeIsolationLauncherVersion(script); err == nil {
		t.Fatal("script passed ELF identity")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("launcher identity executed a process")
	}
	// This check reads the real host file only. In particular it never runs
	// --version. CI's fixed file is admitted by the same static reader.
	err := nativeIsolationLauncherVersion(nativeIsolationLauncher)
	if data, readErr := os.ReadFile(nativeIsolationLauncher); readErr == nil && bytes.Contains(data, []byte("bubblewrap 0.11.1\x00")) && err == nil {
		t.Fatal("vulnerable host launcher passed static identity; no execution attempted")
	}
	if err == nil {
		if os.Getenv("OC_NATIVE_ISOLATION_REQUIRED") != "1" {
			t.Log("fixed launcher installed; vulnerable-file branch not applicable")
		}
		return
	}
	if os.Getenv("OC_NATIVE_ISOLATION_REQUIRED") == "1" {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(nativeIsolationLauncher); statErr != nil {
		t.Log(statErr)
		return
	}
	if !strings.Contains(err.Error(), "static proof of exactly bubblewrap 0.12.0") {
		t.Fatalf("unexpected static host refusal: %v", err)
	}
	argv := []string{"/nonexistent/workload"}
	plan := nativeIsolationTestPlan(argv)
	plan.Profile = nativeIsolationProfile
	started := false
	_, runErr := runNativeOracleStage(context.Background(), root, argv, nil, nil, &nativeStreamCapture{complete: true}, func() { started = true }, &plan)
	if runErr == nil || started || !strings.Contains(runErr.Error(), "static proof of exactly bubblewrap 0.12.0") {
		t.Fatalf("unverified launch: started=%v err=%v", started, runErr)
	}
}

func TestNativeIsolationExportAssembly(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	home := filepath.Join(root, "home")
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{snapshot, home, tmp} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	runtime := filepath.Join(root, "node")
	if err := os.WriteFile(runtime, []byte("synthetic runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	mounts := []nativeIsolationMount{{Source: runtime, Target: "/runtime/node"}, {Source: home, Target: home, Write: true}, {Source: tmp, Target: tmp, Write: true}, {Source: snapshot, Target: snapshot}}
	e := &nativeIsolationEnvironment{Policy: nativeIsolationLoopback, Profile: nativeIsolationProfile}
	argv := []string{"/runtime/node", "check.js"}
	p, err := nativeIsolationAssemble(root, snapshot, snapshot, argv, mounts, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNativeIsolation(p, argv); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(p.Argv, "\x00")
	for _, value := range []string{"--ro-bind\x00" + runtime + "\x00/runtime/node", "--ro-bind\x00" + snapshot + "\x00" + snapshot, "--bind\x00" + home + "\x00" + home, "--proc\x00/proc", "--dev\x00/dev", "--remount-ro\x00/"} {
		if !strings.Contains(text, value) {
			t.Fatalf("missing export %q", value)
		}
	}
	for _, value := range []string{"--share-net", "--as-pid-1", "--unshare-user-try", "--args", "--dev-bind", "--bind\x00/\x00/", "--ro-bind\x00/\x00/"} {
		if strings.Contains(text, value) {
			t.Fatalf("forbidden export %q", value)
		}
	}
	for name, m := range map[string]nativeIsolationMount{
		"root": {Source: "/", Target: "/"}, "usr": {Source: "/usr", Target: "/usr"},
		"source-write":          {Source: snapshot, Target: snapshot, Write: true},
		"candidate-destination": {Source: home, Target: filepath.Join(snapshot, "redirect"), Write: true},
		"host-write":            {Source: "/etc", Target: "/synthetic", Write: true},
		"host-read-tree":        {Source: "/etc", Target: "/synthetic"},
		"run-root-write":        {Source: root, Target: root, Write: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := nativeIsolationAssemble(root, snapshot, snapshot, argv, append(slices.Clone(mounts), m), e); err == nil {
				t.Fatal("unsafe mount admitted")
			}
		})
	}
	link := filepath.Join(root, "redirect")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeIsolationAssemble(root, snapshot, snapshot, argv, []nativeIsolationMount{{Source: link, Target: "/synthetic"}}, e); err == nil {
		t.Fatal("symlink source admitted")
	}
	if err := os.WriteFile(runtime, []byte("changed runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	changed, err := nativeIsolationAssemble(root, snapshot, snapshot, argv, mounts, e)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == p.Fingerprint {
		t.Fatal("export bytes not bound")
	}
}

func TestNativeIsolationEnvironmentBindings(t *testing.T) {
	e := nativeTargetEnvironment{Materializer: "bun", Runner: "vitest", RuntimeIdentity: "pinned", DependencyInputsDigest: nativeDigest([]byte("deps"))}
	finalizeNativeTargetEnvironment(&e)
	legacy := e.Digest
	want := nativeDigest([]byte(strings.Join([]string{e.Materializer, e.Runner, e.RuntimeIdentity, e.DependencyInputsDigest, nativeDigest([]byte(workflowJSON(e.Tools))), "offline-locked-no-build-no-scripts-copy-private-scratch"}, "\x00")))
	if legacy != want {
		t.Fatal("legacy environment identity changed")
	}
	e.Isolation = &nativeIsolationEnvironment{Policy: nativeIsolationLoopback, Profile: nativeIsolationProfile, Files: []NativeOracleFile{{Path: "launcher", SHA256: nativeDigest([]byte("fixed"))}}, Exports: []NativeOracleFile{{Path: "/runtime/node", SHA256: nativeDigest([]byte("node"))}}}
	finalizeNativeTargetEnvironment(&e)
	if e.Digest == legacy {
		t.Fatal("required policy not bound")
	}
	p := nativeOraclePlan{Target: &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{Isolation: nativeIsolationLoopback}, Environment: e}}
	o := &NativeOraclePreparation{IsolationPolicy: nativeIsolationLoopback, IsolationFingerprint: nativeIsolationEnvironmentDigest(e.Isolation), IsolationEnforcement: "established"}
	stages := []NativeOracleStage{{IsolationFingerprint: nativeDigest([]byte("export")), IsolationEnforced: true}}
	if !nativeIsolationResultMatches(p, o, stages) {
		t.Fatal("established producer observation refused")
	}
	for _, change := range []func(*NativeOraclePreparation){
		func(o *NativeOraclePreparation) { o.IsolationPolicy = "" }, func(o *NativeOraclePreparation) { o.IsolationFingerprint = "" },
		func(o *NativeOraclePreparation) { o.IsolationEnforcement = "unavailable" }, func(o *NativeOraclePreparation) { o.IsolationPolicy = "future" },
	} {
		copy := *o
		change(&copy)
		if nativeIsolationResultMatches(p, &copy, stages) {
			t.Fatal("legacy/changed enforcement satisfied required policy")
		}
	}
	if nativeIsolationResultMatches(p, o, nil) || nativeIsolationResultMatches(p, o, []NativeOracleStage{{}}) {
		t.Fatal("missing stage enforcement admitted")
	}
	exports := e.Isolation.Exports
	e.Isolation.Exports = nil
	if nativeIsolationPreparationMatches(p, o) {
		t.Fatal("missing export identity admitted")
	}
	e.Isolation.Exports = exports
	old := exports[0].SHA256
	exports[0].SHA256 = nativeDigest([]byte("export drift"))
	if nativeIsolationPreparationMatches(p, o) {
		t.Fatal("changed export identity admitted")
	}
	exports[0].SHA256 = old
	e.Isolation.Files[0].SHA256 = nativeDigest([]byte("drift"))
	finalizeNativeTargetEnvironment(&e)
	if nativeIsolationPreparationMatches(p, o) {
		t.Fatal("changed enforcement identity admitted")
	}
	if nativeIsolationResultMatches(nativeOraclePlan{}, o, stages) {
		t.Fatal("isolated record relabeled as legacy")
	}
}

func TestNativeIsolationELFClosure(t *testing.T) {
	tool, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	tool, err = filepath.EvalSymlinks(tool)
	if err != nil {
		t.Fatal(err)
	}
	files, err := nativeIsolationELFClosure([]string{tool})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 || !slices.Contains(files, tool) {
		t.Fatalf("incomplete interpreter/library closure: %v", files)
	}
	for _, name := range files {
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("whole directory or missing input: %s %v", name, err)
		}
	}
	if _, err := nativeIsolationELFClosure([]string{"/nonexistent/elf"}); err == nil {
		t.Fatal("missing runtime admitted")
	}
}

func TestNativeIsolationRequiredStageRefusesBeforeLaunch(t *testing.T) {
	for _, phase := range []string{"prepare", "execute"} {
		for _, name := range []string{"collect", "check_inputs", "metadata", "toolcheck", "test"} {
			t.Run(phase+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				m := nativeTargetOracleManifest{Isolation: nativeIsolationLoopback, Runner: "check"}
				target := &nativeTargetOraclePlan{Recipe: m}
				n := nativeTargetOracleRun{store: &Store{}, req: WorktreeVerifyRequest{Oracle: &NativeOracleRequest{Phase: phase}, SupportedBudgetSeconds: 1}, plan: nativeOraclePlan{Target: target}, recipe: m, root: root, snapshot: filepath.Join(root, "snapshot"), cwd: root, result: &NativeOracleResult{}, capture: &nativeStreamCapture{complete: true}}
				_, _, err := n.runStage(context.Background(), NativeOracleStage{Name: name, Argv: []string{"/nonexistent/workload"}})
				if err == nil || len(n.result.Stages) != 0 {
					t.Fatalf("required stage reached launch: %v", err)
				}
			})
		}
	}
}

func TestNativeIsolationReportSlot(t *testing.T) {
	m, _ := nativeVitestManifestForTest()
	m.Isolation = nativeIsolationLoopback
	target := &nativeTargetOraclePlan{Recipe: m, Environment: nativeTargetEnvironment{Node: "/runtime/node"}}
	stages, err := target.stages("prepare", nil)
	if err != nil {
		t.Fatal(err)
	}
	argv := nativeTargetStageArgv(target, "/synthetic/scratch", stages[0].Argv)
	if argv[4] != "/synthetic/scratch/reports/vitest-collect.json" {
		t.Fatal(argv)
	}
	if argv[1] != "/synthetic/scratch/vitest-runner.mjs" || argv[3] != "/synthetic/scratch/vitest-plan.json" {
		t.Fatal(argv)
	}
}

func TestNativeIsolationCleanupProofIsNotOutputCompleteness(t *testing.T) {
	capture := &nativeStreamCapture{complete: false}
	nativeIsolationRecordProof(capture, nativeSupervisorResult{Established: true, Reaped: true})
	if capture.complete || !capture.isolationEstablished || !capture.isolationReaped {
		t.Fatal("overflow erased teardown proof or was relabeled complete")
	}
	for _, result := range []nativeSupervisorResult{{Established: true}, {Reaped: true}, {Established: true, Reaped: true, Detail: "lost cleanup"}} {
		capture := &nativeStreamCapture{complete: true}
		nativeIsolationRecordProof(capture, result)
		if capture.isolationReaped || capture.isolationEstablished {
			t.Fatal("partial cleanup qualified")
		}
	}
	// A capture is shared across stages. A previous stage's proof cannot
	// qualify an aborted later stage whose supervisor never established setup.
	_, err := runNativeIsolatedStage(context.Background(), t.TempDir(), []string{"/nonexistent"}, nil, nil, capture, nil, &nativeStageIsolation{})
	if err == nil || capture.isolationReaped || capture.isolationEstablished {
		t.Fatal("previous stage teardown leaked into failed stage")
	}
}

func TestNativeIsolationExportIdentityFiles(t *testing.T) {
	t.Setenv("LANG", "C")
	t.Setenv("LC_ALL", "C")
	root := t.TempDir()
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(snapshot, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "node_modules", "candidate.node"), []byte{0x7f, 'E', 'L', 'F'}, 0600); err != nil {
		t.Fatal(err)
	}
	bash, err := nativeTargetResolvedTool("bash")
	if err != nil {
		t.Fatal(err)
	}
	target := &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck, Isolation: nativeIsolationLoopback}, Environment: nativeTargetEnvironment{Bash: bash, Isolation: &nativeIsolationEnvironment{Policy: nativeIsolationLoopback, Profile: nativeIsolationProfile}}}
	if err := nativeIsolationBindTargetExports(target, snapshot); err != nil {
		t.Fatal(err)
	}
	p, err := nativeIsolationBuildExport(target, root, snapshot, snapshot, []string{bash, "-n", "script.sh"})
	if err != nil {
		t.Fatal(err)
	}
	passwd, err := os.ReadFile(filepath.Join(root, "identity", "passwd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(passwd), ":sandbox:"+filepath.Join(root, "home")+":/nonexistent\n") || bytes.Count(passwd, []byte("\n")) != 1 {
		t.Fatalf("host or incomplete passwd: %s", passwd)
	}
	nss, err := os.ReadFile(filepath.Join(root, "identity", "nsswitch.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(nss), "dns") || strings.Contains(string(nss), "systemd") {
		t.Fatal("host name-service bridge exported")
	}
	group, err := os.ReadFile(filepath.Join(root, "identity", "group"))
	if err != nil || !strings.HasPrefix(string(group), "sandbox:x:") || bytes.Count(group, []byte("\n")) != 1 {
		t.Fatalf("host group exported: %s %v", group, err)
	}
	for _, name := range []string{"passwd", "group", "hosts", "nsswitch.conf"} {
		if !strings.Contains(strings.Join(p.Argv, "\x00"), "--ro-bind\x00"+filepath.Join(root, "identity", name)+"\x00/etc/"+name) {
			t.Fatalf("identity input is not read-only: %s", name)
		}
	}
	for _, name := range []string{"home", "tmp", "reports", "generated", "bun-cache"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("missing private state %s: %v", name, err)
		}
	}
	target.Environment.Isolation.Exports[0].SHA256 = nativeDigest([]byte("changed"))
	if _, err := nativeIsolationBuildExport(target, root, snapshot, snapshot, []string{bash, "-n", "script.sh"}); err == nil {
		t.Fatal("changed runtime exports reused prepared readiness")
	}
}

func TestNativeIsolationUserIdentity(t *testing.T) {
	if err := validateNativeIsolationCredentials(1000, 1000, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][4]int{{0, 0, 0, 0}, {1000, 0, 1000, 1000}, {1000, 1000, 1000, 0}} {
		if err := validateNativeIsolationCredentials(ids[0], ids[1], ids[2], ids[3]); err == nil {
			t.Fatal("privileged identity admitted")
		}
	}
}

func TestNativeIsolationELFLibraryRoots(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "library.so")
	if err := os.WriteFile(name, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := nativeIsolationLibraryAllowed(name, nil); err == nil {
		t.Fatal("undeclared host library admitted")
	}
	if err := nativeIsolationLibraryAllowed(name, []string{root}); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	link := filepath.Join(private, "escape.so")
	if err := os.Symlink(name, link); err != nil {
		t.Fatal(err)
	}
	if err := nativeIsolationLibraryAllowed(link, []string{private}); err == nil {
		t.Fatal("library symlink escaped verified tree")
	}
}

func TestNativeIsolationVerifiedArtifactExports(t *testing.T) {
	m, _ := nativeVitestManifestForTest()
	m.Isolation = nativeIsolationLoopback
	raw := map[string][]byte{}
	for _, name := range []string{m.Pin.DepManifest, m.Pin.DepLock} {
		data, err := os.ReadFile(filepath.Join("testdata", "native-vitest", name))
		if err != nil {
			t.Fatal(err)
		}
		raw[name] = data
	}
	installs, err := nativeBunPlanInstalls(raw[m.Pin.DepManifest], raw[m.Pin.DepLock])
	if err != nil {
		t.Fatal(err)
	}
	cache, err := nativeBunCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if configured := os.Getenv("OC_NATIVE_NPM_CACHE"); configured != "" {
		cache = filepath.Join(configured, "_cacache")
	}
	if err := nativeBunMissing(cache, installs); err != nil {
		if os.Getenv("OC_NATIVE_ISOLATION_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	bash, err := nativeTargetResolvedTool("bash")
	if err != nil {
		t.Fatal(err)
	}
	target := &nativeTargetOraclePlan{Recipe: m, Environment: nativeTargetEnvironment{Bun: bash, Node: bash, NPMCacheDir: cache, Isolation: &nativeIsolationEnvironment{Policy: nativeIsolationLoopback, Profile: nativeIsolationProfile}}}
	for _, name := range []string{m.Pin.DepManifest, m.Pin.DepLock} {
		target.DependencyFiles = append(target.DependencyFiles, NativeOracleFile{Path: filepath.ToSlash(filepath.Join(m.Root, name)), SHA256: nativeDigest(raw[name])})
	}
	if err := qualifyNativeIsolationArtifacts(context.Background(), target, raw); err != nil {
		t.Fatal(err)
	}
	want := nativeIsolationEnvironmentDigest(target.Environment.Isolation)
	if len(target.Environment.Isolation.Exports) < 2 {
		t.Fatal("missing verified runtime/library/data inventory")
	}
	for _, f := range target.Environment.Isolation.Exports {
		if strings.Contains(f.Path, "concord-native-export-inventory-") {
			t.Fatal("temporary artifact path entered immutable environment")
		}
		observed, err := nativeIsolationFile(f.Path)
		if err != nil || observed != f {
			t.Fatalf("unbound export: %s %v", f.Path, err)
		}
	}
	if err := qualifyNativeIsolationArtifacts(context.Background(), target, raw); err != nil {
		t.Fatal(err)
	}
	if nativeIsolationEnvironmentDigest(target.Environment.Isolation) != want {
		t.Fatal("per-run scratch changed immutable runtime export identity")
	}
}

func TestNativeIsolationArchiveLibcSelection(t *testing.T) {
	for _, raw := range []string{`{}`, `{"libc":"glibc"}`, `{"libc":["glibc","musl"]}`, `{"libc":["!musl"]}`} {
		selected, err := nativeIsolationPackageSupportsGlibc([]byte(raw))
		if err != nil || !selected {
			t.Fatalf("selected archive refused: %s %v", raw, err)
		}
	}
	for _, raw := range []string{`{"libc":"musl"}`, `{"libc":["!glibc"]}`} {
		selected, err := nativeIsolationPackageSupportsGlibc([]byte(raw))
		if err != nil || selected {
			t.Fatalf("incompatible archive selected: %s %v", raw, err)
		}
	}
	for _, raw := range []string{`null`, `{"libc":null}`, `{"libc":12}`, `{"libc":"other"}`} {
		if _, err := nativeIsolationPackageSupportsGlibc([]byte(raw)); err == nil {
			t.Fatalf("malformed libc metadata admitted: %s", raw)
		}
	}
}

func TestNativeIsolationProfileSourceIdentity(t *testing.T) {
	if got := nativeIsolationVendorProfileDigest; got != "sha256:11d39094f044f0cda0febb3ad517b830301da6b2ce929664af09ee9e4dd264f9" {
		t.Fatalf("pinned vendor source bytes differ: %s", got)
	}
	if nativeIsolationProfile != "bwrap (enforce)" {
		t.Fatal("pre-exec profile does not match vendor attachment and enforce mode")
	}
}
