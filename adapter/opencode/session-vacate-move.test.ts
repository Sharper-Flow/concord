import { afterAll, afterEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { moveSessionToRegisteredMainCheckout, work_transition, configureConcordAdapter } from "./concord"
import { armedClaimedWorktree, armClaimedWorktree, clearClaimedWorktree, pendingVacateDestination, recordPendingVacateDestination, resetClaimedWorktrees } from "./claimed-worktree"
import { dispatchRequiresNextTurn, resetTurnMoveBoundaries } from "./turn-move-boundary"
import { manifestDigest } from "./generated-contracts"
import { TURN_MOVE_BOUNDARY_NOTICE } from "./move-notice"
import { HostControlPlane } from "./move-session"
import { configureCoreBinary } from "./dispatch"

// The landing record resolves the core binary path before the runner seam
// intercepts the verb, so the file binds a path once at module level.
configureCoreBinary("concord")

const context = () => ({
  sessionID: "session-1",
  messageID: "message-1",
  abort: new AbortController().signal,
  directory: "/worktree",
}) as Parameters<typeof moveSessionToRegisteredMainCheckout>[1]

const args = (input: Record<string, unknown>) => ({ operation: "session_vacate", input }) as Parameters<typeof moveSessionToRegisteredMainCheckout>[0]
const okEnvelope = () => ({
  schema_version: "1.0",
  outcome: "ok",
  result: { work_id: "work-1", destination_directory: "/main" },
}) as Parameters<typeof moveSessionToRegisteredMainCheckout>[2]

// The verified landing records itself through the adapter-only vacate-landing
// verb; the capture records the calls instead of spawning a binary.
let landingCalls: string[] = []

function landingRunner() {
  return {
    async run(argv: string[], input: string) {
      if (argv[1] === "vacate-landing") {
        landingCalls.push((JSON.parse(input) as { landed_directory: string }).landed_directory)
        return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
      }
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    },
  } as never
}

async function fakeHost(post: (body: any) => { status: number; body: any }, get: () => { status: number; body: any }, runner: any = landingRunner()) {
  configureConcordAdapter({ runner })
  await ConcordAdapterPlugin({
    client: {
      _client: {
        post: async (request: any) => {
          const result = post(request.body)
          return { data: result.body, response: new Response(null, { status: result.status }) }
        },
        get: async (request: any) => {
          const result = get()
          // The real session route answers the asked identity, so the record
          // carries the id the caller's scope walk reads back.
          return { data: { id: request?.path?.id ?? "session-1", ...(result.body as Record<string, unknown>) }, response: new Response(null, { status: result.status }) }
        },
      },
    } as never,
    serverUrl: new URL("http://127.0.0.1:4096"),
  })
}

afterEach(async () => {
  landingCalls = []
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  configureConcordAdapter({ reset: true })
  await ConcordAdapterPlugin({})
})

describe("session_vacate moves only to the core-derived checkout", () => {
  test("moves, verifies the registered destination, and records the verified landing", async () => {
    // The fake host models the real one: the session sits in the source
    // worktree until a move POST relocates it, then the readback names the
    // destination.
    let sitting = "/worktree"
    let moved = ""
    await fakeHost((body) => {
      moved = body.destination.directory
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }))
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-1" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(moved).toBe("/main")
    expect(landingCalls).toEqual(["/main"])
  })

  test("refuses an agent-named destination before the move", async () => {
    let calls = 0
    await fakeHost(() => {
      calls++
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: "/main" } }))
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-2", destination: "/other" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    expect(calls).toBe(0)
    if (envelope.outcome === "error") expect((envelope.error as { adapter_reason?: string }).adapter_reason).toBe("agent_named_destination")
  })

  test("refuses a landing mismatch and records no vacate landing", async () => {
    await fakeHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/other" } }))
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-3" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { adapter_reason?: string; effect_state?: string; recovery_action?: { kind?: string } }
      expect(error.adapter_reason).toBe("vacate_destination_mismatch")
      // The relocation request committed, so the refusal reports a possible
      // effect and the occupancy stands. The adapter remembers the committed
      // destination, so the retry is reachable from wherever the host
      // session sits.
      expect(error.effect_state).toBe("possible")
      expect(error.recovery_action?.kind).toBe("retry_same_request")
    }
    // The refused move leaves the occupancy standing: no landing runs, and
    // the committed destination stays remembered for the retry.
    expect(landingCalls).toEqual([])
    expect(pendingVacateDestination("session-1")).toBe("/main")
  })

  // An unreadable successful core answer leaves the committed relocation
  // request standing (CD-0190 D3): the refusal classifies with the
  // state-driven replay as the recovery — session_vacate names no work id,
  // so no generic reconciliation can drive — reports the possible effect,
  // and remembers nothing the core did not return.
  test("classifies an unreadable ok answer with the state-driven replay recovery", async () => {
    await fakeHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/worktree" } }), {
      async run(argv: string[], input: string) {
        if (argv[1] === "project-resolve") {
          return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", scope_version: "sv-1", main_worktree: false, product_ids: ["product-1"] }) + "\n", stderr: "" }
        }
        if (argv[1] === "invoke") {
          const parsed = JSON.parse(input) as { operation?: string }
          if (parsed?.operation === "session_vacate") return { exitCode: 0, stdout: "concord core answer lost in transit\n", stderr: "" }
        }
        throw new Error("unexpected CLI invocation: " + argv.join(" "))
      },
    } as never)
    configureHostLease({ reset: true })
    const unreadable = await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "vacate-unreadable" } } } as never, context())
    const envelope = JSON.parse((typeof unreadable === "string" ? unreadable : unreadable.output).split("\n")[0])
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { kind?: string; effect_state?: string; recovery_action?: { kind?: string }; message?: string }
      expect(error.kind).toBe("malformed_response")
      expect(error.effect_state).toBe("possible")
      expect(error.recovery_action?.kind).toBe("retry_same_request")
      expect(error.message).toContain("replay session_vacate")
    }
    expect(pendingVacateDestination("session-1")).toBeNull()
    expect(landingCalls).toEqual([])
  })

  // After an adapter restart the adapter holds no remembered destination, so
  // the retry's core call resolves its Project from the landed directory and
  // refuses before the pending-request replay can run. That refusal must not
  // promise the unreachable retry: it reports contact_operator (CD-0190 D3).
  test("reports contact_operator when a restart left no remembered destination", async () => {
    await fakeHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/other" } }), {
      async run(argv: string[]) {
        if (argv[1] === "project-resolve") return { exitCode: 1, stdout: "", stderr: "concord project-resolve: git_unreachable: signed directory/worktree is not a git repository" }
        throw new Error("unexpected CLI invocation: " + argv.join(" "))
      },
    } as never)
    configureHostLease({ reset: true })
    const restarted = await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "vacate-restart" } } } as never, context())
    const envelope = JSON.parse((typeof restarted === "string" ? restarted : restarted.output).split("\n")[0])
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { kind?: string; adapter_reason?: string; effect_state?: string; recovery_action?: { kind?: string } }
      expect(error.kind).toBe("transport_failure")
      expect(error.adapter_reason).toBe("io_failure")
      expect(error.effect_state).toBe("none")
      expect(error.recovery_action?.kind).toBe("contact_operator")
    }
    expect(landingCalls).toEqual([])
  })

  // A vacate-landing call that fails without a structured core refusal may
  // have committed before its output failed, so the typed refusal reports
  // the possible effect and never asserts the landing is not recorded: the
  // replay confirms or completes the record.
  test("reports a possible landing on a vacate-landing output failure", async () => {
    await fakeHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/main" } }), {
      async run() {
        return { exitCode: 1, stdout: "", stderr: "concord vacate-landing: write failed after commit: broken pipe" }
      },
    } as never)
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-refused" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { adapter_reason?: string; effect_state?: string; message: string }
      expect(error.adapter_reason).toBe("vacate_landing_refused")
      expect(error.effect_state).toBe("possible")
      expect(error.message).toContain("may be recorded")
      expect(error.message).not.toContain("is not recorded")
      expect(error.message).toContain("replay session_vacate from the verified destination")
    }
  })

  // A replay that resolves a pending landing arrives with the session already
  // at the registered main checkout, so the move is skipped and the verified
  // landing records through the adapter-only verb.
  test("replay from the verified destination skips the move and records the landing", async () => {
    await fakeHost(() => {
      throw new Error("no move may run when the session already sits at the destination")
    }, () => ({ status: 200, body: { directory: "/main" } }))
    const replayContext = { ...context(), directory: "/main" } as Parameters<typeof moveSessionToRegisteredMainCheckout>[1]
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-replay" }), replayContext, okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(landingCalls).toEqual(["/main"])
  })

  // A stale tool context names the registered main checkout while the host
  // session still sits in the source worktree. The host readback decides the
  // move, so the first call moves from the source worktree, records the
  // verified landing, and releases the row; the retry records the landing
  // again and keeps the row released. Under the previous tool-context gate
  // the move was skipped, the readback mismatched, and no retry could ever
  // release the standing row.
  test("moves on the host readback when a stale tool context names the main checkout", async () => {
    let sitting = "/worktree"
    const moves: string[] = []
    await fakeHost((body) => {
      moves.push(body.destination.directory)
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }))
    const stale = { ...context(), directory: "/main" } as Parameters<typeof moveSessionToRegisteredMainCheckout>[1]
    try {
      armClaimedWorktree("session-1", "/worktree")
      const first = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-stale-1" }), stale, okEnvelope())
      expect(first.outcome).toBe("ok")
      expect(moves).toEqual(["/main"])
      expect(landingCalls).toEqual(["/main"])
      expect(armedClaimedWorktree("session-1")).toBeNull()
      const retry = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-stale-2" }), stale, okEnvelope())
      expect(retry.outcome).toBe("ok")
      expect(moves).toEqual(["/main"])
      expect(landingCalls).toEqual(["/main", "/main"])
      expect(armedClaimedWorktree("session-1")).toBeNull()
    } finally {
      clearClaimedWorktree("session-1")
    }
  })

  // The full retry route: the tool context names the remembered destination
  // while the host readback names a directory outside every registered
  // Project. The pre-move decides from the host readback, so the retry moves
  // the host session to the remembered destination, the core call resolves
  // its Project there, the replay appends nothing, and the verified landing
  // releases the row. Under the previous tool-context gate the move was
  // skipped, Project resolution refused before the replay could run with a
  // no-effect classification, and no retry could ever reach the replay
  // (CD-0190 D3).
  test("reaches the replay when a stale tool context names the destination and the readback sits outside every Project", async () => {
    let sitting = "/outside"
    const moves: string[] = []
    await fakeHost((body) => {
      moves.push(body.destination.directory)
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }), {
      async run(argv: string[], input: string) {
        if (argv[1] === "project-resolve") {
          const asked = JSON.parse(input) as { directory?: string }
          if (asked.directory === "/main") {
            return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", scope_version: "sv-1", main_worktree: true, product_ids: ["product-1"] }) + "\n", stderr: "" }
          }
          return { exitCode: 1, stdout: "", stderr: "concord project-resolve: git_unreachable: signed directory/worktree is not a git repository" }
        }
        if (argv[1] === "invoke") {
          const parsed = JSON.parse(input) as { operation?: string }
          if (parsed?.operation === "session_vacate") {
            return {
              exitCode: 0,
              stdout: JSON.stringify({
                schema_version: "1.0",
                request_id: "session-1-message-1",
                origin: "core",
                tool: "concord_work_transition",
                operation: "session_vacate",
                outcome: "ok",
                resolved_scope: null,
                authority: "authoritative",
                freshness: null,
                manifest_digest: manifestDigest,
                source_version_watermark: [],
                ordering_keys: [],
                next_cursor: null,
                omissions: [],
                warnings: [],
                evidence_refs: [],
                replayed: false,
                result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/outside", destination_directory: "/main" },
                changed_refs: [],
                next_valid_intents: [],
              }) + "\n",
              stderr: "",
            }
          }
        }
        if (argv[1] === "vacate-landing") {
          landingCalls.push((JSON.parse(input) as { landed_directory: string }).landed_directory)
          return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
        }
        throw new Error("unexpected CLI invocation: " + argv.join(" "))
      },
    } as never)
    configureHostLease({ reset: true })
    recordPendingVacateDestination("session-1", "/main")
    armClaimedWorktree("session-1", "/worktree")
    try {
      const stale = { ...context(), directory: "/main" } as Parameters<typeof work_transition.execute>[1]
      const result = await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "vacate-outside-retry" } } } as never, stale)
      const output = typeof result === "string" ? result : result.output
      const envelope = JSON.parse(output.split("\n")[0])
      expect(envelope.outcome, output).toBe("ok")
      expect(moves).toEqual(["/main"])
      expect(landingCalls).toEqual(["/main"])
      expect(pendingVacateDestination("session-1")).toBeNull()
      expect(armedClaimedWorktree("session-1")).toBeNull()
      expect(dispatchRequiresNextTurn("session-1")).toBe(true)
      // The pre-move armed the boundary, so the notice on this result names it.
      expect(output.split("\n")[1]).toContain("Concord moved this session to /main")
      expect(output.split("\n")[1]).toContain(TURN_MOVE_BOUNDARY_NOTICE)
    } finally {
      clearClaimedWorktree("session-1")
    }
  })
  // request's state unreadable once the core process started: a timeout
  // kills a running core, so the core may have committed before it died.
  // The refusal reports the possible effect with the state-driven replay
  // recovery, not a proved absence (CD-0190 D3). Every other operation
  // keeps its no-effect runner classification.
  test("classifies a thrown runner timeout on session_vacate as possible with the replay recovery", async () => {
    await fakeHost(() => {
      throw new Error("no host call may run before the core answers")
    }, () => ({ status: 200, body: { directory: "/worktree" } }), {
      async run(argv: string[], input: string) {
        if (argv[1] === "project-resolve") {
          return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", scope_version: "sv-1", main_worktree: false, product_ids: ["product-1"] }) + "\n", stderr: "" }
        }
        if (argv[1] === "invoke") throw Object.assign(new Error("core invocation timed out"), { name: "TimeoutError" })
        throw new Error("unexpected CLI invocation: " + argv.join(" "))
      },
    } as never)
    configureHostLease({ reset: true })
    const result = await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "vacate-timeout" } } } as never, context())
    const envelope = JSON.parse((typeof result === "string" ? result : result.output).split("\n")[0])
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { kind?: string; effect_state?: string; recovery_action?: { kind?: string }; message?: string }
      expect(error.kind).toBe("operation_conflict")
      expect(error.effect_state).toBe("possible")
      expect(error.recovery_action?.kind).toBe("retry_same_request")
      expect(error.message).toContain("replay session_vacate")
    }
    expect(pendingVacateDestination("session-1")).toBeNull()
    expect(landingCalls).toEqual([])
  })

  // A manifest-skew refusal on session_vacate advertises the recovery that
  // works: session_vacate names no work id, so reconcile_operation cannot
  // drive a reconciliation, and the committed relocation request's recovery
  // is the state-driven replay. The refusal keeps the skew detail and both
  // digests, reports the possible effect, and retries the same request. When
  // the skew clears, the replay records the verified landing and releases
  // the row (CD-0190 D3).
  test("routes a manifest-skew session_vacate refusal to the replay recovery, and the retry lands after the skew clears", async () => {
    let skewed = true
    let sitting = "/worktree"
    const moves: string[] = []
    const skewDigest = "sha256:" + "b".repeat(64)
    const sessionVacateAnswer = (digest: string) => ({
      exitCode: 0,
      stdout: JSON.stringify({
        schema_version: "1.0",
        request_id: "session-1-message-1",
        origin: "core",
        tool: "concord_work_transition",
        operation: "session_vacate",
        outcome: "ok",
        resolved_scope: null,
        authority: "authoritative",
        freshness: null,
        manifest_digest: digest,
        source_version_watermark: [],
        ordering_keys: [],
        next_cursor: null,
        omissions: [],
        warnings: [],
        evidence_refs: [],
        replayed: false,
        result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" },
        changed_refs: [],
        next_valid_intents: [],
      }) + "\n",
      stderr: "",
    })
    await fakeHost((body) => {
      moves.push(body.destination.directory)
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }), {
      async run(argv: string[], input: string) {
        if (argv[1] === "project-resolve") {
          return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", scope_version: "sv-1", main_worktree: false, product_ids: ["product-1"] }) + "\n", stderr: "" }
        }
        if (argv[1] === "invoke") {
          const parsed = JSON.parse(input) as { operation?: string }
          if (parsed?.operation === "session_vacate") return sessionVacateAnswer(skewed ? skewDigest : manifestDigest)
        }
        if (argv[1] === "vacate-landing") {
          landingCalls.push((JSON.parse(input) as { landed_directory: string }).landed_directory)
          return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
        }
        throw new Error("unexpected CLI invocation: " + argv.join(" "))
      },
    } as never)
    configureHostLease({ reset: true })
    const skewedResult = await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "vacate-skew-1" } } } as never, context())
    const skewedOutput = typeof skewedResult === "string" ? skewedResult : skewedResult.output
    const skewedEnvelope = JSON.parse(skewedOutput.split("\n")[0])
    expect(skewedEnvelope.outcome, skewedOutput).toBe("error")
    if (skewedEnvelope.outcome === "error") {
      const error = skewedEnvelope.error as { kind?: string; effect_state?: string; recovery_action?: { kind?: string }; message?: string }
      expect(error.kind).toBe("operation_conflict")
      expect(error.effect_state).toBe("possible")
      expect(error.recovery_action?.kind).toBe("retry_same_request")
      expect(error.message).toContain("does not match this adapter's")
      expect(error.message).toContain("both digests")
      expect(error.message).toContain("replay session_vacate")
    }
    // The skew refusal precedes the move: no host move ran, no landing
    // recorded, and the committed request stands possible.
    expect(moves).toEqual([])
    expect(landingCalls).toEqual([])

    // The skew clears; the replay retry records the verified landing and
    // releases the row, so nothing strands the session.
    skewed = false
    const retry = await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "vacate-skew-2" } } } as never, context())
    const retryOutput = typeof retry === "string" ? retry : retry.output
    const retryEnvelope = JSON.parse(retryOutput.split("\n")[0])
    expect(retryEnvelope.outcome, retryOutput).toBe("ok")
    expect(moves).toEqual(["/main"])
    expect(landingCalls).toEqual(["/main"])
    expect(pendingVacateDestination("session-1")).toBeNull()
    expect(armedClaimedWorktree("session-1")).toBeNull()
  })

  // The confirmed vacate landing drops the armed claim, so a later dispatch
  // from the main checkout runs the no-claim path exactly as before.
  test("clears the armed claimed worktree once the vacate landing is confirmed", async () => {
    let sitting = "/worktree"
    let moved = ""
    await fakeHost((body) => {
      moved = body.destination.directory
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }))
    try {
      armClaimedWorktree("session-1", "/worktree")
      const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-armed" }), context(), okEnvelope())
      expect(envelope.outcome).toBe("ok")
      expect(moved).toBe("/main")
      expect(landingCalls).toEqual(["/main"])
      expect(armedClaimedWorktree("session-1")).toBeNull()
    } finally {
      clearClaimedWorktree("session-1")
    }
  })
})

test("liveSessionDirectories is removed from the host control plane (CD-0178 D3)", async () => {
  const controlPlane = new HostControlPlane()
  expect((controlPlane as unknown as { liveSessionDirectories?: unknown }).liveSessionDirectories).toBeUndefined()
})

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file leaves the lease state clean.
afterAll(() => configureHostLease({ reset: true }))
