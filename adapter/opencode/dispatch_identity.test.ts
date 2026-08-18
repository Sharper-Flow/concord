import { expect, test } from "bun:test"
import { readExportSessionMetadata, readRunSessionMetadata } from "./dispatch"

const runEvent = (type: string, sessionID: string, extra: Record<string, unknown> = {}) => JSON.stringify({
  type,
  timestamp: 1,
  sessionID,
  ...extra,
})

test("run metadata accepts only one typed session identity", () => {
  const output = [
    runEvent("step_start", "session-1", { part: { type: "step-start", model: "incidental/not-readback" } }),
    runEvent("step_finish", "session-1", { part: { type: "step-finish", reason: "stop" } }),
  ].join("\n")
  expect(readRunSessionMetadata(output)).toEqual({ session_id: "session-1", fallback_reason: null })
  expect(readRunSessionMetadata(`${output}\n${runEvent("text", "session-2", { part: { type: "text", text: "x" } })}`)).toBeNull()
  expect(readRunSessionMetadata(`${output}\n${JSON.stringify({ type: "unknown", sessionID: "session-1" })}`)).toBeNull()
})

test("export metadata reads the latest typed assistant and ignores nested model-shaped content", () => {
  const exported = JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", providerID: "openai", modelID: "preferred", time: { created: 10 } }, parts: [] },
      { info: { id: "message-user", sessionID: "session-1", role: "user", time: { created: 15 } }, parts: [] },
      { info: { id: "message-2", sessionID: "session-1", role: "assistant", providerID: "zai-coding-plan", modelID: "glm-5.2", time: { created: 20 } }, parts: [{ type: "tool", state: { output: { model: "hostile/not-readback" } } }] },
    ],
  })
  expect(readExportSessionMetadata(exported, "session-1")).toEqual({ readback_model: "zai-coding-plan/glm-5.2", session_id: "session-1" })
  expect(readExportSessionMetadata(exported, "different-session")).toBeNull()
})

test("export metadata rejects ambiguous duplicate assistant identity", () => {
  const exported = JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", providerID: "openai", modelID: "preferred", time: { created: 10 } }, parts: [] },
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", providerID: "zai-coding-plan", modelID: "glm-5.2", time: { created: 10 } }, parts: [] },
    ],
  })
  expect(readExportSessionMetadata(exported, "session-1")).toBeNull()
})
