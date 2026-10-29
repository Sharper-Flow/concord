// CD-0102 D2: the plugin entry must register the hook that binds an authorized
// dispatch to the next native Task call. Without the registration the window is
// unreachable and any Task call the model composes runs unbound.
import { afterAll, afterEach, describe, expect, test } from "bun:test"
import nodeFs from "node:fs"
import os from "node:os"
import path from "node:path"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { directoryIdentity, dispatchWindows, DispatchWindowError, TASK_TOOL_ID } from "./dispatch-window"
import { hostControlPlane, MOVE_SESSION_ROUTE, MoveSessionUnavailable } from "./move-session"
import { enqueueWorkNotice } from "./concord"
import { contractOperations, hostToolSchemas } from "./generated-contracts"
import { expandedPublishedRequestSchema } from "./generated-contract-tests"
import { configureCoreBinary } from "./dispatch"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"
import { syntheticHostVersionFixture } from "./host-version.test-support"
const testDirectories: string[] = []
function mkdtempSync(prefix: string): string {
  const directory = nodeFs.mkdtempSync(prefix)
  testDirectories.push(directory)
  return directory
}
const fs = { ...nodeFs, mkdtempSync }

syntheticHostVersionFixture()

test("work start publishes the closed capture and resume contract through the host definition hook", async () => {
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
  if (typeof hook !== "function") throw new Error("plugin registers no tool.definition hook")
  await hook({ toolID: "concord_work_start" }, output)
  const published = JSON.parse(JSON.stringify(output.jsonSchema))
  // The generated closed modes survive in conditional branches; the root
  // remains an object accepted by Anthropic's tool-schema validator.
  expect(published.type).toBe("object")
  expect(published.oneOf).toBeUndefined()
  expect(published.if).toEqual({ required: ["work_id"] })
  // Publication interns repeated field schemas into the root's local
  // definitions table, so the branch comparison reads the resolved view
  // against the unchanged generated contracts.
  const resolved = expandedPublishedRequestSchema(published) as any
  expect(resolved.else.if).toEqual({ required: ["issue_key"] })
  expect(resolved.else.else).toEqual(hostToolSchemas.concord_work_start.oneOf[0])
  expect(resolved.then).toEqual(expandedPublishedRequestSchema(hostToolSchemas.concord_work_start.oneOf[1]))
  expect(resolved.else.then).toEqual(expandedPublishedRequestSchema(hostToolSchemas.concord_work_start.oneOf[1]))
  expect(resolved.else.else.properties.work_id).toBeUndefined()
  expect(resolved.then.properties.title).toBeUndefined()
  expect(output.parameters).toBe(parameters)
  expect(output.description).toBe(plugin.tool.concord_work_start.description)
})

test("the plugin registration set equals the complete host tool catalog", async () => {
  // Every registered tool must be a catalog member, so the generated lane
  // and utility denials derived from the catalog can never miss a new
  // registration.
  const plugin = await ConcordAdapterPlugin()
  const catalog = [...new Set([...Object.keys(hostToolSchemas), ...contractOperations.map((operation: any) => operation.tool)])].sort()
  expect(catalog.length).toBe(13)
  expect(Object.keys(plugin.tool).sort()).toEqual(catalog)
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
      get: async () => ({ data: { id: "session-compact", title: "Goal: Ship the closure notice" }, response: new Response(null, { status: 200 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    })
    const output = { context: [] as string[] }
    await hook({ sessionID: "session-compact" }, output)
    expect(output.context).toEqual(["Goal: Ship the closure notice"])
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

test("the definition hook leaves tools no hook owns unchanged", async () => {
  const plugin = await ConcordAdapterPlugin()
  const output = { description: "another tool", parameters: {}, jsonSchema: { type: "object" } }
  const schema = output.jsonSchema
  const hook = Reflect.get(plugin, "tool.definition")
  expect(typeof hook).toBe("function")
  // "bash" is no Concord tool: neither the request hook, the work start
  // hook, nor the ci watch hook may touch its definition.
  await hook({ toolID: "bash" }, output)
  expect(output.jsonSchema).toBe(schema)
  expect(output.description).toBe("another tool")
})

test("a request tool publishes the hoisted argument root through the host definition hook", async () => {
  const plugin = await ConcordAdapterPlugin()
  const output = {
    description: plugin.tool.concord_work_trace.description,
    parameters: { untouched: true } as Record<string, unknown>,
    jsonSchema: { type: "object", properties: { request: { type: "object" } }, required: ["request"] },
  }
  const hook = Reflect.get(plugin, "tool.definition")
  await hook({ toolID: "concord_work_trace" }, output)
  const published = JSON.parse(JSON.stringify(output.jsonSchema))
  // The final registered root carries the request body under properties.request
  // and the request definitions at the root, so every local reference the
  // compact request makes resolves at the root the provider sees.
  expect(published.type).toBe("object")
  expect(published.required).toEqual(["request"])
  expect(Object.keys(published.properties)).toEqual(["request"])
  expect(published.$defs).toBeObject()
  expect(Object.keys(published.$defs).length).toBeGreaterThan(0)
  expect(Object.hasOwn(published.properties.request, "$defs")).toBe(false)
  expect(published.properties.request.required).toEqual(["operation", "input"])
  expect(published.properties.request.oneOf).toHaveLength(
    contractOperations.filter((operation: any) => operation.tool === "concord_work_trace").length,
  )
  // The hoisted definitions are the ones the registration schema's compact
  // request names, and the publication replaces only the definition schema.
  const { $defs } = plugin.tool.concord_work_trace.args.request as Record<string, any>
  expect(Object.keys(published.$defs).sort()).toEqual(Object.keys($defs ?? {}).sort())
  expect(output.parameters).toEqual({ untouched: true })
  expect(output.description).toBe(plugin.tool.concord_work_trace.description)
})

test("published work start schemas cannot mutate runtime validation", async () => {
  const plugin = await ConcordAdapterPlugin()
  const output: { description: string; parameters: Record<string, unknown>; jsonSchema: Record<string, any> } = {
    description: "work start", parameters: {}, jsonSchema: { properties: plugin.tool.concord_work_start.args },
  }
  await plugin["tool.definition"]({ toolID: "concord_work_start" }, output)
  const argsTitle = plugin.tool.concord_work_start.args.title
  const publishedTitle = output.jsonSchema.else.else.properties.title
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
  lane_digest: "sha256:" + "b".repeat(64),
  work_id: "work-plugin",
  step_id: "step-1",
  inputs: { task: "do the bounded thing", binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" }, context: "", constraints: [] },
}

describe("plugin entry registers the dispatch window hook", () => {
  test("binds the recorded packet onto the next matching lane task call", async () => {
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
    const output = { args: { subagent_type: "concord-implement", prompt: "attempt-plugin", description: "bounded worker" } }
    await plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-plugin", callID: "call-1" }, output)

    expect(output.args.subagent_type).toBe("concord-implement")
    expect(JSON.parse(output.args.prompt).attempt_id).toBe("attempt-plugin")
    expect(output.args).not.toHaveProperty("task_id")
    expect(dispatchWindows().has("session-plugin")).toBe(false)
  })

  test("refuses an unauthorized lane task call and leaves other tools untouched", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
      "tool.execute.before": (i: { tool: string; sessionID: string; callID: string }, o: { args: any }) => Promise<void>
    }
    hostControlPlane().bind({
      get: async () => ({ data: { id: "session-none", metadata: { "concord.task_scope": "managed" } }, response: new Response(null, { status: 200 }) }),
      post: async () => { throw new Error("Task admission cannot write host state") },
    })
    const output = { args: { subagent_type: "concord-implement", prompt: "unbound" } }
    await expect(
      plugin["tool.execute.before"]({ tool: TASK_TOOL_ID, sessionID: "session-none", callID: "call-2" }, output),
    ).rejects.toThrow(/dispatch_worker/i)

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

      const output = { args: { subagent_type: "concord-implement", prompt: "attempt-plugin", description: "bounded worker" } }
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
    const callerArgs = () => ({ subagent_type: "concord-implement", prompt: "attempt-plugin", description: "bounded worker" })
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
      const output = { args: { subagent_type: "concord-implement", prompt: "attempt-plugin", description: "bounded worker" } }
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
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("https://example.invalid") })

    await hostControlPlane().moveSession("session-1", "/w")
    expect(seen).toEqual([{ url: MOVE_SESSION_ROUTE, body: { sessionID: "session-1", destination: { directory: "/w" } } }])
    expect(await hostControlPlane().sessionDirectory("session-1")).toBe("/w")
  })

  test("refuses the route when the host supplied no client", async () => {
    await ConcordAdapterPlugin({ serverUrl: new URL("https://example.invalid") })
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
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("https://example.invalid") })
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
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("https://example.invalid") })
    await expect(hostControlPlane().sessionDirectory("session-1")).rejects.toThrow(
      /session-1 is unknown to this host/,
    )
  })

  test("says so plainly when the host supplied no refusal at all", async () => {
    const raw = {
      get: async () => ({ response: new Response(null, { status: 500 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("https://example.invalid") })
    await expect(hostControlPlane().sessionDirectory("session-1")).rejects.toThrow(/no readable refusal/)
  })
})

// The control plane is module-shared state. A test that binds a fake client
// and leaves it bound changes what every later test file sees when the runner
// shares one process, so every binding is undone as soon as its test ends.
afterEach(() => {
  dispatchWindows().close("session-plugin")
  dispatchWindows().takeInFlight("session-plugin")
  hostControlPlane().bind(undefined)
  configureCoreBinary(null)
  resetTurnMoveBoundaries()
  for (const directory of testDirectories.splice(0)) fs.rmSync(directory, { recursive: true, force: true })
})

// The factory claims a host lease at load, which fails against the unstamped
// repository placeholder and closes the adapter transport. Later test files
// share this process, so the factory tests leave the lease state clean.
afterAll(() => {
  expect(dispatchWindows().inFlightAttempt("session-plugin")).toBeNull()
  configureHostLease({ reset: true })
})
