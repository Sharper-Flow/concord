// Package agent contains the core-owned, generated-contract-backed agent runtime
// primitives. It deliberately does not dispatch domain operations.
package agent

// MaxEnvelopeBytes is the shared envelope-era cap that bounds the response
// output and the standalone body caps (receipt render, release input, launch
// prompt). It is not the request transport limit.
const MaxEnvelopeBytes = 65536

// MaxRequestBytes is the request transport limit for the whole invoke
// request — the plugin request envelope and the CLI stdin body. It is
// independent of the store's workflow action payload budget and response caps.
const MaxRequestBytes = 8 * 1024 * 1024
