---
{
  "id": "CD-0033",
  "title": "Out-of-band operator direction binds through contract supersession",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0033: Out-of-band operator direction binds through contract supersession

- **Status:** Accepted
- **Date:** 2026-08-16
- **Scope:** Outcome-binding law; completion clause 5; operator direction
- **Related:** CD-0005 (D6, TS4), CD-0012, issue #71
- **Amended:** Concord (CON) issue CON-910 (2026-10-13) re-homes the operator
  console to the host-started session under the Snowball coordinator's
  operator-delegated approval.
- **Amended:** 2026-10-18.

**Approval amendment:** [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes)
limits this record's operator-consent requirements to its six classes; its D5 governs recovery and permanent loss. All other checks remain required.

## Context

The operator console is the host-started OpenCode session. Observation and
intervention live in the session: the operator can
redirect, correct, or narrow an agent mid-run by typing to it. That division
is intended (CD-0005 D6, TS4): Concord owns authority and durable truth, not
session observation.

The seam: an operator redirect that changes what gets built enters through
the observation channel, which has no write path into Product truth. CD-0012
compares delivered outcomes against what it recorded; a substitution the
record cannot see is drift caught late — or rubber-stamped at clause 5 by the
same operator who redirected.

## Decision

Out-of-band direction changes a committed contract only through attributed, versioned supersession with audit evidence. Operator decisions follow CD-0243; neither a contract revision nor a chat message enacts Product law or waives governing requirements. Actual law changes follow CD-0230 D4.


**Operator direction of agent labor is legitimate and stays out-of-band. Its
effects on the declared end-state enter Product truth through exactly one
path: attributed contract supersession.**

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
