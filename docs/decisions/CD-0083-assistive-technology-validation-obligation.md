# CD-0083: Assistive-technology validation is an obligation, not a deferral

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** CD-0014 §147; C14 §9; `docs/terminal-launcher-contract.md`; the
  launcher no-color render path; issue #534
- **Approval:** Operator approved splitting the deferral on 2026-08-29 and
  directed that the no-color half close against named anchors while the
  screen-reader half keeps an operator owner.
- **Related:** CD-0014, C14 §§9 and 11, issue #527
- **Amends:** CD-0014 §147
- **Preserves:** CD-0014's falsifier rule that a validation failure reopens the
  rendering decision
- **Supersedes:** The deferral of assistive-technology validation to launcher
  implementation acceptance

## Context

CD-0014 §147 deferred screen-reader and assistive-technology validation to
"launcher implementation acceptance". That acceptance has happened. S1 shipped
through issue #45, S2 and S3 through issue #51, and the CD-0048 answer-stack
composition through issue #239.

A deferral to a passed event cannot fire. It reads like tracked work and tracks
nothing. `docs/terminal-launcher-contract.md` repeats the same sentence twice,
so the dead deferral appears three times across accepted law.

The deferral also joined two obligations of different kinds. One is machine
provable and already proved. The other needs a person and real software.

C14 §9 lists "screen-reader/no-color textual interpretation" among its
prototype-acceptance conditions. The launcher was built for it:
`internal/launcher/projection.go` emits textual reliance markers so meaning
survives without color, and `cmd/concord/main.go` honors `NO_COLOR`.

## Decision

### D1. No-color textual interpretation is discharged

The no-color half of C14 §9 is proved and closed. Rendering with color disabled
keeps every semantic marker as plain text, and the render output carries no
terminal control bytes.

`internal/launcher/render/bubbletea.TestNoColorOutputIsPlainTextAndKeepsAllSemanticMarkers`
and `internal/launcher/render/bubbletea.TestRenderIsStableNoColorAndResizeDoesNotRead`
are the anchors. C14 coverage names both.

Determinism of the projection is a separate guarantee. It does not by itself
prove textual interpretation, so it does not discharge this condition.

### D2. Screen-reader validation is an operator obligation with a live trigger

Screen-reader validation is not deferred. It is open work owned by an operator,
because it requires a person running assistive technology against a live
terminal. No automated test in this repository substitutes for it.

The obligation states its own terms:

- The operator runs the launcher under one named screen reader on Linux. Orca is
  the reference technology, because Linux is the only release platform.
- The run covers the portfolio screen, one Product detail screen, and one
  blocked or degraded row.
- The run passes when the operator identifies the Product that needs attention,
  states why, and reaches the next Product, using announced output alone.
- The result is recorded on the owning issue with the screen reader name and
  version.

The trigger is the next launcher screen that changes announced content or
navigation order. A launcher change of that kind requires the run before merge.

### D3. A validation failure remains a CD-0014 falsifier

CD-0014 §147 made a failed validation a falsifier of the rendering decision.
That rule survives this amendment. A failed screen-reader run reopens CD-0014
and may reopen C14 under its §11 falsifiers when the failure is a row-content
or focus-ranking defect rather than a rendering defect.

## Consequences

- C14 §9 has one open condition rather than two, and the open one names an
  owner, a technology, a procedure, and a pass test.
- A launcher change that alters announced content carries a stated obligation
  instead of an expired deferral.
- No test claims to prove assistive-technology behavior. The repository states
  what it has not verified rather than implying coverage it lacks.
- CD-0014 keeps its original text. This record states the current rule without
  rewriting an earlier decision into false history.

## Rejected alternatives

**Close the deferral against the corpus.** Rejected because
`scenarios/launcher-portfolio.v1.json` declares no screen-reader condition. Its
five cases cover row composition, focus priority, coverage states, session, and
first run. Citing it would record evidence that does not exist.

**Treat projection determinism as sufficient.** Rejected because determinism
proves the same input yields the same output. It says nothing about whether a
screen reader announces that output usefully.

**Write an automated screen-reader test.** Rejected because a harness driving a
screen reader proves the harness reads its own output. The condition is whether
an operator can act on what is announced, which needs the operator.

**Delete the condition.** Rejected because nothing established that the
launcher is unusable with assistive technology. An unverified condition is not a
disproved one, and deleting it would convert missing evidence into a claim.
