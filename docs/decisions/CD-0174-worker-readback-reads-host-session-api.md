# CD-0174: The worker readback reads the host session API in process

- **Status:** Accepted
- **Date:** 2026-09-23
- **Scope:** OpenCode adapter worker completion and session readback
- **Amends:** CD-0058 D2, CD-0102 D5
- **Related:** CD-0017, CD-0059
- **Approval:** The operator approved replacing the export subprocess after its truncation and exit-before-drain behavior stopped managed completions.

## Context

The readback spawned `opencode export` and read its stdout. That child writes
its body and exits before the pipe drains, so the adapter read an empty or
truncated body, and a byte ceiling refused large transcripts. Both faults turn
into a missing executing-model evidence at completion, which stops the lane.

## Decision

### D1. The readback source is the host session API

The completion path reads the worker session in process through the host
session API: one session record, then a bounded read of the message pages. The
export subprocess path and its runner are removed. The assembled body holds
the same `{info, messages}` shape, so the byte-exact opening packet check and
the model and agent identity checks run unchanged over it.

### D2. The bound is a message-page bound

The messages read takes a bounded page and follows the host pagination cursor
toward the oldest page, with a fixed page count bound. A transcript that
exceeds the bound refuses with the typed `readback_message_bound` predicate.
The former 8 MiB byte ceiling is removed; transcript size alone can no longer
refuse a completion.

### D3. The evidence contract is unchanged

CD-0058 D2 keeps its meaning: the readback model stays the sole model
evidence, a readback without one model identity is still recorded as one
attempt born `failed`, and the adapter never retries it. CD-0102 D5 keeps its
meaning: completion still resolves the report, signs the dispatch and terminal
assertions, and records the outcome. A host read that answers an error status
refuses the readback as a born-failed read; a transport fault reports the
host's own diagnostic and names reconciliation.

## Verification

- The adapter suite proves the completion readback runs one session read and
  one bounded page, admits a transcript larger than any pipe buffer, refuses
  an unbounded transcript with the typed predicate, and retains the session
  diagnostics and the worker result on the recorded failed attempt.
- The route end-to-end test dispatches through the real core with the session
  API reader and completes on the real store.
