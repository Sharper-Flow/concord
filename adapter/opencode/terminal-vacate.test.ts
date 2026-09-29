// A terminal transition (complete, cancel, supersede) leaves the calling
// session inside the work item's worktree, so reclaiming the tree would move
// or remove it under a live session. These tests hold the terminal-vacate
// hook to its contract (CD-0179): the core names the vacate target only when
// the session runs in that work item's worktree, the vacate runs before the
// move, and a failed move keeps the transition result and queues a notice.
import { afterEach, afterAll, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { configureCoreBinary } from "./dispatch"
import { manifestDigest } from "./generated-contracts"
import { configureConcordAdapter, moveSessionToRegisteredMainCheckout, takeWorkNotices, vacateTerminalWorktree } from "./concord"
import { resetClaimedWorktrees, armClaimedWorktree, armedClaimedWorktree, clearClaimedWorktree } from "./claimed-worktree"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"

configureCoreBinary("concord")

const context = () =>
  ({ sessionID: "session-1", messageID: "message-1", abort: new AbortController().signal, directory: "/worktree" }) as Parameters<typeof moveSessionToRegisteredMainCheckout>[1]

const lifecycleArgs = (target: string) => ({ operation: "lifecycle", input: { work_id: "work-1", target, expected_version: 4, idempotency_key: "k-1" } })

const supersedeArgs = () => ({ operation: "supersede", input: { predecessor_id: "work-1", successor_id: "work-2", predecessor_expected_version: 4, successor_expected_version: 1, reason: "replaced", idempotency_key: "k-2" } })

const okEnvelope = (withTarget = true) => ({
  schema_version: "1.0",
  outcome: "ok",
  result: {
    changed_refs: [],
    next_valid_intents: [],
    ...(withTarget ? { vacate_target: { work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } } : {}),
  },
})

let vacateCalls: string[] = []
let landingCalls: string[] = []

// coreEnvelope mirrors the core's TS7 envelope contract the adapter
// validates every response against. A mutation response carries the
// changed_refs and next_valid_intents arrays at the envelope level.
const coreEnvelope = (operation: string, fields: Record<string, unknown>) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core",
  tool: "concord_work_transition", operation, outcome: "ok", resolved_scope: null, authority: "authoritative",
  freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [],
  warnings: [], evidence_refs: [], replayed: false, changed_refs: [], next_valid_intents: [], ...fields,
})

function vacateRunner(opts: { landingFails?: boolean } = {}) {
  return {
    async run(argv: string[], input: string) {
      // The invoke path resolves the call context before the operation, so
      // the runner answers project-resolve the way the core does.
      if (argv[1] === "project-resolve") {
        return { exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", scope_version: "scope-1", main_worktree: false, product_ids: ["product-1"] }) + "\n", stderr: "" }
      }
      if (argv[1] === "invoke") {
        const parsed = JSON.parse(input) as { operation?: string }
        if (parsed.operation === "session_vacate") {
          vacateCalls.push(parsed.operation)
          return { exitCode: 0, stdout: JSON.stringify(coreEnvelope("session_vacate", { result: { changed_refs: [], next_valid_intents: [], work_id: "work-1", project_id: "project-1", source_directory: "/worktree", destination_directory: "/main" } })) + "\n", stderr: "" }
        }
      }
      if (argv[1] === "vacate-landing") {
        landingCalls.push((JSON.parse(input) as { landed_directory: string }).landed_directory)
        if (opts.landingFails) {
          return { exitCode: 1, stdout: "", stderr: "vacate-landing refused: the committed request is not pending\n" }
        }
        return { exitCode: 0, stdout: JSON.stringify({ work_id: "work-1", already_recorded: false }) + "\n", stderr: "" }
      }
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    },
  }
}

async function fakeHost(getDirectory: string, runnerOpts?: { landingFails?: boolean }) {
  // The plugin factory claims the host lease at init and records a fault
  // when the claim fails; every later invoke then refuses with
  // host_lease_missing. Binding the release and the lease runner first lets
  // the claim succeed the way an installed adapter's does.
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" },
    runner: { async run() { return { exitCode: 0, stdout: JSON.stringify({ pid: 4242, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest }), stderr: "" } } } as never,
  })
  configureConcordAdapter({ runner: vacateRunner(runnerOpts) as never })
  await ConcordAdapterPlugin({
    client: {
      _client: {
        post: async () => ({ data: null, response: new Response(null, { status: 204 }) }),
        get: async () => ({ data: { directory: getDirectory }, response: new Response(null, { status: 200 }) }),
      },
    } as never,
    serverUrl: new URL("http://127.0.0.1:4096"),
  })
}

afterEach(async () => {
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  vacateCalls = []
  landingCalls = []
  takeWorkNotices("session-1")
  configureHostLease({ reset: true })
  configureConcordAdapter({ reset: true })
  await ConcordAdapterPlugin({})
})

describe("terminal transitions vacate the worktree after success", () => {
  test("a completed lifecycle with a vacate target vacates, moves, and records the verified landing", async () => {
    await fakeHost("/main")
    armClaimedWorktree("session-1", "/worktree")
    try {
      const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), okEnvelope())
      expect(envelope.outcome).toBe("ok")
      expect(vacateCalls).toEqual(["session_vacate"])
      // The verified landing runs only after the host readback names the
      // registered main checkout: the landing releases the occupancy rows
      // the core vacate request left standing.
      expect(landingCalls).toEqual(["/main"])
      expect(armedClaimedWorktree("session-1")).toBeNull()
      expect(takeWorkNotices("session-1")).toEqual([])
    } finally {
      clearClaimedWorktree("session-1")
    }
  })

  test("a supersede with a vacate target vacates, moves, and records the verified landing", async () => {
    await fakeHost("/main")
    const envelope = await vacateTerminalWorktree("concord_work_relate", supersedeArgs(), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual(["session_vacate"])
    expect(landingCalls).toEqual(["/main"])
  })

  test("a lifecycle without a vacate target does nothing", async () => {
    await fakeHost("/main")
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), okEnvelope(false))
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual([])
    expect(landingCalls).toEqual([])
  })

  test("a non-terminal lifecycle target does nothing", async () => {
    await fakeHost("/main")
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("in_progress"), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual([])
    expect(landingCalls).toEqual([])
  })

  test("an error envelope does nothing", async () => {
    await fakeHost("/main")
    const failure = { schema_version: "1.0", outcome: "error", error: { kind: "invalid_transition", message: "no" } }
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), failure as never)
    expect(vacateCalls).toEqual([])
    expect(landingCalls).toEqual([])
    expect(envelope).toBe(failure)
  })

  // The transition is durable: a failed move must not fail the tool result.
  // The notice names the work item and the replay route instead. The refused
  // move records no landing, so the occupancy rows stand.
  test("a failed move keeps the transition result, records no landing, and queues a notice", async () => {
    await fakeHost("/nowhere-else")
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual(["session_vacate"])
    expect(landingCalls).toEqual([])
    const notices = takeWorkNotices("session-1")
    expect(notices).toHaveLength(1)
    expect(notices[0]).toContain("work-1")
    expect(notices[0]).toContain("session_vacate")
  })

  // A move that landed but recorded no landing leaves the session at the
  // registered main checkout with the relocation request standing, so a
  // worktree retry cannot recover it. The notice reports the possible
  // effect and names the verified-destination replay instead (CD-0190 D3).
  test("a landing record failure after the move landed reports the possible effect and the verified-destination replay", async () => {
    await fakeHost("/main", { landingFails: true })
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual(["session_vacate"])
    const notices = takeWorkNotices("session-1")
    expect(notices).toHaveLength(1)
    expect(notices[0]).toContain("effect_state possible")
    expect(notices[0]).toContain("/main")
    expect(notices[0]).toContain("session_vacate")
  })
})

afterAll(() => configureHostLease({ reset: true }))
