package store

import "encoding/json"

func historicalWorkflowPayload(action string, raw json.RawMessage) error {
	_, err := validateWorkflowPlanActionPayload(action, raw)
	return err
}
