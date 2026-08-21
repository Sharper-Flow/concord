# Concord

*Organized Product Development at Chaotic Speed.*

> **Status:** Public constitutional bootstrap snapshot; authority begins at the annotated `constitutional-bootstrap` tag.
> **Codename:** Concord (concordance: bringing scattered work into one view).
> **Created:** 2026-07-25, from predecessor-state analysis and product-design work.
> **Repository:** [`Sharper-Flow/concord`](https://github.com/Sharper-Flow/concord); Go module `github.com/sharper-flow/concord`.
> **Canonical priorities:** [`priorities.md`](./priorities.md)

Concord is a **Product-law-first, agent-native planning and coordination
surface** for one operator and many concurrent local AI agents working on one
machine. Product → Domain architecture binds specifications, work, and evidence
so independently clean changes cannot silently enact contradictory Product
truth. Concord is vision-led, not Advance-led: its operating envelope, ranked
priorities, and quality governance are defined in [`priorities.md`](./priorities.md).

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
| [`floor-readiness.md`](./floor-readiness.md) + [`floor-readiness.v1.json`](./floor-readiness.v1.json) | **Authorizing readiness record:** validated per-item state of every first-usable floor condition, distinguishing satisfied, outstanding, unmeasured, and out-of-scope. |
| [`law-coverage.v1.json`](./law-coverage.v1.json) | **Authorizing coverage record:** how each indexed law record is proved, by typed anchor rather than by repository path; an indexed record the manifest omits is a validator finding (CD-0047). |
| [`reachability-exceptions.v1.json`](./reachability-exceptions.v1.json) | **Authorizing exception record:** functions no `cmd/concord` invocation reaches that the repository has decided to keep; reachability itself is computed, so an undeclared unreachable function is a validator finding (CD-0047). |
| [`predecessor-operational-coverage.md`](./predecessor-operational-coverage.md) | **Authorizing for floor condition 6:** predecessor operational territory enumerated by outcome; the fc6 bar is covered-with-evidence or excluded-with-reason, with nothing left not covered (see `floor-readiness.v1.json`). |
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
| [`concord-knowledge-index.md`](./concord-knowledge-index.md) + [`concord-knowledge-index.v1.json`](./concord-knowledge-index.v1.json) | Manifest-primary durable decisions, specs, and lessons; strict blob proofs, scope modes, and typed research availability. |
| [`agent-tool-surface-jobs.md`](./agent-tool-surface-jobs.md) + [`agent-jobs.v1.json`](../scenarios/agent-jobs.v1.json) | **Accepted TS1:** eight canonical end-to-end agent jobs and 23 tool-neutral evaluation scenarios; binding input to TS2–TS9. |
| [`agent-tool-surface-budget.md`](./agent-tool-surface-budget.md) | **Accepted TS2:** at most nine always-visible domain tools, structural merge/split rules, static v1 exposure, and scenario-first candidate selection. |
| [`agent-read-tool-contract.md`](./agent-read-tool-contract.md) | **Accepted TS3:** four bounded read tools covering Product orientation, actionable work, work provenance, and durable knowledge. |
| [`agent-mutation-tool-contract.md`](./agent-mutation-tool-contract.md) | **Accepted TS4:** four intent mutation tools for defining, transitioning, relating, and compacting work; native systems retain external execution authority. |
| [`agent-call-context-contract.md`](./agent-call-context-contract.md) | **Accepted TS5:** core-verified ambient scope, capability grants, operation-bound approvals, expected versions, and durable retry identity. |
| [`agent-adapter-transport-contract.md`](./agent-adapter-transport-contract.md) | **Accepted TS6:** one global `concord.ts` custom-tool module adapting the generated current tool set to the short-lived Go CLI; no plugin or MCP. |
| [`agent-result-envelope.md`](./agent-result-envelope.md) + [`agent-tool-envelope.schema.json`](../contracts/agent-tool-envelope.schema.json) | **Accepted TS7, amended by issue #43:** strict `ok|pending|partial|error` envelopes, producer-validated closed results, typed recovery/evidence, authenticated pagination, and bounded output. |
| [`agent-tool-surface-evolution.md`](./agent-tool-surface-evolution.md) | **Accepted TS8, amended by CD-0042:** one generated current manifest identified by exact digest; static registration, strict generation, fail-closed mismatch, and explicit first-go-live evolution trigger. |
| [`agent-tool-surface-measurement.md`](./agent-tool-surface-measurement.md) | **Accepted TS9, amended by CD-0042:** pre-go-live PM1/TS1 deterministic evidence, strict schemas, authority/transaction/negative/conformance proof; model trials and telemetry are research only before go-live. |
| [`terminal-launcher-contract.md`](./terminal-launcher-contract.md) | **Accepted C18 contract; S1 and issue #51 S2/S3 slice implemented:** read-only Product launcher — screen set, navigation graph, knowledge-in-context, ambient Product context, identity-only launch handoff with CD-0031 core session boot, no-poll refresh model, Bubble Tea v2 adapter, and Product-only query scope. Replacement readiness remains unclaimed. |
| [`product-row-contract.md`](./product-row-contract.md) | **Accepted C14:** five-group Product-row glance projection covering identity, stage, reliance, action counts, and one deterministic focus item. |
| [`product-coordination-view.md`](./product-coordination-view.md) | **Accepted C17:** Product coordination drill-down behind the C14 row: a structural Q8 relation tree plus a ranked Q5/Q4 work table, with bounded Product-scoped reads and visible incomplete coverage. |
| [`managed-resource-inventory.md`](./managed-resource-inventory.md) | **Accepted C15:** first-class resource identity, singular Product owner, consumer links, explicit stage, typed locators/work/replacement edges, and native execution authority. |
| [`decisions/CD-0005-concord-agent-tool-surface.md`](./decisions/CD-0005-concord-agent-tool-surface.md) | **Accepted CD-0005:** consolidated TS1–TS9 agent-surface architecture and invariants. |
| [`decisions/CD-0006-concord-root-product-policy.md`](./decisions/CD-0006-concord-root-product-policy.md) | **Accepted CD-0006:** full-successor intent, Product-at-a-time fix-forward migration, workflow constitution, rigor/governance policy, permanent single-operator scope, and synthetic release evidence. |
| [`decisions/CD-0007-concord-repository-bootstrap.md`](./decisions/CD-0007-concord-repository-bootstrap.md) | **Accepted CD-0007:** `Sharper-Flow/concord`, Go module identity, audited public-doc migration, MIT/public governance, one-version release/install, Linux amd64, privacy, workflow/conformance floor, and selective skills. |
| [`decisions/CD-0010-pre-readiness-development-authority.md`](./decisions/CD-0010-pre-readiness-development-authority.md) | **Accepted CD-0010:** GitHub-native development authority before Concord can replace its predecessor. |
| [`decisions/CD-0011-retain-sqlite-after-conformance.md`](./decisions/CD-0011-retain-sqlite-after-conformance.md) | **Accepted CD-0011:** retain direct local SQLite after reviewing environment-sensitive ten-process latency evidence; correctness and recovery remain clean, with explicit future reopen conditions. |
| [`decisions/CD-0008-concord-mechanism-hardening.md`](./decisions/CD-0008-concord-mechanism-hardening.md) | **Accepted CD-0008:** one shared Product authority with isolated worktree sets, immutable-subject evidence binding, dependency-aware unreadable-record policy, workflow checkpoints/attempt fencing, typed external conditions, event upcasters/history reads, and confirmed SQLite authority with alternative comparison only after a falsifier. |
| [`decisions/CD-0009-active-research-context.md`](./decisions/CD-0009-active-research-context.md) | **Accepted CD-0009, amended by CD-0041:** Initiative and research are ordinary work-item kinds; Initiative is secondary business/outcome context, while active research packs remain versioned SQLite working context and are deleted after proof-backed archive compaction. |
| [`decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md`](./decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md) | **Accepted CD-0012:** three-part outcome contract (premise, required end-state, candidate set) with separate revision authorities; end-state approved at planning beside the CD-0006 D10 spec mandate and verified at completion; strengthen-only delivery comparison; mid-execution discoveries forward-link rather than substitute. |
| [`decisions/CD-0013-workflow-engine-mechanism.md`](./decisions/CD-0013-workflow-engine-mechanism.md) | **Accepted CD-0013:** workflow definitions as code-defined versioned registry, workflow state as work-item-subject events with typed projections, closed outcome-predicate union, executing-identity evaluator distinctness, entity-keyed impact notices, forward-link-only composition, and a completion gate binding evidence, conditions, spec mandate, verdict, and premise confirmation in one transaction. |
| [`decisions/CD-0014-terminal-launcher-rendering.md`](./decisions/CD-0014-terminal-launcher-rendering.md) | **Accepted CD-0014:** Bubble Tea v2 behind an isolated launcher adapter, Product-only query scope, no autonomous state polling, exact dependency/license inventory, and tcell v3 fallback/reopen rules. |
| [`decisions/CD-0015-typed-law-relations.md`](./decisions/CD-0015-typed-law-relations.md) | **Accepted CD-0015:** Git-authoritative typed law relations, derived SQLite projection, and bounded workflow conflict checks with an explicit amendment path. |
| [`decisions/CD-0016-context-continuity.md`](./decisions/CD-0016-context-continuity.md) | **Accepted CD-0016:** durable context checkpoints and boundaries, derived pinned continuity, summary-only fallback, and closed restart unavailability until #120. |
| [`decisions/CD-0017-typed-workers-and-model-routing.md`](./decisions/CD-0017-typed-workers-and-model-routing.md) | **Accepted CD-0017:** Concord-owned typed lane registry with capability-class model routing, pinned preferred dispatch, recorded declared fallback resolution and readback identity evidence, workflow-declared reviewer/model distinctness, and a hard worker authority boundary preserving R1/CD-0013. |
| [`decisions/CD-0018-declared-urgency-and-provenance.md`](./decisions/CD-0018-declared-urgency-and-provenance.md) | **Accepted CD-0018, amended by CD-0041:** closed two-band urgency plus typed `raised_from` provenance; no assignment or lease, while Product-changing parallel safety now uses Domain-overlap detection and operator-approved resolution. |
| [`decisions/CD-0019-predecessor-strength-preservation.md`](./decisions/CD-0019-predecessor-strength-preservation.md) | **Accepted CD-0019:** six predecessor strengths (specs, narrative artifacts, knowledge index, conformance, triage, reflection) preservation-mandated, each research-informed in its Concord shape rather than inherited; umbrella mandate binding existing partial treatments. |
| [`decisions/CD-0020-retain-knowledge-index-shape.md`](./decisions/CD-0020-retain-knowledge-index-shape.md) | **Accepted CD-0020:** retain the manifest-primary, Git-authoritative, SQLite-derived knowledge-index shape; keep Q9/Q10 bounded, compose with independent code intelligence, and repair conformance drift without expanding the architecture. |
| [`decisions/CD-0021-floor-condition-1-scope.md`](./decisions/CD-0021-floor-condition-1-scope.md) | **Accepted CD-0021:** floor condition 1 means reach, not an operator authoring surface; planning happens through an agent session and cross-Product result sets stay excluded. |
| [`decisions/CD-0022-active-research-finding-scope.md`](./decisions/CD-0022-active-research-finding-scope.md) | **Accepted CD-0022:** active research findings reuse durable knowledge’s applies-to scope vocabulary while keeping active context disposable and writerless. |
| [`decisions/CD-0023-verdict-read-scope.md`](./decisions/CD-0023-verdict-read-scope.md) | **Accepted CD-0023:** recorded verdicts become readable, read-scoped results — every session except the recorded executing actor audits an acceptance; influence stays with CD-0013 D5. |
| [`decisions/CD-0024-epic-agent-surface-and-ts9-exception.md`](./decisions/CD-0024-epic-agent-surface-and-ts9-exception.md) | **Accepted CD-0024, amended by CD-0041 and CD-0042:** the versioned Epic surface and one-time TS9 exception are historical evidence only; no current compatibility path exists before go-live. |
| [`decisions/CD-0025-research-surface.md`](./decisions/CD-0025-research-surface.md) | **Accepted CD-0025:** research authoring on `concord_work_define` under a `research` capability, one read path on `concord_work_trace.research`, and engine-proven reliance — `workflow_action` declares bindings that bind consumers and fail closed on stale required revisions. |
| [`decisions/CD-0026-learning-capture.md`](./decisions/CD-0026-learning-capture.md) | **Accepted CD-0026:** lessons publish through `concord_work_compact.lesson_publish` with operator approval into git and the manifest; promotion is explicit scope; reflections are tagged lessons; manifest records carry `evidence` paths the validator keeps honest as a structural drift audit. |
| [`decisions/CD-0027-typed-restart-excluded.md`](./decisions/CD-0027-typed-restart-excluded.md) | **Accepted CD-0027:** typed restart after a boundary is deliberately excluded — CD-0016's per-call re-derived pinned continuity already prevents silent authority loss; restart would only preserve in-flight working memory, which the host owns. |
| [`decisions/CD-0028-resource-claims.md`](./decisions/CD-0028-resource-claims.md) | **Accepted CD-0028:** durable, typed resource claims — records of intent, not locks; holders are work items so claims survive session death and end at terminal transition; advisory and honest about foreign mutation. |
| [`decisions/CD-0029-peer-messages.md`](./decisions/CD-0029-peer-messages.md) | **Accepted CD-0029:** peer messages are durable events addressed to work — direct or Product broadcast, delivered pull-at-next-call through a continuity pending-count, withdrawable, carrying no authority. |
| [`decisions/CD-0030-mid-execution-observations.md`](./decisions/CD-0030-mid-execution-observations.md) | **Accepted CD-0030:** observations are the durable form of "I noticed something" — a lightweight mid-life record on work items, visible at resume through continuity, carrying no authority; promotion to work stays the unchanged CD-0018 path. |
| [`decisions/CD-0031-core-derived-session-boot.md`](./decisions/CD-0031-core-derived-session-boot.md) | **Accepted CD-0031:** launcher-started operator sessions receive a core-derived, versioned continuity packet before OpenCode starts; the launcher remains identity-only. |
| [`decisions/CD-0033-out-of-band-direction.md`](./decisions/CD-0033-out-of-band-direction.md) | **Accepted CD-0033:** operator direction of agent labor stays out-of-band; its effects on a declared end-state enter through contract supersession — the single write path — with clause 5's premise confirmation restated as the check and the unverifiable trigger documented with its falsifier. |
| [`decisions/CD-0034-host-prompt-provenance.md`](./decisions/CD-0034-host-prompt-provenance.md) | **Accepted CD-0034:** host prompt injection is permitted only when recorded — dispatch evidence v3 carries a host provenance digest over the enumerable injection surfaces; unenumerable surfaces are named; D7 evals are reproducible only when both digests match. |
| [`decisions/CD-0035-governing-requirements-at-capture.md`](./decisions/CD-0035-governing-requirements-at-capture.md) | **Accepted CD-0035:** governing requirements bind to a Project scope and capture refuses by set difference, never by reading the instruction; the refusal carries typed operator `options` and mints the challenge an approved scope cut resolves against. |
| [`decisions/CD-0036-breaking-law-cutovers.md`](./decisions/CD-0036-breaking-law-cutovers.md) | **Accepted CD-0036:** contracts pin exact law revisions; same-ID edits are compatible amendments, while a law `supersedes` relation strictly quiesces old consumers until they re-contract or become terminal. |
| [`decisions/CD-0037-core-derived-approval-consequence-summaries.md`](./decisions/CD-0037-core-derived-approval-consequence-summaries.md) | **Accepted CD-0037:** core-issued approval challenges carry a typed consequence summary derived only from the exact operation, consequence, digest, scope, versions, and expiry the approval binds. |
| [`decisions/CD-0038-per-operation-seconds-budgets.md`](./decisions/CD-0038-per-operation-seconds-budgets.md) | **Accepted CD-0038:** every operation declares a seconds-denominated ceiling in the manifest; unsupported requests refuse before effects and accepted budgets become one propagated deadline, never a silent clamp. |
| [`decisions/CD-0039-attributed-native-run-outcomes.md`](./decisions/CD-0039-attributed-native-run-outcomes.md) | **Accepted CD-0039:** native-run outcomes are typed reports attributed to the authenticated trusted client; failed health plus rollback records durable status and returns a workflow-action partial without making Concord the native executor. |
| [`decisions/CD-0040-verifiable-external-observations.md`](./decisions/CD-0040-verifiable-external-observations.md) | **Accepted CD-0040:** external observations share typed provenance and append-only verification; presence is broad, while absence and consequential use require pinned completeness/current proof. |
| [`decisions/CD-0041-architecture-bound-product-law.md`](./decisions/CD-0041-architecture-bound-product-law.md) | **Accepted CD-0041:** Product law and architectural concordance become Priority 1; canonical Domains own law and bind Product-changing work; concurrent Domain overlap requires version-pinned resolution; Initiative is secondary context; SQLite remains sole local authority. |
| [`decisions/CD-0042-pre-go-live-single-path.md`](./decisions/CD-0042-pre-go-live-single-path.md) | **Accepted CD-0042:** pre-go-live Concord has one digest-identified generated agent surface; obsolete version, compatibility, deprecation, supported-model gate, and unreleased replay paths are deleted; deterministic authority evidence remains required. |
| [`decisions/CD-0043-host-owned-lane-methodology.md`](./decisions/CD-0043-host-owned-lane-methodology.md) | **Accepted CD-0043:** Concord owns the lane contract, evidence boundary, and provenance record but never lane methodology; methodology reaches a dispatch only through the CD-0034 enumerable host instruction surface; `skills/` stays reserved with a closed reason; deferring coverage rows name the host owner. |
| [`decisions/CD-0044-worker-evidence-caller-authentication.md`](./decisions/CD-0044-worker-evidence-caller-authentication.md) | **Accepted CD-0044:** worker-evidence CLI writes authenticate their caller with a signed `worker-evidence-v1` assertion bound to the exact attempt, lane, routing, and provenance identity; the nonce is consumed in the appending transaction; `worker_evidence` is client-policy authority never carried by a grant; a terminal attempt refuses further evidence; the recorded actor is a verified client. |
| [`decisions/CD-0045-writer-admission-invariant.md`](./decisions/CD-0045-writer-admission-invariant.md) | **Accepted CD-0045:** the concurrency invariant states bounded writer admission rather than zero waiting; writer-admission wait, commit duration, and escaped busy failure are distinct measured quantities; correctness precedes latency and population identity is part of the bound; CD-0011 keeps sole ownership of the reopen conditions. |
| [`decisions/CD-0046-conformance-population-provenance.md`](./decisions/CD-0046-conformance-population-provenance.md) | **Accepted CD-0046:** conformance population authority is invocation provenance established by the workflow setting `CONCORD_ACCEPTANCE_RUNNER=1`, not measured host isolation; the resolver is a pure function over profile and signal, the falsifier verdict is gated on the resolved authority, a CI tripwire makes a missing signal a visible failure, and CD-0011's "structural" wording is amended to match the enforceable claim. |
| [`decisions/CD-0047-declared-coverage-state.md`](./decisions/CD-0047-declared-coverage-state.md) | **Accepted CD-0047:** every indexed law record carries a coverage state; a `satisfied` record cites typed anchors that resolve and execute in a required check; product reachability is computed by a pinned callgraph analysis with only exceptions declared; both planes reuse the floor-readiness state vocabulary and fail on an undeclared gap in either direction. |
| [`decisions/CD-0048-s2-answer-stack-composition.md`](./decisions/CD-0048-s2-answer-stack-composition.md) | **Accepted CD-0048:** S2 composes as a panel stack in §3's job order — governing Domain with overlap state, blocked and blockers, next work; collapsed panels carry store-materialized answers and their reasons; ordering stays on the stored rank and deterministic store tiers; the closed screen set, widget floor, and read-only surface are unchanged, and §5's Tab row becomes panel-focus cycling. |
| [`decisions/CD-0049-agent-delivery-and-identity-assertion.md`](./decisions/CD-0049-agent-delivery-and-identity-assertion.md) | **Accepted CD-0049:** Concord-owned lane definitions reach a host only through a project's `.opencode/agents/`, because global placement cannot be scoped and an environment-supplied definition presents no file for CD-0034 provenance to hash; Concord asserts required agent identity itself before launch rather than relying on `--agent`, whose unknown-name failure mode is a silent substitution with a success exit code; the orchestrator is constrained and verified rather than authored, since CD-0016 excludes an `agent_definitions` table; a session with absent identity refuses to start. |
| [`agent-lanes-contract.md`](./agent-lanes-contract.md) + [`agent-lanes.v1.json`](../contracts/agent-lanes.v1.json) + [`agent-lanes.schema.json`](../contracts/agent-lanes.schema.json) | **CD-0017 Phase 1:** generated closed lane registry, packet/report schemas, pinned models, budgets, lifecycle states, and evidence obligations. |
| [`routing-policy-contract.md`](./routing-policy-contract.md) + [`routing-policy.v1.json`](../contracts/routing-policy.v1.json) + [`routing-policy.schema.json`](../contracts/routing-policy.schema.json) | **CD-0017 amendment:** generated capability-class resolution sets, pinned preferred-model cross-validation, declared fallback reasons, and digest-pinned routing evidence. |
| [`workflow-engine-contract.md`](./workflow-engine-contract.md) + [`workflow-engine.v1.json`](../scenarios/workflow-engine.v1.json) + [`workflow-definition.schema.json`](../contracts/workflow-definition.schema.json) | **CD-0013 implementation contract, amended by CD-0016:** registry digest pinning, V15/V22 projections and folds, closed event payloads, outcome comparison, completion gate, conditions, impact notices, context continuity, dispatch, family graphs, strict schemas, and all 48 conformance carriers. |
| [`research/R4-competitive-mechanism-hardening.md`](./research/R4-competitive-mechanism-hardening.md) | **Accepted through CD-0008:** ranked mechanism findings across Beads/Dolt, LangGraph, Restate/DBOS, Letta, Herdr, Claude, Jido, Qodo, Devin, Orca, and Superset; includes SQLite ten-process conformance and target multi-agent worktree topology. |
| [`research/R5-goal-to-outcome-binding.md`](./research/R5-goal-to-outcome-binding.md) | **Accepted through CD-0012:** measured goal drift, specification gaming, and goal misgeneralization evidence; mechanism and formalism surveys; four explicit insufficient-evidence findings; and the counter-evidence that shapes the contract. |
| [`research/R6-typed-workers-and-model-routing.md`](./research/R6-typed-workers-and-model-routing.md) | **Spike findings for issue #57:** current-law trace; A/B/C ownership comparison; OpenCode host feasibility for pinned-agent dispatch, dispatch gating, and model-identity readback; capability-class routing with typed fallback; explicit insufficient-evidence findings including the deferred same/cross-model measurement. |
| [`research/R7-expedited-parallel-work.md`](./research/R7-expedited-parallel-work.md) | **Spike findings for issue #70:** current-law trace of the owners/assignments exclusion against the many-concurrent-agents envelope; evidence that the priority ranking path is already complete; prior-art survey of urgency bands and non-blocking relation vocabularies; four options; five insufficient-evidence findings including the agent-authored-priority tension with C17 §5.1. |
| [`research/R8-mid-execution-discovery.md`](./research/R8-mid-execution-discovery.md) | **Spike findings for issue #99:** silent-drop survey, touched-refs soundness analysis (unsound as gate owner), root-cause finding (no cheap recording act), and the selected observation mechanism. |
| [`product-memory-authority-scope.md`](./product-memory-authority-scope.md) | **Accepted PM2:** one global local SQLite authority per Concord installation/operator-machine. |
| [`product-memory-domain-schema.md`](./product-memory-domain-schema.md) | **Accepted PM3, amended by CD-0009:** generic authoritative event log plus explicit typed Product-memory projections; retention-bounded active research context is the sole direct-table exception. |
| [`product-memory-lifecycle-relations.md`](./product-memory-lifecycle-relations.md) | **Accepted PM4:** five-state work lifecycle, derived blocked/ready views, canonical typed relations, atomic supersession, and cycle rejection. |
| [`product-memory-membership.md`](./product-memory-membership.md) | **Accepted PM5:** many-to-many Product/Project/work membership, one canonical work identity, derived cross-Product scope, optional singular primary roles, and atomic scope edits. |
| [`canonical-git-note-placement.md`](./canonical-git-note-placement.md) | **Accepted PM6:** deterministic Product-home/primary-Project note placement, typed ambiguity, one content-addressed in-tree note, and git-first publish proof. |
| [`compaction-retention-policy.md`](./compaction-retention-policy.md) | **Accepted PM7:** bounded lazy projection pruning, retained event authority, immutable pruned IDs, git-rebuildable historical index, and separate `archived_work_linked` follow-up events—not PM4 live relations. |
| [`workflows.md`](./workflows.md) | Purpose-built workflow types. |
| [`decisions/`](./decisions/) | Concord decision records (`CD-NNNN`) produced by architecture spikes. Binding until superseded. |
| [`architecture-spike.md`](./architecture-spike.md) | The architecture-spike workflow type: Initiative entries that decide rather than ship; binding decision records and supersession. |
| [`product-data-model.md`](./product-data-model.md) | Product ownership, canonical Domain architecture, Initiative's secondary role, lifecycle stage, shared resources, and typed replacement homes. |
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
and consolidated by CD-0005; adapter/tool scaffolding may begin and pre-go-live
changes remain subject to the deterministic TS9 evidence contract.** C14/C15 now fix Product rows and managed
resources. Storage tables and Go CLI commands do not automatically become tools.
CD-0006 fixes Concord's root Product policy and accepts workflow composition,
concrete C16 obligations, and structural cross-workflow impact propagation.
CD-0007 fixes the public repository, migration, governance, release/install,
platform/privacy, workflow/conformance, and skill boundaries. CD-0010 fixes the
pre-readiness development authority; Concord must not self-host its own development
workflow before replacement readiness.
CD-0012 gives Priority 3's *intent fidelity* and *no silent drift* attributes a mechanism,
extending CD-0006 D10's approved-mandate pattern from specs authorized for modification to
end-state required for delivery: a delivered outcome weaker than the approved one fails,
and work discovered mid-execution forward-links rather than substituting.
CD-0016 gives Priority 4's continuity requirement a durable mechanism: bounded
workflow checkpoints and boundaries, pinned state derived from authority, and a
canonical continuity read. Summary prose is never an authority source, and
typed-agent restart remains unavailable until issue #120.
CD-0041 makes Product law and architectural concordance Priority 1. It replaces
opaque component authority with canonical Domains, binds Product-changing work
to exact Domain/law footprints, requires version-pinned resolution for concurrent
 Domain overlap, establishes Initiative as the Product grouping kind, and retains CD-0002/
CD-0011 SQLite authority. Its runtime mechanisms remain follow-up work; this
constitutional record does not claim the floor is satisfied.
CD-0042 amends the pre-go-live agent-surface path: the generated manifest digest is
the only current identity, obsolete compatibility machinery is removed, and first
go-live must define any future compatibility and measurement law.
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
