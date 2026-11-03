package payloadschema

import (
	"sync"

	concord "github.com/sharper-flow/concord"
)

var (
	envelopeDocumentOnce  sync.Once
	envelopeDocumentValue map[string]any
)

// ValidateEnvelope checks one JSON value against the embedded TS7 envelope
// root schema. Duplicate-member uniqueness is a separate lexical check.
func ValidateEnvelope(data []byte) error {
	value, err := decodeOne(data)
	if err != nil {
		return err
	}
	loadDocumentOnce(&envelopeDocumentOnce, concord.EnvelopeSchemaDocument, &envelopeDocumentValue, "envelope schema")
	return ValidateValue(value, envelopeDocumentValue, envelopeDocumentValue, "$")
}
