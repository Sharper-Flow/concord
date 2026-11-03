// Package concord embeds the canonical agent tool contract documents the Go
// core validates against. The contracts under contracts/ are the single
// published source: the core, the adapter generators and the drift checks all
// read the same bytes, so the core carries no Go-side copy of a contract.
package concord

import _ "embed"

// PayloadSchemaDocument is the generated agent tool payload schema,
// contracts/agent-tool-surface-payloads.schema.json.
//
//go:embed contracts/agent-tool-surface-payloads.schema.json
var PayloadSchemaDocument string

// EnvelopeSchemaDocument is the canonical TS7 envelope schema,
// contracts/agent-tool-envelope.schema.json.
//
//go:embed contracts/agent-tool-envelope.schema.json
var EnvelopeSchemaDocument string
