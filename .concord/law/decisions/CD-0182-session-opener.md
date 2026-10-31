---
{
  "id": "CD-0182",
  "title": "Cross-repository coordinator sessions are host-placed",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0078"
    },
    {
      "kind": "refines",
      "target_id": "CD-0163"
    },
    {
      "kind": "refines",
      "target_id": "CD-0176"
    },
    {
      "kind": "refines",
      "target_id": "CD-0178"
    }
  ]
}
---
# CD-0182: Cross-repository coordinator sessions are host-placed

- **Status:** Accepted
- **Date:** 2026-09-27
- **Scope:** How a work item reaches a second repository's coordinator
  session; the `cross_repository_claim` remedy, the installer's recognition
  of the plugin tuple entry, the coordinator instructions that name the
  route, and the bounded Project-session handoff its sessions record and
  consume
- **Approval:** Concord (CON) issue CON-910 (2026-10-13), under the Snowball coordinator's operator-delegated approval.
- **Related:** CD-0178, CD-0093, CD-0098, CD-0176, CD-0163
- **Preserves:** CD-0078 D1's boundary for every Concord binary, CD-0093 D2's
  one-directory binding and D3's fail-closed resolution, CD-0176 D3's
  claimed-directory rules, and CD-0178 D2's repository-local execution with
  its refusal of a move into another repository
- **Approval:** The operator approved the seven-outcome contract in session
  on 2026-09-27, and that approval covers the original record. For the D5
  amendment, the operator delegated the decision to the coordinator
  session in chat on 2026-10-07. The coordinator accepted a consume bound
  to the work item and its receiving Project, usable by every verified
  landed session, with the stale-contract and cross-Project refusals and
  CD-0178 D2's repository-local execution unchanged. An exclusive session
  bind adds no safety beyond verified landing and the contract version,
  and it forced duplicate sessions. The pull request is the public
  record.

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
with one real implementation on one machine. CD-0163 restates that
boundary.

CD-0176 D2 limited the worktree landing to the work's primary Project. A
session could not start in a member Project that lives in another
repository, even when the store held an active worktree for it. The gap was
complete: no launch into a secondary Project, no opener for the second
session, and a refusal remedy that named nothing an agent could call.

## Decision

### D1. Retired: the session opener registration

The host starts the second coordinator session itself, over CON-771's typed
`session_placement` detail and the Project's registered canonical checkout.
A host-started session enters managed work through `concord_work_start`
(CD-0098). CD-0078 D1 keeps binding every Concord binary and command.

### D2. Retired: the explicit Project selector

A different Project in the same repository is reached through an
explicit `worktree_claim`; a different repository is reached through a
host-started session there, placed through CON-771's typed
`session_placement` detail. CD-0093 D3's fail-closed rule binds
session-prepare in the claimed worktree.

The membership record still comes first: a placement in a non-member
Project refuses at the store read, and the receiving session resumes the
work item through `concord_work_start` with the `work_id`.

### D3. The refusal remedy names one route, and the adapter carries it

The `cross_repository_claim` refusal names `concord_work_start` with the
`work_id` and the `project_id` of the target Project. The `project_id` is
the refusal and placement identity for the target Project, not a resume
input: the published resume is exactly the `work_id`. The refusal's typed
`session_placement` detail names the registered canonical checkout
directory, not the Git common directory. The host coordinator uses that
detail to reuse a receiving session or open one through its host session
tools. The receiving session resumes with the `work_id` alone: its calling
directory resolves the member Project, and the resume verifies its worktree
landing. Concord core neither chooses a terminal host nor opens a terminal
session.

### D4. The installer keeps options, and the instructions name the route

`plan_plugin_entry` and `remove_plugin_entry` recognize the tuple form of
the Concord entry and keep its options, so an upgrade never adds a duplicate
bare entry and an uninstall removes the tuple whole. The example coordinator
definitions name host placement and then the `concord_work_start` resume
with the `work_id`, and they carry no opener text and no claim-and-move
text.

### D5. The Project-selected session carries a bounded addressed handoff

A session placed in the receiving Project receives one bounded repository job.
The job names its receiving Project, shared work item, and active contract.
The source session records that job as a typed handoff on the work item.
The record carries the changes, the verification results, the immutable
artifact references, the open blockers, and the exact next action. The
source session verifies its changed artifacts against the claimed worktree
before the record commits: a dirty or untracked worktree refuses, and the
core never commits, stashes, or hides files.

The receiving session consumes the recorded handoff before it runs any
managed external effect. The consume binds the record to the work item
and its addressed receiving Project under the current contract: any
coordinator session with a verified landing in the receiving Project may
act under the frontier handoff's bind, and every such session reads
the frontier's bounded job.
A session without a verified landing, or in another Project, stays
refused. A missing, wrong-target, or stale handoff refuses.
Managed-execution admission fails closed while an unconsumed or stale
handoff stands, and a contract replacement refuses the stale bind equally
for the consuming session and a later session of the Project.

Opening a session in another Project serves only an actual edit in the
other repository, never a handoff-bind recovery. A placement or
handoff-bind refusal in the receiving Project is not that evidence. While
the current contract stands, a session records no fresh handoff and opens
no new session to recover from such a refusal. The refusing session replays
`concord_work_start` in its own Project, and its verified landing records
placement.

`READY_TO_CLOSE_OR_REPLACE` is a derived read, never a written state. It
reports ready only when the session recorded an addressed handoff, its
changed artifacts verify preserved, its nonterminal
actions stopped, and its verified vacate landing released its occupancy.
Retirement never completes or
cancels the shared work, removes a worktree, or terminates a process. The
continuity snapshot carries the newest unconsumed handoff.
The receiving Project's resume names the bounded job without operator copying.

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

Cross-repository work needs no Concord registration: the host starts the
receiving session, and CON-771's typed `session_placement` detail answers
where it goes. Once the receiving session is placed, the first session must
stop driving the other repository.

A placed receiving session proves itself by resuming the work item. A
non-member Project wastes a host-started session and fails at the placement
read, so the membership record comes first.

Two Projects in one repository keep the within-repository move, and this
record adds no second landing rule inside one repository.

## Verification

- `go test ./internal/agent/ -run
  TestWorktreeClaimRefusesCrossRepositoryBeforeCreation` proves D3's remedy
  text names `concord_work_start` with both identities and carries the
  typed placement answer.
- `bun test adapter/opencode/concord.test.ts -t "a successful work start
  forks no terminal command"` and `bun test
  adapter/opencode/workflow-status.test.ts -t "the work state report forks
  no terminal command"` prove no terminal fork along the work-start and
  work-state report paths. `bun test adapter/opencode/concord.test.ts -t
  "work start resume rejects a project_id selector before any effect"`
  proves the closed resume refuses a `project_id` selector before any child
  runs.
- `python3 scripts/test-installer.py` proves D4: the tuple entry survives an
  upgrade with its options and deregisters whole.
- `go test ./cmd/concord/ -run
  TestLauncherAndCommandCarryNoMultiplexerKnowledge` proves the binaries
  stay multiplexer-free.
- `go test ./internal/store/ -run
  TestManagedExecutionGateAdmitsEveryPlacedSessionOfTheReceivingProject`
  proves D5's widened bind: a second landed session of the receiving
  Project dispatches under a frontier the first session consumed, the
  stale-bind refusal after a contract replacement is equal for both
  sessions, and an unverified or wrong-Project session stays refused.
  `go test ./internal/store/ -run
  TestProjectHandoffConsumeResolvesTheSharedBindForEverySessionOfTheProject`
  proves the consume resolves the shared bind for a second session and
  refuses an unplaced session's replay at the placement boundary, and
  `go test ./internal/store/ -run
  TestBootResumeRendersTheConsumedFrontierToEveryPlacedSessionOfTheProject`
  proves the widened frontier read. `go test ./internal/store/ -run
  TestManagedExecutionGateKeepsTheWrongProjectBindRefusedAndSourceExecutionAdmitted`
  proves the widened bind never crosses Projects: a session placed in the
  source Project acquires no bind while that Project's own execution stays
  admitted. `go test ./internal/store/ -run
  TestConsumedSharedBindSurvivesTheOriginalReceiverVacate` proves the
  widened bind's lifetime: after the original receiver's verified vacate
  landing releases its occupancy and its own admission fails closed, a
  second landed session of the receiving Project still dispatches under
  the standing bind, and its consume replay resolves as already consumed
  with no second consumed event. `bun test
  adapter/opencode/project_handoff_end_to_end.test.ts`
  proves the cold second-session boot on the real boundary: the boot
  re-reads the addressed handoff after its verified landing records
  placement, renders the shared bind's bounded job, and resolves the
  standing bind with no second consumed event.
- Committed law-folder snapshot validation passes.
