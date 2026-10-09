# CD-0206: An accepted no-ship review opens the settling review obligation

- **Status:** Accepted
- **Date:** 2026-10-05
- **Scope:** The refinement review admission and the historical delivery-gate
  corrective return
- **Amends:** CD-0166 D6, at its trigger: the delivery review recovery opens
  on either coordinator disposition, not only a rejected refinement result;
  CD-0201 D3, at its subject: the obligation behind the settling rule opens
  on the same two dispositions, and a completed review that is not the newest
  is stale
- **Preserves:** CD-0201 D1, the single admission owner; CD-0201 D2, the
  liveness law; CD-0115 D1, released definition content and digests; CD-0197
  D6, a lane report alone carries no workflow authority; every other route
  refusal keeps its scope (CD-0186)
- **Related:** CD-0166, CD-0197, CD-0201
- **Approval:** The operator approved the amendment in session chat on
  2026-10-05 for the no-ship delivery-gate defect. This record serves
  contract version 2 of work `work-24f181c52c44d61e5b7604af` and legislates
  the behavior that PR #1531 delivered with no record.

## Context

CD-0166 D6 scopes the delivery review recovery to a rejected refinement
result. CD-0201 D3 settles that debt only through a ship or absent verdict.
An initial `no_ship` review carries no rejection, so it opened no obligation.

The result: a historical implementation version 19 acceptance advanced the
refinement pass to delivery, and the gate then refused `request_correction`
because no rejection existed. PR #1531 delivered the shared disposition, the
`verification` correction context, and the gate hold, with no law record.
A review of the superseded first repair found one more gap, still open after
that merge: a pending `no_ship` review survived a newer completed review, so
an older report could still bind its findings.

## Decision

### D1. One obligation opens on both coordinator dispositions

A rejected refinement result and an accepted review-capability report with a
`no_ship` verdict are the same obligation. The shared derivation reads the
latest such disposition, its freshness frontier, and the settling review, so
the admission fold, the accept guard, the correction binding, the preflight,
and the work pin answer identically.

### D2. An accepted no-ship review opens the obligation

The coordinator accepts a ready `no_ship` review as evidence. That acceptance
binds the findings and opens the settling obligation like a rejected result.
On the historical pins the acceptance advances the refinement step as
recorded; the obligation then holds the delivery gate, so `record_delivery`
refuses and the evidence-bearing corrective return stays admitted. Only a
later accepted review with a ship or absent verdict settles it, and its
dispatch must follow the obligation and the result's latest refinement
activity. A report alone, without the acceptance, opens nothing.

On oracle-bound histories the shared context retains canonical ranked-finding
IDs, their oracle bindings, and receipt references. Accepted `no_ship`
evidence leaves the settling debt outstanding and is not productive
acceptance. It creates no convergence basis family. A later correction
record uses the exact derived open set through the existing correction
route; an unrelated P0 follow-up remains visible to the decision owner
without becoming a repair obligation.

### D3. Only the newest completed review can bind

A completed review that a newer completed review has superseded is stale.
The acceptance of a stale review refuses with the fresh-review refusal, on
current and released definition pins, whatever the folded state names.

### D4. The historical gate returns through the truthful disposition

A historical delivery gate with the obligation outstanding keeps the typed
corrective return of CD-0166 D2. The context reports the `verification`
disposition that names the accepted review, never a fabricated rejection,
and names the origin event's predicates or the active contract's predicates.
Operator identity and durably bound evidence stay required. An ordinary
parked gate without the obligation keeps its existing route.

No definition version changes. Admission code is not definition content
(CD-0115 D1).

## Alternatives considered

- Fabricate a coordinator rejection for the historical acceptance. Rejected:
  it changes the recorded disposition and invents authority.
- Hold the refinement step instead of the gate. Rejected: the merged
  recovery holds the gate, and rewriting recorded history is not admission
  code's to do (CD-0115 D2).
- Amend CD-0166 D6 in place instead of by record. Rejected: the corpus amends
  by explicit amendment record, so accepted history stays readable.
- Let any pending review bind after a newer one completes. Rejected: the
  newer evidence supersedes the older findings.

## Consequences

An accepted initial `no_ship` review opens the settling obligation, and a
parked historical gate with that obligation outstanding admits the
evidence-bearing corrective return with the truthful `verification`
disposition. An accept that names a superseded review refuses. Public packet
schemas and existing correction consumption stay unchanged.

## Verification

```gherkin
Scenario: The first accepted negative review opens the obligation
  Given a completed no_ship review without a coordinator rejection
  When the coordinator accepts its findings without delivery
  Then the acceptance opens the settling review obligation
  And the delivery gate refuses record_delivery and admits the corrective return

Scenario: Historical negative review delivery retains correction
  Given a historical delivery gate after an accepted no_ship review
  And no coordinator rejection exists
  When the operator requests correction with durably bound evidence
  Then the return reports the verification disposition and keeps the contract

Scenario: A stale pending review cannot bind
  Given a completed no_ship review and a newer completed review at the step
  When the coordinator accepts the older review
  Then the acceptance refuses with the fresh-review refusal

Scenario: A settling review preserves ordinary delivery
  Given a historical delivery gate after an accepted ship or legacy review
  When the coordinator reads correction admission
  Then the negative-review corrective return is absent
```

- `go test ./internal/store/ -run 'TestInitialNoShipReviewHoldsDeliveryBehindTheObligation|TestInitialNoShipHistoricalDeliveryKeepsTypedCorrection'` proves the first and second scenarios.
- `go test ./internal/store/ -run 'TestPendingNoShipReviewDoesNotOutliveAFreshSettlingShip|TestRenewedNoShipObligationCarriesItsOwnEvidence'` proves the third scenario and the renewal provenance.
- `go test ./internal/store/ -run 'TestInitialSettlingReviewDoesNotOpenHistoricalDeliveryCorrection|TestAcceptedNonReviewReportDoesNotOpenReviewObligation'` proves the fourth scenario and the boundary refusals.
- `python3 scripts/check-doc-contract.py` proves this record carries the current decision outline and passes the writing rules.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0206 identifier allocates once.
- `TestOwnerOracleFinding` and `TestOwnerOracleRepairFamily` cover retained findings and receipts without converting follow-ups into repair obligations.
