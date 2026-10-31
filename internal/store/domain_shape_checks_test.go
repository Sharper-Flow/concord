package store

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func ParseDomainShapeCheckInput(raw []byte) (DomainShapeCheckInput, error) {
	var input DomainShapeCheckInput
	if err := domainShapeJSON(raw, &input); err != nil {
		return input, err
	}
	return input, validateDomainShapeInput(input)
}

func domainShapeFixture(operation string) DomainShapeCheckInput {
	return DomainShapeCheckInput{
		SchemaVersion: "1.0", Operation: operation,
		Subject:   DomainShapeSubject{"product-demo", "project-demo", strings.Repeat("a", 40), "sha256:" + strings.Repeat("b", 64)},
		Roots:     []DomainShapeRoot{{Path: "src/orders", Role: "domain", OwnerDomainID: "orders"}, {Path: "src/ledger", Role: "domain", OwnerDomainID: "ledger"}, {Path: "cmd/app", Role: "composition"}, {Path: "test/support", Role: "test_support"}},
		DependsOn: []DomainShapeEdge{{"orders", "ledger"}}, Facts: []DomainFact{},
		RepositoryAnchors: []DomainFactRepositoryAnchor{}, EnforcementAnchors: []DomainFactEnforcementAnchor{}, RequiredChecks: []string{"ci-test"}, Strict: true,
		Ratchet: DomainShapeRatchet{"1.0", []string{}, []DomainShapeImport{}},
	}
}

func domainShapeOutput(input DomainShapeCheckInput) DomainShapeCheckOutput {
	return DomainShapeCheckOutput{SchemaVersion: input.SchemaVersion, Operation: input.Operation, Subject: input.Subject, Complete: true}
}

func domainShapeCodes(result DomainShapeEvaluation) []string {
	var codes []string
	for _, finding := range result.Findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func TestDomainShapeBoundaryGraphAndStrictRatchet(t *testing.T) {
	input := domainShapeFixture("boundary")
	output := domainShapeOutput(input)
	output.Imports = []DomainShapeImport{{"src/orders", "src/ledger"}, {"cmd/app", "src/orders"}, {"src/orders", "src/orders"}, {"test/support", "src/ledger"}}
	result, err := EvaluateDomainShapeCheck(input, &output)
	if err != nil || result.State != "satisfied" {
		t.Fatalf("allowed graph: %+v %v", result, err)
	}
	input.DependsOn = []DomainShapeEdge{}
	result, err = EvaluateDomainShapeCheck(input, &output)
	if err != nil || !slices.Contains(domainShapeCodes(result), "disallowed_import") {
		t.Fatalf("supplied graph was not enforced: %+v %v", result, err)
	}
	input.Ratchet.Imports = []DomainShapeImport{{"src/orders", "src/ledger"}}
	result, err = EvaluateDomainShapeCheck(input, &output)
	if err != nil || result.State != "satisfied" {
		t.Fatalf("existing ratchet: %+v %v", result, err)
	}
	output.Imports = append(output.Imports, DomainShapeImport{"src/ledger", "src/orders"})
	result, err = EvaluateDomainShapeCheck(input, &output)
	if err != nil || result.State != "outstanding" {
		t.Fatalf("new violation admitted: %+v %v", result, err)
	}
	input.Roots = append(input.Roots, DomainShapeRoot{Path: "src/mixed", Role: "mixed", DomainIDs: []string{"orders", "ledger"}})
	result, err = EvaluateDomainShapeCheck(input, &output)
	if err != nil || !slices.Contains(domainShapeCodes(result), "new_mixed_root") {
		t.Fatalf("new mixed root admitted: %+v %v", result, err)
	}
	absent, err := EvaluateDomainShapeCheck(input, nil)
	if err != nil || absent.State != "outstanding" || absent.Verification != "unverified" || !slices.Contains(domainShapeCodes(absent), "new_mixed_root") {
		t.Fatalf("absent boundary checker hid a new mixed root: %+v %v", absent, err)
	}
	input.Ratchet.MixedRoots = []string{"src/mixed"}
	input.Ratchet.Imports = append(input.Ratchet.Imports, DomainShapeImport{"src/ledger", "src/orders"})
	result, err = EvaluateDomainShapeCheck(input, &output)
	if err != nil || result.State != "satisfied" {
		t.Fatalf("ratchet mutated or ignored: %+v %v", result, err)
	}
}

func TestDomainShapeBoundaryRejectsUnknownAndDuplicateRoots(t *testing.T) {
	for _, imports := range [][]DomainShapeImport{{{"unknown", "src/orders"}}, {{"src/orders", "src/ledger"}, {"src/orders", "src/ledger"}}} {
		input := domainShapeFixture("boundary")
		output := domainShapeOutput(input)
		output.Imports = imports
		if _, err := EvaluateDomainShapeCheck(input, &output); err == nil {
			t.Fatal("invalid imports accepted")
		}
	}
	input := domainShapeFixture("boundary")
	input.Roots = append(input.Roots, input.Roots[0])
	if _, err := EvaluateDomainShapeCheck(input, nil); err == nil {
		t.Fatal("duplicate roots accepted")
	}
}

func TestDomainShapeExtractorsMissingOrphanAndAbsent(t *testing.T) {
	anchor := DomainFactRepositoryAnchor{"project-demo", "src/orders/config.go", "ReadLimit"}
	for _, operation := range []string{"data_objects", "config_reads"} {
		t.Run(operation, func(t *testing.T) {
			input := domainShapeFixture(operation)
			if operation == "data_objects" {
				input.Facts = []DomainFact{{ID: "fact-orders", Kind: DomainFactDataObject, OwningRoot: "src/orders", DataObject: &DomainDataObjectFact{Name: "orders", ResourceID: "resource-demo"}}}
			} else {
				input.Facts = []DomainFact{{ID: "fact-limit", Kind: DomainFactConfigCoupling, OwningRoot: "src/orders", ConfigCoupling: &DomainConfigCouplingFact{Setting: "LIMIT", Source: "environment", ReadBy: anchor}}}
			}
			result, err := EvaluateDomainShapeCheck(input, nil)
			if err != nil || result.State != "unmeasured" || result.Verification != "unverified" || len(result.Findings) != 1 {
				t.Fatalf("absent extractor: %+v %v", result, err)
			}
			output := domainShapeOutput(input)
			if operation == "data_objects" {
				output.DataObjects = []DomainShapeDataObject{{"src/orders", "resource-demo", "events"}}
			} else {
				output.ConfigReads = []DomainShapeConfigRead{{"src/orders", "PORT", "environment", anchor}}
			}
			result, err = EvaluateDomainShapeCheck(input, &output)
			if err != nil || !slices.Equal(domainShapeCodes(result), []string{"orphan_fact", "missing_fact"}) {
				t.Fatalf("incomplete comparison: %+v %v", result, err)
			}
			if operation == "data_objects" {
				output.DataObjects[0].Name = "orders"
			} else {
				output.ConfigReads[0].Setting = "LIMIT"
			}
			result, err = EvaluateDomainShapeCheck(input, &output)
			if err != nil || result.State != "satisfied" || result.Verification != "verified" {
				t.Fatalf("matching inventory: %+v %v", result, err)
			}
		})
	}
}

func TestDomainShapeReferencesObserveResolutionAndMembership(t *testing.T) {
	input := domainShapeFixture("references")
	anchor := DomainFactEnforcementAnchor{"go_test", "src/orders:TestLimit", "ci-test"}
	repo := DomainFactRepositoryAnchor{"project-demo", "src/orders/config.go", "ReadLimit"}
	input.EnforcementAnchors = []DomainFactEnforcementAnchor{anchor}
	input.RepositoryAnchors = []DomainFactRepositoryAnchor{repo}
	output := domainShapeOutput(input)
	output.EnforcementAnchors = []DomainShapeEnforcementResolution{{anchor, true, []string{}}}
	output.RepositoryAnchors = []DomainShapeRepositoryResolution{{repo, true}}
	result, err := EvaluateDomainShapeCheck(input, &output)
	if err != nil || result.RequiredCheckAnchors[anchor] || !slices.Contains(domainShapeCodes(result), "anchor_not_required") {
		t.Fatalf("requested membership became proof: %+v %v", result, err)
	}
	output.EnforcementAnchors[0].RequiredCheckIDs = []string{"ci-test"}
	result, err = EvaluateDomainShapeCheck(input, &output)
	if err != nil || !result.RequiredCheckAnchors[anchor] || !result.RepositoryAnchors[repo] || result.State != "satisfied" {
		t.Fatalf("observed resolution: %+v %v", result, err)
	}
	output.EnforcementAnchors[0].Resolved = false
	result, err = EvaluateDomainShapeCheck(input, &output)
	if err != nil || result.RequiredCheckAnchors[anchor] || !slices.Contains(domainShapeCodes(result), "unresolved_anchor") {
		t.Fatalf("membership became anchor resolution: %+v %v", result, err)
	}
	output.RepositoryAnchors = nil
	if _, err := EvaluateDomainShapeCheck(input, &output); err == nil {
		t.Fatal("omitted observation accepted")
	}
	output.RepositoryAnchors = []DomainShapeRepositoryResolution{{repo, true}, {repo, true}}
	if _, err := EvaluateDomainShapeCheck(input, &output); err == nil {
		t.Fatal("duplicate observation accepted")
	}
	output.RepositoryAnchors = []DomainShapeRepositoryResolution{{repo, true}}
	output.EnforcementAnchors[0].RequiredCheckIDs = []string{"other-check"}
	if _, err := EvaluateDomainShapeCheck(input, &output); err == nil {
		t.Fatal("undeclared required-check membership accepted")
	}
}

func TestDomainShapeProtocolStrictWireAndBounds(t *testing.T) {
	input := domainShapeFixture("boundary")
	raw, _ := json.Marshal(input)
	if _, err := ParseDomainShapeCheckInput(raw); err != nil {
		t.Fatal(err)
	}
	output := domainShapeOutput(input)
	valid, _ := json.Marshal(output)
	if _, err := ParseDomainShapeCheckOutput(valid); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic boundary input=%d bytes output=%d bytes; bound=%d", len(raw), len(valid), DomainShapeMaxBytes)
	for _, raw := range []string{
		strings.Replace(string(valid), `"complete":true`, `"complete":true,"complete":true`, 1),
		strings.Replace(string(valid), `"complete":true`, `"complete":null`, 1),
		strings.Replace(string(valid), `"complete":true`, `"complete":false`, 1),
		strings.Replace(string(valid), `"imports":[]`, `"imports":null`, 1),
		strings.Replace(string(valid), `"imports":[],`, ``, 1),
		strings.Replace(string(valid), `"complete":true`, `"Complete":true`, 1),
		strings.Replace(string(valid), `"complete":true`, `"complete":true,"pass":true`, 1),
		string(valid) + ` {}`,
		string(valid) + strings.Repeat(" ", DomainShapeMaxBytes),
	} {
		if _, err := ParseDomainShapeCheckOutput([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid output: %.200s", raw)
		}
	}
	for _, change := range []func(*DomainShapeCheckOutput){func(o *DomainShapeCheckOutput) { o.Subject.ProductID = "other-product" }, func(o *DomainShapeCheckOutput) { o.Subject.CommitOID = strings.Repeat("c", 40) }, func(o *DomainShapeCheckOutput) { o.Subject.ContentHash = "sha256:" + strings.Repeat("c", 64) }, func(o *DomainShapeCheckOutput) { o.Complete = false }} {
		changed := domainShapeOutput(input)
		change(&changed)
		if _, err := EvaluateDomainShapeCheck(input, &changed); err == nil {
			t.Fatal("stale/foreign/incomplete output accepted")
		}
	}
	input.Roots = append(input.Roots, DomainShapeRoot{Path: "src/*", Role: "composition"})
	if _, err := EvaluateDomainShapeCheck(input, nil); err == nil {
		t.Fatal("glob root accepted")
	}
}

type domainShapeFakeRunner struct {
	t        *testing.T
	calls    int
	response []byte
}

func (runner *domainShapeFakeRunner) Run(_ context.Context, root string, argv []string, raw []byte) ([]byte, error) {
	runner.calls++
	if root != "/synthetic/checkout" || !slices.Equal(argv, []string{"bin/shape", "--json"}) {
		runner.t.Fatalf("wrong explicit invocation: %s %v", root, argv)
	}
	input, err := ParseDomainShapeCheckInput(raw)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(input.DependsOn, []DomainShapeEdge{{"orders", "ledger"}}) {
		runner.t.Fatal("allowed edges not supplied to Product command")
	}
	if runner.response != nil {
		return runner.response, nil
	}
	return json.Marshal(domainShapeOutput(input))
}

func TestDomainShapeExplicitRunnerAndTooling(t *testing.T) {
	raw := []byte(`{"schema_version":"1.0","project":"demo","tools":[{"id":"shape-check","invocation":"bin/shape --json"},{"id":"ci-test","invocation":"go test ./..."}],"domain_shape":{"boundary_check":"shape-check","required_checks":["ci-test"]}}`)
	tooling, err := ParseProjectToolingManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	input := domainShapeFixture("boundary")
	runner := &domainShapeFakeRunner{t: t}
	result, err := RunDomainShapeCheck(context.Background(), runner, "/synthetic/checkout", tooling, input)
	if err != nil || result.State != "satisfied" || runner.calls != 1 {
		t.Fatalf("explicit run: %+v %v calls=%d", result, err, runner.calls)
	}
	input.Operation = "data_objects"
	result, err = RunDomainShapeCheck(context.Background(), runner, "/synthetic/checkout", tooling, input)
	if err != nil || result.Verification != "unverified" || runner.calls != 1 {
		t.Fatalf("absent extractor executed or verified: %+v %v", result, err)
	}
	input.RequiredChecks = []string{"unknown-check"}
	if _, err := RunDomainShapeCheck(context.Background(), runner, "/synthetic/checkout", tooling, input); err == nil || runner.calls != 1 {
		t.Fatal("caller replaced required checks")
	}
	for _, change := range []string{
		`"boundary_check":"missing-check"`, `"required_checks":["missing-check"]`, `"required_checks":["ci-test","ci-test"]`, `"boundary_check":""`, `"boundary_check":null`, `"invented":true`,
	} {
		candidate := strings.Replace(string(raw), `"boundary_check":"shape-check","required_checks":["ci-test"]`, change, 1)
		if _, err := ParseProjectToolingManifest([]byte(candidate)); err == nil {
			t.Fatalf("invalid declaration accepted: %s", change)
		}
	}
	empty, err := ParseProjectToolingManifest([]byte(`{"schema_version":"1.0","project":"demo","tools":[],"domain_shape":{}}`))
	if err != nil || empty.DomainShape == nil || len(empty.Declared) != 0 {
		t.Fatalf("fresh Product empty tooling: %+v %v", empty, err)
	}
	if _, err := ParseProjectToolingManifest([]byte(`{"schema_version":"1.0","project":"demo","tools":[],"domain_shape":null}`)); err == nil {
		t.Fatal("null shape accepted")
	}
}

// The subprocess fixture tests IO/deadline behavior without a shell or analyser.
func TestDomainShapeCommandProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "domain-shape-helper" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "stdout":
		_, _ = os.Stdout.Write(bytesForDomainShapeOverflow())
	case "stderr":
		_, _ = os.Stderr.Write(bytesForDomainShapeOverflow())
	case "sleep":
		time.Sleep(time.Minute)
	case "fail":
		os.Exit(7)
	}
	os.Exit(0)
}

func bytesForDomainShapeOverflow() []byte { return []byte(strings.Repeat("x", DomainShapeMaxBytes+1)) }

func TestDomainShapeExecRunnerBoundsDeadlineAndNoShell(t *testing.T) {
	argv := func(mode string) []string {
		return []string{os.Args[0], "-test.run=^TestDomainShapeCommandProcess$", "domain-shape-helper", mode}
	}
	runner := ExecDomainShapeCommandRunner{Timeout: time.Second}
	result, err := runner.Run(context.Background(), t.TempDir(), argv("echo"), []byte(`{"literal":"$(never executed); |"}`))
	if err != nil || string(result) != `{"literal":"$(never executed); |"}` {
		t.Fatalf("argv/stdin altered: %s %v", result, err)
	}
	for _, mode := range []string{"stdout", "stderr", "fail"} {
		if result, err := runner.Run(context.Background(), t.TempDir(), argv(mode), nil); err == nil || result != nil {
			t.Fatalf("%s was accepted", mode)
		}
	}
	runner.Timeout = 20 * time.Millisecond
	if _, err := runner.Run(context.Background(), t.TempDir(), argv("sleep"), nil); err == nil {
		t.Fatal("deadline ignored")
	}
	for _, budget := range []time.Duration{-1, 6 * time.Minute} {
		runner.Timeout = budget
		if _, err := runner.Run(context.Background(), t.TempDir(), argv("echo"), nil); err == nil {
			t.Fatal("invalid budget accepted")
		}
	}
	runner.Timeout = time.Second
	if _, err := runner.Run(context.Background(), t.TempDir(), argv("echo"), bytesForDomainShapeOverflow()); err == nil {
		t.Fatal("oversized stdin accepted")
	}
	if _, err := runner.Run(context.Background(), "relative", argv("echo"), nil); err == nil {
		t.Fatal("relative checkout accepted")
	}
	if _, err := runner.Run(context.Background(), t.TempDir(), []string{}, nil); err == nil {
		t.Fatal("empty command accepted")
	}
}

func TestDomainShapeProtocolSchemaMatchesTypedFields(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/domain-shape-check.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"input": DomainShapeCheckInput{}, "output": DomainShapeCheckOutput{}, "subject": DomainShapeSubject{},
		"root": DomainShapeRoot{}, "edge": DomainShapeEdge{}, "import": DomainShapeImport{}, "ratchet": DomainShapeRatchet{},
		"repository_anchor": DomainFactRepositoryAnchor{}, "enforcement_anchor": DomainFactEnforcementAnchor{},
		"data_object": DomainShapeDataObject{}, "config_read": DomainShapeConfigRead{},
		"repository_resolution": DomainShapeRepositoryResolution{}, "enforcement_resolution": DomainShapeEnforcementResolution{},
	} {
		shape := reflect.TypeOf(value)
		definition, ok := schema.Defs[name]
		if !ok || len(definition.Properties) != shape.NumField() {
			t.Fatalf("%s schema fields differ from typed protocol", name)
		}
		for index := 0; index < shape.NumField(); index++ {
			field := shape.Field(index)
			key, optional, _ := strings.Cut(field.Tag.Get("json"), ",")
			if _, ok := definition.Properties[key]; !ok {
				t.Fatalf("%s missing schema property %s", name, key)
			}
			if optional != "omitempty" && !slices.Contains(definition.Required, key) {
				t.Fatalf("%s missing required field %s", name, key)
			}
		}
	}
}
