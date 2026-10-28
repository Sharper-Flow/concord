# Concord goals

The operator's goals for Concord. Quoted text is the operator's own words; the rest is text the operator approved.

[CD-0243](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md) defines the roles and the
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
  [CD-0243 D2](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision).
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
the case (Appendix A), not a ranking.

### A. Formal product law and domain architecture
- Formal, retrievable stores of product law and of domain architecture. All agents follow them, or change them
  so that all agents follow the change.
- Every change keeps a footprint record, at every size, and a tool fills it. The design keeps modularity in mind:
  - The agent names the files or behavior it will change. The tool returns the governing domains, the current
    law versions, and the checks that apply.
  - The agent adds only what a tool cannot know: the acceptance criteria, and whether it means to change a law.
  - Size changes what follows the record, not whether the record exists.
  - A declared, checked map from each code package to its domain feeds the tool.
  - The record tool and the map are reusable parts (goal C).
- Measure: one tool call returns the laws and the domain that govern a given file or behavior. Spot
  checks find no merged change that breaks a current law without a recorded law change.

### B. Fast, right-sized delivery
- Deliver fast, with "right-sizing the amount of ceremony based on the subject/sensitivity/etc".
- Ceremony covers all three: "less steps, less proof, less testing" for small work. No change ships with zero
  proof: a small change runs the checks the footprint tool names, plus one review.
- Workflows are "dynamically different sizes". The operator's examples:
  - very small task: Define -> implement -> review -> ship
  - larger or more important single change: Define -> research -> architect -> implement -> review -> refine ->
    ship
  - crucial or larger: Define -> research -> architect -> implement -> implement -> implement -> review ->
    refine -> ship
- "If its something thats just a few lines of code, we probably dont need a refine step. If its something that
  delivers a complete refactor, we may need multiple review and refine steps."
- Who picks the size: A defined decision model sets the minimum steps. The agent may add steps, never
  remove one.
  - Inputs: subject, sensitivity, "blast radius, security, relatize size, span, etc."
  - "Span" means how many domains, packages, or repositories a change touches.
  - The operator may move a size up or down. An agent may only ask for the model to run again.
- Measure, small changes: "a small change should go from start to merge queue in 1 hour or less". "Start" is
  the agent's first action on a defined item, the Define step included.
- Measure, larger and crucial changes: the operator is "not sure". Measure current start-to-merge-queue times
  first, then set the targets.

### C. Composable workflows ("legos")
- "We need to better define those kinds of work and workflow combos. Each one should not require full
  definition, but rather take existing parts/pieces."
- "like having legos for different parts needed for a workflow, we snap them together in different
  combinations for different workflows, not define each separately. Modality is crucial."
- Measure: a new workflow size or kind is made by combining existing parts, with no new step code.
  Each step is defined in exactly one part.

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

### F. The conflict rules and the sizing model are built into Concord
- "we need to architect and build something in to concord to handle this. Then we can in the future do spot
  checks to see if we are following".
- "This" covers both the conflict rules (Appendix A) and the sizing decision model (goal B). They
  become a mechanism inside Concord, not prose.
- Measure: the operator's spot checks show that agents follow the rules.

### How Concord's own development runs during the rebuild
- Agents that build Concord work outside Concord's workflow until the sized workflow exists.
- Switch point: the sized workflow takes a small change from start to merge queue in one hour or less. Then
  Concord's own work moves onto it.
- Until then, quality rests on pull requests, required checks, and an independent review. Law and domain
  changes are still made in Git, through those pull requests.
- Reasoning: small changes take days under today's workflow, and building its replacement through it pays that
  cost on every change. Current law already permits outside work:
  `.concord/docs/decisions/CD-0122-concord-workflow-scope-and-defect-repair.md`.

## 3. Non-goals and things to stop doing
- Rule: what to stop or remove "depends what conflicts with what we just decided". Anything in Concord that
  conflicts with the Purpose section and sections 2 and 4 is a candidate to stop or remove. The full sweep belongs to the alignment
  step. Appendix C lists the conflicts that remain open.
- Stop giving agents prose instructions for jobs that a programmed function or tool can do.

## 4. Constraints that must hold
- Single machine.
- OpenCode is the agent host.
- Data safety follows S and P10. Modifying production application data and risking irreversible
  customer-data loss require operator approval. The operator may explicitly approve an explained
  irreversible loss without a verified recovery route. Lack of recovery is not a separate approval
  class or absolute stop outside the six classes (CD-0243 D2, D5).
- Must not change, even where it slows agents down: "Not allowing slop or dead code to continue existing or go
  unnoticed". Bloat a change did not make still gets a follow-up item (rule R1), and goal E requires those
  items to be closed.

## 5. Open questions
- Time targets from start to merge queue for larger and crucial changes. Set them after current times are
  measured.

---

## Appendix A. Conflict rules (approved by the operator, 2026-10-16)
The operator: "We need to have more matrix thinking. For each conflict, we should have a defined rule." and "I
was thinking a cross-tabulation chart so we have an answer for each combo". The operator asked for proposals
("Fill this in to the best of your ability with rule proposals as well, and I will review") and approved them.

### A1. Goal-by-goal chart
Axes: Law = product law. Arch = domain architecture. Speed = fast, right-sized delivery. Quality = quality and
bloat removal. Safety = data safety. You = the six operator approval classes in CD-0243 D2. Parts =
reusable workflow parts and programmed tools. Each pair appears once.

|  | Arch | Speed | Quality | Safety | You | Parts |
|---|---|---|---|---|---|---|
| **Law** | P1 | R3 | P2 | S | R3 | P3 |
| **Arch** |  | R2 | P4 | S | P5 | P6 |
| **Speed** |  |  | R1 | S | U | P7 |
| **Quality** |  |  |  | S | P8 | P9 |
| **Safety** |  |  |  |  | P10 | S |
| **You** |  |  |  |  |  | P11 |

Rules the operator stated:
- R1 (Quality vs Speed): "if the bloat was made by the fix then remove, otherwise create a follow up tech debt
  item".
- R2 (Arch vs Speed): The coordinator identifies the work step that changes the shared Domain and records
  which change waits. The coordinator may pause the whole waiting change during that shared step, and
  explicitly releases it when the other change completes that step. Other changes and work before or after
  that shared step may continue independently.
- R3 (Law vs Speed, Law vs You): change the law and record why; ask the operator only when P12 requires it (CD-0243).

From the operator's earlier answers:
- S: establish data-loss risk and recovery before acting; apply P10 and CD-0243 D5.
- U: a new or changed UI/UX design or interaction waits for operator review and acceptance.
  Minor corrections that introduce no new design or interaction need no fresh approval (CD-0243 D2).

Approved proposals (situation, then rule):
- P1 Law vs Arch. A law's home domain no longer owns the behavior the law governs. The law states what must be
  true; the architecture states where it lives. Fix whichever is wrong, and move the law to the domain that
  owns the behavior. Apply R3's approval test.
- P2 Law vs Quality. A current law requires code the agent judges bloat. Never delete what a current law requires.
  Change or retire the law under R3 in the change that removes the code, or in an earlier change. Never merge
  the removal while the law still requires the code.
- P3 Law vs Parts. A law needs enforcement that no reusable part provides. The change that adds or changes the
  law names how the law is upheld, using the cheapest adequate means: an existing check, a focused test,
  review, or clear guidance. Build a new programmed check only when none of these catches a violation that
  matters. Never build a runtime system to enforce a rule that one of these already upholds.
- P4 Arch vs Quality. The cleanest code crosses a domain boundary, or a boundary forces duplicate code. Keep the
  boundary inside the change. An architect step may move the boundary. With no architect step, file a
  follow-up item.
- P5 Arch vs You. An architecture choice also changes what the operator sees or decides. Agents own
  architecture. Consult the operator only when the choice falls within CD-0243 D2's six approval classes.
- P6 Arch vs Parts. A domain needs a variant of a shared part. Extend the shared part with a parameter. Never
  copy it into a domain-local version. If the part cannot fit without a special case, file a follow-up item to
  redesign the part.
- P7 Speed vs Parts. A one-off script or prose instruction ships today; a reusable part or tool takes longer.
  The first time a need appears, ship the one-off and file a follow-up item for the part or tool. The second
  time, build the part or tool before shipping. Never add a new prose instruction where a tool can do the job.
- P8 Quality vs You. The operator asks for a behavior the agent judges adds complexity. The operator's decision
  wins. The agent states the quality cost once, with evidence, before building it.
- P9 Quality vs Parts. A shared part has bloat or fits the change badly. Use the part and fix the part itself,
  not a local workaround. If the fix is larger than the change's size, file a follow-up item on the part.
- P10 Safety vs You. The agent explains permanent-loss risk and any missing recovery route before asking
  for required approval. The operator may explicitly approve irreversible loss. Without that approval,
  do not risk irreversible customer-data loss. Outside the six classes, lack of recovery alone is not
  an operator-approval requirement or an absolute stop (CD-0243 D2, D5).
- P11 You vs Parts. The operator wants a behavior that no reusable part supports. The operator decides what;
  the agent builds it as a new part or as a parameter of an existing part, not as a one-off.
- P12 Approval test for law changes under R3. Apply CD-0243 D2's six approval classes. Seriousness,
  Domain count, and crucial size do not independently require approval. Adding, removing, or reclassifying
  an operator approval always requires the operator's approval of its exact wording.

### A2. Conflict situations by change size
Columns are the sizes the decision model gives a change. "Same" means the rule does not change with size.

| # | Conflict | Small | Larger | Crucial | Applied by |
|---|---|---|---|---|---|
| 1 | A fix meets bloat | R1 | Same | Same | Agent |
| 2 | Two agents change one domain at once | R2 | Same | Same | Coordinator |
| 3 | A change breaks a product law | R3, with P12 for approval | Same | Same | Agent; operator when P12 requires approval |
| 4 | A change needs a new domain or moves a domain boundary | Resize the task (row 8); apply P12 | The architect step decides; apply P12 | Same | Agent; operator when P12 requires approval |
| 5 | Review finds problems | Review fixes problems inside the change's scope and verifies its fixes; file follow-up items for the rest | Same | Same; the coordinator runs more review until review reports no high or medium problem | Review fixes; the coordinator ends the loop |
| 6 | A check fails for a cause the change did not make | Do not fix it in the current change. File a bug item. If it blocks shipping, the coordinator schedules the bug first | Same | Same | Coordinator |
| 7 | A change affects UI/UX | U | Same | Same | Operator for new design or interaction; agent for minor corrections under U |
| 8 | A task proves bigger than its workflow size | Run the decision model again, add the newly required steps, and continue without redoing completed work | Same | Same | Decision model; coordinator |
| 9 | Data safety is at risk | S; P10 | Same | Same | Agent assesses risk; operator when CD-0243 D2 requires approval |

## Appendix C. Open conflicts with current Concord text
Not a full sweep. In each case the operator's decision wins. Row numbers are stable identifiers.

| # | Current text | Conflicting decision |
|---|---|---|
| 3 | CD-0006 D5: "Human approval is always required to enact law changes or governed scope cuts." CD-0222 D4: "modified/added law is operator-mandated". | Agents change law with a recorded reason; approval follows R3 and P12's six-class test. |
| 4 | CD-0222 D6: "only the operator may approve compatibility, merger, supersession, or a sequence that changes Product scope." | The coordinator sequences the overlapping step (R2). |
| 10 | Prose instruction files under `.concord/instructions/` given to every agent session. | Programmed tools wherever a tool can do the job (goal D, P7). |
