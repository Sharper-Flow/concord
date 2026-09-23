// CD-0102 D5: completion is the second adapter entry point. The host runs the
// worker between dispatch and completion, so the plugin's `tool.execute.after`
// hook is where a finished lane's result reaches completeWorkerAttempt. Without
// that wire the window opens, the worker runs, and no attempt is recorded, so
// accept_worker_result refuses with "worker attempt does not exist" (issue #781).
import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { computeHostPromptProvenance, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { agentLanes } from "./generated-agent-lanes"
import { completeDispatchedWorker, failDispatchedWorker, type LaneCompletionDeps } from "./lane_completion"
import { hostControlPlane, SESSION_LIST_ROUTE, type SessionReader } from "./move-session"
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

const workPin = {
  work_id: "work-1",
  title: "Repair the adapter",
  linear_issue_key: "",
  version: 4,
  lifecycle: "in_progress",
  workflow_type: "workflow.break_fix",
  step: "repair",
  pending_operator_decision: null,
}

// The dispatch window owns attempt and lane identity; the report carries none
// of it.
const report = (status = "completed") => ({
  schema_version: "1.0",
  readback_model: READBACK_MODEL,
  status,
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `${obligation} discharged` })),
})

const taskWrap = (text: string, state = "completed") =>
  [`<task id="${WORKER_SESSION}" state="${state}">`, "<task_result>", text, "</task_result>", "</task>"].join("\n")

// CD-0102: an authorized worker session opens with the dispatch packet as its
// first user message, so the completion fixtures open that way.
const exportedSession = (agent = `concord-${lane.id}`, parentID: string | null = SESSION, opener: unknown = packet()) => JSON.stringify({
  info: { id: WORKER_SESSION, ...(parentID === null ? {} : { parentID }) },
  messages: [
    { info: { id: "message-0", sessionID: WORKER_SESSION, role: "user", agent, time: { created: 0 } }, parts: [{ type: "text", text: typeof opener === "string" ? opener : JSON.stringify(opener) }] },
    { info: { id: "message-1", sessionID: WORKER_SESSION, role: "assistant", agent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] },
  ],
})

// The session index supplies live-session evidence. The dispatch window owns
// the worker directory because the export runs with --sanitize.
const sessionIndex = (directory = "/claimed/worktree") =>
  JSON.stringify([{ id: "ses_other", directory: "/somewhere/else" }, { id: SESSION, directory }])

// The readback reads the worker session in process through the host session
// API; the reader serves the transcript the fixture export carries.
const failingReader = (status: number): SessionReader => ({
  async get() { return { data: undefined, response: new Response("{}", { status }) } },
  async messages() { return { data: undefined, response: new Response("{}", { status }) } },
})

const malformedReader = (): SessionReader => ({
  async get() { return { data: { id: WORKER_SESSION }, response: new Response("{}", { status: 200 }) } },
  async messages() { return { data: "{", response: new Response("[]", { status: 200 }) } },
})

const readerFor = (exported: string): SessionReader => {
  const parsed = JSON.parse(exported)
  return {
    async get() { return { data: parsed.info, response: new Response("{}", { status: 200 }) } },
    async messages(_sessionID: string, _limit: number, before: string | undefined) {
      return { data: before ? [] : parsed.messages, response: new Response("[]", { status: 200 }) }
    },
  }
}

// One runner answers the session export and index, and records every CLI verb.
const recordingRunner = (verbs: string[], agent?: string): DispatchRunner => ({
  async run(argv) {
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(agent), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
    verbs.push(argv[1])
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

const deps = (verbs: string[], windows: DispatchWindows, agent?: string): LaneCompletionDeps => ({
  windows,
  credentials: testCredentials,
  runner: recordingRunner(verbs, agent),
  sessionReader: readerFor(exportedSession(agent)),
  concordBinary: "concord",
})

describe("completeDispatchedWorker", () => {
  test("records dispatch and completion for the in-flight attempt", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
    expect(windows.takeInFlight(SESSION)).toBeNull()
    expect(output.output).toContain("<task_result>")
  })

  test("computes prompt provenance from the dispatch-window directory", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    let dispatchInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex("/different/worktree"), stderr: "" }
        if (argv[1] === "worker-dispatch") dispatchInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-provenance", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    const expected = await computeHostPromptProvenance(lane.id, process.cwd())
    expect((dispatchInput?.host_provenance as { digest?: string } | undefined)?.digest).toBe(expected.digest)
  })

  // The directory reaches the core from the dispatch window, never from the
  // sanitized export or the session index.
  const completionInputFor = async (directory: string, callID: string): Promise<Record<string, unknown> | undefined> => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    let completionInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(directory), stderr: "" }
        if (argv[1] === "worker-complete") completionInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID, args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    return completionInput
  }

  test("passes the dispatch-window worker directory to worker-complete", async () => {
    const completionInput = await completionInputFor("/claimed/worktree", "call-directory")
    expect(completionInput?.worker_directory).toBe(process.cwd())
  })

  // A subagent session need not appear in the session index. The dispatch
  // window still supplies the worker directory to the completion event.
  test("uses the dispatch-window directory despite parent observation", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    let completionInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        // Reality: the index holds the parent alone.
        if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: SESSION, directory: "/claimed/worktree" }]), stderr: "" }
        if (argv[1] === "worker-complete") completionInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-parent", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    expect(completionInput?.worker_directory).toBe(process.cwd())
  })

  test("uses the dispatch-window directory when neither session nor parent is listed", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    let completionInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(`concord-${lane.id}`, null), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "ses_other", directory: "/somewhere/else" }]), stderr: "" }
        if (argv[1] === "worker-complete") completionInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-orphan", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    expect(completionInput?.worker_directory).toBe(process.cwd())
  })

  test("ignores an invalid session-index directory", async () => {
    const completionInput = await completionInputFor(`[redacted:session-directory:${WORKER_SESSION}]`, "call-redacted")
    expect(completionInput?.worker_directory).toBe(process.cwd())
  })

  // An attempt the core refused to complete must not stay dispatched: an open
  // attempt blocks every later dispatch on that work item.
  test("a refused completion is closed with worker-fail", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    const verbs: string[] = []
    let failureInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
        verbs.push(argv[1])
        if (argv[1] === "worker-complete") return { exitCode: 1, stdout: "", stderr: "store: worker_dispatch: unauthorized_dispatch: refused" }
        if (argv[1] === "worker-fail") failureInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-refused", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    expect(verbs).toEqual(["worker-dispatch", "worker-complete", "worker-fail"])
    expect(failureInput?.failure_kind).toBe("abandoned")
    expect(failureInput?.observed_session_directories).toEqual([
      { session_ref: "ses_other", directory: "/somewhere/else" },
      { session_ref: SESSION, directory: "/claimed/worktree" },
    ])
    expect(output.output).toContain("worker-complete refused")
  })

  test("a refused completion stays open when host liveness is unreadable", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    const verbs: string[] = []
    const runner: DispatchRunner = {
      async run(argv) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 1, stdout: "", stderr: "session list unavailable" }
        verbs.push(argv[1])
        if (argv[1] === "worker-complete") return { exitCode: 1, stdout: "", stderr: "completion refused" }
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-unreadable", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
    expect(output.output).toContain("live host sessions could not be observed")
  })

  test("does not add a WorkPin state line to the lane report", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(output.output).not.toContain("◆ CONCORD WORK STATE")
  })

  test("a failed report records worker-fail and surfaces the refusal on the tool output", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report("failed"))), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(output.output).toContain("concord_attempt")
    expect(output.output).toContain("worker reported failure")
  })

  test("a substituted executor is refused and nothing is recorded as that lane's evidence", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows, "general"))
    expect(verbs).toEqual([])
    expect(output.output).toContain("agent_identity_mismatch")
  })

  // CD-0102 completion identity: a session that opened with caller-composed
  // prose never ran the authorized packet. Completion closes the attempt with
  // the typed refusal and records no completion evidence.
  test("a worker session that opened without the packet is refused", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
    const verbs: string[] = []
    const runner: DispatchRunner = {
      async run(argv) {
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
        verbs.push(argv[1])
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession(`concord-${lane.id}`, SESSION, "Run the task described above and report back.")),
      concordBinary: "concord",
    })
    expect(verbs).toEqual(["worker-dispatch"])
    expect(output.output).toContain("dispatched_packet_identity")
    expect(output.output).toContain("readback_refusal")
  })

  // The packet pins lane_version and lane_digest so completion binds to the
  // definition the dispatch authorized. An installed registry that drifts from
  // it — an upgrade between dispatch and completion — must refuse, because
  // signing the installed lane's version and digest onto the attempt records
  // the worker as having run a contract it never received.
  test("a lane that drifted from the dispatched version is refused", async () => {
    const windows = new DispatchWindows()
    const drifted = { ...packet(), lane_version: lane.version + 1 }
    windows.open(SESSION, drifted, PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
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
    windows.open(SESSION, drifted, PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
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
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, undefined, async () => process.cwd())
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

describe("host task failure", () => {
  const failedEvent = (callID = "call-cancel", sessionID = SESSION, metadata: unknown = { sessionId: WORKER_SESSION }) => ({
    type: "message.part.updated",
    properties: { part: { type: "tool", tool: "task", sessionID, callID,
      state: { status: "error", error: "Task cancelled", metadata } } },
  })

  for (const fault of ["export-command", "malformed-export"]) {
    test(`a persisted born-failed ${fault} releases settlement`, async () => {
      const windows = new DispatchWindows()
      windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
      await windows.bind(TASK_TOOL_ID, SESSION, {}, "call-cancel", async () => process.cwd())
      const verbs: string[] = []
      const options = deps(verbs, windows)
      options.sessionReader = fault === "export-command" ? failingReader(503) : malformedReader()
      options.evidenceRunner = { async run(argv, raw) {
        verbs.push(argv[1])
        const input = JSON.parse(raw)
        expect(input.terminal).toBe("failed")
        expect(input.readback_model).toBe("")
        return { exitCode: 0, stdout: "", stderr: "" }
      } }
      await failDispatchedWorker(failedEvent(), options)
      expect(verbs).toEqual(["worker-dispatch"])
      expect(windows.inFlight(SESSION, "call-cancel")).toBeNull()
      expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
    })
  }

  test("a cancelled task records failure once using exported identity", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, "call-cancel", async () => process.cwd())
    const verbs: string[] = []
    const result = await failDispatchedWorker(failedEvent(), deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(result?.error?.message).toContain("Task cancelled")
    await failDispatchedWorker(failedEvent(), deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  })

  test("a refused born-failed write retains settlement without retry", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, "call-cancel", async () => process.cwd())
    const verbs: string[] = []
    const options = deps(verbs, windows)
    options.sessionReader = failingReader(503)
    options.evidenceRunner = { async run(argv) {
      verbs.push(argv[1])
      return { exitCode: 1, stdout: "", stderr: "write unavailable" }
    } }
    await failDispatchedWorker(failedEvent(), options)
    await failDispatchedWorker(failedEvent(), options)
    expect(verbs).toEqual(["worker-dispatch"])
    expect(windows.inFlight(SESSION, "call-cancel")).not.toBeNull()
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).toThrow()
    // The in-flight refusal names worker_abandon as the recovery route, so the
    // refused write releases its claim and that route reaches the retained
    // record. A held claim would wedge the session against its own remedy.
    expect(windows.releaseRetained(SESSION, packet().attempt_id, lane.id)).toBe(true)
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
  })

  test("foreign calls and unbound sessions cannot consume an attempt", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, "call-cancel", async () => process.cwd())
    const verbs: string[] = []
    await failDispatchedWorker(failedEvent("foreign"), deps(verbs, windows))
    await failDispatchedWorker(failedEvent("call-cancel", "other-session"), deps(verbs, windows))
    expect(verbs).toEqual([])
    await failDispatchedWorker(failedEvent(), deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  })

  test("missing child identity refuses without inventing model evidence", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, "call-cancel", async () => process.cwd())
    const verbs: string[] = []
    const result = await failDispatchedWorker(failedEvent("call-cancel", SESSION, {}), deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(result?.error?.recovery_action).toBe("reconcile_operation")
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).toThrow()
    // The refused abandonment names the reconcile route, and the retained
    // record releases only to that attempt's identity.
    expect(windows.releaseRetained(SESSION, "attempt-other", lane.id)).toBe(false)
    expect(windows.releaseRetained(SESSION, packet().attempt_id, lane.id)).toBe(true)
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
  })

  test("a substituted executor still fails identity verification", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, "call-cancel", async () => process.cwd())
    const verbs: string[] = []
    const result = await failDispatchedWorker(failedEvent(), deps(verbs, windows, "general"))
    expect(verbs).toEqual([])
    expect(result?.error?.kind).toBe("agent_identity_mismatch")
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).toThrow()
  })

  test("failed persistence retains the authorization and does not automatically retry", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, "call-cancel", async () => process.cwd())
    const verbs: string[] = []
    const options = deps(verbs, windows)
    options.evidenceRunner = { async run(argv) {
      verbs.push(argv[1])
      return { exitCode: 1, stdout: "", stderr: "unavailable" }
    } }
    await failDispatchedWorker(failedEvent(), options)
    await failDispatchedWorker(failedEvent(), options)
    // The session-list observation rides the same CLI runner; only worker
    // evidence verbs assert here.
    expect(verbs.filter((verb) => verb.startsWith("worker-"))).toEqual(["worker-dispatch"])
    expect(windows.inFlight(SESSION, "call-cancel")).not.toBeNull()
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).toThrow()
  })
})

// A spawn that dies before any result or error part fires neither the
// completion hook nor an error part: the host publishes session.error on the
// parent session and leaves the tool part running. That event is the settle
// trigger, and the abandonment route is the settle path because the record
// names no child session.
describe("spawn failure without a part event", () => {
  afterEach(() => hostControlPlane().bind(undefined))

  const spawnFailureEvent = (sessionID = SESSION, error: unknown = { name: "UnknownAgentError", data: { message: 'Agent not found: "concord-verify"' } }) => ({
    type: "session.error",
    properties: { sessionID, error },
  })

  const bindSessionList = (status = 200) => {
    hostControlPlane().bind({
      post: async () => ({ response: new Response(null, { status: 404 }) }),
      get: async ({ url }) => {
        if (url !== SESSION_LIST_ROUTE) return { response: new Response(null, { status: 404 }) }
        if (status !== 200) return { response: new Response("host is unwell", { status }) }
        return { data: [{ id: "ses_other", directory: "/somewhere/else" }], response: new Response(null, { status: 200 }) }
      },
    })
  }

  const withInFlightAttempt = async (windows: DispatchWindows, callID: string) => {
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, {}, callID, async () => process.cwd())
  }

  test("settles the attempt by abandonment and releases the session", async () => {
    bindSessionList()
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-spawn")
    const verbs: string[] = []
    let abandonInput: Record<string, unknown> | undefined
    const options = deps(verbs, windows)
    options.evidenceRunner = { async run(argv, raw) {
      verbs.push(argv[1])
      if (argv[1] === "worker-abandon") {
        abandonInput = JSON.parse(raw) as Record<string, unknown>
        expect((abandonInput.assertion as Record<string, unknown>).failure_kind).toBe("abandoned")
        expect((abandonInput.assertion as Record<string, unknown>).readback_model).toBe("")
        expect(abandonInput.detail).toContain("spawn failed before any result or error part")
        expect(abandonInput.detail).toContain('Agent not found: "concord-verify"')
      }
      return { exitCode: 0, stdout: "", stderr: "" }
    } }
    const result = await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual(["worker-abandon"])
    expect(result?.error?.retry_safe).toBe(false)
    expect(result?.error?.message).toContain("spawn failed before any result or error part")
    expect(windows.inFlightAttempt(SESSION)).toBeNull()
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
    windows.close(SESSION)
  })

  test("a refused abandonment records why and keeps the release route live", async () => {
    bindSessionList()
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-spawn")
    const verbs: string[] = []
    const options = deps(verbs, windows)
    options.runner = { async run() { return { exitCode: 1, stdout: "", stderr: "write unavailable" } } }
    const result = await failDispatchedWorker(spawnFailureEvent(), options)
    expect(result?.error?.message).toContain("worker attempt remains open because abandonment could not be recorded")
    expect(windows.inFlightAttempt(SESSION)).not.toBeNull()
    // The settlement claim released with the record retained, so the named
    // worker_abandon route still releases the retained attempt.
    expect(windows.releaseRetained(SESSION, packet().attempt_id, lane.id)).toBe(true)
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
    windows.close(SESSION)
  })

  test("unreadable host liveness retains the attempt with the recorded reason", async () => {
    bindSessionList(500)
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-spawn")
    const verbs: string[] = []
    const result = await failDispatchedWorker(spawnFailureEvent(), deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(result?.error?.message).toContain("live host sessions could not be observed")
    expect(windows.inFlightAttempt(SESSION)).not.toBeNull()
    expect(windows.releaseRetained(SESSION, packet().attempt_id, lane.id)).toBe(true)
    windows.close(SESSION)
  })

  test("a session with no in-flight attempt is ignored", async () => {
    bindSessionList()
    const windows = new DispatchWindows()
    const verbs: string[] = []
    const result = await failDispatchedWorker(spawnFailureEvent(), deps(verbs, windows))
    expect(result).toBeNull()
    expect(verbs).toEqual([])
  })

  // An aborted worker is cancellation, not a spawn failure: the host moves the
  // tool part to an error state and the part-event path settles it with the
  // child session identity.
  test("an aborted spawn is left to the part-event path", async () => {
    bindSessionList()
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-spawn")
    const verbs: string[] = []
    const result = await failDispatchedWorker(spawnFailureEvent(SESSION, { name: "MessageAbortedError" }), deps(verbs, windows))
    expect(result).toBeNull()
    expect(verbs).toEqual([])
    expect(windows.inFlightAttempt(SESSION)).not.toBeNull()
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

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file leaves the lease state clean.
afterAll(() => configureHostLease({ reset: true }))
