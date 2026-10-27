package store

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NativeGoPackageInventory is a bounded proof of candidate package execution.
// Digests cover sorted file identities, runnable names, and terminal outcomes.
// Skips remain named and unproved; this never credits an independent witness.
type NativeGoPackageInventory struct {
	TestFileCount      int      `json:"test_file_count"`
	TestInputBytes     int      `json:"test_input_bytes"`
	TestFilesDigest    string   `json:"test_files_digest"`
	TestCount          int      `json:"test_count"`
	TestNamesDigest    string   `json:"test_names_digest"`
	PassedCount        int      `json:"passed_count"`
	SkippedTests       []string `json:"skipped_tests"`
	NestedSkippedTests []string `json:"nested_skipped_tests"`
	OutcomesDigest     string   `json:"outcomes_digest"`
	ConverterDigest    string   `json:"converter_digest"`
}

func nativeGoPinnedFiles(ctx context.Context, r GitRunner, repo string, p nativeOraclePlan) ([]NativeOracleFile, error) {
	if p.HarnessInputCount <= 32 {
		return p.Files, nil
	}
	var files []NativeOracleFile
	names := slices.Concat(p.Manifest.TestFiles, p.Manifest.FixtureFiles)
	if p.Target != nil {
		names = slices.Concat(p.Target.Recipe.HarnessFiles, p.Target.Recipe.SupportFiles)
	}
	for _, name := range names {
		f, _, err := nativeGitFile(ctx, r, repo, p.Bundle.Control.RecipeSource.CommitOID, name)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	slices.SortFunc(files, func(a, b NativeOracleFile) int { return strings.Compare(a.Path, b.Path) })
	if len(files) != p.HarnessInputCount || nativeDigest([]byte(workflowJSON(files))) != p.HarnessInputDigest {
		return nil, nativeUnavailable("pinned harness inventory differs from immutable plan")
	}
	return files, nil
}

func nativeGoPackageTestInventory(rooted *os.Root, cwd string, target nativeGoPackage) (NativeGoPackageInventory, []string, error) {
	var inventory NativeGoPackageInventory
	files := slices.Concat(target.TestGoFiles, target.XTestGoFiles)
	slices.Sort(files)
	if len(files) == 0 || len(files) > nativeGoInputFileLimit {
		return inventory, nil, nativeUnavailable("complete package test files exceed 4096 or are empty")
	}
	var identities []NativeOracleFile
	found := map[string]bool{}
	for _, name := range files {
		filePath := path.Join(cwd, name)
		if path.Base(name) != name || !strings.HasSuffix(name, "_test.go") {
			return inventory, nil, nativeUnavailable("build-selected test file is not contained")
		}
		data, err := rooted.ReadFile(filePath)
		if err != nil {
			return inventory, nil, err
		}
		inventory.TestInputBytes += len(data)
		if inventory.TestInputBytes > nativeGoInputByteLimit {
			return inventory, nil, nativeUnavailable("complete package test inputs exceed 32 MiB")
		}
		identities = append(identities, NativeOracleFile{Path: filePath, SHA256: nativeDigest(data)})
		if err := nativeGoPackageFileTests(filePath, data, found); err != nil {
			return inventory, nil, err
		}
	}
	delete(found, "TestMain")
	var names []string
	for name := range found {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 || len(names) > 65536 {
		return inventory, nil, nativeUnavailable("complete package runnable inventory is empty or exceeds bound")
	}
	inventory.TestFileCount = len(files)
	inventory.TestFilesDigest = nativeDigest([]byte(workflowJSON(identities)))
	inventory.TestCount = len(names)
	inventory.TestNamesDigest = nativeDigest([]byte(workflowJSON(names)))
	return inventory, names, nil
}

func nativeGoRunnableName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	tail := strings.TrimPrefix(name, prefix)
	if tail == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(tail)
	return !unicode.IsLower(r)
}

func nativeGoPackageFileTests(name string, data []byte, found map[string]bool) error {
	file, err := parser.ParseFile(token.NewFileSet(), name, data, 0)
	if err != nil {
		return err
	}
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		if imp.Path.Value == `"testing"` {
			alias := "testing"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			aliases[alias] = true
		}
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		n := fn.Name.Name
		if nativeGoRunnableName(n, "Fuzz") || nativeGoRunnableName(n, "Example") {
			return nativeUnavailable("complete package has unsupported Fuzz/Example runnable: " + n)
		}
		if !nativeGoRunnableName(n, "Test") {
			continue
		}
		if found[n] {
			return nativeUnavailable("duplicate package runnable: " + n)
		}
		if n != "TestMain" {
			if err := validateNativeGoTestSignature(fn, aliases); err != nil {
				return err
			}
		}
		found[n] = true
	}
	return nil
}

func nativeGoPackageGrade(inventory *NativeGoPackageInventory, g *nativeGoEventGrade, converter string) {
	type outcome struct {
		Name  string
		Phase int
	}
	var outcomes []outcome
	for _, name := range g.required {
		outcomes = append(outcomes, outcome{name, g.state[name]})
		if g.state[name] == 2 {
			inventory.PassedCount++
		}
	}
	raw, _ := json.Marshal(outcomes)
	inventory.OutcomesDigest = nativeDigest(raw)
	inventory.SkippedTests = slices.Clone(g.skipped)
	inventory.NestedSkippedTests = slices.Clone(g.nestedSkips)
	inventory.ConverterDigest = converter
}

func validateNativeGoPackageFrames(stdout []byte, names, skipped []string) error {
	runs, terminals := map[string]int{}, map[string]int{}
	for _, name := range names {
		runs[name], terminals[name] = 0, 0
	}
	trailers := 0
	for rest := stdout; ; {
		_, frame, ok := bytes.Cut(rest, []byte{0x16})
		if !ok {
			break
		}
		rest = frame
		lineBytes, _, _ := bytes.Cut(frame, []byte{'\n'})
		line := strings.TrimLeft(string(lineBytes), " \t")
		if line == "PASS" {
			trailers++
		}
		if err := consumeNativeGoPackageFrame(line, runs, terminals, skipped); err != nil {
			return err
		}
	}
	if trailers != 1 {
		return nativeUnavailable("raw test stdout lacks exactly one framed package pass")
	}
	for _, name := range names {
		if runs[name] != 1 || terminals[name] != 1 {
			return nativeUnavailable("raw package stdout lacks unique inventory events: " + name)
		}
	}
	return nil
}

func consumeNativeGoPackageFrame(line string, runs, terminals map[string]int, skipped []string) error {
	if name, ok := strings.CutPrefix(line, "=== RUN   "); ok {
		if _, known := runs[name]; known {
			runs[name]++
		}
		return nil
	}
	for _, prefix := range []string{"--- PASS: ", "--- SKIP: "} {
		suffix, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		name, _, found := strings.Cut(suffix, " (")
		if _, known := terminals[name]; !known || !found || !strings.HasSuffix(suffix, "s)") {
			continue
		}
		if (prefix == "--- SKIP: ") != slices.Contains(skipped, name) {
			return nativeUnavailable("framed package outcome differs from converter")
		}
		terminals[name]++
	}
	return nil
}

func nativeGoPackageSelectionMatches(p nativeOraclePlan, o *NativeOracleResult) bool {
	names, err := nativeOracleSelector(p.Manifest, p.Bundle.Control)
	return err == nil && o.RecipeKind == nativeGoPackageKind && o.TargetRunner == "" && o.TargetMaterializer == "" && len(o.SelectedTestNames)+len(o.CaseToTestMap) == 0 && slices.Equal(o.SelectedWitnessIDs, names) && o.SelectedDistinctCount == len(names) && oracleEntryIdentical(o.CaseToWitnessMap, p.Manifest.Cases)
}

func nativeGoHarnessReceiptMatches(p nativeOraclePlan, o *NativeOracleResult) bool {
	if !oracleEntryIdentical(p.Files, o.Files) || len(o.Files) > 32 || p.HarnessInputCount != o.HarnessInputCount || p.HarnessInputDigest != o.HarnessInputDigest {
		return false
	}
	if p.Target != nil {
		return len(o.Files) > 0 || p.HarnessInputCount > 32 && p.HarnessInputCount <= nativeGoInputFileLimit && workflowDigest(p.HarnessInputDigest)
	}
	if p.Manifest.Kind == nativeGoPackageKind {
		return len(o.Files) == 0 && p.HarnessInputCount == 0 && p.HarnessInputDigest == ""
	}
	return len(o.Files) > 0 || p.HarnessInputCount > 32 && p.HarnessInputCount <= nativeGoInputFileLimit && workflowDigest(p.HarnessInputDigest)
}

func nativeGoPackageProofComplete(p nativeOraclePlan, o *NativeOracleResult) bool {
	i := o.PackageInventory
	if !nativeGoPackageSelectionMatches(p, o) || !slices.Equal(o.SelectedWitnessIDs, o.ObservedWitnessIDs) || len(o.ObservedTestNames) != 0 || i == nil {
		return false
	}
	if i.TestFileCount < 1 || i.TestFileCount > nativeGoInputFileLimit || i.TestInputBytes < 1 || i.TestInputBytes > nativeGoInputByteLimit || i.TestCount < 1 || i.PassedCount < 0 || i.PassedCount+len(i.SkippedTests) != i.TestCount || !workflowDigest(i.TestFilesDigest) || !workflowDigest(i.TestNamesDigest) || !workflowDigest(i.OutcomesDigest) || !workflowDigest(i.ConverterDigest) {
		return false
	}
	seen := map[string]bool{}
	for _, name := range i.SkippedTests {
		if seen[name] || !slices.Contains(p.Manifest.ExpectedSkips, name) {
			return false
		}
		seen[name] = true
	}
	return len(i.NestedSkippedTests) <= 64
}
