package store

import "encoding/json"

func workflowDeclaresDecisionDomains(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID == "approve_contract" {
			for _, field := range action.Payload.Fields {
				if field.Name == "decision_domains" && field.Required {
					return true
				}
			}
		}
	}
	return false
}

func workflowSubmittedDecisionDomains(definition WorkflowDefinition, fields map[string]json.RawMessage) ([]string, error) {
	if !workflowDeclaresDecisionDomains(definition) {
		return nil, nil
	}
	raw, present := fields["decision_domains"]
	var domains []string
	if present && json.Unmarshal(raw, &domains) != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_contract", "approval decision_domains is not a decodable string array", false, "declare the six decision classes as a JSON array")
	}
	if domains == nil {
		return nil, newFailure(KindInvalidPayload, "workflow_contract", "approval requires the submitted decision-domain fact", false, "declare the contract's decision domains explicitly")
	}
	if err := validateWorkflowAuthoredDecisionDomains(domains); err != nil {
		return nil, err
	}
	return domains, nil
}
