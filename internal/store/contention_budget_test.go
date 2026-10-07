//go:build !race

package store

// The uninstrumented regression keeps its contention window below the
// demonstrated per-entry validator hold without changing production's 5s budget.
const testContentionBusyTimeoutMs = 250
