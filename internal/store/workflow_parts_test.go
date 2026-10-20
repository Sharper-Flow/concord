package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
)

// CD-0229 D1: each step is defined in exactly one part, counting every step
// ID a part can emit through its parameters.
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

// CD-0229 D2: a new composition is assembled from existing parts and compiles
// to a valid definition; it adds no part and no step code.
func TestWorkflowCompositionCompilesWithoutNewStepCode(t *testing.T) {
	t.Parallel()
	head, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	base := builtinWorkflowComposition(t, head.Definition.Ref, head.Definition.Version)
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

// CD-0229 D2 and D3: each live family head is the compiled form of its
// composition, byte for byte, so the registered digest is the compiled digest.
func TestLiveHeadsAreTheirCompiledCompositions(t *testing.T) {
	t.Parallel()
	for _, head := range BuiltinWorkflowDefinitions() {
		composition := builtinWorkflowComposition(t, head.Ref, head.Version)
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
			t.Errorf("%s v%d: the registered head is not its compiled composition", head.Ref, head.Version)
		}
	}
}

// The first composed heads change no behavior: each holds the same steps in the
// same order with the same ordered actions (workflowStepIsDeliveryGate reads
// that order), the same edges, recovery routes, action contracts, policy, and
// capabilities as the released head it succeeds. Only the definition-level
// available_actions and action_definitions lists may be reordered.
func TestComposedHeadsMatchTheReleasedHeadsTheySucceed(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct {
		ref               string
		composed, release int64
	}{
		{"workflow.implementation", 33, 32},
		{"workflow.implementation", 37, 35},
		{"workflow.break_fix", 30, 29},
		{"workflow.research", 20, 19},
	} {
		composed, ok := BuiltinWorkflowRegistry().Lookup(pair.ref, pair.composed)
		previous, held := BuiltinWorkflowRegistry().Lookup(pair.ref, pair.release)
		if !ok || !held {
			t.Fatalf("%s v%d or v%d is not registered", pair.ref, pair.composed, pair.release)
		}
		head := composed.Definition
		if got, want := workflowShapeForComparison(t, head), workflowShapeForComparison(t, previous.Definition); got != want {
			t.Errorf("%s v%d differs from v%d beyond list order", head.Ref, head.Version, previous.Definition.Version)
		}
		if workflowCapabilities(head) != workflowCapabilities(previous.Definition) {
			t.Errorf("%s v%d enables different capabilities than v%d", head.Ref, head.Version, previous.Definition.Version)
		}
	}
}

// CD-0229 D5: the small implementation composition is define (alignment and
// planning), implement, review, and ship. Full stays the family head, so
// every other size and unsized work resolve to it.
func TestSmallImplementationComposesDefineImplementReviewShip(t *testing.T) {
	t.Parallel()
	const ref = "workflow.implementation"
	head, err := BuiltinWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	small, err := builtinWorkflowDefinitionForSize(ref, WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	definition := small.Definition
	if definition.Version >= head.Definition.Version || workflowCompositionSize(ref, definition.Version) != WorkSizeSmall {
		t.Fatalf("small v%d must be a sized composition below the full head v%d", definition.Version, head.Definition.Version)
	}
	var steps []string
	for _, step := range definition.StepGraph.Steps {
		steps = append(steps, step.ID)
	}
	if want := []string{"alignment", "planning", "execution", "acceptance", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("small steps = %v, want %v", steps, want)
	}
	if !slices.Contains(definition.StepGraph.Edges, WorkflowEdge{From: "acceptance", To: "execution", Kind: WorkflowEdgeFailure}) {
		t.Fatalf("a small review finding must return to execution: %+v", definition.StepGraph.Edges)
	}
	if definition.ApprovalPolicy != head.Definition.ApprovalPolicy || !slices.Equal(definition.RequiredEvidenceKinds, head.Definition.RequiredEvidenceKinds) {
		t.Fatal("small must keep the full head's approval policy and evidence: no size ships with zero proof")
	}
	for _, size := range []WorkSize{"", WorkSizeLarger, WorkSizeCrucial} {
		selected, err := builtinWorkflowDefinitionForSize(ref, size)
		if err != nil || selected.Definition.Version != head.Definition.Version {
			t.Fatalf("size %q selected v%d, %v; want the full head v%d", size, selected.Definition.Version, err, head.Definition.Version)
		}
	}
	for _, family := range []string{"workflow.break_fix", "workflow.research"} {
		familyHead, err := BuiltinWorkflowDefinitionForRef(family)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := builtinWorkflowDefinitionForSize(family, WorkSizeSmall)
		if err != nil || selected.Definition.Version != familyHead.Definition.Version {
			t.Fatalf("%s small selected v%d, %v; a family without a small composition keeps its head", family, selected.Definition.Version, err)
		}
	}
	if _, err := builtinWorkflowDefinitionForSize("workflow.unknown_family", WorkSizeSmall); !hasFailureKind(err, KindDefinitionDigestMismatch) {
		t.Fatalf("an unregistered family = %v, want the registry refusal", err)
	}
}

func builtinWorkflowComposition(t *testing.T, ref string, version int64) WorkflowComposition {
	t.Helper()
	for _, composition := range builtinWorkflowCompositions() {
		if composition.Ref == ref && composition.Version == version {
			return composition
		}
	}
	t.Fatalf("no composition for %s v%d", ref, version)
	return WorkflowComposition{}
}

// workflowShapeForComparison renders a definition with version cleared and
// its two definition-level action lists sorted, so two definitions compare
// equal exactly when they differ only in version and in those lists' order.
func workflowShapeForComparison(t *testing.T, definition WorkflowDefinition) string {
	t.Helper()
	d := normalizeWorkflowDefinition(definition)
	d.Version = 0
	sort.Strings(d.AvailableActions)
	sort.Slice(d.ActionDefinitions, func(i, j int) bool { return d.ActionDefinitions[i].ID < d.ActionDefinitions[j].ID })
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// workflowPartVersionPins holds the digest of every released part version
// (CD-0229 D3). Editing a released part changes its digest and fails this
// test: ship the change as a new part version and add its digest here.
var workflowPartVersionPins = map[string]string{
	"part.proposal v1":    "sha256:a206cbb04379b37b348068e926e8763aa05cb53517b47710b44e7c0f935b2fa8",
	"part.alignment v1":   "sha256:da0cc472e8cef6fa8618101fc9fef8765253d4b4f7a4295a27a3ec12cd45261c",
	"part.design v1":      "sha256:28b95de52d2d3e37baf34b993a74b2a0047963c27c465f756cbc8c3822b96dd7",
	"part.diagnosis v1":   "sha256:cb38a8dd66e16d105f76331ab9b6ef1577df1d2ea9e44a6029bb0f5cdf0467cb",
	"part.planning v1":    "sha256:728d3f2d09030a90521e04b1d3f85313dcb53985500dcf0eb12e951716b51c94",
	"part.execution v1":   "sha256:b49ffd7600b17b4f84614b0ef3a523b0d0ba4186166f566543655f15770ddd0c",
	"part.execution v2":   "sha256:05dabd5ea52a9aa4bdab5f12ef4f9b9ff5d34bc5ee9f4eabc31afda1ecb85eed",
	"part.repair v1":      "sha256:a6aea7c89c648eb273384f2df934008a989634ce8fa08ca7d704886346d79725",
	"part.investigate v1": "sha256:d6588623b7a7bfcc252558a6088233b09a6a5da07555d00fb0bc7d6e3aac324d",
	"part.findings v1":    "sha256:2335e131f433452f678c5636caa5d87b199f2362cad6ead11a11514053274f5b",
	"part.refine v1":      "sha256:a75892d11d65be5b0335f9391b6ec294d28c539abe697130dda4577cd4f9ac61",
	"part.delivery v1":    "sha256:476be87d5f80afe76b7d82a2415180df4f22c1f1a7849ad69c649d49172b9bf5",
	"part.verdict v1":     "sha256:c0b74473625db971cfed009736efa7148f03c02a7f8e8d82a55ad343f2bec06d",
	"part.close v1":       "sha256:d7fc2c1abcee2340642165713f8f93f28a965600b29386e69e7a68bc8a827585",
	"part.close v2":       "sha256:6ce2f07328cb8fb96a3f34376ac0247c77074f5140e3a4d0212bca541f69d175",
}

func TestWorkflowPartVersionPinsHold(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, part := range builtinWorkflowParts() {
		key := workflowPartKey(part.Ref, part.Version)
		seen[key] = true
		raw, err := json.Marshal(part)
		if err != nil {
			t.Fatal(err)
		}
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		pinned, ok := workflowPartVersionPins[key]
		if !ok {
			t.Errorf("%s is released but holds no digest pin", key)
		} else if digest != pinned {
			t.Errorf("%s digest drifted: computed %s pinned %s", key, digest, pinned)
		}
	}
	for key := range workflowPartVersionPins {
		if !seen[key] {
			t.Errorf("%s holds a pin but is not released", key)
		}
	}
}

func TestWorkflowPartCatalogRefusesASecondStepOwner(t *testing.T) {
	t.Parallel()
	parts := builtinWorkflowParts()
	second := parts[0]
	second.Ref = "part.proposal_copy"
	_, err := workflowPartCatalog(append(parts, second))
	assertInvalidDefinition(t, err, "step proposal is defined by part.proposal and part.proposal_copy")
}

// Every parameter a use sets is checked against its part, and every name a
// composition pins must be released.
func TestWorkflowCompositionRefusesInvalidUses(t *testing.T) {
	t.Parallel()
	head, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	base := builtinWorkflowComposition(t, head.Definition.Ref, head.Definition.Version)
	edit := func(index int, change func(*WorkflowPartUse)) WorkflowComposition {
		composition := base
		composition.Parts = slices.Clone(base.Parts)
		change(&composition.Parts[index])
		return composition
	}
	verdict := slices.IndexFunc(base.Parts, func(use WorkflowPartUse) bool { return use.Ref == "part.verdict" })
	execution := slices.IndexFunc(base.Parts, func(use WorkflowPartUse) bool { return use.Ref == "part.execution" })
	unreleasedCatalog := base
	unreleasedCatalog.ActionCatalogVersion = 99
	for name, tc := range map[string]struct {
		composition WorkflowComposition
		detail      string
	}{
		"unreleased part":        {edit(execution, func(use *WorkflowPartUse) { use.Version = 9 }), "workflow part part.execution v9 is not released"},
		"unknown step":           {edit(verdict, func(use *WorkflowPartUse) { use.StepID = "judge" }), `use of part.verdict names step "judge", not one of [acceptance verify conclude]`},
		"unchosen step":          {edit(verdict, func(use *WorkflowPartUse) { use.StepID = "" }), `use of part.verdict names step "", not one of [acceptance verify conclude]`},
		"undeclared lead action": {edit(verdict, func(use *WorkflowPartUse) { use.LeadAction = "frame_research" }), `use of part.verdict names lead action "frame_research", not one of [bind_evidence record_conclusion]`},
		"failure edge":           {edit(execution, func(use *WorkflowPartUse) { use.FailureTo = "refine" }), "part part.execution declares no failure edge"},
		"missing recovery":       {edit(verdict, func(use *WorkflowPartUse) { use.RecoveryTo = "" }), "use of part.verdict must name a recovery target exactly when the part opens recovery routes"},
		"stray recovery":         {edit(execution, func(use *WorkflowPartUse) { use.RecoveryTo = "execution" }), "use of part.execution must name a recovery target exactly when the part opens recovery routes"},
		"premise route":          {edit(verdict, func(use *WorkflowPartUse) { use.DisprovedPremiseRoute = true }), "part part.verdict offers no premise route"},
		"unreleased catalog":     {unreleasedCatalog, "action catalog v99 is not released"},
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
