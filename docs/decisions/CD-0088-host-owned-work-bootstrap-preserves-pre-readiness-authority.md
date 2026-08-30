# CD-0088: Host-owned work bootstrap preserves pre-readiness authority

- **Status:** Accepted
- **Date:** 2026-08-30
- **Scope:** Host-side work capture, canonical worktree creation and claim,
  session preparation, and child-agent launch
- **Approval:** The operator approved the issue #611 implementation plan
- **Related:** CD-0010, CD-0031, CD-0049, CD-0072, CD-0079, issue #611
- **Preserves:** CD-0010 and the default-checkout implementation firewall

## Context

The ordinary agent surface could not start managed work from a registered Project
default checkout. Work capture needed mutation authority, but the authority boundary
refused all such mutations there. A worktree claim also needed an existing work item,
and the host could not move its active session after startup.

Manual worktree creation avoided the boundary but did not satisfy the Product
contract. It also split one operator intent across unrelated actions with no durable
recovery identity.

CD-0010 creates a separate constraint. Concord can ship a generic bootstrap mechanism
before it has authority to use Concord as the coordinator of its own development.
Capability presence and self-host activation are different decisions.

## Decision

### D1. Bootstrap is one host-owned operation

The host exposes `concord_work_start` outside the ordinary ten-tool agent surface. It
derives Product and Project identity from the registered default checkout. Callers
cannot supply either identity.

The operation captures or resumes one work item, derives its canonical worktree
intent, creates and claims that worktree, prepares the required Concord agent, and
launches the child session in the claimed directory.

### D2. The store owns recovery identity before Git changes

One idempotency key derives one operation and work identity. The store records the
request and its state in the same transaction that captures the work item. Git changes
start only after that durable record exists.

The operation uses the registered Project locator and `worktree-locate` result without
recomputing branch, base SHA, or path policy. A retry reconciles the recorded intent
with native Git state, then creates only the missing state. A native branch or
worktree that survives interruption remains attached to its recorded operation and is
not an orphan.

### D3. Bootstrap authority is narrow

`work-bootstrap` runs only from the requested Project default checkout. It can perform
only the capture, branch, worktree, claim, and creation-event sequence defined here.
It does not accept an arbitrary Concord operation and cannot grant ordinary mutation
authority to the default checkout.

The default checkout stays on its default branch. Implementation writes occur only in
the claimed linked worktree.

### D4. Launch authority binds to the prepared worktree and agent

`session-prepare` refuses a directory other than the active claimed worktree. It checks
the installed lane definitions, asserts the orchestrator identity, and obtains the
core-derived session packet before launch.

The host launches the named agent with the worktree as both process directory and host
directory. It exports the resulting session and refuses success unless the read-back
agent identity matches the prepared agent.

### D5. Capability does not activate Concord self-hosting

This decision does not claim replacement readiness and does not amend CD-0010.
Concord's own planned work, review, and merge evidence remain GitHub-native. Using
`concord_work_start` to coordinate Concord development requires both a proven
replacement-readiness claim and a later accepted decision that changes development
authority.

## Consequences

An operator can start or replay managed work with one host action. Exact replay reuses
the work identity, canonical branch, directory, claim, and creation event, while each
successful launch obtains fresh session continuity evidence.

An interrupted external Git step can leave native state present. The durable operation
state makes that state attributable and recoverable, so replay completes the same
operation rather than creating a second branch or worktree.

The host tool is shipped and registered with the adapter, but it does not enter
`concord invoke` or weaken the ordinary authority boundary.

## Rejected alternatives

**Permit capture from the default checkout.** Rejected because that change grants a
general mutation path where only one bootstrap sequence needs authority.

**Start the child in the default checkout and instruct it to move.** Rejected because
the host authority envelope binds at session startup and instructions cannot change
that structural identity.

**Require a manual worktree command and a fresh session.** Rejected because it leaves
the normal workflow non-atomic and prevents exact replay from one request.

**Treat the generic mechanism as replacement readiness.** Rejected because CD-0010
requires a separate evidence claim and authority decision.

## Verification

`TestWorkBootstrapExactReplayAndPendingRecovery` proves durable recovery and exact
replay. `TestWorkBootstrapConcurrentExactReplayHasOneNativeResult` proves one native
result under concurrent requests. `TestWorkBootstrapRefusesNonDefaultMainCheckoutBeforeJournal`
proves the default-branch scope. `TestWorkBootstrapRefusesPlantedCanonicalWorktreeWithCommits`
proves that a fresh operation does not adopt unattributed native state.

The adapter test `work_start integrates with a real Concord binary and a fake OpenCode
child` uses a real Git repository and the shipped adapter path. The agent contract
check proves that the host publishes and registers the generated tool schema.
