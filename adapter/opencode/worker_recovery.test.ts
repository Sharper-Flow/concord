import { expect, test } from "bun:test"
import { reconcileRetainedWorker } from "./worker_recovery"
import { resolveWorkerReportFromText, type AgentLanePacket, type HostProvenance, type WorkerRecoveryContext } from "./dispatch"
import { agentLanes } from "./generated-agent-lanes"
import { dispatchWindows, serializeLanePacket, TASK_TOOL_ID } from "./dispatch-window"
import type { SessionReader } from "./move-session"

const lane = agentLanes.find((entry) => entry.id === "verify")!
const model = "openai/test-model"
const packet: AgentLanePacket = { schema_version: "1.0", work_id: "work-recovery", attempt_id: "attempt-recovery", lane_id: lane.id, lane_digest: lane.digest, step_id: "repair", inputs: { task: "Verify a retained synthetic report.", binding: { objective_source: "contract_premise", work_version: 7, contract_version: 1, assigned_result: "exit_codes" } } }
const report = { schema_version: "1.0", readback_model: model, status: "completed", evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: "synthetic result" })) }
const wrap = `<task id="child-recovery" state="completed">\n<task_result>\n${JSON.stringify(report)}\n</task_result>\n</task>`
const recovery: WorkerRecoveryContext = { packet_digest: "sha256:"+"a".repeat(64), worker_worktree: process.cwd(), coordinator_session: "parent-recovery", attempt_epoch: 23, dispatch_event_id: "historical-random-dispatch", lifecycle_state: "dispatched", packet_schema_version: "1.0", report_schema_version: "1.0", dispatch: { attempt_id: packet.attempt_id, lane_id: lane.id, lane_digest: lane.digest, packet_schema_version: "1.0", report_schema_version: "1.0", packet_digest: "sha256:"+"a".repeat(64), readback_model: model, host_provenance: { digest: "sha256:"+"b".repeat(64), sources: [{ kind: "agent_definition", path: "original-agent.md", sha256: "sha256:"+"c".repeat(64) }] } } }

function host() {
  const parent = { info: { id: "parent-recovery", directory: process.cwd() }, messages: [{ info: { id: "parent-message", sessionID: "parent-recovery", role: "assistant" }, parts: [{ id: "retained-part", callID: "retained-call", sessionID: "parent-recovery", type: "tool", tool: "task", state: { status: "completed", input: { subagent_type: `concord-${lane.id}`, prompt: serializeLanePacket(packet) }, output: wrap } }] }] }
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

test.each([1_000, 45 * 60_000])("refused retained recovery preserves original report bytes and host timing through invalid-report settlement (%d ms)", async (duration) => {
  const originalPacket: AgentLanePacket = { ...packet, schema_version: "2.0", inputs: { ...packet.inputs, report_protocol: "concord-worker-result-v1" } }
  const { schema_version: _echo, ...workerReport } = report
  workerReport.evidence = workerReport.evidence.map(entry => ({ ...entry, detail: "synthetic retained result — unchanged" }))
  const text = `\`\`\`concord-worker-result-v1\n${JSON.stringify(workerReport, null, 2)}\n\`\`\``
  const transcript = host()
  const part = transcript.parent.messages[0].parts[0]
  const start = Date.parse("2026-08-01T10:00:00Z")
  const captured = start + duration
  Object.assign(part.state, { time: { start, end: captured } })
  Object.assign(transcript.child.messages[0].info.time, { created: start })
  Object.assign(transcript.child.messages[1].info.time, { created: captured - 1, completed: captured })
  part.state.input.prompt = serializeLanePacket(originalPacket)
  part.state.output = `<task id="child-recovery" state="completed">\n<task_result>\n${text}\n</task_result>\n</task>`
  transcript.child.messages[0].parts[0].text = part.state.input.prompt
  transcript.child.messages[1].parts[0].text = text
  const originalBytes = Buffer.from(JSON.stringify([transcript.parent, transcript.child]), "utf8")
  const originalOutput = Buffer.from(part.state.output, "utf8")
  const originalRecovery: WorkerRecoveryContext = {
    ...recovery, packet_schema_version: "2.0", report_schema_version: "2.0",
    dispatch: { ...recovery.dispatch, packet_schema_version: "2.0", report_schema_version: "2.0" },
  }
  const beforeRecovery = JSON.stringify(originalRecovery)
  const calls: { verb: string; request: any }[] = []
  let kind = "missing_evidence"
  let recorded = 0
  const options = {
    sessionReader: transcript.reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    onRecorded: () => { recorded++ },
    evidenceRunner: { async run(argv: string[], input: string) {
      const request = JSON.parse(input)
      calls.push({ verb: argv[1], request })
      if (argv[1] === "worker-complete") return { exitCode: 1, stderr: "", stdout: JSON.stringify({ ok: false, event_ids: [], error: {
        kind, operation: "worker_oracle_findings", retry_safe: false, effect_state: "none", message: "synthetic fixed oracle contradiction",
      } }) }
      if (argv[1] !== "worker-dispatch" && argv[1] !== "worker-fail") throw new Error("no replacement worker or capture is allowed")
      return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
    } },
  }
  const refused = await reconcileRetainedWorker(originalRecovery, packet.work_id, packet.attempt_id, "retained-part", options, new AbortController().signal)
  expect(refused.outcome).toBe("error")
  expect(refused.error?.details?.effect_state).toBe("none")
  expect(recorded).toBe(0)
  expect(calls.map(call => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
  expect(Buffer.from(JSON.stringify([transcript.parent, transcript.child]), "utf8")).toEqual(originalBytes)
  kind = "invalid_payload"
  const settled = await reconcileRetainedWorker(originalRecovery, packet.work_id, packet.attempt_id, "retained-part", options, new AbortController().signal)
  expect(settled.outcome).toBe("error")
  expect(settled.error?.kind).toBe("invalid_report")
  expect(settled.error?.retry_safe).toBe(false)
  expect(Buffer.from(settled.output!, "utf8")).toEqual(Buffer.from(text, "utf8"))
  expect(recorded).toBe(1)
  expect(calls.map(call => call.verb)).toEqual(["worker-dispatch", "worker-complete", "worker-dispatch", "worker-complete", "worker-fail"])
  expect(calls[0].request.event_id).toBe(recovery.dispatch_event_id)
  expect(calls[2].request.event_id).toBe(recovery.dispatch_event_id)
  expect(calls[2].request.authorized_packet).toEqual(originalPacket)
  const { assertion: _first, ...first } = calls[1].request
  const { assertion: _second, ...second } = calls[3].request
  expect(second).toEqual(first)
  expect(first.report_schema_version).toBe("2.0")
  expect(calls.every(call => call.request.work_id === packet.work_id && call.request.attempt_id === packet.attempt_id)).toBe(true)
  expect(Buffer.from(part.state.output, "utf8")).toEqual(originalOutput)
  expect(Buffer.from(JSON.stringify([transcript.parent, transcript.child]), "utf8")).toEqual(originalBytes)
  expect(JSON.stringify(originalRecovery)).toBe(beforeRecovery)
  expect((part.state as typeof part.state & { time: { start: number; end: number } }).time).toEqual({ start, end: captured })
  expect(transcript.child.messages[1].info.time).toEqual({ created: captured - 1, completed: captured })
})

for (const version of ["1.0", "1.1", "2.0"] as const) {
  for (const foldRefused of [false, true]) {
    test(`retained ${version} recovery admits omitted schema echo and preserves fold refusal=${foldRefused}`, async () => {
      const originalPacket: AgentLanePacket = {
        ...packet, schema_version: version,
        inputs: { ...packet.inputs, ...(version === "2.0" ? { report_protocol: "concord-worker-result-v1" as const } : {}) },
      }
      const { schema_version: _echo, ...workerReport } = report
      const text = version === "2.0" ? `\`\`\`concord-worker-result-v1\n${JSON.stringify(workerReport)}\n\`\`\`` : JSON.stringify(workerReport)
      const transcript = host()
      transcript.parent.messages[0].parts[0].state.input.prompt = serializeLanePacket(originalPacket)
      transcript.parent.messages[0].parts[0].state.output = `<task id="child-recovery" state="completed">\n<task_result>\n${text}\n</task_result>\n</task>`
      transcript.child.messages[0].parts[0].text = serializeLanePacket(originalPacket)
      transcript.child.messages[1].parts[0].text = text
      const before = JSON.stringify([transcript.parent, transcript.child])
      const admitted = resolveWorkerReportFromText(text, originalPacket, model)
      expect("report" in admitted).toBe(true)
      if (!("report" in admitted)) throw new Error(admitted.detail)
      expect(admitted.report.schema_version).toBe(version)
      const originalRecovery: WorkerRecoveryContext = {
        ...recovery, packet_schema_version: version, report_schema_version: version,
        dispatch: { ...recovery.dispatch, packet_schema_version: version, report_schema_version: version },
      }
      const calls: { verb: string; request: any }[] = []
      const result = await reconcileRetainedWorker(originalRecovery, packet.work_id, packet.attempt_id, "retained-part", {
        sessionReader: transcript.reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
        evidenceRunner: { async run(argv, input) {
          const request = JSON.parse(input)
          calls.push({ verb: argv[1], request })
          if (foldRefused && argv[1] === "worker-complete") return {
            exitCode: 1, stderr: "", stdout: JSON.stringify({ ok: false, event_ids: [], error: {
              kind: "missing_evidence", operation: "worker_oracle_findings", retry_safe: false, effect_state: "none",
              message: "synthetic closure lacks a current-subject control receipt",
            } }),
          }
          return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
        } },
      }, new AbortController().signal)
      expect(calls.map(call => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
      expect(calls[0].request.event_id).toBe(recovery.dispatch_event_id)
      expect(calls[0].request.host_provenance).toEqual(recovery.dispatch.host_provenance)
      expect(calls[0].request.authorized_packet).toEqual(originalPacket)
      expect(calls[0].request.packet_schema_version).toBe(version)
      expect(calls[1].request.report_schema_version).toBe(version)
      expect(calls[1].request.attempt_id).toBe(packet.attempt_id)
      expect(JSON.stringify([transcript.parent, transcript.child])).toBe(before)
      expect(result.outcome).toBe(foldRefused ? "error" : "ok")
      if (foldRefused) {
        expect(result.error?.message).toContain("synthetic closure lacks a current-subject control receipt")
        expect(result.error?.details?.effect_state).toBe("none")
      }
    })
  }
}

test("retained original Task recovery advances only its correlated terminal frontier", async () => {
  const windows = dispatchWindows()
  const sessionID = recovery.coordinator_session
  const { reader } = host()
  const verbs: string[] = []
  windows.open(sessionID, packet, recovery.packet_digest, process.cwd())
  await windows.bind(TASK_TOOL_ID, sessionID, { subagent_type: `concord-${lane.id}`, prompt: packet.attempt_id, description: "retained worker" }, "retained-call", async () => process.cwd(), process.cwd())
  const original = windows.claimSettlement(sessionID, "retained-call")!
  windows.refuseSettlement(sessionID, original)
  const replacement = { ...packet, attempt_id: "attempt-after-retained-recovery" }
  try {
    const result = await reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", {
      sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
      evidenceRunner: { async run(argv) { verbs.push(argv[1]); return { exitCode: 0, stdout: "", stderr: "" } } },
    }, new AbortController().signal)
    expect(result.outcome, JSON.stringify(result)).toBe("ok")
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
    expect(windows.releaseRetained(sessionID, packet.attempt_id, lane.id)).toBe(true)
    windows.open(sessionID, replacement, recovery.packet_digest, process.cwd())
    await windows.bind(TASK_TOOL_ID, sessionID, { subagent_type: `concord-${lane.id}`, prompt: replacement.attempt_id, description: "next worker" }, "next-call", async () => process.cwd(), process.cwd())
    expect(windows.canSettleSessionError(sessionID, windows.inFlightAttempt(sessionID)!)).toBe(true)
  } finally {
    windows.releaseRetained(sessionID, packet.attempt_id, lane.id)
    windows.releaseRetained(sessionID, replacement.attempt_id, lane.id)
  }
})

test("stale content refuses retained recovery before host reads or evidence writes", async () => {
  const original = { ...recovery, dispatch: { ...recovery.dispatch, lane_digest: "sha256:" + "0".repeat(64) } }
  const before = JSON.stringify(original)
  let calls = 0
  const forbidden = async (): Promise<never> => { calls++; throw new Error("must not run") }
  await expect(reconcileRetainedWorker(original, packet.work_id, packet.attempt_id, "retained-part", {
    workerDirectory: process.cwd(), sessionReader: { get: forbidden, messages: forbidden }, evidenceRunner: { run: forbidden }, credentials,
  }, new AbortController().signal)).rejects.toThrow("content revalidation")
  expect(JSON.stringify(original)).toBe(before)
  expect(calls).toBe(0)
})

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
  // The dispatch evidence keeps the legacy 1.0 packet/report schema pair the
  // original dispatch recorded: recovery replays the historical shape and
  // never relabels a legacy attempt with the current schema versions.
  expect(calls[0].request.packet_schema_version).toBe("1.0")
  expect(calls[0].request.report_schema_version).toBe("1.0")
  expect(new Set(calls.map((call) => call.request.assertion.nonce)).size).toBe(calls.length)
})

// A typed Git observation refusal from the terminal ingest stops the first
// recovery submission without closing the attempt or looping. Once the
// worktree answers, the same native retained-report route re-submits the same
// report under the same attempt and terminal event identity, launching no
// replacement worker.
test("a typed git observation refusal keeps the retained report recoverable through the same route", async () => {
  const { reader } = host()
  const calls: { verb: string; request: any }[] = []
  let worktreeAnswers = false
  const runner = { async run(argv: string[], input: string) {
    const request = JSON.parse(input)
    calls.push({ verb: argv[1], request })
    if (argv[1] === "worker-complete" && !worktreeAnswers) {
      return {
        exitCode: 1,
        stdout: JSON.stringify({ ok: false, event_ids: [], error: { kind: "git_unreachable", operation: "worker_terminal_observation", retry_safe: true, effect_state: "none", message: "store: worker_terminal_observation: git_unreachable: cannot observe the attempt's clean status" } }),
        stderr: "",
      }
    }
    return { exitCode: 0, stdout: JSON.stringify({ ok: true, event_ids: [request.event_id] }), stderr: "" }
  } }
  const options = { sessionReader: reader, credentials, evidenceRunner: runner, concordBinary: "concord-test", workerDirectory: process.cwd() }
  const refused = await reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", options, new AbortController().signal)
  expect(refused.outcome).toBe("error")
  expect(refused.error?.message).toContain("cannot observe the attempt's clean status")
  expect(refused.error?.retry_safe).toBe(true)
  expect((refused.error as any)?.details?.effect_state).toBe("none")
  const completions = calls.filter((call) => call.verb === "worker-complete")
  expect(completions).toHaveLength(1)
  const terminalEventID = completions[0].request.event_id
  worktreeAnswers = true
  const result = await reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", { ...options }, new AbortController().signal)
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  const retries = calls.filter((call) => call.verb === "worker-complete")
  expect(retries).toHaveLength(2)
  expect(retries[1].request.event_id).toBe(terminalEventID)
  expect(retries[1].request.attempt_id).toBe(packet.attempt_id)
  expect(retries[1].request.report_schema_version).toBe(completions[0].request.report_schema_version)
  expect(JSON.stringify(retries[1].request.evidence)).toBe(JSON.stringify(completions[0].request.evidence))
  expect(JSON.stringify(retries[1].request.context_findings)).toBe(JSON.stringify(completions[0].request.context_findings))
  const dispatches = calls.filter((call) => call.verb === "worker-dispatch")
  expect(new Set(dispatches.map((call) => call.request.event_id))).toEqual(new Set([recovery.dispatch_event_id]))
  expect(calls.every((call) => call.verb !== "task")).toBe(true)
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

test.each(["parent", "part", "unfinished", "packet", "agent", "readback-agent", "missing-model", "report", "directory", "work", "attempt"])("retained recovery refuses mismatched %s before evidence", async (fault) => {
  const { reader, parent, child } = host()
  let work = packet.work_id, attempt = packet.attempt_id
  switch (fault) {
    case "parent": child.info.parentID = "another-parent"; break
    case "part": parent.messages[0].parts[0].sessionID = "another-parent"; break
    case "unfinished": parent.messages[0].parts[0].state.status = "running"; break
    case "packet": parent.messages[0].parts[0].state.input.prompt = serializeLanePacket({ ...packet, attempt_id: "another-attempt" }); break
    case "agent": parent.messages[0].parts[0].state.input.subagent_type = "general"; break
    case "readback-agent": child.messages[1].info.agent = "general"; break
    case "missing-model": delete child.messages[1].info.modelID; break
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

test.each([undefined, "openai/forged-model", { provider: "forged" }])("recovery derives terminal model identity from the host instead of worker value %j", async (supplied) => {
  const { reader, parent, child } = host()
  const carried = { ...report, readback_model: supplied }
  parent.messages[0].parts[0].state.output = wrap.replace(JSON.stringify(report), JSON.stringify(carried))
  child.messages[1].info.modelID = "actual-terminal-model"
  const calls: { verb: string; request: Record<string, unknown> }[] = []
  const result = await reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv, input) {
      calls.push({ verb: argv[1], request: JSON.parse(input) })
      return { exitCode: 0, stdout: "", stderr: "" }
    } },
  }, new AbortController().signal)
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(calls.map(call => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
  expect(calls[0].request.readback_model).toBe(model)
  expect(calls[1].request.readback_model).toBe("openai/actual-terminal-model")
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

// CD-0208 D5: the caller cannot replace the original provenance. A capture
// injected beside the recovery record — the union of the pre-authorization
// capture and the retained recovery route — must lose to the dispatch record's
// original manifest, and no host file is re-read to build a third one.
test("recovery keeps the original dispatch manifest over an injected capture", async () => {
  const { reader } = host()
  const injected: HostProvenance = { digest: "sha256:" + "d".repeat(64), sources: [{ kind: "agent_definition", path: "injected-agent.md", sha256: "sha256:" + "e".repeat(64) }] }
  const calls: any[] = []
  const result = await reconcileRetainedWorker({ ...recovery, lifecycle_state: "completed", terminal_event_id: "original-manifest-event" }, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(), capturedProvenance: injected,
    evidenceRunner: { async run(argv, input) { calls.push({ verb: argv[1], input: JSON.parse(input) }); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, new AbortController().signal)
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(calls.map((call) => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
  const dispatch = calls[0].input
  expect(dispatch.host_provenance).toEqual(recovery.dispatch.host_provenance)
  expect(dispatch.host_provenance).not.toEqual(injected)
  expect(dispatch.assertion.host_provenance_digest).toBe(recovery.dispatch.host_provenance.digest)
})

// The retained card-bearing 1.1 history recovers through its
// recorded branch. The packet carries a domain_cards member no current
// producer may write, the recorded pair selects the historical reader, and
// the replayed evidence keeps the released 1.1 pair.
test("a recorded card-bearing 1.1 recovery replays through its recorded branch", async () => {
  const source = { kind: "repository_file", project_id: "project", path: ".concord/navigation/domains/child-alpha.md", commit_oid: "a1".repeat(20) }
  const cardBearing: AgentLanePacket = {
    ...packet,
    schema_version: "1.1",
    inputs: { ...packet.inputs, work_context: { source_event_frontier: 19, required_reading: [], findings: [], domain_groups: [{ domain_id: "child-alpha", required_reading_ordinals: [], finding_ids: [], domain_cards: [source] } as never] } },
  }
  const transcript = host()
  transcript.parent.messages[0].parts[0].state.input.prompt = serializeLanePacket(cardBearing)
  transcript.child.messages[0].parts[0].text = serializeLanePacket(cardBearing)
  // The era's builder stamped the dispatching packet's identity into the
  // retained report text, so the 1.1 recovery retains a 1.1-claiming report.
  const retainedReport = { ...report, schema_version: "1.1" }
  const retainedWrap = `<task id="child-recovery" state="completed">\n<task_result>\n${JSON.stringify(retainedReport)}\n</task_result>\n</task>`
  transcript.parent.messages[0].parts[0].state.output = retainedWrap
  transcript.child.messages[transcript.child.messages.length - 1].parts = [{ type: "text", text: JSON.stringify(retainedReport) }]
  const { reader } = transcript
  const calls: any[] = []
  const result = await reconcileRetainedWorker({ ...recovery, packet_schema_version: "1.1", report_schema_version: "1.1", dispatch: { ...recovery.dispatch, packet_schema_version: "1.1", report_schema_version: "1.1" } }, cardBearing.work_id, cardBearing.attempt_id, "retained-part", {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv, input) { calls.push({ verb: argv[1], input: JSON.parse(input) }); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, new AbortController().signal)
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(calls.map((call) => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
  expect(calls[0].input.packet_schema_version).toBe("1.1")
  expect(calls[0].input.report_schema_version).toBe("1.1")
  expect(calls[0].input.authorized_packet.schema_version).toBe("1.1")
  expect(calls[1].input.report_schema_version).toBe("1.1")
})

// A recorded pair no released revision produced is a record no reader owns:
// typed refusal before any replacement worker or recomputed digest.
test.each([["9.9", "9.9"], ["1.1", "2.0"], ["2.0", "1.1"]])("recovery refuses the unretained schema pair %s/%s before evidence", async (packetVersion, reportVersion) => {
  const { reader } = host()
  const calls: string[] = []
  await expect(reconcileRetainedWorker({ ...recovery, packet_schema_version: packetVersion, report_schema_version: reportVersion, dispatch: { ...recovery.dispatch, packet_schema_version: packetVersion, report_schema_version: reportVersion } }, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv: string[]) { calls.push(argv[1]); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, new AbortController().signal)).rejects.toThrow(/no released revision produced/)
  expect(calls).toEqual([])
})

// A retained packet relabeled to a different identity than its recorded
// dispatch refuses: recovery never rewrites recorded schema identity.
test("recovery refuses a retained packet whose schema identity differs from the recorded dispatch", async () => {
  host()
  const relabeled: AgentLanePacket = { ...packet, schema_version: "2.0" }
  const parent = host()
  parent.parent.messages[0].parts[0].state.input.prompt = serializeLanePacket(relabeled)
  const calls: string[] = []
  await expect(reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: parent.reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv: string[]) { calls.push(argv[1]); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, new AbortController().signal)).rejects.toThrow(/schema identity differs/)
  expect(calls).toEqual([])
})

// CD-0056 D7 and Verification: worker echoes cannot select canonical identity.
test("recovery derives schema identity from the recorded packet instead of a worker echo", async () => {
  const relabeledPacket: AgentLanePacket = { ...packet, schema_version: "1.1" }
  const mismatchTranscript = host()
  mismatchTranscript.parent.messages[0].parts[0].state.input.prompt = serializeLanePacket(relabeledPacket)
  mismatchTranscript.child.messages[0].parts[0].text = serializeLanePacket(relabeledPacket)
  const { reader } = mismatchTranscript
  const calls: { verb: string; request: any }[] = []
  const result = await reconcileRetainedWorker({ ...recovery, packet_schema_version: "1.1", report_schema_version: "1.1", dispatch: { ...recovery.dispatch, packet_schema_version: "1.1", report_schema_version: "1.1" } }, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: reader, credentials, concordBinary: "concord-test", workerDirectory: process.cwd(),
    evidenceRunner: { async run(argv, input) { calls.push({ verb: argv[1], request: JSON.parse(input) }); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, new AbortController().signal)
  expect(result.outcome, JSON.stringify(result)).toBe("ok")
  expect(calls.map(call => call.verb)).toEqual(["worker-dispatch", "worker-complete"])
  expect(calls[0].request.authorized_packet.schema_version).toBe("1.1")
  expect(calls[0].request.report_schema_version).toBe("1.1")
  expect(calls[1].request.report_schema_version).toBe("1.1")
})

test("invalid worker content without a schema echo still refuses before signing or evidence", async () => {
  const transcript = host()
  const invalid = JSON.stringify({ status: "completed", evidence: "not-an-array" })
  transcript.parent.messages[0].parts[0].state.output = `<task id="child-recovery" state="completed">\n<task_result>\n${invalid}\n</task_result>\n</task>`
  transcript.child.messages[1].parts[0].text = invalid
  let calls = 0
  const forbidden = async (): Promise<never> => { calls++; throw new Error("must not run") }
  await expect(reconcileRetainedWorker(recovery, packet.work_id, packet.attempt_id, "retained-part", {
    sessionReader: transcript.reader, workerDirectory: process.cwd(),
    credentials: { getPrivateKey: forbidden }, evidenceRunner: { run: forbidden },
  }, new AbortController().signal)).rejects.toThrow("retained report is invalid or incomplete: worker report failed the closed agent-lane-report.v1 schema: evidence: is not of type array")
  expect(calls).toBe(0)
})
