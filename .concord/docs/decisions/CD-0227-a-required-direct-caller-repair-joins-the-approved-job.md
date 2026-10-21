# CD-0227: A required direct-caller repair joins the approved job

- **Status:** Accepted
- **Date:** 2026-10-15
- **Scope:** A bounded direct-caller repair inside an editing job's existing parent authority.
- **Amends:** [CD-0205](CD-0205-worker-jobs-bind-dispatch-and-separate-local-acceptance-from-delivery.md) D1-D2 and [CD-0220](CD-0220-repository-authoring-follows-lane-capability-and-approved-scope.md) D2, by admitting the conditional caller scope recorded below.
- **Preserves:** [CD-0012](CD-0012-bind-stated-goals-to-delivered-outcomes.md), [CD-0017](CD-0017-typed-workers-and-model-routing.md) D4, [CD-0222](CD-0222-architecture-bound-product-law-with-package-boundary-code-ownership.md) D5-D7, and contract supersession's existing authority.
- **Dependency:** The direct-caller allowance below is admitted with its
  dispatched job; [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
  D6 governs: no size cap, size measurement, or mandatory split exists.
- **Approval:** The operator accepted this record as written on 2026-10-15. Implementation requires a separate assignment.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D6, D7, D14

## Context

A strict owner change can make an existing direct caller refuse its own valid requests.
Deferring the caller because a later slice owns its file would deliver a broken integration.
The caller and the changed owner need one verified repair within the approved behavioral objective.

`internal/agent/mutations.go` contains caller-side request plans, including `planCorrectDelivery`.
`internal/store/workflow_admission.go` and `internal/store/workflow.go` govern contract supersession and predecessor authority.
Those owners do not authorize a worker to repair a direct caller outside its recorded path scope.
A contract-recovery route must not become an implicit path-scope bypass.

## Decision

### D1. Conditional caller scope belongs in the dispatched job

Every editing job under a new caller-capable definition records a closed direct-caller allowance beside its explicit path scope.
The allowance binds the changed owner mechanisms, the permitted Project, and the parent contract's approved Domain and law boundaries.
It authorizes only the minimum source and test changes needed to preserve an existing direct caller's approved request behavior after the assigned owner change.
The job digest and dispatch packet bind this allowance before execution.
Read-only jobs receive no allowance and gain no edit capability.

The allowance is part of approved job scope, not a worker-authored scope amendment.
This rule narrowly amends CD-0220 D2's path interpretation while preserving its prohibition on expanding parent authority.
The worker may use the allowance without another coordinator turn when all its conditions hold.
Another job of the same work item that names the caller path in its scope or reserved work is not a reason to defer the necessary caller repair.
At dispatch, the packet binds the recorded paths of other unresolved cumulative corrections as exclusions from the allowance, without changing the recorded job digest.
A discovered conflict returns to coordinator integration before edits.
A caller path under another work item's scope, resource claim, or architecture overlap requires coordinator reconciliation before edits.
The coordinator owns integration order and prevents overlapping job assignments in the shared worktree.

### D2. Evidence must establish a direct break and a minimal repair

A direct caller invokes the changed owner mechanism through an existing call or declared interface edge.
The caller repair must preserve previously valid behavior, not add a new behavior, unrelated cleanup, or a transitive caller chain.
The worker records the owner and caller source paths and symbols, the direct edge, and the request or fixture that exposes the break.
Generated calls require their owning input change and regeneration, never a hand-edited generated output.

Before the caller fix, the same request must pass on the baseline and fail with the owner change alone.
After the fix, it must pass with the combined change.
For a compile-time break, the caller's compile check supplies that signal instead of an invented runtime refusal.
The report must distinguish observed output from an inferred cause.
Minimum means the source and tests necessary to preserve that request behavior, not an arbitrary line allowance or permission to weaken validation.
Independent review judges semantic necessity. The core checks identities and containment, not prose claims of minimality.

The worker carries typed `direct_caller_repairs` in its result, bound to the dispatched job, allowance, baseline, actual delta, and check evidence.
The existing host observation records the actual delivered subject.
The coordinator dispositions that result through ordinary acceptance.
Every path outside explicit scope must match an evidenced caller repair and remain within the allowance's authority.
Neither an unrecorded repair nor a worker's statement alone supplies acceptance evidence.

### D3. Caller repair stays inside the allowance and its authority

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: no production-line cap, size measurement, or mandatory split exists;
the caller repair joins the job's recorded scope.
Tests retain their normal classification and verification obligations. They are not permission to hide production code in test paths.
A caller path outside the allowance's recorded mechanisms returns to the coordinator before edits.
The coordinator owns that return with the ordinary acceptance, not a new scope approval or job revision.
The record binds the unchanged dispatched job and actual delivered subject; recorded classifications are never relabeled ([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

The worker must not synthesize an executing actor, approval, authority grant, or producer observation to make a caller request pass.
It must carry real authenticated context through the existing caller boundary and retain all owner-side validation.
A requirement to add a Product, Project, Domain, law modification, approved outcome, or new external effect returns to the coordinator's existing approval boundary.
A law conflict or conflict with another work item's authority also returns there before affected edits.
Necessity for the caller does not confer the missing authority.

No blanket contract supersession or self-repair exemption is introduced.
Ordinary changes within the allowance need no new whole-contract approval.
Changes outside it require the existing scope or contract-revision route and its actual approval when required.
A recorded job without the allowance cannot acquire it from a worker report; the coordinator records a newly authorized job revision before dispatch ([CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it)).
The predecessor's unresolved obligations remain under CD-0205 D4.
A caller allowance or new revision alone cannot discharge them; [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) governs: no attempt refund or retry permission exists.

### D4. Change scope and report owners, not contract-recovery semantics

Implementation must extend worker-job authoring in `contracts/agent-tool-surface-payloads.schema.json` and the packet schema's `lane_packet_worker_job`.
`internal/store/worker_jobs.go` owns immutable allowance content, digest derivation, authority joins, and readiness.
`contracts/agent-lane-report.schema.json` and `internal/store/worker_lanes.go` own bounded repair reports and exact job bindings.
`adapter/opencode/packet.ts` projects the allowance, and `adapter/opencode/dispatch.ts` admits its typed report without inferring authority.
The existing result-acceptance owner checks the repair bindings and evidence.
Shared delivery admission still requires integration evidence for the combined result.
`scripts/generate-agent-lanes.py` states the allowance and its stop boundaries without granting Concord tools to workers.

`internal/store/workflow_admission.go` and `internal/store/workflow.go` retain their contract-recovery semantics.
Their predecessor self-repair checks neither prove a direct call nor authorize an extra source path.
[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)
governs: the allowance is carried explicitly by the one canonical source.
Generated derivation invents no allowance, caller proof, or accepted repair ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Alternatives considered

- Defer every caller to a later slice. Rejected: an isolated owner pass can leave the actual request unusable.
- Let a worker revise arbitrary path scope. Rejected: necessity does not grant parent authority.
- Require a coordinator amendment for every contained direct caller. Rejected: the dispatched conditional allowance already grants that bounded repair.
- Add callers recursively. Rejected: this would remove the direct-edge boundary and permit unbounded scope.
- Weaken owner checks or synthesize approval context. Rejected: a caller fix must preserve the real authority boundary.
- Treat contract supersession self-repair as caller permission. Rejected: predecessor recovery and repository edit scope are different responsibilities.

## Consequences

The combined change preserves the actual caller request rather than only the owner's isolated tests.
Workers can resolve a contained direct-caller break without waiting for a routine scope amendment.
The coordinator still owns wider scope, law conflicts, shared ownership, and integration order.
This proposal creates no runtime behavior until the operator accepts it and a separately authorized implementation passes verification.

## Verification

- `python3 scripts/check-doc-contract.py` checks the registered outline and prose. Draft review must call its `check_record` function with proposed-record metadata before registration.
- `python3 scripts/check-knowledge-closure.py` reports an unregistered draft as unprocessed. Acceptance must register the approved revision and pass strict closure without asserting implementation coverage.
- `go test ./internal/store` must cover allowance containment, immutable digests, exact report bindings, missing allowances, non-editing lanes, transitive callers, and historical absence.
- `go test ./internal/agent ./internal/store` must prove baseline success, owner-only caller failure, and combined success with real authority. Negative cases must reject synthesized actors, approvals, and weakened owner validation.
- `go test ./internal/store` must preserve allowance accounting, integration evidence, Domain/law boundaries, and refusal of caller paths owned by another work item (CD-0242 D6).
- `bun test adapter/opencode/packet.test.ts adapter/opencode/dispatch.test.ts` must prove exact allowance projection and bounded repair admission without worker Concord access.
- `python3 scripts/generate-agent-lanes.py --check` and `python3 scripts/check-agent-contracts.py` must pass after regeneration. Independent review must assess the direct edge, minimal delta, and actual caller evidence.
- `go test ./internal/store` must cover a caller path discovered after dispatch, its coverage by the allowance at acceptance, and preservation of the original job digest (CD-0242 D6).
