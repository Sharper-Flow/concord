import { afterAll, afterEach, describe, expect, test } from "bun:test"
import nodeFs from "node:fs"
import os from "node:os"
import path from "node:path"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { hostControlPlane, MOVE_SESSION_ROUTE, MoveSessionUnavailable } from "./move-session"
import { enqueueWorkNotice } from "./concord"
import { contractOperations, hostToolSchemas, manifestDigest } from "./generated-contracts"
import { expandedPublishedRequestSchema } from "./generated-contract-tests"
import { bindCiWatchClient, ciWatchSettled, configureCiWatch, drainQueuedCiReports, type CiWatchPeerRead, type VerbSpawner } from "./ci-watch"
import { configureCoreBinary } from "./core-runtime"
import { armTurnMoveBoundary, questionRequiresNormalChat, resetTurnMoveBoundaries } from "./turn-move-boundary"
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
  expect(published).toEqual(hostToolSchemas.concord_ci_watch)
  expect(published.required).toEqual(["repo", "selector"])
  expect(published.additionalProperties).toBe(false)
  expect(Object.keys(published.properties).sort()).toEqual(["head_sha", "mode", "repo", "selector", "time_seconds_max"])
  expect(output.description).toBe(plugin.tool.concord_ci_watch.description)
})

test("the plugin registration set equals the complete host tool catalog", async () => {
  // Every registered tool must be a catalog member, so the generated
  // utility denials derived from the catalog can never miss a new
  // registration.
  const plugin = await ConcordAdapterPlugin()
  const catalog = [...new Set([...Object.keys(hostToolSchemas), ...contractOperations.map((operation: any) => operation.tool)])].sort()
  expect(catalog.length).toBe(11)
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

// The ci-watch start notice is a noReply user message the host still reports
// through chat.message. The hook must return early for it: a drained report
// would land in a message that never wakes the model, and the boundary clear
// would treat the notice as an operator turn. These tests queue a real report
// and prove the notice message consumes nothing.
describe("plugin entry isolates the ci-watch notice from chat.message", () => {
  const NOTICE_SESSION = "notice-session"

  const watchFixture = (options: { promptStatus?: number; confirmReply?: boolean } = {}) => {
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
          // confirmReply proves the delivered path: an assistant reply the host
          // parents to the injected report confirms on the first poll.
          if (options.confirmReply === true) {
            injected.push({ info: { id: `msg_reply_${promptCount}`, role: "assistant", parentID: id }, parts: [{ type: "text", text: "report received" }] })
          }
          // A 400 fails delivery outright, so the report queues without
          // waiting out the confirmation window.
          return { response: new Response(null, { status: options.promptStatus ?? 204 }) }
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
        { sessionID: NOTICE_SESSION, agent: "Concord", abort: new AbortController().signal },
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

  // Discover the factory's port through the global symbol alone.
  const PEER_SYMBOL = Symbol.for("concord.ci-watch.peer.v1")
  type PeerPort = { version: number; read: (request: unknown) => Promise<CiWatchPeerRead> }
  const peerGlobals = globalThis as typeof globalThis & { [PEER_SYMBOL]?: PeerPort }
  const peerPort = (): PeerPort | undefined => peerGlobals[PEER_SYMBOL]
  const requirePeerPort = (): PeerPort => {
    const port = peerPort()
    if (port === undefined) throw new Error("the plugin factory published no ci-watch peer port on its global symbol")
    return port
  }
  const requireDispose = (plugin: object): () => Promise<void> => {
    const dispose = Reflect.get(plugin, "dispose")
    if (typeof dispose !== "function") throw new Error("the plugin factory returned no dispose hook for its peer binding")
    return dispose as () => Promise<void>
  }
  function deferred<T>() {
    let resolve!: (value: T) => void
    const promise = new Promise<T>((done) => { resolve = done })
    return { promise, resolve }
  }
  type VerbResult = { exitCode: number; stdout: string; stderr: string }
  const TERMINAL_SHA = "0123456789abcdef0123456789abcdef01234567"
  const SUCCESS_REPORT = { status: "success", sha: TERMINAL_SHA, head_sha: TERMINAL_SHA, checks: { total: 1, passing: 1, failing: 0, pending: 0, skipped: 0 } }
  // The factory claims a host lease before any tool runs, so the tests bind a
  // fake runner whose lease names the synthetic release and this adapter's own
  // contract digest; the real core binary is never executed.
  const configureSyntheticLease = () => {
    configureHostLease({
      release: { coreBinary: "/synthetic/concord", releaseRoot: "/synthetic/release" },
      runner: {
        async run() {
          return { exitCode: 0, stdout: JSON.stringify({ release_root: "/synthetic/release", core_binary: "/synthetic/concord", manifest_digest: manifestDigest }), stderr: "" }
        },
      },
    })
  }
  const peerPlugin = async (client: unknown, serverUrl: URL, directory: string) =>
    ConcordAdapterPlugin({ client: { _client: client } as never, serverUrl, directory, worktree: directory })

  test("the factory publishes the peer port and stamps its binding onto the watches it admits", async () => {
    expect(peerPort()).toBeUndefined()
    const fixture = watchFixture({ confirmReply: true })
    configureCiWatch({ stateDir: fs.mkdtempSync(path.join(os.tmpdir(), "plugin-peer-read-")), idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
    let spawns = 0
    configureCiWatch({ spawner: async () => { spawns++; return { exitCode: 0, stdout: JSON.stringify({ status: "success" }), stderr: "" } } })
    configureCoreBinary("/synthetic/concord")
    configureSyntheticLease()
    const serverUrl = new URL("http://peer-a.invalid:4097/")
    const directory = "/synthetic/plugin-project"
    const plugin = await peerPlugin(fixture.client, serverUrl, directory)
    const port = requirePeerPort()
    expect(port.version).toBe(1)
    const baseline = await port.read({ session_id: NOTICE_SESSION })
    expect(baseline.coverage).toBe("complete_current_epoch")
    expect(baseline.client_bound).toBe(true)
    expect(baseline.gaps).toContain("prior_epoch_unverified")
    expect(baseline.snapshot!.active).toEqual([])
    expect(baseline.bindings).toHaveLength(1)
    expect(baseline.bindings[0]!.directory).toBe(directory)
    expect(baseline.bindings[0]!.endpoint).toBe(serverUrl.href)
    // The read consumed no delivery writes and spawned nothing.
    expect(fixture.posts).toHaveLength(0)
    expect(spawns).toBe(0)
    const bindingID = baseline.bindings[0]!.id
    const started = JSON.parse(
      (await plugin.tool.concord_ci_watch.execute!(
        { repo: "owner/name", selector: { kind: "pr", value: "12" } },
        { sessionID: NOTICE_SESSION, agent: "Concord", abort: new AbortController().signal },
      )).output,
    ) as Record<string, unknown>
    try {
      expect(started.status).toBe("started")
      await ciWatchSettled(String(started.watch_id))
      const settled = await port.read({ session_id: NOTICE_SESSION })
      expect(settled.coverage).toBe("complete_current_epoch")
      expect(settled.snapshot!.active).toEqual([])
      const receipt = settled.snapshot!.receipts[0]!
      expect(receipt.binding_id).toBe(bindingID)
      expect(receipt.delivery_state).toBe("delivered")
      expect(settled.bindings.map((entry) => entry.id)).toContain(bindingID)
    } finally {
      await ciWatchSettled(String(started.watch_id))
      drainQueuedCiReports(NOTICE_SESSION, "plugin-peer-delivered-drain", { parts: [] })
    }
  })

  test("the peer port observes a held watch and then carries the terminal receipt with exact-head evidence", async () => {
    const fixture = watchFixture({ promptStatus: 400 })
    configureCiWatch({ stateDir: fs.mkdtempSync(path.join(os.tmpdir(), "plugin-peer-observe-")), idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
    configureCoreBinary("/synthetic/concord")
    configureSyntheticLease()
    const plugin = await peerPlugin(fixture.client, new URL("http://peer-a.invalid:4097/"), "/synthetic/plugin-project")
    const port = requirePeerPort()
    const baseline = await port.read({ session_id: NOTICE_SESSION })
    const bindingID = baseline.bindings[0]!.id
    const held = deferred<VerbResult>()
    configureCiWatch({ spawner: () => held.promise })
    const started = JSON.parse(
      (await plugin.tool.concord_ci_watch.execute!(
        { repo: "owner/name", selector: { kind: "pr", value: "12" } },
        { sessionID: NOTICE_SESSION, agent: "Concord", abort: new AbortController().signal },
      )).output,
    ) as Record<string, unknown>
    try {
      expect(started.status).toBe("started")
      const observing = await port.read({ session_id: NOTICE_SESSION })
      expect(observing.snapshot!.active).toHaveLength(1)
      expect(observing.snapshot!.active[0]!.phase).toBe("observing")
      expect(observing.snapshot!.active[0]!.binding_id).toBe(bindingID)
      expect(observing.snapshot!.active[0]!.last_observation).toBeNull()
      held.resolve({ exitCode: 0, stdout: JSON.stringify(SUCCESS_REPORT), stderr: "" })
      await ciWatchSettled(String(started.watch_id))
      const settled = await port.read({ session_id: NOTICE_SESSION })
      expect(settled.snapshot!.active).toHaveLength(1)
      expect(settled.snapshot!.active[0]!.phase).toBe("queued")
      expect(settled.snapshot!.active[0]!.binding_id).toBe(bindingID)
      const receipt = settled.snapshot!.receipts[0]!
      expect(receipt.binding_id).toBe(bindingID)
      expect(receipt.delivery_state).toBe("queued")
      expect(receipt.terminal_observation).toMatchObject({ sha: TERMINAL_SHA, evidence: "exact_head", stale: false })
    } finally {
      held.resolve({ exitCode: 0, stdout: JSON.stringify(SUCCESS_REPORT), stderr: "" })
      await ciWatchSettled(String(started.watch_id))
      drainQueuedCiReports(NOTICE_SESSION, "plugin-peer-observe-drain", { parts: [] })
    }
  })

  test("plugin dispose invalidates the peer port for its binding without canceling the running watch", async () => {
    const fixture = watchFixture({ promptStatus: 400 })
    configureCiWatch({ stateDir: fs.mkdtempSync(path.join(os.tmpdir(), "plugin-peer-dispose-")), idlePollMs: 2, confirmPollMs: 2, confirmWindowMs: 20 })
    configureCoreBinary("/synthetic/concord")
    configureSyntheticLease()
    const plugin = await peerPlugin(fixture.client, new URL("http://peer-a.invalid:4097/"), "/synthetic/plugin-project")
    const port = requirePeerPort()
    const baseline = await port.read({ session_id: NOTICE_SESSION })
    const generationBefore = baseline.binding_generation
    const bindingID = baseline.bindings[0]!.id
    const held = deferred<VerbResult>()
    configureCiWatch({ spawner: () => held.promise })
    const started = JSON.parse(
      (await plugin.tool.concord_ci_watch.execute!(
        { repo: "owner/name", selector: { kind: "pr", value: "12" } },
        { sessionID: NOTICE_SESSION, agent: "Concord", abort: new AbortController().signal },
      )).output,
    ) as Record<string, unknown>
    try {
      expect(started.status).toBe("started")
      const dispose = requireDispose(plugin)
      await dispose()
      const invalidated = await port.read({ session_id: NOTICE_SESSION })
      expect(invalidated.coverage).toBe("unknown")
      expect(invalidated.gaps).toContain("disposed_watch")
      expect(invalidated.gaps).toContain("no_live_binding")
      expect(invalidated.binding_generation).toBeGreaterThan(generationBefore)
      expect(invalidated.snapshot!.active[0]!.binding_id).toBe(bindingID)
      // Dispose kills the binding, not the watch: the held slice still
      // resolves and the loop runs to its queued terminal report.
      held.resolve({ exitCode: 0, stdout: JSON.stringify(SUCCESS_REPORT), stderr: "" })
      await ciWatchSettled(String(started.watch_id))
      const queued = await port.read({ session_id: NOTICE_SESSION })
      expect(queued.coverage).toBe("unknown")
      expect(queued.snapshot!.active[0]!.phase).toBe("queued")
      expect(queued.snapshot!.active[0]!.binding_id).toBe(bindingID)
      expect(queued.snapshot!.receipts[0]!.delivery_state).toBe("queued")
    } finally {
      held.resolve({ exitCode: 0, stdout: JSON.stringify(SUCCESS_REPORT), stderr: "" })
      await ciWatchSettled(String(started.watch_id))
      drainQueuedCiReports(NOTICE_SESSION, "plugin-peer-dispose-drain", { parts: [] })
    }
  })

  test("a second same-endpoint factory keeps an empty session provable after the first binding disposes", async () => {
    const fixture = watchFixture()
    configureCoreBinary("/synthetic/concord")
    configureSyntheticLease()
    const serverUrl = new URL("http://peer-a.invalid:4097/")
    const directory = "/synthetic/plugin-project"
    const first = await peerPlugin(fixture.client, serverUrl, directory)
    const second = await peerPlugin(fixture.client, serverUrl, directory)
    const port = requirePeerPort()
    expect((await port.read({ session_id: NOTICE_SESSION })).coverage).toBe("complete_current_epoch")
    await requireDispose(first)()
    const after = await port.read({ session_id: NOTICE_SESSION })
    expect(after.coverage).toBe("complete_current_epoch")
    expect(after.snapshot!.active).toEqual([])
    expect(after.bindings).toHaveLength(1)
    // With no live binding left, the session absence is no longer provable.
    await requireDispose(second)()
    expect((await port.read({ session_id: NOTICE_SESSION })).coverage).toBe("unknown")
  })

  test("repeated dispose is idempotent and the binding generation stays stable", async () => {
    const fixture = watchFixture()
    configureCoreBinary("/synthetic/concord")
    configureSyntheticLease()
    const plugin = await peerPlugin(fixture.client, new URL("http://peer-a.invalid:4097/"), "/synthetic/plugin-project")
    const port = requirePeerPort()
    const before = await port.read({ session_id: NOTICE_SESSION })
    const dispose = requireDispose(plugin)
    await dispose()
    const afterFirst = await port.read({ session_id: NOTICE_SESSION })
    expect(afterFirst.coverage).toBe("unknown")
    expect(afterFirst.binding_generation).toBeGreaterThan(before.binding_generation)
    await dispose()
    const afterSecond = await port.read({ session_id: NOTICE_SESSION })
    expect(afterSecond.binding_generation).toBe(afterFirst.binding_generation)
    expect(afterSecond.coverage).toBe("unknown")
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
  for (const directory of testDirectories.splice(0)) fs.rmSync(directory, { recursive: true, force: true })
})

// The factory claims a host lease at load, which fails against the unstamped
// repository placeholder and closes the adapter transport. Later test files
// share this process, so the factory tests leave the lease state clean.
afterAll(() => {
  configureHostLease({ reset: true })
})
