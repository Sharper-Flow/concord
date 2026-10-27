package agent

import (
	"fmt"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func ValidateOperationPayload(tool, operation string, data []byte, result bool) error {
	if err := payloadschema.ValidateUniqueJSON(data); err != nil {
		return err
	}
	contract, ok := ValidateContractOperation(tool, operation)
	if !ok {
		return fmt.Errorf("unknown operation payload %s.%s", tool, operation)
	}
	name := contract.ResultSchema
	if !result {
		name = contract.InputSchema
	}
	if name == "" {
		return fmt.Errorf("operation payload schema is not generated for %s", contract.ID)
	}
	if err := ValidateGeneratedPayload(tool, contract.ID, name, data, result); err != nil {
		if !result {
			if detail := ValidatePayloadSchema(name, data); payloadschema.HasSelectedGuidance(detail) {
				return detail
			}
		}
		return err
	}
	if err := ValidatePayloadSchema(name, data); err != nil {
		return err
	}
	return nil
}

// ValidatePayloadSchema checks one payload against the named generated schema.
// The schema document and the validation engine belong to payloadschema, which
// the workflow engine answers to as well, so one declaration keeps one
// enforcement authority.
func ValidatePayloadSchema(name string, data []byte) error {
	return payloadschema.Validate(name, data)
}
