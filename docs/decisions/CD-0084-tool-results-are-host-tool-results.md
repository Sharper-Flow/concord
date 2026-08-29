# CD-0084: Concord tool results are host ToolResults

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** The ten `tool()` exports in `adapter/opencode/concord.ts`; the
  `execute` return boundary; the spent TS2322 allowance in
  `docs/adapter-host-pin.v1.json`; issue #560
- **Approval:** Operator approved on 2026-08-29, choosing the serialized
  envelope in `output` with the same object on `metadata`, a decision record
  rather than a bare defect fix, and removal of the concealing `any`.
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

Issue #560 asked whether the adapter or the declaration was wrong, and proposed
a runtime probe. The upstream source answers it. The declaration is the
contract, and core enforces it by crashing rather than by validating.

## Decision

### D1. The adapter returns the host's declared ToolResult

Every tool export returns a value satisfying `ToolResult`. The adapter treats
the host declaration as binding, not advisory. No export returns a domain
object to the host.

### D2. The envelope travels as JSON text and rides metadata unchanged

`output` carries `JSON.stringify(envelope)`. `metadata` carries the same
envelope object for hosts that read structured tool metadata. Nothing is
dropped, summarized, or reordered.

The adapter does not render prose for the model. A rendered summary would move
presentation into the adapter, which CD-0017 D2 keeps envelope-thin, and would
put adapter wording between the core result and the reader.

### D3. The execute boundary states its own return type

`invokeConcordOperation` returns `CoreResponse`, not `any`. Each export
declares `Record<string, unknown>` arguments and returns `Promise<ToolResult>`
through one normalization helper. No export reaches the host contract through
`any`, so the typechecker sees the boundary and the TS2322 allowance is spent
and removed.

## Consequences

An agent receives the same envelope content it received before, as JSON text
rather than as a structured object. Callers that read fields off the tool
result read them from parsed `output`, or from `metadata`.

One normalization helper owns the conversion, so a new tool cannot reach the
host through a different path. A test asserts that every tool the contract
declares returns a string `output`, and it fails if a new export is added
without that guarantee.

The host pin holds no outstanding allowance. A future TS2322 is a real
diagnostic rather than an expected one.

This decision binds the adapter to a host declaration Concord does not own. If
the host widens or changes `ToolResult`, the pin records the version and the
generated contract tests fail on the change.

## Rejected alternatives

**Recording the divergence and keeping the raw envelope.** This was reading 2
in issue #560, and it required core to accept a wider value than it declares.
The source refutes it: `registry.ts:155` reads `output`, and `truncate.ts:90`
splits it without a guard. Recording a divergence would have documented a
crash as a design.

**A cast to silence TS2322.** Rejected because the diagnostic was correct. The
cast would have removed the last signal that the surface did not work.

**An adapter-rendered human summary in `output`.** Rejected under CD-0017 D2.
It reads better and costs fewer tokens, but it makes the adapter decide what
the model learns about a core result.

**Waiting for a runtime probe against a live host.** Rejected because the
pinned source settles the question with more precision than one observation.
A probe shows what one host build did; `registry.ts` and `truncate.ts` show
why, and for which versions.
