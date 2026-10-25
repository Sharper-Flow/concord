package store

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestWorkflowPartsDefineEachStepOnce(t *testing.T) {
	t.Parallel()
	catalog, err := workflowPartCatalog(builtinWorkflowParts())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) == 0 {
		t.Fatal("the part catalog defines no steps")
	}
}

func TestWorkflowCompositionCompilesWithoutNewStepCode(t *testing.T) {
	t.Parallel()
	head, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	base := builtinWorkflowComposition(t, head.Definition.Ref)
	reduced := base
	reduced.Parts = slices.DeleteFunc(slices.Clone(base.Parts), func(use WorkflowPartUse) bool {
		return use.Ref == "part.proposal" || use.Ref == "part.alignment" || use.Ref == "part.design"
	})
	if len(reduced.Parts) == len(base.Parts) {
		t.Fatal("the implementation composition does not use the proposal, alignment, and design parts")
	}
	compiled, err := CompileWorkflowComposition(reduced)
	if err != nil {
		t.Fatalf("a composition of existing parts must compile: %v", err)
	}
	if slices.ContainsFunc(compiled.StepGraph.Steps, func(step WorkflowStep) bool { return step.ID == "proposal" }) {
		t.Fatal("a dropped part's step survived compilation")
	}
}

// The registered definition and its digest derive from the current composition.
func TestLiveHeadsAreTheirCompiledCompositions(t *testing.T) {
	t.Parallel()
	for _, head := range BuiltinWorkflowDefinitions() {
		composition := builtinWorkflowComposition(t, head.Ref)
		compiled, err := CompileWorkflowComposition(composition)
		if err != nil {
			t.Fatalf("%s: %v", head.Ref, err)
		}
		want, err := CanonicalWorkflowDefinition(head)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CanonicalWorkflowDefinition(compiled)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: the registered head is not its compiled composition", head.Ref)
		}
	}
}

func builtinWorkflowComposition(t *testing.T, ref string) WorkflowComposition {
	t.Helper()
	for _, composition := range builtinWorkflowCompositions() {
		if composition.Ref == ref {
			return composition
		}
	}
	t.Fatalf("no composition for %s", ref)
	return WorkflowComposition{}
}

func TestWorkflowPartCatalogRefusesASecondStepOwner(t *testing.T) {
	t.Parallel()
	parts := builtinWorkflowParts()
	second := parts[0]
	second.Ref = "part.proposal_copy"
	_, err := workflowPartCatalog(append(parts, second))
	assertInvalidDefinition(t, err, "step proposal is defined by part.proposal and part.proposal_copy")
}

// Every composition parameter is checked against its owning part.
func TestWorkflowCompositionRefusesInvalidUses(t *testing.T) {
	t.Parallel()
	head, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	base := builtinWorkflowComposition(t, head.Definition.Ref)
	edit := func(index int, change func(*WorkflowPartUse)) WorkflowComposition {
		composition := base
		composition.Parts = slices.Clone(base.Parts)
		change(&composition.Parts[index])
		return composition
	}
	verdict := slices.IndexFunc(base.Parts, func(use WorkflowPartUse) bool { return use.Ref == "part.verdict" })
	execution := slices.IndexFunc(base.Parts, func(use WorkflowPartUse) bool { return use.Ref == "part.execution" })
	proposal := slices.IndexFunc(base.Parts, func(use WorkflowPartUse) bool { return use.Ref == "part.proposal" })
	for name, tc := range map[string]struct {
		composition WorkflowComposition
		detail      string
	}{
		"unknown step":           {edit(verdict, func(use *WorkflowPartUse) { use.StepID = "judge" }), `use of part.verdict names step "judge", not one of [acceptance verify conclude]`},
		"unchosen step":          {edit(verdict, func(use *WorkflowPartUse) { use.StepID = "" }), `use of part.verdict names step "", not one of [acceptance verify conclude]`},
		"undeclared lead action": {edit(verdict, func(use *WorkflowPartUse) { use.LeadAction = "frame_research" }), `use of part.verdict names lead action "frame_research", not one of [bind_evidence record_conclusion]`},
		"failure edge":           {edit(execution, func(use *WorkflowPartUse) { use.FailureTo = "refine" }), "part part.execution declares no failure edge"},
		"missing recovery":       {edit(verdict, func(use *WorkflowPartUse) { use.RecoveryTo = "" }), "use of part.verdict must name a recovery target exactly when the part opens recovery routes"},
		"stray recovery":         {edit(proposal, func(use *WorkflowPartUse) { use.RecoveryTo = "execution" }), "use of part.proposal must name a recovery target exactly when the part opens recovery routes"},
		"premise route":          {edit(verdict, func(use *WorkflowPartUse) { use.DisprovedPremiseRoute = true }), "part part.verdict offers no premise route"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CompileWorkflowComposition(tc.composition)
			assertInvalidDefinition(t, err, tc.detail)
		})
	}
}

func assertInvalidDefinition(t *testing.T, err error, detail string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidDefinition || failure.Detail != detail {
		t.Fatalf("err = %v, want %s failure %q", err, KindInvalidDefinition, detail)
	}
}
