package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nativeTargetUVPackagePyproject turns the committed fixture project into an
// installable package with a build backend the private offline cache never
// holds. The materializer's --no-install-project and --no-build policy keeps
// the project uninstalled; only a later re-sync would try to build it.
func nativeTargetUVPackagePyproject(t *testing.T, committed string) string {
	t.Helper()
	const virtual = "[tool.uv]\npackage = false\n"
	if !strings.Contains(committed, virtual) {
		t.Fatalf("committed fixture project no longer declares a virtual uv project:\n%s", committed)
	}
	return strings.Replace(committed, virtual, "[build-system]\nrequires = [\"setuptools>=68\"]\nbuild-backend = \"setuptools.build_meta\"\n", 1)
}

// newNativeTargetUVRunFixture builds a uv+check fixture whose pinned harness
// calls `uv run`, as a Project test wrapper does. The project is an
// installable package, so a `uv run` that re-syncs the environment would
// build and install it inside the candidate snapshot.
func newNativeTargetUVRunFixture(t *testing.T) *nativeTargetFixture {
	t.Helper()
	const work = "native-target-uvrun-work"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{s: seed.store, work: work, repo: t.TempDir(), actor: seed.owner, root: "uvrun", recipePath: "uvrun/recipe.json", materializer: "uv", runner: "check"}
	f.controlArgv = []string{"check", "wrapped"}
	f.installCommittedUVProject(t)
	lock := readNativeTargetUVLockForTest(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "pyproject.toml"):         nativeTargetUVPackagePyproject(t, lock.pyproject),
		nativeTargetFixtureName(f.root, "value.txt"):              "7\n",
		nativeTargetFixtureName(f.root, "scripts/check_value.py"): "import pathlib\nimport sys\n\nimport iniconfig\n\nsys.exit(0 if int(pathlib.Path(\"value.txt\").read_text().strip()) == 7 else 1)\n",
		nativeTargetFixtureName(f.root, "scripts/wrapper.sh"):     "#!/usr/bin/env bash\nset -euo pipefail\nuv run python scripts/check_value.py\n",
	})
	f.regenerateLock(t)
	f.setManifestForTest(t, map[string]any{
		"kind":          "target_runner_v1",
		"materializer":  "uv",
		"runner":        "check",
		"root":          f.root,
		"harness_files": []string{nativeTargetFixtureName(f.root, "scripts/wrapper.sh"), nativeTargetFixtureName(f.root, "scripts/check_value.py")},
		"support_files": []string{},
		"cases":         map[string][]string{"case:fixture": {"wrapped"}},
		"checks":        []map[string]any{{"witness": "wrapped", "argv": []string{"bash", "scripts/wrapper.sh"}}},
		"pin": map[string]string{
			"python":       liveVersionForTest(t, "python3", `Python ([0-9][0-9.]*)`),
			"uv":           liveVersionForTest(t, "uv", `uv ([0-9][0-9.]*)`),
			"shell":        liveVersionForTest(t, "bash", `version ([0-9][0-9.]*)`),
			"dep_manifest": "pyproject.toml",
			"dep_lock":     "uv.lock",
		},
	})
	f.commitAll(t, "pinned uv run wrapper harness")
	f.seedAuthority(t, seed)
	return f
}

// A pinned harness that runs its check through `uv run` consumes the
// materialized environment as sealed: the run neither builds nor installs
// the project into the candidate snapshot, so the receipt qualifies the
// candidate instead of refusing a producer-made build artifact.
func TestNativeTargetAxisUVRunUsesMaterializedEnvironment(t *testing.T) {
	f := newNativeTargetUVRunFixture(t)
	r, err := f.s.VerifyWorktree(context.Background(), f.request("execute", &f.a))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || f.launches != 1 {
		t.Fatalf("uv run wrapper execute %+v %v launches=%d", r, err, f.launches)
	}
}

// Sealing the materialized environment does not admit an input change: a
// harness that writes an egg-info tree into the candidate itself still
// refuses.
func TestNativeTargetAxisUVRunInTreeBuildArtifactRefuses(t *testing.T) {
	f := newNativeTargetUVRunFixture(t)
	writeRepoFilesForTest(t, f.repo, map[string]string{
		nativeTargetFixtureName(f.root, "scripts/wrapper.sh"): "#!/usr/bin/env bash\nset -euo pipefail\nmkdir -p native_target_uv.egg-info\nprintf 'Metadata-Version: 2.4\\n' > native_target_uv.egg-info/PKG-INFO\nuv run python scripts/check_value.py\n",
	})
	f.commitAll(t, "harness writes a build artifact")
	f.bundle.Control.RecipeSource.CommitOID = f.head
	f.prepareWithCurrentBundle(t, "artifact")
	o := f.authorize(t, "artifact", f.actor)
	_, err := f.s.VerifyWorktree(context.Background(), f.request("artifact-execute", &o))
	if err == nil || !strings.Contains(err.Error(), "consumed candidate inputs changed during execution: "+nativeTargetFixtureName(f.root, "native_target_uv.egg-info/PKG-INFO")) {
		t.Fatalf("in-tree build artifact was not refused: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.repo, f.root, "native_target_uv.egg-info")); !os.IsNotExist(statErr) {
		t.Fatalf("the native run wrote into the claimed worktree: %v", statErr)
	}
}
