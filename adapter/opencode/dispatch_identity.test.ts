import { expect, test } from "bun:test"
import { readExportSessionMetadata, readRunLineMetadata, readRunSessionMetadata } from "./dispatch"

const runEvent = (type: string, sessionID: string, extra: Record<string, unknown> = {}) => JSON.stringify({
  type,
  timestamp: 1,
  sessionID,
  ...extra,
})

const completeRun = [
  runEvent("step_start", "session-1", { part: { type: "step-start", model: "incidental/not-readback" } }),
  runEvent("step_finish", "session-1", { part: { type: "step-finish", reason: "stop" } }),
].join("\n")

test("run metadata accepts only one typed session identity", () => {
  const output = completeRun
  expect(readRunSessionMetadata(output)).toEqual({ ok: true, metadata: { session_id: "session-1" } })
  expect(readRunSessionMetadata(`${output}\n${runEvent("text", "session-2", { part: { type: "text", text: "x" } })}`)).toEqual({ ok: false, refusal: "multiple_session_identities" })
  // A run event missing its typed identity is still fatal.
  expect(readRunSessionMetadata(`${output}\n${JSON.stringify({ type: "text", part: { type: "text", text: "x" } })}`)).toEqual({ ok: false, refusal: "malformed_event" })
  // Host plugins log their own JSON to the same stdout. Those lines are not run
  // events, carry no session identity, and are ignored rather than fatal.
  expect(readRunSessionMetadata(`${JSON.stringify({ ts: "2026-08-22T05:08:17.270Z", level: "info", plugin: "opencode-model-routing", event: "config.loaded" })}\n${output}`)).toEqual({ ok: true, metadata: { session_id: "session-1" } })
  expect(readRunSessionMetadata(`${output}\n${JSON.stringify({ type: "unknown", sessionID: "session-2" })}`)).toEqual({ ok: true, metadata: { session_id: "session-1" } })
  expect(readRunSessionMetadata(`${output}\nplain host chatter that is not JSON`)).toEqual({ ok: true, metadata: { session_id: "session-1" } })
})

// Four conditions previously collapsed into one null, so a caller could not say
// which one fired. Each carries its own refusal, because the operator response
// differs: an oversized stream is not a stream that never identified a session.
test("each run-stream refusal names its own cause", () => {
  const oversized = `${completeRun}\n${runEvent("text", "session-1", { part: { type: "text", text: "x".repeat(70_000) } })}`
  expect(Buffer.byteLength(oversized)).toBeGreaterThan(65_536)
  expect(readRunSessionMetadata(oversized)).toEqual({ ok: false, refusal: "output_exceeded_bound" })

  expect(readRunSessionMetadata("")).toEqual({ ok: false, refusal: "no_official_events" })
  expect(readRunSessionMetadata(JSON.stringify({ ts: "2026-08-22T05:08:17.270Z", level: "info", plugin: "host", event: "config.loaded" }))).toEqual({ ok: false, refusal: "no_official_events" })

  const noCompletion = [
    runEvent("step_start", "session-1"),
    runEvent("text", "session-1", { part: { type: "text", text: "partial" } }),
  ].join("\n")
  expect(readRunSessionMetadata(noCompletion)).toEqual({ ok: false, refusal: "no_completion_event" })

  const abortedCompletion = [
    runEvent("step_start", "session-1"),
    runEvent("step_finish", "session-1", { part: { type: "step-finish", reason: "length" } }),
  ].join("\n")
  expect(readRunSessionMetadata(abortedCompletion)).toEqual({ ok: false, refusal: "no_completion_event" })
})

// The bound is checked before parsing, so an oversized stream reports its size
// rather than whichever parse predicate would have failed first inside it.
test("the output bound outranks every parse refusal", () => {
  const oversizedAndUnidentified = JSON.stringify({ type: "text", timestamp: 1, part: { type: "text", text: "x".repeat(70_000) } })
  expect(Buffer.byteLength(oversizedAndUnidentified)).toBeGreaterThan(65_536)
  expect(readRunSessionMetadata(oversizedAndUnidentified)).toEqual({ ok: false, refusal: "output_exceeded_bound" })
})

test("run line metadata exposes the session before run completion", () => {
  expect(readRunLineMetadata(runEvent("step_start", "session-1"))).toEqual({ session_id: "session-1", official: true, completed: false })
  expect(readRunLineMetadata(runEvent("step_finish", "session-1", { part: { type: "step-finish", reason: "stop" } }))).toEqual({ session_id: "session-1", official: true, completed: true })
  expect(readRunLineMetadata("host chatter")).toBeNull()
  expect(() => readRunLineMetadata(JSON.stringify({ type: "text", timestamp: 1 }))).toThrow()
})

test("export metadata reads the latest typed assistant and ignores nested model-shaped content", () => {
  const exported = JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "preferred", time: { created: 10 } }, parts: [] },
      { info: { id: "message-user", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 15 } }, parts: [] },
      { info: { id: "message-2", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "zai-coding-plan", modelID: "glm-5.2", time: { created: 20 } }, parts: [{ type: "tool", state: { output: { model: "hostile/not-readback" } } }] },
    ],
  })
  expect(readExportSessionMetadata(exported, "session-1")).toEqual({ readback_model: "zai-coding-plan/glm-5.2", readback_agent: "concord-research", session_id: "session-1" })
  expect(readExportSessionMetadata(exported, "different-session")).toBeNull()
})

test("export metadata rejects ambiguous duplicate assistant identity", () => {
  const exported = JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "preferred", time: { created: 10 } }, parts: [] },
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "zai-coding-plan", modelID: "glm-5.2", time: { created: 10 } }, parts: [] },
    ],
  })
  expect(readExportSessionMetadata(exported, "session-1")).toBeNull()
})
