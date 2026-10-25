import { expect, test } from "bun:test"
import type { AgentLanePacket, DispatchRunner } from "./dispatch"
import { DispatchWindows, serializeLanePacket } from "./dispatch-window"
import { agentLanes } from "./generated-agent-lanes"
import { createWorkerCapabilityGuard } from "./lane_capabilities"

async function architectureReviewGuard(stepID: string) {
  const lane = agentLanes.find((entry) => entry.id === "review")!
  const directory = process.cwd()
  const subject = "a".repeat(40)
  const packet: AgentLanePacket = {
    schema_version: "1.0", attempt_id: "architecture-review", work_id: "architecture-work", step_id: stepID,
    lane_id: lane.id, lane_digest: lane.digest,
    inputs: {
      task: "Review the recorded architecture against accepted criteria",
      binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "contract_findings" },
      work_context: { source_event_frontier: 1, required_reading: [], findings: [], domain_groups: [], subject_commit: subject },
    },
  }
  const windows = new DispatchWindows()
  windows.open("parent", packet, "sha256:" + "b".repeat(64), directory)
  await windows.bind("task", "parent", {
    subagent_type: "concord-review", prompt: packet.attempt_id, description: "bounded architecture review",
  }, "architecture-task", async () => directory, directory)
  const subjectRunner: DispatchRunner = {
    run: async (argv) => {
      if (argv.includes("rev-parse")) return { exitCode: 0, stdout: subject + "\n", stderr: "", signal_state: null }
      if (argv.includes("status")) return { exitCode: 0, stdout: "", stderr: "", signal_state: null }
      throw new Error("unexpected subject probe: " + argv.join(" "))
    },
  }
  const reader = {
    get: async (id: string) => ({
      data: { id, directory, agent: id === "parent" ? "build" : "concord-review", ...(id === "parent" ? {} : { parentID: "parent" }) },
      response: new Response(null, { status: 200 }),
    }),
    messages: async (id: string) => ({
      data: [{ info: { sessionID: id, role: "user", agent: "concord-review" }, parts: [{ type: "text", text: serializeLanePacket(packet) }] }],
      response: new Response(null, { status: 200 }),
    }),
  }
  const guard = createWorkerCapabilityGuard({ windows, subjectRunner, sessions: {
    sessionReader: () => reader,
    taskScope: async () => "managed",
    hasManagedParent: async () => true,
  } })
  return (tool: string) => guard.before({ tool, sessionID: "child" }, { args: {} })
}

for (const tool of ["edit", "write", "patch", "apply_patch", "morph_edit", "bash"]) {
  test(`architecture-purpose Review denies ${tool} before effect`, async () => {
    const before = await architectureReviewGuard("design")
    await expect(before(tool)).rejects.toThrow("does not grant")
  })
}

test("architecture-purpose Review retains pinned reads and recorded checks", async () => {
  const before = await architectureReviewGuard("design")
  for (const tool of ["read", "concord_worker_read", "concord_worker_diff", "concord_worker_check"]) {
    await before(tool)
  }
})

test("accumulated-solution Review retains its scoped corrective edit authority", async () => {
  const before = await architectureReviewGuard("execution")
  for (const tool of ["edit", "write", "patch", "apply_patch", "morph_edit", "bash"]) {
    await before(tool)
  }
})
