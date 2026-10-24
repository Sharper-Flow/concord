# CD-0174: The worker readback reads the host session API in process

- **Status:** Accepted
- **Date:** 2026-09-23
- **Amended:** 2026-10-17. Coordinator approval under the operator's delegation
  for [Concord (CON) issue 160](https://linear.app/sharper-flow/issue/CON-160). Readback walks every page under a deadline and keeps only readback evidence.
- **Scope:** OpenCode adapter worker completion and session readback
- **Related:** CD-0017, CD-0059
- **Approval:** The operator approved replacing the export subprocess after its truncation and exit-before-drain behavior stopped managed completions.

## Context

The readback spawned `opencode export` and read its stdout. That child writes
its body and exits before the pipe drains, so the adapter read an empty or
truncated body, and a byte ceiling refused large transcripts. Both faults turn
into a missing executing-model evidence at completion, which stops the lane.

## Decision

### D1. The readback source is the host session API

The completion path reads the worker session in process through the host session API: one session record, then message pages of at most 128 messages. The evidence body keeps every message's identity, role, time, and model and agent fields, the unchanged oldest message, and any retained Task part selected for reconciliation. Other message parts are not readback evidence and are not retained. The body keeps the `{info, messages}` shape, so the byte-exact opening packet, duplicate-message, model, and agent checks apply to the complete page walk. Its digest binds this evidence body, not discarded transcript parts.

### D2. Readback bounds pages and time, not transcript length

The read follows the host pagination cursor to the oldest page. It has no fixed page-count limit. A 30-second readback deadline and caller cancellation bound the walk; either stops it as a transport failure that names reconciliation, without recording a terminal readback refusal. A repeated pagination cursor refuses with `session_read`. A finite transcript that reaches the oldest page is checked in full, regardless of its page count or byte size. No reduced identity proof, truncated opening packet, or replacement worker substitutes for that check.

### D3. The evidence contract is unchanged

CD-0058 D2 keeps its meaning: the readback model stays the sole model
evidence, a readback without one model identity is still recorded as one
attempt born `failed`, and the adapter never retries it. CD-0102 D5 keeps its
meaning: completion still resolves the report, signs the dispatch and terminal
assertions, and records the outcome. A host read that answers an error status
refuses the readback as a born-failed read; a transport fault reports the
host's own diagnostic and names reconciliation.

### D4. The lane packet cannot form a host agent mention

The host resolves an `@agent` mention in a Task prompt into an agent part and
a synthetic instruction in the worker's opening message. A packet that quotes
a prior session title carries such a mention. One serializer produces the
packet bytes for both the Task prompt and the opening-packet comparison, and
it escapes every `@` as `\u0040`, which JSON decoding restores. The opening
message is then exactly one text part, and any other part refuses the
readback with `dispatched_packet_identity`.

## Verification

- The adapter suite proves that completion reads every page of a transcript longer than sixteen pages, retains only readback evidence, and records a valid report. It proves that duplicate messages and conflicting model identities on different pages still refuse, that repeated cursors refuse, and that cancellation stops the walk without recording terminal failure. Session diagnostics and the worker result remain available on a recorded readback failure.
- The adapter suite proves a packet that contains `@concord-implement`
  serializes with no `@` and decodes to the same packet, and that a host agent
  part beside the packet refuses.
- The route end-to-end test dispatches through the real core with the session
  API reader and completes on the real store.
