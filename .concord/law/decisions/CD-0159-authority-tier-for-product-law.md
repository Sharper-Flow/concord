# CD-0159: Authority tier for Product law

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The standing of a Product law record, and the approval cost of
  revising one
- **Approval:** The Snowball coordinator approved this law change on
  2026-10-14 under the operator's delegation. The operator approved the contract for
  `work-c089308b507cfac61f7dd228` on 2026-09-20, then accepted D1 to D5
  below on the same date.
- **Related:** CD-0006, CD-0015, CD-0036, CD-0222, and
  `.concord/docs/specs-as-laws.md`
- **Preserves:** CD-0006's ban on a silent scope cut, CD-0012's rule against
  outcome substitution, CD-0036 revision identity and breaking-law cutovers, and
  CD-0222 Domain-bound contracts

**Consent:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
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

The law file is the place to fix it. A law file's authored metadata carries
its stable ID, title, and authority standing; kind and acceptance come from
the law folder, and path and content proof come from the committed bytes — all
derived, never authored. Standing had no authored field, so adding one cost no
migration of existing law text.

## Decision

Under CD-0013 D11, recording an engineering contract or routine successor does not itself enact Product law. Actual law changes follow CD-0230 D4. A routine workflow consent class does not establish law-change authority. A governed scope cut remains a product decision requiring operator approval under CD-0230 D5 and is never silent.


### D1. A Product law file carries an authority tier

Each law file's metadata carries one of two tier values.

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

### D3. The tier is authored in the law file's metadata

The law file's metadata gains an `authority` object.

```text
authority
  tier                 # legislated | derived
  legislated_by?       # work_id, required when tier is legislated
  contract_version?    # integer, required when tier is legislated
```

Identity, title, and this authority standing are authored metadata. Kind and
acceptance come from the law folder, and path and content hash come from the
committed bytes; they are derived, never authored
([law-folder-authority](../constitution/law-folder-authority.md)). Standing and
source stay separate facts.

A law file approved outside managed workflow carries `tier: derived` and
no `legislated_by` or `contract_version` until a managed contract
promotes it under D2. An issue key, pull request, or approval phrase
does not substitute for a contract. The authority tier does not erase
the recorded operator approval or the host route's review obligations.

`legislated_by` uses the store's work-ID form, `^work-[0-9a-f]{8,64}$`.
The referenced contract version is positive. Every consumer applies the
same closed D5 cohort described in D5.

### D4. A conflict a `derived` record declares is revised inside the contract

A conflict whose declaring record is `legislated` keeps the legislative moment
that CD-0006 and `.concord/docs/specs-as-laws.md` define. The operator chooses to clarify
intent, to evolve the law, or to accept a scope reduction.

A conflict whose declaring record is `derived` is revised inside the work
contract and recorded there as a revision line, because only the declaring
record can withdraw the conflict. It costs no separate approval turn.

Human approval remains required to enact a law change, and
the approval of the contract that carries the revision supplies it. The operator
keeps sight of every revision, because each one appears in the contract they
approve.

A scope cut stays governed by the request, not by the tier. CD-0006 and CD-0012
continue to forbid a silent scope cut and a substituted outcome, whatever tier
the touched law carries.

### D5. Defaults for existing records

The legislative default cohort is closed: the decision and constitutional
records present at Git commit `fe69821f724bc8d40ec2730c9fd390bd80f1f527` that
still exist. Deleting a record removes it from the cohort; no record ever joins
it. D5 itself establishes the members' standing; the vehicle that applied it is
`work-8d7bd788b2890355460bb5b7` at contract version 1. No other record may name
that vehicle.

`internal/store/knowledge_authority_defaults.v1.json` is the one committed
cohort source. It records that commit and the IDs and kinds of the surviving
members. Every consumer must use it; membership never grows with
current `HEAD`.

The records present at that commit took their default tier by kind:

| Kind | Default tier |
|---|---|
| `decision` | `legislated` |
| `constitution` | `legislated` |
| `spec` | `derived` |
| `reference` | `derived` |
| `lesson` | `derived` |
| `research` | `derived` |

A decision record and a constitutional record are operator-accepted by
construction. The other kinds describe built behavior or observed material.

### D6. A store verifies only the history it holds

A store that holds the named work checks its exact versioned approved
contract against D2. Missing or unapproved versions and contracts without
an admitted law reference cause refusal. A closed D5 default uses D5 as
its basis instead of claiming contract approval under D2.

A store that does not hold the named work reports the claim unverified,
not false. It preserves the declared tier; absence does not authorize
demotion or promotion. Database failures do not count as absence.

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
- An agent cannot promote its own law. Outside the closed D5 cohort,
  only a named operator-approved contract that meets D2 sets `legislated`.
- CD-0006 forbids a silent scope cut.

## Verification

- A schema test rejects a record whose `authority.tier` is `legislated` and
  whose `legislated_by` or `contract_version` is absent.
- A store test proves that each non-default legislative claim meets D2's
  Product-changing condition and record-kind reference rules.
- A store test refuses a non-default claim backed only by read-law pins,
  and reports an absent work as unverified without changing the tier.
- A law-metadata test refuses non-work IDs and out-of-cohort D5 vehicle claims.
- A store test proves that a conflict a `derived` record declares records a
  contract revision line and raises no operator approval requirement.
- A store test proves that a conflict a `legislated` record declares still raises
  the legislative moment at approval.
- A validator run checks every current tier and each legislative reference.
  A history-derived test proves that every cohort member was present at the
  pinned application commit and that the cohort lists every surviving one.
