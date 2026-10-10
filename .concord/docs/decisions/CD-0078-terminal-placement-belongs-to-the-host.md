# CD-0078: Terminal placement belongs to the host

- **Status:** Accepted
- **Date:** 2026-08-26
- **Scope:** Where the session entry route runs, what launch does to the
  invoking terminal, and what a host launcher may forward to a session the
  route starts
- **Approval:** Operator raised the question on 2026-08-26 while deciding how much
  the launcher serves one operator's setup against general use, and selected this
  record as the next launcher move. The pull request is the public record.
- **Related:** C18 (§§5, 6, 7), CD-0014, CD-0021, CD-0031, CD-0008 (D1), CD-0219
- **Amended:** CD-0182 (2026-09-27) keeps D1 and D2 for every Concord binary
  and adds the host-registered session opener as the host's own route to a
  second coordinator session. CD-0219 (2026-10-09) retires the interactive
  launcher TUI; D1 through D3 bind the session entry route exactly as they
  bound the launcher.
- **Preserves:** C18 §6's exclusion of external-system execution, C18 §7's
  per-instance ambient Product, CD-0014's no-renderer rule, CD-0031's
  core-derived session boot

## Context

Concord ships no terminal. Its session entry route runs in whatever surface
the host already provides: a plain shell, a remote shell, a terminal emulator
tab, or a multiplexer pane.

The predecessor evidence cited by C14 is a host launcher built around Zellij. It
discovers repositories by walking a filesystem root, keeps a pin store, names the
tab, and then replaces itself with the target program. That tool answers a
different question than Concord answers. It asks which directory the operator
wants. Concord asks which Product holds the work.

The two questions meet at one moment: the host has placed a terminal, and
Concord must run inside it. Nothing in accepted law said which side owns that
placement, so this record says it.

## Decision

### D1. No Concord component knows about a terminal multiplexer

The session entry route never creates, names, splits, focuses, or destroys a tab,
pane, window, or session in Zellij, tmux, or any terminal emulator. Each
binary occupies the terminal it is given and returns it unchanged.

C18 §6 already excludes "any git, build, deploy, or external-system execution"
from every screen. Creating a tab is external-system execution. This decision
states the consequence directly so the question does not recur.

The host keeps what the host already does well: filesystem discovery, pins,
recency, tab titles, and placement. Concord keeps what only Concord can do:
which Products exist, which work is blocked, and what the session must know.

CD-0182 amends this rule at one point. The operator may register one
session-opener argv template in the host OpenCode config, and the adapter
executes that registration to open a second repository's coordinator
session. The template is the operator's own data, so no Concord binary gains
multiplexer knowledge, and this rule keeps binding every Concord binary and
command.

### D2. Launch runs the session in the terminal it was given

Launch runs the `concord session` bootstrap in the terminal it was given and
returns that terminal when the session exits. There is no navigation position
to hold: the handoff carries identity only, which is what C18 §5 means when
it calls launch a leaf rather than a screen transition.

One entry invocation therefore carries one session at a time. An operator who
wants two Products open at once runs two invocations. C18 §7 already allows
this: ambient Product is scoped per invocation, and two invocations may hold
two different Products.

Concord does not background the session, and it does not open a second terminal
surface to hold it. Both would require the placement authority D1 declines.
CD-0182 amends the second-surface clause at the host boundary: the adapter
runs the operator's registered session opener to open a second coordinator
session, and the entry route itself still opens no surface.

### D3. Launch is one action, and attach is a label

C18 §6's action table describes launch as starting or attaching a session. That
row states an outcome the operator perceives. It does not promise two code paths.

The same section is explicit: the entry route offers one launch action whose
availability does not vary with workflow state, and a "resume" or "open" label is
display text derived from the read that selected the work, never a second
decision. A route that chose between starting and attaching would hold
its own derivation of workflow position, which `design-constraints.md` §14
forbids.

Continuity does not travel through a terminal session identifier. Under CD-0031
every session the entry route starts runs the `concord session` child, which
reads the canonical CD-0016 continuity projection and validates a versioned
packet before
OpenCode starts. Resumption is that packet.

A host launcher must therefore not forward an OpenCode session identifier into a
session the route starts. Doing so would start a session whose position came from
a terminal identifier rather than from Concord's projection, which is the
split-authority shape the predecessor postmortem names as a recurring root cause.
A host remains free to resume its own sessions by identifier outside the Concord
launch path.

## Consequences

- The released Linux amd64 binary depends on no multiplexer, and a host that
  provides none loses no entry-route capability.
- A host launcher adopts Concord by changing the program it runs. Its discovery,
  pins, and tab naming need no Concord awareness.
- Two Products at once costs the operator a second terminal surface, which the
  host already knows how to produce.
- `ResolveProject` answers "which Project owns this directory" through the agent
  envelope only, so a host that wants that answer has no operator-level read
  path. This record does not open one. It names the gap for a separate issue.

## Rejected alternatives

**Concord creates Zellij tabs.** Rejected because it binds a released product to
one operator's multiplexer, and because C18 §6 excludes external-system
execution from every screen.

**A pluggable terminal-placement strategy.** Rejected because it is an
abstraction with one real implementation on one machine. The host boundary
already separates the concerns with no Concord code at all. CD-0182 keeps
this rejection for Concord code: the session opener is one operator
registration in host configuration, not a strategy surface in Concord.

**Forward an OpenCode session identifier through launch.** Rejected because
CD-0031 derives continuity from the canonical projection at the moment of use. A
forwarded identifier would introduce a second source for workflow position.

**Leave the boundary implied.** Rejected because C18 and CD-0014 each exclude
the behavior for their own reasons, and neither states the rule an integrator
needs. The question reached the operator as an open design choice, which is
evidence that the implication was not enough.

## Verification

- `TestLauncherAndCommandCarryNoMultiplexerKnowledge`
  (`cmd/concord/host_boundary_test.go`) proves D1 structurally by scanning
  the command package for multiplexer identifiers.
- `TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap`
  (`cmd/concord/session_handoff_test.go`) proves D3 by asserting the
  session argument vector is exactly the Concord binary and `session`, with
  identity carried in the environment. No session identifier can be forwarded.
- `TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap`
  (`cmd/concord/session_handoff_test.go`) and
  `TestSessionBootPassesCorePacketToOpenCodeBeforeSessionStarts` (`cmd/concord`)
  remain the existing anchors for the CD-0031 handoff this record preserves.
- Launch runs the `concord session` bootstrap in the terminal it was given
  and returns that terminal when the session exits.
  `TestSessionLauncherFailsClosedWithoutRunningBinaryIdentity`
  (`cmd/concord/session_handoff_test.go`) proves the launch path fails closed
  when the binary cannot identify itself.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.
