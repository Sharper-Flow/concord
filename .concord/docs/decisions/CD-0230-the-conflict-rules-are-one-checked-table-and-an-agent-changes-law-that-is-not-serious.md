# CD-0230: The conflict rules are one checked table, and an agent changes law that is not serious

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** How Concord applies the conflict rules of `goals.md` Appendix A:
  the machine-readable rule table, the mechanism that enforces each rule, the
  record of each application, law-change authority under R3 and P12, the
  operator approval classes, and the follow-up ledger
- **Implements:** [`goals.md`](../goals.md) goal F for the conflict rules,
  Appendix A, and goal E's measure that follow-up items get closed
- **Amends:** [CD-0006](CD-0006-concord-root-product-policy.md) D5 and D10;
  [CD-0013](CD-0013-workflow-engine-mechanism.md) D9;
  [CD-0036](CD-0036-breaking-law-cutovers.md) D2, who chooses amendment or
  replacement; [CD-0159](CD-0159-authority-tier-for-product-law.md) D2, D4, and D5;
  [CD-0218](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md)
  D1, its operator-approved law route;
  [CD-0222](CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md)
  D1, D4, and D5, the law outcome and law mandate of Product-changing work
- **Preserves:** CD-0159 D1, D3, D6, and D7, the authority tier and its checks;
  the ban on a silent scope cut and a substituted outcome in CD-0006 and
  [CD-0012](CD-0012-bind-stated-goals-to-delivered-outcomes.md); CD-0036 D1
  and D3 to D6, law revision identity and breaking cutovers;
  [CD-0122](CD-0122-concord-workflow-scope-and-defect-repair.md), work outside
  the workflow; [CD-0213](CD-0213-linear-mcp-is-the-only-planning-authority.md),
  Linear as the only planning authority; each operator approval point an
  accepted record holds, until that record is amended
- **Approval:** The Snowball coordinator approved this record on 2026-10-17
  under the operator's delegation. The record is serious under P12 because it
  affects more than one Domain.
  The operator approved the D5 coordinator-routing amendment as exact text on
  2026-10-19 for
  [Concord (CON) issue 1212](https://linear.app/sharper-flow/issue/CON-1212).
- **Amended:** 2026-10-19, approved by the operator as exact text
  for [Concord (CON) issue 1200](https://linear.app/sharper-flow/issue/CON-1200).
  D4's P12 Domain test counts the child Domains of the laws a change adds,
  modifies, or removes, not its footprint record. The change is serious under
  P12 because it narrows when an operator approval applies, which removes
  behavior the operator sees.
- **Amended:** 2026-10-19, for
  [Concord (CON) issue 1212](https://linear.app/sharper-flow/issue/CON-1212).
  D5 routes a choice that carries a security risk, changes UI/UX, sets a
  preference, or sets product direction, and that no approval point holds,
  through the item's coordinator before the operator. The change is serious
  under P12 because it changes which questions reach the operator.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D9, D10

## Context

Appendix A of `goals.md` is accepted constitutional law. It gives a rule for
each pair of goals (A1) and for each conflict situation by change size (A2).
Goal F asks that the rules become "a mechanism inside Concord, not prose", so
the operator's spot checks can show that agents follow them.

Today the rules exist only as text in `goals.md` and in coordinator
instructions. No record shows which rule settled a conflict. Accepted records
still require operator approval for every law change: CD-0006 D5 ("Human
approval is always required to enact law changes or governed scope cuts"),
CD-0159 D4 ("Human approval remains required to enact a law change"),
CD-0222 D1 ("it carries an operator-approved mandate"), CD-0218 D1
("Product-law changes require the existing operator-approved Git law route"),
and CD-0036 D2 ("The operator makes this legislative choice through the Git
delta"). CD-0006 D10, CD-0013 D9, and CD-0222 D4 and D5 bound each law change
by an operator-approved spec mandate. Rule R3 says instead: "change the law or
if serious than consult the user", and P12 defines serious.

## Decision

### D1. One machine-readable table holds the rules

A contract file under `contracts/` holds every A1 cell and every A2 row: the
rule ID, the goals or situation in conflict, the rule for each size, the actor
who applies it, and its enforcing mechanism. The rule text stays in
`goals.md`. The table cites each rule by its ID and records the `goals.md`
content hash it was checked against.

A validator in the repository's required checks refuses the table when a cell
or row of Appendix A is missing, when a rule names no mechanism kind, or when
the `goals.md` content hash differs from the recorded one.

### D2. Each rule names how it is enforced

A rule's mechanism kind is one of four: a programmed check that refuses or
reports; a coordinator decision that a tool records; an operator boundary
where the operator approves or accepts; or a gap. A prose instruction is not a
mechanism (Appendix A P3 and P7).

A gap row names the follow-up item that will build its mechanism. The
validator reports every gap, so the gap count measures progress toward goal F.

### D3. Applying a rule leaves a record

When an agent, the coordinator, or the operator settles a conflict, the work
record names the rule ID, the change's size, and the outcome. The operator's
spot checks read these records.

A conflict between a law's letter and its stated intent is a law conflict. The
agent raises it under R3 and does not apply the letter silently.

### D4. An agent changes law that is not serious

An agent changes a law and records why, unless the change is serious under
P12. A serious law change needs the operator's approval of its exact text. At
`crucial` size, every law change needs that approval (Appendix A2 row 3).

The P12 test that a change affects more than one Domain counts the child
Domains of the laws the change adds, modifies, or removes. Each such law
contributes its home Domain and its `applies_to_domain_ids` (CD-0222 D3), as
recorded before and after the change. The Product-root Domain is not counted:
a root-homed law is Product-wide by placement, and its applicability names the
child Domains it reaches. A reference record is not law and adds no Domain.
A change to the Domain registry, itself Product law (CD-0222 D2), counts each
child Domain whose entry it adds, modifies, or removes, and both child ends of
each architecture relation it adds, modifies, or removes. The test holds when
the count exceeds one.

The count reads recorded law homes and applicability, so it needs no
declaration. It does not count the Domains of the code that ships with the law
change. Those Domains are the work's footprint, which reaches every Domain of
each touched package (CD-0222 D2) and sizes the work under CD-0229. Review
checks that each added law's home follows CD-0222 D3, so the root Domain does
not become a catch-all that lowers the count.

The agent declares the other three tests: whether the change removes or
reverses behavior users see, touches security or data safety, or changes
UI/UX. Independent review checks that declaration.

This amends CD-0006 D5 and D10, CD-0013 D9, CD-0036 D2, CD-0159 D4, CD-0218
D1, and CD-0222 D1, D4, and D5. Where they require human or operator approval
for a law change, a law mandate, or the choice between amendment and
replacement, that approval is required only for a serious change. A
non-serious change carries the agent's recorded reason, and the agent records
its CD-0036 choice with it.

This amends CD-0159 D2 and D5. A law revision enacted without the operator's
approval gives the record the `derived` tier, whatever tier it held before,
including a default tier from the closed D5 cohort.

A governed scope cut is not a law change. It is a product decision under D5,
keeps the operator's approval, and is never silent.

[CD-0242 D10](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d10-knowledge-is-an-input-and-a-law-edit-changes-only-what-it-changes)
governs: an agent corrects a factual documentation error inside its work item
without an operator prompt when the correction changes neither intended
behavior nor an operator decision, and such a correction is not a law change
under P12.

### D5. The operator approves only at the operator's boundaries

An operator approval point belongs to one of five classes, or is a listed gap:

- a change that affects UI/UX, which waits for the operator's review and
  acceptance (rule U);
- a change under rule U that needs the operator's approval of a development
  preview of the final candidate before it is pushed to a shared remote or
  merged ([CD-0242 D9](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d9-completion-needs-proof-approvals-delivery-and-publication));
- a product or preference decision, including a governed scope cut;
- a serious law change under D4;
- a destructive action on Concord-owned state that has a verified recovery
  route before it runs, such as a snapshot Concord has verified.

A destructive action without a verified recovery route is not an approval
point. It is a data-safety risk: the agent stops, states the risk, and
proposes a safe route, even at the operator's request (rule S and P10).

The table lists each operator approval point that an accepted record holds,
with its class. A point that fits no class is listed as a gap with its
follow-up item. It stays an operator approval until the record that holds it
is amended. Removing a point or changing its class removes behavior the
operator sees, so it is serious under P12.

A choice inside a work item that carries a security risk, changes UI/UX, sets
a preference, or sets product direction, and that no approval point holds,
goes first to the item's coordinator: the release train engineer (RTE) when
one coordinates the item, otherwise the operator. The agent states the choice
in plain words with its recommendation and the risk. The coordinator decides a
choice whose answer is clear, alone or with an advisor subagent, and records
the decision and its reasons on the item. A choice that neither can clearly
decide goes to the operator. This route settles no approval point: each point
the table lists, the exact-text approval of a serious law change, rule U's
review and acceptance, and a Concord approval challenge stay with the
operator. A purely technical choice stays with the agent that owns the item.

### D6. Follow-up items form a checkable ledger

A follow-up item that a rule creates is a Linear issue. It carries a label
naming the rule ID and a link to the change that raised it. The rule table
names that label for each rule that files follow-up items. A Linear view
filtered by those labels lists the open items by rule, age, and raising
change, so goal E's measure, that follow-up items get closed and not only
filed, can be checked. Until a check enforces the label, the rule table lists
the D6 label duty as a gap.

## Alternatives considered

- Keep the rules as prose and rely on review. Goal F rules this out.
- Copy the rule text into the table. Two copies would drift.
- Let the agent decide all four P12 tests alone. The Domain test reads
  recorded law homes, and a recorded fact needs no judgement.
- Count the Domains of the change's footprint record. The footprint lists
  every Domain of each touched package. An amendment of a law homed in one
  Domain that also changed the installer counted six child Domains, so every
  law change that ships with code in a shared package needed an operator turn.
- Count the change's declared affected Domains. That set also names the
  Domains the shipped code touches, so a one-Domain law change with installer
  code still counts two, and a change outside a contract has no store-checked
  set.
- Give each file its own Domains in the package map. CD-0222 D2 keeps the map
  at package grain, and a file such as the installer still serves several
  Domains, so the count would follow the file and not the law. It would also
  rewrite the map and change work sizing, which reads the same footprint.
- Count a root-homed law as every child Domain. Fifty current law records are
  root-homed, so every amendment of one would be serious: the footprint's
  overcount again.
- Keep operator approval for every law change. R3 replaces that rule.
- Treat every change to a `legislated` record as serious. That adds a fifth
  test P12 does not hold.
- Reclassify every operator approval point outside the four classes at once,
  without amending the records that hold them. An unnamed amendment cannot be
  checked, and it would remove approvals no review has seen.
- Keep the follow-up ledger in the store. CD-0213 keeps planning facts in
  Linear only.

## Consequences

A non-serious law change no longer costs an operator turn. The cost moves to
the declaration of the P12 tests and its review.

The P12 Domain test reads the changed laws' child Domains. A change to
root-homed law without applicability is no longer serious by the Domain
test alone; the other three tests and the `crucial`-size rule still apply.

A record the operator legislated becomes `derived` when an agent revises it
without operator approval, so its standing shows who last enacted it.

Operator approval points outside the five D5 classes stay in force as listed
gaps. Each becomes an agent or coordinator decision only when a later change
amends the record that holds it.

Every change to `goals.md` moves its content hash, so the change that edits
`goals.md` also checks the table against it and records the new hash.

## Verification

- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-knowledge-closure.py` pass when the record is
  registered.
- `python3 scripts/run-script-tests.py` must prove that the rule-table
  validator refuses a missing A1 cell or A2 row, a rule with no mechanism
  kind, and a stale `goals.md` hash, that it reports each gap, and that each
  rule that files follow-up items names its label.
- `go test ./internal/store` must prove that a non-serious law change with a
  recorded reason is admitted without operator approval and records the
  `derived` tier, also for a D5 cohort record, that a change modifying laws
  homed in two child Domains requires approval, that a changed law's
  `applies_to_domain_ids` count toward the test, that a change removing or
  superseding laws homed in two child Domains requires approval, that a law
  whose `applies_to_domain_ids` the same change narrows still counts the
  Domain it dropped, that a law moved between two child Domains counts both,
  that a change adding an architecture relation between two child Domains
  requires approval, that a change modifying laws homed in one child Domain
  and in the root Domain, with code in `internal/agent` and `internal/store`,
  counts one Domain, that an amendment of one law homed in `agent-surface`
  that also edits `scripts/install.py` counts one Domain, that every law
  change at `crucial` size requires approval, and that a governed scope cut
  requires it at every size.
- `go test ./internal/store` must prove that each operator approval point
  carries one of the five D5 classes or stays an operator approval as a listed
  gap, and that a conflict record names its rule ID, size, and outcome.
