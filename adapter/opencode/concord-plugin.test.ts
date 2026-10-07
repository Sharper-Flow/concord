// CD-0102 D2: the plugin entry must register the hook that binds an authorized
// dispatch to the next native Task call. Without the registration the window is
// unreachable and any Task call the model composes runs unbound.
import { afterAll, afterEach, describe, expect, test } from "bun:test"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { directoryIdentity, dispatchWindows, DispatchWindowError, TASK_TOOL_ID } from "./dispatch-window"
import { hostControlPlane, MOVE_SESSION_ROUTE, MoveSessionUnavailable } from "./move-session"
import { enqueueWorkNotice } from "./concord"
import { hostToolSchemas } from "./generated-contracts"
import { bindCiWatchClient, ciWatchSettled, configureCiWatch, type VerbSpawner } from "./ci-watch"
import { configureCoreBinary } from "./dispatch"
import { armTurnMoveBoundary, questionRequiresNormalChat, resetTurnMoveBoundaries } from "./turn-move-boundary"
import { syntheticHostVersionFixture } from "./host-version.test-support"

syntheticHostVersionFixture()

test("work start publishes optional fields through the host definition hook", async () => {
  const plugin = await ConcordAdapterPlugin()
  // The host's legacy JSON-schema conversion drops undefined entries and
  // requires every remaining property before it invokes tool.definition.
  const properties = Object.fromEntries(Object.entries(plugin.tool.concord_work_start.args)
    .filter(([, value]) => value !== null && typeof value === "object" && !Array.isArray(value)))
  const parameters = {}
  const output = {
    description: plugin.tool.concord_work_start.description,
    parameters,
    jsonSchema: { type: "object", properties, required: Object.keys(properties) },
  }
  const hook = Reflect.get(plugin, "tool.definition")
  if (typeof hook === "function") await hook({ toolID: "concord_work_start" }, output)
  const published = JSON.parse(JSON.stringify(output.jsonSchema))
  const expected = Object.assign({}, ...hostToolSchemas.concord_work_start.oneOf.map((branch) => branch.properties))
  expect(published).toEqual({ type: "object", properties: expected, required: [], additionalProperties: false })
  expect(published.properties.work_id).toEqual(expected.work_id)
  expect(output.parameters).toBe(parameters)
  expect(output.description).toBe(plugin.tool.concord_work_start.description)
})

test("ci watch publishes only repo and selector as required through the host definition hook", async () => {
  const plugin = await ConcordAdapterPlugin()
  // The host's legacy JSON-schema conversion requires every per-field entry
  // before it invokes tool.definition.
  const properties = { ...plugin.tool.concord_ci_watch.args }
  const output = {
    description: plugin.tool.concord_ci_watch.description,
    parameters: {},
    jsonSchema: { type: "object", properties, required: Object.keys(properties) },
  }
  const hook = Reflect.get(plugin, "tool.definition")
  if (typeof hook === "function") await hook({ toolID: "concord_ci_watch" }, output)
  const published = JSON.parse(JSON.stringify(output.jsonSchema))
  expect(published.required).toEqual(["repo", "selector"])
  expect(published.additionalProperties).toBe(false)
  expect(Object.keys(published.properties).sort()).toEqual(["mode", "repo", "selector", "time_seconds_max"])
  expect(output.description).toBe(plugin.tool.concord_ci_watch.description)
})

// The text-part channel. The work-state reporter queues operator-facing
// blocks per session, and this hook drains them into the assistant's own
// message as the text part completes, so the transcript holds them without a
// tool-result expansion and the agent spends no tokens forming them.
describe("plugin entry registers the text-completion hook", () => {
  const completeHook = async () => {
    const plugin = await ConcordAdapterPlugin() as unknown as {
      "experimental.text.complete"?: (input: { sessionID: string; messageID: string; partID: string }, output: { text: string }) => Promise<void>
    }
    expect(typeof plugin["experimental.text.complete"]).toBe("function")
    return plugin["experimental.text.complete"] as (input: { sessionID: string; messageID: string; partID: string }, output: { text: string }) => Promise<void>
  }

  test("appends the queued notice blocks to the assistant text and clears the queue", async () => {
    const hook = await completeHook()
    enqueueWorkNotice("session-text", "Concord released the worktree for work-1.")
    enqueueWorkNotice("session-text", "| 🛫 CON-42 Complete (work-1) |\n| :-- |\n| ✅ Delegate passes the verb bytes |\n| ✓ check check:repo:verify · pass |")
    const output = { text: "assistant text" }
    await hook({ sessionID: "session-text", messageID: "message-1", partID: "part-1" }, output)
    expect(output.text).toBe([
      "assistant text",
      "Concord released the worktree for work-1.",
      "| 🛫 CON-42 Complete (work-1) |\n| :-- |\n| ✅ Delegate passes the verb bytes |\n| ✓ check check:repo:verify · pass |",
    ].join("\n\n"))
    // The queue drained, so a later completion in the same session carries
    // no notice and other sessions stay untouched.
    const second = { text: "more assistant text" }
    await hook({ sessionID: "session-text", messageID: "message-2", partID: "part-2" }, second)
    expect(second.text).toBe("more assistant text")
    const untouched = { text: "unrelated session text" }
    await hook({ sessionID: "session-text-other", messageID: "message-3", partID: "part-3" }, untouched)
    expect(untouched.text).toBe("unrelated session text")
  })

  test("leaves an empty session's text alone", async () => {
    const hook = await completeHook()
    const output = { text: "assistant text" }
    await hook({ sessionID: "session-no-notices", messageID: "message-1", partID: "part-1" }, output)
    expect(output.text).toBe("assistant text")
  })

  test("swallows a failed write so a display can never damage an assistant message", async () => {
    const hook = await completeHook()
    enqueueWorkNotice("session-throw", "| 🛫 CON-42 Complete (work-1) |\n| :-- |\n| ✅ Delegate passes the verb bytes |\n| ✓ check check:repo:verify · pass |")
    const output = { text: "assistant text" }
    Object.defineProperty(output, "text", {
      get: () => "assistant text",
      set: () => { throw new Error("the host refused the replacement") },
    })
    await expect(hook({ sessionID: "session-throw", messageID: "message-1", partID: "part-1" }, output)).resolves.toBeUndefined()
  })
})

// The session title is the single source of the goal text, and the compaction
// prompt is where that goal must survive: the host joins the strings this hook
// pushes onto the context into the prompt it summarizes with. Only a title the
// adapter wrote carries the goal prefix, so a generated title never reaches it.
describe("plugin entry registers the session compacting hook", () => {
  const compactingHook = async () => {
    const plugin = await ConcordAdapterPlugin() as unknown as {
      "experimental.session.compacting"?: (input: { sessionID: string }, output: { context: string[] }) => Promise<void>
    }
    expect(typeof plugin["experimental.session.compacting"]).toBe("function")
    return plugin["experimental.session.compacting"] as (input: { sessionID: string }, output: { context: string[] }) => Promise<void>
  }

  test("pushes the goal title onto the compaction context", async () => {
    // The factory re-binds the control plane, so the fake client binds after
    // the hook is resolved.
    const hook = await compactingHook()
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-compact", title: "Goal: Ship the tab stub" }, response: new Response(null, { status: 200 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    })
    const output = { context: [] as string[] }
    await hook({ sessionID: "session-compact" }, output)
    expect(output.context).toEqual(["Goal: Ship the tab stub"])
  })

  test("leaves a title without the goal prefix out of the compaction context", async () => {
    const hook = await compactingHook()
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-compact", title: "Fix login redirect" }, response: new Response(null, { status: 200 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    })
    const output = { context: ["existing"] }
    await hook({ sessionID: "session-compact" }, output)
    expect(output.context).toEqual(["existing"])
  })

  test("leaves the compaction context alone when the title route is absent", async () => {
    const hook = await compactingHook()
    const output = { context: ["existing"] }
    await hook({ sessionID: "session-compact" }, output)
    expect(output.context).toEqual(["existing"])
  })
})

test("work start definition hook leaves other tool definitions unchanged", async () => {
  const plugin = await ConcordAdapterPlugin()
  const output = { description: "another tool", parameters: {}, jsonSchema: { type: "object" } }
  const schema = output.jsonSchema
  const hook = Reflect.get(plugin, "tool.definition")
  expect(typeof hook).toBe("function")
  await hook({ toolID: "concord_work_trace" }, output)
  expect(output.jsonSchema).toBe(schema)
  expect(output.description).toBe("another tool")
})

test("published work start schemas cannot mutate runtime validation", async () => {
  const plugin = await ConcordAdapterPlugin()
  const output = { description: "work start", parameters: {}, jsonSchema: { properties: plugin.tool.concord_work_start.args } }
  await plugin["tool.definition"]({ toolID: "concord_work_start" }, output)
  const argsTitle = plugin.tool.concord_work_start.args.title
  const publishedTitle = output.jsonSchema.properties.title
  const expectedTitle = hostToolSchemas.concord_work_start.oneOf[0].properties.title
  const maximum = expectedTitle.maxLength
  try {
    argsTitle.maxLength = 1
    publishedTitle.maxLength = 2
    expect(expectedTitle.maxLength).toBe(maximum)
    expect(argsTitle.maxLength).toBe(1)
    expect(publishedTitle.maxLength).toBe(2)
  } finally {
    argsTitle.maxLength = maximum
    publishedTitle.maxLength = maximum
  }
})

const packet = {
  schema_version: "1.0" as const,
  attempt_id: "attempt-plugin",
  lane_id: "implement",
  lane_version: 1,
  lane_digest: "sha256:" + "b".repeat(64),
  work_id: "work-plugin",
  step_id: "step-1",
  inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, context: "", constraints: [] },
}

describe("plugin entry registers the dispatch window hook", () => {
  test("binds the recorded packet onto the next task call", async () => {
    // The factory directory is the execution instance directory the bind
    // compares against the window's claim, so the fixture host instance runs
    // in the same directory the window recorded.
    const plugin = (await ConcordAdapterPlugin({ directory: process.cwd() })) as {
      "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
    }
    expect(typeof plugin["tool.execute.before"]).toBe("function")

    // The bind reads back where the host runs this session, so the fixture host
    // must answer with the directory the window recorded.
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-plugin", directory: process.cwd() }, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("Task admission cannot write host state") },
    })
    dispatchWindows().open("session-plugin", packet, "", process.cwd())
    const output = { args: { subagent_type: "general", prompt: "whatever I like", task_id: "old" } }
    await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-plugin", callID: "call-1" }, output)

    expect(output.args.subagent_type).toBe("concord-implement")
    expect(JSON.parse(output.args.prompt).attempt_id).toBe("attempt-plugin")
    expect(output.args.task_id).toBeUndefined()
    expect(dispatchWindows().has("session-plugin")).toBe(false)
  })

  test("refuses an unauthorized managed task call and leaves other tools untouched", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
    }
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-none", metadata: { "concord.task_scope": "managed" } }, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("Task admission cannot write host state") },
    })
    const output = { args: { subagent_type: "general", prompt: "unbound" } }
    await expect(
      plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-none", callID: "call-2" }, output),
    ).rejects.toThrow(/no authorized dispatch/i)

    const other = { args: { filePath: "/x" } }
    await plugin["tool.execute.before"]({ tool: "read", sessionID: "session-none", callID: "call-3" }, other)
    expect(other.args.filePath).toBe("/x")
  })
})

// CON-397. The host runs a native Task child in the plugin factory's
// directory — the execution instance directory — while GET /session/{id} can
// report the claimed worktree. Reported placement is still checked, but only
// the instance directory proves where the worker executes, so the bind gate
// in tool.execute.before (CD-0102 D7) compares it against the window's claim.
describe("plugin entry refuses a dispatch whose worker would execute in the launch trunk", () => {
  test("trunk execution: the before hook refuses before the worker writes, preserves the caller arguments, discards the window, and records no in-flight attempt", async () => {
    const originalCwd = process.cwd()
    const trunk = fs.mkdtempSync(path.join(os.tmpdir(), "concord-trunk-"))
    const claimedWorktree = fs.mkdtempSync(path.join(os.tmpdir(), "concord-claimed-worktree-"))
    const sessionID = "session-trunk-execution"
    const sentinel = path.join(trunk, "worker-write-sentinel")
    try {
      // The observed case: the host process also runs from the trunk.
      process.chdir(trunk)
      expect(directoryIdentity(process.cwd())).toBe(directoryIdentity(trunk))
      expect(directoryIdentity(trunk)).not.toBe(directoryIdentity(claimedWorktree))
      const raw = {
        get: async (request: { url: string }) => {
          if (request.url === "/session/{id}") {
            return { data: { id: sessionID, directory: claimedWorktree }, response: new Response(null, { status: 200 }) }
          }
          return { response: new Response(null, { status: 404 }) }
        },
        post: async () => { throw new Error("Task admission cannot write host state") },
      }
      const plugin = (await ConcordAdapterPlugin({ directory: trunk, worktree: trunk, client: { _client: raw } as never })) as {
        "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
      }
      // The host session record reports the claimed worktree and the open
      // window owns the claim on it.
      expect(directoryIdentity(await hostControlPlane().sessionDirectory(sessionID))).toBe(directoryIdentity(claimedWorktree))
      dispatchWindows().open(sessionID, packet, "", claimedWorktree)
      expect(dispatchWindows().has(sessionID)).toBe(true)

      const output = { args: { subagent_type: "general", prompt: "whatever I like", task_id: "old" } }
      const callerArgs = { ...output.args }
      let refusal: unknown
      try {
        await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID, callID: "call-trunk-execution" }, output)
      } catch (error) {
        refusal = error
      }
      // An admitted call runs the worker in the factory directory — the
      // trunk — so the write below is the first worker write the admitted
      // execution performs there. A refused call writes nothing.
      if (refusal === undefined) fs.writeFileSync(sentinel, "the worker wrote the trunk")

      expect(refusal).toBeInstanceOf(DispatchWindowError)
      // The refusal leaves the caller's composed arguments untouched.
      expect(output.args).toEqual(callerArgs)
      // The open window is discarded, so no later Task call consumes it.
      expect(dispatchWindows().has(sessionID)).toBe(false)
      // No in-flight attempt exists, so no settle path can admit a result.
      expect(dispatchWindows().inFlightAttempt(sessionID)).toBe(null)
      // The refused worker never wrote the trunk.
      expect(fs.existsSync(sentinel)).toBe(false)
    } finally {
      process.chdir(originalCwd)
      dispatchWindows().close(sessionID)
      dispatchWindows().takeInFlight(sessionID)
      hostControlPlane().bind(undefined)
      configureHostLease({ reset: true })
      fs.rmSync(trunk, { recursive: true, force: true })
      fs.rmSync(claimedWorktree, { recursive: true, force: true })
    }
  })

  // A host restart recreates the plugin and empties in-memory claim records,
  // so the fence rests on the window record plus the factory's own instance
  // directory alone; a recreated instance enforces it identically.
  test("a recreated plugin enforces the execution fence with no in-memory claim", async () => {
    const trunk = fs.mkdtempSync(path.join(os.tmpdir(), "concord-trunk-recreated-"))
    const claimedWorktree = fs.mkdtempSync(path.join(os.tmpdir(), "concord-claimed-recreated-"))
    const sessionID = "session-plugin-recreation"
    const raw = {
      get: async () => ({ data: { id: sessionID, directory: claimedWorktree }, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("Task admission cannot write host state") },
    }
    const callerArgs = () => ({ subagent_type: "general", prompt: "whatever I like", task_id: "old" })
    try {
      dispatchWindows().open(sessionID, packet, "", claimedWorktree)
      const first = (await ConcordAdapterPlugin({ directory: trunk, worktree: trunk, client: { _client: raw } as never })) as {
        "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
      }
      const firstOutput = { args: callerArgs() }
      await expect(first["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID, callID: "call-first-instance" }, firstOutput))
        .rejects.toThrow(/does not match the active claimed worktree/i)
      expect(firstOutput.args).toEqual(callerArgs())

      dispatchWindows().open(sessionID, packet, "", claimedWorktree)
      const restarted = (await ConcordAdapterPlugin({ directory: trunk, worktree: trunk, client: { _client: raw } as never })) as {
        "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
      }
      const restartedOutput = { args: callerArgs() }
      await expect(restarted["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID, callID: "call-restarted-instance" }, restartedOutput))
        .rejects.toThrow(/does not match the active claimed worktree/i)
      expect(restartedOutput.args).toEqual(callerArgs())
      expect(dispatchWindows().has(sessionID)).toBe(false)
      expect(dispatchWindows().inFlightAttempt(sessionID)).toBe(null)

      // An instance whose factory directory is the claimed worktree binds.
      dispatchWindows().open(sessionID, packet, "", claimedWorktree)
      const placed = (await ConcordAdapterPlugin({ directory: claimedWorktree, worktree: claimedWorktree, client: { _client: raw } as never })) as {
        "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
      }
      const bound = { args: callerArgs() }
      await placed["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID, callID: "call-placed-instance" }, bound)
      expect(bound.args.subagent_type).toBe("concord-implement")
      expect(dispatchWindows().inFlightAttempt(sessionID)?.packet.attempt_id).toBe(packet.attempt_id)
    } finally {
      dispatchWindows().close(sessionID)
      dispatchWindows().takeInFlight(sessionID)
      hostControlPlane().bind(undefined)
      configureHostLease({ reset: true })
      fs.rmSync(trunk, { recursive: true, force: true })
      fs.rmSync(claimedWorktree, { recursive: true, force: true })
    }
  })

  // The positive path: an instance whose factory directory is the claimed
  // worktree binds the packet, and the worker's first write lands only in the
  // claimed worktree while the host process stays in the trunk.
  test("a claimed-instance dispatch binds and the simulated worker writes only the claimed worktree", async () => {
    const originalCwd = process.cwd()
    const trunk = fs.mkdtempSync(path.join(os.tmpdir(), "concord-trunk-positive-"))
    const claimedWorktree = fs.mkdtempSync(path.join(os.tmpdir(), "concord-claimed-positive-"))
    const sessionID = "session-claimed-instance"
    const sentinelName = "worker-write-sentinel"
    const raw = {
      get: async () => ({ data: { id: sessionID, directory: claimedWorktree }, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("Task admission cannot write host state") },
    }
    try {
      process.chdir(trunk)
      const plugin = (await ConcordAdapterPlugin({ directory: claimedWorktree, worktree: claimedWorktree, client: { _client: raw } as never })) as {
        "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
      }
      dispatchWindows().open(sessionID, packet, "", claimedWorktree)
      const output = { args: { subagent_type: "general", prompt: "whatever I like", task_id: "old" } }
      await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID, callID: "call-claimed-instance" }, output)
      expect(output.args.subagent_type).toBe("concord-implement")
      expect(JSON.parse(output.args.prompt).attempt_id).toBe(packet.attempt_id)

      // Simulated host execution, not a real OpenCode run: the host starts the
      // Task child in the instance directory, so a native child with that cwd
      // performs the worker's first write there.
      const child = Bun.spawnSync([process.execPath, "-e", `require("node:fs").writeFileSync(${JSON.stringify(sentinelName)}, process.cwd())`], { cwd: claimedWorktree })
      expect(child.exitCode).toBe(0)
      expect(directoryIdentity(fs.readFileSync(path.join(claimedWorktree, sentinelName), "utf8"))).toBe(directoryIdentity(claimedWorktree))
      expect(fs.existsSync(path.join(trunk, sentinelName))).toBe(false)
      expect(process.cwd()).toBe(trunk)
    } finally {
      process.chdir(originalCwd)
      dispatchWindows().close(sessionID)
      dispatchWindows().takeInFlight(sessionID)
      hostControlPlane().bind(undefined)
      configureHostLease({ reset: true })
      fs.rmSync(trunk, { recursive: true, force: true })
      fs.rmSync(claimedWorktree, { recursive: true, force: true })
    }
  })
})

// CD-0098 D2. The control plane takes its transport from the client the host
// supplies, because that client dispatches in process and carries the host's
// headers. A host that binds no listener still exposes a `serverUrl`, so a
// route reached through that value cannot connect and work start refuses on
// every attempt.
describe("plugin entry binds the control plane to the host client", () => {
  test("routes the move through the client the host supplied", async () => {
    const seen: Array<{ url: string; body?: unknown }> = []
    const raw = {
      get: async () => ({ data: { directory: "/w" }, response: new Response(null, { status: 200 }) }),
      post: async ({ url, body }: { url: string; body?: unknown }) => {
        seen.push({ url, body })
        return { response: new Response(null, { status: 204 }) }
      },
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })

    await hostControlPlane().moveSession("session-1", "/w")
    expect(seen).toEqual([{ url: MOVE_SESSION_ROUTE, body: { sessionID: "session-1", destination: { directory: "/w" } } }])
    expect(await hostControlPlane().sessionDirectory("session-1")).toBe("/w")
  })

  test("refuses the route when the host supplied no client", async () => {
    await ConcordAdapterPlugin({ serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().moveSession("session-1", "/w")).rejects.toBeInstanceOf(MoveSessionUnavailable)
    // A server URL is present and still yields no route: the URL is not what
    // the adapter needs, so its presence must not read as a usable transport.
    await expect(hostControlPlane().moveSession("session-1", "/w")).rejects.toThrow(/handed the plugin no client \(host version synthetic-test-host\)/)
  })
})

// The client parses the refusal body before it returns, so the parsed value on
// the result is the only readable copy. A refusal that reads the response a
// second time reports that the host said nothing, and the operator loses the
// one sentence that says why the move failed.
describe("a refusal carries the host's own words", () => {
  // A body the client already consumed. Reading it again throws, which is what
  // a real host response does once the SDK client has parsed it.
  const consumed = (status: number, payload: unknown): Response => {
    const response = new Response(JSON.stringify(payload), { status })
    void response.text()
    return response
  }

  test("reports the host message when the move is refused", async () => {
    const raw = {
      get: async () => ({ data: { directory: "/w" }, response: new Response(null, { status: 200 }) }),
      post: async () => ({
        data: { data: { message: "worktree /w is held by another session" } },
        response: consumed(409, { data: { message: "worktree /w is held by another session" } }),
      }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().moveSession("session-1", "/w")).rejects.toThrow(
      /worktree \/w is held by another session/,
    )
  })

  test("reports the host message when the session readback is refused", async () => {
    const raw = {
      get: async () => ({
        data: { message: "session-1 is unknown to this host" },
        response: consumed(404, { message: "session-1 is unknown to this host" }),
      }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().sessionDirectory("session-1")).rejects.toThrow(
      /session-1 is unknown to this host/,
    )
  })

  test("says so plainly when the host supplied no refusal at all", async () => {
    const raw = {
      get: async () => ({ response: new Response(null, { status: 500 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
    await expect(hostControlPlane().sessionDirectory("session-1")).rejects.toThrow(/no readable refusal/)
  })
})

// The ci-watch start notice is a noReply user message the host still reports
// through chat.message. The hook must return early for it: a drained report
// would land in a message that never wakes the model, and the boundary clear
// would treat the notice as an operator turn. These tests queue a real report
// and prove the notice message consumes nothing.
describe("plugin entry isolates the ci-watch notice from chat.message", () => {
  const NOTICE_SESSION = "notice-session"

  const watchFixture = () => {
    const posts: Array<{ url: string; body?: unknown }> = []
    const injected: Array<Record<string, unknown>> = []
    let promptCount = 0
    const client = {
      get: async (request: { url: string }) => {
        if (request.url === "/session/status") {
          return { data: { [NOTICE_SESSION]: { type: "idle" } }, response: new Response(null, { status: 200 }) }
        }
        if (request.url === "/session/{id}") {
          return {
            data: { id: NOTICE_SESSION, agent: "concord-1", model: { id: "glm-test", providerID: "zai" } },
            response: new Response(null, { status: 200 }),
          }
        }
        if (request.url === "/session/{id}/message") {
          return { data: injected, response: new Response(null, { status: 200 }) }
        }
        return { response: new Response(null, { status: 404 }) }
      },
      post: async (request: { url: string; body?: unknown }) => {
        posts.push({ url: request.url, body: request.body })
        if (request.url === "/session/{id}/prompt_async") {
          promptCount++
          const id = `msg_report_${promptCount}`
          const text = (request.body as { parts?: Array<{ text?: string }> })?.parts?.[0]?.text ?? ""
          injected.push({ info: { id, role: "user" }, parts: [{ type: "text", text }] })
          return { response: new Response(null, { status: 204 }) }
        }
        return { response: new Response(null, { status: 200 }) }
      },
    }
    return { posts, client }
  }

  test("a notice message drains nothing and the next real message drains the queued report", async () => {
    const fixture = watchFixture()
    configureCiWatch({ stateDir: fs.mkdtempSync(path.join(os.tmpdir(), "plugin-notice-test-")), idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
    configureCoreBinary("/synthetic/concord")
    const spawner: VerbSpawner = async () => ({ exitCode: 0, stdout: JSON.stringify({ status: "success" }), stderr: "" })
    configureCiWatch({ spawner })
    const plugin = await ConcordAdapterPlugin({ client: { _client: fixture.client } as never })
    const started = JSON.parse(
      (await plugin.tool.concord_ci_watch.execute!(
        { repo: "owner/name", selector: { kind: "pr", value: "12" } },
        { sessionID: NOTICE_SESSION, abort: new AbortController().signal },
      )).output,
    ) as Record<string, unknown>
    expect(started.status).toBe("started")
    await ciWatchSettled(String(started.watch_id))
    const noticeOutput = {
      parts: [
        { type: "text", text: "⏳ Watching CI", ignored: true, metadata: { "concord.ci_watch_notice": started.watch_id } },
      ] as unknown[],
    }
    await plugin["chat.message"]({ sessionID: NOTICE_SESSION, messageID: "msg_notice" }, noticeOutput)
    expect(noticeOutput.parts).toHaveLength(1)
    const realOutput = { parts: [] as unknown[] }
    await plugin["chat.message"]({ sessionID: NOTICE_SESSION, messageID: "msg_real" }, realOutput)
    expect(realOutput.parts).toHaveLength(1)
    expect((realOutput.parts[0] as { messageID: string }).messageID).toBe("msg_real")
    expect((realOutput.parts[0] as { synthetic: boolean }).synthetic).toBe(true)
  })

  test("the notice message leaves the turn-move boundary armed and a real message clears it", async () => {
    const plugin = await ConcordAdapterPlugin()
    const session = "notice-boundary-session"
    const noticePart = { type: "text", text: "notice", ignored: true, metadata: { "concord.ci_watch_notice": "watch-1" } }
    armTurnMoveBoundary(session)
    await plugin["chat.message"]({ sessionID: session, messageID: "msg_notice_boundary" }, { parts: [noticePart] })
    expect(questionRequiresNormalChat(session)).toBe(true)
    await plugin["chat.message"]({ sessionID: session, messageID: "msg_real_boundary" }, { parts: [] })
    expect(questionRequiresNormalChat(session)).toBe(false)
  })
})

// The control plane is module-shared state. A test that binds a fake client
// and leaves it bound changes what every later test file sees when the runner
// shares one process, so every binding is undone as soon as its test ends.
afterEach(() => {
  hostControlPlane().bind(undefined)
  configureCiWatch({ reset: true })
  bindCiWatchClient(undefined)
  configureCoreBinary(null)
  resetTurnMoveBoundaries()
})

// The factory claims a host lease at load, which fails against the unstamped
// repository placeholder and closes the adapter transport. Later test files
// share this process, so the factory tests leave the lease state clean.
afterAll(() => configureHostLease({ reset: true }))
