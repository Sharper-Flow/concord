package store

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// nativeTargetOraclePlan is the family-specific half of one target_runner_v1
// producer plan.
type nativeTargetOraclePlan struct {
	Recipe      nativeTargetOracleManifest `json:"recipe"`
	RecipeFile  NativeOracleFile           `json:"recipe_file"`
	Environment nativeTargetEnvironment    `json:"environment"`
	// DependencyFiles are the byte-equal candidate and recipe dependency
	// manifest and lock digests. They stay candidate inputs, never overlay
	// bytes, so a candidate cannot quietly change its own dependency
	// obligation. The none materializer leaves them empty.
	DependencyFiles []NativeOracleFile `json:"dependency_files"`
	GitMetadata     *NativeGitMetadata `json:"git_metadata,omitempty"`
	// A scoped repair keeps its original readiness view, not a relabelled
	// preparation. Its actual candidate remains in GitMetadata.
}

// prepareCommand returns the canonical prepare lease command for the
// runner: the collection stage for pytest, or the first check-input stage
// for check.
func (t *nativeTargetOraclePlan) prepareCommand(control OracleControl) ([]string, error) {
	witnesses, err := nativeTargetCaseWitnesses(t.Recipe, control)
	if err != nil {
		return nil, err
	}
	stages, err := t.stages("prepare", witnesses)
	if err != nil {
		return nil, err
	}
	for _, stage := range stages {
		if stage.Name == "collect" || stage.Name == "check_inputs" {
			return stage.Argv, nil
		}
	}
	return nil, oracleFailure(KindInvariantViolation, "target plan has no canonical prepare stage", "reconcile the retained native run")
}

// stages composes the producer-derived stage list for one phase. The caller
// never supplies stage commands: every argv derives from the closed
// materializer and runner contracts. The materializer owns the dependency
// stages and their flags; the runner owns collection, check inputs, and the
// graded stages.
func (t *nativeTargetOraclePlan) stages(phase string, witnesses []string) ([]NativeOracleStage, error) {
	m := t.Recipe
	pair, err := nativeTargetPairOf(m)
	if err != nil {
		return nil, err
	}
	var flags, targets []string
	if pair.Runner == nativeTargetRunnerPytest {
		flags, targets = nativeTargetPytestScope(m)
	}
	var stages []NativeOracleStage
	if pair.Materializer == nativeTargetMaterializerUV {
		// --locked asserts manifest/lock freshness and refuses an outdated
		// lockfile; it is never combined with --frozen, which skips exactly
		// that check. Offline, no-build, no-download, and copy-mode flags
		// stay closed materializer policy.
		stages = append(stages, NativeOracleStage{Name: "metadata", Argv: []string{"uv", "sync", "--locked", "--offline", "--no-build", "--no-python-downloads", "--no-install-project", "--no-install-workspace", "--no-install-local", "--link-mode", "copy"}})
		if pair.Runner == nativeTargetRunnerPytest {
			stages = append(stages, NativeOracleStage{Name: "toolcheck", Argv: slices.Concat([]string{"pytest", "--version", "-p", "no:cacheprovider", "-q"}, flags)})
		}
	}
	// Readiness must not qualify a control whose execute result exceeds the
	// public stage envelope, even when preparation itself is shorter.
	if planned := len(stages) + len(witnesses); planned > nativeTargetStageBudget {
		return nil, oracleFailure(KindLimitExceeded,
			"complete target stage plan renders "+strconv.Itoa(planned)+" stages over the "+strconv.Itoa(nativeTargetStageBudget)+"-stage result bound",
			"select fewer witnesses for the pinned runner")
	}
	switch pair.Runner {
	case nativeTargetRunnerVitest:
		return nativeTargetVitestStages(phase, witnesses)
	case nativeTargetRunnerPytest:
		if phase == "prepare" {
			return append(stages, NativeOracleStage{Name: "collect", Argv: slices.Concat([]string{"pytest", "--collect-only", "-q", "-p", "no:cacheprovider"}, flags, targets)}), nil
		}
		for i, witness := range witnesses {
			stages = append(stages, NativeOracleStage{Name: "test", Argv: slices.Concat([]string{"pytest", witness, "-ra", "-q", "-p", "no:cacheprovider", "--junitxml=$scratch/junit-" + strconv.Itoa(i) + ".xml"}, flags)})
		}
		return stages, nil
	case nativeTargetRunnerCheck:
		checks, err := nativeTargetCheckStages(m, phase, witnesses)
		if err != nil {
			return nil, err
		}
		return append(stages, checks...), nil
	default:
		return nil, nativeUnavailable("unsupported target runner")
	}
}

func nativeTargetCheckStages(m nativeTargetOracleManifest, phase string, witnesses []string) ([]NativeOracleStage, error) {
	var stages []NativeOracleStage
	for _, witness := range witnesses {
		check, err := nativeTargetCheckFor(m, witness)
		if err != nil {
			return nil, err
		}
		if phase == "prepare" {
			// Prove availability and syntax of every pinned script the
			// argv names without executing the check.
			for _, script := range nativeTargetCheckScripts(m, check) {
				argv := []string{"bash", "-n", script}
				if check.Argv[0] == "python3" {
					argv = []string{"python3", "-B", "-c", nativeTargetPythonSyntaxCheck, script}
				}
				if check.Argv[0] == "node" {
					argv = []string{"node", "--check", script}
				}
				stages = append(stages, NativeOracleStage{Name: "check_inputs", Argv: argv})
			}
			continue
		}
		stages = append(stages, NativeOracleStage{Name: "test", Argv: append([]string{}, check.Argv...)})
	}
	return stages, nil
}

// nativeTargetPytestScope resolves one pytest plan's declared configuration
// scope from the manifest alone: the flags every pytest stage appends, and
// the root-relative whole-file .py harness targets a collect run names in
// declared order. A root config is an exact rooted member of either declared
// inventory, resolved in pinned priority order; with none declared, the
// pinned dependency manifest configures the run. The candidate filesystem is
// never scanned.
func nativeTargetPytestScope(m nativeTargetOracleManifest) (flags, targets []string) {
	config := m.Pin.DepManifest
	for _, name := range []string{"pytest.toml", ".pytest.toml", "pytest.ini", ".pytest.ini", "pyproject.toml", "tox.ini", "setup.cfg"} {
		rooted, ok := nativeTargetRootedPath(m.Root, name)
		if ok && (slices.Contains(m.HarnessFiles, rooted) || slices.Contains(m.SupportFiles, rooted)) {
			config = name
			break
		}
	}
	flags = []string{"-c", config, "--rootdir=."}
	for _, name := range m.HarnessFiles {
		if strings.HasSuffix(name, ".py") {
			targets = append(targets, strings.TrimPrefix(name, m.Root+"/"))
		}
	}
	return flags, targets
}

// nativeTargetCheckScripts names the one pinned harness script a check's
// argv invokes: the literal script slot directly behind the interpreter
// slot. Manifest validation guarantees that slot names a declared pinned
// harness file, so preparation proves the exact script the check runs —
// never a filename look-alike among its arguments.
func nativeTargetCheckScripts(m nativeTargetOracleManifest, check nativeTargetCheck) []string {
	if len(check.Argv) < 2 {
		return nil
	}
	script, ok := nativeTargetRootedPath(m.Root, check.Argv[1])
	if !ok || !slices.Contains(m.HarnessFiles, script) {
		return nil
	}
	return []string{check.Argv[1]}
}

// nativeTargetPythonSyntaxCheck parses one script's syntax without executing
// it and without writing bytecode.
const nativeTargetPythonSyntaxCheck = `import ast,sys; ast.parse(open(sys.argv[1],encoding='utf-8').read(),sys.argv[1])`

// normalizeNativeTargetOraclePlan completes the shared plan skeleton for one
// target_runner_v1 recipe: strict manifest decode, the closed pair decision,
// the exact selector, pinned harness inputs, the materializer's byte-equal
// dependency inputs, resolved runtime, and the derived stages.
func normalizeNativeTargetOraclePlan(ctx context.Context, r GitRunner, entry WorktreeEntry, p nativeOraclePlan, f NativeOracleFile, raw []byte, req WorktreeVerifyRequest) (nativeOraclePlan, error) {
	b := p.Bundle
	m, err := decodeNativeTargetManifest(raw)
	if err != nil {
		return p, err
	}
	pair, err := nativeTargetPairOf(m)
	if err != nil {
		return p, err
	}
	if err := validateNativeTargetManifest(m, b.Control); err != nil {
		return p, err
	}
	witnesses, err := nativeTargetSelector(m, b.Control)
	if err != nil {
		return p, err
	}
	// The overlay removes the recipe from the snapshot before it writes the
	// declared files, so a recipe that names itself or a dependency input
	// would delete candidate or harness bytes and refuses here.
	recipePath := b.Control.RecipeSource.Path
	if slices.Contains(m.HarnessFiles, recipePath) || slices.Contains(m.SupportFiles, recipePath) {
		return p, nativeUnavailable("recipe path collides with a declared harness or dependency input")
	}
	for _, name := range []string{m.Pin.DepManifest, m.Pin.DepLock} {
		if depPath, ok := nativeTargetRootedPath(m.Root, name); ok && depPath == recipePath {
			return p, nativeUnavailable("recipe path collides with a declared harness or dependency input")
		}
	}
	target := &nativeTargetOraclePlan{Recipe: m, RecipeFile: f}
	p.ManifestFile = f
	if err := readNativeTargetHarnessFiles(ctx, r, entry, &p, m); err != nil {
		return p, err
	}
	depDigest, depRaw, err := readNativeTargetDependencyFiles(ctx, r, entry, p, target, pair)
	if err != nil {
		return p, err
	}
	p.Target = target
	// Runtime resolution returns a fresh environment; the materializer's
	// dependency digest must survive to the finalized identity.
	resolved, err := resolveNativeTargetRuntime(ctx, m, pair)
	if err != nil {
		return p, err
	}
	resolved.DependencyInputsDigest = depDigest
	target.Environment = resolved
	if err := normalizeNativeTargetGitMetadata(ctx, entry.Path, &p); err != nil {
		return p, err
	}
	if err := resolveNativeTargetMaterializerCache(ctx, target, pair, depRaw); err != nil {
		return p, err
	}
	if err := qualifyNativeIsolationArtifacts(ctx, target, depRaw); err != nil {
		return p, err
	}
	tools, err := nativeTargetToolDigests(target.Environment)
	if err != nil {
		return p, err
	}
	target.Environment.Tools = tools
	finalizeNativeTargetEnvironment(&target.Environment)
	stages, err := target.stages(req.Oracle.Phase, witnesses)
	if err != nil {
		return p, err
	}
	p.Stages = stages
	for _, stage := range p.Stages {
		if err := validateWorktreeVerifyCommand(stage.Argv); err != nil {
			return p, err
		}
	}
	return p, nil
}

func readNativeTargetHarnessFiles(ctx context.Context, r GitRunner, entry WorktreeEntry, p *nativeOraclePlan, m nativeTargetOracleManifest) error {
	seen := map[string]bool{}
	total := 0
	for _, name := range append(slices.Clone(m.HarnessFiles), m.SupportFiles...) {
		if seen[name] {
			return nativeUnavailable("manifest paths must be unique")
		}
		seen[name] = true
		file, data, err := nativeGitFile(ctx, r, entry.Path, p.Bundle.Control.RecipeSource.CommitOID, name)
		if err != nil {
			return err
		}
		total += len(data)
		if total > 4*1024*1024 {
			return oracleFailure(KindLimitExceeded, "named harness inputs exceed 4 MiB", "bound the declared harness and support bytes")
		}
		p.Files = append(p.Files, file)
	}
	slices.SortFunc(p.Files, func(a, b NativeOracleFile) int { return strings.Compare(a.Path, b.Path) })
	return nil
}

func readNativeTargetDependencyFiles(ctx context.Context, r GitRunner, entry WorktreeEntry, p nativeOraclePlan, target *nativeTargetOraclePlan, pair nativeTargetPair) (string, map[string][]byte, error) {
	m := target.Recipe
	var depDigest string
	depRaw := map[string][]byte{}
	if pair.Materializer != nativeTargetMaterializerNone {
		var depBytes []byte
		for _, name := range []string{m.Pin.DepManifest, m.Pin.DepLock} {
			depPath, ok := nativeTargetRootedPath(m.Root, name)
			if !ok {
				return "", nil, nativeUnavailable("supported recipe requires the pinned dependency manifest and lock at the root")
			}
			_, candidateBytes, err := nativeGitFile(ctx, r, entry.Path, p.SubjectCommit, depPath)
			if err != nil {
				return "", nil, nativeUnavailable("supported recipe requires the pinned dependency manifest and lock at the root")
			}
			recipeFile, recipeBytes, err := nativeGitFile(ctx, r, entry.Path, p.Bundle.Control.RecipeSource.CommitOID, depPath)
			if err != nil {
				return "", nil, err
			}
			if !bytes.Equal(candidateBytes, recipeBytes) {
				return "", nil, nativeUnavailable("candidate dependency inputs differ from pinned preparation")
			}
			target.DependencyFiles = append(target.DependencyFiles, recipeFile)
			depRaw[name] = recipeBytes
			depBytes = append(append(depBytes, []byte(recipeFile.SHA256)...), 0)
		}
		depDigest = nativeDigest(depBytes)
	} else {
		// The none materializer declares its dependency-free state into the
		// same identity field, so a recipe that grows dependencies re-pins.
		depDigest = nativeDigest([]byte("materializer:none"))
	}
	return depDigest, depRaw, nil
}

func resolveNativeTargetMaterializerCache(ctx context.Context, target *nativeTargetOraclePlan, pair nativeTargetPair, depRaw map[string][]byte) error {
	m := target.Recipe
	switch pair.Materializer {
	case nativeTargetMaterializerUV:
		out, err := nativeTargetHostPath(ctx, target.Environment.UV, "cache", "dir")
		if err != nil {
			return nativeTargetUnavailableMaterializer(pair.Materializer, err.Error())
		}
		target.Environment.UVCacheDir = out
	case nativeTargetMaterializerBun:
		// Lock verification and cache presence decide availability before
		// any lease; the run re-derives the same install list from the
		// same pinned bytes and proves every cached byte.
		installs, err := nativeBunPlanInstalls(depRaw[m.Pin.DepManifest], depRaw[m.Pin.DepLock])
		if err != nil {
			return err
		}
		cache, err := nativeBunCacheDir()
		if err != nil {
			return err
		}
		if err := nativeBunMissing(cache, installs); err != nil {
			return err
		}
		target.Environment.NPMCacheDir = cache
	case nativeTargetMaterializerNone:
	}
	return nil
}

// nativeTargetPreparationWitnessesComplete is the target-family half of the
// retained-preparation completeness check: the exact selected witness IDs
// and case map, the exact admitted pair on both plan and result, and no
// fabricated Go test names.
func nativeTargetPreparationWitnessesComplete(o *NativeOracleResult, p nativeOraclePlan) bool {
	witnesses, err := nativeTargetCaseWitnesses(p.Target.Recipe, p.Bundle.Control)
	if err != nil {
		return false
	}
	if len(witnesses) == 0 || !slices.Equal(o.SelectedWitnessIDs, witnesses) || o.SelectedDistinctCount != len(witnesses) || len(o.CaseToWitnessMap) == 0 || !oracleEntryIdentical(o.CaseToWitnessMap, p.Target.Recipe.Cases) {
		return false
	}
	pair, err := nativeTargetPairOf(p.Target.Recipe)
	if err != nil {
		return false
	}
	return o.RecipeKind == nativeTargetKind && o.TargetMaterializer == pair.Materializer && o.TargetRunner == pair.Runner && o.TargetMaterializer == p.Target.Environment.Materializer && o.TargetRunner == p.Target.Environment.Runner && len(o.SelectedTestNames) == 0 && len(o.CaseToTestMap) == 0
}

func nativeTargetHostPath(ctx context.Context, tool string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, tool, args...).Output() //nolint:gosec // resolved executable, fixed argv.
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(out))
	if value == "" || !filepath.IsAbs(value) {
		return "", nativeUnavailable("tool resolved no absolute host path")
	}
	return value, nil
}

// nativeTargetToolDigests digests every resolved pinned executable into the
// environment's stable tool inventory.
func nativeTargetToolDigests(e nativeTargetEnvironment) ([]NativeOracleFile, error) {
	var tools []NativeOracleFile
	for _, slot := range []struct{ role, path string }{
		{"uv", e.UV}, {"python", e.Python}, {"bash", e.Bash}, {"bun", e.Bun}, {"node", e.Node}, {"git", e.Git},
	} {
		if slot.path == "" {
			continue
		}
		data, err := os.ReadFile(slot.path)
		if err != nil {
			return nil, nativeUnavailable("pinned executable is unreadable: " + err.Error())
		}
		tools = append(tools, NativeOracleFile{Path: slot.role, SHA256: nativeDigest(data)})
	}
	return tools, nil
}
