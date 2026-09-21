// CD-0102 D2: the plugin entry must register the hook that binds an authorized
// dispatch to the next native Task call. Without the registration the window is
// unreachable and any Task call the model composes runs unbound.
import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { dispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { hostControlPlane, MOVE_SESSION_ROUTE, MoveSessionUnavailable } from "./move-session"
import { enqueueWorkNotice } from "./concord"
import { hostToolSchemas } from "./generated-contracts"

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
    enqueueWorkNotice("session-text", "```\n◆◆◆ QUEST COMPLETE ◆◆◆\nwork-1 | Concord\nRepair the adapter\nlifecycle=completed | evidence=none\n```")
    const output = { text: "assistant text" }
    await hook({ sessionID: "session-text", messageID: "message-1", partID: "part-1" }, output)
    expect(output.text).toBe([
      "assistant text",
      "```\n◆◆◆ QUEST COMPLETE ◆◆◆\nwork-1 | Concord\nRepair the adapter\nlifecycle=completed | evidence=none\n```",
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
    enqueueWorkNotice("session-throw", "```\n◆◆◆ QUEST COMPLETE ◆◆◆\nwork-1 | Concord\nRepair the adapter\nlifecycle=completed | evidence=none\n```")
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
  inputs: { task: "do the bounded thing", context: "", constraints: [] },
}

describe("plugin entry registers the dispatch window hook", () => {
  test("binds the recorded packet onto the next task call", async () => {
    const plugin = (await ConcordAdapterPlugin()) as {
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
    await expect(hostControlPlane().moveSession("session-1", "/w")).rejects.toThrow(/handed the plugin no client/)
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

// The control plane is module-shared state. A test that binds a fake client
// and leaves it bound changes what every later test file sees when the runner
// shares one process, so every binding is undone as soon as its test ends.
afterEach(() => hostControlPlane().bind(undefined))

// The factory claims a host lease at load, which fails against the unstamped
// repository placeholder and closes the adapter transport. Later test files
// share this process, so the factory tests leave the lease state clean.
afterAll(() => configureHostLease({ reset: true }))
