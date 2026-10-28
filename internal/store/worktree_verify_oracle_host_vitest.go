package store

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	nativeTargetNodeVersion       = "24.15.0"
	nativeTargetVitestVersion     = "4.1.11"
	nativeTargetViteVersion       = "8.2.2"
	nativeTargetVitestReportLimit = 256 * 1024
)

//go:embed worktree_verify_oracle_host_vitest.mjs
var nativeTargetVitestShim []byte

// A witness has a structural ancestry. Display-name delimiters never join it.
func nativeTargetVitestIdentity(project, file string, ancestors []string, title string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if ancestors == nil {
		ancestors = []string{}
	}
	_ = e.Encode([]any{project, file, ancestors, title})
	return strings.TrimSuffix(b.String(), "\n")
}

func nativeTargetVitestWitness(root, witness string) (project, file string, err error) {
	var parts []json.RawMessage
	var ancestors []string
	var title string
	if json.Unmarshal([]byte(witness), &parts) != nil || len(parts) != 4 ||
		json.Unmarshal(parts[0], &project) != nil || json.Unmarshal(parts[1], &file) != nil ||
		json.Unmarshal(parts[2], &ancestors) != nil || string(parts[2]) == "null" || json.Unmarshal(parts[3], &title) != nil ||
		project == "" || title == "" || len(ancestors) > 16 || witness != nativeTargetVitestIdentity(project, file, ancestors, title) {
		return "", "", nativeUnavailable("vitest witnesses must be canonical JSON [namedProject,rootRelativePinnedFile,ancestorTitles[],title]")
	}
	if _, ok := nativeTargetRootedPath(root, file); !ok {
		return "", "", nativeUnavailable("vitest witness file is not root-relative")
	}
	return project, file, nil
}

func validateNativeTargetVitestManifest(m nativeTargetOracleManifest, witnesses []string) error {
	if err := nativeTargetPinExact(m.Pin, "bun node vitest vite", "package.json", "bun.lock"); err != nil {
		return err
	}
	if m.Pin.Bun != "1.4.0" || m.Pin.Node != nativeTargetNodeVersion || m.Pin.Vitest != nativeTargetVitestVersion || m.Pin.Vite != nativeTargetViteVersion {
		return nativeUnavailable("vitest requires Bun 1.4.0, Node 24.15.0, Vitest 4.1.11 and Vite 8.2.2")
	}
	config, ok := nativeTargetRootedPath(m.Root, m.VitestConfig)
	if !ok || !slices.Contains(m.HarnessFiles, config) || len(m.Checks) != 0 {
		return nativeUnavailable("vitest requires a pinned vitest_config and refuses checks")
	}
	for _, witness := range witnesses {
		_, file, err := nativeTargetVitestWitness(m.Root, witness)
		if err != nil {
			return err
		}
		rooted, _ := nativeTargetRootedPath(m.Root, file)
		if !slices.Contains(m.HarnessFiles, rooted) {
			return nativeUnavailable("vitest witness must name a pinned harness file")
		}
	}
	return nil
}

func nativeTargetVitestStages(phase string, witnesses []string) ([]NativeOracleStage, error) {
	if len(witnesses) > nativeTargetStageBudget {
		return nil, nativeUnavailable("vitest witness population exceeds the stage bound")
	}
	argv := []string{"node", "$scratch/vitest-runner.mjs", "collect", "$scratch/vitest-plan.json", "$scratch/vitest-collect.json"}
	if phase == "prepare" {
		return []NativeOracleStage{{Name: "collect", Argv: argv}}, nil
	}
	var stages []NativeOracleStage
	for i, witness := range witnesses {
		a := slices.Clone(argv)
		a[2], a[4] = "run", "$scratch/vitest-"+strconv.Itoa(i)+".json"
		a = append(a, witness)
		stages = append(stages, NativeOracleStage{Name: "test", Argv: a})
	}
	return stages, nil
}

func nativeTargetNodeBinDir(root string) string { return filepath.Join(root, "node-bin") }

func nativeTargetInstallRunner(t *nativeTargetOraclePlan, root string, control OracleControl) error {
	if t.Environment.Node != "" {
		if err := os.Mkdir(nativeTargetNodeBinDir(root), 0700); err != nil {
			return err
		}
		if err := os.Symlink(t.Environment.Node, filepath.Join(nativeTargetNodeBinDir(root), "node")); err != nil {
			return err
		}
	}
	if t.Recipe.Runner != nativeTargetRunnerVitest {
		return nil
	}
	witnesses, err := nativeTargetCaseWitnesses(t.Recipe, control)
	if err != nil {
		return err
	}
	pinned := []string{}
	for _, name := range append(slices.Clone(t.Recipe.HarnessFiles), t.Recipe.SupportFiles...) {
		if t.Recipe.Root != "." {
			name = strings.TrimPrefix(name, t.Recipe.Root+"/")
		}
		pinned = append(pinned, name)
	}
	plan, err := json.Marshal(struct {
		Config    string   `json:"config"`
		Witnesses []string `json:"witnesses"`
		Pinned    []string `json:"pinned"`
		SvelteKit bool     `json:"sveltekit"`
	}{t.Recipe.VitestConfig, witnesses, pinned, t.Recipe.SvelteKit})
	if err != nil {
		return err
	}
	private, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = private.Close() }()
	if err := private.WriteFile("vitest-plan.json", plan, 0600); err != nil {
		return err
	}
	return private.WriteFile("vitest-runner.mjs", nativeTargetVitestShim, 0600)
}

func nativeTargetNodeRunnerParts(t *nativeTargetOraclePlan, root string) ([][]byte, error) {
	if t.Environment.Node == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(nativeTargetNodeBinDir(root))
	link := filepath.Join(nativeTargetNodeBinDir(root), "node")
	target, linkErr := os.Readlink(link)
	if err != nil || len(entries) != 1 || entries[0].Name() != "node" || linkErr != nil || target != t.Environment.Node {
		return nil, nativeUnavailable("per-run node link no longer names the pinned Node")
	}
	data, err := os.ReadFile(target) //nolint:gosec // target equals the producer-resolved, version-pinned Node executable checked above.
	if err != nil {
		return nil, err
	}
	parts := [][]byte{[]byte("node\x00" + target), data}
	if t.Recipe.Runner == nativeTargetRunnerVitest {
		private, err := os.OpenRoot(root)
		if err != nil {
			return nil, err
		}
		defer func() { _ = private.Close() }()
		shim, err := private.ReadFile("vitest-runner.mjs")
		if err != nil || !bytes.Equal(shim, nativeTargetVitestShim) {
			return nil, nativeUnavailable("producer Vitest shim changed")
		}
		plan, err := private.ReadFile("vitest-plan.json")
		if err != nil {
			return nil, err
		}
		parts = append(parts, shim, plan)
	}
	return parts, nil
}

type nativeTargetVitestRecord struct {
	ID      string `json:"id"`
	Witness string `json:"witness"`
	State   string `json:"state"`
}

type nativeTargetVitestReport struct {
	Version    int                        `json:"version"`
	Phase      string                     `json:"phase"`
	Collection []nativeTargetVitestRecord `json:"collection"`
	Callbacks  []nativeTargetVitestRecord `json:"callbacks"`
	Results    []nativeTargetVitestRecord `json:"results"`
	End        int                        `json:"end"`
	Complete   bool                       `json:"complete"`
}

func nativeTargetReadVitestReport(root, name string) (nativeTargetVitestReport, error) {
	var report nativeTargetVitestReport
	if filepath.Base(name) != name {
		return report, nativeUnavailable("Vitest result channel is not producer-owned")
	}
	private, err := os.OpenRoot(root)
	if err != nil {
		return report, nativeUnavailable("Vitest result channel is missing")
	}
	defer func() { _ = private.Close() }()
	info, err := private.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return report, nativeUnavailable("Vitest result channel is missing or nonregular")
	}
	f, err := private.Open(name)
	if err != nil {
		return report, nativeUnavailable("Vitest result channel is missing")
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > nativeTargetVitestReportLimit {
		return report, nativeUnavailable("Vitest result channel exceeds bounds or is nonregular")
	}
	d := json.NewDecoder(io.LimitReader(f, nativeTargetVitestReportLimit+1))
	d.DisallowUnknownFields()
	if d.Decode(&report) != nil {
		return report, nativeUnavailable("Vitest result channel is malformed")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return report, nativeUnavailable("Vitest result channel has trailing input")
	}
	return report, nil
}

func nativeTargetVerifyVitestReport(report nativeTargetVitestReport, selected []string, witness string) error {
	if report.Version != 1 || !report.Complete || len(report.Collection) == 0 || len(report.Collection) > 40 || len(report.Callbacks) > 40 || len(report.Results) > 40 {
		return nativeUnavailable("Vitest report is incomplete or unbounded")
	}
	ids, err := nativeTargetVitestCollection(report.Collection, selected)
	if err != nil {
		return err
	}
	if witness == "" {
		if report.Phase != "collect" || len(report.Results) != 0 || len(report.Callbacks) != 0 || report.End != 0 {
			return nativeUnavailable("Vitest preparation reported acceptance execution")
		}
		return nil
	}
	if report.Phase != "run" || report.End != 1 || len(report.Callbacks) == 0 || len(report.Results) != len(report.Callbacks) {
		return nativeUnavailable("Vitest execution lacks complete callback witnesses")
	}
	project, file, _ := nativeTargetVitestWitness(".", witness)
	expected := map[string]string{}
	for id, identity := range ids {
		p, f, _ := nativeTargetVitestWitness(".", identity)
		if p == project && f == file {
			expected[id] = identity
		}
	}
	if err := nativeTargetVitestOutcomes(report.Callbacks, expected, witness, "run", "skip"); err != nil {
		return err
	}
	return nativeTargetVitestOutcomes(report.Results, expected, witness, "passed", "skipped")
}

func nativeTargetVitestCollection(collection []nativeTargetVitestRecord, selected []string) (map[string]string, error) {
	ids, population := map[string]string{}, []string{}
	seen := map[string]bool{}
	for _, record := range collection {
		if record.ID == "" || len(record.ID) > 256 || ids[record.ID] != "" || seen[record.Witness] || record.State != "run" {
			return nil, nativeUnavailable("Vitest collection contains skip/todo, duplicate identity or API-ID collision")
		}
		if _, _, err := nativeTargetVitestWitness(".", record.Witness); err != nil {
			return nil, err
		}
		ids[record.ID], seen[record.Witness] = record.Witness, true
		population = append(population, record.Witness)
	}
	slices.Sort(population)
	if !slices.Equal(population, selected) {
		return nil, nativeUnavailable("Vitest collection differs from the exact selected population")
	}
	return ids, nil
}

func nativeTargetVitestOutcomes(records []nativeTargetVitestRecord, expected map[string]string, witness, selectedState, otherState string) error {
	seenIDs := map[string]bool{}
	for _, record := range records {
		want := otherState
		if record.Witness == witness {
			want = selectedState
		}
		identity, exists := expected[record.ID]
		if !exists || identity != record.Witness || seenIDs[record.ID] || record.State != want {
			return nativeUnavailable("Vitest callbacks contain missing, extra, skipped or non-passing selected outcomes")
		}
		seenIDs[record.ID] = true
	}
	if len(seenIDs) != len(expected) {
		return nativeUnavailable("Vitest callback population is incomplete")
	}
	return nil
}

// The source slot is a producer-created link, never candidate write permission.
// Its target is emptied after the previous stage has completely terminated.
func (n *nativeTargetOracleRun) freshGeneratedInputs() error {
	if !n.recipe.SvelteKit {
		return nil
	}
	link := filepath.Join(n.cwd, ".svelte-kit")
	dir := filepath.Join(n.root, "generated")
	if target, err := os.Readlink(link); err == nil {
		if target != dir {
			return nativeUnavailable("SvelteKit generated input slot changed")
		}
	} else if !os.IsNotExist(err) {
		return nativeUnavailable("SvelteKit generated input slot is not producer-owned")
	} else if n.testOrdinal > 0 {
		return nativeUnavailable("SvelteKit generated input slot disappeared")
	} else if err := os.Symlink(dir, link); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Mkdir(dir, 0700)
}
