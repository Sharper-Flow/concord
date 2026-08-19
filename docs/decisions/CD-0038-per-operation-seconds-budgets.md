# CD-0038: Operation time budgets are explicit, per-operation, and never clamped

- **Status:** Accepted
- **Date:** 2026-08-17
- **Scope:** Agent operation inputs and manifest bounds; TS7 budget refusal; issue #173
- **Approval:** Operator selected per-operation manifest ceilings on 2026-08-17
- **Related:** CD-0005, CD-0036 D4, TS1 AJ8, TS4
  ([`agent-mutation-tool-contract.md`](../agent-mutation-tool-contract.md)), TS6
  ([`agent-adapter-transport-contract.md`](../agent-adapter-transport-contract.md)),
  TS7 ([`agent-result-envelope.md`](../agent-result-envelope.md)), TS8
  ([`agent-tool-surface-evolution.md`](../agent-tool-surface-evolution.md))
- **Supersedes:** the undifferentiated `budget.max_millis` operation-time input
- **Historical surface note:** CD-0042 supersedes the pre-go-live agent-surface
  compatibility and negotiation treatment described in D6/D7; those passages are
  historical evidence, not current compatibility policy.

## Context

The current shared budget object limits output bytes and items and accepts one
global `max_millis` value up to 300,000. The runtime turns that value into a Go
context deadline. Nothing declares what one operation supports, so a caller
cannot discover a limit and a refusal cannot say what value would work.

TS1 `AJ8-budget-refused` fixes the missing behaviour: an approved audit asks for
60 seconds, the operation supports 30, the core refuses before starting, and it
returns 30. Silently running for 30 seconds would be worse than refusing because
the caller could mistake a truncated audit for a complete one.

## Decision

### D1. The caller budget is a shared domain input

Every operation input may carry:

```text
requested_budget_seconds   # optional integer, minimum 1
```

One shared schema definition supplies the field to all operation inputs. It is
not part of the hidden TS5 call envelope: the budget is model-visible intent and
belongs in the canonical request digest. It is not nested in the existing
result-size budget object, whose required bytes/items fields have different
meaning.

Changing the requested budget changes the canonical request. A refused request
records no idempotency effect, so the caller may lower the budget and reuse the
same idempotency key. Once an operation commits, the same key with a different
budget is a different request and receives `idempotency_conflict`.

The budget does not bind cursor identity. It bounds one invocation, not the
query population or ordering.

### D2. Every operation declares a ceiling

The canonical manifest records `supported_budget_seconds` on each operation.
The manifest schema and generator require exactly one value for all operations,
and the manifest digest covers it. A caller can inspect the same contract the
adapter and core use before making a call.

The global manifest maximum is 300 seconds, preserving the already accepted
300,000 millisecond bound. Initial per-operation values are 300 seconds unless
accepted scenario evidence says otherwise. `concord_work_transition.workflow_action`
declares 30 seconds because that is the operation through which the accepted AJ8
audit runs and TS1 fixes its supported value at 30.

Later values change only with accepted scenario or TS9 measurement evidence.
Deployment configuration cannot silently replace them. The 30-second value is
an accepted extrapolation from one audit scenario to every `workflow_action`;
if evidence shows different action families need different ceilings, the
surface must split that distinction structurally rather than inspect prose.

### D3. Unsupported budgets refuse before any effect

If the requested value exceeds the operation ceiling, the core returns:

```text
kind: budget_refused
recovery_action: adjust_budget
supported_budget_seconds: <declared ceiling>
effect_state: none
```

`supported_budget_seconds` is a typed `TypedError` field, required for
`budget_refused`. The requested value may be echoed in bounded diagnostic
details, but the ceiling needed for recovery is not hidden there.

Admission happens after strict input validation. For mutations, the core
first performs the idempotency lookup: same key and digest returns the original
committed result even if a later surface lowers the ceiling. A request that has
not committed is checked against the negotiated operation ceiling before scope
resolution, approval consumption, or any domain/external effect. Reads have no
idempotency record and check the ceiling before query execution. No path may
lower the request and continue.

### D4. An accepted budget is a real deadline

If the requested value is present and within the ceiling, the core creates one
context deadline at dispatch and propagates the remaining budget through store,
Git, and other cooperative calls. A callee does not start a fresh full timeout.

If the caller omits the field, Concord applies no operation deadline. Existing
internal bounds, such as SQLite lock waits and per-command Git timeouts, remain.
Omission is not a request for the maximum.

### D5. Expiry reports the effect that may exist

- A read that expires returns `timeout`, `effect_state=none`, and
  `retry_same_request`.
- An inline SQLite mutation that expires before commit returns the same shape.
- If commit may have occurred, the response is not `timeout`. It returns
  `operation_conflict` with reconciliation so cancellation cannot erase a
  possible effect.
- Core-owned cross-authority work returns its durable `pending` or `partial`
  operation and the same operation ID.
- Native-owned work records what the native authority reported. Context
  cancellation is not proof that an external effect stopped or rolled back.

This applies CD-0036's authority-stop rule: Concord can stop granting authority
or waiting, but it does not claim to pre-empt a host process.

### D6. Milliseconds have a bounded compatibility window

`budget.max_millis` is deprecated under TS8. During the compatibility window an
old negotiated client may send it. If both units are present, they must express
the same exact duration; otherwise the core returns `invalid_input`. No rounding
or preference rule is allowed.

New clients send seconds only. Removal follows TS8's 30–90 day window and fails
old bootstrap after the supported old surface retires. The two fields do not
remain indefinitely.

### D7. Surface evolution is additive within its negotiated major

The input is optional and an old client can omit it. An old representation of
`budget_refused` without the typed ceiling remains safe, though less actionable,
so negotiation may omit that field for a supported old client. Relative to the major line that carries it, this change is a TS8 minor amendment. If bundled with CD-0037's major cutover it ships in that major; if
shipped first, it is a minor on the current line. It never requires a second
major by itself. The manifest, generated contracts, adapter, compatibility matrix, corpus, and digests move together.

## Rejected alternatives

**One global ceiling.** It must fit the slowest operation and therefore cannot
protect expensive operations with a lower supported bound.

**Deployment-configured ceiling.** Callers could not discover it and two
installations could answer the same contract differently.

**Budget in the TS5 call envelope.** That envelope is host-injected and hidden
from the model, while the budget is caller intent.

**Keep milliseconds in the result-size budget object.** This couples unrelated
limits and would force byte/item fields onto mutations.

**Honor means only “do not clamp.”** Accepted TS4 law already uses the caller
budget as an execution boundary. A declared budget must become a real propagated
deadline.

**Blanket timeout after deadline.** `timeout` claims no effect. That claim is
false after a commit or an external step may have occurred.

## Verification

- `AJ8-budget-refused` binds against the 30-second workflow-action ceiling and
  proves the operation never started or silently clamped 60 to 30.
- Every operation declares one ceiling not greater than the global maximum.
- Admission happens before approval consumption and effects.
- Accepted budgets create one propagated deadline; no hop resets the clock.
- Read, pre-commit, post-commit, and cross-authority expiry paths return the
  effect state they can prove.
- Equivalent legacy milliseconds and seconds agree; conflicting values refuse.
