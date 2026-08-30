# CD-0084: Concord tool results are host ToolResults

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** The ten `tool()` exports in `adapter/opencode/concord.ts`; the
  `execute` return boundary and `encodeHostResult`; the recorded runtime probe
  and the spent TS2322 allowance in `docs/adapter-host-pin.v1.json`; issue #560
- **Approval:** Operator approved a decision record for this boundary rather
  than a bare defect fix. The implementation is accepted in PR #579.
- **Related:** CD-0005, CD-0017 D2, TS6, TS7, issues #485 and #560
- **Preserves:** The TS7 result envelope and its generated contract; the
  envelope-thin adapter boundary CD-0017 D2 sets

## Context

Every Concord tool export returned the core response envelope. The host
declares a narrower type. `@opencode-ai/plugin@1.18.23` declares `ToolResult`
as a string, or an object carrying a string `output`.

The pinned host does not tolerate the wider value. At
`packages/opencode/src/tool/registry.ts:155` core reads
`typeof result === "string" ? result : result.output`. It passes that value to
`truncate.output` at line 159. `packages/opencode/src/tool/truncate.ts:85`
declares the parameter `text: string` and line 90 calls `text.split("\n")`.
The function holds no guard for an absent value.

A Concord envelope carries `outcome`, `error`, and payload fields, and no
`output`. Core therefore read `undefined` and threw a `TypeError`. The AI SDK
turns a rejected `execute` into a `tool-error` part, so the model received an
error string instead of the response. No Concord tool call could succeed.

Two conditions hid this. `invokeConcordOperation` was typed `Promise<any>`, so
nine exports typechecked; only `work_transition` reported TS2322, and issue
#485 recorded that as a single allowance. CD-0010 forbids Concord from
coordinating its own development, so the tool surface never ran against a real
host, and the defect never showed itself in use.

Issue #560 asked whether the adapter or the declaration was wrong. The
declaration is the contract, and core enforces it by crashing rather than by
validating.

## Decision

### D1. The adapter returns the host's declared ToolResult

Every tool export returns a value satisfying `ToolResult`. The adapter treats
the host declaration as binding, not advisory. No export returns a domain
object to the host.

### D2. The envelope travels as JSON text, and `output` is its only carrier

`output` carries `JSON.stringify(envelope)`. Nothing is dropped, summarized, or
reordered. `metadata` carries no copy of the envelope, so the result has one
carrier and one parse: a reader never has to decide which of two copies is
current, and the payload is not doubled against the host's byte ceiling.

The adapter does not render prose for the model. A rendered summary would move
presentation into the adapter, which CD-0017 D2 keeps envelope-thin, and would
put adapter wording between the core result and the reader.

### D3. The execute boundary states its own return type

`invokeConcordOperation` returns `CoreConcordEnvelope`, not `any`. Each export
declares `Promise<ToolResult>` and reaches the host through `encodeHostResult`.
No export reaches the host contract through `any`, so the typechecker sees the
boundary and the TS2322 allowance is spent and removed.

### D4. An oversize result becomes a bounded error envelope

The host truncates output above a byte ceiling. Rather than let it cut an
envelope into invalid JSON, `encodeHostResult` measures the serialized envelope
against `maxEnvelopeBytes` and replaces an oversize result with a
`malformed_core_response` adapter error. A reader therefore parses either the
whole envelope or a valid statement that it did not fit, and never a fragment.

## Consequences

An agent receives the same envelope content it received before, as JSON text
rather than as a structured object. Callers that read fields off the tool
result read them from parsed `output`.

`encodeHostResult` owns the conversion, so a new tool cannot reach the host
through a different path. A test asserts that every tool the contract declares
returns a string `output` that parses to a valid envelope, and a second test
holds the oversize path to the byte ceiling.

The host pin holds no outstanding allowance. A future TS2322 is a real
diagnostic rather than an expected one.

`docs/adapter-host-pin.v1.json` records a runtime probe under
`probe_identity: tool-result-contract-v1`. The probe ran both shapes against
the pinned host: the bare envelope failed with
`undefined is not an object (evaluating 'c.split')`, the minified form of the
unguarded `split` above, and the conforming result passed. The reading is
therefore held by source and by observation, and the probe is re-runnable when
the pin moves.

This decision binds the adapter to a host declaration Concord does not own. If
the host widens or changes `ToolResult`, the pin records the version and the
generated contract tests fail on the change.

## Rejected alternatives

**Recording the divergence and keeping the raw envelope.** This was reading 2
in issue #560, and it required core to accept a wider value than it declares.
Source and probe both refute it: `registry.ts:155` reads `output`,
`truncate.ts:90` splits it without a guard, and the probe observed the crash.
Recording a divergence would have documented a crash as a design.

**A cast to silence TS2322.** Rejected because the diagnostic was correct. The
cast would have removed the last signal that the surface did not work.

**An adapter-rendered human summary in `output`.** Rejected under CD-0017 D2.
It reads better and costs fewer tokens, but it makes the adapter decide what
the model learns about a core result.

**Carrying the envelope object on `metadata` as well.** Rejected because a
second copy serves no reader the parsed `output` does not, and two copies of
one result can disagree once either path gains handling.

**Truncating an oversize envelope to the ceiling.** Rejected because a cut
envelope is invalid JSON that still looks like a result. D4 substitutes a valid
error envelope instead.
