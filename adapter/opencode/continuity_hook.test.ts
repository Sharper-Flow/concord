import { expect, test } from "bun:test"
import { createContinuityTransform } from "./continuity-hook"

const PRODUCT_ENV = "CONCORD_SELECTED_PRODUCT_ID"
const WORK_ENV = "CONCORD_SELECTED_WORK_ID"
const START = "<!-- concord:continuity:v1 -->"
const END = "<!-- /concord:continuity:v1 -->"
const pluginSource = await Bun.file(new URL("./concord-plugin.ts", import.meta.url)).text()

type Transform = (input: unknown, output: { system: string[] }) => Promise<void>

async function withIdentity<T>(fn: () => Promise<T>, product = "product-1", work = "work-1") {
  const previousProduct = process.env[PRODUCT_ENV]
  const previousWork = process.env[WORK_ENV]
  process.env[PRODUCT_ENV] = product
  process.env[WORK_ENV] = work
  try {
    return await fn()
  } finally {
    if (previousProduct === undefined) delete process.env[PRODUCT_ENV]
    else process.env[PRODUCT_ENV] = previousProduct
    if (previousWork === undefined) delete process.env[WORK_ENV]
    else process.env[WORK_ENV] = previousWork
  }
}

function output(system: string): { system: string[] } {
  return { system: [system] }
}

test("plugin entry registers the continuity system transform", () => {
  expect(pluginSource).toContain('"experimental.chat.system.transform": createContinuityTransform()')
})

test("continuity transform keeps identical cached output byte-stable", async () => {
  await withIdentity(async () => {
    let calls = 0
    const transform = createContinuityTransform({
      now: () => 1_000,
      runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "fixture", stderr: "" } } },
    }) as Transform
    const first = output("system prefix")
    const second = output("system prefix")

    await transform({}, first)
    await transform({}, second)

    expect(first.system[0]).toBe(second.system[0])
    expect(calls).toBe(1)
  })
})

test("continuity transform carries pending messages from core output", async () => {
  await withIdentity(async () => {
    const packet = '{"continuity":{"pending_messages":1}}'
    const transformed = output("system prefix")
    const transform = createContinuityTransform({
      runner: { run: async () => ({ exitCode: 0, stdout: packet, stderr: "" }) },
    }) as Transform

    await transform({}, transformed)

    expect(transformed.system[0]).toContain(`${START}\n${packet}\n${END}`)
  })
})

test("continuity transform replaces a sentinel block instead of appending", async () => {
  await withIdentity(async () => {
    let now = 1_000
    let content = "old"
    let calls = 0
    const transform = createContinuityTransform({
      now: () => now,
      runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: content, stderr: "" } } },
    }) as Transform
    const first = output("system prefix")
    await transform({}, first)

    content = "new content"
    now += 10_001
    const previous = first.system[0]
    await transform({}, first)

    expect(first.system[0]).toContain(`${START}\nnew content\n${END}`)
    expect(first.system[0]).not.toContain(`${START}\nold\n${END}`)
    expect(first.system[0].indexOf(START)).toBe(first.system[0].lastIndexOf(START))
    expect(first.system[0].indexOf(END)).toBe(first.system[0].lastIndexOf(END))
    expect(first.system[0].length - previous.length).toBe("new content".length - "old".length)
    expect(calls).toBe(2)
  })
})

test("continuity transform leaves system bytes unchanged on failure, empty output, or absent identity", async () => {
  await withIdentity(async () => {
    const original = "prefix\n\nexact bytes"
    const failed = output(original)
    await (createContinuityTransform({ runner: { run: async () => { throw new Error("spawn failed") } } }) as Transform)({}, failed)
    expect(failed.system[0]).toBe(original)

    const nonzero = output(original)
    await (createContinuityTransform({ runner: { run: async () => ({ exitCode: 1, stdout: "unexpected", stderr: "failure" }) } }) as Transform)({}, nonzero)
    expect(nonzero.system[0]).toBe(original)

    const empty = output(original)
    await (createContinuityTransform({ runner: { run: async () => ({ exitCode: 0, stdout: "", stderr: "" }) } }) as Transform)({}, empty)
    expect(empty.system[0]).toBe(original)
  })

  const previousProduct = process.env[PRODUCT_ENV]
  const previousWork = process.env[WORK_ENV]
  delete process.env[PRODUCT_ENV]
  delete process.env[WORK_ENV]
  try {
    const unchanged = output("manual session bytes")
    let calls = 0
    await (createContinuityTransform({ runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "unexpected", stderr: "" } } } }) as Transform)({}, unchanged)
    expect(unchanged.system[0]).toBe("manual session bytes")
    expect(calls).toBe(0)
  } finally {
    if (previousProduct === undefined) delete process.env[PRODUCT_ENV]
    else process.env[PRODUCT_ENV] = previousProduct
    if (previousWork === undefined) delete process.env[WORK_ENV]
    else process.env[WORK_ENV] = previousWork
  }

  await withIdentity(async () => {
    const unchanged = output("empty work bytes")
    let calls = 0
    const previousWork = process.env[WORK_ENV]
    delete process.env[WORK_ENV]
    try {
      await (createContinuityTransform({ runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "unexpected", stderr: "" } } } }) as Transform)({}, unchanged)
      expect(unchanged.system[0]).toBe("empty work bytes")
      expect(calls).toBe(0)
    } finally {
      if (previousWork === undefined) delete process.env[WORK_ENV]
      else process.env[WORK_ENV] = previousWork
    }
  })
})

test("continuity transform gates spawns by identity for ten seconds", async () => {
  await withIdentity(async () => {
    let now = 5_000
    let calls = 0
    const transform = createContinuityTransform({
      now: () => now,
      runner: { run: async () => { calls += 1; return { exitCode: 0, stdout: "fixture", stderr: "" } } },
    }) as Transform
    await transform({}, output("one"))
    now += 9_999
    await transform({}, output("two"))
    expect(calls).toBe(1)
  })
})
