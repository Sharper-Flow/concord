import { describe, expect, test } from "bun:test"
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"

const packet = {
  schema_version: "1.0" as const,
  attempt_id: "attempt-1",
  lane_id: "implement",
  lane_version: 1,
  lane_digest: "sha256:" + "a".repeat(64),
  work_id: "work-1",
  step_id: "step-1",
  inputs: { task: "do the bounded thing", context: "", constraints: [] },
}

describe("dispatch authorization window", () => {
  test("refuses a task call when no window is open for the session", () => {
    const windows = new DispatchWindows()
    const args = { subagent_type: "general", prompt: "whatever I like", description: "x" }
    expect(() => windows.bind(TASK_TOOL_ID, "session-a", args)).toThrow(/no authorized dispatch/i)
    expect(args.subagent_type).toBe("general")
  })

  test("replaces caller arguments with the recorded packet", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet)
    const args = { subagent_type: "general", prompt: "whatever I like", description: "x" }
    windows.bind(TASK_TOOL_ID, "session-a", args)
    expect(args.subagent_type).toBe("concord-implement")
    expect(JSON.parse(args.prompt).attempt_id).toBe("attempt-1")
  })

  test("consumes the window exactly once", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet)
    windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" })
    expect(() => windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "x", prompt: "y", description: "z" })).toThrow(
      /no authorized dispatch/i,
    )
  })

  test("scopes a window to the session that requested it", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet)
    expect(() => windows.bind(TASK_TOOL_ID, "session-b", { subagent_type: "x", prompt: "y", description: "z" })).toThrow(
      /no authorized dispatch/i,
    )
    const args = { subagent_type: "x", prompt: "y", description: "z" }
    windows.bind(TASK_TOOL_ID, "session-a", args)
    expect(args.subagent_type).toBe("concord-implement")
  })

  test("ignores tools other than the task tool", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet)
    const args = { subagent_type: "general", prompt: "untouched" }
    windows.bind("bash", "session-a", args)
    expect(args.prompt).toBe("untouched")
    const taskArgs = { subagent_type: "x", prompt: "y", description: "z" }
    windows.bind(TASK_TOOL_ID, "session-a", taskArgs)
    expect(taskArgs.subagent_type).toBe("concord-implement")
  })

  test("refuses a second open window for one session", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet)
    expect(() => windows.open("session-a", packet)).toThrow(/already holds an open dispatch/i)
  })

  test("refuses to resume a prior worker session", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet)
    const args = { subagent_type: "x", prompt: "y", description: "z", task_id: "session-prior" }
    windows.bind(TASK_TOOL_ID, "session-a", args)
    expect(args.task_id).toBeUndefined()
  })
})

describe("in-flight retention across the host task call", () => {
  test("bind moves the record to in flight and completion takes it once", () => {
    const windows = new DispatchWindows()
    windows.open("session-a", packet, "sha256:" + "c".repeat(64))
    windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "x" })
    expect(windows.has("session-a")).toBe(false)

    const record = windows.takeInFlight("session-a")
    expect(record?.packet.attempt_id).toBe("attempt-1")
    expect(record?.packetDigest).toBe("sha256:" + "c".repeat(64))
    // One authorization admits one result.
    expect(windows.takeInFlight("session-a")).toBeNull()
  })

  test("a session with no dispatch has nothing in flight", () => {
    expect(new DispatchWindows().takeInFlight("session-none")).toBeNull()
  })
})
