package store

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestCurrentDeclarationsResolvePublicDocument(t *testing.T) {
	for id := range builtinActionPolicies {
		action := currentActionDefinition(id, true)
		payloads := []WorkflowPayloadDefinition{action.Payload}
		if action.PublicPayload != nil {
			payloads = append(payloads, *action.PublicPayload)
		}
		for _, payload := range payloads {
			for _, field := range payload.Fields {
				for _, ref := range []string{field.SchemaRef, field.ItemRef} {
					if ref != "" && !payloadschema.HasIn(payloadschema.Document(), ref) {
						t.Errorf("%s.%s names unavailable schema %s", id, field.Name, ref)
					}
				}
			}
		}
	}
}
