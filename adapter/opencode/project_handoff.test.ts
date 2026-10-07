import { afterEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureCoreBinary, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { dispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { manifestDigest } from "./generated-contracts"
import { configureHostLease } from "./host-lease"
import { configureConcordAdapter, consumeAddressedProjectHandoff, consumedProjectHandoff, projectHandoffConsumeKey, resetConsumedProjectHandoffs, work_trace } from "./concord"

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
  }) as Parameters<typeof consumeAddressedProjectHandoff>[2]

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

// The bounded job the work-resume rendered for this Project: the consume
// carries its durable id, never an adapter-invented identity.
const renderedHandoffID = "work-1:project-handoff:project-source:project-receive:abcd1234abcd"

// The identity members the generated consume payload accepts: the owning
// agent-tool-surface-payloads.schema.json $defs/id bound both ids share.
const MAX_ID = 128
const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/
const maxID = (head: string) => (head + "w".repeat(MAX_ID)).slice(0, MAX_ID)

describe("projectHandoffConsumeKey", () => {
  // The negative control: the pre-fix construction concatenated the full
  // work_id, so the accepted 123-character work_id of the review
  // reproduction produced a 156-character key the core refused with
  // "maxLength at $.idempotency_key", and the maximum accepted pair produced
  // 161. Both assertions fail again if the key ever grows unbounded.
  test("fits the 128-character contract bound at maximum accepted identity lengths", () => {
    const workID = maxID("work-")
    const handoffID = maxID("project-handoff-")
    expect(workID).toHaveLength(128)
    expect(ID_PATTERN.test(workID)).toBe(true)
    const key = projectHandoffConsumeKey(workID, handoffID)
    expect(key.length).toBeLessThanOrEqual(128)
    expect(ID_PATTERN.test(key)).toBe(true)
    expect(ID_PATTERN.test(projectHandoffConsumeKey(workID, maxID("work-")))).toBe(true)
  })

  test("the reviewed 123-character work_id no longer overflows the bound", () => {
    const workID = "work-" + "w".repeat(118)
    const key = projectHandoffConsumeKey(workID, renderedHandoffID)
    expect(key.length).toBeLessThanOrEqual(128)
    expect(`handoff-consume-${workID}-${"b".repeat(16)}`.length).toBeGreaterThan(128)
  })

  test("equal identities replay one key; another work or handoff never collides", () => {
    const workID = maxID("work-")
    expect(projectHandoffConsumeKey(workID, renderedHandoffID)).toBe(projectHandoffConsumeKey(workID, renderedHandoffID))
    expect(projectHandoffConsumeKey(workID, renderedHandoffID)).not.toBe(projectHandoffConsumeKey(workID, "project-handoff-" + "f".repeat(32)))
    expect(projectHandoffConsumeKey(workID, renderedHandoffID)).not.toBe(projectHandoffConsumeKey("work-other", renderedHandoffID))
  })
})

describe("consumeAddressedProjectHandoff", () => {
  // The consume's success path is proven on the real boundary in
  // project_handoff_end_to_end.test.ts: a real core binary against a real
  // store, the real boot route's bounded job, and the real claim-landing
  // placement. The tests here pin the adapter's own refusal handling on
  // typed core refusals and transport faults.

  test("a typed stale-contract refusal never binds and carries the typed refusal message", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(refusalAnswer("invalid_input", "the handoff was recorded under contract version 1, but the active contract is version 2", "reread_entities"), captured))
    const out = await consumeAddressedProjectHandoff("work-1", renderedHandoffID, context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("invalid_input")
    expect(out.message).toContain("active contract is version 2")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("an unknown-scope answer for a rendered handoff fails closed instead of reporting quiet", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(refusalAnswer("unknown_scope", "no recorded project handoff addresses this Project", "none"), captured))
    const out = await consumeAddressedProjectHandoff("work-1", renderedHandoffID, context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("unknown_scope")
    expect(out.message).toContain("no recorded project handoff addresses this Project")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("a runner failure at the invoke leg surfaces as a refusal, never a bypass", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(refusalAnswer("operation_conflict", "core unreachable", "retry_same_request"), captured, true))
    const out = await consumeAddressedProjectHandoff("work-1", renderedHandoffID, context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("operation_conflict")
    expect(out.message).toContain("core unreachable")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("an unreadable core answer fails the closed-envelope gate and binds nothing", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(refusalAnswer("operation_conflict", "answer lost", "retry_same_request"), captured, false, "concord core answer lost in transit\n"))
    const out = await consumeAddressedProjectHandoff("work-1", renderedHandoffID, context(), landingTransport())
    expect(out.consumed).toBe(false)
    expect(out.message).toContain("malformed_response")
    expect(consumedProjectHandoff("session-receive")).toBeNull()
  })

  test("a missing work, handoff, or session identity skips the consume entirely", async () => {
    resetConsumedProjectHandoffs()
    const captured: CapturedInvoke = {}
    await fakeHost(coreRunner(refusalAnswer("operation_conflict", "never reached", "retry_same_request"), captured))
    const out = await consumeAddressedProjectHandoff("", renderedHandoffID, context(), landingTransport())
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
    inputs: { task: "do the bounded thing", context: "", constraints: [], binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "bounded_findings" } },
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
    await windows().bind(TASK_TOOL_ID, "session-receive", {}, "call-1", async () => process.cwd(), process.cwd())
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
