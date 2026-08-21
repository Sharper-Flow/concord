# CD-0049: Agent definitions are delivered per project, and Concord asserts agent identity itself

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** Where Concord-owned OpenCode agent definitions are placed on an
  operator's host; who is responsible for confirming that the required agent is
  the one running; what a session does when that confirmation fails; issues
  #253, #254, #257
- **Approval:** Operator accepted the drafted decision on 2026-08-21; the public
  record is [issue #257](https://github.com/Sharper-Flow/concord/issues/257)
- **Related:** CD-0016 (context continuity, typed agent identity, no
  `agent_definitions` table), CD-0017 (typed workers and model routing), CD-0031
  (launcher-started session boot packet), CD-0034 (host prompt provenance),
  CD-0043 (host-owned lane methodology)

## Context

CD-0017 D1 makes every worker lane a typed, versioned, digest-pinned
Concord-owned definition, on the stated premise that generic host agents drift
on policy, authority, evidence, and result shape at the delegation boundary.
The agent that performs the dispatching has no definition. CD-0016 anticipated
this, blocked restart dispatch until a typed agent registry existed, reserved
`typed_agent_type`, `typed_agent_version`, and `typed_agent_ruleset_digest` on
`workflow_context_boundaries`, and stated that Concord provides no
generic-agent fallback. CD-0017 read the requirement as covering sub-agents and
delivered a lane registry; the half CD-0016 depended on was never built.

Two questions blocked progress on both halves. Where does a Concord-owned agent
definition go on an operator's host, and who confirms that the definition
Concord installed is the one that actually ran.

Host behaviour was verified against an installed OpenCode build rather than
inferred from documentation. Three placements work: the global agent directory,
a project's `.opencode/agents/`, and an inline definition supplied through
`OPENCODE_CONFIG_CONTENT` at launch. No mechanism scopes a globally installed
agent to selected projects; the config root refuses unknown keys and has no
agent equivalent of `skills.paths`. Most consequentially, `opencode run --agent`
with an unknown name does not fail — it warns, falls back to the operator's
default agent, and exits zero.

## Decision

### D1. Lane agent definitions are delivered per project

The installer places generated lane definitions into a project's
`.opencode/agents/`. Global placement is refused because it reaches every
project on the operator's machine with no available scoping. Environment-supplied
placement is refused for lane definitions because `host_provenance` under CD-0034
is computed by hashing agent definitions read from the filesystem, and a
definition carried in the environment presents no file to hash.

The installer has no project concept today. Acquiring one is the cost of this
decision and is owned by issue #253.

### D2. Concord asserts agent identity before launch

Concord enumerates the agents available to a session and asserts that the
required identity is present before starting OpenCode. Identity assertion is
never delegated to the host: passing `--agent` and relying on the outcome is
unavailable as a design, because the host's failure mode for an unknown agent is
a silent substitution with a success exit code.

A missing or unexpected identity is a typed failure naming what was required and
what was found.

### D3. The orchestrator is constrained, not owned

Concord records the orchestrator identity a session requires and verifies it.
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

## Invariants

1. A Concord-owned lane definition reaches a host only through a project's
   `.opencode/agents/`.
2. No Concord code path treats the presence of an `--agent` argument as evidence
   that the named agent ran.
3. A launcher-started session either records the typed agent identity it
   asserted, or refuses with a typed failure naming the required and observed
   identity.
4. Concord holds no durable definition of an orchestrator persona.

## Consequences

Issue #253 gains a defined delivery target, and its substantive remaining work
is the installer's missing project concept rather than an allowlist edit.

Issue #254 gains a shape that survives CD-0016's exclusion of an
`agent_definitions` table.

CD-0016's clean-restart boundary becomes reachable. The assertion required by D2
is what populates the reserved `typed_agent_*` columns that have been unwritten
since that decision was accepted.

CD-0031 describes a launcher-started session bootstrap with no identity
assertion step, and requires amendment to carry one.

This decision does not settle how lane methodology reaches a worker. CD-0043 D2
names a channel that records paths without injecting them, and an inline agent
definition supplied at launch can carry a prompt, which is a candidate channel
D2 currently lacks. Whether methodology should travel that way is deliberately
left open, because deciding delivery and methodology under one record would
conflate a placement question with a content question.
