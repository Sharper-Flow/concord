import { test, expect } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { completeWorkerAttempt, dispatchWorker, readExportSessionMetadata, readRunSessionMetadata, validateAgentLanePacket, type AgentLanePacket, type DispatchAuthorizer, type DispatchRunner } from "./dispatch"
import { DispatchWindows } from "./dispatch-window"
import type { CredentialStore } from "./credentials"

const isRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)

// The adapter signs worker evidence with its registered client key (CD-0044).
// Tests supply a deterministic seed so dispatch does not reach the host
// credential service.
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }


// CD-0059 D1: every test that exercises the dispatch path supplies an
// authorizer so the dispatch path can exercise authorize -> spawn -> append
// evidence. A permissive stub acknowledges every dispatch_worker request with
// an `ok` core envelope; tests that probe a refused authorization supply a
// stub that returns the typed error instead.
const coreOk = () => ({ schema_version: "1.0", request_id: "auth-test", origin: "core", tool: "concord_work_transition", operation: "workflow_action", outcome: "ok", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false })

const permissiveAuthorizer = (): DispatchAuthorizer => async () => coreOk()

const lane = agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
// CD-0067 D6: dispatchWorker needs packetDigest on the happy path so the
// signed assertion can quote the value the core recorded. Tests that
// exercise the refusal branch omit it deliberately.
const PACKET_DIGEST = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
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

// CD-0102 D5: the host wraps a finished worker's final text in a task element
// that carries the worker session identifier, which is what the completion path
// exports for readback.
const taskWrap = (text: string, id = "session-1", state = "completed") =>
  [`<task id="${id}" state="${state}">`, "<task_result>", text, "</task_result>", "</task>"].join("\n")

const workerBody = (carried: unknown = report()) =>
  taskWrap(carried === null ? "" : typeof carried === "string" ? carried : JSON.stringify(carried))

// The completion path reads the worker session back and records evidence. It
// never starts a process, so the runner answers `export` and nothing else.
const readbackRunner = (model = READBACK_MODEL, agent = "concord-research"): DispatchRunner => ({
  async run(argv) {
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(model, agent), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

const SIGNAL = new AbortController().signal
const SESSION = "session-parent"

type CompleteOptions = Parameters<typeof completeWorkerAttempt>[3]
const acceptingEvidence = (): DispatchRunner => ({ async run() { return { exitCode: 0, stdout: "", stderr: "" } } })
const complete = (body: string, options: Partial<CompleteOptions> = {}, dispatched: AgentLanePacket = packet()) =>
  completeWorkerAttempt(lane, dispatched, body, { credentials: testCredentials, readbackRunner: readbackRunner(), evidenceRunner: acceptingEvidence(), packetDigest: PACKET_DIGEST, ...options }, SIGNAL)

test("packet validation is closed before any runner call", async () => {
  let calls = 0
  const invalid = { ...packet(), inputs: { task: "" } }
  expect(validateAgentLanePacket(invalid)).toBe(false)
  const result = await dispatchWorker(invalid, { credentials: testCredentials, runner: { async run() { calls++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } }, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows: new DispatchWindows() })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(calls).toBe(0)
})

test("unknown lane identity fails closed before a window opens", async () => {
  const windows = new DispatchWindows()
  const unknown = { ...packet(), lane_id: "unknown" }
  const result = await dispatchWorker(unknown, { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(windows.has(SESSION)).toBe(false)
})

// CD-0102 D1: an authorized dispatch opens the window and returns before the
// worker runs. The host issues the Task call, so the adapter starts no process
// and asserts no model here.
test("an authorized dispatch opens one window and returns a directive", async () => {
  const windows = new DispatchWindows()
  const result = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(result.agent).toBe("concord-research")
  expect(result.readback_model).toBe(null)
  expect(windows.has(SESSION)).toBe(true)
})

test("a dispatch that cannot name its calling session opens no window", async () => {
  const windows = new DispatchWindows()
  const result = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, windows })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(windows.has(SESSION)).toBe(false)
})

// Two open windows would let one authorized dispatch start whichever worker the
// next Task call happened to name.
test("a second dispatch on one session is refused while a window is open", async () => {
  const windows = new DispatchWindows()
  const first = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows })
  expect(first.outcome).toBe("ok")
  const second = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows })
  expect(second.outcome).toBe("error")
  expect(second.error?.message).toMatch(/already holds an open dispatch window/)
})

test("a result that is not a host task result is not a completion", async () => {
  const result = await complete("the worker said some prose")
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_report")
})

test("matching recorded session metadata returns bounded ok envelope", async () => {
  const result = await complete(workerBody())
  expect(result.outcome).toBe("ok")
  expect(result.agent).toBe("concord-research")
  expect(result.readback_model).toBe(READBACK_MODEL)
  expect(result.session_id).toBe("session-1")
})

test("completion obtains readback from a sanitized session export", async () => {
  const calls: string[][] = []
  const base = readbackRunner()
  const result = await complete(workerBody(), {
    readbackRunner: { async run(argv, input, signal) { calls.push(argv); return base.run(argv, input, signal) } },
    evidenceRunner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("ok")
  expect(calls.map((argv) => argv.slice(0, 2))).toEqual([["opencode", "export"]])
  expect(calls[0]).toEqual(["opencode", "export", "session-1", "--sanitize"])
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
  const result = await complete(workerBody(), {
    readbackRunner: readbackRunner(READBACK_MODEL, "adv"),
    evidenceRunner: { async run(argv) { evidenceCalls.push(argv); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("agent_identity_mismatch")
  expect(result.error?.recovery_action).toBe("contact_operator")
  expect(result.error?.message).toBe('executed agent "adv" does not match the dispatched lane agent "concord-research"')
  expect(evidenceCalls).toEqual([])
})

test("a successful run records dispatch evidence before completion evidence", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const result = await complete(workerBody(), {
    concordBinary: "concord-test",
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
  expect(dispatched.readback_model).toBe(READBACK_MODEL)
  expect(dispatched.packet_schema_version).toBe("1.0")
  expect(dispatched.report_schema_version).toBe("1.0")
  expect(typeof dispatched.event_id).toBe("string")
  // CD-0067 D6: the CLI has required packet_digest as a top-level field of
  // worker-dispatch since #470. The adapter signed it inside the assertion
  // and omitted it from the request, and no live completion ran this path
  // until the after-hook landed, so the first real completion refused with
  // "missing required field packet_digest". The value is the digest the
  // core recorded at dispatch, never re-derived here.
  expect(dispatched.packet_digest).toBe(PACKET_DIGEST)

  const completed = JSON.parse(calls[1].input)
  expect(completed.work_id).toBe("work-1")
  expect(completed.attempt_id).toBe("attempt-1")
  expect(completed.readback_model).toBe(READBACK_MODEL)
  expect(completed.report_schema_version).toBe("1.0")
  expect(completed.event_id).not.toBe(dispatched.event_id)
})

test("a run whose evidence cannot be recorded is not reported as a success", async () => {
  const refuse = async (argv: string[]) => argv[1] === "worker-dispatch" ? { exitCode: 1, stdout: "", stderr: "evidence write refused" } : { exitCode: 0, stdout: "", stderr: "" }
  const result = await complete(workerBody(), {
    evidenceRunner: { async run(argv) { return refuse(argv) } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toBe("evidence write refused")
})

test("a completion that cannot be recorded is not reported as a success", async () => {
  let recorded = 0
  const result = await complete(workerBody(), {
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
      authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("invalid_input")
    expect(spawned).toBe(0)
    expect(recorded).toBe(0)
  }
})

test("the registered lane set is closed and every agent name is Concord-owned", () => {
  expect(agentLanes.map((entry) => entry.id).sort()).toEqual(["design", "implement", "research", "review", "verify"])
  for (const entry of agentLanes) expect(`concord-${entry.id}`).toMatch(/^concord-[a-z]+$/)
})

test("the adapter does not declare a model — argv carries no --model", async () => {
  let argv: string[] = []
  await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run(args) { argv = args; return { exitCode: 0, stdout: runOutput(), stderr: "" } } }, evidenceRunner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } }, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
  expect(argv).not.toContain("--model")
})

test("the readback shape is recorded verbatim regardless of host configuration", async () => {
  const hostExecuted = "zai-coding-plan/glm-5.3"
  const result = await complete(workerBody(report({}, hostExecuted)), { readbackRunner: readbackRunner(hostExecuted) })
  expect(result.outcome).toBe("ok")
  expect(result.readback_model).toBe(hostExecuted)
})

test("an unknown readback is recorded as-is and not refused", async () => {
  const hostExecuted = "openai/not-declared"
  const result = await complete(workerBody(report({}, hostExecuted)), { readbackRunner: readbackRunner(hostExecuted) })
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

async function terminalEvidence(carried: unknown = report()) {
  const calls: { argv: string[]; input: string }[] = []
  const result = await complete(workerBody(carried), {
    concordBinary: "concord-test",
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  return { result, verbs: calls.map((call) => call.argv[1]), payloads: calls.map((call) => JSON.parse(call.input)) }
}

test("a valid completed report carries its reported evidence into worker-complete", async () => {
  const { result, verbs, payloads } = await terminalEvidence()
  expect(result.outcome).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence_origin).toBe("reported")
  expect(payloads[1].evidence).toEqual(reportEvidence())
  expect(payloads[1].report_schema_version).toBe("1.0")
})

test("a missing report is worker-fail with invalid_report, not a completion", async () => {
  const { result, verbs, payloads } = await terminalEvidence(null)
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toBe("worker output carried no agent-lane-report.v1 report")
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_report")
})

test("an unparseable report is worker-fail with invalid_report", async () => {
  const { result, verbs, payloads } = await terminalEvidence('{"schema_version": "1.0", "lane_id":')
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("malformed JSON document")
  expect(result.error?.kind).toBe("invalid_report")
})

test("a report naming an obligation outside the enum is worker-fail with invalid_report", async () => {
  const evidence = [...reportEvidence(), { obligation: "vibes", detail: "felt right" }]
  const { verbs, payloads } = await terminalEvidence(report({ evidence }))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("evidence[3].obligation")
  expect(payloads[1].detail).toContain("outside the closed enum")
})

test("a report that does not echo the dispatched attempt is worker-fail with invalid_report", async () => {
  const { verbs, payloads } = await terminalEvidence(report({ attempt_id: "attempt-other" }))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toBe('worker report attempt_id "attempt-other" does not match dispatched packet "attempt-1"')
})

test("a reported failure is recorded as the worker's own failure, not an invalid report", async () => {
  const evidence = [{ obligation: "uncertainties", detail: "the cited source was unreachable" }]
  const { result, verbs, payloads } = await terminalEvidence(report({ status: "failed", evidence }))
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

test("a real opencode run --format json capture parses to session metadata and report", async () => {
  const stdout = realRunCaptureEnvelope(JSON.stringify(report()))
  expect(readRunSessionMetadata(stdout)).toEqual({ ok: true, metadata: { session_id: "session-1" } })
  expect(readWorkerReport(stdout).report).toEqual(report())
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
})

test("a fenced report is admitted and a fence around prose is not salvaged", async () => {
  const fenced = "```json\n" + JSON.stringify(report()) + "\n```"
  expect(readWorkerReport(runOutput("", fenced)).report).toEqual(report())
  expect(readWorkerReport(runOutput("", "```\n" + JSON.stringify(report()) + "\n```")).report).toEqual(report())
  const { verbs } = await terminalEvidence(fenced)
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  // Prose around the JSON is not mined for a report; it fails closed.
  expect(readWorkerReport(runOutput("", `Here is the report: ${JSON.stringify(report())} — done.`))).toEqual({ report: null, malformed: false })
})

test("a run with no text part at all carries no report", async () => {
  const stdout = [
    JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
    JSON.stringify({ type: "step_finish", timestamp: 2, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
  ].join("\n")
  expect(readWorkerReport(stdout)).toEqual({ report: null, malformed: false })
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
// The authorizer returns the core's typed refusal; the adapter aborts before
// touching the spawn runner.
test("dispatchWorker aborts when dispatch_worker authorization is refused", async () => {
  let spawned = 0
  let authorizeCalls = 0
  const result = await dispatchWorker(packet(), {
    credentials: testCredentials,
    runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
    evidenceRunner: { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } },
    async authorize(request) {
      authorizeCalls++
      expect(request.work_id).toBe(packet().work_id)
      expect(request.attempt_id).toBe(packet().attempt_id)
      return { schema_version: "1.0", request_id: "auth-1", origin: "core", tool: "concord_work_transition", operation: "workflow_action", outcome: "error", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, error: { kind: "unauthorized_dispatch", retry_safe: false, recovery_action: { kind: "reconcile_operation" }, effect_state: "none", message: "no authorized dispatch window exists for this work item at the current step" } }
    },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("unauthorized_dispatch")
  expect(result.error?.message).toBe("no authorized dispatch window exists for this work item at the current step")
  expect(spawned).toBe(0)
  expect(authorizeCalls).toBe(1)
})

// Issue #436: a refusal and a broken authorizer are different outcomes. The
// adapter previously probed an optional ToolContext method that no host
// declares, so an absent transport was indistinguishable from the core saying
// no. Each transport fault now carries `transport_failure`, leaving
// `unauthorized_dispatch` to mean only that the core refused.
test("a transport fault is not reported as an authorization refusal", async () => {
  const cases: { name: string; options: Record<string, unknown> }[] = [
    { name: "authorizer absent", options: {} },
    { name: "authorizer throws", options: { async authorize() { throw new Error("socket closed") } } },
    { name: "authorizer returns no envelope", options: { async authorize() { return undefined } } },
  ]
  for (const item of cases) {
    let spawned = 0
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
      evidenceRunner: { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } },
      ...item.options,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("transport_failure")
    expect(spawned).toBe(0)
  }
})

