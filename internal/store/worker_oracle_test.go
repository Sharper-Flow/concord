package store

import (
	"strings"
	"testing"
)

// testOracleCommit pins one synthetic harness commit; the store author path
// shape-checks the pin, and Git object resolution belongs to dispatch
// preparation, never to store admission.
func testOracleCommit() string { return strings.Repeat("1c9f", 10) }

// testOracleGraph is one structurally valid multi-owner oracle: two owners,
// each with two cases and two controls, covering both job predicates
// reciprocally.
func testOracleGraph() *AcceptanceOracle {
	return &AcceptanceOracle{
		Owners: []OracleOwner{
			{
				OwnerID: "owner:entry", DomainID: "root",
				Mechanism:    OracleMechanism{ProjectID: "project", Path: "internal/store/worker_jobs.go", EntryPoint: "foldWorkerJobRecorded"},
				Obligation:   "a recorded revision never changes content in place",
				PredicateIDs: []string{"predicate:one", "predicate:two"},
			},
			{
				OwnerID: "owner:dispatch", DomainID: "root",
				Mechanism:    OracleMechanism{ProjectID: "project", Path: "internal/store/worker_lanes.go", EntryPoint: "validateWorkerPacketJob"},
				Obligation:   "a dispatch binds the recorded revision digest",
				PredicateIDs: []string{"predicate:two"},
			},
		},
		Cases: []OracleCase{
			{CaseID: "case:immutable", OwnerID: "owner:entry", EntryPoint: "record", InputClass: "first recording", ExpectedState: "revision recorded", ControlIDs: []string{"control:immutable"}},
			{CaseID: "case:rerecord", OwnerID: "owner:entry", EntryPoint: "record", InputClass: "re-recording", ExpectedState: "digest-identical fold", ControlIDs: []string{"control:immutable"}},
			{CaseID: "case:bind", OwnerID: "owner:dispatch", EntryPoint: "dispatch", InputClass: "packet binding", ExpectedState: "bound revision", ControlIDs: []string{"control:bind"}},
			{CaseID: "case:tamper", OwnerID: "owner:dispatch", EntryPoint: "dispatch", InputClass: "tampered packet", ExpectedState: "refused dispatch", ControlIDs: []string{"control:bind"}},
		},
		Controls: []OracleControl{
			{
				ControlID: "control:immutable", OwnerID: "owner:entry",
				PredicateIDs: []string{"predicate:one"}, CaseIDs: []string{"case:immutable", "case:rerecord"},
				RecipeSource:          WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
				Argv:                  oracleGoArgv("immutability"),
				Cwd:                   "internal/store",
				ExpectedResult:        "pass",
				RequiredEvidenceRole:  "reported",
				ReadinessEvidenceRefs: []string{"evidence:oracle-ready"},
			},
			{
				ControlID: "control:bind", OwnerID: "owner:dispatch",
				PredicateIDs: []string{"predicate:two"}, CaseIDs: []string{"case:bind", "case:tamper"},
				RecipeSource:          WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
				Argv:                  oracleGoArgv("binding"),
				Cwd:                   "internal/store",
				ExpectedResult:        "pass",
				RequiredEvidenceRole:  "independently_executed",
				ReadinessEvidenceRefs: []string{"evidence:oracle-ready"},
			},
		},
	}
}

// A valid multi-entry owner graph passes the closed validator, and each
// structural mutation refuses. The validator reads no registry, so the same
// proof holds for replay: the graph alone decides.
func TestOwnerOracleGraph(t *testing.T) {
	t.Parallel()
	jobPredicates := []string{"predicate:one", "predicate:two"}
	if err := validateAcceptanceOracle(testOracleGraph(), jobPredicates); err != nil {
		t.Fatalf("valid multi-owner oracle refused: %v", err)
	}
	if err := validateAcceptanceOracle(nil, jobPredicates); err == nil {
		t.Fatal("absent oracle passed the validator")
	}
	cloneOracle := func(mutation func(oracle *AcceptanceOracle)) *AcceptanceOracle {
		oracle := testOracleGraph()
		mutation(oracle)
		return oracle
	}
	refusals := []struct {
		name     string
		oracle   *AcceptanceOracle
		wantText string
	}{
		{"no owners", cloneOracle(func(o *AcceptanceOracle) { o.Owners = nil }), "owners"},
		{"no cases", cloneOracle(func(o *AcceptanceOracle) { o.Cases = nil }), "cases"},
		{"no controls", cloneOracle(func(o *AcceptanceOracle) { o.Controls = nil }), "controls"},
		{"duplicate owner", cloneOracle(func(o *AcceptanceOracle) { o.Owners = append(o.Owners, o.Owners[0]) }), "declared twice"},
		{"duplicate case", cloneOracle(func(o *AcceptanceOracle) { o.Cases = append(o.Cases, o.Cases[0]) }), "declared twice"},
		{"duplicate control", cloneOracle(func(o *AcceptanceOracle) { o.Controls = append(o.Controls, o.Controls[0]) }), "declared twice"},
		{"owner id without prefix", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].OwnerID = "entry" }), "owner_id"},
		{"case id without prefix", cloneOracle(func(o *AcceptanceOracle) { o.Cases[0].CaseID = "immutable" }), "case_id"},
		{"control id without prefix", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].ControlID = "immutable" }), "control_id"},
		{"case names undeclared owner", cloneOracle(func(o *AcceptanceOracle) { o.Cases[0].OwnerID = "owner:missing" }), "undeclared owner"},
		{"control names undeclared owner", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].OwnerID = "owner:missing" }), "undeclared owner"},
		{"case names undeclared control", cloneOracle(func(o *AcceptanceOracle) { o.Cases[0].ControlIDs = []string{"control:missing"} }), "undeclared control"},
		{"control names undeclared case", cloneOracle(func(o *AcceptanceOracle) {
			o.Controls[0].CaseIDs = []string{"case:immutable", "case:rerecord", "case:ghost"}
		}), "undeclared case"},
		{"nonreciprocal control", cloneOracle(func(o *AcceptanceOracle) { o.Controls[1].CaseIDs = []string{"case:bind"} }), "does not name back"},
		{"nonreciprocal case", cloneOracle(func(o *AcceptanceOracle) { o.Cases[2].ControlIDs = []string{"control:bind", "control:immutable"} }), "does not name back"},
		{"owner predicate wrong shape", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].PredicateIDs = []string{"pred:not-declared"} }), "predicate grammar"},
		{"owner predicate duplicated", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].PredicateIDs = []string{"predicate:one", "predicate:one"} }), "twice"},
		{"control predicate duplicated", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].PredicateIDs = []string{"predicate:one", "predicate:one"} }), "twice"},
		{"job predicate uncovered by owner", cloneOracle(func(o *AcceptanceOracle) {
			o.Owners[0].PredicateIDs = []string{"predicate:one"}
			o.Owners[1].PredicateIDs = []string{"predicate:three"}
		}), "without an owning owner"},
		{"job predicate uncovered by control", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].PredicateIDs = []string{"predicate:two"} }), "without an exercising control"},
		{"empty obligation", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].Obligation = "" }), "obligation"},
		{"mechanism path traversal", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].Mechanism.Path = "../outside.go" }), "contained repository path"},
		{"mechanism absolute path", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].Mechanism.Path = "/etc/passwd" }), "contained repository path"},
		{"empty mechanism entry point", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].Mechanism.EntryPoint = "" }), "entry_point"},
		{"law binding repository arm", cloneOracle(func(o *AcceptanceOracle) {
			o.Owners[0].LawBindings = []OracleLawBinding{{Source: WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "law.md", CommitOID: testOracleCommit()}, Clause: "D1"}}
		}), "knowledge source"},
		{"law binding bad hash", cloneOracle(func(o *AcceptanceOracle) {
			o.Owners[0].LawBindings = []OracleLawBinding{{Source: WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "source:one", LawID: "CD-0001", ContentHash: "sha256:short"}, Clause: "D1"}}
		}), "content_hash"},
		{"law binding empty clause", cloneOracle(func(o *AcceptanceOracle) {
			o.Owners[0].LawBindings = []OracleLawBinding{{Source: WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "source:one", LawID: "CD-0001", ContentHash: "sha256:" + strings.Repeat("a", 64)}, Clause: ""}}
		}), "clause"},
		{"law binding duplicated", cloneOracle(func(o *AcceptanceOracle) {
			binding := OracleLawBinding{Source: WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "source:one", LawID: "CD-0001", ContentHash: "sha256:" + strings.Repeat("a", 64)}, Clause: "D1"}
			o.Owners[0].LawBindings = []OracleLawBinding{binding, binding}
		}), "twice"},
		{"recipe knowledge arm", cloneOracle(func(o *AcceptanceOracle) {
			o.Controls[0].RecipeSource = WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "source:one", LawID: "CD-0001", ContentHash: "sha256:" + strings.Repeat("a", 64)}
		}), "repository file source"},
		{"recipe short commit", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].RecipeSource.CommitOID = "abc123" }), "commit_oid"},
		{"argv empty", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].Argv = nil }), "arguments"},
		{"argv empty argument", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].Argv = []string{"go", ""} }), "arguments"},
		{"cwd absolute", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].Cwd = "/tmp" }), "cwd"},
		{"expected result fail", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].ExpectedResult = "fail" }), "expected_result"},
		{"evidence role trusted", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].RequiredEvidenceRole = "trusted" }), "required_evidence_role"},
		{"readiness refs empty", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].ReadinessEvidenceRefs = nil }), "readiness evidence"},
		{"readiness ref duplicated", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].ReadinessEvidenceRefs = []string{"evidence:a", "evidence:a"} }), "readiness evidence"},
		{"readiness ref oversized", cloneOracle(func(o *AcceptanceOracle) {
			o.Controls[0].ReadinessEvidenceRefs = []string{"evidence:" + strings.Repeat("x", 2100)}
		}), "readiness evidence"},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			err := validateAcceptanceOracle(refusal.oracle, jobPredicates)
			if err == nil {
				t.Fatalf("oracle mutation %q passed the closed validator", refusal.name)
			}
			if refusal.wantText != "" && !strings.Contains(err.Error(), refusal.wantText) {
				t.Fatalf("oracle mutation %q refused with %q, want text %q", refusal.name, err.Error(), refusal.wantText)
			}
		})
	}
	// An oversized oracle refuses whole: a maximal valid case inventory with
	// maximal descriptions overflows the 32 KiB bound without ever being
	// truncated. The overflow keeps the graph structurally valid — unique
	// case identities, reciprocal control mapping — so the combined bound
	// is the refusal that fires.
	oversized := testOracleGraph()
	for i := range oversized.Owners {
		oversized.Owners[i].Obligation = strings.Repeat("o", 512)
	}
	overflowControls := []string{"case:immutable", "case:rerecord"}
	for i := range oversized.Cases {
		oversized.Cases[i].InputClass = strings.Repeat("i", 512)
		oversized.Cases[i].ExpectedState = strings.Repeat("e", 512)
		oversized.Cases[i].EntryPoint = strings.Repeat("p", 512)
	}
	for len(oversized.Cases) < OracleCasesMax {
		index := len(oversized.Cases)
		caseID := "case:overflow-" + strings.Repeat("0", 3-len(itoa(index))) + itoa(index)
		oversized.Cases = append(oversized.Cases, OracleCase{
			CaseID: caseID, OwnerID: "owner:entry", EntryPoint: strings.Repeat("p", 512),
			InputClass: strings.Repeat("i", 512), ExpectedState: strings.Repeat("e", 512),
			ControlIDs: []string{"control:immutable"},
		})
		overflowControls = append(overflowControls, caseID)
	}
	oversized.Controls[0].CaseIDs = overflowControls
	for i := range oversized.Controls {
		oversized.Controls[i].ReadinessEvidenceRefs = []string{"evidence:" + strings.Repeat("r", 400), "evidence:" + strings.Repeat("s", 400)}
	}
	err := validateAcceptanceOracle(oversized, jobPredicates)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized oracle error = %v, want the combined-bound refusal", err)
	}
}

// itoa keeps the overflow case identities inside the bounded reference
// grammar without importing strconv into every reader of this file.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// argv carries an exact argument vector, so control characters refuse.
// Shell metacharacters inside one argument stay structurally legal — the
// adapter execs the vector without a shell, so no heuristic denies them —
// and the validator refuses only what it can decide.
func TestOwnerOracleGraphRefusesControlCharacterArgv(t *testing.T) {
	t.Parallel()
	oracle := testOracleGraph()
	oracle.Controls[0].Argv = []string{"go", "test\n./..."}
	if err := validateAcceptanceOracle(oracle, []string{"predicate:one", "predicate:two"}); err == nil {
		t.Fatal("argument vector with a control character passed the closed validator")
	}
}

// A newer oracle satisfies an older one only when every previously required
// control is retained byte-identically, including its pinned recipe; a
// strengthened inventory may add controls, and a dropped oracle never
// retains anything.
func TestOwnerOracleControlsRetained(t *testing.T) {
	t.Parallel()
	previous := testOracleGraph()
	if !oracleControlsRetained(nil, nil) {
		t.Fatal("no previous oracle must retain vacuously")
	}
	if !oracleControlsRetained(previous, previous) {
		t.Fatal("identical oracle did not retain its controls")
	}
	latest := testOracleGraph()
	latest.Controls = append(latest.Controls, OracleControl{
		ControlID: "control:added", OwnerID: "owner:entry",
		PredicateIDs: []string{"predicate:one"}, CaseIDs: []string{"case:immutable"},
		RecipeSource: WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
		Argv:         oracleGoArgv("added"),
		Cwd:          "internal/store", ExpectedResult: "pass", RequiredEvidenceRole: "reported",
		ReadinessEvidenceRefs: []string{"evidence:oracle-ready"},
	})
	if !oracleControlsRetained(previous, latest) {
		t.Fatal("a strengthened inventory with byte-identical old controls did not retain")
	}
	// The added case must be reciprocal for the latest graph to be valid at
	// all; the retention witness only compares old controls.
	dropped := testOracleGraph()
	dropped.Controls = dropped.Controls[1:]
	if oracleControlsRetained(previous, dropped) {
		t.Fatal("a deleted required control counted as retained")
	}
	repinned := testOracleGraph()
	repinned.Controls[0].RecipeSource.CommitOID = strings.Repeat("2d0a", 10)
	if oracleControlsRetained(previous, repinned) {
		t.Fatal("a re-pinned recipe counted as retained")
	}
	retitled := testOracleGraph()
	retitled.Controls[0].Argv = oracleGoArgv("immutability-v2")
	if oracleControlsRetained(previous, retitled) {
		t.Fatal("a changed argument vector counted as retained")
	}
	reroled := testOracleGraph()
	reroled.Controls[0].RequiredEvidenceRole = "independently_executed"
	if oracleControlsRetained(previous, reroled) {
		t.Fatal("a changed evidence role counted as retained")
	}
	// Refreshing every control's readiness references to a new candidate's
	// preparations is not a rewrite: the bundle digest excludes readiness
	// references, and the exact native-readiness gate re-proves them at
	// record time.
	refreshed := testOracleGraph()
	for i := range refreshed.Controls {
		refreshed.Controls[i].ReadinessEvidenceRefs = []string{"worktree_verify:fixture-prepare-next-candidate"}
	}
	if !oracleControlsRetained(previous, refreshed) {
		t.Fatal("a readiness-reference refresh on unchanged obligations counted as a rewrite")
	}
	if oracleControlsRetained(previous, nil) {
		t.Fatal("a dropped oracle counted as retaining its controls")
	}
}

// Oracle capability derives from the declared acceptance_oracle action
// member: the newest implementation and break-fix definitions carry it,
// every earlier version does not, and families without worker jobs never
// do.
func TestOwnerOracleActiveDerivesFromDeclaredMember(t *testing.T) {
	t.Parallel()
	capable := []WorkflowDefinition{implementationOwnerOracleV27(), breakFixOwnerOracleV24()}
	for _, definition := range capable {
		if !workflowOwnerOracleActive(definition) {
			t.Fatalf("%s version %d declares record_worker_job without the oracle member", definition.Ref, definition.Version)
		}
	}
	for _, pin := range []struct {
		ref     string
		version int64
	}{
		{"workflow.implementation", 24},
		{"workflow.implementation", 25},
		{"workflow.implementation", 26},
		{"workflow.break_fix", 21},
		{"workflow.break_fix", 22},
		{"workflow.break_fix", 23},
		{"workflow.research", 15},
	} {
		registered, ok := BuiltinWorkflowRegistry().Lookup(pin.ref, pin.version)
		if !ok {
			t.Fatalf("%s@%d is not registered", pin.ref, pin.version)
		}
		if workflowOwnerOracleActive(registered.Definition) {
			t.Fatalf("%s@%d is oracle-capable without the declared member", pin.ref, pin.version)
		}
	}
	// The declared member is the only derivation: a definition whose
	// record_worker_job payload omits the field is not oracle-capable even
	// at the newest version shape.
	stripped := cloneWorkflowDefinition(implementationOwnerOracleV27())
	for index := range stripped.ActionDefinitions {
		if stripped.ActionDefinitions[index].ID != "record_worker_job" {
			continue
		}
		fields := stripped.ActionDefinitions[index].Payload.Fields
		kept := fields[:0]
		for _, field := range fields {
			if field.Name != "acceptance_oracle" {
				kept = append(kept, field)
			}
		}
		stripped.ActionDefinitions[index].Payload.Fields = kept
	}
	if workflowOwnerOracleActive(stripped) {
		t.Fatal("capability survived stripping the declared member")
	}
}
