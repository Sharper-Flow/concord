import { test, expect } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { dispatchWorker, readExportSessionMetadata, readRunSessionMetadata, validateAgentLanePacket, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"

const isRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)

// The adapter signs worker evidence with its registered client key (CD-0044).
// Tests supply a deterministic seed so dispatch does not reach the host
// credential service.
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

type ToolContext = Parameters<typeof dispatchWorker>[1] extends infer O ? (O extends { toolContext?: infer T } ? T : never) : never

// CD-0059 D1: every test that exercises the dispatch path supplies an
// authorizer so the dispatch path can exercise authorize → spawn → append
// evidence. A permissive stub acknowledges every dispatch_worker invocation
// with an `ok` envelope; tests that probe a refused authorization supply a
// stub that returns the typed error instead.
const permissiveToolContext = (): ToolContext => ({
  sessionID: "session-test", messageID: "message-test", agent: "agent-test",
  directory: "/repo", worktree: "/repo-wt", abort: new AbortController().signal,
  async ask() { return true },
  async invoke(args: unknown) {
    if (isRecord(args) && args.tool === "concord_work_transition" && args.operation === "workflow_action") {
      return { schema_version: "1.0", request_id: "auth-test", origin: "core", tool: "concord_work_transition", operation: "workflow_action", outcome: "ok", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false }
    }
    return { outcome: "ok" }
  },
})

const lane = agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
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

const report = (overrides: Record<string, unknown> = {}, model = READBACK_MODEL) => ({
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

const exportedSession = (model = READBACK_MODEL, agent = "concord-research") => JSON.stringify({
  info: { id: "session-1" },
  messages: [
    { info: { id: "message-0", sessionID: "session-1", role: "user", agent, time: { created: 0 } }, parts: [] },
    { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent, providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] },
  ],
})

const workerRunner = (model = READBACK_MODEL, output = runOutput(), agent = "concord-research"): DispatchRunner => ({
  async run(argv) {
    if (argv[1] === "run") return { exitCode: 0, stdout: output, stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(model, agent), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

test("packet validation is closed before any runner call", async () => {
  let calls = 0
  const invalid = { ...packet(), inputs: { task: "" } }
  expect(validateAgentLanePacket(invalid)).toBe(false)
  const result = await dispatchWorker(invalid, { credentials: testCredentials, runner: { async run() { calls++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } }, toolContext: permissiveToolContext() })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(calls).toBe(0)
})

test("unknown lane identity fails closed before spawn", async () => {
  const unknown = { ...packet(), lane_id: "unknown" }
  const result = await dispatchWorker(unknown, { credentials: testCredentials, runner: { async run() { throw new Error("must not spawn") } }, toolContext: permissiveToolContext() })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
})

test("spawn failure is a typed blocked outcome with bounded diagnostic", async () => {
  let argv: string[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, binary: "opencode-test", runner: { async run(args) { argv = args; throw new Error("spawn failed") } }, toolContext: permissiveToolContext() })
  expect(result.outcome).toBe("blocked")
  expect(result.error?.kind).toBe("blocked")
  expect(argv).toEqual(["opencode-test", "run", "--agent", "concord-research", "--format", "json", JSON.stringify(packet())])
})

test("spawn returning a non-zero exit code is typed as an error", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run() { return { exitCode: 1, stdout: "", stderr: "provider unavailable" } } }, toolContext: permissiveToolContext() })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
})

test("matching recorded session metadata returns bounded ok envelope", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner(), toolContext: permissiveToolContext() })
  expect(result.outcome).toBe("ok")
  expect(result.agent).toBe("concord-research")
  expect(result.readback_model).toBe(READBACK_MODEL)
  expect(result.session_id).toBe("session-1")
})

test("dispatch obtains readback from a sanitized session export", async () => {
  const calls: string[][] = []
  const base = workerRunner()
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: { async run(argv, input, signal) { calls.push(argv); return base.run(argv, input, signal) } },
    evidenceRunner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } },
    toolContext: permissiveToolContext(),
  })
  expect(result.outcome).toBe("ok")
  expect(calls.map((argv) => argv.slice(0, 2))).toEqual([["opencode", "run"], ["opencode", "export"]])
  expect(calls[1]).toEqual(["opencode", "export", "session-1", "--sanitize"])
})

// The sanitized export carries the executing agent on each message info; the
// readback takes executor identity from the latest assistant message, exactly
// where it takes model identity from.
test("readback extracts the executing agent from the latest assistant message", () => {
  expect(readExportSessionMetadata(exportedSession(), "session-1")).toEqual({ readback_model: READBACK_MODEL, readback_agent: "concord-research", session_id: "session-1" })
  const substituted = exportedSession(READBACK_MODEL, "adv")
  expect(readExportSessionMetadata(substituted, "session-1")?.readback_agent).toBe("adv")
})

// An assistant message without a typed agent string is not a readback: the
// assertion boundary fails closed rather than guessing an executor.
test("an export whose assistant message carries no agent string is not a readback", () => {
  const stripped = JSON.stringify({
    info: { id: "session-1" },
    messages: [{ info: { id: "message-1", sessionID: "session-1", role: "assistant", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] }],
  })
  expect(readExportSessionMetadata(stripped, "session-1")).toBe(null)
})

// A host that substitutes the executor — run mode falls back to the default
// agent when the named agent is not selectable — produced output no lane
// contract governs. The dispatch fails closed before any evidence is recorded,
// so a substituted executor can never drive worker-complete.
test("a dispatch executed by a substituted agent fails closed with no worker evidence", async () => {
  const evidenceCalls: string[][] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner(READBACK_MODEL, runOutput(), "adv"),
    evidenceRunner: { async run(argv) { evidenceCalls.push(argv); return { exitCode: 0, stdout: "", stderr: "" } } },
    toolContext: permissiveToolContext(),
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("agent_identity_mismatch")
  expect(result.error?.recovery_action).toBe("contact_operator")
  expect(result.error?.message).toBe('executed agent "adv" does not match the dispatched lane agent "concord-research"')
  expect(evidenceCalls).toEqual([])
})

test("a successful run records dispatch evidence before completion evidence", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    concordBinary: "concord-test",
    runner: workerRunner(),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
    toolContext: permissiveToolContext(),
  })
  expect(result.outcome).toBe("ok")
  expect(calls.map((call) => call.argv)).toEqual([["concord-test", "worker-dispatch"], ["concord-test", "worker-complete"]])

  const dispatched = JSON.parse(calls[0].input)
  expect(dispatched.work_id).toBe("work-1")
  expect(dispatched.attempt_id).toBe("attempt-1")
  expect(dispatched.lane_id).toBe(lane.id)
  expect(dispatched.lane_version).toBe(lane.version)
  expect(dispatched.lane_digest).toBe(lane.digest)
  expect(dispatched.readback_model).toBe(READBACK_MODEL)
  expect(dispatched.packet_schema_version).toBe("1.0")
  expect(dispatched.report_schema_version).toBe("1.0")
  expect(typeof dispatched.event_id).toBe("string")

  const completed = JSON.parse(calls[1].input)
  expect(completed.work_id).toBe("work-1")
  expect(completed.attempt_id).toBe("attempt-1")
  expect(completed.readback_model).toBe(READBACK_MODEL)
  expect(completed.report_schema_version).toBe("1.0")
  expect(completed.event_id).not.toBe(dispatched.event_id)
})

test("a run whose evidence cannot be recorded is not reported as a success", async () => {
  const refuse = async (argv: string[]) => argv[1] === "worker-dispatch" ? { exitCode: 1, stdout: "", stderr: "evidence write refused" } : { exitCode: 0, stdout: "", stderr: "" }
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner(),
    evidenceRunner: { async run(argv) { return refuse(argv) } },
    toolContext: permissiveToolContext(),
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toBe("evidence write refused")
})

test("a completion that cannot be recorded is not reported as a success", async () => {
  let recorded = 0
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner(),
    evidenceRunner: { async run(argv) { recorded++; return argv[1] === "worker-complete" ? { exitCode: 1, stdout: "", stderr: "worker attempt belongs to a different work item" } : { exitCode: 0, stdout: "", stderr: "" } } },
    toolContext: permissiveToolContext(),
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
      toolContext: permissiveToolContext(),
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

test("the adapter does not declare a model — argv carries no --model", async () => {
  let argv: string[] = []
  await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run(args) { argv = args; return { exitCode: 0, stdout: runOutput(), stderr: "" } } }, evidenceRunner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } }, toolContext: permissiveToolContext() })
  expect(argv).not.toContain("--model")
})

test("the readback shape is recorded verbatim regardless of host configuration", async () => {
  const hostExecuted = "zai-coding-plan/glm-5.3"
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner(hostExecuted), toolContext: permissiveToolContext() })
  expect(result.outcome).toBe("ok")
  expect(result.readback_model).toBe(hostExecuted)
})

test("an unknown readback is recorded as-is and not refused", async () => {
  const hostExecuted = "openai/not-declared"
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner(hostExecuted), toolContext: permissiveToolContext() })
  expect(result.outcome).toBe("ok")
  expect(result.readback_model).toBe(hostExecuted)
})

// CD-0063 D5: the conduct corpus reaches a project as an absolute glob —
// `<stable root>/instructions/*.md` — and a corpus file that reaches an agent
// must be bound by content hash, not merely named. An absolute glob resolves
// against one fixed directory, so it expands exactly.
test("host prompt provenance binds an absolute corpus glob file for file", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-cwd-"))
  const stableRoot = await mkdtemp(path.join(os.tmpdir(), "provenance-stable-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await Bun.write(`${stableRoot}/instructions/asking.md`, "# asking v1\n")
    await Bun.write(`${stableRoot}/instructions/voice.md`, "# voice v1\n")
    await Bun.write(
      `${configDir}/opencode.jsonc`,
      `{"instructions": [${JSON.stringify(`${stableRoot}/instructions/*.md`)}]}\n`,
    )
    const first = await computeHostPromptProvenance("research", dir)

    const bound = first.sources.filter(s => s.kind === "instruction_file")
    expect(bound.map(s => s.path).sort()).toEqual([
      `${stableRoot}/instructions/asking.md`,
      `${stableRoot}/instructions/voice.md`,
    ])
    expect(bound.every(s => typeof s.sha256 === "string" && s.sha256.startsWith("sha256:"))).toBe(true)

    // A silent corpus change must move the digest — this is the guarantee the
    // link entry exists to carry.
    await Bun.write(`${stableRoot}/instructions/asking.md`, "# asking v2 — silently changed\n")
    expect((await computeHostPromptProvenance("research", dir)).digest).not.toBe(first.digest)

    // An absolute glob matching nothing is named rather than dropped.
    await Bun.write(`${configDir}/opencode.jsonc`, `{"instructions": [${JSON.stringify(`${stableRoot}/absent/*.md`)}]}\n`)
    const empty = await computeHostPromptProvenance("research", dir)
    expect(empty.sources.filter(s => s.kind === "unenumerated").map(s => s.path)).toContain(
      `${stableRoot}/absent/*.md`,
    )
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

// CD-0032 / issue #103: provenance is deterministic for the same inputs and
// changes when an enumerated source changes.
import { computeHostPromptProvenance } from "./dispatch"
import { mkdtemp } from "node:fs/promises"
import * as path from "node:path"
import * as os from "node:os"

test("host prompt provenance is deterministic and content-bound", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-"))
  // Pointed at an empty config directory so the result depends on the fixture
  // rather than on whatever the machine running the suite has installed.
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
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
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

// Issue #408: the global AGENTS.md is injected into every session but sits
// outside the spawn directory's ancestry, so the upward walk alone can never
// reach it and it bound to nothing.
test("host prompt provenance binds the global AGENTS.md", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-cwd-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await Bun.write(`${configDir}/AGENTS.md`, "# global v1\n")
    const first = await computeHostPromptProvenance("research", dir)
    const globalAgents = first.sources.find(s => s.kind === "agents_md" && s.path === `${configDir}/AGENTS.md`)
    expect(globalAgents?.sha256).toMatch(/^sha256:/)

    await Bun.write(`${configDir}/AGENTS.md`, "# global v2 — silently changed\n")
    expect((await computeHostPromptProvenance("research", dir)).digest).not.toBe(first.digest)
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

// Issue #409: instruction files the host config declares reach every lane. They
// are bound when they resolve exactly, and named when they cannot, so nothing
// injected is absent from the manifest.
test("host prompt provenance binds config-declared instruction files", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-cwd-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await Bun.write(`${configDir}/rules.md`, "# rules v1\n")
    await Bun.write(
      `${configDir}/opencode.jsonc`,
      `{\n  // a comment, and a trailing comma\n  "instructions": [\n    ${JSON.stringify(`${configDir}/rules.md`)},\n    "https://example.com/remote.md",\n    "packages/*/AGENTS.md",\n  ],\n}\n`,
    )
    const first = await computeHostPromptProvenance("research", dir)

    const bound = first.sources.find(s => s.kind === "instruction_file" && s.path === `${configDir}/rules.md`)
    expect(bound?.sha256).toMatch(/^sha256:/)

    const named = first.sources.filter(s => s.kind === "unenumerated").map(s => s.path)
    expect(named).toContain("https://example.com/remote.md")
    expect(named).toContain("packages/*/AGENTS.md")

    await Bun.write(`${configDir}/rules.md`, "# rules v2 — silently changed\n")
    expect((await computeHostPromptProvenance("research", dir)).digest).not.toBe(first.digest)
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

// An unparseable config is never guessed at. It is named, so the operator can
// see that a surface exists which the manifest could not read.
test("host prompt provenance names a config it cannot parse", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-cwd-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await Bun.write(`${configDir}/opencode.json`, "{ this is not json")
    const result = await computeHostPromptProvenance("research", dir)
    expect(result.sources.filter(s => s.kind === "unenumerated").map(s => s.path)).toContain(
      `${configDir}/opencode.json`,
    )
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

// CD-0056 D7 / issue #333: the adapter parses the report it already receives,
// carries its evidence into worker-complete, and turns anything it cannot admit
// into a typed worker-fail rather than a completion.
import { readWorkerReport, resolveWorkerReport, validateAgentLaneReport, validateAgainstSchema } from "./dispatch"

async function terminalEvidence(output: string) {
  const calls: { argv: string[]; input: string }[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    concordBinary: "concord-test",
    runner: workerRunner(READBACK_MODEL, output),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
    toolContext: permissiveToolContext(),
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
  expect(readRunSessionMetadata(stdout)).toEqual({ session_id: "session-1" })
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

// CD-0059 D1: the adapter authorizes dispatch_worker before spawning the
// worker, so a refused authorization aborts before any process is started.
// The test hands the adapter a stub ToolContext whose invoke returns the
// typed refusal; the adapter aborts before touching the spawn runner.
test("dispatchWorker aborts when dispatch_worker authorization is refused", async () => {
  let spawned = 0
  let authorizeCalls = 0
  const toolContext = {
    sessionID: "session-cd0059", messageID: "message-cd0059", agent: "agent-cd0059",
    directory: "/repo", worktree: "/repo-wt", abort: new AbortController().signal,
    async ask() { return true },
    async invoke(args: unknown) {
      if (isRecord(args) && args.tool === "concord_work_transition" && args.operation === "workflow_action") {
        authorizeCalls++
        const input = isRecord(args.input) ? args.input : null
        if (input && (input as { action_id?: unknown }).action_id === "dispatch_worker") {
          return { schema_version: "1.0", request_id: "auth-1", origin: "core", tool: "concord_work_transition", operation: "workflow_action", outcome: "error", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, error: { kind: "unauthorized_dispatch", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "none", message: "no authorized dispatch window exists for this work item at the current step" } }
        }
      }
      return { outcome: "ok" }
    },
  }
  const result = await dispatchWorker(packet(), {
    credentials: testCredentials,
    runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
    evidenceRunner: { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } },
    toolContext: toolContext as unknown as Parameters<typeof dispatchWorker>[1] extends infer O ? (O extends { toolContext?: infer T } ? T : never) : never,
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("unauthorized_dispatch")
  expect(spawned).toBe(0)
  expect(authorizeCalls).toBe(1)
})

// CD-0059 D1 (close the hatch): dispatchWorker refuses to spawn when no
// authorizer is supplied. The dispatch path requires authorize → spawn →
// append evidence, unconditionally. Omitting the authorizer is the bypass
// being removed; the typed error envelope names the missing authorization
// so an operator can tell it apart from a refused authorization.
test("dispatchWorker refuses and does not spawn when no authorizer is supplied", async () => {
  let spawned = 0
  const result = await dispatchWorker(packet(), {
    credentials: testCredentials,
    runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
    evidenceRunner: { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("unauthorized_dispatch")
  expect(spawned).toBe(0)
})
