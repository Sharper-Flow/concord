// CD-0102 D5: the host runs the worker between dispatch and completion, so the
// adapter reaches the finished attempt through the plugin's `tool.execute.after`
// hook. These tests drive the whole route the host drives — bind the window,
// then hand back the task tool's rendered output — and assert that the attempt
// reaches durable evidence. A completion that records nothing leaves the
// dispatching step unable to advance, which is the failure this covers.
import { expect, test } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { completeDispatchedTaskCall, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import type { CredentialStore } from "./credentials"

const lane = agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
const PACKET_DIGEST = "sha256:" + "d".repeat(64)
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

const packet = (): AgentLanePacket => ({
  schema_version: "1.0", attempt_id: "attempt-1", lane_id: lane.id, lane_version: lane.version,
  lane_digest: lane.digest, work_id: "work-1", step_id: "step-1", inputs: { task: "bounded task" },
})

const laneReport = () => ({
  schema_version: "1.0", attempt_id: "attempt-1", lane_id: lane.id, lane_version: lane.version,
  lane_digest: lane.digest, readback_model: READBACK_MODEL, status: "completed",
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
})

const exportedSession = JSON.stringify({
  info: { id: "session-1" },
  messages: [{ info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: `concord-${lane.id}`, providerID: READBACK_MODEL.split("/")[0], modelID: READBACK_MODEL.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] }],
})

// The host renders a finished worker with this wrapper, and the identifier in it
// is the adapter's only route to the executing model and agent.
const taskOutput = (doc: unknown) =>
  ['<task id="session-1" state="completed">', "<task_result>", JSON.stringify(doc), "</task_result>", "</task>"].join("\n")

const exportRunner: DispatchRunner = {
  async run(argv) {
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession, stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
}

function evidenceCollector(recorded: Record<string, unknown>[]): DispatchRunner {
  return {
    async run(argv, input) {
      recorded.push({ command: argv[1], request: JSON.parse(input) })
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
}

function boundWindows(): DispatchWindows {
  const windows = new DispatchWindows()
  windows.open("session-a", packet(), PACKET_DIGEST)
  windows.bind(TASK_TOOL_ID, "session-a", { subagent_type: "general", prompt: "whatever the model composed" })
  return windows
}

test("a bound task call records the dispatched attempt and its completion", async () => {
  const recorded: Record<string, unknown>[] = []
  const output = { output: taskOutput(laneReport()) }
  await completeDispatchedTaskCall(TASK_TOOL_ID, "session-a", output, {
    windows: boundWindows(), credentials: testCredentials, readbackRunner: exportRunner, evidenceRunner: evidenceCollector(recorded),
  })
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-complete"])
  for (const entry of recorded) {
    expect((entry.request as any).attempt_id).toBe("attempt-1")
    expect((entry.request as any).work_id).toBe("work-1")
  }
})

test("the recorded attempt is the one the window bound, not one the model named", async () => {
  const recorded: Record<string, unknown>[] = []
  const output = { output: taskOutput({ ...laneReport(), attempt_id: "attempt-the-model-invented" }) }
  await completeDispatchedTaskCall(TASK_TOOL_ID, "session-a", output, {
    windows: boundWindows(), credentials: testCredentials, readbackRunner: exportRunner, evidenceRunner: evidenceCollector(recorded),
  })
  // A report bound to another packet is not a completion (CD-0056 D7).
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-fail"])
  expect((recorded[1].request as any).failure_kind).toBe("invalid_report")
  expect((recorded[1].request as any).attempt_id).toBe("attempt-1")
})

test("a refused completion surfaces on the task result", async () => {
  const failingExport: DispatchRunner = { async run() { return { exitCode: 1, stdout: "", stderr: "session export failed" } } }
  const original = taskOutput(laneReport())
  const output = { output: original }
  await completeDispatchedTaskCall(TASK_TOOL_ID, "session-a", output, {
    windows: boundWindows(), credentials: testCredentials, readbackRunner: failingExport, evidenceRunner: evidenceCollector([]),
  })
  expect(output.output).not.toBe(original)
  expect(output.output).toContain("session export failed")
})

test("one authorization admits one result", async () => {
  const recorded: Record<string, unknown>[] = []
  const windows = boundWindows()
  const first = { output: taskOutput(laneReport()) }
  const second = { output: taskOutput(laneReport()) }
  const options = { windows, credentials: testCredentials, readbackRunner: exportRunner, evidenceRunner: evidenceCollector(recorded) }
  await completeDispatchedTaskCall(TASK_TOOL_ID, "session-a", first, options)
  await completeDispatchedTaskCall(TASK_TOOL_ID, "session-a", second, options)
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-complete"])
})

test("a call the window never bound is left alone", async () => {
  const recorded: Record<string, unknown>[] = []
  const untouched = { output: "ordinary tool output" }
  await completeDispatchedTaskCall(TASK_TOOL_ID, "session-none", untouched, {
    windows: new DispatchWindows(), credentials: testCredentials, readbackRunner: exportRunner, evidenceRunner: evidenceCollector(recorded),
  })
  expect(untouched.output).toBe("ordinary tool output")
  expect(recorded).toHaveLength(0)
})

test("a tool other than the task tool is left alone", async () => {
  const recorded: Record<string, unknown>[] = []
  const windows = boundWindows()
  const untouched = { output: "bash output" }
  await completeDispatchedTaskCall("bash", "session-a", untouched, {
    windows, credentials: testCredentials, readbackRunner: exportRunner, evidenceRunner: evidenceCollector(recorded),
  })
  expect(untouched.output).toBe("bash output")
  expect(recorded).toHaveLength(0)
  // The attempt stays in flight for the task call it belongs to.
  expect(windows.takeInFlight("session-a")?.packet.attempt_id).toBe("attempt-1")
})
