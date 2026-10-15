package store

import "slices"

// The research-context publication heads. Each builder promotes its
// family one version past the failure-attribution head: the restated fields
// are the definition's own registry content, so every earlier version keeps
// the payload and digest it shipped, and the shared policy table stays
// byte-identical.

// researchContextSelectionField declares the optional selected-research
// action input on record_work_context: an array of the closed selection
// tuples, sharing the generic finding-reference bound. Absent and empty both
// select nothing, and the retained heads' closed payloads refuse the member
// outright, so an old pin can never author a selection.
func researchContextSelectionField() WorkflowPayloadField {
	return actionItemArrayField("research_finding_refs", false, 0, WorkContextFindingRefsMax, "work_context_research_selection")
}

// withResearchContextSelection restates record_work_context's payload as the
// current declaration plus the optional selected-research input. Only that
// action's in-definition entry changes; no global action policy, old
// builder, or frozen definition moves.
func withResearchContextSelection(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for index := range definition.ActionDefinitions {
		if definition.ActionDefinitions[index].ID != "record_work_context" {
			continue
		}
		definition.ActionDefinitions[index].Payload.Fields = append(slices.Clone(definition.ActionDefinitions[index].Payload.Fields), researchContextSelectionField())
	}
	return definition
}

// withResearchContextPacketRef points the definition's dispatch_worker packet
// field at the research-context packet3 closure, whose frozen decoder anchors
// the research-enabled work_context_view3. The packet3 identity keeps the
// 2.0 schema version; the coordinated extension rides the workflow
// definition heads and the frozen schema closure, not a new packet version
// constant. The retained packet2 declarations replay exactly the closure
// they recorded.
func withResearchContextPacketRef(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID != "dispatch_worker" {
			continue
		}
		for j := range definition.ActionDefinitions[i].Payload.Fields {
			field := &definition.ActionDefinitions[i].Payload.Fields[j]
			if field.Name == "worker_packet" {
				field.SchemaRef = "worker_packet3"
			}
		}
	}
	return definition
}

// Each builder below ships its family's research-context head: the promoted
// version restates the record_work_context payload and anchors the dispatch
// packet at the packet3 closure.
func implementationResearchContextV32() WorkflowDefinition {
	d := implementationFailureAttributionV31()
	d.Version = 32
	return withResearchContextPacketRef(withResearchContextSelection(d))
}

func breakFixResearchContextV29() WorkflowDefinition {
	d := breakFixFailureAttributionV28()
	d.Version = 29
	return withResearchContextPacketRef(withResearchContextSelection(d))
}

func researchResearchContextV19() WorkflowDefinition {
	d := researchFailureAttributionV18()
	d.Version = 19
	return withResearchContextPacketRef(withResearchContextSelection(d))
}
