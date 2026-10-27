package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestNativeFootprintSelectedSubsetAndNestedWitness(t *testing.T) {
	m := nativeGoOracleManifest{Cases: map[string][]string{"case:chosen": {"TestCorpus/scenario-one"}, "case:unrelated": {"TestOther"}}}
	c := OracleControl{CaseIDs: []string{"case:chosen"}, Argv: []string{"go", "test", "-count=1", "-run", "^(TestCorpus)$", "."}}
	names, err := nativeOracleSelector(m, c)
	if err != nil || !slices.Equal(names, []string{"TestCorpus/scenario-one"}) {
		t.Fatalf("selected nested witness = %v, %v", names, err)
	}
}

func TestNativeFootprintSelectedWitnessCardinality(t *testing.T) {
	for _, count := range []int{40, 41} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := nativeGoOracleManifest{Kind: "go_top_level_v1", Cases: map[string][]string{}}
			c := OracleControl{Argv: []string{"go", "test", "-count=1", "-run", "^(TestCorpus)$", "."}}
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("case:group-%d", i/8)
				if _, ok := m.Cases[id]; !ok {
					c.CaseIDs = append(c.CaseIDs, id)
				}
				m.Cases[id] = append(m.Cases[id], fmt.Sprintf("TestCorpus/scenario-%d", i))
			}
			names, err := nativeOracleSelector(m, c)
			if count == 40 {
				if err != nil || len(names) != count {
					t.Fatalf("bounded literal witnesses = %d, %v", len(names), err)
				}
			} else {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Kind != KindLimitExceeded {
					t.Fatalf("41 distinct nested witnesses under one short root qualified: %v, %v", names, err)
				}
			}
		})
	}
	m := nativeGoOracleManifest{Kind: "go_top_level_v1", Cases: map[string][]string{"case:one": {"TestCorpus/same"}, "case:two": {"TestCorpus/same"}}}
	names, err := nativeOracleCaseTests(m, OracleControl{CaseIDs: []string{"case:one", "case:two"}})
	if err != nil || !slices.Equal(names, []string{"TestCorpus/same"}) {
		t.Fatalf("shared witness counted twice: %v, %v", names, err)
	}
}

func TestNativeFootprintRecipeCaseDecoder(t *testing.T) {
	goRecipe := []byte(`{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/corpus_test.go"],"fixture_files":[".concord/scenarios/corpus.json"],"cases":{"case:one":["TestCorpus/one"],"case:other":["TestOther"]}}`)
	r, err := ResolveNativeRecipeCase(goRecipe, "case:one")
	if err != nil || r.Kind != "go_top_level_v1" || r.Cwd != "pkg" || !slices.Equal(r.Witnesses, []string{"TestCorpus/one"}) || !slices.Equal(r.FixtureFiles, []string{".concord/scenarios/corpus.json"}) {
		t.Fatalf("case binding: %+v %v", r, err)
	}
	if _, err := ResolveNativeRecipeCase(goRecipe, "case:absent"); err == nil {
		t.Fatal("unknown case qualified")
	}
	packageRecipe := []byte(`{"kind":"go_package_v1","package_cwd":"pkg","cases":{"case:package":["go_package:./pkg"]},"expected_skips":["TestPrerequisite"]}`)
	r, err = ResolveNativeRecipeCase(packageRecipe, "case:package")
	if err != nil || !slices.Equal(r.Witnesses, []string{"go_package:./pkg"}) || len(r.HarnessFiles) != 0 {
		t.Fatalf("package binding: %+v %v", r, err)
	}
	target := []byte(`{"kind":"target_runner_v1","root":"scripts","materializer":"none","runner":"check","harness_files":["scripts/check.py"],"support_files":[".concord/scenarios/corpus.json"],"cases":{"case:one":["check-one"],"case:two":["check-two"]},"checks":[{"witness":"check-one","argv":["python3","check.py","one"]},{"witness":"check-two","argv":["python3","check.py","two"]}],"pin":{"python":"3.13.7"}}`)
	r, err = ResolveNativeRecipeCase(target, "case:one")
	if err != nil || !slices.Equal(r.Witnesses, []string{"check-one"}) {
		t.Fatalf("target binding: %+v %v", r, err)
	}
	for _, raw := range [][]byte{bytes.Replace(goRecipe, []byte("corpus.json"), []byte("corpus.go"), 1), bytes.Replace(packageRecipe, []byte(`"expected_skips"`), []byte(`"unknown"`), 1), append(bytes.Clone(goRecipe), []byte(` {}`)...)} {
		if _, err := ResolveNativeRecipeCase(raw, "case:one"); err == nil {
			t.Fatalf("malformed recipe qualified: %s", raw)
		}
	}
}

func nativeFootprintRepo(t *testing.T, inputs map[string]string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	for name, data := range inputs {
		filename := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitRunStore(t, repo, "init", "-b", "native-footprint")
	gitRunStore(t, repo, "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "pinned fixture")
	head, err := (ExecGitRunner{}).Run(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return repo, strings.TrimSpace(string(head))
}

func TestNativeFootprintOutsideCwdFixturePinned(t *testing.T) {
	repo, head := nativeFootprintRepo(t, map[string]string{"pkg/corpus_test.go": "package p", ".concord/scenarios/corpus.json": `{"id":"one"}`})
	p := nativeOraclePlan{SubjectCommit: head, Manifest: nativeGoOracleManifest{PackageCwd: "pkg", TestFiles: []string{"pkg/corpus_test.go"}, FixtureFiles: []string{".concord/scenarios/corpus.json"}}, Bundle: NativeOracleControlBundle{Control: OracleControl{RecipeSource: WorkContextReadingSource{CommitOID: head}}}}
	if err := collectNativeGoHarnessFiles(context.Background(), ExecGitRunner{}, WorktreeEntry{Path: repo}, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 2 {
		t.Fatalf("fixture missing from pinned files: %+v", p.Files)
	}
	if err := os.WriteFile(filepath.Join(repo, ".concord/scenarios/corpus.json"), []byte(`{"id":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "changed corpus")
	p.SubjectCommit = "HEAD"
	p.Files = nil
	if err := collectNativeGoHarnessFiles(context.Background(), ExecGitRunner{}, WorktreeEntry{Path: repo}, &p); err == nil {
		t.Fatal("candidate rewrote pinned scenario assertions")
	}
	for _, name := range []string{"../data.json", "pkg/replacement.go", "go.mod", "scripts/check.py", "adapter/test.ts", "package.json"} {
		if nativeFixtureDataPath(name) {
			t.Errorf("executable or escaping fixture accepted: %s", name)
		}
	}
	for _, name := range []string{"internal/store/testdata/native-target-uv/pyproject.toml", "internal/store/testdata/native-target-uv/uv.lock"} {
		if !nativeFixtureDataPath(name) {
			t.Errorf("inert existing testdata fixture refused: %s", name)
		}
	}
}

func TestNativeFootprintPackageCandidateInventory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("package p\nimport(\"testing\";\"os\")\nfunc TestMain(m *testing.M){os.Exit(m.Run())}\nfunc TestOne(t *testing.T){}\nfunc Test_two(t *testing.T){}\n")
	name := filepath.Join(root, "pkg/inventory_test.go")
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	i, names, err := nativeGoPackageTestInventory(r, "pkg", nativeGoPackage{TestGoFiles: []string{"inventory_test.go"}})
	if err != nil || i.TestCount != 2 || !slices.Equal(names, []string{"TestOne", "Test_two"}) {
		t.Fatalf("candidate inventory: %+v %v %v", i, names, err)
	}
	for _, runnable := range []string{"FuzzThing", "Example"} {
		if err := os.WriteFile(name, append(bytes.Clone(data), []byte("func "+runnable+"(){}\n")...), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := nativeGoPackageTestInventory(r, "pkg", nativeGoPackage{TestGoFiles: []string{"inventory_test.go"}}); err == nil {
			t.Fatalf("unsupported %s omitted from completeness", runnable)
		}
	}
}

func TestNativeFootprintPackageProducerExecutesCandidate(t *testing.T) {
	ctx := context.Background()
	recipe := `{"kind":"go_package_v1","package_cwd":"pkg","cases":{"case:package":["go_package:./pkg"]},"expected_skips":["TestPrerequisite"]}`
	repo, pinned := nativeFootprintRepo(t, map[string]string{
		"go.mod":                "module example.invalid/packageproof\n\ngo 1.26.0\ntoolchain " + runtime.Version() + "\n",
		"pkg/value.go":          "package p\nimport _ \"embed\"\n//go:embed value.txt\nvar value string\n",
		"pkg/value.txt":         "candidate embed",
		"pkg/testdata/data.txt": "candidate testdata",
		"pkg/value_test.go": `package p
import("testing";"os")
func TestMain(m *testing.M){if value!="candidate embed"{os.Exit(2)};os.Exit(m.Run())}
func TestOriginal(t *testing.T){if b,e:=os.ReadFile("testdata/data.txt");e!=nil||string(b)!="candidate testdata"{t.Fatal("testdata",e)}}
func TestPrerequisite(t *testing.T){t.Skip("synthetic missing prerequisite")}
`,
		"pkg/recipe.json": recipe,
	})
	if err := os.WriteFile(filepath.Join(repo, "pkg/new_test.go"), []byte("package p\nimport \"testing\"\nfunc TestCandidate(t *testing.T){t.Run(\"nested\",func(t *testing.T){t.Skip(\"non-required nested skip\")})}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "add", ".")
	gitRunStore(t, repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "candidate tests")
	head, err := (ExecGitRunner{}).Run(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	subject := strings.TrimSpace(string(head))
	bundle := NativeOracleControlBundle{Control: OracleControl{Cwd: "pkg", CaseIDs: []string{"case:package"}, Argv: []string{"go", "test", "-count=1", "."}, RecipeSource: WorkContextReadingSource{ProjectID: "project", CommitOID: pinned, Path: "pkg/recipe.json"}}, Owner: OracleOwner{Mechanism: OracleMechanism{ProjectID: "project"}}}
	req := WorktreeVerifyRequest{ProjectID: "project", SupportedBudgetSeconds: 60, Oracle: &NativeOracleRequest{Phase: "execute"}}
	prepareReq := req
	prepareReq.Oracle = &NativeOracleRequest{Phase: "prepare"}
	preparedPlan, err := normalizeNativeOraclePlan(ctx, ExecGitRunner{}, WorktreeEntry{Path: repo}, prepareReq, bundle, 1, subject, "identity", nativeOracleAuthorization{})
	if err != nil {
		t.Fatal(err)
	}
	prepared := nativeOracleInitialResult(prepareReq, WorktreeEntry{}, preparedPlan).Oracle
	launches := 0
	producer := &Store{nativeOracleProgramLaunched: func() { launches++ }}
	if err := producer.runNativeGoOracle(ctx, ExecGitRunner{}, WorktreeEntry{Path: repo}, prepareReq, preparedPlan, &nativeStreamCapture{complete: true}, prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Qualification != "ready" || prepared.PackageInventory != nil || launches != 0 {
		t.Fatalf("preparation claimed execution: %+v launches=%d", prepared, launches)
	}
	p, err := normalizeNativeOraclePlan(ctx, ExecGitRunner{}, WorktreeEntry{Path: repo}, req, bundle, 1, subject, "identity", nativeOracleAuthorization{})
	if err != nil {
		t.Fatal(err)
	}
	result := nativeOracleInitialResult(req, WorktreeEntry{}, p).Oracle
	capture := &nativeStreamCapture{complete: true}
	if err := producer.runNativeGoOracle(ctx, ExecGitRunner{}, WorktreeEntry{Path: repo}, req, p, capture, result); err != nil {
		t.Fatal(err)
	}
	if result.Qualification != "pass" || result.PackageInventory == nil || result.PackageInventory.TestCount != 3 || result.PackageInventory.PassedCount != 2 || !slices.Equal(result.PackageInventory.SkippedTests, []string{"TestPrerequisite"}) || !slices.Equal(result.PackageInventory.NestedSkippedTests, []string{"TestCandidate/nested"}) || len(result.SelectedTestNames) != 0 || !nativeOraclePassComplete(p, result) {
		t.Fatalf("package producer result: %+v", result)
	}
	if bytes.Contains(capture.stdout, []byte(`"Action":`)) {
		t.Fatal("duplicate converter event stream retained")
	}
	if !bytes.Contains(capture.stdout, []byte("TestCandidate")) || !capture.complete {
		t.Fatal("candidate execution output missing or incomplete")
	}
	if launches != 1 {
		t.Fatalf("executed package launch count=%d", launches)
	}
	proof := *result.PackageInventory
	result.PackageInventory.TestCount++
	if nativeOraclePassComplete(p, result) {
		t.Fatal("incomplete package inventory qualified")
	}
	*result.PackageInventory = proof
	result.PackageInventory.SkippedTests = []string{"TestCandidate"}
	if nativeOraclePassComplete(p, result) {
		t.Fatal("unexpected skip qualified")
	}
	*result.PackageInventory = proof
	// This is a producer regression, not a dispatched workflow receipt.
	t.Logf("candidate=%s pinned=%s tests=%d passes=%d skips=%v raw_bytes=%d", subject, pinned, result.PackageInventory.TestCount, result.PackageInventory.PassedCount, result.PackageInventory.SkippedTests, len(capture.stdout))
}

func TestNativeFootprintSelectedProducerNestedCorpus(t *testing.T) {
	ctx := context.Background()
	recipe := `{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/corpus_test.go"],"fixture_files":[".concord/scenarios/corpus.json"],"cases":{"case:one":["TestCorpus/scenario-one"],"case:other":["TestUnrelated"]}}`
	repo, head := nativeFootprintRepo(t, map[string]string{
		"go.mod":                         "module example.invalid/corpusproof\n\ngo 1.26.0\ntoolchain " + runtime.Version() + "\n",
		"pkg/value.go":                   "package p\n",
		".concord/scenarios/corpus.json": `{"id":"scenario-one","value":7}`,
		"pkg/corpus_test.go": `package p
import("testing";"os";"encoding/json")
func TestCorpus(t *testing.T){raw,e:=os.ReadFile("../.concord/scenarios/corpus.json");if e!=nil{t.Fatal(e)};var c struct{ID string;Value int};if e=json.Unmarshal(raw,&c);e!=nil{t.Fatal(e)};t.Run(c.ID,func(t *testing.T){if c.Value!=7{t.Fatal(c.Value)}})}
func TestUnrelated(t *testing.T){t.Fatal("unrelated case must not execute")}
`,
		"pkg/recipe.json": recipe,
	})
	b := NativeOracleControlBundle{Owner: OracleOwner{Mechanism: OracleMechanism{ProjectID: "project"}}, Control: OracleControl{Cwd: "pkg", CaseIDs: []string{"case:one"}, Argv: []string{"go", "test", "-count=1", "-run", "^(TestCorpus)$", "."}, RecipeSource: WorkContextReadingSource{ProjectID: "project", CommitOID: head, Path: "pkg/recipe.json"}}}
	req := WorktreeVerifyRequest{ProjectID: "project", SupportedBudgetSeconds: 60, Oracle: &NativeOracleRequest{Phase: "execute"}}
	p, err := normalizeNativeOraclePlan(ctx, ExecGitRunner{}, WorktreeEntry{Path: repo}, req, b, 1, head, "identity", nativeOracleAuthorization{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Manifest.Cases) != 1 || p.Manifest.Cases["case:other"] != nil {
		t.Fatal("unrelated case retained")
	}
	result := nativeOracleInitialResult(req, WorktreeEntry{}, p).Oracle
	if err := (&Store{}).runNativeGoOracle(ctx, ExecGitRunner{}, WorktreeEntry{Path: repo}, req, p, &nativeStreamCapture{complete: true}, result); err != nil {
		t.Fatal(err)
	}
	if result.Qualification != "pass" || !slices.Equal(result.ObservedTestNames, []string{"TestCorpus/scenario-one"}) {
		t.Fatalf("exact corpus observation missing: %+v", result)
	}
}

func TestNativeFootprintNestedWitnessMustRunAndPass(t *testing.T) {
	for _, outcome := range []string{"pass", "skip", "missing"} {
		t.Run(outcome, func(t *testing.T) {
			var events []byte
			add := func(action, name string) {
				b, _ := json.Marshal(map[string]string{"Action": action, "Package": "pkg", "Test": name})
				events = append(append(events, b...), '\n')
			}
			add("start", "")
			add("run", "TestCorpus")
			if outcome != "missing" {
				add("run", "TestCorpus/scenario-one")
				add(outcome, "TestCorpus/scenario-one")
			}
			add("pass", "TestCorpus")
			add("pass", "")
			err := validateNativeOracleWitnessEvents(events, "pkg", []string{"TestCorpus/scenario-one"})
			if (err == nil) != (outcome == "pass") {
				t.Fatalf("outcome %s: %v", outcome, err)
			}
		})
	}
}

func TestNativeFootprintLargePinnedInventory(t *testing.T) {
	inputs := map[string]string{}
	var tests, scripts []string
	for i := 0; i < 33; i++ {
		name := fmt.Sprintf("pkg/value%d_test.go", i)
		tests = append(tests, name)
		inputs[name] = "package p"
		name = fmt.Sprintf("scripts/check%d.py", i)
		scripts = append(scripts, name)
		inputs[name] = "assert True"
	}
	repo, head := nativeFootprintRepo(t, inputs)
	p := nativeOraclePlan{SubjectCommit: head, Manifest: nativeGoOracleManifest{PackageCwd: "pkg", TestFiles: tests}, Bundle: NativeOracleControlBundle{Control: OracleControl{RecipeSource: WorkContextReadingSource{CommitOID: head}}}}
	if err := collectNativeGoHarnessFiles(context.Background(), ExecGitRunner{}, WorktreeEntry{Path: repo}, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 0 || p.HarnessInputCount != 33 || !workflowDigest(p.HarnessInputDigest) {
		t.Fatalf("large inventory not compact: %+v", p)
	}
	files, err := nativeGoPinnedFiles(context.Background(), ExecGitRunner{}, repo, p)
	if err != nil || len(files) != 33 {
		t.Fatalf("pinned inventory lost: %d %v", len(files), err)
	}
	p.HarnessInputDigest = nativeDigest([]byte("wrong"))
	if _, err := nativeGoPinnedFiles(context.Background(), ExecGitRunner{}, repo, p); err == nil {
		t.Fatal("tampered inventory digest qualified")
	}
	m := nativeTargetOracleManifest{HarnessFiles: scripts}
	p.Files = nil
	p.HarnessInputCount = 0
	p.HarnessInputDigest = ""
	if err := readNativeTargetHarnessFiles(context.Background(), ExecGitRunner{}, WorktreeEntry{Path: repo}, &p, m); err != nil {
		t.Fatal(err)
	}
	p.Target = &nativeTargetOraclePlan{Recipe: m}
	files, err = nativeGoPinnedFiles(context.Background(), ExecGitRunner{}, repo, p)
	if err != nil || len(files) != 33 || len(p.Files) != 0 {
		t.Fatalf("target inventory not bound: %d %v", len(files), err)
	}
}

func TestNativeFootprintPackageEventRefusals(t *testing.T) {
	for _, mode := range []string{"complete", "missing", "skip", "duplicate", "failure", "late-child", "oversized", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var raw []byte
			add := func(action, test string) {
				b, _ := json.Marshal(map[string]string{"Action": action, "Test": test, "Package": "pkg"})
				raw = append(append(raw, b...), '\n')
			}
			add("start", "")
			add("run", "TestOne")
			if mode == "late-child" {
				add("run", "TestOne/child")
			}
			add("pass", "TestOne")
			if mode != "missing" {
				add("run", "TestTwo")
				if mode == "skip" {
					add("skip", "TestTwo")
				} else if mode == "failure" {
					add("fail", "TestTwo")
				} else {
					add("pass", "TestTwo")
				}
			}
			if mode == "duplicate" {
				add("pass", "TestOne")
			}
			if mode == "late-child" {
				add("pass", "TestOne/child")
			}
			if mode == "oversized" {
				raw = append(raw, []byte(strings.Repeat("x", 65537))...)
			}
			if mode == "malformed" {
				raw = append(raw, []byte("{bad}\n")...)
			}
			add("pass", "")
			err := gradeNativeGoEvents(bytes.NewReader(raw), newNativeGoEventGrade("pkg", []string{"TestOne", "TestTwo"}, true, nil))
			if (err == nil) != (mode == "complete") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}

func TestNativeFootprintPackageRecipeCannotReplaceTests(t *testing.T) {
	m := nativeGoOracleManifest{Kind: nativeGoPackageKind, PackageCwd: "pkg", Cases: map[string][]string{"case:package": {"go_package:./pkg"}}}
	for _, input := range []string{"test", "fixture", "skip", "witness", "selector"} {
		t.Run(input, func(t *testing.T) {
			v := m
			c := OracleControl{Cwd: "pkg", CaseIDs: []string{"case:package"}, Argv: []string{"go", "test", "-count=1", "."}}
			switch input {
			case "test":
				v.TestFiles = []string{"pkg/pinned_test.go"}
			case "fixture":
				v.FixtureFiles = []string{"pkg/data.json"}
			case "skip":
				v.ExpectedSkips = []string{"TestOne/child"}
			case "witness":
				v.Cases = map[string][]string{"case:package": {"TestOne"}}
			case "selector":
				c.Argv = []string{"go", "test", "-count=1", "-run", "^(TestOne)$", "."}
			}
			err := validateNativeGoManifest(v)
			if err == nil {
				_, err = nativeOracleSelector(v, c)
			}
			if err == nil {
				t.Fatalf("%s silently narrowed/replaced package proof", input)
			}
		})
	}
}

func TestNativeFootprintPinnedTestMainSelected(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	mainFile := "package p\nimport(\"testing\";\"os\")\nfunc TestMain(m *testing.M){os.Exit(m.Run())}\n"
	testFile := "package p\nimport \"testing\"\nfunc TestPinned(t *testing.T){}\n"
	for name, data := range map[string]string{"pkg/main_test.go": mainFile, "pkg/pinned_test.go": testFile} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := nativeOraclePlan{Manifest: nativeGoOracleManifest{PackageCwd: "pkg", TestFiles: []string{"pkg/main_test.go", "pkg/pinned_test.go"}, Cases: map[string][]string{"case:x": {"TestPinned"}}}, Bundle: NativeOracleControlBundle{Control: OracleControl{Cwd: "pkg", CaseIDs: []string{"case:x"}, Argv: []string{"go", "test", "-count=1", "-run", "^(TestPinned)$", "."}}}}
	pkg := nativeGoPackage{Dir: filepath.Join(root, "pkg"), ImportPath: "p", TestGoFiles: []string{"main_test.go", "pinned_test.go"}}
	raw, _ := json.Marshal(pkg)
	if _, err := validateNativeGoMetadata(raw, root, p); err != nil {
		t.Fatalf("one pinned TestMain refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg/second_test.go"), []byte(mainFile), 0600); err != nil {
		t.Fatal(err)
	}
	p.Manifest.TestFiles = append(p.Manifest.TestFiles, "pkg/second_test.go")
	pkg.TestGoFiles = append(pkg.TestGoFiles, "second_test.go")
	raw, _ = json.Marshal(pkg)
	if _, err := validateNativeGoMetadata(raw, root, p); failureKind(err) != KindUnavailable {
		t.Fatalf("second pinned TestMain admitted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg/main_test.go"), []byte("package p\nimport \"testing\"\nfunc TestMain(t *testing.T){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateNativeGoMetadata(raw, root, p); failureKind(err) != KindUnavailable {
		t.Fatal("a TestMain without the testing.M run signature was admitted")
	}
}

func TestNativeFootprintCorpusRecipesDecode(t *testing.T) {
	for name, want := range map[string]struct {
		caseID     string
		witnesses  int
		fixtureHas string
	}{
		"../../.concord/oracles/store/workflow-corpus.v1.json":           {"case:workflow-corpus-a", 6, ".concord/scenarios/workflow-engine.v1.json"},
		"../../.concord/oracles/store/workflow-corpus.v1.json#b":         {"case:workflow-corpus-b", 6, ""},
		"../../.concord/oracles/agents/agent-jobs-corpus.v1.json":        {"case:agent-jobs-corpus-a", 8, ".concord/scenarios/agent-jobs.v1.json"},
		"../../.concord/oracles/agents/agent-jobs-corpus.v1.json#c":      {"case:agent-jobs-corpus-c", 6, ""},
		"../../.concord/oracles/store/continuity-vacate-reclaim.v1.json": {"case:continuity-vacate-reclaim", 1, ".concord/scenarios/adapter-continuity.v1.json"},
	} {
		t.Run(name, func(t *testing.T) {
			path := name
			if i := strings.Index(path, "#"); i >= 0 {
				path = path[:i]
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			r, err := ResolveNativeRecipeCase(raw, want.caseID)
			if err != nil {
				t.Fatalf("resolve %s: %v", want.caseID, err)
			}
			if len(r.Witnesses) != want.witnesses {
				t.Fatalf("witnesses=%d, want %d", len(r.Witnesses), want.witnesses)
			}
			if want.fixtureHas != "" && !slices.Contains(r.FixtureFiles, want.fixtureHas) {
				t.Fatalf("fixture %s missing: %v", want.fixtureHas, r.FixtureFiles)
			}
			if _, err := ResolveNativeRecipeCase(raw, "case:absent"); err == nil {
				t.Fatal("unknown case qualified")
			}
		})
	}
}
