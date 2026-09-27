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

// coreEnvelope mirrors the core's TS7 envelope contract the adapter
// validates every response against. A mutation response carries the
// changed_refs and next_valid_intents arrays at the envelope level.
const coreEnvelope = (operation: string, fields: Record<string, unknown>) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core",
  tool: "concord_work_transition", operation, outcome: "ok", resolved_scope: null, authority: "authoritative",
  freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [],
  warnings: [], evidence_refs: [], replayed: false, changed_refs: [], next_valid_intents: [], ...fields,
})

function vacateRunner() {
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
      throw new Error("unexpected CLI invocation: " + argv.join(" "))
    },
  }
}

async function fakeHost(getDirectory: string) {
  // The plugin factory claims the host lease at init and records a fault
  // when the claim fails; every later invoke then refuses with
  // host_lease_missing. Binding the release and the lease runner first lets
  // the claim succeed the way an installed adapter's does.
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" },
    runner: { async run() { return { exitCode: 0, stdout: JSON.stringify({ pid: 4242, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest }), stderr: "" } } } as never,
  })
  configureConcordAdapter({ runner: vacateRunner() as never })
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
  takeWorkNotices("session-1")
  configureHostLease({ reset: true })
  configureConcordAdapter({ reset: true })
  await ConcordAdapterPlugin({})
})

describe("terminal transitions vacate the worktree after success", () => {
  test("a completed lifecycle with a vacate target vacates and moves to the main checkout", async () => {
    await fakeHost("/main")
    armClaimedWorktree("session-1", "/worktree")
    try {
      const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), okEnvelope())
      expect(envelope.outcome).toBe("ok")
      expect(vacateCalls).toEqual(["session_vacate"])
      expect(armedClaimedWorktree("session-1")).toBeNull()
      expect(takeWorkNotices("session-1")).toEqual([])
    } finally {
      clearClaimedWorktree("session-1")
    }
  })

  test("a supersede with a vacate target vacates and moves", async () => {
    await fakeHost("/main")
    const envelope = await vacateTerminalWorktree("concord_work_relate", supersedeArgs(), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual(["session_vacate"])
  })

  test("a lifecycle without a vacate target does nothing", async () => {
    await fakeHost("/main")
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), okEnvelope(false))
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual([])
  })

  test("a non-terminal lifecycle target does nothing", async () => {
    await fakeHost("/main")
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("in_progress"), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual([])
  })

  test("an error envelope does nothing", async () => {
    await fakeHost("/main")
    const failure = { schema_version: "1.0", outcome: "error", error: { kind: "invalid_transition", message: "no" } }
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), failure as never)
    expect(vacateCalls).toEqual([])
    expect(envelope).toBe(failure)
  })

  // The transition is durable: a failed move must not fail the tool result.
  // The notice names the work item and the replay route instead.
  test("a failed move keeps the transition result and queues a notice", async () => {
    await fakeHost("/nowhere-else")
    const envelope = await vacateTerminalWorktree("concord_work_transition", lifecycleArgs("completed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
    expect(vacateCalls).toEqual(["session_vacate"])
    const notices = takeWorkNotices("session-1")
    expect(notices).toHaveLength(1)
    expect(notices[0]).toContain("work-1")
    expect(notices[0]).toContain("session_vacate")
  })
})

afterAll(() => configureHostLease({ reset: true }))
