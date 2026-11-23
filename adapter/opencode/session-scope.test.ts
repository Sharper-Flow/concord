import { afterEach, expect, test } from "bun:test"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureHostLease } from "./host-lease"
import { fakeV2Context } from "./v2-context.fixture"

afterEach(() => {
  configureHostLease({ reset: true })
})

test("native Task preserves arguments without reading or changing Concord participation", async () => {
  let reads = 0
  const host = fakeV2Context(async () => { reads++; throw new Error("Task must not read Concord participation") })
  await ConcordAdapterPlugin.setup(host.context)
  expect(reads).toBe(0)
  expect(host.tools.some(tool => tool.name.startsWith("concord_worker_"))).toBe(false)
})
