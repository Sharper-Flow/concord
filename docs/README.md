# Concord

*Organized Product Development at Chaotic Speed.*

> **Status:** Public constitutional bootstrap snapshot; authority begins at the annotated `constitutional-bootstrap` tag.
> **Codename:** Concord (concordance: bringing scattered work into one view).
> **Created:** 2026-07-25, from predecessor-state analysis and product-design work.
> **Repository:** [`Sharper-Flow/concord`](https://github.com/Sharper-Flow/concord); Go module `github.com/sharper-flow/concord`.
> **Canonical priorities:** [`priorities.md`](./priorities.md)

Concord is a **Product-first, agent-native planning and coordination surface** for one operator and many concurrent local AI agents working on one machine. It is vision-led, not Advance-led: its operating envelope, ranked priorities, and quality governance are defined in [`priorities.md`](./priorities.md).

---

## How to read this set

1. Start with [`priorities.md`](./priorities.md) for the operating envelope, ranked priorities, and the predecessor relationship.
2. Read [`design-constraints.md`](./design-constraints.md) for the hard boundaries that shape architecture.
3. Read [`core-architecture.md`](./core-architecture.md) for the Go-core direction, state authority (CD-0002: SQLite sole authority), and consolidated resilience invariants.
4. Read [`clarifications.md`](./clarifications.md) for accepted decisions and the
   explicitly deferred questions that do not block the first storage slice.
5. Read [`rollout-plan.md`](./rollout-plan.md) for the dependency-driven sequence.

---

## Companion documents

| Document | What it owns |
|---|---|
| [`priorities.md`](./priorities.md) | Ranked priorities, operating envelope, Advance relationship, quality governance, open questions. **This file is the authority.** |
| [`core-architecture.md`](./core-architecture.md) | Go-core direction, state authority (CD-0002), consolidated resilience invariants. |
| [`design-constraints.md`](./design-constraints.md) | NFRs and hard constraints derived from the priorities. |
| [`rollout-plan.md`](./rollout-plan.md) | Sequencing and dependency-driven entry conditions. |
| [`advance-postmortem.md`](./advance-postmortem.md) | Evidence record of Advance's state-model and coordination failures; source for constraints §14–§19 and Research Backlog items 6–7. |
| [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) | Public, issue-linked lessons from the predecessor; non-authorizing design input. |
| [`provenance.md`](./provenance.md) | Public-safety boundary, preserved identifiers, and authority transition. |
| [`development-authority.md`](./development-authority.md) | Accepted GitHub/Git/docs authority model before replacement readiness. |
| [`installation.md`](./installation.md) | Release installation, Secret Service prerequisites, OpenCode registration, upgrade, and uninstall. |
| [`clarifications.md`](./clarifications.md) | Accepted clarification history plus explicitly deferred later-phase questions. |
| [`product-memory-query-contract.md`](./product-memory-query-contract.md) + [`product-memory-query.v1.json`](../scenarios/product-memory-query.v1.json) | **Accepted PM1** canonical query contract and golden corpus; binding input to PM2/PM3 and TS1/TS3 evaluation. |
| [`agent-tool-surface-jobs.md`](./agent-tool-surface-jobs.md) + [`agent-jobs.v1.json`](../scenarios/agent-jobs.v1.json) | **Accepted TS1:** eight canonical end-to-end agent jobs and 21 tool-neutral evaluation scenarios; binding input to TS2–TS9. |
| [`agent-tool-surface-budget.md`](./agent-tool-surface-budget.md) | **Accepted TS2:** at most nine always-visible domain tools, structural merge/split rules, static v1 exposure, and scenario-first candidate selection. |
| [`agent-read-tool-contract.md`](./agent-read-tool-contract.md) | **Accepted TS3:** four bounded read tools covering Product orientation, actionable work, work provenance, and durable knowledge. |
| [`agent-mutation-tool-contract.md`](./agent-mutation-tool-contract.md) | **Accepted TS4:** four intent mutation tools for defining, transitioning, relating, and compacting work; native systems retain external execution authority. |
| [`agent-call-context-contract.md`](./agent-call-context-contract.md) | **Accepted TS5:** core-verified ambient scope, capability grants, operation-bound approvals, expected versions, and durable retry identity. |
| [`agent-adapter-transport-contract.md`](./agent-adapter-transport-contract.md) | **Accepted TS6:** one global `concord.ts` custom-tool module adapting eight tools to the short-lived Go CLI; no plugin or MCP. |
| [`agent-result-envelope.md`](./agent-result-envelope.md) + [`agent-tool-envelope.schema.json`](../contracts/agent-tool-envelope.schema.json) | **Accepted TS7:** strict `ok|pending|partial|error` envelopes, typed recovery/evidence, authenticated pagination, and bounded output. |
| [`agent-tool-surface-evolution.md`](./agent-tool-surface-evolution.md) | **Accepted TS8:** canonical generated manifest, signed version negotiation, bounded 30–90 day deprecation, and no permanent aliases/discovery tool. |
| [`agent-tool-surface-measurement.md`](./agent-tool-surface-measurement.md) | **Accepted TS9:** deterministic/model launch floors, explicit telemetry populations, and scenario-first expansion/removal gates. |
| [`terminal-launcher-contract.md`](./terminal-launcher-contract.md) | **Candidate C18:** launcher screen set, navigation graph, ambient Product context, narrow action surface, no-poll refresh model, and the open terminal rendering-dependency question. Non-authorizing. |
| [`product-row-contract.md`](./product-row-contract.md) | **Accepted C14:** five-group Product-row glance projection covering identity, stage, reliance, action counts, and one deterministic focus item. |
| [`managed-resource-inventory.md`](./managed-resource-inventory.md) | **Accepted C15:** first-class resource identity, singular Product owner, consumer links, explicit stage, typed locators/work/replacement edges, and native execution authority. |
| [`decisions/CD-0005-concord-agent-tool-surface.md`](./decisions/CD-0005-concord-agent-tool-surface.md) | **Accepted CD-0005:** consolidated TS1–TS9 agent-surface architecture and invariants. |
| [`decisions/CD-0006-concord-root-product-policy.md`](./decisions/CD-0006-concord-root-product-policy.md) | **Accepted CD-0006:** full-successor intent, Product-at-a-time fix-forward migration, workflow constitution, rigor/governance policy, permanent single-operator scope, and synthetic release evidence. |
| [`decisions/CD-0007-concord-repository-bootstrap.md`](./decisions/CD-0007-concord-repository-bootstrap.md) | **Accepted CD-0007:** `Sharper-Flow/concord`, Go module identity, audited public-doc migration, MIT/public governance, one-version release/install, Linux amd64, privacy, workflow/conformance floor, and selective skills. |
| [`decisions/CD-0010-pre-readiness-development-authority.md`](./decisions/CD-0010-pre-readiness-development-authority.md) | **Accepted CD-0010:** GitHub-native development authority before Concord can replace its predecessor. |
| [`decisions/CD-0011-retain-sqlite-after-conformance.md`](./decisions/CD-0011-retain-sqlite-after-conformance.md) | **Accepted CD-0011:** retain direct local SQLite after reviewing environment-sensitive ten-process latency evidence; correctness and recovery remain clean, with explicit future reopen conditions. |
| [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) | **Accepted CD-0008:** one shared Product authority with isolated worktree sets, immutable-subject evidence binding, dependency-aware unreadable-record policy, workflow checkpoints/attempt fencing, typed external conditions, event upcasters/history reads, and confirmed SQLite authority with alternative comparison only after a falsifier. |
| [`decisions/CD-0009-active-research-context.md`](./decisions/CD-0009-active-research-context.md) | **Accepted CD-0009:** Epics and research remain ordinary work-item kinds; active research packs are versioned SQLite working context, never retained events/Git knowledge, and are deleted after proof-backed archive compaction. |
| [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md) | **Accepted CD-0012:** three-part outcome contract (premise, required end-state, candidate set) with separate revision authorities; end-state approved at planning beside the CD-0006 D10 spec mandate and verified at completion; strengthen-only delivery comparison; mid-execution discoveries forward-link rather than substitute. |
| [`decisions/CD-0013-workflow-engine-mechanism.md`](./decisions/CD-0013-workflow-engine-mechanism.md) | **Accepted CD-0013:** workflow definitions as code-defined versioned registry, workflow state as work-item-subject events with typed projections, closed outcome-predicate union, executing-identity evaluator distinctness, entity-keyed impact notices, forward-link-only composition, and a completion gate binding evidence, conditions, spec mandate, verdict, and premise confirmation in one transaction. |
| [`workflow-engine-contract.md`](./workflow-engine-contract.md) + [`workflow-engine.v1.json`](../scenarios/workflow-engine.v1.json) + [`workflow-definition.schema.json`](../contracts/workflow-definition.schema.json) | **CD-0013 implementation contract:** registry digest pinning, V15 projections/folds, closed event payloads, outcome comparison, completion gate, conditions, impact notices, dispatch, family graphs, strict schemas, and all 47 conformance carriers. |
| [`research/R4-competitive-mechanism-hardening.md`](./research/R4-competitive-mechanism-hardening.md) | **Accepted through CD-0008:** ranked mechanism findings across Beads/Dolt, LangGraph, Restate/DBOS, Letta, Herdr, Claude, Jido, Qodo, Devin, Orca, and Superset; includes SQLite ten-process conformance and target multi-agent worktree topology. |
| [`research/R5-goal-to-outcome-binding.md`](./research/R5-goal-to-outcome-binding.md) | **Accepted through CD-0012:** measured goal drift, specification gaming, and goal misgeneralization evidence; mechanism and formalism surveys; four explicit insufficient-evidence findings; and the counter-evidence that shapes the contract. |
| [`product-memory-authority-scope.md`](./product-memory-authority-scope.md) | **Accepted PM2:** one global local SQLite authority per Concord installation/operator-machine. |
| [`product-memory-domain-schema.md`](./product-memory-domain-schema.md) | **Accepted PM3, amended by CD-0009:** generic authoritative event log plus explicit typed Product-memory projections; retention-bounded active research context is the sole direct-table exception. |
| [`product-memory-lifecycle-relations.md`](./product-memory-lifecycle-relations.md) | **Accepted PM4:** five-state work lifecycle, derived blocked/ready views, canonical typed relations, atomic supersession, and cycle rejection. |
| [`product-memory-membership.md`](./product-memory-membership.md) | **Accepted PM5:** many-to-many Product/Project/work membership, one canonical work identity, derived cross-Product scope, optional singular primary roles, and atomic scope edits. |
| [`canonical-git-note-placement.md`](./canonical-git-note-placement.md) | **Accepted PM6:** deterministic Product-home/primary-Project note placement, typed ambiguity, one content-addressed in-tree note, and git-first publish proof. |
| [`compaction-retention-policy.md`](./compaction-retention-policy.md) | **Accepted PM7:** bounded lazy projection pruning, retained event authority, immutable pruned IDs, git-rebuildable historical index, and separate `archived_work_linked` follow-up events—not PM4 live relations. |
| [`workflows.md`](./workflows.md) | Purpose-built workflow types. |
| [`decisions/`](./decisions/) | Concord decision records (`CD-NNNN`) produced by architecture spikes. Binding until superseded. |
| [`architecture-spike.md`](./architecture-spike.md) | The architecture-spike workflow type: Epic entries that decide rather than ship; binding decision records and supersession. |
| [`product-data-model.md`](./product-data-model.md) | Product ownership and membership model, lifecycle stage, shared resources, and the replacement relation. |
| [`specs-as-laws.md`](./specs-as-laws.md) | Spec-law conflict surfacing and evolution. |
| [`self-documentation.md`](./self-documentation.md) | Browsable specs and durable workflow docs. |
| [`feature-inventory.md`](./feature-inventory.md) | Capability inventory and placement rubric. |
| [`capability-placement.md`](./capability-placement.md) | Where each capability belongs by shape, including external/native ownership. |
| [`market-landscape.md`](./market-landscape.md) | Competitor and adjacent-tool research. |
| [`vertical-integration.md`](./vertical-integration.md) | Product-scoping for lgrep / vision / episode / ZLauncher. |

---

## Current status

Concord is a **clean, simplicity-first rewrite** of Advance. Advance is a
**predecessor**, not a prerequisite: its state-model failures (documented in
[`advance-postmortem.md`](./advance-postmortem.md)) are Concord's founding
anti-pattern evidence, not a gate Concord waits on. The recurring root cause
across Advance's 2026-08-02→08-05 failure cluster is **split state authority**
between Temporal and a disk projection plus the reconciliation machinery around
it; that shape is what Concord's design structurally prevents.

> **Operator decision (2026-08-05).** Advance is reclassified from a
> stabilization prerequisite to a predecessor / anti-pattern source. The
> "Concord implementation may begin only when Advance is healthy" gating model
> is **retired**. [`rollout-plan.md`](./rollout-plan.md) §3/§5 and the
> "Advance healthy" subset alias are retained as historical record and lesson
> evidence, not as active gates.

CD-0002/CD-0003 remain the accepted storage baseline as narrowed by PM2 and PM3:
**one global local SQLite authority** holds one generic authoritative event log
plus explicit typed Product-memory projections. PM3 supersedes CD-0003 D1's
generic materialized `entities` spine; PM4 fixes lifecycle and work-relation
semantics; PM5 fixes membership and cross-Product scope. **The PM1–PM5 storage
build-authorizing sequence is complete; the storage acceptance slice may begin after
the public bootstrap snapshot is tagged and the accepted development-authority rules
are in force.**
PM6 fixes canonical durable-note
placement; accepted PM7 fixes projection retention; accepted PM8 excludes WIP-byte
storage and generic screenshot requirements; accepted PM9 rejects a separate exhaust
receipt; accepted PM10 defines SQLite/Git disaster recovery. **TS1–TS9 are accepted
and consolidated by CD-0005; adapter/tool scaffolding may begin and release remains
subject to the accepted TS9 evidence gate as narrowed by CD-0006.** C14/C15 now fix Product rows and managed
resources. Storage tables and Go CLI commands do not automatically become tools.
CD-0006 fixes Concord's root Product policy and accepts workflow composition,
concrete C16 obligations, and structural cross-workflow impact propagation.
CD-0007 fixes the public repository, migration, governance, release/install,
platform/privacy, workflow/conformance, and skill boundaries. CD-0010 fixes the
pre-readiness development authority; Concord must not self-host its own development
workflow before replacement readiness.
CD-0012 gives Priority 2's *intent fidelity* and *no silent drift* attributes a mechanism,
extending CD-0006 D10's approved-mandate pattern from specs authorized for modification to
end-state required for delivery: a delivered outcome weaker than the approved one fails,
and work discovered mid-execution forward-links rather than substituting.
The ordered decisions, current non-authorizing leans, evidence plans,
dependencies, and decision artifacts live in
[`clarifications.md`](./clarifications.md) under the Product-memory and minimal
tool-surface backlogs. Planning and source-backed research may continue.
Storage/core and agent-tool scaffolding may begin after the constitutional snapshot
is tagged and the accepted public migration completes.
Benchmarks, PoCs, and performance/crash harnesses are
implementation-acceptance evidence after the relevant decisions are accepted—not
architecture-decision prerequisites.

---

*Concord is a direction, not a deadline. Each phase earns the next through evidence, not calendar pressure.*
