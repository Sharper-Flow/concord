package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

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

var (
	envelopeSchemaOnce  sync.Once
	envelopeSchemaValue map[string]any
)

func envelopeSchemaDocument() map[string]any {
	envelopeSchemaOnce.Do(func() {
		decoder := json.NewDecoder(strings.NewReader(GeneratedEnvelopeSchemaDocument))
		decoder.UseNumber()
		if err := decoder.Decode(&envelopeSchemaValue); err != nil {
			panic(fmt.Sprintf("generated envelope schema document is not JSON: %v", err))
		}
	})
	return envelopeSchemaValue
}

// ValidateGeneratedEnvelope checks one marshalled envelope against the
// generated TS7 envelope schema, the same contracts/agent-tool-envelope.schema.json
// projection the generated adapter validator enforces. The producer calls it
// through Envelope.Validate so a wire shape the declared law does not name
// fails closed instead of reaching agents as an untyped reconciliation.
func ValidateGeneratedEnvelope(data []byte) error {
	var value any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	document := envelopeSchemaDocument()
	return payloadschema.ValidateValue(value, document, document, "$")
}
