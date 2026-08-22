import { test, expect } from "bun:test"
import { agentLanes, routingPolicies, routingPolicyManifestDigest, routingPolicyVersion } from "./generated-agent-lanes"
import { dispatchWorker, preferredModelForLane, readExportSessionMetadata, readRunSessionMetadata, resetRoutingPolicyForTesting, validateAgentLanePacket, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"

// The adapter signs worker evidence with its registered client key (CD-0044).
// Tests supply a deterministic seed so dispatch does not reach the host
// credential service.
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

const lane = agentLanes[0]
const packet = (): AgentLanePacket => ({
  schema_version: "1.0",
  attempt_id: "attempt-1",
  lane_id: lane.id,
  lane_version: lane.version,
  lane_digest: lane.digest,
  work_id: "work-1",
  step_id: "step-1",
  inputs: { task: "Run the bounded worker fixture." },
})

// The worker's agent-lane-report.v1 report travels as the text of a `text` host
// run event, at part.text, which is the only place the host carries model text.
const reportEvidence = () => [
  { obligation: "source_citations", detail: "contracts/agent-lanes.v1.json" },
  { obligation: "bounded_findings", detail: "the research lane declares three obligations" },
  { obligation: "uncertainties", detail: "none" },
]

const report = (overrides: Record<string, unknown> = {}, model = preferredModelForLane(lane)) => ({
  schema_version: "1.0",
  attempt_id: "attempt-1",
  lane_id: lane.id,
  lane_version: lane.version,
  lane_digest: lane.digest,
  readback_model: model,
  status: "completed",
  evidence: reportEvidence(),
  ...overrides,
})

const reportEvent = (document: unknown) => JSON.stringify({ type: "text", timestamp: 3, sessionID: "session-1", part: { type: "text", text: typeof document === "string" ? document : JSON.stringify(document) } })

const runOutput = (extra = "", carried: unknown = report()) => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  carried === null ? "" : reportEvent(carried),
  JSON.stringify({ type: "step_finish", timestamp: 2, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
  extra,
].filter(Boolean).join("\n")

const availableModels = () => routingPolicies.flatMap(policy => policy.resolution_set).join("\n")

const exportedSession = (model = preferredModelForLane(lane)) => JSON.stringify({
  info: { id: "session-1" },
  messages: [{ info: { id: "message-1", sessionID: "session-1", role: "assistant", providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] }],
})

const workerRunner = (model = preferredModelForLane(lane), output = runOutput()): DispatchRunner => ({
  async run(argv) {
    if (argv[1] === "models") return { exitCode: 0, stdout: availableModels(), stderr: "" }
    if (argv[1] === "run") return { exitCode: 0, stdout: output, stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(model), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

test("generated adapter routing policy digest and preferred cross-validation are deterministic", async () => {
  const digest = (await Bun.file(new URL("../../contracts/routing-policy.digest", import.meta.url)).text()).trim()
  expect(digest).toBe(routingPolicyManifestDigest)
  expect(routingPolicyVersion).toBe("routing-v1")
  for (const policy of routingPolicies) {
    expect(policy.resolution_set[0]).toBe(policy.preferred_model)
    expect(policy.resolution_set.length).toBeGreaterThanOrEqual(1)
  }
})

function hostPolicy(preferred = "host/preferred", fallback = "host/fallback") {
  return { schema_version: "1.0", registry: "routing_policy", version: "routing-v1", policies: routingPolicies.map(policy => ({ capability_class: policy.capability_class, preferred_model: preferred, resolution_set: [preferred, fallback] })) }
}

test("host routing policy takes precedence and supplies the dispatch model", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "routing-policy-"))
  const policyPath = `${dir}/routing-policy.json`
  await Bun.write(policyPath, JSON.stringify(hostPolicy()))
  process.env.CONCORD_ROUTING_POLICY = policyPath
  resetRoutingPolicyForTesting()
  let argv: string[] = []
  const runner: DispatchRunner = { async run(args) {
    if (args[1] === "models") return { exitCode: 0, stdout: "host/preferred\nhost/fallback", stderr: "" }
    if (args[1] === "run") { argv = args; return { exitCode: 0, stdout: runOutput(), stderr: "" } }
    return { exitCode: 0, stdout: exportedSession("host/preferred"), stderr: "" }
  } }
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner })
  expect(result.outcome).toBe("ok")
  expect(argv).toContain("host/preferred")
  expect(result.routing_policy_digest).not.toBe(routingPolicyManifestDigest)
  delete process.env.CONCORD_ROUTING_POLICY
  resetRoutingPolicyForTesting()
})

test("host policy names every unavailable model and its source", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "routing-policy-missing-"))
  const policyPath = `${dir}/routing-policy.json`
  await Bun.write(policyPath, JSON.stringify(hostPolicy("host/missing", "host/fallback")))
  process.env.CONCORD_ROUTING_POLICY = policyPath
  resetRoutingPolicyForTesting()
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run(args) {
    if (args[1] === "models") return { exitCode: 0, stdout: "host/other", stderr: "" }
    throw new Error("must not dispatch")
  } } })
  expect(result.error?.kind).toBe("routing_policy_model_unavailable")
  expect(result.error?.message).toContain(policyPath)
  expect(result.error?.message).toContain("host/missing")
  expect(result.error?.message).toContain("host/fallback")
  delete process.env.CONCORD_ROUTING_POLICY
  resetRoutingPolicyForTesting()
})

test("policy preferred model must be the first resolution member", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "routing-policy-anchor-"))
  const policyPath = `${dir}/routing-policy.json`
  const policy = hostPolicy()
  policy.policies[0].resolution_set = ["host/fallback", "host/preferred"]
  await Bun.write(policyPath, JSON.stringify(policy))
  process.env.CONCORD_ROUTING_POLICY = policyPath
  resetRoutingPolicyForTesting()
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run() { throw new Error("must not dispatch") } } })
  expect(result.error?.kind).toBe("routing_policy_invalid")
  expect(result.error?.message).toContain("preferred_model")
  delete process.env.CONCORD_ROUTING_POLICY
  resetRoutingPolicyForTesting()
})

test("packet validation is closed before any runner call", async () => {
  let calls = 0
  const invalid = { ...packet(), inputs: { task: "" } }
  expect(validateAgentLanePacket(invalid)).toBe(false)
  const result = await dispatchWorker(invalid, { credentials: testCredentials, runner: { async run() { calls++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(calls).toBe(0)
})

test("unknown lane identity fails closed before spawn", async () => {
  const unknown = { ...packet(), lane_id: "unknown" }
  const result = await dispatchWorker(unknown, { credentials: testCredentials, runner: { async run() { throw new Error("must not spawn") } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
})

test("spawn failure is a typed blocked outcome with bounded diagnostic", async () => {
  let argv: string[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, binary: "opencode-test", runner: { async run(args) { if (args[1] === "models") return { exitCode: 0, stdout: availableModels(), stderr: "" }; argv = args; return { exitCode: 1, stdout: "", stderr: "provider unavailable", fallbackExhausted: true } } } })
  expect(result.outcome).toBe("blocked")
  expect(result.error?.kind).toBe("blocked")
  expect(argv).toEqual(["opencode-test", "run", "--agent", "concord-research", "--model", preferredModelForLane(lane), "--format", "json", JSON.stringify(packet())])
})

test("spawn failure without exhaustion evidence is not mislabeled blocked", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run(args) { if (args[1] === "models") return { exitCode: 0, stdout: availableModels(), stderr: "" }; return { exitCode: 1, stdout: "", stderr: "provider unavailable" } } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
})

test("recorded session metadata proves the executing model and exposes fallback", async () => {
  const fallback = "zai-coding-plan/glm-5.3"
  const fallbackOutput = runOutput(JSON.stringify({ type: "message.updated", properties: { sessionId: "session-1", status: { action: { reason: "account_rate_limit" } } } }))
  expect(readRunSessionMetadata(fallbackOutput)).toEqual({ session_id: "session-1", fallback_reason: "rate_limit" })
  expect(readExportSessionMetadata(exportedSession(fallback), "session-1")).toEqual({ readback_model: fallback, session_id: "session-1" })
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner(fallback, fallbackOutput) })
  expect(result.outcome).toBe("fallback")
  expect(result.readback_model).toBe(fallback)
  expect(result.error?.kind).toBe("fallback")
  expect(result.resolution_role).toBe("fallback")
  expect(result.fallback_reason).toBe("rate_limit")
})

test("matching recorded session metadata returns bounded ok envelope", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner() })
  expect(result.outcome).toBe("ok")
  expect(result.agent).toBe("concord-research")
  expect(result.resolved_model).toBe(preferredModelForLane(lane))
  expect(result.resolution_role).toBe("preferred")
  expect(result.fallback_reason).toBe("")
  expect(result.readback_model).toBe(preferredModelForLane(lane))
  expect(result.session_id).toBe("session-1")
})

test("dispatch obtains readback from a sanitized session export", async () => {
  const calls: string[][] = []
  const base = workerRunner()
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: { async run(argv, input, signal) { calls.push(argv); return base.run(argv, input, signal) } },
    evidenceRunner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("ok")
  expect(calls.map((argv) => argv.slice(0, 2))).toEqual([["opencode", "models"], ["opencode", "run"], ["opencode", "export"]])
  expect(calls[2]).toEqual(["opencode", "export", "session-1", "--sanitize"])
})

test("undeclared readback is rejected instead of becoming an implicit fallback", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner("openai/not-declared") })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("model_identity_mismatch")
})

test("a successful run records dispatch evidence before completion evidence", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    concordBinary: "concord-test",
    runner: workerRunner(),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("ok")
  expect(calls.map((call) => call.argv)).toEqual([["concord-test", "worker-dispatch"], ["concord-test", "worker-complete"]])

  const dispatched = JSON.parse(calls[0].input)
  expect(dispatched.work_id).toBe("work-1")
  expect(dispatched.attempt_id).toBe("attempt-1")
  expect(dispatched.lane_id).toBe(lane.id)
  expect(dispatched.lane_version).toBe(lane.version)
  expect(dispatched.lane_digest).toBe(lane.digest)
  expect(dispatched.routing_policy_version).toBe(routingPolicyVersion)
  expect(dispatched.routing_policy_digest).toBe(routingPolicyManifestDigest)
  expect(dispatched.resolved_model).toBe(preferredModelForLane(lane))
  expect(dispatched.resolution_role).toBe("preferred")
  expect(dispatched.fallback_reason).toBe("")
  expect(dispatched.packet_schema_version).toBe("1.0")
  expect(dispatched.report_schema_version).toBe("1.0")
  expect(typeof dispatched.event_id).toBe("string")

  const completed = JSON.parse(calls[1].input)
  expect(completed.work_id).toBe("work-1")
  expect(completed.attempt_id).toBe("attempt-1")
  expect(completed.readback_model).toBe(preferredModelForLane(lane))
  expect(completed.report_schema_version).toBe("1.0")
  expect(completed.event_id).not.toBe(dispatched.event_id)
})

test("a declared fallback is recorded as fallback evidence, not as a failure", async () => {
  const fallbackOutput = runOutput(JSON.stringify({ type: "message.updated", properties: { sessionId: "session-1", status: { action: { reason: "account_rate_limit" } } } }))
  const calls: { argv: string[]; input: string }[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner("zai-coding-plan/glm-5.3", fallbackOutput),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("fallback")
  expect(calls).toHaveLength(2)
  const dispatched = JSON.parse(calls[0].input)
  const completed = JSON.parse(calls[1].input)
  expect(dispatched.resolved_model).toBe("zai-coding-plan/glm-5.3")
  expect(dispatched.resolution_role).toBe("fallback")
  expect(dispatched.fallback_reason).toBe("rate_limit")
  expect(completed.readback_model).toBe(dispatched.resolved_model)
})

test("a run whose evidence cannot be recorded is not reported as a success", async () => {
  const refuse = async (argv: string[]) => argv[1] === "worker-dispatch" ? { exitCode: 1, stdout: "", stderr: "resolved model is not a declared routing-policy member" } : { exitCode: 0, stdout: "", stderr: "" }
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner(),
    evidenceRunner: { async run(argv) { return refuse(argv) } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toBe("resolved model is not a declared routing-policy member")
})

test("a completion that cannot be recorded is not reported as a success", async () => {
  let recorded = 0
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner(),
    evidenceRunner: { async run(argv) { recorded++; return argv[1] === "worker-complete" ? { exitCode: 1, stdout: "", stderr: "worker attempt belongs to a different work item" } : { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(recorded).toBe(2)
  expect(result.outcome).toBe("error")
  expect(result.error?.message).toBe("worker attempt belongs to a different work item")
})

test("generic host agents are not dispatchable and never spawn or record", async () => {
  for (const generic of ["general", "explore", "build", "plan"]) {
    let spawned = 0
    let recorded = 0
    const result = await dispatchWorker({ ...packet(), lane_id: generic }, { credentials: testCredentials,
      runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
      evidenceRunner: { async run() { recorded++; return { exitCode: 0, stdout: "", stderr: "" } } },
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("invalid_input")
    expect(spawned).toBe(0)
    expect(recorded).toBe(0)
  }
})

test("the registered lane set is closed and every agent name is Concord-owned", () => {
  expect(agentLanes.map((entry) => entry.id).sort()).toEqual(["implement", "research", "review", "verify"])
  for (const entry of agentLanes) expect(`concord-${entry.id}`).toMatch(/^concord-[a-z]+$/)
})

// CD-0032 / issue #103: provenance is deterministic for the same inputs and
// changes when an enumerated source changes.
import { computeHostPromptProvenance } from "./dispatch"
import { mkdtemp } from "node:fs/promises"
import * as path from "node:path"
import * as os from "node:os"

test("host prompt provenance is deterministic and content-bound", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-"))
  await Bun.write(`${dir}/AGENTS.md`, "# instructions v1\n")
  const first = await computeHostPromptProvenance("research", dir)
  const second = await computeHostPromptProvenance("research", dir)
  expect(first.digest).toBe(second.digest)
  expect(first.digest).toMatch(/^sha256:[0-9a-f]{64}$/)
  const agentsMd = first.sources.find((s) => s.kind === "agents_md")
  expect(agentsMd?.path).toBe(`${dir}/AGENTS.md`)
  expect(agentsMd?.sha256).toMatch(/^sha256:/)
  expect(first.sources.filter((s) => s.kind === "unenumerated").length).toBeGreaterThan(0)
  await Bun.write(`${dir}/AGENTS.md`, "# instructions v2 — silently changed\n")
  const changed = await computeHostPromptProvenance("research", dir)
  expect(changed.digest).not.toBe(first.digest)
})

// CD-0056 D7 / issue #333: the adapter parses the report it already receives,
// carries its evidence into worker-complete, and turns anything it cannot admit
// into a typed worker-fail rather than a completion.
import { readWorkerReport, resolveWorkerReport, validateAgentLaneReport, validateAgainstSchema } from "./dispatch"

async function terminalEvidence(output: string) {
  const calls: { argv: string[]; input: string }[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    concordBinary: "concord-test",
    runner: workerRunner(preferredModelForLane(lane), output),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  return { result, verbs: calls.map((call) => call.argv[1]), payloads: calls.map((call) => JSON.parse(call.input)) }
}

test("a valid completed report carries its reported evidence into worker-complete", async () => {
  const { result, verbs, payloads } = await terminalEvidence(runOutput())
  expect(result.outcome).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence_origin).toBe("reported")
  expect(payloads[1].evidence).toEqual(reportEvidence())
  expect(payloads[1].report_schema_version).toBe("1.0")
})

test("a missing report is worker-fail with invalid_report, not a completion", async () => {
  const { result, verbs, payloads } = await terminalEvidence(runOutput("", null))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toBe("worker output carried no agent-lane-report.v1 report")
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_report")
})

test("an unparseable report is worker-fail with invalid_report", async () => {
  const { result, verbs, payloads } = await terminalEvidence(runOutput("", '{"schema_version": "1.0", "lane_id":'))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("malformed JSON document")
  expect(result.error?.kind).toBe("invalid_report")
})

test("a report naming an obligation outside the enum is worker-fail with invalid_report", async () => {
  const evidence = [...reportEvidence(), { obligation: "vibes", detail: "felt right" }]
  const { verbs, payloads } = await terminalEvidence(runOutput("", report({ evidence })))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("evidence[3].obligation")
  expect(payloads[1].detail).toContain("outside the closed enum")
})

test("a report that does not echo the dispatched attempt is worker-fail with invalid_report", async () => {
  const { verbs, payloads } = await terminalEvidence(runOutput("", report({ attempt_id: "attempt-other" })))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toBe('worker report attempt_id "attempt-other" does not match dispatched packet "attempt-1"')
})

test("a reported failure is recorded as the worker's own failure, not an invalid report", async () => {
  const evidence = [{ obligation: "uncertainties", detail: "the cited source was unreachable" }]
  const { result, verbs, payloads } = await terminalEvidence(runOutput("", report({ status: "failed", evidence })))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("worker_error")
  expect(payloads[1].detail).toBe("worker reported failure: uncertainties: the cited source was unreachable")
  expect(result.error?.kind).toBe("error")
})

test("the report scan reads part.text only and separates absence from malformed content", () => {
  expect(readWorkerReport(runOutput()).report).toEqual(report())
  expect(readWorkerReport(runOutput("", null))).toEqual({ report: null, malformed: false })
  expect(readWorkerReport(reportEvent("{ not json"))).toEqual({ report: null, malformed: true })
  // No key other than part.text on a `text` event carries the report.
  for (const key of ["report", "result", "output", "data", "text"]) {
    const misplaced = JSON.stringify({ type: "text", timestamp: 3, sessionID: "session-1", [key]: report(), part: { type: "text", [key]: report() } })
    expect(readWorkerReport(misplaced)).toEqual({ report: null, malformed: false })
  }
  // A step_finish part is not a text part, whatever it holds.
  const stepPart = JSON.stringify({ type: "step_finish", timestamp: 3, sessionID: "session-1", part: { type: "step-finish", text: JSON.stringify(report()) } })
  expect(readWorkerReport(stepPart)).toEqual({ report: null, malformed: false })
})

// This is the carrier-shape regression guard. The envelope below is a real
// `opencode run --format json` capture, reproduced field for field, with the
// session identifiers normalised and the captured `{"ok":true}` text replaced by
// a valid agent-lane-report.v1 document. The first line is a host plugin log
// line from that same capture: it shares stdout with the run stream and is not a
// run event. If the host ever moves model text off `text` -> part.text, this
// test is what fails.
const realRunCaptureEnvelope = (text: string) => [
  JSON.stringify({ ts: "2026-08-22T05:08:17.270Z", level: "info", plugin: "opencode-model-routing", event: "config.loaded", agentCount: 13 }),
  JSON.stringify({ type: "step_start", timestamp: 1787375308335, sessionID: "session-1", part: { id: "prt_1", messageID: "msg_1", sessionID: "session-1", type: "step-start" } }),
  JSON.stringify({ type: "text", timestamp: 1787375309617, sessionID: "session-1", part: { id: "prt_2", messageID: "msg_1", sessionID: "session-1", type: "text", text, time: { start: 1787375309544, end: 1787375309584 } } }),
  JSON.stringify({ type: "step_finish", timestamp: 1787375309617, sessionID: "session-1", part: { id: "prt_3", reason: "stop", messageID: "msg_1", sessionID: "session-1", type: "step-finish", tokens: { total: 29608, input: 29531, output: 7, reasoning: 70, cache: { write: 0, read: 0 } }, cost: 0 } }),
].join("\n")

test("a real opencode run --format json capture drives worker-complete, plugin log line and all", async () => {
  const stdout = realRunCaptureEnvelope(JSON.stringify(report()))
  expect(readRunSessionMetadata(stdout)).toEqual({ session_id: "session-1", fallback_reason: null })
  expect(readWorkerReport(stdout).report).toEqual(report())
  const { result, verbs, payloads } = await terminalEvidence(stdout)
  expect(result.outcome).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence).toEqual(reportEvidence())
})

test("the last text part wins when an earlier part is working prose", async () => {
  const stdout = [
    JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
    reportEvent("I read the lane registry and will now return the report."),
    reportEvent(report({ evidence: [{ obligation: "uncertainties", detail: "superseded draft" }] })),
    reportEvent(report()),
    JSON.stringify({ type: "step_finish", timestamp: 2, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
  ].join("\n")
  expect(readWorkerReport(stdout)).toEqual({ report: report(), malformed: false })
  const { verbs, payloads } = await terminalEvidence(stdout)
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence).toEqual(reportEvidence())
})

test("a fenced report is admitted and a fence around prose is not salvaged", async () => {
  const fenced = "```json\n" + JSON.stringify(report()) + "\n```"
  expect(readWorkerReport(runOutput("", fenced)).report).toEqual(report())
  expect(readWorkerReport(runOutput("", "```\n" + JSON.stringify(report()) + "\n```")).report).toEqual(report())
  const { verbs } = await terminalEvidence(runOutput("", fenced))
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  // Prose around the JSON is not mined for a report; it fails closed.
  expect(readWorkerReport(runOutput("", `Here is the report: ${JSON.stringify(report())} — done.`))).toEqual({ report: null, malformed: false })
})

test("a run with no text part at all is worker-fail with invalid_report", async () => {
  const stdout = [
    JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
    JSON.stringify({ type: "step_finish", timestamp: 2, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
  ].join("\n")
  expect(readWorkerReport(stdout)).toEqual({ report: null, malformed: false })
  const { result, verbs, payloads } = await terminalEvidence(stdout)
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toBe("worker output carried no agent-lane-report.v1 report")
  expect(result.error?.kind).toBe("invalid_report")
})

test("report resolution admits only a schema-valid, packet-bound report", () => {
  expect(resolveWorkerReport(runOutput(), packet())).toEqual({ report: report() as never })
  expect(resolveWorkerReport(runOutput("", report({ lane_digest: "sha256:" + "0".repeat(64) })), packet())).toEqual({
    detail: `worker report lane_digest ${JSON.stringify("sha256:" + "0".repeat(64))} does not match dispatched packet ${JSON.stringify(lane.digest)}`,
  })
  const missing = resolveWorkerReport(runOutput("", report({ status: undefined })), packet())
  expect("detail" in missing && missing.detail).toContain("missing required property status")
})

test("the report schema validator resolves $defs through $ref", () => {
  expect(validateAgentLaneReport(report())).toBe(true)
  expect(validateAgentLaneReport(report({ evidence: [{ obligation: "source_citations", detail: "x", extra: 1 }] }))).toBe(false)
  expect(validateAgentLaneReport(report({ evidence: [{ obligation: "source_citations" }] }))).toBe(false)
  expect(validateAgentLaneReport(report({ evidence: [] }))).toBe(false)
})

test("validateSchema resolves a local $ref and fails closed on an unresolvable one", () => {
  const schema = { type: "object", required: ["value"], additionalProperties: false, properties: { value: { $ref: "#/$defs/token" } }, $defs: { token: { type: "string", minLength: 2 } } }
  expect(validateAgainstSchema(schema, { value: "ok" })).toBe(true)
  expect(validateAgainstSchema(schema, { value: "x" })).toBe(false)
  expect(validateAgainstSchema(schema, { value: 7 })).toBe(false)
  const dangling: string[] = []
  expect(validateAgainstSchema({ $ref: "#/$defs/absent" }, "anything", dangling)).toBe(false)
  expect(dangling[0]).toContain("unresolvable $ref")
  expect(validateAgainstSchema({ $ref: "https://example.invalid/schema" }, "anything")).toBe(false)
})

test("validateSchema enforces enum membership and names the failing path", () => {
  const schema = { type: "object", required: ["status"], properties: { status: { enum: ["completed", "failed"] } } }
  expect(validateAgainstSchema(schema, { status: "completed" })).toBe(true)
  const failures: string[] = []
  expect(validateAgainstSchema(schema, { status: "done" }, failures)).toBe(false)
  expect(failures[0]).toBe('status: "done" is outside the closed enum')
  expect(validateAgainstSchema({ enum: [1, 2] }, 3)).toBe(false)
  expect(validateAgainstSchema({ enum: [{ a: 1 }] }, { a: 1 })).toBe(true)
  expect(validateAgainstSchema({ enum: "completed" }, "completed")).toBe(false)
})

test("validateSchema names the failing array item and the undeclared property", () => {
  const schema = { type: "array", items: { type: "object", additionalProperties: false, required: ["id"], properties: { id: { type: "string" } } } }
  const failures: string[] = []
  expect(validateAgainstSchema(schema, [{ id: "a" }, { id: "b", extra: true }], failures)).toBe(false)
  expect(failures[0]).toBe("[1]: carries undeclared property extra")
  expect(validateAgainstSchema(schema, [{ id: "a" }])).toBe(true)
})
