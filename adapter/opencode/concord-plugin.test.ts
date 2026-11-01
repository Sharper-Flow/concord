import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { sessionDirectoryReader, SESSION_ROUTE } from "./session-directory"
import { publishedRequestSchema } from "./concord"
import { contractOperations, hostToolSchemas } from "./generated-contracts"
import { configureCoreBinary } from "./core-runtime"


test("the plugin registration set equals the complete host tool catalog", async () => {
  // Every registered tool must be a catalog member, so the generated
  // utility denials derived from the catalog can never miss a new
  // registration.
  const plugin = await ConcordAdapterPlugin()
  const catalog = [...new Set([...Object.keys(hostToolSchemas), ...contractOperations.map((operation: any) => operation.tool)])].sort()
  expect(catalog.length).toBe(8)
  expect(Object.keys(plugin.tool).sort()).toEqual(catalog)
})

test("the system transform keeps agent-switch notices without reading host continuity", async () => {
  let reads = 0
  const raw = {
    get: async () => {
      reads += 1
      return { data: { directory: "/session/project" }, response: new Response(null, { status: 200 }) }
    },
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  }
  const plugin = await ConcordAdapterPlugin({ client: { _client: raw } as never })
  const input = { sessionID: "session-system-transform" }
  const transform = plugin["experimental.chat.system.transform"]
  const unchanged = { system: ["system prefix"] }
  await transform(input, unchanged)
  expect(unchanged.system).toEqual(["system prefix"])

  await plugin["chat.message"]({ ...input, agent: "agent-first" })
  await plugin["chat.message"]({ ...input, agent: "agent-second" })
  const switched = { system: ["system prefix"] }
  await transform(input, switched)
  expect(switched.system[0]).toContain("The active agent changed from `agent-first` to `agent-second`.")
  expect(switched.system[0]).not.toContain("<!-- concord:continuity:v1 -->")
  expect(reads).toBe(0)
})

test("the plugin leaves lifecycle, question, and assistant text handling to the host", async () => {
  const plugin = await ConcordAdapterPlugin()
  for (const key of ["event", "tool.execute.before", "tool.execute.after", "experimental.text.complete", "experimental.session.compacting"]) {
    expect(Reflect.get(plugin, key), key).toBeUndefined()
  }
})

test("the definition hook leaves tools no hook owns unchanged", async () => {
  const plugin = await ConcordAdapterPlugin()
  const output = { description: "another tool", parameters: {}, jsonSchema: { type: "object" } }
  const schema = output.jsonSchema
  const hook = Reflect.get(plugin, "tool.definition")
  expect(typeof hook).toBe("function")
  // "bash" is no Concord tool: no Concord definition hook may change it.
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

test("published request schemas cannot mutate the registered arguments or generated operations", async () => {
  const plugin = await ConcordAdapterPlugin()
  const output: { description: string; parameters: Record<string, unknown>; jsonSchema: Record<string, any> } = {
    description: "work define", parameters: {}, jsonSchema: { properties: plugin.tool.concord_work_define.args },
  }
  await plugin["tool.definition"]({ toolID: "concord_work_define" }, output)
  const argsOperations = (plugin.tool.concord_work_define.args.request as any).properties.operation.enum as string[]
  const publishedOperations = output.jsonSchema.properties.request.properties.operation.enum as string[]
  const expected = contractOperations.filter((operation) => operation.tool === "concord_work_define").map((operation) => operation.id.slice("concord_work_define.".length))
  const original = [...argsOperations]
  try {
    argsOperations.push("registration-only")
    expect(publishedOperations).toEqual(expected)
    publishedOperations.push("publication-only")
    expect(argsOperations).toEqual([...expected, "registration-only"])
    expect((publishedRequestSchema("concord_work_define") as any).properties.operation.enum).toEqual(expected)
    expect(contractOperations.filter((operation) => operation.tool === "concord_work_define").map((operation) => operation.id.slice("concord_work_define.".length))).toEqual(expected)
  } finally {
    argsOperations.splice(0, argsOperations.length, ...original)
  }
})

describe("plugin entry binds the directory reader to the host client", () => {
  test("reads the actual session directory through the client the host supplied", async () => {
    const seen: Array<{ url: string; path?: Record<string, unknown> }> = []
    const raw = {
      get: async ({ url, path }: { url: string; path?: Record<string, unknown> }) => {
        seen.push({ url, path })
        return { data: { directory: "/w" }, response: new Response(null, { status: 200 }) }
      },
      post: async () => { throw new Error("a directory read must not write") },
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("https://example.invalid") })

    expect(await sessionDirectoryReader().sessionDirectory("session-1")).toBe("/w")
    expect(seen).toEqual([{ url: SESSION_ROUTE, path: { id: "session-1" } }])
  })

  test("refuses the route when the host supplied no client", async () => {
    await ConcordAdapterPlugin({ serverUrl: new URL("https://example.invalid") })
    // A server URL is present and still yields no route: the URL is not what
    // the adapter needs, so its presence must not read as a usable transport.
    await expect(sessionDirectoryReader().sessionDirectory("session-1")).rejects.toThrow(/handed the plugin no client/)
  })
})

// The client parses the refusal body before it returns, so the parsed value on
// the result is the only readable copy. A refusal that reads the response a
// second time reports that the host said nothing, and the operator loses the
// one sentence that says why the session read failed.
describe("a refusal carries the host's own words", () => {
  // A body the client already consumed. Reading it again throws, which is what
  // a real host response does once the SDK client has parsed it.
  const consumed = (status: number, payload: unknown): Response => {
    const response = new Response(JSON.stringify(payload), { status })
    void response.text()
    return response
  }

  test("reports the host message when the session readback is refused", async () => {
    const raw = {
      get: async () => ({
        data: { message: "session-1 is unknown to this host" },
        response: consumed(404, { message: "session-1 is unknown to this host" }),
      }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("https://example.invalid") })
    await expect(sessionDirectoryReader().sessionDirectory("session-1")).rejects.toThrow(
      /session-1 is unknown to this host/,
    )
  })

  test("says so plainly when the host supplied no refusal at all", async () => {
    const raw = {
      get: async () => ({ response: new Response(null, { status: 500 }) }),
      post: async () => ({ response: new Response(null, { status: 204 }) }),
    }
    await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("https://example.invalid") })
    await expect(sessionDirectoryReader().sessionDirectory("session-1")).rejects.toThrow(/no readable refusal/)
  })
})

// The directory reader is module-shared state. A test that binds a fake client
// and leaves it bound changes what every later test file sees when the runner
// shares one process, so every binding is undone as soon as its test ends.
afterEach(() => {
  sessionDirectoryReader().bind(undefined)
  configureCoreBinary(null)
})

// The factory claims a host lease at load, which fails against the unstamped
// repository placeholder and closes the adapter transport. Later test files
// share this process, so the factory tests leave the lease state clean.
afterAll(() => {
  configureHostLease({ reset: true })
})
