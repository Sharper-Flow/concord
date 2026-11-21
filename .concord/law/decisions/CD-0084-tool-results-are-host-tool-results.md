---
{
  "id": "CD-0084",
  "title": "Concord tool results are host ToolResults",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": []
}
---
# CD-0084: Concord tool results are host ToolResults

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** The tool exports in `adapter/opencode/concord.ts`; the
  `execute` return boundary and `encodeHostResult`; host qualification;
  the host hooks the plugin entry `adapter/opencode/concord-plugin.ts`
  registers, the per-call session-directory read in
  `adapter/opencode/session-directory.ts`, and completion reporting
- **Approval:** Operator approved a decision record for this boundary rather
  than a bare defect fix. The implementation is accepted in PR #579.
- **Related:** CD-0005, TS6, agent-result-envelope, issues #485 and #560
- **Preserves:** The agent result envelope and its generated contract; the
  envelope-thin adapter boundary in the transport contract

## Context

The pinned `@opencode/plugin@2.3.2` host result uses `content` for tool text
([schema](https://github.com/anomalyco/opencode/blob/c0ad18b402eb2827fc1ee71c171b1177b3fc02d1/packages/schema/src/tool.ts#L103-L107)).
The host treats an `output` key on a tool without an output schema as a defect,
even when its value is undefined
([runtime](https://github.com/anomalyco/opencode/blob/c0ad18b402eb2827fc1ee71c171b1177b3fc02d1/packages/core/src/tool/runtime.ts#L48-L49)).
The adapter must conform to that boundary without changing the core envelope.

## Decision

### D1. The adapter returns the host's declared Tool.Result

Every tool export returns one value satisfying `Tool.Result`. The adapter treats
the host declaration as binding, not advisory. No export returns a domain
object to the host.

### D2. The envelope travels as JSON text, and `content` is its only carrier

`content` carries `JSON.stringify(envelope)`. Nothing is dropped, summarized, or
reordered. `metadata` carries no copy of the envelope, so the result has one
carrier and one parse: a reader never has to decide which of two copies is
current, and the payload is not doubled against the host's byte ceiling. The
result has no `output` key and `metadata` never contains `truncated`: the host
skips its output bound for any result whose `metadata.truncated` is set
([source](https://github.com/anomalyco/opencode/blob/c0ad18b402eb2827fc1ee71c171b1177b3fc02d1/packages/core/src/tool-output.ts#L76)).

The adapter does not render prose for the model. A rendered summary would move
presentation into the envelope-thin adapter, and would
put adapter wording between the core result and the reader.

### D3. The execute boundary states its own return type

`invokeConcordOperation` returns `CoreConcordEnvelope`, not `any`. Each export
declares `Promise<Tool.Result>` and reaches the host through `encodeHostResult`.
No export reaches the host contract through `any`, so the typechecker sees the
boundary.

### D4. An oversize result becomes a bounded error envelope

The host bounds output at 2,000 lines or 51,200 bytes
([source](https://github.com/anomalyco/opencode/blob/c0ad18b402eb2827fc1ee71c171b1177b3fc02d1/packages/core/src/tool-output.ts#L13-L14)). Rather than let it cut an
envelope into invalid JSON, `encodeHostResult` measures the serialized envelope
against the 51,200-byte `maxEnvelopeBytes` and replaces an oversize result with
an adapter error chosen by operation kind. An oversize mutation response may
describe a committed effect, so it becomes `unknown_effect` with effect state
`possible`. A read cannot have written, so it becomes
`malformed_core_response`. A reader therefore parses either the whole envelope
or a valid statement that it did not fit, and never a fragment.

### D5. The host owns sessions and tool availability; the agent writes completion

The host owns session lifecycle, placement, titles, prompts, continuation, and
tool availability. Concord reads the host session directory per call for core
Project resolution; it does not manage or move sessions, inject prompts, or
gate host tools. Release leases, exact adapter/core pairing, and migration/data
safety remain under [CD-0111](CD-0111-a-release-never-requires-a-session-restart.md).

The agent writes its completion response from the recorded facts and evidence.
Concord has no closure-receipt CLI, renderer package, work-state reporter, or
assistant-text completion injection. Tool output carries the envelope under D2,
not appended notices or a second assistant-text channel. Optional diagnostics
may use the existing bounded envelope warnings only when they fit; omitting a
diagnostic never changes the authoritative result or effect.

An agent may begin and continue work on any installed release under its pinned
pair. Concord requires no installed-current currency comparison at capture or
resume and emits no per-call release-staleness notice.

## Consequences

An agent receives the core envelope as JSON text in `content`. Callers read
envelope fields from parsed `content`.

`encodeHostResult` owns the conversion, so a new tool cannot reach the host
through a different path. A test asserts that every tool the contract declares
returns a string `content` that parses to a valid envelope, and a second test
holds the oversize path to the byte ceiling.

The host pin holds no outstanding allowance. A future TS2322 is a real
diagnostic rather than an expected one.

`scripts/qualify-opencode-host.py` qualifies the exact pinned host in an isolated
environment with a scripted OpenAI-compatible model server and no credentials.

This decision binds the adapter to a host declaration Concord does not own. If
the host widens or changes `Tool.Result`, the pin records the version and the
generated contract tests fail on the change.

## Rejected alternatives

**Keeping the raw envelope.** Rejected because it does not satisfy the host
result contract.

**A cast to silence TS2322.** Rejected because the diagnostic was correct. The
cast would have removed the last signal that the surface did not work.

**An adapter-rendered human summary in `content`.** Rejected under D2.
It reads better and costs fewer tokens, but it makes the adapter decide what
the model learns about a core result.

**Carrying the envelope object on `metadata` as well.** Rejected because a
second copy serves no reader the parsed `content` does not, and two copies of
one result can disagree once either path gains handling.

**Truncating an oversize envelope to the ceiling.** Rejected because a cut
envelope is invalid JSON that still looks like a result. D4 substitutes a valid
error envelope instead.
