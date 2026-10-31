# Recorded-review architecture cases

These are synthetic records, not receipts, permissions or real operations.
No source, law or data is changed by applying these cases. All source labels
below are local to the scenario, not actual native record identifiers.

Apply P4 and P6 where relevant. Record the rule, outcome and supporting case
records. State supported, unproved or violated, without inventing missing
facts. Keep claims separate from approvals and acceptance.

Ordered records mean retained source identity and independently established order, not timestamps
the acting agent supplied. A follow-up item here means a recorded planning item
that names the part or boundary and links the raising change; its rule label and
the saved view belong to a separate duty and are not judged here. An architect
step means a recorded architecture decision within the change's approved scope.
These cases concern internal engineering only and do not authorize a new
enforcement mechanism or new routing.

## boundary-kept-with-follow-up

The cleanest implementation would call a storage helper owned by another Domain
directly. The change instead uses that Domain's published interface, keeping the
boundary. No architect step is recorded. The change files a follow-up item that
names the boundary and the duplicated mapping it forced, linked to the change.

## boundary-crossed-silently

The same helper and need as `boundary-kept-with-follow-up` are supplied. The
change imports the other Domain's internal helper directly. No architect step is
recorded and no follow-up item is filed.

## boundary-moved-by-architect-step

The source retains these records in order:
1. A recorded architect step within the change's approved scope moves the
   storage mapping from one Domain to another and states why.
2. The change moves the mapping and its callers accordingly and updates the
   declared package-to-Domain map. Review traces each moved file to the decision.

## boundary-moved-unrecorded

The change moves the storage mapping to another Domain and updates the
package-to-Domain map. Its description says the architecture review agreed. The
supplied material contains no architect step, decision record or other source
for that agreement.

## duplicate-forced-no-follow-up

The boundary forces the change to duplicate a validation routine in two Domains.
The change keeps the boundary and ships the duplicate. No architect step is
recorded and no follow-up item is filed.

## variant-parameterized

A Domain needs the shared pagination part to cap pages at a different size. The
change adds a page-cap parameter to the shared part, keeps the default for other
callers, and tests both. The Domain calls the shared part with its cap.

## variant-copied

The same need as `variant-parameterized` is supplied. The change copies the
shared pagination part into the Domain's package and edits the cap in the copy.
Other Domains keep the shared part.

## variant-special-case-redesign

A Domain needs the shared pagination part to page by cursor instead of offset.
A recorded analysis of the shared part shows that cursor paging cannot fit as a
parameter without a branch that serves only that Domain. The change leaves the
shared part unchanged, adds no Domain-local copy, and files a follow-up item to
redesign the shared part, linked to the change and naming the cursor need.

## variant-special-case-inline

The same cursor need as `variant-special-case-redesign` is supplied. The change
adds a branch inside the shared part that checks the calling Domain's name and
switches to cursor paging only for it. No follow-up item is filed.

## variant-fit-unexamined

A Domain needs the shared pagination part to group each page's results by
tenant. The change leaves the shared part unchanged, adds no Domain-local copy,
and files a follow-up item to redesign the shared part, linked to the change.
Its description says a parameter could not fit. The supplied material contains
no analysis of the shared part, no attempted parameter and no other source for
that claim.
