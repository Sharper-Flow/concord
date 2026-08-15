# Predecessor operational coverage

> **Status:** Authorizing for floor condition 6 coverage assessment. Companion to
> [`priorities.md`](./priorities.md) (canonical floor definition) and
> [`floor-readiness.md`](./floor-readiness.md) (authorizing readiness record).
> **Issue:** [#78](https://github.com/Sharper-Flow/concord/issues/78).
> **Predecessor:** the public [Advance](https://github.com/Sharper-Flow/Advance)
> repository. Citations below name public predecessor documents, never private
> history or local state.

[`priorities.md`](./priorities.md) §First-usable floor condition 6 gates migration
on Concord replacing predecessor surfaces "without losing operational coverage."
That is unassessable without a list of what the operational coverage *is*. This
document is that list, and the coverage judgement against it.

## What this document is

An enumeration of **operational territory** — what an operator or an agent must be
able to accomplish — paired with a coverage state for each entry.

It is deliberately not organized by predecessor command shape.
[`priorities.md`](./priorities.md) §Relationship to Advance rejects command-for-command
cloning, and CD-0006 fixes Concord's own workflow model. An enumeration organized
around predecessor commands would smuggle that shape back in through the gate that
exists to keep it out.

### Coverage states

| State | Meaning |
|---|---|
| **Covered** | Concord has a runtime path that produces the outcome. Evidence is a repository path. |
| **Not covered** | The outcome is required and Concord cannot produce it today. |
| **Excluded** | The predecessor supports it and Concord deliberately does not. A reason is required; omission is never acceptable. |

"Covered" is judged against the **outcome**, not the interface. A predecessor
capability delivered through a smaller or differently-shaped Concord mechanism is
covered. A capability whose code exists but has no reachable operator or agent
surface is **not covered** — unreachable code produces no outcome.

### Relationship to `feature-inventory.md`

[`feature-inventory.md`](./feature-inventory.md) is a **non-authorizing capability
lineage map**, organized around predecessor tool names and bucketed
Transfer / Refactor / New. It answers "where did this capability come from."

This document is **authorizing for coverage** and organized by territory. It
answers "can Concord produce this outcome today." The two do not overlap in
authority: the inventory never records readiness, and this document never records
lineage. Where they disagree about whether something exists, this document's
evidence paths win, because they are checked.

---

## 1. Product-scoped planning

Turning intent into durable, reviewable commitment before implementation.

| Outcome | State | Evidence or reason |
|---|---|---|
| Capture a unit of work with value statement, kind, priority, urgency, tags, and Project membership | Covered | `internal/agent/mutations.go` (`concord_work_define.capture`) |
| Revise the intent of captured work, with a recorded reason | Covered | `internal/agent/mutations.go` (`revise_intent`) |
| Bind a stated goal to a checkable outcome contract before execution begins | Covered | CD-0012; `internal/store/workflow_outcome.go` |
| Require human approval at the consequence boundary before autonomous work | Covered | `approve_contract` and `confirm_premise` actions, `internal/store/workflow_dispatch.go` |
| Triage a defect before committing to a fix shape | Covered | `break_fix` workflow definition (reproduce → diagnose → repair → verify), `internal/store/workflow_registry.go` |
| Declare which laws a unit of work is mandated by, and which it amends | Covered | CD-0015; `spec_mandate` / `law_modifies` in `internal/store/workflow_completion.go` |
| Block planning and completion on unknown laws or unresolved law conflicts | Covered | CD-0015; `internal/store/workflow_completion.go` |
| Frame an initiative that spans several units of work, with a narrative and an ordered entry set | Covered | `concord_work_epic.create`, entry mutations, narrative revision, and bounded `entries` read in `internal/agent/mutations.go`, `internal/agent/runtime.go`, and `contracts/agent-tool-surface.v1.json`; `TestDispatchEpicSurfaceUsesEpicEventsAndBoundedEntriesRead` proves the reachable event and read path. |
| Track lightweight future work that is not yet ready to start | Excluded | CD-0009 rejects additional trackable kinds. Early-lifecycle work items carry this, and a separate backlog entity would reintroduce the trackable proliferation the decision closes. |
| Audit drift between recorded law and current implementation | Covered | Manifest records carry `evidence` paths; `scripts/check-knowledge-index.py` fails when an evidence path rots (CD-0026). Structural reachability, not semantic verification. |

## 2. Visibility and portfolio status

Seeing the state of the portfolio without blind spots, across sessions.

| Outcome | State | Evidence or reason |
|---|---|---|
| See every Product as a row with focus, attention, and actionable counts | Covered | C14; `internal/store/product_row.go`, `internal/portfolio/read.go` |
| Get a bounded Product snapshot with lifecycle counts and staleness | Covered | Q2, `internal/store/query.go` |
| List and filter work in a Product or Project with stable pagination | Covered | Q3, `internal/store/query.go` |
| See what is blocked and why | Covered | Q4, `internal/store/query.go` |
| See what is ready, ranked | Covered | Q5, `internal/store/query.go` |
| Look up work across Projects | Covered | Q6, `internal/store/query.go` |
| Read the typed history of one unit of work | Covered | Q7, `internal/store/query.go` |
| Read the typed relation graph around one unit of work | Covered | Q8, `internal/store/query.go` |
| Navigate portfolio → Product → work from a terminal surface | Covered | `internal/launcher/model.go`, `internal/launcher/render/bubbletea/` |
| See the full Product scope from the operator surface | Excluded | Per CD-0021 D2, "across the full Product scope" means every Product is reachable from the launcher, which the S1 portfolio delivers. Result sets spanning Products stay excluded by C18 §12 anti-requirement 11 and CD-0014. |
| Create work from the operator surface without holding an agent grant | Excluded | Per CD-0021 D1, the operator plans by reaching work in the launcher and opening a session that authors it. The launcher stays read-only and work creation keeps a single write authority. |
| See which concurrent agent sessions are active and which are blocked on an operator decision | **Not covered** | No session projection. Tracked separately as issue #72. |

## 3. Implementation changes

The spec-driven lifecycle from confirmed intent to released, evidenced change.

| Outcome | State | Evidence or reason |
|---|---|---|
| Drive a unit of implementation work through a staged lifecycle with required evidence at each stage | Covered | `implementation` workflow definition, `internal/store/workflow_registry.go` |
| Bind completion to recorded proof rather than assertion | Covered | `bind_evidence`, `RequiredEvidenceKinds`, `internal/store/workflow_completion.go` |
| Require an evaluator distinct from the author on high-risk work | Covered | `EvaluatorIndependence`; verdict-actor distinctness in `internal/store/workflow_completion.go` |
| Reject a delivered end-state weaker than the approved one | Covered | CD-0012; `CompareOutcomeStrength` in `internal/store/workflow_outcome.go` |
| Absorb a mid-execution discovery without discarding completed work or silently substituting scope | Covered | `link_successor` forward composition, `internal/store/workflow_dispatch.go` |
| Resume interrupted work without losing workflow position or repeating external effects | Covered | Attempt epochs, claim/checkpoint/complete fencing in `internal/store/fence.go` |
| Survive a working-window boundary without dropping law, approvals, or position | Covered | CD-0016; `checkpoint_context` and `cross_context_boundary` in `internal/store/workflow_continuity.go` |
| Restart cleanly into a typed agent after a boundary rather than summarizing | **Not covered** | The lane registry now exists, so the original blocker is gone; typed restart dispatch was never implemented and still fails closed. Tracked under issue #120. |
| Cancel work safely, with approval and recorded evidence | Covered | Terminal lifecycle transitions require approval and evidence, `internal/agent/mutations.go` |
| Delegate a bounded execution attempt to a typed worker, verify which model actually ran, and record that evidence durably | Covered | CD-0017; `internal/store/worker_lanes.go`, `adapter/opencode/dispatch.ts`, and the `worker-dispatch` / `worker-complete` / `worker-fail` evidence verbs in `cmd/concord/main.go` |
| Drive research, implementation, and review lanes through one end-to-end workflow with typed evidence | Covered | `WF48-lane-pipeline-typed-evidence` in `scenarios/workflow-engine.v1.json` dispatches three registered lanes on one workflow and accepts a completed attempt from a distinct owner. |
| Measure lane prompt behaviour with an evaluation harness | Covered | `adapter/opencode/evals/promptfooconfig.yaml` evaluates every registered lane through its pinned model; `scripts/check-lane-evals.py` fails on drift from the lane registry. |
| Isolate each unit of work in its own git worktree, and refuse writes to the trunk checkout | Covered | `internal/store/worktrees.go` owns worktree creation as one durable cross-authority operation (atomic claim, probe-before-create, verify, append verified locator, reconcile on interruption) and reclamation from git facts; `internal/agent/authority.go` refuses mutating grants bound to the main checkout. Reachable as `concord_work_transition.worktree_claim` / `.worktree_reclaim`.
| Sub-divide a change into an ordered task graph with per-task evidence policy | Excluded | CD-0013 makes the workflow step the unit of execution authority, with evidence obligations declared per workflow definition. A second sub-unit with its own evidence model would duplicate that authority. |
| Run quality scanners over a change — code smell, architectural inconsistency, competitive comparison, optimization survey | Excluded | [`capability-placement.md`](./capability-placement.md) §3: analysis tooling is an external native authority. Concord coordinates such work through the `static_analysis` workflow type and records its verdict; it does not implement scanners. |
| Obtain an acceptance verdict from an authority the implementing agent cannot read or influence | Covered | CD-0013 D5 and CD-0017 D4 hold the influence half; CD-0023 read-scopes the recorded verdict so every session except the recorded executing actor audits it (`internal/agent/verdict_scope_test.go`). |

## 4. Research and investigation

Commissioning, validating, and reusing investigation so work does not start blind.

| Outcome | State | Evidence or reason |
|---|---|---|
| Track an independent investigation as first-class work with its own completion evidence | Covered | `research` workflow definition, `internal/store/workflow_registry.go` |
| Explore a bounded architectural question, record options, and reach a recorded decision | Covered | `architecture_spike` workflow definition, `internal/store/workflow_registry.go` |
| Persist a reusable research pack with revisions, findings, sources, and consumer bindings | Covered | `concord_work_define.research_pack_create` and its authoring operations (CD-0025) reach the pack-operation boundary; `concord_work_trace.research` reads it back. |
| Prove a consumer read a sufficiently fresh research revision before relying on it | Covered | `workflow_action` declares research bindings; the engine validates, binds the consumer, and refuses required reliance on non-current freshness inside the action's transaction (`internal/agent/research_surface_test.go`, CD-0025). |
| Detect that a recorded plan has gone stale against current reality, and revise it | Covered | Staleness rules and `replace_outcome` / `supersede_contract` in `internal/store/workflow_revision.go` |
| Record post-completion learning about how the work itself went | Covered | A reflection is a lesson with a `reflection` tag, recorded through `concord_work_compact.lesson_publish` (CD-0026 D3). |

## 5. Ops runbooks

Executing operational procedures with approval, conditions, and rollback.

| Outcome | State | Evidence or reason |
|---|---|---|
| Execute an operational procedure with an approval step, checkpoints, and a cleanup obligation | Covered | `ops_runbook` workflow definition, `internal/store/workflow_registry.go` |
| Roll back a partially executed operation | Covered | `rollback_run` action, `internal/store/workflow_registry.go` |
| Wait on an external signal without polling, and distinguish waiting from never-completable | Covered for waiting | `add_condition` / `resolve_condition` / `cancel_condition`, `internal/store/workflow_conditions.go`. Distinguishing never-completable is tracked as issue #87. |
| Record the health of an operational run | Covered | `record_health` action, `internal/store/workflow_registry.go` |
| Claim a shared resource so concurrent agents do not collide outside the repository | **Not covered** | Tracked as issue #88. |
| Throttle local test execution, wait on remote CI, keep worktrees fresh, bootstrap a project | Excluded | [`capability-placement.md`](./capability-placement.md) §6 places these as host scripts. They are cross-tool executables that outlive any coordination system, and Concord observes their results rather than owning them. |
| Install, sync, pin, and roll back the coordination runtime itself | Excluded | Owned by the release and installer path (`scripts/release.py`, `scripts/install.py`), which CI validates. Not coordination territory. |

## 6. Durable product knowledge

Knowledge that outlives the change that produced it.

| Outcome | State | Evidence or reason |
|---|---|---|
| Publish a canonical durable note to a deterministic home, with publish proof | Covered | PM6; `internal/store/git_knowledge.go` |
| Verify a published note still matches its recorded digest | Covered | `VerifyCommittedNote`, `internal/store/git_knowledge.go` |
| Search durable knowledge by kind, tag, text, and time window | Covered | Q9, `internal/store/knowledge_query.go` |
| Resolve the canonical note for a completed unit of work | Covered | Q10, `internal/store/knowledge_query.go` |
| Keep specifications as binding law with typed relations between them | Covered | CD-0015; `docs/decisions/CD-0015-typed-law-relations.md` |
| Bind a law change to the work that justified it, so law and history move together | Covered | `law_modifies` amendment path, `internal/store/workflow_completion.go` |
| Preserve the provenance link between a unit of work and its external tracking record | Covered | `external_ref` on capture, `internal/agent/mutations.go` |
| Capture a per-change learning and promote the durable ones to project scope | Covered | `concord_work_compact.lesson_publish` records a lesson per change under operator approval; explicit scopes promote it to project/Product reach through the existing scope-filtered reads (CD-0026 D1/D2). |
| Preserve provenance when an item is promoted into an initiative | Covered | `concord_work_epic.add_entry` folds the typed `parent` relation and ordered `epic_entries` projection through `store.EpicEntryEvent`; the scoped agent boundary test verifies removal clears the relation without changing the child. |

## 7. Cross-cutting

Territory that appears across all six and is an outcome in its own right.

| Outcome | State | Evidence or reason |
|---|---|---|
| Many concurrent agents mutate shared state on one machine without lock contention | Covered | Ten-process conformance harness, `internal/store/` |
| Reconstruct any subject at any point in its history | Covered | `ReconstructSubjectAt`, `internal/store/reconstruction.go` |
| Rebuild every projection from the event log without loss | Covered | `internal/store/reconstruction.go`; rebuild byte-equality scenario in `scenarios/workflow-engine.v1.json` |
| Constrain what an agent may do, scoped to a Product or Project | Covered | Grants and scope validation, `internal/agent/authority.go` |
| Refuse a mutation whose scope version is stale, while allowing a refreshed read | Covered in path, unverified in split | `internal/agent/runtime.go`. Recorded as `fc2-context-freshness` (`unmeasured`). |

---

## Coverage tally

| State | Count |
|---|---|
| Covered | 55 |
| Not covered | 3 |
| Excluded with reason | 7 |

**Total enumerated outcomes: 65.**

The three not-covered entries cluster into four groups:

1. **Session continuity** — active-session visibility is tracked by issue #72 and
   clean typed restart by issue #120 (§2, §3).
   unreachable; issue #131 owns its deliberate floor exclusion (§4).
   spec/implementation drift audit; issue #129 owns the durable learning path
   (§1, §4, §6).
4. **Shared-resource coordination** — a durable cross-repository claim is tracked
   by issue #88 (§5).

The operator-surface and isolation groups are gone: CD-0021 resolved the former as
deliberate exclusions; issues #124 and #125 covered the latter; and CD-0024 makes
initiative coordination reachable.

## Predecessor surfaces consumed as evidence

Floor condition 6 has a second half: any predecessor surface Concord *consumes* as
evidence must be stable enough for the reliance placed on it.

**Concord consumes no predecessor runtime surface.** No Go, TypeScript, or Python
source in this repository reads predecessor state, invokes a predecessor tool, or
imports a predecessor artifact. Every predecessor reference in the codebase is the
English verb "advance" in unrelated prose.

This is the state CD-0010 rule 5 requires — the predecessor is reference-only, and
Concord does not dual-write or treat predecessor runtime state as a second
authority. The reliance is therefore zero, and the stability question resolves
without needing a stability assessment.

Predecessor *documents* remain design evidence — see
[`advance-predecessor-lessons.md`](./advance-predecessor-lessons.md) and
[`feature-inventory.md`](./feature-inventory.md) — but a document read during
design is not a consumed runtime surface.

If Concord later consumes a predecessor surface at runtime, this section must be
replaced with a real per-surface stability assessment before any condition-6 claim.

## What this document does not do

It does not authorize migration, set a readiness date, or sequence the not-covered
work. [`floor-readiness.md`](./floor-readiness.md) records state; issues plan work;
[`priorities.md`](./priorities.md) defines the floor. A condition-6 claim requires
every entry above to be covered or excluded with an accepted reason, judged against
`priorities.md` rather than against this file.
