// The move notice is the adapter's statement to the agent whose session just
// moved. These tests hold every successful move route to it: the notice rides
// the tool result, names the new absolute path, points reads, edits, and the
// shell working directory under it, and marks the stale <env> working
// directory and pre-move checkout. A refused move records no notice.
import { afterEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { configureCoreBinary } from "./dispatch"
import { manifestDigest } from "./generated-contracts"
import { configureConcordAdapter, moveSessionToClaimedWorktree, moveSessionToRegisteredMainCheckout, takeWorkNotices, vacateTerminalWorktree, work_relate, work_transition } from "./concord"
import { resetClaimedWorktrees } from "./claimed-worktree"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"
import { recordMoveNotice, resetMoveNotices, takeMoveNotice } from "./move-notice"

configureCoreBinary("concord")

const claimContext = () =>
  ({ sessionID: "session-1", messageID: "message-1", abort: new AbortController().signal, directory: "/old" }) as Parameters<typeof moveSessionToClaimedWorktree>[1]

const vacateContext = () =>
  ({ sessionID: "session-1", messageID: "message-1", abort: new AbortController().signal, directory: "/worktree" }) as Parameters<typeof moveSessionToRegisteredMainCheckout>[1]

const claimArgs = () => ({ operation: "worktree_claim", input: { work_id: "work-1" } }) as Parameters<typeof moveSessionToClaimedWorktree>[0]

const vacateArgs = (input: Record<string, unknown>) => ({ operation: "session_vacate", input }) as Parameters<typeof moveSessionToRegisteredMainCheckout>[0]

const claimOkEnvelope = () => ({ schema_version: "1.0", outcome: "ok", result: { path: "/claimed" } }) as Parameters<typeof moveSessionToClaimedWorktree>[2]

const vacateOkEnvelope = () => ({
  schema_version: "1.0",
  outcome: "ok",
  result: { work_id: "work-1", destination_directory: "/main" },
}) as Parameters<typeof moveSessionToRegisteredMainCheckout>[2]

const lifecycleArgs = (target: string) => ({ operation: "lifecycle", input: { work_id: "work-1", target, expected_version: 4, idempotency_key: "k-1" } })

const supersedeToolArgs = () => ({ request: { operation: "supersede", input: { predecessor_id: "work-1", successor_id: "work-2", predecessor_expected_version: 4, successor_expected_version: 1, reason: "replaced", idempotency_key: "k-2" } } })

const terminalOkEnvelope = (withTarget = true) => ({
  schema_version: "1.0",
  outcome: "ok",
  result: {
    changed_refs: [],
    next_valid_intents: [],
    ...(withTarget ? { vacate_target: { work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } } : {}),
  },
})

// coreEnvelope mirrors the core's TS7 envelope contract the adapter
// validates every response against. A mutation response carries the
// changed_refs and next_valid_intents arrays at the envelope level.
const coreEnvelope = (tool: string, operation: string, fields: Record<string, unknown>) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core",
  tool, operation, outcome: "ok", resolved_scope: null, authority: "authoritative",
  freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [],
  warnings: [], evidence_refs: [], replayed: false, changed_refs: [], next_valid_intents: [], ...fields,
})

function expectMoveNotice(notice: string | null, path: string): void {
  expect(notice).not.toBeNull()
  expect(notice).toContain(`Concord moved this session to ${path}`)
  expect(notice).toContain(`Use paths under ${path} for reads, edits, and the shell working directory`)
  expect(notice).toContain("The <env> working directory and the pre-move checkout are stale until the next turn")
}

function leaseRunner() {
  return { async run() { return { exitCode: 0, stdout: JSON.stringify({ pid: 4242, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest }), stderr: "" } } } as never
}

// The direct-route fake host: the plugin factory claims the host lease at
// init, the move POST relocates the session, and the readback returns
// whatever the test's sitting closure holds — the way the real control plane
// answers a move.
async function bindHost(post: (body: any) => { status: number; body: any }, get: () => { status: number; body: any }, runner: unknown = { async run() { throw new Error("unexpected CLI invocation") } }): Promise<void> {
  configureHostLease({ release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" }, runner: leaseRunner() })
  configureConcordAdapter({ runner: runner as never })
  await ConcordAdapterPlugin({
    client: {
      _client: {
        post: async (request: any) => {
          const result = post(request.body)
          return { data: result.body, response: new Response(null, { status: result.status }) }
        },
        get: async (request: any) => {
          const result = get()
          return { data: { id: request?.path?.id ?? "session-1", ...(result.body as Record<string, unknown>) }, response: new Response(null, { status: result.status }) }
        },
      },
    } as never,
    serverUrl: new URL("http://127.0.0.1:4096"),
  })
}

// The landing verbs run as child processes; the capture answers them the way
// the core does.
function landingOnlyRunner() {
  return { async run(_argv: string[], input: string) {
    return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
  } }
}

// The invoke-path runner answers the project-resolve pre-step, the invoke
// operations the test declares, and the landing verbs.
function invokeRunner(handlers: { invoke?: (operation: string) => { exitCode: number; stdout: string; stderr: string } | undefined }) {
  return { async run(argv: string[], input: string) {
    if (argv[1] === "project-resolve") {
      return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", scope_version: "scope-1", main_worktree: false, product_ids: ["product-1"] }) + "\n", stderr: "" }
    }
    if (argv[1] === "invoke") {
      const handled = handlers.invoke((JSON.parse(input) as { operation?: string }).operation ?? "")
      if (handled) return handled
    }
    if (argv[1] === "claim-landing" || argv[1] === "vacate-landing") {
      return { exitCode: 0, stdout: JSON.stringify({ work_id: (JSON.parse(input) as { work_id: string }).work_id, already_recorded: false }) + "\n", stderr: "" }
    }
    throw new Error("unexpected CLI invocation: " + argv.join(" "))
  } }
}

afterEach(async () => {
  resetMoveNotices()
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  takeWorkNotices("session-1")
  configureConcordAdapter({ reset: true })
  await ConcordAdapterPlugin({})
  configureHostLease({ reset: true })
})

describe("a confirmed worktree_claim move records the move notice", () => {
  test("the claim route records the notice naming the claimed path", async () => {
    await bindHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/claimed" } }), landingOnlyRunner())
    const envelope = await moveSessionToClaimedWorktree(claimArgs(), claimContext(), claimOkEnvelope())
    expect(envelope.outcome).toBe("ok")
    expectMoveNotice(takeMoveNotice("session-1"), "/claimed")
  })

  test("the tool result carries the notice line after the envelope", async () => {
    await bindHost(
      () => ({ status: 204, body: null }),
      () => ({ status: 200, body: { directory: "/claimed" } }),
      invokeRunner({ invoke: (operation) => operation === "worktree_claim"
        ? { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "worktree_claim", { result: { changed_refs: [], next_valid_intents: [], path: "/claimed" } })) + "\n", stderr: "" }
        : undefined }),
    )
    const result = await work_transition.execute({ request: { operation: "worktree_claim", input: { work_id: "work-1" } } } as never, claimContext() as never)
    const output = typeof result === "string" ? result : result.output
    const envelope = JSON.parse(output.split("\n")[0])
    expect(envelope.outcome, output).toBe("ok")
    expectMoveNotice(output.split("\n")[1] ?? null, "/claimed")
    // The envelope itself stays schema-clean: the notice rides the output
    // layer, not the published closed envelope.
    expect(envelope.output).toBeUndefined()
  })

  test("a landing mismatch refuses and records no notice", async () => {
    await bindHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/elsewhere" } }))
    const envelope = await moveSessionToClaimedWorktree(claimArgs(), claimContext(), claimOkEnvelope())
    expect(envelope.outcome).toBe("error")
    expect(takeMoveNotice("session-1")).toBeNull()
  })

  test("a non-claim operation records no notice", async () => {
    await bindHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/claimed" } }))
    const envelope = await moveSessionToClaimedWorktree({ operation: "lifecycle", input: {} } as never, claimContext(), claimOkEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(takeMoveNotice("session-1")).toBeNull()
  })
})

describe("a confirmed session_vacate move records the move notice", () => {
  test("the vacate route records the notice naming the registered main checkout", async () => {
    let sitting = "/worktree"
    await bindHost((body) => {
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }), landingOnlyRunner())
    const envelope = await moveSessionToRegisteredMainCheckout(vacateArgs({ idempotency_key: "vacate-notice-1" }), vacateContext(), vacateOkEnvelope())
    expect(envelope.outcome).toBe("ok")
    expectMoveNotice(takeMoveNotice("session-1"), "/main")
  })

  test("an agent-named destination refuses and records no notice", async () => {
    await bindHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/main" } }))
    const envelope = await moveSessionToRegisteredMainCheckout(vacateArgs({ idempotency_key: "vacate-notice-2", destination: "/other" }), vacateContext(), vacateOkEnvelope())
    expect(envelope.outcome).toBe("error")
    expect(takeMoveNotice("session-1")).toBeNull()
  })
})

describe("a confirmed terminal vacate records the move notice", () => {
  test("a completed lifecycle with a vacate target records the notice", async () => {
    let sitting = "/worktree"
    await bindHost((body) => {
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }), invokeRunner({ invoke: (operation) => operation === "session_vacate"
      ? { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } })) + "\n", stderr: "" }
      : undefined }))
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), vacateContext(), terminalOkEnvelope())
    expect(envelope.outcome).toBe("ok")
    expectMoveNotice(takeMoveNotice("session-1"), "/main")
  })

  test("a terminal vacate retains the verified destination when its earlier target differs", async () => {
    let sitting = "/worktree"
    await bindHost((body) => {
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }), invokeRunner({ invoke: (operation) => operation === "session_vacate"
      ? { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } })) + "\n", stderr: "" }
      : undefined }))
    const terminal = terminalOkEnvelope()
    terminal.result.vacate_target!.destination_directory = "/earlier-main"
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), vacateContext(), terminal)
    expect(envelope.outcome).toBe("ok")
    expect(sitting).toBe("/main")
    expectMoveNotice(takeMoveNotice("session-1"), "/main")
  })

  test("a failed vacate move queues a work notice and records no move notice", async () => {
    let sitting = "/worktree"
    await bindHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: sitting } }), invokeRunner({ invoke: (operation) => operation === "session_vacate"
      ? { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } })) + "\n", stderr: "" }
      : undefined }))
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), vacateContext(), terminalOkEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(takeMoveNotice("session-1")).toBeNull()
    // The failure keeps its own channel: the work notice names the replay.
    expect(takeWorkNotices("session-1").length).toBe(1)
  })

  test("a lifecycle without a vacate target records no notice", async () => {
    await bindHost(() => ({ status: 204, body: null }), () => ({ status: 200, body: { directory: "/main" } }), invokeRunner({ invoke: () => undefined }))
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), vacateContext(), terminalOkEnvelope(false))
    expect(envelope.outcome).toBe("ok")
    expect(takeMoveNotice("session-1")).toBeNull()
  })
})

describe("the work_relate supersede route carries the move notice", () => {
  test("a supersede with a vacate target records the notice on the relate result", async () => {
    let sitting = "/worktree"
    await bindHost((body) => {
      sitting = body.destination.directory
      return { status: 204, body: null }
    }, () => ({ status: 200, body: { directory: sitting } }), invokeRunner({ invoke: (operation) => {
      if (operation === "supersede") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_relate", "supersede", { result: { changed_refs: [], next_valid_intents: [], vacate_target: { work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } } })) + "\n", stderr: "" }
      if (operation === "session_vacate") return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("concord_work_transition", "session_vacate", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } })) + "\n", stderr: "" }
      return undefined
    } }))
    const result = await work_relate.execute(supersedeToolArgs() as never, vacateContext() as never)
    const output = typeof result === "string" ? result : result.output
    const envelope = JSON.parse(output.split("\n")[0])
    expect(envelope.outcome, output).toBe("ok")
    expectMoveNotice(output.split("\n")[1] ?? null, "/main")
  })
})

describe("the notice queue never leaks across calls", () => {
  test("a drained session leaves nothing behind", async () => {
    recordMoveNotice("session-1", "one")
    expect(takeMoveNotice("session-1")).toBe("one")
    expect(takeMoveNotice("session-1")).toBeNull()
    expect(takeMoveNotice("session-2")).toBeNull()
  })
})
