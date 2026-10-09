# CD-0191: A stale session sees its replaced release and cannot dispatch a worker

- **Status:** Accepted
- **Date:** 2026-09-28
- **Scope:** The adapter's installed-versus-session release comparison at the
  tool boundary, the results and `dispatch_worker` refusal of a stale
  session, and the installer's retention output
- **Amends:** CD-0111 (adds the lane-surface staleness rule beside D1 and D2)
- **Related:** CD-0102, CD-0111, CD-0132
- **Approval:** The operator approved this decision on work item
  `work-fd94329981b8575144fe93ed`, with the objective and the design
  recorded in its dispatch packet.
- **Preserves:** CD-0111 D1's pinned pair, CD-0111 D2's lease-held retention,
  and the closed envelope and refusal vocabulary

## Context

CD-0111 pins a session's core calls to the release it started on, so an
install never breaks a running session. The lane agent definitions under the
host config directory are different. OpenCode loads them once at process
start and never hot-reloads them, while `scripts/install.py` rewrites them in
place on every install. After an install, every still-running session
dispatches lanes with replaced lane text while believing it is current.

No layer detected this. The adapter compared its stamped release root only
with the core's lease at session start and never against the host's
`current` symlink at call time. On 2026-09-28 this burned a terminal worker
attempt: a verify lane self-failed under a predicate rule its process still
held in memory after a lane fix shipped.

## Decision

### D1. The adapter owns installed-versus-session staleness

At every concord tool call the adapter derives the data root as the parent of
its stamped release root and reads the `current` symlink beside it. Any
difference between the pinned release and the installed release is stale.
The comparison costs one readlink per call. An absent or unreadable link is
never stale: staleness is never guessed from a missing observation.

Inequality is the rule in both directions. An upgrade and a rollback both
repoint `current` and rewrite the lane files a running process holds in
memory, so both carry the same mismatch hazard.

### D2. Every result from a stale session carries one bounded notice

A stale session's every result carries one warning notice that names both
versions and the restart remedy. The notice rides the envelope's bounded
warnings channel in the closed notice shape, so no schema changes. When the
core envelope already fills the warnings array, the same notice rides the
tool-result output layer. Reads and every other mutation keep succeeding.

### D3. The dispatch_worker action refuses while stale

The adapter refuses a stale session's `dispatch_worker` action before any
core call, so no worker attempt opens on lane text the install replaced. The
refusal rides the dispatch surface's adapter-gate envelope, the shape the
turn-move gate already uses: `unauthorized_dispatch` with the
`release_stale` boundary, and the `stale_context` refusal kind in the error
details. The closed adapter-origin kind vocabulary of the tool envelope
has no stale-context classification, and this decision changes no schema,
so the classification rides the details member this surface owns.

Every other action keeps working. A stale session can finish its item,
record its evidence, and reach a terminal state. Only new worker attempts
close.

### D4. The installer names the holder sessions

The installer's lease observation already decides which release directories
to retain. Its retention output now enumerates the holder sessions per
retained release, each with its process id and its directory or worktree, so
the operator knows exactly which sessions to restart.

## Alternatives considered

- Refuse every call while stale. Rejected: this bricks every live session on
  each install and contradicts CD-0111's no-restart guarantee for reads.
- Warn only and refuse nothing. Rejected: the terminal-attempt harm stays
  open, because a stale-text worker self-failure needs operator approval to
  retry.
- Compare releases only when the installed release is newer. Rejected: a
  rollback leaves sessions dispatching against mismatched lane text with no
  signal.
- Compare in the core against a well-known path. Rejected: each release's
  core derives its release from its own binary and cannot see host install
  state.
- Add a new envelope field for staleness. Rejected: the warnings channel
  already carries bounded notices, and a schema change contradicts the
  closed vocabulary.
- List holder sessions through a separate command. Rejected: the install
  moment is when the operator needs the list, and the installer already
  queries the leases.

## Consequences

A stale session stays usable: reads and non-dispatch mutations work, and
every reply names both versions and the restart remedy. New worker attempts
cannot open on replaced lane text, so no attempt burns on a rule the process
no longer holds. The refusal precedes any core call, so it records nothing
and needs no reconciliation. The install moment now answers the operator's
question directly: which sessions hold which release, and which to restart.

## Verification

- `bun test adapter/opencode/` proves the resolver, the per-result notice,
  the full-warnings fallback, and the dispatch refusal before any core call.
- `python3 scripts/test-installer.py` proves the retention output names the
  holder sessions per retained release.
- `python3 scripts/check-doc-contract.py` proves this record's outline and
  prose against the decision contract.
