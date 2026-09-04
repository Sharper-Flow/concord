// CD-0102 D5: completion is the second adapter entry point. The host runs the
// worker between dispatch and completion, so the plugin's `tool.execute.after`
// hook is where a finished lane's result reaches completeWorkerAttempt. Without
// that wire the window opens, the worker runs, and no attempt is recorded, so
// accept_worker_result refuses with "worker attempt does not exist" (issue #781).
import { describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { agentLanes } from "./generated-agent-lanes"
import { completeDispatchedWorker, type LaneCompletionDeps } from "./lane_completion"
import type { CredentialStore } from "./credentials"

const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }
const lane = agentLanes.find((candidate) => candidate.id === "verify") ?? agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
const PACKET_DIGEST = "sha256:" + "d".repeat(64)
const WORKER_SESSION = "ses_worker"
const SESSION = "session-parent"

const packet = (): AgentLanePacket => ({
  schema_version: "1.0",
  attempt_id: "attempt-complete",
  lane_id: lane.id,
  lane_version: lane.version,
  lane_digest: lane.digest,
  work_id: "work-1",
  step_id: "repair",
  inputs: { task: "Verify the bounded fixture." },
})

const report = (status = "completed") => ({
  schema_version: "1.0",
  attempt_id: "attempt-complete",
  lane_id: lane.id,
  lane_version: lane.version,
  lane_digest: lane.digest,
  readback_model: READBACK_MODEL,
  status,
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `${obligation} discharged` })),
})

const taskWrap = (text: string, state = "completed") =>
  [`<task id="${WORKER_SESSION}" state="${state}">`, "<task_result>", text, "</task_result>", "</task>"].join("\n")

const exportedSession = (agent = `concord-${lane.id}`) => JSON.stringify({
  info: { id: WORKER_SESSION },
  messages: [
    { info: { id: "message-0", sessionID: WORKER_SESSION, role: "user", agent, time: { created: 0 } }, parts: [] },
    { info: { id: "message-1", sessionID: WORKER_SESSION, role: "assistant", agent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] },
  ],
})

// One runner answers the session export and records every CLI verb it sees.
const recordingRunner = (verbs: string[], agent?: string): DispatchRunner => ({
  async run(argv) {
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(agent), stderr: "" }
    verbs.push(argv[1])
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

const deps = (verbs: string[], windows: DispatchWindows, agent?: string): LaneCompletionDeps => ({
  windows,
  credentials: testCredentials,
  runner: recordingRunner(verbs, agent),
  concordBinary: "concord",
})

describe("completeDispatchedWorker", () => {
  test("records dispatch and completion for the in-flight attempt", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST)
    windows.bind(TASK_TOOL_ID, SESSION, {})
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
    expect(windows.takeInFlight(SESSION)).toBeNull()
    expect(output.output).toContain("<task_result>")
  })

  test("a failed report records worker-fail and surfaces the refusal on the tool output", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST)
    windows.bind(TASK_TOOL_ID, SESSION, {})
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report("failed"))), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(output.output).toContain("concord_attempt")
    expect(output.output).toContain("worker reported failure")
  })

  test("a substituted executor is refused and nothing is recorded as that lane's evidence", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST)
    windows.bind(TASK_TOOL_ID, SESSION, {})
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows, "general"))
    expect(verbs).toEqual([])
    expect(output.output).toContain("agent_identity_mismatch")
  })

  // The packet pins lane_version and lane_digest so completion binds to the
  // definition the dispatch authorized. An installed registry that drifts from
  // it — an upgrade between dispatch and completion — must refuse, because
  // signing the installed lane's version and digest onto the attempt records
  // the worker as having run a contract it never received.
  test("a lane that drifted from the dispatched version is refused", async () => {
    const windows = new DispatchWindows()
    const drifted = { ...packet(), lane_version: lane.version + 1 }
    windows.open(SESSION, drifted, PACKET_DIGEST)
    windows.bind(TASK_TOOL_ID, SESSION, {})
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(output.output).toContain("concord_attempt")
    expect(output.output).toContain("does not carry")
  })

  test("a lane whose digest drifted from the dispatched packet is refused", async () => {
    const windows = new DispatchWindows()
    const drifted = { ...packet(), lane_digest: "sha256:" + "e".repeat(64) }
    windows.open(SESSION, drifted, PACKET_DIGEST)
    windows.bind(TASK_TOOL_ID, SESSION, {})
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(output.output).toContain("does not carry")
  })

  // One authorization admits one result. A second call for the same session
  // finds nothing in flight, so a single dispatch cannot record two attempts.
  test("one authorization admits one result", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST)
    windows.bind(TASK_TOOL_ID, SESSION, {})
    const verbs: string[] = []
    const first = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    const second = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, first, deps(verbs, windows))
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-2", args: {} }, second, deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
    expect(second.output).toBe(taskWrap(JSON.stringify(report())))
  })

  test("ignores tools that are not the task tool and sessions with no in-flight attempt", async () => {
    const windows = new DispatchWindows()
    const verbs: string[] = []
    const read = { title: "read", output: "file contents", metadata: {} }
    await completeDispatchedWorker({ tool: "read", sessionID: SESSION, callID: "call-2", args: {} }, read, deps(verbs, windows))
    const unbound = { title: "task", output: taskWrap("prose"), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: "session-none", callID: "call-3", args: {} }, unbound, deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(read.output).toBe("file contents")
    expect(unbound.output).toBe(taskWrap("prose"))
  })
})

describe("plugin entry registers the completion hook", () => {
  test("tool.execute.after is registered and leaves an unbound call untouched", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.after": (i: { tool: string; sessionID: string; callID: string; args: any }, o: { title: string; output: string; metadata: any }) => Promise<void>
    }
    expect(typeof plugin["tool.execute.after"]).toBe("function")
    const output = { title: "task", output: taskWrap("prose"), metadata: {} }
    await plugin["tool.execute.after"]({ tool: TASK_TOOL_ID, sessionID: "session-plugin-unbound", callID: "call-1", args: {} }, output)
    expect(output.output).toBe(taskWrap("prose"))
  })
})
