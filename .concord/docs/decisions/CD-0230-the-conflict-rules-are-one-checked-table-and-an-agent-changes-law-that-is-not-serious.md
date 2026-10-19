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
- **Amended:** 2026-10-19. Operator approval of the exact text for
  [Concord (CON) issue 1200](https://linear.app/sharper-flow/issue/CON-1200).
  D4's P12 Domain test counts the change's declared affected Domains, not its
  footprint record. The change is serious under P12 because it narrows when an
  operator approval applies, which removes behavior the operator sees.

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

The P12 test that a change affects more than one Domain counts the change's
declared affected Domains. In a Product-changing contract these are the
architecture binding's `affected_domain_ids`. The store already requires that
set to hold the home Domain of every law the change adds or modifies and both
ends of every Domain relation it modifies. A law change made outside such a
contract declares the same set, under the same rule, in its pull request. The
test holds when the set names more than one Domain.

The footprint record is the evidence for that declaration, not the count. A
path's footprint reaches every Domain of its owning package (CD-0222 D2), so a
count of the footprint would make every law change to a shared package
serious. The agent also declares the other three tests: whether the change
removes or reverses behavior users see, touches security or data safety, or
changes UI/UX. Independent review checks the declared Domains against the
footprint, and the other three declarations, before the change is enacted.

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

### D5. The operator approves only at the operator's boundaries

An operator approval point belongs to one of four classes, or is a listed gap:

- a change that affects UI/UX, which waits for the operator's review and
  acceptance (rule U);
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
- Let the agent decide all four P12 tests alone. The Domain test's floor, the
  home Domain of every law the change adds or modifies, is a fact the store
  enforces; review checks what the agent declares beyond it.
- Count the Domains of the change's footprint record. The footprint lists
  every Domain of each touched package. An installer-only amendment of a law
  homed in one Domain counted seven, so every law change that ships with code
  needed an operator turn.
- Give each file its own Domains in the package map. CD-0222 D2 keeps the map
  at package grain, and a file such as the installer still serves several
  Domains, so the count would follow the file and not the change. It would
  also rewrite the map and change work sizing, which reads the same footprint.
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

The P12 Domain test reads the declared affected Domains, while work sizing
under CD-0229 and the footprint read keep their package-grain counts. An agent
that declares too few Domains understates seriousness; the review of the
declaration against the footprint is the check on that risk.

A record the operator legislated becomes `derived` when an agent revises it
without operator approval, so its standing shows who last enacted it.

Operator approval points outside the four D5 classes stay in force as listed
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
  `derived` tier, also for a D5 cohort record, that a change whose declared
  affected Domains name two Domains requires approval, that a change touching
  only `scripts/install.py` to amend a law homed in one Domain counts one
  Domain, that a declared set without the home Domain of a law the change
  modifies is refused, that every law change at `crucial` size requires
  approval, and that a governed scope cut requires it at every size.
- `go test ./internal/store` must prove that each operator approval point
  carries one of the four D5 classes or stays an operator approval as a listed
  gap, and that a conflict record names its rule ID, size, and outcome.
