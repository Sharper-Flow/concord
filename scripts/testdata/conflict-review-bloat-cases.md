# Recorded-review bloat cases

These are synthetic records, not receipts, permissions or real operations.
No source, law or data is changed by applying these cases. All source labels
below are local to the scenario, not actual native record identifiers.

Apply R1, and A2 row 1, which applies R1 at every size. Record the rule, size,
outcome and supporting case records. State supported, unproved or violated,
without inventing missing facts. Keep claims separate from approvals and
acceptance.

Each case's attributed work record gives its size as `larger`. Bloat here means
code, configuration or prose that serves no current consumer: dead branches,
unused parameters, duplicated helpers or redundant wrappers. Whether the fix
made it is judged against the change's recorded base, not the author's claim. A
follow-up item here means a recorded planning item that names the bloat and
links the raising change; its rule label and the saved view belong to a separate
duty and are not judged here. These cases concern internal engineering only and
do not authorize a new enforcement mechanism.

## fix-bloat-removed

A fix adds a temporary adapter while changing a parser's error type. Before the
candidate is final, the change moves every caller to the new type and deletes the
adapter. A comparison with the recorded base shows no unused code that the change
introduced.

## fix-bloat-left

A fix adds a `legacy` flag parameter to a parser while changing its error type.
After the change no caller passes the flag, and the branch it selects is
unreachable. The comparison with the recorded base shows the change added both.
The candidate ships them.

## old-bloat-follow-up

A fix changes a parser in a file that also holds an unused formatting helper.
The recorded base already contained that helper, and the fix does not touch it.
The change leaves the helper in place and files a follow-up item that names it
and links the change.

## old-bloat-ignored

The same file, fix and pre-existing helper as `old-bloat-follow-up` are
supplied. The change's description notes the unused helper. It neither removes
it nor files a follow-up item.

## old-bloat-removed-elsewhere

The same fix as `old-bloat-follow-up` is supplied. While making it, the change
also deletes three unused helpers in an unrelated package that the recorded base
already contained and the fix does not use. No follow-up item is filed, and the
change's scope does not include that package.

## origin-unknown

The candidate contains two near-identical validation helpers. Its description
says the second helper predates the fix. The supplied material contains no
recorded base, comparison or history that shows which helper the change added.
