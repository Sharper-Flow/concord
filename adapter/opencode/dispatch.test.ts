import { test, expect } from "bun:test"
import { createHash, randomUUID } from "node:crypto"
import { mkdtemp } from "node:fs/promises"
import fs from "node:fs"
import * as os from "node:os"
import path from "node:path"
import { agentLanes } from "./generated-agent-lanes"
import { completeWorkerAttempt, computeHostPromptProvenance, concordBinaryPath, configureCoreBinary, defaultExportRunner, dispatchWorker, MAX_EXPORT_BYTES, readExportOpeningPacket, readExportSession, readExportSessionMetadata, readRunSessionMetadata, resolveCoreBinary, validateAgentLanePacket, type AgentLanePacket, type CanonicalLaneReport, type DispatchAuthorizer, type DispatchRunner } from "./dispatch"

// Fake-runner suite: bind worker-evidence CLI calls to a nominal core path
// instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord-test")
// This suite runs with no host. The completion path prefers a bound control
// plane for its session observation, and other test files bind fake clients
// into the module-shared instance; a runner that shares one process would
// otherwise hand this file a host it never asked for. State the precondition.
import { hostControlPlane } from "./move-session"
hostControlPlane().bind(undefined)
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
  readback_model: model,
  status: "completed",
  evidence: reportEvidence(),
  ...overrides,
})

// The admitted canonical report composes the transport-owned identity fields
// from the authorized dispatch packet with the worker-authored content.
const canonicalReport = (overrides: Record<string, unknown> = {}, p: AgentLanePacket = packet()): CanonicalLaneReport =>
  ({
    ...report(overrides),
    attempt_id: p.attempt_id,
    lane_id: p.lane_id,
    lane_version: p.lane_version,
    lane_digest: p.lane_digest,
  }) as CanonicalLaneReport

const reportEvent = (document: unknown) => JSON.stringify({ type: "text", timestamp: 3, sessionID: "session-1", part: { type: "text", text: typeof document === "string" ? document : JSON.stringify(document) } })

const runOutput = (extra = "", carried: unknown = report()) => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  carried === null ? "" : reportEvent(carried),
  JSON.stringify({ type: "step_finish", timestamp: 2, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
  extra,
].filter(Boolean).join("\n")

// CD-0102: an authorized worker session opens with the dispatch packet as its
// first user message, so every fixture that reaches completion opens that way.
// `opener` substitutes a foreign opening message to drive the packet-identity
// refusal; it stays a string or a packet-shaped object for byte comparison.
const exportedSession = (model = READBACK_MODEL, agent = "concord-research", opener: unknown = packet()) => JSON.stringify({
  info: { id: "session-1" },
  messages: [
    { info: { id: "message-0", sessionID: "session-1", role: "user", agent, time: { created: 0 } }, parts: [{ type: "text", text: typeof opener === "string" ? opener : JSON.stringify(opener) }] },
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
// Completion reads two host surfaces: the sanitized export for the executing
// model, and the session index for live-session evidence. The dispatch window
// supplies the worker directory.
const sessionIndex = (directory = "/claimed/worktree") => JSON.stringify([{ id: "session-1", directory }])

const readbackRunner = (model = READBACK_MODEL, agent = "concord-research", opener: unknown = packet()): DispatchRunner => ({
  async run(argv) {
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(model, agent, opener), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

const SIGNAL = new AbortController().signal
const SESSION = "session-parent"
const WORKER_DIRECTORY = process.cwd()

type CompleteOptions = Parameters<typeof completeWorkerAttempt>[3]
const acceptingEvidence = (): DispatchRunner => ({ async run() { return { exitCode: 0, stdout: "", stderr: "" } } })
// The fixture session opens with the packet under test, so the completion
// path verifies packet identity against the same dispatch it admits.
const complete = (body: string, options: Partial<CompleteOptions> = {}, dispatched: AgentLanePacket = packet()) =>
  completeWorkerAttempt(lane, dispatched, body, { credentials: testCredentials, readbackRunner: readbackRunner(READBACK_MODEL, "concord-research", dispatched), evidenceRunner: acceptingEvidence(), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY, ...options }, SIGNAL)

test("packet validation is closed before any runner call", async () => {
  let calls = 0
  const invalid = { ...packet(), inputs: { task: "" } }
  expect(validateAgentLanePacket(invalid)).toBe(false)
  const result = await dispatchWorker(invalid, { credentials: testCredentials, runner: { async run() { calls++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } }, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows: new DispatchWindows() })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(calls).toBe(0)
})

test("an omitted or nonexistent worker directory refuses before authorization", async () => {
  for (const workerDirectory of [undefined, `${process.cwd()}/concord-dispatch-nonexistent-${randomUUID()}`]) {
    let authorizeCalls = 0
    const windows = new DispatchWindows()
    const result = await dispatchWorker(packet(), {
      authorize: async () => { authorizeCalls++; return coreOk() },
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      ...(workerDirectory === undefined ? {} : { workerDirectory }),
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("invalid_input")
    expect(result.error?.message).toMatch(/resolvable worker directory/)
    expect(authorizeCalls).toBe(0)
    expect(windows.has(SESSION)).toBe(false)
  }
})

test("a worker directory retargeted during authorization is refused", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  const claimed = path.join(root, "claimed")
  const other = path.join(root, "other")
  const alias = path.join(root, "alias")
  for (const directory of [claimed, other]) fs.mkdirSync(directory)
  fs.symlinkSync(claimed, alias)
  try {
    const windows = new DispatchWindows()
    const result = await dispatchWorker(packet(), {
      authorize: async () => {
        fs.unlinkSync(alias)
        fs.symlinkSync(other, alias)
        return coreOk()
      },
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: alias,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.message).toMatch(/does not match the active claimed worktree/i)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
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
  const result = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows, workerDirectory: WORKER_DIRECTORY })
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(result.agent).toBe("concord-research")
  expect(result.readback_model).toBe(null)
  expect(windows.has(SESSION)).toBe(true)
})

test("a dispatch that cannot name its calling session opens no window", async () => {
  const windows = new DispatchWindows()
  const result = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, windows, workerDirectory: WORKER_DIRECTORY })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(windows.has(SESSION)).toBe(false)
})

// Two open windows would let one authorized dispatch start whichever worker the
// next Task call happened to name.
test("a second dispatch on one session is refused while a window is open", async () => {
  const windows = new DispatchWindows()
  const first = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows, workerDirectory: WORKER_DIRECTORY })
  expect(first.outcome).toBe("ok")
  const second = await dispatchWorker(packet(), { credentials: testCredentials, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, sessionID: SESSION, windows, workerDirectory: WORKER_DIRECTORY })
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
  expect(calls.map((argv) => argv.slice(0, 2))).toEqual([["opencode", "export"], ["opencode", "export"], ["opencode", "session"]])
  // The opening packet read runs first and unsanitized — the sanitized body
  // redacts every text part — then the sanitized export for model readback.
  expect(calls[0]).toEqual(["opencode", "export", "session-1"])
  expect(calls[1]).toEqual(["opencode", "export", "session-1", "--sanitize"])
  expect(calls[2]).toEqual(["opencode", "session", "list", "--format", "json"])
})

test("readback accepts a large sanitized session export", () => {
  const largeExport = JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: "x".repeat(70_000) }] },
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] },
    ],
  })
  expect(Buffer.byteLength(largeExport)).toBeGreaterThan(65_536)
  expect(readExportSessionMetadata(largeExport, "session-1")).toEqual({ readback_model: "openai/gpt-5.6-luna", readback_agent: "concord-research", session_id: "session-1" })
})

test("readback refuses an export above its own ceiling", () => {
  const runaway = JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: "x".repeat(MAX_EXPORT_BYTES) }] },
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] },
    ],
  })
  expect(readExportSessionMetadata(runaway, "session-1")).toBe(null)
})

test("readback refusal names its predicate and preserves the export digest", () => {
  const malformed = "not-json"
  const expectedDigest = `sha256:${createHash("sha256").update(malformed, "utf8").digest("hex")}`
  const result = readExportSession(malformed, "session-1")
  expect(result.ok).toBe(false)
  if (result.ok) return
  expect(result.predicate).toBe("export_json")
  expect(result.export_digest).toBe(expectedDigest)
  expect(result.export_bytes).toBe(Buffer.byteLength(malformed))
  expect(result.message).toContain("valid JSON")
})

test("readback refusal is typed and does not change valid completion", async () => {
  const result = await complete(workerBody(), {
    readbackRunner: { async run(argv) {
      if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
      return { exitCode: 0, stdout: "not-json", stderr: "" }
    } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("readback_refusal")
  expect(result.error?.predicate).toBe("export_json")
  expect(result.error?.export_digest).toBe(`sha256:${createHash("sha256").update("not-json", "utf8").digest("hex")}`)
  expect(result.error?.export_bytes).toBe(Buffer.byteLength("not-json"))
  expect(result.error?.message).toContain("readback predicate export_json refused")
  expect(result.error?.message).not.toContain("not-json")
  expect(result.error?.retry_safe).toBe(false)

  const accepted = await complete(workerBody())
  expect(accepted.outcome).toBe("ok")
  expect(accepted.readback_model).toBe(READBACK_MODEL)
})

// CD-0102 completion identity: a worker session that did not open with the
// authorized packet is refused through the typed readback predicate, records
// one durable failed attempt, and signs no completion evidence.
const packetRefusalRunner = (opener: string): DispatchRunner => ({ async run(argv) {
  if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(READBACK_MODEL, "concord-research", opener), stderr: "" }
  if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
  return { exitCode: 0, stdout: "", stderr: "" }
} })

test("caller-composed prose as the first user message refuses the attempt", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const result = await complete(workerBody(), {
    readbackRunner: packetRefusalRunner("Fix the bug in the adapter, then summarize what you changed."),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("readback_refusal")
  expect(result.error?.predicate).toBe("dispatched_packet_identity")
  expect(result.error?.retry_safe).toBe(false)
  expect(result.error?.message).toContain("readback predicate dispatched_packet_identity refused")
  expect(result.error?.message).not.toContain("Fix the bug")
  expect(result.session_id).toBe("session-1")
  // One durable failed dispatch event; no model evidence, no completion.
  expect(calls.map((call) => call.argv[1])).toEqual(["worker-dispatch"])
  expect(JSON.parse(calls[0].input).terminal).toBe("failed")
  expect(JSON.parse(calls[0].input).terminal_failure_kind).toBe("model_readback_missing")
  expect(JSON.parse(calls[0].input).readback_model).toBe("")
  expect(JSON.parse(calls[0].input).terminal_detail).toContain("readback predicate dispatched_packet_identity refused")
})

test("a packet for another attempt refuses the attempt the same way", async () => {
  const foreign = { ...packet(), attempt_id: "attempt-other" }
  const calls: string[] = []
  const result = await complete(workerBody(), {
    readbackRunner: packetRefusalRunner(JSON.stringify(foreign)),
    evidenceRunner: { async run(argv) { calls.push(argv[1]); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.predicate).toBe("dispatched_packet_identity")
  expect(calls).toEqual(["worker-dispatch"])
})

test("readExportOpeningPacket admits the exact packet and refuses every substitute", () => {
  expect(readExportOpeningPacket(exportedSession(), "session-1", packet())).toEqual({ ok: true })
  const prose = readExportOpeningPacket(exportedSession(READBACK_MODEL, "concord-research", "do the work described in this prose"), "session-1", packet())
  expect(prose).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with a message that is not the authorized dispatch packet" })
  const otherAttempt = readExportOpeningPacket(exportedSession(READBACK_MODEL, "concord-research", JSON.stringify({ ...packet(), work_id: "work-2" })), "session-1", packet())
  expect(otherAttempt).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with a message that is not the authorized dispatch packet" })
  const assistantFirst = readExportOpeningPacket(JSON.stringify({
    info: { id: "session-1" },
    messages: [
      { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
    ],
  }), "session-1", packet())
  expect(assistantFirst).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with a non-user message instead of the authorized dispatch packet" })
  expect(readExportOpeningPacket("not-json", "session-1", packet())).toEqual({ ok: false, predicate: "export_json", message: "export body was not valid JSON" })
  expect(readExportOpeningPacket(JSON.stringify({ info: { id: "session-1" }, messages: [] }), "session-1", packet())).toEqual({ ok: false, predicate: "export_shape", message: "export body did not match the session shape" })
})

// The authorized dispatch writes the packet as one text part. Content beside
// it reached the worker and was never authorized, so the packet text alone
// cannot establish identity.
test("readExportOpeningPacket refuses content beside the exact packet", () => {
  const openingParts = (parts: unknown[]) => JSON.stringify({
    info: { id: "session-1" },
    messages: [{ info: { id: "message-1", sessionID: "session-1", role: "user", time: { created: 1 } }, parts }],
  })
  const withFile = readExportOpeningPacket(openingParts([
    { type: "text", text: JSON.stringify(packet()) },
    { type: "file", filename: "notes.md", url: "file:///notes.md" },
  ]), "session-1", packet())
  expect(withFile).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with 2 message parts instead of the single authorized dispatch packet" })
  const splitText = readExportOpeningPacket(openingParts([
    { type: "text", text: JSON.stringify(packet()).slice(0, 10) },
    { type: "text", text: JSON.stringify(packet()).slice(10) },
  ]), "session-1", packet())
  expect(splitText).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with 2 message parts instead of the single authorized dispatch packet" })
  const fileOnly = readExportOpeningPacket(openingParts([{ type: "file", filename: "notes.md", url: "file:///notes.md" }]), "session-1", packet())
  expect(fileOnly).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with a file part instead of the authorized dispatch packet" })
  expect(readExportOpeningPacket(openingParts([]), "session-1", packet())).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with 0 message parts instead of the single authorized dispatch packet" })
})

// The sanitized readback bounds its own export. This predicate reads a second,
// unsanitized export of the same session, so it carries the same bound.
test("readExportOpeningPacket refuses an export above the size bound", () => {
  const oversize = JSON.stringify({
    info: { id: "session-1", padding: "p".repeat(MAX_EXPORT_BYTES) },
    messages: [{ info: { id: "message-1", sessionID: "session-1", role: "user", time: { created: 1 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] }],
  })
  expect(Buffer.byteLength(oversize)).toBeGreaterThan(MAX_EXPORT_BYTES)
  expect(readExportOpeningPacket(oversize, "session-1", packet())).toEqual({ ok: false, predicate: "export_size_bound", message: `export body exceeded ${MAX_EXPORT_BYTES} bytes` })
})

test("ambiguous model readback records one durable failed attempt", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const exportRunner: DispatchRunner = { async run(argv) {
    if (argv[1] === "export") return { exitCode: 0, stdout: JSON.stringify({
      info: { id: "session-1" },
      messages: [
        { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
        { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "first", time: { created: 1 } }, parts: [] },
        { info: { id: "message-2", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "second", time: { created: 2 } }, parts: [] },
      ],
    }), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  } }
  const result = await complete(workerBody(), {
    readbackRunner: exportRunner,
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(calls.map((call) => call.argv[1])).toEqual(["worker-dispatch"])
  expect(JSON.parse(calls[0].input).terminal).toBe("failed")
  expect(JSON.parse(calls[0].input).terminal_failure_kind).toBe("model_readback_ambiguous")
  expect(JSON.parse(calls[0].input).readback_model).toBe("")
  expect(JSON.parse(calls[0].input).terminal_detail).toContain("readback predicate export_model_ambiguous refused")
  expect(JSON.parse(calls[0].input).terminal_detail).toContain("export_digest sha256:")
  expect(result.error?.kind).toBe("readback_refusal")
  expect(result.error?.predicate).toBe("export_model_ambiguous")
  expect(result.session_id).toBe("session-1")
})

test("missing model readback records one durable failed attempt", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const exportRunner: DispatchRunner = { async run(argv) {
    if (argv[1] === "export") return { exitCode: 0, stdout: JSON.stringify({ info: { id: "session-1" }, messages: [
      { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
    ] }), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  } }
  const result = await complete(workerBody(), {
    readbackRunner: exportRunner,
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(calls.map((call) => call.argv[1])).toEqual(["worker-dispatch"])
  expect(JSON.parse(calls[0].input).terminal).toBe("failed")
  expect(JSON.parse(calls[0].input).terminal_failure_kind).toBe("model_readback_missing")
  expect(JSON.parse(calls[0].input).readback_model).toBe("")
  expect(JSON.parse(calls[0].input).terminal_detail).toContain("readback predicate export_assistant_message refused")
  expect(result.error?.kind).toBe("readback_refusal")
  expect(result.error?.predicate).toBe("export_assistant_message")
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

// A refused completion is reported as an error and closed with worker-fail, so
// the attempt never stays dispatched. An open attempt blocks every later
// dispatch on that work item and pins the host session to its worktree.
test("a completion that cannot be recorded is closed and not reported as a success", async () => {
  const verbs: string[] = []
  const result = await complete(workerBody(), {
    evidenceRunner: { async run(argv) { verbs.push(argv[1]); return argv[1] === "worker-complete" ? { exitCode: 1, stdout: "", stderr: "worker attempt belongs to a different work item" } : { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(verbs).toEqual(["worker-dispatch", "worker-complete", "worker-fail"])
  expect(result.outcome).toBe("error")
  expect(result.error?.message).toBe("worker-complete refused: worker attempt belongs to a different work item")
})

test("generic host agents are not dispatchable and never spawn or record", async () => {
  for (const generic of ["general", "explore", "build", "plan"]) {
    let spawned = 0
    let recorded = 0
    const result = await dispatchWorker({ ...packet(), lane_id: generic }, { credentials: testCredentials,
      runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
      evidenceRunner: { async run() { recorded++; return { exitCode: 0, stdout: "", stderr: "" } } },
      authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY,
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
  await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run(args) { argv = args; return { exitCode: 0, stdout: runOutput(), stderr: "" } } }, evidenceRunner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } }, authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY })
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

test("worker evidence uses the supplied worker directory for provenance", async () => {
  const parent = await mkdtemp(path.join(os.tmpdir(), "worker-provenance-"))
  const worker = `${parent}/worktree`
  const configDir = await mkdtemp(path.join(os.tmpdir(), "worker-provenance-config-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await fs.promises.mkdir(`${worker}/.git`, { recursive: true })
    await Bun.write(`${worker}/opencode.json`, JSON.stringify({ instructions: ["worker-rules.md"] }))
    await Bun.write(`${worker}/worker-rules.md`, "# worker rules\n")
    let dispatchPayload: Record<string, unknown> | undefined
    const result = await complete(workerBody(), {
      workerDirectory: worker,
      readbackRunner: {
        async run(argv) {
          if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
          if (argv[1] === "session") return { exitCode: 0, stdout: sessionIndex(worker), stderr: "" }
          return { exitCode: 0, stdout: "", stderr: "" }
        },
      },
      evidenceRunner: {
        async run(argv, input) {
          if (argv[1] === "worker-dispatch") dispatchPayload = JSON.parse(input) as Record<string, unknown>
          return { exitCode: 0, stdout: "", stderr: "" }
        },
      },
    })
    expect(result.outcome).toBe("ok")
    const sources = (dispatchPayload?.host_provenance as { sources?: { path?: string }[] }).sources ?? []
    expect(sources.map(source => source.path)).toContain(`${worker}/worker-rules.md`)
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
    await fs.promises.rm(parent, { recursive: true, force: true })
  }
})

test("worker evidence remains successful when the session index is unreadable", async () => {
  let evidenceCalls = 0
  const result = await complete(workerBody(), {
    readbackRunner: {
      async run(argv) {
        if (argv[1] === "session") return { exitCode: 1, stdout: "", stderr: "session list failed" }
        return { exitCode: 0, stdout: exportedSession(), stderr: "" }
      },
    },
    evidenceRunner: {
      async run() {
        evidenceCalls++
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    },
  })
  expect(result.outcome).toBe("ok")
  expect(evidenceCalls).toBe(2)
})

// CD-0032 / issue #103: provenance is deterministic for the same inputs and
// changes when an enumerated source changes.

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

test("host prompt provenance prefers the project agent definition", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-cwd-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await fs.promises.mkdir(`${configDir}/agents`, { recursive: true })
    await fs.promises.mkdir(`${dir}/.opencode/agents`, { recursive: true })
    await Bun.write(`${configDir}/agents/concord-research.md`, "# global agent\n")
    await Bun.write(`${dir}/.opencode/agents/concord-research.md`, "# project agent\n")

    const first = await computeHostPromptProvenance("research", dir)
    expect(first.sources.filter(source => source.kind === "agent_definition")).toEqual([
      expect.objectContaining({ kind: "agent_definition", path: `${dir}/.opencode/agents/concord-research.md` }),
    ])

    await Bun.write(`${configDir}/agents/concord-research.md`, "# global agent changed\n")
    expect((await computeHostPromptProvenance("research", dir)).digest).toBe(first.digest)
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

test("host prompt provenance falls back to the global agent definition", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-cwd-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await fs.promises.mkdir(`${configDir}/agents`, { recursive: true })
    await Bun.write(`${configDir}/agents/concord-research.md`, "# global agent\n")

    const first = await computeHostPromptProvenance("research", dir)
    expect(first.sources.filter(source => source.kind === "agent_definition")).toEqual([
      expect.objectContaining({ kind: "agent_definition", path: `${configDir}/agents/concord-research.md` }),
    ])

    await Bun.write(`${configDir}/agents/concord-research.md`, "# global agent changed\n")
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

test("host prompt provenance stops project discovery at the Git root", async () => {
  const parent = await mkdtemp(path.join(os.tmpdir(), "provenance-parent-"))
  const root = `${parent}/project`
  const nested = `${root}/packages/child`
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await fs.promises.mkdir(`${root}/.git`, { recursive: true })
    await fs.promises.mkdir(nested, { recursive: true })
    await Bun.write(`${parent}/opencode.json`, JSON.stringify({ instructions: [`${parent}/outside.md`] }))
    await Bun.write(`${parent}/outside.md`, "# outside\n")
    await Bun.write(`${root}/opencode.json`, JSON.stringify({ instructions: ["rules.md"] }))
    await Bun.write(`${root}/rules.md`, "# project\n")
    await Bun.write(`${root}/AGENTS.md`, "# project agents\n")
    await Bun.write(`${parent}/AGENTS.md`, "# parent agents\n")

    const result = await computeHostPromptProvenance("research", nested)

    expect(result.sources.map(source => source.path)).toContain(`${root}/rules.md`)
    expect(result.sources.map(source => source.path)).not.toContain(`${parent}/outside.md`)
    expect(result.sources.map(source => source.path)).toContain(`${root}/AGENTS.md`)
    expect(result.sources.map(source => source.path)).not.toContain(`${parent}/AGENTS.md`)
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

test("host prompt provenance emits unique source identities", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-cwd-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    const result = await computeHostPromptProvenance("research", dir)
    const identities = result.sources.map(source => `${source.kind}:${source.path ?? ""}`)
    expect(new Set(identities).size).toBe(identities.length)
    expect(result.sources.filter(source => source.kind === "unenumerated").map(source => source.path)).toEqual(expect.arrayContaining([
      "provider_behavioral_hints",
      "output_voice_overlays",
      "mcp_tool_definition_prompt",
    ]))
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

test("the AGENTS.md walk names the global file once when the spawn directory is the config directory", async () => {
  const configDir = await mkdtemp(path.join(os.tmpdir(), "provenance-config-"))
  const previous = process.env.OPENCODE_CONFIG_DIR
  process.env.OPENCODE_CONFIG_DIR = configDir
  try {
    await Bun.write(`${configDir}/AGENTS.md`, "# shared instructions\n")
    const result = await computeHostPromptProvenance("research", configDir)
    const identities = result.sources.map(source => `${source.kind}:${source.path ?? ""}`)
    expect(new Set(identities).size).toBe(identities.length)
    expect(identities.filter(identity => identity === `agents_md:${configDir}/AGENTS.md`)).toHaveLength(1)
  } finally {
    if (previous === undefined) delete process.env.OPENCODE_CONFIG_DIR
    else process.env.OPENCODE_CONFIG_DIR = previous
  }
})

// CD-0056 D7 / issue #333: the adapter parses the report it already receives,
// carries its evidence into worker-complete, and turns anything it cannot admit
// into a typed worker-fail rather than a completion.
import { readWorkerReport, resolveWorkerReport, resolveWorkerReportFromText, validateAgentLaneReport, validateAgainstSchema } from "./dispatch"

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

// CD-0056 D4 binds each lane to its own evidence obligations, while
// agent-lane-report.v1 types `obligation` as one flat union across every lane
// and the report carries no lane field. A report naming another lane's
// obligation is therefore schema-valid and still inadmissible, so the check is
// per lane and every registered lane carries its own proof.
type LaneEvidence = { obligation: string; detail: string }

const laneOf = (id: string) => agentLanes.find((entry) => entry.id === id)!

const lanePacketFor = (laneID: string): AgentLanePacket => {
  const target = laneOf(laneID)
  return { ...packet(), lane_id: target.id, lane_version: target.version, lane_digest: target.digest }
}

const dischargingEvidence = (laneID: string): LaneEvidence[] =>
  laneOf(laneID).evidence_obligations.map((obligation) => ({ obligation, detail: `the ${laneID} lane discharges ${obligation}` }))

// The intruding obligation is the first one, in registry order, that another
// lane declares and this lane does not. Registry order makes the choice
// deterministic without pinning a literal the lane manifest owns.
const foreignObligation = (laneID: string): { lane: string; obligation: string } => {
  const declared = new Set<string>(laneOf(laneID).evidence_obligations)
  for (const other of agentLanes) {
    if (other.id === laneID) continue
    const obligation = other.evidence_obligations.find((candidate) => !declared.has(candidate))
    if (obligation) return { lane: other.id, obligation }
  }
  throw new Error(`no lane declares an obligation the ${laneID} lane omits`)
}

async function laneTerminalEvidence(laneID: string, evidence: LaneEvidence[]) {
  const target = laneOf(laneID)
  const calls: { argv: string[]; input: string }[] = []
  const body = workerBody({ schema_version: "1.0", readback_model: READBACK_MODEL, status: "completed", evidence })
  const result = await completeWorkerAttempt(target, lanePacketFor(laneID), body, {
    credentials: testCredentials,
    readbackRunner: readbackRunner(READBACK_MODEL, `concord-${target.id}`, lanePacketFor(laneID)),
    packetDigest: PACKET_DIGEST,
    workerDirectory: process.cwd(),
    concordBinary: "concord-test",
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, SIGNAL)
  return { result, verbs: calls.map((call) => call.argv[1]), payloads: calls.map((call) => JSON.parse(call.input)) }
}

for (const registered of agentLanes) {
  const laneID = registered.id
  const intruder = foreignObligation(laneID)

  test(`the ${laneID} lane refuses a report naming the ${intruder.lane} lane's ${intruder.obligation} obligation`, async () => {
    const detail = `an obligation the ${laneID} lane does not declare`
    const evidence = [...dischargingEvidence(laneID), { obligation: intruder.obligation, detail }]
    const { result, verbs, payloads } = await laneTerminalEvidence(laneID, evidence)
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(payloads[1].failure_kind).toBe("invalid_report")
    expect(payloads[1].detail).toContain(intruder.obligation)
    expect(payloads[1].detail).toContain(laneID)
    expect(result.error?.kind).toBe("invalid_report")
    expect(result.error?.retry_safe).toBe(false)
    expect(result.output).toContain(detail)
  })

  test(`the ${laneID} lane refuses a report that leaves one of its obligations undischarged`, async () => {
    const dropped = registered.evidence_obligations[registered.evidence_obligations.length - 1]
    const evidence = dischargingEvidence(laneID).filter((entry) => entry.obligation !== dropped)
    const { result, verbs, payloads } = await laneTerminalEvidence(laneID, evidence)
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(payloads[1].failure_kind).toBe("invalid_report")
    expect(payloads[1].detail).toContain(dropped)
    expect(payloads[1].detail).toContain(laneID)
    expect(result.error?.kind).toBe("invalid_report")
  })

  test(`the ${laneID} lane admits a report that discharges exactly its declared obligations`, async () => {
    const evidence = dischargingEvidence(laneID)
    const { result, verbs, payloads } = await laneTerminalEvidence(laneID, evidence)
    expect(result.outcome).toBe("ok")
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
    expect(payloads[1].evidence).toEqual(evidence)
  })
}

test("multiple distinct findings may discharge one declared obligation", async () => {
  const evidence = [...reportEvidence(), { obligation: "bounded_findings", detail: "A second bounded finding." }]
  const { result, verbs, payloads } = await terminalEvidence(report({ evidence }))
  expect(result.outcome).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence).toEqual(evidence)
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

test("an oversized evidence array is worker-fail with invalid_report", async () => {
  const evidence = Array.from({ length: 65 }, (_, index) => ({ obligation: "uncertainties", detail: `item ${index}` }))
  const { verbs, payloads } = await terminalEvidence(report({ evidence }))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("evidence: carries more than 64 item(s)")
})

test("an oversized evidence detail is worker-fail with invalid_report", async () => {
  const evidence = [{ obligation: "uncertainties", detail: "x".repeat(513) }]
  const { verbs, payloads } = await terminalEvidence(report({ evidence }))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("evidence[0].detail: is longer than 512 characters")
})

test("an unknown report top-level field is worker-fail with invalid_report", async () => {
  const { verbs, payloads } = await terminalEvidence(report({ unexpected: true }))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("carries undeclared property unexpected")
})

test("model-supplied identity is refused, however near-identical", () => {
  const cases = [
    {
      packetAttempt: "attempt-work-4c23eeda61b1bf31fa61e58e-implement-01a07f5d6291",
      reportedAttempt: "attempt-work-4c23eeda61b1bf31fa61e58e-implement",
    },
    {
      packetAttempt: "attempt-work-3669a80ab2828182e6ccc334-verify-01a083c23815",
      reportedAttempt: "attempt-work-3669a80ab2828182e6ccc334-verify-01a83c23815",
    },
  ]
  for (const testCase of cases) {
    const dispatched = { ...packet(), attempt_id: testCase.packetAttempt }
    const resolved = resolveWorkerReport(runOutput("", report({ attempt_id: testCase.reportedAttempt })), dispatched)
    expect(resolved).toEqual({ detail: "worker report supplied dispatch-owned field(s) attempt_id; identity belongs to the authorized dispatch window" })
  }
})

test("each dispatch-owned identity field is refused when the model supplies it", () => {
  const conflicts: Record<string, unknown> = {
    attempt_id: "attempt-work-1-other",
    lane_id: "review",
    lane_version: 99,
    lane_digest: "sha256:" + "0".repeat(64),
  }
  for (const [field, value] of Object.entries(conflicts)) {
    const resolved = resolveWorkerReport(runOutput("", report({ [field]: value })), packet())
    expect("detail" in resolved && resolved.detail).toContain(`supplied dispatch-owned field(s) ${field}`)
  }
})

test("identity-free worker content reaches worker-complete bound to the dispatch window", async () => {
  const dispatched = { ...packet(), attempt_id: "attempt-work-3669a80ab2828182e6ccc334-implement-01a0838fa54c" }
  const carried = report()
  expect(carried).not.toHaveProperty("attempt_id")
  const calls: { argv: string[]; input: string }[] = []
  const result = await complete(workerBody(carried), {
    concordBinary: "concord-test",
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, dispatched)
  const payloads = calls.map((call) => JSON.parse(call.input))
  expect(result.outcome).toBe("ok")
  expect(calls.map((call) => call.argv[1])).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[0].attempt_id).toBe(dispatched.attempt_id)
  expect(payloads[1].attempt_id).toBe(dispatched.attempt_id)
  expect(payloads[1].evidence).toEqual(reportEvidence())
})

test("an incomplete report remains worker-fail with invalid_report", async () => {
  const { verbs, payloads } = await terminalEvidence(report({ status: undefined }))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("missing required property status")
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

test("report resolution composes packet identity over closed worker content", () => {
  expect(resolveWorkerReport(runOutput(), packet())).toEqual({ report: canonicalReport() })
  const conflicting = resolveWorkerReport(runOutput("", report({ lane_digest: "sha256:" + "0".repeat(64) })), packet())
  expect("detail" in conflicting && conflicting.detail).toContain("supplied dispatch-owned field(s) lane_digest")
  const missing = resolveWorkerReport(runOutput("", report({ status: undefined })), packet())
  expect("detail" in missing && missing.detail).toContain("missing required property status")
})

test("the native-text admission path composes packet identity and refuses supplied identity", () => {
  expect(resolveWorkerReportFromText(JSON.stringify(report()), packet())).toEqual({ report: canonicalReport() })
  const refused = resolveWorkerReportFromText(JSON.stringify(report({ attempt_id: "attempt-work-1-other" })), packet())
  expect("detail" in refused && refused.detail).toContain("supplied dispatch-owned field(s) attempt_id")
  const malformed = resolveWorkerReportFromText("the worker returned prose", packet())
  expect("detail" in malformed && malformed.detail).toContain("carried no agent-lane-report.v1 report")
})

test("the report schema validator resolves $defs through $ref", () => {
  expect(validateAgentLaneReport(report())).toBe(true)
  expect(validateAgentLaneReport(report({ evidence: [{ obligation: "source_citations", detail: "x", extra: 1 }] }))).toBe(false)
  expect(validateAgentLaneReport(report({ evidence: [{ obligation: "source_citations" }] }))).toBe(false)
  expect(validateAgentLaneReport(report({ evidence: [] }))).toBe(false)
})

test("the report schema refuses oversized evidence arrays and details", () => {
  const oversizedEvidence = Array.from({ length: 65 }, (_, index) => ({ obligation: "source_citations", detail: String(index + 1) }))
  expect(validateAgentLaneReport(report({ evidence: oversizedEvidence }))).toBe(false)
  expect(validateAgentLaneReport(report({ evidence: [{ obligation: "source_citations", detail: "x".repeat(513) }] }))).toBe(false)
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
  expect(failures[0]).toBe('status: is outside the closed enum; expected one of ["completed","failed"]')
  expect(failures[0]).not.toContain('"done"')
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

test("validateSchema enforces declared UTF-8 byte bounds without echoing values", () => {
  const schema = { type: "object", properties: { text: { type: "string", "x-maxBytes": 4 } } }
  for (const text of ["1234", "éé", "🙂"]) expect(validateAgainstSchema(schema, { text })).toBe(true)
  for (const text of ["12345", "ééa", "🙂a"]) {
    const failures: string[] = []
    expect(validateAgainstSchema(schema, { text }, failures)).toBe(false)
    expect(failures).toEqual(["text: exceeds 4 UTF-8 bytes"])
  }
})

test("validateSchema closed properties require own declarations and own required fields", () => {
  const schema = { type: "object", properties: { id: { type: "string" } }, required: ["id"], additionalProperties: false }
  for (const name of ["constructor", "toString", "__proto__"]) {
    const failures: string[] = []
    expect(validateAgainstSchema(schema, { id: "valid", [name]: "private-value" }, failures)).toBe(false)
    expect(failures).toEqual([`carries undeclared property ${name}`])
  }
  expect(validateAgainstSchema(schema, Object.create({ id: "inherited" }))).toBe(false)
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
    workerDirectory: WORKER_DIRECTORY,
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
      workerDirectory: WORKER_DIRECTORY,
      runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
      evidenceRunner: { async run() { spawned++; return { exitCode: 0, stdout: "", stderr: "" } } },
      ...item.options,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("transport_failure")
    expect(spawned).toBe(0)
  }
})

// The host `opencode export` CLI truncates its stdout to one stdio buffer on
// a pipe, so the export readback runs file-backed: the child writes the
// export to a temporary file and the adapter reads the file whole.
const largeSanitizedExport = () => JSON.stringify({
  info: { id: "session-1" },
  messages: [
    // The opening user message is exactly the dispatch packet, so completion's
    // packet-identity readback admits the fixture; the size lives on the
    // assistant text, which the readback does not parse.
    { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
    { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [{ type: "text", text: "x".repeat(70_000) }] },
  ],
})

const writeExportFixture = (): { binary: string; body: string; cleanup: () => void } => {
  const body = largeSanitizedExport()
  expect(Buffer.byteLength(body)).toBeGreaterThan(16_384)
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "concord-fixture-export-"))
  fs.writeFileSync(path.join(directory, "export.json"), body)
  const script = path.join(directory, "opencode-export")
  fs.writeFileSync(script, `#!/bin/sh\ncase "$1" in\n  export) cat "$(dirname "$0")/export.json" ;;\n  session) printf '[{"id":"session-1","directory":"/claimed/worktree"}]\\n' ;;\nesac\n`, { mode: 0o755 })
  return { binary: script, body, cleanup: () => fs.rmSync(directory, { recursive: true, force: true }) }
}

const exportScratchDirectories = (): string[] =>
  fs.readdirSync(os.tmpdir()).filter((entry) => entry.startsWith("concord-export-"))

test("TestExportReadbackRunnerReadsFullExportThroughFile", async () => {
  // The temporary directory is shared with every other process on the host,
  // so the claim under test is that this run leaves nothing behind, not that
  // the directory is empty. Asserting the latter made one abandoned export
  // from a crashed run fail this test on every later run, which reads as a
  // regression in the runner rather than as unrelated debris.
  const before = new Set(exportScratchDirectories())
  const { binary, body, cleanup } = writeExportFixture()
  try {
    const result = await defaultExportRunner.run([binary, "export", "session-1", "--sanitize"], "", SIGNAL)
    expect(result.exitCode).toBe(0)
    expect(result.stderr).toBe("")
    expect(result.stdout).toBe(body)
  } finally {
    cleanup()
  }
  const leaked = exportScratchDirectories().filter((entry) => !before.has(entry))
  expect(leaked).toEqual([])
})

test("TestDispatchWorkerCompletesWithExportLargerThanPipeBuffer", async () => {
  const { binary, cleanup } = writeExportFixture()
  let result: Awaited<ReturnType<typeof completeWorkerAttempt>>
  try {
    result = await completeWorkerAttempt(lane, packet(), workerBody(), {
      credentials: testCredentials,
      evidenceRunner: acceptingEvidence(),
      packetDigest: PACKET_DIGEST,
      binary,
      workerDirectory: WORKER_DIRECTORY,
    }, SIGNAL)
  } finally {
    cleanup()
  }
  expect(result.outcome).toBe("ok")
  expect(result.readback_model).toBe("openai/gpt-5.6-luna")
  expect(result.session_id).toBe("session-1")
})

// The repository placeholder stays unstamped, so an adapter copy without a
// release binding still refuses instead of resolving `concord` ambiently.
test("concordBinaryPath refuses the unstamped placeholder when no override is bound", () => {
  configureCoreBinary(null)
  expect(() => concordBinaryPath()).toThrow("not bound to a release")
  configureCoreBinary("concord-test")
})

// CD-0111 D1 regression (#914): with no override bound, the resolution must
// reach the stamped release constant. An empty-string bound override would
// shadow it, so `null` is the unbound state and the per-call override wins
// over both.
test("resolveCoreBinary falls through an unbound override to the stamped constant", () => {
  expect(resolveCoreBinary(undefined, null, "/release/bin/concord")).toBe("/release/bin/concord")
  expect(resolveCoreBinary(undefined, "/test-bin/concord", "/release/bin/concord")).toBe("/test-bin/concord")
  expect(resolveCoreBinary("/call-override/concord", "/test-bin/concord", "/release/bin/concord")).toBe("/call-override/concord")
  expect(resolveCoreBinary(undefined, null, "")).toBe("")
})
