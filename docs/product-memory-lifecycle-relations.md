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

CD-0009 fixes two ordinary work-item kinds without adding lifecycle states:

- `epic`: finite single-Product initiative; non-Epic child entries use `parent`,
  retain independent workflows/recovery, and project bounded
  `epic_entries(epic_work_id, child_work_id, position, required)` metadata;
- `research`: independently trackable investigation that may conclude `no change`.

New Epic entries default to `required=true`; optionality is explicit. An Epic cannot
complete while any required child or typed external condition remains nonterminal.
Removing an entry atomically removes its `parent` edge/order metadata without
cancelling the child. Embedded research is not another work item.

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

- Every transition is an append to `domain_events` plus projection update in one
  SQLite transaction (CD-0002 I4).
- Reopen is an explicit event with actor, reason, expected version, and prior
  terminal state. It never edits or deletes terminal history.
- `in_progress → needed` means intentionally paused/returned to the queue; it is
  not a hidden blocked state.
- Terminal reason/evidence fields required by the accepted operation commit in the
  same transaction. A partially terminal record is unrepresentable.
- Workflow-specific gates, approvals, or phases may constrain when an operation is
  accepted, but they do not create additional Product-memory lifecycle states.

## 3. Derived work views

For work item `W`:

- `terminal(W)` iff lifecycle is `completed`, `cancelled`, or `superseded`;
- `active(W)` iff lifecycle is `in_progress`;
- `blocked(W)` iff at least one nonterminal work item has a stored `blocks` edge to
  `W`;
- `ready(W)` iff lifecycle is `needed` and `blocked(W)` is false.

A terminal blocker resolves its outgoing blocking effect regardless of which
terminal state it reached. History still explains whether it completed, was
cancelled, or was superseded. Parent relations do not silently block children or
auto-transition parents. Any progress roll-up is a derived read model.

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

`depends_on` is not a second stored relation kind. “A depends on B” is stored once
as `B blocks A`. This removes mirrored-edge reconciliation and follows the relation
models used by Jira, Bugzilla, Linear, Plane, and GitHub Issues.

`implements` is a non-governing fulfillment link: it does not determine readiness,
terminality, or hierarchy, so PM4 imposes no transitive-closure/cycle rule on it.

`relates_to` is excluded because no accepted PM1 job requires an untyped symmetric
link. Add it only through a future demonstrated query need.

## 5. Validation and atomic operations

### 5.1 Graph validity

Before inserting `parent`, `blocks`, or `supersedes`, Concord evaluates the
**resulting graph** inside the same transaction. A cycle-creating operation fails
closed with typed `invariant_violation`; no event or projection row commits.

Evaluating the resulting graph—not only the incremental edge—allows an accepted
operation to reverse a dependency without falsely detecting the outgoing edge that
the same transaction removes. Exact closure-query/index strategy is implementation
acceptance work at Concord's measured scale.

### 5.2 Supersession

`supersede(successor=A, predecessor=B)` is one domain operation:

1. validate same Product scope, expected versions, edge uniqueness, and acyclicity;
2. append the supersession relation event and B's lifecycle transition event;
3. project the edge and `B.lifecycle_state = superseded` atomically;
4. return A as B's canonical direct successor.

Following a chain yields the current canonical successor. A predecessor has at most
one direct successor, while one successor may replace multiple predecessors.

Reopening B requires an explicit operation that first removes/replaces the active
supersession relation and then transitions B to `needed` in the same transaction.
Directly changing `superseded → needed` is invalid.

This work-item operation is distinct from managed-resource replacement in
`product-data-model.md` §10, whose declared/building/coexisting/cutover/retired
states model migration between repos, infrastructure, or services. PM4 does not
collapse that resource lifecycle into work-item supersession.

### 5.3 External blockers

An external dependency is represented as a canonical `work_item` with kind
`external_blocker`. CD-0008 D5 amends its typed `external_ref` into a closed condition
carrying `await_type`, `await_ref`, `resolution_authority`, resolution evidence, and
the resolving lifecycle event. Supported initial conditions are PR merge, CI result,
timer, human approval, and remote-work state. It participates in the same FK-enforced
`blocks` relation as every other blocker.

- External state is not silently treated as Concord authority.
- Resolution is an accepted Concord lifecycle event backed by the applicable
  evidence/actor contract.
- Conditions are evaluated only on explicit request or consequential boundaries;
  no polling/timer daemon or heuristic resolution is introduced.
- No polymorphic relation endpoint points directly to a URL or provider object.
- If real usage proves external blockers are not work, PM4 may be reopened for a
  separate typed external-blocker projection; the derivation rule remains uniform.

## 6. Structural invariants

1. **One lifecycle truth:** every work item has exactly one state from §2.1.
2. **Derived status only:** blocked/ready/active/terminal are pure projections of
   lifecycle and relations, never independent mutation inputs.
3. **One edge, two reads:** inverse relation labels never create mirrored rows.
4. **Atomic supersession:** successor edge and predecessor terminal state cannot
   disagree at a committed version.
5. **Acyclic governing graphs:** parent, `blocks`, and supersession cycles are
   rejected before commit.
6. **Explainability:** every lifecycle/edge change has an ordered `domain_events`
   record with actor, reason, expected version, and resulting version.
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
- PM7's accepted post-prune immutability boundary: before pruning, PM4 reopen rules
  apply; after `work_projection_pruned`, renewed need creates a new work ID through
  an `archived_work_linked` event and `archived_work_links` read model. This is not a
  PM4 live `relations` row and cannot use an archived ID as an FK endpoint;
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
