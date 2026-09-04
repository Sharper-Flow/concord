# Terminal launcher replacement — accepted contract

**Status:** Accepted under [`CD-0108`](./decisions/CD-0108-the-launcher-is-the-zlauncher-replacement.md).
**Supersedes:** the terminal launcher contract (C18,
[`terminal-launcher-contract.md`](./terminal-launcher-contract.md)) in full.
**Implementation status:** not started; tracked by
[issue #803](https://github.com/Sharper-Flow/concord/issues/803).

## Context

CD-0108 records the operator direction of 2026-09-03: delete the current
launcher and remake it as the replacement for zellij-project-launcher
(ZLauncher), the predecessor session bootstrap layer. This contract is the
successor specification that CD-0108 names. It replaces C18, which defined
the read-only, status-only launcher the operator rejected as too far off
expectation.

The binding inputs are CD-0108, the C14 Product-row contract, the C17
coordination view, CD-0014 for rendering, and R1's recorded supersession in
[`clarifications.md`](./clarifications.md).

What ZLauncher does today frames the core loop. It scans configured roots
plus pinned workspaces, offers a fuzzy pick with a preview pane, opens a
zellij tab for the selection, starts OpenCode in that tab, resumes sessions,
and passes a prompt through to the session. Status context rides in the
preview.

## Contract

### 1. The launcher's job

The launcher is the operator's daily entry surface: browse Concord state,
then enter work. Its core loop has two halves.

Browse: the launcher shows Products, their work, and live sessions, read
from the store.

Enter: from a selected work item, the launcher opens a zellij tab on the
work's Concord worktree and starts the OpenCode session through Concord's
session bootstrap. Resume reattaches a live session bound to that work. A
passed prompt becomes that work's directive.

The launcher performs no durable store write. It reads the store and
executes host processes: zellij tab creation and the OpenCode start. Work
capture reached through a passed prompt happens inside the session through
the typed mutation surface. This preserves C18's read-only construction and
ends its status-only scope, as CD-0108 D4 states.

### 2. Screens and navigation

The launcher renders dense panes in the lazygit tradition: a Product and
work list, a detail pane for the selection, a preview or status pane, and a
status or keymap bar. Everything the operator needs on one screen.

Navigation uses arrow keys to move, Enter to act, and Tab to cycle pane
focus. No vim chords. Type-to-filter narrows the current list.

### 3. The action surface

- Enter on a work item with no live session: open a zellij tab on the
  work's claimed worktree, start OpenCode through Concord's session
  bootstrap, and pass any queued prompt as the work directive.
- Enter on a work item with live session or sessions: offer reattach; one
  session reattaches directly, several open a picker.
- `--resume-last`: resume the most recent workspace in the current tab,
  matching ZLauncher's verb.
- `zl` command forwarding: `zl <work> [-- <prompt>...]` starts or resumes
  the named work without the interactive UI.
- `--list`: print the candidate list as JSON for scripts. No other
  non-interactive verb ships in the first build.

### 4. Candidates and ordering

- Candidates come from the store: Products, projects, work items, claimed
  worktrees, and live sessions.
- Configurable scan roots admit projects Concord does not know yet; a
  scanned project starts a plain OpenCode session with no work identity.
- Pins: pinned paths stay at the top of the list; Ctrl-P pins and Ctrl-U
  unpins the highlighted path.
- Ordering is most-recently-used first, then stored rank.
- Two-stage pick: selecting a Product with work narrows to its work items
  and worktrees; selecting a plain project launches directly.

### 5. Status and preview

- The preview pane renders the selection's context: work state, worktree
  status, and Domain context per Product.
- Work columns show lifecycle, priority, and urgency.
- Blocked-work indicators mark work whose blocking relations are recorded.
- A session inventory lists live OpenCode sessions per work with reattach.
- Vision live status and lgrep status appear as preview content, read
  through their local probes, never as launcher dependencies: both failing
  degrades the pane, not the launcher.
- An in-place refresh key re-reads the store and probes.

### 6. Presentation

- Zellij tab identity: launched tabs carry the work or project badge and
  color.
- Abbreviations: deterministic short labels for tab and row identity.
- Time-ago stamps on rows, with a timezone override.
- ANSI color control: a no-color mode, honoring `NO_COLOR`.
- An icon-badge option, off by default.

### 7. Refresh and read bounds

The launcher reads the store on start, on refresh key, and on navigation
that needs uncached detail. It polls nothing on a timer, matching C18's
no-poll rule. Reads are Product-scoped and bounded; a read that exceeds its
bound fails the pane, not the process.

### 8. Failure and first run

- No store: the launcher shows the failure and the candidates from scan
  roots only, and marks work features unavailable.
- First run with no store content: same behavior, plus a hint that
  Concord's store is empty.
- A failed Vision or lgrep probe renders an unavailable pane section.
- A failed launch — zellij missing, worktree missing, session bootstrap
  refusal — reports the refusal and changes no state.

### 9. Rendering

Bubble Tea v2 per CD-0014, behind the isolated adapter. The remake
replaces C18's three-screen model; it keeps the dependency decision.

### 10. Anti-requirements

- No launch registry. The store owns worktree claims and session records.
- No in-launcher work capture or any durable store write.
- No knowledge or observation search.
- No OpenCode database or provider health panels, stack hygiene, or
  doctor; these are deferred, not ported.
- No typo-tolerant matching in the first build.
- No mutation of workflow state. The launcher never records a transition,
  verdict, or completion.

### 11. Implementation boundary

- The current `internal/launcher` is deleted; the replacement is new code
  under `internal/launcher` with a new model, not a port of the old
  screens.
- The read path uses the existing bounded store queries where they fit and
  adds launcher-specific queries beside them.
- The launch path calls Concord's session bootstrap through the CLI, never
  a hand-built OpenCode argv.

## Acceptance criteria

```gherkin
Scenario: browse-to-launch
  Given the store holds Product "concord" with work item "W" in progress
  And work item "W" has a claimed worktree
  When the operator selects "W" and presses Enter
  Then a zellij tab opens on W's worktree
  And an OpenCode session starts through Concord's session bootstrap
  And the session is bound to work item "W"

Scenario: resume-reattach
  Given work item "W" has one live session
  When the operator selects "W" and presses Enter
  Then the launcher reattaches the live session in a new zellij tab

Scenario: prompt-passthrough
  Given the operator passes a prompt with the launch
  When the session starts
  Then the prompt becomes that work's directive in the session

Scenario: zl-forwarding
  When the operator runs "zl <work> -- <prompt>"
  Then the launcher starts or resumes the named work without the UI
  And the prompt becomes the work directive

Scenario: resume-last
  Given a workspace was launched in the current tab
  When the operator runs the resume-last verb
  Then the most recent workspace resumes in that tab

Scenario: store-write-free
  When any launcher action completes
  Then the store holds no write authored by the launcher

Scenario: standard-keys
  When the launcher renders
  Then arrow keys move the selection
  And Enter acts on the selection
  And Tab cycles pane focus

Scenario: scan-root-project
  Given a configured scan root holds a project unknown to Concord
  When the operator selects it and presses Enter
  Then a plain OpenCode session starts in that project with no work identity

Scenario: pin-keys
  When the operator presses Ctrl-P then Ctrl-U on a highlighted path
  Then the path is pinned then unpinned, and the list reflects it

Scenario: refresh-in-place
  When the operator presses the refresh key
  Then the store is re-read and the panes update without a restart

Scenario: degraded-probes
  Given Vision and lgrep probes fail
  When the launcher renders the preview
  Then the preview shows both as unavailable and the launcher stays usable

Scenario: list-json
  When the operator runs the launcher with --list
  Then the candidate list prints as JSON and exits

Scenario: zlauncher-retirement
  Given every criterion above holds on the operator's real store
  When the operator deletes zellij-project-launcher
  Then the launcher remains the operator's only daily entry surface
```

## Verification

- A store-backed corpus test drives browse, launch, resume, and prompt
  pass-through against a fixture store.
- A degradation test fails both probes and asserts the launcher stays
  usable.
- The write-free boundary is asserted by a test that runs the full action
  surface against a store snapshot and compares content hashes.
- The ZLauncher retirement condition is operator-verified on the real
  store and recorded on issue #803 before it closes.
