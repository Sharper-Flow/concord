package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// worktreeGoSourceFixtureFiles returns a fresh base fixture: one intact,
// self-contained module whose selected inputs all live in HEAD after the
// caller commits them.
func worktreeGoSourceFixtureFiles() map[string]string {
	return map[string]string{
		"go.mod": `module example.test/fixture

go 1.26.0
`,
		"dep/dep.go": `package dep

// Answer returns the deterministic fixture dependency answer.
func Answer() string {
	return "dep-answer"
}
`,
		"fixture.go": `package fixture

import (
	"embed"

	"example.test/fixture/dep"
)

//go:embed assets/hello.txt
var assets embed.FS

// Payload joins the embedded data with the in-module dependency answer.
func Payload() (string, string) {
	data, err := assets.ReadFile("assets/hello.txt")
	if err != nil {
		panic(err)
	}
	return string(data), dep.Answer()
}
`,
		"fixture_test.go": `package fixture

import "testing"

func TestFixturePayload(t *testing.T) {
	embedded, dependency := Payload()
	if embedded != "embed-payload\n" || dependency != "dep-answer" {
		t.Fatalf("unexpected fixture payload %q %q", embedded, dependency)
	}
}
`,
		"assets/hello.txt": `embed-payload
`,
	}
}

// worktreeGoSourceNativeFixtureFiles extends the intact pure-Go base with the
// in-module cgo package. Native inputs must earn no proof, so the extension
// serves only refusal cases.
func worktreeGoSourceNativeFixtureFiles() map[string]string {
	files := worktreeGoSourceFixtureFiles()
	files["cgo/cgo.go"] = `package cgo

// #include "cgo_lib.h"
import "C"

// Answer returns the fixture answer produced by the C translation unit.
func Answer() string {
	return C.GoString(C.fixtureAnswer())
}
`
	files["cgo/cgo_lib.h"] = "const char *fixtureAnswer(void);\n"
	files["cgo/cgo_lib.c"] = `#include "cgo_lib.h"

const char *fixtureAnswer(void) {
	return "cgo-answer";
}
`
	return files
}

// worktreeGoSourceFixtureSelectedCount pins the intact pure-Go fixture's
// whole selected input set: go.mod, dep/dep.go, fixture.go, fixture_test.go,
// assets/hello.txt.
const worktreeGoSourceFixtureSelectedCount = 5

// commitWorktreeGoSourceFixture initializes, adds, and commits the files the
// caller already wrote under dir. Fixture identity travels on the commit
// command alone; persistent Git configuration stays untouched.
func commitWorktreeGoSourceFixture(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"add", "-A"},
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "fixture"},
	} {
		if _, err := (ExecGitRunner{}).Run(t.Context(), dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}

func writeWorktreeGoSourceFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	commitWorktreeGoSourceFixture(t, dir)
	return dir
}

func worktreeGoSourceFixtureHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := (ExecGitRunner{}).Run(t.Context(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func assertWorktreeGoSourceFixtureStatusClean(t *testing.T, dir string) {
	t.Helper()
	out, err := (ExecGitRunner{}).Run(t.Context(), dir, "status", "--porcelain")
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("fixture status is not clean: %q, %v", string(out), err)
	}
}

// assertWorktreeGoSourceFixtureHidden proves the fixture premise that name is
// ignored and therefore absent from HEAD.
func assertWorktreeGoSourceFixtureHidden(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := (ExecGitRunner{}).Run(t.Context(), dir, "check-ignore", name); err != nil {
		t.Fatalf("fixture %s is not ignored: %v", name, err)
	}
	if _, err := (ExecGitRunner{}).Run(t.Context(), dir, "ls-files", "--error-unmatch", name); err == nil {
		t.Fatalf("fixture %s is tracked in HEAD", name)
	}
}

// observeWorktreeGoSourceClosureSuccess demands full proof: the
// commit-prefixed exact HEAD subject, the intact fixture's exact selected
// count, and a present digest. It never accepts a partial or unbound
// observation as success.
func observeWorktreeGoSourceClosureSuccess(t *testing.T, dir string) *WorktreeGoSourceClosure {
	t.Helper()
	environment, _ := worktreeGoSuiteEnvironment()
	subject := worktreeGoSourceFixtureHead(t, dir)
	closure, err := observeWorktreeGoSources(t.Context(), dir, environment)
	if err != nil || closure == nil || !closure.Complete {
		t.Fatalf("intact fixture earned no closure proof: %+v, %v", closure, err)
	}
	if closure.SubjectRef != "commit:"+subject {
		t.Fatalf("closure subject %q is not the exact HEAD commit %q", closure.SubjectRef, "commit:"+subject)
	}
	if closure.SelectedCount != worktreeGoSourceFixtureSelectedCount {
		t.Fatalf("closure selected %d inputs, want %d: %+v", closure.SelectedCount, worktreeGoSourceFixtureSelectedCount, closure)
	}
	if closure.ModulePath != "example.test/fixture" {
		t.Fatalf("closure main module path = %q", closure.ModulePath)
	}
	if closure.Residual != goSourceRuntimeResidual {
		t.Fatalf("closure runtime residual = %q", closure.Residual)
	}
	if closure.Digest == "" {
		t.Fatalf("closure captured no input digest: %+v", closure)
	}
	return closure
}

// observeWorktreeGoSourceClosureFailure demands that a mutation earns no
// proof at all: the observation must refuse, and any returned observation
// must not claim completeness.
func observeWorktreeGoSourceClosureFailure(t *testing.T, dir string) {
	t.Helper()
	environment, _ := worktreeGoSuiteEnvironment()
	closure, err := observeWorktreeGoSources(t.Context(), dir, environment)
	if err == nil {
		t.Fatalf("mutated fixture earned closure proof: %+v", closure)
	}
	if closure != nil && closure.Complete {
		t.Fatalf("refused observation still claimed complete proof: %+v", closure)
	}
}

// observeWorktreeGoSourceNativeRefusal demands the typed native rejection:
// an incomplete closure naming goSourceNativeUnsupported with a nonempty
// runtime residual, plus a non-nil refusal error.
func observeWorktreeGoSourceNativeRefusal(t *testing.T, dir string) {
	t.Helper()
	environment, _ := worktreeGoSuiteEnvironment()
	closure, err := observeWorktreeGoSources(t.Context(), dir, environment)
	if err == nil {
		t.Fatalf("native fixture earned closure proof: %+v", closure)
	}
	if closure == nil {
		t.Fatalf("native rejection carried no closure observation: %v", err)
	}
	if closure.Complete {
		t.Fatalf("native rejection claimed complete proof: %+v", closure)
	}
	if closure.Reason != goSourceNativeUnsupported {
		t.Fatalf("native rejection reason = %q", closure.Reason)
	}
	if closure.Residual == "" {
		t.Fatalf("native rejection carried no runtime residual: %+v", closure)
	}
}

func TestWorktreeGoSourceClosureEnvironmentNormalizesControls(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=caller")
	t.Setenv("GOMAXPROCS", "99")
	t.Setenv("GOWORK", "caller.work")
	environment, fixed := worktreeGoSuiteEnvironment()
	for _, required := range []string{"GOFLAGS=-mod=readonly", "GOMAXPROCS=4", "GOWORK=off", "GOENV=off"} {
		if !slices.Contains(fixed, required) {
			t.Fatalf("fixed controls lack %q: %v", required, fixed)
		}
	}
	for _, control := range fixed {
		key, _, _ := strings.Cut(control, "=")
		count := 0
		for _, value := range environment {
			if strings.HasPrefix(value, key+"=") {
				count++
				if value != control {
					t.Fatalf("inherited value overrides %q: %q", control, value)
				}
			}
		}
		if count != 1 {
			t.Fatalf("control %q appears %d times", control, count)
		}
	}
}

func TestWorktreeGoSourceClosureBindsExactHeadWithStableDigest(t *testing.T) {
	dir := writeWorktreeGoSourceFixture(t, worktreeGoSourceFixtureFiles())
	if _, err := os.Stat(filepath.Join(dir, "go.sum")); !os.IsNotExist(err) {
		t.Fatalf("fixture must carry no go.sum: %v", err)
	}
	first := observeWorktreeGoSourceClosureSuccess(t, dir)
	second := observeWorktreeGoSourceClosureSuccess(t, dir)
	if second.Digest != first.Digest || second.SelectedCount != first.SelectedCount || second.SubjectRef != first.SubjectRef {
		t.Fatalf("repeat observation changed its proof: %+v vs %+v", first, second)
	}
}

func TestWorktreeGoSourceClosureRefusesIgnoredSelectedTestMissingFromHead(t *testing.T) {
	files := worktreeGoSourceFixtureFiles()
	files[".gitignore"] = "hidden_test.go\n"
	files["hidden_test.go"] = `package fixture

import "testing"

func TestHiddenFromHead(t *testing.T) {}
`
	dir := writeWorktreeGoSourceFixture(t, files)
	assertWorktreeGoSourceFixtureHidden(t, dir, "hidden_test.go")
	observeWorktreeGoSourceClosureFailure(t, dir)
}

func TestWorktreeGoSourceClosureRefusesStatusHiddenGoSourceChange(t *testing.T) {
	dir := writeWorktreeGoSourceFixture(t, worktreeGoSourceFixtureFiles())
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "core.checkStat")
	t.Setenv("GIT_CONFIG_VALUE_0", "minimal")
	t.Setenv("GIT_CONFIG_KEY_1", "core.trustctime")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")
	path := filepath.Join(dir, "dep", "dep.go")
	cached := time.Unix(946684800, 0)
	if err := os.Chtimes(path, cached, cached); err != nil {
		t.Fatal(err)
	}
	if _, err := (ExecGitRunner{}).Run(t.Context(), dir, "update-index", "--refresh"); err != nil {
		t.Fatal(err)
	}
	changed := worktreeGoSourceFixtureFiles()
	changed["dep/dep.go"] = strings.Replace(changed["dep/dep.go"], "dep-answer", "dep-ANSWER", 1)
	if err := os.WriteFile(path, []byte(changed["dep/dep.go"]), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, cached, cached); err != nil {
		t.Fatal(err)
	}
	assertWorktreeGoSourceFixtureStatusClean(t, dir)
	environment, _ := worktreeGoSuiteEnvironment()
	closure, err := observeWorktreeGoSources(t.Context(), dir, environment)
	if err == nil || !strings.Contains(err.Error(), "Go selected input differs from its pinned blob: dep/dep.go") || closure != nil && closure.Complete {
		t.Fatalf("stat-hidden mutation did not reach the blob comparison: %+v, %v", closure, err)
	}
}

func TestWorktreeGoSourceClosureRefusesSkipWorktreeDependencyFlag(t *testing.T) {
	dir := writeWorktreeGoSourceFixture(t, worktreeGoSourceFixtureFiles())
	if _, err := (ExecGitRunner{}).Run(t.Context(), dir, "update-index", "--skip-worktree", "dep/dep.go"); err != nil {
		t.Fatal(err)
	}
	assertWorktreeGoSourceFixtureStatusClean(t, dir)
	observeWorktreeGoSourceClosureFailure(t, dir)
}

func TestWorktreeGoSourceClosureRefusesAssumeUnchangedDependencyFlag(t *testing.T) {
	dir := writeWorktreeGoSourceFixture(t, worktreeGoSourceFixtureFiles())
	if _, err := (ExecGitRunner{}).Run(t.Context(), dir, "update-index", "--assume-unchanged", "dep/dep.go"); err != nil {
		t.Fatal(err)
	}
	assertWorktreeGoSourceFixtureStatusClean(t, dir)
	observeWorktreeGoSourceClosureFailure(t, dir)
}

func TestWorktreeGoSourceClosureRefusesChangedEmbedInput(t *testing.T) {
	dir := writeWorktreeGoSourceFixture(t, worktreeGoSourceFixtureFiles())
	if err := os.WriteFile(filepath.Join(dir, "assets", "hello.txt"), []byte("embed-changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	observeWorktreeGoSourceClosureFailure(t, dir)
}

func TestWorktreeGoSourceClosureRefusesChangedCgoInput(t *testing.T) {
	dir := writeWorktreeGoSourceFixture(t, worktreeGoSourceNativeFixtureFiles())
	changed := `#include "cgo_lib.h"

const char *fixtureAnswer(void) {
	return "cgo-changed";
}
`
	if err := os.WriteFile(filepath.Join(dir, "cgo", "cgo_lib.c"), []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	observeWorktreeGoSourceNativeRefusal(t, dir)
}

func TestWorktreeGoSourceClosureRefusesIgnoredCgoInclude(t *testing.T) {
	files := worktreeGoSourceNativeFixtureFiles()
	files[".gitignore"] = "cgo/include/answer.h\n"
	files["cgo/include/answer.h"] = "#define FIXTURE_ANSWER \"nested-answer\"\n"
	files["cgo/cgo_lib.h"] = "#include \"include/answer.h\"\nconst char *fixtureAnswer(void);\n"
	files["cgo/cgo_lib.c"] = "#include \"cgo_lib.h\"\nconst char *fixtureAnswer(void) { return FIXTURE_ANSWER; }\n"
	dir := writeWorktreeGoSourceFixture(t, files)
	assertWorktreeGoSourceFixtureHidden(t, dir, "cgo/include/answer.h")
	assertWorktreeGoSourceFixtureStatusClean(t, dir)
	observeWorktreeGoSourceNativeRefusal(t, dir)
}

func TestWorktreeGoSourceClosureRefusesIgnoredRuntimeInput(t *testing.T) {
	files := worktreeGoSourceFixtureFiles()
	files[".gitignore"] = "scratch/\n"
	files["runtime_test.go"] = `package fixture

import (
	"os"
	"testing"
)

func TestReadsIgnoredRuntimeInput(t *testing.T) {
	data, err := os.ReadFile("scratch/expected.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "runtime-expected\n" {
		t.Fatalf("unexpected runtime input %q", data)
	}
}
`
	files["scratch/expected.txt"] = "runtime-expected\n"
	dir := writeWorktreeGoSourceFixture(t, files)
	assertWorktreeGoSourceFixtureHidden(t, dir, "scratch/expected.txt")
	assertWorktreeGoSourceFixtureStatusClean(t, dir)
	observeWorktreeGoSourceClosureFailure(t, dir)
}

func TestWorktreeGoSourceClosureRefusesIgnoredNonGoDirectory(t *testing.T) {
	files := worktreeGoSourceFixtureFiles()
	files[".gitignore"] = "scratch/\n"
	files["scratch/notes.txt"] = "notes\n"
	dir := writeWorktreeGoSourceFixture(t, files)
	assertWorktreeGoSourceFixtureHidden(t, dir, "scratch/notes.txt")
	observeWorktreeGoSourceClosureFailure(t, dir)
}

// TestWorktreeGoSourceClosureRefusesUntrackedEmptyDirectory pins that an
// empty untracked directory inside the root package tree refuses: status
// never shows it, so only the package-tree scan can reject it.
func TestWorktreeGoSourceClosureRefusesUntrackedEmptyDirectory(t *testing.T) {
	dir := writeWorktreeGoSourceFixture(t, worktreeGoSourceFixtureFiles())
	if err := os.Mkdir(filepath.Join(dir, "untracked"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "untracked"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("fixture directory is not empty: %d entries, %v", len(entries), err)
	}
	assertWorktreeGoSourceFixtureStatusClean(t, dir)
	observeWorktreeGoSourceClosureFailure(t, dir)
}

func TestWorktreeGoSourceClosureRefusesIgnoredConstraintExcludedGoFile(t *testing.T) {
	files := worktreeGoSourceFixtureFiles()
	files[".gitignore"] = "excluded.go\n"
	files["excluded.go"] = `//go:build ignore

package fixture

import "testing"

func TestExcludedByConstraint(t *testing.T) {}
`
	dir := writeWorktreeGoSourceFixture(t, files)
	assertWorktreeGoSourceFixtureHidden(t, dir, "excluded.go")
	observeWorktreeGoSourceClosureFailure(t, dir)
}

func TestWorktreeGoSourceClosureRefusesTrackedGoSumRemovedWhileHidden(t *testing.T) {
	files := worktreeGoSourceFixtureFiles()
	files["go.sum"] = ""
	dir := writeWorktreeGoSourceFixture(t, files)
	if _, err := (ExecGitRunner{}).Run(t.Context(), dir, "update-index", "--skip-worktree", "go.sum"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "go.sum")); err != nil {
		t.Fatal(err)
	}
	assertWorktreeGoSourceFixtureStatusClean(t, dir)
	observeWorktreeGoSourceClosureFailure(t, dir)
}
