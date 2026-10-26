// Package store holds Concord's sole durable local authority: SQLite current
// rows, effect receipts, and the workflow state they serve.
//
// Connection discipline (the invariant every reader of this package needs):
//
// The pool allows at most one connection: sqlitedb.Open calls
// SetMaxOpenConns(1) in internal/sqlitedb/connection.go.
// While any *sql.Tx is open, that transaction holds the pool's only
// connection. A nested call through s.db — query, exec, or another BeginTx —
// parks on the pool forever and never reaches SQLite. WAL and busy_timeout
// cannot help, and a test exercising it hangs. Never call an s.db-backed
// *Store method from inside a transaction. Extract or use a tx-scoped core:
// an xxxTx function, or a small queryer interface taking the tx. Raising the
// pool size is not the fix: a second connection would read the
// pre-transaction snapshot and silently miss uncommitted writes.
// txscope_test.go enforces this structurally.
//
// Transaction shape (sqlitedb.DSN): the data source name sets
// _txlock=immediate, so every explicit BeginTx is BEGIN IMMEDIATE and
// acquires the write lock before its first read. A read snapshot passes
// sql.TxOptions{ReadOnly: true} to get a deferred BEGIN that holds no write
// lock. busy_timeout, WAL, synchronous=NORMAL, and foreign_keys=ON travel in
// the data source name and are verified after open.
//
// Seams, each owned by its file and its adjacent tests:
//
//   - internal/sqlitedb: connection, pragma, revision, commit, current-row guard,
//     effect-receipt storage and sequence mechanics.
//   - store.go, transaction.go: domain-facing open and transaction lifecycles.
//   - events.go, operation.go, fold.go: domain-facing effect identity, registered
//     current-row writers, and transaction-owned write scopes.
//   - work_context.go, worker_packet_binding.go,
//     worker_context_findings.go: declared immutable readings, findings,
//     dispatch packets, and typed bounds.
//   - workflow_dispatch.go, workflow_preflight.go,
//     workflow_domain_overlap.go: action admission, law-revision staleness,
//     and shared-write overlap checks inside the owning transaction.
//   - worktrees.go, project_handoffs.go, session_vacate.go: claims,
//     occupancy, same-Project incarnation, and handoff recovery.
//   - research_*.go, law_boundary.go, knowledge_*.go: research-pack
//     identity, consumer pins, accepted-law lookup, derived projections.
//   - worker_lanes.go, worker_recovery.go: lane versions, packet and report
//     schema identity, and retained-result recovery as a data reader.
//
// See .concord/docs/core-architecture.md for the cross-package entry-point
// table and the accepted read-boundary invariants.
package store
