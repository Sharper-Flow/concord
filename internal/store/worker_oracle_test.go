package store

import (
	"encoding/json"
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

// Native preparation retains owner, case, control and combined-byte validation.
func TestOwnerOracleGraph(t *testing.T) {
	t.Parallel()
	validateNativeParts := func(oracle *AcceptanceOracle) error {
		owners := map[string]OracleOwner{}
		for _, owner := range oracle.Owners {
			if err := validateOracleOwnerEntry(owner); err != nil {
				return err
			}
			owners[owner.OwnerID] = owner
		}
		if err := validateOracleCases(oracle.Cases, owners); err != nil {
			return err
		}
		for _, control := range oracle.Controls {
			if err := validateOracleControlDefinition(control); err != nil {
				return err
			}
		}
		return validateOracleCombinedBound(oracle)
	}
	if err := validateNativeParts(testOracleGraph()); err != nil {
		t.Fatalf("valid native oracle parts refused: %v", err)
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
		{"duplicate case", cloneOracle(func(o *AcceptanceOracle) { o.Cases = append(o.Cases, o.Cases[0]) }), "declared twice"},
		{"case id without prefix", cloneOracle(func(o *AcceptanceOracle) { o.Cases[0].CaseID = "immutable" }), "case_id"},
		{"case names undeclared owner", cloneOracle(func(o *AcceptanceOracle) { o.Cases[0].OwnerID = "owner:missing" }), "undeclared owner"},
		{"owner predicate wrong shape", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].PredicateIDs = []string{"pred:not-declared"} }), "predicate grammar"},
		{"owner predicate duplicated", cloneOracle(func(o *AcceptanceOracle) { o.Owners[0].PredicateIDs = []string{"predicate:one", "predicate:one"} }), "twice"},
		{"control predicate duplicated", cloneOracle(func(o *AcceptanceOracle) { o.Controls[0].PredicateIDs = []string{"predicate:one", "predicate:one"} }), "twice"},
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
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			err := validateNativeParts(refusal.oracle)
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
	err := validateOracleCombinedBound(oversized)
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
	if err := validateOracleControlDefinition(oracle.Controls[0]); err == nil {
		t.Fatal("argument vector with a control character passed the closed validator")
	}
}

func TestNativeOracleBundleShapeRefusals(t *testing.T) {
	f := newNativeOracleFixture(t)
	raw := mustJSONValue(f.bundle)
	for _, tc := range []struct {
		name   string
		mutate func(*NativeOracleControlBundle)
	}{
		{"valid", func(b *NativeOracleControlBundle) {}},
		{"no cases", func(b *NativeOracleControlBundle) { b.Cases = nil }},
		{"owner id without prefix", func(b *NativeOracleControlBundle) { b.Owner.OwnerID = "entry"; b.Control.OwnerID = "entry" }},
		{"control id without prefix", func(b *NativeOracleControlBundle) { b.Control.ControlID = "control" }},
		{"control names undeclared owner", func(b *NativeOracleControlBundle) { b.Control.OwnerID = "owner:missing" }},
		{"case names undeclared owner", func(b *NativeOracleControlBundle) { b.Cases[0].OwnerID = "owner:missing" }},
		{"case names undeclared control", func(b *NativeOracleControlBundle) { b.Cases[0].ControlIDs = []string{"control:missing"} }},
		{"control names undeclared case", func(b *NativeOracleControlBundle) { b.Control.CaseIDs = []string{"case:missing"} }},
		{"duplicate case", func(b *NativeOracleControlBundle) {
			b.Cases = append(b.Cases, b.Cases[0])
			b.Control.CaseIDs = append(b.Control.CaseIDs, "case:another")
		}},
		{"readiness supplied before preparation", func(b *NativeOracleControlBundle) { b.Control.ReadinessEvidenceRefs = []string{"evidence:invented"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bundle NativeOracleControlBundle
			if err := json.Unmarshal(raw, &bundle); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&bundle)
			tx, err := f.s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = validateNativeBundleAuthorityTx(t.Context(), tx, f.work, 1, bundle)
			if tc.name == "valid" {
				if err != nil {
					t.Fatalf("valid native bundle refused: %v", err)
				}
			} else if !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("native shape refusal = %v, want invalid_payload", err)
			}
		})
	}
}
