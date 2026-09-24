import { test, expect } from "bun:test"
import { createHash, randomUUID } from "node:crypto"
import { mkdtemp } from "node:fs/promises"
import fs from "node:fs"
import * as os from "node:os"
import path from "node:path"
import { agentLanes, workerScopeAssignedResult } from "./generated-agent-lanes"
import { boundedTextPrefix, completeWorkerAttempt, computeHostPromptProvenance, concordBinaryPath, configureCoreBinary, defaultRunner, dispatchWorker, MAX_READBACK_MESSAGE_PAGES, READBACK_MESSAGE_PAGE, readExportOpeningPacket, readExportSession, readExportSessionMetadata, readRunSessionMetadata, readWorkerSessionBody, resolveCoreBinary, validateAgentLanePacket, type AgentLanePacket, type CanonicalLaneReport, type DispatchAuthorizer, type DispatchRunner } from "./dispatch"
import type { RouteResult, SessionReader } from "./move-session"

// Fake-runner suite: bind worker-evidence CLI calls to a nominal core path
// instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord-test")
// This suite runs with no host. The completion path prefers a bound control
// plane for its session observation, and other test files bind fake clients
// into the module-shared instance; a runner that shares one process would
// otherwise hand this file a host it never asked for. State the precondition.
import { hostControlPlane } from "./move-session"
hostControlPlane().bind(undefined)
import { DispatchWindows, serializeLanePacket } from "./dispatch-window"
import { armClaimedWorktree, clearClaimedWorktree, recordUnlandedClaimedWorktree, resetClaimedWorktrees } from "./claimed-worktree"
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

// The completion path reads the worker session back through the host session
// API and records evidence. It never starts a process for the readback, so
// the reader answers the session record and its messages and nothing else.
// Completion reads one bounded session body for the packet and model
// readback, and the session index for live-session evidence. The dispatch
// window supplies the worker directory.
const sessionIndex = (directory = "/claimed/worktree") => JSON.stringify([{ id: "session-1", directory }])

const okRoute = (data: unknown, link?: string): RouteResult => ({
  data,
  response: new Response(JSON.stringify(data), { status: 200, headers: link ? { link } : undefined }),
})

// The fixture session carries one user message (the opener, which is the
// dispatch packet on the admitted path) and one assistant message carrying
// the model identity, exactly the two ends the readback checks.
const transcript = (model = READBACK_MODEL, agent = "concord-research", opener: unknown = packet()) => [
  { info: { id: "message-0", sessionID: "session-1", role: "user", agent, time: { created: 0 } }, parts: [{ type: "text", text: typeof opener === "string" ? opener : JSON.stringify(opener) }] },
  { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent, providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] },
]

const readbackSessionReader = (model = READBACK_MODEL, agent = "concord-research", opener: unknown = packet()): SessionReader => {
  const session = { id: "session-1" }
  return {
    async get() { return okRoute(session) },
    async messages(_sessionID, _limit, before) {
      if (before) return okRoute([])
      return okRoute(transcript(model, agent, opener))
    },
  }
}

const SIGNAL = new AbortController().signal
const SESSION = "session-parent"
const WORKER_DIRECTORY = process.cwd()

type CompleteOptions = Parameters<typeof completeWorkerAttempt>[3]
const acceptingEvidence = (): DispatchRunner => ({ async run() { return { exitCode: 0, stdout: "", stderr: "" } } })
// The fixture session opens with the packet under test, so the completion
// path verifies packet identity against the same dispatch it admits.
const complete = (body: string, options: Partial<CompleteOptions> = {}, dispatched: AgentLanePacket = packet()) =>
  completeWorkerAttempt(lane, dispatched, body, { credentials: testCredentials, sessionReader: readbackSessionReader(READBACK_MODEL, "concord-research", dispatched), evidenceRunner: acceptingEvidence(), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY, ...options }, SIGNAL)

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

// The armed claim is the adapter's own record of the last confirmed landing.
// The host's fresh session-directory answer must match it, and a process cwd
// inside a foreign managed worktree refuses before the window opens.
test("dispatch refuses when the host answer disagrees with the armed claimed worktree", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  fs.mkdirSync(path.join(root, "claimed"))
  fs.mkdirSync(path.join(root, "stale"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const stale = fs.realpathSync(path.join(root, "stale"))
  const previousDirectory = process.cwd()
  const windows = new DispatchWindows()
  try {
    armClaimedWorktree(SESSION, claimed)
    process.chdir(claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: stale,
      resolveWorkerDirectory: async () => stale,
      contextDirectory: stale,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.recovery_action).toBe("reconcile_operation")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toContain(stale)
    expect(result.error?.message).toMatch(/worktree_claim/)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    process.chdir(previousDirectory)
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// The observed dispatch failure is a host booted inside one item's managed
// worktree that then claims another item's worktree: every host metadata check
// passes and spawned lanes run in the boot worktree. The guard refuses exactly
// that state — a process cwd inside the armed claim's managed worktrees (the
// claim's parent directory) that is not the claim itself.
test("dispatch refuses when the process cwd is a foreign sibling worktree of the armed claim", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  fs.mkdirSync(path.join(root, "managed", "claimed"), { recursive: true })
  fs.mkdirSync(path.join(root, "managed", "sibling"))
  const claimed = fs.realpathSync(path.join(root, "managed", "claimed"))
  const sibling = fs.realpathSync(path.join(root, "managed", "sibling"))
  const previousDirectory = process.cwd()
  const windows = new DispatchWindows()
  try {
    armClaimedWorktree(SESSION, claimed)
    process.chdir(sibling)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: claimed,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.recovery_action).toBe("reconcile_operation")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toContain(sibling)
    expect(result.error?.message).toMatch(/restart the host process from the project trunk or in the claimed worktree/i)
    expect(result.error?.message).not.toMatch(/replay worktree_claim/i)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    process.chdir(previousDirectory)
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// The operator's launch route boots the host in the project trunk and relies
// on the work_start move; children of a trunk-booted host follow the claimed
// worktree. A trunk cwd sits outside the armed claim's managed worktrees, so
// it must never trip the sibling-worktree refusal.
test("dispatch proceeds when the process cwd is the project trunk with an armed claim", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  fs.mkdirSync(path.join(root, "managed", "claimed"), { recursive: true })
  fs.mkdirSync(path.join(root, "trunk"))
  const claimed = fs.realpathSync(path.join(root, "managed", "claimed"))
  const trunk = fs.realpathSync(path.join(root, "trunk"))
  const previousDirectory = process.cwd()
  const windows = new DispatchWindows()
  try {
    armClaimedWorktree(SESSION, claimed)
    process.chdir(trunk)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: claimed,
    })
    expect(result.outcome).toBe("ok")
    expect(result.dispatch_state).toBe("awaiting_worker")
    expect(windows.has(SESSION)).toBe(true)
  } finally {
    process.chdir(previousDirectory)
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

test("dispatch proceeds when the host answer agrees with the armed claimed worktree", async () => {
  const windows = new DispatchWindows()
  try {
    armClaimedWorktree(SESSION, WORKER_DIRECTORY)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: WORKER_DIRECTORY,
      resolveWorkerDirectory: async () => WORKER_DIRECTORY,
      contextDirectory: WORKER_DIRECTORY,
    })
    expect(result.outcome).toBe("ok")
    expect(result.dispatch_state).toBe("awaiting_worker")
    expect(windows.has(SESSION)).toBe(true)
  } finally {
    clearClaimedWorktree(SESSION)
  }
})

// Issue #1322: the host's session record can agree with the armed claim while
// the session's tools still run elsewhere, because the record converges before
// the effective tool context does. The calling tool context is where this
// dispatch actually executes, so it must sit inside the armed claim.
test("dispatch refuses when the calling tool context sits outside the armed claimed worktree", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-context-"))
  fs.mkdirSync(path.join(root, "claimed"))
  fs.mkdirSync(path.join(root, "elsewhere"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const elsewhere = fs.realpathSync(path.join(root, "elsewhere"))
  const windows = new DispatchWindows()
  let authorizeCalls = 0
  try {
    armClaimedWorktree(SESSION, claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: async () => { authorizeCalls++; return coreOk() },
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: elsewhere,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.recovery_action).toBe("reconcile_operation")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toContain(elsewhere)
    expect(result.error?.message).toMatch(/replay work_start or worktree_claim/)
    // The pre-effect preflight refuses before the core authorization, so a
    // mismatched context persists no authorized attempt (issue #1322).
    expect(authorizeCalls).toBe(0)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

test("dispatch proceeds when the calling tool context resolves inside the armed claimed worktree", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-context-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const windows = new DispatchWindows()
  try {
    armClaimedWorktree(SESSION, claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: claimed,
    })
    expect(result.outcome).toBe("ok")
    expect(result.dispatch_state).toBe("awaiting_worker")
    expect(windows.has(SESSION)).toBe(true)
  } finally {
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// Issue #1322, the pre-effect preflight: the dispatch_worker action persists
// an authorized attempt in the core, so a tool context outside the host
// session directory refuses before the core is asked at all — with no armed
// claim, unlanded record, or durable worktree to lean on.
test("a tool context outside the host session directory authorizes nothing in the core", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-preflight-"))
  fs.mkdirSync(path.join(root, "claimed"))
  fs.mkdirSync(path.join(root, "elsewhere"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const elsewhere = fs.realpathSync(path.join(root, "elsewhere"))
  const windows = new DispatchWindows()
  let authorizeCalls = 0
  try {
    resetClaimedWorktrees()
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: async () => { authorizeCalls++; return coreOk() },
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: elsewhere,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.recovery_action).toBe("reconcile_operation")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toContain(elsewhere)
    expect(result.error?.message).toMatch(/replay work_start or worktree_claim/)
    expect(authorizeCalls).toBe(0)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    resetClaimedWorktrees()
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// The preflight passes when the calling tool context resolves inside the host
// session directory, with no claim record needed: the core is asked exactly
// once and the window opens.
test("a tool context matching the host session directory authorizes and opens the window", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-preflight-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const windows = new DispatchWindows()
  let authorizeCalls = 0
  try {
    resetClaimedWorktrees()
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: async () => { authorizeCalls++; return coreOk() },
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: claimed,
    })
    expect(result.outcome).toBe("ok")
    expect(result.dispatch_state).toBe("awaiting_worker")
    expect(authorizeCalls).toBe(1)
    expect(windows.has(SESSION)).toBe(true)
  } finally {
    resetClaimedWorktrees()
    windows.close(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// Issue #1322, the refused-start state: a metadata-only work_start refused
// before arming, so no armed claim exists to gate this session. The unlanded
// record fails the dispatch gate closed until the tool context lands.
test("dispatch refuses while a metadata-only refusal leaves the claimed worktree unlanded", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  fs.mkdirSync(path.join(root, "elsewhere"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const elsewhere = fs.realpathSync(path.join(root, "elsewhere"))
  const windows = new DispatchWindows()
  let authorizeCalls = 0
  try {
    recordUnlandedClaimedWorktree(SESSION, claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: async () => { authorizeCalls++; return coreOk() },
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: elsewhere,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.recovery_action).toBe("reconcile_operation")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toContain(elsewhere)
    expect(result.error?.message).toMatch(/replay work_start/)
    expect(authorizeCalls).toBe(0)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// Without a context answer the landing cannot be proved, so the refused-start
// state stays refused.
test("dispatch fails closed for an unlanded claim when no tool context is supplied", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const windows = new DispatchWindows()
  try {
    recordUnlandedClaimedWorktree(SESSION, claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.message).toMatch(/tool context directory was not supplied/)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// The dispatch gate opens exactly when the tool context resolves inside the
// recorded claimed worktree, before any work_start replay arms the claim.
test("dispatch proceeds once the tool context resolves inside the unlanded claimed worktree", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const windows = new DispatchWindows()
  try {
    recordUnlandedClaimedWorktree(SESSION, claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: claimed,
    })
    expect(result.outcome).toBe("ok")
    expect(result.dispatch_state).toBe("awaiting_worker")
    expect(windows.has(SESSION)).toBe(true)
  } finally {
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// Issue #1322, the stale-claim bypass: a session with an armed claim from a
// prior landing retargets, and the newer metadata-only refusal records the
// move as unlanded. The newest unlanded move takes precedence, so the host
// answers and the tool context agreeing on the PRIOR directory cannot open a
// window while the newer move is unresolved.
test("dispatch refuses while a newer unlanded move stands despite a prior armed claim", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  fs.mkdirSync(path.join(root, "previous"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const previous = fs.realpathSync(path.join(root, "previous"))
  const windows = new DispatchWindows()
  try {
    armClaimedWorktree(SESSION, previous)
    recordUnlandedClaimedWorktree(SESSION, claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: previous,
      resolveWorkerDirectory: async () => previous,
      contextDirectory: previous,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.recovery_action).toBe("reconcile_operation")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toMatch(/replay work_start/)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// Issue #1322, the eviction bypass: an unlanded record is the fail-closed
// state of a refused move whose only exits are a confirmed landing or vacate.
// Cap eviction would forget an unresolved move and dispatch would authorize
// on lost in-memory state, so the record must survive map pressure.
test("an unlanded record survives map pressure until its move lands", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const windows = new DispatchWindows()
  try {
    recordUnlandedClaimedWorktree(SESSION, claimed)
    for (let i = 0; i < 512; i++) recordUnlandedClaimedWorktree(`session-filler-${i}`, claimed)
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.message).toMatch(/tool context directory was not supplied/)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    resetClaimedWorktrees()
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// Issue #1322, the durable gate: a host process restart empties both in-memory
// claim maps, so the tool-context gate cannot rest on adapter memory alone.
// The authorized dispatch names the claimed worktree the core authorized it
// against, and the calling tool context must resolve inside it even with no
// claim record left in this process.
test("dispatch fails closed on the authorized claimed worktree after both claim maps are empty", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  fs.mkdirSync(path.join(root, "elsewhere"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const elsewhere = fs.realpathSync(path.join(root, "elsewhere"))
  const windows = new DispatchWindows()
  let authorizeCalls = 0
  try {
    resetClaimedWorktrees()
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: async () => { authorizeCalls++; return coreOk() },
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      authorizedWorktree: claimed,
      contextDirectory: elsewhere,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.recovery_action).toBe("reconcile_operation")
    expect(result.error?.message).toContain(claimed)
    expect(result.error?.message).toContain(elsewhere)
    expect(result.error?.message).toMatch(/replay work_start or worktree_claim/)
    expect(authorizeCalls).toBe(0)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    resetClaimedWorktrees()
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// The durable gate fails closed without a context answer too: an authorized
// dispatch cannot prove its landing from host metadata alone.
test("the authorized claimed worktree gate refuses when no tool context is supplied", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const windows = new DispatchWindows()
  try {
    resetClaimedWorktrees()
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      authorizedWorktree: claimed,
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("unauthorized_dispatch")
    expect(result.error?.message).toMatch(/tool context directory was not supplied/)
    expect(windows.has(SESSION)).toBe(false)
  } finally {
    resetClaimedWorktrees()
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// The durable gate opens exactly when the tool context resolves inside the
// claimed worktree the core authorized, with no in-memory claim record needed.
test("dispatch proceeds when the tool context resolves inside the authorized claimed worktree", async () => {
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-unlanded-"))
  fs.mkdirSync(path.join(root, "claimed"))
  const claimed = fs.realpathSync(path.join(root, "claimed"))
  const windows = new DispatchWindows()
  try {
    resetClaimedWorktrees()
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      authorizedWorktree: claimed,
      contextDirectory: claimed,
    })
    expect(result.outcome).toBe("ok")
    expect(result.dispatch_state).toBe("awaiting_worker")
    expect(windows.has(SESSION)).toBe(true)
  } finally {
    resetClaimedWorktrees()
    windows.close(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
})

// CD-0102 D1: an authorized dispatch opens the window and returns before the
// worker runs. The host issues the Task call, so the adapter starts no process
// and asserts no model here.
test("an authorized dispatch opens one window and returns a directive", async () => {
  const windows = new DispatchWindows()
  clearClaimedWorktree(SESSION)
  const root = fs.mkdtempSync(path.join(process.cwd(), "concord-dispatch-"))
  const claimed = path.join(root, "unarmed")
  fs.mkdirSync(claimed)
  try {
    const result = await dispatchWorker(packet(), {
      credentials: testCredentials,
      authorize: permissiveAuthorizer(),
      packetDigest: PACKET_DIGEST,
      sessionID: SESSION,
      windows,
      workerDirectory: claimed,
      resolveWorkerDirectory: async () => claimed,
      contextDirectory: claimed,
    })
    expect(result.outcome).toBe("ok")
    expect(result.dispatch_state).toBe("awaiting_worker")
    expect(result.agent).toBe("concord-research")
    expect(result.readback_model).toBe(null)
    expect(windows.has(SESSION)).toBe(true)
  } finally {
    clearClaimedWorktree(SESSION)
    fs.rmSync(root, { recursive: true, force: true })
  }
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

test("completion obtains readback from one in-process session body", async () => {
  const readerCalls: string[][] = []
  const cliCalls: string[][] = []
  const base = readbackSessionReader()
  const result = await complete(workerBody(), {
    sessionReader: {
      get(sessionID, signal) { readerCalls.push(["session.get", sessionID]); return base.get(sessionID, signal) },
      messages(sessionID, limit, before, signal) { readerCalls.push(["session.messages", sessionID, `limit=${limit}`, before ?? ""]); return base.messages(sessionID, limit, before, signal) },
    },
    evidenceRunner: { async run(argv) { cliCalls.push(argv); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("ok")
  // One session record, then one bounded message page with no cursor to
  // follow — the whole transcript fit inside the first page.
  expect(readerCalls).toEqual([["session.get", "session-1"], ["session.messages", "session-1", `limit=${READBACK_MESSAGE_PAGE}`, ""]])
  // The live-session observation still rides the CLI runner seam, ahead of
  // the worker-dispatch and worker-complete evidence records.
  expect(cliCalls.map((argv) => argv.slice(0, 2))).toEqual([
    ["opencode", "session"],
    ["concord-test", "worker-dispatch"],
    ["concord-test", "worker-complete"],
  ])
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

test("a worker transcript above the old export limit records completion", async () => {
  const messages: unknown[] = transcript()
  messages.splice(1, 0, {
    info: { id: "message-large", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0.5 } },
    parts: [{ type: "text", text: "x".repeat(14_000_000) }],
  })
  expect(Buffer.byteLength(JSON.stringify(messages))).toBeGreaterThan(8_388_608)
  const recorded: string[] = []
  const result = await complete(workerBody(), {
    sessionReader: {
      async get() { return okRoute({ id: "session-1" }) },
      async messages() { return okRoute(messages) },
    },
    evidenceRunner: {
      async run(argv) { recorded.push(argv[1]); return { exitCode: 0, stdout: "", stderr: "" } },
    },
  })
  expect(result.outcome).toBe("ok")
  expect(result.readback_model).toBe(READBACK_MODEL)
  expect(recorded).toEqual(["session", "worker-dispatch", "worker-complete"])
})

test("a transcript that outlives the readback page bound refuses typed", async () => {
  // A reader that always advertises a next page simulates a transcript the
  // page walk never reaches the head of; the walk refuses after its bound.
  const session = { id: "session-1" }
  const alwaysMore: SessionReader = {
    async get() { return okRoute(session) },
    async messages() { return okRoute(transcript(), `<http://host/session/session-1/message?limit=${READBACK_MESSAGE_PAGE}&before=cursor-1>; rel="next"`) },
  }
  const read = await readWorkerSessionBody(alwaysMore, "session-1", SIGNAL)
  expect(read.ok).toBe(false)
  if (read.ok || read.kind !== "readback_refusal") {
    throw new Error("the page-bound walk refused with the wrong kind")
  }
  expect(read.predicate).toBe("readback_message_bound")
  expect(read.message).toContain(`${MAX_READBACK_MESSAGE_PAGES} readback pages`)
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

// A session whose record identity does not match the worker session refuses
// through the typed shape predicate, and a valid session still completes.
test("readback refusal is typed and does not change valid completion", async () => {
  const foreign: SessionReader = {
    async get() { return okRoute({ id: "session-9" }) },
    async messages(_id, _limit, before) { return before ? okRoute([]) : okRoute(transcript()) },
  }
  const result = await complete(workerBody(), { sessionReader: foreign })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("readback_refusal")
  expect(result.error?.predicate).toBe("export_shape")
  expect(result.error?.message).toContain("readback predicate export_shape refused")
  expect(result.error?.retry_safe).toBe(false)

  const accepted = await complete(workerBody())
  expect(accepted.outcome).toBe("ok")
  expect(accepted.readback_model).toBe(READBACK_MODEL)
})

// A refused readback stays terminal, but the body it refused and the work the
// lane produced are retained: the recorded failed attempt carries the host
// request identity, the status, and bounded prefixes of the readback body and
// the worker result, so the store alone is enough to diagnose and recover.
test("a refused readback retains session diagnostics on the recorded failed attempt", async () => {
  const calls: string[] = []
  const result = await complete(workerBody(), {
    sessionReader: {
      async get() { return okRoute({ id: "session-9" }) },
      async messages(_id, _limit, before) { return before ? okRoute([]) : okRoute(transcript()) },
    },
    evidenceRunner: { async run(argv, input) { calls.push(input); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("readback_refusal")
  expect(calls.map((input) => JSON.parse(input).terminal)).toEqual(["failed"])
  const refusedBody = JSON.stringify({ info: { id: "session-9" }, messages: transcript() })
  const detail = JSON.parse(calls[0]).terminal_detail as string
  expect(detail).toContain("readback predicate export_shape refused")
  expect(detail).toContain(`export_digest sha256:${createHash("sha256").update(refusedBody, "utf8").digest("hex")}`)
  expect(detail).toContain(`export_bytes ${Buffer.byteLength(refusedBody)}`)
  expect(detail).toContain("command session.messages session-1 limit=")
  expect(detail).toContain("exit_code 200")
  expect(detail).toContain("signal_state none")
  expect(detail).toContain(`export_body_prefix ${boundedTextPrefix(refusedBody, 1024)}`)
  expect(Buffer.byteLength(detail, "utf8")).toBeLessThanOrEqual(4096)
})

test("a host session read failure carries the host diagnostic and reconciles", async () => {
  const result = await complete(workerBody(), {
    sessionReader: {
      async get() { throw new Error("host socket closed") },
      async messages() { return okRoute([]) },
    },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.message).toContain("the host session read failed: host socket closed")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
})

test("a refused readback preserves the completed work it would have carried", async () => {
  const calls: string[] = []
  // The completion window owns the unwrapped worker text, so that is the work
  // the refusal must preserve.
  const carried = JSON.stringify(report())
  const result = await complete(taskWrap(carried), {
    sessionReader: {
      async get() { return okRoute({ id: "session-9" }) },
      async messages(_id, _limit, before) { return before ? okRoute([]) : okRoute(transcript()) },
    },
    evidenceRunner: { async run(argv, input) { calls.push(input); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("readback_refusal")
  expect(result.error?.retry_safe).toBe(false)
  // The work rides out the refusal on the envelope and on the durable record.
  expect(result.output).toBe(carried)
  expect(JSON.parse(calls[0]).terminal_detail).toContain(`worker_result_prefix ${carried}`)
  // The refusal stays terminal: one failed dispatch, no completion evidence.
  expect(calls).toHaveLength(1)
  expect(JSON.parse(calls[0]).terminal).toBe("failed")
  expect(JSON.parse(calls[0]).readback_model).toBe("")
})

test("the retained worker result prefix stays inside the failure detail bound", async () => {
  const calls: string[] = []
  const result = await complete(taskWrap("x".repeat(20_000)), {
    sessionReader: {
      async get() { return okRoute({ id: "session-9" }) },
      async messages(_id, _limit, before) { return before ? okRoute([]) : okRoute(transcript()) },
    },
    evidenceRunner: { async run(argv, input) { calls.push(input); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  // The envelope carries the whole body; the store record retains the prefix.
  expect(result.output).toBe("x".repeat(20_000))
  const detail = JSON.parse(calls[0]).terminal_detail as string
  expect(detail).toContain(`worker_result_prefix ${"x".repeat(1536)}`)
  expect(detail).not.toContain("x".repeat(1537))
  expect(Buffer.byteLength(detail, "utf8")).toBeLessThanOrEqual(4096)
})

// Fail-closed is unchanged: the refused session body is never completed from
// a second read. Exactly one message page runs, and the attempt ends in the
// typed refusal with no retry and no alternate readback source.
test("a refused session body is never completed from a second read", async () => {
  let reads = 0
  const result = await complete(workerBody(), {
    sessionReader: {
      async get() { return okRoute({ id: "session-1" }) },
      async messages(_id, _limit, before) {
        if (!before) reads++
        return okRoute([{ info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: "not the packet" }] }])
      },
    },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.predicate).toBe("dispatched_packet_identity")
  expect(result.error?.retry_safe).toBe(false)
  expect(reads).toBe(1)
})

test("defaultRunner reports the terminating signal", async () => {
  const killed = await defaultRunner.run(["sh", "-c", "kill -TERM $$"], "", SIGNAL)
  expect(killed.signal_state).toBe("SIGTERM")
  const clean = await defaultRunner.run(["sh", "-c", "exit 0"], "", SIGNAL)
  expect(clean.exitCode).toBe(0)
  expect(clean.signal_state).toBeNull()
})

test("boundedTextPrefix cuts on a UTF-8 boundary and under the bound", () => {
  const text = `${"x".repeat(10)}${"é".repeat(5)}`
  expect(Buffer.byteLength(text, "utf8")).toBe(20)
  expect(boundedTextPrefix(text, 11)).toBe("x".repeat(10))
  expect(boundedTextPrefix(text, 12)).toBe(`${"x".repeat(10)}é`)
  expect(boundedTextPrefix("short", 100)).toBe("short")
})

// CD-0102 completion identity: a worker session that did not open with the
// authorized packet is refused through the typed readback predicate, records
// one durable failed attempt, and signs no completion evidence.
const packetRefusalReader = (opener: string): SessionReader => ({
  async get() { return okRoute({ id: "session-1" }) },
  async messages(_id, _limit, before) { return before ? okRoute([]) : okRoute(transcript(READBACK_MODEL, "concord-research", opener)) },
})

test("caller-composed prose as the first user message refuses the attempt", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const result = await complete(workerBody(), {
    sessionReader: packetRefusalReader("Fix the bug in the adapter, then summarize what you changed."),
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
  expect(calls.filter((call) => call.argv[1].startsWith("worker-")).map((call) => call.argv[1])).toEqual(["worker-dispatch"])
  expect(JSON.parse(calls[0].input).terminal).toBe("failed")
  expect(JSON.parse(calls[0].input).terminal_failure_kind).toBe("model_readback_missing")
  expect(JSON.parse(calls[0].input).readback_model).toBe("")
  expect(JSON.parse(calls[0].input).terminal_detail).toContain("readback predicate dispatched_packet_identity refused")
})

test("a packet for another attempt refuses the attempt the same way", async () => {
  const foreign = { ...packet(), attempt_id: "attempt-other" }
  const calls: string[] = []
  const result = await complete(workerBody(), {
    sessionReader: packetRefusalReader(JSON.stringify(foreign)),
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
// cannot establish identity. The host's own wrapper parts — the agent part
// and the synthetic instruction the host derives when the packet text
// mentions the lane agent — are the one exception, and they must match the
// host's deterministic bytes for this lane's agent.
test("readExportOpeningPacket refuses content beside the exact packet", () => {
  const openingParts = (parts: unknown[]) => JSON.stringify({
    info: { id: "session-1" },
    messages: [{ info: { id: "message-1", sessionID: "session-1", role: "user", time: { created: 1 } }, parts }],
  })
  const withFile = readExportOpeningPacket(openingParts([
    { type: "text", text: JSON.stringify(packet()) },
    { type: "file", filename: "notes.md", url: "file:///notes.md" },
  ]), "session-1", packet())
  expect(withFile).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with a file part instead of the authorized dispatch packet" })
  const splitText = readExportOpeningPacket(openingParts([
    { type: "text", text: JSON.stringify(packet()).slice(0, 10) },
    { type: "text", text: JSON.stringify(packet()).slice(10) },
  ]), "session-1", packet())
  expect(splitText).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with an unauthorized text part beside the authorized dispatch packet" })
  const fileOnly = readExportOpeningPacket(openingParts([{ type: "file", filename: "notes.md", url: "file:///notes.md" }]), "session-1", packet())
  expect(fileOnly).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with a file part instead of the authorized dispatch packet" })
  expect(readExportOpeningPacket(openingParts([]), "session-1", packet())).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with 0 message parts instead of the single authorized dispatch packet" })
})

// A packet whose correction context quotes a prior session title carries
// "@concord-implement". The serializer escapes every '@', so the Task prompt
// holds no host @agent mention, and JSON decoding restores the exact packet.
test("serializeLanePacket escapes host agent mentions and round-trips", () => {
  const mentioned = { ...packet(), inputs: { ...packet().inputs, task: "prior title: implement lane (@concord-implement subagent)" } } as AgentLanePacket
  const text = serializeLanePacket(mentioned)
  expect(text.includes("@")).toBe(false)
  expect(text.includes("\\u0040concord-implement")).toBe(true)
  expect(JSON.parse(text)).toEqual(mentioned)
})

// The opening message is exactly the serialized packet. A host agent part or
// synthetic instruction beside it means a mention formed, and it refuses.
test("readExportOpeningPacket refuses a host agent part and wrapper beside the packet", () => {
  const openingParts = (parts: unknown[]) => JSON.stringify({
    info: { id: "session-1" },
    messages: [{ info: { id: "message-1", sessionID: "session-1", role: "user", time: { created: 1 } }, parts }],
  })
  const laneAgent = "concord-" + packet().lane_id
  expect(readExportOpeningPacket(openingParts([{ type: "text", text: serializeLanePacket(packet()) }]), "session-1", packet())).toEqual({ ok: true })
  const wrapped = readExportOpeningPacket(openingParts([
    { type: "text", text: serializeLanePacket(packet()) },
    { type: "agent", name: laneAgent },
    { type: "text", text: " Use the above message and context to generate a prompt and call the task tool with subagent: " + laneAgent },
  ]), "session-1", packet())
  expect(wrapped).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: `worker session opened with a host agent part for ${laneAgent} beside the authorized dispatch packet` })
  const duplicatedPacket = readExportOpeningPacket(openingParts([
    { type: "text", text: serializeLanePacket(packet()) },
    { type: "text", text: serializeLanePacket(packet()) },
  ]), "session-1", packet())
  expect(duplicatedPacket).toEqual({ ok: false, predicate: "dispatched_packet_identity", message: "worker session opened with the authorized dispatch packet repeated beside itself" })
})

test("ambiguous model readback records one durable failed attempt", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const exportReader: SessionReader = {
    async get() { return okRoute({ id: "session-1" }) },
    async messages(_id, _limit, before) {
      if (before) return okRoute([])
      return okRoute([
        { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
        { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "first", time: { created: 1 } }, parts: [] },
        { info: { id: "message-2", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "second", time: { created: 2 } }, parts: [] },
      ])
    },
  }
  const result = await complete(workerBody(), {
    sessionReader: exportReader,
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(calls.filter((call) => call.argv[1].startsWith("worker-")).map((call) => call.argv[1])).toEqual(["worker-dispatch"])
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
  const exportReader: SessionReader = {
    async get() { return okRoute({ id: "session-1" }) },
    async messages(_id, _limit, before) {
      if (before) return okRoute([])
      return okRoute([
        { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
      ])
    },
  }
  const result = await complete(workerBody(), {
    sessionReader: exportReader,
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("error")
  expect(calls.filter((call) => call.argv[1].startsWith("worker-")).map((call) => call.argv[1])).toEqual(["worker-dispatch"])
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
    sessionReader: readbackSessionReader(READBACK_MODEL, "adv"),
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
  const records = calls.filter((call) => call.argv[1].startsWith("worker-"))
  expect(records.map((call) => call.argv)).toEqual([["concord-test", "worker-dispatch"], ["concord-test", "worker-complete"]])

  const dispatched = JSON.parse(records[0].input)
  expect(dispatched.work_id).toBe("work-1")
  expect(dispatched.attempt_id).toBe("attempt-1")
  expect(dispatched.lane_id).toBe(lane.id)
  expect(dispatched.lane_version).toBe(lane.version)
  expect(dispatched.lane_digest).toBe(lane.digest)
  expect(dispatched.readback_model).toBe(READBACK_MODEL)
  expect(dispatched.packet_schema_version).toBe("1.0")
  expect(dispatched.report_schema_version).toBe("1.0")
  expect(typeof dispatched.event_id).toBe("string")

  const completed = JSON.parse(records[1].input)
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
  expect(verbs.filter((verb) => verb.startsWith("worker-"))).toEqual(["worker-dispatch", "worker-complete", "worker-fail"])
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
  const result = await complete(workerBody(report({}, hostExecuted)), { sessionReader: readbackSessionReader(hostExecuted) })
  expect(result.outcome).toBe("ok")
  expect(result.readback_model).toBe(hostExecuted)
})

test("an unknown readback is recorded as-is and not refused", async () => {
  const hostExecuted = "openai/not-declared"
  const result = await complete(workerBody(report({}, hostExecuted)), { sessionReader: readbackSessionReader(hostExecuted) })
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
    const result = await complete(workerBody(report()), {
      workerDirectory: worker,
      sessionReader: readbackSessionReader(),
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
    sessionReader: readbackSessionReader(),
    // The CLI runner also carries the live-session observation; its session
    // list fails here and completion must tolerate that.
    evidenceRunner: {
      async run(argv) {
        evidenceCalls++
        if (argv[1] === "session") return { exitCode: 1, stdout: "", stderr: "session list failed" }
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    },
  })
  expect(result.outcome).toBe("ok")
  expect(evidenceCalls).toBe(3)
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
import { readWorkerReport, resolveWorkerReport, resolveWorkerReportFromText, scanReportTexts, validateAgentLaneReport, validateAgainstSchema, type AgentLaneReportBaseComparison } from "./dispatch"

async function terminalEvidence(carried: unknown = report()) {
  const calls: { argv: string[]; input: string }[] = []
  const result = await complete(workerBody(carried), {
    concordBinary: "concord-test",
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  // The session-list observation rides the same CLI runner; the evidence
  // records are the worker-* calls alone.
  const records = calls.filter((call) => call.argv[1].startsWith("worker-"))
  return { result, verbs: records.map((call) => call.argv[1]), payloads: records.map((call) => JSON.parse(call.input)) }
}

test("a valid completed report carries its reported evidence into worker-complete", async () => {
  const { result, verbs, payloads } = await terminalEvidence()
  expect(result.outcome).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence_origin).toBe("reported")
  expect(payloads[1].evidence).toEqual(reportEvidence())
  expect(payloads[1].report_schema_version).toBe("1.0")
})

// The dispatch window owns the worker directory; the report no longer carries
// it. A report that omits cwd admits completion, and a worker that echoes the
// field anyway is refused by the closed schema.
test("report-admits-without-cwd: a report omitting the worker directory admits completion", async () => {
  expect(Object.keys(report())).not.toContain("cwd")
  const { result, verbs, payloads } = await terminalEvidence(report())
  expect(result.outcome).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence).toEqual(reportEvidence())
  expect(payloads[1].report_schema_version).toBe("1.0")
})

test("a report echoing the retired cwd field is an invalid report", async () => {
  const { result, verbs, payloads } = await terminalEvidence(report({ cwd: WORKER_DIRECTORY }))
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_report")
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
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
    sessionReader: readbackSessionReader(READBACK_MODEL, `concord-${target.id}`, lanePacketFor(laneID)),
    packetDigest: PACKET_DIGEST,
    workerDirectory: process.cwd(),
    concordBinary: "concord-test",
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  }, SIGNAL)
  // The session-list observation rides the same CLI runner; the evidence
  // records are the worker-* calls alone.
  const records = calls.filter((call) => call.argv[1].startsWith("worker-"))
  return { result, verbs: records.map((call) => call.argv[1]), payloads: records.map((call) => JSON.parse(call.input)) }
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

  // The worker-scope contract bounds the attempt to one assigned result, so a
  // report missing that discharge is the assigned-result defect: it fails the
  // attempt as invalid_report and names the result it did not complete.
  test(`the ${laneID} lane fails a report that leaves its assigned result ${workerScopeAssignedResult(laneID)} undischargeable`, async () => {
    const assigned = workerScopeAssignedResult(laneID)!
    const evidence = dischargingEvidence(laneID).filter((entry) => entry.obligation !== assigned)
    const { result, verbs, payloads } = await laneTerminalEvidence(laneID, evidence)
    expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
    expect(payloads[1].failure_kind).toBe("invalid_report")
    expect(payloads[1].detail).toContain("assigned result")
    expect(payloads[1].detail).toContain(assigned)
    expect(result.error?.kind).toBe("invalid_report")
    expect(result.error?.retry_safe).toBe(false)
  })

  test(`the ${laneID} lane admits a report that discharges exactly its declared obligations`, async () => {
    const evidence = dischargingEvidence(laneID)
    const { result, verbs, payloads } = await laneTerminalEvidence(laneID, evidence)
    expect(result.outcome).toBe("ok")
    expect(result.assigned_result).toBe(workerScopeAssignedResult(laneID)!)
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

test("an oversized evidence detail is normalized before admission", async () => {
  const evidence = [{ ...reportEvidence()[0], detail: "x".repeat(513) }, ...reportEvidence().slice(1)]
  const { verbs, payloads } = await terminalEvidence(report({ evidence }))
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].evidence[0].detail).toBe("x".repeat(500) + " [truncated]")
  expect(payloads[1].evidence[1]).toEqual(reportEvidence()[1])
})

test("an unknown report top-level field is worker-fail with invalid_report", async () => {
  const { verbs, payloads } = await terminalEvidence(report({ unexpected: true }))
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
  expect(payloads[1].detail).toContain("carries undeclared property unexpected")
})

// CD-0056 D7 as amended 2026-09-17: a near-identical echoed attempt id is
// stripped and the packet value wins — one-character transcription drift no
// longer costs the run, because identity is never taken from the model.
test("model-supplied identity is stripped, however near-identical", () => {
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
    expect(resolved).toEqual({ report: canonicalReport({}, dispatched) })
  }
})

test("each dispatch-owned identity field is stripped when the model supplies it", () => {
  const conflicts: Record<string, unknown> = {
    attempt_id: "attempt-work-1-other",
    lane_id: "review",
    lane_version: 99,
    lane_digest: "sha256:" + "0".repeat(64),
  }
  for (const [field, value] of Object.entries(conflicts)) {
    const resolved = resolveWorkerReport(runOutput("", report({ [field]: value })), packet())
    expect(resolved).toEqual({ report: canonicalReport() })
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
  const records = calls.filter((call) => call.argv[1].startsWith("worker-"))
  const payloads = records.map((call) => JSON.parse(call.input))
  expect(result.outcome).toBe("ok")
  expect(records.map((call) => call.argv[1])).toEqual(["worker-dispatch", "worker-complete"])
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

test("a fenced report is admitted and prose around the JSON no longer discards it", async () => {
  const fenced = "```json\n" + JSON.stringify(report()) + "\n```"
  expect(readWorkerReport(runOutput("", fenced)).report).toEqual(report())
  expect(readWorkerReport(runOutput("", "```\n" + JSON.stringify(report()) + "\n```")).report).toEqual(report())
  const { verbs } = await terminalEvidence(fenced)
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  // CON-203: one lead-in and one trailing sentence around the JSON is an
  // answer, not a failure; admission stays closed at the schema.
  expect(readWorkerReport(runOutput("", `Here is the report: ${JSON.stringify(report())} — done.`)).report).toEqual(report())
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
  // CD-0056 D7 as amended 2026-09-17: an echoed dispatch-owned field is
  // stripped, not refused; the packet value still wins.
  const echoed = resolveWorkerReport(runOutput("", report({ lane_digest: "sha256:" + "0".repeat(64) })), packet())
  expect(echoed).toEqual({ report: canonicalReport() })
  const missing = resolveWorkerReport(runOutput("", report({ status: undefined })), packet())
  expect("detail" in missing && missing.detail).toContain("missing required property status")
})

test("the native-text admission path composes packet identity over echoed identity", () => {
  expect(resolveWorkerReportFromText(JSON.stringify(report()), packet())).toEqual({ report: canonicalReport() })
  const echoed = resolveWorkerReportFromText(JSON.stringify(report({ attempt_id: "attempt-work-1-other" })), packet())
  expect(echoed).toEqual({ report: canonicalReport() })
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

const baseComparison = (): AgentLaneReportBaseComparison => ({
  checks: [
    { command: "go test ./...", branch_result: "pass", base_result: "pass" },
    { command: "go vet ./...", branch_result: "pass", base_result: "fail" },
  ],
})

test("the report schema admits the optional base_comparison and refuses drifted shapes", () => {
  expect(validateAgentLaneReport(report({ base_comparison: baseComparison() }))).toBe(true)
  expect(validateAgentLaneReport(report())).toBe(true)
  expect(validateAgentLaneReport(report({ base_comparison: { checks: [] } }))).toBe(true)
  const refusals = [
    { name: "unknown sibling property", value: { ...baseComparison(), mood: "confident" } },
    { name: "missing checks", value: {} },
    { name: "oversized checks", value: { checks: Array.from({ length: 65 }, () => baseComparison().checks[0]) } },
    { name: "check with an undeclared property", value: { checks: [{ ...baseComparison().checks[0], exit_code: 0 }] } },
    { name: "result outside the closed set", value: { checks: [{ ...baseComparison().checks[0], base_result: "skipped" }] } },
    { name: "empty command", value: { checks: [{ ...baseComparison().checks[0], command: "" }] } },
    { name: "command beyond 512 characters", value: { checks: [{ ...baseComparison().checks[0], command: "x".repeat(513) }] } },
    { name: "command within characters but beyond 512 bytes", value: { checks: [{ ...baseComparison().checks[0], command: "é".repeat(300) }] } },
  ]
  for (const refusal of refusals) {
    expect(validateAgentLaneReport(report({ base_comparison: refusal.value })), refusal.name).toBe(false)
  }
})

test("admission carries a valid base_comparison into the canonical report and refuses a drifted one", () => {
  const carried = resolveWorkerReportFromText(JSON.stringify(report({ base_comparison: baseComparison() })), packet())
  expect("report" in carried && carried.report.base_comparison).toEqual(baseComparison())
  const drifted = resolveWorkerReportFromText(JSON.stringify(report({ base_comparison: { checks: [{ ...baseComparison().checks[0], branch_result: "skipped" }] } })), packet())
  expect("detail" in drifted && drifted.detail).toContain("failed the closed agent-lane-report.v1 schema")
})

test("a reported base_comparison rides worker-complete and the completed envelope", async () => {
  const { result, verbs, payloads } = await terminalEvidence(report({ base_comparison: baseComparison() }))
  expect(result.outcome).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].base_comparison).toEqual(baseComparison())
  expect(result.base_comparison).toEqual(baseComparison())
})

test("the largest base_comparison the schema admits completes and reaches the envelope", async () => {
  const largest: AgentLaneReportBaseComparison = { checks: Array.from({ length: 64 }, () => ({ command: "x".repeat(512), branch_result: "fail" as const, base_result: "not_run" as const })) }
  const carried = report({ base_comparison: largest })
  expect(validateAgentLaneReport(carried)).toBe(true)
  const { result, verbs, payloads } = await terminalEvidence(carried)
  expect(result.outcome, JSON.stringify(result.error)).toBe("ok")
  expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  expect(payloads[1].base_comparison).toEqual(largest)
  expect(result.base_comparison).toEqual(largest)
})

test("an absent base_comparison reaches neither worker-complete nor the envelope", async () => {
  const { result, payloads } = await terminalEvidence()
  expect(result.outcome).toBe("ok")
  expect(payloads[1].base_comparison).toBeUndefined()
  expect(result.base_comparison).toBeUndefined()
})

test("a drifted base_comparison is a typed invalid report, never a completion", async () => {
  const { result, verbs, payloads } = await terminalEvidence(report({ base_comparison: { checks: [{ ...baseComparison().checks[0], base_result: "skipped" }] } }))
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_report")
  expect(verbs).toEqual(["worker-dispatch", "worker-fail"])
  expect(payloads[1].failure_kind).toBe("invalid_report")
})

test("an over-length evidence detail is truncated at admission, not refused", () => {
  const long = "x".repeat(900)
  const admitted = resolveWorkerReportFromText(JSON.stringify(report({
    evidence: [{ obligation: "source_citations", detail: long }, ...reportEvidence().slice(1)],
  })), packet())
  expect("detail" in admitted).toBe(false)
  const entry = ("report" in admitted ? admitted.report.evidence[0] : null) as { obligation: string, detail: string }
  expect(entry.obligation).toBe("source_citations")
  expect(entry.detail.length).toBe(512)
  expect(entry.detail.endsWith(" [truncated]")).toBe(true)
  expect(entry.detail.startsWith("x".repeat(500))).toBe(true)
  // An entry inside the cap is carried through byte for byte.
  const untouched = ("report" in admitted ? admitted.report.evidence[1] : null) as { detail: string }
  expect(untouched.detail).toBe(reportEvidence()[1].detail)
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

// The probe guards execution, not dispatch. Authorization runs first, because
// an approval challenge or a refused dispatch returns without a worker and must
// not require a credential. Once authorization passes, a spawn is imminent, so
// an unreadable credential refuses here rather than after a wasted lane run.
test("a credential probe failure refuses after authorization and before worker execution", async () => {
  const events: string[] = []
  const windows = new DispatchWindows()
  const result = await dispatchWorker(packet(), {
    credentials: { async getPrivateKey() { events.push("credential"); throw new Error("credential service unavailable") } },
    workerDirectory: WORKER_DIRECTORY,
    sessionID: SESSION,
    windows,
    runner: { async run() { events.push("run"); return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
    evidenceRunner: { async run() { events.push("evidence"); return { exitCode: 0, stdout: "", stderr: "" } } },
    async authorize() { events.push("authorize"); return coreOk() },
  })

  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.message).toContain("credential service unavailable")
  expect(events).toEqual(["authorize", "credential"])
  expect(windows.has(SESSION)).toBe(false)
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

// The readback reads the session in process, so a transcript larger than any
// stdio pipe buffer completes: the size lives on message text the readback
// never parses, and the two-end read still finds the opening packet and the
// latest assistant identity.
const largeTranscriptReader = (): SessionReader => {
  const session = { id: "session-1" }
  const messages = [
    { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
    { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [{ type: "text", text: "x".repeat(70_000) }] },
  ]
  return {
    async get() { return okRoute(session) },
    async messages(_sessionID, _limit, before) { return before ? okRoute([]) : okRoute(messages) },
  }
}

test("TestDispatchWorkerCompletesWithSessionLargerThanPipeBuffer", async () => {
  const result = await completeWorkerAttempt(lane, packet(), workerBody(), {
    credentials: testCredentials,
    sessionReader: largeTranscriptReader(),
    evidenceRunner: acceptingEvidence(),
    packetDigest: PACKET_DIGEST,
    workerDirectory: WORKER_DIRECTORY,
  }, SIGNAL)
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

// CON-203: a worker that wraps its report in prose is answering, not
// failing. The scan admits the last parseable JSON object across every
// fenced block and brace candidate, because admission stays closed at the
// schema and the last-parseable rule already decides between candidates.
test("scanReportTexts admits a report wrapped in surrounding prose", () => {
  const json = JSON.stringify(report())
  const cases: [string, string][] = [
    ["lead-in sentence then fence", `Here is my report:\n\n\`\`\`json\n${json}\n\`\`\``],
    ["fence then trailing sentence", `\`\`\`json\n${json}\n\`\`\`\n\nLet me know if more is needed.`],
    ["unfenced json after prose", `Working... final answer follows.\n${json}`],
    ["whole-text fence", "```json\n" + json + "\n```"],
    ["bare json", json],
  ]
  for (const [name, text] of cases) {
    const scan = scanReportTexts([text])
    expect(scan.report, name).toEqual(report())
    expect(scan.malformed, name).toBe(false)
  }
})

test("scanReportTexts still distinguishes broken announced json from prose", () => {
  expect(scanReportTexts(["prose with a stray { brace and no report"])).toEqual({ report: null, malformed: false })
  const corrupt = "```json\n{\"schema_version\": \"1.0\", \"truncated\n```"
  expect(scanReportTexts([corrupt])).toEqual({ report: null, malformed: true })
  const lastWins = scanReportTexts([`earlier superseded answer ${JSON.stringify(report({ status: "failed" }))}`, `final answer:\n\`\`\`json\n${JSON.stringify(report())}\n\`\`\``])
  expect(lastWins.report).toEqual(report())
})

// CD-0056 D7 as amended 2026-09-17: the adapter strips dispatch-owned fields
// from the worker-authored surface instead of refusing the report. Identity
// still reaches the canonical report exclusively from the packet, and the
// closed schema still refuses every other unknown field.
test("an echoed dispatch-owned field is stripped, and the schema stays closed", () => {
  const echoed = resolveWorkerReportFromText(JSON.stringify(report({
    attempt_id: "attempt-forged",
    lane_id: "lane-forged",
    lane_version: 99,
    lane_digest: "sha256:" + "0".repeat(64),
    work_id: "work-forged",
    step_id: "step-forged",
  })), packet())
  expect(echoed).toEqual({ report: canonicalReport() })
  const unknown = resolveWorkerReportFromText(JSON.stringify(report({ mood: "confident" })), packet())
  expect("detail" in unknown && unknown.detail).toContain("failed the closed agent-lane-report.v1 schema")
})
