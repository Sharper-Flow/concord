// Package agent contains the core-owned, generated-contract-backed agent runtime
// primitives. It deliberately does not dispatch domain operations.
package agent

import "github.com/sharper-flow/concord/internal/store"

// MaxEnvelopeBytes is the shared envelope-era cap that bounds the response
// output and the standalone body caps (receipt render, release input, launch
// prompt). It is not the request transport limit.
const MaxEnvelopeBytes = 65536

// MaxRequestBytes is the request transport limit for the whole invoke
// request — the plugin request envelope and the CLI stdin body. It aliases
// the store's shared action payload ceiling, which the widest-shape
// derivation in internal/agent pins. It never bounds a response, whose caps
// stay MaxEnvelopeBytes and agentcontract.MaxResultEnvelopeBytes.
const MaxRequestBytes = store.MaxWorkflowActionPayloadBytes
