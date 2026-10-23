# CD-0159: Authority tier for Product law

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The standing of a Product law record, and the approval cost of
  revising one
- **Approval:** The Snowball coordinator approved this law change on
  2026-10-14 under the operator's delegation.
- **Original approval:** The operator approved the contract for
  `work-c089308b507cfac61f7dd228` on 2026-09-20, then accepted D1 to D5
  below on the same date.
- **Amended:** 2026-10-17. D4 and its verification key the tier on the record
  that declares a conflict, the only record that can withdraw it.
- **Amended:** 2026-10-18.
- **Related:** CD-0006, CD-0015, CD-0036, CD-0222, and
  `.concord/docs/specs-as-laws.md`
- **Amends:** CD-0006's rule that human approval is always required to enact a
  law change
- **Preserves:** CD-0006's ban on a silent scope cut, CD-0012's rule against
  outcome substitution, CD-0036 revision identity and breaking-law cutovers, and
  CD-0222 Domain-bound contracts

**Approval amendment:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
governs D4: law enactment requires approval only under its six classes. Operator-free revisions retain the `derived` tier under CD-0230 D4; the tests for `legislated` standing do not change.

## Context

Concord records no difference between law the operator legislated and law an
agent wrote while it delivered a change. Both cost one operator approval turn to
revise. A layout note from a past change therefore obstructs the next request at
the price of a real commitment.

CD-0222 names the deliverable a pruned statement of what the Product must keep
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
content hash. CD-0222 binds it to a home Domain. The checks rerun in a
transaction at every consequential action. Admitting a build transcript as law
therefore costs more under Concord than under the predecessor.

The manifest record is the place to fix it. Each record under
`.concord/docs/knowledge/records/` already carries identity, kind, status, path,
and a SHA-256 content proof. No field records standing, so a new field costs no
migration of existing law text.

## Decision

Under CD-0013 D11, recording an engineering contract or routine successor does not itself enact Product law. Actual law changes follow CD-0230 D4, including its amendment of this record's D2, D4, and D5. A routine workflow consent class does not establish law-change authority. A governed scope cut remains a product decision requiring operator approval under CD-0230 D5 and is never silent.


### D1. A Product law record carries an authority tier

Each law record carries one of two tier values.

- `legislated` — the operator approved this statement as law in its own right.
- `derived` — an agent authored this statement while it delivered a change, and
  the statement describes what the change built.

The tier is a recorded fact about the write. It is not a judgement about the
content. An admission test that asks whether a statement is a genuine Product
obligation is a heuristic, and CD-0222 with `.concord/docs/specs-as-laws.md` refuses a
heuristic as sole authority for correctness.

### D2. The deliverable sets the tier, not the presence of an approval

Outside the closed D5 cohort, the named operator-approved contract version
must pin a definition with `changes_product_truth=true`. This test applies
CD-0222 D5. A non-Product-changing contract never establishes legislative
standing, whatever field names the law.

A decision or constitutional record is `legislated` when that contract
version names its `law_id` in an `exists` or `absent` predicate's `subjects`,
`architecture_binding.law_additions`, or `law_modifies`.

Every other record kind is `legislated` only when that contract version
names its `law_id` in an `exists` or `absent` predicate's `subjects`.
`law_revisions` and `spec_mandate` do not establish legislative standing.

A record outside the closed D5 default cohort is `derived` until an
operator-approved contract meets this test. The test reads the
named contract version and needs no judgement about the law's content.

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

A record approved outside managed workflow carries `tier: derived` and
no `legislated_by` or `contract_version` until a managed contract
promotes it under D2. An issue key, pull request, or approval phrase
does not substitute for a contract. The authority tier does not erase
the recorded operator approval or the host route's review obligations.

`legislated_by` uses the store's work-ID form, `^work-[0-9a-f]{8,64}$`.
The referenced contract version is positive. Both manifest readers
apply the same closed D5 cohort described in D5.

### D4. A conflict a `derived` record declares is revised inside the contract

A conflict whose declaring record is `legislated` keeps the legislative moment
that CD-0006 and `.concord/docs/specs-as-laws.md` define. The operator chooses to clarify
intent, to evolve the law, or to accept a scope reduction.

A conflict whose declaring record is `derived` is revised inside the work
contract and recorded there as a revision line, because only the declaring
record can withdraw the conflict. It costs no separate approval turn.

This amends CD-0006. Human approval remains required to enact a law change, and
the approval of the contract that carries the revision supplies it. The operator
keeps sight of every revision, because each one appears in the contract they
approve. The change collapses many separate questions into one contract line.

A scope cut stays governed by the request, not by the tier. CD-0006 and CD-0012
continue to forbid a silent scope cut and a substituted outcome, whatever tier
the touched law carries.

### D5. Defaults for existing records and for an import

The 202 records at Git commit `fe69821f724bc8d40ec2730c9fd390bd80f1f527`
took a default tier by kind under docs/knowledge/records.
The closed legislative default cohort contains exactly its 149 decision
records and three constitutional records. D5 itself establishes their
standing; the vehicle that applied it is `work-8d7bd788b2890355460bb5b7`
at contract version 1. No later record may name that vehicle.

`internal/store/knowledge_authority_defaults.v1.json` is the one committed
cohort source. It records that immutable commit and those record IDs and
kinds. Both manifest readers must use it; the set does not follow current `HEAD`.

| Kind | Count | Default tier |
|---|---|---|
| `decision` | 149 | `legislated` |
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

### D6. A store verifies only the history it holds

A store that holds the named work checks its exact versioned approved
contract against D2. Missing or unapproved versions and contracts without
an admitted law reference cause refusal. A closed D5 default uses D5 as
its basis instead of claiming contract approval under D2.

A store that does not hold the named work reports the claim unverified,
not false. It preserves the declared tier; absence does not authorize
demotion or promotion. Database failures do not count as absence.

Under `allow_degraded`, a source whose admission check refuses, or that
search did not admit, counts as a degraded source under CD-0020 D3
as amended by CD-0200 D3.

CI checks what the checkout can prove: work-ID form, contract-version
form, and membership of any D5 vehicle claim in the closed cohort.
Repository review remains the approval boundary for outside-workflow
edits. CI does not claim to resolve a contract unavailable to the checkout.

### D7. A correction preserves the historical approval facts

An unsupported legislative declaration is corrected to `derived` when
the held contract evidence fails D2. The correction removes
`legislated_by` and `contract_version`. It does not rewrite a contract,
erase a recorded operator approval, or change the law's substantive scope.
A missing work alone is not held negative evidence and does not authorize
this correction. Promotion still requires D2. The D5 cohort stays closed.

## Consequences

- Revising a build transcript stops costing an operator turn. Revising a real
  commitment still costs one.
- The record schema and its `authority` object must land before the first typed
  knowledge record exists. `work-771ff028fe5936b21685b7d0` creates that record
  and enacts five law amendments. Those amendments must not land first, or they
  describe a typed record with no tier and need amending again.
- An agent cannot promote its own law. Outside the closed D5 cohort,
  only a named operator-approved contract that meets D2 sets `legislated`.
- A Product migrated from the predecessor gains a usable law surface without a
  hand review of every requirement.
- CD-0006 gains a qualification. Its ban on a silent scope cut is untouched.

## Verification

- A schema test rejects a record whose `authority.tier` is `legislated` and
  whose `legislated_by` or `contract_version` is absent.
- A store test proves that each non-default legislative claim meets D2's
  Product-changing condition and record-kind reference rules.
- A store test refuses a non-default claim backed only by read-law pins,
  and reports an absent work as unverified without changing the tier.
- A manifest test refuses non-work IDs and out-of-cohort D5 vehicle claims.
- A search test proves that admission refusals and unadmitted sources use
  CD-0020 D3 as amended by CD-0200 D3.
- A store test proves that a conflict a `derived` record declares records a
  contract revision line and raises no operator approval requirement.
- A store test proves that a conflict a `legislated` record declares still raises
  the legislative moment at approval.
- A validator run checks every current tier and each legislative reference.
  A history-derived test proves the closed D5 cohort and its counts at
  the pinned application commit, not the current checkout.
