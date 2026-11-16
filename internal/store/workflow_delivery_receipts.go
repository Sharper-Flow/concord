package store

import "encoding/json"

func workflowEventDeliveryAssertion(event Event) (bool, error) {
	if event.SubjectType != SubjectWorkItem || event.Kind != WorkflowActionCompleted {
		return false, nil
	}
	var fields struct {
		Artifact string `json:"delivery_artifact"`
		State    string `json:"delivery_state"`
	}
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return false, err
	}
	return fields.Artifact != "" && fields.State == "asserted", nil
}
