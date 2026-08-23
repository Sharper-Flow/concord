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
  expect(readRunSessionMetadata(output)).toEqual({ session_id: "session-1" })
  expect(readRunSessionMetadata(`${output}\n${runEvent("text", "session-2", { part: { type: "text", text: "x" } })}`)).toBeNull()
  // A run event missing its typed identity is still fatal.
  expect(readRunSessionMetadata(`${output}\n${JSON.stringify({ type: "text", part: { type: "text", text: "x" } })}`)).toBeNull()
  // Host plugins log their own JSON to the same stdout. Those lines are not run
  // events, carry no session identity, and are ignored rather than fatal.
  expect(readRunSessionMetadata(`${JSON.stringify({ ts: "2026-08-22T05:08:17.270Z", level: "info", plugin: "opencode-model-routing", event: "config.loaded" })}\n${output}`)).toEqual({ session_id: "session-1" })
  expect(readRunSessionMetadata(`${output}\n${JSON.stringify({ type: "unknown", sessionID: "session-2" })}`)).toEqual({ session_id: "session-1" })
  expect(readRunSessionMetadata(`${output}\nplain host chatter that is not JSON`)).toEqual({ session_id: "session-1" })
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
