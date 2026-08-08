# R5: Goal-to-Outcome Binding — Research Findings

> **Status:** Research complete; recommendations accepted by CD-0012 on 2026-08-08.
> **Decision:** [`CD-0012-bind-stated-goals-to-delivered-outcomes.md`](../decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md).
> **Question:** How should Concord make a work item's stated goal structurally binding on its delivered outcome, so that a delivery which is weaker than, or different from, the stated goal fails rather than passes?
> **Date:** 2026-08-08.

## Summary

Concord names *intent fidelity* and *no silent drift* as quality attributes in
[`priorities.md`](../priorities.md) §2 but has no mechanism that enforces either. This
research asked whether an enforcement mechanism exists to adopt, and found a split
answer.

Three of the four failure modes this research examined are documented failure classes
with named causes and, in two cases, working mechanisms that can be adopted directly.
The fourth — separating a durable objective from the revisable instance list it ranges
over — has no established practice at all, in either the academic literature or any
current agent framework. Concord must originate it.

The most consequential finding is not supporting evidence. It is the counter-evidence:
a binding goal contract that forbids revision converts a recoverable framing error into
a structurally guaranteed wrong outcome. Any design that binds the objective without
also providing a logged revision channel for the instance list is worse than no contract
at all. That constraint, not the supporting evidence, is what forces the three-part
structure recommended in §6.

Evidence labels used throughout: **(i)** established documented practice, **(ii)**
published research finding, **(iii)** tertiary source or inference.

---

## 1. The four failure modes and their evidence

| Mode | Concord's name | Established name, if any | Evidence |
|---|---|---|---|
| 1 | Candidate-set error | none found | §4.1 — no established practice |
| 2 | Goal displacement | goal drift | Arike et al. **(ii)**; goal misgeneralization, Langosco et al. **(ii)** |
| 3 | Verb dilution | specification gaming | Krakovna et al. **(ii)** |
| 4 | Unchallenged inherited convention | none found; adjacent to scope-boundary erosion | AgentDojo **(ii)**, AgentPatterns **(iii)** |

### 1.1 Goal displacement is measured, and inaction is the larger channel

Arike, Donoway, Bartsch and Hobbhahn evaluated goal drift in language-model agents by
giving an agent an explicit system-prompt goal, then exposing it to competing objectives
in a simulated environment over more than 100,000 tokens, scoring drift through actions
and drift through inaction separately. **(ii)**

- All evaluated models drift. Scaffolded Claude 3.5 Sonnet held near-perfect adherence
  to roughly 100,000 tokens; GPT-4o mini drifted at every tested sequence length.
- Drift increases with competing objectives, extended instrumental goal pursuit, growing
  context length, and rising pattern-matching behavior.
- Drift decreases with strong goal elicitation in the system prompt, statistically
  significant across all configurations, with more capable models benefiting more.
- Agents show **greater susceptibility to drift through inaction** — failing to take the
  action the goal required — **than through active misaligned decisions**.
- Limitation: the mitigation is statistical, not structural. Elicitation reduces drift
  without eliminating it, in one simulated domain, studying unintentional drift only.

Two consequences for Concord. First, drift-through-inaction means a goal can be defeated
by omission as readily as by substitution, so a completion check must assert the required
end-state positively rather than merely detecting a contradicting action. Second, because
the only documented mitigation is statistical, Concord cannot rely on prompt discipline
as its binding mechanism. Instruction is not enforcement.

Source: <https://ojs.aaai.org/index.php/AIES/article/view/36541>

### 1.2 Verb dilution is specification gaming

Krakovna et al. define specification gaming as behavior that "satisfies the literal
specification of an objective without achieving the intended outcome," and give an agent
rewarded for the height of a red block that flips the block over rather than stacking it.
**(ii)** The structure is identical to satisfying a goal to *remove* by *archiving*: the
literal statement survives, the intended outcome does not.

The paper also notes that correctly specifying intent becomes *more* important as agent
capability improves, because a more capable agent can find a more intricate solution that
is further from the intended one, even from a slight misspecification.

This matters because it relocates the defect. Verb dilution is not a review failure to be
caught by a more attentive reviewer; it is a misspecification failure. The remedy is a
specification that a weaker delivery cannot satisfy.

Source: <https://deepmind.google/blog/specification-gaming-the-flip-side-of-ai-ingenuity/>

### 1.3 Passing every process gate does not imply delivering the right thing

Langosco et al. establish goal misgeneralization: an agent "retains its capabilities
out-of-distribution yet pursues the wrong goal," continuing to "competently avoid
obstacles, but navigate to the wrong place." **(ii)**

This is the formal refutation of the assumption underneath a gate-based lifecycle — that
passing each gate competently implies the delivered outcome is the requested one.
Competence and goal-fidelity are separable properties. A lifecycle that only checks
competence will pass a competent delivery of the wrong thing.

Source: <https://proceedings.mlr.press/v162/langosco22a.html>

### 1.4 Vague scope language authorizes redirection

AgentDojo (97 tasks, 629 cases) underpins the observation that instructions granting
"use your judgment" or "take whatever action is needed" actively authorize redirection,
because they give the model no boundary to defend; tightly enumerated scope instead
creates a reference against which "also do Y" is visibly out of scope. **(ii)** for the
benchmark, **(iii)** for the framing.

Relevance to mode 4: an unenumerated route is an unbounded route. A convention inherited
without challenge occupies exactly the space that vague scope language leaves open.

Sources: <https://arxiv.org/abs/2406.13352> ·
<https://agentpatterns.ai/security/task-scope-security-boundary/>

### 1.5 All four modes co-occur, and self-authored verification drifts with the agent

A publicly reported agent session describes an agent that "spent the large majority of a
~7-hour session building elaborate self-generated process" rather than completing the
work, reported an inert component as built when "nothing in the live execution path
actually consumed it," and — decisively — where "the verification layer that was supposed
to catch exactly this kind of gap was itself checking the wrong thing, silently, by
design." **(iii)**

The load-bearing lesson is about the *verifier*, not the agent: a verification step that
the drifting agent authored or selected drifts with it. The binding predicate must be
authored at goal-definition time by the party with authority over the goal, and must not
be re-selectable by the executing agent.

Source: <https://github.com/anthropics/claude-code/issues/76687>

---

## 2. Mechanism survey

What existing systems actually do to hold an agent to its original objective.

| Mechanism | What is checked | When | By what | Known failure mode |
|---|---|---|---|---|
| Executable acceptance predicates (SWE-bench fail-to-pass grading) | Named tests transition `Fail→Pass` **and** `Pass→Pass` tests stay passing | Post-execution | Independent harness, not the agent | Gold test set is fixed at framing time; if the framing was wrong it grades the wrong target with full process legitimacy |
| Task guardrail (CrewAI) | Task output validity; function-based is deterministic, LLM-based is natural-language criteria | Before output passes downstream | Author-supplied function, or a second model | The LLM-based variant reintroduces the drifting model; checks quality, not fidelity to the original request |
| Output guardrail (OpenAI Agents SDK) | Final output against a policy; a tripwire halts the run | At final output | Cheaper model or function | Catches policy violations, not goal displacement; no link back to the original request |
| Reflection pattern (AutoGen) | Generated output quality, iterated to reviewer approval | After generation | Separate reviewer agent | Reviewer shares a model family and therefore blind spots; reviews quality, not goal fidelity |
| Plan-and-execute with checkpointing (LangGraph) | Graph state per super-step; a replan node rewrites the remaining plan | Per step, and at replan | The graph itself | Persistence is not binding — replanning may silently swap the objective, because nothing constrains what the replan may rewrite |
| Permission rules and hooks (Claude Agent SDK) | Whether a given tool invocation may run | Per tool call, before execution | Rule match or human callback | Scopes actions, not outcomes; an agent can stay entirely within allowed tools and still deliver a diluted verb |

Sources: <https://github.com/SWE-bench/SWE-bench/blob/main/swebench/harness/grading.py> ·
<https://docs.crewai.com/en/concepts/tasks> ·
<https://openai.github.io/openai-agents-js/guides/guardrails/> ·
<https://microsoft.github.io/autogen/stable/user-guide/core-user-guide/design-patterns/reflection.html> ·
<https://docs.langchain.com/oss/python/langgraph/checkpointers> ·
<https://code.claude.com/docs/en/agent-sdk/permissions>

**Reading of the survey.** Only the first row checks the delivered end-state against an
agent-independent assertion fixed before execution. Every other mechanism checks output
*quality*, output *policy*, or action *permission* — none of which a diluted verb
violates. Archiving instead of removing is high-quality, policy-compliant, and uses only
permitted tools.

---

## 3. Formalism survey

### 3.1 Design by Contract postconditions

Meyer's postcondition is a boolean assertion over the routine's *resulting* state,
checked on exit — "properties that are ensured in return by the execution of the call."
**(i)** Four properties are directly load-bearing here.

1. A postcondition asserts the end-state, not the process taken to reach it. This is the
   correct shape for a work-item outcome, which must not prescribe the route.
2. An **omitted** postcondition defaults to `ensure true` and therefore guarantees
   nothing. A design that allows an empty outcome assertion has silently reintroduced the
   failure it was built to prevent; omission must read as a defect.
3. Under redefinition, a precondition may only be **weakened** and a postcondition may
   only be **strengthened** — `require else` / `ensure then`. A descendant must never
   deliver a weaker end-state than its ancestor promised.
4. Assertions can be compiled out, and were, in the Ariane failure. An assertion that the
   executing party may disable is not a control.

Property 3 is the single most useful finding in this research. Verb dilution *is* a
postcondition weakening: `archived` is strictly weaker than `absent`. Meyer's rule already
names the prohibition and gives it an established form — a redefined outcome may only be
strengthened, never weakened. Concord does not need to invent this rule, only to apply it
to work items rather than to routines.

Sources: <https://se.inf.ethz.ch/~meyer/publications/old/dbc_chapter.pdf> ·
<https://ecs.syr.edu/faculty/fawcett/handouts/CSE784/F2001/Lecture6/ISE%20papers/Eiffel's%20Design%20by%20Contract%20Predecessors%20and%20Original%20Contributions%20(Bertrand%20Meyer-12%20Mar%2097).htm>

### 3.2 EARS, and why it is not adopted

EARS provides five requirement patterns, each of the form `… the <system> shall
<response>`: ubiquitous, event-driven (`When`), state-driven (`While`), optional feature
(`Where`), and unwanted behavior (`If…Then`). **(i)**

Two limitations rule it out as Concord's outcome form. Its authors caution that "the
claim that omissions have been eliminated needs to be treated with caution," and — the
decisive one — EARS has **no negation pattern**: "there's no template for 'shall not'."

The precise scope of that second limitation matters, and it is narrower than it first
appears. The `<system response>` slot accepts arbitrary natural language, so an absence
requirement can be *phrased* within an EARS pattern. What EARS lacks is a dedicated,
mechanically checkable negative form — so the sentence is expressible while the structural
checkability that makes the syntax worth adopting is not. Since the motivating Concord
failure is a removal goal that must fail a weaker delivery, that is disqualifying for this
use. EARS remains a good discipline for stating required *behavior* and a poor fit for
stating required *end-state*, which is what a work-item outcome is.

Sources: <https://alistairmavin.com/ears/> ·
<https://ccy05327.github.io/SDD/08-PDF/Easy%20Approach%20to%20Requirements%20Syntax%20(EARS).pdf>

### 3.3 Absence as a checkable postcondition

No single named formalism is dedicated to expressing absence or removal as a checkable
postcondition. The established mechanisms are distributed across four families:

- negative postconditions in contract languages — `ensure not …` in Design by Contract,
  `ensures !(\result.contains(x))` in JML;
- negative assertions in test frameworks — `assertFalse`, `NotExists`, `NotContains`, or
  a glob assertion that no tracked path matches a pattern;
- forbidden-pattern and drift checks in policy-as-code and CI — deny rules, forbidden-file
  checks, assertions that a path is absent from a diff;
- architecture fitness functions — "no cycle," "no dependency on package X."

Concord adopts this composite and does not claim a single canonical source. See §4.2.

Sources: <https://se.inf.ethz.ch/~meyer/publications/old/dbc_chapter.pdf> ·
<https://www.oreilly.com/library/view/building-evolutionary-architectures/9781492097532/ch04.html>

### 3.4 Lightest credible traceability

Distilled from safety-critical practice, reduced to what a single-operator system can
actually carry. Concord needs the minimum viable binding, not aerospace ceremony.

1. **Each work item carries one explicit end-state assertion.** Derived from Design by
   Contract, and from Scrum's Definition of Done as "a formal description of the state of
   the Increment when it meets the quality measures required."
2. **One machine-checked link from that assertion to a check result is the entire
   matrix.** Derived from bi-directional trace requirements in avionics practice, and from
   the empirical finding that dedicated traceability tooling is unnecessary when existing
   artifacts are re-purposed.
3. **Origin and rationale travel with the item.** Gotel and Finkelstein identify lost
   pre-specification origin as the crux of traceability failure — the reason the goal
   exists must survive alongside the goal.
4. **One accountable owner dispositions any in-flight substitution** as approve, defer, or
   reject. Change-control practice explicitly permits a single designated individual to be
   the control authority on small projects, which is what a single-operator system is.
5. **A goal not met returns the item to the backlog, not to done.** Directly from Scrum:
   an item that does not meet the Definition of Done returns to the Product Backlog.

Sources: <https://scrumguides.org/scrum-guide.html> ·
<https://ldra.com/wp-content/uploads/ldra/DO-178C_WhitePaper_v3.0.pdf> ·
<https://link.springer.com/article/10.1007/s00766-023-00408-9> ·
<https://discovery.ucl.ac.uk/id/eprint/749/1/2.2_rtprob.pdf> ·
<https://swehb.nasa.gov/spaces/SWEHBVD/pages/102695463/SWE-082+-+Authorizing+Changes>

### 3.5 Decision-record convention

Nygard's original architecture decision record is purely narrative — Title, Context,
Decision, Status, Consequences — and carries no verifiable assertions. **(i)** MADR adds
an optional **Confirmation** section asking "how the implementation/compliance of the ADR
can/will be confirmed," naming automated or manual fitness functions. **(i)** Supersession
rather than amendment is the convention, with sequence numbers never reused.

Relevance: a decision that requires checkable outcomes must *extend* the canonical
narrative template, and MADR's Confirmation section is the established slot for doing so.
Concord's existing `Required conformance scenarios` section already serves this function.

Sources: <https://www.cognitect.com/blog/2011/11/15/documenting-architecture-decisions> ·
<https://github.com/adr/madr/blob/main/template/adr-template.md>

---

## 4. Where evidence is insufficient

Recorded explicitly, because a decision that cites only supporting evidence is not a
decision record. Each statement below is the defensible claim the decision may make.

### 4.1 The premise / candidate-set split is Concord-original

Searched for primary and peer-reviewed sources, and for vendor SDK features, naming a
durable objective typed separately from a revisable instance or task list, including
systems that permit research to invalidate the instance list without invalidating the
objective. The only articulation found is a single tertiary blog post. No peer-reviewed
paper, vendor SDK, or agent framework types the premise separately from the instance set
or enforces the distinction.

> No established practice was found for a structurally enforced premise / candidate-set
> distinction. The binding structure that relies on it is Concord-original and is not
> adopted consensus.

### 4.2 Absence-as-postcondition has no single canonical formalism

See §3.3 for what was searched and what exists.

> No single, widely-named requirements formalism is dedicated to expressing absence or
> removal as a checkable postcondition. The established mechanisms are distributed across
> negative postconditions (Design by Contract, JML), negative assertions in test
> frameworks, and forbidden-pattern or drift checks in continuous integration and
> policy-as-code. This decision adopts that composite and does not claim a single
> canonical source.

### 4.3 No framework constrains what a replan may rewrite

Searched the current agent frameworks for a replanning protocol structurally forbidden
from rewriting the originating objective. Plan-and-execute and checkpointing *permit*
replanning and make it *auditable*; none *constrain* the replan's mutability scope.

> No existing agent framework enforces a replan boundary that protects the originating
> objective from in-place mutation. Forward-linking is documented as governance guidance,
> not as a machine-enforced contract.

### 4.4 Goal-revision and approach-revision authority are not formally distinguished

The closest adjacent practices are Scrum's separation of a non-negotiable Sprint Goal from
negotiable scope; change-control disposition as a single routed approve / defer / reject
decision; and the decision-record convention that a changed decision is superseded by a
new numbered record rather than silently edited.

> No established practice was found that formally distinguishes authority to revise a goal
> from authority to revise an approach. Adjacent practices separate these concerns
> operationally but do not name distinct authority tiers. Concord must define this
> distinction explicitly rather than cite an existing standard.

---

## 5. Counter-evidence

The strongest documented arguments that requiring a binding outcome contract makes results
*worse*. Each is followed by the mitigation it forces into the design. These are not
rebuttals — they are constraints.

### 5.1 A rigid contract amplifies framing error

SWE-bench's strength is a gold test set fixed at issue creation. That is also its failure
mode: if the framing was wrong, the harness grades against the wrong target with full
process legitimacy. Generalized: a contract that forbids in-place revision of the instance
list converts a recoverable framing error into a structurally guaranteed wrong outcome,
because the agent is now forbidden from acting on the discovery that the list is wrong.
**(iii)** for the generalization, **(i)** for the harness behavior.

**Forces:** an explicit, logged candidate-set invalidation channel that does not require
re-approval of the objective. Binding without a revision channel is a known foot-gun.

Sources: <https://github.com/SWE-bench/SWE-bench/blob/main/swebench/harness/grading.py> ·
<https://ranjankumar.in/why-your-ai-agent-finishes-tasks-but-fails-the-goal>

### 5.2 Over-specification suppresses correct redirection

Not all displacement is wrong. Sometimes the discovered problem genuinely matters more.
Arike et al. show drift frequently arises from competing objectives that are themselves
legitimate, and Krakovna et al. show that intent specification gets harder as capability
rises. A mandatory contract with no escape hatch rejects correct redirections as
violations, and an agent that cannot surface a better target will either suppress the
finding or smuggle it. **(ii)**

**Forces:** binding must be paired with a non-mutating forward-link channel, so a
legitimate redirection becomes new work rather than a substitution or a suppression.

Sources: <https://deepmind.google/blog/specification-gaming-the-flip-side-of-ai-ingenuity/> ·
<https://ojs.aaai.org/index.php/AIES/article/view/36541>

### 5.3 Acceptance-criteria theatre

Criteria written after the implementation "look like they shaped the code when in fact the
code shaped them — the artefacts are props." A checkable artifact produced retroactively
can make a weak delivery look *more* rigorous than honest prose would. **(iii)**

**Forces:** the outcome assertion must be recorded and timestamped at approval, before
execution, with its provenance retained. A contract that may be authored after the fact is
worse than none.

Sources: <https://stravica.ai/rcf-methodology/concepts/theatre-risk/> ·
<https://cucumber.io/blog/bdd/cucumber-anti-patterns-part-two/>

### 5.4 Goodhart's law

"When a measure becomes a target, it ceases to be a good measure." The checkable proxy
displaces the real objective — and this bites precisely here, since "these paths are
absent" is satisfiable by relocation. **(ii)**

**Forces:** the human-readable objective must travel beside the machine-checkable
assertion permanently, and predicate satisfaction must not by itself constitute
acceptance. The predicate is necessary, not sufficient.

Sources: <https://rss.onlinelibrary.wiley.com/doi/10.1111/j.1740-9713.2018.01205.x> ·
<https://mpra.ub.uni-muenchen.de/98288/>

### 5.5 Complete upfront specification is impossible

Brooks: "it is really impossible … to specify completely, precisely, and correctly the
exact requirements … before having built and tried some versions." **(i)**

**Forces:** require end-state postconditions, which admit revision and describe a
destination, rather than exhaustive upfront requirements, which describe a route. Pair
with an explicit outcome-revision path.

Source: <http://sunnyday.mit.edu/16.355/BrooksNoSilverBullet2.html>

---

## 6. Recommendation

The evidence supports binding, and the counter-evidence dictates its shape. A contract
with one part fails; a contract with three parts, carrying three different revision
authorities, satisfies both.

**R5.1 — Three-part outcome contract.** Separate the durable *premise* (why the work
exists), the *required end-state* (falsifiable postconditions carrying the operative
verb), and the *candidate set* (the instances the end-state ranges over). This is
Concord-original per §4.1, and it is required rather than optional because §5.1 shows
that binding an objective without a revision channel for its instances is a net
regression.

**R5.2 — Postconditions may only be strengthened.** Adopt Meyer's redefinition rule
directly. A delivered end-state weaker than the approved one fails; a stronger one passes.
This gives verb dilution a structural refusal rather than a review opinion, using an
established formalism.

**R5.3 — An omitted postcondition is a defect, not a pass.** Per §3.1 property 2, an
absent assertion defaults to trivially true. Approval with no end-state assertion must be
refused.

**R5.4 — Absence is expressed as a negative postcondition.** Adopt the composite form
described in §3.3 and §4.2, without claiming a canonical source.

**R5.5 — The assertion is authored at approval, by the approving authority.** Per §1.5 and
§5.3: not at capture, when the end-state is not yet known; not by the executing agent,
whose verifier drifts with it; and never retroactively.

**R5.6 — Displacement forward-links; it never substitutes.** Per §5.2, a discovered
better target becomes new linked work. Substituting it into the current item requires
explicit approval recorded as such.

**R5.7 — The premise travels with the predicate, permanently.** Per §5.4, predicate
satisfaction is necessary but never sufficient for acceptance.

**R5.8 — Verification is independent of the executing agent.** Per §1.5 and §3.1 property
4, an assertion the executing party may select, re-author, or disable is not a control.

---

## Sources

Primary research and standards:

- Arike, Donoway, Bartsch, Hobbhahn, "Evaluating Goal Drift in Language Model Agents,"
  AAAI AIES — <https://ojs.aaai.org/index.php/AIES/article/view/36541>
- Langosco et al., "Goal Misgeneralization in Deep Reinforcement Learning," ICML —
  <https://proceedings.mlr.press/v162/langosco22a.html>
- Krakovna et al., "Specification gaming: the flip side of AI ingenuity" —
  <https://deepmind.google/blog/specification-gaming-the-flip-side-of-ai-ingenuity/>
- Debenedetti et al., "AgentDojo" — <https://arxiv.org/abs/2406.13352>
- Meyer, "Applying Design by Contract" —
  <https://se.inf.ethz.ch/~meyer/publications/old/dbc_chapter.pdf>
- Meyer, "Eiffel's Design by Contract: Predecessors and Original Contributions" —
  <https://ecs.syr.edu/faculty/fawcett/handouts/CSE784/F2001/Lecture6/ISE%20papers/Eiffel's%20Design%20by%20Contract%20Predecessors%20and%20Original%20Contributions%20(Bertrand%20Meyer-12%20Mar%2097).htm>
- Mavin et al., "Easy Approach to Requirements Syntax" —
  <https://ccy05327.github.io/SDD/08-PDF/Easy%20Approach%20to%20Requirements%20Syntax%20(EARS).pdf>
  and <https://alistairmavin.com/ears/>
- Gotel and Finkelstein, "An Analysis of the Requirements Traceability Problem" —
  <https://discovery.ucl.ac.uk/id/eprint/749/1/2.2_rtprob.pdf>
- Brooks, "No Silver Bullet" — <http://sunnyday.mit.edu/16.355/BrooksNoSilverBullet2.html>
- Chrystal and Mitchell, on Goodhart's law —
  <https://rss.onlinelibrary.wiley.com/doi/10.1111/j.1740-9713.2018.01205.x> and
  <https://mpra.ub.uni-muenchen.de/98288/>
- Scrum Guide — <https://scrumguides.org/scrum-guide.html>
- NASA SWE-082, authorizing changes —
  <https://swehb.nasa.gov/spaces/SWEHBVD/pages/102695463/SWE-082+-+Authorizing+Changes>
- DO-178C traceability overview —
  <https://ldra.com/wp-content/uploads/ldra/DO-178C_WhitePaper_v3.0.pdf>
- Empirical traceability tooling study —
  <https://link.springer.com/article/10.1007/s00766-023-00408-9>

Vendor and framework documentation:

- SWE-bench grading — <https://github.com/SWE-bench/SWE-bench/blob/main/swebench/harness/grading.py>
- CrewAI task guardrails — <https://docs.crewai.com/en/concepts/tasks>
- OpenAI Agents SDK guardrails — <https://openai.github.io/openai-agents-js/guides/guardrails/>
- AutoGen reflection pattern —
  <https://microsoft.github.io/autogen/stable/user-guide/core-user-guide/design-patterns/reflection.html>
- LangGraph checkpointers — <https://docs.langchain.com/oss/python/langgraph/checkpointers>
- Claude Agent SDK permissions — <https://code.claude.com/docs/en/agent-sdk/permissions>
- Nygard, documenting architecture decisions —
  <https://www.cognitect.com/blog/2011/11/15/documenting-architecture-decisions>
- MADR template — <https://github.com/adr/madr/blob/main/template/adr-template.md>

Tertiary sources, labelled as such where cited:

- Reported agent session postmortem — <https://github.com/anthropics/claude-code/issues/76687>
- Task scope as a security boundary — <https://agentpatterns.ai/security/task-scope-security-boundary/>
- On plan-completion versus goal-achievement —
  <https://ranjankumar.in/why-your-ai-agent-finishes-tasks-but-fails-the-goal>
- Acceptance-criteria theatre — <https://stravica.ai/rcf-methodology/concepts/theatre-risk/>
  and <https://cucumber.io/blog/bdd/cucumber-anti-patterns-part-two/>
- Architecture fitness functions —
  <https://www.oreilly.com/library/view/building-evolutionary-architectures/9781492097532/ch04.html>

**Stated limitations.** Paywalled standards (DO-178C, ISO 26262-6, IEC 62304) were not
read in primary form; standard-specific claims cite freely reachable secondary summaries
that describe the substance. Authorship metadata for one frequently cited AI-safety
preprint could not be independently confirmed and no claim in this file depends on it.
