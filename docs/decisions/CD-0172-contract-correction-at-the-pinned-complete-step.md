# CD-0172: Contract correction at the pinned complete step

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** Store admission for operator-approved contract supersession at the pinned completion step, the workflow step return, and the verdict carryover rule
- **Approval:** The operator approved this bounded recovery route for a disproved contract at completion.
- **Related:** CD-0013, CD-0030, CD-0059, CD-0115, CD-0124, CD-0133, CD-0143
- **Amends:** CD-0133 at its correction checkpoint, and CD-0030 D1 for the one scoped reader D6 adds
- **Preserves:** CD-0143's unhealthy-verdict route, released workflow definitions, definition digests, predecessor contracts, verdict history, worker attempt history, and operator authority

## Context

A break-fix or implementation work item can reach its pinned completion step
under an approved contract whose premise a later durable record contradicts.
The workflow instance may even record completion before the contradiction is
known. The correction routes closed at that step, so the only paths forward
were completion under a contract the operator no longer holds, or a terminal
work item that strands the record.

A regression hit exactly this shape. A break-fix item's workflow instance
recorded completion while the work item stayed nonterminal, and the
contradiction arrived only afterward as a same-work observation. The complete
step declared no evidence-binding action and admitted no correction route, so
no mechanism could reopen the work under a corrected contract.

## Decision

### D1. Admit correction at the pinned complete step

`supersede_contract` is available at the step that declares `complete` on a
break-fix or implementation workflow when the work item lifecycle is
nonterminal, exactly one approved contract is active, no worker attempt is
unsettled, and a same-work observation was recorded after the latest recorded
verdict. The completion step declares no evidence-binding action, so the
post-verdict observation is the durable contradiction record: it is the one
typed record a public route can append there. Any other work kind or step
refuses.

Action discovery, the work pin, preflight, the dispatch guard, the fold, and
replay share one admission predicate.

### D2. Require the exact successor through the operator boundary

The correction keeps the typed supersede payload: the exact successor contract,
audit evidence, the current work version, and the verified operator approval.
The successor must declare the reserved route convention
`complete_step_correction`. The convention is refused on every other route and
on every directly approved contract. The fold refuses a successor whose
convention does not match the supersession step.

### D3. Return the instance to the declared external effect

When a typed supersession folds at the pinned complete step, the instance
returns to the external-effect step the pinned definition declares, and the
instance state returns to `running`. Break-fix returns to `repair` and
implementation returns to `execution`. The pinned definition, its digest, and
the definition pin do not change. The next delivery runs through the ordinary
fenced starts.

### D4. Cut predecessor verdicts and evidence off the successor and its descendants

A contract that declares the reserved convention accepts no verdict recorded
under a predecessor contract version. Predicate identity and payload equality
carry no verdict across this supersession. The same cut bounds the evidence
history. The supersession event is the cutoff. A binding recorded at or
before the cutoff satisfies no required evidence kind, no spec mandate, and
no verification obligation of the successor.

The cutoff is ancestry state, not a property of the active contract row: a
later ordinary successor of a corrected contract keeps the cutoff and reads
the same bound, so the route marker cannot be dropped by superseding the
corrected contract. Only fresh post-cutoff evidence and verdicts re-establish
a contract. A contract approved directly, and a contract with no corrected
contract in its ancestry, keep the whole evidence history. Fresh delivery,
fresh required evidence, an independent review, and a fresh verdict under the
current contract are the only path back to completion. Completion stays
fail-closed, `request_correction` keeps the unhealthy-verdict route unchanged,
and the correction window opens at the cutoff: historical healthy verdicts
baseline neither the successor's predicates nor its correction bound.

### D5. Keep the terminal boundary on the work item

A completed workflow instance whose work item is nonterminal admits
`supersede_contract` and refuses every other action. A terminal work item
lifecycle refuses contract recovery as before. CD-0092's main-checkout refusal
is untouched.

### D6. Read the contradiction record without promoting it

The admission predicate reads a post-verdict same-work observation's
existence and its ordering against the verdict history. It reads no
observation content, satisfies no evidence kind with it, and promotes nothing
through it; CD-0018's promotion path is unchanged. The correction's authority
stays where D2 puts it: the verified operator approval and the successor's
audit evidence. CD-0030 D1 keeps its non-authority rule for every other
reader; this predicate is the one scoped reader this decision adds.

## Verification

- The complete-step route admits correction with the reserved convention and refuses without it.
- A same-work observation recorded after the latest verdict admits the route, and an observation that predates the verdict admits nothing.
- A completed instance with a nonterminal work item reopens to `repair` or `execution` under the unchanged pinned definition.
- Predecessor contracts, verdicts, attempt history, and the definition digest survive the supersession.
- Identical successor predicates inherit no predecessor verdict, and a fresh verdict under the successor restores the completion path.
- Predecessor evidence bindings satisfy no required kind, mandate, or verification obligation of the successor: with only predecessor bindings on the log, premise confirmation refuses, and completion refuses at its evidence clause.
- The cutoff persists across a later ordinary successor of the corrected contract: that successor inherits neither the pre-correction verdict nor the pre-correction bindings.
- Duplicate contract recovery refuses at the pinned complete step, and a stale-law condition admits correction only through the same shared gate.
- Rebuilding the log refolds the route supersession to the same instance return, and a historical supersession at the step replays without the reserved convention.
- A missing observation, an unsettled attempt, a terminal item, an unsupported shape, a missing approval, and a stale version each refuse before any effect.
