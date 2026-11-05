---
{
  "id": "PM4",
  "title": "product memory lifecycle relations",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "work-coordination",
  "applies_to_domain_ids": [],
  "criterion_bindings": [
    {
      "criterion": 1,
      "exemption": "The corpus asserts derived answers, not the closed state set itself. The guarantee is proved by TestLifecycleTransitionsAreClosedAndTyped (internal/store/lifecycle_relations_test.go)."
    },
    {
      "criterion": 2,
      "exemption": "Cycle refusal is proved by TestCycleCheckRejectsSimpleTwoNodeCycle (internal/store/lifecycle_relations_test.go)."
    },
    {
      "criterion": 3,
      "exemption": "Reopen is proved by TestLifecycleTransitionsAreClosedAndTyped (internal/store/lifecycle_relations_test.go)."
    },
    {
      "criterion": 4,
      "exemption": "List readiness and blocker release are proved by TestOverlapBlocksRemovesWorkFromReady (internal/store/planning_operation_retirement_test.go)."
    }
  ]
}
---
# Concord Product-Memory Lifecycle and Relations (PM4)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM4; direct accepted authority for Concord's Product-memory lifecycle and relation semantics.
> **Binding inputs:** accepted PM1 query contract, PM2 global SQLite authority,
> PM3 hybrid explicit-core schema, and CD-0002 invariants I1–I6.
> **Research basis:** official/public
> models from Linear, Jira, Bugzilla, GitHub Issues, Plane, and Fossil. No benchmark
> or PoC is part of this decision.
> **Does not decide:** PM5 Project-membership roles/order, exact DDL/indexes, agent
> tools, workflow/gate ceremony, PM8 WIP-byte exclusion, PM9 no-receipt boundary, PM10 recovery, or
> external-system polling.
> **Planning and relations:** business grouping lives in Linear, and
> Concord stores no grouping kind or membership; architecture-overlap resolutions extend the
> work-pair grammar.
> **Storage ([CD-0242](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2):** every
> transition and relation change is one recorded current-row mutation; no event
> log or retained event history exists.
> CD-0142 governs operator-directed removal of nonterminal execution projections
> without adding a lifecycle state. The planning record remains.
> No aliases, upcasters, or compatibility windows are authorized for the pre-go-live forms.

[CD-0243 D7](../decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies CD-0142's operator-directed removal clauses. Removal remains
per-item, scoped, fenced, and subject to the existing planning handoff.

## Context

Work items need one lifecycle and relation semantics shared by every PM1
read. The binding inputs are the accepted PM1 query contract, PM2 global
authority, PM3's hybrid explicit core, and CD-0002 invariants I1 through I6.
This record decides the closed lifecycle state set, the allowed transitions,
the derived work views, and the typed relation model with its graph rules.
CD-0222 and CD-0042 extend the work-pair grammar with architecture-overlap
resolutions. CD-0213 places business grouping in Linear through the Linear
MCP server; Concord keeps no grouping projection or membership authoring.
## Contract

The binding contract is sections 1 through 6: the decision, the closed
lifecycle state set and transitions, the derived work views, the relation
model, validation and atomic operations, and structural invariants. Sections 7 through 11
record corpus fixture compatibility, rejected alternatives, deferred scope,
falsifiers, and sources, and carry no obligation.
## 1. Decision

Concord stores one small lifecycle state on each canonical work item and stores
typed directional relations between work items. Read-time properties such as
`blocked`, `ready`, `active`, and `terminal` are deterministic projections of that
state and relation graph—never independently mutable fields.

This creates one explainable lifecycle/relation answer to PM1 Q2, Q3, Q7 and Q8
without importing a gate hierarchy into Product memory or allowing stored
summary flags to drift from truth. Accepted PM5 supplies membership identity.

## 2. Lifecycle

### 2.1 Closed state set

| State | Class | Meaning |
|---|---|---|
| `needed` | nonterminal | Accepted work exists but is not actively underway. |
| `in_progress` | nonterminal | Work is actively underway. |
| `completed` | terminal | Intended outcome was delivered. |
| `cancelled` | terminal | Work intentionally ended without delivery. |

Upgrade maps previously superseded work to `cancelled` and retains the verified
snapshot under the existing upgrade admission rule.

`blocked`, `ready`, `active`, and `terminal` are not lifecycle states. No independent
`is_blocked`, `is_ready`, or `is_terminal` column exists.

CD-0222 and CD-0213 fix one ordinary work-item kind without adding
lifecycle states:

- `research`: independently trackable investigation that may conclude `no change`.

Embedded research is not another work item. A historical work item of kind `initiative` stays
readable with no grouping behavior, and capture refuses a new one under CD-0213 D8.

### 2.2 Allowed transitions

```text
needed      → in_progress | completed | cancelled
in_progress → needed | completed | cancelled
completed   → needed       (explicit reopen)
cancelled   → needed       (explicit reopen)
```

- Every transition is one recorded current-row mutation in one
  SQLite transaction (CD-0002 I4;
  [CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- Reopen is an explicit recorded transition with actor, reason, expected version, and prior
  terminal state. It keeps the work identity and refuses if another live item holds
  the same Linear issue key or UUID.
- In the same transaction, reopen clears an existing workflow instance's terminal
  completion metadata. An instance with a plan returns to `running` and keeps its
  plan and prelude state, allowing the agent to insert the next step. An instance
  without a plan returns to `intake` and clears its prelude state so the agent can
  record intake again. Neither case requires a new capture.
- `in_progress → needed` means intentionally paused/returned to the queue; it is
  not a hidden blocked state.
- Terminal reason/evidence fields required by the accepted operation commit in the
  same transaction. A partially terminal record is unrepresentable.
- Workflow-specific gates, approvals, or phases may constrain when an operation is
  accepted, but they do not create additional Product-memory lifecycle states.

CD-0142 defines a separate operator-directed removal operation for nonterminal
execution. It preserves the planning handoff, then removes the WorkItem and its
owned execution projections in one recorded mutation. The
removal reason is `shelved` or `cancelled`; shelving never emits cancellation.
The removed identity is audit-only and cannot reopen. Renewed work receives a
new identity from the surviving planning record.

## 3. Derived work views

For work item `W`:

- `actionable(W)` iff its work kind is not retired from execution;
- `terminal(W)` iff lifecycle is `completed` or `cancelled`;
- `active(W)` iff `actionable(W)` and lifecycle is `in_progress`;
- `blocked(W)` iff `actionable(W)` and at least one actionable nonterminal work
  item has a stored `blocks` edge to `W`;
- `ready(W)` iff `actionable(W)`, lifecycle is `needed`, and `blocked(W)` is false.

The list `work_summary.ready` is true exactly when lifecycle is `needed` and no
live blocker holds the item. A needed item with `ready` absent or false is blocked;
the relations read supplies its blocker graph. No public `blocked` flag exists.

CD-0213 retires the `initiative` kind from execution. Its historical lifecycle
and record remain readable, but actionable views do not select it.

A terminal blocker resolves its outgoing blocking effect regardless of which
terminal state it reached. Under CD-0213 D8, a blocker of a kind retired from
execution has no blocking effect. Its stored edge stays readable as history.
Parent relations do not silently block children or auto-transition parents.
Any progress roll-up is a derived read model.

PM1 defines deterministic ordering. Lifecycle stage does not imply business priority.

## 4. Relation model

One typed relation structure stores each semantic edge once. Direction and inverse
read names are part of the contract; callers never create mirrored rows.

| Stored kind/direction | Inverse read | Meaning | Structural rule |
|---|---|---|---|
| `parent`: A parent-of B | B child-of A | Work hierarchy/containment | no self-edge, duplicate, or cycle |
| `blocks`: A blocks B | B depends-on A (`blocked-by` display alias) | B is not ready while A is nonterminal | no self-edge, duplicate, or cycle |
| `implements`: A implements B | B implemented-by A | A fulfills another work item | no self-edge or duplicate; no lifecycle effect |
| `raised_from`: B raised-from A | A raised B | B was raised while working on A | no self-edge, duplicate, or cycle; non-blocking |

The `raised_from` relation carries a nonempty reason, records provenance only, and asserts no
dependency, hierarchy, or replacement. It does not bypass Linear issue collision
admission and has no removal operation. It does not create an assignment, claim,
lease, executor, or per-work-item concurrency fence.

`implements` is a non-governing fulfillment link. It does not determine readiness,
terminality, or hierarchy, and imposes no transitive-closure or cycle rule.

`relates_to` is excluded because no accepted PM1 job requires an untyped symmetric
link. Add it only through a future demonstrated query need.

## 5. Validation and atomic operations

### 5.1 Graph validity

Before inserting `parent`, `blocks`, or `raised_from`, Concord evaluates the
**resulting graph** inside the same transaction. A cycle-creating operation fails
closed with typed `invariant_violation`; no row commits.

Evaluating the resulting graph—not only the incremental edge—allows an accepted
operation to reverse a dependency without falsely detecting the outgoing edge that
the same transaction removes. Exact closure-query/index strategy is implementation
acceptance work at Concord's measured scale.

## 6. Structural invariants

1. **One lifecycle truth:** every work item has exactly one state from §2.1.
2. **Derived status only:** blocked/ready/active/terminal are pure projections of
   lifecycle and relations, never independent mutation inputs.
3. **One edge, two reads:** inverse relation labels never create mirrored rows.
4. **Acyclic governing graphs:** parent, `blocks`, and `raised_from` cycles are
   rejected before commit.
5. **Explainability:** every lifecycle/edge change is one recorded mutation with
   actor, reason, expected version, and resulting version
   ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
7. **No partial terminality:** terminal state and required terminal metadata commit
   together or not at all.

## 7. PM1 corpus fixture compatibility

PM1's scenario corpus intentionally used provisional PM4 vocabulary. An adapter
normalizes it as follows:

- fixture `depends_on` with `source=A, target=B` becomes the canonical stored edge
  `blocks` with `source=B, target=A`—the direction is inverted;
- fixture `resolved` is compatibility input only. No resolved flag is stored on an
  edge; resolution is derived from the blocking work item's terminal lifecycle.

This normalization belongs in the scenario adapter and must be tested explicitly.
A caller using the accepted PM4 surface reads `depends_on` as an inverse view, not
as a mutation kind.

## 8. Alternatives rejected

### Stored `blocked` lifecycle or flag

Rejected. It duplicates relation truth, can disagree with the graph, and directly
violates the derived readiness rule. Every surveyed mature work system models blocking as a
relation rather than a lifecycle status.

### Separate `blocks` and `depends_on` rows

Rejected. They are directional reads of one relation; storing both creates a
reconciliation invariant with no additional meaning.

### Workflow-specific lifecycle states

Rejected for Product memory. Gates, approvals, review phases, and release steps
belong to purpose-built workflow execution, not the canonical cross-work query model.

### Polymorphic external relation endpoints

Rejected. They cannot receive ordinary SQLite foreign-key enforcement and would
violate PM3's typed-edge rule.

## 9. Scope deliberately deferred

PM4 does not decide:

- membership roles/cardinality, Project removal/move behavior, and Product scope—
  now settled by accepted PM5;
- whether Product membership limits which same-Product relation operations are
  authorized (PM5/TS5);
- exact DDL, indexes, closure algorithm, terminal metadata columns, or event names;
- work→git-spec fulfillment links; `implements` is work→work only here, while git
  knowledge remains an `external_ref` until another accepted model provides a typed
  target;
- PM10 recovery shape or external-system observation/polling;
- agent tool names, schemas, or transport.

## 10. Falsifiers

Reopen PM4 if:

1. derived readiness reads cannot meet PM1's bounded P99 target at 10× scale;
2. repeated real work requires a soft/hard dependency distinction that cannot be a
   typed relation attribute;
3. external blockers repeatedly fail the work-item model;
4. `implements` repeatedly needs a non-work typed target;
5. an accepted PM1 job requires a lifecycle state or relation absent here.

## 11. Primary sources

- Linear issue relations: https://linear.app/docs/issue-relations
- Linear workflow statuses: https://linear.app/docs/configuring-workflows
- Jira issue linking: https://confluence.atlassian.com/spaces/ADMINJIRASERVER/pages/938847862/Configuring+issue+linking
- Bugzilla issue fields/dependencies: https://bz.apache.org/bugzilla/docs/en/html/using/understanding.html
- Bugzilla workflow: https://bugzilla.readthedocs.io/en/stable/administering/workflow.html
- GitHub Issues: https://docs.github.com/en/issues/tracking-your-work-with-issues/about-issues
- Plane work-item relations/properties: https://docs.plane.so/core-concepts/issues/overview
- Plane workflow states: https://docs.plane.so/core-concepts/issues/states
- Fossil ticket model: https://fossil-scm.org/doc/tip/www/tickets.wiki

External models are comparison evidence, not authority for Concord. PM1's accepted
jobs/oracles, CD-0002 invariants, and the falsifiers above remain controlling.

## Acceptance criteria

- Given any work item
  When its lifecycle is read
  Then its state is one of the closed set of four, with blocked, ready,
  active, and terminal derived — never stored.

- Given a relation insertion that would create a cycle
  When the operation is attempted
  Then it fails closed with typed invariant_violation and no record commits.

- Given a completed or cancelled item whose Linear issue has no other live holder
  When it is reopened with a reason
  Then it returns to needed.

- Given needed work held by a live blocker
  When Q3 lists that work
  Then its ready flag is false and the relation read identifies the blocker.

## Verification

The criteria carry typed exemptions naming the tests that prove the guarantees.

- Criterion 1 is proved by `TestLifecycleTransitionsAreClosedAndTyped`
  (`internal/store/lifecycle_relations_test.go`).
- Criterion 2 is proved by `TestCycleCheckRejectsSimpleTwoNodeCycle`
  (`internal/store/lifecycle_relations_test.go`).
- Criterion 3 is proved by `TestLifecycleTransitionsAreClosedAndTyped` (`internal/store/lifecycle_relations_test.go`).
- Criterion 4 is proved by `TestOverlapBlocksRemovesWorkFromReady`
  (`internal/store/planning_operation_retirement_test.go`). Section 10 records
  the falsifiers for each guarantee.
