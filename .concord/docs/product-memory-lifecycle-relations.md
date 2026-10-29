# Concord Product-Memory Lifecycle and Relations (PM4)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-05.
> **Decision:** PM4; direct accepted authority for Concord's Product-memory lifecycle and relation semantics.
> **Binding inputs:** accepted PM1 query contract, PM2 global SQLite authority,
> PM3 hybrid explicit-core schema, and CD-0002 invariants I1–I6.
> **Research basis:** public predecessor lessons plus official/public
> models from Linear, Jira, Bugzilla, GitHub Issues, Plane, and Fossil. No benchmark
> or PoC is part of this decision.
> **Does not decide:** PM5 Project-membership roles/order, exact DDL/indexes, agent
> tools, workflow/gate ceremony, PM8 WIP-byte exclusion, PM9 no-receipt boundary, PM10 recovery, or
> external-system polling.
> **Planning and relations:** business grouping lives in Linear, and
> Concord stores no grouping kind or membership; architecture-overlap resolutions extend the
> work-pair grammar.
> **Storage ([CD-0242](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2):** every
> transition and relation change is one recorded current-row mutation; no event
> log or retained event history exists.
> CD-0142 governs operator-directed removal of nonterminal execution projections
> without adding a lifecycle state. The planning record remains.
> No aliases, upcasters, or compatibility windows are authorized for the pre-go-live forms.

[CD-0243 D7](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
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
model, the validation and atomic operations including supersession and
external blockers, and the structural invariants. Sections 7 through 11
record corpus fixture compatibility, rejected alternatives, deferred scope,
falsifiers, and sources, and carry no obligation.
## 1. Decision

Concord stores one small lifecycle state on each canonical work item and stores
typed directional relations between work items. Read-time properties such as
`blocked`, `ready`, `active`, and `terminal` are deterministic projections of that
state and relation graph—never independently mutable fields.

This creates one explainable lifecycle/relation answer to PM1 Q2–Q5 and Q7–Q8
without importing Advance's gate hierarchy into Product memory or allowing stored
summary flags to drift from truth. Accepted PM5 now supplies Q6 membership identity.

## 2. Lifecycle

### 2.1 Closed state set

| State | Class | Meaning |
|---|---|---|
| `needed` | nonterminal | Accepted work exists but is not actively underway. |
| `in_progress` | nonterminal | Work is actively underway. |
| `completed` | terminal | Intended outcome was delivered. |
| `cancelled` | terminal | Work intentionally ended without delivery. |
| `superseded` | terminal | Another canonical work item replaced this item. |

`blocked`, `ready`, `active`, and `terminal` are not lifecycle states. No independent
`is_blocked`, `is_ready`, or `is_terminal` column exists.

CD-0215 D2 with CD-0222 and CD-0213 fixes one ordinary work-item kind without adding
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
needed | in_progress | completed | cancelled
            → superseded   (only through the atomic supersession operation)
superseded  → needed       (only after removing/replacing its supersession edge)
```

- Every transition is one recorded current-row mutation in one
  SQLite transaction (CD-0002 I4;
  [CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- Reopen is an explicit recorded transition with actor, reason, expected version, and prior
  terminal state. It never edits or deletes the terminal record it reopens.
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
- `terminal(W)` iff lifecycle is `completed`, `cancelled`, or `superseded`;
- `active(W)` iff `actionable(W)` and lifecycle is `in_progress`;
- `blocked(W)` iff `actionable(W)` and at least one actionable nonterminal work
  item has a stored `blocks` edge to `W`;
- `ready(W)` iff `actionable(W)`, lifecycle is `needed`, and `blocked(W)` is false.

CD-0213 retires the `initiative` kind from execution. Its historical lifecycle
and record remain readable, but actionable views do not select it.

A terminal blocker resolves its outgoing blocking effect regardless of which
terminal state it reached. History still explains whether it completed, was
cancelled, or was superseded. Under CD-0213 D8, a blocker of a kind retired from
execution has no blocking effect. Its stored edge stays readable as history.
Parent relations do not silently block children or auto-transition parents.
Any progress roll-up is a derived read model.

PM1 ranking remains: explicit priority, creation time, stable ID. Lifecycle stage
is displayed but does not silently rewrite business priority.

## 4. Relation model

One typed relation structure stores each semantic edge once. Direction and inverse
read names are part of the contract; callers never create mirrored rows.

| Stored kind/direction | Inverse read | Meaning | Structural rule |
|---|---|---|---|
| `parent`: A parent-of B | B child-of A | Work hierarchy/containment | no self-edge, duplicate, or cycle |
| `blocks`: A blocks B | B depends-on A (`blocked-by` display alias) | B is not ready while A is nonterminal | no self-edge, duplicate, or cycle |
| `supersedes`: A supersedes B | B superseded-by A | A is B's canonical replacement | no self-edge, duplicate, cycle, or second direct successor for B |
| `implements`: A implements B | B implemented-by A | A fulfills another work item | no self-edge or duplicate; no lifecycle effect |

CD-0222's work-pair grammar adds version-pinned `merged_into` and
architecture-sequencing use of `blocks`/`depends_on`/`supersedes`; its
implementation issue must update this table, schemas, records, and conformance in
one major rather than partially accepting new relation strings.

`depends_on` is not a second stored relation kind. “A depends on B” is stored once
as `B blocks A`. This removes mirrored-edge reconciliation and follows the relation
models used by Jira, Bugzilla, Linear, Plane, and GitHub Issues.

`implements` is a non-governing fulfillment link: it does not determine readiness,
terminality, or hierarchy, so PM4 imposes no transitive-closure/cycle rule on it.

PM4 supersession is a work-item relation only. It does not imply a generic
Product, Project, resource, or other entity migration model.

`relates_to` is excluded because no accepted PM1 job requires an untyped symmetric
link. Add it only through a future demonstrated query need.

## 5. Validation and atomic operations

### 5.1 Graph validity

Before inserting `parent`, `blocks`, or `supersedes`, Concord evaluates the
**resulting graph** inside the same transaction. A cycle-creating operation fails
closed with typed `invariant_violation`; no row commits.

Evaluating the resulting graph—not only the incremental edge—allows an accepted
operation to reverse a dependency without falsely detecting the outgoing edge that
the same transaction removes. Exact closure-query/index strategy is implementation
acceptance work at Concord's measured scale.

### 5.2 Supersession

`supersede(successor=A, predecessor=B)` is one domain operation:

1. validate same Product scope, expected versions, edge uniqueness, and acyclicity;
2. record the supersession edge and B's terminal transition in one transaction;
3. commit the edge and `B.lifecycle_state = superseded` atomically;
4. return A as B's canonical direct successor.

Following a chain yields the current canonical successor. A predecessor has at most
one direct successor, while one successor may replace multiple predecessors.

Reopening B requires an explicit operation that first removes/replaces the active
supersession relation and then transitions B to `needed` in the same transaction.
Directly changing `superseded → needed` is invalid.

This work-item operation is distinct from the managed-resource identity and
attachment model in `product-data-model.md` §9. PM4 does not extend its
supersession semantics to resources or other entity types.

## 6. Structural invariants

1. **One lifecycle truth:** every work item has exactly one state from §2.1.
2. **Derived status only:** blocked/ready/active/terminal are pure projections of
   lifecycle and relations, never independent mutation inputs.
3. **One edge, two reads:** inverse relation labels never create mirrored rows.
4. **Atomic supersession:** successor edge and predecessor terminal state cannot
   disagree at a committed version.
5. **Acyclic governing graphs:** parent, `blocks`, and supersession cycles are
   rejected before commit.
6. **Explainability:** every lifecycle/edge change is one recorded mutation with
   actor, reason, expected version, and resulting version
   ([CD-0242 D2](decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
7. **No partial terminality:** terminal state and required terminal metadata commit
   together or not at all.

## 7. PM1 corpus fixture compatibility

PM1's scenario corpus intentionally used provisional PM4 vocabulary. An adapter
normalizes it as follows:

- fixture `depends_on` with `source=A, target=B` becomes the canonical stored edge
  `blocks` with `source=B, target=A`—the direction is inverted;
- fixture `supersedes` already uses canonical direction (`source=successor`,
  `target=predecessor`) and is not inverted;
- fixture `resolved` is compatibility input only. No resolved flag is stored on an
  edge; resolution is derived from the blocking work item's terminal lifecycle.

This normalization belongs in the scenario adapter and must be tested explicitly.
A caller using the accepted PM4 surface reads `depends_on` as an inverse view, not
as a mutation kind.

## 8. Alternatives rejected

### Stored `blocked` lifecycle or flag

Rejected. It duplicates relation truth, can disagree with the graph, and directly
violates PM1 Q4's oracle. Every surveyed mature work system models blocking as a
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

1. derived blocked/ready queries cannot meet PM1's bounded P99 target at 10× scale;
2. repeated real work requires a soft/hard dependency distinction that cannot be a
   typed relation attribute;
3. external blockers repeatedly fail the work-item model;
4. canonical supersession legitimately requires multiple simultaneous direct
   successors;
5. `implements` repeatedly needs a non-work typed target;
6. reopen-from-superseded is frequent enough that relation replacement is the wrong
   primary operation;
7. an accepted PM1 job requires a lifecycle state or relation absent here.

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
  Then its state is one of the closed set of five, with blocked, ready,
  active, and terminal derived — never stored.

- Given a relation insertion that would create a cycle
  When the operation is attempted
  Then it fails closed with typed invariant_violation and no record commits.

- Given a valid supersession of predecessor by successor
  When the supersede operation commits
  Then the edge and the predecessor's superseded state commit atomically as
  one domain operation.

- Given superseded work
  When it is reopened
  Then the reopen removes or replaces the supersession edge and transitions
  the state in one recorded operation, never a direct superseded-to-needed edit.

- Given ready work ranked against a blocked higher-priority item
  When Q5 selects work
  Then readiness is the derived needed-and-unblocked projection and the
  ranking stays deterministic.

## Verification

Criteria 2 and 5 are proved by scenarios of
`.concord/scenarios/product-memory-query.v1.json`, executed by
`TestAcceptedQ1ToQ10Corpus` (`internal/store/query_corpus_test.go`). The
remainder carry typed exemptions in the record naming the lifecycle tests
that prove the guarantees.

- Criterion 1 is proved by `TestLifecycleTransitionsAreClosedAndTyped`
  (`internal/store/lifecycle_relations_test.go`).
- Criterion 2 is proved by the bound `Q4-blocked-reason` scenario, whose
  blocked answer is derived from the relation graph.
- Criterion 3 is proved by
  `TestSupersessionIsAtomicAndRejectsCyclesAndSecondSuccessors`
  (`internal/store/lifecycle_relations_test.go`).
- Criterion 4 is proved by
  `TestSupersessionRejectsAlreadySupersededAndReopenRequiresCompositeEvent`
  (`internal/store/lifecycle_relations_test.go`).
- Criterion 5 is proved by the bound `Q5-ready-ranking` scenario. Section 10
  records the falsifiers for each guarantee.
