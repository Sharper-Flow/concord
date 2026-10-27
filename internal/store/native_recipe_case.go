package store

import (
	"bytes"
	"encoding/json"
	"io"
	"path"
	"slices"
	"strings"
)

// NativeRecipeCase is the immutable recipe's executable binding for one case.
// It describes inputs and witnesses, never readiness or execution evidence.
type NativeRecipeCase struct {
	Kind         string
	Cwd          string
	Witnesses    []string
	HarnessFiles []string
	FixtureFiles []string
	// Only commands belonging to this selected case, derived by the closed
	// target decoder. Harness membership alone says nothing about selection.
	checks       []nativeTargetCheck
	materializer string
	runner       string
}

// ResolveNativeRecipeCase uses the same closed readers as native preparation.
func ResolveNativeRecipeCase(raw []byte, caseID string) (NativeRecipeCase, error) {
	var out NativeRecipeCase
	if len(raw) > 16*1024 {
		return out, nativeUnavailable("recipe manifest exceeds 16384 bytes")
	}
	kind, err := nativeOracleRecipeKind(raw)
	if err != nil {
		return out, err
	}
	switch kind {
	case "go_top_level_v1", nativeGoPackageKind:
		m, err := decodeNativeGoManifest(raw)
		if err != nil {
			return out, err
		}
		if err := validateNativeGoManifest(m); err != nil {
			return out, err
		}
		names, err := nativeOracleCaseTests(m, OracleControl{CaseIDs: []string{caseID}})
		if err != nil {
			return out, err
		}
		return NativeRecipeCase{Kind: kind, Cwd: m.PackageCwd, Witnesses: names, HarnessFiles: slices.Clone(m.TestFiles), FixtureFiles: slices.Clone(m.FixtureFiles)}, nil
	case nativeTargetKind:
		m, err := decodeNativeTargetManifest(raw)
		if err != nil {
			return out, err
		}
		// Validate the complete declaration and this case's bounded control.
		if err := validateNativeTargetManifest(m, OracleControl{Cwd: m.Root, CaseIDs: []string{caseID}}); err != nil {
			return out, err
		}
		names, err := nativeTargetCaseWitnesses(m, OracleControl{CaseIDs: []string{caseID}})
		if err != nil {
			return out, err
		}
		out = NativeRecipeCase{Kind: kind, Cwd: m.Root, Witnesses: names, HarnessFiles: slices.Clone(m.HarnessFiles), FixtureFiles: slices.Clone(m.SupportFiles), materializer: m.Materializer, runner: m.Runner}
		for _, check := range m.Checks {
			if slices.Contains(names, check.Witness) {
				out.checks = append(out.checks, nativeTargetCheck{Witness: check.Witness, Argv: slices.Clone(check.Argv)})
			}
		}
		return out, nil
	default:
		return out, nativeUnavailable("unsupported recipe family")
	}
}

const nativeGoPackageKind = "go_package_v1"
const nativeGoInputFileLimit = 4096
const nativeGoInputByteLimit = 32 * 1024 * 1024
const nativeGoSelectedWitnessLimit = 40

func decodeNativeGoManifest(raw []byte) (nativeGoOracleManifest, error) {
	var m nativeGoOracleManifest
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return m, nativeUnavailable("recipe contains duplicate JSON keys")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, nativeUnavailable("recipe is not one closed Go manifest")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return m, nativeUnavailable("recipe contains trailing input")
	}
	return m, nil
}

func validateNativeGoManifest(m nativeGoOracleManifest) error {
	if m.PackageCwd != "." {
		if err := validateWorkContextRepoPath(m.PackageCwd); err != nil {
			return err
		}
	}
	if len(m.Cases) < 1 || len(m.Cases) > OracleCasesMax {
		return nativeUnavailable("recipe cases are out of bounds")
	}
	switch m.Kind {
	case "go_top_level_v1":
		if len(m.TestFiles) < 1 || len(m.TestFiles)+len(m.FixtureFiles) > nativeGoInputFileLimit || len(m.ExpectedSkips) != 0 {
			return nativeUnavailable("selected Go file inventory or skip declaration is unsupported")
		}
	case nativeGoPackageKind:
		if len(m.TestFiles)+len(m.FixtureFiles) != 0 || len(m.ExpectedSkips) > 64 {
			return nativeUnavailable("package recipes cannot replace candidate tests or fixtures")
		}
		seen := map[string]bool{}
		for _, name := range m.ExpectedSkips {
			if !nativeOracleTestName.MatchString(name) || len(name) > 128 || seen[name] {
				return nativeUnavailable("expected skips must be unique bounded top-level Test names")
			}
			seen[name] = true
		}
	default:
		return nativeUnavailable("unsupported recipe family")
	}
	if err := validateNativeGoManifestFiles(m); err != nil {
		return err
	}
	for id := range m.Cases {
		if !oracleCaseIDPattern.MatchString(id) {
			return nativeUnavailable("invalid recipe case identity")
		}
		if _, err := nativeOracleCaseTests(m, OracleControl{CaseIDs: []string{id}}); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeGoManifestFiles(m nativeGoOracleManifest) error {
	seen := map[string]bool{}
	for i, files := range [][]string{m.TestFiles, m.FixtureFiles} {
		for _, name := range files {
			if seen[name] || validateWorkContextRepoPath(name) != nil || (i == 0 && (!nativeTargetContainedName(m.PackageCwd, name) || !strings.HasSuffix(name, "_test.go"))) || (i == 1 && !nativeFixtureDataPath(name)) {
				return nativeUnavailable("manifest paths must be unique contained test or data files, never production/module replacements")
			}
			seen[name] = true
		}
	}
	return nil
}

// Data outside cwd is admitted only as inert fixture data, never executable,
// module, dependency, or runner configuration replacement.
func nativeFixtureDataPath(name string) bool {
	if validateWorkContextRepoPath(name) != nil {
		return false
	}
	base := path.Base(name)
	if slices.Contains([]string{"go.mod", "go.sum", "go.work"}, base) {
		return false
	}
	testdata := slices.Contains(strings.Split(name, "/"), "testdata")
	if !testdata && slices.Contains([]string{"package.json", "bun.lock", "pyproject.toml", "uv.lock", "pytest.ini", "tox.ini", "setup.cfg"}, base) {
		return false
	}
	if testdata && slices.Contains([]string{".toml", ".lock", ".ini", ".cfg"}, strings.ToLower(path.Ext(name))) {
		return true
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".json", ".jsonl", ".txt", ".yaml", ".yml", ".csv", ".tsv", ".xml", ".dat", ".bin", ".md", ".digest":
		return true
	}
	return false
}

func nativeSelectedCaseMap(cases map[string][]string, ids []string) map[string][]string {
	out := make(map[string][]string, len(ids))
	for _, id := range ids {
		out[id] = slices.Clone(cases[id])
	}
	return out
}

func nativeGoWitnessValid(name string) bool {
	parts := strings.Split(name, "/")
	if len(name) > 256 || len(parts) > 8 || !nativeOracleTestName.MatchString(parts[0]) || len(parts[0]) > 128 {
		return false
	}
	for _, part := range parts[1:] {
		if part == "" || strings.ContainsFunc(part, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-", r))
		}) {
			return false
		}
	}
	return true
}

func nativeGoWitnessRoots(names []string) []string {
	var roots []string
	for _, name := range names {
		root, _, _ := strings.Cut(name, "/")
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	slices.Sort(roots)
	return roots
}
