// CD-0102 D2: the plugin entry must register the hook that binds an authorized
// dispatch to the next native Task call. Without the registration the window is
// unreachable and any Task call the model composes runs unbound.
import { describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { dispatchWindows, TASK_TOOL_ID } from "./dispatch-window"

const packet = {
  schema_version: "1.0" as const,
  attempt_id: "attempt-plugin",
  lane_id: "implement",
  lane_version: 1,
  lane_digest: "sha256:" + "b".repeat(64),
  work_id: "work-plugin",
  step_id: "step-1",
  inputs: { task: "do the bounded thing", context: "", constraints: [] },
}

describe("plugin entry registers the dispatch window hook", () => {
  test("binds the recorded packet onto the next task call", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
    }
    expect(typeof plugin["tool.execute.before"]).toBe("function")

    dispatchWindows().open("session-plugin", packet)
    const output = { args: { subagent_type: "general", prompt: "whatever I like", task_id: "old" } }
    await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-plugin", callID: "call-1" }, output)

    expect(output.args.subagent_type).toBe("concord-implement")
    expect(JSON.parse(output.args.prompt).attempt_id).toBe("attempt-plugin")
    expect(output.args.task_id).toBeUndefined()
    expect(dispatchWindows().has("session-plugin")).toBe(false)
  })

  test("refuses an unauthorized task call and leaves other tools untouched", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
    }
    const output = { args: { subagent_type: "general", prompt: "unbound" } }
    await expect(
      plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-none", callID: "call-2" }, output),
    ).rejects.toThrow(/no authorized dispatch/i)

    const other = { args: { filePath: "/x" } }
    await plugin["tool.execute.before"]({ tool: "read", sessionID: "session-none", callID: "call-3" }, other)
    expect(other.args.filePath).toBe("/x")
  })
})
