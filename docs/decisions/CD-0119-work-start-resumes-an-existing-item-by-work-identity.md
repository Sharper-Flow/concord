# CD-0119: Work start resumes an existing item by work identity

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** The `concord_work_start` argument surface; the resume read and
  existing-item bootstrap that derives an existing item's canonical worktree;
  issue #891
- **Approval:** The operator approved the contract in session on 2026-09-06
  (Concord work `work-daa4c53e214c07b611364073`), choosing the
  `concord_work_start` resume shape over a `worktree_enter` operation. The
  operator amended D3 in session on 2026-09-08 (observation
  `obs:a61be78c4a8ddee1`): the read derives the target before the origin
  gate, so a dirty same-target resume converges instead of refusing. The
  operator amended D1-D2 in the approved bootstrap-first plan: an eligible
  existing item without an active worktree starts through the durable
  host-owned bootstrap under its original work identity.
- **Related:** CD-0096, CD-0098, CD-0104, CD-0110, issues #891, #822
- **Amends:** CD-0098 D1 at its capture-only clause; CD-0104 D3 at its
  digest-keyed clause
- **Preserves:** CD-0096 D2 (identity over input), CD-0098 D2 and D3 (the move
  route and its read-back), CD-0104 D1 and D5 (no stored session binding; no
  occupancy refusal)

## Context

CD-0104 D5 states that a session which needs to edit another work item's
worktree starts that work item. The only start route keyed on the request
digest, so a session that held only a `work_id` — from the portfolio, a
continuity trace, or an item captured by intake or another session — could not
reproduce it. The claim path refuses a second claim on a claimed item, and the
host move route answered no typed caller. Operators saw agents ask for new
sessions, which is the cost CD-0098 removed.

## Decision

### D1. `concord_work_start` accepts a resume shape

Beside the capture shape, the tool accepts `{work_id}` alone. The resume
request carries no `idempotency_key` and no capture fields. The host derives a
stable bootstrap identity from the Product, Project, and work identity when it
must create the item's first worktree.

### D2. The resume read derives the entry, or starts the missing entry

The `work-resume` CLI takes the Product, Project, and work identity, applies
the origin gate `work-bootstrap` applies (CD-0110 D1 as amended for issue
#896), and returns the item's active worktree entry for the resolved Project.
If the item is live, in scope, and has no active worktree, the same command
invokes the durable bootstrap recovery mechanism. That operation records the
existing work identity and current version before Git effects, then adds only
the worktree event. It does not create a second work item, reset workflow
state, or accept a worktree path from a caller (CD-0096 D2).

### D3. The resume runs the same tail as a capture

The adapter runs `session-prepare` in the entry directory, moves the session
through the host route, reads the landing back, and refuses on mismatch
(CD-0098 D3). The move is a no-op when the session already runs there, so a
replay converges. The read derives an active entry before it applies the
origin gate: a session that already runs in the target chains from no origin,
so a dirty target does not refuse. A resume that creates a missing entry uses
the origin gate and the durable bootstrap phases. `session-prepare` takes an
optional task: a capture sends its task, a resume sends none, and the derived
prompt carries a task line only when a task exists.

### D4. The published tool view is flattened, the contract is not

The host publishes tool arguments as a flat per-field shape that cannot
express the alternative, so the published view lists every field optional.
The host tool manifest stays the closed `oneOf` of the two shapes, the
generator pins it, and the adapter refuses a call that matches neither shape
or both.

## Acceptance Criteria

```gherkin
Scenario: A session resumes an existing item from the default checkout
  Given a work item with an active worktree in the session's Project
  When the session calls concord_work_start with only the work_id
  Then the read derives the active entry for that Project
  And the session moves to the entry and the landing is read back

Scenario: A session resumes from a live item's clean worktree
  Given a session that runs in another item's clean worktree
  When the session calls concord_work_start with the target work_id
  Then the origin gate admits the clean origin
  And the session moves to the target entry

Scenario: A session resumes its own worktree while it is dirty
  Given a session that runs in the target item's worktree with uncommitted changes
  When the session calls concord_work_start with that work_id
  Then the read derives the entry and applies no origin gate
  And the move is the convergent no-op and the resume succeeds

Scenario: The resume read refuses what it cannot derive or start
  Given a work item that is terminal, unknown, outside the Project membership, or outside the Product scope
  When the session calls concord_work_start with that work_id
  Then the refusal is typed and records nothing
  And no move runs

Scenario: A live item without an active worktree starts under its original identity
  Given a live work item with Project membership and no active worktree
  When the session calls concord_work_start with only the work_id
  Then the host records bootstrap intent before native Git effects
  And the canonical worktree is claimed without a new work item or workflow reset

Scenario: Occupancy never refuses the move
  Given another live session already runs in the target worktree
  When the session calls concord_work_start with that work_id
  Then the session moves anyway (CD-0104 D5)
```

## Consequences

- The CD-0104 D5 clause has a reachable implementation for an item captured
  by any session, by intake, or with a lost idempotency key.
- `session-prepare` no longer requires a task, so its prompt omits the task
  line for a resume.
- The published model-facing view of `concord_work_start` carries no required
  field; callers read the manifest or the refusal to learn the two shapes.
- The `work-resume` read joins `work-bootstrap` as a host-consumed CLI verb;
  the command specification lists its closed input. Existing-item bootstrap
  uses the same recovery phases and does not change the capture contract.

## Verification

- `cmd/concord.TestWorkResumeDerivesActiveEntryFromDefaultCheckout`,
  `TestWorkResumeRefusesTerminalAndUnknownButBootstrapsUnclaimedWork`,
  `TestWorkResumeBootstrapsExistingIdentityAndRecoversAfterNativeCreate`, and
  `TestWorkResumeAppliesTheBootstrapOriginGate` prove D2.
- `cmd/concord.TestWorkResumeSameTargetSkipsTheOriginGate` proves D3's
  same-target clause: a dirty target resumes, and the gate still refuses a
  dirty different-target move inside `TestWorkResumeAppliesTheBootstrapOriginGate`.
- `internal/store.TestResumeWorktreeLocationRefusals` proves the typed store
  refusals.
- `cmd/concord.TestSessionPrepareAcceptsEmptyTask` proves D3's optional task.
- `adapter/opencode/concord.test.ts` "work start resume derives the entry by
  work_id and moves the session", "forwards the typed core refusal and reads
  the landing back", and "rejects mixed and malformed argument shapes" prove
  D1, D3, and D4.
- `python3 scripts/generate-agent-contracts.py --check` passes with the
  pinned `oneOf` host manifest.
- `python3 scripts/check-doc-contract.py`, `python3 scripts/check-json.py`,
  `python3 scripts/check-doc-links.py`,
  `python3 scripts/check-knowledge-closure.py`, and
  `python3 scripts/check-knowledge-index.py` pass on this record.
