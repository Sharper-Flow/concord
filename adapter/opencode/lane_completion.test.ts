// CD-0102 D5: completion is the second adapter entry point. The host runs the
// worker between dispatch and completion, so the plugin's `tool.execute.after`
// hook is where a finished lane's result reaches completeWorkerAttempt. Without
// that wire the window opens, the worker runs, and no attempt is recorded, so
// accept_worker_result refuses with "worker attempt does not exist" (issue #781).
import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { reviewBlockFixture, reviewFindingFixture } from "./review-test-fixture"
import { createHash } from "node:crypto"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { computeHostPromptProvenance, type AgentLanePacket, type DispatchRunner, type HostProvenance } from "./dispatch"
import { DispatchWindows, serializeLanePacket, TASK_TOOL_ID } from "./dispatch-window"
import { agentLanes } from "./generated-agent-lanes"
import { cancellationFailureObservation, completeDispatchedWorker, failDispatchedWorker, HostFailureObservations, spawnFailureObservation, type LaneCompletionDeps } from "./lane_completion"
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
  lane_digest: lane.digest,
  work_id: "work-1",
  step_id: "repair",
  inputs: { task: "Verify the bounded fixture.", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "exit_codes" } },
})

// The exact native Task selector the coordinator issues after authorization
// (CD-0102 D2): the window's recorded lane, its attempt reference, and no
// other field. The hook matches it against the open window before any
// mutation, then overwrites the caller's arguments from the recorded packet.
const windowArgs = () => ({ subagent_type: `concord-${lane.id}`, prompt: "attempt-complete", description: "bounded worker" })

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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
    expect(windows.takeInFlight(SESSION)).toBeNull()
    expect(output.output).toContain("<task_result>")
  })

  // The optional comparison is informational: the attempt readback holds it
  // when the worker reported one and stays free of the field otherwise.
  test("the attempt readback carries a reported base_comparison and omits an unreported one", async () => {
    const reported = { checks: [{ command: "go test ./...", branch_result: "pass", base_result: "fail" }] }
    const attemptSummary = async (extra: Record<string, unknown> = {}): Promise<Record<string, unknown>> => {
      const windows = new DispatchWindows()
      windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
      await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
      const verbs: string[] = []
      const output = { title: "verify lane", output: taskWrap(JSON.stringify({ ...report(), ...extra })), metadata: {} }
      await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-attempt", args: {} }, output, deps(verbs, windows))
      expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
      const element = output.output.split("<concord_attempt>")[1]?.split("</concord_attempt>")[0]
      return JSON.parse(element!.trim()) as Record<string, unknown>
    }
    expect((await attemptSummary({ base_comparison: reported })).base_comparison).toEqual(reported)
    expect("base_comparison" in (await attemptSummary())).toBe(false)
  })

  // CD-0197: the attempt readback carries a completed review report's typed
  // disposition and per-severity counts, and stays free of the field when the
  // report carried none. The disposition is report content only: it records no
  // workflow verdict.
  test("the attempt readback carries a reported disposition and per-severity counts", async () => {
    const reported = reviewBlockFixture({ findings: [reviewFindingFixture({ detail: "a finding" }), reviewFindingFixture({ finding_id: "finding:second", severity: "P3", confidence: "low", detail: "another" })] })
    const attemptSummary = async (extra: Record<string, unknown> = {}): Promise<Record<string, unknown>> => {
      const windows = new DispatchWindows()
      windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
      await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
      const verbs: string[] = []
      const output = { title: "verify lane", output: taskWrap(JSON.stringify({ ...report(), ...extra })), metadata: {} }
      await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-review", args: {} }, output, deps(verbs, windows))
      expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
      const element = output.output.split("<concord_attempt>")[1]?.split("</concord_attempt>")[0]
      return JSON.parse(element!.trim()) as Record<string, unknown>
    }
    expect((await attemptSummary({ review: reported })).review).toEqual({ disposition: "correction_required", findings: { P0: 0, P1: 1, P2: 0, P3: 1 } })
    expect("review" in (await attemptSummary())).toBe(false)
  })

  test("computes prompt provenance from the dispatch-window directory", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
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

  // CON-821: completion records the manifest the window captured at dispatch,
  // verbatim, instead of recomputing one after the worker ran.
  test("completion forwards the manifest the window captured", async () => {
    const captured: HostProvenance = { digest: "sha256:" + "f".repeat(64), sources: [{ kind: "unenumerated", path: "synthetic-captured-surface" }] }
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd(), undefined, captured)
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    let dispatchInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
        if (argv[1] === "worker-dispatch") dispatchInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-captured", args: {} }, output, {
      windows, credentials: testCredentials, runner, sessionReader: readerFor(exportedSession()), concordBinary: "concord",
    })
    expect(dispatchInput?.host_provenance).toEqual(captured)
  })

  test("passes the dispatch-window worker directory to worker-complete", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    let completionInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "worker-complete") completionInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-directory", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    expect(completionInput?.worker_directory).toBe(process.cwd())
  })

  // An attempt the core refused to complete must not stay dispatched: an open
  // attempt blocks every later dispatch on that work item. The close is
  // invalid_report carrying the refusal text, not abandoned, so the abandoned
  // liveness gate (CD-0178 D3) cannot block a lane that ran inside the live
  // coordinator process.
  test("a precommit invalid report is closed with worker-fail", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    let failureInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
        verbs.push(argv[1])
        if (argv[1] === "worker-complete") return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: null, error: { kind: "invalid_payload", operation: "fold_event", retry_safe: false, effect_state: "none", message: "invalid report evidence" } }), stderr: "invalid report evidence" }
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
    expect(failureInput?.failure_kind).toBe("invalid_report")
    expect(failureInput?.detail).toBe("worker-complete refused: invalid report evidence")
    // worker-fail carries no host session observation; the close names no
    // observed sessions (CD-0178 D3).
    expect(failureInput?.observed_session_directories).toBeUndefined()
    expect(output.output).toContain("worker-complete refused")
    expect(output.output).toContain('"kind":"invalid_report"')
    // CD-0044 D4 terminal finality: the worker-fail close that landed is the
    // attempt's terminal outcome, so the window record releases and the next
    // dispatch can open instead of retaining a settled attempt.
    expect(windows.inFlight(SESSION, "call-refused")).toBeNull()
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
  })

  // The other edge of terminal finality: a close whose worker-fail write was
  // refused recorded nothing terminal, so the record stays retained for the
  // worker_reconcile recovery the refusal names. A false release here would
  // strand a dispatched attempt with no settle left to wait for.
  test("a refused worker-fail close retains the in-flight record for recovery", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const runner: DispatchRunner = {
      async run(argv) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
        verbs.push(argv[1])
        if (argv[1] === "worker-complete") return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: null, error: { kind: "invalid_payload", operation: "fold_event", retry_safe: false, effect_state: "none", message: "invalid report evidence" } }), stderr: "invalid report evidence" }
        if (argv[1] === "worker-fail") return { exitCode: 1, stdout: "", stderr: "write unavailable" }
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-refused-close", args: {} }, output, {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    expect(verbs).toEqual(["worker-dispatch", "worker-complete", "worker-fail"])
    expect(output.output).toContain("the attempt stays open because its failure could not be recorded: write unavailable")
    expect(windows.inFlight(SESSION, "call-refused-close")).not.toBeNull()
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).toThrow()
    // The refusal names the recovery route, and the retained record releases
    // only to that attempt's identity.
    expect(windows.releaseRetained(SESSION, packet().attempt_id, lane.id)).toBe(true)
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
  })

  // CD-0178 D3: completion never reads host liveness, so a refused
  // completion closes with worker-fail whatever the host session index
  // serves — even a failing one.
  test("a precommit invalid report is closed with worker-fail without reading host liveness", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const runner: DispatchRunner = {
      async run(argv) {
        if (argv[1] === "session") throw new Error("the session index must not be read")
        verbs.push(argv[1])
        if (argv[1] === "worker-complete") return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: null, error: { kind: "invalid_payload", operation: "fold_event", retry_safe: false, effect_state: "none", message: "completion refused" } }), stderr: "completion refused" }
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
    expect(verbs).toEqual(["worker-dispatch", "worker-complete", "worker-fail"])
    expect(output.output).toContain("worker-complete refused")
  })

  test("does not add a WorkPin state line to the lane report", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(output.output).not.toContain("◆ CONCORD WORK STATE")
  })

  test("a failed report records worker-fail and surfaces the refusal on the tool output", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
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

  test("a lane id that differs from the current registry is refused", async () => {
    const windows = new DispatchWindows()
    const drifted = { ...packet(), lane_id: "unknown" }
    windows.open(SESSION, drifted, PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, { ...windowArgs(), subagent_type: "concord-unknown" }, undefined, async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(output.output).toContain("concord_attempt")
    expect(output.output).toContain("content revalidation")
  })

  test("a lane whose digest drifted from the dispatched packet is refused", async () => {
    const windows = new DispatchWindows()
    const drifted = { ...packet(), lane_digest: "sha256:" + "e".repeat(64) }
    windows.open(SESSION, drifted, PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const output = { title: "verify lane", output: taskWrap(JSON.stringify(report())), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "call-1", args: {} }, output, deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(output.output).toContain("content revalidation")
  })

  // One authorization admits one result. A second call for the same session
  // finds nothing in flight, so a single dispatch cannot record two attempts.
  test("one authorization admits one result", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), undefined, async () => process.cwd(), process.cwd())
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
  const failedEvent = (callID = "call-cancel", sessionID = SESSION, metadata: unknown = { sessionId: WORKER_SESSION }, error = "Task cancelled") => ({
    type: "message.part.updated",
    properties: { part: { type: "tool", tool: "task", sessionID, callID,
      state: { status: "error", error, metadata, input: { prompt: packet().attempt_id } } } },
  })

  test("a native after-hook with no result leaves settlement to the terminal part event", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "native-part", async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION, callID: "native-part", args: {} }, undefined, deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(windows.claimSettlement(SESSION, "native-part")).not.toBeNull()
  })

  test("a native terminal part matches its before-hook part identity, not its separate call identity", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "native-part", async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const event = failedEvent("native-tool-call")
    const foreignEvent = { ...event, properties: { part: { ...event.properties.part, id: "another-part" } } }
    expect(await failDispatchedWorker(foreignEvent, deps(verbs, windows))).toBeNull()
    expect(verbs).toEqual([])
    const nativeEvent = { ...event, properties: { part: { ...event.properties.part, id: "native-part" } } }
    const result = await failDispatchedWorker(nativeEvent, deps(verbs, windows))
    expect(result?.error?.message).toContain("Task cancelled")
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(windows.inFlightAttempt(SESSION)).toBeNull()
    await failDispatchedWorker(nativeEvent, deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  })

  for (const fault of ["export-command", "malformed-export"]) {
    test(`a persisted born-failed ${fault} releases settlement`, async () => {
      const windows = new DispatchWindows()
      windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
      await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const result = await failDispatchedWorker(failedEvent(), deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(result?.error?.message).toContain("Task cancelled")
    await failDispatchedWorker(failedEvent(), deps(verbs, windows))
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  })

  // CON-821: a born-failed record carries the captured manifest too.
  test("a cancelled task records failure with the manifest the window captured", async () => {
    const captured: HostProvenance = { digest: "sha256:" + "f".repeat(64), sources: [{ kind: "unenumerated", path: "synthetic-captured-surface" }] }
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd(), undefined, captured)
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    let dispatchInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        verbs.push(argv[1])
        if (argv[1] === "worker-dispatch") dispatchInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const result = await failDispatchedWorker(failedEvent(), { windows, credentials: testCredentials, runner, sessionReader: readerFor(exportedSession()), concordBinary: "concord" })
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect((dispatchInput?.host_provenance as { digest?: string } | undefined)?.digest).toBe(captured.digest)
    expect(result?.error?.message).toContain("Task cancelled")
  })

  // The byte bound holds at the worker-fail write itself: a multi-byte detail
  // records through worker-fail and the attempt's in-flight authorization is
  // consumed, so the work item can dispatch again instead of stranding in
  // dispatched state.
  test("a multi-byte failure detail records through worker-fail instead of stranding the attempt", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    let failureInput: Record<string, unknown> | undefined
    const runner: DispatchRunner = {
      async run(argv, input) {
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
        if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
        verbs.push(argv[1])
        if (argv[1] === "worker-fail") failureInput = JSON.parse(input) as Record<string, unknown>
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const error = `任务取消：${"🎉".repeat(1500)}`
    await failDispatchedWorker(failedEvent("call-cancel", SESSION, { sessionId: WORKER_SESSION }, error), {
      windows,
      credentials: testCredentials,
      runner,
      sessionReader: readerFor(exportedSession()),
      concordBinary: "concord",
    })
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(failureInput?.failure_kind).toBe("worker_error")
    const recorded = failureInput?.detail as string
    expect(recorded.startsWith("任务取消：")).toBe(true)
    expect(Buffer.byteLength(recorded, "utf8")).toBeLessThanOrEqual(4096)
    expect(windows.inFlight(SESSION, "call-cancel")).toBeNull()
  })

  // Retry and terminal errors remain separate host observations; neither message
  // alone establishes what caused the Task cancellation.
  const cancelledSettleDeps = (windows: DispatchWindows, runner: DispatchRunner): LaneCompletionDeps => ({
    windows,
    credentials: testCredentials,
    runner,
    sessionReader: readerFor(exportedSession()),
    concordBinary: "concord",
    hostObservations: new HostFailureObservations(),
  })

  const workerFailRunner = (failureInput: { value?: Record<string, unknown> }): DispatchRunner => ({
    async run(argv, input) {
      if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
      if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
      if (argv[1] === "worker-fail") failureInput.value = JSON.parse(input) as Record<string, unknown>
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  })

  test("retry-event-recorded: a session.status retry cause is appended to the cancelled Task's failure detail", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    // The host observed the provider fault on the child session and published
    // its retry status carrying the provider message before the cancel ended
    // the Task.
    await failDispatchedWorker({
      type: "session.status",
      properties: { sessionID: WORKER_SESSION, status: { type: "retry", attempt: 1, message: "AI_APICallError: Rate limit reached for requests", next: 1 } },
    }, options)
    const result = await failDispatchedWorker(failedEvent(), options)
    expect(result?.error?.kind).toBe("error")
    expect(failureInput.value?.failure_kind).toBe("worker_error")
    expect(failureInput.value?.detail).toBe("Task cancelled; host retry observation: AI_APICallError: Rate limit reached for requests")
    expect(windows.inFlight(SESSION, "call-cancel")).toBeNull()
  })

  test("info-error-fallback-recorded: the child message info.error is appended when no retry status was observed", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    // A provider fault that never entered the retry policy still lands on the
    // child session's terminal assistant message as a typed info.error.
    await failDispatchedWorker({
      type: "message.updated",
      properties: { info: { id: "message-1", sessionID: WORKER_SESSION, role: "assistant", error: { name: "APIError", data: { message: "Rate limit reached for requests", statusCode: 429, isRetryable: false } } } },
    }, options)
    const result = await failDispatchedWorker(failedEvent(), options)
    expect(result?.error?.kind).toBe("error")
    expect(failureInput.value?.failure_kind).toBe("worker_error")
    expect(failureInput.value?.detail).toBe("Task cancelled; host terminal observation: APIError: Rate limit reached for requests")
    expect(windows.inFlight(SESSION, "call-cancel")).toBeNull()
  })

  test("cancel-without-cause-unchanged: a cancelled Task with no provider cause records the bare host string", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    // An abort marker names the operator's interrupt, not a provider fault,
    // so it is held as no cause and the detail stays exactly as the host
    // wrote it.
    await failDispatchedWorker({
      type: "message.updated",
      properties: { info: { id: "message-1", sessionID: WORKER_SESSION, role: "assistant", error: { name: "MessageAbortedError", data: { message: "The operation was aborted" } } } },
    }, options)
    const result = await failDispatchedWorker(failedEvent(), options)
    expect(result?.error?.kind).toBe("error")
    expect(failureInput.value?.failure_kind).toBe("worker_error")
    expect(failureInput.value?.detail).toBe("Task cancelled")
    expect(windows.inFlight(SESSION, "call-cancel")).toBeNull()
  })

  // the same settle route records a typed failure_attribution
  // beside the legacy detail. The retained sources ride the observation with
  // their host digest; a cancellation stays an unknown cause even when a
  // provider fault was observed on the same child, and the fault's message
  // text alone never upgrades the cause.
  const attribution = (input?: Record<string, unknown>): Record<string, any> => (input?.failure_attribution ?? {}) as Record<string, any>

  test("attribution-bare-cancel: a bare cancellation records the absence case as an explicit unknown", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    await failDispatchedWorker(failedEvent(), options)
    const observed = attribution(failureInput.value)
    expect(observed.observer).toBe("host_adapter")
    expect(observed.phase).toBe("host_execution")
    expect(observed.cause).toBe("unknown")
    expect(observed.basis).toBe("unknown")
    expect(observed.sources).toHaveLength(1)
    expect(observed.sources[0].kind).toBe("host_event")
    expect(observed.sources[0].host).toEqual({ tag: "cancellation", event_type: "message.part.updated", session_id: WORKER_SESSION })
    expect(observed.sources[0].message).toBe("Task cancelled")
    expect(observed.sources[0].digest).toMatch(/^sha256:[0-9a-f]{64}$/)
    const assertion = failureInput.value?.assertion as Record<string, unknown>
    expect(assertion.version).toBe(2)
    expect(assertion.observation_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
  })

  test("attribution-retry-source: a held retry rides the sources and never becomes the cause", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    await failDispatchedWorker({
      type: "session.status",
      properties: { sessionID: WORKER_SESSION, status: { type: "retry", attempt: 1, message: "AI_APICallError: Rate limit reached for requests", next: 1 } },
    }, options)
    await failDispatchedWorker(failedEvent(), options)
    const observed = attribution(failureInput.value)
    expect(observed.cause).toBe("unknown")
    expect(observed.basis).toBe("unknown")
    expect(observed.sources).toHaveLength(2)
    const retrySource = observed.sources[0]
    expect(retrySource.kind).toBe("host_event")
    expect(retrySource.host.tag).toBe("retry")
    expect(retrySource.host.event_type).toBe("session.status")
    expect(retrySource.host.session_id).toBe(WORKER_SESSION)
    // The core host shape carries no retry counters; the retained digest owns
    // them. A "Rate limit" substring in the message text never upgrades the
    // cause: only a typed host error identity could, and this source has none.
    expect(retrySource.message).toContain("Rate limit reached")
    expect(retrySource.host).not.toHaveProperty("retry_attempt")
    expect(retrySource.host).not.toHaveProperty("next_retry")
    expect(retrySource.host).not.toHaveProperty("error_name")
    expect(retrySource.digest).toMatch(/^sha256:[0-9a-f]{64}$/)
    expect(observed.sources[1].host.tag).toBe("cancellation")
    const assertion = failureInput.value?.assertion as Record<string, unknown>
    expect(assertion.version).toBe(2)
  })

  test("attribution-terminal-source: a held typed provider fault rides with its retained digest and fields", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    // A probe instance learns the retained source digest the same record()
      // call produces, so the assertion binds the exact digest byte for byte.
    const probe = new HostFailureObservations()
    probe.record({ tag: "terminal", event_type: "message.updated", session_id: WORKER_SESSION, message_id: "message-1", error_name: "APIError", status_code: 429, message: "Rate limit reached for requests" })
    const retained = probe.take(WORKER_SESSION)[0]
    await failDispatchedWorker({
      type: "message.updated",
      properties: { info: { id: "message-1", sessionID: WORKER_SESSION, role: "assistant", error: { name: "APIError", data: { message: "Rate limit reached for requests", statusCode: 429, isRetryable: false } } } },
    }, options)
    await failDispatchedWorker(failedEvent(), options)
    const observed = attribution(failureInput.value)
    // The cancellation cause stays unknown: the host record shows the fault
    // but does not establish that it caused the cancellation.
    expect(observed.cause).toBe("unknown")
    expect(observed.basis).toBe("unknown")
    expect(observed.sources).toHaveLength(2)
    const fault = observed.sources[0]
    expect(fault.kind).toBe("host_event")
    expect(fault.locator).toBe("message-1")
    expect(fault.digest).toBe(retained.source_digest)
    expect(fault.message).toBe("Rate limit reached for requests")
    expect(fault.host).toEqual({ tag: "terminal", event_type: "message.updated", session_id: WORKER_SESSION, message_id: "message-1", error_name: "APIError", status_code: 429 })
    const assertion = failureInput.value?.assertion as Record<string, unknown>
    expect(assertion.version).toBe(2)
  })

  test("attribution-typed-terminal-error: a non-cancel Task error with a typed fault stays a hypothesis", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    await failDispatchedWorker({
      type: "message.updated",
      properties: { info: { id: "message-1", sessionID: WORKER_SESSION, role: "assistant", error: { name: "APIError", data: { message: "Rate limit reached for requests", statusCode: 429, isRetryable: false } } } },
    }, options)
    await failDispatchedWorker(failedEvent("call-cancel", SESSION, { sessionId: WORKER_SESSION }, "APIError: Rate limit reached for requests"), options)
    const observed = attribution(failureInput.value)
    // The host record carries the fault and the terminal error side by side,
    // but nothing in it links the fault to the terminal: the locator alone
    // proves no causality, so the cause stays a hypothesis.
    expect(observed.cause).toBe("host_provider")
    expect(observed.basis).toBe("hypothesis")
    expect(observed.sources[0].locator).toBe("message-1")
    expect(observed.sources[0].digest).toMatch(/^sha256:[0-9a-f]{64}$/)
    expect(observed.sources[observed.sources.length - 1].host.tag).toBe("terminal")
    const assertion = failureInput.value?.assertion as Record<string, unknown>
    expect(assertion.version).toBe(2)
  })

  test("attribution-provider-hypothesis: a provider fault beside a different generic terminal never promotes to supported", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    await failDispatchedWorker({
      type: "message.updated",
      properties: { info: { id: "message-1", sessionID: WORKER_SESSION, role: "assistant", error: { name: "APIError", data: { message: "Rate limit reached for requests", statusCode: 429, isRetryable: false } } } },
    }, options)
    // The terminal error is a different, generic diagnostic: the host record
    // links it to the held fault no more than it links a bare cancellation.
    await failDispatchedWorker(failedEvent("call-cancel", SESSION, { sessionId: WORKER_SESSION }, "provider request failed"), options)
    const observed = attribution(failureInput.value)
    expect(observed.cause).toBe("host_provider")
    expect(observed.basis).toBe("hypothesis")
    // The typed fault itself stays exact: its locator, digest, message, and
    // host identity are unchanged by the refusal to promote the cause.
    expect(observed.sources[0].locator).toBe("message-1")
    expect(observed.sources[0].message).toBe("Rate limit reached for requests")
    expect(observed.sources[0].host).toEqual({ tag: "terminal", event_type: "message.updated", session_id: WORKER_SESSION, message_id: "message-1", error_name: "APIError", status_code: 429 })
  })

  test("attribution-arm-overflow: an over-bound terminal diagnostic omits the copied message and binds the full content digest", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    const fullError = "overflow-" + "x".repeat(2500)
    await failDispatchedWorker(failedEvent("call-cancel", SESSION, { sessionId: WORKER_SESSION }, fullError), options)
    const observed = attribution(failureInput.value)
    const arm = observed.sources[observed.sources.length - 1]
    // No copied message past the bound: a truncated diagnostic is a new
    // claim, and the source carries the full-content digest instead.
    expect(arm.message).toBeUndefined()
    const expectedDigest = "sha256:" + createHash("sha256").update(JSON.stringify({ host: { tag: "terminal", event_type: "message.part.updated", session_id: WORKER_SESSION }, message: fullError }), "utf8").digest("hex")
    expect(arm.digest).toBe(expectedDigest)
    expect(arm.host).toEqual({ tag: "terminal", event_type: "message.part.updated", session_id: WORKER_SESSION })
    const assertion = failureInput.value?.assertion as Record<string, unknown>
    expect(assertion.version).toBe(2)
  })

  test("attribution-foreign-source-refused: the builder refuses a held source naming another child", () => {
    const foreign = {
      tag: "terminal" as const, event_type: "message.updated" as const, session_id: "ses_other_child",
      message_id: "message-9", error_name: "APIError", message: "foreign fault", source_digest: "sha256:" + "0".repeat(64),
    }
    expect(() => cancellationFailureObservation("Task cancelled", WORKER_SESSION, [foreign])).toThrow(/cannot ride/)
  })

  test("attribution-spawn-overflow: an over-bound spawn diagnostic omits the copied message and binds the full content digest", () => {
    const fullMessage = "y".repeat(3000)
    const observed = spawnFailureObservation(SESSION, { name: "OverflowError", data: { message: fullMessage } })
    expect(observed.sources[0].message).toBeUndefined()
    const expectedDigest = "sha256:" + createHash("sha256").update(JSON.stringify({ host: { tag: "terminal", event_type: "session.error", session_id: SESSION, error_name: "OverflowError" }, message: fullMessage }), "utf8").digest("hex")
    expect(observed.sources[0].digest).toBe(expectedDigest)
    expect(observed.sources[0].host).toEqual({ tag: "terminal", event_type: "session.error", session_id: SESSION, error_name: "OverflowError" })
  })

  test("attribution-foreign-child: a held observation for another session never rides this attempt", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    await failDispatchedWorker({
      type: "message.updated",
      properties: { info: { id: "message-9", sessionID: "ses_other_child", role: "assistant", error: { name: "APIError", data: { message: "foreign fault", statusCode: 500 } } } },
    }, options)
    await failDispatchedWorker(failedEvent(), options)
    const observed = attribution(failureInput.value)
    expect(observed.sources).toHaveLength(1)
    expect(observed.sources[0].host.session_id).toBe(WORKER_SESSION)
    expect(observed.cause).toBe("unknown")
  })

  test("two maximum host observations keep complete sources and balanced detail summaries", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const failureInput: { value?: Record<string, unknown> } = {}
    const options = cancelledSettleDeps(windows, workerFailRunner(failureInput))
    const retry = "retry-start " + "🎉".repeat(509)
    const terminal = "terminal-start " + "🎉".repeat(508)
    await failDispatchedWorker({ type: "session.status", properties: {
      sessionID: WORKER_SESSION, status: { type: "retry", message: retry },
    } }, options)
    await failDispatchedWorker({ type: "message.updated", properties: { info: {
      id: "message-error", sessionID: WORKER_SESSION,
      error: { name: "APIError", data: { message: terminal, statusCode: 429 } },
    } } }, options)
    const result = await failDispatchedWorker(failedEvent(), options)
    const detail = failureInput.value?.detail as string
    expect(Buffer.byteLength(detail, "utf8")).toBeLessThanOrEqual(4096)
    expect(detail).toContain("host retry observation: retry-start")
    expect(detail).toContain("host terminal observation: APIError: terminal-start")
    expect(detail.split("[summary truncated]")).toHaveLength(3)
    expect(Buffer.from(detail, "utf8").toString("utf8")).toBe(detail)
    expect(result?.error?.details?.host_observations).toEqual([
      expect.objectContaining({ tag: "retry", message: retry }),
      expect.objectContaining({ tag: "terminal", message: terminal }),
    ])
  })

  test("a refused born-failed write retains settlement without retry", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
    const verbs: string[] = []
    const result = await failDispatchedWorker(failedEvent(), deps(verbs, windows, "general"))
    expect(verbs).toEqual([])
    expect(result?.error?.kind).toBe("agent_identity_mismatch")
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).toThrow()
  })

  test("failed persistence retains the authorization and does not automatically retry", async () => {
    const windows = new DispatchWindows()
    windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), "call-cancel", async () => process.cwd(), process.cwd())
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
// parent session and leaves the tool part running. That event settles the
// attempt only when every earlier authorization reached its correlated Task
// terminal first, and the abandonment route is the settle path because the
// record names no child session.
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
    await windows.bind(TASK_TOOL_ID, SESSION, windowArgs(), callID, async () => process.cwd(), process.cwd())
  }

  const bindReplacement = async (windows: DispatchWindows, callID = "call-replacement", attemptID = "attempt-replacement") => {
    const replacement = { ...packet(), attempt_id: attemptID }
    windows.open(SESSION, replacement, PACKET_DIGEST, process.cwd())
    await windows.bind(TASK_TOOL_ID, SESSION, { ...windowArgs(), prompt: replacement.attempt_id }, callID, async () => process.cwd(), process.cwd())
    return windows.inFlight(SESSION, callID)!
  }

  const completedPart = (callID: string, id = "part-completed", prompt = packet().attempt_id) => ({
    type: "message.part.updated",
    properties: { part: { type: "tool", tool: TASK_TOOL_ID, sessionID: SESSION, callID, id,
      state: { status: "completed", output: "original Task completed", input: { prompt } } } },
  })

  test("a late session error cannot abandon a successor after session-only abandonment", async () => {
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-original")
    const verbs: string[] = []
    const options = deps(verbs, windows)
    await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual(["worker-abandon"])
    const replacement = await bindReplacement(windows)
    const result = await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual(["worker-abandon"])
    expect(windows.inFlightAttempt(SESSION)).toBe(replacement)
    expect(result?.error?.details?.effect_state).toBe("unknown")
    expect(result?.error?.recovery_action).toBe("reconcile_operation")
    expect(JSON.stringify(result?.error?.details)).toContain('Agent not found: \\"concord-verify\\"')
  })

  test("a settled after hook without its host terminal does not certify the next generation", async () => {
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-original")
    const original = windows.claimSettlement(SESSION, "call-original")!
    windows.finishSettlement(SESSION, "call-original", original)
    const replacement = await bindReplacement(windows)
    const verbs: string[] = []
    const result = await failDispatchedWorker(spawnFailureEvent(), deps(verbs, windows))
    expect(verbs).toEqual([])
    expect(windows.inFlightAttempt(SESSION)).toBe(replacement)
    expect(result?.error?.details?.effect_state).toBe("unknown")
  })

  for (const nativePartIdentity of [false, true]) {
    test(`sequential recovery remains admitted after a correlated ${nativePartIdentity ? "native part" : "call"} terminal`, async () => {
      const windows = new DispatchWindows()
      const callID = nativePartIdentity ? "part-original" : "call-original"
      await withInFlightAttempt(windows, callID)
      const original = windows.claimSettlement(SESSION, callID)!
      windows.finishSettlement(SESSION, callID, original)
      const verbs: string[] = []
      const options = deps(verbs, windows)
      await failDispatchedWorker(completedPart("call-original", "part-original", nativePartIdentity ? packet().attempt_id : serializeLanePacket(packet())), options)
      await bindReplacement(windows)
      await failDispatchedWorker(spawnFailureEvent(), options)
      expect(verbs).toEqual(["worker-abandon"])
      expect(windows.inFlightAttempt(SESSION)).toBeNull()
    })
  }

  test("a foreign terminal cannot certify the next generation", async () => {
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-original")
    const original = windows.claimSettlement(SESSION, "call-original")!
    windows.finishSettlement(SESSION, "call-original", original)
    const verbs: string[] = []
    const options = deps(verbs, windows)
    await failDispatchedWorker(completedPart("foreign-call", "foreign-part"), options)
    const replacement = await bindReplacement(windows)
    await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual([])
    expect(windows.inFlightAttempt(SESSION)).toBe(replacement)
  })

  test("a duplicate old terminal cannot certify a replacement that reuses its call selector", async () => {
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "reused-call")
    const original = windows.claimSettlement(SESSION, "reused-call")!
    windows.finishSettlement(SESSION, "reused-call", original)
    const verbs: string[] = []
    const options = deps(verbs, windows)
    const oldTerminal = completedPart("reused-call")
    await failDispatchedWorker(oldTerminal, options)
    const replacement = await bindReplacement(windows, "reused-call")
    await failDispatchedWorker(oldTerminal, options)
    windows.finishSettlement(SESSION, "reused-call", replacement)
    const next = await bindReplacement(windows, "next-call", "attempt-after-replacement")
    await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual([])
    expect(windows.activeAttempt(SESSION)).toBe(next)
  })

  test("a duplicate old error cannot settle a replacement that reuses its call selector", async () => {
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "reused-call")
    const original = windows.claimSettlement(SESSION, "reused-call")!
    windows.finishSettlement(SESSION, "reused-call", original)
    const verbs: string[] = []
    const options = deps(verbs, windows)
    const oldTerminal = completedPart("reused-call")
    await failDispatchedWorker(oldTerminal, options)
    const replacement = await bindReplacement(windows, "reused-call")
    await failDispatchedWorker({ ...oldTerminal, properties: { part: { ...oldTerminal.properties.part,
      state: { status: "error", error: "late old error", metadata: { sessionId: WORKER_SESSION }, input: oldTerminal.properties.part.state.input },
    } } }, options)
    expect(verbs).toEqual([])
    expect(windows.activeAttempt(SESSION)).toBe(replacement)
  })

  test("a prior terminal arriving after authorization cannot retroactively certify it", async () => {
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-original")
    const original = windows.claimSettlement(SESSION, "call-original")!
    windows.finishSettlement(SESSION, "call-original", original)
    const replacement = await bindReplacement(windows)
    const verbs: string[] = []
    const options = deps(verbs, windows)
    await failDispatchedWorker(completedPart("call-original"), options)
    await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual([])
    expect(windows.inFlightAttempt(SESSION)).toBe(replacement)
  })

  test("a malformed terminal cannot certify the next generation", async () => {
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-original")
    const original = windows.claimSettlement(SESSION, "call-original")!
    windows.finishSettlement(SESSION, "call-original", original)
    const event = completedPart("call-original")
    const verbs: string[] = []
    const options = deps(verbs, windows)
    await failDispatchedWorker({ ...event, properties: { part: { ...event.properties.part, state: { status: "completed" } } } }, options)
    const replacement = await bindReplacement(windows)
    await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual([])
    expect(windows.inFlightAttempt(SESSION)).toBe(replacement)
  })

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
        expect(abandonInput.detail).toContain("execution outcome unknown")
        expect(abandonInput.detail).toContain('Agent not found: "concord-verify"')
      }
      return { exitCode: 0, stdout: "", stderr: "" }
    } }
    const result = await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual(["worker-abandon"])
    expect(result?.error?.retry_safe).toBe(false)
    expect(result?.error?.message).toContain("execution outcome unknown")
    expect(windows.inFlightAttempt(SESSION)).toBeNull()
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
    windows.close(SESSION)
  })

  // abandonment sends a host observation for the session.error
  // event that settled the attempt. The observation identifies the parent,
  // not whether a child existed, and the cause stays unknown.
  test("attribution-abandonment: the abandonment carries the host session.error observation", async () => {
    bindSessionList()
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-spawn")
    const verbs: string[] = []
    let abandonInput: Record<string, unknown> | undefined
    const options = deps(verbs, windows)
    options.evidenceRunner = { async run(argv, raw) {
      verbs.push(argv[1])
      if (argv[1] === "worker-abandon") abandonInput = JSON.parse(raw) as Record<string, unknown>
      return { exitCode: 0, stdout: "", stderr: "" }
    } }
    await failDispatchedWorker(spawnFailureEvent(), options)
    expect(verbs).toEqual(["worker-abandon"])
    const observed = (abandonInput?.failure_attribution ?? {}) as Record<string, any>
    expect(observed.observer).toBe("host_adapter")
    expect(observed.phase).toBe("host_execution")
    expect(observed.cause).toBe("unknown")
    expect(observed.basis).toBe("unknown")
    expect(observed.sources).toHaveLength(1)
    expect(observed.sources[0].kind).toBe("host_event")
    expect(observed.sources[0].locator).toBeUndefined()
    expect(observed.sources[0].digest).toMatch(/^sha256:[0-9a-f]{64}$/)
    expect(observed.sources[0].host).toEqual({ tag: "terminal", event_type: "session.error", session_id: SESSION, error_name: "UnknownAgentError" })
    const assertion = abandonInput?.assertion as Record<string, unknown>
    expect(assertion.version).toBe(2)
    expect(assertion.observation_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
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
    expect(result?.error?.message).toBe("write unavailable")
    expect(result?.error?.details?.effect_state).toBe("unknown")
    expect(windows.inFlightAttempt(SESSION)).not.toBeNull()
    // The settlement claim released with the record retained, so the named
    // worker_abandon route still releases the retained attempt.
    expect(windows.releaseRetained(SESSION, packet().attempt_id, lane.id)).toBe(true)
    expect(() => windows.open(SESSION, packet(), PACKET_DIGEST, process.cwd())).not.toThrow()
    windows.close(SESSION)
  })

  // CD-0178 D3: the spawn-failure close reads no host liveness. An unreadable
  // session index cannot strand the attempt — the signed worker-abandon close
  // rides and releases the window.
  test("an unreadable session index does not strand a spawn-failed attempt", async () => {
    bindSessionList(500)
    const windows = new DispatchWindows()
    await withInFlightAttempt(windows, "call-spawn")
    const verbs: string[] = []
    await failDispatchedWorker(spawnFailureEvent(), deps(verbs, windows))
    expect(verbs).toEqual(["worker-abandon"])
    expect(windows.inFlightAttempt(SESSION)).toBeNull()
    expect(windows.releaseRetained(SESSION, packet().attempt_id, lane.id)).toBe(false)
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
// controls, so this file resets the factory's shared lease and peer state.
afterAll(() => {
  configureHostLease({ reset: true })
})

describe("structured host failure observations", () => {
  test("retry and terminal observations retain their distinct source fields", async () => {
    const observations = new HostFailureObservations()
    const options = { hostObservations: observations }
    await failDispatchedWorker({ type: "session.status", properties: {
      sessionID: WORKER_SESSION, status: { type: "retry", attempt: 1, message: "retry observed", next: 3 },
    } }, options)
    await failDispatchedWorker({ type: "message.updated", properties: { info: {
      id: "message-error", sessionID: WORKER_SESSION, error: {
        name: "APIError", data: { message: "terminal error observed", statusCode: 429, isRetryable: false, responseBody: "not retained" },
      },
    } } }, options)
    expect(observations.take(WORKER_SESSION)).toEqual([
      { tag: "retry", event_type: "session.status", session_id: WORKER_SESSION, message: "retry observed", retry_attempt: 1, next_retry: 3, source_digest: expect.stringMatching(/^sha256:[a-f0-9]{64}$/) },
      { tag: "terminal", event_type: "message.updated", session_id: WORKER_SESSION, message_id: "message-error", error_name: "APIError", status_code: 429, message: "terminal error observed", source_digest: expect.stringMatching(/^sha256:[a-f0-9]{64}$/) },
    ])
    expect(observations.take(WORKER_SESSION)).toEqual([])
  })

  test("an oversized observation refuses whole without a truncated diagnostic", async () => {
    const observations = new HostFailureObservations()
    await failDispatchedWorker({ type: "session.status", properties: {
      sessionID: WORKER_SESSION, status: { type: "retry", message: "🎉".repeat(513) },
    } }, { hostObservations: observations })
    expect(observations.take(WORKER_SESSION)).toEqual([])
  })

  test("busy and idle status do not erase a prior retry observation", async () => {
    const observations = new HostFailureObservations()
    const options = { hostObservations: observations }
    await failDispatchedWorker({ type: "session.status", properties: {
      sessionID: WORKER_SESSION, status: { type: "retry", message: "retry observed" },
    } }, options)
    await failDispatchedWorker({ type: "session.status", properties: {
      sessionID: WORKER_SESSION, status: { type: "busy" },
    } }, options)
    await failDispatchedWorker({ type: "session.status", properties: {
      sessionID: WORKER_SESSION, status: { type: "idle" },
    } }, options)
    expect(observations.take(WORKER_SESSION)).toEqual([
      expect.objectContaining({ tag: "retry", message: "retry observed" }),
    ])
  })

  test("a consumed observation cannot cross a child-session identity", async () => {
    const observations = new HostFailureObservations()
    await failDispatchedWorker({ type: "message.updated", properties: { info: {
      id: "message-error", sessionID: "ses_otherchild", error: { name: "APIError", data: { message: "other child fault", statusCode: 503 } },
    } } }, { hostObservations: observations })
    expect(observations.take(WORKER_SESSION)).toEqual([])
    expect(observations.take("ses_otherchild")).toEqual([expect.objectContaining({ tag: "terminal", session_id: "ses_otherchild", message_id: "message-error", status_code: 503 })])
  })

  test("an invalid typed error code does not manufacture valid provider evidence", async () => {
    const observations = new HostFailureObservations()
    await failDispatchedWorker({ type: "message.updated", properties: { info: {
      id: "message-error", sessionID: WORKER_SESSION, error: { name: "APIError", data: { message: "invalid code", statusCode: 700 } },
    } } }, { hostObservations: observations })
    expect(observations.take(WORKER_SESSION)).toEqual([])
  })

  test("an invalid new observation preserves the earlier valid source", () => {
    const observations = new HostFailureObservations()
    const valid = { tag: "terminal" as const, event_type: "message.updated" as const,
      session_id: WORKER_SESSION, message_id: "message-error", error_name: "APIError",
      message: "exact earlier observation", status_code: 429 }
    observations.record(valid)
    observations.record({ ...valid, message: "x".repeat(2049) })
    observations.record({ ...valid, status_code: 700 })
    expect(observations.take(WORKER_SESSION)).toEqual([
      expect.objectContaining(valid),
    ])
  })
})
