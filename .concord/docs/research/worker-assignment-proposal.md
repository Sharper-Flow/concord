# Explicit worker assignments and failure attribution

Status: proposal, not accepted Product law.

This record proposes an implementation contract. It authorizes no runtime
change, workflow-definition change, or new mutation route. The coordinator must
obtain approval for that contract before implementation.

## Decision proposed

Record typed, durable assignments under one work item. Bind each dispatched
attempt to one immutable assignment revision and the whole work authority.
The coordinator resolves ambiguity, records the partition, accepts or rejects
results, and owns integration and whole-contract acceptance.

An assignment is a behavioral objective, not a file budget or an evidence-token
name. The lane's existing evidence obligations remain mandatory. A worker may
edit and test repeatedly before it returns its final report.

Separate failure observations from causal attribution. Preserve terminal
attempt history, exact failed-retry approval, and the existing correction
counting population. A more precise diagnosis grants no dispatch authority.

## Observations and governing law

The following source paths identify current behavior, not a proposed API:

| Observation | Source |
|---|---|
| The approved premise becomes the task for every contract-bound packet. | [packet.ts](../../../adapter/opencode/packet.ts), lines 314-336 |
| The static lane result adds one evidence obligation, not a component objective. | [packet.ts](../../../adapter/opencode/packet.ts), lines 355-365; [worker-scope.v1.json](../../../contracts/worker-scope.v1.json) |
| Recorded task, design, law, proposal, and narrative become packet context. Checkpoints do not enter this expression. | [packet.ts](../../../adapter/opencode/packet.ts), lines 370-393 |
| Completed reports must carry every declared lane obligation, not only the assigned result. | [dispatch.ts](../../../adapter/opencode/dispatch.ts), lines 936-975 |
| The adapter derives report identity from the packet rather than trusting worker-authored identity. | [dispatch.ts](../../../adapter/opencode/dispatch.ts), lines 926-927 and 976 |
| Completion reads the host session and compares its opening packet with the authorized packet. | [dispatch.ts](../../../adapter/opencode/dispatch.ts), lines 1754-1807 |
| The adapter quotes the core's packet digest at dispatch. Terminal assertions bind the stored attempt, not a newly computed packet digest. | [lane_dispatch.ts](../../../adapter/opencode/lane_dispatch.ts), lines 220-256; [dispatch.ts](../../../adapter/opencode/dispatch.ts), lines 1885-1986 |
| A host failure and a valid worker-reported failure both become `worker_error`. A malformed report becomes `invalid_report`. | [dispatch.ts](../../../adapter/opencode/dispatch.ts), lines 1876-1883 |
| The durable failure payload contains attempt, model, failure kind, and detail, but no typed causal evidence. | [worker_lanes.go](../../../internal/store/worker_lanes.go), lines 175-180 |
| `accept_worker_result` uses `ActionAdvance`. Local result acceptance cannot be treated as a hold without changing the owning definition. | [workflow_registry.go](../../../internal/store/workflow_registry.go), line 1532 |

Knowledge search confirmed these decision records as accepted. Their locator and
content, not their presence in a directory, establish the law used here:

- [CD-0067](../decisions/CD-0067-dispatch-worker-packet.md), D2 and D5-D6:
  the adapter derives the packet, and the core binds its digest. The coordinator
  does not author a packet or attempt identity.
- [CD-0115](../decisions/CD-0115-a-released-workflow-definition-never-changes-content-in-place.md):
  released workflow versions and digests remain immutable.
- [CD-0137](../decisions/CD-0137-iterative-correction-preserves-result-disposition.md):
  completed-result rejection differs from terminal failure and carries correction
  evidence into a fresh attempt.
- [CD-0148](../decisions/CD-0148-approved-fresh-worker-retries.md):
  a failed retry needs the exact unused approval and fresh identity and fencing.
- [CD-0164](../decisions/CD-0164-correction-attempt-counting-population.md):
  every dispatch in the correction window counts, including infrastructure
  failures. Accepted results reset that window. Contract supersession does not.
- [CD-0173](../decisions/CD-0173-escalation-wall-stays-visible-gate-counts-product.md):
  escalation keeps the approval-gated dispatch intent visible. Removing that
  intent is not the current contract.
- [CD-0197](../decisions/CD-0197-a-review-report-carries-a-typed-verdict-and-ranked-findings.md):
  a report can carry typed findings without granting the worker workflow
  authority. Compatibility needs an explicit payload upcast and replay rule,
  not merely an optional field.
- [CD-0198](../decisions/CD-0198-small-items-batch-merge-and-derive-routine-workflow-actions.md),
  D4-D5: final acceptance can assert delivery through the complete existing
  admission mechanism. Terminal failure derives correction facts, and optional
  rejection plus fresh dispatch shares the sole dispatch admission.

## Alternatives

1. **Whole-contract packets with static lane results.** Retain the present
   mechanism. It binds execution identity, but does not name a behavioral unit,
   its dependencies, its stopping condition, or its reserved integration work.
2. **Prose-only slices.** Put a smaller instruction in a mutable task or prompt.
   This does not establish immutable assignment identity, result ownership, or
   readiness. Replacing the whole work intent to direct the next worker also
   changes the wrong owner.
3. **Typed durable assignments.** Choose this route. It adds a checkable boundary
   between approved context and local authority while retaining one work
   aggregate, the dispatch window, and coordinator-owned acceptance.

The cost of option 3 is a store projection, closed schemas, adapter projections,
and new workflow-definition versions. A prompt-only change cannot meet the
identity, continuation, attribution, and replay requirements.

## Assignment shape

The field names below are proposed contract vocabulary, not callable tools.
An assignment record has a stable opaque `assignment_id`. Its ordinal controls
display order only. Neither step, lane, worker session, nor ordinal is its
identity. Append an immutable revision for a change to assignment content.

| Field | Meaning and structural rule |
|---|---|
| `assignment_id`, `revision`, `content_digest` | Stable identity, positive revision, and SHA-256 of canonical assignment content. A dispatched revision never changes. |
| `work_id`, `authority` | One owning work item. Authority is either a pinned approved-contract version and digest, or a recorded-question intent version and digest for a read-only lane. |
| `step_id`, `lane_id` | The step and registered lane that can execute this assignment. The existing lane-step admission still applies. |
| `objective` | The specific behavior, component, or bounded research question. Nonempty text, at most 4096 characters. |
| `scope` | Member Projects and exact permitted path boundaries for edits. Effect scope must remain within the approved design and contract. Read-only assignments grant no edit scope. |
| `predicate_ids` | Relevant approved predicate IDs. Effects require a nonempty subset of the pinned contract. A pre-approval recorded question has no approved predicates and states that fact. |
| `interfaces` | Interface references and settled version or content digests, with their preservation checks. An empty set explicitly means no relevant interface. |
| `checks` | One to 32 named checks, each with a typed check reference or argv and an explicit expected result. No shell-string interpolation. |
| `stopping_condition` | The result and checks that justify a successful final report, plus the requirement to report blockers instead of taking reserved work. |
| `dependencies` | Exact prerequisite assignment revision/digest and accepted-result references. No dependency on a mutable display ordinal. |
| `unresolved_refs` | Decision or interface questions that prevent effect dispatch. Read-only investigation may own those questions. |
| `reserved_scope` | Work left to later assignments and coordinator integration. Empty later scope does not remove whole-contract acceptance. |

List and text bounds protect transport size. They do not decide decomposition,
limit an assignment to a fixed file count, or cap the number of assignments in
one work item. Assignment browsing uses bounded pages.

The coordinator records and revises assignments through the owning work-definition
surface. It does not alter the whole work task to simulate an assignment.
The core validates typed identity, references, predicate membership, Project
membership, and scope containment. The coordinator decides semantic boundaries
and resolves ambiguity before it marks an effect assignment ready.

A check cannot prove that prose describes all relevant uncertainty. Readiness
therefore requires both structural prerequisites and a recorded coordinator
decision with its evidence. The core never infers settlement from text length,
an empty prose paragraph, or a worker's confidence.

## Dependency and uncertainty boundaries

A migration, runtime integration, and compatibility proof can be separate
assignments when they agree on an immutable schema/interface contract. The
runtime assignment depends on accepted migration evidence. Compatibility proof
depends on the integrated runtime subject it checks, not only on the migration.

If the migration and runtime disagree about the interface, the coordinator
resolves the question first or assigns the coupled design and implementation
together. Two workers do not each decide one half of the same unsettled interface.

No new parallel scheduler is part of this proposal. Preserve the present
single active attempt boundary. Dependency records explain order and readiness,
not permission to run overlapping workers in one worktree.

## Attempt and result lifecycle

Assignment readiness is derived from its authority, current revision, settled
interfaces, accepted dependencies, and the work item's unresolved correction.
The revision is pending, executing, awaiting disposition, or satisfied, based on
recorded assignment and attempt events. Superseded revisions remain historical
and cannot dispatch. Do not add a second mutable attempt-state machine.

1. The coordinator records an assignment revision within unchanged approved
   scope. A new effect outside that scope requires contract approval first.
2. Dispatch selects one ready revision. The adapter derives the packet, fresh
   attempt ID, step epoch, and host provenance. The core binds the assignment
   reference beside its existing authorized packet digest in one transaction.
3. The worker performs as many local edit/test cycles as necessary within that
   window. A failing intermediate check is not a new assignment or terminal
   attempt. The stopping condition governs the final report.
4. An admitted final report carries a typed `assignment_result`: exact assignment
   identity/revision/digest, observed check results, artifacts, and remaining
   work or blockers. The host checks this against the bound window. Worker text
   cannot select another assignment or replace the host's attempt identity.
5. The coordinator accepts a satisfied result or rejects an unmet result with
   diagnosis, strategy, predicate IDs, and evidence. Report admission alone
   grants neither assignment success nor work acceptance. A valid blocked report
   that fails its assigned objective cannot be accepted merely to reset retries.
6. An accepted result permits the next ready assignment in the same approved
   scope without fresh operator approval. Rejection uses existing correction
   rules. Terminal failure uses exact failed-retry approval and a fresh attempt.

In assignment-capable definition versions, ordinary `accept_worker_result`
holds the current effect step while it records the local result disposition.
After required assignments and integration are satisfied, the coordinator can
use the existing delivery or phase-exit action. A final acceptance that carries
the optional delivery fields follows CD-0198 D4: it can advance only after the
complete `record_delivery` admission succeeds. Acceptance, the same typed
delivery event, and advance are atomic. No acceptance route enters the CD-0166
delivery gate, whose sole advance remains `record_delivery`.

The owning action contract must distinguish local-result acceptance from this
guarded final-delivery variant. Reuse the accepted optional delivery fields,
not a generic caller-selected advance flag. Both entry routes share the fenced
start, current refine proof, default-ref tooling resolution, and post-rejection
review guard. A refused final delivery leaves no acceptance, delivery, or advance.
Do not add a parallel slice-acceptance or delivery-admission mechanism.

Local acceptance holds are a proposed definition change, not current behavior.
Released pins retain their recorded policy. Each changed family needs a new
definition version and frozen historical copy. Steps need explicit reachable
exits, including research and review paths; a hold with no admitted exit is a
defect. A final combined acceptance/delivery is not an ordinary local hold.

The core retains unresolved failure or rejection across assignment names and
revisions. A different assignment ID, lane, contract revision, or display order
cannot clear that correction, reset its count, or evade its approval binding.
Discharge of an unrelated evidence token cannot establish assignment success.

CD-0198 D5 owns the terminal-failure derivation and optional rejection/dispatch.
The assignment binding and attribution extend that terminal event and its
existing correction projection. Do not restore a second callable failure
recorder or count one terminal outcome twice. Combined rejection/dispatch uses
the same assignment-ready, packet, provenance, fence, correction, and approval
admission as standalone dispatch. If it refuses, neither half records. At the
escalation wall, rejection-only remains available and the approval-gated retry
stays visible. Failed-attempt retry approval remains unchanged.

## Failure observations and causal attribution

Keep the closed operational `failure_kind` vocabulary and its routing behavior.
Add a versioned attribution block to the owning worker failure/result-disposition
records. The block names:

- The host-derived assignment, revision, attempt, and epoch.
- The observed phase: dispatch, host execution, worker execution, readback,
  report parse, report validation, evidence write, result review, or integration.
- The exact observed error, check, predicate, or interface reference.
- Source authority and evidence references, with digest/version where available.
- A cause classification: assignment/requirement, implementation, dependency,
  host/provider, report contract, identity/provenance, or unknown.
- The diagnosis, and whether the cited evidence establishes the cause or leaves
  it a hypothesis. Unknown cause stays explicit.

The producer can establish the phase it observed. It cannot establish a deeper
cause merely by choosing a failure-kind token. A host terminal message
`Task cancelled` proves that terminal message, not a rate limit. Provider
attribution requires a bound host/provider observation such as an actual error
code. Missing evidence produces unknown cause, not an invented explanation.

An unavailable report does not blame implementation. An admitted report with a
failed compatibility check is an observed check result, not automatically an
infrastructure failure. An evidence-write error can leave an attempt open and
effects uncertain; read the durable state before claiming terminal failure or
granting a fresh window.

Workers report observations. The coordinator owns result and integration
diagnoses. Neither can overwrite host identity or grant retry authority.
All dispatched infrastructure failures still count under CD-0164. Attribution
does not exempt failures, lower the limit, or convert failed retries into
successful continuation.

## Packet, report, event, and pin compatibility

Keep the whole approved premise, predicates, design, and governing context
available separately from the local typed assignment. `inputs.assignment` is the
bounded execution objective. Whole-contract context cannot grant edit authority
outside it. The adapter projects both from recorded state and refuses overflow
rather than truncating either.

Extend the packet and report through their owning schemas and generators.
An assignment-capable dispatch requires the typed assignment and its matching
report block. A legacy dispatch is identified by its recorded definition and
event version, never inferred from the absence of a required new field.

Version the assignment binding and attribution in durable events. Upcast older
dispatch, completion, and failure payloads with explicit absence meaning
legacy/unavailable. Do not fabricate an assignment or causal evidence from an
old task string. The event version and stored dispatch binding select the fold
rule. Pin all new lane/schema digests and preserve existing accepted digests.

The packet digest remains the core's canonical digest. The adapter quotes it.
The host session's opening packet and single-use window bind the terminal
report to the attempt. The new assignment binding extends that chain, and the
report's claimed assignment must match it. Completion does not re-read a mutable
current assignment and credit the old result to its replacement.

Replay reconstructs assignment revisions, attempt bindings, dispositions, and
attribution from the same event semantics as live application. Explicit old
payload upcasts preserve historical absence. A live-only required-field check
does not, by itself, prove projection parity or prevent bad new events from
entering through replay/import.

Changed workflow behavior ships only as new definition versions. Historical
pins keep their existing dispatch and acceptance behavior and remain readable,
replayable, and completable. They do not silently adopt assignment holds or
silently bypass requirements on assignment-capable pins. Phase one does not
migrate a live instance to another definition version. Adopting the new behavior
for historical work needs a separate, explicit approved successor/adoption
decision; starting another worker session is not such a decision.

Contract supersession preserves every assignment and attempt record. Old
assignment revisions cease to authorize new effects. The coordinator records
new revisions bound to the successor authority and revalidates dependency and
interface evidence. Prior success remains historical evidence, not automatic
acceptance of changed obligations. Correction counters survive unchanged.

## Integration and acceptance

The coordinator reserves integration before the first dispatch. It can delegate
the engineering to an integration assignment, but owns the dependency choices
and the integration result disposition. The integration assignment names the
accepted input revisions and interfaces. Verification identifies the combined
immutable subject and its full required predicates.

Slice acceptance proves only the assigned result. Delivery requires all required
assignments to have accepted results, integration evidence for the current
combined subject, and every existing work-level gate. Whole-contract verdicts
and operator acceptance remain where the workflow already owns them.

The delivery guard detects absent or stale integration evidence. It does not
derive success from a count of accepted workers, a union of mentioned predicate
IDs, or the presence of a final report.

## Related-work coordination assessment

The coordinator inspected the authoritative Product work inventory and recorded
the exact references, observed versions, and overlap decisions in typed
continuity. That record retains the planning evidence. This public proposal
states the architectural constraints without copying a planning backlog,
private diagnostics, or claims about unverified provider behavior into git.

The assessment establishes these boundaries:

- Provider-error capture and causal cancellation research can supply attributed
  observations. The assignment mechanism accepts their source-bound evidence
  or records unknown cause; it does not duplicate their diagnostic investigation.
- Repair of a final invalid report inside a still-running host attempt is a
  separate capability question. This contract preserves terminal rules and
  claims no such repair capability.
- Accepted CD-0198 D4-D5 defines shared delivery and correction owners. Assignment
  implementation must settle those event and admission contracts before workers
  divide the same acceptance, terminal-fold, or dispatch seam. A separate
  implementation of those accepted reductions would duplicate planned work.
- The inventory includes an unresolved allegation that coordinators can author
  packet and attempt identity. Source inspection of the intended binding chain
  does not close that defect. Assignment dispatch requires host-window anti-spoof
  fixtures that reject hand-authored identity and mismatched packet content.
- Existing worker-loop planning owns related recovery follow-ups. This proposal
  adds no duplicate umbrella or speculative recovery mechanism.
- Measurement of token, cost, and duration facts is separate work. This proposal
  makes no spend or convergence-rate claim from declared lane budgets.

The architectural proposal does not wait for those implementation and research
items to complete. Runtime work must use settled shared interfaces and the
existing owners. If an interface remains unsettled, keep the coupled change
together rather than assigning its halves to different workers.

## Proposed implementation contract

Implement the assignment record/projection, typed authoring and browsing,
dispatch binding, packet/report projection, result lifecycle, attribution, and
new definition versions as one coherent change. Update the owning schemas and
generators, lane instructions, store, adapter, and deterministic witnesses.
Remove the use of whole-objective task text as local effect authority on new
assignment-capable pins. Retain historical behavior only for its explicit pin
compatibility obligation.

The contract preserves one work item across its member Projects. Each touched
Project still uses its own claimed worktree and any required coordinator
session. An assignment does not grant cross-repository edit authority.

Exclude parallel worker scheduling, automatic causal inference from prose,
new retry exemptions, model-routing changes, and runtime migration of old
workflow pins. Report-schema preflight inside an ongoing host attempt is a
separate question; this proposal makes no claim that the present host supports it.

The following scenarios are requirements for future tests, not passing tests
of this proposal:

| ID | Given / when | Required observable result |
|---|---|---|
| A1 | A ready assignment dispatches. | One authorization binds work authority, exact assignment revision/digest, fresh attempt/epoch, core packet digest, and host provenance. |
| A2 | An effect assignment lacks an objective, checks, stopping condition, reserved scope, or valid predicate/scope references. | Typed pre-effect refusal; zero dispatch events and no window. |
| A3 | A dependency is unaccepted or an interface decision remains unresolved. | Effect dispatch refuses without side effects. A correctly scoped read-only investigation remains admissible at an admitting step. |
| A4 | The worker edits and tests twice before its final valid report. | One attempt identity and epoch; no extra dispatch, correction request, or operator approval. |
| A5 | Two settled assignments complete and receive coordinator acceptance. | Both results remain bound to their own revisions; the next ready assignment opens without new approval and the effect step stays open. |
| A6 | A completed report claims another assignment, stale revision/digest, or predicates outside its assignment. | No completion or result credit for either assignment. The observing boundary records its typed report/identity failure through the existing terminal route. |
| A7 | A completed result does not satisfy its assignment. | One rejection records diagnosis, strategy, predicates, and evidence. It is not relabeled terminal worker failure or accepted to reset a counter. |
| A8 | A terminal failed attempt is renamed as a new assignment or placed under a superseding contract. | Unresolved correction and count remain; fresh dispatch still needs the exact unused approval and fresh identity/epoch. |
| A9 | Three infrastructure attempts fail within one correction window. | Every dispatch counts; the visible next intent is `escalated_retry_requires_approval`; unapproved dispatch has no effect. |
| A10 | Host failure and worker failure share `worker_error`. | Their observed phases and sources differ in durable attribution. Retry authority remains identical to the existing law. |
| A11 | The only host evidence says `Task cancelled`. | Cause is unknown; no provider-rate-limit claim. A cited provider event can establish provider cause in a separate source-backed case. |
| A12 | A report is malformed or fails the closed schema. | No completed result; report-phase observation and validator error survive in the failure record without an invented implementation cause. |
| A13 | Evidence recording fails after authorization or file effects. | Readback states the persisted boundary and possible effects. No invented terminal event or replacement window. |
| A14 | Migration and runtime slices pass, but the combined subject fails compatibility. | No whole-contract acceptance. Coordinator integration attribution names the combined subject, failed check, and relevant input revisions. |
| A15 | All slices pass without current integration evidence. | Delivery refuses. Integration plus full work-level verification is required, not a worker-count threshold. |
| A16 | Replay includes pre-feature events and a historical definition pin. | Released digest verifies; old events retain explicit missing assignment/attribution; historical workflow behavior remains reachable. |
| A17 | Replay includes new assignment-bearing events. | Live and replay projections agree on revisions, bindings, dispositions, readiness, and attribution; mismatched required binding is rejected. |
| A18 | Authority changes, or packet context exceeds its bound. | Stale authority or overflow refuses before dispatch; no truncation, old-result reassignment, or silent fallback. |
| A19 | An ordinary accepted local assignment has later required assignments or integration work. | Acceptance records only its result, holds the effect step, and permits only the next ready assignment. No delivery event is inferred. |
| A20 | Final combined acceptance/delivery carries missing, stale, failed, dirty, or undeclared proof, a missing start, or unresolved review debt. | The same complete admission as standalone delivery refuses atomically. Valid current proof permits acceptance, delivery, and advance; the CD-0166 delivery gate remains `record_delivery`-only. |
| A21 | Optional rejection/dispatch carries an unready assignment, stale packet, or unmet approval requirement. | Neither rejection nor dispatch records. A valid request records both atomically through the sole dispatch admission; escalation still permits rejection-only and advertises approval-gated retry. |
| A22 | One terminal attempt event applies live and then replays. | Exactly one derived failure/correction fact carries its assignment and attribution, with the same diagnosis, strategy, count, and escalation projection on both paths. |

Delivery evidence must include the applicable store and adapter tests,
generated-contract checks, definition-pin and completion witnesses, replay and
upgrade fixtures, and the repository verification contract. A stubbed installed
dispatch proves route reachability under CD-0067 D4; it does not prove a live
provider's behavior. This proposal includes no performance or convergence-rate
claim without a measured population.

## Evidence limits and next decisions

The source establishes the ownership gap and the failure-phase conflation.
It does not establish a lower retry rate or faster convergence. No feature
implementation, migration, proposed scenario test, or new live worker round trip
has run.

Before implementation, the operator must approve this proposed contract and
its necessary Product-law and definition changes. The exact schema names,
event-version registrations, and touched paths belong in that approved change's
design record. Historical adoption beyond the compatibility behavior above is
not silently included.
