import { test, expect, afterAll, mock } from "bun:test"
import { readFileSync } from "node:fs"
import { agentLanes, agentUtilities } from "./generated-agent-lanes"
import { dispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { hostControlPlane } from "./move-session"
import { configureHostLease } from "./host-lease"

afterAll(() => configureHostLease({ reset: true }))

// The registry's Task-list rule: utilities are coordinator-native Tasks a
// coordinator picks by description; lanes are dispatch-only and never accept a
// direct coordinator Task. The plugin Task hook enforces the lane half at the
// only boundary the adapter owns — call admission — so a lane named outside an
// open dispatch window is refused with the correcting route instead of
// competing in the coordinator's Task list.
const fakeTool = (config: unknown) => config
mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const ConcordAdapterPlugin = (await import("./concord-plugin")).default
type Plugin = { "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void> }

const lane = agentLanes[0]
const packet = {
  schema_version: "1.0" as const,
  attempt_id: "attempt-lane-visibility",
  lane_id: lane.id,
  lane_version: lane.version,
  lane_digest: lane.digest,
  work_id: "work-lane-visibility",
  step_id: "step-1",
  inputs: { task: "prove the windowed lane Task passes admission", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, context: "", constraints: [] },
}

test("a lane Task with no open dispatch window is refused with the dispatch route", async () => {
  const plugin = (await ConcordAdapterPlugin()) as Plugin
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-lane-visibility", directory: process.cwd() }, response: new Response(null, { status: 200 }) }),
    post: async () => { throw new Error("Task admission cannot write host state") },
  })
  await expect(
    plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-lane-visibility", callID: "call-lane-no-window" }, { args: { subagent_type: `concord-${lane.id}`, description: "direct lane call" } }),
  ).rejects.toThrow(/dispatch_worker/)
  expect(dispatchWindows().has("session-lane-visibility")).toBe(false)
})

test("a Task call inside an open dispatch window still binds the recorded packet", async () => {
  const plugin = (await ConcordAdapterPlugin()) as Plugin
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-lane-window", directory: process.cwd() }, response: new Response(null, { status: 200 }) }),
    post: async () => { throw new Error("Task admission cannot write host state") },
  })
  dispatchWindows().open("session-lane-window", packet as never, "", process.cwd())
  const output = { args: { subagent_type: `concord-${lane.id}`, description: "windowed lane call", prompt: JSON.stringify(packet) } as Record<string, unknown> }
  await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-lane-window", callID: "call-lane-window" }, output)
  // The window owns the agent selection and the prompt: the bound call names
  // the recorded packet, whatever the caller named.
  expect(output.args.subagent_type).toBe(`concord-${packet.lane_id}`)
  expect(JSON.parse(output.args.prompt as string).attempt_id).toBe("attempt-lane-visibility")
  expect(dispatchWindows().has("session-lane-window")).toBe(false)
})

test("a utility Task from a session with no managed parent passes the hook unchanged", async () => {
  const plugin = (await ConcordAdapterPlugin()) as Plugin
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-lane-utility", directory: process.cwd() }, response: new Response(null, { status: 200 }) }),
    post: async () => { throw new Error("Task admission cannot write host state") },
  })
  const output = { args: { subagent_type: `concord-${agentUtilities[0].id}`, description: "utility call", prompt: "bounded question" } as Record<string, unknown> }
  await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-lane-utility", callID: "call-utility" }, output)
  expect(output.args.prompt).toBe("bounded question")
})

test("every registry utility generates as a subagent-only agent and every purpose states what and when", () => {
  for (const utility of agentUtilities) {
    const file = readFileSync(new URL(`../../.opencode/agents/concord-${utility.id}.md`, import.meta.url), "utf8")
    expect(file, utility.id).toContain("mode: subagent")
    expect(file, utility.id).toContain("Use ")
  }
  for (const lane of agentLanes) {
    expect(lane.purpose, lane.id).toContain("Dispatch")
  }
})
