import { expect, test } from "bun:test"
import { reconcileRetainedWorker } from "./worker_recovery"
import { type AgentLanePacket, type WorkerRecoveryContext } from "./dispatch"
import { agentLanes } from "./generated-agent-lanes"
import { serializeLanePacket } from "./dispatch-window"
import type { SessionReader } from "./move-session"

const lane = agentLanes.find((entry) => entry.id === "verify")!
const model = "openai/test-model"
const packet: AgentLanePacket = { schema_version: "1.0", work_id: "work-recovery", attempt_id: "attempt-recovery", lane_id: lane.id, lane_version: lane.version, lane_digest: lane.digest, step_id: "repair", inputs: { task: "Verify a retained synthetic report.", binding: { objective_source: "contract_premise", work_version: 7, contract_version: 1, assigned_result: "exit_codes" } } }
const report = { schema_version: "1.0", readback_model: model, status: "completed", evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: "synthetic result" })) }
const wrap = `<task id="child-recovery" state="completed">\n<task_result>\n${JSON.stringify(report)}\n</task_result>\n</task>`
const recovery: WorkerRecoveryContext = { packet_digest: "sha256:"+"a".repeat(64), worker_worktree: process.cwd(), coordinator_session: "parent-recovery", attempt_epoch: 23, dispatch_event_id: "historical-random-dispatch", lifecycle_state: "dispatched", dispatch: { attempt_id: packet.attempt_id, lane_id: lane.id, lane_version: lane.version, lane_digest: lane.digest, packet_digest: "sha256:"+"a".repeat(64), readback_model: model, host_provenance: { digest: "sha256:"+"b".repeat(64), sources: [{ kind: "agent_definition", path: "original-agent.md", sha256: "sha256:"+"c".repeat(64) }] } } }

function host() {
  const parent = { info: { id: "parent-recovery", directory: process.cwd() }, messages: [{ info: { id: "parent-message", sessionID: "parent-recovery", role: "assistant" }, parts: [{ id: "retained-part", sessionID: "parent-recovery", type: "tool", tool: "task", state: { status: "completed", input: { subagent_type: `concord-${lane.id}`, prompt: serializeLanePacket(packet) }, output: wrap } }] }] }
  const child = { info: { id: "child-recovery", parentID: "parent-recovery", directory: process.cwd() }, messages: [
    { info: { id: "opening", sessionID: "child-recovery", role: "user", time: { created: 1 } }, parts: [{ type: "text", text: serializeLanePacket(packet) }] },
    { info: { id: "report", sessionID: "child-recovery", role: "assistant", agent: `concord-${lane.id}`, providerID: "openai", modelID: "test-model", time: { created: 2 } }, parts: [{ type: "text", text: JSON.stringify(report) }] },
  ] }
  const reader: SessionReader = {
    async get(id) { return { data: id === "parent-recovery" ? parent.info : child.info, response: new Response("{}") } },
    async messages(id) { return { data: id === "parent-recovery" ? parent.messages : child.messages, response: new Response("[]") } },
  }
  return { parent, child, reader }
}

const credentials = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

test("retained historical report recovery uses original event identity and provenance after adapter restart", async () => {
  const { reader } = host()
  const events = new Map<string, string>()
  const calls: { verb: string; request: any }[] = []
  let obstructed = true
  const runner = { async run(argv: string[], input: string) {
    const request = JSON.parse(input)
    calls.push({ verb: argv[1], request })
    const payload = JSON.stringify({ ...request, assertion: undefined })
    const prior = events.get(request.event_id)
    if (prior && prior !== payload) throw new Error("conflicting evidence payload")
    events.set(request.event_id, payload)
    if (obstructed) return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: [request.event_id], error: { kind: "unavailable", operation: "durable_commit", effect_state: "possible", retry_safe: true, message: "synthetic uncertain commit" } }), stderr: "synthetic uncertain commit" }
    return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
  } }
  const options = { sessionReader: reader, credentials, evidenceRunner: runner, concordBinary: "concord-test", workerDirectory: process.cwd() }
  const pending = await reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", options, new AbortController().signal)
  expect(pending.outcome).toBe("error")
  expect(pending.error?.details?.effect_state).toBe("possible")
  expect(calls.map((call) => call.verb)).toEqual(["worker-dispatch"])
  obstructed = false
  for (let i = 0; i < 3; i++) {
    // No DispatchWindows instance or in-memory settlement state is supplied.
    const result = await reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", { ...options }, new AbortController().signal)
    expect(result.outcome).toBe("ok")
  }
  expect(events.size).toBe(2)
  expect(calls.every((call) => call.verb !== "worker-fail" && call.verb !== "task")).toBe(true)
  expect(calls[0].request.event_id).toBe(recovery.dispatch_event_id)
  expect(calls[0].request.host_provenance).toEqual(recovery.dispatch.host_provenance)
  expect(calls[0].request.authorized_packet).toEqual(packet)
  expect(new Set(calls.map((call) => call.request.assertion.nonce)).size).toBe(calls.length)
})

test("a previously committed completion reuses its historical terminal event id", async () => {
  const { reader } = host()
  const calls: any[] = []
  const result = await reconcileRetainedWorker({ ...recovery, lifecycle_state: "completed", terminal_event_id: "historical-random-completion" }, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv, input) {
      calls.push({ verb: argv[1], input: JSON.parse(input) })
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  }, new AbortController().signal)
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(calls.map((call) => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
  expect(calls[1].input.event_id).toBe("historical-random-completion")
})

test.each(["parent", "part", "unfinished", "packet", "agent", "readback-agent", "model", "report-model", "report", "directory", "work", "attempt"])("retained recovery refuses mismatched %s before evidence", async (fault) => {
  const { reader, parent, child } = host()
  let work = packet.work_id, attempt = packet.attempt_id
  switch (fault) {
    case "parent": child.info.parentID = "another-parent"; break
    case "part": parent.messages[0].parts[0].sessionID = "another-parent"; break
    case "unfinished": parent.messages[0].parts[0].state.status = "running"; break
    case "packet": parent.messages[0].parts[0].state.input.prompt = serializeLanePacket({ ...packet, attempt_id: "another-attempt" }); break
    case "agent": parent.messages[0].parts[0].state.input.subagent_type = "general"; break
    case "readback-agent": child.messages[1].info.agent = "general"; break
    case "model": child.messages[1].info.modelID = "different-model"; break
    case "report-model": parent.messages[0].parts[0].state.output = wrap.replace('"readback_model":"openai/test-model"', '"readback_model":"openai/other-model"'); break
    case "report": parent.messages[0].parts[0].state.output = wrap.replace('"status":"completed"', '"status":"failed"'); break
    case "directory": child.info.directory = "/"; break
    case "work": work = "another-work"; break
    case "attempt": attempt = "another-attempt"; break
  }
  const calls: string[] = []
  const options = {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv: string[]) {
      calls.push(argv[1])
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  }
  try {
    const result = await reconcileRetainedWorker(recovery, work, attempt, "retained-part", options, new AbortController().signal)
    expect(result.outcome).toBe("error")
  } catch (error) { expect(error).toBeInstanceOf(Error) }
  expect(calls).toEqual([])
})

test("recovery preserves distinct recorded dispatch and original terminal models", async () => {
  const { reader, parent, child } = host()
  const terminalModel = "openai/original-terminal-model"
  const terminalReport = { ...report, readback_model: terminalModel }
  parent.messages[0].parts[0].state.output = wrap.replace(JSON.stringify(report), JSON.stringify(terminalReport))
  child.messages[1].info.modelID = "original-terminal-model"
  child.messages[1].parts[0].text = JSON.stringify(terminalReport)
  const calls: any[] = []
  const result = await reconcileRetainedWorker({ ...recovery, lifecycle_state: "completed", terminal_event_id: "original-terminal-event" }, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv, input) { calls.push({ verb: argv[1], input: JSON.parse(input) }); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, new AbortController().signal)
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(calls.map((call) => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
  expect(calls[0].input.readback_model).toBe(model)
  expect(calls[0].input.assertion.readback_model).toBe(model)
  expect(calls[1].input.readback_model).toBe(terminalModel)
  expect(calls[1].input.event_id).toBe("original-terminal-event")
})
