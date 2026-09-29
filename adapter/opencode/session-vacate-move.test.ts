import { afterAll, afterEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { moveSessionToRegisteredMainCheckout, configureConcordAdapter } from "./concord"
import { armedClaimedWorktree, armClaimedWorktree, clearClaimedWorktree, resetClaimedWorktrees } from "./claimed-worktree"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"
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
        get: async () => {
          const result = get()
          return { data: result.body, response: new Response(null, { status: result.status }) }
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
    let moved = ""
    await fakeHost((body) => {
      moved = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: "/main" } }))
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
      const error = envelope.error as { adapter_reason?: string; effect_state?: string }
      expect(error.adapter_reason).toBe("vacate_destination_mismatch")
      // The relocation request committed, so the refusal reports a possible
      // effect and the occupancy stands.
      expect(error.effect_state).toBe("possible")
    }
    // The refused move leaves the occupancy standing: no landing runs.
    expect(landingCalls).toEqual([])
  })

  // A landing the core refuses records nothing, so the source occupancy
  // stands and the typed refusal names the replay route.
  test("refuses when the vacate landing does not record", async () => {
    await fakeHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/main" } }), {
      async run() {
        return { exitCode: 1, stdout: "", stderr: "concord vacate-landing: projection_not_found: the committed session vacate names /main as its registered main checkout, not /main" }
      },
    } as never)
    const envelope = await moveSessionToRegisteredMainCheckout(args({ idempotency_key: "vacate-refused" }), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { adapter_reason?: string; effect_state?: string; message: string }
      expect(error.adapter_reason).toBe("vacate_landing_refused")
      expect(error.effect_state).toBe("possible")
      expect(error.message).toContain("the source occupancy stands")
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

  // The confirmed vacate landing drops the armed claim, so a later dispatch
  // from the main checkout runs the no-claim path exactly as before.
  test("clears the armed claimed worktree once the vacate landing is confirmed", async () => {
    let moved = ""
    await fakeHost((body) => {
      moved = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: "/main" } }))
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
