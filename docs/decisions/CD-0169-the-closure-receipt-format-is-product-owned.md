# CD-0169: The closure receipt format is product-owned

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** The closure receipt format, its render verb, and the adapter
  delegate
- **Approval:** The operator approved one owner for the receipt bytes.
- **Related:** CD-0170, CD-0113

## Context

A completed work item prints a closure receipt. The receipt had two defects.
The adapter owned a fenced ASCII box with hand-padded columns, and no
product function owned the format. The padding math measured emoji-width
glyphs with UTF-16 lengths, so verified marks could not sit inside the box
without breaking the alignment the borders exist to provide.

An agent could hand-format the fixed pattern, and a second renderer would
drift from the first. A fixed operator-facing pattern needs one owner and
one verb.

## Decision

### D1. One Go renderer owns the receipt bytes

The `internal/receipt` package renders the closure receipt. Its `Render`
function takes a work pin and returns the receipt string. The `concord
receipt` verb declared in `commandSpecs` is the only render surface: it
reads the pin through `ReadWorkPin` and prints the string. An agent shells
one call and never formats the pattern itself.

### D2. The receipt is an unfenced single-column markdown table

The receipt is one markdown table with a single column. The table's header
row is the plane line: glyph U+1F6EB, the Linear issue key or the work ID,
the word `Complete`, and the work ID in parentheses when an issue key is
present. The first body row is the summary row: U+2705 and the work title,
which states what the completion delivered. One row per verified predicate
follows, marked U+2713: the mark states membership in
the gate-verified set, not the predicate's kind. The label joins the typed
fields CD-0170 binds with a middle dot and holds at most sixty-four code
points. A longer label keeps its first sixty-three code points and ends
with an ellipsis. The receipt never carries a code fence, and no code pads
columns: the host markdown renderer owns alignment. The exact bytes are
fixed by the golden tests in `internal/receipt`.

### D3. The adapter delegates and keeps no formatting logic

The adapter's work-state reporter calls the verb through the existing runner
at a terminal lifecycle and queues the bytes the verb prints. The verb
prints nothing for a work item outside the completed lifecycle, so a
cancelled or superseded closure queues no notice. The adapter holds no
hand-padding machinery: `closureCell`, `closureHeadingCell`,
`CLOSURE_CELL_MAX`, and the code fence do not exist in the adapter. A failed
call appends a warning and never changes the mutation outcome.

Content law stays CD-0170: completed lifecycle only, verified contract
predicates only, and an ambiguous projection degrades by omission.

## Rejected alternatives

**Keep the fenced ASCII box and escape the emoji.** String math cannot
align double-width glyphs, and the golden bytes would measure per font.

**Render the receipt in TypeScript beside the Go renderer.** Two owners is
the drift this record repairs.

**Retire the TS grid behind a flag.** A hidden twin records dual ownership.

**Amend CD-0170 in place.** CD-0170 owns receipt content. Format is a
second law and takes its own record.

## Consequences

One function owns the receipt bytes, and one verb reaches it. The receipt
gains verified marks without string column math. The host markdown renderer
owns alignment. Cancelled and superseded closures stay silent, and an
ambiguous projection shows the header row alone.

## Verification

- The `internal/receipt` golden tests pin the table bytes, the marks, the
  sixty-four-code-point abbreviation, the header fallback, and the degrade
  paths.
- The store pin tests prove the completed projection carries the approved
  criteria, while non-completed and ambiguous projections omit them.
- The adapter delegation tests prove the reporter shells the verb at a
  terminal lifecycle and queues its bytes.
- The agent contract generator regenerates the payload schema and the Go
  and TypeScript projections.
