import { describe, expect, test } from "bun:test"
import { readdirSync, readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const directory = dirname(fileURLToPath(import.meta.url))
const self = "process_global_isolation.test.ts"
// Strip types and normalize function/hook layout without importing host modules.
const transpiler = new Bun.Transpiler({ loader: "ts", trimUnusedImports: false })
const files = readdirSync(directory).filter((name) => name.endsWith(".ts")).sort()
const sources = new Map(files.filter((name) => name !== self).map((name) => [
  name, transpiler.transformSync(readFileSync(join(directory, name), "utf8")),
]))
type FunctionSource = { name: string; body: string; module: string; isDefault: boolean }
const functions: FunctionSource[] = []
for (const [module, source] of sources) {
  if (module.endsWith(".test.ts") || module.endsWith(".case.ts")) continue
  for (const match of source.matchAll(/^(?:export\s+(default\s+)?)?(?:async\s+)?function\s+(\w+)\([^]*?^\}/gm)) {
    functions.push({ name: match[2]!, body: match[0], module, isDefault: Boolean(match[1]) })
  }
}
const escape = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
const code = (source: string) => source.replace(/"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`/g, "\"\"")
const calls = (body: string, name: string) => new RegExp(`\\b${escape(name)}\\s*\\(`).test(code(body))

function callers(initial: string[]): FunctionSource[] {
  const names = new Set(initial)
  let changed = true
  while (changed) {
    changed = false
    for (const fn of functions) {
      if (!names.has(fn.name) && [...names].some((name) => calls(fn.body, name))) {
        names.add(fn.name)
        changed = true
      }
    }
  }
  return functions.filter((fn) => names.has(fn.name))
}

function globalSlots(source: string, body = source, deletion = false): Array<{ key: string; target: string }> {
  const aliases = ["globalThis", ...[...source.matchAll(/\b(?:const|let|var)\s+(\w+)\s*=\s*globalThis\s*;/g)].map((match) => match[1]!)]
  const view = `(?:${aliases.map(escape).join("|")})`
  const symbols = new Map([...source.matchAll(/\b(?:const|let|var)\s+(\w+)\s*=\s*Symbol\.for\("([^"]+)"\)/g)].map((match) => [match[1]!, match[2]!]))
  const result: Array<{ key: string; target: string }> = []
  const prefix = deletion ? "\\bdelete\\s+" : "\\b"
  const suffix = deletion ? "" : "\\s*(?:\\?\\?|\\|\\||&&)?=(?!=|>)"
  for (const match of body.matchAll(new RegExp(`${prefix}${view}\\s*(\\[([^\\]\\n]+)\\]|\\.(\\w+))${suffix}`, "g"))) {
    const selector = match[1]!
    const expression = match[2]?.trim()
    const key = match[3] ?? symbols.get(expression ?? "") ?? expression?.match(/^(?:Symbol\.for\()?"([^"]+)"\)?$/)?.[1]
    if (!key) throw new Error(`unclassified process-global slot ${selector}`)
    result.push({ key, target: `${view}\\s*${escape(selector)}` })
  }
  return result
}

type Publication = { key: string; module: string; publishers: FunctionSource[]; resets: FunctionSource[] }
const publications: Publication[] = []
for (const [module, source] of sources) {
  if (module.endsWith(".test.ts") || module.endsWith(".case.ts")) continue
  for (const { key, target } of globalSlots(source)) {
    const slot = new RegExp(`\\b${target}\\s*(?:\\?\\?|\\|\\||&&)?=`)
    const deletion = new RegExp(`\\bdelete\\s+${target}`)
    const owned = functions.filter((fn) => fn.module === module)
    const direct = owned.filter((fn) => slot.test(fn.body)).map((fn) => fn.name)
    const resets = owned.filter((fn) => deletion.test(fn.body)).map((fn) => fn.name)
    if (direct.length === 0 || resets.length === 0) throw new Error(`${module}: ${key} needs identifiable publication and deletion owners`)
    publications.push({ key, module, publishers: callers(direct), resets: callers(resets) })
  }
}

function cleanupSource(source: string): string {
  // Bun normalizes block closures to their hook's indentation. Single-line
  // callbacks cover the expression form used by the host-lease model.
  return [...source.matchAll(/^([ \t]*)after(?:All|Each)\([^\n]*\{\n[^]*?^\1\}\);|^[ \t]*after(?:All|Each)\([^\n]+/gm)]
    .map((match) => match[0]).join("\n")
}

function publishes(source: string, publication: Publication): boolean {
  return publication.publishers.some((fn) => calls(source, fn.name) ||
    calls(source, source.match(new RegExp(`\\b${escape(fn.name)}\\s+as\\s+(\\w+)`))?.[1] ?? fn.name) ||
    fn.isDefault && new RegExp(`\\bimport\\s+\\w+\\s+from\\s+"\\./${escape(fn.module.replace(/\.ts$/, ""))}"`).test(source)) ||
    globalSlots(source).some((slot) => slot.key === publication.key)
}

function resets(source: string, publication: Publication): boolean {
  const cleanup = cleanupSource(source)
  return publication.resets.some((fn) => fn.name.startsWith("configure")
    ? new RegExp(`\\b${escape(fn.name)}\\s*\\(\\s*\\{[^}]*\\breset\\s*:\\s*true`).test(code(cleanup))
    : calls(cleanup, fn.name)) ||
    globalSlots(source, cleanup, true).some((slot) => slot.key === publication.key)
}

describe("adapter process-global publication has test-file cleanup", () => {
  test("the source-derived inventory and publisher coverage are not empty", () => {
    expect(publications.length).toBeGreaterThan(0)
    for (const publication of publications) {
      expect([...sources].some(([name, source]) => name.endsWith(".test.ts") && publishes(source, publication)), publication.key).toBe(true)
    }
  })
  for (const publication of publications) {
    test(`${publication.key} distinguishes publication and matching teardown from source mentions`, () => {
      const parse = (source: string) => transpiler.transformSync(source)
      const publisher = publication.publishers.find((fn) => fn.module === publication.module)!
      const reset = publication.resets.find((fn) => fn.name.startsWith("configure"))
      const invoke = `${publisher.name}()`
      expect(publishes(parse(`const mention = ${JSON.stringify(invoke)}`), publication)).toBe(false)
      expect(publishes(parse(`${invoke};`), publication)).toBe(true)
      expect(resets(parse(`${invoke};`), publication)).toBe(false)
      if (reset) {
        expect(resets(parse(`beforeEach(() => ${reset.name}({ reset: true })); ${invoke};`), publication)).toBe(false)
        expect(resets(parse(`afterAll(() => ${reset.name}({ reset: true })); ${invoke};`), publication)).toBe(true)
        expect(resets(parse(`afterAll(() => ${reset.name}()); ${invoke};`), publication)).toBe(false)
      }
      const direct = `const KEY = Symbol.for(${JSON.stringify(publication.key)}); const shared = globalThis; shared[KEY] = {};`
      expect(publishes(parse(direct), publication)).toBe(true)
      expect(resets(parse(`${direct} afterEach(() => { delete shared[KEY] });`), publication)).toBe(true)
      expect(resets(parse(`${direct} afterEach(() => { delete shared.unrelated });`), publication)).toBe(false)
    })
    for (const [name, source] of sources) {
      if (!name.endsWith(".test.ts") || !publishes(source, publication)) continue
      test(`${name} resets ${publication.key} (${publication.module})`, () => {
        expect(resets(source, publication),
          `${name} publishes process global ${publication.key} but has no afterEach/afterAll reset. ` +
          `Use the owning module's reset seam in teardown; disposing a binding does not retire the global slot.`,
        ).toBe(true)
      })
    }
  }
})
