import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { sessionDirectoryReader } from "./session-directory"

afterEach(() => {
  sessionDirectoryReader().bind(undefined)
  configureHostLease({ reset: true })
})

test("native Task preserves arguments without reading or changing Concord participation", async () => {
  let reads = 0
  const plugin = await ConcordAdapterPlugin({ client: { _client: {
    get: async () => { reads++; throw new Error("Task must not read Concord participation") },
  } } as never })
  expect(Reflect.get(plugin, "tool.execute.before")).toBeUndefined()
  expect(Reflect.get(plugin, "tool.execute.after")).toBeUndefined()
  expect(reads).toBe(0)
  expect(Object.keys(plugin.tool).some(name => name.startsWith("concord_worker_"))).toBe(false)
})
