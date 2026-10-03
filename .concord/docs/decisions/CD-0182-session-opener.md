# CD-0182: The session opener is host placement

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** How a work item reaches a second repository's coordinator
  session; the session opener, the explicit Project selector for the
  launcher, the `cross_repository_claim` remedy, the installer's recognition
  of the plugin tuple entry, the coordinator instructions that name the
  route, and the bounded Project-session handoff its sessions record and
  consume
- **Amends:** CD-0078 D1 and D2 and its rejected pluggable-placement
  alternative, CD-0163, CD-0176 D2
- **Related:** CD-0178, CD-0093, CD-0098, CD-0176, CD-0163
- **Preserves:** CD-0078 D1's boundary for every Concord binary, CD-0093 D2's
  one-directory binding and D3's fail-closed canonical path, CD-0176 D1's
  worktree landing, and CD-0178 D2's one coordinator session per repository
- **Approval:** The operator approved the seven-outcome contract in session
  on 2026-09-27. The pull request is the public record.

## Context

CD-0178 D2 gives a work item that spans two repositories one coordinator
session in each repository. Nothing in the product could open the second
session. A `worktree_claim` for a Project in another repository refuses with
`cross_repository_claim`, and its remedy named no reachable route. The host
coordinator definitions still told agents to claim a second worktree and
move, which is the move the host refuses.

Two accepted boundaries stood in the way of a product answer. CD-0078 D1
forbids every Concord component from knowing a terminal multiplexer, and its
record rejected a pluggable terminal-placement strategy as an abstraction
with one real implementation on one machine. CD-0163 restates that boundary
for the launcher.

CD-0176 D2 limited the worktree landing to the work's primary Project. A
session could not start in a member Project that lives in another
repository, even when the store held an active worktree for it. The gap was
complete: no launch into a secondary Project, no opener for the second
session, and a refusal remedy that named nothing an agent could call.

## Decision

### D1. The session opener is one host registration

The operator may register one session-opener argv template in the options of
the Concord plugin tuple entry in the OpenCode config. The host passes the
tuple options to the plugin factory, and the adapter reads the template
there. Concord stores nothing outside that entry and ships no opener.

The template is a JSON array of strings with the placeholders `{directory}`,
`{title}`, and `{command}`. `{command}` must be one whole element and must
appear once; the adapter splices the core launch argv into it as separate
elements. Unknown placeholders refuse, and no shell runs at any point, so a
title or a path cannot inject into the opener argv.

Registering the opener is the operator's standing consent for coordinators
to open sessions and to spend model quota in the opened session. Concord's
binaries and shipped coordinator instructions still name no terminal
multiplexer: CD-0078 D1 keeps binding every Concord binary, and the opener
stays data the operator chose.

### D2. The explicit Project selector lands a session in a member Project

`concord zl` gains `--project <project_id>`. The named Project must be a
member of the work; the store read refuses a non-member with the membership
remedy. The session lands in that Project's active worktree when one is
usable on this machine, else in that Project's canonical path. CD-0093 D3's
fail-closed rule stands: a canonical path that does not resolve refuses the
launch. Without the selector, the primary landing of CD-0093 and CD-0176 is
unchanged.

The fixed prompt of a Project-selected session tells the new coordinator to
resume the work item there by calling `concord_work_start` with the
`work_id` and the `project_id`. The prompt carries the session boot packet
as any work-selected session does.

### D3. The refusal remedy names one route, and the adapter carries it

The `cross_repository_claim` refusal names `concord_work_start` with the
`work_id` and the `project_id` of the target Project. The adapter's
`concord_work_start` resume accepts the optional `project_id`.

A selected Project whose canonical path is the calling Project's keeps the
existing claim-and-move route. A selected Project whose canonical path
differs never reaches the host move. With the opener registered, the adapter
substitutes `{directory}`, `{title}`, and `{command}` and runs the opener
without a shell, then reports the opener's exit status and argv and does not
claim the new session is running. With no opener, it returns the exact
launch command and directory for the operator. An invalid opener refuses
with the invalid field named and still returns the command.

### D4. The installer keeps options, and the instructions name the route

`plan_plugin_entry` and `remove_plugin_entry` recognize the tuple form of
the Concord entry and keep its options, so an upgrade never adds a duplicate
bare entry and an uninstall removes the tuple whole. The example coordinator
definitions name the route of D3 and carry no claim-and-move text.

### D5. The Project-selected session carries a bounded addressed handoff

A Project-selected session receives one bounded repository job and one
explicit receiving Project on the same shared work item and active contract.
The source session records that job as a typed handoff on the work item.
The record carries the changes, the verification results, the immutable
artifact references, the open blockers, and the exact next action. The
source session verifies its changed artifacts against the claimed worktree
before the record commits: a dirty or untracked worktree refuses, and the
core never commits, stashes, or hides files.

The receiving session consumes the recorded handoff before it runs any
managed external effect. The consume binds the record to the authenticated
receiving session, its target Project, and the current contract. A missing,
wrong-target, foreign, or stale handoff refuses. Managed-execution
admission fails closed while an unconsumed or stale handoff stands.

`READY_TO_CLOSE_OR_REPLACE` is a derived read, never a written state. It
reports ready only when the session recorded an addressed handoff, its
changed artifacts verify preserved, its own open workers and nonterminal
actions stopped, and its verified vacate landing released its occupancy.
Unknown worker attribution blocks readiness. Retirement never completes or
cancels the shared work, removes a worktree, or terminates a process. The
continuity snapshot carries the newest unconsumed handoff, so a
Project-selected boot names the bounded job without operator copying.

## Alternatives considered

- Read the opener from a Concord core config file. Rejected: it puts host
  placement in the Go binary, which CD-0078 D1 refuses, and it adds a second
  config surface.
- Hard-code one multiplexer in the adapter. Rejected: it binds the released
  product to one machine's tool.
- Accept a shell string template. Rejected: quoting and injection risk from
  titles and paths; argv substitution keeps validation structural.
- Refuse the install until an opener is registered. Rejected per the
  operator's 2026-09-27 decision: unattended installs, upgrades,
  `scripts/test-installer.py`, and hosts without a multiplexer keep working,
  and the no-opener route returns the exact command.
- Add a new transition operation that opens sessions. Rejected: it adds
  manifest surface for the same intent, and `concord_work_start` is already
  the one entry for working an item in a place.
- Open the session inside `worktree_claim` on
  `cross_repository_claim`. Rejected: it hides the quota spend inside a
  claim call.
- Launch with `CONCORD_SELECTED_PROJECT_PATH` plus work identity. Rejected:
  that variable deliberately refuses Concord identity, and a path cannot
  carry the work-bound landing rules.

## Consequences

Cross-repository work costs the operator one registration per host. Without
it, a coordinator that reaches a second repository hands back one exact
command, and the operator opens the session. With it, the opened session
starts and resumes on its own, and the first session must stop driving the
other repository.

The opener result is evidence of a run, not proof of a session: the answer
reports an exit status and argv, and the opened session proves itself by
resuming the work item. A non-member `project_id` wastes an opener run and
fails at the landing read, so the membership record comes first.

Two Projects in one repository keep the within-repository move, and this
record adds no second landing rule inside one repository. The adapter's
built-in tab and pane renames stay as they are; moving them under a host
registration is a named follow-up this record does not perform.

Session-to-session messages stay in work-033a2fb710eef89eb4b94e69, and the
tab-rename parse fault stays in work-ece2f503b41959e5a7bce78d. Neither moves
under this record.

## Verification

- `go test ./internal/store/ -run
  TestResolveSessionDirectoryForProjectSelectsAMemberProject` proves D2's
  read: the member gate, the selected Project's candidates, and the
  unchanged primary form.
- `go test ./cmd/concord/ -run TestSessionProjectSelection` and
  `go test ./cmd/concord/ -run TestZLProjectSelectorRefusals` prove D2's
  command surface: the selector reaches the resolver, the fixed prompt names
  the resume route, and the refusals are typed.
- `go test ./internal/agent/ -run
  TestWorktreeClaimRefusesCrossRepositoryBeforeCreation` proves D3's remedy
  text names `concord_work_start` with both identities.
- `go test ./internal/launcher/render/bubbletea/ -run
  TestSessionCommandPassesProjectSelectionThroughEnvironment` proves the
  selector travels the launcher handoff.
- `bun test adapter/opencode/` proves D3's adapter route: the opener
  substitution, the exit-status report, the no-opener command, the invalid
  opener refusal, and the same-repository route that keeps the move.
- `python3 scripts/test-installer.py` proves D4: the tuple entry survives an
  upgrade with its options and deregisters whole.
- `python3 scripts/check-primary-prompts.py` and `go test ./cmd/concord/ -run
  TestLauncherAndCommandCarryNoMultiplexerKnowledge` prove D1's boundary:
  the instructions name the route, and the binaries stay multiplexer-free.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-index.py`,
  `python3 scripts/generate-knowledge-index.py --check`,
  `python3 scripts/check-knowledge-closure.py`, and
  `python3 scripts/check-cd-allocation.py --no-fetch` pass.
