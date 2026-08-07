# R4: Competitive Mechanism Hardening — Research Findings

> **Status:** Research complete; recommendations accepted by CD-0008.
> **Decision:** [`CD-0008-concord-mechanism-hardening.md`](../decisions/CD-0008-concord-mechanism-hardening.md).
> **Question:** Which mechanisms from current agent and durable-execution systems
> would materially improve Concord without importing their Product shapes?
> **Date:** 2026-08-06.

## Summary

The research strengthens rather than overturns Concord's accepted direction. A
single local SQLite authority, append-only events, rebuildable typed projections,
short-lived invocations, durable idempotency, and Git-backed approved knowledge fit
the measured workload. The strongest additions concern authority edges and
execution mechanics: evidence resolution, unreadable-record isolation, durable
workflow checkpoints, stale-attempt fencing, typed external conditions, schema
upcasters, and an explicit shared-state/worktree contract.

No additional architecture decision is required before repository creation. The public
scrub, provenance record, and explicit creation authorization remain the only
pre-repository requirements. CD-0008 D2/D3 now resolve the evidence and cross-record
query mechanisms; the first runtime slice must implement and verify those accepted
rules rather than reopen them.

## Ranked findings

Ranked by strength of the case to adopt or formally retain—not by novelty.

| Rank | Finding | Recommendation | Timing |
|---:|---|---|---|
| 1 | **One shared Product authority plus isolated code worktrees** | Formalize one global local Concord DB outside every repo/worktree; each implementation workflow gets one tool-owned branch/worktree per affected repo. Stable Project/work identity never derives from a path. | Before worktree implementation |
| 2 | **Evidence-resolution authority** | Bind producer, durable evidence authority, consumer-resolution path, read-after-write guarantee, unresolved-proof result, and disagreement recovery. | Before first evidence policy/runtime slice |
| 3 | **Unreadable-record isolation** | Fail only queries whose answer depends on unreadable records; return typed partial/degraded results with the explicit unreadable set. Never infer “no conflict.” Global correctness/repair queries may still fail closed. | Before cross-record queries |
| 4 | **Durable workflow step journal** | Persist pinned workflow-type version, current step, accepted inputs, step result/evidence, interruption, and resume cursor as authoritative transitions. Every completed step is a checkpoint. | Before workflow engine |
| 5 | **Execution-attempt fencing** | Atomically increment an attempt epoch when claiming/resuming a durable operation; reject stale completions. Use provider idempotency where available and operation CAS locally. | Before cross-process/external effects |
| 6 | **Typed external-condition gates** | Represent awaited PR merge, CI result, timer, human approval, and remote work state as typed conditions linked into the dependency graph. Resolve from the owning authority; a work-status flag cannot substitute. | Before ops/implementation workflows |
| 7 | **Schema/event evolution contract** | Define upcaster registration/order, supported version window, projection migration, newer-than-binary refusal, and replay tests. | Before stable schema ships |
| 8 | **Worktree lifecycle truth** | Record branch/base/head/worktree-set references, but derive merge/reclaim from Git facts. Merge proof gates dependents; worktree deletion follows merge; stale status cannot veto proven reclamation. | Before worktree implementation |
| 9 | **Event-subject referential integrity** | Choose a closed-world subject registry or equivalent structurally validated reference; unknown/incompatible subjects fail before append. Do not use unenforced polymorphic relation endpoints. | During storage-spine design |
| 10 | **Replacement-relation home** | Bind replacement across Product/Project/workflow/resource identities before final typed DDL. Keep one canonical edge and deterministic inverse reads. | During storage-spine design |
| 11 | **Explicit concurrent-write rule** | State that live Product state does not branch or cell-merge: short writes serialize, expected versions reject stale intent, and deterministic events fold in accepted order. | Contract clarification |
| 12 | **Point-in-time diagnosis** | Expose read-only reconstruction at an event/version for audit and debugging. A historical fork creates new linked work; it never rewrites history. | Post-spine tool candidate |
| 13 | **Verified projection snapshots** | Add only if measured replay time requires them. Snapshot is disposable acceleration, carries event watermark/checksum, and never replaces the event log. | Falsifier-driven |
| 14 | **Isolated durable-note maintenance** | Let concurrent knowledge-maintenance agents use Git worktrees, while canonical note placement, operator approval, and merge rules remain authoritative. | Post-core |
| 15 | **Adversarial evidence techniques** | Add optional mutation/chaos verification and independent requirement-compliance review as evidence routes; do not depend on Qodo's retired CoverAgent implementation. | Workflow evidence design |
| 16 | **Explicit state taxonomy** | Keep conversation/transcript, filesystem/worktree, Product authority, native-system effects, and durable knowledge distinct. Resume never claims to restore an authority it did not checkpoint. | Documentation/tests |
| 17 | **Typed actions before model execution** | Retain generated schemas/core validation and independently test actions without an LLM; Jido corroborates this accepted direction. | Already accepted |
| 18 | **Live process handoff** | Borrow Herdr's old-process/new-process handoff acceptance pattern only if Concord later owns persistent launcher processes. It does not justify a core daemon. | Conditional/later |
| 19 | **Semantic memory/search** | Permit only an optional disposable one-way index over canonical notes. Never make vectors, summaries, or retrieval scores authority. | Conditional/later |
| 20 | **SQLite ten-process conformance** | Confirm the selected SQLite implementation under realistic concurrent reads, short writes, conflicts, crashes, retries, and recovery. This validates implementation behavior; it does not reopen engine selection absent a falsifier. | Required storage-slice evidence |
| 21 | **Dolt as contingency only** | Do not implement or benchmark Dolt unless SQLite breaches an accepted correctness/latency falsifier. Its strongest benefits solve branch/federation/multi-machine state, while Concord requires one current Product truth. | Conditional after falsifier |
| 22 | **Full external runtimes/product shapes** | Do not adopt Restate, DBOS, LangGraph Platform, Devin/Qodo cloud topology, Orca/Superset workspace authority, or Jido's runtime. Borrow mechanisms only. | Rejected |

## SQLite confirmation under ten local agents

Beads' choice is internally coherent. Its database is project-local, designed to
branch/federate, and expected to merge independently authored issue state. Beads
therefore recommends Dolt server mode when several agents on one machine write
concurrently. Dolt supplies versioned SQL, cell-level merge, native push/pull, and
multi-client transactions.

That does not automatically make it a better Concord authority.

### Same-machine comparison

| Concern | SQLite WAL, accepted Concord shape | Dolt server, one shared branch | Dolt branch per agent/worktree |
|---|---|---|---|
| Current Product truth | One view | One view | Several intentionally stale views |
| Readers/writers | Concurrent readers; writes serialize | Concurrent MVCC transactions | Concurrent per-branch transactions |
| Conflict rule | Expected-version rejection + one accepted order | Three-way cell merge; conflicting same-cell writes roll back | Merge after independently visible histories |
| Cross-row invariants | Checked once in the committing transaction | Must survive Dolt's merge/transaction semantics | Must be revalidated at branch merge across the complete resulting graph |
| Operational shape | In-process library; no daemon | `dolt sql-server` lifecycle/port/backups | Server plus branch/merge/reconciliation policy |
| Version history | Concord's domain event log | Domain log plus Dolt commit graph | Domain log plus multiple Dolt histories |
| Best fit | One local authority, short typed operations | High concurrent SQL write demand | Offline/federated independent authorities |

SQLite officially permits simultaneous readers and a writer in WAL mode and
serializes writers with serializable isolation. Concord's measured predecessor
workload was fewer than 0.1 writes/second system-wide, with about eight potentially
concurrent writer processes but none observed writing concurrently in the sampled
window. Ten active agents do not imply ten simultaneous state commits; most time is
spent reading, reasoning, editing, testing, or waiting on external systems.

Dolt can commit non-conflicting concurrent cell changes by three-way merging a
transaction against branch head. Dolt's own 2026 concurrency note also records two
important limits: same-cell conflicting values roll back, its repeatable-read merge
can be susceptible to lost updates, and commit-graph writes remain globally
serialized. Cell-level merge is not a substitute for Concord's cross-row lifecycle,
dependency, approval, evidence, and scope invariants.

### Decision rule

SQLite is selected from researched semantic fit and measured workload. Confirm the
implementation with a realistic ten-process conformance/load test. If that run shows
escaped `SQLITE_BUSY`, unacceptable P99/queueing, lost/duplicate effects, or an
invariant violation, reopen the decision and compare a single-writer IPC boundary,
Dolt server, and Postgres/DBOS—not Dolt alone.

The bounded SQLite conformance run uses ten independent processes from ten Git
worktrees:

1. reads while distinct entities mutate;
2. competing expected-version writes to the same entity;
3. atomic relation/lifecycle mutations spanning several rows;
4. invalid/corrupt subject and evidence records;
5. process death before, during, and after commit;
6. durable-operation resume with competing attempts; and
7. backup, rebuild, and binary-upgrade recovery.

Measure accepted effects, lost/duplicated effects, invariant violations,
`SQLITE_BUSY`/transaction conflicts, P50/P99, and recovery time. A passing run
confirms the implementation; it does not choose between engines. Alternative-engine
work begins only after a named falsifier fires; that later comparison must also measure
daemon/process burden and retained storage rather than silently dropping operational
cost from the decision.

## Target worktree topology

```text
${XDG_DATA_HOME:-~/.local/share}/concord/
├── concord.db                 # sole current Product/workflow authority
└── versions/<version>/...     # installed assets, not authority

repo default checkout          # always default branch; read/deploy source
worktree root/
└── <project-id>/
    └── <work-id>/
        ├── <repo-a worktree>  # branch bound to canonical work
        └── <repo-b worktree>  # optional cross-repo sibling
```

Binding rules:

1. A workflow has one canonical `work_id` and optional `worktree_set_id`; each
   affected Project contributes at most one active implementation worktree to that
   set.
2. Product/Project identity comes from stable IDs and registered repository
   locators—not checkout/worktree paths.
3. Worktree creation is a durable cross-authority operation: atomically claim work,
   pin base SHA/branch intent, create through the native Git owner, verify, then
   record the locator. Partial creation reconciles the same operation.
4. Every mutation still carries expected versions and durable idempotency. Worktree
   possession grants no Product authority.
5. Declared `modifies`/hard-soft `depends_on` edges drive coordination. File overlap
   and semantic scans may warn or suggest edges; heuristics never create blockers.
6. Before execution and merge, refresh remote state and evaluate declared impact.
   Never silently rebase a dirty worktree or resolve conflicts heuristically.
7. Tests/builds use host-wide resource gates; ten isolated worktrees must not imply
   ten unbounded test suites.
8. PR/CI/merge are typed external conditions. Dependents unblock on authoritative
   merge evidence, not “task complete” or branch push.
9. Reclamation uses Git facts: clean tree, merged/reachable head, and no required
   external operation. A stale Concord projection is reconciled but cannot override
   stronger Git truth.
10. Product memory remains visible globally while agents work. Never clone, branch,
    or copy the authoritative DB into worktrees.

## Competitor mechanism assessment

- **Beads:** strongest new source for typed dependency gates and claim/federation
  invariants. Copy the contracts, not its project-local Dolt topology.
- **LangGraph:** strongest source for checkpoint/resume, historical replay, and an
  explicit concurrent-update contract.
- **Restate/DBOS:** strongest independent validation of log-first authority,
  deterministic projections, idempotent steps, fencing, and bounded snapshots.
- **Letta MemFS:** useful proof that Git worktrees can isolate concurrent durable
  knowledge maintenance.
- **Claude Agent SDK:** useful warning that transcript, filesystem, and external
  effect checkpoints cover different facts.
- **Herdr:** useful persistent-process/live-handoff mechanism; limited state-model
  relevance to Concord's no-daemon core.
- **Orca/Superset:** corroborate agent-neutral worktree isolation and persistent
  terminals; offer no stronger Product-state authority.
- **Jido:** corroborates schema-validated, independently testable typed actions.
- **Qodo/Devin:** useful verification and provenance ideas, but public runtime/store
  details are too limited for authority decisions; Qodo CoverAgent is unmaintained.

## Sources

- Beads dependencies/gates: <https://beads.gascity.com/core-concepts/dependencies>
- Beads architecture FAQ: <https://beads.gascity.com/reference/faq>
- Beads federation/leases: <https://beads.gascity.com/multi-agent/federation>
- Dolt concurrency: <https://www.dolthub.com/blog/2026-02-17-dolt-concurrency/>
- SQLite WAL: <https://www.sqlite.org/wal.html>
- SQLite isolation: <https://www.sqlite.org/isolation.html>
- LangGraph persistence: <https://docs.langchain.com/oss/python/langgraph/persistence>
- Letta MemFS: <https://docs.letta.com/concepts/memfs>
- Restate architecture: <https://www.restate.dev/blog/building-a-modern-durable-execution-engine-from-first-principles>
- DBOS architecture: <https://docs.dbos.dev/architecture>
- Herdr concepts: <https://herdr.dev/docs/concepts/>
- Claude Agent SDK sessions: <https://code.claude.com/docs/en/agent-sdk/sessions>
- Claude Agent SDK file checkpointing: <https://code.claude.com/docs/en/agent-sdk/file-checkpointing>
- Jido typed tools: <https://jido.run/features/tools>
- Qodo CoverAgent repository: <https://github.com/qodo-ai/qodo-cover>
