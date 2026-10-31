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
  session; the installer's recognition
  of the plugin tuple entry, the coordinator instructions that name the
  route, and the bounded Project-session handoff its sessions record and
  consume
- **Approval:** Concord (CON) issue CON-910 (2026-10-13), under the Snowball coordinator's operator-delegated approval.
- **Related:** CD-0088, CD-0178, CD-0093, CD-0176, CD-0163
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
session in each repository. Host placement must preserve that boundary without
putting terminal or worktree lifecycle control in Concord.

Two accepted boundaries stood in the way of a product answer. CD-0078 D1
forbids every Concord component from knowing a terminal multiplexer, and its
record rejected a pluggable terminal-placement strategy as an abstraction
with one real implementation on one machine. CD-0163 restates that
boundary.

## Decision

### D1. Host tools place coordinator sessions

Host tools start and place the second coordinator session under CD-0088 D1.
CD-0078 D1 keeps binding every Concord binary and command.

### D2. Membership precedes placement

The membership record comes first. A non-member Project refuses at the
existing store read. CD-0093 D3's fail-closed rule binds session preparation.

### D3. Placement is not Concord execution authority

The calling directory resolves the receiving Project. Host placement grants
no workflow authority and does not replace existing placement or handoff
checks. Concord core neither chooses a terminal host nor opens a terminal
session. It records no new claim or vacate landing (CD-0088 D1).

### D4. The installer keeps options, and the instructions name the route

`plan_plugin_entry` and `remove_plugin_entry` recognize the tuple form of
the Concord entry and keep its options, so an upgrade never adds a duplicate
bare entry and an uninstall removes the tuple whole. Coordinator definitions
name host-owned worktree isolation and carry no Concord opener, bootstrap,
claim-and-move, or vacate recipe.

### D5. The Project-selected session carries a bounded addressed handoff

A session placed in the receiving Project receives one bounded repository job.
The job names its receiving Project, shared work item, and active contract.
The source session records that job as a typed handoff on the work item.
The record carries the changes, the verification results, the immutable
artifact references, the open blockers, and the exact next action. The
source session verifies its changed artifacts against the host-reported
worktree it runs in before the record commits: an artifact outside that
worktree, a dirty worktree, or an untracked file refuses, and the core never
commits, stashes, or hides files.

The receiving session consumes the recorded handoff before it runs any
managed external effect. The consume binds the record to the work item
and its addressed receiving Project under the current contract. Any
coordinator session whose directory resolves the receiving Project, a member
of the work, may act under the frontier handoff's bind, and every such
session reads the frontier's bounded job. The directory is the
authenticated session directory that resolves the Project for every Concord
call (CD-0178 D2); Concord records no separate placement for the bind.
A session that resolves no member Project, or resolves another Project,
stays refused. A missing, wrong-target, or stale handoff refuses.
Managed-execution admission fails closed while an unconsumed or stale
handoff stands, and a contract replacement refuses the stale bind equally
for the consuming session and a later session of the Project.

Opening a session in another Project serves only an actual edit in the
other repository, never a handoff-bind recovery. A placement or
handoff-bind refusal in the receiving Project is not that evidence. While
the current contract stands, a session records no fresh handoff and opens
no new session to recover from such a refusal. The refusing session follows
the served recovery in its own Project. No replacement placement writer is
authorized by this decision.

`READY_TO_CLOSE_OR_REPLACE` is a derived read, never a written state. It
reports ready only when the session recorded an addressed handoff, its
changed artifacts verify preserved, its nonterminal actions stopped, and its
retained departure checks pass. Host tools own the physical move; no Concord
vacate or occupancy-release writer is required. Retirement never completes or
cancels the shared work, removes a worktree, or terminates a process. The
continuity snapshot carries the newest handoff addressed to the reading
session's Project under the active contract, consumed or not. A read from
any other Project carries the work's newest unconsumed handoff.
The receiving Project's continuity read names the bounded job without
operator copying.

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
- Launch with `CONCORD_SELECTED_PROJECT_PATH` plus work identity. Rejected:
  that variable deliberately refuses Concord identity, and a path cannot
  carry the work-bound landing rules.

## Consequences

Cross-repository work uses host placement. Once the receiving session is placed, the first session must
stop driving the other repository.

A non-member Project fails at the placement read, so the membership record
comes first. Existing placement reads remain required.

Host tools own within-repository placement too.

## Verification

- `bun test adapter/opencode/workflow-status.test.ts -t "the work state report
  forks no terminal command"` proves the work-state report starts no terminal.
- `python3 scripts/test-installer.py` proves D4: the tuple entry survives an
  upgrade with its options and deregisters whole.
- `go test ./cmd/concord/ -run
  TestLauncherAndCommandCarryNoMultiplexerKnowledge` proves the binaries
  stay multiplexer-free.
- `go test ./internal/store/ -run
  TestManagedExecutionGateAdmitsEverySessionOfTheReceivingProject`
  proves D5's widened bind: a second session of the receiving Project
  dispatches under a frontier the first session consumed, the stale-bind
  refusal after a contract replacement is equal for both sessions, and a
  session that resolves no Project stays refused.
  `go test ./internal/store/ -run
  TestManagedExecutionGateRefusesUnresolvedProjectOnHandoffBearingWork`
  proves an unresolved or non-member Project refuses admission on
  handoff-bearing work. `go test ./internal/store/ -run
  TestProjectHandoffConsumeResolvesTheSharedBindForEverySessionOfTheProject`
  proves the consume resolves the shared bind for a second session and
  refuses a replay from a session that resolves no Project, and
  `go test ./internal/store/ -run
  TestContinuityRendersTheAddressedFrontierToTheReceivingProject`
  proves the widened frontier read. `go test ./internal/store/ -run
  TestManagedExecutionGateKeepsTheWrongProjectBindRefusedAndSourceExecutionAdmitted`
  proves the widened bind never crosses Projects: a session of the source
  Project acquires no bind while that Project's own execution stays
  admitted. `go test ./internal/store/ -run
  TestProjectHandoffConsumeAdmissionAndRetirementWithoutOwnershipRows`
  proves record, consume, admission, and retirement succeed with no Concord
  ownership row. `bun test
  adapter/opencode/project_handoff_end_to_end.test.ts`
  exercises the receiving session's bounded job and standing bind.
- Committed law-folder snapshot validation passes.
