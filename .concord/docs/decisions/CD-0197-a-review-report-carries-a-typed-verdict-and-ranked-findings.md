# CD-0197: A review report carries a typed verdict and ranked findings

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The subject-bound Review outcome recorded by the coordinator
- **Related:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5, D7 and D8

## Context

Prose alone cannot distinguish ranked findings, their disposition and the
candidate they judge. Workflow admission needs that typed outcome without
owning the native Task that produced it.

## Decision

### D1. The coordinator records the typed review block

`record_review` takes `review`, the existing typed Review block. It names the
input and output candidates, criteria and architecture dependencies, findings,
remaining obligations and disposition. No lane, job, attempt or report envelope
is required. Main owns the claim and must obtain independent native Task review.
Concord does not attest the producer's identity.

The block carries at most 64 findings. Each finding has severity `P0` through
`P3`, confidence `low`, `medium` or `high`, and detail of 1 to 512 bytes.
The closed tokens remain checkable without pretending numeric precision.

### D4. Review reports one of four dispositions

The disposition is continue, correction required, revision required, or ready
for final proof. Main records its acceptance against the current candidate
and depended-on revisions. An open in-scope blocking finding blocks a new
assignment and leaving execution. A report about another subject grants no
current acceptance.

### D5. Recorded content stays truthful

The current Review state owns the report. Reads return its recorded content;
they invent no finding, resolution, receipt or subagent provenance. Supported
reads of retired launcher state report absence under CD-0242 D2 and D13.

### D6. Review content is not completion authority

A recorded Review outcome supplies no verification receipt, operator approval
or completion by itself. Verify uses `worktree_verify` receipts and
`record_verdict`. Required CI and completion retain their existing gates.

## Alternatives considered

- Parse severity and disposition from prose. Rejected: ambiguous claims cannot
  supply structural admission.
- Authenticate a Task report through a Concord launcher. Rejected: the host
  owns subagent execution under CD-0242 D6.

## Consequences

The coordinator records a typed, subject-bound outcome without execution
attestation. Independent review remains required; fabricated proof is forbidden.

## Verification

- `go test ./internal/store` checks Review shape, subject/dependency admission,
  current-state recording and disposition gates.
- `python3 scripts/check-doc-contract.py` checks the decision outline.
- `python3 scripts/check-knowledge-index.py` checks identity and hashes.
