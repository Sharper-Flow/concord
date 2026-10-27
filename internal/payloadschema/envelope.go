package payloadschema

import "sync"

var (
	envelopeDocumentOnce  sync.Once
	envelopeDocumentValue map[string]any
)

// ValidateEnvelope checks one JSON value against the generated TS7 envelope
// root schema. Duplicate-member uniqueness is a separate lexical check.
func ValidateEnvelope(data []byte) error {
	value, err := decodeOne(data)
	if err != nil {
		return err
	}
	loadDocumentOnce(&envelopeDocumentOnce, GeneratedEnvelopeSchemaDocument, &envelopeDocumentValue, "envelope schema")
	return ValidateValue(value, envelopeDocumentValue, envelopeDocumentValue, "$")
}
