# Concord Canonical Agent Jobs (TS1)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS1; binding input to CD-0005 §1 and TS2–TS9.
> **Binding inputs:** accepted PM1 query contract and scenario corpus, PM2–PM10,
> Concord priorities, workflows, design constraints, and Advance postmortem.
> **Scenario corpus:** [`agent-jobs.v1.json`](../scenarios/agent-jobs.v1.json).
> **Does not decide:** tool count or names (TS2), read or mutation schemas
> (TS3/TS4), context and authorization (TS5), transport (TS6), result envelopes
> (TS7), discovery/versioning (TS8), or change gates for the surface (TS9).

## 1. Decision boundary

TS1 defines the smallest evidenced set of **end-to-end agent jobs** Concord's
agent surface must support and the tool-neutral scenarios used to judge later
surface candidates. A job starts with an agent-visible intent and ends with an
observable Product-memory or external-authority outcome. It is not a table, CLI
command, workflow phase, existing Advance tool, or proposed Concord tool.

PM1's Q1–Q10 remain the canonical read contract. TS1 composes those reads with
the mutation, knowledge, completion, and operational outcomes already required
by Concord. No Q-number implies a separate tool.

## 2. Canonical jobs

| ID | End-to-end agent job | Successful outcome |
|---|---|---|
| **AJ1** | **Orient to a Product and choose work.** Resolve ambient Product/Project context; inspect needed, in-progress, blocked, and terminal work; understand cross-Project scope; select the highest-priority ready item. | Scope is explicit and unambiguous; one canonical work item appears once even when it spans Projects; blocked work is excluded from ready work; authority and freshness are visible. |
| **AJ2** | **Explain why work is blocked.** Trace canonical blockers and state what must change. | Blockage is derived from relations and blocker terminality, never a competing stored state; resolved blockers disappear; graph depth and output stay bounded. |
| **AJ3** | **Capture needed work without silently cutting scope.** Record a bounded idea, defect, investigation, implementation, or operational need with value, kind, and Project membership. | One canonical work item is created in the resolved scope. Ambiguity or conflict with governing law is surfaced for human direction rather than guessed or omitted. |
| **AJ4** | **Transition work with evidence.** Move work through its accepted lifecycle, including completion, while satisfying evidence, approval, and version requirements. | One atomic domain transition occurs, evidence and actor are attributable, invalid or stale transitions fail structurally, and a retry cannot duplicate the effect. |
| **AJ5** | **Relate and scope work.** Add or remove Project membership and create parent, blocking, supersession, or implementation relations. | Graph and membership invariants hold; cycles and duplicate authority are rejected; supersession is one atomic relation-plus-terminal transition. |
| **AJ6** | **Compact and reconcile terminal work.** Publish the one canonical durable note required for terminal work and reconcile its Product-memory locator. | Git publication is proven before the SQLite locator is recorded; an interruption leaves an explicit, recoverable partial outcome; retries do not create duplicate notes or competing authority. |
| **AJ7** | **Retrieve durable Product knowledge.** Find prior work, decisions, lessons, specs, and canonical completed-work notes. | One bounded query returns canonical locators and index watermark; missing, ambiguous, degraded, and unreachable are distinct; no repeated list→show→search choreography is required. |
| **AJ8** | **Execute operational work with consequence controls.** Carry an ops item through plan, required approval, execution, health verification, rollback when needed, and cleanup—including ground-truth reclamation of derived resources. | Native authority executes the operation; Concord preserves intent, approval, status, and evidence. Missing approval blocks consequences, health failure triggers the declared recovery path, cleanup uses native facts rather than stale bookkeeping, and partial completion is explicit. |

These eight jobs are intentionally broader than storage queries and narrower than
"do anything." They are the evidenced intent families repeatedly present in
Concord's Product model, workflow plurality, first-usable floor, and PM1 contract.
They do not prescribe one tool per row.

## 3. Shared success oracles

Every scenario applies the relevant subset of these outcome oracles. TS5–TS7
later choose their concrete request/result schemas.

1. **Correct scope:** ambient scope is used when singular; ambiguity and intentional
   cross-Product work require explicit resolution.
2. **Canonical authority:** successful reads and mutations agree with the one
   current authority; degraded or unreachable state is never presented as current.
3. **Atomic core effects:** each lifecycle transition, supersession, or membership
   move commits as one SQLite domain transaction; no observer sees half its state.
4. **Evidence and consequence authority:** required evidence and human approval
   are checked by the core before the consequential effect.
5. **Honest recovery:** partial success, stale versions, invalid relations,
   exhausted budgets, and recovery actions are structural outcomes—not prose hidden
   beneath `success`.
6. **Retry safety:** replaying the same intent does not duplicate work, relations,
   transitions, notes, or external effects.
7. **Bounded context:** output is summary-first, cursor-bounded, and explicit about
   omissions. Empty, unknown, ambiguous, degraded, and unreachable remain distinct.
8. **No silent scope reduction:** an agent may clarify, propose a law change, or
   present the consequence of a smaller scope; it may not silently omit an accepted
   obligation.
9. **Ground-truth cleanup:** reclaim and release decisions use authoritative git,
   process, or native-system facts rather than a potentially stale projection flag.
10. **Ordered cross-authority proof:** git and external native operations are not
    falsely presented as one transaction with SQLite. Steps are ordered, attributed,
    retry-safe, and expose partial outcomes with an idempotent reconciliation path.
11. **Stable evolution:** in-flight work remains interpretable under its recorded
    contract version; surface evolution does not rewrite history.

## 4. Evaluation corpus contract

The JSON corpus is tool-, transport-, and model-neutral. Each scenario records:

- `job_id` and natural-language `instruction`;
- deterministic `initial_state` or a named fixture source;
- optional multi-turn `driver` for ambiguity or human approval;
- expected final-state assertions and required communication;
- prohibited effects that must remain absent;
- shared invariants exercised by the scenario;
- measurements to record: tools exposed and considered, selected calls, retries,
  typed errors, schema/prompt tokens, output bytes, latency, operator interventions,
  and outcome.

Mutation scenarios judge **resulting state**, not response wording or one exact call
sequence. A reference trajectory may illustrate one valid route but is binding only
when the operation is irreversible and exactly one safe ordering exists. Read
scenarios reuse PM1's accepted fixtures and oracles rather than copying or weakening
them.

### Passing rule

A candidate passes a scenario only when all required final-state, communication,
prohibited-effect, and invariant assertions pass. Efficiency measurements do not
rescue an incorrect result. TS2 sets candidate-comparison bands; TS9 governs later
surface expansion or pruning. No universal numeric tool ceiling is assumed.

## 5. Evidence basis

### Concord evidence

- PM1 Q1–Q10 establish Product orientation, work views, blocked/ready selection,
  cross-Project identity, history, relations, knowledge search, and canonical-note
  resolution (`product-memory-query-contract.md` §§4–5).
- Concord's first-usable floor requires one Product view, required work capture,
  visible ready/blocked/next work, evidence-backed completion, and durable Product
  knowledge (`priorities.md` §§3–6).
- Workflow plurality includes implementation, research, static analysis, and ops;
  completion is a contract outcome rather than a process-liveness flag
  (`workflows.md` §§2–4).
- The Advance postmortem supplies adversarial cases: split authority, partial
  terminality, dishonest top-level success, bookkeeping-blocked reclamation,
  silently clamped budgets, and destructive recovery (`advance-postmortem.md`).

### External evaluation and tool-design evidence

- Anthropic recommends consolidating related operations and keeping the tool set
  focused to reduce selection ambiguity; it does not establish a universal numeric
  ceiling: <https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools>.
- OpenAI recommends strict schemas and explicit purpose, parameter, and output
  descriptions: <https://developers.openai.com/api/docs/guides/function-calling>.
- MCP defines named tools with typed input schemas; Concord does not
  rely on transport-local implicit state:
  <https://modelcontextprotocol.io/specification/2026-07-28/server/tools>.
- tau²-bench separates initial state, user instruction, reference trajectory, and
  state/communication reward; one reference trajectory is not assumed to be the
  only valid path:
  <https://github.com/sierra-research/tau2-bench/blob/main/docs/evaluation.md>.
- WebArena demonstrates why mutation tasks should be judged against resulting
  environment state rather than response-text matching:
  <https://arxiv.org/html/2307.13854>.
- BFCL contributes call-relevance/irrelevance and order-independent matching for
  parallel valid calls:
  <https://github.com/ShishirPatil/gorilla/tree/main/berkeley-function-call-leaderboard>.

## 6. Rejected or deferred as jobs

- Existing Advance tool identities, CRUD verbs, table operations, CLI subcommands,
  generic repair commands, and orchestration internals do not become jobs merely
  because they exist.
- Launcher navigation and optional web/admin projections are human surfaces, not
  separate agent jobs.
- Free-form SQL, arbitrary JSON paths, unbounded full-text search, and "show all"
  remain structurally rejected by PM1.
- Staleness checks, budgets, authority labels, idempotency, pagination, and recovery
  are scenario properties or downstream envelope mechanics—not standalone jobs.
- Tool discovery and catalog management are TS8 concerns, not Product outcomes.

## 7. Falsifiers and amendment rule

Amend TS1 only when one of these occurs:

- a repeated real agent intent cannot be completed by any AJ1–AJ8 composition;
- two jobs are always chained and merging them measurably improves success without
  crossing a consequence or authority boundary;
- one job mixes unrelated intents and causes measured selection or recovery errors;
- an accepted Product-memory job cannot remain bounded;
- the scenario corpus rewards response wording while permitting wrong state;
- implementation evidence shows a scenario lacks a legitimate successful path or
  omits a recurring ambiguity/recovery case.

New jobs require the unmet intent, success oracle, authority boundary, and a failing
scenario. An existing tool or endpoint is never sufficient evidence.

### Approved amendments

**2026-08-16, issue #156 — `AJ1-ambient-ready-work` gains an authority assertion.**
Building the corpus runner surfaced that this scenario asserted only `result` and
`communication` facets. It declared the `canonical_authority` invariant and carries
AJ1's "authority and freshness are visible" attribute from section 2, but checked
neither, while all four other read-only scenarios already asserted a durable
`effects` or `authority` fact. That is the sixth falsifier above: the scenario
rewarded response wording while permitting wrong authority. The amendment adds

```json
{ "target": "authority", "path": "stored_ready_flag_used", "op": "eq", "value": false }
```

which mirrors `AJ2-blocker-explanation`'s `stored_blocked_flag_used` assertion and is
satisfiable because readiness is derived at read time and never persisted as a flag.
No job definition, instruction, invariant, or other scenario changed.
