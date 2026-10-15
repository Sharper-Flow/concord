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
