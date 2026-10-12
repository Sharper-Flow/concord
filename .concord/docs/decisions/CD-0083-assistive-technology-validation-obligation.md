# CD-0083: Assistive-technology validation is an obligation, not a deferral

- **Status:** Accepted (amended 2026-10-09, 2026-10-10)
- **Date:** 2026-08-29
- **Scope:** the assistive-technology validation obligations CD-0014 §147 and
  C14 §9 carried for the launcher render surface, vacated with that surface;
  issue #534
- **Approval:** Operator approved splitting the deferral on 2026-08-29 and
  directed that the no-color half close against named anchors while the
  screen-reader half keeps an operator owner. The operator approved the
  vacatur of both halves with the launcher render surface on 2026-10-09
  through the Snowball coordinator (operator-delegated) for
  [Concord (CON) issue 908](https://linear.app/sharper-flow/issue/CON-908).
  The Snowball coordinator (operator-delegated) approved this record alignment
  on 2026-10-10 at 07:10 Eastern Daylight Time (EDT).
- **Amended by:** CD-0219 (2026-10-09) vacates the no-color render anchors,
  the operator screen-reader obligation, the C14 §9 render condition, and the
  CD-0014 falsifier with the retired launcher render surface
- **Related:** CD-0014, CD-0219, C14 §§9 and 11, issue #527
- **Amends:** CD-0014 §147
- **Supersedes:** The deferral of assistive-technology validation to launcher
  implementation acceptance

## Context

CD-0014 §147 deferred screen-reader and assistive-technology validation to
"launcher implementation acceptance". That acceptance had happened, and this
record split the dead deferral on 2026-08-29: the machine-provable no-color
half closed against named render anchors, and the screen-reader half became
an open operator obligation with a live trigger.

The launcher render surface retired with the terminal launcher TUI under
CD-0219. Both halves named that surface as their subject, so the vacatur
retires both: the render anchors no longer exist, and no launcher screen
exists for a screen-reader run to cover.

## Decision

### D1. Both validation obligations retire with the render surface

Amended 2026-10-09: the no-color render condition of C14 §9 and the operator
screen-reader obligation are vacated with the retired launcher render surface
(CD-0219). No repository test claims assistive-technology behavior, and this
record claims none.

CD-0014's validation-failure falsifier retired with CD-0014's rendering
decision (CD-0219).

## Consequences

- C14 §9 carries no screen-reader/no-color render condition; the condition
  retired with the render surface.
- Coverage for this record carries an out-of-scope state, not a satisfied
  one.
- The retirement records a vacated subject, not a claim about assistive
  technology: an unverified condition was never a disproved one.

## Verification

- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-doc-links.py`, and
  `python3 scripts/check-knowledge-index.py` pass with this record indexed
  exactly once.
- No test anchor is claimed: the obligations this record carried are vacated
  with their surface, and the coverage record carries an out-of-scope state.
