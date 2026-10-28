package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// runNativeTargetOracle executes one leased target_runner_v1 plan. It
// materializes the sealed view (recipe commit at preparation, candidate
// commit with the pinned harness overlay at execution), materializes
// dependencies offline through the plan's materializer into private
// scratch, and grades the exact selected witness population through
// host-observed pinned invocations. No stage runs while any SQL transaction
// is open; the caller holds no lease transaction here.
func (s *Store) runNativeTargetOracle(ctx context.Context, r GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, p nativeOraclePlan, capture *nativeStreamCapture, result *NativeOracleResult) (returnErr error) {
	t := p.Target
	m := t.Recipe
	pair, err := nativeTargetPairOf(m)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "concord-native-target-")
	if err != nil {
		return err
	}
	preserveScratch := false
	defer func() {
		if preserveScratch {
			return
		}
		if err := os.RemoveAll(root); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	if err := nativeScratchDirectories(root); err != nil {
		return err
	}
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		return err
	}
	commit := p.SubjectCommit
	if req.Oracle.Phase == "prepare" {
		commit = p.Bundle.Control.RecipeSource.CommitOID
	}
	if err := materializeNativeSnapshot(ctx, r, entry.Path, commit, snapshot); err != nil {
		return err
	}
	if err := nativeTargetOverlayHarness(ctx, r, entry.Path, snapshot, p); err != nil {
		return err
	}
	if err := materializeNativeTargetGit(ctx, entry.Path, root, snapshot, p); err != nil {
		return err
	}
	baseline, err := nativeTargetInputInventory(p, snapshot, true)
	if err != nil {
		return err
	}
	result.InputManifestDigest = nativeTargetInventoryDigest(baseline)
	env := nativeTargetControlledEnv(t, root)
	cwd := filepath.Join(snapshot, filepath.FromSlash(m.Root))
	if !nativeContained(snapshot, cwd) {
		return nativeUnavailable("effective cwd escapes candidate snapshot")
	}
	if pair.Materializer == nativeTargetMaterializerBun {
		// The bun materializer runs in-process after the strict source
		// inventory and before any stage, so the verified tree, the pinned
		// bun link, and the empty private install cache exist before the
		// runner-digest baseline below, which is never reset.
		if err := materializeNativeBunRun(ctx, t, root, cwd); err != nil {
			return err
		}
	}
	if err := nativeTargetInstallRunner(t, root, p.Bundle.Control); err != nil {
		return err
	}
	binaryDigest := ""
	if pair.Materializer == nativeTargetMaterializerNone || pair.Materializer == nativeTargetMaterializerBun {
		// Check runners without a subprocess materializer consume the
		// pinned interpreters (and, for bun, the materialized tree); their
		// closure is digested once up front.
		binaryDigest, err = nativeTargetRunnerDigest(t, root)
		if err != nil {
			return err
		}
		result.BinaryDigest = binaryDigest
	}
	// The run loop owns the stage ordinal: each executed test stage's
	// position among the producer-generated test stages joins it to its
	// selected witness, so no caller-supplied index can enter the join.
	run := nativeTargetOracleRun{store: s, req: req, plan: p, capture: capture, result: result,
		root: root, snapshot: snapshot, cwd: cwd, env: env, baseline: baseline, binaryDigest: binaryDigest, recipe: m, preserveScratch: &preserveScratch}
	if err := run.execute(ctx); err != nil {
		return err
	}
	return nativeTargetGitMetadataUnchanged(ctx, entry.Path, p)
}

type nativeTargetOracleRun struct {
	store                             *Store
	req                               WorktreeVerifyRequest
	plan                              nativeOraclePlan
	recipe                            nativeTargetOracleManifest
	capture                           *nativeStreamCapture
	result                            *NativeOracleResult
	root, snapshot, cwd, binaryDigest string
	env                               []string
	baseline                          []NativeOracleFile
	testOrdinal                       int
	preserveScratch                   *bool
	isolationExportDigest             string
}

func (n *nativeTargetOracleRun) execute(ctx context.Context) error {
	for _, stage := range n.plan.Stages {
		if err := n.freshGeneratedInputs(); err != nil {
			return err
		}
		if err := n.validateStageInputs(stage.Name); err != nil {
			return err
		}
		exit, stdout, err := n.runStage(ctx, stage)
		if err != nil {
			return err
		}
		if exit != 0 && stage.Name == "test" && n.req.Oracle.Phase == "execute" {
			n.result.Qualification = "fail"
			return nil
		}
		if exit != 0 {
			return nativeUnavailable("native " + stage.Name + " returned a nonzero exit")
		}
		if err := n.consumeStage(ctx, stage, stdout); err != nil {
			return err
		}
	}
	return n.finish()
}

func (n *nativeTargetOracleRun) validateStageInputs(name string) error {
	home, err := os.Lstat(filepath.Join(n.root, "home"))
	if err != nil || !home.IsDir() || home.Mode()&os.ModeSymlink != 0 {
		return nativeUnavailable("disposable scratch no longer realizes the leased plan slot")
	}
	if name == "test" {
		if err := nativeTargetInputsUnchanged(n.plan, n.snapshot, n.baseline, "candidate inputs changed before test launch"); err != nil {
			return err
		}
		if n.binaryDigest != "" {
			current, err := nativeTargetRunnerDigest(n.plan.Target, n.root)
			if err != nil {
				return err
			}
			if current != n.binaryDigest {
				return nativeUnavailable("consumed dependency closure changed before launch")
			}
		}
	}
	return nil
}

func (n *nativeTargetOracleRun) runStage(ctx context.Context, stage NativeOracleStage) (int, []byte, error) {
	s, capture, result := n.store, n.capture, n.result
	argv := nativeTargetStageArgv(n.plan.Target, n.root, slices.Clone(stage.Argv))
	isolation, err := nativeTargetIsolationExport(n.plan.Target, n.root, n.snapshot, n.cwd, argv)
	if err != nil {
		return -1, nil, err
	}
	if isolation != nil {
		if n.isolationExportDigest != "" && n.isolationExportDigest != isolation.Fingerprint {
			return -1, nil, nativeIsolationUnavailable("consumed export changed before stage")
		}
		n.isolationExportDigest = isolation.Fingerprint
		n.result.IsolationPolicy = n.recipe.Isolation
		n.result.IsolationFingerprint = nativeIsolationEnvironmentDigest(n.plan.Target.Environment.Isolation)
		n.result.IsolationEnforcement = "unavailable"
	}
	var launched func()
	if stage.Name == "test" {
		launched = s.nativeOracleProgramLaunched
	}
	if s.nativeOracleStageObserved != nil {
		s.nativeOracleStageObserved(stage.Name, n.cwd, slices.Clone(argv), slices.Clone(n.env))
	}
	stageCtx := ctx
	cancel := func() {}
	if stage.Name == "test" {
		var err error
		stageCtx, cancel, err = nativeOracleTestContext(ctx, n.req)
		if err != nil {
			return 0, nil, err
		}
	}
	stageCapture := nativeTargetStageCapture(stage.Name, capture)
	so, se := 0, 0
	if stageCapture == capture {
		so, se = len(capture.stdout), len(capture.stderr)
	}
	exit, runErr := runNativeOracleStage(stageCtx, n.cwd, argv, n.env, nil, stageCapture, launched, isolation)
	cancel()
	if isolation != nil {
		runErr = n.observeIsolatedStage(&stage, argv, isolation, stageCapture, runErr)
	}
	stage.ExitCode = exit
	stage.StdoutOffset = so
	stage.StderrOffset = se
	stage.StdoutLength = len(stageCapture.stdout) - so
	stage.StderrLength = len(stageCapture.stderr) - se
	if stageCapture != capture {
		// Diagnostics captured beside the retained evidence streams
		// record zero evidence bounds; their bytes are not retained.
		stage.StdoutLength, stage.StderrLength = 0, 0
	}
	result.Stages = append(result.Stages, stage)
	if runErr != nil || !stageCapture.complete {
		return 0, nil, nativeUnavailable(fmt.Sprintf("native %s failed: %v", stage.Name, runErr))
	}
	return exit, stageCapture.stdout, nil
}

func (n *nativeTargetOracleRun) observeIsolatedStage(stage *NativeOracleStage, argv []string, isolation *nativeStageIsolation, stageCapture *nativeStreamCapture, runErr error) error {
	if !stageCapture.isolationReaped {
		// A failed controller may still own cleanup. Never race its
		// retained namespace with RemoveAll, even on an unavailable run.
		if n.preserveScratch != nil {
			*n.preserveScratch = true
		}
	} else {
		stage.IsolationFingerprint = isolation.Fingerprint
		stage.IsolationEnforced = true
		current, checkErr := nativeTargetIsolationExport(n.plan.Target, n.root, n.snapshot, n.cwd, argv)
		if checkErr != nil {
			runErr = checkErr
		} else if current.Fingerprint != isolation.Fingerprint {
			runErr = nativeIsolationUnavailable("consumed export changed during stage")
		}
		if runErr == nil {
			runErr = nativeTargetInputsUnchanged(n.plan, n.snapshot, n.baseline, "consumed candidate inputs changed during isolation")
		}
		if runErr == nil {
			currentDigest, checkErr := nativeTargetRunnerDigest(n.plan.Target, n.root)
			if checkErr != nil {
				runErr = checkErr
			} else if currentDigest != n.binaryDigest {
				runErr = nativeIsolationUnavailable("consumed dependency closure changed during stage")
			}
		}
		if runErr == nil {
			n.result.IsolationEnforcement = "established"
		}
	}
	return runErr
}

func (n *nativeTargetOracleRun) consumeStage(ctx context.Context, stage NativeOracleStage, stdout []byte) error {
	t, root, result := n.plan.Target, n.root, n.result
	var err error
	switch stage.Name {
	case "metadata":
		if err := nativeTargetVerifyMaterialization(ctx, t, root); err != nil {
			return err
		}
		// The materialized environment now binds the consumed runner
		// closure for every uv pair; the runner's own toolcheck
		// re-proves it before launch.
		n.binaryDigest, err = nativeTargetRunnerDigest(t, root)
		if err != nil {
			return err
		}
		result.BinaryDigest = n.binaryDigest
	case "toolcheck":
		if err := nativeTargetVerifyToolVersion(t, stdout); err != nil {
			return err
		}
		// The baseline never resets after the toolcheck: a drift between
		// materialization and here refuses instead of blessing itself.
		current, err := nativeTargetRunnerDigest(t, root)
		if err != nil {
			return err
		}
		if current != n.binaryDigest {
			return nativeUnavailable("consumed dependency closure changed before collection")
		}
	case "collect":
		if t.Recipe.Runner == nativeTargetRunnerVitest {
			report, err := nativeTargetReadVitestReport(nativeTargetReportRoot(t, root), "vitest-collect.json")
			if err != nil {
				return err
			}
			selected, err := nativeTargetSelectedFor(n.plan)
			if err != nil {
				return err
			}
			return nativeTargetVerifyVitestReport(report, selected, "")
		}
		if err := nativeTargetVerifyCollect(n.plan, stdout); err != nil {
			return err
		}
	case "test":
		witnesses, err := nativeTargetStageWitnesses(t, n.plan.Bundle.Control, stage.Argv, n.testOrdinal)
		if err != nil {
			return err
		}
		n.testOrdinal++
		if err := nativeTargetVerifyTestStage(t, root, stage.Argv, stdout); err != nil {
			return err
		}
		result.ObservedWitnessIDs = append(result.ObservedWitnessIDs, witnesses...)
	}
	return nil
}

func (n *nativeTargetOracleRun) finish() error {
	if err := nativeTargetInputsUnchanged(n.plan, n.snapshot, n.baseline, "consumed candidate inputs changed during execution"); err != nil {
		return err
	}
	current, err := nativeTargetRunnerDigest(n.plan.Target, n.root)
	if err != nil {
		return err
	}
	if current != n.binaryDigest {
		return nativeUnavailable("consumed dependency closure changed during execution")
	}
	if n.req.Oracle.Phase == "prepare" {
		n.result.Qualification = "ready"
		return nil
	}
	selected, err := nativeTargetCaseWitnesses(n.recipe, n.plan.Bundle.Control)
	if err != nil {
		return err
	}
	slices.Sort(n.result.ObservedWitnessIDs)
	if !slices.Equal(n.result.ObservedWitnessIDs, selected) {
		return nativeUnavailable("observed witnesses differ from the exact selected population")
	}
	n.result.Qualification = "pass"
	return nil
}

// nativeTargetStageCapture keeps dependency metadata, tool probes, and
// check-input diagnostics out of the retained test-evidence streams.
func nativeTargetStageCapture(name string, capture *nativeStreamCapture) *nativeStreamCapture {
	if name == "metadata" || name == "toolcheck" || name == "check_inputs" {
		return &nativeStreamCapture{complete: true}
	}
	return capture
}

// nativeTargetStageArgv resolves the plan's logical executable slots
// against this run's sealed view. Slot resolution decides only argv[0], the
// executable position the plan owns; every other value is producer-owned
// scratch substitution or a literal argument the recipe pinned, so a data
// argument that reads like a slot name keeps its exact bytes.
func nativeTargetStageArgv(t *nativeTargetOraclePlan, root string, argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	switch argv[0] {
	case "uv":
		argv[0] = t.Environment.UV
	case "bash":
		argv[0] = t.Environment.Bash
	case "python3":
		// The uv materializer runs python3 slots through the
		// materialized venv interpreter; the none materializer uses the
		// pinned host interpreter.
		if t.Environment.Materializer == nativeTargetMaterializerUV {
			argv[0] = filepath.Join(root, "venv", "bin", "python")
		} else {
			argv[0] = t.Environment.Python
		}
	case "node":
		argv[0] = t.Environment.Node
	case "pytest":
		argv[0] = filepath.Join(root, "venv", "bin", "pytest")
	}
	for i, part := range argv[1:] {
		if strings.Contains(part, "$scratch/") {
			argv[i+1] = strings.ReplaceAll(part, "$scratch/", root+string(filepath.Separator))
		}
	}
	if t.Recipe.Isolation != "" && t.Recipe.Runner == nativeTargetRunnerVitest && len(argv) >= 5 {
		argv[4] = filepath.Join(nativeTargetReportRoot(t, root), filepath.Base(argv[4]))
	}
	return argv
}

func nativeTargetReportRoot(t *nativeTargetOraclePlan, root string) string {
	if t.Recipe.Isolation != "" {
		return filepath.Join(root, "reports")
	}
	return root
}

// nativeTargetControlledEnv builds the closed stage environment: inherited
// PATH, LANG, and LC_ALL only, a private HOME and TMPDIR, and the closed
// materializer's declared policy values. Ambient credentials, runner
// overrides, plugin autoload, and user configuration cannot reach a stage.
func nativeTargetControlledEnv(t *nativeTargetOraclePlan, root string) []string {
	var env []string
	for _, key := range []string{"PATH", "LANG", "LC_ALL"} {
		value, ok := os.LookupEnv(key)
		if key == "PATH" && t.Environment.Materializer == nativeTargetMaterializerBun {
			// The pinned bun link precedes the inherited PATH, so a
			// harness's bare bun is the fingerprinted executable.
			value = strings.Join(slices.DeleteFunc([]string{nativeBunBinDir(root), value}, func(s string) bool { return s == "" }), string(os.PathListSeparator))
			ok = true
		}
		if key == "PATH" && t.Environment.Node != "" {
			value = nativeTargetNodeBinDir(root) + string(os.PathListSeparator) + value
			ok = true
		}
		if key == "PATH" && t.Recipe.GitMetadata != "" {
			value = strings.Join(slices.DeleteFunc([]string{nativeTargetGitBinDir(root), value}, func(s string) bool { return s == "" }), string(os.PathListSeparator))
			ok = true
		}
		if ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "HOME="+filepath.Join(root, "home"), "TMPDIR="+filepath.Join(root, "tmp"))
	if t.Environment.Materializer == nativeTargetMaterializerBun {
		env = append(env,
			"BUN_RUNTIME_TRANSPILER_CACHE_PATH=0",
			"DO_NOT_TRACK=1",
			"BUN_INSTALL_CACHE_DIR="+nativeBunInstallCacheDir(root),
		)
	}
	if t.Environment.Materializer == nativeTargetMaterializerUV {
		// UV_NO_SYNC seals the materialized environment: a harness's own
		// `uv run` uses it as the metadata stage left it, instead of
		// re-syncing, which would build and install the project inside the
		// candidate snapshot past the materializer's --no-install-project
		// and --no-build policy. `uv sync` itself ignores the variable.
		env = append(env,
			"UV_NO_SYNC=1",
			"PYTHONDONTWRITEBYTECODE=1",
			"PYTHONNOUSERSITE=1",
			"UV_CACHE_DIR="+t.Environment.UVCacheDir,
			"UV_PROJECT_ENVIRONMENT="+filepath.Join(root, "venv"),
			"UV_PYTHON="+t.Recipe.Pin.Python,
			"UV_PYTHON_DOWNLOADS=never",
			"UV_NO_CONFIG=1",
			"UV_LINK_MODE=copy",
			"UV_OFFLINE=1",
			"UV_NO_PROGRESS=1",
		)
	}
	if t.Recipe.GitMetadata != "" {
		env = append(env,
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0",
			"GIT_CEILING_DIRECTORIES="+root,
		)
	}
	return env
}

// nativeTargetOverlayHarness removes candidate runner discovery inputs and
// writes the exact pinned harness and support bytes. Pinned bytes are
// re-read from the recipe commit and digest-checked, so a worktree change
// between plan and run cannot enter the sealed view.
func nativeTargetOverlayHarness(ctx context.Context, r GitRunner, repo, root string, p nativeOraclePlan) error {
	m := p.Target.Recipe
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = rooted.Close() }()
	if err := rooted.Remove(p.ManifestFile.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	rootDir := filepath.Join(root, filepath.FromSlash(m.Root))
	if m.Runner == nativeTargetRunnerPytest {
		if err := removeMatchingFiles(rootDir, func(name string) bool { return name == "conftest.py" }); err != nil {
			return err
		}
	}
	if m.Materializer == nativeTargetMaterializerBun {
		// Bun loads bunfig files as runner configuration and .env files as
		// process environment; only pinned copies, written below, remain.
		if err := removeMatchingFiles(rootDir, nativeBunAutoloadedName); err != nil {
			return err
		}
	}
	if m.Runner == nativeTargetRunnerVitest {
		if err := removeMatchingFiles(rootDir, func(name string) bool {
			return strings.HasPrefix(name, "vitest.config.") || strings.HasPrefix(name, "vite.config.") || strings.HasPrefix(name, "svelte.config.")
		}); err != nil {
			return err
		}
	}
	for _, f := range p.Files {
		observed, data, err := nativeGitFile(ctx, r, repo, p.Bundle.Control.RecipeSource.CommitOID, f.Path)
		if err != nil {
			return err
		}
		if observed != f {
			return nativeUnavailable("pinned harness bytes changed")
		}
		if err := rooted.MkdirAll(path.Dir(f.Path), 0700); err != nil {
			return err
		}
		if err := rooted.WriteFile(f.Path, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func removeMatchingFiles(dir string, match func(name string) bool) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := removeMatchingFiles(filepath.Join(dir, entry.Name()), match); err != nil {
				return err
			}
			continue
		}
		if match(entry.Name()) {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// nativeTargetScratchDirs names the producer-owned per-run scratch
// directories a candidate commit may never carry: a committed dependency or
// cache tree would shadow the materialized environment.
var nativeTargetScratchDirs = []string{".venv", "__pycache__", ".pytest_cache"}

func nativeTargetInventoryDigest(inventory []NativeOracleFile) string {
	return nativeDigest([]byte(workflowJSON(inventory)))
}

// nativeTargetInputsUnchanged rechecks the consumed snapshot against its
// baseline inventory. A change names its first changed path; a change under
// node_modules adds the materializer route.
func nativeTargetInputsUnchanged(p nativeOraclePlan, snapshot string, baseline []NativeOracleFile, refusal string) error {
	current, err := nativeTargetInputInventory(p, snapshot, false)
	if err != nil {
		return err
	}
	changed := nativeTargetFirstInputChange(baseline, current)
	if changed == "" {
		return nil
	}
	detail := refusal + ": " + changed
	if slices.Contains(strings.Split(changed, "/"), "node_modules") {
		detail += "; the run created or changed " + changed + " inside the candidate tree; a recipe that needs dependencies declares a dependency materializer"
	}
	return nativeUnavailable(detail)
}

// nativeTargetFirstInputChange names the first path, in sorted order, that
// was added, removed, or changed between two sorted inventories.
func nativeTargetFirstInputChange(before, after []NativeOracleFile) string {
	i, j := 0, 0
	for i < len(before) || j < len(after) {
		switch {
		case j >= len(after) || i < len(before) && before[i].Path < after[j].Path:
			return before[i].Path
		case i >= len(before) || after[j].Path < before[i].Path:
			return after[j].Path
		case before[i].SHA256 != after[j].SHA256:
			return before[i].Path
		}
		i++
		j++
	}
	return ""
}

// nativeTargetInputInventory inventories every regular file in the consumed
// snapshot. Declared harness, support, and dependency files must byte-match
// their pins; every other input owns its identity as a typed immutable
// source byte — docs, data, and configuration included — with no extension
// filter. Symlinks and nonregular files refuse. With strict set, the
// producer-owned scratch directories refuse; afterwards the dependency
// materialization inside the snapshot is skipped by the walk. For the bun
// materializer, the only admitted node_modules directory is the producer's
// own tree at the manifest root, and only after materialization: any other,
// empty or not and even inside a skipped scratch subtree, refuses.
func nativeTargetInputInventory(p nativeOraclePlan, root string, strict bool) ([]NativeOracleFile, error) {
	t := p.Target
	bun := t.Recipe.Materializer == nativeTargetMaterializerBun
	producerTree := path.Join(t.Recipe.Root, "node_modules")
	pinned := map[string]string{}
	for _, f := range append(slices.Clone(p.Files), t.DependencyFiles...) {
		pinned[f.Path] = f.SHA256
	}
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rooted.Close() }()
	var inventory []NativeOracleFile
	err = fs.WalkDir(rooted.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if t.Recipe.SvelteKit && name == path.Join(t.Recipe.Root, ".svelte-kit") {
			if strict {
				return nativeUnavailable("candidate carries producer-owned SvelteKit generated inputs")
			}
			target, err := rooted.Readlink(name)
			if err != nil || target != filepath.Join(filepath.Dir(root), "generated") {
				return nativeUnavailable("SvelteKit generated input slot changed")
			}
			return nil
		}
		if d.IsDir() {
			return nativeTargetInventoryDirectory(rooted.FS(), name, d.Name(), producerTree, bun, strict)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nativeUnavailable("source input symlink is unsupported: " + name)
		}
		if !d.Type().IsRegular() {
			return nativeUnavailable("source inputs must remain regular files: " + name)
		}
		data, err := rooted.ReadFile(name)
		if err != nil {
			return err
		}
		if want, ok := pinned[name]; ok {
			if nativeDigest(data) != want {
				return nativeUnavailable("declared pinned input differs from its recipe digest: " + name)
			}
		}
		inventory = append(inventory, NativeOracleFile{Path: name, SHA256: nativeDigest(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(inventory, func(a, b NativeOracleFile) int { return strings.Compare(a.Path, b.Path) })
	return inventory, nil
}

func nativeTargetInventoryDirectory(root fs.FS, name, base, producerTree string, bun, strict bool) error {
	if bun && base == "node_modules" {
		if !strict && name == producerTree {
			return fs.SkipDir
		}
		return nativeUnavailable("candidate carries a materialized dependency or cache tree: " + name)
	}
	if slices.Contains(nativeTargetScratchDirs, base) {
		if strict {
			return nativeUnavailable("candidate carries a materialized dependency or cache tree: " + name)
		}
		if bun {
			if hidden := nativeBunHiddenTree(root, name); hidden != "" {
				return nativeUnavailable("candidate carries a materialized dependency or cache tree: " + hidden)
			}
		}
		return fs.SkipDir
	}
	return nil
}

// nativeTargetVerifyMaterialization proves the just-materialized dependency
// environment: uv installs verify every installed file against its wheel
// RECORD digest and the interpreter version pin.
func nativeTargetVerifyMaterialization(ctx context.Context, t *nativeTargetOraclePlan, root string) error {
	if t.Environment.Materializer != nativeTargetMaterializerUV {
		return nativeUnavailable("dependency metadata stage ran without the uv materializer")
	}
	venv := filepath.Join(root, "venv")
	if err := nativeTargetVerifyVenvRecords(venv); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, filepath.Join(venv, "bin", "python"), "-V").Output() //nolint:gosec // producer-built scratch interpreter, fixed argv.
	if err != nil {
		return nativeTargetUnavailableMaterializer(t.Environment.Materializer, "venv python probe failed: "+err.Error())
	}
	if strings.TrimSpace(string(out)) != "Python "+t.Recipe.Pin.Python {
		return nativeTargetUnavailableMaterializer(t.Environment.Materializer, "venv python does not match the exact pin")
	}
	return nil
}

// nativeTargetVerifyVenvRecords hashes every installed file against its
// wheel RECORD entry. uv verifies downloaded archives once but never re-reads
// cached unpacked bytes, so this gate is the dependency-integrity check for
// the materialized environment: a corrupted cached wheel surfaces here.
// Per the recording-installed-packages spec
// (https://packaging.python.org/en/latest/specifications/recording-installed-packages/),
// an installed project's .dist-info lives alongside its importable modules at
// the top of site-packages, and its RECORD paths resolve against that parent;
// entries may legitimately name the venv's own bin scripts through relative
// parent steps and are verified there. Nested vendored .dist-info directories
// are package data of the installed project, covered by its RECORD hashes, so
// only top-level RECORDs are read directly and a missing one refuses. An
// empty hash is legitimate only for the distribution's RECORD self-entry; any
// other unhashed or mismatching entry refuses, a policy deliberately stricter
// than the spec requires so every installed file is verified.
func nativeTargetVerifyVenvRecords(venv string) error {
	sites, err := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages"))
	if err != nil || len(sites) != 1 {
		return nativeUnavailable("materialized venv has no single site-packages root")
	}
	siteRoot, venvRoot := sites[0], venv
	entries, err := os.ReadDir(siteRoot)
	if err != nil {
		return nativeUnavailable("materialized venv site-packages is unreadable: " + err.Error())
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".dist-info") {
			continue
		}
		name, selfRow := entry.Name(), entry.Name()+"/RECORD"
		data, err := fs.ReadFile(os.DirFS(siteRoot), name+"/RECORD")
		if err != nil {
			return nativeUnavailable("installed distribution has no readable RECORD: " + name)
		}
		rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
		if err != nil {
			return nativeUnavailable("malformed RECORD: " + name)
		}
		for _, row := range rows {
			if err := nativeTargetVerifyRecordRow(siteRoot, venvRoot, name, selfRow, row); err != nil {
				return err
			}
		}
	}
	return nil
}

func nativeTargetVerifyRecordRow(siteRoot, venvRoot, name, selfRow string, row []string) error {
	if len(row) != 3 {
		return nativeUnavailable("malformed RECORD row in " + name)
	}
	rel, digest, size := row[0], row[1], row[2]
	if rel == "" || strings.HasPrefix(rel, "/") {
		return nativeUnavailable("malformed RECORD entry in " + name)
	}
	resolved := filepath.Clean(filepath.Join(siteRoot, rel))
	if resolved != venvRoot && !strings.HasPrefix(resolved, venvRoot+string(filepath.Separator)) {
		return nativeUnavailable("RECORD entry escapes the materialized venv in " + name)
	}
	if digest == "" {
		if rel != selfRow {
			return nativeUnavailable("unhashed RECORD entry is unsupported in " + name + ": " + rel)
		}
		return nil
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nativeUnavailable("RECORD entry is missing from the materialized environment: " + rel)
	}
	if size != "" && parseDecimal(size) != int64(len(raw)) {
		return nativeUnavailable("RECORD size mismatch: " + rel)
	}
	want, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(digest, "sha256="))
	if err != nil {
		return nativeUnavailable("malformed RECORD digest: " + rel)
	}
	if nativeDigest(raw) != "sha256:"+hexEncode(want) {
		return nativeUnavailable("materialized file differs from its wheel RECORD digest: " + rel)
	}
	return nil
}

func parseDecimal(s string) int64 {
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
		n = n*10 + int64(s[i]-'0')
	}
	return n
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, hexDigits[v>>4], hexDigits[v&0x0f])
	}
	return string(out)
}

// nativeTargetVerifyToolVersion pins the installed runner version from the
// host-observed toolcheck output.
func nativeTargetVerifyToolVersion(t *nativeTargetOraclePlan, stdout []byte) error {
	line, _, _ := strings.Cut(strings.TrimSpace(string(stdout)), "\n")
	line = strings.TrimSpace(line)
	if t.Recipe.Runner != nativeTargetRunnerPytest {
		return nativeUnavailable("runner toolcheck ran without the pytest runner")
	}
	if line != "pytest "+t.Recipe.Pin.Pytest {
		return nativeTargetUnavailableMaterializer(t.Environment.Materializer, "installed pytest does not match the exact pin: "+line)
	}
	return nil
}

// nativeTargetSiteDigest digests every regular file in the materialized
// site-packages tree, RECORD files included: the consumed dependency
// closure itself, not only its entry points. Bytecode generation is
// controlled by the closed environment, and a symlinked or nonregular
// closure entry refuses instead of hiding from the walk.
func nativeTargetSiteDigest(t *nativeTargetOraclePlan, root string) ([]byte, error) {
	if t.Environment.Materializer != nativeTargetMaterializerUV {
		return nil, nil
	}
	sites, err := filepath.Glob(filepath.Join(root, "venv", "lib", "python*", "site-packages"))
	if err != nil || len(sites) != 1 {
		return nil, nativeUnavailable("materialized venv has no single site-packages root")
	}
	siteRoot := sites[0]
	rooted, err := os.OpenRoot(siteRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rooted.Close() }()
	var parts [][]byte
	walkErr := fs.WalkDir(rooted.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return nativeUnavailable("materialized dependency closure is unreadable: " + name)
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nativeUnavailable("dependency closure entry is a symlink or nonregular file: " + name)
		}
		data, err := rooted.ReadFile(name)
		if err != nil {
			return nativeUnavailable("dependency closure entry is unreadable: " + name)
		}
		parts = append(parts, []byte(name+"\x00"), data)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return bytes.Join(parts, []byte{0}), nil
}

// nativeTargetRunnerDigest binds the consumed executable closure: the
// materialized runner entry point plus its interpreter for pytest, or the
// exact interpreters the declared checks consume, together with the uv
// materializer's whole installed dependency closure.
func nativeTargetRunnerDigest(t *nativeTargetOraclePlan, root string) (string, error) {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rooted.Close() }()
	var parts [][]byte
	switch {
	case t.Recipe.Runner == nativeTargetRunnerPytest:
		pytest, err := rooted.ReadFile("venv/bin/pytest")
		if err != nil {
			return "", nativeUnavailable("materialized pytest entry point is missing")
		}
		python, err := filepath.EvalSymlinks(filepath.Join(root, "venv", "bin", "python"))
		if err != nil {
			return "", nativeUnavailable("venv python is unreadable")
		}
		interpreter, err := os.ReadFile(python)
		if err != nil {
			return "", nativeUnavailable("venv python is unreadable")
		}
		parts = append(parts, pytest, interpreter)
	default:
		parts, err = nativeTargetCheckInterpreterParts(t, root)
		if err != nil {
			return "", err
		}
	}
	if t.Environment.Materializer == nativeTargetMaterializerBun {
		bun, err := nativeBunRunnerParts(t, root)
		if err != nil {
			return "", err
		}
		parts = append(parts, bun...)
	}
	node, err := nativeTargetNodeRunnerParts(t, root)
	if err != nil {
		return "", err
	}
	parts = append(parts, node...)
	site, err := nativeTargetSiteDigest(t, root)
	if err != nil {
		return "", err
	}
	if len(site) > 0 {
		parts = append(parts, []byte("site-packages\x00"), site)
	}
	if t.Recipe.GitMetadata != "" {
		git, err := nativeTargetGitExecutable(t)
		if err != nil {
			return "", err
		}
		link, err := os.Readlink(filepath.Join(nativeTargetGitBinDir(root), "git"))
		if err != nil || link != t.Environment.Git {
			return "", nativeUnavailable("candidate Git executable slot changed")
		}
		parts = append(parts, []byte("git\x00"), git)
	}
	return nativeDigest(bytes.Join(parts, []byte{0})), nil
}

func nativeTargetCheckInterpreterParts(t *nativeTargetOraclePlan, root string) ([][]byte, error) {
	var parts [][]byte
	interpreters := map[string]bool{}
	for _, check := range t.Recipe.Checks {
		interpreters[check.Argv[0]] = true
	}
	for _, slot := range []struct{ name, resolved string }{
		{"bash", t.Environment.Bash},
		{"python3", t.Environment.Python},
		{"node", t.Environment.Node},
	} {
		if !interpreters[slot.name] {
			continue
		}
		if slot.name == "python3" && t.Environment.Materializer == nativeTargetMaterializerUV {
			// The uv materializer grades python3 checks through the
			// materialized venv interpreter.
			venvPython, err := filepath.EvalSymlinks(filepath.Join(root, "venv", "bin", "python"))
			if err != nil {
				return nil, nativeUnavailable("venv python is unreadable")
			}
			slot.resolved = venvPython
		}
		binary, err := os.ReadFile(slot.resolved)
		if err != nil {
			return nil, nativeUnavailable("pinned interpreter is unreadable: " + slot.name)
		}
		parts = append(parts, []byte(slot.name+"\x00"), binary)
	}
	return parts, nil
}

func nativeTargetSelectedFor(p nativeOraclePlan) ([]string, error) {
	return nativeTargetCaseWitnesses(p.Target.Recipe, p.Bundle.Control)
}

// nativeTargetVerifyCollect proves the complete collected population equals
// the exact selected witness set, from a structured complete collection.
func nativeTargetVerifyCollect(p nativeOraclePlan, stdout []byte) error {
	t := p.Target
	if t.Recipe.Runner != nativeTargetRunnerPytest {
		return nativeUnavailable("collection stage ran without the pytest runner")
	}
	collected, err := nativeTargetPytestCollectOutput(stdout)
	if err != nil {
		return err
	}
	selected, err := nativeTargetSelectedFor(p)
	if err != nil {
		return err
	}
	if !slices.Equal(collected, selected) {
		return nativeUnavailable("collected population differs from the exact selected witnesses")
	}
	return nil
}

var nativeTargetPytestCollectSummary = regexp.MustCompile(`^([0-9]+) tests? collected in `)

// nativeTargetPytestCollectOutput parses the canonical quiet collection
// listing: one node ID per line, then the collected-count summary. Any other
// shape is malformed.
func nativeTargetPytestCollectOutput(stdout []byte) ([]string, error) {
	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) < 2 {
		return nil, nativeUnavailable("pytest collection output is malformed")
	}
	match := nativeTargetPytestCollectSummary.FindStringSubmatch(strings.TrimSpace(lines[len(lines)-1]))
	if match == nil {
		return nil, nativeUnavailable("pytest collection output is malformed")
	}
	collected := make([]string, 0, len(lines)-1)
	for _, line := range lines[:len(lines)-1] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, "::") {
			return nil, nativeUnavailable("pytest collection output is malformed")
		}
		collected = append(collected, line)
	}
	if len(collected) != int(parseDecimal(match[1])) {
		return nil, nativeUnavailable("pytest collection summary differs from its listing")
	}
	slices.Sort(collected)
	return collected, nil
}

// nativeTargetStageWitnesses names the witness one recorded test stage
// covers: the stage's trusted ordinal among the producer-generated test
// stages — the producer renders exactly one test stage per selected
// witness, in the sorted selected order — selects that witness from the
// exact selected population, and the stage's own logical argv must equal
// that witness's declared invocation. The check family's identity is each
// declared invocation, not a unique argv, so two witnesses may share one
// argv and each physical check is its own witness; a first-match lookup
// would fold them into one observed identity.
func nativeTargetStageWitnesses(t *nativeTargetOraclePlan, control OracleControl, argv []string, ordinal int) ([]string, error) {
	selected, err := nativeTargetCaseWitnesses(t.Recipe, control)
	if err != nil {
		return nil, err
	}
	if ordinal < 0 || ordinal >= len(selected) {
		return nil, nativeUnavailable("recorded test stage has no selected witness at its ordinal")
	}
	witness := selected[ordinal]
	switch t.Recipe.Runner {
	case nativeTargetRunnerVitest:
		expected, err := nativeTargetVitestStages("execute", selected)
		if err != nil {
			return nil, err
		}
		if slices.Equal(argv, expected[ordinal].Argv) {
			return []string{witness}, nil
		}
	case nativeTargetRunnerPytest:
		if len(argv) > 1 && argv[1] == witness {
			return []string{witness}, nil
		}
	case nativeTargetRunnerCheck:
		check, err := nativeTargetCheckFor(t.Recipe, witness)
		if err != nil {
			return nil, err
		}
		if len(argv) == len(check.Argv) && slices.Equal(argv, check.Argv) {
			return []string{witness}, nil
		}
	}
	return nil, nativeUnavailable("recorded test stage does not name its selected witnesses")
}

var nativeTargetJUnitSummary = regexp.MustCompile(`(?m)^(FAILED|ERROR|SKIPPED|XFAIL|XPASS) `)
var nativeTargetPytestPassedSummary = regexp.MustCompile(`(?m)^1 passed(,| in |\r|$)`)

type nativeTargetJUnitTestsuites struct {
	Suites []nativeTargetJUnitTestsuite `xml:"testsuite"`
}

type nativeTargetJUnitTestsuite struct {
	Tests     int `xml:"tests,attr"`
	Failures  int `xml:"failures,attr"`
	Errors    int `xml:"errors,attr"`
	Skipped   int `xml:"skipped,attr"`
	TestCases []struct {
		Name string `xml:"name,attr"`
	} `xml:"testcase"`
}

// nativeTargetVerifyTestStage grades one host-observed invocation against
// its runner's structured result channel. The structured channel, not
// candidate stdout, decides the outcome, and skip, todo, xfail, xpass,
// duplicate, foreign, missing, zero-test, and malformed outcomes can never
// pass. A check is its own evidence: the host observed the exact pinned
// argv and the kernel exit status is zero.
func nativeTargetVerifyTestStage(t *nativeTargetOraclePlan, root string, argv []string, stdout []byte) error {
	switch t.Recipe.Runner {
	case nativeTargetRunnerVitest:
		if len(argv) != 6 {
			return nativeUnavailable("Vitest invocation has no exact witness/result channel")
		}
		name := strings.TrimPrefix(argv[4], "$scratch/")
		if name == argv[4] || filepath.Base(name) != name {
			return nativeUnavailable("Vitest result path is not producer-owned")
		}
		report, err := nativeTargetReadVitestReport(nativeTargetReportRoot(t, root), name)
		if err != nil {
			return err
		}
		var selected []string
		for _, identities := range t.Recipe.Cases {
			selected = append(selected, identities...)
		}
		slices.Sort(selected)
		selected = slices.Compact(selected)
		return nativeTargetVerifyVitestReport(report, selected, argv[5])
	case nativeTargetRunnerPytest:
		return nativeTargetVerifyPytestRun(root, argv, stdout)
	case nativeTargetRunnerCheck:
		return nil
	}
	return nativeUnavailable("unsupported target runner")
}

func nativeTargetVerifyPytestRun(root string, argv []string, stdout []byte) error {
	index := -1
	for _, part := range argv {
		if _, rest, ok := strings.Cut(part, "--junitxml=$scratch/junit-"); ok && strings.HasSuffix(rest, ".xml") {
			index = int(parseDecimal(strings.TrimSuffix(rest, ".xml")))
		}
	}
	if index < 0 {
		return nativeUnavailable("pytest run recorded no junit result channel")
	}
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return nativeUnavailable("pytest junit result channel is missing")
	}
	defer func() { _ = rooted.Close() }()
	data, err := rooted.ReadFile("junit-" + strconv.Itoa(index) + ".xml")
	if err != nil {
		return nativeUnavailable("pytest junit result channel is missing")
	}
	var doc nativeTargetJUnitTestsuites
	if xml.Unmarshal(data, &doc) != nil || len(doc.Suites) != 1 {
		return nativeUnavailable("pytest junit result channel is malformed")
	}
	suite := doc.Suites[0]
	if suite.Tests != 1 || suite.Failures != 0 || suite.Errors != 0 || suite.Skipped != 0 || len(suite.TestCases) != 1 {
		return nativeUnavailable("pytest result is not exactly one passing witness: skipped, failed, errored, or duplicated outcomes are present")
	}
	if nativeTargetJUnitSummary.Match(stdout) {
		return nativeUnavailable("pytest reported a non-pass outcome for the selected witness")
	}
	if !nativeTargetPytestPassedSummary.MatchString(string(stdout)) {
		return nativeUnavailable("pytest recorded no single-pass terminal summary")
	}
	return nil
}
