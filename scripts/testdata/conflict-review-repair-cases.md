# Recorded-review repair cases

These are synthetic records, not receipts, permissions or real operations.
No source, law or data is changed by applying these cases. All source labels
below are local to the scenario, not actual native record identifiers.

Apply A2 row 5 (Review finds problems). Record the rule, size, outcome and
supporting case records. State supported, unproved or violated, without
inventing missing facts. Keep claims separate from approvals and acceptance.

Each case gives its size. In scope means inside the change's approved contract
scope. Severity uses the review report's `P0`-`P3` scale: `P0` and `P1` are high,
`P2` is medium and `P3` is low. A follow-up item here means a recorded planning
item that names the problem and links the raising change; its rule label and
the saved view belong to a separate duty and are not judged here. These cases
concern internal engineering only and do not authorize a new enforcement
mechanism.

## repaired-and-followed-up

Size `larger`. Review finds an in-scope `P2` defect: a parser accepts an empty
key. It commits a fix, runs the parser's tests on the corrected candidate, and
closes the finding citing that passing run. It also reports an out-of-scope `P3`
naming problem in another package. The supplied work context holds a follow-up
item that names the naming problem and links the change.

## in-scope-left-open

Size `larger`. Review reports an in-scope `P3` finding: a new error message omits
the failing field. It marks the finding non-blocking, makes no edit, and returns
disposition ready for final proof. The report gives no reason the fix is outside
the change's scope or beyond Review's reach.

## fix-unverified

Size `small`. Review commits a fix for an in-scope `P2` finding and reports it
closed. The report cites no check run on the corrected candidate, and the supplied
material holds no such run.

## out-of-scope-dropped

Size `larger`. Review fixes and verifies every in-scope finding. It reports an
out-of-scope `P2` defect in a shared helper the change calls but does not edit.
The supplied work context holds no follow-up item for it, and the report records
none as missing.

## crucial-medium-repaired

Size `crucial`. The first Review reports an in-scope `P2` finding, marks it
non-blocking, and returns ready for final proof. The coordinator's attempt to
leave execution is refused because the finding is open. The coordinator
dispatches a correction, and a second Review on the corrected candidate closes
the finding citing a passing run of the declared check. Execution then ends.

## crucial-medium-waived

Size `crucial`. Review reports an in-scope `P1` finding and marks it
non-blocking. The coordinator's notes say the finding is minor, and the change
is described as delivered while the finding remains open in the latest accepted
Review.

## follow-up-claimed

Size `larger`. Review fixes and verifies its in-scope findings and reports one
out-of-scope `P3` finding. Its report says a follow-up item was filed. The
supplied material contains no work context, item or link that shows the item.
