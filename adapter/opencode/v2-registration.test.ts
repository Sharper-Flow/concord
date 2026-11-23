import { expect, test } from "bun:test"
import plugin from "./concord-plugin"
import { contractOperations, manifestDigest } from "./generated-contracts"
import { product_view, configureConcordAdapter } from "./concord"
import { configureHostLease } from "./host-lease"
import { configureCoreBinary } from "./core-runtime"
import { fakeV2Context } from "./v2-context.fixture"

test("an unclaimed host lease refuses before reading the session or invoking core", async () => {
  configureHostLease({ reset: true })
  let reads = 0
  let calls = 0
  configureConcordAdapter({ runner: { async run() { calls++; throw new Error("unexpected core call") } } })
  try {
    const result = await product_view.execute({ request: { operation: "resolve", input: {} } }, {
      sessionID: "lease-test", messageID: "message", agent: "Concord", signal: new AbortController().signal,
      sessionDirectory: async () => { reads++; return "/synthetic" },
    })
    const envelope = JSON.parse(result.content)
    expect(envelope.error.adapter_reason).toBe("host_lease_missing")
    expect(reads).toBe(0)
    expect(calls).toBe(0)
  } finally {
    configureConcordAdapter({ reset: true })
    configureHostLease({ reset: true })
  }
})

test("the OpenCode entry exposes a v2 plugin object", () => {
  expect(typeof plugin).toBe("object")
  expect(typeof (plugin as any).setup).toBe("function")
})

test("setup registers the catalog through the v2 tool editor", async () => {
  const tools: Array<any> = []
  const hooks: string[] = []
  await plugin.setup({
    location: { directory: "/synthetic/project" },
    tool: { transform: async (transform: any) => transform({ add: (tool: any) => tools.push(tool) }) },
    session: { hook: async (name: string) => { hooks.push(name) } },
  } as any)

  expect(tools.map((tool) => tool.name).sort()).toEqual([...new Set(contractOperations.map((operation) => operation.tool))].sort())
  expect(tools).toHaveLength(7)
  for (const tool of tools) {
    expect(tool.options).toEqual({ codemode: false })
    expect(tool.namespace).toBeUndefined()
    expect(tool.input.required).toEqual(["request"])
    expect(typeof tool.execute).toBe("function")
  }
  expect(hooks).toEqual(["context"])
})

test("repeated setup claims once and registers independent tools", async () => {
  configureCoreBinary("concord")
  let claims = 0
  configureHostLease({ reset: true, release: { coreBinary: "concord", releaseRoot: "/releases/test" }, runner: { async run() {
    claims++
    return { exitCode: 0, stderr: "", stdout: JSON.stringify({ core_binary: "concord", release_root: "/releases/test", manifest_digest: manifestDigest }) }
  } } })
  try {
    const first = fakeV2Context()
    const second = fakeV2Context()
    await Promise.all([plugin.setup(first.context), plugin.setup(second.context)])
    expect(claims).toBe(1)
    expect(first.tools).toHaveLength(7)
    expect(second.tools).toHaveLength(7)
    expect(first.tools.map(tool => tool.name)).toEqual(second.tools.map(tool => tool.name))
    expect(first.tools[0]).not.toBe(second.tools[0])
    const original = [...second.tools[0].input.required]
    first.tools[0].input.required.push("contaminated")
    expect(second.tools[0].input.required).toEqual(original)
  } finally {
    configureHostLease({ reset: true })
    configureCoreBinary(null)
  }
})

test("agent notices do not leak between plugin setups", async () => {
  const first = fakeV2Context()
  const second = fakeV2Context()
  await plugin.setup(first.context)
  await plugin.setup(second.context)
  const one: any = { sessionID: "same-session", agent: "first", system: [] }
  const two: any = { sessionID: "same-session", agent: "second", system: [] }
  first.hooks[0].hook(one)
  second.hooks[0].hook(two)
  expect(two.system).toEqual([])
})

test("the context hook announces each agent switch once per session", async () => {
  let contextHook: any
  await plugin.setup({
    location: { directory: "/synthetic/project" },
    tool: { transform: async (transform: any) => transform({ add: () => {} }) },
    session: { hook: async (_name: string, hook: any) => { contextHook = hook } },
  } as any)
  const send = (sessionID: string, agent: string) => {
    const system: Array<{ type: "text"; text: string }> = [{ type: "text", text: "base" }]
    contextHook({ sessionID, agent, system })
    return system
  }
  expect(send("v2-switch", "first")).toHaveLength(1)
  expect(send("v2-other", "second")).toHaveLength(1)
  const switched = send("v2-switch", "second")
  expect(switched).toHaveLength(2)
  expect(switched[1].text).toContain("`first` to `second`")
  expect(send("v2-switch", "second")).toHaveLength(1)
  expect(send("v2-switch", "first")).toHaveLength(2)
})
