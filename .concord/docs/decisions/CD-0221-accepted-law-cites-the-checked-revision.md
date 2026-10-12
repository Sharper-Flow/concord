# CD-0221: Accepted law cites the repository revision under review

- **Status:** Accepted
- **Date:** 2026-10-10
- **Scope:** Repository path and Go test citations in accepted Product law
- **Related:** CD-0036, CD-0055, CD-0114, CD-0175, CD-0194
- **Time zone:** Eastern Daylight Time (EDT)
- **Approval:** Snowball coordinator, operator-delegated, 2026-10-10 01:08 EDT

## Context

Accepted law contains repository paths and Go test names in inline code spans.
Markdown link validation does not resolve those spans. A moved or removed
artifact can leave a current obligation with a citation that names no owner
or proof.

CD-0036 distinguishes current law from its recorded revisions. A reference
check must preserve that distinction without treating a deletion requirement,
an example, or a foreign repository path as a current dependency.

## Decision

### D1. Resolve current citations at the checked revision

Concrete repository citations in accepted decisions, specifications, and
constitutional records resolve at the repository revision under review.
The manifest owns record status. A superseded or published record is outside
this rule. The legacy outline profile grants no reference exemption.

A local check reads the candidate working tree. Continuous integration reads
the checked commit. Neither resolves a current citation against an inferred
acceptance commit. Historical law revisions remain available through Git.

### D2. Use explicit citation forms

A standalone inline code span names a repository path when its entire content
has relative path syntax and contains a slash. Path components contain letters,
digits, underscores, periods, or hyphens. A trailing slash names a directory.
The form applies regardless of which top-level directories still exist.

Bare paths and paths starting with `./` resolve from the repository root.
A path starting with `../` resolves from the citing document's directory.
A root file or extensionless command can use the `./` prefix. A citation can
append a numeric line or line range after a colon; resolution checks the path,
not the continued meaning of that line.

A qualified Go test citation has the form `<package-path>.TestName`.
The package path is relative to the repository root. The named test must have
a Go test declaration in that package. A citation proves presence, not that
the test proves the cited obligation or ran successfully.

The script path in a single `python3 scripts/<file>.py` command citation also
resolves. Concrete file arguments in a single Bun test command and concrete
package arguments in a single Go test command resolve as repository paths.
The checker does not execute a cited command or interpret a shell program.

### D3. Distinguish citations from other operands

Fenced examples and spans containing angle-bracket placeholders are not
concrete citations. Globs and Go package wildcard selectors are patterns,
not literal file citations. A pattern alone cannot replace a concrete proof
citation that a contract requires.

A Markdown link carries its own target. The existing Markdown link checker
owns that target; a code span in the link label does not create another target.
Foreign paths and historical artifacts use links to their owning source.
A historical Git link pins a commit, not a branch name.

A removed artifact can remain a negative requirement without a positive
repository citation. The record states the required absence in ordinary prose,
or links to the historical artifact. Runtime output locations use a placeholder
root rather than claim that the checked tree already contains an output.

Git references, import identifiers, and other slash-shaped identifiers do not
use standalone repository-path citation syntax. Their owning link or command
supplies their meaning. The checker does not infer meaning from nearby prose.

### D4. Keep amendments and proof together

A rename or deletion that invalidates a current citation repairs the citing
accepted record in the same change. A surviving obligation names its surviving
owner or proof. Removing citation formatting does not repair a lost proof.

CD-0036 owns whether the law change is a compatible amendment or a breaking
replacement. This rule creates no supersession, approval, or workflow bypass.
No stale-reference baseline, per-record exemption, or compatibility artifact
admits a missing current citation.

### D5. The document-contract checker owns enforcement

`python3 scripts/check-doc-contract.py` checks these references with the
existing document contract. It reports the record path, source line, and
unresolved target. Enforcement uses the existing manifest setting.

Path resolution stays inside the repository and uses the candidate file
inventory. An ignored generated file cannot satisfy a citation. A directory
citation resolves through at least one candidate file below that directory.

Go test resolution uses the native Go parser through the shared evidence-anchor
projector. A projector failure is a tool failure, not evidence that a test is
absent. The checker reports that failure separately and cannot pass enforcement.

## Alternatives considered

- Resolve every reference at an acceptance commit. Rejected: a compatible
  amendment changes the body in place, and record identity does not supply
  that commit. This approach also hides later deletion of a current owner.
- Require every slash-shaped span to exist without defining citation syntax.
  Rejected: examples, foreign paths, and required absence would become false
  findings. Explicit forms make the verdict deterministic.
- Infer absence or foreign ownership from nearby prose. Rejected: wording
  would decide whether a missing dependency passes a required check.
- Add absence markers, a historical exemption list, or a second checker.
  Rejected: the current law, source links, and document-contract checker already
  own those responsibilities.
- Recognize paths only under the current tracked root directories. Rejected:
  removal of a whole root would remove that root from the detector itself.

## Consequences

Accepted law and the repository revision under review remain consistent.
Deletions can land after the checker, but must update surviving citations.
Git preserves removed source without an empty file or compatibility path.

Existing path-shaped identifiers and negative references need explicit source
links or placeholder forms. Review still checks substantive coverage; target
existence does not prove that an obligation has an adequate test.

## Verification

- `python3 scripts/test-doc-contract.py` proves accepted-record selection,
  legacy-profile enforcement, missing files and directories, escaped paths,
  ignored artifacts, explicit examples, linked labels, and command citations.
- `python3 scripts/test-doc-contract.py` proves that deletion of a cited file
  or its whole root fails until the current citation is repaired.
- `python3 scripts/test-doc-contract.py` proves that a missing qualified Go
  test fails and a projector failure reports its own failure.
- `python3 scripts/test-law-coverage.py` proves that citation presence does
  not weaken the assertion requirement for a satisfied coverage anchor.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-doc-links.py`, and
  `python3 scripts/check-knowledge-closure.py` pass over the amended corpus.
