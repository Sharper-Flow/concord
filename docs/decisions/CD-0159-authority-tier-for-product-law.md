# CD-0159: Authority tier for Product law

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The standing of a Product law record, and the approval cost of
  revising one
- **Approval:** The operator approved the contract for `work-c089308b507cfac61f7dd228`
  on 2026-09-20, then accepted D1 to D5 below on the same date.
- **Related:** CD-0006, CD-0036, CD-0041, and `docs/specs-as-laws.md`
- **Amends:** CD-0006's rule that human approval is always required to enact a
  law change
- **Preserves:** CD-0006's ban on a silent scope cut, CD-0012's rule against
  outcome substitution, CD-0036 revision identity and breaking-law cutovers, and
  CD-0041 Domain-bound contracts

## Context

Concord records no difference between law the operator legislated and law an
agent wrote while it delivered a change. Both cost one operator approval turn to
revise. A layout note from a past change therefore obstructs the next request at
the price of a real commitment.

CD-0041 names the deliverable a pruned statement of what the Product must keep
true. No rule states what may enter that statement, and no field records how it
got there.

The predecessor demonstrates the cost. In one Product migrated from Advance, a
single page specification reached version 1.39.0 with 61 requirements. Its Git
history holds more than 12 commits that rewrote it, and its change archive holds
about 20 layout changes to that one page in two months. Three of the 61
requirements fix the position of a control, the order of five sections, and the
default tab of a strip. Each records what one change built. Each now binds every
later change.

Concord enforces harder than the predecessor did. CD-0036 pins each law by
content hash. CD-0041 binds it to a home Domain. The checks rerun in a
transaction at every consequential action. Admitting a build transcript as law
therefore costs more under Concord than under the predecessor.

The record schema has room for the fix and no record to migrate.
`.concord/schemas/knowledge-record.v1.schema.json` requires a `provenance`
object with `canonical_path`, `source_revision`, `source_digest`, `authored_at`,
`author`, and `location_authority`. The `author` field is free short text. The
`location_authority` field selects `canonical` or `operator_override`, which the
typed knowledge draft calls a placement exception and not a second authority.
The `status.state` field selects `current`, `historical`, or `superseded`, which
is lifecycle. No field records standing. A glob of `.concord/knowledge` returns
zero typed records, so a schema change costs no migration today.

## Decision

### D1. A Product law record carries an authority tier

Each law record carries one of two tier values.

- `legislated` — the operator approved this statement as law in its own right.
- `derived` — an agent authored this statement while it delivered a change, and
  the statement describes what the change built.

The tier is a recorded fact about the write. It is not a judgement about the
content. An admission test that asks whether a statement is a genuine Product
obligation is a heuristic, and CD-0041 with `docs/specs-as-laws.md` refuses a
heuristic as sole authority for correctness.

### D2. The deliverable sets the tier, not the presence of an approval

A law record is `legislated` only when an operator-approved contract names its
`law_id` among the subjects of an outcome predicate. The law is then the thing
the contract delivered.

A law record is `derived` when a contract names it only in
`architecture_binding.law_additions`. The contract delivered other behavior, and
the law came with it.

This test is mechanical. It reads the approved contract and needs no judgement.
Without it the tier collapses, because every accreted requirement in the
predecessor Product arrived through an approved change.

### D3. The tier sits beside `status`, not inside `provenance`

The record gains a sibling `authority` object.

```text
authority
  tier                 # legislated | derived
  legislated_by?       # work_id, required when tier is legislated
  contract_version?    # integer, required when tier is legislated
```

The `provenance` object describes where the record came from in Git. It sets
`additionalProperties` to false and holds no place for the approving contract.
Standing and source are separate facts, so they take separate objects.

### D4. A conflict with `derived` law is revised inside the contract

A conflict with `legislated` law keeps the legislative moment that CD-0006 and
`docs/specs-as-laws.md` define. The operator chooses to clarify intent, to evolve
the law, or to accept a scope reduction.

A conflict with `derived` law is revised inside the work contract and recorded
there as a revision line. It costs no separate approval turn.

This amends CD-0006. Human approval remains required to enact a law change, and
the approval of the contract that carries the revision supplies it. The operator
keeps sight of every revision, because each one appears in the contract they
approve. The change collapses many separate questions into one contract line.

A scope cut stays governed by the request, not by the tier. CD-0006 and CD-0012
continue to forbid a silent scope cut and a substituted outcome, whatever tier
the touched law carries.

### D5. Defaults for existing records and for an import

The 197 records under `docs/knowledge/records` take a default tier by kind.

| Kind | Count | Default tier |
|---|---|---|
| `decision` | 144 | `legislated` |
| `constitution` | 3 | `legislated` |
| `spec` | 26 | `derived` |
| `reference` | 18 | `derived` |
| `lesson` | 4 | `derived` |
| `research` | 2 | `derived` |

A decision record and a constitutional record are operator-accepted by
construction. The other kinds describe built behavior or observed material.

Every record imported from a predecessor specification tree lands as `derived`.
The predecessor change workflow authored those statements, and its archive proves
it. The operator may promote any record to `legislated` afterward. Promotion is
deliberate and needs an approved contract under D2.

## Consequences

- Revising a build transcript stops costing an operator turn. Revising a real
  commitment still costs one.
- The record schema and its `authority` object must land before the first typed
  knowledge record exists. `work-771ff028fe5936b21685b7d0` creates that record
  and enacts five law amendments. Those amendments must not land first, or they
  describe a typed record with no tier and need amending again.
- An agent cannot promote its own law. Only an operator-approved contract that
  names the `law_id` in an outcome predicate sets `legislated`.
- A Product migrated from the predecessor gains a usable law surface without a
  hand review of every requirement.
- CD-0006 gains a qualification. Its ban on a silent scope cut is untouched.

## Verification

- A schema test rejects a record whose `authority.tier` is `legislated` and
  whose `legislated_by` or `contract_version` is absent.
- A store test proves that a contract naming a `law_id` only in
  `architecture_binding.law_additions` writes `derived`, and that a contract
  naming the same `law_id` in an outcome predicate writes `legislated`.
- A store test proves that a conflict with a `derived` record records a contract
  revision line and raises no operator approval requirement.
- A store test proves that a conflict with a `legislated` record still raises the
  legislative moment.
- A validator run over `docs/knowledge/records` proves every record carries a
  tier and that the counts in D5 hold.
