import { afterEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureCoreBinary, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { dispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { manifestDigest } from "./generated-contracts"
import { configureHostLease } from "./host-lease"
import { configureConcordAdapter, consumeAddressedProjectHandoff, consumedProjectHandoff, resetConsumedProjectHandoffs, work_trace } from "./concord"

// The consume resolves the core binary path before the runner seam
// intercepts the verb, so the file binds a path once at module level.
configureCoreBinary("concord")

const context = () =>
  ({
    sessionID: "session-receive",
    messageID: "message-1",
    agent: "agent-receive",
    directory: "/worktree",
    worktree: "/worktree",
    abort: new AbortController().signal,
    metadata: () => {},
    ask: async () => {},
  }) as Parameters<typeof consumeAddressedProjectHandoff>[1]

// fakeHost wires the plugin's control-plane client so the session route
// answers the worktree the verified landing read back, and swaps the runner
// seam the owning invoke route uses. The plugin factory claims the host
// lease at load, so the file stamps the release identity and answers the
// claim leg the way an installed release would, or every operation would
// refuse closed before the transport runs (CD-0111 D2). The runner models
// the real two-leg transport: `concord project-resolve` for the ambient
// context, then `concord invoke` for the typed consume.
async function fakeHost(runner: DispatchRunner) {
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" },
    runner: {
      async run(argv: string[]) {
        if (argv[1] === "host-lease") {
          return { exitCode: 0, stdout: JSON.stringify({ pid: 4242, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest, directory: "/worktree", worktree: "/worktree" }), stderr: "" }
        }
        throw new Error("unexpected host-lease invocation: " + argv.join(" "))
      },
    } as never,
  })
  configureConcordAdapter({ runner })
  await ConcordAdapterPlugin({
    client: {
      _client: {
        post: async () => {
          throw new Error("unexpected POST")
        },
        get: async (request: { path?: { id?: string } }) => ({
          data: { id: request?.path?.id ?? "session-receive", directory: "/worktree" },
          response: new Response(null, { status: 200 }),
        }),
      },
    },
    serverUrl: new URL("http://127.0.0.1:4096"),
  } as never)
}

type CapturedInvoke = { call_envelope?: Record<string, unknown>; tool?: string; operation?: string; input?: Record<string, unknown>; projectResolve?: boolean }

// coreRunner answers the two transport legs. It records the invoke body so a
// test asserts the exact call envelope the owning route built, and answers
// with a core envelope that must survive the adapter's own closed-envelope
// response gate before the consume reads it.
function coreRunner(answer: Record<string, unknown>, captured: CapturedInvoke, failInvoke = false, rawStdout = "") {
  return {
    async run(argv: string[], input: string) {
      if (argv[1] === "project-resolve") {
        captured.projectResolve = true
        return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-receive", scope_version: "sv-1", main_worktree: false, product_ids: ["product-1"] }) + "\n", stderr: "" }
      }
      if (argv[1] === "invoke") {
        const parsed = JSON.parse(input) as CapturedInvoke
        captured.call_envelope = parsed.call_envelope
        captured.tool = parsed.tool
        captured.operation = parsed.operation
        captured.input = parsed.input
        if (failInvoke) return { exitCode: 1, stdout: "", stderr: "core unreachable" }
        return { exitCode: 0, stdout: rawStdout || JSON.stringify(answer) + "\n", stderr: "" }
      }
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    },
  } as never
}

// coreEnvelope builds a core answer that passes the adapter's closed-envelope
// contract check: the identity members, the pinned manifest digest, and the
// base members the generated envelope schema requires.
function coreEnvelope(tool: string, operation: string, outcome: string, extra: Record<string, unknown>): Record<string, unknown> {
  return {
    schema_version: "1.0",
    request_id: "core-answer-1",
    origin: "core",
    tool,
    operation,
    outcome,
    manifest_digest: manifestDigest,
    resolved_scope: { project_ids: ["project-receive"], work_ids: ["work-1"] },
    authority: "authoritative",
    freshness: null,
    source_version_watermark: [],
    ordering_keys: [],
    next_cursor: null,
    omissions: [],
    warnings: [],
    evidence_refs: [],
    replayed: false,
    ...extra,
  }
}

const okAnswer = () =>
  coreEnvelope("concord_work_transition", "project_handoff_consume", "ok", {
    changed_refs: [],
    next_valid_intents: [],
    result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", handoff_id: "work-1:project-handoff:project-source:project-receive:abcd1234abcd", already_consumed: false },
  })

const refusalAnswer = (kind: string, message: string, recovery: string) =>
  coreEnvelope("concord_work_transition", "project_handoff_consume", "error", { error: { kind, retry_safe: false, recovery_action: { kind: recovery }, effect_state: "none", message } })

afterEach(async () => {
  resetConsumedProjectHandoffs()
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  // The bare factory re-claims the lease against the repository placeholder,
  // which records the closed-transport fault; clear it so the shared test
  // process hands the next file an unfaulted lease module.
  await ConcordAdapterPlugin({})
  configureHostLease({ reset: true })
})

// The verified landing supplies the transport facts: the session directory
// the host readback proved and the ambient context the boot flow resolved.
const landingTransport = () => ({ sessionDirectory: "/worktree", ambient: { projectID: "project-receive", productIDs: ["product-1"], scopeVersion: "sv-1", mainWorktree: false } })

describe("consumeAddressedProjectHandoff", () => {
  test("binds the addressed handoff through the owning invoke route with the authenticated call envelope", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(okAnswer(), captured))
    const out = await consumeAddressedProjectHandoff("work-1", context(), landingTransport())
    expect(out.consumed).toBe(true)
    expect(out.handoffID).toBe("work-1:project-handoff:project-source:project-receive:abcd1234abcd")
    expect(consumedProjectHandoff("session-receive")?.handoffID).toBe("work-1:project-handoff:project-source:project-receive:abcd1234abcd")
    // Strict call-envelope boundary: the consume rode the context-resolved
    // transport, so the envelope names the authenticated identities the
    // verified landing supplied — never receiver identities this file could
    // have invented. The consume re-resolves nothing: the runner saw the
    // invoke leg alone.
    expect(captured.projectResolve).toBeUndefined()
    const envelope = captured.call_envelope ?? {}
    expect(captured.tool).toBe("concord_work_transition")
    expect(captured.operation).toBe("project_handoff_consume")
    expect(envelope.schema_version).toBe("1.0")
    expect(envelope.request_id).toBe("session-receive-message-1")
    expect(envelope.session_ref).toBe("session-receive")
    expect(envelope.agent_ref).toBe("agent-receive")
    expect(envelope.client_ref).toBe("opencode")
    expect(envelope.directory).toBe("/worktree")
    expect(envelope.worktree).toBe("/worktree")
    expect(envelope.ambient_project_id).toBe("project-receive")
    expect(envelope.selected_product_id).toBe("product-1")
    expect(envelope.scope_version).toBe("sv-1")
    expect(envelope.manifest_digest).toBe(manifestDigest)
    expect(captured.input).toEqual({ work_id: "work-1", idempotency_key: "handoff-consume-work-1" })
  })

  test("a typed stale-contract refusal never binds and carries the typed refusal message", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(refusalAnswer("invalid_input", "the handoff was recorded under contract version 1, but the active contract is version 2", "reread_entities"), captured))
    const out = await consumeAddressedProjectHandoff("work-1", context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("invalid_input")
    expect(out.message).toContain("active contract is version 2")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("the ordinary boot state — no handoff addresses this Project — stays quiet", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(refusalAnswer("unknown_scope", "no recorded project handoff addresses this Project", "none"), captured))
    const out = await consumeAddressedProjectHandoff("work-1", context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message ?? "").toBe("")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("a runner failure at the invoke leg surfaces as a warning, never a bypass", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(okAnswer(), captured, true))
    const out = await consumeAddressedProjectHandoff("work-1", context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("operation_conflict")
    expect(out.message).toContain("core unreachable")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("an unreadable core answer fails the closed-envelope gate and binds nothing", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(okAnswer(), captured, false, "concord core answer lost in transit\n"))
    const out = await consumeAddressedProjectHandoff("work-1", context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("malformed_response")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("a missing work or session identity skips the consume entirely", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(okAnswer(), captured))
    const out = await consumeAddressedProjectHandoff("", context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("consume skipped")
    expect(captured.call_envelope).toBeUndefined()
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })
})

// The Project-session retirement read composes the core's derived state with
// this session's own adapter dispatch-window quiescence (CD-0182 amendment).
// The core cannot see the adapter's windows, so a core answer of
// ready_to_close_or_replace must downgrade to pending while this session
// holds an open, in-flight, settling, or refused window — and a clean session
// passes the core answer through unchanged. Every state below is driven on
// the real shared window instance through the real tool path.
describe("project_retirement composes adapter dispatch-window quiescence", () => {
  const retirePacket: AgentLanePacket = {
    schema_version: "1.0",
    attempt_id: "attempt-1",
    lane_id: "implement",
    lane_version: 1,
    lane_digest: "sha256:" + "a".repeat(64),
    work_id: "work-1",
    step_id: "execution",
    inputs: { task: "do the bounded thing", context: "", constraints: [] },
  }
  const windows = () => dispatchWindows()
  const hostCall = (operation: string, input: Record<string, unknown>) => ({ request: { operation, input } })
  const rawHostResult = async (result: Promise<string | { output: string }>) => {
    const value = await result
    if (typeof value === "string") throw new Error("adapter returned a string ToolResult")
    const newline = value.output.indexOf("\n")
    return JSON.parse(newline === -1 ? value.output : value.output.slice(0, newline))
  }

  const readyAnswer = () =>
    coreEnvelope("concord_work_trace", "project_retirement", "ok", {
      query_id: "CD-0182.R1",
      result: { work_id: "work-1", project_id: "project-receive", session_ref: "session-receive", state: "ready_to_close_or_replace", recorded_handoff: true, artifacts_preserved: true, workers_stopped: true, vacate_landed: true, blockers: [] },
    })

  async function readRetirement() {
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(readyAnswer(), captured))
    return rawHostResult(work_trace.execute(hostCall("project_retirement", { work_id: "work-1" }), context()) as Promise<{ output: string }>)
  }

  // bind moves an open window into the in-flight state the way the host's
  // Task hook does; claim and refuse walk the settle path. The window pins a
  // real resolvable directory, so the authorization identity check passes.
  async function openThenBind() {
    windows().open("session-receive", retirePacket, "", process.cwd())
    await windows().bind(TASK_TOOL_ID, "session-receive", {}, "call-1", async () => process.cwd())
  }

  afterEach(() => {
    windows().close("session-receive")
    windows().releaseRetained("session-receive", "attempt-1", "implement")
    windows().finishSettlement("session-receive", "call-1")
  })

  test("a clean session passes the core's derived ready state through", async () => {
    const result = await readRetirement()
    expect(result.outcome).toBe("ok")
    expect(result.result.state).toBe("ready_to_close_or_replace")
    expect(result.result.blockers).toEqual([])
  })

  test("an open dispatch window downgrades readiness to pending", async () => {
    windows().open("session-receive", retirePacket, "", process.cwd())
    const result = await readRetirement()
    expect(result.result.state).toBe("pending")
    expect(result.result.blockers.join(" ")).toContain("open dispatch window")
    expect(result.result.blockers.join(" ")).toContain("implement lane")
  })

  test("an in-flight worker attempt downgrades readiness to pending", async () => {
    await openThenBind()
    const result = await readRetirement()
    expect(result.result.state).toBe("pending")
    expect(result.result.blockers.join(" ")).toContain("in-flight worker attempt")
  })

  test("a settling attempt downgrades readiness to pending", async () => {
    await openThenBind()
    expect(windows().claimSettlement("session-receive")).not.toBeNull()
    const result = await readRetirement()
    expect(result.result.state).toBe("pending")
    expect(result.result.blockers.join(" ")).toContain("still settling")
  })

  test("a refused settle downgrades readiness to pending", async () => {
    await openThenBind()
    windows().claimSettlement("session-receive")
    windows().refuseSettlement("session-receive")
    const result = await readRetirement()
    expect(result.result.state).toBe("pending")
    expect(result.result.blockers.join(" ")).toContain("worker_abandon release")
  })
})
