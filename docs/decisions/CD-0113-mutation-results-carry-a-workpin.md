# CD-0113: Mutation results carry a WorkPin

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** The mutation result envelope of the agent tool surface, the read
  projections that carry the same state, and the derivation of
  `next_valid_intents`
- **Approval:** The operator approved this decision for issue #857 on work item
  `work-201ad0df0700db9255d3f256`.
- **Related:** CD-0005, CD-0027, CD-0030, CD-0039, CD-0096
- **Amends:** None

## Context

Concord's typed surface mixes three concurrency regimes. A work item carries an
optimistic-concurrency version. A workflow instance carries a step state
machine. A worker attempt carries an epoch. Every mutation demands a pin that
combines all three, and the surface hands none of them back.

Measurement over one project shard (`agent=concord-1`, about 12,987 tool calls)
shows the consequence. Sessions ran 345 raw `sqlite3` reads against the
authority database: `work_items` 130 times (mostly `SELECT version`),
`domain_events` 109, `workflow_instances` 38, `worker_attempts` 38, and contract
or actor tables 45. The session that holds the authority reads the store around
the surface, and the version pin that exists to make mutations safe is read from
an unversioned side channel.

The same defect appears from the other side. `workflow_action` ran 566 times
with 226 refusals (40 percent). The leading refusal classes are shape and state
guesses the response could have prevented: `oneOf mismatch at $.fields` (20),
action not declared on the current step (11), `dispatch_worker requires
fields.lane_id` (8), attempt epoch mismatch (7), worker attempt does not exist
(5).

`mutation_result.next_valid_intents` is contractually required but populated
only by hand-written static literals in `internal/agent/mutations.go` (more
than 20 sites). It is empty for `workflow_action`, the action that runs most.
The step definition knows which actions the step declares, which fields each
requires, and which attempt is live. It tells the session none of that.

The read surface scatters the same facts. The version is readable through
`concord_work_browse.list`. The step is readable through
`concord_work_trace.continuity`. Worker attempts are readable through no typed
operation.

## Decision

### D1. One projection

`internal/store` defines a `WorkPin`: `work_id`, `version`, `lifecycle`,
`workflow_type`, `step`, `attempt` (id, epoch, lane, state, or null),
`pending_operator_decision`, and `watermark`. One tx-scoped reader derives it.
The store connection invariant applies: no `s.db`-backed call runs inside the
transaction.

### D2. Every mutation result carries the pin

Every mutation result that lists a `work_item` in `changed_refs` carries the
pin computed inside the mutation's own transaction, so it is the post-state and
cannot disagree with the committed write. The generic `changed_refs` version
stays; the pin is the typed form.

### D3. Intents are derived, not authored

`next_valid_intents` is derived from the pin and the step definition: one
intent per action the current step declares, each carrying `tool`,
`operation`, `action_id`, `required_fields`, and `expected_version`. The
hand-written `plan.intents` literals for work mutations are removed. Read
verifications that follow a write keep their intents.

### D4. Reads embed the same pin

`concord_work_trace.continuity` and `concord_work_browse.list` with
`detail: full` embed the pin. `WorkflowStatus` in
`internal/store/workflow_continuity.go` is a partial precursor of the pin and
is removed.

### D5. Structural tests

A test proves every mutation result with a `work_item` changed ref carries a
pin whose version equals that ref. A test proves every emitted intent names an
action the pin's step declares.

## Acceptance Criteria

```gherkin
Scenario: a mutation returns its post-state
  Given a work item at version N on step S
  When a mutation names that work item
  Then the result carries a WorkPin with version N+1 and step S
  And the pin is computed inside the mutation transaction

Scenario: a session learns its next actions
  Given a work item whose step declares actions A1 and A2
  When any mutation or read returns the pin
  Then next_valid_intents lists A1 and A2
  And each intent names its required fields and expected version

Scenario: a session never reads the store around the surface
  Given a driving session preparing a workflow action
  When it needs the version, the step, or the attempt state
  Then one typed read returns all three
  And no refusal of a typed operation names reading the database as recovery

Scenario: intents stay honest
  Given any emitted next_valid_intent
  When the pin's step definition is read
  Then the step declares the intent's action
```

## Verification

- Store tests derive the pin in one read-only transaction and prove the
  version equals the committed `changed_refs` version.
- Runtime tests prove every work mutation envelope carries the pin and every
  intent names a declared step action.
- The generated payload schemas, the TS7 contracts, and the adapter are
  regenerated; the manifest digest changes once, in the same change.
- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-knowledge-index.py` pass.

## Out of scope

- The refusal class `core response failed the generated TS7 contract` (31
  occurrences in the same shard): the core failing its own generated schema is
  a separate defect and carries its own issue.
- The overlap gate approval volume tracked by issue #845.
