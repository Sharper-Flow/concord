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
//     effect-receipt storage, sequence, finite-snapshot, installation-key and typed
//     result-code mechanics, including the historical routing-digest SQL function.
//   - backup.go: snapshot admission, manifest, restore verification and promotion
//     policy; SQLite copy, integrity probes and state hashing stay in sqlitedb.
//   - store.go, transaction.go: domain-facing open and transaction lifecycles.
//   - events.go, operation.go, fold.go: domain-facing effect identity, registered
//     current-row writers, and transaction-owned write scopes.
//   - work_context_migration.go: frozen replay of historical work-context
//     readings and findings for migration.
//   - workflow_dispatch.go, workflow_preflight.go: action admission inside the
//     owning transaction.
//   - workflow_domain_overlap.go: the resolve_overlap work-relation writer.
//   - project_handoffs.go: Project handoff record, consume, and recovery.
//   - law_boundary.go, knowledge_*.go: accepted-law lookup and derived projections.
//   - session_resume_read.go: session identity, workflow resume, and pending
//     Project handoff at one store revision.
//
// See .concord/docs/core-architecture.md for the cross-package entry-point
// table and the accepted read-boundary invariants.
package store
