package store

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeTargetTestManifest() (nativeTargetOracleManifest, OracleControl) {
	m := nativeTargetOracleManifest{
		Kind:         nativeTargetKind,
		Materializer: nativeTargetMaterializerUV,
		Runner:       nativeTargetRunnerPytest,
		Root:         "pyfix",
		HarnessFiles: []string{"pyfix/tests/test_value.py", "pyfix/pytest.ini"},
		SupportFiles: []string{},
		Cases:        map[string][]string{"case:fixture": {"tests/test_value.py::test_value"}},
		Pin:          nativeTargetPin{Python: "3.13.7", UV: "0.11.26", Pytest: "9.1.1", DepManifest: "pyproject.toml", DepLock: "uv.lock"},
	}
	c := OracleControl{CaseIDs: []string{"case:fixture"}, Argv: []string{"pytest", "tests/test_value.py::test_value"}, Cwd: "pyfix"}
	return m, c
}

// The admitted matrix is exactly three pairs; every other combination,
// including unknown members, refuses as one closed decision.
func TestNativeTargetAxisPairMatrix(t *testing.T) {
	for _, materializer := range []string{nativeTargetMaterializerNone, nativeTargetMaterializerUV, "future"} {
		for _, runner := range []string{nativeTargetRunnerPytest, nativeTargetRunnerCheck, "jest"} {
			m, c := nativeTargetTestManifest()
			m.Materializer, m.Runner = materializer, runner
			_, err := nativeTargetPairOf(m)
			admitted := (materializer == nativeTargetMaterializerUV && runner == nativeTargetRunnerPytest) ||
				(materializer == nativeTargetMaterializerNone && runner == nativeTargetRunnerCheck) ||
				(materializer == nativeTargetMaterializerUV && runner == nativeTargetRunnerCheck)
			if admitted != (err == nil) {
				t.Fatalf("pair %s+%s admitted=%v err=%v", materializer, runner, admitted, err)
			}
			if admitted {
				continue
			}
			if err := validateNativeTargetManifest(m, c); err == nil {
				t.Fatalf("unsupported pair %s+%s was admitted by manifest validation", materializer, runner)
			}
		}
	}
	// Each admitted pair passes its own pin projection; swapping pins
	// across pairs refuses.
	uvPytest, c := nativeTargetTestManifest()
	if err := validateNativeTargetManifest(uvPytest, c); err != nil {
		t.Fatalf("valid uv+pytest manifest refused: %v", err)
	}
	noneCheck := nativeTargetOracleManifest{
		Kind: nativeTargetKind, Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck,
		Root:         "chkfix",
		HarnessFiles: []string{"chkfix/scripts/check.sh"},
		SupportFiles: []string{},
		Cases:        map[string][]string{"case:fixture": {"shell-value"}},
		Checks:       []nativeTargetCheck{{Witness: "shell-value", Argv: []string{"bash", "scripts/check.sh", "--strict"}}},
		Pin:          nativeTargetPin{Shell: "5.3.9"},
	}
	noneCheckControl := OracleControl{CaseIDs: []string{"case:fixture"}, Argv: []string{"check", "shell-value"}, Cwd: "chkfix"}
	if err := validateNativeTargetManifest(noneCheck, noneCheckControl); err != nil {
		t.Fatalf("valid none+check manifest refused: %v", err)
	}
	uvCheck := deepCopyNativeTargetManifest(t, noneCheck)
	uvCheck.Materializer = nativeTargetMaterializerUV
	uvCheck.Pin = nativeTargetPin{Python: "3.13.7", UV: "0.11.26", Shell: "5.3.9", DepManifest: "pyproject.toml", DepLock: "uv.lock"}
	uvCheck.Checks = append(uvCheck.Checks, nativeTargetCheck{Witness: "deps", Argv: []string{"python3", "scripts/check.py"}})
	uvCheck.HarnessFiles = append(uvCheck.HarnessFiles, "chkfix/scripts/check.py")
	uvCheck.Cases["case:fixture"] = []string{"deps", "shell-value"}
	noneCheckControl.Argv = []string{"check", "deps", "shell-value"}
	if err := validateNativeTargetManifest(uvCheck, noneCheckControl); err != nil {
		t.Fatalf("valid uv+check manifest refused: %v", err)
	}
}

// A root of "." names the repository root itself, so contained manifest
// paths are the plain repository paths. Containment must still refuse
// traversal, absolute, and tmp-scratch names under either root spelling.
func TestNativeTargetAxisRootDotManifest(t *testing.T) {
	m, c := nativeTargetTestManifest()
	m.Root = "."
	m.HarnessFiles = []string{"tests/test_value.py", "pytest.ini"}
	c.Cwd = "."
	if err := validateNativeTargetManifest(m, c); err != nil {
		t.Fatalf("root '.' pytest manifest refused: %v", err)
	}
	witnesses, err := nativeTargetSelector(m, c)
	if err != nil || !slicesEqualStrings(witnesses, []string{"tests/test_value.py::test_value"}) {
		t.Fatalf("root '.' selector %v err=%v", witnesses, err)
	}
	none := nativeTargetOracleManifest{
		Kind: nativeTargetKind, Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck,
		Root:         ".",
		HarnessFiles: []string{"scripts/check.sh"},
		SupportFiles: []string{},
		Cases:        map[string][]string{"case:fixture": {"shell-value"}},
		Checks:       []nativeTargetCheck{{Witness: "shell-value", Argv: []string{"bash", "scripts/check.sh", "--strict"}}},
		Pin:          nativeTargetPin{Shell: "5.3.9"},
	}
	noneC := OracleControl{CaseIDs: []string{"case:fixture"}, Argv: []string{"check", "shell-value"}, Cwd: "."}
	if err := validateNativeTargetManifest(none, noneC); err != nil {
		t.Fatalf("root '.' check manifest refused: %v", err)
	}
	for _, root := range []string{".", "pyfix"} {
		for name, harness := range map[string][]string{
			"traversal": {"../etc/passwd"},
			"absolute":  {"/etc/passwd"},
			"tmp":       {"tmp/scratch.txt"},
			"self":      {"./tests/test_value.py"},
		} {
			bad := m
			bad.Root = root
			bad.HarnessFiles = harness
			if err := validateNativeTargetManifest(bad, c); err == nil {
				t.Fatalf("escaping harness %s admitted under root %q", name, root)
			}
		}
		bad := m
		bad.Root = root
		bad.Cases = map[string][]string{"case:fixture": {"../etc/passwd::test_x"}}
		if err := validateNativeTargetManifest(bad, c); err == nil {
			t.Fatalf("traversing witness admitted under root %q", root)
		}
	}
}

// deepCopyNativeTargetManifest round-trips one manifest through the strict
// closed decoder so each table case mutates its own fixture. A shallow
// struct copy shares slice and map backing arrays, so one invalid case
// poisons every later case in the table.
func deepCopyNativeTargetManifest(t *testing.T, m nativeTargetOracleManifest) nativeTargetOracleManifest {
	t.Helper()
	var out nativeTargetOracleManifest
	d := json.NewDecoder(strings.NewReader(workflowJSON(m)))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		t.Fatalf("fixture round-trip failed: %v", err)
	}
	return out
}

// nativeTargetControlDefinition returns one otherwise-valid public control
// definition whose cwd the table varies. Every earlier field passes its own
// closed validation, so a refusal names the cwd and nothing else.
func nativeTargetControlDefinition(cwd string) OracleControl {
	return OracleControl{
		ControlID:            "control:cwd-table",
		OwnerID:              "owner:pinned-target",
		PredicateIDs:         []string{"predicate:target-value"},
		CaseIDs:              []string{"case:fixture"},
		RecipeSource:         WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "pyfix/recipe.json", CommitOID: strings.Repeat("a", 40)},
		Argv:                 []string{"pytest", "tests/test_value.py::test_value"},
		Cwd:                  cwd,
		ExpectedResult:       OracleExpectedResultPass,
		RequiredEvidenceRole: OracleEvidenceRoleReported,
	}
}

// The public control definition admits exactly the repository root and
// normalized contained relative directories for cwd, and refuses every
// other shape with the contained-directory payload failure. The
// file-selector validator keeps refusing ".": directory-role admission
// does not change the file path owner.
func TestNativeTargetAxisControlCwd(t *testing.T) {
	for _, tc := range []struct{ name, cwd string }{
		{"repository_root", "."},
		{"nested_directory", "a/b"},
		{"multibyte_512_bytes", strings.Repeat("é", 256)},
	} {
		t.Run("accepted/"+tc.name, func(t *testing.T) {
			if err := validateOracleControlDefinition(nativeTargetControlDefinition(tc.cwd)); err != nil {
				t.Fatalf("control cwd %q refused: %v", tc.cwd, err)
			}
		})
	}
	for name, cwd := range map[string]string{
		"empty":          "",
		"absolute":       "/pyfix",
		"leading_dot":    "./pyfix",
		"trailing_slash": "pyfix/",
		"self_segment":   "pyfix/./tests",
		"parent_segment": "pyfix/../tests",
		"empty_segment":  "pyfix//tests",
		"leading_tmp":    "tmp/scratch",
		"exact_tmp":      "tmp",
		"tmp_slash":      "tmp/",
		"delete_char":    "pyfix\x7f",
		"over_512_bytes": strings.Repeat("é", 256) + "x",
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			err := validateOracleControlDefinition(nativeTargetControlDefinition(cwd))
			assertFailureKind(t, err, KindInvalidPayload)
			if !strings.Contains(err.Error(), "cwd must be a contained relative directory") {
				t.Fatalf("cwd %q refused with the wrong message: %v", cwd, err)
			}
		})
	}
	if err := validateWorkContextRepoPath("."); err == nil {
		t.Fatal("file-selector validator admitted '.'")
	}
}

func TestNativeTargetAxisManifestValidation(t *testing.T) {
	m, c := nativeTargetTestManifest()
	if err := validateNativeTargetManifest(m, c); err != nil {
		t.Fatalf("valid manifest refused: %v", err)
	}
	before := workflowJSON(m)
	for _, tc := range []struct {
		name   string
		mutate func(*nativeTargetOracleManifest)
	}{
		{"escaping_file", func(m *nativeTargetOracleManifest) { m.HarnessFiles[0] = "../etc/passwd" }},
		{"empty_witness", func(m *nativeTargetOracleManifest) { m.Cases["case:fixture"] = []string{""} }},
		{"foreign_family", func(m *nativeTargetOracleManifest) { m.Kind = "other" }},
		{"root_mismatch", func(m *nativeTargetOracleManifest) { m.Root = "other" }},
		{"empty_harness", func(m *nativeTargetOracleManifest) { m.HarnessFiles = nil }},
		{"missing_pytest_pin", func(m *nativeTargetOracleManifest) { m.Pin.Pytest = "" }},
		{"foreign_shell_pin", func(m *nativeTargetOracleManifest) { m.Pin.Shell = "5.3.9" }},
		{"wrong_dep_lock", func(m *nativeTargetOracleManifest) { m.Pin.DepLock = "package-lock.json" }},
		{"nonnumeric_pin", func(m *nativeTargetOracleManifest) { m.Pin.UV = "latest" }},
		{"unpinned_witness", func(m *nativeTargetOracleManifest) { m.Cases["case:fixture"] = []string{"tests/other.py::test_x"} }},
		{"nine_witnesses", func(m *nativeTargetOracleManifest) {
			m.Cases["case:fixture"] = []string{"a::1", "a::2", "a::3", "a::4", "a::5", "a::6", "a::7", "a::8", "a::9"}
		}},
		{"extra_case", func(m *nativeTargetOracleManifest) {
			m.Cases["case:extra"] = []string{"tests/test_value.py::test_value"}
		}},
		{"oversize_witness", func(m *nativeTargetOracleManifest) {
			m.Cases["case:fixture"] = []string{"tests/test_value.py::" + strings.Repeat("x", 256)}
		}},
		{"missing_case", func(m *nativeTargetOracleManifest) { delete(m.Cases, "case:fixture") }},
		{"checks_on_pytest", func(m *nativeTargetOracleManifest) {
			m.Checks = []nativeTargetCheck{{Witness: "w", Argv: []string{"bash", "x.sh"}}}
		}},
		{"support_escaping", func(m *nativeTargetOracleManifest) { m.SupportFiles = []string{"pyfix/../escape.txt"} }},
		{"superseded_driver", func(m *nativeTargetOracleManifest) { m.Pin.DepManifest = "package.json" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := deepCopyNativeTargetManifest(t, m)
			tc.mutate(&bad)
			if err := validateNativeTargetManifest(bad, c); err == nil {
				t.Fatal("malformed manifest was admitted")
			}
		})
	}
	// Each table case mutates only its own fixture: a leaked mutation would
	// poison every later case in the table.
	if workflowJSON(m) != before {
		t.Fatal("table case mutation leaked into the base fixture")
	}
}

func TestNativeTargetAxisCheckArgv(t *testing.T) {
	none := nativeTargetOracleManifest{
		Kind: nativeTargetKind, Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck,
		Root:         "chkfix",
		HarnessFiles: []string{"chkfix/scripts/check.sh"},
		SupportFiles: []string{},
		Cases:        map[string][]string{"case:fixture": {"shell-value"}},
		Checks:       []nativeTargetCheck{{Witness: "shell-value", Argv: []string{"bash", "scripts/check.sh", "--strict"}}},
		Pin:          nativeTargetPin{Shell: "5.3.9"},
	}
	control := OracleControl{CaseIDs: []string{"case:fixture"}, Argv: []string{"check", "shell-value"}, Cwd: "chkfix"}
	if err := validateNativeTargetManifest(none, control); err != nil {
		t.Fatalf("valid check manifest refused: %v", err)
	}
	before := workflowJSON(none)
	for _, tc := range []struct {
		name   string
		mutate func(*nativeTargetOracleManifest)
	}{
		{"missing_arg_file", func(m *nativeTargetOracleManifest) { m.Checks[0].Argv[1] = "scripts/other.sh" }},
		{"scratch_slot", func(m *nativeTargetOracleManifest) { m.Checks[0].Argv = append(m.Checks[0].Argv, "$scratch/x") }},
		{"inline_option_slot", func(m *nativeTargetOracleManifest) { m.Checks[0].Argv = []string{"bash", "-c", "echo hi"} }},
		{"foreign_interpreter", func(m *nativeTargetOracleManifest) { m.Checks[0].Argv[0] = "zsh" }},
		{"no_checks", func(m *nativeTargetOracleManifest) { m.Checks = nil }},
		{"duplicate_witness", func(m *nativeTargetOracleManifest) {
			m.Checks = append(m.Checks, nativeTargetCheck{Witness: "shell-value", Argv: []string{"bash", "scripts/check.sh"}})
		}},
		{"undeclared_selected", func(m *nativeTargetOracleManifest) { m.Cases["case:fixture"] = []string{"shell-value", "other"} }},
		{"unselected_check", func(m *nativeTargetOracleManifest) { m.Checks[0].Witness = "other" }},
		{"unpinned_interpreter", func(m *nativeTargetOracleManifest) { m.Pin.Shell = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := deepCopyNativeTargetManifest(t, none)
			tc.mutate(&bad)
			if err := validateNativeTargetManifest(bad, control); err == nil {
				t.Fatalf("malformed check manifest %s was admitted", tc.name)
			}
		})
	}
	// Each table case mutates only its own fixture.
	if workflowJSON(none) != before {
		t.Fatal("table case mutation leaked into the base fixture")
	}
	// Seventeen argv values exceed the bounded shape.
	tooMany := deepCopyNativeTargetManifest(t, none)
	tooMany.Checks[0].Argv = []string{"bash", "scripts/check.sh"}
	for i := 0; i < 16; i++ {
		tooMany.Checks[0].Argv = append(tooMany.Checks[0].Argv, "--flag")
	}
	if err := validateNativeTargetManifest(tooMany, control); err == nil {
		t.Fatal("oversize check argv was admitted")
	}
}

// The check argv is structural: the closed interpreter slot, then the one
// declared pinned harness script, then literal bounded data arguments. An
// interpreter option, inline program, or undeclared program can never
// occupy the script slot, and literal data arguments are never refused or
// rewritten for reading like a slot name.
func TestNativeTargetAxisCheckArgvStructure(t *testing.T) {
	none := nativeTargetOracleManifest{
		Kind: nativeTargetKind, Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck,
		Root:         "chkfix",
		HarnessFiles: []string{"chkfix/scripts/check.sh"},
		SupportFiles: []string{},
		Cases:        map[string][]string{"case:fixture": {"shell-value"}},
		Checks:       []nativeTargetCheck{{Witness: "shell-value", Argv: []string{"bash", "scripts/check.sh", "--expected", "7"}}},
		Pin:          nativeTargetPin{Shell: "5.3.9"},
	}
	control := OracleControl{CaseIDs: []string{"case:fixture"}, Argv: []string{"check", "shell-value"}, Cwd: "chkfix"}
	// Literal data arguments that read like slot names are ordinary bytes.
	ok := none
	ok.Checks = []nativeTargetCheck{{Witness: "shell-value", Argv: []string{"bash", "scripts/check.sh", "--expected", "7", "pytest", "uv", "python3"}}}
	if err := validateNativeTargetManifest(ok, control); err != nil {
		t.Fatalf("literal data arguments refused: %v", err)
	}
	for name, argv := range map[string][]string{
		"inline_code":        {"python3", "-c", "import os; os.system('touch x')"},
		"interpreter_option": {"bash", "--version"},
		"zero_stage":         {"python3", "--version"},
		"unpinned_program":   {"bash", "scripts/run"},
		"undeclared_script":  {"bash", "scripts/other.sh"},
		"bare_interpreter":   {"bash"},
	} {
		bad := none
		bad.Checks = []nativeTargetCheck{{Witness: "shell-value", Argv: argv}}
		if err := validateNativeTargetManifest(bad, control); err == nil {
			t.Fatalf("non-structural check argv %s was admitted: %v", name, argv)
		}
	}
}

// Stage resolution owns only the executable position: literal data
// arguments keep their exact bytes, and producer-owned scratch
// substitution still applies to its own closed stage arguments.
func TestNativeTargetAxisStageArgvLiteralArgs(t *testing.T) {
	p := &nativeTargetOraclePlan{Environment: nativeTargetEnvironment{Materializer: nativeTargetMaterializerNone, Bash: "/bin/bash", Python: "/usr/bin/python3", UV: "/bin/uv"}}
	argv := nativeTargetStageArgv(p, "/scratch", []string{"bash", "scripts/check.sh", "--expected", "7", "pytest", "uv", "python3"})
	want := []string{"/bin/bash", "scripts/check.sh", "--expected", "7", "pytest", "uv", "python3"}
	if !slicesEqualStrings(argv, want) {
		t.Fatalf("literal data arguments were rewritten: %v", argv)
	}
	staged := nativeTargetStageArgv(p, "/scratch", []string{"pytest", "--junitxml=$scratch/junit-0.xml"})
	if !slicesEqualStrings(staged, []string{"/scratch/venv/bin/pytest", "--junitxml=/scratch/junit-0.xml"}) {
		t.Fatalf("producer scratch substitution lost: %v", staged)
	}
}

// The input digest covers every regular snapshot input: a normal Project's
// docs and data files are candidate inputs with their own identity, not a
// refused extension class. A mutated input moves the digest, so the
// before/after gate can never bless a changed input; producer-owned scratch
// trees refuse while strict and skip after materialization; symlinks and a
// Node remnant get no special authority either way.
func TestNativeTargetAxisInputDigestPlainRepo(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"tests/test_value.py": "def test_value():\n    pass\n",
		"pytest.ini":          "[pytest]\n",
		"README.md":           "# pyfix\n",
		"assets/values.txt":   "expected=7\n",
		"app/value.py":        "VALUE = 7\n",
	}
	for name, data := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := nativeOraclePlan{Files: []NativeOracleFile{
		{Path: "tests/test_value.py", SHA256: nativeDigest([]byte(files["tests/test_value.py"]))},
		{Path: "pytest.ini", SHA256: nativeDigest([]byte(files["pytest.ini"]))},
	}, Target: &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{Kind: nativeTargetKind, Materializer: nativeTargetMaterializerUV, Runner: nativeTargetRunnerPytest, Root: "."}}}
	before, err := nativeTargetInputDigest(p, dir, true)
	if err != nil {
		t.Fatalf("plain repository inputs refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# pyfix\n\ngrown\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := nativeTargetInputDigest(p, dir, true)
	if err != nil || after == before {
		t.Fatalf("mutated input did not move the digest: %v %v", after, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".venv", "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetInputDigest(p, dir, true); err == nil {
		t.Fatal("strict digest admitted a materialized scratch tree")
	}
	if _, err := nativeTargetInputDigest(p, dir, false); err != nil {
		t.Fatalf("post-materialization digest refused its own scratch: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "left"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "left", "index.js"), []byte("export {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetInputDigest(p, dir, false); err != nil {
		t.Fatalf("Node remnant carried scratch-dir authority: %v", err)
	}
	if err := os.Symlink("tests/test_value.py", filepath.Join(dir, "link.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetInputDigest(p, dir, false); err == nil {
		t.Fatal("symlinked input admitted")
	}
}

// The runner closure digest covers the materialized dependency closure, not
// only the entry points: an installed dependency change moves the digest,
// and a symlinked dependency file refuses instead of hiding from the walk.
func TestNativeTargetAxisDependencyClosureDigest(t *testing.T) {
	root := t.TempDir()
	venv := filepath.Join(root, "venv")
	for _, dir := range []string{
		filepath.Join(venv, "bin"),
		filepath.Join(venv, "lib", "python3.13", "site-packages", "tinyfix"),
		filepath.Join(venv, "lib", "python3.13", "site-packages", "tinyfix-1.0.0.dist-info"),
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte("VALUE = 7\n")
	for path, data := range map[string][]byte{
		filepath.Join(venv, "bin", "pytest"):                                                           []byte("#!/bin/sh\n"),
		filepath.Join(venv, "bin", "python"):                                                           []byte("ELF\n"),
		filepath.Join(venv, "lib", "python3.13", "site-packages", "tinyfix", "__init__.py"):            payload,
		filepath.Join(venv, "lib", "python3.13", "site-packages", "tinyfix-1.0.0.dist-info", "RECORD"): []byte("tinyfix/__init__.py,,\n"),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	tPlan := &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{Kind: nativeTargetKind, Materializer: nativeTargetMaterializerUV, Runner: nativeTargetRunnerPytest}, Environment: nativeTargetEnvironment{Materializer: nativeTargetMaterializerUV, Runner: nativeTargetRunnerPytest}}
	before, err := nativeTargetRunnerDigest(tPlan, root)
	if err != nil {
		t.Fatalf("closure digest refused a clean venv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(venv, "lib", "python3.13", "site-packages", "tinyfix", "__init__.py"), []byte("VALUE = 8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := nativeTargetRunnerDigest(tPlan, root)
	if err != nil || after == before {
		t.Fatalf("installed dependency change did not move the closure digest: %v %v", after, err)
	}
	if err := os.Symlink("tinyfix/__init__.py", filepath.Join(venv, "lib", "python3.13", "site-packages", "tinyfix", "link.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeTargetRunnerDigest(tPlan, root); err == nil {
		t.Fatal("symlinked dependency closure entry admitted")
	}
	// The none materializer consumes no site-packages closure.
	none := &nativeTargetOraclePlan{Recipe: nativeTargetOracleManifest{Kind: nativeTargetKind, Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck}, Environment: nativeTargetEnvironment{Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck}}
	if _, err := nativeTargetRunnerDigest(none, root); err != nil {
		t.Fatalf("none materializer refused without a venv: %v", err)
	}
}

func TestNativeTargetAxisSelector(t *testing.T) {
	m, c := nativeTargetTestManifest()
	witnesses, err := nativeTargetSelector(m, c)
	if err != nil || !slicesEqualStrings(witnesses, []string{"tests/test_value.py::test_value"}) {
		t.Fatalf("selector %v err=%v", witnesses, err)
	}
	c.Argv = []string{"pytest", "tests/other.py::test_value"}
	if _, err := nativeTargetSelector(m, c); err == nil {
		t.Fatal("mismatching control argv was admitted")
	}
	// Sixteen argv values are the ceiling: seventeen refuse.
	var ids []string
	for i := 0; i < 17; i++ {
		ids = append(ids, "tests/test_value.py::test_"+string(rune('a'+i)))
	}
	big := m
	big.Cases = map[string][]string{"case:a": ids[:9], "case:b": ids[9:]}
	bigC := OracleControl{CaseIDs: []string{"case:a", "case:b"}, Argv: append([]string{"pytest"}, ids...), Cwd: "pyfix"}
	if _, err := nativeTargetSelector(big, bigC); err == nil {
		t.Fatal("oversize selector was admitted")
	}
	// The check selector is the logical identity over sorted witnesses.
	chk := nativeTargetOracleManifest{
		Kind: nativeTargetKind, Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck,
		Root: "chkfix", HarnessFiles: []string{"chkfix/scripts/check.sh"},
		Cases: map[string][]string{"case:fixture": {"shell-value", "alpha"}},
		Checks: []nativeTargetCheck{
			{Witness: "shell-value", Argv: []string{"bash", "scripts/check.sh"}},
			{Witness: "alpha", Argv: []string{"python3", "scripts/check.py"}},
		},
		Pin: nativeTargetPin{Shell: "5.3.9", Python: "3.13.7"},
	}
	chkC := OracleControl{CaseIDs: []string{"case:fixture"}, Argv: []string{"check", "alpha", "shell-value"}, Cwd: "chkfix"}
	if _, err := nativeTargetSelector(chk, chkC); err != nil {
		t.Fatalf("valid check selector refused: %v", err)
	}
	chkC.Argv = []string{"check", "shell-value", "alpha"}
	if _, err := nativeTargetSelector(chk, chkC); err == nil {
		t.Fatal("unsorted check selector was admitted")
	}
}

func slicesEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNativeTargetAxisPytestCollectParser(t *testing.T) {
	good := []byte("tests/test_value.py::test_value\n\n1 test collected in 0.01s\n")
	collected, err := nativeTargetPytestCollectOutput(good)
	if err != nil || !slicesEqualStrings(collected, []string{"tests/test_value.py::test_value"}) {
		t.Fatalf("collect %v err=%v", collected, err)
	}
	for name, bad := range map[string]string{
		"no_summary":     "tests/test_value.py::test_value\n",
		"count_mismatch": "tests/a.py::a\ntests/b.py::b\n\n1 test collected in 0.01s\n",
		"non_id_line":    "hello\n\n1 test collected in 0.01s\n",
		"empty":          "",
		"error_line":     "ERROR tests/test_value.py\n\n1 test collected in 0.01s\n",
	} {
		if _, err := nativeTargetPytestCollectOutput([]byte(bad)); err == nil {
			t.Fatalf("malformed collection %s was admitted", name)
		}
	}
}

func TestNativeTargetAxisJUnitGate(t *testing.T) {
	dir := t.TempDir()
	argv := []string{"pytest", "tests/test_value.py::test_value", "--junitxml=$scratch/junit-0.xml"}
	stdout := []byte(".                                                                        [100%]\n1 passed in 0.01s\n")
	writeJUnit := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "junit-0.xml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJUnit(`<testsuites><testsuite name="pytest" errors="0" failures="0" skipped="0" tests="1"><testcase classname="test_value" name="test_value" time="0.001"/></testsuite></testsuites>`)
	if err := nativeTargetVerifyPytestRun(dir, argv, stdout); err != nil {
		t.Fatalf("clean single pass refused: %v", err)
	}
	for name, suite := range map[string]string{
		"skipped":   `<testsuite errors="0" failures="0" skipped="1" tests="1"><testcase name="test_value"><skipped type="pytest.skip"/></testcase></testsuite>`,
		"xfail":     `<testsuite errors="0" failures="0" skipped="1" tests="1"><testcase name="test_value"><skipped type="pytest.xfail"/></testcase></testsuite>`,
		"failed":    `<testsuite errors="0" failures="1" skipped="0" tests="1"><testcase name="test_value"><failure/></testcase></testsuite>`,
		"zero":      `<testsuite errors="0" failures="0" skipped="0" tests="0"></testsuite>`,
		"two":       `<testsuite errors="0" failures="0" skipped="0" tests="2"><testcase name="a"/><testcase name="b"/></testsuite>`,
		"malformed": `<html>not junit</html>`,
	} {
		writeJUnit(`<testsuites>` + suite + `</testsuites>`)
		if err := nativeTargetVerifyPytestRun(dir, argv, stdout); err == nil {
			t.Fatalf("junit %s was admitted", name)
		}
	}
	// XPASS prints a short-summary line: refused even with a clean junit.
	writeJUnit(`<testsuites><testsuite errors="0" failures="0" skipped="0" tests="1"><testcase name="test_value"/></testsuite></testsuites>`)
	if err := nativeTargetVerifyPytestRun(dir, argv, []byte(".                                                                    [100%]\nXPASS tests/test_value.py::test_value\n1 passed in 0.01s\n")); err == nil {
		t.Fatal("xpass witness was admitted")
	}
	if err := nativeTargetVerifyPytestRun(dir, []string{"pytest", "x"}, stdout); err == nil {
		t.Fatal("stage without a junit channel was admitted")
	}
}

func TestNativeTargetAxisVenvRecordGate(t *testing.T) {
	venv := t.TempDir()
	site := filepath.Join(venv, "lib", "python3.13", "site-packages")
	dist := filepath.Join(site, "tinyfix-1.0.0.dist-info")
	if err := os.MkdirAll(dist, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(site, "tinyfix"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("VALUE = 7\n")
	sum := sha256.Sum256(payload)
	record := "tinyfix/__init__.py,sha256=" + base64.RawURLEncoding.EncodeToString(sum[:]) + "," + itoaForTest(len(payload)) + "\n" + "tinyfix-1.0.0.dist-info/RECORD,,\n" + "../../../bin/tinyfix,sha256=" + base64.RawURLEncoding.EncodeToString(sum[:]) + "," + itoaForTest(len(payload)) + "\n"
	if err := os.WriteFile(filepath.Join(site, "tinyfix", "__init__.py"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "RECORD"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "bin", "tinyfix"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := nativeTargetVerifyVenvRecords(venv); err != nil {
		t.Fatalf("clean venv refused: %v", err)
	}
	// A flipped payload byte with intact metadata must refuse.
	if err := os.WriteFile(filepath.Join(site, "tinyfix", "__init__.py"), []byte("VALUE = 8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := nativeTargetVerifyVenvRecords(venv); err == nil || !strings.Contains(err.Error(), "RECORD") {
		t.Fatalf("corrupted payload was admitted: %v", err)
	}
}

func TestNativeTargetAxisControlledEnvScrub(t *testing.T) {
	t.Setenv("PYTEST_ADDOPTS", "--evil")
	t.Setenv("NODE_OPTIONS", "--evil")
	t.Setenv("PYTHONPATH", "/evil")
	t.Setenv("CONCORD_ORACLE_SECRET_SENTINEL", "synthetic-secret")
	t.Setenv("UV_CACHE_DIR", "/ambient-cache")
	m, _ := nativeTargetTestManifest()
	e := nativeTargetEnvironment{Materializer: m.Materializer, Runner: m.Runner, RuntimeIdentity: "uv 0.11.26", UVCacheDir: "/plan-cache"}
	tEnv := &nativeTargetOraclePlan{Recipe: m, Environment: e}
	env := nativeTargetControlledEnv(tEnv, "/scratch")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"PYTEST_ADDOPTS", "NODE_OPTIONS", "PYTHONPATH", "CONCORD_ORACLE_SECRET_SENTINEL", "/ambient"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("controlled environment leaked %s", forbidden)
		}
	}
	for _, required := range []string{"PATH=", "HOME=/scratch/home", "TMPDIR=/scratch/tmp", "UV_CACHE_DIR=/plan-cache", "UV_OFFLINE=1", "UV_PYTHON=3.13.7", "PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("controlled environment lost %s", required)
		}
	}
	// The none materializer's environment carries no uv policy values.
	none := m
	none.Materializer = nativeTargetMaterializerNone
	noneEnv := nativeTargetControlledEnv(&nativeTargetOraclePlan{Recipe: none, Environment: nativeTargetEnvironment{Materializer: nativeTargetMaterializerNone, Runner: nativeTargetRunnerCheck}}, "/scratch")
	noneJoined := strings.Join(noneEnv, "\n")
	if strings.Contains(noneJoined, "UV_") {
		t.Fatalf("none materializer leaked uv policy values: %s", noneJoined)
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
