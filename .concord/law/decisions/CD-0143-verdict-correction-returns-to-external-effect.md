# CD-0143: Verdict correction returns to the external effect

- **Status:** Accepted
- **Date:** 2026-09-13
- **Scope:** Correction of delivered work after an unhealthy verification verdict
- **Related:** CD-0013, CD-0124, CD-0138, CD-0209, CD-0242
- **Preserves:** Contract authority, native evidence, applicable approvals,
  subject bindings and fail-closed completion

## Context

A delivered candidate can receive an unhealthy verdict. Correction needs a
declared return to production without rewriting the contract or calling that
verdict healthy.

## Decision

### D1. Admit one typed verdict-correction action

`request_correction` requires delivered work and a current unhealthy verdict,
diagnosis, strategy, affected predicates/findings and bound evidence. An
authenticated coordinator records it. No subagent failure disposition is a
prerequisite or alternate ground.

### D2. Return to the declared external effect

The declared route returns implementation to execution and break-fix to repair.
It substitutes neither refine nor a generic retry edge, rewrites no contract
and bypasses no lifecycle gate. Native Task remains host-owned.

### D3. Correction repeats until healthy

Correction ends only when the latest verdict set is healthy and comparable for
every approved predicate. No attempt count, correction budget or escalation
wall gates it.

### D4. Keep completion fail-closed

Completion refuses while any latest approved predicate is unhealthy or
incomparable. Correction creates no healthy verdict and weakens no evidence,
predicate, subject or version check. A stale verdict cannot reopen correction.

## Alternatives considered

- Re-label an unhealthy verdict to resume. Rejected: it fabricates acceptance.
- Guess a return from step order. Rejected: CD-0209 owns declared producer routes.

## Consequences

Correction retains its real evidence and declared target without a worker
attempt or launcher recovery lifecycle.

## Verification

- `go test ./internal/store` checks declared returns, bound evidence, current
  verdicts, no-effect refusals and fail-closed completion.
- `python3 scripts/check-knowledge-index.py` checks identity and hashes.
