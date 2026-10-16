package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

// designArchitectureMemberProject is the caller-supplied membership join the
// pure validator receives: the validator must not read the store, so
// membership arrives from this predicate exactly as a later writer supplies
// it from the work's member Projects.
func designArchitectureMemberProject(project string) bool {
	return project == "concord" || project == "other-repo"
}

// designArchitectureFixture returns one closed, valid plan: every required
// section populated, every join resolvable inside the object, and the
// section 4a witness capacity allocation.
func designArchitectureFixture() WorkflowDesignArchitecture {
	return WorkflowDesignArchitecture{
		Owners: []WorkflowDesignArchitectureOwner{{
			OwnerID:   "store-arch-owner",
			ProjectID: "concord",
			Path:      "internal/store/workflow_architecture.go",
			Symbol:    "validateWorkflowDesignArchitecture",
		}},
		Invariants: []WorkflowDesignArchitectureInvariant{{
			InvariantID: "closed-plan",
			OwnerID:     "store-arch-owner",
			Statement:   "the plan carries closed types and resolvable joins only",
		}},
		DataFlow: []WorkflowDesignArchitectureDataFlow{{
			FlowID:       "validate-then-bind",
			FromOwnerID:  "store-arch-owner",
			ToOwnerID:    "store-arch-owner",
			Value:        "a validated plan reaches the design writer",
			InvariantIDs: []string{"closed-plan"},
		}},
		FailureBehavior: []WorkflowDesignArchitectureFailureBehavior{{
			FailureID: "over-bound-refusal",
			OwnerID:   "store-arch-owner",
			Condition: "the canonical plan exceeds its byte bound",
			Result:    "the plan refuses whole and is never truncated",
			Preserves: []string{"closed-plan"},
		}},
		References: []WorkflowDesignArchitectureReference{{
			ReferenceID: "design-reference",
			Source: WorkContextReadingSource{
				Kind:      WorkContextSourceRepositoryFile,
				ProjectID: "concord",
				Path:      "internal/store/workflow_design.go",
				CommitOID: strings.Repeat("a", 40),
			},
			StartLine: 9,
			EndLine:   50,
		}},
		Files: []WorkflowDesignArchitectureFile{
			{ProjectID: "concord", Path: "internal/store/workflow_architecture.go", Role: DesignArchitectureFileRoleSource},
			{ProjectID: "concord", Path: "internal/store/workflow_architecture_test.go", Role: DesignArchitectureFileRoleTest},
		},
		UnchangedFiles: []WorkflowDesignArchitectureUnchangedFile{
			{ProjectID: "concord", Path: "internal/store/workflow_design.go"},
		},
		Slices: []WorkflowDesignArchitectureSlice{{
			SliceID:         "value-slice",
			Kind:            DesignArchitectureSliceKindEdit,
			Ordinal:         0,
			OwnerID:         "store-arch-owner",
			Files:           []string{"internal/store/workflow_architecture.go"},
			InvariantIDs:    []string{"closed-plan"},
			ReferenceIDs:    []string{"design-reference"},
			Tests:           []string{"control:design-architecture-value"},
			EndState:        "the value tests pass",
			MaxChangedLines: 400,
		}},
		Capacity: &WorkflowDesignArchitectureCapacity{
			DesignRecord: 10240, WorkerJob: 10240, WorkContext: 4096, LawContext: 1024,
			ProposalRecord: 512, WorkRecord: 512, TaskPremise: 4096, OutcomePredicates: 1024,
		},
	}
}

func validateDesignArchitectureVariant(t *testing.T, change func(plan *WorkflowDesignArchitecture)) error {
	t.Helper()
	plan := designArchitectureFixture()
	if change != nil {
		change(&plan)
	}
	return validateWorkflowDesignArchitecture(plan, designArchitectureMemberProject)
}

func assertDesignArchitectureRefusal(t *testing.T, err error, want FailureKind) {
	t.Helper()
	if err == nil {
		t.Fatal("validateWorkflowDesignArchitecture accepted a plan it must refuse")
	}
	assertFailureKind(t, err, want)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want *Failure", err)
	}
	if failure.Op != "workflow_design_architecture" {
		t.Fatalf("Failure.Op = %q, want workflow_design_architecture", failure.Op)
	}
}

func TestArchitectureValueAcceptsClosedPlan(t *testing.T) {
	if err := validateWorkflowDesignArchitecture(designArchitectureFixture(), designArchitectureMemberProject); err != nil {
		t.Fatalf("closed plan refused: %v", err)
	}
	raw, err := json.Marshal(designArchitectureFixture())
	if err != nil {
		t.Fatalf("cannot encode the fixture: %v", err)
	}
	decoded, err := decodeWorkflowDesignArchitecture(raw)
	if err != nil {
		t.Fatalf("closed plan refused at decode: %v", err)
	}
	if err := validateWorkflowDesignArchitecture(decoded, designArchitectureMemberProject); err != nil {
		t.Fatalf("decoded plan refused: %v", err)
	}
	// A same-owner data flow is valid, and an integration slice may carry no
	// files for a wholly automatic merge.
	err = validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
		plan.Slices = append(plan.Slices, WorkflowDesignArchitectureSlice{
			SliceID:         "integration-slice",
			Kind:            DesignArchitectureSliceKindIntegration,
			Ordinal:         1,
			OwnerID:         "store-arch-owner",
			Tests:           []string{"control:integration-anchor"},
			EndState:        "the merge anchor is recorded",
			MaxChangedLines: 500,
		})
	})
	if err != nil {
		t.Fatalf("integration slice without files refused: %v", err)
	}
	for _, limit := range []int{1, 500} {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].MaxChangedLines = limit
		})
		if err != nil {
			t.Fatalf("max_changed_lines %d refused: %v", limit, err)
		}
	}
}

func TestArchitectureValueStrictDecode(t *testing.T) {
	raw, err := json.Marshal(designArchitectureFixture())
	if err != nil {
		t.Fatalf("cannot encode the fixture: %v", err)
	}
	cases := []struct{ name, body string }{
		{"unknown member", `{"owners":[],"capacity":null,"unknown_member":true}`},
		{"duplicate key", `{"owners":[],"owners":[]}`},
		{"trailing data", string(raw) + `{"owners":[]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := decodeWorkflowDesignArchitecture([]byte(testCase.body))
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
	// The eight-slot capacity allocation is a required closed member: a plan
	// without one decodes but refuses validation.
	var absent WorkflowDesignArchitecture
	if err := json.Unmarshal(raw, &absent); err != nil {
		t.Fatalf("cannot decode the fixture: %v", err)
	}
	absent.Capacity = nil
	err = validateWorkflowDesignArchitecture(absent, designArchitectureMemberProject)
	assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
}

func TestArchitectureValueSectionBounds(t *testing.T) {
	minimalOwner := func(i int) WorkflowDesignArchitectureOwner {
		return WorkflowDesignArchitectureOwner{OwnerID: fmt.Sprintf("owner-%02d", i), ProjectID: "concord", Path: "a/b.go", Symbol: "ss"}
	}
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"no owners", func(plan *WorkflowDesignArchitecture) { plan.Owners = nil }},
		{"17 owners", func(plan *WorkflowDesignArchitecture) {
			plan.Owners = nil
			for i := 0; i <= designArchitectureOwnersMax; i++ {
				plan.Owners = append(plan.Owners, minimalOwner(i))
			}
		}},
		{"no invariants", func(plan *WorkflowDesignArchitecture) { plan.Invariants = nil }},
		{"33 invariants", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants = nil
			for i := 0; i <= designArchitectureInvariantsMax; i++ {
				plan.Invariants = append(plan.Invariants, WorkflowDesignArchitectureInvariant{InvariantID: fmt.Sprintf("inv-%02d", i), OwnerID: "store-arch-owner", Statement: "ss"})
			}
			stripDesignArchitectureInvariantJoins(plan)
		}},
		{"no data flows", func(plan *WorkflowDesignArchitecture) { plan.DataFlow = nil }},
		{"33 data flows", func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow = nil
			for i := 0; i <= designArchitectureDataFlowsMax; i++ {
				plan.DataFlow = append(plan.DataFlow, WorkflowDesignArchitectureDataFlow{FlowID: fmt.Sprintf("flow-%02d", i), FromOwnerID: "store-arch-owner", ToOwnerID: "store-arch-owner", Value: "vv"})
			}
		}},
		{"no failure behavior", func(plan *WorkflowDesignArchitecture) { plan.FailureBehavior = nil }},
		{"33 failure behaviors", func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior = nil
			for i := 0; i <= designArchitectureFailuresMax; i++ {
				plan.FailureBehavior = append(plan.FailureBehavior, WorkflowDesignArchitectureFailureBehavior{FailureID: fmt.Sprintf("failure-%02d", i), OwnerID: "store-arch-owner", Condition: "cc", Result: "rr"})
			}
		}},
		{"no references", func(plan *WorkflowDesignArchitecture) { plan.References = nil }},
		{"65 references", func(plan *WorkflowDesignArchitecture) {
			source := plan.References[0].Source
			plan.References = nil
			for i := 0; i <= designArchitectureReferencesMax; i++ {
				plan.References = append(plan.References, WorkflowDesignArchitectureReference{ReferenceID: fmt.Sprintf("ref-%02d", i), Source: source, StartLine: 1, EndLine: 1})
			}
			plan.Slices[0].ReferenceIDs = nil
		}},
		{"129 files", func(plan *WorkflowDesignArchitecture) {
			plan.Files = nil
			for i := 0; i <= designArchitectureFilesMax; i++ {
				plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: fmt.Sprintf("f/f%03d.go", i), Role: DesignArchitectureFileRoleSource})
			}
			plan.Slices[0].Files = nil
			plan.Slices[0].Kind = DesignArchitectureSliceKindIntegration
		}},
		{"129 unchanged files", func(plan *WorkflowDesignArchitecture) {
			plan.UnchangedFiles = nil
			for i := 0; i <= designArchitectureUnchangedMax; i++ {
				plan.UnchangedFiles = append(plan.UnchangedFiles, WorkflowDesignArchitectureUnchangedFile{ProjectID: "concord", Path: fmt.Sprintf("u/u%03d.go", i)})
			}
		}},
		{"no slices", func(plan *WorkflowDesignArchitecture) { plan.Slices = nil }},
		{"33 slices", func(plan *WorkflowDesignArchitecture) {
			plan.Slices = nil
			for i := 0; i <= designArchitectureSlicesMax; i++ {
				plan.Slices = append(plan.Slices, WorkflowDesignArchitectureSlice{
					SliceID: fmt.Sprintf("slice-%02d", i), Kind: DesignArchitectureSliceKindEdit, Ordinal: i,
					OwnerID: "store-arch-owner", Files: []string{"internal/store/workflow_architecture.go"},
					Tests: []string{"control:design-architecture-value"}, EndState: "ss", MaxChangedLines: 1,
				})
			}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

// stripDesignArchitectureInvariantJoins drops every invariant reference so a
// replaced invariant set cannot leave a dangling join behind in a variant.
func stripDesignArchitectureInvariantJoins(plan *WorkflowDesignArchitecture) {
	for i := range plan.DataFlow {
		plan.DataFlow[i].InvariantIDs = nil
	}
	for i := range plan.FailureBehavior {
		plan.FailureBehavior[i].Preserves = nil
	}
	for i := range plan.Slices {
		plan.Slices[i].InvariantIDs = nil
	}
}

func TestArchitectureValueSectionMaximumsPass(t *testing.T) {
	t.Run("16 owners", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.Owners = nil
			for i := 0; i < designArchitectureOwnersMax; i++ {
				plan.Owners = append(plan.Owners, WorkflowDesignArchitectureOwner{OwnerID: fmt.Sprintf("owner-%02d", i), ProjectID: "concord", Path: "a/b.go", Symbol: "ss"})
			}
			first := plan.Owners[0].OwnerID
			plan.Invariants[0].OwnerID = first
			plan.DataFlow[0].FromOwnerID = first
			plan.DataFlow[0].ToOwnerID = first
			plan.FailureBehavior[0].OwnerID = first
			plan.Slices[0].OwnerID = first
		})
		if err != nil {
			t.Fatalf("16 owners refused: %v", err)
		}
	})
	t.Run("32 invariants", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.Invariants = nil
			for i := 0; i < designArchitectureInvariantsMax; i++ {
				plan.Invariants = append(plan.Invariants, WorkflowDesignArchitectureInvariant{InvariantID: fmt.Sprintf("inv-%02d", i), OwnerID: "store-arch-owner", Statement: "ss"})
			}
			stripDesignArchitectureInvariantJoins(plan)
		})
		if err != nil {
			t.Fatalf("32 invariants refused: %v", err)
		}
	})
	t.Run("32 data flows", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow = nil
			for i := 0; i < designArchitectureDataFlowsMax; i++ {
				plan.DataFlow = append(plan.DataFlow, WorkflowDesignArchitectureDataFlow{FlowID: fmt.Sprintf("flow-%02d", i), FromOwnerID: "store-arch-owner", ToOwnerID: "store-arch-owner", Value: "vv"})
			}
		})
		if err != nil {
			t.Fatalf("32 data flows refused: %v", err)
		}
	})
	t.Run("32 failure behaviors", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior = nil
			for i := 0; i < designArchitectureFailuresMax; i++ {
				plan.FailureBehavior = append(plan.FailureBehavior, WorkflowDesignArchitectureFailureBehavior{FailureID: fmt.Sprintf("failure-%02d", i), OwnerID: "store-arch-owner", Condition: "cc", Result: "rr"})
			}
		})
		if err != nil {
			t.Fatalf("32 failure behaviors refused: %v", err)
		}
	})
	t.Run("128 unchanged files", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.UnchangedFiles = nil
			for i := 0; i < designArchitectureUnchangedMax; i++ {
				plan.UnchangedFiles = append(plan.UnchangedFiles, WorkflowDesignArchitectureUnchangedFile{ProjectID: "concord", Path: fmt.Sprintf("u/u%03d.go", i)})
			}
		})
		if err != nil {
			t.Fatalf("128 unchanged files refused: %v", err)
		}
	})
	t.Run("32 slices", func(t *testing.T) {
		// Built on a minimal base plan: the 8,192-byte canonical bound
		// constrains sections jointly, so a 32-slice maximum does not fit
		// beside the full fixture (the bound refuses that plan, as §4a
		// intends).
		plan := WorkflowDesignArchitecture{
			Owners:          []WorkflowDesignArchitectureOwner{{OwnerID: "o0", ProjectID: "concord", Path: "a/b.go", Symbol: "ss"}},
			Invariants:      []WorkflowDesignArchitectureInvariant{{InvariantID: "i0", OwnerID: "o0", Statement: "ss"}},
			DataFlow:        []WorkflowDesignArchitectureDataFlow{{FlowID: "f0", FromOwnerID: "o0", ToOwnerID: "o0", Value: "vv"}},
			FailureBehavior: []WorkflowDesignArchitectureFailureBehavior{{FailureID: "x0", OwnerID: "o0", Condition: "cc", Result: "rr"}},
			References: []WorkflowDesignArchitectureReference{{
				ReferenceID: "r0",
				Source:      WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "concord", Path: "a/b.go", CommitOID: strings.Repeat("a", 40)},
				StartLine:   1, EndLine: 1,
			}},
			Files: []WorkflowDesignArchitectureFile{{ProjectID: "concord", Path: "f.go", Role: DesignArchitectureFileRoleSource}},
			Capacity: &WorkflowDesignArchitectureCapacity{
				DesignRecord: 10240, WorkerJob: 10240, WorkContext: 4096, LawContext: 1024,
				ProposalRecord: 512, WorkRecord: 512, TaskPremise: 4096, OutcomePredicates: 1024,
			},
		}
		for i := 0; i < designArchitectureSlicesMax; i++ {
			plan.Slices = append(plan.Slices, WorkflowDesignArchitectureSlice{
				SliceID: fmt.Sprintf("slice-%02d", i), Kind: DesignArchitectureSliceKindEdit, Ordinal: i,
				OwnerID: "o0", Files: []string{"f.go"},
				Tests: []string{"control:x"}, EndState: "ss", MaxChangedLines: 1,
			})
		}
		if err := validateWorkflowDesignArchitecture(plan, designArchitectureMemberProject); err != nil {
			t.Fatalf("32 slices refused: %v", err)
		}
	})
}

func TestArchitectureValueIdentityJoins(t *testing.T) {
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"duplicate owner identity", func(plan *WorkflowDesignArchitecture) {
			plan.Owners = append(plan.Owners, plan.Owners[0])
		}},
		{"duplicate invariant identity", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants = append(plan.Invariants, plan.Invariants[0])
		}},
		{"invariant names an undeclared owner", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants[0].OwnerID = "ghost-owner"
		}},
		{"duplicate flow identity", func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow = append(plan.DataFlow, plan.DataFlow[0])
		}},
		{"flow names an undeclared from-owner", func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow[0].FromOwnerID = "ghost-owner"
		}},
		{"flow names an undeclared to-owner", func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow[0].ToOwnerID = "ghost-owner"
		}},
		{"flow names an undeclared invariant", func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow[0].InvariantIDs = []string{"ghost-invariant"}
		}},
		{"duplicate failure identity", func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior = append(plan.FailureBehavior, plan.FailureBehavior[0])
		}},
		{"failure names an undeclared owner", func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior[0].OwnerID = "ghost-owner"
		}},
		{"failure preserves an undeclared invariant", func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior[0].Preserves = []string{"ghost-invariant"}
		}},
		{"duplicate reference identity", func(plan *WorkflowDesignArchitecture) {
			plan.References = append(plan.References, plan.References[0])
		}},
		{"duplicate slice identity", func(plan *WorkflowDesignArchitecture) {
			second := plan.Slices[0]
			second.SliceID = plan.Slices[0].SliceID
			second.Ordinal = 1
			plan.Slices = append(plan.Slices, second)
		}},
		{"slice names an undeclared owner", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].OwnerID = "ghost-owner"
		}},
		{"slice names an undeclared invariant", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].InvariantIDs = []string{"ghost-invariant"}
		}},
		{"slice names an undeclared reference", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].ReferenceIDs = []string{"ghost-reference"}
		}},
		{"duplicate flow invariant reference", func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow[0].InvariantIDs = []string{"closed-plan", "closed-plan"}
		}},
		{"duplicate failure preserves reference", func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior[0].Preserves = []string{"closed-plan", "closed-plan"}
		}},
		{"duplicate slice invariant reference", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].InvariantIDs = []string{"closed-plan", "closed-plan"}
		}},
		{"duplicate slice reference reference", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].ReferenceIDs = []string{"design-reference", "design-reference"}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

func TestArchitectureValueExactPaths(t *testing.T) {
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"absolute owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "/etc/passwd" }},
		{"parent traversal owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "internal/../store/x.go" }},
		{"self segment owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "./x.go" }},
		{"trailing separator owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "internal/" }},
		{"empty owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "" }},
		{"control byte owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "internal\x00/x.go" }},
		{"oversize owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = strings.Repeat("a", 513) }},
		{"tmp owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "tmp/scratch.go" }},
		{"duplicate file entry", func(plan *WorkflowDesignArchitecture) {
			plan.Files = append(plan.Files, plan.Files[0])
		}},
		{"duplicate unchanged entry", func(plan *WorkflowDesignArchitecture) {
			plan.UnchangedFiles = append(plan.UnchangedFiles, plan.UnchangedFiles[0])
		}},
		{"slice file outside the owner's project", func(plan *WorkflowDesignArchitecture) {
			plan.Owners = append(plan.Owners, WorkflowDesignArchitectureOwner{OwnerID: "other-owner", ProjectID: "other-repo", Path: "docs/other.md", Symbol: "ss"})
			plan.Slices = append(plan.Slices, WorkflowDesignArchitectureSlice{
				SliceID: "other-slice", Kind: DesignArchitectureSliceKindEdit, Ordinal: 1,
				OwnerID: "other-owner", Files: []string{"internal/store/workflow_architecture.go"},
				Tests: []string{"control:other-value"}, EndState: "ss", MaxChangedLines: 1,
			})
		}},
		{"slice file absent from the file map", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].Files = []string{"internal/store/undeclared.go"}
		}},
		{"absolute slice file", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].Files = []string{"/abs/x.go"} }},
		{"traversal slice file", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].Files = []string{"../x.go"} }},
		{"directory slice file", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].Files = []string{"internal/store/"} }},
		{"duplicate slice file", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].Files = []string{"internal/store/workflow_architecture.go", "internal/store/workflow_architecture.go"}
		}},
		{"glob owner path", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Path = "internal/store/*.go" }},
		{"glob file map path", func(plan *WorkflowDesignArchitecture) {
			plan.Files[0].Path = "internal/store/f[0].go"
			plan.Slices[0].Files = []string{"internal/store/f[0].go"}
		}},
		{"glob unchanged path", func(plan *WorkflowDesignArchitecture) { plan.UnchangedFiles[0].Path = "internal/store/u?.go" }},
		{"glob slice file", func(plan *WorkflowDesignArchitecture) {
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: "internal/store/a*b.go", Role: DesignArchitectureFileRoleSource})
			plan.Slices[0].Files = []string{"internal/store/a*b.go"}
		}},
		{"glob reference source path", func(plan *WorkflowDesignArchitecture) { plan.References[0].Source.Path = "internal/store/ref*.go" }},
		{"glob generator source path", func(plan *WorkflowDesignArchitecture) {
			generator := WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "concord", Path: "scripts/gen*.py", CommitOID: strings.Repeat("a", 40)}
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: "internal/store/generated_x.go", Role: DesignArchitectureFileRoleGenerated, GeneratedBy: &generator})
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

func TestArchitectureValueReferenceAndFileArms(t *testing.T) {
	pinnedGenerator := WorkContextReadingSource{
		Kind: WorkContextSourceRepositoryFile, ProjectID: "concord",
		Path: "scripts/generate-agent-lanes.py", CommitOID: strings.Repeat("a", 40),
	}
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"reference source knowledge kind", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].Source.Kind = WorkContextSourceKnowledge
		}},
		{"reference source carries knowledge fields", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].Source.SourceID = "source"
			plan.References[0].Source.LawID = "law"
			plan.References[0].Source.ContentHash = "sha256:" + strings.Repeat("b", 64)
		}},
		{"reference commit too short", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].Source.CommitOID = strings.Repeat("a", 39)
		}},
		{"reference commit not hex", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].Source.CommitOID = strings.Repeat("g", 40)
		}},
		{"reference start line below one", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].StartLine = 0
		}},
		{"reference end line before start", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].EndLine = 8
		}},
		{"unknown file role", func(plan *WorkflowDesignArchitecture) {
			plan.Files[0].Role = "binary"
		}},
		{"generated file without a generator source", func(plan *WorkflowDesignArchitecture) {
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: "internal/store/generated_x.go", Role: DesignArchitectureFileRoleGenerated})
		}},
		{"source file with a generator source", func(plan *WorkflowDesignArchitecture) {
			plan.Files[0].GeneratedBy = &pinnedGenerator
		}},
		{"test file with a generator source", func(plan *WorkflowDesignArchitecture) {
			plan.Files[1].GeneratedBy = &pinnedGenerator
		}},
		{"generator source knowledge arm", func(plan *WorkflowDesignArchitecture) {
			knowledge := WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "source", LawID: "law", ContentHash: "sha256:" + strings.Repeat("b", 64)}
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: "internal/store/generated_x.go", Role: DesignArchitectureFileRoleGenerated, GeneratedBy: &knowledge})
		}},
		{"generator project outside membership", func(plan *WorkflowDesignArchitecture) {
			foreign := pinnedGenerator
			foreign.ProjectID = "elsewhere"
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: "internal/store/generated_x.go", Role: DesignArchitectureFileRoleGenerated, GeneratedBy: &foreign})
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
	t.Run("generated file with a pinned generator passes", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			generator := pinnedGenerator
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: "internal/store/generated_x.go", Role: DesignArchitectureFileRoleGenerated, GeneratedBy: &generator})
		})
		if err != nil {
			t.Fatalf("generated file with pinned generator refused: %v", err)
		}
	})
}

func TestArchitectureValueSliceShape(t *testing.T) {
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"unknown slice kind", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].Kind = "review" }},
		{"edit slice without files", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].Files = nil }},
		{"65 slice files", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].Files = nil
			for i := 0; i <= designArchitectureSliceFilesMax; i++ {
				plan.Slices[0].Files = append(plan.Slices[0].Files, fmt.Sprintf("f/f%02d.go", i))
			}
		}},
		{"no tests", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].Tests = nil }},
		{"33 tests", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].Tests = nil
			for i := 0; i <= designArchitectureSliceTestsMax; i++ {
				plan.Slices[0].Tests = append(plan.Slices[0].Tests, fmt.Sprintf("control:t%02d", i))
			}
		}},
		{"test identity outside the control grammar", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].Tests = []string{"go test ./internal/store"}
		}},
		{"duplicate tests", func(plan *WorkflowDesignArchitecture) {
			plan.Slices[0].Tests = []string{"control:same", "control:same"}
		}},
		{"ordinal ahead of position", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].Ordinal = 1 }},
		{"zero max changed lines", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].MaxChangedLines = 0 }},
		{"max changed lines over the bound", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].MaxChangedLines = 501 }},
		{"empty end state", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].EndState = "" }},
		{"oversize end state", func(plan *WorkflowDesignArchitecture) { plan.Slices[0].EndState = strings.Repeat("a", 1025) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

func TestArchitectureValueByteBounds(t *testing.T) {
	passes := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"statement at the byte bound passes", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants[0].Statement = strings.Repeat("a", 1024)
		}},
		{"owner symbol at the byte bound passes", func(plan *WorkflowDesignArchitecture) {
			plan.Owners[0].Symbol = strings.Repeat("a", 512)
		}},
	}
	for _, testCase := range passes {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validateDesignArchitectureVariant(t, testCase.change); err != nil {
				t.Fatalf("plan at a declared bound refused: %v", err)
			}
		})
	}
	refusals := []struct {
		name   string
		want   FailureKind
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"statement over the byte bound", KindInvalidPayload, func(plan *WorkflowDesignArchitecture) {
			plan.Invariants[0].Statement = strings.Repeat("a", 1025)
		}},
		{"flow value over the byte bound", KindInvalidPayload, func(plan *WorkflowDesignArchitecture) {
			plan.DataFlow[0].Value = strings.Repeat("a", 1025)
		}},
		{"failure condition over the byte bound", KindInvalidPayload, func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior[0].Condition = strings.Repeat("a", 1025)
		}},
		{"failure result over the byte bound", KindInvalidPayload, func(plan *WorkflowDesignArchitecture) {
			plan.FailureBehavior[0].Result = strings.Repeat("a", 1025)
		}},
		{"owner symbol over the byte bound", KindInvalidPayload, func(plan *WorkflowDesignArchitecture) {
			plan.Owners[0].Symbol = strings.Repeat("a", 513)
		}},
		{"negative capacity slot", KindInvalidPayload, func(plan *WorkflowDesignArchitecture) {
			plan.Capacity.WorkContext = -1
		}},
		{"capacity one byte over the allocation", KindLimitExceeded, func(plan *WorkflowDesignArchitecture) {
			plan.Capacity.DesignRecord = 10241
		}},
		{"max int capacity slot", KindLimitExceeded, func(plan *WorkflowDesignArchitecture) {
			plan.Capacity.DesignRecord = math.MaxInt
		}},
		{"two large capacity slots overflow the sum", KindLimitExceeded, func(plan *WorkflowDesignArchitecture) {
			plan.Capacity.DesignRecord = math.MaxInt/2 + 1
			plan.Capacity.WorkerJob = math.MaxInt/2 + 1
		}},
	}
	for _, testCase := range refusals {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, testCase.want)
		})
	}
	t.Run("canonical plan at exactly 8192 bytes passes", func(t *testing.T) {
		plan := designArchitectureAtCanonicalBound(t)
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("cannot encode the padded plan: %v", err)
		}
		if len(raw) != designArchitectureCanonicalMaxBytes {
			t.Fatalf("padded plan is %d bytes, want exactly %d", len(raw), designArchitectureCanonicalMaxBytes)
		}
		if err := validateWorkflowDesignArchitecture(plan, designArchitectureMemberProject); err != nil {
			t.Fatalf("plan at the canonical bound refused: %v", err)
		}
	})
	t.Run("canonical plan one byte over refuses", func(t *testing.T) {
		plan := designArchitectureAtCanonicalBound(t)
		plan.UnchangedFiles[len(plan.UnchangedFiles)-1].Path += "a"
		err := validateWorkflowDesignArchitecture(plan, designArchitectureMemberProject)
		assertDesignArchitectureRefusal(t, err, KindLimitExceeded)
	})
}

// designArchitectureAtCanonicalBound pads unchanged-file paths until the
// canonical serialized plan measures exactly 8,192 bytes.
func designArchitectureAtCanonicalBound(t *testing.T) WorkflowDesignArchitecture {
	t.Helper()
	plan := designArchitectureFixture()
	for i := 0; ; i++ {
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("cannot encode the plan: %v", err)
		}
		gap := designArchitectureCanonicalMaxBytes - len(raw)
		if gap <= 540 {
			// One appended array entry costs its own bytes plus one comma,
			// so the pad must leave that comma byte room.
			plan.UnchangedFiles = append(plan.UnchangedFiles, WorkflowDesignArchitectureUnchangedFile{ProjectID: "concord", Path: "p"})
			last := &plan.UnchangedFiles[len(plan.UnchangedFiles)-1]
			probe, _ := json.Marshal(*last)
			overhead := len(probe) + 1
			pad := gap - overhead
			if pad < 1 {
				t.Fatalf("padding arithmetic left no room: gap %d overhead %d", gap, overhead)
			}
			last.Path += strings.Repeat("a", pad)
			return plan
		}
		plan.UnchangedFiles = append(plan.UnchangedFiles, WorkflowDesignArchitectureUnchangedFile{
			ProjectID: "concord",
			Path:      "pad/" + strings.Repeat("p", 280) + fmt.Sprintf("%03d", i),
		})
	}
}

func TestArchitectureValueMemberProjectJoin(t *testing.T) {
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"owner outside membership", func(plan *WorkflowDesignArchitecture) {
			plan.Owners[0].ProjectID = "elsewhere"
		}},
		{"file outside membership", func(plan *WorkflowDesignArchitecture) {
			plan.Files[0].ProjectID = "elsewhere"
		}},
		{"reference outside membership", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].Source.ProjectID = "elsewhere"
		}},
		{"unchanged file outside membership", func(plan *WorkflowDesignArchitecture) {
			plan.UnchangedFiles[0].ProjectID = "elsewhere"
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
	t.Run("second member project passes", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.Owners = append(plan.Owners, WorkflowDesignArchitectureOwner{OwnerID: "other-owner", ProjectID: "other-repo", Path: "docs/other.md", Symbol: "ss"})
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "other-repo", Path: "docs/other.md", Role: DesignArchitectureFileRoleSource})
		})
		if err != nil {
			t.Fatalf("second member project refused: %v", err)
		}
	})
}

func TestArchitectureValueUnchangedDisjoint(t *testing.T) {
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"edit slice overlaps an unchanged file", func(plan *WorkflowDesignArchitecture) {
			plan.UnchangedFiles = append(plan.UnchangedFiles, WorkflowDesignArchitectureUnchangedFile{ProjectID: "concord", Path: "internal/store/workflow_architecture.go"})
		}},
		{"integration slice overlaps an unchanged file", func(plan *WorkflowDesignArchitecture) {
			plan.UnchangedFiles[0] = WorkflowDesignArchitectureUnchangedFile{ProjectID: "concord", Path: "internal/store/workflow_architecture.go"}
			plan.Slices = append(plan.Slices, WorkflowDesignArchitectureSlice{
				SliceID: "integration-slice", Kind: DesignArchitectureSliceKindIntegration, Ordinal: 1,
				OwnerID: "store-arch-owner", Files: []string{"internal/store/workflow_architecture.go"},
				Tests: []string{"control:integration-anchor"}, EndState: "ss", MaxChangedLines: 500,
			})
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
	t.Run("same relative path in another member project is not overlap", func(t *testing.T) {
		// The fixture's unchanged entry is {concord, workflow_design.go}; the
		// same relative path under other-repo stays writable there.
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.Owners = append(plan.Owners, WorkflowDesignArchitectureOwner{OwnerID: "other-owner", ProjectID: "other-repo", Path: "docs/other.md", Symbol: "ss"})
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "other-repo", Path: "internal/store/workflow_design.go", Role: DesignArchitectureFileRoleSource})
			plan.Slices = append(plan.Slices, WorkflowDesignArchitectureSlice{
				SliceID: "other-slice", Kind: DesignArchitectureSliceKindEdit, Ordinal: 1,
				OwnerID: "other-owner", Files: []string{"internal/store/workflow_design.go"},
				Tests: []string{"control:other-value"}, EndState: "ss", MaxChangedLines: 1,
			})
		})
		if err != nil {
			t.Fatalf("same relative path under another member Project refused: %v", err)
		}
	})
}

func TestArchitectureValueProseData(t *testing.T) {
	passes := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"one byte symbol", func(plan *WorkflowDesignArchitecture) { plan.Owners[0].Symbol = "x" }},
		{"one byte statement", func(plan *WorkflowDesignArchitecture) { plan.Invariants[0].Statement = "x" }},
		{"quotes control bytes and HTML stay data", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants[0].Statement = "quote \" backslash \\ newline \n tab \t bell \a html <b>&amp;</b>"
		}},
		{"multibyte statement at the byte bound", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants[0].Statement = strings.Repeat("é", 512)
		}},
	}
	for _, testCase := range passes {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validateDesignArchitectureVariant(t, testCase.change); err != nil {
				t.Fatalf("bounded prose refused: %v", err)
			}
		})
	}
	refusals := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"multibyte statement over the byte bound", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants[0].Statement = strings.Repeat("é", 513)
		}},
		{"invalid utf-8 statement", func(plan *WorkflowDesignArchitecture) {
			plan.Invariants[0].Statement = "ok \xff"
		}},
	}
	for _, testCase := range refusals {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

// TestArchitectureValueUTF8PathPropagation proves the S03a UTF-8 guards hold
// through the composed whole-value validation: invalid path bytes refuse at
// every entry arm while valid multibyte path bytes stay exact accepted data.
func TestArchitectureValueUTF8PathPropagation(t *testing.T) {
	t.Run("valid multibyte owner path passes whole value", func(t *testing.T) {
		err := validateDesignArchitectureVariant(t, func(plan *WorkflowDesignArchitecture) {
			plan.Owners[0].Path = "internal/日本語/workflow_architecture.go"
		})
		if err != nil {
			t.Fatalf("valid multibyte owner path refused: %v", err)
		}
	})
	cases := []struct {
		name   string
		change func(plan *WorkflowDesignArchitecture)
	}{
		{"invalid utf-8 owner path", func(plan *WorkflowDesignArchitecture) {
			plan.Owners[0].Path = "internal\xff/workflow_architecture.go"
		}},
		{"invalid utf-8 file map path", func(plan *WorkflowDesignArchitecture) {
			plan.Files[0].Path = "internal\xff/workflow_architecture.go"
		}},
		{"invalid utf-8 unchanged path", func(plan *WorkflowDesignArchitecture) {
			plan.UnchangedFiles[0].Path = "internal\xff/workflow_design.go"
		}},
		{"invalid utf-8 reference source path", func(plan *WorkflowDesignArchitecture) {
			plan.References[0].Source.Path = "internal\xff/workflow_design.go"
		}},
		{"invalid utf-8 generator source path", func(plan *WorkflowDesignArchitecture) {
			generator := WorkContextReadingSource{
				Kind: WorkContextSourceRepositoryFile, ProjectID: "concord",
				Path: "scripts/gen\xff.py", CommitOID: strings.Repeat("a", 40),
			}
			plan.Files = append(plan.Files, WorkflowDesignArchitectureFile{ProjectID: "concord", Path: "internal/store/generated_x.go", Role: DesignArchitectureFileRoleGenerated, GeneratedBy: &generator})
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateDesignArchitectureVariant(t, testCase.change)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

// designArchitectureRawFixtureWithOwnerPath builds the fixture's canonical
// JSON with the owner path member replaced, so raw byte-level decoding is
// exercised against a known member without re-encoding the changed value.
func designArchitectureRawFixtureWithOwnerPath(t *testing.T, ownerPath string) []byte {
	t.Helper()
	raw, err := json.Marshal(designArchitectureFixture())
	if err != nil {
		t.Fatalf("cannot encode the fixture: %v", err)
	}
	quoted := `"` + ownerPath + `"`
	return []byte(strings.Replace(string(raw), `"internal/store/workflow_architecture.go"`, quoted, 1))
}

func TestArchitectureValueStructureStrictDecode(t *testing.T) {
	raw, err := json.Marshal(designArchitectureFixture())
	if err != nil {
		t.Fatalf("cannot encode the fixture: %v", err)
	}
	cases := []struct{ name, body string }{
		{"unknown member", `{"owners":[],"capacity":null,"unknown_member":true}`},
		{"duplicate key", `{"owners":[],"owners":[]}`},
		{"trailing data", string(raw) + `{"owners":[]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := decodeWorkflowDesignArchitecture([]byte(testCase.body))
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
	t.Run("raw invalid utf-8 bytes refuse before json replaces them", func(t *testing.T) {
		raw := designArchitectureRawFixtureWithOwnerPath(t, "internal\xff/store/workflow_architecture.go")
		_, err := decodeWorkflowDesignArchitecture(raw)
		assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
	})
	t.Run("valid multibyte path bytes decode exactly", func(t *testing.T) {
		raw := designArchitectureRawFixtureWithOwnerPath(t, "internal/日本語/store/workflow_architecture.go")
		decoded, err := decodeWorkflowDesignArchitecture(raw)
		if err != nil {
			t.Fatalf("valid multibyte path refused at decode: %v", err)
		}
		if got := decoded.Owners[0].Path; got != "internal/日本語/store/workflow_architecture.go" {
			t.Fatalf("decoded path = %q, want the exact declared bytes", got)
		}
	})
	t.Run("literal replacement character is valid data", func(t *testing.T) {
		raw := designArchitectureRawFixtureWithOwnerPath(t, "internal/\uFFFD/store/workflow_architecture.go")
		decoded, err := decodeWorkflowDesignArchitecture(raw)
		if err != nil {
			t.Fatalf("literal replacement character refused at decode: %v", err)
		}
		if got := decoded.Owners[0].Path; got != "internal/\uFFFD/store/workflow_architecture.go" {
			t.Fatalf("decoded path = %q, want the exact declared bytes", got)
		}
	})
}

func TestArchitectureValueStructureExactPath(t *testing.T) {
	passes := []string{
		"internal/store/x.go",
		"internal/日本語/×.go",
		"internal/\uFFFD/x.go",
	}
	for _, path := range passes {
		t.Run("accepts "+path, func(t *testing.T) {
			if err := designArchitectureContainedPath("owner path", path); err != nil {
				t.Fatalf("exact contained path %q refused: %v", path, err)
			}
		})
	}
	cases := []struct {
		name string
		path string
	}{
		{"empty path", ""},
		{"absolute path", "/etc/passwd"},
		{"self segment", "./x.go"},
		{"parent traversal", "internal/../store/x.go"},
		{"trailing separator", "internal/"},
		{"empty segment", "internal//x.go"},
		{"tmp source", "tmp/scratch.go"},
		{"control byte", "internal\x00/x.go"},
		{"glob star", "internal/store/*.go"},
		{"glob question", "internal/store/u?.go"},
		{"glob bracket", "internal/store/f[0].go"},
		{"oversize path", strings.Repeat("a", 513)},
		{"invalid utf-8 byte", "internal\xff/x.go"},
	}
	for _, testCase := range cases {
		t.Run("refuses "+testCase.name, func(t *testing.T) {
			err := designArchitectureContainedPath("owner path", testCase.path)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

func TestArchitectureValueStructureProjectEntry(t *testing.T) {
	t.Run("member project with contained path passes", func(t *testing.T) {
		if err := designArchitectureRepoEntry("file path", "concord", "internal/store/x.go", designArchitectureMemberProject); err != nil {
			t.Fatalf("member repo entry refused: %v", err)
		}
	})
	t.Run("member project with multibyte path passes", func(t *testing.T) {
		if err := designArchitectureRepoEntry("file path", "concord", "internal/日本語/x.go", designArchitectureMemberProject); err != nil {
			t.Fatalf("multibyte member repo entry refused: %v", err)
		}
	})
	cases := []struct {
		name    string
		project string
		path    string
	}{
		{"one byte project", "c", "x.go"},
		{"oversize project", strings.Repeat("p", 129), "x.go"},
		{"project outside membership", "elsewhere", "x.go"},
		{"glob path", "concord", "internal/store/*.go"},
		{"traversal path", "concord", "../x.go"},
		{"invalid utf-8 path", "concord", "internal\xff/x.go"},
	}
	for _, testCase := range cases {
		t.Run("refuses "+testCase.name, func(t *testing.T) {
			err := designArchitectureRepoEntry("file path", testCase.project, testCase.path, designArchitectureMemberProject)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

func TestArchitectureValueStructureRepositorySource(t *testing.T) {
	pinned := func(path string) WorkContextReadingSource {
		return WorkContextReadingSource{
			Kind:      WorkContextSourceRepositoryFile,
			ProjectID: "concord",
			Path:      path,
			CommitOID: strings.Repeat("a", 40),
		}
	}
	t.Run("pinned repository source passes", func(t *testing.T) {
		if err := designArchitectureRepositorySource(pinned("scripts/generate-agent-lanes.py"), "reference source", designArchitectureMemberProject); err != nil {
			t.Fatalf("pinned repository source refused: %v", err)
		}
	})
	t.Run("pinned multibyte source path passes", func(t *testing.T) {
		if err := designArchitectureRepositorySource(pinned("scripts/生成/gen.py"), "reference source", designArchitectureMemberProject); err != nil {
			t.Fatalf("multibyte pinned source refused: %v", err)
		}
	})
	cases := []struct {
		name   string
		source WorkContextReadingSource
	}{
		{"knowledge arm", func() WorkContextReadingSource {
			source := pinned("scripts/gen.py")
			source.Kind = WorkContextSourceKnowledge
			source.SourceID = "source"
			source.LawID = "law"
			source.ContentHash = "sha256:" + strings.Repeat("b", 64)
			source.Path = ""
			return source
		}()},
		{"commit too short", func() WorkContextReadingSource {
			source := pinned("scripts/gen.py")
			source.CommitOID = strings.Repeat("a", 39)
			return source
		}()},
		{"commit not hex", func() WorkContextReadingSource {
			source := pinned("scripts/gen.py")
			source.CommitOID = strings.Repeat("g", 40)
			return source
		}()},
		{"carries knowledge fields", func() WorkContextReadingSource {
			source := pinned("scripts/gen.py")
			source.SourceID = "source"
			return source
		}()},
		{"project outside membership", func() WorkContextReadingSource {
			source := pinned("scripts/gen.py")
			source.ProjectID = "elsewhere"
			return source
		}()},
		{"invalid utf-8 path", pinned("scripts/gen\xff.py")},
	}
	for _, testCase := range cases {
		t.Run("refuses "+testCase.name, func(t *testing.T) {
			err := designArchitectureRepositorySource(testCase.source, "reference source", designArchitectureMemberProject)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

func TestArchitectureValueStructureSections(t *testing.T) {
	owner := func(id string) WorkflowDesignArchitectureOwner {
		return WorkflowDesignArchitectureOwner{OwnerID: id, ProjectID: "concord", Path: "a/b.go", Symbol: "ss"}
	}
	ident := func(owner WorkflowDesignArchitectureOwner) string { return owner.OwnerID }
	t.Run("count boundaries", func(t *testing.T) {
		if _, err := designArchitectureSection(nil, 1, designArchitectureOwnersMax, "owners", ident); err == nil {
			t.Fatal("empty section accepted")
		}
		for _, count := range []int{1, designArchitectureOwnersMax} {
			entries := make([]WorkflowDesignArchitectureOwner, 0, count)
			for i := 0; i < count; i++ {
				entries = append(entries, owner(fmt.Sprintf("owner-%02d", i)))
			}
			index, err := designArchitectureSection(entries, 1, designArchitectureOwnersMax, "owners", ident)
			if err != nil {
				t.Fatalf("section of %d entries refused: %v", count, err)
			}
			if len(index) != count {
				t.Fatalf("identity index holds %d entries, want %d", len(index), count)
			}
		}
		entries := make([]WorkflowDesignArchitectureOwner, 0, designArchitectureOwnersMax+1)
		for i := 0; i <= designArchitectureOwnersMax; i++ {
			entries = append(entries, owner(fmt.Sprintf("owner-%02d", i)))
		}
		if _, err := designArchitectureSection(entries, 1, designArchitectureOwnersMax, "owners", ident); err == nil {
			t.Fatal("section over its bound accepted")
		}
	})
	t.Run("identity grammar boundaries", func(t *testing.T) {
		if _, err := designArchitectureSection([]WorkflowDesignArchitectureOwner{owner("owner-0")}, 1, designArchitectureOwnersMax, "owners", ident); err != nil {
			t.Fatalf("stable identity refused: %v", err)
		}
		for _, bad := range []string{"-leading", "has space", strings.Repeat("a", 129)} {
			if _, err := designArchitectureSection([]WorkflowDesignArchitectureOwner{owner(bad)}, 1, designArchitectureOwnersMax, "owners", ident); err == nil {
				t.Fatalf("identity %q accepted outside the stable grammar", bad)
			}
		}
		duplicate := []WorkflowDesignArchitectureOwner{owner("owner-0"), owner("owner-0")}
		if _, err := designArchitectureSection(duplicate, 1, designArchitectureOwnersMax, "owners", ident); err == nil {
			t.Fatal("duplicate identity accepted")
		}
	})
	t.Run("count primitive boundaries", func(t *testing.T) {
		if err := designArchitectureCount(1, 1, 1, "items", "one item only"); err != nil {
			t.Fatalf("count at exact bound refused: %v", err)
		}
		for _, count := range []int{0, 2} {
			if err := designArchitectureCount(count, 1, 1, "items", "one item only"); err == nil {
				t.Fatalf("count %d outside the exact bound accepted", count)
			}
		}
	})
}

func TestArchitectureValueStructureBoundedProse(t *testing.T) {
	passes := []struct {
		name  string
		value string
	}{
		{"one byte", "x"},
		{"ascii at the byte bound", strings.Repeat("a", 1024)},
		{"multibyte at the byte bound", strings.Repeat("é", 512)},
		{"escaping stays data", "quote \" backslash \\ newline \n tab \t bell \a html <b>&amp;</b>"},
		{"literal replacement character", "ok \uFFFD"},
	}
	for _, testCase := range passes {
		t.Run("accepts "+testCase.name, func(t *testing.T) {
			if err := designArchitectureBoundedText(testCase.value, "invariant statement", designArchitectureStatementMaxBytes); err != nil {
				t.Fatalf("bounded prose refused: %v", err)
			}
		})
	}
	refusals := []struct {
		name  string
		value string
	}{
		{"empty value", ""},
		{"ascii over the byte bound", strings.Repeat("a", 1025)},
		{"multibyte over the byte bound", strings.Repeat("é", 513)},
		{"invalid utf-8", "ok \xff"},
	}
	for _, testCase := range refusals {
		t.Run("refuses "+testCase.name, func(t *testing.T) {
			err := designArchitectureBoundedText(testCase.value, "invariant statement", designArchitectureStatementMaxBytes)
			assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
		})
	}
}

func TestArchitectureValueStructureCapacity(t *testing.T) {
	t.Run("absent allocation refuses", func(t *testing.T) {
		err := validateDesignArchitectureCapacity(nil)
		assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
	})
	t.Run("fixture witness allocation sums exactly to the cap and passes", func(t *testing.T) {
		capacity := designArchitectureFixture().Capacity
		sum := capacity.DesignRecord + capacity.WorkerJob + capacity.WorkContext + capacity.LawContext +
			capacity.ProposalRecord + capacity.WorkRecord + capacity.TaskPremise + capacity.OutcomePredicates
		if sum != designArchitectureCapacityTotalMax {
			t.Fatalf("witness allocation sums to %d, want exactly %d", sum, designArchitectureCapacityTotalMax)
		}
		if err := validateDesignArchitectureCapacity(capacity); err != nil {
			t.Fatalf("exact allocation refused: %v", err)
		}
	})
	t.Run("one byte over the allocation refuses", func(t *testing.T) {
		capacity := designArchitectureFixture().Capacity
		capacity.OutcomePredicates++
		err := validateDesignArchitectureCapacity(capacity)
		assertDesignArchitectureRefusal(t, err, KindLimitExceeded)
	})
	t.Run("negative slot refuses", func(t *testing.T) {
		capacity := designArchitectureFixture().Capacity
		capacity.WorkContext = -1
		err := validateDesignArchitectureCapacity(capacity)
		assertDesignArchitectureRefusal(t, err, KindInvalidPayload)
	})
	t.Run("max int slot refuses without overflowing the sum", func(t *testing.T) {
		capacity := &WorkflowDesignArchitectureCapacity{DesignRecord: math.MaxInt}
		err := validateDesignArchitectureCapacity(capacity)
		assertDesignArchitectureRefusal(t, err, KindLimitExceeded)
	})
	t.Run("two half max int slots overflow the running sum and refuse", func(t *testing.T) {
		capacity := &WorkflowDesignArchitectureCapacity{
			DesignRecord: math.MaxInt/2 + 1,
			WorkerJob:    math.MaxInt/2 + 1,
		}
		err := validateDesignArchitectureCapacity(capacity)
		assertDesignArchitectureRefusal(t, err, KindLimitExceeded)
	})
}

func TestArchitectureValueStructureCanonicalBound(t *testing.T) {
	t.Run("closed fixture passes well under the bound", func(t *testing.T) {
		if err := validateDesignArchitectureCanonicalBound(designArchitectureFixture()); err != nil {
			t.Fatalf("closed fixture refused at the canonical bound: %v", err)
		}
	})
	t.Run("canonical plan at exactly 8192 bytes passes", func(t *testing.T) {
		plan := designArchitectureAtCanonicalBound(t)
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("cannot encode the padded plan: %v", err)
		}
		if len(raw) != designArchitectureCanonicalMaxBytes {
			t.Fatalf("padded plan is %d bytes, want exactly %d", len(raw), designArchitectureCanonicalMaxBytes)
		}
		if err := validateDesignArchitectureCanonicalBound(plan); err != nil {
			t.Fatalf("plan at the canonical bound refused: %v", err)
		}
	})
	t.Run("canonical plan one byte over refuses", func(t *testing.T) {
		plan := designArchitectureAtCanonicalBound(t)
		plan.UnchangedFiles[len(plan.UnchangedFiles)-1].Path += "a"
		err := validateDesignArchitectureCanonicalBound(plan)
		assertDesignArchitectureRefusal(t, err, KindLimitExceeded)
	})
	t.Run("escaped prose measures its serialized bytes", func(t *testing.T) {
		// A statement of quote/control bytes costs far more canonical bytes
		// than its raw length; the padded plan must refuse once those escaped
		// bytes push it over the bound even though every field bound holds.
		plan := designArchitectureAtCanonicalBound(t)
		plan.UnchangedFiles = plan.UnchangedFiles[:len(plan.UnchangedFiles)-1]
		plan.Invariants[0].Statement = strings.Repeat(`"\a`+"\n\t", 100)
		err := validateDesignArchitectureCanonicalBound(plan)
		assertDesignArchitectureRefusal(t, err, KindLimitExceeded)
	})
}
