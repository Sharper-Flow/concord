# CD-0113: Mutation results carry a WorkPin

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** The mutation result envelope of the agent tool surface, the read
  projections that carry the same state, and the derivation of
  `next_valid_intents`
- **Approval:** The operator approved this decision for issue #857 on work item
  `work-201ad0df0700db9255d3f256`.
- **Related:** CD-0005, CD-0027, CD-0030, CD-0039, CD-0096, CD-0173,
  CD-0201, CD-0209

## Context

Concord mutations need the work version and current workflow state. A response
that omits those facts forces another read or an unsafe guess.

`mutation_result.next_valid_intents` is contractually required but populated
only by hand-written static literals in `internal/agent/mutations.go` (more
than 20 sites). It is empty for `workflow_action`, the action that runs most.
The step definition knows which actions the step declares, which fields each
requires, and which attempt is live. It tells the session none of that.

The read surface scatters the same facts. The version is readable through
`concord_work_browse.list`. The step is readable through
`concord_work_trace.continuity`.

## Decision

### D1. One projection

`internal/store` defines a `WorkPin`: `work_id`, `version`, `lifecycle`,
`workflow_type`, `step`,
`pending_operator_decision`, and `watermark`. One tx-scoped reader derives it.
The store connection invariant applies: no `s.db`-backed call runs inside the
transaction.

### D2. Every mutation result carries the pin

Every mutation result that lists a `work_item` in `changed_refs` carries the
pin computed inside the mutation's own transaction, so it is the post-state and
cannot disagree with the committed write. Two kinds of work item have no pin:
one that has no workflow instance and no held outside-repair disposition, and
one that the mutation removed. The result lists such an item without a pin. A
result that lists any such item carries exactly one `work_pin_unavailable`
notice in the envelope's `omissions`, whose `count` is the number of them. The
notice carries no `source_id` and no `details` that name items, because the
pinless items are exactly the `work_item` changed refs that `work_pins` omits.
When the pin of any other listed work item cannot be read inside the
transaction, the mutation refuses. The generic `changed_refs` version stays;
the pin is the typed form.

Complete WorkPins remain inline when the final serialized envelope fits the
51,200-byte limit. When it does not fit, the producer may use the closed
projection reference and immutable transaction document required by
[TS7 §8.2](../agent-result-envelope.md#82-lossless-oversized-result-transport).
The reference is transport, not a third pinless case; changed references and
truthful effect metadata remain in the mutation envelope.

### D3. Intents are derived, not authored

`next_valid_intents` is derived from the pin and the workflow admission: one
intent per action the engine admits at the pin's state, each carrying `tool`,
`operation`, `action_id`, `required_fields`, and `expected_version`. The
admitted actions are the current step's declared actions and the engine-owned
recovery actions that admission admits. An action that admission holds only
behind the operator's approval or the fresh review its own acceptance supplies
counts as admitted, so the pin names that route
([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host)).
A result that carries a pin takes its intents from the pin
alone, so no hand-written intent reaches it. A result without a pin keeps the
intents its operation writes, if any: reads that verify the write, or
research-pack mutations, never a work mutation.

### D4. Reads embed the same pin

`concord_work_trace.continuity` and `concord_work_browse.list` with
`detail: full` embed the pin. When the final serialized envelope does not fit,
[TS7 §8.2](../agent-result-envelope.md#82-lossless-oversized-result-transport)
permits a closed projection reference and lossless reconstruction instead.

### D5. Structural tests

A test proves every mutation result carries the pin of each `work_item` changed
ref that has one, at a version equal to that ref, counts the refs without one
in a single `work_pin_unavailable` notice, and refuses when such a pin cannot
be read. A test proves every emitted intent names an action the engine admits
at the pin's state.

Tests prove both inline and reconstructed results meet the same completeness,
version, derivation and schema obligations under
[TS7 §8.2](../agent-result-envelope.md#82-lossless-oversized-result-transport).

## Acceptance Criteria

```gherkin
Scenario: a mutation returns its post-state
  Given a work item at version N on step S
  When a mutation names that work item
  Then the result carries a WorkPin with version N+1 and step S
  And the pin is computed inside the mutation transaction

Scenario: a session learns its next actions
  Given a work item at a state where the engine admits actions A1 and A2
  When any mutation or read returns the pin
  Then next_valid_intents lists A1 and A2
  And each intent names its required fields and expected version

Scenario: a session never reads the store around the surface
  Given a driving session preparing a workflow action
   When it needs the version and current step state
   Then one typed read returns both
  And no refusal of a typed operation names reading the database as recovery

Scenario: intents stay honest
  Given any emitted next_valid_intent
  When the workflow admission is evaluated at the pin's state
  Then the engine admits the intent's action
```

## Verification

- Store tests derive the pin in one read-only transaction and prove the
  version equals the committed `changed_refs` version.
- Runtime tests prove every work mutation envelope carries the pin of each
  listed work item that has one and one `work_pin_unavailable` notice that
  counts those that have none, the mutation refuses when that pin cannot be
  read, and every intent names an admitted action.
- The generated payload schemas, the TS7 contracts, and the adapter are
  regenerated; the manifest digest changes once, in the same change.
- `python3 scripts/check-knowledge-index.py` passes.

## Out of scope

- The refusal class `core response failed the generated TS7 contract` (31
  occurrences in the same shard): the core failing its own generated schema is
  a separate defect and carries its own issue.
- The overlap gate approval volume tracked by issue #845.
