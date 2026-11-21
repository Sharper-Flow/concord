import { afterEach, describe, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureCoreBinary, type CoreRunner } from "./core-runtime"
import { manifestDigest } from "./generated-contracts"
import { configureHostLease } from "./host-lease"
import { configureConcordAdapter, work_trace } from "./concord"
import { fakeV2Context } from "./v2-context.fixture"

// The consume resolves the core binary path before the runner seam
// intercepts the verb, so the file binds a path once at module level.
configureCoreBinary("concord")

const context = () =>
  ({
    sessionID: "session-receive",
    messageID: "message-1",
    agent: "agent-receive",
    signal: new AbortController().signal,
    sessionDirectory: async () => "/worktree",
  }) satisfies Parameters<typeof work_trace.execute>[1]

// fakeHost wires the plugin's session context so the session lookup
// answers the host-placed checkout, and swaps the runner
// seam the owning invoke route uses. The plugin setup claims the host
// lease at load, so the file stamps the release identity and answers the
// claim leg the way an installed release would, or every operation would
// refuse closed before the transport runs (CD-0111 D2). The runner models
// the real two-leg transport: `concord project-resolve` for the ambient
// context, then `concord invoke` for the typed consume.
async function fakeHost(runner: CoreRunner) {
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
  const { context, tools } = fakeV2Context(async () => ({ location: { directory: "/worktree" } }))
  await ConcordAdapterPlugin.setup(context)
  return tools.find((tool) => tool.name === "concord_work_trace")
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
    origin: "core",
    tool,
    operation,
    outcome,
    manifest_digest: manifestDigest,
    next_cursor: null,
    omissions: [],
    warnings: [],
    ...extra,
  }
}

afterEach(async () => {
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
})

describe("project_retirement preserves the core's derived state", () => {
  const hostCall = (operation: string, input: Record<string, unknown>) => ({ request: { operation, input } })
  const rawHostResult = async (result: Promise<{ content: string }>) => {
    const value = await result
    const newline = value.content.indexOf("\n")
    return JSON.parse(newline === -1 ? value.content : value.content.slice(0, newline))
  }

  const readyAnswer = () =>
    coreEnvelope("concord_work_trace", "project_retirement", "ok", {
      result: { work_id: "work-1", project_id: "project-receive", session_ref: "session-receive", state: "ready_to_close_or_replace", recorded_handoff: true, artifacts_preserved: true, blockers: [] },
    })

  async function readRetirement() {
    const captured: CapturedInvoke = {}
    const tool = await fakeHost(coreRunner(readyAnswer(), captured))
    return rawHostResult(tool.execute(hostCall("project_retirement", { work_id: "work-1" }), context()))
  }

  test("a clean session passes the core's derived ready state through", async () => {
    const result = await readRetirement()
    expect(result.outcome).toBe("ok")
    expect(result.result.state).toBe("ready_to_close_or_replace")
    expect(result.result.blockers).toEqual([])
  })

})
