import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { contractOperations, hostToolSchemas } from "./generated-contracts"
import { fakeV2Context } from "./v2-context.fixture"

afterEach(() => configureHostLease({ reset: true }))

test("the plugin registration set equals the complete host tool catalog", async () => {
  const host = fakeV2Context()
  await ConcordAdapterPlugin.setup(host.context)
  const catalog = [...new Set([...Object.keys(hostToolSchemas), ...contractOperations.map(operation => operation.tool)])].sort()
  expect(catalog).toHaveLength(7)
  expect(host.tools.map(tool => tool.name).sort()).toEqual(catalog)
})

test("the registered request input publishes the closed root and its definitions", async () => {
  const host = fakeV2Context()
  await ConcordAdapterPlugin.setup(host.context)
  const schema = host.tools.find(tool => tool.name === "concord_work_trace").input
  expect(schema.type).toBe("object")
  expect(schema.required).toEqual(["request"])
  expect(Object.keys(schema.properties)).toEqual(["request"])
  expect(schema.$defs).toBeObject()
  expect(schema.properties.request.required).toEqual(["operation", "input"])
  expect(schema.properties.request.oneOf).toHaveLength(contractOperations.filter(operation => operation.tool === "concord_work_trace").length)
})

test("the context hook observes agent switches without reading the session", async () => {
  let reads = 0
  const host = fakeV2Context(async () => { reads++; throw new Error("unexpected session read") })
  await ConcordAdapterPlugin.setup(host.context)
  const hook = host.hooks.find(item => item.name === "context")!.hook
  const first = { sessionID: "session-switch", agent: "first", system: [] as any[] }
  const second = { sessionID: "session-switch", agent: "second", system: [] as any[] }
  hook(first)
  hook(second)
  expect(first.system).toEqual([])
  expect(second.system[0].text).toContain("`first` to `second`")
  expect(reads).toBe(0)
})
