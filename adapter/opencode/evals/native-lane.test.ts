import { expect, test } from "bun:test"
import NativeLaneProvider, { childRunStream } from "./native-lane.mjs"
import { readWorkerReportTexts } from "../worker-report-protocol.js"

const agent = "concord-review"
const message = (who = agent, finish = "stop") => ({
  info: { role: "assistant", agent: who, sessionID: "child", finish },
  parts: [
    { id: "text", type: "text", sessionID: "child", text: "fixture-report" },
    { type: "tool", tool: "task", state: { status: "error" } },
    { type: "step-finish", reason: finish },
  ],
})

test("native eval streams exact child parts without the parent dispatch", () => {
  const output = childRunStream([message()], agent)
  expect(readWorkerReportTexts(output).texts).toEqual(["fixture-report"])
  expect(JSON.parse(output.split("\n")[1]).part.tool).toBe("task")
})

test("native eval refuses absent, substituted, or incomplete child results", () => {
  for (const messages of [[], [message("build")], [message(agent, "length")]]) {
    expect(() => childRunStream(messages, agent)).toThrow()
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
