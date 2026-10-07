//go:build race

package store

// Race instrumentation also instruments SQLite's Go VM. This budget admits
// the set-based validation but still rejects the per-entry query workload.
const testContentionBusyTimeoutMs = 20000
