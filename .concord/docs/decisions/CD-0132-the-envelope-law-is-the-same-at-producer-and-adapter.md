# CD-0132: The envelope law is the same at producer and adapter

- **Status:** Accepted
- **Date:** 2026-09-11
- **Scope:** The TS7 envelope contract and its two validators: the Go
  producer-side validator and the adapter TypeScript validator, and the
  error outcome's carriage of committed refs
- **Approval:** The operator approved the contract in Concord
  work-f0b589d86cab4ccfe4875866 on 2026-09-10, after issue 1039 showed the
  two validators disagreeing on the same envelope.
- **Related:** CD-0040, CD-0075, issue 757, issue 1039
- **Amends:** Nothing. This record adds the parity rule the envelope
  contract implied but no validator enforced.
- **Preserves:** Closed-world envelope validation, effect truth on partial
  application, and generated-artifact single-sourcing

## Context

The TS7 schema expresses its closed-world rule on `ok`, `partial`, and
`errorOutcome` through `unevaluatedProperties: false`. The Go validator
implemented only `additionalProperties: false`, so it accepted envelopes
the adapter refused. `auditReclaimPostCommitFailure` attached
`changed_refs` to an error envelope without validating it: the audit's
committed writes reached callers only as adapter salvage, and the failure
surfaced as an unrelated `operation_ref` member (issue 1039).

## Decision

### D1. The two validators enforce one law

The Go validator tracks evaluated properties through `allOf`, `anyOf`,
and `oneOf`, and enforces `unevaluatedProperties: false`. Any envelope the
adapter refuses is refused at the producer before it reaches the wire.

### D2. An error outcome may carry committed refs

`errorOutcome` admits `changed_refs`, a changedRef array bounded at 32.
Effect truth on a partially-applied mutation is exactly what
`changed_refs` states; refusing it forced the truth into salvage.

### D3. A failure path validates its output

`auditReclaimPostCommitFailure` validates its envelope before returning
it. An invalid shape degrades to `malformed_response` while still
carrying the committed refs with `effect_state: possible`.

## Verification

```text
go test ./internal/agent/
cd adapter/opencode && bun test
```

Green on the delivery branch; the three version-skew adapter failures
reproduce on `main` unchanged and are out of scope.
