package store

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The target_runner_v1 family runs one target repository's own pinned test
// framework or check scripts through two closed axes inside one recipe:
//
//   - materializer `none`, `uv`, or `bun`: the materializer owns the dependency
//     manifest and lockfile, offline lock-verified materialization into
//     per-run scratch, cache-integrity proof, disabled scripts, and runtime
//     fingerprints. `none` declares a dependency-free recipe.
//   - runner `pytest`, `vitest`, or `check`: the runner owns collection, the exact
//     witness population, and the result grammar. `check` pins each check's
//     full bounded argv and grades the host-observed process exit.
//
// Exactly five pairs are admitted: uv+pytest, none+check, uv+check,
// bun+check, and bun+vitest. The
// admitted set is the closed union below — the pair is decided as one
// discriminated decision, never as two independently accepted enum values —
// and every other combination refuses as unsupported. Adding a member
// requires an approved design and executable obligation tests, not a law
// change (CD-0205 D1).
//
// The manifest below is the entire admitted input surface: everything else a
// caller supplies — shell snippets, result parsers, host paths, stage
// commands — is refused. Producer-consumed stages, environment, and
// witnesses derive only from these fields.
type nativeTargetOracleManifest struct {
	// Kind is exactly "target_runner_v1".
	Kind string `json:"kind"`
	// Materializer is one closed member: none, uv, or bun.
	Materializer string `json:"materializer"`
	// Runner is one closed member: pytest, check, or vitest.
	Runner string `json:"runner"`
	// Root is the contained Project-relative working root. It must equal the
	// control's cwd.
	Root string `json:"root"`
	// HarnessFiles are the pinned runner inputs the producer overlays onto
	// the candidate snapshot from the recipe commit: test files, conftest or
	// runner configuration, setup and reporter files, and check scripts.
	// Candidate discovery inputs are removed before these are written.
	HarnessFiles []string `json:"harness_files"`
	// SupportFiles are pinned data fixtures the harness reads. They are
	// overlaid like HarnessFiles and hash-verified at every input check.
	SupportFiles []string `json:"support_files"`
	// Cases maps each control case to 1 to 8 witness IDs. A witness ID is
	// runner-defined and exact: a pytest node ID, or a declared check's
	// witness string. IDs are unique, nonempty, at most 256 UTF-8 bytes, and
	// carry no control characters.
	Cases map[string][]string `json:"cases"`
	// Checks declares one bounded argv per check witness. Runner pytest
	// refuses this field; runner check requires it, and every selected
	// witness must carry exactly one declared check.
	Checks []nativeTargetCheck `json:"checks,omitempty"`
	// VitestConfig names the root-relative pinned Vitest configuration.
	VitestConfig string `json:"vitest_config,omitempty"`
	// SvelteKit reserves a fresh producer-owned .svelte-kit output per stage.
	SvelteKit bool `json:"sveltekit,omitempty"`
	// Pin carries the exact runtime identities this recipe requires.
	Pin nativeTargetPin `json:"pin"`
}

// nativeTargetCheck is one declared check: its witness identity and the
// exact bounded argv the producer must invoke and observe. Argv values are
// literal argument values, never shell text; argv[0] names one closed
// interpreter slot (bash or python3), and the remaining values are bounded
// literals — script paths under the root, flags, and arguments.
type nativeTargetCheck struct {
	Witness string   `json:"witness"`
	Argv    []string `json:"argv"`
}

// nativeTargetPin names one exact installed runtime per field. An absent or
// nonmatching installed tool is unavailable. The admitted pair decides which
// fields are required and which are foreign, so one axis member's
// requirement can never leak into another pair's recipe.
type nativeTargetPin struct {
	// Python is the exact interpreter version, for example "3.13.7". For
	// uv materialization it pins the interpreter uv selects and the
	// materialized venv must report.
	Python string `json:"python,omitempty"`
	// UV is the exact uv version, for example "0.11.26".
	UV string `json:"uv,omitempty"`
	// Pytest is the exact pytest version installed in the materialized
	// environment (uv+pytest).
	Pytest string `json:"pytest,omitempty"`
	// Shell is the exact bash version, required when a check invokes the
	// bash interpreter slot.
	Shell string `json:"shell,omitempty"`
	// Bun is the exact bun version for bun materialization. It must be a
	// member of nativeBunAdmittedVersions.
	Bun    string `json:"bun,omitempty"`
	Node   string `json:"node,omitempty"`
	Vitest string `json:"vitest,omitempty"`
	Vite   string `json:"vite,omitempty"`
	// DepManifest is the Project-relative dependency manifest path:
	// "pyproject.toml" for uv and "package.json" for bun materialization.
	// The candidate commit's file must equal the recipe commit's byte for
	// byte.
	DepManifest string `json:"dep_manifest,omitempty"`
	// DepLock is the Project-relative lockfile path: "uv.lock" for uv and
	// "bun.lock" for bun materialization. Equality is enforced like
	// DepManifest.
	DepLock string `json:"dep_lock,omitempty"`
}

const (
	nativeTargetKind             = "target_runner_v1"
	nativeTargetMaterializerNone = "none"
	nativeTargetMaterializerUV   = "uv"
	nativeTargetRunnerPytest     = "pytest"
	nativeTargetRunnerCheck      = "check"
	nativeTargetRunnerVitest     = "vitest"
	// nativeTargetWitnessMaxBytes matches the verify-command part bound, so
	// a selected witness stays renderable as one exact argv value.
	nativeTargetWitnessMaxBytes = 256
	// Both phases share the public result's stage envelope.
	nativeTargetStageBudget = 16
)

var nativeTargetVersionPin = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// nativeTargetPair is one admitted materializer/runner decision.
type nativeTargetPair struct {
	Materializer string
	Runner       string
}

// nativeTargetAdmittedPairs is the entire closed union. The receipt join,
// plan normalization, and stage composition all decide through this table,
// so no surface can admit a pair the others refuse.
var nativeTargetAdmittedPairs = []nativeTargetPair{
	{nativeTargetMaterializerUV, nativeTargetRunnerPytest},
	{nativeTargetMaterializerNone, nativeTargetRunnerCheck},
	{nativeTargetMaterializerUV, nativeTargetRunnerCheck},
	{nativeTargetMaterializerBun, nativeTargetRunnerCheck},
	{nativeTargetMaterializerBun, nativeTargetRunnerVitest},
}

// nativeTargetPairOf resolves the manifest's axes as one closed discriminated
// decision. A pair outside the admitted union — including unknown members —
// refuses as unsupported; it never falls back to another pair or family.
func nativeTargetPairOf(m nativeTargetOracleManifest) (nativeTargetPair, error) {
	p := nativeTargetPair{Materializer: m.Materializer, Runner: m.Runner}
	if slices.Contains(nativeTargetAdmittedPairs, p) {
		return p, nil
	}
	return p, nativeUnavailable("unsupported materializer/runner pair")
}

// nativeTargetCaseWitnesses returns the sorted distinct witness IDs the
// manifest maps the control's cases to: the exact witness set an execute run
// must observe passing exactly once.
func nativeTargetCaseWitnesses(m nativeTargetOracleManifest, c OracleControl) ([]string, error) {
	if len(m.Cases) != len(c.CaseIDs) {
		return nil, nativeUnavailable("manifest cases differ from the control")
	}
	set := map[string]bool{}
	for _, id := range c.CaseIDs {
		ids, ok := m.Cases[id]
		if !ok || len(ids) < 1 || len(ids) > 8 {
			return nil, nativeUnavailable("each case must select 1 to 8 literal witnesses")
		}
		local := map[string]bool{}
		for _, witness := range ids {
			if len(witness) < 1 || len(witness) > nativeTargetWitnessMaxBytes || local[witness] || strings.ContainsFunc(witness, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				return nil, nativeUnavailable("manifest witness IDs must be unique bounded visible strings")
			}
			local[witness] = true
			set[witness] = true
		}
	}
	witnesses := make([]string, 0, len(set))
	for witness := range set {
		witnesses = append(witnesses, witness)
	}
	slices.Sort(witnesses)
	return witnesses, nil
}

// nativeTargetSelector derives the control's closed logical argv from the
// runner's selected witnesses and refuses any other shape. The pytest
// control names the exact selected node IDs; the check control is the
// logical "check" identity followed by the exact selected witness strings
// (each check's real invocation is its own declared argv, graded
// separately).
func nativeTargetSelector(m nativeTargetOracleManifest, c OracleControl) ([]string, error) {
	witnesses, err := nativeTargetCaseWitnesses(m, c)
	if err != nil {
		return nil, err
	}
	var logical []string
	switch m.Runner {
	case nativeTargetRunnerPytest:
		logical = append([]string{"pytest"}, witnesses...)
	case nativeTargetRunnerCheck:
		logical = append([]string{"check"}, witnesses...)
	case nativeTargetRunnerVitest:
		logical = append([]string{"vitest"}, witnesses...)
	default:
		return nil, nativeUnavailable("unsupported target runner")
	}
	if err := validateWorktreeVerifyCommand(logical); err != nil {
		return nil, oracleFailure(KindLimitExceeded, "rendered target selector exceeds the bounded verify command shape", "select fewer or shorter witnesses")
	}
	if !slices.Equal(logical, c.Argv) {
		rendered, renderErr := json.Marshal(logical)
		if renderErr != nil {
			return nil, wrapFailure(KindUnavailable, "native_target", "cannot render the closed selector", false, "repair the pinned harness", renderErr)
		}
		return nil, oracleFailure(KindUnavailable, "control argv is not the exact closed "+m.Runner+" selector",
			"set control argv to the exact closed selector "+string(rendered)+" (the "+m.Runner+" runner identity followed by the sorted distinct case witnesses), or record the corrected control revision through record_worker_job, then request explicit fresh native preparation")
	}
	return witnesses, nil
}

// nativeTargetPytestWitness splits one pytest node ID into its root-relative
// file part and its exact remaining node path. Pytest renders node IDs
// relative to its rootdir, which the producer pins to the manifest root, so
// a witness is the exact ID the runner invokes. The file part must name a
// pinned harness test file.
func nativeTargetPytestWitness(root, witness string) (string, string, error) {
	file, node, found := strings.Cut(witness, "::")
	if !found || node == "" || file == "" || strings.Contains(file, "::") {
		return "", "", nativeUnavailable("pytest witnesses must be <harness file>::<node id> identities under the manifest root")
	}
	if _, ok := nativeTargetRootedPath(root, file); !ok {
		return "", "", nativeUnavailable("pytest witnesses must be <harness file>::<node id> identities under the manifest root")
	}
	return file, node, nil
}

// nativeTargetCheckFor returns the one declared check for a selected
// witness. Duplicate or undeclared check witnesses refuse.
func nativeTargetCheckFor(m nativeTargetOracleManifest, witness string) (nativeTargetCheck, error) {
	found := -1
	for i, check := range m.Checks {
		if check.Witness == witness {
			if found >= 0 {
				return nativeTargetCheck{}, nativeUnavailable("check witness declared more than once: " + witness)
			}
			found = i
		}
	}
	if found < 0 {
		return nativeTargetCheck{}, nativeUnavailable("selected check witness has no declared check: " + witness)
	}
	return m.Checks[found], nil
}

// validateNativeTargetCheckArgv enforces one check's structural bounded
// argv: the closed interpreter slot, then the one pinned harness script the
// check invokes, then bounded literal data arguments. Producer scratch
// placeholders must never redirect the pinned script to candidate-owned
// bytes. Interpreter options and inline programs cannot occupy the script
// slot.
func validateNativeTargetCheckArgv(argv []string) error {
	if err := validateWorktreeVerifyCommand(argv); err != nil {
		return nativeUnavailable("check argv exceeds the bounded verify command shape")
	}
	switch argv[0] {
	case "bash", "python3", "node":
	default:
		return nativeUnavailable("check argv must start with the bash, python3 or node interpreter slot")
	}
	if len(argv) < 2 {
		return nativeUnavailable("check argv must name one pinned harness script behind the interpreter slot")
	}
	// The script slot is a literal file operand: a value starting with '-'
	// is the interpreter's own option (or stdin), never the pinned file.
	// HarnessFiles membership cannot decide this — an option-shaped name is
	// an ordinary relative file name — and an admitted option slot lets the
	// interpreter exit zero on its own flag without ever grading the pinned
	// bytes.
	if strings.HasPrefix(argv[1], "-") {
		return nativeUnavailable("check argv script slot must be a literal file operand, not an interpreter option")
	}
	for _, part := range argv[1:] {
		if part == "$scratch" || strings.Contains(part, "$scratch/") || strings.ContainsAny(part, "\x00") {
			return nativeUnavailable("check argv values must be bounded literal arguments")
		}
	}
	return nil
}

// The strict closed manifest decode refuses unknown fields and trailing
// input, so a target recipe can never smuggle an undocumented input surface
// — including the superseded fused driver field.
func decodeNativeTargetManifest(raw []byte) (nativeTargetOracleManifest, error) {
	var m nativeTargetOracleManifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, nativeUnavailable("recipe is not one closed target_runner_v1 manifest")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return m, nativeUnavailable("recipe contains trailing input")
	}
	return m, nil
}

// nativeTargetRecipePeek is the closed family envelope. Family selection
// reads only this field and tolerates the family's own fields; each family
// decoder then applies its own strict closed shape. A malformed manifest
// never falls back to the Go family, and a malformed Go manifest never
// becomes a target recipe.
type nativeTargetRecipePeek struct {
	Kind string `json:"kind"`
}

func nativeOracleRecipeKind(raw []byte) (string, error) {
	var peek nativeTargetRecipePeek
	if err := json.Unmarshal(raw, &peek); err != nil {
		return "", nativeUnavailable("recipe is not one closed native oracle manifest")
	}
	return peek.Kind, nil
}

// validateNativeTargetManifest enforces the closed manifest shape for one
// admitted pair: family, pair, contained root, bounded unique contained
// files, runner-correct witnesses, per-witness check argv for the check
// runner, and the exact pin fields the pair requires with no foreign pin
// fields.
func validateNativeTargetManifest(m nativeTargetOracleManifest, c OracleControl) error {
	if m.Kind != nativeTargetKind {
		return nativeUnavailable("unsupported recipe family")
	}
	pair, err := nativeTargetPairOf(m)
	if err != nil {
		return err
	}
	if m.Root != c.Cwd {
		return nativeUnavailable("manifest root must equal the control cwd")
	}
	if m.Root != "." {
		if err := validateWorkContextRepoPath(m.Root); err != nil {
			return err
		}
	}
	if len(m.HarnessFiles) < 1 || len(m.HarnessFiles) > 16 || len(m.SupportFiles) > 16 {
		return nativeUnavailable("target manifest file inventories are out of bounds")
	}
	if m.Runner != nativeTargetRunnerVitest && m.VitestConfig != "" {
		return nativeUnavailable("only vitest recipes may declare vitest_config")
	}
	if m.SvelteKit && (pair.Materializer != nativeTargetMaterializerBun || m.Pin.Node == "") {
		return nativeUnavailable("SvelteKit generated inputs require bun and pinned Node")
	}
	seen := map[string]bool{}
	for _, group := range [][]string{m.HarnessFiles, m.SupportFiles} {
		for _, name := range group {
			if seen[name] || !nativeTargetContainedName(m.Root, name) {
				return nativeUnavailable("manifest paths must be unique contained files under the root")
			}
			seen[name] = true
		}
	}
	if m.SvelteKit {
		for _, name := range []string{"svelte.config.js", "tsconfig.json"} {
			rooted, _ := nativeTargetRootedPath(m.Root, name)
			if !slices.Contains(m.HarnessFiles, rooted) {
				return nativeUnavailable("SvelteKit requires pinned svelte.config.js and tsconfig.json")
			}
		}
	}
	witnesses, err := nativeTargetCaseWitnesses(m, c)
	if err != nil {
		return err
	}
	switch pair.Runner {
	case nativeTargetRunnerPytest:
		if err := validateNativeTargetPytestManifest(m, c, witnesses); err != nil {
			return err
		}
	case nativeTargetRunnerCheck:
		if err := validateNativeTargetCheckManifest(m, pair, witnesses); err != nil {
			return err
		}
	case nativeTargetRunnerVitest:
		if err := validateNativeTargetVitestManifest(m, witnesses); err != nil {
			return err
		}
	}
	for _, pin := range []string{m.Pin.UV, m.Pin.Python, m.Pin.Pytest, m.Pin.Shell, m.Pin.Bun, m.Pin.Node, m.Pin.Vitest, m.Pin.Vite} {
		if pin != "" && !nativeTargetVersionPin.MatchString(pin) {
			return nativeUnavailable("runtime pins must be exact dotted numeric versions")
		}
	}
	return nil
}

func validateNativeTargetPytestManifest(m nativeTargetOracleManifest, c OracleControl, witnesses []string) error {
	if err := nativeTargetPinExact(m.Pin, "python uv pytest", "pyproject.toml", "uv.lock"); err != nil {
		return err
	}
	if len(m.Checks) != 0 {
		return nativeUnavailable("pytest recipes must not declare checks")
	}
	for _, witness := range witnesses {
		file, _, err := nativeTargetPytestWitness(m.Root, witness)
		if err != nil {
			return err
		}
		joined, ok := nativeTargetRootedPath(m.Root, file)
		if !ok || !slices.Contains(m.HarnessFiles, joined) || !strings.HasSuffix(file, ".py") {
			return nativeUnavailable("pytest witnesses must name pinned harness Python test files")
		}
	}
	argv, err := (&nativeTargetOraclePlan{Recipe: m}).prepareCommand(c)
	if err != nil {
		return err
	}
	if err := validateWorktreeVerifyCommand(argv); err != nil {
		return oracleFailure(KindLimitExceeded, "pytest recipe collection exceeds command bounds: "+err.Error(), "declare fewer or shorter Python harness file paths")
	}
	return nil
}

func validateNativeTargetCheckManifest(m nativeTargetOracleManifest, pair nativeTargetPair, witnesses []string) error {
	if len(m.Checks) < 1 || len(m.Checks) > 16 {
		return nativeUnavailable("check recipes must declare one to sixteen checks")
	}
	checkWitnesses := map[string]bool{}
	usesBash, usesPython, usesNode := false, false, false
	for _, check := range m.Checks {
		if len(check.Witness) < 1 || len(check.Witness) > nativeTargetWitnessMaxBytes || checkWitnesses[check.Witness] {
			return nativeUnavailable("check witnesses must be unique bounded strings")
		}
		checkWitnesses[check.Witness] = true
		if err := validateNativeTargetCheckArgv(check.Argv); err != nil {
			return err
		}
		switch check.Argv[0] {
		case "bash":
			usesBash = true
		case "python3":
			usesPython = true
		case "node":
			usesNode = true
		}
		script, ok := nativeTargetRootedPath(m.Root, check.Argv[1])
		if !ok || !slices.Contains(m.HarnessFiles, script) {
			return nativeUnavailable("check argv must invoke one declared pinned harness script: " + check.Argv[1])
		}
	}
	for _, witness := range witnesses {
		if _, err := nativeTargetCheckFor(m, witness); err != nil {
			return err
		}
	}
	for witness := range checkWitnesses {
		if !slices.Contains(witnesses, witness) {
			return nativeUnavailable("declared check witness is not selected by the control: " + witness)
		}
	}
	return validateNativeTargetCheckPins(m, pair, usesBash, usesPython, usesNode)
}

func validateNativeTargetCheckPins(m nativeTargetOracleManifest, pair nativeTargetPair, usesBash, usesPython, usesNode bool) error {
	required := ""
	if usesBash {
		required += " shell"
	}
	if usesPython {
		required += " python"
	}
	if usesNode {
		required += " node"
		if pair.Materializer != nativeTargetMaterializerBun || m.Pin.Node != nativeTargetNodeVersion {
			return nativeUnavailable("node checks require bun and Node " + nativeTargetNodeVersion)
		}
	}
	required = strings.TrimSpace(required)
	switch {
	case pair.Materializer == nativeTargetMaterializerUV:
		if err := nativeTargetPinExact(m.Pin, "python uv"+nativeTargetOptionalPin(usesBash, "shell"), "pyproject.toml", "uv.lock"); err != nil {
			return err
		}
	case pair.Materializer == nativeTargetMaterializerBun:
		if err := nativeTargetPinExact(m.Pin, strings.TrimSpace("bun "+required), "package.json", "bun.lock"); err != nil {
			return err
		}
		if !slices.Contains(nativeBunAdmittedVersions, m.Pin.Bun) {
			return nativeUnavailable("bun " + m.Pin.Bun + " is not a validated materializer runtime")
		}
	case pair.Materializer == nativeTargetMaterializerNone:
		if err := nativeTargetPinExact(m.Pin, required, "", ""); err != nil {
			return err
		}
	}
	if usesBash && m.Pin.Shell == "" {
		return nativeUnavailable("check recipes invoking bash must pin the exact bash version")
	}
	if usesPython && m.Pin.Python == "" {
		return nativeUnavailable("check recipes invoking python3 must pin the exact python version")
	}
	return nil
}

func nativeTargetOptionalPin(used bool, name string) string {
	if used {
		return " " + name
	}
	return ""
}

// nativeTargetPinExact enforces one pair's closed pin projection: the
// required fields are present, the dependency binding is exact, and every
// other field is empty.
func nativeTargetPinExact(pin nativeTargetPin, required, depManifest, depLock string) error {
	values := map[string]string{"uv": pin.UV, "python": pin.Python, "pytest": pin.Pytest, "shell": pin.Shell, "bun": pin.Bun, "node": pin.Node, "vitest": pin.Vitest, "vite": pin.Vite}
	for name, value := range values {
		present := value != ""
		switch {
		case slices.Contains(strings.Split(required, " "), name) && !present:
			return nativeUnavailable("target pin must name the exact " + name + " version")
		case !slices.Contains(strings.Split(required, " "), name) && present:
			return nativeUnavailable("target pin must not carry the foreign " + name + " field")
		}
	}
	if pin.DepManifest != depManifest || pin.DepLock != depLock {
		return nativeUnavailable("target pin must bind the exact materializer dependency manifest and lock")
	}
	return nil
}

// nativeTargetContainedName decides whether one declared manifest path
// stays inside the manifest root. A root of "." names the repository root
// itself and contains every normalized repository path; any other root
// prefixes its contained paths. Traversal, absolute, self, and tmp-scratch
// names refuse through the shared repository-path owner either way.
func nativeTargetContainedName(root, name string) bool {
	if root != "." && validateWorkContextRepoPath(root) != nil {
		return false
	}
	if validateWorkContextRepoPath(name) != nil {
		return false
	}
	return root == "." || strings.HasPrefix(name, root+"/")
}

// nativeTargetRootedPath joins one root-relative name onto the manifest
// root into one normalized repository path. A root of "." resolves to the
// name itself. The name must already be a normalized contained relative
// path, so the join can never introduce traversal, an absolute path, or a
// tmp-scratch source.
func nativeTargetRootedPath(root, name string) (string, bool) {
	if root != "." && validateWorkContextRepoPath(root) != nil {
		return "", false
	}
	if validateWorkContextRepoPath(name) != nil {
		return "", false
	}
	joined := path.Join(root, name)
	if validateWorkContextRepoPath(joined) != nil {
		return "", false
	}
	return joined, true
}

// The producer-derived execution view resolves every logical executable slot
// against an exact installed identity. Slots are stable role names; the plan
// carries the resolved absolute paths beside the digested identity.
type nativeTargetEnvironment struct {
	Materializer string             `json:"materializer"`
	Runner       string             `json:"runner"`
	Tools        []NativeOracleFile `json:"tools"`
	// RuntimeIdentity is the joined exact version string of every pinned
	// tool, for example "uv 0.11.26 python 3.13.7 pytest 9.1.1". It becomes
	// the preparation's ToolchainIdentity.
	RuntimeIdentity string `json:"runtime_identity"`
	// DependencyInputsDigest covers the byte-equal candidate and recipe
	// dependency manifest and lock bytes. The uv and bun materializers own
	// it; the none materializer pins its dependency-free declaration into
	// the same field.
	DependencyInputsDigest string `json:"dependency_inputs_digest"`
	// Digest binds the runtime identity, executable digests, dependency
	// inputs, and the closed pair policy string. Preparation and execution
	// join on it, so a dependency-input change re-pins the environment.
	Digest string `json:"digest"`
	// Resolved slots: absolute paths of the pinned executables and the
	// read-only host cache the uv or bun materializer materializes from.
	// Cache paths never enter the digest: they are input sources, not
	// identities.
	UV          string `json:"uv,omitempty"`
	Python      string `json:"python,omitempty"`
	Bash        string `json:"bash,omitempty"`
	Bun         string `json:"bun,omitempty"`
	Node        string `json:"node,omitempty"`
	UVCacheDir  string `json:"uv_cache_dir,omitempty"`
	NPMCacheDir string `json:"npm_cache_dir,omitempty"`
}

func nativeTargetUnavailableMaterializer(materializer, cause string) error {
	return nativeUnavailable("pinned " + materializer + " materialization is unavailable (cached-only, offline): " + cause)
}

// resolveNativeTargetRuntime resolves and version-probes the exact pinned
// executables on the host for one admitted pair. It never installs,
// downloads, or writes: a missing or mismatched tool is unavailable before
// any lease runs.
func resolveNativeTargetRuntime(ctx context.Context, m nativeTargetOracleManifest, pair nativeTargetPair) (nativeTargetEnvironment, error) {
	e := nativeTargetEnvironment{Materializer: pair.Materializer, Runner: pair.Runner}
	probe := nativeTargetRuntimeProbe{ctx: ctx, manifest: m, environment: &e}
	var err error
	switch pair.Materializer {
	case nativeTargetMaterializerUV:
		err = probe.resolveUV()
	case nativeTargetMaterializerBun:
		err = probe.resolveBun()
		if err == nil {
			err = probe.resolveCheckInterpreters()
		}
	case nativeTargetMaterializerNone:
		err = probe.resolveCheckInterpreters()
	default:
		err = nativeUnavailable("unsupported target materializer")
	}
	if err != nil {
		return e, err
	}
	e.RuntimeIdentity = strings.TrimSpace(e.RuntimeIdentity)
	return e, nil
}

type nativeTargetRuntimeProbe struct {
	ctx         context.Context
	manifest    nativeTargetOracleManifest
	environment *nativeTargetEnvironment
}

func nativeTargetResolvedTool(name string) (string, error) {
	found, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(found)
}

func (p nativeTargetRuntimeProbe) versionOf(name string, argv ...string) (string, error) {
	path, err := nativeTargetResolvedTool(name)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(p.ctx, path, argv...).Output() //nolint:gosec // resolved installed executable, fixed argv.
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (p nativeTargetRuntimeProbe) pinned(tool, got, want string) error {
	if got != want {
		return nativeTargetUnavailableMaterializer(p.environment.Materializer, tool+" "+got+" does not match the exact pin "+want)
	}
	return nil
}

func (p nativeTargetRuntimeProbe) bashVersion() error {
	e, m := p.environment, p.manifest
	materializer := e.Materializer
	bash, err := nativeTargetResolvedTool("bash")
	if err != nil {
		return nativeTargetUnavailableMaterializer(materializer, err.Error())
	}
	e.Bash = bash
	out, err := p.versionOf("bash", "--version")
	if err != nil {
		return nativeTargetUnavailableMaterializer(materializer, err.Error())
	}
	got, err := nativeTargetLeadVersion(out, `version ([0-9][0-9.]*)`)
	if err != nil {
		return nativeTargetUnavailableMaterializer(materializer, err.Error())
	}
	return p.pinned("bash", got, m.Pin.Shell)
}

func (p nativeTargetRuntimeProbe) resolveUV() error {
	e, m := p.environment, p.manifest
	uv, err := nativeTargetResolvedTool("uv")
	if err != nil {
		return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
	}
	e.UV = uv
	out, err := p.versionOf("uv", "--version")
	if err != nil {
		return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
	}
	got, err := nativeTargetLeadVersion(out, `uv ([0-9][0-9.]*)`)
	if err != nil {
		return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
	}
	if err := p.pinned("uv", got, m.Pin.UV); err != nil {
		return err
	}
	python, err := nativeTargetResolvePython(p.ctx, uv, m.Pin.Python)
	if err != nil {
		return err
	}
	e.Python = python
	e.RuntimeIdentity = "uv " + m.Pin.UV + " python " + m.Pin.Python
	if e.Runner == nativeTargetRunnerPytest {
		e.RuntimeIdentity += " pytest " + m.Pin.Pytest
	}
	if m.Pin.Shell != "" {
		if err := p.bashVersion(); err != nil {
			return err
		}
		e.RuntimeIdentity += " bash " + m.Pin.Shell
	}
	return nil
}

func (p nativeTargetRuntimeProbe) resolveBun() error {
	e, m := p.environment, p.manifest
	bun, err := nativeTargetResolvedTool("bun")
	if err != nil {
		return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
	}
	e.Bun = bun
	out, err := p.versionOf("bun", "--version")
	if err != nil {
		return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
	}
	got, err := nativeTargetLeadVersion(out, `^([0-9][0-9.]*)$`)
	if err != nil {
		return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
	}
	if err := p.pinned("bun", got, m.Pin.Bun); err != nil {
		return err
	}
	e.RuntimeIdentity = "bun " + m.Pin.Bun
	return nil
}

func (p nativeTargetRuntimeProbe) resolveCheckInterpreters() error {
	e, m := p.environment, p.manifest
	if m.Pin.Node != "" {
		node, err := nativeTargetResolvedTool("node")
		if err != nil {
			return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
		}
		e.Node = node
		out, err := p.versionOf("node", "--version")
		if err != nil {
			return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
		}
		if err := p.pinned("node", strings.TrimPrefix(strings.TrimSpace(out), "v"), m.Pin.Node); err != nil {
			return err
		}
		e.RuntimeIdentity += " node " + m.Pin.Node
		if m.Runner == nativeTargetRunnerVitest {
			e.RuntimeIdentity += " vitest " + m.Pin.Vitest + " vite " + m.Pin.Vite
		}
	}
	if m.Pin.Shell != "" {
		if err := p.bashVersion(); err != nil {
			return err
		}
		e.RuntimeIdentity += " bash " + m.Pin.Shell
	}
	if m.Pin.Python != "" {
		python, err := nativeTargetResolvedTool("python3")
		if err != nil {
			return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
		}
		e.Python = python
		out, err := p.versionOf("python3", "--version")
		if err != nil {
			return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
		}
		got, err := nativeTargetLeadVersion(out, `Python ([0-9][0-9.]*)`)
		if err != nil {
			return nativeTargetUnavailableMaterializer(e.Materializer, err.Error())
		}
		if err := p.pinned("python3", got, m.Pin.Python); err != nil {
			return err
		}
		e.RuntimeIdentity += " python " + m.Pin.Python
	}
	return nil
}

// nativeTargetLeadVersion extracts the first capture group of the first line
// of one tool's version output.
func nativeTargetLeadVersion(out, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(out, "\n")
	match := re.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return "", nativeUnavailable("unparseable version output")
	}
	return match[1], nil
}

func nativeTargetResolvePython(ctx context.Context, uv, pin string) (string, error) {
	out, err := exec.CommandContext(ctx, uv, "python", "find", pin).Output() //nolint:gosec // resolved uv, fixed argv.
	if err != nil {
		return "", nativeTargetUnavailableMaterializer(nativeTargetMaterializerUV, "python "+pin+" not found: "+err.Error())
	}
	found := strings.TrimSpace(string(out))
	if found == "" {
		return "", nativeTargetUnavailableMaterializer(nativeTargetMaterializerUV, "python "+pin+" resolved to no interpreter")
	}
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil {
		return "", nativeTargetUnavailableMaterializer(nativeTargetMaterializerUV, err.Error())
	}
	return resolved, nil
}

// finalizeNativeTargetEnvironment digests the resolved runtime into the
// single identity preparation and execution join on. The caller assigns
// DependencyInputsDigest first: the digest binds it, so a dependency-input
// change re-pins the environment identity.
func finalizeNativeTargetEnvironment(e *nativeTargetEnvironment) {
	slices.SortFunc(e.Tools, func(a, b NativeOracleFile) int { return strings.Compare(a.Path, b.Path) })
	e.Digest = nativeDigest([]byte(strings.Join([]string{
		e.Materializer, e.Runner, e.RuntimeIdentity, e.DependencyInputsDigest,
		nativeDigest([]byte(workflowJSON(e.Tools))),
		"offline-locked-no-build-no-scripts-copy-private-scratch",
	}, "\x00")))
}
