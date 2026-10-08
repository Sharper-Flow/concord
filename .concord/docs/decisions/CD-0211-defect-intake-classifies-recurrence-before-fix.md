# CD-0211: Defect intake classifies recurrence before a ready fix

- **Status:** Accepted
- **Date:** 2026-10-08
- **Scope:** Public defect capture, its Product-scoped sibling classification,
  the research prerequisite for recurrence, and the exploration utility body
- **Amends:** CD-0149 D2 at its exploration return prose: the generated explore
  body returns a facts-only packet, per D5 below
- **Preserves:** CD-0122 D2, CD-0115 released pins, CD-0156 D5 relation authority,
  CD-0198 D2 diagnosis consolidation, operator approval, independent verdicts,
  evidence provenance, and historical event replay
- **Planning record:** Concord (CON) issue [CON-797](https://linear.app/sharper-flow/issue/CON-797/defect-intake-skips-root-cause-analysis-so-a-systemic-workflow-fault),
  `work-b0b08d6e28a8db2bad386ee6`
- **Approval:** The operator approved this record's obligations on 2026-10-08,
  relayed by the Snowball coordinator under operator delegation. The approval
  records the executable contract of `work-b0b08d6e28a8db2bad386ee6` at
  contract version 1. It invents no earlier workflow history.
- **Provenance:** The Decision section below is the verbatim D1 through D6 text
  of proposal CD-0210 at [PR 1589](https://github.com/Sharper-Flow/concord/pull/1589)
  commit `521af7ea0c03146a23e72365bb294d692795985b` (Git blob
  `5f5e364661e1b8b91bd18eed99221056ec4b22f5`). Only the registration identity
  CD-0211 and this accepted approval and provenance metadata changed.

## Context

### Registration and provenance

This record is accepted Product law under `.concord/docs/decisions/`. The
outside-repair draft that proposed it stayed outside the accepted knowledge
roots until this registration. CD allocation ran again at registration, and
the accepted record takes CD-0211.

The knowledge record, coverage record, and accepted CD-0149 amendment pointer
for this law are registered with authority `legislated`, legislated by
`work-b0b08d6e28a8db2bad386ee6` at the approved contract version 1.

### Root-cause analysis of the process failure

The issue lists 17 reports: CON-704, CON-705, CON-708, CON-711, CON-714,
CON-721, CON-728, CON-731, CON-748, CON-757, CON-764, CON-770, CON-788,
CON-791, CON-794, CON-795, and CON-796. This list is the population below.
It establishes a recurring symptom, not one causal mechanism for all 17.

The baseline for this analysis is `84ee7613` on `origin/main`. It contains
[CON-861 / pull request 1576](https://github.com/Sharper-Flow/concord/pull/1576),
merged as `53332b05095dc9ee0c5f74ab75e007ace12b06ca`.

The confirmed process contributors are:

1. **Capture has no classification prerequisite.** Baseline
   `internal/agent/mutations.go:1560-1669` accepts bug intent without a
   reproduction, failure-shape identifier, or sibling search. Baseline
   `internal/store/bootstrap.go:1066-1108` validates the host capture shape,
   not a defect cluster. Both routes create ordinary work before diagnosis.
   The new public tests reproduce this omission before the repair.
2. **A fix-shaped instruction can enter unchanged.** The full
   [CON-704 report](https://linear.app/sharper-flow/issue/CON-704/request-correction-refuses-after-a-verify-step-review-accepted-via)
   names a specific predicate extension under “Repair”.
   [CON-731](https://linear.app/sharper-flow/issue/CON-731/fix-give-a-wrong-subject-contract-a-correction-route-at-the-execution)
   names predicate widening as its premise.
   [CON-757](https://linear.app/sharper-flow/issue/CON-757/repair-correction-admission-after-a-failed-checkpoint-review)
   asks for a correction route. Capture accepts such task text without a
   separate classification. These observations confirm the entry condition;
   they do not prove every report received no human analysis.
3. **The diagnosis record proves no cluster assessment.** Baseline
   `internal/store/workflow_registry.go:1468-1469` defines reproduction and
   root-cause actions as generic advances without payload fields.
   `record_alignment` records a declared candidate search, not a check of
   recurring failure shapes or accumulated special cases. CD-0156 D3-D5
   deliberately leaves semantic relations with the coordinator.
4. **Explore permits inference alongside facts.** The baseline explore body
   in `scripts/generate-agent-lanes.py` says “Separate observed facts from
   inferences.” CD-0149 D2-D4 gives the utility no diagnosis authority,
   report schema, or workflow evidence. Its body nevertheless permits an
   inference-shaped answer that a parent could mistake for a diagnosis.
5. **No capture-time cluster owner exists.** The two capture routes do not
   compare historical same-shape defects. Alignment checks candidate overlap
   inside an individual workflow. Neither mechanism holds a recurring bug
   behind completed cluster analysis before it enters the ready population.

The following stronger explanations remain unconfirmed and are rejected as
established causes:

- **Every one of the 17 reports skipped human analysis.** The reports and
  source establish a missing structural obligation, not every actor's conduct.
- **The coordinator adopted an explore diagnosis in every instance.** The
  linked CON-797 history contains a creation event and no diagnosis event.
  The lesson asserts this behavior, but it does not supply the invocation
  transcripts for this population. The repair narrows the unsafe return
  contract without claiming that historical adoption is proved.
- **A missing periodic review caused this cluster.** No capture-time cluster
  gate exists, but this evidence does not establish the conduct of all
  periodic reviews. A scheduled stream-review service is not needed for
  admission-time classification and is not part of this proposal.
- **One recovery fault explains all 17.** Their owning mechanisms differ.
  A packet-capacity failure and an in-flight removal guard are not evaluator
  correction routes. Classifying a common symptom must precede causal analysis;
  it must not replace that analysis.

### What the merged recovery contract covers

CD-0209 declares two triggers: `unhealthy_verdict` and
`disproved_premise_at_complete`. Its D1 and D3 explicitly preserve the separate
same-step recovery owners. The proposed five-trigger CON-861 issue text is
not the merged contract.

| Coverage of the listed shapes | Reports | Owning evidence |
|---|---|---|
| Direct shared correction mechanism | CON-704, CON-757 | `workflowAcceptedWorkerDelivery`, `workflowCheckpointFailedReviewCorrection`, and `workflowUnhealthyVerdictRouteTarget`; `TestVerdictCorrectionAdmitsReviewEvidenceAccept`, `TestCheckpointFailedReviewCorrectionReturnsBreakFixToRepair` |
| Failed-review return only, not failed-review disposition or unspawned-window hold | CON-708 | `TestCheckpointUnspawnedDispatchKeepsTheFailedReviewHold` retains its separate owner |
| Generic evaluator return and whitelist removal, not consume-before-spawn semantics | CON-748 | `TestCorrectionRoutesDeriveFromNoWorkKind`, `TestGenericOneOffVerifyWallKeepsTheEscalationOperatorApprovable` |
| Supersession staleness and declared artifact return, not design-revision admission | CON-721, CON-794 | `TestSupersessionStalesArtifactUntilReproduced`; design admission remains in `internal/store/workflow_design.go` |
| Delivery-gate return target, not accepted no-ship disposition | CON-796 | `workflowDeliveryGateCorrectionTarget`; `internal/store/workflow_accepted_no_ship_recovery_test.go` retains the separate disposition owner |
| Not owned by the two-trigger recovery contract | CON-705, CON-711, CON-714, CON-728, CON-731, CON-764, CON-770, CON-788, CON-791, CON-795 | Registry overlap, law allocation, historical delivery exits, execution contract correction, retry-wall approval, design revision, removal, abandonment, and packet capacity retain separate owners |

These are mechanism boundaries, not claims that uncovered reports remain
unfixed. The current recovery tables do not classify intake. The baseline
therefore does not satisfy CON-797.

## Decision

### D1. Every new public bug capture declares its failure shape

`concord_work_define.capture` and the capture form of `concord_work_start`
require a `defect_intake` record for kind `bug`. Kind `research` may carry the
same record as a cluster-analysis identity. Other kinds refuse it.

The record carries:

- `failure_shape`: a canonical lowercase identifier of at most 64 characters;
- `reproduction`: bounded steps and the observed failure;
- `searched`: the declared scope of the earlier-defect search;
- `related_defect_ids`: up to 20 distinct earlier bug work identifiers,
  including historical unclassified reports when the search finds them;
- `root_cause_work_id`: the optional completed research prerequisite.

The coordinator classifies the symptom and reuses its known identifier.
The core does not infer semantic equivalence from prose or title similarity.
A declared search is a recorded duty, not machine proof that the search was
thorough. A different spelling must not conceal a known recurring shape.

The reproduction remains distinct from a repair hypothesis. An instruction
that already names a guard extension does not discharge this obligation.

### D2. One transaction-scoped owner computes the sibling cluster

After capture memberships establish Product scope, the core finds earlier
stored bugs with the exact failure-shape identifier. All lifecycles count.
The cluster also includes valid explicitly named historical defects.

Explicit identifiers must exist, name bugs, share Product scope, and not name
the capturing item. A classified explicit sibling must carry the same shape.
The core persists its complete, sorted sibling snapshot beside the capture
record in the intent projection. It does not create a semantic relation.

The two public routes use the same membership fold inside the capture
transaction. A refusal rolls back the operation before a work item, journal,
claim, or native worktree effect becomes durable. Concurrent captures cannot
both claim to be the first stored bug of the same shape in that scope.

The input identifier list remains bounded. The authoritative sibling snapshot
has no arbitrary cluster-size cutoff that could enclose the research route.
Refusal display uses bounded candidates and the total count, not a reduced
admission population.

### D3. Recurrence enters root-cause work before a ready fix

One earlier matching bug makes a new report recurrent. A recurrent bug capture
requires completed kind `research` work in `workflow.research`, in the same
Product, with the same failure shape. Its recorded sibling snapshot must cover
every member of the cluster the core now computes.

An open, cancelled, superseded, wrong-kind, wrong-family, wrong-shape,
cross-Product, or incomplete-coverage prerequisite refuses. The refusal names
the shape and sibling candidates and gives this sequence:

1. Capture kind `research` with the report's reproduction and cluster identity.
2. Complete the cluster analysis through the existing research workflow.
3. Retry bug capture with that completed `root_cause_work_id`.

A research capture needs no completed analysis prerequisite. It remains an
admitted route when the bug capture refuses. No automatic capture, completion,
supersession, or disposition of siblings occurs.

A research capture must omit `root_cause_work_id`. A bug that supplies this
reference must satisfy the same prerequisite checks even without siblings.
The optional field cannot assert an unchecked research identity.

The coordinator owns the research contract and diagnosis. That contract must
ask which causes the evidence confirms or rejects and whether repeated special
cases reveal a wrong mechanism. A common symptom can have distinct causes.
The operator-approved research contract and independent evaluation judge the
findings. Lifecycle completion alone is not a substitute for those existing
gates, and this proposal does not remove them.

A stale snapshot does not authorize another ready fix. A new sibling requires
analysis that covers the enlarged cluster. This cost is deliberate.

### D4. Classification survives revision and remains readable

Capture owns the classification. Ordinary intent revision carries it forward,
cannot alter its fields, and cannot change the kind of a classified item.
Converting research into a bug would bypass recurrence admission. Converting
a bug into another kind would hide it from the sibling population.

A live revision cannot convert an unclassified non-bug into a bug.
Historical accepted kind changes still replay. Full-detail work lists and
scope reads expose the classification and sibling snapshot through the
existing recorded-intent decoder. Bounded summaries retain their current
summary policy.

### D5. Exploration returns facts, not a delegated diagnosis

The generated explore body requires a plain-text facts packet: observed facts
with source `path:line`, relevant counts with their populations, historical
commit identifiers, and explicit unknowns. It prohibits inferred root causes,
diagnoses, and repair recommendations. The parent coordinator owns diagnosis.

This amends CD-0149 D2's return prose on approval. It creates no report schema,
lane evidence, new tool permission, or workflow authority. Generator tests
prove the body contract; they do not prove that a model obeys prose.

### D6. Historical records and workflow law remain intact

New public captures append `work.created` payload version 3. Versions 1 and 2
upcast without a retroactive classification obligation. No existing bug is
automatically reclassified, no prior contract is changed, and no released
workflow digest or graph changes. Rebuild derives the same classification
snapshot from the event prefix.

CD-0198 D2 remains authoritative for future diagnosis consolidation. This
capture prerequisite does not add another break-fix diagnosis action or
restore the separate steps that CD-0198 merges. A periodic review service,
lookup utility narrowing, and recovery admission changes remain out of scope.

## Alternatives considered

- **Rely on alignment prose alone.** Rejected because alignment does not
  compare known failure shapes before capture readiness.
- **Infer clusters from text similarity.** Rejected because a heuristic must
  not decide admission or the root cause of a defect.
- **Require only an unverified root-cause text field.** Rejected because a
  ready fix could assert analysis without completing the existing research
  evidence and independent-evaluation route.
- **Automatically turn a bug into research.** Rejected because that changes
  the caller's intent and creates a second shaping authority.
- **Add another root-cause step to break-fix.** Rejected because it repeats
  the outstanding CD-0198 consolidation and runs after ordinary capture.
- **Register the draft as accepted law before approval.** Rejected because the
  operator had not approved the proposed obligations at proposal time.

## Consequences

The second stored defect of a known shape cannot enter the ready-fix population
without completed cluster research. New reports cost a reproduction and a
declared search. Recurrences also cost research completion. Existing historical
items remain available without retrospective intake refusal.

Exact identifiers make admission deterministic but do not establish semantic
truth. Historical unclassified siblings need explicit acknowledgement from
the coordinator's search. This repair does not prove the conduct of past
sessions or fix the workflow recovery defects themselves.

Outside repair creates no Concord workflow evidence. After approval, merge,
required checks, and an automated release, the operator or a managed session
must reconcile `work-b0b08d6e28a8db2bad386ee6` against the released behavior.
This record does not complete that item or authorize an installation.

## Verification

- `TestNativeBugCaptureRequiresDefectIntake` and
  `TestPublicCaptureBugWithoutIntakeRefused` reproduce the missing prerequisite.
- `TestRecurrentBugCaptureRefusesNamingSiblingAndRoute` proves recurrence
  refusal and rollback before ready capture.
- `TestPublicDefectJourneyThroughRealResearchCompletion` proves the admitted
  research route, independent verdict, operator confirmation, and bug retry.
- `TestClassifiedResearchRevisionToBugRefused` and
  `TestClassifiedBugRevisionAwayRefused` prove kind-conversion refusal.
- `go test ./internal/store ./internal/agent -run 'TestLargeCluster|TestRecurrenceRefusalDetail|TestPublic'`
  exercises complete large-cluster coverage and public reads.
- `python3 scripts/test-generate-agent-lanes.py` proves the facts-only body.
- `python3 scripts/generate-agent-contracts.py --check` and
  `python3 scripts/generate-agent-lanes.py --check` prove generated consistency.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-contract.py`,
  and `python3 scripts/check-public-content.py` check accepted law and public
  artifacts against this accepted record.
