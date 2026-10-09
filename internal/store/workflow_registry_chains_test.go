package store

import "testing"

// Appending a promotion to a family chain retains the outgoing version and
// publishes the new current in the same act. Retention is a property of the
// promotion, not of a separately maintained list, so the registry's own
// checks hold on every promoted chain without further edits.
func TestWorkflowPromotionAppendsRetainTheOutgoingVersion(t *testing.T) {
	t.Parallel()
	for _, chain := range workflowDefinitionChains() {
		outgoing := chain[len(chain)-1]
		promoted := outgoing
		promoted.Version++
		extended := append(append([]WorkflowDefinition{}, chain...), promoted)
		if err := validateBuiltinWorkflowVersionContinuity(extended); err != nil {
			t.Fatalf("%s promoted chain fails continuity: %v", promoted.Ref, err)
		}
		registry := NewWorkflowDefinitionRegistry()
		for _, definition := range extended {
			if _, err := registry.Register(definition); err != nil {
				t.Fatalf("%s version %d does not register: %v", definition.Ref, definition.Version, err)
			}
		}
		if _, ok := registry.Lookup(outgoing.Ref, outgoing.Version); !ok {
			t.Fatalf("%s version %d did not stay registered across the promotion", outgoing.Ref, outgoing.Version)
		}
	}
}

// The currents are derived once: BuiltinWorkflowDefinitions is each family
// chain's last element, so a promotion cannot publish a version the chain
// does not carry.
func TestBuiltinDefinitionsAreTheChainTails(t *testing.T) {
	t.Parallel()
	chains := workflowDefinitionChains()
	currents := BuiltinWorkflowDefinitions()
	if len(currents) != len(chains) {
		t.Fatalf("current count = %d, want one per family chain (%d)", len(currents), len(chains))
	}
	for i, chain := range chains {
		tail := chain[len(chain)-1]
		if currents[i].Ref != tail.Ref || currents[i].Version != tail.Version {
			t.Fatalf("current %d = %s v%d, want the chain tail %s v%d", i, currents[i].Ref, currents[i].Version, tail.Ref, tail.Version)
		}
	}
}

// Store open answers pin compatibility from builtinWorkflowCurrentVersions
// instead of building the chains (CON-905). The table must name exactly the
// versions the registry registers, so a promotion that forgets the table
// fails here rather than refusing its own pins at open.
func TestBuiltinWorkflowCurrentVersionsMatchChains(t *testing.T) {
	t.Parallel()
	registry := BuiltinWorkflowRegistry()
	current := map[string]int64{}
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		if definition.Version > current[definition.Ref] {
			current[definition.Ref] = definition.Version
		}
	}
	if len(current) != len(builtinWorkflowCurrentVersions) {
		t.Fatalf("builtinWorkflowCurrentVersions names %d families, chains register %d: %v", len(builtinWorkflowCurrentVersions), len(current), current)
	}
	for ref, version := range current {
		if builtinWorkflowCurrentVersions[ref] != version {
			t.Fatalf("builtinWorkflowCurrentVersions[%q] = %d, chains register through version %d", ref, builtinWorkflowCurrentVersions[ref], version)
		}
		for candidate := int64(0); candidate <= version+1; candidate++ {
			_, registered := registry.Lookup(ref, candidate)
			if got := builtinWorkflowVersionRegistered(ref, candidate); got != registered {
				t.Fatalf("builtinWorkflowVersionRegistered(%q, %d) = %v, registry Lookup = %v", ref, candidate, got, registered)
			}
		}
	}
	if builtinWorkflowVersionRegistered("workflow.unknown", 1) {
		t.Fatal("an unknown workflow family must not be registered")
	}
}
