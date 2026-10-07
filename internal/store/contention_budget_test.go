//go:build !race

package store

// The uninstrumented regression keeps its contention window below the
// demonstrated per-entry validator hold without changing production's 5s budget.
// 500ms admits the set-based validation's observed CI hold (297ms at 150x100)
// with headroom, while a reintroduced per-entry loop (~950ms at the same
// population) still exhausts it; the read-count assertion catches that loop
// deterministically when the hold fits the budget.
const testContentionBusyTimeoutMs = 500
