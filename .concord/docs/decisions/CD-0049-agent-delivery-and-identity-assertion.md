# CD-0049: Agent definitions are delivered per project, and Concord asserts agent identity itself

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** Where Concord-owned OpenCode agent definitions are placed on an
  operator's host; who is responsible for confirming that the required agent is
  the one running; what a session does when that confirmation fails; issues
  #253, #254, #257
- **Approval:** Operator accepted the drafted decision on 2026-08-21; the public
  record is [issue #257](https://github.com/Sharper-Flow/concord/issues/257)
- **Related:** CD-0016 (context continuity and typed session identity),
  CD-0031 (core-derived session context), CD-0061 (orchestrator identity),
  CD-0154 (host-owned numbered agents)

## Context

Managed session preparation needs an explicit agent identity. A host fallback
to a different agent cannot establish the identity Concord requires.

Host behaviour was verified against an installed OpenCode build rather than
inferred from documentation. Three placements work: the global agent directory,
a project's `.opencode/agents/`, and an inline definition supplied through
`OPENCODE_CONFIG_CONTENT` at launch. No mechanism scopes a globally installed
agent to selected projects; the config root refuses unknown keys and has no
agent equivalent of `skills.paths`. Most consequentially, `opencode run --agent`
with an unknown name does not fail — it warns, falls back to the operator's
default agent, and exits zero.

## Decision

### D2. Concord asserts agent identity before managed action

Concord enumerates the agents available to a session and asserts that the
required identity is present before managed action. session-prepare performs
the assertion against the calling host session's registry in the claimed
worktree. Identity assertion is never delegated to the host: passing
`--agent` and relying on the outcome is unavailable as a design, because the
host's failure mode for an unknown agent is a silent substitution with a
success exit code.

A missing or unexpected identity is a typed failure naming what was required and
what was found.

### D3. The orchestrator is constrained, not owned

Concord verifies the orchestrator identity a session requires without recording
an identity assertion (CD-0061 D4).
Concord does not author, ship, or version an orchestrator persona. CD-0016
excludes an `agent_definitions` table, so a Concord-owned orchestrator
definition would have nowhere to record what it pinned. Ownership stays at the
evidence boundary, which is the same split
[`capability-placement.md`](../capability-placement.md) §3 draws between
coordinating a responsibility and reimplementing it.

### D4. A session refuses to start when required identity is absent

There is no degraded start. CD-0016 admits none, and a degraded start would
reintroduce the generic-agent fallback that decision disclaims. The cost is that
an operator on an incompletely installed host cannot begin work until the
install is repaired; that cost is accepted here rather than discovered at the
point of failure.

### D5. The trusted client policy bounds the agent a caller may present

The agent gains the standing the Product and the Project already have. A
trusted client policy names the agents it may present, and authorization
refuses any other. The scope is fail-closed: a client that names no agent
authorizes none, because a default-open scope on an identity field is
indistinguishable from having no scope at all.

The reference bounded here is the agent name the host presents, which is not
necessarily its definition file name. Binding a definition digest instead is
refused by CD-0061 D4. Authenticating the invocation itself remains
unsolved and is not settled here.

## Invariants

2. No Concord code path treats the presence of an `--agent` argument as evidence
   that the named agent ran.
3. A managed session either verifies the agent identity during session preparation,
   or refuses with a typed failure naming the required and observed
   identity.
4. Concord holds no durable definition of an orchestrator persona.
5. An authorized invocation carries an agent reference its trusted client policy
   names.

## Consequences

Work-start preparation verifies the selected agent before managed action. It
writes no identity assertion or workflow continuity boundary (CD-0061 D2, D4).

D5 gives every client registered before it an empty agent scope, which
authorizes nothing. Each such client must be re-registered with the agents it
presents. That interruption is the cost of the fail-closed default, and it is
accepted here rather than avoided by trusting existing rows.

Native Task identity is not attested by this managed-session identity check
([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host)).
