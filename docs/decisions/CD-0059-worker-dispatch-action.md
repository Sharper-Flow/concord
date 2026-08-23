# CD-0059: Worker dispatch is a registered workflow action

- **Status:** Accepted
- **Date:** 2026-08-22
- **Scope:** The authority that admits a worker attempt; the capability that
  guards it; issue #275
- **Approval:** Operator accepted the drafted decision as written on 2026-08-22;
  the public record is an
  [issue #275 comment](https://github.com/Sharper-Flow/concord/issues/275)
- **Related:** CD-0017 (amends D2's dispatch mechanism; makes D4's nested-worker
  and step-window rules structural), CD-0044 (evidence authentication,
  unchanged), CD-0005 D6 and `capability-placement.md` (adapter is
  envelope-thin), CD-0013 (workflow authority)
- **Preserves:** the CD-0044 signed-evidence boundary in full; lane identity
  validation; the worker authority boundary
- **Supersedes:** nothing

## Context

The result side of the worker boundary is modeled. `accept_worker_result` is a
registered action with a consequence class, an approval policy, and an
execution mode; it is folded; and an agent reaches it through
`concord_work_transition.workflow_action`.

The dispatch side has no counterpart. The registered action set is
`accept_worker_result`, `checkpoint_context`, and `cross_context_boundary`. A
workflow can accept a worker result it had no modeled way to request.

Two consequences follow. `dispatchWorker` has no non-test caller, because the
action that would authorize a call was never registered. And a lane agent
spawned directly through the host's Task tool performs lane work that creates
no `worker_attempts` row at all: the CD-0044 signing key never reaches the
worker, so no evidence path is even available to it. Measured on one host, 225
of 246 lane-agent calls ran that way. The evidence boundary was not violated.
It was bypassed, because nothing admits a worker attempt in the first place.

CD-0017 D4 states that a worker "cannot invoke another advancing action once a
worker attempt is dispatched in the current step window." Nothing can enforce
that today, because dispatch is not a folded event and so opens no window.

## Decision

### D1. Dispatch is a registered workflow action

Register `dispatch_worker` in the workflow action registry. Its fold opens the
worker attempt window against the current step epoch. The adapter then runs
authorize, spawn, append evidence, in that order. The core authorizes before
any process starts, replacing an arrangement where `worker-dispatch` records an
attempt that already happened.

This is the placement `docs/capability-placement.md` §4 selects: a capability
that mutates durable state and needs validation, authorization, and approval is
a core domain operation, exposed only through the accepted adapter. CD-0017 D2
places the same boundary from the other side — the adapter validates envelopes
and identity only.

### D2. Its action policy matches `start_execution`

`dispatch_worker` is registered as
`actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventGeneric)`.

Each member follows existing precedent rather than a new judgment:

- **`ActionExternalEffect`** — dispatch launches a host process. This is the
  registered vocabulary member for that case.
- **`ActionFenced`** — `workflow_dispatch.go` passes `ActionFenced` to
  `workflowActionStartEpochForDispatch` as the epoch-fence flag. A fenced action
  opens a window against the step epoch without advancing the step, which is
  exactly what CD-0017 D4 describes. D4 becomes enforceable by construction.
- **`ActionApprovalNone`** — dispatch is the routine operation of the system.
  Gating every lane spawn on human approval would remove the autonomy the lane
  registry exists to provide. The consequence class already marks the action as
  externally effectful.

`start_execution` already carries this exact triple. Dispatch is the same
shape: begin externally effectful work under a fence.

### D3. Dispatch is guarded by a registrable, non-grantable capability

Dispatch requires a new `worker_dispatch` capability. Like `worker_evidence`
(`internal/agent/authority.go`), it is registrable in a trusted client policy
and deliberately absent from the grant-request vocabulary: no bearer grant can
carry it.

`work_transition` is not reused. A worker may hold `work_transition` for other
reasons, so reusing it would leave CD-0017 D4's nested-worker prohibition
resting on convention. Today that prohibition is enforced only by the generated
lane definitions setting `task: false` — host configuration, not Concord law. A
capability workers cannot acquire makes nested dispatch unrepresentable.

### D4. Lane identity validation stays where it is

`ValidateWorkerDispatchIdentity` validates lane and packet coherence. That is
identity, not authority. The registered action supplies the authorization it
does not perform: whether this actor may dispatch for this step now.

### D5. CD-0017 clauses amend as follows

- D2's dispatch mechanism is reached through the registered action; the adapter
  keeps envelope validation and the host spawn.
- D4's step-window and nested-worker rules become structural: the fence is the
  window, and the capability is the nesting prohibition.

## Consequences

- A workflow can request a worker attempt through the same operation that
  already accepts its result.
- A Task-tool spawn of a lane agent cannot produce an authorized attempt, so
  lane work outside the adapter is visible as absent evidence rather than
  indistinguishable from work that was never requested.
- `#253`'s packaging half becomes actionable: the import closure from
  `concord.ts` reaches `dispatch.ts` once a caller exists.
- `#254` is unaffected. The orchestrator identity question stands.

## Rejected alternatives

**A new agent tool for dispatch.** Rejected: it places the authorization
decision in the adapter, which `capability-placement.md` §4 and CD-0017 D2 both
refuse.

**A new CLI verb.** Rejected: it places an agent-reachable authority path
outside the grant and capability machinery `concord_work_transition` already
applies. The existing `worker-*` verbs are deliberately not agent callable.

**Reuse `work_transition`.** Rejected under D3: it makes the D4 nested-worker
invariant conventional rather than structural.

**A packet-carried dispatch token validated by the lane.** Rejected: it is a
second authorization path beside the registered-action mechanism, and it places
the check in the worker rather than in the core.

## Verification

- `dispatch_worker` appears in the registered action set with the D2 policy.
- A dispatch fold opens a fenced window; a second advancing action in the same
  step window is refused.
- A principal without `worker_dispatch` is refused; the capability cannot be
  obtained through a bearer grant request.
- `dispatchWorker` has a non-test caller reached through
  `concord_work_transition.workflow_action`.
- An attempt spawned without an authorized dispatch records no
  `worker_attempts` row and is not mistakable for a completed attempt.
