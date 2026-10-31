# Concord goals

The operator's goals for Concord. Quoted text is the operator's own words; the rest is text the operator approved.

This document is guidance, not law. The constraints, the conflict rules, and the route for Concord's own
development are law in [`../law/constitution/priorities.md`](../law/constitution/priorities.md).

[CD-0243](../law/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md) defines the roles and the
operator's approval boundary.

## Purpose
- Scope: "a single machine solution using Opencode to coordinate agent led code development of software
  products."
- Form: "a set of tools and infrastructure that make subjects larger than one session can own understood and
  organized."
- Two knowledge areas, each with "a formal system of organization and information store and retrieval":
  - product laws for functionality;
  - architectural information, organized by domain.
- Purpose of both: "many agents working separately on the same system can understand the greater picture and
  adhere to those different sources of alignment **or** modify them in a way that all agents will then also align
  to."
- For whom: agents that work separately on the same system, under one operator.
- The User Operator owns product direction and the six approval classes in
  [CD-0243 D2](../law/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision).
- The Concord Agent owns its item through delivery. Concord-RTE, the release train engineer (RTE), coordinates concurrent work;
  Concord-Planner is the operator's assistant Product Owner. Both are optional, not approval gates
  on a Concord Agent's work (CD-0243 D1).
- Agents review and accept work outside the operator's approval classes. A spawned review
  subagent counts as external review; required checks and evidence still apply.

### What hurts now
- "Agents trying to finish Concord seem to be continually scattered."
- What that looks like: "Small changes that should take an agent maybe an hour or two stretching out to take
  days."
- Suspected cause: "the various bloat of instructions they are trying to follow rather than trying to complete
  the work they are given with the tools they are provided."
- Not known: where the days go today. Measuring current times is a decided step (goal B).

## 2. Goals, with how the operator will know each is met
The goals are not ranked: "Right sized and fast is equal to all of these. None should interfere with eachother
in reality. speed to ship requires quality." When two goals do pull against each other, a defined rule settles
the case ([`../law/constitution/priorities.md`](../law/constitution/priorities.md) Appendix A), not a ranking.

### A. Formal product law and domain architecture
- Formal, retrievable stores of product law and of domain architecture. All agents follow them, or change them
  so that all agents follow the change.
- Product-changing work names its affected Domains, exact law revisions, acceptance criteria, and
  intended law changes. The design keeps modularity in mind.
- Measure: spot checks find no merged change that breaks a current law without a recorded law change.

### B. Fast, right-sized delivery
- Deliver fast, with "right-sizing the amount of ceremony based on the subject/sensitivity/etc".
- Ceremony covers all three: "less steps, less proof, less testing" for small work. No change ships with zero
  proof: a small change runs the applicable checks, plus one review.
- "If its something thats just a few lines of code, we probably dont need a refine step. If its something that
  delivers a complete refactor, we may need multiple review and refine steps."
- Each live workflow retains its mandatory steps. Main chooses assignment
  complexity and may add depth; no programmed size classification selects steps.

### C. Plain workflow definitions and reusable implementations
- Each live family has a plain Go definition. Common runtime action and payload implementations stay shared,
  without a parts catalog or composition compiler ([CD-0229 D1](../law/decisions/CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md#d1-workflows-use-plain-definitions)).
- Work items compose through forward-linked successors. Each keeps its own authority and recovery.
- Measure: definitions preserve their complete live steps and routes, and public projections match their
  canonical Go source.

### D. Programmed tools over prose instructions
- "Provide fast functions and tools for agents to focus on most important context and acceptance criteria".
- "Repeatable parts, fast tools, make things that are repeatable programmed functions/tools and not prose
  instructions for agents."
- Instruction bloat covers both Concord's own instructions and the host's. Each is fixed in the repository that
  owns it.
- Measure: the prose instruction text loaded into each agent session gets smaller over time and does
  not grow when a rule is added. Each job that agents repeat has a tool.

### E. Quality, including removal of bloat
- "Quality and removing bloat/inefficient code/slop is critical."
- Quality does not conflict with workflow sizes: "smaller changes need less refinement."
- Measure: at merge, review finds no high or medium problem. Spot checks of merged changes find no
  slop. Follow-up tech-debt items get closed, not only filed.

### F. The conflict rules are built into Concord
- "we need to architect and build something in to concord to handle this. Then we can in the future do spot
  checks to see if we are following".
- The conflict rules ([`../law/constitution/priorities.md`](../law/constitution/priorities.md) Appendix A)
  become a mechanism inside Concord, not prose.
- Measure: the operator's spot checks show that agents follow the rules.

## 3. Non-goals and things to stop doing
- Rule: what to stop or remove "depends what conflicts with what we just decided". Anything in Concord that
  conflicts with the Purpose section, section 2, or the constraints in `priorities.md` is a candidate to stop or remove. The full sweep belongs to the alignment
  step. `priorities.md` Appendix B lists the conflicts that remain open.
- Stop giving agents prose instructions for jobs that a programmed function or tool can do.

## 4. Constraints that must hold
The constraints are law in [`../law/constitution/priorities.md`](../law/constitution/priorities.md#constraints).
