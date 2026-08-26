# CD-0078: Terminal placement belongs to the host

- **Status:** Accepted
- **Date:** 2026-08-26
- **Scope:** Where the terminal launcher runs, what launch does to the invoking
  terminal, and what a host launcher may forward to a launcher-started session
- **Approval:** Operator raised the question on 2026-08-26 while deciding how much
  the launcher serves one operator's setup against general use, and selected this
  record as the next launcher move. The pull request is the public record.
- **Related:** C18 (§§5, 6, 7), CD-0014, CD-0021, CD-0031, CD-0008 (D1)
- **Preserves:** C18 §6's exclusion of external-system execution, C18 §7's
  per-instance ambient Product, CD-0014's renderer isolation, CD-0031's
  core-derived session boot

## Context

Concord ships a terminal launcher. It does not ship a terminal. An operator
reaches the launcher through whatever surface their host already provides: a
plain shell, a remote shell, a terminal emulator tab, or a multiplexer pane.

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

The launcher never creates, names, splits, focuses, or destroys a tab, pane,
window, or session in Zellij, tmux, or any terminal emulator. It occupies the
terminal it is given and returns it unchanged.

C18 §6 already excludes "any git, build, deploy, or external-system execution"
from every screen, and CD-0014 §17 authorized no process-management surface.
Creating a tab is external-system execution. This decision states the consequence
directly so the question does not recur.

The host keeps what the host already does well: filesystem discovery, pins,
recency, tab titles, and placement. Concord keeps what only Concord can do:
which Products exist, which work is blocked, and what the session must know.

### D2. Launch suspends the launcher in the terminal it was given

Launch releases the terminal, runs the `concord session` bootstrap in that same
terminal, and restores the launcher when the session exits. The launcher holds
its navigation position across the handoff, which is what C18 §5 means when it
calls launch a leaf rather than a screen transition.

One launcher instance therefore carries one session at a time. An operator who
wants two Products open at once runs two instances. C18 §7 already allows this:
ambient Product is scoped per launcher instance, and two instances may hold two
different Products.

Concord does not background the session, and it does not open a second terminal
surface to hold it. Both would require the placement authority D1 declines.

### D3. Launch is one action, and attach is a label

C18 §6's action table describes launch as starting or attaching a session. That
row states an outcome the operator perceives. It does not promise two code paths.

The same section is explicit: the launcher offers one launch action whose
availability does not vary with workflow state, and a "resume" or "open" label is
display text derived from the read that already populated the screen, never a
second decision. A launcher that chose between starting and attaching would hold
its own derivation of workflow position, which `design-constraints.md` §14
forbids.

Continuity does not travel through a terminal session identifier. Under CD-0031
every launcher-started session runs the `concord session` child, which reads the
canonical CD-0016 continuity projection and validates a versioned packet before
OpenCode starts. Resumption is that packet.

A host launcher must therefore not forward an OpenCode session identifier into a
launcher-started session. Doing so would start a session whose position came from
a terminal identifier rather than from Concord's projection, which is the
split-authority shape the predecessor postmortem names as a recurring root cause.
A host remains free to resume its own sessions by identifier outside the Concord
launch path.

## Consequences

- The released Linux amd64 binary depends on no multiplexer, and a host that
  provides none loses no launcher capability.
- A host launcher adopts Concord by changing the program it runs. Its discovery,
  pins, and tab naming need no Concord awareness.
- Two Products at once costs the operator a second terminal surface, which the
  host already knows how to produce.
- `ResolveProject` answers "which Project owns this directory" through the agent
  envelope only, so a host that wants that answer has no operator-level read
  path. This record does not open one. It names the gap for a separate issue.
- CD-0014 §147 deferred assistive-technology validation to launcher
  implementation acceptance. That validation remains open and unaffected here.

## Rejected alternatives

**Concord creates Zellij tabs.** Rejected because it binds a released product to
one operator's multiplexer, and because C18 §6 excludes external-system
execution from every screen.

**A pluggable terminal-placement strategy.** Rejected because it is an
abstraction with one real implementation on one machine. The host boundary
already separates the concerns with no Concord code at all.

**Forward an OpenCode session identifier through launch.** Rejected because
CD-0031 derives continuity from the canonical projection at the moment of use. A
forwarded identifier would introduce a second source for workflow position.

**Leave the boundary implied.** Rejected because C18 and CD-0014 each exclude
the behavior for their own reasons, and neither states the rule an integrator
needs. The question reached the operator as an open design choice, which is
evidence that the implication was not enough.

## Verification

- `TestLauncherAndCommandCarryNoMultiplexerKnowledge`
  (`internal/launcher/host_boundary_test.go`) proves D1 structurally by scanning
  the launcher packages and the command package for multiplexer identifiers.
- `TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap`
  (`internal/launcher/render/bubbletea/model_test.go`) proves D3 by asserting the
  session argument vector is exactly the Concord binary and `session`, with
  identity carried in the environment. No session identifier can be forwarded.
- `TestLaunchHandoffIsIdentityOnlyAndS1CannotReachWork`
  (`internal/launcher/render/bubbletea/model_test.go`) and
  `TestSessionBootPassesCorePacketToOpenCodeBeforeSessionStarts` (`cmd/concord`)
  remain the existing anchors for the CD-0031 handoff this record preserves.
- D2's suspend-and-return model is the `tea.ExecProcess` call in
  `defaultSessionLauncher`. `TestSessionLauncherFailsClosedWithoutRunningBinaryIdentity`
  proves the launch path fails closed when the binary cannot identify itself.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, `python3 scripts/check-knowledge-index.py`,
  and `python3 scripts/check-cd-allocation.py` pass.
