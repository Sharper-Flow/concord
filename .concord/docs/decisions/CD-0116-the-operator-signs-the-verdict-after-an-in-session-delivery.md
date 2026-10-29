# CD-0116: The operator signs the verdict after an in-session delivery

- **Status:** Accepted
- **Date:** 2026-09-08
- **Scope:** The recorded operator-substitution route after delivery
- **Related:** CD-0013, CD-0242, CD-0243

## Context

A session that delivered work cannot invent independence by changing its actor
label. Recorded operator evaluation is distinct from a native verification result.

## Decision

### D1. Verified operator authority follows a definition-backed exit

For definitions without `approval_policy=consequence_v1`, `record_verdict` and
`complete` may use verified operator identity after the declared advancing exit
into the verdict step. `record_delivery`, research `record_report` and retained
architecture `record_decision` keep their definition-backed meaning. An action
name from another family supplies no exit authority.

Host approval retains its operation digest, expected versions, scope, session
and worktree bindings. The operator is recorded as actor without rewriting
the caller or recorded facts. A read failure refuses rather than inferring
independence. An unsigned self-evaluation remains forbidden on this route.

### D2. Consequence-based workflows retain coordinator recording

Under `consequence_v1`, verdict and completion do not independently request
host approval. Main records them against the accepted ready Review on the exact
candidate and passing native receipts under CD-0242 D5/D8/D9. Independent native
Task review remains required, but Concord attests no subagent producer identity.
Applicable approval classes remain under CD-0243.

### D4. Operator acceptance creates no verification result

Every evidence, predicate, premise and version gate remains. Approval never
asserts that an absent test passed. Deferred scope needs an explicit contract
revision, not a passing verdict on an unrun check. Worktree movement cannot
clear subject or authority requirements.

## Alternatives considered

- Treat approval as verification. Rejected: consent cannot create producer evidence.
- Treat a native Task result as an operator act. Rejected: the coordinator
  records outcomes; operator identity keeps its own approval bindings.

## Consequences

The recorded operator route remains available where applicable, without worker
roles, executing-lease rotation or report authentication.

## Verification

- `go test ./internal/store ./internal/agent` checks applicable evaluation
  authority, exact approval bindings, missing exits and evidence refusals.
- `python3 scripts/check-knowledge-index.py` checks identity and hashes.
