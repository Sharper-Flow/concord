package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorktreeGoSuiteObservesPackageAndNamedPasses(t *testing.T) {
	raw := worktreeGoSuiteJSON(t,
		[3]string{"start", "example.test/p", ""},
		[3]string{"start", "example.test/empty", ""},
		[3]string{"run", "example.test/p", "TestBody"},
		[3]string{"run", "example.test/p", "TestBody/Sub"},
		[3]string{"pause", "example.test/p", "TestBody/Sub"},
		[3]string{"cont", "example.test/p", "TestBody/Sub"},
		[3]string{"skip", "example.test/empty", ""},
		[3]string{"pass", "example.test/p", "TestBody/Sub"},
		[3]string{"pass", "example.test/p", "TestBody"},
		[3]string{"run", "example.test/p", "TestSkipped"},
		[3]string{"skip", "example.test/p", "TestSkipped"},
		[3]string{"pass", "example.test/p", ""})
	result, err := observeWorktreeGoSuite(strings.NewReader(raw), map[string]bool{"TestBody": true, "TestSkipped": true})
	if err != nil || result == nil || !result.Complete || result.StreamDigest != nativeDigest([]byte(raw)) {
		t.Fatalf("observation=%+v error=%v", result, err)
	}
	if len(result.Packages) != 2 || result.Packages[0].Package != "example.test/empty" || result.Packages[0].Passed {
		t.Fatalf("empty package gained a passing suite: %+v", result.Packages)
	}
	pkg := result.Packages[1]
	if !pkg.Passed || pkg.PassedCount != 1 || pkg.SkippedCount != 1 || !slices.Equal(pkg.ObservedTestNames, []string{"TestBody"}) {
		t.Fatalf("wrong body observations: %+v", pkg)
	}
}

func TestWorktreeGoSuiteRefusesConflictingOrIncompleteJSON(t *testing.T) {
	start := [3]string{"start", "example.test/p", ""}
	run := [3]string{"run", "example.test/p", "TestBody"}
	pass := [3]string{"pass", "example.test/p", "TestBody"}
	end := [3]string{"pass", "example.test/p", ""}
	for name, events := range map[string][][3]string{
		"empty": {}, "unfinished package": {start}, "unfinished test": {start, run, end},
		"test before start": {run, pass, end}, "pass without run": {start, pass, end},
		"duplicate start":        {start, start, run, pass, end},
		"duplicate run":          {start, run, run, pass, end},
		"duplicate pass":         {start, run, pass, pass, end},
		"post-terminal test":     {start, run, pass, end, run},
		"test fail":              {start, run, {"fail", "example.test/p", "TestBody"}, end},
		"package fail":           {start, run, pass, {"fail", "example.test/p", ""}},
		"build fail":             {{"build-fail", "", ""}},
		"unknown action":         {start, {"invented", "example.test/p", ""}, end},
		"pause before run":       {start, {"pause", "example.test/p", "TestBody"}, run, pass, end},
		"continue without pause": {start, run, {"cont", "example.test/p", "TestBody"}, pass, end},
		"duplicate pause":        {start, run, {"pause", "example.test/p", "TestBody"}, {"pause", "example.test/p", "TestBody"}, {"cont", "example.test/p", "TestBody"}, pass, end},
		"duplicate continue":     {start, run, {"pause", "example.test/p", "TestBody"}, {"cont", "example.test/p", "TestBody"}, {"cont", "example.test/p", "TestBody"}, pass, end},
		"pass while paused":      {start, run, {"pause", "example.test/p", "TestBody"}, pass, end},
		"skip after pass":        {start, run, pass, {"skip", "example.test/p", "TestBody"}, end},
		"unclosed other package": {start, {"start", "example.test/q", ""}, run, pass, end},
	} {
		t.Run(name, func(t *testing.T) {
			if result, err := observeWorktreeGoSuite(strings.NewReader(worktreeGoSuiteJSON(t, events...)), nil); err == nil {
				t.Fatalf("invalid stream qualified: %+v", result)
			}
		})
	}
	for _, raw := range []string{"{", "unstructured output\n", worktreeGoSuiteJSON(t, start, run, pass, end) + "{"} {
		if result, err := observeWorktreeGoSuite(strings.NewReader(raw), nil); err == nil {
			t.Fatalf("malformed stream qualified: %+v", result)
		}
	}
}

func TestWorktreeGoSuiteSkipOnlyAndUnrequestedNamesEarnNoNamedCredit(t *testing.T) {
	for _, action := range []string{"skip", "pass"} {
		raw := worktreeGoSuiteJSON(t, [3]string{"start", "example.test/p", ""},
			[3]string{"run", "example.test/p", "TestOther"}, [3]string{action, "example.test/p", "TestOther"},
			[3]string{"pass", "example.test/p", ""})
		result, err := observeWorktreeGoSuite(strings.NewReader(raw), map[string]bool{"TestRequired": true})
		if err != nil || result == nil || len(result.Packages) != 1 || len(result.Packages[0].ObservedTestNames) != 0 {
			t.Fatalf("unexecuted required test earned credit: %+v, %v", result, err)
		}
		if action == "skip" && result.Packages[0].PassedCount != 0 {
			t.Fatalf("skip-only package gained a test pass: %+v", result.Packages[0])
		}
	}
}

func worktreeGoSuiteJSON(t *testing.T, events ...[3]string) string {
	t.Helper()
	var result strings.Builder
	encoder := json.NewEncoder(&result)
	for _, event := range events {
		if err := encoder.Encode(struct{ Action, Package, Test string }{event[0], event[1], event[2]}); err != nil {
			t.Fatal(err)
		}
	}
	return result.String()
}

func TestWorktreeGoSuiteReceiptComesFromItsNativeProducer(t *testing.T) {
	ctx := context.Background()
	const workID = "go-suite-receipt"
	s, git, _, _ := verifyEvidenceFixture(t, workID)
	defer s.Close()
	recordWorktreeGoSuiteFootprint(t, s, workID, []FootprintCheck{
		{Kind: "go_test", Value: "internal/store.TestBody"},
		{Kind: "go_test", Value: "internal/store.TestUnobserved"},
	})
	dir := worktreeGoSuiteEnvironmentFixture(t)
	raw := worktreeGoSuiteJSON(t, [3]string{"start", "example.test/p", ""},
		[3]string{"run", "example.test/p", "TestBody"}, [3]string{"pass", "example.test/p", "TestBody"},
		[3]string{"pass", "example.test/p", ""})
	program := "#!/bin/sh\nprintf '%s' '" + raw + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	req := verifyEvidenceRun(git, workID, verifyLeaseID(workID), 0)
	req.Command, req.RunCommand = worktreeGoSuiteCommand(), nil
	result, err := s.VerifyWorktree(ctx, req)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("native verify=%+v error=%v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	var observation WorktreeGoSuiteResult
	if err := json.Unmarshal(envelope["go_suite"], &observation); err != nil || !observation.Complete || observation.StreamDigest != nativeDigest([]byte(raw)) {
		t.Fatalf("producer observation absent or unbound: %s, %v", encoded, err)
	}
	if len(observation.Packages) != 1 || !slices.Equal(observation.Packages[0].ObservedTestNames, []string{"TestBody"}) {
		t.Fatalf("native producer did not retain stored-footprint passes: %+v", observation.Packages)
	}
	receipt, err := readOracleVerifySubjectReceipt(ctx, s.DatabaseForTesting(), workID, result.OperationRef)
	if err != nil || receipt == nil || receipt.SubjectRef != result.SubjectRef {
		t.Fatalf("native receipt did not join: %+v, %v", receipt, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	retained, err := s.VerifyWorktree(ctx, req)
	retainedJSON, marshalErr := json.Marshal(retained)
	if err != nil || marshalErr != nil || string(retainedJSON) != string(encoded) {
		t.Fatalf("same lease reran or changed its native record: %+v, %v, %v", retained, err, marshalErr)
	}
}

func TestWorktreeGoSuiteConsumesBeyondRetainedOutput(t *testing.T) {
	dir := worktreeGoSuiteEnvironmentFixture(t)
	start := worktreeGoSuiteJSON(t, [3]string{"start", "example.test/p", ""})
	output := `{"Action":"output","Package":"example.test/p","Output":"` + strings.Repeat("x", 1024) + "\"}\n"
	raw := start + strings.Repeat(output, nativeOracleStreamLimit/len(output)+32) + worktreeGoSuiteJSON(t,
		[3]string{"run", "example.test/p", "TestBody"}, [3]string{"pass", "example.test/p", "TestBody"},
		[3]string{"pass", "example.test/p", ""})
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\nprintf '%s' '"+raw+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := runWorktreeGoSuite(t.Context(), dir, map[string]bool{"TestBody": true}, 64)
	if err != nil || result.ExitCode != 0 || !result.OutputTruncated || len(result.Output) > 64 {
		t.Fatalf("preview bounds: %+v, %v", result, err)
	}
	if result.GoSuite == nil || !result.GoSuite.Complete || result.GoSuite.StreamDigest != nativeDigest([]byte(raw)) ||
		len(result.GoSuite.Packages) != 1 || !slices.Equal(result.GoSuite.Packages[0].ObservedTestNames, []string{"TestBody"}) {
		t.Fatalf("full observation lost after preview/raw-retention limits: %+v", result.GoSuite)
	}
}

func TestWorktreeGoSuiteOversizedMetadataCannotLeavePartialCredit(t *testing.T) {
	result := WorktreeVerifyResult{GoSuite: &WorktreeGoSuiteResult{Complete: true, Protocol: "native_go_suite_v1"}}
	for range 1024 {
		result.GoSuite.Packages = append(result.GoSuite.Packages, WorktreeGoSuitePackage{
			Package: strings.Repeat("x", 512), Passed: true, PassedCount: 1,
		})
	}
	raw, err := marshalBoundWorktreeVerifyRecord(&result)
	if err != nil || len(raw) > 48*1024 || result.GoSuite.Complete || len(result.GoSuite.Packages) != 0 || result.GoSuite.Detail == "" {
		t.Fatalf("oversized observation retained proof: %+v, size=%d, %v", result.GoSuite, len(raw), err)
	}
}

func TestWorktreeGoSuiteRunsRealBodiesWithoutCacheOrInheritedSelectors(t *testing.T) {
	dir := t.TempDir()
	module := "module example.test/observed\n\ngo 1.26.0\n"
	source := `package observed
import ("os"; "runtime"; "testing")
func TestBody(t *testing.T) {
 if os.Getenv("GOFLAGS") != "-mod=readonly" || runtime.GOMAXPROCS(0) != 4 || testing.Short() { t.Fatal("inherited selection") }
 f, err := os.OpenFile(os.Getenv("GO_SUITE_MARKER"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
 if err != nil { t.Fatal(err) }; if _, err := f.WriteString("ran\n"); err != nil { t.Fatal(err) }; if err := f.Close(); err != nil { t.Fatal(err) }
 if os.Getenv("GO_SUITE_MUTATE_SOURCE") == "1" {
  f, err := os.OpenFile("body_test.go", os.O_WRONLY|os.O_APPEND, 0600); if err != nil { t.Fatal(err) }
  if _, err := f.WriteString("\n// changed selected input\n"); err != nil { t.Fatal(err) }; if err := f.Close(); err != nil { t.Fatal(err) }
 }
  t.Run("Nested/Path", func(t *testing.T) { t.Parallel(); t.Log("executed") })
}
func TestSkipped(t *testing.T) { t.Skip("synthetic skip") }
`
	for name, content := range map[string]string{"go.mod": module, "body_test.go": source} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(t.TempDir(), "executions")
	commitWorktreeGoSourceFixture(t, dir)
	t.Setenv("GO_SUITE_MARKER", marker)
	t.Setenv("GOFLAGS", "-run=NeverMatches -skip=TestBody -short -tags=absent")
	t.Setenv("GOMAXPROCS", "99")
	t.Setenv("CGO_ENABLED", "0")
	t.Setenv("GOWORK", filepath.Join(dir, "absent.work"))
	t.Setenv("GOENV", filepath.Join(dir, "absent.env"))
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	for range 2 {
		started := time.Now()
		result, err := runWorktreeGoSuite(ctx, dir, map[string]bool{"TestBody": true, "TestSkipped": true}, 4096)
		t.Logf("real uncached fixture duration=%s", time.Since(started))
		if err != nil || result.ExitCode != 0 || result.GoSuite == nil || !result.GoSuite.Complete || len(result.GoSuite.Packages) != 1 {
			t.Fatalf("real execution=%+v observation=%+v error=%v", result, result.GoSuite, err)
		}
		pkg := result.GoSuite.Packages[0]
		if pkg.Package != "example.test/observed" || !pkg.Passed || pkg.PassedCount != 1 || pkg.SkippedCount != 1 || !slices.Equal(pkg.ObservedTestNames, []string{"TestBody"}) {
			t.Fatalf("wrong real body observation: %+v", pkg)
		}
		closure := result.GoSuite.Closure
		if closure == nil || !closure.Complete || closure.SelectedCount != 2 || closure.Digest == "" || closure.SubjectRef != "commit:"+worktreeGoSourceFixtureHead(t, dir) {
			t.Fatalf("real execution has no exact source closure: %+v", closure)
		}
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "ran\nran\n" {
		t.Fatalf("bodies did not execute twice: %q, %v", raw, err)
	}
	t.Setenv("GO_SUITE_MUTATE_SOURCE", "1")
	mutated, err := runWorktreeGoSuite(ctx, dir, map[string]bool{"TestBody": true}, 4096)
	if err != nil || mutated.ExitCode != 0 || mutated.GoSuite == nil || !mutated.GoSuite.Complete || mutated.GoSuite.Closure == nil || mutated.GoSuite.Closure.Complete {
		t.Fatalf("post-execution source mutation retained closure: %+v, %v", mutated.GoSuite, err)
	}
}

func TestWorktreeGoSuiteProducerFixesSelectionEnvironment(t *testing.T) {
	dir := worktreeGoSuiteEnvironmentFixture(t)
	result, err := runWorktreeGoSuite(context.Background(), dir, nil, 4096)
	if err != nil || result.ExitCode != 0 || result.OutputTruncated {
		t.Fatalf("run=%+v error=%v", result, err)
	}
	var observed string
	decoder := json.NewDecoder(strings.NewReader(result.Output))
	for decoder.More() {
		var event struct{ Output string }
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode native fixture output: %v: %s", err, result.Output)
		}
		observed += event.Output
	}
	for _, want := range []string{
		"GOFLAGS=-mod=readonly|", "GOENV=off|", "GOWORK=off|", "GOTOOLCHAIN=local|",
		"GOMAXPROCS=4|", "CGO_ENABLED=1|", "GOOS=" + runtime.GOOS + "|",
		"GOARCH=" + runtime.GOARCH + "|", "ARGV=test -race -count=1 -json -p=4 -timeout=20m ./...|",
	} {
		if !strings.Contains(observed, want) {
			t.Errorf("producer environment lacks %q: %s", want, observed)
		}
	}
}

func TestWorktreeLegacyCommandsKeepTheirEnvironment(t *testing.T) {
	dir := worktreeGoSuiteEnvironmentFixture(t)
	for _, command := range [][]string{
		{"go", "test", "-race", "-timeout=20m", "./..."},
		{"/bin/sh", "-c", `printf '%s' "$GOFLAGS"`},
	} {
		code, output, truncated, err := RunWorktreeVerifyCommand(context.Background(), dir, command, 4096)
		if err != nil || code != 0 || truncated || !strings.Contains(string(output), "-run=NeverMatches") {
			t.Fatalf("legacy argv %q changed environment: code=%d truncated=%v err=%v output=%s", command, code, truncated, err, output)
		}
	}
}

func worktreeGoSuiteEnvironmentFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	program := `#!/bin/sh
printf '{"Action":"start","Package":"example.test/proof"}\n'
printf '{"Action":"output","Package":"example.test/proof","Output":"GOFLAGS=%s|GOENV=%s|GOWORK=%s|GOTOOLCHAIN=%s|GOMAXPROCS=%s|CGO_ENABLED=%s|GOOS=%s|GOARCH=%s|ARGV=%s|"}\n' "$GOFLAGS" "$GOENV" "$GOWORK" "$GOTOOLCHAIN" "$GOMAXPROCS" "$CGO_ENABLED" "$GOOS" "$GOARCH" "$*"
printf '{"Action":"skip","Package":"example.test/proof"}\n'
`
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for key, value := range map[string]string{
		"GOFLAGS": "-run=NeverMatches", "GOENV": filepath.Join(dir, "host-goenv"),
		"GOWORK": filepath.Join(dir, "host-go.work"), "GOTOOLCHAIN": "auto",
		"GOMAXPROCS": "99", "CGO_ENABLED": "0", "GOOS": "fixture-os", "GOARCH": "fixture-arch",
	} {
		t.Setenv(key, value)
	}
	return dir
}

func TestWorktreeGoSuiteWantsStoredFootprintTestNames(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "go-suite-wanted")
	recordWorktreeGoSuiteFootprint(t, s, "go-suite-wanted", []FootprintCheck{
		{Kind: "go_test", Value: "internal/store.TestRequired"},
		{Kind: "go_test", Value: "internal/store.helper"},
		{Kind: "go_package", Value: "./internal/store"},
	})
	wanted, err := worktreeGoSuiteWanted(context.Background(), s.DatabaseForTesting(), "go-suite-wanted")
	if err != nil || len(wanted) != 1 || !wanted["TestRequired"] {
		t.Fatalf("stored footprint names=%v error=%v", wanted, err)
	}
}

func recordWorktreeGoSuiteFootprint(t *testing.T, s *Store, workID string, checks []FootprintCheck) {
	t.Helper()
	inputs := workSizeInputsFixture()
	size, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	footprint := workSizeFootprintFixture()
	footprint.ChecksComplete = true
	footprint.Laws = []FootprintLaw{{LawID: "CD-0001", Checks: checks}}
	if err := applyWorkEvent(t, s, workSizedEventWith(workID+"-size", workID, WorkSizeModelVersion, size, inputs, footprint), nil); err != nil {
		t.Fatal(err)
	}

}

func TestWorktreeGoSuiteCommandMatchesDeclaredTool(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".concord", "tooling.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Tools []struct{ ID, Invocation string }
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, tool := range manifest.Tools {
		if tool.ID == "go-test-proof" {
			if tool.Invocation != strings.Join(worktreeGoSuiteCommand(), " ") {
				t.Fatalf("declared Go proof command %q differs from producer argv %q", tool.Invocation, worktreeGoSuiteCommand())
			}
			return
		}
	}
	t.Fatal("Go proof tool declaration is absent")
}
