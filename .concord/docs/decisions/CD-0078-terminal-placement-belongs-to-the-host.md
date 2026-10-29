# CD-0078: Terminal placement belongs to the host

- **Status:** Accepted
- **Date:** 2026-08-26
- **Scope:** Where operator sessions start and resume, and which component
  owns terminal placement and naming
- **Approval:** Operator raised the question on 2026-08-26 while deciding how much
  the launcher serves one operator's setup against general use, and selected this
  record as the next launcher move. The pull request is the public record.
- **Related:** C18 (§§5, 6, 7), CD-0014, CD-0021, CD-0031, CD-0008 (D1), CD-0219
- **Approval:** Host-owned session start and resume: Concord (CON) issue
  CON-910 (2026-10-13), under the Snowball coordinator's operator-delegated
  approval.
- **Preserves:** C18 §6's exclusion of external-system execution, C18 §7's
  per-instance ambient Product, CD-0014's no-renderer rule, CD-0031's
  core-derived session boot

## Context

Concord ships no terminal and starts no operator session. An operator session
runs in whatever surface the host provides, and the host starts and resumes it.

The predecessor evidence cited by C14 is a host launcher built around a
terminal multiplexer. It discovers repositories by walking a filesystem root,
keeps a pin store, names the tab, and then replaces itself with the target
program. That tool answers a different question than Concord answers. It asks
which directory the operator wants. Concord asks which Product holds the work.

The two questions meet at one moment: the host has placed a terminal and
started a session, and Concord must resolve the work inside it. Nothing in
accepted law said which side owns placement and session start, so this record
says it.

## Decision

### D1. No Concord component knows about a terminal multiplexer

No Concord component creates, names, splits, focuses, or destroys a tab,
pane, window, or session in a terminal multiplexer or terminal emulator. Each
Concord process occupies the terminal it is given and returns it unchanged.

C18 §6 already excludes "any git, build, deploy, or external-system execution"
from every screen. Creating a tab is external-system execution. This decision
states the consequence directly so the question does not recur.

The host keeps what the host already does well: filesystem discovery, pins,
recency, tab titles, placement, and session start and resume. Concord keeps
what only Concord can do: which Products exist, which work is blocked, and
what the session must know.

### D2. Concord starts and resumes no operator session

The host starts and resumes operator sessions. Concord neither forks an
operator session nor keeps an entry route beside that start. A host-started
session enters managed work through `concord_work_start`, which moves the
calling session and starts no process (CD-0098).

Concord does not background a session, and it does not open a second terminal
surface to hold one. Both would require the placement authority D1 declines.
Concord never infers launch success from a process exit status or from a
terminal session identifier.

### D3. Continuity never travels through a terminal session identifier

Resumption of workflow position is not a terminal fact. Under CD-0031 the
core derives the bounded context packet from the canonical CD-0016 continuity
projection; work-start preparation renders and validates it, and the CD-0090
per-turn hook re-asserts it after context boundaries. Managed position comes
from that projection, never from a terminal identifier.

A host remains free to resume its own sessions by identifier outside Concord.
Deriving workflow position from that identifier would be the split-authority
shape the predecessor postmortem names as a recurring root cause.

## Consequences

- The released Linux amd64 binary depends on no multiplexer and starts no
  session, and a host that provides none loses no capability.
- A host adopts Concord by starting the session in the surface it chooses.
  Its discovery, pins, and tab naming need no Concord awareness.
- Two Products at once costs the operator a second terminal surface, which the
  host already knows how to produce.
- A host resolves directory ownership through the read-only `project-resolve`
  CLI (CD-0079 D1).

## Rejected alternatives

**Concord creates multiplexer tabs.** Rejected because it binds a released
product to one operator's multiplexer, and because C18 §6 excludes
external-system execution from every screen.

**A pluggable terminal-placement strategy.** Rejected because it is an
abstraction with one real implementation on one machine. The host boundary
already separates the concerns with no Concord code at all.

**Derive workflow position from a terminal session identifier.** Rejected
because CD-0031 derives continuity from the canonical projection at the
moment of use. A derived identifier would introduce a second source for
workflow position.

**Keep a Concord entry route beside the host start.** Rejected because the
host owns session start and resume, and a second launch entry keeps that
responsibility in two owners.

**Leave the boundary implied.** Rejected because C18 and CD-0014 each exclude
the behavior for their own reasons, and neither states the rule an integrator
needs. The question reached the operator as an open design choice, which is
evidence that the implication was not enough.

## Verification

- `go test ./cmd/concord/ -run
  'TestRemovedLaunchEntriesRefuseWithoutExecuting|TestRemovedLaunchArgvZeroDoesNotForward|TestRemovedLauncherVerbPrintsUsageWithoutOpeningAuthority'`
  proves the removed entry verbs, argv[0] forwarding, and launcher verb reach
  no launch: the refusals exit without executing a child, argv[0] does not
  forward, and bare invocation prints usage without opening authority. The
  removed operator repair command is one of the refused entries in
  `TestRemovedLaunchEntriesRefuseWithoutExecuting`, which asserts exit 2, no
  stdin read, and no authority storage for it.
- `go test ./cmd/concord/ -run TestLauncherAndCommandCarryNoMultiplexerKnowledge`
  proves D1 structurally by scanning the command package for multiplexer
  identifiers.
- `bun test adapter/opencode/concord.test.ts -t "a successful work start
  forks no terminal command"` proves the work-start path forks no terminal
  command.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`, and
  `python3 scripts/check-knowledge-index.py` pass.
