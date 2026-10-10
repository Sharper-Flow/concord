# CD-0014: terminal launcher rendering stack

**Status:** Accepted
**Approval date:** 2026-08-10
**Approval:** Operator-approved architecture spike under GitHub issue #39
**Supersedes:** The rendering-dependency and query-scope sub-questions in C18

## Decision

Concord ships no interactive terminal renderer under
[`CD-0219`](./CD-0219-the-terminal-launcher-tui-is-retired.md).
Bubble Tea, Bubbles, and Lip Gloss are absent from its module graph.
The session entry route requires no rendering dependency.
No replacement rendering stack is authorized by this record.

## Evidence gate

The module graph must contain no Charm rendering dependency:

```text
go mod why -m charm.land/bubbletea/v2
go mod why -m charm.land/bubbles/v2
go mod why -m charm.land/lipgloss/v2
```

## Reopen and falsifiers

This record reopens only through CD-0219's replacement rule: a future
interactive terminal surface requires a new operator-approved decision with its
own dependency, license, and accessibility evidence before any rendering
dependency re-enters the module graph.
