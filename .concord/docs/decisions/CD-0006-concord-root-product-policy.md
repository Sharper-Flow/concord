# CD-0006: Concord Root Product Policy

**Status:** Accepted
**Date:** 2026-08-06
**Decision owner:** Operator
**Scope:** Concord purpose, workflow constitution, governance, operator boundary,
knowledge authority, audience policy, release evidence, and Advance migration.

## Context

PM1–PM10, C14/C15, and TS1–TS9 settled Concord's Product-memory, resource,
launcher-row, and agent-surface mechanics. Root Product documents still carried older
leans: indefinite Advance coexistence, Product-by-Product usability before full
readiness, workflow ownership left open, exposure inferred as reachability, a future
team-server ambition, and production-call evidence before initial release.

The operator resolved those root questions in a sequential clarification session.
This record captures the accepted policy. Three bounded research questions remain
explicitly deferred; they do not weaken the accepted constraints.

## Decisions

CD-0222 governs Domain-bound Product law, architecture-overlap resolution, and
the priority order in D5/D10/R3. D1 and D2 follow goals.md and CD-0082 D1.
[CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
governs D5 and D10: law mandates and enactment require approval only under its six classes.

### D1. Product direction follows the goals

[`goals.md`](../goals.md) owns Concord's purpose, goals, and measures. Advance
replacement is not current Product direction. Concord has no full-successor or
predecessor-replacement-readiness requirement.

Usability and delivery are judged against those goals and applicable current law.
This removes no accepted release, maturity, quality, or data-integrity obligation.

### D2. Demand-driven migration and correction

Migration and correction follow
[`CD-0082`](./CD-0082-migration-is-demand-driven.md) D1. Each selected operation
keeps its local authority and safeguards.

### D3. Full workflow coordination engine; native effects

Concord owns durable workflow progression, branching, retries, evidence, recovery,
and the eventual accepted composition model. Native systems still own external
effects such as git, CI, cloud, database, and service-manager operations.

Workflow types are code-defined, versioned, purpose-built built-ins plus one
versioned generic workflow for true one-offs. Repeated generic patterns graduate
into purpose-built types. Arbitrary inline workflow languages are not accepted.

Workflow composition is accepted in §Resolved research R1.

### D4. Proportional rigor uses independent obligation bands

Rigor uses user-declared **audience commitment**:

- `operator_only` — no stability commitment outside the operator;
- `limited` — known testers/early adopters may rely on it;
- `public` — general users may reasonably rely on it.

Audience commitment is independent of maturity, repository visibility, deployment
visibility, or observed traffic. Concord never infers it.

Maturity and audience commitment contribute independent evidence obligations; the
combined work must satisfy both. Concord defines a global minimum policy.
Product/Domain/resource policy may strengthen that floor but never weaken it.
Concrete obligation bands are accepted in §Resolved research R2.

### D5. Specs are human-enacted laws

Spec-law conflicts are detected at every scope-changing boundary. Planning owns
clarification, spec evolution, and conscious governed scope reduction so execution
normally proceeds against settled law.

- Related requirements are grouped by root conflict.
- Unrelated conflicts remain separate.
- One planning checkpoint presents conflicts sequentially, one decision at a time.
- Agents detect, explain, and draft changes.
- Human approval is always required to enact law changes or governed scope cuts.
- A new execution-time conflict stops and returns to planning.
- Completion requires no unresolved law conflict.

### D6. Context-rich navigation, not a control center

Substantive approval, conflict resolution, editing,
history, resources, and planning happen inside the selected Product/workflow.

Every human question must first provide the purpose, relevant context, concrete
example, and consequence of each option. Concord never asks the operator to choose
between unexplained internal labels.

### D7. Durable authority is divided by fact type

- Concord operational memory owns live workflow state, blockers, approvals,
  versions, and active relations.
- Versioned Product knowledge owns accepted specs/laws, decisions, runbooks, and
  durable completion narratives.
- Search indexes, summaries, and browse views are rebuildable
  projections.

The same fact must never have two authorities.

### D8. Single operator per installation is permanent scope

Concord remains single-operator per installation. Multiple humans may run separate
Concord installations against shared repositories and git knowledge; their live
workflow memories remain independent. Concord does not grow shared assignments,
boards, identity, permissions, or team-server operation by default.

### D9. Initial release evidence is synthetic/scenario-based

PM1/TS1 deterministic conformance remains the pre-go-live release evidence gate.
Supported-model evaluation and production telemetry may inform research but do not
block a pre-go-live release. The first-go-live decision must define supported
populations and re-accept any future measurement gate.

### D10. Approved spec mandate prevents self-blocking execution

When planning approves a change that includes spec deltas, the approved change
contract carries a **spec mandate** listing exactly which specs that change is
authorized to modify. During execution:

- Modifications to mandated specs are authorized work, not law violations.
- Specs not listed in the mandate still enforce as laws.
- If execution discovers it needs to modify a spec outside the mandate, that is a
  genuine new conflict → routes back to planning.
- Completion verifies delivered changes match the approved mandate: no silent
  scope expansion or reduction.

This prevents the Advance failure mode where a change created to replace a spec
blocks itself during execution because the agent treats its own approved purpose as
a violation. The legislator (human) enacted the spec change during planning;
execution is enacting that approval.

## Resolved research (accepted 2026-08-06)

### R1. Workflow composition — forward-linked successors

A workflow may create/link the next workflow or work item, then finish. Each
workflow keeps independent durable authority and recovery. No nested child
execution; no parent waiting on child completion. Bounded parent/child is a
future extension only if a named scenario proves forward-linking is insufficient
and measured evidence justifies the complexity.

Research: [`research/R1-workflow-composition.md`](https://github.com/Sharper-Flow/concord/blob/a98dfe5db3fa2af53bd2d82829aec840e94e64c4/.concord/docs/research/R1-workflow-composition.md).

### R2. Concrete C16 obligation bands — independent bands with high-water-mark combine

Combine formula: `effective_rigor = floor ∪ max(maturity_obligation, audience_obligation)`.

Global floor (all work): stated purpose + owner, at least one proof artifact,
no silent weakening. Maturity bands: prototype (proof artifact), alpha
(functional verification), beta (draft SLOs + graduation criteria), production
(SLO + production readiness review (PRR) + monitoring + rollback), deprecated (sunset date + migration path).
Audience bands: operator_only (minimal threat model), limited (opt-in terms +
proportional review), public (full threat model + security review). Products may
strengthen but never weaken the floor. Prototype/operator_only/limited are
medium-confidence pending calibration against real work.

Research: [`research/R2-obligation-bands.md`](https://github.com/Sharper-Flow/concord/blob/a98dfe5db3fa2af53bd2d82829aec840e94e64c4/.concord/docs/research/R2-obligation-bands.md).

### R3. Workflow composition and boundary freshness

Typed `modifies`, hard/soft `depends_on`, and `forward_link` edges govern
workflow composition. Boundary checks validate pinned revisions. Heuristics may
suggest overlap but never author blocking edges. The completion verdict
classifies the delivered change. No polling, timers, or automatic downstream
rewrites.

## Consequences

### Positive

- Workflow ownership and native execution ownership have separate boundaries.
- Product rigor reflects declared responsibility rather than repository visibility.
- Human legislative authority and operator interaction are explicit.
- Single-operator scope avoids speculative team-server architecture.
- Release evidence does not depend on post-release calls before release exists.

### Cost

- Full coordination is a larger Product commitment than a registry-only workflow
  surface.
- Independent human installations may duplicate overlapping work; Concord does not
  solve live cross-human coordination.

## Supersession and amendments

R1–R3 are accepted. Any future amendment follows the standard operator-decision process.
