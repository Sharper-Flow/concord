package store

// The live authoring selector narrows new authoring to three families while
// the frozen registry keeps every released pin registered for decode, replay,
// and rebuild. This file owns the authoring-side selection; the historical
// side stays in the registry owners.

// archivedWorkflowFamilies names the four families whose live authoring the
// successor retires. The closed set — not a complement of the live set —
// leaves runtime-registered fixture families and unreleased refs outside the
// retirement, and keeps the refusal precise about what it refuses.
var archivedWorkflowFamilies = map[string]bool{
	"workflow.architecture_spike": true,
	"workflow.ops_runbook":        true,
	"workflow.static_analysis":    true,
	"workflow.generic_one_off":    true,
}

// liveWorkflowAuthoringFamilies names the three families a new capture or
// revision may pin. Selection resolves the family's current definition: the
// live head is the frozen registry's latest version for that family, so no
// second definition source exists.
var liveWorkflowAuthoringFamilies = map[string]bool{
	"workflow.implementation": true,
	"workflow.break_fix":      true,
	"workflow.research":       true,
}

// workflowFamilyArchived reports whether a family's authoring is retired.
// The registry keeps the family's versions registered either way: archived
// families are read-protected history, not unregistered pins.
func workflowFamilyArchived(ref string) bool {
	return archivedWorkflowFamilies[ref]
}

// LiveWorkflowDefinitionForRef resolves the definition a new capture or
// revision may pin through the live authoring selector. A retired family is
// a typed authoring refusal before any write, and an unknown ref is the same
// refusal class it always was; the frozen lookup keeps its own broader
// contract for decode and replay.
func LiveWorkflowDefinitionForRef(ref string) (RegisteredDefinition, error) {
	if workflowFamilyArchived(ref) {
		return RegisteredDefinition{}, definitionFailure(KindInvalidDefinition,
			"workflow family "+ref+" is archived: new captures and revisions are refused")
	}
	if !liveWorkflowAuthoringFamilies[ref] {
		return RegisteredDefinition{}, definitionFailure(KindInvalidDefinition,
			"workflow family "+workflowRefExcerpt(ref)+" is not a live authoring family")
	}
	return BuiltinWorkflowDefinitionForRef(ref)
}

// workflowCanonicalAuthoringActive reports whether the pinned definition
// declares the canonical authoring contract: record_verdict's own payload
// names the verdicts batch alone. The signal is the definition's own
// registry content, not a mutable policy row, so the canonical surfaces
// (batch verdicts, explicit outcome predicates, no synthesized outcome pair)
// bind exactly to the heads that declare the canonical shape and never to a
// version table that could drift from it.
func workflowCanonicalAuthoringActive(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_verdict" {
			continue
		}
		if len(action.Payload.Fields) != 2 {
			return false
		}
		versionedBatch := false
		optionalVersion := false
		for _, field := range action.Payload.Fields {
			switch field.Name {
			case "verdicts":
				versionedBatch = field.Required
			case "contract_version":
				optionalVersion = !field.Required
			}
		}
		return versionedBatch && optionalVersion
	}
	return false
}
