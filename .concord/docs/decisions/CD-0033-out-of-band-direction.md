# CD-0033: Out-of-band operator direction binds through contract supersession

- **Status:** Accepted
- **Date:** 2026-08-16
- **Scope:** Outcome-binding law; completion clause 5; operator direction
- **Related:** CD-0005 (D6, TS4), CD-0012, issue #71
- **Supersedes:** nothing

## Context

The operator console is the launcher plus the OpenCode TUI sessions it
launches. Observation and intervention live in the session: the operator can
redirect, correct, or narrow an agent mid-run by typing to it. That division
is intended (CD-0005 D6, TS4): Concord owns authority and durable truth, not
session observation.

The seam: an operator redirect that changes what gets built enters through
the observation channel, which has no write path into Product truth. CD-0012
compares delivered outcomes against what it recorded; a substitution the
record cannot see is drift caught late — or rubber-stamped at clause 5 by the
same operator who redirected.

## Decision

**Operator direction of agent labor is legitimate and stays out-of-band. Its
effects on the declared end-state enter Product truth through exactly one
path: contract supersession, which already requires operator approval.**

The obligation: when operator direction changes the required end-state, the
agent supersedes the contract — recording the new premise, outcome, and
candidate set — before continuing against it. No new mechanism is created:
`supersede_contract` plus `approve_contract` (operator approval required) is
the existing, single write authority.

**The trigger is unverifiable and is documented as such.** Concord cannot
observe the session transcript, so no structural check can detect that
direction occurred and supersession was owed. What *is* structural:

1. **Clause 5 is the check, not a rubber stamp.** Premise confirmation binds
   to the current contract version. After a supersession, prior confirmations
   do not count — the operator must confirm the *new* premise. An
   un-superseded redirect leaves clause 5 naming the *original* premise,
   which the operator will not recognize against redirected work; refusing
   it is the failure made visible at the latest point it can still stop
   completion.
2. **No second write authority.** Direction never writes; only supersession
   writes. Declared decision points (digest-bound operator selections)
   remain the mechanism for positions the workflow declares.

## Authority boundary

Concord is in charge of what enters Product truth: contracts, versions,
approvals, confirmations, outcomes. The session surface is in charge of
directing agent labor. Direction becomes Product truth only through
Concord's write path — never by virtue of having been typed.

## Rejected alternatives

- **Forcing a declared decision point** (candidate 1): interrupts at exactly
  the moment the operator is engaged, and risks a second decision path
  parallel to contract supersession.
- **Accepting as out-of-band with clause 5 as silent backstop** (candidate
  3): leaves clause 5's role assumed rather than stated, and names no
  falsifier for the drift it tolerates.

## Falsifier

The decision reopens when an operator observes a clause-5 confirmation
naming a premise that matches the redirected work while no supersession was
recorded — i.e., the record silently absorbed direction. Until then, every
observed failure mode presents as a refusal the operator can act on.

## Verification

`internal/store/workflow_supersession_confirmation_test.go`: superseding a
contract invalidates prior premise confirmations — clause 5 refuses until
the operator confirms the new premise. That is the structural tooth behind
the obligation: supersession forces re-confirmation; failure to supersede
leaves the stale premise named where the operator must confront it.
