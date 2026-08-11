# Concord Clarifications & Open Questions

> **Status:** Living doc. Companion to [`README.md`](./README.md) and the others.
> **Purpose:** The unresolved decisions and ambiguities that need operator
> direction before Concord moves from vision to build, plus resolved directions
> that already have an answer and must be honored by the document set.
> **Origin:** Session 2026-07-25.

## How to use this

Each item carries: the **question**, **why it matters**, **current direction**
(resolved or lean), and **what it blocks**. Answer the 🔴 blocking ones first —
they gate any build. **Resolved items are recorded in this document under "Resolved directions"; new
questions get added here as the vision evolves.**

Two ordered, build-authorizing backlogs now govern the foundation:

- **PM1–PM10:** Product-memory and per-store decisions.
- **TS1–TS9:** minimal agent tool-surface decisions.

**PM1–PM10, TS1–TS9, C7, C14, C15, and CD-0006 through CD-0009 are accepted.**
Storage/core and agent-adapter/tool acceptance slices may begin once the implementation
repo/package is chosen and separately authorized; release remains subject to TS9.
CD-0006 fixes root policy, CD-0007 fixes the repository/public-migration contract, and
CD-0008 fixes evidence binding, unreadable-record isolation, and the remaining runtime
mechanics; CD-0009 fixes Epic/research work identity and active-pack retention. Open
clarifications remain open; acceptance of these records does not close C8–C10 or
other explicitly deferred questions.

The canonical Concord priorities are maintained in [`priorities.md`](./priorities.md); this document
follows them without restating the ranked list.

---

## ✅ Resolved directions

These are decided for the current planning baseline. Companion documents must not
re-litigate them silently.

### R1. Primary operator surface and ZLauncher role
- **Direction:** The **Product-first terminal launcher** is the primary operator
  surface. A lightweight grid/table admin panel and a web UI are optional
  projections, not the daily operating surface. **ZLauncher remains the
  session/project bootstrap layer** and is not a candidate for Concord's primary
  interface.
- **Why:** Concord is optimized for a solo dev + many local agents. The terminal
  is the fastest path; optional views are for convenience, not correctness.
- **Effect:** `design-constraints.md` §6, `workflows.md` §0, `feature-inventory.md`
  §3.10, `self-documentation.md` §1.1, `vertical-integration.md` are aligned to
  this direction.
- **Clarified by C14/CD-0006:** the launcher is a context-rich navigator with narrow
  open/start/resume/launch actions. Product-row fields are accepted; substantive
  decisions happen inside the selected Product/workflow.

### R2. Cross-workflow impact propagation / freshness
- **Problem:** one workflow may change a shared spec, dependency, component, or
  resource assumption used by another workflow. The downstream agent must see and
  classify that impact before consequential work.
- **Decision (2026-08-06):** accepted by CD-0006 R3. Work declares `modifies` and
  hard/soft `depends_on` edges; completion writes breaking/non-breaking notices;
  consequential boundaries run one bounded check. Only a declared hard edge plus a
  breaking change blocks. Version stamps are the deterministic fallback.
- **Constraint:** no polling, timers, automatic downstream rewrites, or heuristic
  blockers. Heuristics may suggest edges but cannot authorize them.

### R3. Factored lifecycle truth
- **Direction:** Each workflow's designated durable orchestrator owns its single
  authoritative lifecycle record. During Product-at-a-time migration, Advance owns
  unmigrated Products and Concord owns migrated Products; one Product is never split
  across both. No workflow/documentation surface duplicates or repairs lifecycle truth.
- **Why:** avoids competing sources of "what is actually happening" and keeps
  the architecture chaos-proof by design.
- **Effect:** recorded in `workflows.md` §2.2 and `self-documentation.md` §1.3.

### R4. Product → component primary navigation
- **Direction:** Durable product knowledge is navigated **Product → component**.
  Workflows and changes are linked history from the component view, not the
  top-level browse path.
- **Why:** enforces locality of behavior (P04) and makes ownership obvious at a
  glance.
- **Effect:** recorded in `product-data-model.md` §6, `self-documentation.md` §1.1,
  and `workflows.md` §2.5.

### R5. Minimal active-work visibility
- **Direction:** Default views show **active gates** and **active problems**
  first; completed history and passive context are available through explicit
  drill-down. This applies to the terminal launcher, any admin panel, and agent
  read surfaces.
- **Why:** keeps the operator focused on what blocks execution now, instead of
  drowning them in history.
- **Effect:** recorded in `product-data-model.md` §7, `self-documentation.md`
  §2.3, and `workflows.md` §2.4.

### R6. Go core; TypeScript only at the accepted adapter boundary
- **Direction (2026-08-02, finalized 2026-08-06):** The Concord core is written in
  **Go**. Accepted TS6 permits one global `concord.ts` custom-tool module for typed
  declarations, context/permission mapping, and transport only. It is not a plugin,
  contains no hooks/domain logic, and invokes the short-lived Go CLI.
  See [`core-architecture.md`](./core-architecture.md) §1 for the full
  language ownership statement.
- **Why:** the core domain benefits from compile-time guarantees, static binary
  distribution, and the pure-Go SQLite binding. TS6 prevents the adapter from
  becoming a second domain/tool surface.
- **Effect:** recorded in `core-architecture.md` §1,
  `design-constraints.md` §7, and `priorities.md` (open questions table).

---

## 🔴 Blocking — need before building anything

### C1. Where does Concord itself live? ✅ Accepted 2026-08-07
- **Decision:** CD-0007 selects public `Sharper-Flow/concord`, Go module
  `github.com/sharper-flow/concord`, default branch `main`, MIT, one Product
  version, GitHub Releases plus a Concord installer, and public-repository authority
  after the audited bootstrap tag.
- **Execution status:** the contract was accepted on 2026-08-06; bootstrap execution
  was explicitly authorized on 2026-08-07. Runtime self-hosting remains blocked until
  replacement readiness.

### C2. Storage model for Concord-owned state
- **✅ RESOLVED 2026-08-05 by [`CD-0002-concord-state-authority.md`](./decisions/CD-0002-concord-state-authority.md), PM2, and PM3.**
  SQLite is Concord's sole durable authority: one global local database, one
  append-only `domain_events` log, and explicit typed Product-memory projections
  updated in one transaction (`synchronous=NORMAL`, WAL, one writer at a time).
  This was the recommended primary option (A) from the D1 research; it structurally
  prevents the Advance failure cluster via six binding invariants (I1–I6). The
  ranked alternatives (DBOS/Postgres, Restate, LMDB, Temporal-collapsed) remain
  documented in CD-0002 §2 as upgrade/fallback paths. PM2 resolves one global
  local authority; PM3 replaces CD-0003 D1's generic materialized spine with typed
  projections while retaining its generic authoritative event log and D2/D3.
- **Blocks (resolved):** PM1–PM5 authorize storage/core implementation; CD-0007
  resolves repository/deploy packaging, while runtime self-hosting remains deferred
  until replacement readiness. TS tracks govern later release
  completeness rather than reopening the accepted storage authority.

### C3. Read-path language rule scope
- **Status:** ✅ **Superseded (2026-08-02)** by R6 and
  [`core-architecture.md`](./core-architecture.md). The Go-core direction is
  adopted from day one; the read-path is Go, not cached TypeScript. The
  question below is retained for historical context only.
- **Question (historical):** Your "Go or Rust **if a rebuild**" — does that
  apply to the *new* fast read-path (greenfield), or only to reworking
  *existing* ADV components? (i.e., can the new read-path be cached-TS, or
  must it be Go/Rust?)
- **Resolution:** Go. The core language direction is settled in
  [`core-architecture.md`](./core-architecture.md) §1.

---

## 🟠 High-value — shape the design

### C4. What renders the admin panel? (RESOLVED by R1)
- **Question:** Browser web UI / TUI / rendered inside OpenCode / standalone app?
   "Grid and tables" — but *what medium*? (`design-constraints.md` §6 was
  deliberately vague.)
- **Why:** the primary human-facing surface; the medium dictates the tech stack
  and the agent-buildability story.
- **Direction (2026-07-25):** ✅ resolved by R1. The terminal launcher is the
  primary surface; any admin panel / web UI is an optional projection. No
  dedicated thick client required.
- **Blocks:** none. Build the launcher path first; optional panel later.

### C5. Multi-client: concrete or hypothetical?
- **Question:** Is there a real second client in mind (web UI? CLI? another agent
  host), or is "support other clients" purely "don't preclude"?
- **Why:** affects how much to invest in the client-agnostic surface vs the
  OpenCode path.
- **Lean:** purely "don't preclude" for now; optimize OpenCode.
- **Direction (2026-07-25):** 🟡 unchanged. No concrete second client; keep the
  primary surface sharp and don't compromise it for hypothetical portability.

### C6. Workflow type model ✅ Accepted 2026-08-06
- **Question:** Does *adhoc* mean spin-up-and-discard one-offs, or
  purpose-built-but-registered types? Are there transient workflows alongside the
  registered ones? (+ how are workflow types *registered* — declarative / code /
  both?)
- **Why:** defines the workflow-type model's flexibility. (`workflows.md` §7)
- **Decision:** Concord owns full workflow coordination. Purpose-built workflow
  types are code-defined and versioned; one versioned generic type handles true
  one-offs. Repeated generic patterns graduate into built-ins. CD-0006 R1 accepts
  forward-linked successors with independent authority/recovery; no nested child
  execution or parent waiting.

### C7. Research trackable ✅ Accepted 2026-08-07
- **Question:** Option A (reframe backlog items as investigations) or Option B
  (new trackable type)? (`feature-inventory.md` §2.5/§3.6)
- **Why:** decides whether the research surface is a small reframe or a new
  primitive.
- **Decision:** CD-0009 selects Option A. Independent research is an ordinary
  `work_items.kind = research`; embedded research remains inside its owning change;
  active research packs are retention-bounded context/output, not another trackable.

### C15. Managed-resource inventory shape ✅ Accepted 2026-08-06
- **Question:** Does a managed resource (infrastructure, SaaS) remain a **member
  beneath one owning Product** with an optional `shared_with` list, or become a
  **first-class registry entity** with its own identity that Products link to
  many-to-many? (`product-data-model.md` §9, `feature-inventory.md` §3.18)
- **Why:** sharing is a stated requirement — one database, queue, observability
  account, or runner pool can serve several Products. Two capabilities depend on
  which shape wins: per-resource lifecycle stage (`product-data-model.md` §8.2)
  and cross-Product replacement (`product-data-model.md` §10) both make a
  consistent shared-resource identity necessary.
- **Decision:** [`managed-resource-inventory.md`](./managed-resource-inventory.md).
  Use a resource-first registry: one canonical resource identity, exactly one owning
  Product, zero or more consuming Products, explicit resource stage, authority/
  namespace-scoped locators, typed work/replacement edges, and native execution
  authority. No copied `shared_with` records, credentials, live status, or automatic
  agent-surface expansion.

### C17. What does the Product coordination drill-down render?
- **Question:** After a Product row is selected, what does the coordination detail
  show for dependencies, blockage, and next work? (`product-row-contract.md` §1,
  `priorities.md` §4)
- **Why:** Priority 4 requires that dependencies and sequences are visible before
  they become blockers, and that the operator sees what is ready, what is blocked,
  and what is next. Accepted C14 deliberately answers none of this: the row is an
  orientation projection with one focus item and explicitly excludes the raw blocker
  graph. C14 defers the rest to Product/workflow detail in one sentence and no
  accepted document specifies what that detail renders.
- **Lean:** two modes over already-accepted canonical queries — a structural relation
  tree from Q8, and a ranked work table from Q5 joined with Q4 for blocked and blocker
  columns. Ranking uses the stored explicit priority rank; blocked stays PM4-derived
  and two-valued; grouping comes from declared edges only; incomplete coverage renders
  `unavailable` rather than a shortened table. Single-Product only, since cross-Product
  prioritization remains deferred pending PM2 authority and portability.
- **Decision (2026-08-11):** [`product-coordination-view.md`](./product-coordination-view.md).
  Accept two bounded Product-scoped modes: a structural Q8 relation tree and a Q5
  ranked work table joined with Q4 blockage explanation. Adds no field to the C14 row;
  terminal interaction and prototype detail remain implementation design per
  `rollout-plan.md` and CD-0006 D6.
- **Unblocks:** the first Product coordination drill-down consumer. Storage and
  workflow execution were never blocked by C17.

### C18. What is the terminal launcher itself?
- **Question:** What screens exist, how does the operator move between them, what
  establishes ambient Product context, what actions may the launcher take, and when
  does it re-read state?
- **Why:** the launcher is the primary operator surface and part of the
  replacement-ready floor, but no accepted document specifies the container. Accepted
  C14 fixes the Product row and explicitly defers terminal interaction, keybindings,
  layout toolkit, and the detail screen; C17 proposes a drill-down and defers the
  container to the launcher.
- **Operator direction (2026-08-09):** the launcher exists to see status and resume
   work in the OpenCode TUI. It performs no durable write. Durable knowledge belongs to
   its owning Product, Project, Epic, or change rather than to a global browse surface,
   and its section uses the shipped resolver once launcher wiring is implemented.
- **Accepted by CD-0014 (2026-08-10):** three closed screens (portfolio, Product, work) with
  knowledge as a scoped section rather than a screen; stack navigation; ambient context
  established by Product selection and changed nowhere else; a navigate-and-launch
  action surface with no writes; a launch handoff carrying identity but never workflow
  position, so the session resolves state and the launcher holds no second derivation;
  and a refresh model with no timer or poll, where staleness is displayed and never
  enforced by the launcher. See
  [`terminal-launcher-contract.md`](./terminal-launcher-contract.md).
- **Resolved sub-questions:** Bubble Tea v2 is selected behind an isolated adapter;
  query is Product-only and scoped to the ambient Product. The exact versions,
  dependency inventory, hard-proof results, no-poll interpretation, and tcell v3
  fallback are binding in [`CD-0014`](./decisions/CD-0014-terminal-launcher-rendering.md).
- **Direction:** ✅ accepted. The workflow engine and durable knowledge resolver have
   shipped; stale sequencing statements that treated them as C18 prerequisites are
   retired.
- **Implementation status:** S1 shipped through issue #45 and PR #48. S2 Product
  coordination, S3 Work detail, scoped search/knowledge, and identity-only OpenCode
  handoff shipped through issue #51 and PR #52. Concord remains not replacement-ready.

### C19. Agent context continuity
- **Question:** What law governs an agent's working context across the point where it
  exceeds the model's window? Concord has no position. Does Concord specify
  summarize-in-place, clean restart from a structured handoff, or both with a stated
  precedence — and what must survive unconditionally?
- **Why:** every long-running agent session crosses this boundary, and the boundary is
  lossy. Published measurement finds that standing instructions are dropped
  disproportionately relative to hard rules when a session is summarized, that
  protecting the plan alone does not recover task success because the working state is
  what matters, and that loss compounds across successive boundaries rather than adding.
  A coordination system whose value is stated obligations cannot leave the survival of
  those obligations to a summarizer's judgement.
- **Naming hazard:** "compaction" is already taken. PM6/PM7 use it for the terminal-work
  durable-knowledge tier — see [`compaction-design.md`](./compaction-design.md) and
  [`compaction-retention-policy.md`](./compaction-retention-policy.md). This question is
  unrelated to that tier and must be named **context continuity** to avoid collision.
- **Existing contracts do not cover it.** TS5
  ([`agent-call-context-contract.md`](./agent-call-context-contract.md)) is the per-call
  ambient scope envelope, not the model's working window. TS2 §3
  ([`agent-tool-surface-budget.md`](./agent-tool-surface-budget.md)) bounds tool-schema
  tokens, not conversation history. The C18 launcher handoff carries identity only. A
  position here needs a new decision record; no existing record can be amended into one.
- **Why Concord is well placed:** TS5 already states that there is no mutable protocol
  session and no ambient state held by a daemon. If every call is stateless and all
  state is durable, the working context is a **derived projection** that can be rebuilt
  deterministically from the store rather than a conversation that must be compressed.
  That removes the summarizer from the correctness path entirely. Predecessor harnesses
  are converging on the same conclusion from the opposite direction, having started with
  summarization and added restarts afterwards.
- **Candidate shape (2026-08-10):** three operations at three anchors, not one setting.
  A small **pinned** set re-emitted from durable state on every call — active obligation
  level, pending operator decision, and the crucial subset of standing law — which is
  never carried *through* a summary. A **summarization** step permitted only at a
  completed unit of work, never mid-derivation, because that is the one moment when the
  working state has just been made durable and eviction is therefore free. A **clean
  restart** seeded from the already-approved handoff artifact at a phase boundary, where
  the prior phase's exploration is genuinely spent. Boundaries crossed are counted and
  observable, since compounding loss makes that count the best available signal of
  degraded output.
- **Predecessor evidence:** [Advance #422](https://github.com/Sharper-Flow/Advance/issues/422),
  [#423](https://github.com/Sharper-Flow/Advance/issues/423), and
  [#424](https://github.com/Sharper-Flow/Advance/issues/424), recorded in
  [`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md). These identify
  mechanisms to prevent structurally; they are not work Concord must reproduce.
- **Long-term gap surfaced by #422 (2026-08-10):** the predecessor's short-term fix bounds the
  fallback path and adds typed size limits, but the deeper structural gap remains: the
  predecessor's report persistence is change-keyed (reports are projections stored inside a
  change's own record), so investigation work not tied to a change has no durable home and
  takes the fallback every time. A proper solution needs a persistence surface for stateless
  research output — a project-scoped or session-scoped store that is not a change projection.
  This is tracked as a separate predecessor follow-up
  ([Sharper-Flow/Advance](https://github.com/Sharper-Flow/Advance) ADV change
  `changelessReportPersistence`) and should inform [Concord #43](https://github.com/Sharper-Flow/concord/issues/43):
  bounding a delegated result is necessary but not sufficient if the result has nowhere
  durable to land.
- **What it blocks:** nothing in the storage or tool-surface slices. It shapes any
  workflow expected to run longer than one window, and it should be settled before a
  self-hosting readiness claim.
- **Direction:** 🟠 candidate recorded, awaiting operator direction.

---

## 🟡 Medium — resolve as they come up

### C8. lgrep / vision / episode
- **Question:** Confirm *product-scoping first*, or seriously evaluate
  owning/swallowing now? (`vertical-integration.md`)
- **Lean:** product-scoping first; revisit only on measured need.
- **Direction (2026-07-25):** 🟡 unchanged. See `vertical-integration.md` for
  the resolved launcher/interface boundary.

### C9. Capability-placement rubric enforcement
- **Question:** Structural (blocks misplaced capabilities) or advisory + recorded?
  (`capability-placement.md` §6)
- **Lean:** advisory + recorded; structural only for hard rules (adapter never
  owns domain authorization/approval; native authority is not reimplemented).
- **Direction (2026-07-25):** 🟡 unchanged.

### C10. Plugin extension mechanism
- **Question:** "Extended at any time to other stuff" — what's the actual plugin
  API/surface for third platforms?
- **Why:** the portability promise needs a concrete extension contract, not just
  "extensible."
- **Direction (2026-07-25):** 🟡 unchanged.

### C16. Proportional-rigor policy ✅ Accepted 2026-08-06
- **Question:** What do the independent `maturity` and `audience_commitment` bands
  require as proof before work is called done, and how does each axis affect
  that bar? (`product-data-model.md` §8.4, `feature-inventory.md` §3.17)
- **Why:** lifecycle stage is only useful once it resolves to a concrete evidence
  bar. Until then, stage is declared but inert.
- **Decision (CD-0006):** independent maturity and user-declared audience-commitment
  obligation bands. Concord defines a global floor; Product/component/resource
  policy may only strengthen it. Audience commitment is independent of repository/
  deployment visibility and replaces reachability-style `exposure` semantics.
- **Decision (CD-0006 R2):** all work carries stated purpose/owner, at least one
  proof artifact, and no silent weakening. Maturity obligations progress from proof
  artifact through functional verification, draft SLOs/graduation, production
  readiness/monitoring/rollback, and sunset/migration. Audience obligations progress
  from minimal threat model through opt-in/proportional review to full threat model
  and security review. Effective rigor is the global floor plus the higher obligation
  on either axis; local policy may only strengthen it.

---

## ⚪ Strategic / branding

### C11. Concord vs Advance ✅ Accepted 2026-08-06
- **Question:** Is Concord a rebrand/successor of Advance, or a coexisting layer?
- **Why:** "Advance V2" suggests succession; a non-goal says coexist indefinitely
  — slight tension worth resolving.
- **Decision:** Concord is Advance's full successor. No partial slice is called
  usable. After full readiness, migrate one Product at a time; migrated Products fix
  forward in Concord, unmigrated Products remain in Advance, and Advance retires
  after the final Product moves. See CD-0006 D1–D2.

### C12. Realism vs aspiration ✅ Accepted 2026-08-06
- **Question:** The scope is large (10+ docs, 19+ New capabilities). Is this a
  realistic incremental build by you + agents, or an aspirational north-star we
  document but build selectively?
- **Why:** affects how much to commit to "build it all" vs "document the vision,
  build the high-leverage slices."
- **Decision:** incremental design/build/shadow evaluation is allowed, but only the
  full accepted replacement system is called usable or becomes primary.

### C13. Operator boundary ✅ Accepted 2026-08-06
- **Question:** When does the Jira/Linear-for-teams ambition activate? (Solo +
  agents now; non-goal defers multi-human.)
- **Why:** defines when team features (assignments, sprints, boards for humans)
  become in-scope.
- **Decision:** single operator per installation is an enduring Product boundary.
  Other humans may run independent installations against shared git knowledge;
  Concord does not become a shared team server by default.

---

## ✅ Accepted Product-row decision

### C14. Which fields appear on each Product row?
- **Question:** What exactly belongs in the default Product-row glance view
  (terminal launcher, optional admin panel)?
- **Why:** drives the launcher UX, the read-path projections, and the
  fast-portfolio dashboard.
- **Direction (2026-08-06):** ✅ **Accepted.** The binding field set is
  [`product-row-contract.md`](./product-row-contract.md): Product identity,
  declared stage, reliance state, five typed action counts, and one deterministic
  focus item. Unknown counts are unavailable—not zero; mixed stages are not ranked;
  terminal/resource/dashboard detail stays behind drill-down.
- **Blocks:** cleared for Product-row projection implementation. Terminal interaction
  design remains a separate prototype concern.

---

## ✅ Product-memory decision record — ordered, build-authorizing

Concord's core is **optimized Product memory**: Products, their Projects, and
work needed/active/blocked/terminal across them. CD-0002/CD-0003 as narrowed by
accepted PM2–PM5 are the current build-authorizing storage baseline.

**A lean below is evidence-ranking only — never implementation authority.** PM1–PM10
are accepted; the storage/core acceptance slice may begin after CD-0007 repository
creation/migration is separately authorized and completed.
Accepted TS decisions do not by themselves authorize runtime/tool surfaces.

### Decision order and store coverage

| Order | Decision | Store(s) shaped | Blocks |
|---:|---|---|---|
| PM1 | Canonical Product-memory query contract | all | PM2–PM5; implementation |
| PM2 | Memory authority scope and physical home | SQLite | PM3, PM5; implementation |
| PM3 | Explicit domain schema vs generic spine | SQLite | PM4–PM5; implementation |
| PM4 | Work lifecycle and relation semantics | SQLite | work-state implementation |
| PM5 | Multi-Project work identity and membership | SQLite + git | cross-project implementation |
| PM6 | Canonical git-note placement | git markdown | completed-work compaction |
| PM7 | Compaction, retention, and historical index | SQLite + git | archive/prune behavior |
| PM8 | WIP evidence/blob scope | none — producer-owned output | excludes v1 byte store |
| PM9 | Exhaust salvage and audit receipt | none — existing event/note/locator sequence | excludes v1 receipt |
| PM10 ✅ | Backup, restore, and garbage collection | SQLite + git authorities | recovery policy accepted |

### PM1–PM10 — decisions at a glance

| ID | Decision boundary + current binding | Non-authorizing lean | Evidence / falsifier | Depends → artifact |
|---|---|---|---|---|
| **PM1 ✅ Accepted 2026-08-05** Canonical Product-memory query contract. *Binding:* Q1–Q10 + universal result/authority/freshness/pagination/error/performance contract in [`product-memory-query-contract.md`](./product-memory-query-contract.md). | Ten storage/tool-neutral jobs: Product context, snapshot, work lists, blockage, ready work, cross-Project work, history, relations/supersession, durable-knowledge search, canonical note resolution. | Binding 22-scenario corpus at [`product-memory-query.v1.json`](../scenarios/product-memory-query.v1.json). PM2/PM3 use jobs/oracles + authoritative research to reject structurally wrong shapes; the selected implementation later runs the corpus at representative scale for plans/P99. Amend PM1 only on a repeated unmet real job or measured query-granularity failure. | — → **PM1 contract + TS1/TS3 input**, operator-approved. |

> **PM1–PM10 are accepted and binding.** PM4/PM5 bridge provisional lifecycle/
> relation/membership fixture fields through explicit normalization notes; PM6 fixes
> canonical durable-note placement; PM7 fixes bounded projection retention; PM8 excludes
> WIP-byte storage and generic screenshot requirements; PM9 rejects a separate receipt.
| **PM2 ✅ Accepted 2026-08-05** Memory authority scope + physical home. *Binding:* [`product-memory-authority-scope.md`](./product-memory-authority-scope.md) narrowly supersedes CD-0002's per-Project file line. | **One global local SQLite database per Concord installation/operator-machine.** Product/Project are logical scopes; git remains durable-knowledge authority. | Accepted PM1 + official SQLite/Context7 guarantees + Exa architecture research. Reopen for multi-host, hard Product isolation/export/encryption/retention, CD-0002 load falsifiers, new PM1 jobs, or PM10 restore failure. | PM1 → **PM2 authority scope + PM3/PM5/TS5 input**, operator-approved. |

> **PM2–PM5 are accepted and binding.** Membership may not reintroduce duplicate
> physical authority, application fan-out, copied work state, or a generic
> materialized spine without explicit supersession.
| **PM3 ✅ Accepted 2026-08-05** Product-memory domain schema. *Binding:* [`product-memory-domain-schema.md`](./product-memory-domain-schema.md) narrowly supersedes CD-0003 D1's generic materialized spine. | **Hybrid explicit core:** one generic authoritative `domain_events` log; explicit typed Product/Project/component/work/membership/relation projections; bounded versioned JSON only for rare extensions. | PM1/PM2 + official SQLite/Context7 guarantees + public-source comparisons. Reopen on the accepted falsifiers: repeated schema churn by work kind, bounded generic projection without facet proliferation, non-total folds, per-kind-table need, or misclassified first-class identities. | PM1–PM2 → **PM3 schema + PM4/PM5 input**, operator-approved. |

> **PM3 is accepted and binding.** It keeps one generic authoritative event log,
> replaces CD-0003 D1's generic materialized spine with explicit typed Product-memory
> projections, and bounds JSON to versioned extensions. PM4/PM5 are accepted;
> C15 and TS1–TS9 are now accepted separately.
| **PM4 ✅ Accepted 2026-08-05** Work lifecycle + relation semantics. *Binding:* [`product-memory-lifecycle-relations.md`](./product-memory-lifecycle-relations.md). | Five stored states: `needed`, `in_progress`, `completed`, `cancelled`, `superseded`. Blocked/ready/active/terminal are derived. Store `parent`, `blocks`, `supersedes`, and `implements`; depends-on/blocked-by are inverse reads. Supersession is one atomic edge + terminal transition. | Local predecessor mapping + official/public Linear/Jira/Bugzilla/GitHub/Plane/Fossil models. Reopen on bounded-query failure, legitimate soft/hard dependency need, external-blocker model failure, multi-successor need, non-work implements targets, frequent superseded reopen, or a new accepted PM1 job. | PM3 → **PM4 lifecycle/relation + PM5 input**, operator-approved. |
> **PM4 is accepted and binding.** It fixes lifecycle, derived status, relation
> direction/inverses, graph validation, reopen, supersession, and external-blocker
> semantics. PM5 and TS1–TS9 are accepted.
| **PM5 ✅ Accepted 2026-08-05** Multi-Project work identity + membership. *Binding:* [`product-memory-membership.md`](./product-memory-membership.md). | Many-to-many Product↔Project and work↔Project membership; one canonical work item; Product scope derived through joins; cross-Product work allowed; optional singular primary roles; no required/merge-order/lifecycle/status edge fields. | Local predecessor mapping + official/public Linear/GitLab/GitHub/Changesets/Jira/Bugzilla comparisons. Reopen on bounded-query failure, legitimate per-Project lifecycle need, repeated stored-Product need, PM6 failure from optional primary, unsafe membership moves, new membership attributes required by accepted jobs, or independent per-Product status need. | PM2–PM4 → **PM5 membership + PM6/C15/TS5 input**, operator-approved. |
> **PM5 is accepted and binding.** It completes the PM1–PM5 storage build-authorizing
> sequence. PM6, C15, TS5, and TS1–TS9 are now accepted separately.
| **PM6 ✅ Accepted 2026-08-05** Canonical git-note placement. *Binding:* [`canonical-git-note-placement.md`](./canonical-git-note-placement.md) amends compaction design. | Deterministic unique Product-home→primary-Project knowledge-locator selection; otherwise typed `ambiguous`. One in-tree markdown note, stable Project/locator identity, commit OID + content hash, no copies/stubs, git-first publish proof. | Public predecessor lessons + Fowler/Log4brains/cross-cutting ADR patterns + official git docs. Reopen on non-rebuildable selection, excessive primary-less blocking, failed duplicate/orphan detection, accepted need for copied notes, legitimate multiple authorities, broken locator resolution, or PM10 restore failure. | PM2, PM5 → **compaction-design amendment + accepted PM7–PM9 / PM10 input**, operator-approved. |
> **PM6 is accepted and binding.** It fixes canonical durable-note placement. Accepted
> PM7 supplies the bounded lazy pruning transition; PM8 excludes WIP-byte storage; PM9
> rejects a separate receipt; PM10 fixes recovery. C15 and TS1–TS9 are now accepted.
| **PM7 ✅ Accepted 2026-08-06** Compaction, retention, historical index. *Binding:* [`compaction-retention-policy.md`](./compaction-retention-policy.md) amends PM6/compaction design. | Terminal compaction plus verified git proof makes a work item eligible; only explicit or measured-pressure bounded lazy maintenance may prune its live projections. Retain authoritative `domain_events`; keep a git-rebuildable historical index with frozen compaction scope. Pruned IDs never reopen; renewed work has a new ID and typed `archived_work_linked` cross-tier link. | Reopen if retained core events are the growth problem, needed history is absent from git/events, linked successor work repeatedly fails legitimate jobs, combined live/historical Q2/Q3 misses target, historical front matter becomes a dump, maintenance burdens operators, measured pressure needs another trigger, or PM9/PM10 provides a simpler replay authority. | PM4, PM6 → **compaction-design/PM6 amendment**, operator-approved. |
> **PM7 is accepted and binding.** It rejects a fixed 30-day sweep, preserves v1
> event authority, makes the prune transition atomic/FK-clean, and fixes immutable
> pruned identity plus separate cross-tier follow-up links. PM8 excludes WIP-byte storage;
> PM9 rejects a separate receipt; PM10 fixes recovery. TS1–TS9 are accepted.
| **PM8 ✅ Accepted 2026-08-06** WIP evidence/blob scope. *Binding:* [`product-memory-evidence-store.md`](./product-memory-evidence-store.md) narrows CD-0002 §2d. | No CAS, hashing, external-byte path references, or generic screenshot requirement. WIP output stays with its producer; Concord retains bounded state, concise notes, and ordinary external refs only. | Reopen only for a measured recurring job with a named reader, exact-byte need after WIP is gone, explicit retention/restore promise, and value sufficient to justify storage complexity. | PM1–PM7 → **CD-0002 §2d amendment**, operator-approved. |
| **PM9 ✅ Accepted 2026-08-06** Process-exhaust salvage + audit receipt. *Binding:* [`product-memory-process-exhaust.md`](./product-memory-process-exhaust.md) amends compaction design/CD-0002. | No receipt object: existing terminal event, approved note, verified PM6 locator, and optional PM7 prune event are sufficient. Material lessons enter reviewed note/decision text; raw exhaust stays producer-owned. | Reopen only for a recurring named reader whose needed fact cannot be answered by existing event/note/locator/link state and whose proposal states proof, retention, restore promise, and why concise knowledge is insufficient. | PM1–PM8 → **compaction-design/CD-0002 amendment**, operator-approved. |
| **PM10 ✅ Accepted 2026-08-06** Backup, restore, GC. *Binding:* [`product-memory-recovery.md`](./product-memory-recovery.md). | Online SQLite snapshots plus canonical git; clean staging restore, verification, rebuild, then atomic swap. No WIP/CAS/receipt input. | Reopen on unmet measured recovery objectives, omitted authority, required continuous replication, or a deletion job outside this policy. | PM1–PM9 → **recovery amendment**, operator-approved. |

---

## 🔴 Minimal agent tool-surface decision backlog — ordered, build-authorizing

Concord's agent surface must be **right-sized to proven jobs**, not copied from
Advance or generated mechanically from tables/CLI commands. Advance's 86-tool
catalog is predecessor evidence, not a transfer target. Minimal means **the
fewest tools that complete canonical jobs reliably**, with bounded schemas.

**Hard avoidances (binding while these decisions are open):**

- No tool-per-table / CRUD surface.
- No command-for-command port of Advance.
- No single untyped "invoke anything" mega-tool.
- No 1:1 mapping from Go CLI subcommands to agent tools.
- TS6 selects one global custom-tool module; no plugin/MCP implementation in v1.

### Tool-surface decision order

| Order | Decision | Blocks |
|---:|---|---|
| TS1 | Canonical agent jobs and evaluation scenarios | all tool decisions |
| TS2 | Surface budget and granularity | TS3–TS4 |
| TS3 | Read/query tool shape | agent reads |
| TS4 | Intent mutation tool shape | agent writes |
| TS5 | Scope, context, authorization, and idempotency | safe reads/writes |
| TS6 | Adapter, plugin, and transport role | implementation |
| TS7 | Result/error/evidence envelope | stable implementation |
| TS8 | Discovery, evolution, and deprecation | compatibility |
| TS9 | Measurement, pruning, and expansion gate | release/stewardship |

### TS1–TS9 — decisions at a glance

| ID | Decision boundary + current binding | Non-authorizing lean | Evidence / falsifier | Depends → artifact |
|---|---|---|---|---|
| **TS1 ✅ Accepted 2026-08-06** Canonical agent jobs + evaluation scenarios. *Binding:* [`agent-tool-surface-jobs.md`](./agent-tool-surface-jobs.md) and [`agent-jobs.v1.json`](../scenarios/agent-jobs.v1.json). | Eight tool-neutral jobs: orient/choose, explain blockage, capture, transition with evidence, relate/scope, compact/reconcile, retrieve knowledge, and execute ops. Twenty-one scenarios judge resulting state, required communication, prohibited effects, and shared invariants. | Local Concord requirements/postmortem + official MCP/Anthropic/OpenAI guidance + tau²/WebArena/BFCL evaluation patterns. Reopen only for a repeated unmet intent, measured merge/split need, unbounded job, wrong-state oracle, or missing recurring recovery case. | PM1 → **CD-0005 §1 + TS2–TS9 input**, operator-approved. |
| **TS2 ✅ Accepted 2026-08-06** Surface budget + granularity. *Binding:* [`agent-tool-surface-budget.md`](./agent-tool-surface-budget.md). | At most nine always-visible domain tools; exact count belongs to TS3/TS4. Merge only across aligned intent, authority, consequence, retry, and result families; split at material boundary changes. Static v1 exposure; no discovery meta-tool before evidence. | TS1 corpus candidate comparison; hard-oracle correctness first, then success/recovery, tool count, schema/context cost, calls, and retries. Reopen if no valid sub-nine candidate passes, tools are measurably indistinguishable/always chained, context cost causes failure, or discovery becomes materially better. | TS1 → **CD-0005 §2 + TS3–TS9 input**, operator-approved. |
| **TS3 ✅ Accepted 2026-08-06** Read/query tool shape. *Binding:* [`agent-read-tool-contract.md`](./agent-read-tool-contract.md). | Four always-visible reads: `concord_product_view` (`resolve|snapshot`), `concord_work_browse` (`list|ready|blocked|scope`), `concord_work_trace` (`history|relations`), and `concord_knowledge` (`search|resolve_note`). Strict typed unions, bounded detail/cursors/graphs, no read-side mutation. | PM1 Q1–Q10 + TS1 scenarios under TS2 rules. Reopen for an unexpressible accepted read, repeated selection confusion, prose-only union validation, measurable always-chained tools, unbounded payloads, or a new distinct read authority/oracle. | PM1, TS1–TS2 → **CD-0005 §3 + TS5–TS9 input**, operator-approved. |
| **TS4 ✅ Accepted 2026-08-06** Intent mutation tool shape. *Binding:* [`agent-mutation-tool-contract.md`](./agent-mutation-tool-contract.md). | Four tools: `concord_work_define` (`capture|revise_intent`), `concord_work_transition` (`lifecycle|workflow_action`), `concord_work_relate` (memberships/links/supersession), and `concord_work_compact` (`publish|reconcile`). Single-intent calls, no generic patch/mixed batch; native systems retain external execution. | PM3–PM7 + TS1 scenarios under TS2 rules. Reopen for client-sequenced invariants, repeated selection confusion, demonstrated full-replacement failure, justified homogeneous batch, unrecoverable PM6 interruption, or a recurring operation requiring new Concord authority. | PM3–PM7, TS1–TS3 → **CD-0005 §4 + TS5–TS9 input**, operator-approved. |
| **TS5 ✅ Accepted 2026-08-06** Scope, context, authorization, idempotency. *Binding:* [`agent-call-context-contract.md`](./agent-call-context-contract.md). | Trusted host injects a typed self-contained envelope; core-issued grant proves principal/client/scope/capabilities. Core re-resolves ambient scope every call; mutations require current scope/entity versions, operation-bound approval where needed, and durable idempotency. No path/trust booleans. | Shared/cross-Product, stale-context, spoofed-principal, approval-change, duplicate-delivery, and partial-operation scenarios. Reopen if the primary client cannot inject proof, scope resolution fails, grants are too broad, approval cannot bind transport, dedupe dominates retention, or another client cannot map to the envelope. | PM2, PM5, TS3–TS4 → **CD-0005 §5 + TS6–TS9 input**, operator-approved. |
| **TS6 ✅ Accepted 2026-08-06** Adapter, plugin, transport role. *Binding:* [`agent-adapter-transport-contract.md`](./agent-adapter-transport-contract.md). | One global `concord.ts` custom-tool module exports all eight accepted tools and invokes the short-lived Go CLI over JSON stdin/stdout. No plugin hooks, MCP, daemon, FFI, TS domain logic, or native-system proxy. Signed client bootstrap + current pinned `ToolContext.ask`; core remains authority. | Official OpenCode docs/current source + TS1–TS5. Reopen if pinned approval API fails, hidden grant cannot be established, module lifecycle loses to another adapter, spawn reversal fires, a real second client appears, or OpenCode adds a simpler equivalent binary adapter. | TS2–TS5 → **CD-0005 §6 + TS7–TS9 input**, operator-approved. |
| **TS7 ✅ Accepted 2026-08-06; amendment approved 2026-08-11 under issue #43** Result/error/evidence/pagination envelope. *Binding:* [`agent-result-envelope.md`](./agent-result-envelope.md) + [`agent-tool-envelope.schema.json`](../contracts/agent-tool-envelope.schema.json). | Strict `ok|pending|partial|error`; producer-validated closed results; required origin/scope/authority/freshness/watermarks/pagination/omissions/evidence; typed errors/effect/recovery; mutation changed refs + next intents; 64 KiB cap; authenticated cursor; no raw exhaust or excerpts. | PM1/TS1/TS5 scenario replay plus Draft 2020-12 structural probes and producer-side budget/schema vectors. Reopen if agents must parse messages, routine success needs follow-up status, bounds cause pathological calls, cursor binding blocks legitimate continuation, locators cannot identify proof, client validators diverge, or a distinct outcome class is proven. | TS3–TS6 → **CD-0005 §7 + TS8–TS9 input**, operator-approved. |
| **TS8 ✅ Accepted 2026-08-06** Discovery, evolution, deprecation. *Binding:* [`agent-tool-surface-evolution.md`](./agent-tool-surface-evolution.md). | One schema-validated manifest generates Go/TS/schema/tests/docs. Signed version/digest negotiation; static eight-tool v1; no discovery tool; strict SemVer; no simultaneous/permanent aliases; 30–90 day compatibility window; durable history remains readable. | Old/new adapter-core matrix, digest drift, unknown variants, old durable operations, alias expiry, and generated-doc coverage. Reopen for lossy generation/down-conversion, stranded clients, unsafe removals, unrecoverable operations, real second-client needs, or TS9 discovery evidence. | TS2–TS7 → **tool-surface evolution contract + TS9 input**, operator-approved. |
| **TS9 ✅ Accepted 2026-08-06; narrowed by CD-0006** Measurement, pruning, expansion gate. *Binding:* [`agent-tool-surface-measurement.md`](./agent-tool-surface-measurement.md). | Deterministic hard oracles first; supported-model success/selection floors; paired confidence bounds and practical thresholds; no heuristic job-success authority. Three unmet occurrences or one reproducible severe boundary violation; low usage alone never removes. | PM1/TS1 synthetic launch baseline and paired candidate comparisons gate initial release. Real-call telemetry is optional post-cutover stewardship evidence, never an initial release quota. | TS1–TS8 → **tool-surface scorecard + release gate**, operator-approved. |

---

## Recommended answering order

**PM1–PM10, TS1–TS9, C14, C15, CD-0006, CD-0007, and CD-0008 are accepted.** PM
decisions authorize the storage/core acceptance slice; CD-0005 consolidates the
accepted agent surface; C14/C15 fix Product-row and resource ownership projections;
CD-0006/CD-0007/CD-0008 fix root policy, public-migration boundaries, evidence
binding, unreadable-record isolation, and execution mechanics. PM1 remains the shared
query/read-tool corpus. No storage table or CLI command automatically earns a tool.
Remaining open clarifications resolve through their own evidence gates; C5 and
C8–C10 remain deferred, and the replacement-relation home remains open.
