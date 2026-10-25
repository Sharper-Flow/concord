package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// nativeTargetFixtureName joins one fixture-relative path onto the
// fixture's manifest root. A root of "." names the repository root, so the
// joined inventory stays a normalized repository path either way.
func nativeTargetFixtureName(root, rel string) string {
	return path.Join(root, rel)
}

// assertNativeTargetReceipt is the schema assertion helper for one core
// qualified target receipt: the closed family axes, the control's logical
// working directory, and the exact observed witness population.
func assertNativeTargetReceipt(t *testing.T, r WorktreeVerifyResult, materializer, runner, cwd string) {
	t.Helper()
	if r.Oracle == nil {
		t.Fatal("execute produced no oracle receipt")
	}
	if r.Oracle.RecipeKind != nativeTargetKind || r.Oracle.TargetMaterializer != materializer || r.Oracle.TargetRunner != runner {
		t.Fatalf("target receipt lost its axis properties: %+v", r.Oracle.NativeOraclePreparation)
	}
	if r.Oracle.LogicalCwd != cwd {
		t.Fatalf("receipt logical_cwd = %q, want %q", r.Oracle.LogicalCwd, cwd)
	}
	if len(r.Oracle.ObservedWitnessIDs) == 0 || !slices.Equal(r.Oracle.ObservedWitnessIDs, r.Oracle.SelectedWitnessIDs) {
		t.Fatalf("observed witnesses %v differ from selected %v", r.Oracle.ObservedWitnessIDs, r.Oracle.SelectedWitnessIDs)
	}
}

// nativeTargetFixture is the target_runner_v1 test fixture: one synthetic
// Project repository with no Go module, one pinned axis recipe, and the
// seeded workflow authority the native producer shares with the Go family.
type nativeTargetFixture struct {
	s               *Store
	work, repo      string
	head            string
	dispatchSubject string
	bundle          NativeOracleControlBundle
	prepare         WorktreeVerifyResult
	a, b            NativeOracleRequest
	actor           WorkflowActor
	launches        int
	materializer    string
	runner          string
	uvCache         string
	lockVersions    map[string]string
	recipePath      string
	manifestJSON    string
	controlArgv     []string
	root            string
}

func TestNativeTargetAxisNoGoModule(t *testing.T) {
	f := newNativeTargetFixture(t)
	if _, err := os.Stat(filepath.Join(f.repo, "go.mod")); !os.IsNotExist(err) {
		t.Fatalf("the synthetic target project must not be a Go module: %v", err)
	}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 {
		t.Fatalf("target execute %+v err=%v", r, err)
	}
	if f.launches != 1 {
		t.Fatalf("launches=%d", f.launches)
	}
	if r.Oracle.RecipeKind != "target_runner_v1" || r.Oracle.TargetMaterializer != "uv" || r.Oracle.TargetRunner != "pytest" || len(r.Oracle.SelectedWitnessIDs) == 0 || len(r.Oracle.SelectedTestNames) != 0 {
		t.Fatalf("target receipt lost its axis properties: %+v", r.Oracle.NativeOraclePreparation)
	}
	if !slices.Equal(r.Oracle.ObservedWitnessIDs, r.Oracle.SelectedWitnessIDs) {
		t.Fatalf("observed witnesses %v differ from selected %v", r.Oracle.ObservedWitnessIDs, r.Oracle.SelectedWitnessIDs)
	}
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a)); err != nil {
		t.Fatalf("terminal replay refused: %v", err)
	}
	if f.launches != 1 {
		t.Fatalf("replay launched again: launches=%d", f.launches)
	}
}

func TestNativeTargetAxisPublicWire(t *testing.T) {
	f := newNativeTargetCheckFixture(t)
	assertNativeTargetPublicWire(t, f.prepare)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("public-wire-execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("target execute: %+v, %v", r, err)
	}
	assertNativeTargetPublicWire(t, r)
}

func assertNativeTargetPublicWire(t *testing.T, result WorktreeVerifyResult) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"work_id": result.WorkID, "project_id": result.ProjectID, "branch": result.Branch, "path": result.Path,
		"lease_id": result.LeaseID, "operation_ref": result.OperationRef, "command": result.Command,
		"exit_code": result.ExitCode, "output": result.Output, "output_truncated": result.OutputTruncated,
		"tracked_files_changed": result.TrackedFilesChanged, "oracle": result.Oracle,
		"changed_refs": []any{}, "next_valid_intents": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("worktree_verify_result", raw); err != nil {
		t.Fatalf("actual target core output violates its public schema: %v\n%s", err, raw)
	}
	var wire struct {
		Oracle map[string]json.RawMessage `json:"oracle"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"selected_test_names", "case_to_test_map", "observed_test_names"} {
		if _, present := wire.Oracle[field]; present {
			t.Errorf("target wire contains Go-only field %s", field)
		}
	}
}

// A candidate production fault fails through the pinned harness, a candidate
// edit that neuters the harness still fails, and a genuine repair passes.
func TestNativeTargetAxisPinnedHarness(t *testing.T) {
	f := newNativeTargetFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "app", "value.py"), []byte("VALUE = 8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "faulty")
	o := f.authorize(t, "faulty", f.actor)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("faulty-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 || f.launches != 1 {
		t.Fatalf("false-green candidate escaped the pinned harness: %+v %v launches=%d", r, err, f.launches)
	}
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "fail"); err != nil {
		t.Fatalf("truthful target failure receipt refused: %v", err)
	}
	// The candidate cannot repair itself by editing its own harness copy.
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "tests", "test_value.py"), []byte("def test_value():\n    pass\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "neutered")
	o = f.authorize(t, "neutered", f.actor)
	r, err = f.s.VerifyWorktree(context.Background(), f.request("neutered-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || f.launches != 2 {
		t.Fatalf("neutered candidate test escaped the pinned harness: %+v %v launches=%d", r, err, f.launches)
	}
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "app", "value.py"), []byte("VALUE = 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "repaired")
	o = f.authorize(t, "repaired", f.actor)
	r, err = f.s.VerifyWorktree(context.Background(), f.request("repaired-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 3 {
		t.Fatalf("genuine repair %+v %v launches=%d", r, err, f.launches)
	}
}

// commitAndPrepare commits the working tree and refreshes the qualified
// subject and preparation. The recipe commit stays pinned: candidate commits
// never move the harness pin.
func (f *nativeTargetFixture) commitAndPrepare(t *testing.T, id string) {
	t.Helper()
	f.commitAll(t, id)
	f.prepareWithCurrentBundle(t, id)
}

// commitAndPrepareRepin additionally re-pins the recipe at the new head, as
// an approved harness or dependency re-pin does.
func (f *nativeTargetFixture) commitAndPrepareRepin(t *testing.T, id string) {
	t.Helper()
	f.commitAll(t, id)
	f.bundle.Control.RecipeSource.CommitOID = f.head
	f.prepareWithCurrentBundle(t, id)
}

func (f *nativeTargetFixture) prepareWithCurrentBundle(t *testing.T, id string) {
	t.Helper()
	if _, err := f.s.VerifyWorktree(context.Background(), f.request(id+"-subject", nil)); err != nil {
		t.Fatal(err)
	}
	prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	var err error
	f.prepare, err = f.s.VerifyWorktree(context.Background(), f.request(id+"-prepare", &prepare))
	if err != nil || f.prepare.Oracle == nil || f.prepare.Oracle.Qualification != "ready" {
		t.Fatalf("fresh preparation %v %+v", err, f.prepare)
	}
}

// A selected witness that the framework would skip, xfail, xpass, or leave
// without a test body can never qualify a pass.
func TestNativeTargetAxisWitnessPopulation(t *testing.T) {
	for name, body := range map[string]string{
		"skipped": "import pytest\n\nfrom app.value import VALUE\n\n\n@pytest.mark.skip\ndef test_value():\n    assert VALUE == 7\n",
		"xfail":   "import pytest\n\nfrom app.value import VALUE\n\n\n@pytest.mark.xfail\ndef test_value():\n    assert VALUE != 7\n",
		"xpass":   "import pytest\n\nfrom app.value import VALUE\n\n\n@pytest.mark.xfail\ndef test_value():\n    assert VALUE == 7\n",
		"empty":   "from app.value import VALUE\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newNativeTargetFixture(t)
			if err := os.WriteFile(filepath.Join(f.repo, f.root, "tests", "test_value.py"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			f.commitAll(t, "population-"+name)
			f.bundle.Control.RecipeSource.CommitOID = f.head
			if _, err := f.s.VerifyWorktree(context.Background(), f.request("population-"+name+"-subject", nil)); err != nil {
				t.Fatal(err)
			}
			prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
			var err error
			f.prepare, err = f.s.VerifyWorktree(context.Background(), f.request("population-"+name+"-prepare", &prepare))
			if name == "empty" {
				// A zero collected population refuses preparation itself.
				if err == nil || f.prepare.Oracle == nil || f.prepare.Oracle.Qualification == "ready" || f.launches != 0 {
					t.Fatalf("empty population prepared: %+v %v launches=%d", f.prepare, err, f.launches)
				}
				return
			}
			if err != nil || f.prepare.Oracle == nil || f.prepare.Oracle.Qualification != "ready" {
				t.Fatalf("fresh preparation %v %+v", err, f.prepare)
			}
			o := f.authorize(t, "population-"+name, f.actor)
			r, err := f.s.VerifyWorktree(context.Background(), f.request("population-"+name, &o))
			if err == nil || r.Oracle == nil || r.Oracle.Qualification == "pass" || f.launches > 1 {
				t.Fatalf("malformed population qualified: %+v %v launches=%d", r, err, f.launches)
			}
			var green int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=?`, r.OperationRef).Scan(&green); err != nil || green != 0 {
				t.Fatal("malformed population created verification authority")
			}
		})
	}
}

// Environment closure: a wrong uv pin, a wrong pytest pin, a stale lock, and
// a malformed lock refuse unavailable before any acceptance body launches.
// The stale-lock case is the materializer's freshness proof: manifest byte
// equality alone cannot establish manifest/lock agreement.
func TestNativeTargetAxisEnvironmentClosure(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, f *nativeTargetFixture){
		"wrong_uv_pin": func(t *testing.T, f *nativeTargetFixture) {
			f.mutateRecipePin(t, map[string]any{"uv": "0.0.1"})
		},
		"wrong_pytest_pin": func(t *testing.T, f *nativeTargetFixture) {
			f.mutateRecipePin(t, map[string]any{"pytest": "0.0.1"})
		},
		"stale_lock": func(t *testing.T, f *nativeTargetFixture) {
			project := filepath.Join(f.repo, f.root, "pyproject.toml")
			data, err := os.ReadFile(project)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(data), "]\n\n[tool.uv]", ", \"iniconfig=="+f.lockVersions["iniconfig"]+"\"]\n\n[tool.uv]", 1)
			if changed == string(data) {
				t.Fatal("fixture could not add a lock-absent dependency")
			}
			if err := os.WriteFile(project, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"malformed_lock": func(t *testing.T, f *nativeTargetFixture) {
			if err := os.WriteFile(filepath.Join(f.repo, f.root, "uv.lock"), []byte("version = 1\nrequires-python = \">=3.13\"\n\n[[package]]\nname = \"broken\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newNativeTargetFixture(t)
			mutate(t, f)
			f.commitAll(t, name)
			if _, err := f.s.VerifyWorktree(context.Background(), f.request(name+"-subject", nil)); err != nil {
				t.Fatal(err)
			}
			b := f.bundle
			b.Control.RecipeSource.CommitOID = f.head
			_, err := f.s.VerifyWorktree(context.Background(), f.request(name+"-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &b}))
			if err == nil || !strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("closed-environment fault did not refuse unavailable: %v", err)
			}
			if f.launches != 0 {
				t.Fatalf("closure refusal launched the acceptance body: %d", f.launches)
			}
		})
	}
}

func (f *nativeTargetFixture) mutateRecipePin(t *testing.T, mutations map[string]any) {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal([]byte(f.manifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	pin := manifest["pin"].(map[string]any)
	for key, value := range mutations {
		pin[key] = value
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, f.recipePath), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// A dependency-input change must re-pin the retained environment identity:
// the retained plan's DependencyInputsDigest and Environment.Digest both
// move. Digest loss through runtime resolution would leave one identity for
// two different dependency obligations.
func TestNativeTargetAxisDependencyDigestRetained(t *testing.T) {
	f := newNativeTargetFixture(t)
	digests := map[string]string{f.head: f.retainedEnvironmentDigest(t)}
	// Re-lock with a different declared dependency set: iniconfig becomes a
	// direct dependency, so the lock bytes legitimately change.
	project := filepath.Join(f.repo, f.root, "pyproject.toml")
	data, err := os.ReadFile(project)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), "]\n\n[tool.uv]", ", \"iniconfig=="+f.lockVersions["iniconfig"]+"\"]\n\n[tool.uv]", 1)
	if changed == string(data) {
		t.Fatal("fixture could not change the dependency set")
	}
	if err := os.WriteFile(project, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	f.regenerateLock(t)
	f.commitAndPrepareRepin(t, "relock")
	digests[f.head] = f.retainedEnvironmentDigest(t)
	if len(digests) != 2 {
		names := make([]string, 0, len(digests))
		for name := range digests {
			names = append(names, name)
		}
		slices.Sort(names)
		t.Fatalf("retained environment identity did not move with dependency inputs: %v", names)
	}
}

func (f *nativeTargetFixture) retainedEnvironmentDigest(t *testing.T) string {
	t.Helper()
	var plan string
	if err := f.s.db.QueryRow(`SELECT native_plan_json FROM worktree_verify_leases WHERE lease_id=?`, f.prepare.LeaseID).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Target *struct {
			Environment struct {
				DependencyInputsDigest string `json:"dependency_inputs_digest"`
				Digest                 string `json:"digest"`
			} `json:"environment"`
		} `json:"target"`
	}
	if err := json.Unmarshal([]byte(plan), &parsed); err != nil || parsed.Target == nil {
		t.Fatalf("retained plan carries no target environment: %v", err)
	}
	return parsed.Target.Environment.DependencyInputsDigest + "|" + parsed.Target.Environment.Digest
}

// A corrupted cached wheel that uv itself accepts must refuse through the
// RECORD gate: the payload flips while the wheel metadata stays intact.
func TestNativeTargetAxisCorruptUVCache(t *testing.T) {
	f := newNativeTargetFixture(t)
	corrupted := corruptCachedWheelForTest(t, f.uvCache, "iniconfig")
	if corrupted == "" {
		t.Fatal("fixture found no cached iniconfig wheel payload to corrupt")
	}
	prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("corrupt-prepare", &prepare))
	if err == nil || r.Oracle == nil || r.Oracle.Qualification == "ready" || !strings.Contains(err.Error(), "RECORD") {
		t.Fatalf("corrupted cached wheel did not refuse through the RECORD gate: %+v %v", r, err)
	}
	if f.launches != 0 {
		t.Fatalf("corrupt-cache refusal launched the acceptance body: %d", f.launches)
	}
}

// corruptCachedWheelForTest flips one payload byte of a cached unpacked
// wheel in the run's private cache, leaving wheel metadata untouched. It
// returns the corrupted file path, or empty when the entry is absent.
func corruptCachedWheelForTest(t *testing.T, cache, dist string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(cache, "archive-v0", "*", dist+"-*.dist-info"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	root := filepath.Dir(matches[0])
	target := filepath.Join(root, dist, "__init__.py")
	data, err := os.ReadFile(target)
	if err != nil || len(data) == 0 {
		return ""
	}
	flip := byte('x')
	if data[len(data)-1] == 'x' {
		flip = 'y'
	}
	if err := os.WriteFile(target, append(data[:len(data)-1], flip), 0600); err != nil {
		t.Fatal(err)
	}
	return target
}

// The shared authorization machinery is family-independent: a forged attempt
// or a foreign packet refuses typed with zero launches for a target control.
func TestNativeTargetAxisForgedDispatchRefusal(t *testing.T) {
	f := newNativeTargetFixture(t)
	o := f.a
	o.AttemptID = "invented-attempt"
	f.refusal(t, "forged", o)
	o = f.a
	o.WorkerPacketDigest = f.b.WorkerPacketDigest
	f.refusal(t, "foreign-packet", o)
}

func (f *nativeTargetFixture) refusal(t *testing.T, id string, o NativeOracleRequest) {
	t.Helper()
	_, err := f.s.VerifyWorktree(context.Background(), f.request(id, &o))
	if err == nil || !strings.Contains(err.Error(), "unauthorized_dispatch") {
		t.Errorf("typed refusal=%v, want unauthorized_dispatch", err)
	}
	if f.launches != 0 {
		t.Errorf("refusal launched the acceptance body: launches=%d", f.launches)
	}
}

// The none materializer with the check runner grades explicit bounded
// argv checks through host-observed bash and python3 invocations; a
// production break fails them truthfully.
func TestNativeTargetAxisNoneCheck(t *testing.T) {
	f := newNativeTargetCheckFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 2 {
		t.Fatalf("none+check execute %+v %v launches=%d", r, err, f.launches)
	}
	if r.Oracle.RecipeKind != "target_runner_v1" || r.Oracle.TargetMaterializer != "none" || r.Oracle.TargetRunner != "check" || !slices.Equal(r.Oracle.SelectedWitnessIDs, []string{"python-value", "shell-value"}) {
		t.Fatalf("none+check receipt lost its axis properties: %+v", r.Oracle.NativeOraclePreparation)
	}
	// A genuine production break fails both pinned checks.
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "value.txt"), []byte("8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "broken")
	o := f.authorize(t, "broken", f.actor)
	r, err = f.s.VerifyWorktree(context.Background(), f.request("broken-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 || f.launches != 3 {
		t.Fatalf("none+check broken candidate %+v %v launches=%d", r, err, f.launches)
	}
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "fail"); err != nil {
		t.Fatalf("none+check truthful failure receipt refused: %v", err)
	}
}

// The uv materializer with the check runner materializes dependencies into
// the sealed environment first: python3 checks run under the materialized
// venv interpreter with the locked dependencies, never a fallback host
// python.
func TestNativeTargetAxisUVCheck(t *testing.T) {
	f := newNativeTargetUVCheckFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 2 {
		t.Fatalf("uv+check execute %+v %v launches=%d", r, err, f.launches)
	}
	if r.Oracle.RecipeKind != "target_runner_v1" || r.Oracle.TargetMaterializer != "uv" || r.Oracle.TargetRunner != "check" {
		t.Fatalf("uv+check receipt lost its axis properties: %+v", r.Oracle.NativeOraclePreparation)
	}
	// A dependency-input change re-pins; a broken production input fails.
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "value.txt"), []byte("8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "broken")
	o := f.authorize(t, "broken", f.actor)
	r, err = f.s.VerifyWorktree(context.Background(), f.request("broken-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 || f.launches != 3 {
		t.Fatalf("uv+check broken candidate %+v %v launches=%d", r, err, f.launches)
	}
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "fail"); err != nil {
		t.Fatalf("uv+check truthful failure receipt refused: %v", err)
	}
}

// A normal Project carries ungraded docs and data files beside its code:
// genuine preparation and execution must not refuse them.
func TestNativeTargetAxisPlainRepoInputs(t *testing.T) {
	f := newNativeTargetFixture(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "README.md"):         "# pyfix\n\nNormal Project documentation the runner never grades.\n",
		nativeTargetFixtureName(f.root, "assets/values.txt"): "expected=7\n",
	})
	f.commitAll(t, "plain-repo docs")
	f.bundle.Control.RecipeSource.CommitOID = f.head
	f.prepareWithCurrentBundle(t, "docs")
	o := f.authorize(t, "docs", f.actor)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("docs-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 1 {
		t.Fatalf("plain-repo docs execute %+v %v launches=%d", r, err, f.launches)
	}
}

// Candidate code that alters an ungraded input during the run and exits
// zero is infrastructure drift, never a pass: the input digest moves.
func TestNativeTargetAxisInputMutationFails(t *testing.T) {
	f := newNativeTargetFixture(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "README.md"):           "# pyfix\n\nNormal Project documentation.\n",
		nativeTargetFixtureName(f.root, "tests/test_value.py"): "import pathlib\n\nfrom app.value import VALUE\n\n\ndef test_value():\n    readme = pathlib.Path(__file__).parents[1] / \"README.md\"\n    readme.write_text(readme.read_text() + \"mutated\\n\")\n    assert VALUE == 7\n",
	})
	f.commitAll(t, "mutating harness")
	f.bundle.Control.RecipeSource.CommitOID = f.head
	f.prepareWithCurrentBundle(t, "mutating")
	o := f.authorize(t, "mutating", f.actor)
	_, err := f.s.VerifyWorktree(context.Background(), f.request("mutating-execute", &o))
	if err == nil || !strings.Contains(err.Error(), "changed during execution") {
		t.Fatalf("run-time input mutation was not refused: %v", err)
	}
	if f.launches != 1 {
		t.Fatalf("mutation execute launches=%d", f.launches)
	}
}

// The mutating harness below rewrites one installed dependency payload and
// its RECORD row coherently, then exits zero. The RECORD gate alone cannot
// see this: the before/after dependency fingerprints must.
const nativeTargetDependencyMutation = `import base64
import glob
import hashlib
import pathlib

import iniconfig

from app.value import VALUE


def _mutate_installed_dependency():
    payload = pathlib.Path(iniconfig.__file__)
    data = payload.read_bytes() + b"\n# mutated\n"
    payload.write_bytes(data)
    record = pathlib.Path(glob.glob(str(payload.parent.parent / "iniconfig-*.dist-info" / "RECORD"))[0])
    digest = "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
    rows = []
    for row in record.read_text().splitlines():
        parts = row.split(",")
        if parts[0] == "iniconfig/__init__.py":
            parts[1] = digest
            parts[2] = str(len(data))
            row = ",".join(parts)
        rows.append(row)
    record.write_text("\n".join(rows) + "\n")


`

// A pinned harness that mutates an installed dependency (with its RECORD
// rewritten coherently) and exits zero is unavailable, never a pass.
func TestNativeTargetAxisDependencyMutatedInTest(t *testing.T) {
	f := newNativeTargetFixture(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "tests/test_value.py"): nativeTargetDependencyMutation + "\n\ndef test_value():\n    _mutate_installed_dependency()\n    assert VALUE == 7\n",
	})
	f.commitAll(t, "dep-mutating harness")
	f.bundle.Control.RecipeSource.CommitOID = f.head
	f.prepareWithCurrentBundle(t, "depmut")
	o := f.authorize(t, "depmut", f.actor)
	_, err := f.s.VerifyWorktree(context.Background(), f.request("depmut-execute", &o))
	if err == nil || !strings.Contains(err.Error(), "dependency closure changed during execution") {
		t.Fatalf("test-stage dependency mutation was not refused: %v", err)
	}
	if f.launches != 1 {
		t.Fatalf("dependency mutation execute launches=%d", f.launches)
	}
}

// Candidate production code that mutates the installed environment at
// collection import time cannot even be qualified ready: preparation runs
// the same sealed closure, and its fingerprints refuse the mutant.
func TestNativeTargetAxisDependencyMutatedInCollect(t *testing.T) {
	f := newNativeTargetFixture(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "app/value.py"): "import pathlib\n\nVALUE = 7\n\n\ndef _touch_installed():\n    import iniconfig\n    installed = pathlib.Path(iniconfig.__file__)\n    installed.write_bytes(installed.read_bytes() + b\"\\n# mutated\\n\")\n\n_touch_installed()\n",
	})
	f.commitAll(t, "collect-mutating candidate")
	b := f.bundle
	b.Control.RecipeSource.CommitOID = f.head
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("collectmut-subject", nil)); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.VerifyWorktree(context.Background(), f.request("collectmut-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &b}))
	if err == nil || !strings.Contains(err.Error(), "dependency closure changed") {
		t.Fatalf("collection-stage dependency mutation did not refuse preparation: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("collection mutation launched the acceptance body: %d", f.launches)
	}
}

// A uv+pytest control whose working directory is the repository root itself
// admits through the public control definition, prepares ready without
// launching the acceptance body, and executes a real pass whose receipt
// names the root.
func TestNativeTargetAxisRootDirectoryPytest(t *testing.T) {
	f := newNativeTargetFixtureAtRoot(t, ".")
	if f.launches != 0 {
		t.Fatalf("preparation launched the acceptance body: launches=%d", f.launches)
	}
	if f.prepare.Oracle == nil || f.prepare.Oracle.Qualification != "ready" || f.prepare.Oracle.LogicalCwd != "." {
		t.Fatalf("root preparation did not qualify ready: %+v", f.prepare.Oracle)
	}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.ExitCode != 0 || f.launches != 1 {
		t.Fatalf("root pytest execute %+v %v launches=%d", r, err, f.launches)
	}
	assertNativeTargetReceipt(t, r, "uv", "pytest", ".")
}

// The none+check pair runs the same repository-root settlement: both pinned
// checks execute through real preparation and the receipt carries the root.
func TestNativeTargetAxisRootDirectoryCheck(t *testing.T) {
	f := newNativeTargetCheckFixtureAtRoot(t, ".")
	if f.launches != 0 {
		t.Fatalf("preparation launched the acceptance body: launches=%d", f.launches)
	}
	if f.prepare.Oracle == nil || f.prepare.Oracle.Qualification != "ready" || f.prepare.Oracle.LogicalCwd != "." {
		t.Fatalf("root preparation did not qualify ready: %+v", f.prepare.Oracle)
	}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.ExitCode != 0 || f.launches != 2 {
		t.Fatalf("root check execute %+v %v launches=%d", r, err, f.launches)
	}
	assertNativeTargetReceipt(t, r, "none", "check", ".")
}

// newNativeTargetFixture builds the uv+pytest target fixture under its
// nested project root.
func newNativeTargetFixture(t *testing.T) *nativeTargetFixture {
	t.Helper()
	return newNativeTargetFixtureAtRoot(t, "pyfix")
}

// newNativeTargetFixtureAtRoot builds the uv+pytest target fixture: a
// synthetic Python project whose pinned harness asserts the candidate's
// production value through a real uv-materialized pytest environment. The
// root is the manifest's working directory: a nested directory or the
// repository root itself.
func newNativeTargetFixtureAtRoot(t *testing.T, root string) *nativeTargetFixture {
	t.Helper()
	const work = "native-target-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: root, recipePath: nativeTargetFixtureName(root, "recipe.json"), materializer: "uv", runner: "pytest"}
	f.controlArgv = []string{"pytest", "tests/test_value.py::test_value"}

	f.installCommittedUVProject(t)
	pyVersion := liveVersionForTest(t, "python3", `Python ([0-9][0-9.]*)`)
	uvVersion := liveVersionForTest(t, "uv", `uv ([0-9][0-9.]*)`)
	files := map[string]string{
		nativeTargetFixtureName(f.root, "app/__init__.py"):     "",
		nativeTargetFixtureName(f.root, "app/value.py"):        "VALUE = 7\n",
		nativeTargetFixtureName(f.root, "tests/__init__.py"):   "",
		nativeTargetFixtureName(f.root, "pytest.ini"):          "[pytest]\npythonpath = .\ntestpaths = tests\n",
		nativeTargetFixtureName(f.root, "tests/test_value.py"): "from app.value import VALUE\n\n\ndef test_value():\n    assert VALUE == 7\n",
	}
	writeRepoFilesForTest(t, f.repo, files)
	f.setManifestForTest(t, map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "uv",
		"runner":        "pytest",
		"root":          f.root,
		"harness_files": []string{nativeTargetFixtureName(f.root, "tests/test_value.py"), nativeTargetFixtureName(f.root, "pytest.ini")},
		"support_files": []string{},
		"cases":         map[string][]string{"case:fixture": {"tests/test_value.py::test_value"}},
		"pin": map[string]string{
			"python": pyVersion, "uv": uvVersion, "pytest": f.lockVersions["pytest"],
			"dep_manifest": "pyproject.toml", "dep_lock": "uv.lock",
		},
	})
	f.commitAll(t, "pinned harness")
	f.seedAuthority(t, seed)
	return f
}

// newNativeTargetCheckFixture builds the none+check fixture under its
// nested project root.
func newNativeTargetCheckFixture(t *testing.T) *nativeTargetFixture {
	t.Helper()
	return newNativeTargetCheckFixtureAtRoot(t, "chkfix")
}

// newNativeTargetCheckFixtureAtRoot builds the none+check fixture: two
// pinned checks with explicit bounded argv — one bash check carrying
// arguments, one python3 check carrying arguments — that read the
// candidate's production value file. The root is the manifest's working
// directory: a nested directory or the repository root itself.
func newNativeTargetCheckFixtureAtRoot(t *testing.T, root string) *nativeTargetFixture {
	t.Helper()
	const work = "native-target-check-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: root, recipePath: nativeTargetFixtureName(root, "recipe.json"), materializer: "none", runner: "check"}
	f.controlArgv = []string{"check", "python-value", "shell-value"}
	shellVersion := liveVersionForTest(t, "bash", `version ([0-9][0-9.]*)`)
	pyVersion := liveVersionForTest(t, "python3", `Python ([0-9][0-9.]*)`)
	raw, err := json.Marshal(map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "none",
		"runner":        "check",
		"root":          f.root,
		"harness_files": []string{nativeTargetFixtureName(f.root, "scripts/check.sh"), nativeTargetFixtureName(f.root, "scripts/check.py")},
		"support_files": []string{},
		"cases":         map[string][]string{"case:fixture": {"shell-value", "python-value"}},
		"checks": []map[string]any{
			{"witness": "shell-value", "argv": []string{"bash", "scripts/check.sh", "--expected", "7"}},
			{"witness": "python-value", "argv": []string{"python3", "scripts/check.py", "--expected", "7"}},
		},
		"pin": map[string]string{"shell": shellVersion, "python": pyVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.manifestJSON = string(raw)
	files := map[string]string{
		nativeTargetFixtureName(f.root, "value.txt"):        "7\n",
		nativeTargetFixtureName(f.root, "scripts/check.sh"): "#!/usr/bin/env bash\nset -euo pipefail\nexpected=\"\"\nwhile [[ $# -gt 0 ]]; do\n  case \"$1\" in\n    --expected) expected=\"$2\"; shift 2;;\n    *) shift;;\n  esac\ndone\ntest \"$(cat value.txt)\" = \"$expected\"\n",
		nativeTargetFixtureName(f.root, "scripts/check.py"): "import argparse\nimport pathlib\nimport sys\n\nparser = argparse.ArgumentParser()\nparser.add_argument(\"--expected\", type=int)\nsys.exit(0 if int(pathlib.Path(\"value.txt\").read_text().strip()) == parser.parse_args().expected else 1)\n",
		f.recipePath: f.manifestJSON,
	}
	writeRepoFilesForTest(t, f.repo, files)
	f.commitAll(t, "pinned check harness")
	f.seedAuthority(t, seed)
	return f
}

// newNativeTargetUVCheckFixture builds the uv+check fixture: the uv
// materializer provisions a locked dependency set first, and the pinned
// python3 check proves it runs under the materialized venv interpreter with
// the locked dependency importable, while a bash check carries arguments.
func newNativeTargetUVCheckFixture(t *testing.T) *nativeTargetFixture {
	t.Helper()
	const work = "native-target-uvcheck-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: "uvchk", recipePath: "uvchk/recipe.json", materializer: "uv", runner: "check"}
	f.controlArgv = []string{"check", "deps", "shell-args"}

	f.installCommittedUVProject(t)
	pyVersion := liveVersionForTest(t, "python3", `Python ([0-9][0-9.]*)`)
	uvVersion := liveVersionForTest(t, "uv", `uv ([0-9][0-9.]*)`)
	shellVersion := liveVersionForTest(t, "bash", `version ([0-9][0-9.]*)`)
	files := map[string]string{
		nativeTargetFixtureName(f.root, "value.txt"):             "7\n",
		nativeTargetFixtureName(f.root, "scripts/check_deps.py"): "import pathlib\nimport sys\n\nimport iniconfig\n\nassert sys.prefix != sys.base_prefix, \"python3 checks must run in the materialized environment\"\nsys.exit(0 if int(pathlib.Path(\"value.txt\").read_text().strip()) == 7 else 1)\n",
		nativeTargetFixtureName(f.root, "scripts/args.sh"):       "#!/usr/bin/env bash\nset -euo pipefail\ntest \"$1\" = \"--flag\"\ntest -n \"${UV_PROJECT_ENVIRONMENT:-}\"\n",
	}
	writeRepoFilesForTest(t, f.repo, files)
	f.setManifestForTest(t, map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "uv",
		"runner":        "check",
		"root":          f.root,
		"harness_files": []string{nativeTargetFixtureName(f.root, "scripts/check_deps.py"), nativeTargetFixtureName(f.root, "scripts/args.sh")},
		"support_files": []string{},
		"cases":         map[string][]string{"case:fixture": {"deps", "shell-args"}},
		"checks": []map[string]any{
			{"witness": "deps", "argv": []string{"python3", "scripts/check_deps.py"}},
			{"witness": "shell-args", "argv": []string{"bash", "scripts/args.sh", "--flag"}},
		},
		"pin": map[string]string{"python": pyVersion, "uv": uvVersion, "shell": shellVersion, "dep_manifest": "pyproject.toml", "dep_lock": "uv.lock"},
	})
	f.commitAll(t, "pinned uv check harness")
	f.seedAuthority(t, seed)
	return f
}

// nativeTargetUVProjectDir holds the committed uv fixture project. Its
// uv.lock is the single source of the uv fixtures' dependency set: the CI
// warm step fills the source cache from the same lock, and every fixture
// copies these bytes instead of resolving a set of its own.
const nativeTargetUVProjectDir = "testdata/native-target-uv"

// nativeTargetUVLock is the committed fixture project as the fixtures read
// it: the manifest and lock bytes, each registry package's locked version,
// and the registry packages the lock installs only behind an environment
// marker.
type nativeTargetUVLock struct {
	pyproject, lock string
	versions        map[string]string
	markerOnly      map[string]bool
}

var (
	nativeTargetUVLockPackagePattern    = regexp.MustCompile(`(?m)^name = "([^"]+)"\nversion = "([^"]+)"\nsource = \{ registry = "https://pypi\.org/simple" \}$`)
	nativeTargetUVLockDependencyPattern = regexp.MustCompile(`\{ name = "([^"]+)"(, marker = "[^"]*")? \}`)
)

func readNativeTargetUVLockForTest(t *testing.T) nativeTargetUVLock {
	t.Helper()
	pyproject, err := os.ReadFile(filepath.Join(nativeTargetUVProjectDir, "pyproject.toml"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(nativeTargetUVProjectDir, "uv.lock"))
	if err != nil {
		t.Fatal(err)
	}
	l := nativeTargetUVLock{pyproject: string(pyproject), lock: string(lock), versions: map[string]string{}, markerOnly: map[string]bool{}}
	for _, match := range nativeTargetUVLockPackagePattern.FindAllStringSubmatch(l.lock, -1) {
		l.versions[match[1]] = match[2]
	}
	if l.versions["pytest"] == "" || l.versions["iniconfig"] == "" {
		t.Fatalf("committed fixture lock %s pins no registry pytest and iniconfig", nativeTargetUVProjectDir)
	}
	unmarked := map[string]bool{}
	for _, match := range nativeTargetUVLockDependencyPattern.FindAllStringSubmatch(l.lock, -1) {
		if match[2] == "" {
			unmarked[match[1]] = true
		} else {
			l.markerOnly[match[1]] = true
		}
	}
	for name := range unmarked {
		delete(l.markerOnly, name)
	}
	return l
}

// installCommittedUVProject copies the committed project and lock into the
// fixture root and seeds the run's private uv cache from the read-only
// source cache. UV_CACHE_DIR names the private cache before any driver run,
// so no run can alter the source cache (CD-0205 D1).
func (f *nativeTargetFixture) installCommittedUVProject(t *testing.T) {
	t.Helper()
	lock := readNativeTargetUVLockForTest(t)
	f.lockVersions = lock.versions
	f.uvCache = filepath.Join(t.TempDir(), "uv-cache")
	seedNativeTargetUVCacheForTest(t, hostUVCacheForTest(t), f.uvCache, lock)
	t.Setenv("UV_CACHE_DIR", f.uvCache)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "pyproject.toml"): lock.pyproject,
		nativeTargetFixtureName(f.root, "uv.lock"):        lock.lock,
	})
}

// setManifestForTest renders the strict closed manifest and writes it as
// the fixture's pinned recipe.
func (f *nativeTargetFixture) setManifestForTest(t *testing.T, manifest map[string]any) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	f.manifestJSON = string(raw)
	writeRepoFilesForTest(t, f.repo, map[string]string{f.recipePath: f.manifestJSON})
}

// regenerateLock regenerates the fixture's uv.lock after a declared
// dependency change, offline against the private cache. The declared
// change names only versions the committed lock already pins.
func (f *nativeTargetFixture) regenerateLock(t *testing.T) {
	t.Helper()
	cmd := exec.Command("uv", "lock", "--no-config", "--offline", "--no-python-downloads") //nolint:gosec // test fixture, fixed argv.
	cmd.Dir = filepath.Join(f.repo, f.root)
	cmd.Env = append(os.Environ(), "UV_CACHE_DIR="+f.uvCache)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline uv relock failed: %v: %s", err, out)
	}
}

func liveVersionForTest(t *testing.T, name, pattern string) string {
	t.Helper()
	out, err := exec.Command(name, "--version").Output() //nolint:gosec // fixed host probe.
	if err != nil {
		t.Fatalf("probe %s: %v", name, err)
	}
	match := regexp.MustCompile(pattern).FindStringSubmatch(strings.TrimSpace(string(out)))
	if match == nil {
		t.Fatalf("unparseable %s version output: %q", name, out)
	}
	return match[1]
}

// nativeTargetUVWarmHint names the commands that warm a source cache from
// the committed lock; CI runs the same commands before the store tests.
const nativeTargetUVWarmHint = "warm it from internal/store/" + nativeTargetUVProjectDir + " with `uv lock --locked --refresh` and `uv sync --locked --no-build`"

// hostUVCacheForTest resolves the read-only source uv cache. The test
// workloads sandbox HOME and scrub CONCORD_* variables, so an explicit
// source may be supplied through OC_NATIVE_UV_CACHE; otherwise the standard
// cache locations apply. A missing cache fails the test loudly: it is never
// skipped.
func hostUVCacheForTest(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("OC_NATIVE_UV_CACHE"); v != "" {
		return v
	}
	var candidates []string
	if dir, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "uv"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".cache", "uv"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(filepath.Join(candidate, "wheels-v6", "pypi")); err == nil && info.IsDir() {
			return candidate
		}
	}
	t.Fatalf("no source uv cache found (tried %v); set OC_NATIVE_UV_CACHE to a uv cache root and %s", candidates, nativeTargetUVWarmHint)
	return ""
}

// seedNativeTargetUVCacheForTest copies, for each locked registry package,
// the source cache's index page, its locked-version wheel entries, and the
// unpacked archives those entries link to into a private writable cache.
// The source cache is only read. A source cache the committed lock did not
// warm fails the test loudly.
func seedNativeTargetUVCacheForTest(t *testing.T, source, private string, lock nativeTargetUVLock) {
	t.Helper()
	for name, version := range lock.versions {
		page := filepath.Join("simple-v21", "pypi", name+".rkyv")
		if err := copyUVCacheFileForTest(filepath.Join(source, page), filepath.Join(private, page)); err != nil {
			t.Fatalf("source uv cache %s has no index page for %s; %s: %v", source, name, nativeTargetUVWarmHint, err)
		}
		wheels := filepath.Join("wheels-v6", "pypi", name)
		entries, err := os.ReadDir(filepath.Join(source, wheels))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(private, wheels), 0700); err != nil {
			t.Fatal(err)
		}
		unpacked := false
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), version+"-") {
				continue
			}
			rel := filepath.Join(wheels, entry.Name())
			if entry.Type()&os.ModeSymlink == 0 {
				if err := copyUVCacheFileForTest(filepath.Join(source, rel), filepath.Join(private, rel)); err != nil {
					t.Fatal(err)
				}
				continue
			}
			target, err := os.Readlink(filepath.Join(source, rel))
			if err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(wheels, target)
			if !filepath.IsLocal(archive) {
				t.Fatalf("source uv cache wheel entry %s links outside the cache: %s", rel, target)
			}
			if err := copyUVCacheTreeForTest(filepath.Join(source, archive), filepath.Join(private, archive)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(private, rel)); err != nil {
				t.Fatal(err)
			}
			unpacked = true
		}
		if !unpacked && !lock.markerOnly[name] {
			t.Fatalf("source uv cache %s has no unpacked %s %s wheel; %s", source, name, version, nativeTargetUVWarmHint)
		}
	}
}

func copyUVCacheFileForTest(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // test fixture reads its own source cache.
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}

// copyUVCacheTreeForTest copies one unpacked archive into the private cache
// as writable files, so a test may alter its private copy.
func copyUVCacheTreeForTest(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case entry.IsDir():
			return os.MkdirAll(out, 0700)
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, out)
		default:
			return copyUVCacheFileForTest(path, out)
		}
	})
}

func writeRepoFilesForTest(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		full := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *nativeTargetFixture) commitAll(t *testing.T, message string) {
	t.Helper()
	if f.head == "" {
		gitRunStore(t, f.repo, "init", "-b", "target-branch")
	}
	gitRunStore(t, f.repo, "add", ".")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", message)
	head, err := (ExecGitRunner{}).Run(context.Background(), f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	f.head = strings.TrimSpace(string(head))
	f.dispatchSubject = f.head
}

// seedAuthority mirrors the Go fixture's store seeding: an active claim, a
// qualified subject, the bundle, and two authorized dispatch bindings.
func (f *nativeTargetFixture) seedAuthority(t *testing.T, seed workflowReturnRouteFixture) {
	t.Helper()
	f.seedTargetAuthority(t)
	prepare := &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	var err error
	f.prepare, err = f.s.VerifyWorktree(context.Background(), f.request("prepare", prepare))
	if err != nil || f.prepare.Oracle == nil || f.prepare.Oracle.Qualification != "ready" {
		t.Fatalf("prepare: %+v err=%v", f.prepare, err)
	}
	if f.launches != 0 {
		t.Fatalf("preparation launched the acceptance body: launches=%d", f.launches)
	}
	f.a = f.authorizeLane(t, "a", seed.owner, "implement")
	f.b = f.authorize(t, "b", seed.owner)
}

// seedTargetAuthority seeds the active claim, the qualified subject, the
// pinned bundle, and the launch hook, leaving the preparation to the
// caller: a fixture whose manifest must refuse preparation on a
// conforming store cannot assert a ready preparation of its own.
func (f *nativeTargetFixture) seedTargetAuthority(t *testing.T) {
	t.Helper()
	if _, err := f.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?,'project','native-target-claim','target-branch',?,?,?,'active','2026-10-09T00:00:00Z','{}');DELETE FROM write_guard`, WorktreeSetID(f.work), f.head, f.repo, f.repo); err != nil {
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
	f.bundle.Control.Cwd = f.root
	f.bundle.Control.Argv = f.controlArgv
	f.bundle.Control.RecipeSource = WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: f.recipePath, CommitOID: f.head}
	f.bundle.Control.ReadinessEvidenceRefs = nil
	f.s.nativeOracleProgramLaunched = func() {
		f.launches++
		var count int
		if err := f.s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE state='held'`).Scan(&count); err != nil || count != 1 {
			t.Errorf("launch with unavailable sole SQL connection: %d %v", count, err)
		}
	}
}

func (f *nativeTargetFixture) request(id string, o *NativeOracleRequest) WorktreeVerifyRequest {
	r := WorktreeVerifyRequest{WorkID: f.work, ProjectID: "project", LeaseID: "native-target-" + id, RequestID: "request-" + id, PrincipalRef: "principal/operator", Owner: SessionWorktreeOwner{ClientRef: "client/oracle", AgentRef: "agent/native", SessionRef: "session/native"}, Runner: ExecGitRunner{}, Oracle: o, Now: time.Now().UTC()}
	r.SupportedBudgetSeconds = 60
	if o == nil {
		r.Command = []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}
	}
	return r
}

func (f *nativeTargetFixture) authorize(t *testing.T, id string, actor WorkflowActor) NativeOracleRequest {
	t.Helper()
	return f.authorizeLane(t, id, actor, "verify")
}

func (f *nativeTargetFixture) authorizeLane(t *testing.T, id string, actor WorkflowActor, laneID string) NativeOracleRequest {
	t.Helper()
	b := f.bundle
	b.Control.ReadinessEvidenceRefs = []string{f.prepare.OperationRef}
	oracle := &AcceptanceOracle{Owners: []OracleOwner{b.Owner}, Cases: b.Cases, Controls: []OracleControl{b.Control}}
	version := readWorkVersion(t, f.s, f.work)
	next := version + 1
	productionPath := nativeTargetFixtureName(f.root, "value.txt")
	if f.runner == "pytest" {
		productionPath = nativeTargetFixtureName(f.root, "app/value.py")
	}
	job := WorkerJobRecordedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: f.work, ExpectedVersion: &version, ResultingVersion: &next}, JobID: "job:" + id, Revision: 1, ContractVersion: 1, Objective: "execute pinned target control " + id, StoppingCondition: "the exact control executes", ProjectScope: "project", PathScope: []string{productionPath}, PredicateIDs: b.Control.PredicateIDs, Checks: []string{"pinned target value"}, Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{}, Readiness: &WorkerJobReadiness{Ready: true, Evidence: []string{f.prepare.OperationRef}}, AcceptanceOracle: oracle}
	job.Digest = DeriveWorkerJobDigest(job)
	raw, _ := json.Marshal(job)
	actorRef, _ := WorkflowActorRef(actor)
	event := workflowEventWithActor("native-target-job-"+id, WorkerJobRecorded, f.work, actorRef, map[string]any{})
	event.Payload = raw
	event.PayloadVersion = CurrentEventPayloadVersion(WorkerJobRecorded)
	if err := applyWorkflowTestOperationDirect(context.Background(), f.s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.work): version}}); err != nil {
		t.Fatal(err)
	}
	version++
	packet := map[string]any{"work_id": f.work, "attempt_id": "attempt-" + id, "job": job.Digest, "subject_commit": f.head}
	packetRaw, _ := json.Marshal(packet)
	canonical, _ := canonicalJSON(packetRaw)
	digest := nativeDigest(canonical)
	lane := reviewGateLane(t, map[string]string{"implement": "implementation", "verify": "verification"}[laneID])
	ld := lane.Digest
	var epoch int64
	if err := f.s.db.QueryRow(`SELECT coalesce(max(json_extract(payload,'$.attempt_epoch')),0)+1 FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='repair'`, f.work, WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	start := workflowEventWithActor("native-target-start-"+id, WorkflowActionStarted, f.work, actorRef, map[string]any{"work_id": f.work, "expected_version": version, "resulting_version": version + 1, "step_id": "repair", "action_id": "dispatch_worker", "attempt_epoch": epoch, "accepted_inputs_digest": digest, "idempotency_identity": "native-target-dispatch-" + id, "actor_ref": actorRef})
	completion := workflowEventWithActor("native-target-completion-"+id, WorkflowActionCompleted, f.work, actorRef, map[string]any{"work_id": f.work, "expected_version": version + 1, "resulting_version": version + 2, "step_id": "repair", "action_id": "dispatch_worker", "attempt_epoch": epoch, "actor_ref": actorRef, "result_evidence_refs": []string{}, "changed_refs": []string{}, "worker_attempt_id": "attempt-" + id, "worker_packet_digest": digest, "worker_subject_commit": f.dispatchSubject, "worker_worktree_identity": workerWorktreeIdentity(f.repo), "worker_job": WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}, "worker_lane_id": lane.ID, "worker_lane_digest": ld, "worker_capability_class": lane.CapabilityClass, "worker_packet_predicate_ids": b.Control.PredicateIDs})
	completion.PayloadVersion = CurrentEventPayloadVersion(WorkflowActionCompleted)
	if err := applyWorkflowTestOperationDirect(context.Background(), f.s, Operation{Events: []Event{start, completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.work): version}}); err != nil {
		t.Fatal(err)
	}
	return NativeOracleRequest{Phase: "execute", AttemptID: "attempt-" + id, AttemptEpoch: epoch, WorkerPacketDigest: digest, WorkerJobBinding: &WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}, ControlID: b.Control.ControlID, PreparationRunRef: f.prepare.OperationRef}
}

// An unsupported pair refuses through the real producer before any plan
// exists: the closed union, not two independently accepted enum values.
func TestNativeTargetAxisUnsupportedPairE2E(t *testing.T) {
	f := newNativeTargetFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal([]byte(f.manifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["materializer"] = "none"
	manifest["runner"] = "pytest"
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, f.recipePath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAll(t, "unsupported pair")
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("unsupported-subject", nil)); err != nil {
		t.Fatal(err)
	}
	b := f.bundle
	b.Control.RecipeSource.CommitOID = f.head
	_, err = f.s.VerifyWorktree(context.Background(), f.request("unsupported-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &b}))
	if err == nil || !strings.Contains(err.Error(), "unsupported materializer/runner pair") {
		t.Fatalf("unsupported pair did not refuse: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("unsupported pair refusal launched the acceptance body: %d", f.launches)
	}
}

// A uv+pytest control whose complete execute plan exceeds the public
// result's staged bound — the materializer prefix plus one graded stage
// per witness — refuses preparation itself with the typed limit refusal:
// the store never pays a preparation for an output it could not record,
// and no acceptance program launches.
func TestNativeTargetAxisStageBudgetRefusesPreparation(t *testing.T) {
	f := newNativeTargetFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal([]byte(f.manifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	harness, ok := manifest["harness_files"].([]any)
	if !ok {
		t.Fatal("fixture manifest carries no harness file inventory")
	}
	// Two pinned files carry all fifteen witnesses, so the harness
	// inventory stays inside its own bound; the superseded single-witness
	// file leaves the inventory and the tree, or collection would observe
	// a foreign witness.
	witnessesA := budgetWitnesses("tests/test_budget_a.py", 8)
	witnessesB := budgetWitnesses("tests/test_budget_b.py", 7)
	pytestBody := func(count int) string {
		var body strings.Builder
		for i := 0; i < count; i++ {
			fmt.Fprintf(&body, "def test_%02d():\n    pass\n\n", i)
		}
		return body.String()
	}
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "tests/test_budget_a.py"): pytestBody(8),
		nativeTargetFixtureName(f.root, "tests/test_budget_b.py"): pytestBody(7),
	})
	if err := os.Remove(filepath.Join(f.repo, f.root, "tests", "test_value.py")); err != nil {
		t.Fatal(err)
	}
	kept := make([]any, 0, len(harness)+2)
	for _, raw := range harness {
		name, ok := raw.(string)
		if !ok {
			t.Fatal("fixture harness inventory carries a non-string path")
		}
		if strings.HasSuffix(name, "tests/test_value.py") {
			continue
		}
		kept = append(kept, name)
	}
	kept = append(kept,
		nativeTargetFixtureName(f.root, "tests/test_budget_a.py"),
		nativeTargetFixtureName(f.root, "tests/test_budget_b.py"),
	)
	manifest["harness_files"] = kept
	witnesses := append(append([]string{}, witnessesA...), witnessesB...)
	manifest["cases"] = map[string]any{"case:a": witnessesA, "case:b": witnessesB}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, f.recipePath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAll(t, "stage budget overbound")
	b := f.bundle
	b.Control.RecipeSource.CommitOID = f.head
	b.Control.CaseIDs = []string{"case:a", "case:b"}
	b.Control.Argv = append([]string{"pytest"}, witnesses...)
	splitCase := func(id string) OracleCase {
		c := b.Cases[0]
		c.CaseID = id
		return c
	}
	b.Cases = []OracleCase{splitCase("case:a"), splitCase("case:b")}
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("budget-subject", nil)); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("budget-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &b}))
	if err == nil || (r.Oracle != nil && r.Oracle.Qualification == "ready") {
		t.Fatalf("over-budget witness population prepared: %+v %v launches=%d", r, err, f.launches)
	}
	assertFailureKind(t, err, KindLimitExceeded)
	if !strings.Contains(err.Error(), "16-stage result bound") {
		t.Fatalf("over-budget refusal does not name the staged bound: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("over-budget refusal launched the acceptance body: %d", f.launches)
	}
}

// newNativeTargetScratchRedirectFixture builds the none+check fixture whose
// single declared check carries the producer's $scratch slot in its script
// position: the slot root-joins to a genuinely pinned value check A, while
// run-time stage substitution redirects the invocation to the candidate's
// ordinary undeclared script B beside it. The manifest selects the shell
// check only and pins only the shell runtime.
func newNativeTargetScratchRedirectFixture(t *testing.T) *nativeTargetFixture {
	t.Helper()
	const work = "native-target-redirect-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: "chkfix", recipePath: nativeTargetFixtureName("chkfix", "recipe.json"), materializer: "none", runner: "check"}
	f.controlArgv = []string{"check", "shell-value"}
	shellVersion := liveVersionForTest(t, "bash", `version ([0-9][0-9.]*)`)
	redirect := "$scratch/snapshot/" + f.root + "/scripts/check.sh"
	pinnedA := nativeTargetFixtureName(f.root, redirect)
	raw, err := json.Marshal(map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "none",
		"runner":        "check",
		"root":          f.root,
		"harness_files": []string{pinnedA},
		"support_files": []string{},
		"cases":         map[string][]string{"case:fixture": {"shell-value"}},
		"checks":        []map[string]any{{"witness": "shell-value", "argv": []string{"bash", redirect}}},
		"pin":           map[string]string{"shell": shellVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.manifestJSON = string(raw)
	// A is the pinned value check; B is the ordinary undeclared script the
	// candidate owns. Both honestly expect 7 at the recipe commit.
	honest := "#!/usr/bin/env bash\nset -euo pipefail\ntest \"$(cat value.txt)\" = \"7\"\n"
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "value.txt"): "7\n",
		pinnedA: honest,
		nativeTargetFixtureName(f.root, "scripts/check.sh"): honest,
		f.recipePath: f.manifestJSON,
	})
	f.commitAll(t, "pinned redirect harness")
	f.seedTargetAuthority(t)
	return f
}

// redirectFalseGreen completes the pre-fix attack: the candidate moves its
// own undeclared script and the production value to 8 while the pinned
// value check A stays immutable at 7, and the redirected check grades the
// candidate's copy — a pass A would have failed. It returns the
// qualification the store recorded.
func (f *nativeTargetFixture) redirectFalseGreen(t *testing.T) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "scripts", "check.sh"), []byte("#!/usr/bin/env bash\nset -euo pipefail\ntest \"$(cat value.txt)\" = \"8\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "value.txt"), []byte("8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "redirected-candidate")
	if f.bundle.Control.RecipeSource.CommitOID == f.head {
		t.Fatal("the pinned recipe moved with the candidate commit")
	}
	pinned, err := os.ReadFile(filepath.Join(f.repo, f.root, "$scratch", "snapshot", f.root, "scripts", "check.sh"))
	if err != nil || !strings.Contains(string(pinned), `= "7"`) {
		t.Fatalf("the pinned value check did not stay immutable at 7: %q %v", pinned, err)
	}
	o := f.authorize(t, "redirected-candidate", f.actor)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("redirected-execute", &o))
	if err != nil || r.Oracle == nil {
		t.Fatalf("redirect execute: %+v %v", r, err)
	}
	return r.Oracle.Qualification
}

// The producer's $scratch slot in a check's script position redirects the
// run from the pinned harness bytes to a candidate-owned path beside them:
// every caller-owned argument is literal, so a conforming store refuses
// the manifest unavailable before any program launches. The pre-fix
// demonstration branch records the actual false green first.
func TestNativeTargetAxisScratchRedirectScriptSlot(t *testing.T) {
	f := newNativeTargetScratchRedirectFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("redirect-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}))
	if err == nil && r.Oracle != nil && r.Oracle.Qualification == "ready" {
		t.Fatalf("hostile $scratch script slot was admitted and qualified %q (launches=%d): the pinned value check never ran", f.redirectFalseGreen(t), f.launches)
	}
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "bounded literal arguments") {
		t.Fatalf("redirect script slot refused with the wrong message: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("redirect refusal launched the acceptance body: %d", f.launches)
	}
}

// newNativeTargetOptionScriptFixture builds the none+check fixture whose
// single declared check names the option-shaped pinned file --version in
// its script slot: the manifest genuinely pins that file, so HarnessFiles
// membership alone carries the slot past admission. The interpreter
// resolves the slot as its own option and exits zero without grading the
// pinned body. The interpreter decides whether the false green is
// reachable: python3's preparation helper reads the pinned bytes (a valid
// python body) honestly, while bash's -n preparation helper refuses
// --version as an option by accident of its own argument parser.
func newNativeTargetOptionScriptFixture(t *testing.T, interpreter string) *nativeTargetFixture {
	t.Helper()
	const work = "native-target-option-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: "chkfix", recipePath: nativeTargetFixtureName("chkfix", "recipe.json"), materializer: "none", runner: "check"}
	witness := "python-value"
	pin := map[string]string{"python": liveVersionForTest(t, "python3", `Python ([0-9][0-9.]*)`)}
	argv := []string{interpreter, "--version"}
	body := "import pathlib\nimport sys\n\nsys.exit(0 if pathlib.Path(\"value.txt\").read_text().strip() == \"7\" else 1)\n"
	if interpreter == "bash" {
		witness = "shell-value"
		pin = map[string]string{"shell": liveVersionForTest(t, "bash", `version ([0-9][0-9.]*)`)}
		body = "#!/usr/bin/env bash\nset -euo pipefail\ntest \"$(cat value.txt)\" = \"7\"\n"
	}
	f.controlArgv = []string{"check", witness}
	raw, err := json.Marshal(map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "none",
		"runner":        "check",
		"root":          f.root,
		"harness_files": []string{nativeTargetFixtureName(f.root, "--version")},
		"support_files": []string{},
		"cases":         map[string][]string{"case:fixture": {witness}},
		"checks":        []map[string]any{{"witness": witness, "argv": argv}},
		"pin":           pin,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.manifestJSON = string(raw)
	// The candidate's production value is 8: the pinned body would fail if
	// any interpreter ever read it, so a recorded pass is a false witness.
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "value.txt"): "8\n",
		nativeTargetFixtureName(f.root, "--version"): body,
		f.recipePath: f.manifestJSON,
	})
	f.commitAll(t, "pinned option script")
	f.seedTargetAuthority(t)
	return f
}

// optionFalseGreen completes the pre-fix attack for the python3 slot:
// preparation's pinned-file helper reads the option-shaped bytes honestly,
// and execution resolves the slot as python3's own option — the version
// banner replaces the graded body, the host-observed exit is zero, and the
// witness qualifies although the pinned body would have failed the
// candidate's production value of 8. It returns the recorded
// qualification.
func (f *nativeTargetFixture) optionFalseGreen(t *testing.T) string {
	t.Helper()
	o := f.authorize(t, "option-candidate", f.actor)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("option-execute", &o))
	if err != nil || r.Oracle == nil {
		t.Fatalf("option script slot execute: %+v %v", r, err)
	}
	pinned, err := os.ReadFile(filepath.Join(f.repo, f.root, "--version"))
	if err != nil || !strings.Contains(string(pinned), `== "7"`) {
		t.Fatalf("the pinned check body did not stay immutable at 7: %q %v", pinned, err)
	}
	return r.Oracle.Qualification
}

// A manifest can pin a file whose name is an interpreter option and name it
// in the script slot: HarnessFiles membership alone admitted it, and the
// interpreter resolved the slot as its own option — preparation and
// execution exited zero without grading the pinned body, a true witness
// for a false check. The script slot is a literal file operand, so a
// conforming store refuses the manifest unavailable before preparation is
// ready, with no acceptance launch. The pre-fix demonstration branch
// records the actual false witness first.
func TestNativeTargetAxisOptionScriptSlot(t *testing.T) {
	f := newNativeTargetOptionScriptFixture(t, "python3")
	r, err := f.s.VerifyWorktree(context.Background(), f.request("option-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}))
	if err == nil && r.Oracle != nil && r.Oracle.Qualification == "ready" {
		f.prepare = r
		t.Fatalf("interpreter option script slot was admitted and qualified %q (launches=%d): the pinned check body never ran", f.optionFalseGreen(t), f.launches)
	}
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "literal file operand") {
		t.Fatalf("option script slot refused with the wrong message: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("option script slot refusal launched the acceptance body: %d", f.launches)
	}
}

// The same grammar boundary covers the bash slot the manifest grammar
// admits just as well: bash resolves --version as its own option at
// execution time, and only its preparation helper refuses the option — by
// accident of its own argument parser, never by the store's admission
// rule. The structural refusal is the only control.
func TestNativeTargetAxisOptionScriptSlotBash(t *testing.T) {
	f := newNativeTargetOptionScriptFixture(t, "bash")
	r, err := f.s.VerifyWorktree(context.Background(), f.request("option-bash-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}))
	if err == nil && r.Oracle != nil && r.Oracle.Qualification == "ready" {
		f.prepare = r
		t.Fatalf("bash option script slot was admitted and qualified %q (launches=%d)", f.optionFalseGreen(t), f.launches)
	}
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "literal file operand") {
		t.Fatalf("bash option script slot refused with the wrong message: %v", err)
	}
	if f.launches != 0 {
		t.Fatalf("bash option script slot refusal launched the acceptance body: %d", f.launches)
	}
}

// newNativeTargetSharedArgvFixture builds the none+check fixture whose two
// distinct declared witnesses legitimately invoke one identical argv: the
// check family's identity is each declared invocation, not a unique argv.
// The manifest selects the shell check only and pins only the shell
// runtime.
func newNativeTargetSharedArgvFixture(t *testing.T) *nativeTargetFixture {
	t.Helper()
	const work = "native-target-shared-argv-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: "chkfix", recipePath: nativeTargetFixtureName("chkfix", "recipe.json"), materializer: "none", runner: "check"}
	f.controlArgv = []string{"check", "shell-first", "shell-second"}
	shellVersion := liveVersionForTest(t, "bash", `version ([0-9][0-9.]*)`)
	raw, err := json.Marshal(map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "none",
		"runner":        "check",
		"root":          f.root,
		"harness_files": []string{nativeTargetFixtureName(f.root, "scripts/check.sh")},
		"support_files": []string{},
		"cases":         map[string][]string{"case:fixture": {"shell-first", "shell-second"}},
		"checks": []map[string]any{
			{"witness": "shell-first", "argv": []string{"bash", "scripts/check.sh", "--expected", "7"}},
			{"witness": "shell-second", "argv": []string{"bash", "scripts/check.sh", "--expected", "7"}},
		},
		"pin": map[string]string{"shell": shellVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.manifestJSON = string(raw)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "value.txt"):        "7\n",
		nativeTargetFixtureName(f.root, "scripts/check.sh"): "#!/usr/bin/env bash\nset -euo pipefail\nexpected=\"\"\nwhile [[ $# -gt 0 ]]; do\n  case \"$1\" in\n    --expected) expected=\"$2\"; shift 2;;\n    *) shift;;\n  esac\ndone\ntest \"$(cat value.txt)\" = \"$expected\"\n",
		f.recipePath: f.manifestJSON,
	})
	f.commitAll(t, "pinned shared argv harness")
	f.seedAuthority(t, seed)
	return f
}

// Two distinct declared checks may legitimately invoke one identical argv:
// the check family grades each declared invocation, so both physical
// checks are separate witnesses. A first-match argv lookup folds both
// observed stages into the first witness and refuses the truthful pass as
// a duplicated population; each executed stage's trusted ordinal joins it
// to the sorted selected witness set instead. A genuine production break
// still fails truthfully.
func TestNativeTargetAxisSharedArgvWitnesses(t *testing.T) {
	f := newNativeTargetSharedArgvFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("shared-execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 2 {
		t.Fatalf("shared-argv dual invocation did not pass on its own ordinals: %+v %v launches=%d", r, err, f.launches)
	}
	if !slices.Equal(r.Oracle.ObservedWitnessIDs, []string{"shell-first", "shell-second"}) {
		t.Fatalf("observed witnesses %v lose either declared invocation", r.Oracle.ObservedWitnessIDs)
	}
	if len(r.Oracle.Stages) != 2 || r.Oracle.Stages[0].Name != "test" || r.Oracle.Stages[1].Name != "test" || r.Oracle.Stages[0].ExitCode != 0 || r.Oracle.Stages[1].ExitCode != 0 {
		t.Fatalf("shared-argv receipt did not record both ordered passing stages: %+v", r.Oracle.Stages)
	}
	if err := os.WriteFile(filepath.Join(f.repo, f.root, "value.txt"), []byte("8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "shared-broken")
	o := f.authorize(t, "shared-broken", f.actor)
	r, err = f.s.VerifyWorktree(context.Background(), f.request("shared-broken-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 || f.launches != 3 {
		t.Fatalf("shared-argv broken candidate %+v %v launches=%d", r, err, f.launches)
	}
}

// Candidate application bytes reach grading through the pinned harness: an
// undeclared test-shaped module is neither stripped nor replaced by a pin.
func TestNativeTargetPytestScopeApplication(t *testing.T) {
	for _, root := range []string{".", "pyfix"} {
		t.Run("root="+root, func(t *testing.T) {
			f := newNativeTargetFixtureAtRoot(t, root)
			writeRepoFilesForTest(t, f.repo, map[string]string{
				nativeTargetFixtureName(root, "app/test_mode.py"):    "VALUE = 7\n",
				nativeTargetFixtureName(root, "tests/test_value.py"): "from app.test_mode import VALUE\n\n\ndef test_value():\n    assert VALUE == 7\n",
			})
			f.commitAndPrepareRepin(t, "mode-pinned")
			o := f.authorize(t, "mode-pinned", f.actor)
			r, err := f.s.VerifyWorktree(context.Background(), f.request("mode-pinned-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 1 {
				t.Fatalf("pinned application mode execute %+v %v launches=%d", r, err, f.launches)
			}
			// The pin stays at the previous head; execution grades candidate bytes.
			if err := os.WriteFile(filepath.Join(f.repo, root, "app", "test_mode.py"), []byte("VALUE = 8\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.commitAndPrepare(t, "mode-regressed")
			o = f.authorize(t, "mode-regressed", f.actor)
			r, err = f.s.VerifyWorktree(context.Background(), f.request("mode-regressed-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 || f.launches != 2 {
				t.Fatalf("unpinned application regression escaped grading: %+v %v launches=%d", r, err, f.launches)
			}
			if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "fail"); err != nil {
				t.Fatalf("truthful application failure receipt refused: %v", err)
			}
			if err := os.WriteFile(filepath.Join(f.repo, root, "app", "test_mode.py"), []byte("VALUE = 7\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.commitAndPrepare(t, "mode-repaired")
			o = f.authorize(t, "mode-repaired", f.actor)
			r, err = f.s.VerifyWorktree(context.Background(), f.request("mode-repaired-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 3 {
				t.Fatalf("genuine application repair %+v %v launches=%d", r, err, f.launches)
			}
		})
	}
}

// nativeTargetHostilePytestConfig renders one candidate-owned root config
// body whose addopts name an option pytest must reject: any stage that
// consumes the file instead of the pinned selection fails the run.
func nativeTargetHostilePytestConfig(name string) string {
	switch name {
	case "pytest.toml", ".pytest.toml":
		return "[pytest]\naddopts = [\"--definitely-invalid-native-option\"]\n"
	case "setup.cfg":
		return "[tool:pytest]\naddopts = --definitely-invalid-native-option\n"
	default:
		return "[pytest]\naddopts = --definitely-invalid-native-option\n"
	}
}

// No candidate-owned configuration can determine pytest behavior: every
// stage names the pinned root config, so each freely mutable rival name is
// refused any influence, and a pytest section added to the already-pinned
// dependency manifest refuses through dependency byte equality.
func TestNativeTargetPytestScopeCandidateConfig(t *testing.T) {
	for _, root := range []string{".", "pyfix"} {
		t.Run("root="+root, func(t *testing.T) {
			for _, name := range []string{"pytest.toml", ".pytest.toml", "pytest.ini", ".pytest.ini", "tox.ini", "setup.cfg"} {
				t.Run(name, func(t *testing.T) {
					f := newNativeTargetFixtureAtRoot(t, root)
					if root != "." {
						writeRepoFilesForTest(t, f.repo, map[string]string{
							"pytest.ini": nativeTargetHostilePytestConfig("pytest.ini"),
						})
					}
					writeRepoFilesForTest(t, f.repo, map[string]string{
						nativeTargetFixtureName(root, name):          nativeTargetHostilePytestConfig(name),
						nativeTargetFixtureName(root, "tests/"+name): nativeTargetHostilePytestConfig(name),
					})
					f.commitAndPrepare(t, "hostile-config")
					o := f.authorize(t, "hostile-config", f.actor)
					r, err := f.s.VerifyWorktree(context.Background(), f.request("hostile-config-execute", &o))
					if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 1 {
						t.Fatalf("hostile candidate config %s escaped the pinned selection: %+v %v launches=%d", name, r, err, f.launches)
					}
				})
			}
		})
	}
	t.Run("pinned manifest", func(t *testing.T) {
		f := newNativeTargetFixture(t)
		project := filepath.Join(f.repo, f.root, "pyproject.toml")
		data, err := os.ReadFile(project)
		if err != nil {
			t.Fatal(err)
		}
		changed := string(data) + "\n[tool.pytest.ini_options]\naddopts = \"--definitely-invalid-native-option\"\n"
		if err := os.WriteFile(project, []byte(changed), 0600); err != nil {
			t.Fatal(err)
		}
		f.commitAll(t, "manifest-config")
		if _, err := f.s.VerifyWorktree(context.Background(), f.request("manifest-config-subject", nil)); err != nil {
			t.Fatal(err)
		}
		_, err = f.s.VerifyWorktree(context.Background(), f.request("manifest-config-prepare", &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}))
		if err == nil || !strings.Contains(err.Error(), "candidate dependency inputs differ from pinned preparation") {
			t.Fatalf("pinned dependency manifest did not refuse through byte equality: %v", err)
		}
		if f.launches != 0 {
			t.Fatalf("pinned manifest refusal launched the acceptance body: launches=%d", f.launches)
		}
	})
}

// Explicit collection grades exactly the declared harness population: an
// extra function appended to a declared file refuses the exact population.
func TestNativeTargetPytestScopeDeclaredCollection(t *testing.T) {
	f := newNativeTargetFixture(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "tests/test_unpinned.py"): "def test_unpinned_failure():\n    assert False\n",
		nativeTargetFixtureName(f.root, "app/test_mode.py"):       "VALUE = 7\n\n\ndef render_value():\n    return VALUE\n\n\ndef test_mode_value():\n    return VALUE\n",
	})
	f.commitAndPrepareRepin(t, "undeclared")
	o := f.authorize(t, "undeclared", f.actor)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("undeclared-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 1 {
		t.Fatalf("undeclared test-shaped files entered grading: %+v %v launches=%d", r, err, f.launches)
	}
	// The appended function rides the declared file: a selected-node
	// workaround cannot restore the exact population.
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "tests/test_value.py"): "from app.value import VALUE\n\n\ndef test_value():\n    assert VALUE == 7\n\n\ndef test_value_extra():\n    assert VALUE == 7\n",
	})
	f.commitAll(t, "extra-declared-witness")
	f.bundle.Control.RecipeSource.CommitOID = f.head
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("extra-subject", nil)); err != nil {
		t.Fatal(err)
	}
	prepare := NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &f.bundle}
	_, err = f.s.VerifyWorktree(context.Background(), f.request("extra-prepare", &prepare))
	if err == nil || !strings.Contains(err.Error(), "collected population differs from the exact selected witnesses") {
		t.Fatalf("extra declared witness did not refuse the exact population: %v", err)
	}
	if f.launches != 1 {
		t.Fatalf("population refusal launched the acceptance body: launches=%d", f.launches)
	}
}

// The overlay restores declared support bytes and strips undeclared
// conftest.py files: a poisoned or ancestor conftest cannot reach grading.
func TestNativeTargetPytestScopePinnedSupport(t *testing.T) {
	for _, root := range []string{".", "pyfix"} {
		t.Run("root="+root, func(t *testing.T) {
			f := newNativeTargetFixtureAtRoot(t, root)
			var manifest map[string]any
			if err := json.Unmarshal([]byte(f.manifestJSON), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["support_files"] = []string{nativeTargetFixtureName(root, "tests/conftest.py")}
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.repo, f.recipePath), raw, 0600); err != nil {
				t.Fatal(err)
			}
			writeRepoFilesForTest(t, f.repo, map[string]string{
				nativeTargetFixtureName(root, "tests/conftest.py"):   "import pytest\n\n\n@pytest.fixture\ndef pinned_seven():\n    return 7\n",
				nativeTargetFixtureName(root, "tests/test_value.py"): "from app.value import VALUE\n\n\ndef test_value(pinned_seven):\n    assert VALUE == pinned_seven\n",
			})
			f.commitAndPrepareRepin(t, "pinned-support")
			o := f.authorize(t, "pinned-support", f.actor)
			r, err := f.s.VerifyWorktree(context.Background(), f.request("pinned-support-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 1 {
				t.Fatalf("pinned support fixture %+v %v launches=%d", r, err, f.launches)
			}
			files := map[string]string{
				nativeTargetFixtureName(root, "tests/conftest.py"): "import pytest\n\n\n@pytest.fixture\ndef pinned_seven():\n    return 8\n",
				nativeTargetFixtureName(root, "conftest.py"):       "raise RuntimeError(\"hostile candidate conftest\")\n",
			}
			if root != "." {
				files["conftest.py"] = "raise RuntimeError(\"hostile ancestor conftest\")\n"
			}
			writeRepoFilesForTest(t, f.repo, files)
			f.commitAndPrepare(t, "poisoned-support")
			o = f.authorize(t, "poisoned-support", f.actor)
			r, err = f.s.VerifyWorktree(context.Background(), f.request("poisoned-support-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 2 {
				t.Fatalf("poisoned support candidate escaped the pinned overlay: %+v %v launches=%d", r, err, f.launches)
			}
		})
	}
}

// A conftest.py declared as an explicit whole-file harness collect target
// admits real recipes that pin it: it contributes no witness of its own, so
// grading still observes exactly the selected node while its pinned fixture
// loads.
func TestNativeTargetPytestScopeHarnessConftest(t *testing.T) {
	for _, root := range []string{".", "pyfix"} {
		t.Run("root="+root, func(t *testing.T) {
			f := newNativeTargetFixtureAtRoot(t, root)
			var manifest map[string]any
			if err := json.Unmarshal([]byte(f.manifestJSON), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["harness_files"] = append(manifest["harness_files"].([]any), nativeTargetFixtureName(root, "tests/conftest.py"))
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.repo, f.recipePath), raw, 0600); err != nil {
				t.Fatal(err)
			}
			writeRepoFilesForTest(t, f.repo, map[string]string{
				nativeTargetFixtureName(root, "tests/conftest.py"):   "import pytest\n\n\n@pytest.fixture\ndef seven():\n    return 7\n",
				nativeTargetFixtureName(root, "tests/test_value.py"): "from app.value import VALUE\n\n\ndef test_value(seven):\n    assert VALUE == seven\n",
			})
			f.commitAndPrepareRepin(t, "harness-conftest")
			o := f.authorize(t, "harness-conftest", f.actor)
			r, err := f.s.VerifyWorktree(context.Background(), f.request("harness-conftest-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 1 {
				t.Fatalf("explicit harness conftest collect target %+v %v launches=%d", r, err, f.launches)
			}
			if !slices.Equal(r.Oracle.ObservedWitnessIDs, []string{"tests/test_value.py::test_value"}) {
				t.Fatalf("conftest collect target disturbed the exact witness set: %v", r.Oracle.ObservedWitnessIDs)
			}
		})
	}
}

// With no root config declared, the pinned dependency manifest is the
// fallback -c selection, and hostile candidate configs stay uninfluential.
func TestNativeTargetPytestScopeFallback(t *testing.T) {
	for _, root := range []string{".", "pyfix"} {
		t.Run("root="+root, func(t *testing.T) {
			f := newNativeTargetFixtureAtRoot(t, root)
			var manifest map[string]any
			if err := json.Unmarshal([]byte(f.manifestJSON), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["harness_files"] = []string{nativeTargetFixtureName(root, "tests/test_value.py")}
			recipe, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.repo, f.recipePath), recipe, 0600); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				nativeTargetFixtureName(root, "pytest.ini"): nativeTargetHostilePytestConfig("pytest.ini"),
			}
			if root != "." {
				files["tox.ini"] = nativeTargetHostilePytestConfig("tox.ini")
			}
			writeRepoFilesForTest(t, f.repo, files)
			f.commitAndPrepareRepin(t, "fallback")
			o := f.authorize(t, "fallback", f.actor)
			r, err := f.s.VerifyWorktree(context.Background(), f.request("fallback-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 1 {
				t.Fatalf("fallback manifest selection %+v %v launches=%d", r, err, f.launches)
			}
		})
	}
}
