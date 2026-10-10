# CD-0163: The launcher carries no terminal placement

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The launch clause of CD-0108 D1 and the launch action of the
  session entry route (`concord zl` and the `concord session` command it
  starts). No other CD-0108 decision changes.
- **Approval:** The operator approved the launcher objective that surfaced
  the conflict and this correcting record.
- **Related:** CD-0108, CD-0078, CD-0014, CD-0219, C18
- **Amended:** CD-0182 (2026-09-27) opens a second coordinator session
  through the host-registered session opener; the placement boundary of D1 is
  unchanged. CD-0219 (2026-10-09) retires the interactive launcher TUI; the
  boundary re-homes to the session entry route unchanged in substance.
- **Preserves:** The CD-0108 session entry route, the CD-0078 D1
  placement boundary, and the store-write-free entry boundary of CD-0108 D4
- **Supersedes:** The zellij-tab clause of CD-0108 D1: "launch opens a
  zellij tab on the work's Concord worktree"

## Context

CD-0078 D1, accepted 2026-08-26, forbids every Concord component from
knowing a terminal multiplexer. It names zellij tab creation as the exact
case and keeps placement with the host.

CD-0108 D1, accepted 2026-09-03, described the remade launch action as
opening a zellij tab on the work's Concord worktree. The remake record
reintroduced the placement the earlier boundary had removed, so the two
texts could not both govern the launcher.

The conflict was textual, not behavioral. The launch path owns no multiplexer
code. The entry route hands session identity to the session bootstrap, which
starts OpenCode in the terminal the operator gave the entry route, and the
command package carries a structural test that scans its source for
multiplexer names. This record repairs the text so the accepted decision
states what the accepted code does.

## Decision

### D1. The launch action starts a session, and the operator owns the terminal

The entry route starts the OpenCode session for the selected work inside the
work's Project, through Concord's session bootstrap. The entry hands the
session identity only.

The operator provides the terminal and places its window or tab. The entry
never creates, names, splits, focuses, or destroys a tab, pane, window, or
session, exactly as CD-0078 D1 states. CD-0108 D1 reads with this
correction, and no new authority is created here.

### D2. Resume and prompt pass-through keep their CD-0108 meaning

Resume reattaches a live session the host attests, and a passed prompt
becomes that work's directive. Both ride the same session start, so both
inherit the same placement boundary. The entry keeps its read-only store
boundary under CD-0108 D4.

## Consequences

- The zellij-tab wording of CD-0108 D1 no longer states Product law. The
  launch clause reads as this record states it.
- The command-source boundary test that scans for multiplexer names stays
  the structural proof of D1.
- A future route that reattaches a live session still places nothing.
  Reattachment targets a session the host already placed.
- CD-0182 adds one host-side route: the adapter runs the operator's
  registered session opener to start a second repository's coordinator
  session. The entry still creates, names, splits, focuses, and destroys
  nothing.

## Verification

- `go test ./cmd/concord/ -run TestSession` proves the session command
  starts the host in the resolved landing directory through the resolved
  host command, with no placement of its own.
- `go test ./cmd/concord/ -run TestProductOnlySessionRemainsIdentityOnly`
  proves the session carries identity only and derives no workflow position.
- `go test ./cmd/concord/ -run TestLauncherAndCommandCarryNoMultiplexerKnowledge`
  proves D1 structurally through the command-source scan.
- `go test ./cmd/concord/ -run TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap`
  proves the fixed session argument and identity-only environment.
