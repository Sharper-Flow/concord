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

### D1. Full successor; full system before usability

Concord is Advance's full successor. Concord may be designed, built, tested, replayed,
and shadow-evaluated incrementally, but no partial slice is called usable or becomes
the operator's primary coordination system. Replacement readiness requires the full
accepted operational floor.

### D2. Product-at-a-time, one-way migration

After Concord is fully replacement-ready, migration proceeds one **Product** at a
time—not one Project at a time.

- All Projects in that Product move together.
- Only deliberately selected active Advance changes are migrated.
- Durable specs, decisions, and Product knowledge remain shared through git.
- A migrated Product fixes forward in Concord; it does not roll back to Advance.
- Unmigrated Products remain in Advance until selected.
- Advance retires after the final Product moves.

This allows a bounded transition without splitting one Product's live authority
across systems.

### D3. Full workflow coordination engine; native effects

Concord owns durable workflow progression, branching, retries, evidence, recovery,
and the eventual accepted composition model. Native systems still own external
effects such as git, CI, cloud, database, and service-manager operations.

Workflow types are code-defined, versioned, purpose-built built-ins plus one
versioned generic workflow for true one-offs. Repeated generic patterns graduate
into purpose-built types. Arbitrary inline workflow languages are not accepted.

Workflow composition is accepted in §Resolved research R1.

### D4. Proportional rigor uses independent obligation bands

Replace the earlier `exposure` reachability axis with user-declared **audience
commitment**:

- `operator_only` — no stability commitment outside the operator;
- `limited` — known testers/early adopters may rely on it;
- `public` — general users may reasonably rely on it.

Audience commitment is independent of maturity, repository visibility, deployment
visibility, or observed traffic. Concord never infers it.

Maturity and audience commitment contribute independent evidence obligations; the
combined work must satisfy both. Concord defines a global minimum policy.
Product/component/resource policy may strengthen that floor but never weaken it.
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

The Product-first terminal launcher is a context-rich navigator. It shows enough
identity, state, focus, attention, and reliance information to choose what to open,
start, resume, or launch. Substantive approval, conflict resolution, editing,
history, resources, and planning happen inside the selected Product/workflow.

Every human question must first provide the purpose, relevant context, concrete
example, and consequence of each option. Concord never asks the operator to choose
between unexplained internal labels.

### D7. Durable authority is divided by fact type

- Concord operational memory owns live workflow state, blockers, approvals,
  versions, and active relations.
- Versioned Product knowledge owns accepted specs/laws, decisions, runbooks, and
  durable completion narratives.
- Search indexes, launcher rows, summaries, and browse views are rebuildable
  projections.

The same fact must never have two authorities.

### D8. Single operator per installation is permanent scope

Concord remains single-operator per installation. Multiple humans may run separate
Concord installations against shared repositories and git knowledge; their live
workflow memories remain independent. Concord does not grow shared assignments,
boards, identity, permissions, or team-server operation by default.

### D9. Initial release evidence is synthetic/scenario-based

PM1/TS1 deterministic conformance and supported-model scenario evaluation remain
release gates. A 500-call production baseline is **not** required before initial
release/cutover. Post-cutover aggregate telemetry may guide later TS9 stewardship but
cannot retroactively define initial usability.

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

Research: [`research/R1-workflow-composition.md`](../research/R1-workflow-composition.md).

### R2. Concrete C16 obligation bands — independent bands with high-water-mark combine

Combine formula: `effective_rigor = floor ∪ max(maturity_obligation, audience_obligation)`.

Global floor (all work): stated purpose + owner, at least one proof artifact,
no silent weakening. Maturity bands: prototype (proof artifact), alpha
(functional verification), beta (draft SLOs + graduation criteria), production
(SLO + PRR + monitoring + rollback), deprecated (sunset date + migration path).
Audience bands: operator_only (minimal threat model), limited (opt-in terms +
proportional review), public (full threat model + security review). Products may
strengthen but never weaken the floor. Prototype/operator_only/limited are
medium-confidence pending calibration against real work.

Research: [`research/R2-obligation-bands.md`](../research/R2-obligation-bands.md).

### R3. Cross-workflow impact propagation — declared edges + breaking verdict + boundary checks

Each workflow declares `modifies` and `depends_on` (hard/soft) edges. At
completion, compute reverse-edge set and write breaking/non-breaking notices
keyed by entity. Downstream checks notices only at consequential boundaries
(plan→exec, merge, ship) via one bounded query. Block only on declared hard edge
+ breaking change; everything else warns. Version-stamp freshness at boundaries
as deterministic fallback. Heuristics may suggest overlap but never author
blocking edges. No polling, timers, or automatic downstream rewrites.

Research: [`research/R3-impact-propagation.md`](../research/R3-impact-propagation.md).

## Consequences

### Positive

- Concord has a clear successor identity and non-ambiguous migration authority.
- Full workflow ownership no longer conflicts with native execution ownership.
- Product rigor reflects declared responsibility rather than repository visibility.
- Human legislative authority and operator interaction are explicit.
- Single-operator scope avoids speculative team-server architecture.
- Release evidence no longer depends on post-release calls before release exists.

### Cost

- Product-at-a-time migration creates a temporary period where different Products
  use different systems.
- Fix-forward migration requires strong pre-cutover evidence and correction paths.
- Full coordination is a larger Product commitment than a registry-only workflow
  surface.
- Independent human installations may duplicate overlapping work; Concord does not
  solve live cross-human coordination.

## Supersession and amendments

This decision narrows/supersedes conflicting root-policy prose in priorities,
clarifications, workflows, rollout, stage, self-documentation, and TS9 measurement
documents. It does not alter PM/CD-0005 implementation mechanics except where those
documents explicitly carried the superseded Product policy.

R1–R3 are accepted. Any future amendment follows the standard operator-decision process.
