# R7: Expedited, Parallel-Eligible Work — Research Findings

> **Status:** Research complete; recommendation accepted by CD-0018 on 2026-08-12.
> **Decision:** [`CD-0018-declared-urgency-and-provenance.md`](../decisions/CD-0018-declared-urgency-and-provenance.md).
> **Question:** How should an agent capture work that the operator can immediately
> hand to a second agent to run in parallel — recording both its urgency and its
> provenance — without adding assignment semantics or a computed importance score?
> **Issue:** [#70](https://github.com/Sharper-Flow/concord/issues/70).
> **Date:** 2026-08-12.

## Summary

Issue #70 asks for a way for an agent working one item to raise a second item that
another agent starts immediately and concurrently. Two motivating pairings — an
ops task raised beside a durable fix, and a stopgap raised beside its permanent
replacement — are deliberately non-blocking.

The research traced the accepted law, established what the codebase already does, and
surveyed public prior art across Kanban method, ITIL/ITSM, Jira, Linear, GitHub
Projects, GitLab, Azure DevOps, Asana, and Google SRE practice.

Four findings carry the decision:

1. **The ranking half is already built; the gap is semantic, not mechanical.**
   `priority` is a fully wired first-class field and the *primary* sort key on every
   ready, list, blocked, and launcher query. An agent can already raise work that
   sorts to the top of the operator's ready queue, and the launcher can already hand
   that item to a fresh agent session. Nothing needs building for "high priority."
   What is missing is a shared meaning for the number and any record of *why* the
   item exists.

2. **Concord's priority scale is the one part of its design that contradicts
   universal prior art.** Every surveyed system uses a small closed band — three to
   five named values — as the primary urgency field. An unbounded numeric scale
   appears in exactly one surveyed system (Azure DevOps `Backlog Priority`), and
   there it is an *auxiliary* stack-rank sitting beneath a named `Priority` and a
   named `Severity`. Concord's `[-100, 100]` integer is primary and alone. One
   vendor argues the point directly in its own documentation.

3. **Typed provenance — "B was raised while working on A" — has no prior art in any
   surveyed system, and that absence is explicable rather than disqualifying.** Every
   tracker that offers a non-blocking link collapses it into one symmetric
   `relates to`. Provenance survives only as prose in a description field or, in one
   system, as an intake *status*. Those systems are human-first: the raiser is a
   person who can write the context down. Concord is agent-native with one operator
   and many concurrent machine participants, where the raiser is a process and a
   description is not queryable. The case for a typed edge is therefore stronger in
   Concord than in any system that declined to build one.

4. **The exclusion most likely to be misread as blocking this work does not block
   it.** The constitutional root of the owners/assignments exclusion is the operating
   envelope's rejection of *team-server ambition* — shared assignments, boards,
   identity, permissions, and multi-human live coordination. The same envelope makes
   many concurrent agents a design center. Recording that an item is urgent and that
   an agent raised it during other work is neither an assignment nor a multi-human
   coordination surface. Any decision arising from this spike should say so
   explicitly, because the adjacency is close enough to invite future
   misapplication.

Evidence labels used throughout: **(i)** documented product behavior or accepted
Concord law, **(ii)** published methodology or vendor documentation, **(iii)**
inference.

---

## 1. Current-law trace

| Law | What it fixes | Consequence for this question |
|---|---|---|
| [`priorities.md`](../priorities.md) operating envelope | One operator per installation; **many concurrent OpenCode TUIs**; agents are first-class machine participants; **no team-server ambition** — shared assignments, boards, identity, permissions, and multi-human live coordination are non-goals | Parallel agent execution is inside the envelope, not an extension of it. The assignments exclusion targets multi-human coordination; it does not forbid recording urgency or provenance. A human-assignee field would violate it; a declared urgency band does not. |
| [`priorities.md`](../priorities.md) Priority 4 | Planning and coordination at Product scope; the operator sees what is ready, what is blocked, and what is next | "What is next" is exactly the surface this question touches. An expedited item that the operator cannot distinguish from an ordinary high-rank item weakens Priority 4. |
| [`product-coordination-view.md`](../product-coordination-view.md) §5.1 | No computed importance score; ranking uses the **stored explicit priority rank**; a model-assigned numeric importance is heuristic authority over correctness | Written for the view layer. Note the unresolved tension in §5 below: agents already author `priority` through `capture`, so a model *is* assigning a numeric importance today, just at the write boundary rather than the read boundary. |
| [`product-coordination-view.md`](../product-coordination-view.md) §5.2 | No activity-derived priority | Any urgency signal must be **declared** at capture, never inferred from recency, blocker age, or churn. |
| [`product-coordination-view.md`](../product-coordination-view.md) §5.4 | No third blocked state; no stalled, idle, or inferred category | An "expedited" treatment must not become a fourth lifecycle or a derived state. It is an attribute of an item, not a state of one. |
| [`product-coordination-view.md`](../product-coordination-view.md) §5.6 | Terminal counts, repository paths, velocity, percent complete, estimates, **owners, and assignments** stay excluded | The clause to read narrowly per finding 4. |
| [`terminal-launcher-contract.md`](../terminal-launcher-contract.md) §12 | The launcher performs **no durable writes**; no repair actions; no second read path | The launcher can *display* an urgency band and *hand off* identity, which it already does. It cannot dispatch, transition, or acknowledge without amending C18. |
| [`agent-tool-surface-evolution.md`](../agent-tool-surface-evolution.md) §2 | MAJOR covers adding an operation, changing required fields or meaning, or removing an accepted variant; adding an optional field is MINOR **only** when negotiation can losslessly down-convert for older clients | Adding a closed `urgency` enum to `capture` input and to `work_summary` output is not automatically MINOR: strict clients reject unknown output fields. Any read-surface addition is MAJOR unless negotiated omission is built. Adding a `relation_kind` member changes a closed variant set and is MAJOR. |
| CD-0009 D2 | Architecture spikes are distinct because they must publish an accepted binding decision; research may conclude `no change` | This document is a spike. Its outcome is a decision record or an explicit `no change` with durable guidance. |
| CD-0013, CD-0017 D4 | Workers never record step transitions, verdicts, or completion; durable authority stays with the owning workflow | A second agent started on an expedited item runs its own workflow. Nothing here creates nested authority or lets one item's worker advance another item. |
| CD-0010 | Concord must not coordinate its own development before replacement readiness | This spike is tracked in GitHub, not in Concord. |

**Gap confirmed.** No accepted law defines what a priority value *means*, distinguishes
urgency from rank, or provides any vocabulary for a non-blocking relationship between
two work items. The closed `relation_kind` set is
`parent | blocks | implements | supersedes | superseded_by` — every member either
establishes hierarchy, establishes a dependency that removes the target from the ready
queue, or asserts replacement that has already happened.

## 2. What already works

This section exists because the issue's premise is half-satisfied, and a decision that
ignores that would over-build.

| Layer | State | Evidence |
|---|---|---|
| Contract | `work_define.capture` and `.revise_intent` accept `priority` integer `[-100, 100]`; `work_browse.list` accepts `priority_min` / `priority_max`; `work_summary` returns it; `product_row_focus` **requires** it | `contracts/agent-tool-surface-payloads.schema.json` |
| Persistence | `work_items.priority INTEGER NOT NULL` since migration v3; decode rejects events missing it | `internal/store/schema.go:147`; `internal/store/lifecycle.go:151` |
| Ready queue (PM1.Q5) | `ORDER BY w.priority, w.created_at DESC, w.id` — priority is the **primary** key | `internal/store/query.go:1141` |
| List (PM1.Q3), blocked (PM1.Q4), launcher search and product | priority primary or first tiebreak in every case | `internal/store/query.go:666,948`; `internal/store/launcher_query.go:131,238` |
| Product row focus (C14) | attention-kind rank first, then priority ascending | `internal/store/product_row.go:406-414,586` |
| Launcher display | priority column in the S2 ranked table; `PRIORITY:` in the S3 detail header | `internal/launcher/render/bubbletea/model.go:587,637` |
| Operator handoff | `l` spawns a fresh session carrying `CONCORD_SELECTED_PRODUCT_ID` and `CONCORD_SELECTED_WORK_ID` | `internal/launcher/render/bubbletea/model.go:743-752` |

So the following already works end to end today, with no change of any kind **(i)**:
an agent calls `capture` with `priority: -100`; the item sorts to the head of the
operator's ready queue and to the head of the Product row; the operator presses `l`;
a second agent session starts already scoped to that work item. The two agents then
run concurrently, which the operating envelope already anticipates.

Three things that path does not do:

- **It does not distinguish "rank this first" from "start this now."** Both are the
  same integer. An item that must displace current work and an item that is merely
  next in line are indistinguishable to the operator reading the ranked table.
- **It does not record why the item exists.** The relationship to the work that
  raised it is lost. `blocks` would record a dependency, but a `blocks` edge removes
  the target from PM1.Q5 entirely (`internal/store/query.go:1141`) — the exact
  opposite of the intent. `parent` asserts decomposition that did not occur.
  `supersedes` asserts a replacement that has not happened yet.
- **It gives the number no shared meaning.** `-100` is a convention nobody has
  written down, on a scale with 201 positions and no named landmarks.

## 3. Prior art

### 3.1 Urgency modelling

| Mechanism | System | What it does | Source |
|---|---|---|---|
| Class of Service — Standard, Fixed date, **Expedite**, Intangible | Kanban Method | A *policy* attached to an item that sets its treatment and service-level expectation, chosen on cost of delay | [kanban.university/glossary](https://kanban.university/glossary/) |
| Expedite swimlane with WIP override | Kanban Method | Expedite items "must be clearly recognizable" and may pass "even if the WIP limit is fully exhausted," while others form a rescue lane; the stated net effect is that everything else takes longer | [kanban.university/kanban-guide](https://kanban.university/kanban-guide/) |
| Priority = impact × urgency | ITIL / ITSM | Two independent axes. "Severity is a measurement of impact… Priority, on the other hand, is a measurement of urgency," with a worked low-impact / high-urgency counterexample | [atlassian.com/incident-management/kpis/severity-levels](https://www.atlassian.com/incident-management/kpis/severity-levels) |
| `Priority` (1–4) **and** `Severity` (1–4) **and** `Backlog Priority` (Double) | Azure DevOps | The only surveyed system exposing named urgency, named impact, and an unbounded numeric stack-rank simultaneously — as three distinct fields | [learn.microsoft.com — manage bugs](https://learn.microsoft.com/en-us/azure/devops/boards/backlogs/manage-bugs) |
| Named ordered enum (Highest…Lowest) | Jira | One `priority` field, named bands, orderable in JQL; time pressure lives in SLAs, not in priority | [support.atlassian.com — JQL fields](https://support.atlassian.com/jira-software-cloud/docs/jql-fields/) |
| Integer 0–4 mapped to five names | Linear | Stated purpose "signify urgency"; documentation explicitly declines granularity: it "avoids custom or more granular priorities to prevent complexity and diminishing returns" | [linear.app/docs/priority](https://linear.app/docs/priority) |
| Priority as a user-defined custom field | GitHub Projects, Asana | No native priority at all | [docs.github.com — field types](https://docs.github.com/en/issues/planning-and-tracking-with-projects/understanding-field-types); [developers.asana.com — custom fields](https://developers.asana.com/docs/custom-fields-guide) |

Three tradeoffs the sources state themselves:

- **Expedite is a policy exception, not a rank.** Kanban's entire expedite content is
  the WIP-limit override plus the requirement that the class be rare, declared, and
  recognizable under "agreed rules and criteria known to all drivers." Concord has no
  WIP limit and a read-only launcher, so there is nothing for an expedite flag to
  override. What transfers is the *policy discipline* — rare, declared, visibly
  distinct — not the mechanism **(iii)**.
- **Impact and urgency are separated precisely because they diverge.** The canonical
  ITSM counterexample is a low-impact defect carrying high urgency for reasons
  external to the system. Collapsing them loses that signal. ITSM-derived tools keep
  two fields; pure development trackers collapse to one and push the time axis into
  SLAs.
- **No surveyed system endorses an open-ended numeric scale as its primary urgency
  field.** Bands cluster at three to five. The single unbounded numeric field found
  is explicitly auxiliary.

### 3.2 Non-blocking relation vocabularies

| Mechanism | System | What it does | Source |
|---|---|---|---|
| `relates to` — symmetric, no inward/outward pair | Jira | The catch-all for "related, neither blocks" | [support.atlassian.com — link issues](https://support.atlassian.com/jira-software-cloud/docs/link-issues/) |
| `blocks`, `duplicate`, `related` | Linear | Three relation kinds, plus separate parent/sub-issue hierarchy | [linear.app/docs/issue-relations](https://linear.app/docs/issue-relations) |
| `relates to`, `blocks`, `is blocked by` — and nothing else | GitLab | Three bi-directional types at the work-item level | [docs.gitlab.com — linked items](https://docs.gitlab.com/ee/user/work_items/linked_items/) |
| `Related`, `Successor`/`Predecessor`, `Parent`/`Child`, `Duplicate`, `Affects`/`Affected By`, `Tested By`/`Tests` | Azure DevOps | Richest typed set surveyed; `Related` is still the sole non-blocking kind | [learn.microsoft.com — link type reference](https://learn.microsoft.com/en-us/azure/devops/boards/queries/link-type-reference) |
| Sub-issues | Linear, GitHub | Universal, but means **decomposition** — never provenance | [linear.app/docs/parent-and-sub-issues](https://linear.app/docs/parent-and-sub-issues); [docs.github.com — sub-issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/adding-sub-issues) |
| Triage intake for unplanned work | Linear | A shared inbox for work of "varying scope, priority, and origin" — origin captured as an intake *status*, not an edge | [linear.app/docs/triage](https://linear.app/docs/triage-manage-unplanned-work) |
| Incident → Problem (1:N); Known Error = root cause + workaround | ITIL | "A problem is a cause, or potential cause, of one or more incidents"; the workaround is a "temporary solution for reducing the impact," and the permanent fix closes the problem | [atlassian.com — problem management](https://www.atlassian.com/itsm/problem-management) |
| Postmortem → mitigation, root cause, prioritized follow-up actions | Google SRE | Review asks whether "resulting bug fixes are at appropriate priority" | [sre.google — postmortem culture](https://sre.google/sre-book/postmortem-culture/) |

Two structural observations:

- **The stopgap/permanent-fix pairing is modelled cross-type everywhere, never as
  peer items.** ITIL links an Incident carrying a workaround to a Problem carrying the
  permanent fix; SRE links a postmortem to prioritized follow-up actions. In both,
  the two halves are *different kinds of thing* joined by a causal or evidentiary
  edge. Concord's `work_kind` enum is `task | bug | decision | research | epic | other`
  and has no incident/problem distinction, so it cannot adopt that shape without a
  second closed-enum change **(iii)**.
- **The sources disagree on whether the separation is desirable.** ITIL prescribes
  keeping incident and problem management as distinct equal practices specifically so
  that "problem management — with its less urgent but deeply valuable long-term goals
  — doesn't get deprioritized in favor of the in-your-face urgency of incident
  management." DevOps and SRE sources argue the industry is deliberately blurring the
  boundary. Both agree on the *risk* the issue describes; they disagree on the
  structural remedy. This disagreement is recorded rather than resolved.

### 3.3 The notable absence

No surveyed system records, as a typed and queryable edge, that one item was raised
while working on another. The nearest approximations are an intake status and an
auto-populated description link. Nor does any surveyed system join provenance to
parallelism — "B came from A and is meant to run beside A, not after it." Each half
exists somewhere; the combination exists nowhere.

That absence is worth reading carefully rather than treating as a verdict. Every
surveyed system is human-first: a person raises the item and writes the context into a
description that another person reads. Concord's envelope is one operator and many
concurrent machine participants. When the raiser is a process, unstructured prose is
not a substitute for an edge, because nothing downstream can query it. The absence
looks like a consequence of the surveyed systems' user model rather than evidence that
the edge is a bad idea **(iii)**.

## 4. Options compared

### Option A — Convention only, no contract change

Document a band convention over the existing integer (for example `-100` drop
everything, `-50` expedite, `0` standard) in agent-facing guidance. Teach agents to
carry provenance in `tags` or `external_ref`.

- **Cost:** zero contract change, zero regeneration, ships immediately.
- **Against:** a convention with no schema behind it is exactly the heuristic
  authority the anti-requirements exist to prevent. Nothing validates it, nothing
  rejects a drifting agent, and the provenance stays unqueryable in a free-text field.
  It also leaves the §5 tension untouched.

### Option B — Declared urgency band plus typed provenance

Add a closed low-cardinality `urgency` enum to `capture` and `revise_intent`, retain
`priority` as the pure stack-rank tiebreak beneath it, and add one typed
non-blocking edge recording the item that raised this one.

This is the Azure DevOps shape — named band above, numeric rank beneath — combined
with the provenance edge no surveyed system offers.

- **For:** matches the strongest cross-system pattern on bands; makes urgency
  declared and validatable rather than conventional; makes provenance queryable;
  keeps ranking on the "stored explicit priority rank" that C17 §5.1 already blesses.
- **Cost:** MAJOR TS8 change on both counts. Band on the read surface means strict
  clients see a new output field; a new `relation_kind` member changes a closed
  variant set. Both require manifest edit, regeneration through
  `scripts/generate-agent-contracts.py`, and a re-pinned
  `contracts/agent-tool-surface.digest`. A decision record must also state explicitly
  that this is not an assignment, or the §5.6 exclusion will be misapplied later.

### Option C — Relation only

Add the non-blocking provenance edge; leave priority untouched and document the band
convention informally.

- **For:** smaller surface; solves the traceability half, which is the half with no
  workaround at all.
- **Against:** leaves the urgency question exactly where it is, meaning the operator
  still cannot distinguish "start now" from "next." Given that both halves need the
  same MAJOR bump and the same regeneration cycle, splitting them buys little and
  costs a second contract revision later **(iii)**.

### Option D — Extend to lane dispatch

Everything in Option B, plus a TS8 operation that dispatches an expedited item to a
CD-0017 lane.

- **Against:** the dispatch surface is unfinished for reasons that predate this
  question. `dispatchWorker` exists at `adapter/opencode/dispatch.ts:204` with no
  production caller; the `worker-dispatch` CLI verb is explicitly not a grant-gated
  agent tool; restart dispatch fails closed at
  `internal/store/workflow_dispatch.go:241,398` citing issue #57. Wiring dispatch is
  CD-0017 follow-through and should be decided there on its own evidence, not
  attached to a vocabulary question. The operator handoff that exists today is
  sufficient for the motivating scenarios.

## 5. Insufficient-evidence findings

1. **Whether the stopgap/durable pair needs its own typing.** Prior art models it
   cross-type (Incident/Problem, postmortem/action) and Concord has no such kinds.
   Whether one symmetric "raised alongside" edge is sufficient, or whether the
   stopgap relationship needs its own directional kind, cannot be settled from the
   sources — the sources solve it with a type distinction Concord does not have.
   Recommend deferring until the general edge has been used and the shortfall, if
   any, is observed.

2. **Whether agent-authored priority already sits in tension with C17 §5.1.** The
   anti-requirement forbids "a model-assigned numeric importance." It was written
   about the coordination *view* computing a score. But `capture` lets an agent
   author `priority` directly, so a model is assigning a numeric importance today, at
   the write boundary. Either §5.1 is narrower than its wording suggests, or the
   capture surface needs a stated criterion. This is an operator ruling, not a
   research finding. Note that a closed band with written criteria would *strengthen*
   compliance relative to a free 201-position integer, since it constrains what the
   model may assert.

3. **The correct band cardinality.** Surveyed systems cluster at three to five with no
   convergent rationale beyond "more yields diminishing returns." Concord's single
   operator may need fewer. No evidence distinguishes three from four.

4. **Whether any attention surface beyond ordering is warranted.** C14 already ranks
   the Product-row focus by attention kind before priority
   (`internal/store/product_row.go:406-414`). Whether an expedited item needs anything
   more than a distinct band rendered in the existing ranked table is unmeasured, and
   the launcher's read-only contract means anything more is a C18 amendment.

5. **The "priority inflation" critique could not be sourced.** The everything-is-P0
   failure mode is widely asserted but no reachable public citation was found. The
   closest reachable evidence is one vendor's documented rationale against
   granularity. Treated as unsourced.

## 6. Recommendation and falsifiers

**Recommend Option B**, scoped to vocabulary only, with dispatch deferred to CD-0017
follow-through and stopgap typing deferred per §5.1.

Concretely, a resulting decision record should fix:

- a closed `urgency` enum of small cardinality, **declared at capture only** and never
  inferred, with written criteria for each member and an explicit statement that
  expedite is expected to be rare;
- `priority` retained unchanged as the stack-rank beneath the band, preserving every
  existing ORDER BY;
- one typed non-blocking relation recording the item that raised this one, which must
  **not** participate in the PM1.Q5 blocking exclusion;
- an explicit statement that neither addition is an owner, assignee, claim, or lease,
  and that parallel-safety remains operator judgement per the issue's pre-spike
  decision;
- the TS8 MAJOR path, regeneration, and digest re-pin.

**Falsifiers.** The recommendation should be withdrawn if any of the following holds:

- the operator rules that C17 §5.1 forbids agent-authored urgency of any kind, in
  which case urgency must move to an operator-only surface and Option A becomes the
  only agent-facing answer;
- the band and the integer prove redundant in use — that is, if the operator finds the
  ranked table's existing ordering already answers "start now versus next" without a
  band;
- the provenance edge proves unused after the general edge exists, indicating the
  relationship was adequately carried by narrative;
- a MAJOR surface bump for vocabulary alone is judged too expensive against the
  remaining pre-readiness work, in which case both additions should wait and ride the
  next MAJOR change already required for another reason.

## Sources

- Kanban University — Official Guide (expedite, WIP override, rescue lane): https://kanban.university/kanban-guide/
- Kanban University — Glossary (classes of service, cost of delay): https://kanban.university/glossary/
- Atlassian — Incident severity levels (severity as impact, priority as urgency): https://www.atlassian.com/incident-management/kpis/severity-levels
- Atlassian — Incident vs problem management (ITIL problem definition, separation rationale): https://www.atlassian.com/incident-management/devops/incident-vs-problem-management
- Atlassian — Problem management (known error, workaround, permanent fix): https://www.atlassian.com/itsm/problem-management
- Atlassian Support — Jira link issues (default link-type set including `relates to`): https://support.atlassian.com/jira-software-cloud/docs/link-issues/
- Atlassian Support — Jira JQL fields (`priority` ordering, SLA fields): https://support.atlassian.com/jira-software-cloud/docs/jql-fields/
- Microsoft Learn — Azure DevOps manage bugs (Priority, Severity, Backlog Priority): https://learn.microsoft.com/en-us/azure/devops/boards/backlogs/manage-bugs
- Microsoft Learn — Azure DevOps link type reference: https://learn.microsoft.com/en-us/azure/devops/boards/queries/link-type-reference
- Linear — Priority (0–4, urgency, anti-granularity rationale): https://linear.app/docs/priority
- Linear — Issue relations (blocks, duplicate, related): https://linear.app/docs/issue-relations
- Linear — Parent and sub-issues (decomposition): https://linear.app/docs/parent-and-sub-issues
- Linear — Triage and unplanned work (origin as intake status): https://linear.app/docs/triage-manage-unplanned-work
- GitLab — Linked work items (three relationship types): https://docs.gitlab.com/ee/user/work_items/linked_items/
- GitHub Docs — Adding sub-issues: https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/adding-sub-issues
- GitHub Docs — Understanding field types (priority as custom field): https://docs.github.com/en/issues/planning-and-tracking-with-projects/understanding-field-types
- Asana Developers — Custom fields guide (priority as custom enum): https://developers.asana.com/docs/custom-fields-guide
- Google SRE Book — Postmortem culture (mitigation, root cause, follow-up priority): https://sre.google/sre-book/postmortem-culture/
