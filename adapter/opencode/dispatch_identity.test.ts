import { expect, test } from "bun:test"
import { readExportSession, readExportSessionMetadata, readRunLineMetadata, readRunSessionMetadata, runStreamRefusalMessage } from "./dispatch"

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

test("bounded run streams preserve every refusal name and message", () => {
  const cases = [
    { refusal: "malformed_event" as const, message: "carried an official run event with no typed session identity", output: `${completeRun}\n${JSON.stringify({ type: "text", part: { type: "text", text: "x" } })}` },
    { refusal: "no_official_events" as const, message: "carried no official run event", output: "host chatter" },
    { refusal: "no_completion_event" as const, message: "ended before a step completed", output: JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1" }) },
    { refusal: "multiple_session_identities" as const, message: "carried more than one session identity", output: `${completeRun}\n${runEvent("text", "session-2", { part: { type: "text", text: "x" } })}` },
  ]
  for (const { refusal, message, output } of cases) {
    expect(Buffer.byteLength(output)).toBeLessThanOrEqual(65_536)
    expect(readRunSessionMetadata(output)).toEqual({ ok: false, refusal })
    expect(runStreamRefusalMessage[refusal]).toBe(message)
  }
})

test("run line metadata exposes the session before run completion", () => {
  expect(readRunLineMetadata(runEvent("step_start", "session-1"))).toEqual({ session_id: "session-1", official: true, completed: false })
  expect(readRunLineMetadata(runEvent("step_finish", "session-1", { part: { type: "step-finish", reason: "stop" } }))).toEqual({ session_id: "session-1", official: true, completed: true })
  expect(readRunLineMetadata("host chatter")).toBeNull()
  expect(() => readRunLineMetadata(JSON.stringify({ type: "text", timestamp: 1 }))).toThrow()
})

test("export metadata refuses multiple typed model identities", () => {
  const exported = JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "preferred", time: { created: 10 } }, parts: [] },
      { info: { id: "message-user", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 15 } }, parts: [] },
      { info: { id: "message-2", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "zai-coding-plan", modelID: "glm-5.2", time: { created: 20 } }, parts: [{ type: "tool", state: { output: { model: "hostile/not-readback" } } }] },
    ],
  })
  expect(readExportSessionMetadata(exported, "session-1")).toBeNull()
  expect(readExportSessionMetadata(exported, "different-session")).toBeNull()
})

test("export metadata reads session model and agent when sanitized messages omit them", () => {
  const exported = JSON.stringify({
    info: { id: "session-1", agent: "concord-implement", model: { id: "gpt-5.6-luna", providerID: "openai" } },
    messages: [
      { info: { id: "message-user", sessionID: "session-1", role: "user", time: { created: 10 } }, parts: [] },
      { info: { id: "message-assistant", sessionID: "session-1", role: "assistant", time: { created: 20 } }, parts: [] },
    ],
  })
  expect(readExportSessionMetadata(exported, "session-1")).toEqual({ readback_model: "openai/gpt-5.6-luna", readback_agent: "concord-implement", session_id: "session-1" })
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

// The host's own export shape: a session-level model object and agent, assistant
// messages that each carry providerID, modelID, agent, and time.created, and a
// turn the host ran as agent "compaction" inside the same session. The reader
// must keep accepting this shape unchanged, and the compaction turn must not
// displace the lane agent when it is not the latest assistant message.
test("export in the host's real shape is accepted with one model and the lane agent", () => {
  const sessionID = "ses_real_shape"
  const assistant = (id: string, created: number, agent: string) => ({
    info: { id, sessionID, role: "assistant", agent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created, completed: created + 10 } },
    parts: [{ type: "text", text: "ok" }],
  })
  const exported = JSON.stringify({
    info: { id: sessionID, model: { providerID: "openai", id: "gpt-5.6-luna", variant: "default" }, agent: "concord-implement" },
    messages: [
      { info: { id: "u-1", sessionID, role: "user", time: { created: 1 } }, parts: [{ type: "text", text: "go" }] },
      assistant("a-1", 10, "concord-implement"),
      assistant("a-2", 20, "compaction"),
      assistant("a-3", 30, "concord-implement"),
      assistant("a-4", 40, "concord-implement"),
    ],
  })
  const result = readExportSession(exported, sessionID)
  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.metadata).toEqual({ readback_model: "openai/gpt-5.6-luna", readback_agent: "concord-implement", session_id: sessionID })
  expect(result.export_bytes).toBe(Buffer.byteLength(exported))
  expect(result.export_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
  expect(readExportSessionMetadata(exported, sessionID)).toEqual(result.metadata)
})
