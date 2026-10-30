# CD-0243: Four roles and six operator approval classes

- **Status:** Accepted
- **Date:** 2026-10-23
- **Scope:** Role ownership, operator approval, law-change authority, and data-safety decisions
- **Approval:** Operator approved the exact text on 2026-10-23 (package SHA-256 `808f55997b3e5a61ca20e841c40530fe06ad6976e8392f5f439bd08ba42893f4`).
- **Supplement approval:** Operator approved the complete supplement on 2026-10-23 (SHA-256 `972f2aab111173f051f0e7e4041decf31394f7a360f5553e9e512d9bda98cff2`).
- **Linear write ownership approval:** The operator approved D1's Linear write split on 2026-10-23 in [Concord (CON) issue 1304](https://linear.app/sharper-flow/issue/CON-1304).

## Context

Concord's approval rules stop routine engineering and work management, route
operator decisions to the release train engineer (RTE), and treat the Planner as a research assistant
rather than an assistant Product Owner. The operator wants self-sufficient
agents and a defined set of decisions reserved to the operator.

## Decision

### D1. Four roles have separate owners

- **User Operator:** owns product direction and the approvals in D2.
- **Concord Agent:** the agent session that owns an assigned item from
  investigation through delivery, including design, implementation, tests,
  review, law, publication, CI follow-through, and planning records.
- **Concord-RTE:** the optional release train engineer. Coordinates concurrent
  agents and work, resolves sequencing conflicts, relays facts, and reports
  delivery status. It does not approve or accept another agent's work or decide
  an operator-reserved choice.
- **Concord-Planner:** the optional assistant Product Owner. Helps the operator
  investigate needs, refine product direction, and maintain an actionable
  backlog. Proposals are not operator decisions. The Planner does not approve
  execution, change production code, or advance managed work.

Linear writes follow this split:

- The Concord agent owns its own item's issue.
- The Planner owns backlog shaping and creates tickets the operator approves.
- The RTE corrects stale status and links, and never changes scope.

A Concord Agent needs neither an RTE nor a Planner to complete its work. It
contacts the RTE for coordination, not permission. It uses an advisor subagent
for a second opinion. A spawned review subagent counts as external review;
required checks, review scope, and evidence binding still apply.

### D2. The operator approves six classes of decision

1. **Product direction:** what the product offers, whom it serves, and which
   outcomes it pursues. This includes a governed scope cut, subject to D3's
   Concord deletion exemption, and any change to the operator's approval boundary.
   Adding, removing, or reclassifying an
   operator approval requires the operator's approval of its exact wording.
2. **UI/UX:** new or changed design or interaction. Minor corrections that
   introduce no new design or interaction, such as a typo fix or restoring a
   broken button's approved behavior, need no fresh approval. Other UI/UX
   changes retain operator review and acceptance, subject to D3's Concord deletion
   exemption.
3. **Production application data:** modifications to data in a deployed
   production application. Irreversible loss of customer data also requires
   approval, wherever the data is held. Ordinary Concord coordination writes
   and local development branches and worktrees are not production application
   data. Their location does not exempt customer-data loss.
4. **Security:** changing security policy, granting access, weakening
   protections, or accepting unresolved security risk. A repair that restores
   existing protections needs no separate security approval.
5. **Increased spending:** a change that increases spending, such as adding a
   paid infrastructure resource. Agents need not track spending limits or ask
   for each ordinary use of an existing paid service, resource, or model.
6. **Legal obligations:** accepting contracts, changing licensing, or making
   commitments on the operator's behalf.

Every applicable class counts. An exemption from one class does not waive
another. The agent brings these decisions directly to the operator, with its
recommendation and material risks. Neither an RTE, a Planner, an advisor, nor a
reviewer can supply the operator's approval.

### D3. Decisions outside those classes belong to agents

Outside D2, an agent does not stop to ask for discretionary approval. Routine
technical preferences, implementation within approved product direction, and
work management are agent-owned. Examples include internal helper names, test
organization, cancelling obsolete work, combining duplicate work, and deleting
unneeded research records.

Agents may remove dead code and genuinely incorrect tests within their scope.
Deletion alone does not require approval. They do not remove valid checks to
hide failures. Stated operator preferences remain binding.

Within assigned work on Concord, a Concord agent may retire or delete Concord’s own laws, unwanted mechanisms and systems, and tests whose required behavior it retires, without per-deletion operator approval. Deletion alone does not trigger product-direction, UI/UX, or governed-scope-cut approval, including when it removes a Concord capability or a law that requires the mechanism.

The agent records why, retires the governing requirements with or before the behavior, and delivers the removal through an ordinary pull request with independent review and required CI. The pull request and completion report list each retired law and its change in meaning. Each deleted test cites the clause that retires its behavior. Do not delete valid checks merely to hide failures or silently substitute an assigned item’s agreed outcome.

This exemption does not authorize changes to other Products’ law or data, production application data, irreversible customer-data loss, removal or weakening of security protections, legal obligations, or increased spending. Their existing approval rules remain. Changes to the operator’s approval boundary still require approval of their exact wording. Existing operator-only gate waivers and current tool refusals remain binding.

Approval is not authorization to exceed scope, impersonate an actor, fabricate
evidence, or bypass a tool refusal. Missing information, credentials, host
permissions, and unavailable capabilities can still block an operation. They do
not create another discretionary approval class. Use admitted recovery routes;
report an implementation gap instead of evading a gate.

### D4. Law changes use the same approval boundary

A law change requires operator approval only when it falls within D2. Being
serious, touching several Domains, or having crucial size does not independently
require approval. An agent records the reason for a law change it owns and
publishes it through the applicable law and repository checks.

A law revision enacted without operator approval has the `derived` authority
tier under CD-0230 D4. This decision does not change legislative standing,
law-revision identity, explicit amendment or replacement, or the ban on silent
scope cuts. Changes to the approval boundary always require exact-text approval
under D2, including changes to that reservation itself.

### D5. Recovery informs the decision; it is not an absolute veto

Before an action that risks permanent loss, the agent establishes what will be
lost and whether a verified recovery route exists. When D2 requires approval,
it explains the permanent-loss risk and any missing recovery route. The
operator may explicitly approve the irreversible loss. A general task request
does not substitute for informed approval of that loss.

Without that approval, an agent does not risk irreversible customer-data loss.
Outside D2, lack of a verified recovery route does not create an operator
approval requirement or an absolute stop. Git-recoverable source cleanup is
ordinary maintenance, not irreversible loss merely because it removes text.

### D6. Existing approval requirements follow these classes

The following approval and recovery requirements preserve authentication,
capability, scope, state, evidence, version, retention-eligibility, and
required-review checks.

| Owner | Subject |
|---|---|
| `priorities.md` Constraints; Appendix A: A1 axis key, R3, S, U, P1, P5, P10, P12, A2 rows 3, 4, 7, 9; Appendix B row 3 | Roles, six approval classes, law-change and data-safety decisions |
| CD-0230 D4, D5 | Law-change authority, approval classes, listed gaps, recovery |
| CD-0013 D11 | Conversational operator decisions and routine actions |
| CD-0006 D5, D10; CD-0159 D4; CD-0218 D1; CD-0222 D1, D4, D5 | Law mandates and enactment require approval only under D2 |
| CD-0033; CD-0096; CD-0122; CD-0166; CD-0198; CD-0215; CD-0217; CD-0222 D6; CD-0229 | Operator consent for their operations applies only under D2; D5 governs recovery |
| CD-0154 D2 | Four roles, optional RTE and Planner, direct operator routing |
| TS1, TS4, TS9 contracts; TS5 §5; clarifications TS1–TS9 binding; PM9 contract; CD-0029 | Retained approval gaps for overlap, cancellation, removal, repair, reconciliation, and research deletion apply only under D2 |

An operation that grants access
still requires security approval; routine work management does not acquire
that class merely because it changes a record.

The conflict table and generated instructions must
implement this law. Until they do, their mismatches are implementation gaps,
not permission for an agent to bypass their current gates.

### D7. The remaining owners use the same approval boundary

The following requirements apply D2 through D5, including D3's Concord deletion
exemption; they create no approval class, execution route, or permission.
For these owners and every owner in D6, the qualification governs ordinary
operator-consent clauses, including tables, acceptance criteria, verification
prose, and summaries. An ordinary operation that passes its admission gates
requires operator consent only when a D2 class applies.

| Owner | Qualified subject |
|---|---|
| CD-0018 D4; CD-0224 Decision and its overlap criteria | Merger and supersession do not independently require operator approval; a governing-requirement scope cut follows D2 and D3's Concord deletion exemption |
| CD-0094 D2, D3 and terminal-transition claims | Completion, cancellation, and destructive-effect approval follows D2, not terminal status alone |
| CD-0142 Consequences; PM4 and compaction-retention-policy removal clauses | Per-item execution removal may be agent-directed within the assigned scope; its planning destination and preparation fences remain required |
| CD-0210 D1, D2 and reconciliation criteria | Outside-repair disposition and reconciliation require operator consent only under D2 |
| CD-0216 retirement contract | Physical research-pack deletion requires operator consent only under D2 |
| CD-0218 D2 | Recovery consent follows D2; current admission and safety requirements remain |
| `priorities.md` quality and visibility clauses | Law-change and contract-supersession approval follows D2, not seriousness |
| TS4 operation rows and overlap criteria; TS8 overlap proofs; TS1 and clarifications overlap summaries | Version-pinned overlap resolutions retain their scope checks; conversational operator approval follows D2 |
| PM6 publication sequence and content lock; TS4 publication row; PM9 payload summary | The durable publication intent freezes exact content and home; it is not operator approval |
| Workflow-engine contract and workflows outcome-contract and transition tables | Conversational operator decisions about contracts and supersession follow D2 |
| PM10 emergency-salvage recovery record | The recovery record remains required; operator consent follows D2, not the absence of a verified snapshot alone |
| CD-0015 relation authoring and `law_modifies`; CD-0040 D7 | Law-relation, law-delta, and freshness-policy revisions follow D4; they require operator approval only under D2 |

`specs-as-laws.md` and `concord-knowledge-index.md` are companion explanations,
not new law. They follow this same boundary: a law conflict is identified and
recorded, but an agent pauses for operator approval only under D2 or when
information needed to decide the task is missing. An agent may evolve law
within its scope and record why. It never silently cuts governed scope.
Legislative standing remains unchanged. The documentation index summarizes
these requirements.

These amendments change consent, not other admission. Authentication,
capability, source and actor identity, exact versions, scope, execution
relinquishment, retention
eligibility, publication proof, workflow recovery admission, checkpoints,
evidence, and required independent review keep their existing conditions and
bounded exceptions. No agent gains a bypass.
CD-0218 D5's release activation and cutover,
CD-0142 D3's choice of a non-Linear planning destination, and CD-0040 D9's
exact exception for an unverifiable source also remain unchanged.
D5's treatment of data recovery does not remove workflow recovery checks. Emergency salvage
still isolates suspect output and preserves PM10's verification and retention
requirements.

## Alternatives considered

- Keep serious law, routine preferences, and irreversible loss as separate
  approval classes. The operator reserved the six classes in D2 instead.
- Let the RTE decide operator choices. Coordination is not operator authority.
- Require approval for every paid request or ordinary Concord write. That
  would make normal execution depend on repeated operator intervention.
- Keep an absolute data-safety veto. The operator may approve an explained
  irreversible loss, while customer-data loss remains reserved.

## Consequences

Agents own routine engineering and work management. The operator retains
product direction and consequential data, security, cost, and legal decisions.
Changing that boundary requires the operator's exact-text approval. Runtime
and generated-surface alignment must be verified separately from accepting
this law; this record does not claim those mechanisms are implemented.

## Verification

- `python3 scripts/check-knowledge-index.py` checks the law record and relations.
- `python3 scripts/check-primary-prompts.py` checks the generated role sources.
- `python3 scripts/generate-agent-profiles.py --check` checks their projection.
- `bun test adapter/opencode/` checks the shipped asking instructions.
