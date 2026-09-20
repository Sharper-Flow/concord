# CD-0163: The launcher carries no terminal placement

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The launch clause of CD-0108 D1 and the launch action of the
  launcher. No other CD-0108 decision changes.
- **Approval:** The operator approved the launcher objective that surfaced
  the conflict and this correcting record.
- **Related:** CD-0108, CD-0078, CD-0014, C18
- **Preserves:** The CD-0108 remake of the launcher, the CD-0078 D1
  placement boundary, and the store-write-free launcher of CD-0108 D4
- **Supersedes:** The zellij-tab clause of CD-0108 D1: "launch opens a
  zellij tab on the work's Concord worktree"

## Context

CD-0078 D1, accepted 2026-08-26, forbids every Concord component from
knowing a terminal multiplexer. It names zellij tab creation as the exact
case and keeps placement with the host.

CD-0108 D1, accepted 2026-09-03, describes the remade launch action as
opening a zellij tab on the work's Concord worktree. The remake record
reintroduced the placement the earlier boundary had removed, so the two
texts cannot both govern the launcher.

The conflict is textual, not behavioral. The launcher implementation owns
no multiplexer code. The launch action hands session identity to the
session bootstrap, which starts OpenCode in the terminal the operator gave
the launcher, and the launcher package carries a structural test that
scans its source for multiplexer names. This record repairs the text so
the accepted decision states what the accepted code does.

## Decision

### D1. The launch action starts a session, and the operator owns the terminal

The launcher's launch action starts the OpenCode session for the selected
work inside the work's Project, through Concord's session bootstrap. The
launcher hands the session identity only.

The operator provides the terminal and places its window or tab. The
launcher never creates, names, splits, focuses, or destroys a tab, pane,
window, or session, exactly as CD-0078 D1 states. CD-0108 D1 reads with
this correction, and no new authority is created here.

### D2. Resume and prompt pass-through keep their CD-0108 meaning

Resume reattaches a live session the host attests, and a passed prompt
becomes that work's directive. Both ride the same session start, so both
inherit the same placement boundary. The launcher keeps its read-only
store boundary under CD-0108 D4.

## Consequences

- The zellij-tab wording of CD-0108 D1 no longer states Product law. The
  launch clause reads as this record states it.
- The launcher boundary test that scans its source for multiplexer names
  stays the structural proof of D1.
- A future launcher that reattaches a live session still places nothing.
  Reattachment targets a session the host already placed.

## Verification

- `go test ./internal/launcher/ -run TestLauncherAndCommandCarryNoMultiplexerKnowledge`
  proves no launcher or command source file names a multiplexer.
- `go test ./cmd/concord/ -run TestBareConcord` proves bare concord starts
  the launcher at an interactive terminal and refuses with usage and exit 2
  without one.
- `go test ./internal/launcher/render/bubbletea/ -run TestNewBacklogResolvesIssueKeyOrDegradesToProjects`
  proves the launch handoff carries identity only and degrades to the
  Project select without launching.
- `go test ./internal/launcher/storeport/ -run TestLauncherPortReadsPerformNoDurableWrite`
  proves the launcher reads leave durable store state untouched.
