# Recorded-review repair cases

These are synthetic records, not receipts, permissions or real operations.
No source, law or data is changed by applying these cases. All source labels
below are local to the scenario, not actual native record identifiers.

Apply A2 row 5 (Review finds problems). Record the rule, outcome and
supporting case records. State supported, unproved or violated, without
inventing missing facts. Keep claims separate from approvals and acceptance.

In scope means inside the change's approved contract
scope. Severity uses the review report's `P0`-`P3` scale: `P0` and `P1` are high,
`P2` is medium and `P3` is low. A follow-up item here means a recorded planning
item that names the problem and links the raising change; its rule label and
the saved view belong to a separate duty and are not judged here. These cases
concern internal engineering only and do not authorize a new enforcement
mechanism.

## repaired-and-followed-up

Review finds an in-scope `P2` defect: a parser accepts an empty
key. It commits a fix, runs the parser's tests on the corrected candidate, and
closes the finding citing that passing run. It also reports an out-of-scope `P3`
naming problem in another package. The supplied work context holds a follow-up
item that names the naming problem and links the change.

## in-scope-left-open

Review reports an in-scope `P3` finding: a new error message omits
the failing field. It marks the finding non-blocking, makes no edit, and returns
disposition ready for final proof. The report gives no reason the fix is outside
the change's scope or beyond Review's reach.

## fix-unverified

Review commits a fix for an in-scope `P2` finding and reports it
closed. The report cites no check run on the corrected candidate, and the supplied
material holds no such run.

## out-of-scope-dropped

Review fixes and verifies every in-scope finding. It reports an
out-of-scope `P2` defect in a shared helper the change calls but does not edit.
The change completes. The supplied work context holds no follow-up item for the
defect, and no record states that one was filed.

## follow-up-claimed

Review fixes and verifies its in-scope findings and reports one
out-of-scope `P3` finding. Its report says a follow-up item was filed. The
supplied material contains no work context, item or link that shows the item.
