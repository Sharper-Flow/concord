# CD-0198: Small items batch, merge, and derive routine workflow actions

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** Batched verdicts, typed diagnosis and derived producing-step starts
- **Related:** CD-0012, CD-0156, CD-0166, CD-0173, CD-0183, CD-0192, CD-0242, CD-0243

## Context

Separate calls for each verdict, diagnosis section and step start add
coordination cost without adding an independent judgment.

## Decision

Contract authority, evidence and independent Review remain mandatory. Consent
follows CD-0243's six classes in conversation. Premise confirmation uses
authenticated coordinator identity. Batching never silently
omits an approved predicate or supplies fabricated proof.

### D1. One record_verdict call carries each predicate it judges

The batched form carries one or more verdict objects, each naming an approved
predicate of the active contract and its verdict kind, evaluation evidence and
incomparable flag. Duplicate predicate IDs refuse. Each verdict retains its
actor and records atomically. An omitted predicate stays missing and blocks
completion. Independent Review and captured native receipts remain required;
Concord attests no subagent identity.

### D2. Break-fix records one typed diagnosis

`record_diagnosis` advances the diagnosis step only with reproduction,
alignment-search and root-cause sections, each retaining its fields and guards.
The search includes `searched`, `outcome` and `related_ids` under CD-0156 D3.
It writes the same alignment declaration and creates no relation. An outcome
of found-nothing is distinct from not searched. Implementation retains its
declared alignment step. Analysis cannot advance without the search beside it.

### D3. Repair and refine starts derive from the first action

The first action at repair or refine derives the step-start fact in its own
transaction. The first recorded action wins, opens the epoch and binds the
fence from its actor. Delivery still requires that start. Refine proof must
come from a green, unchanged-tree `worktree_verify` run acquired after it and,
where declared, match exact tooling argv from the default ref under CD-0192.
A prior run remains stale. Current rows own these facts; generated definitions
derive from one canonical source.

Whole-work delivery uses `record_delivery` and its complete admission. A Review
outcome is recorded separately through `record_review`; it does not combine
subagent acceptance, delivery or retry dispatch.

## Alternatives considered

- Replace typed actions with a progress paragraph. Rejected: prose cannot
  preserve per-action validation.
- Remove contract or verdict gates to save calls. Rejected: those judgments
  remain independent requirements.

## Consequences

Batching changes transport and derivation, not evidence or approval owners.
No worker-failure record, combined worker acceptance or retry launcher exists.

## Verification

- `go test ./internal/store ./internal/agent` checks batching, duplicate and
  omitted predicates, diagnosis completeness, first-action starts and delivery
  refusal for missing, stale, failed, dirty or undeclared verification.
- `python3 scripts/check-knowledge-index.py` checks identity and hashes.
