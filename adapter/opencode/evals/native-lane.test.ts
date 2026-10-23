import { expect, test } from "bun:test"
import NativeLaneProvider, { childRunStream } from "./native-lane.mjs"
import { readWorkerReportTexts } from "../worker-report-protocol.js"

const agent = "concord-review"
const child = "child"
const message = (who = agent, finish = "stop") => ({
  info: { role: "assistant", agent: who, sessionID: child, providerID: "fixture", modelID: "actual-model", finish },
  parts: [
    { id: "text", type: "text", sessionID: "child", text: "fixture-report" },
    { type: "tool", tool: "task", state: { status: "error" } },
    { type: "step-finish", reason: finish },
  ],
})

test("native eval streams exact child parts without the parent dispatch", () => {
  const { output, metadata } = childRunStream([message()], agent, child)
  expect(readWorkerReportTexts(output).texts).toEqual(["fixture-report"])
  expect(JSON.parse(output.split("\n")[1]).part.tool).toBe("task")
  expect(metadata).toEqual({ readback_model: "fixture/actual-model", readback_agent: agent, session_id: child })
})

test("native eval refuses absent, substituted, or incomplete child results", () => {
  for (const messages of [[], [message("build")], [message(agent, "length")]]) {
    expect(() => childRunStream(messages, agent, child)).toThrow()
  }
})

test("native eval reads one executing model across every child assistant", () => {
  const first = message(agent, "tool-calls")
  const last = message()
  const response = childRunStream([first, last], agent, child)
  expect(response.metadata.readback_model).toBe("fixture/actual-model")
  expect(response.output.split("\n")).toHaveLength(first.parts.length + last.parts.length)
})

for (const [label, override] of [
  ["missing provider", { providerID: undefined }],
  ["missing model", { modelID: undefined }],
  ["empty provider", { providerID: "" }],
  ["empty model", { modelID: "" }],
  ["untyped model", { modelID: 42 }],
  ["invalid model", { modelID: "not a model" }],
  ["missing child", { sessionID: undefined }],
  ["foreign child", { sessionID: "other-child" }],
  ["missing agent", { agent: undefined }],
  ["foreign agent", { agent: "concord-verify" }],
  ["error terminal", { error: { name: "SyntheticError" } }],
] as const) {
  test(`native eval refuses ${label} assistant readback`, () => {
    const bad = message()
    Object.assign(bad.info, override)
    expect(() => childRunStream([bad], agent, child)).toThrow()
    expect(() => childRunStream([bad, message()], agent, child)).toThrow()
  })
}

test("native eval refuses mixed executing models even when the last assistant is valid", () => {
  for (const override of [{ providerID: "other" }, { modelID: "other-model" }]) {
    const first = message(agent, "tool-calls")
    Object.assign(first.info, override)
    expect(() => childRunStream([first, message()], agent, child)).toThrow()
  }
})

test("native eval admits only registered lane targets", () => {
  expect(new NativeLaneProvider({ config: { lane: "review" } }).id()).toBe("native-concord-review")
  expect(() => new NativeLaneProvider({ config: { lane: "build" } })).toThrow()
})

test("native eval asks its parent to invoke Task instead of injecting an executable subtask", () => {
  const provider = new NativeLaneProvider({ config: { lane: "review" } })
  const part = provider.taskMessage("Evaluate the synthetic report.")
  expect(part.type).toBe("text")
  expect(part.text).toContain("Invoke the native Task tool exactly once")
  expect(JSON.parse(part.text.split("\n")[1])).toEqual({ subagent_type: agent, description: `Advisory ${agent} eval`, prompt: "Evaluate the synthetic report." })
})
